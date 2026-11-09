package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Clipboard Manager — copy/paste history with search
// Ctrl+Shift+V opens the history; Ctrl+C / Ctrl+V copy and paste
// ──────────────────────────────────────────────────────────────────────

// clipboardSetText / clipboardGetText stand in for the SDL clipboard calls so
// tests can drive copy and paste without touching the real system clipboard.
var clipboardSetText = func(text string) error { return sdl.SetClipboardText(text) }
var clipboardGetText = func() (string, error) { return sdl.GetClipboardText() }

const clipMaxHistory = 30

type clipEntry struct {
	text      string
	timestamp time.Time
	source    string // where it was copied from
}

var clipHistory []clipEntry
var clipSearchOpen bool
var clipSearchBuf string
var clipCursorIdx int // which entry is focused

// copyTextToClipboard puts text on the system clipboard and records it in
// the clipboard-manager history (Ctrl+Shift+V). Every copy in the app goes
// through here so the history fills itself for free and clipboard failures
// surface the same way everywhere. The error is returned so a caller that
// wants to speak for itself can; copyWithToast is the common flow.
func copyTextToClipboard(text, source string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("nothing to copy")
	}
	if err := clipboardSetText(text); err != nil {
		return err
	}
	clipPush(text, source)
	return nil
}

// pasteTextFromClipboard reads text off the system clipboard.
func pasteTextFromClipboard() (string, error) {
	return clipboardGetText()
}

// copyWithToast is the house copy flow: copy, record in the history, then one
// toast for whichever way it went. Callers keep their own wording for the
// success line ("Message copied", "Component report copied"...) so the toast
// still names what was copied.
func copyWithToast(text, source, successToast string) {
	if err := copyTextToClipboard(text, source); err != nil {
		ShowToast("Could not copy: "+err.Error(), ToastKindWarning)
		return
	}
	ShowToast(successToast, ToastKindSuccess)
}

// clipboardPasteIntoActiveField pastes the system clipboard into whatever
// text surface currently owns the keyboard, in the same way the shared text
// input path (handleTextInputEvent) would deliver typed text: the on-screen
// keyboard and its targets (Browser address and form fields, Chat composer,
// toolbox arguments, Wi-Fi passphrase...), then the two scenes that keep
// their own buffers (Terminal at the prompt cursor, Notes in the open field).
// It reports whether the chord was consumed and only touches the system
// clipboard when a surface is really editing, so Ctrl+V in a menu keeps its
// ordinary meaning instead of popping a failure toast.
func clipboardPasteIntoActiveField(config *Config) bool {
	switch {
	case virtualKeyboardActive, chatInputActive, unitInputActive, activeSceneIndex != -1,
		textBrowserAddressEditing, textBrowserSearchEditing, textBrowserFindEditing,
		browserFormEditing:
	case isTerminalScene():
		text, err := pasteTextFromClipboard()
		if err != nil {
			ShowToast("Clipboard is unavailable: "+err.Error(), ToastKindWarning)
			return true
		}
		if strings.TrimSpace(text) == "" {
			ShowToast("Nothing to paste", ToastKindInfo)
			return true
		}
		termInsertText(text)
		// Done here: falling through to the shared path below pasted the
		// text a second time into a stale on-screen keyboard target.
		return true
	case isNotesScene():
		text, err := pasteTextFromClipboard()
		if err != nil {
			ShowToast("Clipboard is unavailable: "+err.Error(), ToastKindWarning)
			return true
		}
		if strings.TrimSpace(text) == "" {
			ShowToast("Nothing to paste", ToastKindInfo)
			return true
		}
		notesInsertText(text)
		return true
	default:
		return false
	}
	if virtualKeyboardActive {
		// The keyboard's own Paste: at the caret, line breaks folded into
		// spaces (every keyboard field is one line).
		vkPaste(config)
		return true
	}
	text, err := pasteTextFromClipboard()
	if err != nil {
		ShowToast("Clipboard is unavailable: "+err.Error(), ToastKindWarning)
		return true
	}
	if strings.TrimSpace(text) == "" {
		ShowToast("Nothing to paste", ToastKindInfo)
		return true
	}
	// vkInsertText is the app's one text-entry funnel: it strips control
	// bytes, replaces a field that opened with its old value selected, and
	// honours each target's own write-back.
	vkInsertText(text, config)
	return true
}

