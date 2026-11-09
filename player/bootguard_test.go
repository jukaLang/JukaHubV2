package main

// Tests for the boot guard: how a boot is counted (healthy, failed, clean),
// when a crash loop starts safe mode, when it rolls a new build back, and that
// none of it depends on the wall clock — the handheld may have no RTC.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withBootGuardReset saves and clears the globals the boot guard writes to.
func withBootGuardReset(t *testing.T) {
	t.Helper()
	prevBoot, prevMode, prevOutcome, prevCrashed := safeboot, safeMode, bootGuardOutcome, bootGuardPrevCrashed
	prevExit, prevRequested := bootGuardPrevExit, exitRequested.Load()
	t.Cleanup(func() {
		safeboot, safeMode, bootGuardOutcome, bootGuardPrevCrashed = prevBoot, prevMode, prevOutcome, prevCrashed
		bootGuardPrevExit = prevExit
		exitRequested.Store(prevRequested)
		bootGuardResetLive()
	})
	bgPrevCrashed = false
	bootGuardPrevCrashed = func() bool { return bgPrevCrashed }
	bgPrevExit = bootGuardLastExit{}
	// Read and removed, as the start does with launch.sh's file.
	bootGuardPrevExit = func() bootGuardLastExit {
		e := bgPrevExit
		bgPrevExit = bootGuardLastExit{}
		return e
	}
	bootGuardNewProcess()
}

// bgPrevCrashed is whether the last simulated start left a crash report, and
// bgPrevExit the .last-exit line launch.sh wrote for its process (none: PID 0).
var (
	bgPrevCrashed bool
	bgPrevExit    bootGuardLastExit
)

// bootGuardNewProcess is what a fresh process starts with.
func bootGuardNewProcess() {
	safeboot = safebootState{}
	safeMode = safeModeState{}
	bootGuardOutcome = bootGuardOutcomeState{}
	exitRequested.Store(false)
	bootGuardResetLive()
}

func bootGuardResetLive() {
	bootGuardLive.mu.Lock()
	if bootGuardLive.health != nil {
		bootGuardLive.health.Stop()
	}
	bootGuardLive.dir, bootGuardLive.state, bootGuardLive.ok, bootGuardLive.health = "", bootGuardFileState{}, false, nil
	bootGuardLive.keepReverts = false
	bootGuardLive.lastGood = [len(bootGuardLastGoodFiles)][]byte{}
	bootGuardLive.mu.Unlock()
}

// How a simulated boot ends.
type bgEnd int

const (
	bgCrash   bgEnd = iota // crashes before it is healthy: a crash report is filed
	bgKilled               // drew frames, then killed before it was healthy (power cut, SIGKILL): no report
	bgEarly                // dies before its first frame without a report (a startup failure, exit 1)
	bgHealthy              // passes the health mark, then dies (a long session)
	bgClean                // its main loop ends on a quit
)

// bgResult is one boot's verdict and the safe mode it started.
type bgResult struct {
	v    bootGuardVerdict
	mode safeModeState
	due  bool
}

// bgBoot runs one start of version at the given wall clock in a fresh process
// and ends it the given way.
func bgBoot(t *testing.T, dir, version string, now time.Time, end bgEnd) bgResult {
	t.Helper()
	bootGuardNewProcess()
	v := applyBootGuard(dir, version, now)
	r := bgResult{v: v, mode: safeMode, due: bootGuardOutcome.RollbackDue}
	if end != bgEarly {
		bootGuardMarkFramed()
	}
	switch end {
	case bgHealthy:
		bootGuardMarkHealthy()
	case bgClean:
		clearBootGuard()
	}
	bgPrevCrashed = end == bgCrash
	bgPrevExit = bootGuardLastExit{} // no launch.sh line: the rule as it was before there was one
	return r
}

// bgRun runs one start in a fresh process the way the handheld sees it: it
// draws its first frame when framed, is asked to stop (requestAppExit) when
// stop is set, and launch.sh's .last-exit line gives its process's exit
// status — none when status is negative (an older launch.sh).
func bgRun(t *testing.T, dir, version string, framed, stop bool, status int) bgResult {
	t.Helper()
	bootGuardNewProcess()
	v := applyBootGuard(dir, version, time.Now())
	r := bgResult{v: v, mode: safeMode, due: bootGuardOutcome.RollbackDue}
	if framed {
		bootGuardMarkFramed()
	}
	if stop {
		exitRequested.Store(true)
		bootGuardNoteStop()
	}
	bgPrevCrashed = false
	bgPrevExit = bootGuardLastExit{}
	if status >= 0 {
		bgPrevExit = bootGuardLastExit{Status: status, PID: os.Getpid()}
	}
	return r
}

