package main

// fileeditor_hex_ui.go — the hex editor's body, keys and clicks
// (fileeditor_hex.go holds the bytes and saves them; fileeditor.go is the
// page around it).
//
// Inter is not monospaced, so a row is not one string: every byte gets a
// cell as wide as the widest digit pair, drawn on its own, which keeps the
// columns straight and puts the cursor's box exactly on its byte. The texture
// cache keys on the text, so the 256 possible pairs are at most 256 textures
// however large the file. The printable characters stay one string per row.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// hexLayout is where a frame put the columns: the offset, each byte's cell
// and the characters (textX 0 when they do not fit).
type hexLayout struct {
	perRow int
	digits int
	digitW int32
	cellW  int32
	offX   int32
	cellX  [16]int32
	textX  int32
}

// hexPairs are the 256 two-digit texts, made once.
var hexPairs = func() (p [256]string) {
	const digits = "0123456789ABCDEF"
	for i := range p {
		p[i] = string([]byte{digits[i>>4], digits[i&0x0F]})
	}
	return p
}()

// hexDigitWidth caches the widest hex digit of the body font.
var hexDigitWidth struct {
	font *ttf.Font
	w    int32
}

// hexWidestDigit is the width of the widest hex digit in font.
func hexWidestDigit(font *ttf.Font) int32 {
	if hexDigitWidth.font == font && hexDigitWidth.w > 0 {
		return hexDigitWidth.w
	}
	w := int32(0)
	for i := 0; i < 16; i++ {
		if dw, _ := measureTextRaw(font, hexPairs[i][1:]); dw > w {
			w = dw
		}
	}
	if w <= 0 {
		w = 10
	}
	hexDigitWidth.font, hexDigitWidth.w = font, w
	return w
}

// hexLayoutFor fits the columns into body: sixteen bytes a row when they and
// their characters fit, else eight (a large text size, a narrow screen),
// dropping the characters only when even eight bytes need the room.
func hexLayoutFor(font *ttf.Font, body sdl.Rect, size int64) hexLayout {
	dw := hexWidestDigit(font)
	lay := hexLayout{digitW: dw, cellW: 2*dw + dw/2 + 6, digits: 8, offX: body.X + 16}
	for lay.digits < 16 && (size-1)>>(4*uint(lay.digits)) > 0 {
		lay.digits++
	}
	groupGap := dw/2 + 6
	cellsX := lay.offX + int32(lay.digits)*dw + 24
	right := body.X + body.W - 22
	place := func(per int, withText, force bool) bool {
		cellsW := int32(per)*lay.cellW + int32(per/4-1)*groupGap
		textX := cellsX + cellsW + 18
		if !force && withText && textX+int32(per)*dw > right {
			return false
		}
		if !force && !withText && cellsX+cellsW > right {
			return false
		}
		lay.perRow = per
		for b := 0; b < per; b++ {
			lay.cellX[b] = cellsX + int32(b)*lay.cellW + int32(b/4)*groupGap
		}
		lay.textX = 0
		if withText {
			lay.textX = textX
		}
		return true
	}
	if !place(16, true, false) && !place(8, true, false) && !place(8, false, false) {
		// Too narrow even for that: eight bytes, cut off at the right.
		place(8, false, true)
	}
	return lay
}

