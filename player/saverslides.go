package main

// saverslides.go — two more idle screens: "Game slideshow" and "Game videos"
// (the "slideshow" and "random video" screensavers of Batocera).
//
// Settings → Power → Idle screen picks the clock dock (ambient.go), the
// starfield (screensaver.go) or one of these two. The dock is still the
// boolean Custom "ambientMode", so old configs keep what they had; the other
// three share the starfield's slot and are told apart by Custom
// "idleScreenKind" ("stars", "slides" or "videos"; unset is the starfield).
// renderScreensaver asks saverSlidesRender first and draws the starfield when
// it declines (another kind, or nothing to show).
//
//   - Game slideshow: a random game's picture every saverSlidesHoldMS — its
//     gameplay picture (playshot_cover.go) or its cover (box art, title
//     screen or snap: what the library shows), picked at random when it has
//     both — shown whole, drifting slowly, crossfading into the next, with
//     the game's title and console in a small caption. Between fades the
//     idle frame rate applies (saver_power.go asks saverSlidesFrameBudget).
//   - Game videos: a random video snap — the library's snaps,
//     <data>/game-snaps/<System>/<rom stem>.mp4 or .webm (snappreview.go) —
//     muted and letterboxed, the next one when it ends. The choice is only
//     offered when the card has snaps; with none for the library's games the
//     slideshow shows instead. The decoder is snappreview.go's command (the
//     bundled ffmpeg at a small size, raw YUV over a pipe, the texture made
//     on the render thread only), here without its loop and at the screen's
//     shape. While a clip plays the CPU keeps the ceiling the dock keeps for
//     music (saverSlidesVideoPlaying, saver_power.go).
//
// Nothing else is drawn — no clock, weather or network chip — so a screenshot
// of either says nothing about the device or where it is. The games come from
// the listing the automatic collections read (autoCollSync: Home's library,
// hidden games left out). Pictures are decoded off the render thread by
// saver_backdrop.go's decoder (EXIF, size limits, the shared decode permit),
// one at a time and never larger than the screen. A press wakes as on every
// idle screen; the wake (deactivateScreensaver → saverSlidesWake) stops the
// decoder and frees every texture and picture.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// saverSlidesKindKey is the Custom key telling the starfield's slot apart.
	saverSlidesKindKey = "idleScreenKind"

	// The idle-screen choices (the Settings row's values).
	saverKindDock   = "dock"
	saverKindStars  = "stars"
	saverKindSlides = "slides"
	saverKindVideos = "videos"

	// saverSlidesHoldMS is how long a picture stays, saverSlidesFadeMS its
	// crossfade, saverSlidesLeadMS how early the next one is decoded.
	saverSlidesHoldMS = 8000
	saverSlidesFadeMS = 1200
	saverSlidesLeadMS = 3000
	// saverSlidesFailMax is how many pictures in a row may fail to decode
	// before the slideshow gives up for this idle spell (the starfield shows).
	saverSlidesFailMax = 8
	// saverSlidesStuckMS frees a decode that never answered;
	// saverSlidesRefillMS is how often an empty pool is built again.
	saverSlidesStuckMS  = 20000
	saverSlidesRefillMS = 3000
	// saverSlidesFit is the share of the picture area a picture fills, so
	// there is always room for it to drift.
	saverSlidesFit = 0.94

	// The video decode: saverVideoWidth wide at the screen's shape,
	// saverVideoFPS frames a second, drawn at most every saverVideoFrameMS.
	saverVideoWidth   = 480
	saverVideoFPS     = 20
	saverVideoFrameMS = uint32(40)
	// saverVideoFirstMS is how long a clip may take to show its first frame,
	// saverVideoMaxMS how long one may play before the next, saverVideoFadeMS
	// how long a new clip takes to fade in.
	saverVideoFirstMS = 8000
	saverVideoMaxMS   = 90000
	saverVideoFadeMS  = 400

	// saverClipsTTL is how long a count of the snaps on the card is trusted,
	// saverClipsMax how many are listed.
	saverClipsTTL = time.Minute
	saverClipsMax = 20000
)

// ── The choice ────────────────────────────────────────────────────────────

// idleScreenKindOf is the idle screen chosen: dock, stars, slides or videos.
func idleScreenKindOf(c *Config) string {
	if c == nil || ambientModeEnabled(c) {
		return saverKindDock
	}
	switch v := strings.ToLower(strings.TrimSpace(customStringValue(c.Variables.Custom, saverSlidesKindKey))); v {
	case saverKindSlides, saverKindVideos:
		return v
	}
	return saverKindStars
}

