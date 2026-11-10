package main

// vkopenertext.go — the character a keyboard-opening key types.
//
// Text input is always on (main.go starts it once and never stops it), so a
// printable key arrives twice: as a key press, and right behind it as the
// character it types. When the press opens an on-screen keyboard — X for a
// playlist name in the Audio panel, A for a podcast feed, Space on a row whose
// action asks for text — that character landed in the field the keyboard had
// just opened: "Playlist 3" became "Playlist 3x", and a feed address started
// with "a", so the subscription failed. The browser had solved this for its
// own fields (browserDropKeyText); the panels drop the character the same way
// through here.

import "github.com/veandco/go-sdl2/sdl"

// vkDropOpenerText drops the character e typed when e is the key that just
// opened the on-screen keyboard. wasOpen is whether a keyboard was already up
// before the key was handled, so only a key that opened one loses its
// character. A key that types nothing (Enter, Escape, the arrows) leaves the
// queue alone, or the next character typed would be the one lost; so does a
// Ctrl/Alt chord or a synthetic key (browserDropKeyText checks those). Main
// thread.
func vkDropOpenerText(e *sdl.KeyboardEvent, wasOpen bool) {
	if e == nil || wasOpen || !virtualKeyboardActive {
		return
	}
	if sym := e.Keysym.Sym; sym < sdl.K_SPACE || sym > sdl.K_z {
		return
	}
	browserDropKeyText(e)
}
