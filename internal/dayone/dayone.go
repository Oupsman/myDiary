// Package dayone imports journals exported from Day One ("JSON" export: a
// zip with one JSON file per journal and a photos/ folder, or a lone JSON
// file).
package dayone

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oupsman/mydiary/internal/diary"
	"github.com/oupsman/mydiary/internal/picture"
)

type export struct {
	Entries []entry `json:"entries"`
}

type entry struct {
	UUID           string    `json:"uuid"`
	CreationDate   time.Time `json:"creationDate"`
	ModifiedDate   time.Time `json:"modifiedDate"`
	TimeZone       string    `json:"timeZone"`
	Text           string    `json:"text"`
	Tags           []string  `json:"tags"`
	Photos         []media   `json:"photos"`
	Videos         []media   `json:"videos"`
	Audios         []media   `json:"audios"`
	PDFAttachments []media   `json:"pdfAttachments"`
}

type media struct {
	Identifier string `json:"identifier"`
	MD5        string `json:"md5"`
	Type       string `json:"type"`
}

// Result holds the imported entries and their pictures, ready to be added to
// a diary.
type Result struct {
	Entries []*diary.Entry
	Images  map[string]*diary.Image

	Journals    int
	Skipped     int // already in the diary (imported before)
	Pictures    int
	Unsupported int // videos, audio recordings and PDFs, not imported
	Missing     int // pictures referenced but absent from the export
	HEIC        int // HEIC pictures, kept as they are
}

// maxJSON and maxPicture bound what is read from an export, in bytes.
const (
	maxJSON    = 512 << 20
	maxPicture = 64 << 20
)

// Import reads a Day One export (.zip or .json). exists reports entry IDs
// already in the target diary: they are skipped, so importing twice adds
// nothing. Entry IDs are derived from Day One's.
func Import(file string, exists func(id string) bool) (*Result, error) {
	res := &Result{Images: map[string]*diary.Image{}}
	if strings.EqualFold(path.Ext(file), ".json") {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if err := res.addJournal(f, nil, "", exists); err != nil {
			return nil, err
		}
		return res, nil
	}

	z, err := zip.OpenReader(file)
	if err != nil {
		return nil, fmt.Errorf("not a Day One export: %w", err)
	}
	defer z.Close()
	files := map[string]*zip.File{}
	var journals []*zip.File
	for _, f := range z.File {
		name := strings.TrimPrefix(f.Name, "./")
		files[name] = f
		if !strings.Contains(name, "/") && strings.EqualFold(path.Ext(name), ".json") {
			journals = append(journals, f)
		}
	}
	if len(journals) == 0 {
		return nil, errors.New("not a Day One export: no journal (.json) in the zip")
	}
	sort.Slice(journals, func(i, j int) bool { return journals[i].Name < journals[j].Name })
	for _, j := range journals {
		// With several journals, each entry is tagged with its journal name.
		tag := ""
		if len(journals) > 1 {
			tag = strings.TrimSuffix(j.Name, path.Ext(j.Name))
		}
		r, err := j.Open()
		if err != nil {
			return nil, err
		}
		err = res.addJournal(r, files, tag, exists)
		r.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", j.Name, err)
		}
	}
	return res, nil
}

func (res *Result) addJournal(r io.Reader, files map[string]*zip.File, tag string, exists func(string) bool) error {
	var ex export
	if err := json.NewDecoder(io.LimitReader(r, maxJSON)).Decode(&ex); err != nil {
		return fmt.Errorf("not a Day One journal: %w", err)
	}
	res.Journals++
	for _, de := range ex.Entries {
		id := strings.ToLower(de.UUID)
		if id == "" {
			id = diary.NewID()
		}
		if exists(id) {
			res.Skipped++
			continue
		}
		e := res.convert(de, files)
		e.ID = id
		if tag != "" {
			e.Tags = diary.NormalizeTags(append(e.Tags, tag))
		}
		res.Entries = append(res.Entries, e)
	}
	return nil
}

