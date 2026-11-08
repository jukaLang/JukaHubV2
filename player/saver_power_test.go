package main

// The idle low-power state against a fake cpufreq tree: what it lowers, that it
// puts back exactly what it found (and only what is still its own), that a
// crash marker is replayed by the next start, and the frame budget and poller
// gate that go with it.

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const saverTestOPPs = "408000 600000 816000 1008000 1200000 1608000 1800000 2000000"

// saverFakePolicy writes one cpufreq policy at root/cpuN/cpufreq.
func saverFakePolicy(t *testing.T, root string, cpu int, governor, maxFreq, minFreq string) string {
	t.Helper()
	dir := filepath.Join(root, "cpu"+strconv.Itoa(cpu), "cpufreq")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"scaling_governor":              governor,
		"scaling_available_governors":   "ondemand powersave performance schedutil",
		"scaling_max_freq":              maxFreq,
		"scaling_min_freq":              minFreq,
		"cpuinfo_min_freq":              "408000",
		"cpuinfo_max_freq":              "2000000",
		"scaling_available_frequencies": saverTestOPPs,
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// saverPowerTestEnv points the low-power state at a fresh fake tree and a
// private marker file, and puts every piece of state back afterwards.
func saverPowerTestEnv(t *testing.T) (root, marker string) {
	t.Helper()
	root = t.TempDir()
	marker = filepath.Join(t.TempDir(), "idle-cpufreq.json")
	prevRoot, prevSupported, prevMarker := cpufreqRoot, saverPowerSupported, saverPowerMarkerPath
	cpufreqRoot = root
	saverPowerSupported = func() bool { return true }
	saverPowerMarkerPath = func() string { return marker }
	reset := func() {
		saverPower.mu.Lock()
		saverPower.mode = saverPowerOff
		saverPower.writes = nil
		saverPower.since = time.Time{}
		saverPower.checked = time.Time{}
		saverPower.denied = false
		saverPower.mu.Unlock()
		saverPowerEngaged.Store(false)
	}
	reset()
	t.Cleanup(func() {
		reset()
		cpufreqRoot, saverPowerSupported, saverPowerMarkerPath = prevRoot, prevSupported, prevMarker
	})
	return root, marker
}

func saverReadFile(t *testing.T, path string) string {
	t.Helper()
	return saverReadTrimmed(path)
}

func TestSaverIdleCeilingChoice(t *testing.T) {
	opps := []int64{1800000, 408000, 816000, 600000, 1200000}
	cases := []struct {
		name           string
		available      []int64
		cpuMin, cpuMax int64
		floor          int64
		want           int64
	}{
		{"lowest point at the floor", opps, 0, 0, 600000, 600000},
		{"next point above the floor", opps, 0, 0, 700000, 816000},
		{"floor above every point", opps, 0, 0, 3000000, 1800000},
		{"no table: the floor", nil, 408000, 2000000, 600000, 600000},
		{"no table: the CPU's minimum", nil, 900000, 2000000, 600000, 900000},
		{"no table: the CPU's maximum", nil, 0, 500000, 600000, 500000},
		{"nothing known", nil, 0, 0, 600000, 0},
	}
	for _, c := range cases {
		if got := saverIdleCeilingKHz(c.available, c.cpuMin, c.cpuMax, c.floor); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSaverPowerWant(t *testing.T) {
	settled := saverPowerSettle + time.Second
	cases := []struct {
		name                       string
		idleUp                     bool
		idleFor                    time.Duration
		alarm, video, launch, play bool
		want                       saverPowerMode
	}{
		{"awake", false, time.Hour, false, false, false, false, saverPowerOff},
		{"idle screen still settling", true, time.Second, false, false, false, false, saverPowerOff},
		{"idle and silent", true, settled, false, false, false, false, saverPowerQuiet},
		{"idle with music", true, settled, false, false, false, true, saverPowerAudio},
		{"alarm ringing", true, settled, true, false, false, false, saverPowerOff},
		{"video on screen", true, settled, false, true, false, false, saverPowerOff},
		{"a game owns the CPU", true, settled, false, false, true, false, saverPowerOff},
	}
	for _, c := range cases {
		if got := saverPowerWant(c.idleUp, c.idleFor, c.alarm, c.video, c.launch, c.play); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The quiet profile lowers every policy's ceiling to the lowest operating
// point at the floor and switches to powersave; waking puts back exactly the
// values that were there, and the marker goes with them.
func TestSaverPowerEngagesAndRestoresExactly(t *testing.T) {
	root, marker := saverPowerTestEnv(t)
	var dirs []string
	for cpu := 0; cpu < 4; cpu++ {
		dirs = append(dirs, saverFakePolicy(t, root, cpu, "ondemand", "1800000", "408000"))
	}

	saverPowerApply(saverPowerQuiet)
	if !saverLowPowerEngaged() {
		t.Fatal("the quiet profile did not engage")
	}
	for _, dir := range dirs {
		if got := saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); got != "600000" {
			t.Errorf("%s: ceiling %q, want 600000", dir, got)
		}
		if got := saverReadFile(t, filepath.Join(dir, "scaling_governor")); got != "powersave" {
			t.Errorf("%s: governor %q, want powersave", dir, got)
		}
		if got := saverReadFile(t, filepath.Join(dir, "scaling_min_freq")); got != "408000" {
			t.Errorf("%s: a floor below the ceiling was touched: %q", dir, got)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no crash marker while the CPU is lowered: %v", err)
	}

	saverPowerWake()
	if saverLowPowerEngaged() {
		t.Fatal("waking left the low-power state engaged")
	}
	for _, dir := range dirs {
		if got := saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); got != "1800000" {
			t.Errorf("%s: ceiling after wake %q, want 1800000", dir, got)
		}
		if got := saverReadFile(t, filepath.Join(dir, "scaling_governor")); got != "ondemand" {
			t.Errorf("%s: governor after wake %q, want ondemand", dir, got)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the crash marker outlived the restore: %v", err)
	}
}

// With music on the dock the governor stays and the ceiling keeps headroom for
// the decoder.
func TestSaverPowerAudioKeepsTheGovernor(t *testing.T) {
	root, _ := saverPowerTestEnv(t)
	dir := saverFakePolicy(t, root, 0, "schedutil", "2000000", "408000")
	saverPowerApply(saverPowerAudio)
	if got := saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); got != "816000" {
		t.Errorf("ceiling with music %q, want 816000", got)
	}
	if got := saverReadFile(t, filepath.Join(dir, "scaling_governor")); got != "schedutil" {
		t.Errorf("governor with music %q, want it left alone", got)
	}
	// Music stopping on the dock moves to the quiet profile, from the
	// original values rather than from the audio ones.
	saverPowerApply(saverPowerQuiet)
	if got := saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); got != "600000" {
		t.Errorf("quiet ceiling %q, want 600000", got)
	}
	saverPowerApply(saverPowerOff)
	if got := saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); got != "2000000" {
		t.Errorf("restored ceiling %q, want 2000000", got)
	}
	if got := saverReadFile(t, filepath.Join(dir, "scaling_governor")); got != "schedutil" {
		t.Errorf("restored governor %q, want schedutil", got)
	}
}

// A firmware floor above the idle ceiling is lowered first (the kernel refuses
// a ceiling under the floor) and raised last on the way back.
func TestSaverPowerLowersAHighFloorFirst(t *testing.T) {
	root, _ := saverPowerTestEnv(t)
	dir := saverFakePolicy(t, root, 0, "performance", "1800000", "1008000")
	plan := saverPowerPlan(root, saverPowerQuiet)
	if len(plan) < 2 || filepath.Base(plan[0].Path) != "scaling_min_freq" || filepath.Base(plan[1].Path) != "scaling_max_freq" {
		t.Fatalf("plan %+v: the floor must be lowered before the ceiling", plan)
	}
	saverPowerApply(saverPowerQuiet)
	if lo, hi := saverReadFile(t, filepath.Join(dir, "scaling_min_freq")), saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); lo != "600000" || hi != "600000" {
		t.Fatalf("floor %s ceiling %s, want both at 600000", lo, hi)
	}
	saverPowerApply(saverPowerOff)
	if lo, hi := saverReadFile(t, filepath.Join(dir, "scaling_min_freq")), saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); lo != "1008000" || hi != "1800000" {
		t.Fatalf("restored floor %s ceiling %s, want 1008000 / 1800000", lo, hi)
	}
	if got := saverReadFile(t, filepath.Join(dir, "scaling_governor")); got != "performance" {
		t.Fatalf("restored governor %q, want performance", got)
	}
}

