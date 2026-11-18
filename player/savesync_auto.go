package main

// savesync_auto.go — saves follow the player with no button pressed.
//
// Before a library game starts, its saves come down from Google Drive (and
// from RomM, when that is set up: rommAutoSyncGame in romm_autosync.go),
// within six seconds all told; offline, signed out or switched off, nothing
// waits at all. When the game has ended, its saves go up, and half a minute
// later — unless another game started — everything else is synced in the
// background. Nothing ever syncs while a game holds the screen.
//
// launchLibraryGame runs on the render thread, so the pull cannot simply
// block it: saveSyncDeferLaunch takes the launch over, runs the pull on a
// goroutine and starts the game from the main loop when it is done
// (docs/CLOUD_SYNC.md → Automatic sync).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// saveSyncLaunchBudget is the longest a game waits for its saves.
	saveSyncLaunchBudget = 6 * time.Second
	// saveSyncExitBudget bounds sending one game's saves after it ended.
	saveSyncExitBudget = 2 * time.Minute
	// saveSyncFullDelay is the quiet time after a game before the background
	// sync of everything; another exit restarts it.
	saveSyncFullDelay = 30 * time.Second
	// saveSyncFullBudget bounds the background sync (a first one may send a
	// whole card of save states).
	saveSyncFullBudget = 30 * time.Minute
	// saveSyncWarnEvery spaces out "could not sync" toasts.
	saveSyncWarnEvery = 10 * time.Minute
)

var saveSyncAuto struct {
	mu         sync.Mutex
	fullTimer  *time.Timer
	fullCancel context.CancelFunc
	lastWarn   time.Time
	// fullGen changes on every schedule and cancel: a timer that already
	// fired when a game started finds it changed and does not start.
	fullGen uint64
	// pending and resume belong to the main thread: a launch waiting for its
	// saves, and the launch that already has them.
	pending bool
	resume  string
}

// saveSyncRommConfigured is a cheap "could RomM have work" check (a server
// set and its auto-sync on); the RomM hook decides the rest.
func saveSyncRommConfigured() bool {
	return rommServerSettingsGet().autoSyncOn()
}

// saveSyncDeferLaunch is called by launchLibraryGame (render thread) once the
// game is known to be startable. deferred is true when the launch was taken
// over: the saves are fetched in the background and launchLibraryGame runs
// again from the main loop afterwards.
func saveSyncDeferLaunch(game libraryGame, launcher string) (deferred bool, err error) {
	if saveSyncAuto.resume == game.Path {
		saveSyncAuto.resume = ""
		return false, nil
	}
	drive := gdriveAutoReady() && IsOnline()
	if !drive && !saveSyncRommConfigured() {
		return false, nil
	}
	if saveSyncAuto.pending {
		return false, errors.New("still getting the saves for the last game — one moment")
	}
	saveSyncAuto.pending = true
	// After the caller's own "Starting…" toast, which would replace it.
	if drive {
		tbxPost(func() {
			if saveSyncAuto.pending {
				ShowToast("Getting your latest saves…", ToastKindInfo)
			}
		})
	}
	go func() {
		saveSyncBeforeLaunch(game.Path, game.System)
		tbxPost(func() {
			saveSyncAuto.pending = false
			if gameScreenSuspended() {
				// Something else took the screen meanwhile.
				return
			}
			saveSyncAuto.resume = game.Path
			err := launchLibraryGame(game, launcher)
			saveSyncAuto.resume = ""
			if err != nil {
				ShowToast("Cannot launch "+game.Title+": "+err.Error(), ToastKindError)
			}
		})
	}()
	return true, nil
}

