package main

// cmddeck_ui.go — how the Command Deck looks: Home's overlay card over the
// Terminal with the category rail on the left and the commands on the right;
// the form with its live preview of the exact line; the pickers; the confirm
// card for commands that change things. State and input are in cmddeck.go.
//
// Strings a steady frame needs (the header status, the hint line, the slot
// read-outs, the wrapped preview) are cached and rebuilt only when what they
// show changes.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// cmdDeckUI holds the cached strings.
var cmdDeckUI struct {
	hintKey   uint32
	hint      string
	statusKey cmdDeckStatusKey
	statusSet bool
	status    string
	slotDisp  []string
	wrapLine  string
	wrapW     int32
	wrapFont  *ttf.Font
	wrapRows  []string
	confirmBt [2]sdl.Rect // Run, Cancel
	formRun   sdl.Rect    // the form's Run button; empty when it is not drawn
}

// cmdDeckRefreshSlotDisp rebuilds the form's value read-outs.
func cmdDeckRefreshSlotDisp() {
	cmdDeckUI.slotDisp = cmdDeckUI.slotDisp[:0]
	if cmdDeck.entry < 0 || cmdDeck.entry >= len(cmdDeck.entries) {
		return
	}
	e := &cmdDeck.entries[cmdDeck.entry]
	for k, s := range e.slots {
		v := ""
		if k < len(cmdDeck.values) {
			v = cmdDeck.values[k]
		}
		switch {
		case v == "":
			v = "Choose…"
		case s.kind == cmdSlotFolder || s.kind == cmdSlotFile:
			v = termShortPath(v, 44)
		case s.kind == cmdSlotInt || s.kind == cmdSlotChoice || s.kind == cmdSlotPattern:
			v = "‹  " + cmdChoiceLabel(v) + "  ›"
		case s.kind == cmdSlotProcess:
			v = "PID " + v
		}
		cmdDeckUI.slotDisp = append(cmdDeckUI.slotDisp, v)
	}
}

// cmdDeckHintKey identifies the hint line the frame needs.
func cmdDeckHintKey() uint32 {
	k := uint32(cmdDeck.mode) + 1
	if hintsUseKeyboard() {
		k |= 1 << 4
	}
	sub := uint32(0)
	switch cmdDeck.mode {
	case cmdModeBrowse:
		if it, ok := cmdDeckSelected(); ok {
			sub = uint32(it.kind) + 1
			if it.kind == cmdItemEntry {
				e := &cmdDeck.entries[it.entry]
				switch {
				case e.ask != "":
					sub = 8
				case len(e.slots) > 0:
					sub = 9
				}
			}
		}
	case cmdModePick:
		sub = uint32(cmdPick.sk) + 1
		if cmdPick.kind == cmdPickMacro {
			sub = 15
		}
	}
	return k | sub<<8
}

// cmdDeckHints is the card's hint row for the frame (cached by key).
func cmdDeckHints() string {
	key := cmdDeckHintKey()
	if key == cmdDeckUI.hintKey && cmdDeckUI.hint != "" {
		return cmdDeckUI.hint
	}
	cmdDeckUI.hintKey = key
	cmdDeckUI.hint = hintLine(cmdDeckFooterHints()...)
	return cmdDeckUI.hint
}

