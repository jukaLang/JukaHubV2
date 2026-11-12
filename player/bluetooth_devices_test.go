package main

// Kept from the Bluetooth page's tests when the suite was cut to its core:
// what a nearby device can make the bluetoothctl session do.

import (
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
