package main

// memfree.go — making room in memory before something big starts, and
// keeping a 1 GB handheld out of the red while it idles.
//
// Juka (2026-11-19): "only 140 MB left and the AI doesn't launch sometimes".
// The offline model needs about 220 MB to start (offline_ai.go) and the
// speech recognizers 256–384 MB (stt_local.go, stt_stream.go). They used to
// refuse at once and offer "Close all apps & games" (closeall.go). Now they
// first ask memFreeEnsure for what they need, and refuse — with the same
// offer — only when it cannot be found.
//
// memFreeUp frees in increasing order of cost and stops as soon as the target
// is met:
//  1. caches: debug.FreeOSMemory hands the Go heap's free pages back, then
//     JukaHub's texture and byte caches are cleared on the main thread
//     (EvictCachesForPressure honours memFreeEvictWant; nothing here frees a
//     texture);
//  2. helpers: JukaHub's own idle helpers the start does not need — a loaded
//     Piper voice, the warm speech recognizer, the offline model's server
//     (never the one being started) and, for a start the user asked for, the
//     web-video link extractors and the media library's scan;
//  3. kernel (Linux handheld): sync, drop the clean page cache and compact
//     memory, so what is free is free now rather than after reclaim;
//  4. apps — only for a start the user asked for and only while no game or
//     app is on screen: what closeall.go's /proc scan finds (an emulator a
//     crashed JukaHub left behind, a Remote Play client), which is an orphan
//     while JukaHub's own interface is in front. JukaHub's screen children,
//     its helpers and the AI and speech servers are never touched; a toast
//     names what closed.
//
// The automatic path (memWatchTick, fed by MemoryMonitor's 5 s poll in
// adaptive.go): when MemAvailable stays under memWatchFloorMB on a handheld
// with 1.5 GB or less and no game on screen, it runs steps 1–3 with a light
// cache trim, at most every memWatchEvery (memWatchBackoff after a run that
// found little). It never closes an app: nobody asked, and something running
// beside JukaHub with nobody asking may be wanted (a Remote Play client
// playing sound, a port's helper). That stays the user's choice.
//
// A game, emulator, port or app that takes the screen with less free than the
// Video memory (VRAM) assigned in Settings (vram.go) gets steps 1–3 as
// memFreeGame: its textures were already cleared on the main thread at the
// hand-over, and it never closes an app either.
//
// "Free" here is memHeadroomMB: MemAvailable plus a cautious share of free
// swap (memSwapCreditMB, memswap.go). The read-outs (Home's gauge, the
// Hardware screen, the log) keep showing RAM alone.
//
// Never on the main thread: memFreeUp waits for the main loop and for
// processes to exit.

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// memFreePurpose is what memory is being freed for: it decides which steps
// may run and which helpers stay.
type memFreePurpose int

const (
	memFreeAuto      memFreePurpose = iota // the watcher: steps 1–3, a light trim
	memFreeAIPreload                       // a background load (the model while the user talks, the wake word's recognizer): steps 1–3
	memFreeAI                              // the model starting for a question: every step
	memFreeSpeech                          // a speech recognizer starting: every step
	memFreeGame                            // a game took the screen short of its Video memory (vram.go): steps 1–3, textures already gone
)

// memEvictTrim and memEvictFull are the forced evictions memFreeEvictWant
// asks the main loop for: half the text cache and the byte caches, or every
// texture and byte cache (EvictCachesForPressure's critical branch).
const (
	memEvictTrim int32 = 1
	memEvictFull int32 = 2
)

