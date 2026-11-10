package main

// inmap_runtime.go — the engine that runs the active control profile.
//
// It sits at the top of handleControllerEvent (after the ringing alarm and
// the enlarged QR code) and sees every press before any panel or screen. A
// press either goes through untouched, is rewritten into a fresh synthesized
// event (a button or a key, queued through SDL with its own marker so nothing
// remaps it twice), runs a named action, or is held back while the engine
// waits to see whether it becomes a hold, a double tap or the first half of a
// chord. Waiting is decided by timestamps checked once a frame (inmapTick):
// no goroutine, nothing blocks. Releases arrive through inmapButtonUp, which
// main.go calls for every release before anything can drop one, and the
// engine never keeps an SDL event pointer: it copies what it needs.
//
// The switch hotkey — hold Select, press R1 — is a chord the engine keeps on
// every profile. Select's own press waits for its release (Back fires on the
// way up instead of on the way down) so R1 can join it; it only does so while
// a pad is attached and the hotkey is on.
//
// Profiles stand aside while the on-screen keyboard is up and on the Control
// profiles and Local play screens, so a profile that broke a button can always
// be fixed. A profile scoped to some screens also stands aside while a panel
// or menu floats over them; a profile for every screen keeps working there.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// inmapSynthWhich marks the presses the engine queues itself. They are
	// already what the profile wanted, so neither the accessibility remap
	// (main.go) nor the engine touches them again.
	inmapSynthWhich sdl.JoystickID = -3
	// inmapDoubleWindowMs is how long a first tap waits for its second.
	inmapDoubleWindowMs = 300
	// inmapModifierTapMs is how long a chord's first button may stay down and
	// still count as a tap of its own when no chord came of it.
	inmapModifierTapMs = 900
	// inmapPhysicalGraceMs is how long a press is trusted before the engine
	// asks the pad itself whether the button is still down.
	inmapPhysicalGraceMs = 250
	// inmapStuckMs drops a wait that has outlived any real gesture.
	inmapStuckMs = 10000
)

// inmapMode is what the engine made of one press.
type inmapMode uint8

const (
	inmapIdle inmapMode = iota
	// inmapPassed went through unchanged.
	inmapPassed
	// inmapRemapped became another button or key, released with it.
	inmapRemapped
	// inmapSwallowed ran something; its release is dropped.
	inmapSwallowed
	// inmapPending waits for a hold, a second tap or the rest of a chord.
	inmapPending
)

// inmapBtnState is the engine's view of one button. A waiting press keeps
// copies of the targets it may still run, so a profile switched meanwhile
// cannot make it run another profile's line.
type inmapBtnState struct {
	held      bool
	mode      inmapMode
	which     sdl.JoystickID
	downAt    uint64
	upAt      uint64
	taps      int
	holdFired bool
	chordUsed bool
	modifier  bool
	hasPlain  bool
	hasHold   bool
	hasDouble bool
	holdMs    int
	plain     inmapTarget
	hold      inmapTarget
	double    inmapTarget
	remap     inmapTarget
}

// inmapSlot is a button's line in a compiled table: the one for this screen
// and the one for every screen (-1 when none).
type inmapSlot struct {
	scene  int16
	global int16
}

// pick chooses the line that applies: a screen's own line wins, unless a
// panel floats over the screen.
func (s inmapSlot) pick(overlay bool) int16 {
	if !overlay && s.scene >= 0 {
		return s.scene
	}
	return s.global
}

// inmapChordRef is one chord line of a compiled table.
type inmapChordRef struct {
	binding int16
	scoped  bool
}

// inmapTable is a profile compiled for one screen.
type inmapTable struct {
	profile   *inmapProfile
	scene     string
	plain     [inmapButtonCount]inmapSlot
	hold      [inmapButtonCount]inmapSlot
	double    [inmapButtonCount]inmapSlot
	chords    []inmapChordRef
	modScene  [inmapButtonCount]bool
	modGlobal [inmapButtonCount]bool
}

// inmapEngine is the engine's whole state; main thread only.
var inmapEngine struct {
	btn       [inmapButtonCount]inmapBtnState
	table     inmapTable
	tableFor  *inmapProfile
	tableGen  int
	tableOK   bool
	lastScene string
}

