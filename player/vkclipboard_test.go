package main

// Tests for the clipboard history overlay (clipboard.go): the list the search
// keeps is the list Enter, A and X act on, and copying an entry back moves it
// to the top of the history instead of adding a second copy.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// withClipOverlay saves the overlay's state and fills the history with texts,
// oldest first, through the real copy path.
func withClipOverlay(t *testing.T, texts ...string) *clipboardSpy {
	t.Helper()
	spy := &clipboardSpy{}
	installClipboardSpy(t, spy)
	resetClipboardHistory(t)
	prevOpen, prevBuf, prevIdx := clipSearchOpen, clipSearchBuf, clipCursorIdx
	t.Cleanup(func() {
		clipSearchOpen, clipSearchBuf, clipCursorIdx = prevOpen, prevBuf, prevIdx
		vkClipGen++
	})
	for _, s := range texts {
		if err := copyTextToClipboard(s, "Test"); err != nil {
			t.Fatalf("copy %q: %v", s, err)
		}
	}
	return spy
}

func TestVKClipFilteredCopyTakesTheShownEntry(t *testing.T) {
	spy := withClipOverlay(t, "alpha", "beta link", "gamma", "delta link")
	key := func(sym sdl.Keycode) {
		handleClipboardInput(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}})
	}

	// Newest first, the search keeps "delta link" and "beta link".
	clipSearchOpen, clipSearchBuf, clipCursorIdx = true, "link", 0
	if got := len(vkClipVisible()); got != 2 {
		t.Fatalf("the search kept %d entries, want 2", got)
	}
	key(sdl.K_DOWN)
	key(sdl.K_RETURN)
	if spy.set != "beta link" {
		t.Fatalf("Enter copied %q, want the second match \"beta link\"", spy.set)
	}
	if clipSearchOpen || clipSearchBuf != "" {
		t.Fatal("copying an entry should close the overlay and forget the search")
	}
	if len(clipHistory) != 4 || clipHistory[0].text != "beta link" {
		t.Fatalf("copying back should move the entry to the top, got %+v", clipHistory)
	}

	// X on the pad removes the highlighted match, not its unfiltered neighbour.
	clipSearchOpen, clipSearchBuf, clipCursorIdx = true, "gamma", 0
	ctrlClipboard(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_X})
	if len(clipHistory) != 3 {
		t.Fatalf("X left %d entries, want 3", len(clipHistory))
	}
	for _, e := range clipHistory {
		if e.text == "gamma" {
			t.Fatal("X did not remove the highlighted match")
		}
	}

	// A search with no match copies nothing.
	clipSearchBuf, clipCursorIdx = "zzz", 0
	spy.set = ""
	ctrlClipboard(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A})
	if spy.set != "" || !clipSearchOpen {
		t.Fatalf("A on an empty result copied %q", spy.set)
	}
}

func TestVKClipLinesPreview(t *testing.T) {
	cases := []struct{ text, first, rest string }{
		{"one line", "one line", ""},
		{"first\nsecond\nthird", "first", "second"},
		{"first\r\n\n  second  ", "first", "second"},
	}
	for _, tc := range cases {
		first, rest := vkClipLines(tc.text)
		if first != tc.first || rest != tc.rest {
			t.Errorf("vkClipLines(%q) = %q, %q; want %q, %q", tc.text, first, rest, tc.first, tc.rest)
		}
	}
}
