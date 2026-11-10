package main

// volumectl.go — the system volume as the header's speaker button sees it.
//
// Reading or writing the mixer can mean starting a process (amixer, pactl) or
// writing the card (the TrimUI firmware's system.json, volume_trimui.go), and
// the speaker glyph is drawn on every frame. So the level lives in a small
// cache here. A background poll keeps it fresh — every few seconds, faster
// while the popover is open, and right after a hardware volume key — and every
// change goes through one worker goroutine that applies only the newest level
// asked for: a held D-pad never queues a mixer process per repeat, and the
// render thread never waits on the mixer.
//
// The popover (volumepopover.go) and Quick Settings both change the level
// through here, so the two always show the same number.

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	// volumeStep is one press of the D-pad, a key or a wheel notch. It is also
	// the grid every level the popover sets is snapped to: the TrimUI firmware
	// keeps its level in twentieths (system.json "vol"), and a level between
	// two of them would read back as the nearer one a second later.
	volumeStep = 5

	// volumePollIdle and volumePollOpen are how stale the cached level may get:
	// the glyph only needs to follow the hardware keys eventually, the open
	// popover's percentage should follow them promptly.
	volumePollIdle = 5 * time.Second
	volumePollOpen = 1500 * time.Millisecond

	// volumeUnmuteDefault is the level un-muting returns to when nothing
	// audible was ever seen this session.
	volumeUnmuteDefault = 50

	// volumeHardwareKeyDelay gives the firmware time to apply a hardware key
	// press before the level is read back.
	volumeHardwareKeyDelay = 250 * time.Millisecond
)

// volumeLevel is the cached level. pct is 0..100; known is false until the
// first reading or change. restore is the last audible level, where an
// un-mute returns to.
//
// editGen counts the changes asked for and appliedGen is the newest one the
// worker has finished writing (or failed to write). A reading is only kept
// when it started after the newest change had landed: one that raced the
// write may still report the old level, and applying it snapped the slider
// back. A fixed settle time did not cover a slow write on a busy handheld.
var volumeLevel struct {
	mu         sync.Mutex
	pct        int
	known      bool
	restore    int
	editGen    int
	appliedGen int
	pollAt     time.Time
	polling    bool
	// rerun asks for one more read as soon as the running poll ends: a
	// hardware key pressed while a poll was under way may have been read
	// before the firmware applied it (volumeRefreshNow).
	rerun bool
}

// volumeAlarmRaised is set while the alarm has borrowed the speaker at 100%
// (alarmvol.go). A reading then is the alarm's level, not the user's, so it
// must not become the level an un-mute returns to.
var volumeAlarmRaised atomic.Bool

// volumeAvail caches whether the device has a mixer the button can drive, and
// which one.
var volumeAvail struct {
	once    sync.Once
	ok      bool
	backend string
}

// statusVolumeAvailable decides whether the status cluster shows the button. A
// variable so a render test can pin it on any host.
var statusVolumeAvailable = volumeControlAvailable

// volumeControlAvailable reports whether there is a mixer to drive (see
// volumeBackend). Asked once: it only looks for binaries and a DLL, but the
// answer is wanted on every frame and does not change while JukaHub runs.
func volumeControlAvailable() bool {
	volumeAvail.once.Do(volumeFindBackend)
	return volumeAvail.ok
}

// volumeCachedBackend is volumeBackend asked once, for the callers that need
// it per press.
func volumeCachedBackend() string {
	volumeAvail.once.Do(volumeFindBackend)
	return volumeAvail.backend
}

func volumeFindBackend() {
	volumeAvail.backend = volumeBackend()
	volumeAvail.ok = volumeAvail.backend != ""
}

// volumeCurrent returns the cached level and starts a background refresh
// when the cache is due. It is cheap: the speaker glyph calls it every frame.
func volumeCurrent() (int, bool) {
	now := time.Now()
	volumeLevel.mu.Lock()
	defer volumeLevel.mu.Unlock()
	interval := volumePollIdle
	if volumePopoverOpen {
		interval = volumePollOpen
	}
	if !volumeLevel.polling && (volumeLevel.pollAt.IsZero() || now.Sub(volumeLevel.pollAt) >= interval) && volumeControlAvailable() {
		volumeLevel.polling = true
		volumeLevel.pollAt = now
		go volumePoll(volumeLevel.editGen, volumeLevel.appliedGen)
	}
	return volumeLevel.pct, volumeLevel.known
}

