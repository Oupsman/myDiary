// Package export writes a diary out of myDiary, unencrypted, as a zip of
// Markdown files plus a JSON file with everything, and the pictures.
//
// Layout:
//
//	entries/2026-10-24 First day.md   one file per entry, with YAML front matter
//	images/<id>.jpg                   pictures, referenced by the entries
//	diary.json                        all entries, with formatting (runs)
package export

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/oupsman/mydiary/internal/diary"
)

type jsonExport struct {
	Title   string            `json:"title"`
	Created time.Time         `json:"created"`
	Entries []jsonEntry       `json:"entries"`
	Images  map[string]string `json:"images"` // image ID → file in the zip
}

type jsonEntry struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Created  time.Time   `json:"created"`
	Modified time.Time   `json:"modified"`
	Mood     string      `json:"mood,omitempty"`
	Tags     []string    `json:"tags,omitempty"`
	File     string      `json:"file"`     // the Markdown version
	Markdown string      `json:"markdown"` // its body
	Content  []diary.Run `json:"content"`  // t: text, img: image ID, f: font, s: size (pt), b/i/u
}

// WriteZip writes the export of d to w.
func WriteZip(w io.Writer, d *diary.Diary) error {
	z := zip.NewWriter(w)
	out := jsonExport{Title: d.Title, Created: d.Created, Images: map[string]string{}}

	for id, img := range d.Images {
		name := "images/" + id + extension(img.MIME)
		out.Images[id] = name
		if err := writeFile(z, name, img.Data); err != nil {
			return err
		}
	}

	used := map[string]bool{}
	entries := d.SortedEntries()
	for i := len(entries) - 1; i >= 0; i-- { // oldest first
		e := entries[i]
		name := entryFileName(e, used)
		body := Markdown(e.Content, func(id string) string { return "../" + out.Images[id] })
		if err := writeFile(z, name, []byte(frontMatter(e)+body)); err != nil {
			return err
		}
		out.Entries = append(out.Entries, jsonEntry{
			ID: e.ID, Title: e.Title, Created: e.Created, Modified: e.Modified,
			Mood: e.Mood, Tags: e.Tags, File: name, Markdown: body, Content: e.Content,
		})
	}

	js, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(z, "diary.json", js); err != nil {
		return err
	}
	return z.Close()
}

func writeFile(z *zip.Writer, name string, data []byte) error {
	w, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func extension(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/heic":
		return ".heic"
	}
	return ".bin"
}

var unsafeName = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]+`)

// entryFileName returns a unique, portable file name such as
// "entries/2026-10-24 First day.md".
func entryFileName(e *diary.Entry, used map[string]bool) string {
	title := strings.Join(strings.Fields(unsafeName.ReplaceAllString(e.Title, " ")), " ")
	if r := []rune(title); len(r) > 60 {
		title = strings.TrimSpace(string(r[:60]))
	}
	base := "entries/" + e.Created.Local().Format("2006-01-02")
	if title != "" {
		base += " " + title
	}
	name := base + ".md"
	for n := 2; used[name]; n++ {
		name = fmt.Sprintf("%s (%d).md", base, n)
	}
	used[name] = true
	return name
}

func frontMatter(e *diary.Entry) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) } // JSON strings are valid YAML
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: " + q(e.Title) + "\n")
	b.WriteString("date: " + e.Created.Local().Format(time.RFC3339) + "\n")
	if m, ok := diary.MoodByKey(e.Mood); ok {
		b.WriteString("mood: " + q(m.Key) + " # " + m.Emoji + " " + m.Label + "\n")
	}
	if len(e.Tags) > 0 {
		tags, _ := json.Marshal(e.Tags)
		b.WriteString("tags: " + string(tags) + "\n")
	}
	b.WriteString("---\n\n")
	return b.String()
}

// Markdown converts runs to Markdown. Bold and italic become ** and *,
// underline <u>; fonts and sizes are dropped. imageURL gives the link of a
// picture.
func Markdown(runs []diary.Run, imageURL func(id string) string) string {
	var b strings.Builder
	lineStart := true
	for _, r := range runs {
		if r.Image != "" {
			b.WriteString("![](" + imageURL(r.Image) + ")")
			lineStart = false
			continue
		}
		for i, part := range strings.Split(r.Text, "\n") {
			if i > 0 {
				b.WriteString("\n")
				lineStart = true
			}
			if part == "" {
				continue
			}
			text := escape(part, lineStart)
			lineStart = false
			// Markers must touch the text: keep surrounding spaces outside.
			core := strings.TrimSpace(text)
			if core == "" || (!r.Bold && !r.Italic && !r.Underline) {
				b.WriteString(text)
				continue
			}
			lead := text[:strings.Index(text, core)]
			trail := text[len(lead)+len(core):]
			open, close := "", ""
			if r.Underline {
				open, close = "<u>", "</u>"
			}
			if r.Italic {
				open, close = open+"*", "*"+close
			}
			if r.Bold {
				open, close = open+"**", "**"+close
			}
			b.WriteString(lead + open + core + close + trail)
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

var blockStart = regexp.MustCompile(`^(\s*)([#>+-]|\d+\.)`)

// escape makes text literal in Markdown.
func escape(s string, lineStart bool) string {
	var b strings.Builder
	for _, c := range s {
		if strings.ContainsRune("\\*_`[]<>", c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if lineStart {
		if m := blockStart.FindStringSubmatchIndex(out); m != nil {
			// Escape the character that would start a heading, quote or list.
			at := m[5] - 1
			out = out[:at] + "\\" + out[at:]
		}
	}
	return out
}