const (
	// memWatchFloorMB: the watcher acts when MemAvailable stays under this on
	// a handheld with memWatchSmallMB of RAM or less, and frees towards
	// memWatchFloorMB+memWatchMarginMB.
	memWatchFloorMB  = 200
	memWatchMarginMB = 60
	memWatchSmallMB  = 1536
	// memWatchPolls consecutive 5 s polls under the floor count as "stays".
	memWatchPolls   = 2
	memWatchEvery   = 3 * time.Minute
	memWatchBackoff = 15 * time.Minute // after a run that freed under memWatchLittleMB
	// memWatchLittleMB: a run that freed less found nothing worth the trouble.
	memWatchLittleMB = 16
	// memFreeKernelEvery spaces the sync and drop_caches of starts the user
	// asked for; memFreeKernelAutoEvery the watcher's (the page cache it drops
	// is read back from the card).
	memFreeKernelEvery     = 20 * time.Second
	memFreeKernelAutoEvery = 10 * time.Minute
	// memFreeAppsEvery: a second ask within it finds the same nothing.
	memFreeAppsEvery = 20 * time.Second
	// memFreePreloadEvery spaces the preload's free-ups (each orb opening).
	memFreePreloadEvery = time.Minute
	// memFreeEvictWait bounds the wait for the main loop's eviction;
	// memFreeEvictEvery spaces evictions (what they clear is drawn again).
	memFreeEvictWait  = 2 * time.Second
	memFreeEvictEvery = 15 * time.Second
	// memFreeHelperIdle: a helper used more recently than this is in use.
	memFreeHelperIdle = 15 * time.Second
)

// memFreeEvictWant is the forced eviction asked for (0 none, memEvictTrim,
// memEvictFull); EvictCachesForPressure runs it on the main thread whatever
// the pressure level and counts it in memFreeEvictDone, which the asker
// watches. Any goroutine.
var (
	memFreeEvictWant atomic.Int32
	memFreeEvictDone atomic.Uint64
)

// memFree serialises free-ups and spaces the costly steps. Under mu.
var memFree struct {
	mu         sync.Mutex
	evictAt    time.Time
	evictLevel int32
	kernelAt   time.Time
	appsAt     time.Time
	preloadAt  time.Time
}

// memFreeEnsure reports whether needMB of headroom is there, running the
// free-up for purpose first when it is not. Never on the main thread.
func memFreeEnsure(ctx context.Context, needMB uint64, purpose memFreePurpose, reason string) bool {
	if memHeadroomMB(needMB) >= needMB {
		return true
	}
	return memFreeUp(ctx, needMB, purpose, reason) >= needMB
}

// memHeadroomMB is the memory a start of needMB may count on: MemAvailable
// plus memSwapCreditMB. The largest value when /proc/meminfo cannot be read
// (Windows, unusual firmware): the process itself decides there. Any
// goroutine; two small /proc reads.
func memHeadroomMB(needMB uint64) uint64 {
	info := readProcMeminfo()
	if info.MemTotal == 0 {
		return ^uint64(0)
	}
	zramKB, fileKB := memSwapFreeKB()
	return info.MemAvail/1024 + memSwapCreditMB(info, zramKB, fileKB, needMB)
}

// memAvailNowMB is MemAvailable in MB, 0 when unknown.
func memAvailNowMB() uint64 {
	return readProcMeminfo().MemAvail / 1024
}

// memKeepHardFloorMB: raw MemAvailable under this stops a loaded helper
// whatever swap promises (memKeepShort).
const memKeepHardFloorMB = 32

// memKeepShort reports whether a loaded helper that gives way under keepMB
// (the speech server's sttServeKeepAvail, the model's offlineAILowWaterMB)
// should let its memory go now. It is memHeadroomMB(keepMB) < keepMB, the
// measure the start gates use: a RAM-only check stopped a server that swap
// credit had let start seconds earlier, and the next sound or question
// loaded it again (thrash). The hard floor stays because swap credit is a
// forecast (the kernel still has to move idle pages out) while under ~32 MB
// of real MemAvailable a 1 GB handheld is in direct reclaim, a step from
// the OOM killer, and a helper idle this instant is the cheapest thing to
// give back. false when /proc/meminfo is missing (Windows): the process
// decides there, as before. Any goroutine; /proc/swaps is read only when RAM
// alone is short.
func memKeepShort(keepMB uint64) bool {
	info := readProcMeminfo()
	if info.MemTotal == 0 {
		return false
	}
	availMB := info.MemAvail / 1024
	switch {
	case availMB < memKeepHardFloorMB:
		return true
	case availMB >= keepMB:
		return false
	}
	zramKB, fileKB := memSwapFreeKB()
	return availMB+memSwapCreditMB(info, zramKB, fileKB, keepMB) < keepMB
}

