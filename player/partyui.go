package main

// partyui.go — the Watch Party overlay.
//
// It is a modal like Tools, reachable with F8 or the Tools row, so it works on
// every config without adding a scene. The panel covers three states: not in a
// party (host or join), waiting for a join/link submission, and in a room with
// the shared queue, presence list and watch-time leaderboard.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type partyInputMode int

const (
	partyInputNone partyInputMode = iota
	partyInputJoin
	partyInputQueue
)

type partyRowKind int

const (
	partyRowHost partyRowKind = iota
	partyRowJoin
	partyRowAdd
	partyRowQueue
	partyRowCopy
	partyRowLeave
	partyRowClose
)

type partyRow struct {
	label  string
	detail string
	kind   partyRowKind
	index  int
	id     int
}

type partyUIState struct {
	open      bool
	cursor    int
	inputMode partyInputMode
	input     string
	busy      bool
	flash     string
}

var partyUI partyUIState

// partyPanelRect / partyRowRects are recomputed while rendering and used for
// hit testing. Only touched on the render/event thread.
var partyPanelRect sdl.Rect
var partyRowRects []sdl.Rect

// partyAsyncResults carries the outcome of a LAN request back to the render
// thread. UI state is never written from a network goroutine.
var partyAsyncResults = make(chan string, 8)

func partyRunAsync(work func() (string, error)) {
	if partyUI.busy {
		return
	}
	partyUI.busy = true
	go func() {
		msg, err := work()
		if err != nil {
			partyAsyncResults <- firstLine(err.Error())
			return
		}
		if msg == "" {
			msg = "Done"
		}
		partyAsyncResults <- msg
	}()
}

// partyDrainAsync applies completed network work on the UI thread. Called once
// per frame while the panel is open.
func partyDrainAsync() {
	for {
		select {
		case msg := <-partyAsyncResults:
			partyUI.busy = false
			partyUI.flash = msg
		default:
			return
		}
	}
}

func partyDeviceName() string {
	name := strings.TrimSpace(chatDisplayName())
	if name == "" {
		return "Handheld"
	}
	return name
}

// ── Panel actions ───────────────────────────────────────────────────────────

func partyOpenPanel() {
	qs.open = false
	if toolsPanel.open {
		toolsPanel.open = false
	}
	if companion.open {
		closeCompanionOverlay()
	}
	partyUI.open = true
	partyUI.cursor = 0
	partyUI.flash = ""
	partyUI.busy = false
	partyPanelRect = sdl.Rect{}
	partyRowRects = nil
	PlayActivateSound()
}

func partyClosePanel() {
	partyUI.open = false
	partyUI.inputMode = partyInputNone
	PlayBackSound()
}

func partyTogglePanel() {
	if partyUI.open {
		partyClosePanel()
		return
	}
	partyOpenPanel()
}

func partyRowCount() int { return len(partyRows()) }

func partyMoveCursor(delta int) {
	n := partyRowCount()
	if n == 0 {
		return
	}
	partyUI.cursor = (partyUI.cursor + delta + n) % n
	PlayNavSound()
}

// partyRows builds the actionable rows for the current state. Queue rows carry
// their shared index so selecting one starts it for the whole room.
func partyRows() []partyRow {
	snapshot, _, active, _ := partyState()
	if !active {
		return []partyRow{
			{label: "Host a watch party", detail: "This device serves the room on the LAN", kind: partyRowHost},
			{label: "Join a watch party", detail: "Paste the host address and 5-character code", kind: partyRowJoin},
			{label: "Close", detail: "Back to the app", kind: partyRowClose},
		}
	}
	rows := []partyRow{{label: "Add a link to the queue", detail: "Everyone can watch it", kind: partyRowAdd}}
	for i, item := range snapshot.Queue {
		label := item.Title
		if label == "" {
			label = partyTitleFromURL(item.URL)
		}
		detail := "by " + item.AddedBy
		if i == snapshot.Current {
			detail = "now playing · " + detail
		}
		rows = append(rows, partyRow{label: label, detail: detail, kind: partyRowQueue, index: i, id: item.ID})
	}
	rows = append(rows,
		partyRow{label: "Copy invite", detail: "Address and room code as one line", kind: partyRowCopy},
		partyRow{label: "Leave the party", detail: "The room stays open for the others", kind: partyRowLeave},
		partyRow{label: "Close", detail: "Keep watching together", kind: partyRowClose},
	)
	return rows
}

