package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Notification Center
// ──────────────────────────────────────────────────────────────────────

type notifPriority int

const (
	notifInfo notifPriority = iota
	notifSuccess
	notifWarning
	notifError
)

type notification struct {
	Message   string
	Priority  notifPriority
	Timestamp time.Time
	Read      bool
	// Repeat counts how many identical events were folded into this row, so a
	// chatty producer (a retry loop, a volume slider) shows "×3" instead of
	// pushing everything else out of the 50-entry history.
	Repeat int
}

type notifCenter struct {
	history   []notification
	open      bool // dropdown visible
	cursor    int  // focused row in dropdown
	bellRect  sdl.Rect
	dropRect  sdl.Rect
	clearRect sdl.Rect // "Clear All" hit area
	enter     uiEnter  // drop-down + fade entrance
	// visibleRows and rowRects describe the rows the last frame drew; the
	// cursor limit and click selection are driven by them instead of a
	// hard-coded row count.
	visibleRows int
	rowRects    []sdl.Rect
}

var nc notifCenter

// toastKindPriority maps a toast severity onto the history's own scale.
func toastKindPriority(k ToastKind) notifPriority {
	switch k {
	case ToastKindSuccess:
		return notifSuccess
	case ToastKindWarning:
		return notifWarning
	case ToastKindError:
		return notifError
	default:
		return notifInfo
	}
}

// priorityToastKind is the reverse mapping, used when a caller supplies the
// priority itself (notifPush) and the toast still has to render.
func priorityToastKind(p notifPriority) ToastKind {
	switch p {
	case notifSuccess:
		return ToastKindSuccess
	case notifWarning:
		return ToastKindWarning
	case notifError:
		return ToastKindError
	default:
		return ToastKindInfo
	}
}

// notifRecord files a message in the history without showing a toast. It is
// the single writer for nc.history: ShowToast calls it for every user-facing
// toast, so downloads, update prompts, screenshots and errors all survive the
// toast timeout instead of vanishing after three seconds.
//
// A repeat of the newest unread row is folded into it (Timestamp refreshed,
// Repeat bumped) rather than appended, so a loop cannot flush the history.
func notifRecord(msg string, p notifPriority) {
	if msg == "" {
		return
	}
	if len(nc.history) > 0 {
		top := &nc.history[0]
		if top.Message == msg && top.Priority == p && !top.Read {
			top.Timestamp = time.Now()
			top.Repeat++
			return
		}
	}
	nc.history = append([]notification{{Message: msg, Priority: p, Timestamp: time.Now(), Repeat: 1}}, nc.history...)
	if len(nc.history) > 50 {
		nc.history = nc.history[:50]
	}
}

// notifPush files a notification and shows its toast. It records directly
// (not through ShowToast) so the message is stored exactly once.
func notifPush(msg string, p notifPriority) {
	notifRecord(msg, p)
	showToastPrimitive(msg, priorityToastKind(p))
}

func notifUnreadCount() int {
	n := 0
	for _, notif := range nc.history {
		if !notif.Read {
			n++
		}
	}
	return n
}

// notifBellSize returns the bell pill's width and height for the active font,
// so the top bar can reserve its corner before drawing. The icon is drawn from
// primitives, so the pill is sized from the font height rather than a glyph.
func notifBellSize(config *Config) (int32, int32) {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return 0, 0
	}
	h := int32(font.Height()) + 10
	if h < 24 {
		h = 24
	}
	return h + 14, h
}

