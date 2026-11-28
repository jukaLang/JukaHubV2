package main

// musicviz.go — the real visualizers: what the speaker plays, analysed and
// drawn in ten styles on a small four-shade canvas with Bayer ordered
// dithering, the look of an old monochrome LCD.
//
// The tap. QueueVideoAudio hands every buffer it queues to musicScopeWrite,
// under the device lock, with the device's running byte count. The device
// plays the queue in order, so the frame being heard is the one the clock
// says was consumed (videoAudioClock): the analysis reads the 1024 frames
// that end there, not the ones being decoded a second ahead. Video audio
// passes through the same path; nothing reads it unless a visualizer draws.
//
// The analysis runs once a frame on the render thread: a Hann-windowed
// 1024-point FFT folded into 48 log-spaced bands (35 Hz to 16 kHz), the VU and
// peak level of each channel, the waveform, and a beat pulse from the bass.
//
// The canvas is 320 pixels wide and as tall as the target's shape, one
// byte of brightness per pixel. Each style draws brightness; the dither turns
// it into the four shades of the palette (or a smooth ramp with dithering
// off) and the picture is scaled up without filtering, so the pixels stay
// square. Styles with trails keep a feedback copy of the last frame.

import (
	"math"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// The tap
// ──────────────────────────────────────────────────────────────────────

const (
	musicScopeFrames = 1 << 17 // ~2.7 s at 48 kHz: more than the queue holds
	musicScopeMask   = musicScopeFrames - 1
	vizFFTSize       = 1024
	vizBands         = 48
)

// musicScope is the ring of recent stereo frames, indexed by the device's
// frame count. Its own lock: the writer holds the device lock too, the reader
// (the render thread) must not.
var musicScope struct {
	mu    sync.Mutex
	l, r  [musicScopeFrames]int16
	end   int64 // the frame index after the newest written
	rate  int32
	wrote time.Time
}

// musicScopeWrite records one queued buffer of stereo S16. base is the
// device's count of bytes taken before this buffer. Called with
// videoAudioMutex held, so it must stay a copy.
func musicScopeWrite(pcm []byte, base int64, rate int32) {
	frames := len(pcm) / 4
	if frames == 0 {
		return
	}
	start := base / 4
	musicScope.mu.Lock()
	for i := 0; i < frames; i++ {
		o := i * 4
		idx := (start + int64(i)) & musicScopeMask
		musicScope.l[idx] = int16(uint16(pcm[o]) | uint16(pcm[o+1])<<8)
		musicScope.r[idx] = int16(uint16(pcm[o+2]) | uint16(pcm[o+3])<<8)
	}
	musicScope.end = start + int64(frames)
	musicScope.rate = rate
	musicScope.wrote = time.Now()
	musicScope.mu.Unlock()
}

// musicScopeRead copies the n frames heard last into l and r (as -1..1) and
// reports the rate; ok is false when nothing has played for a moment.
func musicScopeRead(l, r []float64) (rate int32, ok bool) {
	n := len(l)
	musicScope.mu.Lock()
	idle := musicScope.end == 0 || time.Since(musicScope.wrote) > 2*time.Second
	musicScope.mu.Unlock()
	if idle {
		// Nothing queued lately: the device's clock is not even asked.
		return 0, false
	}
	consumed, _, _, _, chunk, clockOK := videoAudioClock()
	musicScope.mu.Lock()
	defer musicScope.mu.Unlock()
	now := musicScope.end
	if clockOK {
		now = consumed / 4
		// The device's own buffer plays after the queue let go of it.
		if musicScope.rate > 0 {
			now -= int64(chunk.Seconds()*float64(musicScope.rate)) / 2
		}
		if now > musicScope.end {
			now = musicScope.end
		}
		if now < musicScope.end-musicScopeFrames+int64(n) {
			now = musicScope.end
		}
	}
	start := now - int64(n)
	for i := 0; i < n; i++ {
		idx := (start + int64(i)) & musicScopeMask
		l[i] = float64(musicScope.l[idx]) / 32768
		r[i] = float64(musicScope.r[idx]) / 32768
	}
	return musicScope.rate, true
}

// ──────────────────────────────────────────────────────────────────────
// Analysis
// ──────────────────────────────────────────────────────────────────────

type vizFFT struct {
	cos, sin [vizFFTSize / 2]float64
	rev      [vizFFTSize]int
	win      [vizFFTSize]float64
	re, im   [vizFFTSize]float64
}

func newVizFFT() *vizFFT {
	f := &vizFFT{}
	const bits = 10
	for i := 0; i < vizFFTSize; i++ {
		r := 0
		for b := 0; b < bits; b++ {
			if i&(1<<b) != 0 {
				r |= 1 << (bits - 1 - b)
			}
		}
		f.rev[i] = r
		f.win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(vizFFTSize-1))
	}
	for i := 0; i < vizFFTSize/2; i++ {
		a := -2 * math.Pi * float64(i) / vizFFTSize
		f.cos[i], f.sin[i] = math.Cos(a), math.Sin(a)
	}
	return f
}