// A value someone else changed while the screen was idle — a launch profile,
// the shutdown's restore, the user — is theirs: the wake does not stomp it.
func TestSaverPowerRestoreYieldsToOtherWriters(t *testing.T) {
	root, _ := saverPowerTestEnv(t)
	dir := saverFakePolicy(t, root, 0, "ondemand", "1800000", "408000")
	saverPowerApply(saverPowerQuiet)
	if err := os.WriteFile(filepath.Join(dir, "scaling_governor"), []byte("performance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	saverPowerApply(saverPowerOff)
	if got := saverReadFile(t, filepath.Join(dir, "scaling_governor")); got != "performance" {
		t.Fatalf("the wake overwrote another writer's governor with %q", got)
	}
	if got := saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); got != "1800000" {
		t.Fatalf("the untouched ceiling was not restored: %q", got)
	}
}

// A run that died lowered leaves a marker; the next start replays it before
// the boot values are recorded.
func TestSaverPowerRecoversAfterACrash(t *testing.T) {
	root, marker := saverPowerTestEnv(t)
	dir := saverFakePolicy(t, root, 0, "ondemand", "1800000", "408000")
	saverPowerApply(saverPowerQuiet)
	// The crash: the process is gone, its memory with it, the marker stays.
	saverPower.mu.Lock()
	saverPower.mode = saverPowerOff
	saverPower.writes = nil
	saverPower.mu.Unlock()
	saverPowerEngaged.Store(false)

	saverPowerRecover()
	if got := saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); got != "1800000" {
		t.Fatalf("recovery left the ceiling at %q", got)
	}
	if got := saverReadFile(t, filepath.Join(dir, "scaling_governor")); got != "ondemand" {
		t.Fatalf("recovery left the governor at %q", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the marker survived the recovery: %v", err)
	}
	// A second start with no marker changes nothing.
	saverPowerRecover()
}

// On the A133P every cpuN/cpufreq is a link to one policy: it is written once,
// so its original value is recorded once and restored correctly.
func TestSaverPowerSharedPolicyIsPlannedOnce(t *testing.T) {
	root, _ := saverPowerTestEnv(t)
	saverFakePolicy(t, root, 0, "ondemand", "1800000", "408000")
	for cpu := 1; cpu < 4; cpu++ {
		parent := filepath.Join(root, "cpu"+strconv.Itoa(cpu))
		if err := os.MkdirAll(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "cpu0", "cpufreq"), filepath.Join(parent, "cpufreq")); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}
	}
	plan := saverPowerPlan(root, saverPowerQuiet)
	if len(plan) != 2 {
		t.Fatalf("plan has %d writes for one shared policy, want 2 (ceiling, governor): %+v", len(plan), plan)
	}
	saverPowerApply(saverPowerQuiet)
	saverPowerApply(saverPowerOff)
	if got := saverReadFile(t, filepath.Join(root, "cpu0", "cpufreq", "scaling_max_freq")); got != "1800000" {
		t.Fatalf("shared ceiling restored to %q", got)
	}
}