func TestBootGuardCountsBootsNotClockTime(t *testing.T) {
	installed := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	clocks := []struct {
		name string
		at   func(i int) time.Time
	}{
		{"normal", func(i int) time.Time { return installed.Add(time.Duration(i) * time.Minute) }},
		// No RTC: every cold boot starts at the same factory epoch, before the
		// update and before the previous boot.
		{"clock reset", func(i int) time.Time { return time.Date(2020, 1, 1, 0, 0, 20, 0, time.UTC) }},
		// NTP or a user set the clock years ahead between boots.
		{"clock forward", func(i int) time.Time { return installed.AddDate(5*i, 0, 0) }},
		{"clock jumping both ways", func(i int) time.Time {
			if i%2 == 0 {
				return installed.AddDate(-6, 0, 0)
			}
			return installed.AddDate(3, 0, 0)
		}},
	}
	for _, c := range clocks {
		t.Run(c.name, func(t *testing.T) {
			withBootGuardReset(t)
			dir := t.TempDir()
			// v1 ran fine and quit; v2 is installed and dies three times.
			bgBoot(t, dir, "v1", c.at(0), bgClean)
			for i := 1; i <= 3; i++ {
				r := bgBoot(t, dir, "v2", c.at(i), bgCrash)
				if r.v.SafeMode || r.v.Rollback || r.mode.Active {
					t.Fatalf("boot %d of v2 decided too early: %+v", i, r)
				}
				if want := i - 1; r.v.Streak != want {
					t.Fatalf("boot %d of v2: streak %d, want %d", i, r.v.Streak, want)
				}
			}
			r := bgBoot(t, dir, "v2", c.at(4), bgClean)
			if !r.v.SafeMode || !r.mode.Active || !strings.Contains(r.mode.Reason, "3 failed starts") {
				t.Fatalf("fourth boot: safe mode %+v, verdict %+v", r.mode, r.v)
			}
			if !r.v.Rollback || !r.due || r.v.BuildFails != 3 {
				t.Fatalf("fourth boot: a new build that failed three times must be rolled back: %+v due=%v", r.v, r.due)
			}
			// That boot quit cleanly: the next one is normal again.
			r = bgBoot(t, dir, "v2", c.at(5), bgClean)
			if r.v.Streak != 0 || r.v.SafeMode || r.mode.Active {
				t.Fatalf("after a clean exit: %+v %+v", r.v, r.mode)
			}
		})
	}
}

func TestBootGuardHealthyBootsDoNotCount(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	// Long sessions that end in a power cut or a late crash are healthy boots.
	for i := 0; i < 5; i++ {
		r := bgBoot(t, dir, "v2", now, bgHealthy)
		if r.v.Streak != 0 || r.v.PrevFailed {
			t.Fatalf("boot %d after healthy sessions: %+v", i, r.v)
		}
	}
	// Two early deaths, then a healthy session: the streak starts over.
	bgBoot(t, dir, "v2", now, bgCrash)
	bgBoot(t, dir, "v2", now, bgCrash)
	r := bgBoot(t, dir, "v2", now, bgHealthy)
	if r.v.Streak != 2 || r.v.SafeMode {
		t.Fatalf("third boot sees two failures and no safe mode yet: %+v", r.v)
	}
	r = bgBoot(t, dir, "v2", now, bgCrash)
	if r.v.Streak != 0 {
		t.Fatalf("a healthy boot must reset the streak, got %d", r.v.Streak)
	}
}

func TestBootGuardSafeModeWhateverTheUpdateHistory(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	// A build that has worked for many boots starts crashing: safe mode, but
	// it is not rolled back — the loop did not begin while it was new.
	for i := 0; i < bootGuardRecentBoots+2; i++ {
		bgBoot(t, dir, "v2", now, bgClean)
	}
	for i := 0; i < 3; i++ {
		bgBoot(t, dir, "v2", now, bgCrash)
	}
	r := bgBoot(t, dir, "v2", now, bgCrash)
	if !r.v.SafeMode || !r.mode.Active {
		t.Fatalf("three failed starts must start safe mode: %+v", r)
	}
	if r.v.Rollback || r.due {
		t.Fatalf("an established build must not be rolled back: %+v", r.v)
	}
	// Safe mode stays while the starts keep failing.
	r = bgBoot(t, dir, "v2", now, bgClean)
	if r.v.Streak != 4 || !r.mode.Active {
		t.Fatalf("a fourth failure keeps safe mode: %+v", r)
	}
}

