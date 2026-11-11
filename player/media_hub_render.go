package main

// media_hub_render.go — drawing the Media screen in Home's glass.
//
// Everything a frame draws was measured or built before it: card strings come
// from the list build (media_hub.go), widths from the shared text cache, and
// artwork only ever from textures already decoded or bytes already in memory —
// cachedThumbnailTexture never reads the disk or the network on this thread.
// Cold artwork is warmed on a worker (mediaWarmThumb) and appears a frame later.

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// mediaLayout is the screen's geometry for one frame.
type mediaLayout struct {
	search  sdl.Rect
	status  sdl.Rect
	tabs    sdl.Rect
	content sdl.Rect
}

// mediaHubComputeLayout lays the screen out on the current canvas: search row,
// tab strip, then the content down to the footer band.
func mediaHubComputeLayout(statusW int32) mediaLayout {
	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + 10
	bottom := sceneFooterBand().Y - 10
	full := screenWidth - 2*margin
	searchH := int32(46)
	l := mediaLayout{}
	l.search = sdl.Rect{X: margin, Y: top, W: full, H: searchH}
	if statusW > 0 {
		l.search.W = full - statusW - 12
		l.status = sdl.Rect{X: margin + l.search.W + 12, Y: top, W: statusW, H: searchH}
	}
	l.tabs = sdl.Rect{X: margin, Y: top + searchH + 12, W: full, H: 38}
	contentY := l.tabs.Y + l.tabs.H + 14
	l.content = sdl.Rect{X: margin, Y: contentY, W: full, H: bottom - contentY}
	if l.content.H < 80 {
		l.content.H = 80
	}
	return l
}

// mediaHubGridColumns is how many cards one grid row holds on this canvas.
func mediaHubGridColumns(vertical bool) int {
	w := screenWidth - 2*scaleX(homeSideMargin, screenWidth)
	if vertical {
		if w >= 1100 {
			return 6
		}
		return 5
	}
	if w >= 1100 {
		return 4
	}
	return 3
}

const (
	mediaCardGap   = int32(16)
	mediaCardPad   = int32(6)
	mediaCardRound = int32(14)
)

// mediaCardHeights are the text rows under a card's artwork.
func mediaCardTextH(titleFont, metaFont *ttf.Font) int32 {
	h := int32(8)
	if titleFont != nil {
		h += int32(titleFont.Height())
	}
	if metaFont != nil {
		h += 2 + int32(metaFont.Height())
	}
	return h + 10
}

// mediaRowCardW is a For you card's width: a little over four per row, so the
// next card peeks in and says the row scrolls.
func mediaRowCardW() int32 {
	return scaleX(246, screenWidth)
}

// mediaRowPitch is the distance between two cards (or chips) of a row.
func mediaRowPitch(row mediaRow) int32 {
	if row.kind == mediaRowRecentSearch {
		return 0
	}
	return mediaRowCardW() + mediaCardGap
}

// mediaHitClip is the visible part of the strip or list being drawn: a card
// scrolled under the search bar or past the tab strip's edge must not take
// their clicks. The zero rect means no clipping.
var mediaHitClip sdl.Rect

// mediaAddHit records a click target cut to mediaHitClip; one scrolled wholly
// out of view is not recorded at all.
func mediaAddHit(h mediaHit) {
	if mediaHitClip.W > 0 && mediaHitClip.H > 0 {
		vis, ok := h.rect.Intersect(&mediaHitClip)
		if !ok {
			return
		}
		h.rect = vis
	}
	mediaHub.hits = append(mediaHub.hits, h)
}

// ── Artwork ──────────────────────────────────────────────────────────────────

var (
	// mediaThumbAsked remembers when each artwork key was last warmed, so a
	// card that has no picture yet does not queue it on every frame. Only the
	// render thread touches it.
	mediaThumbAsked = map[string]uint64{}
	// mediaThumbQueue feeds the worker that loads posters from the card and,
	// offline, artwork from the disk cache — never from the network.
	mediaThumbQueue      = make(chan string, 48)
	mediaThumbWorkerOnce sync.Once
)

// mediaWarmThumb gets an artwork key's bytes into memory in the background:
// through the shared prefetch workers online, through mediaThumbWorker for a
// poster file or while offline.
func mediaWarmThumb(key string) {
	if key == "" {
		return
	}
	now := sdl.GetTicks64()
	if at, ok := mediaThumbAsked[key]; ok && now-at < 4000 {
		return
	}
	if len(mediaThumbAsked) > 512 {
		mediaThumbAsked = map[string]uint64{}
	}
	mediaThumbAsked[key] = now
	if strings.HasPrefix(key, "file:") || !IsOnline() {
		mediaThumbWorkerOnce.Do(func() { go mediaThumbWorker() })
		select {
		case mediaThumbQueue <- key:
		default:
		}
		return
	}
	PrefetchThumbnail(key)
}

// mediaThumbWorker loads posters saved beside the Downloads library and, with
// the network down, artwork the disk cache already holds.
func mediaThumbWorker() {
	for key := range mediaThumbQueue {
		norm := normalizeThumbnailURL(key)
		if thumbnailDataGet(norm) != nil {
			continue
		}
		var data []byte
		if path, ok := strings.CutPrefix(key, "file:"); ok {
			if info, err := os.Stat(path); err == nil && info.Size() > 0 && info.Size() < 4<<20 {
				data, _ = os.ReadFile(path)
			}
		} else if db, err := openSharedCache("jukahub.cache"); err == nil {
			data, _ = cacheGet(db, thumbnailDiskPrefix+norm)
		}
		if len(data) > 0 {
			thumbnailDataSet(norm, data)
		}
	}
}

// mediaDrawCardArt paints a card's artwork into rect, or a drawn placeholder
// while none is decoded yet. A picture of another shape is cropped to its
// middle rather than squeezed (a Short's 16:9 frame in a 9:16 card, a film
// poster in a 16:9 one), except an album cover, which is shown whole.
func mediaDrawCardArt(renderer *sdl.Renderer, card *mediaCard, rect sdl.Rect) bool {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return false
	}
	for _, key := range card.art {
		if key == "" {
			continue
		}
		tex := cachedThumbnailTexture(renderer, key)
		if tex == nil {
			continue
		}
		_, _, tw, th, err := tex.Query()
		if err != nil || tw <= 0 || th <= 0 {
			renderer.Copy(tex, nil, &rect)
			return true
		}
		if card.artContain {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, sdl.Color{R: 0, G: 0, B: 0, A: 110})
			dst := mediaFitRect(rect, tw, th)
			renderer.Copy(tex, nil, &dst)
			return true
		}
		src := mediaCoverCrop(tw, th, rect.W, rect.H)
		renderer.Copy(tex, &src, &rect)
		return true
	}
	for _, key := range card.art {
		mediaWarmThumb(key)
	}
	// Placeholder: a quiet glass tile with the kind of media in its middle.
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, glassA(18))
	glyph := rect.H / 3
	if glyph > 40 {
		glyph = 40
	}
	if glyph >= 12 {
		icon := "video"
		switch {
		case card.glyph != "":
			icon = card.glyph
		case card.path != "" && isAudioFile(card.path):
			icon = "music"
		}
		DrawHomeIcon(renderer, icon, sdl.Rect{X: rect.X + (rect.W-glyph)/2, Y: rect.Y + (rect.H-glyph)/2, W: glyph, H: glyph}, false)
	}
	return false
}