// A firmware that refuses the writes still gets the slow frame rate and the
// parked pollers, and is not asked again on every idle screen.
func TestSaverPowerReadOnlyFirmware(t *testing.T) {
	root, _ := saverPowerTestEnv(t)
	dir := saverFakePolicy(t, root, 0, "ondemand", "1800000", "408000")
	for _, name := range []string{"scaling_max_freq", "scaling_governor"} {
		path := filepath.Join(dir, name)
		if err := os.Chmod(path, 0o444); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(path, 0o644) })
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			f.Close()
			t.Skip("this test user can write read-only files (root?)")
		}
	}
	saverPowerApply(saverPowerQuiet)
	if !saverLowPowerEngaged() {
		t.Fatal("a read-only firmware lost the frame-rate half of the state")
	}
	saverPower.mu.Lock()
	denied, writes := saverPower.denied, len(saverPower.writes)
	saverPower.mu.Unlock()
	if !denied || writes != 0 {
		t.Fatalf("denied=%v writes=%d, want the refusal remembered and nothing recorded", denied, writes)
	}
	saverPowerApply(saverPowerOff)
	if got := saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); got != "1800000" {
		t.Fatalf("a refused write changed the ceiling to %q", got)
	}
}

// The tick follows the idle screen: nothing before it has settled, the state
// once it has, and the wake path restores before the next frame.
func TestSaverPowerTickFollowsTheIdleScreen(t *testing.T) {
	root, _ := saverPowerTestEnv(t)
	dir := saverFakePolicy(t, root, 0, "ondemand", "1800000", "408000")
	prevActive, prevFading := ss.active, ss.fadingIn
	t.Cleanup(func() { ss.active, ss.fadingIn = prevActive, prevFading })
	ambient.mu.Lock()
	prevAmbient := ambient.active
	ambient.active = false
	ambient.mu.Unlock()
	t.Cleanup(func() {
		ambient.mu.Lock()
		ambient.active = prevAmbient
		ambient.mu.Unlock()
	})
	music.mu.Lock()
	prevPlaying, prevPaused := music.playing, music.paused
	music.playing, music.paused = false, false
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		music.playing, music.paused = prevPlaying, prevPaused
		music.mu.Unlock()
	})
	embeddedPlayer.mu.Lock()
	prevPhase := embeddedPlayer.phase
	embeddedPlayer.phase = "idle"
	embeddedPlayer.mu.Unlock()
	t.Cleanup(func() {
		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = prevPhase
		embeddedPlayer.mu.Unlock()
	})

	start := time.Now()
	ss.active = true
	saverPowerTick(start)
	if saverLowPowerEngaged() {
		t.Fatal("engaged before the idle screen settled")
	}
	saverPowerTick(start.Add(saverPowerSettle + time.Second))
	if !saverLowPowerEngaged() {
		t.Fatalf("not engaged after %v of idle screen", saverPowerSettle+time.Second)
	}
	if got := saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); got != "600000" {
		t.Fatalf("idle ceiling %q, want 600000", got)
	}
	if !backgroundPaused() {
		t.Error("the background pollers were not parked")
	}

	deactivateScreensaver()
	if saverLowPowerEngaged() {
		t.Fatal("the wake did not leave the low-power state")
	}
	if got := saverReadFile(t, filepath.Join(dir, "scaling_max_freq")); got != "1800000" {
		t.Fatalf("ceiling after the wake %q, want 1800000", got)
	}
	if backgroundPaused() && !gameScreenSuspended() && !windowMinimised() {
		t.Error("the pollers stayed parked after the wake")
	}
}

