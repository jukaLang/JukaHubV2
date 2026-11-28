package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRadioICYPollReadsStreamTitle reads one ICY metadata block after the
// audio bytes the header declares.
func TestRadioICYPollReadsStreamTitle(t *testing.T) {
	meta := "StreamTitle='Artist - Song';"
	if len(meta) > 16 {
		meta += string(make([]byte, 16-len(meta)%16))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Icy-MetaData") != "1" {
			t.Errorf("request did not ask for ICY metadata")
		}
		w.Header().Set("icy-metaint", "4")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("abcd"))
		_, _ = w.Write([]byte{byte(len(meta) / 16)})
		_, _ = w.Write([]byte(meta))
	}))
	defer srv.Close()

	title, err := radioICYPoll(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if title != "Artist - Song" {
		t.Fatalf("title = %q, want %q", title, "Artist - Song")
	}
}

// TestRadioICYPollNeedsTheHeader refuses a stream that does not carry ICY
// metadata rather than reading it as if it did.
func TestRadioICYPollNeedsTheHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("plain audio"))
	}))
	defer srv.Close()

	if _, err := radioICYPoll(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("a stream without icy-metaint was read as metadata")
	}
}

// TestRadioNowShowsBufferingAndICYTitle pins what both radio screens say while
// a station connects and what the station is playing afterwards.
func TestRadioNowShowsBufferingAndICYTitle(t *testing.T) {
	np := musicNowState{live: true, playing: true, buffering: true}
	if got := np.state(); got != "Buffering" {
		t.Fatalf("state = %q, want Buffering", got)
	}
	if got := radioNowLine(np); got != "Buffering · Internet radio" {
		t.Fatalf("line = %q", got)
	}
	np.buffering = false
	np.stationTitle = "Artist - Song"
	if got := radioNowTitle(np); got != "Artist - Song" {
		t.Fatalf("title = %q, want the ICY title", got)
	}
	if got := radioNowLine(np); got != "Now playing · Internet radio" {
		t.Fatalf("line = %q", got)
	}
}

// TestMusicNowTurnsBufferingOffAtTheFirstPCM proves the player state, not just
// a hand-built one, carries the buffering flag.
func TestMusicNowTurnsBufferingOffAtTheFirstPCM(t *testing.T) {
	music.mu.Lock()
	oldTracks, oldCurrent, oldPlaying, oldPaused, oldLive, oldPCM := music.tracks, music.current, music.playing, music.paused, music.liveURLs, music.pcmBytes
	music.tracks = []musicTrack{{Path: "http://radio.example/live", Name: "Station"}}
	music.current = 0
	music.playing = true
	music.paused = false
	music.liveURLs = map[string]bool{"http://radio.example/live": true}
	music.pcmBytes = 0
	music.mu.Unlock()
	defer func() {
		music.mu.Lock()
		music.tracks, music.current, music.playing, music.paused, music.liveURLs, music.pcmBytes = oldTracks, oldCurrent, oldPlaying, oldPaused, oldLive, oldPCM
		music.mu.Unlock()
	}()

	np, ok := musicNow()
	if !ok || !np.buffering || np.state() != "Buffering" {
		t.Fatalf("before PCM: ok=%v buffering=%v state=%q", ok, np.buffering, np.state())
	}
	music.mu.Lock()
	music.pcmBytes = 1
	music.mu.Unlock()
	np, _ = musicNow()
	if np.buffering || np.state() != "Playing" {
		t.Fatalf("after PCM: buffering=%v state=%q", np.buffering, np.state())
	}
}