// magnitudes fills out (N/2 values) with |X[k]| of the windowed input.
func (f *vizFFT) magnitudes(in []float64, out []float64) {
	const n = vizFFTSize
	for i := 0; i < n; i++ {
		j := f.rev[i]
		f.re[j] = in[i] * f.win[i]
		f.im[j] = 0
	}
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		step := n / size
		for start := 0; start < n; start += size {
			for k := 0; k < half; k++ {
				wr, wi := f.cos[k*step], f.sin[k*step]
				a, b := start+k, start+k+half
				tr := f.re[b]*wr - f.im[b]*wi
				ti := f.re[b]*wi + f.im[b]*wr
				f.re[b], f.im[b] = f.re[a]-tr, f.im[a]-ti
				f.re[a] += tr
				f.im[a] += ti
			}
		}
	}
	for k := 0; k < n/2 && k < len(out); k++ {
		out[k] = math.Hypot(f.re[k], f.im[k])
	}
}

// vizState is the analysis the styles draw from. Render thread only.
type vizState struct {
	fft      *vizFFT
	l, r     [vizFFTSize]float64
	mono     [vizFFTSize]float64
	mags     [vizFFTSize / 2]float64
	bands    [vizBands]float64 // 0..1, smoothed
	peaks    [vizBands]float64 // falling peak marks
	vu       [2]float64        // 0..1 needle position, VU ballistics
	peak     [2]float64        // 0..1 LED meter, falling
	energy   float64           // overall loudness 0..1
	bass     float64           // low-band energy 0..1
	treble   float64
	bassAvg  float64
	pulse    float64 // beat pulse, 1 on a beat, decays
	lastBeat time.Time
	live     bool // something is playing
	at       time.Time
	t        float64 // seconds of animation
	dt       float64
}

var viz vizState

// vizAnalyse updates the analysis once a frame.
func vizAnalyse() {
	now := time.Now()
	if !viz.at.IsZero() && now.Sub(viz.at) < 4*time.Millisecond {
		return
	}
	dt := 1.0 / 30
	if !viz.at.IsZero() {
		dt = now.Sub(viz.at).Seconds()
	}
	if dt > 0.25 {
		dt = 0.25
	}
	viz.at, viz.dt = now, dt
	viz.t += dt
	if viz.fft == nil {
		viz.fft = newVizFFT()
	}
	music.mu.Lock()
	active := music.playing && !music.paused
	music.mu.Unlock()
	rate, ok := musicScopeRead(viz.l[:], viz.r[:])
	viz.live = ok && active
	fall := math.Exp(-dt / 0.28)
	if !viz.live {
		for i := range viz.bands {
			viz.bands[i] *= fall
			viz.peaks[i] *= fall
		}
		for c := 0; c < 2; c++ {
			viz.vu[c] *= fall
			viz.peak[c] *= fall
		}
		for i := range viz.l {
			viz.l[i] *= 0.5
			viz.r[i] *= 0.5
		}
		viz.energy *= fall
		viz.bass *= fall
		viz.treble *= fall
		viz.pulse *= math.Exp(-dt / 0.15)
		return
	}
	if rate <= 0 {
		rate = 44100
	}
	for i := range viz.mono {
		viz.mono[i] = (viz.l[i] + viz.r[i]) * 0.5
	}
	viz.fft.magnitudes(viz.mono[:], viz.mags[:])
	binHz := float64(rate) / vizFFTSize
	const fmin, fmaxCap = 35.0, 16000.0
	fmax := math.Min(fmaxCap, float64(rate)/2-binHz)
	ref := vizFFTSize / 4.0
	var bassSum, trebleSum, all float64
	for b := 0; b < vizBands; b++ {
		lo := fmin * math.Pow(fmax/fmin, float64(b)/vizBands)
		hi := fmin * math.Pow(fmax/fmin, float64(b+1)/vizBands)
		center := math.Sqrt(lo * hi)
		var mag float64
		if hi-lo < binHz {
			// Narrower than a bin: read the spectrum at the band's centre.
			x := center / binHz
			i := int(x)
			if i+1 < len(viz.mags) {
				f := x - float64(i)
				mag = viz.mags[i]*(1-f) + viz.mags[i+1]*f
			}
		} else {
			for k := int(lo / binHz); k <= int(hi/binHz) && k < len(viz.mags); k++ {
				if viz.mags[k] > mag {
					mag = viz.mags[k]
				}
			}
		}
		db := 20 * math.Log10(mag/ref+1e-9)
		// Music falls off towards the treble; tilt it up so the right half of
		// the spectrum is not always dark.
		db += 3 * math.Log2(center/1000)
		v := (db + 62) / 58
		if v < 0 {
			v = 0
		} else if v > 1 {
			v = 1
		}
		if v > viz.bands[b] {
			viz.bands[b] += (v - viz.bands[b]) * 0.65
		} else {
			viz.bands[b] = math.Max(v, viz.bands[b]*fall)
		}
		if viz.bands[b] > viz.peaks[b] {
			viz.peaks[b] = viz.bands[b]
		} else {
			viz.peaks[b] -= dt * 0.35
			if viz.peaks[b] < 0 {
				viz.peaks[b] = 0
			}
		}
		all += viz.bands[b]
		if center < 160 {
			bassSum += viz.bands[b]
		}
		if center > 4000 {
			trebleSum += viz.bands[b]
		}
	}
	viz.energy = all / vizBands
	nb, nt := 0, 0
	for b := 0; b < vizBands; b++ {
		c := fmin * math.Pow(fmax/fmin, (float64(b)+0.5)/vizBands)
		if c < 160 {
			nb++
		}
		if c > 4000 {
			nt++
		}
	}
	if nb > 0 {
		viz.bass = bassSum / float64(nb)
	}
	if nt > 0 {
		viz.treble = trebleSum / float64(nt)
	}
	// Beat: the bass jumping well above its recent average.
	viz.bassAvg += (viz.bass - viz.bassAvg) * (1 - math.Exp(-dt/0.9))
	viz.pulse *= math.Exp(-dt / 0.15)
	if viz.bass > viz.bassAvg*1.18+0.04 && now.Sub(viz.lastBeat) > 260*time.Millisecond {
		viz.pulse, viz.lastBeat = 1, now
	}
	// Levels: RMS for the VU needles (0 VU at -18 dBFS), peak for the LEDs.
	for c := 0; c < 2; c++ {
		src := viz.l[:]
		if c == 1 {
			src = viz.r[:]
		}
		var sum, pk float64
		for _, v := range src {
			sum += v * v
			if a := math.Abs(v); a > pk {
				pk = a
			}
		}
		rms := math.Sqrt(sum / float64(len(src)))
		vuDB := 20*math.Log10(rms+1e-9) + 18 // dB relative to 0 VU
		target := (vuDB + 20) / 23           // -20 VU .. +3 VU
		target = math.Max(0, math.Min(1, target))
		viz.vu[c] += (target - viz.vu[c]) * (1 - math.Exp(-dt/0.3))
		pkDB := 20 * math.Log10(pk+1e-9)
		pkv := math.Max(0, math.Min(1, (pkDB+42)/42))
		if pkv > viz.peak[c] {
			viz.peak[c] = pkv
		} else {
			viz.peak[c] = math.Max(pkv, viz.peak[c]-dt*0.9)
		}
	}
}

