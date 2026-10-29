package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"time"

	"fyne.io/systray"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/godbus/dbus/v5"

	"github.com/oupsman/mydiary/internal/autostart"
	"github.com/oupsman/mydiary/internal/reminder"
)

// reminderInterval is how often, in seconds, the daily reminder is checked.
const reminderInterval = 60

const reminderNotification = "daily-reminder"

// ---- tray icon -------------------------------------------------------------

// startTray shows the icon in the notification area (StatusNotifierItem over
// D-Bus). Menu clicks arrive on other goroutines and are handed to GTK.
func (a *App) startTray() {
	start, end := systray.RunWithExternalLoop(nil, nil)
	start()
	a.app.ConnectShutdown(end)

	systray.SetIcon(trayIcon())
	systray.SetTitle("myDiary")
	systray.SetTooltip("myDiary")
	show := systray.AddMenuItem("Show / hide myDiary", "")
	write := systray.AddMenuItem("Write an entry", "")
	systray.AddSeparator()
	a.trayReminder = systray.AddMenuItemCheckbox("Daily reminder", "Remind me to write every day", a.reminder.Enabled)
	a.trayAutostart = systray.AddMenuItemCheckbox("Start at login", "Start myDiary in the tray when you log in",
		autostart.Enabled(AppID))
	settings := systray.AddMenuItem("Reminder and startup…", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "")

	onMain := func(f func()) { coreglib.IdleAdd(f) }
	systray.SetOnTapped(func() { onMain(a.toggleWindow) })
	go func() {
		for {
			select {
			case <-show.ClickedCh:
				onMain(a.toggleWindow)
			case <-write.ClickedCh:
				onMain(a.writeEntry)
			case <-a.trayReminder.ClickedCh:
				onMain(func() {
					cfg := a.reminder
					cfg.Enabled = !cfg.Enabled
					a.setReminder(cfg)
				})
			case <-a.trayAutostart.ClickedCh:
				onMain(func() { a.setAutostart(!autostart.Enabled(AppID)) })
			case <-settings.ClickedCh:
				onMain(func() {
					a.win.Present()
					a.reminderSettings()
				})
			case <-quit.ClickedCh:
				onMain(func() { a.actions["quit"].Activate(nil) })
			}
		}
	}()
}

// trayHosted reports whether a desktop component currently shows tray icons.
// Without one, closing the window must quit: a hidden window could not be
// brought back.
func trayHosted() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

func (a *App) toggleWindow() {
	if a.win.Visible() && a.win.IsActive() {
		a.win.Close() // hides to the tray, locking the diary
		return
	}
	a.win.Present()
}

// writeEntry brings the window up on a new entry, unlocking the last diary
// first if none is open.
func (a *App) writeEntry() {
	a.app.WithdrawNotification(reminderNotification)
	a.win.Present()
	if a.diary != nil {
		a.newEntry()
		return
	}
	p := loadState().LastPath
	if p == "" {
		return
	}
	if _, err := os.Stat(p); err != nil {
		return
	}
	a.openPathThen(p, a.newEntry)
}

