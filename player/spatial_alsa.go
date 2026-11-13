package main

// spatial_alsa.go — spatial audio for every game and app on the handheld: the
// "system audio" route.
//
// JukaHub's own media runs through the DSP inside the player (spatial.go), and
// RetroArch can load the same DSP as an audio filter (spatial_games.go). Every
// other child — the standalone emulators (PPSSPP, DraStic, Mupen…), ports,
// apps, and RetroArch builds that cannot load a filter — plays through ALSA
// itself, and the plugin library (player/spatialfx) is also a LADSPA plugin.
// alsa-lib has a "ladspa" PCM that runs LADSPA plugins in front of another
// PCM, so a child started with
//
//	ALSA_CONFIG_PATH = <the system's alsa.conf>:/tmp/jukahub-audio/asound-spatial.conf
//	AUDIODEV         = jhspatial
//
// opens its sound through the chain
//
//	jhspatial (plug) -> jhspatial_fx (ladspa, label jukahub_spatial) -> jhspatial_out (plug) -> the real output
//
// whether it asks for AUDIODEV (SDL programs) or for "default" (RetroArch's
// ALSA drivers, most native programs). The plugs convert to the float samples
// LADSPA works in and back to what the output takes; the plugin's three
// controls carry the mode, strength and room of the moment. Nothing on the
// firmware is changed: the files are named only in that child's environment,
// after the system's alsa.conf, and rewritten at every launch.
//
// alsa-lib reads every ALSA_CONFIG_PATH file first and only then runs
// alsa.conf's load hook (hook 0: /etc/asound.conf, ~/.asoundrc), so a
// "default" written into the path file itself would lose to the firmware's.
// The path file therefore adds hook 1, which loads a second file holding only
// `pcm.!default "jhspatial"` after the system's (the headset route does the
// same, audioroute_bluealsa.go).
//
// The real output, the end of the chain:
//
//   - a Bluetooth headset JukaHub routes to (audioroute.go): its BlueALSA PCM,
//     the one audioRouteChildEnv would have given the child;
//   - otherwise ALSA's "sysdefault" — the card's own default, which is what
//     "default" is unless a system file redefines it. When one does (a
//     firmware's asound.conf, a desktop's PulseAudio file), that definition
//     cannot be renamed from here, so "default" is not taken over: the chain
//     ends in the system's "default" and only SDL programs (AUDIODEV) get the
//     effect; RetroArch keeps its filter route.
//
// A route that does not work would leave a game silent — alsa-lib fails the
// open when the ladspa PCM or the plugin is missing — so nothing is given to a
// child before a probe has proven it on this device: the system's libasound
// must hold the ladspa PCM, and a child copy of JukaHub (--spatial-alsa-probe)
// must open SDL audio through the whole chain by name, ending in ALSA's "null"
// PCM so no device is needed and none is held. The probe runs once per run, in
// the background, after the switch and a mode are on. Until it answers,
// launches play as before.
//
// The plugin is a 64-bit library. A 32-bit program (early firmware ran a 32-bit
// RetroArch, ra32.trimui) could not load it, and its sound would fail to open,
// so a launch that names a 32-bit ARM program, directly or in its script, is
// left alone (spatialLaunchLooks32).
//
// The route is for the TrimUI firmware (/usr/trimui): a desktop Linux has its
// own sound server, which alsa-lib reaches through "default".
// JUKAHUB_SPATIAL_ALSA=1 turns it on elsewhere (development), =0 off.

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// spatialALSAEnv forces the route on (1) or off (0).
	spatialALSAEnv = "JUKAHUB_SPATIAL_ALSA"
	// spatialALSAConfDir holds the files the children and the probe read: in
	// RAM, gone at the next boot, and shared with the headset files
	// (audioroute_bluealsa.go).
	spatialALSAConfDir = "/tmp/jukahub-audio"
	// The launch's two files (the path file and the "default" its hook
	// loads), and the probe's own pair, which ends in ALSA's "null" PCM.
	spatialALSAConfName         = "asound-spatial.conf"
	spatialALSADefaultName      = "asound-spatial-default.conf"
	spatialALSAProbeConfName    = "asound-spatial-probe.conf"
	spatialALSAProbeDefaultName = "asound-spatial-probe-default.conf"
	// spatialALSAProbeFlag starts this binary as the probe child.
	spatialALSAProbeFlag = "--spatial-alsa-probe"
	// spatialALSAPCM is the PCM a child opens; the stages behind it are
	// spatialALSAPCM+"_fx" (the ladspa PCM) and spatialALSAPCM+"_out".
	spatialALSAPCM = "jhspatial"
	// spatialLADSPALabel is the plugin's LADSPA label (jukahub_spatial.c).
	spatialLADSPALabel = "jukahub_spatial"
	// spatialALSALadspaSymbol is in every libasound built with the ladspa PCM.
	spatialALSALadspaSymbol = "_snd_pcm_ladspa_open"
	// spatialALSAProbeTimeout bounds the probe child.
	spatialALSAProbeTimeout = 15 * time.Second
	// spatialALSAProbeAfter keeps the probe out of the start-up rush.
	spatialALSAProbeAfter = 20 * time.Second
	// spatialALSARetryAfter is when a probe that failed for a passing reason
	// (the child could not start or did not finish, the data folder was not
	// ready) runs again, at most spatialALSARetryMax times; a probe that
	// proved "no" stands for the run.
	spatialALSARetryAfter = 5 * time.Minute
	spatialALSARetryMax   = 3
	// spatialDSPArmedWindow is how long the launch that armed RetroArch's
	// filter (prepareSpatialAudio) waits for its child.
	spatialDSPArmedWindow = 30 * time.Second
	// spatialLibasoundMax bounds the read of libasound (about 1 MB);
	// spatialScriptMax the read of a launch script.
	spatialLibasoundMax = 16 << 20
	spatialScriptMax    = 64 << 10
)

