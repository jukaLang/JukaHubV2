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
//     commented out; OS-Trimux PR #1), and the stock bluez-alsa is 3.1.0.
//     JukaHub starts it — and a bluealsa someone else
//     started without the A2DP source role is restarted with it.
//   - A headset that connected before bluealsa registered its A2DP endpoint
//     is connected without its audio profile: BlueZ says "Connected: yes" and
//     there is no stream to open. The A2DP sink profile is asked for on its
//     own (Device1.ConnectProfile), a few times per connect.
//   - alsa-lib's thread-safe locking stalls a BlueALSA stream on these boards
//     (no sound, a hang on disconnect). alsa-lib reads
//     LIBASOUND_THREAD_SAFE once, at a process's first PCM open, so it is set
//     before main runs (init below).
//   - Games open ALSA's "default", which is the speaker; RetroArch ignores
//     AUDIODEV. A game started while the headset is routed gets
//     ALSA_CONFIG_PATH = the system's alsa.conf plus a JukaHub file whose load
//     hook, run after the firmware's asound.conf, redefines "default" as the
//     headset (audioChildALSAConfig) — no firmware file is touched, and a
//     game started later on the speaker route never sees it.
//   - A bluealsa can run without its name on the system bus (2026-11-15,
//     JH-RT2AJY: "org.bluealsa was not provided by any .service files", then
//     ALSA's "No such file or directory"). It is followed to a suffixed name,
//     continued when stopped, or restarted after a grace; what a bluealsa
//     JukaHub started prints is kept for the diagnostics. An ALSA that does
//     not know the "bluealsa" PCM gets JukaHub's own definition.
//
// Every probe here runs on the output monitor's goroutine (audiooutput.go);
// the diagnostics block only reads what the probes recorded.

import (
	"errors"
	"fmt"
	"io"
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
	if strings.Contains(strings.ToLower(d.openErr), "no such file or directory") {
		// alsa-lib's ENOENT is an unknown PCM or a plugin it could not load;
		// BlueALSA's own "no such stream" is ENODEV.
		open += " (ALSA does not know the \"bluealsa\" PCM, or could not load its plugin)"
	}
	lines := [][2]string{
		{"Headset", or(d.headset, "none detected")},
		{"BlueALSA", or(d.bluealsa, "not checked (no headset yet)")},
		{"A2DP streams", or(d.streams, "not asked")},
		{"Audio route", routeLine},
		{"Headset open", open},
		{"Game audio", or(d.child, "no game started on the headset route")},
		{"RetroArch", ra},
		{"ALSA", alsa},
	}
	if svc := bluealsaService(); svc != bluealsaDefaultService {
		lines = append(lines, [2]string{"BlueALSA bus", "on D-Bus as " + svc})
	}
	bluealsaDefs.mu.Lock()
	defsNote := bluealsaDefs.note
	bluealsaDefs.mu.Unlock()
	if defsNote != "" {
		lines = append(lines, [2]string{"ALSA PCM", defsNote})
	}
	if tail := bluealsaLogTail(3); len(tail) > 0 {
		lines = append(lines, [2]string{"BlueALSA log", strings.Join(tail, " | ")})
	}
	return lines
}

// ── The daemon ───────────────────────────────────────────────────────────────

