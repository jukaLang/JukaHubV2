package main

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// litPixels counts pixels in the rect that are brighter than a mostly-black
// background, so a drawn control is distinguishable from an empty panel.
func litPixels(buf []byte, r sdl.Rect, screenW int32) int {
	lit := 0
	for y := r.Y + 2; y < r.Y+r.H-2; y++ {
		for x := r.X + 2; x < r.X+r.W-2; x++ {
			i := (int(y)*int(screenW) + int(x)) * 4
			if i+2 >= len(buf) {
				continue
			}
			if int(buf[i])+int(buf[i+1])+int(buf[i+2]) > 90 {
				lit++
			}
		}
	}
	return lit
}

// probeRenderer gives a test a software renderer over the shared probe window
// and restores the global screen size when the test ends.
func probeRenderer(t *testing.T) *sdl.Renderer {
	t.Helper()
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() {
		renderer.Destroy()
		screenWidth, screenHeight = prevW, prevH
	})
	return renderer
}

// The Set alarm row must actually be drawn inside its hit rect, otherwise the
// visible button would be smaller than the clickable one.
func TestRenderCalendarPopoverDrawsAlarmButton(t *testing.T) {
	renderer := probeRenderer(t)
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderCalendarPopover(renderer, &Config{})

	buf := make([]byte, probeW*probeH*4)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	if n := litPixels(buf, calendarAlarmButtonRect(), probeW); n == 0 {
		t.Fatal("the Set alarm button drew nothing inside its hit rect")
	}
}

// The alarm screen is reachable from the calendar, so it must not be a black
// page and must offer a visible Back control for touch users.
func TestAlarmSceneRendersBackdropAndBackPill(t *testing.T) {
	renderer := probeRenderer(t)
	config := &Config{}
	alarmUI.tab = 0
	renderAlarmClock(renderer, config)

	if alarmBackRect.W <= 0 || alarmBackRect.H <= 0 {
		t.Fatalf("alarm Back pill rect is empty: %+v", alarmBackRect)
	}
	buf := make([]byte, probeW*probeH*4)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	if n := litPixels(buf, alarmBackRect, probeW); n == 0 {
		t.Fatal("the alarm Back pill drew nothing")
	}
	black := 0
	for y := int32(0); y < probeH; y += 8 {
		for x := int32(0); x < probeW; x += 8 {
			i := (int(y)*int(probeW) + int(x)) * 4
			if int(buf[i])+int(buf[i+1])+int(buf[i+2]) < 6 {
				black++
			}
		}
	}
	if black > (int(probeW/8)*int(probeH/8))/2 {
		t.Fatalf("%d sampled pixels are pure black: the alarm scene fell back to an empty page", black)
	}
}

// The clock popover is shared chrome: the calendar card and the Set alarm row
// must both live inside the popover bounds so one hit test covers them.
func TestCalendarPopoverIncludesAlarmButton(t *testing.T) {
	cal := homeCalendarRect()
	btn := calendarAlarmButtonRect()
	pop := calendarPopoverRect()

	if cal.W <= 0 || cal.H <= 0 {
		t.Fatalf("calendar rect is empty: %+v", cal)
	}
	if btn.X != cal.X || btn.W != cal.W {
		t.Errorf("alarm button %+v is not aligned with the calendar %+v", btn, cal)
	}
	if btn.Y < cal.Y+cal.H {
		t.Errorf("alarm button y=%d overlaps the calendar bottom %d", btn.Y, cal.Y+cal.H)
	}
	if pop.Y != cal.Y || pop.Y+pop.H < btn.Y+btn.H {
		t.Errorf("popover %+v does not cover calendar %+v and button %+v", pop, cal, btn)
	}
}

// Clicking the calendar must not dismiss it; clicking the action row opens the
// alarm screen, and clicking anywhere else closes the popover.
func TestCalendarPopoverClickTargets(t *testing.T) {
	prevOpen, prevScene := homeCalendarOpen, currentSceneIndex
	t.Cleanup(func() {
		homeCalendarOpen = prevOpen
		currentSceneIndex = prevScene
		alarmStore.Alarms = nil
	})

	// A click on the calendar card is swallowed and the popover stays open.
	homeCalendarOpen = true
	cal := homeCalendarRect()
	if !calendarPopoverClick(cal.X+5, cal.Y+5, nil) {
		t.Fatal("click inside the calendar was not consumed")
	}
	if !homeCalendarOpen {
		t.Fatal("clicking the calendar dismissed it")
	}

	// A click outside closes it.
	if !calendarPopoverClick(0, screenHeight-1, nil) {
		t.Fatal("outside click was not consumed")
	}
	if homeCalendarOpen {
		t.Fatal("outside click left the popover open")
	}
}

// A long notification message must be clipped to the card's inner width, so
// the text can never spill past the dropdown background.
func TestNotifMessageClippedToCardWidth(t *testing.T) {
	prevW := screenWidth
	t.Cleanup(func() { screenWidth = prevW })
	screenWidth = 1280

	w := notifDropW
	maxW := w - 32 - 16
	font, _ := getCachedFont(&Config{}, "small")
	if font == nil {
		t.Skip("no font available")
	}
	long := "A very long notification message that would run far past the right edge of the panel if it were not clipped"
	got := ellipsize(font, long, maxW)
	gw, _, _ := font.SizeUTF8(got)
	if int32(gw) > maxW {
		t.Fatalf("clipped message is %dpx wide, card allows %dpx", gw, maxW)
	}
	if got == long {
		t.Fatal("long message was not clipped at all")
	}
}

// The dropdown must never be taller than the space below the bell, so its
// background always covers every row it draws.
func TestNotifDropdownFitsBelowBell(t *testing.T) {
	config := &Config{}
	by := int32(100)
	w, h, rows := notifDropdownLayout(config, by, 50)
	if w <= 0 || w > screenWidth {
		t.Fatalf("dropdown width %d does not fit screen %d", w, screenWidth)
	}
	if rows > notifMaxRows {
		t.Fatalf("dropdown wants %d rows, max is %d", rows, notifMaxRows)
	}
	if by+h > screenHeight {
		t.Fatalf("dropdown bottom %d exceeds screen height %d", by+h, screenHeight)
	}
	if want := notifHeaderH + int32(rows)*notifRowH + 12; h != want {
		t.Fatalf("dropdown height %d, want header + rows + padding = %d", h, want)
	}

	// An empty history reserves the empty state's measured height, not one
	// notification row: the bell glyph and two text lines used to overflow the
	// card, which made the background look truncated behind the message.
	_, emptyH, emptyRows := notifDropdownLayout(config, by, 0)
	if emptyRows != 0 {
		t.Fatalf("empty dropdown reports %d rows, want its empty state", emptyRows)
	}
	content := uiStateBlockHeight(config, "@bell", "No notifications", "You're all caught up.")
	if got := emptyH - notifHeaderH; got < content {
		t.Fatalf("empty dropdown body is %dpx tall but its content needs %dpx", got, content)
	}
	if by+emptyH > screenHeight {
		t.Fatalf("empty dropdown bottom %d exceeds screen height %d", by+emptyH, screenHeight)
	}
}