// cmdDeckFooterHints is what the buttons do right now. The Terminal's footer
// shows it too while the deck is open (termFooterHints).
func cmdDeckFooterHints() []FooterHint {
	kb := hintsUseKeyboard()
	back := FooterHint{Button: "B", Label: "Back"}
	if kb {
		back = FooterHint{Button: "Esc", Label: "Back"}
	}
	switch cmdDeck.mode {
	case cmdModeConfirm:
		if kb {
			return []FooterHint{{Button: "Enter", Label: "Run"}, {Button: "Esc", Label: "Cancel"}}
		}
		return []FooterHint{{Button: "A", Label: "Run"}, {Button: "B", Label: "Cancel"}}
	case cmdModePick:
		if cmdPick.kind == cmdPickMacro {
			if kb {
				return []FooterHint{{Button: "Space", Label: "Tick"}, {Button: "Ctrl+Enter", Label: "Save macro"}, back}
			}
			return []FooterHint{{Button: "A", Label: "Tick"}, {Button: "Start", Label: "Save macro"}, back}
		}
		switch cmdPick.sk {
		case cmdSlotFolder:
			if kb {
				return []FooterHint{{Button: "Enter", Label: "Open"}, {Button: "Ctrl+Enter", Label: "Use this folder"}, {Button: "Left", Label: "Up"}, back}
			}
			return []FooterHint{{Button: "A", Label: "Open"}, {Button: "Y", Label: "Use this folder"}, {Button: "Left", Label: "Up"}, back}
		case cmdSlotFile:
			if kb {
				return []FooterHint{{Button: "Enter", Label: "Choose"}, {Button: "Left", Label: "Up"}, back}
			}
			return []FooterHint{{Button: "A", Label: "Choose"}, {Button: "Left", Label: "Up"}, back}
		}
		if kb {
			return []FooterHint{{Button: "Enter", Label: "Choose"}, back}
		}
		return []FooterHint{{Button: "A", Label: "Choose"}, back}
	case cmdModeForm:
		if kb {
			return []FooterHint{{Button: "Enter", Label: "Choose"}, {Button: "Left/Right", Label: "Change"},
				{Button: "Ctrl+Enter", Label: "Run"}, {Button: "Ctrl+E", Label: "Edit"}, {Button: "Ctrl+S", Label: "Save"}, back}
		}
		return []FooterHint{{Button: "A", Label: "Choose"}, {Button: "Left/Right", Label: "Change"},
			{Button: "Start", Label: "Run"}, {Button: "Y", Label: "Edit"}, {Button: "X", Label: "Save"}, back}
	}
	closeHint := FooterHint{Button: "B", Label: "Close"}
	cat := FooterHint{Button: "L1/R1", Label: "Category"}
	if kb {
		closeHint = FooterHint{Button: "Esc", Label: "Close"}
		cat = FooterHint{Button: "Tab", Label: "Category"}
	}
	it, ok := cmdDeckSelected()
	if !ok {
		return []FooterHint{cat, closeHint}
	}
	open, run, edit, save := "A", "Start", "Y", "X"
	if kb {
		open, run, edit, save = "Enter", "Ctrl+Enter", "Ctrl+E", "Ctrl+S"
	}
	switch it.kind {
	case cmdItemNewMacro:
		return []FooterHint{{Button: open, Label: "Choose commands"}, cat, closeHint}
	case cmdItemFav:
		remove := save
		if kb {
			remove = "Del"
		}
		return []FooterHint{{Button: open, Label: "Run"}, {Button: edit, Label: "Edit"}, {Button: remove, Label: "Remove"}, cat, closeHint}
	}
	e := &cmdDeck.entries[it.entry]
	if e.ask != "" {
		return []FooterHint{{Button: open, Label: "Ask Juka"}, cat, closeHint}
	}
	if len(e.slots) > 0 {
		return []FooterHint{{Button: open, Label: "Fill in"}, {Button: run, Label: "Run"}, {Button: edit, Label: "Edit"}, {Button: save, Label: "Save"}, cat, closeHint}
	}
	return []FooterHint{{Button: open, Label: "Run"}, {Button: edit, Label: "Edit"}, {Button: save, Label: "Save"}, cat, closeHint}
}

// cmdDeckStatusKey is what the header status depends on; comparing it costs
// no allocation on a steady frame.
type cmdDeckStatusKey struct {
	mode   cmdDeckMode
	filter string
	cat    int
	n      int
	kb     bool
	ready  bool
	dir    string
	ticked int
	entry  int
	pick   cmdPickKind
}

