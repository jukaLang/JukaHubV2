package main

// gamemenu_cheats.go — Cheats in the in-game menu (gamemenu.go): the game's
// cheats with on/off switches, changed while the game is paused.
//
// The cheats themselves come from the game options sheet (cheats.go,
// cheats_ui.go): the switched-on ones of the chosen libretro database file
// are written, for the launch, as the game-specific cheat file RetroArch
// loads by itself. The row shows only when that happened for the RetroArch
// game on screen — the file is JukaHub's (marker line, and the same cheats in
// the same order) and the in-game menu's command port is on
// (gmStatesAvailable). A user's own cheat file, a NextUI pak (minarch) or a
// game with no cheat switched on has no row: what is loaded there is not a
// list JukaHub knows the order of.
//
// Switching happens over RetroArch's network commands (libretro's "Network
// Control Interface"; command.h maps them onto the cheat hotkeys):
//
//	CHEAT_INDEX_PLUS / CHEAT_INDEX_MINUS   move the cheat manager's selection
//	CHEAT_TOGGLE                           switch the selected cheat
//
// RetroArch's cheat list is the file's, in its order, with the selection on
// the first cheat at start, so JukaHub keeps track of where it moved the
// selection (the shorter way round; the list wraps) and toggles each cheat
// whose switch changed. A paused (stopped) RetroArch reads no commands, so,
// as with Save state and Load state, the switches are sent once the game runs
// again: B on a changed list gives the game back and sends them once
// RetroArch says it plays (its own pause, gamepause_ra.go, is lifted a moment
// after the continue), and only while nothing pauses the game again — the menu
// reopened, an alarm, a sleep leave the rest for the next start. Each command
// is its own datagram, a few frames apart, since RetroArch acts on a cheat
// hotkey when it goes down. RetroArch shows its own message for each switch
// ("Cheat #3 [ON]: …") and, with apply_cheats_after_toggle (set by the
// launch), applies it at once.
//
// The choice is also kept for the next start, through cheats.go's own record
// (cheatsRecordFor / cheatsSetRecord / cheatsFlush), exactly as if it had
// been switched in the options sheet.
//
// What cannot be known: cheats switched or the selection moved in
// RetroArch's own Quick Menu or with its cheat hotkeys during the game. When
// a command could not be sent the selection is lost, so later changes in this
// game only apply from its next start, and the list says so; a list too long
// to walk quickly (gmCheatsMaxSteps) is treated the same way.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// gmCheatsMaxSteps bounds the selection moves one apply sends.
	gmCheatsMaxSteps = 400
	// gmCheatsStepGap spaces the commands: a cheat hotkey acts when it goes
	// down, so two commands must not land in the same frame.
	gmCheatsStepGap = 80 * time.Millisecond
	// gmCheatsMaxRows is how many cheats a page of the list shows at most.
	gmCheatsMaxRows = 8
)

// gmCheatItem is one cheat of RetroArch's list.
type gmCheatItem struct {
	Label string
	Key   string // cheats.go's record key: description and code
	On    bool
}

// gmCheatsLaunch is the cheat list of the RetroArch game the library last
// started, in RetroArch's order, and where JukaHub left RetroArch's
// selection. Written by the launch's loader goroutine and the send goroutine,
// read by the menu.
var gmCheatsLaunch struct {
	mu     sync.Mutex
	gen    int
	rom    string
	source string
	entry  string
	items  []gmCheatItem
	ptr    int
	lost   bool // the selection is no longer known: changes wait for the next start
}

// gmCheatsUI is the open list. Main thread only.
var gmCheatsUI struct {
	open     bool
	openedAt time.Time
	rom      string
	port     int
	sel      int
	page     int // rows the last draw fitted
	items    []gmCheatItem
	was      []bool
	lost     bool
}

// gmCheatsWalk is held by the goroutine walking RetroArch's selection
// (gmCheatsSend), so two walks never interleave their commands.
var gmCheatsWalk sync.Mutex

