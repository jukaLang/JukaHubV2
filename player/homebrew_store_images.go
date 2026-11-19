package main

// homebrew_store_images.go — Get Homebrew's card pictures.
//
// Each feed item names its cover (imageurl, 315×250 on itch.io's image host).
// The cards on screen ask for theirs; two workers fetch them, the most
// recently wanted first (so scrolling fast does not queue the whole list
// ahead of what is visible), shrink each to hbShotMaxW pixels wide in Go and
// keep it as a JPEG under data/homebrew-store/covers, pruned past
// hbShotDiskMax. A picture that cannot be had is remembered for a day with an
// empty .none file and drawn as the console's glyph.
//
// Textures are made on the render thread, at most hbShotDecodesPerFrame new
// ones a frame, kept in a small least-recently-drawn cache that never drops
// one drawn this frame and is let go a few seconds after the store leaves the
// screen. The same small picture serves the game page; the cover saved for
// the library is fetched full size at install (homebrew_store_install.go).
//
// The pattern, and the status type, are the Ports Store's
// (ports_store_images.go).

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

const (
	hbShotMaxW            = 320
	hbShotMaxBytes        = 6 << 20
	hbShotDiskMax         = 30 << 20
	hbShotWorkers         = 2
	hbShotWantMax         = 48
	hbShotMissTTL         = 24 * time.Hour
	hbShotRetry           = 2 * time.Minute
	hbShotTexMax          = 40
	hbShotDecodesPerFrame = 2
	hbShotMaxSide         = 2048
	hbTexIdle             = 3 * time.Second
)

type hbShotWant struct{ key, url string }

var hbShots struct {
	mu      sync.Mutex
	status  map[string]pstoreShotStatus
	retry   map[string]time.Time
	want    []hbShotWant
	workers int
	written int
}

var hbShotDirOnce struct {
	once sync.Once
	dir  string
}

// hbShotDir is data/homebrew-store/covers, made once.
func hbShotDir() string {
	hbShotDirOnce.once.Do(func() {
		hbShotDirOnce.dir = filepath.Join(hbDataDir(), "covers")
		_ = os.MkdirAll(hbShotDirOnce.dir, 0o755)
	})
	return hbShotDirOnce.dir
}

// hbShotStatusOf is what is known about a picture, asking for it when
// nothing is. Main thread.
func hbShotStatusOf(u string) (string, pstoreShotStatus) {
	if u == "" {
		return "", pstoreShotMissing
	}
	key := hbHash(u)
	hbShots.mu.Lock()
	defer hbShots.mu.Unlock()
	if hbShots.status == nil {
		hbShots.status = map[string]pstoreShotStatus{}
		hbShots.retry = map[string]time.Time{}
	}
	if st := hbShots.status[key]; st != pstoreShotUnknown {
		return key, st
	}
	if t, ok := hbShots.retry[key]; ok && time.Now().Before(t) {
		return key, pstoreShotMissing
	}
	hbShots.status[key] = pstoreShotPending
	hbShots.want = append(hbShots.want, hbShotWant{key: key, url: u})
	if over := len(hbShots.want) - hbShotWantMax; over > 0 {
		for _, w := range hbShots.want[:over] {
			delete(hbShots.status, w.key)
		}
		hbShots.want = append(hbShots.want[:0], hbShots.want[over:]...)
	}
	if hbShots.workers < hbShotWorkers {
		hbShots.workers++
		go hbShotWorker()
	}
	return key, pstoreShotPending
}

// hbShotWorker fetches wanted pictures, newest wish first.
func hbShotWorker() {
	for {
		hbShots.mu.Lock()
		n := len(hbShots.want)
		if n == 0 {
			hbShots.workers--
			hbShots.mu.Unlock()
			return
		}
		w := hbShots.want[n-1]
		hbShots.want = hbShots.want[:n-1]
		hbShots.mu.Unlock()
		st, netFail := hbShotFetch(w)
		hbShots.mu.Lock()
		if netFail {
			delete(hbShots.status, w.key)
			hbShots.retry[w.key] = time.Now().Add(hbShotRetry)
		} else {
			hbShots.status[w.key] = st
		}
		hbShots.mu.Unlock()
	}
}

