package main

// gamemanual_ingame.go — Manual in the in-game menu (gamemenu.go): the
// game's controls and its text guides, read with the game paused under them.
//
// The row shows for a game the library started when it has a control
// layout's chart (ctrlLayoutForGame) or any document (gamemanual.go). The
// launch is noted through gameLaunchRegisterStarted — the game and its
// process — and the game's documents are looked for at that moment, so the
// menu answers from memory; the row only counts while that process is still
// one of the screen's children (another program the library did not start
// has no manual here).
//
// The page shows parts one at a time: Controls first (the same chart or words
// as the game page's Controls, gamemanual_ui.go), then each text or Markdown
// guide (read on a goroutine, at most gmManTextMax of each, wrapped once for
// the card's width), then a list of what only the game page opens. Left and
// Right change part, Up and Down scroll, A turns a page, B goes back to the
// menu.
//
// PDF and picture pages are not drawn here. The Reader's renderer keeps one
// global document, its tile textures and a decode worker (pdfv_view.go), so
// borrowing it over a paused game would close whatever the Reader had open,
// and a scanned page is a 10–50 MB decode while the paused game still holds
// its memory and the overlay's three-minute cap runs. Those documents are
// named on the last part, to be opened from the game's page after playing.
//
// A Linux handheld only, like the menu itself.

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// gmManTextMax is how much of one guide the page reads.
	gmManTextMax = 64 << 10
	// gmManMaxGuides is how many text guides the page offers.
	gmManMaxGuides = 4
)

// manPlaying is the game the library started last and its process. Written
// by the launch, read by the menu.
var manPlaying struct {
	mu   sync.Mutex
	game libraryGame
	cmd  *exec.Cmd
}

func init() {
	gameLaunchRegisterStarted(func(game libraryGame, launcher string, cmd *exec.Cmd) {
		manPlaying.mu.Lock()
		manPlaying.game, manPlaying.cmd = game, cmd
		manPlaying.mu.Unlock()
		if gameAssistSupported() {
			// The in-game menu reads the list from memory.
			manRefresh(game, false, nil)
		}
	})
}

// gmMan is what the menu learned about the paused game, once per opening of
// the overlay. Main thread only.
var gmMan struct {
	checkedFor time.Time
	ok         bool
	game       libraryGame
	docs       []manDoc
}

// gmManualAvailable reports whether the paused game has a Manual row: a game
// the library started, still running, with a chart or a document.
func gmManualAvailable() bool {
	if !gaOverlay.active {
		return false
	}
	if !gmMan.checkedFor.IsZero() && gmMan.checkedFor.Equal(gaOverlay.openedAt) {
		return gmMan.ok
	}
	gmMan.checkedFor = gaOverlay.openedAt
	gmMan.ok, gmMan.game, gmMan.docs = false, libraryGame{}, nil
	manPlaying.mu.Lock()
	game, cmd := manPlaying.game, manPlaying.cmd
	manPlaying.mu.Unlock()
	if cmd == nil || game.Path == "" || !gaCmdIn(liveScreenChildren(), cmd) {
		return false
	}
	docs, _ := manDocsFor(game.Path)
	l, chart := ctrlLayoutForGame(game)
	gmMan.game, gmMan.docs = game, docs
	gmMan.ok = len(docs) > 0 || (chart && len(l.Chart) > 0)
	return gmMan.ok
}

// gmManualSub is the row's smaller line.
func gmManualSub() string {
	texts := 0
	for _, d := range gmMan.docs {
		if d.Kind == manKindText {
			texts++
		}
	}
	switch {
	case texts == 1:
		return "Controls and a guide"
	case texts > 1:
		return "Controls and " + strconv.Itoa(min(texts, gmManMaxGuides)) + " guides"
	case len(gmMan.docs) > 0:
		return "Controls · the manual opens from the game's page"
	}
	return "The game's controls"
}

// gmManPart is one part of the page.
type gmManPart struct {
	title string
	// pairs is the Controls chart; note goes under it.
	pairs []ctrlLayoutLine
	note  string
	// text is a guide (or the list of other documents), wrapped into lines
	// for wrapW.
	guide int // index into gmManText, -1 when text is set here
	text  string
	lines []string
	wrapW int32
}

