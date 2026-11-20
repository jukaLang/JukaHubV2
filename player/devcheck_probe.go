package main

// devcheck_probe.go — what the Device check (devcheck.go) measures, off the
// main thread: the test tone and whether the output played it, the output
// and the volume, the headphone jack, a Bluetooth search, the Wi-Fi link and
// the internet, a real suspend and what came back after it, and the game the
// check started. Each probe posts its lines back through tbxPost; the page
// never waits on one.
//
// Nothing measured here names a network, a device or an address: the route is
// a kind ("Speaker", "Bluetooth headset"), Bluetooth devices are counted, and
// Wi-Fi is a link, an answer from the internet and a signal level.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ── Durations ─────────────────────────────────────────────────────────────

// devCheckDur writes a measured duration: "0.8 s", "12 s", "1 min 5 s".
func devCheckDur(d time.Duration) string {
	switch {
	case d < 0:
		d = 0
	case d < 10*time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + " s"
	case d < time.Minute:
		return strconv.Itoa(int(d.Round(time.Second).Seconds())) + " s"
	}
	d = d.Round(time.Second)
	m, s := int(d/time.Minute), int((d%time.Minute)/time.Second)
	if s == 0 {
		return strconv.Itoa(m) + " min"
	}
	return strconv.Itoa(m) + " min " + strconv.Itoa(s) + " s"
}

// ── Buttons and sticks ────────────────────────────────────────────────────

// devCheckButtonsRequired are the buttons every handheld has. L3 and R3 are
// counted when pressed but not asked for: not every model clicks its sticks.
var devCheckButtonsRequired = [...]uint8{
	sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y,
	sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER,
	sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_DPAD_DOWN,
	sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT,
	sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_BACK,
}

// The controller test's raw readings the step judges: a stick reaches its
// edge past devCheckStickEdge of full travel both ways on both axes, a
// trigger past devCheckTriggerEdge.
const (
	devCheckStickEdge   = 0.7 * 32767
	devCheckTriggerEdge = 0.5 * 32767
)

// devCheckButtonsResult reads what the controller test (ctrldiag.go) saw:
// the lines for the step, whether everything this model has was seen, and
// whether anything was pressed or moved at all. trimui asks for L2 and R2,
// sticks for both sticks. Main thread: the test's state is the main loop's.
func devCheckButtonsResult(trimui, sticks bool) (lines []string, complete, touched bool) {
	seen := 0
	var missing []string
	for _, b := range devCheckButtonsRequired {
		if st := ctrldiag.buttons[b]; st != nil && st.Presses > 0 {
			seen++
			touched = true
		} else {
			missing = append(missing, ctrldiagButtonLabel(b))
		}
	}
	line := fmt.Sprintf("%d of %d buttons seen", seen, len(devCheckButtonsRequired))
	if len(missing) > 0 && len(missing) <= 6 {
		line += " (not seen: " + strings.Join(missing, ", ") + ")"
	}
	lines = append(lines, line)
	complete = len(missing) == 0

	// A stick reaches its edges when both of its axes travelled past
	// devCheckStickEdge each way.
	edges := func(axis int) bool {
		a := ctrldiag.axes[axis]
		return a.Seen && float64(a.Min) <= -devCheckStickEdge && float64(a.Max) >= devCheckStickEdge
	}
	moved := func(axes ...int) bool {
		for _, ax := range axes {
			if ctrldiag.axes[ax].Seen {
				return true
			}
		}
		return false
	}
	stick := func(name string, ax, ay int) (bool, string) {
		switch {
		case edges(ax) && edges(ay):
			return true, name + " reached its edges"
		case moved(ax, ay):
			return false, name + " did not reach every edge"
		}
		return false, name + " not moved"
	}
	leftOK, leftLine := stick("left stick", 0, 1)
	rightOK, rightLine := stick("right stick", 2, 3)
	if moved(0, 1, 2, 3) {
		touched = true
	}
	switch {
	case leftOK && rightOK:
		lines = append(lines, "Both sticks reached their edges")
	case sticks || moved(0, 1, 2, 3):
		lines = append(lines, devCheckUpperFirst(leftLine)+", "+rightLine)
	}
	if sticks && !(leftOK && rightOK) {
		complete = false
	}

	full := func(axis int) bool {
		a := ctrldiag.axes[axis]
		return a.Seen && float64(a.Max) >= devCheckTriggerEdge
	}
	switch {
	case full(4) && full(5):
		lines = append(lines, "L2 and R2 pressed fully")
		touched = true
	case moved(4, 5):
		lines = append(lines, "L2 or R2 did not travel fully")
		touched = true
		if trimui {
			complete = false
		}
	case trimui:
		lines = append(lines, "L2 and R2 not pressed")
		complete = false
	}

	var extras []string
	for _, b := range []uint8{sdl.CONTROLLER_BUTTON_LEFTSTICK, sdl.CONTROLLER_BUTTON_RIGHTSTICK} {
		if st := ctrldiag.buttons[b]; st != nil && st.Presses > 0 {
			extras = append(extras, ctrldiagButtonLabel(b))
		}
	}
	if len(extras) > 0 {
		lines = append(lines, "Also seen: "+strings.Join(extras, ", "))
	}
	if problems := ctrldiagProblems(); len(problems) > 0 {
		lines = append(lines, "Drift: "+problems[0])
		complete = false
	}
	return lines, complete && touched, touched
}