// saveSyncBeforeLaunch brings one game's saves to this device before it
// starts: Google Drive first, then RomM, within saveSyncLaunchBudget. Called
// off the render thread, before the emulator starts.
func saveSyncBeforeLaunch(romPath, system string) {
	// A background sync must not be writing files under the game.
	saveSyncCancelFull()
	ctx, cancel := context.WithTimeout(context.Background(), saveSyncLaunchBudget)
	defer cancel()
	var notes []string
	kind := ToastKindSuccess
	if gdriveAutoReady() && IsOnline() {
		res, err := gdriveSyncGame(ctx, romPath, true)
		switch {
		case err != nil:
			if saveSyncWarnDue(err) {
				notes = append(notes, "Saves not checked on Google Drive ("+gdriveErrorText(err)+") — the saves here are used")
				kind = ToastKindWarning
			}
			logInfo("gdrive", "before %s: %v", ssyncGameStem(romPath), err)
		default:
			if line := saveSyncResultLine(res, true); line != "" {
				notes = append(notes, line)
				if res.Failed > 0 || res.Conflicts > 0 {
					kind = ToastKindWarning
				}
				gdriveNoteResult(line)
			}
		}
	}
	if ctx.Err() == nil {
		note, err := rommAutoSyncGame(ctx, romPath, system, true)
		if err != nil {
			logInfo("romm", "auto-sync before %s: %v", ssyncGameStem(romPath), err)
		} else if note != "" {
			notes = append(notes, note)
		}
	}
	saveSyncToast(notes, kind)
}

// saveSyncAfterExit sends one game's saves up once its process ended, then
// schedules the background sync of everything. It returns at once.
func saveSyncAfterExit(romPath, system string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), saveSyncExitBudget)
		defer cancel()
		var notes []string
		kind := ToastKindSuccess
		drive := gdriveAutoReady()
		if drive && IsOnline() {
			res, err := gdriveSyncGame(ctx, romPath, false)
			switch {
			case err != nil:
				if saveSyncWarnDue(err) {
					notes = append(notes, "Saves not sent to Google Drive ("+gdriveErrorText(err)+") — they go up next time")
					kind = ToastKindWarning
				}
				logInfo("gdrive", "after %s: %v", ssyncGameStem(romPath), err)
			default:
				if line := saveSyncResultLine(res, false); line != "" {
					notes = append(notes, line)
					if res.Failed > 0 || res.Conflicts > 0 {
						kind = ToastKindWarning
					}
					gdriveNoteResult(line)
				}
			}
		}
		note, err := rommAutoSyncGame(ctx, romPath, system, false)
		if err != nil {
			logInfo("romm", "auto-sync after %s: %v", ssyncGameStem(romPath), err)
		} else if note != "" {
			notes = append(notes, note)
		}
		saveSyncToast(notes, kind)
		if drive {
			saveSyncScheduleFull()
		}
	}()
}

// saveSyncResultLine says what a game's sync did, or "" when it did nothing
// worth a toast.
func saveSyncResultLine(res ssyncResult, pull bool) string {
	var parts []string
	switch {
	case pull && res.Pulled > 0:
		parts = append(parts, "Saves updated from Google Drive")
	case !pull && res.Pushed > 0:
		parts = append(parts, "Saves backed up to Google Drive")
	}
	if res.Conflicts > 0 {
		parts = append(parts, pluralizeCount(res.Conflicts, "save changed here and on Drive", "saves changed here and on Drive")+
			" — both kept, Drive's as \"(from Google Drive)\"")
	}
	if res.Failed > 0 {
		msg := pluralizeCount(res.Failed, "save", "saves") + " not copied"
		if len(res.Errors) > 0 {
			msg += ": " + res.Errors[0]
		}
		parts = append(parts, msg)
	}
	return strings.Join(parts, " · ")
}

// saveSyncWarnDue reports whether a failure should be shown: a sign-in that
// expired always, anything else at most once per saveSyncWarnEvery.
func saveSyncWarnDue(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, errGdriveExpired) {
		return true
	}
	saveSyncAuto.mu.Lock()
	defer saveSyncAuto.mu.Unlock()
	if !saveSyncAuto.lastWarn.IsZero() && time.Since(saveSyncAuto.lastWarn) < saveSyncWarnEvery {
		return false
	}
	saveSyncAuto.lastWarn = time.Now()
	return true
}

