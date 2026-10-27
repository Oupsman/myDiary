package diary

import (
	"testing"
	"time"
)

func entry(id, text string, mod time.Time) *Entry {
	return &Entry{ID: id, Title: id, Created: mod, Modified: mod, Content: []Run{{Text: text}}}
}

func TestMerge(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := time.Hour

	base := New("d")
	base.Entries = []*Entry{
		entry("same", "x", t0),
		entry("editedLocal", "old", t0),
		entry("editedRemote", "old", t0),
		entry("deletedRemote", "x", t0),
		entry("deletedThenEdited", "x", t0),
	}
	local := base.Clone()
	remote := base.Clone()

	local.Entry("editedLocal").Content = []Run{{Text: "new local"}}
	local.Entry("editedLocal").Modified = t0.Add(h)
	local.Entries = append(local.Entries, entry("newLocal", "l", t0.Add(h)))
	local.Entry("deletedThenEdited").Modified = t0.Add(3 * h)

	remote.Entry("editedRemote").Content = []Run{{Text: "new remote"}}
	remote.Entry("editedRemote").Modified = t0.Add(h)
	remote.Entries = append(remote.Entries, entry("newRemote", "r", t0.Add(h)))
	remote.RemoveEntry("deletedRemote")
	remote.RemoveEntry("deletedThenEdited")
	remote.Deleted["deletedThenEdited"] = t0.Add(2 * h) // before the local edit

	keepPtr := local.Entry("same")
	m, changed := Merge(local, remote)
	if !changed {
		t.Fatal("expected changed")
	}
	want := map[string]string{
		"same": "x", "editedLocal": "new local", "editedRemote": "new remote",
		"deletedThenEdited": "x", "newLocal": "l", "newRemote": "r",
	}
	if len(m.Entries) != len(want) {
		t.Fatalf("got %d entries", len(m.Entries))
	}
	for id, text := range want {
		e := m.Entry(id)
		if e == nil || e.PlainText() != text {
			t.Errorf("%s: got %+v", id, e)
		}
	}
	if m.Entry("same") != keepPtr {
		t.Error("local entry pointer not preserved")
	}

	// Merging is idempotent and symmetric in content.
	m2, changed := Merge(m, remote)
	if changed || len(m2.Entries) != len(m.Entries) {
		t.Fatalf("second merge changed=%v n=%d", changed, len(m2.Entries))
	}
	r, _ := Merge(remote, local)
	for id, text := range want {
		if e := r.Entry(id); e == nil || e.PlainText() != text {
			t.Errorf("reverse %s: got %+v", id, e)
		}
	}
}

func TestMergeMoodOnly(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	local := New("d")
	local.Entries = []*Entry{entry("e", "x", t0)}
	remote := local.Clone()
	remote.Entries[0].Mood = "happy"
	remote.Entries[0].Modified = t0.Add(time.Hour)

	m, changed := Merge(local, remote)
	if !changed || m.Entry("e").Mood != "happy" {
		t.Fatalf("changed=%v mood=%q, want the remote mood", changed, m.Entry("e").Mood)
	}
}

func TestMergeUnchanged(t *testing.T) {
	d := New("d")
	d.AddEntry()
	if _, changed := Merge(d, d.Clone()); changed {
		t.Fatal("identical copies reported as changed")
	}
}

func TestMergeDateAndTags(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	local := New("d")
	local.Entries = []*Entry{entry("e", "x", t0)}
	remote := local.Clone()
	remote.Entries[0].Created = t0.Add(-24 * time.Hour)
	remote.Entries[0].Tags = []string{"travel"}
	remote.Entries[0].Modified = t0.Add(time.Hour)

	m, changed := Merge(local, remote)
	e := m.Entry("e")
	if !changed || !e.Created.Equal(t0.Add(-24*time.Hour)) || len(e.Tags) != 1 {
		t.Fatalf("changed=%v created=%v tags=%v, want the remote date and tags", changed, e.Created, e.Tags)
	}
}
