package main

// appicons.go draws the real icon of an installed app in the Apps launcher.
//
// An icon comes from one of three places, cheapest first:
//
//  1. The app folder's own image (TrimUI: config.json "icon" or icon.png).
//  2. An image stored next to the program under the same name
//     ("Firefox.lnk" + "Firefox.png").
//  3. The icon Windows extracts from a .lnk/.exe, cached on disk (see
//     appicons_windows.go). Extraction runs in the background, so a tile shows
//     its initial until the picture is ready.
//
// When none of those exists, the tile keeps the coloured initial from
// appgrid.go: an app without artwork still reads as a deliberate tile.

import (
	"os"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// appIconMaxTextures bounds the texture cache. A launcher can list hundreds of
// programs and a full-size icon is up to 256x256x4 bytes, so the cache keeps the
// most recently drawn ones instead of growing with the list.
const appIconMaxTextures = 64

// appIconTexture is one cached icon with the footprint it occupies. The size is
// recorded at creation because SDL cannot report it without a round trip.
type cachedAppIcon struct {
	texture *sdl.Texture
	bytes   int64
}

// appIconTextureCache is insertion-ordered and bounded twice: 64 icons, and
// appIconTextureBudgetBytes. The count alone was a byte ceiling only for icons
// of one size; a launcher mixing 64px applet icons with 256px extracted ones
// could hold sixteen megabytes under it.
type appIconTextureCache struct {
	textures *byteOrderedStore[string, cachedAppIcon]
}

func newAppIconTextureCache() *appIconTextureCache {
	return &appIconTextureCache{textures: newByteOrderedStore[string, cachedAppIcon](
		appIconMaxTextures,
		appIconTextureBudgetBytes(),
		func(t cachedAppIcon) int64 { return t.bytes },
		func(t cachedAppIcon) {
			if t.texture != nil {
				t.texture.Destroy()
			}
		},
	)}
}

var appIconTextures = newAppIconTextureCache()

// appIconTextureStats reports the icons and bytes held, for the texture
// accounting log and the tests.
func appIconTextureStats() (entries int, bytes int64) {
	return appIconTextures.textures.stats()
}

// appEntryIconPath returns the image file to draw for a launcher row, or "".
// A scanned sidecar/folder icon wins; otherwise the extracted-icon cache is
// checked, which is how an icon appears a moment after the launcher opened.
func appEntryIconPath(entry FileEntry) string {
	if entry.Icon != "" {
		return entry.Icon
	}
	if strings.HasPrefix(entry.Path, builtinScenePrefix) || strings.HasPrefix(entry.Path, builtinActionPrefix) {
		// Built-in applets have no file on disk to take a picture of.
		return ""
	}
	return cachedExtractedAppIcon(entry.Path)
}

// appIconPathCache remembers resolved extracted-icon paths. On Windows that
// lookup stats a cache file, and the launcher asks once per visible tile per
// frame, so a miss was costing a filesystem call per tile per frame.
var (
	appIconPathMu    sync.Mutex
	appIconPathCache = map[string]string{}
)

const appIconPathCacheMax = 512

func cachedExtractedAppIcon(programPath string) string {
	appIconPathMu.Lock()
	icon, ok := appIconPathCache[programPath]
	appIconPathMu.Unlock()
	if ok {
		return icon
	}
	icon = extractedAppIcon(programPath)
	appIconPathMu.Lock()
	if len(appIconPathCache) >= appIconPathCacheMax {
		appIconPathCache = map[string]string{}
	}
	appIconPathCache[programPath] = icon
	appIconPathMu.Unlock()
	return icon
}

// dropAppIconPathCache forgets resolved icon paths, so a tile picks up an icon
// that appeared after the frame which cached the miss.
func dropAppIconPathCache() {
	appIconPathMu.Lock()
	appIconPathCache = map[string]string{}
	appIconPathMu.Unlock()
}

// appIconTexture loads and caches a launcher icon texture. A file that cannot
// be decoded is remembered as nil so a broken image is not re-read every frame.
func appIconTexture(renderer *sdl.Renderer, path string) *sdl.Texture {
	if renderer == nil || path == "" {
		return nil
	}
	if entry, ok := appIconTextures.textures.get(path); ok {
		return entry.texture
	}
	surface, err := img.Load(path)
	if err != nil || surface == nil {
		// Remember the miss through the same bound as a hit. Storing it
		// straight into the map left failed icons outside the eviction order,
		// so a launcher full of broken icons grew the map without limit while
		// the count cap only ever accounted for the ones that loaded.
		appIconTextures.remember(path, cachedAppIcon{})
		return nil
	}
	tex, _ := renderer.CreateTextureFromSurface(surface)
	size := int64(surface.W) * int64(surface.H) * 4
	surface.Free()
	appIconTextures.remember(path, cachedAppIcon{texture: tex, bytes: size})
	return tex
}

// remember stores an icon (a zero value for a failed decode) and lets the store
// evict the oldest entries past the count or byte ceiling. A key already
// tracked is refreshed in place, so the order holds one entry per cached path.
func (c *appIconTextureCache) remember(path string, icon cachedAppIcon) {
	c.textures.put(path, icon)
}

// clearAppIconCache releases every cached icon texture. Textures belong to the
// renderer that created them, so this runs whenever that renderer goes away.
func clearAppIconCache() {
	appIconTextures.textures.clear()
}

// appProgramPaths lists the external programs in a launcher list. Only those
// can carry an extracted icon; built-in applets and rows that already have an
// image are skipped.
func appProgramPaths(entries []FileEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Icon != "" {
			continue
		}
		if strings.HasPrefix(e.Path, builtinScenePrefix) || strings.HasPrefix(e.Path, builtinActionPrefix) {
			continue
		}
		paths = append(paths, e.Path)
	}
	return paths
}

// extractedIconExists reports whether an extracted icon file is usable. It is
// the cross-platform half of the cache lookup: on a device that never extracts
// icons the path is empty and nothing is touched.
func extractedIconExists(path string) string {
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return ""
	}
	return path
}
