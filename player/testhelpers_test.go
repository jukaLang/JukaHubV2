package main

// testhelpers_test.go — helpers shared by the kept tests whose original files
// were removed when the suite was cut down to its fast, high-value core.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func init() {
	// Saves performed by the rest of the test suite must not create a
	// history folder in the checkout (powertools_backend.go).
	powerConfigHistoryEnabled = false
}

func resetA11yForTest() {
	a11y = a11ySettings{fontScale: 1.0, captionSize: 1, captionBackground: true,
		longPressMs: 450, repeatDelayMs: 320, repeatRateMs: 70, toastPercent: 100,
		magnifierZoom: 2, switchMode: a11ySwitchOff, switchIntervalMs: a11ySwitchDefaultIntervalMS}
	a11yChordReset()
	a11yApplyVision()
	magnifierReset()
	switchScanReset()
	legibleFontReset()
}

func chordEvent(kind uint32, button uint8) *sdl.ControllerButtonEvent {
	return &sdl.ControllerButtonEvent{Type: kind, Button: button}
}

func setCurationTestDirs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevDir := curationDirOverride
	prevProfiles, prevSaves := saveManagerDirOverride, saveManagerSDRootOverride
	curationDirOverride = dir
	saveManagerDirOverride = filepath.Join(dir, "save-manager")
	saveManagerSDRootOverride = filepath.Join(dir, "sdcard")
	// No collector or feed may leak in from the developer's environment.
	t.Setenv("JUKAHUB_CURATION_VOTES_URL", "")
	t.Setenv("JUKAHUB_CURATION_FEED", "")
	if err := ensureCurationLayout(); err != nil {
		t.Fatalf("ensureCurationLayout: %v", err)
	}
	if err := ensureSaveManagerLayout(); err != nil {
		t.Fatalf("ensureSaveManagerLayout: %v", err)
	}
	t.Cleanup(func() {
		curationDirOverride = prevDir
		saveManagerDirOverride = prevProfiles
		saveManagerSDRootOverride = prevSaves
	})
	return dir
}

func withTempPatchState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevFn, prevDir := patchStateDirFn, patchStateDir
	patchStateDirFn = func() (string, error) { return dir, nil }
	patchStateDir = dir
	t.Cleanup(func() {
		patchStateDirFn = prevFn
		patchStateDir = prevDir
	})
	return dir
}

func noLookPath(string) (string, error) { return "", os.ErrNotExist }

func withPartyFriendsTempFile(t *testing.T) string {
	t.Helper()
	prevPath := partyFriendsPath
	prevLoaded := partyFriendsStore.loaded
	prevItems := partyFriendsStore.items

	path := filepath.Join(t.TempDir(), partyFriendsFileName)
	partyFriendsPath = func() string { return path }
	partyFriendsStore.mu.Lock()
	partyFriendsStore.loaded = false
	partyFriendsStore.items = nil
	partyFriendsStore.mu.Unlock()

	t.Cleanup(func() {
		partyFriendsPath = prevPath
		partyFriendsStore.mu.Lock()
		partyFriendsStore.loaded = prevLoaded
		partyFriendsStore.items = prevItems
		partyFriendsStore.mu.Unlock()
	})
	return path
}

func withPartyHistoryTempFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), partyHistoryFileName)

	prevPath := partyHistoryPath
	partyHistoryStore.mu.Lock()
	prevLoaded := partyHistoryStore.loaded
	prevData := partyHistoryStore.data
	prevDirty := partyHistoryStore.dirty
	prevSave := partyHistoryStore.lastSave

	partyHistoryPath = func() string { return path }
	partyHistoryStore.loaded = false
	partyHistoryStore.data = newPartyHistoryData()
	partyHistoryStore.dirty = false
	partyHistoryStore.lastSave = time.Time{}
	partyHistoryStore.mu.Unlock()

	t.Cleanup(func() {
		partyHistoryPath = prevPath
		partyHistoryStore.mu.Lock()
		partyHistoryStore.loaded = prevLoaded
		partyHistoryStore.data = prevData
		partyHistoryStore.dirty = prevDirty
		partyHistoryStore.lastSave = prevSave
		partyHistoryStore.mu.Unlock()
	})
	return path
}

func partyHistoryTestSnapshot(code, room, addr, host, title string, scores ...partyLeaderRow) partySnapshot {
	hostID := "host-" + code
	return partySnapshot{
		Code:     code,
		Name:     room,
		HostID:   hostID,
		HostAddr: addr,
		Members: []partyMember{
			{ID: hostID, Name: host, Host: true},
			{ID: "friend-" + code, Name: "Guest", Host: false},
		},
		Playback:    partyPlayback{URL: "https://example.com/party.mp4", Title: title, Playing: true},
		Leaderboard: scores,
	}
}

// plugxTestWrite writes files (slash names, folders made) under dir.
func plugxTestWrite(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// clearToastAfterTest drops any toast a test raised. A toast lives in a package
// global for a few seconds, so without this it would leak into a later test.
func clearToastAfterTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		currentToastMu.Lock()
		currentToast = ToastState{}
		currentToastMu.Unlock()
	})
}

// subtitlesIsolateForTests keeps the whole test binary (TestMain) away from the
// network, the helpers and the real data folder: CC outlives a stopped video,
// so a test that played something with CC on would otherwise fetch for real
// and write the real subtitle cache. It returns the cleanup.
func subtitlesIsolateForTests() func() {
	dir, err := os.MkdirTemp("", "jukahub-subtitles")
	if err == nil {
		subtitleCacheDirOverride = dir
	}
	ytNativeCaptionsFor = func(context.Context, string, string) ([]subtitleCue, error) {
		return nil, errors.New("no network in tests")
	}
	subtitleYtdlpRunner = func(context.Context, subtitleRequest) ([]subtitleCue, string) {
		return nil, "no helper in tests"
	}
	videoSubs.mu.Lock()
	videoSubs.enabled = false
	videoSubs.mu.Unlock()
	return func() {
		if err == nil {
			os.RemoveAll(dir)
		}
	}
}
