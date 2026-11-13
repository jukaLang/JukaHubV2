package main

// audioroute_bluealsa.go — getting a Bluetooth headset's A2DP stream ready
// for the outputs in audioroute.go, handing it to the games, and saying in the
// diagnostics why it did not happen.
//
// The first routing pass assumed the handheld's BlueALSA was running and the
// headset's audio profile attached to it. On the TrimUI firmware neither is a
// given (2026-11-13: "headphones connect, sound still on the speakers"):
//
//   - The stock firmware does not start bluealsa at all (its start line is
//     commented out; OS-Trimux PR #1), and the stock bluez-alsa is 3.1.0
//     (spruceOS PR #1765). JukaHub starts it — and a bluealsa someone else
//     started without the A2DP source role is restarted with it.
//   - A headset that connected before bluealsa registered its A2DP endpoint
//     is connected without its audio profile: BlueZ says "Connected: yes" and
//     there is no stream to open. The A2DP sink profile is asked for on its
//     own (Device1.ConnectProfile), a few times per connect.
//   - alsa-lib's thread-safe locking stalls a BlueALSA stream on these boards
//     (NextUI #810/#811: no sound, a hang on disconnect). alsa-lib reads
//     LIBASOUND_THREAD_SAFE once, at a process's first PCM open, so it is set
//     before main runs (init below).
//   - Games open ALSA's "default", which is the speaker; RetroArch ignores
//     AUDIODEV. A game started while the headset is routed gets
//     ALSA_CONFIG_PATH = the system's alsa.conf plus a JukaHub file whose load
//     hook, run after the firmware's asound.conf, redefines "default" as the
//     headset (audioChildALSAConfig) — no firmware file is touched, and a
//     game started later on the speaker route never sees it.
//
// Every probe here runs on the output monitor's goroutine (audiooutput.go);
// the diagnostics block only reads what the probes recorded.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// bluealsaRestartAfter spaces restarts of a bluealsa that runs without
	// the A2DP source role, so a firmware that keeps starting its own does not
	// make JukaHub kill it in a loop.
	bluealsaRestartAfter = 5 * time.Minute
	// a2dpConnectEvery and a2dpConnectMax pace the requests for a connected
	// headset's missing audio profile; the count starts again on the next
	// connect.
	a2dpConnectEvery = 15 * time.Second
	a2dpConnectMax   = 3
	// bluealsaPrestartMax caps the starts ahead of a headset per run.
	bluealsaPrestartMax = 3
	// a2dpSinkUUID is the headset's side of A2DP (Audio Sink, 0x110B).
	a2dpSinkUUID = "0000110b-0000-1000-8000-00805f9b34fb"
	// audioChildConfDir holds the ALSA file games get while a headset is
	// routed. /tmp: it must not outlive the boot, and the card may be
	// read-only or slow.
	audioChildConfDir = "/tmp/jukahub-audio"
)

// audioThreadSafeSetByUs is true when the init below set
// LIBASOUND_THREAD_SAFE, so a game on the speaker route can be given the
// environment the firmware would have given it.
var audioThreadSafeSetByUs bool

func init() {
	// TrimUI only (its /usr/trimui): a desktop's PulseAudio or PipeWire ALSA
	// plugin is not BlueALSA, and the setting is not JukaHub's to change there.
	if runtime.GOOS != "linux" {
		return
	}
	if _, set := os.LookupEnv("LIBASOUND_THREAD_SAFE"); set {
		return
	}
	if fi, err := os.Stat("/usr/trimui"); err != nil || !fi.IsDir() {
		return
	}
	if os.Setenv("LIBASOUND_THREAD_SAFE", "0") == nil {
		audioThreadSafeSetByUs = true
	}
}

// ── What the diagnostics show ────────────────────────────────────────────────

