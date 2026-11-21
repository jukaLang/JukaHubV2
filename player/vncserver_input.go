package main

// vncserver_input.go — what a VNC viewer's keyboard, mouse and clipboard do.
//
// On JukaHub's own screens a viewer's keys arrive the way a USB keyboard's
// do: each KeyEvent becomes the SDL key event a real keyboard sends, plus
// the text event SDL adds for a printable key. Every keyboard twin JukaHub
// knows (inputhints.go: Enter is A, Esc is B, the arrows are the D-pad,
// PgUp/PgDn page, Home/End jump, Tab switches tabs, F10 opens the Start
// menu, X and Y are X and Y) and every text field therefore work from a
// viewer exactly as from a keyboard plugged into the handheld. The
// connection goroutines only queue; the main loop pushes the events
// (vncDrainInput, beside the Phone Companion's queue), so SDL is touched on
// its own thread alone.
//
// While a game owns the screen JukaHub's loop is parked and reads no SDL
// events, so the keys go to the game as the built-in pad's buttons instead
// (vncPadFor; vncserver_input_linux.go writes them into the pad): the same
// twins where they exist, Q/E for L2/R2 as on Home, Tab for Select and F1
// for the Menu button, which opens the in-game menu.
//
// The mouse moves JukaHub's own pointer (virtualcursor.go) while it is up —
// mouse mode, or the right stick's pointer — and is ignored otherwise: left
// button is the click, the wheel scrolls. Clipboard text a viewer sends
// lands on JukaHub's clipboard (clipboard.go), at most vncMaxCutText.

