package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeAmixer answers the commands trimuiSetVolume and trimuiVolume run, keeps
// the two controls' raw values, and records every call. scontrols overrides
// the control list; failGet makes every `amixer get` fail.
type fakeAmixer struct {
	dac       int
	digital   int
	scontrols string
	failGet   bool
	calls     []string
}

func (f *fakeAmixer) run(name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.failGet && strings.HasPrefix(call, "amixer get ") {
		return "", errors.New("amixer: not answering")
	}
	switch {
	case call == "amixer scontrols":
		if f.scontrols != "" {
			return f.scontrols, nil
		}
		return "Simple mixer control 'digital volume',0\nSimple mixer control 'DAC Volume',0\n", nil
	case call == "amixer get digital volume":
		return "  Limits: Playback 0 - 63\n  Mono: Playback " + itoa(f.digital) + " [100%]\n", nil
	case call == "amixer get DAC Volume":
		return "  Limits: Playback 0 - 255\n  Front Left: Playback " + itoa(f.dac) + " [63%]\n", nil
	case strings.HasPrefix(call, "amixer -q sset DAC Volume "):
		f.dac = atoiOr(strings.TrimPrefix(call, "amixer -q sset DAC Volume "), f.dac)
	case strings.HasPrefix(call, "amixer -q sset digital volume "):
		f.digital = atoiOr(strings.TrimPrefix(call, "amixer -q sset digital volume "), f.digital)
	}
	return "", nil
}

