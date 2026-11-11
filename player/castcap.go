package main

// castcap.go — where Screen Share's pictures come from, and the goroutines
// that turn them into JPEGs.
//
// Remote Cast encoded every frame inside the render loop: a 1280x720 frame
// through Go's JPEG encoder costs well over 100 ms on the Smart Pro's A53, so
// the UI stuttered for as long as anyone watched. Now the render loop only
// copies a finished frame into one reusable buffer — and only when a frame is
// due and the encoder has finished the previous one — and a single goroutine
// scales and encodes it. The phone page's PNG screenshot goes through the same
// goroutine, because encoding it on the render thread stalled a frame too.
//
// While a game owns the screen the render loop parks (gamesuspend.go) and the
// capture never runs, which froze the share on the last menu frame. A
// supervisor goroutine covers that: it reads the panel's framebuffer — through
// the bundled ffmpeg when it is installed, else castcap_fb.go's own reader —
// so a TV can show the handheld while it plays, the job the Smart Pro's
// missing HDMI port would do.

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// ── Settings ──────────────────────────────────────────────────────────────

// castPreset is one picture quality the panel offers. Width is the shared
// picture's width in pixels (0 keeps the screen's own), FPS the most frames a
// second the share sends, Quality the JPEG quality.
type castPreset struct {
	Name    string
	Label   string
	Width   int
	FPS     int
	Quality int
}

// castPresets are the panel's stops, smoothest first. Smooth is the handheld's
// default because the handheld's CPU is the encoder: 640 px at 20 fps costs
// about what a few full-size frames did, and on a TV across the room a fluid
// picture reads better than a sharp one that stutters.
var castPresets = []castPreset{
	{Name: "smooth", Label: "Smooth", Width: 640, FPS: 20, Quality: 60},
	{Name: "balanced", Label: "Balanced", Width: 960, FPS: 15, Quality: 65},
	{Name: "sharp", Label: "Sharp", Width: 0, FPS: 10, Quality: 75},
}

// The share's settings, kept in the user config's Custom map.
const (
	castQualityKey = "castQuality" // smooth | balanced | sharp
	castGamesKey   = "castGames"   // share games too (handheld)
	castRotateKey  = "castRotate"  // 0/90/180/270: turn a game's picture clockwise
)

// castPresetIndex finds a preset by name, -1 when there is none.
func castPresetIndex(name string) int {
	name = strings.ToLower(strings.TrimSpace(name))
	for i, p := range castPresets {
		if p.Name == name {
			return i
		}
	}
	return -1
}

// castDefaultQuality is the preset a fresh install shares at: smooth where the
// handheld's own CPU encodes, balanced on a desktop.
func castDefaultQuality(handheld bool) string {
	if handheld {
		return "smooth"
	}
	return "balanced"
}

// castDefaultPreset is castDefaultQuality's preset for this platform.
func castDefaultPreset() castPreset {
	return castPresets[castPresetIndex(castDefaultQuality(P().IsHandheld()))]
}

// castPresetFor reads the Quality setting. Main thread: it reads the config.
func castPresetFor(config *Config) castPreset {
	name := ""
	if config != nil {
		name = customStringValue(config.Variables.Custom, castQualityKey)
	}
	if i := castPresetIndex(name); i >= 0 {
		return castPresets[i]
	}
	return castDefaultPreset()
}

// castGamesSupported reports whether a game's picture can be read at all: the
// framebuffer reader needs the handheld's Linux.
func castGamesSupported() bool { return runtime.GOOS == "linux" }

// castGamesFor reads "Games too": on unless turned off, where it can work.
func castGamesFor(config *Config) bool {
	if !castGamesSupported() {
		return false
	}
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, castGamesKey, true)
}

// castParseRotate reads a castRotate value: a quarter turn clockwise count in
// degrees, or -1 (anything else) for "decide from the framebuffer's shape".
func castParseRotate(raw string) int {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0":
		return 0
	case "90":
		return 90
	case "180":
		return 180
	case "270", "-90":
		return 270
	}
	return -1
}

// castRotateFor reads the castRotate setting.
func castRotateFor(config *Config) int {
	if config == nil {
		return -1
	}
	return castParseRotate(customStringValue(config.Variables.Custom, castRotateKey))
}