// Test seams: a fake clock, recorders for what the engine queues, and stand-
// ins for the pad. The app runs with all of them nil.
var (
	inmapClockHook    func() uint64
	inmapEmitHook     func(b inmapButton, down bool)
	inmapKeyHook      func(key sdl.Keycode, down bool)
	inmapPhysicalHook func(b inmapButton, which sdl.JoystickID) bool
	inmapPadHook      func() bool
)

func init() {
	// Hold-to-repeat follows the profile: a direction remapped away stops
	// repeating, and a button turned into a direction repeats like one.
	padRepeatHeldDirection = inmapHeldNavDirection
}

func inmapNow() uint64 {
	if inmapClockHook != nil {
		return inmapClockHook()
	}
	return sdl.GetTicks64()
}

// inmapPadPresent reports whether a pad is attached: the switch hotkey only
// holds Select back when there is a Select to hold.
func inmapPadPresent() bool {
	if inmapPadHook != nil {
		return inmapPadHook()
	}
	return gameController != nil
}

// inmapHotkeyActive reports whether hold Select + R1 switches profiles now.
func inmapHotkeyActive() bool {
	return inmapPadPresent() && inmapHotkeyEnabled()
}

// inmapResetEngine forgets every press and the compiled table.
func inmapResetEngine() {
	inmapEngine.btn = [inmapButtonCount]inmapBtnState{}
	inmapEngine.table = inmapTable{}
	inmapEngine.tableFor = nil
	inmapEngine.tableOK = false
	inmapEngine.lastScene = ""
}

// ── Synthesized events ───────────────────────────────────────────────────

// inmapEmitButton queues one press or release of b as a fresh event. The A/B
// and X/Y swaps applyButtonSwap applies to every polled event are undone first
// (padFaceButton is its own inverse), so the screen sees the button the profile
// named.
func inmapEmitButton(b inmapButton, down bool) {
	if inmapEmitHook != nil {
		inmapEmitHook(b, down)
		return
	}
	if b >= inmapButtonCount || inmapButtons[b].trigger {
		return
	}
	code := padFaceButton(appConfig, inmapButtons[b].code)
	eventType, state := uint32(sdl.CONTROLLERBUTTONUP), uint8(sdl.RELEASED)
	if down {
		eventType, state = sdl.CONTROLLERBUTTONDOWN, sdl.PRESSED
	}
	if err := pushControllerButton(eventType, inmapSynthWhich, code, state); err != nil {
		LogScene("input").Warn("could not queue a profile press", "button", inmapButtons[b].name, "err", err)
	}
}

// inmapEmitKey queues one keyboard press or release. A queued key is a key,
// not text: it never types a character into a field.
func inmapEmitKey(key sdl.Keycode, down bool) {
	if inmapKeyHook != nil {
		inmapKeyHook(key, down)
		return
	}
	eventType, state := uint32(sdl.KEYUP), uint8(sdl.RELEASED)
	if down {
		eventType, state = sdl.KEYDOWN, sdl.PRESSED
	}
	if err := pushSyntheticKey(eventType, key, state); err != nil {
		LogScene("input").Warn("could not queue a profile key", "key", int(key), "err", err)
	}
}

// inmapTapKey presses and releases a key.
func inmapTapKey(key sdl.Keycode) {
	inmapEmitKey(key, true)
	inmapEmitKey(key, false)
}

// inmapTapButton presses and releases b: a queued pair for a button, the
// shoulders' own handler for a trigger.
func inmapTapButton(config *Config, b inmapButton) {
	if b == inmapBtnL2 || b == inmapBtnR2 {
		inmapShoulder(config, b)
		return
	}
	inmapEmitButton(b, true)
	inmapEmitButton(b, false)
}

// inmapShoulder presses a trigger the way the axis latch in controller.go
// does: unless an overlay owns the triggers, the shoulders' handler runs it.
func inmapShoulder(config *Config, b inmapButton) {
	if inmapEmitHook != nil {
		inmapEmitHook(b, true)
		inmapEmitHook(b, false)
		return
	}
	side := padL2
	if b == inmapBtnR2 {
		side = padR2
	}
	if config == nil || triggerOwnedByOverlay() {
		return
	}
	handleShoulderPress(side, config)
}

// ── When profiles apply ──────────────────────────────────────────────────

