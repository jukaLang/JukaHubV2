package main

// brightnessctl.go — the screen brightness as the header's sun button sees it.
//
// The sun sits immediately left of the volume speaker in the status cluster
// (statusbar_status.go draws it) and opens the brightness card
// (brightnesspopover.go). Quick Settings' Brightness slider, the Settings row
// built on it, the control profiles' brightness actions and the QR card's
// full-brightness boost all change the level through here, so every surface
// shows the same number.
//
// Which hardware moves depends on the device, and it is found out once
// (brightnessFindBackend):
//
//   - "disp": the Allwinner display driver's ioctl on /dev/disp
//     (brightness_disp_linux.go). It is what the TrimUI Smart Pro and Brick
//     firmware, MinUI and spruceOS drive; the old sysfs-only code had nothing
//     to move on a kernel without a /sys/class/backlight device.
//   - "sysfs": a writable /sys/class/backlight device (systemcontrol.go).
//   - "wmi" and "ddc": on Windows, an external monitor through DDC/CI, or a
//     laptop or handheld panel through WMI (brightness_windows.go).
//   - "soft": none of those. The picture itself is dimmed by a black layer
//     drawn over every frame (brightnessSoftRender). It saves no power, and
//     the card says so, but the slider still does what it says.
//
// Reading or writing can mean a process (PowerShell) or a slow bus (DDC/CI),
// and the sun is drawn on every frame, so the level lives in a cache like the
// volume's (volumectl.go): a background poll keeps it fresh, every change goes
// through one worker that applies only the newest level asked for, and change
// counters keep a reading that raced a write from snapping the slider back.
// The main loop's brightnessFrameTick does the rest of the main-thread work.
//
// On the TrimUI the firmware keeps a level of its own: "brightness" in
// /mnt/UDISK/system.json, eleven steps, which its keymon re-reads when the
// file changes and its brightness keys move. So there the slider walks the
// firmware's 10% steps, every level the user sets is written back to the file
// (brightness_trimui.go), each step is written with the panel level the
// firmware itself was seen to use for it (brightnessFirmwareCurve), and
// nothing JukaHub saved is pushed at launch: the firmware already restored
// its level, and its keys may have moved it since. Windows owns its brightness
// the same way. Only where JukaHub is the sole owner (the software dimmer, a
// plain Linux backlight) does the level saved in jukauser.json come back.

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The backends, as brightnessFindBackend names them.
const (
	brightnessBackendDisp  = "disp"
	brightnessBackendSysfs = "sysfs"
	brightnessBackendWMI   = "wmi"
	brightnessBackendDDC   = "ddc"
	brightnessBackendSoft  = "soft"
)

const (
	// brightnessStep is one press of the D-pad, a key or a wheel notch, and the
	// grid every level is snapped to. brightnessStepFirmware is the TrimUI's:
	// its firmware keeps eleven levels, and a level between two of them would
	// be rounded by the firmware the next time it applied its file. A Windows
	// panel that only has eleven levels gets it too (brightnessWMIGrid).
	brightnessStep         = 5
	brightnessStepFirmware = 10

	// brightnessPollIdle and brightnessPollOpen are how stale the cached level
	// may get with the card closed and open, on the backends that are cheap to
	// read (a file, an ioctl).
	brightnessPollIdle = 5 * time.Second
	brightnessPollOpen = 1500 * time.Millisecond
	// brightnessPollSlowOpen is the open card's interval on Windows, where a
	// reading starts PowerShell or talks to the monitor over DDC/CI. With the
	// card (or Quick Settings) closed those backends are not polled at all: the
	// sun only needs the level the probe read and the changes made here.
	brightnessPollSlowOpen = 4 * time.Second

	// brightnessUndimDefault is where "Brighten" returns when no level above
	// the floor was seen this session.
	brightnessUndimDefault = 60

	// brightnessHardwareKeyDelay gives the firmware time to apply its own
	// brightness keys before the level is read back.
	brightnessHardwareKeyDelay = 300 * time.Millisecond

	// brightnessFirmwareFileCheck is how often the TrimUI's settings file is
	// looked at for a change made by the firmware (its brightness keys).
	brightnessFirmwareFileCheck = time.Second

	// brightnessFullPct is the top of the scale; the QR card borrows it.
	brightnessFullPct = 100

	// dispBrightnessMax is the top of the Allwinner display driver's backlight
	// scale (brightness_disp_linux.go). It lives here so the percentage
	// mapping builds and is tested on every host.
	dispBrightnessMax = 255
)

// brightnessLevel is the cached level. pct is 0..100; known is false until
// the first reading or change. restore is the last level above the floor,
// where "Brighten" returns after "Dim". editGen and appliedGen are the change
// counters, with the same rule as the volume's (volumeReadingCurrent): a
// reading is kept only when it began after the newest change had landed.
var brightnessLevel struct {
	mu         sync.Mutex
	pct        int
	known      bool
	restore    int
	editGen    int
	appliedGen int
	pollAt     time.Time
	polling    bool
	// rerun asks for one more reading as soon as the running poll ends (a
	// refresh asked for while it ran, brightnessRefreshNow).
	rerun bool
	// seenStep and seenRaw are the TrimUI step and panel level the last
	// current reading paired; the curve learns a pair only when the next
	// reading pairs them again (brightnessNoteReadingLocked).
	seenStep, seenRaw int
}