// drawBellIcon paints a notification bell centred in a size×size box using only
// SDL primitives. A text glyph such as "(*)" was hard to read on the handheld's
// small screen and looked like an error marker, not a notification control.
func drawBellIcon(renderer *sdl.Renderer, cx, cy, size int32, col sdl.Color) {
	if renderer == nil || size < 10 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, col)

	f := float64(size)
	bodyTop := float64(cy) - f*0.36
	bodyH := f * 0.58
	bodyBottom := bodyTop + bodyH
	halfMax := f * 0.34

	// Dome and flared skirt. The width grows smoothly from the rounded top to
	// the rim, which reads as a bell even at 14–16 px.
	for y := int32(math.Round(bodyTop)); y <= int32(math.Round(bodyBottom)); y++ {
		t := (float64(y) - bodyTop) / bodyH
		if t < 0 || t > 1 {
			continue
		}
		half := halfMax * (0.40 + 0.60*t)
		if t < 0.28 {
			half = halfMax * math.Sqrt(0.16+(t/0.28)*0.84)
		}
		renderer.DrawLine(cx-int32(math.Round(half)), y, cx+int32(math.Round(half)), y)
	}

	// Hanger knob.
	knobR := int32(math.Max(1, f*0.09))
	fillCircle(renderer, cx, int32(math.Round(bodyTop))-knobR+1, knobR, col)

	// Rim and clapper.
	rimW := int32(math.Round(f * 0.88))
	rimH := int32(math.Max(2, f*0.13))
	rimY := int32(math.Round(bodyBottom)) - rimH/2
	fillRoundedRect(renderer, cx-rimW/2, rimY, rimW, rimH, rimH/2, col)
	clapR := int32(math.Max(1, f*0.11))
	fillCircle(renderer, cx, int32(math.Round(bodyBottom+f*0.20)), clapR, col)

	// Soft highlight on the left shoulder gives the silhouette a little depth.
	if size >= 18 {
		sheen := sdl.Color{R: 255, G: 255, B: 255, A: 55}
		for y := int32(math.Round(bodyTop + f*0.20)); y < int32(math.Round(bodyTop+f*0.45)); y++ {
			t := (float64(y) - bodyTop) / bodyH
			half := halfMax * (0.40 + 0.60*t)
			x := cx - int32(math.Round(half)) + 2
			renderer.SetDrawColor(sheen.R, sheen.G, sheen.B, sheen.A)
			renderer.DrawLine(x, y, x+1, y)
		}
	}
}

// drawNotifBell paints the bell pill, focus/open ring, icon and unread badge.
// Every screen (home header and the scene status bar) funnels through here, so
// the control cannot drift between the home screen and the rest of the app.
func drawNotifBell(renderer *sdl.Renderer, font *ttf.Font, rect sdl.Rect) {
	if renderer == nil || font == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}

	// Background pill in the active theme's input surface.
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, ColorIconSurface)
	if nc.open {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, rect.H/2, 1, accentColor)
	}

	// Focus ring when the D-pad/arrows sit on the bell.
	if focusEngine.ElementIndex() == ChromeBellIndex {
		strokeRoundedRect(renderer, rect.X-2, rect.Y-2, rect.W+4, rect.H+4, (rect.H+4)/2, 2, accentColor)
	}

	// Bell icon: primary text colour so it reads as a control, with the accent
	// ring already marking focus/open state.
	drawBellIcon(renderer, rect.X+rect.W/2, rect.Y+rect.H/2, rect.H-8, ColorTextPrimary())

	// Unread count badge, measured from the same font the bell is sized for so
	// its pill and digits match on every screen.
	if unread := notifUnreadCount(); unread > 0 {
		numStr := fmt.Sprintf("%d", unread)
		tw, th, _ := font.SizeUTF8(numStr)
		bw := int32(tw) + SpaceSM*2
		bh := int32(th) + 8
		badgeX := rect.X + rect.W - bw + SpaceXS
		badgeY := rect.Y - bh/2
		fillRoundedRect(renderer, badgeX, badgeY, bw, bh, bh/2,
			sdl.Color{R: 220, G: 60, B: 60, A: 240})
		drawText(renderer, font, numStr, badgeX+SpaceSM, badgeY+(bh-int32(th))/2,
			sdl.Color{R: 255, G: 255, B: 255, A: 255}, textAlignLeft)
	}

	nc.bellRect = rect
}