// inmapPaused reports whether profiles stand aside for this press: while the
// on-screen keyboard types, and on the two screens where profiles are set up
// and players join.
func inmapPaused(config *Config) bool {
	if config == nil || virtualKeyboardActive {
		return true
	}
	switch inmapCurrentScene(config) {
	case inmapProfilesScene, hotseatScene, stickCalScene:
		return true
	}
	return false
}

// inmapOverlayOpen reports whether a panel or a menu floats over the screen
// and owns the pad. Lines scoped to a screen stand aside while one is up.
func inmapOverlayOpen() bool {
	return tb.startOpen || qs.open || nc.open || controlsLegend.open || shortcutsOpen ||
		clipSearchOpen || globalSearchOpen || wpOpen || homeCalendarOpen ||
		weatherPopoverOpen || podPanel.open || ledPanel.open || companion.open || qrZoom.open ||
		browserCookiePanel.open || browserBookmarkPanel.open || brwInfo.open ||
		themeCommunityPanel.open || modalPanelBlocksShoulders()
}

// inmapSceneMatches reports whether a scene list names the screen, by its
// config name or by the name its header shows ("Media" is Tube).
func inmapSceneMatches(list []string, scene string) bool {
	if scene == "" {
		return false
	}
	display := sceneDisplayName(scene)
	for _, s := range list {
		if s == "*" || strings.EqualFold(s, scene) || strings.EqualFold(s, display) {
			return true
		}
	}
	return false
}

// ── Compiling ────────────────────────────────────────────────────────────

// inmapCompile builds the lookup table of profile p for one screen. Lines
// scoped to other screens are left out; the first line wins a trigger.
func inmapCompile(p *inmapProfile, scene string) inmapTable {
	t := inmapTable{profile: p, scene: scene}
	for i := range t.plain {
		t.plain[i] = inmapSlot{scene: -1, global: -1}
		t.hold[i] = inmapSlot{scene: -1, global: -1}
		t.double[i] = inmapSlot{scene: -1, global: -1}
	}
	if p == nil {
		return t
	}
	for i := range p.bindings {
		b := &p.bindings[i]
		if !b.ok || len(b.trigger.buttons) == 0 {
			continue
		}
		scenes := b.scenes
		if scenes == nil {
			scenes = p.scenes
		}
		scoped := len(scenes) > 0
		if scoped && !inmapSceneMatches(scenes, scene) {
			continue
		}
		idx := int16(i)
		btns := b.trigger.buttons
		if len(btns) > 1 {
			t.chords = append(t.chords, inmapChordRef{binding: idx, scoped: scoped})
			for _, m := range btns[:len(btns)-1] {
				if scoped {
					t.modScene[m] = true
				} else {
					t.modGlobal[m] = true
				}
			}
			continue
		}
		var slot *inmapSlot
		switch b.trigger.kind {
		case inmapPressHold:
			slot = &t.hold[btns[0]]
		case inmapPressDouble:
			slot = &t.double[btns[0]]
		default:
			slot = &t.plain[btns[0]]
		}
		if scoped {
			if slot.scene < 0 {
				slot.scene = idx
			}
		} else if slot.global < 0 {
			slot.global = idx
		}
	}
	return t
}

// inmapSyncTable compiles the active profile for the screen on display when
// the profile, the screen or the registry changed since the last press.
func inmapSyncTable(config *Config) {
	inmapEnsureValid(config)
	p := inmapEffective()
	scene := inmapCurrentScene(config)
	gen := inmapGeneration()
	eng := &inmapEngine
	if eng.tableOK && eng.tableFor == p && eng.table.scene == scene && eng.tableGen == gen {
		return
	}
	eng.table = inmapCompile(p, scene)
	eng.tableFor, eng.tableGen, eng.tableOK = p, gen, true
}

// inmapMatchChord finds the chord b completes — every other button of it is
// held now — and returns its line, or -1. The longest chord wins, so l1+r1+a
// beats r1+a.
func inmapMatchChord(t *inmapTable, b inmapButton, overlay bool) int16 {
	best, bestLen := int16(-1), 0
	for _, ref := range t.chords {
		if ref.scoped && overlay {
			continue
		}
		btns := t.profile.bindings[ref.binding].trigger.buttons
		if btns[len(btns)-1] != b || len(btns) <= bestLen {
			continue
		}
		all := true
		for _, m := range btns[:len(btns)-1] {
			if !inmapEngine.btn[m].held {
				all = false
				break
			}
		}
		if all {
			best, bestLen = ref.binding, len(btns)
		}
	}
	return best
}

