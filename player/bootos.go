package main

// bootos.go — the boot itself: what a power-on looks like, and the quick hello
// an app launch gets.
//
// JukaHub used to greet every start with the same glass card, squeezed into
// whatever was left of a two-second budget. On a warm device that was well
// under a second, and a tester asked where the start animation had gone. The
// card was the right length for opening an app and far too short for an
// operating system, so the two starts are now told apart:
//
//   - A power-on (the device booted straight into JukaHub, or the user asked
//     for the full boot every time) plays the boot of an OS. The JukaHub logo
//     the firmware showed (bootlogo.go writes it, bootlogo_layout.go places
//     it) stays on the black screen; a light runs once round its glass pane;
//     the backdrop's four blooms ignite with the startup chime's four bells
//     and drift out from behind the logo while the theme's colours dawn; the
//     wordmark catches a glint; a quiet progress line reports the real boot
//     queue; and the first screen arrives card by card (bootarrive.go).
//   - An app launch keeps the budget: the same logo, light and blooms,
//     compressed into the few hundred milliseconds the budget allows, and a
//     quicker arrival.
//
// The power-on is detected, not configured: JUKAHUB_BOOT=1 or --boot from a
// boot hook, the TrimUI autostart hook's marker, or — where the hook cannot
// pass anything through the firmware's launcher — a kernel uptime short enough
// to be this boot, with "Start JukaHub at login" on and no JukaHub start
// recorded since the boot began. Settings → General → Startup animation can
// ask for the full boot every time, or never.
//
// The boot still pays for itself: the time a frame does not spend drawing runs
// the queued startup work (bootwork.go), as the old card did, and the hold
// ends as soon as the queue has settled once the beats have played. Reduced
// motion keeps every beat as a fade: no light travels, nothing drifts, scales
// or flies — a Reduce motion chosen by hand, or a battery saver, not the
// handheld's out-of-box default (bootOSMotion).

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// bootOSMode is how this start is greeted.
type bootOSMode uint8

const (
	// bootOSModeNone draws no boot at all: a warm start, or no budget left.
	bootOSModeNone bootOSMode = iota
	// bootOSModeLaunch is the quick hello of a start from a launcher.
	bootOSModeLaunch
	// bootOSModePowerOn is the full boot of a start with the device.
	bootOSModePowerOn
)

const (
	// bootOSAnimationKey is the Custom setting behind Settings → General →
	// Startup animation.
	bootOSAnimationKey = "startupAnimation"
	// Its values: the full boot at power-on only (the default), on every
	// start, or never.
	bootOSAnimationPower  = "power"
	bootOSAnimationAlways = "always"
	bootOSAnimationQuick  = "quick"

	// bootOSPowerOnWindow is how long after the kernel started a TrimUI start
	// still counts as the boot itself: the autostart hook waits for the stock
	// launcher (up to two minutes on a slow card) before it hands over.
	bootOSPowerOnWindow = 150 * time.Second
	// bootOSPowerOnAllotment is the longest the full boot waits for the boot
	// queue before the first screen arrives anyway; what is left drains after.
	bootOSPowerOnAllotment = 9 * time.Second
	// bootOSPowerOnMinMS holds the full boot until every beat has played.
	bootOSPowerOnMinMS = 2700.0
	// bootOSChimeDelay pushes the power-on chime's score back, in seconds, so
	// its four bells land on the four blooms (bootOSPowerBeats.bells).
	bootOSChimeDelay = 0.40
	// bootOSAutostartMarker is the file the TrimUI autostart hook leaves next
	// to the executable while it hands the device to JukaHub.
	bootOSAutostartMarker = ".autostart-pending"
	// bootOSHookRanPath is what the autostart hook's queued command creates
	// (with mkdir) the moment it starts JukaHub, once per boot: /tmp is empty
	// at every boot, so it says the hook started JukaHub during this one.
	bootOSHookRanPath = "/tmp/jukahub-autostart.ran"
	// bootOSBootIDPath is the kernel's random id for this boot.
	bootOSBootIDPath = "/proc/sys/kernel/random/boot_id"
	// bootOSLaunchMinMS is the shortest quick hello: the logo up and the
	// wordmark on its way into focus.
	bootOSLaunchMinMS = 380.0
	// bootOSLaunchOverlapMS starts a launch's arrival this long before its
	// budget runs out, so the arrival costs about what the old cross-fade did.
	bootOSLaunchOverlapMS = 200.0
	// bootOSStatusHoldMS keeps each status label up at least this long: the
	// queue's steps take milliseconds, and a label per frame is a flicker.
	bootOSStatusHoldMS = 220.0
	// bootOSPreviewSettleMS is when the preview's simulated queue finishes.
	bootOSPreviewSettleMS = 2600.0
	// bootOSCometTrail is how many soft dots the running light is made of.
	bootOSCometTrail = 9
	// bootOSDotSize is the side of the soft round light texture.
	bootOSDotSize = 64
)

// bootOS is the decision made for this start: main.go reads it to leave the
// chime to the full boot, the splash to pick its beats, and reason says what
// made it a power-on (one of the bootOSReason values).
var bootOS struct {
	mode   bootOSMode
	reason string
	// running is set while a boot or its preview owns the screen.
	running bool
}

// The reasons a start is a power-on, as bootOSDecide reports and the log
// prints them.
const (
	bootOSReasonEnv    = "JUKAHUB_BOOT"
	bootOSReasonArg    = "--boot"
	bootOSReasonAlways = "full boot on every start"
	bootOSReasonMarker = "autostart hook"
	bootOSReasonUptime = "started with the device"
)

// bootOSContinuityReason reports whether a power-on decided for reason comes
// straight after the firmware's own boot logo: a start by a boot hook (the
// TrimUI hook's marker or its queued command, or a hook of one's own that
// passes JUKAHUB_BOOT or --boot). "Full every time" plays on starts from the
// menu too, where the menu, not the logo, was on screen.
func bootOSContinuityReason(reason string) bool {
	switch reason {
	case bootOSReasonEnv, bootOSReasonArg, bootOSReasonMarker, bootOSReasonUptime:
		return true
	}
	return false
}

// The logo's own light: its rim runs from cyan at the top left to violet at
// the bottom right (branding/mklogo.py CYAN_LIGHT, VIOLET_LIGHT), and its
// second bloom is the backdrop's violet.
var (
	bootOSRimCyan     = sdl.Color{R: 85, G: 214, B: 255, A: 255}
	bootOSRimViolet   = sdl.Color{R: 139, G: 124, B: 255, A: 255}
	bootOSBloomViolet = sdl.Color{R: 124, G: 58, B: 237, A: 255}
	bootOSStatusInk   = sdl.Color{R: 150, G: 158, B: 178, A: 255}
)