// outBluealsa remembers the last start or restart of bluealsa.
var outBluealsa struct {
	mu        sync.Mutex
	started   time.Time
	restarted time.Time
	prestarts int // bluealsaPrestart's starts this run
	// offBusPID is the daemon last found without its D-Bus name, and
	// offBusSince when that was first seen (bluealsaNotOnBus).
	offBusPID   int
	offBusSince time.Time
	// contPID is the stopped daemon last sent SIGCONT; offBusRestarts
	// counts the restarts for a missing name this run.
	contPID        int
	offBusRestarts int
	// wedgedPID is the daemon last found owning its name but not answering,
	// wedgedSince when that was first seen, wedgedRestarts the restarts for
	// it this run (bluealsaWedged).
	wedgedPID      int
	wedgedSince    time.Time
	wedgedRestarts int
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
			audioDiagSet(func() {
				audioDiag.bluealsa = "not running (JukaHub started it under a minute ago; it exited — see the log)"
			})
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
	// The D-Bus name it owns: "org.bluealsa", or a -B/--dbus suffix on it.
	bluealsaNoteDaemon(pid, args)

	// The streams bluealsa holds, when dbus-send can ask: a headset without
	// one is connected without its audio profile.
	sinks, answer := bluealsaA2DPSinks()
	audioDiagSet(func() { audioDiag.streams = answer })
	if sinks != nil && !bluealsaHas(sinks, addr) {
		a2dpConnect(addr, "bluealsa holds no A2DP stream for the headset")
	}
	if sinks != nil || !bluealsaOffBus(answer) {
		// On the bus (or not known to be off it): a later blip starts its
		// grace afresh instead of counting from a spell long past.
		outBluealsa.mu.Lock()
		outBluealsa.offBusPID, outBluealsa.offBusSince = 0, time.Time{}
		outBluealsa.mu.Unlock()
	}
	if sinks == nil && bluealsaOffBus(answer) {
		// A bluealsa process with no name on the system bus serves nothing:
		// its ALSA plugin cannot find it, so opening the headset only fails
		// (tester report JH-RT2AJY: "The name org.bluealsa was not provided
		// by any .service files", then ALSA's "No such file or directory").
		bluealsaNotOnBus(pid, args, addr)
		return false
	}
	if sinks == nil && bluealsaNoReply(answer) {
		// A bluealsa that does not answer D-Bus would hold the ALSA open (and
		// every other open waiting on audioDevEnvMu) for its 25 s timeout. It
		// was also left like that for good: on these boards bluez-alsa 3.1
		// stops answering (spruceOS saw `amixer -D bluealsa` hang there and
		// restarts it when it does not answer within 3 s), and the headset
		// stayed silent until a reboot. It is restarted now (bluealsaWedged).
		bluealsaWedged(pid, args, addr)
		return false
	}
	bluealsaAnswered(pid)
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
	if err := bluealsaSpawn(path, args); err != nil {
		logWarn("audio", "bluealsa did not start: %v", err)
		audioDiagSet(func() {
			audioDiag.bluealsa = fmt.Sprintf("not running; %s %s failed: %v", path, strings.Join(args, " "), err)
		})
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

// bluealsaLogPath keeps what a bluealsa JukaHub started printed (bluez-alsa
// logs to stderr unless told --syslog): why it could not take its D-Bus name,
// say. It is rewritten at each start; the diagnostics show its last lines.
const bluealsaLogPath = audioChildConfDir + "/bluealsa.log"

// bluealsaLogCap trims the log when a daemon keeps complaining.
const bluealsaLogCap = 256 << 10

// bluealsaSpawn starts the daemon detached, like btStartDetached, with its
// output in bluealsaLogPath (or nowhere when that cannot be made).
func bluealsaSpawn(path string, args []string) error {
	var f *os.File
	if os.MkdirAll(audioChildConfDir, 0o755) == nil {
		// O_APPEND: after the size watch truncates it, the daemon's next
		// write lands at the new end, not at its old offset in a sparse file.
		f, _ = os.OpenFile(bluealsaLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o644)
	}
	if f == nil {
		_, err := btStartDetached(path, args...)
		return err
	}
	cmd := hiddenCommand(path, args...)
	cmd.Env = screenChildEnv(os.Environ())
	cmd.Dir = "/"
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = btDetachedAttr()
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return err
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if st, err := f.Stat(); err == nil && st.Size() > bluealsaLogCap {
					_ = f.Truncate(0)
				}
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		close(done)
		_ = f.Close()
		logInfo("audio", "bluealsa exited: %v%s", err, bluealsaLogWords())
	}()
	return nil
}

// bluealsaLogTail is the last n non-empty lines of bluealsaLogPath,
// addresses masked.
func bluealsaLogTail(n int) []string {
	f, err := os.Open(bluealsaLogPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	// The end only: a daemon left running after JukaHub exits keeps writing
	// with no watch on the size.
	const tailBytes = 16 << 10
	if st, err := f.Stat(); err == nil && st.Size() > tailBytes {
		_, _ = f.Seek(st.Size()-tailBytes, io.SeekStart)
	}
	data, err := io.ReadAll(io.LimitReader(f, tailBytes))
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > 200 {
				l = string(r[:200]) + "…"
			}
			lines = append(lines, btDiagMaskAddrs(l))
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// bluealsaLogWords is "; it said: <last lines>" for a log line, or "".
func bluealsaLogWords() string {
	if tail := bluealsaLogTail(3); len(tail) > 0 {
		return "; it said: " + strings.Join(tail, " | ")
	}
	return ""
}

// bluealsaSvc is the D-Bus name the running BlueALSA owns ("" = the default),
// and that daemon's pid.
var bluealsaSvc struct {
	mu   sync.Mutex
	name string
	pid  int
}

// bluealsaNoteDaemon takes the name a newly seen daemon owns from its command
// line; for the same daemon the name found on the bus (bluealsaNotOnBus)
// stays.
func bluealsaNoteDaemon(pid int, args []string) {
	name := bluealsaArgsService(args)
	bluealsaSvc.mu.Lock()
	defer bluealsaSvc.mu.Unlock()
	if bluealsaSvc.pid != pid {
		bluealsaSvc.pid, bluealsaSvc.name = pid, name
	}
}

// bluealsaDefaultService is BlueALSA's own D-Bus name.
const bluealsaDefaultService = "org.bluealsa"

// bluealsaService is the D-Bus name to ask BlueALSA at and to name in the PCM.
func bluealsaService() string {
	bluealsaSvc.mu.Lock()
	defer bluealsaSvc.mu.Unlock()
	if bluealsaSvc.name == "" {
		return bluealsaDefaultService
	}
	return bluealsaSvc.name
}

// bluealsaSetService records the name the daemon owns.
func bluealsaSetService(name string) {
	if !strings.HasPrefix(name, bluealsaDefaultService) || strings.ContainsAny(name, ",\"' \n\\") {
		return
	}
	bluealsaSvc.mu.Lock()
	bluealsaSvc.name = name
	bluealsaSvc.mu.Unlock()
}

// bluealsaArgsService is the D-Bus name a bluealsa command line (argv[0]
// first) takes: "org.bluealsa", or "org.bluealsa.<suffix>" for -B/--dbus.
func bluealsaArgsService(args []string) string {
	for i := 1; i < len(args); i++ {
		a, v := args[i], ""
		switch {
		case a == "-B" || a == "--dbus":
			if i+1 < len(args) {
				v = args[i+1]
			}
		case strings.HasPrefix(a, "--dbus="):
			v = strings.TrimPrefix(a, "--dbus=")
		case strings.HasPrefix(a, "-B") && len(a) > 2:
			v = a[2:]
		}
		if v = strings.TrimSpace(v); v != "" {
			return bluealsaDefaultService + "." + v
		}
	}
	return bluealsaDefaultService
}

// bluealsaOffBus reports whether a dbus-send answer says no one owns the name
// asked for on the system bus.
func bluealsaOffBus(answer string) bool {
	for _, w := range []string{"ServiceUnknown", "NameHasNoOwner", "was not provided by any"} {
		if strings.Contains(answer, w) {
			return true
		}
	}
	return false
}

// bluealsaBusNames lists the org.bluealsa names on the system bus.
func bluealsaBusNames() []string {
	if !btToolExists("dbus-send") {
		return nil
	}
	out, err := btToolRun("dbus-send", "--system", "--print-reply", "--reply-timeout=2000",
		"--dest=org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.ListNames")
	if err != nil {
		return nil
	}
	return parseBluealsaBusNames(out)
}

// parseBluealsaBusNames reads the org.bluealsa names out of a ListNames reply
// (`string "org.bluealsa"` lines), each once, in order.
func parseBluealsaBusNames(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "string \"") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(line, "string \""), "\"")
		if name != bluealsaDefaultService && !strings.HasPrefix(name, bluealsaDefaultService+".") {
			continue
		}
		dup := false
		for _, n := range names {
			dup = dup || n == name
		}
		if !dup {
			names = append(names, name)
		}
	}
	return names
}