// castLive is what the HTTP handlers and the share's goroutines read. The
// config map belongs to the main thread, so the main thread copies the share's
// settings here (castSyncSettings) and nothing else touches the map.
var castLive struct {
	preset    atomic.Int32 // index into castPresets
	presetOK  atomic.Bool  // preset has been published at least once
	games     atomic.Bool  // "Games too"
	rotate    atomic.Int32 // castRotate, -1 = automatic
	canvasW   atomic.Int32 // the UI canvas, for guessing a framebuffer's shape
	canvasH   atomic.Int32
	toolsPath atomic.Value // string: the config's tools_path, for finding ffmpeg
	source    atomic.Int32 // castSource*
	fbBlack   atomic.Bool  // the game's framebuffer has shown nothing but black
	fbFailed  atomic.Bool  // the game's picture could not be read at all
}

// castLivePreset is the preset the share runs at right now. Safe from any
// goroutine.
func castLivePreset() castPreset {
	if castLive.presetOK.Load() {
		if i := int(castLive.preset.Load()); i >= 0 && i < len(castPresets) {
			return castPresets[i]
		}
	}
	return castDefaultPreset()
}

// castSync throttles castTick. Main thread only.
var castSync struct {
	at time.Time
}

// castSyncSettings publishes the share's settings for the other goroutines.
// Main thread: it reads the config.
func castSyncSettings(config *Config) {
	p := castPresetFor(config)
	castLive.preset.Store(int32(castPresetIndex(p.Name)))
	castLive.presetOK.Store(true)
	castLive.games.Store(castGamesFor(config))
	castLive.rotate.Store(int32(castRotateFor(config)))
	castLive.canvasW.Store(screenWidth)
	castLive.canvasH.Store(screenHeight)
	tools := ""
	if config != nil {
		tools = config.Variables.ToolsPath
	}
	castLive.toolsPath.Store(tools)
}

// castTick keeps castLive current while the share runs: twice a second is
// plenty for settings that only change from the panel, which syncs at once.
// The first frame also restores the saved share token, so a TV's bookmarked
// link works whether or not sharing was ever opened this session.
func castTick() {
	castTokenLoad(appConfig)
	if !castIsEnabled() {
		return
	}
	now := time.Now()
	if now.Sub(castSync.at) < 500*time.Millisecond {
		return
	}
	castSync.at = now
	castSyncSettings(appConfig)
}

// castFindFFmpeg is the bundled ffmpeg, never a download: a share must not
// wait on a network install. "" when it is not there. It stats files and
// searches PATH, so the supervisor goroutine runs it, with the tools_path the
// main thread published (castLive.toolsPath) — never the live config.
func castFindFFmpeg(toolsPath string) string {
	path := strings.TrimSpace(getToolPath("ffmpeg", &Config{Variables: Variables{ToolsPath: toolsPath}}))
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return ""
	}
	return path
}

// The share's picture comes from one of these.
const (
	castSourceOff    int32 = iota // not sharing
	castSourceUI                  // JukaHub's own frames
	castSourceGame                // the framebuffer, while a game plays
	castSourcePaused              // a game is on screen and is not being shared
)

// castSourceName is a source's word in /status.
func castSourceName(s int32) string {
	switch s {
	case castSourceUI:
		return "ui"
	case castSourceGame:
		return "game"
	case castSourcePaused:
		return "paused"
	}
	return "off"
}

// ── Who is watching ───────────────────────────────────────────────────────

// castWatcherCount is how many browsers watch: the open streams plus the
// browsers polling single frames (companion_cast.go castPollerCount).
func castWatcherCount() int {
	return castViewerCount() + castPollerCount()
}

// ── Frame statistics (the panel's "fps · kB/s") ───────────────────────────

const castStatSlots = 64

var castStats struct {
	mu    sync.Mutex
	at    [castStatSlots]time.Time
	size  [castStatSlots]int
	next  int
	count int
	w, h  int
}

// castNoteFrame records one published frame.
func castNoteFrame(size, w, h int) {
	castStats.mu.Lock()
	castStats.at[castStats.next] = time.Now()
	castStats.size[castStats.next] = size
	castStats.next = (castStats.next + 1) % castStatSlots
	if castStats.count < castStatSlots {
		castStats.count++
	}
	if w > 0 && h > 0 {
		castStats.w, castStats.h = w, h
	}
	castStats.mu.Unlock()
}

