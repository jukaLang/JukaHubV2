package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A non-YouTube link must not pay for the three YouTube player-client
// invocations. yt-dlp ignores those arguments for every other extractor, and
// each call is a ~2.8 s unpack of the one-file binary on a cold handheld.
func TestSiteStreamAttemptsDoNotAskForYouTubeClients(t *testing.T) {
	attempts := siteStreamAttempts(720)
	if len(attempts) != 1 {
		t.Fatalf("site path has %d attempts, want 1", len(attempts))
	}
	args := strings.Join(attempts[0].args, " ")
	if strings.Contains(args, "youtube:player_client") {
		t.Fatalf("site attempt still asks for YouTube clients: %q", args)
	}
	if !strings.Contains(args, "height<=720") || !strings.Contains(args, "/best") {
		t.Fatalf("site attempt lost the height ceiling or its fallback: %q", args)
	}
	// A video-only format must not win the ranking: the audio decoder would
	// find no stream and the clip would play silent.
	if !strings.Contains(args, "vcodec!=none][acodec!=none") {
		t.Fatalf("site selector can pick a silent format: %q", args)
	}
	if !strings.Contains(siteStreamFormatSelector(720), "acodec!=none") {
		t.Fatal("site selector lost its audio requirement")
	}
}

func TestStreamFormatSelectorPrefersAudio(t *testing.T) {
	sel := streamFormatSelector(720)
	if !strings.HasPrefix(sel, "best[height<=720][ext=mp4][acodec!=none]/") {
		t.Fatalf("YouTube's first format term can pick a silent MP4: %q", sel)
	}
}

func TestSourceIsYouTube(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", true},
		{"https://youtu.be/dQw4w9WgXcQ", true},
		{"https://www.redgifs.com/watch/AbCdEf", false},
		{"https://soundcloud.com/artist/track", false},
		{"", false},
	} {
		if got := sourceIsYouTube(tc.url); got != tc.want {
			t.Fatalf("sourceIsYouTube(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestResolveWithRunnerUsesOneCallForSites(t *testing.T) {
	url := "https://www.redgifs.com/watch/AbCdEf"
	calls := 0
	var seen []string
	direct, err := resolveWithRunner(context.Background(), url, 720, nil, false,
		func(_ context.Context, args []string) (string, error) {
			calls++
			seen = append(seen, strings.Join(args, " "))
			return "https://media.redgifs.com/clip.mp4", nil
		})
	if err != nil || direct == "" {
		t.Fatalf("site extraction failed: %v, %q", err, direct)
	}
	if calls != 1 {
		t.Fatalf("site extraction ran %d calls, want 1", calls)
	}
	if strings.Contains(seen[0], "youtube:player_client") {
		t.Fatalf("site call carried YouTube args: %q", seen[0])
	}
}

func TestResolveWithRunnerKeepsYouTubeClients(t *testing.T) {
	url := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	calls := 0
	_, err := resolveWithRunner(context.Background(), url, 720, nil, false,
		func(_ context.Context, _ []string) (string, error) {
			calls++
			return "", errors.New("temporary failure")
		})
	if err == nil {
		t.Fatal("all YouTube attempts succeeded unexpectedly")
	}
	if calls != 3 {
		t.Fatalf("YouTube ran %d calls, want 3", calls)
	}
}

func TestYtdlAttemptsTryMuxedFileFirstOnSites(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	site := ytdlAttempts(&Config{}, "https://www.redgifs.com/watch/AbCdEf", ffmpeg)
	if len(site) < 2 {
		t.Fatalf("site download has %d attempts, want a muxed first and a joined fallback", len(site))
	}
	if site[0].name != "single file" || site[1].name != "picture and sound" {
		t.Fatalf("site attempt order = %q, %q", site[0].name, site[1].name)
	}
	if args := strings.Join(site[0].args, " "); !strings.Contains(args, "vcodec!=none][acodec!=none") {
		t.Fatalf("site download can pick a silent file: %q", args)
	}
	youTube := ytdlAttempts(&Config{}, "https://www.youtube.com/watch?v=dQw4w9WgXcQ", ffmpeg)
	if len(youTube) == 0 || youTube[0].name != "picture and sound" {
		t.Fatalf("YouTube attempt order changed: %+v", youTube)
	}
}
