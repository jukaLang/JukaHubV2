package main

// timelapse.go — one frame every few seconds of a playthrough.
//
// The hub launches emulators detached and never sees their pixels: the emulator
// owns the framebuffer and JukaHub is only waiting for the process to exit, so a
// playthrough time-lapse cannot be recorded from our renderer. It is recorded
// from the device framebuffer instead: every interval a short ffmpeg grab reads
// /dev/fb0 into one PNG, and when the game exits the bundled ffmpeg assembles
// those stills into a short mp4 next to the data folder.
//
// Nothing here talks to the emulator, so any child that draws to the panel is
// recorded, standalone emulators included. Where the framebuffer cannot be read
// (desktop builds, or a firmware that keeps the panel behind a compositor) the
// session never starts and the launch is left untouched.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// timelapseDirName is the folder under the data directory that holds the
	// finished films. Session frames live in a dot-prefixed child, so an
	// interrupted session is never mistaken for a finished time-lapse.
	timelapseDirName = "timelapses"

	// timelapseConfigKey / timelapseIntervalKey are the user switches:
	// `timelapse: false` turns recording off, and `timelapse_interval` is the
	// number of seconds between frames (clamped below).
	timelapseConfigKey   = "timelapse"
	timelapseIntervalKey = "timelapse_interval"

	timelapseDefaultEvery = 20 * time.Second
	timelapseMinEvery     = 3 * time.Second
	timelapseMaxEvery     = 5 * time.Minute

	// timelapseMaxFrames bounds one film. 240 stills at the default interval
	// is eighty minutes of play, ten seconds of video and roughly 6 MB of
	// frames on the card.
	timelapseMaxFrames = 240
	// timelapseMinFrames is where an encode is worth its CPU. A game opened
	// and closed in under a minute leaves nothing behind.
	timelapseMinFrames = 4
	timelapseOutputFPS = 24

	// timelapseFrameWidth keeps each still small enough for a 1 GB device.
	timelapseFrameWidth = 640

	// timelapseGrabTimeout bounds one grab so a wedged capture can never hold
	// the session open.
	timelapseGrabTimeout = 15 * time.Second
	// timelapseEncodeTimeout bounds one assembly. A wedged ffmpeg used to run
	// forever while holding timelapseEncodeMu, which stalled every later
	// session's Stop behind it. Ten minutes is far longer than any real film
	// needs on the handheld's CPU.
	timelapseEncodeTimeout = 10 * time.Minute
	// timelapseFBDevice is the panel framebuffer on the handheld firmware.
	timelapseFBDevice = "/dev/fb0"
)

// timelapseSettings is the resolved configuration for one session.
type timelapseSettings struct {
	enabled   bool
	every     time.Duration
	maxFrames int
}

// timelapseSettingsFor reads the user switches. Recording is on by default on
// the handheld: it is invisible, and the capture path itself is what decides
// whether it can run at all.
func timelapseSettingsFor(config *Config) timelapseSettings {
	settings := timelapseSettings{enabled: true, every: timelapseDefaultEvery, maxFrames: timelapseMaxFrames}
	if config == nil {
		return settings
	}
	custom := config.Variables.Custom
	settings.enabled = customBool(custom, timelapseConfigKey, settings.enabled)
	if raw := strings.TrimSpace(customStringValue(custom, timelapseIntervalKey)); raw != "" {
		if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 {
			settings.every = time.Duration(seconds * float64(time.Second))
		}
	}
	if settings.every < timelapseMinInterval {
		settings.every = timelapseMinInterval
	}
	if settings.every > timelapseMaxInterval {
		settings.every = timelapseMaxInterval
	}
	return settings
}

// Seams for tests: the capture command, the encoder, the runner, the clock, the
// interval bounds and the output root are all replaceable, so the whole pipeline
// runs on a machine with no framebuffer, no ffmpeg and no minute-long waits.
var (
	timelapseRootOverride string
	timelapseNow          = time.Now
	timelapseMinInterval  = timelapseMinEvery
	timelapseMaxInterval  = timelapseMaxEvery
	timelapseCapture      = defaultTimelapseCapture
	timelapseEncoder      = defaultTimelapseEncoder
	timelapseGrab         = func(prog string, args []string) error {
		return runQuietCommand(prog, args, timelapseGrabTimeout)
	}
	timelapseEncode = func(prog string, args []string) error {
		return runQuietCommand(prog, args, timelapseEncodeTimeout)
	}
	timelapseNotify = queueTimelapseNotice
)

// timelapseDeps is the set of helpers one session uses. They are read once,
// when the session starts, and carried on the recorder: the capture goroutine
// then never touches the seam variables above, which other code (and the tests)
// may replace at any time.
type timelapseDeps struct {
	capture func(*Config, string) (string, []string, error)
	grab    func(string, []string) error
	encoder func(*Config) (string, error)
	encode  func(string, []string) error
	root    func() string
	now     func() time.Time
	notify  func(string, ToastKind)
}

