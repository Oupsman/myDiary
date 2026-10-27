package export

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/oupsman/mydiary/internal/diary"
)

func TestMarkdown(t *testing.T) {
	got := Markdown([]diary.Run{
		{Text: "# not a heading, "}, {Text: "bold ", Bold: true}, {Text: "both", Bold: true, Italic: true},
		{Text: " 2*3 "}, {Text: "under", Underline: true}, {Text: "\n1. not a list\n"},
		{Image: "pic"}, {Text: "\nserif", Font: "Serif", Size: 16},
	}, func(id string) string { return "../images/" + id + ".jpg" })
	want := "\\# not a heading, **bold** ***both*** 2\\*3 <u>under</u>\n1\\. not a list\n![](../images/pic.jpg)\nserif\n"
	if got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

func TestWriteZip(t *testing.T) {
	d := diary.New("Mine")
	img := d.AddImage("image/png", []byte("png data"))
	a := d.AddEntry()
	a.Title, a.Mood, a.Tags = "Same: day?", "happy", []string{"work"}
	a.Created = time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local)
	a.Content = []diary.Run{{Text: "Hello "}, {Image: img}}
	b := d.AddEntry()
	b.Title, b.Created = "Same: day?", a.Created.Add(time.Hour)

	var buf bytes.Buffer
	if err := WriteZip(&buf, d); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range z.File {
		r, _ := f.Open()
		data, _ := io.ReadAll(r)
		files[f.Name] = string(data)
	}

	md := files["entries/2026-10-24 Same day.md"]
	for _, s := range []string{`title: "Same: day?"`, `mood: "happy"`, `tags: ["work"]`, "Hello ![](../images/" + img + ".png)"} {
		if !strings.Contains(md, s) {
			t.Errorf("markdown lacks %q:\n%s", s, md)
		}
	}
	if _, ok := files["entries/2026-10-24 Same day (2).md"]; !ok {
		t.Errorf("same-name entry not kept apart: %v", keys(files))
	}
	if files["images/"+img+".png"] != "png data" {
		t.Errorf("picture missing: %v", keys(files))
	}
	var js jsonExport
	if err := json.Unmarshal([]byte(files["diary.json"]), &js); err != nil {
		t.Fatal(err)
	}
	if js.Title != "Mine" || len(js.Entries) != 2 || js.Entries[0].ID != a.ID || js.Images[img] == "" {
		t.Errorf("diary.json: %+v", js)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
