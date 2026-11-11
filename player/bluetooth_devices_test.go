package main

// Kept from the Bluetooth page's tests when the suite was cut to its core:
// what a nearby device can make the bluetoothctl session do.

import "testing"

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
