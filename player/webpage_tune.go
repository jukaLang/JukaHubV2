package main

// webpage_tune.go — how hard the reader may work on the machine it runs on.
//
// The reader used to make the same choices on every machine: each picture was
// decoded on the render thread, at its full size, in the frame it first
// scrolled into view, and nothing below the screen was prepared ahead. On a PC
// that costs a few milliseconds. On a handheld a 2000-px photo is on the order
// of 100 ms of JPEG work plus a 12 MB texture upload — a visible stall in the
// middle of a scroll — and a picture longer than the GPU's texture limit (a
// long infographic or screenshot) could not become one texture at all, so it
// drew as a grey placeholder.
//
// The tune says, per device, how many threads decode pictures off the render
// thread (webpage_decode.go), how far beyond the view they are decoded, how
// many bytes of new textures one frame may upload, and how many concurrent
// downloads a page's images get:
//
//   - Smart Pro S (TG5050, Allwinner A523: eight Cortex-A55 in two clusters,
//     Mali-G57, Wi-Fi 6): four decode threads, two screens ahead and one
//     behind, 8 MB of uploads per frame, ten downloads at once.
//   - The A133P handhelds (Smart Pro, Brick, Brick Pro, Hammer: four
//     Cortex-A53, PowerVR GE8300): two decode threads, so a core stays with the
//     menus and the audio, one screen ahead, 4 MB per frame.
//   - Any other machine (a PC, a Linux desktop, the test harness): decoding
//     stays on the render thread, exactly as before.
//
// JUKAHUB_WEB_DECODE=0 keeps a handheld on the old render-thread path;
// JUKAHUB_WEB_DECODE=N (1-8) sets the thread count.
//
// It also says how the page reaches the screen and how soon: how many page
// tiles the GPU keeps (webpage_tiles.go; 12 on the Smart Pro S, 10 on the
// A133P handhelds, none on a PC, where the page is painted directly as
// before), how often a page may be laid out again while its pictures and web
// fonts arrive (every 250 / 400 ms on the handhelds, at once on a PC), and
// how long a page's scripts run before the page as its server sent it is drawn
// meanwhile (0.9 / 0.8 s on the handhelds, 1.5 s on a PC).
// JUKAHUB_WEB_TILES=0 paints the page directly on a handheld too;
// JUKAHUB_WEB_TILES=N (1-64) keeps N tiles, also on a PC.

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// webTune is the reader's work budget on this machine.
type webTune struct {
	// Name is what the log calls the profile.
	Name string
	// DecodeWorkers is how many threads decode pictures; 0 decodes them on
	// the render thread when they are first drawn.
	DecodeWorkers int
	// AheadScreens and BehindScreens are how far below and above the view
	// pictures are decoded before they scroll in.
	AheadScreens  float64
	BehindScreens float64
	// UploadBytes is how many bytes of new picture textures one frame may
	// create; a single larger picture still goes, alone.
	UploadBytes int64
	// DecodeBytes bounds the decoded pixels in flight across the threads, so
	// four 12-megapixel photos are never open at once on a 1 GB device.
	DecodeBytes int64
	// MaxPixels refuses a picture whose canvas is larger than this many
	// pixels: a small PNG can declare 20000×20000 and ask for 1.6 GB.
	MaxPixels int64
	// MaxSide is the longest side a picture texture may have; the renderer's
	// own limit, when lower, wins (webTuneTextureLimit).
	MaxSide int32
	// FetchWorkers is how many of a page's images download at once.
	FetchWorkers int
	// Tiles is how many page tiles the GPU keeps (webpage_tiles.go): the page
	// is painted into them once and copied to the screen each frame. 0 paints
	// the page directly every frame.
	Tiles int
	// RelayoutEvery is the least time between two layouts of a page that its
	// arriving pictures and web fonts ask for (layoutFor); 0 lays it out again
	// on the next frame.
	RelayoutEvery time.Duration
	// PreviewAfter is how long a page's scripts run before the page as its
	// server sent it is drawn meanwhile (browserRunScripts).
	PreviewAfter time.Duration
}

// The profiles. Named so the docs and the log agree on the numbers.
var webTuneSmartProS = webTune{
	Name: "Smart Pro S", DecodeWorkers: 4, AheadScreens: 2, BehindScreens: 1,
	UploadBytes: 8 << 20, DecodeBytes: 96 << 20, MaxPixels: 24 << 20, MaxSide: 4096,
	FetchWorkers: 10, Tiles: 12, RelayoutEvery: 250 * time.Millisecond, PreviewAfter: 900 * time.Millisecond,
}

