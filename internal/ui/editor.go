package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"github.com/oupsman/mydiary/internal/diary"
)

const (
	defaultFontSize = 12.0
	minFontSize     = 6.0
	maxFontSize     = 96.0

	tagBold      = "b"
	tagItalic    = "i"
	tagUnderline = "u"
	tagFontPfx   = "font:"
	tagSizePfx   = "size:"

	objectReplacementChar = '￼'
)

// style is the character formatting at a point of the text. Zero values mean
// "use the editor default" and produce no tag.
type style struct {
	Font      string
	Size      float64
	Bold      bool
	Italic    bool
	Underline bool
}

func (s style) run(text string) diary.Run {
	return diary.Run{Text: text, Font: s.Font, Size: s.Size, Bold: s.Bold, Italic: s.Italic, Underline: s.Underline}
}

func (s style) effectiveSize() float64 {
	if s.Size == 0 {
		return defaultFontSize
	}
	return s.Size
}

// Editor is a word-processor-like rich text editor with embedded images.
type Editor struct {
	Widget *gtk.Box
	View   *gtk.TextView

	buf     *gtk.TextBuffer
	toolbar *gtk.Box

	fontBtn   *gtk.Button
	sizeSpin  *gtk.SpinButton
	boldBtn   *gtk.ToggleButton
	italicBtn *gtk.ToggleButton
	underBtn  *gtk.ToggleButton

	// cur is the style applied to newly typed text.
	cur style

	loading    bool // suppress change tracking while loading an entry
	updatingUI bool // suppress toolbar signal handlers while syncing them

	pending      bool
	pendingStart int
	pendingLen   int

	diary    *diary.Diary
	textures map[string]*gdk.Texture // image ID -> display texture
	imageIDs map[uintptr]string      // texture GObject pointer -> image ID

	// OnChanged is called whenever the user modifies the content.
	OnChanged func()
	// Parent is used as transient parent for dialogs.
	Parent func() *gtk.Window
	// OnError reports a user-facing error.
	OnError func(error)
}

// NewEditor builds the editor widget tree.
func NewEditor() *Editor {
	ed := &Editor{
		textures: map[string]*gdk.Texture{},
		imageIDs: map[uintptr]string{},
	}

	ed.buf = gtk.NewTextBuffer(nil)
	ed.buf.SetEnableUndo(true)

	ed.View = gtk.NewTextViewWithBuffer(ed.buf)
	ed.View.SetWrapMode(gtk.WrapWordChar)
	ed.View.SetLeftMargin(48)
	ed.View.SetRightMargin(48)
	ed.View.SetTopMargin(24)
	ed.View.SetBottomMargin(48)
	ed.View.SetPixelsBelowLines(4)
	ed.View.AddCSSClass("diary-text")
	ed.View.SetVExpand(true)
	ed.View.SetHExpand(true)

	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(ed.View)
	scroll.SetVExpand(true)

	ed.buildToolbar()

	ed.Widget = gtk.NewBox(gtk.OrientationVertical, 0)
	ed.Widget.Append(ed.toolbar)
	ed.Widget.Append(gtk.NewSeparator(gtk.OrientationHorizontal))
	ed.Widget.Append(scroll)

	ed.buf.ConnectInsertText(func(loc *gtk.TextIter, text string, _ int) {
		if ed.loading {
			return
		}
		ed.pending = true
		ed.pendingStart = loc.Offset()
		ed.pendingLen = utf8.RuneCountInString(text)
	})
	// "changed" is emitted at the end of the default insert-text handler, so
	// the inserted range is in place by now: give it the current style.
	ed.buf.ConnectChanged(func() {
		if ed.loading {
			return
		}
		if ed.pending {
			ed.pending = false
			start := ed.buf.IterAtOffset(ed.pendingStart)
			end := ed.buf.IterAtOffset(ed.pendingStart + ed.pendingLen)
			ed.applyStyle(ed.cur, start, end)
		}
		ed.changed()
	})
	ed.buf.ConnectMarkSet(func(loc *gtk.TextIter, mark *gtk.TextMark) {
		if ed.loading || mark.Name() != "insert" {
			return
		}
		ed.cur = ed.styleBefore(loc)
		ed.syncToolbar()
	})

	return ed
}

