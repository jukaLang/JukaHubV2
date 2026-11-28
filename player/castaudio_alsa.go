package main

// castaudio_alsa.go — a game's own sound reaches Screen Share's viewers.
//
// The stock firmware has no loopback device to record what plays, so the
// sound is copied on its way out instead. A child started while sharing is on
// with sound (screengroup_unix.go, the last of the audio steps) gets
//
//	ALSA_CONFIG_PATH = <the system's alsa.conf>[:<earlier steps' files>]:/tmp/jukahub-audio/asound-share.conf
//
// whose hook — the next free index after alsa.conf's hook 0 and any hook an
// earlier step (spatial audio, music in games, the headset route) added — makes
// ALSA's "default" the chain
//
//	jhshare (plug, to 16-bit) -> jhshare_tap (file plugin) -> jhshare_out (plug) -> what "default" was
//
// The file plugin writes everything played into the pipe of
// `JukaHub --share-audio-tap <port>` (castaudio_tap.go) and passes it on
// unchanged, so the game hears no difference. "What default was" cannot be
// named once "default" is redefined, and the stock firmware's own
// /etc/asound.conf redefines it (a softvol "Game Volume" over a dmix), which is
// why spatial audio and music in games leave such a firmware alone. So the
// files that define it — the system's, in the order alsa.conf's hook 0 loads
// them, then the earlier steps' hook files — are read a second time inside a
// compound of their own (`jhprev { </etc/asound.conf> … }`), and the chain
// ends in "jhprev.pcm.default": a dotted name resolves from the top of the
// configuration, the way alsa.conf's own "cards.pcm.default" does, so it is
// exactly the definition the program would have played through, and every
// name it refers to is still the global one. With no file defining it, the
// chain ends in "sysdefault", the card's own default. A program that names
// AUDIODEV (SDL ones) is moved to "jhshare" only when an earlier step pointed
// it at the "default" it had taken over; a chain that ends in "default" itself
// (spatial audio on such a firmware) reaches the tap through it.
//
// The 16-bit plug is there because the plugin's WAV header cannot tell
// 32-bit float from 32-bit integer samples; the rate and the channel count are
// the program's own, read by the tap from that header.
//
// A route that did not work could leave a game silent, so nothing is given to
// a child before a check has proven it on this device (castTapProbeCheck,
// once per run, in the background, when sharing with sound is first switched
// on): a copy of JukaHub started as `JukaHub --share-audio-probe` must open
// its sound through the same kind of chain — the system's default files read
// inside the compound, the tap started by the plugin — ending in ALSA's "null"
// PCM, and the tap's datagrams must reach JukaHub. Until it has, launches play
// as before. Programs that bring their own ALSA setup, their own libasound or
// are 32-bit (whose alsa-lib may be older) are left alone, like spatial audio
// leaves them.
//
// The route is for the TrimUI firmware (/usr/trimui); JUKAHUB_SHARE_TAP=1
// turns it on elsewhere (development), =0 off.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// castTapEnv forces the route on (1) or off (0).
	castTapEnv = "JUKAHUB_SHARE_TAP"
	// castTapProbeFlag starts this binary as the check's child.
	castTapProbeFlag = "--share-audio-probe"
	// castTapPCM is the chain's head; its stages are castTapPCM+"_tap" and
	// castTapPCM+"_out". castTapPrevNS is the compound holding the second
	// reading of the files that define "default".
	castTapPCM    = "jhshare"
	castTapPrevNS = "jhprev"
	// The launch's two files and the check's one, beside the other steps'
	// (audioChildConfDir: in RAM, gone at the next boot).
	castTapConfName      = "asound-share.conf"
	castTapDefaultName   = "asound-share-default.conf"
	castTapProbeConfName = "asound-share-probe.conf"
	// castTapProbeTimeout bounds the check's child; castTapProbeAfter keeps
	// the check out of the start-up rush.
	castTapProbeTimeout = 15 * time.Second
	castTapProbeAfter   = 10 * time.Second
	// castTapRetryAfter / castTapRetryMax: a check that failed for a passing
	// reason runs again that much later, that many times.
	castTapRetryAfter = 5 * time.Minute
	castTapRetryMax   = 3
	// castTapProbeBlocks is how many datagrams the check must receive.
	castTapProbeBlocks = 3
)

