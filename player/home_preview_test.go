package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHomeWaveLevelsAndBuckets drives the audio-graph maths with a synthetic
// track: a quiet half then a loud half must come out as low bars then tall
// bars, normalised to the peak.
func TestHomeWaveLevelsAndBuckets(t *testing.T) {
	const rate = 2000
	var pcm bytes.Buffer
	for i := 0; i < rate*4; i++ {
		amp := 0.05
		if i >= rate*2 {
			amp = 0.8
		}
		v := int16(amp * 32767 * math.Sin(float64(i)*2*math.Pi*220/rate))
		_ = binary.Write(&pcm, binary.LittleEndian, v)
	}
	levels := homeWaveLevels(bytes.NewReader(pcm.Bytes()), rate/4)
	if len(levels) != 16 {
		t.Fatalf("got %d windows for 4 s at 4 windows/s, want 16", len(levels))
	}
	bars := homeWaveBucket(levels)
	first, last := bars[0], bars[homeWaveBars-1]
	if !(first < last) {
		t.Fatalf("quiet start %v is not lower than the loud end %v", first, last)
	}
	peak := float32(0)
	for _, b := range bars {
		if b < 0.12-1e-6 || b > 1+1e-6 {
			t.Fatalf("bar %v is outside 0.12..1", b)
		}
		if b > peak {
			peak = b
		}
	}
	if math.Abs(float64(peak)-1) > 1e-4 {
		t.Fatalf("the loudest bar is %v, want 1", peak)
	}
	if got := homeWaveBucket(nil); got != ([homeWaveBars]float32{}) {
		t.Fatalf("no levels gave %v, want an empty graph", got)
	}
}

// TestHomeSyntheticWaveIsStable: the stand-in graph is the same for the same
// track (no flicker between frames) and different for another one.
func TestHomeSyntheticWaveIsStable(t *testing.T) {
	a1 := homeSyntheticWave("/sd/Music/a.mp3")
	a2 := homeSyntheticWave("/sd/Music/a.mp3")
	b := homeSyntheticWave("/sd/Music/b.mp3")
	if a1 != a2 {
		t.Fatal("the same track drew two different graphs")
	}
	if a1 == b {
		t.Fatal("two tracks drew the same graph")
	}
	for _, v := range a1 {
		if v < 0.15 || v > 1 {
			t.Fatalf("bar %v is outside the drawable range", v)
		}
	}
}

// TestHomePreviewPNGHasRoundedCorners: the preview is cover-cropped to the
// card's size and its corners are transparent, so the frame needs no clipping
// when it is drawn.
func TestHomePreviewPNGHasRoundedCorners(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 160, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 160; x++ {
			src.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
		}
	}
	data, err := homePreviewPNG(src, 120, 80, 12)
	if err != nil {
		t.Fatalf("homePreviewPNG: %v", err)
	}
	out, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := out.Bounds(); b.Dx() != 120 || b.Dy() != 80 {
		t.Fatalf("preview is %dx%d, want the card's 120x80", b.Dx(), b.Dy())
	}
	if _, _, _, a := out.At(0, 0).RGBA(); a != 0 {
		t.Errorf("corner alpha = %d, want transparent", a)
	}
	if _, _, _, a := out.At(119, 79).RGBA(); a != 0 {
		t.Errorf("far corner alpha = %d, want transparent", a)
	}
	if r, _, _, a := out.At(60, 40).RGBA(); a != 0xffff || r>>8 < 190 {
		t.Errorf("centre = r%d a%d, want the opaque picture", r>>8, a)
	}
	if got := homeCornerAlpha(60, 0, 120, 80, 12); got != 1 {
		t.Errorf("top edge middle alpha = %v, want 1", got)
	}
}