// spatialALSAStarted is when the process started, for spatialALSAProbeAfter.
var spatialALSAStarted = time.Now()

// Test seams and the places looked in.
var (
	spatialALSASystemConfs = []string{"/usr/share/alsa/alsa.conf", "/usr/local/share/alsa/alsa.conf"}
	spatialALSALibDirs     = []string{"/usr/lib", "/lib", "/usr/lib64", "/lib64", "/usr/lib/aarch64-linux-gnu", "/lib/aarch64-linux-gnu"}
	spatialArm32Loaders    = []string{"/lib/ld-linux-armhf.so.3", "/lib/ld-linux.so.3", "/usr/lib/ld-linux-armhf.so.3", "/lib32/ld-linux-armhf.so.3"}
	spatialALSARunProbe    = spatialALSARunProbeChild
	spatialALSAProbeGo     = func(run func()) { go run() }
)

// spatialALSADefaultRE finds a system file that defines ALSA's "default" PCM
// (pcm.default, pcm.!default; comments are stripped first). ctl.!default and
// defaults.pcm.* do not match.
var spatialALSADefaultRE = regexp.MustCompile(`(^|[^.\w])pcm\s*\.\s*!?\s*"?default\b`)

// spatialSystemRouteOnce caches spatialSystemRouteApplies: the answer cannot
// change while JukaHub runs.
var spatialSystemRouteOnce = sync.OnceValue(func() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(spatialALSAEnv))) {
	case "0", "off", "false", "no":
		return false
	case "1", "on", "true", "yes":
		return true
	}
	fi, err := os.Stat("/usr/trimui")
	return err == nil && fi.IsDir()
})

// spatialSystemRouteApplies reports whether this device uses the ALSA route.
func spatialSystemRouteApplies() bool { return spatialSystemRouteOnce() }

// ── The probe ─────────────────────────────────────────────────────────────

// The probe's states.
const (
	spatialALSAUnknown = iota
	spatialALSARunning
	spatialALSAOK
	spatialALSANo
)

// spatialALSAProbeState is the probe's answer for this run.
var spatialALSAProbeState struct {
	mu      sync.Mutex
	state   int
	why     string
	retry   time.Time // a passing failure probes again after this
	retries int
}

// spatialSystemRouteState is the probe's answer: ok, the reason when not, and
// whether there is an answer yet.
func spatialSystemRouteState() (ok bool, why string, known bool) {
	p := &spatialALSAProbeState
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.state {
	case spatialALSAOK:
		return true, "", true
	case spatialALSANo:
		return false, p.why, true
	}
	return false, "", false
}

// spatialSystemRouteReady reports whether the route is proven and takes over
// "default" too — every program, RetroArch included — which is what lets the
// launch leave RetroArch's filter off (prepareSpatialAudio). Launch time only:
// it reads the system's ALSA files.
func spatialSystemRouteReady() bool {
	if !spatialSystemRouteApplies() {
		return false
	}
	if ok, _, _ := spatialSystemRouteState(); !ok {
		return false
	}
	if audioRouteChildPCM() != "" {
		return true
	}
	base := spatialALSABaseConf()
	return base != "" && spatialALSAHasSysdefault(base) && spatialALSACustomDefault() == ""
}

