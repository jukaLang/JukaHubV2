package main

// systool_logs.go — the Logs screen (config scene "LogExporter").
//
// It shows the app's log as soon as it opens: how many errors and warnings
// there are, the latest error, and the lines themselves, newest at the bottom
// and coloured by severity, following new lines unless the user has scrolled
// up. Left/Right filter to warnings or errors, A shows a whole line, X saves
// logs.txt next to the app (for a bug report) and Y clears the log (pressed
// twice, so a stray press cannot wipe it). The buffer is read only when it has
// changed, at most twice a second.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	sysToolLogInfo = iota
	sysToolLogWarn
	sysToolLogError
)

// sysToolLogClearWindow is how long the first Y stays armed.
const sysToolLogClearWindow = 3 * time.Second

// sysToolLogRow is one parsed log line.
type sysToolLogRow struct {
	level int
	clock string
	where string
	text  string
	raw   string
}

// sysToolParseLogLine splits a ring line ("15:04:05 WARN  apps: message
// key=value", logging.go) into its time, its level, its subsystem and its
// message. A line without a level column (a dependency writing raw text) has
// its severity guessed from its words.
func sysToolParseLogLine(line string) sysToolLogRow {
	r := sysToolLogRow{raw: line}
	s := line
	if len(s) >= 9 && s[2] == ':' && s[5] == ':' && s[8] == ' ' {
		r.clock, s = s[:8], s[9:]
	}
	level := -1
	if len(s) >= 6 && s[5] == ' ' {
		switch s[:5] {
		case "DEBUG", "INFO ":
			level = sysToolLogInfo
		case "WARN ":
			level = sysToolLogWarn
		case "ERROR":
			level = sysToolLogError
		}
		if level >= 0 {
			s = strings.TrimLeft(s[6:], " ")
		}
	}
	if i := strings.Index(s, ": "); i > 0 && i <= 24 && !strings.ContainsAny(s[:i], " \t") {
		r.where, s = s[:i], s[i+2:]
	}
	r.text = s
	if level < 0 {
		level = sysToolLogLevel(s)
	}
	r.level = level
	return r
}

// sysToolLogLevel guesses a line's severity from its words.
func sysToolLogLevel(s string) int {
	l := strings.ToLower(s)
	for _, w := range []string{"panic", "fatal", "error", "failed", "failure"} {
		if strings.Contains(l, w) {
			return sysToolLogError
		}
	}
	for _, w := range []string{"warn", "timeout", "timed out", "retry", "could not", "cannot", "can't"} {
		if strings.Contains(l, w) {
			return sysToolLogWarn
		}
	}
	return sysToolLogInfo
}

// sysToolLogExport is the result of the last export, written by its task.
var sysToolLogExport struct {
	mu   sync.Mutex
	note string
}

// sysToolLogsState is the screen's state. Render thread only.
var sysToolLogs struct {
	version  uint64
	read     bool
	readAt   time.Time
	rows     []sysToolLogRow
	errors   int
	warnings int
	lastErr  string
	counts   [3]string

	filter      int // 0 all, 1 warnings and errors, 2 errors
	view        []int
	viewFor     uint64
	viewFilter  int
	viewBuilt   bool
	cursor      int // index into view; len(view)-1 follows new lines
	following   bool
	visible     int
	clearArmed  time.Time
	sheet       bool
	sheetRow    sysToolLogRow
	sheetLines  []string
	sheetW      int32
	exportRect  sdl.Rect
	clearRect   sdl.Rect
	filterRects [3]sdl.Rect
	rowHits     []sdl.Rect
	rowFirst    int
	sheetRect   sdl.Rect
}

// sysToolLogsEnter shows the newest lines whenever the screen opens.
func sysToolLogsEnter() {
	sysToolLogs.following = true
	sysToolLogs.sheet = false
	sysToolLogs.read = false
}

