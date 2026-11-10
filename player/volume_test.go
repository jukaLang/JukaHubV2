package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The attenuation curve runs the right way (100% is raw 0, the loudest),
// reproduces the two points NextUI measured on a tg5040, and reads back every
// 5% step exactly, so a level set by the popover is the level the poll reports.
func TestTrimuiAttenuationCurve(t *testing.T) {
	for _, c := range []struct{ pct, raw int }{
		{100, 0}, {0, 63}, {50, 13}, {20, 30}, {5, 56}, {-4, 63}, {140, 0},
	} {
		if got := trimuiAttenuationFor(c.pct, 63); got != c.raw {
			t.Errorf("trimuiAttenuationFor(%d) = %d, want %d", c.pct, got, c.raw)
		}
	}
	prev := 64
	for pct := 0; pct <= 100; pct++ {
		raw := trimuiAttenuationFor(pct, 63)
		if raw > prev {
			t.Fatalf("attenuation rose from %d to %d at %d%%: louder must mean less attenuation", prev, raw, pct)
		}
		prev = raw
	}
	for pct := 5; pct <= 100; pct += 5 {
		if back := trimuiPercentForAttenuation(trimuiAttenuationFor(pct, 63), 63); back != pct {
			t.Errorf("%d%% reads back as %d%%", pct, back)
		}
	}
	if got := trimuiPercentForAttenuation(63, 63); got != 5 {
		t.Errorf("the quietest audible step reads %d%%, want 5 (0 means muted)", got)
	}
	if got := trimuiAttenuationFor(50, 0); got != 13 {
		t.Errorf("an unknown range must fall back to 0..63, got %d", got)
	}
}

func TestParseAmixerOutput(t *testing.T) {
	names := parseAmixerSimpleControls("Simple mixer control 'digital volume',0\nSimple mixer control 'DAC Volume',0\nSimple mixer control 'Headphone',0\n")
	if strings.Join(names, "|") != "digital volume|DAC Volume|Headphone" {
		t.Fatalf("scontrols parsed as %q", names)
	}
	out := `Simple mixer control 'digital volume',0
  Capabilities: pvolume pvolume-joined
  Playback channels: Mono
  Limits: Playback 0 - 63
  Mono: Playback 30 [48%] [-30.00dB]
`
	if raw, top, ok := parseAmixerLevel(out); !ok || raw != 30 || top != 63 {
		t.Fatalf("digital volume parsed as raw=%d top=%d ok=%v", raw, top, ok)
	}
	stereo := "Simple mixer control 'DAC Volume',0\n  Limits: 0 - 255\n  Front Left: 160 [63%]\n  Front Right: 150 [59%]\n"
	if raw, top, ok := parseAmixerLevel(stereo); !ok || raw != 160 || top != 255 {
		t.Fatalf("DAC Volume parsed as raw=%d top=%d ok=%v", raw, top, ok)
	}
	if _, _, ok := parseAmixerLevel("amixer: Unable to find simple control 'Master',0\n"); ok {
		t.Fatal("an error message must not parse as a level")
	}
}

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

// A level is written to "digital volume" as an attenuation; zero also takes
// the DAC to 0 (the quietest attenuation is not silent), and the next audible
// level puts the DAC back where it was.
func TestTrimuiSetVolumeMuteAndRestore(t *testing.T) {
	fake := withFakeTrimuiMixer(t, 170)
	if err := trimuiSetVolume(50); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.calls, "\n"); !strings.Contains(got, "amixer -q sset digital volume 13") {
		t.Fatalf("50%% ran:\n%s", got)
	}
	fake.calls = nil
	if err := trimuiSetVolume(0); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fake.calls, "\n")
	if !strings.Contains(joined, "amixer -q sset DAC Volume 0") || !strings.Contains(joined, "amixer -q sset digital volume 63") {
		t.Fatalf("mute ran:\n%s", joined)
	}
	if fake.dac != 0 {
		t.Fatalf("DAC after mute = %d", fake.dac)
	}
	fake.calls = nil
	if err := trimuiSetVolume(30); err != nil {
		t.Fatal(err)
	}
	if fake.dac != 170 {
		t.Fatalf("un-mute restored the DAC to %d, want the 170 it had", fake.dac)
	}
	if got := strings.Join(fake.calls, "\n"); !strings.Contains(got, "amixer -q sset digital volume 22") {
		t.Fatalf("30%% ran:\n%s", got)
	}
}

