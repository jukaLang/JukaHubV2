package main

// toolssheet.go — the sheet a tools page opens over itself (a probe's result,
// a server's details, a Wi-Fi network's details) and the layout that turns a
// tool's plain-text report into aligned rows.
//
// The reports are written for a terminal: "Label:   value" pairs, tables
// padded with spaces, wrapped notes. The UI draws Inter, a proportional font,
// where space padding does not line anything up, so the text is classified
// once per result and laid out with measured columns: a shared label column
// for each run of pairs, measured columns for each run of table rows, and word
// wrapping (with hard breaks for long tokens) for everything else.

import (
	"regexp"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// toolsHubTextRun is one piece of a laid-out line; x is relative to the body.
type toolsHubTextRun struct {
	text  string
	x     int32
	color sdl.Color
}

// toolsHubTextLine is one drawn line. A blank line has no runs.
type toolsHubTextLine struct {
	runs []toolsHubTextRun
}

// toolsHubLineKind classifies one line of a report.
type toolsHubLineKind uint8

const (
	toolsHubLineBlank toolsHubLineKind = iota
	toolsHubLinePara
	toolsHubLineKV
	// toolsHubLineCont is an indented continuation of the value above it.
	toolsHubLineCont
	toolsHubLineTable
)

// toolsHubRawLine is one classified line before layout.
type toolsHubRawLine struct {
	kind  toolsHubLineKind
	label string
	value string
	cells []string
	text  string
}

var (
	// toolsHubKVLine is "Label: value" where the label reads like a field name:
	// it starts with a letter and stays short, so "64 bytes from 1.1.1.1: …" is
	// not mistaken for a pair.
	toolsHubKVLine = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^([A-Za-z][A-Za-z0-9 /&().-]{0,22}):\s+(\S.*)$`) })
	// toolsHubCellBreak splits a padded table row into its cells.
	toolsHubCellBreak = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\s{2,}`) })
)

// toolsHubClassify splits a report into classified lines.
func toolsHubClassify(text string) []toolsHubRawLine {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\t", "    ")
	lines := strings.Split(strings.TrimRight(text, "\n "), "\n")
	out := make([]toolsHubRawLine, 0, len(lines))
	prev := toolsHubLineBlank
	for _, raw := range lines {
		line := strings.TrimRight(raw, " \r")
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case trimmed == "":
			out = append(out, toolsHubRawLine{kind: toolsHubLineBlank})
		case (prev == toolsHubLineKV || prev == toolsHubLineCont) && indent >= 4 && !strings.Contains(trimmed, "  "):
			out = append(out, toolsHubRawLine{kind: toolsHubLineCont, value: trimmed})
		default:
			if m := toolsHubKVLine().FindStringSubmatch(line); m != nil {
				out = append(out, toolsHubRawLine{kind: toolsHubLineKV, label: strings.TrimSpace(m[1]), value: strings.TrimSpace(m[2])})
			} else if cells := toolsHubCellBreak().Split(trimmed, -1); len(cells) >= 2 {
				out = append(out, toolsHubRawLine{kind: toolsHubLineTable, cells: cells})
			} else {
				out = append(out, toolsHubRawLine{kind: toolsHubLinePara, text: trimmed})
			}
		}
		prev = out[len(out)-1].kind
	}
	return out
}

// toolsHubIsHeaderRow reports whether a table row is a column header: every
// cell is upper case ("IP  MAC  INTERFACE").
func toolsHubIsHeaderRow(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if c != strings.ToUpper(c) || !strings.ContainsAny(c, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			return false
		}
	}
	return true
}

// toolsHubTextWidth measures without the shared measure cache: wrapping tries
// many throwaway candidates, which must not evict the labels drawn each frame.
func toolsHubTextWidth(font *ttf.Font, text string) int32 {
	if font == nil || text == "" {
		return 0
	}
	w, _, err := font.SizeUTF8(text)
	if err != nil {
		return 0
	}
	return int32(w)
}