// renderNotifBell draws the bell icon at the top-right of the status bar.
// Returns its rect for hit testing.
func renderNotifBell(renderer *sdl.Renderer, config *Config, x, y int32) sdl.Rect {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return sdl.Rect{}
	}
	bellW, bellH := notifBellSize(config)
	if bellW <= 0 {
		return sdl.Rect{}
	}
	rect := sdl.Rect{X: x - bellW, Y: y, W: bellW, H: bellH}
	drawNotifBell(renderer, font, rect)
	return rect
}

const (
	notifDropW   = int32(400)
	notifHeaderH = int32(40)
	// notifRowMinH is the shortest row (one line plus its timestamp); taller
	// rows grow from their measured content instead.
	notifRowMinH = int32(52)
	notifMaxRows = 8
	// notifMsgLines is how many lines a message may use. Two covers the long
	// path and error strings that a single ellipsized line used to swallow.
	notifMsgLines = 2
	notifRowPad   = int32(8)
)

// notifRowLayout is one history row's measured geometry: the wrapped message
// lines and the height they plus the timestamp need.
type notifRowLayout struct {
	lines  []string
	height int32
	rect   sdl.Rect
}

// notifDropWidth is the card width, clamped so it never runs off the panel.
func notifDropWidth() int32 {
	w := notifDropW
	if maxW := screenWidth - 24; w > maxW {
		w = maxW
	}
	return w
}

// notifMessageWidth is the inner width a message may occupy: the card minus
// the priority-dot gutter and the right padding.
func notifMessageWidth(w int32) int32 {
	m := w - 32 - 16
	if m < 40 {
		m = 40
	}
	return m
}

// notifRowMessage is the text a row shows: the message plus the fold count when
// identical events were merged. ASCII on purpose, since the handheld's bundled
// fonts are not guaranteed to carry U+00D7.
func notifRowMessage(n notification) string {
	if n.Repeat > 1 {
		return fmt.Sprintf("%s  x%d", n.Message, n.Repeat)
	}
	return n.Message
}

// notifWrapLines wraps a notification message. It behaves like wrapTextLines
// except for a token wider than the whole line: paths and URLs have no spaces
// to break at, and ellipsizing them kept the folder and threw away the file
// name and the reason (".../Dracula X Chroni..."). Those are cut into chunks
// that fit instead, so both lines carry text the user can actually read.
func notifWrapLines(font *ttf.Font, text string, maxW, maxLines int32) []string {
	if font == nil || maxLines <= 0 || maxW <= 0 {
		return nil
	}
	limit := int(maxLines)
	measure := func(s string) int32 {
		w, _, _ := font.SizeUTF8(s)
		return int32(w)
	}
	var out []string
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for _, word := range strings.Fields(text) {
		if measure(word) > maxW {
			// Over-long token: split it at the widest prefix that still fits.
			flush()
			rest := []rune(word)
			for len(rest) > 0 {
				n := 0
				for n < len(rest) && measure(string(rest[:n+1])) <= maxW {
					n++
				}
				if n == 0 {
					n = 1 // one glyph at a time, so the loop always ends
				}
				// Paths read far better split at a separator ("/storage/roms/
				// psp/") than mid-word (".../Games/Ca"). If a slash lands in
				// the back half of the chunk, end the line there instead.
				cut := -1
				for i := n - 1; i > 0; i-- {
					if rest[i] == '/' {
						cut = i
						break
					}
				}
				if cut > 0 && cut >= n/2 {
					n = cut + 1
				}
				out = append(out, string(rest[:n]))
				rest = rest[n:]
			}
			continue
		}
		cand := word
		if cur != "" {
			cand = cur + " " + word
		}
		if cur == "" || measure(cand) <= maxW {
			cur = cand
			continue
		}
		flush()
		cur = word
	}
	flush()

	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	if truncated && len(out) > 0 {
		out[len(out)-1] = fitTextToWidth(font, out[len(out)-1]+"…", maxW)
	}
	return out
}

