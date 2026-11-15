package main

// closeall.go — "Close all apps & games": one action that closes, politely and
// then firmly, everything JukaHub counts as an app or a game, and says what it
// closed and how much memory came back.
//
// Testers found Juka's offline AI refusing with "close a game or app to free
// memory" and no way to do that from where they stood. Now the refusal offers
// the fix (A: close apps and ask again), and the same action sits in Quick
// Settings, Power tools, the Processes panel, the in-game menu ("Close
// background apps", which keeps the game on screen) and the voice commands
// ("close all apps", "free up memory").
//
// What closes:
//   - JukaHub's own screen children (gamesuspend.go): games, emulators, ports,
//     Wine sessions, Android games, the WPE browser engine, LAN-play
//     emulators. Each gets the interrupt the exit path sends (RetroArch and
//     PPSSPP save and quit on it), plus a continue for one paused under the
//     alarm or the in-game overlay; whatever is left after closeAllChildGrace
//     loses its whole process group.
//   - The video player, the Terminal's running command, the library's video
//     snap and the web-video prefetchers. Music keeps playing: it is small,
//     and people ask Juka things while listening.
//   - On a handheld, apps no JukaHub of this run is tracking: an emulator left
//     behind by a crashed JukaHub, a Remote Play client, a PortMaster port,
//     Wine's server. /proc is scanned for the known names in closeAllAppRules
//     (and a few folders: Emus, ports, AndroidRT); each gets SIGTERM and
//     SIGCONT, then SIGKILL after closeAllProcGrace. A PID is only signalled
//     while its start time still matches the scan, so a reused PID is safe.
//
// What never closes: init and kernel threads, JukaHub itself and every
// process above it (launch.sh, the firmware's supervisor), firmware daemons,
// SSH/FTP servers, JukaHub's own helpers (anything in its folder that no rule
// names) and the offline AI and speech servers (llama, Vosk, Whisper, Piper):
// freeing memory for the AI is the usual reason to be here. Only names in the
// table are ever matched, so an unknown process is left alone by design.
//
// keepCurrentGame (the in-game menu, the assistant over a game) also spares
// the game on screen: every live screen child, its process group and its
// descendants.
//
// The waiting runs on a goroutine; the result comes back through tbxPost. On
// Linux the page cache is dropped afterwards (sync; drop_caches). Windows
// closes JukaHub's own children only.

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// closeAllChildGrace is what a screen child gets to save and quit before
	// its group is killed: RetroArch writing a save state takes a moment (the
	// in-game menu's Quit waits as long, gmQuitGrace).
	closeAllChildGrace = 6 * time.Second
	// closeAllProcGrace is the same for an untracked process found in /proc.
	closeAllProcGrace = 3 * time.Second
	// closeAllArmWindow is how long the first press of a two-step close stays
	// armed.
	closeAllArmWindow = 5 * time.Second
	// closeAllNoteFor is how long the in-game menu row shows the last result.
	closeAllNoteFor = 20 * time.Second
)

// closeAllRunning is set while a close is under way. Any goroutine.
var closeAllRunning atomic.Bool

// closeAllUI is the two-step arm and the in-game menu's note. Main thread.
var closeAllUI struct {
	armKey string
	armAt  time.Time
	note   string
	noteAt time.Time
}

// closeAllProc is one untracked process the /proc scan picked.
type closeAllProc struct {
	pid   int
	start uint64 // start time in clock ticks: guards against a reused PID
	label string
}

// closeAllPlan is what a close would touch right now, for the confirm text.
type closeAllPlan struct {
	labels []string
}

func (p closeAllPlan) empty() bool { return len(p.labels) == 0 }

// closeAllResult is what one close did.
type closeAllResult struct {
	labels  []string
	keep    bool
	freeMB  int // MemAvailable afterwards (0: unknown, Windows)
	freedMB int
}

// closeAllLabels collects app names once each, in the order met.
type closeAllLabels struct {
	seen map[string]bool
	list []string
}

func (l *closeAllLabels) add(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "App"
	}
	if l.seen == nil {
		l.seen = make(map[string]bool, 4)
	}
	key := strings.ToLower(name)
	if l.seen[key] {
		return
	}
	l.seen[key] = true
	l.list = append(l.list, name)
}

// ── Plan, start, finish ──────────────────────────────────────────────────

