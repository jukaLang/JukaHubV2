package main

// chiptune_art.go — a soundtrack wears its game's cover.
//
// The Games library keeps the box art it downloaded under
// <data>/game-art/<Console>/<ROM name>.png (romsetup.go romArtworkPath). A
// soundtrack names its game (NSF/GBS/SPC/VGM headers) and its console, so the
// cover is found by title: both sides are reduced to lower-case letters and
// digits without the "(USA)"/"[!]" tags, and an equal pair is a match. The
// file's folder name and its own name are tried too (a folder of SPCs is
// usually named after the game). Without a library cover the folder's own
// picture is used, as for any song (screensaver_media.go scanCoverArt).
//
// Each console's art folder is listed once a few minutes, and the answer per
// soundtrack is cached; the render thread only reads the cache and a miss is
// looked up on a goroutine. The picture is decoded (and shrunk) on a goroutine
// too; the render thread only uploads it (chiptuneArtTexture).

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	chiptuneArtTTL    = 2 * time.Minute
	chiptuneArtDirTTL = 5 * time.Minute
)

// chiptuneArtSystems lists the library consoles whose covers a soundtrack of
// a system may wear (a GBS can be a Game Boy Color game).
var chiptuneArtSystems = map[string][]string{
	"Game Boy":      {"Game Boy", "Game Boy Color"},
	"Mega Drive":    {"Mega Drive", "Sega CD", "32X"},
	"Master System": {"Master System", "Game Gear"},
	"Sega":          {"Mega Drive", "Master System", "Game Gear"},
}

type chiptuneArtEntry struct {
	path string
	at   time.Time
	busy bool
}

type chiptuneArtDir struct {
	byTitle map[string]string
	at      time.Time
}

var chiptuneArt struct {
	mu     sync.Mutex
	byFile map[string]chiptuneArtEntry
	dirs   map[string]chiptuneArtDir
}

// chiptuneCoverArt is the picture for a soundtrack (ok false for anything
// else). It never blocks: a miss starts a lookup and answers "" until the
// lookup is back.
func chiptuneCoverArt(path string) (string, bool) {
	if !chiptuneHandles(path) {
		return "", false
	}
	file := chiptuneFileOf(path)
	key := dlPathKey(file)
	now := time.Now()
	chiptuneArt.mu.Lock()
	if chiptuneArt.byFile == nil {
		chiptuneArt.byFile = map[string]chiptuneArtEntry{}
	}
	e, ok := chiptuneArt.byFile[key]
	if ok && (e.busy || now.Sub(e.at) < chiptuneArtTTL) {
		chiptuneArt.mu.Unlock()
		return e.path, true
	}
	if len(chiptuneArt.byFile) > 512 {
		chiptuneArt.byFile = map[string]chiptuneArtEntry{}
	}
	chiptuneArt.byFile[key] = chiptuneArtEntry{path: e.path, busy: true}
	chiptuneArt.mu.Unlock()

	go func() {
		art := chiptuneLibraryCover(file)
		if art == "" {
			art = scanCoverArt(filepath.Dir(file), file)
		}
		chiptuneArt.mu.Lock()
		chiptuneArt.byFile[key] = chiptuneArtEntry{path: art, at: time.Now()}
		chiptuneArt.mu.Unlock()
	}()
	return e.path, true
}

// chiptuneLibraryCover finds the Games library's cover for the soundtrack in
// file, "" when there is none. It reads the disk: never on the render thread.
func chiptuneLibraryCover(file string) string {
	if !chiptuneHandles(file) {
		return ""
	}
	file = chiptuneFileOf(file)
	meta, ok := chiptuneMetaFor(file)
	if !ok || meta.System == "" {
		return ""
	}
	root := romArtworkDir()
	if root == "" {
		return ""
	}
	titles := []string{meta.Game, filepath.Base(filepath.Dir(file)),
		strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))}
	systems := chiptuneArtSystems[meta.System]
	if systems == nil {
		systems = []string{meta.System}
	}
	for _, sys := range systems {
		index := chiptuneArtIndex(filepath.Join(root, strings.ReplaceAll(sys, " ", "_")))
		for _, t := range titles {
			if art := index[chiptuneNormTitle(t)]; art != "" {
				return art
			}
		}
	}
	return ""
}

