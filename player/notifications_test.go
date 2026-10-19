package main

import "testing"

// resetNotifCenter clears the global bell state between cases.
func resetNotifCenter(t *testing.T) {
	t.Helper()
	nc = notifCenter{}
}

func TestShowToastRecordsHistory(t *testing.T) {
	resetNotifCenter(t)

	ShowToast("Download finished", ToastKindSuccess)
	ShowToast("Low battery", ToastKindWarning)
	ShowToast("Update failed", ToastKindError)

	if len(nc.history) != 3 {
		t.Fatalf("history has %d entries, want 3", len(nc.history))
	}
	// Newest first.
	if nc.history[0].Message != "Update failed" || nc.history[0].Priority != notifError {
		t.Fatalf("newest entry = %q/%v, want error", nc.history[0].Message, nc.history[0].Priority)
	}
	if nc.history[2].Message != "Download finished" || nc.history[2].Priority != notifSuccess {
		t.Fatalf("oldest entry = %q/%v, want download/success", nc.history[2].Message, nc.history[2].Priority)
	}
	if got := notifUnreadCount(); got != 3 {
		t.Fatalf("unread = %d, want 3", got)
	}
}

// Scene-switch labels are navigation feedback, not events; logging them would
// bury downloads and errors under every L2/R2 press.
func TestShowToastSkipsSceneSwitch(t *testing.T) {
	resetNotifCenter(t)

	ShowToast("Hardware", ToastKindSwitch)
	if len(nc.history) != 0 {
		t.Fatalf("scene-switch toast was recorded: %+v", nc.history)
	}
}

func TestShowToastIgnoresEmptyMessage(t *testing.T) {
	resetNotifCenter(t)

	ShowToast("   ", ToastKindInfo)
	if len(nc.history) != 0 {
		t.Fatalf("whitespace toast was recorded: %+v", nc.history)
	}
}

// A producer stuck in a loop must not flush the 50-entry history: the newest
// unread row absorbs the repeat.
func TestShowToastFoldsRepeats(t *testing.T) {
	resetNotifCenter(t)

	for i := 0; i < 5; i++ {
		ShowToast("Network unreachable", ToastKindError)
	}
	if len(nc.history) != 1 {
		t.Fatalf("history has %d entries, want 1 folded row", len(nc.history))
	}
	if nc.history[0].Repeat != 5 {
		t.Fatalf("repeat = %d, want 5", nc.history[0].Repeat)
	}
}

// Once the user has opened the bell, the row is read; a later identical event
// is new information and gets its own unread row.
func TestShowToastAfterReadStartsNewRow(t *testing.T) {
	resetNotifCenter(t)

	notifPush("Update available", notifInfo)
	notifToggle() // opens the panel and marks everything read
	if !nc.open {
		t.Fatal("bell did not open")
	}
	nc.open = false

	ShowToast("Update available", ToastKindInfo)
	if len(nc.history) != 2 {
		t.Fatalf("history has %d entries, want 2", len(nc.history))
	}
	if nc.history[0].Read {
		t.Fatal("new event should be unread")
	}
	if nc.history[0].Repeat != 1 || nc.history[1].Repeat != 1 {
		t.Fatalf("repeats = %d/%d, want 1/1", nc.history[0].Repeat, nc.history[1].Repeat)
	}
	if got := notifUnreadCount(); got != 1 {
		t.Fatalf("unread = %d, want 1", got)
	}
}

// notifPush shows its toast and records exactly one row, not two.
func TestNotifPushRecordsOnce(t *testing.T) {
	resetNotifCenter(t)

	notifPush("JukaHub v2.4 is available", notifSuccess)
	if len(nc.history) != 1 {
		t.Fatalf("history has %d entries, want 1", len(nc.history))
	}
	if nc.history[0].Priority != notifSuccess {
		t.Fatalf("priority = %v, want success", nc.history[0].Priority)
	}
	if currentToast.Message != "JukaHub v2.4 is available" {
		t.Fatalf("toast = %q, want the pushed message", currentToast.Message)
	}
}

func TestNotifHistoryIsCapped(t *testing.T) {
	resetNotifCenter(t)

	for i := 0; i < 60; i++ {
		notifRecord("event", notifInfo)
		// Read the row so the next identical event is not folded into it.
		if len(nc.history) > 0 {
			nc.history[0].Read = true
		}
	}
	if len(nc.history) != 50 {
		t.Fatalf("history has %d entries, want the 50 cap", len(nc.history))
	}
}

func TestToastKindPriorityRoundTrip(t *testing.T) {
	cases := []ToastKind{ToastKindInfo, ToastKindSuccess, ToastKindWarning, ToastKindError}
	for _, k := range cases {
		if got := priorityToastKind(toastKindPriority(k)); got != k {
			t.Fatalf("round trip of %v = %v", k, got)
		}
	}
}
