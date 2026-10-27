package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/oupsman/mydiary/internal/dayone"
	"github.com/oupsman/mydiary/internal/diary"
	"github.com/oupsman/mydiary/internal/export"
)

// ---- entry date ------------------------------------------------------------

// buildDateButton returns the button showing the entry date, with a popover
// to change it.
func (a *App) buildDateButton() *gtk.MenuButton {
	btn := gtk.NewMenuButton()
	btn.AddCSSClass("flat")
	btn.SetTooltipText("Change the date (Ctrl+Shift+D)")

	cal := gtk.NewCalendar()
	hour := gtk.NewSpinButtonWithRange(0, 23, 1)
	minute := gtk.NewSpinButtonWithRange(0, 59, 1)
	hour.SetWrap(true)
	minute.SetWrap(true)
	timeRow := gtk.NewBox(gtk.OrientationHorizontal, 6)
	timeLbl := gtk.NewLabelWithMnemonic("_Time")
	timeLbl.SetMnemonicWidget(hour)
	timeLbl.SetHExpand(true)
	timeLbl.SetXAlign(0)
	timeRow.Append(timeLbl)
	timeRow.Append(hour)
	timeRow.Append(gtk.NewLabel(":"))
	timeRow.Append(minute)

	set := gtk.NewButtonWithMnemonic("_Set date")
	set.AddCSSClass("suggested-action")

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.Append(cal)
	box.Append(timeRow)
	box.Append(set)
	pop := gtk.NewPopover()
	pop.SetChild(box)
	btn.SetPopover(pop)

	pop.ConnectShow(func() {
		if a.current == nil {
			return
		}
		t := a.current.Created.Local()
		cal.SelectDay(glib.NewDateTimeFromGo(t))
		hour.SetValue(float64(t.Hour()))
		minute.SetValue(float64(t.Minute()))
		cal.GrabFocus()
	})
	set.ConnectClicked(func() {
		hour.Update()
		minute.Update()
		d := cal.Date()
		t := time.Date(d.Year(), time.Month(d.Month()), d.DayOfMonth(),
			hour.ValueAsInt(), minute.ValueAsInt(), 0, 0, time.Local)
		pop.Popdown()
		a.setCreated(t)
		a.focusEditor()
	})
	return btn
}

func (a *App) setCreated(t time.Time) {
	if a.current == nil || a.current.Created.Equal(t) {
		return
	}
	a.commit()
	a.current.Created = t
	a.current.Modified = time.Now()
	a.updateDate()
	a.rebuildList() // the entry moves in the list
	a.markDirty()
}

// ---- tags ------------------------------------------------------------------

func (a *App) buildTagsEntry() *gtk.Entry {
	e := gtk.NewEntry()
	e.AddCSSClass("tags-entry")
	e.SetPlaceholderText("Tags, separated by commas (Ctrl+Shift+T)")
	e.SetMarginStart(40)
	e.SetMarginEnd(40)
	e.SetMarginBottom(8)
	e.ConnectChanged(func() {
		if a.loadingEntry || a.current == nil {
			return
		}
		tags := diary.NormalizeTags(strings.Split(e.Text(), ","))
		if slices.Equal(tags, a.current.Tags) {
			return
		}
		a.current.Tags = tags
		a.current.Modified = time.Now()
		a.updateRow(a.current)
		a.markDirty()
	})
	e.ConnectActivate(a.focusEditor)
	return e
}

// ---- on this day -----------------------------------------------------------

func (a *App) buildOnThisDayButton() *gtk.ToggleButton {
	b := gtk.NewToggleButton()
	b.SetIconName("x-office-calendar-symbolic")
	b.SetTooltipText("On this day: entries written on today's date in earlier years (Ctrl+D)")
	b.ConnectToggled(func() {
		a.commit()
		a.updateEmptyLabel()
		a.list.InvalidateFilter()
		a.selectVisibleEntry()
	})
	a.onThisDay = b
	a.updateEmptyLabel()
	return b
}

// selectVisibleEntry moves the selection to the first listed entry when the
// open one was filtered out, so that the page matches the list.
func (a *App) selectVisibleEntry() {
	if a.current != nil {
		if r := a.rows[a.current.ID]; r != nil && a.filterRow(r.row) {
			return
		}
	}
	for i := 0; ; i++ {
		row := a.list.RowAtIndex(i)
		if row == nil {
			break
		}
		if a.filterRow(row) {
			a.list.SelectRow(row)
			return
		}
	}
	a.list.UnselectAll()
	a.showEntry(nil)
}

func (a *App) updateEmptyLabel() {
	if a.onThisDay.Active() {
		a.emptyLbl.SetText("Nothing was written on " + time.Now().Format("2 January") + "\nin earlier years.")
	} else {
		a.emptyLbl.SetText("No entries yet.\nPress Ctrl+N to write one.")
	}
}

// ---- import and export -----------------------------------------------------

// importDayOne adds the entries of a Day One export to the open diary.
// Entries imported before are skipped.
func (a *App) importDayOne() {
	if a.diary == nil {
		return
	}
	a.commit()
	chooseDayOneExport(&a.win.Window, func(path string) {
		known := map[string]bool{}
		for _, e := range a.diary.Entries {
			known[e.ID] = true
		}
		for id := range a.diary.Deleted {
			known[id] = true
		}
		done := showBusy(&a.win.Window, "Importing from Day One…",
			"Pictures are shrunk on the way: this can take a while.")
		go func() {
			res, err := dayone.Import(path, func(id string) bool { return known[id] })
			coreglib.IdleAdd(func() {
				done()
				if err != nil {
					showError(&a.win.Window, "Import failed", err)
					return
				}
				if a.diary == nil {
					return
				}
				a.addImported(res)
			})
		}()
	})
}