// sysToolLogsRefresh re-reads the buffer when it changed (at most twice a
// second) and rebuilds the filtered view.
func sysToolLogsRefresh() {
	s := &sysToolLogs
	if !s.read || (time.Since(s.readAt) >= 500*time.Millisecond && appLog.Version() != s.version) {
		lines, version := appLog.Snapshot()
		s.version, s.readAt, s.read = version, time.Now(), true
		s.rows = s.rows[:0]
		s.errors, s.warnings, s.lastErr = 0, 0, ""
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			row := sysToolParseLogLine(line)
			switch row.level {
			case sysToolLogError:
				s.errors++
				s.lastErr = row.text
			case sysToolLogWarn:
				s.warnings++
			}
			s.rows = append(s.rows, row)
		}
		s.counts = [3]string{fmt.Sprint(s.errors), fmt.Sprint(s.warnings), fmt.Sprint(len(s.rows))}
		s.viewBuilt = false
	}
	if !s.viewBuilt || s.viewFor != s.version || s.viewFilter != s.filter {
		s.view = s.view[:0]
		for i, row := range s.rows {
			if row.level >= s.filter {
				s.view = append(s.view, i)
			}
		}
		s.viewFor, s.viewFilter, s.viewBuilt = s.version, s.filter, true
		if s.following || s.cursor >= len(s.view) {
			s.cursor = len(s.view) - 1
		}
		if s.cursor < 0 {
			s.cursor = 0
		}
	}
}

// sysToolLogsMove moves the highlighted line; reaching the newest line
// follows new output again. It reports false when there is nothing above.
func sysToolLogsMove(delta int) bool {
	s := &sysToolLogs
	n := len(s.view)
	if n == 0 {
		return delta > 0
	}
	next := s.cursor + delta
	if next < 0 {
		if s.cursor == 0 {
			return false
		}
		next = 0
	}
	if next > n-1 {
		next = n - 1
	}
	if next != s.cursor {
		PlayNavSound()
	}
	s.cursor = next
	s.following = s.cursor == n-1
	return true
}

func sysToolLogsSetFilter(filter int) {
	if filter < 0 || filter > 2 || filter == sysToolLogs.filter {
		return
	}
	sysToolLogs.filter = filter
	sysToolLogs.following = true
	PlayNavSound()
}

// sysToolLogsSave writes logs.txt next to the app, off the render thread, and
// says where it went.
func sysToolLogsSave() {
	PlayActivateSound()
	data := capLogData([]byte(appLog.String()), logExportMaxBytes)
	runSceneTask(func(ctx context.Context) {
		path, err := filepath.Abs("logs.txt")
		if err != nil {
			path = "logs.txt"
		}
		if err := writeRotatedLog(path, data, logExportMaxBytes); err != nil {
			showToast("Couldn't save the log: "+firstLine(err.Error()), ToastWarn())
			return
		}
		lines := 0
		if len(data) > 0 {
			lines = strings.Count(string(data), "\n") + 1
		}
		note := fmt.Sprintf("Saved %d lines to %s at %s", lines, path, time.Now().Format("15:04"))
		sysToolLogExport.mu.Lock()
		sysToolLogExport.note = note
		sysToolLogExport.mu.Unlock()
		showToast("Saved logs.txt — attach it to a bug report", ToastSuccess())
	})
}

// sysToolLogsClear is Y: the first press arms, the second within a few
// seconds clears.
func sysToolLogsClear() {
	s := &sysToolLogs
	if s.clearArmed.IsZero() || time.Since(s.clearArmed) > sysToolLogClearWindow {
		s.clearArmed = time.Now()
		PlayNavSound()
		showToast("Press Y again to clear the log", ToastInfo())
		return
	}
	s.clearArmed = time.Time{}
	appLog.Clear()
	s.read = false
	s.following = true
	PlayActivateSound()
	showToast("Log cleared", ToastInfo())
}

