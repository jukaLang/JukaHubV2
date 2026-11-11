package main

// psremote_helpers_bg.go — PS Remote Play downloads itself in the background.
//
// The two native helpers (psremote_helpers_install.go) used to be fetched only
// when PS Remote Play opened, so the first visit began with a download, and a
// PS5's PIN prompt waited for it. Now a small scheduler fetches them by itself,
// once, soon after JukaHub starts on a handheld that has none, and the first
// visit finds Remote Play ready. It is the same install with the same checks;
// only the moment is new:
//
//   - Where: only where Remote Play runs (psRemoteSupported), outside safe
//     mode, with the Settings switch on (Games & emulators → Download Remote
//     Play automatically, remotePlayAutoDownload, on by default), while a
//     helper is missing and the firmware has not refused them
//     (bin/.remote-play-refused: only Retry downloads those again).
//   - When: a minute after the first frame, so boot's own downloads go first,
//     then at every look (every 30 s, at once when JukaHub is back from a
//     game) that finds the handheld online (a captive portal is not), its
//     clock past this build's date (an older clock fails every certificate,
//     so network time goes first), JukaHub on screen (not behind a game or
//     its low-power idle screen, backgroundPaused) and no install running.
//     A PSN Account-ID being saved (from the phone, or typed) is a reason to
//     look at once: whoever saves one is about to pair.
//   - After a failure, whoever started the install: a dropped network,
//     GitHub's rate limit or a server error, a clock still wrong, or a
//     download that took too long waits 2, 5, 15, 30 and then 60 minutes
//     between tries; anything else (no release has the helpers, a full or
//     read-only card, a download that did not verify, a certificate this
//     handheld cannot check, a stop on the chip) ends the background tries
//     until JukaHub starts again, an Account-ID is saved, the switch is turned
//     back on, or the panel's Retry or the phone's Download now (which start
//     one themselves) wake it (psBgKick).
//
// Progress shows wherever the user is: the panel's subtitle and its row say
// Downloading or Installing (psHelperRows), a chip on every other screen
// follows the download and stops it with its X (psHelpersActivityItem,
// activity.go), and the phone's PlayStation card reads psHelperStatusNow
// (companion_psremote.go).

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// psBgSettingKey is the Custom variable behind the Settings switch.
	psBgSettingKey = "remotePlayAutoDownload"
	// psBgStartDelay keeps the first look out of the boot: by then the network
	// monitor has probed, network time has had its first try, and the tools a
	// first start installs have their head start.
	psBgStartDelay = 60 * time.Second
	// psBgPoll is how often the scheduler looks while it waits for a moment.
	// A look with nothing to do reads a few atomics and the helpers' cached
	// presence.
	psBgPoll = 30 * time.Second
)

// psBgBackoff is the wait after each failure in a row that a later try may
// fix; the last one repeats.
var psBgBackoff = [...]time.Duration{2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

// psBgOff mirrors the Settings switch for the scheduler goroutine and the
// phone's handler: the Custom map belongs to the main thread. The zero value
// is "on", the default.
var psBgOff atomic.Bool

// psBgStarted makes the scheduler one goroutine however often its start runs.
var psBgStarted atomic.Bool

// psBgKickCh asks the scheduler to look now. It holds one request; more add
// nothing, and nothing blocks on it (psBgKick).
var psBgKickCh = make(chan struct{}, 1)

func init() {
	// After the first frame, on the main thread, where the setting may be read
	// (startup.go); the scheduler runs on its own goroutine after that.
	deferStartupTask("psremote-helpers", func() { psBgStart(appConfig) })
}

// ── The setting ───────────────────────────────────────────────────────────

// psBgEnabled reports the Settings switch (main thread). It is on unless the
// user turned it off: the key is read in any casing (the web builder
// capitalises), and only an explicit off — false, "false", "off", "no", 0 —
// counts, so a garbled value keeps the default.
func psBgEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	v, ok := customValue(config.Variables.Custom, psBgSettingKey)
	if !ok || v == nil {
		return true
	}
	if s, isString := v.(string); isString {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "false", "off", "no", "0":
			return false
		}
		return true
	}
	return coerceBool(v)
}

// psBgSetEnabled stores the switch and tells the scheduler (main thread).
// Turning it on looks at once; turning it off leaves an install that is
// already running to finish (its chip's X stops it).
func psBgSetEnabled(config *Config, on bool) {
	psBgOff.Store(!on)
	setStoreBool(config, psBgSettingKey, on)
	if on {
		psBgKick()
	}
}

// psBgBind re-reads the switch after the settings were replaced: a hot reload
// or a restored backup (applyConfigReload), an imported settings document
// (cfgdocRefresh). Main thread; turned back on, the scheduler looks at once.
func psBgBind(config *Config) {
	on := psBgEnabled(config)
	if wasOff := psBgOff.Swap(!on); wasOff && on {
		psBgKick()
	}
}

