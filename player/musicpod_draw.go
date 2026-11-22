package main

// musicpod_draw.go — drawing the iPod layout: the title bar (play state,
// title, battery), the menus with their selection bar and scroll bar, Now
// Playing (dithered cover, visualizer or lyrics; title, artist, album; the
// progress or volume bar), the visualizer and information pages, the context
// menu, and the six transitions between screens.

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

type podFonts struct {
	small, med, big *ttf.Font
}

// podTex holds the two pages of a transition.
var podTex struct {
	a, b *sdl.Texture
	w, h int32
	ren  *sdl.Renderer
	bad  bool
}

var podRootRebuilt time.Time

func renderMusicPod(renderer *sdl.Renderer, config *Config) {
	if renderer == nil {
		return
	}
	if len(musicPod.stack) == 0 {
		musicPodEnter(config)
	}
	// The app's own bar is not drawn here: its hit areas must not answer taps.
	brwClearChromeHits()
	col := musicPodColors(config)
	small, _ := getCachedFont(config, "small")
	med, _ := getCachedFont(config, "medium")
	big, _ := getCachedFont(config, "big")
	if small == nil {
		return
	}
	if med == nil {
		med = small
	}
	if big == nil {
		big = med
	}
	f := podFonts{small: small, med: med, big: big}
	renderer.SetDrawColor(col.bg.R, col.bg.G, col.bg.B, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	// The top menu gains or loses Now Playing as music starts and stops.
	if root := musicPod.stack[0]; root.rebuild != nil && time.Since(podRootRebuilt) > time.Second {
		podRootRebuilt = time.Now()
		root.rebuild(root)
	}

	headH := int32(med.Height()) + 18
	area := sdl.Rect{X: 0, Y: headH + 1, W: screenWidth, H: screenHeight - headH - 1}
	top := podTop()
	if !podDrawTransition(renderer, config, col, f, area, top) {
		_ = renderer.SetClipRect(&area)
		podDrawPage(renderer, config, top, area, col, f, true)
		_ = renderer.SetClipRect(nil)
	} else {
		musicPod.rows = musicPod.rows[:0]
	}
	podDrawHeader(renderer, config, top.title, headH, col, f)
	if musicPod.ctx != nil {
		podDrawContext(renderer, config, area, col, f)
	} else {
		musicPod.ctxRows = musicPod.ctxRows[:0]
	}
}

// podDrawHeader draws the title bar.
func podDrawHeader(renderer *sdl.Renderer, config *Config, title string, h int32, col musicPodInk, f podFonts) {
	renderer.SetDrawColor(col.head.R, col.head.G, col.head.B, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: h})
	renderer.SetDrawColor(col.line.R, col.line.G, col.line.B, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: h, W: screenWidth, H: 1})
	musicPod.back = sdl.Rect{X: 0, Y: 0, W: screenWidth / 4, H: h}

	// Left: a back mark below the top menu, then the play state.
	x := int32(14)
	if len(musicPod.stack) > 1 {
		renderText(renderer, config, f.med, "‹", col.headInk, x, (h-int32(f.med.Height()))/2)
		x += 22
	}
	music.mu.Lock()
	playing, paused := music.playing, music.paused
	music.mu.Unlock()
	g := h / 3
	gy := (h - g) / 2
	switch {
	case playing && !paused:
		podTriangle(renderer, x, gy, g, col.headInk)
	case playing:
		renderer.SetDrawColor(col.headInk.R, col.headInk.G, col.headInk.B, 255)
		renderer.FillRect(&sdl.Rect{X: x, Y: gy, W: g / 3, H: g})
		renderer.FillRect(&sdl.Rect{X: x + g*2/3, Y: gy, W: g / 3, H: g})
	}

	// Right: the battery.
	bw, bh := h*3/4, h*2/5
	bx, by := screenWidth-14-bw, (h-bh)/2
	podStroke(renderer, sdl.Rect{X: bx, Y: by, W: bw - 4, H: bh}, col.headInk)
	renderer.SetDrawColor(col.headInk.R, col.headInk.G, col.headInk.B, 255)
	renderer.FillRect(&sdl.Rect{X: bx + bw - 4, Y: by + bh/3, W: 4, H: bh / 3})
	pct := getBatteryPercent()
	if pct >= 0 {
		fill := int32(float64(bw-10) * math.Min(1, float64(pct)/100))
		renderer.FillRect(&sdl.Rect{X: bx + 3, Y: by + 3, W: fill, H: bh - 6})
	}
	if getBatteryCharging() {
		tw, _ := measureText(f.small, "+")
		renderText(renderer, config, f.small, "+", col.headInk, bx-int32(tw)-6, (h-int32(f.small.Height()))/2)
	}

	// The title, centred between the two.
	maxW := screenWidth - 2*(bw+60)
	line := ellipsize(f.med, title, maxW)
	tw, _ := measureText(f.med, line)
	renderText(renderer, config, f.med, line, col.headInk, (screenWidth-tw)/2, (h-int32(f.med.Height()))/2)
}