// chiptuneArtIndex lists one console's art folder as normalised title →
// picture, cached for a few minutes.
func chiptuneArtIndex(dir string) map[string]string {
	chiptuneArt.mu.Lock()
	if chiptuneArt.dirs == nil {
		chiptuneArt.dirs = map[string]chiptuneArtDir{}
	}
	if d, ok := chiptuneArt.dirs[dir]; ok && time.Since(d.at) < chiptuneArtDirTTL {
		chiptuneArt.mu.Unlock()
		return d.byTitle
	}
	chiptuneArt.mu.Unlock()
	index := map[string]string{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			name := e.Name()
			switch strings.ToLower(filepath.Ext(name)) {
			case ".png", ".jpg", ".jpeg":
			default:
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			key := chiptuneNormTitle(strings.TrimSuffix(name, filepath.Ext(name)))
			if key != "" && index[key] == "" {
				index[key] = filepath.Join(dir, name)
			}
		}
	}
	chiptuneArt.mu.Lock()
	chiptuneArt.dirs[dir] = chiptuneArtDir{byTitle: index, at: time.Now()}
	chiptuneArt.mu.Unlock()
	return index
}

// chiptuneNormTitle reduces a game title to what two spellings of it share:
// no bracketed tags, no leading "The", lower-case letters and digits only.
// "Super Mario Bros. 3 (USA) [!]" and "super_mario_bros_3" are equal.
func chiptuneNormTitle(s string) string {
	if i := strings.IndexAny(s, "(["); i > 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "_", " ")))
	s = strings.TrimPrefix(s, "the ")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// chiptuneArtUpload is the one cover being decoded for Now Playing's tile.
// artTexture would decode it on the render thread at full size (a folder's
// cover.jpg can be a poster); this decodes it on a goroutine, shrunk to the
// library's 512 px (decodeLibraryArt), and the render thread only uploads it.
var chiptuneArtUpload struct {
	mu     sync.Mutex
	path   string // the picture being, or last, decoded
	busy   bool
	failed bool
	pic    *image.NRGBA // decoded, waiting for the upload
}

// chiptuneArtTexture is the cover's texture once it is ready, nil until then.
// Render thread only (gameLib's cache is the render thread's); the texture
// lives in that cache, so the dock and the lock screen find it there too.
func chiptuneArtTexture(renderer *sdl.Renderer, art string) *sdl.Texture {
	if tex, ok := gameLib.artCache[art]; ok {
		return tex // nil for a picture that would not decode
	}
	u := &chiptuneArtUpload
	u.mu.Lock()
	if u.path == art && !u.busy {
		pic, failed := u.pic, u.failed || u.pic == nil
		u.pic = nil
		u.path = ""
		u.mu.Unlock()
		if failed {
			gameLib.rememberArt(art, nil, 0, 0)
			return nil
		}
		tex := imgvTexture(renderer, pic)
		var w, h int32
		if tex != nil {
			w, h = int32(pic.Rect.Dx()), int32(pic.Rect.Dy())
		}
		gameLib.rememberArt(art, tex, w, h)
		return tex
	}
	if u.busy {
		// One decode at a time; a cover asked for meanwhile is asked again
		// on a later frame.
		u.mu.Unlock()
		return nil
	}
	u.path, u.busy, u.failed, u.pic = art, true, false, nil
	u.mu.Unlock()
	go func() {
		pic, err := decodeLibraryArt(art, context.Background())
		u.mu.Lock()
		u.busy = false
		if u.path == art {
			u.pic, u.failed = pic, err != nil || pic == nil
		}
		u.mu.Unlock()
	}()
	return nil
}

// chiptuneDrawArt draws a soundtrack's cover into Now Playing's artwork tile
// and reports whether there was one to draw. A dark band along the top keeps
// the tile's "Playing" label readable on any cover. Nothing here reads the
// disk: the lookup and the decode run on goroutines, and the tile shows the
// bars until they are back.
func chiptuneDrawArt(renderer *sdl.Renderer, path string, x, y, size int32, alpha uint8) bool {
	art, ok := chiptuneCoverArt(path)
	if !ok || art == "" {
		return false
	}
	tex := chiptuneArtTexture(renderer, art)
	if tex == nil {
		return false
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 {
		return false
	}
	dst := saverCoverRect(tw, th, sdl.Rect{X: x, Y: y, W: size, H: size})
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	_ = tex.SetAlphaMod(alpha)
	renderWithClip(renderer, x, y, size, size, func(r *sdl.Renderer) {
		_ = r.Copy(tex, nil, &dst)
	})
	_ = tex.SetAlphaMod(255)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, x, y, size, 52, 16, sdl.Color{A: uint8(int(alpha) * 150 / 255)})
	return true
}