// bootOSBlobBell is the bell that lights each backdrop bloom (bootBlobs): the
// bells climb left to right across the stereo field, so the blooms ignite in
// the order they sit across the screen — lilac, cyan, blue, violet.
var bootOSBlobBell = [4]int{1, 3, 2, 0}

// Scratch destinations for the per-frame draws, so passing their address to
// SDL never allocates (as in bootliquid.go).
var (
	bootOSF     sdl.FRect
	bootArriveR sdl.Rect
	bootArriveF sdl.FRect
)

// ── Deciding what this start is ─────────────────────────────────────────

// bootOSInputs is everything the power-on decision reads, gathered in one
// place so that the decision itself is a pure function a test can drive.
// HookRan is the autostart hook's once-per-boot claim (bootOSHookRanPath);
// BootID is this boot's kernel id and LastBootID the one recorded with the
// last first frame (empty where the kernel has none, a desktop).
type bootOSInputs struct {
	Setting    string
	Env        string
	Args       []string
	Bench      bool
	Marker     bool
	HookRan    bool
	TrimUI     bool
	Uptime     time.Duration
	UptimeOK   bool
	AutoStart  bool
	LastLaunch time.Time
	BootID     string
	LastBootID string
	Now        time.Time
}

// bootOSDecide reports whether this start is a power-on, and why. The user's
// "Always quick" wins over everything; an explicit request from a boot hook
// comes next; the startup bench measures the ordinary start; "Full every time"
// makes every start one; the autostart hook's marker is the hook saying so
// itself; and on a TrimUI, whose hook cannot pass an argument through the
// firmware's launcher, a start this soon after the kernel came up, with
// autostart on and the hook's once-per-boot claim made, is the boot (a start
// from the menu after the hook stood aside makes no claim). None of the
// signals counts for a relaunch — JukaHub restarting itself (an update, a
// reset, a crash respawn) inherits the environment, the arguments and, for a
// few seconds, the hook's marker, and replaying the whole boot then would be
// a stutter, not a greeting.
func bootOSDecide(in bootOSInputs) (bool, string) {
	relaunch := bootOSRelaunch(in)
	switch {
	case in.Setting == bootOSAnimationQuick:
		return false, ""
	case !relaunch && bootOSTruthy(in.Env):
		return true, bootOSReasonEnv
	case !relaunch && bootOSHasBootArg(in.Args):
		return true, bootOSReasonArg
	case in.Bench:
		return false, ""
	case in.Setting == bootOSAnimationAlways:
		return true, bootOSReasonAlways
	case relaunch:
		return false, ""
	case in.Marker:
		return true, bootOSReasonMarker
	case in.TrimUI && in.UptimeOK && in.Uptime < bootOSPowerOnWindow && in.AutoStart && in.HookRan:
		return true, bootOSReasonUptime
	}
	return false, ""
}

// bootOSRelaunchWindow is how recent a recorded start makes this one a relaunch
// where the kernel's uptime is unknown (a desktop).
const bootOSRelaunchWindow = 2 * time.Minute

// bootOSRelaunch reports whether JukaHub already reached its first frame since
// the machine booted. The kernel's boot id answers it outright where both ends
// have one: the clock cannot fool it (a handheld that boots with an old clock
// and is set right by the network after its first start would otherwise look
// as if that start came before the boot). Without ids: with a known uptime, a
// recorded start inside it; without one, a start in the last two minutes. A
// start recorded "in the future" is from before a clock that was set back, so
// it is not this boot's.
func bootOSRelaunch(in bootOSInputs) bool {
	if in.BootID != "" && in.LastBootID != "" {
		return in.BootID == in.LastBootID
	}
	if in.LastLaunch.IsZero() || in.LastLaunch.After(in.Now) {
		return false
	}
	if in.UptimeOK {
		return !bootOSLaunchPredates(in.LastLaunch, in.Now, in.Uptime)
	}
	return in.Now.Sub(in.LastLaunch) < bootOSRelaunchWindow
}

// bootOSTruthy reads an environment switch.
func bootOSTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// bootOSHasBootArg reports whether the command line asks for the full boot.
func bootOSHasBootArg(args []string) bool {
	for _, a := range args {
		if bootOSIsBootArg(a) {
			return true
		}
	}
	return false
}

// bootOSIsBootArg reports whether a is the full-boot request.
func bootOSIsBootArg(a string) bool { return a == "--boot" || a == "-boot" }

// bootOSRestartArgs is args without the full-boot request, for JukaHub
// starting itself again (recoverysettings.go recoverySpawn): a restart is not
// a power-on.
func bootOSRestartArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if !bootOSIsBootArg(a) {
			out = append(out, a)
		}
	}
	return out
}

// bootOSRestartEnv is env (KEY=value entries) without JUKAHUB_BOOT, for the
// same restart.
func bootOSRestartEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if key, _, _ := strings.Cut(kv, "="); strings.EqualFold(key, "JUKAHUB_BOOT") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// bootOSLaunchPredates reports whether a JukaHub start recorded at last
// happened before a boot that began uptime ago. A start "in the future" did:
// the clock was set back since (a handheld without network time boots with an
// old clock), and no start of this boot can postdate now on the same clock.
func bootOSLaunchPredates(last, now time.Time, uptime time.Duration) bool {
	if last.IsZero() || last.After(now) {
		return true
	}
	return last.Before(now.Add(-uptime))
}

// launchPredatesBoot reports whether a launch recorded at last, during the
// boot with id lastBootID (empty when it was not recorded), happened before
// the machine's current boot: by the boot ids where both are known, else by
// the uptime. Where neither answers it says no, which keeps the old
// warm-start rule (startup.go): a reboot never leaves the card's caches warm,
// so a launch from before one must not skip the greeting.
func launchPredatesBoot(last time.Time, lastBootID string, now time.Time) bool {
	if cur := bootOSBootIDFn(); cur != "" && lastBootID != "" {
		return cur != lastBootID
	}
	up, ok := bootOSUptimeFn()
	if !ok || last.IsZero() {
		return false
	}
	return bootOSLaunchPredates(last, now, up)
}

// bootOSUptimeFn reads the kernel's uptime and bootOSBootIDFn this boot's id;
// tests replace them.
var (
	bootOSUptimeFn = bootOSReadUptime
	bootOSBootIDFn = bootOSReadBootID
)

// bootOSReadBootID is the kernel's random id for this boot, "" where there is
// none (a desktop that is not Linux).
func bootOSReadBootID() string {
	data, err := os.ReadFile(bootOSBootIDPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// bootOSHookRan reports whether the autostart hook's queued command has
// started JukaHub during this boot.
func bootOSHookRan() bool {
	_, err := os.Stat(bootOSHookRanPath)
	return err == nil
}

// bootOSReadUptime is the kernel's uptime from /proc/uptime (which counts time
// asleep too). ok is false where there is no such file.
func bootOSReadUptime() (time.Duration, bool) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	return bootOSParseUptime(string(data))
}

// bootOSParseUptime reads the first field of /proc/uptime: seconds, with a
// fraction.
func bootOSParseUptime(s string) (time.Duration, bool) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs < 0 || math.IsNaN(secs) || math.IsInf(secs, 0) {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// bootOSMarkerPresent reports whether the autostart hook's marker sits next to
// the executable.
func bootOSMarkerPresent() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(filepath.Dir(exe), bootOSAutostartMarker))
	return err == nil
}

