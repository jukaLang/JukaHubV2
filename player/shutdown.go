package main

// shutdown.go — the glass teardown.
//
// Shutting down is drawn on the same liquid glass as the boot (stageui.go,
// bootliquid.go), and every row on the card is a real step with its result:
// games and helper processes stopped, downloads and servers closed, state
// written, the CPU and the speaker put back the way they were found, the
// cache database closed and the card synced. At the end the wordmark dissolves
// back into the glass and the light fades out.
//
// Order matters, so the steps run in groups: what owns the screen, the speaker
// or the network goes first (side by side — they are independent); state is
// persisted once nothing is still changing it; the CPU is restored alongside,
// so a tired card does not sit on a performance governor through the flushes;
// the sync comes last. Every step that can block and touches no main-loop
// state runs on its own goroutine and is waited for only up to its timeout, and
// the whole teardown fits a budget under the exit watchdog (exitguard.go): a
// wedged child or a dying SD card costs a red row, never a hung exit or the
// rows after it. The steps that own main-loop state (playback, the saves that
// read the config the card is drawn from) run on the render thread between
// frames.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// shutdownStep is one teardown step: a label for the card, the work, how long
// the card waits for it, whether it must run on the render thread (it touches
// SDL objects or main-loop state), and its group — steps of one group run side
// by side, groups run in order. The detail the work returns is what its row
// shows.
type shutdownStep struct {
	label   string
	fn      func() (string, error)
	timeout time.Duration
	inline  bool
	group   int
}

const (
	// shutdownStepTimeout is a step's wait when it does not name its own.
	shutdownStepTimeout = 1500 * time.Millisecond
	// shutdownBudget bounds the whole teardown, card included. It stays under
	// exitShutdownGrace so the watchdog never has to cut it short.
	shutdownBudget = 5 * time.Second
	// shutdownGroupMin keeps each group on screen long enough to be read.
	shutdownGroupMin = 140 * time.Millisecond
	// shutdownHoldMS is how long the finished card is held, and
	// shutdownDissolveMS the wordmark dissolving and the light fading out.
	shutdownHoldMS     = 260.0
	shutdownDissolveMS = 360.0
)

// errShutdownTimeout marks a step that did not finish in time.
var errShutdownTimeout = fmt.Errorf("timed out")

// bootShutdownHooks are the extra services other features register to be
// stopped on the teardown's services row (bootOnShutdown).
var bootShutdownHooks struct {
	mu  sync.Mutex
	fns []func()
}

// bootOnShutdown registers fn to run while the teardown disconnects services:
// off the render thread, alongside the servers, bounded by the row's timeout.
// It is for network sessions and background workers that should end cleanly
// (a gateway close, a "leaving" message), not for SDL objects.
func bootOnShutdown(fn func()) {
	if fn == nil {
		return
	}
	bootShutdownHooks.mu.Lock()
	bootShutdownHooks.fns = append(bootShutdownHooks.fns, fn)
	bootShutdownHooks.mu.Unlock()
}

// shutdownSteps is the teardown, in the order it must happen.
func shutdownSteps(config *Config) []shutdownStep {
	steps := []shutdownStep{
		{label: "Stopping games and helpers", group: 0, timeout: exitChildGrace + 500*time.Millisecond, fn: shutdownStopChildren},
		{label: "Stopping audio and video", group: 0, inline: true, fn: shutdownStopPlayback},
		{label: "Pausing downloads", group: 0, timeout: time.Second, fn: shutdownPauseDownloads},
		{label: "Disconnecting services", group: 0, timeout: 1500 * time.Millisecond, fn: shutdownStopServices},
		// On the render thread: the playtime flush writes the config's Custom
		// map, which every frame of the card reads (text substitution, the
		// backdrop), and the notes and favorites are main-loop state.
		{label: "Saving your place", group: 1, inline: true, fn: func() (string, error) {
			// Edit Home left open saves its tiles (home_edit.go); the save
			// below writes them out.
			homeEditEndForExit(config)
			playtimeFlush(config)
			partyHistoryFlush()
			saveFavorites()
			notesFlushOnExit()
			flushPendingUserConfig()
			return "state written", nil
		}},
		{label: "Restoring the CPU and speaker", group: 1, timeout: 1200 * time.Millisecond, fn: shutdownRestoreHardware},
		{label: "Closing caches and files", group: 2, timeout: 1200 * time.Millisecond, fn: func() (string, error) {
			// A boot that reached the teardown was healthy bootguard-wise: the
			// crash-loop counter resets, so the next boot starts from zero.
			// main.go recorded it when the loop ended; this call only waits for
			// that write (and writes nothing more) — a backstop.
			clearBootGuard()
			clearThumbnailDataCache()
			conditionalCache.clear()
			closeSharedCache()
			// The texture caches are released after the card's last frame
			// (shutdownReleaseTextures): a frame drawn after clearing them
			// would only fill them again.
			return "cache database closed", nil
		}},
	}
	if shutdownSyncs() {
		steps = append(steps, shutdownStep{label: "Writing everything to the card", group: 3, timeout: 1500 * time.Millisecond, fn: func() (string, error) {
			bootSyncFilesystems()
			return "synced", nil
		}})
	}
	return steps
}