// bluealsaProcState is the state letter of /proc/<pid>/stat ("S", "T"…), ""
// when it cannot be read.
func bluealsaProcState(pid int) string {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	s := string(stat)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return ""
	}
	return s[i+2 : i+3]
}

// bluealsaOffBusGrace is how long a bluealsa may run without its D-Bus name
// before JukaHub restarts it: it takes the name as it starts, so 20 s
// without one is a daemon that never will.
const bluealsaOffBusGrace = 20 * time.Second

// bluealsaOffBusRestartMax caps those restarts per run: a daemon that can
// never take its name (no D-Bus policy for it) is not restarted every five
// minutes all evening.
const bluealsaOffBusRestartMax = 3

// bluealsaNotOnBus handles a bluealsa that runs but owns no name on the
// system bus: it follows a name it does own, wakes a stopped one, and
// restarts one that had its grace (paced like the no-source restart), with
// its output kept so the diagnostics can say why it would not take the name.
func bluealsaNotOnBus(pid int, args []string, addr string) {
	cmdline := strings.Join(args, " ")
	svc := bluealsaService()
	// The family's name under a suffix its command line does not show (a
	// wrapper started it with -B): use that one.
	for _, name := range bluealsaBusNames() {
		if name != svc {
			logInfo("audio", "bluealsa is on D-Bus as %s, not %s; the headset is opened through that", name, svc)
			bluealsaSetService(name)
			time.AfterFunc(time.Second, audioRouteKick)
			return
		}
	}
	state := bluealsaProcState(pid)
	outBluealsa.mu.Lock()
	firstCont := outBluealsa.contPID != pid
	if (state == "T" || state == "t") && firstCont {
		outBluealsa.contPID = pid
	}
	outBluealsa.mu.Unlock()
	if (state == "T" || state == "t") && firstCont && runtime.GOOS == "linux" {
		// Stopped (a job-control stop or a debugger): nothing answers until
		// it runs again. Once per daemon; one still stopped next time goes
		// the grace-and-restart way. 18 is SIGCONT on Linux; Windows' syscall
		// package has no such name, and none of this runs there.
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.Signal(18))
		}
		logWarn("audio", "bluealsa (pid %d) was stopped, so it could not answer on D-Bus; continued it", pid)
		audioDiagSet(func() {
			audioDiag.bluealsa = fmt.Sprintf("pid %d: %s — was stopped (state %s); JukaHub continued it", pid, cmdline, state)
		})
		time.AfterFunc(2*time.Second, audioRouteKick)
		return
	}
	outBluealsa.mu.Lock()
	if outBluealsa.offBusPID != pid {
		outBluealsa.offBusPID, outBluealsa.offBusSince = pid, time.Now()
		time.AfterFunc(bluealsaOffBusGrace+time.Second, audioRouteKick)
	}
	waited := time.Since(outBluealsa.offBusSince)
	recent := (!outBluealsa.restarted.IsZero() && time.Since(outBluealsa.restarted) < bluealsaRestartAfter) ||
		outBluealsa.offBusRestarts >= bluealsaOffBusRestartMax
	restart := waited >= bluealsaOffBusGrace && !recent
	if restart {
		outBluealsa.restarted = time.Now()
		outBluealsa.offBusRestarts++
	}
	outBluealsa.mu.Unlock()
	switch {
	case waited < bluealsaOffBusGrace:
		audioDiagSet(func() {
			audioDiag.bluealsa = fmt.Sprintf("pid %d: %s — not on D-Bus as %s yet (state %s, %s)", pid, cmdline, svc, state, waited.Round(time.Second))
		})
	case !restart:
		words := bluealsaLogWords()
		audioDiagSet(func() {
			audioDiag.bluealsa = fmt.Sprintf("pid %d: %s — not on D-Bus as %s (state %s); JukaHub's restarts are spent or too recent%s", pid, cmdline, svc, state, words)
		})
	default:
		path := bluealsaExePath(pid, args)
		rest := append([]string{}, args[1:]...)
		if !bluealsaArgsHaveSource(args) {
			rest = append(rest, "-p", "a2dp-source")
		}
		logWarn("audio", "bluealsa (pid %d, %s) ran %s without its D-Bus name %s; restarting it", pid, cmdline, waited.Round(time.Second), svc)
		bluealsaStop(pid, args)
		bluealsaStart(path, rest, addr, "restarted: it was not on D-Bus")
	}
}

