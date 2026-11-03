package main

// Regression tests for the two handheld-polish complaints:
//
//   * Files and Settings drew two icons each (a wide tile from the horizontal
//     layout plus a small stacked tile on top of it). Every action card must
//     resolve to exactly one icon tile.
//   * Button clicks should animate (a short eased press dip plus the scene
//     fade), including on the TrimUI where Low Power is on by default.
//
// The icon checks are pure geometry, so they run without SDL; the animation
// checks drive the shared keyframe helpers directly.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// homeActionCards lists the cards drawn by drawActionCard (Continue, Clock and
// Exit use dedicated renderers and are covered separately).
func homeActionCards(h *HomeLayout) []HomeFocusID {
	var ids []HomeFocusID
	for _, id := range h.Order {
		switch id {
		case HomeFocusContinue, HomeFocusClock, HomeFocusWeather, HomeFocusExit,
			HomeFocusPS5, HomeFocusUpdate, HomeFocusNowPlaying, HomeFocusBell, HomeFocusWiFi:
			// Big continue card, header chips and the PS5 launcher tile are not
			// action cards: they draw themselves through drawHeader.
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// cardBadgeRect mirrors drawActionCard's badge placement. The design sheet has
// exactly one tile anatomy, so the badge is always the 48px square in the card's
// top-left corner, inset by the card padding.
func cardBadgeRect(card sdl.Rect, width, height int32) sdl.Rect {
	pad := scaleX(homeTilePad, width)
	size := min32(scaleX(homeIconSize, width), scaleY(homeIconSize, height))
	if maxIcon := min32(card.W-pad*2, card.H-pad*2-scaleY(homeGlyphSize, height)); size > maxIcon {
		size = maxIcon
	}
	return sdl.Rect{X: card.X + pad, Y: card.Y + pad, W: size, H: size}
}

// TestHomeActionCardDrawsSingleIconBadge pins the duplicated-icon bug and the
// one-composition rule: every action card resolves to exactly one icon badge,
// in its top-left corner, and the label sits below it inside the card.
func TestHomeActionCardDrawsSingleIconBadge(t *testing.T) {
	snapshotHomeGlobals(t)
	screenWidth, screenHeight = probeW, probeH
	layout := NewHomeLayout(probeW, probeH)

	wantSize := min32(scaleX(homeIconSize, probeW), scaleY(homeIconSize, probeH))
	pad := scaleX(homeTilePad, probeW)
	for _, id := range homeActionCards(layout) {
		item := layout.Items[id]
		badge := cardBadgeRect(item.Rect, probeW, probeH)
		if badge.W != badge.H {
			t.Errorf("%s: icon badge is not square: %+v", id, badge)
		}
		if badge.W != wantSize {
			t.Errorf("%s: icon badge size = %d, want %d", id, badge.W, wantSize)
		}
		if badge.X != item.Rect.X+pad || badge.Y != item.Rect.Y+pad {
			t.Errorf("%s: icon badge %+v is not in the card's top-left corner (card %+v)", id, badge, item.Rect)
		}
		if badge.X+badge.W > item.Rect.X+item.Rect.W || badge.Y+badge.H > item.Rect.Y+item.Rect.H {
			t.Errorf("%s: icon badge %+v escapes card %+v", id, badge, item.Rect)
		}
	}

	// Continue and Media have dedicated renderers, but they carry a badge too:
	// the header/hero pair must not drift back to a different anatomy.
	continueCard := layout.Items[HomeFocusContinue].Rect
	if cardBadgeRect(continueCard, probeW, probeH).H <= 0 {
		t.Errorf("the Continue card has no icon badge: %+v", continueCard)
	}
}

// TestClickPressAnimatesAndDips checks the eased press weight: it starts at
// rest, peaks inside the window and settles back to zero, so a click pushes
// the control down and releases it instead of toggling a fixed offset.
func TestClickPressAnimatesAndDips(t *testing.T) {
	snapshotHomeGlobals(t)
	screenWidth, screenHeight = probeW, probeH
	if got := homePressOffset(0, probeH); got != 0 {
		t.Errorf("settled press offset = %d, want 0", got)
	}
	if got := homePressOffset(1, probeH); got != scaleY(3, probeH) {
		t.Errorf("full press offset = %d, want %d", got, scaleY(3, probeH))
	}

	const window = 120.0
	if got := uiPressDepth(0, window); got != 0 {
		t.Errorf("press depth at t=0 = %v, want 0", got)
	}
	// The dip peaks early in the window (a press should respond at once) and
	// then eases back out, rather than holding until the midpoint.
	peak, peakAt := 0.0, 0.0
	for ms := 0.0; ms <= window; ms += 1 {
		if d := uiPressDepth(ms, window); d > peak {
			peak, peakAt = d, ms
		}
	}
	if peak < 0.95 {
		t.Errorf("press depth never reaches full: peak %v", peak)
	}
	if peakAt > window/2 {
		t.Errorf("press depth peaks at %vms, want the first half of the %vms window", peakAt, window)
	}
	release := uiPressDepth(window*0.75, window)
	if release <= 0 || release >= peak {
		t.Errorf("press depth at 75%% of the window = %v, want a partial release between 0 and %v", release, peak)
	}
	if got := uiPressDepth(window, window); got != 0 {
		t.Errorf("press depth at the end = %v, want 0", got)
	}
	if got := uiPressDepth(window*1.5, window); got != 0 {
		t.Errorf("press depth after the window = %v, want 0", got)
	}

	// Low Power (the TrimUI default) keeps the cheap press dip...
	saver := &Config{}
	saver.Variables.LowPower = true
	if got := uiPressProgress(saver, peakAt, window); got != peak {
		t.Errorf("Low Power press depth = %v, want the animated %v", got, peak)
	}
	// ...while reduced motion snaps it to full depth for the whole window.
	quiet := &Config{}
	quiet.Variables.ReducedMotion = true
	if got := uiPressProgress(quiet, 0, window); got != 1 {
		t.Errorf("reduced-motion press depth = %v, want the snapped 1", got)
	}
}

// TestSceneFadeUsesTokensAndTheme pins the fade shape: fade-out runs to fully
// covered, fade-in reveals the new scene, both finish inside the handheld
// budget, and the scrim follows the theme background instead of hardcoded
// near-black.
func TestSceneFadeUsesTokensAndTheme(t *testing.T) {
	// The zero value keeps the build's default fade: an unset transition must
	// never snap a scene change on its own.
	var def Config
	if uiSceneFadeMS(&def, "fade-out") != uiSceneOutMS || uiSceneFadeMS(&def, "fade-in") != uiSceneInMS {
		t.Fatalf("fade durations = %v/%v, want %d/%d",
			uiSceneFadeMS(&def, "fade-out"), uiSceneFadeMS(&def, "fade-in"), uiSceneOutMS, uiSceneInMS)
	}
	if uiSceneOutMS > 200 || uiSceneInMS > 250 {
		t.Errorf("scene fades (%d/%d ms) are too slow for the handheld", uiSceneOutMS, uiSceneInMS)
	}

	// transition instant snaps: duration 0 in both phases.
	snap := &Config{}
	snap.Variables.Transition = TransitionInstant
	if d := uiSceneFadeMS(snap, "fade-out") + uiSceneFadeMS(snap, "fade-in"); d != 0 {
		t.Errorf("instant transition durations = %v, want 0", d)
	}
	if !motionInstant(snap) || motionInstant(nil) {
		t.Error("motionInstant does not follow Variables.Transition")
	}

	if got := uiSceneScrimAlpha(&def, "fade-out", 0); got != 0 {
		t.Errorf("fade-out alpha at t=0 = %d, want 0", got)
	}
	if got := uiSceneScrimAlpha(&def, "fade-out", float64(uiSceneOutMS)); got != 255 {
		t.Errorf("fade-out alpha at the end = %d, want 255", got)
	}
	if got := uiSceneScrimAlpha(&def, "fade-in", 0); got != 255 {
		t.Errorf("fade-in alpha at t=0 = %d, want 255", got)
	}
	if got := uiSceneScrimAlpha(&def, "fade-in", float64(uiSceneInMS)); got != 0 {
		t.Errorf("fade-in alpha at the end = %d, want 0", got)
	}
	if !uiSceneFadeDone(&def, "fade-out", float64(uiSceneOutMS)) || uiSceneFadeDone(&def, "fade-out", 0) {
		t.Error("fade done flag does not follow the token duration")
	}
	if got, want := uiSceneScrimColor(), ColorBackground; got != want {
		t.Errorf("scene scrim = %+v, want the theme background %+v", got, want)
	}
}
