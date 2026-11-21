package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// cueGenTestSector is a raw sector: the data sync and mode byte, or silence
// (mode 0) or music (mode -1) for an audio track.
func cueGenTestSector(mode int) []byte {
	s := make([]byte, cueGenSectorRaw)
	switch {
	case mode > 0:
		copy(s, cueGenSync)
		s[15] = byte(mode)
	case mode < 0:
		for i := range s {
			s[i] = byte(i*7 + 1)
		}
	}
	return s
}

func cueGenTestFile(t *testing.T, path string, mode, sectors int) {
	t.Helper()
	var data []byte
	for i := 0; i < sectors; i++ {
		data = append(data, cueGenTestSector(mode)...)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCueGenSectorMode(t *testing.T) {
	if got := cueGenSectorMode(cueGenTestSector(2)); got != "MODE2/2352" {
		t.Errorf("mode 2 sector = %q", got)
	}
	if got := cueGenSectorMode(cueGenTestSector(1)); got != "MODE1/2352" {
		t.Errorf("mode 1 sector = %q", got)
	}
	if got := cueGenSectorMode(cueGenTestSector(-1)); got != "" {
		t.Errorf("audio sector = %q, want no mode", got)
	}
	if got := cueGenSectorMode(cueGenSync); got != "" {
		t.Errorf("12 bytes = %q, want no mode", got)
	}
	// A cooked image: MODE1/2048 for a Sega or NEC CD folder, nothing for a
	// PlayStation one.
	cooked := int64(cueGenSectorCooked * 1000)
	if mode, ok := cueGenSingleMode(make([]byte, 16), cooked, "MODE1/2352"); !ok || mode != "MODE1/2048" {
		t.Errorf("cooked Sega CD image = %q, %v", mode, ok)
	}
	if _, ok := cueGenSingleMode(make([]byte, 16), cooked, "MODE2/2352"); ok {
		t.Error("cooked PlayStation image got a sheet")
	}
}

func TestCueGenTrackSetsSortAndGaps(t *testing.T) {
	var names []string
	for _, n := range []string{"10", "2", "1", "3", "4", "5", "6", "7", "8", "9"} {
		names = append(names, "Game (USA) (Track "+n+").bin")
	}
	names = append(names, "Other (Track 01).bin", "Other (Track 03).bin", "Lone.bin")
	sets := cueGenTrackSets(names)
	if len(sets) != 1 || sets[0].Base != "Game (USA)" || len(sets[0].Files) != 10 {
		t.Fatalf("sets = %+v, want only Game (USA) with 10 tracks (Other misses track 2)", sets)
	}
	if sets[0].Files[1] != "Game (USA) (Track 2).bin" || sets[0].Files[9] != "Game (USA) (Track 10).bin" {
		t.Errorf("track order = %q", sets[0].Files)
	}
}

func TestCueGenThreeTrackSheet(t *testing.T) {
	dir := t.TempDir()
	cueGenTestFile(t, filepath.Join(dir, "Game (Track 1).bin"), 2, 160)
	cueGenTestFile(t, filepath.Join(dir, "Game (Track 2).bin"), 0, 160)
	cueGenTestFile(t, filepath.Join(dir, "Game (Track 3).bin"), 0, 160)
	made, errs := cueGenTrackSetSheets(dir)
	if made != 1 || len(errs) != 0 {
		t.Fatalf("made %d, errs %v", made, errs)
	}
	got, err := os.ReadFile(filepath.Join(dir, "Game.cue"))
	if err != nil {
		t.Fatal(err)
	}
	want := "FILE \"Game (Track 1).bin\" BINARY\n" +
		"  TRACK 01 MODE2/2352\n" +
		"    INDEX 01 00:00:00\n" +
		"FILE \"Game (Track 2).bin\" BINARY\n" +
		"  TRACK 02 AUDIO\n" +
		"    INDEX 00 00:00:00\n" +
		"    INDEX 01 00:02:00\n" +
		"FILE \"Game (Track 3).bin\" BINARY\n" +
		"  TRACK 03 AUDIO\n" +
		"    INDEX 00 00:00:00\n" +
		"    INDEX 01 00:02:00\n"
	if string(got) != want {
		t.Errorf("sheet =\n%s\nwant\n%s", got, want)
	}
	// Run again: the set is named by its sheet now, nothing more is made.
	if made, _ := cueGenTrackSetSheets(dir); made != 0 {
		t.Errorf("second run made %d", made)
	}
	// A PC Engine CD: music first, then a data track whose file opens with
	// a second of audio-type silence (no sync) before its MODE1 sectors.
	pce := t.TempDir()
	cueGenTestFile(t, filepath.Join(pce, "P (Track 1).bin"), -1, 4)
	data := append(make([]byte, 75*cueGenSectorRaw), cueGenTestSector(1)...)
	for len(data) < 300*cueGenSectorRaw {
		data = append(data, cueGenTestSector(1)...)
	}
	if err := os.WriteFile(filepath.Join(pce, "P (Track 2).bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	tracks, why := cueGenReadTracks(pce, cueGenTrackSet{Base: "P", Files: []string{"P (Track 1).bin", "P (Track 2).bin"}})
	if why != "" || len(tracks) != 2 || tracks[0].Mode != cueGenModeAudio || tracks[1].Mode != "MODE1/2352" || tracks[1].Pregap != cueGenDataPregap {
		t.Errorf("PC Engine CD tracks = %+v (%s)", tracks, why)
	}
}

func TestCueGenNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	cueGenTestFile(t, filepath.Join(dir, "Game (Track 1).bin"), 1, 4)
	cueGenTestFile(t, filepath.Join(dir, "Game (Track 2).bin"), -1, 4)
	own := filepath.Join(dir, "Game.cue")
	if err := os.WriteFile(own, []byte("REM mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if made, _ := cueGenTrackSetSheets(dir); made != 0 {
		t.Errorf("made %d beside the user's own sheet", made)
	}
	if err := cueGenWriteNew(own, "FILE \"x.bin\" BINARY\n"); !errors.Is(err, os.ErrExist) {
		t.Errorf("cueGenWriteNew over a sheet: %v", err)
	}
	if got, _ := os.ReadFile(own); string(got) != "REM mine\n" {
		t.Errorf("the user's sheet became %q", got)
	}
}
