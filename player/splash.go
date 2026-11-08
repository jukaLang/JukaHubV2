package main

// splash.go — the liquid-glass boot screen.
//
// The startup screen is a PS5-style living backdrop with a pane of liquid glass
// over it (bootliquid.go): the light flows, the pane rises into place, the
// JukaHub wordmark comes into focus through the glass, and a glint runs across
// it. The pane is not decoration over a timer: its track and its rows are the
// boot queue (bootwork.go, bootchecks.go) — every row is a step that actually
// ran, with its own result — and the frames in between pay for that work.
//
// The splash is still budgeted (startup.go): it gets what the boot budget left
// over, plus a short grace so the checks it reports can finish on screen, and
// it ends as soon as the queue has settled, so a fast boot gets a short splash.
// It never ends with a cut: the warm render of the first screen (warmSceneCaches)
// cross-fades in under the pane as the pane dissolves, which is the hand-over
// into Home. A fresh button press skips straight to that hand-over.
//
// Reduced motion keeps the look and drops the movement: the blobs rest and
// breathe in brightness, nothing scales or sweeps, and every change is a fade.

import (
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// splashCheckGrace is how far past the boot budget the card may run so the
	// checks it is reporting (a write probe on the card, a tool inventory)
	// finish on screen instead of freezing mid-row. Network work is never
	// waited for.
	splashCheckGrace = 420 * time.Millisecond
	// splashMinMS is the shortest a cold splash stays up before its hand-over:
	// long enough for the wordmark to come into focus.
	splashMinMS = 480.0
	// splashHandoffMS is the cross-fade into Home; splashSkipHandoffMS the
	// quicker one after a skip.
	splashHandoffMS     = 240.0
	splashSkipHandoffMS = 110.0
	// splashInputGuardMS ignores presses this early: the press that launched
	// JukaHub from the stock launcher (or a held chord) is not a request to
	// skip the splash it caused.
	splashInputGuardMS = 220.0
	// splashFadeInMS is the veil over the first frames, so the boot does not
	// pop from a black window to the backdrop.
	splashFadeInMS = 180.0
)

// splashFrameBudget is one frame: the Smart Pro's 30 fps cadence, 60 on a
// desktop. The time a frame does not spend drawing pays for queued boot work.
func splashFrameBudget() time.Duration {
	if IsTrimuiDevice() {
		return 33 * time.Millisecond
	}
	return 16 * time.Millisecond
}

// closeSplashFonts is called after the teardown's last frame. The wordmark
// face now comes from the shared font cache, which main closes with the rest;
// what is left to release here is the liquid kit, if a frame still holds it.
func closeSplashFonts() {
	if bootLiquid.renderer != nil && bootLiquid.ready {
		bootLiquidRelease()
	}
}

// splashSkipHint is the footer's skip hint in the device's own words.
func splashSkipHint() string {
	if P().IsHandheld() {
		return "Any button skips"
	}
	return "Any key skips"
}

// splashPollInput drains the event queue. Only a fresh press after the input
// guard skips (a release, a key repeat or the launcher's own press does not);
// a quit request is reported so the caller can hand it to the main loop.
func splashPollInput(elapsedMS float64) (quit, skip bool) {
	for ev := sdl.PollEvent(); ev != nil; ev = sdl.PollEvent() {
		switch e := ev.(type) {
		case *sdl.QuitEvent:
			quit = true
		case *sdl.KeyboardEvent:
			if e.Type == sdl.KEYDOWN && e.Repeat == 0 {
				skip = true
			}
		case *sdl.MouseButtonEvent:
			if e.Type == sdl.MOUSEBUTTONDOWN {
				skip = true
			}
		case *sdl.ControllerButtonEvent:
			if e.Type == sdl.CONTROLLERBUTTONDOWN {
				skip = true
			}
		case *sdl.TouchFingerEvent:
			if e.Type == sdl.FINGERDOWN {
				skip = true
			}
		}
	}
	if elapsedMS < splashInputGuardMS {
		skip = false
	}
	return quit, skip
}