func atoiOr(s string, fallback int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return fallback
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// withFakeTrimuiMixer swaps the command runner and the firmware file for the
// test and puts the mixer state back afterwards.
func withFakeTrimuiMixer(t *testing.T, dac int) *fakeAmixer {
	t.Helper()
	fake := &fakeAmixer{dac: dac}
	prevRunner, prevPath, prevMarker := systemCommandRunner, trimuiSystemJSONPath, trimuiMuteMarkerPath
	systemCommandRunner = fake.run
	trimuiSystemJSONPath = filepath.Join(t.TempDir(), "missing-system.json")
	marker := filepath.Join(t.TempDir(), "volume-mute")
	trimuiMuteMarkerPath = func() string { return marker }
	resetTrimuiMixerForTest()
	t.Cleanup(func() {
		resetTrimuiMixerForTest()
		systemCommandRunner, trimuiSystemJSONPath, trimuiMuteMarkerPath = prevRunner, prevPath, prevMarker
	})
	return fake
}

// resetTrimuiMixerForTest forgets everything the mixer and the system.json
// sync learned, and stops a pending write.
func resetTrimuiMixerForTest() {
	trimuiSystemSync.mu.Lock()
	if trimuiSystemSync.timer != nil {
		trimuiSystemSync.timer.Stop()
		trimuiSystemSync.timer = nil
	}
	trimuiSystemSync.pending, trimuiSystemSync.failed = false, false
	trimuiSystemSync.seq, trimuiSystemSync.retries, trimuiSystemSync.vol = 0, 0, 0
	trimuiSystemSync.mu.Unlock()
	trimuiMixer.mu.Lock()
	trimuiMixer.probed, trimuiMixer.probedAt, trimuiMixer.attempts = false, time.Time{}, 0
	trimuiMixer.info, trimuiMixer.savedDAC, trimuiMixer.dacMuted = trimuiMixerInfo{}, 0, false
	trimuiMixer.mutedHere = false
	trimuiMixer.mu.Unlock()
}

// A JukaHub mute survives a restart as JukaHub's: the next run's hardware keys
// still lift it, back to the DAC level the mute replaced, and the record goes.
func TestTrimuiMuteSurvivesARestart(t *testing.T) {
	fake := withFakeTrimuiMixer(t, 170)
	if err := trimuiSetVolume(0); err != nil {
		t.Fatal(err)
	}
	if fake.dac != 0 {
		t.Fatalf("the mute left the DAC at %d", fake.dac)
	}
	if saved, ok := trimuiReadMuteMarker(); !ok || saved != 170 {
		t.Fatalf("mute record = %d, %v; want 170, true", saved, ok)
	}
	resetTrimuiMixerForTest() // a new run: only the codec and the record are left
	fake.digital = 56         // a hardware Vol+ press: the firmware wrote 5%
	if pct, ok := trimuiVolume(); !ok || pct != 5 {
		t.Fatalf("after the key the level reads %d, %v; want 5", pct, ok)
	}
	if fake.dac != 170 {
		t.Fatalf("the restarted run left its own mute silent: DAC = %d", fake.dac)
	}
	if _, ok := trimuiReadMuteMarker(); ok {
		t.Fatal("the mute record must go once the DAC is back")
	}
}

// A borrowed level (the alarm's ring) never reaches the firmware's file; a
// level the user sets does, in twentieths that keep an audible level above 0,
// and a pending write is flushed on demand (the exit path).
func TestTrimuiSystemSyncPersistsOnlyUserLevels(t *testing.T) {
	withFakeTrimuiMixer(t, 170)
	path := filepath.Join(t.TempDir(), "system.json")
	if err := os.WriteFile(path, []byte("{\"vol\": 7, \"brightness\": 6}"), 0o644); err != nil {
		t.Fatal(err)
	}
	trimuiSystemJSONPath = path
	if err := trimuiSetVolumeTransient(100); err != nil {
		t.Fatal(err)
	}
	if pending, _ := trimuiSystemSyncState(); pending {
		t.Fatal("the alarm's 100% must not be queued for system.json")
	}
	if err := trimuiSetVolume(2); err != nil {
		t.Fatal(err)
	}
	trimuiSystemVolumeFlush()
	if v, ok := trimuiSystemVolumeStep(); !ok || v != 1 {
		t.Fatalf("2%% was stored as vol %d, %v; want 1 (0 is the firmware's mute)", v, ok)
	}
	if pending, failed := trimuiSystemSyncState(); pending || failed {
		t.Fatalf("after the flush pending=%v failed=%v", pending, failed)
	}
	for _, c := range []struct{ pct, vol int }{{0, 0}, {1, 1}, {7, 1}, {8, 2}, {50, 10}, {100, 20}, {150, 20}} {
		if got := trimuiSystemStepFor(c.pct); got != c.vol {
			t.Errorf("trimuiSystemStepFor(%d) = %d, want %d", c.pct, got, c.vol)
		}
	}
}

// A symlinked settings file is updated where it lives, and stays a link.
func TestTrimuiSystemVolumeWriteThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target-system.json")
	if err := os.WriteFile(target, []byte(`{"vol": 3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "system.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	prev := trimuiSystemJSONPath
	trimuiSystemJSONPath = link
	defer func() { trimuiSystemJSONPath = prev }()
	if err := trimuiSystemVolumeWrite(9); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", info, err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != `{"vol": 9}` {
		t.Fatalf("the target file reads %q, %v", data, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the target file's mode changed: %v %v", info, err)
	}
}

func TestTrimuiSystemJSONVolume(t *testing.T) {
	doc := []byte("{\n\t\"vol\":\t7,\n\t\"brightness\": 6,\n\t\"hpvol\": 3, \"lumination\": 0.5\n}\n")
	if v, ok := trimuiSystemVolumeFrom(doc); !ok || v != 7 {
		t.Fatalf("vol = %d, %v", v, ok)
	}
	out, changed := trimuiSystemJSONWithVolume(doc, 12)
	if !changed {
		t.Fatal("a new level must change the document")
	}
	want := strings.Replace(string(doc), "\"vol\":\t7", "\"vol\":\t12", 1)
	if string(out) != want {
		t.Fatalf("rewrite touched more than vol:\n%s\nwant:\n%s", out, want)
	}
	if _, changed := trimuiSystemJSONWithVolume(out, 12); changed {
		t.Fatal("the same level must not rewrite the file")
	}
	if _, changed := trimuiSystemJSONWithVolume([]byte(`["vol", 3]`), 5); changed {
		t.Fatal("a document that is not an object must be left alone")
	}
	if _, changed := trimuiSystemJSONWithVolume([]byte(`{"vol": 3, "x": {"vol": 4}}`), 5); changed {
		t.Fatal("two \"vol\" members are ambiguous and must be left alone")
	}
	if _, ok := trimuiSystemVolumeFrom([]byte(`{"brightness": 3}`)); ok {
		t.Fatal("a document without vol has no level")
	}

	path := filepath.Join(t.TempDir(), "system.json")
	if err := os.WriteFile(path, doc, 0o640); err != nil {
		t.Fatal(err)
	}
	prev := trimuiSystemJSONPath
	trimuiSystemJSONPath = path
	defer func() { trimuiSystemJSONPath = prev }()
	if err := trimuiSystemVolumeWrite(15); err != nil {
		t.Fatal(err)
	}
	if pct, ok := trimuiSystemVolume(); !ok || pct != 75 {
		t.Fatalf("after writing 15/20 the level reads %d%%, %v", pct, ok)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 && os.PathSeparator == '/' {
		t.Fatalf("the rewrite lost the file's mode: %v %v", info, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*jukahub-tmp")); len(leftovers) > 0 {
		t.Fatalf("temp file left behind: %v", leftovers)
	}
}
