package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const appID = "io.example.Test"

func TestEnableDisable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if Enabled(appID) {
		t.Fatal("enabled before Enable")
	}
	if err := Enable(appID, "/opt/my diary/mydiary"); err != nil {
		t.Fatal(err)
	}
	if !Enabled(appID) {
		t.Fatal("not enabled after Enable")
	}
	b, err := os.ReadFile(filepath.Join(dir, "autostart", appID+".desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "\nExec=\"/opt/my diary/mydiary\" --hidden\n"; !strings.Contains(string(b), want) {
		t.Errorf("entry lacks %q:\n%s", want, b)
	}

	if err := Disable(appID); err != nil {
		t.Fatal(err)
	}
	if Enabled(appID) {
		t.Fatal("enabled after Disable")
	}
	if err := Disable(appID); err != nil {
		t.Fatalf("Disable twice: %v", err)
	}
}

func TestQuoteExec(t *testing.T) {
	tests := map[string]string{
		"/usr/bin/mydiary":   "/usr/bin/mydiary",
		"/a b/mydiary":       `"/a b/mydiary"`,
		`/a$b/my"diary`:      `"/a\\$b/my\\"diary"`,
		`/a\b/mydiary`:       `"/a\\\\b/mydiary"`,
		"/100%/mydiary":      `"/100%%/mydiary"`,
		"/home/x/.local/bin": "/home/x/.local/bin",
	}
	for in, want := range tests {
		if got := quoteExec(in); got != want {
			t.Errorf("quoteExec(%q) = %q, want %q", in, got, want)
		}
	}
}