// brightnessBorrowed is set while the QR card has the screen at full
// (qrzoom.go). A reading then finds the card's level, not the user's, so
// polls wait until it is given back.
var brightnessBorrowed atomic.Bool

// brightnessAvail is the backend, found once. backend is "" until the probe
// has answered: on Windows the probe may run PowerShell, so it runs on its own
// goroutine and the sun appears a moment after the first frames.
var brightnessAvail struct {
	once    sync.Once
	mu      sync.Mutex
	backend string
}

// brightnessQSSynced records that Quick Settings' row has taken the probe's
// answer (brightnessMainTick). Main thread only.
var brightnessQSSynced bool

// brightnessPendingRestore is the level saved in jukauser.json waiting for
// the backend to be known (brightnessNoteSavedLevel), plus one; 0 means none.
var brightnessPendingRestore atomic.Int32

// brightnessSoftDim is the software dimmer's strength: 100 minus its level, so
// the zero value draws nothing.
var brightnessSoftDim atomic.Int32

// brightnessWMIGrid is the step a Windows panel's own levels allow (the
// probe reads how many it has); 0 means the usual grid.
var brightnessWMIGrid atomic.Int32

// brightnessFirmwareCurve is the panel's raw level seen at each of the TrimUI
// firmware's steps (index 0..10; 0 = not seen yet), learned from readings
// where the firmware's file and the panel were read together with nothing of
// JukaHub's on its way. Writing a step with the firmware's own raw level means
// keymon re-applying its file, or the next press of its keys, does not jump;
// and a QR card that borrowed full brightness gives back exactly what the
// panel had. A step not seen yet is written on the linear scale.
var brightnessFirmwareCurve [trimuiBrightnessSteps + 1]atomic.Int32

// brightnessLastRaw is the raw backlight level JukaHub itself wrote last. The
// curve never learns it back (nor a level one off it): it would only be
// learning its own guess, and a driver that reports a level one past the one
// it was given would walk the curve up a little with every write.
var brightnessLastRaw atomic.Int32

// statusBrightnessAvailable decides whether the status cluster shows the sun.
// A variable so a render test can pin it on any host.
var statusBrightnessAvailable = brightnessControlAvailable

// brightnessOnTrimui says whether this is a TrimUI handheld, whose firmware
// keeps a level of its own. A variable so the tests can be a TrimUI on any
// host.
var brightnessOnTrimui = IsTrimuiDevice

// brightnessControlAvailable reports whether the sun is shown: always, once
// the probe has answered — the software dimmer is the last resort.
func brightnessControlAvailable() bool {
	return brightnessCachedBackend() != ""
}

// brightnessCachedBackend is the backend, starting the probe on the first
// call (the main loop's frame tick, or the header, on the first frame).
func brightnessCachedBackend() string {
	brightnessAvail.once.Do(brightnessStartProbe)
	return brightnessKnownBackend()
}

// brightnessKnownBackend is the backend the probe found, "" before it has
// answered. It never starts the probe, so the startup code that runs before
// the window exists (qsInit) can ask it.
func brightnessKnownBackend() string {
	brightnessAvail.mu.Lock()
	defer brightnessAvail.mu.Unlock()
	return brightnessAvail.backend
}

// brightnessStartProbe runs the probe: on its own goroutine where it may
// block (Windows), at once elsewhere, where it is a few file reads and one
// ioctl. The window is noted first, so DDC/CI asks the monitor it is on.
func brightnessStartProbe() {
	brightnessNoteWindow()
	if brightnessProbeBlocks {
		go brightnessRunProbe()
		return
	}
	brightnessRunProbe()
}

// brightnessRunProbe finds the backend and seeds the cache with the level the
// probe read on the way, before the backend is published: the sun appears
// already knowing its level, and no second PowerShell runs for it.
func brightnessRunProbe() {
	b, r := brightnessFindBackend()
	if r.ok {
		brightnessLevel.mu.Lock()
		brightnessNoteReadingLocked(b, r, 0, 0)
		brightnessLevel.pollAt = time.Now()
		brightnessLevel.mu.Unlock()
	}
	brightnessAvail.mu.Lock()
	brightnessAvail.backend = b
	brightnessAvail.mu.Unlock()
	logInfo("brightness", "screen brightness: %s", brightnessBackendLabel(b))
	if brightnessFirmwareSync(b) {
		// The panel is read on its own: the reading only carries its raw level
		// when the firmware's file had a usable level, and the cases where it
		// had none are the ones this line is for.
		raw, _, _ := brightnessHardwareRaw(b)
		trimuiLogBrightnessEvidence(raw)
	}
}

