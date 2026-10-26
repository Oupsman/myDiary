// Package syncserver implements the diary sync server. It stores encrypted
// diary files as opaque blobs with optimistic concurrency; merging happens on
// the clients, which hold the keys.
package syncserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oupsman/mydiary/internal/syncproto"
)

// vaultMagic is the start of every encrypted diary file; anything else is
// refused, so a buggy client can never upload plaintext.
var vaultMagic = []byte("MYDIARY\x00")

// Server is the HTTP handler.
type Server struct {
	Store   *Store
	Tokens  *Tokens
	MaxSize int64 // maximum upload size in bytes
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/diaries", s.list)
	mux.HandleFunc("GET /v1/diaries/{id}", s.get)
	mux.HandleFunc("PUT /v1/diaries/{id}", s.put)
	mux.HandleFunc("DELETE /v1/diaries/{id}", s.del)
	return s.auth(mux)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		device := s.Tokens.Check(tok)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		if device == "" {
			// Slow down token guessing a little.
			time.Sleep(500 * time.Millisecond)
			http.Error(sw, "unauthorized", http.StatusUnauthorized)
		} else {
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(sw, r)
		}
		if device == "" {
			device = "-"
		}
		log.Printf("%s %s %s %d %s", device, r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func diaryID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !syncproto.ValidID(id) {
		http.Error(w, "invalid diary id", http.StatusBadRequest)
		return "", false
	}
	return id, true
}

func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrConflict):
		http.Error(w, "version conflict", http.StatusPreconditionFailed)
	default:
		log.Printf("store error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	infos, err := s.Store.List()
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, infos)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, ok := diaryID(w, r)
	if !ok {
		return
	}
	if v, ok := syncproto.ParseETag(r.Header.Get("If-None-Match")); ok {
		if info, err := s.Store.Stat(id); err == nil && info.Version == v {
			w.Header().Set("ETag", syncproto.ETag(v))
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	info, data, err := s.Store.Get(id)
	if err != nil {
		storeError(w, err)
		return
	}
	w.Header().Set("ETag", syncproto.ETag(info.Version))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

// precondition returns the version the request is based on: 0 for
// "If-None-Match: *" (create), or the If-Match version.
func precondition(w http.ResponseWriter, r *http.Request, allowCreate bool) (int64, bool) {
	if allowCreate && strings.TrimSpace(r.Header.Get("If-None-Match")) == "*" {
		return 0, true
	}
	v, ok := syncproto.ParseETag(r.Header.Get("If-Match"))
	if !ok {
		http.Error(w, "If-Match (or If-None-Match: *) required", http.StatusPreconditionRequired)
		return 0, false
	}
	return v, true
}

func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	id, ok := diaryID(w, r)
	if !ok {
		return
	}
	base, ok := precondition(w, r, true)
	if !ok {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.MaxSize))
	if err != nil {
		http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !bytes.HasPrefix(data, vaultMagic) {
		http.Error(w, "body is not an encrypted diary", http.StatusUnsupportedMediaType)
		return
	}
	info, err := s.Store.Put(id, base, data)
	if err != nil {
		if errors.Is(err, ErrConflict) && info.Version > 0 {
			w.Header().Set("ETag", syncproto.ETag(info.Version))
		}
		storeError(w, err)
		return
	}
	w.Header().Set("ETag", syncproto.ETag(info.Version))
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) del(w http.ResponseWriter, r *http.Request) {
	id, ok := diaryID(w, r)
	if !ok {
		return
	}
	base, ok := precondition(w, r, false)
	if !ok {
		return
	}
	if err := s.Store.Delete(id, base); err != nil {
		storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