func (a *App) addImported(res *dayone.Result) {
	for id, img := range res.Images {
		a.diary.Images[id] = img
	}
	a.diary.Entries = append(a.diary.Entries, res.Entries...)
	if len(res.Entries) > 0 {
		a.markDirty()
		a.rebuildList()
	}

	var b strings.Builder
	b.WriteString(plural(len(res.Entries), "entry", "entries") + " imported")
	if res.Journals > 1 {
		fmt.Fprintf(&b, " from %d journals (each entry is tagged with its journal)", res.Journals)
	}
	b.WriteString(".")
	if res.Skipped > 0 {
		b.WriteString("\n" + plural(res.Skipped, "entry was", "entries were") + " already in this diary.")
	}
	if res.Pictures > 0 {
		b.WriteString("\n" + plural(res.Pictures, "picture", "pictures") + ".")
	}
	if res.HEIC > 0 {
		b.WriteString("\n" + plural(res.HEIC, "picture is", "pictures are") +
			" in HEIC format: shown only if this computer can read HEIC.")
	}
	if res.Missing > 0 {
		b.WriteString("\n" + plural(res.Missing, "picture was", "pictures were") + " missing from the export.")
	}
	if res.Unsupported > 0 {
		b.WriteString("\n" + plural(res.Unsupported, "video, audio recording or PDF was", "videos, audio recordings or PDFs were") + " left out.")
	}
	if len(res.Entries) > 0 {
		b.WriteString("\n\nSave the diary (Ctrl+S) to keep them.")
	}
	showMessage(&a.win.Window, "Day One import", b.String())
}

// exportDiary writes an unencrypted Markdown + JSON copy of the diary.
func (a *App) exportDiary() {
	if a.diary == nil {
		return
	}
	a.commit()
	askConfirm(&a.win.Window, "Export an unencrypted copy?",
		"The export holds every entry and picture in plain files that anyone with the file can read. Keep it somewhere safe, and delete it when you no longer need it.",
		"_Export", func() {
			chooseExportFile(&a.win.Window, a.diary.Title, func(path string) {
				snapshot := a.diary.Clone()
				done := showBusy(&a.win.Window, "Exporting…", "")
				go func() {
					err := writeExport(path, snapshot)
					coreglib.IdleAdd(func() {
						done()
						if err != nil {
							showError(&a.win.Window, "Export failed", err)
							return
						}
						showMessage(&a.win.Window, "Export done",
							plural(len(snapshot.Entries), "entry", "entries")+" exported to "+path)
					})
				}()
			})
		})
}

func writeExport(path string, d *diary.Diary) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = export.WriteZip(f, d)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// plural formats a count with the singular or plural noun: "1 entry".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---- dialogs ---------------------------------------------------------------

func showMessage(parent *gtk.Window, heading, body string) {
	d := newDialog(parent, heading, body)
	ok := d.addButton("_OK", "suggested-action", d.win.Close)
	d.win.SetDefaultWidget(ok)
	d.win.Present()
	ok.GrabFocus()
}

// showBusy shows a modal "working" dialog that cannot be closed, and
// returns the function that closes it.
func showBusy(parent *gtk.Window, heading, body string) (done func()) {
	d := newDialog(parent, heading, body)
	sp := gtk.NewSpinner()
	sp.SetSizeRequest(32, 32)
	sp.Start()
	d.content.Append(sp)
	finished := false
	d.win.ConnectCloseRequest(func() bool { return !finished })
	d.win.Present()
	return func() {
		finished = true
		d.win.Close()
	}
}

func chooseDayOneExport(parent *gtk.Window, f func(path string)) {
	exp := gtk.NewFileFilter()
	exp.SetName("Day One exports")
	exp.AddSuffix("zip")
	exp.AddSuffix("json")
	dlg := gtk.NewFileDialog()
	dlg.SetTitle("Import from Day One")
	dlg.SetModal(true)
	dlg.SetFilters(filterList(exp, allFilesFilter()))
	dlg.Open(context.Background(), parent, func(res gio.AsyncResulter) {
		if file, err := dlg.OpenFinish(res); err == nil && file != nil {
			f(file.Path())
		}
	})
}

func chooseExportFile(parent *gtk.Window, title string, f func(path string)) {
	zipFilter := gtk.NewFileFilter()
	zipFilter.SetName("Zip archives")
	zipFilter.AddSuffix("zip")
	dlg := gtk.NewFileDialog()
	dlg.SetTitle("Export diary")
	dlg.SetModal(true)
	dlg.SetAcceptLabel("_Export")
	dlg.SetInitialName(strings.TrimSpace(title+" export") + ".zip")
	dlg.SetFilters(filterList(zipFilter))
	dlg.Save(context.Background(), parent, func(res gio.AsyncResulter) {
		file, err := dlg.SaveFinish(res)
		if err != nil || file == nil {
			return
		}
		path := file.Path()
		if filepath.Ext(path) == "" {
			path += ".zip"
		}
		f(path)
	})
}
