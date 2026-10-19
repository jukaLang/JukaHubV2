package main

// Headless tests for the long-form audio features: .lrc parsing and lookup,
// resume rules, listening speed and the sleep-timer formatting. None of these
// need an SDL renderer, so they run on CI like the rest of the package.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseLRCBasic(t *testing.T) {
	data := "\ufeff[ar:Someone]\n[ti:Song]\n[offset:500]\n" +
		"[00:01.00]first line\n" +
		"[00:05.50][00:20.00]chorus\n" +
		"[01:02]last\n"

	lines := parseLRC(data)
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4: %+v", len(lines), lines)
	}
	want := []struct {
		at   float64
		text string
	}{
		{1.5, "first line"},
		{6.0, "chorus"},
		{20.5, "chorus"},
		{62.5, "last"},
	}
	for i, w := range want {
		if lines[i].At != w.at || lines[i].Text != w.text {
			t.Errorf("line %d = %+v, want At=%v Text=%q", i, lines[i], w.at, w.text)
		}
	}
}

func TestParseLRCStripsEnhancedWordTags(t *testing.T) {
	lines := parseLRC("[00:00.00]<00:00.00>Hello <00:00.40>world")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if lines[0].Text != "Hello world" {
		t.Errorf("Text = %q, want %q", lines[0].Text, "Hello world")
	}
}

func TestParseLRCSkipsMalformed(t *testing.T) {
	data := "[99:99]bad seconds\n" +
		"[notatime]not a timestamp\n" +
		"[00:03.25]good\n" +
		"[00:04.00]\n" // empty lyric text
	lines := parseLRC(data)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %+v", len(lines), lines)
	}
	if lines[0].At != 3.25 || lines[0].Text != "good" {
		t.Errorf("line = %+v", lines[0])
	}
}

func TestParseLRCNegativeOffsetClamps(t *testing.T) {
	lines := parseLRC("[offset:-2000]\n[00:01.00]early")
	if len(lines) != 1 || lines[0].At != 0 {
		t.Fatalf("got %+v, want one line at 0", lines)
	}
}

func TestLyricIndexAt(t *testing.T) {
	lines := []lyricLine{{At: 2, Text: "a"}, {At: 5, Text: "b"}, {At: 9, Text: "c"}}
	cases := []struct {
		pos  float64
		want int
	}{
		{0, -1},
		{2, 0},
		{4.99, 0},
		{5, 1},
		{100, 2},
	}
	for _, c := range cases {
		if got := lyricIndexAt(lines, c.pos); got != c.want {
			t.Errorf("lyricIndexAt(%v) = %d, want %d", c.pos, got, c.want)
		}
	}
	if got := lyricIndexAt(nil, 10); got != -1 {
		t.Errorf("empty lines: got %d, want -1", got)
	}
}