// brightnessFindBackend picks the backend, best first, and reads the level on
// the way (ok=false when it cannot say). See the file comment for the order.
// On the Smart Pro and the Brick the display driver comes first even if a
// sysfs backlight shows up: it is the path their firmware drives. The newer
// TrimUI boards (Smart Pro S, Brick Pro) run a newer kernel whose backlight
// has a sysfs device, which comes first there.
func brightnessFindBackend() (string, brightnessReading) {
	if b, pct, ok := brightnessPlatformProbe(); b != "" {
		return b, brightnessReading{pct: pct, ok: ok, step: -1}
	}
	b := brightnessBackendSoft
	trimui := brightnessOnTrimui()
	newer := trimui && brightnessNewerTrimui()
	switch {
	case trimui && !newer && dispBrightnessAnswers():
		b = brightnessBackendDisp
	case sysfsBrightnessAvailable():
		b = brightnessBackendSysfs
	case trimui && dispBrightnessAnswers():
		b = brightnessBackendDisp
	case !trimui && dispBrightnessAvailable():
		b = brightnessBackendDisp
	}
	return b, brightnessRead(b)
}

// brightnessNewerTrimui reports a TrimUI board on the newer kernel (TG5050,
// TG4040), where the backlight is a sysfs device.
func brightnessNewerTrimui() bool {
	switch DetectDevice() {
	case DeviceTrimuiSmartProS, DeviceTrimuiBrickPro:
		return true
	}
	return false
}

// dispBrightnessAnswers reports whether the display driver answers the
// brightness query at all. On a TrimUI the driver is known to be the sunxi one,
// so any answer will do — even 0, a backlight someone else switched off;
// elsewhere dispBrightnessAvailable's stricter test applies.
func dispBrightnessAnswers() bool {
	_, err := dispBrightnessRaw()
	return err == nil
}

// brightnessBackendLabel names a backend for the log.
func brightnessBackendLabel(b string) string {
	switch b {
	case brightnessBackendDisp:
		return "display driver (/dev/disp)"
	case brightnessBackendSysfs:
		return "sysfs backlight"
	case brightnessBackendWMI:
		return "Windows display brightness (WMI)"
	case brightnessBackendDDC:
		return "monitor brightness (DDC/CI)"
	case brightnessBackendSoft:
		return "software dimmer (no brightness control found)"
	}
	return "none"
}

// brightnessFirmwareSync reports whether b moves a TrimUI's backlight, whose
// level the firmware keeps in system.json too.
func brightnessFirmwareSync(b string) bool {
	return (b == brightnessBackendDisp || b == brightnessBackendSysfs) && brightnessOnTrimui()
}

// brightnessOwnedByJukaHub reports whether JukaHub is the level's only owner
// on b, and so restores the level it saved at launch: the software dimmer and
// a plain Linux backlight. The TrimUI firmware and Windows keep their own.
func brightnessOwnedByJukaHub(b string) bool {
	switch b {
	case brightnessBackendSoft:
		return true
	case brightnessBackendDisp, brightnessBackendSysfs:
		return !brightnessOnTrimui()
	}
	return false
}

// brightnessGridFor is the step levels snap to on b.
func brightnessGridFor(b string) int {
	if brightnessFirmwareSync(b) {
		return brightnessStepFirmware
	}
	if b == brightnessBackendWMI {
		if g := int(brightnessWMIGrid.Load()); g > 0 {
			return g
		}
	}
	return brightnessStep
}

// brightnessFloorFor is the lowest level JukaHub sets on b: a wrong press must
// never leave a panel too dark to find the way back. It is at least one step
// of b's grid — on the TrimUI the firmware's first step above its own
// minimum.
func brightnessFloorFor(b string) int {
	floor := qsBrightnessFloorPct
	if g := brightnessGridFor(b); g > floor {
		floor = g
	}
	return floor
}

// brightnessGrid and brightnessFloor are the two for the backend in use.
func brightnessGrid() int  { return brightnessGridFor(brightnessKnownBackend()) }
func brightnessFloor() int { return brightnessFloorFor(brightnessKnownBackend()) }

// ── Reading and writing the hardware ───────────────────────────────────────

// brightnessReading is one reading: the level and whether there is one, and
// on a TrimUI the firmware's step with the panel's raw level read beside it
// (step -1 when the level did not come from the firmware's file, raw 0 when
// the panel did not answer).
type brightnessReading struct {
	pct  int
	ok   bool
	step int
	raw  int
}

// brightnessRead reads the level through b. On the TrimUI the firmware's own
// level (system.json) is the one shown while JukaHub has no write of its own
// on the way: its keys set it, in its steps. It may block (a PowerShell
// process on Windows): the header reads it through the poll.
func brightnessRead(b string) brightnessReading {
	r := brightnessReading{step: -1}
	if brightnessFirmwareSync(b) {
		if step, ok := trimuiSystemBrightnessStep(); ok {
			r.pct, r.ok, r.step = step*brightnessFullPct/trimuiBrightnessSteps, true, step
			if raw, _, err := brightnessHardwareRaw(b); err == nil && raw > 0 {
				r.raw = raw
			}
			return r
		}
	}
	r.pct, r.ok = brightnessHardwareLevel(b)
	return r
}

// brightnessHardwareLevel reads what the hardware itself reports.
func brightnessHardwareLevel(b string) (int, bool) {
	switch b {
	case brightnessBackendDisp, brightnessBackendSysfs:
		raw, max, err := brightnessHardwareRaw(b)
		if err != nil {
			return 0, false
		}
		return brightnessLevelForRaw(b, raw, max), true
	case brightnessBackendWMI:
		return wmiBrightness()
	case brightnessBackendDDC:
		return ddcBrightness()
	case brightnessBackendSoft:
		return brightnessFullPct - int(brightnessSoftDim.Load()), true
	}
	return 0, false
}