// mediaCoverCrop is the middle of a tw×th picture with the shape of a w×h
// frame, so the picture fills the frame without being squeezed.
func mediaCoverCrop(tw, th, w, h int32) sdl.Rect {
	if tw <= 0 || th <= 0 || w <= 0 || h <= 0 {
		return sdl.Rect{W: tw, H: th}
	}
	if int64(tw)*int64(h) > int64(th)*int64(w) {
		cw := int32(int64(th) * int64(w) / int64(h))
		return sdl.Rect{X: (tw - cw) / 2, Y: 0, W: cw, H: th}
	}
	ch := int32(int64(tw) * int64(h) / int64(w))
	return sdl.Rect{X: 0, Y: (th - ch) / 2, W: tw, H: ch}
}

// mediaDrawDurationBadge paints the duration pill in a picture's corner.
func mediaDrawDurationBadge(renderer *sdl.Renderer, font *ttf.Font, text string, art sdl.Rect) {
	if font == nil || text == "" {
		return
	}
	tw, th := measureText(font, text)
	w := tw + 12
	h := th + 4
	x := art.X + art.W - w - 6
	y := art.Y + art.H - h - 6
	fillRoundedRect(renderer, x, y, w, h, h/2, sdl.Color{R: 0, G: 0, B: 0, A: 170})
	drawText(renderer, font, text, x+6, y+2, sdl.Color{R: 250, G: 250, B: 250, A: 255}, textAlignLeft)
}

// mediaDrawProgress paints the watched-progress line along a picture's foot.
func mediaDrawProgress(renderer *sdl.Renderer, art sdl.Rect, fraction float64) {
	if fraction <= 0 {
		return
	}
	if fraction > 1 {
		fraction = 1
	}
	h := int32(4)
	y := art.Y + art.H - h
	renderer.SetDrawColor(0, 0, 0, 150)
	renderer.FillRect(&sdl.Rect{X: art.X, Y: y, W: art.W, H: h})
	accent := glassAccent()
	renderer.SetDrawColor(accent.R, accent.G, accent.B, 255)
	renderer.FillRect(&sdl.Rect{X: art.X, Y: y, W: int32(float64(art.W) * fraction), H: h})
}

// mediaDrawDownloadedBadge marks a card that plays offline: a round accent
// badge with the download glyph in the picture's top-left corner.
func mediaDrawDownloadedBadge(renderer *sdl.Renderer, art sdl.Rect, warn bool) {
	size := int32(26)
	if art.H < 80 {
		size = 20
	}
	x, y := art.X+6, art.Y+6
	fill := WithAlpha(glassAccent(), 230)
	if warn {
		fill = WithAlpha(ColorWarning, 235)
	}
	fillCircle(renderer, x+size/2, y+size/2, size/2, fill)
	glyph := size - 10
	DrawHomeIcon(renderer, "download", sdl.Rect{X: x + 5, Y: y + 5, W: glyph, H: glyph}, true)
}

// ── Cards ────────────────────────────────────────────────────────────────────

// mediaDrawCard paints one thumbnail card: glass surface, artwork with its
// badges, title and one muted line. A focused card lifts with Home's ring and
// bloom, and shows its "…" details button.
func mediaDrawCard(renderer *sdl.Renderer, card *mediaCard, rect sdl.Rect, focused bool, titleFont, metaFont, badgeFont *ttf.Font, row int, index int) {
	if focused {
		drawGlassFocusGlow(renderer, rect)
	}
	drawGlassSurface(renderer, rect, mediaCardRound, glassState{Focused: focused})
	r := rect
	if focused {
		r = glassLifted(rect)
	}
	artH := (r.W - 2*mediaCardPad) * 9 / 16
	if card.vertical {
		artH = r.H - mediaCardTextH(titleFont, metaFont) - mediaCardPad
	}
	art := sdl.Rect{X: r.X + mediaCardPad, Y: r.Y + mediaCardPad, W: r.W - 2*mediaCardPad, H: artH}
	mediaDrawCardArt(renderer, card, art)
	if card.duration != "" {
		mediaDrawDurationBadge(renderer, badgeFont, card.duration, art)
	}
	// Every Library card is a file on the card, so only the web lists badge
	// what plays offline.
	if (card.downloaded || card.path != "") && card.mlibKind == mlibCardNone {
		mediaDrawDownloadedBadge(renderer, art, card.overLimit)
	}
	if card.favorite {
		fillCircle(renderer, art.X+art.W-16, art.Y+16, 11, sdl.Color{R: 0, G: 0, B: 0, A: 150})
		DrawHomeIcon(renderer, "heart", sdl.Rect{X: art.X + art.W - 24, Y: art.Y + 8, W: 16, H: 16}, false)
	}
	if card.progress > 0 {
		mediaDrawProgress(renderer, art, card.progress)
	}
	textX := art.X + 2
	textW := art.W - 4
	y := art.Y + art.H + 8
	if titleFont != nil {
		drawText(renderer, titleFont, ellipsize(titleFont, card.title, textW), textX, y, ColorTextPrimary(), textAlignLeft)
		y += int32(titleFont.Height()) + 2
	}
	if metaFont != nil {
		line := card.meta
		if card.detail != "" && card.mlibKind == mlibCardNone && (card.path != "" || card.meta == "") {
			line = card.detail
		}
		drawText(renderer, metaFont, ellipsize(metaFont, line, textW), textX, y, ColorTextTertiary(), textAlignLeft)
	}
	mediaAddHit(mediaHit{kind: mediaHitCard, a: row, b: index, rect: rect})
	if focused {
		// The details button: a tap target with the same action as Y. It
		// sits under the favourite heart when the card has one.
		more := sdl.Rect{X: art.X + art.W - 34, Y: art.Y + 6, W: 28, H: 28}
		if card.favorite {
			more.Y = art.Y + 32
		}
		fillCircle(renderer, more.X+14, more.Y+14, 14, sdl.Color{R: 0, G: 0, B: 0, A: 160})
		dot := sdl.Color{R: 245, G: 245, B: 245, A: 255}
		for i := int32(-1); i <= 1; i++ {
			fillCircle(renderer, more.X+14+i*7, more.Y+14, 2, dot)
		}
		mediaAddHit(mediaHit{kind: mediaHitCard, a: row, b: -index - 1, rect: more})
		if card.favorite && card.path == "" && mediaHub.tab == mediaTabFavorites {
			// The bin beside it removes the favourite (X on the pad).
			bin := sdl.Rect{X: more.X - 34, Y: more.Y, W: 28, H: 28}
			fillCircle(renderer, bin.X+14, bin.Y+14, 14, sdl.Color{R: 0, G: 0, B: 0, A: 160})
			drawHomeGlyphTinted(renderer, "trash", sdl.Rect{X: bin.X + 6, Y: bin.Y + 6, W: 16, H: 16}, dot)
			mediaAddHit(mediaHit{kind: mediaHitRemove, a: row, b: index, rect: bin})
		}
	}
}

// ── The screen ───────────────────────────────────────────────────────────────

