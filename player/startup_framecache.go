package main

// startup_framecache.go — the first frame's labels and icons, ready before it.
//
// The first frame is the most expensive one JukaHub draws: every label on Home
// is rasterised cold by FreeType (40 to 60 of them, tens of milliseconds and
// more on an A53), and every icon by the vector rasteriser. Both are pure
// functions of things that rarely change between two starts: the font file and
// size, the text, the colour; the icon's name and size. So the first frame of
// one start is remembered for the next:
//
//   - while the first frame is being prepared (the scene warm-up behind a
//     splash included), every label rendered through renderLabelTexture keeps
//     its coverage (one byte per pixel; the colour of a Blended label is the
//     same on every pixel) and every icon baked into the atlas its name and
//     size;
//   - right after the first frame they are written to data/first-frame.cache
//     (deflated, a few tens of kB) on a goroutine — only when this start had to
//     render something the file did not have, or when much of the file went
//     unused, so a steady device writes nothing;
//   - at the next start a goroutine (startupPreload) reads the file during SDL
//     and GPU start-up, expands the labels to ready pixels and rasterises the
//     icons, and a label or icon the first frame misses in its caches is
//     uploaded from there instead of being rendered.
//
// A label is keyed by everything that shapes its pixels: the font file (path,
// size and time), the point size, the face's height, style, outline, hinting
// and kerning, the colour and the text. A theme, language, font, text-size or
// canvas change simply misses and is rendered as before; a new JukaHub build
// or another SDL version ignores the whole file. Off the first frame nothing
// here runs: one atomic load per text-cache miss.
//
// JUKAHUB_FRAME_CACHE=0 turns it off (no read, no write); the start-up log line
// says how many of the first frame's labels came from the file ("labels=").

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	frameCacheFileName = "first-frame.cache"
	frameCacheMagic    = "JHFF1\n"
	// frameCacheMaxBytes bounds the coverage kept for all labels together,
	// frameCacheMaxLabel one label's, frameCacheMaxLabels and frameCacheMaxIcons
	// the counts. A Home is far below every one of them.
	frameCacheMaxBytes  = 3 << 20
	frameCacheMaxLabel  = 256 << 10
	frameCacheMaxLabels = 400
	frameCacheMaxIcons  = 256
	frameCacheMaxKey    = 2048
	// frameCacheFileMax bounds the read of the (deflated) file.
	frameCacheFileMax = 4 << 20
)

// frameCacheLabel is one remembered label: its size, the colour of its covered
// and uncovered pixels (0x00RRGGBB) and its coverage. pixels is the ARGB8888
// picture, built by the loader.
type frameCacheLabel struct {
	w, h   int32
	ink    uint32
	clear  uint32
	alpha  []byte
	pixels []uint32
}

// frameCacheData is the last start's file, read and prepared. Never changed
// after it is published.
type frameCacheData struct {
	stamp    string
	labels   map[string]*frameCacheLabel
	order    []string
	iconKeys []iconAtlasKey
	icons    map[iconAtlasKey][]uint8
}

var frameCache struct {
	active atomic.Bool                    // from startupPreload until the first frame
	loaded atomic.Pointer[frameCacheData] // the last start's, once read
	// sdlVersion is the linked SDL's version, asked on the main thread by
	// frameCacheStart before any goroutine here starts; read-only after.
	sdlVersion string

	mu        sync.Mutex
	faces     map[*ttf.Font]string // every face opened while active
	used      map[string]*frameCacheLabel
	usedOrder []string
	usedBytes int
	icons     []iconAtlasKey
	iconSeen  map[iconAtlasKey]bool
	hits      int // labels uploaded from the file
	rendered  int // labels rendered while active
	iconHits  int
	summary   string // for the start-up line, set at the first frame
}

// frameCacheOff reports whether JUKAHUB_FRAME_CACHE turns the cache off.
func frameCacheOff() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_FRAME_CACHE"))) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}

// frameCachePath is the file, "" without a data folder.
func frameCachePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, frameCacheFileName)
}