// bootOSLiveInputs gathers the decision's inputs for this process.
func bootOSLiveInputs(config *Config, now time.Time) bootOSInputs {
	args := os.Args
	if len(args) > 0 {
		args = args[1:]
	}
	in := bootOSInputs{
		Setting: bootOSAnimationSetting(config),
		Env:     os.Getenv("JUKAHUB_BOOT"),
		Args:    args,
		Bench:   startupBenchEnabled(),
		Marker:  bootOSMarkerPresent(),
		HookRan: bootOSHookRan(),
		TrimUI:  IsTrimuiDevice(),
		BootID:  bootOSBootIDFn(),
		Now:     now,
	}
	in.Uptime, in.UptimeOK = bootOSUptimeFn()
	in.AutoStart = autoStartEnabled(config)
	if state, ok := loadStartupState(startupStatePath()); ok {
		in.LastLaunch = state.LastLaunch
		in.LastBootID = state.BootID
	}
	return in
}

// bootOSPlanStartup decides how this start is greeted, once, before the boot
// queue is built (main.go). A power-on gets the full boot whatever the budget
// left — it is the point of booting into JukaHub, and the queue runs under it
// either way; any other start keeps the allotment the budget gave it.
func bootOSPlanStartup(config *Config, allotment time.Duration) time.Duration {
	powerOn, reason := bootOSDecide(bootOSLiveInputs(config, time.Now()))
	bootOS.reason = reason
	switch {
	case powerOn:
		bootOS.mode = bootOSModePowerOn
		logInfo("startup", "power-on boot (%s): full boot animation", reason)
		return bootOSPowerOnAllotment
	case allotment > 0:
		bootOS.mode = bootOSModeLaunch
	default:
		bootOS.mode = bootOSModeNone
	}
	return allotment
}

// bootOSPowerOnActive reports whether this start plays the full boot, which
// starts its own chime later in its score.
func bootOSPowerOnActive() bool { return bootOS.mode == bootOSModePowerOn }

// ── The setting ─────────────────────────────────────────────────────────

// bootOSAnimationSetting is the stored startup-animation choice.
func bootOSAnimationSetting(c *Config) string {
	if c != nil {
		if s, ok := customString(c.Variables.Custom, bootOSAnimationKey); ok {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case bootOSAnimationAlways:
				return bootOSAnimationAlways
			case bootOSAnimationQuick:
				return bootOSAnimationQuick
			}
		}
	}
	return bootOSAnimationPower
}

// bootOSAnimationOptions are the Settings row's stops.
func bootOSAnimationOptions(*Config) []setOption {
	return []setOption{
		{Value: bootOSAnimationPower, Label: "Full at power-on", Note: "The whole boot when the device starts into JukaHub; a quick hello otherwise"},
		{Value: bootOSAnimationAlways, Label: "Full every time", Note: "The whole boot on every start"},
		{Value: bootOSAnimationQuick, Label: "Always quick", Note: "Only the quick hello, even at power-on"},
	}
}

// bootOSAnimationCurrent is the stop the stored choice is.
func bootOSAnimationCurrent(c *Config) int {
	switch bootOSAnimationSetting(c) {
	case bootOSAnimationAlways:
		return 1
	case bootOSAnimationQuick:
		return 2
	}
	return 0
}

// bootOSAnimationChoose stores a stop and says so.
func bootOSAnimationChoose(c *Config, value string) {
	if c == nil {
		return
	}
	v := strings.ToLower(strings.TrimSpace(value))
	for _, o := range bootOSAnimationOptions(c) {
		if o.Value == v {
			setStoreString(c, bootOSAnimationKey, v)
			setToast("Startup animation: " + o.Label)
			return
		}
	}
}

// bootOSAnimationDetail is the line under the row.
func bootOSAnimationDetail(c *Config) string {
	switch bootOSAnimationSetting(c) {
	case bootOSAnimationAlways:
		return "The whole boot every time JukaHub starts"
	case bootOSAnimationQuick:
		return "A quick hello on every start, power-on included"
	}
	return "The whole boot when the device starts into JukaHub; a quick hello otherwise"
}

// ── The beats ───────────────────────────────────────────────────────────

// bootOSBeats times one kind of boot, in milliseconds from its first frame:
// the logo coming up (fadeIn; skipped when the firmware's own logo is already
// on screen), the wordmark coming into focus, one lap of light round the
// pane's rim, the halo swelling, the theme's backdrop dawning from black, the
// blooms drifting out from behind the logo, the glint, the progress line, and
// the chime's four bells, which light the four blooms.
type bootOSBeats struct {
	fadeIn     float64
	wordFrom   float64
	wordTo     float64
	cometFrom  float64
	cometMS    float64
	haloFrom   float64
	haloPeak   float64
	dawnFrom   float64
	dawnTo     float64
	emergeFrom float64
	emergeTo   float64
	glintFrom  float64
	glintMS    float64
	trackFrom  float64
	trackTo    float64
	bells      [4]float64
}

// bootOSPowerBeats are the full boot's. The bells are the start chime's
// (0.30–0.66 s) plus bootOSChimeDelay; the glint is its sparkle.
var bootOSPowerBeats = bootOSBeats{
	fadeIn:     420,
	wordFrom:   200,
	wordTo:     800,
	cometFrom:  450,
	cometMS:    1000,
	haloFrom:   400,
	haloPeak:   1400,
	dawnFrom:   550,
	dawnTo:     1750,
	emergeFrom: 700,
	emergeTo:   2000,
	glintFrom:  1240,
	glintMS:    600,
	trackFrom:  1500,
	trackTo:    1900,
	bells:      [4]float64{700, 820, 940, 1060},
}

// bootOSQuickBeats are the quick hello's: the same story inside the budget.
// main.go starts the chime with the first frame, so the bells are its own.
// The progress line only appears when a slow card keeps the hello up.
var bootOSQuickBeats = bootOSBeats{
	fadeIn:     200,
	wordFrom:   60,
	wordTo:     400,
	cometFrom:  120,
	cometMS:    500,
	haloFrom:   40,
	haloPeak:   500,
	dawnFrom:   0,
	dawnTo:     420,
	emergeFrom: 0,
	emergeTo:   650,
	glintFrom:  400,
	glintMS:    450,
	trackFrom:  900,
	trackTo:    1200,
	bells:      [4]float64{300, 420, 540, 660},
}

