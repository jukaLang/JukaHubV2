package main

// inputhints.go — control hints that name the keys the user actually has.
//
// Every footer badge, legend row and hint line in JukaHub is written in the
// pad's words ("A Open", "B Back", "L1/R1 Page"). On a PC without a gamepad, or
// on a TrimUI with a USB or Bluetooth keyboard plugged in, those words name
// buttons nobody is holding. This file decides which vocabulary the hints use
// and translates pad names into keyboard keys where they are drawn, so the
// several hundred hint call sites keep one source of truth.
//
// The rule (owner decision, 2026-11-09): no controller with a D-pad (a PC
// without a pad) means keyboard keys; an external keyboard connected to a
// TrimUI handheld means keyboard keys; anything else means pad buttons. When a
// pad and a keyboard are both present, the one pressed last wins, and a hotplug
// (a pad or a keyboard arriving or leaving) puts the rule back in charge.
// Settings → Controls → Button hints overrides all of it.
//
// The translation is deliberately conservative. A pad name is replaced only
// where its keyboard twin is the same on every screen (A is Enter, B is Esc,
// the D-pad is the arrow keys), or where the hint's own label says what a
// shoulder or Start does there (paging is PgUp/PgDn, first/last is Home/End,
// switching tabs is Tab, the Start menu is F10). A shoulder that clears the
// calculator or seeks a video keeps its pad name instead of promising a key
// that does something else on that screen.
//
// Pad names follow Accessibility's remap (a11y.go): with the Nintendo layout,
// an A/B or X/Y swap or a per-button remap, "A Open" names the physical button
// that now opens (hintRemapPads), and a job no button does any more reads "—"
// (hintRemapNone). Prose that names a control goes through hintSentence, so a
// sentence speaks the same words as the badges.

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// hintModeKey is the Custom setting behind Settings → Controls → Button hints.
const hintModeKey = "buttonHints"

// The values the setting takes; anything else, or nothing, is Auto.
const (
	hintModeAuto       = "auto"
	hintModeController = "controller"
	hintModeKeyboard   = "keyboard"
)

// hintVocab is the vocabulary hints are drawn in.
type hintVocab uint8

const (
	hintVocabPad hintVocab = iota
	hintVocabKeyboard
)

// hintUsed is the device pressed last since the connected set last changed.
type hintUsed uint8

const (
	hintUsedNone hintUsed = iota
	hintUsedPad
	hintUsedKeyboard
)

// inputHints is the Auto rule's state. Main thread only.
var inputHints struct {
	ready       bool
	pad         bool     // a controller with a D-pad can be used
	kb          bool     // a keyboard can be used
	last        hintUsed // pressed last since pad/kb last changed
	since       time.Time
	rescanAsked time.Time
}

// hintKeyboardNow is the vocabulary of the current frame, published by
// inputHintsTick. Draw code on any goroutine reads it.
var hintKeyboardNow atomic.Bool

// hintExternalKeyboard is set by the Linux watcher while a real keyboard (USB
// or Bluetooth) is attached to the handheld.
var hintExternalKeyboard atomic.Bool

// hintKeyboardName is the attached keyboard's name, for Settings.
var hintKeyboardName atomic.Value

// hintRescan asks the watcher to look now: a key arrived that only a keyboard
// can send, before the watcher's next tick.
var hintRescan = make(chan struct{}, 1)

// hintAnnounceQuiet keeps hotplug toasts quiet while the boot steps open the
// pad and the watcher takes its first look.
const hintAnnounceQuiet = 5 * time.Second

// hintOnHandheld reports whether this is a TrimUI handheld, whose built-in pad
// is always there.
func hintOnHandheld() bool { return DetectDevice().IsTrimui() }

// hintsUseKeyboard reports whether hints name keyboard keys this frame. Safe
// from any goroutine.
func hintsUseKeyboard() bool { return hintKeyboardNow.Load() }

// hintVocabNow is hintsUseKeyboard as a vocabulary.
func hintVocabNow() hintVocab {
	if hintKeyboardNow.Load() {
		return hintVocabKeyboard
	}
	return hintVocabPad
}

// hintModeOf reads the Button hints setting.
func hintModeOf(c *Config) string {
	if c == nil || c.Variables.Custom == nil {
		return hintModeAuto
	}
	if v, ok := c.Variables.Custom[hintModeKey].(string); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case hintModeController:
			return hintModeController
		case hintModeKeyboard:
			return hintModeKeyboard
		}
	}
	return hintModeAuto
}