func TestBootGuardFailuresOfAnotherBuildDoNotRollBack(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	// v1 failed twice, then v2 replaced it (by hand: no clean exit between).
	bgBoot(t, dir, "v1", now, bgClean)
	bgBoot(t, dir, "v1", now, bgCrash)
	bgBoot(t, dir, "v1", now, bgCrash)
	r := bgBoot(t, dir, "v2", now, bgCrash)
	if r.v.Streak != 2 || r.v.BuildFails != 0 || r.v.Next.Boots != 1 {
		t.Fatalf("first boot of v2: %+v", r.v)
	}
	r = bgBoot(t, dir, "v2", now, bgCrash)
	if !r.v.SafeMode {
		t.Fatalf("three failed starts in a row, across builds, start safe mode: %+v", r.v)
	}
	if r.v.Rollback || r.v.BuildFails != 1 {
		t.Fatalf("v2 failed once; it must not be rolled back for v1's failures: %+v", r.v)
	}
}

func TestBootGuardExplicitSafeModeKeepsRollbackAway(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	bgBoot(t, dir, "v1", now, bgClean)
	for i := 0; i < 3; i++ {
		bgBoot(t, dir, "v2", now, bgCrash)
	}
	bootGuardNewProcess()
	safeMode = safeModeState{Active: true, Reason: safeModeFlagPath}
	v := applyBootGuard(dir, "v2", now)
	if !v.Rollback {
		t.Fatalf("the verdict itself still says rollback: %+v", v)
	}
	if bootGuardOutcome.RollbackDue {
		t.Fatal("a safe-mode flag must keep the automatic rollback away")
	}
	if safeMode.Reason != safeModeFlagPath {
		t.Fatalf("the flag's reason must stay: %+v", safeMode)
	}

	// Neither does the recovery chord: the person holding it chooses, and the
	// recovery card offers the previous version first.
	bootGuardNewProcess()
	safeMode = safeModeState{Active: true, Reason: "recovery chord (L+R+Start)"}
	safeboot.Triggered = true
	applyBootGuard(dir, "v2", now)
	if bootGuardOutcome.RollbackDue || !bootGuardOutcome.RollbackOffered {
		t.Fatalf("with the chord held the rollback is offered, not run: %+v", bootGuardOutcome)
	}
	if s, _ := readBootGuardFile(dir); !s.Recovery {
		t.Fatal("a chord boot is recorded as one")
	}
}

func TestBootGuardNoSecondRollbackAfterARevert(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	bgBoot(t, dir, "v1", now, bgClean)
	for i := 0; i < 3; i++ {
		bgBoot(t, dir, "v2", now, bgCrash)
	}
	// The fourth start of v2 rolls back and quits into the restored build.
	bootGuardNewProcess()
	if v := applyBootGuard(dir, "v2", now); !v.Rollback || !bootGuardOutcome.RollbackDue {
		t.Fatalf("v2 must be rolled back: %+v", v)
	}
	bootGuardNoteRollback()
	clearBootGuard()
	bgPrevCrashed = false
	// The restored v1 fails three starts as well: the failures are not v2's.
	for i := 1; i <= 3; i++ {
		if r := bgBoot(t, dir, "v1", now, bgCrash); r.due || r.v.Reverts != 1 {
			t.Fatalf("start %d of the restored build: %+v", i, r.v)
		}
	}
	r := bgBoot(t, dir, "v1", now, bgClean)
	if !r.v.SafeMode || !r.mode.Active {
		t.Fatalf("three failed starts still start safe mode: %+v", r)
	}
	if r.v.Rollback || r.due || r.v.BuildFails != 3 || r.v.CrashFails != 3 {
		t.Fatalf("no second rollback, back to the build just left: %+v", r.v)
	}
	// A safe-mode start that quits cleanly proves nothing about the build...
	r = bgBoot(t, dir, "v1", now, bgClean)
	if r.v.Reverts != 1 || r.mode.Active {
		t.Fatalf("after the safe-mode start: %+v %+v", r.v, r.mode)
	}
	// ...a normal one does: the next update may be rolled back again.
	if r = bgBoot(t, dir, "v1", now, bgClean); r.v.Reverts != 0 {
		t.Fatalf("a clean normal start clears the rollback count: %+v", r.v)
	}
}

