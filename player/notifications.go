package main

import (
	"fmt"
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
}

type notifCenter struct {
	history  []notification
	open     bool // dropdown visible
	cursor   int  // focused row in dropdown
	bellRect sdl.Rect
	dropRect sdl.Rect
}

var nc notifCenter

func notifPush(msg string, p notifPriority) {
	n := notification{
		Message:   msg,
		Priority:  p,
		Timestamp: time.Now(),
	}
	nc.history = append([]notification{n}, nc.history...)
	if len(nc.history) > 50 {
		nc.history = nc.history[:50]
	}
	// Also show the toast as before.
	switch p {
	case notifSuccess:
		showToast(msg, ToastSuccess())
	case notifWarning:
		showToast(msg, ToastWarn())
	case notifError:
		showToast(msg, ToastError())
	default:
		showToast(msg, ToastInfo())
	}
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

// renderNotifBell draws the bell icon in the status bar area. Returns its rect for hit testing.
func renderNotifBell(renderer *sdl.Renderer, config *Config, x, y int32) sdl.Rect {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return sdl.Rect{}
	}
	bellStr := "(*)"
	bw, bh, _ := font.SizeUTF8(bellStr)
	bellW := int32(bw) + 16
	bellH := int32(bh) + 8
	bellX := x - bellW
	bellY := y
	rect := sdl.Rect{X: bellX, Y: bellY, W: bellW, H: bellH}

	// Background pill.
	fillRoundedRect(renderer, bellX, bellY, bellW, bellH, bellH/2,
		sdl.Color{R: 20, G: 24, B: 34, A: 200})

	// Bell icon.
	renderText(renderer, config, font, bellStr,
		sdl.Color{R: 200, G: 210, B: 230, A: 220},
		bellX+8, bellY+4)

	// Unread count badge.
	unread := notifUnreadCount()
	if unread > 0 {
		badgeR := int32(8)
		badgeX := bellX + bellW - 4
		badgeY := bellY - 2
		fillCircle(renderer, badgeX, badgeY, badgeR,
			sdl.Color{R: 220, G: 60, B: 60, A: 240})
		numStr := fmt.Sprintf("%d", unread)
		nw, _, _ := font.SizeUTF8(numStr)
		renderText(renderer, config, font, numStr,
			sdl.Color{R: 255, G: 255, B: 255, A: 255},
			badgeX-int32(nw)/2, badgeY-6)
	}

	nc.bellRect = rect
	return rect
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

	dropW := int32(400)
	maxH := int32(400)
	rowH := int32(44)
	headerH := int32(40)

	// Position below the bell.
	bx := nc.bellRect.X
	by := nc.bellRect.Y + nc.bellRect.H + 8
	if bx+dropW > screenWidth {
		bx = screenWidth - dropW - 12
	}

	numRows := len(nc.history)
	if numRows > 8 {
		numRows = 8
	}
	dropH := headerH + int32(numRows)*rowH + 12
	if dropH > maxH {
		dropH = maxH
	}

	// Dark backdrop.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 160)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	// Dropdown card.
	nc.dropRect = sdl.Rect{X: bx, Y: by, W: dropW, H: dropH}
	fillRoundedRect(renderer, bx, by, dropW, dropH, 16,
		sdl.Color{R: 16, G: 20, B: 30, A: 245})
	strokeRoundedRect(renderer, bx, by, dropW, dropH, 16, 1, ColorBorder)

	// Header: "Notifications" + Clear All.
	title := "Notifications"
	renderText(renderer, config, font, title,
		sdl.Color{R: 200, G: 210, B: 230, A: 220}, bx+16, by+12)

	if len(nc.history) > 0 {
		clearStr := "Clear All"
		cw, _, _ := font.SizeUTF8(clearStr)
		renderText(renderer, config, font, clearStr,
			sdl.Color{R: 220, G: 100, B: 100, A: 200},
			bx+dropW-int32(cw)-16, by+12)
	}

	// Notification rows.
	listY := by + headerH
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

		// Message.
		msg := n.Message
		if len(msg) > 38 {
			msg = msg[:38] + "…"
		}
		msgCol := sdl.Color{R: 180, G: 190, B: 210, A: 220}
		if n.Read {
			msgCol = sdl.Color{R: 110, G: 120, B: 140, A: 180}
		}
		renderText(renderer, config, font, msg, msgCol, bx+32, ry+8)

		// Timestamp.
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
			sdl.Color{R: 80, G: 90, B: 110, A: 150}, bx+32, ry+24)
	}

	if len(nc.history) == 0 {
		empty := "No notifications"
		ew, _, _ := font.SizeUTF8(empty)
		renderText(renderer, config, font, empty,
			sdl.Color{R: 100, G: 110, B: 130, A: 140},
			bx+(dropW-int32(ew))/2, listY+40)
	}
}

// notifToggle opens/closes the notification dropdown.
func notifToggle() {
	nc.open = !nc.open
	nc.cursor = 0
	if nc.open {
		// Mark all as read.
		for i := range nc.history {
			nc.history[i].Read = true
		}
	}
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

// notifClearAll clears all notifications.
func notifClearAll() {
	nc.history = nc.history[:0]
	nc.open = false
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

// renderNotifCenter renders both the bell and dropdown. Call this from the
// status bar area on every frame.
func renderNotifCenter(renderer *sdl.Renderer, config *Config, bellX, bellY int32) {
	renderNotifBell(renderer, config, bellX, bellY)
	renderNotifDropdown(renderer, config)
}
