package syncserver

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestValidUser(t *testing.T) {
	for _, u := range []string{"alice", "bob-2", "a.b_c", "0"} {
		if !ValidUser(u) {
			t.Errorf("%q refused", u)
		}
	}
	for _, u := range []string{"", ".", "..", "-a", "Alice", "a/b", "a b", "abcdefghijklmnopqrstuvwxyz0123456"} {
		if ValidUser(u) {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestUsersAreIsolated(t *testing.T) {
	tokens := NewTokens(filepath.Join(t.TempDir(), "tokens.json"))
	stores, err := NewStores(t.TempDir(), tokens)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := tokens.Add("alice", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := tokens.Add("bob", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	h := (&Server{Stores: stores, Tokens: tokens, MaxSize: 1 << 20}).Handler()
	do := func(tok, method, path string, body []byte, hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	const path = "/v1/diaries/0123456789abcdef01234567"
	diary := append(append([]byte{}, vaultMagic...), "alice's"...)
	if w := do(alice, http.MethodPut, path, diary, "If-None-Match", "*"); w.Code != http.StatusOK {
		t.Fatalf("alice upload: %d %s", w.Code, w.Body)
	}
	if w := do(bob, http.MethodGet, path, nil); w.Code != http.StatusNotFound {
		t.Errorf("bob reads alice's diary: %d", w.Code)
	}
	if w := do(bob, http.MethodGet, "/v1/diaries", nil); w.Body.String() != "[]\n" {
		t.Errorf("bob's list: %s", w.Body)
	}
	if w := do(bob, http.MethodDelete, path, nil, "If-Match", `"1"`); w.Code != http.StatusNotFound {
		t.Errorf("bob deletes alice's diary: %d", w.Code)
	}
	// The same ID is a different diary for bob.
	if w := do(bob, http.MethodPut, path, diary, "If-None-Match", "*"); w.Code != http.StatusOK {
		t.Errorf("bob upload: %d %s", w.Code, w.Body)
	}
	if w := do(alice, http.MethodGet, path, nil); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), diary) {
		t.Errorf("alice's diary: %d %q", w.Code, w.Body)
	}
}

func TestMigrate(t *testing.T) {
	data := t.TempDir()
	dir := filepath.Join(data, "diaries")
	tokPath := filepath.Join(data, "tokens.json")
	id := "0123456789abcdef01234567"
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, id+".json"), []byte(`{"id":"`+id+`","version":1,"size":3}`), 0o600)
	os.WriteFile(filepath.Join(dir, id+".1.diary"), []byte("abc"), 0o600)
	// A token file written by the single-user version: no "user".
	os.WriteFile(tokPath, []byte(`[{"name":"laptop","hash":"`+hashToken("secret")+`","created":"2026-10-26T07:00:00Z"}]`), 0o600)
	tokens := NewTokens(tokPath)

	if _, ok := tokens.Check("secret"); ok {
		t.Error("token without a user accepted")
	}
	if _, err := NewStores(dir, tokens); !errors.Is(err, ErrLegacyLayout) {
		t.Fatalf("legacy layout not detected: %v", err)
	}
	if err := Migrate(dir, tokens, "alice"); err != nil {
		t.Fatal(err)
	}
	stores, err := NewStores(dir, tokens)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := tokens.Check("secret")
	if !ok || d.User != "alice" || d.Name != "laptop" {
		t.Fatalf("migrated token: %+v %v", d, ok)
	}
	s, err := stores.For("alice")
	if err != nil {
		t.Fatal(err)
	}
	if info, data, err := s.Get(id); err != nil || info.Version != 1 || string(data) != "abc" {
		t.Fatalf("migrated diary: %+v %q %v", info, data, err)
	}
	if err := Migrate(dir, tokens, "alice"); err != nil {
		t.Errorf("second migrate: %v", err)
	}
}

// TestTokensReloadSameMtime simulates a file system with coarse timestamps:
// a change that keeps the modification time must still be seen.
func TestTokensReloadSameMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	tokens := NewTokens(path)
	first, err := tokens.Add("alice", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if _, ok := tokens.Check(first); !ok {
		t.Fatal("first token refused")
	}
	second, err := tokens.Add("alice", "phone")
	if err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, st.ModTime(), st.ModTime())
	if _, ok := tokens.Check(second); !ok {
		t.Error("token added within the same mtime refused")
	}
}
