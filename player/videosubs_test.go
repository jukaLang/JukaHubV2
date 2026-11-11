package main

// videosubs_test.go — subtitles: what is asked of yt-dlp and YouTube, which
// file or track is taken, how YouTube's rolling captions are tidied, the files
// kept on the card (cache and sidecars), and what a press of CC does when CC
// carried over from the last video. Nothing here dials or runs a helper: the
// chain's network steps are swapped out.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// subtitlesIsolateForTests keeps the whole test binary (TestMain) away from the
// network, the helpers and the real data folder: CC now outlives a stopped
// video, so a test that played something with CC on would otherwise fetch for
// real and write the real subtitle cache. It returns the cleanup.
func subtitlesIsolateForTests() func() {
	dir, err := os.MkdirTemp("", "jukahub-subtitles")
	if err == nil {
		subtitleCacheDirOverride = dir
	}
	ytNativeCaptionsFor = func(context.Context, string, string) ([]subtitleCue, error) {
		return nil, errors.New("no network in tests")
	}
	subtitleYtdlpRunner = func(context.Context, subtitleRequest) ([]subtitleCue, string) {
		return nil, "no helper in tests"
	}
	videoSubs.mu.Lock()
	videoSubs.enabled = false
	videoSubs.mu.Unlock()
	return func() {
		if err == nil {
			os.RemoveAll(dir)
		}
	}
}

func TestSubtitleLangSelectorCoversRegionsAndTracks(t *testing.T) {
	cases := map[string]string{
		"en":      "en,en-.*",
		"pt-BR":   "pt-BR,pt-BR-.*,pt",
		"zh-Hans": "zh-Hans,zh-Hans-.*,zh",
		"fil":     "fil,fil-.*",
	}
	for lang, want := range cases {
		if got := subtitleLangSelector(lang); got != want {
			t.Errorf("subtitleLangSelector(%q) = %q, want %q", lang, got, want)
		}
	}
}

func TestSubtitleDownloadArgs(t *testing.T) {
	page := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	args := subtitleDownloadArgs("/tmp/subs-dir", page, "en", []string{"--cookies", "/c.txt"})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--skip-download", "--no-playlist", "--ignore-no-formats-error",
		"--write-subs", "--write-auto-subs", "--sub-langs en,en-.*", "--sub-format vtt/srt",
		"--extractor-args youtube:skip=translated_subs --cookies /c.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("subtitle args lack %q: %s", want, joined)
		}
	}
	n := len(args)
	if n < 4 || args[n-2] != "--" || args[n-1] != page {
		t.Fatalf("the page must come last, after --: %v", args)
	}
	if args[n-4] != "--cookies" || args[n-3] != "/c.txt" {
		t.Fatalf("the user's arguments must come just before the page: %v", args)
	}
	// Another site has no YouTube translations to skip.
	other := strings.Join(subtitleDownloadArgs("/tmp/subs-dir", "https://vimeo.example/123", "en", nil), " ")
	if strings.Contains(other, "--extractor-args") {
		t.Fatalf("a non-YouTube page got YouTube's extractor arguments: %s", other)
	}
}

