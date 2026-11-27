package main

// screenrec.go — Record screen: a video of whatever is on the panel, JukaHub
// itself, RetroArch, a port or a standalone emulator, into the folder the
// Recordings page lists (recordings.go).
//
// The picture comes from the panel's framebuffer, as the time-lapse
// (timelapse.go) and Screen Share's game source (castcap_fb.go) read it: the
// bundled ffmpeg's fbdev input follows the double-buffer pan, so whoever
// draws is recorded and nothing is asked of the emulator. It runs at the
// lightest setting that still reads well on a phone — at most 640 px wide and
// 30 frames a second, MPEG-4 Part 2 (the stock ffmpeg has no libx264), one
// encoder thread, nice 10 so the game always wins the CPU — in its own process
// group.
//
// A stop always leaves a playable file. The MP4 is fragmented (a fragment
// every keyframe, two seconds): a recording cut short by a crash, a kill or a
// flat battery keeps everything up to its last fragment. A normal stop is
// ffmpeg's own "q" on its stdin, then an interrupt to its group, and a kill
// only when both were ignored. ffmpeg's -t caps a recording at
// screenRecMaxLen even if JukaHub is gone.
//
// Video only: the emulators write straight to the ALSA device and the stock
// kernels have no loopback to read the game's sound back from.
//
// Guard rails, checked every few seconds: the card running out of room, the
// battery about to give out (before lowbattery.go suspends), the maximum
// length. A card that cannot be written refuses to start. One recording at a
// time.
//
// Where it starts and stops: Quick Settings → Record screen (it carries on
// into a game launched meanwhile, ports and standalone emulators included),
// the game page's More → Record this game (from the game's start to its
// end), the in-game menu (Select + L2 → Record screen / Stop recording), and
// a "REC" pill at the top of JukaHub's screens while it runs. CrossMix's own
// Screen Recorder app is not driven: it puts itself in the background and
// JukaHub could not tell when it stops; its ffmpeg is used when JukaHub has
// none, and its clips are listed on the Recordings page.
//
// Windows and desktop builds have no recorder: there is no framebuffer to
// read, a game is another window, and the share's desktop capture is a
// different path (screenRecSupported).

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// screenRecMaxLen is the longest recording; ffmpeg's own -t enforces it.
	screenRecMaxLen = 30 * time.Minute
	// screenRecFPS caps the frame rate, screenRecWidth the picture's width.
	screenRecFPS   = 30
	screenRecWidth = 640
	// screenRecQ is MPEG-4's quantiser (2 best .. 31): 5 keeps text sharp at
	// a few MB a minute. screenRecGOP is a keyframe, and so a fragment,
	// every two seconds.
	screenRecQ   = 5
	screenRecGOP = 2 * screenRecFPS
	// screenRecNice is the recorder's CPU priority (screenrec_linux.go).
	screenRecNice = 10

	// screenRecStartFree is the room a recording needs to start, and
	// screenRecStopFree where a running one stops.
	screenRecStartFree = 300 << 20
	screenRecStopFree  = 150 << 20
	// screenRecBatteryFloor is the charge (not charging) under which nothing
	// records: lowbattery.go saves and suspends at 3 %.
	screenRecBatteryFloor = lowBatteryWarnPct

	// screenRecGuardEvery paces the free-space and battery checks.
	screenRecGuardEvery = 5 * time.Second
	// screenRecQuitWait is how long each polite stop gets: "q", then an
	// interrupt, then the kill.
	screenRecQuitWait = 4 * time.Second
	// screenRecArmFor is how long "Record this game" waits for the game to
	// start (an emulator picker or Game Ready's card may ask first).
	screenRecArmFor = 5 * time.Minute
	// screenRecMinBytes is the smallest file kept: less is a recording that
	// never got a frame.
	screenRecMinBytes = 2 << 10
)

// qsActionScreenRec is Quick Settings' Record screen switch, numbered past
// quicksettings.go's own like the Stopwatch button (stopwatch.go).
const qsActionScreenRec qsAction = 100

// gmRecord is the in-game menu's Record screen row, numbered past
// gamemenu.go's own like the Stopwatch row.
const gmRecord gmItem = 101

