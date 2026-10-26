package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"github.com/oupsman/mydiary/internal/diary"
	"github.com/oupsman/mydiary/internal/syncclient"
	"github.com/oupsman/mydiary/internal/syncproto"
	"github.com/oupsman/mydiary/internal/vault"
)

// syncInterval is how often, in seconds, an open and saved diary is synced.
const syncInterval = 120

const syncTimeout = 3 * time.Minute

// ---- configuration ---------------------------------------------------------

func syncConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "mydiary", "sync.json")
}

func loadSyncConfig() syncclient.Config {
	var c syncclient.Config
	if p := syncConfigPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			json.Unmarshal(b, &c)
		}
	}
	return c
}

func saveSyncConfig(c syncclient.Config) error {
	p := syncConfigPath()
	if p == "" {
		return errors.New("no configuration directory")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return vault.WriteFileAtomic(p, b) // 0600: the file holds the device token
}

// ---- status ----------------------------------------------------------------

type syncStatus int

const (
	syncIdle syncStatus = iota
	syncRunning
	syncOK
	syncOffline
	syncError
)

func (a *App) updateSyncStatus(st syncStatus, detail string) {
	if !a.syncCfg.Enabled() || a.diary == nil {
		a.syncBtn.SetVisible(false)
		return
	}
	icon, tip := "emblem-synchronizing-symbolic", "Sync now (F5)"
	switch st {
	case syncRunning:
		tip = "Syncing…"
	case syncOK:
		icon, tip = "emblem-ok-symbolic", "Synced at "+time.Now().Format("15:04")+" — Sync now (F5)"
	case syncOffline:
		icon, tip = "network-offline-symbolic", "Sync server unreachable: "+detail+" — Retry (F5)"
	case syncError:
		icon, tip = "dialog-warning-symbolic", "Sync failed: "+detail+" — Retry (F5)"
	}
	a.syncBtn.SetIconName(icon)
	a.syncBtn.SetTooltipText(tip)
	a.syncBtn.SetVisible(true)
}

func (a *App) syncLabel() string {
	return strings.TrimSuffix(filepath.Base(a.path), diary.FileExtension)
}

// ---- sync run --------------------------------------------------------------

// startSync syncs the open diary in the background. Only saved content is
// ever uploaded: callers save first (or skip syncing while dirty).
func (a *App) startSync() {
	if a.diary == nil || !a.syncCfg.Enabled() {
		a.updateSyncStatus(syncIdle, "")
		return
	}
	if a.syncing {
		a.syncAgain = true
		return
	}
	client, err := syncclient.New(a.syncCfg)
	if err != nil {
		a.updateSyncStatus(syncError, err.Error())
		return
	}
	a.commit()
	st := loadState().Sync[a.diary.ID]
	req := syncclient.Request{
		Diary:   a.diary.Clone(),
		Key:     a.key.Clone(),
		Version: st.Version,
		Pending: st.Pending,
		Label:   a.syncLabel(),
	}
	d, saveSeq := a.diary, a.saveSeq
	a.syncing = true
	a.updateSyncStatus(syncRunning, "")

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
		res, err := syncclient.Sync(ctx, client, req)
		cancel()
		req.Key.Wipe()
		coreglib.IdleAdd(func() { a.syncDone(d, saveSeq, res, err) })
	}()
}

func (a *App) syncDone(d *diary.Diary, saveSeq int, res syncclient.Result, err error) {
	a.syncing = false
	defer func() {
		if a.syncAgain && a.diary != nil && !a.syncPaused {
			a.syncAgain = false
			a.startSync()
		}
	}()
	if a.diary != d {
		return // the diary was closed (or replaced) meanwhile
	}

	var km *syncclient.KeyMismatchError
	switch {
	case errors.As(err, &km):
		a.syncPaused = true
		a.updateSyncStatus(syncError, "the passphrase was changed on another computer")
		a.askRemotePassphrase(km)
		return
	case errors.Is(err, syncclient.ErrUnauthorized):
		a.updateSyncStatus(syncError, "the device token was refused")
		return
	case err != nil:
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) || strings.Contains(err.Error(), "dial") {
			a.updateSyncStatus(syncOffline, err.Error())
		} else {
			a.updateSyncStatus(syncError, err.Error())
		}
		return
	}

	if res.Changed {
		hadEdits := a.dirty
		a.applyMerged(res.Diary)
		if hadEdits {
			a.markDirty()
			// Unsaved local edits: the merged content will be saved (and
			// synced again) with them. Don't record the version, so the next
			// sync pulls again whatever is not in the file yet.
			a.updateSyncStatus(syncOK, "")
			return
		}
		if !a.saveLocal() {
			return
		}
	}
	id := d.ID
	userSaved := a.saveSeq != saveSeq
	updateState(func(s *state) {
		s.Sync[id] = diarySyncState{Version: res.Version, Pending: userSaved}
	})
	a.updateSyncStatus(syncOK, "")
}

