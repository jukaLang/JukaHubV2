package main

// hometabs_backdrop.go — the Tabs Home's Backdrop view (a full-background
// main menu): the selected game fills the screen behind Home,
// dimmed, with its box art large and its name beside it, and a slim row of
// its neighbours along the bottom of the page to walk with Left/Right.
//
// The picture behind is the game's own screen where there is one — its
// screenshot from the art folder (<art>/.snap), else the newest save-state
// picture, what the game switcher shows (gswStates) — and its box art
// otherwise; an app or a console has none, and the backdrop stays Home's own.
// It is looked up and decoded off the render thread, one at a time, only once
// the selection has rested on a game for a moment (homeTabsBackdropSettleMS),
// so walking the row does not decode a picture per step; the old picture
// stays until the new one fades in over it. At most two textures exist (the
// one on screen and the one fading out), each at most gswPicW × gswPicH, and
// both are freed when the page on screen leaves the Backdrop view
// (homeTabsBackdropRelease). The Library view (hometabs_library.go) borrows
// the same picture for its banner.
//
// Like every view, the row is a list of rects (homeTabsLayout), so the D-pad,
// the pointer, the scroll and the Dim unselected art work as on the others;
// the hero above it is drawn from the focused item. The veil over the picture
// is the theme's background colour, so a light theme fades to light.

import (
	"context"
	"image"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// homeTabViewBackdrop is the Backdrop view's name (homeTabViews).
const homeTabViewBackdrop = "backdrop"

const (
	// homeTabsBackdropSettleMS is how long the selection rests on a game
	// before its picture is looked up.
	homeTabsBackdropSettleMS = 220
	// homeTabsBackdropFadeMS is the new picture's fade over the old one.
	homeTabsBackdropFadeMS = 260
	// homeTabsBackdropVeil is the veil's alpha over the picture (of 255).
	homeTabsBackdropVeil = 178
	// homeTabsBackdropRetry is how long a picture that would not decode is
	// left alone.
	homeTabsBackdropRetry = 2 * time.Minute
	// homeTabsBackdropKeep bounds the looked-up pictures kept by game.
	homeTabsBackdropKeep = 96
)

// homeTabsBackdrop is the picture behind the view.
var homeTabsBackdrop = struct {
	// Render thread only:
	want    string // the game path the selection is on ("" none)
	since   uint64 // when it came to it
	tex     *sdl.Texture
	texFile string
	texPath string       // the game tex is the picture of
	prev    *sdl.Texture // the one fading out
	owner   *sdl.Renderer
	fade    uiEnter
	// stamp is the recent games' newest launch (gswRecentsStamp) the
	// looked-up pictures belong to: a game played since may have a newer
	// state picture under the same file name.
	stamp time.Time

	// Shared with the workers:
	mu        sync.Mutex
	resolved  map[string]string // game path → picture file ("" none)
	resolving map[string]bool
	loading   string // the file being decoded
	ready     *image.NRGBA
	readyFile string
	failed    map[string]time.Time
}{
	resolved:  map[string]string{},
	resolving: map[string]bool{},
	failed:    map[string]time.Time{},
}

// homeTabsBackdropLayout lays n neighbours out as one slim row along the
// bottom of a bw×bh body; the room above is the hero's. k is the canvas
// scale. Pure.
func homeTabsBackdropLayout(n int, bh int32, k float64, gap int32, out []sdl.Rect) ([]sdl.Rect, bool, int32) {
	if n <= 0 {
		return out, true, 0
	}
	side := min32(max32(bh*24/100, 40), int32(math.Round(128*k)))
	y := bh - side
	for i := 0; i < n; i++ {
		out = append(out, sdl.Rect{X: int32(i) * (side + gap), Y: y, W: side, H: side})
	}
	last := out[len(out)-1]
	return out, true, last.X + last.W
}

// homeTabsBackdropGame is the game path an item names, "" for anything else.
func homeTabsBackdropGame(it homeTabItem) string {
	if !strings.HasPrefix(it.Ref, homeTabRefGame) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(it.Ref, homeTabRefGame))
}