// podTriangle fills a right-pointing triangle of side s.
func podTriangle(renderer *sdl.Renderer, x, y, s int32, c sdl.Color) {
	renderer.SetDrawColor(c.R, c.G, c.B, 255)
	for i := int32(0); i < s; i++ {
		half := s / 2
		d := i - half
		if d < 0 {
			d = -d
		}
		w := (half - d) * 2
		if w > 0 {
			renderer.FillRect(&sdl.Rect{X: x, Y: y + i, W: w, H: 1})
		}
	}
}

// podStroke draws a 2 px outline.
func podStroke(renderer *sdl.Renderer, r sdl.Rect, c sdl.Color) {
	renderer.SetDrawColor(c.R, c.G, c.B, 255)
	renderer.FillRect(&sdl.Rect{X: r.X, Y: r.Y, W: r.W, H: 2})
	renderer.FillRect(&sdl.Rect{X: r.X, Y: r.Y + r.H - 2, W: r.W, H: 2})
	renderer.FillRect(&sdl.Rect{X: r.X, Y: r.Y, W: 2, H: r.H})
	renderer.FillRect(&sdl.Rect{X: r.X + r.W - 2, Y: r.Y, W: 2, H: r.H})
}

// podDrawPage draws one page into area; live records the hit areas.
func podDrawPage(renderer *sdl.Renderer, config *Config, p *podPage, area sdl.Rect, col musicPodInk, f podFonts, live bool) {
	switch p.kind {
	case podPageNowPlaying:
		podDrawNowPlaying(renderer, config, area, col, f, live)
	case podPageVisualizer:
		podDrawVisualizerPage(renderer, config, p, area, col, f)
	case podPageInfo:
		podDrawInfo(renderer, config, p, area, col, f)
	default:
		podDrawList(renderer, config, p, area, col, f, live)
	}
}