// timelapseDepsNow takes a consistent copy of the current helpers.
func timelapseDepsNow() timelapseDeps {
	return timelapseDeps{
		capture: timelapseCapture,
		grab:    timelapseGrab,
		encoder: timelapseEncoder,
		encode:  timelapseEncode,
		root:    timelapseRootDir,
		now:     timelapseNow,
		notify:  timelapseNotify,
	}
}

// timelapseEncodeMu serialises assembly: two games finishing at once would
// otherwise compete for the encoder on a small CPU.
var timelapseEncodeMu sync.Mutex

// A film is assembled on the goroutine that reaped the game, but the toast
// belongs to the render thread. The notice is queued under a lock and shown by
// timelapseDrainNotice from the frame loop, so a finishing session never writes
// UI state from the wrong thread.
var timelapseNoticeMu sync.Mutex
var timelapseNotice struct {
	message string
	kind    ToastKind
}

// queueTimelapseNotice parks a completion notice for the next frame.
func queueTimelapseNotice(message string, kind ToastKind) {
	timelapseNoticeMu.Lock()
	timelapseNotice.message, timelapseNotice.kind = message, kind
	timelapseNoticeMu.Unlock()
}

// timelapseDrainNotice shows a queued notice. It runs on the render thread.
func timelapseDrainNotice() {
	timelapseNoticeMu.Lock()
	message, kind := timelapseNotice.message, timelapseNotice.kind
	timelapseNotice.message = ""
	timelapseNoticeMu.Unlock()
	if message != "" {
		ShowToast(message, kind)
	}
}

// defaultTimelapseCapture builds the single-frame grab. A frame is downscaled
// while it is read, so the card holds thumbnails rather than screenshots.
func defaultTimelapseCapture(config *Config, framePath string) (string, []string, error) {
	if runtime.GOOS != "linux" {
		return "", nil, errors.New("framebuffer capture needs the handheld build")
	}
	if _, err := os.Stat(timelapseFBDevice); err != nil {
		return "", nil, fmt.Errorf("%s is not readable: %w", timelapseFBDevice, err)
	}
	ffmpeg, err := timelapseEncoder(config)
	if err != nil {
		return "", nil, err
	}
	return ffmpeg, []string{
		"-loglevel", "error",
		"-f", "fbdev", "-i", timelapseFBDevice,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:-2", timelapseFrameWidth),
		"-y", framePath,
	}, nil
}

// defaultTimelapseEncoder resolves the bundled ffmpeg without downloading it:
// a launch must never wait on a network install.
func defaultTimelapseEncoder(config *Config) (string, error) {
	path := getToolPath("ffmpeg", config)
	if strings.TrimSpace(path) == "" {
		return "", errors.New("ffmpeg is not installed")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", errors.New("ffmpeg is not installed")
	}
	return path, nil
}

// runQuietCommand runs one short helper command and folds its last error line
// into the returned error. stdout is discarded; stderr is bounded by the
// encoder's own `-loglevel error`.
func runQuietCommand(prog string, args []string, timeout time.Duration) error {
	ctx := context.Background()
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Stdout = io.Discard
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			if len(detail) > 400 {
				detail = detail[len(detail)-400:]
			}
			return fmt.Errorf("%v (%s)", err, detail)
		}
		return err
	}
	return nil
}

// timelapseRootDir is the folder the finished films live in.
func timelapseRootDir() string {
	if dir := strings.TrimSpace(timelapseRootOverride); dir != "" {
		return dir
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, timelapseDirName)
}

// timelapseSlug turns a game title into a filename-safe stem.
func timelapseSlug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 48 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "game"
	}
	return slug
}

// timelapseFilmName is the finished file's name.
func timelapseFilmName(title string, now time.Time) string {
	return "timelapse_" + timelapseSlug(title) + "_" + now.Format("20060102-150405") + ".mp4"
}