// musicVizBars gives n bar heights (0..1) from the live spectrum for the small
// artwork-tile bars; ok is false when nothing plays.
func musicVizBars(out []float64) bool {
	vizAnalyse()
	if !viz.live {
		return false
	}
	n := len(out)
	for i := 0; i < n; i++ {
		lo := i * vizBands / n
		hi := (i + 1) * vizBands / n
		if hi <= lo {
			hi = lo + 1
		}
		var m float64
		for b := lo; b < hi && b < vizBands; b++ {
			m = math.Max(m, viz.bands[b])
		}
		out[i] = m
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Canvas
// ──────────────────────────────────────────────────────────────────────

type vizCanvas struct {
	w, h int
	pix  []uint8 // brightness drawn this frame
	fb   []uint8 // the last frame, for the styles with trails
	rgba []byte
	tex  *sdl.Texture
	ren  *sdl.Renderer
	// The palette and dither the texture was made with.
	style int
}

var vizCv vizCanvas

// vizEnsure sizes the canvas for a target of tw×th pixels.
func vizEnsure(tw, th int32) {
	w := 320
	if int(tw) < 2*w {
		w = int(tw) / 2
	}
	if w < 64 {
		w = 64
	}
	h := int(float64(w) * float64(th) / float64(tw))
	if h < 32 {
		h = 32
	}
	if w == vizCv.w && h == vizCv.h {
		return
	}
	if vizCv.tex != nil {
		_ = vizCv.tex.Destroy()
		vizCv.tex = nil
	}
	vizCv.w, vizCv.h = w, h
	vizCv.pix = make([]uint8, w*h)
	vizCv.fb = make([]uint8, w*h)
	vizCv.rgba = make([]byte, w*h*4)
}

func (c *vizCanvas) clear() {
	for i := range c.pix {
		c.pix[i] = 0
	}
}

// plot brightens one pixel (the brighter of the two wins).
func (c *vizCanvas) plot(x, y int, v uint8) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	if i := y*c.w + x; c.pix[i] < v {
		c.pix[i] = v
	}
}

// fillRect paints a rectangle over whatever is there (shapes are layered:
// a darker one in front hides a lighter one behind).
func (c *vizCanvas) fillRect(x0, y0, x1, y1 int, v uint8) {
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > c.w {
		x1 = c.w
	}
	if y1 > c.h {
		y1 = c.h
	}
	for y := y0; y < y1; y++ {
		row := y * c.w
		for x := x0; x < x1; x++ {
			c.pix[row+x] = v
		}
	}
}

// line draws a light line: it only ever brightens (trails and traces).
func (c *vizCanvas) line(x0, y0, x1, y1 int, v uint8) { c.stroke(x0, y0, x1, y1, v, false) }

// lineSet draws a line of exactly v, dark ones too (spokes, needles).
func (c *vizCanvas) lineSet(x0, y0, x1, y1 int, v uint8) { c.stroke(x0, y0, x1, y1, v, true) }

func (c *vizCanvas) stroke(x0, y0, x1, y1 int, v uint8, set bool) {
	dx := x1 - x0
	if dx < 0 {
		dx = -dx
	}
	dy := y1 - y0
	if dy > 0 {
		dy = -dy
	}
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	e := dx + dy
	for n := 0; n < 4096; n++ {
		if !set {
			c.plot(x0, y0, v)
		} else if x0 >= 0 && y0 >= 0 && x0 < c.w && y0 < c.h {
			c.pix[y0*c.w+x0] = v
		}
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * e
		if e2 >= dy {
			e += dy
			x0 += sx
		}
		if e2 <= dx {
			e += dx
			y0 += sy
		}
	}
}

// disc paints a filled circle over whatever is there.
func (c *vizCanvas) disc(cx, cy, r float64, v uint8) {
	for y := int(cy - r); y <= int(cy+r); y++ {
		dy := float64(y) - cy
		span := r*r - dy*dy
		if span < 0 {
			continue
		}
		dx := math.Sqrt(span)
		c.fillRect(int(cx-dx), y, int(cx+dx)+1, y+1, v)
	}
}

// fade carries the last frame into this one at strength keep (0..1),
// optionally zoomed and turned about the centre: the trails of the vortex,
// the phosphor and the star warp.
func (c *vizCanvas) fade(keep, zoom, turn float64) {
	w, h := c.w, c.h
	if zoom == 1 && turn == 0 {
		for i, v := range c.fb {
			if nv := uint8(float64(v) * keep); nv > c.pix[i] {
				c.pix[i] = nv
			}
		}
		return
	}
	cx, cy := float64(w)/2, float64(h)/2
	cs, sn := math.Cos(turn)/zoom, math.Sin(turn)/zoom
	for y := 0; y < h; y++ {
		dy := float64(y) - cy
		for x := 0; x < w; x++ {
			dx := float64(x) - cx
			sx := int(cx + dx*cs - dy*sn)
			sy := int(cy + dx*sn + dy*cs)
			if sx < 0 || sy < 0 || sx >= w || sy >= h {
				continue
			}
			if nv := uint8(float64(c.fb[sy*w+sx]) * keep); nv > c.pix[y*w+x] {
				c.pix[y*w+x] = nv
			}
		}
	}
}

// vizBayer is the 4×4 ordered-dither matrix.
var vizBayer = [4][4]uint8{{0, 8, 2, 10}, {12, 4, 14, 6}, {3, 11, 1, 9}, {15, 7, 13, 5}}

// vizUpload turns brightness into the palette and uploads it.
func (c *vizCanvas) upload(renderer *sdl.Renderer, pal [4]sdl.Color, dither bool) *sdl.Texture {
	if c.tex != nil && c.ren != renderer {
		c.tex = nil
	}
	if c.tex == nil {
		prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
		sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "0")
		tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STREAMING, int32(c.w), int32(c.h))
		sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
		if err != nil || tex == nil {
			return nil
		}
		c.tex, c.ren = tex, renderer
	}
	var ramp [256][3]byte
	if !dither {
		for v := 0; v < 256; v++ {
			x := float64(v) / 255 * 3
			i := int(x)
			if i > 2 {
				i = 2
			}
			f := x - float64(i)
			a, b := pal[i], pal[i+1]
			ramp[v] = [3]byte{byte(float64(a.R) + (float64(b.R)-float64(a.R))*f),
				byte(float64(a.G) + (float64(b.G)-float64(a.G))*f),
				byte(float64(a.B) + (float64(b.B)-float64(a.B))*f)}
		}
	}
	for y := 0; y < c.h; y++ {
		row := y * c.w
		for x := 0; x < c.w; x++ {
			v := c.pix[row+x]
			var col [3]byte
			if dither {
				// Four shades: the level plus the matrix's threshold, floored.
				q := (int(v)*3*16 + int(vizBayer[y&3][x&3])*255 + 127) / (255 * 16)
				if q > 3 {
					q = 3
				}
				p := pal[q]
				col = [3]byte{p.R, p.G, p.B}
			} else {
				col = ramp[v]
			}
			o := (row + x) * 4
			c.rgba[o], c.rgba[o+1], c.rgba[o+2], c.rgba[o+3] = col[0], col[1], col[2], 255
		}
	}
	if err := c.tex.Update(nil, unsafe.Pointer(&c.rgba[0]), c.w*4); err != nil {
		return nil
	}
	copy(c.fb, c.pix)
	return c.tex
}

