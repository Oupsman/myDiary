// Package ui implements the GTK 4 user interface.
package ui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"github.com/oupsman/mydiary/internal/diary"
	"github.com/oupsman/mydiary/internal/syncclient"
	"github.com/oupsman/mydiary/internal/vault"
)

const AppID = "io.github.oupsman.MyDiary"

// Run starts the application and returns its exit code.
func Run(args []string) int {
	app := gtk.NewApplication(AppID, gio.ApplicationDefaultFlags)
	var a *App
	app.ConnectActivate(func() {
		if a != nil {
			a.win.Present()
			return
		}
		a = newApp(app)
		a.win.Present()
	})
	return app.Run(args)
}

type entryRow struct {
	row     *gtk.ListBoxRow
	title   *gtk.Label
	date    *gtk.Label
	snippet *gtk.Label
}

// App is the main window and the state of the open diary.
type App struct {
	app *gtk.Application
	win *gtk.ApplicationWindow

	titleLbl    *gtk.Label
	subtitleLbl *gtk.Label
	stack       *gtk.Stack // "welcome" | "diary"
	recentBtn   *gtk.Button

	search *gtk.SearchEntry
	list   *gtk.ListBox
	rows   map[string]*entryRow

	pageStack  *gtk.Stack // "empty" | "entry"
	titleEntry *gtk.Entry
	dateLbl    *gtk.Label
	editor     *Editor

	actions map[string]*gio.SimpleAction

	// Open diary state.
	path    string
	diary   *diary.Diary
	key     *vault.Key
	dirty   bool
	current *diary.Entry

	entryDirty     bool // editor content not yet copied into current
	loadingEntry   bool
	suppressSelect bool
	forceClose     bool

	// Sync.
	syncCfg    syncclient.Config
	syncBtn    *gtk.Button
	syncing    bool
	syncAgain  bool // a sync was requested while one was running
	syncPaused bool // waiting for the user (e.g. new passphrase needed)
	saveSeq    int  // incremented on every save made by the user
}

func newApp(app *gtk.Application) *App {
	a := &App{app: app, rows: map[string]*entryRow{}, actions: map[string]*gio.SimpleAction{}}
	loadCSS()

	a.win = gtk.NewApplicationWindow(app)
	a.win.SetDefaultSize(1100, 760)
	a.win.SetTitle("myDiary")

	a.buildHeader()
	a.stack = gtk.NewStack()
	a.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	a.stack.AddNamed(a.buildWelcome(), "welcome")
	a.stack.AddNamed(a.buildDiaryView(), "diary")
	a.win.SetChild(a.stack)

	a.setupActions()
	registerAccels(app)
	a.syncCfg = loadSyncConfig()
	coreglib.TimeoutSecondsAdd(syncInterval, func() bool {
		if a.diary != nil && !a.dirty && !a.syncPaused {
			a.startSync()
		}
		return true
	})

	a.win.ConnectCloseRequest(func() bool {
		if a.forceClose || a.diary == nil || !a.dirty {
			a.lock()
			return false
		}
		askUnsaved(&a.win.Window, func(choice string) {
			switch choice {
			case "save":
				if a.save() {
					a.forceClose = true
					a.win.Close()
				}
			case "discard":
				a.forceClose = true
				a.win.Close()
			}
		})
		return true // keep open until answered
	})

	a.showWelcome()
	return a
}

func loadCSS() {
	css := gtk.NewCSSProvider()
	css.LoadFromString(styleSheet)
	gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), css, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
}

const styleSheet = `
.welcome-title { font-size: 28pt; font-weight: 800; }
.entry-list row { padding: 10px 14px; border-radius: 8px; margin: 2px 6px; }
.entry-list .row-title { font-weight: 700; }
.entry-list .row-date { font-size: 9pt; }
.entry-list .row-snippet { font-size: 9pt; }
.sidebar-pane { background-color: alpha(currentColor, 0.03); }
.entry-title, .entry-title:focus-within {
  font-size: 20pt; font-weight: 700;
  background: none; border: none; box-shadow: none; outline: none;
  min-height: 48px;
}
.editor-toolbar { padding: 6px 12px; }
.editor-toolbar spinbutton { min-width: 0; }
textview.diary-text { font-size: 12pt; }
.keycap {
  font-family: monospace; font-size: 9pt;
  padding: 2px 8px; border-radius: 6px;
  background-color: alpha(currentColor, 0.08);
  border: 1px solid alpha(currentColor, 0.15);
}
`