// frameCacheStamp names what the pixels depend on beyond their keys: this
// build (its rasterisers and icon set) and the SDL it runs on. Release builds
// stamp only the day (buildDate) and a local build nothing, so two builds of
// one day would pass for each other and the newer one would draw the older
// one's icons: the executable's own size and time tell them apart. "" when
// the executable cannot be named, and then nothing is read or written. Any
// goroutine (no SDL call: the version was asked by frameCacheStart).
func frameCacheStamp() string {
	exe, err := os.Executable()
	if err != nil || frameCache.sdlVersion == "" {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("build %s %s %d:%d · sdl %s", strings.TrimSpace(buildTime), strings.TrimSpace(buildDate),
		fi.Size(), fi.ModTime().UnixNano(), frameCache.sdlVersion)
}

// frameCacheStart switches recording on and starts the loader. Main thread,
// once, before any font is opened (startupPreload).
func frameCacheStart() {
	if frameCacheOff() {
		return
	}
	var v sdl.Version
	sdl.GetVersion(&v) // callable before SDL_Init
	frameCache.sdlVersion = fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	frameCache.active.Store(true)
	go frameCacheLoad()
}

// frameCacheLoad reads the last start's file and prepares it: labels expanded
// to pixels, icons rasterised. It touches no SDL object. It runs before the
// boot guard has counted this start, so nothing in it may end the process: a
// panic only costs the cache.
func frameCacheLoad() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("startup", "first-frame cache not used: %v", r)
		}
	}()
	path, stamp := frameCachePath(), frameCacheStamp()
	if path == "" || stamp == "" {
		return
	}
	started := time.Now()
	f, err := os.Open(path)
	if err != nil {
		return
	}
	data, err := readFrameCache(io.LimitReader(f, frameCacheFileMax), stamp)
	_ = f.Close()
	if err != nil {
		logInfo("startup", "first-frame cache not used: %v", err)
		return
	}
	for _, key := range data.order {
		l := data.labels[key]
		l.pixels = make([]uint32, len(l.alpha))
		for i, a := range l.alpha {
			rgb := l.clear
			if a != 0 {
				rgb = l.ink
			}
			l.pixels[i] = uint32(a)<<24 | rgb
		}
	}
	var cv vecCanvas
	var vp vecPath
	for _, k := range data.iconKeys {
		size := int(k.size)
		mask := make([]uint8, size*size)
		iconRasterize(k.name, size, mask, &cv, &vp)
		data.icons[k] = mask
	}
	if frameCache.active.Load() {
		frameCache.loaded.Store(data)
		// The first frame may have come between the check and the store:
		// nothing would ever drop the data then.
		if !frameCache.active.Load() {
			frameCache.loaded.Store(nil)
		}
	}
	logDebug("startup", "first-frame cache ready: %d labels, %d icons in %d ms", len(data.order), len(data.iconKeys), time.Since(started).Milliseconds())
}

// frameCacheFontOpened remembers which face font is, while recording.
// home_fonts.go openUIFontFile calls it for every face it opens.
func frameCacheFontOpened(font *ttf.Font, source *homeUIFontSource, size int) {
	if font == nil || source == nil || !frameCache.active.Load() {
		return
	}
	id := source.key + "|" + strconv.FormatInt(source.size, 10) + "|" + strconv.FormatInt(source.mod.UnixNano(), 10) +
		"|" + strconv.Itoa(size)
	frameCache.mu.Lock()
	if frameCache.faces == nil {
		frameCache.faces = make(map[*ttf.Font]string)
	}
	frameCache.faces[font] = id
	frameCache.mu.Unlock()
}

