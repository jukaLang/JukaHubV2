package main

// The time-lapse pipeline has three parts that can fail separately: deciding
// whether the machine can capture at all, taking the stills while a child runs,
// and assembling them when it exits. These tests drive the real recorder and the
// real launch hook with a fake framebuffer grab and a fake encoder, so the
// scheduling and the hand-off are exercised without SDL, ffmpeg or a panel.

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// timelapseRig is what the fake capture and encoder leave behind for the test.
type timelapseRig struct {
	root       string
	encodeArgs []string
	frameCount int
	toasts     []string
}

// useTimelapseRig installs fakes for every seam the recorder has. The fake grab
// writes a non-empty file where a PNG would be (the recorder only checks that a
// frame exists), and the fake encoder writes the output file the real one would.
func useTimelapseRig(t *testing.T) *timelapseRig {
	t.Helper()
	rig := &timelapseRig{root: t.TempDir()}

	prevRoot, prevNow := timelapseRootOverride, timelapseNow
	prevMin, prevMax := timelapseMinInterval, timelapseMaxInterval
	prevCapture, prevEncoder := timelapseCapture, timelapseEncoder
	prevGrab, prevEncode, prevNotify := timelapseGrab, timelapseEncode, timelapseNotify
	t.Cleanup(func() {
		timelapseRootOverride, timelapseNow = prevRoot, prevNow
		timelapseMinInterval, timelapseMaxInterval = prevMin, prevMax
		timelapseCapture, timelapseEncoder = prevCapture, prevEncoder
		timelapseGrab, timelapseEncode, timelapseNotify = prevGrab, prevEncode, prevNotify
	})

	timelapseRootOverride = rig.root
	timelapseMinInterval = time.Millisecond
	timelapseCapture = func(_ *Config, framePath string) (string, []string, error) {
		return "grab", []string{framePath}, nil
	}
	timelapseEncoder = func(*Config) (string, error) { return "ffmpeg", nil }
	timelapseGrab = func(_ string, args []string) error {
		if len(args) == 0 {
			return errors.New("no frame path")
		}
		return os.WriteFile(args[0], []byte("png"), 0o644)
	}
	timelapseEncode = func(_ string, args []string) error {
		rig.encodeArgs = append([]string(nil), args...)
		for _, arg := range args {
			if filepath.Base(arg) == "frame_%04d.png" {
				entries, _ := os.ReadDir(filepath.Dir(arg))
				rig.frameCount = len(entries)
			}
		}
		return os.WriteFile(args[len(args)-1], []byte("mp4"), 0o644)
	}
	timelapseNotify = func(message string, _ ToastKind) {
		rig.toasts = append(rig.toasts, message)
	}
	return rig
}

// timelapseTestConfig enables recording with a millisecond interval; the clamp
// seam makes that legal in tests.
func timelapseTestConfig(extra map[string]interface{}) *Config {
	custom := map[string]interface{}{timelapseIntervalKey: 0.005}
	for k, v := range extra {
		custom[k] = v
	}
	return &Config{Variables: Variables{Custom: custom}}
}

func globMatches(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	return matches
}

// waitForTimelapseFrames blocks until the session folder holds n frames.
func waitForTimelapseFrames(t *testing.T, root string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if frames := globMatches(filepath.Join(root, "*", "*.png")); len(frames) >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("only %d frames were captured, want at least %d",
		len(globMatches(filepath.Join(root, "*", "*.png"))), n)
}

func TestTimelapseSettingsDefaultsAndClamps(t *testing.T) {
	useTimelapseRig(t)

	if got := timelapseSettingsFor(nil); !got.enabled || got.every != timelapseDefaultEvery {
		t.Fatalf("nil config = %+v, want enabled at %v", got, timelapseDefaultEvery)
	}
	if got := timelapseSettingsFor(timelapseTestConfig(map[string]interface{}{timelapseConfigKey: false})); got.enabled {
		t.Error("timelapse:false did not disable recording")
	}
	// A stored JSON number and the string form both parse.
	for _, raw := range []interface{}{"7", 7.0, 7} {
		got := timelapseSettingsFor(timelapseTestConfig(map[string]interface{}{timelapseIntervalKey: raw}))
		if got.every != 7*time.Second {
			t.Errorf("interval %v (%T) = %v, want 7s", raw, raw, got.every)
		}
	}
	timelapseMinInterval, timelapseMaxInterval = 3*time.Second, time.Minute
	if got := timelapseSettingsFor(timelapseTestConfig(map[string]interface{}{timelapseIntervalKey: 0.1})); got.every != 3*time.Second {
		t.Errorf("tiny interval = %v, want the 3s floor", got.every)
	}
	if got := timelapseSettingsFor(timelapseTestConfig(map[string]interface{}{timelapseIntervalKey: 9999})); got.every != time.Minute {
		t.Errorf("huge interval = %v, want the 1m ceiling", got.every)
	}
	if got := timelapseSettingsFor(timelapseTestConfig(nil)); got.maxFrames != timelapseMaxFrames {
		t.Errorf("max frames = %d, want %d", got.maxFrames, timelapseMaxFrames)
	}
}

