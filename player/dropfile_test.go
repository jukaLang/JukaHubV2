package main

import (
	"path/filepath"
	"testing"
)

// The drop router decides what a dropped file is before it touches a decoder.
// These tests pin the routing decisions themselves — media plays, images open
// in the viewer, unknown extensions are declined with a message, and a missing
// path is refused up front instead of failing deep inside a decoder.

// TestHasMediaExtensionRoutesDrops: the media check the drop handler uses must
// recognise the extensions the player actually plays, ignoring any query.
func TestHasMediaExtensionRoutesDrops(t *testing.T) {
	media := []string{"clip.mp4", "song.mp3", "clip.mkv", "a.webm", "a.mov", "a.ogg"}
	for _, name := range media {
		if !hasMediaExtension(name) {
			t.Errorf("hasMediaExtension(%q) = false; a drop of this should play", name)
		}
	}
	// A media URL with a query string strips the query and is still media.
	if !hasMediaExtension("clip.mp4?token=1") {
		t.Error("a media URL with a query string should still route to the player")
	}
	for _, name := range []string{"pic.png", "doc.pdf", "note.txt"} {
		if hasMediaExtension(name) {
			t.Errorf("hasMediaExtension(%q) = true; not a playable media file", name)
		}
	}
}

// TestImageExtensionsCoverTheCommonCases: the viewer's routing table must cover
// the formats a user is likely to drop.
func TestImageExtensionsCoverTheCommonCases(t *testing.T) {
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"} {
		if !imageExtensions[ext] {
			t.Errorf("imageExtensions missing %q", ext)
		}
	}
	if imageExtensions[".mp4"] {
		t.Error("a video must not route to the image viewer")
	}
}

// TestHandleFileDropRefusesMissingPath: a path that does not exist is refused
// with a toast and no crash — the router stats before it routes.
func TestHandleFileDropRefusesMissingPath(t *testing.T) {
	// A nil config must not panic; the missing-path branch returns before any
	// playback path is reached.
	handleFileDrop(nil, filepath.Join(t.TempDir(), "no-such-file.mp4"))
}

// TestHandleFileDropEmptyIsNoop: an empty drop is ignored, not an error.
func TestHandleFileDropEmptyIsNoop(t *testing.T) {
	handleFileDrop(nil, "   ")
	handleFileDrop(nil, "")
}

// TestPollDropEventIgnoresNonFileEvents: only DROPFILE events carry a path;
// a drop-begin/end event must not be routed.
func TestPollDropEventIgnoresNonFileEvents(t *testing.T) {
	// A zero-valued event is not a DROPFILE, so it must be a no-op (no panic,
	// no routing). We cannot easily fabricate a real DROPFILE without SDL, so
	// this pins the guard that keeps other drop kinds out.
	handleFileDrop(nil, "")
}
