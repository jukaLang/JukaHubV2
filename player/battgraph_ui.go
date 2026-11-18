package main

// battgraph_ui.go — the Battery screen: the level over the last 2, 8 or 24
// hours (battgraph.go keeps the history), how long the battery lasts
// (battery_eta.go's estimate, not a second one), and two figures the graph
// makes easy to trust: how fast it drains with the screen on, and when it was
// last full.
//
// One page in the tool screens' frame (tbxui.go): the level and estimate on
// top with the range pills, the graph in a glass card — gridlines at quarters,
// a faint red band under 20 %, green shading where a cable was in, the line
// broken wherever the device slept or JukaHub was not running — and a line of
// stats under it. L1/R1 (or Left/Right, or A) step the range; B goes back. The
// x axis is labelled "8 h ago … now", never with a time of day: the handheld
// boots before network time (battgraph.go explains the clock handling).
//
// Opened from Settings → Power & battery → Battery history. The model is
// rebuilt once a second (the estimate counts down) or when the history or the
// range changes; drawing reuses its point and rect slices, so a frame
// allocates nothing.

import (
	"strconv"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const battGraphSceneName = "Battery"

// The ranges L1/R1 step through, with their axis words.
var (
	battGraphRanges      = [...]int64{2 * 3600, 8 * 3600, 24 * 3600}
	battGraphRangeLabels = [...]string{"2 h", "8 h", "24 h"}
	battGraphHalfLabels  = [...]string{"1 h", "4 h", "12 h"}
	battGraphGridPcts    = [...]uint8{0, 25, 50, 75, 100}
)

// battGraphModel is what the screen draws, rebuilt at most once a second.
type battGraphModel struct {
	builtAt  time.Time
	rev      uint64
	rangeIdx int
	plot     sdl.Rect
	header   UIPageHeader
	level    string
	detail   string
	stats    string
	legend   string
	xLeft    string
	xMid     string
	points   int
	line     []sdl.Point
	line2    []sdl.Point // the line one pixel lower: a 2 px stroke
	segEnds  []int       // where each unbroken stretch of line ends
	spans    []sdl.Rect  // charging spans, full plot height
}

// battGraphScreenState is the screen's state; samples is its copy of the
// history.
type battGraphScreenState struct {
	visit    tbxVisit
	rangeIdx int
	samples  []battSample
	model    battGraphModel
}

// The screen opens on 8 hours: a day of play without the night.
var battGraphScreen = battGraphScreenState{rangeIdx: 1}

// battGraphOpen opens the Battery screen (Settings → Power & battery). The
// scene lives in memory only, like the tool screens'.
func battGraphOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	if sceneIndexExact(config, battGraphSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: battGraphSceneName})
	}
	tbxOpenScene(config, battGraphSceneName)
}

type battGraphScene struct{ sceneBase }

func (battGraphScene) render(r *sdl.Renderer, c *Config) bool {
	battGraphRender(r, c)
	return true
}

func (battGraphScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), battGraphAction)
}

func (battGraphScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), battGraphAction)
}

func init() { registerScene(battGraphScene{sceneBase{name: battGraphSceneName}}) }

// battGraphAction steps the range: L1/Left shorter, R1/Right longer, A round.
// Anything else falls through (Up reaches the top bar, B leaves).
func battGraphAction(_ *Config, act tbxAction) bool {
	sc := &battGraphScreen
	switch act {
	case tbxActPageUp, tbxActLeft:
		if sc.rangeIdx > 0 {
			sc.rangeIdx--
		}
		return true
	case tbxActPageDown, tbxActRight:
		if sc.rangeIdx < len(battGraphRanges)-1 {
			sc.rangeIdx++
		}
		return true
	case tbxActConfirm:
		sc.rangeIdx = (sc.rangeIdx + 1) % len(battGraphRanges)
		return true
	}
	return false
}