// castTapApplies reports whether this device uses the route (cached: it
// cannot change while JukaHub runs).
var castTapApplies = sync.OnceValue(func() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(castTapEnv))) {
	case "0", "off", "false", "no":
		return false
	case "1", "on", "true", "yes":
		return true
	}
	fi, err := os.Stat("/usr/trimui")
	return err == nil && fi.IsDir()
})

// castTapLogged keeps the one-per-run notes.
var castTapLogged struct {
	mu   sync.Mutex
	seen map[string]bool
}

func castTapLogOnce(key, format string, args ...any) {
	castTapLogged.mu.Lock()
	if castTapLogged.seen == nil {
		castTapLogged.seen = map[string]bool{}
	}
	first := !castTapLogged.seen[key]
	castTapLogged.seen[key] = true
	castTapLogged.mu.Unlock()
	if first {
		logInfo("cast", format, args...)
	}
}

// ── The check ─────────────────────────────────────────────────────────────

// The check's states.
const (
	castTapUnknown = iota
	castTapRunning
	castTapOK
	castTapNo
)

var castTapProbe struct {
	mu      sync.Mutex
	state   int
	why     string
	retry   time.Time
	retries int
}

// castTapProbeState is the check's answer: ok, the reason when not, and
// whether there is an answer yet.
func castTapProbeState() (ok bool, why string, known bool) {
	castTapProbe.mu.Lock()
	defer castTapProbe.mu.Unlock()
	switch castTapProbe.state {
	case castTapOK:
		return true, "", true
	case castTapNo:
		return false, castTapProbe.why, true
	}
	return false, "", false
}

// castTapProbeKick starts the check when it has not run (or a passing failure
// is due again). Cheap; any goroutine.
func castTapProbeKick() {
	if !castTapApplies() || time.Since(spatialALSAStarted) < castTapProbeAfter {
		return
	}
	p := &castTapProbe
	p.mu.Lock()
	due := p.state == castTapUnknown || (p.state == castTapNo && !p.retry.IsZero() && time.Now().After(p.retry))
	if due {
		p.state = castTapRunning
	}
	p.mu.Unlock()
	if due {
		go castTapProbeRun()
	}
}

// castTapProbeRun runs the check and records its answer.
func castTapProbeRun() {
	ok, why, passing := false, "the check stopped unexpectedly", true
	defer func() {
		if r := recover(); r != nil {
			logWarn("cast", "the game sound check failed: %v", r)
		}
		p := &castTapProbe
		p.mu.Lock()
		p.retry = time.Time{}
		if ok {
			p.state, p.why = castTapOK, ""
		} else {
			p.state, p.why = castTapNo, why
			if passing && p.retries < castTapRetryMax {
				p.retries++
				p.retry = time.Now().Add(castTapRetryAfter)
			}
		}
		p.mu.Unlock()
		if ok {
			logInfo("cast", "game sound for Screen Share works: games started while sharing send their sound too")
		} else {
			logWarn("cast", "game sound for Screen Share is not available: %s", why)
		}
	}()
	ok, why, passing = castTapProbeCheck()
}