// gmCheatsGameHeld reports the game paused, or about to be: the in-game
// overlay is up or the pad watcher has just taken the pad for it, an alarm
// rings or the handheld sleeps. release is read before held, because a
// hand-back clears held first and release after. Any goroutine.
func gmCheatsGameHeld() bool {
	if gaLink.live.Load() || alarmFiringActive() || powerSleepActive() {
		return true
	}
	if gaLink.release.Load() {
		return false // the pad is on its way back to the game
	}
	return gaLink.held.Load()
}

// gmCheatsWaitPlaying waits, after the hand-back, until RetroArch says it
// plays (GET_STATUS) and keeps playing for raCoopSettle, as gamepause_ra.go's
// unpause does. A build that never answers GET_STATUS was never paused by
// JukaHub (raCoopPause needs the answer), so two silences in a row count as
// playing. false: it did not play within the unpause's own bound, or the game
// was paused again or is gone.
func gmCheatsWaitPlaying(port int) bool {
	deadline := time.Now().Add(raCoopResumeFor + time.Second)
	silent := 0
	var playingSince time.Time
	for time.Now().Before(deadline) {
		if gmCheatsGameHeld() || !retroArchRunning() {
			return false
		}
		reply, err := askRetroArchNetCmd(port, "GET_STATUS", raCoopAskWait)
		switch {
		case err != nil && gmNetTimeout(err):
			silent++
			if silent >= 2 {
				return true
			}
			playingSince = time.Time{}
		case err != nil:
			return false
		case raStatusWord(reply) == "PLAYING":
			silent = 0
			if playingSince.IsZero() {
				playingSince = time.Now()
			} else if time.Since(playingSince) >= raCoopSettle {
				return true
			}
		default:
			silent = 0
			playingSince = time.Time{}
		}
		time.Sleep(raCoopPoll)
	}
	return false
}

// gmCheatsNoteLaunch forgets the last game's list and, for a RetroArch game
// with cheats switched on, reads the list prepareCheats wrote (on a
// goroutine: the database may be a zip). Called from prepareGameControls,
// after prepareCheats.
func gmCheatsNoteLaunch(launcher string, game libraryGame) {
	gmCheatsLaunch.mu.Lock()
	gmCheatsLaunch.gen++
	gen := gmCheatsLaunch.gen
	gmCheatsLaunch.rom, gmCheatsLaunch.source, gmCheatsLaunch.entry = "", "", ""
	gmCheatsLaunch.items, gmCheatsLaunch.ptr, gmCheatsLaunch.lost = nil, 0, false
	gmCheatsLaunch.mu.Unlock()
	rec, ok := cheatsRecordFor(game.Path)
	if !ok || len(rec.On) == 0 || !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return
	}
	go gmCheatsLoad(gen, launcher, game, rec)
}

