package main

// discpage.go — games on more than one disc: which disc a game starts on (the
// Disc row of its options sheet) and changing discs while it plays (the Disc
// row of the in-game menu).
//
// A multi-disc game in the library is an .m3u playlist (disctools.go writes
// one for loose discs), and RetroArch already remembers per game which disc
// was in: its disk control interface keeps a small JSON record named after
// the content, "<content name>.ldci", in the folder it writes the game's
// battery saves to (RetroArch's disk_index_file.c, FILE_PATH_DISK_CONTROL_
// INDEX_EXTENSION in file_path_special.h):
//
//	{
//	  "image_index": 1,
//	  "image_path": "/mnt/SDCARD/Roms/PS/Game (Disc 2).cue"
//	}
//
// image_index counts from 0. Before the content loads, RetroArch reads the
// record and hands a non-zero index and its path to cores that take an
// initial disc (the libretro set_initial_image call: PCSX-ReARMed, Beetle
// PSX and others); after loading it checks that the core reports the same
// path for that disc, and on a mismatch starts disc 1 and resets the record.
// It rewrites the record itself whenever the disc changes, so the next start
// is on the last disc played. The Disc row writes the same record, which is
// the documented way of choosing the first disc, not a patch to anything.
//
// The path must be the one the core reports: PCSX-ReARMed joins the
// playlist's folder, exactly as RetroArch was given the playlist's path, with
// the line of the .m3u. A firmware launch script is given the playlist as
// Emus/<system>/../../Roms/… (gamelaunch_firmware.go), so the path is worked
// out from the argument the launch really passes, and the launch
// (prepareDiscStart, game_library.go) puts it right again when the launcher
// changed since the row was set. The record's folder is where RetroArch keeps
// the game's saves: savefile_directory (else the playlist's own folder), with
// the content folder and the core's name added when RetroArch sorts saves so;
// a record RetroArch already wrote elsewhere is updated too.
//
// In the game, the menu's Disc row says "Disc 1 of 3"; Left/Right pick
// another, A puts it in: the game is given back (a stopped RetroArch reads no
// commands) and RetroArch's network commands do what its own hotkeys do —
// DISK_EJECT_TOGGLE, then DISK_NEXT or DISK_PREV once per disc, then
// DISK_EJECT_TOGGLE — spaced so each lands in its own frame (RetroArch
// latches a hotkey command once per frame). The disc in the tray is tracked
// here, from the disc the game started on. Sources: RetroArch's command list
// (command.h: DISK_EJECT_TOGGLE, DISK_NEXT, DISK_PREV) and the network
// command interface the in-game save slots already use (gamemenu_states.go).

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// discIndexExt is RetroArch's disc record extension.
const discIndexExt = ".ldci"

// discSwapGap spaces the disc commands: RetroArch acts on one of each per
// frame, and the tray needs a moment open.
const discSwapGap = 250 * time.Millisecond

// discIndexRecord is RetroArch's disc record. Version is kept when RetroArch
// wrote one.
type discIndexRecord struct {
	Version    string `json:"version,omitempty"`
	ImageIndex uint   `json:"image_index"`
	ImagePath  string `json:"image_path"`
}

// discIndexEncode writes a record the way RetroArch reads it: plain JSON, no
// HTML escaping (a "Tom & Jerry" disc stays readable), a newline at the end.
func discIndexEncode(rec discIndexRecord) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rec); err != nil {
		return nil
	}
	return buf.Bytes()
}

// discIndexDecode reads a record; ok is false for anything that is not one.
func discIndexDecode(data []byte) (discIndexRecord, bool) {
	var rec discIndexRecord
	if len(data) == 0 || len(data) > 64<<10 || json.Unmarshal(data, &rec) != nil {
		return discIndexRecord{}, false
	}
	return rec, true
}