// renderHexBody draws the hex editor's rows: the offset, a cell per byte and
// the bytes as characters. The cursor's byte is boxed (its digit underlined
// while it is being edited), changed bytes are in the warning colour and zero
// bytes are dimmed, so the data stands out from the padding.
func renderHexBody(renderer *sdl.Renderer, config *Config, font *ttf.Font, body sdl.Rect) {
	h := &fileEditor.hex
	rowH := int32(font.Height()) + 6
	visible := int((body.H - 16) / rowH)
	if visible < 1 {
		visible = 1
	}
	lay := hexLayoutFor(font, body, fileEditor.size)
	h.perRow, h.rows, h.lay, h.body, h.rowH = lay.perRow, visible, lay, body, rowH
	fileEditor.viewLines = visible
	fileEditorRowRects = fileEditorRowRects[:0]
	if fileEditor.size <= 0 {
		filesDrawPlainText(renderer, config, font, "This file is empty", ColorTextTertiary(), body.X+16, body.Y+12)
		return
	}
	per := int64(lay.perRow)
	totalRows := (fileEditor.size + per - 1) / per
	hexFollowCursor()
	if h.top > totalRows-int64(visible) {
		h.top = max(0, totalRows-int64(visible))
	}
	first := h.top * per
	if !hexEnsureWindow(first, int64(visible)*per) {
		filesDrawPlainText(renderer, config, font, "Cannot read the file: "+h.readErr, WarningInk(), body.X+16, body.Y+12)
		return
	}
	accent := glassAccent()
	dim, plain, changed := ColorTextTertiary(), ColorTextPrimary(), WarningInk()
	y := body.Y + 8
	renderWithClip(renderer, body.X+2, body.Y+2, body.W-4, body.H-4, func(r *sdl.Renderer) {
		var chars [16]byte
		for row := 0; row < visible; row++ {
			off := first + int64(row)*per
			if off >= fileEditor.size {
				break
			}
			n := int(min(per, fileEditor.size-off))
			onRow := h.cursor >= off && h.cursor < off+int64(n)
			offInk := dim
			if onRow {
				fillRoundedRect(r, body.X+6, y, body.W-12, rowH, 8, WithAlpha(accent, 22))
				offInk = ColorTextSecondary()
			}
			filesDrawPlainText(r, config, font, hexOffsetText(off, lay.digits), offInk, lay.offX, y+3)
			for b := 0; b < n; b++ {
				at := off + int64(b)
				v, patched, _ := hexByteAt(at)
				x := lay.cellX[b]
				ink := plain
				switch {
				case patched:
					ink = changed
				case v == 0:
					ink = dim
				}
				if at == h.cursor {
					boxW := 2*lay.digitW + 8
					if h.editing {
						fillRoundedRect(r, x-4, y+1, boxW, rowH-2, 6, WithAlpha(accent, 96))
						strokeRoundedRect(r, x-4, y+1, boxW, rowH-2, 6, 2, accent)
					} else {
						fillRoundedRect(r, x-4, y+1, boxW, rowH-2, 6, WithAlpha(accent, 60))
					}
				}
				if at == h.cursor && h.editing {
					// Each digit in its own half, the one Up/Down counts
					// underlined.
					pair := hexPairs[v]
					for d := 0; d < 2; d++ {
						digit := pair[d : d+1]
						w, _ := measureTextRaw(font, digit)
						dx := x + int32(d)*lay.digitW + (lay.digitW-w)/2
						filesDrawPlainText(r, config, font, digit, ink, dx, y+3)
					}
					setDrawColor(r, accent)
					r.FillRect(&sdl.Rect{X: x + int32(h.nibble)*lay.digitW, Y: y + rowH - 5, W: lay.digitW, H: 3})
				} else {
					pair := hexPairs[v]
					w, _ := measureTextRaw(font, pair)
					filesDrawPlainText(r, config, font, pair, ink, x+(2*lay.digitW-w)/2, y+3)
				}
				chars[b] = '.'
				if v >= 0x20 && v < 0x7F {
					chars[b] = v
				}
			}
			if lay.textX > 0 {
				text := string(chars[:n])
				if onRow {
					col := int(h.cursor - off)
					// Measured with a bar after it: a width that ends in a
					// space is where font versions disagree.
					bar, _ := measureTextRaw(font, "|")
					before, _ := measureTextRaw(font, text[:col]+"|")
					through, _ := measureTextRaw(font, text[:col+1]+"|")
					cx, cw := lay.textX+before-bar, through-before
					if cw < 4 {
						cw = 4
					}
					fillRoundedRect(r, cx-1, y+1, cw+2, rowH-2, 4, WithAlpha(accent, 60))
				}
				filesDrawPlainText(r, config, font, text, ColorTextSecondary(), lay.textX, y+3)
			}
			y += rowH
		}
	})
	if thumb := float64(visible) / float64(totalRows); thumb < 1 {
		pos := 0.0
		if totalRows > int64(visible) {
			pos = float64(h.top) / float64(totalRows-int64(visible))
		}
		drawScrollbar(renderer, body.X+body.W-8, body.Y+10, 3, body.H-20, thumb, pos)
	}
}

// hexPosition is the header's position for the hex editor: the cursor's
// offset, the file's size and the byte under the cursor in hex, decimal and
// as a character.
func hexPosition() string {
	h := &fileEditor.hex
	if fileEditor.size <= 0 {
		return "empty file"
	}
	where := "offset " + hexOffsetText(h.cursor, 8) + " of " + humanBytes(fileEditor.size)
	v, _, ok := hexByteAt(h.cursor)
	if !ok {
		return where
	}
	where += " · " + hexPairs[v] + " = " + filesNumberString(int64(v))
	if v >= 0x21 && v < 0x7F {
		where += " '" + string(rune(v)) + "'"
	}
	return where
}

// hexKeyDigit is the hex digit a key types.
func hexKeyDigit(sym sdl.Keycode) (byte, bool) {
	switch {
	case sym >= sdl.K_0 && sym <= sdl.K_9:
		return byte(sym - sdl.K_0), true
	case sym >= sdl.K_a && sym <= sdl.K_f:
		return byte(sym-sdl.K_a) + 10, true
	case sym >= sdl.K_KP_1 && sym <= sdl.K_KP_9:
		return byte(sym-sdl.K_KP_1) + 1, true
	case sym == sdl.K_KP_0:
		return 0, true
	}
	return 0, false
}