import (
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

// What a queued viewer action is.
const (
	vncInKey = iota
	vncInPointer
	vncInClip
	// vncInRelease: a viewer left or the server stopped, so every key and
	// button the viewers held is let go.
	vncInRelease
)

// vncInput is one viewer action waiting for the main loop.
type vncInput struct {
	kind    uint8
	down    bool
	key     uint32
	buttons uint8
	x, y    int
	text    string
}

// vncInputQueueMax bounds the backlog; the oldest actions go first, as in
// the Phone Companion's queue (a release lost there is made good by the
// release-all a viewer's leaving queues).
const vncInputQueueMax = 256

var vncInputQueue struct {
	mu    sync.Mutex
	items []vncInput
}

// vncQueueInput queues one action from a connection goroutine. A pointer
// move replaces a queued move with the same buttons: only where the pointer
// ends up matters, and a fast mouse would fill the queue otherwise.
func vncQueueInput(ev vncInput) {
	vncInputQueue.mu.Lock()
	defer vncInputQueue.mu.Unlock()
	if n := len(vncInputQueue.items); n > 0 && ev.kind == vncInPointer {
		if last := &vncInputQueue.items[n-1]; last.kind == vncInPointer && last.buttons == ev.buttons {
			*last = ev
			return
		}
	}
	if len(vncInputQueue.items) >= vncInputQueueMax {
		vncInputQueue.items = append(vncInputQueue.items[:0], vncInputQueue.items[1:]...)
	}
	vncInputQueue.items = append(vncInputQueue.items, ev)
}

// vncViewerKey routes a viewer's key: to the game's pad while a game owns
// the screen, else to the main loop as a keyboard key.
func vncViewerKey(down bool, keysym uint32) {
	if gameScreenSuspended() {
		if a := vncPadFor(keysym); a != vncPadNone {
			vncPadPress(a, down)
		}
		// A key that went down on JukaHub's screens (the Enter that launched
		// the game) is let go there too once the main loop runs again; a
		// release of a key it never saw pressed is dropped there.
		if !down {
			vncQueueInput(vncInput{kind: vncInKey, key: keysym})
		}
		return
	}
	// The game ended: give the pad back before JukaHub reads it again.
	vncPadClose()
	vncQueueInput(vncInput{kind: vncInKey, down: down, key: keysym})
}

// vncKeys is the main thread's view of what the viewers hold.
var vncKeys struct {
	held    map[sdl.Keycode]bool
	mod     uint16
	buttons uint8
}

// vncDrainInput applies what the viewers sent since the last frame. Main
// loop only, where the Phone Companion's queue is applied.
func vncDrainInput(config *Config) {
	_ = config
	vncInputQueue.mu.Lock()
	items := vncInputQueue.items
	vncInputQueue.items = nil
	vncInputQueue.mu.Unlock()
	for _, ev := range items {
		switch ev.kind {
		case vncInKey:
			vncApplyKey(ev.down, ev.key)
		case vncInPointer:
			vncApplyPointer(ev.buttons, ev.x, ev.y)
		case vncInClip:
			// copyTextToClipboard also files it in the clipboard history.
			if err := copyTextToClipboard(ev.text, "VNC viewer"); err != nil {
				logDebug("vnc", "viewer clipboard not taken: %v", err)
			}
		case vncInRelease:
			vncReleaseAll()
		}
	}
}

// vncKeysymSDL maps the X11 keysyms of keys that type nothing to SDL keys.
// KP_Enter is Return, as Enter is A on every screen.
var vncKeysymSDL = map[uint32]sdl.Keycode{
	0xff08: sdl.K_BACKSPACE, 0xff09: sdl.K_TAB, 0xfe20: sdl.K_TAB, 0xff0d: sdl.K_RETURN, 0xff8d: sdl.K_RETURN,
	0xff1b: sdl.K_ESCAPE, 0xffff: sdl.K_DELETE, 0xff9f: sdl.K_DELETE, 0xff63: sdl.K_INSERT,
	0xff50: sdl.K_HOME, 0xff51: sdl.K_LEFT, 0xff52: sdl.K_UP, 0xff53: sdl.K_RIGHT, 0xff54: sdl.K_DOWN,
	0xff55: sdl.K_PAGEUP, 0xff56: sdl.K_PAGEDOWN, 0xff57: sdl.K_END,
	0xff95: sdl.K_HOME, 0xff96: sdl.K_LEFT, 0xff97: sdl.K_UP, 0xff98: sdl.K_RIGHT, 0xff99: sdl.K_DOWN,
	0xff9a: sdl.K_PAGEUP, 0xff9b: sdl.K_PAGEDOWN, 0xff9c: sdl.K_END,
	0xffbe: sdl.K_F1, 0xffbf: sdl.K_F2, 0xffc0: sdl.K_F3, 0xffc1: sdl.K_F4, 0xffc2: sdl.K_F5, 0xffc3: sdl.K_F6,
	0xffc4: sdl.K_F7, 0xffc5: sdl.K_F8, 0xffc6: sdl.K_F9, 0xffc7: sdl.K_F10, 0xffc8: sdl.K_F11, 0xffc9: sdl.K_F12,
	0xffe1: sdl.K_LSHIFT, 0xffe2: sdl.K_RSHIFT, 0xffe3: sdl.K_LCTRL, 0xffe4: sdl.K_RCTRL,
	0xffe9: sdl.K_LALT, 0xffea: sdl.K_RALT, 0xffe7: sdl.K_LGUI, 0xffe8: sdl.K_RGUI, 0xffeb: sdl.K_LGUI, 0xffec: sdl.K_RGUI,
}

// vncKeysymKey is the SDL key a keysym presses and, for a key that types,
// its text: Latin-1 keysyms are their own code points, 0x01000000 + U is
// Unicode U, the keypad digits type digits. SDL names a typing key by its
// unshifted character, so a letter's key is lower case.
func vncKeysymKey(keysym uint32) (sdl.Keycode, string) {
	if code, ok := vncKeysymSDL[keysym]; ok {
		return code, ""
	}
	var r rune
	switch {
	case keysym >= 0x20 && keysym <= 0x7e, keysym >= 0xa0 && keysym <= 0xff:
		r = rune(keysym)
	case keysym >= 0xffb0 && keysym <= 0xffb9:
		r = rune('0' + keysym - 0xffb0)
	case keysym&0xff000000 == 0x01000000:
		r = rune(keysym & 0x00ffffff)
	default:
		return 0, ""
	}
	if r < 0x20 || !utf8.ValidRune(r) {
		return 0, ""
	}
	return sdl.Keycode(unicode.ToLower(r)), string(r)
}

// vncModBit is the SDL modifier a modifier keysym holds, 0 for any other.
func vncModBit(keysym uint32) uint16 {
	switch keysym {
	case 0xffe1:
		return uint16(sdl.KMOD_LSHIFT)
	case 0xffe2:
		return uint16(sdl.KMOD_RSHIFT)
	case 0xffe3:
		return uint16(sdl.KMOD_LCTRL)
	case 0xffe4:
		return uint16(sdl.KMOD_RCTRL)
	case 0xffe9:
		return uint16(sdl.KMOD_LALT)
	case 0xffea:
		return uint16(sdl.KMOD_RALT)
	case 0xffe7, 0xffeb:
		return uint16(sdl.KMOD_LGUI)
	case 0xffe8, 0xffec:
		return uint16(sdl.KMOD_RGUI)
	}
	return 0
}

// vncQueuedText is a padded text event, like sdlevents.go's synthetic
// events: SDL_PushEvent copies a whole SDL_Event from the pointer.
type vncQueuedText struct {
	sdl.TextInputEvent
	_ [syntheticEventPad]byte
}

// vncApplyKey pushes one viewer key as a keyboard's events. Main thread. A
// release for a key no viewer pressed here (it went down in a game) is
// dropped; a press of a key already down is a repeat, as SDL marks one.
func vncApplyKey(down bool, keysym uint32) {
	code, text := vncKeysymKey(keysym)
	if code == 0 {
		return
	}
	if bit := vncModBit(keysym); bit != 0 {
		if down {
			vncKeys.mod |= bit
		} else {
			vncKeys.mod &^= bit
		}
	}
	if vncKeys.held == nil {
		vncKeys.held = make(map[sdl.Keycode]bool)
	}
	repeat := down && vncKeys.held[code]
	if down {
		vncKeys.held[code] = true
	} else {
		if !vncKeys.held[code] {
			return
		}
		delete(vncKeys.held, code)
	}
	vncPushKey(down, code, repeat)
	if down && text != "" && vncKeys.mod&uint16(sdl.KMOD_CTRL|sdl.KMOD_ALT|sdl.KMOD_GUI) == 0 {
		ev := &vncQueuedText{TextInputEvent: sdl.TextInputEvent{Type: sdl.TEXTINPUT, Timestamp: sdl.GetTicks()}}
		copy(ev.Text[:len(ev.Text)-1], text)
		_, _ = syntheticEventPush(ev)
	}
}

// vncPushKey queues one keyboard event with the scancode a real key has and
// the modifiers the viewer holds.
func vncPushKey(down bool, code sdl.Keycode, repeat bool) {
	typ, state := uint32(sdl.KEYUP), uint8(sdl.RELEASED)
	if down {
		typ, state = sdl.KEYDOWN, sdl.PRESSED
	}
	ev := newQueuedKeyboard(typ, code, state)
	ev.Keysym.Scancode = sdl.GetScancodeFromKey(code)
	ev.Keysym.Mod = vncKeys.mod
	if repeat {
		ev.Repeat = 1
	}
	_, _ = syntheticEventPush(ev)
}

// vncReleaseAll lets go of every key and button the viewers hold. Main
// thread.
func vncReleaseAll() {
	for code := range vncKeys.held {
		vncPushKey(false, code, false)
	}
	vncKeys.held = nil
	vncKeys.mod = 0
	if vncKeys.buttons&1 != 0 && vcursor.active {
		vcursor.pressed = false
		vcursorPushButton(false)
	}
	vncKeys.buttons = 0
}

// vncApplyPointer moves JukaHub's pointer to where the viewer's mouse is, in
// the canvas's pixels (the framebuffer is the canvas). Main thread. With no
// pointer up the mouse is ignored, but its buttons are followed, so a press
// made then does not read as one later.
func vncApplyPointer(buttons uint8, x, y int) {
	prev := vncKeys.buttons
	vncKeys.buttons = buttons
	if !vcursor.active {
		return
	}
	lx := clampFloat(float64(x), 0, float64(screenWidth-1))
	ly := clampFloat(float64(y), 0, float64(screenHeight-1))
	if lx != vcursor.x || ly != vcursor.y || buttons != prev {
		vcursorRouse(vcursorClock())
	}
	vcursor.x, vcursor.y, vcursor.placed = lx, ly, true
	vcursor.pressed = buttons&1 != 0
	vcursorPushMotion(false)
	if (buttons^prev)&1 != 0 {
		vcursorPushButton(buttons&1 != 0)
	}
	// Buttons 4 and 5 are the wheel: one press, one notch.
	if buttons&8 != 0 && prev&8 == 0 {
		vcursorPushWheel(0, 1)
	}
	if buttons&16 != 0 && prev&16 == 0 {
		vcursorPushWheel(0, -1)
	}
	vcursorWake()
}

// The pad buttons a viewer's keys press while a game owns the screen.
const (
	vncPadNone = iota
	vncPadUp
	vncPadDown
	vncPadLeft
	vncPadRight
	vncPadA
	vncPadB
	vncPadX
	vncPadY
	vncPadL1
	vncPadR1
	vncPadL2
	vncPadR2
	vncPadSelect
	vncPadStart
	vncPadMenu
	vncPadCount
)

// vncPadFor maps a keysym to the pad button it presses in a game: the
// keyboard twins of JukaHub's screens (arrows, Enter = A, Esc and Backspace
// = B, X, Y, PgUp/PgDn = L1/R1, Home/End and Q/E = L2/R2, F10 = Start), Tab
// for Select and F1 for Menu.
func vncPadFor(keysym uint32) int {
	switch keysym {
	case 0xff52, 0xff97:
		return vncPadUp
	case 0xff54, 0xff99:
		return vncPadDown
	case 0xff51, 0xff96:
		return vncPadLeft
	case 0xff53, 0xff98:
		return vncPadRight
	case 0xff0d, 0xff8d:
		return vncPadA
	case 0xff1b, 0xff08:
		return vncPadB
	case 'x', 'X':
		return vncPadX
	case 'y', 'Y':
		return vncPadY
	case 0xff55:
		return vncPadL1
	case 0xff56:
		return vncPadR1
	case 0xff50, 'q', 'Q':
		return vncPadL2
	case 0xff57, 'e', 'E':
		return vncPadR2
	case 0xff09:
		return vncPadSelect
	case 0xffc7:
		return vncPadStart
	case 0xffbe:
		return vncPadMenu
	}
	return vncPadNone
}