// bluealsaWedgedGrace is how long a bluealsa that owns its D-Bus name may go
// without answering it before JukaHub restarts it: two of the monitor's 2 s
// questions in a row, about what spruceOS waits (3 s) on the same boards.
const bluealsaWedgedGrace = 5 * time.Second

// bluealsaWedgedRestartMax caps those restarts per run, so a daemon that
// hangs again at once is not restarted all evening.
const bluealsaWedgedRestartMax = 3

// bluealsaWedged handles a bluealsa that runs and owns its name but does not
// answer it (dbus-send's NoReply): a stopped one is continued once; one that
// stays silent for bluealsaWedgedGrace is restarted with the A2DP source role
// (at most bluealsaWedgedRestartMax times a run), and the headset is asked for
// its audio profile again once the new daemon registered its endpoint.
func bluealsaWedged(pid int, args []string, addr string) {
	cmdline := strings.Join(args, " ")
	state := bluealsaProcState(pid)
	outBluealsa.mu.Lock()
	firstCont := outBluealsa.contPID != pid
	if (state == "T" || state == "t") && firstCont {
		outBluealsa.contPID = pid
	}
	outBluealsa.mu.Unlock()
	if (state == "T" || state == "t") && firstCont && runtime.GOOS == "linux" {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.Signal(18)) // SIGCONT, as in bluealsaNotOnBus
		}
		logWarn("audio", "bluealsa (pid %d) was stopped, so it did not answer on D-Bus; continued it", pid)
		audioDiagSet(func() {
			audioDiag.bluealsa = fmt.Sprintf("pid %d: %s — was stopped (state %s); JukaHub continued it", pid, cmdline, state)
		})
		time.AfterFunc(2*time.Second, audioRouteKick)
		return
	}
	outBluealsa.mu.Lock()
	if outBluealsa.wedgedPID != pid {
		outBluealsa.wedgedPID, outBluealsa.wedgedSince = pid, time.Now()
		time.AfterFunc(bluealsaWedgedGrace+time.Second, audioRouteKick)
	}
	waited := time.Since(outBluealsa.wedgedSince)
	spent := outBluealsa.wedgedRestarts >= bluealsaWedgedRestartMax
	restart := waited >= bluealsaWedgedGrace && !spent
	if restart {
		outBluealsa.wedgedRestarts++
		outBluealsa.wedgedPID, outBluealsa.wedgedSince = 0, time.Time{}
		outBluealsa.restarted = time.Now()
	}
	outBluealsa.mu.Unlock()
	switch {
	case !restart && spent:
		audioDiagSet(func() {
			audioDiag.bluealsa = fmt.Sprintf("pid %d: %s — does not answer on D-Bus (state %s); JukaHub's %d restarts are spent%s",
				pid, cmdline, state, bluealsaWedgedRestartMax, bluealsaLogWords())
		})
	case !restart:
		audioDiagSet(func() {
			audioDiag.bluealsa = fmt.Sprintf("pid %d: %s — does not answer on D-Bus yet (state %s, %s)", pid, cmdline, state, waited.Round(time.Second))
		})
	default:
		path := bluealsaExePath(pid, args)
		rest := append([]string{}, args[1:]...)
		if !bluealsaArgsHaveSource(args) {
			rest = append(rest, "-p", "a2dp-source")
		}
		logWarn("audio", "bluealsa (pid %d, %s) has not answered on D-Bus for %s; restarting it", pid, cmdline, waited.Round(time.Second))
		bluealsaStop(pid, args)
		bluealsaStart(path, rest, addr, "restarted: it stopped answering")
	}
}