// memFreeUp frees memory for reason until memHeadroomMB reaches needMB or
// the steps purpose allows run out, logs what it did, and returns the
// headroom afterwards. Never on the main thread.
func memFreeUp(ctx context.Context, needMB uint64, purpose memFreePurpose, reason string) uint64 {
	if !memFreeLock(ctx) {
		return memHeadroomMB(needMB) // given up: the caller sees ctx.Err()
	}
	defer memFree.mu.Unlock()
	have := memHeadroomMB(needMB)
	if have >= needMB || ctx.Err() != nil {
		return have // another free-up made the room meanwhile
	}
	if purpose == memFreeAIPreload {
		// Every opening of the orb preloads: once a minute is plenty.
		if !memFree.preloadAt.IsZero() && time.Since(memFree.preloadAt) < memFreePreloadEvery {
			return have
		}
		memFree.preloadAt = time.Now()
	}
	before := memAvailNowMB()
	var steps, closed []string
	met := func() bool {
		have = memHeadroomMB(needMB)
		return have >= needMB || ctx.Err() != nil
	}
	// 1. The Go heap's free pages, then JukaHub's caches. A recent light trim
	// must not delay the full clear a question needs to start its model.
	debug.FreeOSMemory()
	steps = append(steps, "Go heap")
	level := memEvictTrim
	if purpose == memFreeAI || purpose == memFreeSpeech {
		level = memEvictFull
	}
	if !met() && memFreeEvictDue(time.Now(), memFree.evictAt, memFree.evictLevel, level) {
		if memFreeEvictCaches(level == memEvictFull) {
			memFree.evictAt = time.Now()
			memFree.evictLevel = level
			steps = append(steps, "caches")
			debug.FreeOSMemory()
		}
	}
	// 2. Idle helpers; a stopped process frees its memory as it is reaped.
	if !met() {
		if stopped := memFreeStopHelpers(purpose); len(stopped) > 0 {
			steps = append(steps, stopped...)
			memFreeSettle(ctx, needMB)
		}
	}
	// 3. The kernel's caches.
	if !met() && memFreeKernelDue(purpose) && memFreeKernel() {
		steps = append(steps, "page cache")
	}
	// 4. Apps left running in the background.
	if !met() && memFreeAppsAllowed(purpose) {
		if closed = memFreeCloseOrphans(); len(closed) > 0 {
			steps = append(steps, "apps: "+strings.Join(closed, ", "))
			memFreeSettle(ctx, needMB)
			memFreeKernel() // the closed apps' files
		}
	}
	met()
	after := memAvailNowMB()
	freed := uint64(0)
	if after > before {
		freed = after - before
	}
	logInfo("mem", "freed %d MB (%s) for %s: %d MB available (%d MB with swap), %d MB wanted",
		freed, strings.Join(steps, ", "), reason, after, have, needMB)
	if len(closed) > 0 {
		msg := memFreeClosedText(closed, purpose)
		tbxPost(func() { ShowToast(msg, ToastKindInfo) })
	}
	return have
}

// memFreeEvictDue spaces repeat clears, but lets a full AI/speech clear
// supersede a recent light trim. Pure; the caller holds memFree.mu.
func memFreeEvictDue(now, last time.Time, lastLevel, requestedLevel int32) bool {
	return last.IsZero() || now.Sub(last) >= memFreeEvictEvery || requestedLevel > lastLevel
}