// mediaHubRender draws the Media scene: the player while a video is on screen,
// the browser otherwise. It also draws the shared header and footer.
func mediaHubRender(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || config == nil {
		return
	}
	mediaHub.hits = mediaHub.hits[:0]
	mediaHitClip = sdl.Rect{}
	mediaRefreshLibraryView()
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	if mediaHubPlayerOnScreen() {
		mediaHubRenderPlayer(renderer, config)
		renderStatusBar(renderer, config)
		renderFooter(renderer, config)
		return
	}
	mediaHubEnsureBuilt(config)

	online := IsOnline()
	statusW := int32(0)
	statusText := ""
	statusKind := 0
	backOnline := online && mediaHub.backOnlineAt != 0 && sdl.GetTicks64()-mediaHub.backOnlineAt < 4000
	statusFont := FontForRole(config, TypeLabel)
	switch {
	case !online && mediaHub.tab == mediaTabLibrary:
		statusText, statusKind = "Not connected — your library works offline", 1
	case !online:
		statusText, statusKind = "Not connected — showing your downloads", 1
	case backOnline:
		statusText = "Back online"
	case mediaHub.tab == mediaTabLibrary && mlibScanning.Load():
		// The scan runs on behind a library that is already listed; the
		// empty library shows it in place of the rows instead.
		if m := mlibCurrentModel(); m != nil && len(m.items) > 0 {
			_, found, _ := mlibStatus()
			statusText, statusKind = mlibScanLine(found), 2
		}
	}
	if statusText != "" && statusFont != nil {
		tw, _ := measureText(statusFont, statusText)
		statusW = tw + 58
		if limit := screenWidth / 2; statusW > limit {
			statusW = limit
		}
	}
	layout := mediaHubComputeLayout(statusW)

	mediaHubDrawSearch(renderer, config, layout.search, online)
	if statusW > 0 {
		mediaHubDrawStatusPill(renderer, statusFont, layout.status, statusText, statusKind)
	}
	mediaHubDrawTabs(renderer, config, layout.tabs, online)

	switch mediaHub.tab {
	case mediaTabForYou:
		mediaHubDrawRows(renderer, config, layout.content, online)
	case mediaTabDownloads:
		mediaHubDrawDownloads(renderer, config, layout.content)
	case mediaTabLibrary:
		mediaHubDrawLibrary(renderer, config, layout.content, online)
	default:
		mediaHubDrawGrid(renderer, config, layout.content, online)
	}
	if mediaHub.tab == mediaTabFavorites {
		mediaHubDrawFavUndo(renderer, config, layout.content)
	}

	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	mediaHubDrawSheet(renderer, config)
	mediaHubDrawConfirm(renderer, config)
}

// mediaHubRenderPlayer draws the embedded player in the content area at the
// video's shape (full screen is drawn by the main loop over everything).
func mediaHubRenderPlayer(renderer *sdl.Renderer, config *Config) {
	if videoFullscreen {
		return
	}
	if mediaHub.playerCanvas[0] != screenWidth || mediaHub.playerCanvas[1] != screenHeight || mediaHub.playerElem.Type == "" {
		margin := scaleX(homeSideMargin, screenWidth)
		top := headerBarHeight() + 8
		bottom := sceneFooterBand().Y - 8
		box := sdl.Rect{X: margin, Y: top, W: screenWidth - 2*margin, H: bottom - top}
		w := box.W
		h := w * 9 / 16
		if h > box.H {
			h = box.H
			w = h * 16 / 9
		}
		r := sdl.Rect{X: box.X + (box.W-w)/2, Y: box.Y + (box.H-h)/2, W: w, H: h}
		mediaHub.playerRect = r
		mediaHub.playerElem = Element{
			Type:   "video",
			X:      r.X,
			Y:      r.Y,
			Width:  StringOrInt(strconv.Itoa(int(r.W))),
			Height: StringOrInt(strconv.Itoa(int(r.H))),
		}
		mediaHub.playerCanvas = [2]int32{screenWidth, screenHeight}
	}
	r := mediaHub.playerRect
	// A dark well under the picture, so letterbox bars read as the player.
	fillRoundedRect(renderer, r.X-4, r.Y-4, r.W+8, r.H+8, 18, sdl.Color{R: 0, G: 0, B: 0, A: 200})
	RenderVideoOverlay(renderer, config, mediaHub.playerElem)
}

// mediaHubDrawSearch paints the glass search bar.
func mediaHubDrawSearch(renderer *sdl.Renderer, config *Config, rect sdl.Rect, online bool) {
	focused := mediaHub.zone == mediaZoneSearch && !mediaHub.sheet.open && !mediaHub.confirm.open
	editing := mediaHubSearchEditing(config)
	if focused || editing {
		drawGlassFocusGlow(renderer, rect)
	}
	radius := rect.H / 2
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, glassA(12))
	if focused || editing {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, glassFocusRingWidth, glassAccent())
	} else {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, glassA(30))
	}
	glyph := int32(22)
	DrawHomeIcon(renderer, "search", sdl.Rect{X: rect.X + 16, Y: rect.Y + (rect.H-glyph)/2, W: glyph, H: glyph}, false)
	font := FontForRole(config, TypeBody)
	if font == nil {
		return
	}
	textX := rect.X + 16 + glyph + 12
	textW := rect.X + rect.W - 20 - textX
	text := ""
	ink := ColorTextPrimary()
	switch {
	case editing:
		text = inputTextBuffer
		if uint32(sdl.GetTicks64()/500)%2 == 0 {
			text += "_"
		}
	case mediaHub.query != "" && mediaHub.tab == mediaTabSearch:
		text = mediaHub.query
	}
	if text == "" {
		ink = ColorTextTertiary()
		if online {
			text = "Search videos or paste a link"
		} else {
			text = "Search your downloads"
		}
	}
	_, th := measureText(font, text)
	drawText(renderer, font, ellipsize(font, text, textW), textX, rect.Y+(rect.H-th)/2, ink, textAlignLeft)
	if focused && !editing {
		hint := FontForRole(config, TypeCaption)
		if hint != nil {
			label := hintSentence("%s to type", "A", "Type")
			hw, hh := measureText(hint, label)
			if textW > hw+200 {
				drawText(renderer, hint, label, rect.X+rect.W-20-hw, rect.Y+(rect.H-hh)/2, ColorTextTertiary(), textAlignLeft)
			}
		}
	}
	mediaHub.hits = append(mediaHub.hits, mediaHit{kind: mediaHitSearch, rect: rect})
}

// mediaHubDrawStatusPill is the compact state beside the search bar: kind 0 is
// back online, 1 not connected, 2 the Library's scan.
func mediaHubDrawStatusPill(renderer *sdl.Renderer, font *ttf.Font, rect sdl.Rect, text string, kind int) {
	if font == nil || rect.W <= 0 {
		return
	}
	radius := rect.H / 2
	ink := SuccessInk()
	icon := "check"
	switch kind {
	case 1:
		ink = WarningInk()
		icon = "wifi-off"
	case 2:
		ink = glassAccent()
		icon = "scan"
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, WithAlpha(ink, 34))
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, WithAlpha(ink, 150))
	glyph := int32(20)
	DrawHomeIcon(renderer, icon, sdl.Rect{X: rect.X + 16, Y: rect.Y + (rect.H-glyph)/2, W: glyph, H: glyph}, false)
	textX := rect.X + 16 + glyph + 10
	_, th := measureText(font, text)
	drawText(renderer, font, ellipsize(font, text, rect.X+rect.W-14-textX), textX, rect.Y+(rect.H-th)/2, ink, textAlignLeft)
}

