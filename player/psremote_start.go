package main

// psremote_start.go — the part of Play that may wait, kept off the event
// thread.
//
// Before the hand-off, Play needs two answers that can take a while: whether
// chiaki-stream will start on this firmware (psremote_preflight.go starts it
// once, up to two seconds) and, for a console with an internet address,
// whether it is at home (a discovery probe of the home address, up to 700 ms).
// Both used to be asked on the event thread right before JukaHub quit, so the
// panel stopped for that long. Now Play is a run of the panel's wake
// machinery (psremote_wake.go): the console's row says "Starting the stream…",
// B stops it, A on it says so, and the answers come on this goroutine. A wake
// that ends in a stream goes on into the same step, with the address its own
// first wake already chose. Only the hand-off itself (the session file and the
// detached script, which waits for JukaHub to exit) starts on the main loop,
// after the last check that the panel is still free for it — a hand-off that
// started and was then not followed by JukaHub's exit would run the stream
// whenever JukaHub next quits.

import "time"

// psPanelStartStream is Play on a console that needs no wake: its row says the
// stream is starting while psStreamGetReady settles the helper's answer and
// the address.
func psPanelStartStream(c psConsole) {
	// Nothing in this step listens for a stop (cancel does nothing): B marks
	// the run stopped and its result is dropped (psWakeClaim).
	run := &psWakeRun{
		host:    c.Host,
		label:   c.label(),
		play:    true,
		started: time.Now(),
		stage:   psWakeConnecting,
		cancel:  func() {},
	}
	if !psWakeBegin(run) {
		return
	}
	PlayActivateSound()
	go psStreamGetReady(run, c, c.Host, false)
}

// psStreamGetReady runs on run's goroutine. It asks for chiaki-stream's
// answer (the panel's warm-up has usually found it; a check still running is
// joined, not repeated) and chooses the address: psChooseStreamHost probes the
// home address only when c still has both, which a wake's c no longer does
// (psConsoleAt). Then the hand-off goes to the main loop. listHost is the
// console's key in the panel's list; on says a wake has just seen the console
// on, for the toast when the stream cannot start now.
func psStreamGetReady(run *psWakeRun, c psConsole, listHost string, on bool) {
	if run.stopped.Load() {
		return // B: psWakeCancelRunning already let the run go and said so
	}
	psWakeSetStage(run, psWakeConnecting)
	host, remote := psStreamHostFor(c, true)
	if psRemoteSupported() {
		// Elsewhere psRemoteStream says Remote Play does not run here, and
		// nothing is worth asking first.
		if engine := psRemoteEngine(); engine.installed() {
			psEnsureExecutable(engine.Stream)
			psStreamHelperCheckWait(engine)
		}
		host, remote = psChooseStreamHost(c)
		if remote {
			logInfo("psremote", "console not on this network; streaming to its internet address")
		}
	}
	tbxPost(func() {
		// The run is let go only now: until this frame A and X still find it
		// running, so nothing else starts in between, and B still stops it.
		if !psWakeClaim(run) {
			return
		}
		if !chiakiPanel.open || chiakiPanel.page != psPageConsoles || virtualKeyboardActive {
			switch {
			case on && chiakiPanel.open:
				ShowToast(run.label+" is on — "+hintSentence("press %s on it to stream", "A", "Play"), ToastKindSuccess)
			case on:
				ShowToast(run.label+" is on — open PS Remote Play to stream", ToastKindSuccess)
			}
			return
		}
		console, ok := psPanelConsole(listHost)
		if !ok || !console.paired() {
			// Forgotten (Y) meanwhile: not streamed.
			if on {
				ShowToast(run.label+" is on", ToastKindSuccess)
			}
			return
		}
		psPanelPlay(console, psConsoleAt(console, host, remote))
	})
}
