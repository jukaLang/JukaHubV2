package main

// ports_store_images.go — the Ports Store's screenshots.
//
// Every PortMaster port carries a screenshot in its folder of the
// PortMaster-New repository (ports/<name>/screenshot.{jpg,png}, 640×480 by
// their rules; attr.image may name it). The store fetches the ones on screen
// — two at a time, the most recently wanted first, so scrolling fast does not
// queue a whole catalogue ahead of what is visible — shrinks each to at most
// pstoreShotMaxW pixels wide in Go and keeps it as a JPEG under
// data/ports-store/shots (pruned past pstoreShotDiskMax). A port whose
// picture cannot be found is remembered for a day with an empty <name>.none
// file and drawn as a glyph card.
//
// The repository path is inferred from PortMaster's layout and has not been
// fetched from this sandbox: if it changes, cards keep their glyph and the
// first miss is logged once.
//
// Textures are made on the render thread from the small JPEGs, at most
// pstoreShotDecodesPerFrame new ones a frame, and kept in a small
// least-recently-drawn cache that never drops one drawn this frame.

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
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
	// pstoreShotBase is the PortMaster-New tree the screenshots are in.
	pstoreShotBase = "https://raw.githubusercontent.com/PortsMaster/PortMaster-New/HEAD/ports/"
	// pstoreShotMaxW is the stored width.
	pstoreShotMaxW = 320
	// pstoreShotMaxBytes bounds one download.
	pstoreShotMaxBytes = 6 << 20
	// pstoreShotDiskMax is the cache size past which the oldest go.
	pstoreShotDiskMax = 40 << 20
	// pstoreShotWorkers is how many fetch at once.
	pstoreShotWorkers = 2
	// pstoreShotWantMax bounds the wanted stack.
	pstoreShotWantMax = 48
	// pstoreShotMissTTL is how long a missing picture is not asked for again.
	pstoreShotMissTTL = 24 * time.Hour
	// pstoreShotRetry is the pause after a network failure.
	pstoreShotRetry = 2 * time.Minute
	// pstoreShotTexMax bounds the texture cache.
	pstoreShotTexMax = 40
	// Decoded screenshots are much larger than their JPEGs on disk. Keep only
	// a few screens' worth on a 1 GB handheld, even when every image is valid.
	pstoreShotTexHandheldBytes = int64(8 << 20)
	pstoreShotTexDesktopBytes  = int64(24 << 20)
	// pstoreShotDecodesPerFrame is the per-frame budget for new textures.
	pstoreShotDecodesPerFrame = 2
	// pstoreShotMaxSide bounds a downloaded picture's sides before it is
	// decoded (PortMaster asks for 640×480).
	pstoreShotMaxSide = 2048
	// pstoreTexIdle is how long after the store's last frame its textures
	// are let go.
	pstoreTexIdle = 3 * time.Second
)

// pstoreShotStatus is what is known about one screenshot.
type pstoreShotStatus uint8

const (
	pstoreShotUnknown pstoreShotStatus = iota
	pstoreShotPending
	pstoreShotReady
	pstoreShotMissing
)

type pstoreShotWant struct {
	key, dir, image string
}

var pstoreShots struct {
	mu       sync.Mutex
	status   map[string]pstoreShotStatus
	retry    map[string]time.Time // a network failure: not before
	want     []pstoreShotWant
	workers  int
	written  int
	loggedNo bool
}