// clipboardCopyFromActiveField copies what the active text surface holds: the
// command line in the Terminal, the open field in Notes, the Chat composer
// draft, or the buffer an open on-screen keyboard is editing. Like the paste
// chord it is a no-op (with the chord left to ordinary handling) when no text
// is being edited.
func clipboardCopyFromActiveField(config *Config) bool {
	switch {
	case virtualKeyboardActive:
		// The keyboard's own Copy (vkedit.go): the field, named for the
		// history, and never a password.
		if vkCurrentText() == "" {
			return true
		}
		vkCopy()
	case chatInputActive:
		if chatInputText == "" {
			return true
		}
		copyWithToast(chatInputText, "Chat", "Copied to clipboard")
	case isTerminalScene():
		// Ctrl+C in a terminal with a command running is the interrupt, as
		// in every shell; this chord used to be taken for copy first, so the
		// terminal's own Ctrl+C never saw it.
		if termStopJob() {
			return true
		}
		if term.input == "" {
			return true
		}
		copyWithToast(term.input, "Terminal", "Copied to clipboard")
	case isNotesScene():
		switch notes.tab {
		case 1:
			if notes.editTitle == "" {
				return true
			}
			copyWithToast(notes.editTitle, "Notes", "Copied to clipboard")
		case 2:
			if notes.editContent == "" {
				return true
			}
			copyWithToast(notes.editContent, "Notes", "Copied to clipboard")
		default:
			return false
		}
	default:
		return false
	}
	return true
}

// isNotesScene reports whether the Notes scene owns the screen now, using the
// same guarded read of the scene table isTerminalScene applies.
func isNotesScene() bool {
	if appConfig != nil && currentSceneIndex >= 0 && currentSceneIndex < len(appConfig.Scenes) {
		return appConfig.Scenes[currentSceneIndex].Name == "Notes"
	}
	return false
}

func clipPush(text, source string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	// A text that is already in the history moves to the top instead of
	// appearing twice; copying an entry back from the overlay is the usual
	// case, and it used to leave a second copy of the same entry.
	for i := range clipHistory {
		if clipHistory[i].text == text {
			clipHistory = append(clipHistory[:i], clipHistory[i+1:]...)
			break
		}
	}
	entry := clipEntry{text: text, timestamp: time.Now(), source: source}
	clipHistory = append([]clipEntry{entry}, clipHistory...)
	if len(clipHistory) > clipMaxHistory {
		clipHistory = clipHistory[:clipMaxHistory]
	}
	vkClipGen++
}

// ── History overlay (Ctrl+Shift+V) ───────────────────────────────────────
//
// The overlay is a glass card over the dimmed screen: a search field that
// filters as you type, the entries (newest first, two-line preview, age and
// source on the right) and the hint badges for the pad or the keyboard. A row
// is picked with A, Enter or a click and goes back on the clipboard.

// vkClipGen changes whenever clipHistory does, so the overlay's view is
// rebuilt only then.
var vkClipGen int

// vkClipView is what the overlay shows: the entries the search keeps (indices
// into clipHistory, newest first) with their age labels, the subtitle, the
// rows' click targets and the footer plan. A steady frame filters, formats and
// measures nothing.
var vkClipView struct {
	ok        bool
	gen       int
	n         int
	search    string
	idx       []int
	subtitle  string
	agoAt     time.Time
	ago       []string
	top       int
	card      sdl.Rect
	rows      []sdl.Rect
	planOK    bool
	planCfg   *Config
	planW     int32
	planPad   bool
	planRemap uint32
	planScale float64
	plan      footerPlan
}

// vkClipPadHints and vkClipKeyHints are the overlay's footer for a pad and for
// a keyboard.
var (
	vkClipPadHints = [...]FooterHint{
		{Button: "D-Pad", Label: "Choose"},
		{Button: "A", Label: "Copy"},
		{Button: "X", Label: "Remove"},
		{Button: "B", Label: "Close"},
	}
	vkClipKeyHints = [...]FooterHint{
		{Button: "↑↓", Label: "Choose"},
		{Button: "Enter", Label: "Copy"},
		{Button: "Del", Label: "Remove"},
		{Button: "Esc", Label: "Close"},
	}
)

