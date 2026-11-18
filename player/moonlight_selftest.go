package main

// moonlight_selftest.go — "Check this handheld" (Moonlight).
//
// No PC is needed. JukaHub writes a session file with self_test=1 and hands
// the screen to moonlight-stream through the same tuned hand-off as a stream
// (psHandoffSpec.Tune). The helper opens its window, renderer, sound and
// pads, decodes an embedded 1280x720 H.264 clip and times it, buzzes the
// motor, waits for a press of A and writes its status file with st_* keys
// (native/README.md). At the next start that note becomes one toast and a
// device profile, DataDir/moonlight/device-check.json, and the measured
// decode time picks the quality a PC that was never given one streams at
// (mlRecommendedPreset, used by mlStreamSettings.Resolve): Balanced when a
// frame of the panel's size decodes and draws in 11 ms, Light when a 3/4-size
// frame does, Steady 30 otherwise.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mlSelfTestFileName  = "moonlight-selftest.txt"
	mlDeviceCheckName   = "device-check.json"
	mlDeviceCheckVer    = 1
	mlSelfTestLabel     = "Moonlight check"
	mlSelfTestResult    = "self_test"
	mlSelfTestKeyPrefix = "st_"
	mlSelfTestKeyMax    = 32
	mlSelfTestKeysMax   = 64
	mlSelfTestValueMax  = 64
	mlSelfTestClipW     = 1280
	mlSelfTestClipH     = 720
)

// mlSelfTestBudgetMS is the most one frame's decode and upload may take for
// 60 fps to stay smooth: 11 of a frame's 16.7 ms, the rest is the network,
// the sound and presenting. mlSelfTestLightShare is the Light preset's share
// of the panel's pixels (3/4 x 3/4).
const (
	mlSelfTestBudgetMS   = 11.0
	mlSelfTestLightShare = 0.5625
)

// mlSelfTestNoneNote is the PC list row's detail before the first check.
const mlSelfTestNoneNote = "Not checked yet — tests picture, sound, controls and decoding speed in about 20 s"

var errMLSelfTestHandheldOnly = errors.New("the Moonlight check runs on the handheld only")

func init() {
	// After the first frame, where a toast can show (startup.go).
	deferStartupTask("moonlight-self-test", mlShowSelfTest)
}

// mlSelfTestPath is the helper's note of a check, beside the stream's.
func mlSelfTestPath() string {
	return mlDataPath(mlSelfTestFileName)
}

// mlDeviceCheckPath is the device profile beside the client identity ("" when
// there is no data folder).
func mlDeviceCheckPath() string {
	dir := mlIdentityDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, mlDeviceCheckName)
}

// ── Starting a check ─────────────────────────────────────────────────────

// mlSelfTestSessionFile renders the check's session file: self_test=1 and
// what a stream would get for sound, the motor and the hints; nothing about
// a PC.
func mlSelfTestSessionFile(volume int, motor psStreamMotor, status string) string {
	var b strings.Builder
	line := func(key, value string) {
		value = psSessionValue(value)
		if value == "" {
			return
		}
		b.WriteString(key + "=" + value + "\n")
	}
	line("self_test", "1")
	line("volume", strconv.Itoa(volume))
	line("hints", "1")
	if motor.GPIO != "" {
		line("motor_gpio", motor.GPIO)
		if motor.Level != "" && motor.LevelMax > motor.LevelMin {
			line("motor_level", motor.Level)
			line("motor_level_min", strconv.Itoa(motor.LevelMin))
			line("motor_level_max", strconv.Itoa(motor.LevelMax))
		}
	}
	line("status_file", status)
	line("verbose", "0")
	return b.String()
}

// mlStartSelfTest hands the screen to moonlight-stream for the handheld
// check and asks JukaHub to exit (launch.sh brings it back when the check
// ends, in about 20 s). errMLHelperMissing: the helper is not installed
// (mlEnsureHelper). It blocks for the helper's preflight: call it from a
// goroutine.
func mlStartSelfTest() error {
	return mlStartSelfTestCtx(context.Background())
}