// hbShotFetch makes w's cached JPEG: Ready, Missing, or a network failure to
// try again later. A panic counts as Missing.
func hbShotFetch(w hbShotWant) (st pstoreShotStatus, netFail bool) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("homebrew", "cover picture: %v", r)
			st, netFail = pstoreShotMissing, false
		}
	}()
	dir := hbShotDir()
	path := filepath.Join(dir, w.key+".jpg")
	none := filepath.Join(dir, w.key+".none")
	if regularFile(path) {
		return pstoreShotReady, false
	}
	if info, err := os.Stat(none); err == nil && time.Since(info.ModTime()) < hbShotMissTTL {
		return pstoreShotMissing, false
	}
	if !IsOnline() {
		return pstoreShotUnknown, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	data, status, err := hbGet(ctx, w.url, "", hbShotMaxBytes)
	cancel()
	if err != nil || status >= 500 || status == 429 {
		return pstoreShotUnknown, true
	}
	if status != 200 {
		_ = os.WriteFile(none, nil, 0o644)
		return pstoreShotMissing, false
	}
	cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data))
	if cerr != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > hbShotMaxSide || cfg.Height > hbShotMaxSide {
		_ = os.WriteFile(none, nil, 0o644)
		return pstoreShotMissing, false
	}
	dctx, dcancel := context.WithTimeout(context.Background(), 20*time.Second)
	rgba, err := decodeImageRGBA(dctx, data)
	dcancel()
	if err != nil {
		_ = os.WriteFile(none, nil, 0o644)
		return pstoreShotMissing, false
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, pstoreDownscale(rgba, hbShotMaxW), &jpeg.Options{Quality: 82}); err != nil {
		return pstoreShotMissing, false
	}
	if err := pstoreWriteFile(path, buf.Bytes()); err != nil {
		return pstoreShotMissing, false
	}
	hbShotNoteWritten(dir)
	return pstoreShotReady, false
}

// hbShotNoteWritten prunes the cache every so often: the oldest pictures go
// until it is three quarters of hbShotDiskMax.
func hbShotNoteWritten(dir string) {
	hbShots.mu.Lock()
	hbShots.written++
	due := hbShots.written%25 == 1
	hbShots.mu.Unlock()
	if !due {
		return
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	for _, it := range items {
		if it.IsDir() || !strings.HasSuffix(it.Name(), ".jpg") {
			continue
		}
		info, err := it.Info()
		if err != nil {
			continue
		}
		files = append(files, file{filepath.Join(dir, it.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= hbShotDiskMax {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	var removed []string
	for _, f := range files {
		if total <= hbShotDiskMax*3/4 {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
			removed = append(removed, strings.TrimSuffix(filepath.Base(f.path), ".jpg"))
		}
	}
	hbShots.mu.Lock()
	for _, k := range removed {
		delete(hbShots.status, k)
	}
	hbShots.mu.Unlock()
}

// ── Textures (render thread) ────────────────────────────────────────────

var hbTex struct {
	renderer *sdl.Renderer
	m        map[string]*pstoreTexEntry
	frame    uint64
	loads    int
	janitor  bool
}

// homebrewTexClear lets every store texture go. Main thread: memory
// pressure (adaptive.go) and the janitor once the store is off screen.
func homebrewTexClear() {
	for k, t := range hbTex.m {
		if t.tex != nil {
			_ = t.tex.Destroy()
		}
		delete(hbTex.m, k)
	}
}

// hbTexJanitor frees the textures a few seconds after the store left the
// screen.
func hbTexJanitor() {
	if hbTex.janitor || len(hbTex.m) == 0 {
		return
	}
	hbTex.janitor = true
	go func() {
		time.Sleep(hbTexIdle)
		tbxPost(func() {
			hbTex.janitor = false
			if homebrewOnScreen(appConfig) && time.Since(hb.lastFrame) < hbTexIdle {
				hbTexJanitor()
				return
			}
			homebrewTexClear()
		})
	}()
}

// hbCoverTexture is the card picture at u, or nil (not fetched yet, missing,
// or over this frame's decode budget). Main thread.
func hbCoverTexture(renderer *sdl.Renderer, u string) *sdl.Texture {
	if renderer == nil || u == "" {
		return nil
	}
	if hbTex.renderer != renderer || hbTex.m == nil {
		hbTex.renderer = renderer
		hbTex.m = make(map[string]*pstoreTexEntry, hbShotTexMax)
	}
	if hbTex.frame != frameCount {
		hbTex.frame, hbTex.loads = frameCount, 0
		hbTexJanitor()
	}
	key := hbHash(u)
	if t, ok := hbTex.m[key]; ok {
		t.used = frameCount
		return t.tex
	}
	_, st := hbShotStatusOf(u)
	if st != pstoreShotReady || hbTex.loads >= hbShotDecodesPerFrame {
		return nil
	}
	hbTex.loads++
	path := filepath.Join(hbShotDir(), key+".jpg")
	surface, err := img.Load(path)
	if err != nil || surface == nil {
		hbShots.mu.Lock()
		delete(hbShots.status, key)
		hbShots.retry[key] = time.Now().Add(hbShotRetry)
		hbShots.mu.Unlock()
		_ = os.Remove(path)
		return nil
	}
	tex, err := renderer.CreateTextureFromSurface(surface)
	surface.Free()
	if err != nil {
		return nil
	}
	for len(hbTex.m) >= hbShotTexMax {
		oldKey, oldUsed := "", frameCount
		for k, t := range hbTex.m {
			if t.used < oldUsed {
				oldKey, oldUsed = k, t.used
			}
		}
		if oldKey == "" {
			break
		}
		if t := hbTex.m[oldKey]; t.tex != nil {
			_ = t.tex.Destroy()
		}
		delete(hbTex.m, oldKey)
	}
	hbTex.m[key] = &pstoreTexEntry{tex: tex, used: frameCount}
	return tex
}
