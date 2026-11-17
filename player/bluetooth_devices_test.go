package main

// Kept from the Bluetooth page's tests when the suite was cut to its core:
// what a nearby device can make the bluetoothctl session do.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A device's name is free text that bluetoothctl prints after the address, so
// a neighbour named "... Pairing successful" must not finish a pairing step or
// start a search.
func TestBzDeviceNamesNeverAnswerAStep(t *testing.T) {
	neighbour := "[NEW] Device 11:22:33:44:55:66 "
	for _, line := range []string{
		neighbour + "Pairing successful",
		neighbour + "Connection successful",
		neighbour + "Device has been removed",
		neighbour + "Discovery started",
	} {
		if bzAnswerHas(line, "Pairing successful", "Connection successful", "Device has been removed", "Discovery started") {
			t.Errorf("a device's name answered a step: %q", line)
		}
		if done, _ := bzScanStarted(line); done {
			t.Errorf("a device's name started a search: %q", line)
		}
	}
}

// The bring-up and the top bar read three things the system writes: the
// Bluetooth class directory's names (adapters and live links), a firmware
// script's first line (what JukaHub runs it with) and a /proc stat line (a
// zombie bluetoothd is not a running one).
func TestBtSystemReadings(t *testing.T) {
	if a, l := btSysfsCount([]string{"hci0", "hci0:11", "hci0:12", "hci1", "rfkill3", "hcix", "hci0:", "hci0:1a"}); a != 2 || l != 2 {
		t.Errorf("btSysfsCount = %d adapters, %d links; want 2, 2", a, l)
	}
	for _, c := range []struct {
		first, name string
		args        []string
		ok          bool
	}{
		{"#!/bin/sh /etc/rc.common", "/bin/sh", []string{"/etc/rc.common", "/s"}, true},
		{"#!/bin/sh\r", "/bin/sh", []string{"/s"}, true},
		{"echo no shebang", "/bin/sh", []string{"/s"}, true},
		{"#!/usr/bin/python3", "", nil, false},
		{"#! /tmp/evil", "", nil, false},
	} {
		name, args, ok := btShebangCommand(c.first, "/s")
		if ok != c.ok || name != c.name || strings.Join(args, "|") != strings.Join(c.args, "|") {
			t.Errorf("btShebangCommand(%q) = %q %q %v; want %q %q %v", c.first, name, args, ok, c.name, c.args, c.ok)
		}
	}
	for stat, live := range map[string]bool{
		"812 (bluetoothd) S 1 812 812 0 -1": true,
		"812 (bluetoothd) Z 1 812 812 0 -1": false,
		"9 (odd) name) R 1 9 9 0 -1":        true,
		"9 (odd) Z) name) X 1 9 9 0 -1":     false,
		"garbage":                           false,
	} {
		if got := btProcStatLive(stat); got != live {
			t.Errorf("btProcStatLive(%q) = %v; want %v", stat, got, live)
		}
	}
	// BlueALSA's PCM list as dbus-send prints it: only the A2DP source role's
	// playback PCMs name a headset to play into.
	reply := `   array [
      dict entry(
         object path "/org/bluealsa/hci0/dev_00_1b_66_AA_BB_CC/a2dpsrc/sink"
      )
      dict entry(
         object path "/org/bluealsa/hci0/dev_11_22_33_44_55_66/hfpag/sink"
      )
      dict entry(
         object path "/org/bluealsa/hci0/dev_00_1B_66_AA_BB_CC/a2dpsrc/sink"
      )
   ]`
	if got := strings.Join(parseBluealsaA2DPSinks(reply), " "); got != "00:1B:66:AA:BB:CC" {
		t.Errorf("parseBluealsaA2DPSinks = %q; want 00:1B:66:AA:BB:CC", got)
	}
	// A bluealsa command line plays into a headset only with a2dp-source, which
	// bluez-alsa 3.x enables when no profile is named at all.
	for _, c := range []struct {
		cmdline string
		source  bool
	}{
		{"bluealsa", true},
		{"bluealsa -S --keep-alive=5", true},
		{"bluealsa -p a2dp-sink", false},
		{"bluealsa -p a2dp-sink -p a2dp-source", true},
		{"bluealsa --profile=a2dp-sink", false},
		{"bluealsa -pa2dp-source", true},
		{"/usr/bin/bluealsa --profile hfp-ag", false},
	} {
		cmdline, source := c.cmdline, c.source
		if got := bluealsaArgsHaveSource(strings.Fields(cmdline)); got != source {
			t.Errorf("bluealsaArgsHaveSource(%q) = %v; want %v", cmdline, got, source)
		}
	}
	// The D-Bus name a command line takes, and the BlueALSA names a
	// ListNames reply holds.
	for _, c := range [][2]string{
		{"bluealsa -p a2dp-source", "org.bluealsa"},
		{"bluealsa -B trimui -p a2dp-source", "org.bluealsa.trimui"},
		{"bluealsa --dbus=sink", "org.bluealsa.sink"},
		{"bluealsa -Bx", "org.bluealsa.x"},
	} {
		if got := bluealsaArgsService(strings.Fields(c[0])); got != c[1] {
			t.Errorf("bluealsaArgsService(%q) = %q; want %q", c[0], got, c[1])
		}
	}
	names := `method return time=1 sender=org.freedesktop.DBus -> destination=:1.9 serial=3 reply_serial=2
   array [
      string "org.freedesktop.DBus"
      string "org.bluez"
      string "org.bluealsa.trimui"
      string "org.bluealsatool"
      string "org.bluealsa.trimui"
   ]`
	if got := strings.Join(parseBluealsaBusNames(names), " "); got != "org.bluealsa.trimui" {
		t.Errorf("parseBluealsaBusNames = %q; want org.bluealsa.trimui", got)
	}
	if !bluealsaOffBus("Error org.freedesktop.DBus.Error.ServiceUnknown: The name org.bluealsa was not provided by any .service files") ||
		bluealsaOffBus("Error org.freedesktop.DBus.Error.NoReply: Did not receive a reply") {
		t.Error("bluealsaOffBus mistook a missing name for a wedged daemon or back")
	}
}

