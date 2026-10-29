// Package autostart starts the desktop app at login, through an XDG
// autostart entry (~/.config/autostart).
package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// HiddenFlag makes the app start in the system tray, without a window.
const HiddenFlag = "--hidden"

// path returns the autostart entry of the app with the given ID.
func path(appID string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", appID+".desktop"), nil
}

// Enabled reports whether the app starts at login.
func Enabled(appID string) bool {
	p, err := path(appID)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Enable makes exe start at login, hidden in the tray.
func Enable(appID, exe string) error {
	p, err := path(appID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	entry := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=myDiary\n" +
		"Comment=Keeps myDiary in the system tray for the daily reminder\n" +
		"Exec=" + quoteExec(exe) + " " + HiddenFlag + "\n" +
		"Icon=accessories-text-editor\n" +
		"Terminal=false\n" +
		"NoDisplay=true\n" +
		"X-GNOME-Autostart-enabled=true\n"
	return os.WriteFile(p, []byte(entry), 0o644)
}

// Disable stops the app from starting at login.
func Disable(appID string) error {
	p, err := path(appID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// quoteExec quotes a program path for an Exec key, as the desktop entry
// specification asks: inside double quotes, `"`, "`", `$` and `\` are
// escaped with a backslash, and the backslashes are then escaped again
// because Exec is a string value. "%" is doubled for field codes.
func quoteExec(s string) string {
	if !strings.ContainsAny(s, " \t\n\"'\\><~|&;$*?#()`%") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '`', '$':
			b.WriteString(`\\`)
		case '\\':
			b.WriteString(`\\\\`)
			continue
		case '%':
			b.WriteByte('%')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
