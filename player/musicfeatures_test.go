package main

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestMusicInitials(t *testing.T) {
	cases := []struct{ title, want string }{
		{"I Want You Now", "iwyn"},
		{"  I_Want... You  Now ", "iwyn"},
		{"Sgt. Pepper's Lonely Hearts Club Band", "splhcb"},
		{"Don\u2019t Stop", "ds"},
		{"01 - Intro", "0i"},
		{"", ""},
	}
	for _, c := range cases {
		if got := musicInitials(c.title); got != c.want {
			t.Errorf("musicInitials(%q) = %q, want %q", c.title, got, c.want)
		}
	}
	// File names go through musicTitleInitials, which drops the extension first.
	names := []struct{ name, want string }{
		{"I Want You Now.mp3", "iwyn"},
		{"Don't Stop (Remix).flac", "dsr"},
		{"01 - Intro.aiff", "0i"},
	}
	for _, c := range names {
		if got := musicTitleInitials(c.name); got != c.want {
			t.Errorf("musicTitleInitials(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMusicSearchMatch(t *testing.T) {
	title := "I Want You Now.mp3"
	for _, query := range []string{"iwyn", "IWYN", "iw", "i w y n", "i.w.y.n"} {
		if !musicSearchMatch(title, query) {
			t.Errorf("%q should match %q", query, title)
		}
	}
	for _, query := range []string{"", " ", "wyn", "iwynx", "you"} {
		if musicSearchMatch(title, query) {
			t.Errorf("%q should not match %q", query, title)
		}
	}
	// The file extension is never part of the initialism.
	if !musicSearchMatch("01 - Intro.aiff", "0i") {
		t.Error("extension leaked into the initials")
	}
}

func TestMusicSearchHitsPrefersExactInitials(t *testing.T) {
	tracks := []musicTrack{
		{Name: "I Want You.mp3"},        // initials iwy: a prefix of iwyn
		{Name: "I Want You Now.mp3"},    // exact
		{Name: "I Want You Now (Live)"}, // not iwyn
		{Name: "Into Water.mp3"},
	}
	hits := musicSearchHits(tracks, "iwyn", 10)
	// The exact title comes first; the live version shares the prefix and is
	// listed after it rather than being hidden.
	if len(hits) != 2 || hits[0] != 1 || hits[1] != 2 {
		t.Fatalf("iwyn hits = %v, want [1 2]", hits)
	}

	// A shorter query lists the exact initialism first, then longer titles.
	hits = musicSearchHits(tracks, "iwy", 10)
	if len(hits) != 3 || hits[0] != 0 || hits[1] != 1 || hits[2] != 2 {
		t.Fatalf("iwy hits = %v, want [0 1 2]", hits)
	}

	if hits := musicSearchHits(tracks, "", 10); len(hits) != 0 {
		t.Fatalf("empty query returned %v", hits)
	}
	// The limit is applied after ranking.
	if hits := musicSearchHits(tracks, "i", 1); len(hits) != 1 || hits[0] != 0 {
		t.Fatalf("limited hits = %v, want [0]", hits)
	}
}

func TestMusicSearchMoveWrapsAndClamps(t *testing.T) {
	hits := []int{0, 1, 2}
	if got := musicSearchMove(hits, 0, -1); got != 2 {
		t.Errorf("up from first = %d, want 2", got)
	}
	if got := musicSearchMove(hits, 2, 1); got != 0 {
		t.Errorf("down from last = %d, want 0", got)
	}
	if got := musicSearchMove(nil, 0, 1); got != -1 {
		t.Errorf("empty hits selection = %d, want -1", got)
	}
}

func TestMusicVolumeHoldSkipsOnce(t *testing.T) {
	start := time.Unix(1000, 0)
	var hold musicVolumeHold

	delta, fresh := hold.press(sdl.K_EQUALS, start)
	if delta != 0 || !fresh {
		t.Fatalf("first press = (%d, %v), want (0, true)", delta, fresh)
	}
	// Repeats inside the threshold must not skip and must not report fresh.
	if delta, fresh := hold.press(sdl.K_EQUALS, start.Add(200*time.Millisecond)); delta != 0 || fresh {
		t.Fatalf("early repeat = (%d, %v)", delta, fresh)
	}
	delta, fresh = hold.press(sdl.K_EQUALS, start.Add(musicHoldSkipDelay+10*time.Millisecond))
	if delta != 1 || fresh {
		t.Fatalf("held volume up = (%d, %v), want (1, false)", delta, fresh)
	}
	// Only one skip per hold, however long it is held.
	if delta, _ := hold.press(sdl.K_EQUALS, start.Add(2*time.Second)); delta != 0 {
		t.Fatalf("second skip = %d, want 0", delta)
	}
	// Releasing the key ends the hold, so an immediate re-press is fresh.
	hold.release(sdl.K_EQUALS)
	if delta, fresh := hold.press(sdl.K_EQUALS, start.Add(2*time.Second)); delta != 0 || !fresh {
		t.Fatalf("after release = (%d, %v), want (0, true)", delta, fresh)
	}
	// A press that outlives the gap without a key-up still counts as one hold.
	if delta, fresh := hold.press(sdl.K_EQUALS, start.Add(10*time.Second)); delta != 0 || !fresh {
		t.Fatalf("stale hold = (%d, %v), want (0, true)", delta, fresh)
	}
}

func TestMusicVolumeHoldDownGoesPrevious(t *testing.T) {
	start := time.Unix(2000, 0)
	var hold musicVolumeHold
	hold.press(sdl.K_MINUS, start)
	delta, _ := hold.press(sdl.K_MINUS, start.Add(musicHoldSkipDelay+time.Millisecond))
	if delta != -1 {
		t.Fatalf("held volume down = %d, want -1", delta)
	}
	// The other key starts its own hold rather than continuing this one.
	if delta, fresh := hold.press(sdl.K_EQUALS, start.Add(musicHoldSkipDelay+20*time.Millisecond)); delta != 0 || !fresh {
		t.Fatalf("switching key = (%d, %v), want (0, true)", delta, fresh)
	}
}

func TestMusicVolumeKey(t *testing.T) {
	for _, key := range []sdl.Keycode{sdl.K_MINUS, sdl.K_KP_MINUS, sdl.K_EQUALS, sdl.K_PLUS, sdl.K_KP_PLUS} {
		if !musicVolumeKey(key) {
			t.Errorf("%v should be a volume key", key)
		}
	}
	if musicVolumeKey(sdl.K_RIGHT) {
		t.Error("right arrow is not a volume key")
	}
}

func TestMusicExitTimerCycle(t *testing.T) {
	if got := musicTimerCycle(0, musicExitOptions); got != 15*time.Minute {
		t.Fatalf("off -> %v, want 15m", got)
	}
	if got := musicTimerCycle(15*time.Minute, musicExitOptions); got != 30*time.Minute {
		t.Fatalf("15m -> %v, want 30m", got)
	}
	if got := musicTimerCycle(90*time.Minute, musicExitOptions); got != 0 {
		t.Fatalf("90m -> %v, want off", got)
	}
	// A part-way timer still advances from the option it is inside.
	if got := musicTimerCycle(20*time.Minute, musicExitOptions); got != 60*time.Minute {
		t.Fatalf("20m -> %v, want 60m", got)
	}
}

func TestMusicScreenModeFromConfig(t *testing.T) {
	cases := []struct {
		value interface{}
		want  musicScreenMode
	}{
		{nil, musicScreenAny},
		{"any", musicScreenAny},
		{"controls", musicScreenControls},
		{"Playback", musicScreenControls},
		{"select-only", musicScreenSelect},
		{"nonsense", musicScreenAny},
	}
	for _, c := range cases {
		config := &Config{}
		config.Variables.Custom = map[string]interface{}{"musicScreenOffMode": c.value}
		if got := musicScreenModeFromConfig(config); got != c.want {
			t.Errorf("%v -> %v, want %v", c.value, got, c.want)
		}
	}
	if got := musicScreenModeFromConfig(nil); got != musicScreenAny {
		t.Errorf("nil config -> %v", got)
	}
}

func TestMusicScreenActionAllowed(t *testing.T) {
	if !musicScreenActionAllowed(musicScreenAny, musicActionWake) {
		t.Error("wake must always work")
	}
	if musicScreenActionAllowed(musicScreenAny, musicActionNext) {
		t.Error("any-key mode should only wake")
	}
	if !musicScreenActionAllowed(musicScreenControls, musicActionNext) ||
		!musicScreenActionAllowed(musicScreenControls, musicActionVolume) {
		t.Error("controls mode should keep transport working")
	}
	if musicScreenActionAllowed(musicScreenSelect, musicActionPlayPause) {
		t.Error("select mode must ignore transport")
	}
	if musicScreenActionAllowed(musicScreenSelect, musicActionNone) {
		t.Error("unmapped input must not act")
	}
}

func TestMusicScreenOffInputClassifiers(t *testing.T) {
	if got := musicScreenOffKey(sdl.K_n); got != musicActionNext {
		t.Errorf("N = %v, want next", got)
	}
	if got := musicScreenOffKey(sdl.K_o); got != musicActionWake {
		t.Errorf("O = %v, want wake", got)
	}
	if got := musicScreenOffButton(sdl.CONTROLLER_BUTTON_BACK); got != musicActionWake {
		t.Errorf("Select = %v, want wake", got)
	}
	if got := musicScreenOffButton(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER); got != musicActionNext {
		t.Errorf("R1 = %v, want next", got)
	}
	if got := musicScreenOffButton(sdl.CONTROLLER_BUTTON_A); got != musicActionNone {
		t.Errorf("A = %v, want none", got)
	}
}