// cmdDeckStatus is the header's right-hand read-out (cached).
func cmdDeckStatus() string {
	key := cmdDeckStatusKey{mode: cmdDeck.mode, filter: cmdDeck.filter, cat: cmdDeck.cat, kb: hintsUseKeyboard(),
		ready: cmdDeck.avail.ready, dir: cmdPick.dir, ticked: cmdPickMacroCount(), entry: cmdDeck.entry, pick: cmdPick.kind}
	if cmdDeck.filter != "" {
		key.n = len(cmdDeck.filtered)
	}
	if key == cmdDeckUI.statusKey && cmdDeckUI.statusSet {
		return cmdDeckUI.status
	}
	cmdDeckUI.statusSet = true
	cmdDeckUI.statusKey = key
	s := ""
	switch cmdDeck.mode {
	case cmdModeBrowse:
		switch {
		case cmdDeck.filter != "":
			s = "“" + cmdDeck.filter + "” · " + itoaSmall(len(cmdDeck.filtered))
		case hintsUseKeyboard():
			s = "Type to search"
		case !cmdDeck.avail.ready:
			s = "Checking this device…"
		}
	case cmdModePick:
		if cmdPick.kind == cmdPickMacro {
			s = itoaSmall(cmdPickMacroCount()) + " ticked"
		} else if cmdPick.dir != "" {
			s = termShortPath(cmdPick.dir, 40)
		}
	case cmdModeForm, cmdModeConfirm:
		e := &cmdDeck.entries[cmdDeck.entry]
		for _, c := range cmdDeckCats {
			if c.id == e.cat {
				s = c.title
			}
		}
	}
	cmdDeckUI.status = s
	return s
}

// cmdDeckWrap wraps line for the preview, cached by line, width and font.
func cmdDeckWrap(font *ttf.Font, line string, w int32) []string {
	if line != cmdDeckUI.wrapLine || w != cmdDeckUI.wrapW || font != cmdDeckUI.wrapFont {
		cmdDeckUI.wrapLine, cmdDeckUI.wrapW, cmdDeckUI.wrapFont = line, w, font
		cmdDeckUI.wrapRows = termWrap(font, line, w)
	}
	return cmdDeckUI.wrapRows
}

// cmdDeckCard is the sheet's rectangle for this canvas.
func cmdDeckCard() sdl.Rect {
	margin := scaleX(homeSideMargin, screenWidth)
	w := screenWidth - 2*margin
	if w > 980 {
		w = 980
	}
	top := headerBarHeight() + 12
	h := screenHeight - top - 24
	if h > 640 {
		h = 640
	}
	return sdl.Rect{X: (screenWidth - w) / 2, Y: top, W: w, H: h}
}

// renderCmdDeck draws the deck over the Terminal.
func renderCmdDeck(renderer *sdl.Renderer, config *Config, font, outFont *ttf.Font) {
	if font == nil || outFont == nil {
		return
	}
	drawOverlayScrim(renderer)
	card := cmdDeckCard()
	drawOverlayCard(renderer, card)
	cmdDeck.card = card

	mode := cmdDeck.mode
	if mode == cmdModeConfirm {
		mode = cmdDeck.confirmBack
	}
	title := "Commands"
	if mode == cmdModeForm {
		title = cmdDeck.entries[cmdDeck.entry].title
	} else if mode == cmdModePick {
		title = cmdPick.title
	}
	y := UIPageHeaderDraw(renderer, config, overlayPageHeader(card, title, cmdDeckStatus(), "terminal")) + 10

	hint := cmdDeckHints()
	hw, hh := measureText(font, hint)
	hintY := card.Y + card.H - hh - 14
	if cmdDeck.mode != cmdModeConfirm {
		drawText(renderer, font, ellipsize(font, hint, card.W-2*OverlayPadX), card.X+(card.W-minI32(hw, card.W-2*OverlayPadX))/2, hintY, ColorTextTertiary(), textAlignLeft)
	}
	body := sdl.Rect{X: card.X + OverlayPadX, Y: y, W: card.W - 2*OverlayPadX, H: hintY - 10 - y}

	cmdDeckUI.formRun = sdl.Rect{}
	switch mode {
	case cmdModeForm:
		renderCmdDeckForm(renderer, config, font, outFont, body)
	case cmdModePick:
		renderCmdDeckPick(renderer, config, font, body)
	default:
		renderCmdDeckBrowse(renderer, config, font, body)
	}
	if cmdDeck.mode == cmdModeConfirm {
		renderCmdDeckConfirm(renderer, config, font, outFont)
	}
}