// idleScreenOptions are the Settings row's stops: Game videos only when the
// card has snaps (or it is the stored choice, so the row can show it).
func idleScreenOptions(c *Config) []setOption {
	opts := []setOption{
		{Value: saverKindDock, Label: "Clock dock"},
		{Value: saverKindStars, Label: "Starfield"},
		{Value: saverKindSlides, Label: "Game slideshow"},
	}
	if saverClipsAvailable() || idleScreenKindOf(c) == saverKindVideos {
		opts = append(opts, setOption{Value: saverKindVideos, Label: "Game videos"})
	}
	return opts
}

// idleScreenIndex is the stored choice's stop.
func idleScreenIndex(c *Config) int {
	cur := idleScreenKindOf(c)
	for i, o := range idleScreenOptions(c) {
		if o.Value == cur {
			return i
		}
	}
	return -1
}

// idleScreenChoose stores a choice: the dock in "ambientMode" as before, the
// starfield's kinds in "idleScreenKind" beside it.
func idleScreenChoose(c *Config, value string) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom["ambientMode"] = value == saverKindDock
	if value != saverKindDock {
		c.Variables.Custom[saverSlidesKindKey] = value
	}
	saveUserConfigDebounced(userConfigFromVariables(c))
}

// idleScreenDetail is the row's line for the stored choice.
func idleScreenDetail(c *Config) string {
	switch idleScreenKindOf(c) {
	case saverKindStars:
		return "Stars with the time and what is playing"
	case saverKindSlides:
		return "Your games' pictures, a new one every 8 seconds"
	case saverKindVideos:
		if !saverClipsAvailable() {
			return "No video snaps on the card: the slideshow shows instead"
		}
		return "Your games' video snaps, muted, one after another"
	}
	return "The clock dock shows the time, the weather and what is playing"
}

// ── The snaps on the card ─────────────────────────────────────────────────

// saverClip is one snap file and the game it is named after
// ("<System_with_underscores>/<rom stem>", gameSnapPath's layout).
type saverClip struct {
	path, key string
}

// saverClips is the last listing of the snap folder, made on a goroutine.
var saverClips struct {
	mu       sync.Mutex
	root     string
	known    bool
	scanning bool
	at       time.Time
	list     []saverClip
}

// saverClipsSnapshot is the last listing of root (the slice is replaced, never
// changed, so it can be read after the lock), and starts a new one when it
// is stale. known is false until a listing of root landed.
func saverClipsSnapshot(root string) ([]saverClip, bool) {
	saverClips.mu.Lock()
	same := saverClips.known && saverClips.root == root
	start := root != "" && !saverClips.scanning && (!same || time.Since(saverClips.at) >= saverClipsTTL)
	if start {
		saverClips.scanning = true
	}
	var list []saverClip
	if same {
		list = saverClips.list
	}
	saverClips.mu.Unlock()
	if start {
		go func() {
			found := saverClipsWalk(root)
			saverClips.mu.Lock()
			saverClips.root, saverClips.list, saverClips.known = root, found, true
			saverClips.at = time.Now()
			saverClips.scanning = false
			saverClips.mu.Unlock()
		}()
	}
	return list, same
}

// saverClipsAvailable reports whether the snap folder has any clip.
func saverClipsAvailable() bool {
	list, _ := saverClipsSnapshot(snapPreviewRoot(appConfig))
	return len(list) > 0
}

// saverClipsWalk lists root/<console>/<stem>.mp4|.webm. Off the render thread.
func saverClipsWalk(root string) []saverClip {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []saverClip
	for _, d := range dirs {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			ext := filepath.Ext(name)
			if f.IsDir() || strings.HasPrefix(name, ".") || !saverClipExt(ext) {
				continue
			}
			out = append(out, saverClip{
				path: filepath.Join(root, d.Name(), name),
				key:  d.Name() + "/" + strings.TrimSuffix(name, ext),
			})
			if len(out) >= saverClipsMax {
				return out
			}
		}
	}
	return out
}

// saverClipExt reports whether ext is one of the snap extensions.
func saverClipExt(ext string) bool {
	for _, e := range snapExtensions {
		if strings.EqualFold(e, ext) {
			return true
		}
	}
	return false
}

// ── State ─────────────────────────────────────────────────────────────────

// saverSlideGame is one game the screen can show: its ROM (or, in the video
// pool, its clip), title and console, and its cover.
type saverSlideGame struct {
	path, system, title, art string
}

// saverSlideShown is a picture on screen (or waiting for its turn).
type saverSlideShown struct {
	tex           *sdl.Texture
	w, h          int32
	title, system string
	path          string
	at            uint64 // the ticks it came up
	dir           int32  // which way it drifts: +1 or -1
}

