package main

// mousehide.go — a pointer nobody uses goes out of sight after ten seconds.
//
// Juka: "Mouse should auto hide when not used after 10 seconds." Every pointer
// JukaHub has follows the same clock, pointerIdleHide:
//
//	right-stick pointer  put away (virtualcursor.go); the right stick brings
//	                     it back where it was
//	mouse mode           the pointer dozes: it is not drawn but mouse mode
//	                     stays on, and the next move, A, L2 or R2 wakes it
//	                     where it was (vcursor_mode.go, vcursorRouse)
//	a real mouse         the SDL cursor is hidden — a USB or Bluetooth mouse
//	                     on a handheld, the system pointer over the PC
//	                     window — and shown again on its next move or button
//
// The real mouse is watched here, from vcursorTick, by polling SDL's own mouse
// state once a frame. The virtual pointer's queued events never move that
// state (vcursorMouseState), so only a real mouse counts, and the event loop
// needs no hook. The poll sees moves and buttons, not the wheel: scrolling with
// the wheel neither keeps the cursor up nor brings it back. Reading a long
// page with the wheel is when an arrow in the middle of the text is most in
// the way, and the next move shows it again where it was. A held button keeps
// it up (a drag), and a click while it is hidden shows it and clicks as usual:
// a desktop user expects that, unlike the pad's dozing pointer, whose A could
// otherwise press something nobody can see.
//
// Nothing else in JukaHub hides, warps or captures the system cursor (no
// relative mouse mode; the browser and HTML5 games read plain motion), so this
// is the only caller of sdl.ShowCursor, and it calls it only on a change. In a
// game the program draws its own pointer — Mouse in games and apps hands it a
// uinput mouse (vcursor_bridge_linux.go) — and JukaHub draws nothing there.

import (
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// pointerIdleHide is how long a pointer may go unused before it is hidden: the
// right-stick pointer, mouse mode's pointer and a real mouse's cursor alike.
const pointerIdleHide = 10 * time.Second

// mouseHide follows the real mouse; main thread only (vcursorTick).
var mouseHide struct {
	x, y    int32     // SDL's mouse position at the last poll, window pixels
	buttons uint32    // and the buttons it held
	polled  bool      // x, y and buttons hold a reading
	used    time.Time // last real move or button
	hidden  bool      // sdl.ShowCursor(sdl.DISABLE) is in force
}

// mouseHideTick polls the real mouse once a frame: a move or a button since the
// last poll shows the cursor and starts the clock again, a held button keeps
// it up, and pointerIdleHide with neither hides it.
func mouseHideTick(now time.Time) {
	x, y, buttons := sdl.GetMouseState()
	if !mouseHide.polled {
		// The first poll is where the mouse already was, not a move.
		mouseHide.x, mouseHide.y, mouseHide.buttons = x, y, buttons
		mouseHide.polled, mouseHide.used = true, now
		return
	}
	if x != mouseHide.x || y != mouseHide.y || buttons != mouseHide.buttons || buttons != 0 {
		mouseHide.x, mouseHide.y, mouseHide.buttons = x, y, buttons
		mouseHide.used = now
		mouseHideSet(false)
		return
	}
	if !mouseHide.hidden && now.Sub(mouseHide.used) > pointerIdleHide {
		mouseHideSet(true)
	}
}

// mouseHideSet hides or shows the SDL cursor, calling SDL only on a change.
func mouseHideSet(hidden bool) {
	if mouseHide.hidden == hidden {
		return
	}
	mouseHide.hidden = hidden
	toggle := sdl.ENABLE
	if hidden {
		toggle = sdl.DISABLE
	}
	if _, err := sdl.ShowCursor(toggle); err != nil {
		logDebug("MOUSE", "show cursor %d: %v", toggle, err)
	}
}
