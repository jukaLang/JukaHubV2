package main

// dropfile.go — drag-and-drop of a file onto the window (desktop).
//
// On Windows (and Linux desktops) dropping a video, audio file or image onto
// the JukaHub window should play or show it, the same as picking it from Files.
// The handheld has no pointer to drag with, so this path never fires there; it
// exists for the desktop build, where a user triaging a downloaded clip expects
// "drag it in" to work.
//
// SDL delivers the dropped path in a DropEvent with a heap string that must be
// freed with sdl.FreeWV. The handler decides what the file is and routes it to
// the existing open paths rather than reimplementing playback.

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// imageExtensions are the files the image viewer opens. Kept small and explicit:
// a drop is a deliberate action, and silently opening an unknown binary in a
// viewer is worse than a toast that says it is not supported.
var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".bmp": true,
	".gif": true, ".webp": true,
}

// handleFileDrop routes one dropped path to the player, the image viewer or the
// music player. It is safe to call from the event loop; heavy work (decoding,
// network) is handed off to the existing background paths.
func handleFileDrop(config *Config, path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	// The dropped path is absolute on every platform SDL reports one. Refuse a
	// path that does not exist rather than failing deep inside a decoder.
	info, err := os.Stat(path)
	if err != nil {
		log.Printf("[drop] refused %q: %v", filepath.Base(path), err)
		showToast("Could not open the dropped file", ToastError())
		return
	}
	if info.IsDir() {
		// A dropped folder is a library root: hand it to the file explorer so
		// the user can browse it, rather than trying to "play" a directory.
		showToast("Dropped a folder — open Files to browse it", ToastInfo())
		return
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case hasMediaExtension(path):
		// Video and audio play through the embedded player, the same path the
		// Files scene uses for a media file.
		log.Printf("[drop] playing %s", filepath.Base(path))
		playStream(config, path)
	case imageExtensions[ext]:
		log.Printf("[drop] showing %s", filepath.Base(path))
		openImageAtPath(config, path)
	default:
		// Anything else is not something the shell can open. Say so instead of
		// silently doing nothing, which reads as "the drop was ignored".
		showToast("JukaHub cannot open ."+strings.TrimPrefix(ext, ".")+" files", ToastInfo())
	}
}

// openImageAtPath shows a dropped image in the image viewer. The viewer owns
// its own scene setup; this is the same entry the Files scene uses.
func openImageAtPath(config *Config, path string) {
	if config == nil {
		return
	}
	openImageViewer(config, path)
}

// pollDropEvent is called from the event loop for a sdl.DropEvent. The go-sdl2
// binding copies the dropped path into a Go string and frees the SDL buffer
// itself, so there is nothing to release here — only the routing decision.
func pollDropEvent(config *Config, e *sdl.DropEvent) {
	if e.Type != sdl.DROPFILE {
		return
	}
	handleFileDrop(config, e.File)
}