// screenRecSession is one running recording.
type screenRecSession struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	tail  *castTail
	dir   string
	path  string
	stamp time.Time
	// named is the title the file was named after when it started.
	named string
	// gamePath: the recording belongs to this game's launch and stops when
	// it ends ("" — it runs until it is stopped).
	gamePath string
	stopReq  chan string
	done     chan struct{}
}

// screenRec is the recorder's state. mu guards everything in it; the main
// thread starts recordings, any goroutine may stop one.
var screenRec struct {
	mu  sync.Mutex
	cur *screenRecSession
	// stopping: cur was asked to stop and is closing its file.
	stopping bool
	// title is what the file is named after: the game on screen when the
	// recording started, or the first one launched during it.
	title string
	// armPath / armAt: "Record this game" waits for that game's start.
	armPath string
	armAt   time.Time
	// gameTitle / gamePath: the library game on screen now ("" none).
	gameTitle string
	gamePath  string
	// note is the in-game menu row's second line after a stop ("Saved …").
	note string
}

// screenRecSince is when the running recording started (UnixNano), 0 when
// none runs: the render thread's pill and the switches read it every frame.
var screenRecSince atomic.Int64

// screenRecSupported reports whether this device can record its panel: a
// Linux handheld with a framebuffer. Answered from memory.
func screenRecSupported() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice() && castGamesSupported()
}

// screenRecActive reports whether a recording runs (or is finishing).
func screenRecActive() bool { return screenRecSince.Load() != 0 }

// screenRecCurrentPath is the file being written ("" none), so the
// Recordings list can leave it out until it is finished.
func screenRecCurrentPath() string {
	screenRec.mu.Lock()
	defer screenRec.mu.Unlock()
	if screenRec.cur == nil {
		return ""
	}
	return screenRec.cur.path
}

// ── ffmpeg ────────────────────────────────────────────────────────────────

// screenRecArgs is the recording command: the framebuffer at screenRecFPS,
// turned deg degrees, scaled down to screenRecWidth when it is wider, MPEG-4
// into a fragmented MP4 at out, for at most screenRecMaxLen. There is no
// -nostdin: "q" on stdin is the clean stop.
func screenRecArgs(device, out string, deg, fbW, fbH int) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostats",
		"-f", "fbdev", "-framerate", strconv.Itoa(screenRecFPS), "-i", device,
		"-t", strconv.Itoa(int(screenRecMaxLen / time.Second)), "-an"}
	var filters []string
	outW := fbW
	switch deg {
	case 90:
		filters = append(filters, "transpose=1")
		outW = fbH
	case 180:
		filters = append(filters, "hflip", "vflip")
	case 270:
		filters = append(filters, "transpose=2")
		outW = fbH
	}
	if outW <= 0 || outW > screenRecWidth {
		filters = append(filters, fmt.Sprintf("scale=%d:-2:flags=fast_bilinear", screenRecWidth))
	}
	if len(filters) > 0 {
		args = append(args, "-vf", strings.Join(filters, ","))
	}
	return append(args,
		"-c:v", "mpeg4", "-q:v", strconv.Itoa(screenRecQ), "-g", strconv.Itoa(screenRecGOP),
		"-threads", "1", "-pix_fmt", "yuv420p",
		"-movflags", "+frag_keyframe+empty_moov",
		"-f", "mp4", "-y", out)
}

// screenRecFindFFmpeg is the ffmpeg to record with: JukaHub's own (the
// tools folder or PATH), else the one CrossMix's Screen Recorder runs. Main
// thread (it reads the config's tools_path).
func screenRecFindFFmpeg(config *Config) string {
	tools := ""
	if config != nil {
		tools = config.Variables.ToolsPath
	}
	if p := castFindFFmpeg(tools); p != "" {
		return p
	}
	return screenRecCrossMixFFmpeg()
}

// screenRecCrossMixFFmpeg is the ffmpeg CrossMix ships for its Screen
// Recorder (Apps/ScreencapTK/bin), "" when there is none.
func screenRecCrossMixFFmpeg() string {
	var cands []string
	if app, ok := extAppFind(extAppScreenRecorder); ok && app.Dir != "" {
		cands = append(cands, filepath.Join(app.Dir, "bin", "ffmpeg"),
			filepath.Join(filepath.Dir(app.Dir), "ScreencapTK", "bin", "ffmpeg"))
	}
	for _, root := range extAppScanDirs() {
		cands = append(cands, filepath.Join(root, "ScreencapTK", "bin", "ffmpeg"))
	}
	for _, c := range cands {
		if info, err := os.Stat(c); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return c
		}
	}
	return ""
}