// saverSlides is the screen's state. Render thread only.
var saverSlides struct {
	on    bool
	drew  bool   // the last frame was this screen's (not the starfield's)
	want  string // the choice it was started for
	kind  string // what it shows: slides, or videos while they work
	retry bool   // the snap listing was not in yet: look again
	gen   uint64

	games          []saverSlideGame
	order          []int
	pos            int
	cur, prev, nxt saverSlideShown
	loading        bool
	askedAt        uint64
	fails          int
	dir            int32

	clips     []saverSlideGame
	clipOrder []int
	clipPos   int
	clipNow   saverSlideGame
	clipAt    uint64 // ticks the clip was started
	clipSeen  uint64 // ticks its first frame was drawn, 0 before

	filledAt uint64 // ticks the pools were last built
	cleanup  bool
}

// saverSlideDecoded is a finished picture, handed to the render thread.
type saverSlideDecoded struct {
	game saverSlideGame
	pix  *image.RGBA
	err  error
}

// saverSlideReady is the hand-over slot: the newest session's picture wins.
var saverSlideReady struct {
	mu  sync.Mutex
	gen uint64
	pic *saverSlideDecoded
}

// ── The screen ────────────────────────────────────────────────────────────

// saverSlidesRender draws one frame of the slideshow or the videos, and
// reports whether it did; renderScreensaver draws the starfield when not.
// The fade-in is the starfield's (ss.fadeAlpha), one veil over the frame.
func saverSlidesRender(renderer *sdl.Renderer, config *Config, tick uint64) bool {
	kind := idleScreenKindOf(config)
	if kind != saverKindSlides && kind != saverKindVideos {
		if saverSlides.on {
			saverSlidesStop()
		}
		return false
	}
	if !saverSlides.on || saverSlides.want != kind {
		saverSlidesStop()
		saverSlidesStart(config, kind, tick)
	}
	if saverSlides.retry {
		saverSlidesRetryClips(config)
	}
	if len(saverSlides.games) == 0 && len(saverSlides.clips) == 0 && tick-saverSlides.filledAt >= saverSlidesRefillMS {
		saverSlidesFill(config, tick)
	}
	renderer.SetDrawColor(saverBackdropBase.R, saverBackdropBase.G, saverBackdropBase.B, 255)
	renderer.Clear()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	drew := false
	if saverSlides.kind == saverKindVideos {
		drew = saverVideoDraw(renderer, config, tick)
	}
	if !drew {
		drew = saverSlidesDraw(renderer, config, tick)
	}
	saverSlides.drew = drew
	if !drew {
		return false
	}
	if ss.fadeAlpha < 255 {
		renderer.SetDrawColor(saverBackdropBase.R, saverBackdropBase.G, saverBackdropBase.B, 255-ss.fadeAlpha)
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	}
	return true
}

// saverSlidesStart builds the pools for one idle spell.
func saverSlidesStart(config *Config, kind string, tick uint64) {
	s := &saverSlides
	s.on, s.want, s.kind = true, kind, saverKindSlides
	s.gen++
	s.dir = 1
	if !s.cleanup {
		// A clip still decoding when JukaHub quits must not outlive it.
		s.cleanup = true
		Cleanup = append(Cleanup, saverVideoHalt)
	}
	saverSlidesFill(config, tick)
}

// saverSlidesFill builds the pools from the library listing. With no library
// yet (Home has not read the card in this run) it is tried again every few
// seconds while the screen is up; the starfield shows meanwhile.
func saverSlidesFill(config *Config, tick uint64) {
	s := &saverSlides
	s.filledAt = tick
	autoCollSync()
	games := autoCollIdx.games
	s.games = s.games[:0]
	for _, g := range games {
		if g.HasArt && strings.TrimSpace(g.Art) != "" && !isBuiltInLibraryGame(g) {
			s.games = append(s.games, saverSlideGame{path: g.Path, system: g.System, title: g.Label(), art: g.Art})
		}
	}
	s.order, s.pos = nil, 0
	if s.want == saverKindVideos {
		clips, known := saverClipsSnapshot(snapPreviewRoot(config))
		s.clips = saverSlidesClipPool(games, clips)
		s.retry = !known
		if len(s.clips) > 0 {
			s.kind = saverKindVideos
		}
	}
}

// saverSlidesRetryClips takes the snap listing once it lands, when the spell
// started before it did.
func saverSlidesRetryClips(config *Config) {
	clips, known := saverClipsSnapshot(snapPreviewRoot(config))
	if !known {
		return
	}
	saverSlides.retry = false
	saverSlides.clips = saverSlidesClipPool(autoCollIdx.games, clips)
	if len(saverSlides.clips) > 0 {
		saverSlides.kind = saverKindVideos
	}
}

