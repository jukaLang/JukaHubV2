package main

// castcap_fb.go — Screen Share's view of a game: the panel's framebuffer.
//
// A game draws straight to /dev/fb0 (the firmware's SDL renders through the
// mali fbdev EGL path, and timelapse.go already records play from it), so the
// framebuffer holds whatever the player sees, whoever drew it. The bundled
// ffmpeg reads and encodes it far faster than Go can — its fbdev input follows
// the double-buffer pan, and its MJPEG encoder is C — so it is used when it is
// installed. Without it the frame is read here and goes through the same
// encoder as the UI frames.
//
// Nothing here runs on Windows: castGamesSupported keeps the supervisor away,
// and the ioctls live behind a build tag (castcap_fbinfo_*.go).

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	castFBDevice = "/dev/fb0"
	castFBSysfs  = "/sys/class/graphics/fb0"
	// castMaxJPEG bounds one frame from ffmpeg. A sharp 1280x720 frame is a few
	// hundred KB, so 8 MB only ever stops a stream that has gone wrong.
	castMaxJPEG = 8 << 20
	// castBlackWindow is how long a game's framebuffer may stay black before
	// the share says so: games boot through black screens, so a black first
	// frame proves nothing.
	castBlackWindow = 10 * time.Second
)

// castFBGeom is what the framebuffer looks like.
type castFBGeom struct {
	W, H       int // the visible picture
	VW, VH     int // the whole buffer (double buffering makes it taller)
	BPP        int // bits per pixel
	Stride     int // bytes per buffer row
	XOff, YOff int // pan: where the visible picture starts
	Layout     castPixLayout
}

// valid reports whether the reader can use the geometry.
func (g castFBGeom) valid() bool {
	return g.W > 0 && g.H > 0 && (g.BPP == 32 || g.BPP == 16) && g.Stride >= g.W*g.BPP/8
}

// castFBGeomFromInfo decodes struct fb_var_screeninfo and fb_fix_screeninfo as
// the kernel lays them out on a little-endian machine. line_length follows an
// unsigned long, so its offset depends on the pointer size.
func castFBGeomFromInfo(v, fix []byte, ptrSize int) castFBGeom {
	u := func(b []byte, off int) int {
		if off < 0 || off+4 > len(b) {
			return 0
		}
		return int(binary.LittleEndian.Uint32(b[off:]))
	}
	g := castFBGeom{
		W: u(v, 0), H: u(v, 4), VW: u(v, 8), VH: u(v, 12),
		XOff: u(v, 16), YOff: u(v, 20), BPP: u(v, 24),
	}
	lineOff := 44
	if ptrSize == 8 {
		lineOff = 48
	}
	g.Stride = u(fix, lineOff)
	if g.Stride <= 0 && g.VW > 0 {
		g.Stride = g.VW * g.BPP / 8
	}
	g.Layout = castLayoutBGRA
	red, green, blue := u(v, 32), u(v, 44), u(v, 56)
	if g.BPP == 32 && red%8 == 0 && green%8 == 0 && blue%8 == 0 &&
		red <= 24 && green <= 24 && blue <= 24 && red != green && green != blue && red != blue {
		g.Layout = castPixLayout{bpp: 4, r: red / 8, g: green / 8, b: blue / 8}
	}
	return g
}

// castParseInt reads one sysfs number ("32\n").
func castParseInt(raw string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	return n, err == nil
}

// castParsePair reads a sysfs pair: "1280,1440" (virtual_size, pan) or
// "1280x720".
func castParsePair(raw string) (int, int, bool) {
	raw = strings.TrimSpace(raw)
	sep := strings.IndexAny(raw, ",x")
	if sep <= 0 {
		return 0, 0, false
	}
	a, errA := strconv.Atoi(strings.TrimSpace(raw[:sep]))
	b, errB := strconv.Atoi(strings.TrimSpace(raw[sep+1:]))
	if errA != nil || errB != nil {
		return 0, 0, false
	}
	return a, b, true
}

// castGuessVisible picks the visible part of a buffer whose mode line is
// missing: a double- or triple-buffered panel is the canvas's shape stacked
// two or three times, in either orientation.
func castGuessVisible(vw, vh, cw, ch int) (int, int) {
	if vw <= 0 || vh <= 0 || cw <= 0 || ch <= 0 {
		return vw, vh
	}
	for _, want := range []int{vw * ch / cw, vw * cw / ch} {
		if want <= 0 {
			continue
		}
		for n := 1; n <= 3; n++ {
			if vh == want*n {
				return vw, want
			}
		}
	}
	return vw, vh
}