// mediaTabsScroll is the strip's horizontal offset when its chips do not fit.
var mediaTabsScroll float64

// mediaHubDrawTabs paints the chip strip. The selected tab is accent glass; the
// chip under the pad ring lifts with Home's ring. The strip scrolls sideways on
// a narrow canvas so the focused chip is always whole.
func mediaHubDrawTabs(renderer *sdl.Renderer, config *Config, rect sdl.Rect, online bool) {
	font := FontForRole(config, TypeLabel)
	if font == nil {
		return
	}
	tabs := mediaHubTabs()
	glyph := int32(18)
	gap := int32(10)
	// Measure the strip.
	total := int32(0)
	focusX, focusW := int32(0), int32(0)
	for i, t := range tabs {
		w := mediaTabChipWidth(font, t, glyph)
		if mediaTabShortcut(t) && i > 0 && !mediaTabShortcut(tabs[i-1]) {
			total += 12
		}
		if (mediaHub.zone == mediaZoneTabs && i == mediaHub.tabFocus) || (mediaHub.zone != mediaZoneTabs && t == mediaHub.tab) {
			focusX, focusW = total, w
		}
		total += w + gap
	}
	target := mediaTabsScroll
	if focusX-int32(target) < 0 {
		target = float64(focusX)
	}
	if focusX+focusW-int32(target) > rect.W {
		target = float64(focusX + focusW - rect.W)
	}
	if max := float64(total - gap - rect.W); target > max {
		target = max
	}
	if target < 0 {
		target = 0
	}
	mediaTabsScroll += (target - mediaTabsScroll) * 0.35
	if d := target - mediaTabsScroll; d < 1 && d > -1 {
		mediaTabsScroll = target
	}

	clip := sdl.Rect{X: rect.X - 8, Y: rect.Y - 8, W: rect.W + 16, H: rect.H + 16}
	renderer.SetClipRect(&clip)
	mediaHitClip = rect
	x := rect.X - int32(mediaTabsScroll)
	for i, t := range tabs {
		if mediaTabShortcut(t) && i > 0 && !mediaTabShortcut(tabs[i-1]) {
			setDrawColor(renderer, glassHairline())
			renderer.FillRect(&sdl.Rect{X: x + 1, Y: rect.Y + 8, W: 1, H: rect.H - 16})
			x += 12
		}
		w := mediaTabChipWidth(font, t, glyph)
		chip := sdl.Rect{X: x, Y: rect.Y, W: w, H: rect.H}
		selected := t == mediaHub.tab
		focused := mediaHub.zone == mediaZoneTabs && i == mediaHub.tabFocus && !mediaHub.sheet.open && !mediaHub.confirm.open
		mediaDrawTabChip(renderer, font, chip, t, selected, focused, online, glyph)
		mediaAddHit(mediaHit{kind: mediaHitTab, a: i, rect: chip})
		x += w + gap
	}
	mediaHitClip = sdl.Rect{}
	renderer.SetClipRect(nil)
}

// mediaTabChipWidth measures one chip: icon, label and the Downloads count.
func mediaTabChipWidth(font *ttf.Font, t mediaTab, glyph int32) int32 {
	tw, _ := measureText(font, mediaTabLabel(t))
	w := 14 + glyph + 8 + tw + 16
	if t == mediaTabDownloads && mediaHub.downloadsTag != "" {
		cw, _ := measureText(font, mediaHub.downloadsTag)
		w += cw + 14
	}
	return w
}

// mediaDrawTabChip paints one chip.
func mediaDrawTabChip(renderer *sdl.Renderer, font *ttf.Font, chip sdl.Rect, t mediaTab, selected, focused, online bool, glyph int32) {
	radius := chip.H / 2
	if focused {
		drawGlassFocusGlow(renderer, chip)
	}
	switch {
	case selected:
		fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, radius, WithAlpha(glassAccent(), 46))
		strokeRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, radius, 1, WithAlpha(glassAccent(), 200))
	default:
		fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, radius, glassChipFill())
		strokeRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, radius, 1, glassEdge())
	}
	if focused {
		lifted := glassLifted(chip)
		drawGlassFocusRing(renderer, lifted, radius+glassFocusLift)
	}
	DrawHomeIcon(renderer, mediaTabIcon(t), sdl.Rect{X: chip.X + 14, Y: chip.Y + (chip.H-glyph)/2, W: glyph, H: glyph}, false)
	ink := ColorTextSecondary()
	if selected || focused {
		ink = ColorTextPrimary()
	}
	if !online && mediaTabNeedsNetwork(t) {
		ink = ColorTextTertiary()
	}
	label := mediaTabLabel(t)
	tw, th := measureText(font, label)
	tx := chip.X + 14 + glyph + 8
	drawText(renderer, font, label, tx, chip.Y+(chip.H-th)/2, ink, textAlignLeft)
	if t == mediaTabDownloads && mediaHub.downloadsTag != "" {
		cw, ch := measureText(font, mediaHub.downloadsTag)
		bx := tx + tw + 8
		bh := ch + 2
		fillRoundedRect(renderer, bx, chip.Y+(chip.H-bh)/2, cw+12, bh, bh/2, glassA(30))
		drawText(renderer, font, mediaHub.downloadsTag, bx+6, chip.Y+(chip.H-ch)/2, ColorTextPrimary(), textAlignLeft)
	}
}

// ── States ───────────────────────────────────────────────────────────────────

// mediaHubDrawState paints a centred empty/offline/loading state in rect, with
// an optional button (a = action id, see mediaHubClickHit).
func mediaHubDrawState(renderer *sdl.Renderer, config *Config, rect sdl.Rect, glyph, title, body, button string, action int, loading bool) {
	if loading {
		renderSpinner(renderer, rect.X+rect.W/2, rect.Y+rect.H/2-30, 24, glassAccent())
		font := FontForRole(config, TypeBody)
		if font != nil {
			tw, _ := measureText(font, title)
			drawText(renderer, font, title, rect.X+(rect.W-tw)/2, rect.Y+rect.H/2+8, ColorTextSecondary(), textAlignLeft)
		}
		return
	}
	block := rect
	if button != "" {
		block.H -= 70
	}
	UIEmptyState(renderer, config, block, glyph, title, body)
	if button == "" {
		return
	}
	font := FontForRole(config, TypeBody)
	if font == nil {
		return
	}
	tw, _ := measureText(font, button)
	btn := sdl.Rect{W: tw + 48, H: 44}
	btn.X = rect.X + (rect.W-btn.W)/2
	btn.Y = block.Y + block.H + 6
	focused := mediaHub.zone == mediaZoneContent && !mediaHub.sheet.open && !mediaHub.confirm.open
	UIButton(renderer, nil, btn, button, UIButtonStyle{Variant: UIButtonPrimary, Font: font}, UIButtonState{Focused: focused})
	mediaHub.hits = append(mediaHub.hits, mediaHit{kind: mediaHitStateAction, a: action, rect: btn})
}