// hintsAutoKeyboard is the Auto rule as a pure function: should the hints name
// keyboard keys, given what is connected, what was pressed last, and whether
// this is a handheld with its own pad?
func hintsAutoKeyboard(pad, kb bool, last hintUsed, handheld bool) bool {
	switch {
	case !pad:
		return true
	case !kb:
		return false
	case last == hintUsedKeyboard:
		return true
	case last == hintUsedPad:
		return false
	}
	// Both are there and neither has been pressed since they appeared: a
	// keyboard plugged into the handheld is there to be typed on, a pad plugged
	// into a PC is there to be played with.
	return handheld
}

// inputHintsTick re-reads what is connected and the setting, once per frame
// before anything is drawn, and publishes the vocabulary for the frame.
func inputHintsTick(config *Config) {
	// The badges name the buttons Accessibility's remap now puts the actions
	// on (INP-4).
	hintRemapRefresh()
	handheld := hintOnHandheld()
	if !inputHints.ready && handheld {
		// The first look is synchronous so the first frame is already right;
		// /proc/bus/input/devices is generated in memory, not read from a card.
		hintScanKeyboards()
	}
	pad := handheld || (gameController != nil && gameController.Attached())
	kb := !handheld || hintExternalKeyboard.Load()
	if !inputHints.ready || pad != inputHints.pad || kb != inputHints.kb {
		if inputHints.ready {
			hintAnnounceChange(config, pad, kb, handheld)
		} else {
			inputHints.since = time.Now()
		}
		inputHints.ready, inputHints.pad, inputHints.kb = true, pad, kb
		inputHints.last = hintUsedNone
	}
	keyboard := hintsAutoKeyboard(pad, kb, inputHints.last, handheld)
	switch hintModeOf(config) {
	case hintModeKeyboard:
		keyboard = true
	case hintModeController:
		keyboard = false
	}
	// A screenshot shows the hints a player holding the pad sees, whatever is
	// plugged into the PC taking it (screenshotmode.go).
	if shotModeOn() {
		keyboard = false
	}
	if hintKeyboardNow.Load() != keyboard {
		hintKeyboardNow.Store(keyboard)
	}
}

// hintAnnounceChange says why the hints just changed words, the way a console
// announces a controller. Only in Auto, never during boot, never behind a game.
func hintAnnounceChange(config *Config, pad, kb, handheld bool) {
	if hintModeOf(config) != hintModeAuto || time.Since(inputHints.since) < hintAnnounceQuiet || backgroundPaused() {
		return
	}
	switch {
	case handheld && kb && !inputHints.kb:
		ShowToast("Keyboard connected · hints show its keys", ToastKindInfo)
	case handheld && !kb && inputHints.kb:
		ShowToast("Keyboard disconnected · hints show the buttons", ToastKindInfo)
	case !handheld && pad && !inputHints.pad:
		ShowToast("Controller connected · hints show its buttons", ToastKindInfo)
	case !handheld && !pad && inputHints.pad:
		ShowToast("Controller disconnected · hints show keyboard keys", ToastKindInfo)
	}
}

// inputHintsNoteKey records a real key press. A synthesized key (a control
// profile, the Phone Companion) has no scancode, and the handheld's volume,
// power and media keys are not typing, so neither counts.
func inputHintsNoteKey(e *sdl.KeyboardEvent) {
	if e == nil || e.Type != sdl.KEYDOWN || e.Repeat != 0 || !hintTypingScancode(int(e.Keysym.Scancode)) {
		return
	}
	inputHints.last = hintUsedKeyboard
	// A keyboard that is typed on before the watcher has seen it: look now,
	// at most every two seconds (a firmware that delivers its own buttons as
	// keys would otherwise reread the device list on every press).
	if !hintExternalKeyboard.Load() && hintOnHandheld() && time.Since(inputHints.rescanAsked) >= 2*time.Second {
		inputHints.rescanAsked = time.Now()
		select {
		case hintRescan <- struct{}{}:
		default:
		}
	}
}

// inputHintsNotePad records a pad press: a button, or a stick or trigger
// pushed past its dead zone.
func inputHintsNotePad() {
	inputHints.last = hintUsedPad
}

// keyShoulder maps PgUp/PgDn/Home/End onto L1/R1/L2/R2: the keyboard twins the
// hints name for paging a list and for jumping to its ends.
func keyShoulder(sym sdl.Keycode) padShoulder {
	switch sym {
	case sdl.K_PAGEDOWN:
		return padR1
	case sdl.K_HOME:
		return padL2
	case sdl.K_END:
		return padR2
	}
	return padL1
}

// hintTypingScancode reports whether a scancode is a key a keyboard has and a
// handheld's buttons do not send: letters through the arrows, the keypad and
// the Menu key (SDL_SCANCODE_A = 4 … SDL_SCANCODE_APPLICATION = 101). Power,
// volume and media keys sit above that range.
func hintTypingScancode(sc int) bool {
	return sc >= 4 && sc <= 101
}