// saverSlidesClipPool is the clips named after a game of the listing.
func saverSlidesClipPool(games []libraryGame, clips []saverClip) []saverSlideGame {
	if len(clips) == 0 {
		return nil
	}
	byKey := make(map[string]libraryGame, len(games))
	for _, g := range games {
		if strings.TrimSpace(g.System) == "" || isBuiltInLibraryGame(g) {
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(g.Path), filepath.Ext(g.Path))
		byKey[strings.ReplaceAll(g.System, " ", "_")+"/"+stem] = g
	}
	var out []saverSlideGame
	for _, c := range clips {
		if g, ok := byKey[c.key]; ok {
			out = append(out, saverSlideGame{path: c.path, system: g.System, title: g.Label()})
		}
	}
	return out
}

// saverSlidesWake ends the screen: deactivateScreensaver calls it on every
// wake, so the decoder stops and the textures go before the UI draws.
func saverSlidesWake() {
	if saverSlides.on {
		saverSlidesStop()
	}
}

// saverSlidesStop stops the decoder and frees every texture and picture.
// Render thread.
func saverSlidesStop() {
	s := &saverSlides
	s.on, s.drew, s.want, s.retry = false, false, "", false
	s.gen++
	saverVideoStop()
	for _, sh := range []*saverSlideShown{&s.cur, &s.prev, &s.nxt} {
		if sh.tex != nil {
			sh.tex.Destroy()
		}
		*sh = saverSlideShown{}
	}
	s.games, s.order, s.pos = nil, nil, 0
	s.clips, s.clipOrder, s.clipPos = nil, nil, 0
	s.clipNow, s.clipAt, s.clipSeen = saverSlideGame{}, 0, 0
	s.loading, s.fails = false, 0
	saverSlideReady.mu.Lock()
	saverSlideReady.pic, saverSlideReady.gen = nil, s.gen
	saverSlideReady.mu.Unlock()
}

// saverSlidesShowing reports whether the idle screen up is this one (the
// starfield's photo and its clear button are then not on screen).
func saverSlidesShowing() bool {
	return ss.active && saverSlides.on && saverSlides.drew
}

// saverSlidesVideoPlaying reports whether a clip is decoding on the idle
// screen: saver_power.go then keeps the dock's music ceiling instead of the
// lowest one.
func saverSlidesVideoPlaying() bool {
	if !ss.active || !saverSlides.on || saverSlides.kind != saverKindVideos {
		return false
	}
	saverVid.mu.Lock()
	defer saverVid.mu.Unlock()
	return saverVid.run != nil
}

// saverSlidesFrameBudget is the idle frame budget while this screen is up
// (ok=false when it is not): every frame during a fade, the video's rate
// while a clip plays, the starfield's slow cadence while a picture drifts.
func saverSlidesFrameBudget(time.Time) (uint32, bool) {
	s := &saverSlides
	if !saverSlidesShowing() {
		return 0, false // the starfield's cadence, if anything
	}
	if s.kind == saverKindVideos {
		return saverVideoFrameMS, true
	}
	if s.cur.tex == nil || sdl.GetTicks64()-s.cur.at < saverSlidesFadeMS {
		return 0, true
	}
	return saverStarfieldFrameMS, true
}

// ── The slideshow ─────────────────────────────────────────────────────────

// saverSlidesArea is where pictures are fitted: the screen less its margins
// and the caption's band at the bottom.
func saverSlidesArea() sdl.Rect {
	mx, top, bottom := saverScaled(48, 16), saverScaled(36, 12), saverScaled(96, 52)
	return sdl.Rect{X: mx, Y: top, W: max(screenWidth-2*mx, 1), H: max(screenHeight-top-bottom, 1)}
}