// ──────────────────────────────────────────────────────────────────────
// The styles
// ──────────────────────────────────────────────────────────────────────

// musicVizNames are the ten styles in the order Left/Right steps through.
var musicVizNames = []string{
	"Cassette Deck", "Stereo VU Meter", "Sonic Vortex", "Phosphor Scope", "Spectrum Matrix",
	"Cyber Horizon", "Liquid Metal", "Chladni Plate", "Star Warp", "LCD Plasma",
}

const (
	musicVizStyleKey  = "musicVisualizer"
	musicVizDitherKey = "musicVizDither"
)

// musicVizStyle is the chosen style (Cassette Deck when unset).
func musicVizStyle(config *Config) int {
	if config == nil {
		return 0
	}
	raw := strings.TrimSpace(customStringValue(config.Variables.Custom, musicVizStyleKey))
	for i, name := range musicVizNames {
		if strings.EqualFold(raw, name) {
			return i
		}
	}
	return 0
}

func musicVizSetStyle(config *Config, style int) {
	if config == nil {
		return
	}
	n := len(musicVizNames)
	style = ((style % n) + n) % n
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicVizStyleKey] = musicVizNames[style]
}

// musicVizDitherOn reports whether the canvas is dithered to four shades (the
// default) or drawn as a smooth ramp.
func musicVizDitherOn(config *Config) bool {
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, musicVizDitherKey, true)
}

