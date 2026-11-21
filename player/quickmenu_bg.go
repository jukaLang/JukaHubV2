package main

// quickmenu_bg.go — the Quick menu's full-screen pictures (quickmenu.go).
//
// NextUI themes give each Quick menu entry a picture: <card>/.media/
// quick_<Name>.png, Name one of Collections, Games, Poweroff, Reboot,
// Settings, Sleep, Tools, Wifi (Recents too here, which NextUI draws from the
// game's own art). JukaHub reads the same files, so a NextUI theme's pictures
// work as they are, and also <data>/quickmenu/quick_<Name>.png (or .jpg) for
// pictures that belong to JukaHub alone; the card's .media comes first. The
// focused entry's picture fills the screen; an entry without one keeps
// JukaHub's own backdrop.
//
// Nothing here touches the disk on the render thread: a visit's start looks
// for the files on a goroutine, a picture is decoded on another (under the
// shared decode permit, scaled to cover the screen, never enlarged) the first
// time its entry is focused, and uploaded on the next frame. The last few
// stay as textures while the menu is up; leaving the screen frees them. While
// a picture is still decoding the one shown last stays, so moving the focus
// never flashes the plain backdrop between two pictures.

import (
	"context"
	"errors"
	"image"
	"path/filepath"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// quickMenuBgKeep is how many pictures stay uploaded during a visit.
const quickMenuBgKeep = 4

// errQuickMenuBgPanic is a decoder that panicked on a damaged file.
var errQuickMenuBgPanic = errors.New("the picture could not be decoded")

// quickMenuBgThemes are the names a picture is looked for under.
var quickMenuBgThemes = []string{
	"Recents", "Collections", "Games", "Tools", "Wifi", "Sleep", "Reboot", "Poweroff", "Settings",
}

// quickMenuBg is the visit's pictures: what the goroutines found and decoded
// (under mu) and the render thread's textures.
var quickMenuBg struct {
	mu      sync.Mutex
	gen     uint64
	found   bool
	paths   map[string]string
	pending string
	done    map[string]*image.NRGBA
	failed  map[string]bool

	// Render thread.
	owner     *sdl.Renderer
	tex       map[string]*sdl.Texture
	order     []string
	lastTheme string
}

// quickMenuBgDirs are the folders searched, in order: the card's .media,
// then JukaHub's own data folder.
func quickMenuBgDirs() []string {
	var dirs []string
	if root := strings.TrimSpace(bkupCardRoot()); root != "" {
		dirs = append(dirs, filepath.Join(root, ".media"))
	}
	if data, err := P().DataDir(); err == nil && strings.TrimSpace(data) != "" {
		dirs = append(dirs, filepath.Join(data, "quickmenu"))
	}
	return dirs
}

// quickMenuBgFind looks for every entry's picture. Its own goroutine.
func quickMenuBgFind(gen uint64) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("quickmenu", "looking for the pictures failed: %v", r)
		}
	}()
	paths := map[string]string{}
	dirs := quickMenuBgDirs()
	for _, theme := range quickMenuBgThemes {
		for _, name := range []string{"quick_" + theme, "quick_" + strings.ToLower(theme)} {
			for _, dir := range dirs {
				for _, ext := range []string{".png", ".jpg", ".jpeg"} {
					p := filepath.Join(dir, name+ext)
					if paths[theme] == "" && regularFile(p) {
						paths[theme] = p
					}
				}
			}
		}
	}
	quickMenuBg.mu.Lock()
	defer quickMenuBg.mu.Unlock()
	if quickMenuBg.gen != gen {
		return
	}
	quickMenuBg.paths, quickMenuBg.found = paths, true
	if len(paths) > 0 {
		logDebug("quickmenu", "%d theme pictures found", len(paths))
	}
}

// quickMenuBgStart begins a visit: the search runs again (a theme copied in
// meanwhile shows), and the visit's pictures go when the screen is left.
// Render thread.
func quickMenuBgStart() {
	quickMenuBgDrop()
	quickMenuBg.mu.Lock()
	quickMenuBg.gen++
	gen := quickMenuBg.gen
	quickMenuBg.found, quickMenuBg.paths, quickMenuBg.pending = false, nil, ""
	quickMenuBg.done, quickMenuBg.failed = map[string]*image.NRGBA{}, map[string]bool{}
	quickMenuBg.mu.Unlock()
	go quickMenuBgFind(gen)
	go quickMenuBgWatchLeave(sceneWorkContext(), gen)
}

// quickMenuBgWatchLeave frees the visit's pictures once the screen is left,
// after the fade (as the game switcher's gswWatchLeave).
func quickMenuBgWatchLeave(ctx context.Context, gen uint64) {
	<-ctx.Done()
	var drop func()
	drop = func() {
		quickMenuBg.mu.Lock()
		stale := quickMenuBg.gen != gen
		quickMenuBg.mu.Unlock()
		if stale {
			return // a newer visit owns the pictures now
		}
		if quickMenuOnScreen(appConfig) && transitionPhase != "none" {
			tbxPost(drop) // still fading out
			return
		}
		if !quickMenuOnScreen(appConfig) {
			quickMenuBg.mu.Lock()
			quickMenuBg.gen++ // a decode still running is dropped
			quickMenuBg.paths, quickMenuBg.done, quickMenuBg.pending = nil, nil, ""
			quickMenuBg.mu.Unlock()
			quickMenuBgDrop()
		}
	}
	tbxPost(drop)
}