// applyMerged merges a synced copy into the live diary and refreshes the UI.
func (a *App) applyMerged(remote *diary.Diary) {
	a.commit()
	merged, changed := diary.Merge(a.diary, remote)
	if !changed {
		return
	}
	a.diary = merged
	a.editor.ReplaceDiary(merged)

	var cur *diary.Entry
	if a.current != nil {
		cur = merged.Entry(a.current.ID)
	}
	reload := cur != a.current
	a.current = cur
	a.rebuildList()
	switch {
	case cur == nil:
		if row := a.list.RowAtIndex(0); row != nil {
			a.list.SelectRow(row)
		} else {
			a.showEntry(nil)
		}
	case reload:
		a.current = nil // showEntry must not commit the stale editor content
		a.showEntry(cur)
	}
}

// askRemotePassphrase asks for the passphrase set on another computer and
// adopts it, re-encrypting the local file.
func (a *App) askRemotePassphrase(km *syncclient.KeyMismatchError) {
	d := a.diary
	var newKey *vault.Key
	askPasswordWith(&a.win.Window, "Passphrase changed",
		"The passphrase of this diary was changed on another computer. Enter the new passphrase to keep syncing.",
		func(pw []byte) string {
			pt, k, err := vault.Open(km.Data, pw)
			if err != nil {
				return "Wrong passphrase."
			}
			wipe(pt)
			newKey = k
			return ""
		},
		func() {
			if a.diary != d {
				newKey.Wipe()
				return
			}
			old := a.key
			a.key = newKey
			old.Wipe()
			a.syncPaused = false
			a.save() // re-encrypt locally with the new passphrase, then sync
		})
}

// ---- settings --------------------------------------------------------------

func (a *App) syncSettings() {
	d := newDialog(&a.win.Window, "Sync settings",
		"Diaries are uploaded already encrypted: the server never sees your passphrase or content.")

	url := gtk.NewEntry()
	url.SetPlaceholderText("https://diary.example.lan")
	url.SetText(a.syncCfg.ServerURL)
	d.field("Server _address", url)

	tok := passwordField()
	tok.SetText(a.syncCfg.Token)
	d.field("Device _token (from “mydiary-server token add”)", tok)

	ca := gtk.NewEntry()
	ca.SetPlaceholderText("Optional: /path/to/ca.pem")
	ca.SetText(a.syncCfg.CAFile)
	d.field("Certificate _authority file", ca)

	apply := func(cfg syncclient.Config) {
		if err := saveSyncConfig(cfg); err != nil {
			d.setError(err.Error())
			return
		}
		a.syncCfg = cfg
		d.win.Close()
		a.syncPaused = false
		a.startSync()
	}

	if a.syncCfg.Enabled() {
		d.addButton("_Disable sync", "destructive-action", func() { apply(syncclient.Config{}) })
	}
	d.addButton("_Cancel", "", d.win.Close)
	var ok *gtk.Button
	submit := func() {
		cfg := syncclient.Config{
			ServerURL: strings.TrimSpace(url.Text()),
			Token:     strings.TrimSpace(tok.Text()),
			CAFile:    strings.TrimSpace(ca.Text()),
		}
		client, err := syncclient.New(cfg)
		if err != nil {
			d.setError(err.Error())
			return
		}
		d.setBusy(true)
		ok.SetLabel("Connecting…")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := client.Ping(ctx)
			cancel()
			coreglib.IdleAdd(func() {
				d.setBusy(false)
				ok.SetLabel("_Connect")
				if err != nil {
					d.setError("Cannot connect: " + err.Error())
					return
				}
				apply(cfg)
			})
		}()
	}
	ok = d.addButton("_Connect", "suggested-action", submit)
	url.ConnectActivate(func() { tok.GrabFocus() })
	tok.ConnectActivate(submit)
	ca.ConnectActivate(submit)
	d.win.SetDefaultWidget(ok)
	d.win.Present()
	url.GrabFocus()
}