// battGraphFooterHints is the screen's footer; ok is false for any other
// scene.
func battGraphFooterHints(name string) ([]FooterHint, bool) {
	if name != battGraphSceneName {
		return nil, false
	}
	return []FooterHint{
		{Button: "L1/R1", Label: "Range"},
		{Button: "B", Label: "Back"},
	}, true
}

// battGraphRender draws the screen.
func battGraphRender(renderer *sdl.Renderer, config *Config) {
	sc := &battGraphScreen
	m := &sc.model
	if sc.visit.begin() {
		m.builtAt = time.Time{}
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	if m.builtAt.IsZero() || m.rangeIdx != sc.rangeIdx {
		m.header = tbxHeader("Battery", "The level over time and how long it lasts", "battery",
			"Last "+battGraphRangeLabels[sc.rangeIdx], ColorTextTertiary())
	}
	frame := tbxBeginFrame(renderer, config, m.header)
	body := frame.bodyRect()
	x, w := body.X, body.W
	y := body.Y

	// Layout: the level line, the detail, the card, two lines of stats.
	statsH := 2*fonts.smallH + 22
	card := sdl.Rect{X: x, Y: y + fonts.titleH + fonts.smallH + 16, W: w}
	card.H = body.Y + body.H - statsH - card.Y
	if card.H < 80 {
		card.H = 80
	}
	labelW, _ := measureText(fonts.tiny, "100%")
	inset := 14 + labelW + 8
	plot := sdl.Rect{X: card.X + inset, Y: card.Y + 14, W: card.W - inset - 16, H: card.H - 14 - fonts.tinyH - 14}

	now := time.Now()
	if rev := battGraphRevision(); rev != m.rev || m.builtAt.IsZero() || now.Sub(m.builtAt) >= time.Second ||
		m.rangeIdx != sc.rangeIdx || m.plot != plot {
		battGraphBuild(config, plot, now)
		m.rev = rev
	}

	// The range pills, right-aligned on the level line; the level fits
	// what they leave.
	right := x + w
	for i := len(battGraphRanges) - 1; i >= 0; i-- {
		tone := tbxToneNeutral
		if i == sc.rangeIdx {
			tone = tbxToneAccent
		}
		r := tbxDrawPill(renderer, config, fonts.tiny, right, y+fonts.titleH/2, battGraphRangeLabels[i], tone)
		right = r.X - 8
	}
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, m.level, right-x-12), ColorTextPrimary(), x, y)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, m.detail, w), ColorTextTertiary(), x, y+fonts.titleH+4)

	drawGlassSurface(renderer, card, 14, glassState{})
	if m.points < 2 || plot.W < 40 || plot.H < 30 {
		tbxDrawEmpty(renderer, config, card, "battery", "Collecting battery history",
			"The level is recorded once a minute, games included; the graph fills in as you play.")
	} else {
		battGraphDrawPlot(renderer, config, fonts, plot)
	}

	sy := card.Y + card.H + 10
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, m.stats, w), ColorTextSecondary(), x, sy)
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, m.legend, w), ColorTextTertiary(), x, sy+fonts.smallH+4)
	tbxEndFrame(renderer, config)
}