func TestBootGuardRollbackNeedsCrashEvidence(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	bgBoot(t, dir, "v1", now, bgClean)
	// Three working sessions of the new build end in a power cut or a kill.
	for i := 0; i < 3; i++ {
		bgBoot(t, dir, "v2", now, bgKilled)
	}
	r := bgBoot(t, dir, "v2", now, bgEarly)
	if !r.v.SafeMode || !r.mode.Active {
		t.Fatalf("three failed starts start safe mode whatever ended them: %+v", r)
	}
	if r.v.Rollback || r.due || r.v.CrashFails != 0 {
		t.Fatalf("kills are not crashes; the update stays: %+v", r.v)
	}
	// That start died before its first frame: a crash, and the build goes.
	r = bgBoot(t, dir, "v2", now, bgClean)
	if !r.v.Rollback || !r.due || r.v.CrashFails != 1 {
		t.Fatalf("a start that never drew a frame counts as a crash: %+v", r.v)
	}
}

// TestBootGuardDecideHowTheLastStartEnded is the decision table for one
// unfinished start (running, not healthy) of the same build: is it a failed
// start, is it crash evidence, or was it stopped on purpose — from the record,
// a crash report and launch.sh's .last-exit line.
func TestBootGuardDecideHowTheLastStartEnded(t *testing.T) {
	const pid = 4242
	line := func(status int) bootGuardLastExit { return bootGuardLastExit{Status: status, PID: pid} }
	none := bootGuardLastExit{}
	other := func(status int) bootGuardLastExit { return bootGuardLastExit{Status: status, PID: pid + 1} }
	// framed, stopping: the record; report: a crash report was filed for it;
	// failed, crash, stopped: the verdict.
	cases := []struct {
		name     string
		framed   bool
		stopping bool
		report   bool
		exit     bootGuardLastExit
		failed   bool
		crash    bool
		stopped  bool
	}{
		// No line (Windows, a desktop, an older launch.sh): the rule as before.
		{"no line, killed after its first frame", true, false, false, none, true, false, false},
		{"no line, died before its first frame", false, false, false, none, true, true, false},
		{"no line, crash report", true, false, true, none, true, true, false},
		// What launch.sh saw.
		{"137 after the first frame (out-of-memory killer)", true, false, false, line(137), true, true, false},
		{"139 segfault in C", true, false, false, line(139), true, true, false},
		{"134 abort", true, false, false, line(134), true, true, false},
		{"2 Go crash without a report", true, false, false, line(2), true, true, false},
		{"1 fatal error after the first frame", true, false, false, line(1), true, true, false},
		{"143 SIGTERM", true, false, false, line(143), false, false, true},
		{"130 SIGINT before the first frame", false, false, false, line(130), false, false, true},
		{"129 SIGHUP", true, false, false, line(129), false, false, true},
		{"0 forced exit nobody asked for, no frame", false, false, false, line(0), true, false, false},
		{"0 after the first frame, quit never recorded", true, false, false, line(0), true, false, false},
		// Asked to stop (requestAppExit) before the loop ended.
		{"stop during the splash, forced by the watchdog", false, true, false, line(0), false, false, true},
		{"stop during the splash, forced, no line", false, true, false, none, false, false, true},
		{"stop escalated to SIGKILL (137)", false, true, false, line(137), false, false, true},
		{"stop, then a Go crash (2)", false, true, false, line(2), true, true, false},
		{"stop, then a segfault (139)", true, true, false, line(139), true, true, false},
		{"stop, then a crash report, no line", false, true, true, none, true, true, false},
		// A line about another process says nothing about this record.
		{"another process's 137, framed", true, false, false, other(137), true, false, false},
		{"another process's 0, no frame", false, false, false, other(0), true, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prev := bootGuardFileState{Version: "v2", Boots: 2, Running: true, Framed: c.framed,
				Stopping: c.stopping, Reverts: 1, PID: pid}
			v := bootGuardDecide(prev, true, "v2", time.Now(), c.report, c.exit)
			crash := v.CrashFails > 0
			if v.PrevFailed != c.failed || crash != c.crash || v.PrevStopped != c.stopped {
				t.Fatalf("failed=%v crash=%v stopped=%v, want %v %v %v: %+v",
					v.PrevFailed, crash, v.PrevStopped, c.failed, c.crash, c.stopped, v)
			}
			wantStreak := 0
			if c.failed {
				wantStreak = 1
			}
			if v.Next.Unclean != wantStreak {
				t.Fatalf("streak written = %d, want %d", v.Next.Unclean, wantStreak)
			}
			if v.Reverts != 1 {
				t.Fatalf("an unfinished start never clears the rollback count: %+v", v)
			}
			if matched := c.exit.PID == pid; (v.Exit.PID != 0) != matched {
				t.Fatalf("line used = %+v, want it only when it names the process", v.Exit)
			}
		})
	}
	// A finished start is not judged by its exit status.
	for _, prev := range []bootGuardFileState{
		{Version: "v2", Boots: 2, Running: true, Healthy: true, Framed: true, PID: pid},
		{Version: "v2", Boots: 2, Framed: true, PID: pid},
	} {
		if v := bootGuardDecide(prev, true, "v2", time.Now(), false, line(137)); v.PrevFailed || v.PrevStopped {
			t.Fatalf("a healthy or cleanly ended start: %+v", v)
		}
	}
}