// ── Translation ─────────────────────────────────────────────────────────────

// hintCtx is what a hint's label says about the action, which decides the
// keyboard twin of a shoulder or Start.
type hintCtx uint8

const (
	hintCtxNone hintCtx = iota
	hintCtxPage         // scroll or page through a list
	hintCtxEnds         // jump to the first or last entry
	hintCtxMore         // load the next results
	hintCtxTabs         // switch tabs
	hintCtxMenu         // the Start menu
)

// hintClass sorts a button name: pad-only, a keyboard key, both (arrows, the
// mouse), or a letter that is both a pad button and a key (X, Y).
type hintClass uint8

const (
	hintClassKey hintClass = iota
	hintClassPad
	hintClassShared
	hintClassLetter
)

// hintCacheKey is one translated badge. remap is the generation of the
// Accessibility remap it was drawn under (hintRemapNow).
type hintCacheKey struct {
	v      hintVocab
	ctx    hintCtx
	remap  uint32
	button string
}

// hintCacheMax bounds the cache; the hint vocabulary is a few hundred strings.
const hintCacheMax = 1024

var hintCache struct {
	mu sync.Mutex
	m  map[hintCacheKey]string
}

// hintKeyFor is the badge text for a hint's button in this frame's
// vocabulary; label is the hint's action. A pad button is named as the
// physical button that sends it under Accessibility's remap (hintRemapPads).
// Safe from any goroutine.
func hintKeyFor(button, label string) string {
	return hintKeyIn(button, label, hintVocabNow(), hintRemapNow.Load())
}

// hintKeyIn is hintKeyFor in a given vocabulary and under a given remap (nil
// for none), for a caller that names several controls in one frame's words.
func hintKeyIn(button, label string, v hintVocab, remap *hintPadRemap) string {
	if button == "" {
		return button
	}
	key := hintCacheKey{v: v, button: button}
	if remap != nil {
		key.remap = remap.gen
	}
	if hintNeedsLabel(button) {
		key.ctx = hintLabelContext(label)
	}
	hintCache.mu.Lock()
	out, ok := hintCache.m[key]
	hintCache.mu.Unlock()
	if ok {
		return out
	}
	brick := brickHasFunctionKeys()
	out = hintTranslate(button, key.v, key.ctx, !hintOnHandheld(), brick)
	if remap != nil {
		out = hintRemapPads(out, key.v, brick, remap.pairs)
	}
	hintCache.mu.Lock()
	if hintCache.m == nil || len(hintCache.m) >= hintCacheMax {
		hintCache.m = make(map[hintCacheKey]string, 64)
	}
	hintCache.m[key] = out
	hintCache.mu.Unlock()
	return out
}

// hintNeedsLabel reports whether a button's keyboard twin depends on what the
// hint does: the shoulders and Start.
func hintNeedsLabel(button string) bool {
	for _, s := range [...]string{"L1", "R1", "L2", "R2", "L / R", "Start"} {
		if hintContainsFold(button, s) {
			return true
		}
	}
	return false
}

// hintCtxWords are the label words that say what a shoulder or Start does.
var hintCtxWords = [...]struct {
	word string
	ctx  hintCtx
}{
	{"tab", hintCtxTabs}, {"tabs", hintCtxTabs},
	{"menu", hintCtxMenu},
	{"more", hintCtxMore},
	{"first", hintCtxEnds}, {"last", hintCtxEnds}, {"top", hintCtxEnds}, {"end", hintCtxEnds}, {"bottom", hintCtxEnds},
	{"page", hintCtxPage}, {"pages", hintCtxPage}, {"scroll", hintCtxPage},
	// The tools pages jump a section with L1/R1 and PgUp/PgDn (toolshub.go).
	{"section", hintCtxPage}, {"sections", hintCtxPage},
}

// hintLabelContext reads a hint's label for the action it names: the first
// whole word that means something, so "Send" is not "end" and "Stop" is not
// "top". It runs for every shoulder badge of every frame and does not allocate.
func hintLabelContext(label string) hintCtx {
	start := -1
	for i := 0; i <= len(label); i++ {
		letter := i < len(label) && (('a' <= label[i] && label[i] <= 'z') || ('A' <= label[i] && label[i] <= 'Z'))
		switch {
		case letter && start < 0:
			start = i
		case !letter && start >= 0:
			word := label[start:i]
			for _, w := range hintCtxWords {
				if strings.EqualFold(word, w.word) {
					return w.ctx
				}
			}
			start = -1
		}
	}
	return hintCtxNone
}