// sysToolLogsOpenSheet shows the highlighted line in full.
func sysToolLogsOpenSheet() {
	s := &sysToolLogs
	if s.cursor < 0 || s.cursor >= len(s.view) {
		return
	}
	idx := s.view[s.cursor]
	if idx < 0 || idx >= len(s.rows) {
		return
	}
	s.sheet, s.sheetRow, s.sheetLines = true, s.rows[idx], nil
	PlayActivateSound()
}

// sysToolLogsFooterHints is the Logs screen's footer row.
func sysToolLogsFooterHints() []FooterHint {
	if sysToolLogs.sheet {
		return []FooterHint{{Button: "B", Label: "Close"}, {Button: "X", Label: "Copy line"}}
	}
	clearHint := FooterHint{Button: "Y", Label: "Clear"}
	if !sysToolLogs.clearArmed.IsZero() && time.Since(sysToolLogs.clearArmed) <= sysToolLogClearWindow {
		clearHint.Label = "Clear — press again"
	}
	return []FooterHint{
		{Button: "D-Pad", Label: "Choose"},
		{Button: "A", Label: "Details"},
		{Button: "B", Label: "Back"},
		{Button: "X", Label: "Save logs.txt"},
		clearHint,
		{Button: "L1/R1", Label: "Page"},
	}
}

func renderSysToolLogs(renderer *sdl.Renderer, config *Config) {
	sysToolLogsRefresh()
	s := &sysToolLogs
	status := ""
	if s.read {
		status = s.counts[2] + " lines · live"
	}
	area := sysToolDrawPage(renderer, config, "LogExporter", sysToolPageSpec{
		Title:       "Logs",
		Subtitle:    "What JukaHub has been doing — save it to send with a bug report",
		Icon:        "logs",
		Status:      status,
		StatusColor: ColorTextTertiary(),
	})
	f, ok := sysToolLoadFonts(config)
	if !ok {
		sysToolFinishPage(renderer, config)
		return
	}
	s.exportRect, s.clearRect = sdl.Rect{}, sdl.Rect{}
	gap := int32(12)

	// Summary: the counts, the latest error, and the two actions.
	sumH := int32(f.value.Height()) + int32(f.label.Height()) + int32(f.body.Height()) + 40
	sum := sdl.Rect{X: area.X, Y: area.Y, W: area.W, H: sumH}
	sysToolCard(renderer, sum, false)
	pad := int32(18)
	captions := [3]string{"ERRORS", "WARNINGS", "LINES"}
	inks := [3]sdl.Color{DangerInk(), WarningInk(), ColorTextPrimary()}
	x := sum.X + pad
	for i := 0; i < 3; i++ {
		ink := inks[i]
		if i < 2 && s.counts[i] == "0" {
			ink = ColorTextTertiary()
		}
		drawText(renderer, f.label, captions[i], x, sum.Y+14, ColorTextTertiary(), textAlignLeft)
		drawText(renderer, f.value, s.counts[i], x, sum.Y+14+int32(f.label.Height())+2, ink, textAlignLeft)
		cw, _ := measureText(f.label, captions[i])
		vw, _ := measureText(f.value, s.counts[i])
		if vw > cw {
			cw = vw
		}
		x += cw + 36
	}
	clearLabel := "Clear"
	if !s.clearArmed.IsZero() && time.Since(s.clearArmed) <= sysToolLogClearWindow {
		clearLabel = "Clear — again"
	}
	cw := sysToolActionWidth(f, "Y", clearLabel)
	ew := sysToolActionWidth(f, "X", "Save logs.txt")
	actY := sum.Y + 14
	s.clearRect = sysToolAction(renderer, f, sum.X+sum.W-pad-cw, actY, "Y", clearLabel, false)
	s.exportRect = sysToolAction(renderer, f, s.clearRect.X-10-ew, actY, "X", "Save logs.txt", true)
	note := ""
	sysToolLogExport.mu.Lock()
	note = sysToolLogExport.note
	sysToolLogExport.mu.Unlock()
	noteInk := ColorTextTertiary()
	if note == "" && s.lastErr != "" {
		note, noteInk = "Latest error: "+s.lastErr, DangerInk()
	}
	if note == "" {
		note = "No errors so far"
	}
	drawText(renderer, f.body, ellipsize(f.body, note, sum.W-2*pad), sum.X+pad, sum.Y+sum.H-pad-int32(f.body.Height())+4, noteInk, textAlignLeft)

	// Filters.
	fy := sum.Y + sum.H + gap
	names := [3]string{"All", "Warnings", "Errors"}
	fh := int32(f.label.Height()) + 16
	fx := area.X
	for i, name := range names {
		w, lh := measureText(f.body, name)
		r := sdl.Rect{X: fx, Y: fy, W: w + 32, H: fh}
		active := s.filter == i
		accent := glassAccent()
		switch {
		case active:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, fh/2, WithAlpha(accent, 34))
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, fh/2, 2, accent)
		case sysToolPointIn(r):
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, fh/2, glassFillHover())
		default:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, fh/2, glassFill())
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, fh/2, 1, glassEdge())
		}
		ink := ColorTextSecondary()
		if active {
			ink = ColorTextPrimary()
		}
		drawText(renderer, f.body, name, r.X+16, r.Y+(fh-lh)/2, ink, textAlignLeft)
		s.filterRects[i] = r
		fx += r.W + 8
	}

	// The lines.
	listY := fy + fh + gap
	list := sdl.Rect{X: area.X, Y: listY, W: area.W, H: area.Y + area.H - listY}
	sysToolCard(renderer, list, false)
	s.rowHits = s.rowHits[:0]
	if len(s.view) == 0 {
		title, body := "The log is empty", "Messages appear here as JukaHub works."
		if s.filter > 0 && len(s.rows) > 0 {
			title, body = "Nothing at this level", "Press Left for every line."
		}
		UIEmptyState(renderer, config, list, "@logs", title, body)
	} else {
		sysToolLogsDrawLines(renderer, f, list)
	}

	sysToolFinishPage(renderer, config)
	if s.sheet {
		sysToolLogsDrawSheet(renderer, config, f)
	}
}