// screenRecCrossMixOutput is the folder CrossMix's Screen Recorder writes,
// when that app is on the card ("" otherwise).
func screenRecCrossMixOutput() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if app, ok := extAppFind(extAppScreenRecorder); ok && app.Dir != "" {
		return filepath.Join(app.Dir, "output")
	}
	for _, root := range extAppScanDirs() {
		if dir := filepath.Join(root, "ScreenRecorder", "output"); dirExists(dir) {
			return dir
		}
	}
	return ""
}

// ── Names ─────────────────────────────────────────────────────────────────

// screenRecTitleClean keeps a title's letters, digits and a few marks, so
// it is a file name on any card.
func screenRecTitleClean(title string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.TrimSpace(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_.,()'&!+", r):
			b.WriteRune(r)
			space = false
		case b.Len() > 0 && !space:
			b.WriteByte(' ')
			space = true
		}
		if b.Len() >= 64 {
			break
		}
	}
	return strings.Trim(b.String(), " .")
}

// screenRecFileName is the clip's name: the game (or "Screen") and when.
func screenRecFileName(title string, at time.Time) string {
	name := screenRecTitleClean(title)
	if name == "" {
		name = "Screen"
	}
	return name + " - " + at.Format("2006-01-02 15.04.05") + ".mp4"
}

// screenRecFreePath is dir/name, or "name (2).mp4" and so on when taken.
func screenRecFreePath(dir, name string) string {
	path := filepath.Join(dir, name)
	ext := filepath.Ext(name)
	for n := 2; n < 100; n++ {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		path = filepath.Join(dir, strings.TrimSuffix(name, ext)+" ("+strconv.Itoa(n)+")"+ext)
	}
	return path
}

// ── Start and stop ────────────────────────────────────────────────────────

// screenRecPreflight checks what a recording needs before anything starts:
// the device, ffmpeg, the battery and room on the card. It returns the ffmpeg
// and the folder. Main thread.
func screenRecPreflight(config *Config) (string, string, error) {
	if !screenRecSupported() {
		return "", "", errors.New("recording needs a handheld with a readable screen")
	}
	screenRec.mu.Lock()
	busy, stopping := screenRec.cur != nil, screenRec.stopping
	screenRec.mu.Unlock()
	if stopping {
		return "", "", errors.New("the last recording is still being saved")
	}
	if busy {
		return "", "", errors.New("a recording is already running")
	}
	ffmpeg := screenRecFindFFmpeg(config)
	if ffmpeg == "" {
		return "", "", errors.New("ffmpeg is not installed")
	}
	if pct := getBatteryPercent(); pct >= 0 && pct <= screenRecBatteryFloor && !getBatteryCharging() {
		return "", "", fmt.Errorf("the battery is too low (%d%%)", pct)
	}
	dir := recordingsDirFor(config)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("cannot create %s — is the card read-only?", dir)
	}
	if free, err := freeBytes(dir); err == nil && free < screenRecStartFree {
		return "", "", fmt.Errorf("only %s free on the card", formatSize(free))
	}
	return ffmpeg, dir, nil
}

