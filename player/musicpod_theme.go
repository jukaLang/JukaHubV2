package main

// musicpod_theme.go — the iPod screen's look: 38 four-shade themes (the LCD
// of an iPod mini in every colour it came in, the Game Boy family, phosphor
// monitors and more), six screen transitions, Bayer-dithered album art in
// the theme's four shades, and the click-wheel tick.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg" // album art
	_ "image/png"  // album art
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// musicPodTheme is four shades, darkest first. A dark theme puts light ink on
// a dark screen; the others are lit LCDs with dark ink.
type musicPodTheme struct {
	name string
	pal  [4]uint32
	dark bool
}

var musicPodThemes = []musicPodTheme{
	{"Classic", [4]uint32{0x1C1C1A, 0x565652, 0x9A9A92, 0xD8D8CE}, false},
	{"Silver", [4]uint32{0x202226, 0x5E636B, 0xA7ADB6, 0xE9ECF0}, false},
	{"Graphite", [4]uint32{0x0E0E10, 0x3A3A40, 0x75757E, 0xC2C2CA}, true},
	{"DMG", [4]uint32{0x0F380F, 0x306230, 0x8BAC0F, 0x9BBC0F}, false},
	{"Pocket", [4]uint32{0x1F1F1F, 0x4D533C, 0x8B956D, 0xC4CFA1}, false},
	{"Backlight", [4]uint32{0x04241F, 0x0D5C4E, 0x2FB39A, 0x9CF5DD}, false},
	{"Amber", [4]uint32{0x1A0E00, 0x5C3300, 0xB36A00, 0xFFB000}, true},
	{"Ice", [4]uint32{0x0B1A2A, 0x2E5A80, 0x6FA8D6, 0xD8F0FF}, false},
	{"Pink", [4]uint32{0x2A0F1C, 0x7A3156, 0xD07AA6, 0xFCE3F0}, false},
	{"Blue", [4]uint32{0x0B1530, 0x24427A, 0x5B8BD0, 0xD6E6FF}, false},
	{"Green", [4]uint32{0x0E2410, 0x2B6431, 0x6DB36F, 0xDDF6D8}, false},
	{"Gold", [4]uint32{0x241A06, 0x6B5216, 0xBB9A45, 0xF6E8C2}, false},
	{"Coral", [4]uint32{0x2A0E0A, 0x7C2E22, 0xE06A52, 0xFFD9CF}, false},
	{"Lavender", [4]uint32{0x1A1430, 0x463A78, 0x8C7ED0, 0xE8E2FF}, false},
	{"Mint", [4]uint32{0x0A231C, 0x23664F, 0x59C39C, 0xD9FFF0}, false},
	{"Sepia", [4]uint32{0x2B1D0E, 0x6B4E2E, 0xA88A66, 0xEADBC4}, false},
	{"Cocoa", [4]uint32{0x1B100B, 0x4A2E20, 0x8A6050, 0xD8BBA6}, true},
	{"Cherry", [4]uint32{0x200004, 0x6E0A1A, 0xC8283C, 0xFFC9D0}, false},
	{"Ocean", [4]uint32{0x001A26, 0x00506E, 0x0091B8, 0xA6EEFF}, false},
	{"Forest", [4]uint32{0x0B1A0B, 0x224222, 0x4F7F3F, 0xB5D69A}, true},
	{"Sunset", [4]uint32{0x1F0B26, 0x6E1F4A, 0xE0603A, 0xFFD38A}, false},
	{"Neon", [4]uint32{0x07000F, 0x4B0C7A, 0xE0359B, 0xFFE45C}, true},
	{"Phosphor", [4]uint32{0x001100, 0x0B3D0B, 0x2EA82E, 0x8CFF8C}, true},
	{"Terminal", [4]uint32{0x001414, 0x004848, 0x00A0A0, 0x8CFFFF}, true},
	{"Vaporwave", [4]uint32{0x1A0A2E, 0x5A2A8A, 0xFF71CE, 0xB8FFF9}, true},
	{"Crimson", [4]uint32{0x120000, 0x4A0000, 0xA01010, 0xFF6060}, true},
	{"Sakura", [4]uint32{0x2A1620, 0x7A4A5E, 0xE7A3BC, 0xFFF0F5}, false},
	{"Sand", [4]uint32{0x2A2416, 0x6E6244, 0xB9A97A, 0xF3EAD0}, false},
	{"Slate", [4]uint32{0x11161C, 0x39444F, 0x74838F, 0xC9D3DB}, true},
	{"Plum", [4]uint32{0x1C0A1C, 0x552155, 0x9E5A9E, 0xEBCFEB}, false},
	{"Teal", [4]uint32{0x021A1A, 0x0B4F4F, 0x2A9494, 0xB8E8E0}, false},
	{"Peach", [4]uint32{0x2A140C, 0x7A4232, 0xE89A7A, 0xFFE6D8}, false},
	{"Night", [4]uint32{0x050812, 0x141E3C, 0x2E4A8A, 0x8FA8E8}, true},
	{"Paper", [4]uint32{0x262626, 0x6E6E6E, 0xB4B4B4, 0xF2F0E8}, false},
	{"Matrix", [4]uint32{0x000800, 0x003B00, 0x008F11, 0x00FF41}, true},
	{"Virtual Boy", [4]uint32{0x000000, 0x550000, 0xAA0000, 0xFF0000}, true},
	{"Kyiv", [4]uint32{0x0B1630, 0x0057B7, 0x7FA7D9, 0xFFD700}, false},
	{"Midnight LCD", [4]uint32{0x101418, 0x2A3640, 0x56707E, 0xB6CDD8}, true},
}

