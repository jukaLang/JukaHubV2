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
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// homeUIFontFile is the bundled UI typeface, resolved like every other asset
// (resolvePath) from the executable's folder.
const (
	homeUIFontFile     = "Inter-Regular.ttf"
	homeUIFontBoldFile = "Inter-SemiBold.ttf"
)

// homeUIFontForRole is the face every UI role resolves to. Legible reading
// text replaces the face with the best system face the machine has
// (legiblefont.go), falling back to the bundled Inter when none is installed;
// next comes the font picked in Settings → Display → Font (uifont.go), when
// its file is there; Bold text then prefers that face's bold cut, or the
// bundled SemiBold. Every role still goes through the normal
// path/cache/fallback chain in loadCachedFont, so a picked file that will not
// open draws in Inter.
func homeUIFontForRole(path string) string {
	if legible := legibleFontFile(); legible != "" {
		return legible
	}
	if chosen := uiFontFile(); chosen != "" {
		return chosen
	}
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
	// size and mod are the file's when it was read, so a file replaced on the
	// card under the same name can be told apart (homeUIFontSourceRefresh).
	size int64
	mod  time.Time
}

// homeUIFontSourceCache maps a resolved path to its bytes. retired holds the
// sources dropped from byPath because their file was replaced or removed: a
// Font opened from them may still sit in a cache until the next refresh, so
// their bytes stay pinned until shutdown.
var homeUIFontSourceCache = struct {
	sync.Mutex
	byPath  map[string]*homeUIFontSource
	retired []*homeUIFontSource
}{byPath: make(map[string]*homeUIFontSource)}

// openHomeUIFont opens one size from the shared in-memory source instead of
// asking SDL_ttf to reopen the font file from the card for every size. A
// picked face much wider or narrower than Inter opens at its scaled size
// (uiFontScaledSize), so rows laid out for Inter still fit.
func openHomeUIFont(path string, size int) (*ttf.Font, error) {
	path = homeUIFontForRole(path)
	return openUIFontFile(path, uiFontScaledSize(path, size))
}

// openUIFontFile opens exactly the file it is given, with no role substitution.
// The fallback to the bundled Inter uses it so a broken or unreadable legible
// face cannot send the open back into the chain that chose it.
func openUIFontFile(path string, size int) (*ttf.Font, error) {
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

// homeUIFontSourceKey is the cache key for path: resolved like every asset
// and made absolute.
func homeUIFontSourceKey(path string) string {
	resolved := resolvePath(path)
	if resolved == "" {
		resolved = homeUIFontFile
	}
	if absolute, err := filepath.Abs(resolved); err == nil {
		resolved = filepath.Clean(absolute)
	}
	return resolved
}

func homeUIFontSourceFor(path string) (*homeUIFontSource, error) {
	resolved := homeUIFontSourceKey(path)

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
	source := &homeUIFontSource{data: data, size: int64(len(data))}
	if st, err := os.Stat(resolved); err == nil {
		source.mod = st.ModTime()
	}
	source.pinner.Pin(&source.data[0])
	homeUIFontSourceCache.byPath[resolved] = source
	return source, nil
}

// homeUIFontSourceRetire forgets path's cached bytes, so the next open reads
// the file again: a downloaded font installed again, or a removed one. The
// bytes stay pinned (retired) for any Font still opened from them.
func homeUIFontSourceRetire(path string) {
	key := homeUIFontSourceKey(path)
	homeUIFontSourceCache.Lock()
	if source := homeUIFontSourceCache.byPath[key]; source != nil {
		homeUIFontSourceCache.retired = append(homeUIFontSourceCache.retired, source)
		delete(homeUIFontSourceCache.byPath, key)
	}
	homeUIFontSourceCache.Unlock()
}

// homeUIFontSourceRefresh retires path's cached bytes only when the file on
// the card is no longer the one read (another size or time, or gone): a face
// applied again keeps its bytes, a face replaced under the same name is read
// afresh. Retiring on every apply would pin one more copy per Left/Right step.
func homeUIFontSourceRefresh(path string) {
	key := homeUIFontSourceKey(path)
	homeUIFontSourceCache.Lock()
	source := homeUIFontSourceCache.byPath[key]
	homeUIFontSourceCache.Unlock()
	if source == nil {
		return
	}
	if st, err := os.Stat(key); err == nil && st.Size() == source.size && st.ModTime().Equal(source.mod) {
		return
	}
	homeUIFontSourceRetire(path)
}

// homeUIInterFonts are the bundled Inter at the sizes the JukaHub wordmark is
// drawn at. Render thread only; kept for the run (font refreshes leave them
// alone) and closed at shutdown.
var homeUIInterFonts = map[int]*ttf.Font{}

// homeUIInterFont is the bundled Inter at size whatever face the interface
// uses: the JukaHub wordmark is the brand and stays Inter, on the boot logo
// and in the boot animation alike, so the two pictures still match.
func homeUIInterFont(size int) *ttf.Font {
	if font := homeUIInterFonts[size]; font != nil {
		return font
	}
	font, err := openUIFontFile(homeUIFontFile, size)
	if err != nil {
		logWarn("core", "Error loading the wordmark face at %d px: %v", size, err)
		return nil
	}
	registerFontPath(font, homeUIFontFile)
	homeUIInterFonts[size] = font
	return font
}

// closeHomeUIFontSources releases pinned font bytes after the cached fonts are
// closed during shutdown. Until then, every SDL_ttf RWops can safely read them.
func closeHomeUIFontSources() {
	for size, font := range homeUIInterFonts {
		font.Close()
		delete(homeUIInterFonts, size)
	}
	homeUIFontSourceCache.Lock()
	for path, source := range homeUIFontSourceCache.byPath {
		source.pinner.Unpin()
		source.data = nil
		delete(homeUIFontSourceCache.byPath, path)
	}
	for _, source := range homeUIFontSourceCache.retired {
		source.pinner.Unpin()
		source.data = nil
	}
	homeUIFontSourceCache.retired = nil
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