// shutdownSyncs reports whether this platform has a sync step.
func shutdownSyncs() bool { return !IsWindows() }

// shutdownStopChildren stops what owns the screen: a game, emulator or port. An
// alarm that was ringing over it already ended (ShowShutdown), which let a game
// it paused run again — a stopped process would sit out the quit request until
// it was killed.
func shutdownStopChildren() (string, error) {
	stopped := stopScreenChildren(exitChildGrace)
	if stopped > 0 {
		return fmt.Sprintf("%d stopped", stopped), nil
	}
	return "nothing running", nil
}

// shutdownStopPlayback stops the speaker's other users. It runs on the render
// thread: the video player retires its frame textures there, and the
// Terminal's job is main-loop state.
func shutdownStopPlayback() (string, error) {
	musicStopIfPlaying()
	StopEmbeddedPlayback()
	termStopJob()
	// LEDs off: an LED left on the last audio frame is a light that stays on
	// after the process is gone.
	_ = SetLEDSync(false)
	return "stopped", nil
}

// shutdownPauseDownloads cancels the running yt-dlp: left alone it is
// orphaned and keeps downloading after the hub is gone. The queue is not
// persisted, so this only stops the transfer and its partial file stays.
func shutdownPauseDownloads() (string, error) {
	active, queued, _, _ := downloadCounts(downloadsSnapshot())
	downloadSetPaused(true)
	switch {
	case active+queued == 0:
		return "none running", nil
	case active > 0:
		return fmt.Sprintf("%d stopped", active+queued), nil
	}
	return fmt.Sprintf("%d left queued", queued), nil
}

// shutdownStopServices closes every listener and session the hub opened: the
// FTP and SSH servers, the phone companion, a Watch Party (hosted or joined),
// and whatever other features registered with bootOnShutdown.
func shutdownStopServices() (string, error) {
	var names []string
	if ftpSrv.Running() {
		names = append(names, "FTP")
	}
	ftpSrv.Stop()
	if sshSrv.Running() {
		names = append(names, "SSH")
	}
	sshSrv.Stop()
	if companion.srv != nil {
		names = append(names, "companion")
	}
	shutdownCompanion()
	if partyActive() {
		names = append(names, "Watch Party")
	}
	partyLeaveSession()
	bootShutdownHooks.mu.Lock()
	hooks := append([]func(){}, bootShutdownHooks.fns...)
	bootShutdownHooks.mu.Unlock()
	for _, fn := range hooks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logError("shutdown", "service hook panicked: %v", r)
				}
			}()
			fn()
		}()
	}
	if len(names) == 0 {
		return "nothing open", nil
	}
	return strings.Join(names, ", ") + " closed", nil
}

// shutdownRestoreHardware puts back what the session changed in the machine:
// the CPU governor and ceiling it booted with, and the mixer level an alarm
// raised.
func shutdownRestoreHardware() (string, error) {
	detail, err := restoreBootCPUSettings()
	if !alarmVolumeRestoreWait(time.Second) {
		logInfo("shutdown", "the speaker volume restore did not finish in time")
	}
	return detail, err
}

// shutdownCall runs one step's work and turns a panic into an error.
func shutdownCall(fn func() (string, error)) (detail string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn()
}