func partyActivateRow(config *Config) {
	rows := partyRows()
	if partyUI.cursor < 0 || partyUI.cursor >= len(rows) {
		return
	}
	row := rows[partyUI.cursor]
	switch row.kind {
	case partyRowHost:
		if !IsOnline() {
			partyUI.flash = "Connect to WiFi first — the party needs a LAN address"
			return
		}
		if err := partyHost(partyDeviceName()); err != nil {
			partyUI.flash = "Could not start the room: " + firstLine(err.Error())
			return
		}
		partyUI.cursor = 0
		partyUI.flash = "Party open — share the invite"
		PlaySuccessSound()
	case partyRowJoin:
		partyUI.inputMode = partyInputJoin
		partyUI.input = ""
		if invite := partyInviteText(); invite != "" {
			partyUI.input = invite
		}
		vkOpenPartyInput()
	case partyRowAdd:
		partyUI.inputMode = partyInputQueue
		partyUI.input = ""
		vkOpenPartyInput()
	case partyRowQueue:
		index := row.index
		partyRunAsync(func() (string, error) {
			if err := partySelectItem(index); err != nil {
				return "", err
			}
			return "Starting for everyone", nil
		})
		PlayActivateSound()
	case partyRowCopy:
		invite := partyInviteText()
		if invite == "" {
			partyUI.flash = "No invite yet"
			return
		}
		if err := sdl.SetClipboardText(invite); err != nil {
			partyUI.flash = "Could not copy the invite"
			return
		}
		partyUI.flash = "Copied " + invite
		PlaySuccessSound()
	case partyRowLeave:
		partyLeaveSession()
		partyUI.cursor = 0
		partyUI.flash = "Left the party"
		PlayBackSound()
	case partyRowClose:
		partyClosePanel()
	}
}

// partySubmitInput receives the keyboard's submitted text.
func partySubmitInput(text string) {
	mode := partyUI.inputMode
	partyUI.inputMode = partyInputNone
	partyUI.input = ""
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	switch mode {
	case partyInputJoin:
		go func() {
			err := partyJoin(text, partyDeviceName())
			if err != nil {
				partyAsyncResults <- firstLine(err.Error())
				return
			}
			partyAsyncResults <- "Joined " + partyInviteText()
		}()
		partyUI.busy = true
	case partyInputQueue:
		partyRunAsync(func() (string, error) {
			if err := partyQueueURL(text, ""); err != nil {
				return "", err
			}
			return "Added to the queue", nil
		})
	}
}

// partyDeleteSelected removes the queue row under the cursor.
func partyDeleteSelected() {
	rows := partyRows()
	if partyUI.cursor < 0 || partyUI.cursor >= len(rows) {
		return
	}
	row := rows[partyUI.cursor]
	if row.kind != partyRowQueue {
		return
	}
	id := row.id
	partyRunAsync(func() (string, error) {
		if err := partyRemoveItem(id); err != nil {
			return "", err
		}
		return "Removed from the queue", nil
	})
}

// ── Input ───────────────────────────────────────────────────────────────────

// partyHandleKey consumes every key while the panel is open so the scene below
// never reacts to it.
func partyHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !partyUI.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		partyMoveCursor(-1)
	case sdl.K_DOWN:
		partyMoveCursor(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		partyActivateRow(config)
	case sdl.K_DELETE, sdl.K_x:
		partyDeleteSelected()
	case sdl.K_ESCAPE, sdl.K_b:
		partyClosePanel()
	}
	return true
}

func partyHandleController(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || !partyUI.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		partyMoveCursor(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		partyMoveCursor(1)
	case sdl.CONTROLLER_BUTTON_A:
		partyActivateRow(config)
	case sdl.CONTROLLER_BUTTON_X:
		partyDeleteSelected()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		partyClosePanel()
	}
	return true
}

func partyHandleMouse(mx, my int32, config *Config) bool {
	if !partyUI.open {
		return false
	}
	for i, r := range partyRowRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			partyUI.cursor = i
			partyActivateRow(config)
			return true
		}
	}
	if partyPanelRect.W > 0 && !pointInRect(mx, my, partyPanelRect) {
		partyClosePanel()
	}
	return true
}

// ── Rendering ───────────────────────────────────────────────────────────────

func partyFormatWatch(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", int(seconds+0.5))
	}
	return fmt.Sprintf("%dm %02ds", int(seconds)/60, int(seconds)%60)
}

