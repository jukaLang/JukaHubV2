package main

// gameready.go — "is this game ready to start?", asked before it starts
// instead of after it failed.
//
// The game diagnosis (gamediag.go, gamediag_bios.go) already knows the usual
// reasons a game cannot start: a BIOS its core cannot start without, a .cue or
// .m3u whose files are not beside it, an empty or damaged archive, a core the
// launch script names that is not installed. It used to say so only after a
// black second and a drop back to JukaHub. Game Ready runs the same checks at
// the moment they are needed:
//
//   - as a gate in the library's launch path (gamelaunch_hooks.go): a game that
//     cannot start shows a card with the fix instead of starting — the files,
//     the full folder to copy them into, and a button for the fix (BIOS
//     Doctor, Get more cores, the Emulators page with the console highlighted);
//   - as a chip on the game's page ("Ready", "Needs BIOS", "Missing disc file",
//     "No emulator") filled in the background when the page opens, and a
//     "Ready check" row in its More sheet that shows the same card as a report,
//     with a tick for each thing that was checked.
//
// What holds a launch, and what only informs. Only a certain finding holds
// one (gameDiagSure: "the game cannot start without it"): a "likely" or "maybe"
// (a BIOS a core usually boots without, a BIOS folder guessed because no
// retroarch.cfg was readable, a core that may not load — the launch path itself
// fetches the newer C library such a core usually needs, ra_glibc.go) is shown
// on the card and the page but never stops a game. A missing core is certain
// only when the script names that one core and the file is in none of
// RetroArch's cores folders nor beside the script. On a PC with no controller
// connected the card says so, but never holds the launch for it. The one other
// hold is Local play (hotseat.go): a player whose controller is gone would
// start the game with the wrong seats, so the card offers to drop them.
//
// What it costs. The gate runs on the main thread, so it reads only what the
// diagnosis reads — stats, a retroarch.cfg, a launch script's head, a .cue or
// .m3u, a zip's directory, never a whole archive and never the core itself
// (gameReadyCoreQuick) — and keeps each answer for
// gameReadyFresh per game and emulator; the page's goroutine usually answers
// first. Controllers are SDL's, so they are asked on the main thread each time.
//
// What never sees a card: a start nobody pressed for (the resume at power-on,
// quickresume.go; hakuxUnasked), the launch path's own second round after a
// save download or the private C library's install (savesync_auto.go,
// ra_glibc.go), and a start the player already waved through: "Play anyway"
// leaves a pass for that game until it starts (gameReadyPassFor). Pressing
// Play anyway twice for one game, or "Don't ask again" on its report, stops the
// card for that game (game-ready.json); its chip still tells.

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// gameReadyFresh is how long one game's answer is reused: a player who
	// copies a BIOS in and presses Play again a minute later is checked again.
	gameReadyFresh = 30 * time.Second
	// gameReadyPassFor bounds the pass "Play anyway" leaves: until the game
	// starts, or this long — a save download or the C library's install
	// before the start can take minutes on handheld Wi-Fi.
	gameReadyPassFor = 10 * time.Minute
	// gameReadyCacheMax bounds the answers kept in memory.
	gameReadyCacheMax = 64
	// gameReadyQuietAfter is how many "Play anyway" stop the card for a game.
	gameReadyQuietAfter = 2
	// gameReadyMemoryName is the per-game record in JukaHub's data folder.
	gameReadyMemoryName   = "game-ready.json"
	gameReadyMemorySchema = 1
	gameReadyMemoryMax    = 2000
)

// gameReadyKind is what a finding is about: it picks the chip's words and the
// card's fix.
type gameReadyKind int

const (
	gameReadyKindFile gameReadyKind = iota
	gameReadyKindDisc
	gameReadyKindEmulator
	gameReadyKindCore
	gameReadyKindBIOS
	gameReadyKindController
	gameReadyKindLocalPlay
)

// gameReadyIssue is one finding: the diagnosis's words plus what it is about
// and whether it holds the launch.
type gameReadyIssue struct {
	launchIssue
	kind  gameReadyKind
	block bool
}

// gameReadyResult is one game's answer for one emulator.
type gameReadyResult struct {
	path     string
	launcher string
	system   string
	at       time.Time
	// emulator names what starts the game ("RetroArch · mGBA"); "" for none.
	emulator string
	// desktopOnly: no emulator, and the console needs a PC (emuDesktopSystems).
	desktopOnly bool
	// quick: the gate's check, without the core's load and build advisories.
	quick  bool
	issues []gameReadyIssue
	// checked are the things that were fine, for the report's ticks.
	checked []string
}