// closeAllPlanNow lists what a close would end now. Main thread (the
// Terminal's job and the player phase are read here).
func closeAllPlanNow(keep bool) closeAllPlan {
	var labels closeAllLabels
	live := liveScreenChildren()
	for _, cmd := range live {
		if !keep {
			labels.add(closeAllCmdLabel(cmd))
		}
	}
	if playerPhase() != "idle" {
		labels.add("Video player")
	}
	if closeAllTermBusy() {
		labels.add("Terminal command")
	}
	for _, p := range closeAllScan(live, closeAllKeepsWine(live, keep)) {
		labels.add(p.label)
	}
	return closeAllPlan{labels: labels.list}
}

// closeAllTermBusy reports a Terminal command still running.
func closeAllTermBusy() bool {
	j := term.job
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return !j.stopped && !j.done
}

// closeAllKeepsWine reports that the game kept on screen is a Wine program:
// Wine's server is shared by every program of the prefix and may have left
// the game's process group, so no Wine process is closed beside it.
func closeAllKeepsWine(cmds []*exec.Cmd, keep bool) bool {
	if !keep {
		return false
	}
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		all := strings.ToLower(cmd.Path + " " + strings.Join(cmd.Args, " "))
		if strings.Contains(all, "wine") || strings.Contains(all, "box64") {
			return true
		}
	}
	return false
}

// closeAllStart closes everything now (keep: all but the game on screen).
// Main thread. done (may be nil) runs on the main thread after the result
// toast. false: a close was already running.
func closeAllStart(keep bool, done func(closeAllResult)) bool {
	if !closeAllRunning.CompareAndSwap(false, true) {
		ShowToast("Already closing apps…", ToastKindInfo)
		return false
	}
	closeAllUI.armKey = ""
	before := closeAllMemMB()
	var labels closeAllLabels
	// The parts that live on the main thread go first.
	if playerPhase() != "idle" {
		labels.add("Video player")
	}
	mediaStopForScreenChild() // the player, and the library's ffprobe scan
	if closeAllTermBusy() {
		labels.add("Terminal command")
		termStopJob()
	}
	snapPreviewHalt()
	cancelPrefetchWork()
	var kept []*exec.Cmd
	if keep {
		kept = liveScreenChildren()
		for _, cmd := range gaOverlay.paused {
			if !gaCmdIn(kept, cmd) {
				kept = append(kept, cmd)
			}
		}
	}
	logInfo("closeall", "closing all apps and games (keep the game on screen: %t, %d MB free)", keep, before)
	go func() {
		r := closeAllResult{keep: keep}
		defer func() {
			if rec := recover(); rec != nil {
				logWarn("closeall", "closing apps stopped: %v", rec)
			}
			closeAllRunning.Store(false)
			r.labels = labels.list
			tbxPost(func() { closeAllFinish(r, done) })
		}()
		closeAllRun(kept, &labels)
		closeAllFlushCaches()
		time.Sleep(200 * time.Millisecond)
		r.freeMB = closeAllMemMB()
		if before > 0 && r.freeMB > before {
			r.freedMB = r.freeMB - before
		}
	}()
	return true
}