// ── Telling the player ───────────────────────────────────────────────────────

// audioRouteStuckAfter is how long a connected headset may stay without
// JukaHub's sound before the player is told why, once per connection.
const audioRouteStuckAfter = 45 * time.Second

// audioRouteStuck follows a connected headset JukaHub's sound has not reached:
// its address, since when, the monitor's last look, and whether the player
// was told.
var audioRouteStuck struct {
	mu    sync.Mutex
	addr  string
	since time.Time
	last  time.Time
	told  bool
}

// audioRouteNoteStuck is the monitor's look after each route decision: addr
// is the connected headset ("" for none), routed whether JukaHub's sound left
// the speaker. A headset that stays connected and unrouted for
// audioRouteStuckAfter gets one toast with what the probes found, and the
// Bluetooth description goes to the log. Before, the only sign of a headset
// JukaHub could not open was silence ("Bluetooth audio is not working still",
// 2026-11-22, no log).
func audioRouteNoteStuck(addr string, routed bool) {
	now := time.Now()
	s := &audioRouteStuck
	s.mu.Lock()
	if addr == "" || routed {
		s.addr, s.since, s.told = "", time.Time{}, false
		s.mu.Unlock()
		return
	}
	// A new headset, or the first look after a pause (a game held the
	// screen, the monitor slept): its time starts now.
	paused := !s.last.IsZero() && now.Sub(s.last) > 3*audioOutputPollInterval
	s.last = now
	if !strings.EqualFold(s.addr, addr) || paused {
		s.addr, s.since, s.told = addr, now, false
		s.mu.Unlock()
		time.AfterFunc(audioRouteStuckAfter+time.Second, audioRouteKick)
		return
	}
	if s.told || now.Sub(s.since) < audioRouteStuckAfter {
		s.mu.Unlock()
		return
	}
	s.told = true
	waited := now.Sub(s.since)
	s.mu.Unlock()
	why := audioRouteStuckReason()
	logWarn("audio", "a headset has been connected for %s, but JukaHub's sound is not on it: %s", waited.Round(time.Second), why)
	go btLogDiagnostics("a connected headset never got the sound")
	ShowToast("Bluetooth headset connected, but its sound could not be opened: "+why, ToastKindWarning)
}