// brightnessHardwareRaw reads a backlight backend's raw level and the top of
// its scale.
func brightnessHardwareRaw(b string) (raw, max int, err error) {
	switch b {
	case brightnessBackendDisp:
		raw, err = dispBrightnessRaw()
		return raw, dispBrightnessMax, err
	case brightnessBackendSysfs:
		return sysfsBacklightRaw()
	}
	return 0, 0, errors.New("no raw backlight level on " + b)
}

// setScreenBrightnessVia applies a 0..100 level through b. It may block; the
// app calls it on the worker (brightnessWorker). The TrimUI firmware's file is
// not written here: brightnessApplyLevel schedules that for user levels only.
func setScreenBrightnessVia(b string, pct int) error {
	pct = brightnessClamp(pct)
	switch b {
	case brightnessBackendDisp:
		raw := brightnessRawForLevel(b, pct, dispBrightnessMax)
		brightnessLastRaw.Store(int32(raw))
		return dispSetBrightnessRaw(raw)
	case brightnessBackendSysfs:
		// Only the scale is needed: a backlight whose level cannot be read can
		// still be set.
		_, max, err := findBacklight(backlightRoot)
		if err != nil {
			return err
		}
		raw := brightnessRawForLevel(b, pct, max)
		brightnessLastRaw.Store(int32(raw))
		return sysfsSetBacklightRaw(raw)
	case brightnessBackendWMI:
		return wmiSetBrightness(pct)
	case brightnessBackendDDC:
		return ddcSetBrightness(pct)
	case brightnessBackendSoft:
		brightnessSoftDim.Store(int32(brightnessFullPct - pct))
		return nil
	}
	return errors.New("no brightness control on this device")
}

// brightnessRawForLevel is the raw level a percentage is written as on b: on
// a TrimUI the raw level the firmware itself was seen to use for that step,
// otherwise the linear mapping (brightnessRawFor), which keeps the floor.
func brightnessRawForLevel(b string, pct, max int) int {
	if brightnessFirmwareSync(b) {
		if raw := int(brightnessFirmwareCurve[trimuiBrightnessStepFor(pct)].Load()); raw > 0 && raw <= max {
			return raw
		}
	}
	return brightnessRawFor(pct, max)
}

// brightnessLevelForRaw is brightnessRawForLevel's inverse on b: on a TrimUI
// the firmware step whose raw level is nearest (so a step written from the
// learned curve reads back as itself even before the firmware's file has
// it), otherwise the linear mapping.
func brightnessLevelForRaw(b string, raw, max int) int {
	if !brightnessFirmwareSync(b) {
		return brightnessPercentFor(raw, max)
	}
	best, bestD := 1, -1
	for step := 1; step <= trimuiBrightnessSteps; step++ {
		want := brightnessRawForLevel(b, step*brightnessFullPct/trimuiBrightnessSteps, max)
		d := want - raw
		if d < 0 {
			d = -d
		}
		if bestD < 0 || d < bestD {
			best, bestD = step, d
		}
	}
	return best * brightnessFullPct / trimuiBrightnessSteps
}

// brightnessLearnFirmwareStep records the raw level the panel showed while
// the firmware's file said step (see brightnessFirmwareCurve).
func brightnessLearnFirmwareStep(step, raw int) {
	if step < 0 || step > trimuiBrightnessSteps || raw <= 0 {
		return
	}
	brightnessFirmwareCurve[step].Store(int32(raw))
}

// ── The cache ──────────────────────────────────────────────────────────────

// brightnessCurrent returns the cached level and starts a background reading
// when one is due. It is cheap: the sun calls it every frame. Main thread.
func brightnessCurrent() (int, bool) {
	b := brightnessCachedBackend()
	now := time.Now()
	brightnessLevel.mu.Lock()
	defer brightnessLevel.mu.Unlock()
	interval, polls := brightnessPollInterval(b, brightnessPopoverOpen || qs.open)
	if polls && !brightnessLevel.polling && !brightnessBorrowed.Load() &&
		(brightnessLevel.pollAt.IsZero() || now.Sub(brightnessLevel.pollAt) >= interval) {
		brightnessLevel.polling = true
		brightnessLevel.pollAt = now
		go brightnessPoll(b, brightnessLevel.editGen, brightnessLevel.appliedGen)
	}
	return brightnessLevel.pct, brightnessLevel.known
}

// brightnessPollInterval is how often b is read, with the card (or Quick
// Settings) open or not; polls=false means not on a timer at all. Pure.
func brightnessPollInterval(b string, open bool) (interval time.Duration, polls bool) {
	switch b {
	case "":
		return 0, false
	case brightnessBackendWMI, brightnessBackendDDC:
		if !open {
			return 0, false
		}
		return brightnessPollSlowOpen, true
	}
	if open {
		return brightnessPollOpen, true
	}
	return brightnessPollIdle, true
}

