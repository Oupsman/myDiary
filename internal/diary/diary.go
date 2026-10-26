// Package diary holds the diary data model and its (encrypted) persistence.
package diary

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/oupsman/mydiary/internal/vault"
)

// FileExtension is the conventional suffix for diary files.
const FileExtension = ".diary"

const schemaVersion = 1

// Run is a contiguous piece of an entry sharing the same formatting, or a
// single embedded image when Image is set.
type Run struct {
	Text      string  `json:"t,omitempty"`
	Image     string  `json:"img,omitempty"`
	Font      string  `json:"f,omitempty"`
	Size      float64 `json:"s,omitempty"`
	Bold      bool    `json:"b,omitempty"`
	Italic    bool    `json:"i,omitempty"`
	Underline bool    `json:"u,omitempty"`
}

// SameStyle reports whether two text runs can be merged.
func (r Run) SameStyle(o Run) bool {
	return r.Image == "" && o.Image == "" &&
		r.Font == o.Font && r.Size == o.Size &&
		r.Bold == o.Bold && r.Italic == o.Italic && r.Underline == o.Underline
}

// Entry is one diary page.
type Entry struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
	Content  []Run     `json:"content"`
}

// PlainText returns the entry text without formatting or images.
func (e *Entry) PlainText() string {
	var b strings.Builder
	for _, r := range e.Content {
		b.WriteString(r.Text)
	}
	return b.String()
}

// Image is an attached picture, stored in its original encoding.
type Image struct {
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

// Diary is the content of one diary file.
type Diary struct {
	Version int               `json:"version"`
	Title   string            `json:"title"`
	Created time.Time         `json:"created"`
	Entries []*Entry          `json:"entries"`
	Images  map[string]*Image `json:"images"`
}

// New returns an empty diary.
func New(title string) *Diary {
	return &Diary{
		Version: schemaVersion,
		Title:   title,
		Created: time.Now(),
		Images:  map[string]*Image{},
	}
}

// NewID returns a random identifier.
func NewID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// AddEntry creates a new empty entry dated now and returns it.
func (d *Diary) AddEntry() *Entry {
	now := time.Now()
	e := &Entry{
		ID:       NewID(),
		Title:    now.Format("Monday 2 January 2006"),
		Created:  now,
		Modified: now,
	}
	d.Entries = append(d.Entries, e)
	return e
}

// RemoveEntry deletes the entry with the given ID.
func (d *Diary) RemoveEntry(id string) {
	for i, e := range d.Entries {
		if e.ID == id {
			d.Entries = append(d.Entries[:i], d.Entries[i+1:]...)
			return
		}
	}
}

// Entry returns the entry with the given ID, or nil.
func (d *Diary) Entry(id string) *Entry {
	for _, e := range d.Entries {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// SortedEntries returns entries, newest first.
func (d *Diary) SortedEntries() []*Entry {
	s := append([]*Entry(nil), d.Entries...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].Created.After(s[j].Created) })
	return s
}

// AddImage stores an image and returns its ID.
func (d *Diary) AddImage(mime string, data []byte) string {
	id := NewID()
	d.Images[id] = &Image{MIME: mime, Data: data}
	return id
}

// PruneImages drops images no entry refers to any more.
func (d *Diary) PruneImages() {
	used := map[string]bool{}
	for _, e := range d.Entries {
		for _, r := range e.Content {
			if r.Image != "" {
				used[r.Image] = true
			}
		}
	}
	for id := range d.Images {
		if !used[id] {
			delete(d.Images, id)
		}
	}
}

// Marshal serializes the diary (unencrypted).
func (d *Diary) Marshal() ([]byte, error) {
	d.PruneImages()
	return json.Marshal(d)
}

// Unmarshal parses a serialized diary.
func Unmarshal(data []byte) (*Diary, error) {
	d := &Diary{}
	if err := json.Unmarshal(data, d); err != nil {
		return nil, fmt.Errorf("decoding diary: %w", err)
	}
	if d.Version > schemaVersion {
		return nil, fmt.Errorf("diary was written by a newer version (schema %d)", d.Version)
	}
	if d.Images == nil {
		d.Images = map[string]*Image{}
	}
	return d, nil
}

// Load reads and decrypts a diary file. The returned key can be used to save
// it again without re-deriving.
func Load(path string, passphrase []byte) (*Diary, *vault.Key, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	pt, key, err := vault.Open(data, passphrase)
	if err != nil {
		return nil, nil, err
	}
	defer wipe(pt)
	d, err := Unmarshal(pt)
	if err != nil {
		key.Wipe()
		return nil, nil, err
	}
	return d, key, nil
}

// Save encrypts the diary with key and writes it atomically to path.
func Save(path string, d *Diary, key *vault.Key) error {
	pt, err := d.Marshal()
	if err != nil {
		return err
	}
	defer wipe(pt)
	sealed, err := key.Seal(pt)
	if err != nil {
		return err
	}
	return vault.WriteFileAtomic(path, sealed)
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