// sysToolLogsDrawLines draws the visible window of the filtered lines, the
// highlighted one in view.
func sysToolLogsDrawLines(renderer *sdl.Renderer, f sysToolFonts, list sdl.Rect) {
	s := &sysToolLogs
	pad := int32(12)
	rowH := int32(f.body.Height()) + 8
	inner := sdl.Rect{X: list.X + pad, Y: list.Y + 8, W: list.W - 2*pad - 10, H: list.H - 16}
	visible := int(inner.H / rowH)
	if visible < 1 {
		visible = 1
	}
	s.visible = visible
	first := s.cursor - visible + 1
	if first < 0 {
		first = 0
	}
	// Keep the window where it was while the cursor stays inside it.
	if s.rowFirst <= s.cursor && s.cursor < s.rowFirst+visible && s.rowFirst+visible <= len(s.view) && !s.following {
		first = s.rowFirst
	}
	if first > len(s.view)-visible {
		first = len(s.view) - visible
	}
	if first < 0 {
		first = 0
	}
	s.rowFirst = first
	clockW, _ := measureText(f.label, "00:00:00")
	accent := glassAccent()
	for i := first; i < len(s.view) && i < first+visible; i++ {
		row := s.rows[s.view[i]]
		y := inner.Y + int32(i-first)*rowH
		rect := sdl.Rect{X: list.X + 6, Y: y, W: list.W - 12 - 10, H: rowH}
		if i == s.cursor {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, WithAlpha(accent, 30))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, 1, WithAlpha(accent, 160))
		} else if sysToolPointIn(rect) {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, glassFillHover())
		}
		dot := glassA(60)
		ink := ColorTextPrimary()
		switch row.level {
		case sysToolLogError:
			dot, ink = ColorDanger, DangerInk()
		case sysToolLogWarn:
			dot, ink = ColorWarning, WarningInk()
		}
		fillCircle(renderer, inner.X+4, y+rowH/2, 4, dot)
		tx := inner.X + 16
		_, th := measureText(f.body, "Ag")
		ty := y + (rowH-th)/2
		if row.clock != "" {
			_, ch := measureText(f.label, row.clock)
			drawText(renderer, f.label, row.clock, tx, y+(rowH-ch)/2, ColorTextTertiary(), textAlignLeft)
		}
		tx += clockW + 12
		drawText(renderer, f.body, ellipsize(f.body, row.text, inner.X+inner.W-tx), tx, ty, ink, textAlignLeft)
		s.rowHits = append(s.rowHits, rect)
	}
	if len(s.view) > visible {
		thumb := float64(visible) / float64(len(s.view))
		pos := float64(first) / float64(len(s.view)-visible)
		drawScrollbar(renderer, list.X+list.W-10, inner.Y, 4, inner.H, thumb, pos)
	}
}