// mlStartSelfTestCtx is mlStartSelfTest that ctx can call off until the
// hand-off starts.
func mlStartSelfTestCtx(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !mlOnHandheld() {
		return errMLSelfTestHandheldOnly
	}
	helper := mlHelperPath()
	if helper == "" {
		return errMLHelperMissing
	}
	engine := psEngine{Stream: helper, Root: filepath.Dir(helper)}
	// A helper this firmware's loader would refuse is reported while JukaHub
	// is still on screen, as for a stream (psremote_preflight.go).
	psEnsureExecutable(helper)
	if problem := psStreamHelperCheckWait(engine); problem != "" {
		return errors.New(problem)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	status := mlSelfTestPath()
	path, err := mlWriteSessionFile(mlSelfTestSessionFile(mlDefaultVolume, psStreamMotorLookup(), status))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(path)
		return err
	}
	if f, err := os.OpenFile(mlLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
		logWarn("moonlight", "cannot write %s (%v); the check logs to %s", mlLogPath(), err, mlFallbackLogPath)
	} else {
		_ = f.Close()
	}
	mlMarkSelfTestStarted(status)
	spec := mlHandoffSpec()
	spec.Status = status
	spec.Banner = mlLogBanner + " check"
	if err := psStartHandoffWith(spec, engine, path, mlSelfTestLabel); err != nil {
		_ = os.Remove(path)
		_ = os.Remove(status)
		return err
	}
	logInfo("moonlight", "handed the screen to %s for the handheld check", mlHelperName)
	requestAppExit()
	return nil
}

// mlMarkSelfTestStarted leaves the "started" note the helper replaces, with
// how long moonlight.log is now (the check's lines come after that point).
func mlMarkSelfTestStarted(path string) {
	data := "version=1\nresult=" + mlRunStarted + "\n"
	if info, err := os.Stat(mlLogPath()); err == nil {
		data += "log_offset=" + strconv.FormatInt(info.Size(), 10) + "\n"
	} else if os.IsNotExist(err) {
		data += "log_offset=0\n"
	}
	if err := writeFileSynced(path, []byte(data), 0o600); err != nil {
		logWarn("moonlight", "could not leave the check's note: %v", err)
	}
}

// ── The helper's note ────────────────────────────────────────────────────

// mlSelfTestReport is one parsed check note: the helper's result keys, the
// hand-off script's handoff_step / handoff_log lines, JukaHub's log_offset,
// and every st_* value as written (cleaned and bounded).
type mlSelfTestReport struct {
	RC          int
	HaveRC      bool
	Result      string
	Message     string
	HandoffStep string
	HandoffLog  string
	LogOffset   int64
	Values      map[string]string
}

// checked reports whether the helper ran the check and said how it went.
func (r mlSelfTestReport) checked() bool {
	return r.Result == mlSelfTestResult || len(r.Values) > 0
}

// mlSelfTestKey reports whether key is a check value: st_ and lower-case
// letters, digits and underscores, bounded.
func mlSelfTestKey(key string) bool {
	if len(key) <= len(mlSelfTestKeyPrefix) || len(key) > mlSelfTestKeyMax || !strings.HasPrefix(key, mlSelfTestKeyPrefix) {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// mlParseSelfTest reads a check note. The last line of a key wins; unknown
// keys are ignored; a note with no result, exit code or check value is none.
func mlParseSelfTest(data []byte) (mlSelfTestReport, bool) {
	rep := mlSelfTestReport{LogOffset: -1, Values: map[string]string{}}
	for _, raw := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(raw), "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch {
		case key == "rc":
			if n, err := strconv.Atoi(value); err == nil {
				rep.RC, rep.HaveRC = n, true
			}
		case key == "result":
			rep.Result = strings.ToLower(value)
		case key == "message":
			rep.Message = mlTruncate(value, 160)
		case key == "handoff_step":
			rep.HandoffStep = mlTruncate(value, 120)
		case key == "handoff_log":
			// Only the script's own fallback, never an arbitrary file.
			if value == mlFallbackLogPath {
				rep.HandoffLog = value
			}
		case key == "log_offset":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
				rep.LogOffset = n
			}
		case mlSelfTestKey(key):
			if _, seen := rep.Values[key]; seen || len(rep.Values) < mlSelfTestKeysMax {
				rep.Values[key] = mlTruncate(value, mlSelfTestValueMax)
			}
		}
	}
	if rep.Result == "" && !rep.HaveRC && len(rep.Values) == 0 {
		return mlSelfTestReport{}, false
	}
	return rep, true
}

// ── The device profile ───────────────────────────────────────────────────

