package main

import (
	"sync"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// sdlEventSize is the number of bytes SDL_PushEvent copies from the pointer it
// is given. Anything smaller is the over-read these helpers exist to prevent.
const sdlEventSize = 56

type eventRecorder struct {
	mu     sync.Mutex
	events []sdl.Event
}

func (r *eventRecorder) push(ev sdl.Event) (bool, error) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	return true, nil
}

func (r *eventRecorder) all() []sdl.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sdl.Event(nil), r.events...)
}

func recordSyntheticEvents(t *testing.T) *eventRecorder {
	t.Helper()
	rec := &eventRecorder{}
	old := syntheticEventPush
	syntheticEventPush = rec.push
	t.Cleanup(func() { syntheticEventPush = old })
	return rec
}

// eventBytes reports how much memory the queued event actually holds.
func eventBytes(ev sdl.Event) uintptr {
	switch e := ev.(type) {
	case *queuedQuitEvent:
		return unsafe.Sizeof(*e)
	case *queuedControllerButtonEvent:
		return unsafe.Sizeof(*e)
	case *queuedKeyboardEvent:
		return unsafe.Sizeof(*e)
	case *queuedMouseMotionEvent:
		return unsafe.Sizeof(*e)
	case *queuedMouseButtonEvent:
		return unsafe.Sizeof(*e)
	}
	return 0
}

// Every kind of synthesized event must be at least one SDL_Event long, or
// SDL_PushEvent reads past the Go allocation.
func TestSyntheticEventsFitSDLEvent(t *testing.T) {
	rec := recordSyntheticEvents(t)

	if err := pushControllerButton(sdl.CONTROLLERBUTTONDOWN, padRepeatWhich, sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.PRESSED); err != nil {
		t.Fatalf("controller push: %v", err)
	}
	if err := pushSyntheticKey(sdl.KEYDOWN, sdl.K_RETURN, sdl.PRESSED); err != nil {
		t.Fatalf("key push: %v", err)
	}
	if err := pushSyntheticMouseMotion(12, 34); err != nil {
		t.Fatalf("mouse motion push: %v", err)
	}
	if err := pushSyntheticMouseButton(sdl.BUTTON_LEFT, sdl.PRESSED, 12, 34); err != nil {
		t.Fatalf("mouse button push: %v", err)
	}
	requeueQuit()

	events := rec.all()
	if len(events) != 5 {
		t.Fatalf("queued %d events, want 5", len(events))
	}
	for i, ev := range events {
		if got := eventBytes(ev); got < sdlEventSize {
			t.Errorf("event %d (%T) is %d bytes, smaller than SDL_Event's %d", i, ev, got, sdlEventSize)
		}
	}
}

// A press must arrive as a press: the first synthetic button events carried
// State 0 (released) on CONTROLLERBUTTONDOWN, so a handler that read the state
// saw the event it was not sent.
func TestSyntheticEventsCarryPressState(t *testing.T) {
	rec := recordSyntheticEvents(t)

	pushControllerButtonDown(sdl.CONTROLLER_BUTTON_DPAD_DOWN)
	pushCompanionKey("a", &Config{})
	pushTapClick(5, 6)

	downs, ups := 0, 0
	mouseDowns, mouseUps := 0, 0
	for _, ev := range rec.all() {
		switch e := ev.(type) {
		case *queuedControllerButtonEvent:
			switch e.Type {
			case sdl.CONTROLLERBUTTONDOWN:
				downs++
				if e.State != sdl.PRESSED {
					t.Errorf("button down carries State %d, want PRESSED", e.State)
				}
			case sdl.CONTROLLERBUTTONUP:
				ups++
				if e.State != sdl.RELEASED {
					t.Errorf("button up carries State %d, want RELEASED", e.State)
				}
			}
		case *queuedKeyboardEvent:
			switch e.Type {
			case sdl.KEYDOWN:
				if e.State != sdl.PRESSED {
					t.Errorf("key down carries State %d, want PRESSED", e.State)
				}
			case sdl.KEYUP:
				if e.State != sdl.RELEASED {
					t.Errorf("key up carries State %d, want RELEASED", e.State)
				}
			}
		case *queuedMouseButtonEvent:
			switch e.State {
			case sdl.PRESSED:
				mouseDowns++
			case sdl.RELEASED:
				mouseUps++
			default:
				t.Errorf("mouse button carries State %d, want PRESSED or RELEASED", e.State)
			}
		}
	}
	// The stick repeat is one press, the phone's A is a press/release pair, and
	// the tap is a mouse press/release on top of its motion event.
	if downs < 2 || ups < 1 {
		t.Errorf("recorded %d button downs and %d ups, want a press and both pairs", downs, ups)
	}
	if mouseDowns != 1 || mouseUps != 1 {
		t.Errorf("recorded %d mouse downs and %d ups, want one of each", mouseDowns, mouseUps)
	}
}

// Every phone label and every key the companion can send goes through the same
// padded path.
func TestCompanionKeysUsePaddedEvents(t *testing.T) {
	rec := recordSyntheticEvents(t)
	for name := range companionControllerButtons {
		pushCompanionKey(name, &Config{})
	}
	for name := range companionKeyboardKeys {
		pushCompanionKey(name, &Config{})
	}
	for i, ev := range rec.all() {
		if got := eventBytes(ev); got < sdlEventSize {
			t.Errorf("companion event %d (%T) is %d bytes, smaller than SDL_Event", i, ev, got)
		}
	}
}

// The queue seam is shared by the stick, the phone and the touch pipeline;
// pushing from several goroutines must stay data-race free under -race.
func TestSyntheticEventPushIsRaceFree(t *testing.T) {
	rec := recordSyntheticEvents(t)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				pushControllerButtonDown(sdl.CONTROLLER_BUTTON_DPAD_RIGHT)
				pushCompanionKey("a", &Config{})
			}
		}()
	}
	wg.Wait()
	if got := len(rec.all()); got == 0 {
		t.Fatal("no events were queued")
	}
}
