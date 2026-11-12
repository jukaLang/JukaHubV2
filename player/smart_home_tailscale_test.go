package main

import (
	"strings"
	"testing"
)

// TestParseTailscaleStatusPeerList covers NET-18: the row must show this
// device's own address and list the other nodes with name, address and online
// state, in a stable order.
func TestParseTailscaleStatusPeerList(t *testing.T) {
	data := []byte(`{
		"BackendState": "Running",
		"Self": {"HostName": "handheld", "TailscaleIPs": ["100.64.0.5"]},
		"Peer": {
			"key-b": {"HostName": "phone", "DNSName": "phone.tailnet.ts.net.", "TailscaleIPs": ["100.64.0.9"], "Online": false},
			"key-a": {"HostName": "laptop", "DNSName": "laptop.tailnet.ts.net.", "TailscaleIPs": ["100.64.0.7"], "Online": true}
		}
	}`)
	snap, err := parseTailscaleStatus(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.IPs) != 1 || snap.IPs[0] != "100.64.0.5" {
		t.Fatalf("self address = %v, want [100.64.0.5]", snap.IPs)
	}
	if len(snap.Peers) != 2 {
		t.Fatalf("peers = %+v, want 2", snap.Peers)
	}
	if snap.Peers[0].Name != "laptop.tailnet.ts.net" || !snap.Peers[0].Online {
		t.Fatalf("first peer = %+v, want the online laptop", snap.Peers[0])
	}
	if snap.Peers[1].Name != "phone.tailnet.ts.net" || snap.Peers[1].Online {
		t.Fatalf("second peer = %+v, want the offline phone", snap.Peers[1])
	}
	line := tailscalePeersLine(snap.Peers)
	for _, want := range []string{"laptop.tailnet.ts.net (100.64.0.7, online)", "phone.tailnet.ts.net (100.64.0.9, offline)"} {
		if !strings.Contains(line, want) {
			t.Fatalf("peer line %q does not name %q", line, want)
		}
	}
}