// timelapseRecorder is one running session. Frames are captured by a single
// goroutine in order, so the encoder always sees frame_0000..frame_N.
type timelapseRecorder struct {
	settings timelapseSettings
	title    string
	dir      string

	frames []string
	warned bool

	// deps is fixed for the session, so the goroutine below can use it
	// without synchronising against the package-level seams.
	deps timelapseDeps

	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

// startTimelapse begins recording the panel for a launch. It returns nil when
// the feature is off, the framebuffer cannot be read, or the data folder is
// unavailable, so callers can invoke it unconditionally and pass the result to
// Stop, which tolerates nil.
func startTimelapse(config *Config, title string) *timelapseRecorder {
	settings := timelapseSettingsFor(config)
	if !settings.enabled {
		return nil
	}
	// Probe the capture once. A machine without a readable framebuffer must not
	// start a ticker that fails every twenty seconds for the whole session.
	if _, _, err := timelapseCapture(config, filepath.Join(os.TempDir(), "timelapse-probe.png")); err != nil {
		log.Printf("[timelapse] not recording: %v", err)
		return nil
	}
	root := timelapseRootDir()
	if root == "" {
		return nil
	}
	dir := filepath.Join(root, "."+timelapseSlug(title)+"-"+timelapseNow().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[timelapse] cannot create %s: %v", dir, err)
		return nil
	}
	recorder := &timelapseRecorder{
		settings: settings,
		title:    title,
		dir:      dir,
		deps:     timelapseDepsNow(),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	go recorder.run(config)
	return recorder
}

// run captures immediately (the title screen is the best single frame), then
// once per interval until Stop. Once the cap is reached it simply waits, so a
// long session still produces one complete film rather than several.
func (r *timelapseRecorder) run(config *Config) {
	defer close(r.doneCh)
	for {
		if len(r.frames) < r.settings.maxFrames {
			r.capture(config)
		}
		wait := time.NewTimer(r.settings.every)
		select {
		case <-r.stopCh:
			wait.Stop()
			r.finish(config)
			return
		case <-wait.C:
		}
	}
}

// capture grabs one frame. A failed grab is logged once and then ignored: the
// game keeps running and the rest of the session may still be captured.
func (r *timelapseRecorder) capture(config *Config) {
	name := fmt.Sprintf("frame_%04d.png", len(r.frames))
	path := filepath.Join(r.dir, name)
	prog, args, err := r.deps.capture(config, path)
	if err != nil {
		r.warn(err)
		return
	}
	if err := r.deps.grab(prog, args); err != nil {
		r.warn(err)
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		r.warn(errors.New("capture produced no frame"))
		return
	}
	r.frames = append(r.frames, name)
}

func (r *timelapseRecorder) warn(err error) {
	if r.warned || err == nil {
		return
	}
	r.warned = true
	log.Printf("[timelapse] capture failed: %v", err)
}

// Stop ends the session and waits for the film to be assembled. It is safe to
// call more than once, and on a nil recorder.
func (r *timelapseRecorder) Stop() {
	if r == nil {
		return
	}
	r.once.Do(func() { close(r.stopCh) })
	<-r.doneCh
}

// finish assembles the film, or cleans the session up when it was too short.
func (r *timelapseRecorder) finish(config *Config) {
	if len(r.frames) < timelapseMinFrames {
		if err := os.RemoveAll(r.dir); err != nil {
			log.Printf("[timelapse] cannot clean %s: %v", r.dir, err)
		}
		return
	}
	out, err := assembleTimelapseWith(r.deps, config, r.dir, r.title)
	if err != nil {
		// Keep the frames: they are the only copy of the session, and a later
		// build may be able to assemble them.
		log.Printf("[timelapse] encode failed for %q: %v (frames kept in %s)", r.title, err, r.dir)
		r.notify("Time-lapse could not be encoded", ToastKindWarning)
		return
	}
	if err := os.RemoveAll(r.dir); err != nil {
		log.Printf("[timelapse] cannot clean %s: %v", r.dir, err)
	}
	log.Printf("[timelapse] %q ready: %s (%d frames)", r.title, out, len(r.frames))
	r.notify("Time-lapse ready: "+filepath.Base(out), ToastKindSuccess)
}

func (r *timelapseRecorder) notify(message string, kind ToastKind) {
	if r.deps.notify != nil {
		r.deps.notify(message, kind)
	}
}

// assembleTimelapse encodes the session's frames into one mp4. 24 fps keeps the
// file short; each still stands for the seconds between captures.
func assembleTimelapse(config *Config, dir, title string) (string, error) {
	return assembleTimelapseWith(timelapseDepsNow(), config, dir, title)
}

// assembleTimelapseWith is the assembly half, run against one session's helpers.
func assembleTimelapseWith(deps timelapseDeps, config *Config, dir, title string) (string, error) {
	ffmpeg, err := deps.encoder(config)
	if err != nil {
		return "", err
	}
	root := deps.root()
	if root == "" {
		return "", errors.New("no data folder for time-lapses")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(root, timelapseFilmName(title, deps.now()))
	args := []string{
		"-loglevel", "error",
		"-framerate", strconv.Itoa(timelapseOutputFPS),
		"-i", filepath.Join(dir, "frame_%04d.png"),
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "23",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		"-y", out,
	}
	timelapseEncodeMu.Lock()
	err = deps.encode(ffmpeg, args)
	timelapseEncodeMu.Unlock()
	if err != nil {
		_ = os.Remove(out)
		return "", err
	}
	return out, nil
}