func TestBootGuardStopDuringTheSplashIsNotAFailedStart(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
	}{
		{"forced exit 0", 0},
		{"no line (an older launch.sh)", -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			withBootGuardReset(t)
			dir := t.TempDir()
			bgBoot(t, dir, "v1", time.Now(), bgClean)
			// A slow card: three starts of the new build are stopped during the
			// splash, and the exit watchdog has to force each stop.
			for i := 1; i <= 3; i++ {
				if r := bgRun(t, dir, "v2", false, true, c.status); r.v.PrevFailed || r.v.Streak != 0 {
					t.Fatalf("start %d: %+v", i, r.v)
				}
			}
			r := bgRun(t, dir, "v2", true, false, c.status)
			if !r.v.PrevStopped || r.v.PrevFailed || r.v.Streak != 0 || r.v.SafeMode || r.v.Rollback || r.mode.Active || r.due {
				t.Fatalf("stops are not failed starts, and a good build stays: %+v %+v", r.v, r.mode)
			}
		})
	}
}

func TestBootGuardKillAfterTheFirstFrameIsACrash(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	bgBoot(t, dir, "v1", time.Now(), bgClean)
	// The new build runs out of memory a few seconds after its first frame,
	// three starts in a row: launch.sh saw 137 each time.
	for i := 0; i < 3; i++ {
		bgRun(t, dir, "v2", true, false, 137)
	}
	r := bgRun(t, dir, "v2", true, false, 0)
	if !r.v.SafeMode || !r.v.Rollback || !r.due || r.v.CrashFails != 3 {
		t.Fatalf("an out-of-memory kill is a crash: %+v due=%v", r.v, r.due)
	}
}

func TestBootGuardForcedExitWithoutAStopIsNoCrash(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	bgBoot(t, dir, "v1", time.Now(), bgClean)
	// Three starts end before their first frame with status 0 and no stop
	// request: failed starts, but nothing crashed.
	for i := 0; i < 3; i++ {
		bgRun(t, dir, "v2", false, false, 0)
	}
	r := bgRun(t, dir, "v2", true, false, 0)
	if !r.v.SafeMode || !r.mode.Active || r.v.Streak != 3 {
		t.Fatalf("they still count as failed starts: %+v %+v", r.v, r.mode)
	}
	if r.v.Rollback || r.due || r.v.CrashFails != 0 {
		t.Fatalf("an exit 0 is not a crash, framed or not: %+v", r.v)
	}
}