// brightnessFrameTick is the controller's per-frame work on the main thread;
// the main loop calls it once a frame, just before the software dimmer. It
// notes the window for DDC/CI and starts the probe; closes a header card that
// a full-screen surface now covers (a video, the image viewer, a ringing
// alarm, Quick Settings), which would otherwise keep the pad while invisible;
// applies a saved level waiting for the probe; keeps Quick Settings' slider
// (and the Settings row built on it) on the cached level; and on a TrimUI
// looks at the firmware's settings file once a second, so its brightness keys
// (START + L/R, which JukaHub never sees as a brightness key) are read back at
// once instead of on the next poll.
func brightnessFrameTick() {
	brightnessNoteWindow()
	b := brightnessCachedBackend()
	if (brightnessPopoverOpen || volumePopoverOpen) && headerCardsCovered() {
		dismissBrightnessPopover()
		dismissVolumePopover()
	}
	if b == "" {
		return
	}
	brightnessMainTick(b)
	brightnessLevel.mu.Lock()
	pct, known := brightnessLevel.pct, brightnessLevel.known
	brightnessLevel.mu.Unlock()
	if known {
		qsNoteBrightnessLevel(pct)
	}
	if brightnessFirmwareSync(b) {
		brightnessWatchFirmwareFile(time.Now())
	}
}

// headerCardsCovered reports whether a surface drawn over the header's cards
// owns the input: Quick Settings, the QR card, a ringing alarm, the search
// sheets, the image viewer, the recovery card, the on-screen keyboard, or a
// video playing full screen (a video playing in its scene leaves the header,
// and changing the volume or the brightness under it, alone). It names them
// one by one on purpose: the shared lists (volumeChromeCovered and the
// shoulder blockers it reaches) count the cards themselves, so a card would
// always be "covered" by itself and close the frame it opened. Main thread.
func headerCardsCovered() bool {
	return qs.open || qrZoom.open || alarmFiringActive() || globalSearchOpen || clipSearchOpen ||
		imageViewerPath != "" || recoveryCard.open || virtualKeyboardActive ||
		(videoFullscreen && playerPhase() != "idle")
}

// brightnessFirmwareFile is the firmware settings file as last looked at by
// brightnessWatchFirmwareFile. Main thread only.
var brightnessFirmwareFile struct {
	checkedAt time.Time
	mod       time.Time
	seen      bool
}

// brightnessWatchFirmwareFile reads the level again when the firmware's file
// changed since the last look (a stat a second). A change JukaHub made itself
// reads back its own level, which is harmless.
func brightnessWatchFirmwareFile(now time.Time) {
	w := &brightnessFirmwareFile
	if now.Sub(w.checkedAt) < brightnessFirmwareFileCheck {
		return
	}
	w.checkedAt = now
	mod, ok := trimuiSystemJSONModTime()
	if !ok {
		return
	}
	if w.seen && !mod.Equal(w.mod) {
		brightnessRefreshNow()
	}
	w.mod, w.seen = mod, true
}

// brightnessMainTick does the main-thread work that waits for the probe: the
// saved level is applied once the backend is known, and Quick Settings' row
// takes the answer (it is built before the window exists, when nothing has
// been probed yet).
func brightnessMainTick(b string) {
	if b == "" {
		return
	}
	if v := brightnessPendingRestore.Swap(0); v > 0 {
		brightnessRestoreFor(b, int(v)-1)
	}
	if !brightnessQSSynced {
		brightnessQSSynced = true
		qsRefreshBrightnessRow()
	}
}

// brightnessNoteSavedLevel queues the level saved in jukauser.json; it is
// applied once the backend is known, and only where JukaHub owns the level
// (brightnessRestoreFor). qsInit calls it at launch.
func brightnessNoteSavedLevel(pct int) {
	brightnessPendingRestore.Store(int32(brightnessClamp(pct)) + 1)
}

// brightnessRestoreFor applies a saved level where JukaHub is the level's only
// owner (brightnessOwnedByJukaHub), unless the level was already changed this
// session. Main thread. The software dimmer takes it at once, so not a single
// frame more is drawn undimmed.
func brightnessRestoreFor(b string, pct int) {
	if !brightnessOwnedByJukaHub(b) {
		return
	}
	brightnessLevel.mu.Lock()
	edited := brightnessLevel.editGen > 0
	brightnessLevel.mu.Unlock()
	if edited {
		return
	}
	applied := brightnessApplyLevel(pct)
	if b == brightnessBackendSoft {
		brightnessSoftDim.Store(int32(brightnessFullPct - applied))
	}
	qsNoteBrightnessLevel(applied)
	logDebug("brightness", "restored the saved level %d%%", applied)
}

// brightnessRefreshNow reads the level again without waiting for the next
// poll: the card opening, or a hardware key that just changed it. Safe from
// any goroutine.
func brightnessRefreshNow() {
	b := brightnessKnownBackend()
	if b == "" || brightnessBorrowed.Load() {
		return
	}
	brightnessLevel.mu.Lock()
	if brightnessLevel.polling {
		brightnessLevel.rerun = true
		brightnessLevel.mu.Unlock()
		return
	}
	brightnessLevel.polling = true
	brightnessLevel.pollAt = time.Now()
	gen, applied := brightnessLevel.editGen, brightnessLevel.appliedGen
	brightnessLevel.mu.Unlock()
	go brightnessPoll(b, gen, applied)
}

