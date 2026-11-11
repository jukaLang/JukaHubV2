package main

import (
	"path/filepath"
	"strings"
	"testing"
)

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

// Every PS5, and every PS4 from firmware 7.0 on, pairs with the PSN
// Account-ID; only a PS4 on older firmware is asked for the PIN alone. A typed
// address (target 0) is taken for current hardware.
func TestPSTargetNeedsAccount(t *testing.T) {
	for _, tc := range []struct {
		ps5    bool
		target int
		want   bool
	}{
		{true, psTargetPS5_1, true},
		{false, psTargetPS5, true},
		{false, psTargetPS4FW10, true},
		{false, psTargetPS4FW9, true},
		{false, 0, true},
		{false, psTargetPS4FW8, false},
	} {
		if got := psTargetNeedsAccount(tc.ps5, tc.target); got != tc.want {
			t.Errorf("psTargetNeedsAccount(%v, %d) = %v, want %v", tc.ps5, tc.target, got, tc.want)
		}
	}
}

// The PIN prompt names the console's own menu. "Link Device" never shows the
// Account-ID, so nothing here may send anyone there to look for it.
func TestPSPanelLinkDeviceHint(t *testing.T) {
	if got := psPanelLinkDeviceHint(5); !strings.Contains(got, "Settings → System → Remote Play → Link Device") {
		t.Errorf("PS5 hint = %q", got)
	}
	if got := psPanelLinkDeviceHint(4); !strings.Contains(got, "Remote Play Connection Settings → Add Device") {
		t.Errorf("PS4 hint = %q", got)
	}
	unknown := psPanelLinkDeviceHint(0)
	for _, want := range []string{"Link Device", "Add Device", "8 numbers"} {
		if !strings.Contains(unknown, want) {
			t.Errorf("hint for an unknown console %q is missing %q", unknown, want)
		}
	}
	if psConsoleKind(psConsole{PS5: true}) != 5 || psConsoleKind(psConsole{Target: psTargetPS4FW9}) != 4 || psConsoleKind(psConsole{}) != 0 {
		t.Error("psConsoleKind misreads a console")
	}
	for _, text := range []string{psAccountRowDetail(""), psAccountHelpText, psAccountSubtitle("")} {
		if strings.Contains(text, "from Link Device") {
			t.Errorf("%q sends people to Link Device for the Account-ID", text)
		}
	}
}

// psUseTempHosts gives a test its own psremote.json and a closed panel.
func psUseTempHosts(t *testing.T) {
	t.Helper()
	clearToastAfterTest(t)
	prevPath, prevPanel := psHostsPathOverride, chiakiPanel
	psHostsPathOverride = filepath.Join(t.TempDir(), "psremote.json")
	chiakiPanel = chiakiPanelState{}
	t.Cleanup(func() { psHostsPathOverride, chiakiPanel = prevPath, prevPanel })
}

// An Account-ID that arrives from the phone while the panel is closed is kept
// for the next pairing; anything that is not an Account-ID is dropped.
func TestPSPanelAccountArrivedSavesWhileClosed(t *testing.T) {
	psUseTempHosts(t)
	psPanelAccountArrived("not an id")
	if got := loadPSHosts().Settings.Account; got != "" {
		t.Fatalf("a non-ID was saved as %q", got)
	}
	psPanelAccountArrived(" FYHpffQQIhE= ")
	if got := loadPSHosts().Settings.Account; got != "FYHpffQQIhE=" {
		t.Fatalf("saved Account-ID = %q, want FYHpffQQIhE=", got)
	}
}

// An empty Account-ID field clears the saved Account-ID only when the field
// showed it. The browser sign-in opens the field empty for the paste, and
// Enter there before pasting must not wipe the saved one.
func TestPSPanelEmptyAccountFieldKeepsSavedID(t *testing.T) {
	psUseTempHosts(t)
	prevSeed, prevPair := psPanelEditSeed, psPanelPairAfterAccount
	t.Cleanup(func() {
		psPanelEditSeed, psPanelPairAfterAccount = prevSeed, prevPair
		psPanelEditField = psEditNone
	})
	psPanelPairAfterAccount = false
	chiakiPanel = chiakiPanelState{open: true, page: psPageAccount, account: "FYHpffQQIhE="}
	psPanelEditField, psPanelEditSeed = psEditAccount, ""
	psPanelApplyEdit("")
	if chiakiPanel.account != "FYHpffQQIhE=" || chiakiPanel.page != psPageAccount {
		t.Fatalf("an empty paste field changed the card: account %q, page %d", chiakiPanel.account, chiakiPanel.page)
	}
	psPanelEditField, psPanelEditSeed = psEditAccount, "FYHpffQQIhE="
	psPanelApplyEdit("")
	if chiakiPanel.account != "" || loadPSHosts().Settings.Account != "" {
		t.Fatalf("erasing the Account-ID the field showed did not clear it: %q", chiakiPanel.account)
	}
}

// The phone's sign-in reaches the panel through the Phone Companion queue.
func TestCompanionQueueDeliversPSNAccount(t *testing.T) {
	psUseTempHosts(t)
	resetCompanionQueue()
	t.Cleanup(resetCompanionQueue)
	enqueueCompanion(companionEvent{Kind: companionEventPSN, Text: "AQAAAAAAAAA="})
	drainCompanionQueue(&Config{})
	if got := loadPSHosts().Settings.Account; got != "AQAAAAAAAAA=" {
		t.Fatalf("Account-ID after the queue = %q, want AQAAAAAAAAA=", got)
	}
}

// A refused Account-ID is named as such: it used to read as "get a fresh PIN",
// which no new PIN could fix.
func TestPS4RefusalDetailExplainsReasons(t *testing.T) {
	got := ps4RefusalDetail("the PS4 refused pairing", 403, "80108b02")
	if !strings.Contains(got, "Account-ID") || strings.Contains(got, "fresh PIN") {
		t.Errorf("Account-ID refusal = %q", got)
	}
	if got := ps4RefusalDetail("x", 403, "0x80108B09"); !strings.Contains(got, "PIN was wrong") {
		t.Errorf("PIN refusal = %q", got)
	}
	if got := ps4RefusalDetail("x", 403, ""); !strings.Contains(got, "fresh PIN") {
		t.Errorf("plain 403 = %q", got)
	}
	if got := ps4RefusalDetail("x", 500, ""); got != "x (HTTP 500)" {
		t.Errorf("plain 500 = %q", got)
	}
}
