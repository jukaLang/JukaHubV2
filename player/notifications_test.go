package main

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// resetNotifCenter clears the global bell state between cases.
func resetNotifCenter(t *testing.T) {
	t.Helper()
	nc = notifCenter{}
}

func TestShowToastRecordsHistory(t *testing.T) {
	resetNotifCenter(t)

	ShowToast("Download finished", ToastKindSuccess)
	ShowToast("Low battery", ToastKindWarning)
	ShowToast("Update failed", ToastKindError)

	if len(nc.history) != 3 {
		t.Fatalf("history has %d entries, want 3", len(nc.history))
	}
	// Newest first.
	if nc.history[0].Message != "Update failed" || nc.history[0].Priority != notifError {
		t.Fatalf("newest entry = %q/%v, want error", nc.history[0].Message, nc.history[0].Priority)
	}
	if nc.history[2].Message != "Download finished" || nc.history[2].Priority != notifSuccess {
		t.Fatalf("oldest entry = %q/%v, want download/success", nc.history[2].Message, nc.history[2].Priority)
	}
	if got := notifUnreadCount(); got != 3 {
		t.Fatalf("unread = %d, want 3", got)
	}
}

// Scene-switch labels are navigation feedback, not events; logging them would
// bury downloads and errors under every L2/R2 press.
func TestShowToastSkipsSceneSwitch(t *testing.T) {
	resetNotifCenter(t)

	ShowToast("Hardware", ToastKindSwitch)
	if len(nc.history) != 0 {
		t.Fatalf("scene-switch toast was recorded: %+v", nc.history)
	}
}

func TestShowToastIgnoresEmptyMessage(t *testing.T) {
	resetNotifCenter(t)

	ShowToast("   ", ToastKindInfo)
	if len(nc.history) != 0 {
		t.Fatalf("whitespace toast was recorded: %+v", nc.history)
	}
}

// A producer stuck in a loop must not flush the 50-entry history: the newest
// unread row absorbs the repeat.
func TestShowToastFoldsRepeats(t *testing.T) {
	resetNotifCenter(t)

	for i := 0; i < 5; i++ {
		ShowToast("Network unreachable", ToastKindError)
	}
	if len(nc.history) != 1 {
		t.Fatalf("history has %d entries, want 1 folded row", len(nc.history))
	}
	if nc.history[0].Repeat != 5 {
		t.Fatalf("repeat = %d, want 5", nc.history[0].Repeat)
	}
}

// Once the user has opened the bell, the row is read; a later identical event
// is new information and gets its own unread row.
func TestShowToastAfterReadStartsNewRow(t *testing.T) {
	resetNotifCenter(t)

	notifPush("Update available", notifInfo)
	notifToggle() // opens the panel and marks everything read
	if !nc.open {
		t.Fatal("bell did not open")
	}
	nc.open = false

	ShowToast("Update available", ToastKindInfo)
	if len(nc.history) != 2 {
		t.Fatalf("history has %d entries, want 2", len(nc.history))
	}
	if nc.history[0].Read {
		t.Fatal("new event should be unread")
	}
	if nc.history[0].Repeat != 1 || nc.history[1].Repeat != 1 {
		t.Fatalf("repeats = %d/%d, want 1/1", nc.history[0].Repeat, nc.history[1].Repeat)
	}
	if got := notifUnreadCount(); got != 1 {
		t.Fatalf("unread = %d, want 1", got)
	}
}

// notifPush shows its toast and records exactly one row, not two.
func TestNotifPushRecordsOnce(t *testing.T) {
	resetNotifCenter(t)

	notifPush("JukaHub v2.4 is available", notifSuccess)
	if len(nc.history) != 1 {
		t.Fatalf("history has %d entries, want 1", len(nc.history))
	}
	if nc.history[0].Priority != notifSuccess {
		t.Fatalf("priority = %v, want success", nc.history[0].Priority)
	}
	if currentToast.Message != "JukaHub v2.4 is available" {
		t.Fatalf("toast = %q, want the pushed message", currentToast.Message)
	}
}

func TestNotifHistoryIsCapped(t *testing.T) {
	resetNotifCenter(t)

	for i := 0; i < 60; i++ {
		notifRecord("event", notifInfo)
		// Read the row so the next identical event is not folded into it.
		if len(nc.history) > 0 {
			nc.history[0].Read = true
		}
	}
	if len(nc.history) != 50 {
		t.Fatalf("history has %d entries, want the 50 cap", len(nc.history))
	}
}