// closeAllRun stops the screen children and the untracked apps, politely
// first, and waits for them. Off the main thread.
func closeAllRun(kept []*exec.Cmd, labels *closeAllLabels) {
	live := liveScreenChildren()
	var children []*exec.Cmd
	for _, cmd := range live {
		if gaCmdIn(kept, cmd) {
			continue
		}
		children = append(children, cmd)
		labels.add(closeAllCmdLabel(cmd))
	}
	exclude := append(append([]*exec.Cmd(nil), live...), kept...)
	keepWine := closeAllKeepsWine(kept, len(kept) > 0)
	procs := closeAllScan(exclude, keepWine)
	for _, p := range procs {
		labels.add(p.label)
	}
	if len(children) == 0 && len(procs) == 0 {
		return
	}
	for _, cmd := range children {
		// Interrupt is what emulators treat as save-and-quit (to the group,
		// gamesuspend.go stopScreenChildren); a paused one is continued so it
		// acts on it at once.
		_ = screenChildSignal(cmd, screenSignalInterrupt)
		_ = alarmSignalChild(cmd, false)
	}
	closeAllSignalProcs(procs, false)
	start := time.Now()
	deadline := start.Add(closeAllChildGrace)
	procKilled := false
	for time.Now().Before(deadline) {
		childLeft := len(children) > 0 && gmAnyLive(children)
		procLeft := closeAllProcsAlive(procs)
		if !childLeft && !procLeft {
			break
		}
		if procLeft && !procKilled && time.Since(start) >= closeAllProcGrace {
			logInfo("closeall", "untracked apps ignored the quit request; killing them")
			closeAllSignalProcs(procs, true)
			procKilled = true
		}
		if alarmFiringActive() || powerSleepActive() {
			// A ring or a sleep stops the games too: nothing is killed
			// mid-save while they are frozen (gmQuitWatch does the same).
			deadline = time.Now().Add(closeAllChildGrace)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, cmd := range children {
		if gmAnyLive([]*exec.Cmd{cmd}) && cmd.Process != nil {
			logInfo("closeall", "child %d did not quit within %s; killing its group", cmd.Process.Pid, closeAllChildGrace)
			_ = screenChildKill(cmd)
		}
	}
	if closeAllProcsAlive(procs) {
		closeAllSignalProcs(procs, true)
	}
	// A killed process frees its memory as it is reaped: a short wait makes
	// the free figure that follows mean something.
	for i := 0; i < 20 && (gmAnyLive(children) || closeAllProcsAlive(procs)); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if len(children) > 0 {
		closeAllLeftovers(kept, keepWine, labels)
	}
}

// closeAllLeftovers closes what the closed children left running outside
// their process groups (a program that made a group or session of its own):
// the first scan spared everything under a live child, and with the launchers
// gone those are orphans it can see now. Off the main thread.
func closeAllLeftovers(kept []*exec.Cmd, keepWine bool, labels *closeAllLabels) {
	procs := closeAllScan(kept, keepWine)
	if len(procs) == 0 {
		return
	}
	for _, p := range procs {
		labels.add(p.label)
	}
	closeAllSignalProcs(procs, false)
	for deadline := time.Now().Add(closeAllProcGrace); time.Now().Before(deadline) && closeAllProcsAlive(procs); {
		time.Sleep(50 * time.Millisecond)
	}
	if closeAllProcsAlive(procs) {
		closeAllSignalProcs(procs, true)
	}
	for i := 0; i < 20 && closeAllProcsAlive(procs); i++ {
		time.Sleep(50 * time.Millisecond)
	}
}

// closeAllFinish reports a close. Main thread.
func closeAllFinish(r closeAllResult, done func(closeAllResult)) {
	text := r.text()
	logInfo("closeall", "%s (closed: %s; %d MB came back)", text, strings.Join(r.labels, ", "), r.freedMB)
	closeAllUI.note, closeAllUI.noteAt = text, time.Now()
	if len(r.labels) > 0 {
		PlaySuccessSound()
		ShowToast(text, ToastKindSuccess)
	} else {
		ShowToast(text, ToastKindInfo)
	}
	if done != nil {
		done(r)
	}
}

// text is the result in one line: "Closed 3 apps · 412 MB free now".
func (r closeAllResult) text() string {
	mem := ""
	if r.freeMB > 0 {
		mem = fmt.Sprintf(" · %d MB free now", r.freeMB)
	}
	switch len(r.labels) {
	case 0:
		if r.keep {
			return "Nothing else was running" + mem
		}
		return "Nothing to close" + mem
	case 1:
		return "Closed " + r.labels[0] + mem
	}
	return fmt.Sprintf("Closed %d apps", len(r.labels)) + mem
}

// closeAllMemMB is MemAvailable in MB, read now (0 where there is no
// /proc/meminfo).
func closeAllMemMB() int {
	return int(readProcMeminfo().MemAvail / 1024)
}

// ── Entry points ─────────────────────────────────────────────────────────

// closeAllArmPress is the two-step close of a panel row or button (key names
// it): the first press says what would close, the second within
// closeAllArmWindow closes it. button is the key the toast tells to press.
// Main thread.
func closeAllArmPress(key, button string, keep bool, done func(closeAllResult)) {
	if closeAllRunning.Load() {
		ShowToast("Already closing apps…", ToastKindInfo)
		return
	}
	if closeAllUI.armKey == key && time.Since(closeAllUI.armAt) <= closeAllArmWindow {
		closeAllUI.armKey = ""
		PlayActivateSound()
		closeAllStart(keep, done)
		return
	}
	plan := closeAllPlanNow(keep)
	if plan.empty() {
		closeAllUI.armKey = ""
		PlayNavSound()
		ShowToast(closeAllResult{keep: keep, freeMB: closeAllMemMB()}.text(), ToastKindInfo)
		return
	}
	closeAllUI.armKey, closeAllUI.armAt = key, time.Now()
	PlayNavSound()
	ShowToast(closeAllConfirmText(plan, button), ToastKindWarning)
}

// closeAllConfirmText is the first press's question: "Close 3 apps:
// RetroArch, Remote Play, Wine? Press A again — unsaved game progress may be
// lost".
func closeAllConfirmText(plan closeAllPlan, button string) string {
	names := closeAllJoinNames(plan.labels, 3)
	q := "Close " + names + "?"
	if len(plan.labels) > 1 {
		q = fmt.Sprintf("Close %d apps: %s?", len(plan.labels), names)
	}
	return q + " Press " + button + " again — unsaved game progress may be lost"
}

// closeAllJoinNames lists up to limit names: "A, B, C and 2 more". Pure.
func closeAllJoinNames(names []string, limit int) string {
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:limit], ", ") + fmt.Sprintf(" and %d more", len(names)-limit)
}