// mediaHubDrawOffline is the compact "no internet" state a network tab shows.
func mediaHubDrawOffline(renderer *sdl.Renderer, config *Config, rect sdl.Rect, what string) {
	mediaHubDrawState(renderer, config, rect, "@wifi-off", "Not connected",
		what+" needs the internet. Your downloads still play.", "Open Downloads", 0, false)
}

// ── For you ──────────────────────────────────────────────────────────────────

// mediaHubDrawRows paints For you: rows of cards that scroll sideways, the
// whole page scrolling to keep the focused row in view.
func mediaHubDrawRows(renderer *sdl.Renderer, config *Config, rect sdl.Rect, online bool) {
	headFont := FontForRole(config, TypeBody)
	titleFont := FontForRole(config, TypeLabel)
	metaFont := FontForRole(config, TypeCaption)
	badgeFont := FontForRole(config, TypeCaption)
	chipFont := FontForRole(config, TypeLabel)
	if headFont == nil || titleFont == nil || metaFont == nil {
		return
	}
	if len(mediaHub.rows) == 0 || !mediaHubRowsHaveContent(online) {
		if !online {
			mediaHubDrawOffline(renderer, config, rect, "For you")
			return
		}
		mediaHubDrawState(renderer, config, rect, "", "Loading Media…", "", "", -1, true)
		return
	}
	cardW := mediaRowCardW()
	artH := (cardW - 2*mediaCardPad) * 9 / 16
	cardH := mediaCardPad + artH + mediaCardTextH(titleFont, metaFont)
	headH := int32(headFont.Height()) + 10
	chipH := int32(38)
	rowGap := int32(20)
	// Row offsets: computed per frame from the fixed heights, no allocation.
	focusTop, focusBottom := int32(0), int32(0)
	total := int32(0)
	for i, row := range mediaHub.rows {
		h := headH + cardH
		if row.kind == mediaRowRecentSearch {
			h = headH + chipH
		}
		if i == mediaHub.row {
			focusTop, focusBottom = total, total+h
		}
		total += h + rowGap
	}
	target := mediaHub.scrollY
	if float64(focusTop) < target {
		target = float64(focusTop)
	}
	if float64(focusBottom) > target+float64(rect.H) {
		target = float64(focusBottom - rect.H)
	}
	if max := float64(total - rowGap - rect.H); target > max {
		target = max
	}
	if target < 0 {
		target = 0
	}
	mediaHub.scrollY = mediaEase(mediaHub.scrollY, target)

	clip := sdl.Rect{X: rect.X - 10, Y: rect.Y - 6, W: rect.W + 20, H: rect.H + 12}
	renderer.SetClipRect(&clip)
	mediaHitClip = rect
	y := rect.Y - int32(mediaHub.scrollY)
	for i := range mediaHub.rows {
		row := &mediaHub.rows[i]
		h := headH + cardH
		if row.kind == mediaRowRecentSearch {
			h = headH + chipH
		}
		if y+h >= rect.Y-10 && y <= rect.Y+rect.H+10 {
			activeRow := mediaHub.zone == mediaZoneContent && i == mediaHub.row && !mediaHub.sheet.open && !mediaHub.confirm.open
			ink := ColorTextSecondary()
			if activeRow {
				ink = ColorTextPrimary()
			}
			drawText(renderer, headFont, row.title, rect.X+2, y, ink, textAlignLeft)
			if row.kind == mediaRowRecentSearch {
				mediaHubDrawChipRow(renderer, chipFont, row, i, sdl.Rect{X: rect.X, Y: y + headH, W: rect.W, H: chipH}, activeRow)
			} else if len(row.cards) == 0 {
				msg := "Nothing here yet"
				if row.kind == mediaRowTrending {
					msg = "Loading trending videos…"
					if mediaSearchIdle(mediaTrendingVar) && !mediaTrendingLoadedAt.IsZero() {
						msg = "Trending did not load — open the Trending tab to try again"
					}
				}
				drawText(renderer, metaFont, msg, rect.X+2, y+headH+cardH/2-10, ColorTextTertiary(), textAlignLeft)
			} else {
				mediaHubDrawCardRow(renderer, row, i, sdl.Rect{X: rect.X, Y: y + headH, W: rect.W, H: cardH},
					cardW, activeRow, titleFont, metaFont, badgeFont)
			}
		}
		y += h + rowGap
	}
	mediaHitClip = sdl.Rect{}
	renderer.SetClipRect(nil)
}

// mediaHubRowsHaveContent reports whether For you has anything to show yet.
func mediaHubRowsHaveContent(online bool) bool {
	for _, row := range mediaHub.rows {
		if len(row.cards) > 0 || len(row.chips) > 0 {
			return true
		}
	}
	// Online, the Trending row is there (loading) even while empty.
	return online && len(mediaHub.rows) > 0
}

// mediaSearchIdle reports whether no fetch of a results variable is running.
func mediaSearchIdle(resultVar string) bool {
	mediaSearchMu.Lock()
	_, running := mediaSearchCancel[resultVar]
	mediaSearchMu.Unlock()
	return !running
}

// mediaEase moves value toward target the way every list here scrolls: a
// third of the way per frame, snapping when it is within a pixel.
func mediaEase(value, target float64) float64 {
	if effectiveReducedMotion(appConfig) {
		return target
	}
	value += (target - value) * 0.3
	if d := target - value; d < 1 && d > -1 {
		return target
	}
	return value
}

// mediaHubDrawCardRow paints one row of cards with its own sideways scroll.
func mediaHubDrawCardRow(renderer *sdl.Renderer, row *mediaRow, rowIndex int, rect sdl.Rect, cardW int32, active bool, titleFont, metaFont, badgeFont *ttf.Font) {
	pitch := cardW + mediaCardGap
	focusX := int32(row.focus) * pitch
	target := row.scroll
	// Keep one card of context on the left once the row has moved.
	left := focusX - pitch/2
	if row.focus == 0 {
		left = 0
	}
	if float64(left) < target {
		target = float64(left)
	}
	if right := focusX + cardW + pitch/2; float64(right) > target+float64(rect.W) {
		target = float64(right - rect.W)
	}
	if max := float64(int32(len(row.cards))*pitch - mediaCardGap - rect.W); target > max {
		target = max
	}
	if target < 0 {
		target = 0
	}
	row.scroll = mediaEase(row.scroll, target)
	x := rect.X - int32(row.scroll)
	for j := range row.cards {
		cx := x + int32(j)*pitch
		if cx+cardW < rect.X-pitch || cx > rect.X+rect.W+pitch {
			continue
		}
		focused := active && j == row.focus
		mediaDrawCard(renderer, &row.cards[j], sdl.Rect{X: cx, Y: rect.Y, W: cardW, H: rect.H}, focused, titleFont, metaFont, badgeFont, rowIndex, j)
	}
}