// audioDiagState is what the last probes found, for the Bluetooth diagnostics
// block and the bug report (audioDiag, under audioDiagMu). Addresses are
// masked where it is printed.
type audioDiagState struct {
	headset  string    // the detected headset, its audio services and how it was found
	bluealsa string    // the daemon: pid and command line, or why there is none
	streams  string    // the A2DP streams bluealsa holds (dbus-send), or why unknown
	tried    string    // the last headset PCM opened
	openErr  string    // and its exact error, "" after a success
	route    string    // the route now
	since    time.Time // when the route last changed
	reason   string    // why it last changed
	child    string    // what the last game was given
	a2dpAddr string    // the headset the A2DP requests below count for
	a2dpN    int
	a2dpAt   time.Time
	drvNoted bool

	// noDaemonNoted: "no bluealsa here" was logged once.
	noDaemonNoted bool
}

var (
	audioDiagMu sync.Mutex
	audioDiag   audioDiagState
)

// audioDiagSet updates fields under the lock.
func audioDiagSet(set func()) {
	audioDiagMu.Lock()
	set()
	audioDiagMu.Unlock()
}

// audioDiagRoute records a route change and why it happened.
func audioDiagRoute(route, reason string) {
	audioDiagSet(func() {
		if route != audioDiag.route {
			audioDiag.since = time.Now()
		}
		audioDiag.route, audioDiag.reason = route, reason
	})
}

// audioRouteALSAFiles are where BlueALSA's ALSA plugin and its "bluealsa" PCM
// definition live on the firmwares seen so far, plus the system ALSA
// configuration that decides what "default" is.
var audioRouteALSAFiles = []string{
	"/usr/lib/alsa-lib/libasound_module_pcm_bluealsa.so",
	"/usr/lib/aarch64-linux-gnu/alsa-lib/libasound_module_pcm_bluealsa.so",
	"/usr/share/alsa/alsa.conf.d/20-bluealsa.conf",
	"/etc/alsa/conf.d/20-bluealsa.conf",
	"/usr/share/alsa/alsa.conf",
	"/etc/asound.conf",
}

// audioRouteDiagLines are the audio lines of the Bluetooth diagnostics block,
// as key/value pairs. Nothing is started here: files, the environment and
// what the monitor recorded.
func audioRouteDiagLines() [][2]string {
	outAudio.mu.Lock()
	route, failed := outAudio.pcm, outAudio.failedPCM
	outAudio.mu.Unlock()
	if route == "" {
		route = "speaker"
	}
	audioDiagMu.Lock()
	d := audioDiag
	audioDiagMu.Unlock()

	routeLine := route
	if !d.since.IsZero() {
		routeLine += fmt.Sprintf(" since %s", d.since.Format("15:04:05"))
	}
	if d.reason != "" {
		routeLine += " (" + d.reason + ")"
	}
	if failed != "" {
		routeLine += "; held: " + failed
	}
	open := "never tried"
	if d.tried != "" {
		open = d.tried
		if d.openErr != "" {
			open += " -> " + d.openErr
		} else {
			open += " -> opened"
		}
	}
	var files []string
	for _, p := range audioRouteALSAFiles {
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}
	if home := os.Getenv(childEnvOrigPrefix + "HOME"); home != "" {
		if _, err := os.Stat(filepath.Join(home, ".asoundrc")); err == nil {
			files = append(files, filepath.Join(home, ".asoundrc"))
		}
	}
	if len(files) == 0 {
		files = []string{"none of " + strings.Join(audioRouteALSAFiles, " ")}
	}
	ts, set := os.LookupEnv("LIBASOUND_THREAD_SAFE")
	if !set {
		ts = "unset"
	}
	alsa := fmt.Sprintf("SDL driver=%q LIBASOUND_THREAD_SAFE=%s files: %s", sdl.GetCurrentAudioDriver(), ts, strings.Join(files, " "))
	// RetroArch follows the headset through ALSA's "default" only while its
	// audio_device is empty or "default".
	ra := "no retroarch.cfg found"
	if text, path, ok := retroArchConfig(); ok {
		drv, _ := retroArchConfigValue(text, "audio_driver")
		dev, _ := retroArchConfigValue(text, "audio_device")
		ra = fmt.Sprintf("%s: audio_driver=%q audio_device=%q", path, drv, dev)
	}
	or := func(s, empty string) string {
		if s == "" {
			return empty
		}
		return s
	}
	return [][2]string{
		{"Headset", or(d.headset, "none detected")},
		{"BlueALSA", or(d.bluealsa, "not checked (no headset yet)")},
		{"A2DP streams", or(d.streams, "not asked")},
		{"Audio route", routeLine},
		{"Headset open", open},
		{"Game audio", or(d.child, "no game started on the headset route")},
		{"RetroArch", ra},
		{"ALSA", alsa},
	}
}