// hexKey answers the keys the hex editor reads its own way: hex digits type
// (A–F included, so B is not Close here: Esc is), Left/Right move by a byte,
// and while a byte is being edited the arrows count and step its digits.
// False leaves the key to handleFileEditorKey.
func hexKey(e *sdl.KeyboardEvent) bool {
	h := &fileEditor.hex
	if fileEditor.confirm {
		return false
	}
	sym := e.Keysym.Sym
	if e.Keysym.Mod&uint16(sdl.KMOD_CTRL) != 0 {
		switch sym {
		case sdl.K_z:
			hexUndoLast()
			return true
		case sdl.K_g:
			hexStartGoto()
			return true
		}
		return false
	}
	if d, ok := hexKeyDigit(sym); ok {
		hexTypeDigit(d)
		return true
	}
	if h.editing {
		switch sym {
		case sdl.K_UP:
			hexStepNibble(1)
		case sdl.K_DOWN:
			hexStepNibble(-1)
		case sdl.K_LEFT, sdl.K_BACKSPACE:
			hexNibbleMove(-1)
		case sdl.K_RIGHT:
			hexNibbleMove(1)
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			hexEndEdit()
		case sdl.K_ESCAPE:
			hexCancelEdit()
		case sdl.K_y:
			hexEndEdit()
			fileEditorSaveAndReport()
		default:
			// Paging and the menu work as without an edit.
			return false
		}
		return true
	}
	switch sym {
	case sdl.K_LEFT:
		hexMoveBy(-1)
	case sdl.K_RIGHT:
		hexMoveBy(1)
	default:
		return false
	}
	return true
}

// hexController is hexKey for the pad. While a byte is being edited the
// D-pad counts (Up/Down) and steps (Left/Right) its digits, A keeps the
// change and B puts it back; otherwise Left/Right move a byte, A edits and X
// types bytes. False leaves the button to ctrlFileEditor.
func hexController(e *sdl.ControllerButtonEvent) bool {
	h := &fileEditor.hex
	if fileEditor.confirm {
		return false
	}
	if h.editing {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			hexStepNibble(1)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			hexStepNibble(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
			hexNibbleMove(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
			hexNibbleMove(1)
		case sdl.CONTROLLER_BUTTON_A:
			hexEndEdit()
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			hexCancelEdit()
		case sdl.CONTROLLER_BUTTON_X:
			hexStartTyping()
		case sdl.CONTROLLER_BUTTON_Y:
			hexEndEdit()
			fileEditorSaveAndReport()
		default:
			return false
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		hexMoveBy(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		hexMoveBy(1)
	case sdl.CONTROLLER_BUTTON_A:
		hexBeginEdit()
	case sdl.CONTROLLER_BUTTON_X:
		hexStartTyping()
	default:
		return false
	}
	return true
}

// hexMouse answers a click in the body: a byte's cell selects it, and a
// click on the selected byte edits it (or ends its edit).
func hexMouse(mx, my int32) bool {
	h := &fileEditor.hex
	if h.rowH <= 0 || !pointInRect(mx, my, h.body) {
		return false
	}
	top := h.body.Y + 8
	if my < top {
		return true
	}
	row := int64((my - top) / h.rowH)
	if row >= int64(h.rows) {
		return true
	}
	col := -1
	for b := 0; b < h.lay.perRow; b++ {
		x := h.lay.cellX[b]
		if mx >= x-6 && mx < x+2*h.lay.digitW+6 {
			col = b
			break
		}
	}
	if col < 0 {
		return true
	}
	off := (h.top+row)*int64(h.lay.perRow) + int64(col)
	if off >= fileEditor.size {
		return true
	}
	if off == h.cursor {
		if h.editing {
			hexEndEdit()
		} else {
			hexBeginEdit()
		}
		return true
	}
	h.cursor, h.editing = off, false
	PlayNavSound()
	return true
}

// hexFooterHints are the keys the hex editor answers in its current state.
// A keyboard types digits straight in, so its footer says so first.
func hexFooterHints() []FooterHint {
	var hints []FooterHint
	if hintsUseKeyboard() && !fileEditor.readOnly {
		hints = append(hints, FooterHint{Button: "0–F", Label: "Type"})
	}
	switch {
	case fileEditor.hex.editing:
		return append(hints,
			FooterHint{Button: "Up/Down", Label: "Change digit"},
			FooterHint{Button: "Left/Right", Label: "Next digit"},
			FooterHint{Button: "A", Label: "Done"},
			FooterHint{Button: "B", Label: "Undo edit"},
			FooterHint{Button: "Y", Label: "Save"},
		)
	case fileEditor.readOnly:
		return append(hints,
			FooterHint{Button: "D-Pad", Label: "Move"},
			FooterHint{Button: "L1/R1", Label: "Page"},
			FooterHint{Button: "L2/R2", Label: "Top / End"},
			FooterHint{Button: "Start / Tab", Label: "View"},
			FooterHint{Button: "B", Label: "Close"},
		)
	}
	return append(hints,
		FooterHint{Button: "D-Pad", Label: "Move"},
		FooterHint{Button: "A", Label: "Edit byte"},
		FooterHint{Button: "X", Label: "Type bytes"},
		FooterHint{Button: "Y", Label: "Save"},
		FooterHint{Button: "L1/R1", Label: "Page"},
		FooterHint{Button: "Start / Tab", Label: "View"},
		FooterHint{Button: "B", Label: "Close"},
	)
}