// mediaHubDrawChipRow paints the recent-search chips.
func mediaHubDrawChipRow(renderer *sdl.Renderer, font *ttf.Font, row *mediaRow, rowIndex int, rect sdl.Rect, active bool) {
	if font == nil {
		return
	}
	x := rect.X
	glyph := int32(16)
	for j, q := range row.chips {
		label := ellipsize(font, q, 260)
		tw, th := measureText(font, label)
		chip := sdl.Rect{X: x, Y: rect.Y, W: 14 + glyph + 8 + tw + 16, H: rect.H}
		if chip.X+chip.W > rect.X+rect.W {
			break
		}
		focused := active && j == row.focus
		if focused {
			drawGlassFocusGlow(renderer, chip)
		}
		drawGlassSurface(renderer, chip, chip.H/2, glassState{Focused: focused})
		r := chip
		if focused {
			r = glassLifted(chip)
		}
		DrawHomeIcon(renderer, "search", sdl.Rect{X: r.X + 14, Y: r.Y + (r.H-glyph)/2, W: glyph, H: glyph}, false)
		drawText(renderer, font, label, r.X+14+glyph+8, r.Y+(r.H-th)/2, ColorTextPrimary(), textAlignLeft)
		mediaAddHit(mediaHit{kind: mediaHitRecent, a: rowIndex, b: j, rect: chip})
		x += chip.W + 10
	}
}

// ── Grids ────────────────────────────────────────────────────────────────────

// mediaHubDrawGrid paints a grid tab with its loading, offline, error and
// empty states.
func mediaHubDrawGrid(renderer *sdl.Renderer, config *Config, rect sdl.Rect, online bool) {
	titleFont := FontForRole(config, TypeLabel)
	metaFont := FontForRole(config, TypeCaption)
	badgeFont := FontForRole(config, TypeCaption)
	headFont := FontForRole(config, TypeLabel)
	if titleFont == nil || metaFont == nil {
		return
	}
	t := mediaHub.tab
	if len(mediaHub.cards) == 0 {
		mediaHubDrawGridEmpty(renderer, config, rect, online)
		return
	}
	if t == mediaTabSearch && headFont != nil {
		// What the grid is showing, above it.
		label := "Results"
		if mediaHub.localSearch {
			label = "In your downloads"
		}
		lw, _ := measureText(headFont, label)
		drawText(renderer, headFont, label, rect.X+2, rect.Y, ColorTextTertiary(), textAlignLeft)
		if mediaHub.query != "" {
			drawText(renderer, headFont, ellipsize(headFont, "“"+mediaHub.query+"”", rect.W-lw-20), rect.X+lw+10, rect.Y, ColorTextSecondary(), textAlignLeft)
		}
		off := int32(headFont.Height()) + 8
		rect.Y += off
		rect.H -= off
	}
	vertical := t == mediaTabShorts
	cols := mediaHubGridColumns(vertical)
	cardW := (rect.W - int32(cols-1)*mediaCardGap) / int32(cols)
	textH := mediaCardTextH(titleFont, metaFont)
	cardH := mediaCardPad + (cardW-2*mediaCardPad)*9/16 + textH
	if vertical {
		artH := (cardW - 2*mediaCardPad) * 16 / 9
		if limit := rect.H - textH - mediaCardPad - 8; artH > limit {
			artH = limit
		}
		cardH = mediaCardPad + artH + textH
	}
	pitch := cardH + mediaCardGap
	rowsN := (len(mediaHub.cards) + cols - 1) / cols
	focusRow := int32(mediaHub.index / cols)
	target := mediaHub.scrollY
	if top := float64(focusRow * pitch); top < target {
		target = top
	}
	if bottom := float64(focusRow*pitch + cardH); bottom > target+float64(rect.H) {
		target = bottom - float64(rect.H)
	}
	contentH := int32(rowsN)*pitch - mediaCardGap
	loading := mediaPageLoading(mediaHubTabVar(t))
	if loading {
		contentH += 40
	}
	if max := float64(contentH - rect.H); target > max {
		target = max
	}
	if target < 0 {
		target = 0
	}
	mediaHub.scrollY = mediaEase(mediaHub.scrollY, target)

	clip := sdl.Rect{X: rect.X - 10, Y: rect.Y - 6, W: rect.W + 20, H: rect.H + 12}
	renderer.SetClipRect(&clip)
	mediaHitClip = rect
	active := mediaHub.zone == mediaZoneContent && !mediaHub.sheet.open && !mediaHub.confirm.open
	baseY := rect.Y - int32(mediaHub.scrollY)
	for i := range mediaHub.cards {
		row := int32(i / cols)
		col := int32(i % cols)
		cy := baseY + row*pitch
		if cy+cardH < rect.Y-12 || cy > rect.Y+rect.H+12 {
			continue
		}
		cx := rect.X + col*(cardW+mediaCardGap)
		mediaDrawCard(renderer, &mediaHub.cards[i], sdl.Rect{X: cx, Y: cy, W: cardW, H: cardH},
			active && i == mediaHub.index, titleFont, metaFont, badgeFont, -1, i)
	}
	if loading {
		ly := baseY + int32(rowsN)*pitch + 4
		if ly < rect.Y+rect.H {
			renderSpinner(renderer, rect.X+rect.W/2-70, ly+14, 10, glassAccent())
			drawText(renderer, metaFont, "Loading more…", rect.X+rect.W/2-50, ly+5, ColorTextTertiary(), textAlignLeft)
		}
	}
	mediaHitClip = sdl.Rect{}
	renderer.SetClipRect(nil)
	if contentH > rect.H {
		thumb := float64(rect.H) / float64(contentH)
		frac := 0.0
		if max := float64(contentH - rect.H); max > 0 {
			frac = mediaHub.scrollY / max
		}
		drawScrollbar(renderer, rect.X+rect.W+8, rect.Y+2, 4, rect.H-4, thumb, frac)
	}
}

// mediaHubDrawGridEmpty is what a grid tab shows with no cards.
func mediaHubDrawGridEmpty(renderer *sdl.Renderer, config *Config, rect sdl.Rect, online bool) {
	switch mediaHub.tab {
	case mediaTabSearch:
		if mediaHub.localSearch {
			mediaHubDrawState(renderer, config, rect, "@search", "Nothing in your downloads matches",
				"Offline, search looks through the videos on this device.", "", -1, false)
			return
		}
		if mediaHub.searchPending {
			mediaHubDrawState(renderer, config, rect, "", "Searching…", "", "", -1, true)
			return
		}
		if errVal, ok := config.Variables.Custom["search_error"]; ok && errVal != nil {
			msg, _ := errVal.(string)
			if note, ok := youTubeFailureFor(mediaResultsVar); ok && note.message != "" {
				msg = note.message
			}
			if !online {
				msg = offlinePlainNotice
			} else {
				// The question the error state raises ("do I need my own API
				// key?") is answered where it is asked.
				msg = ytWithAPIKeyNote(msg)
			}
			mediaHubDrawState(renderer, config, rect, "@warning", "Search did not work", msg, "", -1, false)
			return
		}
		if !online {
			mediaHubDrawOffline(renderer, config, rect, "Search")
			return
		}
		mediaHubDrawState(renderer, config, rect, "@search", "No results", "Try different words, or paste a video link.", "", -1, false)
	case mediaTabTrending, mediaTabShorts:
		what := mediaTabLabel(mediaHub.tab)
		if !online {
			mediaHubDrawOffline(renderer, config, rect, what)
			return
		}
		if !mediaSearchIdle(mediaHubTabVar(mediaHub.tab)) || (mediaHub.tab == mediaTabShorts && !mediaSearchIdle(mediaShortsVar)) {
			mediaHubDrawState(renderer, config, rect, "", "Loading "+strings.ToLower(what)+"…", "", "", -1, true)
			return
		}
		// The list's own reason (a 403 being fixed by a yt-dlp update, a wrong
		// clock, the helper still installing) instead of one line for all.
		body := "The service did not answer. Try again in a moment."
		resultVar := mediaHubTabVar(mediaHub.tab)
		if mediaHub.tab == mediaTabShorts {
			resultVar = mediaShortsVar
		}
		if note, ok := youTubeFailureFor(resultVar); ok && note.message != "" {
			body = note.message
		}
		body = ytWithAPIKeyNote(body)
		mediaHubDrawState(renderer, config, rect, "@media", what+" did not load", body, "Try again", 1, false)
	case mediaTabHistory:
		mediaHubDrawState(renderer, config, rect, "@clock", "Nothing watched yet", "Videos you play show up here, with where you stopped.", "", -1, false)
	case mediaTabFavorites:
		mediaHubDrawState(renderer, config, rect, "@favorites", "No favorites yet",
			hintSentence("Press %s on any video and choose Add to Favorites.", "Y", "Details"), "", -1, false)
	case mediaTabLibrary:
		mediaHubDrawState(renderer, config, rect, "@folder", "Nothing here any more",
			hintSentence("Its files were moved or deleted. %s goes back.", "B", "Back"), "", -1, false)
	}
}