// ── The daemon ───────────────────────────────────────────────────────────────

// outBluealsa remembers the last start or restart of bluealsa.
var outBluealsa struct {
	mu        sync.Mutex
	started   time.Time
	restarted time.Time
	prestarts int // bluealsaPrestart's starts this run
}

// bluealsaProcess is the running BlueALSA daemon (bluealsa, or bluealsad from
// 4.2 on): its pid and command line, 0 and nil when none runs.
func bluealsaProcess() (int, []string) {
	for _, name := range []string{"bluealsa", "bluealsad"} {
		for _, pid := range btProcessPIDs("/proc", name) {
			b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
			if err != nil {
				continue
			}
			args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
			return pid, args
		}
	}
	return 0, nil
}

// bluealsaArgsHaveSource reports whether a bluealsa command line (argv[0]
// first) enables the A2DP source role a handheld needs to play into a
// headset. With no profile option at all, bluez-alsa 3.x enables a2dp-source,
// hfp-ag and hsp-ag (4.x refuses to start), so that counts as yes.
func bluealsaArgsHaveSource(args []string) bool {
	named := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		var v string
		switch {
		case a == "-p" || a == "--profile":
			if i+1 < len(args) {
				i++
				v = args[i]
			}
		case strings.HasPrefix(a, "--profile="):
			v = strings.TrimPrefix(a, "--profile=")
		case strings.HasPrefix(a, "-p") && len(a) > 2 && !strings.HasPrefix(a, "--"):
			v = a[2:]
		default:
			continue
		}
		named = true
		if strings.TrimSpace(v) == "a2dp-source" {
			return true
		}
	}
	return !named
}

// bluealsaBinary is the daemon to start: bluealsa, else bluealsad.
func bluealsaBinary() (string, bool) {
	if p, ok := btToolPath("bluealsa"); ok {
		return p, true
	}
	return btToolPath("bluealsad")
}