// homeTabsBackdropPicture is the picture a game's backdrop shows: its
// screenshot from the art folder (<art>/.snap, artstyle.go), else its newest
// save-state picture, else its box art; "" for none. It reads the card, so it
// runs on a worker.
func homeTabsBackdropPicture(path, system string) string {
	if rom, ok := gameCoverROM(libraryGame{Path: path, System: system}); ok && system != "" {
		if snap := artStyleKindPath(rom, libraryArtworkRoot(), artTypeSnap); snap != "" && artworkCoverPresent(snap) {
			return snap
		}
	}
	if system == "" || gswHasStates(system) {
		if _, shot := gswStates(path); shot != "" {
			return shot
		}
	}
	return homeArtPreviewJob(recentGame{Path: path, System: system}).File
}

// homeTabsBackdropLookup starts looking up a game's picture.
func homeTabsBackdropLookup(path, system string) {
	b := &homeTabsBackdrop
	b.mu.Lock()
	if b.resolving[path] {
		b.mu.Unlock()
		return
	}
	b.resolving[path] = true
	b.mu.Unlock()
	go func() {
		file := ""
		// The answer is recorded even when the lookup panics, so the game is
		// never left "being looked up" for good.
		defer func() {
			if r := recover(); r != nil {
				logWarn("home", "backdrop picture lookup failed: %v", r)
			}
			b.mu.Lock()
			delete(b.resolving, path)
			if len(b.resolved) >= homeTabsBackdropKeep {
				clear(b.resolved)
			}
			b.resolved[path] = file
			b.mu.Unlock()
		}()
		file = homeTabsBackdropPicture(path, system)
	}()
}