// volumeRefreshNow reads the mixer again without waiting for the next poll:
// the popover opening, or a hardware key that just changed the level. Safe
// from any goroutine.
func volumeRefreshNow() {
	if !volumeControlAvailable() {
		return
	}
	volumeLevel.mu.Lock()
	if volumeLevel.polling {
		// The running poll may have read the mixer before the change this
		// refresh is for; it reads once more when it ends. Dropping the
		// request left a hardware key's level (and the DAC it un-mutes) for
		// the next timed poll, five seconds or more away.
		volumeLevel.rerun = true
		volumeLevel.mu.Unlock()
		return
	}
	volumeLevel.polling = true
	volumeLevel.pollAt = time.Now()
	gen, applied := volumeLevel.editGen, volumeLevel.appliedGen
	volumeLevel.mu.Unlock()
	go volumePoll(gen, applied)
}

// volumeNoteHardwareKey is called by the hardware volume-key watcher
// (volumebuttons.go): the firmware applies the press itself, so the glyph
// reads the new level back a moment later instead of up to five seconds later.
func volumeNoteHardwareKey() {
	time.AfterFunc(volumeHardwareKeyDelay, volumeRefreshNow)
}

// volumePoll reads the mixer once, off the render thread. gen and applied are
// the change counters when the read began (see volumeLevel).
func volumePoll(gen, applied int) {
	pct, ok := systemVolume()
	volumeLevel.mu.Lock()
	defer volumeLevel.mu.Unlock()
	volumeLevel.polling = false
	if ok {
		volumeNoteReadingLocked(pct, gen, applied)
	}
	if volumeLevel.rerun {
		volumeLevel.rerun = false
		volumeLevel.polling = true
		volumeLevel.pollAt = time.Now()
		go volumePoll(volumeLevel.editGen, volumeLevel.appliedGen)
	}
}

// volumeEditCounters returns the change counters as they are now, for a
// reader that starts its own read (Quick Settings' probe) and hands the result
// to volumeNoteReadingAt later.
func volumeEditCounters() (gen, applied int) {
	volumeLevel.mu.Lock()
	defer volumeLevel.mu.Unlock()
	return volumeLevel.editGen, volumeLevel.appliedGen
}

// volumeNoteReadingAt takes a level read by someone else into the cache, under
// the same rule as the poll, judged by the counters from when that read began
// (volumeEditCounters). It reports whether the reading was current; a stale
// one must not move a slider either. Judged by the counters at drain time
// instead, a probe that raced a step's write could put the old level back.
func volumeNoteReadingAt(pct, gen, applied int) bool {
	volumeLevel.mu.Lock()
	defer volumeLevel.mu.Unlock()
	if !volumeReadingCurrent(volumeLevel.editGen, gen, applied) {
		return false
	}
	volumeNoteReadingLocked(pct, gen, applied)
	return true
}

// volumeNoteReadingLocked keeps a reading only when no change was asked for
// since it began and the newest change had already been written when it
// began: anything else may predate the write.
func volumeNoteReadingLocked(pct, gen, applied int) {
	if !volumeReadingCurrent(volumeLevel.editGen, gen, applied) {
		return
	}
	pct = volumeClamp(pct)
	volumeLevel.pct = pct
	volumeLevel.known = true
	if pct > 0 && !volumeAlarmRaised.Load() {
		volumeLevel.restore = pct
	}
}

// volumeApplyLevel moves the cached level at once and hands the mixer write to
// the worker. Quick Settings uses it as is (it persists the level itself). It
// returns the level actually applied: on the TrimUI every level goes onto the
// firmware's 5% grid, because its level is kept in twentieths and read back
// snapped to them, so an off-grid level from Quick Settings showed one number
// and read back as another a second later — and an audible level is never
// rounded down to the mute.
func volumeApplyLevel(pct int) int {
	pct = volumeClamp(pct)
	if volumeCachedBackend() == "trimui" {
		pct = volumeSnapAudible(pct)
	}
	volumeLevel.mu.Lock()
	volumeLevel.pct = pct
	volumeLevel.known = true
	volumeLevel.editGen++
	gen := volumeLevel.editGen
	if pct > 0 {
		volumeLevel.restore = pct
	}
	volumeLevel.mu.Unlock()
	volumeRequestLevel(pct, gen)
	return pct
}

// volumeSetLevel is a change from the popover: snapped to the 5% grid, applied
// through the worker, shown by the Quick Settings slider and remembered in
// jukauser.json like a Quick Settings change. Main thread only (it touches the
// Quick Settings state). It returns the level set.
func volumeSetLevel(config *Config, pct int) int {
	pct = volumeApplyLevel(volumeSnap(pct))
	if len(qs.sliders) > 0 && qs.sliders[0].Action == "volume" {
		qs.sliders[0].Value = float64(pct) / 100
		qs.lastVolume = pct
	}
	qsNoteEdit()
	volumePersist(config, pct)
	return pct
}

// volumeStepBy moves the level by delta from where it is (or from the Quick
// Settings slider's saved level before the first reading).
func volumeStepBy(config *Config, delta int) (before, after int) {
	before = volumeBase()
	return before, volumeSetLevel(config, before+delta)
}