// audioRouteStuckReason is what the probes found, in a few words for the
// toast; the diagnostics block keeps the whole of it.
func audioRouteStuckReason() string {
	audioDiagMu.Lock()
	d := audioDiag
	audioDiagMu.Unlock()
	bluealsaDefs.mu.Lock()
	defsNote := bluealsaDefs.note
	bluealsaDefs.mu.Unlock()
	// The part of a daemon line after its command line: "not on D-Bus…".
	daemon := d.bluealsa
	if _, rest, ok := strings.Cut(daemon, " — "); ok {
		daemon = rest
	}
	short := func(s string) string {
		s = firstLine(strings.TrimSpace(s))
		if r := []rune(s); len(r) > 140 {
			s = string(r[:140]) + "…"
		}
		return btDiagMaskAddrs(s)
	}
	switch {
	case strings.Contains(d.bluealsa, "no bluealsa/bluealsad"):
		return "this firmware has no BlueALSA (bluealsa), which every headset's sound goes through"
	case strings.Contains(defsNote, "no BlueALSA ALSA plugin"):
		return "this firmware has no BlueALSA plugin for ALSA (libasound_module_pcm_bluealsa.so)"
	case strings.Contains(d.openErr, "SDL's audio driver"):
		return short(d.openErr)
	case strings.Contains(daemon, "D-Bus"):
		return "BlueALSA " + short(daemon)
	case strings.HasPrefix(d.bluealsa, "not running"):
		// Started and gone again: its own last words say why (no D-Bus
		// policy for it: "Couldn't acquire D-Bus name").
		if tail := bluealsaLogTail(1); len(tail) > 0 {
			return short("BlueALSA stops right after it starts; it said: " + tail[0])
		}
		return "BlueALSA is " + short(d.bluealsa)
	case strings.HasPrefix(d.streams, "none"):
		return "the headset connected without its audio (A2DP) profile, and asking for it did not help"
	case d.openErr != "":
		return "ALSA said " + short(d.openErr)
	}
	return "nothing recorded why — Bluetooth's diagnostics are in the log"
}

// bluealsaAnswered notes that the daemon answered: a later silence starts its
// grace afresh.
func bluealsaAnswered(pid int) {
	outBluealsa.mu.Lock()
	if outBluealsa.wedgedPID == pid {
		outBluealsa.wedgedPID, outBluealsa.wedgedSince = 0, time.Time{}
	}
	outBluealsa.mu.Unlock()
}

// bluealsaExePath is the program a running bluealsa was started from.
func bluealsaExePath(pid int, args []string) string {
	path := ""
	if len(args) > 0 {
		path = args[0]
	}
	if exe, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe")); err == nil && filepath.IsAbs(exe) {
		return exe
	}
	if !filepath.IsAbs(path) {
		if p, ok := bluealsaBinary(); ok {
			return p
		}
	}
	return path
}

// bluealsaStop ends a running bluealsa: SIGTERM, two seconds, then SIGKILL.
func bluealsaStop(pid int, args []string) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = p.Signal(syscall.SIGTERM)
	name := "bluealsa"
	if len(args) > 0 {
		name = filepath.Base(args[0])
	}
	for i := 0; i < 20 && btProcessRunning("/proc", name); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if btProcessRunning("/proc", name) {
		_ = p.Kill()
		time.Sleep(200 * time.Millisecond)
	}
}

// ── ALSA's "bluealsa" PCM ────────────────────────────────────────────────────

// bluealsaDefs is the BlueALSA PCM definition JukaHub gave its own ALSA
// configuration (bluealsaPCMDefine): tried once per run; env is the
// ALSA_CONFIG_PATH it set ("" while none in force), file the definition file.
var bluealsaDefs struct {
	mu    sync.Mutex
	tried bool
	env   string
	file  string
	note  string
}