// castRates is the share's frame rate and kilobytes a second (per viewer)
// over the last two seconds.
func castRates() (fps, kbps float64) {
	const window = 2 * time.Second
	castStats.mu.Lock()
	defer castStats.mu.Unlock()
	now := time.Now()
	frames, total := 0, 0
	for i := 1; i <= castStats.count; i++ {
		idx := (castStats.next - i + castStatSlots) % castStatSlots
		if now.Sub(castStats.at[idx]) > window {
			break
		}
		frames++
		total += castStats.size[idx]
	}
	secs := window.Seconds()
	return float64(frames) / secs, float64(total) / 1024 / secs
}

// castLastFrameSize is the newest frame's size in pixels.
func castLastFrameSize() (int, int) {
	castStats.mu.Lock()
	defer castStats.mu.Unlock()
	return castStats.w, castStats.h
}

// ── Scaling and encoding ──────────────────────────────────────────────────

// castPixLayout says where each channel sits in a 4-byte source pixel: SDL's
// RGBA readback, the framebuffer's BGRA, or whatever a driver reports.
type castPixLayout struct {
	bpp     int // bytes per pixel
	r, g, b int // byte index of each channel
}

var (
	castLayoutRGBA = castPixLayout{bpp: 4, r: 0, g: 1, b: 2}
	castLayoutBGRA = castPixLayout{bpp: 4, r: 2, g: 1, b: 0}
)

// castTargetSize is the shared picture's size for a w×h source turned by deg
// degrees: the preset's width (never wider than the source, so nothing is
// ever scaled up) and the height that keeps the aspect.
func castTargetSize(w, h, presetWidth, deg int) (int, int) {
	if deg == 90 || deg == 270 {
		w, h = h, w
	}
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	tw := w
	if presetWidth > 0 && presetWidth < w {
		tw = presetWidth
	}
	th := (h*tw + w/2) / w
	if th < 1 {
		th = 1
	}
	return tw, th
}

// castBoxBounds fills dst with the source edges of to cells spread over from
// pixels: cell i covers [dst[i], dst[i+1]).
func castBoxBounds(dst []int, from, to int) []int {
	if cap(dst) < to+1 {
		dst = make([]int, to+1)
	}
	dst = dst[:to+1]
	for i := 0; i <= to; i++ {
		dst[i] = i * from / to
	}
	return dst
}

// castBoxScale averages src (w×h pixels at stride bytes a row, laid out as
// lay) into dst, whose bounds are the target size. Every source pixel lands in
// exactly one target pixel, so the 1px lines of a pixel-art game fade instead
// of vanishing the way point sampling drops them.
func castBoxScale(dst *image.RGBA, src []byte, stride, w, h int, lay castPixLayout, xb, yb []int) {
	dw, dh := dst.Rect.Dx(), dst.Rect.Dy()
	if dw == w && dh == h && lay == castLayoutRGBA {
		for y := 0; y < h; y++ {
			copy(dst.Pix[y*dst.Stride:y*dst.Stride+w*4], src[y*stride:y*stride+w*4])
		}
		return
	}
	bpp := lay.bpp
	for dy := 0; dy < dh; dy++ {
		y0, y1 := yb[dy], yb[dy+1]
		if y1 <= y0 {
			y1 = y0 + 1
		}
		out := dst.Pix[dy*dst.Stride : dy*dst.Stride+dw*4]
		for dx := 0; dx < dw; dx++ {
			x0, x1 := xb[dx], xb[dx+1]
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b uint32
			for y := y0; y < y1; y++ {
				row := src[y*stride+x0*bpp : y*stride+x1*bpp]
				for i := 0; i+bpp <= len(row); i += bpp {
					r += uint32(row[i+lay.r])
					g += uint32(row[i+lay.g])
					b += uint32(row[i+lay.b])
				}
			}
			n := uint32((x1 - x0) * (y1 - y0))
			o := dx * 4
			out[o] = uint8(r / n)
			out[o+1] = uint8(g / n)
			out[o+2] = uint8(b / n)
			out[o+3] = 255
		}
	}
}

// castRotateRGBA turns src clockwise by deg into dst, which is already sized
// for the result.
func castRotateRGBA(dst, src *image.RGBA, deg int) {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			dx, dy := x, y
			switch deg {
			case 90:
				dx, dy = sh-1-y, x
			case 180:
				dx, dy = sw-1-x, sh-1-y
			case 270:
				dx, dy = y, sw-1-x
			}
			si := y*src.Stride + x*4
			di := dy*dst.Stride + dx*4
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
}

// castEnsureRGBA returns img when it already has the size, else a new image.
func castEnsureRGBA(img *image.RGBA, w, h int) *image.RGBA {
	if img != nil && img.Rect.Dx() == w && img.Rect.Dy() == h {
		return img
	}
	return image.NewRGBA(image.Rect(0, 0, w, h))
}