// add records a finding.
func (r *gameReadyResult) add(is launchIssue, kind gameReadyKind, block bool) {
	r.issues = append(r.issues, gameReadyIssue{launchIssue: is, kind: kind, block: block})
}

// The answers, the checks running for the page and the passes. Main thread.
var (
	gameReadyCache    = map[string]gameReadyResult{}
	gameReadyLatest   = map[string]gameReadyResult{}
	gameReadyChecking = map[string]bool{}
	gameReadyPasses   = map[string]time.Time{}
	// gameReadyBootSeen: the resume at power-on went through once.
	gameReadyBootSeen bool
)

func init() {
	gameLaunchRegisterGate(gameReadyGate)
	gameLaunchRegisterStarted(func(game libraryGame, _ string, _ *exec.Cmd) {
		// The pass "Play anyway" left is used up: the next start asks again.
		delete(gameReadyPasses, game.Path)
	})
	gamePageRegisterOpened(gameReadyPageOpened)
	gamePageRegisterStatus(gamePageStatus{ID: "ready", Order: 10, Chip: gameReadyChip})
	gamePageRegisterExtra(gamePageExtra{ID: "ready", Order: 20, Icon: "check", Row: gameReadyRow, Open: gameReadyOpenReport})
}

// gameReadyApplies reports whether a game is one the check is about: a ROM an
// emulator starts. Android, HTML5, ports and JukaHub's own games have checks
// of their own.
func gameReadyApplies(game libraryGame) bool {
	switch game.System {
	case androidLibrarySystem, html5LibrarySystem, portsLibrarySystem:
		return false
	}
	return strings.TrimSpace(game.Path) != "" && !isBuiltInLibraryGame(game)
}

// ── The checks ───────────────────────────────────────────────────────────

// gameReadyCompute runs the file checks for one game and emulator. It reads
// the disk and no SDL state, so the page runs it on a goroutine. quick is the
// gate's form, on the main thread: the core is only looked for, not read (the
// diagnosis's load check parses the core's ELF tables and its build check
// checksums a whole GPU core — advisories that never hold a launch).
func gameReadyCompute(game libraryGame, launcher string, quick bool) gameReadyResult {
	launcher = strings.TrimSpace(launcher)
	res := gameReadyResult{path: game.Path, launcher: launcher, system: game.System, at: time.Now(), quick: quick}
	system := strings.TrimSpace(canonicalCompatSystem(game.System))
	if system == "" {
		system = game.System
	}
	// The game file: gone, empty, damaged, an empty zip, a disc's tracks.
	// Only a disc's index (.cue, .gdi, .ccd, .m3u) has disc files to miss: a
	// cartridge that is gone names its own file too.
	romIssues := gameDiagROMIssues(game.System, game.Path)
	discIndex := false
	switch strings.ToLower(filepath.Ext(game.Path)) {
	case ".cue", ".gdi", ".ccd", ".m3u":
		discIndex = true
	}
	for _, is := range romIssues {
		kind := gameReadyKindFile
		if len(is.Files) > 0 && discIndex {
			kind = gameReadyKindDisc
		}
		res.add(is, kind, is.Level == int(gameDiagSure))
	}
	if len(romIssues) == 0 {
		res.checked = append(res.checked, "Game file: "+filepath.Base(game.Path))
	}
	if launcher == "" {
		_, desktop := emuDesktopSystemFor(game.System)
		res.desktopOnly = desktop && !IsWindows() && !strings.EqualFold(strings.TrimSpace(game.System), emuXboxEntryName)
		title := "No emulator for " + system + " yet"
		if res.desktopOnly {
			title = system + " needs a PC"
		}
		// The launch path's own answers: what to install, why this device
		// cannot run it, which NextUI pak is missing.
		detail := emuLibraryLauncherHint(game.System)
		if detail == "" {
			detail = nextuiMissingPakHint(game.Path)
		}
		if detail == "" {
			detail = "Install or turn on an emulator for " + system + " on the Emulators page."
		}
		res.add(launchIssue{Level: int(gameDiagSure), Title: title, Detail: detail}, gameReadyKindEmulator, true)
		return res
	}
	res.emulator = gameReadyEmulatorLabel(launcher, game.System)
	res.checked = append(res.checked, "Emulator: "+res.emulator)
	if !retroArchLauncher(launcher) {
		// A standalone emulator: its own setup is its own (xemu's firmware
		// is checked by the launch path, emuStandaloneLaunchCheck).
		return res
	}
	var core string
	var known bool
	var coreIssues []launchIssue
	if quick {
		core, known, coreIssues = gameReadyCoreQuick(launcher, game.System)
	} else {
		core, known, coreIssues = gameDiagCoreIssues(launcher, game.System)
	}
	for _, is := range coreIssues {
		block := is.Level == int(gameDiagSure) && known && !gameReadyCoreBeside(launcher, core)
		res.add(is, gameReadyKindCore, block)
	}
	if core == "" {
		return res
	}
	if len(coreIssues) == 0 {
		// "Installed" only when the launcher named the one core looked for.
		line := "Core: " + coreConfigDisplayName(core)
		if known {
			line += " is installed"
		}
		res.checked = append(res.checked, line)
	}
	biosIssues := gameDiagBIOSIssues(game.System, game.Path, launcher, core, known)
	dirSure := false
	if len(biosIssues) > 0 {
		// A BIOS folder guessed because no retroarch.cfg was readable may not
		// be the one RetroArch reads: said, never held for.
		_, dirSure, _ = gameDiagSystemDir(launcher, game.Path)
	}
	for _, is := range biosIssues {
		res.add(is, gameReadyKindBIOS, is.Level == int(gameDiagSure) && dirSure)
	}
	if len(biosIssues) == 0 {
		if gameReadyBIOSWanted(game.System, game.Path, core) {
			res.checked = append(res.checked, "BIOS: the files "+coreConfigDisplayName(core)+" needs are there")
		} else {
			res.checked = append(res.checked, "BIOS: "+coreConfigDisplayName(core)+" needs none")
		}
	}
	return res
}