// mlDeviceCheck is the last check, as DataDir/moonlight/device-check.json
// keeps it: when, every st_* value the helper wrote, and what worked. The ok
// flags and the recommendation are for a reader of the file; JukaHub works
// them out again from Values.
type mlDeviceCheck struct {
	Version     int               `json:"version"`
	Time        time.Time         `json:"time"`
	Device      string            `json:"device,omitempty"`
	RC          int               `json:"rc"`
	Result      string            `json:"result,omitempty"`
	Message     string            `json:"message,omitempty"`
	Values      map[string]string `json:"values"`
	Picture     bool              `json:"picture_ok"`
	Sound       bool              `json:"sound_ok"`
	Controls    bool              `json:"controls_ok"`
	Rumble      bool              `json:"rumble_ok"`
	Engine      bool              `json:"engine_ok"`
	ScreenW     int               `json:"screen_w,omitempty"`
	ScreenH     int               `json:"screen_h,omitempty"`
	FrameMS     float64           `json:"frame_ms,omitempty"`
	Recommended string            `json:"recommended,omitempty"`
}

// mlDeviceCheckFrom makes the profile of a check that ran, for a panel of
// screenW x screenH.
func mlDeviceCheckFrom(rep mlSelfTestReport, at time.Time, screenW, screenH int) mlDeviceCheck {
	c := mlDeviceCheck{
		Version: mlDeviceCheckVer,
		Time:    at,
		RC:      rep.RC,
		Result:  rep.Result,
		Message: rep.Message,
		Values:  map[string]string{},
		ScreenW: screenW,
		ScreenH: screenH,
	}
	for k, v := range rep.Values {
		c.Values[k] = v
	}
	c.Picture = c.state("st_video", "st_decode") > 0
	c.Sound = c.state("st_audio", "st_opus") > 0
	c.Controls = c.state("st_pads", "st_input") > 0
	c.Rumble = c.state("st_motor") > 0
	c.Engine = c.state("st_core") > 0
	c.FrameMS = c.frameMS(screenW, screenH)
	c.Recommended = c.recommend(screenW, screenH)
	return c
}

// state is how the check went for the part keys stand for: 1 when every key
// says it worked, -1 when one says it did not, 0 when the helper did not say.
func (c mlDeviceCheck) state(keys ...string) int {
	all := true
	for _, k := range keys {
		v, ok := c.Values[k]
		switch {
		case !ok:
			all = false
		case v != "1" && !strings.EqualFold(v, "true") && !mlPositiveCount(v):
			// st_pads is a count: 2 (the built-in pad and a Bluetooth one)
			// is as good as 1.
			return -1
		}
	}
	if all {
		return 1
	}
	return 0
}

// mlPositiveCount reports whether v is a whole number above zero.
func mlPositiveCount(v string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	return err == nil && n > 0
}

// num is a check's measurement (0 when missing or not a sane number).
func (c mlDeviceCheck) num(key string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(c.Values[key]), 64)
	if err != nil || !(f >= 0 && f < 1e5) {
		return 0
	}
	return f
}

// frameMS is one frame's decode and upload time on a panel of screenW x
// screenH (0 when the clip did not decode).
func (c mlDeviceCheck) frameMS(screenW, screenH int) float64 {
	if c.state("st_video", "st_decode") <= 0 {
		return 0
	}
	return mlSelfTestFrameMS(c.num("st_p_ms"), c.num("st_upload_ms"), screenW, screenH)
}

// recommend is the preset this check picks for screenW x screenH ("" when it
// measured nothing usable).
func (c mlDeviceCheck) recommend(screenW, screenH int) string {
	if c.state("st_video", "st_decode") <= 0 {
		return ""
	}
	return mlRecommendFor(c.num("st_p_ms"), c.num("st_upload_ms"), screenW, screenH)
}

// mlSelfTestFrameMS scales the clip's per-frame decode + upload time (ms,
// 1280x720) to a panel of screenW x screenH (1280x720 when unknown).
func mlSelfTestFrameMS(pMS, uploadMS float64, screenW, screenH int) float64 {
	if pMS <= 0 {
		return 0
	}
	if uploadMS < 0 {
		uploadMS = 0
	}
	if screenW <= 0 || screenH <= 0 {
		screenW, screenH = mlDefaultScreenW, mlDefaultScreenH
	}
	return (pMS + uploadMS) * float64(screenW*screenH) / float64(mlSelfTestClipW*mlSelfTestClipH)
}