// ── Sound ─────────────────────────────────────────────────────────────────

// devCheckToneLen is how long the test tone lasts.
const devCheckToneLen = 900 * time.Millisecond

// devCheckTonePCM renders the test tone for mono S16 at rate: three rising
// notes with soft edges, loud enough to hear and well short of clipping.
func devCheckTonePCM(rate int) []byte {
	if rate <= 0 {
		rate = 48000
	}
	notes := [...]float64{523.25, 659.25, 783.99}
	note := int(float64(rate) * 0.24)
	gap := int(float64(rate) * 0.05)
	fade := rate / 100
	total := len(notes)*(note+gap) + gap
	pcm := make([]byte, 2*total)
	i := gap
	for _, f := range notes {
		for n := 0; n < note; n++ {
			env := 1.0
			if n < fade {
				env = float64(n) / float64(fade)
			} else if note-n < fade {
				env = float64(note-n) / float64(fade)
			}
			v := int16(math.Round(0.4 * 32767 * env * math.Sin(2*math.Pi*f*float64(n)/float64(rate))))
			pcm[2*(i+n)] = byte(v)
			pcm[2*(i+n)+1] = byte(uint16(v) >> 8)
		}
		i += note + gap
	}
	return pcm
}

// devCheckPlayTone plays the test tone on JukaHub's own sound device, at the
// system volume whatever the interface-sound setting says, and reports
// whether the output took all of it and a line saying so. Off the main
// thread: it waits up to a few seconds for the queue to drain.
func devCheckPlayTone() (bool, string) {
	bootSfxPrepare()
	if !audioAvailable {
		return false, "No sound device opened"
	}
	if alarmFiringActive() {
		return false, "An alarm was ringing; the tone was not played"
	}
	pcm := devCheckTonePCM(int(audioSampleRate))
	audioMutex.Lock()
	err := outQueue(audioDev, pcm)
	audioMutex.Unlock()
	if err != nil {
		return false, "The sound device refused the tone: " + devCheckClip(err.Error(), 80)
	}
	start := time.Now()
	limit := devCheckToneLen + 3*time.Second
	for time.Since(start) < limit {
		time.Sleep(50 * time.Millisecond)
		audioMutex.Lock()
		left := outQueued(audioDev)
		audioMutex.Unlock()
		if left == 0 {
			return true, "The output played the whole tone in " + devCheckDur(time.Since(start))
		}
	}
	return false, "The tone did not finish playing within " + devCheckDur(limit)
}

// devCheckRoute names where JukaHub's sound goes now, as a kind only. The
// order is the output monitor's (audiooutput.go): a USB DAC, then a Bluetooth
// headset, then the jack. Off the main thread: the jack is read from the
// kernel.
func devCheckRoute() string {
	pcm := audioRouteChildPCM()
	switch {
	case usbDACPCMIs(pcm):
		return "USB DAC"
	case pcm != "":
		return "Bluetooth headset"
	case !IsTrimuiDevice():
		return "This computer's default output"
	case devCheckJack():
		return "Headphones"
	}
	return "Speaker"
}

// devCheckJack reports whether the kernel says a cable is in the jack: the
// switch class, extcon, or the evdev switch the Smart Pro uses.
func devCheckJack() bool {
	return headphoneJackPlugged(os.ReadFile) || headphoneJackEvdev()
}

