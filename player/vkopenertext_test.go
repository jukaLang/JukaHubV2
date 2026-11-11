package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestVkDropOpenerTextArmsOnlyTheOpeningKey(t *testing.T) {
	prevActive, prevOpener := virtualKeyboardActive, browserTextOpener
	t.Cleanup(func() {
		virtualKeyboardActive = prevActive
		browserTextOpener = prevOpener
	})

	key := func(sym sdl.Keycode, scancode sdl.Scancode, mod uint16) *sdl.KeyboardEvent {
		return &sdl.KeyboardEvent{
			Type:   sdl.KEYDOWN,
			Keysym: sdl.Keysym{Sym: sym, Scancode: scancode, Mod: mod},
		}
	}

	// The key did not open a keyboard, so its character belongs to the field
	// that was already up.
	virtualKeyboardActive = false
	browserTextOpener = 0
	vkDropOpenerText(key(sdl.K_x, sdl.SCANCODE_X, 0), false)
	if browserTextOpener != 0 {
		t.Fatalf("a key that did not open a keyboard armed its text: %v", browserTextOpener)
	}

	// A printable key that opened one arms the drop.
	virtualKeyboardActive = true
	vkDropOpenerText(key(sdl.K_x, sdl.SCANCODE_X, 0), false)
	if browserTextOpener != sdl.SCANCODE_X {
		t.Fatalf("opener scancode = %v, want %v", browserTextOpener, sdl.SCANCODE_X)
	}

	// A keyboard that was already open keeps its key's character.
	browserTextOpener = 0
	vkDropOpenerText(key(sdl.K_x, sdl.SCANCODE_X, 0), true)
	if browserTextOpener != 0 {
		t.Fatalf("a key typed into an already open keyboard armed its text: %v", browserTextOpener)
	}

	// Keys that type nothing, and Ctrl/Alt chords, are not printable openers.
	for _, tc := range []struct {
		name string
		e    *sdl.KeyboardEvent
	}{
		{"enter", key(sdl.K_RETURN, sdl.SCANCODE_RETURN, 0)},
		{"escape", key(sdl.K_ESCAPE, sdl.SCANCODE_ESCAPE, 0)},
		{"ctrl+x", key(sdl.K_x, sdl.SCANCODE_X, sdl.KMOD_CTRL)},
		{"alt+x", key(sdl.K_x, sdl.SCANCODE_X, sdl.KMOD_ALT)},
	} {
		browserTextOpener = 0
		vkDropOpenerText(tc.e, false)
		if browserTextOpener != 0 {
			t.Errorf("%s armed a text drop", tc.name)
		}
	}
}