func (ed *Editor) buildToolbar() {
	ed.toolbar = gtk.NewBox(gtk.OrientationHorizontal, 6)
	ed.toolbar.AddCSSClass("toolbar")
	ed.toolbar.AddCSSClass("editor-toolbar")

	ed.fontBtn = gtk.NewButtonWithLabel("Default font")
	ed.fontBtn.SetTooltipText("Font family (Ctrl+Shift+F)")
	ed.fontBtn.SetFocusOnClick(false)
	ed.fontBtn.ConnectClicked(ed.ChooseFont)

	ed.sizeSpin = gtk.NewSpinButtonWithRange(minFontSize, maxFontSize, 1)
	ed.sizeSpin.SetValue(defaultFontSize)
	ed.sizeSpin.SetTooltipText("Font size (Ctrl+] / Ctrl+[)")
	ed.sizeSpin.ConnectValueChanged(func() {
		if ed.updatingUI {
			return
		}
		ed.SetSize(float64(ed.sizeSpin.ValueAsInt()))
	})

	toggle := func(icon, tip string, f func()) *gtk.ToggleButton {
		b := gtk.NewToggleButton()
		b.SetIconName(icon)
		b.SetTooltipText(tip)
		b.SetFocusOnClick(false)
		b.AddCSSClass("flat")
		b.ConnectToggled(func() {
			if !ed.updatingUI {
				f()
			}
		})
		return b
	}
	ed.boldBtn = toggle("format-text-bold-symbolic", "Bold (Ctrl+B)", ed.ToggleBold)
	ed.italicBtn = toggle("format-text-italic-symbolic", "Italic (Ctrl+I)", ed.ToggleItalic)
	ed.underBtn = toggle("format-text-underline-symbolic", "Underline (Ctrl+U)", ed.ToggleUnderline)

	clear := gtk.NewButtonFromIconName("edit-clear-all-symbolic")
	clear.SetTooltipText("Clear formatting (Ctrl+M)")
	clear.SetFocusOnClick(false)
	clear.AddCSSClass("flat")
	clear.ConnectClicked(ed.ClearFormatting)

	img := gtk.NewButtonFromIconName("insert-image-symbolic")
	img.SetTooltipText("Insert picture (Ctrl+Shift+I)")
	img.SetFocusOnClick(false)
	img.AddCSSClass("flat")
	img.ConnectClicked(ed.InsertImageFromFile)

	ed.toolbar.Append(ed.fontBtn)
	ed.toolbar.Append(ed.sizeSpin)
	ed.toolbar.Append(gtk.NewSeparator(gtk.OrientationVertical))
	ed.toolbar.Append(ed.boldBtn)
	ed.toolbar.Append(ed.italicBtn)
	ed.toolbar.Append(ed.underBtn)
	ed.toolbar.Append(clear)
	ed.toolbar.Append(gtk.NewSeparator(gtk.OrientationVertical))
	ed.toolbar.Append(img)
}

func (ed *Editor) changed() {
	if !ed.loading && ed.OnChanged != nil {
		ed.OnChanged()
	}
}

// SetDiary sets the diary images are stored in, and resets the image cache.
func (ed *Editor) SetDiary(d *diary.Diary) {
	ed.diary = d
	ed.textures = map[string]*gdk.Texture{}
	ed.imageIDs = map[uintptr]string{}
}

// ReplaceDiary points the editor at a new copy of the same diary (after a
// sync merge), keeping the decoded pictures shown in the buffer.
func (ed *Editor) ReplaceDiary(d *diary.Diary) {
	ed.diary = d
}

// ---- tags ------------------------------------------------------------------

