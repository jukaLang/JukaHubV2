package main

// terminal_ui.go — how the Terminal looks: Home's glass page with a console
// card for the output, a prompt field with a real caret, and the history
// picker. State and input are in terminal.go.
//
// The caret is a thin bar at the true insertion point: the text before the
// cursor is measured with the font it is drawn in, so it lands between the
// right two characters for "é", "ё" or a wide "W" alike. It blinks 530 ms on /
// 530 ms off, is solid while typing (every edit restarts the blink) and is
// hidden whenever the prompt does not have focus.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// termOutputSize is the console's type size in design pixels: small
	// enough for a useful screenful on the handheld, large enough to read at
	// arm's length.
	termOutputSize = 17
	// termUISize is the prompt chip, buttons and picker rows.
	termUISize = 16
	// termWrapMaxRows caps how many rows one output line may wrap to; the
	// rest of a minified megabyte line is cut with an ellipsis.
	termWrapMaxRows = 40
)

// Hit boxes of the last frame, in logical screen coordinates. renderTerminal
// fills them, so clicks always match what is drawn.
var (
	termConsoleRect  sdl.Rect
	termInputRect    sdl.Rect
	termRunRect      sdl.Rect
	termKeyboardRect sdl.Rect
	termNewRect      sdl.Rect
	// termInputTextX and termInputFont are where and with what the prompt
	// text was drawn, so a click can be turned back into a caret position.
	termInputTextX int32
	termInputFont  *ttf.Font
)

// termLabelCache keeps the strings the header and the pill show, so a steady
// frame formats nothing.
var termLabelCache struct {
	cwd       string
	cwdLabel  string
	cwdBase   string
	runSecs   int
	runLabel  string
	newCount  int
	newLabel  string
	histCount int
	histTotal int
	histQuery string
	histLabel string
}

var termBlankRow = []string{""}

// termOutputFont is the console's font.
func termOutputFont(config *Config) *ttf.Font {
	if f := getFontSized(config, "small", termOutputSize); f != nil {
		return f
	}
	f, _ := getCachedFont(config, "small")
	return f
}

// termUIFont is the font of the prompt's chrome and the picker.
func termUIFont(config *Config) *ttf.Font {
	if f := getFontSized(config, "small", termUISize); f != nil {
		return f
	}
	f, _ := getCachedFont(config, "small")
	return f
}

// termTextWidth measures text as drawText will draw it.
func termTextWidth(font *ttf.Font, text string) int32 {
	if font == nil || text == "" {
		return 0
	}
	w, _ := measureText(font, sanitizeCached(font, text))
	return w
}