// gmCheatsLoad builds the list when the game-specific file every config of
// the launch points at is the one JukaHub wrote from rec.
func gmCheatsLoad(gen int, launcher string, game libraryGame, rec cheatsGameRecord) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("gamemenu", "reading the game's cheats failed: %v", r)
		}
	}()
	stem := gameResetStem(game.Path)
	folder := cheatsCoreFolder(launcher, game.System)
	if stem == "" || folder == "" {
		return
	}
	data, err := cheatsReadRef(cheatRef{Source: rec.Source, Entry: rec.Entry})
	if err != nil {
		logDebug("gamemenu", "no in-game cheat list: %v", err)
		return
	}
	cheats := parseCht(data)
	if cheatsApplyRecord(cheats, rec.On) == 0 {
		return
	}
	fp := cheatsFingerprint(cheats)
	ours := 0
	for _, cfg := range raScopeTargets(raLaunchCandidates(launcher, retroArchConfigCandidates())) {
		raw, err := os.ReadFile(cfg)
		if err != nil {
			continue
		}
		dir, _ := cheatsLaunchDir(string(raw), cfg)
		path := filepath.Join(dir, folder, stem+".cht")
		file, err := cheatsReadSmall(path)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(strings.TrimPrefix(string(file), "\ufeff"), cheatsFileMarker) ||
			cheatsFingerprint(parseCht(file)) != fp {
			logInfo("gamemenu", "%s is not the cheat file JukaHub wrote; the in-game menu leaves its cheats to RetroArch", path)
			return
		}
		ours++
	}
	if ours == 0 {
		return
	}
	items := make([]gmCheatItem, len(cheats))
	for i, c := range cheats {
		items[i] = gmCheatItem{Label: c.label(i), Key: c.key(), On: c.Enable}
	}
	gmCheatsLaunch.mu.Lock()
	if gmCheatsLaunch.gen == gen {
		gmCheatsLaunch.rom, gmCheatsLaunch.source, gmCheatsLaunch.entry = game.Path, rec.Source, rec.Entry
		gmCheatsLaunch.items, gmCheatsLaunch.ptr, gmCheatsLaunch.lost = items, 0, false
	}
	gmCheatsLaunch.mu.Unlock()
}

// gmCheatsAvailable reports whether the paused game has a cheat list the menu
// can switch (the row shows).
func gmCheatsAvailable() bool {
	// A cheat switched on would end hardcore RetroAchievements: the row
	// waits for a game without it (raach_unlocks.go).
	if !gmStatesAvailable() || raachHardcoreRunning() {
		return false
	}
	gmCheatsLaunch.mu.Lock()
	defer gmCheatsLaunch.mu.Unlock()
	return gmCheatsLaunch.rom != "" && gmCheatsLaunch.rom == gmPick.rom && len(gmCheatsLaunch.items) > 0
}

// gmCheatsSub is the menu row's smaller line.
func gmCheatsSub() string {
	gmCheatsLaunch.mu.Lock()
	defer gmCheatsLaunch.mu.Unlock()
	on := 0
	for _, it := range gmCheatsLaunch.items {
		if it.On {
			on++
		}
	}
	return strconv.Itoa(on) + " of " + strconv.Itoa(len(gmCheatsLaunch.items)) + " on · switch them for this game"
}

// gmCheatsActive reports the list open over the menu of this opening of the
// overlay; one left from an overlay that closed some other way is gone.
func gmCheatsActive() bool {
	if gmCheatsUI.open && (!gaOverlay.active || !gmCheatsUI.openedAt.Equal(gaOverlay.openedAt)) {
		gmCheatsUI.open = false
		gmCheatsUI.items, gmCheatsUI.was = nil, nil
	}
	return gmCheatsUI.open
}

// gmCheatsOpen shows the list.
func gmCheatsOpen() {
	gmCheatsLaunch.mu.Lock()
	items := append([]gmCheatItem(nil), gmCheatsLaunch.items...)
	rom, lost := gmCheatsLaunch.rom, gmCheatsLaunch.lost
	gmCheatsLaunch.mu.Unlock()
	if len(items) == 0 {
		PlayErrorSound()
		return
	}
	PlayActivateSound()
	gmCheatsUI.open, gmCheatsUI.openedAt = true, gaOverlay.openedAt
	gmCheatsUI.rom, gmCheatsUI.port, gmCheatsUI.lost = rom, gmPick.port, lost
	gmCheatsUI.sel, gmCheatsUI.items = 0, items
	gmCheatsUI.was = make([]bool, len(items))
	for i, it := range items {
		gmCheatsUI.was[i] = it.On
	}
}

// gmCheatsChanged lists the cheats whose switch differs from when the list
// opened, in RetroArch's order.
func gmCheatsChanged() []int {
	var out []int
	for i, it := range gmCheatsUI.items {
		if i < len(gmCheatsUI.was) && it.On != gmCheatsUI.was[i] {
			out = append(out, i)
		}
	}
	return out
}

