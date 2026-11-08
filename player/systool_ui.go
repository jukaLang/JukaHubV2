package main

// systool_ui.go — the glass pieces the system tool screens (Speed test,
// Benchmark, Hardware, Storage, Logs) are built from: the page frame with
// Home's header, stat tiles with a big value and a bar, a two-pixel history
// line, and an action button that names its pad button. Values are formatted
// when a sample arrives, not per frame, so a steady frame allocates nothing.

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// sysToolFonts are the four type sizes a tool page uses.
type sysToolFonts struct {
	label *ttf.Font // tile captions
	body  *ttf.Font // rows and sub lines
	value *ttf.Font // tile values
	big   *ttf.Font // the one headline number
}

// sysToolLoadFonts resolves the page's fonts, falling back to the theme's
// small font so a config without the roles still draws.
func sysToolLoadFonts(config *Config) (sysToolFonts, bool) {
	fallback, _ := getCachedFont(config, "small")
	pick := func(role string, size int) *ttf.Font {
		if f := getFontSized(config, role, size); f != nil {
			return f
		}
		return fallback
	}
	f := sysToolFonts{
		label: pick("small", 14),
		body:  pick("small", 17),
		value: pick("medium", 30),
		big:   pick("big", 60),
	}
	return f, f.label != nil && f.body != nil && f.value != nil && f.big != nil
}

// sysToolPageSpec is what a tool page's header says.
type sysToolPageSpec struct {
	Title       string
	Subtitle    string
	Icon        string
	Status      string
	StatusColor sdl.Color
}

// sysToolDrawPage paints Home's backdrop and the shared page header, and
// returns the content area between the header and the footer band.
func sysToolDrawPage(renderer *sdl.Renderer, config *Config, sceneName string, spec sysToolPageSpec) sdl.Rect {
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + 8
	bottom := screenHeight - bottomBarHeight(sceneName) - 16
	icon := spec.Icon
	headerBottom := UIPageHeaderDraw(renderer, config, UIPageHeader{
		X: margin, Y: top, W: screenWidth - 2*margin,
		Title:       spec.Title,
		TitleSize:   menuTitleSize,
		Subtitle:    spec.Subtitle,
		Status:      spec.Status,
		StatusColor: spec.StatusColor,
		Glyph: func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
			DrawHomeIcon(r, icon, box, false)
		},
	})
	y := headerBottom + 14
	return sdl.Rect{X: margin, Y: y, W: screenWidth - 2*margin, H: bottom - y}
}

// sysToolFinishPage draws the shared top bar and footer over the page.
func sysToolFinishPage(renderer *sdl.Renderer, config *Config) {
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
}

// sysToolCard paints a glass card, ringed in the accent while it is the part
// of the page that is busy.
func sysToolCard(renderer *sdl.Renderer, r sdl.Rect, active bool) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	drawCard(renderer, r.X, r.Y, r.W, r.H, homeTileRadius)
	if active {
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, homeTileRadius, 2, WithAlpha(glassAccent(), 210))
	}
}

// sysToolStat is one tile: a caption with its icon, a big value with its
// unit, a sub line and an optional bar.
type sysToolStat struct {
	Label  string
	Icon   string
	Value  string
	Unit   string
	Sub    string
	Frac   float64   // bar fill 0..1; negative draws no bar
	Color  sdl.Color // bar colour; zero means the accent
	Active bool      // ring the tile (the phase being measured)
	Dim    bool      // nothing measured yet, or not available here
}

// sysToolDrawStat paints one tile and returns the space left under its text,
// where a caller can draw a history line.
func sysToolDrawStat(renderer *sdl.Renderer, f sysToolFonts, r sdl.Rect, s sysToolStat) sdl.Rect {
	sysToolCard(renderer, r, s.Active)
	pad := int32(16)
	x := r.X + pad
	y := r.Y + pad - 2
	inner := r.W - 2*pad
	caption := strings.ToUpper(s.Label)
	if s.Icon != "" {
		badge := int32(28)
		box := drawGlassBadge(renderer, sdl.Rect{X: x, Y: y, W: badge, H: badge})
		DrawHomeIcon(renderer, s.Icon, box, false)
		lh := int32(f.label.Height())
		drawText(renderer, f.label, ellipsize(f.label, caption, inner-badge-10), x+badge+10, y+(badge-lh)/2, ColorTextTertiary(), textAlignLeft)
		y += badge + 8
	} else {
		drawText(renderer, f.label, ellipsize(f.label, caption, inner), x, y, ColorTextTertiary(), textAlignLeft)
		y += int32(f.label.Height()) + 6
	}
	ink := ColorTextPrimary()
	if s.Dim {
		ink = ColorTextTertiary()
	}
	value := ellipsize(f.value, s.Value, inner)
	vw, vh := measureText(f.value, value)
	drawText(renderer, f.value, value, x, y, ink, textAlignLeft)
	if s.Unit != "" && vw+8 < inner {
		_, uh := measureText(f.body, s.Unit)
		drawText(renderer, f.body, ellipsize(f.body, s.Unit, inner-vw-8), x+vw+8, y+vh-uh-3, ColorTextSecondary(), textAlignLeft)
	}
	y += vh
	if s.Sub != "" {
		drawText(renderer, f.body, ellipsize(f.body, s.Sub, inner), x, y, ColorTextSecondary(), textAlignLeft)
		y += int32(f.body.Height()) + 4
	}
	bottom := r.Y + r.H - pad
	if s.Frac >= 0 {
		col := s.Color
		if col.A == 0 {
			col = glassAccent()
		}
		barY := bottom - 6
		UIProgressBar(renderer, sdl.Rect{X: x, Y: barY, W: inner, H: 6}, s.Frac, glassA(18), col)
		bottom = barY - 8
	}
	return sdl.Rect{X: x, Y: y + 6, W: inner, H: bottom - (y + 6)}
}