// hintContainsFold is an ASCII case-insensitive strings.Contains that does not
// allocate (it runs for every badge of every frame).
func hintContainsFold(s, sub string) bool {
	n := len(sub)
	if n == 0 {
		return true
	}
	for i := 0; i+n <= len(s); i++ {
		match := true
		for j := 0; j < n; j++ {
			a, b := s[i+j], sub[j]
			if 'A' <= a && a <= 'Z' {
				a += 'a' - 'A'
			}
			if 'A' <= b && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// hintIsQuitChord reports the pad's quit chord in any of its spellings.
func hintIsQuitChord(b string) bool {
	compact := strings.ToLower(strings.ReplaceAll(b, " ", ""))
	return compact == "select+start" || compact == "start+select"
}

// hintIsMouseChord reports mouse mode's chord, which is read from the physical
// buttons like the quit chord (vcursor_mode.go), so an Accessibility remap does
// not rename it. It is kept apart from hintIsQuitChord, which hintTranslate
// turns into Alt+F4.
func hintIsMouseChord(b string) bool {
	compact := strings.ToLower(strings.ReplaceAll(b, " ", ""))
	return compact == "select+l1" || compact == "l1+select"
}

// hintTranslate is the translation as a pure function. In the pad vocabulary
// it only drops keyboard alternatives ("A / Enter" → "A"); in the keyboard one
// it names the twin key ("A" → "Enter", "B / Esc" → "Esc"). desktop is a PC
// (a window that Alt+F4 closes) rather than the handheld.
func hintTranslate(button string, v hintVocab, ctx hintCtx, desktop, brick bool) string {
	b := strings.TrimSpace(button)
	if b == "" {
		return button
	}
	if hintIsQuitChord(b) {
		// The window's own close keys on a PC; the handheld keeps its chord
		// (the pad is still in the user's hands).
		if v == hintVocabKeyboard && desktop {
			return "Alt+F4"
		}
		return button
	}
	if rest, ok := strings.CutPrefix(b, "Hold "); ok && rest != "" {
		// "Hold A" is a held press of whatever names A: "Hold Enter" on a
		// keyboard (Patch's hold-to-confirm takes Enter the same way).
		return "Hold " + hintTranslate(rest, v, ctx, desktop, brick)
	}
	if v == hintVocabKeyboard {
		if twin, ok := hintKeyboardTwin(b, ctx); ok {
			return twin
		}
	}
	sep := " / "
	parts := strings.Split(b, sep)
	if len(parts) == 1 {
		// One name stays as written: a chord (Ctrl+V, Select + R1) or a name
		// without "/". "U/L1" and "</>" are alternatives written without spaces.
		if strings.Contains(b, "+") || !strings.Contains(b, "/") || hintClassOf(b, brick) != hintClassKey {
			return button
		}
		sep = "/"
		parts = strings.Split(b, sep)
		// Only a pad name spelt out ("U/L1") makes the halves alternatives;
		// "A/D" (JukaLand's move keys) and "</>" are pairs of keys.
		named := false
		for _, p := range parts {
			if t := strings.TrimSpace(p); len(t) > 1 && hintClassOf(t, brick) == hintClassPad {
				named = true
			}
		}
		if !named {
			return button
		}
	}
	return hintPickAlternatives(button, parts, sep, v, ctx, brick)
}

// hintPickAlternatives keeps the alternatives of one badge that belong to the
// vocabulary. In the keyboard one a pad name the hint already pairs with a key
// is dropped ("Start / Del" → "Del"), the rest are translated, and a pad name
// with no twin goes when something pressable is left.
func hintPickAlternatives(button string, parts []string, sep string, v hintVocab, ctx hintCtx, brick bool) string {
	hasKey := false
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if parts[i] != "" && hintClassOf(parts[i], brick) == hintClassKey {
			hasKey = true
		}
	}
	out := make([]string, 0, len(parts))
	var stranded []string
	for _, p := range parts {
		if p == "" {
			continue
		}
		switch hintClassOf(p, brick) {
		case hintClassKey:
			if v == hintVocabPad {
				continue
			}
		case hintClassLetter:
			// X and Y are pad buttons and letter keys. Next to a named key the
			// key is the keyboard's half ("X / C" is C on a keyboard).
			if v == hintVocabKeyboard && hasKey {
				continue
			}
		case hintClassPad:
			if v == hintVocabKeyboard {
				if hasKey {
					continue
				}
				twin, ok := hintKeyboardTwin(p, ctx)
				if !ok {
					stranded = append(stranded, p)
					continue
				}
				p = twin
			}
		}
		if !hintHasFold(out, p) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = stranded
	}
	if len(out) == 0 {
		return button
	}
	return strings.Join(out, sep)
}

// hintHasFold reports whether list already holds s, ignoring case.
func hintHasFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// hintClassOf sorts one button name. On a Brick, F1 and F2 are keys on the
// device as well as on a keyboard.
func hintClassOf(tok string, brick bool) hintClass {
	t := strings.ToLower(strings.TrimSpace(tok))
	if len(t) > 1 && strings.Contains(t, "+") {
		// A chord: the keyboard's (Ctrl+V, Shift+←→) or the pad's (L1+X).
		if strings.HasPrefix(t, "ctrl+") || strings.HasPrefix(t, "shift+") || strings.HasPrefix(t, "alt+") {
			return hintClassKey
		}
		return hintClassPad
	}
	switch t {
	case "a", "b", "start", "select", "menu", "d-pad", "dpad",
		"l1", "r1", "l2", "r2", "l3", "r3", "l1/r1", "l2/r2", "l / r", "l/r":
		return hintClassPad
	case "x", "y":
		return hintClassLetter
	case "up", "down", "left", "right", "up/down", "left/right",
		"up / down", "left / right", "left right", "arrows",
		"↑", "↓", "←", "→", "↑ ↓", "↑↓", "←/→", "←→", "← →", "↑ ↓ ← →",
		"mouse", "click", "tap", "touch":
		return hintClassShared
	case "f1", "f2":
		if brick {
			return hintClassShared
		}
	}
	return hintClassKey
}

// hintKeyboardTwin is the key that does what a pad button does, where that is
// the same on every screen (A, B, the D-pad) or the label says so.
func hintKeyboardTwin(tok string, ctx hintCtx) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(tok)) {
	case "a":
		return "Enter", true
	case "b", "menu":
		// Not Select: a screen that names Select gives it its own job (Wi-Fi
		// diagnostics), which Esc would not do.
		return "Esc", true
	case "d-pad", "dpad":
		return "Arrows", true
	case "start":
		if ctx == hintCtxMenu {
			return "F10", true
		}
	case "l1/r1", "l / r", "l/r":
		switch ctx {
		case hintCtxPage:
			return "PgUp/PgDn", true
		case hintCtxTabs:
			return "Tab", true
		}
	case "l1":
		if ctx == hintCtxPage {
			return "PgUp", true
		}
	case "r1":
		if ctx == hintCtxPage {
			return "PgDn", true
		}
	case "l2/r2":
		if ctx == hintCtxEnds {
			return "Home/End", true
		}
	case "l2":
		if ctx == hintCtxEnds {
			return "Home", true
		}
	case "r2":
		switch ctx {
		case hintCtxEnds:
			return "End", true
		case hintCtxMore:
			return "PgDn", true
		}
	}
	return "", false
}

