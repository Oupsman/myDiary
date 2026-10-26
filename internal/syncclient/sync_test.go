package syncclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/oupsman/mydiary/internal/diary"
	"github.com/oupsman/mydiary/internal/syncserver"
	"github.com/oupsman/mydiary/internal/vault"
)

var testParams = vault.Params{Time: 1, Memory: 8 * 1024, Threads: 1}

func newServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := syncserver.NewStore(filepath.Join(dir, "diaries"))
	if err != nil {
		t.Fatal(err)
	}
	tokens := syncserver.NewTokens(filepath.Join(dir, "tokens.json"))
	tok, err := tokens.Add("test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&syncserver.Server{Store: store, Tokens: tokens, MaxSize: 1 << 20}).Handler())
	t.Cleanup(srv.Close)
	return srv, tok
}

// device simulates one computer: its own copy of the diary and sync state.
type device struct {
	d       *diary.Diary
	key     *vault.Key
	version int64
	pending bool
}

func (dv *device) sync(t *testing.T, c *Client) Result {
	t.Helper()
	res, err := Sync(context.Background(), c, Request{
		Diary: dv.d.Clone(), Key: dv.key.Clone(), Version: dv.version, Pending: dv.pending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		dv.d, _ = diary.Merge(dv.d, res.Diary)
	}
	dv.version, dv.pending = res.Version, false
	return res
}

func TestTwoDevices(t *testing.T) {
	srv, tok := newServer(t)
	c, err := New(Config{ServerURL: srv.URL, Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	key, _ := vault.NewKey([]byte("pw"), testParams)
	a := &device{d: diary.New("d"), key: key, pending: true}
	ea := a.d.AddEntry()
	ea.Content = []diary.Run{{Text: "from A"}}

	if res := a.sync(t, c); !res.Pushed || res.Version != 1 {
		t.Fatalf("first push: %+v", res)
	}

	// B starts from the server copy (as "download from server" does).
	data, v, _, err := c.Get(ctx, a.d.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	pt, kb, err := vault.Open(data, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	db, _ := diary.Unmarshal(pt)
	b := &device{d: db, key: kb, version: v}

	// Both edit offline.
	later := time.Now().Add(time.Minute)
	ea.Content = []diary.Run{{Text: "edited on A"}}
	ea.Modified = later
	a.pending = true
	eb := b.d.AddEntry()
	eb.Content = []diary.Run{{Text: "new on B"}}
	b.pending = true

	b.sync(t, c) // version 2
	if res := a.sync(t, c); !res.Changed || !res.Pushed || res.Version != 3 {
		t.Fatalf("A merge: %+v", res)
	}
	if res := b.sync(t, c); !res.Changed || res.Pushed {
		t.Fatalf("B pull: %+v", res)
	}
	for _, dv := range []*device{a, b} {
		if len(dv.d.Entries) != 2 || dv.d.Entry(ea.ID).PlainText() != "edited on A" || dv.d.Entry(eb.ID).PlainText() != "new on B" {
			t.Fatalf("devices diverged: %+v", dv.d.Entries)
		}
	}

	// Nothing to do: no upload, no change.
	if res := a.sync(t, c); res.Changed || res.Pushed {
		t.Fatalf("idle sync: %+v", res)
	}

	// Deletion on B propagates to A.
	b.d.RemoveEntry(ea.ID)
	b.pending = true
	b.sync(t, c)
	a.sync(t, c)
	if a.d.Entry(ea.ID) != nil {
		t.Fatal("deletion not propagated")
	}

	// Passphrase changed on B: A gets a KeyMismatchError.
	nk, _ := vault.NewKey([]byte("new pw"), testParams)
	b.key = nk
	b.pending = true
	b.sync(t, c)
	_, err = Sync(ctx, c, Request{Diary: a.d.Clone(), Key: a.key.Clone(), Version: a.version})
	var km *KeyMismatchError
	if !errors.As(err, &km) {
		t.Fatalf("expected key mismatch, got %v", err)
	}
	if _, _, err := vault.Open(km.Data, []byte("new pw")); err != nil {
		t.Fatalf("mismatch data not openable with new passphrase: %v", err)
	}
}

func TestServerRefusals(t *testing.T) {
	srv, tok := newServer(t)
	ctx := context.Background()

	bad, _ := New(Config{ServerURL: srv.URL, Token: "nope"})
	if err := bad.Ping(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token: %v", err)
	}

	c, _ := New(Config{ServerURL: srv.URL, Token: tok})
	id := diary.NewID()
	if _, err := c.Put(ctx, id, 0, []byte("plaintext diary!")); err == nil {
		t.Fatal("plaintext upload accepted")
	}
	key, _ := vault.NewKey([]byte("pw"), testParams)
	sealed, _ := key.Seal([]byte("{}"))
	if _, err := c.Put(ctx, id, 0, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put(ctx, id, 0, sealed); !errors.Is(err, ErrConflict) {
		t.Fatalf("double create: %v", err)
	}
	if _, err := c.Put(ctx, id, 7, sealed); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
	list, err := c.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("list: %+v %v", list, err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/diaries/..%2fetc", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode == http.StatusOK {
		t.Fatalf("path traversal: %v %v", resp, err)
	}
}