// ---- widgets ---------------------------------------------------------------

func (a *App) buildHeader() {
	hb := gtk.NewHeaderBar()

	titles := gtk.NewBox(gtk.OrientationVertical, 0)
	titles.SetVAlign(gtk.AlignCenter)
	a.titleLbl = gtk.NewLabel("myDiary")
	a.titleLbl.AddCSSClass("title")
	a.subtitleLbl = gtk.NewLabel("")
	a.subtitleLbl.AddCSSClass("subtitle")
	a.subtitleLbl.SetEllipsize(pango.EllipsizeMiddle)
	titles.Append(a.titleLbl)
	titles.Append(a.subtitleLbl)
	hb.SetTitleWidget(titles)

	newEntry := gtk.NewButtonFromIconName("document-new-symbolic")
	newEntry.SetTooltipText("New entry (Ctrl+N)")
	newEntry.SetActionName("win.new-entry")
	hb.PackStart(newEntry)

	menu := gio.NewMenu()
	sec1 := gio.NewMenu()
	sec1.Append("_New diary…", "win.new-diary")
	sec1.Append("_Open diary…", "win.open-diary")
	sec1.Append("_Get diary from server…", "win.download-diary")
	sec1.Append("_Save", "win.save")
	sec1.Append("Change _passphrase…", "win.change-passphrase")
	sec1.Append("_Close diary", "win.close-diary")
	menu.AppendSection("", sec1)
	secSync := gio.NewMenu()
	secSync.Append("S_ync now", "win.sync-now")
	secSync.Append("Sync _settings…", "win.sync-settings")
	menu.AppendSection("", secSync)
	sec2 := gio.NewMenu()
	sec2.Append("_Keyboard shortcuts", "win.shortcuts")
	sec2.Append("_Quit", "win.quit")
	menu.AppendSection("", sec2)

	mb := gtk.NewMenuButton()
	mb.SetIconName("open-menu-symbolic")
	mb.SetTooltipText("Main menu (F10)")
	mb.SetMenuModel(menu)
	mb.SetPrimary(true)
	hb.PackEnd(mb)

	a.syncBtn = gtk.NewButtonFromIconName("emblem-synchronizing-symbolic")
	a.syncBtn.SetActionName("win.sync-now")
	a.syncBtn.AddCSSClass("flat")
	a.syncBtn.SetVisible(false)
	hb.PackEnd(a.syncBtn)

	save := gtk.NewButtonFromIconName("document-save-symbolic")
	save.SetTooltipText("Save (Ctrl+S)")
	save.SetActionName("win.save")
	hb.PackEnd(save)

	a.win.SetTitlebar(hb)
}

func (a *App) buildWelcome() gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetVAlign(gtk.AlignCenter)
	box.SetHAlign(gtk.AlignCenter)

	icon := gtk.NewImageFromIconName("accessories-text-editor-symbolic")
	icon.SetPixelSize(96)
	icon.AddCSSClass("dim-label")
	box.Append(icon)

	t := gtk.NewLabel("myDiary")
	t.AddCSSClass("welcome-title")
	box.Append(t)
	sub := gtk.NewLabel("Your private, encrypted diary.")
	sub.AddCSSClass("dim-label")
	sub.SetMarginBottom(18)
	box.Append(sub)

	a.recentBtn = gtk.NewButton()
	a.recentBtn.AddCSSClass("suggested-action")
	a.recentBtn.AddCSSClass("pill")
	a.recentBtn.ConnectClicked(func() {
		if p := loadState().LastPath; p != "" {
			a.openPath(p)
		}
	})
	box.Append(a.recentBtn)

	open := gtk.NewButtonWithMnemonic("_Open a diary…  (Ctrl+O)")
	open.AddCSSClass("pill")
	open.SetActionName("win.open-diary")
	box.Append(open)

	create := gtk.NewButtonWithMnemonic("_Create a new diary…  (Ctrl+Shift+N)")
	create.AddCSSClass("pill")
	create.SetActionName("win.new-diary")
	box.Append(create)

	download := gtk.NewButtonWithMnemonic("_Get a diary from the sync server…  (Ctrl+Shift+O)")
	download.AddCSSClass("pill")
	download.SetActionName("win.download-diary")
	box.Append(download)

	help := gtk.NewLabel("Press F1 to see all keyboard shortcuts.")
	help.AddCSSClass("dim-label")
	help.SetMarginTop(18)
	box.Append(help)
	return box
}