// spatialALSAHasSysdefault reports whether the system's alsa.conf defines
// "sysdefault" (every alsa-lib since 2009 does), the name the chain ends in on
// the speakers.
func spatialALSAHasSysdefault(base string) bool {
	has, err := spatialFileHas(base, []byte("pcm.sysdefault"), 1<<20)
	return err == nil && has
}

// spatialSystemRouteKick starts the probe when it has not run (or a passing
// failure is due a retry). Cheap: the UI thread calls it once a second while
// spatial audio in games is on (spatialBind).
func spatialSystemRouteKick() {
	if !spatialSystemRouteApplies() || time.Since(spatialALSAStarted) < spatialALSAProbeAfter {
		return
	}
	p := &spatialALSAProbeState
	p.mu.Lock()
	due := p.state == spatialALSAUnknown || (p.state == spatialALSANo && !p.retry.IsZero() && time.Now().After(p.retry))
	if due {
		p.state = spatialALSARunning
	}
	p.mu.Unlock()
	if due {
		spatialALSAProbeGo(spatialALSAProbeRun)
	}
}

// spatialALSAProbeRun runs the probe and records its answer; a panic counts
// as a passing failure, so it is tried again later.
func spatialALSAProbeRun() {
	ok, why, passing := false, "the check stopped unexpectedly", true
	defer func() {
		if r := recover(); r != nil {
			logWarn("spatial", "the system audio check failed: %v", r)
		}
		p := &spatialALSAProbeState
		p.mu.Lock()
		p.retry = time.Time{}
		if ok {
			p.state, p.why = spatialALSAOK, ""
		} else {
			p.state, p.why = spatialALSANo, why
			if passing && p.retries < spatialALSARetryMax {
				p.retries++
				p.retry = time.Now().Add(spatialALSARetryAfter)
			}
		}
		p.mu.Unlock()
		if ok {
			logInfo("spatial", "system audio route works: games and apps can play through the spatial plugin")
		} else {
			logWarn("spatial", "system audio route not available: %s", why)
		}
	}()
	ok, why, passing = spatialALSAProbeCheck()
}

// spatialALSAProbeCheck is the probe itself. passing marks a failure worth
// another try later.
func spatialALSAProbeCheck() (ok bool, why string, passing bool) {
	plugin := spatialPluginLocate()
	if plugin == "" {
		return false, "the game audio plugin (lib/" + spatialPluginFileName() + ") is not installed; it comes with the handheld package", false
	}
	base := spatialALSABaseConf()
	if base == "" {
		return false, "this system has no alsa.conf", false
	}
	if lib := spatialFindLibasound(); lib != "" {
		has, err := spatialFileHas(lib, []byte(spatialALSALadspaSymbol), spatialLibasoundMax)
		if err == nil && !has {
			return false, "the system's sound library (" + lib + ") was built without LADSPA support", false
		}
	}
	dir, err := spatialStagePlugin(plugin)
	if err != nil {
		return false, "could not stage the plugin: " + err.Error(), true
	}
	if !spatialALSAQuotable(dir) || strings.Contains(dir, ":") {
		return false, "the plugin folder " + dir + " cannot be named in an ALSA file", false
	}
	conf, err := spatialALSAWriteFiles(spatialALSAProbeConfName, spatialALSAProbeDefaultName, dir, "null", true,
		spatialDSPHeadphones, spatialDSPRoomMedium, spatialDefaultStrength/100.0)
	if err != nil {
		return false, "could not write the check's ALSA file: " + err.Error(), true
	}
	if err := spatialALSARunProbe(base + ":" + conf); err != nil {
		var passingErr *spatialProbePassingError
		if errors.As(err, &passingErr) {
			return false, err.Error(), true
		}
		return false, "a test program could not open its sound through the plugin (" + err.Error() + ")", false
	}
	return true, "", false
}

// spatialProbePassingError is a probe child that could not be started or did
// not finish — worth another try, unlike one that ran and said no.
type spatialProbePassingError struct{ msg string }

func (e *spatialProbePassingError) Error() string { return e.msg }

