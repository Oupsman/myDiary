package vault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var testParams = Params{Time: 1, Memory: 8 * 1024, Threads: 1}

func TestRoundTrip(t *testing.T) {
	k, err := NewKey([]byte("correct horse"), testParams)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := k.Seal([]byte("dear diary"))
	if err != nil {
		t.Fatal(err)
	}
	pt, k2, err := Open(sealed, []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "dear diary" {
		t.Fatalf("got %q", pt)
	}

	// Re-sealing with the recovered key must produce an openable file with a fresh nonce.
	sealed2, err := k2.Seal([]byte("again"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sealed[headerSize-nonceSize:headerSize], sealed2[headerSize-nonceSize:headerSize]) {
		t.Fatal("nonce reused")
	}
	if pt, _, err := Open(sealed2, []byte("correct horse")); err != nil || string(pt) != "again" {
		t.Fatalf("reopen: %q %v", pt, err)
	}
}

func TestWrongPassphraseAndTamper(t *testing.T) {
	k, _ := NewKey([]byte("pw"), testParams)
	sealed, _ := k.Seal([]byte("secret"))

	if _, _, err := Open(sealed, []byte("nope")); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	// Flip a salt byte: header is authenticated, so this must fail.
	bad := bytes.Clone(sealed)
	bad[30] ^= 1
	if _, _, err := Open(bad, []byte("pw")); err == nil {
		t.Fatal("tampered header accepted")
	}
	bad = bytes.Clone(sealed)
	bad[len(bad)-1] ^= 1
	if _, _, err := Open(bad, []byte("pw")); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, _, err := Open([]byte("hello world, not a vault at all, really not"), []byte("pw")); !errors.Is(err, ErrNotVault) {
		t.Fatalf("garbage: %v", err)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.diary")
	if err := WriteFileAtomic(p, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("two")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "two" {
		t.Fatalf("got %q", b)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}
