package ui

import (
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// shortcut binds keyboard accelerators to a window action. An empty action
// documents a key handled by GTK itself (e.g. undo in the text view).
type shortcut struct {
	action string
	accels []string
	desc   string
}

type shortcutGroup struct {
	title string
	items []shortcut
}

// shortcuts is the single source of truth for key bindings: it is used both
// to register accelerators and to build the help window.
var shortcuts = []shortcutGroup{
	{"Diary", []shortcut{
		{"new-diary", []string{"<Control><Shift>n"}, "Create a new diary"},
		{"open-diary", []string{"<Control>o"}, "Open a diary"},
		{"save", []string{"<Control>s"}, "Save the diary"},
		{"change-passphrase", []string{"<Control><Shift>p"}, "Change the passphrase"},
		{"close-diary", []string{"<Control>w"}, "Close and lock the diary"},
		{"quit", []string{"<Control>q"}, "Quit"},
	}},
	{"Entries", []shortcut{
		{"new-entry", []string{"<Control>n"}, "New entry"},
		{"delete-entry", []string{"<Control><Shift>Delete"}, "Delete the entry (or Delete in the list)"},
		{"search", []string{"<Control>f"}, "Search entries"},
		{"focus-list", []string{"<Control>l", "F6"}, "Go to the entry list"},
		{"focus-title", []string{"<Control>t"}, "Edit the entry title"},
		{"focus-editor", []string{"<Control>e"}, "Go to the entry text"},
		{"prev-entry", []string{"<Alt>Up", "<Control>Page_Up"}, "Previous entry"},
		{"next-entry", []string{"<Alt>Down", "<Control>Page_Down"}, "Next entry"},
	}},
	{"Formatting", []shortcut{
		{"bold", []string{"<Control>b"}, "Bold"},
		{"italic", []string{"<Control>i"}, "Italic"},
		{"underline", []string{"<Control>u"}, "Underline"},
		{"font", []string{"<Control><Shift>f"}, "Choose font"},
		{"grow-font", []string{"<Control>bracketright", "<Control>plus", "<Control>equal"}, "Larger text"},
		{"shrink-font", []string{"<Control>bracketleft", "<Control>minus"}, "Smaller text"},
		{"clear-format", []string{"<Control>m"}, "Clear formatting"},
		{"insert-image", []string{"<Control><Shift>i"}, "Insert a picture"},
		{"paste-image", []string{"<Control><Shift>v"}, "Paste a picture from the clipboard"},
		{"", []string{"<Control>z"}, "Undo"},
		{"", []string{"<Control><Shift>z"}, "Redo"},
	}},
	{"General", []shortcut{
		{"shortcuts", []string{"<Control>question", "F1"}, "Show keyboard shortcuts"},
		{"fullscreen", []string{"F11"}, "Toggle full screen"},
	}},
}

func registerAccels(app *gtk.Application) {
	for _, g := range shortcuts {
		for _, s := range g.items {
			if s.action != "" {
				app.SetAccelsForAction("win."+s.action, s.accels)
			}
		}
	}
}

func accelLabel(accel string) string {
	key, mods, ok := gtk.AcceleratorParse(accel)
	if !ok {
		return accel
	}
	return gtk.AcceleratorGetLabel(key, mods)
}

// showShortcuts opens the keyboard shortcuts help window.
func showShortcuts(parent *gtk.Window) {
	d := newDialog(parent, "Keyboard shortcuts", "")
	d.win.SetResizable(true)
	d.win.SetDefaultSize(560, 640)

	grid := gtk.NewGrid()
	grid.SetColumnSpacing(24)
	grid.SetRowSpacing(6)
	row := 0
	for _, g := range shortcuts {
		h := gtk.NewLabel(g.title)
		h.AddCSSClass("heading")
		h.SetXAlign(0)
		if row > 0 {
			h.SetMarginTop(12)
		}
		grid.Attach(h, 0, row, 2, 1)
		row++
		for _, s := range g.items {
			desc := gtk.NewLabel(s.desc)
			desc.SetXAlign(0)
			desc.SetHExpand(true)
			keys := gtk.NewBox(gtk.OrientationHorizontal, 6)
			keys.SetHAlign(gtk.AlignEnd)
			for _, a := range s.accels {
				k := gtk.NewLabel(accelLabel(a))
				k.AddCSSClass("keycap")
				keys.Append(k)
			}
			grid.Attach(desc, 0, row, 1, 1)
			grid.Attach(keys, 1, row, 1, 1)
			row++
		}
	}

	scroll := gtk.NewScrolledWindow()
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scroll.SetChild(grid)
	scroll.SetVExpand(true)
	d.content.Append(scroll)
	d.content.SetVExpand(true)

	ok := d.addButton("_Close", "suggested-action", d.win.Close)
	d.win.SetDefaultWidget(ok)
	d.win.Present()
	ok.GrabFocus()
}