// frameCacheLabelKey is the label's identity, "" when the face was not opened
// while recording (it cannot be named).
func frameCacheLabelKey(font *ttf.Font, text string, color sdl.Color) string {
	frameCache.mu.Lock()
	face, ok := frameCache.faces[font]
	frameCache.mu.Unlock()
	if !ok || len(text) > frameCacheMaxKey {
		return ""
	}
	b := make([]byte, 0, len(face)+len(text)+48)
	b = append(b, face...)
	b = append(b, "|h"...)
	b = strconv.AppendInt(b, int64(font.Height()), 10)
	b = append(b, "|s"...)
	b = strconv.AppendInt(b, int64(font.GetStyle()), 10)
	b = append(b, "|o"...)
	b = strconv.AppendInt(b, int64(font.GetOutline()), 10)
	b = append(b, "|n"...)
	b = strconv.AppendInt(b, int64(font.GetHinting()), 10)
	if font.GetKerning() {
		b = append(b, "|k"...)
	}
	b = append(b, "|c"...)
	b = strconv.AppendUint(b, uint64(color.R)<<24|uint64(color.G)<<16|uint64(color.B)<<8|uint64(color.A), 16)
	b = append(b, '|')
	b = append(b, text...)
	if len(b) > frameCacheMaxKey {
		return ""
	}
	return string(b)
}

// renderLabelTexture is font.RenderUTF8Blended plus CreateTextureFromSurface
// for one single-line label, the text caches' miss path (core.go drawTextRun,
// homelayout.go drawTextRaw, hometabs_draw.go). Until the first frame the
// label comes from the last start's first frame when it is there, and is
// remembered for the next one when it is not. Main thread.
func renderLabelTexture(renderer *sdl.Renderer, font *ttf.Font, text string, color sdl.Color) (*sdl.Texture, int32, int32, error) {
	if font == nil {
		return nil, 0, 0, errors.New("text: no font")
	}
	key := ""
	if frameCache.active.Load() {
		key = frameCacheLabelKey(font, text, color)
		if key != "" {
			if tex, w, h, ok := frameCacheTake(renderer, key); ok {
				return tex, w, h, nil
			}
		}
	}
	surface, err := font.RenderUTF8Blended(text, color)
	if err != nil {
		return nil, 0, 0, err
	}
	if surface == nil {
		return nil, 0, 0, errors.New("text: no surface")
	}
	defer surface.Free()
	if key != "" {
		frameCacheNote(key, surface)
	}
	texture, err := renderer.CreateTextureFromSurface(surface)
	if err != nil {
		return nil, 0, 0, err
	}
	if texture == nil {
		return nil, 0, 0, errors.New("text: no texture")
	}
	return texture, surface.W, surface.H, nil
}

