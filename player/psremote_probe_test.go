package main

import (
	"net"
	"testing"
)

// TestPSProbeAcceptsOnlyThisConsole covers PSR-11: a discovery answer from
// another PlayStation at the same private address on another network is not
// this console, while one unknown MAC cannot settle it either way.
func TestPSProbeAcceptsOnlyThisConsole(t *testing.T) {
	pairing := psConsole{Host: "192.168.0.10", ServerMAC: "AA:BB:CC:DD:EE:FF"}
	cases := []struct {
		name   string
		pair   psConsole
		answer psDiscovered
		want   bool
	}{
		{
			name: "the same console answers", pair: pairing,
			answer: psDiscovered{HostID: "aa-bb-cc-dd-ee-ff"}, want: true,
		},
		{
			name: "another console answers", pair: pairing,
			answer: psDiscovered{HostID: "11:22:33:44:55:66"}, want: false,
		},
		{
			name: "the answer names no MAC", pair: pairing,
			answer: psDiscovered{}, want: true,
		},
		{
			name: "the pairing kept no MAC", pair: psConsole{Host: "192.168.0.10"},
			answer: psDiscovered{HostID: "11:22:33:44:55:66"}, want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := psProbeAcceptsConsole(tc.pair, tc.answer); got != tc.want {
				t.Fatalf("psProbeAcceptsConsole = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPSStreamHostHomeOrAway covers PSR-9: a console that answered at home is
// streamed there, one that did not goes to its internet address, and the
// console Play hands to the event thread (psConsoleAt) keeps that choice
// whatever is asked of it, so nothing there probes again.
func TestPSStreamHostHomeOrAway(t *testing.T) {
	both := psConsole{Host: "192.168.0.10", RemoteHost: "203.0.113.7"}
	cases := []struct {
		name       string
		c          psConsole
		home       bool
		wantHost   string
		wantRemote bool
	}{
		{name: "home", c: both, home: true, wantHost: "192.168.0.10"},
		{name: "away", c: both, home: false, wantHost: "203.0.113.7", wantRemote: true},
		{name: "no internet address", c: psConsole{Host: "192.168.0.10"}, home: false, wantHost: "192.168.0.10"},
		{name: "home, handed on", c: psConsoleAt(both, "192.168.0.10", false), home: false, wantHost: "192.168.0.10"},
		{name: "away, handed on", c: psConsoleAt(both, "203.0.113.7", true), home: true, wantHost: "203.0.113.7", wantRemote: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, remote := psStreamHostFor(tc.c, tc.home)
			if host != tc.wantHost || remote != tc.wantRemote {
				t.Fatalf("psStreamHostFor = %q, %v; want %q, %v", host, remote, tc.wantHost, tc.wantRemote)
			}
		})
	}
}

// TestXboxDiscoveryAnswer covers the X1 pill's dot (xremote_discovery.go): a
// SmartGlass 0xDD01 answer names the console at the address it came from,
// and anything else off the network, cut short or not an answer, is no Xbox.
func TestXboxDiscoveryAnswer(t *testing.T) {
	from := &net.UDPAddr{IP: net.IPv4(192, 168, 0, 20), Port: xboxDiscoveryPort}
	answer := []byte{0xdd, 0x01, 0x00, 0x20, 0x00, 0x02, 0x00, 0x00, 0x00, 0x02, 0x00, 0x01}
	answer = append(answer, 0x00, 0x07)
	answer = append(answer, "Den Box"...)
	answer = append(answer, 0x00, 0x00, 0x04)
	answer = append(answer, "ABCD"...)
	answer = append(answer, 0x00)

	c, ok := xboxParseDiscoveryResponse(answer, from)
	if !ok || c.Host != "192.168.0.20" || c.Name != "Den Box" || c.ID != "ABCD" {
		t.Fatalf("answer = %+v, %v; want Den Box (ABCD) at 192.168.0.20", c, ok)
	}
	for name, payload := range map[string][]byte{
		"empty":         nil,
		"request":       xboxDiscoveryPacket(),
		"name cut":      answer[:16],
		"length cut":    answer[:13],
		"other message": append([]byte{0xcc, 0x01}, answer[2:]...),
	} {
		if _, ok := xboxParseDiscoveryResponse(payload, from); ok {
			t.Errorf("%s: parsed as an Xbox", name)
		}
	}
}