func (res *Result) convert(de entry, files map[string]*zip.File) *diary.Entry {
	loc := time.Local
	if l, err := time.LoadLocation(de.TimeZone); err == nil && de.TimeZone != "" {
		loc = l
	}
	created := de.CreationDate
	if created.IsZero() {
		created = time.Now()
	}
	modified := de.ModifiedDate
	if modified.Before(created) {
		modified = created
	}
	e := &diary.Entry{
		Created:  created,
		Modified: modified,
		Tags:     diary.NormalizeTags(de.Tags),
	}

	photos := map[string]media{}
	for _, p := range de.Photos {
		photos[p.Identifier] = p
	}
	other := map[string]string{}
	for kind, ms := range map[string][]media{"video": de.Videos, "audio recording": de.Audios, "PDF": de.PDFAttachments} {
		for _, m := range ms {
			other[m.Identifier] = kind
		}
	}
	used := map[string]bool{}
	attach := func(ident string) diary.Run {
		used[ident] = true
		if kind, ok := other[ident]; ok {
			res.Unsupported++
			return diary.Run{Text: "[" + kind + " not imported]", Italic: true}
		}
		p, ok := photos[ident]
		if !ok {
			res.Missing++
			return diary.Run{Text: "[missing picture]", Italic: true}
		}
		id, err := res.picture(p, files)
		if err != nil {
			res.Missing++
			return diary.Run{Text: "[missing picture]", Italic: true}
		}
		return diary.Run{Image: id}
	}

	title, body := splitTitle(de.Text)
	e.Title = title
	if e.Title == "" {
		e.Title = created.In(loc).Format("Monday 2 January 2006")
	}
	e.Content = markdownRuns(body, attach)
	// Old exports list photos without referring to them in the text.
	for _, p := range de.Photos {
		if !used[p.Identifier] {
			e.Content = append(e.Content, diary.Run{Text: "\n"}, attach(p.Identifier))
		}
	}
	e.Content = merge(e.Content)
	return e
}

// picture stores a photo of the export and returns its image ID.
func (res *Result) picture(p media, files map[string]*zip.File) (string, error) {
	typ := strings.ToLower(p.Type)
	f := files["photos/"+p.MD5+"."+typ]
	if f == nil {
		return "", errors.New("missing")
	}
	r, err := f.Open()
	if err != nil {
		return "", err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxPicture))
	if err != nil {
		return "", err
	}
	mime := map[string]string{"jpeg": "image/jpeg", "jpg": "image/jpeg", "png": "image/png",
		"gif": "image/gif", "heic": "image/heic", "webp": "image/webp"}[typ]
	if mime == "" {
		mime = "application/octet-stream"
	}
	if m, small, ok := picture.Shrink(data, picture.MaxSide); ok {
		mime, data = m, small
	}
	if typ == "heic" {
		res.HEIC++
	}
	res.Pictures++
	id := diary.NewID()
	res.Images[id] = &diary.Image{MIME: mime, Data: data}
	return id, nil
}