// portStoreImageName reads attr.image: a file name, a list of them, or an
// object whose "screenshot" (else any picture-named value) is one. Only a
// plain file name is kept. Pure.
func portStoreImageName(v interface{}) string {
	ok := func(s string) string {
		s = strings.TrimSpace(s)
		ext := strings.ToLower(filepath.Ext(s))
		if s == "" || strings.ContainsAny(s, `/\`) || strings.HasPrefix(s, ".") ||
			(ext != ".jpg" && ext != ".jpeg" && ext != ".png") {
			return ""
		}
		return s
	}
	switch t := v.(type) {
	case string:
		return ok(t)
	case []interface{}:
		for _, x := range t {
			if s, _ := x.(string); ok(s) != "" {
				return ok(s)
			}
		}
	case map[string]interface{}:
		if s, _ := t["screenshot"].(string); ok(s) != "" {
			return ok(s)
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, _ := t[k].(string); ok(s) != "" && !strings.Contains(strings.ToLower(k), "cover") {
				return ok(s)
			}
		}
	}
	return ""
}

// pstoreShotKey is a port's cache name: its zip name, lower case, safe.
func pstoreShotKey(zipName string) string {
	base := strings.ToLower(strings.TrimSuffix(zipName, filepath.Ext(zipName)))
	return sanitizeFileName(base)
}

var pstoreShotDirOnce struct {
	once sync.Once
	dir  string
}

// pstoreShotDir is data/ports-store/shots, created once: the render thread
// asks for it whenever it loads a texture.
func pstoreShotDir() string {
	pstoreShotDirOnce.once.Do(func() {
		pstoreShotDirOnce.dir = filepath.Join(pstoreDataDir(), "shots")
		_ = os.MkdirAll(pstoreShotDirOnce.dir, 0o755)
	})
	return pstoreShotDirOnce.dir
}

// pstoreShotStatusOf returns what is known, asking for the picture when
// nothing is. Main thread (the cards) — the workers do the file checks.
func pstoreShotStatusOf(e mcCatalogEntry) (string, pstoreShotStatus) {
	key := pstoreShotKey(e.Zip)
	if key == "" {
		return "", pstoreShotMissing
	}
	pstoreShots.mu.Lock()
	defer pstoreShots.mu.Unlock()
	if pstoreShots.status == nil {
		pstoreShots.status = map[string]pstoreShotStatus{}
		pstoreShots.retry = map[string]time.Time{}
	}
	st := pstoreShots.status[key]
	if st != pstoreShotUnknown {
		return key, st
	}
	if t, ok := pstoreShots.retry[key]; ok && time.Now().Before(t) {
		return key, pstoreShotMissing
	}
	pstoreShots.status[key] = pstoreShotPending
	pstoreShots.want = append(pstoreShots.want, pstoreShotWant{
		key: key, dir: strings.TrimSuffix(e.Zip, filepath.Ext(e.Zip)), image: e.Image,
	})
	if over := len(pstoreShots.want) - pstoreShotWantMax; over > 0 {
		// The oldest wishes are dropped and may be asked for again.
		for _, w := range pstoreShots.want[:over] {
			delete(pstoreShots.status, w.key)
		}
		pstoreShots.want = append(pstoreShots.want[:0], pstoreShots.want[over:]...)
	}
	if pstoreShots.workers < pstoreShotWorkers {
		pstoreShots.workers++
		go pstoreShotWorker()
	}
	return key, pstoreShotPending
}

// pstoreShotWorker fetches wanted pictures, newest wish first, until none
// are left.
func pstoreShotWorker() {
	for {
		pstoreShots.mu.Lock()
		n := len(pstoreShots.want)
		if n == 0 {
			pstoreShots.workers--
			pstoreShots.mu.Unlock()
			return
		}
		w := pstoreShots.want[n-1]
		pstoreShots.want = pstoreShots.want[:n-1]
		pstoreShots.mu.Unlock()
		st, netFail := pstoreShotFetch(w)
		pstoreShots.mu.Lock()
		if netFail {
			delete(pstoreShots.status, w.key)
			pstoreShots.retry[w.key] = time.Now().Add(pstoreShotRetry)
		} else {
			pstoreShots.status[w.key] = st
		}
		pstoreShots.mu.Unlock()
	}
}

// pstoreShotFetch makes w's cached JPEG. It reports Ready, Missing, or a
// network failure (to be tried again later). A panic counts as Missing, so a
// card does not ask for the same picture again every frame.
func pstoreShotFetch(w pstoreShotWant) (st pstoreShotStatus, netFail bool) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("portsstore", "screenshot %s: %v", w.key, r)
			st, netFail = pstoreShotMissing, false
		}
	}()
	dir := pstoreShotDir()
	path := filepath.Join(dir, w.key+".jpg")
	none := filepath.Join(dir, w.key+".none")
	if regularFile(path) {
		return pstoreShotReady, false
	}
	if info, err := os.Stat(none); err == nil && time.Since(info.ModTime()) < pstoreShotMissTTL {
		return pstoreShotMissing, false
	}
	if !IsOnline() {
		return pstoreShotUnknown, true
	}
	names := []string{"screenshot.jpg", "screenshot.png"}
	if w.image != "" && w.image != names[0] && w.image != names[1] {
		names = append([]string{w.image}, names...)
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: ytTransport()}
	for _, name := range names {
		u := pstoreShotBase + url.PathEscape(w.dir) + "/" + url.PathEscape(name)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		data, status, err := pstoreShotGet(ctx, client, u)
		cancel()
		if err != nil {
			logDebug("portsstore", "screenshot %s: %v", u, err)
			return pstoreShotUnknown, true
		}
		if status == http.StatusNotFound {
			continue
		}
		if status != http.StatusOK {
			return pstoreShotUnknown, true
		}
		// The sides are read before the pixels: a 640×480 picture is what is
		// expected, and a huge one would cost tens of megabytes to decode on
		// a 1 GB handheld.
		cfg, _, cerr := image.DecodeConfig(bytes.NewReader(data))
		if cerr != nil || cfg.Width <= 0 || cfg.Height <= 0 ||
			cfg.Width > pstoreShotMaxSide || cfg.Height > pstoreShotMaxSide {
			logDebug("portsstore", "screenshot %s: unreadable or too large", u)
			continue
		}
		dctx, dcancel := context.WithTimeout(context.Background(), 20*time.Second)
		rgba, err := decodeImageRGBA(dctx, data)
		dcancel()
		if err != nil {
			logDebug("portsstore", "screenshot %s: %v", u, err)
			continue
		}
		small := pstoreDownscale(rgba, pstoreShotMaxW)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: 82}); err != nil {
			continue
		}
		if err := pstoreWriteFile(path, buf.Bytes()); err != nil {
			logDebug("portsstore", "saving screenshot %s: %v", path, err)
			return pstoreShotMissing, false
		}
		pstoreShotNoteWritten(dir)
		return pstoreShotReady, false
	}
	pstoreShots.mu.Lock()
	first := !pstoreShots.loggedNo
	pstoreShots.loggedNo = true
	pstoreShots.mu.Unlock()
	if first {
		logInfo("portsstore", "no screenshot found for %s under %s%s/ (if every port says this, the repository layout changed)",
			w.key, pstoreShotBase, w.dir)
	}
	_ = os.WriteFile(none, nil, 0o644)
	return pstoreShotMissing, false
}

// pstoreShotGet reads url; a 404 is a status, not an error.
func pstoreShotGet(ctx context.Context, client *http.Client, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "JukaHub-PortsStore/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, pstoreShotMaxBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > pstoreShotMaxBytes {
		return nil, 0, errors.New("the screenshot is larger than the limit")
	}
	return data, http.StatusOK, nil
}

// pstoreDownscale shrinks src to at most maxW pixels wide with a box filter
// (each output pixel is the average of the source pixels it covers). A
// picture already small enough is returned as it is. Pure.
func pstoreDownscale(src *image.RGBA, maxW int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= maxW || sw <= 0 || sh <= 0 {
		return src
	}
	dw := maxW
	dh := sh * dw / sw
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		y0, y1 := dy*sh/dh, (dy+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < dw; dx++ {
			x0, x1 := dx*sw/dw, (dx+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint32
			for y := y0; y < y1; y++ {
				row := src.Pix[(y)*src.Stride:]
				for x := x0; x < x1; x++ {
					p := row[x*4 : x*4+4]
					r += uint32(p[0])
					g += uint32(p[1])
					bl += uint32(p[2])
					a += uint32(p[3])
					n++
				}
			}
			o := dst.Pix[dy*dst.Stride+dx*4:]
			o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(bl/n), uint8(a/n)
		}
	}
	return dst
}

// pstoreShotNoteWritten prunes the cache every so often: the oldest
// pictures go until it is three quarters of pstoreShotDiskMax.
func pstoreShotNoteWritten(dir string) {
	pstoreShots.mu.Lock()
	pstoreShots.written++
	due := pstoreShots.written%25 == 1
	pstoreShots.mu.Unlock()
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
	if total <= pstoreShotDiskMax {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	removed := map[string]bool{}
	for _, f := range files {
		if total <= pstoreShotDiskMax*3/4 {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
			removed[strings.TrimSuffix(filepath.Base(f.path), ".jpg")] = true
		}
	}
	pstoreShots.mu.Lock()
	for k := range removed {
		delete(pstoreShots.status, k)
	}
	pstoreShots.mu.Unlock()
}

// ── Textures (render thread) ────────────────────────────────────────────

type pstoreTexEntry struct {
	tex   *sdl.Texture
	used  uint64
	bytes int64
}

var pstoreTex struct {
	renderer *sdl.Renderer
	m        map[string]*pstoreTexEntry
	bytes    int64
	frame    uint64
	loads    int
	// janitor: a release check is scheduled (main thread only).
	janitor bool
}

// pstoreTexClear lets every store texture go. Main thread: memory pressure
// (adaptive.go) and the janitor once the store is off screen.
func pstoreTexClear() {
	for _, t := range pstoreTex.m {
		if t.tex != nil {
			_ = t.tex.Destroy()
		}
	}
	pstoreTex.m = nil
	pstoreTex.bytes = 0
	pstoreTex.renderer = nil
}

// pstoreTexJanitor frees the textures a few seconds after the store left the
// screen (no other screen draws them). The check runs on the main loop
// through tbxPost; it reschedules itself while the store is still up.
func pstoreTexJanitor() {
	if pstoreTex.janitor || len(pstoreTex.m) == 0 {
		return
	}
	pstoreTex.janitor = true
	go func() {
		time.Sleep(pstoreTexIdle)
		tbxPost(func() {
			pstoreTex.janitor = false
			if portsStoreOnScreen(appConfig) && time.Since(pstore.lastFrame) < pstoreTexIdle {
				pstoreTexJanitor()
				return
			}
			pstoreTexClear()
		})
	}()
}

// pstoreShotTexture is the card picture for e, or nil (not fetched yet,
// missing, or over this frame's decode budget). Main thread.
func pstoreShotTexture(renderer *sdl.Renderer, e mcCatalogEntry) *sdl.Texture {
	if renderer == nil {
		return nil
	}
	if pstoreTex.renderer != renderer || pstoreTex.m == nil {
		// A new renderer: the old one took its textures with it.
		pstoreTex.renderer = renderer
		pstoreTex.m = make(map[string]*pstoreTexEntry, pstoreShotTexMax)
		pstoreTex.bytes = 0
	}
	if pstoreTex.frame != frameCount {
		pstoreTex.frame, pstoreTex.loads = frameCount, 0
		pstoreTexJanitor()
	}
	key := pstoreShotKey(e.Zip)
	if t, ok := pstoreTex.m[key]; ok {
		t.used = frameCount
		return t.tex
	}
	_, st := pstoreShotStatusOf(e)
	if st != pstoreShotReady || pstoreTex.loads >= pstoreShotDecodesPerFrame {
		return nil
	}
	pstoreTex.loads++
	surface, err := img.Load(filepath.Join(pstoreShotDir(), key+".jpg"))
	if err != nil || surface == nil {
		// Unreadable: fetch it again next time.
		pstoreShots.mu.Lock()
		delete(pstoreShots.status, key)
		pstoreShots.retry[key] = time.Now().Add(pstoreShotRetry)
		pstoreShots.mu.Unlock()
		_ = os.Remove(filepath.Join(pstoreShotDir(), key+".jpg"))
		return nil
	}
	bytes := int64(surface.W) * int64(surface.H) * 4
	budget := cacheBudgetByDevice(pstoreShotTexHandheldBytes, pstoreShotTexDesktopBytes)
	if !storeShotTextureMakeRoom(pstoreTex.m, &pstoreTex.bytes, pstoreShotTexMax, budget, bytes, frameCount) {
		surface.Free()
		return nil
	}
	tex, err := renderer.CreateTextureFromSurface(surface)
	surface.Free()
	if err != nil {
		return nil
	}
	pstoreTex.m[key] = &pstoreTexEntry{tex: tex, used: frameCount, bytes: bytes}
	pstoreTex.bytes += bytes
	return tex
}

// storeShotTextureMakeRoom is shared by the Ports and Homebrew stores. It
// never destroys a texture drawn this frame; if none can leave, the new image
// waits for a later frame rather than pushing RAM past the byte ceiling.
func storeShotTextureMakeRoom(entries map[string]*pstoreTexEntry, usedBytes *int64, maxEntries int, maxBytes, incoming int64, frame uint64) bool {
	if incoming <= 0 || incoming > maxBytes {
		return false
	}
	for len(entries) >= maxEntries || *usedBytes+incoming > maxBytes {
		oldKey, oldUsed := "", ^uint64(0)
		for key, entry := range entries {
			if entry.used < frame && (oldKey == "" || entry.used < oldUsed) {
				oldKey, oldUsed = key, entry.used
			}
		}
		if oldKey == "" {
			return false
		}
		entry := entries[oldKey]
		if entry.tex != nil {
			_ = entry.tex.Destroy()
		}
		*usedBytes -= entry.bytes
		delete(entries, oldKey)
	}
	return true
}