// castKeepAlive is how often a picture that has not changed is sent again
// (castEncoder.dedupe): often enough that a browser joining a still menu gets
// a picture within a second, rarely enough that a still menu costs nothing.
const castKeepAlive = time.Second

// castEncoder is one producer's scratch for scaling and encoding. The UI
// encoder and the framebuffer reader each own one, so nothing in it is shared.
type castEncoder struct {
	scaled *image.RGBA
	turned *image.RGBA
	out    bytes.Buffer
	xb, yb []int
	boxKey [4]int

	// dedupe skips encoding a picture identical to the last one: a menu that
	// sits still was encoded and sent at the full frame rate, costing the
	// handheld a JPEG encode and every viewer ~0.5 MB/s for nothing. prev is
	// the last encoded picture's pixels and prevKey what it was made for;
	// prevJPEG is sent again as the keep-alive.
	dedupe   bool
	prev     []byte
	prevKey  [5]int
	prevJPEG []byte
	prevAt   time.Time
}

// jpeg scales and turns one frame and encodes it: src holds w×h pixels laid
// out as lay at stride bytes a row. The returned bytes belong to the caller
// (a published frame is read by every stream at once, so it is never reused).
// With dedupe on, a picture identical to the previous one returns no bytes,
// or the previous JPEG again once castKeepAlive has passed.
func (e *castEncoder) jpeg(src []byte, stride, w, h int, lay castPixLayout, p castPreset, deg int) ([]byte, int, int, error) {
	if w <= 0 || h <= 0 || lay.bpp <= 0 || stride < w*lay.bpp || len(src) < stride*(h-1)+w*lay.bpp {
		return nil, 0, 0, errors.New("the frame has no pixels")
	}
	tw, th := castTargetSize(w, h, p.Width, deg)
	// Scale first, turn second: turning the small picture is the cheap order.
	sw, sh := tw, th
	if deg == 90 || deg == 270 {
		sw, sh = th, tw
	}
	e.scaled = castEnsureRGBA(e.scaled, sw, sh)
	if key := [4]int{w, h, sw, sh}; key != e.boxKey || len(e.xb) != sw+1 || len(e.yb) != sh+1 {
		e.xb = castBoxBounds(e.xb, w, sw)
		e.yb = castBoxBounds(e.yb, h, sh)
		e.boxKey = key
	}
	castBoxScale(e.scaled, src, stride, w, h, lay, e.xb, e.yb)
	img := e.scaled
	if deg == 90 || deg == 180 || deg == 270 {
		e.turned = castEnsureRGBA(e.turned, tw, th)
		castRotateRGBA(e.turned, e.scaled, deg)
		img = e.turned
	}
	quality := p.Quality
	if quality <= 0 || quality > 100 {
		quality = 65
	}
	key := [5]int{tw, th, quality, deg, p.Width}
	if e.dedupe && key == e.prevKey && len(e.prevJPEG) > 0 && bytes.Equal(img.Pix, e.prev) {
		// bytes.Equal is a memory compare, a fraction of a millisecond next to
		// the tens an encode costs on the A53.
		if time.Since(e.prevAt) < castKeepAlive {
			return nil, tw, th, nil
		}
		e.prevAt = time.Now()
		return e.prevJPEG, tw, th, nil
	}
	e.out.Reset()
	// *image.RGBA is the encoder's fast path; the NRGBA Remote Cast handed it
	// went through the generic per-pixel At() route.
	if err := jpeg.Encode(&e.out, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, 0, err
	}
	frame := append([]byte(nil), e.out.Bytes()...)
	if e.dedupe {
		e.prev = append(e.prev[:0], img.Pix...)
		e.prevKey = key
		e.prevJPEG = frame
		e.prevAt = time.Now()
	}
	return frame, tw, th, nil
}