// closeAllVoiceOutcome is "close all apps", "free up memory" said to the orb
// or the Voice command page (voicecmd_exec.go).
func closeAllVoiceOutcome() voiceOutcome {
	return voiceOutcome{Say: "Closing all apps and games", Act: func(*Config) { closeAllStart(false, nil) }}
}

// closeAllGameCommand takes the same sentences said to the assistant over a
// game: background apps close, the game stays. It reports whether the
// sentence was one (gaAsk asks Juka otherwise). Main thread.
func closeAllGameCommand(question string) bool {
	if voiceParse(question).Kind != voiceIntentCloseAll {
		return false
	}
	if closeAllRunning.Load() {
		gaCommandDone(question, voiceOutcome{Say: "Already closing apps…", Done: true})
		return true
	}
	gaCommandDone(question, voiceOutcome{Say: "Closing background apps — your game stays open", Done: true})
	gen := voiceOrb.gen
	closeAllStart(true, func(r closeAllResult) {
		if gaOverlay.active && voiceOrb.gen == gen && voiceOrb.phase == orbAnswer {
			voiceOrb.title = r.text()
		}
	})
	return true
}

// closeAllMemoryOffer is the fix an "out of memory for offline AI" answer
// offers: orbOfferFreeMemory when something could be closed, else none (no
// point asking to close nothing). Main thread.
func closeAllMemoryOffer() orbOffer {
	if closeAllRunning.Load() || closeAllPlanNow(gameAssistOverlayActive()).empty() {
		return orbOfferNone
	}
	return orbOfferFreeMemory
}

// closeAllOrbRetry is A on "Close apps and try again": apps close (over a
// game only the background ones) while the orb thinks, then question is asked
// again by itself. An empty question (a speech failure) ends with the result
// and the way to speak again. Main thread.
func closeAllOrbRetry(question string) {
	keep := gameAssistOverlayActive()
	voiceOrbCancelRequest()
	voiceOrbSetPhase(orbThinking)
	voiceOrb.title, voiceOrb.sub, voiceOrb.tone, voiceOrb.offer = "Closing apps…", orbQuote(question), tbxToneNeutral, orbOfferNone
	if keep {
		voiceOrb.title = "Closing background apps…"
	}
	if question == "" {
		voiceOrb.sub = ""
	}
	voiceOrb.ai, voiceOrb.hideAt = false, 0
	gen := voiceOrb.gen
	ok := closeAllStart(keep, func(r closeAllResult) {
		if voiceOrb.gen != gen || voiceOrb.phase != orbThinking || !voiceOrbActive() {
			return // given up on meanwhile
		}
		if keep != gameAssistOverlayActive() {
			voiceOrbHideNow() // the overlay came or went meanwhile
			return
		}
		switch {
		case question == "":
			voiceOrbSay(r.text(), "Press "+orbKeyName("Menu")+" and speak again", tbxToneGood, orbOfferNone)
		case keep:
			gaAsk(question)
		default:
			voiceOrbAsk(question, false)
		}
	})
	if !ok {
		voiceOrbSay("Already closing apps…", orbQuote(question), tbxToneNeutral, orbOfferNone)
	}
}

// closeAllFromGameMenu is the in-game menu's "Close background apps": the
// game stays paused under the menu while the rest closes. Main thread.
func closeAllFromGameMenu() {
	closeAllUI.note, closeAllUI.noteAt = "", time.Time{}
	closeAllStart(true, nil)
}

