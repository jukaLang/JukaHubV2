package main

// tts_phonon_install.go — the natural offline voice for the TrimUI handhelds:
// Gradium's Phonon 31M English model (Apache-2.0, October 2026) behind the
// speech server of xn-ptts, which sounds far more like a person than Piper's
// Kristin and streams its sound in 80 ms frames.
//
// It is optional and comes second. Piper stays the offline voice every
// handheld gets first (tts_install.go): the natural voice is a separate
// download (JukaHub-Voice-linux-arm64.zip, built for the Cortex-A53 by
// .github/scripts/build-voice.sh and installed from the newest JukaHub release
// that carries it, relpack.go), fetched by itself once Piper is in, or from
// Settings → Voice & AI → Natural offline voice.
//
// Whether the A133P is quick enough for it is measured, not assumed: the
// install's probe starts the server from the unpacked package and times a
// sentence. Slower than ttsPhononMaxRTF seconds of work per second of speech
// and the package is refused (Piper stays, and the startup download does not
// come back: ttsPhononSlowMark says why); otherwise the measured speed is kept
// (ttsPhononSpeedName) and shown in Settings. Once installed it is the
// device's voice (ttsPhononVoice) unless the user turned it off
// (ttsPhononOffMark, Settings → the same row), it failed twice this run, or
// memory is short — each of those leaves Piper speaking (tts_phonon.go).
//
// The package says how to start it (voice.json, written by the build from the
// command line its offline smoke run used), so the server's arguments live in
// one place and a newer build can change them without a JukaHub update.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ttsPhononPackKey        = "voice-phonon"
	ttsPhononPackAsset      = "JukaHub-Voice-linux-arm64.zip"
	ttsPhononDirName        = "juka-voice"
	ttsPhononManifestName   = "voice.json"
	ttsPhononInstallTimeout = 40 * time.Minute
	// Beside the folder, in required/: the user chose Piper instead; this
	// device cannot run the natural voice (the probe's reason inside); the
	// speed the probe measured.
	ttsPhononOffMark   = ".juka-voice-off"
	ttsPhononSlowMark  = ".juka-voice-unsupported"
	ttsPhononSpeedName = ".juka-voice-speed"
	// ttsPhononMaxRTF is the most work per second of speech the probe
	// accepts. Sentences render one ahead of the one playing, so up to 1.0
	// speech flows; a little slower leaves short pauses between sentences,
	// which still beats Piper's voice for most people; much slower would be
	// waiting.
	ttsPhononMaxRTF = 1.4
	// ttsPhononDefaultMB is what a loaded server is taken to need when the
	// package does not say (voice.json's rss_mb, measured by the build).
	ttsPhononDefaultMB = 220
	// ttsPhononLowWaterMB stays free beside a loaded server (Piper's mark).
	ttsPhononLowWaterMB = 60
)

// ttsPhononManifest is voice.json.
type ttsPhononManifest struct {
	Engine   string            `json:"engine"`
	Name     string            `json:"name"`
	Version  string            `json:"version"`
	Server   string            `json:"server"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	AddrFlag string            `json:"addr_flag"`
	Model    string            `json:"model"`
	Format   string            `json:"format"`
	Rate     int               `json:"rate"`
	Female   string            `json:"female"`
	Male     string            `json:"male"`
	RSSMB    int               `json:"rss_mb"`
	CIRTF    float64           `json:"ci_rtf"`
}

// ttsPhononEnvKey is a variable voice.json may set: none that would change
// what the loader runs (LD_*, PATH), nothing odd.
var ttsPhononEnvKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,47}$`)