// mlRecommendFor is the preset a P-frame decode of pMS and an upload of
// uploadMS (the clip's 1280x720) call for on screenW x screenH: Balanced when
// a frame takes at most mlSelfTestBudgetMS, Light when a 3/4-size frame does,
// Steady 30 otherwise; "" without a measurement. Pure.
func mlRecommendFor(pMS, uploadMS float64, screenW, screenH int) string {
	ms := mlSelfTestFrameMS(pMS, uploadMS, screenW, screenH)
	switch {
	case ms <= 0:
		return ""
	case ms <= mlSelfTestBudgetMS:
		return mlPresetBalanced
	case ms*mlSelfTestLightShare <= mlSelfTestBudgetMS:
		return mlPresetLight
	}
	return mlPresetSteady30
}

// mlDeviceCheckState keeps the last check in memory: read from the file once,
// replaced when a check comes back. Resolve reads it on the render thread.
var mlDeviceCheckState struct {
	mu     sync.Mutex
	loaded bool
	have   bool
	check  mlDeviceCheck
}

// mlDeviceCheckGet is the last check, if any.
func mlDeviceCheckGet() (mlDeviceCheck, bool) {
	mlDeviceCheckState.mu.Lock()
	defer mlDeviceCheckState.mu.Unlock()
	if !mlDeviceCheckState.loaded {
		mlDeviceCheckState.loaded = true
		if path := mlDeviceCheckPath(); path != "" {
			var c mlDeviceCheck
			if _, err := readJSONWithBackup(path, &c); err == nil && c.Version >= 1 && len(c.Values) > 0 {
				mlDeviceCheckState.check, mlDeviceCheckState.have = c, true
			}
		}
	}
	return mlDeviceCheckState.check, mlDeviceCheckState.have
}

func mlDeviceCheckSet(c mlDeviceCheck) {
	mlDeviceCheckState.mu.Lock()
	mlDeviceCheckState.check, mlDeviceCheckState.have, mlDeviceCheckState.loaded = c, true, true
	mlDeviceCheckState.mu.Unlock()
}

// mlSaveDeviceCheck writes the profile atomically, 0600, in the identity's
// 0700 folder.
func mlSaveDeviceCheck(c mlDeviceCheck) error {
	path := mlDeviceCheckPath()
	if path == "" {
		return errors.New("no data folder")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o600)
}

// mlRecommendedPreset is the preset the handheld check picked for a panel of
// screenW x screenH, "" when no check measured one (or off the handheld).
// Cheap after the first call: the profile stays in memory.
func mlRecommendedPreset(screenW, screenH int) string {
	if !mlOnHandheld() {
		return ""
	}
	c, ok := mlDeviceCheckGet()
	if !ok {
		return ""
	}
	if screenW <= 0 || screenH <= 0 {
		screenW, screenH = mlDefaultScreenW, mlDefaultScreenH
	}
	return c.recommend(screenW, screenH)
}

// ── What the user reads ──────────────────────────────────────────────────

// findings splits a check into the parts that worked and the problems, in
// the user's words (a part the helper did not report is in neither).
func (c mlDeviceCheck) findings() (works, problems []string) {
	switch c.state("st_video", "st_decode") {
	case 1:
		works = append(works, "picture")
	case -1:
		if c.state("st_video") < 0 {
			problems = append(problems, "the picture did not open")
		} else {
			problems = append(problems, "the test video did not decode")
		}
	}
	switch c.state("st_audio", "st_opus") {
	case 1:
		works = append(works, "sound")
	case -1:
		if c.state("st_audio") < 0 {
			problems = append(problems, "no sound")
		} else {
			problems = append(problems, "no sound decoder")
		}
	}
	switch c.state("st_pads", "st_input") {
	case 1:
		works = append(works, "controls")
	case -1:
		if c.state("st_pads") < 0 {
			problems = append(problems, "no controller was found")
		} else {
			problems = append(problems, "no button press came")
		}
	}
	switch c.state("st_motor") {
	case 1:
		works = append(works, "rumble")
	case -1:
		problems = append(problems, "no rumble")
	}
	if c.state("st_core") < 0 {
		problems = append(problems, "the streaming engine is missing")
	}
	return works, problems
}

// mlJoinAnd is "a", "a and b", "a, b and c".
func mlJoinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// mlWorkVerb is " works" after one part, " work" after several.
func mlWorkVerb(n int) string {
	if n == 1 {
		return " works"
	}
	return " work"
}

