package diary

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/oupsman/mydiary/internal/vault"
)

func TestSaveLoad(t *testing.T) {
	d := New("Mine")
	e := d.AddEntry()
	img := d.AddImage("image/png", []byte{1, 2, 3})
	orphan := d.AddImage("image/png", []byte{4})
	e.Content = []Run{
		{Text: "Hello ", Font: "Serif", Size: 14, Bold: true},
		{Image: img},
		{Text: "world", Italic: true, Underline: true},
	}

	key, err := vault.NewKey([]byte("pw"), vault.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "x"+FileExtension)
	if err := Save(p, d, key); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(p, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Mine" || len(got.Entries) != 1 {
		t.Fatalf("bad diary %+v", got)
	}
	c := got.Entries[0].Content
	if len(c) != 3 || c[0] != e.Content[0] || c[1].Image != img || c[2] != e.Content[2] {
		t.Fatalf("content mismatch: %+v", c)
	}
	if _, ok := got.Images[orphan]; ok {
		t.Fatal("orphan image not pruned")
	}
	if string(got.Images[img].Data) != "\x01\x02\x03" {
		t.Fatal("image data mismatch")
	}
	if _, _, err := Load(p, []byte("bad")); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestNormalizeTags(t *testing.T) {
	got := NormalizeTags([]string{" work", "#Travel", "", "travel", "WORK ", "#", "a b"})
	want := []string{"work", "Travel", "a b"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMatches(t *testing.T) {
	e := &Entry{Title: "Holiday", Mood: "happy", Tags: []string{"Travel"},
		Content: []Run{{Text: "We went to the sea."}}}
	for q, want := range map[string]bool{
		"": true, "holi": true, "SEA": true, "happy": true, "trav": true,
		"#trav": true, "#sea": false, "mountain": false,
	} {
		if got := e.Matches(q); got != want {
			t.Errorf("Matches(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestOnThisDay(t *testing.T) {
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }
	now := at(2026, time.October, 27)
	for _, c := range []struct {
		created time.Time
		want    bool
	}{
		{at(2025, time.October, 27), true},
		{at(2019, time.October, 27), true},
		{at(2026, time.October, 27), false}, // this year
		{at(2025, time.October, 26), false},
	} {
		if got := (&Entry{Created: c.created}).OnThisDay(now); got != c.want {
			t.Errorf("%v: got %v", c.created, got)
		}
	}
	if !(&Entry{Created: at(2024, time.February, 29)}).OnThisDay(at(2026, time.February, 28)) {
		t.Error("February 29th not shown on February 28th of a common year")
	}
	if (&Entry{Created: at(2024, time.February, 29)}).OnThisDay(at(2028, time.February, 28)) {
		t.Error("February 29th shown on February 28th of a leap year")
	}
}