func minI32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

// renderCmdDeckBrowse draws the rail and the rows.
func renderCmdDeckBrowse(renderer *sdl.Renderer, config *Config, font *ttf.Font, body sdl.Rect) {
	fh := int32(font.Height())
	railW := int32(210)
	if body.W < 760 {
		railW = 168
	}
	railRowH := fh + 14
	cmdDeck.railRows = cmdDeck.railRows[:0]
	ry := body.Y
	for i, c := range cmdDeck.cats {
		if ry+railRowH > body.Y+body.H {
			break
		}
		row := sdl.Rect{X: body.X, Y: ry, W: railW, H: railRowH}
		selected := i == cmdDeck.cat && cmdDeck.filter == ""
		UIListRow(renderer, row, glassRowRadius, selected, pointInRect(mouseX, mouseY, row))
		ink := ColorTextSecondary()
		if selected {
			ink = ColorTextPrimary()
		}
		label := ellipsize(font, c.title, row.W-24)
		drawText(renderer, font, label, row.X+12, row.Y+(row.H-fh)/2, ink, textAlignLeft)
		cmdDeck.railRows = append(cmdDeck.railRows, row)
		ry += railRowH + 4
	}

	list := sdl.Rect{X: body.X + railW + 16, Y: body.Y, W: body.W - railW - 16, H: body.H}
	cmdDeck.rows = cmdDeck.rows[:0]
	items := cmdDeckItems()
	if len(items) == 0 {
		cmdDeck.visible = 1
		ttl, txt := "Nothing here", "No command in this group can run on this device."
		switch {
		case cmdDeck.filter != "":
			ttl, txt = "No matches", "No command contains that text."
		case !cmdDeck.avail.ready:
			ttl, txt = "Checking this device…", "Finding which commands can run here."
		}
		UIEmptyState(renderer, config, list, "@terminal", ttl, txt)
		return
	}
	rowH := 2*fh + 22
	gap := int32(6)
	visible := int((list.H + gap) / (rowH + gap))
	if visible < 1 {
		visible = 1
	}
	cmdDeck.visible = visible
	if cmdDeck.cursor < cmdDeck.first {
		cmdDeck.first = cmdDeck.cursor
	}
	if cmdDeck.cursor >= cmdDeck.first+visible {
		cmdDeck.first = cmdDeck.cursor - visible + 1
	}
	if maxFirst := len(items) - visible; cmdDeck.first > maxFirst {
		cmdDeck.first = maxFirst
	}
	if cmdDeck.first < 0 {
		cmdDeck.first = 0
	}
	cmdDeck.rowsFirst = cmdDeck.first
	for i := cmdDeck.first; i < len(items) && i < cmdDeck.first+visible; i++ {
		it := items[i]
		row := sdl.Rect{X: list.X, Y: list.Y + int32(i-cmdDeck.first)*(rowH+gap), W: list.W, H: rowH}
		UIListRow(renderer, row, glassRowRadius, i == cmdDeck.cursor, pointInRect(mouseX, mouseY, row))
		titleInk := ColorTextPrimary()
		if it.kind == cmdItemNewMacro {
			titleInk = ColorTextAccent()
		}
		drawText(renderer, font, ellipsize(font, it.title, row.W-32), row.X+16, row.Y+10, titleInk, textAlignLeft)
		if it.sub != "" {
			subInk := ColorTextTertiary()
			if it.kind == cmdItemEntry && cmdDeck.entries[it.entry].danger != "" {
				subInk = DangerInk()
			}
			drawText(renderer, font, ellipsize(font, it.sub, row.W-32), row.X+16, row.Y+12+fh, subInk, textAlignLeft)
		}
		cmdDeck.rows = append(cmdDeck.rows, row)
	}
	if len(items) > visible {
		thumb := float64(visible) / float64(len(items))
		pos := float64(cmdDeck.first) / float64(len(items)-visible)
		drawScrollbar(renderer, list.X+list.W+OverlayPadX/2-2, list.Y, 4, list.H, thumb, pos)
	}
}