func TestTimelapseUnavailableCaptureSkipsSession(t *testing.T) {
	rig := useTimelapseRig(t)
	timelapseCapture = func(*Config, string) (string, []string, error) {
		return "", nil, errors.New("no framebuffer")
	}
	if recorder := startTimelapse(timelapseTestConfig(nil), "Zelda"); recorder != nil {
		t.Fatal("a session started without a readable framebuffer")
	}
	if files := globMatches(filepath.Join(rig.root, "*")); len(files) != 0 {
		t.Fatalf("an unavailable capture left %v behind", files)
	}
	// Stop is the only call the launch hook makes, and it must tolerate nil.
	var nilRecorder *timelapseRecorder
	nilRecorder.Stop()
}

func TestTimelapseRecordsAndAssembles(t *testing.T) {
	rig := useTimelapseRig(t)
	recorder := startTimelapse(timelapseTestConfig(nil), "Zelda: A Link to the Past")
	if recorder == nil {
		t.Fatal("no session for an available capture")
	}
	waitForTimelapseFrames(t, rig.root, timelapseMinFrames+2)
	recorder.Stop()

	if rig.frameCount < timelapseMinFrames {
		t.Fatalf("encoder saw %d frames, want at least %d", rig.frameCount, timelapseMinFrames)
	}
	if !hasFFmpegArg(rig.encodeArgs, strconv.Itoa(timelapseOutputFPS)) {
		t.Errorf("encoder args %v do not pin %d fps", rig.encodeArgs, timelapseOutputFPS)
	}
	if !hasFFmpegArg(rig.encodeArgs, "yuv420p") {
		t.Errorf("encoder args %v do not pin a phone-friendly pixel format", rig.encodeArgs)
	}
	films := globMatches(filepath.Join(rig.root, "timelapse_zelda-a-link-to-the-past_*.mp4"))
	if len(films) != 1 {
		t.Fatalf("films = %v, want one named after the game", films)
	}
	if len(rig.toasts) != 1 || !strings.Contains(rig.toasts[0], "Time-lapse ready") {
		t.Fatalf("toasts = %v, want one completion notice", rig.toasts)
	}
	// The session folder is gone; only the film remains.
	if dirs := globMatches(filepath.Join(rig.root, ".*")); len(dirs) != 0 {
		t.Fatalf("session frames were left behind: %v", dirs)
	}
}

func TestTimelapseShortSessionLeavesNothing(t *testing.T) {
	rig := useTimelapseRig(t)
	// One frame is taken immediately and the next is an hour away, so a game
	// that exits at once produces exactly one still.
	timelapseMaxInterval = time.Hour
	recorder := startTimelapse(timelapseTestConfig(map[string]interface{}{timelapseIntervalKey: 3600}), "Quick")
	if recorder == nil {
		t.Fatal("no session for an available capture")
	}
	waitForTimelapseFrames(t, rig.root, 1)
	recorder.Stop()

	if rig.encodeArgs != nil {
		t.Fatalf("a one-frame session was encoded: %v", rig.encodeArgs)
	}
	if files := globMatches(filepath.Join(rig.root, "*")); len(files) != 0 {
		t.Fatalf("a discarded session left %v", files)
	}
	if len(rig.toasts) != 0 {
		t.Fatalf("toasts = %v, want none for a discarded session", rig.toasts)
	}
}

func TestTimelapseEncodeFailureKeepsFrames(t *testing.T) {
	rig := useTimelapseRig(t)
	timelapseEncode = func(string, []string) error { return errors.New("libx264 missing") }

	recorder := startTimelapse(timelapseTestConfig(nil), "Broken Encode")
	if recorder == nil {
		t.Fatal("no session for an available capture")
	}
	waitForTimelapseFrames(t, rig.root, timelapseMinFrames+1)
	recorder.Stop()

	frames := globMatches(filepath.Join(rig.root, ".*", "*.png"))
	if len(frames) < timelapseMinFrames {
		t.Fatalf("a failed encode removed its frames: %v", frames)
	}
	if films := globMatches(filepath.Join(rig.root, "*.mp4")); len(films) != 0 {
		t.Fatalf("a failed encode wrote %v", films)
	}
	if len(rig.toasts) != 1 || !strings.Contains(rig.toasts[0], "could not be encoded") {
		t.Fatalf("toasts = %v, want an encode warning", rig.toasts)
	}
}

func TestTimelapseSlugAndFilmName(t *testing.T) {
	cases := map[string]string{
		"Zelda: A Link to the Past (USA)": "zelda-a-link-to-the-past-usa",
		"  ":                              "game",
		"Pokémon — Crystal":               "pok-mon-crystal",
		"1943":                            "1943",
	}
	for title, want := range cases {
		if got := timelapseSlug(title); got != want {
			t.Errorf("slug(%q) = %q, want %q", title, got, want)
		}
	}
	if got := timelapseSlug(strings.Repeat("a", 120)); len(got) > 48 {
		t.Errorf("long slug is %d characters: %q", len(got), got)
	}
	when := time.Date(2026, 10, 22, 16, 5, 9, 0, time.UTC)
	if got, want := timelapseFilmName("Chrono Trigger", when), "timelapse_chrono-trigger_20261022-160509.mp4"; got != want {
		t.Errorf("film name = %q, want %q", got, want)
	}
}