// gmManText is the guides read for the open page: written by the reading
// goroutine, read by the draw.
var gmManText struct {
	mu     sync.Mutex
	gen    int
	texts  []string
	failed []bool
	ready  []bool
}

// gmManUI is the open page. Main thread only.
var gmManUI struct {
	open     bool
	openedAt time.Time
	parts    []gmManPart
	part     int
	scroll   int
	page     int // lines the last draw fitted
	total    int // lines of the part on screen
}

// gmManualActive reports the page open over the menu of this opening of the
// overlay; one left from an overlay that closed some other way is gone.
func gmManualActive() bool {
	if gmManUI.open && (!gaOverlay.active || !gmManUI.openedAt.Equal(gaOverlay.openedAt)) {
		gmManUI.open = false
		gmManUI.parts = nil
	}
	return gmManUI.open
}

// gmManualOpen shows the page: Controls, the guides (being read), and what
// only the game page opens.
func gmManualOpen() {
	if !gmManualAvailable() {
		PlayErrorSound()
		return
	}
	PlayActivateSound()
	game := gmMan.game
	title, pairs, note := manControls(game)
	parts := []gmManPart{{title: "Controls · " + title, pairs: pairs, note: note, guide: -1}}
	var paths []string
	var others []string
	for _, d := range gmMan.docs {
		if d.Kind == manKindText && len(paths) < gmManMaxGuides {
			parts = append(parts, gmManPart{title: d.Title, guide: len(paths)})
			paths = append(paths, d.Path)
			continue
		}
		if d.Kind != manKindText {
			others = append(others, "• "+d.Title+" ("+manDocKindLabel(d)+")")
		}
	}
	if len(others) > 0 {
		text := strings.Join(others, "\n") + "\n\nThese open from the game's page in the library: More → Manual & guide."
		parts = append(parts, gmManPart{title: "More for this game", text: text, guide: -1})
	}
	gmManText.mu.Lock()
	gmManText.gen++
	gen := gmManText.gen
	gmManText.texts = make([]string, len(paths))
	gmManText.failed = make([]bool, len(paths))
	gmManText.ready = make([]bool, len(paths))
	gmManText.mu.Unlock()
	if len(paths) > 0 {
		go gmManRead(gen, paths)
	}
	gmManUI.open, gmManUI.openedAt = true, gaOverlay.openedAt
	gmManUI.parts, gmManUI.part, gmManUI.scroll = parts, 0, 0
}

// gmManRead reads the guides for page gen. Its own goroutine.
func gmManRead(gen int, paths []string) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("manual", "reading a guide for the in-game menu failed: %v", r)
		}
	}()
	for i, p := range paths {
		text, err := gmManReadText(p)
		gmManText.mu.Lock()
		if gmManText.gen != gen || i >= len(gmManText.texts) {
			gmManText.mu.Unlock()
			return
		}
		gmManText.texts[i], gmManText.failed[i], gmManText.ready[i] = text, err != nil, true
		gmManText.mu.Unlock()
	}
}

// gmManReadText reads at most gmManTextMax of a guide as plain text.
func gmManReadText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, gmManTextMax))
	if err != nil {
		return "", err
	}
	text := strings.ToValidUTF8(strings.TrimPrefix(string(data), "\ufeff"), "?")
	if strings.HasSuffix(strings.ToLower(path), ".md") || strings.HasSuffix(strings.ToLower(path), ".markdown") {
		text = gmManPlainMarkdown(text)
	}
	return text, nil
}

// gmManPlainMarkdown keeps a Markdown guide readable as plain text: heading
// marks, emphasis and code ticks go, list marks become bullets. Pure.
func gmManPlainMarkdown(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		t = strings.TrimSpace(strings.TrimLeft(t, "#"))
		if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "+ ") {
			t = "• " + t[2:]
		}
		t = strings.NewReplacer("**", "", "__", "", "`", "").Replace(t)
		lines[i] = t
	}
	return strings.Join(lines, "\n")
}

