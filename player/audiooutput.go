package main

// audiooutput.go — which output the ears are on.
//
// The top-right chip has two jobs. While something plays it names the media;
// while nothing does, it names the output instead, so a Bluetooth headset that
// just connected (or a cable that was pulled) is visible at a glance without
// opening Settings. That needs a probe of its own: BlueZ for wireless devices
// and the kernel's jack switch for the 3.5mm cable.
//
// Both are host probes, so they run on a background ticker and the result is
// cached: the render thread only ever reads a string. A device with neither a
// radio nor a jack switch reports nothing, which is a normal state, not an
// error.

import (
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// audioOutputPollInterval is how often the output is re-probed. Connecting a
	// headset or pulling a cable is a deliberate act, so ten seconds is
	// responsive enough while keeping bluetoothctl off the CPU in between.
	audioOutputPollInterval = 10 * time.Second
	// audioOutputNameMax caps the stored name. The chip ellipsizes to a quarter
	// of the screen anyway; the cap keeps a firmware-supplied name with control
	// characters or a paragraph from reaching the bar.
	audioOutputNameMax = 64
)

// audioOutputState is the cached detection result. It is written by the
// background monitor and read by the header on every frame, so it is guarded.
var audioOutputState struct {
	mu   sync.Mutex
	name string
}

// rememberAudioOutput records the detected output, or "" for the built-in
// speakers. The monitor and the tests share this one entry point, so the header
// never reads a half-written name.
func rememberAudioOutput(name string) {
	name = cleanAudioOutputName(name)
	audioOutputState.mu.Lock()
	audioOutputState.name = name
	audioOutputState.mu.Unlock()
}

// connectedAudioOutput names the output the chip should advertise while nothing
// plays, or "" when the speakers are it. Screenshot mode names none: a headset
// is often called after its owner (screenshotmode.go).
func connectedAudioOutput() string {
	if shotModeOn() {
		return ""
	}
	audioOutputState.mu.Lock()
	defer audioOutputState.mu.Unlock()
	return audioOutputState.name
}

// cleanAudioOutputName trims a device name and drops anything that is not
// printable, so a firmware-supplied name cannot smuggle newlines or control
// characters into the bar. Runs of whitespace collapse to one space, and a name
// longer than the cap is cut on a rune boundary.
func cleanAudioOutputName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			// Drop control characters outright.
		default:
			b.WriteRune(r)
		}
	}
	clean := strings.Join(strings.Fields(b.String()), " ")
	runes := []rune(clean)
	if len(runes) > audioOutputNameMax {
		clean = strings.TrimSpace(string(runes[:audioOutputNameMax]))
	}
	return clean
}

// ── Bluetooth ──────────────────────────────────────────────────────────────

// btDevice is one connected device as `bluetoothctl devices Connected` prints
// it.
type btDevice struct {
	addr string
	name string
}

// parseBluetoothDevices reads bluetoothctl's "Device AA:BB:CC:DD:EE:FF Name"
// lines. A line without a name or with something that is not a MAC address is
// skipped: a chip that says only an address is worse than no chip.
func parseBluetoothDevices(out string) []btDevice {
	var devices []btDevice
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Device ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "Device "))
		split := strings.IndexAny(rest, " \t")
		if split <= 0 {
			continue
		}
		addr := rest[:split]
		name := cleanAudioOutputName(rest[split+1:])
		if !looksLikeBluetoothAddress(addr) || name == "" {
			continue
		}
		devices = append(devices, btDevice{addr: addr, name: name})
	}
	return devices
}