// musicPodTransitions are the six ways one screen gives way to the next.
var musicPodTransitions = []string{"Push", "Slide", "Fade", "Zoom", "Dither", "None"}

const (
	musicPodThemeKey      = "musicPodTheme"
	musicPodTransitionKey = "musicPodTransition"
	musicPodClickerKey    = "musicPodClicker"
)

func podRGB(v uint32) sdl.Color {
	return sdl.Color{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
}

// musicPodThemeIndex is the chosen theme (Classic when unset).
func musicPodThemeIndex(config *Config) int {
	if config == nil {
		return 0
	}
	raw := strings.TrimSpace(customStringValue(config.Variables.Custom, musicPodThemeKey))
	for i, t := range musicPodThemes {
		if strings.EqualFold(t.name, raw) {
			return i
		}
	}
	return 0
}

func musicPodSetTheme(config *Config, i int) {
	if config == nil {
		return
	}
	n := len(musicPodThemes)
	i = ((i % n) + n) % n
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicPodThemeKey] = musicPodThemes[i].name
	podArtFlush()
}

// musicPodPalette is the theme's four shades, darkest first.
func musicPodPalette(config *Config) [4]sdl.Color {
	t := musicPodThemes[musicPodThemeIndex(config)]
	return [4]sdl.Color{podRGB(t.pal[0]), podRGB(t.pal[1]), podRGB(t.pal[2]), podRGB(t.pal[3])}
}

// musicVizPalette is the visualizer's palette: the iPod theme in the iPod
// layout, else four shades of the accent colour.
func musicVizPalette(config *Config) [4]sdl.Color {
	if musicPodActive(config) {
		return musicPodPalette(config)
	}
	a := accentColor
	scale := func(f float64) sdl.Color {
		return sdl.Color{R: uint8(float64(a.R) * f), G: uint8(float64(a.G) * f), B: uint8(float64(a.B) * f), A: 255}
	}
	return [4]sdl.Color{{R: 8, G: 11, B: 18, A: 255}, scale(0.34), a, {R: 236, G: 240, B: 248, A: 255}}
}

// musicPodInk is the theme mapped onto the screen's parts.
type musicPodInk struct {
	bg, ink, dim, line, selBg, selInk, head, headInk sdl.Color
}