func TestSubtitleLanguageAcceptsOnlyLiteralCodes(t *testing.T) {
	for in, want := range map[string]string{"": "en", "pt-BR": "pt-BR", "  de ": "de", "zh-Hans": "zh-Hans"} {
		got, err := subtitleLanguage(&Config{Variables: Variables{SubtitleLanguage: in}})
		if err != nil || got != want {
			t.Errorf("subtitleLanguage(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"-en", "en.*", "1en", "en,de", "e n", "all", "ALL"} {
		if _, err := subtitleLanguage(&Config{Variables: Variables{SubtitleLanguage: bad}}); err == nil {
			t.Errorf("subtitleLanguage(%q) accepted a selector", bad)
		}
	}
}

func TestPickSubtitleFilePrefersTheWantedTrack(t *testing.T) {
	write := func(dir string, names ...string) {
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("WEBVTT\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	pick := func(dir, lang string) string { return filepath.Base(pickSubtitleFileFor(dir, lang)) }

	// Without the auto-generated original, the exact code wins, as WebVTT.
	plain := t.TempDir()
	write(plain, "subs.en.srt", "subs.en.vtt", "subs.en-US.vtt", "subs.de.vtt")
	if got := pick(plain, "en"); got != "subs.en.vtt" {
		t.Fatalf("picked %q, want the exact code as WebVTT", got)
	}
	// With "en-orig" written, plain "en" may be the auto-generated track: a
	// written regional track comes first then.
	orig := t.TempDir()
	write(orig, "subs.de.vtt", "subs.en-US.vtt", "subs.en-orig.vtt", "subs.en.vtt")
	if got := pick(orig, "en"); got != "subs.en-US.vtt" {
		t.Fatalf("picked %q, want the written regional track over the auto-generated one", got)
	}
	// A translation of another written track ("en-de") is English, but the
	// auto-generated original beats it.
	trans := t.TempDir()
	write(trans, "subs.en-de.vtt", "subs.en-orig.vtt")
	if got := pick(trans, "en"); got != "subs.en-orig.vtt" {
		t.Fatalf("picked %q, want the auto-generated original over a translation", got)
	}
	pt := t.TempDir()
	write(pt, "subs.pt-PT.vtt", "subs.pt.vtt")
	if got := pick(pt, "pt-BR"); got != "subs.pt.vtt" {
		t.Fatalf("picked %q for pt-BR, want plain Portuguese before another region", got)
	}
	if got := pickSubtitleFileFor(t.TempDir(), "en"); got != "" {
		t.Fatalf("an empty folder gave %q", got)
	}
	for code, want := range map[string]bool{"en-de": true, "en-zh-Hans": true, "en-US": false, "en-orig": false, "en-nP7-2PuUl7o": false, "de": false} {
		if got := subtitleCodeIsTranslation(code, "en"); got != want {
			t.Errorf("subtitleCodeIsTranslation(%q) = %v, want %v", code, got, want)
		}
	}
}

func TestSubtitleFailureReasonNamesTheCause(t *testing.T) {
	cases := map[string]string{
		"ERROR: Unable to download video subtitles for 'en': HTTP Error 429: Too Many Requests": "limiting",
		"ERROR: [youtube] x: Sign in to confirm you’re not a bot":                               "not a bot",
		"ERROR: [youtube] x: Sign in to confirm your age":                                       "age-restricted",
		"ERROR: [youtube] x: Private video":                                                     "not available",
		"something else entirely":                                                               "Subtitle download failed",
	}
	for out, want := range cases {
		if got := subtitleFailureReason(out); !strings.Contains(got, want) {
			t.Errorf("subtitleFailureReason(%q) = %q, want it to mention %q", out, got, want)
		}
	}
}

func TestChooseCaptionTrack(t *testing.T) {
	tracks := []ytCaptionTrack{
		{BaseURL: "https://www.youtube.com/api/timedtext?v=x&lang=en&kind=asr", LanguageCode: "en", Kind: "asr", IsTranslatable: true},
		{BaseURL: "https://www.youtube.com/api/timedtext?v=x&lang=en-US", LanguageCode: "en-US", IsTranslatable: true},
		{BaseURL: "https://www.youtube.com/api/timedtext?v=x&lang=de", LanguageCode: "de", IsTranslatable: true},
	}
	if c, ok := ytChooseCaptionTrack(tracks, "en"); !ok || c.track.LanguageCode != "en-US" || c.tlang != "" {
		t.Fatalf("en chose %+v, want the written en-US track over the auto-generated one", c)
	}
	if c, ok := ytChooseCaptionTrack(tracks, "de"); !ok || c.track.LanguageCode != "de" || c.tlang != "" {
		t.Fatalf("de chose %+v", c)
	}
	if c, ok := ytChooseCaptionTrack(tracks, "fr"); !ok || c.track.Kind == "asr" || c.tlang != "fr" {
		t.Fatalf("fr chose %+v, want a written track translated to fr", c)
	}
	asrOnly := tracks[:1]
	if c, ok := ytChooseCaptionTrack(asrOnly, "en"); !ok || c.track.Kind != "asr" || c.tlang != "" {
		t.Fatalf("en with only the auto-generated track chose %+v", c)
	}
	if c, ok := ytChooseCaptionTrack(asrOnly, "es"); !ok || c.tlang != "es" {
		t.Fatalf("es chose %+v, want the auto-generated track translated", c)
	}
	hant := []ytCaptionTrack{{BaseURL: "https://www.youtube.com/api/timedtext?v=x", LanguageCode: "zh-Hant", IsTranslatable: true}}
	if c, ok := ytChooseCaptionTrack(hant, "zh-Hans"); !ok || c.tlang != "zh-Hans" {
		t.Fatalf("zh-Hans chose %+v, want a translation rather than the other script", c)
	}
	if _, ok := ytChooseCaptionTrack(nil, "en"); ok {
		t.Fatal("no tracks still chose one")
	}
	locked := []ytCaptionTrack{{BaseURL: "https://www.youtube.com/api/timedtext?v=x", LanguageCode: "ja"}}
	if _, ok := ytChooseCaptionTrack(locked, "en"); ok {
		t.Fatal("an untranslatable track in another language was chosen")
	}
}

func TestCaptionURL(t *testing.T) {
	got, err := ytCaptionURL("https://www.youtube.com/api/timedtext?v=abc&lang=en&fmt=srv3", "")
	if err != nil || !strings.Contains(got, "fmt=vtt") || strings.Contains(got, "srv3") || strings.Contains(got, "tlang") {
		t.Fatalf("plain track URL = %q, %v", got, err)
	}
	got, err = ytCaptionURL("https://www.youtube.com/api/timedtext?v=abc&lang=en", "fr")
	if err != nil || !strings.Contains(got, "tlang=fr") {
		t.Fatalf("translated track URL = %q, %v", got, err)
	}
	got, err = ytCaptionURL("/api/timedtext?v=abc", "")
	if err != nil || !strings.HasPrefix(got, "https://www.youtube.com/api/timedtext?") {
		t.Fatalf("relative track URL = %q, %v", got, err)
	}
	for _, bad := range []string{"https://evil.example/api/timedtext?v=x", "http://www.youtube.com/api/timedtext?v=x", "https://youtube.com.evil.example/x"} {
		if _, err := ytCaptionURL(bad, ""); err == nil {
			t.Errorf("ytCaptionURL(%q) was allowed", bad)
		}
	}
}

func TestCaptionTracksAreReadApartFromTheStream(t *testing.T) {
	p := &ytPlayerResponse{Captions: json.RawMessage(`{"playerCaptionsTracklistRenderer":{"captionTracks":[` +
		`{"baseUrl":"https://www.youtube.com/api/timedtext?v=x","languageCode":"en","kind":"asr","isTranslatable":true,` +
		`"vssId":"a.en","name":{"simpleText":"English (auto-generated)"}}]}}`)}
	tracks, err := ytCaptionTracksOf(p)
	if err != nil || len(tracks) != 1 || tracks[0].LanguageCode != "en" || tracks[0].Kind != "asr" ||
		!tracks[0].IsTranslatable || !strings.Contains(tracks[0].Name.String(), "English") {
		t.Fatalf("tracks = %+v, %v", tracks, err)
	}
	for _, empty := range []json.RawMessage{nil, json.RawMessage(`null`)} {
		p.Captions = empty
		if tracks, err := ytCaptionTracksOf(p); err != nil || len(tracks) != 0 {
			t.Fatalf("no caption list (%q) = %+v, %v", string(empty), tracks, err)
		}
	}
	p.Captions = json.RawMessage(`{"playerCaptionsTracklistRenderer":{"captionTracks":"oops"}}`)
	if _, err := ytCaptionTracksOf(p); err == nil {
		t.Fatal("a malformed caption list was read")
	}
	// The same malformed list must not make the answer unreadable for the
	// stream path.
	var whole ytPlayerResponse
	answer := `{"playabilityStatus":{"status":"OK"},"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":7}}}`
	if err := json.Unmarshal([]byte(answer), &whole); err != nil || whole.PlayabilityStatus.Status != "OK" {
		t.Fatalf("a malformed caption list broke the player answer: %v", err)
	}
}

// youTubeRollingVTT is the shape of YouTube's auto-generated WebVTT: word
// timings, the previous line repeated above the new one, and 10 ms cues.
const youTubeRollingVTT = "WEBVTT\nKind: captions\nLanguage: en\n\n" +
	"00:00:00.160 --> 00:00:02.230 align:start position:0%\n \nso<00:00:00.480><c> today</c><00:00:00.640><c> we're</c><00:00:00.880><c> going</c>\n\n" +
	"00:00:02.230 --> 00:00:02.240 align:start position:0%\nso today we're going\n \n\n" +
	"00:00:02.240 --> 00:00:04.789 align:start position:0%\nso today we're going\nto<00:00:02.480><c> talk</c><00:00:02.720><c> about</c>\n\n" +
	"00:00:04.789 --> 00:00:04.799 align:start position:0%\nto talk about\n \n\n" +
	"00:00:04.799 --> 00:00:07.110 align:start position:0%\nto talk about\nsomething<00:00:05.200><c> new</c>\n"

func TestRollingCaptionsShowEachLineOnce(t *testing.T) {
	cues := parseSubtitles(youTubeRollingVTT)
	want := []string{"so today we're going", "to talk about", "something new"}
	if len(cues) != len(want) {
		t.Fatalf("got %d cues %+v, want %d", len(cues), cues, len(want))
	}
	for i, w := range want {
		if cues[i].Text != w {
			t.Errorf("cue %d = %q, want %q", i, cues[i].Text, w)
		}
	}
	if cues[1].Start < 2.2 || cues[1].End < 4.7 {
		t.Errorf("cue timing moved: %+v", cues[1])
	}
	// The dropped hand-over cues leave no blank frame between two lines.
	for i := 0; i+1 < len(cues); i++ {
		if cues[i].End != cues[i+1].Start {
			t.Errorf("gap between cue %d (ends %.3f) and cue %d (starts %.3f)", i, cues[i].End, i+1, cues[i+1].Start)
		}
	}
}

func TestPlainSubtitlesParseAsWritten(t *testing.T) {
	// A hand-made file may repeat a line on purpose; only YouTube's word
	// timings switch the rolling-caption cleanup on, so nothing here is
	// folded or dropped, short cues included.
	srt := "1\n00:00:01,000 --> 00:00:02,000\nNo!\n\n" +
		"2\n00:00:02,000 --> 00:00:04,000\nNo!\nPlease\n\n" +
		"3\n00:00:04,000 --> 00:00:04,020\nBlink\n"
	cues := parseSubtitles(srt)
	if len(cues) != 3 || cues[1].Text != "No!\nPlease" || cues[2].Text != "Blink" {
		t.Fatalf("plain SRT changed: %+v", cues)
	}
}

func TestStyledWebVTTParsesAsWritten(t *testing.T) {
	// Class spans (<c.yellow>) colour hand-made and converted WebVTT; without
	// word timings the file is not YouTube's rolling track.
	vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<c.yellow>No!</c>\n\n" +
		"00:00:02.000 --> 00:00:04.000\n<c.yellow>No!</c>\nPlease\n\n" +
		"00:00:04.000 --> 00:00:04.020\n<c.blue>Blink</c>\n"
	cues := parseSubtitles(vtt)
	if len(cues) != 3 || cues[0].Text != "No!" || cues[1].Text != "No!\nPlease" || cues[2].Text != "Blink" {
		t.Fatalf("styled WebVTT changed: %+v", cues)
	}
}

func TestReadSubtitleCuesCapsTheSize(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "subs.en.srt")
	if err := os.WriteFile(small, []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cues, err := readSubtitleCues(small); err != nil || len(cues) != 1 {
		t.Fatalf("a normal file = %+v, %v", cues, err)
	}
	big := filepath.Join(dir, "subs.en.vtt")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(subtitleFileMax + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if _, err := readSubtitleCues(big); err == nil {
		t.Fatal("a file over the cap was read into memory")
	}
}

func TestFormatSRTRoundTrip(t *testing.T) {
	in := []subtitleCue{
		{Start: 1.25, End: 3.5, Text: "Hello"},
		{Start: 3723.45, End: 3725, Text: "Two\nlines"},
		{Start: 10, End: 12, Text: "A --> B"},
		{Start: 20, End: 19, Text: "backwards, dropped"},
	}
	text := formatSRT(in)
	if !strings.Contains(text, "01:02:03,450 --> 01:02:05,000") {
		t.Fatalf("SRT timing wrong:\n%s", text)
	}
	out := parseSubtitles(text)
	if len(out) != 3 {
		t.Fatalf("round trip gave %d cues: %+v", len(out), out)
	}
	if out[0] != in[0] || out[1] != in[1] {
		t.Fatalf("round trip changed cues: %+v", out)
	}
	if out[2].Text != "A -> B" {
		t.Fatalf("an arrow in the text must not pass for a timing: %q", out[2].Text)
	}
	if got := srtTimestamp(-4); got != "00:00:00,000" {
		t.Fatalf("negative time = %q", got)
	}
}

func TestSubtitleCacheKeysAndFiles(t *testing.T) {
	prev := subtitleCacheDirOverride
	subtitleCacheDirOverride = t.TempDir()
	t.Cleanup(func() { subtitleCacheDirOverride = prev })

	watch := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	if subtitleCacheKey(watch) != subtitleCacheKey("https://youtu.be/dQw4w9WgXcQ") {
		t.Fatal("two links of one video must share a cache file")
	}
	if strings.EqualFold(subtitleCacheKey(watch), subtitleCacheKey("https://www.youtube.com/watch?v=DQW4W9WGXCQ")) {
		t.Fatal("ids that differ only in case must not share a file on a FAT card")
	}
	if key := subtitleCacheKey("https://vimeo.example/123"); !strings.HasPrefix(key, "u-") {
		t.Fatalf("other pages are hashed, got %q", key)
	}
	if subtitleCachePath(watch, "-en") != "" || subtitleCachePath("", "en") != "" {
		t.Fatal("a bad language or an empty page must not name a cache file")
	}
	cues := []subtitleCue{{Start: 1, End: 2, Text: "Cached"}}
	subtitleCacheStore(watch, "en", cues)
	got := subtitleCacheLoad("https://youtu.be/dQw4w9WgXcQ", "en")
	if len(got) != 1 || got[0] != cues[0] {
		t.Fatalf("cache load = %+v", got)
	}
	if subtitleCacheLoad(watch, "de") != nil {
		t.Fatal("another language must miss")
	}
	entries, _ := os.ReadDir(subtitleCacheDirOverride)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("a store left its temporary file: %s", e.Name())
		}
	}
}

func TestSubtitleCachePrune(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	for i, age := range []time.Duration{2 * time.Hour, 50 * time.Minute, 40 * time.Minute, 30 * time.Minute, 20 * time.Minute} {
		write("yt-"+string(rune('a'+i))+".en.srt", age)
	}
	// A temporary file a crash left behind goes once it is an hour old; one
	// being written right now stays.
	write("yt-x.en.srt.123.tmp", 2*time.Hour)
	write("yt-y.en.srt.456.tmp", 10*time.Minute)
	if removed := subtitleCachePrune(dir, 3, time.Hour, now); removed != 3 {
		t.Fatalf("pruned %d files, want the expired one, the oldest of the rest and the stale temporary file", removed)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 4 {
		t.Fatalf("%d files left, want 3 cache files and the fresh temporary one", len(left))
	}
	for _, e := range left {
		switch e.Name() {
		case "yt-a.en.srt", "yt-b.en.srt", "yt-x.en.srt.123.tmp":
			t.Fatalf("%s should have been pruned", e.Name())
		}
	}
}

func TestSubtitleSidecarFilesIgnoreOtherVideos(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Song [Live].mp4")
	for _, name := range []string{"Song [Live].mp4", "Song [Live].en.srt", "Song [Live].srt", "song [live].en-US.VTT",
		"Song [Live].part1.srt", "Song [Live].en.en.srt", "Other.en.srt", "Song [Live] (2).en.srt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]string{}
	for _, s := range subtitleSidecarFiles(video) {
		got[filepath.Base(s.path)] = s.lang
	}
	want := map[string]string{"Song [Live].en.srt": "en", "Song [Live].srt": "", "song [live].en-US.VTT": "en-US"}
	if len(got) != len(want) {
		t.Fatalf("sidecars = %v, want %v", got, want)
	}
	for name, lang := range want {
		if l, ok := got[name]; !ok || l != lang {
			t.Fatalf("sidecars = %v, want %v", got, want)
		}
	}
	if p := subtitleSidecarPath(filepath.Join(dir, "Clip.mp4"), "pt-BR"); filepath.Base(p) != "Clip.pt-BR.srt" {
		t.Fatalf("sidecar path = %q", p)
	}
}

func TestLoadSidecarPrefersTheConfiguredLanguage(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Clip.mp4")
	files := map[string]string{"Clip.mp4": "video", "Clip.de.srt": "Hallo", "Clip.en.srt": "Hello", "Clip.srt": "Own"}
	for name, text := range files {
		body := "1\n00:00:01,000 --> 00:00:02,000\n" + text + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if cues, label := loadSidecarSubtitles(video, "en"); len(cues) != 1 || cues[0].Text != "Hello" || label != "Clip.en.srt" {
		t.Fatalf("en loaded %+v from %q", cues, label)
	}
	if cues, _ := loadSidecarSubtitles(video, "fr"); len(cues) != 1 || cues[0].Text != "Own" {
		t.Fatalf("fr loaded %+v, want the file named like the video", cues)
	}
}

func TestWriteAndRemoveSubtitleSidecars(t *testing.T) {
	prevRoot := downloadsRootOverride
	downloadsRootOverride = t.TempDir()
	t.Cleanup(func() { downloadsRootOverride = prevRoot })

	cues := []subtitleCue{{Start: 1, End: 2, Text: "Hi"}}
	video := filepath.Join(downloadsVideoDir(), "Clip.mp4")
	if _, err := writeSubtitleSidecar(video, "en", cues); err == nil {
		t.Fatal("a sidecar was written for a video that is not there")
	}
	if err := os.MkdirAll(downloadsVideoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := writeSubtitleSidecar(video, "en", cues)
	if err != nil || filepath.Base(path) != "Clip.en.srt" {
		t.Fatalf("sidecar = %q, %v", path, err)
	}
	if loaded, _ := loadSidecarSubtitles(video, "en"); len(loaded) != 1 || loaded[0] != cues[0] {
		t.Fatalf("the sidecar does not read back: %+v", loaded)
	}
	removeSubtitleSidecars(video)
	if regularFile(path) {
		t.Fatal("removing the download left its subtitles behind")
	}

	// Nothing outside the app's download folder is ever written or deleted.
	outside := t.TempDir()
	userVideo := filepath.Join(outside, "Film.mp4")
	userSubs := filepath.Join(outside, "Film.en.srt")
	if err := os.WriteFile(userSubs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeSubtitleSidecars(userVideo)
	if !regularFile(userSubs) {
		t.Fatal("a subtitle file outside the download folder was deleted")
	}
	if err := os.WriteFile(userVideo, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeSubtitleSidecar(userVideo, "de", cues); err == nil {
		t.Fatal("a sidecar was written beside a user's own file")
	}
}

// subtitleChainStubs swaps the chain's network steps for one test.
func subtitleChainStubs(t *testing.T, native func(context.Context, string, string) ([]subtitleCue, error), ytdlp func(context.Context, subtitleRequest) ([]subtitleCue, string)) {
	t.Helper()
	prevNative, prevYtdlp, prevDir := ytNativeCaptionsFor, subtitleYtdlpRunner, subtitleCacheDirOverride
	ytNativeCaptionsFor, subtitleYtdlpRunner = native, ytdlp
	subtitleCacheDirOverride = t.TempDir()
	netMon.mu.Lock()
	prevChecked, prevOnline := netMon.checked, netMon.online
	netMon.checked, netMon.online = true, true
	netMon.mu.Unlock()
	t.Cleanup(func() {
		ytNativeCaptionsFor, subtitleYtdlpRunner, subtitleCacheDirOverride = prevNative, prevYtdlp, prevDir
		netMon.mu.Lock()
		netMon.checked, netMon.online = prevChecked, prevOnline
		netMon.mu.Unlock()
	})
}

func TestFetchPageSubtitlesChain(t *testing.T) {
	page := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	nativeCues := []subtitleCue{{Start: 1, End: 2, Text: "Native"}}
	ytdlpCalls := 0
	subtitleChainStubs(t,
		func(context.Context, string, string) ([]subtitleCue, error) { return nativeCues, nil },
		func(context.Context, subtitleRequest) ([]subtitleCue, string) {
			ytdlpCalls++
			return nil, "should not run"
		})
	res := fetchPageSubtitles(context.Background(), subtitleRequest{page: page, lang: "en"})
	if !res.ok || len(res.cues) != 1 || res.cues[0].Text != "Native" || !strings.Contains(res.status, "YouTube") || ytdlpCalls != 0 {
		t.Fatalf("native fetch = %+v (yt-dlp ran %d times)", res, ytdlpCalls)
	}
	// The next play is answered by the cache, without asking YouTube again.
	ytNativeCaptionsFor = func(context.Context, string, string) ([]subtitleCue, error) {
		t.Fatal("the cache should have answered")
		return nil, nil
	}
	if res := fetchPageSubtitles(context.Background(), subtitleRequest{page: page, lang: "en"}); !res.ok || !strings.Contains(res.status, "saved") {
		t.Fatalf("cached fetch = %+v", res)
	}
}

func TestFetchPageSubtitlesFallsBackToYtdlp(t *testing.T) {
	page := "https://www.youtube.com/watch?v=aaaaaaaaaaa"
	subtitleChainStubs(t,
		func(context.Context, string, string) ([]subtitleCue, error) { return nil, os.ErrDeadlineExceeded },
		func(_ context.Context, r subtitleRequest) ([]subtitleCue, string) {
			if r.lang != "de" {
				t.Errorf("yt-dlp asked for %q", r.lang)
			}
			return []subtitleCue{{Start: 1, End: 2, Text: "Helper"}}, ""
		})
	res := fetchPageSubtitles(context.Background(), subtitleRequest{page: page, lang: "de"})
	if !res.ok || res.cues[0].Text != "Helper" || !strings.Contains(res.status, "from YouTube") || strings.Contains(res.status, "yt-dlp") {
		t.Fatalf("fallback = %+v", res)
	}

	subtitleYtdlpRunner = func(context.Context, subtitleRequest) ([]subtitleCue, string) { return nil, "" }
	res = fetchPageSubtitles(context.Background(), subtitleRequest{page: "https://www.youtube.com/watch?v=bbbbbbbbbbb", lang: "en"})
	if res.ok || !res.none || !res.final || !strings.Contains(res.reason, "No English subtitles") {
		t.Fatalf("no track anywhere = %+v", res)
	}
	res = fetchPageSubtitles(context.Background(), subtitleRequest{page: "https://cdn.example/live/stream.m3u8", lang: "en"})
	if res.ok || !res.none || !strings.Contains(res.reason, "no subtitles") {
		t.Fatalf("a bare stream = %+v", res)
	}
	subtitleYtdlpRunner = func(context.Context, subtitleRequest) ([]subtitleCue, string) {
		return nil, "YouTube is limiting subtitle downloads — try again in a few minutes"
	}
	res = fetchPageSubtitles(context.Background(), subtitleRequest{page: "https://www.youtube.com/watch?v=ccccccccccc", lang: "en"})
	if res.ok || res.none || !strings.Contains(res.reason, "limiting") {
		t.Fatalf("a helper failure is not a page without subtitles: %+v", res)
	}
}

func TestFetchPageSubtitlesTakesTheListsWordUnlessPressed(t *testing.T) {
	page := "https://www.youtube.com/watch?v=eeeeeeeeeee"
	ytdlpCalls := 0
	subtitleChainStubs(t,
		func(context.Context, string, string) ([]subtitleCue, error) {
			return nil, fmt.Errorf("ANDROID: the answer lists no caption tracks: %w", errNoCaptions)
		},
		func(context.Context, subtitleRequest) ([]subtitleCue, string) {
			ytdlpCalls++
			return nil, ""
		})
	res := fetchPageSubtitles(context.Background(), subtitleRequest{page: page, lang: "en"})
	if res.ok || !res.none || res.final || ytdlpCalls != 0 {
		t.Fatalf("a fetch nobody pressed for = %+v (yt-dlp ran %d times), want the list's word", res, ytdlpCalls)
	}
	res = fetchPageSubtitles(context.Background(), subtitleRequest{page: page, lang: "en", pressed: true})
	if res.ok || !res.none || !res.final || ytdlpCalls != 1 {
		t.Fatalf("a pressed fetch = %+v (yt-dlp ran %d times), want the helper asked too", res, ytdlpCalls)
	}
}

func TestSubtitleSaveSidecarForADownload(t *testing.T) {
	prevRoot, prevFetcher := downloadsRootOverride, subtitleFetcher
	downloadsRootOverride = t.TempDir()
	t.Cleanup(func() { downloadsRootOverride, subtitleFetcher = prevRoot, prevFetcher })
	subtitleFetcher = func(_ context.Context, r subtitleRequest) subtitleResult {
		return subtitleResult{ok: true, cues: []subtitleCue{{Start: 1, End: 2, Text: "Saved " + r.lang}}}
	}
	if err := os.MkdirAll(downloadsVideoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(downloadsVideoDir(), "Talk.mp4")
	if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := subtitleSaveSidecar(context.Background(), subtitleRequest{page: "https://www.youtube.com/watch?v=ccccccccccc", lang: "en"}, video)
	if err != nil || filepath.Base(path) != "Talk.en.srt" {
		t.Fatalf("sidecar = %q, %v", path, err)
	}
	// A second run finds the file and does not fetch again.
	subtitleFetcher = func(context.Context, subtitleRequest) subtitleResult {
		t.Fatal("fetched again although the sidecar exists")
		return subtitleResult{}
	}
	if again, err := subtitleSaveSidecar(context.Background(), subtitleRequest{page: "https://www.youtube.com/watch?v=ccccccccccc", lang: "en"}, video); err != nil || again != path {
		t.Fatalf("second run = %q, %v", again, err)
	}
}

func TestSubtitleSaveSidecarSkipsAPageKnownToHaveNone(t *testing.T) {
	prevRoot, prevFetcher := downloadsRootOverride, subtitleFetcher
	downloadsRootOverride = t.TempDir()
	videoSubs.mu.Lock()
	prevNoTrack := videoSubs.noTrack
	videoSubs.noTrack = nil
	videoSubs.mu.Unlock()
	t.Cleanup(func() {
		downloadsRootOverride, subtitleFetcher = prevRoot, prevFetcher
		videoSubs.mu.Lock()
		videoSubs.noTrack = prevNoTrack
		videoSubs.mu.Unlock()
	})
	calls := 0
	subtitleFetcher = func(context.Context, subtitleRequest) subtitleResult {
		calls++
		return subtitleResult{reason: "No English subtitles for this video", none: true}
	}
	if err := os.MkdirAll(downloadsVideoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(downloadsVideoDir(), "Quiet.mp4")
	if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := subtitleRequest{page: "https://www.youtube.com/watch?v=ddddddddddd", lang: "en"}
	for i := 0; i < 2; i++ {
		if _, err := subtitleSaveSidecar(context.Background(), r, video); err == nil {
			t.Fatal("a page without subtitles saved a sidecar")
		}
	}
	if calls != 1 {
		t.Fatalf("fetched %d times, want once: the session knows the page has none", calls)
	}
}

// ── CC presses ───────────────────────────────────────────────────────────────

// subtitleCCFixture is a fake video on screen for the CC logic, with the fetch
// chain swapped for an answer function. requests receives every request the
// chain is asked; landed ticks when a fetch's goroutine is done, toast
// included.
type subtitleCCFixture struct {
	t        *testing.T
	sess     *playbackSession
	requests chan subtitleRequest
	landed   chan struct{}
}

func newSubtitleCCFixture(t *testing.T, page string, answer func(context.Context, subtitleRequest) subtitleResult) *subtitleCCFixture {
	t.Helper()
	clearToastAfterTest(t)
	subtitleClearToast()
	f := &subtitleCCFixture{t: t, requests: make(chan subtitleRequest, 16), landed: make(chan struct{}, 16)}
	prevFetcher, prevLanded, prevDir := subtitleFetcher, subtitleFetchLanded, subtitleCacheDirOverride
	videoSubs.mu.Lock()
	prevEnabled, prevNoTrack, prevSaid := videoSubs.enabled, videoSubs.noTrack, videoSubs.autoSaid
	videoSubs.enabled, videoSubs.noTrack, videoSubs.autoSaid = false, nil, nil
	videoSubs.mu.Unlock()
	subtitleCacheDirOverride = t.TempDir()
	subtitleFetcher = func(ctx context.Context, r subtitleRequest) subtitleResult {
		f.requests <- r
		return answer(ctx, r)
	}
	subtitleFetchLanded = func() { f.landed <- struct{}{} }
	f.sess = &playbackSession{source: page, pageURL: page, local: !isNetworkSource(page)}
	resetVideoSubtitles(f.sess)
	t.Cleanup(func() {
		resetVideoSubtitles(nil)
		subtitleFetcher, subtitleFetchLanded, subtitleCacheDirOverride = prevFetcher, prevLanded, prevDir
		videoSubs.mu.Lock()
		videoSubs.enabled, videoSubs.noTrack, videoSubs.autoSaid = prevEnabled, prevNoTrack, prevSaid
		videoSubs.mu.Unlock()
	})
	return f
}

// play puts another video on screen, as the player does when one starts.
func (f *subtitleCCFixture) play(page string) {
	f.sess = &playbackSession{source: page, pageURL: page, local: !isNetworkSource(page)}
	resetVideoSubtitles(f.sess)
	startEnabledNetworkSubtitles(nil)
}

// request waits for the chain to be asked.
func (f *subtitleCCFixture) request() subtitleRequest {
	f.t.Helper()
	select {
	case r := <-f.requests:
		return r
	case <-time.After(3 * time.Second):
		f.t.Fatal("the subtitle chain was not asked")
	}
	return subtitleRequest{}
}

// noRequest fails when the chain was asked.
func (f *subtitleCCFixture) noRequest(why string) {
	f.t.Helper()
	select {
	case r := <-f.requests:
		f.t.Fatalf("%s, but the chain was asked: %+v", why, r)
	default:
	}
}

// wait waits for the running fetch to land.
func (f *subtitleCCFixture) wait() {
	f.t.Helper()
	select {
	case <-f.landed:
	case <-time.After(3 * time.Second):
		f.t.Fatal("the subtitle fetch did not land")
	}
}

func subtitleSetEnabled(on bool) {
	videoSubs.mu.Lock()
	videoSubs.enabled = on
	videoSubs.mu.Unlock()
}

func subtitleToastNow() (string, ToastKind) {
	currentToastMu.Lock()
	defer currentToastMu.Unlock()
	return currentToast.Message, currentToast.Kind
}

func subtitleClearToast() {
	currentToastMu.Lock()
	currentToast = ToastState{}
	currentToastMu.Unlock()
}

func TestCarriedOverCCFetchesOnlyYouTubeAndStaysQuiet(t *testing.T) {
	yt := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	f := newSubtitleCCFixture(t, yt, func(context.Context, subtitleRequest) subtitleResult {
		return subtitleResult{reason: "No English subtitles for this video", none: true}
	})
	subtitleSetEnabled(true)
	startEnabledNetworkSubtitles(nil)
	if r := f.request(); r.pressed || r.page != yt || r.lang != "en" {
		t.Fatalf("carried-over request = %+v, want an unpressed English fetch of the page", r)
	}
	f.wait()
	if msg, _ := subtitleToastNow(); msg != "" {
		t.Fatalf("a video without subtitles toasted %q for a fetch nobody pressed for", msg)
	}
	// The session remembers: the same video played again does not ask again.
	f.play(yt)
	f.noRequest("the page is known to have no subtitles")
	if s := subtitleStatusLine(); !strings.Contains(s, "No English subtitles") {
		t.Fatalf("status = %q", s)
	}
	// Another site, and a live channel, wait for a press.
	f.play("https://example.com/watch/42")
	f.noRequest("CC carried over to another site")
	if s := subtitleStatusLine(); !strings.Contains(s, "Press CC") {
		t.Fatalf("status on another site = %q", s)
	}
	f.play("https://cdn.example/live/channel.m3u8")
	f.noRequest("CC carried over to a live channel")
}

func TestPressAfterAQuietNoneAsksAgainThenTurnsOff(t *testing.T) {
	yt := "https://www.youtube.com/watch?v=fffffffffff"
	f := newSubtitleCCFixture(t, yt, func(_ context.Context, r subtitleRequest) subtitleResult {
		// The caption list says none; asked with a press, the helper agrees.
		return subtitleResult{reason: "No English subtitles for this video", none: true, final: r.pressed}
	})
	subtitleSetEnabled(true)
	startEnabledNetworkSubtitles(nil)
	f.request()
	f.wait()

	// CC carried over and nothing is showing: the first press asks, with the
	// helper's second opinion, and hears the answer.
	result, msg := toggleSubtitlesFor(nil)
	if result != subtitleToggleFetching || !strings.Contains(msg, "Getting English subtitles") {
		t.Fatalf("first press = %v %q, want a fetch", result, msg)
	}
	if r := f.request(); !r.pressed {
		t.Fatalf("the press asked as %+v, want pressed", r)
	}
	f.wait()
	if got, kind := subtitleToastNow(); !strings.Contains(got, "No English subtitles") || kind != ToastKindInfo {
		t.Fatalf("toast = %q (kind %v), want the answer as a notice", got, kind)
	}
	// The next press turns CC off.
	if result, _ := toggleSubtitlesFor(nil); result != subtitleToggleOff || subtitlesEnabled() {
		t.Fatalf("second press = %v, enabled %v; want CC off", result, subtitlesEnabled())
	}
	// Turned on again, the answer the session is sure of comes at once.
	result, msg = toggleSubtitlesFor(nil)
	if result != subtitleToggleNone || !strings.Contains(msg, "No English subtitles") {
		t.Fatalf("press with a known answer = %v %q", result, msg)
	}
	f.noRequest("the session is sure the page has none")
}

func TestCarriedOverFailureToastsOncePerSession(t *testing.T) {
	limited := "YouTube is limiting subtitle downloads — try again in a few minutes"
	f := newSubtitleCCFixture(t, "https://www.youtube.com/watch?v=ggggggggggg", func(context.Context, subtitleRequest) subtitleResult {
		return subtitleResult{reason: limited}
	})
	subtitleSetEnabled(true)
	startEnabledNetworkSubtitles(nil)
	f.request()
	f.wait()
	if got, kind := subtitleToastNow(); got != limited || kind != ToastKindInfo {
		t.Fatalf("toast = %q (kind %v), want the reason once, as a notice", got, kind)
	}
	subtitleClearToast()
	f.play("https://www.youtube.com/watch?v=hhhhhhhhhhh")
	f.request()
	f.wait()
	if got, _ := subtitleToastNow(); got != "" {
		t.Fatalf("the same reason toasted again on the next video: %q", got)
	}
}

func TestPressDuringACarriedOverFetchAnnouncesThenTurnsOff(t *testing.T) {
	release := make(chan struct{})
	f := newSubtitleCCFixture(t, "https://www.youtube.com/watch?v=iiiiiiiiiii", func(ctx context.Context, _ subtitleRequest) subtitleResult {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return subtitleResult{ok: true, cues: []subtitleCue{{Start: 0, End: 5, Text: "Hi"}}, status: "English · from YouTube"}
	})
	subtitleSetEnabled(true)
	startEnabledNetworkSubtitles(nil)
	f.request()
	result, msg := toggleSubtitlesFor(nil)
	if result != subtitleToggleFetching || !strings.Contains(msg, "Getting English subtitles") {
		t.Fatalf("press during the fetch = %v %q, want it announced", result, msg)
	}
	if result, _ := toggleSubtitlesFor(nil); result != subtitleToggleOff {
		t.Fatalf("second press = %v, want CC off", result)
	}
	close(release)
	f.wait()
	if got, _ := subtitleToastNow(); got != "" {
		t.Fatalf("a cancelled fetch toasted %q", got)
	}
	if subtitleTextAt(1) != "" {
		t.Fatal("a cancelled fetch installed its cues")
	}
}

func TestPressOnAStreamOrAnotherSite(t *testing.T) {
	f := newSubtitleCCFixture(t, "https://cdn.example/live/channel.m3u8", func(context.Context, subtitleRequest) subtitleResult {
		return subtitleResult{ok: true, cues: []subtitleCue{{Start: 0, End: 5, Text: "Hi"}}, status: "English · downloaded"}
	})
	result, msg := toggleSubtitlesFor(nil)
	if result != subtitleToggleNone || msg != "This stream has no subtitles" {
		t.Fatalf("press on a bare stream = %v %q", result, msg)
	}
	f.noRequest("a bare stream has no subtitle list")

	subtitleSetEnabled(false)
	f.play("https://example.com/watch/42")
	if result, _ := toggleSubtitlesFor(nil); result != subtitleToggleFetching {
		t.Fatalf("press on another site = %v, want a fetch", result)
	}
	if r := f.request(); !r.pressed {
		t.Fatalf("request = %+v, want pressed", r)
	}
	f.wait()
	if got, kind := subtitleToastNow(); got != "English subtitles on" || kind != ToastKindSuccess {
		t.Fatalf("toast = %q (kind %v)", got, kind)
	}
	if subtitleTextAt(1) != "Hi" {
		t.Fatal("the fetched cues are not showing")
	}
}

func TestPressAnswersFromTheCacheAtOnce(t *testing.T) {
	yt := "https://www.youtube.com/watch?v=jjjjjjjjjjj"
	f := newSubtitleCCFixture(t, yt, func(context.Context, subtitleRequest) subtitleResult {
		return subtitleResult{reason: "should not be asked"}
	})
	subtitleCacheStore(yt, "en", []subtitleCue{{Start: 0, End: 5, Text: "Cached"}})
	if result, _ := toggleSubtitlesFor(nil); result != subtitleToggleOn {
		t.Fatalf("press with the cache warm = %v, want the cues on at once", result)
	}
	f.noRequest("the cache had the subtitles")
	if subtitleTextAt(1) != "Cached" {
		t.Fatal("the cached cues are not showing")
	}
	if s := subtitleStatusLine(); s != "Subtitles: On (English · saved)" {
		t.Fatalf("status = %q", s)
	}
}

func TestPrepareClearsPreparingForALinkSource(t *testing.T) {
	sess := &playbackSession{pageURL: filepath.Join(t.TempDir(), "clip.mp4"), source: "https://cdn.example/clip.mp4"}
	t.Cleanup(func() { resetVideoSubtitles(nil) })
	resetVideoSubtitles(sess)
	videoSubs.mu.Lock()
	marked := videoSubs.preparing
	videoSubs.preparing = true // as a stale flag would be
	videoSubs.mu.Unlock()
	if marked {
		t.Fatal("a session whose source is a link was marked as being searched")
	}
	prepareVideoSubtitles(sess, nil, sess.source)
	videoSubs.mu.Lock()
	preparing := videoSubs.preparing
	videoSubs.mu.Unlock()
	if preparing {
		t.Fatal("the search left the session marked as being searched")
	}
}
