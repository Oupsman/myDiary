package syncserver

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/oupsman/mydiary/internal/vault"
)

// Device is an authorised client of a user. Only a SHA-256 of its token is
// stored.
type Device struct {
	User    string    `json:"user"`
	Name    string    `json:"name"`
	Hash    string    `json:"hash"`
	Created time.Time `json:"created"`
}

// Tokens is the device list, reloaded automatically when the file changes so
// that adding or revoking a device needs no restart.
type Tokens struct {
	path string

	mu      sync.Mutex
	loaded  os.FileInfo // of the file devices was read from
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

// unchanged reports whether st is the file devices was read from. The file
// is replaced, never rewritten in place, so a new inode means new content even
// when the file system's timestamps are too coarse to tell.
func (t *Tokens) unchanged(st os.FileInfo) bool {
	return t.loaded != nil && os.SameFile(st, t.loaded) &&
		st.ModTime().Equal(t.loaded.ModTime()) && st.Size() == t.loaded.Size()
}

// Check returns the device owning tok, or false if none does.
func (t *Tokens) Check(tok string) (Device, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st, err := os.Stat(t.path); err == nil && !t.unchanged(st) {
		if ds, err := t.load(); err == nil {
			t.devices, t.loaded = ds, st
		}
	} else if errors.Is(err, os.ErrNotExist) {
		t.devices, t.loaded = nil, nil
	}
	if tok == "" {
		return Device{}, false
	}
	h := []byte(hashToken(tok))
	var found Device
	ok := false
	for _, d := range t.devices {
		// A device left without a user by an old version is refused until
		// "migrate" assigns one.
		if subtle.ConstantTimeCompare(h, []byte(d.Hash)) == 1 && d.User != "" {
			found, ok = d, true
		}
	}
	return found, ok
}

// Add creates a device for user and returns its token, which is shown only
// once.
func (t *Tokens) Add(user, name string) (string, error) {
	if !ValidUser(user) {
		return "", errInvalidUser
	}
	ds, err := t.load()
	if err != nil {
		return "", err
	}
	for _, d := range ds {
		if d.User == user && d.Name == name {
			return "", errors.New("this user already has a device with this name")
		}
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw[:])
	ds = append(ds, Device{User: user, Name: name, Hash: hashToken(tok), Created: time.Now().UTC()})
	return tok, t.save(ds)
}

// Revoke removes a device of user.
func (t *Tokens) Revoke(user, name string) error {
	ds, err := t.load()
	if err != nil {
		return err
	}
	for i, d := range ds {
		if d.User == user && d.Name == name {
			return t.save(append(ds[:i], ds[i+1:]...))
		}
	}
	return errors.New("no such device")
}

// List returns the devices.
func (t *Tokens) List() ([]Device, error) { return t.load() }

// RevokeUser removes all the devices of user and returns how many there were.
func (t *Tokens) RevokeUser(user string) (int, error) {
	ds, err := t.load()
	if err != nil {
		return 0, err
	}
	kept := ds[:0]
	for _, d := range ds {
		if d.User != user {
			kept = append(kept, d)
		}
	}
	n := len(ds) - len(kept)
	if n == 0 {
		return 0, nil
	}
	return n, t.save(kept)
}

// hasOrphans reports whether some device has no user (written by a
// single-user version).
func (t *Tokens) hasOrphans() (bool, error) {
	ds, err := t.load()
	for _, d := range ds {
		if d.User == "" {
			return true, err
		}
	}
	return false, err
}

// adoptOrphans gives the devices that have no user to user.
func (t *Tokens) adoptOrphans(user string) error {
	ds, err := t.load()
	if err != nil {
		return err
	}
	changed := false
	for i := range ds {
		if ds[i].User == "" {
			for _, d := range ds {
				if d.User == user && d.Name == ds[i].Name {
					return fmt.Errorf("%s already has a device named %q", user, d.Name)
				}
			}
			ds[i].User, changed = user, true
		}
	}
	if !changed {
		return nil
	}
	return t.save(ds)
}
