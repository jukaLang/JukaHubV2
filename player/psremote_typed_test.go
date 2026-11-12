package main

// PSR-14: a typed address is a row while it pairs, so the panel shows what it
// is doing instead of only a PIN toast, and a failure stays listed.

import (
	"strings"
	"testing"
)

func TestChiakiRowsShowsTypedHostWhilePairing(t *testing.T) {
	psPanelMu.Lock()
	chiakiPanel = chiakiPanelState{
		open:      true,
		page:      psPageConsoles,
		typedHost: "192.168.1.51",
		busy:      "Pairing 192.168.1.51…",
	}
	psPanelMu.Unlock()
	defer func() {
		psPanelMu.Lock()
		chiakiPanel = chiakiPanelState{}
		psPanelMu.Unlock()
	}()

	rows := chiakiRows()
	row, ok := chiakiRowByHost(rows, "192.168.1.51")
	if !ok {
		t.Fatalf("typed host has no row: %+v", rows)
	}
	if row.label != "192.168.1.51" || row.status != "…" || row.action != psActionNone {
		t.Fatalf("pairing row = %+v, want the address with a busy status", row)
	}
	if !strings.Contains(row.detail, "Pairing") {
		t.Fatalf("pairing row detail = %q, want the busy line", row.detail)
	}

	psPanelMu.Lock()
	chiakiPanel.busy = ""
	psPanelMu.Unlock()
	row, ok = chiakiRowByHost(chiakiRows(), "192.168.1.51")
	if !ok {
		t.Fatal("typed host disappeared after the pairing stopped")
	}
	if row.status != "Pair" || row.action != psActionPair {
		t.Fatalf("unpaired row = %+v, want Pair with the pair action", row)
	}
}

func chiakiRowByHost(rows []chiakiRow, host string) (chiakiRow, bool) {
	for _, row := range rows {
		if row.host == host {
			return row, true
		}
	}
	return chiakiRow{}, false
}