func (a *App) buildDiaryView() gtk.Widgetter {
	// Sidebar: search + entry list.
	a.search = gtk.NewSearchEntry()
	a.search.SetPlaceholderText("Search entries (Ctrl+F)")
	a.search.SetMarginTop(8)
	a.search.SetMarginBottom(8)
	a.search.SetMarginStart(8)
	a.search.SetMarginEnd(8)
	a.search.ConnectSearchChanged(func() {
		a.commit()
		a.list.InvalidateFilter()
	})
	a.search.ConnectStopSearch(func() {
		a.search.SetText("")
		a.focusList()
	})
	a.search.ConnectActivate(a.focusList)

	a.list = gtk.NewListBox()
	a.list.AddCSSClass("entry-list")
	a.list.AddCSSClass("navigation-sidebar")
	a.list.SetSelectionMode(gtk.SelectionBrowse)
	empty := gtk.NewLabel("No entries yet.\nPress Ctrl+N to write one.")
	empty.SetJustify(gtk.JustifyCenter)
	empty.AddCSSClass("dim-label")
	empty.SetMarginTop(24)
	a.list.SetPlaceholder(empty)
	a.list.SetFilterFunc(a.filterRow)
	a.list.ConnectRowSelected(func(row *gtk.ListBoxRow) {
		if a.suppressSelect {
			return
		}
		if row == nil {
			a.showEntry(nil)
			return
		}
		a.showEntry(a.diary.Entry(row.Name()))
	})
	// Activating a row (Enter, double click) moves to the text.
	a.list.ConnectRowActivated(func(*gtk.ListBoxRow) { a.focusEditor() })
	del := gtk.NewShortcutController()
	del.AddShortcut(gtk.NewShortcut(gtk.NewShortcutTriggerParseString("Delete"), gtk.NewNamedAction("win.delete-entry")))
	a.list.AddController(del)

	listScroll := gtk.NewScrolledWindow()
	listScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	listScroll.SetChild(a.list)
	listScroll.SetVExpand(true)

	sidebar := gtk.NewBox(gtk.OrientationVertical, 0)
	sidebar.AddCSSClass("sidebar-pane")
	sidebar.Append(a.search)
	sidebar.Append(listScroll)
	sidebar.SetSizeRequest(260, -1)

	// Entry page.
	a.editor = NewEditor()
	a.editor.Parent = func() *gtk.Window { return &a.win.Window }
	a.editor.OnError = func(err error) { showError(&a.win.Window, "Error", err) }
	a.editor.OnChanged = func() {
		a.entryDirty = true
		a.markDirty()
	}

	a.titleEntry = gtk.NewEntry()
	a.titleEntry.AddCSSClass("entry-title")
	a.titleEntry.SetPlaceholderText("Title")
	a.titleEntry.SetMarginStart(40)
	a.titleEntry.SetMarginEnd(40)
	a.titleEntry.SetMarginTop(16)
	a.titleEntry.ConnectChanged(func() {
		if a.loadingEntry || a.current == nil {
			return
		}
		a.current.Title = a.titleEntry.Text()
		a.current.Modified = time.Now()
		a.updateRow(a.current)
		a.markDirty()
	})
	a.titleEntry.ConnectActivate(a.focusEditor)

	a.dateLbl = gtk.NewLabel("")
	a.dateLbl.SetXAlign(0)
	a.dateLbl.AddCSSClass("dim-label")
	a.dateLbl.SetMarginStart(48)
	a.dateLbl.SetMarginBottom(8)

	page := gtk.NewBox(gtk.OrientationVertical, 0)
	page.Append(a.titleEntry)
	page.Append(a.dateLbl)
	page.Append(a.editor.Widget)

	emptyPage := gtk.NewBox(gtk.OrientationVertical, 8)
	emptyPage.SetVAlign(gtk.AlignCenter)
	ei := gtk.NewImageFromIconName("document-edit-symbolic")
	ei.SetPixelSize(64)
	ei.AddCSSClass("dim-label")
	emptyPage.Append(ei)
	el := gtk.NewLabel("Select an entry, or press Ctrl+N to start a new one.")
	el.AddCSSClass("dim-label")
	emptyPage.Append(el)

	a.pageStack = gtk.NewStack()
	a.pageStack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	a.pageStack.AddNamed(emptyPage, "empty")
	a.pageStack.AddNamed(page, "entry")
	a.pageStack.SetHExpand(true)

	paned := gtk.NewPaned(gtk.OrientationHorizontal)
	paned.SetStartChild(sidebar)
	paned.SetEndChild(a.pageStack)
	paned.SetResizeStartChild(false)
	paned.SetShrinkStartChild(false)
	paned.SetPosition(300)
	return paned
}