func TestBootGuardRecordsTheStopRequest(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	stopping := func() bool {
		bootGuardLive.mu.Lock()
		defer bootGuardLive.mu.Unlock()
		return bootGuardLive.state.Stopping
	}
	// No request: the watchdog's wait is over at once and notes nothing.
	bootGuardNewProcess()
	applyBootGuard(dir, "v2", time.Now())
	select {
	case <-bootGuardNoteStopAsync():
	default:
		t.Fatal("without a stop request there is nothing to wait for")
	}
	if s, _ := readBootGuardFile(dir); s.Stopping || stopping() {
		t.Fatalf("no request, no stop: %+v", s)
	}
	// A request (a signal, the Exit button) marks the record off the caller's
	// goroutine.
	requestAppExit()
	for deadline := time.Now().Add(5 * time.Second); !stopping(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("requestAppExit did not mark the boot record")
		}
	}
	if s, _ := readBootGuardFile(dir); !s.Stopping || !s.Running {
		t.Fatalf("record after a stop request = %+v", s)
	}
	<-bootGuardNoteStopAsync()
	// A loop that ended on the quit is a clean exit; nothing marks it again.
	clearBootGuard()
	bootGuardNoteStop()
	if s, _ := readBootGuardFile(dir); s.Running {
		t.Fatalf("record after the clean exit = %+v", s)
	}
	// A stop asked for before the record existed is written with it.
	bootGuardNewProcess()
	exitRequested.Store(true)
	bootGuardNoteStop()
	applyBootGuard(dir, "v2", time.Now())
	if s, _ := readBootGuardFile(dir); !s.Stopping || s.PID != os.Getpid() {
		t.Fatalf("an early stop request must reach the record: %+v", s)
	}
}

