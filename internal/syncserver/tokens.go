package syncserver

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/oupsman/mydiary/internal/vault"
)

// Device is an authorised client. Only a SHA-256 of its token is stored.
type Device struct {
	Name    string    `json:"name"`
	Hash    string    `json:"hash"`
	Created time.Time `json:"created"`
}

// Tokens is the device list, reloaded automatically when the file changes so
// that adding or revoking a device needs no restart.
type Tokens struct {
	path string

	mu      sync.Mutex
	modTime time.Time
	devices []Device
}

func NewTokens(path string) *Tokens { return &Tokens{path: path} }

func (t *Tokens) load() ([]Device, error) {
	b, err := os.ReadFile(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ds []Device
	err = json.Unmarshal(b, &ds)
	return ds, err
}

func (t *Tokens) save(ds []Device) error {
	b, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return err
	}
	return vault.WriteFileAtomic(t.path, b)
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// Check returns the name of the device owning tok, or "" if none does.
func (t *Tokens) Check(tok string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st, err := os.Stat(t.path); err == nil && !st.ModTime().Equal(t.modTime) {
		if ds, err := t.load(); err == nil {
			t.devices, t.modTime = ds, st.ModTime()
		}
	} else if errors.Is(err, os.ErrNotExist) {
		t.devices = nil
	}
	if tok == "" {
		return ""
	}
	h := []byte(hashToken(tok))
	name := ""
	for _, d := range t.devices {
		if subtle.ConstantTimeCompare(h, []byte(d.Hash)) == 1 {
			name = d.Name
		}
	}
	return name
}

// Add creates a device and returns its token, which is shown only once.
func (t *Tokens) Add(name string) (string, error) {
	ds, err := t.load()
	if err != nil {
		return "", err
	}
	for _, d := range ds {
		if d.Name == name {
			return "", errors.New("a device with this name already exists")
		}
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw[:])
	ds = append(ds, Device{Name: name, Hash: hashToken(tok), Created: time.Now().UTC()})
	return tok, t.save(ds)
}

// Revoke removes a device.
func (t *Tokens) Revoke(name string) error {
	ds, err := t.load()
	if err != nil {
		return err
	}
	for i, d := range ds {
		if d.Name == name {
			return t.save(append(ds[:i], ds[i+1:]...))
		}
	}
	return errors.New("no such device")
}

// List returns the devices.
func (t *Tokens) List() ([]Device, error) { return t.load() }