// ---- actions ---------------------------------------------------------------

func (a *App) setupActions() {
	add := func(name string, f func()) {
		act := gio.NewSimpleAction(name, nil)
		act.ConnectActivate(func(*glib.Variant) { f() })
		a.win.AddAction(act)
		a.actions[name] = act
	}
	add("new-diary", a.newDiary)
	add("open-diary", func() {
		a.closeDiary(func() { chooseDiaryToOpen(&a.win.Window, a.openPath) })
	})
	add("save", func() { a.save() })
	add("change-passphrase", a.changePassphrase)
	add("close-diary", func() { a.closeDiary(nil) })
	add("quit", func() { a.win.Close() })

	add("new-entry", a.newEntry)
	add("delete-entry", a.deleteEntry)
	add("search", func() { a.search.GrabFocus() })
	add("focus-list", a.focusList)
	add("focus-title", func() { a.titleEntry.GrabFocus() })
	add("focus-editor", a.focusEditor)
	add("prev-entry", func() { a.moveSelection(-1) })
	add("next-entry", func() { a.moveSelection(1) })

	add("bold", a.editor.ToggleBold)
	add("italic", a.editor.ToggleItalic)
	add("underline", a.editor.ToggleUnderline)
	add("font", a.editor.ChooseFont)
	add("grow-font", a.editor.GrowFont)
	add("shrink-font", a.editor.ShrinkFont)
	add("clear-format", a.editor.ClearFormatting)
	add("insert-image", a.editor.InsertImageFromFile)
	add("paste-image", a.editor.PasteImage)

	add("sync-now", func() {
		a.syncPaused = false
		if a.dirty && !a.save() {
			return
		}
		a.startSync()
	})
	add("sync-settings", a.syncSettings)
	add("download-diary", func() { a.closeDiary(a.downloadDiary) })

	add("shortcuts", func() { showShortcuts(&a.win.Window) })
	add("fullscreen", func() {
		if a.win.IsFullscreen() {
			a.win.Unfullscreen()
		} else {
			a.win.Fullscreen()
		}
	})
}

var (
	diaryActions = []string{"save", "change-passphrase", "close-diary", "new-entry",
		"search", "focus-list", "prev-entry", "next-entry", "sync-now"}
	entryActions = []string{"delete-entry", "focus-title", "focus-editor", "bold", "italic",
		"underline", "font", "grow-font", "shrink-font", "clear-format", "insert-image", "paste-image"}
)

func (a *App) updateActions() {
	for _, n := range diaryActions {
		a.actions[n].SetEnabled(a.diary != nil)
	}
	for _, n := range entryActions {
		a.actions[n].SetEnabled(a.current != nil)
	}
}

// ---- diary lifecycle -------------------------------------------------------

func (a *App) showWelcome() {
	if p := loadState().LastPath; p != "" {
		if _, err := os.Stat(p); err == nil {
			a.recentBtn.SetLabel("Open “" + strings.TrimSuffix(filepath.Base(p), diary.FileExtension) + "”")
			a.recentBtn.SetVisible(true)
		} else {
			a.recentBtn.SetVisible(false)
		}
	} else {
		a.recentBtn.SetVisible(false)
	}
	a.stack.SetVisibleChildName("welcome")
	a.updateTitle()
	a.updateActions()
	if a.recentBtn.Visible() {
		a.recentBtn.GrabFocus()
	}
}

