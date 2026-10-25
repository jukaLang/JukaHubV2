package main

// whileaway_test.go — the ledger and its wording are plain functions of plain
// values, in the style of downloads_test.go. The gate is exercised the same
// way gamesuspend_test.go exercises its counter: noteGameChildStarted(nil)
// and noteGameChildExited(nil) turn the "a child owns the screen" state on
// and off without any real process.

import (
	"strings"
	"testing"
)

func withScreenChild(t *testing.T, alive bool) {
	t.Helper()
	if alive {
		noteGameChildStarted(nil)
	} else {
		// Guarantee the ledger sees no child: the counter clamps at zero, and
		// gamesuspend_test.go's t.Cleanup fixtures may have left state behind.
		noteGameChildExited(nil)
	}
	t.Cleanup(func() { noteGameChildExited(nil) })
}

func resetAwayLedger(t *testing.T) {
	t.Helper()
	whileAway.mu.Lock()
	whileAway.events = nil
	whileAway.mu.Unlock()
	t.Cleanup(func() {
		whileAway.mu.Lock()
		whileAway.events = nil
		whileAway.mu.Unlock()
	})
}

func TestWhileAwayIgnoresEventsWithoutAChild(t *testing.T) {
	resetAwayLedger(t)
	withScreenChild(t, false)

	noteAwayDownloadSaved("Neon Nights")
	noteAwayToolInstalled("yt-dlp")
	noteAwayAchievement("Gamer")
	if events := whileAwayDrain(); len(events) != 0 {
		t.Fatalf("drained %d events with no child alive, want 0: %+v", len(events), events)
	}
	if got := awaySummaryText(nil); got != "" {
		t.Fatalf("empty ledger text = %q, want \"\"", got)
	}
}

func TestWhileAwayRecordsOnlyWithAChildAlive(t *testing.T) {
	resetAwayLedger(t)
	withScreenChild(t, true)

	noteAwayDownloadSaved("Neon Nights")
	noteAwayDownloadSaved("Two On A Bridge")
	noteAwayToolInstalled("PortMaster")

	events := whileAwayDrain()
	if len(events) != 3 {
		t.Fatalf("drained %d events, want 3: %+v", len(events), events)
	}
	if events[0].kind != awayKindDownload || events[0].title != "Neon Nights" {
		t.Fatalf("first event = %+v, want a download named Neon Nights", events[0])
	}
	if events[2].kind != awayKindTool {
		t.Fatalf("third event kind = %q, want tool", events[2].kind)
	}
	// Draining empties the ledger: a second drain is a no-op.
	if again := whileAwayDrain(); len(again) != 0 {
		t.Fatalf("second drain returned %d events, want 0", len(again))
	}
}

func TestWhileAwayStopsCountingWhenTheChildExits(t *testing.T) {
	resetAwayLedger(t)
	withScreenChild(t, true)

	noteAwayDownloadSaved("Neon Nights")
	noteGameChildExited(nil)
	noteAwayDownloadSaved("Two On A Bridge")

	events := whileAwayDrain()
	if len(events) != 1 {
		t.Fatalf("drained %d events, want 1 (the one under the live child): %+v", len(events), events)
	}
}

func TestAwaySummaryTextWording(t *testing.T) {
	cases := []struct {
		name   string
		events []awayEvent
		want   string
	}{
		{name: "empty", events: nil, want: ""},
		{name: "one download", events: []awayEvent{{kind: awayKindDownload}}, want: "While you were playing: 1 track saved"},
		{name: "three downloads", events: []awayEvent{
			{kind: awayKindDownload}, {kind: awayKindDownload}, {kind: awayKindDownload},
		}, want: "While you were playing: 3 tracks saved"},
		{name: "mixed like the pitch", events: []awayEvent{
			{kind: awayKindDownload}, {kind: awayKindDownload}, {kind: awayKindDownload},
			{kind: awayKindTool},
		}, want: "While you were playing: 3 tracks saved · 1 tool installed"},
		{name: "achievement singular", events: []awayEvent{{kind: awayKindAchievement}}, want: "While you were playing: 1 achievement unlocked"},
		{name: "achievements plural", events: []awayEvent{
			{kind: awayKindAchievement}, {kind: awayKindAchievement},
		}, want: "While you were playing: 2 achievements unlocked"},
		{name: "clauses in a fixed order", events: []awayEvent{
			{kind: awayKindAchievement}, {kind: awayKindTool}, {kind: awayKindDownload},
		}, want: "While you were playing: 1 track saved · 1 tool installed · 1 achievement unlocked"},
	}
	for _, tc := range cases {
		if got := awaySummaryText(tc.events); got != tc.want {
			t.Errorf("%s: awaySummaryText = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWhileAwayNotifyRaisesOneLongToast(t *testing.T) {
	clearToastAfterTest(t)
	resetAwayLedger(t)
	withScreenChild(t, true)
	noteAwayDownloadSaved("Neon Nights")
	noteAwayToolInstalled("PortMaster")

	whileAwayNotify()

	if !strings.HasPrefix(currentToast.Message, "While you were playing: ") {
		t.Fatalf("toast = %q, want the assigned prefix", currentToast.Message)
	}
	if !strings.Contains(currentToast.Message, "1 track saved") || !strings.Contains(currentToast.Message, "1 tool installed") {
		t.Fatalf("toast = %q, want both clauses", currentToast.Message)
	}
	if currentToast.Duration != whileAwayToastDurationMS {
		t.Fatalf("toast duration = %d, want the longer return slot (%d)", currentToast.Duration, whileAwayToastDurationMS)
	}
	// The ledger is spent: a second notify has nothing to say and leaves the
	// first toast to fade on its own timer.
	if again := whileAwayDrain(); len(again) != 0 {
		t.Fatalf("ledger still held %d events after notify, want it spent", len(again))
	}
}

func TestWhileAwayLedgerIsBounded(t *testing.T) {
	resetAwayLedger(t)
	withScreenChild(t, true)
	for i := 0; i < awayEventCap*3; i++ {
		noteAwayEvent(awayKindDownload, "x")
	}
	if got := len(whileAwayDrain()); got != awayEventCap {
		t.Fatalf("ledger held %d events, want the %d cap", got, awayEventCap)
	}
}