type shutdownResult struct {
	detail string
	err    error
}

type shutdownWait struct {
	index int
	done  chan shutdownResult
	until time.Time
}

// runShutdownStepsUntil runs steps group by group, recording each on track. A
// group's off-thread steps start together; its inline steps then run on this
// thread; the group ends when every step reported or ran out of time. A step
// that times out is recorded as failed and left running — the process is about
// to end, and a wedged step must not hold the exit. frame, when set, is called
// while waiting so the card keeps animating (and paces the wait); without one
// the wait sleeps.
func runShutdownStepsUntil(steps []shutdownStep, track *stageTrack, frame func(), deadline time.Time) {
	record := func(i int, detail string, err error) {
		if err != nil {
			logWarn("shutdown", "%s: %v", steps[i].label, err)
			if detail == "" {
				detail = err.Error()
			}
		}
		track.finishAt(i, detail, err)
	}
	for start := 0; start < len(steps); {
		end := start
		for end < len(steps) && steps[end].group == steps[start].group {
			end++
		}
		groupStart := time.Now()
		var waits []shutdownWait
		for i := start; i < end; i++ {
			if steps[i].inline {
				continue
			}
			timeout := steps[i].timeout
			if timeout <= 0 {
				timeout = shutdownStepTimeout
			}
			until := groupStart.Add(timeout)
			if until.After(deadline) {
				until = deadline
			}
			done := make(chan shutdownResult, 1)
			track.beginAt(i)
			go func(fn func() (string, error)) {
				detail, err := shutdownCall(fn)
				done <- shutdownResult{detail: detail, err: err}
			}(steps[i].fn)
			waits = append(waits, shutdownWait{index: i, done: done, until: until})
		}
		for i := start; i < end; i++ {
			if !steps[i].inline {
				continue
			}
			track.beginAt(i)
			if frame != nil {
				frame()
			}
			detail, err := shutdownCall(steps[i].fn)
			record(i, detail, err)
		}
		for len(waits) > 0 {
			now := time.Now()
			pending := waits[:0]
			for _, w := range waits {
				select {
				case r := <-w.done:
					record(w.index, r.detail, r.err)
				default:
					if !now.Before(w.until) {
						record(w.index, "", errShutdownTimeout)
					} else {
						pending = append(pending, w)
					}
				}
			}
			waits = pending
			if len(waits) == 0 {
				break
			}
			if frame != nil {
				frame()
			} else {
				time.Sleep(10 * time.Millisecond)
			}
		}
		// Each group stays up long enough to be read, when there is a card to
		// read it on and time left for it.
		if frame != nil {
			for time.Since(groupStart) < shutdownGroupMin && time.Now().Before(deadline) {
				frame()
			}
		}
		start = end
	}
}