// trayIcon draws a 64×64 notebook: a cover with a darker spine and a page
// with ruled lines.
func trayIcon() []byte {
	const n = 64
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	cover := color.RGBA{0x35, 0x84, 0xe4, 0xff}
	spine := color.RGBA{0x1c, 0x5f, 0xb0, 0xff}
	page := color.RGBA{0xfa, 0xfa, 0xfa, 0xff}
	line := color.RGBA{0x9a, 0xb8, 0xdc, 0xff}
	fill := func(x0, y0, x1, y1 int, c color.RGBA) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.SetRGBA(x, y, c)
			}
		}
	}
	// Cover with cut corners, so it reads as rounded at tray sizes.
	fill(10, 4, 56, 60, cover)
	for _, p := range [][2]int{{10, 4}, {55, 4}, {10, 59}, {55, 59}} {
		img.SetRGBA(p[0], p[1], color.RGBA{})
	}
	fill(10, 4, 18, 60, spine)
	fill(23, 10, 51, 54, page)
	for y := 18; y <= 46; y += 7 {
		fill(26, y, 48, y+2, line)
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// ---- daily reminder --------------------------------------------------------

func loadReminder() reminder.Config {
	if r := loadState().Reminder; r != nil {
		return *r
	}
	return reminder.Default
}

func (a *App) setReminder(cfg reminder.Config) {
	a.reminder = cfg
	updateState(func(s *state) { s.Reminder = &cfg })
	if a.trayReminder != nil {
		if cfg.Enabled {
			a.trayReminder.Check()
		} else {
			a.trayReminder.Uncheck()
		}
	}
	if !cfg.Enabled {
		a.app.WithdrawNotification(reminderNotification)
	}
}

func (a *App) setupReminder() {
	a.reminder = loadReminder()
	write := gio.NewSimpleAction("write", nil)
	write.ConnectActivate(func(*glib.Variant) { a.writeEntry() })
	a.app.AddAction(write)
	coreglib.TimeoutSecondsAdd(reminderInterval, func() bool {
		a.checkReminder(time.Now())
		return true
	})
}

func (a *App) checkReminder(now time.Time) {
	// No need to remind someone who is using the app or already wrote today.
	if !a.reminder.Due(now) || a.win.IsActive() || a.wroteOn(now) {
		return
	}
	n := gio.NewNotification("Time to write")
	n.SetBody("How was your day? Take a moment to write it down in your diary.")
	n.SetIcon(gio.NewThemedIcon("accessories-text-editor"))
	n.SetDefaultAction("app.write")
	n.AddButton("Write now", "app.write")
	a.app.SendNotification(reminderNotification, n)

	a.reminderDone(now)
}

func (a *App) reminderDone(now time.Time) {
	cfg := a.reminder
	cfg.LastDone = reminder.Day(now)
	a.setReminder(cfg)
}

// noteSaved marks today's reminder done once the diary is saved with an
// entry for today: the diary is locked when the window closes, so the
// reminder could not see that entry later.
func (a *App) noteSaved() {
	now := time.Now()
	if a.reminder.LastDone != reminder.Day(now) && a.wroteOn(now) {
		a.reminderDone(now)
	}
}

// wroteOn reports whether the open diary has a non-empty entry dated the
// same day as now. A locked diary cannot be checked, so it counts as no.
func (a *App) wroteOn(now time.Time) bool {
	if a.diary == nil {
		return false
	}
	a.commit()
	day := reminder.Day(now)
	for _, e := range a.diary.Entries {
		if reminder.Day(e.Created.In(now.Location())) == day && (e.Title != "" || e.PlainText() != "") {
			return true
		}
	}
	return false
}

// setAutostart turns starting at login (hidden in the tray) on or off.
func (a *App) setAutostart(on bool) {
	var err error
	if on {
		var exe string
		if exe, err = os.Executable(); err == nil {
			err = autostart.Enable(AppID, exe)
		}
	} else {
		err = autostart.Disable(AppID)
	}
	if err != nil {
		a.win.Present()
		showError(&a.win.Window, "Could not change starting at login", err)
	}
	if autostart.Enabled(AppID) {
		a.trayAutostart.Check()
	} else {
		a.trayAutostart.Uncheck()
	}
}

func (a *App) reminderSettings() {
	d := newDialog(&a.win.Window, "Reminder and startup",
		"A notification reminds you to write, unless you already did that day. "+
			"myDiary must be running: closing the window keeps it in the system tray.")

	on := gtk.NewCheckButtonWithMnemonic("_Remind me to write every day")
	on.SetActive(a.reminder.Enabled)
	d.content.Append(on)

	twoDigits := func(s *gtk.SpinButton) {
		s.ConnectOutput(func() bool {
			s.SetText(fmt.Sprintf("%02d", s.ValueAsInt()))
			return true
		})
	}
	hour := gtk.NewSpinButtonWithRange(0, 23, 1)
	hour.SetWrap(true)
	twoDigits(hour)
	hour.SetValue(float64(a.reminder.Hour))
	minute := gtk.NewSpinButtonWithRange(0, 59, 1)
	minute.SetWrap(true)
	twoDigits(minute)
	minute.SetValue(float64(a.reminder.Minute))

	at := gtk.NewBox(gtk.OrientationHorizontal, 6)
	lbl := gtk.NewLabelWithMnemonic("_At")
	lbl.SetMnemonicWidget(hour)
	at.Append(lbl)
	at.Append(hour)
	at.Append(gtk.NewLabel(":"))
	at.Append(minute)
	at.SetSensitive(on.Active())
	on.ConnectToggled(func() { at.SetSensitive(on.Active()) })
	d.content.Append(at)

	login := gtk.NewCheckButtonWithMnemonic("_Start myDiary in the tray when I log in")
	login.SetActive(autostart.Enabled(AppID))
	login.SetMarginTop(8)
	d.content.Append(login)

	d.addButton("_Cancel", "", d.win.Close)
	ok := d.addButton("_Save", "suggested-action", func() {
		cfg := a.reminder
		cfg.Enabled = on.Active()
		cfg.Hour, cfg.Minute = hour.ValueAsInt(), minute.ValueAsInt()
		a.setReminder(cfg)
		d.win.Close()
		if login.Active() != autostart.Enabled(AppID) {
			a.setAutostart(login.Active())
		}
	})
	d.win.SetDefaultWidget(ok)
	d.win.Present()
	on.GrabFocus()
}
