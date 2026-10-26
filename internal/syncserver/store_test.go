package syncserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScrubLabels(t *testing.T) {
	dir := t.TempDir()
	id := "0123456789abcdef01234567"
	old := `{"id":"` + id + `","version":1,"size":3,"updated":"2026-10-26T07:00:00Z","label":"Secret name"}`
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".1.diary"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, id+".json"))
	if strings.Contains(string(b), "Secret") || strings.Contains(string(b), "label") {
		t.Fatalf("label not scrubbed: %s", b)
	}
	info, data, err := s.Get(id)
	if err != nil || info.Version != 1 || string(data) != "abc" {
		t.Fatalf("diary damaged by scrub: %+v %q %v", info, data, err)
	}
}