// castTapProbeCheck is the check itself; passing marks a failure worth
// another try later.
func castTapProbeCheck() (ok bool, why string, passing bool) {
	base := spatialALSABaseConf()
	if base == "" {
		return false, "this system has no alsa.conf", false
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return false, "cannot listen on the loopback address: " + err.Error(), true
	}
	defer conn.Close()
	var got atomic.Int32
	go castTapReadLoop(conn, func(_ uint32, rate, channels int, _ []byte) {
		if rate == 48000 && channels == 2 {
			got.Add(1)
		}
	})
	files := castTapSystemDefaultFiles(os.Getenv("HOME"))
	text, err := castTapConfText(files, castTapPrevNS+".pcm.jhprobe_end", "pcm.jhprobe_end {\n\t\ttype null\n\t}",
		conn.LocalAddr().(*net.UDPAddr).Port, "", 0)
	if err != nil {
		return false, err.Error(), false
	}
	conf, err := spatialALSAWriteConf(castTapProbeConfName, text)
	if err != nil {
		return false, "could not write the check's ALSA file: " + err.Error(), true
	}
	if err := castTapRunProbeChild(base + ":" + conf); err != nil {
		var passingErr *spatialProbePassingError
		if errors.As(err, &passingErr) {
			return false, err.Error(), true
		}
		return false, "a test program could not play through the tap (" + err.Error() + ")", false
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for got.Load() < castTapProbeBlocks && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got.Load() < castTapProbeBlocks {
		return false, "the test program played, but its sound never reached JukaHub (the tap did not start)", false
	}
	return true, "", false
}

// castTapRunProbeChild starts this binary with castTapProbeFlag, the check's
// ALSA_CONFIG_PATH and AUDIODEV naming the chain, and returns why it failed.
func castTapRunProbeChild(configPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return &spatialProbePassingError{"the check could not start: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), castTapProbeTimeout)
	defer cancel()
	cmd := hiddenCommandContext(ctx, exe, castTapProbeFlag)
	env := spatialEnvWithout(os.Environ(), "AUDIODEV", "ALSA_CONFIG_PATH", "SDL_AUDIODRIVER")
	cmd.Env = append(env, "ALSA_CONFIG_PATH="+configPath, "AUDIODEV="+castTapPCM, "SDL_AUDIODRIVER=alsa")
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

// handleCastTapProbeCommand runs the check's child (main.go, before SDL
// starts): open SDL audio through the chain, play a moment of a quiet tone,
// report.
func handleCastTapProbeCommand(args []string) (handled bool, exitCode int) {
	if len(args) == 0 || args[0] != castTapProbeFlag {
		return false, 0
	}
	if err := castTapProbeMain(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return true, 1
	}
	fmt.Println("ok")
	return true, 0
}

func castTapProbeMain() error {
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
		return fmt.Errorf("opening the share chain: %w", err)
	}
	defer sdl.CloseAudioDevice(dev)
	// 400 ms of a quiet 440 Hz tone: never heard (the chain ends in "null"),
	// but not silence either.
	const frames = 48000 * 2 / 5
	pcm := make([]byte, frames*4)
	for i := 0; i < frames; i++ {
		v := uint16(int16(2000 * math.Sin(2*math.Pi*440*float64(i)/48000)))
		pcm[4*i], pcm[4*i+1], pcm[4*i+2], pcm[4*i+3] = byte(v), byte(v>>8), byte(v), byte(v>>8)
	}
	if err := sdl.QueueAudio(dev, pcm); err != nil {
		return fmt.Errorf("queueing: %w", err)
	}
	sdl.PauseAudioDevice(dev, false)
	time.Sleep(500 * time.Millisecond)
	if sdl.GetAudioDeviceStatus(dev) == sdl.AUDIO_STOPPED {
		return errors.New("the share chain stopped while playing")
	}
	return nil
}

// ── The files ─────────────────────────────────────────────────────────────

// castTapSystemDefaultFiles are the system files that define "default", in
// the order alsa.conf's hook 0 loads them (the configuration folders, then
// asound.conf, then the home's .asoundrc), so the last one read is the one in
// force. home is the program's HOME.
func castTapSystemDefaultFiles(home string) []string {
	var cands []string
	for _, dir := range []string{"/var/lib/alsa/conf.d", "/usr/share/alsa/alsa.conf.d", "/usr/etc/alsa/conf.d", "/etc/alsa/conf.d"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.conf"))
		sort.Strings(matches)
		cands = append(cands, matches...)
	}
	if regularFile("/etc/asound.conf") {
		cands = append(cands, "/etc/asound.conf")
	} else {
		cands = append(cands, "/usr/etc/asound.conf")
	}
	if home != "" {
		cands = append(cands, filepath.Join(home, ".asoundrc"), filepath.Join(home, ".config", "alsa", "asoundrc"))
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range cands {
		if seen[p] || !regularFile(p) {
			continue
		}
		seen[p] = true
		raw, err := os.ReadFile(p)
		if err != nil || len(raw) > 1<<20 {
			continue
		}
		if spatialALSADefaultRE.MatchString(spatialALSAStripComments(string(raw))) {
			out = append(out, p)
		}
	}
	return out
}

// castTapPriorHooks reads the hooks the earlier steps' path files add
// ("@hooks.N { … files [ "<file>" ] … }", the form every JukaHub step writes):
// the files they load, in hook order, and the highest index used.
func castTapPriorHooks(paths []string) (files []string, last int) {
	type hook struct {
		idx  int
		file string
	}
	var hooks []hook
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil || len(raw) > 1<<20 {
			continue
		}
		text := spatialALSAStripComments(string(raw))
		for {
			at := strings.Index(text, "@hooks.")
			if at < 0 {
				break
			}
			text = text[at+len("@hooks."):]
			end := 0
			for end < len(text) && text[end] >= '0' && text[end] <= '9' {
				end++
			}
			idx, err := strconv.Atoi(text[:end])
			if err != nil {
				continue
			}
			block := text[end:]
			if cl := strings.Index(block, "}"); cl >= 0 {
				block = block[:cl]
			}
			if f := strings.Index(block, "files"); f >= 0 {
				rest := block[f:]
				if q := strings.Index(rest, "\""); q >= 0 {
					if q2 := strings.Index(rest[q+1:], "\""); q2 >= 0 {
						hooks = append(hooks, hook{idx, rest[q+1 : q+1+q2]})
					}
				}
			}
			last = max(last, idx)
		}
	}
	sort.SliceStable(hooks, func(i, j int) bool { return hooks[i].idx < hooks[j].idx })
	for _, h := range hooks {
		files = append(files, h.file)
	}
	return files, last
}

// castTapIncludable reports whether path can be named in an ALSA include.
func castTapIncludable(path string) bool {
	return filepath.IsAbs(path) && spatialALSAQuotable(path) && !strings.ContainsAny(path, "<>")
}

// castTapShellSafe reports whether s can sit inside single quotes in the tap's
// command line, inside an ALSA string the file plugin expands % codes in.
func castTapShellSafe(s string) bool {
	return !strings.ContainsAny(s, "'\"\\%\n\r\x00")
}

// castTapCommand is the file plugin's pipe: the tap with JukaHub's own
// library path (the program's is the firmware's, which may not hold
// JukaHub's libraries), and cat draining the stream if the tap cannot run.
func castTapCommand(port int) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	ld := os.Getenv("LD_LIBRARY_PATH")
	if !castTapShellSafe(exe) || !castTapShellSafe(ld) {
		return "", errors.New("JukaHub's folder cannot be named in an ALSA file")
	}
	return fmt.Sprintf("| LD_PRELOAD= LD_LIBRARY_PATH='%s' JUKAHUB_BOOT_PREFETCH=0 '%s' %s %d || exec cat >/dev/null",
		ld, exe, castAudioTapFlag, port), nil
}

// castTapConfText is the path file: the compound reading prevFiles a second
// time (plus extra, the check's own end), the chain ending in end with the tap
// sending to port, and — when defaultFile is set — hook hookIdx loading it.
func castTapConfText(prevFiles []string, end, extra string, port int, defaultFile string, hookIdx int) (string, error) {
	pipe, err := castTapCommand(port)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# Written by JukaHub (castaudio_alsa.go) for a program it starts while Screen\n")
	b.WriteString("# Share sends sound: ALSA_CONFIG_PATH names it after the system's alsa.conf, so\n")
	b.WriteString("# only that program reads it. Rewritten at every launch; safe to delete.\n")
	if len(prevFiles) > 0 || extra != "" {
		// What "default" was, read a second time under a name of its own.
		fmt.Fprintf(&b, "%s {\n", castTapPrevNS)
		for _, f := range prevFiles {
			if !castTapIncludable(f) {
				return "", fmt.Errorf("the file %q cannot be named in an ALSA file", f)
			}
			fmt.Fprintf(&b, "\t<%s>\n", f)
		}
		if extra != "" {
			fmt.Fprintf(&b, "\t%s\n", extra)
		}
		b.WriteString("}\n")
	}
	fmt.Fprintf(&b, "pcm.%s_out {\n\ttype plug\n\tslave.pcm \"%s\"\n}\n", castTapPCM, end)
	fmt.Fprintf(&b, "pcm.%s_tap {\n\ttype file\n\tslave.pcm \"%s_out\"\n\tfile \"%s\"\n\tformat \"wav\"\n}\n", castTapPCM, castTapPCM, pipe)
	fmt.Fprintf(&b, "pcm.%s {\n\ttype plug\n\tslave {\n\t\tpcm \"%s_tap\"\n\t\tformat S16_LE\n\t}\n}\n", castTapPCM, castTapPCM)
	if defaultFile != "" {
		b.WriteString("# Hooks run in index order, so this \"default\" is the last one loaded.\n")
		fmt.Fprintf(&b, "@hooks.%d {\n\tfunc load\n\tfiles [ \"%s\" ]\n\terrors false\n}\n", hookIdx, defaultFile)
	}
	return b.String(), nil
}

// castTapLaunchConf writes a launch's files and returns the ALSA_CONFIG_PATH
// value, and the AUDIODEV to give the program ("" to leave it as it is).
func castTapLaunchConf(env []string) (path, audiodev string, err error) {
	base := spatialALSABaseConf()
	if base == "" {
		return "", "", errors.New("no system alsa.conf")
	}
	var prior []string
	if v, set := childEnvLookup(env, "ALSA_CONFIG_PATH"); set && v != "" {
		parts := strings.Split(v, ":")
		if parts[0] != base {
			return "", "", errors.New("the program brings its own ALSA setup")
		}
		for _, p := range parts[1:] {
			if p == "" {
				continue
			}
			if filepath.Dir(p) != audioChildConfDir {
				return "", "", errors.New("the program brings its own ALSA setup")
			}
			prior = append(prior, p)
		}
	}
	hookFiles, lastHook := castTapPriorHooks(prior)
	home, _ := childEnvLookup(env, "HOME")
	prev := append(castTapSystemDefaultFiles(home), hookFiles...)
	end, capture := "sysdefault", "sysdefault"
	if len(prev) > 0 {
		end = castTapPrevNS + ".pcm.default"
		capture = end
	} else if !spatialALSAHasSysdefault(base) {
		return "", "", errors.New("this system's ALSA has no sysdefault")
	}
	port := castTapEnsure()
	if port == 0 {
		return "", "", errors.New("no loopback port to send the sound to")
	}
	def, err := spatialALSAWriteConf(castTapDefaultName, "# Written by JukaHub (castaudio_alsa.go): ALSA's \"default\" also sends its\n"+
		"# sound to Screen Share, for the program it starts. Safe to delete.\n"+
		"pcm.!default {\n\ttype asym\n\tplayback.pcm \""+castTapPCM+"\"\n\tcapture.pcm \""+capture+"\"\n}\n")
	if err != nil {
		return "", "", err
	}
	text, err := castTapConfText(prev, end, "", port, def, lastHook+1)
	if err != nil {
		return "", "", err
	}
	conf, err := spatialALSAWriteConf(castTapConfName, text)
	if err != nil {
		return "", "", err
	}
	// An earlier step that took "default" over pointed AUDIODEV at the same
	// chain; it now starts at the tap. Otherwise AUDIODEV stays: unset or
	// "default" is the tap already, a chain ending in "default" reaches it,
	// and a program's own device is its own.
	if v, set := childEnvLookup(env, "AUDIODEV"); set && v != "" && v != "default" && len(hookFiles) > 0 {
		audiodev = castTapPCM
	}
	parts := append([]string{base}, prior...)
	return strings.Join(append(parts, conf), ":"), audiodev, nil
}

// castAudioChildEnv is env for the screen-taking child cmd (screengroup_unix.go,
// after the other audio steps): with Screen Share on, its sound on and the
// route proven, the program's sound also goes to the viewers. Anything else
// returns env as it was: a launch never waits for or fails on the share.
func castAudioChildEnv(cmd *exec.Cmd, env []string) []string {
	if !castTapApplies() || !castIsEnabled() || !castLive.sound.Load() {
		return env
	}
	if castGamesSupported() && !castLive.games.Load() {
		// Games too is off: viewers see a pause card, and hear nothing of
		// the game either.
		return env
	}
	ok, why, known := castTapProbeState()
	if !known {
		castTapProbeKick()
		castTapLogOnce("pending", "game sound for Screen Share: the sound route is still being checked; this program's sound stays on the handheld")
		return env
	}
	if !ok {
		castTapLogOnce("no", "game sound for Screen Share is not available: %s", why)
		return env
	}
	if cmd != nil && spatialLaunchLooks32(cmd) {
		castTapLogOnce("32:"+strings.Join(cmd.Args, " "), "game sound for Screen Share skips %s: a 32-bit program", strings.Join(cmd.Args, " "))
		return env
	}
	if cmd != nil {
		if lib := spatialLaunchBundledALSA(cmd); lib != "" {
			castTapLogOnce("lib:"+lib, "game sound for Screen Share skips %s: it brings its own ALSA library (%s)", strings.Join(cmd.Args, " "), lib)
			return env
		}
	}
	path, audiodev, err := castTapLaunchConf(env)
	if err != nil {
		castTapLogOnce("err:"+err.Error(), "game sound for Screen Share skips this program: %v", err)
		return env
	}
	env = append(spatialEnvWithout(env, "ALSA_CONFIG_PATH"), "ALSA_CONFIG_PATH="+path)
	if audiodev != "" {
		env = append(spatialEnvWithout(env, "AUDIODEV"), "AUDIODEV="+audiodev)
	}
	logInfo("cast", "this program's sound also goes to Screen Share (%s)", path)
	return env
}