// battGraphDrawPlot draws the gridlines, the low band, the charging spans,
// the line and the axis words from the built model.
func battGraphDrawPlot(renderer *sdl.Renderer, config *Config, fonts tbxFonts, plot sdl.Rect) {
	m := &battGraphScreen.model
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	low := battGraphY(plot, 20)
	band := WithAlpha(ColorToastError, 26)
	renderer.SetDrawColor(band.R, band.G, band.B, band.A)
	renderer.FillRect(&sdl.Rect{X: plot.X, Y: low, W: plot.W, H: plot.Y + plot.H - low})

	if len(m.spans) > 0 {
		c := WithAlpha(ColorToastSuccess, 46)
		renderer.SetDrawColor(c.R, c.G, c.B, c.A)
		renderer.FillRects(m.spans)
	}

	grid := WithAlpha(ColorTextTertiary(), 60)
	renderer.SetDrawColor(grid.R, grid.G, grid.B, grid.A)
	for _, p := range battGraphGridPcts {
		gy := battGraphY(plot, p)
		renderer.DrawLine(plot.X, gy, plot.X+plot.W-1, gy)
	}
	for _, p := range [...]uint8{100, 50, 0} {
		label := "0%"
		switch p {
		case 100:
			label = "100%"
		case 50:
			label = "50%"
		}
		lw, _ := measureText(fonts.tiny, label)
		renderText(renderer, config, fonts.tiny, label, ColorTextTertiary(), plot.X-8-lw, battGraphY(plot, p)-fonts.tinyH/2)
	}

	renderer.SetDrawColor(ColorAccent.R, ColorAccent.G, ColorAccent.B, 255)
	start := 0
	for _, end := range m.segEnds {
		if end-start == 1 {
			p := m.line[start]
			renderer.FillRect(&sdl.Rect{X: p.X - 1, Y: p.Y - 1, W: 3, H: 3})
		} else if end-start > 1 {
			renderer.DrawLines(m.line[start:end])
			renderer.DrawLines(m.line2[start:end])
		}
		start = end
	}

	ay := plot.Y + plot.H + 6
	renderText(renderer, config, fonts.tiny, m.xLeft, ColorTextTertiary(), plot.X, ay)
	mw, _ := measureText(fonts.tiny, m.xMid)
	renderText(renderer, config, fonts.tiny, m.xMid, ColorTextTertiary(), plot.X+plot.W/2-mw/2, ay)
	nw, _ := measureText(fonts.tiny, "now")
	renderText(renderer, config, fonts.tiny, "now", ColorTextTertiary(), plot.X+plot.W-nw, ay)
}

// battGraphY is the y of a level in the plot.
func battGraphY(plot sdl.Rect, pct uint8) int32 {
	return plot.Y + plot.H - 1 - int32(pct)*(plot.H-1)/100
}

// battGraphBuild rebuilds the model: the words, the stats and the geometry
// for plot.
func battGraphBuild(config *Config, plot sdl.Rect, now time.Time) {
	sc := &battGraphScreen
	m := &sc.model
	var runStart int
	sc.samples, runStart, _ = battGraphCopy(sc.samples)
	samples := sc.samples
	m.builtAt, m.rangeIdx, m.plot = now, sc.rangeIdx, plot

	// Until the clock is plausible, older stamps and this run's do not share
	// a clock: only this run's samples are shown.
	clockOK := !now.Before(clockFloor())
	if !clockOK {
		samples = samples[min(runStart, len(samples)):]
	}

	m.level = batteryETASettingsValue(config)
	if m.level == "" {
		m.level = "No battery reading"
	}
	m.detail = batteryETASettingsDetail(config)
	m.xLeft = battGraphRangeLabels[sc.rangeIdx] + " ago"
	m.xMid = battGraphHalfLabels[sc.rangeIdx] + " ago"

	to := now.Unix()
	from := to - battGraphRanges[sc.rangeIdx]
	battGraphGeometry(m, samples, plot, from, to)

	since := to - 24*3600
	stats := "Screen-on drain: still measuring"
	if r, ok := battGraphDrain(samples, since, false); ok {
		stats = "Screen on: " + battGraphRate(r)
		if g, ok := battGraphDrain(samples, since, true); ok {
			stats += " · in games " + battGraphRate(g)
		}
	}
	m.stats = stats + " · " + battGraphFullText(samples, to)
	m.legend = "Green: on the cable · red band: under 20% · gaps: asleep or off"
	if !clockOK {
		m.legend = "Clock not set yet: this session only · green: on the cable"
	}
}

