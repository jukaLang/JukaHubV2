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
	"os"
	"path"
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
// plays, or "" when the speakers are it.
func connectedAudioOutput() string {
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
	return strings.Contains(info, "Audio Sink")
}

// bluetoothAudioName picks the first connected device that advertises an audio
// sink. infoFor is injected so the selection is testable without bluetoothctl.
func bluetoothAudioName(devicesOut string, infoFor func(string) string) string {
	if infoFor == nil {
		return ""
	}
	for _, d := range parseBluetoothDevices(devicesOut) {
		if bluetoothInfoIsAudioSink(infoFor(d.addr)) {
			return d.name
		}
	}
	return ""
}

// systemBluetoothAudioName asks BlueZ for the connected device and checks that
// it is an audio sink. A missing bluetoothctl, a radio that is off, or a daemon
// that does not answer all mean "no wireless output" rather than an error worth
// surfacing.
func systemBluetoothAudioName() string {
	if !commandExists("bluetoothctl") {
		return ""
	}
	out, err := systemCommandRunner("bluetoothctl", "devices", "Connected")
	if err != nil {
		return ""
	}
	return bluetoothAudioName(out, func(addr string) string {
		info, err := systemCommandRunner("bluetoothctl", "info", addr)
		if err != nil {
			return ""
		}
		return info
	})
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

// startAudioOutputMonitor probes the output once after startup and then every
// audioOutputPollInterval. The ticker lives on its own goroutine: bluetoothctl
// is a known slow daemon and must never block a frame.
func startAudioOutputMonitor() {
	go func() {
		// Let the first frame and the deferred startup work finish first.
		time.Sleep(2 * time.Second)
		probeAudioOutput()
		ticker := time.NewTicker(audioOutputPollInterval)
		defer ticker.Stop()
		for range ticker.C {
			probeAudioOutput()
		}
	}()
}

// probeAudioOutput refreshes the cached output name from the host. Bluetooth
// wins: a headset that is connected is what the user is listening through, and
// a jack on a device with a radio is usually the fallback.
func probeAudioOutput() {
	if name := systemBluetoothAudioName(); name != "" {
		rememberAudioOutput(name)
		return
	}
	if headphoneJackPlugged(os.ReadFile) {
		rememberAudioOutput("Headphones")
		return
	}
	rememberAudioOutput("")
}