// castFBSysfsGeom reads the geometry the kernel publishes in sysfs. It cannot
// say where the colour channels are; ARGB8888 (B, G, R, A in memory) is what
// these SoCs' fbdev drivers use.
func castFBSysfsGeom(dir string, canvasW, canvasH int) (castFBGeom, error) {
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return ""
		}
		return string(data)
	}
	var g castFBGeom
	vw, vh, ok := castParsePair(read("virtual_size"))
	if !ok {
		return g, errors.New("the framebuffer's size is unknown")
	}
	g.VW, g.VH = vw, vh
	g.BPP, _ = castParseInt(read("bits_per_pixel"))
	if stride, ok := castParseInt(read("stride")); ok && stride > 0 {
		g.Stride = stride
	} else {
		g.Stride = vw * g.BPP / 8
	}
	g.XOff, g.YOff, _ = castParsePair(read("pan"))
	if w, h, ok := parsePanelSize(read("modes")); ok {
		g.W, g.H = int(w), int(h)
	} else {
		g.W, g.H = castGuessVisible(vw, vh, canvasW, canvasH)
	}
	g.Layout = castLayoutBGRA
	return g, nil
}

// castProbeFB reads the framebuffer's geometry: from the driver when its ioctl
// answers, else from sysfs.
func castProbeFB(f *os.File, canvasW, canvasH int) (castFBGeom, error) {
	if g, err := castFBIoctlGeom(f); err == nil && g.valid() {
		return g, nil
	}
	g, err := castFBSysfsGeom(castFBSysfs, canvasW, canvasH)
	if err != nil {
		return castFBGeom{}, err
	}
	if !g.valid() {
		return castFBGeom{}, fmt.Errorf("unsupported framebuffer: %dx%d at %d bits a pixel", g.W, g.H, g.BPP)
	}
	return g, nil
}

// castFBPan is where the visible picture starts right now. A double-buffered
// panel flips between two halves of the buffer every frame, so it is read
// again for each frame, and kept inside the buffer.
func castFBPan(f *os.File, g castFBGeom) (int, int) {
	x, y, err := castFBIoctlPan(f)
	if err != nil {
		data, rerr := os.ReadFile(filepath.Join(castFBSysfs, "pan"))
		var ok bool
		if rerr == nil {
			x, y, ok = castParsePair(string(data))
		}
		if !ok {
			x, y = g.XOff, g.YOff
		}
	}
	if x < 0 || (g.VW > 0 && x+g.W > g.VW) {
		x = 0
	}
	if y < 0 || (g.VH > 0 && y+g.H > g.VH) {
		y = 0
	}
	return x, y
}

// castAutoRotate turns a portrait framebuffer for a landscape canvas. Which way
// a panel is mounted is not something the framebuffer says: a quarter turn
// anticlockwise is the guess, and castRotate fixes a wrong one.
func castAutoRotate(fbW, fbH, canvasW, canvasH int) int {
	if fbH > fbW && canvasW > canvasH {
		return 270
	}
	return 0
}

// castRGB565ToRGBA expands a 16-bit framebuffer into RGBA rows (w*4 bytes a
// row in dst).
func castRGB565ToRGBA(dst, src []byte, w, h, stride int) {
	for y := 0; y < h; y++ {
		row := src[y*stride : y*stride+w*2]
		out := dst[y*w*4 : y*w*4+w*4]
		for x := 0; x < w; x++ {
			v := uint16(row[x*2]) | uint16(row[x*2+1])<<8
			r := uint8(v >> 11 & 0x1f)
			g := uint8(v >> 5 & 0x3f)
			b := uint8(v & 0x1f)
			out[x*4] = r<<3 | r>>2
			out[x*4+1] = g<<2 | g>>4
			out[x*4+2] = b<<3 | b>>2
			out[x*4+3] = 255
		}
	}
}