// bluealsaEnsure makes sure BlueALSA runs with the A2DP source role and holds
// the headset's stream, and reports whether opening the headset is worth a
// try now. A start or restart answers false: the headset's profile has to
// attach to the new endpoint first, and the monitor is kicked once it had the
// time to.
func bluealsaEnsure(addr string) bool {
	pid, args := bluealsaProcess()
	if pid == 0 {
		path, ok := bluealsaBinary()
		if !ok {
			noted := false
			audioDiagSet(func() {
				audioDiag.bluealsa = "not running, and no bluealsa/bluealsad on this firmware: the headset cannot be fed"
				noted, audioDiag.noDaemonNoted = audioDiag.noDaemonNoted, true
			})
			if !noted {
				logWarn("audio", "a headset is connected, but this firmware has no bluealsa: its sound stays on the speaker")
			}
			return false
		}
		outBluealsa.mu.Lock()
		recent := !outBluealsa.started.IsZero() && time.Since(outBluealsa.started) < outBluealsaRestartAfter
		if !recent {
			outBluealsa.started = time.Now()
		}
		outBluealsa.mu.Unlock()
		if recent {
			audioDiagSet(func() { audioDiag.bluealsa = "not running (JukaHub started it under a minute ago; it exited — see the log)" })
			return false
		}
		bluealsaStart(path, []string{"-p", "a2dp-source"}, addr, "started")
		return false
	}
	cmdline := strings.Join(args, " ")
	if !bluealsaArgsHaveSource(args) {
		outBluealsa.mu.Lock()
		recent := !outBluealsa.restarted.IsZero() && time.Since(outBluealsa.restarted) < bluealsaRestartAfter
		if !recent {
			outBluealsa.restarted = time.Now()
		}
		outBluealsa.mu.Unlock()
		if recent {
			audioDiagSet(func() {
				audioDiag.bluealsa = fmt.Sprintf("pid %d: %s — no a2dp-source, and a restart with it did not hold", pid, cmdline)
			})
			return true
		}
		path := args[0]
		if exe, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe")); err == nil && filepath.IsAbs(exe) {
			path = exe
		} else if !filepath.IsAbs(path) {
			if p, ok := bluealsaBinary(); ok {
				path = p
			}
		}
		logInfo("audio", "bluealsa runs without the A2DP source role (%s); restarting it with -p a2dp-source", cmdline)
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
		for i := 0; i < 20 && btProcessRunning("/proc", filepath.Base(args[0])); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		bluealsaStart(path, append(append([]string{}, args[1:]...), "-p", "a2dp-source"), addr, "restarted with -p a2dp-source")
		return false
	}
	audioDiagSet(func() { audioDiag.bluealsa = fmt.Sprintf("pid %d: %s", pid, cmdline) })

	// The streams bluealsa holds, when dbus-send can ask: a headset without
	// one is connected without its audio profile.
	sinks, answer := bluealsaA2DPSinks()
	audioDiagSet(func() { audioDiag.streams = answer })
	if sinks != nil && !bluealsaHas(sinks, addr) {
		a2dpConnect(addr, "bluealsa holds no A2DP stream for the headset")
	}
	if sinks == nil && bluealsaNoReply(answer) {
		// A bluealsa that does not answer D-Bus would hold the ALSA open (and
		// every other open waiting on audioDevEnvMu) for its 25 s timeout.
		return false
	}
	// The open is the real test either way: a stream can appear between the
	// two, and dbus-send may be missing or answer in a shape not foreseen.
	return true
}

// bluealsaPrestart starts BlueALSA while no headset is connected yet, once
// the adapter and bluetoothd are up: a headset that connects to an A2DP
// endpoint already registered gets its audio profile at once, instead of
// being connected without it and asked again (a2dpConnect).
func bluealsaPrestart() {
	if btLinkCount() < 0 || !btDaemonRunningCached() {
		return
	}
	if pid, _ := bluealsaProcess(); pid != 0 {
		return
	}
	path, ok := bluealsaBinary()
	if !ok {
		return
	}
	// A daemon that dies at once (no D-Bus policy for it, say) is started
	// ahead only a few times per run, not every minute for as long as
	// Bluetooth is on and no headset comes; a headset that connects still
	// starts it (bluealsaEnsure).
	outBluealsa.mu.Lock()
	recent := outBluealsa.prestarts >= bluealsaPrestartMax ||
		(!outBluealsa.started.IsZero() && time.Since(outBluealsa.started) < outBluealsaRestartAfter)
	if !recent {
		outBluealsa.started = time.Now()
		outBluealsa.prestarts++
	}
	outBluealsa.mu.Unlock()
	if !recent {
		bluealsaStart(path, []string{"-p", "a2dp-source"}, "", "started ahead of a headset")
	}
}