func musicPodColors(config *Config) musicPodInk {
	t := musicPodThemes[musicPodThemeIndex(config)]
	p := [4]sdl.Color{podRGB(t.pal[0]), podRGB(t.pal[1]), podRGB(t.pal[2]), podRGB(t.pal[3])}
	if t.dark {
		return musicPodInk{bg: p[0], ink: p[3], dim: p[2], line: p[1], selBg: p[2], selInk: p[0], head: p[1], headInk: p[3]}
	}
	return musicPodInk{bg: p[3], ink: p[0], dim: p[1], line: p[2], selBg: p[1], selInk: p[3], head: p[2], headInk: p[0]}
}

func musicPodTransitionIndex(config *Config) int {
	if config == nil {
		return 0
	}
	raw := strings.TrimSpace(customStringValue(config.Variables.Custom, musicPodTransitionKey))
	for i, t := range musicPodTransitions {
		if strings.EqualFold(t, raw) {
			return i
		}
	}
	return 0
}

func musicPodSetTransition(config *Config, i int) {
	if config == nil {
		return
	}
	n := len(musicPodTransitions)
	i = ((i % n) + n) % n
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicPodTransitionKey] = musicPodTransitions[i]
}

// musicPodClickerOn reports whether the wheel ticks (on by default).
func musicPodClickerOn(config *Config) bool {
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, musicPodClickerKey, true)
}

func musicPodSetClicker(config *Config, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicPodClickerKey] = boolString(on)
}

// ──────────────────────────────────────────────────────────────────────
// The click-wheel tick
// ──────────────────────────────────────────────────────────────────────

var podClick struct {
	once sync.Once
	pcm  []byte
	tmp  []byte
	rate int32
}

// podClickPCM is the tick as mono S16: a 6 ms burst of filtered noise and a
// short high tone, like the click wheel's piezo.
func podClickPCM(rate int32) []byte {
	if rate <= 0 {
		return nil
	}
	sr := float64(rate)
	n := int(0.006 * sr)
	out := make([]byte, n*2)
	seed := uint32(0x2545F491)
	prev := 0.0
	for i := 0; i < n; i++ {
		t := float64(i) / sr
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		noise := float64(int32(seed)) / 2147483648.0
		hp := noise - prev
		prev = noise
		v := 0.2*hp*math.Exp(-t/0.0007) + 0.16*math.Sin(2*math.Pi*2800*t)*math.Exp(-t/0.0016)
		s := int16(math.Round(math.Max(-0.95, math.Min(0.95, v)) * 32767))
		out[i*2], out[i*2+1] = byte(s), byte(uint16(s)>>8)
	}
	return out
}

// musicPodTick plays the tick (or the usual move sound with the clicker off),
// with the move's haptic pulse.
func musicPodTick(config *Config) {
	if !musicPodClickerOn(config) {
		PlayNavSound()
		return
	}
	playHaptic(hapticNavPulse)
	if !bootSfxEnabled() || quietModeOn() || alarmFiringActive() {
		return
	}
	audioInitOnce.Do(initAudioDevice)
	if !audioAvailable {
		return
	}
	podClick.once.Do(func() {
		podClick.rate = audioSampleRate
		podClick.pcm = podClickPCM(audioSampleRate)
		podClick.tmp = make([]byte, len(podClick.pcm))
	})
	if len(podClick.pcm) == 0 {
		return
	}
	data := podClick.pcm
	if level := bootSfxVolume(); level < 100 {
		gain := float64(level) / 100
		gain *= gain
		data = podClick.tmp[:len(podClick.pcm)]
		for i := 0; i+1 < len(podClick.pcm); i += 2 {
			s := int16(uint16(podClick.pcm[i]) | uint16(podClick.pcm[i+1])<<8)
			v := int16(math.Round(float64(s) * gain))
			data[i], data[i+1] = byte(v), byte(uint16(v)>>8)
		}
	}
	audioMutex.Lock()
	defer audioMutex.Unlock()
	// A held D-pad must not build a backlog that ticks on after the key is up.
	if limit := uint32(float64(podClick.rate) * 2 * 0.05); outQueued(audioDev) > limit {
		return
	}
	_ = outQueue(audioDev, data)
}

