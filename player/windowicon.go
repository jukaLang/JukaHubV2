package main

import (
	_ "embed"
	"errors"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// windowIconPNG is the app artwork, embedded so the window icon always has a
// source. The Windows executable also carries the same image compiled in as a
// resource (see JukaHub.rc / mkwinres.sh), which is what Explorer and unpinned
// taskbar entries read; this one is for the running window.
//
//go:embed JukaPlayer.png
var windowIconPNG []byte

// applyWindowIcon gives the window its own icon for the title bar, Alt-Tab and
// the taskbar. Windows draws those from the window icon once one is set and
// only otherwise falls back to the executable's icon, so a windowed build can
// look generic even when the .exe itself is branded.
//
// Failure is not fatal: a missing or undecodable icon leaves the default in
// place, which is exactly what happened before this existed.
func applyWindowIcon(window *sdl.Window) {
	if window == nil || !IsWindows() {
		return
	}
	surface, err := windowIconSurface()
	if err != nil {
		Log().Warn("window icon skipped", "err", err)
		return
	}
	defer surface.Free()
	window.SetIcon(surface)
}

// windowIconSurface decodes the embedded artwork. It is split out from
// applyWindowIcon so the decode path can be tested without a real window:
// SDL_image reads the PNG straight out of the binary, which is also what makes
// the icon work when the executable is copied somewhere without its assets.
func windowIconSurface() (*sdl.Surface, error) {
	if len(windowIconPNG) == 0 {
		return nil, errors.New("no embedded icon artwork")
	}
	rw, err := sdl.RWFromMem(windowIconPNG)
	if err != nil {
		return nil, err
	}
	defer rw.Close()
	// SDL_image copies the pixels into the surface, so the source can close.
	return img.LoadRW(rw, false)
}