// ── Presses ──────────────────────────────────────────────────────────────

// inmapHandleButtonDown is the engine's entry at the top of
// handleControllerEvent. It reports whether the press was consumed.
func inmapHandleButtonDown(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	// The engine's own presses, and the held-direction repeats (already the
	// direction the screen should see), go straight through.
	if e.Which == inmapSynthWhich || e.Which == padRepeatWhich {
		return false
	}
	b, ok := inmapButtonFromSDL(e.Button)
	if !ok {
		return false
	}
	return inmapPress(b, e.Which, config)
}

// inmapTriggerPress is the engine's entry for L2 and R2, whose presses are
// latched from axis motion (controller.go). Their release is polled.
func inmapTriggerPress(b inmapButton, config *Config) bool {
	return inmapPress(b, -1, config)
}

// inmapPress decides what one press does. which is the pad it came from (-1
// for a trigger).
func inmapPress(b inmapButton, which sdl.JoystickID, config *Config) bool {
	if b >= inmapButtonCount || inmapPaused(config) {
		return false
	}
	eng := &inmapEngine
	now := inmapNow()
	inmapSyncTable(config)
	t := &eng.table
	overlay := inmapOverlayOpen()
	st := &eng.btn[b]

	// The second tap of a double runs on its press.
	if st.mode == inmapPending && !st.held && st.taps == 1 && st.hasDouble && now-st.upAt <= inmapDoubleWindowMs {
		tgt := st.double
		*st = inmapBtnState{held: true, mode: inmapSwallowed, which: which, downAt: now}
		inmapRunTap(config, tgt, b)
		return true
	}
	// A lone tap whose window has closed settles before the new press.
	if st.mode == inmapPending && !st.held && st.taps == 1 {
		inmapResolveTap(config, b)
	}
	// A press for a button still marked down lost its release somewhere (a
	// screen that swallowed it, a game that took the screen): start over.
	if st.held {
		inmapForget(b)
	}

	// The switch hotkey: Select held back, R1 pressed.
	if b == inmapBtnR1 && inmapHotkeyActive() {
		sel := &eng.btn[inmapBtnSelect]
		if sel.held && sel.mode == inmapPending && !sel.chordUsed {
			sel.chordUsed = true
			*st = inmapBtnState{held: true, mode: inmapSwallowed, which: which, downAt: now}
			inmapCycleProfile(config, 1)
			return true
		}
	}
	// A chord this press completes.
	if idx := inmapMatchChord(t, b, overlay); idx >= 0 {
		line := &t.profile.bindings[idx]
		btns := line.trigger.buttons
		for _, m := range btns[:len(btns)-1] {
			eng.btn[m].chordUsed = true
		}
		*st = inmapBtnState{held: true, mode: inmapSwallowed, which: which, downAt: now}
		inmapRunTap(config, line.target, b)
		return true
	}

	*st = inmapBtnState{held: true, which: which, downAt: now}
	hold, dbl, plain := t.hold[b].pick(overlay), t.double[b].pick(overlay), t.plain[b].pick(overlay)
	modifier := t.modGlobal[b] || (!overlay && t.modScene[b]) || (b == inmapBtnSelect && inmapHotkeyActive())
	if hold >= 0 || dbl >= 0 || modifier {
		st.mode = inmapPending
		st.modifier = modifier && hold < 0 && dbl < 0
		if hold >= 0 {
			st.hasHold, st.hold = true, t.profile.bindings[hold].target
			st.holdMs = t.profile.bindings[hold].trigger.holdMs
		}
		if dbl >= 0 {
			st.hasDouble, st.double = true, t.profile.bindings[dbl].target
		}
		if plain >= 0 {
			st.hasPlain, st.plain = true, t.profile.bindings[plain].target
		}
		return true
	}
	if plain >= 0 {
		return inmapPressPlain(config, st, t.profile.bindings[plain].target)
	}
	st.mode = inmapPassed
	return false
}