// sysToolRingSize is how many samples a history line keeps.
const sysToolRingSize = 60

// sysToolRing is a fixed history of the latest samples.
type sysToolRing struct {
	vals [sysToolRingSize]float64
	n    int
	head int
}

func (r *sysToolRing) push(v float64) {
	r.vals[r.head] = v
	r.head = (r.head + 1) % sysToolRingSize
	if r.n < sysToolRingSize {
		r.n++
	}
}

// ordered copies the samples, oldest first, into dst and returns them.
func (r *sysToolRing) ordered(dst *[sysToolRingSize]float64) []float64 {
	start := (r.head - r.n + sysToolRingSize) % sysToolRingSize
	for i := 0; i < r.n; i++ {
		dst[i] = r.vals[(start+i)%sysToolRingSize]
	}
	return dst[:r.n]
}

// peak is the largest sample (0 when empty).
func (r *sysToolRing) peak() float64 {
	m := 0.0
	for i := 0; i < r.n; i++ {
		if v := r.vals[i]; v > m {
			m = v
		}
	}
	return m
}

// sysToolSparkline draws a history (oldest first) as a two-pixel line across
// r, scaled so top touches the upper edge and newest at the right edge.
func sysToolSparkline(renderer *sdl.Renderer, r sdl.Rect, samples []float64, capacity int, top float64, col sdl.Color) {
	if r.W < 8 || r.H < 8 || capacity < 2 || top <= 0 {
		return
	}
	setDrawColor(renderer, glassHairline())
	_ = renderer.DrawLine(r.X, r.Y+r.H, r.X+r.W, r.Y+r.H)
	if len(samples) < 2 {
		return
	}
	step := float64(r.W) / float64(capacity-1)
	x0 := float64(r.X+r.W) - step*float64(len(samples)-1)
	point := func(i int) (int32, int32) {
		v := samples[i]
		if v < 0 {
			v = 0
		}
		if v > top {
			v = top
		}
		return int32(x0 + step*float64(i)), r.Y + r.H - 1 - int32(math.Round(v/top*float64(r.H-3)))
	}
	soft := WithAlpha(col, col.A/2)
	px, py := point(0)
	for i := 1; i < len(samples); i++ {
		x, y := point(i)
		setDrawColor(renderer, col)
		_ = renderer.DrawLine(px, py, x, y)
		setDrawColor(renderer, soft)
		_ = renderer.DrawLine(px, py+1, x, y+1)
		px, py = x, y
	}
}

// sysToolAction draws an action button that names its pad button in a badge
// ("X  Run again") and returns its rect for clicks.
func sysToolAction(renderer *sdl.Renderer, f sysToolFonts, x, y int32, button, label string, primary bool) sdl.Rect {
	lw, lh := measureText(f.body, label)
	bw, bh := measureText(f.label, button)
	badge := int32(f.label.Height()) + 6
	badgeW := bw + 12
	if badgeW < badge {
		badgeW = badge
	}
	h := lh + 22
	w := 14 + badgeW + 10 + lw + 20
	r := sdl.Rect{X: x, Y: y, W: w, H: h}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if primary {
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, WithAlpha(glassAccent(), 56))
	}
	drawGlassSurface(renderer, r, h/2, glassState{Hovered: sysToolPointIn(r), NoLift: true})
	br := sdl.Rect{X: x + 14, Y: y + (h-badge)/2, W: badgeW, H: badge}
	fillRoundedRect(renderer, br.X, br.Y, br.W, br.H, badge/2, glassChipFill())
	strokeRoundedRect(renderer, br.X, br.Y, br.W, br.H, badge/2, 1, glassChipEdge())
	drawText(renderer, f.label, button, br.X+(br.W-bw)/2, br.Y+(badge-bh)/2, glassChipInk(), textAlignLeft)
	drawText(renderer, f.body, label, br.X+br.W+10, y+(h-lh)/2, ColorTextPrimary(), textAlignLeft)
	return r
}

// sysToolActionWidth is how wide sysToolAction will draw, for right-aligning.
func sysToolActionWidth(f sysToolFonts, button, label string) int32 {
	lw, _ := measureText(f.body, label)
	bw, _ := measureText(f.label, button)
	badge := int32(f.label.Height()) + 6
	badgeW := bw + 12
	if badgeW < badge {
		badgeW = badge
	}
	return 14 + badgeW + 10 + lw + 20
}

// sysToolDuration says an uptime the short way: "2d 4h", "3h 12m", "12m".
func sysToolDuration(d time.Duration) string {
	if d < time.Minute {
		return "under a minute"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

// sysToolHeatColor is green → amber → red as frac rises (busy CPU, full
// memory, a hot SoC, a full card).
func sysToolHeatColor(frac float64) sdl.Color {
	switch {
	case frac >= 0.9:
		return ColorDanger
	case frac >= 0.75:
		return ColorWarning
	}
	return ColorSuccess
}
