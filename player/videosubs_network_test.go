package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestSubtitleLanguageAndArguments(t *testing.T) {
	config := &Config{}
	if lang, err := subtitleLanguage(config); err != nil || lang != "en" {
		t.Fatalf("default language = %q, %v", lang, err)
	}
	config.Variables.SubtitleLanguage = "pt-BR"
	lang, err := subtitleLanguage(config)
	if err != nil || lang != "pt-BR" {
		t.Fatalf("language = %q, %v", lang, err)
	}
	args := subtitleDownloadArgs(config, "temp", "https://example.com/video", lang)
	for _, arg := range []string{"--skip-download", "--write-subs", "--write-auto-subs", "--sub-langs", "pt-BR", "--sub-format", "vtt/srt"} {
		if !strings.Contains("\x00"+strings.Join(args, "\x00")+"\x00", "\x00"+arg+"\x00") {
			t.Fatalf("missing %q in args %v", arg, args)
		}
	}
	if !reflect.DeepEqual(args[len(args)-2:], []string{"--", "https://example.com/video"}) {
		t.Fatalf("URL is not the final positional argument: %v", args)
	}
	config.Variables.SubtitleLanguage = "en.*"
	if _, err := subtitleLanguage(config); err == nil {
		t.Fatal("regex language selector accepted as a language code")
	}
}

func TestSubtitleLanguagePersists(t *testing.T) {
	config := &Config{}
	config.Variables.SubtitleLanguage = "fr"
	data, err := json.Marshal(userConfigFromVariables(config))
	if err != nil {
		t.Fatal(err)
	}
	var user UserConfig
	if err := json.Unmarshal(data, &user); err != nil {
		t.Fatal(err)
	}
	loaded := &Config{}
	mergeUserConfig(loaded, &user)
	if got := loaded.Variables.SubtitleLanguage; got != "fr" {
		t.Fatalf("restored language = %q", got)
	}
}