// mlCapitalize upper-cases an ASCII first letter.
func mlCapitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// mlResLabel names a frame size: "720p" for 1280x720, else "W×H".
func mlResLabel(w, h int) string {
	switch {
	case w == 1280 && h == 720:
		return "720p"
	case w == 1920 && h == 1080:
		return "1080p"
	}
	return strconv.Itoa(w) + "×" + strconv.Itoa(h)
}

// verdict is the speed sentence for screenW x screenH ("" when the check
// measured nothing).
func (c mlDeviceCheck) verdict(screenW, screenH int) string {
	if screenW <= 0 || screenH <= 0 {
		screenW, screenH = mlDefaultScreenW, mlDefaultScreenH
	}
	rec := c.recommend(screenW, screenH)
	if rec == "" {
		return ""
	}
	ms := strconv.FormatFloat(c.frameMS(screenW, screenH), 'f', 1, 64)
	frame := "a " + mlResLabel(screenW, screenH) + " frame takes " + ms + " ms here"
	w, h, fps, _ := mlStreamSettings{Preset: rec}.Resolve(screenW, screenH)
	switch rec {
	case mlPresetBalanced:
		return frame + ", so Balanced (" + strconv.Itoa(fps) + " fps) will be smooth"
	case mlPresetLight:
		return frame + ", so PCs on Auto quality stream Light (" + strconv.Itoa(w) + "×" + strconv.Itoa(h) + ", " +
			strconv.Itoa(fps) + " fps)"
	}
	return frame + ", so PCs on Auto quality stream Steady 30 (" + strconv.Itoa(fps) + " fps)"
}

// mlSelfTestSummary is the toast a check becomes on a panel of screenW x
// screenH, and its kind. Pure.
func mlSelfTestSummary(c mlDeviceCheck, screenW, screenH int) (string, ToastKind) {
	works, problems := c.findings()
	var b strings.Builder
	b.WriteString("Moonlight check: ")
	switch {
	case len(works) > 0 && len(problems) > 0:
		b.WriteString(mlJoinAnd(works) + mlWorkVerb(len(works)) + ", but " + mlJoinAnd(problems))
	case len(works) > 0:
		b.WriteString(mlJoinAnd(works) + mlWorkVerb(len(works)))
	case len(problems) > 0:
		b.WriteString(mlJoinAnd(problems))
	case c.Message != "":
		b.WriteString(strings.TrimSuffix(c.Message, "."))
	default:
		b.WriteString("the helper reported no results")
	}
	picture := c.state("st_video", "st_decode")
	if v := c.verdict(screenW, screenH); v != "" {
		b.WriteString(" — " + v + ".")
	} else if picture < 0 {
		b.WriteString(". Streaming needs the picture; the details are in " + mlLastRunLogHint + ".")
	} else {
		b.WriteString(".")
	}
	switch {
	case picture < 0 || c.state("st_core") < 0:
		return b.String(), ToastKindError
	case len(problems) > 0 || picture == 0:
		return b.String(), ToastKindWarning
	}
	return b.String(), ToastKindSuccess
}

// mlSelfTestChecked reports whether the handheld was checked.
func mlSelfTestChecked() bool {
	_, ok := mlDeviceCheckGet()
	return ok
}

// mlSelfTestNote is the PC list row's detail for "Check this handheld", and
// whether the last check found everything working.
func mlSelfTestNote() (text string, ok bool) {
	c, have := mlDeviceCheckGet()
	if !have {
		return mlSelfTestNoneNote, false
	}
	sw, sh := mlScreenSize()
	works, problems := c.findings()
	var parts []string
	switch {
	case len(problems) > 0:
		parts = append(parts, mlCapitalize(mlJoinAnd(problems)))
	case len(works) > 0:
		parts = append(parts, mlCapitalize(mlJoinAnd(works))+mlWorkVerb(len(works)))
	}
	if rec := c.recommend(sw, sh); rec != "" {
		ms := strconv.FormatFloat(c.frameMS(sw, sh), 'f', 1, 64)
		parts = append(parts, mlResLabel(sw, sh)+" in "+ms+" ms a frame → Auto: "+mlPresetLabel(rec))
	}
	if len(parts) == 0 {
		parts = append(parts, "The last check reported no results")
	}
	return strings.Join(parts, " · "), len(problems) == 0 && c.state("st_video", "st_decode") > 0
}

