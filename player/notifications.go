package main

import (
	"fmt"
	"math"
	"time"

	"github.com/veandco/go-sdl2/sdl"
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
// so the top bar can reserve its corner before drawing.
func notifBellSize(config *Config) (int32, int32) {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return 0, 0
	}
	bw, bh, _ := font.SizeUTF8("(*)")
	return int32(bw) + 16, int32(bh) + 8
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
	bellStr := "(*)"
	bellX := x - bellW
	bellY := y
	rect := sdl.Rect{X: bellX, Y: bellY, W: bellW, H: bellH}

	// Background pill in the active theme's input surface.
	fillRoundedRect(renderer, bellX, bellY, bellW, bellH, bellH/2, ColorIconSurface)
	if nc.open {
		strokeRoundedRect(renderer, bellX, bellY, bellW, bellH, bellH/2, 1, accentColor)
	}

	// Focus ring when the D-pad/arrows sit on the bell.
	if focusEngine.ElementIndex() == ChromeBellIndex {
		strokeRoundedRect(renderer, bellX-2, bellY-2, bellW+4, bellH+4, (bellH+4)/2, 2, accentColor)
	}

	// Bell icon.
	renderText(renderer, config, font, bellStr, ColorTextSecondary(), bellX+8, bellY+4)

	// Unread count badge.
	unread := notifUnreadCount()
	if unread > 0 {
		numStr := fmt.Sprintf("%d", unread)
		bw, bh := UIBadgeSize(config, numStr)
		UIBadge(renderer, config, bellX+bellW-bw+SpaceXS, bellY-bh/2, numStr,
			sdl.Color{R: 220, G: 60, B: 60, A: 240}, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	}

	nc.bellRect = rect
	return rect
}

const (
	notifDropW   = int32(400)
	notifHeaderH = int32(40)
	notifRowH    = int32(52)
	notifMaxRows = 8
)

// notifDropdownLayout sizes the dropdown so it always fits on screen: the
// panel is measured from the bell's anchor, and the visible row count shrinks
// until the card (header + rows + padding) fits below it.
func notifDropdownLayout(by int32, count int) (w, h int32, rows int) {
	w = notifDropW
	if maxW := screenWidth - 24; w > maxW {
		w = maxW
	}
	rows = count
	if rows == 0 {
		// The empty state needs one row's worth of height to breathe.
		rows = 1
	}
	if rows > notifMaxRows {
		rows = notifMaxRows
	}
	maxH := screenHeight - by - 12
	if maxH < notifHeaderH+notifRowH+12 {
		maxH = notifHeaderH + notifRowH + 12
	}
	for rows > 0 && notifHeaderH+int32(rows)*notifRowH+12 > maxH {
		rows--
	}
	h = notifHeaderH + int32(rows)*notifRowH + 12
	return w, h, rows
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

	// Position below the bell, then size the card to the rows that fit.
	by := nc.bellRect.Y + nc.bellRect.H + 8
	dropW, dropH, numRows := notifDropdownLayout(by, len(nc.history))
	bx := nc.bellRect.X
	if bx+dropW > screenWidth {
		bx = screenWidth - dropW - 12
	}
	if bx < 12 {
		bx = 12
	}
	rowH := notifRowH
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

		// Notification rows. The message is clipped to the card's inner width
		// in pixels (not characters) so no text can spill past the background.
		listY := by + headerH
		msgMaxW := dropW - 32 - 16
		for i := 0; i < numRows && i < len(nc.history); i++ {
			n := nc.history[i]
			ry := listY + int32(i)*rowH

			if ry+rowH > by+dropH {
				break
			}

			// Row background.
			if i == nc.cursor {
				fillRoundedRect(renderer, bx+4, ry, dropW-8, rowH-2, 8,
					sdl.Color{R: 30, G: 36, B: 50, A: 255})
			}

			// Priority dot.
			dotCol := sdl.Color{R: 80, G: 160, B: 255, A: 220}
			switch n.Priority {
			case notifSuccess:
				dotCol = sdl.Color{R: 80, G: 200, B: 120, A: 220}
			case notifWarning:
				dotCol = sdl.Color{R: 240, G: 180, B: 60, A: 220}
			case notifError:
				dotCol = sdl.Color{R: 220, G: 70, B: 70, A: 220}
			}
			fillCircle(renderer, bx+20, ry+rowH/2, 4, dotCol)

			// Message, with the fold count when identical events were merged.
			msg := n.Message
			if n.Repeat > 1 {
				// ASCII on purpose: the handheld's bundled fonts are not
				// guaranteed to carry U+00D7.
				msg = fmt.Sprintf("%s  x%d", msg, n.Repeat)
			}
			msg = ellipsize(font, msg, msgMaxW)
			msgCol := sdl.Color{R: 180, G: 190, B: 210, A: 220}
			if n.Read {
				msgCol = sdl.Color{R: 110, G: 120, B: 140, A: 180}
			}
			renderText(renderer, config, font, msg, msgCol, bx+32, ry+8)

			// Timestamp, on its own line inside the taller row.
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
				sdl.Color{R: 80, G: 90, B: 110, A: 150}, bx+32, ry+28)
		}

		if len(nc.history) == 0 {
			UIEmptyState(renderer, config, sdl.Rect{X: bx, Y: listY, W: dropW, H: dropH - headerH},
				"(*)", "No notifications", "You're all caught up.")
		}
	})
}

// notifToggle opens/closes the notification dropdown.
func notifToggle() {
	nc.open = !nc.open
	nc.cursor = 0
	if nc.open {
		nc.enter.begin(uiClockMS(), uiPanelEnterMS)
		// The two top-bar overlays are exclusive.
		tb.startOpen = false
		// Mark all as read.
		for i := range nc.history {
			nc.history[i].Read = true
		}
	}
	PlayActivateSound()
}

// notifNavigate moves the cursor in the dropdown.
func notifNavigate(dr int) {
	if !nc.open {
		return
	}
	nc.cursor += dr
	if nc.cursor < 0 {
		nc.cursor = 0
	}
	max := len(nc.history) - 1
	if max > 7 {
		max = 7
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
	}
	return true
}

// renderNotifCenter renders both the bell and dropdown. Call this from the
// status bar area on every frame.
func renderNotifCenter(renderer *sdl.Renderer, config *Config, bellX, bellY int32) {
	renderNotifBell(renderer, config, bellX, bellY)
	renderNotifDropdown(renderer, config)
}