// podDrawList draws a menu.
func podDrawList(renderer *sdl.Renderer, config *Config, p *podPage, area sdl.Rect, col musicPodInk, f podFonts, live bool) {
	if live {
		musicPod.rows = musicPod.rows[:0]
	}
	if p.loading && len(p.entries) == 0 {
		renderSpinner(renderer, area.X+area.W/2, area.Y+area.H/2-20, 16, col.ink)
		podCentered(renderer, config, f.small, "Loading…", col.dim, area, area.Y+area.H/2+14)
		return
	}
	if len(p.entries) == 0 {
		msg := p.empty
		if p.errText != "" {
			msg = p.errText
		}
		if msg == "" {
			msg = "Nothing here"
		}
		podWrapped(renderer, config, f.small, msg, col.dim, area, area.Y+area.H/3)
		return
	}
	rowH := int32(f.med.Height()) + 14
	visible := int((area.H - 4) / rowH)
	if visible < 1 {
		visible = 1
	}
	if p.cursor < p.scroll {
		p.scroll = p.cursor
	}
	if p.cursor >= p.scroll+visible {
		p.scroll = p.cursor - visible + 1
	}
	if p.scroll > len(p.entries)-visible {
		p.scroll = len(p.entries) - visible
	}
	if p.scroll < 0 {
		p.scroll = 0
	}
	scrollbar := len(p.entries) > visible
	rowW := area.W
	if scrollbar {
		rowW -= 12
	}
	if live {
		musicPod.rowBase = p.scroll
	}
	for i := p.scroll; i < len(p.entries) && i < p.scroll+visible; i++ {
		e := p.entries[i]
		y := area.Y + 2 + int32(i-p.scroll)*rowH
		r := sdl.Rect{X: area.X, Y: y, W: rowW, H: rowH}
		ink, side := col.ink, col.dim
		if e.dim {
			ink = col.dim
		}
		if i == p.cursor {
			renderer.SetDrawColor(col.selBg.R, col.selBg.G, col.selBg.B, 255)
			renderer.FillRect(&r)
			ink, side = col.selInk, col.selInk
		}
		right := e.right
		if e.value != nil {
			right = e.value()
		}
		textY := y + (rowH-int32(f.med.Height()))/2
		chevW := int32(0)
		if e.more {
			chevW = 26
			renderText(renderer, config, f.med, "›", side, r.X+r.W-22, textY)
		}
		rw := int32(0)
		if right != "" {
			right = ellipsize(f.small, right, r.W/2)
			rw, _ = measureText(f.small, right)
			renderText(renderer, config, f.small, right, side, r.X+r.W-chevW-14-rw, y+(rowH-int32(f.small.Height()))/2)
		}
		label := ellipsize(f.med, e.label, r.W-28-chevW-rw-24)
		renderText(renderer, config, f.med, label, ink, r.X+16, textY)
		if live {
			musicPod.rows = append(musicPod.rows, r)
		}
	}
	if scrollbar {
		track := sdl.Rect{X: area.X + area.W - 8, Y: area.Y + 4, W: 6, H: area.H - 8}
		renderer.SetDrawColor(col.line.R, col.line.G, col.line.B, 255)
		renderer.FillRect(&track)
		thumbH := track.H * int32(visible) / int32(len(p.entries))
		if thumbH < 16 {
			thumbH = 16
		}
		thumbY := track.Y + (track.H-thumbH)*int32(p.scroll)/int32(max(1, len(p.entries)-visible))
		renderer.SetDrawColor(col.ink.R, col.ink.G, col.ink.B, 255)
		renderer.FillRect(&sdl.Rect{X: track.X, Y: thumbY, W: track.W, H: thumbH})
	}
}

// podCentered draws one centred line.
func podCentered(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, c sdl.Color, area sdl.Rect, y int32) {
	text = ellipsize(font, text, area.W-40)
	tw, _ := measureText(font, text)
	renderText(renderer, config, font, text, c, area.X+(area.W-tw)/2, y)
}

// podWrapped draws a paragraph centred in area from y.
func podWrapped(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, c sdl.Color, area sdl.Rect, y int32) {
	measure := func(s string) int32 { w, _ := measureText(font, s); return w }
	for _, line := range wrapMeasured([]rune(text), area.W-80, measure) {
		podCentered(renderer, config, font, line, c, area, y)
		y += int32(font.Height()) + 4
	}
}