// termWrap breaks one output line into rows no wider than maxW: at a space when
// one is near the end of the row, mid-word otherwise (a path or a URL has no
// spaces to break at). The measuring is done once per line and width.
func termWrap(font *ttf.Font, text string, maxW int32) []string {
	if text == "" || font == nil {
		return termBlankRow
	}
	text = sanitizeForFont(font, text)
	width := func(s string) int32 {
		w, _, err := font.SizeUTF8(s)
		if err != nil {
			return 0
		}
		return int32(w)
	}
	if maxW <= 0 || width(text) <= maxW {
		return []string{text}
	}
	runes := []rune(text)
	var rows []string
	for len(runes) > 0 {
		if len(rows) == termWrapMaxRows-1 {
			rows = append(rows, ellipsize(font, string(runes), maxW))
			break
		}
		if width(string(runes)) <= maxW {
			rows = append(rows, string(runes))
			break
		}
		// The longest prefix that fits, found by halving: a 4 KB line costs a
		// dozen measurements per row instead of one per character.
		lo, hi := 1, len(runes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if width(string(runes[:mid])) <= maxW {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		cut := lo
		for i := cut - 1; i > cut/2; i-- {
			if runes[i] == ' ' {
				cut = i + 1
				break
			}
		}
		rows = append(rows, strings.TrimRight(string(runes[:cut]), " "))
		runes = runes[cut:]
	}
	return rows
}

// termRowsFor returns a line's rows for this font and width, wrapping only
// when either changed since the last frame.
func termRowsFor(l *termLine, font *ttf.Font, width int32) []string {
	if l.rows != nil && l.rowsFont == font && l.rowsW == width {
		return l.rows
	}
	l.rows = termWrap(font, l.text, width)
	l.rowsFont, l.rowsW = font, width
	return l.rows
}

// termShortPath shows a directory the way a shell prompt does: the home folder
// as ~, and only the tail of a path too long for the header.
func termShortPath(path string, maxRunes int) string {
	if path == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == home {
			path = "~"
		} else if strings.HasPrefix(path, home+string(filepath.Separator)) {
			path = "~" + path[len(home):]
		}
	}
	if utf8.RuneCountInString(path) <= maxRunes || maxRunes < 4 {
		return path
	}
	runes := []rune(path)
	return "…" + string(runes[len(runes)-(maxRunes-1):])
}

// termStatus is the header's right-hand read-out: how long a command has been
// running, or the folder the terminal is in.
func termStatus() (string, sdl.Color) {
	if j := term.job; j != nil {
		secs := int(time.Since(j.started) / time.Second)
		if secs != termLabelCache.runSecs || termLabelCache.runLabel == "" {
			termLabelCache.runSecs = secs
			termLabelCache.runLabel = fmt.Sprintf("Running · %d:%02d", secs/60, secs%60)
		}
		return termLabelCache.runLabel, glassAccent()
	}
	termRefreshCwdLabels()
	return termLabelCache.cwdLabel, ColorTextTertiary()
}

// termRefreshCwdLabels re-derives the folder labels when the folder changes.
func termRefreshCwdLabels() {
	if term.cwd == termLabelCache.cwd && termLabelCache.cwdBase != "" {
		return
	}
	termLabelCache.cwd = term.cwd
	termLabelCache.cwdLabel = termShortPath(term.cwd, 44)
	base := filepath.Base(term.cwd)
	if term.cwd == "" || base == "." || base == string(filepath.Separator) {
		base = termShortPath(term.cwd, 16)
	}
	if home, err := os.UserHomeDir(); err == nil && term.cwd == home {
		base = "~"
	}
	if base == "" {
		base = "~"
	}
	termLabelCache.cwdBase = base
}

// termFooterHints is the footer row for the Terminal: what the pad does right
// now, so X reads Stop while a command runs.
func termFooterHints() []FooterHint {
	// On a keyboard the terminal is typed into, so its keys are its own
	// (handleTermInput, termHistoryKey), not the pad's names translated.
	if hintsUseKeyboard() {
		if termHist.open {
			return []FooterHint{
				{Button: "Up/Down", Label: "Choose"},
				{Button: "Enter", Label: "Insert"},
				{Button: "Ctrl+Enter", Label: "Run"},
				{Button: "Del", Label: "Delete"},
				{Button: "Esc", Label: "Close"},
			}
		}
		stop := FooterHint{Button: "Ctrl+L", Label: "Clear"}
		if term.job != nil {
			stop = FooterHint{Button: "Ctrl+C", Label: "Stop"}
		}
		return []FooterHint{
			{Button: "Enter", Label: "Run"},
			{Button: "Esc", Label: "Back"},
			stop,
			{Button: "F7", Label: "History"},
			{Button: "Tab", Label: "Complete"},
			{Button: "PgUp/PgDn", Label: "Scroll"},
		}
	}
	if termHist.open {
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose"},
			{Button: "A", Label: "Insert"},
			{Button: "B", Label: "Close"},
			{Button: "Start", Label: "Run"},
			{Button: "X", Label: "Delete"},
		}
	}
	x := FooterHint{Button: "X", Label: "Clear"}
	if term.job != nil {
		x = FooterHint{Button: "X", Label: "Stop"}
	}
	return []FooterHint{
		{Button: "D-Pad", Label: "Scroll"},
		{Button: "A", Label: "Keyboard"},
		{Button: "B", Label: "Back"},
		{Button: "Start", Label: "Run"},
		x,
		{Button: "Y", Label: "History"},
		{Button: "L1/R1", Label: "Page"},
	}
}

// termCaretOn reports whether the caret is drawn this frame.
func termCaretOn(now time.Time) bool {
	if !termPromptFocused() {
		return false
	}
	return (now.Sub(term.caretAt)/termCaretBlink)%2 == 0
}

