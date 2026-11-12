package main

import "testing"

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