// ── Downloads ────────────────────────────────────────────────────────────────

// mediaHubDrawDownloads paints the Downloads list: the storage summary and its
// two settings, the transfers, then the files on the card.
func mediaHubDrawDownloads(renderer *sdl.Renderer, config *Config, rect sdl.Rect) {
	small := FontForRole(config, TypeLabel)
	titleFont := FontForRole(config, TypeBody)
	metaFont := FontForRole(config, TypeCaption)
	badgeFont := FontForRole(config, TypeCaption)
	if small == nil || titleFont == nil || metaFont == nil {
		return
	}
	entries, _, loaded := dlLibrarySnapshotView()
	all, kept := dlLibraryTotals(entries)
	limit := dlKeptLimitBytes()
	over := limit > 0 && kept > limit

	// Header: storage summary on the left, the settings chips on the right.
	headH := int32(42)
	head := sdl.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: headH}
	chipsW := mediaHubDrawDownloadChips(renderer, small, head, over)
	summary, warn := mediaDownloadsHeadline(len(entries), all, kept, limit, mediaFreeBytes.Load())
	ink := ColorTextSecondary()
	if warn {
		ink = WarningInk()
	}
	_, sh := measureText(small, summary)
	drawText(renderer, small, ellipsize(small, summary, head.W-chipsW-24), head.X+2, head.Y+(head.H-sh)/2, ink, textAlignLeft)

	list := sdl.Rect{X: rect.X, Y: rect.Y + headH + 10, W: rect.W, H: rect.H - headH - 10}
	if len(mediaHub.cards) == 0 {
		if !loaded {
			mediaHubDrawState(renderer, config, list, "", "Reading your downloads…", "", "", -1, true)
			return
		}
		mediaHubDrawState(renderer, config, list, "@download", "No downloads yet",
			"Download a video from its details (Y), or keep the ones you finish watching. Everything here plays offline.", "", -1, false)
		return
	}
	rowH := int32(84)
	gap := int32(8)
	pitch := rowH + gap
	target := mediaHub.scrollY
	if mediaHub.chipFocus >= 0 {
		target = 0
	}
	focusTop := float64(int32(mediaHub.index) * pitch)
	if focusTop < target {
		target = focusTop
	}
	if focusTop+float64(rowH) > target+float64(list.H) {
		target = focusTop + float64(rowH) - float64(list.H)
	}
	contentH := int32(len(mediaHub.cards))*pitch - gap
	if max := float64(contentH - list.H); target > max {
		target = max
	}
	if target < 0 {
		target = 0
	}
	mediaHub.scrollY = mediaEase(mediaHub.scrollY, target)

	clip := sdl.Rect{X: list.X - 6, Y: list.Y - 4, W: list.W + 12, H: list.H + 8}
	renderer.SetClipRect(&clip)
	mediaHitClip = list
	active := mediaHub.zone == mediaZoneContent && mediaHub.chipFocus < 0 && !mediaHub.sheet.open && !mediaHub.confirm.open
	y := list.Y - int32(mediaHub.scrollY)
	for i := range mediaHub.cards {
		r := sdl.Rect{X: list.X, Y: y + int32(i)*pitch, W: list.W, H: rowH}
		if r.Y+r.H < list.Y-6 || r.Y > list.Y+list.H+6 {
			continue
		}
		mediaDrawDownloadRow(renderer, &mediaHub.cards[i], r, active && i == mediaHub.index, titleFont, metaFont, badgeFont, i)
	}
	mediaHitClip = sdl.Rect{}
	renderer.SetClipRect(nil)
	if contentH > list.H {
		thumb := float64(list.H) / float64(contentH)
		frac := 0.0
		if max := float64(contentH - list.H); max > 0 {
			frac = mediaHub.scrollY / max
		}
		drawScrollbar(renderer, list.X+list.W+8, list.Y+2, 4, list.H-4, thumb, frac)
	}
}

// dlLibrarySnapshotView is the library as the Downloads view draws it. The
// copy is taken from the rows already built, so the frame does not copy the
// whole library again.
func dlLibrarySnapshotView() ([]dlLibraryEntry, uint64, bool) {
	return mediaLibraryView.entries, mediaLibraryView.gen, mediaLibraryView.loaded
}

// mediaLibraryView caches the library snapshot per change counter.
var mediaLibraryView struct {
	entries []dlLibraryEntry
	gen     uint64
	loaded  bool
	valid   bool
}

// mediaRefreshLibraryView refreshes the cached snapshot when the library moved.
func mediaRefreshLibraryView() {
	gen := dlLibraryGen()
	if mediaLibraryView.valid && gen == mediaLibraryView.gen {
		return
	}
	mediaLibraryView.entries, mediaLibraryView.gen, mediaLibraryView.loaded = dlLibrarySnapshot()
	mediaLibraryView.valid = true
}

// mediaHeadlineCache keeps the Downloads header line between frames; it is only
// rebuilt when one of the numbers in it changes.
var mediaHeadlineCache struct {
	count                  int
	all, kept, limit, free int64
	text                   string
	warn                   bool
	valid                  bool
}

// mediaDownloadsHeadline is the header line, and whether it is the warning that
// the kept videos are over the limit.
func mediaDownloadsHeadline(count int, all, kept, limit, free int64) (string, bool) {
	c := &mediaHeadlineCache
	if c.valid && c.count == count && c.all == all && c.kept == kept && c.limit == limit && c.free == free {
		return c.text, c.warn
	}
	c.count, c.all, c.kept, c.limit, c.free, c.valid = count, all, kept, limit, free, true
	c.warn = limit > 0 && kept > limit
	if c.warn {
		c.text = "Watched videos use " + humanBytes(kept) + " of your " + humanBytes(limit) + " limit"
	} else {
		c.text = mediaDownloadsSummary(count, all, free)
	}
	return c.text, c.warn
}