// spatialALSARunProbeChild starts this binary with spatialALSAProbeFlag, the
// probe's ALSA_CONFIG_PATH and AUDIODEV naming the chain, and returns why it
// failed (nil when it opened SDL audio through the chain and played).
func spatialALSARunProbeChild(configPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return &spatialProbePassingError{"the check could not start: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), spatialALSAProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, spatialALSAProbeFlag)
	env := spatialEnvWithout(os.Environ(), "AUDIODEV", "ALSA_CONFIG_PATH", "SDL_AUDIODRIVER")
	cmd.Env = append(env, "ALSA_CONFIG_PATH="+configPath, "AUDIODEV="+spatialALSAPCM, "SDL_AUDIODRIVER=alsa")
	cmd.WaitDelay = 2 * time.Second
	var out spatialCappedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return &spatialProbePassingError{"the check could not start: " + err.Error()}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return &spatialProbePassingError{"the check did not finish in time"}
	}
	if err != nil {
		msg := spatialProbeLastLine(out.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// spatialProbeLastLine is the most telling line of the child's output: the
// first alsa-lib "ALSA lib pcm_ladspa.c…" line when there is one (the cause),
// else the last line.
func spatialProbeLastLine(out string) string {
	pick := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if pick == "" || !strings.HasPrefix(pick, "ALSA lib") {
			pick = l
		}
	}
	if len(pick) > 240 {
		pick = pick[:240] + "…"
	}
	return pick
}

// spatialCappedBuffer keeps the first 8 KB a child writes.
type spatialCappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *spatialCappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := 8<<10 - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *spatialCappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// handleSpatialCommand runs the probe child (main.go, before SDL starts):
// open SDL audio with the probe's files in force, play a moment of silence and
// report.
func handleSpatialCommand(args []string) (handled bool, exitCode int) {
	if len(args) == 0 || args[0] != spatialALSAProbeFlag {
		return false, 0
	}
	if err := spatialALSAProbeMain(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return true, 1
	}
	fmt.Println("ok")
	return true, 0
}

func spatialALSAProbeMain() error {
	if err := sdl.Init(sdl.INIT_AUDIO); err != nil {
		return fmt.Errorf("SDL audio: %w", err)
	}
	defer sdl.Quit()
	if drv := sdl.GetCurrentAudioDriver(); !strings.EqualFold(drv, "alsa") {
		return fmt.Errorf("SDL's audio driver is %q, not alsa", drv)
	}
	want := sdl.AudioSpec{Freq: 48000, Format: sdl.AUDIO_S16SYS, Channels: 2, Samples: 1024}
	var got sdl.AudioSpec
	dev, err := sdl.OpenAudioDevice("", false, &want, &got, 0)
	if err == nil && dev == 0 {
		err = errors.New("SDL opened no device")
	}
	if err != nil {
		return fmt.Errorf("opening the spatial chain: %w", err)
	}
	defer sdl.CloseAudioDevice(dev)
	if err := sdl.QueueAudio(dev, make([]byte, 48000/5*4)); err != nil {
		return fmt.Errorf("queueing: %w", err)
	}
	sdl.PauseAudioDevice(dev, false)
	time.Sleep(300 * time.Millisecond)
	if sdl.GetAudioDeviceStatus(dev) == sdl.AUDIO_STOPPED {
		return errors.New("the spatial chain stopped while playing")
	}
	return nil
}

// ── The files ─────────────────────────────────────────────────────────────

// spatialALSABaseConf is the system's alsa.conf, which must lead
// ALSA_CONFIG_PATH; "" when there is none.
func spatialALSABaseConf() string {
	for _, p := range spatialALSASystemConfs {
		if regularFile(p) {
			return p
		}
	}
	return ""
}

// spatialFindLibasound is the system's libasound.so.2, "" when none is found
// (then only the child probe decides).
func spatialFindLibasound() string {
	for _, dir := range spatialALSALibDirs {
		matches, _ := filepath.Glob(filepath.Join(dir, "libasound.so.2*"))
		for _, m := range matches {
			if regularFile(m) {
				return m
			}
		}
	}
	return ""
}

// spatialFileHas reports whether the file (at most limit bytes) holds needle.
func spatialFileHas(path string, needle []byte, limit int64) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return false, err
	}
	return bytes.Contains(raw, needle), nil
}