func TestSubtitleDownloadFollowsToggleAndSession(t *testing.T) {
	oldDownload := subtitleDownload
	defer func() { subtitleDownload = oldDownload; setSubtitlesEnabled(false); resetVideoSubtitles(nil) }()
	type request struct {
		ctx    context.Context
		lang   string
		result chan struct{}
	}
	requests := make(chan request, 4)
	done := make(chan struct{}, 4)
	subtitleDownload = func(ctx context.Context, _ *Config, _ string, lang string) ([]subtitleCue, string) {
		r := request{ctx: ctx, lang: lang, result: make(chan struct{})}
		requests <- r
		<-r.result // deliberately finish even if canceled, testing stale results
		defer func() { done <- struct{}{} }()
		return []subtitleCue{{Start: 0, End: 5, Text: "hello"}}, "test"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess := &playbackSession{ctx: ctx, pageURL: "https://example.com/watch"}
	config := &Config{}
	config.Variables.SubtitleLanguage = "es"
	setSubtitlesEnabled(false)
	resetVideoSubtitles(sess)
	startEnabledNetworkSubtitles(config)
	select {
	case <-requests:
		t.Fatal("download started with CC off")
	default:
	}
	if !toggleSubtitlesForConfig(config) {
		t.Fatal("CC did not turn on without preloaded cues")
	}
	r := <-requests
	if r.lang != "es" {
		t.Fatalf("requested language %q", r.lang)
	}
	if toggleSubtitlesForConfig(config) {
		t.Fatal("CC did not turn off")
	}
	if r.ctx.Err() == nil {
		t.Fatal("turning off CC did not cancel download")
	}
	close(r.result)
	<-done
	if subtitlesAvailable() {
		t.Fatal("canceled download installed subtitles")
	}
	if !toggleSubtitlesForConfig(config) {
		t.Fatal("CC did not turn on again")
	}
	r = <-requests
	resetVideoSubtitles(&playbackSession{ctx: ctx, pageURL: "https://example.com/other"})
	close(r.result)
	<-done
	if subtitlesAvailable() {
		t.Fatal("previous session installed subtitles")
	}
	startEnabledNetworkSubtitles(config)
	r = <-requests
	close(r.result)
	<-done
	deadline := time.Now().Add(time.Second)
	for !subtitlesAvailable() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := subtitleTextAt(1); got != "hello" {
		t.Fatalf("current session subtitle = %q", got)
	}
	if toggleSubtitlesForConfig(config) {
		t.Fatal("CC did not turn off")
	}
	config.Variables.SubtitleLanguage = "fr"
	if !toggleSubtitlesForConfig(config) {
		t.Fatal("CC did not turn on with new language")
	}
	r = <-requests
	if r.lang != "fr" || subtitlesAvailable() {
		t.Fatalf("language change reused old subtitles: request=%q", r.lang)
	}
	close(r.result)
	<-done
	deadline = time.Now().Add(time.Second)
	for !subtitlesAvailable() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !subtitlesAvailable() {
		t.Fatal("new language cues did not load")
	}
}

func TestSubtitleVTTMarkupAndCueBoundary(t *testing.T) {
	cues := parseSubtitles("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<c>Fish &amp; chips</c>\n")
	if len(cues) != 1 || cues[0].Text != "Fish & chips" {
		t.Fatalf("cues: %+v", cues)
	}
	videoSubs.mu.Lock()
	oldCues, oldEnabled := videoSubs.cues, videoSubs.enabled
	videoSubs.cues, videoSubs.enabled = cues, true
	videoSubs.mu.Unlock()
	defer func() {
		videoSubs.mu.Lock()
		videoSubs.cues, videoSubs.enabled = oldCues, oldEnabled
		videoSubs.mu.Unlock()
	}()
	if subtitleTextAt(1.5) != "Fish & chips" || subtitleTextAt(2) != "" {
		t.Fatal("incorrect active cue boundary")
	}
}

func TestSubtitleOverlayUsesWhiteTextAndTranslucentBlack(t *testing.T) {
	resetVideoUIControls(t)
	loadTestCues(t)
	setSubtitlesEnabled(true)
	renderer := probeRenderer(t)
	renderer.SetDrawColor(255, 255, 255, 255)
	renderer.Clear()
	renderVideoSubtitles(renderer, &Config{}, 0, 0, 500, 200, 1, 20)
	pixels := make([]byte, 500*200*4)
	rect := sdl.Rect{X: 0, Y: 0, W: 500, H: 200}
	if err := renderer.ReadPixels(&rect, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&pixels[0]), 500*4); err != nil {
		t.Fatal(err)
	}
	gray, whiteInPanel := 0, 0
	for y := 110; y < 185; y++ {
		for x := 200; x < 300; x++ {
			i := (y*500 + x) * 4
			b, g, r := pixels[i], pixels[i+1], pixels[i+2]
			if r == g && g == b && r > 20 && r < 150 {
				gray++
			}
			if r == 255 && g == 255 && b == 255 && x > 220 && x < 280 && y > 145 && y < 170 {
				whiteInPanel++
			}
		}
	}
	if gray == 0 || whiteInPanel == 0 {
		t.Fatalf("expected blended black panel and white subtitle pixels; got %d dark and %d white", gray, whiteInPanel)
	}
}

func TestIsNetworkSource(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/watch?v=1": true,
		"HTTP://example.com/a.mp4":      true,
		"rtsp://cam/stream":             true,
		"/home/user/movie.mkv":          false,
		"":                              false,
	}
	for src, want := range cases {
		if got := isNetworkSource(src); got != want {
			t.Errorf("isNetworkSource(%q) = %v, want %v", src, got, want)
		}
	}
}

func TestPickSubtitleFilePrefersVTT(t *testing.T) {
	dir := t.TempDir()
	if got := pickSubtitleFile(dir); got != "" {
		t.Fatalf("empty dir: got %q, want empty", got)
	}
	srt := filepath.Join(dir, "subs.en.srt")
	if err := os.WriteFile(srt, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pickSubtitleFile(dir); got != srt {
		t.Fatalf("srt only: got %q, want %q", got, srt)
	}
	vtt := filepath.Join(dir, "subs.en.vtt")
	if err := os.WriteFile(vtt, []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pickSubtitleFile(dir); got != vtt {
		t.Fatalf("vtt and srt: got %q, want %q", got, vtt)
	}
}