// notifMeasureRows wraps each visible message and gives its row a height that
// fits the lines plus the timestamp. The fixed height these rows used to share
// ellipsized long errors to one line — exactly the messages worth reading — and
// the timestamp then sat on top of the clipped text.
func notifMeasureRows(font *ttf.Font, history []notification, maxW int32, limit int) []notifRowLayout {
	if font == nil || limit <= 0 {
		return nil
	}
	lineH := int32(font.Height())
	rows := make([]notifRowLayout, 0, limit)
	for i := 0; i < limit && i < len(history); i++ {
		lines := notifWrapLines(font, notifRowMessage(history[i]), maxW, notifMsgLines)
		h := notifRowPad*2 + int32(len(lines))*lineH + 2 + lineH
		if h < notifRowMinH {
			h = notifRowMinH
		}
		rows = append(rows, notifRowLayout{lines: lines, height: h})
	}
	return rows
}

// notifRowSpan is the total height of the given measured rows.
func notifRowSpan(rows []notifRowLayout) int32 {
	total := int32(0)
	for _, r := range rows {
		total += r.height
	}
	return total
}

// notifEmptyContentH is the vertical space the empty state needs below the
// dropdown header: the drawn bell glyph, both text lines and breathing room.
// A single row (52px) was smaller than that stack, so the card used to end
// above the second line and the empty-state text spilled out of its surface.
func notifEmptyContentH(config *Config) int32 {
	content := uiStateBlockHeight(config, "@bell", "No notifications", "You're all caught up.")
	if content <= 0 {
		content = 72
	}
	return content + SpaceLG*2
}

// notifDropdownLayout sizes the dropdown so it always fits on screen: the
// panel is measured from the bell's anchor, and the visible row count shrinks
// until the card (header + measured rows + padding) fits below it. Rows have
// their own measured heights, so a two-line message is never clipped to make
// the arithmetic work. An empty history gets the empty state's measured height
// instead of a guessed row count.
func notifDropdownLayout(config *Config, by int32, rows []notifRowLayout) (w, h int32, visible int) {
	w = notifDropWidth()
	maxH := screenHeight - by - 12
	if maxH < notifHeaderH+notifRowMinH+12 {
		maxH = notifHeaderH + notifRowMinH + 12
	}
	if len(rows) == 0 {
		h = notifHeaderH + notifEmptyContentH(config)
		if h > maxH {
			h = maxH
		}
		return w, h, 0
	}
	visible = len(rows)
	if visible > notifMaxRows {
		visible = notifMaxRows
	}
	for visible > 0 && notifHeaderH+notifRowSpan(rows[:visible])+12 > maxH {
		visible--
	}
	h = notifHeaderH + notifRowSpan(rows[:visible]) + 12
	return w, h, visible
}