// brightnessNoteHardwareKey is called by the hardware key watcher
// (volumebuttons.go): a firmware brightness chord that holds a volume key is
// applied by the firmware itself, so the level is read back a moment later
// instead of on the next poll. Only the cheap backends are read for it.
func brightnessNoteHardwareKey() {
	switch brightnessKnownBackend() {
	case brightnessBackendDisp, brightnessBackendSysfs:
		time.AfterFunc(brightnessHardwareKeyDelay, brightnessRefreshNow)
	}
}

// brightnessPoll reads the level once, off the render thread. gen and applied
// are the change counters when the reading began.
func brightnessPoll(b string, gen, applied int) {
	r := brightnessRead(b)
	brightnessLevel.mu.Lock()
	defer brightnessLevel.mu.Unlock()
	brightnessLevel.polling = false
	if r.ok && !brightnessBorrowed.Load() {
		brightnessNoteReadingLocked(b, r, gen, applied)
	}
	if brightnessLevel.rerun {
		brightnessLevel.rerun = false
		brightnessLevel.polling = true
		brightnessLevel.pollAt = time.Now()
		go brightnessPoll(b, brightnessLevel.editGen, brightnessLevel.appliedGen)
	}
}

// brightnessNoteReadingLocked keeps a reading only when no change was asked
// for since it began and the newest change had been written when it began —
// and only such a reading teaches the firmware's curve: the panel showed the
// firmware's level, not one of JukaHub's on its way. Even then a pair is
// learned only once two readings in a row agree on it, so a reading taken
// while the firmware was between applying the panel and saving its file
// cannot pair a step with its neighbour's level. The caller holds
// brightnessLevel.mu.
func brightnessNoteReadingLocked(b string, r brightnessReading, gen, applied int) {
	if !volumeReadingCurrent(brightnessLevel.editGen, gen, applied) {
		return
	}
	pct := brightnessShownLevel(b, r.pct)
	brightnessLevel.pct = pct
	brightnessLevel.known = true
	if pct > brightnessFloorFor(b) {
		brightnessLevel.restore = pct
	}
	if r.step >= 0 && r.raw > 0 && !brightnessIsOwnRaw(r.raw) {
		if r.step == brightnessLevel.seenStep && r.raw == brightnessLevel.seenRaw {
			brightnessLearnFirmwareStep(r.step, r.raw)
		}
		brightnessLevel.seenStep, brightnessLevel.seenRaw = r.step, r.raw
	}
}

// brightnessIsOwnRaw reports whether raw is the level JukaHub wrote last, or
// one off it (see brightnessLastRaw).
func brightnessIsOwnRaw(raw int) bool {
	own := int(brightnessLastRaw.Load())
	return own > 0 && raw >= own-1 && raw <= own+1
}

// brightnessApplyLevel moves the cached level at once and hands the write to
// the worker; on a TrimUI it also schedules the firmware's file, here rather
// than on the worker, so a borrow that replaces the request in the worker's
// mailbox cannot lose it. It returns the level actually applied: snapped to the
// backend's grid and kept at or above its floor. Main thread (the worker's
// mailbox has one sender).
func brightnessApplyLevel(pct int) int {
	b := brightnessKnownBackend()
	pct = brightnessNormalize(b, pct)
	brightnessLevel.mu.Lock()
	brightnessLevel.pct = pct
	brightnessLevel.known = true
	brightnessLevel.editGen++
	gen := brightnessLevel.editGen
	if pct > brightnessFloorFor(b) {
		brightnessLevel.restore = pct
	}
	brightnessLevel.mu.Unlock()
	brightnessRequestLevel(pct, gen)
	if brightnessFirmwareSync(b) {
		trimuiBrightnessSyncLater(pct)
	}
	return pct
}

// brightnessSetLevel is a change from the card: applied through the worker,
// shown by the Quick Settings slider and remembered in jukauser.json like a
// Quick Settings change. Main thread. It returns the level set.
func brightnessSetLevel(config *Config, pct int) int {
	pct = brightnessApplyLevel(pct)
	qsNoteBrightnessLevel(pct)
	qsNoteEdit()
	brightnessPersist(config, pct)
	return pct
}

// brightnessStepBy moves the level by delta from where it is. A step that
// would not change the level (already at the floor or the top) writes
// nothing: a monitor stores every DDC/CI write.
func brightnessStepBy(config *Config, delta int) (before, after int) {
	before = brightnessBase()
	target := brightnessStepTarget(brightnessKnownBackend(), before, delta)
	if target == before {
		return before, before
	}
	return before, brightnessSetLevel(config, target)
}

// brightnessToggleDim takes the screen to its floor, or back to the last
// level above it — the card's A, the way A mutes on the volume card.
func brightnessToggleDim(config *Config) int {
	floor := brightnessFloor()
	brightnessLevel.mu.Lock()
	pct, known, restore := brightnessLevel.pct, brightnessLevel.known, brightnessLevel.restore
	brightnessLevel.mu.Unlock()
	if !known {
		pct = brightnessBase()
	}
	if pct > floor {
		return brightnessSetLevel(config, floor)
	}
	if restore <= floor {
		restore = brightnessUndimDefault
	}
	return brightnessSetLevel(config, restore)
}