func renderTerminal(renderer *sdl.Renderer, config *Config) {
	// Home's backdrop, the one every screen stands on (ui_glass.go).
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	// A background command's output joins the scrollback before it is drawn.
	termDrainJob()

	outFont := termOutputFont(config)
	uiFont := termUIFont(config)
	if outFont == nil || uiFont == nil {
		renderStatusBar(renderer, config)
		renderFooter(renderer, config)
		return
	}

	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + 8
	bottom := screenHeight - bottomBarHeight("Terminal") - 16
	contentW := screenWidth - 2*margin

	status, statusColor := termStatus()
	headerBottom := UIPageHeaderDraw(renderer, config, UIPageHeader{
		X: margin, Y: top, W: contentW,
		Title:       "Terminal",
		TitleSize:   menuTitleSize,
		Status:      status,
		StatusColor: statusColor,
		Glyph: func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
			DrawHomeIcon(r, "terminal", box, false)
		},
	})

	promptH := int32(outFont.Height()) + 24
	if promptH < 48 {
		promptH = 48
	}
	prompt := sdl.Rect{X: margin, Y: bottom - promptH, W: contentW, H: promptH}
	console := sdl.Rect{X: margin, Y: headerBottom + 12, W: contentW, H: prompt.Y - 12 - (headerBottom + 12)}
	if console.H < 60 {
		console.H = 60
	}
	termConsoleRect = console
	renderTermConsole(renderer, outFont, uiFont, console)
	renderTermPrompt(renderer, config, outFont, uiFont, prompt)

	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	if termHist.open {
		renderTermHistorySheet(renderer, config, uiFont)
	}
}

// renderTermConsole draws the scrollback in its glass card: wrapped rows,
// coloured by what they are, a scrollbar when there is more, the running
// command's live line and the "new output" pill while reading older lines.
func renderTermConsole(renderer *sdl.Renderer, font, uiFont *ttf.Font, card sdl.Rect) {
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, homeTileRadius, glassFill())
	strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, homeTileRadius, 1, glassEdge())

	padX, padY := int32(18), int32(12)
	scrollW := int32(10)
	text := sdl.Rect{X: card.X + padX, Y: card.Y + padY, W: card.W - 2*padX - scrollW, H: card.H - 2*padY}
	lineH := int32(font.Height()) + 2
	viewRows := int(text.H / lineH)
	if viewRows < 1 {
		viewRows = 1
	}

	// A running command's unfinished line takes the bottom row while the
	// view follows the output.
	live := term.job != nil && term.liveLine != "" && term.scrollY == 0
	rowsAvail := viewRows
	if live && rowsAvail > 1 {
		rowsAvail--
	}

	// How many lines from the top fill the console: that is how far back the
	// output can scroll (termMaxScroll).
	fit, used := 0, 0
	for fit < len(term.lines) {
		n := len(termRowsFor(&term.lines[fit], font, text.W))
		if used > 0 && used+n > viewRows {
			break
		}
		used += n
		fit++
		if used >= viewRows {
			break
		}
	}
	term.fitTop = fit
	if max := termMaxScroll(); term.scrollY > max {
		term.scrollY = max
	}

	end := len(term.lines) - term.scrollY
	if end < 0 {
		end = 0
	}
	start, rows := end, 0
	for start > 0 {
		n := len(termRowsFor(&term.lines[start-1], font, text.W))
		if rows > 0 && rows+n > rowsAvail {
			break
		}
		rows += n
		start--
		if rows >= rowsAvail {
			break
		}
	}
	term.pageLines = end - start
	if term.pageLines < 1 {
		term.pageLines = 1
	}

	y := text.Y
	if start > 0 || rows > rowsAvail {
		// Older lines are above: the newest shown line sits on the last row.
		y = text.Y + int32(rowsAvail-rows)*lineH
	}
	primary := WithAlpha(ColorTextPrimary(), 235)
	secondary := ColorTextSecondary()
	danger := DangerInk()
	accent := glassAccent()
	promptW := termTextWidth(font, termPrompt)
	renderWithClip(renderer, text.X, text.Y, text.W+scrollW, text.H, func(r *sdl.Renderer) {
		for i := start; i < end; i++ {
			line := &term.lines[i]
			col := primary
			switch line.kind {
			case termKindError:
				col = danger
			case termKindSystem:
				col = secondary
			}
			for ri, row := range line.rows {
				if line.kind == termKindEcho && ri == 0 && strings.HasPrefix(row, termPrompt) {
					drawText(r, font, termPrompt, text.X, y, accent, textAlignLeft)
					drawText(r, font, row[len(termPrompt):], text.X+promptW, y, primary, textAlignLeft)
				} else {
					drawText(r, font, row, text.X, y, col, textAlignLeft)
				}
				y += lineH
			}
		}
		if live && y+lineH <= text.Y+text.H+lineH/2 {
			// Right under the last line, where the next line will appear.
			drawText(r, font, ellipsize(font, term.liveLine, text.W), text.X, y, secondary, textAlignLeft)
		}
	})

	// The scrollbar: the thumb is the share of the buffer on screen, at the
	// top when the oldest line is showing.
	total := len(term.lines)
	if max := termMaxScroll(); max > 0 && total > 0 {
		thumb := float64(term.pageLines) / float64(total)
		pos := 1 - float64(term.scrollY)/float64(max)
		drawScrollbar(renderer, card.X+card.W-padX/2-4, text.Y, 4, text.H, thumb, pos)
	}

	// A command is running: a slim bar slides along the card's top edge, so
	// the screen never looks idle while output is pending.
	if term.job != nil {
		barW := card.W / 5
		period := int64(1600)
		phase := time.Now().UnixMilli() % period
		travel := card.W - 2*homeTileRadius - barW
		if travel < 0 {
			travel = 0
		}
		x := card.X + homeTileRadius + int32(int64(travel)*phase/period)
		fillRoundedRect(renderer, x, card.Y+1, barW, 3, 1, WithAlpha(accent, 200))
	}

	// New output arrived while the user was reading older lines.
	termNewRect = sdl.Rect{}
	if term.unseen > 0 && term.scrollY > 0 {
		if term.unseen != termLabelCache.newCount || termLabelCache.newLabel == "" {
			termLabelCache.newCount = term.unseen
			if term.unseen == 1 {
				termLabelCache.newLabel = "1 new line"
			} else {
				termLabelCache.newLabel = fmt.Sprintf("%d new lines", term.unseen)
			}
		}
		tw, th := measureText(uiFont, termLabelCache.newLabel)
		pillH := th + 12
		pillW := tw + 32
		pill := sdl.Rect{X: card.X + (card.W-pillW)/2, Y: card.Y + card.H - pillH - 10, W: pillW, H: pillH}
		hover := pointInRect(mouseX, mouseY, pill)
		fill := glassSheetFill()
		if hover {
			fill = glassRaisedFill()
		}
		fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pillH/2, fill)
		strokeRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pillH/2, 1, WithAlpha(accent, 200))
		drawText(renderer, uiFont, termLabelCache.newLabel, pill.X+16, pill.Y+(pillH-th)/2, ColorTextPrimary(), textAlignLeft)
		termNewRect = pill
	}
}