// ---- download --------------------------------------------------------------

// downloadDiary lets the user pick a diary on the server and store it
// locally, e.g. when setting up a new computer.
func (a *App) downloadDiary() {
	if !a.syncCfg.Enabled() {
		a.syncSettings()
		return
	}
	client, err := syncclient.New(a.syncCfg)
	if err != nil {
		showError(&a.win.Window, "Sync is misconfigured", err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		list, err := client.List(ctx)
		cancel()
		coreglib.IdleAdd(func() {
			if err != nil {
				showError(&a.win.Window, "Cannot reach the sync server", err)
				return
			}
			a.chooseRemoteDiary(client, list)
		})
	}()
}

func (a *App) chooseRemoteDiary(client *syncclient.Client, list []syncproto.DiaryInfo) {
	d := newDialog(&a.win.Window, "Get a diary from the server", "")
	if len(list) == 0 {
		d.setError("There is no diary on the server yet.")
	}
	lb := gtk.NewListBox()
	lb.AddCSSClass("boxed-list")
	lb.SetSelectionMode(gtk.SelectionBrowse)
	for _, info := range list {
		row := gtk.NewListBoxRow()
		box := gtk.NewBox(gtk.OrientationVertical, 2)
		box.SetMarginTop(8)
		box.SetMarginBottom(8)
		box.SetMarginStart(12)
		box.SetMarginEnd(12)
		name := info.Label
		if name == "" {
			name = info.ID
		}
		t := gtk.NewLabel(name)
		t.SetXAlign(0)
		t.SetEllipsize(pango.EllipsizeEnd)
		t.AddCSSClass("heading")
		sub := gtk.NewLabel(fmt.Sprintf("Updated %s · %.1f MB",
			info.Updated.Local().Format("2 Jan 2006 15:04"), float64(info.Size)/1e6))
		sub.SetXAlign(0)
		sub.AddCSSClass("dim-label")
		box.Append(t)
		box.Append(sub)
		row.SetChild(box)
		lb.Append(row)
	}
	scroll := gtk.NewScrolledWindow()
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scroll.SetMinContentHeight(min(80*len(list), 320))
	scroll.SetChild(lb)
	d.content.Append(scroll)

	d.addButton("_Cancel", "", d.win.Close)
	ok := d.addButton("_Download…", "suggested-action", func() {
		row := lb.SelectedRow()
		if row == nil {
			return
		}
		info := list[row.Index()]
		d.win.Close()
		a.fetchRemoteDiary(client, info)
	})
	ok.SetSensitive(len(list) > 0)
	lb.ConnectRowActivated(func(*gtk.ListBoxRow) { ok.Activate() })
	d.win.SetDefaultWidget(ok)
	d.win.Present()
	if row := lb.RowAtIndex(0); row != nil {
		lb.SelectRow(row)
		row.GrabFocus()
	}
}

func (a *App) fetchRemoteDiary(client *syncclient.Client, info syncproto.DiaryInfo) {
	chooseDiaryToCreateNamed(&a.win.Window, info.Label, func(path string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
			data, version, _, err := client.Get(ctx, info.ID, 0)
			cancel()
			coreglib.IdleAdd(func() {
				if err != nil {
					showError(&a.win.Window, "Download failed", err)
					return
				}
				a.unlockDownloaded(path, info.ID, data, version)
			})
		}()
	})
}

func (a *App) unlockDownloaded(path, id string, data []byte, version int64) {
	var (
		d *diary.Diary
		k *vault.Key
	)
	askPassword(&a.win.Window, path, func(pw []byte) string {
		pt, key, err := vault.Open(data, pw)
		if err != nil {
			return "Wrong passphrase."
		}
		defer wipe(pt)
		dd, err := diary.Unmarshal(pt)
		if err != nil {
			key.Wipe()
			return err.Error()
		}
		if dd.ID != id {
			key.Wipe()
			return "The server copy does not match the requested diary."
		}
		// Store the file exactly as downloaded: it is already encrypted.
		if err := vault.WriteFileAtomic(path, data); err != nil {
			key.Wipe()
			return err.Error()
		}
		d, k = dd, key
		return ""
	}, func() {
		updateState(func(s *state) { s.Sync[id] = diarySyncState{Version: version} })
		a.setDiary(path, d, k)
	})
}