// closeAllMenuSub is that row's smaller line: what it does, the close under
// way, or the last result for a while. Main thread.
func closeAllMenuSub() string {
	switch {
	case closeAllRunning.Load():
		return "Closing background apps…"
	case closeAllUI.note != "" && time.Since(closeAllUI.noteAt) < closeAllNoteFor:
		return closeAllUI.note
	}
	return "Frees memory · this game stays open"
}

// ── Naming ───────────────────────────────────────────────────────────────

// closeAllShells run a script that names the app better than they do.
var closeAllShells = map[string]bool{"sh": true, "bash": true, "ash": true, "dash": true, "busybox": true,
	"cmd": true, "cmd.exe": true, "python": true, "python3": true}

// closeAllCmdLabel names a screen child for people: "RetroArch", "Wine", or
// the script it runs ("Celeste.sh" → "Celeste"; ".../PSP/launch.sh" → "PSP").
func closeAllCmdLabel(cmd *exec.Cmd) string {
	if cmd == nil {
		return "App"
	}
	args := cmd.Args
	if len(args) == 0 {
		args = []string{cmd.Path}
	}
	name := args[0]
	if closeAllShells[strings.ToLower(closeAllBase(name))] {
		for _, a := range args[1:] {
			if a == "" || strings.HasPrefix(a, "-") || closeAllShells[strings.ToLower(closeAllBase(a))] {
				continue
			}
			if f := strings.Fields(a); len(f) > 0 {
				name = f[0]
			}
			break
		}
	}
	// The program and the script only: a ROM's name must not read as one.
	for _, a := range []string{args[0], name} {
		base := closeAllBase(a)
		if IsWindows() {
			// A native program there, not a Wine one (the ".exe" rule).
			base = strings.TrimSuffix(strings.ToLower(base), ".exe")
		}
		if label, ok := closeAllRuleLabel(base); ok {
			return label
		}
	}
	return closeAllNameFromPath(name)
}