// bluealsaStart starts the daemon and, once it had the time to register its
// endpoint, asks the headset for its audio profile and wakes the monitor.
func bluealsaStart(path string, args []string, addr, what string) {
	if _, err := btStartDetached(path, args...); err != nil {
		logWarn("audio", "bluealsa did not start: %v", err)
		audioDiagSet(func() { audioDiag.bluealsa = fmt.Sprintf("not running; %s %s failed: %v", path, strings.Join(args, " "), err) })
		return
	}
	logInfo("audio", "bluealsa %s for the headset: %s %s", what, path, strings.Join(args, " "))
	audioDiagSet(func() {
		audioDiag.bluealsa = fmt.Sprintf("%s by JukaHub at %s: %s %s", what, time.Now().Format("15:04:05"), path, strings.Join(args, " "))
		audioDiag.a2dpN, audioDiag.a2dpAt = 0, time.Time{}
	})
	if addr == "" {
		return
	}
	go func() {
		time.Sleep(2 * time.Second)
		a2dpConnect(addr, "bluealsa "+what)
		audioRouteKick()
	}()
}

// ── The headset's stream ─────────────────────────────────────────────────────

// bluealsaPCMPath matches a BlueALSA playback PCM of the A2DP source role in
// its D-Bus object paths: ".../dev_AA_BB_CC_DD_EE_FF/a2dpsrc/sink" (3.x and
// 4.x alike).
var bluealsaPCMPath = regexp.MustCompile(`/dev_((?:[0-9A-Fa-f]{2}_){5}[0-9A-Fa-f]{2})/a2dpsrc/sink`)