// saverSlidesDraw draws the slideshow, false when it has nothing to show.
func saverSlidesDraw(renderer *sdl.Renderer, config *Config, tick uint64) bool {
	s := &saverSlides
	if len(s.games) == 0 || (s.fails >= saverSlidesFailMax && s.cur.tex == nil) {
		return false
	}
	saverSlidesTake(renderer)
	if s.nxt.tex != nil && (s.cur.tex == nil || tick-s.cur.at >= saverSlidesHoldMS) {
		if s.prev.tex != nil {
			s.prev.tex.Destroy()
		}
		s.prev, s.cur, s.nxt = s.cur, s.nxt, saverSlideShown{}
		s.cur.at = tick
	}
	if s.prev.tex != nil && tick-s.cur.at >= saverSlidesFadeMS {
		s.prev.tex.Destroy()
		s.prev = saverSlideShown{}
	}
	if s.loading && tick-s.askedAt >= saverSlidesStuckMS {
		s.loading = false
	}
	if !s.loading && s.nxt.tex == nil && s.fails < saverSlidesFailMax &&
		(s.cur.tex == nil || tick-s.cur.at+saverSlidesLeadMS >= saverSlidesHoldMS) {
		saverSlidesAsk(tick)
	}
	if s.cur.tex == nil {
		return true // the first picture is on its way: the dark base
	}
	t := 1.0
	if el := tick - s.cur.at; el < saverSlidesFadeMS {
		t = float64(el) / saverSlidesFadeMS
	}
	area := saverSlidesArea()
	if s.prev.tex != nil {
		saverSlidesDrawOne(renderer, &s.prev, area, tick, uint8(255*(1-t)))
	}
	saverSlidesDrawOne(renderer, &s.cur, area, tick, uint8(255*t))

	// The caption: the old one out, the new one in, through a veil of the
	// base (the band under it is plain base), so no text is drawn faded.
	shown := &s.cur
	veil := 0.0
	switch {
	case s.prev.tex != nil && t < 0.5:
		shown, veil = &s.prev, 2*t
	case s.prev.tex != nil:
		veil = 2 * (1 - t)
	default:
		veil = 1 - t
	}
	band := saverSlidesCaption(renderer, config, shown.title, shown.system, false)
	if veil > 0 && band.H > 0 {
		renderer.SetDrawColor(saverBackdropBase.R, saverBackdropBase.G, saverBackdropBase.B, uint8(255*math.Min(veil, 1)))
		renderer.FillRect(&band)
	}
	return true
}

// saverSlidesDrawOne draws one picture at alpha, drifting along the side it
// has room on.
func saverSlidesDrawOne(renderer *sdl.Renderer, sh *saverSlideShown, area sdl.Rect, tick uint64, alpha uint8) {
	if sh.tex == nil || alpha == 0 {
		return
	}
	freeX, freeY := max(area.W-sh.w, 0), max(area.H-sh.h, 0)
	p := float64(tick-sh.at) / float64(saverSlidesHoldMS+saverSlidesFadeMS)
	p = math.Max(0, math.Min(1, p))
	reach := saverScaled(40, 14)
	var dx, dy int32
	if freeX >= freeY {
		dx = int32(float64(min(freeX/2, reach)) * (2*p - 1) * float64(sh.dir))
	} else {
		dy = int32(float64(min(freeY/2, reach)) * (2*p - 1) * float64(sh.dir))
	}
	dst := sdl.Rect{X: area.X + freeX/2 + dx, Y: area.Y + freeY/2 + dy, W: sh.w, H: sh.h}
	_ = sh.tex.SetAlphaMod(alpha)
	_ = renderer.Copy(sh.tex, nil, &dst)
}

// saverSlidesCaption draws the title and console bottom-left, on glass when
// over a video, and returns the band it used.
func saverSlidesCaption(renderer *sdl.Renderer, config *Config, title, system string, glass bool) sdl.Rect {
	if config == nil || strings.TrimSpace(title) == "" {
		return sdl.Rect{}
	}
	titleFont := getFontSized(config, "small", int(saverScaled(26, 15)))
	subFont := saverChipFont(config)
	if titleFont == nil || subFont == nil {
		return sdl.Rect{}
	}
	mx := saverScaled(48, 16)
	room := screenWidth - 2*mx
	pad := int32(0)
	if glass {
		pad = saverScaled(14, 8)
		room -= 2 * pad
	}
	title = ellipsize(titleFont, title, room)
	// The console as Home names it (Settings → Console names, consolenames.go).
	system = ellipsize(subFont, consoleDisplayName(system), room)
	th, sh := int32(titleFont.Height()), int32(subFont.Height())
	gap := saverScaled(4, 2)
	h := th + gap + sh
	y := screenHeight - saverScaled(30, 12) - h - pad
	if glass {
		tw, _ := measureText(titleFont, title)
		sw, _ := measureText(subFont, system)
		w := max(tw, sw) + 2*pad
		g := saverGlassStyle()
		fillRoundedRect(renderer, mx, y-pad, w, h+2*pad, saverScaled(14, 8), g.fill)
		drawTextRaw(renderer, titleFont, title, mx+pad, y, g.ink, textAlignLeft)
		drawTextRaw(renderer, subFont, system, mx+pad, y+th+gap, g.muted, textAlignLeft)
		return sdl.Rect{X: mx, Y: y - pad, W: w, H: h + 2*pad}
	}
	drawTextRaw(renderer, titleFont, title, mx, y, saverInkClock, textAlignLeft)
	drawTextRaw(renderer, subFont, system, mx, y+th+gap, saverInkSoft, textAlignLeft)
	return sdl.Rect{X: 0, Y: y - gap, W: screenWidth, H: h + 2*gap}
}

