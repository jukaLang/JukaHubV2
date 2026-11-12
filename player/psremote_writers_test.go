package main

import (
	"sync"
	"testing"
	"time"
)

// TestPSRemoteWritersHoldTheHostsLock covers PSR-12: both writers load and save
// psremote.json under one lock, so neither can finish while another writer holds
// it, and a pairing saved in between cannot be written over.
func TestPSRemoteWritersHoldTheHostsLock(t *testing.T) {
	psUseTempHosts(t)
	savePSHostsForTest(t)

	cases := []struct {
		name string
		run  func() error
	}{
		{"psUpdateConsole", func() error {
			return psUpdateConsole("192.168.0.10", func(c *psConsole) { c.Name = "renamed" })
		}},
		{"psSaveStreamSettings", func() error {
			_, err := psSaveStreamSettings(func(s *psRemoteSettings) { s.Volume = 3 })
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			psHostsMu.Lock()
			done := make(chan error, 1)
			go func() { done <- tc.run() }()
			select {
			case err := <-done:
				psHostsMu.Unlock()
				t.Fatalf("%s finished while psHostsMu was held (err=%v)", tc.name, err)
			case <-time.After(100 * time.Millisecond):
			}
			psHostsMu.Unlock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not finish after the lock was released", tc.name)
			}
		})
	}
}

// TestPSRemoteConcurrentWritersKeepBothChanges is the two-goroutine case from
// the task: a console rename and a stream setting saved together both survive.
func TestPSRemoteConcurrentWritersKeepBothChanges(t *testing.T) {
	psUseTempHosts(t)
	savePSHostsForTest(t)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = psUpdateConsole("192.168.0.10", func(c *psConsole) { c.Name = "renamed" })
	}()
	go func() {
		defer wg.Done()
		_, _ = psSaveStreamSettings(func(s *psRemoteSettings) { s.Volume = 7 })
	}()
	wg.Wait()

	state := loadPSHosts()
	if len(state.Hosts) != 1 || state.Hosts[0].Name != "renamed" {
		t.Fatalf("console name = %+v, want renamed", state.Hosts)
	}
	if state.Settings.Volume != 7 {
		t.Fatalf("volume = %d, want 7", state.Settings.Volume)
	}
}

// savePSHostsForTest seeds one paired console in the test's own file.
func savePSHostsForTest(t *testing.T) {
	t.Helper()
	psHostsMu.Lock()
	err := savePSHostsLocked(psHostsFile{Hosts: []psConsole{{Host: "192.168.0.10", Name: "PS5"}}})
	psHostsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}