// homeTabsBackdropDecode starts decoding a picture, unless one is already
// being decoded (the next frame asks again). It reports false for a picture
// that would not decode a moment ago: there is nothing to wait for.
func homeTabsBackdropDecode(file string) bool {
	b := &homeTabsBackdrop
	b.mu.Lock()
	if at, bad := b.failed[file]; bad && time.Since(at) < homeTabsBackdropRetry {
		b.mu.Unlock()
		return false
	}
	if b.loading != "" {
		b.mu.Unlock()
		return true
	}
	b.loading = file
	b.mu.Unlock()
	go func() {
		var pic *image.NRGBA
		var err error
		defer func() {
			if r := recover(); r != nil {
				logWarn("home", "backdrop picture decode failed: %v", r)
				pic = nil
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			b.loading = ""
			if err != nil || pic == nil {
				b.failed[file] = time.Now()
				return
			}
			b.ready, b.readyFile = pic, file
		}()
		pic, err = gswLoadPicture(context.Background(), file)
	}()
	return true
}

// homeTabsBackdropSync follows the selection: it notes the game it is on,
// looks its picture up once it has rested there, and uploads the picture when
// it is decoded. It reports whether something is still on its way (the
// caller keeps the frames coming). Render thread.
func homeTabsBackdropSync(renderer *sdl.Renderer, it homeTabItem, ok bool) bool {
	b := &homeTabsBackdrop
	if b.owner != nil && b.owner != renderer {
		// Textures die with their renderer: forget them, never destroy them.
		b.tex, b.prev, b.texFile, b.texPath, b.owner = nil, nil, "", "", nil
	}
	path := ""
	if ok {
		path = homeTabsBackdropGame(it)
	}
	if stamp := gswRecentsStamp(); !stamp.Equal(b.stamp) {
		// A game was started since the pictures were looked up: look again,
		// and decode the one on screen again too (RetroArch rewrites a state
		// picture in place), fading the new one over it.
		b.stamp = stamp
		b.texFile = ""
		b.mu.Lock()
		clear(b.resolved)
		b.mu.Unlock()
	}
	now := uiClockMS()
	if path != b.want {
		b.want, b.since = path, now
	}
	if path == "" || now-b.since < homeTabsBackdropSettleMS {
		return path != ""
	}
	b.mu.Lock()
	file, known := b.resolved[path]
	looking := b.resolving[path]
	var ready *image.NRGBA
	if b.ready != nil && known && (b.readyFile == file || b.loading == "") {
		// The picture wanted, or one the selection has left (dropped).
		if b.readyFile == file {
			ready = b.ready
		}
		b.ready, b.readyFile = nil, ""
	}
	b.mu.Unlock()
	if !known {
		if !looking {
			// The console: the library's, a recent game's or the folder's
			// (gamepin.go).
			g, _ := gamePinGameFor(path)
			homeTabsBackdropLookup(path, g.System)
		}
		return true
	}
	if file == "" {
		return false
	}
	if file == b.texFile {
		b.texPath = path // two games sharing a picture (a missing cover's stand-in)
		return false
	}
	if ready == nil {
		return homeTabsBackdropDecode(file)
	}
	if !allowThumbnailDecode(frameCount, thumbnailDecodeBudgetPerFrame()) {
		// Uploaded next frame: put it back.
		b.mu.Lock()
		if b.ready == nil {
			b.ready, b.readyFile = ready, file
		}
		b.mu.Unlock()
		return true
	}
	tex := imgvTexture(renderer, ready)
	if tex == nil {
		b.mu.Lock()
		b.failed[file] = time.Now()
		b.mu.Unlock()
		return false
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	if b.prev != nil {
		_ = b.prev.Destroy()
	}
	b.prev, b.tex, b.texFile, b.texPath, b.owner = b.tex, tex, file, path, renderer
	if homeLayout != nil && homeLayout.Motion && b.prev != nil {
		b.fade.begin(now, homeTabsBackdropFadeMS)
	} else {
		b.fade.cancel()
	}
	return b.fade.active
}

// homeTabsBackdropCover copies tex over the whole of dst, cropped to its
// shape, at alpha.
func homeTabsBackdropCover(renderer *sdl.Renderer, tex *sdl.Texture, dst sdl.Rect, alpha uint8) {
	if tex == nil || alpha == 0 {
		return
	}
	homeTabsCopyCoverDim(renderer, tex, dst, alpha, 0)
}

// homeTabsBackdropRelease frees the pictures once no page is in the Backdrop
// view (another view, another tab, another Home layout, Edit Home): the next
// visit settles and decodes again from the looked-up files. Cheap when there
// is nothing to free. Render thread.
func homeTabsBackdropRelease(renderer *sdl.Renderer) {
	b := &homeTabsBackdrop
	if b.tex == nil && b.prev == nil && b.want == "" && !b.fade.active {
		return
	}
	// A texture of an older renderer died with it: forgotten, not destroyed.
	if b.owner == renderer {
		if b.prev != nil {
			_ = b.prev.Destroy()
		}
		if b.tex != nil {
			_ = b.tex.Destroy()
		}
	}
	b.tex, b.prev, b.texFile, b.texPath, b.owner = nil, nil, "", "", nil
	b.want, b.since = "", 0
	b.fade.cancel()
	b.mu.Lock()
	b.ready, b.readyFile = nil, ""
	b.mu.Unlock()
}

// tabsBackdropBehind draws the selected game's picture over the whole screen
// under a veil, before the top bar (Render calls it after the app backdrop).
// Nothing unless the page on screen is in the Backdrop view — or the Library
// view, which loads the picture the same way for its banner and has the
// Library theme's room drawn here instead.
func (h *HomeLayout) tabsBackdropBehind(renderer *sdl.Renderer) {
	if h == nil || renderer == nil {
		return
	}
	lvl := homeTabsLevelNow()
	view := ""
	if h.tabsActive() && homeTabs.on && lvl != nil {
		view = homeTabsViewFor(appConfig, lvl.key)
	}
	// The Library view shows the same picture in its banner
	// (hometabs_library.go).
	if view != homeTabViewBackdrop && view != homeTabViewLibrary {
		// Another view, tab or Home layout: the pictures go.
		homeTabsBackdropRelease(renderer)
		return
	}
	it, ok := homeTabsFocusedItem()
	pending := homeTabsBackdropSync(renderer, it, ok)
	b := &homeTabsBackdrop
	// The fade runs on the clock whether or not a picture is drawn this
	// frame (progress ends it), so moving to an app mid-fade cannot keep it
	// "active" and the frame rate up for good.
	p := 1.0
	if b.fade.active {
		p = uiEaseOutCubic(b.fade.progress(uiClockMS()))
	}
	if pending || b.fade.active {
		homeTabsBackdropBusy = true
	}
	if view == homeTabViewLibrary {
		// The banner draws the picture; nothing fades out behind it. The
		// Library theme's room stands behind the page (themelibrary.go).
		if !b.fade.active && b.prev != nil {
			_ = b.prev.Destroy()
			b.prev = nil
		}
		h.libraryDrawStage(renderer, lvl)
		return
	}
	// The last picture stays while the next one is on its way; a game with
	// none (or an app) shows Home's own backdrop.
	if b.tex == nil || b.want == "" || (b.texPath != b.want && !pending) {
		return
	}
	screen := sdl.Rect{W: h.Width, H: h.Height}
	if b.prev != nil && p < 1 {
		homeTabsBackdropCover(renderer, b.prev, screen, 255)
	}
	if !b.fade.active && b.prev != nil {
		_ = b.prev.Destroy()
		b.prev = nil
	}
	homeTabsBackdropCover(renderer, b.tex, screen, uint8(math.Round(p*255)))
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, WithAlpha(ColorBackground, homeTabsBackdropVeil))
	_ = renderer.FillRect(&screen)
	// The row along the bottom stands on a deeper band.
	from, to := continueScrim(200)
	fillVerticalScrimUntil(renderer, screen, h.Height*55/100, h.Height, from, to)
}

// homeTabsBackdropBusy says the picture is still on its way: the page keeps
// the frame rate up (tabsDrawBackdrop hands it to homeTabs.anim, which
// tabsRender clears at its start, after this was set).
var homeTabsBackdropBusy bool

// homeTabsBackdropFit is the largest rect of tex's shape inside box, centred.
func homeTabsBackdropFit(tex *sdl.Texture, box sdl.Rect) sdl.Rect {
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 || box.W <= 0 || box.H <= 0 {
		return box
	}
	w, h := box.W, int32(int64(box.W)*int64(th)/int64(tw))
	if h > box.H {
		w, h = int32(int64(box.H)*int64(tw)/int64(th)), box.H
	}
	return sdl.Rect{X: box.X + (box.W-w)/2, Y: box.Y + (box.H-h)/2, W: max32(w, 1), H: max32(h, 1)}
}

// tabsDrawBackdrop draws the Backdrop view's page: the focused item large —
// its box art and its name — over the row of its neighbours.
func (h *HomeLayout) tabsDrawBackdrop(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, g *homeTabsGeom, body sdl.Rect,
	dx, scroll int32, clip sdl.Rect, focused, pressed bool, lift, depth float64, alpha uint8, live bool, dimOf func(int) float64) {
	if len(g.rects) == 0 {
		return
	}
	if live && homeTabsBackdropBusy {
		homeTabs.anim = true
	}
	homeTabsBackdropBusy = false
	h.tabsDrawBackdropHero(renderer, fonts, lvl, sdl.Rect{X: body.X + dx, Y: body.Y, W: body.W,
		H: max32(g.rects[0].Y-scaleY(18, h.Height), 1)}, alpha)

	place := func(i int) sdl.Rect {
		r := g.rects[i]
		r.X += body.X + dx - scroll
		r.Y += body.Y
		return r
	}
	drawOne := func(i int) {
		base := place(i)
		if !homeRectsOverlap(base, clip) {
			return
		}
		it := lvl.items[i]
		isFocus := focused && i == lvl.focus
		draw := base
		if isFocus {
			draw = scaleHomeCard(base, lift)
			if pressed {
				draw.Y += homePressOffset(depth, h.Height)
			}
		}
		ring := homeTabsTileRing(isFocus)
		if ring {
			drawHomeFocusGlow(renderer, draw)
		}
		if alpha == 255 {
			drawGlassTile(renderer, base, draw, ring, isFocus && pressed)
		} else {
			fillRoundedRectAt(renderer, base, draw, homeTileRadius, homeTabsInk(homeCardFill(), alpha))
		}
		var art *sdl.Texture
		if it.Art != nil {
			art = it.Art()
		}
		if art != nil {
			homeTabsCopyCoverDim(renderer, art, draw, alpha, dimOf(i))
		} else {
			ink := ColorTextPrimary()
			if it.Dim {
				ink = ColorTextTertiary()
			}
			glyph := it.Glyph
			if glyph == "" {
				glyph = "apps"
			}
			gs := draw.W * 46 / 100
			drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: draw.X + (draw.W-gs)/2, Y: draw.Y + (draw.H-gs)/2, W: gs, H: gs}, homeTabsInk(ink, alpha))
		}
		if live {
			homeTabs.hits = append(homeTabs.hits, homeTabsHit{i: i, r: glassIntersect(base, clip)})
		}
		_ = renderer.SetClipRect(&clip)
	}
	for i := range lvl.items {
		if i < len(g.rects) && !(focused && i == lvl.focus) {
			drawOne(i)
		}
	}
	if focused && lvl.focus >= 0 && lvl.focus < len(lvl.items) && lvl.focus < len(g.rects) {
		drawOne(lvl.focus)
	}
}