// sysToolLogsDrawSheet shows one line in full over a dimmed screen.
func sysToolLogsDrawSheet(renderer *sdl.Renderer, config *Config, f sysToolFonts) {
	s := &sysToolLogs
	drawOverlayScrim(renderer)
	margin := scaleX(homeSideMargin, screenWidth)
	w := screenWidth - 2*margin
	if w > 860 {
		w = 860
	}
	top := headerBarHeight() + 12
	h := screenHeight - top - 24
	if h > 520 {
		h = 520
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: top, W: w, H: h}
	drawOverlayCard(renderer, card)
	s.sheetRect = card
	title := "Log line"
	switch s.sheetRow.level {
	case sysToolLogError:
		title = "Error"
	case sysToolLogWarn:
		title = "Warning"
	}
	sub := strings.TrimSpace(s.sheetRow.clock + "  " + s.sheetRow.where)
	y := UIPageHeaderDraw(renderer, config, overlayPageHeader(card, title, sub, "logs")) + 14
	textW := card.W - 2*OverlayPadX
	if s.sheetLines == nil || s.sheetW != textW {
		s.sheetLines = sysToolWrapAll(f.body, s.sheetRow.text, textW, 14)
		s.sheetW = textW
	}
	lineH := int32(f.body.Height()) + 4
	limit := card.Y + card.H - OverlayPadX - int32(f.label.Height()) - 12
	for _, line := range s.sheetLines {
		if y+lineH > limit {
			break
		}
		drawText(renderer, f.body, line, card.X+OverlayPadX, y, ColorTextPrimary(), textAlignLeft)
		y += lineH
	}
	hint := hintLine(FooterHint{Button: "X", Label: "Copy line"}, FooterHint{Button: "B", Label: "Close"})
	hw, hh := measureText(f.label, hint)
	drawText(renderer, f.label, hint, card.X+(card.W-hw)/2, card.Y+card.H-OverlayPadX-hh, ColorTextTertiary(), textAlignLeft)
}

// sysToolWrapAll wraps text to maxW like wrapTextLines, but breaks a word too
// long for one line (a path, a URL, a stack frame) instead of cutting it off.
func sysToolWrapAll(font *ttf.Font, text string, maxW int32, maxLines int) []string {
	rows := termWrap(font, text, maxW)
	if len(rows) > maxLines {
		rows = rows[:maxLines]
		rows[maxLines-1] = ellipsize(font, rows[maxLines-1]+"…", maxW)
	}
	return rows
}

func sysToolLogsPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	s := &sysToolLogs
	if s.sheet {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_A:
			s.sheet = false
			PlayBackSound()
		case sdl.CONTROLLER_BUTTON_X:
			copyWithToast(s.sheetRow.raw, "Logs", "Log line copied")
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if !sysToolLogsMove(-1) {
			return focusChromeBar()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		sysToolLogsMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		sysToolLogsMove(-sysToolLogsPage())
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		sysToolLogsMove(sysToolLogsPage())
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		sysToolLogsSetFilter(s.filter - 1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		sysToolLogsSetFilter(s.filter + 1)
	case sdl.CONTROLLER_BUTTON_A:
		sysToolLogsOpenSheet()
	case sdl.CONTROLLER_BUTTON_X:
		sysToolLogsSave()
	case sdl.CONTROLLER_BUTTON_Y:
		sysToolLogsClear()
	default:
		return false
	}
	return true
}

// sysToolLogsPage is one L1/R1 step: a screenful less one line.
func sysToolLogsPage() int {
	if sysToolLogs.visible > 2 {
		return sysToolLogs.visible - 1
	}
	return 8
}

func sysToolLogsKey(e *sdl.KeyboardEvent, config *Config) bool {
	s := &sysToolLogs
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
	if s.sheet {
		switch e.Keysym.Sym {
		case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b, sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			s.sheet = false
			PlayBackSound()
		case sdl.K_x, sdl.K_c:
			copyWithToast(s.sheetRow.raw, "Logs", "Log line copied")
		}
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		goBackScene(config)
	case sdl.K_UP:
		if !sysToolLogsMove(-1) {
			return focusChromeBar()
		}
	case sdl.K_DOWN:
		sysToolLogsMove(1)
	case sdl.K_PAGEUP:
		sysToolLogsMove(-sysToolLogsPage())
	case sdl.K_PAGEDOWN:
		sysToolLogsMove(sysToolLogsPage())
	case sdl.K_HOME:
		sysToolLogsMove(-len(s.view))
	case sdl.K_END:
		sysToolLogsMove(len(s.view))
	case sdl.K_LEFT:
		sysToolLogsSetFilter(s.filter - 1)
	case sdl.K_RIGHT:
		sysToolLogsSetFilter(s.filter + 1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		sysToolLogsOpenSheet()
	case sdl.K_x:
		sysToolLogsSave()
	case sdl.K_s:
		if ctrl {
			sysToolLogsSave()
		}
	case sdl.K_y, sdl.K_DELETE:
		sysToolLogsClear()
	default:
		return false
	}
	return true
}

func sysToolLogsClick(mx, my int32) bool {
	s := &sysToolLogs
	if s.sheet {
		if !pointInRect(mx, my, s.sheetRect) {
			s.sheet = false
			PlayBackSound()
		}
		return true
	}
	switch {
	case s.exportRect.W > 0 && pointInRect(mx, my, s.exportRect):
		sysToolLogsSave()
		return true
	case s.clearRect.W > 0 && pointInRect(mx, my, s.clearRect):
		sysToolLogsClear()
		return true
	}
	for i, r := range s.filterRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			sysToolLogsSetFilter(i)
			return true
		}
	}
	for i, r := range s.rowHits {
		if pointInRect(mx, my, r) {
			idx := s.rowFirst + i
			if idx == s.cursor {
				sysToolLogsOpenSheet()
			} else if idx >= 0 && idx < len(s.view) {
				s.cursor = idx
				s.following = idx == len(s.view)-1
				PlayNavSound()
			}
			return true
		}
	}
	return false
}

// sysToolLogsWheel scrolls the lines.
func sysToolLogsWheel(dy int32) bool {
	if sysToolLogs.sheet || dy == 0 {
		return sysToolLogs.sheet
	}
	sysToolLogsMove(-int(dy) * 3)
	return true
}

// sysToolLogsScene is the registered module for the "LogExporter" scene.
type sysToolLogsScene struct{ sceneBase }

func (sysToolLogsScene) render(r *sdl.Renderer, c *Config) bool { renderSysToolLogs(r, c); return true }

func (sysToolLogsScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	return sysToolLogsKey(e, c)
}

func (sysToolLogsScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return sysToolLogsPad(e, c)
}

func init() { registerScene(sysToolLogsScene{sceneBase{name: "LogExporter"}}) }