// inmapPressPlain runs a plain line on the press itself. A button or key
// target stays down for as long as the button does; an action with nothing
// to act on lets the press through unchanged.
func inmapPressPlain(config *Config, st *inmapBtnState, tgt inmapTarget) bool {
	switch tgt.kind {
	case inmapTargetButton:
		if tgt.button == inmapBtnL2 || tgt.button == inmapBtnR2 {
			inmapShoulder(config, tgt.button)
			st.mode = inmapSwallowed
			return true
		}
		inmapEmitButton(tgt.button, true)
		st.mode, st.remap = inmapRemapped, tgt
		return true
	case inmapTargetKey:
		inmapEmitKey(tgt.key, true)
		st.mode, st.remap = inmapRemapped, tgt
		return true
	case inmapTargetAction:
		if inmapRunAction(config, tgt) {
			st.mode = inmapSwallowed
			return true
		}
		st.mode = inmapPassed
		return false
	case inmapTargetProfile:
		inmapApplyProfileTarget(config, tgt)
	}
	st.mode = inmapSwallowed
	return true
}

// ── Releases ─────────────────────────────────────────────────────────────

// inmapButtonUp is the engine's entry for releases; main.go calls it for each
// one before anything can drop it. It reports whether the release belonged to
// a press the engine took (rewritten, held back or used).
func inmapButtonUp(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONUP || e.Which == inmapSynthWhich || e.Which == padRepeatWhich {
		return false
	}
	b, ok := inmapButtonFromSDL(e.Button)
	if !ok {
		return false
	}
	return inmapRelease(b, config)
}

// inmapRelease settles one release.
func inmapRelease(b inmapButton, config *Config) bool {
	if b >= inmapButtonCount {
		return false
	}
	st := &inmapEngine.btn[b]
	if !st.held {
		return false
	}
	st.held = false
	st.upAt = inmapNow()
	switch st.mode {
	case inmapRemapped:
		inmapReleaseRemap(st.remap)
		st.mode = inmapIdle
		return true
	case inmapSwallowed:
		st.mode = inmapIdle
		return true
	case inmapPending:
		switch {
		case st.chordUsed || st.holdFired:
			st.mode = inmapIdle
		case st.hasDouble:
			// Wait for a second tap; inmapTick settles a lone one.
			st.taps = 1
		case st.modifier && st.upAt-st.downAt > inmapModifierTapMs:
			// Held long, and no chord came of it: not a tap either.
			st.mode = inmapIdle
		default:
			inmapResolveTap(config, b)
		}
		return true
	}
	st.mode = inmapIdle
	return false
}

// inmapReleaseRemap releases what a rewritten press became.
func inmapReleaseRemap(t inmapTarget) {
	switch t.kind {
	case inmapTargetButton:
		inmapEmitButton(t.button, false)
	case inmapTargetKey:
		inmapEmitKey(t.key, false)
	}
}

// inmapResolveTap settles a press that waited and turned out to be a tap:
// its plain line runs, or the button itself is pressed for the screen.
func inmapResolveTap(config *Config, b inmapButton) {
	st := &inmapEngine.btn[b]
	plain, hasPlain := st.plain, st.hasPlain
	st.mode, st.taps = inmapIdle, 0
	if hasPlain {
		inmapRunTap(config, plain, b)
		return
	}
	inmapTapButton(config, b)
}

// inmapRunTap runs a target as one complete press: a button or a key is
// pressed and released, and an action that does not apply falls back to the
// button that triggered it.
func inmapRunTap(config *Config, t inmapTarget, from inmapButton) {
	switch t.kind {
	case inmapTargetButton:
		inmapTapButton(config, t.button)
	case inmapTargetKey:
		inmapTapKey(t.key)
	case inmapTargetAction:
		if !inmapRunAction(config, t) {
			inmapTapButton(config, from)
		}
	case inmapTargetProfile:
		inmapApplyProfileTarget(config, t)
	}
}

// inmapForget drops a button's state without running anything. A press that
// was rewritten is released as what it became, so nothing stays stuck down.
func inmapForget(b inmapButton) {
	st := &inmapEngine.btn[b]
	if st.held && st.mode == inmapRemapped {
		inmapReleaseRemap(st.remap)
	}
	*st = inmapBtnState{}
}

// inmapPadRemoved forgets the presses of a pad that was unplugged.
func inmapPadRemoved(which sdl.JoystickID) {
	for i := range inmapEngine.btn {
		if inmapEngine.btn[i].held && inmapEngine.btn[i].which == which {
			inmapForget(inmapButton(i))
		}
	}
}

// ── Frame tick ───────────────────────────────────────────────────────────

