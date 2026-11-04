package main

// sdlevents.go — putting events back on the SDL queue safely.

import "github.com/veandco/go-sdl2/sdl"

// queuedQuitEvent is a QuitEvent padded to the size of SDL_Event. SDL_PushEvent
// copies a whole SDL_Event from the pointer it is handed, so a bare QuitEvent
// would have it read past the Go allocation (and the race build's pointer
// checks reject that conversion outright).
type queuedQuitEvent struct {
	sdl.QuitEvent
	_ [64]byte
}

// requeueQuit puts a quit request back on the SDL queue for the main loop. A
// modal loop that drains the queue must not push the event value it polled:
// go-sdl2 hands out a pointer into one shared C event buffer, which the next
// PollEvent overwrites, so pushing it after the drain re-queued whatever event
// came last instead of the quit.
func requeueQuit() {
	sdl.PushEvent(&queuedQuitEvent{QuitEvent: sdl.QuitEvent{Type: sdl.QUIT, Timestamp: sdl.GetTicks()}})
}