// spatialALSAUserFiles are the system files that may redefine "default": the
// global ones, the firmware's and JukaHub's home, and the conf.d folders.
func spatialALSAUserFiles() []string {
	files := []string{"/etc/asound.conf", "/usr/etc/asound.conf"}
	seen := map[string]bool{}
	for _, home := range []string{os.Getenv(childEnvOrigPrefix + "HOME"), os.Getenv("HOME")} {
		if home == "" || seen[home] {
			continue
		}
		seen[home] = true
		files = append(files, filepath.Join(home, ".asoundrc"), filepath.Join(home, ".config", "alsa", "asoundrc"))
	}
	for _, dir := range []string{"/usr/share/alsa/alsa.conf.d", "/etc/alsa/conf.d"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.conf"))
		files = append(files, matches...)
	}
	return files
}

// spatialALSACustomDefault is the first system file that defines ALSA's
// "default" PCM, "" when none does (then "default" is the card's own, which
// "sysdefault" names too). A definition this misses (a nested form, a file
// pulled in by an include) is still overridden by the hook file, so the chain
// then ends in the card's default instead of the firmware's.
func spatialALSACustomDefault() string {
	for _, path := range spatialALSAUserFiles() {
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) > 1<<20 {
			continue
		}
		if spatialALSADefaultRE.MatchString(spatialALSAStripComments(string(raw))) {
			return path
		}
	}
	return ""
}