// devCheckSoundProbe plays the tone for step id and posts pre (lines the page
// already knows), the route, the volume and what the output did. vol is the
// level read on the main thread (-1 when the mixer is not known). Off the
// main thread.
func devCheckSoundProbe(runID int64, id string, vol int, pre []string, done func()) {
	route := devCheckRoute()
	played, how := devCheckPlayTone()
	out := "Output: " + route
	if vol >= 0 {
		out += " · volume " + strconv.Itoa(vol) + "%"
	}
	lines := append(append([]string(nil), pre...), out)
	if id == dcStepUSBDAC {
		if card := usbDACCurrent(); card.id != "" {
			line := "USB card: " + devCheckClip(card.name, 60)
			if card.usbid != "" {
				line += " (USB " + card.usbid + ")"
			}
			lines = append(lines, line)
		}
	}
	lines = append(lines, how)
	if !played {
		logWarn("devcheck", "%s tone: %s", id, how)
	}
	tbxPost(func() {
		done()
		devCheckSetEvidence(runID, id, lines, "")
	})
}

// ── Bluetooth ─────────────────────────────────────────────────────────────

// devCheckBluetoothProbe switches Bluetooth on and searches once, then posts
// the adapter's state and how many devices answered. Names and addresses are
// never kept. Bluetooth that was off goes off again afterwards, unless the
// player went on to the Bluetooth page. A scene task: leaving the page stops
// it.
func devCheckBluetoothProbe(ctx context.Context, runID int64, done func()) {
	lines := []string{"Adapter before the check: not registered"}
	if btAdapterPresent() {
		lines[0] = "Adapter before the check: present"
	}
	wasOn, known := btKernelRadio()
	if !known {
		wasOn = btRadioIsOn()
	}
	// restore switches a radio this search switched on back off, from the
	// main loop. Not behind the Bluetooth page, which wants it on.
	restore := func() bool {
		if wasOn || tbxCurrentScene(appConfig) == btSceneName {
			return false
		}
		btPowerOffAsync(nil)
		return true
	}
	auto := ""
	err := btBringUp(ctx, nil)
	var devs []bzDevice
	if err == nil {
		if !wasOn {
			lines = append(lines, "Bluetooth switched on for the search")
		}
		devs, err = bzScan(ctx, bzScanDuration, nil)
	} else if btBringUpFailed(err) {
		auto = dcFail
	}
	if ctx.Err() != nil {
		tbxPost(func() {
			done()
			restore()
		})
		return
	}
	switch {
	case err != nil && auto == dcFail:
		lines = append(lines, "Bluetooth did not switch on: "+devCheckClip(err.Error(), 90))
	case err != nil:
		lines = append(lines, "The search did not finish: "+devCheckClip(err.Error(), 90))
	default:
		paired, connected := 0, 0
		for _, d := range devs {
			if d.Paired {
				paired++
			}
			if d.Connected {
				connected++
			}
		}
		lines = append(lines, fmt.Sprintf("Search: %s answered in %s (%d paired, %d connected)",
			pluralizeCount(len(devs), "device", "devices"), devCheckDur(bzScanDuration), paired, connected))
	}
	if links := btSysfsLinkCount(); links > 0 {
		lines = append(lines, "Live links: "+strconv.Itoa(links))
	}
	tbxPost(func() {
		done()
		if restore() {
			lines = append(lines, "Bluetooth switched back off, as it was")
		}
		devCheckSetEvidence(runID, dcStepBluetooth, lines, auto)
	})
}

// ── Wi-Fi ─────────────────────────────────────────────────────────────────

// devCheckWiFiProbe reads the link, asks the internet once more and reads
// the signal, then posts them. A connected link that reaches the internet
// passes. A scene task.
func devCheckWiFiProbe(ctx context.Context, runID int64, done func()) {
	link := wifiLinkIsUp()
	online := false
	if link {
		online = devCheckProbeInternet(ctx, 8*time.Second)
	}
	if ctx.Err() != nil {
		tbxPost(done)
		return
	}
	lines := []string{}
	auto := ""
	if !link {
		lines = append(lines, "Wi-Fi: not connected")
	} else {
		lines = append(lines, "Wi-Fi: connected")
		switch status := NetworkStatus(); {
		case online:
			lines = append(lines, "Internet: reachable")
			auto = dcPass
		case status == "sign-in required":
			lines = append(lines, "Internet: a sign-in page is in the way")
		default:
			lines = append(lines, "Internet: not reachable")
		}
		lines = append(lines, devCheckSignalLine())
	}
	tbxPost(func() {
		done()
		devCheckSetEvidence(runID, dcStepWiFi, lines, auto)
	})
}