// renderCmdDeckForm draws the preview, the slots and the Run button.
func renderCmdDeckForm(renderer *sdl.Renderer, config *Config, font, outFont *ttf.Font, body sdl.Rect) {
	if len(cmdDeckUI.slotDisp) != len(cmdDeck.values) {
		cmdDeckRefreshSlotDisp()
	}
	e := &cmdDeck.entries[cmdDeck.entry]
	fh := int32(font.Height())
	lineH := int32(outFont.Height()) + 2
	rows := cmdDeckWrap(outFont, cmdDeck.preview, body.W-36)
	n := len(rows)
	if n > 4 {
		n = 4
	}
	if n < 1 {
		n = 1
	}
	box := sdl.Rect{X: body.X, Y: body.Y, W: body.W, H: int32(n)*lineH + 24}
	fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, homeTileRadius, glassFill())
	strokeRoundedRect(renderer, box.X, box.Y, box.W, box.H, homeTileRadius, 1, glassEdge())
	ink := ColorTextPrimary()
	if !cmdDeck.previewOK {
		ink = ColorTextTertiary()
	}
	for i := 0; i < n && i < len(rows); i++ {
		text := rows[i]
		if i == n-1 && len(rows) > n {
			text = ellipsize(outFont, text+" …", box.W-36)
		}
		drawText(renderer, outFont, text, box.X+18, box.Y+12+int32(i)*lineH, ink, textAlignLeft)
	}
	y := box.Y + box.H + 10
	if e.danger != "" {
		drawText(renderer, font, ellipsize(font, "Asks before running · "+e.danger, body.W), body.X, y, DangerInk(), textAlignLeft)
		y += fh + 10
	}
	// Run, bottom right under the slots, for the mouse: the pad has Start and
	// the keyboard Ctrl+Enter (the hint line). An incomplete form opens the
	// missing slot's picker, as Start does (cmdDeckFormAction).
	runW, _ := measureText(font, "Run")
	runW += 72
	btnH := fh + 20
	runBtn := sdl.Rect{X: body.X + body.W - runW, Y: body.Y + body.H - btnH, W: runW, H: btnH}
	runVariant := UIButtonPrimary
	if e.danger != "" {
		runVariant = UIButtonDanger
	}
	UIButton(renderer, config, runBtn, "Run", UIButtonStyle{Variant: runVariant, Font: font, Pill: true},
		UIButtonState{Hovered: cmdDeck.mode == cmdModeForm && pointInRect(mouseX, mouseY, runBtn)})
	cmdDeckUI.formRun = runBtn
	slotsBottom := runBtn.Y - 10

	rowH := fh + 22
	cmdDeck.slotRows = cmdDeck.slotRows[:0]
	for k, s := range e.slots {
		if y+rowH > slotsBottom {
			break
		}
		row := sdl.Rect{X: body.X, Y: y, W: body.W, H: rowH}
		UIListRow(renderer, row, glassRowRadius, k == cmdDeck.slot, pointInRect(mouseX, mouseY, row))
		drawText(renderer, font, s.label, row.X+16, row.Y+(rowH-fh)/2, ColorTextSecondary(), textAlignLeft)
		if k < len(cmdDeckUI.slotDisp) {
			v := cmdDeckUI.slotDisp[k]
			vInk := ColorTextPrimary()
			if k < len(cmdDeck.values) && cmdDeck.values[k] == "" {
				vInk = ColorTextAccent()
			}
			lw, _ := measureText(font, s.label)
			v = ellipsize(font, v, row.W-lw-56)
			drawText(renderer, font, v, row.X+row.W-16, row.Y+(rowH-fh)/2, vInk, textAlignRight)
		}
		cmdDeck.slotRows = append(cmdDeck.slotRows, row)
		y += rowH + 6
	}
}