// gameReadyEmulatorLabel names what starts a game, for the card's subtitle.
func gameReadyEmulatorLabel(launcher, system string) string {
	if launcher == "" {
		return ""
	}
	if retroArchLauncher(launcher) {
		if core := gameCoreFromLauncher(launcher, system); core != "" {
			return "RetroArch · " + coreConfigDisplayName(core)
		}
		return "RetroArch"
	}
	base := filepath.Base(launcher)
	if strings.EqualFold(base, "launch.sh") {
		// A firmware launcher is known by its folder (Emus/PS).
		return filepath.Base(filepath.Dir(launcher))
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// gameReadyCoreBeside reports a core file next to the launch script (or in
// its cores folder): a script that loads its own copy, which the diagnosis's
// "not installed" does not see.
func gameReadyCoreBeside(launcher, core string) bool {
	if core == "" {
		return false
	}
	dir := filepath.Dir(launcher)
	for _, d := range []string{dir, filepath.Join(dir, "cores")} {
		if _, ok := gameDiagFileIn(d, coreBuildFileName(core)); ok {
			return true
		}
	}
	return false
}

// gameReadyCoreQuick is gameDiagCoreIssues without reading the core: the one
// finding that can hold a launch, "not installed", in the diagnosis's words
// (keep them in step). A core started directly is the launcher itself, which
// the launch path has just seen.
func gameReadyCoreQuick(launcher, system string) (core string, known bool, issues []launchIssue) {
	core = gameCoreFromLauncher(launcher, system)
	if ext := strings.ToLower(filepath.Ext(launcher)); ext == ".so" || ext == ".dll" {
		return core, true, nil
	}
	head, err := gameDiagReadHead(launcher)
	if err != nil {
		return core, false, nil
	}
	names := coreNamesIn(string(head))
	if len(names) == 0 || core == "" {
		return core, false, nil
	}
	known = len(names) == 1
	dirs := retroArchCoreDirs()
	if len(dirs) == 0 || coreActiveBinary(core, dirs) != "" {
		return core, known, nil
	}
	return core, known, []launchIssue{{
		Level: int(gameDiagSure),
		Title: "The " + coreConfigDisplayName(core) + " core is not installed",
		Files: []string{coreBuildFileName(core)},
		Where: dirs[0],
		Detail: filepath.Base(launcher) + " starts RetroArch with this core and it is in none of RetroArch's cores folders. " +
			"Copy the core file there, or install it with RetroArch's core downloader.",
	}}
}

// gameReadyBIOSWanted reports whether the firmware table has a row for this
// game and core, so the report can tell "found" from "none needed".
func gameReadyBIOSWanted(system, romPath, core string) bool {
	keys := gameDiagSystemKeys(system, romPath)
	for _, row := range gameDiagBIOSTable {
		if !row.matches(keys, romPath) {
			continue
		}
		if _, ok := row.level(core); ok {
			return true
		}
	}
	return false
}

// gameReadyLiveIssues are the checks SDL answers: the controllers on a PC and
// the Local play seats. Main thread.
func gameReadyLiveIssues(launcher string) (issues []gameReadyIssue, checked []string) {
	if !IsTrimuiDevice() {
		if hotseatControllerCount() == 0 {
			issues = append(issues, gameReadyIssue{kind: gameReadyKindController, launchIssue: launchIssue{
				Level:  int(gameDiagMaybe),
				Title:  "No controller connected",
				Detail: "Connect one, or play with the keyboard.",
			}})
		} else {
			name := "connected"
			if gameController != nil {
				if n := strings.TrimSpace(gameController.Name()); n != "" {
					name = n
				}
			}
			checked = append(checked, "Controller: "+name)
		}
	}
	players := len(hotseat.players)
	if players < 2 || launcher == "" {
		return issues, checked
	}
	// The seats reach only a RetroArch launch whose launcher passes options
	// on (hotseatLaunchArgs): a gone pad matters there alone.
	applied := retroArchLauncher(launcher) && hotseatLauncherForwards(launcher)
	var gone []string
	for i, p := range hotseat.players {
		if applied && hotseatJoypadIndex(p.id) < 0 {
			gone = append(gone, hotseatSeatName(i))
		}
	}
	switch {
	case len(gone) > 0:
		issues = append(issues, gameReadyIssue{kind: gameReadyKindLocalPlay, block: true, launchIssue: launchIssue{
			Level: int(gameDiagSure),
			Title: "Local play: a player's controller is gone",
			Detail: strings.Join(gone, ", ") + " joined with a controller that is not connected now, so the seats would be wrong. " +
				"Connect it again and check again, or remove the missing players.",
		}})
	case launcher != "" && !retroArchLauncher(launcher):
		issues = append(issues, gameReadyIssue{kind: gameReadyKindLocalPlay, launchIssue: launchIssue{
			Level:  int(gameDiagMaybe),
			Title:  "Local play's player order won't reach this emulator",
			Detail: "It sets up its own controllers: check them in its settings.",
		}})
	case launcher != "" && !hotseatLauncherForwards(launcher):
		issues = append(issues, gameReadyIssue{kind: gameReadyKindLocalPlay, launchIssue: launchIssue{
			Level:  int(gameDiagMaybe),
			Title:  "Local play's player order won't reach RetroArch",
			Detail: "This system's launcher does not pass options on, so RetroArch's own controller order is used.",
		}})
	default:
		checked = append(checked, "Local play: "+strconv.Itoa(players)+" players")
	}
	return issues, checked
}

// gameReadyHolds reports whether anything in issues holds a launch.
func gameReadyHolds(issues []gameReadyIssue) bool {
	for _, is := range issues {
		if is.block {
			return true
		}
	}
	return false
}

// gameReadyReasons is the log line's list of what held a launch.
func gameReadyReasons(issues []gameReadyIssue) string {
	var out []string
	for _, is := range issues {
		if !is.block {
			continue
		}
		reason := is.Title
		if len(is.Files) > 0 {
			reason += " (" + strings.Join(is.Files, ", ") + ")"
		}
		if is.Where != "" {
			reason += " in " + is.Where
		}
		out = append(out, reason)
	}
	return strings.Join(out, "; ")
}

// ── The cache ────────────────────────────────────────────────────────────

func gameReadyCacheKey(path, launcher string) string { return path + "\x00" + launcher }

// gameReadyFor is the answer for a game and emulator: the cached one while it
// is fresh, else checked now (quick: the gate's form; a full answer serves it
// too). Main thread.
func gameReadyFor(game libraryGame, launcher string, quick bool) gameReadyResult {
	if res, ok := gameReadyCache[gameReadyCacheKey(game.Path, launcher)]; ok && time.Since(res.at) < gameReadyFresh && (quick || !res.quick) {
		return res
	}
	res := gameReadyCompute(game, launcher, quick)
	gameReadyStore(res)
	return res
}

// gameReadyStore keeps an answer, and the page's latest one for its chip.
func gameReadyStore(res gameReadyResult) {
	if len(gameReadyCache) >= gameReadyCacheMax {
		gameReadyCache = map[string]gameReadyResult{}
	}
	gameReadyCache[gameReadyCacheKey(res.path, res.launcher)] = res
	if len(gameReadyLatest) >= gameReadyCacheMax {
		gameReadyLatest = map[string]gameReadyResult{}
	}
	gameReadyLatest[res.path] = res
}

// gameReadyForget drops a game's answers, its chip's too: the player says
// something changed (gameReadyRefreshPage reads it again).
func gameReadyForget(path string) {
	for key, res := range gameReadyCache {
		if res.path == path {
			delete(gameReadyCache, key)
		}
	}
	delete(gameReadyLatest, path)
}

// gameReadyRefreshPage checks the open game page's game again in the
// background, for its chip: back from a page a fix opened. Main thread.
func gameReadyRefreshPage() {
	if gmetaUI.open {
		gameReadyForget(gmetaUI.game.Path)
		gameReadyPageOpened(gmetaUI.game)
	}
}

// ── The gate ─────────────────────────────────────────────────────────────

// gameReadyGate is launchLibraryGame's gate: it holds a start that has
// something to show the player first and opens the card. Main thread.
func gameReadyGate(game libraryGame, launcher string) (held bool, err error) {
	defer func() {
		// A check that fails must never be why a game does not start.
		if r := recover(); r != nil {
			logWarn("library", "Game Ready check for %s failed: %v", filepath.Base(game.Path), r)
			held, err = false, nil
		}
	}()
	if !gameReadyApplies(game) || gameReadyLetThrough(game) {
		return false, nil
	}
	// A DOS game started by a program the player replaced is sent on to its
	// current one by the shelf's gate; the check runs for that start instead.
	if game.System == "DOS" {
		if _, moved := shelfDOSRedirect(game.Path); moved {
			return false, nil
		}
	}
	launcher = strings.TrimSpace(launcher)
	res := gameReadyFor(game, launcher, true)
	live, _ := gameReadyLiveIssues(launcher)
	issues := append(append([]gameReadyIssue(nil), res.issues...), live...)
	if !gameReadyHolds(issues) {
		return false, nil
	}
	if launcher != "" && gameReadyQuiet(game.Path) {
		logInfo("library", "Game Ready: %s starts without the card (the player asked): %s", filepath.Base(game.Path), gameReadyReasons(issues))
		return false, nil
	}
	logInfo("library", "Game Ready held %s (%s, %s): %s", filepath.Base(game.Path), game.System, setOr(res.emulator, "no emulator"), gameReadyReasons(issues))
	if !gameReadyUI.open {
		gameReadyOpenCard(game, launcher, res, false)
	}
	gameReadyHushCallerToast(game)
	return true, nil
}

// gameReadyLetThrough reports a start the card must not hold: one nobody
// pressed for, the launch path's own second round, or one already waved
// through. Main thread.
func gameReadyLetThrough(game libraryGame) bool {
	path := game.Path
	// The resume at power-on: hakuxStartUnasked's flag, or the first start of
	// the game quickresume.go just dispatched.
	if hakuxUnasked {
		return true
	}
	if !gameReadyBootSeen && !quickBoot.pending && !quickBoot.since.IsZero() &&
		quickBoot.marker.Path != "" && filepath.Clean(quickBoot.marker.Path) == filepath.Clean(path) {
		gameReadyBootSeen = true
		return true
	}
	// The launch path running again after the saves came down, the C library
	// was installed, Dolphin or ARMSX2 was set up (dolphin.go and ps2.go set
	// their markers before the re-entry and read them after the gates) or
	// hakuX got its build for this screen (hakux_fit.go).
	if saveSyncAuto.resume == path || raGlibcInstall.resume == path || dolphinResume == path || ps2Resume == path ||
		hakuxFitResume == path {
		return true
	}
	if until, ok := gameReadyPasses[path]; ok {
		if time.Now().Before(until) {
			return true
		}
		delete(gameReadyPasses, path)
	}
	return false
}

// gameReadyGrantPass lets the next starts of path through until it starts.
func gameReadyGrantPass(path string) {
	gameReadyPasses[path] = time.Now().Add(gameReadyPassFor)
}

// gameReadyHushCallerToast takes back the "Playing …" toast the caller shows
// for a launch the gate held: launchLibraryGame returns no error then.
func gameReadyHushCallerToast(game libraryGame) {
	names := []string{strings.TrimSpace(game.Title), strings.TrimSpace(game.Label())}
	currentToastMu.Lock()
	beforeAt, beforeMsg := currentToast.StartedAt, currentToast.Message
	currentToastMu.Unlock()
	tbxPost(func() {
		currentToastMu.Lock()
		defer currentToastMu.Unlock()
		if currentToast.Kind != ToastKindSuccess {
			return
		}
		// A success raised since the gate ran is the caller's, whatever title
		// it names (Get Homebrew says the store's, not the file's).
		if currentToast.StartedAt != beforeAt || currentToast.Message != beforeMsg {
			currentToast = ToastState{}
			return
		}
		for _, name := range names {
			if name != "" && strings.Contains(currentToast.Message, name) {
				currentToast = ToastState{}
				return
			}
		}
	})
}

// ── The game page ────────────────────────────────────────────────────────

// gameReadyPageOpened checks the page's game in the background, for its chip.
func gameReadyPageOpened(game libraryGame) {
	if !gameReadyApplies(game) || gameReadyChecking[game.Path] {
		return
	}
	if res, ok := gameReadyLatest[game.Path]; ok && time.Since(res.at) < gameReadyFresh {
		return
	}
	gameReadyChecking[game.Path] = true
	go func() {
		var res gameReadyResult
		ok := false
		defer func() {
			if r := recover(); r != nil {
				logWarn("library", "Game Ready check for %s failed: %v", filepath.Base(game.Path), r)
			}
			tbxPost(func() {
				delete(gameReadyChecking, game.Path)
				if ok {
					gameReadyStore(res)
				}
			})
		}()
		res = gameReadyCompute(game, resolveGameLauncherFor(game), false)
		ok = true
	}()
}

// gameReadyChip is the page's chip, from memory.
func gameReadyChip(game libraryGame) (string, int, bool) {
	if !gameReadyApplies(game) {
		return "", 0, false
	}
	res, ok := gameReadyLatest[game.Path]
	if !ok {
		return "", 0, false
	}
	return gameReadyChipFor(res)
}

// gameReadyChipFor words an answer as a chip.
func gameReadyChipFor(res gameReadyResult) (string, int, bool) {
	maybeBIOS := false
	for _, is := range res.issues {
		if !is.block {
			maybeBIOS = maybeBIOS || is.kind == gameReadyKindBIOS
			continue
		}
		switch is.kind {
		case gameReadyKindEmulator:
			if res.desktopOnly {
				return "Needs a PC", gamePageToneWarn, true
			}
			return "No emulator", gamePageToneWarn, true
		case gameReadyKindBIOS:
			return "Needs BIOS", gamePageToneWarn, true
		case gameReadyKindDisc:
			return "Missing disc file", gamePageToneWarn, true
		case gameReadyKindCore:
			return "Core missing", gamePageToneWarn, true
		default:
			return "Game file problem", gamePageToneWarn, true
		}
	}
	if maybeBIOS {
		return "BIOS may be needed", gamePageToneNeutral, true
	}
	return "Ready", gamePageToneGood, true
}

// gameReadyRow is the More sheet's row.
func gameReadyRow(game libraryGame) (string, string, bool) {
	if !gameReadyApplies(game) {
		return "", "", false
	}
	detail := "The game file, the emulator and the BIOS it needs"
	if res, ok := gameReadyLatest[game.Path]; ok {
		detail = "Ready to play"
		for _, is := range res.issues {
			if is.block {
				detail = is.Title
				break
			}
		}
		if detail == "Ready to play" && len(res.issues) > 0 {
			detail = "Should start · " + res.issues[0].Title
		}
	} else if gameReadyChecking[game.Path] {
		detail = "Checking…"
	}
	if gameReadyQuiet(game.Path) {
		detail += " · no card before it starts"
	}
	return "Ready check", detail, true
}

// gameReadyOpenReport is the row's A (the sheet played the sound): the card
// as a report, checked now.
func gameReadyOpenReport(game libraryGame) {
	launcher := strings.TrimSpace(resolveGameLauncherFor(game))
	gameReadyForget(game.Path)
	gameReadyOpenCard(game, launcher, gameReadyFor(game, launcher, false), true)
}

// ── Per-game memory (game-ready.json) ────────────────────────────────────

// gameReadyMemoryGame is one game's record. Quiet: the card no longer holds
// its starts.
type gameReadyMemoryGame struct {
	PlayedAnyway int  `json:"playedAnyway,omitempty"`
	Quiet        bool `json:"quiet,omitempty"`
}

// gameReadyMemoryFile is game-ready.json. A missing, broken or newer file
// reads as "nothing set".
type gameReadyMemoryFile struct {
	SchemaVersion int                            `json:"schemaVersion"`
	Games         map[string]gameReadyMemoryGame `json:"games,omitempty"`
}

// gameReadyMemory is the record in memory; main thread. Saves run on a
// goroutine, the newest write winning (gameReadyMemorySeq).
var (
	gameReadyMemory struct {
		loaded bool
		file   gameReadyMemoryFile
	}
	gameReadyMemorySeq     atomic.Uint64
	gameReadyMemorySaveMu  sync.Mutex
	gameReadyMemoryWritten uint64
)

func gameReadyMemoryPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, gameReadyMemoryName)
}