// bootOSFrame is one frame of the boot's own layer: how present each part is
// (0..1; a bloom brightens for a moment just after its bell strikes), where
// the running light is on its lap (comet, -1 when it is not running) and how
// far the blooms have drifted from the logo out to their orbits (emerge).
type bootOSFrame struct {
	logo      float64
	logoScale float64
	word      float64
	glint     float64
	comet     float64
	cometA    float64
	rim       float64
	halo      float64
	dawn      float64
	emerge    float64
	track     float64
	blob      [4]float64
}

// bootOSTimeline is the boot's layer at ms after its first frame. powerOn picks
// the full boot's beats over the quick hello's; without motion every beat
// stays, but nothing travels, drifts or scales; continuity starts with the logo
// and the wordmark already up, where the firmware's boot logo left them.
func bootOSTimeline(ms float64, powerOn, motion, continuity bool) bootOSFrame {
	b := &bootOSQuickBeats
	if powerOn {
		b = &bootOSPowerBeats
	}
	f := bootOSFrame{logo: 1, logoScale: 1, word: 1, comet: -1, emerge: 1}
	if !continuity {
		f.logo = bootSmoothstep(0, b.fadeIn, ms)
		f.word = bootClamp01((ms - b.wordFrom) / (b.wordTo - b.wordFrom))
		if motion {
			f.logoScale = 0.965 + 0.035*uiEaseOutCubic(ms/(b.fadeIn*1.4))
		}
	}
	if motion {
		if p := (ms - b.cometFrom) / b.cometMS; p >= 0 && p < 1 {
			f.comet = bootOSEaseInOut(p)
			f.cometA = bootSmoothstep(0, 0.15, p) * (1 - bootSmoothstep(0.8, 1, p))
		}
		if p := (ms - b.glintFrom) / b.glintMS; p > 0 && p < 1 {
			f.glint = p
		}
		f.emerge = uiEaseOutCubic((ms - b.emergeFrom) / (b.emergeTo - b.emergeFrom))
	}
	// The rim lights behind the running light, settles, then breathes.
	lapEnd := b.cometFrom + b.cometMS
	f.rim = 0.85*bootSmoothstep(b.cometFrom+0.3*b.cometMS, lapEnd, ms) - 0.40*bootSmoothstep(lapEnd, lapEnd+800, ms)
	if settled := lapEnd + 800; ms > settled {
		f.rim += 0.08 * math.Sin(2*math.Pi*(ms-settled)/2600) * bootSmoothstep(settled, settled+400, ms)
	}
	f.halo = bootSmoothstep(b.haloFrom, b.haloPeak, ms) - 0.35*bootSmoothstep(b.haloPeak, b.haloPeak+800, ms)
	f.dawn = bootSmoothstep(b.dawnFrom, b.dawnTo, ms)
	f.track = bootSmoothstep(b.trackFrom, b.trackTo, ms)
	for i := range f.blob {
		bell := b.bells[bootOSBlobBell[i]]
		flash := (ms - (bell + 180)) / 120
		f.blob[i] = bootSmoothstep(bell, bell+650, ms) + 0.22*math.Exp(-flash*flash)
	}
	return f
}

// bootOSEaseInOut is a cubic ease in and out over 0..1.
func bootOSEaseInOut(p float64) float64 {
	p = bootClamp01(p)
	if p < 0.5 {
		return 4 * p * p * p
	}
	q := -2*p + 2
	return 1 - q*q*q/2
}

// bootOSDelayedScore is score with every note pushed back by delay seconds:
// the full boot opens the chime's device with its first frame and lets the
// score's own silence carry the bells onto the blooms.
func bootOSDelayedScore(score []chimeNote, delay float64) []chimeNote {
	out := make([]chimeNote, len(score))
	copy(out, score)
	for i := range out {
		out[i].at += delay
	}
	return out
}

// bootOSPanePoint is the point a fraction u (wrapping) of the way round the
// rounded rect r with corner radius rad, starting where the top edge leaves
// the top-left corner and running clockwise: the path the boot's light takes
// round the logo's pane.
func bootOSPanePoint(r sdl.FRect, rad, u float64) (float64, float64) {
	x0, y0 := float64(r.X), float64(r.Y)
	w, h := float64(r.W), float64(r.H)
	rad = math.Max(0, math.Min(rad, math.Min(w, h)/2))
	sw, sh := w-2*rad, h-2*rad
	arc := math.Pi * rad / 2
	total := 2*sw + 2*sh + 4*arc
	if total <= 0 {
		return x0 + w/2, y0 + h/2
	}
	s := (u - math.Floor(u)) * total
	if s < sw {
		return x0 + rad + s, y0
	}
	s -= sw
	if s < arc {
		a := -math.Pi/2 + s/rad
		return x0 + w - rad + rad*math.Cos(a), y0 + rad + rad*math.Sin(a)
	}
	s -= arc
	if s < sh {
		return x0 + w, y0 + rad + s
	}
	s -= sh
	if s < arc {
		a := s / rad
		return x0 + w - rad + rad*math.Cos(a), y0 + h - rad + rad*math.Sin(a)
	}
	s -= arc
	if s < sw {
		return x0 + w - rad - s, y0 + h
	}
	s -= sw
	if s < arc {
		a := math.Pi/2 + s/rad
		return x0 + rad + rad*math.Cos(a), y0 + h - rad + rad*math.Sin(a)
	}
	s -= arc
	if s < sh {
		return x0, y0 + h - rad - s
	}
	if rad <= 0 {
		return x0, y0
	}
	a := math.Pi + (s-sh)/rad
	return x0 + rad + rad*math.Cos(a), y0 + rad + rad*math.Sin(a)
}

// bootOSRimColour is the logo's rim colour at a point of its pane.
func bootOSRimColour(pane sdl.FRect, x, y float64) sdl.Color {
	if pane.W <= 0 {
		return bootOSRimCyan
	}
	t := ((x - float64(pane.X)) + (y - float64(pane.Y))) / (2 * float64(pane.W))
	return glassMix(bootOSRimCyan, bootOSRimViolet, bootSmoothstep(0, 1, t))
}

// bootOSPreviewSteps are the rows the preview pretends to run, in boot order.
var bootOSPreviewSteps = []string{"settings", "storage", "tools", "cores", "sounds", "gamepad", "keyboard", "focus-graph", "scene-warm"}

// bootOSPreviewProgress is the preview's simulated queue at ms: the boot queue
// finished long before anyone opened Settings.
func bootOSPreviewProgress(ms float64) (float64, string) {
	end := bootOSPreviewSettleMS - 200
	if ms >= end {
		return 1, "Ready"
	}
	frac := bootSmoothstep(200, end, ms)
	i := int(frac * float64(len(bootOSPreviewSteps)))
	if i >= len(bootOSPreviewSteps) {
		i = len(bootOSPreviewSteps) - 1
	}
	return frac, bootStepLabel(bootOSPreviewSteps[i])
}