func TestLoadLyricsForFindsSidecar(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "book.mp3")
	if err := os.WriteFile(audio, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadLyricsFor(audio); got != nil {
		t.Fatalf("missing .lrc should return nil, got %+v", got)
	}
	lrc := filepath.Join(dir, "book.lrc")
	if err := os.WriteFile(lrc, []byte("[00:00.50]hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadLyricsFor(audio)
	if len(got) != 1 || got[0].Text != "hi" {
		t.Fatalf("got %+v, want one line %q", got, "hi")
	}
}

func TestAudioStartPositionRules(t *testing.T) {
	long := 3600.0
	cases := []struct {
		name     string
		saved    float64
		duration float64
		want     float64
	}{
		{"none", 0, long, 0},
		{"too early", 20, long, 0},
		{"short music restarts", 300, 240, 0},
		{"near end restarts", 3590, long, 0},
		{"long-form resumes", 600, long, 600},
		{"unknown duration resumes", 120, 0, 120},
	}
	for _, c := range cases {
		if got := audioStartPosition(c.saved, c.duration); got != c.want {
			t.Errorf("%s: audioStartPosition(%v, %v) = %v, want %v", c.name, c.saved, c.duration, got, c.want)
		}
	}
}

func TestMusicMediaElapsedAccountsForSpeed(t *testing.T) {
	rate := int32(44100)
	bytes := int64(rate * 4 * 10) // ten seconds of PCM at 1x
	if got := musicMediaElapsed(0, bytes, rate, 1); got != 10 {
		t.Errorf("1x: got %v, want 10", got)
	}
	if got := musicMediaElapsed(0, bytes, rate, 2); got != 5 {
		t.Errorf("2x: got %v, want 5 (half the media time)", got)
	}
	if got := musicMediaElapsed(30, 0, rate, 1.25); got != 30 {
		t.Errorf("start offset lost: got %v, want 30", got)
	}
}

func TestMusicFFmpegArgsSpeed(t *testing.T) {
	atOne := musicFFmpegArgs("a.mp3", 0, 1, 48000)
	for _, a := range atOne {
		if a == "-af" {
			t.Errorf("1x must not add an atempo filter: %v", atOne)
		}
	}
	if atOne[len(atOne)-1] != "pipe:1" {
		t.Errorf("last arg = %q, want pipe:1", atOne[len(atOne)-1])
	}
	fast := musicFFmpegArgs("a.mp3", 12.5, 1.5, 48000)
	joined := ""
	for i, a := range fast {
		joined += a
		if i > 0 {
			joined += " "
		}
	}
	if !containsArg(fast, "atempo=1.50") {
		t.Errorf("1.5x missing atempo: %s", joined)
	}
	if !containsArg(fast, "-ss") || !containsArg(fast, "12.500") {
		t.Errorf("seek offset missing: %s", joined)
	}
}

func TestAudioClampSpeed(t *testing.T) {
	if got := audioClampSpeed(0.1); got != 0.75 {
		t.Errorf("low clamp = %v, want 0.75", got)
	}
	if got := audioClampSpeed(9); got != 2 {
		t.Errorf("high clamp = %v, want 2", got)
	}
	if got := audioClampSpeed(1.25); got != 1.25 {
		t.Errorf("mid = %v, want 1.25", got)
	}
}

func TestFormatSpeedLabel(t *testing.T) {
	cases := map[float64]string{1: "1x", 1.25: "1.25x", 0.75: "0.75x", 2: "2x", 1.5: "1.5x"}
	for in, want := range cases {
		if got := formatSpeedLabel(in); got != want {
			t.Errorf("formatSpeedLabel(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatSleepRemaining(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "1m"},
		{14 * time.Minute, "14m"},
		{65 * time.Minute, "1h 05m"},
	}
	for _, c := range cases {
		if got := formatSleepRemaining(c.in); got != c.want {
			t.Errorf("formatSleepRemaining(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// withAudioPrefsTempFile points the resume store at a temp file and resets the
// in-memory state for the duration of one test.
func withAudioPrefsTempFile(t *testing.T) string {
	t.Helper()
	prevPath := audioResumeFilePath
	prevLoaded := audioPrefs.loaded
	prevSpeed := audioPrefs.speed
	prevEntries := audioPrefs.entries

	path := filepath.Join(t.TempDir(), audioResumeFileName)
	audioResumeFilePath = func() string { return path }

	audioPrefs.mu.Lock()
	audioPrefs.loaded = false
	audioPrefs.speed = 1
	audioPrefs.entries = nil
	audioPrefs.mu.Unlock()

	t.Cleanup(func() {
		audioResumeFilePath = prevPath
		audioPrefs.mu.Lock()
		audioPrefs.loaded = prevLoaded
		audioPrefs.speed = prevSpeed
		audioPrefs.entries = prevEntries
		audioPrefs.mu.Unlock()
	})
	return path
}

func TestAudioResumeStoreRoundTrip(t *testing.T) {
	path := withAudioPrefsTempFile(t)

	audioRememberPosition("/books/one.mp3", 600, 3600)
	if got := audioResumePosition("/books/one.mp3"); got != 600 {
		t.Fatalf("position = %v, want 600", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store was not written: %v", err)
	}

	// Reload from disk as a fresh launch would.
	audioPrefs.mu.Lock()
	audioPrefs.loaded = false
	audioPrefs.entries = nil
	audioPrefs.mu.Unlock()
	if got := audioResumePosition("/books/one.mp3"); got != 600 {
		t.Fatalf("position after reload = %v, want 600", got)
	}

	audioForgetPosition("/books/one.mp3")
	if got := audioResumePosition("/books/one.mp3"); got != 0 {
		t.Fatalf("position after forget = %v, want 0", got)
	}

	audioSaveSpeed(1.5)
	if got := audioSavedSpeed(); got != 1.5 {
		t.Fatalf("speed = %v, want 1.5", got)
	}
}

func TestAudioResumePrunesOldest(t *testing.T) {
	withAudioPrefsTempFile(t)
	for i := 0; i < audioResumeMaxEntries+10; i++ {
		audioRememberPosition(filepath.Join("/books", string(rune('a'+i%26))+string(rune('a'+i/26))+".mp3"), float64(60+i), 3600)
	}
	audioPrefs.mu.Lock()
	n := len(audioPrefs.entries)
	audioPrefs.mu.Unlock()
	if n > audioResumeMaxEntries {
		t.Fatalf("entries = %d, want at most %d", n, audioResumeMaxEntries)
	}
}