// renderTermPrompt draws the command line: the folder chip, the text with its
// caret, the keyboard button and Run (or Stop while a command runs).
func renderTermPrompt(renderer *sdl.Renderer, config *Config, font, uiFont *ttf.Font, field sdl.Rect) {
	focused := termPromptFocused()
	hover := pointInRect(mouseX, mouseY, field)
	drawGlassSurface(renderer, field, glassControlRadius, glassState{Focused: focused, Hovered: hover && !focused, NoLift: true})
	termInputRect = field

	pad := int32(8)
	innerH := field.H - 2*pad

	// Run / Stop, at the right end.
	running := term.job != nil
	label := "Run"
	style := UIButtonStyle{Variant: UIButtonPrimary, Font: uiFont, Pill: true, HideFocusRing: true}
	disabled := false
	if running {
		label = "Stop"
		style.Variant = UIButtonDanger
	} else if strings.TrimSpace(term.input) == "" {
		style.Variant = UIButtonSecondary
		disabled = true
	}
	lw, _ := measureText(uiFont, label)
	runW := lw + 44
	if runW < 88 {
		runW = 88
	}
	runRect := sdl.Rect{X: field.X + field.W - pad - runW, Y: field.Y + pad, W: runW, H: innerH}
	UIButton(renderer, config, runRect, label, style, UIButtonState{
		Hovered:  pointInRect(mouseX, mouseY, runRect),
		Disabled: disabled,
	})
	termRunRect = runRect

	// The on-screen keyboard, for a mouse or touch user.
	keyRect := sdl.Rect{X: runRect.X - 8 - innerH, Y: runRect.Y, W: innerH, H: innerH}
	drawGlassSurface(renderer, keyRect, innerH/2, glassState{Hovered: pointInRect(mouseX, mouseY, keyRect), NoLift: true})
	DrawHomeIcon(renderer, "keyboard", insetRect(keyRect, innerH/5), false)
	termKeyboardRect = keyRect

	// The folder the command runs in, as a chip at the left end.
	termRefreshCwdLabels()
	chipH := innerH - 4
	chipX := field.X + pad + 2
	chipY := field.Y + (field.H-chipH)/2
	icon := chipH - 12
	base := ellipsize(uiFont, termLabelCache.cwdBase, 170)
	bw, bh := measureText(uiFont, base)
	chipW := 10 + icon + 8 + bw + 12
	fillRoundedRect(renderer, chipX, chipY, chipW, chipH, chipH/2, glassA(16))
	strokeRoundedRect(renderer, chipX, chipY, chipW, chipH, chipH/2, 1, glassA(26))
	DrawHomeIcon(renderer, "folder", sdl.Rect{X: chipX + 10, Y: chipY + (chipH-icon)/2, W: icon, H: icon}, false)
	drawText(renderer, uiFont, base, chipX+10+icon+8, chipY+(chipH-bh)/2, ColorTextSecondary(), textAlignLeft)

	// The command text, scrolled sideways so the caret is always in view.
	area := sdl.Rect{X: chipX + chipW + 12, Y: field.Y + 4, W: keyRect.X - 12 - (chipX + chipW + 12), H: field.H - 8}
	if area.W < 40 {
		area.W = 40
	}
	fh := int32(font.Height())
	textY := field.Y + (field.H-fh)/2
	termInputFont = font
	if term.input == "" {
		term.inputScroll = 0
		termInputTextX = area.X
		placeholder := "Type a command — Enter runs it"
		if !hintsUseKeyboard() {
			placeholder = "Type a command — A opens the keyboard"
		}
		if running {
			placeholder = "A command is running — X stops it"
			if hintsUseKeyboard() {
				placeholder = "A command is running — Ctrl+C stops it"
			}
		}
		drawText(renderer, font, ellipsize(font, placeholder, area.W), area.X, textY, ColorTextTertiary(), textAlignLeft)
	} else {
		term.cursor = termClampCursor(term.cursor)
		caretPx := termTextWidth(font, term.input[:term.cursor])
		totalPx := termTextWidth(font, term.input)
		edge := area.W - 6
		if caretPx-term.inputScroll > edge {
			term.inputScroll = caretPx - edge
		}
		if caretPx-term.inputScroll < 0 {
			term.inputScroll = caretPx - area.W/3
		}
		if totalPx-term.inputScroll < edge {
			term.inputScroll = totalPx - edge
		}
		if term.inputScroll < 0 {
			term.inputScroll = 0
		}
		termInputTextX = area.X - term.inputScroll
		renderWithClip(renderer, area.X, area.Y, area.W, area.H, func(r *sdl.Renderer) {
			drawText(r, font, term.input, area.X-term.inputScroll, textY, ColorTextPrimary(), textAlignLeft)
		})
	}

	if termCaretOn(time.Now()) {
		caretPx := int32(0)
		if term.input != "" {
			caretPx = termTextWidth(font, term.input[:termClampCursor(term.cursor)])
		}
		cx := area.X + caretPx - term.inputScroll
		if cx >= area.X-1 && cx <= area.X+area.W {
			caretW := scaleX(2, screenWidth)
			if caretW < 2 {
				caretW = 2
			}
			accent := glassAccent()
			_ = renderer.SetDrawColor(accent.R, accent.G, accent.B, 255)
			_ = renderer.FillRect(&sdl.Rect{X: cx, Y: textY + 1, W: caretW, H: fh - 2})
		}
	}
}

