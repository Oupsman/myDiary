package syncserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	errInvalidUser = errors.New("invalid user name: use 1 to 32 lowercase letters, digits, '.', '_' or '-', starting with a letter or digit")
	// ErrLegacyLayout means the data directory was written by a single-user
	// version and must be given to a user with Migrate first.
	ErrLegacyLayout = errors.New("data from a single-user version found")
)

// ValidUser reports whether name is an acceptable user name. It is used as a
// directory name.
func ValidUser(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for i, c := range name {
		alnum := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || !strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return true
}

// Stores holds one Store per user. Users cannot see each other's diaries.
//
//	<dir>/<user>/   the user's Store
type Stores struct {
	dir string

	mu sync.Mutex
	m  map[string]*Store
}

// NewStores opens the per-user stores in dir. It returns ErrLegacyLayout if
// diaries or tokens from a single-user version are still there.
func NewStores(dir string, tokens *Tokens) (*Stores, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	legacy, err := hasLegacyDiaries(dir)
	if err != nil {
		return nil, err
	}
	orphans, err := tokens.hasOrphans()
	if err != nil {
		return nil, err
	}
	if legacy || orphans {
		return nil, ErrLegacyLayout
	}
	return &Stores{dir: dir, m: map[string]*Store{}}, nil
}

// For returns user's store, creating it if needed.
func (s *Stores) For(user string) (*Store, error) {
	if !ValidUser(user) {
		return nil, errInvalidUser
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.m[user]; ok {
		return st, nil
	}
	st, err := NewStore(filepath.Join(s.dir, user))
	if err != nil {
		return nil, err
	}
	s.m[user] = st
	return st, nil
}

// Users returns the users that have a store on disk.
func Users(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && ValidUser(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// legacyFiles returns the diary files a single-user version left directly in
// dir.
func legacyFiles(dir string) ([]string, error) {
	var out []string
	for _, pat := range []string{"*.json", "*.diary"} {
		names, err := filepath.Glob(filepath.Join(dir, pat))
		if err != nil {
			return nil, err
		}
		out = append(out, names...)
	}
	return out, nil
}

func hasLegacyDiaries(dir string) (bool, error) {
	names, err := legacyFiles(dir)
	return len(names) > 0, err
}

// Migrate gives the diaries and device tokens of a single-user version to
// user. It is safe to run again if interrupted.
func Migrate(dir string, tokens *Tokens, user string) error {
	if !ValidUser(user) {
		return errInvalidUser
	}
	names, err := legacyFiles(dir)
	if err != nil {
		return err
	}
	dest := filepath.Join(dir, user)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	// Content first, metadata last: a store never lists a diary whose
	// content has not arrived yet.
	for _, pat := range []string{".diary", ".json"} {
		for _, n := range names {
			if !strings.HasSuffix(n, pat) {
				continue
			}
			target := filepath.Join(dest, filepath.Base(n))
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("%s already exists", target)
			}
			if err := os.Rename(n, target); err != nil {
				return err
			}
		}
	}
	return tokens.adoptOrphans(user)
}