// volumeToggleMute mutes, or returns to the last audible level.
func volumeToggleMute(config *Config) int {
	volumeLevel.mu.Lock()
	pct, known, restore := volumeLevel.pct, volumeLevel.known, volumeLevel.restore
	volumeLevel.mu.Unlock()
	if !known {
		pct = volumeBase()
	}
	if pct > 0 {
		return volumeSetLevel(config, 0)
	}
	if restore <= 0 {
		restore = volumeUnmuteDefault
	}
	return volumeSetLevel(config, restore)
}

// volumeBase is the level a step starts from: the cache once it is known,
// before that the Quick Settings slider (the saved level), else the middle.
func volumeBase() int {
	volumeLevel.mu.Lock()
	pct, known := volumeLevel.pct, volumeLevel.known
	volumeLevel.mu.Unlock()
	if known {
		return pct
	}
	if len(qs.sliders) > 0 && qs.sliders[0].Action == "volume" && qs.sliders[0].Available {
		return volumeClamp(int(qs.sliders[0].Value*100 + 0.5))
	}
	return volumeUnmuteDefault
}

// volumePersist stores the level the way Quick Settings does: in the cached
// user config (Volume + VolumeSet, which userConfigFromVariables carries
// through every save), then a debounced save.
func volumePersist(config *Config, pct int) {
	if config == nil {
		return
	}
	if cache := loadUserConfig(); cache != nil {
		cache.Variables.Volume = pct
		cache.Variables.VolumeSet = true
	}
	if user := userConfigFromVariables(config); user != nil {
		saveUserConfigDebounced(user)
	}
}

// ── The worker ─────────────────────────────────────────────────────────────

// volumeRequest is one level for the worker, with the change it belongs to.
type volumeRequest struct {
	pct int
	gen int
}

// volumeWork is the one goroutine that writes the mixer, and its one-slot
// mailbox: a new level replaces one still waiting, so only the newest is ever
// applied.
var volumeWork struct {
	once sync.Once
	ch   chan volumeRequest
}

// volumeRequestLevel asks the worker to apply pct for change gen. It never
// blocks. Main thread only: draining and refilling the one-slot mailbox is only
// safe with a single sender.
func volumeRequestLevel(pct, gen int) {
	volumeWork.once.Do(func() {
		volumeWork.ch = make(chan volumeRequest, 1)
		go volumeWorker(volumeWork.ch)
	})
	select {
	case <-volumeWork.ch:
	default:
	}
	select {
	case volumeWork.ch <- volumeRequest{pct: pct, gen: gen}:
	default:
	}
}

// volumeWorker applies levels as they arrive, and records each change as
// written once its write returns — a failed one too, so the next poll shows
// the level the mixer really has. A failure is reported once in a while, not
// once per step of a held D-pad.
func volumeWorker(ch <-chan volumeRequest) {
	var lastToast time.Time
	for req := range ch {
		err := setSystemVolume(req.pct)
		volumeLevel.mu.Lock()
		if req.gen > volumeLevel.appliedGen {
			volumeLevel.appliedGen = req.gen
		}
		volumeLevel.mu.Unlock()
		if err == nil {
			continue
		}
		logWarn("volume", "setting the volume to %d%% failed: %v", req.pct, err)
		if time.Since(lastToast) >= 10*time.Second {
			lastToast = time.Now()
			ShowToast("Volume: "+firstLine(err.Error()), ToastKindWarning)
		}
	}
}

// ── Pure helpers ───────────────────────────────────────────────────────────

// volumeClamp keeps a level inside 0..100.
func volumeClamp(pct int) int {
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// volumeSnap rounds a level to the 5% grid (volumeStep), clamped.
func volumeSnap(pct int) int {
	pct = volumeClamp(pct)
	return volumeClamp((pct + volumeStep/2) / volumeStep * volumeStep)
}

// volumeSnapAudible is volumeSnap that never turns an audible level into a
// mute: 1–2% goes to the first step, not to 0.
func volumeSnapAudible(pct int) int {
	snapped := volumeSnap(pct)
	if snapped == 0 && pct > 0 {
		return volumeStep
	}
	return snapped
}

// volumeReadingCurrent decides whether a reading started with the counters gen
// and applied may be kept now that the newest change is editGen: only when no
// change was asked for since it began, and that change had been written
// before it began.
func volumeReadingCurrent(editGen, gen, applied int) bool {
	return editGen == gen && applied == gen
}

// winmmLevelFor and winmmPercentFor convert between a percentage and winmm's
// 16-bit channel level (volume_windows.go). They live here, outside the
// Windows file, so their tests run on every host.
func winmmLevelFor(pct int) int {
	return (volumeClamp(pct)*0xFFFF + 50) / 100
}

func winmmPercentFor(level int) int {
	if level < 0 {
		level = 0
	}
	if level > 0xFFFF {
		level = 0xFFFF
	}
	return volumeClamp((level*100 + 0x7FFF) / 0xFFFF)
}