// gmManualPress acts on one press while the page is up.
func gmManualPress(b gaButton) {
	ui := &gmManUI
	n := len(ui.parts)
	if n == 0 {
		ui.open = false
		return
	}
	step := max(1, ui.page/3)
	maxScroll := max(0, ui.total-ui.page)
	switch b {
	case gaBtnUp:
		if ui.scroll > 0 {
			ui.scroll = max(0, ui.scroll-step)
			PlayNavSound()
		}
	case gaBtnDown:
		if ui.scroll < maxScroll {
			ui.scroll = min(maxScroll, ui.scroll+step)
			PlayNavSound()
		}
	case gaBtnLeft, gaBtnRight:
		next := ui.part + 1
		if b == gaBtnLeft {
			next = ui.part - 1
		}
		if next >= 0 && next < n {
			ui.part, ui.scroll = next, 0
			PlayNavSound()
		}
	case gaBtnA:
		// A turns a page; at the end of a part it moves to the next one.
		switch {
		case ui.scroll < maxScroll:
			ui.scroll = min(maxScroll, ui.scroll+max(1, ui.page-1))
		case ui.part < n-1:
			ui.part, ui.scroll = ui.part+1, 0
		default:
			return
		}
		PlayNavSound()
	case gaBtnB:
		PlayBackSound()
		ui.open, ui.parts = false, nil
	case gaBtnMenu, gaBtnMenuChord:
		PlayBackSound()
		ui.open, ui.parts = false, nil
		gaClose("the player went back", true)
	}
}

// ── Drawing ──────────────────────────────────────────────────────────────

// gmManualDraw is the page's card: which part of how many, the game, the
// part's lines, and the buttons that work it.
func gmManualDraw(renderer *sdl.Renderer, fonts tbxFonts) {
	ui := &gmManUI
	if len(ui.parts) == 0 {
		return
	}
	if ui.part >= len(ui.parts) {
		ui.part = len(ui.parts) - 1
	}
	pad := orbPad
	w := screenWidth * 62 / 100
	if minW := scaleX(520, screenWidth); w < minW {
		w = minW
	}
	if w > screenWidth-4*pad {
		w = screenWidth - 4*pad
	}
	h := screenHeight - 4*pad
	inner := w - 2*pad
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	fill := glassSheetFill()
	fill.A = 240
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, fill)
	strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, 1, glassSheetEdge())

	part := &ui.parts[ui.part]
	kicker := "MANUAL"
	if len(ui.parts) > 1 {
		kicker += " · " + strconv.Itoa(ui.part+1) + " OF " + strconv.Itoa(len(ui.parts))
	}
	title := gaOverlay.title
	if title == "" {
		title = gmMan.game.Label()
	}
	x, y := card.X+pad, card.Y+pad
	drawText(renderer, fonts.tiny, kicker, x, y, glassAccent(), textAlignLeft)
	y += fonts.tinyH + 2
	drawText(renderer, fonts.body, ellipsize(fonts.body, title, inner), x, y, ColorTextPrimary(), textAlignLeft)
	y += fonts.bodyH + 2
	drawText(renderer, fonts.small, ellipsize(fonts.small, part.title, inner), x, y, ColorTextSecondary(), textAlignLeft)
	y += fonts.smallH + pad

	hint := "D-pad scroll · " + orbKeyName("A") + " next page · " + orbKeyName("B") + " menu"
	if len(ui.parts) > 1 {
		hint = "D-pad scroll · Left/Right part · " + orbKeyName("A") + " next page · " + orbKeyName("B") + " menu"
	}
	footY := card.Y + card.H - pad - fonts.smallH
	bodyH := footY - pad/2 - y
	if part.pairs != nil {
		gmManDrawPairs(renderer, fonts, part, x, y, inner, bodyH)
	} else {
		gmManDrawText(renderer, fonts, part, x, y, inner, bodyH)
	}
	if ui.total > ui.page && ui.page > 0 {
		last := min(ui.total, ui.scroll+ui.page)
		hint = strconv.Itoa(ui.scroll+1) + "–" + strconv.Itoa(last) + " of " + strconv.Itoa(ui.total) + " · " + hint
	}
	drawText(renderer, fonts.small, ellipsize(fonts.small, hint, inner), card.X+card.W/2, footY, ColorTextSecondary(), textAlignCenter)
}