// TestTimelapseNoticeDrainsOnTheRenderThread: the completion toast is queued by
// the goroutine that encoded the film and shown once from the frame loop.
func TestTimelapseNoticeDrainsOnTheRenderThread(t *testing.T) {
	prevToast := currentToast
	t.Cleanup(func() { currentToast = prevToast })
	timelapseNoticeMu.Lock()
	prevNotice := timelapseNotice
	timelapseNotice.message = ""
	timelapseNoticeMu.Unlock()
	t.Cleanup(func() {
		timelapseNoticeMu.Lock()
		timelapseNotice = prevNotice
		timelapseNoticeMu.Unlock()
	})
	currentToast = ToastState{}

	queueTimelapseNotice("Time-lapse ready: film.mp4", ToastKindSuccess)
	timelapseDrainNotice()
	if currentToast.Message != "Time-lapse ready: film.mp4" || currentToast.Kind != ToastKindSuccess {
		t.Fatalf("toast = %+v, want the queued notice", currentToast)
	}

	// The queue is drained once; a second frame must not repeat the toast.
	currentToast = ToastState{}
	timelapseDrainNotice()
	if currentToast.Message != "" {
		t.Fatalf("a drained notice was shown again: %q", currentToast.Message)
	}
}

// TestLibraryLaunchTimeLapsesTheSession is the hook test: the real launch path
// starts a recorder for the real child and assembles the film when it exits.
func TestLibraryLaunchTimeLapsesTheSession(t *testing.T) {
	rig := useTimelapseRig(t)
	resetGameRecentsState(t)
	dir := t.TempDir()
	gameRecentsFile = func() string { return filepath.Join(dir, "game-recents.json") }

	rom := filepath.Join(dir, "Chrono.gba")
	if err := os.WriteFile(rom, []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "launch.sh")
	script := "#!/bin/sh\nsleep 0.4\n"
	if IsWindows() {
		launcher = filepath.Join(dir, "launch.bat")
		// About a second, so several intervals fit inside the session.
		script = "@echo off\r\nping -n 2 127.0.0.1 >nul\r\n"
	}
	if err := os.WriteFile(launcher, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	prevConfig := appConfig
	appConfig = timelapseTestConfig(nil)
	t.Cleanup(func() { appConfig = prevConfig })

	if err := launchLibraryGame(libraryGame{Path: rom, System: "Game Boy Advance", Title: "Chrono Trigger"}, launcher); err != nil {
		t.Fatalf("launchLibraryGame: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if films := globMatches(filepath.Join(rig.root, "timelapse_chrono-trigger_*.mp4")); len(films) == 1 {
			if rig.frameCount < timelapseMinFrames {
				t.Fatalf("film has %d frames, want at least %d", rig.frameCount, timelapseMinFrames)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no film after the launch; frames captured: %v", globMatches(filepath.Join(rig.root, "*", "*.png")))
}

// TestTimelapseAssemblyRunsRealFFmpeg checks the argument list against the
// bundled encoder: the stills must become a real mp4, not just a file the fake
// encoder wrote. Skipped where ffmpeg is not installed.
func TestTimelapseAssemblyRunsRealFFmpeg(t *testing.T) {
	ffmpeg := findLocalTool("ffmpeg")
	if ffmpeg == "" {
		t.Skip("ffmpeg is not installed on this machine")
	}
	rig := useTimelapseRig(t)
	timelapseEncoder = func(*Config) (string, error) { return ffmpeg, nil }
	timelapseEncode = func(prog string, args []string) error {
		return runQuietCommand(prog, args, 0)
	}

	session := filepath.Join(rig.root, "session")
	if err := os.MkdirAll(session, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < timelapseMinFrames; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 64, 36))
		img.Set(i*8, 18, color.RGBA{R: 200, G: 60, B: 20, A: 255})
		path := filepath.Join(session, fmt.Sprintf("frame_%04d.png", i))
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, img); err != nil {
			file.Close()
			t.Fatal(err)
		}
		file.Close()
	}

	out, err := assembleTimelapse(nil, session, "Real Encode")
	if err != nil {
		t.Fatalf("assembleTimelapse: %v", err)
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() == 0 {
		t.Fatalf("no film at %s: %v", out, err)
	}

	// A readable duration proves the file is a real container, not a stub.
	if ffprobe := findLocalTool("ffprobe"); ffprobe != "" {
		cmd := exec.Command(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", out)
		raw, err := cmd.Output()
		if err != nil {
			t.Fatalf("ffprobe rejected the film: %v", err)
		}
		duration, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil || duration <= 0 {
			t.Fatalf("film duration = %q, want a positive number", raw)
		}
	}
}

func hasFFmpegArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