// podDrawNowPlaying draws Now Playing.
func podDrawNowPlaying(renderer *sdl.Renderer, config *Config, area sdl.Rect, col musicPodInk, f podFonts, live bool) {
	music.mu.Lock()
	cur, n := music.current, len(music.tracks)
	path, name := "", ""
	if cur >= 0 && cur < n {
		path, name = music.tracks[cur].Path, music.tracks[cur].Name
	}
	pos, dur := music.position, music.duration
	playing, paused := music.playing, music.paused
	shuffle, repeat := music.shuffle, music.repeatMode
	volume := music.volume
	lyrics := music.lyrics
	music.mu.Unlock()
	if path == "" {
		podWrapped(renderer, config, f.small, "Nothing is playing. Pick a song from Music, or Shuffle Songs.", col.dim, area, area.Y+area.H/3)
		return
	}
	meta, _ := musicMetaFor(path)
	title := dlFirstNonEmpty(meta.Title, musicTrackTitle(name))
	pad := max32(12, area.H/36)
	smallH := int32(f.small.Height())

	// The top line: "3 of 12" and the play modes.
	renderText(renderer, config, f.small, fmt.Sprintf("%d of %d", cur+1, n), col.dim, area.X+pad, area.Y+pad/2)
	var modes []string
	if shuffle {
		modes = append(modes, "Shuffle")
	}
	if repeat != musicRepeatOff {
		modes = append(modes, repeat.short())
	}
	if len(modes) > 0 {
		m := strings.Join(modes, " · ")
		mw, _ := measureText(f.small, m)
		renderText(renderer, config, f.small, m, col.dim, area.X+area.W-pad-mw, area.Y+pad/2)
	}

	// The bottom: the progress (or volume) bar and the times.
	barH := int32(10)
	timesY := area.Y + area.H - pad - smallH
	barY := timesY - 8 - barH
	bar := sdl.Rect{X: area.X + pad, Y: barY, W: area.W - 2*pad, H: barH}
	volumeShown := time.Since(musicPod.volAt) < 1500*time.Millisecond
	podStroke(renderer, bar, col.ink)
	frac := 0.0
	if volumeShown {
		frac = volume
	} else if dur > 0 {
		frac = math.Max(0, math.Min(1, pos/dur))
	}
	if fill := int32(float64(bar.W-4) * frac); fill > 0 {
		renderer.SetDrawColor(col.ink.R, col.ink.G, col.ink.B, 255)
		renderer.FillRect(&sdl.Rect{X: bar.X + 2, Y: bar.Y + 2, W: fill, H: bar.H - 4})
	}
	if volumeShown {
		podCentered(renderer, config, f.small, fmt.Sprintf("Volume %d%%", int(math.Round(volume*100))), col.ink, area, timesY)
	} else {
		renderText(renderer, config, f.small, formatTime(pos), col.ink, bar.X, timesY)
		rest := "--:--"
		if dur > 0 {
			rest = "-" + formatTime(math.Max(0, dur-pos))
		}
		rw, _ := measureText(f.small, rest)
		renderText(renderer, config, f.small, rest, col.ink, bar.X+bar.W-rw, timesY)
	}
	if live {
		musicPod.npSeek = sdl.Rect{X: bar.X, Y: bar.Y - 12, W: bar.W, H: bar.H + 24}
	}

	// The picture: cover, visualizer or lyrics.
	top := area.Y + pad/2 + smallH + pad/2
	side := barY - pad - top
	if w := area.W * 44 / 100; side > w {
		side = w
	}
	if side < 32 {
		side = 32
	}
	art := sdl.Rect{X: area.X + pad, Y: top, W: side, H: side}
	if live {
		musicPod.npArt = art
	}
	pal := musicPodPalette(config)
	switch {
	case musicPod.npMode == 1:
		musicVizDraw(renderer, art, musicVizStyle(config), pal, musicVizDitherOn(config))
	case musicPod.npMode == 2 && len(lyrics) > 0:
		podDrawLyrics(renderer, config, f.small, lyrics, pos, art, col)
	default:
		tex := musicPodArt(renderer, meta.Art, pal, musicVizDitherOn(config))
		if tex != nil {
			_ = renderer.Copy(tex, nil, &art)
		} else {
			renderer.SetDrawColor(col.line.R, col.line.G, col.line.B, 255)
			renderer.FillRect(&art)
			podNote(renderer, art, col.bg)
		}
	}
	podStroke(renderer, art, col.ink)

	// The words: title, artist, album, and what shapes the sound.
	tx := art.X + art.W + pad*3/2
	tw := area.X + area.W - pad - tx
	y := top + pad/2
	if tw > 60 {
		for _, l := range []struct {
			font *ttf.Font
			text string
			ink  sdl.Color
		}{
			{f.big, title, col.ink},
			{f.med, meta.Artist, col.ink},
			{f.med, meta.Album, col.dim},
		} {
			if strings.TrimSpace(l.text) == "" {
				continue
			}
			renderText(renderer, config, l.font, ellipsize(l.font, l.text, tw), l.ink, tx, y)
			y += int32(l.font.Height()) + 6
		}
		status := podSoundStatus()
		if status != "" {
			renderText(renderer, config, f.small, ellipsize(f.small, status, tw), col.dim, tx, y+4)
			y += smallH + 8
		}
		// Transport buttons for a touchscreen.
		bs := max32(36, int32(f.med.Height())+12)
		by := y + pad
		if by+bs < barY-pad {
			names := []string{"skip-back", "pause", "skip-forward"}
			if !playing || paused {
				names[1] = "play"
			}
			rects := [3]sdl.Rect{}
			for i, icon := range names {
				r := sdl.Rect{X: tx + int32(i)*(bs+pad), Y: by, W: bs, H: bs}
				podStroke(renderer, r, col.line)
				g := bs * 11 / 20
				drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: r.X + (bs-g)/2, Y: r.Y + (bs-g)/2, W: g, H: g}, col.ink)
				rects[i] = r
			}
			if live {
				musicPod.npPrev, musicPod.npPlay, musicPod.npNext = rects[0], rects[1], rects[2]
			}
		} else if live {
			musicPod.npPrev, musicPod.npPlay, musicPod.npNext = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
		}
	}
}