// devCheckProbeInternet asks the network monitor for a fresh probe and waits
// for its answer, at most limit.
func devCheckProbeInternet(ctx context.Context, limit time.Duration) bool {
	if netReconnecting() {
		return netWaitOnline(ctx, limit)
	}
	probed := netMon.probeSignal()
	netNudge()
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case <-probed:
	case <-t.C:
	case <-ctx.Done():
		return false
	}
	return IsOnline()
}

// devCheckSignalLine is the signal as dBm and meter bars, or as bars alone.
func devCheckSignalLine() string {
	bars := readWiFiSignalLevel()
	if s, err := wifiSignalPoll(); err == nil && s.RSSI < 0 && s.RSSI > -110 {
		return fmt.Sprintf("Signal: %d dBm, %d of 4 bars", s.RSSI, wifiSignalBars(s.RSSI))
	}
	if bars >= 1 && bars <= 4 {
		return fmt.Sprintf("Signal: %d of 4 bars", bars)
	}
	return "Signal: not reported"
}

// ── Sleep and wake ────────────────────────────────────────────────────────

// devCheckSleep follows one sleep while the sleep step is on screen: the page
// marks itself every frame (armedAt), the before-sleep hook notes the
// network as it was, and the wake hook measures what came back.
var devCheckSleep struct {
	mu        sync.Mutex
	armedAt   time.Time
	runID     int64
	pending   bool
	pendRun   int64
	preLink   bool
	preOnline bool
}

// devCheckSleepArmWindow is how recently the step must have been drawn for a
// sleep to be the check's.
const devCheckSleepArmWindow = 5 * time.Second

// devCheckSleepMeasuring is up while a wake is being measured; the page says
// so. Main thread.
var devCheckSleepMeasuring bool

func init() {
	sleepHookRegisterBefore(devCheckBeforeSleep)
	sleepHookRegisterWake(devCheckAfterWakeHook)
	gameLaunchRegisterStarted(devCheckGameStarted)
	gameLaunchRegisterExited(devCheckGameExited)
}

// devCheckSleepArm marks the sleep step as on screen. Main thread, every
// frame the step is drawn.
func devCheckSleepArm(runID int64) {
	devCheckSleep.mu.Lock()
	devCheckSleep.armedAt = time.Now()
	devCheckSleep.runID = runID
	devCheckSleep.mu.Unlock()
}

// devCheckBeforeSleep is the before-sleep hook: a sleep that starts while the
// step is up is the check's. Quick: two reads.
func devCheckBeforeSleep(string) {
	devCheckSleep.mu.Lock()
	armed := !devCheckSleep.armedAt.IsZero() && time.Since(devCheckSleep.armedAt) < devCheckSleepArmWindow
	run := devCheckSleep.runID
	devCheckSleep.mu.Unlock()
	if !armed {
		return
	}
	link := wifiLinkIsUp()
	online := link && IsOnline()
	devCheckSleep.mu.Lock()
	devCheckSleep.pending = true
	devCheckSleep.pendRun = run
	devCheckSleep.preLink = link
	devCheckSleep.preOnline = online
	devCheckSleep.mu.Unlock()
}

// devCheckAfterWakeHook is the wake hook: the measuring runs on its own
// goroutine, so the sleep path is not held.
func devCheckAfterWakeHook(source string, slept time.Duration, suspended bool) {
	devCheckSleep.mu.Lock()
	pending := devCheckSleep.pending
	run, link, online := devCheckSleep.pendRun, devCheckSleep.preLink, devCheckSleep.preOnline
	devCheckSleep.pending = false
	devCheckSleep.mu.Unlock()
	if !pending || quickPowerOffSilent() || exitRequested.Load() {
		return
	}
	wokeAt := time.Now()
	tbxPost(func() { devCheckSleepMeasuring = true })
	go devCheckMeasureWake(run, source, slept, suspended, wokeAt, link, online)
}