// ──────────────────────────────────────────────────────────────────────
// Dithered album art
// ──────────────────────────────────────────────────────────────────────

const (
	podArtSize   = 128 // the art's own pixels, scaled up square on screen
	podArtKeep   = 24
	podArtFetchW = 320
)

type podArtEntry struct {
	tex  *sdl.Texture
	used uint64
}

var podArt struct {
	mu      sync.Mutex
	ren     *sdl.Renderer
	cache   map[string]*podArtEntry
	ready   map[string][]byte // dithered RGBA waiting for the render thread
	pending map[string]bool
	failed  map[string]time.Time
	tick    uint64
}

// podArtFlush forgets every texture (a new theme recolours them all).
func podArtFlush() {
	podArt.mu.Lock()
	defer podArt.mu.Unlock()
	for _, e := range podArt.cache {
		if e.tex != nil {
			_ = e.tex.Destroy()
		}
	}
	podArt.cache = nil
	podArt.ready = nil
}

// musicPodArt is the art's texture in the theme's shades, or nil while it is
// being made (or when there is none). Render thread.
func musicPodArt(renderer *sdl.Renderer, art string, pal [4]sdl.Color, dither bool) *sdl.Texture {
	if art == "" || renderer == nil {
		return nil
	}
	key := fmt.Sprintf("%s|%v|%v", art, pal, dither)
	podArt.mu.Lock()
	defer podArt.mu.Unlock()
	if podArt.ren != renderer {
		podArt.cache, podArt.ready, podArt.ren = nil, nil, renderer
	}
	if podArt.cache == nil {
		podArt.cache = map[string]*podArtEntry{}
		podArt.ready = map[string][]byte{}
		podArt.pending = map[string]bool{}
		podArt.failed = map[string]time.Time{}
	}
	podArt.tick++
	if e, ok := podArt.cache[key]; ok {
		e.used = podArt.tick
		return e.tex
	}
	if pix, ok := podArt.ready[key]; ok {
		delete(podArt.ready, key)
		tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, podArtSize, podArtSize)
		if err != nil || tex == nil {
			return nil
		}
		if err := tex.Update(nil, unsafe.Pointer(&pix[0]), podArtSize*4); err != nil {
			_ = tex.Destroy()
			return nil
		}
		podArt.cache[key] = &podArtEntry{tex: tex, used: podArt.tick}
		podArtEvictLocked()
		return tex
	}
	if podArt.pending[key] {
		return nil
	}
	if at, ok := podArt.failed[key]; ok && time.Since(at) < 2*time.Minute {
		return nil
	}
	podArt.pending[key] = true
	go func() {
		pix, err := podArtMake(art, pal, dither)
		podArt.mu.Lock()
		defer podArt.mu.Unlock()
		if podArt.pending == nil {
			return
		}
		delete(podArt.pending, key)
		if err != nil || len(pix) == 0 {
			podArt.failed[key] = time.Now()
			return
		}
		if podArt.ready != nil {
			podArt.ready[key] = pix
		}
	}()
	return nil
}

func podArtEvictLocked() {
	for len(podArt.cache) > podArtKeep {
		oldest, at := "", uint64(math.MaxUint64)
		for k, e := range podArt.cache {
			if e.used < at {
				oldest, at = k, e.used
			}
		}
		if e := podArt.cache[oldest]; e != nil && e.tex != nil {
			_ = e.tex.Destroy()
		}
		delete(podArt.cache, oldest)
	}
}

// podArtMake reads the picture (fetching a server's cover into the art cache
// first) and dithers it into the palette. Off the render thread.
func podArtMake(art string, pal [4]sdl.Color, dither bool) ([]byte, error) {
	path, err := musicArtFile(art)
	if err != nil {
		return nil, err
	}
	data, err := readSmallFile(path, 16<<20)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	img, err := decodeImageRGBA(ctx, data)
	if err != nil {
		return nil, err
	}
	return podDitherArt(img, pal, dither), nil
}

