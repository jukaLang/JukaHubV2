package main

import "testing"

// A PIN copied the way the console groups it ("1234 5678") is the same PIN, and
// what is left still has to be the console's 8 digits before pairing starts.
func TestPSPanelNormalizePIN(t *testing.T) {
	for in, want := range map[string]string{
		"12345678":    "12345678",
		" 1234 5678 ": "12345678",
		"1234-5678":   "12345678",
		"":            "",
	} {
		if got := psPanelNormalizePIN(in); got != want {
			t.Errorf("psPanelNormalizePIN(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ps4ParsePin(psPanelNormalizePIN("1234 567")); err == nil {
		t.Error("a 7-digit PIN must be refused before pairing")
	}
	if _, err := ps4ParsePin(psPanelNormalizePIN("1234 5678")); err != nil {
		t.Errorf("a grouped 8-digit PIN must be accepted, got %v", err)
	}
}

// The address field takes an IP or a host name. A typo that cannot be an
// address goes back to the keyboard instead of into three handshake timeouts.
func TestPSPanelValidHost(t *testing.T) {
	for host, want := range map[string]bool{
		"192.168.1.51":     true,
		"fe80::1":          true,
		"ps5-living.local": true,
		"PS5":              true,
		"":                 false,
		"192.168.1. 51":    false,
		"http://ps5":       false,
		".local":           false,
		"-ps5":             false,
	} {
		if got := psPanelValidHost(host); got != want {
			t.Errorf("psPanelValidHost(%q) = %v, want %v", host, got, want)
		}
	}
}