// spatialALSAStripComments drops every "#…" to the end of its line, outside
// double quotes (a backslash escapes the next character inside them).
func spatialALSAStripComments(s string) string {
	var b strings.Builder
	quoted, comment, escaped := false, false, false
	for _, r := range s {
		switch {
		case comment:
			if r == '\n' {
				comment = false
				b.WriteRune(r)
			}
			continue
		case escaped:
			escaped = false
		case r == '\\' && quoted:
			escaped = true
		case r == '"':
			quoted = !quoted
		case r == '\n':
			quoted = false
		case r == '#' && !quoted:
			comment = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// spatialALSAQuotable reports whether s can sit inside an ALSA double-quoted
// string as it is.
func spatialALSAQuotable(s string) bool {
	return s != "" && !strings.ContainsAny(s, "\"\\\n\r")
}

// spatialALSAConfText is the path file: the chain behind spatialALSAPCM ending
// in slave, the plugin's controls, and — when defaultFile is set — hook 1,
// which loads defaultFile after the system's own files.
func spatialALSAConfText(pluginDir, slave, defaultFile string, mode, room int, strength float64) string {
	if !(strength >= 0) { // also NaN
		strength = 0
	}
	if strength > 1 {
		strength = 1
	}
	var b strings.Builder
	b.WriteString("# Written by JukaHub (spatial_alsa.go) for the game or app it starts with\n")
	b.WriteString("# spatial audio on: ALSA_CONFIG_PATH names it after the system's alsa.conf,\n")
	b.WriteString("# so only that program reads it. Rewritten at every launch; safe to delete.\n")
	fmt.Fprintf(&b, "pcm.%s_out {\n\ttype plug\n\tslave.pcm \"%s\"\n}\n", spatialALSAPCM, slave)
	fmt.Fprintf(&b, "pcm.%s_fx {\n\ttype ladspa\n\tslave.pcm \"%s_out\"\n\tpath \"%s\"\n\tchannels 2\n", spatialALSAPCM, spatialALSAPCM, pluginDir)
	// playback_plugins: a program recording through the chain (a game's
	// microphone on "default") is passed straight through.
	b.WriteString("\tplayback_plugins [\n\t\t{\n")
	fmt.Fprintf(&b, "\t\t\tlabel \"%s\"\n", spatialLADSPALabel)
	b.WriteString("\t\t\tpolicy none\n")
	b.WriteString("\t\t\tinput.bindings.0 \"Left in\"\n\t\t\tinput.bindings.1 \"Right in\"\n")
	b.WriteString("\t\t\toutput.bindings.0 \"Left out\"\n\t\t\toutput.bindings.1 \"Right out\"\n")
	fmt.Fprintf(&b, "\t\t\tinput.controls [ %d %s %d ]\n", mode, strconv.FormatFloat(strength, 'f', 2, 64), room)
	b.WriteString("\t\t}\n\t]\n}\n")
	fmt.Fprintf(&b, "pcm.%s {\n\ttype plug\n\tslave.pcm \"%s_fx\"\n}\n", spatialALSAPCM, spatialALSAPCM)
	if defaultFile != "" {
		b.WriteString("# Hook 1 runs after alsa.conf's hook 0 (asound.conf, .asoundrc), so this\n")
		b.WriteString("# \"default\" is the last one loaded.\n")
		fmt.Fprintf(&b, "@hooks.1 {\n\tfunc load\n\tfiles [ \"%s\" ]\n\terrors false\n}\n", defaultFile)
	}
	return b.String()
}

// spatialALSADefaultText is the file hook 1 loads: "default" plays through
// the chain and, when the system names the card's own default, records from
// it as before (else recording goes through the chain, which passes it on).
func spatialALSADefaultText(sysdefault bool) string {
	head := "# Written by JukaHub (spatial_alsa.go): ALSA's \"default\" plays through spatial\n" +
		"# audio for the program it starts. Rewritten at every launch; safe to delete.\n"
	if !sysdefault {
		return head + "pcm.!default \"" + spatialALSAPCM + "\"\n"
	}
	return head + "pcm.!default {\n\ttype asym\n\tplayback.pcm \"" + spatialALSAPCM + "\"\n\tcapture.pcm \"sysdefault\"\n}\n"
}

// spatialALSAWriteFiles writes a path file (and, with takeDefault, the
// "default" file its hook loads) into spatialALSAConfDir and returns the path
// file's path. The default file is written first, so a program that reads the
// new path file always finds it.
func spatialALSAWriteFiles(name, defaultName, pluginDir, slave string, takeDefault bool, mode, room int, strength float64) (string, error) {
	defaultFile := ""
	if takeDefault {
		base := spatialALSABaseConf()
		path, err := spatialALSAWriteConf(defaultName, spatialALSADefaultText(base != "" && spatialALSAHasSysdefault(base)))
		if err != nil {
			return "", err
		}
		defaultFile = path
	}
	return spatialALSAWriteConf(name, spatialALSAConfText(pluginDir, slave, defaultFile, mode, room, strength))
}

// spatialALSAWriteConf writes one file into spatialALSAConfDir (through a
// temporary file, so a program opening its sound meanwhile never reads half
// of one) and returns its path.
func spatialALSAWriteConf(name, text string) (string, error) {
	if err := os.MkdirAll(spatialALSAConfDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(spatialALSAConfDir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// ── The launch ────────────────────────────────────────────────────────────

// spatialDSPArmed is the launch prepareSpatialAudio set RetroArch's filter
// for: its child must not go through the plugin a second time.
var spatialDSPArmed struct {
	mu       sync.Mutex
	launcher string
	at       time.Time
}

// spatialDSPArm records (launcher != "") or clears the armed launch.
func spatialDSPArm(launcher string) {
	spatialDSPArmed.mu.Lock()
	defer spatialDSPArmed.mu.Unlock()
	spatialDSPArmed.launcher, spatialDSPArmed.at = launcher, spatialNow()
}

// spatialDSPArmedFor reports whether cmd is the armed launch — the launcher is
// its program, one of its arguments, or the folder it runs in — and clears the
// mark when it is. Any other child (a port started meanwhile) is not affected;
// a mark older than spatialDSPArmedWindow (a launch that failed) is dropped.
func spatialDSPArmedFor(cmd *exec.Cmd) bool {
	a := &spatialDSPArmed
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.launcher == "" {
		return false
	}
	if spatialNow().Sub(a.at) > spatialDSPArmedWindow {
		a.launcher = ""
		return false
	}
	if cmd == nil {
		return false
	}
	want := filepath.Clean(a.launcher)
	match := cmd.Path != "" && filepath.Clean(cmd.Path) == want
	for _, arg := range cmd.Args {
		if arg != "" && filepath.Clean(arg) == want {
			match = true
		}
	}
	if cmd.Dir != "" && filepath.Clean(cmd.Dir) == filepath.Dir(want) {
		match = true
	}
	if match {
		a.launcher = ""
	}
	return match
}

// spatialEnvWithout is env without any entry for the names.
func spatialEnvWithout(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, n := range names {
			if key == n {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// spatialChildEnv is env for the screen-taking child cmd (screengroup_unix.go,
// run before audioRouteChildEnv, which then leaves both variables alone): with
// spatial audio in games on, a mode in force and the route proven, the child
// gets ALSA_CONFIG_PATH and AUDIODEV for the chain. Anything else returns env
// as it was: a launch never waits for or fails on spatial audio. It may run on
// a launch goroutine, so it reads the settings snapshot spatialBind publishes,
// never the config itself.
func spatialChildEnv(cmd *exec.Cmd, env []string) []string {
	if !spatialSystemRouteApplies() {
		return env
	}
	// RetroArch's filter carries this launch (spatial_games.go).
	if spatialDSPArmedFor(cmd) {
		return env
	}
	sp := spatialSettingsLive.Load()
	if sp == nil || !sp.Games {
		return env
	}
	s := *sp
	mode := spatialEffectiveMode(s)
	if mode == spatialDSPOff {
		return env
	}
	if v, set := childEnvLookup(env, "ALSA_CONFIG_PATH"); set && v != "" {
		spatialLogOnce("alsa-own-config", "spatial audio skips programs started with their own ALSA_CONFIG_PATH (%s)", v)
		return env
	}
	if v, set := childEnvLookup(env, "AUDIODEV"); set && v != "" && v != "default" {
		spatialLogOnce("alsa-own-dev:"+v, "spatial audio skips programs started with AUDIODEV=%s", v)
		return env
	}
	ok, why, known := spatialSystemRouteState()
	if !known {
		spatialSystemRouteKick()
		spatialLogOnce("alsa-pending", "spatial audio: the system audio route is still being checked; this program plays without it")
		return env
	}
	if !ok {
		spatialLogOnce("alsa-no", "spatial audio skips games outside RetroArch: %s", why)
		return env
	}
	if cmd != nil && spatialLaunchLooks32(cmd) {
		spatialLogOnce("alsa-32:"+cmd.Path+" "+strings.Join(cmd.Args, " "), "spatial audio skips %s: it runs a 32-bit program, which cannot load the 64-bit plugin", strings.Join(cmd.Args, " "))
		return env
	}
	path, err := spatialALSALaunchConf(mode, spatialRoomIndex(s.Room), s.Strength)
	if err != nil {
		logWarn("spatial", "cannot prepare spatial audio for this program: %v", err)
		return env
	}
	env = spatialEnvWithout(env, "AUDIODEV")
	env = append(env, "ALSA_CONFIG_PATH="+path, "AUDIODEV="+spatialALSAPCM)
	return env
}

// spatialALSALaunchConf stages the plugin, writes the launch's files and
// returns the ALSA_CONFIG_PATH value.
func spatialALSALaunchConf(mode, room int, strength float64) (string, error) {
	base := spatialALSABaseConf()
	if base == "" {
		return "", errors.New("no system alsa.conf")
	}
	plugin := spatialPluginLocate()
	if plugin == "" {
		return "", errors.New("the plugin is gone")
	}
	dir, err := spatialStagePlugin(plugin)
	if err != nil {
		return "", err
	}
	// ladspa's path is a ":"-separated list.
	if !spatialALSAQuotable(dir) || strings.Contains(dir, ":") {
		return "", fmt.Errorf("the plugin folder %q cannot be named in an ALSA file", dir)
	}
	slave, takeDefault, how := "sysdefault", true, "the speakers"
	if !spatialALSAHasSysdefault(base) {
		// No name for the card's own default: the chain can only end in
		// "default", which it then must not take over.
		slave, takeDefault, how = "default", false, "the system default (no sysdefault here; SDL programs only)"
	}
	if pcm := audioRouteChildPCM(); pcm != "" {
		takeDefault = true
		if !spatialALSAQuotable(pcm) {
			return "", fmt.Errorf("odd headset PCM %q", pcm)
		}
		slave, how = pcm, "the Bluetooth headset"
	} else if file := spatialALSACustomDefault(); file != "" {
		// "default" is the system's own here and cannot be wrapped: only
		// SDL programs (AUDIODEV) get the chain, which ends in it.
		slave, takeDefault, how = "default", false, "the system default (defined in "+file+"; SDL programs only)"
	}
	conf, err := spatialALSAWriteFiles(spatialALSAConfName, spatialALSADefaultName, dir, slave, takeDefault, mode, room, strength)
	if err != nil {
		return "", err
	}
	logInfo("spatial", "spatial audio for this program through ALSA, mode %d, to %s", mode, how)
	return base + ":" + conf, nil
}

// ── 32-bit programs ───────────────────────────────────────────────────────

// spatialArm32Possible reports whether this system can run 32-bit ARM
// programs at all (it has their loader); without one, no launch is 32-bit.
var spatialArm32Possible = sync.OnceValue(func() bool {
	for _, p := range spatialArm32Loaders {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
})

// spatialScriptAssignRE and spatialScriptCdRE read the two shell forms a
// launch script names its program with: NAME=value and cd <dir>.
var (
	spatialScriptAssignRE = regexp.MustCompile(`^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=("[^"]*"|'[^']*'|[^\s;]*)`)
	spatialScriptCdRE     = regexp.MustCompile(`(?:^|[;&|]\s*|\b(?:then|do|else)\s+)cd\s+("[^"]*"|'[^']*'|[^\s;&|]+)`)
	spatialScriptVarRE    = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
)

// spatialLaunchLooks32 reports whether cmd runs a 32-bit ARM program: its own
// binary, or one its launch script (one level of scripts below that too)
// names by path, after the script's simple NAME=value assignments and cd
// lines are applied. A miss means only that
// the 64-bit plugin is offered; a 32-bit program would then open its sound and
// fail, which is why everything readable is looked at.
func spatialLaunchLooks32(cmd *exec.Cmd) bool {
	if !spatialArm32Possible() {
		return false
	}
	dir := cmd.Dir
	seen := map[string]bool{}
	cands := []string{cmd.Path}
	if len(cmd.Args) > 1 {
		cands = append(cands, cmd.Args[1:]...)
	}
	for _, c := range cands {
		if spatialFileLooks32(spatialResolve(c, dir), dir, 0, seen) {
			return true
		}
	}
	return false
}

// spatialResolve makes p absolute against dir when it is relative.
func spatialResolve(p, dir string) string {
	p = strings.Trim(strings.TrimSpace(p), `"'`)
	if p == "" || filepath.IsAbs(p) || dir == "" {
		return p
	}
	return filepath.Join(dir, p)
}

// spatialFileLooks32 checks one file: an ELF answers by its class and machine;
// a script is read for the programs it names, depth levels deep.
func spatialFileLooks32(path, dir string, depth int, seen map[string]bool) bool {
	if path == "" || seen[path] || !filepath.IsAbs(path) {
		return false
	}
	seen[path] = true
	// Only regular files are opened: a script names /dev/watchdog, FIFOs and
	// ttys too, and opening those has effects (or blocks).
	for _, sys := range []string{"/dev/", "/proc/", "/sys/"} {
		if strings.HasPrefix(path, sys) {
			return false
		}
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	if string(magic[:]) == elf.ELFMAG {
		class, machine, ok := elfArchOf(f)
		return ok && class == elf.ELFCLASS32 && machine == elf.EM_ARM
	}
	if depth >= 2 {
		return false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(f, spatialScriptMax))
	if err != nil || bytes.IndexByte(raw, 0) >= 0 {
		return false // unreadable, or binary but not ELF
	}
	return spatialScriptLooks32(string(raw), filepath.Dir(path), dir, depth, seen)
}

// spatialScriptLooks32 reads a script's text for the programs it names.
func spatialScriptLooks32(text, scriptDir, runDir string, depth int, seen map[string]bool) bool {
	vars := map[string]string{}
	cwd := runDir
	if cwd == "" {
		cwd = scriptDir
	}
	expand := func(s string) string {
		s = strings.Trim(s, `"'`)
		return spatialScriptVarRE.ReplaceAllStringFunc(s, func(m string) string {
			name := spatialScriptVarRE.FindStringSubmatch(m)[1]
			if v, ok := vars[name]; ok {
				return v
			}
			if name == "PWD" {
				return cwd
			}
			return m
		})
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := spatialScriptAssignRE.FindStringSubmatch(line); m != nil && len(m[0]) == len(line) {
			vars[m[1]] = expand(m[2])
			continue
		}
		if m := spatialScriptCdRE.FindStringSubmatch(line); m != nil {
			if d := expand(m[1]); d != "" && !strings.Contains(d, "$") {
				cwd = spatialResolve(d, cwd)
			}
		}
		for _, tok := range strings.Fields(line) {
			tok = strings.TrimRight(strings.TrimLeft(tok, `"'(`), `"');&|`)
			if i := strings.LastIndexByte(tok, '='); i >= 0 {
				tok = tok[i+1:] // HOME=$RA_DIR/ $RA_DIR/ra64… or VAR=path
			}
			if !strings.Contains(tok, "/") {
				continue
			}
			p := expand(tok)
			if p == "" || strings.Contains(p, "$") {
				continue
			}
			if spatialFileLooks32(spatialResolve(p, cwd), cwd, depth+1, seen) {
				return true
			}
		}
	}
	return false
}