func (ed *Editor) tag(name string) *gtk.TextTag {
	table := ed.buf.TagTable()
	if t := table.Lookup(name); t != nil {
		return t
	}
	t := gtk.NewTextTag(name)
	switch {
	case name == tagBold:
		t.SetObjectProperty("weight", int(pango.WeightBold))
	case name == tagItalic:
		t.SetObjectProperty("style", int(pango.StyleItalic))
	case name == tagUnderline:
		t.SetObjectProperty("underline", int(pango.UnderlineSingle))
	case strings.HasPrefix(name, tagFontPfx):
		t.SetObjectProperty("family", strings.TrimPrefix(name, tagFontPfx))
	case strings.HasPrefix(name, tagSizePfx):
		size, _ := strconv.ParseFloat(strings.TrimPrefix(name, tagSizePfx), 64)
		t.SetObjectProperty("size-points", size)
	}
	table.Add(t)
	return t
}

func sizeTagName(size float64) string {
	return tagSizePfx + strconv.FormatFloat(size, 'f', -1, 64)
}

func styleFromTags(tags []*gtk.TextTag) style {
	var s style
	for _, t := range tags {
		name := t.ObjectProperty("name").(string)
		switch {
		case name == tagBold:
			s.Bold = true
		case name == tagItalic:
			s.Italic = true
		case name == tagUnderline:
			s.Underline = true
		case strings.HasPrefix(name, tagFontPfx):
			s.Font = strings.TrimPrefix(name, tagFontPfx)
		case strings.HasPrefix(name, tagSizePfx):
			s.Size, _ = strconv.ParseFloat(strings.TrimPrefix(name, tagSizePfx), 64)
		}
	}
	return s
}

// styleBefore returns the style of the character typed text at loc would
// continue: the one before it, or the one after it at the start of the text.
func (ed *Editor) styleBefore(loc *gtk.TextIter) style {
	it := loc.Copy()
	if !it.BackwardChar() {
		it = loc.Copy()
	}
	return styleFromTags(it.Tags())
}

func (ed *Editor) styleTags() []*gtk.TextTag {
	var tags []*gtk.TextTag
	ed.buf.TagTable().ForEach(func(t *gtk.TextTag) {
		tags = append(tags, t)
	})
	return tags
}

// applyStyle replaces any formatting in [start, end) by s.
func (ed *Editor) applyStyle(s style, start, end *gtk.TextIter) {
	ed.buf.RemoveAllTags(start, end)
	if s.Bold {
		ed.buf.ApplyTag(ed.tag(tagBold), start, end)
	}
	if s.Italic {
		ed.buf.ApplyTag(ed.tag(tagItalic), start, end)
	}
	if s.Underline {
		ed.buf.ApplyTag(ed.tag(tagUnderline), start, end)
	}
	if s.Font != "" {
		ed.buf.ApplyTag(ed.tag(tagFontPfx+s.Font), start, end)
	}
	if s.Size != 0 {
		ed.buf.ApplyTag(ed.tag(sizeTagName(s.Size)), start, end)
	}
}

// removePrefixed removes every tag whose name starts with prefix from the range.
func (ed *Editor) removePrefixed(prefix string, start, end *gtk.TextIter) {
	for _, t := range ed.styleTags() {
		if strings.HasPrefix(t.ObjectProperty("name").(string), prefix) {
			ed.buf.RemoveTag(t, start, end)
		}
	}
}

// rangeAll reports whether every character in [start, end) satisfies pred.
func rangeAll(start, end *gtk.TextIter, pred func(style) bool) bool {
	it := start.Copy()
	for it.Compare(end) < 0 {
		if !pred(styleFromTags(it.Tags())) {
			return false
		}
		it.ForwardChar()
	}
	return true
}

// ---- formatting commands ---------------------------------------------------

func (ed *Editor) toggleFlag(name string, get func(*style) *bool) {
	start, end, ok := ed.buf.SelectionBounds()
	if !ok {
		f := get(&ed.cur)
		*f = !*f
		ed.syncToolbar()
		return
	}
	all := rangeAll(start, end, func(s style) bool { return *get(&s) })
	if all {
		ed.buf.RemoveTag(ed.tag(name), start, end)
	} else {
		ed.buf.ApplyTag(ed.tag(name), start, end)
	}
	*get(&ed.cur) = !all
	ed.syncToolbar()
	ed.changed()
}