// screenRecStart starts a recording. title names the file ("" until a game
// starts); gamePath ties it to that game's launch. It returns the file.
// Main thread.
func screenRecStart(config *Config, title, gamePath string) (string, error) {
	ffmpeg, dir, err := screenRecPreflight(config)
	if err != nil {
		return "", err
	}
	f, err := os.Open(castFBDevice)
	if err != nil {
		return "", fmt.Errorf("cannot read the screen: %w", err)
	}
	cw, ch := int(screenWidth), int(screenHeight)
	geom, err := castProbeFB(f, cw, ch)
	f.Close()
	if err != nil {
		return "", fmt.Errorf("cannot read the screen: %w", err)
	}
	deg := castRotateFor(config)
	if deg < 0 {
		deg = castAutoRotate(geom.W, geom.H, cw, ch)
	}
	stamp := time.Now()
	path := screenRecFreePath(dir, screenRecFileName(title, stamp))
	// The file is made here first: a card that cannot be written says so
	// now, not as an ffmpeg error after the game has started.
	probe, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("cannot write to %s — is the card read-only?", dir)
	}
	probe.Close()
	cmd := hiddenCommand(ffmpeg, screenRecArgs(castFBDevice, path, deg, geom.W, geom.H)...)
	screenRecConfigure(cmd)
	if lib := filepath.Join(filepath.Dir(filepath.Dir(ffmpeg)), "lib"); strings.Contains(ffmpeg, "ScreencapTK") && dirExists(lib) {
		// CrossMix's ffmpeg, with the libraries beside it.
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+lib+":"+os.Getenv("LD_LIBRARY_PATH"))
	}
	cmd.WaitDelay = 2 * time.Second
	tail := &castTail{}
	cmd.Stderr = tail
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("ffmpeg did not start: %w", err)
	}
	screenRecLower(cmd)
	s := &screenRecSession{cmd: cmd, stdin: stdin, tail: tail, dir: dir, path: path, stamp: stamp,
		named: title, gamePath: gamePath, stopReq: make(chan string, 1), done: make(chan struct{})}
	screenRec.mu.Lock()
	screenRec.cur, screenRec.stopping = s, false
	screenRec.title = title
	screenRec.note = ""
	// Under the lock, with cur: the last recording's finish clears it under
	// the same lock, so it can never zero this one's clock.
	screenRecSince.Store(stamp.UnixNano())
	screenRec.mu.Unlock()
	logInfo("screenrec", "recording %dx%d at %d bits, turned %d°, to %s with %s", geom.W, geom.H, geom.BPP, deg, path, ffmpeg)
	go s.run()
	return path, nil
}

// screenRecStop asks the running recording to stop; why goes in the log.
// Any goroutine; it never waits. false: nothing was recording.
func screenRecStop(why string) bool {
	screenRec.mu.Lock()
	s := screenRec.cur
	if s != nil {
		screenRec.stopping = true
	}
	screenRec.mu.Unlock()
	if s == nil {
		return false
	}
	select {
	case s.stopReq <- why:
	default:
	}
	return true
}

// run watches one recording until it ends, then finishes the file.
func (s *screenRecSession) run() {
	defer close(s.done)
	waitCh := make(chan error, 1)
	go func() { waitCh <- s.cmd.Wait() }()
	tick := time.NewTicker(screenRecGuardEvery)
	defer tick.Stop()
	// ffmpeg's -t ends it at the limit; this is the backstop.
	limit := time.NewTimer(screenRecMaxLen + 15*time.Second)
	defer limit.Stop()
	why := ""
	var werr error
	for done := false; !done; {
		select {
		case werr = <-waitCh:
			done = true
		case why = <-s.stopReq:
			werr, done = s.halt(waitCh), true
		case <-limit.C:
			why = "it reached " + strconv.Itoa(int(screenRecMaxLen/time.Minute)) + " minutes"
			werr, done = s.halt(waitCh), true
		case <-tick.C:
			if why = s.guard(); why != "" {
				werr, done = s.halt(waitCh), true
			}
		}
	}
	s.finish(why, werr)
}

// guard is the reason to stop now ("" none): the card, the battery, or the
// handheld asleep. powerSleepRun stops a recording as the sleep begins;
// this catches one started while a sleep was already under way — ffmpeg is
// not one of the children a sleep stops, so a screen-off sleep would go on
// recording a dark panel in a pocket.
func (s *screenRecSession) guard() string {
	if powerSleepActive() {
		return "the handheld went to sleep"
	}
	if free, err := freeBytes(s.dir); err == nil && free < screenRecStopFree {
		return "the card is nearly full"
	}
	if pct := getBatteryPercent(); pct >= 0 && pct <= screenRecBatteryFloor && !getBatteryCharging() {
		return "the battery is low"
	}
	return ""
}