// The notification control uses a drawn bell, not the old "(*)" glyph. Check
// that the silhouette actually paints and stays inside its box.
func TestBellIconDrawsSilhouette(t *testing.T) {
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	cx, cy, size := probeW/2, probeH/2, int32(40)
	icon := sdl.Color{R: 245, G: 248, B: 255, A: 255}
	drawBellIcon(renderer, cx, cy, size, icon)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}

	painted := f.countColor(icon, cx-size/2-4, cy-size/2-4, cx+size/2+4, cy+size/2+4)
	if painted < 200 {
		t.Fatalf("bell silhouette painted only %d pixels", painted)
	}
	if got := f.countColor(icon, 0, 0, cx-size/2-6, probeH-1); got > 0 {
		t.Fatalf("bell icon bled %d pixels outside its box", got)
	}
}

// TestNotificationCenterControllerNavigation pins the handheld path: once the
// bell is opened with A, the D-pad must walk the rows and B must close the
// panel instead of falling through to the scene underneath.
func TestNotificationCenterControllerNavigation(t *testing.T) {
	prevNC := nc
	prevActive := homeLayoutActive
	t.Cleanup(func() {
		nc = prevNC
		homeLayoutActive = prevActive
	})
	resetNotifCenter(t)
	homeLayoutActive = false // the controller router owns the overlay, not Home

	notifPush("one", notifInfo)
	notifPush("two", notifInfo)
	notifToggle()
	if !nc.open {
		t.Fatal("notification center did not open")
	}

	press := func(button uint8) bool {
		e := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}
		return handleControllerEvent(e, &Config{})
	}
	if !press(sdl.CONTROLLER_BUTTON_DPAD_DOWN) {
		t.Fatal("D-pad Down was not consumed by the open notification center")
	}
	if nc.cursor != 1 {
		t.Fatalf("cursor after Down = %d, want 1", nc.cursor)
	}
	if !press(sdl.CONTROLLER_BUTTON_DPAD_UP) {
		t.Fatal("D-pad Up was not consumed by the open notification center")
	}
	if nc.cursor != 0 {
		t.Fatalf("cursor after Up = %d, want 0", nc.cursor)
	}
	if !press(sdl.CONTROLLER_BUTTON_B) {
		t.Fatal("B was not consumed by the open notification center")
	}
	if nc.open {
		t.Fatal("B did not close the notification center")
	}
}

// Long messages are the ones worth reading: an error carrying a whole path
// must keep both of its wrapped lines, and its row must grow enough to hold
// them plus the timestamp underneath.
func TestNotifLongMessageKeepsTwoLines(t *testing.T) {
	newProbeFrame(t) // initialises SDL_ttf for the font cache
	font, _ := getCachedFont(&Config{}, "small")
	if font == nil {
		t.Skip("no font available")
	}
	long := "/storage/roms/psp/Games/Castlevania - The Dracula X Chronicles.iso could not be opened: permission denied"
	rows := notifMeasureRows(font, []notification{{Message: long, Priority: notifError}}, notifMessageWidth(notifDropWidth()), 1)
	if len(rows) != 1 {
		t.Fatalf("measured %d rows, want 1", len(rows))
	}
	row := rows[0]
	if len(row.lines) < 2 {
		t.Fatalf("long message kept %d line(s), want 2: %q", len(row.lines), row.lines)
	}
	if len(row.lines) > notifMsgLines {
		t.Fatalf("row used %d lines, cap is %d", len(row.lines), notifMsgLines)
	}
	lineH := int32(font.Height())
	need := notifRowPad*2 + int32(len(row.lines))*lineH + 2 + lineH
	if row.height < need {
		t.Fatalf("row is %dpx tall but its lines plus timestamp need %dpx", row.height, need)
	}
	if row.height <= notifRowMinH {
		t.Fatalf("two-line row stayed at the one-line minimum %dpx", notifRowMinH)
	}

	// A short message still gets the compact one-line row.
	short := notifMeasureRows(font, []notification{{Message: "Saved", Priority: notifSuccess}}, notifMessageWidth(notifDropWidth()), 1)
	if len(short) != 1 || len(short[0].lines) != 1 {
		t.Fatalf("short message measured %+v, want one line", short)
	}
	if want := notifRowPad*2 + 2*lineH + 2; short[0].height < want {
		t.Fatalf("short row is %dpx, want at least one line plus its timestamp (%dpx)", short[0].height, want)
	}
	if short[0].height > row.height {
		t.Fatalf("short row (%dpx) is taller than the two-line row (%dpx)", short[0].height, row.height)
	}
}