// saveSyncToast shows the notes from the main loop (nothing for none).
func saveSyncToast(notes []string, kind ToastKind) {
	if len(notes) == 0 {
		return
	}
	msg := strings.Join(notes, " · ")
	tbxPost(func() { ShowToast(msg, kind) })
}

// ── Background sync of everything ───────────────────────────────────────────

// saveSyncScheduleFull (re)starts the quiet-time timer for the background
// sync.
func saveSyncScheduleFull() {
	saveSyncAuto.mu.Lock()
	defer saveSyncAuto.mu.Unlock()
	if saveSyncAuto.fullTimer != nil {
		saveSyncAuto.fullTimer.Stop()
	}
	saveSyncAuto.fullGen++
	gen := saveSyncAuto.fullGen
	saveSyncAuto.fullTimer = time.AfterFunc(saveSyncFullDelay, func() { saveSyncRunFull(gen) })
}

// saveSyncCancelFull stops a waiting or running background sync (a game is
// about to start).
func saveSyncCancelFull() {
	saveSyncAuto.mu.Lock()
	defer saveSyncAuto.mu.Unlock()
	if saveSyncAuto.fullTimer != nil {
		saveSyncAuto.fullTimer.Stop()
		saveSyncAuto.fullTimer = nil
	}
	saveSyncAuto.fullGen++
	if saveSyncAuto.fullCancel != nil {
		saveSyncAuto.fullCancel()
	}
}

// saveSyncRunFull is the background sync of every save folder with Drive.
// gen is the schedule that started it (a cancel since then wins).
func saveSyncRunFull(gen uint64) {
	if gameScreenSuspended() || !gdriveAutoReady() || !IsOnline() {
		// The next game's exit schedules it again.
		return
	}
	if !gdriveSyncMu.TryLock() {
		// The Save sync page is syncing: that covers it.
		return
	}
	gdriveSyncMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), saveSyncFullBudget)
	saveSyncAuto.mu.Lock()
	if saveSyncAuto.fullGen != gen {
		// A game started (or another exit rescheduled) after the timer fired.
		saveSyncAuto.mu.Unlock()
		cancel()
		return
	}
	saveSyncAuto.fullTimer = nil
	saveSyncAuto.fullCancel = cancel
	saveSyncAuto.mu.Unlock()
	defer func() {
		saveSyncAuto.mu.Lock()
		if saveSyncAuto.fullGen == gen {
			saveSyncAuto.fullCancel = nil
		}
		saveSyncAuto.mu.Unlock()
		cancel()
	}()
	// Any game taking the screen stops it, also one started from Ports, the
	// firmware menu or netplay, which never call saveSyncCancelFull.
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if gameScreenSuspended() {
					cancel()
					return
				}
			}
		}
	}()
	res, err := gdriveSyncAll(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logInfo("gdrive", "background sync: %v", err)
			if saveSyncWarnDue(err) {
				saveSyncToast([]string{"Saves not synced with Google Drive: " + gdriveErrorText(err)}, ToastKindWarning)
			}
		}
		return
	}
	logInfo("gdrive", "background sync: %d down, %d up, %d both kept, %d failed", res.Pulled, res.Pushed, res.Conflicts, res.Failed)
	if res.Pulled+res.Pushed+res.Conflicts+res.Failed == 0 {
		return
	}
	gdriveNoteResult(ssyncResultText(res))
	switch {
	case res.Failed > 0 || res.Conflicts > 0:
		saveSyncToast([]string{"Google Drive sync: " + ssyncResultText(res)}, ToastKindWarning)
	case res.Pulled > 0:
		saveSyncToast([]string{fmt.Sprintf("%s updated from Google Drive", pluralizeCount(res.Pulled, "save", "saves"))}, ToastKindSuccess)
	}
}