func (ed *Editor) ToggleBold() {
	ed.toggleFlag(tagBold, func(s *style) *bool { return &s.Bold })
}

func (ed *Editor) ToggleItalic() {
	ed.toggleFlag(tagItalic, func(s *style) *bool { return &s.Italic })
}

func (ed *Editor) ToggleUnderline() {
	ed.toggleFlag(tagUnderline, func(s *style) *bool { return &s.Underline })
}

// SetFont sets the font family of the selection, or of the text to be typed.
func (ed *Editor) SetFont(family string) {
	ed.cur.Font = family
	if start, end, ok := ed.buf.SelectionBounds(); ok {
		ed.removePrefixed(tagFontPfx, start, end)
		if family != "" {
			ed.buf.ApplyTag(ed.tag(tagFontPfx+family), start, end)
		}
		ed.changed()
	}
	ed.syncToolbar()
}

// SetSize sets the font size of the selection, or of the text to be typed.
func (ed *Editor) SetSize(size float64) {
	size = max(minFontSize, min(maxFontSize, size))
	ed.cur.Size = size
	if start, end, ok := ed.buf.SelectionBounds(); ok {
		ed.removePrefixed(tagSizePfx, start, end)
		ed.buf.ApplyTag(ed.tag(sizeTagName(size)), start, end)
		ed.changed()
	}
	ed.syncToolbar()
}

// GrowFont / ShrinkFont change the size by one point.
func (ed *Editor) GrowFont()   { ed.SetSize(ed.cur.effectiveSize() + 1) }
func (ed *Editor) ShrinkFont() { ed.SetSize(ed.cur.effectiveSize() - 1) }

// ClearFormatting removes all formatting from the selection (or resets the
// typing style when nothing is selected).
func (ed *Editor) ClearFormatting() {
	ed.cur = style{}
	if start, end, ok := ed.buf.SelectionBounds(); ok {
		ed.buf.RemoveAllTags(start, end)
		ed.changed()
	}
	ed.syncToolbar()
}

// ChooseFont opens the font family chooser.
func (ed *Editor) ChooseFont() {
	dlg := gtk.NewFontDialog()
	dlg.SetTitle("Choose a font")
	dlg.SetModal(true)
	dlg.ChooseFamily(context.Background(), ed.Parent(), nil, func(res gio.AsyncResulter) {
		fam, err := dlg.ChooseFamilyFinish(res)
		ed.View.GrabFocus()
		if err != nil || fam == nil {
			return // cancelled
		}
		ed.SetFont(pango.BaseFontFamily(fam).Name())
	})
}

func (ed *Editor) syncToolbar() {
	ed.updatingUI = true
	defer func() { ed.updatingUI = false }()
	ed.boldBtn.SetActive(ed.cur.Bold)
	ed.italicBtn.SetActive(ed.cur.Italic)
	ed.underBtn.SetActive(ed.cur.Underline)
	ed.sizeSpin.SetValue(ed.cur.effectiveSize())
	if ed.cur.Font == "" {
		ed.fontBtn.SetLabel("Default font")
	} else {
		ed.fontBtn.SetLabel(ed.cur.Font)
	}
}

// ---- images ----------------------------------------------------------------

// texture returns the display texture for an image ID, decoding it on first use.
func (ed *Editor) texture(id string) *gdk.Texture {
	if t, ok := ed.textures[id]; ok {
		return t
	}
	img := ed.diary.Images[id]
	if img == nil {
		return nil
	}
	t, err := displayTexture(img.Data)
	if err != nil {
		return nil
	}
	ed.textures[id] = t
	ed.imageIDs[t.Native()] = id
	return t
}

// imageID maps a paintable found in the buffer back to its stored image. A
// paintable we do not know (e.g. pasted from elsewhere) is stored as PNG.
func (ed *Editor) imageID(p *gdk.Paintable) (string, bool) {
	if id, ok := ed.imageIDs[p.Native()]; ok {
		return id, true
	}
	saver, ok := p.Cast().(interface{ SaveToPNGBytes() *glib.Bytes })
	if !ok {
		return "", false
	}
	id := ed.diary.AddImage("image/png", saver.SaveToPNGBytes().Data())
	ed.imageIDs[p.Native()] = id
	return id, true
}