// castPNG encodes a whole RGBA frame for the phone page's screenshot. The
// back buffer's alpha is whatever the last blend left, so a picture is made
// opaque.
func castPNG(src []byte, stride, w, h int) ([]byte, error) {
	if w <= 0 || h <= 0 || stride < w*4 || len(src) < stride*(h-1)+w*4 {
		return nil, errors.New("the screenshot has no pixels")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		copy(row, src[y*stride:y*stride+w*4])
		for i := 3; i < len(row); i += 4 {
			row[i] = 255
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ── The UI source: the render loop's frames ───────────────────────────────

// castUIJob is one frame handed from the render thread to the encoder.
// redact lists the parts of the frame, in its pixels, that viewers must not
// see (castShareRedactions); the phone's own screenshot keeps them.
type castUIJob struct {
	pix          []byte
	stride, w, h int
	png, jpeg    bool
	preset       castPreset
	redact       []sdl.Rect
}

// castUI is the render thread's half of the UI source. buf belongs to the
// render thread while busy is false and to the encoder while it is true, so
// the one buffer is never written and read at once and no frame allocates.
var castUI struct {
	once sync.Once
	jobs chan castUIJob
	busy atomic.Bool
	buf  []byte
	last time.Time // render thread only: when the share last took a frame
	// readCost is the smoothed time ReadPixels takes (render thread only).
	readCost time.Duration
}

const (
	// castUIHandheldGap caps the handheld's UI capture at about 12 frames a
	// second, whatever the preset: each capture is a GPU readback on the render
	// thread, which a menu feels before a viewer does.
	castUIHandheldGap = time.Second / 12
	// castUIReadShare is the most of the render thread readbacks may take: the
	// capture spaces out to ten times the readback's cost.
	castUIReadShare = 10
	// castUIMaxGap bounds that stretching, so even a very slow readback still
	// shows the menu moving.
	castUIMaxGap = time.Second
)

// castUIStart starts the encoder goroutine on first use.
func castUIStart() {
	castUI.once.Do(func() {
		castUI.jobs = make(chan castUIJob, 1)
		go castUIEncodeLoop()
	})
}

func castUIEncodeLoop() {
	enc := castEncoder{dedupe: true}
	for job := range castUI.jobs {
		castUIEncode(&enc, job)
		castUI.busy.Store(false)
	}
}

// castUIEncode turns one captured frame into whatever was asked for. A panic
// here must not take the encoder down with it: the share would stall on busy
// for the rest of the session.
func castUIEncode(enc *castEncoder, job castUIJob) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("cast", "encoder failed: %v", r)
		}
	}()
	if job.png {
		if frame, err := castPNG(job.pix, job.stride, job.w, job.h); err == nil {
			publishCompanionPNG(frame)
		} else {
			logDebug("cast", "screenshot not encoded: %v", err)
		}
	}
	// Sharing may have been switched off while the frame waited.
	if job.jpeg && castIsEnabled() {
		castRedact(job.pix, job.stride, job.w, job.h, job.redact)
		frame, w, h, err := enc.jpeg(job.pix, job.stride, job.w, job.h, castLayoutRGBA, job.preset, 0)
		if err != nil {
			logDebug("cast", "frame not encoded: %v", err)
			return
		}
		publishCompanionJPEG(frame, w, h)
	}
}

// castUIFrameDue reports whether the share wants a UI frame now: someone
// watches, the capture interval has passed, and the encoder is free.
func castUIFrameDue(now time.Time) bool {
	if !castIsEnabled() || castWatcherCount() == 0 || castUI.busy.Load() {
		return false
	}
	return now.Sub(castUI.last) >= castUIGap(castLivePreset().FPS, P().IsHandheld(), castUI.readCost)
}

// castUIGap is the time between two UI captures: the preset's frame interval,
// no shorter than castUIHandheldGap on the handheld, and stretched so the
// readback (readCost each) never takes more than a tenth of the render thread.
func castUIGap(fps int, handheld bool, readCost time.Duration) time.Duration {
	if fps <= 0 {
		fps = 10
	}
	gap := time.Second / time.Duration(fps)
	if handheld && gap < castUIHandheldGap {
		gap = castUIHandheldGap
	}
	if slow := readCost * castUIReadShare; slow > gap {
		gap = slow
	}
	if gap > castUIMaxGap {
		gap = castUIMaxGap
	}
	return gap
}

// castUINoteReadCost folds one readback's duration into the smoothed cost.
func castUINoteReadCost(took time.Duration) {
	if castUI.readCost <= 0 {
		castUI.readCost = took
		return
	}
	castUI.readCost = (castUI.readCost*3 + took) / 4
}