func musicVizSetDither(config *Config, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicVizDitherKey] = boolString(on)
}

// musicVizStep moves to the next or previous style and says its name.
func musicVizStep(config *Config, dir int) {
	musicVizSetStyle(config, musicVizStyle(config)+dir)
	showToast(musicVizNames[musicVizStyle(config)], ToastInfo())
	PlayNavSound()
}

// musicVizKey handles the full-screen visualizer's own keys and reports
// whether key was one of them: Left/Right step the visualizer, Y or D turn
// the dithering on and off. Outside that screen it does nothing.
func musicVizKey(key sdl.Keycode, config *Config) bool {
	music.mu.Lock()
	full := music.visualizerFull
	music.mu.Unlock()
	if !full || config == nil {
		return false
	}
	switch key {
	case sdl.K_LEFT:
		musicVizStep(config, -1)
	case sdl.K_RIGHT:
		musicVizStep(config, 1)
	case sdl.K_y, sdl.K_d:
		on := !musicVizDitherOn(config)
		musicVizSetDither(config, on)
		if on {
			showToast("Dithering on: four shades", ToastInfo())
		} else {
			showToast("Dithering off: smooth shades", ToastInfo())
		}
		PlayNavSound()
	default:
		return false
	}
	return true
}

// musicVizDraw analyses, draws style into the canvas and copies it to dst.
func musicVizDraw(renderer *sdl.Renderer, dst sdl.Rect, style int, pal [4]sdl.Color, dither bool) {
	if renderer == nil || dst.W < 16 || dst.H < 16 {
		return
	}
	vizAnalyse()
	vizEnsure(dst.W, dst.H)
	c := &vizCv
	c.clear()
	if style != c.style {
		for i := range c.fb {
			c.fb[i] = 0
		}
		c.style = style
	}
	switch style {
	case 1:
		vizDrawVU(c)
	case 2:
		vizDrawVortex(c)
	case 3:
		vizDrawScope(c)
	case 4:
		vizDrawMatrix(c)
	case 5:
		vizDrawHorizon(c)
	case 6:
		vizDrawMetal(c)
	case 7:
		vizDrawChladni(c)
	case 8:
		vizDrawWarp(c)
	case 9:
		vizDrawPlasma(c)
	default:
		vizDrawCassette(c)
	}
	if tex := c.upload(renderer, pal, dither); tex != nil {
		_ = renderer.Copy(tex, nil, &dst)
	}
}

// vizLevel is a 0..1 amount as brightness.
func vizLevel(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v * 255)
}

// 1. Cassette Deck: the cassette with its two reels turning, the tape between
// them, and a 28-segment LED meter per channel.
var vizReelAngle float64

func vizDrawCassette(c *vizCanvas) {
	w, h := float64(c.w), float64(c.h)
	if viz.live {
		vizReelAngle += viz.dt * (2.2 + 1.5*viz.energy)
	}
	// The shell.
	sx0, sy0 := int(w*0.12), int(h*0.08)
	sx1, sy1 := int(w*0.88), int(h*0.66)
	c.fillRect(sx0, sy0, sx1, sy1, 70)
	c.fillRect(sx0+3, sy0+3, sx1-3, sy1-3, 40)
	// The label strip and the window.
	c.fillRect(sx0+8, sy0+6, sx1-8, sy0+int(h*0.12), 150)
	wy0, wy1 := int(h*0.25), int(h*0.5)
	c.fillRect(int(w*0.3), wy0, int(w*0.7), wy1, 20)
	r := h * 0.15
	for side := 0; side < 2; side++ {
		cx := w * (0.33 + 0.34*float64(side))
		cy := h * 0.37
		// Tape wound on each reel: the left empties as the song goes on.
		music.mu.Lock()
		frac := 0.0
		if music.duration > 0 {
			frac = music.position / music.duration
		}
		music.mu.Unlock()
		wound := 0.35 + 0.6*(1-frac)
		if side == 1 {
			wound = 0.35 + 0.6*frac
		}
		c.disc(cx, cy, r*wound+r*0.25, 95)
		c.disc(cx, cy, r*0.32, 210)
		for s := 0; s < 6; s++ {
			a := vizReelAngle + float64(s)*math.Pi/3
			c.lineSet(int(cx), int(cy), int(cx+r*0.3*math.Cos(a)), int(cy+r*0.3*math.Sin(a)), 30)
		}
	}
	// The tape across the bottom of the window.
	c.line(int(w*0.24), int(h*0.56), int(w*0.76), int(h*0.56), 180)
	// LED meters: 28 segments each.
	const segs = 28
	mx0, mx1 := int(w*0.1), int(w*0.9)
	segW := float64(mx1-mx0) / segs
	for ch := 0; ch < 2; ch++ {
		y0 := int(h*0.74) + ch*int(h*0.11)
		lit := int(math.Round(viz.peak[ch] * segs))
		for s := 0; s < segs; s++ {
			v := uint8(35)
			if s < lit {
				v = 200
				if s >= segs-4 {
					v = 255
				}
			}
			x := mx0 + int(float64(s)*segW)
			c.fillRect(x, y0, x+int(segW)-1, y0+int(h*0.07), v)
		}
	}
}