// gmCheatsPress acts on one press while the list is up.
func gmCheatsPress(b gaButton) {
	n := len(gmCheatsUI.items)
	if n == 0 {
		gmCheatsUI.open = false
		return
	}
	page := gmCheatsUI.page
	if page < 1 {
		page = 1
	}
	switch b {
	case gaBtnUp:
		gmCheatsUI.sel = (gmCheatsUI.sel + n - 1) % n
		PlayNavSound()
	case gaBtnDown:
		gmCheatsUI.sel = (gmCheatsUI.sel + 1) % n
		PlayNavSound()
	case gaBtnLeft, gaBtnRight:
		step := page
		if b == gaBtnLeft {
			step = -page
		}
		next := gmCheatsUI.sel + step
		if next < 0 {
			next = 0
		}
		if next >= n {
			next = n - 1
		}
		if next != gmCheatsUI.sel {
			gmCheatsUI.sel = next
			PlayNavSound()
		}
	case gaBtnA:
		gmCheatsUI.items[gmCheatsUI.sel].On = !gmCheatsUI.items[gmCheatsUI.sel].On
		PlayNavSound()
	case gaBtnB:
		if len(gmCheatsChanged()) > 0 {
			gmCheatsApply()
			return
		}
		PlayBackSound()
		gmCheatsUI.open = false
	case gaBtnMenu, gaBtnMenuChord:
		if len(gmCheatsChanged()) > 0 {
			gmCheatsApply()
			return
		}
		PlayBackSound()
		gmCheatsUI.open = false
		gaClose("the player went back", true)
	}
}

// gmCheatsApply keeps the switches for the next start, gives the game back
// and has RetroArch switch the changed cheats.
func gmCheatsApply() {
	changed := gmCheatsChanged()
	rom, port := gmCheatsUI.rom, gmCheatsUI.port
	items := append([]gmCheatItem(nil), gmCheatsUI.items...)
	gmCheatsUI.open = false
	gmCheatsUI.items, gmCheatsUI.was = nil, nil

	gmCheatsLaunch.mu.Lock()
	same := gmCheatsLaunch.rom == rom
	source, entry := gmCheatsLaunch.source, gmCheatsLaunch.entry
	if same {
		gmCheatsLaunch.items = items
	}
	gmCheatsLaunch.mu.Unlock()
	if !same {
		PlayErrorSound()
		return
	}

	// The record, as the options sheet would leave it.
	rec, ok := cheatsRecordFor(rom)
	if !ok {
		rec = cheatsGameRecord{Source: source, Entry: entry}
	}
	on := map[string]bool{}
	for _, k := range rec.On {
		on[k] = true
	}
	for _, i := range changed {
		on[items[i].Key] = items[i].On
	}
	rec.On = rec.On[:0:0]
	for _, it := range items {
		if on[it.Key] {
			rec.On = append(rec.On, it.Key)
			delete(on, it.Key)
		}
	}
	for k, v := range on {
		if v {
			rec.On = append(rec.On, k) // a key the list does not show (another file's)
		}
	}
	cheatsSetRecord(rom, rec)

	PlayActivateSound()
	gaClose("the player switched cheats", true)
	go gmCheatsSend(rom, port, changed, len(items))
}