// halt stops ffmpeg so it closes the file: "q", then an interrupt to its
// group, then the kill, each after screenRecQuitWait.
func (s *screenRecSession) halt(waitCh <-chan error) error {
	_, _ = io.WriteString(s.stdin, "q\n")
	_ = s.stdin.Close()
	for step := 0; ; step++ {
		t := time.NewTimer(screenRecQuitWait)
		select {
		case err := <-waitCh:
			t.Stop()
			return err
		case <-t.C:
		}
		switch step {
		case 0:
			logInfo("screenrec", "ffmpeg did not stop on q; interrupting it")
			screenRecSignal(s.cmd, false)
		case 1:
			logWarn("screenrec", "ffmpeg ignored the interrupt; killing it (the fragments written so far stay playable)")
			screenRecSignal(s.cmd, true)
		default:
			return errors.New("ffmpeg did not exit")
		}
	}
}

// finish names and announces the file, or removes one with nothing in it.
func (s *screenRecSession) finish(why string, werr error) {
	screenRec.mu.Lock()
	title := screenRec.title
	screenRec.cur, screenRec.stopping = nil, false
	screenRecSince.Store(0)
	screenRec.mu.Unlock()
	ran := time.Since(s.stamp).Round(time.Second)
	if why == "" && ran >= screenRecMaxLen-5*time.Second {
		why = "it reached " + strconv.Itoa(int(screenRecMaxLen/time.Minute)) + " minutes"
	}
	info, err := os.Stat(s.path)
	if err != nil || info.Size() < screenRecMinBytes {
		_ = os.Remove(s.path)
		detail := s.tail.String()
		if detail == "" && werr != nil {
			detail = werr.Error()
		}
		if detail == "" {
			detail = "nothing was recorded"
		}
		logWarn("screenrec", "recording failed after %s: %s", ran, detail)
		screenRecDone(false, "Recording failed: "+firstLine(detail), "Could not record: "+firstLine(detail))
		return
	}
	path := s.path
	if title != "" && title != s.named {
		// A game started after the recording did: the clip is named after it.
		if to := screenRecFreePath(s.dir, screenRecFileName(title, s.stamp)); os.Rename(path, to) == nil {
			path = to
		}
	}
	if werr != nil && why == "" {
		why = "ffmpeg stopped"
		logWarn("screenrec", "ffmpeg stopped by itself: %v (%s)", werr, s.tail.String())
	}
	logInfo("screenrec", "saved %s (%s, %s)%s", path, ran, formatSize(info.Size()), screenRecWhyLog(why))
	trophyEvent(tkRecording)
	msg := "Recording saved to Recordings: " + filepath.Base(path)
	if why != "" {
		msg = "Recording stopped (" + why + ") — saved to Recordings: " + filepath.Base(path)
	}
	screenRecDone(true, msg, "Saved: "+filepath.Base(path))
}

func screenRecWhyLog(why string) string {
	if why == "" {
		return ""
	}
	return ": " + why
}

// screenRecDone hands the outcome to the main thread: the toast, the Quick
// Settings switch, and the in-game menu row's line (read there at once).
func screenRecDone(ok bool, toast, note string) {
	screenRec.mu.Lock()
	screenRec.note = note
	screenRec.mu.Unlock()
	kind := ToastKindSuccess
	if !ok {
		kind = ToastKindWarning
	}
	tbxPost(func() {
		ShowToast(toast, kind)
		qsSetToggleValue(qsActionScreenRec, false)
	})
}

// screenRecShutdown ends a recording on the way out, so the file is closed
// cleanly (a Cleanup hook; bounded): "q" and the interrupt get their usual
// time, then the group is killed rather than left recording the firmware's
// menu — the fragments written so far stay playable.
func screenRecShutdown() {
	screenRec.mu.Lock()
	s := screenRec.cur
	screenRec.mu.Unlock()
	if s == nil {
		return
	}
	screenRecStop("JukaHub is closing")
	select {
	case <-s.done:
		return
	case <-time.After(screenRecQuitWait + time.Second):
	}
	screenRecSignal(s.cmd, true)
	select {
	case <-s.done:
	case <-time.After(time.Second):
	}
}

// ── Games ─────────────────────────────────────────────────────────────────