// vkClipVisible returns the entries the search keeps, as indices into
// clipHistory, newest first.
func vkClipVisible() []int {
	v := &vkClipView
	if v.ok && v.gen == vkClipGen && v.n == len(clipHistory) && v.search == clipSearchBuf {
		return v.idx
	}
	v.ok, v.gen, v.n, v.search = true, vkClipGen, len(clipHistory), clipSearchBuf
	v.idx = v.idx[:0]
	q := strings.ToLower(strings.TrimSpace(clipSearchBuf))
	for i := range clipHistory {
		e := &clipHistory[i]
		if q == "" || strings.Contains(strings.ToLower(e.text), q) || strings.Contains(strings.ToLower(e.source), q) {
			v.idx = append(v.idx, i)
		}
	}
	switch n := len(v.idx); {
	case q != "" && n == 0:
		v.subtitle = "No matches"
	case q != "":
		v.subtitle = strconv.Itoa(n) + " of " + strconv.Itoa(len(clipHistory)) + " entries"
	case n == 1:
		v.subtitle = "1 entry"
	case n > 1:
		v.subtitle = strconv.Itoa(n) + " entries"
	default:
		v.subtitle = "Recent copies"
	}
	v.agoAt = time.Time{}
	return v.idx
}

// vkClipAgo is an entry's age for the list: "just now", "5m ago".
func vkClipAgo(t time.Time) string {
	if time.Since(t) < time.Minute {
		return "just now"
	}
	return timeSince(t) + " ago"
}

// vkClipFocused is the clipHistory index of the highlighted entry, or -1.
func vkClipFocused() int {
	vis := vkClipVisible()
	if clipCursorIdx < 0 || clipCursorIdx >= len(vis) {
		return -1
	}
	return vis[clipCursorIdx]
}

// vkClipMove moves the highlight by delta through the entries the search
// keeps.
func vkClipMove(delta int) {
	clipCursorIdx = clampIndex(clipCursorIdx+delta, len(vkClipVisible()))
}

// vkClipClose closes the overlay and forgets the search.
func vkClipClose() {
	clipSearchOpen = false
	clipSearchBuf = ""
}

// vkClipCopyFocused puts the highlighted entry back on the clipboard (it moves
// to the top of the history) and closes the overlay. It reports whether an
// entry was highlighted. The pad, the keyboard and a click all come here, so
// a filtered list copies the entry that is shown, not the one at the same
// position in the full history.
func vkClipCopyFocused() bool {
	i := vkClipFocused()
	if i < 0 {
		return false
	}
	if err := copyTextToClipboard(clipHistory[i].text, "History"); err != nil {
		ShowToast("Could not copy: "+err.Error(), ToastKindWarning)
	} else {
		ShowToast("Copied to clipboard", ToastKindSuccess)
	}
	vkClipClose()
	return true
}

// vkClipDeleteFocused removes the highlighted entry from the history.
func vkClipDeleteFocused() bool {
	i := vkClipFocused()
	if i < 0 {
		return false
	}
	clipHistory = append(clipHistory[:i], clipHistory[i+1:]...)
	vkClipGen++
	clipCursorIdx = clampIndex(clipCursorIdx, len(vkClipVisible()))
	return true
}

// vkClipLines splits an entry for its two-line preview without allocating:
// the first line and the one after it.
func vkClipLines(text string) (string, string) {
	first, rest := text, ""
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first, rest = text[:i], text[i+1:]
	}
	rest = strings.TrimSpace(rest)
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(first), strings.TrimSpace(rest)
}

// vkClipHandleClick answers a click or tap while the history is open: a row
// copies its entry back and a click outside the card closes it. The overlay is
// modal, so the click never reaches the screen behind it.
func vkClipHandleClick(mx, my int32) {
	v := &vkClipView
	if !pointInRect(mx, my, v.card) {
		vkClipClose()
		PlayBackSound()
		return
	}
	for k, r := range v.rows {
		if pointInRect(mx, my, r) {
			clipCursorIdx = v.top + k
			if vkClipCopyFocused() {
				PlayActivateSound()
			}
			return
		}
	}
}