// brightnessDimmed reports whether the level is at its floor.
func brightnessDimmed(pct int, known bool) bool {
	return known && pct <= brightnessFloor()
}

// brightnessBase is the level a step starts from: the cache once it is known,
// before that the Quick Settings slider (the saved level), else full.
func brightnessBase() int {
	brightnessLevel.mu.Lock()
	pct, known := brightnessLevel.pct, brightnessLevel.known
	brightnessLevel.mu.Unlock()
	if known {
		return pct
	}
	if len(qs.sliders) > 1 && qs.sliders[1].Action == "brightness" {
		return brightnessClamp(int(qs.sliders[1].Value*100 + 0.5))
	}
	return brightnessFullPct
}

// brightnessPersist stores the level the way Quick Settings does: in the
// cached user config (Brightness + BrightnessSet, which
// userConfigFromVariables carries through every save), then a debounced save.
func brightnessPersist(config *Config, pct int) {
	if config == nil {
		return
	}
	if cache := loadUserConfig(); cache != nil {
		cache.Variables.Brightness = pct
		cache.Variables.BrightnessSet = true
	}
	if user := userConfigFromVariables(config); user != nil {
		saveUserConfigDebounced(user)
	}
}

// ── Borrowing (the QR card) ─────────────────────────────────────────────────

// brightnessBorrow takes the screen to pct for a moment — the QR card's full
// brightness, which a phone camera needs — without making it the user's
// level: nothing is saved or written to the firmware's file, and the cache
// keeps the user's level. It reports whether anything was borrowed: not when
// the level is unknown or already at least pct, and then brightnessGiveBack
// has nothing to undo. Main thread.
func brightnessBorrow(pct int) bool {
	if brightnessKnownBackend() == "" {
		return false
	}
	pct = brightnessClamp(pct)
	brightnessLevel.mu.Lock()
	cur, known := brightnessLevel.pct, brightnessLevel.known
	if !known || cur >= pct {
		brightnessLevel.mu.Unlock()
		return false
	}
	brightnessLevel.editGen++
	gen := brightnessLevel.editGen
	brightnessLevel.mu.Unlock()
	brightnessBorrowed.Store(true)
	brightnessRequestLevel(pct, gen)
	return true
}

// brightnessGiveBack returns the screen to the user's level after a borrow —
// on a TrimUI the firmware's own raw level for it (brightnessRawForLevel). A
// no-op without a borrow, so every way the card closes can call it. Main
// thread.
func brightnessGiveBack() {
	if !brightnessBorrowed.Swap(false) {
		return
	}
	brightnessLevel.mu.Lock()
	pct := brightnessLevel.pct
	brightnessLevel.editGen++
	gen := brightnessLevel.editGen
	brightnessLevel.mu.Unlock()
	brightnessRequestLevel(pct, gen)
}

func init() {
	// Quitting with the QR card up, or right after a change the worker has not
	// written yet, must not leave the handheld at the wrong brightness after
	// JukaHub is gone: the exit path writes the level itself.
	Cleanup = append(Cleanup, brightnessGiveBackNow)
}

// brightnessGiveBackNow writes the user's level here and now on the exit
// path, when the QR card still holds a borrow or a change has not been
// written yet: the worker may not get to run again.
func brightnessGiveBackNow() {
	borrowed := brightnessBorrowed.Swap(false)
	b := brightnessKnownBackend()
	brightnessLevel.mu.Lock()
	pct, known := brightnessLevel.pct, brightnessLevel.known
	pending := brightnessLevel.appliedGen < brightnessLevel.editGen
	brightnessLevel.mu.Unlock()
	if (!borrowed && !pending) || b == "" || !known {
		return
	}
	if err := setScreenBrightnessVia(b, pct); err != nil {
		logDebug("brightness", "could not write the level on exit: %v", err)
	}
}

// ── The worker ─────────────────────────────────────────────────────────────

// brightnessRequest is one level for the worker, with the change it belongs
// to.
type brightnessRequest struct {
	pct int
	gen int
}

// brightnessWork is the one goroutine that writes the hardware, and its
// one-slot mailbox: a new level replaces one still waiting, so only the
// newest is ever applied.
var brightnessWork struct {
	once sync.Once
	ch   chan brightnessRequest
}

// brightnessRequestLevel asks the worker to apply pct for change gen. It never
// blocks. Main thread only: draining and refilling the one-slot mailbox is only
// safe with a single sender.
func brightnessRequestLevel(pct, gen int) {
	brightnessWork.once.Do(func() {
		brightnessWork.ch = make(chan brightnessRequest, 1)
		go brightnessWorker(brightnessWork.ch)
	})
	select {
	case <-brightnessWork.ch:
	default:
	}
	select {
	case brightnessWork.ch <- brightnessRequest{pct: pct, gen: gen}:
	default:
	}
}