// homeHintKey names a button of Home's footer. Home's keyboard twins differ
// from the generic ones in two places: "/" opens search there (handleHomeKey)
// and Q/E step the sections like L2/R2 (the global scene keys in main.go).
func homeHintKey(button, label string) string {
	if hintsUseKeyboard() {
		switch button {
		case "Y":
			return "/"
		case "L2/R2":
			return "Q/E"
		}
	}
	return hintKeyFor(button, label)
}

// hintQuitKeys names how to quit from a keyboard: the window's close keys on a
// PC, the pad's chord on the handheld.
func hintQuitKeys() string {
	if hintOnHandheld() {
		return "Select + Start"
	}
	return "Alt+F4"
}

// ── Accessibility remaps ───────────────────────────────────────────────────

// hintRemapPair is one action Accessibility moved to another button: the
// button a hint names (the logical one every handler sees) and the physical
// button that now sends it.
type hintRemapPair struct {
	logical, physical string
}

// hintPadRemap is the inverse of the remap layer in a11y.go (Nintendo layout,
// the A/B and X/Y swaps, the per-button keymap) as the hints need it. It is
// built on the main thread when the layer changes and only read after that, so
// draw code on any goroutine may use it.
type hintPadRemap struct {
	gen   uint32
	pairs []hintRemapPair
}

// hintRemapNow is the inverse in force; nil (no remap) before the first frame.
var hintRemapNow atomic.Pointer[hintPadRemap]

// hintRemapSeen is what the layer did to each button when the inverse was
// last built. Main thread only, like a11y's own state.
var hintRemapSeen struct {
	ready bool
	sig   [16]uint8
}

// hintRemapSignature is what the remap layer does to each button it knows
// (a11yButtonNames), a disabled one included: the per-frame change check,
// eight map lookups and no allocation.
func hintRemapSignature() [16]uint8 {
	var sig [16]uint8
	for i, b := range a11yButtonNames {
		if i >= len(sig) {
			break
		}
		logical, dropped := a11yResolveButton(b.button)
		if dropped {
			logical = a11yDisabledButton
		}
		sig[i] = logical
	}
	return sig
}

