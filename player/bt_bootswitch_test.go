package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A stored Off must survive an incidental bring-up and a wake: the watchdog
// re-reads the file each time instead of trusting a state captured at boot.
func TestBTSwitchStillOffUsesStoredChoice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bluetooth-switch.json")
	oldPath := btSwitchPath
	btSwitchPath = func() string { return path }
	defer func() { btSwitchPath = oldPath }()

	st := &btSwitchState
	oldKnown, oldOn, oldLatch := st.known, st.on, st.onThisSession
	defer func() { st.known, st.on, st.onThisSession = oldKnown, oldOn, oldLatch }()
	st.known, st.on, st.onThisSession = false, false, false

	write := func(on bool) {
		data, err := json.Marshal(btSwitchRecord{On: on})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(false)
	if !btSwitchStillOff() {
		t.Fatal("a stored Off was not enforced")
	}
	// An incidental bring-up in this session cancels the watchdog even if
	// the file still says Off — a press that switched it on wins.
	st.onThisSession = true
	if btSwitchStillOff() {
		t.Fatal("a radio switched on this session was switched off again")
	}
	st.onThisSession = false

	write(true)
	if btSwitchStillOff() {
		t.Fatal("a stored On was treated as Off")
	}

	// Corrupt or missing file must not turn a radio off behind the user.
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if btSwitchStillOff() {
		t.Fatal("an unreadable switch record was treated as Off")
	}
}

// An explicit Off must cancel any "on this session" latch, or the wake watcher
// would skip its checks for the rest of the run.
func TestBTSwitchOffClearsSessionLatch(t *testing.T) {
	st := &btSwitchState
	oldKnown, oldOn, oldLatch := st.known, st.on, st.onThisSession
	defer func() { st.known, st.on, st.onThisSession = oldKnown, oldOn, oldLatch }()
	st.known, st.on, st.onThisSession = true, true, true

	if btSwitchNote(true) {
		t.Fatal("a repeated On was reported as a change")
	}
	if !st.onThisSession {
		t.Fatal("an On did not latch the session")
	}
	if !btSwitchNote(false) {
		t.Fatal("the change from On to Off was not reported")
	}
	if st.onThisSession {
		t.Fatal("Off left the session latch set")
	}
	if !st.known || st.on {
		t.Fatal("the in-memory switch was not left Off")
	}
}
