package diary

import (
	"encoding/json"
	"time"
)

// Clone returns a deep copy of the diary (image data is shared, as it is
// never modified in place).
func (d *Diary) Clone() *Diary {
	c := *d
	c.Entries = make([]*Entry, len(d.Entries))
	for i, e := range d.Entries {
		ce := *e
		ce.Content = append([]Run(nil), e.Content...)
		c.Entries[i] = &ce
	}
	c.Images = make(map[string]*Image, len(d.Images))
	for k, v := range d.Images {
		c.Images[k] = v
	}
	c.Deleted = make(map[string]time.Time, len(d.Deleted))
	for k, v := range d.Deleted {
		c.Deleted[k] = v
	}
	return &c
}

// Merge combines two copies of the same diary, entry by entry: the most
// recently modified version of each entry wins, and an entry deleted after
// its last modification stays deleted.
//
// Entries kept from local are the very same pointers, so callers holding a
// local *Entry stay valid. changed reports whether the result differs from
// local.
func Merge(local, remote *Diary) (merged *Diary, changed bool) {
	m := &Diary{
		Version: schemaVersion,
		ID:      local.ID,
		Title:   local.Title,
		Created: local.Created,
		Images:  map[string]*Image{},
		Deleted: map[string]time.Time{},
	}
	if m.Title == "" {
		m.Title = remote.Title
	}
	if remote.Created.Before(m.Created) {
		m.Created = remote.Created
	}
	for _, src := range []*Diary{local, remote} {
		for id, t := range src.Deleted {
			if t.After(m.Deleted[id]) {
				m.Deleted[id] = t
			}
		}
		for id, img := range src.Images {
			m.Images[id] = img
		}
	}

	deleted := func(e *Entry) bool {
		t, ok := m.Deleted[e.ID]
		return ok && !t.Before(e.Modified)
	}

	remoteByID := make(map[string]*Entry, len(remote.Entries))
	for _, e := range remote.Entries {
		remoteByID[e.ID] = e
	}
	seen := map[string]bool{}
	for _, l := range local.Entries {
		seen[l.ID] = true
		e := l
		if r := remoteByID[l.ID]; r != nil && r.Modified.After(l.Modified) && !sameEntry(l, r) {
			e = r
			changed = true
		}
		if deleted(e) {
			changed = true
			continue
		}
		m.Entries = append(m.Entries, e)
	}
	for _, r := range remote.Entries {
		if seen[r.ID] || deleted(r) {
			continue
		}
		m.Entries = append(m.Entries, r)
		changed = true
	}
	m.PruneImages()
	return m, changed
}

func sameEntry(a, b *Entry) bool {
	if a.Title != b.Title || a.Mood != b.Mood || len(a.Content) != len(b.Content) {
		return false
	}
	ja, _ := json.Marshal(a.Content)
	jb, _ := json.Marshal(b.Content)
	return string(ja) == string(jb)
}