// hintRemapRefresh rebuilds the inverse when Accessibility changed the layer
// (a preset, a row of the panel, a restored backup). inputHintsTick runs it
// once per frame on the main thread; the new generation retires the cached
// badges drawn under the old one.
func hintRemapRefresh() {
	sig := hintRemapSignature()
	if hintRemapSeen.ready && sig == hintRemapSeen.sig {
		return
	}
	hintRemapSeen.ready, hintRemapSeen.sig = true, sig
	next := &hintPadRemap{gen: 1, pairs: hintRemapInverse(a11yResolveButton)}
	if cur := hintRemapNow.Load(); cur != nil {
		next.gen = cur.gen + 1
	}
	hintRemapNow.Store(next)
}

// hintRemapGen identifies the remap in force, for a cache of hint text built
// outside hintKeyFor (a sentence, a memoised footer): it changes whenever the
// remap does.
func hintRemapGen() uint32 {
	if r := hintRemapNow.Load(); r != nil {
		return r.gen
	}
	return 0
}

// hintStamp is the words hints are drawn in: the vocabulary and the remap. A
// cache of hint text or badge widths built outside hintKeyFor (a sheet, a
// memoised footer) keys on it, so a switch to the keyboard and a new
// Accessibility remap both rebuild it.
type hintStamp struct {
	v     hintVocab
	remap uint32
}

// hintStampNow is this frame's hintStamp. Safe from any goroutine.
func hintStampNow() hintStamp {
	return hintStamp{v: hintVocabNow(), remap: hintRemapGen()}
}

// hintRemapNone names the button of an action no physical button sends any
// more: its own button was switched off ("select:none") or given another job
// ("a:x") and nothing took its place. The button's own name would promise a
// button that now does something else or nothing; a dash promises nothing.
const hintRemapNone = "—"

// hintRemapInverse inverts a resolver (a11yResolveButton): for each button a
// hint can name, the physical button that sends it now. A button that still
// sends itself is left out; when two buttons send one action the first in
// a11yButtonNames order is named; and an action no button sends any more is
// named hintRemapNone.
func hintRemapInverse(resolve func(uint8) (uint8, bool)) []hintRemapPair {
	var pairs []hintRemapPair
	for _, want := range a11yButtonNames {
		if got, dropped := resolve(want.button); !dropped && got == want.button {
			continue
		}
		physical := hintRemapNone
		for _, phys := range a11yButtonNames {
			if got, dropped := resolve(phys.button); !dropped && got == want.button {
				physical = phys.label
				break
			}
		}
		pairs = append(pairs, hintRemapPair{logical: want.label, physical: physical})
	}
	return pairs
}

// hintRemapPads names the physical buttons in a translated badge: with the A/B
// swap on, the button that opens is B, so "A" is drawn "B". Keyboard words are
// left alone, because Accessibility remaps the pad and never the keyboard; so
// are X and Y in the keyboard vocabulary, where they are letter keys, a key
// pair such as JukaLand's "A/D", and the quit and mouse-mode chords, which are
// read from the physical buttons (exit_chord.go, vcursor_mode.go). Names match
// as hints spell them ("Select",
// not "SELECT"); the controller test, which also reads the physical buttons,
// translates its line with no remap at all (ctrldiagFooterLine). An
// alternative that no button sends any more is dropped while another is left
// ("B / Start" with Start switched off is "B"); with none left the badge is
// hintRemapNone. It runs only when a badge is not in hintKeyFor's cache.
func hintRemapPads(s string, v hintVocab, brick bool, pairs []hintRemapPair) string {
	if len(pairs) == 0 || hintIsQuitChord(s) || hintIsMouseChord(s) {
		return s
	}
	alts := strings.Split(s, " / ")
	kept := alts[:0]
	for _, alt := range alts {
		if alt = hintRemapAlternative(alt, v, brick, pairs); strings.TrimSpace(alt) != hintRemapNone {
			kept = append(kept, alt)
		}
	}
	if len(kept) == 0 {
		return hintRemapNone
	}
	return strings.Join(kept, " / ")
}

// hintRemapAlternative is hintRemapPads for one alternative of a badge: a
// chord ("Select + R1"), a pair ("L1/R1"), or words ("A", "Hold A").
func hintRemapAlternative(alt string, v hintVocab, brick bool, pairs []hintRemapPair) string {
	t := strings.TrimSpace(alt)
	switch {
	case len(t) > 1 && strings.Contains(t, "+"):
		if hintClassOf(t, brick) == hintClassKey {
			return alt // Ctrl+V, Shift+←→
		}
		// A pad chord: every part is a pad button, X and Y included.
		parts := strings.Split(alt, "+")
		for i, p := range parts {
			parts[i] = hintRemapWord(p, pairs)
		}
		return strings.Join(parts, "+")
	case strings.Contains(t, "/"):
		parts := strings.Split(alt, "/")
		for _, p := range parts {
			if !hintRemapIsPad(p, v, brick) {
				return alt // a pair of keys: "A/D", "P/N", "PgUp/PgDn"
			}
		}
		for i, p := range parts {
			parts[i] = hintRemapWord(p, pairs)
		}
		return strings.Join(parts, "/")
	}
	words := strings.Split(alt, " ")
	for i, w := range words {
		if hintRemapIsPad(w, v, brick) {
			words[i] = hintRemapWord(w, pairs)
		}
	}
	return strings.Join(words, " ")
}

