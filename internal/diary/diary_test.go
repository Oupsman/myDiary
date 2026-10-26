package diary

import (
	"path/filepath"
	"testing"

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