// mediaDownloadsSummary is the header's one line: count, total and free space.
func mediaDownloadsSummary(count int, total, free int64) string {
	if count == 0 {
		if free > 0 {
			return humanBytes(free) + " free on the card"
		}
		return "Nothing on the card yet"
	}
	noun := "files"
	if count == 1 {
		noun = "file"
	}
	line := strconv.Itoa(count) + " " + noun + " · " + humanBytes(total)
	if free > 0 {
		line += " · " + humanBytes(free) + " free"
	}
	return line
}

// mediaDownloadChipLabels are the header chips' texts, rebuilt only when a
// setting changes. overCount is how many kept videos are over the limit,
// counted once per library change and limit rather than every frame.
var mediaDownloadChipLabels struct {
	keep, limit, oldest string
	keepOn              bool
	limitGB             int
	overN               int
	overCount           int
	overGen             uint64
	overLimit           int64
	overValid           bool
}

// mediaHubDrawDownloadChips paints the Downloads header chips right-aligned in
// head and returns the width they took. The third chip only exists while the
// kept videos are over the limit.
func mediaHubDrawDownloadChips(renderer *sdl.Renderer, font *ttf.Font, head sdl.Rect, over bool) int32 {
	keepOn := dlKeepWatchedEnabled()
	limitGB := dlKeptLimitGBValue()
	lbl := &mediaDownloadChipLabels
	overN := 0
	if over {
		limit := dlKeptLimitBytes()
		if !lbl.overValid || lbl.overGen != mediaLibraryView.gen || lbl.overLimit != limit {
			o, _ := dlOverLimit(mediaLibraryView.entries, limit)
			lbl.overCount, lbl.overGen, lbl.overLimit, lbl.overValid = len(o), mediaLibraryView.gen, limit, true
		}
		overN = lbl.overCount
	}
	if lbl.keep == "" || lbl.keepOn != keepOn || lbl.limitGB != limitGB || lbl.overN != overN {
		lbl.keepOn, lbl.limitGB, lbl.overN = keepOn, limitGB, overN
		lbl.keep = "Keep watched videos: Off"
		if keepOn {
			lbl.keep = "Keep watched videos: On"
		}
		lbl.limit = "Limit: " + dlKeptLimitLabel(limitGB)
		lbl.oldest = "Remove oldest (" + strconv.Itoa(overN) + ")"
	}
	labels := [3]string{lbl.keep, lbl.limit, lbl.oldest}
	n := 2
	if overN > 0 {
		n = 3
	}
	mediaHub.chipN = n
	if mediaHub.chipFocus >= n {
		mediaHub.chipFocus = n - 1
	}
	gap := int32(10)
	x := head.X + head.W
	used := int32(0)
	h := head.H - 4
	for i := n - 1; i >= 0; i-- {
		tw, th := measureText(font, labels[i])
		w := tw + 32
		x -= w
		chip := sdl.Rect{X: x, Y: head.Y + (head.H-h)/2, W: w, H: h}
		focused := mediaHub.zone == mediaZoneContent && mediaHub.chipFocus == i && !mediaHub.sheet.open && !mediaHub.confirm.open
		if focused {
			drawGlassFocusGlow(renderer, chip)
		}
		radius := h / 2
		fill, edge, ink := glassChipFill(), glassEdge(), ColorTextPrimary()
		switch {
		case i == 2:
			fill, edge, ink = WithAlpha(ColorWarning, 40), WithAlpha(ColorWarning, 170), WarningInk()
		case i == 0 && keepOn:
			fill, edge = WithAlpha(glassAccent(), 40), WithAlpha(glassAccent(), 170)
		}
		fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, radius, fill)
		strokeRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, radius, 1, edge)
		if focused {
			drawGlassFocusRing(renderer, glassLifted(chip), radius+glassFocusLift)
		}
		drawText(renderer, font, labels[i], chip.X+16, chip.Y+(chip.H-th)/2, ink, textAlignLeft)
		mediaHub.hits = append(mediaHub.hits, mediaHit{kind: mediaHitChip, a: i, rect: chip})
		x -= gap
		used += w + gap
	}
	return used
}

// mediaDrawDownloadRow paints one row of the Downloads list.
func mediaDrawDownloadRow(renderer *sdl.Renderer, card *mediaCard, r sdl.Rect, focused bool, titleFont, metaFont, badgeFont *ttf.Font, index int) {
	UIListRow(renderer, r, glassRowRadius, focused, false)
	thumbH := r.H - 16
	thumbW := thumbH * 16 / 9
	art := sdl.Rect{X: r.X + 10, Y: r.Y + 8, W: thumbW, H: thumbH}
	mediaDrawCardArt(renderer, card, art)
	if card.duration != "" {
		mediaDrawDurationBadge(renderer, badgeFont, card.duration, art)
	}
	if card.progress > 0 {
		mediaDrawProgress(renderer, art, card.progress)
	}
	textX := art.X + art.W + 16
	right := r.X + r.W - 16
	// Right column: the state of the row in a small pill.
	pill := ""
	pillInk := ColorTextSecondary()
	switch {
	case card.queued && card.queueStatus == downloadFailed:
		pill, pillInk = "Failed", DangerInk()
	case card.queued && card.queueStatus == downloadQueued:
		pill = "Queued"
	case card.queued:
		pill = ""
	case card.overLimit:
		pill, pillInk = "Over limit", WarningInk()
	case card.kept:
		pill = "Watched"
	default:
		pill, pillInk = "Downloaded", SuccessInk()
	}
	if pill != "" && badgeFont != nil {
		pw, ph := measureText(badgeFont, pill)
		pr := sdl.Rect{X: right - pw - 20, Y: r.Y + (r.H-ph-8)/2, W: pw + 20, H: ph + 8}
		fillRoundedRect(renderer, pr.X, pr.Y, pr.W, pr.H, pr.H/2, WithAlpha(pillInk, 36))
		drawText(renderer, badgeFont, pill, pr.X+10, pr.Y+4, pillInk, textAlignLeft)
		right = pr.X - 14
	}
	textW := right - textX
	if textW < 60 {
		textW = 60
	}
	y := r.Y + 10
	if titleFont != nil {
		drawText(renderer, titleFont, ellipsize(titleFont, card.title, textW), textX, y, ColorTextPrimary(), textAlignLeft)
		y += int32(titleFont.Height()) + 2
	}
	if metaFont != nil {
		drawText(renderer, metaFont, ellipsize(metaFont, card.meta, textW), textX, y, ColorTextSecondary(), textAlignLeft)
		y += int32(metaFont.Height()) + 2
		if card.queued && card.queueStatus == downloadActive {
			bar := sdl.Rect{X: textX, Y: y + 4, W: textW, H: 5}
			UIProgressBar(renderer, bar, card.queuePct, WithAlpha(ColorTrack, 255), glassAccent())
			if card.detail != "" {
				dw, _ := measureText(metaFont, card.detail)
				if dw < textW {
					drawText(renderer, metaFont, card.detail, textX+textW-dw, y-int32(metaFont.Height())-2, ColorTextTertiary(), textAlignLeft)
				}
			}
		} else if card.detail != "" {
			ink := ColorTextTertiary()
			if card.queued && card.queueStatus == downloadFailed {
				ink = DangerInk()
			}
			drawText(renderer, metaFont, ellipsize(metaFont, card.detail, textW), textX, y, ink, textAlignLeft)
		}
	}
	mediaAddHit(mediaHit{kind: mediaHitCard, a: -1, b: index, rect: r})
}