// hintRemapIsPad reports whether a name in a translated badge is a pad
// button. X and Y are keys in the keyboard vocabulary.
func hintRemapIsPad(w string, v hintVocab, brick bool) bool {
	switch hintClassOf(w, brick) {
	case hintClassPad:
		return true
	case hintClassLetter:
		return v == hintVocabPad
	}
	return false
}

// hintRemapWord renames one button, keeping the spaces around it.
func hintRemapWord(w string, pairs []hintRemapPair) string {
	t := strings.TrimSpace(w)
	for _, p := range pairs {
		if t == p.logical {
			return strings.Replace(w, t, p.physical, 1)
		}
	}
	return w
}

// ── Sentences that name a control ──────────────────────────────────────────

// hintSentenceKey is one cached sentence: its pattern, the controls it names
// and the vocabulary and remap it was written for.
type hintSentenceKey struct {
	format  string
	button1 string
	label1  string
	button2 string
	label2  string
	v       hintVocab
	remap   uint32
}

// hintSentenceMax bounds the sentence cache: a few dozen sentences, each in two
// vocabularies.
const hintSentenceMax = 256

var hintSentences struct {
	mu sync.Mutex
	m  map[hintSentenceKey]string
}

// hintSentence writes prose that names a control: the first %s in format is
// replaced by hintKeyFor(button, label), so "Press %s to type" reads "Press A
// to type" on the pad and "Press Enter to type" on a keyboard, and follows an
// Accessibility remap like every badge. button may be a pad/key twin ("Y / S")
// when the screen's key is not the generic one. The result is cached per
// vocabulary and remap, so a draw call may use it every frame. Safe from any
// goroutine.
func hintSentence(format, button, label string) string {
	return hintSentence2(format, button, label, "", "")
}

// hintSentence2 is hintSentence for two controls: the first %s names button1,
// the second button2.
func hintSentence2(format, button1, label1, button2, label2 string) string {
	// One vocabulary and one remap for the key and both controls, even if the
	// main thread publishes new ones meanwhile.
	v, remap := hintVocabNow(), hintRemapNow.Load()
	key := hintSentenceKey{format: format, button1: button1, label1: label1,
		button2: button2, label2: label2, v: v}
	if remap != nil {
		key.remap = remap.gen
	}
	hintSentences.mu.Lock()
	out, ok := hintSentences.m[key]
	hintSentences.mu.Unlock()
	if ok {
		return out
	}
	out = strings.Replace(format, "%s", hintKeyIn(button1, label1, v, remap), 1)
	if button2 != "" {
		out = strings.Replace(out, "%s", hintKeyIn(button2, label2, v, remap), 1)
	}
	hintSentences.mu.Lock()
	if hintSentences.m == nil || len(hintSentences.m) >= hintSentenceMax {
		hintSentences.m = make(map[hintSentenceKey]string, 32)
	}
	hintSentences.m[key] = out
	hintSentences.mu.Unlock()
	return out
}

// ── Keyboard watcher (Linux handhelds) ─────────────────────────────────────

// hintInputDevicesPath lists the kernel's input devices.
const hintInputDevicesPath = "/proc/bus/input/devices"

// hintWatchInterval is how often the watcher looks for a keyboard that was
// plugged in without being typed on.
const hintWatchInterval = 3 * time.Second

// startInputHintsMonitor watches for a USB or Bluetooth keyboard on a TrimUI
// handheld. A PC always has a keyboard, so it does nothing there. The watcher
// parks with the other pollers while a game owns the screen.
func startInputHintsMonitor() {
	if runtime.GOOS != "linux" || !hintOnHandheld() {
		return
	}
	go func() {
		ticker := time.NewTicker(hintWatchInterval)
		defer ticker.Stop()
		wake := subscribeForegroundWake()
		for {
			if !waitForPollOrRescan(ticker.C, wake) {
				continue
			}
			hintScanKeyboards()
		}
	}()
}

// waitForPollOrRescan is waitForPoll that also wakes for a rescan request.
func waitForPollOrRescan(tick <-chan time.Time, wake <-chan struct{}) bool {
	select {
	case <-tick:
	case <-wake:
	case <-hintRescan:
	}
	return !backgroundPaused()
}

