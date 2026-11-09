package main

// sdlevents.go — putting events back on the SDL queue safely.

import "github.com/veandco/go-sdl2/sdl"

// syntheticEventPad pads each synthesized SDL event past the size of
// SDL_Event. SDL_PushEvent copies a whole SDL_Event (56 bytes) from the pointer
// it is handed, so pushing a bare Go event struct would have SDL read past the
// allocation — harmless in a normal build, fatal under the race detector's
// checkptr, which rejects the conversion outright. Every synthetic event goes
// through the constructors below.
const syntheticEventPad = 64

// syntheticEventPush is the queue the helpers below write into. Tests replace
// it to inspect exactly what would be queued; production always uses SDL.
var syntheticEventPush = sdl.PushEvent

type queuedQuitEvent struct {
	sdl.QuitEvent
	_ [syntheticEventPad]byte
}

type queuedControllerButtonEvent struct {
	sdl.ControllerButtonEvent
	_ [syntheticEventPad]byte
}

type queuedKeyboardEvent struct {
	sdl.KeyboardEvent
	_ [syntheticEventPad]byte
}

type queuedMouseMotionEvent struct {
	sdl.MouseMotionEvent
	_ [syntheticEventPad]byte
}

type queuedMouseButtonEvent struct {
	sdl.MouseButtonEvent
	_ [syntheticEventPad]byte
}

// newQueuedControllerButton builds one padded controller event. state is
// sdl.PRESSED for a down event and sdl.RELEASED for an up event: the first
// pushed button events carried State 0 (released) on a press, so a handler that
// read the state saw the opposite of the event type.
func newQueuedControllerButton(eventType uint32, which sdl.JoystickID, button, state uint8) *queuedControllerButtonEvent {
	return &queuedControllerButtonEvent{ControllerButtonEvent: sdl.ControllerButtonEvent{
		Type:      eventType,
		Timestamp: sdl.GetTicks(),
		Which:     which,
		Button:    button,
		State:     state,
	}}
}

func pushControllerButton(eventType uint32, which sdl.JoystickID, button, state uint8) error {
	_, err := syntheticEventPush(newQueuedControllerButton(eventType, which, button, state))
	return err
}

// newQueuedKeyboard builds one padded keyboard event with its state set.
func newQueuedKeyboard(eventType uint32, key sdl.Keycode, state uint8) *queuedKeyboardEvent {
	return &queuedKeyboardEvent{KeyboardEvent: sdl.KeyboardEvent{
		Type:      eventType,
		Timestamp: sdl.GetTicks(),
		State:     state,
		Keysym:    sdl.Keysym{Sym: key},
	}}
}

func pushSyntheticKey(eventType uint32, key sdl.Keycode, state uint8) error {
	_, err := syntheticEventPush(newQueuedKeyboard(eventType, key, state))
	return err
}

func pushSyntheticMouseMotion(x, y int32) error {
	_, err := syntheticEventPush(&queuedMouseMotionEvent{MouseMotionEvent: sdl.MouseMotionEvent{
		Type: sdl.MOUSEMOTION,
		X:    x,
		Y:    y,
	}})
	return err
}

func pushSyntheticMouseButton(button uint8, state uint8, x, y int32) error {
	_, err := syntheticEventPush(&queuedMouseButtonEvent{MouseButtonEvent: sdl.MouseButtonEvent{
		Type:   sdl.MOUSEBUTTONDOWN,
		Button: button,
		State:  state,
		Clicks: 1,
		X:      x,
		Y:      y,
	}})
	return err
}

// requeueQuit puts a quit request back on the SDL queue for the main loop. A
// modal loop that drains the queue must not push the event value it polled:
// go-sdl2 hands out a pointer into one shared C event buffer, which the next
// PollEvent overwrites, so pushing it after the drain re-queued whatever event
// came last instead of the quit.
func requeueQuit() {
	_, _ = syntheticEventPush(&queuedQuitEvent{QuitEvent: sdl.QuitEvent{Type: sdl.QUIT, Timestamp: sdl.GetTicks()}})
}