// renderNotifDropdown draws the notification history dropdown.
func renderNotifDropdown(renderer *sdl.Renderer, config *Config) {
	if !nc.open {
		return
	}

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	// Position below the bell, then size the card to the rows that fit. Rows
	// are measured first: their heights depend on the wrapped message, and the
	// card has to be tall enough for whatever survives the wrap.
	by := nc.bellRect.Y + nc.bellRect.H + 8
	rowLayouts := notifMeasureRows(font, nc.history, notifMessageWidth(notifDropWidth()), len(nc.history))
	dropW, dropH, numRows := notifDropdownLayout(config, by, rowLayouts)
	bx := nc.bellRect.X
	if bx+dropW > screenWidth {
		bx = screenWidth - dropW - 12
	}
	if bx < 12 {
		bx = 12
	}
	// Navigation and click selection need the same row count the render used,
	// otherwise the D-pad could walk onto a row that was not drawn.
	nc.visibleRows = numRows
	nc.rowRects = nc.rowRects[:0]
	headerH := notifHeaderH

	// Entrance keyframes: the scrim fades while the card drops out from under
	// the bell, so the panel arrives as one layer instead of popping.
	progress := 1.0
	if uiMotionEnabled(config) {
		progress = nc.enter.progress(uiClockMS())
	} else {
		nc.enter.cancel()
	}
	fade := uiFadeIn.at(progress)
	slide := int32(math.Round(uiDropDown.at(progress)))

	// Dark backdrop.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, uint8(160*uiScrimProgress(progress, uiPanelEnterMS)))
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	compositeUILayerIn(renderer, fade, slide, sdl.Rect{X: bx, Y: by, W: dropW, H: dropH}, func() {
		// Dropdown card.
		nc.dropRect = sdl.Rect{X: bx, Y: by, W: dropW, H: dropH}
		UICard(renderer, sdl.Rect{X: bx, Y: by, W: dropW, H: dropH}, UICardStyle{
			Fill:   sdl.Color{R: 16, G: 20, B: 30, A: 245},
			Border: ColorBorder,
			Radius: 16,
		})

		// Header: "Notifications" + Clear All.
		title := "Notifications"
		renderText(renderer, config, font, title,
			sdl.Color{R: 200, G: 210, B: 230, A: 220}, bx+16, by+12)

		nc.clearRect = sdl.Rect{}
		if len(nc.history) > 0 {
			clearStr := "Clear All"
			cw, ch, _ := font.SizeUTF8(clearStr)
			clearX := bx + dropW - int32(cw) - 16
			nc.clearRect = sdl.Rect{X: clearX - 8, Y: by + 6, W: int32(cw) + 16, H: int32(ch) + 12}
			renderText(renderer, config, font, clearStr,
				sdl.Color{R: 220, G: 100, B: 100, A: 200},
				clearX, by+12)
		}

		// Notification rows. Each row is as tall as its wrapped message needs,
		// so a long error shows both lines and its timestamp stays inside the
		// row instead of overlapping the clipped text.
		listY := by + headerH
		lineH := int32(font.Height())
		ry := listY
		for i := 0; i < numRows && i < len(nc.history); i++ {
			n := nc.history[i]
			row := rowLayouts[i]

			if ry+row.height > by+dropH {
				break
			}

			rowRect := sdl.Rect{X: bx + 4, Y: ry, W: dropW - 8, H: row.height - 2}
			nc.rowRects = append(nc.rowRects, rowRect)

			// Row background.
			if i == nc.cursor {
				fillRoundedRect(renderer, rowRect.X, rowRect.Y, rowRect.W, rowRect.H, 8,
					sdl.Color{R: 30, G: 36, B: 50, A: 255})
			}

			// Priority dot, aligned with the first message line.
			dotCol := sdl.Color{R: 80, G: 160, B: 255, A: 220}
			switch n.Priority {
			case notifSuccess:
				dotCol = sdl.Color{R: 80, G: 200, B: 120, A: 220}
			case notifWarning:
				dotCol = sdl.Color{R: 240, G: 180, B: 60, A: 220}
			case notifError:
				dotCol = sdl.Color{R: 220, G: 70, B: 70, A: 220}
			}
			fillCircle(renderer, bx+20, ry+notifRowPad+lineH/2, 4, dotCol)

			// Message lines, wrapped by notifMeasureRows.
			msgCol := sdl.Color{R: 180, G: 190, B: 210, A: 220}
			if n.Read {
				msgCol = sdl.Color{R: 110, G: 120, B: 140, A: 180}
			}
			for li, line := range row.lines {
				renderText(renderer, config, font, line, msgCol,
					bx+32, ry+notifRowPad+int32(li)*lineH)
			}

			// Timestamp under the last message line.
			ago := time.Since(n.Timestamp)
			timeStr := ""
			switch {
			case ago < time.Minute:
				timeStr = "just now"
			case ago < time.Hour:
				timeStr = fmt.Sprintf("%dm ago", int(ago.Minutes()))
			default:
				timeStr = fmt.Sprintf("%dh ago", int(ago.Hours()))
			}
			renderText(renderer, config, font, timeStr,
				sdl.Color{R: 80, G: 90, B: 110, A: 150},
				bx+32, ry+notifRowPad+int32(len(row.lines))*lineH+2)

			ry += row.height
		}

		if len(nc.history) == 0 {
			UIEmptyState(renderer, config, sdl.Rect{X: bx, Y: listY, W: dropW, H: dropH - headerH},
				"@bell", "No notifications", "You're all caught up.")
		}
	})
}