// gameReadyMemoryLoad reads the record the first time it is needed.
func gameReadyMemoryLoad() {
	if gameReadyMemory.loaded {
		return
	}
	gameReadyMemory.loaded = true
	gameReadyMemory.file = gameReadyMemoryFile{SchemaVersion: gameReadyMemorySchema, Games: map[string]gameReadyMemoryGame{}}
	path := gameReadyMemoryPath()
	if path == "" {
		return
	}
	var f gameReadyMemoryFile
	if _, err := readJSONWithBackup(path, &f); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logWarn("library", "Game Ready: ignoring %s: %v", path, err)
		}
		return
	}
	if f.SchemaVersion != gameReadyMemorySchema || f.Games == nil {
		return
	}
	gameReadyMemory.file.Games = f.Games
}

// gameReadyMemorySave writes the record off the main thread.
func gameReadyMemorySave() {
	path := gameReadyMemoryPath()
	if path == "" {
		return
	}
	games := gameReadyMemory.file.Games
	if len(games) > gameReadyMemoryMax {
		// The games only played anyway once go first; the quiet ones stay.
		keys := make([]string, 0, len(games))
		for k, g := range games {
			if !g.Quiet {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(games) <= gameReadyMemoryMax {
				break
			}
			delete(games, k)
		}
	}
	data, err := json.MarshalIndent(gameReadyMemory.file, "", "  ")
	if err != nil {
		return
	}
	seq := gameReadyMemorySeq.Add(1)
	go func() {
		gameReadyMemorySaveMu.Lock()
		defer gameReadyMemorySaveMu.Unlock()
		if seq <= gameReadyMemoryWritten {
			return
		}
		if err := AtomicWrite(path, data, 0o644); err != nil {
			logWarn("library", "Game Ready: could not save %s: %v", path, err)
			return
		}
		gameReadyMemoryWritten = seq
	}()
}

// gameReadyQuiet reports whether the card no longer holds path's starts.
func gameReadyQuiet(path string) bool {
	gameReadyMemoryLoad()
	return gameReadyMemory.file.Games[path].Quiet
}

// gameReadySetQuiet turns the card off (or back on) for path.
func gameReadySetQuiet(path string, quiet bool) {
	gameReadyMemoryLoad()
	g := gameReadyMemory.file.Games[path]
	g.Quiet = quiet
	g.PlayedAnyway = 0
	if !quiet {
		delete(gameReadyMemory.file.Games, path)
	} else {
		gameReadyMemory.file.Games[path] = g
	}
	gameReadyMemorySave()
}

// gameReadyNotePlayedAnyway counts a "Play anyway" and reports whether it
// made the game quiet.
func gameReadyNotePlayedAnyway(path string) bool {
	gameReadyMemoryLoad()
	g := gameReadyMemory.file.Games[path]
	g.PlayedAnyway++
	quieted := !g.Quiet && g.PlayedAnyway >= gameReadyQuietAfter
	if quieted {
		g.Quiet = true
	}
	gameReadyMemory.file.Games[path] = g
	gameReadyMemorySave()
	return quieted
}