// screenRecGameStarted follows a library launch (main thread): "Record this
// game" starts here, and a recording already running takes the game's name.
func screenRecGameStarted(game libraryGame) {
	title := game.Label()
	screenRec.mu.Lock()
	screenRec.gameTitle, screenRec.gamePath = title, game.Path
	// Armed for this game and nothing recording yet (a recording already
	// running simply goes on, named after the game).
	armed := screenRec.armPath != "" && screenRec.armPath == game.Path &&
		time.Since(screenRec.armAt) < screenRecArmFor && screenRec.cur == nil
	screenRec.armPath = ""
	if screenRec.cur != nil && screenRec.title == "" {
		screenRec.title = title
	}
	if screenRec.cur == nil {
		// The in-game row's "Saved: …" / "Cannot record: …" was about an
		// earlier game.
		screenRec.note = ""
	}
	screenRec.mu.Unlock()
	if !armed {
		return
	}
	if _, err := screenRecStart(appConfig, title, game.Path); err != nil {
		logWarn("screenrec", "could not record %s: %v", title, err)
		ShowToast("Could not record "+title+": "+firstLine(err.Error()), ToastKindWarning)
	}
}

// screenRecGameExited ends a recording that belonged to the game (the
// reaper goroutine).
func screenRecGameExited(game libraryGame) {
	screenRec.mu.Lock()
	if screenRec.gamePath == game.Path {
		screenRec.gameTitle, screenRec.gamePath = "", ""
	}
	s := screenRec.cur
	screenRec.mu.Unlock()
	if s != nil && s.gamePath != "" && s.gamePath == game.Path {
		screenRecStop("the game ended")
	}
}

// screenRecPageRow is the game page's More row.
func screenRecPageRow(game libraryGame) (string, string, bool) {
	if !screenRecSupported() {
		return "", "", false
	}
	if screenRecActive() {
		return "Stop recording", "Saves the clip to Recordings", true
	}
	return "Record this game", "Starts it and records the screen until it closes · video only", true
}

// screenRecPageOpen arms the recording for the game's launch and starts the
// game the way Play does.
func screenRecPageOpen(game libraryGame) {
	if screenRecStop("the game page") {
		ShowToast("Saving the recording…", ToastKindInfo)
		return
	}
	if _, _, err := screenRecPreflight(appConfig); err != nil {
		ShowToast("Cannot record: "+firstLine(err.Error()), ToastKindWarning)
		return
	}
	screenRec.mu.Lock()
	screenRec.armPath, screenRec.armAt = game.Path, time.Now()
	screenRec.mu.Unlock()
	if gmetaUI.open && gmetaUI.game.Path == game.Path {
		gmetaPlay(nil)
		return
	}
	chooseGameLauncher(game, func(launcher string) {
		if err := launchLibraryGame(game, launcher); err != nil {
			gameLaunchErrorToast(game.Label(), err)
		}
	})
}

// ── Quick Settings ────────────────────────────────────────────────────────

// screenRecQSToggles adds the Record screen switch where recording works.
func screenRecQSToggles(toggles []qsToggle) []qsToggle {
	if !screenRecSupported() {
		return toggles
	}
	for _, t := range toggles {
		if t.Action == qsActionScreenRec {
			return toggles
		}
	}
	return append(toggles, qsToggle{Label: "Record screen", Icon: "[C]", Action: qsActionScreenRec,
		OnColor: sdl.Color{R: 235, G: 80, B: 90, A: 255}, OffColor: qsToggleOff})
}

// screenRecQSApply is the switch: on starts recording (a game started
// meanwhile is recorded too), off stops and saves.
func screenRecQSApply(want bool) error {
	if !want {
		if screenRecStop("Quick Settings") {
			ShowToast("Saving the recording…", ToastKindInfo)
		}
		return nil
	}
	screenRec.mu.Lock()
	title := screenRec.gameTitle
	screenRec.mu.Unlock()
	path, err := screenRecStart(appConfig, title, "")
	if err != nil {
		return err
	}
	ShowToast("Recording the screen into "+filepath.Dir(path)+" — games you start are recorded too", ToastKindInfo)
	return nil
}

// ── The in-game menu ──────────────────────────────────────────────────────

