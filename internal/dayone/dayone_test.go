package dayone

import (
	"archive/zip"
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/oupsman/mydiary/internal/diary"
)

const journal = `{
  "metadata": {"version": "1.0"},
  "entries": [
    {
      "uuid": "0A1B2C3D4E5F60718293A4B5C6D7E8F9",
      "creationDate": "2024-07-14T09:30:00Z",
      "modifiedDate": "2024-07-15T10:00:00Z",
      "timeZone": "Europe/Paris",
      "tags": ["holiday", "#Sea"],
      "text": "# At the beach\n\nWe swam **a lot** and ate *ice cream*\\.\n![](dayone-moment://PHOTO1)\n- sand\n- [x] sunscreen\nsnake_case stays\n![](dayone-moment:/video/VID1)",
      "photos": [{"identifier": "PHOTO1", "md5": "abc123", "type": "jpeg"}],
      "videos": [{"identifier": "VID1", "md5": "def456", "type": "mov"}]
    },
    {
      "uuid": "FFEEDDCCBBAA99887766554433221100",
      "creationDate": "2024-07-16T20:00:00Z",
      "modifiedDate": "2024-07-16T20:00:00Z",
      "timeZone": "Europe/Paris",
      "text": "This first line is a long paragraph rather than a title, so it must stay in the text and the date becomes the title."
    }
  ]
}`

func writeExport(t *testing.T) string {
	t.Helper()
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 3000, 1500)), nil); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "export.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, data := range map[string][]byte{
		"Journal.json":       []byte(journal),
		"Work.json":          []byte(`{"entries": [{"uuid": "11112222333344445555666677778888", "creationDate": "2024-01-02T08:00:00Z", "text": "Meeting"}]}`),
		"photos/abc123.jpeg": jpg.Bytes(),
	} {
		w, _ := z.Create(name)
		w.Write(data)
	}
	z.Close()
	f.Close()
	return p
}

func TestImport(t *testing.T) {
	p := writeExport(t)
	res, err := Import(p, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if res.Journals != 2 || len(res.Entries) != 3 || res.Pictures != 1 || res.Unsupported != 1 {
		t.Fatalf("journals=%d entries=%d pictures=%d unsupported=%d",
			res.Journals, len(res.Entries), res.Pictures, res.Unsupported)
	}
	byID := map[string]*diary.Entry{}
	for _, e := range res.Entries {
		byID[e.ID] = e
	}

	beach := byID["0a1b2c3d4e5f60718293a4b5c6d7e8f9"]
	if beach == nil || beach.Title != "At the beach" {
		t.Fatalf("beach entry: %+v", beach)
	}
	if !beach.Created.Equal(time.Date(2024, 7, 14, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("created %v", beach.Created)
	}
	if want := []string{"holiday", "Sea", "Journal"}; !slices.Equal(beach.Tags, want) {
		t.Errorf("tags %q, want %q", beach.Tags, want)
	}
	var img string
	for _, r := range beach.Content {
		if r.Image != "" {
			img = r.Image
		}
	}
	want := []diary.Run{
		{Text: "We swam "}, {Text: "a lot", Bold: true}, {Text: " and ate "},
		{Text: "ice cream", Italic: true}, {Text: ".\n"}, {Image: img},
		{Text: "\n• sand\n☑ sunscreen\nsnake_case stays\n"}, {Text: "[video not imported]", Italic: true},
	}
	if !slices.Equal(beach.Content, want) {
		t.Errorf("content\n got %+v\nwant %+v", beach.Content, want)
	}
	pic := res.Images[img]
	if pic == nil || pic.MIME != "image/jpeg" {
		t.Fatalf("picture %v", pic)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(pic.Data))
	if err != nil || cfg.Width != 2048 {
		t.Errorf("picture not shrunk: %+v %v", cfg, err)
	}

	long := byID["ffeeddccbbaa99887766554433221100"]
	if long.Title != "Tuesday 16 July 2024" || long.PlainText() == "" {
		t.Errorf("long first line: title %q, text %q", long.Title, long.PlainText())
	}

	// Importing again adds nothing.
	again, err := Import(p, func(id string) bool { return byID[id] != nil })
	if err != nil || len(again.Entries) != 0 || again.Skipped != 3 {
		t.Fatalf("second import: %d entries, %d skipped, %v", len(again.Entries), again.Skipped, err)
	}
}

func TestMarkdownRuns(t *testing.T) {
	got := markdownRuns("## Sub\n```\nx := 1\n```\n> quoted `code`", nil)
	want := []diary.Run{
		{Text: "Sub", Bold: true, Size: 16}, {Text: "\n"},
		{Text: "x := 1", Font: "Monospace"}, {Text: "\n"},
		{Text: "quoted ", Italic: true}, {Text: "code", Italic: true, Font: "Monospace"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("\n got %+v\nwant %+v", got, want)
	}
	if got := inlineText(`see [the site](https://example.com) \*not bold\*`); got != "see the site (https://example.com) *not bold*" {
		t.Errorf("inline text %q", got)
	}
}

func TestNotAnExport(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.zip")
	os.WriteFile(p, []byte("nope"), 0o600)
	if _, err := Import(p, func(string) bool { return false }); err == nil {
		t.Fatal("garbage accepted")
	}
}