// renderShutdownFrameAt draws one teardown frame. dissolve runs 0 → 1 at the
// very end: the wordmark dissolves into the glass, the pane fades and the light
// goes out to black.
func renderShutdownFrameAt(renderer *sdl.Renderer, config *Config, snap *stageSnapshot, elapsedMS, dissolve float64, motion bool) {
	if renderer == nil {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetClipRect(nil)
	t := elapsedMS / 1000
	out := bootSmoothstep(0, 1, dissolve)
	if bootLiquid.ready && bootLiquid.renderer == renderer {
		bootLiquidDrawBackdrop(renderer, t, 1-out, motion)
	} else {
		drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	}
	in := bootSmoothstep(0, 160, elapsedMS)
	style := stageCardStyle{
		t:       t,
		pane:    in * (1 - out),
		content: in * (1 - bootSmoothstep(0, 0.45, dissolve)),
		scale:   1,
		word:    1 - bootSmoothstep(0, 0.8, dissolve),
		motion:  motion,
		footer:  "Shutting down safely",
	}
	if motion {
		style.scale = 1 - 0.02*out
	}
	stageDrawCard(renderer, config, snap, style)
	if veil := bootSmoothstep(0.55, 1, dissolve); veil > 0 {
		renderer.SetDrawColor(0, 0, 0, uint8(255*veil))
		bootLiquidR = sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
		_ = renderer.FillRect(&bootLiquidR)
	}
}

// shutdownReleaseTextures drops the texture caches once nothing will be drawn
// again, so no texture outlives its renderer.
func shutdownReleaseTextures() {
	ClearTextCache()
	ClearShapeCache()
	ClearIconAtlas()
	ClearBgGradientCache()
	DestroyVKStaticCache()
	clearThumbnailCache()
	DestroyVKVideoOverlayCache()
	DestroyVKChatCache()
	clearHomeTextCache()
	clearAppIconCache()
	clearChatImageCache()
	clearChatAvatarCache()
	// An animated wallpaper's frames are textures too, the largest set there
	// is (gifwall.go).
	gifWallEvict()
}

// ShowShutdown runs the teardown while drawing it. A signal-initiated exit runs
// the same steps without the card: whatever sent the signal is waiting for the
// process, not watching it.
func ShowShutdown(renderer *sdl.Renderer, config *Config) {
	start := time.Now()
	deadline := start.Add(shutdownBudget)
	// Restart or Power off from Home's power card runs later, from main's
	// defer after the Cleanup hooks have flushed (devpower.go).
	// A ring still up ends first, here on the main thread that owns what it
	// paused: the game it stopped must run again before it is asked to quit.
	alarmStopRing()
	// The shutdown chime plays over the card on its own audio device, so the
	// steps that stop playback and restore the speaker leave it alone; the
	// exit waits for it to ring out (bootchime.go).
	shutdownChimeStart(config)
	defer shutdownChimeWait()
	steps := shutdownSteps(config)
	labels := make([]string, len(steps))
	for i, step := range steps {
		labels[i] = step.label
	}
	track := newStageTrack(devPowerShutdownTitle(), labels)

	if renderer == nil || exitRequestedBySignal.Load() {
		runShutdownStepsUntil(steps, track, nil, deadline)
		if renderer != nil {
			shutdownReleaseTextures()
		}
		return
	}

	motion := !effectiveReducedMotion(config)
	card := stageCardRect()
	radius := stageCardRadius
	if radius > card.W/6 {
		radius = card.W / 6
	}
	bootLiquidPrepare(renderer, card.W, card.H, radius, stageWordFont(config), "JukaHub")

	frameBudget := splashFrameBudget()
	var snap stageSnapshot
	lastFrame := time.Time{}
	draw := func(dissolve float64) {
		// Nothing is drawn while a game still owns the framebuffer: the card
		// would only flicker against it. It appears once the child is gone.
		if gameScreenSuspended() {
			return
		}
		track.snapshotInto(&snap)
		renderShutdownFrameAt(renderer, config, &snap, float64(time.Since(start).Microseconds())/1000, dissolve, motion)
		renderer.Present()
	}
	frame := func() {
		// Drain the queue so the window stays responsive. A close request
		// is what is already happening, so it is dropped, never re-pushed:
		// pushing it back inside the drain handed the same event straight
		// back to PollEvent, forever.
		for ev := sdl.PollEvent(); ev != nil; ev = sdl.PollEvent() {
		}
		draw(0)
		if wait := frameBudget - time.Since(lastFrame); wait > 0 && !lastFrame.IsZero() {
			sdl.Delay(uint32(wait.Milliseconds()))
		}
		lastFrame = time.Now()
	}

	frame()
	runShutdownStepsUntil(steps, track, frame, deadline)

	// Hold the finished card, then dissolve, inside whatever is left of the
	// budget.
	holdEnd := time.Now().Add(time.Duration(shutdownHoldMS) * time.Millisecond)
	for time.Now().Before(holdEnd) && time.Now().Before(deadline) {
		frame()
	}
	dissolveStart := time.Now()
	for time.Now().Before(deadline) {
		d := float64(time.Since(dissolveStart).Milliseconds()) / shutdownDissolveMS
		if d >= 1 {
			break
		}
		for ev := sdl.PollEvent(); ev != nil; ev = sdl.PollEvent() {
		}
		draw(d)
		sdl.Delay(uint32(frameBudget.Milliseconds()))
	}
	renderer.SetDrawColor(0, 0, 0, 255)
	_ = renderer.Clear()
	renderer.Present()

	bootLiquidRelease()
	shutdownReleaseTextures()
}
