package main

// Tests for the output detection behind the idle chip: BlueZ's device list, the
// kernel's jack switch, and the name cleaning that keeps the bar safe.

import (
	"errors"
	"strings"
	"testing"
)

// pinAudioOutput holds the detected output for one test and restores it, so a
// named headset never leaks into another test's chip.
func pinAudioOutput(t *testing.T, name string) {
	t.Helper()
	prev := connectedAudioOutput()
	rememberAudioOutput(name)
	t.Cleanup(func() { rememberAudioOutput(prev) })
}

func TestCleanAudioOutputName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"WH-1000XM4", "WH-1000XM4"},
		{"  Bose   QuietComfort \n ", "Bose QuietComfort"},
		{"Bad\x00Name\x07", "BadName"},
		{"Line\nBreak", "Line Break"},
		{strings.Repeat("A", audioOutputNameMax+20), strings.Repeat("A", audioOutputNameMax)},
	}
	for _, c := range cases {
		if got := cleanAudioOutputName(c.in); got != c.want {
			t.Errorf("cleanAudioOutputName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRememberAudioOutputRoundTrip(t *testing.T) {
	pinAudioOutput(t, "Sony WH-1000XM4")
	if got := connectedAudioOutput(); got != "Sony WH-1000XM4" {
		t.Fatalf("connectedAudioOutput = %q, want the pinned name", got)
	}
	rememberAudioOutput("  \x01 ")
	if got := connectedAudioOutput(); got != "" {
		t.Fatalf("a blank name stayed in the chip slot: %q", got)
	}
}

func TestParseBluetoothDevices(t *testing.T) {
	out := "Device AA:BB:CC:DD:EE:FF WH-1000XM4\n" +
		"Device 11:22:33:44:55:66\n" +
		"Device not-a-mac Some Speaker\n" +
		"[bluetooth]# \n" +
		"Device aa:bb:cc:dd:ee:01  Game   Pad \n"
	devices := parseBluetoothDevices(out)
	if len(devices) != 2 {
		t.Fatalf("parsed %d devices, want 2: %+v", len(devices), devices)
	}
	if devices[0].addr != "AA:BB:CC:DD:EE:FF" || devices[0].name != "WH-1000XM4" {
		t.Errorf("first device = %+v", devices[0])
	}
	if devices[1].addr != "aa:bb:cc:dd:ee:01" || devices[1].name != "Game Pad" {
		t.Errorf("second device = %+v", devices[1])
	}
	if len(parseBluetoothDevices("")) != 0 {
		t.Error("empty output produced devices")
	}
}

func TestLooksLikeBluetoothAddress(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"AA:BB:CC:DD:EE:FF", true},
		{"aa:bb:cc:dd:ee:ff", true},
		{"AA:BB:CC:DD:EE:F", false},
		{"AA-BB-CC-DD-EE-FF", false},
		{"AA:BB:CC:DD:EE:FG", false},
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikeBluetoothAddress(c.addr); got != c.want {
			t.Errorf("looksLikeBluetoothAddress(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

// A connected controller is not an output: only a device advertising the A2DP
// sink is advertised by the chip.
func TestBluetoothAudioNameNeedsAnAudioSink(t *testing.T) {
	out := "Device AA:BB:CC:DD:EE:FF Pro Controller\n" +
		"Device AA:BB:CC:DD:EE:01 WH-1000XM4\n"
	infos := map[string]string{
		"AA:BB:CC:DD:EE:FF": "Device AA:BB:CC:DD:EE:FF\n  Name: Pro Controller\n  UUID: Human Interface Device (00001124-...)\n",
		"AA:BB:CC:DD:EE:01": "Device AA:BB:CC:DD:EE:01\n  Name: WH-1000XM4\n  UUID: Audio Sink (0000110b-...)\n",
	}
	infoFor := func(addr string) string { return infos[addr] }
	if got := bluetoothAudioName(out, infoFor); got != "WH-1000XM4" {
		t.Fatalf("bluetoothAudioName = %q, want WH-1000XM4", got)
	}

	// Nothing audio-capable: no output.
	onlyController := "Device AA:BB:CC:DD:EE:FF Pro Controller\n"
	if got := bluetoothAudioName(onlyController, infoFor); got != "" {
		t.Fatalf("a controller was named as the output: %q", got)
	}
	if got := bluetoothAudioName(out, nil); got != "" {
		t.Fatalf("a nil info reader named %q", got)
	}
	if !bluetoothInfoIsAudioSink(infos["AA:BB:CC:DD:EE:01"]) {
		t.Error("an Audio Sink device was not recognised")
	}
}

// The jack switch: any non-zero state means plugged, a missing switch is a
// normal "no cable", and the search falls through the known names.
func TestHeadphoneJackPlugged(t *testing.T) {
	read := func(files map[string]string) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if data, ok := files[path]; ok {
				return []byte(data), nil
			}
			return nil, errors.New("no such file")
		}
	}
	if !headphoneJackPlugged(read(map[string]string{"/sys/class/switch/h2ph/state": "1\n"})) {
		t.Error("a plugged h2ph jack was not detected")
	}
	if headphoneJackPlugged(read(map[string]string{"/sys/class/switch/h2ph/state": "0\n"})) {
		t.Error("an unplugged h2ph jack was reported as plugged")
	}
	if !headphoneJackPlugged(read(map[string]string{"/sys/class/switch/headphone/state": "2\n"})) {
		t.Error("a headset-with-mic state (2) was not detected")
	}
	if !headphoneJackPlugged(read(map[string]string{"/sys/class/switch/headset/state": "true\n"})) {
		t.Error("a boolean jack state was not detected")
	}
	if headphoneJackPlugged(read(nil)) {
		t.Error("a device with no jack switch reported headphones")
	}
	if headphoneJackPlugged(nil) {
		t.Error("a nil reader reported headphones")
	}
}
