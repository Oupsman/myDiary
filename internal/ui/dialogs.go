package ui

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/oupsman/mydiary/internal/diary"
)

// dialog is a small modal window with a heading, a content area and a row of
// buttons. Escape closes it.
type dialog struct {
	win     *gtk.Window
	content *gtk.Box
	buttons *gtk.Box
	errLbl  *gtk.Label
}

func newDialog(parent *gtk.Window, heading, body string) *dialog {
	d := &dialog{win: gtk.NewWindow()}
	d.win.SetModal(true)
	d.win.SetTransientFor(parent)
	d.win.SetResizable(false)
	d.win.SetTitle(heading)
	d.win.SetDefaultSize(420, -1)
	d.win.AddCSSClass("diary-dialog")

	outer := gtk.NewBox(gtk.OrientationVertical, 12)
	outer.SetMarginTop(24)
	outer.SetMarginBottom(24)
	outer.SetMarginStart(24)
	outer.SetMarginEnd(24)

	h := gtk.NewLabel(heading)
	h.AddCSSClass("title-2")
	h.SetWrap(true)
	outer.Append(h)
	if body != "" {
		b := gtk.NewLabel(body)
		b.SetWrap(true)
		b.SetMaxWidthChars(48)
		b.AddCSSClass("dim-label")
		outer.Append(b)
	}

	d.content = gtk.NewBox(gtk.OrientationVertical, 8)
	outer.Append(d.content)

	d.errLbl = gtk.NewLabel("")
	d.errLbl.AddCSSClass("error")
	d.errLbl.SetWrap(true)
	d.errLbl.SetVisible(false)
	outer.Append(d.errLbl)

	d.buttons = gtk.NewBox(gtk.OrientationHorizontal, 8)
	d.buttons.SetHAlign(gtk.AlignEnd)
	d.buttons.SetMarginTop(8)
	outer.Append(d.buttons)

	d.win.SetChild(outer)

	esc := gtk.NewShortcutController()
	esc.AddShortcut(gtk.NewShortcut(gtk.NewShortcutTriggerParseString("Escape"),
		gtk.NewCallbackAction(func(gtk.Widgetter, *glibv2.Variant) bool {
			d.win.Close()
			return true
		})))
	d.win.AddController(esc)
	return d
}

func (d *dialog) addButton(label string, class string, f func()) *gtk.Button {
	b := gtk.NewButtonWithMnemonic(label)
	if class != "" {
		b.AddCSSClass(class)
	}
	b.ConnectClicked(f)
	d.buttons.Append(b)
	return b
}

func (d *dialog) setError(msg string) {
	d.errLbl.SetText(msg)
	d.errLbl.SetVisible(msg != "")
}

func (d *dialog) setBusy(busy bool) {
	d.content.SetSensitive(!busy)
	d.buttons.SetSensitive(!busy)
	if busy {
		d.setError("")
	}
}

func (d *dialog) field(label string, w gtk.Widgetter) {
	l := gtk.NewLabelWithMnemonic(label)
	l.SetXAlign(0)
	l.SetMnemonicWidget(w)
	d.content.Append(l)
	d.content.Append(w)
}

func passwordField() *gtk.PasswordEntry {
	e := gtk.NewPasswordEntry()
	e.SetShowPeekIcon(true)
	return e
}

// showError displays a simple error message.
func showError(parent *gtk.Window, heading string, err error) {
	d := newDialog(parent, heading, err.Error())
	ok := d.addButton("_OK", "suggested-action", d.win.Close)
	d.win.SetDefaultWidget(ok)
	d.win.Present()
	ok.GrabFocus()
}

// askUnsaved asks what to do with unsaved changes. choice is "save",
// "discard" or "" when cancelled.
func askUnsaved(parent *gtk.Window, f func(choice string)) {
	d := newDialog(parent, "Save changes?",
		"The diary has unsaved changes. If you don't save, they will be lost.")
	answered := false
	answer := func(c string) func() {
		return func() {
			answered = true
			d.win.Close()
			f(c)
		}
	}
	d.addButton("_Cancel", "", answer(""))
	d.addButton("_Discard", "destructive-action", answer("discard"))
	save := d.addButton("_Save", "suggested-action", answer("save"))
	d.win.ConnectCloseRequest(func() bool {
		if !answered {
			answered = true
			f("")
		}
		return false
	})
	d.win.SetDefaultWidget(save)
	d.win.Present()
	save.GrabFocus()
}

// askConfirm asks a yes/no question with a destructive confirm button.
func askConfirm(parent *gtk.Window, heading, body, confirm string, f func()) {
	d := newDialog(parent, heading, body)
	cancel := d.addButton("_Cancel", "", d.win.Close)
	d.addButton(confirm, "destructive-action", func() {
		d.win.Close()
		f()
	})
	d.win.Present()
	cancel.GrabFocus()
}

// unlockFunc performs the (slow) unlocking work off the main thread and
// returns a user-facing error message, or "" on success.
type unlockFunc func(password []byte) string

// askPassword asks for the passphrase of an existing diary. work runs in a
// goroutine; done is called on the main thread after a successful unlock.
func askPassword(parent *gtk.Window, path string, work unlockFunc, done func()) {
	askPasswordWith(parent, "Unlock diary", filepath.Base(path), work, done)
}