// Paths and URLs have no spaces to wrap at. The tail of such a message is the
// part worth reading (the file name, the reason), so it is split across lines
// rather than ellipsized away.
func TestNotifWrapSplitsLongPath(t *testing.T) {
	newProbeFrame(t)
	font, _ := getCachedFont(&Config{}, "small")
	if font == nil {
		t.Skip("no font available")
	}
	maxW := notifMessageWidth(notifDropWidth())
	long := "/storage/roms/psp/Games/Castlevania.iso could not be opened: permission denied"
	lines := notifWrapLines(font, long, maxW, notifMsgLines)
	if len(lines) != notifMsgLines {
		t.Fatalf("got %d lines (%q), want %d", len(lines), lines, notifMsgLines)
	}
	for _, l := range lines {
		if w, _, _ := font.SizeUTF8(l); int32(w) > maxW {
			t.Fatalf("line %q is %dpx wide, card allows %dpx", l, w, maxW)
		}
	}
	// The first line must be all text, not cut with a marker: the path is split
	// at a glyph boundary instead of giving up after its first third.
	if strings.HasSuffix(lines[0], "…") {
		t.Fatalf("first line %q was ellipsized instead of split", lines[0])
	}
	if len(lines[0]) < 12 {
		t.Fatalf("first line %q reused too little of the card width", lines[0])
	}

	// Text that fits is untouched and still wraps at spaces.
	if got := notifWrapLines(font, "Saved", maxW, notifMsgLines); len(got) != 1 || got[0] != "Saved" {
		t.Fatalf("short message wrapped to %q", got)
	}
}

// The D-pad must not walk onto a row the dropdown measured out of the card,
// so the cursor limit comes from the last frame's visible row count.
func TestNotifNavigateRespectsVisibleRows(t *testing.T) {
	prevNC := nc
	t.Cleanup(func() { nc = prevNC })
	resetNotifCenter(t)

	for i := 0; i < 5; i++ {
		notifPush("event", notifInfo)
		nc.history[0].Read = true
	}
	nc.open = true
	nc.visibleRows = 2
	for i := 0; i < 6; i++ {
		notifNavigate(1)
	}
	if nc.cursor != 1 {
		t.Fatalf("cursor = %d, want the last visible row (1)", nc.cursor)
	}
	nc.visibleRows = 0 // before the first frame renders
	for i := 0; i < notifMaxRows+4; i++ {
		notifNavigate(1)
	}
	if nc.cursor != len(nc.history)-1 {
		t.Fatalf("cursor = %d, want the last history row (%d)", nc.cursor, len(nc.history)-1)
	}
}

// Clicking a row moves the selection there, and a click anywhere else in the
// card is still swallowed so it cannot reach the scene underneath.
func TestNotifClickSelectsRow(t *testing.T) {
	prevNC := nc
	t.Cleanup(func() { nc = prevNC })
	resetNotifCenter(t)

	notifPush("one", notifInfo)
	notifPush("two", notifInfo)
	nc.open = true
	nc.dropRect = sdl.Rect{X: 100, Y: 100, W: 400, H: 300}
	nc.rowRects = []sdl.Rect{
		{X: 104, Y: 140, W: 392, H: 50},
		{X: 104, Y: 190, W: 392, H: 50},
	}

	if !notifHandleClick(200, 200) {
		t.Fatal("click inside the dropdown was not consumed")
	}
	if nc.cursor != 1 {
		t.Fatalf("cursor = %d, want the clicked row (1)", nc.cursor)
	}
	if !notifHandleClick(450, 380) {
		t.Fatal("click inside the card but outside every row was not consumed")
	}
	if nc.cursor != 1 {
		t.Fatalf("cursor moved to %d on a non-row click", nc.cursor)
	}
	if notifHandleClick(10, 10) {
		t.Fatal("click outside the dropdown was consumed")
	}
}

func TestToastKindPriorityRoundTrip(t *testing.T) {
	cases := []ToastKind{ToastKindInfo, ToastKindSuccess, ToastKindWarning, ToastKindError}
	for _, k := range cases {
		if got := priorityToastKind(toastKindPriority(k)); got != k {
			t.Fatalf("round trip of %v = %v", k, got)
		}
	}
}
