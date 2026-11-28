package main

// hakux_auto.go — hakuX sets itself up on the Smart Pro, and an Xbox game
// started before it is there installs it and then starts.
//
// Juka asked on 2026-11-19 for hakuX to install by itself and to be the Xbox
// games' launcher in Games. Until then it came only from a press of X on the
// Emulators page's Xbox tile, and the daily Emulator updates pass only brought
// forward a copy JukaHub had installed. Now:
//
//   - The pass's hakuX updater (emuautoupdate_hakux.go) also installs it once,
//     when no Xbox launcher is on the card: on the Smart Pro the port is built
//     for, with Emulator updates on Install automatically (the default), at the
//     pass's quiet moment (never over a game or another install; on the charger
//     or after the user stepped away) and online. It is the install X runs
//     (hakuxInstall: the published digests, the file-by-file merge, the
//     provenance file, Roms/XBOX made), under the pass's install card and the
//     tile's "Installing…" pill, after a free-space check that keeps the boot
//     card's low-space floor free on top of what the download and its unpacking
//     take. The pass's toast says it is there and where the games go. Notify
//     me does not announce it (a download of something new is not an update of
//     anything, the rule "Get more cores" follows), and Off runs nothing. The
//     Brick Pro, offered the port as untested, keeps it a press.
//   - A on an Xbox game in Games while no Xbox launcher is on the card starts
//     the same install, on any device the port is offered on, and starts the
//     game when it is in — if the user is still where they pressed and nothing
//     else took the screen; otherwise the toast says the game is ready.
//
// "Once" is kept on the card, in hakuX's own state folder (data/hakux, beside
// the kept copy): auto-install.json records that an Xbox launcher was there —
// JukaHub installed hakuX, or the pass saw a launcher folder, JukaHub's or one
// made by hand. From then on the pass never installs it again, so a hakuX the
// user deleted stays deleted, and a hand-made Emus/XBOX is never replaced (the
// pass also never installs while any Xbox folder is on the card). X on the
// tile and A on a game still install it on request — a press, never a start
// nobody pressed for (the resume at power-on only names what is missing).
//
// A first install by the pass that fails once its download has begun (the
// download, its digests, the merge) is noted in the same file, without the
// "seen" that makes it final, and the pass leaves hakuX alone for a day: the
// pass's own wait after a failure lasts only the session and starts at an
// hour, which would fetch a hundred-odd megabytes again and again — overnight
// on the charger, and after every start-up — for an install nobody asked for.

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// hakuxAutoMarkerName is the once-per-card record, in hakuX's state folder.
	hakuxAutoMarkerName = "auto-install.json"
	// hakuxPlayAfterWithin is how long after A the game still starts by itself
	// once hakuX is in; a slower install only says the game is ready.
	hakuxPlayAfterWithin = 10 * time.Minute
	// hakuxPlayAfterLate is how late the main loop may pick up the finished
	// install and still start the game: later means it was parked under a
	// game or another program that took the screen meanwhile.
	hakuxPlayAfterLate = 10 * time.Second
)

// hakuxAutoMarker is auto-install.json: what showed that an Xbox launcher was
// on the card, and when — or, with no Seen, only when the pass's last first
// install failed after its download began, and why (hakuxAutoNoteFailure).
type hakuxAutoMarker struct {
	Seen     string `json:"seen,omitempty"`
	At       string `json:"at,omitempty"`
	FailedAt string `json:"failedAt,omitempty"`
	Failure  string `json:"failure,omitempty"`
}

// hakuxAutoMarkerPath is the record's file on the card p describes.
func hakuxAutoMarkerPath(p hakuxPaths) string {
	return filepath.Join(p.SDRoot, hakuxOS(p.State), hakuxAutoMarkerName)
}

// hakuxAutoReadMarker reads the record; ok reports a file there at all. One
// that cannot be read or parsed reads as a sighting: before failures were
// noted in it, the file being there was the whole record.
func hakuxAutoReadMarker(p hakuxPaths) (m hakuxAutoMarker, ok bool) {
	data, err := os.ReadFile(hakuxAutoMarkerPath(p))
	if errors.Is(err, fs.ErrNotExist) {
		return hakuxAutoMarker{}, false
	}
	if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &m) != nil {
		return hakuxAutoMarker{Seen: "unreadable record"}, true
	}
	return m, true
}