// Nothing but a Bluetooth address is typed into a session after a command
// word: a line break or a second command inside what claims to be an address
// is refused before any session starts.
func TestBzValidMACRefusesAnythingElse(t *testing.T) {
	if mac, ok := bzValidMAC(" aa:bb:cc:dd:ee:0f "); !ok || mac != "AA:BB:CC:DD:EE:0F" {
		t.Errorf("bzValidMAC(lower case, spaces) = %q, %v; want AA:BB:CC:DD:EE:0F, true", mac, ok)
	}
	for _, bad := range []string{
		"",
		"AA:BB:CC:DD:EE",
		"AA:BB:CC:DD:EE:FF:00",
		"AA-BB-CC-DD-EE-FF",
		"AA:BB:CC:DD:EE:GG",
		"AA:BB:CC:DD:EE:FF\nremove 11:22:33:44:55:66",
		"AA:BB:CC:DD:EE:FF; power off",
	} {
		if mac, ok := bzValidMAC(bad); ok {
			t.Errorf("bzValidMAC(%q) accepted %q", bad, mac)
		}
	}
}

// What the kernel and BlueALSA say about a connected device (bt_padinfo.go):
// a pad battery by percent or by level word, never the handheld's own
// battery; the order pads connected in; a headset's codec by name or number.
func TestBtDeviceFactsFromTheSystem(t *testing.T) {
	root := t.TempDir()
	write := func(dir string, files map[string]string) {
		for name, body := range files {
			path := filepath.Join(root, dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("ps-controller-battery-aa_bb_cc_dd_ee_01", map[string]string{"type": "Battery", "scope": "Device", "capacity": "80", "status": "Discharging"})
	write("nintendo_switch_controller_battery_x", map[string]string{"type": "Battery", "scope": "Device", "capacity_level": "Low",
		"device/uevent": "HID_NAME=Pro Controller\nHID_UNIQ=aa:bb:cc:dd:ee:02"})
	write("axp2202-battery", map[string]string{"type": "Battery", "capacity": "50"})
	got := btBatteryByMAC(btPadBatteriesIn(root))
	if len(got) != 2 || got["AA:BB:CC:DD:EE:01"].Percent != 80 || got["AA:BB:CC:DD:EE:02"].Percent != 15 ||
		got["AA:BB:CC:DD:EE:02"].Name != "Pro Controller" {
		t.Errorf("batteries = %+v", got)
	}

	keys := "7fdb000000000000 0 0 0 0" // a DualShock 4's buttons
	devs := []bzInputDev{
		{Bus: 0x05, Name: "Second", Uniq: "aa:bb:cc:dd:ee:02", KeyBits: keys, Handlers: "event9 js2"},
		{Bus: 0x05, Name: "First", Uniq: "aa:bb:cc:dd:ee:01", KeyBits: keys, Handlers: "kbd event4 js1"},
	}
	if slots := btPadSlots(devs, 64); slots["AA:BB:CC:DD:EE:01"] != 1 || slots["AA:BB:CC:DD:EE:02"] != 2 {
		t.Errorf("slots = %v", slots)
	}

	reply := `   array [
      dict entry(
         object path "/org/bluealsa/hci0/dev_AA_BB_CC_DD_EE_03/a2dpsrc/sink"
         array [
            dict entry(
               string "Codec"
               variant                   string "AAC"
            )`
	if c := parseBluealsaCodec(reply, "aa:bb:cc:dd:ee:03"); c != "AAC" {
		t.Errorf("codec = %q, want AAC", c)
	}
	if c := parseBluealsaCodec(strings.Replace(reply, `string "AAC"`, "uint16 0", 1), "AA:BB:CC:DD:EE:03"); c != "SBC" {
		t.Errorf("numbered codec = %q, want SBC", c)
	}
	if c := parseBluealsaCodec(reply, "AA:BB:CC:DD:EE:04"); c != "" {
		t.Errorf("another headset's codec = %q", c)
	}
}