// psBgSettingDetail is the switch's line in Settings: what Remote Play's
// install is doing right now (whoever started it), else what the switch does.
func psBgSettingDetail(config *Config) string {
	psHelperJob.mu.Lock()
	running, v := psHelperJob.running, psHelperJob.view
	psHelperJob.mu.Unlock()
	switch {
	case running && v.phase == psHelperChecking:
		return "Installing now"
	case running && v.phase == psHelperDownloading && v.total > 0:
		return "Downloading now · " + psHelperPercent(v)
	case running:
		return "Downloading now"
	case !psHelpersNeeded():
		return "Installed · ready to stream a PS4 or PS5"
	case !psBgEnabled(config):
		return "Off: it downloads when PS Remote Play opens"
	}
	return "The PS4/PS5 streaming helpers, once, in the background when online"
}

// ── The scheduler ─────────────────────────────────────────────────────────

// psBgStart starts the scheduler once. It runs from the deferred startup queue
// on the main thread, which is where the setting may be read.
func psBgStart(config *Config) {
	psBgOff.Store(!psBgEnabled(config))
	if !psHelperSupportedFn() {
		return
	}
	if !safeModeAllows(SafeFeatureToolInstall) {
		logInfo("psremote", "background Remote Play download: not started in safe mode")
		return
	}
	if !psBgStarted.CompareAndSwap(false, true) {
		return
	}
	go psBgLoop(psBgStartDelay, psBgPoll)
}

// psBgKick asks the scheduler to look now, with its back-off and its reason
// to stop forgotten (psBgScheduler.kicked). Any goroutine; it never blocks.
func psBgKick() {
	select {
	case psBgKickCh <- struct{}{}:
	default:
	}
}