// ── Textures ────────────────────────────────────────────────────────────

// bootOSTextures are the boot's own textures, beside the liquid kit it borrows
// (bootliquid.go): a soft round light (the running light and the halo) and the
// glow of the logo's pane rim, built for one artwork size.
type bootOSTextures struct {
	dot     *sdl.Texture
	ring    *sdl.Texture
	ringPad int32
}

var bootOSKit bootOSTextures

// bootOSKitPrepare builds the boot's textures for renderer and an artwork of
// side pixels, with linear filtering (they are always drawn scaled). Either may
// come back nil; the boot then leaves that light out.
func bootOSKitPrepare(renderer *sdl.Renderer, side int32) {
	bootOSKitRelease()
	if renderer == nil || side <= 0 {
		return
	}
	k := &bootOSKit
	k.ringPad = side/16 + 2
	ring := int(side + 2*k.ringPad)
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	k.dot = bootLiquidTexture(renderer, bootOSDotPixels(bootOSDotSize), bootOSDotSize, bootOSDotSize)
	k.ring = bootLiquidTexture(renderer, bootOSRingPixels(int(side), int(k.ringPad)), ring, ring)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if k.ring != nil {
		_ = k.ring.SetBlendMode(sdl.BLENDMODE_ADD)
	}
}

// bootOSKitRelease destroys the boot's textures. It runs on the render thread
// while their renderer is alive.
func bootOSKitRelease() {
	k := &bootOSKit
	if k.dot != nil {
		_ = k.dot.Destroy()
	}
	if k.ring != nil {
		_ = k.ring.Destroy()
	}
	*k = bootOSTextures{}
}

// bootOSDotPixels renders a soft round light: white, a gaussian that has
// fallen to nothing well inside the texture's edge, so a scaled copy never
// shows its square.
func bootOSDotPixels(size int) []byte {
	if size <= 0 {
		return nil
	}
	pix := make([]byte, size*size*4)
	const sigma = 0.15
	for y := 0; y < size; y++ {
		v := (float64(y)+0.5)/float64(size) - 0.5
		for x := 0; x < size; x++ {
			u := (float64(x)+0.5)/float64(size) - 0.5
			r2 := u*u + v*v
			a := math.Exp(-r2/(2*sigma*sigma)) * (1 - bootSmoothstep(0.40, 0.5, math.Sqrt(r2)))
			bootPutPixel(pix, (y*size+x)*4, 255, 255, 255, a)
		}
	}
	return pix
}

// bootOSRingPixels renders the light along the logo's pane rim for an artwork
// of side pixels, with pad pixels of room round it: a bright line on the rim
// and a faint glow outside it, in white (the boot tints it).
func bootOSRingPixels(side, pad int) []byte {
	if side <= 0 || pad < 0 {
		return nil
	}
	n := side + 2*pad
	pix := make([]byte, n*n*4)
	s := float64(side)
	half := s * (1 - 2*bootLogoPaneInset) / 2
	radius := 2 * half * bootLogoPaneCorner
	sigma := math.Max(1, s*0.010)
	glow := 5 * sigma
	for y := 0; y < n; y++ {
		py := float64(y-pad) + 0.5 - s/2
		for x := 0; x < n; x++ {
			px := float64(x-pad) + 0.5 - s/2
			d := bootRoundRectSDF(px, py, half, half, radius)
			a := math.Exp(-(d / sigma) * (d / sigma))
			if d > 0 {
				a += 0.30 * math.Exp(-(d/glow)*(d/glow))
			}
			bootPutPixel(pix, (y*n+x)*4, 255, 255, 255, a)
		}
	}
	return pix
}

// ── Playing it ──────────────────────────────────────────────────────────

// bootOSRun is one boot on screen.
type bootOSRun struct {
	renderer   *sdl.Renderer
	config     *Config
	powerOn    bool
	motion     bool
	continuity bool
	preview    bool
	deadlineMS float64
	layout     bootLogoLayout
	cx         float64
	// The progress line: the queue's snapshot, the fraction drawn (eased
	// toward the real one, never back), the status label and when it last
	// changed.
	snap    stageSnapshot
	frac    float64
	label   string
	labelAt float64
	lastMS  float64
	arrive  bootArriveState
}

// bootOSMotionChosenKey is the user-store flag Quick Settings sets the first
// time Reduce motion is switched by hand (quicksettings.go, which Settings →
// Display & theme writes through too): from then on the boot follows the switch.
const bootOSMotionChosenKey = "reducedMotionChosen"

// bootOSMotion reports whether the boot moves. Reduced motion keeps every beat
// a fade — when someone chose it. The handheld's out-of-box settings turn
// Reduce motion on for the menus (trimui/jukaconfig.device.json, and main.go on
// a first start away from Windows), and honouring that default made the boot
// of an OS a fade on exactly the device it was made for: the light never ran
// round the pane and nothing rose into place. So away from Windows, where Reduce
// motion starts off and on is always a choice, the boot follows it only once it
// has been switched by hand (bootOSMotionChosenKey); Battery Saver — the power
// mode, or the saver a low battery engages — always stills it.
func bootOSMotion(config *Config) bool {
	if config == nil {
		return true
	}
	if batterySaverActive || currentPowerMode(config) == PowerModeBatterySaver {
		return false
	}
	if !effectiveReducedMotion(config) {
		return true
	}
	if IsWindows() {
		return false
	}
	_, chosen := config.Variables.Custom[bootOSMotionChosenKey]
	return !chosen
}