// splashRenderFrame draws one frame at elapsedMS. handoff runs 0 → 1 over the
// cross-fade into Home (0 before it starts).
func splashRenderFrame(renderer *sdl.Renderer, config *Config, snap *stageSnapshot, elapsedMS, handoff float64, motion bool, footer, hint string) {
	if renderer == nil {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetClipRect(nil)
	t := elapsedMS / 1000
	liquid := bootLiquid.ready && bootLiquid.renderer == renderer
	if liquid {
		bootLiquidDrawBackdrop(renderer, t, bootSmoothstep(0, 320, elapsedMS), motion)
	} else {
		drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	}

	out := bootSmoothstep(0, 1, handoff)
	// The hand-over: the first screen, already drawn by the warm-up, comes up
	// under the dissolving pane.
	if out > 0 && bootHandoff.tex != nil && bootHandoff.renderer == renderer {
		_ = bootHandoff.tex.SetAlphaMod(uint8(255 * out))
		bootLiquidR = sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
		_ = renderer.Copy(bootHandoff.tex, nil, &bootLiquidR)
		_ = bootHandoff.tex.SetAlphaMod(255)
	}

	rise := bootSmoothstep(60, 380, elapsedMS)
	style := stageCardStyle{
		t:       t,
		pane:    rise * (1 - out),
		content: bootSmoothstep(180, 420, elapsedMS) * (1 - bootSmoothstep(0, 0.6, handoff)),
		scale:   1,
		word:    bootSmoothstep(120, 560, elapsedMS),
		motion:  motion,
		footer:  footer,
		hint:    hint,
	}
	if motion {
		style.scale = 1 - 0.03*(1-rise) + 0.04*out
		if elapsedMS >= 620 && elapsedMS <= 1220 {
			style.glint = (elapsedMS - 620) / 600
		}
	}
	stageDrawCard(renderer, config, snap, style)

	if elapsedMS < splashFadeInMS {
		veil := 1 - elapsedMS/splashFadeInMS
		renderer.SetDrawColor(0, 0, 0, uint8(255*bootClamp01(veil)))
		bootLiquidR = sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
		_ = renderer.FillRect(&bootLiquidR)
	}
}

// renderSplash draws one frame of a splash that hands over at total
// milliseconds: the frame a test (or a still) asks for without running the
// loop.
func renderSplash(renderer *sdl.Renderer, config *Config, snap stageSnapshot, elapsed, total float64) {
	handoff := 0.0
	if start := total - splashHandoffMS; total > 0 && elapsed > start {
		handoff = bootClamp01((elapsed - start) / splashHandoffMS)
	}
	splashRenderFrame(renderer, config, &snap, elapsed, handoff, !effectiveReducedMotion(config), versionDisplayString(config), splashSkipHint())
}

// ShowSplash plays the boot card for the time the boot budget left over,
// running the queued startup work between frames (bootwork.go) so the window
// is spent booting rather than idling. It hands over as soon as the queue has
// settled — a fast boot gets a shorter splash instead of a fixed animation —
// and a zero allotment opens the UI immediately.
func ShowSplash(renderer *sdl.Renderer, config *Config, allotted time.Duration) {
	if renderer == nil || allotted <= 0 {
		return
	}
	start := time.Now()
	motion := !effectiveReducedMotion(config)
	card := stageCardRect()
	radius := stageCardRadius
	if radius > card.W/6 {
		radius = card.W / 6
	}
	if !bootLiquidPrepare(renderer, card.W, card.H, radius, stageWordFont(config), "JukaHub") {
		// The flat card still reports the boot; only the liquid layer is lost.
		bootLiquidRelease()
	}
	defer bootLiquidRelease()
	// The warm-up keeps its render for the hand-over while the splash is up.
	bootHandoff.keep = true
	defer bootHandoffRelease()
	startupMarkNow("splash-ready")

	deadlineMS := float64((allotted + splashCheckGrace).Milliseconds())
	frame := splashFrameBudget()
	footer := versionDisplayString(config)
	hint := splashSkipHint()
	var snap stageSnapshot
	endAt := -1.0
	handoffMS := splashHandoffMS
	for {
		frameStart := time.Now()
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		quit, skip := splashPollInput(elapsed)
		if quit {
			// A close during the splash still exits: the request is put back
			// for the main loop, after the drain (re-queuing inside the drain
			// handed the same event straight back to PollEvent, forever).
			requeueQuit()
			return
		}
		if endAt < 0 {
			switch {
			case skip:
				endAt, handoffMS = elapsed, splashSkipHandoffMS
			case elapsed >= splashMinMS && bootWorkSettled(), elapsed >= deadlineMS:
				endAt = elapsed
			}
		}
		handoff := 0.0
		if endAt >= 0 {
			handoff = (elapsed - endAt) / handoffMS
			if handoff >= 1 {
				break
			}
		}

		bootWorkSnapshotInto(&snap)
		splashRenderFrame(renderer, config, &snap, elapsed, handoff, motion, footer, hint)
		renderer.Present()

		// The frame is a progress screen, not a pause: what it did not spend
		// drawing pays for the queued startup work (at least one step, so a
		// slow one cannot starve the queue). The hand-over frames stay smooth;
		// anything left drains right after the splash.
		if endAt < 0 {
			bootWorkTick(frame - time.Since(frameStart) - 2*time.Millisecond)
		}
		if rest := frame - time.Since(frameStart); rest > 0 {
			sdl.Delay(uint32(rest.Milliseconds()))
		}
	}
}