// termCursorAtX turns a click on the prompt into a caret position: the rune
// boundary closest to the click.
func termCursorAtX(mx int32) int {
	font := termInputFont
	if font == nil || term.input == "" {
		return len(term.input)
	}
	rel := mx - termInputTextX
	if rel <= 0 {
		return 0
	}
	bounds := make([]int, 0, utf8.RuneCountInString(term.input)+1)
	for i := range term.input {
		bounds = append(bounds, i)
	}
	bounds = append(bounds, len(term.input))
	// The first boundary at or past the click, then whichever of it and the
	// one before is nearer.
	k := sort.Search(len(bounds), func(i int) bool {
		return termTextWidth(font, term.input[:bounds[i]]) >= rel
	})
	if k >= len(bounds) {
		return len(term.input)
	}
	if k > 0 {
		before := termTextWidth(font, term.input[:bounds[k-1]])
		after := termTextWidth(font, term.input[:bounds[k]])
		if rel-before < after-rel {
			return bounds[k-1]
		}
	}
	return bounds[k]
}

// renderTermHistorySheet draws the history picker over a dimmed screen.
func renderTermHistorySheet(renderer *sdl.Renderer, config *Config, font *ttf.Font) {
	drawOverlayScrim(renderer)
	margin := scaleX(homeSideMargin, screenWidth)
	w := screenWidth - 2*margin
	if w > 760 {
		w = 760
	}
	top := headerBarHeight() + 12
	h := screenHeight - top - 24
	if h > 600 {
		h = 600
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: top, W: w, H: h}
	drawOverlayCard(renderer, card)
	termHist.card = card

	total := len(term.history)
	if len(termHist.matches) != termLabelCache.histCount || total != termLabelCache.histTotal ||
		termHist.filter != termLabelCache.histQuery || termLabelCache.histLabel == "" {
		termLabelCache.histCount, termLabelCache.histTotal = len(termHist.matches), total
		termLabelCache.histQuery = termHist.filter
		switch {
		case termHist.filter != "":
			termLabelCache.histLabel = fmt.Sprintf("“%s” · %d of %d", termHist.filter, len(termHist.matches), total)
		case total == 1:
			termLabelCache.histLabel = "1 command · newest first"
		default:
			termLabelCache.histLabel = fmt.Sprintf("%d commands · newest first", total)
		}
	}
	header := overlayPageHeader(card, "Command history", termLabelCache.histLabel, "terminal")
	if !P().IsHandheld() && termHist.filter == "" {
		header.Status = "Type to filter"
	}
	y := UIPageHeaderDraw(renderer, config, header) + 12

	hint := hintLine(FooterHint{Button: "A", Label: "Insert"}, FooterHint{Button: "Start", Label: "Run"},
		FooterHint{Button: "X", Label: "Delete"}, FooterHint{Button: "B", Label: "Close"})
	hw, hh := measureText(font, hint)
	hintY := card.Y + card.H - hh - 16
	drawText(renderer, font, hint, card.X+(card.W-hw)/2, hintY, ColorTextTertiary(), textAlignLeft)

	list := sdl.Rect{X: card.X + OverlayPadX, Y: y, W: card.W - 2*OverlayPadX, H: hintY - 12 - y}
	termHist.rows = termHist.rows[:0]
	termHist.rowsFirst = termHist.first
	if len(termHist.matches) == 0 {
		termHist.visible = 1
		title, body := "No commands yet", "Commands you run are kept here, newest first."
		if termHist.filter != "" {
			title, body = "No matches", "Nothing in the history contains that text."
		}
		UIEmptyState(renderer, config, list, "@terminal", title, body)
		return
	}
	rowH := int32(font.Height()) + 22
	gap := int32(6)
	visible := int((list.H + gap) / (rowH + gap))
	if visible < 1 {
		visible = 1
	}
	termHist.visible = visible
	if termHist.cursor < termHist.first {
		termHist.first = termHist.cursor
	}
	if termHist.cursor >= termHist.first+visible {
		termHist.first = termHist.cursor - visible + 1
	}
	if maxFirst := len(termHist.matches) - visible; termHist.first > maxFirst {
		termHist.first = maxFirst
	}
	if termHist.first < 0 {
		termHist.first = 0
	}
	termHist.rowsFirst = termHist.first
	for i := termHist.first; i < len(termHist.matches) && i < termHist.first+visible; i++ {
		idx := termHist.matches[i]
		if idx < 0 || idx >= len(term.history) {
			continue
		}
		row := sdl.Rect{X: list.X, Y: list.Y + int32(i-termHist.first)*(rowH+gap), W: list.W, H: rowH}
		UIListRow(renderer, row, glassRowRadius, i == termHist.cursor, pointInRect(mouseX, mouseY, row))
		text := ellipsize(font, term.history[idx], row.W-32)
		_, th := measureText(font, text)
		drawText(renderer, font, text, row.X+16, row.Y+(row.H-th)/2, ColorTextPrimary(), textAlignLeft)
		termHist.rows = append(termHist.rows, row)
	}
	if len(termHist.matches) > visible {
		thumb := float64(visible) / float64(len(termHist.matches))
		pos := float64(termHist.first) / float64(len(termHist.matches)-visible)
		drawScrollbar(renderer, card.X+card.W-OverlayPadX/2-2, list.Y, 4, list.H, thumb, pos)
	}
}