// discM3UEntries are a playlist's discs, each line as written (what a core
// joins to the playlist's folder): no comments, no blank lines, no
// "|label" RetroArch allows after a name.
func discM3UEntries(path string) []string {
	if !strings.EqualFold(filepath.Ext(path), ".m3u") {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, romSheetMaxBytes))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if bar := strings.IndexByte(line, '|'); bar >= 0 {
			line = strings.TrimSpace(line[:bar])
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// discImagePath is the path a core reports for a playlist line: the folder of
// the content path as RetroArch was given it (not cleaned: "../" stays), the
// platform's separator, the line. A line that is a whole path is kept.
func discImagePath(contentArg, line string) string {
	if filepath.IsAbs(line) {
		return line
	}
	cut := strings.LastIndexAny(contentArg, `/\`)
	if cut < 0 {
		return line
	}
	return contentArg[:cut] + string(filepath.Separator) + line
}

// discContentArg is the playlist path a launch gives RetroArch
// (launchLibraryGame's romArg).
func discContentArg(launcher, romPath string) string {
	if IsWindows() {
		return romPath
	}
	return firmwareROMArg(launcher, romPath)
}

// discConfigOn reports a RetroArch boolean that is on.
func discConfigOn(text, key string) bool {
	v, ok := retroArchConfigValue(text, key)
	v = strings.Trim(strings.TrimSpace(v), `"`)
	return ok && (strings.EqualFold(v, "true") || v == "1")
}

// discLaunchConfig is the retroarch.cfg the launcher's RetroArch reads.
func discLaunchConfig(launcher string) (text, cfgPath string) {
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
		if raw, err := os.ReadFile(path); err == nil {
			return string(raw), path
		}
	}
	text, cfgPath, _ = retroArchConfig()
	return text, cfgPath
}

// discIndexPaths are the records a game's Disc row writes: first where this
// launch's RetroArch keeps it (its saves folder, sorted as its config says),
// then any record RetroArch already wrote in another of its save folders.
func discIndexPaths(game libraryGame, launcher string) []string {
	stem := gameRenameStem(game.Path)
	if stem == "" {
		return nil
	}
	name := stem + discIndexExt
	contentDir := filepath.Dir(filepath.Clean(game.Path))
	text, cfgPath := discLaunchConfig(launcher)
	core := normalizeCoreName(gameCoreFromLauncher(launcher, game.System))
	coreDir := ""
	if names := resetCoreFolders(core); len(names) > 0 {
		coreDir = names[0]
	}
	saveDir := ""
	if v, ok := retroArchConfigValue(text, "savefile_directory"); ok {
		saveDir = resolveRetroArchSaveDir(v, cfgPath)
	}
	primary := filepath.Join(contentDir, name)
	if saveDir != "" && !discConfigOn(text, "savefiles_in_content_dir") {
		dir := saveDir
		// RetroArch adds the content folder, then the core's name.
		if discConfigOn(text, "sort_savefiles_by_content_enable") {
			dir = filepath.Join(dir, filepath.Base(contentDir))
		}
		if discConfigOn(text, "sort_savefiles_enable") && coreDir != "" {
			dir = filepath.Join(dir, coreDir)
		}
		primary = filepath.Join(dir, name)
	}
	out := []string{primary}
	add := func(p string) {
		for _, have := range out {
			if strings.EqualFold(have, p) {
				return
			}
		}
		if regularFile(p) {
			out = append(out, p)
		}
	}
	add(filepath.Join(contentDir, name))
	var cores []string
	if core != "" {
		cores = append(cores, core)
	}
	for _, r := range saveManagerRoots() {
		if r.Name != "RetroArch saves" {
			continue
		}
		add(filepath.Join(r.Path, name))
		for _, rel := range gameResetStateSubdirs(game.Path, cores) {
			add(filepath.Join(r.Path, rel, name))
		}
		if coreDir != "" {
			add(filepath.Join(r.Path, filepath.Base(contentDir), coreDir, name))
		}
	}
	return out
}

// discReadStart is the disc (from 0) the game starts on, from the record
// RetroArch reads, else the newest other one; found is false with none.
func discReadStart(paths []string) (index int, rec discIndexRecord, found bool) {
	var newest time.Time
	for i, p := range paths {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || (found && !info.ModTime().After(newest)) {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		r, ok := discIndexDecode(data)
		if !ok {
			continue
		}
		index, rec, found, newest = int(r.ImageIndex), r, true, info.ModTime()
		if i == 0 {
			// The record this launch's RetroArch reads wins.
			return index, rec, true
		}
	}
	return index, rec, found
}

// discWriteStart writes the record that makes RetroArch start the game on disc
// index (from 0) into every place it may read it. Off or on the main thread.
func discWriteStart(game libraryGame, launcher string, index int) error {
	entries := discM3UEntries(game.Path)
	if index < 0 || index >= len(entries) {
		return os.ErrInvalid
	}
	paths := discIndexPaths(game, launcher)
	_, old, _ := discReadStart(paths)
	rec := discIndexRecord{Version: old.Version, ImageIndex: uint(index),
		ImagePath: discImagePath(discContentArg(launcher, game.Path), entries[index])}
	data := discIndexEncode(rec)
	if data == nil {
		return os.ErrInvalid
	}
	var firstErr error
	written := 0
	for _, p := range paths {
		if have, err := os.ReadFile(p); err == nil && bytes.Equal(have, data) {
			written++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := AtomicWrite(p, data, 0o644); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written++
	}
	if written == 0 && firstErr != nil {
		return firstErr
	}
	logInfo("library", "%s starts on disc %d of %d (%s)", filepath.Base(game.Path), index+1, len(entries), strings.Join(paths, ", "))
	return nil
}

// ── The options sheet's Disc row ────────────────────────────────────────────

// discOpts is the Disc row's game, read when the options sheet opens. Main
// thread.
var discOpts struct {
	path     string
	game     libraryGame
	count    int
	start    int
	launcher string
	ra       bool
}

// discOptionsOpen reads the sheet's game's discs and start disc
// (openGameOptions). A game on one disc has no Disc row.
func discOptionsOpen(game libraryGame) {
	discOpts.path, discOpts.count, discOpts.start = "", 0, 0
	if !strings.EqualFold(filepath.Ext(game.Path), ".m3u") {
		return
	}
	count := len(discM3UEntries(game.Path))
	if count < 2 {
		return
	}
	launcher := resolveGameLauncherFor(game)
	discOpts.path, discOpts.game, discOpts.count, discOpts.launcher = game.Path, game, count, launcher
	discOpts.ra = retroArchLauncher(launcher)
	if index, _, found := discReadStart(discIndexPaths(game, launcher)); found && index >= 0 && index < count {
		discOpts.start = index
	}
}

// discOptionsShown reports the Disc row on the open sheet.
func discOptionsShown() bool {
	return discOpts.count >= 2 && discOpts.path != "" && discOpts.path == gameOptsUI.game.Path
}

// gameOptionsDefaultRowAt is the row at position pos of an emulated game's
// sheet: every row, the Disc row only for a game on more than one disc.
func gameOptionsDefaultRowAt(pos int) int {
	if pos < 0 {
		return -1
	}
	if !discOptionsShown() && pos >= gameOptionsRowDisc {
		pos++
	}
	if pos >= gameOptionsRowCount {
		return -1
	}
	return pos
}

// gameOptionsDefaultRows is how many rows an emulated game's sheet has.
func gameOptionsDefaultRows() int {
	if discOptionsShown() {
		return gameOptionsRowCount
	}
	return gameOptionsRowCount - 1
}

// discOptionsHint is the Disc row's second line.
func discOptionsHint() string {
	if !discOpts.ra {
		return "Its emulator starts on disc 1 · RetroArch can start on another"
	}
	return "RetroArch starts it on this disc, then remembers the last one played"
}

// discOptionsValue is the Disc row's value, with arrows while it has focus.
func discOptionsValue() string {
	value := "Disc " + strconv.Itoa(discOpts.start+1) + " of " + strconv.Itoa(discOpts.count)
	if discOpts.start > 0 {
		value = "Starts on disc " + strconv.Itoa(discOpts.start+1) + " of " + strconv.Itoa(discOpts.count)
	}
	if discOpts.ra && gameOptionsRowAt(gameOptsUI.cursor) == gameOptionsRowDisc {
		value = "‹  " + value + "  ›"
	}
	return value
}

// discOptionsStep moves the start disc by delta, wrapping, and writes it. It
// reports whether anything changed.
func discOptionsStep(delta int) bool {
	if !discOptionsShown() || delta == 0 {
		return false
	}
	// The Emulator row above may have changed the game's emulator since the
	// sheet opened: the record goes where that one's RetroArch reads it.
	if l := resolveGameLauncherFor(discOpts.game); l != discOpts.launcher {
		discOpts.launcher, discOpts.ra = l, retroArchLauncher(l)
	}
	if !discOpts.ra {
		ShowToast("Only RetroArch can start a game on another disc · this game's emulator starts on disc 1", ToastKindInfo)
		return false
	}
	n := discOpts.count
	next := ((discOpts.start+delta)%n + n) % n
	if err := discWriteStart(discOpts.game, discOpts.launcher, next); err != nil {
		logWarn("library", "cannot set the start disc of %s: %v", discOpts.path, err)
		ShowToast("Could not set the start disc: "+firstLine(err.Error()), ToastKindWarning)
		return false
	}
	discOpts.start = next
	return true
}

// ── Launch ──────────────────────────────────────────────────────────────────

// discSession is the multi-disc game the library last started in RetroArch
// and the disc in its tray, as far as JukaHub moved it. Written by the launch
// (main thread) and the swap goroutine.
var discSession struct {
	sync.Mutex
	rom   string
	count int
	cur   int
	// target is the disc the menu's row points at; targetFor the menu
	// opening it was set in.
	target    int
	targetFor time.Time
	// swapping is set while discSwap sends a change.
	swapping bool
}

// prepareDiscStart is the launch hook for a library game (game_library.go):
// for a playlist RetroArch plays, the start record's disc path is made the
// one this launch's core will report, and the in-game menu learns the game's
// discs. It never blocks or fails a launch.
func prepareDiscStart(launcher string, game libraryGame) {
	discSession.Lock()
	discSession.rom, discSession.count, discSession.cur, discSession.target = "", 0, 0, 0
	discSession.targetFor = time.Time{}
	discSession.Unlock()
	if !strings.EqualFold(filepath.Ext(game.Path), ".m3u") || !retroArchLauncher(launcher) {
		return
	}
	entries := discM3UEntries(game.Path)
	if len(entries) < 2 {
		return
	}
	paths := discIndexPaths(game, launcher)
	index, rec, found := discReadStart(paths)
	if index < 0 || index >= len(entries) {
		index = 0
	}
	// Rewritten when the path is not this launch's, or when the record that
	// says so is not the one this launch's RetroArch reads (paths[0]: it was
	// found in another save folder, after the launcher or RetroArch's sorting
	// changed).
	if found && index > 0 && (rec.ImagePath != discImagePath(discContentArg(launcher, game.Path), entries[index]) || !regularFile(paths[0])) {
		if err := discWriteStart(game, launcher, index); err != nil {
			logWarn("library", "cannot point %s's start disc at this launch: %v", filepath.Base(game.Path), err)
		}
	}
	discSession.Lock()
	discSession.rom, discSession.count, discSession.cur, discSession.target = game.Path, len(entries), index, index
	discSession.Unlock()
}

// ── The in-game menu's Disc row ─────────────────────────────────────────────

// gmDiscAvailable reports the Disc row: a multi-disc RetroArch game the
// library started, with the command port the save slots use.
func gmDiscAvailable() bool {
	if !gmStatesAvailable() {
		return false
	}
	discSession.Lock()
	defer discSession.Unlock()
	if discSession.count < 2 || discSession.rom == "" || discSession.rom != gmPick.rom {
		return false
	}
	if !discSession.targetFor.Equal(gaOverlay.openedAt) {
		discSession.target, discSession.targetFor = discSession.cur, gaOverlay.openedAt
	}
	return true
}

// gmDiscLabel is the row's words.
func gmDiscLabel() (string, string) {
	discSession.Lock()
	cur, target, count := discSession.cur, discSession.target, discSession.count
	discSession.Unlock()
	of := " of " + strconv.Itoa(count)
	if target != cur {
		return "Put in disc " + strconv.Itoa(target+1) + of, orbKeyName("A") + " swaps discs and plays on · now disc " + strconv.Itoa(cur+1)
	}
	return "Disc " + strconv.Itoa(cur+1) + of, "Left/Right pick a disc · " + orbKeyName("A") + " puts in the next one"
}

// gmDiscStep points the row at the next or previous disc.
func gmDiscStep(next bool) {
	discSession.Lock()
	if discSession.count < 2 {
		discSession.Unlock()
		return
	}
	d := -1
	if next {
		d = 1
	}
	discSession.target = (discSession.target + d + discSession.count) % discSession.count
	discSession.Unlock()
	PlayNavSound()
}

// gmDiscApply gives the game back and swaps to the disc the row points at (the
// next one when it points at the disc in the tray).
func gmDiscApply() {
	discSession.Lock()
	rom, count, from, to := discSession.rom, discSession.count, discSession.cur, discSession.target
	busy := discSession.swapping
	if !busy && count >= 2 && rom != "" {
		discSession.swapping = true
	}
	discSession.Unlock()
	if count < 2 || rom == "" {
		return
	}
	if busy {
		// The last change is still being sent: its "from" would be wrong.
		ShowToast("Still changing discs · try again in a moment", ToastKindInfo)
		return
	}
	if to == from {
		to = (from + 1) % count
	}
	port := gmPick.port
	PlayActivateSound()
	gaClose("the player changed disc", true)
	go discSwap(port, rom, from, to)
}

// discSwap sends RetroArch the eject, step and close commands once the game
// runs again.
func discSwap(port int, rom string, from, to int) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("gamemenu", "the disc change failed: %v", r)
		}
		discSession.Lock()
		discSession.swapping = false
		discSession.Unlock()
	}()
	time.Sleep(gmStateResumeDelay)
	discSession.Lock()
	same := discSession.rom == rom
	discSession.Unlock()
	if !same {
		return
	}
	name := gameDisplayTitle(rom)
	send := func(cmd string) bool {
		if err := sendRetroArchNetCmd(port, cmd); err != nil {
			logWarn("gamemenu", "%s: %s was not sent: %v", name, cmd, err)
			return false
		}
		time.Sleep(discSwapGap)
		return true
	}
	step, n := "DISK_NEXT", to-from
	if n < 0 {
		step, n = "DISK_PREV", -n
	}
	if !send("DISK_EJECT_TOGGLE") {
		return
	}
	for k := 0; k < n; k++ {
		if !send(step) {
			// The tray is open: close it on whatever disc is in.
			send("DISK_EJECT_TOGGLE")
			return
		}
	}
	if !send("DISK_EJECT_TOGGLE") {
		return
	}
	discSession.Lock()
	if discSession.rom == rom {
		discSession.cur, discSession.target = to, to
	}
	discSession.Unlock()
	logInfo("gamemenu", "%s: disc %d → disc %d", name, from+1, to+1)
}
