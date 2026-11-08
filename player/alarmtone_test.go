package main

// alarmtone_test.go — ringtones: the synthesised motifs, the music choice, the
// fade-in, and the music folders the picker lists.

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Every synthesised ringtone renders to audible PCM that starts at silence and
// never clips.
func TestAlarmSoundPCMIsLoudAndClean(t *testing.T) {
	const rate = 48000
	for _, s := range alarmSoundList {
		pcm := alarmSoundPCM(rate, s)
		if len(pcm) == 0 {
			t.Fatalf("%s rendered nothing", s.ID)
		}
		if pcm[0] != 0 {
			t.Errorf("%s starts at %d, want silence", s.ID, pcm[0])
		}
		peak := 0.0
		for _, v := range pcm {
			if a := math.Abs(float64(v)); a > peak {
				peak = a
			}
		}
		if peak > 0.951*32767 {
			t.Errorf("%s peaks at %.0f, past the 95%% ceiling", s.ID, peak)
		}
		if peak < 0.8*32767*s.Volume {
			t.Errorf("%s peaks at %.0f: an alarm must be loud", s.ID, peak)
		}
	}
	if alarmSoundPCM(rate, alarmSound{}) != nil {
		t.Error("a ringtone with no notes rendered audio")
	}
}

// The ring starts audible and reaches full scale over five seconds, never
// getting quieter on the way; a preview is gentler and fades out at its end.
func TestAlarmGainFadesIn(t *testing.T) {
	if g := alarmGain(0, false); math.Abs(g-alarmFadeFloor) > 1e-9 {
		t.Fatalf("gain at the start = %v, want the floor %v", g, alarmFadeFloor)
	}
	if g := alarmGain(alarmFadeIn, false); math.Abs(g-1) > 1e-9 {
		t.Fatalf("gain after the fade = %v, want 1", g)
	}
	if g := alarmGain(time.Minute, false); math.Abs(g-1) > 1e-9 {
		t.Fatalf("gain a minute in = %v, want 1", g)
	}
	prev := 0.0
	for ms := 0; ms <= 6000; ms += 250 {
		g := alarmGain(time.Duration(ms)*time.Millisecond, false)
		if g < prev {
			t.Fatalf("gain dropped from %v to %v at %d ms", prev, g, ms)
		}
		prev = g
	}
	if g := alarmGain(time.Second, true); g >= 1 || g <= 0 {
		t.Fatalf("preview gain = %v", g)
	}
	if g := alarmGain(alarmPreviewLength, true); g != 0 {
		t.Fatalf("preview gain at its end = %v, want silence", g)
	}
}

// A music ringtone is its own id, named after its file; anything else unknown
// falls back to the default chime.
func TestAlarmMusicRingtoneIDs(t *testing.T) {
	id := alarmMusicPrefix + filepath.Join("music", "Morning Song.mp3")
	if !alarmIsMusic(id) || alarmIsMusic(alarmMusicPrefix) || alarmIsMusic("chime") {
		t.Fatal("alarmIsMusic misclassified an id")
	}
	if got := alarmSoundByID(id); got.ID != id || got.Name != "Morning Song" {
		t.Fatalf("music ringtone resolved to %+v", got)
	}
	if got := alarmSoundName(alarmMusicPickID); got != "Choose from music…" {
		t.Fatalf("the music entry reads %q", got)
	}
	if got := alarmMusicPath(id); got != filepath.Join("music", "Morning Song.mp3") {
		t.Fatalf("music path = %q", got)
	}
	if alarmSoundByID(alarmMusicPickID).ID != alarmDefaultSound {
		t.Fatal("the picker entry is not a ringtone of its own")
	}
}

// Left/Right on the editor's row walks the synthesised list with one more stop
// for music: the chosen track when there is one, else "Choose from music…".
func TestAlarmCycleRingtone(t *testing.T) {
	first := alarmSoundList[0].ID
	last := alarmSoundList[len(alarmSoundList)-1].ID
	if got := alarmCycleRingtone(last, "", 1); got != alarmMusicPickID {
		t.Fatalf("past the last tone = %q, want the music entry", got)
	}
	if got := alarmCycleRingtone(alarmMusicPickID, "", 1); got != first {
		t.Fatalf("past the music entry = %q, want the first tone", got)
	}
	if got := alarmCycleRingtone(first, "", -1); got != alarmMusicPickID {
		t.Fatalf("before the first tone = %q, want the music entry", got)
	}
	track := alarmMusicPrefix + "/music/a.mp3"
	if got := alarmCycleRingtone(last, track, 1); got != track {
		t.Fatalf("with a track chosen, the music stop = %q, want the track", got)
	}
	if got := alarmCycleRingtone(track, track, 1); got != first {
		t.Fatalf("past the chosen track = %q, want the first tone", got)
	}
	if got := alarmCycleRingtone(first, "", 1); got != alarmSoundList[1].ID {
		t.Fatalf("next after the first tone = %q", got)
	}
}

// The picker lists the playable tracks under the music folders: the four
// formats (and their usual siblings), sorted by name, a few levels deep, no
// hidden folders, never more than the limit.
func TestAlarmScanMusic(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b song.mp3")
	write("A song.OGG")
	write("album/c track.flac")
	write("album/d track.wav")
	write("album/cover.jpg")
	write("notes.txt")
	write(".hidden/secret.mp3")
	write("a/b/c/d/too deep.mp3")

	files := alarmScanMusic([]string{root}, 100)
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	want := []string{"A song", "b song", "c track", "d track"}
	if len(names) != len(want) {
		t.Fatalf("scan found %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("scan found %v, want %v", names, want)
		}
	}
	if got := alarmScanMusic([]string{root}, 2); len(got) != 2 {
		t.Fatalf("a limit of 2 returned %d tracks", len(got))
	}
	if got := alarmScanMusic([]string{filepath.Join(root, "missing")}, 10); len(got) != 0 {
		t.Fatalf("a missing folder returned %d tracks", len(got))
	}
}

// A music ringtone whose file is gone fails its decode at once, so the player
// rings the default chime instead of staying silent.
func TestAlarmDecodeMusicMissingFileFails(t *testing.T) {
	var pcm alarmPCM
	alarmDecodeMusic(t.Context(), filepath.Join(t.TempDir(), "gone.mp3"), 48000, 5, &pcm)
	samples, done, failed := pcm.view()
	if !done || !failed || len(samples) != 0 {
		t.Fatalf("missing file: done %v failed %v samples %d", done, failed, len(samples))
	}
}