func (a *App) openPath(path string) {
	var (
		d *diary.Diary
		k *vault.Key
	)
	askPassword(&a.win.Window, path, func(pw []byte) string {
		dd, kk, err := diary.Load(path, pw)
		switch {
		case errors.Is(err, vault.ErrWrongPassphrase):
			return "Wrong passphrase (or the file is damaged)."
		case errors.Is(err, vault.ErrNotVault):
			return "This file is not a myDiary diary."
		case err != nil:
			return err.Error()
		}
		d, k = dd, kk
		return ""
	}, func() { a.setDiary(path, d, k) })
}

func (a *App) newDiary() {
	a.closeDiary(func() {
		chooseDiaryToCreate(&a.win.Window, func(path string) {
			var (
				d *diary.Diary
				k *vault.Key
			)
			askNewPassphrase(&a.win.Window, "New diary",
				"Choose a passphrase. It cannot be recovered: if you forget it, the diary is lost.",
				"_Create", true, false,
				func(f newPassphraseForm) string {
					key, err := vault.NewKey(f.New, vault.DefaultParams)
					if err != nil {
						return err.Error()
					}
					title := f.Title
					if title == "" {
						title = strings.TrimSuffix(filepath.Base(path), diary.FileExtension)
					}
					nd := diary.New(title)
					if err := diary.Save(path, nd, key); err != nil {
						key.Wipe()
						return err.Error()
					}
					d, k = nd, key
					return ""
				},
				func() { a.setDiary(path, d, k) })
		})
	})
}

func (a *App) setDiary(path string, d *diary.Diary, k *vault.Key) {
	a.path, a.diary, a.key = path, d, k
	a.dirty = false
	a.current = nil
	a.editor.SetDiary(d)
	updateState(func(s *state) {
		s.LastPath = path
		st := s.Sync[d.ID]
		st.Path = path
		s.Sync[d.ID] = st
	})

	a.search.SetText("")
	a.rebuildList()
	a.stack.SetVisibleChildName("diary")
	if row := a.list.RowAtIndex(0); row != nil {
		a.list.SelectRow(row)
		row.GrabFocus()
	} else {
		a.showEntry(nil)
	}
	a.updateTitle()
	a.updateActions()

	a.syncPaused = false
	if d.IDAssigned() {
		// Diary from an older version: persist its new sync ID right away.
		a.save()
	} else {
		a.startSync()
	}
}

// closeDiary closes the open diary (asking about unsaved changes) and then
// calls then, if not nil.
func (a *App) closeDiary(then func()) {
	if a.diary == nil {
		if then != nil {
			then()
		}
		return
	}
	finish := func() {
		a.lock()
		a.showWelcome()
		if then != nil {
			then()
		}
	}
	a.commit()
	if !a.dirty {
		finish()
		return
	}
	askUnsaved(&a.win.Window, func(choice string) {
		switch choice {
		case "save":
			if a.save() {
				finish()
			}
		case "discard":
			finish()
		}
	})
}

// lock forgets the diary and wipes the key from memory.
func (a *App) lock() {
	a.key.Wipe()
	a.key = nil
	a.diary = nil
	a.current = nil
	a.path = ""
	a.dirty = false
	a.entryDirty = false
	a.editor.SetDiary(nil)
	a.loadingEntry = true
	a.titleEntry.SetText("")
	a.loadingEntry = false
	a.editor.Load(nil)
	a.suppressSelect = true
	a.list.RemoveAll()
	a.suppressSelect = false
	a.rows = map[string]*entryRow{}
	a.syncAgain = false
	a.updateSyncStatus(syncIdle, "")
}

// save saves the diary on disk and then uploads it to the sync server.
func (a *App) save() bool {
	if !a.saveLocal() {
		return false
	}
	a.saveSeq++
	id := a.diary.ID
	updateState(func(s *state) {
		st := s.Sync[id]
		st.Pending = true
		s.Sync[id] = st
	})
	a.startSync()
	return true
}