// hakuxAutoMarked reports that the card has had an Xbox launcher before: the
// record is there and is not only a failed first install's note.
func hakuxAutoMarked(p hakuxPaths) bool {
	m, ok := hakuxAutoReadMarker(p)
	return ok && (m.Seen != "" || m.FailedAt == "")
}

// hakuxAutoHeld reports a first install by the pass that failed after its
// download began less than a day ago (hakuxAutoNoteFailure), so the pass waits.
// A clock moved back by more than a day no longer holds it; a clock not set yet
// (before 2020, the handheld without its RTC) holds it until it is.
func hakuxAutoHeld(p hakuxPaths, now time.Time) bool {
	m, ok := hakuxAutoReadMarker(p)
	if !ok || m.Seen != "" {
		return false
	}
	failed, ok := parseReleaseTimestamp(m.FailedAt)
	if !ok || failed.Before(minSaneClock) {
		return false
	}
	if now.Before(minSaneClock) {
		return true
	}
	since := now.Sub(failed)
	if since < 0 {
		since = -since
	}
	return since < coreAutoUpdateInterval
}

// hakuxAutoNoteFailure records the pass's first install that failed once its
// download had begun, so the next try is a day away in this session and after
// a restart (hakuxAutoHeld). Written like the record, with no Seen, so it
// never stops a later install by itself.
func hakuxAutoNoteFailure(p hakuxPaths, cause error) {
	if strings.TrimSpace(p.SDRoot) == "" || cause == nil || hakuxAutoMarked(p) {
		return
	}
	data, err := json.MarshalIndent(hakuxAutoMarker{FailedAt: time.Now().UTC().Format(time.RFC3339), Failure: cause.Error()}, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(hakuxAutoMarkerPath(p), append(data, '\n'), 0o644); err != nil {
		logWarn("emulators", "hakuX: could not note the failed install: %v", err)
		return
	}
	logInfo("emulators", "hakuX: the install by itself failed after its download began; the daily pass tries again in a day (X on the Xbox tile or A on an Xbox game installs it now)")
}

// hakuxAutoRemember writes the record unless it is there already. seen says
// what showed it ("hakuX v0.1.3 installed", "Xbox launcher folder …"). A
// failed first install's note is replaced: it has done its job.
func hakuxAutoRemember(p hakuxPaths, seen string) {
	if strings.TrimSpace(p.SDRoot) == "" || hakuxAutoMarked(p) {
		return
	}
	data, err := json.MarshalIndent(hakuxAutoMarker{Seen: seen, At: time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return
	}
	// Synced and renamed into place: a power cut right after an install must
	// not lose the record on a FAT card, or a later deletion would be undone.
	if err := AtomicWrite(hakuxAutoMarkerPath(p), append(data, '\n'), 0o644); err != nil {
		logWarn("emulators", "hakuX: could not record that an Xbox launcher is on the card: %v", err)
		return
	}
	logInfo("emulators", "hakuX: %s — recorded; the daily pass will not install hakuX by itself on this card again", seen)
}

// hakuxAutoHadBefore reports traces of a hakuX that a JukaHub from before the
// record had on this card and that is gone now: the copy an update kept for
// R1, or a build the daily pass installed or a rollback gave up. Without this,
// a hakuX deleted before the record existed would come back by itself. A few
// small reads (the pass's record file is read once per session).
func hakuxAutoHadBefore(p hakuxPaths) bool {
	if _, kept := hakuxReadBackupInfo(p); kept {
		return true
	}
	return strings.TrimSpace(emuRuntimeRecordFor("hakux").Installed) != ""
}

// hakuxAutoDevice reports a device the pass installs hakuX on by itself: the
// Smart Pro the port is built for. The Brick Pro is offered it as untested and
// keeps it a press; every other model is not offered it at all.
func hakuxAutoDevice() bool {
	if IsWindows() {
		return false
	}
	support := hakuxSupportFor(hakuxDevice())
	return support.Offer && support.Untested == ""
}

// hakuxAutoWanted reports whether the pass should install hakuX now, without
// the network: the Smart Pro, a card that never had an Xbox launcher (an Xbox
// folder found now is recorded, so a later removal is respected), and Install
// automatically. A few file reads; asked only while the updater is due.
func hakuxAutoWanted() bool {
	if !hakuxAutoDevice() {
		return false
	}
	p := hakuxDevicePaths()
	if hakuxAutoMarked(p) {
		return false
	}
	if folder, m, managed := hakuxInstalled(deviceEmuRoots); folder != "" {
		seen := "Xbox launcher folder " + folder + " found"
		if managed {
			seen = "hakuX " + m.Tag + " found in " + folder
		}
		hakuxAutoRemember(p, seen)
		return false
	}
	if hakuxAutoHadBefore(p) {
		hakuxAutoRemember(p, "traces of an earlier hakuX found (its kept copy or the daily pass's record)")
		return false
	}
	if hakuxAutoHeld(p, time.Now()) {
		// Its last try failed after the download began: a day's wait.
		return false
	}
	return emuCoreUpdateModeNow() == coreUpdateInstall
}

// hakuxAutoPending is hakuxAutoWanted for the tile and the setup sheet: no
// launcher folder (the caller knows), so only the device, the record, an
// earlier hakuX's traces and the setting are asked, and nothing is written.
// A day's wait after a failed try still counts as pending: it comes.
func hakuxAutoPending() bool {
	if !hakuxAutoDevice() || emuCoreUpdateModeNow() != coreUpdateInstall {
		return false
	}
	p := hakuxDevicePaths()
	return !hakuxAutoMarked(p) && !hakuxAutoHadBefore(p)
}

// hakuxAutoFreeNeeded is the free space the pass wants before it installs
// hakuX by itself: the download and its unpacked tree (hakuxInstall checks
// those two again) plus the floor the boot card warns below, so a card is
// never filled by an install nobody asked for. An unknown size counts as half
// the download cap.
func hakuxAutoFreeNeeded(size int64) int64 {
	if size <= 0 {
		size = hakuxMaxBytes / 2
	}
	return 2*size + bootLowSpaceBytes
}

// hakuxAutoFirstCheck is the pass's check while hakuX is not on the card:
// the newest package, and whether the card has room for it. A card that is too
// full is a quiet "nothing to do" (logged), asked again at tomorrow's pass.
func hakuxAutoFirstCheck(ctx context.Context, client *http.Client) (bool, string, error) {
	rel, err := hakuxFetchRelease(ctx, client)
	if err != nil {
		return false, "", err
	}
	need := hakuxAutoFreeNeeded(rel.Release.Size)
	if free, err := freeBytes(hakuxDevicePaths().SDRoot); err == nil && free < need {
		logInfo("emulators", "hakuX %s not installed by itself: the card has %s free and the install keeps %s", rel.Release.Tag, formatInstallBytes(free), formatInstallBytes(need))
		return false, "", nil
	}
	return true, emuReleaseLabel("", rel.Release), nil
}

// hakuxAutoReadyNote is the line the pass's toast carries after it installed
// hakuX by itself: what it is for, and where the games go or how many wait.
func hakuxAutoReadyNote() string {
	dir := hakuxRomsDir(deviceRomsRoot)
	if n := hakuxCountGames(dir); n > 0 {
		return "hakuX now plays your " + pluralizeCount(n, "Xbox game", "Xbox games") + " in Games"
	}
	return "hakuX now plays Xbox games: put .iso (XISO) files in Roms/" + filepath.Base(dir)
}

// ── A on an Xbox game ──────────────────────────────────────────────────────

// hakuxUnasked is set while a game starts that nobody pressed for — the resume
// at power-on (quickresume.go) — so hakuxInstallForGame names a missing hakuX
// there instead of downloading it: only a press installs it from a game, and a
// hakuX the user deleted stays deleted. Main thread.
var hakuxUnasked bool

// hakuxStartUnasked runs start, a game start nobody pressed for, with
// hakuxUnasked set. Main thread.
func hakuxStartUnasked(start func()) {
	hakuxUnasked = true
	defer func() { hakuxUnasked = false }()
	start()
}

// hakuxGameTitle is how a toast names the game.
func hakuxGameTitle(game libraryGame) string {
	if t := strings.TrimSpace(game.DisplayTitle); t != "" {
		return t
	}
	if t := strings.TrimSpace(game.Title); t != "" {
		return t
	}
	return filepath.Base(game.Path)
}

// hakuxInstallForGame answers a start of an Xbox game that found no launcher
// (chooseGameLauncher): on a device the port is offered on with no Xbox folder
// on the card, it installs hakuX and starts the game when it is in; on one it
// is not offered on, it says why; on a start nobody pressed for (hakuxUnasked)
// it only says hakuX is missing. It reports whether it answered — false
// leaves the caller's own "no emulator" message (Windows, where xemu plays
// Xbox games, and a launcher folder the catalog already has). Main thread.
func hakuxInstallForGame(game libraryGame, launch func(launcher string)) bool {
	if !strings.EqualFold(strings.TrimSpace(game.System), emuXboxEntryName) || IsWindows() {
		return false
	}
	device := hakuxDevice()
	support := hakuxSupportFor(device)
	if !support.Offer {
		PlayNavSound()
		ShowToast(xboxLibraryLauncherHintFor(device), ToastKindInfo)
		return true
	}
	title := hakuxGameTitle(game)
	// Asked before the folder: an install that is merging has a half-made
	// Emus/XBOX on the card, which is neither broken nor ready.
	if emuInstallingName() == emuXboxEntryName {
		ShowToast(hintSentence("hakuX is being installed · press %s on ", "A", "Play")+title+" again once it is in", ToastKindInfo)
		return true
	}
	if folder, _, _ := hakuxInstalled(deviceEmuRoots); folder != "" {
		if hakuxLauncherIn(folder) != "" {
			return false
		}
		ShowToast(folder+" has no launch.sh — Emulators → Xbox → X installs hakuX again", ToastKindWarning)
		return true
	}
	if hakuxUnasked {
		// Nobody pressed anything (a resume at power-on): a download of a
		// hundred-odd megabytes waits for a press.
		logInfo("emulators", "hakuX is not on the card: %s not started by itself (a press installs it)", filepath.Base(game.Path))
		ShowToast(xboxLibraryLauncherHintFor(device), ToastKindWarning)
		return true
	}
	if !IsOnline() {
		ShowToast(hintSentence("Xbox games need hakuX · connect to the internet, then press %s on ", "A", "Install")+title+" to install it", ToastKindWarning)
		return true
	}
	begin := "Installing hakuX for Xbox games · " + title + " starts when it is in"
	if support.Untested != "" {
		begin += " · untested on this model"
	}
	fromLibrary := gameLib.open
	pressed := time.Now()
	hakuxStartInstallRun(hakuxInstallRun{
		Begin: begin,
		Busy:  hintSentence("Another install is running · press %s on ", "A", "Play") + title + " again when it finishes",
		After: func(result hakuxInstallResult, err error) {
			if err != nil {
				return // hakuxFinish said why
			}
			finished := time.Now()
			tbxPost(func() { hakuxPlayAfterInstall(game, launch, fromLibrary, pressed, finished, result) })
		},
	})
	return true
}

// hakuxPlayAfterInstall starts the game A was pressed on once hakuX is in —
// when nothing else took the screen since: no game or video is playing, no
// emulator question is up, the Games Library is as open (or not) as it was,
// and the install did not take so long that the user moved on. Otherwise the
// toast says the game is ready. finished is when the install ended: the main
// loop runs posted work within a frame or two, unless a game (or another
// program) held the screen then — the loop is parked under it — and a game
// must not start by itself the moment that one is quit. Main thread.
func hakuxPlayAfterInstall(game libraryGame, launch func(launcher string), fromLibrary bool, pressed, finished time.Time, result hakuxInstallResult) {
	title := hakuxGameTitle(game)
	still := !gameScreenSuspended() && !videoHoldsScreenAwake() && !emuPickOpen() &&
		gameLib.open == fromLibrary && time.Since(pressed) < hakuxPlayAfterWithin &&
		time.Since(finished) < hakuxPlayAfterLate
	if !still || !regularFile(game.Path) {
		ShowToast("hakuX "+result.Tag+" is in · "+hintSentence("press %s on ", "A", "Play")+title+" in Games to play", ToastKindSuccess)
		return
	}
	logInfo("emulators", "hakuX %s installed from a game's start; starting %s", result.Tag, filepath.Base(game.Path))
	chooseGameLauncher(game, launch)
}