func renderClipboardOverlay(renderer *sdl.Renderer, config *Config) {
	if !clipSearchOpen {
		return
	}
	v := &vkClipView
	vis := vkClipVisible()
	clipCursorIdx = clampIndex(clipCursorIdx, len(vis))

	cardW, cardH := int32(720), int32(540)
	if cardW > screenWidth-40 {
		cardW = screenWidth - 40
	}
	if cardH > screenHeight-40 {
		cardH = screenHeight - 40
	}
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	v.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	header := overlayPageHeader(card, "Clipboard", v.subtitle, "copy")
	top := UIPageHeaderDraw(renderer, config, header) + 12
	vkSetFontsFor(config)
	innerX, innerW := card.X+OverlayPadX, card.W-2*OverlayPadX

	// The search field: a recessed glass control; typing filters the list.
	search := sdl.Rect{X: innerX, Y: top, W: innerW, H: 40}
	fillRoundedRect(renderer, search.X, search.Y, search.W, search.H, glassControlRadius, glassMix(overlayCardFill(), ColorBackground, 0.5))
	strokeRoundedRect(renderer, search.X, search.Y, search.W, search.H, glassControlRadius, 1, glassA(34))
	icon := int32(18)
	drawHomeGlyphTinted(renderer, "search", sdl.Rect{X: search.X + 14, Y: search.Y + (search.H-icon)/2, W: icon, H: icon}, ColorTextTertiary())
	if f := vkSetFonts.body; f != nil {
		tx := search.X + 14 + icon + 10
		th := int32(f.Height())
		ty := search.Y + (search.H-th)/2
		caretX := tx
		if clipSearchBuf == "" {
			drawText(renderer, f, "Type to filter", tx+8, ty, ColorTextTertiary(), textAlignLeft)
		} else {
			shown := ellipsize(f, clipSearchBuf, search.X+search.W-14-tx)
			drawText(renderer, f, shown, tx, ty, ColorTextPrimary(), textAlignLeft)
			w, _ := measureText(f, shown)
			caretX = tx + w + 2
		}
		if (sdl.GetTicks64()/530)%2 == 0 {
			fillRoundedRect(renderer, caretX, ty+2, 2, th-4, 1, glassAccent())
		}
	}

	// The list stops above the footer, which sits on the card's bottom margin.
	band := sdl.Rect{X: innerX, Y: card.Y + card.H - 46, W: innerW, H: 32}
	list := sdl.Rect{X: innerX, Y: search.Y + search.H + 12, W: innerW, H: 0}
	list.H = band.Y - 10 - list.Y
	vkClipDrawList(renderer, config, list, vis)
	fillRoundedRect(renderer, innerX, band.Y-6, innerW, 1, 0, glassHairline())
	vkClipDrawFooter(renderer, config, band)
}

// vkClipDrawList draws the entries the search keeps, keeping the highlight in
// view, and records each row's click target.
func vkClipDrawList(renderer *sdl.Renderer, config *Config, list sdl.Rect, vis []int) {
	v := &vkClipView
	v.rows = v.rows[:0]
	if list.H <= 0 {
		return
	}
	if len(vis) == 0 {
		title, body := "Nothing copied yet", "Copies from any text field land here"
		if strings.TrimSpace(v.search) != "" {
			title, body = "No matches", "Backspace widens the search"
		}
		UIEmptyState(renderer, config, list, "@copy", title, body)
		return
	}
	// The ages are rebuilt once a second, not per frame.
	if v.agoAt.IsZero() || time.Since(v.agoAt) >= time.Second || len(v.ago) != len(vis) {
		v.ago = v.ago[:0]
		for _, i := range vis {
			v.ago = append(v.ago, vkClipAgo(clipHistory[i].timestamp))
		}
		v.agoAt = time.Now()
	}
	rowH, gap := int32(58), int32(6)
	if vkScreenSmall() {
		rowH = 50
	}
	shown := int((list.H + gap) / (rowH + gap))
	if shown < 1 {
		shown = 1
	}
	if clipCursorIdx < v.top {
		v.top = clipCursorIdx
	}
	if clipCursorIdx >= v.top+shown {
		v.top = clipCursorIdx - shown + 1
	}
	if maxTop := len(vis) - shown; v.top > maxTop {
		v.top = maxTop
	}
	if v.top < 0 {
		v.top = 0
	}
	title, detail, small := vkSetFonts.title, vkSetFonts.detail, vkSetFonts.badge
	for k := 0; k < shown && v.top+k < len(vis); k++ {
		pos := v.top + k
		e := &clipHistory[vis[pos]]
		row := sdl.Rect{X: list.X, Y: list.Y + int32(k)*(rowH+gap), W: list.W - 10, H: rowH}
		v.rows = append(v.rows, row)
		UIListRow(renderer, row, glassRowRadius, pos == clipCursorIdx, false)
		pad := int32(16)
		right := row.X + row.W - pad
		// The age above the source, right-aligned.
		if small != nil && pos < len(v.ago) {
			aw, ah := measureText(small, v.ago[pos])
			colW := aw
			lines := ah
			if e.source != "" {
				sw, sh := measureText(small, e.source)
				if sw > colW {
					colW = sw
				}
				lines += sh + 2
			}
			y := row.Y + (row.H-lines)/2
			drawText(renderer, small, v.ago[pos], right-aw, y, ColorTextTertiary(), textAlignLeft)
			if e.source != "" {
				sw, _ := measureText(small, e.source)
				drawText(renderer, small, e.source, right-sw, y+ah+2, ColorTextAccent(), textAlignLeft)
			}
			right -= colW + 16
		}
		if title == nil {
			continue
		}
		first, rest := vkClipLines(e.text)
		x := row.X + pad
		avail := right - x
		if avail < 40 {
			avail = 40
		}
		th := int32(title.Height())
		dh := int32(0)
		if detail != nil && rest != "" {
			dh = int32(detail.Height())
		}
		ty := row.Y + (row.H-th-dh)/2
		drawText(renderer, title, ellipsize(title, first, avail), x, ty, ColorTextPrimary(), textAlignLeft)
		if dh > 0 {
			drawText(renderer, detail, ellipsize(detail, rest, avail), x, ty+th, ColorTextSecondary(), textAlignLeft)
		}
	}
	// A thin scrollbar says there is more.
	if len(vis) > shown {
		trackX := list.X + list.W - 4
		fillRoundedRect(renderer, trackX, list.Y, 3, list.H, 1, glassHairline())
		thumbH := list.H * int32(shown) / int32(len(vis))
		if thumbH < 24 {
			thumbH = 24
		}
		thumbY := list.Y + (list.H-thumbH)*int32(v.top)/int32(len(vis)-shown)
		fillRoundedRect(renderer, trackX, thumbY, 3, thumbH, 1, glassA(90))
	}
}