// The frame budget: none awake, the starfield's 10 fps, the dock's slower one,
// and the normal cadence again while a toast animates.
func TestSaverFrameBudget(t *testing.T) {
	saverPowerTestEnv(t)
	currentToastMu.Lock()
	prevToast := currentToast
	currentToast = ToastState{}
	currentToastMu.Unlock()
	t.Cleanup(func() {
		currentToastMu.Lock()
		currentToast = prevToast
		currentToastMu.Unlock()
	})
	ambient.mu.Lock()
	prevAmbient := ambient.active
	ambient.active = false
	ambient.mu.Unlock()
	t.Cleanup(func() {
		ambient.mu.Lock()
		ambient.active = prevAmbient
		ambient.mu.Unlock()
	})
	now := time.Now()
	if got := saverFrameBudget(now); got != 0 {
		t.Fatalf("awake budget %d, want 0 (the normal cadence)", got)
	}
	saverPowerEngaged.Store(true)
	if got := saverFrameBudget(now); got != saverStarfieldFrameMS || got > 100 {
		t.Fatalf("starfield budget %d, want %d (at most 10 fps)", got, saverStarfieldFrameMS)
	}
	ambient.mu.Lock()
	ambient.active = true
	ambient.mu.Unlock()
	if got := saverFrameBudget(now); got != saverDockFrameMS {
		t.Fatalf("dock budget %d, want %d", got, saverDockFrameMS)
	}
	currentToastMu.Lock()
	currentToast = ToastState{Message: "Volume 40%", StartedAt: sdl.GetTicks64(), Duration: 2500}
	currentToastMu.Unlock()
	if got := saverFrameBudget(now); got != 0 {
		t.Fatalf("budget with a toast up %d, want the normal cadence", got)
	}
}

// The low-power state is the handheld's: off the device nothing engages.
func TestSaverPowerIsHandheldOnly(t *testing.T) {
	if runtime.GOOS != "linux" {
		if saverPowerSupported() {
			t.Fatal("the low-power state claims support off Linux")
		}
		return
	}
	if !IsTrimuiDevice() && saverPowerSupported() {
		t.Fatal("the low-power state claims support on a machine that is not a TrimUI")
	}
}