// screenRecMenuLabel is the in-game menu row's words.
func screenRecMenuLabel() (string, string) {
	screenRec.mu.Lock()
	note, stopping := screenRec.note, screenRec.stopping
	screenRec.mu.Unlock()
	if stopping {
		return "Record screen", "Saving…"
	}
	if since := screenRecSince.Load(); since != 0 {
		return "Stop recording", "Recording " + screenRecClock(time.Since(time.Unix(0, since))) + " · saved to Recordings"
	}
	if note != "" {
		return "Record screen", note
	}
	return "Record screen", "Until you stop it here or the game ends · video only"
}

// screenRecMenuPress is A on the row: a recording stops (the menu stays,
// its line says where the clip went); otherwise one starts and the game
// carries on under it.
func screenRecMenuPress(config *Config) {
	if screenRecStop("the in-game menu") {
		// The row says "Saving…" until the file is closed, then its name.
		PlayActivateSound()
		return
	}
	screenRec.mu.Lock()
	title, gamePath := screenRec.gameTitle, screenRec.gamePath
	screenRec.mu.Unlock()
	if title == "" {
		title = gaOverlay.title
	}
	if _, err := screenRecStart(config, title, gamePath); err != nil {
		PlayErrorSound()
		screenRec.mu.Lock()
		screenRec.note = "Cannot record: " + firstLine(err.Error())
		screenRec.mu.Unlock()
		return
	}
	PlayActivateSound()
	gaClose("the player started recording", true)
}

// ── The REC pill ──────────────────────────────────────────────────────────

// screenRecClock is a running time as m:ss.
func screenRecClock(d time.Duration) string {
	sec := int(d / time.Second)
	if sec < 0 {
		sec = 0
	}
	return strconv.Itoa(sec/60) + ":" + fmt.Sprintf("%02d", sec%60)
}

// screenRecPillText keeps the pill's words for one second. Render thread.
var screenRecPillText struct {
	sec  int
	text string
}

// screenRecDrawIndicator draws a small "REC m:ss" pill at the top of the
// screen while a recording runs, beside Screen Share's LIVE pill when that is
// up. It is drawn on every frame and so goes with the recording; not on a
// frame a screenshot is about to save.
func screenRecDrawIndicator(renderer *sdl.Renderer) {
	since := screenRecSince.Load()
	if renderer == nil || since == 0 || shotModeOn() || screenshotRequested {
		return
	}
	font := loadHomeFonts().Small
	if font == nil {
		return
	}
	sec := int(time.Since(time.Unix(0, since)) / time.Second)
	if screenRecPillText.text == "" || screenRecPillText.sec != sec {
		screenRecPillText.sec = sec
		screenRecPillText.text = "REC " + screenRecClock(time.Duration(sec)*time.Second)
	}
	label := screenRecPillText.text
	tw, th := measureText(font, label)
	h := th + scaleY(6, screenHeight)
	dot := h / 3
	padX := scaleX(10, screenWidth)
	gap := scaleX(6, screenWidth)
	w := padX + dot + gap + tw + padX
	x := (screenWidth - w) / 2
	if castIsEnabled() && !castPanel.open {
		if n := castWatcherCount(); n > 0 {
			// The LIVE pill is centred (castDrawLiveIndicator): this one goes
			// to its left.
			lw, _ := measureText(font, castLiveLabel(n))
			live := padX + dot + gap + lw + padX
			x = (screenWidth-live)/2 - gap - w
		}
	}
	y := scaleY(6, screenHeight)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, x, y, w, h, h/2, sdl.Color{R: 18, G: 18, B: 22, A: 215})
	fillCircle(renderer, x+padX+dot/2, y+h/2, dot/2, ColorDanger)
	drawText(renderer, font, label, x+padX+dot+gap, y+(h-th)/2, sdl.Color{R: 255, G: 255, B: 255, A: 255}, textAlignLeft)
}

func init() {
	gameLaunchRegisterStarted(func(game libraryGame, _ string, _ *exec.Cmd) { screenRecGameStarted(game) })
	gameLaunchRegisterExited(func(game libraryGame, _ string, _ time.Duration, _ error) { screenRecGameExited(game) })
	gamePageRegisterExtra(gamePageExtra{ID: "screenrec", Order: 45, Icon: "video", Row: screenRecPageRow, Open: screenRecPageOpen})
	// Quitting closes the file cleanly rather than leaving ffmpeg to its -t.
	Cleanup = append(Cleanup, screenRecShutdown)
}