// hintScanKeyboards reads the device list and publishes the result, logging
// each change so a false positive on a firmware can be found in the log.
func hintScanKeyboards() {
	data, err := os.ReadFile(hintInputDevicesPath)
	if err != nil {
		return
	}
	found, name := hintParseInputDevices(data)
	if found != hintExternalKeyboard.Load() {
		if found {
			logInfo("input", "keyboard attached: %s", name)
		} else {
			logInfo("input", "keyboard detached")
		}
	}
	hintKeyboardName.Store(name)
	hintExternalKeyboard.Store(found)
}

// hintAttachedKeyboardName is the keyboard the watcher found, or "".
func hintAttachedKeyboardName() string {
	if name, ok := hintKeyboardName.Load().(string); ok && hintExternalKeyboard.Load() {
		return name
	}
	return ""
}

// hintParseInputDevices finds a real keyboard in /proc/bus/input/devices: a USB
// (bus 0003), Bluetooth (0005) or PS/2 (0011) device whose key bitmap has every
// key from Esc to D (bits 1-31 of its lowest word — udev's ID_INPUT_KEYBOARD
// rule). The handheld's own power and volume keys (gpio, bus 0019) fail that
// test, uinput devices under /devices/virtual/input (the firmware's virtual pad,
// key mappers) are skipped, and so is anything that also has gamepad buttons.
func hintParseInputDevices(data []byte) (bool, string) {
	var bus uint64
	var name, sysfs, keyBits string
	check := func() (bool, string) {
		defer func() { bus, name, sysfs, keyBits = 0, "", "", "" }()
		switch bus {
		case 0x03, 0x05, 0x11:
		default:
			return false, ""
		}
		if strings.HasPrefix(sysfs, "/devices/virtual/input/") || keyBits == "" {
			return false, ""
		}
		words := strings.Fields(keyBits)
		low, err := strconv.ParseUint(words[len(words)-1], 16, 64)
		if err != nil || low&0xFFFFFFFE != 0xFFFFFFFE {
			return false, ""
		}
		// BTN_SOUTH (0x130) is bit 48 of the fifth 64-bit word from the end.
		if len(words) >= 5 {
			if w, err := strconv.ParseUint(words[len(words)-5], 16, 64); err == nil && w&(1<<48) != 0 {
				return false, ""
			}
		}
		return true, name
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			if ok, n := check(); ok {
				return true, n
			}
		case strings.HasPrefix(line, "I:"):
			if i := strings.Index(line, "Bus="); i >= 0 && len(line) >= i+8 {
				bus, _ = strconv.ParseUint(line[i+4:i+8], 16, 16)
			}
		case strings.HasPrefix(line, "N: Name="):
			name = strings.Trim(strings.TrimPrefix(line, "N: Name="), "\"")
		case strings.HasPrefix(line, "S: Sysfs="):
			sysfs = strings.TrimPrefix(line, "S: Sysfs=")
		case strings.HasPrefix(line, "B: KEY="):
			keyBits = strings.TrimPrefix(line, "B: KEY=")
		}
	}
	return check()
}

// ── Settings ────────────────────────────────────────────────────────────────

// hintModeOptions are the stops of Settings → Controls → Button hints.
func hintModeOptions(*Config) []setOption {
	return []setOption{
		{Value: hintModeAuto, Label: "Auto", Note: "Follow the controller or keyboard in use"},
		{Value: hintModeController, Label: "Controller", Note: "Always name the pad's buttons"},
		{Value: hintModeKeyboard, Label: "Keyboard", Note: "Always name the keyboard's keys"},
	}
}

// hintModeCurrent is the index of the stored stop.
func hintModeCurrent(c *Config) int {
	switch hintModeOf(c) {
	case hintModeController:
		return 1
	case hintModeKeyboard:
		return 2
	}
	return 0
}

// hintModeDetail says what the hints show and why, so Auto is never a mystery.
func hintModeDetail(c *Config) string {
	switch hintModeOf(c) {
	case hintModeController:
		return "Hints always name the controller's buttons"
	case hintModeKeyboard:
		return "Hints always name the keyboard's keys"
	}
	if !hintsUseKeyboard() {
		return "Auto · showing controller buttons"
	}
	if name := hintAttachedKeyboardName(); name != "" {
		return "Auto · showing keyboard keys for " + name
	}
	if !inputHints.pad {
		return "Auto · showing keyboard keys (no controller found)"
	}
	return "Auto · showing keyboard keys"
}

// hintSetMode stores the setting and applies it on the next frame.
func hintSetMode(c *Config, value string) {
	switch value {
	case hintModeController, hintModeKeyboard:
	default:
		value = hintModeAuto
	}
	setStoreString(c, hintModeKey, value)
	inputHintsTick(c)
}
