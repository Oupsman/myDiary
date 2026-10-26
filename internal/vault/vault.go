// Package vault implements the encrypted container format used for diary files.
//
// Layout of a vault file (all integers big-endian):
//
//	magic      [8]byte  "MYDIARY\x00"
//	version    uint16   format version (1)
//	kdf        uint8    key derivation function (1 = Argon2id)
//	time       uint32   Argon2id iterations
//	memory     uint32   Argon2id memory in KiB
//	threads    uint8    Argon2id parallelism
//	salt       [32]byte random salt
//	nonce      [24]byte random XChaCha20-Poly1305 nonce
//	ciphertext []byte   XChaCha20-Poly1305(plaintext), with the whole header as AAD
//
// The key is derived from the passphrase with Argon2id and the payload is
// sealed with XChaCha20-Poly1305. Because the header is authenticated, any
// tampering with the KDF parameters, salt or nonce makes decryption fail.
package vault

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	formatVersion = 1
	kdfArgon2id   = 1

	saltSize  = 32
	keySize   = chacha20poly1305.KeySize
	nonceSize = chacha20poly1305.NonceSizeX

	headerSize = 8 + 2 + 1 + 4 + 4 + 1 + saltSize + nonceSize
)

var magic = [8]byte{'M', 'Y', 'D', 'I', 'A', 'R', 'Y', 0}

var (
	ErrNotVault        = errors.New("not a diary file")
	ErrUnsupported     = errors.New("unsupported diary file version")
	ErrWrongPassphrase = errors.New("wrong passphrase or corrupted file")
	ErrKeyMismatch     = errors.New("file is encrypted with another passphrase")
)

// Params are the Argon2id cost parameters.
type Params struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
}

// DefaultParams follows the second recommended option of RFC 9106
// (t=3, m=64 MiB, p=4).
var DefaultParams = Params{Time: 3, Memory: 64 * 1024, Threads: 4}

// Key is a derived encryption key bound to the salt and parameters used to
// derive it. Keeping it lets the application re-save without re-running the
// (deliberately slow) key derivation.
type Key struct {
	key    [keySize]byte
	salt   [saltSize]byte
	params Params
}

// NewKey derives a fresh key from passphrase with a new random salt.
func NewKey(passphrase []byte, p Params) (*Key, error) {
	k := &Key{params: p}
	if _, err := rand.Read(k.salt[:]); err != nil {
		return nil, err
	}
	k.derive(passphrase)
	return k, nil
}

func (k *Key) derive(passphrase []byte) {
	dk := argon2.IDKey(passphrase, k.salt[:], k.params.Time, k.params.Memory, k.params.Threads, keySize)
	copy(k.key[:], dk)
	wipe(dk)
}

// Clone returns an independent copy of the key, so it can be used from
// another goroutine while the original may be wiped.
func (k *Key) Clone() *Key {
	c := *k
	return &c
}

// Open decrypts a vault sealed with this very key (same salt and
// parameters), without running the key derivation again. It returns
// ErrKeyMismatch when the vault was sealed with another key, typically after
// the passphrase was changed elsewhere.
func (k *Key) Open(data []byte) ([]byte, error) {
	if len(data) < headerSize+chacha20poly1305.Overhead || !bytes.Equal(data[:8], magic[:]) {
		return nil, ErrNotVault
	}
	nonce := data[headerSize-nonceSize : headerSize]
	if !bytes.Equal(data[:headerSize], k.header(nonce)) {
		return nil, ErrKeyMismatch
	}
	aead, err := chacha20poly1305.NewX(k.key[:])
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, data[headerSize:], data[:headerSize])
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return pt, nil
}

// Wipe zeroes the key material.
func (k *Key) Wipe() {
	if k != nil {
		wipe(k.key[:])
	}
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func (k *Key) header(nonce []byte) []byte {
	h := make([]byte, 0, headerSize)
	h = append(h, magic[:]...)
	h = binary.BigEndian.AppendUint16(h, formatVersion)
	h = append(h, kdfArgon2id)
	h = binary.BigEndian.AppendUint32(h, k.params.Time)
	h = binary.BigEndian.AppendUint32(h, k.params.Memory)
	h = append(h, k.params.Threads)
	h = append(h, k.salt[:]...)
	h = append(h, nonce...)
	return h
}

// Seal encrypts plaintext and returns the complete vault file contents.
func (k *Key) Seal(plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(k.key[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	h := k.header(nonce)
	return aead.Seal(h, nonce, plaintext, h), nil
}

// Open parses a vault, derives the key from passphrase and decrypts it.
// The returned key can be reused with Seal to save the file again.
func Open(data, passphrase []byte) (plaintext []byte, key *Key, err error) {
	if len(data) < headerSize+chacha20poly1305.Overhead {
		return nil, nil, ErrNotVault
	}
	if !bytes.Equal(data[:8], magic[:]) {
		return nil, nil, ErrNotVault
	}
	r := data[8:]
	if binary.BigEndian.Uint16(r) != formatVersion || r[2] != kdfArgon2id {
		return nil, nil, ErrUnsupported
	}
	r = r[3:]
	k := &Key{params: Params{
		Time:    binary.BigEndian.Uint32(r),
		Memory:  binary.BigEndian.Uint32(r[4:]),
		Threads: r[8],
	}}
	r = r[9:]
	// Refuse absurd parameters so a crafted file cannot exhaust memory/CPU.
	if k.params.Time == 0 || k.params.Time > 64 || k.params.Memory < 8*1024 ||
		k.params.Memory > 4*1024*1024 || k.params.Threads == 0 {
		return nil, nil, ErrUnsupported
	}
	copy(k.salt[:], r[:saltSize])
	nonce := r[saltSize : saltSize+nonceSize]

	k.derive(passphrase)
	aead, err := chacha20poly1305.NewX(k.key[:])
	if err != nil {
		k.Wipe()
		return nil, nil, err
	}
	plaintext, err = aead.Open(nil, nonce, data[headerSize:], data[:headerSize])
	if err != nil {
		k.Wipe()
		return nil, nil, ErrWrongPassphrase
	}
	return plaintext, k, nil
}

// WriteFileAtomic writes data to path through a temporary file in the same
// directory, so a crash never leaves a half-written diary behind.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mydiary-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