func partyRender(renderer *sdl.Renderer, config *Config) {
	if !partyUI.open {
		return
	}

	titleFont, _ := getCachedFont(config, "large")
	if titleFont == nil {
		titleFont, _ = getCachedFont(config, "medium")
	}
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		font = titleFont
	}
	small, _ := getCachedFont(config, "small")
	if small == nil {
		small = font
	}
	if font == nil || titleFont == nil {
		return
	}

	// Dim the scene below so the panel reads as modal.
	renderer.SetDrawColor(0, 0, 0, 150)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	w := screenWidth - 120
	if w > 980 {
		w = 980
	}
	h := screenHeight - 120
	if h > 560 {
		h = 560
	}
	x := (screenWidth - w) / 2
	y := (screenHeight - h) / 2
	partyPanelRect = sdl.Rect{X: x, Y: y, W: w, H: h}
	fillRoundedRect(renderer, x, y, w, h, 22, WithAlpha(ColorPanel, 255))
	strokeRoundedRect(renderer, x, y, w, h, 22, 1, WithAlpha(ColorBorder, 220))

	pad := int32(28)
	renderText(renderer, config, titleFont, "WATCH PARTY", ColorTextPrimary(), x+pad, y+22)
	stateLine := "LAN · same stream, same moment"
	if partyActive() {
		stateLine = "Live · " + partyDeviceName()
	}
	sw, _, _ := small.SizeUTF8(stateLine)
	renderText(renderer, config, small, stateLine, ColorTextTertiary(), x+w-pad-int32(sw), y+30)

	snapshot, _, active, errText := partyState()
	listTop := y + 64

	if active {
		code := snapshot.Code
		if code == "" {
			code = "—"
		}
		renderText(renderer, config, titleFont, code, sdl.Color{
			R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 255,
		}, x+pad, listTop)
		addr := snapshot.HostAddr
		if addr == "" {
			addr = partyInviteText()
		}
		if addr == "" {
			addr = "address hidden"
		}
		renderText(renderer, config, small, "Invite: "+addr, ColorTextSecondary(), x+pad, listTop+int32(titleFont.Height())+6)

		members := make([]string, 0, len(snapshot.Members))
		for _, m := range snapshot.Members {
			name := m.Name
			if m.Host {
				name += " (host)"
			}
			members = append(members, name)
		}
		sort.Strings(members)
		presence := "Waiting for friends…"
		if len(members) > 0 {
			presence = "Online: " + strings.Join(members, ", ")
		}
		presence = ellipsize(small, presence, w-2*pad)
		renderText(renderer, config, small, presence, ColorTextTertiary(), x+pad, listTop+int32(titleFont.Height())+6+int32(small.Height())+6)
		listTop += int32(titleFont.Height()) + 6 + int32(small.Height()) + 6 + int32(small.Height()) + 12
	} else {
		intro := "Host on this handheld, or join a friend's room on the same WiFi."
		renderText(renderer, config, font, ellipsize(font, intro, w-2*pad), ColorTextSecondary(), x+pad, listTop)
		listTop += int32(font.Height()) + 18
	}

	rows := partyRows()
	rowH := int32(48)
	rowsBottom := y + h - 52
	leftW := w - 2*pad
	if active {
		leftW = int32(float64(w-2*pad) * 0.62)
	}
	visible := int((rowsBottom - listTop) / rowH)
	if visible < 1 {
		visible = 1
	}
	start := 0
	if partyUI.cursor >= visible {
		start = partyUI.cursor - visible + 1
	}
	if start > len(rows)-visible {
		start = len(rows) - visible
	}
	if start < 0 {
		start = 0
	}
	end := start + visible
	if end > len(rows) {
		end = len(rows)
	}

	partyRowRects = partyRowRects[:0]
	for i := start; i < end; i++ {
		row := rows[i]
		rowY := listTop + int32(i-start)*rowH
		rect := sdl.Rect{X: x + pad, Y: rowY, W: leftW, H: rowH - 6}
		if i == partyUI.cursor {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 12, WithAlpha(ColorCardFocus, 255))
		}
		labelColor := ColorTextPrimary()
		if row.kind == partyRowClose && i != partyUI.cursor {
			labelColor = ColorTextSecondary()
		}
		renderText(renderer, config, font, ellipsize(font, row.label, leftW-24), labelColor, rect.X+14, rect.Y+6)
		if row.detail != "" {
			renderText(renderer, config, small, ellipsize(small, row.detail, leftW-24), ColorTextTertiary(), rect.X+14, rect.Y+8+int32(font.Height()))
		}
		partyRowRects = append(partyRowRects, rect)
	}

	// Leaderboard column, only meaningful once the room has members.
	if active {
		lx := x + pad + leftW + 22
		lw := x + w - pad - lx
		renderText(renderer, config, small, "WATCH LEADERBOARD", ColorTextTertiary(), lx, listTop)
		ly := listTop + int32(small.Height()) + 12
		if len(snapshot.Leaderboard) == 0 {
			renderText(renderer, config, small, "Scores appear as you watch.", ColorTextTertiary(), lx, ly)
		}
		for i, entry := range snapshot.Leaderboard {
			if ly+int32(font.Height()) > rowsBottom {
				break
			}
			num := fmt.Sprintf("%d.", i+1)
			renderText(renderer, config, font, num, ColorTextTertiary(), lx, ly)
			nameColor := ColorTextSecondary()
			if entry.Online {
				nameColor = ColorTextPrimary()
			}
			name := ellipsize(font, entry.Name, lw-90)
			renderText(renderer, config, font, name, nameColor, lx+28, ly)
			secs := partyFormatWatch(entry.WatchSecs)
			secW, _, _ := small.SizeUTF8(secs)
			renderText(renderer, config, small, secs, ColorTextTertiary(), lx+lw-int32(secW), ly+4)
			ly += int32(font.Height()) + 10
		}
	}

	// Status and footer.
	footer := "ENTER select   A add   X remove   B close"
	if !active {
		footer = "ENTER select   B close"
	}
	fw, _, _ := small.SizeUTF8(footer)
	renderText(renderer, config, small, footer, sdl.Color{R: 110, G: 125, B: 150, A: 180},
		x+w-pad-int32(fw), y+h-32)

	status := partyUI.flash
	if status == "" {
		status = errText
	}
	if partyUI.busy {
		status = "Working…"
	}
	if status != "" {
		renderText(renderer, config, small, ellipsize(small, status, w-2*pad-260), ColorTextSecondary(), x+pad, y+h-32)
	}
}