// podNote draws a quaver in the middle of r: the cover of a song without one
// (the UI font has no music signs).
func podNote(renderer *sdl.Renderer, r sdl.Rect, c sdl.Color) {
	u := r.W / 12
	if u < 2 {
		u = 2
	}
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	head := u * 3 / 2
	fillCircle(renderer, cx-u, cy+3*u, head, c)
	renderer.SetDrawColor(c.R, c.G, c.B, 255)
	renderer.FillRect(&sdl.Rect{X: cx - u + head - u/2, Y: cy - 3*u, W: u / 2 * 2, H: 6 * u})
	renderer.FillRect(&sdl.Rect{X: cx - u + head - u/2, Y: cy - 3*u, W: 3 * u, H: u})
}

// podSoundStatus is the line of what shapes the sound: effects, EQ, AutoEQ,
// speed.
func podSoundStatus() string {
	var parts []string
	if fx := musicFXStatus(); fx != "FX off" {
		parts = append(parts, fx)
	}
	if musicEQ.bitPerfect {
		parts = append(parts[:0], "Bit-perfect")
		return strings.Join(parts, " · ")
	}
	if musicEQ.enabled {
		parts = append(parts, "EQ "+musicEQ.preset)
	}
	if a := musicAutoEQName(); a != "" {
		parts = append(parts, "AutoEQ "+a)
	}
	music.mu.Lock()
	s := music.speed
	music.mu.Unlock()
	if s > 0 && math.Abs(s-1) > 0.01 {
		parts = append(parts, formatSpeedLabel(s))
	}
	return strings.Join(parts, " · ")
}

// podDrawLyrics draws the lyric around the position in the theme's shades.
func podDrawLyrics(renderer *sdl.Renderer, config *Config, font *ttf.Font, lines []lyricLine, pos float64, r sdl.Rect, col musicPodInk) {
	idx := lyricIndexAt(lines, pos)
	lineH := int32(font.Height()) + 8
	mid := r.Y + r.H/2 - lineH/2
	for off := -3; off <= 3; off++ {
		i := idx + off
		if i < 0 || i >= len(lines) {
			continue
		}
		text := ellipsize(font, strings.TrimSpace(lines[i].Text), r.W-16)
		if text == "" {
			continue
		}
		ink := col.dim
		if off == 0 {
			ink = col.ink
		}
		w, _ := measureText(font, text)
		renderText(renderer, config, font, text, ink, r.X+(r.W-w)/2, mid+int32(off)*lineH)
	}
}

// podDrawVisualizerPage is a visualizer over the whole page.
func podDrawVisualizerPage(renderer *sdl.Renderer, config *Config, p *podPage, area sdl.Rect, col musicPodInk, f podFonts) {
	capH := int32(f.small.Height()) + 12
	stage := sdl.Rect{X: area.X, Y: area.Y, W: area.W, H: area.H - capH}
	musicVizDraw(renderer, stage, musicVizStyle(config), musicPodPalette(config), musicVizDitherOn(config))
	p.title = musicVizNames[musicVizStyle(config)]
	hint := hintLine(FooterHint{"Left/Right", "Visualizer"}, FooterHint{"A", "Pause"}, FooterHint{"B", "Back"})
	podCentered(renderer, config, f.small, hint, col.dim, area, area.Y+area.H-capH+6)
}