// gmCheatsSend saves the record, then walks RetroArch's selection to each
// changed cheat and toggles it. Its own goroutine.
func gmCheatsSend(rom string, port int, changed []int, n int) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("gamemenu", "switching cheats failed: %v", r)
		}
	}()
	// The record first: the walk can take half a minute, and the choice is
	// kept for the next start whatever happens to it.
	cheatsFlush()
	time.Sleep(gmStateResumeDelay)
	// One walk at a time: a list applied again while the last walk is still
	// sending starts from where that one left RetroArch's selection.
	gmCheatsWalk.Lock()
	defer gmCheatsWalk.Unlock()
	gmCheatsLaunch.mu.Lock()
	same, ptr, lost := gmCheatsLaunch.rom == rom, gmCheatsLaunch.ptr, gmCheatsLaunch.lost
	gen := gmCheatsLaunch.gen
	gmCheatsLaunch.mu.Unlock()
	name := gameDisplayTitle(rom)
	if !same || n <= 0 || port <= 0 {
		return
	}
	if lost {
		logInfo("gamemenu", "%s: cheat switches kept for the next start (RetroArch's selection is not known)", name)
		return
	}
	// Plan the walk first: a list too long to walk quickly waits for the next
	// start instead of sending hundreds of commands.
	type move struct {
		cmd   string
		steps int
	}
	plan := make([]move, 0, len(changed))
	at, total := ptr, 0
	for _, idx := range changed {
		fwd, back := (idx-at+n)%n, (at-idx+n)%n
		m := move{"CHEAT_INDEX_PLUS", fwd}
		if back < fwd {
			m = move{"CHEAT_INDEX_MINUS", back}
		}
		plan = append(plan, m)
		total += m.steps + 1
		at = idx
	}
	setPtr := func(p int, lostNow bool) {
		gmCheatsLaunch.mu.Lock()
		if gmCheatsLaunch.rom == rom && gmCheatsLaunch.gen == gen {
			gmCheatsLaunch.ptr = p
			gmCheatsLaunch.lost = gmCheatsLaunch.lost || lostNow
		}
		gmCheatsLaunch.mu.Unlock()
	}
	if total > gmCheatsMaxSteps {
		logInfo("gamemenu", "%s: %d cheat commands are too many to send; the switches apply from the next start", name, total)
		setPtr(ptr, true)
		return
	}
	// RetroArch reads no cheat hotkey while paused, and its own pause is lifted
	// by gamepause_ra.go's goroutine a moment after the hand-back: the walk
	// starts once it plays.
	if !gmCheatsWaitPlaying(port) {
		logInfo("gamemenu", "%s: RetroArch did not carry on playing; the cheat switches apply from the next start", name)
		setPtr(ptr, true)
		return
	}
	// ready is asked before every command. A game paused again (the menu
	// reopened, an alarm, a sleep) would drop or merge the commands — two
	// that wait under a stop arrive in one frame and count once — so the rest
	// waits for the next start.
	ready := func(from int) bool {
		gmCheatsLaunch.mu.Lock()
		relaunched := gmCheatsLaunch.gen != gen
		gmCheatsLaunch.mu.Unlock()
		if relaunched || !retroArchRunning() {
			return false // the game is gone; a new launch has its own list
		}
		if gmCheatsGameHeld() {
			logInfo("gamemenu", "%s: the game was paused again; the other cheat switches apply from the next start", name)
			setPtr(from, true)
			return false
		}
		return true
	}
	at = ptr
	for k, idx := range changed {
		for s := 0; s < plan[k].steps; s++ {
			if !ready(at) {
				return
			}
			if err := sendRetroArchNetCmd(port, plan[k].cmd); err != nil {
				logWarn("gamemenu", "%s: could not move RetroArch's cheat selection: %v", name, err)
				setPtr(at, true)
				return
			}
			time.Sleep(gmCheatsStepGap)
		}
		if !ready(at) {
			return
		}
		if err := sendRetroArchNetCmd(port, "CHEAT_TOGGLE"); err != nil {
			logWarn("gamemenu", "%s: could not switch cheat %d: %v", name, idx+1, err)
			setPtr(idx, true)
			return
		}
		at = idx
		setPtr(at, false)
		time.Sleep(gmCheatsStepGap)
	}
	logInfo("gamemenu", "%s: %d cheat(s) switched in RetroArch", name, len(changed))
}

// ── Drawing ──────────────────────────────────────────────────────────────