// bluealsaPCMFile is where that definition is written.
const bluealsaPCMFile = audioChildConfDir + "/bluealsa-pcm.conf"

// bluealsaOwnPCMName is the PCM that definition adds. A name of its own: a
// firmware "bluealsa" definition that alsa.conf's hooks load after it could
// not merge into it (bluez-alsa's argument defaults are compounds).
const bluealsaOwnPCMName = "jukahub_bluealsa"

// bluealsaPCMDefine answers an open of a "bluealsa:" PCM that failed with
// ENOENT — alsa-lib's "Unknown PCM" or "Cannot open shared library": a
// firmware with BlueALSA's ALSA plugin whose alsa.conf never loads the
// definition (no alsa.conf.d hook), or loads the plugin from a folder it is
// not in. JukaHub then defines bluealsaOwnPCMName itself, with the plugin by
// its path, in a file only its own process and the games it starts on the
// headset read (ALSA_CONFIG_PATH = the system's alsa.conf plus that file;
// alsa-lib re-reads the path at the next open), and returns the PCM name to
// try again. Once per run; never over an ALSA_CONFIG_PATH someone else set.
// A retry that still fails short of BlueALSA itself undoes it
// (bluealsaPCMUndefine).
func bluealsaPCMDefine(pcm string, openErr error) (string, bool) {
	if runtime.GOOS != "linux" || !strings.HasPrefix(pcm, "bluealsa:") || openErr == nil ||
		!strings.Contains(strings.ToLower(openErr.Error()), "no such file or directory") {
		return "", false
	}
	bluealsaDefs.mu.Lock()
	if bluealsaDefs.tried {
		bluealsaDefs.mu.Unlock()
		return "", false
	}
	bluealsaDefs.tried = true
	bluealsaDefs.mu.Unlock()
	// The work runs without bluealsaDefs.mu: a game launch and the
	// diagnostics read it, and audioDevEnvMu below can be held for a whole
	// slow open.
	env, note, ok := bluealsaPCMWrite()
	bluealsaDefs.mu.Lock()
	bluealsaDefs.note = note
	if ok {
		bluealsaDefs.env, bluealsaDefs.file = env, bluealsaPCMFile
	}
	bluealsaDefs.mu.Unlock()
	logInfo("audio", "ALSA does not know the \"bluealsa\" PCM: %s", note)
	if !ok {
		return "", false
	}
	return bluealsaOwnPCMName + strings.TrimPrefix(pcm, "bluealsa"), true
}

// bluealsaPCMWrite writes the definition and points this process's
// ALSA_CONFIG_PATH at it: the value set, what happened in words, and whether
// it is in force.
func bluealsaPCMWrite() (string, string, bool) {
	plugin := ""
	for _, p := range audioRouteALSAFiles {
		if strings.HasSuffix(p, ".so") && regularFile(p) {
			plugin = p
			break
		}
	}
	if plugin == "" {
		return "", "this firmware has no BlueALSA ALSA plugin (libasound_module_pcm_bluealsa.so), so nothing can open the headset", false
	}
	base := ""
	for _, p := range audioSystemALSAConf {
		if regularFile(p) {
			base = p
			break
		}
	}
	if base == "" {
		return "", "no system alsa.conf to add a definition to", false
	}
	if _, set := os.LookupEnv("ALSA_CONFIG_PATH"); set {
		return "", "ALSA_CONFIG_PATH is set by the firmware; JukaHub leaves it alone", false
	}
	if err := os.MkdirAll(audioChildConfDir, 0o755); err != nil {
		return "", "cannot write a definition: " + err.Error(), false
	}
	// bluez-alsa 3.x's own 20-bluealsa.conf, cut to the arguments JukaHub
	// passes, under JukaHub's name and with the plugin named by its path.
	body := "# Written by JukaHub: BlueALSA's PCM, which this firmware's ALSA setup did\n" +
		"# not define. Only JukaHub (and games it starts on the headset) read it.\n" +
		"# Safe to delete.\n" +
		"pcm_type.bluealsa {\n\tlib \"" + plugin + "\"\n}\n" +
		"pcm." + bluealsaOwnPCMName + " {\n" +
		"\t@args [ SRV DEV PROFILE ]\n" +
		"\t@args.SRV {\n\t\ttype string\n\t\tdefault \"" + bluealsaDefaultService + "\"\n\t}\n" +
		"\t@args.DEV {\n\t\ttype string\n\t\tdefault \"00:00:00:00:00:00\"\n\t}\n" +
		"\t@args.PROFILE {\n\t\ttype string\n\t\tdefault \"a2dp\"\n\t}\n" +
		"\ttype plug\n" +
		"\tslave.pcm {\n\t\ttype bluealsa\n\t\tservice $SRV\n\t\tdevice $DEV\n\t\tprofile $PROFILE\n\t}\n" +
		"}\n"
	if err := audioWriteFileAtomic(bluealsaPCMFile, body); err != nil {
		return "", "cannot write a definition: " + err.Error(), false
	}
	env := base + ":" + bluealsaPCMFile
	audioDevEnvMu.Lock()
	err := os.Setenv("ALSA_CONFIG_PATH", env)
	audioDevEnvMu.Unlock()
	if err != nil {
		return "", "cannot set ALSA_CONFIG_PATH: " + err.Error(), false
	}
	return env, fmt.Sprintf("JukaHub defined %s itself (%s, plugin %s) and opens the headset through it", bluealsaOwnPCMName, bluealsaPCMFile, plugin), true
}