// castCaptureFrame copies the finished frame for whoever waits for one — a
// phone's screenshot request or the share — and hands it to the encoder. Render
// thread only, after the frame is drawn.
func castCaptureFrame(renderer *sdl.Renderer, now time.Time) {
	wantPNG := companionScreenPending()
	wantJPEG := castUIFrameDue(now)
	if wantJPEG && qrZoomActive() {
		// An enlarged code fills the screen, and it may be the pairing code,
		// whose link unlocks the handheld's input: viewers keep the frame
		// before it until it closes.
		wantJPEG = false
	}
	if (!wantPNG && !wantJPEG) || castUI.busy.Load() {
		return
	}
	readAt := time.Now()
	w, h, stride, sx, sy, ok := castReadFrame(renderer)
	if !ok {
		return
	}
	castUINoteReadCost(time.Since(readAt))
	castUIStart()
	var redact []sdl.Rect
	if wantJPEG {
		castUI.last = now
		redact = castShareRedactions(sx, sy)
	}
	castUI.busy.Store(true)
	job := castUIJob{pix: castUI.buf, stride: stride, w: w, h: h, png: wantPNG, jpeg: wantJPEG,
		preset: castLivePreset(), redact: redact}
	select {
	case castUI.jobs <- job:
	default:
		// busy guards the one slot, so this cannot happen; never wedge on it.
		castUI.busy.Store(false)
	}
}

// castReadFrame reads the frame on screen into castUI.buf as RGBA bytes
// (SDL's ABGR8888, which is what the GLES2 renderer reads natively, so SDL
// only copies the rows instead of swizzling every pixel). ReadPixels(nil)
// reads the viewport — the letterboxed canvas inside a resized desktop window
// — so the buffer is sized for the whole output, which the viewport never
// exceeds, and the frame is the viewport's size.
func castReadFrame(renderer *sdl.Renderer) (w, h, stride int, sx, sy float32, ok bool) {
	if renderer == nil {
		return 0, 0, 0, 1, 1, false
	}
	ow, oh, err := renderer.GetOutputSize()
	if err != nil || ow <= 0 || oh <= 0 {
		return 0, 0, 0, 1, 1, false
	}
	w, h = int(ow), int(oh)
	vp := renderer.GetViewport()
	sx, sy = renderer.GetScale()
	if sx <= 0 || sy <= 0 {
		sx, sy = 1, 1
	}
	if vp.W > 0 && vp.H > 0 {
		if pw := int(float32(vp.W) * sx); pw > 0 && pw < w {
			w = pw
		}
		if ph := int(float32(vp.H) * sy); ph > 0 && ph < h {
			h = ph
		}
	}
	stride = int(ow) * 4
	need := stride * int(oh)
	if cap(castUI.buf) < need {
		castUI.buf = make([]byte, need)
	}
	castUI.buf = castUI.buf[:need]
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ABGR8888, unsafe.Pointer(&castUI.buf[0]), stride); err != nil {
		return 0, 0, 0, 1, 1, false
	}
	return w, h, stride, sx, sy, true
}

// castRedactBuf backs castShareRedactions' answer. The render thread fills it
// only while the encoder is idle (castCaptureFrame), so the encoder never reads
// it mid-update.
var castRedactBuf [4]sdl.Rect

// castShareRedactions are the parts of this frame viewers must not see, in the
// captured buffer's pixels (sx, sy: the renderer's logical-to-output scale).
// They are the Phone Companion's pairing code, whose link carries the PIN that
// types on the handheld: Home shows it in its header all the time, and the
// pairing card shows the PIN in large digits. While the Screen Share panel is
// open its own code and link are painted over too: after Y they are the new
// link, which a viewer just shut out must not read off the picture.
// Everything else on screen is shared — the panel's privacy line says so.
func castShareRedactions(sx, sy float32) []sdl.Rect {
	scale := func(r sdl.Rect) sdl.Rect {
		return sdl.Rect{
			X: int32(float32(r.X) * sx), Y: int32(float32(r.Y) * sy),
			W: int32(float32(r.W)*sx + 0.999), H: int32(float32(r.H)*sy + 0.999),
		}
	}
	out := castRedactBuf[:0]
	if homeLayoutActive && companionHeaderRect.W > 0 {
		out = append(out, scale(companionHeaderRect))
	}
	if companion.open {
		out = append(out, scale(companionCardLayoutFor(screenWidth, screenHeight, 0, 0).card))
	}
	if castPanel.open {
		if r := castPanelRects[castFocusQR]; r.W > 0 && r.H > 0 {
			out = append(out, scale(r))
		}
		if r := castPanelInfoRect; r.W > 0 && r.H > 0 {
			out = append(out, scale(r))
		}
	}
	return out
}