// saverSlidesAsk starts decoding the next picture of the shuffled pool.
func saverSlidesAsk(tick uint64) {
	s := &saverSlides
	if len(s.games) == 0 {
		return
	}
	var g saverSlideGame
	for tries := 0; tries < 2; tries++ {
		if s.pos >= len(s.order) {
			s.order, s.pos = rand.Perm(len(s.games)), 0
		}
		g = s.games[s.order[s.pos]]
		s.pos++
		if g.path != s.cur.path || len(s.games) == 1 {
			break
		}
	}
	s.loading, s.askedAt = true, tick
	gen := s.gen
	area := saverSlidesArea()
	artRoot := libraryArtworkRoot()
	go func() {
		pic := saverSlideDecode(g, artRoot, int(area.W), int(area.H))
		saverSlideReady.mu.Lock()
		// An ended spell raised the slot's gen (saverSlidesStop): its late
		// picture is dropped here instead of waiting in memory for the next.
		if gen >= saverSlideReady.gen {
			saverSlideReady.pic, saverSlideReady.gen = pic, gen
		}
		saverSlideReady.mu.Unlock()
	}()
}

// saverSlidesTake uploads a finished picture as the next one.
func saverSlidesTake(renderer *sdl.Renderer) {
	saverSlideReady.mu.Lock()
	pic, gen := saverSlideReady.pic, saverSlideReady.gen
	if pic != nil && gen <= saverSlides.gen {
		saverSlideReady.pic = nil
	}
	saverSlideReady.mu.Unlock()
	s := &saverSlides
	if pic == nil || gen != s.gen {
		return // nothing yet, or an earlier spell's
	}
	s.loading = false
	if pic.err != nil || pic.pix == nil {
		s.fails++
		logDebug("idle", "slideshow: %s skipped: %v", filepath.Base(pic.game.art), pic.err)
		return
	}
	tex, err := saverPicTexture(renderer, pic.pix)
	if err != nil {
		s.fails++
		logDebug("idle", "slideshow: upload failed: %v", err)
		return
	}
	s.fails = 0
	s.dir = -s.dir
	if s.nxt.tex != nil {
		// A decode given up as stuck answered after its replacement did.
		s.nxt.tex.Destroy()
	}
	b := pic.pix.Bounds()
	s.nxt = saverSlideShown{tex: tex, w: int32(b.Dx()), h: int32(b.Dy()), title: pic.game.title,
		system: pic.game.system, path: pic.game.path, dir: s.dir}
}

// saverSlideDecode decodes one game's picture fitted into aw×ah (whole, not
// cropped). Off the render thread.
func saverSlideDecode(g saverSlideGame, artRoot string, aw, ah int) (out *saverSlideDecoded) {
	out = &saverSlideDecoded{game: g}
	defer func() {
		if r := recover(); r != nil {
			out.pix, out.err = nil, fmt.Errorf("decoding failed: %v", r)
		}
	}()
	path := g.art
	if rom, ok := gameCoverROM(libraryGame{Path: g.path, System: g.system}); ok && artRoot != "" {
		if shot := playShotCoverPath(rom, artRoot); shot != "" && shot != path && (path == "" || rand.Intn(2) == 0) {
			path = shot
		}
	}
	if path == "" || aw <= 0 || ah <= 0 {
		out.err = errors.New("no picture")
		return out
	}
	out.game.art = path
	src, orient, err := saverPicDecode(path)
	if err != nil {
		out.err = err
		return out
	}
	b := src.Bounds()
	_, dw, dh := saverOrientFor(orient, b.Dx(), b.Dy())
	if dw <= 0 || dh <= 0 {
		out.err = errors.New("the picture has no size")
		return out
	}
	scale := math.Min(float64(aw)*saverSlidesFit/float64(dw), float64(ah)*saverSlidesFit/float64(dh))
	fw, fh := max(int(float64(dw)*scale+0.5), 1), max(int(float64(dh)*scale+0.5), 1)
	out.pix = saverPicCover(src, orient, fw, fh, saverBackdropBase)
	return out
}

// ── The videos ────────────────────────────────────────────────────────────

// saverVid is the clip decoder. The decoder goroutine fills frame; the
// texture belongs to the render thread (snappreview.go's rule).
var saverVid struct {
	mu     sync.Mutex
	run    context.Context
	cancel context.CancelFunc
	frame  []byte
	w, h   int32
	seq    uint64
	got    bool // a frame arrived
	done   bool // the decoder returned
	broken map[string]bool

	tex      *sdl.Texture
	uploaded uint64
	upload   []byte
}

// saverVideoSize is the decode size: saverVideoWidth wide at the screen's
// shape, both even (YUV 4:2:0).
func saverVideoSize() (int32, int32) {
	w := int32(saverVideoWidth)
	h := int32(saverVideoWidth * 9 / 16)
	if screenWidth > 0 && screenHeight > 0 {
		h = w * screenHeight / screenWidth
	}
	h &^= 3
	return w, max(h, 16)
}