// castMeanLuma is a quick brightness reading of a frame (0..255), from every
// eighth pixel of every eighth row.
func castMeanLuma(src []byte, stride, w, h int, lay castPixLayout) float64 {
	var sum, n float64
	for y := 0; y < h; y += 8 {
		for x := 0; x < w; x += 8 {
			i := y*stride + x*lay.bpp
			if i+lay.bpp > len(src) {
				continue
			}
			sum += 0.299*float64(src[i+lay.r]) + 0.587*float64(src[i+lay.g]) + 0.114*float64(src[i+lay.b])
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

// castBlack follows whether a game's framebuffer shows anything. Games boot
// through black screens, so only a framebuffer that stays black for
// castBlackWindow is reported, and one lit frame clears it.
var castBlack struct {
	mu      sync.Mutex
	since   time.Time
	lit     bool
	checkAt time.Time
}

// castBlackReset starts the watch for a new game.
func castBlackReset() {
	castBlack.mu.Lock()
	castBlack.since = time.Now()
	castBlack.lit = false
	castBlack.checkAt = time.Time{}
	castBlack.mu.Unlock()
	castLive.fbBlack.Store(false)
}

// castBlackDue reports whether the next frame should be measured: once a
// second until the picture lights up, every few seconds while it is flagged.
func castBlackDue(now time.Time) bool {
	castBlack.mu.Lock()
	defer castBlack.mu.Unlock()
	if castBlack.lit || now.Before(castBlack.checkAt) {
		return false
	}
	castBlack.checkAt = now.Add(time.Second)
	if castLive.fbBlack.Load() {
		castBlack.checkAt = now.Add(3 * time.Second)
	}
	return true
}

// castBlackNote records one measurement.
func castBlackNote(luma float64, now time.Time) {
	castBlack.mu.Lock()
	defer castBlack.mu.Unlock()
	if luma >= 2 {
		castBlack.lit = true
		castLive.fbBlack.Store(false)
		return
	}
	if !castBlack.since.IsZero() && now.Sub(castBlack.since) >= castBlackWindow {
		castLive.fbBlack.Store(true)
	}
}

// castBlackCheckJPEG measures a frame ffmpeg encoded. Decoding costs a few
// milliseconds, which is why it only happens at castBlackDue's pace.
func castBlackCheckJPEG(frame []byte) {
	now := time.Now()
	if !castBlackDue(now) {
		return
	}
	img, err := jpeg.Decode(bytes.NewReader(frame))
	if err != nil {
		return
	}
	luma := 0.0
	if yc, ok := img.(*image.YCbCr); ok {
		var sum, n float64
		b := yc.Rect
		for y := b.Min.Y; y < b.Max.Y; y += 8 {
			for x := b.Min.X; x < b.Max.X; x += 8 {
				sum += float64(yc.Y[yc.YOffset(x, y)])
				n++
			}
		}
		if n > 0 {
			luma = sum / n
		}
	} else {
		luma = 255 // not something ffmpeg sends; never call it black
	}
	castBlackNote(luma, now)
}

// ── ffmpeg's MJPEG stream ─────────────────────────────────────────────────

// castFFmpegQ maps a JPEG quality onto ffmpeg's -q:v scale (2 best .. 31).
func castFFmpegQ(quality int) int {
	q := (100 - quality + 3) / 6
	if q < 2 {
		q = 2
	}
	if q > 31 {
		q = 31
	}
	return q
}

// castFFmpegArgs is the framebuffer grab: the preset's frame rate, the turn,
// the preset's width (area-averaged, like castBoxScale) and MJPEG out on
// stdout.
func castFFmpegArgs(device string, p castPreset, deg, fbW, fbH int) []string {
	fps := p.FPS
	if fps <= 0 {
		fps = 10
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "fbdev", "-framerate", strconv.Itoa(fps), "-i", device}
	var filters []string
	outW := fbW
	switch deg {
	case 90:
		filters = append(filters, "transpose=1")
		outW = fbH
	case 180:
		filters = append(filters, "hflip", "vflip")
	case 270:
		filters = append(filters, "transpose=2")
		outW = fbH
	}
	if p.Width > 0 && (outW <= 0 || p.Width < outW) {
		filters = append(filters, fmt.Sprintf("scale=%d:-2:flags=area", p.Width))
	}
	if len(filters) > 0 {
		args = append(args, "-vf", strings.Join(filters, ","))
	}
	return append(args, "-pix_fmt", "yuvj420p", "-q:v", strconv.Itoa(castFFmpegQ(p.Quality)),
		"-f", "mjpeg", "pipe:1")
}

// castJPEGSplitter cuts a stream of back-to-back JPEGs (ffmpeg's mjpeg
// output) into whole frames, however the reads happen to cut it.
//
// The frame at the front of buf is walked once: scan is where the walk
// stopped for want of bytes, and inData whether it stopped inside
// entropy-coded data, so the next Feed carries on from there. Walking it again
// from its SOI on every 32 KB read scanned a few hundred KB frame many times
// over.
type castJPEGSplitter struct {
	buf    []byte
	max    int
	scan   int  // 0: no frame started at buf[0] yet
	inData bool // scan is inside entropy-coded data
}

// castSOI starts every JPEG.
var castSOI = []byte{0xFF, 0xD8}

// Feed adds stream bytes and returns the frames they completed. Each frame is
// a fresh slice the caller may keep.
func (s *castJPEGSplitter) Feed(p []byte) [][]byte {
	s.buf = append(s.buf, p...)
	var frames [][]byte
	for {
		if s.scan == 0 {
			start := bytes.Index(s.buf, castSOI)
			if start < 0 {
				// Keep a trailing 0xFF: it may be the first half of the next SOI.
				if n := len(s.buf); n > 0 && s.buf[n-1] == 0xFF {
					s.buf = append(s.buf[:0], 0xFF)
				} else {
					s.buf = s.buf[:0]
				}
				return frames
			}
			if start > 0 {
				s.buf = append(s.buf[:0], s.buf[start:]...)
			}
			s.scan, s.inData = 2, false
		}
		end, next, inData, complete, bad := castJPEGWalk(s.buf, s.scan, s.inData)
		if bad {
			// Not a JPEG after all: skip this SOI and look for the next one.
			s.buf = append(s.buf[:0], s.buf[2:]...)
			s.scan, s.inData = 0, false
			continue
		}
		if !complete {
			if s.max > 0 && len(s.buf) > s.max {
				s.buf = s.buf[:0]
				s.scan, s.inData = 0, false
			} else {
				s.scan, s.inData = next, inData
			}
			return frames
		}
		frames = append(frames, append([]byte(nil), s.buf[:end]...))
		s.buf = append(s.buf[:0], s.buf[end:]...)
		s.scan, s.inData = 0, false
	}
}

// castJPEGWalk walks the JPEG that starts b (b[0:2] is its SOI) marker by
// marker, from offset i — inside entropy-coded data when inData — and returns
// the frame's length once its EOI has arrived (complete). Short of bytes, it
// returns where to carry on (next, nextInData); a segment that runs past the
// bytes so far is skipped by its length, so next may lie beyond len(b). Walking
// the segments, instead of searching for FF D9, keeps a thumbnail's EOI inside
// an APP segment from cutting a frame in two. bad means b is not a JPEG.
func castJPEGWalk(b []byte, i int, inData bool) (n, next int, nextInData, complete, bad bool) {
	if i < 2 {
		i, inData = 2, false
	}
	for {
		if inData {
			// Entropy-coded data follows SOS: it runs to the next marker that
			// is not a stuffed FF 00, a restart marker or a fill byte.
			for {
				if i+1 >= len(b) {
					return 0, i, true, false, false
				}
				if b[i] != 0xFF {
					i++
					continue
				}
				nb := b[i+1]
				if nb == 0x00 || (nb >= 0xD0 && nb <= 0xD7) {
					i += 2
					continue
				}
				if nb == 0xFF {
					i++
					continue
				}
				break
			}
			inData = false
		}
		if i >= len(b) {
			return 0, i, false, false, false
		}
		if b[i] != 0xFF {
			return 0, 0, false, false, true
		}
		j := i
		for j < len(b) && b[j] == 0xFF {
			j++
		}
		if j >= len(b) {
			return 0, i, false, false, false
		}
		m := b[j]
		j++
		switch {
		case m == 0xD9: // EOI
			return j, j, false, true, false
		case m == 0xD8: // a new image before this one ended
			return 0, 0, false, false, true
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7): // markers with no segment
			i = j
			continue
		}
		if j+1 >= len(b) {
			return 0, i, false, false, false
		}
		segLen := int(b[j])<<8 | int(b[j+1])
		if segLen < 2 {
			return 0, 0, false, false, true
		}
		i = j + segLen
		inData = m == 0xDA
	}
}

// castJPEGSize reads a JPEG's size from its SOF marker.
func castJPEGSize(b []byte) (int, int, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0, 0, false
	}
	i := 2
	for i+3 < len(b) {
		if b[i] != 0xFF {
			return 0, 0, false
		}
		m := b[i+1]
		if m == 0xFF {
			i++
			continue
		}
		if m == 0x01 || (m >= 0xD0 && m <= 0xD8) {
			i += 2
			continue
		}
		if m == 0xD9 || m == 0xDA {
			return 0, 0, false
		}
		segLen := int(b[i+2])<<8 | int(b[i+3])
		if segLen < 2 {
			return 0, 0, false
		}
		if m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC {
			if i+9 > len(b) {
				return 0, 0, false
			}
			h := int(b[i+5])<<8 | int(b[i+6])
			w := int(b[i+7])<<8 | int(b[i+8])
			return w, h, true
		}
		i += 2 + segLen
	}
	return 0, 0, false
}

// castTail keeps the end of ffmpeg's error output for the log line.
type castTail struct{ b []byte }

func (t *castTail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 400 {
		t.b = append(t.b[:0], t.b[len(t.b)-400:]...)
	}
	return len(p), nil
}

func (t *castTail) String() string { return strings.TrimSpace(string(t.b)) }

// ── The running source ────────────────────────────────────────────────────

// castFBSource is one running framebuffer reader.
type castFBSource struct {
	preset        castPreset // the preset it was started for (the panel's)
	rotateSetting int        // the castRotate it was started for (-1 = automatic)
	viaFFmpeg     bool       // ffmpeg reads the panel, not JukaHub's own reader
	cancel        context.CancelFunc
	done          chan struct{}
	mu            sync.Mutex
	err           error
}

const (
	// castNativeMaxFPS / castNativeMaxWidth cap JukaHub's own framebuffer
	// reader whatever the preset asks: each frame is a few megabytes read from
	// the panel's memory plus Go's scaler and JPEG encoder — tens of
	// milliseconds on the A53 — next to a game that needs the CPU.
	castNativeMaxFPS   = 8
	castNativeMaxWidth = 640
	// castFFmpegWaitDelay is how long a stopped ffmpeg gets before its pipes
	// are closed under it (cmd.WaitDelay and the read loop's own guard).
	castFFmpegWaitDelay = 2 * time.Second
)

// castNativePreset is the preset JukaHub's own reader runs at: p within
// castNativeMaxFPS and castNativeMaxWidth.
func castNativePreset(p castPreset) castPreset {
	if p.FPS <= 0 || p.FPS > castNativeMaxFPS {
		p.FPS = castNativeMaxFPS
	}
	if p.Width <= 0 || p.Width > castNativeMaxWidth {
		p.Width = castNativeMaxWidth
	}
	return p
}

// castNativeWait is the pause after a native frame that took elapsed, at an
// interval of interval: the rest of the interval, but never less than the
// frame took, so a reader that cannot keep up uses at most half a core
// instead of all of one.
func castNativeWait(interval, elapsed time.Duration) time.Duration {
	wait := interval - elapsed
	if wait < elapsed {
		wait = elapsed
	}
	if wait < 5*time.Millisecond {
		wait = 5 * time.Millisecond
	}
	return wait
}

// Stop ends the reader and waits for it (and an ffmpeg child) to be gone.
func (s *castFBSource) Stop() {
	s.cancel()
	<-s.done
}

// Finished reports whether the reader ended on its own.
func (s *castFBSource) Finished() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// Err is why the reader ended, nil when it was stopped.
func (s *castFBSource) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *castFBSource) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// castStartFBSource opens the framebuffer and starts reading it at preset p:
// through ffmpeg when ffmpeg names one, else with JukaHub's own reader.
func castStartFBSource(p castPreset, rotateSetting int, ffmpeg string) (*castFBSource, error) {
	if !castGamesSupported() {
		return nil, errors.New("sharing a game needs the handheld build")
	}
	f, err := os.Open(castFBDevice)
	if err != nil {
		return nil, err
	}
	cw, ch := int(castLive.canvasW.Load()), int(castLive.canvasH.Load())
	geom, err := castProbeFB(f, cw, ch)
	if err != nil {
		f.Close()
		return nil, err
	}
	deg := rotateSetting
	if deg < 0 {
		deg = castAutoRotate(geom.W, geom.H, cw, ch)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &castFBSource{preset: p, rotateSetting: rotateSetting, viaFFmpeg: ffmpeg != "",
		cancel: cancel, done: make(chan struct{})}
	castBlackReset()
	via := "JukaHub's reader"
	if ffmpeg != "" {
		via = "ffmpeg"
		f.Close()
		go s.runFFmpeg(ctx, ffmpeg, geom, deg)
	} else {
		go s.runNative(ctx, f, geom, deg, castNativePreset(p))
	}
	logInfo("cast", "sharing the game from %s: %dx%d at %d bits, turned %d°, %s, through %s",
		castFBDevice, geom.W, geom.H, geom.BPP, deg, p.Name, via)
	return s, nil
}

// runFFmpeg streams the framebuffer through the bundled ffmpeg.
func (s *castFBSource) runFFmpeg(ctx context.Context, ffmpeg string, geom castFBGeom, deg int) {
	defer close(s.done)
	cmd := exec.CommandContext(ctx, ffmpeg, castFFmpegArgs(castFBDevice, s.preset, deg, geom.W, geom.H)...)
	// Its own process group, stopped as a whole: if the resolved ffmpeg is a
	// wrapper, or forks, a survivor would keep the pipe and /dev/fb0 open, the
	// read below would never end, and stopping the share would hang on it.
	processGroupConfigure(cmd)
	cmd.Cancel = func() error { return screenChildKill(cmd) }
	cmd.WaitDelay = castFFmpegWaitDelay
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.fail(err)
		return
	}
	tail := &castTail{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		s.fail(err)
		return
	}
	// cmd.WaitDelay only takes effect inside Wait, which runs after the read
	// loop; this closes the pipe under a read that a stopped share would
	// otherwise leave blocked.
	readDone := make(chan struct{})
	defer close(readDone)
	go func() {
		select {
		case <-readDone:
			return
		case <-ctx.Done():
		}
		t := time.NewTimer(castFFmpegWaitDelay)
		defer t.Stop()
		select {
		case <-readDone:
		case <-t.C:
			_ = stdout.Close()
		}
	}()
	split := castJPEGSplitter{max: castMaxJPEG}
	buf := make([]byte, 64<<10)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			for _, frame := range split.Feed(buf[:n]) {
				w, h, _ := castJPEGSize(frame)
				castBlackCheckJPEG(frame)
				publishCompanionJPEG(frame, w, h)
			}
		}
		if rerr != nil {
			break
		}
	}
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return
	}
	if werr == nil {
		werr = errors.New("ffmpeg ended")
	}
	if detail := tail.String(); detail != "" {
		werr = fmt.Errorf("%w (%s)", werr, detail)
	}
	s.fail(werr)
}

