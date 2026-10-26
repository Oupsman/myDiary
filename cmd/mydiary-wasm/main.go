//go:build js && wasm

// Command mydiary-wasm is the core of the web frontend: it runs in the
// browser and exposes, as the global "mydiary" object, the same diary,
// encryption, merge and sync code as the desktop app. The page only does the
// UI; the passphrase and the key never leave this module.
//
// Values cross the boundary as JSON strings. Functions that touch the
// network or derive a key return Promises.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"syscall/js"
	"time"

	"github.com/oupsman/mydiary/internal/diary"
	"github.com/oupsman/mydiary/internal/syncclient"
	"github.com/oupsman/mydiary/internal/syncproto"
	"github.com/oupsman/mydiary/internal/vault"
)

// state of the open diary. JS calls and the sync goroutine all run on the
// browser's single thread, interleaving only while a request is in flight.
var (
	client  *syncclient.Client
	d       *diary.Diary
	key     *vault.Key
	version int64 // server version last synced with

	// editSeq counts local edits; syncedSeq is the last one the server has.
	editSeq, syncedSeq int

	// mismatch holds the server copy when it was re-encrypted with another
	// passphrase, until rekey is called with that passphrase.
	mismatch *syncclient.KeyMismatchError
)

var errLocked = errors.New("the diary is locked")

func main() {
	api := map[string]any{}
	// Synchronous functions return {v} or {e: message}: a Go panic would
	// kill the module rather than throw, so the page unwraps errors.
	fn := func(name string, f func(args []js.Value) (any, error)) {
		api[name] = js.FuncOf(func(_ js.Value, args []js.Value) any {
			v, err := f(args)
			if err != nil {
				return map[string]any{"e": err.Error()}
			}
			return map[string]any{"v": v}
		})
	}
	async := func(name string, f func(args []js.Value) (any, error)) {
		api[name] = js.FuncOf(func(_ js.Value, args []js.Value) any {
			return promise(func() (any, error) { return f(args) })
		})
	}

	fn("configure", configure)
	async("list", list)
	async("unlock", unlock)
	async("sync", sync)
	async("rekey", rekey)
	fn("lock", func([]js.Value) (any, error) { lock(); return nil, nil })
	fn("title", func([]js.Value) (any, error) {
		if d == nil {
			return nil, errLocked
		}
		return d.Title, nil
	})
	fn("pending", func([]js.Value) (any, error) { return editSeq != syncedSeq, nil })
	fn("moods", moods)
	fn("entries", entries)
	fn("entry", entry)
	fn("saveEntry", saveEntry)
	fn("newEntry", newEntry)
	fn("deleteEntry", deleteEntry)
	fn("image", image)

	js.Global().Set("mydiary", js.ValueOf(api))
	js.Global().Call("dispatchEvent", js.Global().Get("Event").New("mydiary-ready"))
	select {}
}

// promise runs f in a goroutine, so that it may block on fetch, and settles
// a JS Promise with its result.
func promise(f func() (any, error)) js.Value {
	var executor js.Func
	executor = js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			defer executor.Release()
			v, err := f()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			resolve.Invoke(v)
		}()
		return nil
	})
	return js.Global().Get("Promise").New(executor)
}

func toJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func requireOpen() error {
	if d == nil {
		return errLocked
	}
	return nil
}