// podDrawInfo draws an information page.
func podDrawInfo(renderer *sdl.Renderer, config *Config, p *podPage, area sdl.Rect, col musicPodInk, f podFonts) {
	y := area.Y + 16
	measure := func(s string) int32 { w, _ := measureText(f.med, s); return w }
	for i, line := range p.info {
		font, ink := f.med, col.ink
		if i == 0 && len(p.info) > 1 {
			font = f.big
		}
		if line == "" {
			y += int32(f.small.Height()) / 2
			continue
		}
		for _, w := range wrapMeasured([]rune(line), area.W-48, measure) {
			if y+int32(font.Height()) > area.Y+area.H {
				return
			}
			renderTextRaw(renderer, config, font, w, ink, area.X+24, y)
			y += int32(font.Height()) + 4
		}
	}
}

// podDrawContext draws the context menu over the page.
func podDrawContext(renderer *sdl.Renderer, config *Config, area sdl.Rect, col musicPodInk, f podFonts) {
	m := musicPod.ctx
	rowH := int32(f.med.Height()) + 12
	w := area.W * 7 / 10
	if w > 680 {
		w = 680
	}
	rows := len(m.entries)
	maxRows := int((area.H - 40 - rowH) / rowH)
	if rows > maxRows {
		rows = maxRows
	}
	h := rowH*int32(rows+1) + 16
	box := sdl.Rect{X: area.X + (area.W-w)/2, Y: area.Y + (area.H-h)/2, W: w, H: h}
	renderer.SetDrawColor(col.bg.R, col.bg.G, col.bg.B, 255)
	renderer.FillRect(&box)
	podStroke(renderer, box, col.ink)
	podCentered(renderer, config, f.small, m.title, col.dim, box, box.Y+(rowH-int32(f.small.Height()))/2+4)
	first := 0
	if m.cursor >= rows {
		first = m.cursor - rows + 1
	}
	musicPod.ctxRows = musicPod.ctxRows[:0]
	for i := first; i < len(m.entries) && i < first+rows; i++ {
		e := m.entries[i]
		r := sdl.Rect{X: box.X + 4, Y: box.Y + 8 + rowH*int32(i-first+1), W: box.W - 8, H: rowH}
		ink := col.ink
		if i == m.cursor {
			renderer.SetDrawColor(col.selBg.R, col.selBg.G, col.selBg.B, 255)
			renderer.FillRect(&r)
			ink = col.selInk
		}
		value := ""
		if e.value != nil {
			value = e.value()
		}
		vw := int32(0)
		if value != "" {
			vw, _ = measureText(f.small, value)
			renderText(renderer, config, f.small, value, ink, r.X+r.W-vw-14, r.Y+(rowH-int32(f.small.Height()))/2)
		}
		renderText(renderer, config, f.med, ellipsize(f.med, e.label, r.W-vw-40), ink, r.X+14, r.Y+(rowH-int32(f.med.Height()))/2)
		// Hit areas are listed from the first entry, so a tap maps by index.
		for len(musicPod.ctxRows) < i {
			musicPod.ctxRows = append(musicPod.ctxRows, sdl.Rect{})
		}
		musicPod.ctxRows = append(musicPod.ctxRows, r)
	}
}

// ── Transitions ─────────────────────────────────────────────────────────────

// podEnsureTex makes the two page textures for a w×h area.
func podEnsureTex(renderer *sdl.Renderer, w, h int32) bool {
	if podTex.bad {
		return false
	}
	if podTex.a != nil && (podTex.ren != renderer || podTex.w != w || podTex.h != h) {
		if podTex.ren == renderer {
			_ = podTex.a.Destroy()
			_ = podTex.b.Destroy()
		}
		podTex.a, podTex.b = nil, nil
	}
	if podTex.a == nil {
		a, err := renderer.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_TARGET, w, h)
		if err != nil || a == nil {
			podTex.bad = true
			return false
		}
		b, err := renderer.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_TARGET, w, h)
		if err != nil || b == nil {
			_ = a.Destroy()
			podTex.bad = true
			return false
		}
		_ = a.SetBlendMode(sdl.BLENDMODE_BLEND)
		_ = b.SetBlendMode(sdl.BLENDMODE_BLEND)
		podTex.a, podTex.b, podTex.w, podTex.h, podTex.ren = a, b, w, h, renderer
	}
	return true
}