// bluealsaPCMResolved reports whether an open error came from BlueALSA
// itself — no such stream yet (ENODEV), or busy — so ALSA did find the PCM
// and load its plugin.
func bluealsaPCMResolved(err error) bool {
	if err == nil {
		return true
	}
	e := strings.ToLower(err.Error())
	return strings.Contains(e, "no such device") || strings.Contains(e, "busy")
}

// bluealsaPCMUndefine takes JukaHub's definition out of force again: the
// retry through it failed before reaching BlueALSA, so it only risks every
// other ALSA open in the process.
func bluealsaPCMUndefine(why string) {
	bluealsaDefs.mu.Lock()
	env := bluealsaDefs.env
	bluealsaDefs.env = ""
	if env != "" {
		bluealsaDefs.note += "; undone, the open through it failed too: " + why
	}
	bluealsaDefs.mu.Unlock()
	if env == "" {
		return
	}
	audioDevEnvMu.Lock()
	if os.Getenv("ALSA_CONFIG_PATH") == env {
		_ = os.Unsetenv("ALSA_CONFIG_PATH")
	}
	audioDevEnvMu.Unlock()
	logInfo("audio", "JukaHub's %s definition undone: the open through it failed too: %s", bluealsaOwnPCMName, why)
}

// bluealsaOwnALSAPath is the ALSA_CONFIG_PATH bluealsaPCMDefine set for
// JukaHub, "" when none is in force.
func bluealsaOwnALSAPath() string {
	bluealsaDefs.mu.Lock()
	defer bluealsaDefs.mu.Unlock()
	return bluealsaDefs.env
}

// audioOwnALSAEnvStrip drops JukaHub's own ALSA_CONFIG_PATH from a child's
// environment: it is JukaHub's setting, not the child's, and spatial audio
// and the headset route skip a child whose environment names one.
func audioOwnALSAEnvStrip(env []string) []string {
	if own := bluealsaOwnALSAPath(); own != "" {
		if v, set := childEnvLookup(env, "ALSA_CONFIG_PATH"); set && v == own {
			return childEnvWithout(env, "ALSA_CONFIG_PATH")
		}
	}
	return env
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
			"--dest="+bluealsaService(), call[0], call[1])
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
	// JukaHub's own ALSA_CONFIG_PATH (bluealsaPCMDefine) is not the child's
	// setting: it goes, and a headset child gets it back inside its own path.
	env = audioOwnALSAEnvStrip(env)
	// Music that keeps playing under the game (musicgame.go): the child plays
	// through the mix it shares, on the speaker card or the USB DAC.
	if out, ok := musicGameChildEnv(env); ok {
		if audioThreadSafeSetByUs {
			out = childEnvWithout(out, "LIBASOUND_THREAD_SAFE")
		}
		return out
	}
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
		audioChannelsDefaultText(pcm)); err != nil { // through Audio channels' downmix when set (audiochannels.go)
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
	if bluealsaOwnALSAPath() != "" && regularFile(bluealsaPCMFile) {
		// The firmware does not define "bluealsa"; JukaHub's definition goes
		// along (bluealsaPCMDefine).
		return base + ":" + bluealsaPCMFile + ":" + path, nil
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