// renderCmdDeckPick draws a picker's rows.
func renderCmdDeckPick(renderer *sdl.Renderer, config *Config, font *ttf.Font, body sdl.Rect) {
	fh := int32(font.Height())
	if cmdPick.note != "" {
		body.H -= fh + 8
		drawText(renderer, font, ellipsize(font, cmdPick.note, body.W), body.X, body.Y+body.H+4, ColorTextTertiary(), textAlignLeft)
	}
	cmdPick.rowRects = cmdPick.rowRects[:0]
	if cmdPick.loading {
		cmdPick.visible = 1
		UIEmptyState(renderer, config, body, "@terminal", "Loading…", "")
		return
	}
	if len(cmdPick.rows) == 0 {
		cmdPick.visible = 1
		UIEmptyState(renderer, config, body, "@terminal", "Nothing to choose", "There is nothing here to pick.")
		return
	}
	rowH := fh + 22
	gap := int32(6)
	visible := int((body.H + gap) / (rowH + gap))
	if visible < 1 {
		visible = 1
	}
	cmdPick.visible = visible
	if cmdPick.cursor < cmdPick.first {
		cmdPick.first = cmdPick.cursor
	}
	if cmdPick.cursor >= cmdPick.first+visible {
		cmdPick.first = cmdPick.cursor - visible + 1
	}
	if maxFirst := len(cmdPick.rows) - visible; cmdPick.first > maxFirst {
		cmdPick.first = maxFirst
	}
	if cmdPick.first < 0 {
		cmdPick.first = 0
	}
	cmdPick.rowsFirst = cmdPick.first
	accent := glassAccent()
	for i := cmdPick.first; i < len(cmdPick.rows) && i < cmdPick.first+visible; i++ {
		r := cmdPick.rows[i]
		row := sdl.Rect{X: body.X, Y: body.Y + int32(i-cmdPick.first)*(rowH+gap), W: body.W, H: rowH}
		UIListRow(renderer, row, glassRowRadius, i == cmdPick.cursor, pointInRect(mouseX, mouseY, row))
		x := row.X + 16
		if cmdPick.kind == cmdPickMacro {
			bs := fh - 2
			by := row.Y + (rowH-bs)/2
			if i < len(cmdPick.checked) && cmdPick.checked[i] {
				fillRoundedRect(renderer, x, by, bs, bs, 5, accent)
			} else {
				strokeRoundedRect(renderer, x, by, bs, bs, 5, 2, ColorTextTertiary())
			}
			x += bs + 12
		}
		ink := ColorTextPrimary()
		switch {
		case r.use:
			ink = ColorTextAccent()
		case r.up:
			ink = ColorTextSecondary()
		}
		detailW := int32(0)
		if r.detail != "" {
			d := ellipsize(font, r.detail, row.W/2-24)
			detailW, _ = measureText(font, d)
			drawText(renderer, font, d, row.X+row.W-16, row.Y+(rowH-fh)/2, ColorTextTertiary(), textAlignRight)
		}
		label := r.label
		if r.dir {
			label += "  ›"
		}
		drawText(renderer, font, ellipsize(font, label, row.X+row.W-x-detailW-40), x, row.Y+(rowH-fh)/2, ink, textAlignLeft)
		cmdPick.rowRects = append(cmdPick.rowRects, row)
	}
	if len(cmdPick.rows) > visible {
		thumb := float64(visible) / float64(len(cmdPick.rows))
		pos := float64(cmdPick.first) / float64(len(cmdPick.rows)-visible)
		drawScrollbar(renderer, body.X+body.W+OverlayPadX/2-2, body.Y, 4, body.H, thumb, pos)
	}
}