// ── At the next start ────────────────────────────────────────────────────

// mlShowSelfTest reads the check's note once, saves the device profile and
// shows one toast. Main thread, at startup; it also loads the profile, so
// Resolve on the render thread never reads the card.
func mlShowSelfTest() {
	mlDeviceCheckGet()
	path := mlSelfTestPath()
	data, err := mlReadSmall(path, mlLastRunFileMax)
	if err != nil {
		if !os.IsNotExist(err) {
			_ = os.Remove(path)
			logWarn("moonlight", "the handheld check's note could not be read: %v", err)
		}
		return
	}
	_ = os.Remove(path)
	rep, ok := mlParseSelfTest(data)
	if !ok {
		logWarn("moonlight", "the handheld check's note was empty")
		ShowToast("The Moonlight check left no result. The details are in "+mlLastRunLogHint+".", ToastKindWarning)
		return
	}
	if !rep.checked() {
		msg := mlSelfTestNotRun(rep)
		logWarn("moonlight", "handheld check did not run: result=%s rc=%d step=%q", rep.Result, rep.RC, rep.HandoffStep)
		ShowToast(msg, ToastKindError)
		return
	}
	// Start + Select before the first decode pass ends the check with rc 0,
	// a working screen and no decode result: stopped, not failed. The last
	// good check (and the Auto quality it set) stays.
	if rep.RC == 0 && rep.Values["st_video"] == "1" && rep.Values["st_decode"] == "0" {
		logInfo("moonlight", "handheld check stopped before decoding: %s", mlSelfTestValuesLine(rep.Values))
		ShowToast("The Moonlight check was stopped before it measured anything. Run it again from PC streaming.", ToastKindInfo)
		return
	}
	sw, sh := mlScreenSize()
	c := mlDeviceCheckFrom(rep, time.Now(), sw, sh)
	c.Device = deviceDisplayName(DetectDevice())
	if err := mlSaveDeviceCheck(c); err != nil {
		logWarn("moonlight", "could not save the handheld check: %v", err)
	}
	mlDeviceCheckSet(c)
	logInfo("moonlight", "handheld check: rc=%d result=%s %s -> %q", rep.RC, rep.Result, mlSelfTestValuesLine(rep.Values), c.Recommended)
	msg, kind := mlSelfTestSummary(c, sw, sh)
	ShowToast(msg, kind)
}

// mlSelfTestValuesLine is the check's values as one sorted log line.
func mlSelfTestValuesLine(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, values[k]))
	}
	return strings.Join(parts, " ")
}

// mlSelfTestNotRun is the toast for a note without check results: the
// hand-off stopped (with its last step), the helper died, or a helper older
// than the check refused the session.
func mlSelfTestNotRun(rep mlSelfTestReport) string {
	where := "The details are in " + mlLastRunLogHint + "."
	logPath, offset := mlLogPath(), rep.LogOffset
	if rep.HandoffLog != "" {
		// The card's log could not be written: the script used its fallback.
		logPath, offset = rep.HandoffLog, -1
		where = "The details are in " + rep.HandoffLog + "."
	}
	result, rc := rep.Result, rep.RC
	if result == "" && rep.HaveRC {
		result = mlLastRunResultForRC(rc)
	}
	if result == mlRunStarted || result == "" {
		// The helper never wrote: the hand-off's lines in the log say how far
		// it got.
		run := mlLastRunFromLog(mlLastRun{}, psLastRunLogText(logPath, offset))
		result, rc = run.Result, run.RC
	}
	switch result {
	case mlRunNotStarted:
		if rep.HandoffStep != "" {
			return "The Moonlight check did not run — the hand-off stopped at: " + strings.TrimSuffix(rep.HandoffStep, ".") + ". " + where
		}
		return "The Moonlight check did not run. " + where
	case mlRunInvalidSession:
		return "The Moonlight check did not run: this handheld's moonlight-stream is older than the check. " + where
	case mlRunCrashed:
		if rc > 0 {
			return "The Moonlight check stopped unexpectedly (code " + strconv.Itoa(rc) + "). " + where
		}
		return "The Moonlight check stopped unexpectedly. " + where
	}
	if rep.Message != "" {
		return "The Moonlight check failed: " + strings.TrimSuffix(rep.Message, ".") + ". " + where
	}
	return "The Moonlight check ended without a result (code " + strconv.Itoa(rc) + "). " + where
}