func TestBootGuardLastExitLine(t *testing.T) {
	parse := []struct {
		in   string
		want bootGuardLastExit
	}{
		{"137 4242\n", bootGuardLastExit{Status: 137, PID: 4242}},
		{" 0   1 \r\n", bootGuardLastExit{Status: 0, PID: 1}},
		{"2 77", bootGuardLastExit{Status: 2, PID: 77}},
		{"", bootGuardLastExit{}},
		{"137", bootGuardLastExit{}},
		{"137 0", bootGuardLastExit{}},
		{"-1 5", bootGuardLastExit{}},
		{"256 5", bootGuardLastExit{}},
		{"x 5", bootGuardLastExit{}},
		{"1 2 3", bootGuardLastExit{}},
	}
	for _, c := range parse {
		if got := bootGuardParseLastExit([]byte(c.in)); got != c.want {
			t.Errorf("parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, bootGuardLastExitFile)
	if got := bootGuardReadLastExit(dir); got.PID != 0 {
		t.Fatalf("no file, no line: %+v", got)
	}
	if err := os.WriteFile(path, []byte("139 321\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := bootGuardReadLastExit(dir); got != (bootGuardLastExit{Status: 139, PID: 321}) {
		t.Fatalf("read = %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the line is read once: %v", err)
	}
	// Damaged: read only up to the bound, ignored, and removed all the same.
	if err := os.WriteFile(path, []byte(strings.Repeat("9 ", 4096)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := bootGuardReadLastExit(dir); got.PID != 0 {
		t.Fatalf("a damaged file is no line: %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a damaged line is removed too: %v", err)
	}
	if got := (bootGuardLastExit{Status: 137, PID: 9}).String(); got != "137 (signal 9)" {
		t.Errorf("String = %q", got)
	}
}

func TestBootGuardRecordHasNoBackupCopy(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	bgBoot(t, dir, "v2", time.Now(), bgClean)
	for _, extra := range []string{bootGuardFile + ".bak", bootGuardFile + ".tmp"} {
		if _, err := os.Stat(filepath.Join(dir, extra)); !os.IsNotExist(err) {
			t.Errorf("%s must not be left beside the record: %v", extra, err)
		}
	}
	if s, ok := readBootGuardFile(dir); !ok || s.Running || !s.Framed {
		t.Fatalf("record = %+v ok=%v", s, ok)
	}
}

func TestBootGuardKeepsTheSettingsOfAHealthyStart(t *testing.T) {
	withBootGuardReset(t)
	dir, appDir := t.TempDir(), t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(appDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("jukauser.json", `{"at":"boot"}`)
	write("jukaconfig.json", `not json`)
	bootGuardNewProcess()
	applyBootGuard(dir, "v2", time.Now())
	files := bootGuardCaptureSettings(appDir)
	bootGuardLive.mu.Lock()
	bootGuardLive.lastGood = files
	bootGuardLive.mu.Unlock()
	// The session changes a setting before the health mark: the copy is what
	// the start read, not what it saved.
	write("jukauser.json", `{"at":"later"}`)
	bootGuardMarkHealthy()
	got, err := os.ReadFile(filepath.Join(dir, bootGuardLastGoodDir, "jukauser.json"))
	if err != nil || string(got) != `{"at":"boot"}` {
		t.Fatalf("last good user settings = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, bootGuardLastGoodDir, "jukaconfig.json")); !os.IsNotExist(err) {
		t.Fatalf("a file that does not parse is not kept as good: %v", err)
	}
}

func TestBootGuardStartRollbackIfDue(t *testing.T) {
	withBootGuardReset(t)
	prev := bootGuardRollback
	t.Cleanup(func() { bootGuardRollback = prev })
	calls := 0
	bootGuardRollback = func(*Config) (string, string, error) {
		calls++
		return "v1_20261109T120000Z", "v1", nil
	}
	bootGuardStartRollbackIfDue(nil)
	if calls != 0 {
		t.Fatal("no rollback without a verdict")
	}
	bootGuardOutcome.RollbackDue = true
	bootGuardStartRollbackIfDue(nil)
	bootGuardStartRollbackIfDue(nil)
	if calls != 1 {
		t.Fatalf("rollback ran %d times, want once", calls)
	}
	if !safeboot.AutoRevert || safeboot.Version != "v1" {
		t.Fatalf("safeboot = %+v", safeboot)
	}
	if got := safebootDiagnostic(); !strings.Contains(got, "Boot guard") || !strings.Contains(got, "v1") {
		t.Fatalf("diagnostics line = %q", got)
	}
}

func TestBootGuardLegacyRecordIsNotCounted(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	// Written by a build from before the boot count: version, unclean, last_boot.
	legacy := []byte(`{"version":"JukaHub-20261001000000","unclean":2,"last_boot":"2026-10-01T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, bootGuardFile), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	r := bgBoot(t, dir, "v2", time.Now(), bgCrash)
	if r.v.Streak != 0 || r.v.PrevFailed || r.v.Next.Boots != 1 || r.mode.Active {
		t.Fatalf("a legacy record must start the count fresh: %+v", r)
	}
}

func TestBootGuardSafeNextIsOneShot(t *testing.T) {
	withBootGuardReset(t)
	dir := t.TempDir()
	now := time.Now()
	bootGuardNewProcess()
	applyBootGuard(dir, "v2", now)
	if !bootGuardRequestSafeNext("chosen after a crash") {
		t.Fatal("the request was not recorded")
	}
	clearBootGuard()
	r := bgBoot(t, dir, "v2", now, bgClean)
	if !r.mode.Active || r.mode.Reason != "chosen after a crash" {
		t.Fatalf("the next boot must start in safe mode: %+v", r.mode)
	}
	r = bgBoot(t, dir, "v2", now, bgClean)
	if r.mode.Active {
		t.Fatalf("the request is used once: %+v", r.mode)
	}
}

func TestBootGuardCleanExitKeepsTheRecord(t *testing.T) {
	withBootGuardReset(t)
	dir := withTempPatchState(t)
	bootGuardNewProcess()
	applyBootGuard(dir, "v2", time.Now())
	clearBootGuard()
	s, ok := readBootGuardFile(dir)
	if !ok {
		t.Fatal("the record must survive a clean exit: its boot count tells a new build from an old one")
	}
	if s.Running || s.Boots != 1 || s.Version != "v2" {
		t.Fatalf("record after a clean exit = %+v", s)
	}
}

func TestBootGuardStamp(t *testing.T) {
	cases := []struct {
		tag, built string
		size       int64
		want       string
	}{
		{"JukaHub-20261109120000", "2026-11-09T12:00:00Z", 1234, "JukaHub-20261109120000@2026-11-09T12:00:00Z@1234"},
		{"", "", 0, "build"},
		{" ", "build:2026-11-09", 0, "build:2026-11-09"},
	}
	for _, c := range cases {
		if got := bootGuardStampFrom(c.tag, c.built, c.size); got != c.want {
			t.Errorf("stamp(%q, %q, %d) = %q, want %q", c.tag, c.built, c.size, got, c.want)
		}
	}
	// A rollback restores an older binary under the same release stamp file:
	// the size keeps the two builds apart.
	if bootGuardStampFrom("v2", "", 1000) == bootGuardStampFrom("v2", "", 1001) {
		t.Error("two binaries under one tag must not share a stamp")
	}
}