// frameCacheTake uploads the remembered label key, if there is one. The
// texture is what CreateTextureFromSurface makes of a Blended label: ARGB8888,
// static, blended.
func frameCacheTake(renderer *sdl.Renderer, key string) (*sdl.Texture, int32, int32, bool) {
	data := frameCache.loaded.Load()
	if data == nil || renderer == nil {
		return nil, 0, 0, false
	}
	l := data.labels[key]
	if l == nil || len(l.pixels) == 0 {
		return nil, 0, 0, false
	}
	tex, err := renderer.CreateTexture(uint32(sdl.PIXELFORMAT_ARGB8888), sdl.TEXTUREACCESS_STATIC, l.w, l.h)
	if err != nil || tex == nil {
		return nil, 0, 0, false
	}
	if err := tex.UpdateRGBA(nil, l.pixels, int(l.w)); err != nil {
		_ = tex.Destroy()
		return nil, 0, 0, false
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	frameCacheUse(key, l, true)
	return tex, l.w, l.h, true
}

// frameCacheNote keeps a label just rendered: its coverage, when every covered
// pixel has one colour and every uncovered pixel another (what SDL_ttf's
// Blended mode makes); anything else is left out.
func frameCacheNote(key string, surface *sdl.Surface) {
	if surface.Format == nil || surface.Format.Format != uint32(sdl.PIXELFORMAT_ARGB8888) || surface.Format.BytesPerPixel != 4 {
		return
	}
	w, h, pitch := int(surface.W), int(surface.H), int(surface.Pitch)
	if w <= 0 || h <= 0 || w > 0xFFFF || h > 0xFFFF || w*h > frameCacheMaxLabel || pitch < w*4 {
		return
	}
	px := surface.Pixels()
	if len(px) < pitch*(h-1)+w*4 {
		return
	}
	alpha := make([]byte, w*h)
	var ink, blank uint32
	haveInk, haveBlank := false, false
	for y := 0; y < h; y++ {
		row := px[y*pitch : y*pitch+w*4]
		for x := 0; x < w; x++ {
			v := binary.NativeEndian.Uint32(row[x*4:])
			a, rgb := byte(v>>24), v&0x00FFFFFF
			alpha[y*w+x] = a
			switch {
			case a != 0 && !haveInk:
				ink, haveInk = rgb, true
			case a != 0 && rgb != ink:
				return
			case a == 0 && !haveBlank:
				blank, haveBlank = rgb, true
			case a == 0 && rgb != blank:
				return
			}
		}
	}
	frameCacheUse(key, &frameCacheLabel{w: int32(w), h: int32(h), ink: ink, clear: blank, alpha: alpha}, false)
}

// frameCacheUse records that the first frame drew label key.
func frameCacheUse(key string, l *frameCacheLabel, hit bool) {
	frameCache.mu.Lock()
	defer frameCache.mu.Unlock()
	if hit {
		frameCache.hits++
	} else {
		frameCache.rendered++
	}
	if frameCache.used == nil {
		frameCache.used = make(map[string]*frameCacheLabel)
	}
	if _, ok := frameCache.used[key]; ok {
		return
	}
	if len(frameCache.used) >= frameCacheMaxLabels || frameCache.usedBytes+len(l.alpha) > frameCacheMaxBytes {
		return
	}
	frameCache.used[key] = l
	frameCache.usedOrder = append(frameCache.usedOrder, key)
	frameCache.usedBytes += len(l.alpha)
}

// frameCacheIconMask fills dst (size×size) with the mask the loader
// rasterised for key and reports whether it did; either way it records the
// icon. iconatlas.go bakeLocked calls it before rasterising.
func frameCacheIconMask(key iconAtlasKey, dst []uint8) bool {
	if !frameCache.active.Load() {
		return false
	}
	frameCache.mu.Lock()
	if frameCache.iconSeen == nil {
		frameCache.iconSeen = make(map[iconAtlasKey]bool)
	}
	if !frameCache.iconSeen[key] && len(frameCache.icons) < frameCacheMaxIcons {
		frameCache.iconSeen[key] = true
		frameCache.icons = append(frameCache.icons, key)
	}
	frameCache.mu.Unlock()
	data := frameCache.loaded.Load()
	if data == nil {
		return false
	}
	mask := data.icons[key]
	if len(mask) == 0 || len(mask) != len(dst) {
		return false
	}
	copy(dst, mask)
	frameCache.mu.Lock()
	frameCache.iconHits++
	frameCache.mu.Unlock()
	return true
}

// frameCacheFirstFrame ends recording at the first presented frame and, when
// this start's first frame differs from the file, writes it for the next start
// on a goroutine. startupFirstFrameReached calls it, once.
func frameCacheFirstFrame() {
	if !frameCache.active.Swap(false) {
		return
	}
	prev := frameCache.loaded.Swap(nil)
	frameCache.mu.Lock()
	used, order, icons := frameCache.used, frameCache.usedOrder, frameCache.icons
	hits, rendered, iconHits := frameCache.hits, frameCache.rendered, frameCache.iconHits
	frameCache.used, frameCache.usedOrder, frameCache.icons = nil, nil, nil
	frameCache.faces, frameCache.iconSeen = nil, nil
	frameCache.summary = fmt.Sprintf("labels=%d/%d icons=%d/%d", hits, hits+rendered, iconHits, len(icons))
	frameCache.mu.Unlock()

	if !frameCacheWorthWriting(prev, order, icons) {
		return
	}
	go func() {
		path, stamp := frameCachePath(), frameCacheStamp()
		if path == "" || stamp == "" {
			return
		}
		var buf bytes.Buffer
		if err := writeFrameCache(&buf, stamp, order, used, icons); err != nil {
			return
		}
		if err := AtomicWrite(path, buf.Bytes(), 0o644); err != nil {
			logWarn("startup", "first-frame cache not saved: %v", err)
		}
	}()
}

// frameCacheRewriteSlack is how many new labels a start may draw without the
// file being rewritten: the header's clock (and a battery percentage) differ
// at almost every start, and rewriting the file for them would put two synced
// writes on the card after every start for one label each.
const frameCacheRewriteSlack = 4

// frameCacheWorthWriting reports whether the file should be replaced: this
// start rendered more than a few labels the file did not have, drew other
// icons, or left a quarter or more of the file unused.
func frameCacheWorthWriting(prev *frameCacheData, order []string, icons []iconAtlasKey) bool {
	if len(order) == 0 && len(icons) == 0 {
		return false
	}
	if prev == nil || len(icons) != len(prev.iconKeys) {
		return true
	}
	for _, k := range icons {
		if _, ok := prev.icons[k]; !ok {
			return true
		}
	}
	fresh := 0 // this start's labels the file did not have
	for _, k := range order {
		if _, ok := prev.labels[k]; !ok {
			fresh++
		}
	}
	if fresh > frameCacheRewriteSlack {
		return true
	}
	unused := len(prev.order) - (len(order) - fresh)
	return unused > 0 && unused*4 >= len(prev.order)
}

// frameCacheSummary is the start-up line's part, "" before the first frame or
// with the cache off.
func frameCacheSummary() string {
	frameCache.mu.Lock()
	defer frameCache.mu.Unlock()
	return frameCache.summary
}

// ── File ──────────────────────────────────────────────────────────────────
//
// The magic, then a deflated body: the stamp, the labels (key, width, height,
// ink, clear, coverage), the icons (name, size) and the CRC-32 of all of it
// (deflate itself has no checksum: a bit flipped on the card could otherwise
// become a label's pixels). Integers are little-endian.

func writeFrameCache(w io.Writer, stamp string, order []string, labels map[string]*frameCacheLabel, icons []iconAtlasKey) error {
	if _, err := io.WriteString(w, frameCacheMagic); err != nil {
		return err
	}
	zw, err := flate.NewWriter(w, flate.BestSpeed)
	if err != nil {
		return err
	}
	sum := crc32.NewIEEE()
	out := io.MultiWriter(zw, sum)
	var scratch [4]byte
	putStr := func(s string) {
		binary.LittleEndian.PutUint16(scratch[:2], uint16(len(s)))
		_, _ = out.Write(scratch[:2])
		_, _ = io.WriteString(out, s)
	}
	put16 := func(v int) {
		binary.LittleEndian.PutUint16(scratch[:2], uint16(v))
		_, _ = out.Write(scratch[:2])
	}
	put32 := func(v uint32) {
		binary.LittleEndian.PutUint32(scratch[:4], v)
		_, _ = out.Write(scratch[:4])
	}
	putStr(stamp)
	var keep []string
	for _, key := range order {
		if l := labels[key]; l != nil && len(key) <= frameCacheMaxKey && int(l.w)*int(l.h) == len(l.alpha) {
			keep = append(keep, key)
		}
	}
	put32(uint32(len(keep)))
	for _, key := range keep {
		l := labels[key]
		putStr(key)
		put16(int(l.w))
		put16(int(l.h))
		put32(l.ink)
		put32(l.clear)
		_, _ = out.Write(l.alpha)
	}
	put32(uint32(len(icons)))
	for _, k := range icons {
		putStr(k.name)
		put16(int(k.size))
	}
	binary.LittleEndian.PutUint32(scratch[:4], sum.Sum32())
	if _, err := zw.Write(scratch[:4]); err != nil {
		return err
	}
	return zw.Close()
}

// readFrameCache reads a file written by writeFrameCache for this stamp.
// Every count and size is checked against the limits before anything is
// allocated, so a damaged or foreign file is refused, never trusted.
func readFrameCache(r io.Reader, stamp string) (*frameCacheData, error) {
	magic := make([]byte, len(frameCacheMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != frameCacheMagic {
		return nil, errors.New("not a first-frame cache")
	}
	zr := flate.NewReader(r)
	defer zr.Close()
	limited := io.LimitReader(zr, frameCacheMaxBytes+(frameCacheMaxLabels+frameCacheMaxIcons)*(frameCacheMaxKey+16)+4096)
	sum := crc32.NewIEEE()
	body := io.TeeReader(limited, sum)
	var scratch [4]byte
	get16 := func() (int, error) {
		if _, err := io.ReadFull(body, scratch[:2]); err != nil {
			return 0, err
		}
		return int(binary.LittleEndian.Uint16(scratch[:2])), nil
	}
	get32 := func() (uint32, error) {
		if _, err := io.ReadFull(body, scratch[:4]); err != nil {
			return 0, err
		}
		return binary.LittleEndian.Uint32(scratch[:4]), nil
	}
	getStr := func() (string, error) {
		n, err := get16()
		if err != nil {
			return "", err
		}
		if n > frameCacheMaxKey {
			return "", errors.New("first-frame cache: key too long")
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(body, b); err != nil {
			return "", err
		}
		return string(b), nil
	}
	got, err := getStr()
	if err != nil {
		return nil, err
	}
	if got != stamp {
		return nil, errors.New("written by another build")
	}
	n, err := get32()
	if err != nil {
		return nil, err
	}
	if n > frameCacheMaxLabels {
		return nil, errors.New("first-frame cache: too many labels")
	}
	data := &frameCacheData{stamp: stamp, labels: make(map[string]*frameCacheLabel, n), icons: map[iconAtlasKey][]uint8{}}
	total := 0
	for i := uint32(0); i < n; i++ {
		key, err := getStr()
		if err != nil {
			return nil, err
		}
		w, err := get16()
		if err != nil {
			return nil, err
		}
		h, err := get16()
		if err != nil {
			return nil, err
		}
		ink, err := get32()
		if err != nil {
			return nil, err
		}
		blank, err := get32()
		if err != nil {
			return nil, err
		}
		if w <= 0 || h <= 0 || w*h > frameCacheMaxLabel || total+w*h > frameCacheMaxBytes || ink > 0xFFFFFF || blank > 0xFFFFFF {
			return nil, errors.New("first-frame cache: bad label")
		}
		alpha := make([]byte, w*h)
		if _, err := io.ReadFull(body, alpha); err != nil {
			return nil, err
		}
		total += w * h
		if _, dup := data.labels[key]; dup {
			continue
		}
		data.labels[key] = &frameCacheLabel{w: int32(w), h: int32(h), ink: ink, clear: blank, alpha: alpha}
		data.order = append(data.order, key)
	}
	m, err := get32()
	if err != nil {
		return nil, err
	}
	if m > frameCacheMaxIcons {
		return nil, errors.New("first-frame cache: too many icons")
	}
	for i := uint32(0); i < m; i++ {
		name, err := getStr()
		if err != nil {
			return nil, err
		}
		size, err := get16()
		if err != nil {
			return nil, err
		}
		if size <= 0 || size > int(iconAtlasMaxGlyph) {
			return nil, errors.New("first-frame cache: bad icon")
		}
		k := iconAtlasKey{name: name, size: int32(size)}
		if _, dup := data.icons[k]; dup {
			continue
		}
		data.icons[k] = nil
		data.iconKeys = append(data.iconKeys, k)
	}
	want := sum.Sum32()
	if _, err := io.ReadFull(limited, scratch[:4]); err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(scratch[:4]) != want {
		return nil, errors.New("first-frame cache: damaged")
	}
	return data, nil
}