// closeAllBase is the last element of a Linux or Windows path. Pure.
func closeAllBase(p string) string {
	p = strings.TrimRight(strings.TrimSpace(p), `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// closeAllGenericNames are script names that say nothing: the folder does.
var closeAllGenericNames = map[string]bool{"launch": true, "launcher": true, "start": true, "run": true,
	"default": true, "go": true, "main": true, "bin": true}

// closeAllNameFromPath is a program's or script's path as a name. Pure.
func closeAllNameFromPath(p string) string {
	p = strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "/")
	for i := 0; i < 3 && p != "" && p != "."; i++ {
		base := closeAllBase(p)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if stem != "" && !closeAllGenericNames[strings.ToLower(stem)] {
			return stem
		}
		j := strings.LastIndex(p, "/")
		if j < 0 {
			break
		}
		p = p[:j]
	}
	return "App"
}

// ── /proc classification (pure; the scan is closeall_linux.go) ───────────

// closeAllRule matches a process name: how '=' the whole name, '^' a prefix,
// '$' a suffix. Names are compared in lower case.
type closeAllRule struct {
	name  string
	how   byte
	label string
}

// closeAllAppRules are the apps and games an untracked process may be. Only
// these (and closeAllPathLabel's folders) are ever closed.
var closeAllAppRules = [...]closeAllRule{
	{"retroarch", '^', "RetroArch"}, // retroarch, retroarch64
	{"ra64.", '^', "RetroArch"},     // ra64.trimui (stock TrimUI, CrossMix)
	{"ra32.", '^', "RetroArch"},
	{"minarch", '^', "MinArch"}, // NextUI / MinUI
	{"ppsspp", '^', "PPSSPP"},   // ppsspp, PPSSPPSDL, ppsspp-tg5040
	{"drastic", '^', "DraStic"},
	{"flycast", '^', "Flycast"},
	{"mupen64plus", '^', "Mupen64Plus"},
	{"yabasanshiro", '^', "Yaba Sanshiro"},
	{"mednafen", '=', "Mednafen"},
	{"scummvm", '=', "ScummVM"},
	{"dosbox", '^', "DOSBox"},
	{"openbor", '^', "OpenBOR"},
	{"pico8", '^', "PICO-8"},
	{"fake-08", '^', "PICO-8"},
	{"love", '=', "LÖVE game"},          // PortMaster's LÖVE ports
	{"gmloader", '^', "GameMaker port"}, // PortMaster's GameMaker runner
	{"gptokeyb", '^', "Port controls"},  // PortMaster's pad-to-keys helper; goes with its port
	{"box64", '=', "Wine"},              // Wine runs under Box64 on arm64
	{"box86", '=', "Wine"},
	{"wine", '^', "Wine"}, // wine, wine64, wineserver, wine64-preloader
	{".exe", '$', "Wine"}, // a Windows program as Box64 names it
	{"java", '=', "Java (Minecraft)"},
	{"classicube", '^', "ClassiCube"},
	{"luanti", '^', "Luanti"},
	{"minetest", '^', "Luanti"},
	{"chiaki", '^', "Remote Play"}, // chiaki, chiaki-stream, chiaki-ng
	{"greenlight", '^', "Xbox Remote Play"},
	{"greenovercast", '^', "Xbox Remote Play"},
	{"moonlight", '^', "Moonlight"},
}

// closeAllNeverRules are never closed, whatever else matches: init, the
// firmware's daemons and shells of record, remote access, and Juka's own
// voice and AI servers (the reason memory is being freed).
var closeAllNeverRules = [...]closeAllRule{
	{"init", '=', ""},
	{"systemd", '^', ""},
	{"juka", '^', ""}, // JukaHub, juka-whisper and every helper of ours
	{"llama", '^', ""},
	{"vosk", '^', ""},
	{"whisper", '^', ""},
	{"piper", '^', ""},
	{"sshd", '=', ""},
	{"dropbear", '^', ""},
	{"ftpd", '$', ""}, // vsftpd, bftpd, pure-ftpd
	{"telnetd", '=', ""},
	{"adbd", '=', ""},
	{"mainui", '=', ""}, // the firmware's own menu
	{"keymon", '^', ""},
	{"trimui_", '^', ""}, // TrimUI's input and Bluetooth daemons
	{"wpa_supplicant", '=', ""},
	{"udhcpc", '=', ""},
	{"dhcpcd", '=', ""},
	{"bluetoothd", '=', ""},
	{"bluealsa", '^', ""},
	{"pulseaudio", '=', ""},
	{"pipewire", '^', ""},
	{"dbus", '^', ""},
	{"syncthing", '=', ""},
}

func closeAllRuleMatch(r closeAllRule, lower string) bool {
	switch r.how {
	case '^':
		return strings.HasPrefix(lower, r.name)
	case '$':
		return strings.HasSuffix(lower, r.name) && len(lower) > len(r.name)
	}
	return lower == r.name
}

// closeAllRuleLabel is the app a process name belongs to. Pure.
func closeAllRuleLabel(name string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return "", false
	}
	for _, r := range closeAllAppRules {
		if closeAllRuleMatch(r, lower) {
			return r.label, true
		}
	}
	return "", false
}

func closeAllNever(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	for _, r := range closeAllNeverRules {
		if closeAllRuleMatch(r, lower) {
			return true
		}
	}
	return false
}

// closeAllPathLabel names a program by the folder it runs from: anything
// under an Emus folder is an emulator, under a ports folder a PortMaster
// port, under androidrt an Android game. Pure.
func closeAllPathLabel(p string) (string, bool) {
	p = strings.ReplaceAll(p, `\`, "/")
	lower := strings.ToLower(p)
	if len(lower) != len(p) {
		p = lower // a name whose case changes its length: index the lowered one
	}
	switch {
	case strings.Contains(lower, "/androidrt/"):
		return "Android game", true
	case strings.Contains(lower, "/emus/"):
		rest := p[strings.Index(lower, "/emus/")+len("/emus/"):]
		if i := strings.IndexByte(rest, '/'); i > 0 {
			return rest[:i] + " emulator", true
		}
		return "Emulator", true
	case strings.Contains(lower, "/ports/"):
		return closeAllNameFromPath(p), true
	}
	return "", false
}

// closeAllClassify decides whether one process is an app to close and names
// it: comm is /proc/PID/stat's name, argv its command line, exe its program
// (may be ""), ownDir JukaHub's folder. Pure.
func closeAllClassify(comm string, argv []string, exe, ownDir string) (string, bool) {
	if len(argv) == 0 {
		return "", false // a kernel thread, or a process on its way out
	}
	names := []string{comm, closeAllBase(argv[0])}
	if exe != "" {
		names = append(names, closeAllBase(exe))
	}
	for _, n := range names {
		if closeAllNever(n) {
			return "", false
		}
	}
	for _, n := range names {
		if label, ok := closeAllRuleLabel(n); ok {
			return label, true
		}
	}
	// By folder: never one of JukaHub's own (its helpers live beside it).
	if ownDir != "" && exe != "" && strings.HasPrefix(exe, strings.TrimRight(ownDir, "/")+"/") {
		return "", false
	}
	paths := []string{exe, argv[0]}
	if closeAllShells[strings.ToLower(closeAllBase(argv[0]))] && len(argv) > 1 {
		paths = append(paths, argv[1])
	}
	for _, p := range paths {
		if p == "" || (ownDir != "" && strings.HasPrefix(p, strings.TrimRight(ownDir, "/")+"/")) {
			continue
		}
		if label, ok := closeAllPathLabel(p); ok {
			return label, true
		}
	}
	return "", false
}

// closeAllStat is the part of /proc/PID/stat the scan reads.
type closeAllStat struct {
	pid   int
	ppid  int
	pgid  int
	state byte
	comm  string
	flags uint64
	start uint64
}

// closeAllKThread is PF_KTHREAD in /proc/PID/stat's flags.
const closeAllKThread = 0x00200000

// kernel reports a kernel thread: never a process to signal.
func (s closeAllStat) kernel() bool {
	return s.flags&closeAllKThread != 0 || s.pid == 2 || s.ppid == 2
}

// closeAllParseStat reads one /proc/PID/stat line. The name sits in
// parentheses and may hold spaces or parentheses itself, so the fields are
// counted from the last ')'. Pure.
func closeAllParseStat(data []byte) (closeAllStat, bool) {
	s := string(data)
	lp := strings.IndexByte(s, '(')
	rp := strings.LastIndexByte(s, ')')
	if lp <= 0 || rp < lp {
		return closeAllStat{}, false
	}
	var st closeAllStat
	pid, err := strconv.Atoi(strings.TrimSpace(s[:lp]))
	if err != nil || pid <= 0 {
		return closeAllStat{}, false
	}
	st.pid, st.comm = pid, s[lp+1:rp]
	// After the name: state(3) ppid(4) pgrp(5) session tty tpgid flags(9) …
	// starttime(22).
	f := strings.Fields(s[rp+1:])
	if len(f) < 20 || len(f[0]) != 1 {
		return closeAllStat{}, false
	}
	st.state = f[0][0]
	var e1, e2, e3, e4 error
	st.ppid, e1 = strconv.Atoi(f[1])
	st.pgid, e2 = strconv.Atoi(f[2])
	st.flags, e3 = strconv.ParseUint(f[6], 10, 64)
	st.start, e4 = strconv.ParseUint(f[19], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return closeAllStat{}, false
	}
	return st, true
}

// closeAllSplitCmdline splits /proc/PID/cmdline (NUL-separated). Pure.
func closeAllSplitCmdline(data []byte) []string {
	s := strings.TrimRight(string(data), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// closeAllPick is the scan's decision over a whole process table: stats by
// PID, a cmdline and exe reader, the PIDs to spare with everything under them
// and in their groups (roots), and the PIDs never touched (JukaHub and its
// ancestors). Pure given its readers.
func closeAllPick(all map[int]closeAllStat, roots, never map[int]bool, ownDir string, keepWine bool,
	read func(pid int) ([]string, string)) []closeAllProc {
	var out []closeAllProc
	for pid, st := range all {
		if pid <= 2 || never[pid] || st.kernel() || st.state == 'Z' || st.state == 'X' {
			continue
		}
		if roots[st.pgid] || closeAllUnder(all, pid, roots) {
			continue
		}
		argv, exe := read(pid)
		label, ok := closeAllClassify(st.comm, argv, exe, ownDir)
		if !ok || (keepWine && label == "Wine") {
			continue
		}
		out = append(out, closeAllProc{pid: pid, start: st.start, label: label})
	}
	// Lowest PID first: a launcher before what it started, and a stable order
	// for the names the toast lists.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].pid < out[j-1].pid; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// closeAllUnder reports whether pid descends from one of roots (or is one).
func closeAllUnder(all map[int]closeAllStat, pid int, roots map[int]bool) bool {
	for i := 0; i < 64 && pid > 1; i++ {
		if roots[pid] {
			return true
		}
		st, ok := all[pid]
		if !ok {
			return false
		}
		pid = st.ppid
	}
	return false
}