// saveLocal writes the diary file only.
func (a *App) saveLocal() bool {
	if a.diary == nil {
		return false
	}
	a.commit()
	if err := diary.Save(a.path, a.diary, a.key); err != nil {
		showError(&a.win.Window, "Could not save the diary", err)
		return false
	}
	a.dirty = false
	a.updateTitle()
	return true
}

func (a *App) changePassphrase() {
	if a.diary == nil {
		return
	}
	var newKey *vault.Key
	path := a.path
	askNewPassphrase(&a.win.Window, "Change passphrase",
		"The diary will be saved and re-encrypted with the new passphrase.",
		"_Change", false, true,
		func(f newPassphraseForm) string {
			data, err := os.ReadFile(path)
			if err != nil {
				return err.Error()
			}
			pt, k, err := vault.Open(data, f.Current)
			if err != nil {
				return "The current passphrase is incorrect."
			}
			wipe(pt)
			k.Wipe()
			newKey, err = vault.NewKey(f.New, vault.DefaultParams)
			if err != nil {
				return err.Error()
			}
			return ""
		},
		func() {
			old := a.key
			a.key = newKey
			if !a.save() {
				a.key = old
				newKey.Wipe()
				return
			}
			old.Wipe()
		})
}

func (a *App) markDirty() {
	if !a.dirty {
		a.dirty = true
		a.updateTitle()
	}
}

func (a *App) updateTitle() {
	if a.diary == nil {
		a.titleLbl.SetText("myDiary")
		a.subtitleLbl.SetText("")
		a.subtitleLbl.SetVisible(false)
		a.win.SetTitle("myDiary")
		return
	}
	t := a.diary.Title
	if a.dirty {
		t = "• " + t
	}
	a.titleLbl.SetText(t)
	a.subtitleLbl.SetText(a.path)
	a.subtitleLbl.SetVisible(true)
	a.win.SetTitle(t + " — myDiary")
}

// ---- entries ---------------------------------------------------------------

// commit copies the editor content into the current entry.
func (a *App) commit() {
	if a.current == nil || !a.entryDirty {
		return
	}
	a.current.Content = a.editor.Content()
	a.current.Modified = time.Now()
	a.entryDirty = false
	a.updateRow(a.current)
}

func (a *App) showEntry(e *diary.Entry) {
	a.commit()
	a.current = e
	a.updateActions()
	if e == nil {
		a.pageStack.SetVisibleChildName("empty")
		return
	}
	a.loadingEntry = true
	a.titleEntry.SetText(e.Title)
	a.loadingEntry = false
	a.updateDate()
	a.editor.Load(e.Content)
	a.entryDirty = false
	a.pageStack.SetVisibleChildName("entry")
}

func (a *App) updateDate() {
	if a.current == nil {
		return
	}
	a.dateLbl.SetText(a.current.Created.Format("Monday 2 January 2006, 15:04"))
}

func (a *App) newEntry() {
	if a.diary == nil {
		return
	}
	a.commit()
	e := a.diary.AddEntry()
	a.markDirty()
	a.search.SetText("")
	a.rebuildList()
	a.list.SelectRow(a.rows[e.ID].row)
	a.focusEditor()
}

func (a *App) deleteEntry() {
	e := a.current
	if e == nil {
		return
	}
	title := e.Title
	if title == "" {
		title = "this entry"
	}
	askConfirm(&a.win.Window, "Delete “"+title+"”?",
		"The entry and its pictures will be removed when the diary is saved.",
		"_Delete", func() {
			idx := a.rows[e.ID].row.Index()
			a.entryDirty = false
			a.current = nil
			a.diary.RemoveEntry(e.ID)
			a.markDirty()
			a.rebuildList()
			row := a.list.RowAtIndex(idx)
			if row == nil && idx > 0 {
				row = a.list.RowAtIndex(idx - 1)
			}
			if row != nil {
				a.list.SelectRow(row)
				row.GrabFocus()
			} else {
				a.showEntry(nil)
			}
		})
}

