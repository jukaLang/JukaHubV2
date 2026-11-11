package main

// home_fonts.go — the one UI typeface.
//
// JukaHub draws every label in the bundled Inter (Inter-Regular.ttf, shipped
// next to the executable). The font paths used to fall back through DejaVu,
// Arial and Roboto, none of which JukaHub ships: on a card where Inter failed
// to open, the UI quietly switched to whatever face the system path happened
// to offer, a different one per device, and a config's per-role "fonts" map
// could point a role at any file at all. Home, the status bar and every screen
// that resolves its type through getCachedFont now render in Inter only.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// homeUIFontFile is the bundled UI typeface, resolved like every other asset
// (resolvePath) from the executable's folder.
const (
	homeUIFontFile     = "Inter-Regular.ttf"
	homeUIFontBoldFile = "Inter-SemiBold.ttf"
)

// homeUIFontForRole is the face every UI role resolves to. Bold text replaces
// the configured Inter cut for every role at once, while still going through
// the normal path/cache/fallback chain in loadCachedFont.
func homeUIFontForRole(path string) string {
	if a11y.boldText {
		return homeUIFontBoldFile
	}
	return path
}

// homeUIFontSource keeps one immutable font file in memory for all point sizes.
// SDL_ttf's RWops retains a pointer into data until its Font is closed, so the
// backing array is pinned and owned here for the full lifetime of every font.
type homeUIFontSource struct {
	data   []byte
	pinner runtime.Pinner
}

var homeUIFontSourceCache = struct {
	sync.Mutex
	byPath map[string]*homeUIFontSource
}{byPath: make(map[string]*homeUIFontSource)}

// openHomeUIFont opens one size from the shared in-memory source instead of
// asking SDL_ttf to reopen the font file from the card for every size.
func openHomeUIFont(path string, size int) (*ttf.Font, error) {
	path = homeUIFontForRole(path)
	source, err := homeUIFontSourceFor(path)
	if err != nil {
		return nil, err
	}
	rw, err := sdl.RWFromMem(source.data)
	if err != nil {
		return nil, err
	}
	font, err := ttf.OpenFontRW(rw, 1, size)
	runtime.KeepAlive(source)
	return font, err
}

func homeUIFontSourceFor(path string) (*homeUIFontSource, error) {
	resolved := resolvePath(path)
	if resolved == "" {
		resolved = homeUIFontFile
	}
	if absolute, err := filepath.Abs(resolved); err == nil {
		resolved = filepath.Clean(absolute)
	}

	homeUIFontSourceCache.Lock()
	defer homeUIFontSourceCache.Unlock()
	if source := homeUIFontSourceCache.byPath[resolved]; source != nil {
		return source, nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("font file is empty")
	}
	source := &homeUIFontSource{data: data}
	source.pinner.Pin(&source.data[0])
	homeUIFontSourceCache.byPath[resolved] = source
	return source, nil
}

// closeHomeUIFontSources releases pinned font bytes after the cached fonts are
// closed during shutdown. Until then, every SDL_ttf RWops can safely read them.
func closeHomeUIFontSources() {
	homeUIFontSourceCache.Lock()
	for path, source := range homeUIFontSourceCache.byPath {
		source.pinner.Unpin()
		source.data = nil
		delete(homeUIFontSourceCache.byPath, path)
	}
	homeUIFontSourceCache.Unlock()
}

// homeUIFontPath maps a configured font path to the face the UI may draw
// with. An Inter file is kept — a bundled weight such as Inter-Bold.ttf is
// still Inter — and anything else (DejaVu, Arial, Roboto, a system font) is
// replaced by the bundled Inter, so a stale config or theme cannot bring
// another typeface back onto Home or the status bar.
func homeUIFontPath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return homeUIFontFile
	}
	base := p
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if homeIsInterFile(base) {
		return p
	}
	return homeUIFontFile
}

// homeIsInterFile reports whether a font file name is a cut of Inter: Inter.ttf,
// Inter-Bold.ttf, Inter_18pt-Regular.ttf, InterVariable.ttf. A name that only
// starts with the same letters ("Interstate.ttf") is a different typeface.
func homeIsInterFile(name string) bool {
	base := strings.ToLower(strings.TrimSpace(name))
	ext := ""
	switch {
	case strings.HasSuffix(base, ".ttf"):
		ext = ".ttf"
	case strings.HasSuffix(base, ".otf"):
		ext = ".otf"
	default:
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	if stem == "inter" {
		return true
	}
	for _, prefix := range []string{"inter-", "inter_", "inter ", "intervariable", "interdisplay"} {
		if strings.HasPrefix(stem, prefix) {
			return true
		}
	}
	return false
}
