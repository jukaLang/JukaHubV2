package main

// screensaver_backdrop_test.go — the idle screen's own way to forget its
// backdrop photo.
//
// The regression this pins: the photo could only be taken away by waking the
// panel, opening Files, opening the same picture and using the viewer's lock
// button — the screen that actually shows the photo offered nothing. The clear
// is a two-press action because every other input on this screen means "wake
// up", so a single press would let an ordinary wake silently throw the photo
// away.

import (
	"image/color"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// resetScreensaverBackdrop keeps the saver's clear state out of the rest of the
// suite: it is process-wide, and the saver tests render frames that read it.
func resetScreensaverBackdrop(t *testing.T) {
	t.Helper()
	prevArmed, prevCleared := ss.backdropArmedAt, ss.backdropClearedAt
	ss.backdropArmedAt, ss.backdropClearedAt = time.Time{}, time.Time{}
	t.Cleanup(func() {
		ss.backdropArmedAt, ss.backdropClearedAt = prevArmed, prevCleared
	})
}

// The offer appears only when there is a photo to forget; a default saver is
// the screen it always was, and a press of the button is an ordinary wake.
func TestScreensaverClearIsOfferedOnlyWithAPhoto(t *testing.T) {
	resetScreensaverBackdrop(t)
	resetScreenlockCache(t)
	config := screenlockTestConfig("")
	now := time.Now()

	if screensaverBackdropClearable(config) {
		t.Error("a screen with no photo reported something to clear")
	}
	if hint := screensaverBackdropHint(config, now); hint != "" {
		t.Errorf("hint with no photo = %q, want no line at all", hint)
	}
	if screensaverBackdropPress(config, now) {
		t.Error("a press with no photo was consumed, so it would not wake the screen")
	}

	picture := writeTestPNG(t, "idle.png", color.RGBA{R: 40, G: 90, B: 200, A: 255})
	if err := setScreenlockImage(config, picture); err != nil {
		t.Fatalf("setScreenlockImage: %v", err)
	}
	if !screensaverBackdropClearable(config) {
		t.Fatal("a screen with a photo reported nothing to clear")
	}
	hint := screensaverBackdropHint(config, now)
	if !strings.Contains(hint, "Y") || strings.Contains(hint, "again") {
		t.Errorf("hint with an unflagged photo = %q, want the offer naming the button", hint)
	}
}

// The button the screen names is the button the event loop answers, and only
// when it is pressed on its own.
func TestScreensaverClearButtonMatchesTheHint(t *testing.T) {
	if !screensaverClearKey(sdl.K_y, sdl.KMOD_NONE) {
		t.Error("the key the hint names is not the key the screen answers")
	}
	if !screensaverClearButton(sdl.CONTROLLER_BUTTON_Y) {
		t.Error("the pad button the hint names is not the one the screen answers")
	}
	// A chord belongs to whatever else owns it: Ctrl+Y must not clear a photo
	// on its way to some other surface.
	if screensaverClearKey(sdl.K_y, sdl.KMOD_CTRL) {
		t.Error("Ctrl+Y was taken as the clear button")
	}
	if screensaverClearKey(sdl.K_y, sdl.KMOD_ALT) {
		t.Error("Alt+Y was taken as the clear button")
	}
	for _, other := range []sdl.Keycode{sdl.K_n, sdl.K_x, sdl.K_RETURN, sdl.K_SPACE, sdl.K_ESCAPE} {
		if screensaverClearKey(other, sdl.KMOD_NONE) {
			t.Errorf("key %v clears the backdrop; waking should stay on any key", other)
		}
	}
	for _, other := range []uint8{sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_START} {
		if screensaverClearButton(other) {
			t.Errorf("pad button %d clears the backdrop; waking should stay on any button", other)
		}
	}
}

// One press asks and a second answers: the photo survives the first press, and
// the picture on the card survives both.
func TestScreensaverClearAsksThenClears(t *testing.T) {
	resetScreensaverBackdrop(t)
	resetScreenlockCache(t)
	config := screenlockTestConfig("")
	picture := writeTestPNG(t, "backdrop.png", color.RGBA{R: 210, G: 90, B: 40, A: 255})
	if err := setScreenlockImage(config, picture); err != nil {
		t.Fatalf("setScreenlockImage: %v", err)
	}
	start := time.Now()

	// The first press only arms: the screen stays asleep and the photo is
	// still the backdrop.
	if !screensaverBackdropPress(config, start) {
		t.Fatal("the first press was not consumed, so it woke the screen instead of asking")
	}
	if screenlockImagePath(config) == "" {
		t.Fatal("the first press cleared the photo: a wake press would throw it away")
	}
	if !screensaverBackdropArmed(start.Add(time.Second)) {
		t.Fatal("the first press did not arm the clear")
	}
	if hint := screensaverBackdropHint(config, start.Add(time.Second)); !strings.Contains(hint, "again") {
		t.Errorf("armed hint = %q, want the second press invited", hint)
	}

	// The second press clears the setting, says so, and leaves the file.
	second := start.Add(2 * time.Second)
	if !screensaverBackdropPress(config, second) {
		t.Fatal("the second press was not consumed")
	}
	if got := screenlockImagePath(config); got != "" {
		t.Errorf("backdrop after the second press = %q, want the default screen", got)
	}
	if _, err := os.Stat(picture); err != nil {
		t.Fatalf("clearing from the idle screen removed the photo: %v", err)
	}
	if ssImage.path != "" || ssImage.texture != nil {
		t.Errorf("the cleared picture kept its decoded state (path %q), so the frame after would still be covered", ssImage.path)
	}
	if hint := screensaverBackdropHint(config, second); !strings.Contains(hint, "cleared") {
		t.Errorf("hint right after clearing = %q, want the confirmation", hint)
	}

	// The confirmation is a moment, not a permanent caption, and with the photo
	// gone the screen stops offering anything.
	ss.backdropClearedAt = second.Add(-ssBackdropNoticeTTL - time.Second)
	if hint := screensaverBackdropHint(config, second); hint != "" {
		t.Errorf("hint after the confirmation expired = %q, want no line", hint)
	}
}

// An arm nobody answers lapses: a press later, after the screen has been left
// alone for a while, asks again instead of clearing.
func TestScreensaverClearArmLapses(t *testing.T) {
	resetScreensaverBackdrop(t)
	resetScreenlockCache(t)
	config := screenlockTestConfig("")
	picture := writeTestPNG(t, "later.png", color.RGBA{R: 30, G: 160, B: 90, A: 255})
	if err := setScreenlockImage(config, picture); err != nil {
		t.Fatalf("setScreenlockImage: %v", err)
	}
	start := time.Now()

	if !screensaverBackdropPress(config, start) {
		t.Fatal("the first press was not consumed")
	}
	late := start.Add(ssBackdropArmWindow + time.Second)
	if screensaverBackdropArmed(late) {
		t.Fatal("the arm outlived its window")
	}
	if !screensaverBackdropPress(config, late) {
		t.Fatal("the late press was not consumed")
	}
	if screenlockImagePath(config) == "" {
		t.Fatal("a press after the window cleared the photo without a second press")
	}
	if !screensaverBackdropArmed(late) {
		t.Error("the late press did not arm a fresh window")
	}
}

// Waking disarms, and each sleep starts at rest: a stray press must not clear a
// photo on a screen the user has since left and come back to.
func TestScreensaverClearArmResetsAcrossSleeps(t *testing.T) {
	_ = newProbeFrame(t)
	resetScreensaverBackdrop(t)
	resetScreenlockCache(t)
	config := screenlockTestConfig("")
	picture := writeTestPNG(t, "again.png", color.RGBA{R: 90, G: 90, B: 220, A: 255})
	if err := setScreenlockImage(config, picture); err != nil {
		t.Fatalf("setScreenlockImage: %v", err)
	}
	start := time.Now()

	if !screensaverBackdropPress(config, start) {
		t.Fatal("the first press was not consumed")
	}
	prevActive, prevInited := ss.active, ss.inited
	t.Cleanup(func() { ss.active, ss.inited = prevActive, prevInited })

	ss.active = true
	deactivateScreensaver()
	if screensaverBackdropArmed(time.Now()) {
		t.Error("waking left the clear armed for the next visit")
	}

	activateScreensaver()
	if screensaverBackdropArmed(time.Now()) || screensaverBackdropNotice(time.Now()) {
		t.Error("a fresh sleep inherited the previous visit's clear state")
	}
	if screenlockImagePath(config) == "" {
		t.Fatal("the photo was lost across a sleep and wake")
	}
}

// The line really is painted: with the starfield held still, the same frame
// drawn with and without the confirmation differs in the band the line occupies.
func TestScreensaverPaintsTheClearHint(t *testing.T) {
	_ = newProbeFrame(t) // SDL video and TTF up
	resetScreensaverBackdrop(t)
	resetScreenlockCache(t)

	config := &Config{}
	prevActive, prevFading, prevAlpha, prevInited := ss.active, ss.fadingIn, ss.fadeAlpha, ss.inited
	prevStars, prevNodes, prevConns := ss.stars, ss.nodes, ss.connections
	prevQuote := ss.lastQuoteChange
	ss.inited = true
	ss.active, ss.fadingIn, ss.fadeAlpha = true, false, 255
	// A still frame: no twinkling stars and no drifting nodes, and a quote that
	// is not due to rotate, so the two captures differ in what this check is
	// about rather than in the animation.
	ss.stars, ss.nodes, ss.connections = nil, nil, nil
	ss.lastQuoteChange = time.Now()
	t.Cleanup(func() {
		ss.active, ss.fadingIn, ss.fadeAlpha, ss.inited = prevActive, prevFading, prevAlpha, prevInited
		ss.stars, ss.nodes, ss.connections = prevStars, prevNodes, prevConns
		ss.lastQuoteChange = prevQuote
	})

	tick := sdl.GetTicks64()
	quiet := renderScreensaverProbe(t, config, tick)

	// The confirmation is the one state that needs no photo on the card, so it
	// can be compared against a screen that is otherwise identical.
	ss.backdropClearedAt = time.Now()
	marked := renderScreensaverProbe(t, config, tick)

	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Fatalf("small font unavailable, so the hint under test could not be drawn at all")
	}
	hy := probeH - 50
	bandTop := hy - int32(font.Height()) - 10
	diff := 0
	for y := bandTop; y < bandTop+int32(font.Height())+4; y++ {
		for x := int32(0); x < probeW; x++ {
			if quiet.at(x, y) != marked.at(x, y) {
				diff++
			}
		}
	}
	if diff < 50 {
		t.Fatalf("only %d pixels changed in the hint band, so the confirmation was not drawn", diff)
	}
}

// renderScreensaverProbe draws one saver frame at probeW×probeH and returns the
// captured pixels.
func renderScreensaverProbe(t *testing.T, config *Config, tick uint64) *probeFrame {
	t.Helper()
	return componentFrame(t, func(r *sdl.Renderer) {
		renderScreensaver(r, config, tick)
	})
}