// A DAC found at 0 when JukaHub starts (muted by the firmware) is restored to
// its 0 dB point by the first audible level.
func TestTrimuiSetVolumeRestoresAnInheritedMute(t *testing.T) {
	fake := withFakeTrimuiMixer(t, 0)
	if err := trimuiSetVolume(40); err != nil {
		t.Fatal(err)
	}
	if fake.dac != trimuiDACUnmuteDefault {
		t.Fatalf("DAC = %d, want %d", fake.dac, trimuiDACUnmuteDefault)
	}
}

// A DAC muted behind JukaHub's back (the firmware's keys at level 0) is read
// back and restored by the next audible level, not left silent at "40%".
func TestTrimuiAudibleLevelLiftsAnExternalMute(t *testing.T) {
	fake := withFakeTrimuiMixer(t, 170)
	if err := trimuiSetVolume(50); err != nil {
		t.Fatal(err)
	}
	fake.dac = 0 // the firmware muted
	if err := trimuiSetVolume(40); err != nil {
		t.Fatal(err)
	}
	if fake.dac != 170 {
		t.Fatalf("DAC after an audible level = %d, want the 170 it had", fake.dac)
	}
}

// The level shown comes from the codec. After a mute made here, the firmware's
// keys moving the attenuation off its top un-mute the DAC on the next poll; a
// mute the firmware made itself is left for the firmware to lift, and a DAC
// someone else restored ends the mute.
func TestTrimuiVolumeFollowsTheCodec(t *testing.T) {
	fake := withFakeTrimuiMixer(t, 170)
	if err := trimuiSetVolume(0); err != nil {
		t.Fatal(err)
	}
	if pct, ok := trimuiVolume(); !ok || pct != 0 {
		t.Fatalf("after a mute the level reads %d, %v", pct, ok)
	}
	fake.digital = 56 // a hardware Vol+ press: the firmware wrote 5%
	if pct, ok := trimuiVolume(); !ok || pct != 5 {
		t.Fatalf("after the key the level reads %d, %v; want 5", pct, ok)
	}
	if fake.dac != 170 {
		t.Fatalf("the key's level stayed silent: DAC = %d", fake.dac)
	}

	fake = withFakeTrimuiMixer(t, 0)
	fake.digital = 30
	if pct, ok := trimuiVolume(); !ok || pct != 0 {
		t.Fatalf("a firmware mute reads %d, %v; want 0", pct, ok)
	}
	if fake.dac != 0 {
		t.Fatal("the firmware's own mute must not be lifted by a poll")
	}
	fake.dac = 150
	if pct, ok := trimuiVolume(); !ok || pct != 20 {
		t.Fatalf("after the firmware un-muted the level reads %d, %v; want 20", pct, ok)
	}

	// amixer not answering: the firmware's file is the fallback.
	fake.failGet = true
	path := filepath.Join(t.TempDir(), "system.json")
	if err := os.WriteFile(path, []byte(`{"vol": 7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	trimuiSystemJSONPath = path
	if pct, ok := trimuiVolume(); !ok || pct != 35 {
		t.Fatalf("the fallback read %d, %v; want 35", pct, ok)
	}
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

// The control names are used as amixer spelled them, and a probe that found
// neither control is asked again later instead of settling on Master/PCM.
func TestTrimuiProbeNamesAndRetry(t *testing.T) {
	fake := withFakeTrimuiMixer(t, 170)
	fake.scontrols = "Simple mixer control 'Headphone',0\n"
	if err := trimuiSetVolume(50); err != nil {
		t.Fatal(err)
	}
	trimuiMixer.mu.Lock()
	probed := trimuiMixer.probed
	trimuiMixer.probedAt = time.Now().Add(-2 * trimuiProbeRetry)
	trimuiMixer.mu.Unlock()
	if probed {
		t.Fatal("a probe that found neither control must be asked again")
	}
	fake.scontrols = "Simple mixer control 'Digital Volume',0\nSimple mixer control 'DAC Volume',0\n"
	fake.calls = nil
	if err := trimuiSetVolume(50); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.calls, "\n"); !strings.Contains(got, "amixer -q sset Digital Volume 13") {
		t.Fatalf("the control was not written as amixer spelled it:\n%s", got)
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

func TestVolumeSnapAndWinmmLevels(t *testing.T) {
	for _, c := range []struct{ in, want int }{{0, 0}, {2, 0}, {3, 5}, {37, 35}, {38, 40}, {99, 100}, {-5, 0}, {130, 100}} {
		if got := volumeSnap(c.in); got != c.want {
			t.Errorf("volumeSnap(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	for pct := 0; pct <= 100; pct++ {
		if back := winmmPercentFor(winmmLevelFor(pct)); back != pct {
			t.Fatalf("winmm level for %d%% reads back as %d%%", pct, back)
		}
	}
	if winmmLevelFor(100) != 0xFFFF || winmmLevelFor(0) != 0 {
		t.Fatal("winmm range ends are wrong")
	}
	for _, c := range []struct{ in, want int }{{0, 0}, {1, 5}, {2, 5}, {3, 5}, {37, 35}, {98, 100}, {-5, 0}} {
		if got := volumeSnapAudible(c.in); got != c.want {
			t.Errorf("volumeSnapAudible(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// A reading is kept only when no change was asked for since it began and the
// newest change had been written before it began.
func TestVolumeReadingCurrent(t *testing.T) {
	for _, c := range []struct {
		edit, gen, applied int
		want               bool
	}{
		{2, 2, 2, true},  // nothing pending
		{3, 2, 2, false}, // a change was asked for during the read
		{2, 2, 1, false}, // the newest change was still being written
		{0, 0, 0, true},  // no change yet
	} {
		if got := volumeReadingCurrent(c.edit, c.gen, c.applied); got != c.want {
			t.Errorf("volumeReadingCurrent(%d, %d, %d) = %v, want %v", c.edit, c.gen, c.applied, got, c.want)
		}
	}
}

func TestStatusVolumeGlyph(t *testing.T) {
	for _, c := range []struct {
		pct   int
		known bool
		want  string
	}{{0, false, "volume"}, {0, true, "volume-mute"}, {30, true, "volume-low"}, {50, true, "volume"}, {100, true, "volume"}} {
		if got := statusVolumeGlyph(c.pct, c.known); got != c.want {
			t.Errorf("statusVolumeGlyph(%d, %v) = %q, want %q", c.pct, c.known, got, c.want)
		}
	}
}

// The card stays on screen under a speaker drawn near the right edge, on the
// Smart Pro's canvas and the Brick's, and its slider maps the pointer onto
// 0..100 with the ends clamped.
func TestVolumePopoverLayout(t *testing.T) {
	for _, c := range []struct{ w, h int32 }{{1280, 720}, {960, 720}} {
		anchor := sdl.Rect{X: c.w - 300, Y: 0, W: 44, H: 56}
		l := volumePopoverLayoutFor(c.w, c.h, anchor, 24, 18, true)
		if l.card.X < 16 || l.card.X+l.card.W > c.w-16 || l.card.Y < 56 || l.card.Y+l.card.H > c.h {
			t.Fatalf("%dx%d: card %+v leaves the screen", c.w, c.h, l.card)
		}
		if l.track.X < l.mute.X+l.mute.W || l.track.X+l.track.W > l.card.X+l.card.W {
			t.Fatalf("%dx%d: track %+v is not between the mute button and the card's edge", c.w, c.h, l.track)
		}
		if l.noteY+18 > l.card.Y+l.card.H {
			t.Fatalf("%dx%d: the note runs off the card", c.w, c.h)
		}
		if got := volumePercentAtX(l.track, l.track.X-50); got != 0 {
			t.Fatalf("left of the track = %d", got)
		}
		if got := volumePercentAtX(l.track, l.track.X+l.track.W+50); got != 100 {
			t.Fatalf("right of the track = %d", got)
		}
		if got := volumePercentAtX(l.track, l.track.X+l.track.W/2); got < 49 || got > 51 {
			t.Fatalf("the middle of the track = %d", got)
		}
	}
	if l := volumePopoverLayoutFor(1280, 720, sdl.Rect{}, 24, 18, false); l.card.X+l.card.W != 1280-32 {
		t.Fatalf("an unmeasured button anchors the card at the corner, got %+v", l.card)
	}
}