// inmapTriggerHeld reports whether a trigger is still past its latch.
func inmapTriggerHeld(b inmapButton) bool {
	if inmapPhysicalHook != nil {
		return inmapPhysicalHook(b, -1)
	}
	if b == inmapBtnR2 {
		return padTriggerHeld[1]
	}
	return padTriggerHeld[0]
}

// inmapPhysicallyHeld asks the pad that pressed b whether the button is still
// down. With no way to ask — a pad that is gone, a remap only the
// accessibility layer can undo — the press is trusted.
func inmapPhysicallyHeld(b inmapButton, which sdl.JoystickID) bool {
	if inmapPhysicalHook != nil {
		return inmapPhysicalHook(b, which)
	}
	if which < 0 || len(a11y.keys) > 0 || a11y.swapAB || a11y.swapXY || a11y.oneHanded {
		return true
	}
	pad := sdl.GameControllerFromInstanceID(which)
	if pad == nil {
		return true
	}
	// The pad is asked for the raw button, so the logical one the profile names
	// goes back through the firmware swaps (A/B and X/Y on a handheld).
	code := padFaceButton(appConfig, inmapButtons[b].code)
	if pad.Button(sdl.GameControllerButton(code)) != 0 {
		return true
	}
	// A handheld reports Select as GUIDE on some firmwares.
	return b == inmapBtnSelect && pad.Button(sdl.CONTROLLER_BUTTON_GUIDE) != 0
}

// inmapTick advances the waits once a frame: a hold that reached its time
// runs, a single tap whose double-tap window closed runs, a trigger that was
// let go is released, and a button whose release was lost is forgotten. It
// also notices a new screen (per-screen profiles) and keeps the pass-and-play
// timer going. It reports whether it ran something, so the frame loop counts
// that as input.
func inmapTick(config *Config) bool {
	eng := &inmapEngine
	if scene := inmapCurrentScene(config); scene != eng.lastScene {
		eng.lastScene = scene
		inmapSceneEntered(scene)
	}
	hotseatTick(config)
	now := inmapNow()
	fired := false
	for i := range eng.btn {
		b := inmapButton(i)
		st := &eng.btn[i]
		if !st.held && st.mode != inmapPending {
			continue
		}
		if st.held {
			if b == inmapBtnL2 || b == inmapBtnR2 {
				if !inmapTriggerHeld(b) {
					fired = inmapRelease(b, config) || fired
					continue
				}
			} else if now-st.downAt > inmapPhysicalGraceMs && !inmapPhysicallyHeld(b, st.which) {
				inmapForget(b)
				continue
			}
		}
		if st.mode != inmapPending {
			continue
		}
		switch {
		case st.held && st.hasHold && !st.holdFired && !st.chordUsed && now-st.downAt >= uint64(st.holdMs):
			st.holdFired = true
			inmapRunTap(config, st.hold, b)
			fired = true
		case !st.held && st.taps == 1 && now-st.upAt > inmapDoubleWindowMs:
			inmapResolveTap(config, b)
			fired = true
		case now-st.downAt > inmapStuckMs:
			inmapForget(b)
		}
	}
	return fired
}

// inmapHeldNavDirection is navrepeat's held-direction reader with the active
// profile applied: a direction the profile took away does not repeat, and a
// button it turned into a direction repeats like one.
func inmapHeldNavDirection() (int, int) {
	dx, dy := vkControllerNavDirection()
	for i := range inmapEngine.btn {
		st := &inmapEngine.btn[i]
		if !st.held || st.mode == inmapPassed || st.mode == inmapIdle {
			continue
		}
		// This press did not reach the screen as itself.
		switch inmapButton(i) {
		case inmapBtnUp:
			if dy < 0 {
				dy = 0
			}
		case inmapBtnDown:
			if dy > 0 {
				dy = 0
			}
		case inmapBtnLeft:
			if dx < 0 {
				dx = 0
			}
		case inmapBtnRight:
			if dx > 0 {
				dx = 0
			}
		}
		if st.mode != inmapRemapped || st.remap.kind != inmapTargetButton {
			continue
		}
		switch st.remap.button {
		case inmapBtnUp:
			dy = -1
		case inmapBtnDown:
			dy = 1
		case inmapBtnLeft:
			dx = -1
		case inmapBtnRight:
			dx = 1
		}
	}
	return dx, dy
}