var webTuneA133P = webTune{
	Name: "A133P handheld", DecodeWorkers: 2, AheadScreens: 1, BehindScreens: 0.5,
	UploadBytes: 4 << 20, DecodeBytes: 48 << 20, MaxPixels: 24 << 20, MaxSide: 4096,
	FetchWorkers: 8, Tiles: 10, RelayoutEvery: 400 * time.Millisecond, PreviewAfter: 800 * time.Millisecond,
}

var webTuneDesktop = webTune{
	Name: "desktop", DecodeWorkers: 0, AheadScreens: 0, BehindScreens: 0,
	UploadBytes: 0, DecodeBytes: 0, MaxPixels: 100 << 20, MaxSide: 16384,
	FetchWorkers: 8, Tiles: 0, RelayoutEvery: 0, PreviewAfter: 1500 * time.Millisecond,
}

// webTuneFor picks the profile for a machine: trimui says it is a TrimUI
// handheld, soc names its chip (socA523, socA133P or "" when unknown) and
// cpus is the online core count. env is JUKAHUB_WEB_DECODE.
func webTuneFor(trimui bool, soc string, cpus int, env string) webTune {
	t := webTuneDesktop
	if trimui {
		switch {
		case soc == socA523:
			t = webTuneSmartProS
		case soc == socA133P:
			t = webTuneA133P
		case cpus >= 8:
			// An unnamed board with eight cores is the A523 family.
			t = webTuneSmartProS
		default:
			t = webTuneA133P
		}
	}
	env = strings.TrimSpace(env)
	if env == "" {
		return t
	}
	n, err := strconv.Atoi(env)
	if err != nil || n < 0 {
		return t
	}
	if n > 8 {
		n = 8
	}
	if n == 0 {
		t.DecodeWorkers = 0
		return t
	}
	if t.DecodeWorkers == 0 {
		// A PC asked for threads: give it the A133P's ahead window and
		// budgets, which are the conservative ones.
		t.AheadScreens, t.BehindScreens = webTuneA133P.AheadScreens, webTuneA133P.BehindScreens
		t.UploadBytes, t.DecodeBytes = webTuneA133P.UploadBytes, webTuneA133P.DecodeBytes
	}
	t.DecodeWorkers = n
	return t
}

var webTuneState struct {
	once sync.Once
	tune webTune
}

// webTuneNow is this machine's profile, chosen once.
func webTuneNow() webTune {
	webTuneState.once.Do(func() {
		trimui := IsTrimuiDevice()
		soc := ""
		if trimui {
			soc = deviceHardware(DetectDevice()).SoC
		}
		webTuneState.tune = webTuneTiles(webTuneFor(trimui, soc, runtime.NumCPU(), os.Getenv("JUKAHUB_WEB_DECODE")),
			os.Getenv("JUKAHUB_WEB_TILES"))
		t := webTuneState.tune
		logInfo("browser", "reader tune: %s, %d decode thread(s), %.1f screen(s) ahead, %d downloads at once, %d page tiles",
			t.Name, t.DecodeWorkers, t.AheadScreens, t.FetchWorkers, t.Tiles)
	})
	return webTuneState.tune
}

// webTuneTiles applies JUKAHUB_WEB_TILES to a profile: 0 paints pages
// directly, N (1-64) keeps N page tiles on any machine.
func webTuneTiles(t webTune, env string) webTune {
	env = strings.TrimSpace(env)
	if env == "" {
		return t
	}
	n, err := strconv.Atoi(env)
	if err != nil || n < 0 {
		return t
	}
	t.Tiles = min(n, 64)
	return t
}

// webTuneTextureLimit is the longest picture side the renderer takes: the
// tune's MaxSide, lowered to the GPU's own maximum when the renderer reports
// one (webDecodeUpload reads it once, from the renderer that draws pages).
func webTuneTextureLimit(maxSide, rendererW, rendererH int32) int32 {
	limit := maxSide
	if limit <= 0 {
		limit = 16384
	}
	for _, v := range [...]int32{rendererW, rendererH} {
		if v > 0 && v < limit {
			limit = v
		}
	}
	if limit < 256 {
		limit = 256
	}
	return limit
}