// brightnessWorker applies levels as they arrive and records each change as
// written once its write returns — a failed one too, so the next poll shows
// the level the hardware really has. A failure is reported once in a while,
// not once per step of a held D-pad.
func brightnessWorker(ch <-chan brightnessRequest) {
	var lastToast time.Time
	for req := range ch {
		err := setScreenBrightnessVia(brightnessKnownBackend(), req.pct)
		brightnessLevel.mu.Lock()
		if req.gen > brightnessLevel.appliedGen {
			brightnessLevel.appliedGen = req.gen
		}
		brightnessLevel.mu.Unlock()
		if err == nil {
			continue
		}
		logWarn("brightness", "setting the brightness to %d%% failed: %v", req.pct, err)
		if time.Since(lastToast) >= 10*time.Second {
			lastToast = time.Now()
			ShowToast("Brightness: "+firstLine(err.Error()), ToastKindWarning)
		}
	}
}

// ── The software dimmer ─────────────────────────────────────────────────────

// brightnessSoftRender dims the finished frame when the software dimmer is the
// backend and below full. The main loop calls it last, just before Present, so
// every scene, overlay and toast is dimmed alike — the way a backlight would.
func brightnessSoftRender(renderer *sdl.Renderer) {
	dim := brightnessSoftDim.Load()
	if dim <= 0 || renderer == nil {
		return
	}
	// A clip left set by an overlay must not cut the dim short.
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetDrawColor(0, 0, 0, brightnessSoftAlpha(brightnessFullPct-int(dim)))
	_ = renderer.FillRect(nil)
}

// ── Pure helpers ───────────────────────────────────────────────────────────

// brightnessClamp keeps a level inside 0..100.
func brightnessClamp(pct int) int {
	if pct < 0 {
		return 0
	}
	if pct > brightnessFullPct {
		return brightnessFullPct
	}
	return pct
}

// brightnessSnapTo rounds a level to the nearest multiple of grid, clamped.
func brightnessSnapTo(pct, grid int) int {
	pct = brightnessClamp(pct)
	if grid <= 1 {
		return pct
	}
	return brightnessClamp((pct + grid/2) / grid * grid)
}

// brightnessNormalize is a level JukaHub sets on b: on b's grid, and never
// under its floor.
func brightnessNormalize(b string, pct int) int {
	pct = brightnessSnapTo(pct, brightnessGridFor(b))
	if floor := brightnessFloorFor(b); pct < floor {
		pct = floor
	}
	return pct
}

// brightnessShownLevel is a reading as the card shows it: on b's grid, so a
// raw level a hair off the one written reads back as that level, and at
// least the floor, the lowest level the card offers.
func brightnessShownLevel(b string, pct int) int {
	return brightnessNormalize(b, pct)
}

// brightnessStepTarget is where a step of delta from current lands on b: at
// least one grid step in delta's direction, so a step finer than the grid
// (Quick Settings moves its sliders by 5%, the TrimUI's grid is 10%) still
// moves the level instead of rounding back to where it was. Pure.
func brightnessStepTarget(b string, current, delta int) int {
	from := brightnessNormalize(b, current)
	target := brightnessNormalize(b, current+delta)
	if target == from && delta != 0 {
		step := brightnessGridFor(b)
		if delta < 0 {
			step = -step
		}
		target = brightnessNormalize(b, from+step)
	}
	return target
}

// brightnessSoftAlpha is the software dimmer's black layer for a level: none
// at full, about three quarters black at the 5% floor — dim, still readable.
func brightnessSoftAlpha(pct int) uint8 {
	pct = brightnessClamp(pct)
	return uint8((brightnessFullPct - pct) * 205 / brightnessFullPct)
}

// brightnessGlyph names the sun for a level: the small sun below half, the
// full one from half up, and the full one before the first reading.
func brightnessGlyph(pct int, known bool) string {
	if known && pct < 50 {
		return "sun-low"
	}
	return "sun"
}

// parseWMIBrightness reads what the WMI reading printed (brightness_windows.go):
// the last line that is "<level>" or "<level> <number of levels>", the level
// 0..100; lines around it that are not (a warning PowerShell merged into the
// output) are passed over. levels is 0 when it is not given. It lives here,
// outside the Windows file, so its test runs on every host. Pure.
func parseWMIBrightness(out string) (pct, levels int, ok bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		fields := strings.Fields(lines[i])
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.Atoi(fields[0])
		if err != nil || v < 0 || v > brightnessFullPct {
			continue
		}
		if len(fields) > 1 {
			if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
				levels = n
			}
		}
		return v, levels, true
	}
	return 0, 0, false
}

// brightnessWMIGridFor is the grid a panel with this many levels allows: one
// with eleven or fewer moves in tenths, so a 5% step would be a press that
// does nothing; 0 (unknown) keeps the usual grid. Pure.
func brightnessWMIGridFor(levels int) int {
	if levels >= 2 && levels <= 11 {
		return brightnessStepFirmware
	}
	return 0
}

// ddcRawFor and ddcPercentFor convert between a percentage and a monitor's
// own DDC/CI range lo..hi (usually 0..100), rounding. Pure, for the same
// reason as parseWMIBrightness.
func ddcRawFor(pct, lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + (brightnessClamp(pct)*(hi-lo)+50)/100
}

func ddcPercentFor(raw, lo, hi int) int {
	if hi <= lo {
		return 0
	}
	if raw < lo {
		raw = lo
	}
	if raw > hi {
		raw = hi
	}
	return brightnessClamp(((raw-lo)*100 + (hi-lo)/2) / (hi - lo))
}