// splitTitle takes the first line as the title, as Day One shows it, when it
// is a heading or short enough to be one.
func splitTitle(text string) (title, body string) {
	text = strings.TrimLeft(text, "\n")
	first, rest, _ := strings.Cut(text, "\n")
	heading := headingRe.MatchString(first)
	plain := inlineText(headingRe.ReplaceAllString(first, ""))
	if strings.TrimSpace(plain) == "" || momentRe.MatchString(first) ||
		(!heading && utf8.RuneCountInString(plain) > 80) {
		return "", text
	}
	return strings.TrimSpace(plain), strings.TrimLeft(rest, "\n")
}

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+`)
	momentRe  = regexp.MustCompile(`!\[[^\]]*\]\(dayone-moment:/{1,2}(?:[A-Za-z]+/)?([A-Za-z0-9]+)\)`)
	linkRe    = regexp.MustCompile(`(^|[^\\!])\[([^\]]*)\]\(([^)\s]+)\)`)
	listRe    = regexp.MustCompile(`^(\s*)[-*+]\s+(\[[ xX]\]\s+)?`)
	quoteRe   = regexp.MustCompile(`^\s*>\s?`)
)

// headingSizes are the font sizes, in points, of Markdown headings 1 to 3.
var headingSizes = []float64{18, 16, 14}

// markdownRuns converts Day One's Markdown to runs: headings, bold, italic,
// inline code and code blocks keep their look; lists, checklists and quotes
// become plain text; attachments are replaced by attach.
func markdownRuns(md string, attach func(ident string) diary.Run) []diary.Run {
	var out []diary.Run
	code := false
	lines := strings.Split(strings.TrimRight(md, "\n"), "\n")
	for i, line := range lines {
		if i > 0 {
			out = append(out, diary.Run{Text: "\n"})
		}
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			// The fence line disappears, with the line break before it.
			code = !code
			if i > 0 {
				out = out[:len(out)-1]
			}
			continue
		}
		if code {
			out = append(out, diary.Run{Text: line, Font: "Monospace"})
			continue
		}
		base := diary.Run{}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			base.Bold = true
			if lvl := len(m[1]); lvl <= len(headingSizes) {
				base.Size = headingSizes[lvl-1]
			}
			line = line[len(m[0]):]
		} else if m := listRe.FindStringSubmatch(line); m != nil {
			bullet := "• "
			switch strings.ToLower(strings.TrimSpace(m[2])) {
			case "[ ]":
				bullet = "☐ "
			case "[x]":
				bullet = "☑ "
			}
			out = append(out, diary.Run{Text: m[1] + bullet})
			line = line[len(m[0]):]
		} else if loc := quoteRe.FindStringIndex(line); loc != nil {
			base.Italic = true
			line = line[loc[1]:]
		}
		// Split around attachments, then parse the text in between.
		last := 0
		for _, m := range momentRe.FindAllStringSubmatchIndex(line, -1) {
			out = append(out, inlineRuns(line[last:m[0]], base)...)
			out = append(out, attach(line[m[2]:m[3]]))
			last = m[1]
		}
		out = append(out, inlineRuns(line[last:], base)...)
	}
	return out
}

// inlineRuns parses emphasis, inline code, links and backslash escapes in
// one line. Emphasis left open at the end of the line is dropped.
func inlineRuns(s string, base diary.Run) []diary.Run {
	s = linkRe.ReplaceAllString(s, "$1$2 ($3)")
	var out []diary.Run
	var b strings.Builder
	cur := base
	flush := func() {
		if b.Len() > 0 {
			r := cur
			r.Text = b.String()
			out = append(out, r)
			b.Reset()
		}
	}
	code := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isPunct(s[i+1]):
			b.WriteByte(s[i+1])
			i++
		case c == '`':
			flush()
			code = !code
			if code {
				cur.Font = "Monospace"
			} else {
				cur.Font = base.Font
			}
		case code:
			b.WriteByte(c)
		case (c == '*' || c == '_') && i+1 < len(s) && s[i+1] == c:
			flush()
			cur.Bold = !cur.Bold
			i++
		case c == '~' && i+1 < len(s) && s[i+1] == '~':
			i++ // strikethrough has no equivalent: keep the text
		case c == '*' || (c == '_' && wordBoundary(s, i)):
			flush()
			cur.Italic = !cur.Italic
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return out
}

// inlineText is the plain text of a Markdown line.
func inlineText(s string) string {
	var b strings.Builder
	for _, r := range inlineRuns(s, diary.Run{}) {
		b.WriteString(r.Text)
	}
	return b.String()
}

func isPunct(c byte) bool {
	return c < 128 && strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

// wordBoundary reports whether the "_" at i is not inside a word, as in
// snake_case, where it is not emphasis.
func wordBoundary(s string, i int) bool {
	isWord := func(c byte) bool {
		return c >= 0x80 || c == '_' || c >= '0' && c <= '9' || (c|0x20) >= 'a' && (c|0x20) <= 'z'
	}
	before := i > 0 && isWord(s[i-1])
	after := i+1 < len(s) && isWord(s[i+1])
	return !(before && after)
}

// merge joins adjacent runs of the same style, as the editor stores them.
func merge(runs []diary.Run) []diary.Run {
	var out []diary.Run
	for _, r := range runs {
		if r.Image == "" && r.Text == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].SameStyle(r) {
			out[n-1].Text += r.Text
			continue
		}
		out = append(out, r)
	}
	for len(out) > 0 && out[len(out)-1].Image == "" && strings.TrimSpace(out[len(out)-1].Text) == "" {
		out = out[:len(out)-1]
	}
	return out
}
