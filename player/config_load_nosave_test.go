package main

import "testing"

// Loading a config never schedules a jukauser.json write. Every load used to:
// at boot that write carried jukaconfig.json's values (with the Quick Settings
// levels zeroed) over the user's settings, so a crash or a power cut inside
// the debounce reset them.
func TestConfigLoadSchedulesNoUserSave(t *testing.T) {
	prevWrites := userConfigWritesEnabled
	userConfigWritesEnabled = true
	cancelPendingUserConfigSave(t)
	t.Cleanup(func() {
		cancelPendingUserConfigSave(t)
		userConfigWritesEnabled = prevWrites
	})

	if _, err := loadConfig("jukaconfig.json"); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	userConfigSaveMutex.Lock()
	pending := pendingUserConfig
	userConfigSaveMutex.Unlock()
	if pending != nil {
		t.Fatal("loading the config scheduled a jukauser.json save")
	}
}