// castRedact paints rects (frame pixels, clipped to the frame) with the
// backdrop's navy in an RGBA frame.
func castRedact(pix []byte, stride, w, h int, rects []sdl.Rect) {
	clampInt := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	for _, r := range rects {
		x0, x1 := clampInt(int(r.X), 0, w), clampInt(int(r.X+r.W), 0, w)
		y0, y1 := clampInt(int(r.Y), 0, h), clampInt(int(r.Y+r.H), 0, h)
		if x1 <= x0 || y1 <= y0 {
			// Empty, off the frame, or inverted (a negative size): nothing to
			// paint, and slicing the row backwards would panic.
			continue
		}
		for y := y0; y < y1; y++ {
			row := pix[y*stride+x0*4 : y*stride+x1*4]
			for i := 0; i+3 < len(row); i += 4 {
				row[i], row[i+1], row[i+2], row[i+3] = 11, 15, 23, 255
			}
		}
	}
}

// ── The LIVE pill ─────────────────────────────────────────────────────────

// castDrawLiveIndicator puts a small red LIVE pill at the top of the screen
// while anyone watches, the way a phone marks a screen recording: whoever
// holds the handheld should know they are on a TV. It is drawn after the
// capture, so viewers never see it, and not on a frame a screenshot is about
// to save.
func castDrawLiveIndicator(renderer *sdl.Renderer) {
	if renderer == nil || !castIsEnabled() || castPanel.open || shotModeOn() || screenshotRequested {
		return
	}
	n := castWatcherCount()
	if n <= 0 {
		return
	}
	font := loadHomeFonts().Small
	if font == nil {
		return
	}
	label := castLiveLabel(n)
	tw, th := measureText(font, label)
	h := th + scaleY(6, screenHeight)
	dot := h / 3
	padX := scaleX(10, screenWidth)
	w := padX + dot + scaleX(6, screenWidth) + tw + padX
	x := (screenWidth - w) / 2
	y := scaleY(6, screenHeight)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, x, y, w, h, h/2, WithAlpha(ColorDanger, 235))
	white := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	fillCircle(renderer, x+padX+dot/2, y+h/2, dot/2, white)
	drawText(renderer, font, label, x+padX+dot+scaleX(6, screenWidth), y+(h-th)/2, white, textAlignLeft)
}

// castLiveLabelCache keeps the pill's words for one watcher count, so a frame
// does not format a string while nothing changed. Render thread only.
var castLiveLabelCache struct {
	n    int
	text string
}

// castLiveLabel is the pill's text for n watchers.
func castLiveLabel(n int) string {
	if n <= 1 {
		return "LIVE"
	}
	if castLiveLabelCache.n != n || castLiveLabelCache.text == "" {
		castLiveLabelCache.n = n
		castLiveLabelCache.text = fmt.Sprintf("LIVE · %d", n)
	}
	return castLiveLabelCache.text
}

// castMaskPIN is how a screen shows the Phone Companion PIN: as it is, except
// while the screen is shared — the PIN types on the handheld, and everything on
// screen goes to the viewers.
func castMaskPIN(pin string) string {
	if pin == "" || !castIsEnabled() {
		return pin
	}
	return "hidden while Screen Share is on"
}

// ── The game source: the supervisor ───────────────────────────────────────

// castFBRetry is how long a framebuffer source that failed waits before the
// next attempt, so a firmware without a readable framebuffer is not probed
// four times a second for a whole game.
const castFBRetry = 15 * time.Second

// castPlaceholderEvery is how often the "paused" picture is re-sent while a
// game is on screen and is not shared, so a browser that connects then still
// gets a picture instead of an empty page.
const castPlaceholderEvery = 1500 * time.Millisecond

// castSup owns the supervisor goroutine, which runs while sharing is on.
var castSup struct {
	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}
}

// castSupervisorStart starts the supervisor once.
func castSupervisorStart() {
	castSup.mu.Lock()
	defer castSup.mu.Unlock()
	if castSup.stop != nil {
		return
	}
	castSup.stop = make(chan struct{})
	castSup.done = make(chan struct{})
	go castSupervise(castSup.stop, castSup.done)
}

// castSupervisorStop ends the supervisor and the framebuffer reader it runs,
// waiting a moment for an ffmpeg child to be gone: one left behind would keep
// reading the panel after sharing stopped, or after JukaHub quit.
func castSupervisorStop() {
	castSup.mu.Lock()
	stop, done := castSup.stop, castSup.done
	castSup.stop, castSup.done = nil, nil
	castSup.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		logWarn("cast", "the screen-share supervisor did not stop in time")
	}
}