// TestHomeRecentAudioRoundTrips: a song on the Continue timeline survives the
// favorites file — it comes back as a FileEntry with its title, not as a map.
func TestHomeRecentAudioRoundTrips(t *testing.T) {
	in := FavoriteItem{Type: "audio", Data: FileEntry{Name: "Song Title", Path: "/sd/Music/song.mp3"}, Timestamp: time.Unix(1700000000, 0)}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out FavoriteItem
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fe, ok := out.Data.(FileEntry)
	if !ok || fe.Path != "/sd/Music/song.mp3" {
		t.Fatalf("round trip gave %#v", out.Data)
	}
	if got := out.Label(); got != "Song Title" {
		t.Fatalf("Label = %q, want the track title", got)
	}
}

// TestHomeRecordRecentAudioDedupes: playing the same song again moves it to
// the top instead of listing it twice.
func TestHomeRecordRecentAudioDedupes(t *testing.T) {
	favoritesMutex.Lock()
	prev := favoritesStore
	favoritesStore = FavoritesStore{Recent: []FavoriteItem{
		{Type: "video", Data: VideoInfo{Title: "Clip", URL: "https://example.com/v"}, Timestamp: time.Now().Add(-time.Hour)},
	}}
	favoritesMutex.Unlock()
	prevCache := userConfigCache
	userConfigCache = &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{}}}
	t.Cleanup(func() {
		favoritesMutex.Lock()
		favoritesStore = prev
		favoritesMutex.Unlock()
		userConfigCache = prevCache
	})

	homeRecordRecentAudio("/sd/Music/a.mp3", "A")
	homeRecordRecentAudio("/sd/Music/b.mp3", "B")
	homeRecordRecentAudio("/sd/Music/a.mp3", "A")
	favoritesMutex.Lock()
	recent := append([]FavoriteItem(nil), favoritesStore.Recent...)
	favoritesMutex.Unlock()
	if len(recent) != 3 {
		t.Fatalf("timeline has %d items, want 3 (a, b, the clip): %+v", len(recent), recent)
	}
	if fe, _ := recent[0].Data.(FileEntry); recent[0].Type != "audio" || fe.Path != "/sd/Music/a.mp3" {
		t.Fatalf("top item = %+v, want the song played last", recent[0])
	}
}

// TestHomeContinueStateNamesWhatItResumes: the card's chip, verb and preview
// follow the kind of item.
func TestHomeContinueStateNamesWhatItResumes(t *testing.T) {
	video := homeContinueStateFor(FavoriteItem{Type: "video", Timestamp: time.Now(),
		Data: VideoInfo{Title: "Clip", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Duration: 600, Position: 150}})
	if video.Kind != "video" || video.Chip != "Continue watching" || video.Remaining != formatTime(450)+" left" {
		t.Errorf("video card = kind %q chip %q remaining %q", video.Kind, video.Chip, video.Remaining)
	}
	if video.Progress < 0.24 || video.Progress > 0.26 {
		t.Errorf("video progress = %v, want 0.25", video.Progress)
	}
	if video.Preview.Key == "" {
		t.Error("a web video has no preview job")
	}

	song := homeContinueStateFor(FavoriteItem{Type: "audio", Timestamp: time.Now(),
		Data: FileEntry{Name: "07 - Track.mp3", Path: "/nowhere/Album/07 - Track.mp3"}})
	if song.Kind != "music" || !song.HasWave || song.Action != "Play" {
		t.Errorf("song card = kind %q wave %v action %q", song.Kind, song.HasWave, song.Action)
	}
	if song.Title != "07 - Track" {
		t.Errorf("song title = %q, want the file name without its extension", song.Title)
	}
	if song.WaveReal {
		t.Error("a missing file claims a measured audio graph")
	}
}

// TestHomeIsLocalMedia separates files on the device from addresses.
func TestHomeIsLocalMedia(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		file:                           true,
		dir:                            false,
		filepath.Join(dir, "gone.mp4"): false,
		"https://example.com/a.mp4":    false,
		"":                             false,
	}
	for in, want := range cases {
		if got := homeIsLocalMedia(in); got != want {
			t.Errorf("homeIsLocalMedia(%q) = %v, want %v", in, got, want)
		}
	}
}