// runNative reads the framebuffer itself, at preset p (castNativePreset's
// caps). A frame identical to the last one is not encoded again.
func (s *castFBSource) runNative(ctx context.Context, f *os.File, geom castFBGeom, deg int, p castPreset) {
	defer close(s.done)
	defer f.Close()
	enc := castEncoder{dedupe: true}
	bpp := geom.BPP / 8
	raw := make([]byte, geom.Stride*(geom.H-1)+geom.W*bpp)
	var conv []byte
	if geom.BPP == 16 {
		conv = make([]byte, geom.W*geom.H*4)
	}
	fps := p.FPS
	if fps <= 0 {
		fps = castNativeMaxFPS
	}
	interval := time.Second / time.Duration(fps)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		start := time.Now()
		x, y := castFBPan(f, geom)
		n, err := f.ReadAt(raw, int64(y)*int64(geom.Stride)+int64(x*bpp))
		if n < len(raw) {
			if err != nil && !errors.Is(err, io.EOF) {
				s.fail(err)
				return
			}
			if n == 0 {
				s.fail(errors.New("the framebuffer read returned nothing"))
				return
			}
			for i := n; i < len(raw); i++ {
				raw[i] = 0
			}
		}
		pix, stride, lay := raw, geom.Stride, geom.Layout
		if geom.BPP == 16 {
			castRGB565ToRGBA(conv, raw, geom.W, geom.H, geom.Stride)
			pix, stride, lay = conv, geom.W*4, castLayoutRGBA
		}
		if now := time.Now(); castBlackDue(now) {
			castBlackNote(castMeanLuma(pix, stride, geom.W, geom.H, lay), now)
		}
		if frame, w, h, err := enc.jpeg(pix, stride, geom.W, geom.H, lay, p, deg); err == nil {
			publishCompanionJPEG(frame, w, h)
		}
		timer.Reset(castNativeWait(interval, time.Since(start)))
	}
}