// notifToggle opens/closes the notification dropdown.
func notifToggle() {
	nc.open = !nc.open
	nc.cursor = 0
	// Home has its own focus graph. A tap or a chrome activation on Home is a
	// deliberate choice, so focus follows it and the D-pad continues from the
	// bell instead of jumping back to a card.
	if homeLayoutActive && homeLayout != nil {
		if homeLayout.Focus.Set(HomeFocusBell) {
			homeLayout.Focus.Remember()
		}
	}
	if nc.open {
		nc.enter.begin(uiClockMS(), uiPanelEnterMS)
		// The top-bar overlays are exclusive.
		tb.startOpen = false
		homeCalendarOpen = false
		dismissWeatherPopover()
		// Mark all as read.
		for i := range nc.history {
			nc.history[i].Read = true
		}
	}
	PlayActivateSound()
}

// notifNavigate moves the cursor in the dropdown. The limit is the number of
// rows the last frame actually drew, so the D-pad cannot land on a row that was
// measured out of the card.
func notifNavigate(dr int) {
	if !nc.open {
		return
	}
	nc.cursor += dr
	if nc.cursor < 0 {
		nc.cursor = 0
	}
	max := nc.visibleRows - 1
	if max < 0 {
		max = notifMaxRows - 1
	}
	if last := len(nc.history) - 1; max > last {
		max = last
	}
	if max < 0 {
		max = 0
	}
	if nc.cursor > max {
		nc.cursor = max
	}
}

// notifClearAll clears all notifications. The dropdown stays open so the
// empty state is visible (touch users get direct feedback).
func notifClearAll() {
	nc.history = nc.history[:0]
	nc.cursor = 0
}

// notifDismiss closes the dropdown.
func notifDismiss() {
	nc.open = false
}

// notifIsPointInDrop tests if a point is inside the notification dropdown.
func notifIsPointInDrop(x, y int32) bool {
	if !nc.open {
		return false
	}
	return x >= nc.dropRect.X && x <= nc.dropRect.X+nc.dropRect.W &&
		y >= nc.dropRect.Y && y <= nc.dropRect.Y+nc.dropRect.H
}

// notifIsPointInBell tests if a point is inside the bell icon.
func notifIsPointInBell(x, y int32) bool {
	return x >= nc.bellRect.X && x <= nc.bellRect.X+nc.bellRect.W &&
		y >= nc.bellRect.Y && y <= nc.bellRect.Y+nc.bellRect.H
}

// notifHandleClick consumes clicks on the open dropdown: the header's "Clear
// All" clears the history, any other click inside the card is swallowed so it
// cannot reach the scene underneath. It reports whether the click was inside
// the open dropdown.
func notifHandleClick(x, y int32) bool {
	if !nc.open || !notifIsPointInDrop(x, y) {
		return false
	}
	if nc.clearRect.W > 0 && x >= nc.clearRect.X && x <= nc.clearRect.X+nc.clearRect.W &&
		y >= nc.clearRect.Y && y <= nc.clearRect.Y+nc.clearRect.H {
		notifClearAll()
		PlayActivateSound()
		return true
	}
	// Clicking a row moves the selection there, so a touch user can see which
	// message the next action would apply to instead of only dismissing the card.
	for i, r := range nc.rowRects {
		if pointInRect(x, y, r) {
			if nc.cursor != i {
				nc.cursor = i
				PlayNavSound()
			}
			break
		}
	}
	return true
}

// renderNotifCenter renders both the bell and dropdown. Call this from the
// status bar area on every frame.
func renderNotifCenter(renderer *sdl.Renderer, config *Config, bellX, bellY int32) {
	renderNotifBell(renderer, config, bellX, bellY)
	renderNotifDropdown(renderer, config)
}