// memFreeLock takes memFree.mu, or gives up when ctx ends first: another
// free-up holds it through sync, closing apps and their waits (seconds), and
// a question cancelled meanwhile must not queue behind that. It reports
// whether the lock is held.
func memFreeLock(ctx context.Context) bool {
	for !memFree.mu.TryLock() {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return true
}

// memFreeClosedText is the toast after apps closed for a start: "Closed
// RetroArch to make room for offline AI". Pure.
func memFreeClosedText(closed []string, purpose memFreePurpose) string {
	names := closeAllJoinNames(closed, 3)
	if purpose == memFreeSpeech {
		return fmt.Sprintf("Closed %s to make room for offline speech", names)
	}
	return fmt.Sprintf("Closed %s to make room for offline AI", names)
}

// memFreeSettle gives stopped processes a moment to be reaped: up to 600 ms,
// less once the target is met.
func memFreeSettle(ctx context.Context, needMB uint64) {
	for i := 0; i < 6 && ctx.Err() == nil; i++ {
		time.Sleep(100 * time.Millisecond)
		if memHeadroomMB(needMB) >= needMB {
			return
		}
	}
}

// memFreeEvictCaches asks the main loop to clear JukaHub's caches (all of
// them with full, else a trim) and waits for it, up to memFreeEvictWait. It
// reports whether the eviction ran. The handheld only: the main loop polls
// the evictor there alone (main.go). Under a game the loop is parked: nothing
// is asked, so a stale request does not clear every texture on the way back
// to the menu, long after the start that wanted the room.
func memFreeEvictCaches(full bool) bool {
	if !IsTrimuiDevice() || gameScreenSuspended() && !gameAssistOverlayActive() {
		return false
	}
	level := memEvictTrim
	if full {
		level = memEvictFull
	}
	done := memFreeEvictDone.Load()
	for {
		cur := memFreeEvictWant.Load()
		if cur >= level || memFreeEvictWant.CompareAndSwap(cur, level) {
			break
		}
	}
	for deadline := time.Now().Add(memFreeEvictWait); time.Now().Before(deadline); {
		if memFreeEvictDone.Load() != done {
			return true
		}
		time.Sleep(40 * time.Millisecond)
	}
	// The render loop did not take this request. Do not let it clear textures
	// later, after the question or game that wanted the room has moved on.
	memFreeEvictWant.CompareAndSwap(level, 0)
	return memFreeEvictDone.Load() != done
}

// memFreeTakeEvict is the main loop's side: the forced eviction asked for,
// 0 when none. Main thread (EvictCachesForPressure).
func memFreeTakeEvict() int32 {
	return memFreeEvictWant.Swap(0)
}

// memFreeStopHelpers stops JukaHub's idle helpers purpose does not need and
// names them.
func memFreeStopHelpers(purpose memFreePurpose) []string {
	var out []string
	why := "memory is needed"
	if memFreeStopIdlePiper(purpose == memFreeAI) {
		out = append(out, "Piper voice")
	}
	// The warm recognizer stays while the user talks to the model, and for
	// the wake word unless a question needs the room (it is loaded again for
	// the next one).
	if purpose != memFreeSpeech && purpose != memFreeAIPreload && sttServeStopIdle(why, purpose == memFreeAI) {
		out = append(out, "speech recognizer")
	}
	if purpose != memFreeAI && purpose != memFreeAIPreload && offlineAIServerStopIdle(why) {
		out = append(out, "offline AI")
	}
	if purpose == memFreeAI || purpose == memFreeSpeech || purpose == memFreeGame {
		// The user is talking to Juka or playing, not browsing videos: the
		// link extractors (Python, tens of MB each) and the library's ffprobe
		// scan carry on later from where they stopped.
		prefetchMu.Lock()
		windows := len(prefetchWindows)
		prefetchMu.Unlock()
		if windows > 0 {
			cancelPrefetchWork()
			out = append(out, "video link prefetch")
		}
		if mlibScanning.Load() {
			mlibCancelScan()
			out = append(out, "media library scan")
		}
	}
	return out
}

// memFreeKernelDue reports whether the kernel step may run now and marks it
// run. Under memFree.mu.
func memFreeKernelDue(purpose memFreePurpose) bool {
	every := memFreeKernelEvery
	if purpose == memFreeAuto || purpose == memFreeAIPreload {
		every = memFreeKernelAutoEvery
	}
	if !memFree.kernelAt.IsZero() && time.Since(memFree.kernelAt) < every {
		return false
	}
	memFree.kernelAt = time.Now()
	return true
}

// memFreeAppsAllowed reports whether background apps may close now: a start
// the user asked for, nothing of a game or app on screen (the overlay over a
// paused game counts), and not again within memFreeAppsEvery. Marks the step
// run. Under memFree.mu.
func memFreeAppsAllowed(purpose memFreePurpose) bool {
	if purpose != memFreeAI && purpose != memFreeSpeech {
		return false
	}
	if gameScreenSuspended() || gameAssistOverlayActive() || len(liveScreenChildren()) > 0 {
		return false
	}
	if !memFree.appsAt.IsZero() && time.Since(memFree.appsAt) < memFreeAppsEvery {
		return false
	}
	memFree.appsAt = time.Now()
	return true
}

// memFreeCloseOrphans closes what closeall.go's /proc scan finds — apps
// running while none of JukaHub's screen children is — and names them. It
// takes closeAllRunning for the time, so a close the user starts meanwhile
// says "Already closing apps". Off the main thread.
func memFreeCloseOrphans() []string {
	if !closeAllRunning.CompareAndSwap(false, true) {
		return nil
	}
	defer closeAllRunning.Store(false)
	// Orphans only: what this JukaHub runs itself in a process group of its
	// own (a Wine prefix being made, a Ports Store install) is its work, not
	// a leftover, whatever the scan's rules call it.
	var procs []closeAllProc
	for _, p := range closeAllScan(liveScreenChildren(), false) {
		if !memFreeOurDescendant(p.pid) {
			procs = append(procs, p)
		}
	}
	if len(procs) == 0 {
		return nil
	}
	var labels closeAllLabels
	for _, p := range procs {
		labels.add(p.label)
	}
	logInfo("mem", "closing apps left running in the background: %s", strings.Join(labels.list, ", "))
	closeAllEndProcs(procs)
	return labels.list
}

// ── The watcher ──────────────────────────────────────────────────────────

// memWatch is the watcher's state. The monitor's goroutine only, apart from
// running.
var memWatch struct {
	under   int
	nextRun time.Time
	started time.Time
	running atomic.Bool
}

// memWatchFloorFor is the watcher's floor for a device with totalMB of RAM:
// memWatchFloorMB on a handheld of memWatchSmallMB or less, 0 (no watcher)
// elsewhere. Pure.
func memWatchFloorFor(totalMB uint64, handheld bool) uint64 {
	if !handheld || totalMB == 0 || totalMB > memWatchSmallMB {
		return 0
	}
	return memWatchFloorMB
}

// memWatchTick is MemoryMonitor's word after each poll (adaptive.go): it
// starts the swap setup at a quiet moment (memswap.go) and the cheap steps
// when memory stays low. The monitor's goroutine; never waits.
func memWatchTick(info memInfo) {
	if runtime.GOOS != "linux" || info.MemTotal == 0 {
		return
	}
	now := time.Now()
	if memWatch.started.IsZero() {
		memWatch.started = now
	}
	if at := memWatchLater.Swap(0); at != 0 {
		if t := time.Unix(0, at); t.After(memWatch.nextRun) {
			memWatch.nextRun = t
		}
	}
	memSwapTick(now.Sub(memWatch.started))
	floor := memWatchFloorFor(info.MemTotal/1024, IsTrimuiDevice() || P().IsHandheld())
	if floor == 0 || info.MemAvail/1024 >= floor {
		memWatch.under = 0
		return
	}
	memWatch.under++
	if memWatch.under < memWatchPolls || now.Before(memWatch.nextRun) || memWatch.running.Load() ||
		gameScreenSuspended() || closeAllRunning.Load() {
		return
	}
	memWatch.nextRun = now.Add(memWatchEvery)
	memWatch.running.Store(true)
	go func() {
		defer memWatch.running.Store(false)
		defer func() {
			if r := recover(); r != nil {
				logWarn("mem", "freeing memory stopped: %v", r)
			}
		}()
		before := memAvailNowMB()
		memFreeUp(context.Background(), floor+memWatchMarginMB, memFreeAuto, "low memory")
		if after := memAvailNowMB(); after < before+memWatchLittleMB {
			// Nothing much was there to free: look again much later, not
			// every few minutes with the same result.
			memWatchLater.Store(now.Add(memWatchBackoff).UnixNano())
		}
	}()
}

// memWatchLater is a later next run a finished run asked for (UnixNano, 0
// none): the run's goroutine hands it to the monitor's, which folds it into
// memWatch.nextRun.
var memWatchLater atomic.Int64