// bootOSPlay shows a boot on renderer (ShowSplash runs it for this start): the
// full one for a power-on or a preview, the quick hello otherwise. allotted
// bounds the hold (a launch's budget, a power-on's longest wait); the arrival
// comes after it.
func bootOSPlay(renderer *sdl.Renderer, config *Config, allotted time.Duration, preview bool) {
	if renderer == nil || bootOS.running {
		return
	}
	bootOS.running = true
	defer func() { bootOS.running = false }()
	// The pad and window events the boot held go back to the main loop as it
	// hands the screen back (splash.go), after a quit it requeued itself.
	defer splashRequeueHeld()
	r := &bootOSRun{
		renderer: renderer,
		config:   config,
		powerOn:  preview || bootOS.mode == bootOSModePowerOn,
		motion:   bootOSMotion(config),
		preview:  preview,
	}
	// The firmware's boot logo is only known to be on screen when a boot hook
	// started this power-on and the logo is ours: then the boot starts from
	// exactly that picture instead of fading in.
	r.continuity = r.powerOn && !preview && bootOSContinuityReason(bootOS.reason) && bootLogoStateSaysOurs()
	r.deadlineMS = float64((allotted + splashCheckGrace).Milliseconds())

	// The wordmark is Inter at the layout's size, without the accessibility
	// scale, so it is the same word on the same pixels as the boot logo's.
	font := loadCachedFont(homeUIFontFile, bootLogoWordPx(screenHeight))
	// The liquid kit's glass sheet is never drawn by the boot: a token sheet
	// keeps the kit's build cheap. Without the kit the boot still plays, on a
	// flat backdrop with the plain face.
	if !bootLiquidPrepare(renderer, 16, 16, 4, font, bootLogoWordText) {
		bootLiquidRelease()
	}
	defer bootLiquidRelease()
	lineH := bootLiquidWordHeight()
	if lineH <= 0 && font != nil {
		lineH = int32(font.Height())
	}
	r.layout = bootLogoPlace(screenWidth, screenHeight, lineH)
	r.cx = float64(screenWidth) / 2
	bootOSKitPrepare(renderer, r.layout.Sprite.W)
	defer bootOSKitRelease()
	defer r.arrive.release()
	if !preview {
		// The warm-up keeps its render for the arrival while the boot is up.
		bootHandoff.keep = true
		defer bootHandoffRelease()
		startupMarkNow("splash-ready")
	}
	if r.powerOn && !preview {
		// The queued startup work runs first, behind a still frame: the
		// firmware's logo held a moment longer (or the black it left), rather
		// than a light that stops on its lap or a bloom that stalls while the
		// first screen is drawn cold. The timeline, and the chime with it,
		// starts after; the checks that run on their own keep the progress
		// line honest.
		r.drawHold(0)
		r.renderer.Present()
		bootWorkDrain()
	}
	if r.powerOn {
		chimeStart(config, bootOSDelayedScore(chimeStartScore, bootOSChimeDelay), chimeStartSeconds+bootOSChimeDelay, chimeStartGain)
	}
	r.play()
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
}

// play runs the boot until its arrival has finished, a quit arrives, or a
// press cuts the arrival short.
func (r *bootOSRun) play() {
	start := time.Now()
	frame := splashFrameBudget()
	holdEnd := -1.0
	for {
		frameStart := time.Now()
		ms := float64(time.Since(start).Microseconds()) / 1000
		quit, skip := splashPollInput(ms)
		if quit || exitRequested.Load() {
			// A close, or the firmware powering off (a signal sets the exit
			// request and starts the exit watchdog), ends the boot at once: a
			// close goes back on the queue for the main loop, after the drain.
			if quit {
				requeueQuit()
			}
			return
		}
		switch {
		case holdEnd < 0 && (skip || r.holdDone(ms)):
			// A press asks for the first screen: a power-on's arrives at the
			// quick pace, a launch's at once.
			beats := &bootArrivePower
			switch {
			case skip && !r.powerOn:
				beats = &bootArriveSkip
			case skip || !r.powerOn:
				beats = &bootArriveQuick
			}
			r.beginArrival(beats)
			holdEnd = float64(time.Since(start).Microseconds()) / 1000
			ms = holdEnd
		case holdEnd >= 0 && skip:
			// A press during the arrival lands on the first screen at once.
			return
		}
		final := false
		if holdEnd < 0 {
			r.updateProgress(ms)
			r.drawHold(ms)
		} else {
			am := ms - holdEnd
			if am >= r.arrive.dur {
				am, final = r.arrive.dur, true
			}
			r.drawArrival(ms, am)
		}
		r.renderer.Present()
		if final {
			return
		}
		// The hold is a progress screen, not a pause: what a frame did not
		// spend drawing pays for queued startup work (at least one step, so a
		// slow one cannot starve the queue). The arrival stays smooth; what is
		// left drains right after it.
		if holdEnd < 0 && !r.preview {
			bootWorkTick(frame - time.Since(frameStart) - 2*time.Millisecond)
		}
		if rest := frame - time.Since(frameStart); rest > 0 {
			sdl.Delay(uint32(rest.Milliseconds()))
		}
	}
}

// holdDone reports whether the boot may stop holding at ms: past its budget, or
// with its beats played and the queue settled.
func (r *bootOSRun) holdDone(ms float64) bool {
	if ms >= r.deadlineMS {
		return true
	}
	if r.powerOn {
		if ms < bootOSPowerOnMinMS {
			return false
		}
		if r.preview {
			return ms >= bootOSPreviewSettleMS
		}
		return bootWorkSettled()
	}
	if ms >= r.deadlineMS-bootOSLaunchOverlapMS {
		return true
	}
	return ms >= bootOSLaunchMinMS && bootWorkSettled()
}

// beginArrival ends the hold with arrival beats. The arrival draws the first
// screen afresh, which needs the setup the queue does (the focus graph, the
// gamepad, the warm caches): a hold cut short by a press or by its budget runs
// what is left of the queue first, instead of arriving on a half-made screen
// (main would run it straight after anyway). A power-on's queue ran before
// its first beat, so there this is a no-op.
func (r *bootOSRun) beginArrival(beats *bootArriveBeats) {
	if !r.preview {
		bootWorkDrain()
	}
	r.arrive.begin(r.renderer, r.config, beats, r.motion)
}

// updateProgress moves the progress line toward the queue's state at ms.
func (r *bootOSRun) updateProgress(ms float64) {
	dt := ms - r.lastMS
	r.lastMS = ms
	target, label := 1.0, ""
	if r.preview {
		target, label = bootOSPreviewProgress(ms)
	} else {
		bootWorkSnapshotInto(&r.snap)
		target = r.snap.fraction()
		label = r.snap.Current
		if label == "" {
			label = "Starting up"
			if r.snap.Total > 0 && r.snap.Done >= r.snap.Total {
				label = "Ready"
			}
		}
	}
	if dt > 0 && target > r.frac {
		r.frac += (target - r.frac) * (1 - math.Exp(-dt/160))
	}
	if label != r.label && (r.label == "" || ms-r.labelAt >= bootOSStatusHoldMS) {
		r.label, r.labelAt = label, ms
	}
}

// bootOSBaseColour is the backdrop the boot dawns into: the theme's base, a
// shade under Home's on a dark theme so the blooms have depth to glow in.
func bootOSBaseColour() sdl.Color {
	base := ColorBackground
	if !glassLightTheme() {
		base = glassMix(base, sdl.Color{A: 255}, 0.28)
	}
	base.A = 255
	return base
}

// bootOSDawnColour is what the boot's backdrop fill looks like at dawn: the
// base over black at that alpha, which is the base scaled toward black.
func bootOSDawnColour(dawn float64) sdl.Color {
	base := bootOSBaseColour()
	d := bootClamp01(dawn)
	return sdl.Color{
		R: uint8(math.Round(float64(base.R) * d)),
		G: uint8(math.Round(float64(base.G) * d)),
		B: uint8(math.Round(float64(base.B) * d)),
		A: 255,
	}
}