// toolsHubFitRunes is the longest prefix of runes that fits width, never less
// than one rune so a caller cutting a long token always makes progress.
func toolsHubFitRunes(font *ttf.Font, runes []rune, width int32) int {
	lo, hi := 1, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if toolsHubTextWidth(font, string(runes[:mid])) <= width {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// toolsHubWrap word-wraps text to width; a token wider than a line (a base64
// blob, a long URL) is cut into pieces instead of running off the sheet.
func toolsHubWrap(font *ttf.Font, text string, width int32) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if font == nil || width <= 0 || toolsHubTextWidth(font, text) <= width {
		return []string{text}
	}
	var lines []string
	cur := ""
	for _, word := range strings.Fields(text) {
		cand := word
		if cur != "" {
			cand = cur + " " + word
		}
		if toolsHubTextWidth(font, cand) <= width {
			cur = cand
			continue
		}
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
		runes := []rune(word)
		for len(runes) > 0 {
			if toolsHubTextWidth(font, string(runes)) <= width {
				cur = string(runes)
				break
			}
			cut := toolsHubFitRunes(font, runes, width)
			lines = append(lines, string(runes[:cut]))
			runes = runes[cut:]
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// toolsHubLayoutReport lays a report out for a body width. It runs once per
// result, never per frame.
func toolsHubLayoutReport(font *ttf.Font, text string, width int32) []toolsHubTextLine {
	raw := toolsHubClassify(text)
	out := make([]toolsHubTextLine, 0, len(raw)+4)
	labelInk, valueInk := ColorTextTertiary(), ColorTextPrimary()
	paraInk, noteInk := ColorTextSecondary(), ColorTextTertiary()
	const gutter = int32(18)
	for i := 0; i < len(raw); {
		switch raw[i].kind {
		case toolsHubLineBlank:
			out = append(out, toolsHubTextLine{})
			i++
		case toolsHubLineKV, toolsHubLineCont:
			j := i
			labelW := int32(0)
			for j < len(raw) && (raw[j].kind == toolsHubLineKV || raw[j].kind == toolsHubLineCont) {
				if raw[j].kind == toolsHubLineKV {
					if w := toolsHubTextWidth(font, raw[j].label); w > labelW {
						labelW = w
					}
				}
				j++
			}
			if limit := width * 2 / 5; labelW > limit {
				labelW = limit
			}
			valueX := labelW + gutter
			for k := i; k < j; k++ {
				for n, part := range toolsHubWrap(font, raw[k].value, width-valueX) {
					line := toolsHubTextLine{runs: make([]toolsHubTextRun, 0, 2)}
					if n == 0 && raw[k].kind == toolsHubLineKV {
						line.runs = append(line.runs, toolsHubTextRun{text: ellipsize(font, raw[k].label, labelW), color: labelInk})
					}
					line.runs = append(line.runs, toolsHubTextRun{text: part, x: valueX, color: valueInk})
					out = append(out, line)
				}
			}
			i = j
		case toolsHubLineTable:
			j := i
			var widths []int32
			for j < len(raw) && raw[j].kind == toolsHubLineTable {
				for c, cell := range raw[j].cells {
					w := toolsHubTextWidth(font, cell)
					if c >= len(widths) {
						widths = append(widths, w)
					} else if w > widths[c] {
						widths[c] = w
					}
				}
				j++
			}
			xs := make([]int32, len(widths))
			x := int32(0)
			for c := range widths {
				xs[c] = x
				x += widths[c] + gutter
			}
			header := toolsHubIsHeaderRow(raw[i].cells)
			for k := i; k < j; k++ {
				line := toolsHubTextLine{runs: make([]toolsHubTextRun, 0, len(raw[k].cells))}
				for c, cell := range raw[k].cells {
					if c >= len(xs) || xs[c] >= width {
						break
					}
					ink := valueInk
					if k == i && header {
						ink = labelInk
					}
					line.runs = append(line.runs, toolsHubTextRun{text: ellipsize(font, cell, width-xs[c]), x: xs[c], color: ink})
				}
				out = append(out, line)
			}
			i = j
		default:
			ink := paraInk
			lower := strings.ToLower(raw[i].text)
			switch {
			case strings.HasPrefix(lower, "read-only"), strings.HasPrefix(lower, "nothing left this device"):
				ink = noteInk
			case strings.HasPrefix(lower, "error"):
				ink = ToastErrorInk()
			}
			for _, part := range toolsHubWrap(font, raw[i].text, width) {
				out = append(out, toolsHubTextLine{runs: []toolsHubTextRun{{text: part, color: ink}}})
			}
			i++
		}
	}
	return out
}

// ── Sheet ─────────────────────────────────────────────────────────────────

// toolsHubPill is one action at the bottom of a sheet. Every pad action of a
// sheet also has a pill, so a finger can do what the buttons do.
type toolsHubPill struct {
	id     string
	label  string
	accent bool
	danger bool
}

// toolsHubSheet is a sheet's content. Pages rebuild it when what it shows
// changes, not every frame.
type toolsHubSheet struct {
	open     bool
	title    string
	subtitle string
	subInk   sdl.Color
	icon     string
	text     string
	// layout, when set, lays the body out instead of the report parser: the
	// Wi-Fi diagnostics colour every value by its state.
	layout func(font *ttf.Font, width int32) []toolsHubTextLine
	lines  []toolsHubTextLine
	// layoutW is the width lines were laid out for; a resize lays them out
	// again.
	layoutW int32
	// busy, when set, is the working line drawn with a spinner.
	busy  string
	empty string
	pills []toolsHubPill
	// focus is the pill the pad is on.
	focus  int
	hint   string
	scroll int
	// Last frame's geometry.
	card      sdl.Rect
	body      sdl.Rect
	pillRects []sdl.Rect
	fits      int
}

// toolsHubSheetFont is the report's type size.
func toolsHubSheetFont(config *Config) *ttf.Font {
	if f := getFontSized(config, "small", 17); f != nil {
		return f
	}
	f, _ := getCachedFont(config, "small")
	return f
}

// setText replaces the report and lays it out again on the next frame.
func (s *toolsHubSheet) setText(text string) {
	if text == s.text && s.lines != nil && s.layout == nil {
		return
	}
	s.layout = nil
	s.text = text
	s.lines = nil
	s.layoutW = 0
}

// scrollBy moves the report, clamped to what the last frame could show.
func (s *toolsHubSheet) scrollBy(delta int) bool {
	maxScroll := len(s.lines) - s.fits
	if maxScroll < 0 {
		maxScroll = 0
	}
	next := s.scroll + delta
	if next > maxScroll {
		next = maxScroll
	}
	if next < 0 {
		next = 0
	}
	if next == s.scroll {
		return false
	}
	s.scroll = next
	return true
}

// movePill walks the pill row.
func (s *toolsHubSheet) movePill(dx int) {
	if len(s.pills) == 0 {
		return
	}
	next := s.focus + dx
	if next < 0 || next >= len(s.pills) {
		return
	}
	s.focus = next
	PlayNavSound()
}

// focusedPill is the pill the pad is on, or "".
func (s *toolsHubSheet) focusedPill() string {
	if s.focus < 0 || s.focus >= len(s.pills) {
		return ""
	}
	return s.pills[s.focus].id
}

// hitPill is the pill under a point, or "".
func (s *toolsHubSheet) hitPill(x, y int32) string {
	for i, r := range s.pillRects {
		if i < len(s.pills) && r.W > 0 && hitTouchTarget(r, x, y) {
			return s.pills[i].id
		}
	}
	return ""
}

// toolsHubDrawSheet paints a sheet over the page: the shared scrim and glass
// card, the page header, the report (or the working line, or the empty line),
// the pill row and the controller hints.
func toolsHubDrawSheet(renderer *sdl.Renderer, config *Config, s *toolsHubSheet) {
	if renderer == nil || s == nil || !s.open {
		return
	}
	font := toolsHubSheetFont(config)
	pillFont, _ := getCachedFont(config, "small")
	hintFont := getFontSized(config, "small", 15)
	if font == nil {
		return
	}
	if pillFont == nil {
		pillFont = font
	}
	if hintFont == nil {
		hintFont = font
	}

	cardW := int32(860)
	if limit := screenWidth - 2*scaleX(homeSideMargin, screenWidth); cardW > limit {
		cardW = limit
	}
	cardH := screenHeight - 2*(headerBarHeight()/2+20)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	s.card = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)

	header := overlayPageHeader(card, s.title, s.subtitle, s.icon)
	header.SubtitleColor = s.subInk
	headerH := UIPanelHeaderBand(config, header, 10)
	UIPageHeaderDraw(renderer, config, header)

	lineH := int32(font.Height()) + 4
	hintH := toolsHubFontH(hintFont, 18) + 14
	pillH := toolsHubFontH(pillFont, 24) + 14
	if pillH < 38 {
		pillH = 38
	}
	pillRowH := int32(0)
	if len(s.pills) > 0 {
		pillRowH = pillH + 14
	}
	body := sdl.Rect{
		X: card.X + OverlayPadX,
		Y: card.Y + headerH,
		W: card.W - 2*OverlayPadX,
		H: card.H - headerH - pillRowH - hintH,
	}
	if body.H < lineH {
		body.H = lineH
	}
	s.body = body
	switch {
	case s.layout != nil:
		if s.layoutW != body.W || s.lines == nil {
			s.lines = s.layout(font, body.W-10)
			s.layoutW = body.W
		}
	case s.text == "":
		s.lines = nil
		s.layoutW = body.W
	case s.layoutW != body.W || s.lines == nil:
		s.lines = toolsHubLayoutReport(font, s.text, body.W-10)
		s.layoutW = body.W
	}

	y := body.Y
	bodyBottom := body.Y + body.H
	if s.busy != "" {
		renderSpinner(renderer, body.X+9, y+lineH/2, 8, glassAccent())
		drawText(renderer, font, ellipsize(font, s.busy, body.W-28), body.X+26, y+2, ColorTextSecondary(), textAlignLeft)
		y += lineH + 6
	}
	avail := int((bodyBottom - y) / lineH)
	if avail < 1 {
		avail = 1
	}
	s.fits = avail
	if maxScroll := len(s.lines) - avail; s.scroll > maxScroll {
		s.scroll = maxScroll
	}
	if s.scroll < 0 {
		s.scroll = 0
	}
	if len(s.lines) == 0 && s.busy == "" && s.empty != "" {
		ew, eh := measureText(font, s.empty)
		if ew > body.W {
			drawText(renderer, font, ellipsize(font, s.empty, body.W), body.X, body.Y+(body.H-eh)/2, ColorTextTertiary(), textAlignLeft)
		} else {
			drawText(renderer, font, s.empty, body.X+(body.W-ew)/2, body.Y+(body.H-eh)/2, ColorTextTertiary(), textAlignLeft)
		}
	}
	for i := s.scroll; i < len(s.lines) && y+lineH <= bodyBottom; i++ {
		for _, run := range s.lines[i].runs {
			drawText(renderer, font, run.text, body.X+run.x, y, run.color, textAlignLeft)
		}
		y += lineH
	}
	if len(s.lines) > avail {
		maxScroll := len(s.lines) - avail
		drawScrollbar(renderer, card.X+card.W-12, body.Y, 4, body.H,
			float64(avail)/float64(len(s.lines)), float64(s.scroll)/float64(maxScroll))
	}

	// Pills, centred, with the focused one ringed.
	if cap(s.pillRects) < len(s.pills) {
		s.pillRects = make([]sdl.Rect, len(s.pills))
	}
	s.pillRects = s.pillRects[:len(s.pills)]
	if len(s.pills) > 0 {
		const gap = int32(12)
		total := int32(0)
		for i, p := range s.pills {
			tw, _ := measureText(pillFont, p.label)
			s.pillRects[i] = sdl.Rect{W: tw + 40, H: pillH}
			total += tw + 40
		}
		total += gap * int32(len(s.pills)-1)
		x := card.X + (card.W-total)/2
		py := card.Y + card.H - hintH - pillH - 4
		for i, p := range s.pills {
			r := s.pillRects[i]
			r.X, r.Y = x, py
			s.pillRects[i] = r
			tint := sdl.Color{}
			switch {
			case p.danger:
				tint = WithAlpha(ColorToastError, 60)
			case p.accent:
				tint = WithAlpha(glassAccent(), 60)
			}
			hovered := pointInRect(mouseX, mouseY, r)
			drawGlassButton(renderer, nil, r, p.label, pillFont, tint, glassState{
				Focused: i == s.focus,
				Hovered: hovered && i != s.focus,
			})
			x += r.W + gap
		}
	}

	if s.hint != "" && hintFont != nil {
		hint := ellipsize(hintFont, s.hint, card.W-2*OverlayPadX)
		hw, hh := measureText(hintFont, hint)
		drawText(renderer, hintFont, hint, card.X+(card.W-hw)/2, card.Y+card.H-hintH+(hintH-hh)/2-2, ColorTextTertiary(), textAlignLeft)
	}
}
