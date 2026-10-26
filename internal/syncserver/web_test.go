package syncserver

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestWeb(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Store:   store,
		Tokens:  NewTokens(filepath.Join(t.TempDir(), "tokens.json")),
		MaxSize: 1 << 20,
		Web: fstest.MapFS{
			"index.html":      {Data: []byte("<!doctype html>")},
			"mydiary.wasm":    {Data: []byte("raw")},
			"mydiary.wasm.gz": {Data: []byte("gzipped")},
		},
	}
	h := s.Handler()
	get := func(path, encoding string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if encoding != "" {
			r.Header.Set("Accept-Encoding", encoding)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := get("/", ""); w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("page: %d, CSP %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	w := get("/mydiary.wasm", "gzip, br")
	if w.Body.String() != "gzipped" || w.Header().Get("Content-Encoding") != "gzip" ||
		w.Header().Get("Content-Type") != "application/wasm" {
		t.Errorf("gzip wasm: %q %v", w.Body.String(), w.Header())
	}
	if w := get("/mydiary.wasm", ""); w.Body.String() != "raw" || w.Header().Get("Content-Encoding") != "" {
		t.Errorf("plain wasm: %q %v", w.Body.String(), w.Header())
	}
	// The page is public; the API still needs a token.
	if w := get("/v1/diaries", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("API without token: %d", w.Code)
	}
}
