package syncclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/oupsman/mydiary/internal/diary"
	"github.com/oupsman/mydiary/internal/vault"
)

// KeyMismatchError is returned when the server copy is encrypted with
// another passphrase, i.e. the passphrase was changed on another computer.
// The caller should ask for that passphrase and unlock Data with it.
type KeyMismatchError struct {
	Data    []byte
	Version int64
}

func (e *KeyMismatchError) Error() string {
	return "the passphrase of this diary was changed on another computer"
}

// Request describes one synchronisation.
type Request struct {
	Diary   *diary.Diary // a snapshot (clone) of the local diary; Sync may modify it
	Key     *vault.Key   // a clone of the local key
	Version int64        // server version last synced with, 0 if never
	Pending bool         // the local diary has changes not uploaded yet
}

// Result is the outcome of a successful synchronisation.
type Result struct {
	// Diary is the merged diary. When Changed is true it differs from the
	// snapshot and must be merged into the live diary.
	Diary   *diary.Diary
	Changed bool
	Version int64 // server version now in sync with
	Pushed  bool
}

const maxAttempts = 5

// Sync pulls the server copy, merges it with the local one and pushes the
// result if the server lacks anything. Concurrent uploads from other
// computers are handled by retrying on version conflicts.
func Sync(ctx context.Context, c *Client, req Request) (Result, error) {
	res := Result{Diary: req.Diary, Version: req.Version}
	for range maxAttempts {
		data, version, notModified, err := c.Get(ctx, req.Diary.ID, res.Version)
		needPush := req.Pending
		switch {
		case errors.Is(err, ErrNotFound):
			res.Version, needPush = 0, true
		case err != nil:
			return res, err
		case !notModified:
			pt, err := req.Key.Open(data)
			if errors.Is(err, vault.ErrKeyMismatch) {
				return res, &KeyMismatchError{Data: data, Version: version}
			}
			if err != nil {
				return res, fmt.Errorf("server copy cannot be decrypted: %w", err)
			}
			remote, err := diary.Unmarshal(pt)
			if err != nil {
				return res, err
			}
			if remote.ID != req.Diary.ID {
				return res, errors.New("server copy belongs to another diary")
			}
			merged, changed := diary.Merge(res.Diary, remote)
			res.Diary, res.Changed = merged, res.Changed || changed
			_, remoteBehind := diary.Merge(remote, merged)
			needPush = remoteBehind
			res.Version = version
		}
		if !needPush {
			return res, nil
		}

		pt, err := res.Diary.Marshal()
		if err != nil {
			return res, err
		}
		sealed, err := req.Key.Seal(pt)
		if err != nil {
			return res, err
		}
		v, err := c.Put(ctx, req.Diary.ID, res.Version, sealed)
		if errors.Is(err, ErrConflict) {
			// Someone uploaded in between: pull their version and merge again.
			req.Pending = true
			continue
		}
		if err != nil {
			return res, err
		}
		res.Version, res.Pushed = v, true
		return res, nil
	}
	return res, errors.New("the diary keeps changing on the server, try again later")
}