// gmManDrawPairs draws the Controls chart: button and action in two columns,
// the note under them.
func gmManDrawPairs(renderer *sdl.Renderer, fonts tbxFonts, part *gmManPart, x, y, inner, bodyH int32) {
	ui := &gmManUI
	lineH := fonts.bodyH + 8
	var noteLines []string
	if part.note != "" {
		noteLines = pdfWrapLines(fonts.small, part.note, inner)
	}
	noteH := int32(len(noteLines)) * (fonts.smallH + 2)
	rows := int((bodyH - noteH) / lineH)
	if rows < 1 {
		rows = 1
	}
	ui.page, ui.total = rows, len(part.pairs)
	if ui.scroll > max(0, ui.total-ui.page) {
		ui.scroll = max(0, ui.total-ui.page)
	}
	colW := int32(0)
	for _, l := range part.pairs {
		if tw, _ := measureText(fonts.body, l.Button); tw > colW {
			colW = tw
		}
	}
	colW = min(colW+max32(16, fonts.bodyH), inner*2/5)
	for i := ui.scroll; i < len(part.pairs) && i < ui.scroll+rows; i++ {
		l := part.pairs[i]
		drawText(renderer, fonts.body, ellipsize(fonts.body, l.Button, colW-8), x, y, glassAccent(), textAlignLeft)
		drawText(renderer, fonts.body, ellipsize(fonts.body, l.Action, inner-colW), x+colW, y, ColorTextPrimary(), textAlignLeft)
		y += lineH
	}
	if ui.scroll+rows >= len(part.pairs) {
		for _, line := range noteLines {
			drawText(renderer, fonts.small, line, x, y, ColorTextSecondary(), textAlignLeft)
			y += fonts.smallH + 2
		}
	}
}

// gmManDrawText draws a guide (or the list of other documents), wrapped once
// for the width and scrolled.
func gmManDrawText(renderer *sdl.Renderer, fonts tbxFonts, part *gmManPart, x, y, inner, bodyH int32) {
	ui := &gmManUI
	status := ""
	if part.guide >= 0 && part.lines == nil {
		gmManText.mu.Lock()
		ready, failed, text := false, false, ""
		if part.guide < len(gmManText.ready) {
			ready, failed, text = gmManText.ready[part.guide], gmManText.failed[part.guide], gmManText.texts[part.guide]
		}
		gmManText.mu.Unlock()
		switch {
		case !ready:
			status = "Opening the guide…"
		case failed:
			status = "This guide could not be read"
		default:
			part.text = text
			part.guide = -1
		}
	}
	if status != "" {
		ui.page, ui.total = 0, 0
		drawText(renderer, fonts.small, status, x, y, ColorTextSecondary(), textAlignLeft)
		return
	}
	if part.lines == nil || part.wrapW != inner {
		// Wrapped once per part and width: a long guide costs one pass here,
		// not one per frame.
		part.lines = pdfWrapLines(fonts.small, part.text, inner)
		part.wrapW = inner
		if len(part.lines) == 0 {
			part.lines = []string{"This guide is empty"}
		}
	}
	lineH := fonts.smallH + 4
	rows := int(bodyH / lineH)
	if rows < 1 {
		rows = 1
	}
	ui.page, ui.total = rows, len(part.lines)
	if ui.scroll > max(0, ui.total-ui.page) {
		ui.scroll = max(0, ui.total-ui.page)
	}
	for i := ui.scroll; i < len(part.lines) && i < ui.scroll+rows; i++ {
		drawText(renderer, fonts.small, ellipsize(fonts.small, part.lines[i], inner), x, y, ColorTextPrimary(), textAlignLeft)
		y += lineH
	}
}