// gmCheatsDraw is the list card: how many are on, the game, a page of cheats
// with their switches, and the buttons that work it.
func gmCheatsDraw(renderer *sdl.Renderer, fonts tbxFonts) {
	pad := orbPad
	rowGap := int32(4)
	n := len(gmCheatsUI.items)
	on := 0
	for _, it := range gmCheatsUI.items {
		if it.On {
			on++
		}
	}
	kicker := "CHEATS · " + strconv.Itoa(on) + " ON"
	title := gaOverlay.title
	if title == "" {
		title = "Your game"
	}
	done := "back"
	if len(gmCheatsChanged()) > 0 {
		done = "play with these"
	}
	hint := "D-pad move · " + orbKeyName("A") + " switch · " + orbKeyName("B") + " " + done
	note := ""
	if gmCheatsUI.lost {
		note = "Changes apply from the game's next start"
	}

	w := screenWidth * 50 / 100
	if minW := scaleX(480, screenWidth); w < minW {
		w = minW
	}
	if w > screenWidth-4*pad {
		w = screenWidth - 4*pad
	}
	inner := w - 2*pad
	rowH := fonts.bodyH + pad
	head := pad + fonts.tinyH + 2 + fonts.bodyH + pad
	foot := pad/2 + fonts.smallH + pad
	if note != "" {
		foot += 2 + fonts.smallH
	}
	rows := int((screenHeight - 4*pad - head - foot) / (rowH + rowGap))
	if rows > gmCheatsMaxRows {
		rows = gmCheatsMaxRows
	}
	if rows < 1 {
		rows = 1
	}
	if rows > n {
		rows = n
	}
	gmCheatsUI.page = rows
	h := head + int32(rows)*(rowH+rowGap) + foot

	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	fill := glassSheetFill()
	fill.A = 240
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, fill)
	strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, 1, glassSheetEdge())

	x, y := card.X+pad, card.Y+pad
	if rows < n {
		kicker += " · " + strconv.Itoa(gmCheatsUI.sel+1) + " OF " + strconv.Itoa(n)
	}
	drawText(renderer, fonts.tiny, kicker, x, y, glassAccent(), textAlignLeft)
	y += fonts.tinyH + 2
	drawText(renderer, fonts.body, ellipsize(fonts.body, title, inner), x, y, ColorTextPrimary(), textAlignLeft)
	y += fonts.bodyH + pad

	accent := glassAccent()
	first := 0
	if rows > 0 {
		first = gmCheatsUI.sel / rows * rows
	}
	stateW := int32(0)
	for _, s := range []string{"On", "Off"} {
		if tw, _, err := fonts.body.SizeUTF8(s); err == nil && int32(tw) > stateW {
			stateW = int32(tw)
		}
	}
	for i := first; i < first+rows && i < n; i++ {
		it := gmCheatsUI.items[i]
		row := sdl.Rect{X: card.X + pad/2, Y: y, W: card.W - pad, H: rowH}
		if i == gmCheatsUI.sel {
			f := accent
			f.A = 56
			fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, orbBubbleRadius-4, f)
			strokeRoundedRect(renderer, row.X, row.Y, row.W, row.H, orbBubbleRadius-4, 2, accent)
		}
		ty := row.Y + (row.H-fonts.bodyH)/2
		label := ellipsize(fonts.body, it.Label, row.W-3*pad-stateW)
		drawText(renderer, fonts.body, label, row.X+pad, ty, ColorTextPrimary(), textAlignLeft)
		state, ink := "Off", ColorTextTertiary()
		if it.On {
			state, ink = "On", accent
		}
		drawText(renderer, fonts.body, state, row.X+row.W-pad, ty, ink, textAlignRight)
		y += rowH + rowGap
	}
	y += pad / 2
	if note != "" {
		drawText(renderer, fonts.small, ellipsize(fonts.small, note, inner), card.X+card.W/2, y, ColorTextSecondary(), textAlignCenter)
		y += fonts.smallH + 2
	}
	drawText(renderer, fonts.small, ellipsize(fonts.small, hint, inner), card.X+card.W/2, y, ColorTextSecondary(), textAlignCenter)
}