func askPasswordWith(parent *gtk.Window, heading, body string, work unlockFunc, done func()) {
	d := newDialog(parent, heading, body)
	pw := passwordField()
	d.field("_Passphrase", pw)

	d.addButton("_Cancel", "", d.win.Close)
	var ok *gtk.Button
	submit := func() {
		if pw.Text() == "" {
			return
		}
		d.setBusy(true)
		ok.SetLabel("Unlocking…")
		secret := []byte(pw.Text())
		go func() {
			msg := work(secret)
			wipe(secret)
			glib.IdleAdd(func() {
				if msg != "" {
					d.setBusy(false)
					ok.SetLabel("_Unlock")
					d.setError(msg)
					pw.SetText("")
					pw.GrabFocus()
					return
				}
				d.win.Close()
				done()
			})
		}()
	}
	ok = d.addButton("_Unlock", "suggested-action", submit)
	pw.ConnectActivate(submit)
	d.win.SetDefaultWidget(ok)
	d.win.Present()
	pw.GrabFocus()
}

// newPassphraseForm is used both to create a diary and to change a passphrase.
type newPassphraseForm struct {
	Title   string
	Current []byte
	New     []byte
}

const minPassphraseLen = 8

// askNewPassphrase asks for a new passphrase (twice), and optionally a diary
// title and/or the current passphrase.
func askNewPassphrase(parent *gtk.Window, heading, body, action string, withTitle, withCurrent bool,
	work func(form newPassphraseForm) string, done func()) {

	d := newDialog(parent, heading, body)
	var title *gtk.Entry
	if withTitle {
		title = gtk.NewEntry()
		title.SetText("My diary")
		d.field("_Title", title)
	}
	var cur *gtk.PasswordEntry
	if withCurrent {
		cur = passwordField()
		d.field("_Current passphrase", cur)
	}
	pw1 := passwordField()
	d.field("_New passphrase", pw1)
	pw2 := passwordField()
	d.field("C_onfirm passphrase", pw2)

	d.addButton("_Cancel", "", d.win.Close)
	var ok *gtk.Button
	submit := func() {
		if pw1.Text() != pw2.Text() {
			d.setError("The passphrases do not match.")
			pw2.GrabFocus()
			return
		}
		if len([]rune(pw1.Text())) < minPassphraseLen {
			d.setError("Use a passphrase of at least 8 characters. Several unrelated words work well.")
			pw1.GrabFocus()
			return
		}
		form := newPassphraseForm{New: []byte(pw1.Text())}
		if title != nil {
			form.Title = strings.TrimSpace(title.Text())
		}
		if cur != nil {
			form.Current = []byte(cur.Text())
		}
		d.setBusy(true)
		go func() {
			msg := work(form)
			wipe(form.New)
			wipe(form.Current)
			glib.IdleAdd(func() {
				if msg != "" {
					d.setBusy(false)
					d.setError(msg)
					return
				}
				d.win.Close()
				done()
			})
		}()
	}
	ok = d.addButton(action, "suggested-action", submit)
	pw2.ConnectActivate(submit)
	pw1.ConnectActivate(func() { pw2.GrabFocus() })
	if cur != nil {
		cur.ConnectActivate(func() { pw1.GrabFocus() })
	}
	if title != nil {
		title.ConnectActivate(func() { pw1.GrabFocus() })
	}
	d.win.SetDefaultWidget(ok)
	d.win.Present()
	switch {
	case title != nil:
		title.GrabFocus()
	case cur != nil:
		cur.GrabFocus()
	default:
		pw1.GrabFocus()
	}
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ---- file choosers ---------------------------------------------------------

func filterList(filters ...*gtk.FileFilter) *gio.ListStore {
	store := gio.NewListStore(gtk.GTypeFileFilter)
	for _, f := range filters {
		store.Append(f.Object)
	}
	return store
}

func diaryFilter() *gtk.FileFilter {
	f := gtk.NewFileFilter()
	f.SetName("Diaries")
	f.AddSuffix(strings.TrimPrefix(diary.FileExtension, "."))
	return f
}

func allFilesFilter() *gtk.FileFilter {
	f := gtk.NewFileFilter()
	f.SetName("All files")
	f.AddPattern("*")
	return f
}

func chooseDiaryToOpen(parent *gtk.Window, f func(path string)) {
	dlg := gtk.NewFileDialog()
	dlg.SetTitle("Open diary")
	dlg.SetModal(true)
	dlg.SetFilters(filterList(diaryFilter(), allFilesFilter()))
	dlg.Open(context.Background(), parent, func(res gio.AsyncResulter) {
		if file, err := dlg.OpenFinish(res); err == nil && file != nil {
			f(file.Path())
		}
	})
}

func chooseDiaryToCreate(parent *gtk.Window, f func(path string)) {
	chooseDiaryToCreateNamed(parent, "My diary", f)
}

func chooseDiaryToCreateNamed(parent *gtk.Window, name string, f func(path string)) {
	if name == "" {
		name = "My diary"
	}
	dlg := gtk.NewFileDialog()
	dlg.SetTitle("Save diary as")
	dlg.SetModal(true)
	dlg.SetAcceptLabel("_Save")
	dlg.SetInitialName(name + diary.FileExtension)
	dlg.SetFilters(filterList(diaryFilter()))
	dlg.Save(context.Background(), parent, func(res gio.AsyncResulter) {
		file, err := dlg.SaveFinish(res)
		if err != nil || file == nil {
			return
		}
		path := file.Path()
		if filepath.Ext(path) == "" {
			path += diary.FileExtension
		}
		f(path)
	})
}

func chooseImageFile(parent *gtk.Window, f func(path string)) {
	img := gtk.NewFileFilter()
	img.SetName("Pictures")
	img.AddPixbufFormats()

	dlg := gtk.NewFileDialog()
	dlg.SetTitle("Insert picture")
	dlg.SetModal(true)
	dlg.SetFilters(filterList(img, allFilesFilter()))
	dlg.Open(context.Background(), parent, func(res gio.AsyncResulter) {
		if file, err := dlg.OpenFinish(res); err == nil && file != nil {
			f(file.Path())
		}
	})
}