// saverVideoArgs is the ffmpeg command: snappreview.go's, played once, scaled
// to fit w×h without stretching and padded to it.
func saverVideoArgs(path string, w, h int32) []string {
	args := snapDecoderArgs(path, w, h, saverVideoFPS)
	if len(args) >= 2 && args[0] == "-stream_loop" {
		args = args[2:]
	}
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:flags=bilinear,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,fps=%d",
		w, h, w, h, saverVideoFPS)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-vf" {
			args[i+1] = vf
		}
	}
	return append([]string{"-nostdin", "-nostats", "-loglevel", "error"}, args...)
}

// saverVideoStart starts decoding a clip, ending any decoder first.
func saverVideoStart(path string, config *Config) {
	saverVideoHalt()
	w, h := saverVideoSize()
	ctx, cancel := context.WithCancel(context.Background())
	saverVid.mu.Lock()
	saverVid.run, saverVid.cancel = ctx, cancel
	saverVid.got, saverVid.done = false, false
	saverVid.mu.Unlock()
	go saverVideoRun(ctx, path, w, h, config)
}

// saverVideoHalt stops the decoder; any goroutine may call it.
func saverVideoHalt() {
	saverVid.mu.Lock()
	cancel := saverVid.cancel
	saverVid.cancel, saverVid.run = nil, nil
	saverVid.frame = nil
	saverVid.got, saverVid.done = false, false
	saverVid.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func init() {
	// A game started while the idle screen is up (from the phone, a Party
	// Mode room) gets the CPU to itself: the clip's decoder stops, and the
	// next idle frame, if any, starts another.
	gameLaunchRegisterStarted(func(libraryGame, string, *exec.Cmd) { saverVideoHalt() })
}

// saverVideoStop is saverVideoHalt plus the texture. Render thread.
func saverVideoStop() {
	saverVideoHalt()
	saverVid.mu.Lock()
	tex := saverVid.tex
	saverVid.tex, saverVid.uploaded, saverVid.upload = nil, 0, nil
	saverVid.mu.Unlock()
	if tex != nil {
		tex.Destroy()
	}
}

// saverVideoBroken reports (and with mark, records) a clip that gave no
// frame this run.
func saverVideoBroken(path string, mark bool) bool {
	saverVid.mu.Lock()
	defer saverVid.mu.Unlock()
	if mark {
		if saverVid.broken == nil {
			saverVid.broken = map[string]bool{}
		}
		saverVid.broken[path] = true
	}
	return saverVid.broken[path]
}

// saverVideoRun decodes one clip until it ends or is cancelled.
func saverVideoRun(ctx context.Context, path string, w, h int32, config *Config) {
	got := false
	defer func() {
		saverVid.mu.Lock()
		if saverVid.run == ctx {
			saverVid.done = true
		}
		saverVid.mu.Unlock()
	}()
	ffmpeg := snapFFmpegPath(config)
	if ffmpeg == "" {
		logDebug("idle", "game videos: no ffmpeg, showing the slideshow")
		saverVideoBroken(path, true)
		return
	}
	cmd := hiddenCommandContext(ctx, ffmpeg, saverVideoArgs(path, w, h)...)
	// No console window over the idle screen on Windows (sched_windows.go).
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		logWarn("idle", "game videos: %s: ffmpeg did not start: %v", filepath.Base(path), err)
		saverVideoBroken(path, true)
		return
	}
	frameSize := int(w * h * 3 / 2)
	buf := make([]byte, frameSize)
	for {
		if _, err := io.ReadFull(pipe, buf); err != nil {
			break
		}
		got = true
		saverVid.mu.Lock()
		if saverVid.run != ctx {
			saverVid.mu.Unlock()
			break
		}
		if len(saverVid.frame) != frameSize {
			saverVid.frame = make([]byte, frameSize)
		}
		buf, saverVid.frame = saverVid.frame, buf
		saverVid.w, saverVid.h = w, h
		saverVid.seq++
		saverVid.got = true
		saverVid.mu.Unlock()
	}
	_ = cmd.Wait()
	if !got && ctx.Err() == nil {
		saverVideoBroken(path, true)
		reason := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(reason, '\n'); i >= 0 {
			reason = reason[:i]
		}
		logDebug("idle", "game videos: %s gave no picture (%s); skipped this run", filepath.Base(path), reason)
	}
}