func (a *App) rebuildList() {
	a.suppressSelect = true
	a.list.RemoveAll()
	a.rows = map[string]*entryRow{}
	for _, e := range a.diary.SortedEntries() {
		r := &entryRow{
			row:     gtk.NewListBoxRow(),
			title:   gtk.NewLabel(""),
			date:    gtk.NewLabel(""),
			snippet: gtk.NewLabel(""),
		}
		r.row.SetName(e.ID)
		box := gtk.NewBox(gtk.OrientationVertical, 2)
		for _, l := range []*gtk.Label{r.title, r.date, r.snippet} {
			l.SetXAlign(0)
			l.SetEllipsize(pango.EllipsizeEnd)
			box.Append(l)
		}
		r.title.AddCSSClass("row-title")
		r.date.AddCSSClass("row-date")
		r.date.AddCSSClass("dim-label")
		r.snippet.AddCSSClass("row-snippet")
		r.snippet.AddCSSClass("dim-label")
		r.row.SetChild(box)
		a.rows[e.ID] = r
		a.updateRow(e)
		a.list.Append(r.row)
	}
	if a.current != nil {
		if r := a.rows[a.current.ID]; r != nil {
			a.list.SelectRow(r.row)
		}
	}
	a.suppressSelect = false
}

func (a *App) updateRow(e *diary.Entry) {
	r := a.rows[e.ID]
	if r == nil {
		return
	}
	t := strings.TrimSpace(e.Title)
	if t == "" {
		t = "Untitled"
	}
	r.title.SetText(t)
	r.date.SetText(e.Created.Format("Mon 2 Jan 2006"))
	snippet := strings.Join(strings.Fields(strings.ReplaceAll(e.PlainText(), "￼", "")), " ")
	if len([]rune(snippet)) > 90 {
		snippet = string([]rune(snippet)[:90]) + "…"
	}
	r.snippet.SetText(snippet)
	r.snippet.SetVisible(snippet != "")
}

func (a *App) filterRow(row *gtk.ListBoxRow) bool {
	q := strings.ToLower(strings.TrimSpace(a.search.Text()))
	if q == "" || a.diary == nil {
		return true
	}
	e := a.diary.Entry(row.Name())
	if e == nil {
		return false
	}
	return strings.Contains(strings.ToLower(e.Title), q) ||
		strings.Contains(strings.ToLower(e.PlainText()), q)
}

func (a *App) focusList() {
	if row := a.list.SelectedRow(); row != nil && row.ChildVisible() {
		row.GrabFocus()
		return
	}
	for i := 0; ; i++ {
		row := a.list.RowAtIndex(i)
		if row == nil {
			a.list.GrabFocus()
			return
		}
		if row.ChildVisible() {
			a.list.SelectRow(row)
			row.GrabFocus()
			return
		}
	}
}

func (a *App) focusEditor() {
	if a.current != nil {
		a.editor.View.GrabFocus()
	}
}

func (a *App) moveSelection(delta int) {
	cur := a.list.SelectedRow()
	i := -1
	if cur != nil {
		i = cur.Index()
	}
	for {
		i += delta
		row := a.list.RowAtIndex(i)
		if i < 0 || row == nil {
			return
		}
		if row.ChildVisible() {
			a.list.SelectRow(row)
			row.GrabFocus()
			return
		}
	}
}

// ---- persisted UI state (never contains diary content) ---------------------

type state struct {
	LastPath string `json:"last_path"`
	// Sync is the per-diary sync state, by diary ID.
	Sync map[string]diarySyncState `json:"sync,omitempty"`
}

type diarySyncState struct {
	Version int64 `json:"version"` // server version the local file matches
	Pending bool  `json:"pending"` // local changes not uploaded yet
	// Path is where this diary lives on this computer. It is never sent to
	// the server; it only helps recognise diaries when downloading.
	Path string `json:"path,omitempty"`
}

func statePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "mydiary", "state.json")
}

func loadState() state {
	var s state
	if p := statePath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			json.Unmarshal(b, &s)
		}
	}
	if s.Sync == nil {
		s.Sync = map[string]diarySyncState{}
	}
	return s
}

func updateState(f func(*state)) {
	s := loadState()
	f(&s)
	saveState(s)
}

func saveState(s state) {
	p := statePath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	b, _ := json.Marshal(s)
	os.WriteFile(p, b, 0o600)
}