// podDitherArt scales a picture to podArtSize square (centre crop), stretches
// its contrast and orders it into the four shades. Pure.
func podDitherArt(img *image.RGBA, pal [4]sdl.Color, dither bool) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	side := w
	if h < side {
		side = h
	}
	x0, y0 := b.Min.X+(w-side)/2, b.Min.Y+(h-side)/2
	lum := make([]float64, podArtSize*podArtSize)
	lo, hi := 1.0, 0.0
	for y := 0; y < podArtSize; y++ {
		sy0 := y0 + y*side/podArtSize
		sy1 := y0 + (y+1)*side/podArtSize
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < podArtSize; x++ {
			sx0 := x0 + x*side/podArtSize
			sx1 := x0 + (x+1)*side/podArtSize
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var sum float64
			n := 0
			for sy := sy0; sy < sy1; sy += 1 + (sy1-sy0)/4 {
				for sx := sx0; sx < sx1; sx += 1 + (sx1-sx0)/4 {
					o := img.PixOffset(sx, sy)
					sum += 0.2126*float64(img.Pix[o]) + 0.7152*float64(img.Pix[o+1]) + 0.0722*float64(img.Pix[o+2])
					n++
				}
			}
			v := sum / float64(n) / 255
			lum[y*podArtSize+x] = v
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
	}
	span := hi - lo
	if span < 0.05 {
		span = 0.05
	}
	out := make([]byte, podArtSize*podArtSize*4)
	for y := 0; y < podArtSize; y++ {
		for x := 0; x < podArtSize; x++ {
			v := (lum[y*podArtSize+x] - lo) / span
			var c sdl.Color
			if dither {
				q := int(v*3 + (float64(vizBayer[y&3][x&3])+0.5)/16)
				if q > 3 {
					q = 3
				} else if q < 0 {
					q = 0
				}
				c = pal[q]
			} else {
				f := v * 3
				i := int(f)
				if i > 2 {
					i = 2
				}
				t := f - float64(i)
				a, d := pal[i], pal[i+1]
				c = sdl.Color{R: uint8(float64(a.R) + (float64(d.R)-float64(a.R))*t),
					G: uint8(float64(a.G) + (float64(d.G)-float64(a.G))*t),
					B: uint8(float64(a.B) + (float64(d.B)-float64(a.B))*t)}
			}
			o := (y*podArtSize + x) * 4
			out[o], out[o+1], out[o+2], out[o+3] = c.R, c.G, c.B, 255
		}
	}
	return out
}

// musicArtFile is a picture on the card for an art key: a local file as it
// is, a server's cover fetched once into the art cache.
func musicArtFile(art string) (string, error) {
	switch {
	case strings.HasPrefix(art, "nd:"), strings.HasPrefix(art, "jf:"):
	default:
		if _, err := os.Stat(art); err != nil {
			return "", err
		}
		return art, nil
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return "", fmt.Errorf("no data folder")
	}
	sum := sha1.Sum([]byte(art))
	path := filepath.Join(dir, "music-art", hex.EncodeToString(sum[:10])+".jpg")
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return path, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var resp *http.Response
	id := art[3:]
	if strings.HasPrefix(art, "nd:") {
		c, cerr := ssNewClient()
		if cerr != nil {
			return "", cerr
		}
		resp, err = musicGet(ctx, c.base.Scheme, c.coverURL(id, podArtFetchW), "", "")
	} else {
		c, cerr := jfNewClient()
		if cerr != nil {
			return "", cerr
		}
		target := musicJoinURL(c.base, "/Items/"+url.PathEscape(id)+"/Images/Primary",
			url.Values{"maxWidth": {fmt.Sprint(podArtFetchW)}, "quality": {"85"}})
		resp, err = musicGet(ctx, c.base.Scheme, target, jfAuthHeader(c.token), "")
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("no cover (%d)", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := AtomicWrite(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