// tabsDrawBackdropHero draws the focused item in area: its box art (or its
// glyph on glass) on the left, its name and line beside it.
func (h *HomeLayout) tabsDrawBackdropHero(renderer *sdl.Renderer, fonts HomeFonts, lvl *homeTabsLevel, area sdl.Rect, alpha uint8) {
	if lvl.focus < 0 || lvl.focus >= len(lvl.items) || area.H < scaleY(60, h.Height) {
		return
	}
	it := lvl.items[lvl.focus]
	side := min32(area.H, area.W*36/100)
	box := sdl.Rect{X: area.X, Y: area.Y + (area.H-side)/2, W: side, H: side}
	var art *sdl.Texture
	if it.Art != nil {
		art = it.Art()
	}
	if art != nil {
		dst := homeTabsBackdropFit(art, box)
		shadow := sdl.Rect{X: dst.X + scaleX(6, h.Width), Y: dst.Y + scaleY(10, h.Height), W: dst.W, H: dst.H}
		fillRoundedRect(renderer, shadow.X, shadow.Y, shadow.W, shadow.H, homeTileRadius, homeTabsInk(sdl.Color{A: 90}, alpha))
		_ = art.SetAlphaMod(alpha)
		_ = renderer.Copy(art, nil, &dst)
		_ = art.SetAlphaMod(255)
		box = dst
	} else {
		fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, homeTileRadius, homeTabsInk(homeCardFill(), alpha))
		strokeRoundedRect(renderer, box.X, box.Y, box.W, box.H, homeTileRadius, 1, homeTabsInk(homeCardEdge(), alpha))
		glyph := it.Glyph
		if glyph == "" {
			glyph = "apps"
		}
		gs := side * 50 / 100
		drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: box.X + (side-gs)/2, Y: box.Y + (side-gs)/2, W: gs, H: gs}, homeTabsInk(ColorTextPrimary(), alpha))
	}
	big, line := fonts.Greeting, fonts.Helper
	if big == nil {
		big = fonts.Card
	}
	if big == nil {
		return
	}
	tx := box.X + box.W + scaleX(32, h.Width)
	room := area.X + area.W - tx
	if room < scaleX(120, h.Width) {
		return
	}
	bh := int32(big.Height())
	lh := int32(0)
	if line != nil && it.Sub != "" {
		lh = int32(line.Height()) + scaleY(6, h.Height)
	}
	y := area.Y + (area.H-bh-lh)/2
	titleInk := ColorTextPrimary()
	if it.Dim {
		titleInk = ColorTextMuted()
	}
	drawText(renderer, big, ellipsize(big, it.Title, room), tx, y, homeTabsInk(titleInk, alpha), textAlignLeft)
	if lh > 0 {
		drawText(renderer, line, ellipsize(line, it.Sub, room), tx, y+bh+scaleY(6, h.Height), homeTabsInk(ColorTextMuted(), alpha), textAlignLeft)
	}
}