// podRenderInto draws a page into a texture.
func podRenderInto(renderer *sdl.Renderer, config *Config, tex *sdl.Texture, p *podPage, w, h int32, col musicPodInk, f podFonts) bool {
	prev := renderer.GetRenderTarget()
	if err := renderer.SetRenderTarget(tex); err != nil {
		podTex.bad = true
		return false
	}
	_ = renderer.SetClipRect(nil)
	renderer.SetDrawColor(col.bg.R, col.bg.G, col.bg.B, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: w, H: h})
	podDrawPage(renderer, config, p, sdl.Rect{X: 0, Y: 0, W: w, H: h}, col, f, false)
	_ = renderer.SetRenderTarget(prev)
	return true
}

// podDrawTransition draws the screen change in progress and reports whether
// there was one.
func podDrawTransition(renderer *sdl.Renderer, config *Config, col musicPodInk, f podFonts, area sdl.Rect, top *podPage) bool {
	t := musicPod.trans
	if t.from == nil || t.from == top {
		return false
	}
	p := float64(time.Since(t.start)) / float64(podTransitionLength)
	if p >= 1 || t.kind == len(musicPodTransitions)-1 {
		musicPod.trans.from = nil
		return false
	}
	ease := 1 - (1-p)*(1-p)*(1-p)
	if !podEnsureTex(renderer, area.W, area.H) ||
		!podRenderInto(renderer, config, podTex.a, t.from, area.W, area.H, col, f) ||
		!podRenderInto(renderer, config, podTex.b, top, area.W, area.H, col, f) {
		musicPod.trans.from = nil
		return false
	}
	_ = renderer.SetClipRect(&area)
	defer func() { _ = renderer.SetClipRect(nil) }()
	old, cur := podTex.a, podTex.b
	place := func(tex *sdl.Texture, x, y int32, scale float64, alpha uint8) {
		w, h := int32(float64(area.W)*scale), int32(float64(area.H)*scale)
		dst := sdl.Rect{X: area.X + x + (area.W-w)/2, Y: area.Y + y + (area.H-h)/2, W: w, H: h}
		_ = tex.SetAlphaMod(alpha)
		_ = renderer.Copy(tex, nil, &dst)
		_ = tex.SetAlphaMod(255)
	}
	dir := int32(t.dir)
	if dir == 0 {
		dir = 1
	}
	shift := int32(float64(area.W) * ease)
	switch t.kind {
	case 0: // Push: the new screen pushes the old one off, as on an iPod.
		place(old, -dir*shift, 0, 1, 255)
		place(cur, dir*(area.W-shift), 0, 1, 255)
	case 1: // Slide: the new screen slides over (back: the old one slides away).
		if dir > 0 {
			place(old, 0, 0, 1, 255)
			place(cur, area.W-shift, 0, 1, 255)
		} else {
			place(cur, 0, 0, 1, 255)
			place(old, shift, 0, 1, 255)
		}
	case 2: // Fade.
		place(old, 0, 0, 1, 255)
		place(cur, 0, 0, 1, uint8(ease*255))
	case 3: // Zoom.
		if dir > 0 {
			place(old, 0, 0, 1, 255)
			place(cur, 0, 0, 0.82+0.18*ease, uint8(ease*255))
		} else {
			place(cur, 0, 0, 1, 255)
			place(old, 0, 0, 1-0.18*ease, uint8((1-ease)*255))
		}
	case 4: // Dither: the new screen shows through in a Bayer order of blocks.
		place(old, 0, 0, 1, 255)
		const block = 24
		limit := ease * 16
		for by := int32(0); by*block < area.H; by++ {
			for bx := int32(0); bx*block < area.W; bx++ {
				if float64(vizBayer[by&3][bx&3]) >= limit {
					continue
				}
				src := sdl.Rect{X: bx * block, Y: by * block, W: block, H: block}
				dst := sdl.Rect{X: area.X + src.X, Y: area.Y + src.Y, W: block, H: block}
				_ = renderer.Copy(cur, &src, &dst)
			}
		}
	default:
		return false
	}
	return true
}