// battGraphGeometry maps the samples inside [from, to] onto plot: the line
// (broken at gaps longer than battGraphGapSecs and wherever the stamps go
// backwards) and the merged charging spans. It reuses the model's slices.
func battGraphGeometry(m *battGraphModel, samples []battSample, plot sdl.Rect, from, to int64) {
	m.line, m.line2, m.segEnds, m.spans = m.line[:0], m.line2[:0], m.segEnds[:0], m.spans[:0]
	m.points = 0
	span := to - from
	if span <= 0 || plot.W < 2 || plot.H < 2 {
		return
	}
	xOf := func(at int64) int32 {
		return plot.X + int32((at-from)*int64(plot.W-1)/span)
	}
	var prev battSample
	have := false
	for _, s := range samples {
		if s.at < from || s.at > to {
			continue
		}
		x, y := xOf(s.at), battGraphY(plot, s.pct)
		if have {
			dt := s.at - prev.at
			switch {
			case dt <= 0 || dt > battGraphGapSecs:
				m.segEnds = append(m.segEnds, len(m.line))
			case prev.flags&battFlagCable != 0:
				x0 := xOf(prev.at)
				if n := len(m.spans); n > 0 && m.spans[n-1].X+m.spans[n-1].W >= x0 {
					if w := x - m.spans[n-1].X + 1; w > m.spans[n-1].W {
						m.spans[n-1].W = w
					}
				} else {
					m.spans = append(m.spans, sdl.Rect{X: x0, Y: plot.Y, W: x - x0 + 1, H: plot.H})
				}
			}
		}
		m.line = append(m.line, sdl.Point{X: x, Y: y})
		m.line2 = append(m.line2, sdl.Point{X: x, Y: y + 1})
		prev, have = s, true
		m.points++
	}
	if have {
		m.segEnds = append(m.segEnds, len(m.line))
	}
}

// battGraphDrain is how fast the level fell, in points per hour, over the
// pairs of adjacent samples since since that were on battery with the screen
// awake (and, for game, both in a game). It needs a quarter of an hour of such
// pairs and two points of fall, or it gives none.
func battGraphDrain(samples []battSample, since int64, game bool) (float64, bool) {
	var drop, secs float64
	for i := 1; i < len(samples); i++ {
		a, b := samples[i-1], samples[i]
		dt := b.at - a.at
		if a.at < since || dt <= 0 || dt > battGraphGapSecs {
			continue
		}
		if (a.flags|b.flags)&(battFlagCable|battFlagAsleep) != 0 {
			continue
		}
		if game && a.flags&b.flags&battFlagGame == 0 {
			continue
		}
		drop += float64(int(a.pct) - int(b.pct))
		secs += float64(dt)
	}
	if secs < 15*60 || drop < 2 {
		return 0, false
	}
	return drop / (secs / 3600), true
}

// battGraphRate writes a drain: "9.5 %/h", "14 %/h".
func battGraphRate(r float64) string {
	prec := 0
	if r < 10 {
		prec = 1
	}
	return strconv.FormatFloat(r, 'f', prec, 64) + " %/h"
}

// battGraphFullText says when the battery was last full: at 100 %, or at 99 %
// on the cable (gauges that never quite report 100).
func battGraphFullText(samples []battSample, now int64) string {
	if n := len(samples); n > 0 {
		if last := samples[n-1]; last.flags&battFlagCable != 0 && last.pct >= 100 && now-last.at <= battGraphGapSecs {
			return "fully charged"
		}
	}
	keep := int64(battGraphKeep / time.Second)
	for i := len(samples) - 1; i >= 0; i-- {
		s := samples[i]
		if s.at > now+60 || s.at < now-keep {
			continue
		}
		if s.pct >= 100 || (s.pct >= 99 && s.flags&battFlagCable != 0) {
			ago := now - s.at
			if ago < 120 {
				return "last full charge just now"
			}
			return "last full charge " + batteryETADuration(batteryETAMinutes(time.Duration(ago)*time.Second), false) + " ago"
		}
	}
	if len(samples) > 0 && now-samples[0].at >= keep-3600 {
		return "no full charge in the last 2 days"
	}
	return "no full charge recorded yet"
}