// insertImage stores data in the diary and embeds it at the cursor.
func (ed *Editor) insertImage(mime string, data []byte) error {
	tex, err := displayTexture(data)
	if err != nil {
		return err
	}
	id := ed.diary.AddImage(mime, data)
	ed.textures[id] = tex
	ed.imageIDs[tex.Native()] = id
	ed.buf.DeleteSelection(true, true)
	ed.buf.InsertPaintable(ed.buf.IterAtMark(ed.buf.GetInsert()), tex)
	ed.View.GrabFocus()
	return nil
}

// InsertImageFromFile asks for a picture file and embeds it at the cursor.
func (ed *Editor) InsertImageFromFile() {
	chooseImageFile(ed.Parent(), func(path string) {
		mime, data, err := readImageFile(path)
		if err == nil {
			err = ed.insertImage(mime, data)
		}
		if err != nil {
			ed.OnError(fmt.Errorf("cannot insert picture: %w", err))
		}
	})
}

// PasteImage embeds the picture currently in the clipboard, if any.
func (ed *Editor) PasteImage() {
	cb := ed.View.Clipboard()
	cb.ReadTextureAsync(context.Background(), func(res gio.AsyncResulter) {
		tx, err := cb.ReadTextureFinish(res)
		if err != nil || tx == nil {
			ed.OnError(fmt.Errorf("the clipboard does not contain a picture"))
			return
		}
		png := gdk.BaseTexture(tx).SaveToPNGBytes().Data()
		if err := ed.insertImage("image/png", png); err != nil {
			ed.OnError(err)
		}
	})
}

// ---- (de)serialization -----------------------------------------------------

// Load replaces the editor content with runs. Undo history is reset.
func (ed *Editor) Load(runs []diary.Run) {
	ed.loading = true
	ed.buf.BeginIrreversibleAction()
	ed.buf.SetText("")
	for _, r := range runs {
		end := ed.buf.EndIter()
		if r.Image != "" {
			if t := ed.texture(r.Image); t != nil {
				ed.buf.InsertPaintable(end, t)
			}
			continue
		}
		off := end.Offset()
		ed.buf.Insert(end, r.Text)
		ed.applyStyle(style{Font: r.Font, Size: r.Size, Bold: r.Bold, Italic: r.Italic, Underline: r.Underline},
			ed.buf.IterAtOffset(off), ed.buf.EndIter())
	}
	ed.buf.EndIrreversibleAction()
	ed.buf.PlaceCursor(ed.buf.StartIter())
	ed.loading = false
	ed.cur = ed.styleBefore(ed.buf.StartIter())
	ed.syncToolbar()
}

// Content serializes the buffer into runs.
func (ed *Editor) Content() []diary.Run {
	var runs []diary.Run
	add := func(r diary.Run) {
		if n := len(runs); n > 0 && r.Image == "" && runs[n-1].SameStyle(r) {
			runs[n-1].Text += r.Text
			return
		}
		runs = append(runs, r)
	}

	it := ed.buf.StartIter()
	for !it.IsEnd() {
		if p := it.Paintable(); p != nil {
			if id, ok := ed.imageID(p); ok {
				add(diary.Run{Image: id})
			}
			it.ForwardChar()
			continue
		}
		start := it.Copy()
		next := it.Copy()
		next.ForwardToTagToggle(nil)
		text := ed.buf.Slice(start, next, true)
		// Stop before the next embedded image so it gets its own run.
		n := 0
		for _, r := range text {
			if r == objectReplacementChar && n > 0 {
				at := start.Copy()
				at.ForwardChars(n)
				if at.Paintable() != nil {
					next = at
					text = ed.buf.Slice(start, next, true)
					break
				}
			}
			n++
		}
		add(styleFromTags(start.Tags()).run(text))
		it = next
	}
	return runs
}