// renderCmdDeckConfirm draws the "run this?" card over the deck.
func renderCmdDeckConfirm(renderer *sdl.Renderer, config *Config, font, outFont *ttf.Font) {
	drawOverlayScrim(renderer)
	outer := cmdDeck.card
	w := outer.W - 120
	if w > 640 {
		w = 640
	}
	fh := int32(font.Height())
	lineH := int32(outFont.Height()) + 2
	// Its own cache: from a form, the preview under this card is wrapped at
	// another width every frame, and a shared cache would re-wrap both.
	rows := cmdDeckConfirmWrap.wrap(outFont, cmdDeck.confirmLine, w-2*OverlayPadX)
	n := len(rows)
	if n > 4 {
		n = 4
	}
	btnH := fh + 20
	h := int32(24) + fh + 14 + int32(n)*lineH + 14 + 2*fh + 20 + btnH + 24
	card := sdl.Rect{X: outer.X + (outer.W-w)/2, Y: outer.Y + (outer.H-h)/2, W: w, H: h}
	drawOverlayCard(renderer, card)
	x := card.X + OverlayPadX
	y := card.Y + 24
	drawText(renderer, font, "Run this command?", x, y, ColorTextPrimary(), textAlignLeft)
	y += fh + 14
	for i := 0; i < n; i++ {
		text := rows[i]
		if i == n-1 && len(rows) > n {
			text = ellipsize(outFont, text+" …", w-2*OverlayPadX)
		}
		drawText(renderer, outFont, text, x, y, ColorTextPrimary(), textAlignLeft)
		y += lineH
	}
	y += 14
	why := cmdDeckWhyCache.wrap(font, cmdDeck.confirmWhy, w-2*OverlayPadX)
	for i := 0; i < len(why) && i < 2; i++ {
		drawText(renderer, font, why[i], x, y, DangerInk(), textAlignLeft)
		y += fh
	}
	y = card.Y + card.H - 24 - btnH
	bw := (w - 2*OverlayPadX - 12) / 2
	kb := hintsUseKeyboard()
	runLabel, cancelLabel := "A  Run", "B  Cancel"
	if kb {
		runLabel, cancelLabel = "Enter  Run", "Esc  Cancel"
	}
	cancel := sdl.Rect{X: x, Y: y, W: bw, H: btnH}
	run := sdl.Rect{X: x + bw + 12, Y: y, W: bw, H: btnH}
	UIButton(renderer, config, cancel, cancelLabel, UIButtonStyle{Variant: UIButtonSecondary, Font: font, Pill: true}, UIButtonState{Hovered: pointInRect(mouseX, mouseY, cancel)})
	UIButton(renderer, config, run, runLabel, UIButtonStyle{Variant: UIButtonDanger, Font: font, Pill: true}, UIButtonState{Focused: true, Hovered: pointInRect(mouseX, mouseY, run)})
	cmdDeckUI.confirmBt = [2]sdl.Rect{run, cancel}
}

// cmdDeckWrapCache is one wrapped text, kept until the text, width or font
// changes. The confirm card's command and its reason each have one, so they
// and the form's preview do not evict each other every frame.
type cmdDeckWrapCache struct {
	text string
	w    int32
	font *ttf.Font
	rows []string
}

var cmdDeckConfirmWrap, cmdDeckWhyCache cmdDeckWrapCache

func (c *cmdDeckWrapCache) wrap(font *ttf.Font, text string, w int32) []string {
	if text != c.text || w != c.w || font != c.font {
		c.text, c.w, c.font = text, w, font
		c.rows = termWrap(font, text, w)
	}
	return c.rows
}