// configure(token): the API is served by the same origin as the page.
func configure(args []js.Value) (any, error) {
	c, err := syncclient.New(syncclient.Config{
		ServerURL: js.Global().Get("location").Get("origin").String(),
		Token:     args[0].String(),
	})
	if err != nil {
		return nil, err
	}
	client = c
	return nil, nil
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

// list() → JSON [{id, short, size, updated}]
func list([]js.Value) (any, error) {
	if client == nil {
		return nil, errors.New("no device token")
	}
	c, cancel := ctx()
	defer cancel()
	infos, err := client.List(c)
	if err != nil {
		return nil, err
	}
	type item struct {
		ID      string    `json:"id"`
		Short   string    `json:"short"`
		Size    int64     `json:"size"`
		Updated time.Time `json:"updated"`
	}
	out := []item{}
	for _, i := range infos {
		out = append(out, item{i.ID, syncproto.ShortID(i.ID), i.Size, i.Updated})
	}
	return toJSON(out)
}

// unlock(id, passphrase) downloads and decrypts a diary.
func unlock(args []js.Value) (any, error) {
	if client == nil {
		return nil, errors.New("no device token")
	}
	id, pass := args[0].String(), []byte(args[1].String())
	c, cancel := ctx()
	defer cancel()
	data, v, _, err := client.Get(c, id, 0)
	if err != nil {
		return nil, err
	}
	pt, k, err := vault.Open(data, pass)
	if err != nil {
		return nil, err
	}
	nd, err := diary.Unmarshal(pt)
	if err != nil {
		k.Wipe()
		return nil, err
	}
	lock()
	d, key, version = nd, k, v
	editSeq, syncedSeq = 0, 0
	return nil, nil
}

func lock() {
	if key != nil {
		key.Wipe()
	}
	d, key, mismatch, version = nil, nil, nil, 0
}

// sync() pulls, merges and pushes like the desktop app. It resolves to
// JSON {status: "ok"|"passphrase", changed}: "passphrase" means the diary
// was re-encrypted elsewhere and rekey must be called.
func sync([]js.Value) (any, error) {
	if err := requireOpen(); err != nil {
		return nil, err
	}
	type result struct {
		Status  string `json:"status"`
		Changed bool   `json:"changed"`
	}
	seq := editSeq
	c, cancel := ctx()
	defer cancel()
	res, err := syncclient.Sync(c, client, syncclient.Request{
		Diary:   d.Clone(),
		Key:     key.Clone(),
		Version: version,
		Pending: seq != syncedSeq,
	})
	if d == nil {
		return nil, errLocked // locked while syncing
	}
	var km *syncclient.KeyMismatchError
	if errors.As(err, &km) {
		mismatch = km
		return toJSON(result{Status: "passphrase"})
	}
	if err != nil {
		return nil, err
	}
	if res.Changed {
		// Keep edits made while the sync was running.
		d, _ = diary.Merge(d, res.Diary)
	}
	version, syncedSeq = res.Version, seq
	return toJSON(result{Status: "ok", Changed: res.Changed})
}

// rekey(passphrase) unlocks the server copy that was re-encrypted with a new
// passphrase and adopts its key, keeping local changes. Call sync after.
func rekey(args []js.Value) (any, error) {
	if err := requireOpen(); err != nil {
		return nil, err
	}
	if mismatch == nil {
		return nil, errors.New("no passphrase change to apply")
	}
	pt, k, err := vault.Open(mismatch.Data, []byte(args[0].String()))
	if err != nil {
		return nil, err
	}
	remote, err := diary.Unmarshal(pt)
	if err != nil {
		k.Wipe()
		return nil, err
	}
	if remote.ID != d.ID {
		k.Wipe()
		return nil, errors.New("server copy belongs to another diary")
	}
	d, _ = diary.Merge(d, remote)
	key.Wipe()
	key, version, mismatch = k, mismatch.Version, nil
	editSeq++ // local changes may still need pushing
	return nil, nil
}

func moods([]js.Value) (any, error) {
	type mood struct {
		Key   string `json:"key"`
		Emoji string `json:"emoji"`
		Label string `json:"label"`
	}
	out := []mood{}
	for _, m := range diary.Moods {
		out = append(out, mood{m.Key, m.Emoji, m.Label})
	}
	return toJSON(out)
}

// entries(query) → JSON summaries, newest first, filtered like the desktop
// search (title, text or mood name).
func entries(args []js.Value) (any, error) {
	if err := requireOpen(); err != nil {
		return nil, err
	}
	q := ""
	if len(args) > 0 && args[0].Type() == js.TypeString {
		q = strings.ToLower(strings.TrimSpace(args[0].String()))
	}
	type summary struct {
		ID       string    `json:"id"`
		Title    string    `json:"title"`
		Created  time.Time `json:"created"`
		Modified time.Time `json:"modified"`
		Mood     string    `json:"mood"`
		Snippet  string    `json:"snippet"`
	}
	out := []summary{}
	for _, e := range d.SortedEntries() {
		text := e.PlainText()
		if q != "" {
			m, _ := diary.MoodByKey(e.Mood)
			if !strings.Contains(strings.ToLower(e.Title), q) &&
				!strings.Contains(strings.ToLower(text), q) &&
				!(m.Label != "" && strings.Contains(strings.ToLower(m.Label), q)) {
				continue
			}
		}
		snippet := strings.Join(strings.Fields(strings.ReplaceAll(text, "￼", "")), " ")
		if r := []rune(snippet); len(r) > 90 {
			snippet = string(r[:90]) + "…"
		}
		out = append(out, summary{e.ID, e.Title, e.Created, e.Modified, e.Mood, snippet})
	}
	return toJSON(out)
}

// entry(id) → JSON entry with its runs, or null.
func entry(args []js.Value) (any, error) {
	if err := requireOpen(); err != nil {
		return nil, err
	}
	e := d.Entry(args[0].String())
	if e == nil {
		return nil, nil
	}
	return toJSON(e)
}

// saveEntry(json {id, title, mood, content}) stores an edited entry and
// reports whether anything changed. Runs are normalised the way the desktop
// editor produces them, and may only refer to pictures the diary has.
func saveEntry(args []js.Value) (any, error) {
	if err := requireOpen(); err != nil {
		return nil, err
	}
	var in diary.Entry
	if err := json.Unmarshal([]byte(args[0].String()), &in); err != nil {
		return nil, err
	}
	e := d.Entry(in.ID)
	if e == nil {
		return false, nil // deleted, e.g. by a sync
	}
	var content []diary.Run
	for _, r := range in.Content {
		r.Text = strings.ReplaceAll(r.Text, "￼", "")
		switch {
		case r.Image != "":
			if d.Images[r.Image] == nil {
				continue
			}
			r = diary.Run{Image: r.Image}
		case r.Text == "":
			continue
		case len(content) > 0 && content[len(content)-1].SameStyle(r):
			content[len(content)-1].Text += r.Text
			continue
		}
		content = append(content, r)
	}
	if _, ok := diary.MoodByKey(in.Mood); !ok && in.Mood != e.Mood {
		in.Mood = ""
	}
	if in.Title == e.Title && in.Mood == e.Mood && slices.Equal(content, e.Content) {
		return false, nil
	}
	e.Title, e.Mood, e.Content = in.Title, in.Mood, content
	e.Modified = time.Now()
	editSeq++
	return true, nil
}

func newEntry([]js.Value) (any, error) {
	if err := requireOpen(); err != nil {
		return nil, err
	}
	editSeq++
	return d.AddEntry().ID, nil
}

func deleteEntry(args []js.Value) (any, error) {
	if err := requireOpen(); err != nil {
		return nil, err
	}
	if d.Entry(args[0].String()) != nil {
		d.RemoveEntry(args[0].String())
		editSeq++
	}
	return nil, nil
}

// image(id) → {mime, data: Uint8Array}, or null.
func image(args []js.Value) (any, error) {
	if err := requireOpen(); err != nil {
		return nil, err
	}
	img := d.Images[args[0].String()]
	if img == nil {
		return nil, nil
	}
	data := js.Global().Get("Uint8Array").New(len(img.Data))
	js.CopyBytesToJS(data, img.Data)
	return map[string]any{"mime": img.MIME, "data": data}, nil
}
