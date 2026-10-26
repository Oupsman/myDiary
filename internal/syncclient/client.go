// Package syncclient talks to the sync server and implements the
// pull-merge-push algorithm.
package syncclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/oupsman/mydiary/internal/syncproto"
)

var (
	ErrNotFound     = errors.New("diary not on the server")
	ErrConflict     = errors.New("diary changed on the server")
	ErrUnauthorized = errors.New("the server refused the device token")
)

// Config is the sync configuration of this computer.
type Config struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
	// CAFile optionally names a PEM file with the certificate authority of
	// the reverse proxy, when it is not trusted system-wide.
	CAFile string `json:"ca_file,omitempty"`
}

// Enabled reports whether sync is configured.
func (c Config) Enabled() bool { return c.ServerURL != "" && c.Token != "" }

// Client is a sync server client.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// New creates a client from cfg.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(cfg.ServerURL, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("the server address must look like https://diary.example.lan")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificate found in %s", cfg.CAFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Client{base: u, token: cfg.Token, http: &http.Client{Transport: tr, Timeout: 2 * time.Minute}}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, hdr map[string]string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, ErrUnauthorized
	}
	return resp, nil
}

func httpError(resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("server error %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
}

// Ping checks the server address and token.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/v1/ping", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return httpError(resp)
	}
	return nil
}

// List returns the diaries stored on the server.
func (c *Client) List(ctx context.Context) ([]syncproto.DiaryInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/diaries", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}
	var out []syncproto.DiaryInfo
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// Get downloads a diary. If have is the current server version, it returns
// notModified without transferring the content.
func (c *Client) Get(ctx context.Context, id string, have int64) (data []byte, version int64, notModified bool, err error) {
	hdr := map[string]string{}
	if have > 0 {
		hdr["If-None-Match"] = syncproto.ETag(have)
	}
	resp, err := c.do(ctx, http.MethodGet, "/v1/diaries/"+id, nil, hdr)
	if err != nil {
		return nil, 0, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, have, true, nil
	case http.StatusNotFound:
		return nil, 0, false, ErrNotFound
	case http.StatusOK:
	default:
		return nil, 0, false, httpError(resp)
	}
	version, ok := syncproto.ParseETag(resp.Header.Get("ETag"))
	if !ok {
		return nil, 0, false, errors.New("server response has no version")
	}
	data, err = io.ReadAll(resp.Body)
	return data, version, false, err
}

// Put uploads a diary based on version base (0 to create it) and returns the
// new version, or ErrConflict if the server has moved on.
func (c *Client) Put(ctx context.Context, id string, base int64, label string, data []byte) (int64, error) {
	hdr := map[string]string{"Content-Type": "application/octet-stream", syncproto.LabelHeader: label}
	if base == 0 {
		hdr["If-None-Match"] = "*"
	} else {
		hdr["If-Match"] = syncproto.ETag(base)
	}
	resp, err := c.do(ctx, http.MethodPut, "/v1/diaries/"+id, data, hdr)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusPreconditionFailed:
		return 0, ErrConflict
	default:
		return 0, httpError(resp)
	}
	v, ok := syncproto.ParseETag(resp.Header.Get("ETag"))
	if !ok {
		return 0, errors.New("server response has no version")
	}
	return v, nil
}
