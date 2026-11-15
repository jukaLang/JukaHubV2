package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The startup lock stores a salted slow hash, never the code; the right code
// verifies, a wrong one, the other kind's or a tampered value does not; and
// the wrong-attempt count survives a save and reload (a restart must not
// reset the wait).
func TestBootLockHashAndBackoffPersistence(t *testing.T) {
	stored, err := bootLockHash(bootLockKindCode, "UDLRAB")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "UDLRAB") || !strings.HasPrefix(stored, bootLockHashTag+"$code$") {
		t.Fatalf("stored form leaks or is mis-tagged: %q", stored)
	}
	if again, _ := bootLockHash(bootLockKindCode, "UDLRAB"); again == stored {
		t.Fatal("two hashes of one code share a salt")
	}
	if st, ok := bootLockParse(stored); !ok || st.iter < 100000 || len(st.salt) != bootLockSaltLen {
		t.Fatalf("parse: %+v %v", st, ok)
	}
	if !bootLockVerify(stored, "UDLRAB") {
		t.Fatal("the right code was refused")
	}
	for _, wrong := range []string{"UDLRBA", "UDLRA", "", "udlrab"} {
		if bootLockVerify(stored, wrong) {
			t.Fatalf("wrong code %q accepted", wrong)
		}
	}
	pw, _ := bootLockHash(bootLockKindPassword, "UDLRAB")
	if pw == stored || !bootLockVerify(pw, "UDLRAB") {
		t.Fatal("password hash must differ from the code's and still verify")
	}
	parts := strings.Split(stored, "$")
	parts[1] = bootLockKindPassword // a kind swapped by hand changes the hash input
	if bootLockVerify(strings.Join(parts, "$"), "UDLRAB") {
		t.Fatal("a swapped kind verified")
	}
	parts = strings.Split(stored, "$")
	parts[2] = "999999999" // an absurd iteration count is no lock, not a freeze
	for _, bad := range []string{"", "plain", strings.Join(parts, "$"), stored + "$x"} {
		if _, ok := bootLockParse(bad); ok {
			t.Fatalf("parsed %q", bad)
		}
	}

	m := map[string]interface{}{}
	if bootLockFailsFrom(m) != 0 || bootLockWaitFor(0) != 0 || bootLockWaitFor(bootLockFreeTries-1) != 0 {
		t.Fatal("no wait before the free tries are used")
	}
	bootLockStoreFails(m, 7)
	m[bootLockSecretKey] = stored
	m[bootLockPendingKey] = true // a start that never got past the lock
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]interface{}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if got := bootLockFailsFrom(back); got != 7 {
		t.Fatalf("fails after reload = %d, want 7", got)
	}
	if !bootLockPendingFrom(back) {
		t.Fatal("the never-unlocked mark did not survive the reload")
	}
	if bootLockSecretFrom(back) != stored || !bootLockVerify(bootLockSecretFrom(back), "UDLRAB") {
		t.Fatal("stored code did not survive the reload")
	}
	want := map[int]time.Duration{5: 30 * time.Second, 6: time.Minute, 7: 2 * time.Minute, 8: 4 * time.Minute, 9: 5 * time.Minute, 40: 5 * time.Minute}
	for fails, d := range want {
		if got := bootLockWaitFor(fails); got != d {
			t.Fatalf("wait after %d = %v, want %v", fails, got, d)
		}
	}
	back[bootLockFailsKey] = "-3"
	if bootLockFailsFrom(back) != 0 {
		t.Fatal("a negative hand-edited count must read as 0")
	}
}