// looksLikeBluetoothAddress checks the MAC form bluetoothctl prints, so a
// firmware that answers with something else is ignored instead of probed.
func looksLikeBluetoothAddress(addr string) bool {
	if len(addr) != 17 {
		return false
	}
	for i, r := range addr {
		if i%3 == 2 {
			if r != ':' {
				return false
			}
			continue
		}
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'F', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// bluetoothInfoIsAudioSink reports whether `bluetoothctl info` says the device
// can receive audio (the A2DP sink service). Without this check a connected
// game controller or phone would be advertised as the output.
func bluetoothInfoIsAudioSink(info string) bool {
	return strings.Contains(info, "Audio Sink") || strings.Contains(strings.ToLower(info), "0000110b-")
}

// bluetoothInfoAudioServices names the audio services `bluetoothctl info`
// lists (A2DP sink, HFP, HSP), for the diagnostics: a headset that offers
// only hands-free cannot take A2DP.
func bluetoothInfoAudioServices(info string) string {
	lower := strings.ToLower(info)
	var out []string
	for _, s := range []struct{ id, name string }{
		{"0000110b-", "A2DP sink"}, {"0000111e-", "HFP"}, {"00001108-", "HSP"}, {"0000110e-", "AVRCP"},
	} {
		if strings.Contains(lower, s.id) {
			out = append(out, s.name)
		}
	}
	if len(out) == 0 {
		return "no audio service listed"
	}
	return strings.Join(out, ", ")
}

// bluetoothAudioDevice picks the first connected device that advertises an
// audio sink. infoFor is injected so the selection is testable without
// bluetoothctl.
func bluetoothAudioDevice(devicesOut string, infoFor func(string) string) btDevice {
	if infoFor == nil {
		return btDevice{}
	}
	for _, d := range parseBluetoothDevices(devicesOut) {
		if bluetoothInfoIsAudioSink(infoFor(d.addr)) {
			return d
		}
	}
	return btDevice{}
}

// systemBluetoothAudioDevice asks BlueZ for the connected device and checks
// that it is an audio sink. A missing bluetoothctl, a radio that is off, or a
// daemon that does not answer all mean "no wireless output" rather than an
// error worth surfacing.
//
// `devices Connected` only exists from BlueZ 5.65 on. The handhelds' 5.54
// refuses the argument ("Too many arguments") and an in-between build may
// ignore it and list every device, so without it the paired devices are asked
// instead, and only one whose info says "Connected: yes" counts either way.
func systemBluetoothAudioDevice() btDevice {
	if !btToolExists("bluetoothctl") {
		return btDevice{}
	}
	// No live link in the kernel's list (bluetooth_quick.go): nothing is
	// connected, and no process needs to start to say so.
	if btLinkCount() == 0 {
		audioDiagSet(func() { audioDiag.headset = "no live Bluetooth link" })
		return btDevice{}
	}
	out, err := btToolRun("bluetoothctl", "devices", "Connected")
	how := "devices Connected"
	if err != nil || len(parseBluetoothDevices(out)) == 0 {
		how = "paired-devices"
		if out, err = btToolRun("bluetoothctl", "paired-devices"); err != nil {
			audioDiagSet(func() {
				audioDiag.headset = "bluetoothctl paired-devices failed: " + firstLine(strings.TrimSpace(out+" "+err.Error()))
			})
			return btDevice{}
		}
	}
	var seen []string
	d := bluetoothAudioDevice(out, func(addr string) string {
		info, err := btToolRun("bluetoothctl", "info", addr)
		if err != nil || !strings.Contains(info, "Connected: yes") {
			return ""
		}
		seen = append(seen, addr+" ("+bluetoothInfoAudioServices(info)+")")
		return info
	})
	note := fmt.Sprintf("no connected audio sink via bluetoothctl %s (connected: %s)", how, strings.Join(seen, "; "))
	if len(seen) == 0 {
		note = fmt.Sprintf("nothing connected per bluetoothctl %s (%d listed)", how, len(parseBluetoothDevices(out)))
	}
	if d.addr != "" {
		note = fmt.Sprintf("%s %q via bluetoothctl %s; connected: %s", d.addr, d.name, how, strings.Join(seen, "; "))
	}
	audioDiagSet(func() { audioDiag.headset = note })
	return d
}

// bluealsaHeldHeadset is the headset BlueALSA holds an A2DP stream for while
// bluetoothctl named none (spruce routes by that alone): only asked on the
// handhelds, while a link is up and bluealsa runs.
func bluealsaHeldHeadset() btDevice {
	if runtime.GOOS != "linux" || !IsTrimuiDevice() || btLinkCount() <= 0 {
		return btDevice{}
	}
	if pid, _ := bluealsaProcess(); pid == 0 {
		return btDevice{}
	}
	sinks, _ := bluealsaA2DPSinks()
	if len(sinks) == 0 {
		return btDevice{}
	}
	audioDiagSet(func() { audioDiag.headset += "; bluealsa holds A2DP for " + sinks[0] + " (used)" })
	return btDevice{addr: sinks[0], name: "Bluetooth audio"}
}

// ── Wired jack ─────────────────────────────────────────────────────────────

// headphoneJackSwitches are the sysfs switch names kernels use for the 3.5mm
// jack, in the order handhelds expose them.
var headphoneJackSwitches = []string{"h2ph", "h2ph_switch", "headphone", "headset", "hp"}

// headphoneJackPlugged reports whether the kernel's jack switch says a cable is
// in. readFile is injected so the parse is testable; a device with no jack
// switch is simply "not plugged", never an error. Some drivers report 2 for a
// headset with a microphone, so anything other than 0 counts as plugged.
func headphoneJackPlugged(readFile func(string) ([]byte, error)) bool {
	if readFile == nil {
		return false
	}
	for _, name := range headphoneJackSwitches {
		// Sysfs paths are always POSIX, even when the tests run on Windows.
		data, err := readFile(path.Join("/sys/class/switch", name, "state"))
		if err != nil {
			continue
		}
		state := strings.TrimSpace(string(data))
		if state == "" || state == "0" || strings.EqualFold(state, "false") {
			return false
		}
		return true
	}
	return false
}

// ── Monitor ────────────────────────────────────────────────────────────────

// audioOutputLinkPoll is how often the kernel's Bluetooth link list is read
// (a directory listing, no process). A change there — a headset connecting or
// dropping — triggers the full probe at once, so sound moves to the headset
// within a couple of seconds instead of up to audioOutputPollInterval.
const audioOutputLinkPoll = 2 * time.Second

// btLinkCount is the number of live Bluetooth links the kernel lists, or -1
// when it lists no adapters directory at all (no Bluetooth, not Linux).
func btLinkCount() int {
	names := btSysfsNames(btSysfsRoot)
	if names == nil {
		return -1
	}
	_, links := btSysfsCount(names)
	return links
}

// startAudioOutputMonitor probes the output once after startup, then whenever
// the link count changes, the route is kicked (audioRouteKick) or
// audioOutputPollInterval has passed. The loop lives on its own goroutine:
// bluetoothctl is a known slow daemon and must never block a frame.
func startAudioOutputMonitor() {
	go func() {
		// Let the first frame and the deferred startup work finish first.
		time.Sleep(2 * time.Second)
		probeAudioOutput()
		lastLinks, lastProbe := btLinkCount(), time.Now()
		var linkChangedAt time.Time
		ticker := time.NewTicker(audioOutputLinkPoll)
		defer ticker.Stop()
		wake := subscribeForegroundWake()
		for {
			force := false
			select {
			case <-ticker.C:
			case <-wake:
				// Back from a game: take the headset back now.
				force = true
			case <-audioRouteKicks:
				force = true
			}
			// bluetoothctl is a daemon call; skipping it while a game owns the
			// screen also stops JukaHub from holding the Bluetooth stack (and
			// the headset, audioroute.go) the game wants. See backgroundwork.go.
			// The screen saver's low-power mode pauses the slow 10 s probe only:
			// a headset that connects or drops then (music plays on under the
			// saver) still moves the sound, the link list being a cheap read.
			if gameScreenSuspended() || windowMinimised() {
				continue
			}
			links := btLinkCount()
			if links != lastLinks {
				linkChangedAt = time.Now()
			}
			// A headset's audio is often not ready the instant its link
			// appears, so under the saver keep probing for a short while after
			// a change rather than once.
			if backgroundPaused() && links == lastLinks && time.Since(linkChangedAt) > 3*audioOutputPollInterval {
				continue
			}
			if !force && links == lastLinks && time.Since(lastProbe) < audioOutputPollInterval {
				continue
			}
			probeAudioOutput()
			lastLinks, lastProbe = links, time.Now()
		}
	}()
}

// probeAudioOutput refreshes the cached output name from the host and moves
// JukaHub's sound to a connected headset (audioroute.go). Bluetooth wins: a
// headset that is connected is what the user is listening through, and a jack
// on a device with a radio is usually the fallback.
func probeAudioOutput() {
	headset := systemBluetoothAudioDevice()
	if headset.addr == "" {
		headset = bluealsaHeldHeadset()
	}
	audioRouteUpdate(headset.addr)
	if headset.name != "" {
		rememberAudioOutput(headset.name)
		return
	}
	if headphoneJackPlugged(os.ReadFile) {
		rememberAudioOutput("Headphones")
		return
	}
	rememberAudioOutput("")
}