// castSupervise decides, four times a second, where the share's picture comes
// from: the render loop while JukaHub is on screen, the framebuffer while a
// game plays and someone watches, or the paused picture.
func castSupervise(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var src *castFBSource
	var retryAt, placeholderAt time.Time
	// For the game on screen: the ffmpeg found when it started (looked up here,
	// off the render thread), and whether ffmpeg already failed on this panel,
	// which hands the rest of the game to JukaHub's own reader.
	var ffmpeg string
	var ffmpegLooked, skipFFmpeg bool
	stopSource := func() {
		if src != nil {
			src.Stop()
			src = nil
		}
	}
	defer stopSource()
	for {
		select {
		case <-stop:
			castLive.source.Store(castSourceOff)
			return
		case <-tick.C:
		}
		now := time.Now()
		if !gameScreenSuspended() {
			stopSource()
			retryAt = time.Time{}
			ffmpegLooked, skipFFmpeg = false, false
			castLive.fbFailed.Store(false)
			castLive.source.Store(castSourceUI)
			continue
		}
		watching := castWatcherCount() > 0
		if watching && castLive.games.Load() && castGamesSupported() {
			p := castLivePreset()
			rot := int(castLive.rotate.Load())
			if src != nil && (src.preset != p || src.rotateSetting != rot) {
				stopSource()
			}
			if src != nil && src.Finished() {
				if err := src.Err(); err != nil {
					logWarn("cast", "game sharing stopped: %v", err)
				}
				src.cancel()
				if src.viaFFmpeg && !skipFFmpeg {
					// ffmpeg could not read this panel (no fbdev input in the
					// build, a permission problem, a crash): JukaHub's own
					// reader gets its turn at once instead of ffmpeg again in
					// fifteen seconds, for as long as this game runs.
					skipFFmpeg = true
					retryAt = now
				} else {
					retryAt = now.Add(castFBRetry)
					castLive.fbFailed.Store(true)
				}
				src = nil
			}
			if src == nil && !now.Before(retryAt) {
				if !ffmpegLooked {
					tools, _ := castLive.toolsPath.Load().(string)
					ffmpeg = castFindFFmpeg(tools)
					ffmpegLooked = true
				}
				use := ffmpeg
				if skipFFmpeg {
					use = ""
				}
				s, err := castStartFBSource(p, rot, use)
				if err != nil {
					logWarn("cast", "cannot share the game's picture: %v", err)
					retryAt = now.Add(castFBRetry)
					castLive.fbFailed.Store(true)
				} else {
					src = s
					castLive.fbFailed.Store(false)
				}
			}
			if src != nil {
				castLive.source.Store(castSourceGame)
				continue
			}
		} else {
			stopSource()
		}
		castLive.source.Store(castSourcePaused)
		if watching && now.Sub(placeholderAt) >= castPlaceholderEvery {
			placeholderAt = now
			if frame := castPlaceholderJPEG(); len(frame) > 0 {
				publishCompanionJPEG(frame, castPlaceholderW, castPlaceholderH)
			}
		}
	}
}

// The paused picture's size.
const (
	castPlaceholderW = 640
	castPlaceholderH = 360
)

var castPlaceholder struct {
	once  sync.Once
	frame []byte
}

// castPlaceholderJPEG is the picture viewers get while a game is on screen and
// is not shared: the backdrop's navy with a pause mark, so a TV shows "paused"
// rather than a frozen menu. /status carries the words.
func castPlaceholderJPEG() []byte {
	castPlaceholder.once.Do(func() {
		w, h := castPlaceholderW, castPlaceholderH
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 11, G: 15, B: 23, A: 255}}, image.Point{}, draw.Src)
		bar := &image.Uniform{C: color.RGBA{R: 230, G: 232, B: 239, A: 255}}
		for _, x := range []int{w/2 - 34, w/2 + 10} {
			draw.Draw(img, image.Rect(x, h/2-40, x+24, h/2+40), bar, image.Point{}, draw.Src)
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err == nil {
			castPlaceholder.frame = buf.Bytes()
		}
	})
	return castPlaceholder.frame
}

func init() {
	// Quitting must not leave an ffmpeg child reading the panel behind it.
	Cleanup = append(Cleanup, castShutdown)
}

// castShutdown stops the share on the way out.
func castShutdown() {
	castEnabled.Store(false)
	castSupervisorStop()
}