// bootOSLuminance is a colour's relative luminance (WCAG), 0..1.
func bootOSLuminance(c sdl.Color) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// bootOSContrast is the WCAG contrast ratio of two colours, 1..21.
func bootOSContrast(a, b sdl.Color) float64 {
	la, lb := bootOSLuminance(a), bootOSLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// bootOSInkOn is a text colour on the boot screen over the background bg drawn
// under it, dawn being how far the theme's backdrop has come up. On a dark
// theme the boot's own ink turns into the theme's as the backdrop comes up:
// both are light, so the word never dims. On a light theme the background
// runs from black to light while the theme's ink is dark, so a blend of the
// two inks would cross the background's own grey halfway and vanish; the word
// takes whichever ink stands out more from bg instead, switching once, where
// the two stand out equally.
func bootOSInkOn(boot, theme, bg sdl.Color, dawn float64, light bool) sdl.Color {
	if !light {
		return glassMix(boot, theme, dawn)
	}
	if bootOSContrast(boot, bg) >= bootOSContrast(theme, bg) {
		return boot
	}
	return theme
}

// drawHold paints a frame of the hold.
func (r *bootOSRun) drawHold(ms float64) {
	f := bootOSTimeline(ms, r.powerOn, r.motion, r.continuity)
	r.drawBackdrop(ms, &f)
	r.drawLayer(&f, 1, 1)
	r.drawWord(&f, 1)
	r.drawProgress(f.track, f.dawn)
}

// drawBackdrop paints the screen behind the logo: black, the theme's base
// dawning over it, and the liquid's four blooms, each at its own presence,
// gathered behind the logo at first and drifting out to their orbits.
func (r *bootOSRun) drawBackdrop(ms float64, f *bootOSFrame) {
	renderer := r.renderer
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetClipRect(nil)
	// Black with the theme's base dawning over it, drawn as the one opaque
	// fill the two would blend to.
	dawn := bootClamp01(f.dawn)
	setDrawColor(renderer, bootOSDawnColour(dawn))
	bootLiquidR = sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
	_ = renderer.FillRect(&bootLiquidR)
	k := &bootLiquid
	if !k.ready || k.renderer != renderer {
		return
	}
	t := ms / 1000
	w, h := float64(screenWidth), float64(screenHeight)
	px, py := bootLiquidParallax(t, r.motion)
	tileX := float64(r.layout.Sprite.X) + float64(r.layout.Sprite.W)/2
	tileY := float64(r.layout.Sprite.Y) + float64(r.layout.Sprite.H)/2
	accent := glassAccent()
	for i := range bootBlobs {
		presence := f.blob[i]
		if presence <= 0 {
			continue
		}
		b := &bootBlobs[i]
		tex := k.blob[b.tex]
		if tex == nil {
			continue
		}
		cx, cy, d, angle := bootBlobPlace(i, t, r.motion, w, h)
		if e := f.emerge; e < 1 {
			cx = tileX + (cx-tileX)*e
			cy = tileY + (cy-tileY)*e
			d *= 0.55 + 0.45*e
		}
		col := b.col
		if b.accent {
			col = accent
		}
		alpha := float64(b.alpha) * presence
		if k.light {
			alpha *= 0.55
		}
		if !r.motion {
			// Reduced motion keeps the light alive with a slow breath of
			// brightness instead of movement.
			alpha *= 0.92 + 0.08*math.Sin(t*0.9+b.phase)
		}
		_ = tex.SetColorMod(col.R, col.G, col.B)
		_ = tex.SetAlphaMod(uint8(math.Min(255, math.Round(alpha))))
		bootLiquidF = sdl.FRect{X: float32(cx + px - d/2), Y: float32(cy + py - d/2), W: float32(d), H: float32(d)}
		_ = renderer.CopyExF(tex, nil, &bootLiquidF, angle, nil, sdl.FLIP_NONE)
	}
	for i := range k.blob {
		if k.blob[i] != nil {
			_ = k.blob[i].SetColorMod(255, 255, 255)
			_ = k.blob[i].SetAlphaMod(255)
		}
	}
	if k.vignette != nil && dawn > 0 {
		va := 255.0
		if k.light {
			va = 70
		}
		_ = k.vignette.SetAlphaMod(uint8(math.Round(va * dawn)))
		_ = renderer.Copy(k.vignette, nil, &bootLiquidR)
		_ = k.vignette.SetAlphaMod(255)
	}
}

// drawLayer paints the logo and its lights: the halo behind the tile, the
// artwork, the glow of its rim and the light running round it. fade scales
// every part's presence and zoom grows them all about the artwork's centre
// (the arrival's zoom through the glass).
func (r *bootOSRun) drawLayer(f *bootOSFrame, fade, zoom float64) {
	sp := r.layout.Sprite
	logoA := bootClamp01(f.logo) * bootClamp01(fade)
	if sp.W <= 0 || logoA <= 0 {
		return
	}
	renderer := r.renderer
	k := &bootOSKit
	cx := float64(sp.X) + float64(sp.W)/2
	cy := float64(sp.Y) + float64(sp.H)/2
	scale := zoom * f.logoScale
	side := float64(sp.W) * scale
	// The halo: the logo's two blooms, spilled onto the screen behind it.
	if halo := bootClamp01(f.halo) * logoA; halo > 0 && k.dot != nil {
		_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
		peak := 74.0
		if glassLightTheme() {
			peak = 46
		}
		r.drawDot(cx-0.10*side, cy-0.08*side, 2.3*side, glassAccent(), peak*halo)
		r.drawDot(cx+0.12*side, cy+0.10*side, 2.3*side, bootOSBloomViolet, 0.85*peak*halo)
	}
	if tex := brandLogoTexture(renderer, sp.W); tex != nil {
		bootOSF = sdl.FRect{X: float32(cx - side/2), Y: float32(cy - side/2), W: float32(side), H: float32(side)}
		_ = tex.SetAlphaMod(uint8(math.Round(255 * logoA)))
		_ = renderer.CopyF(tex, nil, &bootOSF)
		_ = tex.SetAlphaMod(255)
	}
	// The rim's glow, then the light running round it, both added to the
	// glass so they read as light, not paint. Added white vanishes on a light
	// theme's backdrop, so there they are laid on normally in the rim's own
	// colours, which show on black and on white alike.
	light := glassLightTheme()
	if rim := bootClamp01(f.rim) * logoA; rim > 0 && k.ring != nil {
		s := (float64(sp.W) + 2*float64(k.ringPad)) * scale
		tint := glassMix(sdl.Color{R: 255, G: 255, B: 255, A: 255}, glassAccent(), 0.25)
		mode, peak := sdl.BlendMode(sdl.BLENDMODE_ADD), 150.0
		if light {
			tint = glassMix(bootOSRimCyan, bootOSRimViolet, 0.5)
			mode, peak = sdl.BLENDMODE_BLEND, 190
		}
		_ = k.ring.SetBlendMode(mode)
		_ = k.ring.SetColorMod(tint.R, tint.G, tint.B)
		_ = k.ring.SetAlphaMod(uint8(math.Round(peak * rim)))
		bootOSF = sdl.FRect{X: float32(cx - s/2), Y: float32(cy - s/2), W: float32(s), H: float32(s)}
		_ = renderer.CopyF(k.ring, nil, &bootOSF)
		_ = k.ring.SetColorMod(255, 255, 255)
		_ = k.ring.SetAlphaMod(255)
		_ = k.ring.SetBlendMode(sdl.BLENDMODE_ADD)
	}
	if f.comet >= 0 && f.cometA > 0 && k.dot != nil {
		r.drawComet(f.comet, f.cometA*logoA, cx, cy, scale, light)
	}
	if k.dot != nil {
		_ = k.dot.SetColorMod(255, 255, 255)
		_ = k.dot.SetAlphaMod(255)
		_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
	}
}

// drawDot paints the soft round light centred on (x, y), d across, tinted c at
// alpha (0..255), in whatever blend mode the dot is set to.
func (r *bootOSRun) drawDot(x, y, d float64, c sdl.Color, alpha float64) {
	k := &bootOSKit
	if k.dot == nil || alpha <= 0 || d <= 0 {
		return
	}
	_ = k.dot.SetColorMod(c.R, c.G, c.B)
	_ = k.dot.SetAlphaMod(uint8(math.Min(255, math.Round(alpha))))
	bootOSF = sdl.FRect{X: float32(x - d/2), Y: float32(y - d/2), W: float32(d), H: float32(d)}
	_ = r.renderer.CopyF(k.dot, nil, &bootOSF)
}

// drawComet paints the light running round the pane at u of its lap: a short
// trail of soft dots in the rim's colour under each, and a white core at its
// head, added to what is under them. scale grows it about (cx, cy) with the
// artwork. On a light theme (light) it is laid on normally instead, with a
// core in the rim's colour: added light, and a white core, would vanish there.
func (r *bootOSRun) drawComet(u, alpha, cx, cy, scale float64, light bool) {
	k := &bootOSKit
	pane, rad := bootLogoPaneRect(r.layout.Sprite)
	side := float64(r.layout.Sprite.W)
	mode := sdl.BlendMode(sdl.BLENDMODE_ADD)
	if light {
		mode = sdl.BLENDMODE_BLEND
	}
	_ = k.dot.SetBlendMode(mode)
	for i := bootOSCometTrail - 1; i >= 0; i-- {
		back := float64(i) / bootOSCometTrail
		x, y := bootOSPanePoint(pane, rad, u-float64(i)*0.012)
		col := bootOSRimColour(pane, x, y)
		x, y = cx+(x-cx)*scale, cy+(y-cy)*scale
		r.drawDot(x, y, side*0.17*(1-0.5*back)*scale, col, 235*alpha*math.Pow(1-back, 1.6))
	}
	x, y := bootOSPanePoint(pane, rad, u)
	core := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	if light {
		core = bootOSRimColour(pane, x, y)
	}
	x, y = cx+(x-cx)*scale, cy+(y-cy)*scale
	r.drawDot(x, y, side*0.075*scale, core, 255*alpha)
}

// drawWord paints the wordmark under the logo, coming into focus and catching
// its glint, at fade.
func (r *bootOSRun) drawWord(f *bootOSFrame, fade float64) {
	fade = bootClamp01(fade)
	if fade <= 0 {
		return
	}
	ink := bootOSInkOn(bootLogoInk, ColorTextPrimary(), bootOSDawnColour(f.dawn), f.dawn, glassLightTheme())
	if _, ok := bootLiquidDrawWordmark(r.renderer, int32(r.cx), r.layout.WordTop, f.word, fade, f.glint, ink, r.motion); ok {
		return
	}
	// No wordmark textures (the liquid kit was refused): the plain face.
	font := loadCachedFont(homeUIFontFile, r.layout.WordPx)
	if font == nil {
		return
	}
	if a := fade * bootSmoothstep(0.3, 0.95, f.word); a > 0 {
		w, _ := measureText(font, bootLogoWordText)
		renderText(r.renderer, r.config, font, bootLogoWordText, stageInk(ink, a), int32(r.cx)-w/2, r.layout.WordTop)
	}
}

// drawProgress paints the progress line and the step under it at alpha.
func (r *bootOSRun) drawProgress(alpha, dawn float64) {
	tr := r.layout.Track
	if alpha <= 0 || tr.W <= 0 {
		return
	}
	renderer := r.renderer
	fillRoundedRect(renderer, tr.X, tr.Y, tr.W, tr.H, tr.H/2, stageFade(glassA(34), alpha))
	if fw := int32(float64(tr.W) * bootClamp01(r.frac)); fw > 0 {
		if fw < tr.H {
			fw = tr.H
		}
		accent := glassAccent()
		fillRoundedRect(renderer, tr.X, tr.Y, fw, tr.H, tr.H/2, stageFade(accent, alpha))
		fillCircle(renderer, tr.X+fw, tr.Y+tr.H/2, tr.H+3, stageFade(sdl.Color{R: accent.R, G: accent.G, B: accent.B, A: 60}, alpha))
	}
	if r.label == "" {
		return
	}
	font, _ := getCachedFont(r.config, "small")
	if font == nil {
		return
	}
	label := ellipsize(font, r.label, tr.W*2)
	lw, _ := measureText(font, label)
	ink := bootOSInkOn(bootOSStatusInk, ColorTextTertiary(), bootOSDawnColour(dawn), dawn, glassLightTheme())
	renderText(renderer, r.config, font, label, stageInk(ink, alpha), int32(r.cx)-lw/2, r.layout.StatusY)
}

// bootOSPreviewOpen plays the full boot now, over the current screen, and
// arrives back on it (Settings → General → Watch the boot animation). The
// boot queue finished long ago, so its progress line runs a simulated one.
func bootOSPreviewOpen(c *Config) {
	if c == nil || mainWindow == nil || bootOS.running {
		return
	}
	if currentSceneIndex < 0 || currentSceneIndex >= len(c.Scenes) {
		return
	}
	renderer, err := mainWindow.GetRenderer()
	if err != nil || renderer == nil {
		return
	}
	// The arrival draws the screen it returns to afresh; a warm render of it
	// is kept as the fallback should the driver refuse that target, and goes
	// however the preview ends.
	bootHandoffRelease()
	defer bootHandoffRelease()
	bootHandoff.keep = true
	warmSceneCaches(renderer, c, c.Scenes[currentSceneIndex])
	bootOSPlay(renderer, c, time.Duration(bootOSPowerOnMinMS+1500)*time.Millisecond, true)
}