// 2. Stereo VU Meter: two analogue meters side by side, centred, with their
// scales, the +3 red zone and the needles; a peak lamp lights over 0 VU.
func vizDrawVU(c *vizCanvas) {
	w, h := float64(c.w), float64(c.h)
	for ch := 0; ch < 2; ch++ {
		x0 := w*0.06 + float64(ch)*w*0.47
		fw, fh := w*0.41, h*0.7
		y0 := (h - fh) / 2
		c.fillRect(int(x0), int(y0), int(x0+fw), int(y0+fh), 120)
		c.fillRect(int(x0+2), int(y0+2), int(x0+fw-2), int(y0+fh-2), 60)
		px, py := x0+fw/2, y0+fh*0.95
		rad := fh * 0.75
		// The scale: -20..+3 VU over 90 degrees; 0..+3 is the heavy red zone.
		for i := 0; i <= 23; i++ {
			a := math.Pi*1.25 + math.Pi*0.5*float64(i)/23
			inner := rad * 0.88
			if i%5 == 0 || i >= 20 {
				inner = rad * 0.8
			}
			v := uint8(180)
			if i >= 20 {
				v = 255
				c.line(int(px+rad*0.93*math.Cos(a)), int(py+rad*0.93*math.Sin(a)), int(px+rad*math.Cos(a)), int(py+rad*math.Sin(a)), 255)
			}
			c.line(int(px+inner*math.Cos(a)), int(py+inner*math.Sin(a)), int(px+rad*0.93*math.Cos(a)), int(py+rad*0.93*math.Sin(a)), v)
		}
		a := math.Pi*1.25 + math.Pi*0.5*viz.vu[ch]
		c.line(int(px), int(py), int(px+rad*0.98*math.Cos(a)), int(py+rad*0.98*math.Sin(a)), 255)
		c.disc(px, py, 3, 200)
		if viz.vu[ch] > 20.0/23 {
			c.disc(x0+fw*0.88, y0+fh*0.16, 3, 255)
		}
	}
}

// 3. Sonic Vortex: the spectrum as a ring, the last frames spiralling inwards.
func vizDrawVortex(c *vizCanvas) {
	c.fade(0.9, 1.035+0.03*viz.pulse, 0.035+0.04*viz.energy)
	w, h := float64(c.w), float64(c.h)
	cx, cy := w/2, h/2
	base := math.Min(w, h) * (0.18 + 0.08*viz.pulse)
	prevX, prevY := 0, 0
	for i := 0; i <= vizBands*2; i++ {
		b := i % (vizBands * 2)
		band := b
		if band >= vizBands {
			band = vizBands*2 - 1 - band
		}
		a := 2*math.Pi*float64(i)/float64(vizBands*2) + viz.t*0.4
		rr := base + viz.bands[band]*math.Min(w, h)*0.3
		x, y := int(cx+rr*math.Cos(a)), int(cy+rr*math.Sin(a))
		if i > 0 {
			c.line(prevX, prevY, x, y, 255)
		}
		prevX, prevY = x, y
	}
}

// 4. Phosphor Scope: the stereo signal as an X-Y trace (mid up, side across)
// on a slow phosphor.
func vizDrawScope(c *vizCanvas) {
	c.fade(0.78, 1, 0)
	w, h := float64(c.w), float64(c.h)
	cx, cy := w/2, h/2
	scale := math.Min(w, h) * 0.45
	// Faint graticule.
	c.line(0, int(cy), c.w-1, int(cy), 30)
	c.line(int(cx), 0, int(cx), c.h-1, 30)
	px, py := -1, -1
	for i := 0; i < vizFFTSize; i += 2 {
		l, r := viz.l[i], viz.r[i]
		side := (l - r) * 0.7071
		mid := (l + r) * 0.7071
		x, y := int(cx+side*scale*1.6), int(cy-mid*scale*1.6)
		if px >= 0 {
			c.line(px, py, x, y, 230)
		}
		px, py = x, y
	}
}