// devCheckWakeWait is how long Wi-Fi gets to come back after a wake.
const devCheckWakeWait = 60 * time.Second

// devCheckMeasureWake measures one wake: was it a real suspend, how long, in
// which sleep state; how long the Wi-Fi link and the internet took to come
// back; whether sound plays again. A real suspend of at least five seconds
// after which everything came back passes; one after which something did not
// fails; a short or screen-only sleep only says so.
func devCheckMeasureWake(runID int64, source string, slept time.Duration, suspended bool, wokeAt time.Time, preLink, preOnline bool) {
	var lines []string
	state := quickSuspendState()
	realSleep := suspended && slept >= 5*time.Second
	switch {
	case suspended:
		line := "Suspended for " + devCheckDur(slept)
		if state != "" {
			line += " (" + state + ")"
		}
		lines = append(lines, line)
		if !realSleep {
			lines = append(lines, "Too short to count: wait ten seconds before waking it")
		}
	default:
		lines = append(lines, "The kernel did not suspend; only the screen went off")
	}
	if src := devCheckClip(strings.ReplaceAll(source, "-", " "), 24); src != "" {
		lines = append(lines, "Sleep started by: "+src)
	}

	wifiOK := true
	if preLink {
		var linkAt, netAt time.Duration
		for time.Since(wokeAt) < devCheckWakeWait {
			if exitRequested.Load() {
				return
			}
			if linkAt == 0 && wifiLinkIsUp() {
				linkAt = max(time.Since(wokeAt), time.Millisecond)
			}
			// The monitor answers "online" only after a probe made since the
			// wake (netmon.go's reconnect window).
			if linkAt > 0 && preOnline && netAt == 0 && IsOnline() && !netReconnecting() {
				netAt = max(time.Since(wokeAt), time.Millisecond)
			}
			if linkAt > 0 && (!preOnline || netAt > 0) {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		// Measured from the end of the wake: a link that was already up
		// then came back "at once".
		after := func(d time.Duration) string {
			if d < 100*time.Millisecond {
				return "at once"
			}
			return "after " + devCheckDur(d)
		}
		switch {
		case linkAt == 0:
			lines = append(lines, "Wi-Fi did not come back within "+devCheckDur(devCheckWakeWait))
			wifiOK = false
		case preOnline && netAt == 0:
			lines = append(lines, "Wi-Fi link back "+after(linkAt)+"; the internet not within "+devCheckDur(devCheckWakeWait))
			wifiOK = false
		case preOnline:
			lines = append(lines, "Wi-Fi link back "+after(linkAt)+", internet "+after(netAt))
		default:
			lines = append(lines, "Wi-Fi link back "+after(linkAt))
		}
	} else {
		lines = append(lines, "Wi-Fi was off before the sleep: not checked")
	}

	// The outputs reopen as the wake finishes: give them a moment.
	if wait := 1500*time.Millisecond - time.Since(wokeAt); wait > 0 {
		time.Sleep(wait)
	}
	soundOK, how := devCheckPlayTone()
	lines = append(lines, "Sound after the wake: "+devCheckLowerFirst(how))

	auto := ""
	if realSleep {
		auto = dcPass
		if !wifiOK || !soundOK {
			auto = dcFail
		}
	}
	logInfo("devcheck", "wake measured: suspended=%v slept=%s wifi=%v sound=%v", suspended, devCheckDur(slept), wifiOK, soundOK)
	tbxPost(func() {
		devCheckSleepMeasuring = false
		devCheckSetEvidence(runID, dcStepSleep, lines, auto)
		if auto == dcPass {
			ShowToast("Sleep and wake: everything came back", ToastKindSuccess)
		}
	})
}

// devCheckLowerFirst lowers the first letter, for a line used mid-sentence.
func devCheckLowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// ── Game ──────────────────────────────────────────────────────────────────

// devCheckGame follows the one game the check started, from its start
// (main thread) to its exit (the reaper goroutine).
var devCheckGame struct {
	mu      sync.Mutex
	path    string
	runID   int64
	started bool
	exited  bool
	ranFor  time.Duration
	exitWhy string
	// gen counts changes, so the page knows to read them again.
	gen int
}

// devCheckGameWatch starts following game for run runID. Main thread.
func devCheckGameWatch(path string, runID int64) {
	devCheckGame.mu.Lock()
	devCheckGame.path = path
	devCheckGame.runID = runID
	devCheckGame.started = false
	devCheckGame.exited = false
	devCheckGame.ranFor = 0
	devCheckGame.exitWhy = ""
	devCheckGame.gen++
	devCheckGame.mu.Unlock()
}

// devCheckGameState is a copy of what was seen of the game.
type devCheckGameState struct {
	path    string
	runID   int64
	started bool
	exited  bool
	ranFor  time.Duration
	exitWhy string
	gen     int
}

func devCheckGameNow() devCheckGameState {
	devCheckGame.mu.Lock()
	defer devCheckGame.mu.Unlock()
	return devCheckGameState{path: devCheckGame.path, runID: devCheckGame.runID, started: devCheckGame.started,
		exited: devCheckGame.exited, ranFor: devCheckGame.ranFor, exitWhy: devCheckGame.exitWhy, gen: devCheckGame.gen}
}

// devCheckGameStarted and devCheckGameExited follow one run of the watched
// game: once it is back, a later play of the same game from elsewhere is not
// the check's.
func devCheckGameStarted(game libraryGame, _ string, _ *exec.Cmd) {
	devCheckGame.mu.Lock()
	if devCheckGame.path != "" && devCheckGame.path == game.Path && !devCheckGame.exited {
		devCheckGame.started = true
		devCheckGame.gen++
	}
	devCheckGame.mu.Unlock()
}

func devCheckGameExited(game libraryGame, _ string, ranFor time.Duration, waitErr error) {
	devCheckGame.mu.Lock()
	if devCheckGame.path != "" && devCheckGame.path == game.Path && devCheckGame.started && !devCheckGame.exited {
		devCheckGame.exited = true
		devCheckGame.ranFor = ranFor
		devCheckGame.exitWhy = devCheckExitText(waitErr)
		devCheckGame.gen++
	}
	devCheckGame.mu.Unlock()
}

// devCheckExitText says how a game's process ended.
func devCheckExitText(err error) string {
	if err == nil {
		return "exited normally"
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return "exit code " + strconv.Itoa(code)
		}
		return "ended by a signal"
	}
	return "ended with an error"
}

// devCheckPickGame finds the game the step offers: the one played last that
// is still on the card, else the first game in the library. Android, web and
// PortMaster games start another way and are passed over. Off the main
// thread: it reads the recents and may scan the library.
func devCheckPickGame() (libraryGame, string, bool) {
	usable := func(system, path string) bool {
		switch system {
		case androidLibrarySystem, html5LibrarySystem, portsLibrarySystem, builtInLibrarySystem:
			return false
		}
		return strings.TrimSpace(path) != "" && regularFile(path)
	}
	for _, r := range recentGames() {
		if usable(r.System, r.Path) {
			return libraryGame{Path: r.Path, System: r.System, Title: r.Title}, "the game you played last", true
		}
	}
	lib := aiLibrary()
	for _, s := range lib.Systems {
		for _, g := range s.Games {
			if usable(g.System, g.Path) {
				return g, "the first game in your library", true
			}
		}
	}
	return libraryGame{}, "", false
}

// devCheckGameLines are the game step's evidence: the run as JukaHub saw it,
// then the Verified record's results that are newer than the launch (seq).
func devCheckGameLines(game libraryGame, st devCheckGameState, baseSeq int64) []string {
	lines := []string{"Game: " + devCheckClip(game.Label(), 60) + " (" + devCheckClip(game.System, 30) + ")"}
	switch {
	case st.exited:
		lines = append(lines, "Ran for "+devCheckDur(st.ranFor)+", "+st.exitWhy)
	case st.started:
		lines = append(lines, "Started")
	default:
		lines = append(lines, "Not started yet")
	}
	kind, id, _ := vfGameRef(game)
	if rec, ok := vfLookup(kind, id); ok {
		for _, check := range vfChecks {
			res, ok := rec.Checks[check]
			if !ok || res.Seq <= baseSeq || res.By == vfByUser {
				continue
			}
			line := "Verified " + strings.ToLower(vfCheckLabel(check)) + ": " + res.State
			if res.Note != "" {
				line += " (" + devCheckClip(res.Note, 60) + ")"
			}
			lines = append(lines, line)
		}
	}
	return lines
}