// psBgLoop is the scheduler goroutine: it looks every poll, at once when
// JukaHub comes back to the screen, and when kicked. With nothing left to do —
// the helpers in, the switch off, a refusal, a reason to stop — it sleeps until
// a kick.
func psBgLoop(delay, poll time.Duration) {
	wake := subscribeForegroundWake()
	first := time.NewTimer(delay)
	select {
	case <-first.C:
	case <-psBgKickCh:
		first.Stop()
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	s := &psBgScheduler{}
	for {
		if s.tick(time.Now()) == psBgPark {
			<-psBgKickCh
			s.kicked()
			continue
		}
		select {
		case <-ticker.C:
		case <-wake:
		case <-psBgKickCh:
			s.kicked()
		}
	}
}

// psBgVerdict is what one look decided.
type psBgVerdict int

const (
	psBgWait  psBgVerdict = iota // look again at the next poll
	psBgPark                     // nothing to do until a kick
	psBgBegan                    // an install started
)

// psBgScheduler is the goroutine's own state; nothing else touches it.
type psBgScheduler struct {
	// seen is the last install whose outcome was booked (judge).
	seen *psHelperRun
	// failures counts the failed installs in a row a later try may fix;
	// retryAt holds the next try back until then.
	failures int
	retryAt  time.Time
	// stopped is why the background tries ended for this run of JukaHub; ""
	// while they go on.
	stopped string
	// lastWait is the last reason logged for not starting, so a long wait is
	// one log line, not one a poll.
	lastWait string
}

// tick makes one decision and starts the install when its moment has come. A
// panic is logged and the next look tries again: a background download is
// never worth the app.
func (s *psBgScheduler) tick(now time.Time) (verdict psBgVerdict) {
	defer func() {
		if r := recover(); r != nil {
			logError("psremote", "background Remote Play download: %v", r)
			verdict = psBgWait
		}
	}()
	s.judge(now)
	switch {
	case psBgOff.Load():
		s.wait("switched off in Settings")
		return psBgPark
	case !psHelperSupportedFn():
		return psBgPark
	case psHelpersRunning():
		s.wait("an install is running")
		return psBgWait
	case !psHelpersNeeded():
		s.wait("the helpers are installed")
		return psBgPark
	case psHelpersRefusal() != nil:
		s.wait("this firmware refused the helpers; only Retry downloads them again")
		return psBgPark
	case s.stopped != "":
		s.wait(s.stopped)
		return psBgPark
	case now.Before(s.retryAt):
		s.wait("waiting after a failed try")
		return psBgWait
	case !IsOnline():
		s.wait("no internet")
		return psBgWait
	case now.Before(clockFloor()):
		s.wait("the clock is older than this build; waiting for network time")
		return psBgWait
	case backgroundPaused():
		s.wait("JukaHub is not on screen")
		return psBgWait
	case psHelpersAdoptSavedRefusal():
		// Read only here, at a moment the download would start: a refusal
		// kept from an earlier start of JukaHub.
		s.wait("this firmware refused the helpers before; only Retry downloads them again")
		return psBgPark
	case !psHelpersStart(psStartAuto):
		s.wait("the last try ended moments ago")
		return psBgWait
	}
	s.lastWait = ""
	logInfo("psremote", "downloading the Remote Play helpers in the background")
	return psBgBegan
}

// judge books the outcome of the last install, whoever started it (this
// scheduler, the panel, a press, the phone): a success clears the back-off, a
// failure a later try may fix holds the next try back, and any other failure
// ends the background tries for this run of JukaHub.
func (s *psBgScheduler) judge(now time.Time) {
	psHelperJob.mu.Lock()
	run, running := psHelperJob.run, psHelperJob.running
	var err error
	if run != nil && !running {
		err = run.err
	}
	psHelperJob.mu.Unlock()
	if run == nil || running || run == s.seen {
		return
	}
	s.seen = run
	switch {
	case err == nil:
		s.failures, s.retryAt = 0, time.Time{}
	case psBgTransient(err):
		s.failures++
		wait := psBgBackoffAfter(s.failures)
		s.retryAt = now.Add(wait)
		logDebug("psremote", "background Remote Play download: next try in %s (%v)", wait, err)
	default:
		s.stopped = "stopped for this run: " + psHelpersFailText(err)
		logInfo("psremote", "background Remote Play download stops until JukaHub starts again: %s", psHelpersFailText(err))
	}
}

// kicked is a reason to look again from scratch: an Account-ID was saved, or
// the switch was turned on.
func (s *psBgScheduler) kicked() {
	s.stopped = ""
	s.failures = 0
	s.retryAt = time.Time{}
}

// wait logs why the scheduler is not starting an install, once per reason.
func (s *psBgScheduler) wait(why string) {
	if why == s.lastWait {
		return
	}
	s.lastWait = why
	logDebug("psremote", "background Remote Play download waits: %s", why)
}

// psBgTransient reports whether a failed install is one a later try may fix: no
// way to GitHub, a download cut off, GitHub's rate limit or a server error of
// its own, a clock that did not fit a certificate yet, or the time running out.
func psBgTransient(err error) bool {
	for _, passing := range []error{errPSHelpersOffline, errPSHelpersCutOff, errPSHelpersRateLimited,
		errPSHelpersClock, errPSHelpersNoInternet, context.DeadlineExceeded} {
		if errors.Is(err, passing) {
			return true
		}
	}
	return strings.Contains(err.Error(), "HTTP 5")
}

// psBgBackoffAfter is the wait after failures failed tries in a row.
func psBgBackoffAfter(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > len(psBgBackoff) {
		failures = len(psBgBackoff)
	}
	return psBgBackoff[failures-1]
}

// ── Where it shows ────────────────────────────────────────────────────────

// psHelperPercentValue is the download's progress as a number, 0 to 99 (100 is
// the install's end, not the download's), or -1 when its size is unknown.
func psHelperPercentValue(v psHelperView) int {
	if v.phase != psHelperDownloading || v.total <= 0 {
		return -1
	}
	pct := int(max64(v.got, 0) * 100 / v.total)
	if pct > 99 {
		pct = 99
	}
	return pct
}

// psHelperStatus is the helpers' install in words, for the places that show it
// away from the panel's rows: the phone's PlayStation card.
type psHelperStatus struct {
	Supported bool
	// State is "unsupported", "ready", "finding", "downloading", "installing",
	// "failed", "refused" or "missing".
	State string
	Text  string
	// Percent is the download's progress, 0 to 99, while its size is known;
	// -1 otherwise.
	Percent int
	Busy    bool
	// CanStart says a press can start an install now; StartLabel is its word.
	CanStart   bool
	StartLabel string
}

// psHelperStatusInput is what psHelperStatusFor words.
type psHelperStatusInput struct {
	running bool
	needed  bool
	view    psHelperView
	// refusal is the firmware's reason for refusing the helpers, as short as
	// the panel's row has it ("" when there is none), and refusedWhich the
	// helpers it refused: "stream", "regist" or "both".
	refusal      string
	refusedWhich string
	online       bool
	portal       bool
	// auto says the background download runs and is switched on.
	auto bool
}

// psHelperStatusFor words the install's state, from the phone's point of view.
func psHelperStatusFor(in psHelperStatusInput) psHelperStatus {
	st := psHelperStatus{Supported: true, Percent: -1}
	v := in.view
	switch {
	case in.running:
		st.Busy = true
		switch v.phase {
		case psHelperDownloading:
			st.State = "downloading"
			st.Text = "Remote Play is downloading on the handheld"
			switch {
			case v.total > 0:
				st.Percent = psHelperPercentValue(v)
				st.Text += ": " + itoa(st.Percent) + "% (" + formatBytes(uint64(max64(v.got, 0))) + " of " + formatBytes(uint64(v.total)) + ")."
			case v.got > 0:
				st.Text += ": " + formatBytes(uint64(v.got)) + " so far."
			default:
				st.Text += "."
			}
		case psHelperChecking:
			st.State = "installing"
			st.Text = "Remote Play is installing on the handheld: checking that it runs there."
			if v.placing {
				st.Text = "Remote Play is installing on the handheld: copying it into place."
			}
		default:
			st.State = "finding"
			st.Text = "Remote Play is downloading on the handheld: finding the download."
		}
	case !in.needed:
		st.State = "ready"
		st.Text = "Remote Play is installed on the handheld."
	case in.refusal != "":
		st.State = "refused"
		st.Text = "Remote Play won't run on this handheld's firmware: " + in.refusal + "."
		if in.refusedWhich == "regist" {
			// Streaming works; only the PS5's pairing helper was refused.
			st.Text = "PS5 pairing won't run on this handheld's firmware: " + in.refusal + "."
		}
		st.CanStart, st.StartLabel = in.online, "Try again"
	case v.phase == psHelperFailed:
		st.State = "failed"
		st.Text = "Remote Play did not install: " + v.reason + "."
		st.CanStart, st.StartLabel = in.online, "Try again"
	default:
		st.State = "missing"
		switch {
		case in.portal:
			st.Text = "Remote Play is not installed yet: the handheld's Wi-Fi wants a sign-in first."
		case !in.online:
			st.Text = "Remote Play is not installed yet: the handheld has no internet right now."
		case in.auto:
			st.Text = "Remote Play is not installed yet: it downloads by itself in the background."
		default:
			st.Text = "Remote Play is not installed yet: it downloads when PS Remote Play opens."
		}
		st.CanStart, st.StartLabel = in.online, "Download now"
	}
	return st
}

// psHelperStatusNow is psHelperStatusFor with the live state. Any goroutine:
// the install has its own lock, the setting its atomic copy, and a refusal
// kept from an earlier start is read from the card only while nothing in this
// run has said anything newer.
func psHelperStatusNow() psHelperStatus {
	if !psHelperSupportedFn() {
		return psHelperStatus{State: "unsupported", Text: "Remote Play streams on the TrimUI handhelds.", Percent: -1}
	}
	psHelperJob.mu.Lock()
	in := psHelperStatusInput{running: psHelperJob.running, view: psHelperJob.view}
	psHelperJob.mu.Unlock()
	in.needed = psHelpersNeeded()
	in.online = IsOnline()
	in.portal = PortalSignInURL() != ""
	// Not started (safe mode) is not "by itself".
	in.auto = !psBgOff.Load() && psBgStarted.Load()
	if !in.running && in.needed {
		switch {
		case in.view.phase == psHelperFailed && in.view.refused != "":
			in.refusal = strings.TrimPrefix(in.view.reason, psHelperRefusedPrefix)
			in.refusedWhich = in.view.refused
		case in.view.phase != psHelperFailed:
			if _, problem, ok := psHelperLoadRefusal(); ok {
				in.refusal = psHelperCompact(problem)
				in.refusedWhich = psHelperRefusedWhich(problem)
			}
		}
	}
	return psHelperStatusFor(in)
}

// psHelpersActivityItem is the activity strip's chip (activity.go) while
// Remote Play downloads or installs and its panel, which says the same in its
// own rows, is not on screen: the rail follows the download, a tap opens PS
// Remote Play and the X stops the install. Main thread.
func psHelpersActivityItem() (activityItem, bool) {
	if chiakiPanel.open || !psHelperSupportedFn() {
		return activityItem{}, false
	}
	psHelperJob.mu.Lock()
	running, v := psHelperJob.running, psHelperJob.view
	psHelperJob.mu.Unlock()
	if !running {
		return activityItem{}, false
	}
	item := activityItem{
		glyph:  "↓",
		label:  "Remote Play",
		frac:   -1,
		tint:   installHighlight(),
		cancel: psHelpersStopFromChip,
		open:   openChiakiPanel,
	}
	switch v.phase {
	case psHelperDownloading:
		if pct := psHelperPercentValue(v); pct >= 0 {
			item.frac = float64(pct) / 100
		}
	case psHelperChecking:
		// Checking and copying: the bytes are all in.
		item.glyph = "…"
		if v.placing {
			// The copy into bin/ is not stopped half-way: no X.
			item.cancel = nil
		}
	}
	return item, true
}

// psHelpersStopFromChip is the chip's X.
func psHelpersStopFromChip() {
	if !psHelpersStop() {
		if psHelpersRunning() {
			ShowToast("Remote Play is being put in place and can no longer be stopped", ToastKindInfo)
		}
		return
	}
	PlayBackSound()
	ShowToast("Remote Play download stopped · PS Remote Play can start it again", ToastKindInfo)
}