// 5. Spectrum Matrix: the spectrum as columns of LED blocks with falling
// peak blocks.
func vizDrawMatrix(c *vizCanvas) {
	cols, rows := 32, 16
	cw := float64(c.w) / float64(cols)
	rh := float64(c.h) / float64(rows)
	for col := 0; col < cols; col++ {
		lo := col * vizBands / cols
		hi := (col + 1) * vizBands / cols
		v, pk := 0.0, 0.0
		for b := lo; b < hi; b++ {
			v = math.Max(v, viz.bands[b])
			pk = math.Max(pk, viz.peaks[b])
		}
		lit := int(math.Round(v * float64(rows)))
		peakRow := int(math.Round(pk * float64(rows)))
		for row := 0; row < rows; row++ {
			x0 := int(float64(col) * cw)
			y1 := c.h - int(float64(row)*rh)
			y0 := c.h - int(float64(row+1)*rh)
			b := uint8(28)
			if row < lit {
				b = uint8(120 + 135*row/rows)
			} else if row == peakRow-1 || (row == peakRow && peakRow > lit) {
				b = 255
			}
			c.fillRect(x0+1, y0+1, x0+int(cw)-1, y1-1, b)
		}
	}
}

// 6. Cyber Horizon: a striped sun over a spectrum mountain range and a grid
// floor rushing towards you.
func vizDrawHorizon(c *vizCanvas) {
	w, h := float64(c.w), float64(c.h)
	hz := h * 0.52
	// Sun.
	sunR := h * (0.26 + 0.05*viz.pulse)
	sunY := hz - sunR*0.35
	for y := int(sunY - sunR); y < int(hz); y++ {
		dy := float64(y) - sunY
		span := sunR*sunR - dy*dy
		if span < 0 {
			continue
		}
		// The stripes widen towards the horizon.
		band := (float64(y) - (sunY - sunR)) / (hz - (sunY - sunR))
		if band > 0.45 && int(float64(y)*0.5)%int(2+band*4) == 0 {
			continue
		}
		dx := math.Sqrt(span)
		c.fillRect(int(w/2-dx), y, int(w/2+dx)+1, y+1, uint8(170+60*(1-band)))
	}
	// Mountains: the spectrum mirrored about the middle.
	for x := 0; x < c.w; x++ {
		u := math.Abs(float64(x)/w*2 - 1)
		b := int(u * float64(vizBands-1))
		top := hz - viz.bands[b]*h*0.32*(0.4+u)
		c.fillRect(x, int(top), x+1, int(hz), 90)
		c.plot(x, int(top), 220)
	}
	// Floor grid.
	speed := 0.6 + 1.4*viz.energy
	off := math.Mod(viz.t*speed, 1)
	for i := 0; i < 12; i++ {
		z := (float64(i) + 1 - off)
		y := hz + (h-hz)*(1/z)*0.9
		if y < hz || y >= h {
			continue
		}
		c.line(0, int(y), c.w-1, int(y), 150)
	}
	for i := -12; i <= 12; i++ {
		xb := w/2 + float64(i)*w*0.11
		c.line(int(w/2+float64(i)*w*0.01), int(hz), int(xb+float64(i)*w*0.08), c.h-1, 120)
	}
	c.line(0, int(hz), c.w-1, int(hz), 255)
}

// 7. Liquid Metal: blobs of mercury that swell with their part of the
// spectrum and run together.
func vizDrawMetal(c *vizCanvas) {
	const blobs = 6
	var bx, by, br [blobs]float64
	w, h := float64(c.w), float64(c.h)
	for i := 0; i < blobs; i++ {
		ph := viz.t*(0.35+0.11*float64(i)) + float64(i)*1.7
		bx[i] = w/2 + w*0.32*math.Sin(ph)*math.Cos(ph*0.37+float64(i))
		by[i] = h/2 + h*0.32*math.Cos(ph*0.83+float64(i)*0.5)
		e := viz.bands[(i*vizBands/blobs+2)%vizBands]
		r := math.Min(w, h) * (0.07 + 0.16*e + 0.04*viz.pulse)
		br[i] = r * r
	}
	for y := 0; y < c.h; y++ {
		fy := float64(y)
		row := y * c.w
		for x := 0; x < c.w; x++ {
			fx := float64(x)
			var f float64
			for i := 0; i < blobs; i++ {
				dx, dy := fx-bx[i], fy-by[i]
				f += br[i] / (dx*dx + dy*dy + 1)
			}
			if f < 0.55 {
				continue
			}
			// A bright rim and a darker core read as a curved, shiny surface.
			v := 1 - math.Min(1, (f-0.55)*0.9)
			c.pix[row+x] = uint8(110 + 145*v)
		}
	}
}

// 8. Chladni Plate: sand on a vibrating plate gathers on the nodal lines of
// the mode the music excites; the mode follows the strongest band.
var vizChladni struct {
	n, m       float64
	tn, tm     float64
	cosX, cosY []float64
}