// parseBluealsaA2DPSinks reads the headset addresses out of a dbus-send reply
// listing BlueALSA's PCMs (GetPCMs on 3.x, GetManagedObjects on 4.x),
// upper-case and ':'-separated, each once, in order.
func parseBluealsaA2DPSinks(out string) []string {
	addrs := []string{}
	seen := map[string]bool{}
	for _, m := range bluealsaPCMPath.FindAllStringSubmatch(out, -1) {
		addr := strings.ToUpper(strings.ReplaceAll(m[1], "_", ":"))
		if !seen[addr] {
			seen[addr] = true
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// bluealsaA2DPSinks asks bluealsa over D-Bus which headsets it holds an A2DP
// stream for. sinks is nil when it could not be asked; the second value is
// the answer in words for the diagnostics.
func bluealsaA2DPSinks() ([]string, string) {
	if !btToolExists("dbus-send") {
		return nil, "unknown (no dbus-send)"
	}
	var lastErr string
	for _, call := range [][]string{
		{"/org/bluealsa", "org.bluealsa.Manager1.GetPCMs"},
		{"/org/bluealsa", "org.freedesktop.DBus.ObjectManager.GetManagedObjects"},
	} {
		out, err := btToolRun("dbus-send", "--system", "--print-reply", "--reply-timeout=2000",
			"--dest=org.bluealsa", call[0], call[1])
		if err != nil {
			lastErr = firstLine(strings.TrimSpace(out))
			if lastErr == "" {
				lastErr = err.Error()
			}
			if bluealsaNoReply(lastErr) {
				break
			}
			continue
		}
		sinks := parseBluealsaA2DPSinks(out)
		if len(sinks) == 0 {
			return sinks, "none (bluealsa answered " + call[1] + " with no a2dpsrc/sink PCM)"
		}
		return sinks, strings.Join(sinks, " ")
	}
	return nil, "unknown (" + lastErr + ")"
}

// bluealsaNoReply reports whether a failed dbus-send answer means bluealsa
// is wedged (no reply in time) rather than missing a method.
func bluealsaNoReply(answer string) bool {
	for _, w := range []string{"NoReply", "Did not receive a reply", "killed", "deadline exceeded", "Timeout"} {
		if strings.Contains(answer, w) {
			return true
		}
	}
	return false
}

// bluealsaHas reports whether addrs holds addr, case aside.
func bluealsaHas(addrs []string, addr string) bool {
	for _, a := range addrs {
		if strings.EqualFold(a, addr) {
			return true
		}
	}
	return false
}

// a2dpConnect asks BlueZ to connect the headset's A2DP sink profile, paced
// (a2dpConnectEvery, at most a2dpConnectMax per connect). Device1.ConnectProfile
// through dbus-send touches nothing else the headset has connected; without
// dbus-send `bluetoothctl connect` connects whatever profile is missing.
func a2dpConnect(addr, why string) {
	if !looksLikeBluetoothAddress(addr) {
		return
	}
	audioDiagMu.Lock()
	if !strings.EqualFold(audioDiag.a2dpAddr, addr) {
		audioDiag.a2dpAddr, audioDiag.a2dpN = addr, 0
	}
	if audioDiag.a2dpN >= a2dpConnectMax || (!audioDiag.a2dpAt.IsZero() && time.Since(audioDiag.a2dpAt) < a2dpConnectEvery) {
		audioDiagMu.Unlock()
		return
	}
	audioDiag.a2dpN++
	audioDiag.a2dpAt = time.Now()
	n := audioDiag.a2dpN
	audioDiagMu.Unlock()

	var out string
	var err error
	how := "bluetoothctl connect"
	if btToolExists("dbus-send") {
		how = "Device1.ConnectProfile(A2DP sink)"
		path := "/org/bluez/" + btFirstAdapter() + "/dev_" + strings.ToUpper(strings.ReplaceAll(addr, ":", "_"))
		out, err = btToolRun("dbus-send", "--system", "--print-reply", "--reply-timeout=2500",
			"--dest=org.bluez", path, "org.bluez.Device1.ConnectProfile", "string:"+a2dpSinkUUID)
	} else {
		out, err = btToolRun("bluetoothctl", "connect", addr)
	}
	result := "ok"
	if err != nil {
		result = firstLine(strings.TrimSpace(out))
		if result == "" {
			result = err.Error()
		}
	}
	logInfo("audio", "%s: asked for the headset's audio profile (%s, try %d): %s", why, how, n, btDiagMaskAddrs(result))
	audioDiagSet(func() {
		audioDiag.streams += fmt.Sprintf("; %s try %d: %s", how, n, result)
	})
}

// btFirstAdapter is the first adapter the kernel lists ("hci0" when it lists
// none).
func btFirstAdapter() string {
	for _, name := range btSysfsNames(btSysfsRoot) {
		if btIsHCIName(name) {
			return name
		}
	}
	return "hci0"
}

// audioRouteSDLDriverOK reports whether SDL's audio driver can take an ALSA
// PCM name through AUDIODEV. Any other driver (OSS's "dsp" reads AUDIODEV as
// a device file) would only fail the open, so it is said once instead.
func audioRouteSDLDriverOK() bool {
	drv := sdl.GetCurrentAudioDriver()
	if strings.EqualFold(drv, "alsa") {
		return true
	}
	audioDiagMu.Lock()
	noted := audioDiag.drvNoted
	audioDiag.drvNoted = true
	audioDiag.tried, audioDiag.openErr = "(not opened)", fmt.Sprintf("SDL's audio driver is %q, not alsa: it cannot open a BlueALSA PCM", drv)
	audioDiagMu.Unlock()
	if !noted {
		logWarn("audio", "SDL's audio driver is %q, not alsa: JukaHub's sound cannot be moved to the headset", drv)
	}
	return false
}

// ── Games ────────────────────────────────────────────────────────────────────

// audioRouteChildEnv is env for a screen-taking child (a game, an emulator,
// an app). While the headset is routed it gets AUDIODEV (SDL programs) and
// ALSA_CONFIG_PATH = the system's alsa.conf plus a JukaHub file redefining
// "default" as the headset (programs that open "default", RetroArch among
// them), unless its environment names either already. On the speaker route a
// LIBASOUND_THREAD_SAFE that JukaHub set for itself is taken out again, so the
// child runs as the firmware's menu would have started it.
func audioRouteChildEnv(env []string) []string {
	pcm := audioRouteChildPCM()
	if pcm == "" {
		if audioThreadSafeSetByUs {
			env = childEnvWithout(env, "LIBASOUND_THREAD_SAFE")
		}
		return env
	}
	given := []string{}
	if _, set := childEnvLookup(env, "AUDIODEV"); !set {
		env = append(env, "AUDIODEV="+pcm)
		given = append(given, "AUDIODEV")
	}
	if _, set := childEnvLookup(env, "ALSA_CONFIG_PATH"); !set {
		if path, err := audioChildALSAConfig(pcm); err == nil {
			env = append(env, "ALSA_CONFIG_PATH="+path)
			given = append(given, "ALSA_CONFIG_PATH="+path)
		} else {
			given = append(given, "no ALSA default ("+err.Error()+")")
		}
	}
	if _, set := childEnvLookup(env, "LIBASOUND_THREAD_SAFE"); !set {
		env = append(env, "LIBASOUND_THREAD_SAFE=0")
	}
	note := fmt.Sprintf("%s at %s: %s", pcm, time.Now().Format("15:04:05"), strings.Join(given, ", "))
	audioDiagSet(func() { audioDiag.child = note })
	return env
}

// childEnvWithout is env without any entry for name.
func childEnvWithout(env []string, name string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if key, _, _ := strings.Cut(kv, "="); key == name {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// audioSystemALSAConf is the system alsa.conf, which ALSA_CONFIG_PATH must
// name first: it loads /etc/asound.conf, ~/.asoundrc and the conf.d files
// (BlueALSA's PCM definition among them).
var audioSystemALSAConf = []string{"/usr/share/alsa/alsa.conf", "/usr/local/share/alsa/alsa.conf"}

// audioChildALSAConfig writes the file that makes "default" the headset and
// returns the ALSA_CONFIG_PATH value naming it after the system's alsa.conf.
func audioChildALSAConfig(pcm string) (string, error) {
	if strings.ContainsAny(pcm, "\"\n\\") {
		return "", errors.New("odd PCM name")
	}
	base := ""
	for _, p := range audioSystemALSAConf {
		if _, err := os.Stat(p); err == nil {
			base = p
			break
		}
	}
	if base == "" {
		return "", errors.New("no system alsa.conf")
	}
	if err := os.MkdirAll(audioChildConfDir, 0o755); err != nil {
		return "", err
	}
	// alsa-lib reads every ALSA_CONFIG_PATH file first and only then runs
	// alsa.conf's @hooks, which load /etc/asound.conf and ~/.asoundrc: a
	// "default" written straight into a path file would be overridden by the
	// firmware's (or, without its "!", merged into it and broken). So the path
	// file only adds a second load hook, run after the system's hook 0, and
	// that hook loads the file redefining "default".
	def := filepath.Join(audioChildConfDir, "asound-headset-default.conf")
	if err := audioWriteFileAtomic(def, "# Written by JukaHub: ALSA's \"default\" is the Bluetooth headset for the\n"+
		"# game it starts while one is routed. Rewritten on every launch; safe to delete.\n"+
		"pcm.!default {\n\ttype plug\n\tslave.pcm \""+pcm+"\"\n}\n"); err != nil {
		return "", err
	}
	path := filepath.Join(audioChildConfDir, "asound-headset.conf")
	if err := audioWriteFileAtomic(path, "# Written by JukaHub for the game it starts while a Bluetooth headset is\n"+
		"# connected: ALSA_CONFIG_PATH names it after the system's alsa.conf, so only\n"+
		"# that game reads it. Hook 1 runs after alsa.conf's hook 0 (asound.conf,\n"+
		"# .asoundrc), so the headset \"default\" is loaded last. Safe to delete.\n"+
		"@hooks.1 {\n\tfunc load\n\tfiles [ \""+def+"\" ]\n\terrors false\n}\n"); err != nil {
		return "", err
	}
	return base + ":" + path, nil
}

// audioWriteFileAtomic writes body to path through a temporary file, so a game
// starting at that moment never reads half a file.
func audioWriteFileAtomic(path, body string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
