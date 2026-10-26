package syncserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oupsman/mydiary/internal/syncproto"
	"github.com/oupsman/mydiary/internal/vault"
)

var (
	ErrNotFound = errors.New("diary not found")
	ErrConflict = errors.New("version conflict")
)

// Store keeps diaries on disk. Each version is written to its own file and
// then the metadata is switched to it atomically, so a crash never pairs a
// version number with the wrong content.
//
//	<dir>/<id>.json             metadata (syncproto.DiaryInfo)
//	<dir>/<id>.<version>.diary  encrypted content
type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	if err := s.scrubLabels(); err != nil {
		return nil, err
	}
	return s, nil
}

// scrubLabels rewrites metadata written by older versions, which stored the
// diary file name as a label, so that no name is kept on the server.
func (s *Store) scrubLabels() error {
	names, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return err
	}
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			return err
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal(b, &raw) != nil {
			continue
		}
		if _, ok := raw["label"]; !ok {
			continue
		}
		delete(raw, "label")
		clean, _ := json.Marshal(raw)
		if err := vault.WriteFileAtomic(n, clean); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) metaPath(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) blobPath(id string, v int64) string {
	return filepath.Join(s.dir, fmt.Sprintf("%s.%d.diary", id, v))
}

func (s *Store) readMeta(id string) (syncproto.DiaryInfo, error) {
	var info syncproto.DiaryInfo
	b, err := os.ReadFile(s.metaPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return info, ErrNotFound
	}
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(b, &info)
	return info, err
}

// List returns all diaries.
func (s *Store) List() ([]syncproto.DiaryInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := []syncproto.DiaryInfo{}
	for _, n := range names {
		id := strings.TrimSuffix(filepath.Base(n), ".json")
		if !syncproto.ValidID(id) {
			continue
		}
		if info, err := s.readMeta(id); err == nil {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Get returns a diary's metadata and content.
func (s *Store) Get(id string) (syncproto.DiaryInfo, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := s.readMeta(id)
	if err != nil {
		return info, nil, err
	}
	data, err := os.ReadFile(s.blobPath(id, info.Version))
	return info, data, err
}

// Stat returns a diary's metadata.
func (s *Store) Stat(id string) (syncproto.DiaryInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readMeta(id)
}

// Put stores a new version. ifMatch is the version the client based its
// upload on, or 0 to create a diary that must not exist yet.
func (s *Store) Put(id string, ifMatch int64, data []byte) (syncproto.DiaryInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, err := s.readMeta(id)
	switch {
	case errors.Is(err, ErrNotFound):
		if ifMatch != 0 {
			return cur, ErrConflict
		}
	case err != nil:
		return cur, err
	case cur.Version != ifMatch:
		return cur, ErrConflict
	}

	info := syncproto.DiaryInfo{
		ID:      id,
		Version: cur.Version + 1,
		Size:    int64(len(data)),
		Updated: time.Now().UTC(),
	}
	if err := vault.WriteFileAtomic(s.blobPath(id, info.Version), data); err != nil {
		return cur, err
	}
	meta, _ := json.Marshal(info)
	if err := vault.WriteFileAtomic(s.metaPath(id), meta); err != nil {
		os.Remove(s.blobPath(id, info.Version))
		return cur, err
	}
	if cur.Version > 0 {
		os.Remove(s.blobPath(id, cur.Version))
	}
	return info, nil
}

// Delete removes a diary if it is still at version ifMatch.
func (s *Store) Delete(id string, ifMatch int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.readMeta(id)
	if err != nil {
		return err
	}
	if cur.Version != ifMatch {
		return ErrConflict
	}
	if err := os.Remove(s.metaPath(id)); err != nil {
		return err
	}
	os.Remove(s.blobPath(id, cur.Version))
	return nil
}