// ttsPhononParseManifest checks voice.json's content. Pure.
func ttsPhononParseManifest(data []byte) (ttsPhononManifest, error) {
	var m ttsPhononManifest
	if len(data) > 64<<10 {
		return m, errors.New("voice.json is too large")
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("voice.json: %w", err)
	}
	switch {
	case m.Engine != "phonon":
		return m, fmt.Errorf("voice.json: engine %q is not phonon", m.Engine)
	case m.Server == "" || m.Server != filepath.Base(m.Server) || strings.ContainsAny(m.Server, `/\`) || m.Server == "." || m.Server == "..":
		return m, fmt.Errorf("voice.json: server %q is not a file name", m.Server)
	case len(m.Args) > 32:
		return m, errors.New("voice.json: too many arguments")
	case !strings.HasPrefix(m.AddrFlag, "-") || len(m.AddrFlag) > 32 || strings.ContainsAny(m.AddrFlag, " =\x00"):
		return m, fmt.Errorf("voice.json: address flag %q", m.AddrFlag)
	case m.Format != "pcm":
		return m, fmt.Errorf("voice.json: format %q is not pcm", m.Format)
	case m.Rate < 8000 || m.Rate > 48000:
		return m, fmt.Errorf("voice.json: sample rate %d", m.Rate)
	case m.RSSMB < 0 || m.RSSMB > 900:
		return m, fmt.Errorf("voice.json: memory %d MB", m.RSSMB)
	}
	for _, a := range m.Args {
		if len(a) > 512 || strings.ContainsRune(a, 0) {
			return m, errors.New("voice.json: an argument is too long")
		}
	}
	for k, v := range m.Env {
		if !ttsPhononEnvKey.MatchString(k) || strings.HasPrefix(k, "LD_") || k == "PATH" || len(v) > 512 || strings.ContainsRune(v, 0) {
			return m, fmt.Errorf("voice.json: variable %q is not allowed", k)
		}
	}
	for _, v := range []string{m.Female, m.Male, m.Model} {
		if len(v) > 64 || strings.ContainsAny(v, "\"\\\x00\n") {
			return m, errors.New("voice.json: a voice or model name is malformed")
		}
	}
	return m, nil
}

// ttsPhononExpand fills voice.json's placeholders: {dir} the installed
// folder, {tmp} a private scratch folder. Pure.
func ttsPhononExpand(s, dir, tmp string) string {
	return strings.NewReplacer("{dir}", dir, "{tmp}", tmp).Replace(s)
}

// ttsPhononReadManifest reads and checks dir's voice.json.
func ttsPhononReadManifest(dir string) (ttsPhononManifest, error) {
	f, err := os.Open(filepath.Join(dir, ttsPhononManifestName))
	if err != nil {
		return ttsPhononManifest{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil {
		return ttsPhononManifest{}, err
	}
	m, err := ttsPhononParseManifest(data)
	if err != nil {
		return m, err
	}
	if info, err := os.Stat(filepath.Join(dir, m.Server)); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return m, errors.New("the voice's server is missing")
	}
	return m, nil
}

// ttsPhononNeedMB is the free memory a server of m needs to start.
func ttsPhononNeedMB(m ttsPhononManifest) uint64 {
	need := m.RSSMB
	if need <= 0 {
		need = ttsPhononDefaultMB
	}
	return uint64(need + need/5) // the build measured another CPU's process
}

// ttsPhononVoiceFor picks the package's voice for the gender of the chosen
// Groq voice (a woman's voice when there is none). Pure.
func ttsPhononVoiceFor(m ttsPhononManifest, gender string) string {
	if gender == "male" && m.Male != "" {
		return m.Male
	}
	return m.Female
}

func ttsPhononPath() string {
	req := P().RequiredDir()
	if req == "" {
		return ""
	}
	return filepath.Join(req, ttsPhononDirName)
}

// ttsPhononMarkPath is one of the marks beside the folder ("" without one).
func ttsPhononMarkPath(name string) string {
	req := P().RequiredDir()
	if req == "" {
		return ""
	}
	return filepath.Join(req, name)
}

func ttsPhononMarked(name string) bool {
	p := ttsPhononMarkPath(name)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// ttsPhononSlowInfo reads ttsPhononSlowMark: why the natural voice was
// refused, and — when it was a build that would not start rather than a CPU
// too slow — the release that build came from (a newer release is tried).
func ttsPhononSlowInfo() (why, release string, ok bool) {
	p := ttsPhononMarkPath(ttsPhononSlowMark)
	if p == "" {
		return "", "", false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", "", false
	}
	if len(data) > 600 {
		data = data[:600]
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	why = strings.TrimSpace(lines[0])
	if len(lines) > 1 {
		release = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[1]), "release "))
	}
	return why, release, true
}

// ttsPhononTooSlow reports whether this device is known to be too slow for
// the natural voice: the probe or a first use measured it, or a refusal
// that was not about one build.
func ttsPhononTooSlow() bool {
	if ttsPhononSlowNow.Load() {
		return true
	}
	if rtf, ok := ttsPhononSpeed(); ok && rtf > ttsPhononMaxRTF {
		return true
	}
	_, release, ok := ttsPhononSlowInfo()
	return ok && release == ""
}

// ttsPhononMarkText is a mark's first line ("" when absent).
func ttsPhononMarkText(name string) string {
	p := ttsPhononMarkPath(name)
	if p == "" {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	if len(data) > 400 {
		data = data[:400]
	}
	return firstLine(strings.TrimSpace(string(data)))
}

func ttsPhononSetMark(name, text string, on bool) {
	p := ttsPhononMarkPath(name)
	if p == "" {
		return
	}
	if !on {
		_ = os.Remove(p)
		return
	}
	_ = os.WriteFile(p, []byte(text+"\n"), 0o644)
}

// ttsPhononSpeed is the probe's measurement, seconds of work per second of
// speech (false when it was not measured).
func ttsPhononSpeed() (float64, bool) {
	text := ttsPhononMarkText(ttsPhononSpeedName)
	f, err := strconv.ParseFloat(strings.TrimPrefix(text, "rtf "), 64)
	if err != nil || f <= 0 || f > 100 {
		return 0, false
	}
	return f, true
}

// ttsPhononInstalled reports whether the package is on the card and readable.
func ttsPhononInstalled() bool {
	dir := ttsPhononPath()
	if dir == "" || relPackDirInstalling(dir) {
		return false
	}
	_, err := ttsPhononReadManifest(dir)
	return err == nil
}

// ttsPhononRetired is set when the natural voice failed twice in a row this
// run (tts_phonon.go): Piper speaks until Hear Juka or the Settings row tries
// it again (ttsPhononFailReset).
var (
	ttsPhononFails   atomic.Int32
	ttsPhononRetired atomic.Bool
)

// ttsPhononMeasured is set once this run has timed the voice on a real line
// (for an install whose probe could not: too little memory then), and
// ttsPhononSlowNow when that line was too slow: Piper speaks for the rest of
// the run, and the next run times it again.
var (
	ttsPhononMeasured atomic.Bool
	ttsPhononSlowNow  atomic.Bool
)

// ttsPhononFailReset gives the natural voice another chance.
func ttsPhononFailReset() {
	ttsPhononFails.Store(0)
	ttsPhononRetired.Store(false)
}

// ttsPhononVoice is the natural voice as the device's voice, when it is
// installed, wanted and not retired. Any goroutine (file checks only).
func ttsPhononVoice() (ttsLocalVoice, bool) {
	if !ttsOfflineVoiceSupported() || ttsPhononRetired.Load() || ttsPhononMarked(ttsPhononOffMark) || ttsPhononTooSlow() {
		return ttsLocalVoice{}, false
	}
	dir := ttsPhononPath()
	if dir == "" || relPackDirInstalling(dir) {
		return ttsLocalVoice{}, false
	}
	m, err := ttsPhononReadManifest(dir)
	if err != nil {
		return ttsLocalVoice{}, false
	}
	name := "Phonon · natural"
	if strings.TrimSpace(m.Name) != "" {
		name = strings.TrimSpace(m.Name) + " · natural"
	}
	return ttsLocalVoice{name: name, path: filepath.Join(dir, m.Server), kind: "phonon", model: dir}, true
}

func ttsPhononPackSpec() relPackSpec {
	return relPackSpec{
		Key:         ttsPhononPackKey,
		Asset:       ttsPhononPackAsset,
		Title:       "the natural voice",
		MaxAsset:    200 << 20,
		MaxUnpacked: 320 << 20,
		Root:        ttsPhononDirName,
		Probe:       ttsPhononProbe,
		Supported:   ttsOfflineVoiceSupported,
	}
}

// ttsPhononProbing is the install the probe runs for: its context (a
// cancelled install ends the timing too) and whether the probe found the CPU
// too slow (rather than the build not starting). Under mu.
var ttsPhononProbing struct {
	mu   sync.Mutex
	ctx  context.Context
	slow bool
}

// ttsPhononProbe starts the server from the unpacked package and times a
// sentence (ttsPhononBench). Too slow for this CPU, or not starting at all,
// refuses the package; with too little free memory to tell, it is accepted
// and timed on its first real line instead (ttsPhononNoteSpeed).
func ttsPhononProbe(dir string) string {
	ttsPhononProbing.mu.Lock()
	ctx := ttsPhononProbing.ctx
	ttsPhononProbing.slow = false
	ttsPhononProbing.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	m, err := ttsPhononReadManifest(dir)
	if err != nil {
		return err.Error()
	}
	// A card formatted FAT keeps no execute bit; ext4 keeps the zip's.
	_ = os.Chmod(filepath.Join(dir, m.Server), 0o755)
	if free := freeMemoryMB(); free > 0 && free < ttsPhononNeedMB(m)+ttsPhononLowWaterMB {
		logInfo("tts", "natural voice: %d MB free, not enough to time it now; it is timed on first use", free)
		return ""
	}
	rtf, err := ttsPhononBench(ctx, dir, m)
	if err != nil {
		return "the natural voice did not start here: " + err.Error()
	}
	logInfo("tts", "natural voice: %.2f s of work per second of speech on this device (build runner: %.3f)", rtf, m.CIRTF)
	ttsPhononSetMark(ttsPhononSpeedName, fmt.Sprintf("rtf %.2f", rtf), true)
	if rtf > ttsPhononMaxRTF {
		ttsPhononProbing.mu.Lock()
		ttsPhononProbing.slow = true
		ttsPhononProbing.mu.Unlock()
		return fmt.Sprintf("this CPU is too slow for it (%.1f s of work per second of speech)", rtf)
	}
	return ""
}

// ── Settings → Voice & AI → Natural offline voice ────────────────────────

func ttsPhononSettingsRow() setRow {
	return setRow{
		ID: "jukaNaturalVoice", Label: "Natural offline voice", Kind: setKindAction,
		Visible: func(*Config) bool { return ttsOfflineVoiceSupported() },
		Value:   ttsPhononValue,
		Detail:  ttsPhononDetail,
		Open:    ttsPhononOpen,
	}
}

func ttsPhononValue(*Config) string {
	switch {
	case relPackRunning(ttsPhononPackKey):
		return "Installing…"
	case ttsPhononInstalled() && ttsPhononMarked(ttsPhononOffMark):
		return "Off"
	case ttsPhononInstalled() && ttsPhononTooSlow():
		return "Too slow here"
	case ttsPhononInstalled():
		return "On"
	case ttsPhononTooSlow():
		return "Not for this CPU"
	}
	return "Download"
}

func ttsPhononDetail(*Config) string {
	speed := ""
	if rtf, ok := ttsPhononSpeed(); ok {
		speed = fmt.Sprintf(" · %.1f s of work per second of speech here", rtf)
	}
	switch {
	case ttsPhononInstalled() && ttsPhononMarked(ttsPhononOffMark):
		return "Piper speaks offline instead · A turns the natural voice back on" + speed
	case ttsPhononInstalled() && ttsPhononTooSlow():
		return "This CPU is too slow for it, so Piper speaks · A times it again" + speed
	case ttsPhononInstalled() && ttsPhononRetired.Load():
		return "It failed twice, so Piper speaks for now · Hear Juka tries it again" + speed
	case ttsPhononInstalled():
		return "Phonon by Gradium · sounds like a person, works offline · A switches back to Piper" + speed
	}
	if why, _, ok := ttsPhononSlowInfo(); ok {
		if why == "" {
			why = "it would not run here"
		}
		return "Piper stays: " + why + " · A tries again"
	}
	return "More natural than Piper (~70 MB) · checks this CPU is fast enough first · Piper stays as backup"
}

// ttsPhononOpen installs the natural voice, or switches between it and Piper
// once it is installed. Main thread.
func ttsPhononOpen(*Config) {
	switch {
	case !ttsOfflineVoiceSupported():
		setToast("The natural voice is for TrimUI handhelds")
	case relPackRunning(ttsPhononPackKey):
		setToast("The natural voice is already installing")
	case ttsPhononInstalled() && ttsPhononTooSlow():
		// Time it again on the next line (a busy moment may have slowed the
		// first measurement).
		ttsPhononSetMark(ttsPhononSpeedName, "", false)
		ttsPhononSetMark(ttsPhononSlowMark, "", false)
		ttsPhononSetMark(ttsPhononOffMark, "", false)
		ttsPhononMeasured.Store(false)
		ttsPhononSlowNow.Store(false)
		ttsPhononFailReset()
		ttsLocalRecheck()
		setToast("The natural voice will be timed again on Juka's next line")
	case ttsPhononInstalled():
		off := !ttsPhononMarked(ttsPhononOffMark)
		ttsPhononSetMark(ttsPhononOffMark, "Piper chosen in Settings", off)
		ttsPhononFailReset()
		ttsPhononWarmStop()
		ttsLocalRecheck()
		if off {
			setToast("Natural voice off — Piper speaks offline")
			return
		}
		if appConfig != nil {
			ttsStore(appConfig, ttsVoiceKey, ttsVoiceDevice)
		}
		setToast("Natural voice on — press Hear Juka to try it")
	default:
		// The user asked: a device refused before is timed again (the mark
		// goes once the install has started, ttsPhononStartInstallOf).
		ttsPhononStartInstall(false)
	}
}

// ── Installing ───────────────────────────────────────────────────────────

// ttsPhononStartInstall downloads, checks, times and installs the package
// behind the install card. Main thread. It reports whether it started (false:
// Wi-Fi or another install kept it from starting).
func ttsPhononStartInstall(auto bool) bool {
	return ttsPhononStartInstallOf(auto, "")
}

// ttsPhononStartInstallOf is ttsPhononStartInstall with the release the
// startup check found (release, "" when unknown), recorded should its build
// not start here so that only a newer release is tried by itself again.
func ttsPhononStartInstallOf(auto bool, release string) bool {
	say := func(msg string) {
		if !auto {
			setToast(msg)
		}
	}
	switch {
	case !ttsOfflineVoiceSupported():
		return false
	case !IsOnline():
		say("Connect to Wi-Fi to download the natural voice")
		return false
	case installBarActive() || relPackRunning(ttsPhononPackKey) || ttsVoiceInstalling():
		say("Finish the current download, then try the natural voice")
		return false
	}
	dir := ttsPhononPath()
	if dir == "" {
		say("JukaHub's data folder is not available")
		return false
	}
	if !tryBeginPatchOp() {
		say("Another install is running — try again when it finishes")
		return false
	}
	if !auto {
		ttsPhononSetMark(ttsPhononSlowMark, "", false)
		ttsPhononSlowNow.Store(false)
	}
	gen, runCtx := installBarBeginRun("Natural voice")
	go func() {
		defer endPatchOp()
		ctx, cancel := context.WithTimeout(runCtx, ttsPhononInstallTimeout)
		defer cancel()
		// A server started from the old files lets go of them first, and
		// lines meanwhile go to Piper (ttsPhononTake refuses during it).
		ttsPhononWarmStop()
		ttsLocalRecheck()
		ttsPhononProbing.mu.Lock()
		ttsPhononProbing.ctx, ttsPhononProbing.slow = ctx, false
		ttsPhononProbing.mu.Unlock()
		// The probe times the new build; an old figure must not stand for it.
		ttsPhononSetMark(ttsPhononSpeedName, "", false)
		_, err := relPackInstall(ctx, nil, ttsPhononPackSpec(), dir, sttInstallProgress(gen))
		if err != nil && ctx.Err() != nil {
			// Cancelled or out of time, maybe during the probe's timing,
			// whose error would otherwise read as "won't run here".
			err = ctx.Err()
		}
		ttsPhononProbing.mu.Lock()
		slow := ttsPhononProbing.slow
		ttsPhononProbing.ctx = nil
		ttsPhononProbing.mu.Unlock()
		why := ""
		if errors.Is(err, errRelPackWontRun) {
			why = strings.TrimPrefix(err.Error(), errRelPackWontRun.Error()+" · ")
			switch {
			case slow:
				// For good (until the Settings row asks again).
				ttsPhononSetMark(ttsPhononSlowMark, why, true)
			case auto && release != "":
				// This build: a newer release is tried again by itself.
				ttsPhononSetMark(ttsPhononSlowMark, why+"\nrelease "+release, true)
			}
		}
		tbxPost(func() {
			switch {
			case errors.Is(err, context.Canceled):
				installBarAbortRun(gen)
				say("Stopped installing the natural voice")
			case err != nil && auto && errors.Is(err, errRelPackNotPublished):
				installBarAbortRun(gen)
				logInfo("tts", "natural voice: no release carries it yet")
			case err != nil:
				msg := relPackFriendly(err)
				installBarFailRun(gen, installCardDetail(msg))
				if why != "" {
					ShowToast("Natural voice: "+why+" — Piper stays Juka's offline voice", ToastKindInfo)
				} else {
					say("Could not install the natural voice: " + msg)
				}
				logWarn("tts", "natural voice install (auto=%v): %v", auto, err)
			default:
				ttsPhononSetMark(ttsPhononSlowMark, "", false)
				ttsPhononMeasured.Store(false)
				ttsPhononFailReset()
				ttsLocalRecheck()
				installBarDoneRun(gen)
				if !auto && appConfig != nil {
					ttsStore(appConfig, ttsVoiceKey, ttsVoiceDevice)
				}
				ShowToast("Natural voice ready — Juka sounds more like a person offline now", ToastKindInfo)
			}
		})
	}()
	return true
}

// ttsPhononAutoTried keeps the startup download (or update) to one look per
// run (a look that failed for want of a network clears it, so a reconnect
// looks again).
var ttsPhononAutoTried atomic.Bool

// ttsPhononAutoInstall is the startup download, called by the tools check
// after Piper's (netmon.go). It waits for Piper to be on the card and for no
// install card or startup download to have been busy for ten seconds, asks
// GitHub for the newest release that carries the package (no card for that:
// until the first release with it, the answer is no, every start), and only
// then starts the install with its card: the first one, or an update when
// that release carries a different build than the one installed. A build
// that would not start here is not fetched again until a newer release; a CPU
// found too slow, and a user who chose Piper, end it for good.
func ttsPhononAutoInstall() {
	if !ttsOfflineVoiceSupported() || !ttsAutoOnline() || ttsPhononMarked(ttsPhononOffMark) || ttsPhononTooSlow() {
		return
	}
	if !ttsPhononAutoTried.CompareAndSwap(false, true) {
		return
	}
	go func() {
		quiet := 0
		for i := 0; i < 720 && quiet < 2; i++ {
			time.Sleep(5 * time.Second)
			_, piper := ttsManagedVoice()
			if !piper || installBarActive() || ttsToolsInstalling.Load() > 0 || sttAutoWaiting.Load() || ttsVoiceInstalling() {
				quiet = 0
			} else {
				quiet++
			}
		}
		if quiet < 2 {
			// Piper never arrived this run (it waits for a reconnect or
			// Settings): the natural voice waits with it.
			ttsPhononAutoTried.Store(false)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		latest, err := relPackLatest(ctx, relPackClientFn(), ttsPhononPackSpec())
		cancel()
		if err != nil {
			if !errors.Is(err, errRelPackNotPublished) {
				ttsPhononAutoTried.Store(false)
			}
			logDebug("tts", "natural voice: not downloading now: %v", err)
			return
		}
		if _, refused, ok := ttsPhononSlowInfo(); ok && refused == latest.Rel.TagName {
			logDebug("tts", "natural voice: %s's build did not start here; waiting for a newer release", refused)
			return
		}
		if ttsPhononInstalled() {
			dir := ttsPhononPath()
			if installed, ok := relPackRead(dir); ok && !relPackNewer(installed, latest) {
				return
			}
			logInfo("tts", "natural voice: updating to %s", latest.Rel.TagName)
		}
		tag := latest.Rel.TagName
		tbxPost(func() {
			if !ttsPhononStartInstallOf(true, tag) {
				ttsPhononAutoTried.Store(false)
			}
		})
	}()
}