// quickMenuBgDrop frees the textures. Render thread.
func quickMenuBgDrop() {
	for _, t := range quickMenuBg.tex {
		if t != nil {
			_ = t.Destroy()
		}
	}
	quickMenuBg.tex, quickMenuBg.order, quickMenuBg.lastTheme = nil, nil, ""
}

// quickMenuBgTexture is the picture for theme, or nil for none. While it is
// still being found or decoded the picture shown last stands in. Render
// thread.
func quickMenuBgTexture(renderer *sdl.Renderer, theme string) *sdl.Texture {
	if renderer == nil || theme == "" {
		return nil
	}
	if quickMenuBg.owner != renderer {
		// Textures belong to their renderer; a new one starts empty (the old
		// ones went with it, so they are dropped, not destroyed).
		quickMenuBg.owner, quickMenuBg.tex, quickMenuBg.order, quickMenuBg.lastTheme = renderer, nil, nil, ""
	}
	if quickMenuBg.tex == nil {
		quickMenuBg.tex = map[string]*sdl.Texture{}
	}
	if t, ok := quickMenuBg.tex[theme]; ok {
		if t != nil {
			quickMenuBg.lastTheme = theme
		}
		return t
	}
	quickMenuBg.mu.Lock()
	if !quickMenuBg.found {
		quickMenuBg.mu.Unlock()
		requestFrameEvery(50)
		return quickMenuBgLast()
	}
	path := quickMenuBg.paths[theme]
	pic, ready := quickMenuBg.done[theme]
	if ready {
		delete(quickMenuBg.done, theme)
	}
	failed := quickMenuBg.failed[theme]
	busy := quickMenuBg.pending != ""
	start := path != "" && !ready && !failed && !busy
	gen := quickMenuBg.gen
	if start {
		quickMenuBg.pending = theme
	}
	quickMenuBg.mu.Unlock()
	switch {
	case path == "" || failed:
		quickMenuBgRemember(theme, nil)
		return nil
	case ready:
		t := imgvTexture(renderer, pic)
		quickMenuBgRemember(theme, t)
		if t != nil {
			quickMenuBg.lastTheme = theme
		}
		return t
	}
	if start {
		go quickMenuBgDecode(gen, theme, path, int(screenWidth), int(screenHeight))
	}
	requestFrameEvery(50)
	return quickMenuBgLast()
}

// quickMenuBgLast is the picture shown last, while the next one decodes.
func quickMenuBgLast() *sdl.Texture {
	if quickMenuBg.lastTheme == "" {
		return nil
	}
	return quickMenuBg.tex[quickMenuBg.lastTheme]
}

// quickMenuBgRemember keeps t (nil: no picture for theme) and destroys the
// oldest picture past quickMenuBgKeep. The picture on show is never the one
// destroyed: it was just used, so it is the newest. Render thread.
func quickMenuBgRemember(theme string, t *sdl.Texture) {
	quickMenuBg.tex[theme] = t
	if t == nil {
		return
	}
	quickMenuBg.order = append(quickMenuBg.order, theme)
	for len(quickMenuBg.order) > quickMenuBgKeep {
		oldest := quickMenuBg.order[0]
		quickMenuBg.order = quickMenuBg.order[1:]
		if old := quickMenuBg.tex[oldest]; old != nil {
			_ = old.Destroy()
		}
		delete(quickMenuBg.tex, oldest)
		if quickMenuBg.lastTheme == oldest {
			quickMenuBg.lastTheme = ""
		}
	}
}

// quickMenuBgDecode decodes one picture for the visit gen, to cover a sw×sh
// screen. Its own goroutine.
func quickMenuBgDecode(gen uint64, theme, path string, sw, sh int) {
	pic, err := quickMenuBgLoad(path, sw, sh)
	quickMenuBg.mu.Lock()
	defer quickMenuBg.mu.Unlock()
	if quickMenuBg.gen != gen {
		return
	}
	quickMenuBg.pending = ""
	if err != nil || pic == nil {
		logDebug("quickmenu", "cannot show %s: %v", path, err)
		quickMenuBg.failed[theme] = true
		return
	}
	quickMenuBg.done[theme] = pic
}

// quickMenuBgLoad decodes path under the shared decode permit and scales it
// to just cover a sw×sh screen (never larger than the picture itself).
func quickMenuBgLoad(path string, sw, sh int) (pic *image.NRGBA, err error) {
	defer func() {
		if r := recover(); r != nil {
			pic, err = nil, errQuickMenuBgPanic
		}
	}()
	ctx := context.Background()
	if !imageDecodeSem.Acquire(ctx, 1) {
		return nil, ctx.Err()
	}
	defer imageDecodeSem.Release(1)
	src, err := imgvDecodeFile(path)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := quickMenuBgCoverSize(b.Dx(), b.Dy(), sw, sh)
	return imgvScale(src, w, h), nil
}

// quickMenuBgCoverSize is the smallest size with the picture's aspect that
// covers sw×sh, never larger than the picture. Pure.
func quickMenuBgCoverSize(w, h, sw, sh int) (int, int) {
	if w <= 0 || h <= 0 || sw <= 0 || sh <= 0 {
		return max(w, 1), max(h, 1)
	}
	scale := max(float64(sw)/float64(w), float64(sh)/float64(h))
	if scale >= 1 {
		return w, h
	}
	return max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
}
