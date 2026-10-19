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

import "testing"

// homeActionCards lists the cards drawn by drawActionCard (Continue, Clock and
// Exit use dedicated renderers and are covered separately).
func homeActionCards(h *HomeLayout) []HomeFocusID {
	var ids []HomeFocusID
	for _, id := range h.Order {
		switch id {
		case HomeFocusContinue, HomeFocusClock, HomeFocusExit:
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// TestHomeActionCardDrawsSingleIcon pins the duplicated-icon bug: the layout
// must pick one composition (beside-the-label or stacked) before drawing, so
// no card is ever given a wide tile and a stacked tile at the same time.
func TestHomeActionCardDrawsSingleIcon(t *testing.T) {
	snapshotHomeGlobals(t)
	screenWidth, screenHeight = probeW, probeH
	layout := NewHomeLayout(probeW, probeH)

	pad := scaleX(20, probeW)
	for _, id := range homeActionCards(layout) {
		item := layout.Items[id]
		tile, stacked, ok := homeActionIconTile(item.Rect, probeW, probeH)
		if !ok {
			t.Errorf("%s: no icon tile resolved for card %+v", id, item.Rect)
			continue
		}
		if tile.W != tile.H {
			t.Errorf("%s: icon tile is not square: %+v", id, tile)
		}
		if tile.X < item.Rect.X || tile.Y < item.Rect.Y ||
			tile.X+tile.W > item.Rect.X+item.Rect.W ||
			tile.Y+tile.H > item.Rect.Y+item.Rect.H {
			t.Errorf("%s: icon tile %+v escapes card %+v", id, tile, item.Rect)
		}

		narrow := id == HomeFocusFiles || id == HomeFocusSettings
		if stacked != narrow {
			t.Errorf("%s: stacked = %v, want %v", id, stacked, narrow)
		}
		if stacked {
			// The stacked tile is the small centred one; a wide left-aligned
			// tile is what used to sit underneath it.
			if want := scaleX(40, probeW); tile.W != want {
				t.Errorf("%s: stacked icon size = %d, want %d", id, tile.W, want)
			}
			if center := item.Rect.X + item.Rect.W/2; tile.X+tile.W/2 != center {
				t.Errorf("%s: stacked icon is not centred: tile %+v, card %+v", id, tile, item.Rect)
			}
		} else {
			if want := min32(scaleX(56, probeW), scaleY(56, probeH)); tile.W != want {
				t.Errorf("%s: horizontal icon size = %d, want %d", id, tile.W, want)
			}
			if tile.X != item.Rect.X+pad {
				t.Errorf("%s: horizontal icon x = %d, want %d", id, tile.X, item.Rect.X+pad)
			}
		}
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
	if uiSceneFadeMS("fade-out") != uiSceneOutMS || uiSceneFadeMS("fade-in") != uiSceneInMS {
		t.Fatalf("fade durations = %v/%v, want %d/%d",
			uiSceneFadeMS("fade-out"), uiSceneFadeMS("fade-in"), uiSceneOutMS, uiSceneInMS)
	}
	if uiSceneOutMS > 200 || uiSceneInMS > 250 {
		t.Errorf("scene fades (%d/%d ms) are too slow for the handheld", uiSceneOutMS, uiSceneInMS)
	}

	if got := uiSceneScrimAlpha("fade-out", 0); got != 0 {
		t.Errorf("fade-out alpha at t=0 = %d, want 0", got)
	}
	if got := uiSceneScrimAlpha("fade-out", float64(uiSceneOutMS)); got != 255 {
		t.Errorf("fade-out alpha at the end = %d, want 255", got)
	}
	if got := uiSceneScrimAlpha("fade-in", 0); got != 255 {
		t.Errorf("fade-in alpha at t=0 = %d, want 255", got)
	}
	if got := uiSceneScrimAlpha("fade-in", float64(uiSceneInMS)); got != 0 {
		t.Errorf("fade-in alpha at the end = %d, want 0", got)
	}
	if !uiSceneFadeDone("fade-out", float64(uiSceneOutMS)) || uiSceneFadeDone("fade-out", 0) {
		t.Error("fade done flag does not follow the token duration")
	}
	if got, want := uiSceneScrimColor(), ColorBackground; got != want {
		t.Errorf("scene scrim = %+v, want the theme background %+v", got, want)
	}
}