// vkClipDrawFooter draws the hint badges for the pad or the keyboard; the plan
// is measured once per width and type size.
func vkClipDrawFooter(renderer *sdl.Renderer, config *Config, band sdl.Rect) {
	v := &vkClipView
	// The pad's hints or the keyboard's, whichever the hints use this frame
	// (inputhints.go); the plan below is re-measured when that, or
	// Accessibility's remap, changes.
	onPad, remap := !hintsUseKeyboard(), hintRemapGen()
	if !v.planOK || v.planCfg != config || v.planW != band.W || v.planPad != onPad || v.planRemap != remap || v.planScale != a11y.fontScale {
		hints := vkClipKeyHints[:]
		if onPad {
			hints = vkClipPadHints[:]
		}
		v.plan = planFooterHints(config, hints, band.W)
		v.planOK, v.planCfg, v.planW, v.planPad, v.planRemap, v.planScale = true, config, band.W, onPad, remap, a11y.fontScale
	}
	vkDrawHintPlan(renderer, config, v.plan, band)
}

// handleClipboardInput is the history overlay's keyboard: it owns every key
// while it is open. Enter copies the highlighted entry of the list as shown
// (it used to copy the entry at the same position in the unfiltered history).
func handleClipboardInput(event *sdl.KeyboardEvent) bool {
	if !clipSearchOpen {
		return false
	}
	if event.Type != sdl.KEYDOWN {
		return true
	}
	switch event.Keysym.Sym {
	case sdl.K_ESCAPE:
		vkClipClose()
	case sdl.K_UP:
		vkClipMove(-1)
	case sdl.K_DOWN:
		vkClipMove(1)
	case sdl.K_PAGEUP:
		vkClipMove(-5)
	case sdl.K_PAGEDOWN:
		vkClipMove(5)
	case sdl.K_HOME:
		clipCursorIdx = 0
	case sdl.K_END:
		vkClipMove(len(clipHistory))
	case sdl.K_RETURN, sdl.K_KP_ENTER:
		vkClipCopyFocused()
	case sdl.K_DELETE:
		vkClipDeleteFocused()
	case sdl.K_BACKSPACE:
		clipSearchBuf = trimLastRune(clipSearchBuf)
		clipCursorIdx = 0
	case sdl.K_u:
		if event.Keysym.Mod&sdl.KMOD_CTRL != 0 {
			clipSearchBuf = ""
			clipCursorIdx = 0
		}
	}
	return true
}

// handleClipboardTextInput types into the overlay's search; control bytes are
// dropped and the search is kept short.
func handleClipboardTextInput(text string) {
	if !clipSearchOpen {
		return
	}
	clipSearchBuf = vkTruncateUTF8(clipSearchBuf+vkSanitizeInsert(text), 120)
	clipCursorIdx = 0
}

func timeSince(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