func vizDrawChladni(c *vizCanvas) {
	ch := &vizChladni
	if ch.n == 0 {
		ch.n, ch.m, ch.tn, ch.tm = 2, 3, 2, 3
	}
	if viz.pulse > 0.95 {
		best := 0
		for b := 1; b < vizBands; b++ {
			if viz.bands[b] > viz.bands[best] {
				best = b
			}
		}
		ch.tn = float64(1 + best%6)
		ch.tm = float64(2 + (best/6)%5)
		if ch.tn == ch.tm {
			ch.tm++
		}
	}
	k := 1 - math.Exp(-viz.dt/0.4)
	ch.n += (ch.tn - ch.n) * k
	ch.m += (ch.tm - ch.m) * k
	if len(ch.cosX) != c.w*2 {
		ch.cosX = make([]float64, c.w*2)
	}
	if len(ch.cosY) != c.h*2 {
		ch.cosY = make([]float64, c.h*2)
	}
	for x := 0; x < c.w; x++ {
		u := float64(x) / float64(c.w-1)
		ch.cosX[x*2] = math.Cos(ch.n * math.Pi * u)
		ch.cosX[x*2+1] = math.Cos(ch.m * math.Pi * u)
	}
	for y := 0; y < c.h; y++ {
		v := float64(y) / float64(c.h-1)
		ch.cosY[y*2] = math.Cos(ch.n * math.Pi * v)
		ch.cosY[y*2+1] = math.Cos(ch.m * math.Pi * v)
	}
	gain := 0.04 + 0.12*(1-viz.energy)
	for y := 0; y < c.h; y++ {
		row := y * c.w
		yn, ym := ch.cosY[y*2], ch.cosY[y*2+1]
		for x := 0; x < c.w; x++ {
			s := math.Abs(ch.cosX[x*2]*ym - ch.cosX[x*2+1]*yn)
			if s < gain {
				c.pix[row+x] = uint8(255 * (1 - s/gain))
			}
		}
	}
}

// 9. Star Warp: a star field flying at you, faster with the music.
type vizStar struct{ x, y, z float64 }

var vizStars []vizStar

func vizDrawWarp(c *vizCanvas) {
	c.fade(0.6, 1, 0)
	if len(vizStars) == 0 {
		seed := uint32(12345)
		rnd := func() float64 {
			seed ^= seed << 13
			seed ^= seed >> 17
			seed ^= seed << 5
			return float64(seed%10000) / 10000
		}
		vizStars = make([]vizStar, 220)
		for i := range vizStars {
			vizStars[i] = vizStar{rnd()*2 - 1, rnd()*2 - 1, rnd()*0.95 + 0.05}
		}
	}
	w, h := float64(c.w), float64(c.h)
	speed := viz.dt * (0.15 + 1.1*viz.energy + 0.8*viz.pulse)
	for i := range vizStars {
		s := &vizStars[i]
		ox, oy := s.x/s.z, s.y/s.z
		s.z -= speed
		if s.z <= 0.03 {
			s.z += 1
			s.x = math.Sin(float64(i)*12.9898+viz.t) * 1.0
			s.y = math.Cos(float64(i)*78.233+viz.t*1.3) * 1.0
			continue
		}
		nx, ny := s.x/s.z, s.y/s.z
		x0, y0 := int(w/2+ox*w*0.5), int(h/2+oy*h*0.5)
		x1, y1 := int(w/2+nx*w*0.5), int(h/2+ny*h*0.5)
		if x1 < 0 || y1 < 0 || x1 >= c.w || y1 >= c.h {
			// Out of view: back to the far end of the field.
			s.z = 1
			continue
		}
		c.line(x0, y0, x1, y1, vizLevel(1.2-s.z))
	}
}

// 10. LCD Plasma: the demo-scene plasma, its colours swirling faster with
// the bass and its grain finer with the treble.
var vizSinTab [1024]float64

func vizSin(x float64) float64 {
	if vizSinTab[256] == 0 {
		for i := range vizSinTab {
			vizSinTab[i] = math.Sin(2 * math.Pi * float64(i) / 1024)
		}
	}
	i := int(x*1024/(2*math.Pi)) & 1023
	return vizSinTab[i]
}

func vizDrawPlasma(c *vizCanvas) {
	t := viz.t * (0.7 + 1.5*viz.bass)
	k := 0.05 + 0.05*viz.treble
	w, h := float64(c.w), float64(c.h)
	for y := 0; y < c.h; y++ {
		fy := float64(y)
		row := y * c.w
		sy := vizSin(fy*k*1.3 + t)
		for x := 0; x < c.w; x++ {
			fx := float64(x)
			v := vizSin(fx*k+t*1.1) + sy +
				vizSin((fx+fy)*k*0.7+t*0.6) +
				vizSin(math.Sqrt((fx-w/2)*(fx-w/2)+(fy-h/2)*(fy-h/2))*k*1.5-t*1.7)
			c.pix[row+x] = uint8((v + 4) / 8 * (200 + 55*viz.pulse))
		}
	}
}