// saverVideoTexture uploads the newest frame (render thread), nil while
// there is none.
func saverVideoTexture(renderer *sdl.Renderer) *sdl.Texture {
	saverVid.mu.Lock()
	if !saverVid.got || saverVid.frame == nil || saverVid.w <= 0 || saverVid.h <= 0 ||
		len(saverVid.frame) != int(saverVid.w*saverVid.h*3/2) {
		saverVid.mu.Unlock()
		return nil
	}
	w, h, seq, tex := saverVid.w, saverVid.h, saverVid.seq, saverVid.tex
	if tex != nil && seq == saverVid.uploaded {
		saverVid.mu.Unlock()
		return tex
	}
	// Copied under the lock: the decoder's next swap hands this buffer back
	// to ffmpeg.
	saverVid.upload = append(saverVid.upload[:0], saverVid.frame...)
	frame := saverVid.upload
	saverVid.mu.Unlock()
	if tex != nil {
		if _, _, tw, th, err := tex.Query(); err != nil || tw != w || th != h {
			tex.Destroy()
			tex = nil
		}
	}
	if tex == nil {
		var err error
		tex, err = renderer.CreateTexture(uint32(sdl.PIXELFORMAT_IYUV), sdl.TEXTUREACCESS_STREAMING, w, h)
		if err != nil {
			logWarn("idle", "game videos: texture: %v", err)
			saverVid.mu.Lock()
			saverVid.tex = nil
			saverVid.mu.Unlock()
			return nil
		}
	}
	ySize, cSize := int(w*h), int(w*h)/4
	if err := tex.UpdateYUV(nil, frame[:ySize], int(w), frame[ySize:ySize+cSize], int(w)/2, frame[ySize+cSize:], int(w)/2); err != nil {
		logWarn("idle", "game videos: upload: %v", err)
		tex.Destroy()
		saverVid.mu.Lock()
		saverVid.tex, saverVid.uploaded = nil, 0
		saverVid.mu.Unlock()
		return nil
	}
	saverVid.mu.Lock()
	saverVid.tex, saverVid.uploaded = tex, seq
	saverVid.mu.Unlock()
	return tex
}

// saverVideoNext starts the next clip of the shuffled pool that is not
// known to be broken; false when none is left.
func saverVideoNext(config *Config, tick uint64) bool {
	s := &saverSlides
	for tries := 0; tries <= len(s.clips); tries++ {
		if len(s.clips) == 0 {
			return false
		}
		if s.clipPos >= len(s.clipOrder) {
			s.clipOrder, s.clipPos = rand.Perm(len(s.clips)), 0
		}
		c := s.clips[s.clipOrder[s.clipPos]]
		s.clipPos++
		if saverVideoBroken(c.path, false) || (c.path == s.clipNow.path && len(s.clips) > 1) {
			continue
		}
		s.clipNow, s.clipAt, s.clipSeen = c, tick, 0
		saverVideoStart(c.path, config)
		return true
	}
	return false
}

// saverVideoDraw draws the playing clip, starting the next one when it ends;
// false (the slideshow shows) once no clip will play.
func saverVideoDraw(renderer *sdl.Renderer, config *Config, tick uint64) bool {
	s := &saverSlides
	if len(s.clips) == 0 {
		return false
	}
	saverVid.mu.Lock()
	running, got, done := saverVid.run != nil, saverVid.got, saverVid.done
	saverVid.mu.Unlock()
	advance := false
	switch {
	case !running || done:
		// Not started yet, or the clip ended (or gave nothing).
		advance = true
	case !got && tick-s.clipAt >= saverVideoFirstMS:
		saverVideoBroken(s.clipNow.path, true)
		advance = true
	case tick-s.clipAt >= saverVideoMaxMS:
		advance = true
	}
	if advance && !saverVideoNext(config, tick) {
		// Nothing plays: the slideshow takes over for this idle spell.
		saverVideoStop()
		s.kind = saverKindSlides
		return false
	}
	tex := saverVideoTexture(renderer)
	if tex == nil {
		return true // the clip is starting: the dark base
	}
	if s.clipSeen == 0 {
		s.clipSeen = tick
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 {
		return true
	}
	scale := math.Min(float64(screenWidth)/float64(tw), float64(screenHeight)/float64(th))
	dw, dh := int32(float64(tw)*scale), int32(float64(th)*scale)
	_ = renderer.Copy(tex, nil, &sdl.Rect{X: (screenWidth - dw) / 2, Y: (screenHeight - dh) / 2, W: dw, H: dh})
	saverSlidesCaption(renderer, config, s.clipNow.title, s.clipNow.system, true)
	if el := tick - s.clipSeen; el < saverVideoFadeMS {
		renderer.SetDrawColor(saverBackdropBase.R, saverBackdropBase.G, saverBackdropBase.B, uint8(255-255*el/saverVideoFadeMS))
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	}
	return true
}
