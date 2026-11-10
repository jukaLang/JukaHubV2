package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	recoverTestPage       = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	recoverTestSecondPage = "https://www.youtube.com/watch?v=9bZkp7q19f0"
	recoverTestStream     = "https://rr3---sn-a5mekn6d.googlevideo.com/videoplayback?expire=1762750000&itag=18&sig=AOq0QJ8w"
)

// ffmpeg 6.1 refusing a signed URL, the way it prints it at -loglevel error.
const ffmpeg61Forbidden = "[https @ 0x55d5c8f1a2c0] HTTP error 403 Forbidden\n" +
	"[in#0 @ 0x55d5c8f19f40] Error opening input: Server returned 403 Forbidden (access denied)\n" +
	"Error opening input file " + recoverTestStream + ".\n" +
	"Error opening input files: Server returned 403 Forbidden (access denied)\n"

func TestFFmpegFailureReason(t *testing.T) {
	cases := []struct {
		name, stderr, want string
	}{
		{"ffmpeg 6.1 403", ffmpeg61Forbidden, "Server returned 403 Forbidden (access denied)"},
		{"ffmpeg 6.0 403 names the input first",
			recoverTestStream + ": Server returned 403 Forbidden (access denied)\n",
			"Server returned 403 Forbidden (access denied)"},
		{"through the relay",
			"http://127.0.0.1:41234/r/0123456789abcdef0123456789abcdef/videoplayback: Server returned 403 Forbidden (access denied)\r\n",
			"Server returned 403 Forbidden (access denied)"},
		{"no picture", "[out#0/rawvideo @ 0x5566] Output file does not contain any stream\n",
			"Output file does not contain any stream"},
		{"broken off mid-stream",
			"[mov,mp4,m4a,3gp,3g2,mj2 @ 0x5566] stream 0, offset 0x1f4a3c: partial file\n" +
				"[in#0/mov,mp4,m4a,3gp,3g2,mj2 @ 0x5567] Error during demuxing: I/O error\n",
			"I/O error"},
		{"an address alone is never the reason", "Error opening input file " + recoverTestStream + ".\n", ""},
		{"nothing said", "", ""},
	}
	for _, c := range cases {
		got := ffmpegFailureReason(c.stderr)
		if got != c.want {
			t.Errorf("%s: ffmpegFailureReason = %q, want %q", c.name, got, c.want)
		}
		if strings.Contains(got, "googlevideo") || strings.Contains(got, "://") {
			t.Errorf("%s: the reason leaks an address: %q", c.name, got)
		}
	}
}

func TestFFmpegStartReasonDropsThePath(t *testing.T) {
	err := errors.New("fork/exec /mnt/SDCARD/Apps/JukaHub/tools/ffmpeg: exec format error")
	if got := ffmpegStartReason(err); got != "exec format error" {
		t.Errorf("ffmpegStartReason = %q, want %q", got, "exec format error")
	}
	if got := ffmpegStartReason(nil); got != "" {
		t.Errorf("ffmpegStartReason(nil) = %q, want empty", got)
	}
}

func TestMediaPageStream(t *testing.T) {
	cases := []struct {
		name, source, page string
		want               bool
	}{
		{"YouTube stream from its watch page", recoverTestStream, recoverTestPage, true},
		{"HLS from a page", "https://manifest.googlevideo.com/api/manifest/hls_playlist/id/x/file/index.m3u8", recoverTestPage, true},
		{"a direct link played as itself", "https://example.com/clip.mp4", "https://example.com/clip.mp4", false},
		{"a page that is itself a media link", "https://cdn.example.com/a.mp4", "https://example.com/b.m3u8", false},
		{"a file on the card", "/mnt/SDCARD/Videos/clip.mp4", "/mnt/SDCARD/Videos/clip.mp4", false},
		{"a download of a page", "/mnt/SDCARD/Apps/JukaHub/video/jukahub-video-1.mp4", recoverTestPage, false},
		{"an rtsp camera", "rtsp://10.0.0.5/live", "rtsp://10.0.0.5/live", false},
		{"no page", recoverTestStream, "", false},
	}
	for _, c := range cases {
		if got := mediaPageStream(c.source, c.page); got != c.want {
			t.Errorf("%s: mediaPageStream = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMediaIsHLS(t *testing.T) {
	for _, u := range []string{
		"https://manifest.googlevideo.com/api/manifest/hls_playlist/expire/1/id/x/file/index.m3u8",
		"https://manifest.googlevideo.com/api/manifest/hls_variant/expire/1/id/x",
		"https://example.com/live/stream.m3u8?token=abc",
		"HTTPS://EXAMPLE.COM/RADIO.M3U",
	} {
		if !mediaIsHLS(u) {
			t.Errorf("mediaIsHLS(%q) = false, want true", u)
		}
	}
	for _, u := range []string{recoverTestStream, "https://example.com/clip.mp4", "/mnt/SDCARD/a.m3u8.mp4", ""} {
		if mediaIsHLS(u) {
			t.Errorf("mediaIsHLS(%q) = true, want false", u)
		}
	}
}

func TestDecodersAskAsTheBrowserTheStreamWasResolvedFor(t *testing.T) {
	opts := ffmpegStreamInputArgs(recoverTestStream, recoverTestPage)
	want := []string{"-user_agent", streamUserAgent, "-reconnect", "1", "-reconnect_delay_max", "4"}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("ffmpegStreamInputArgs = %q, want %q", opts, want)
	}
	for _, direct := range []string{"https://example.com/live.m3u8", "rtsp://10.0.0.5/live", "udp://239.0.0.1:1234"} {
		if opts := ffmpegStreamInputArgs(direct, direct); opts != nil {
			t.Errorf("%s keeps ffmpeg's defaults (an rtsp/udp input would refuse http options), got %q", direct, opts)
		}
	}

	args := ffmpegDecoderArgs("http://127.0.0.1:41234/r/token/videoplayback", opts, 640, 360, false, 12.5)
	in := recoverTestArgIndex(args, "-i")
	for _, opt := range []string{"-user_agent", "-reconnect", "-reconnect_delay_max"} {
		if at := recoverTestArgIndex(args, opt); at < 0 || in < 0 || at > in {
			t.Fatalf("%s must come before -i: %q", opt, args)
		}
	}
	if args[recoverTestArgIndex(args, "-user_agent")+1] != streamUserAgent || args[in+1] != "http://127.0.0.1:41234/r/token/videoplayback" {
		t.Errorf("unexpected decoder arguments: %q", args)
	}
	if ss := recoverTestArgIndex(args, "-ss"); ss < 0 || ss > in {
		t.Errorf("the start position must stay an input option: %q", args)
	}
	plain := ffmpegDecoderArgs("/mnt/SDCARD/clip.mp4", nil, 640, 360, false, 0)
	if recoverTestArgIndex(plain, "-user_agent") >= 0 || recoverTestArgIndex(plain, "-reconnect") >= 0 {
		t.Errorf("a file gets no http options: %q", plain)
	}
}

// recoverTestArgIndex is the position of s in a command line, or -1.
func recoverTestArgIndex(args []string, s string) int {
	for i, a := range args {
		if a == s {
			return i
		}
	}
	return -1
}

func TestMediaDecoderFailed(t *testing.T) {
	const broke = "[mov,mp4,m4a,3gp,3g2,mj2 @ 0x1] stream 0, offset 0x1: partial file\n"
	cases := []struct {
		name                         string
		startSec, position, duration float64
		frames                       int
		pageStream                   bool
		stderr                       string
		want                         bool
	}{
		{"never opened", 0, 0, 212, 0, true, ffmpeg61Forbidden, true},
		{"never opened, said nothing", 0, 0, 212, 0, false, "", true},
		{"never opened, length unknown", 0, 0, 0, 0, true, "", true},
		{"a seek to the last second is the end", 211, 211, 212, 0, true, "", false},
		{"broke off early", 0, 40, 212, 1200, true, broke, true},
		{"ran to the end", 0, 211.9, 212, 6357, true, "", false},
		{"decode glitches are not a broken stream", 0, 40, 212, 1200, true,
			"[h264 @ 0x1] error while decoding MB 23 11, bytestream -5\n", false},
		{"too close to the end to fetch it again", 0, 195, 212, 5850, true, broke, false},
		{"a direct link keeps its end-of-video path", 0, 40, 212, 1200, false, broke, false},
		{"a live stream has no length to fall short of", 0, 40, 0, 1200, true, broke, false},
	}
	for _, c := range cases {
		got := mediaDecoderFailed(c.startSec, c.position, c.duration, c.frames, c.pageStream, c.stderr)
		if got != c.want {
			t.Errorf("%s: mediaDecoderFailed = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMediaFailureToast(t *testing.T) {
	cases := []struct {
		name string
		f    mediaStreamFailure
		want string
	}{
		{"a refused stream", mediaStreamFailure{reason: "Server returned 403 Forbidden (access denied)"},
			"Couldn't play this stream — the server refused it (HTTP 403)"},
		{"a file with no picture", mediaStreamFailure{local: true, reason: "Output file does not contain any stream"},
			"Couldn't play this video — it has no picture"},
		{"a download that will not decode", mediaStreamFailure{tempFile: true, reason: "Invalid data found when processing input"},
			"Couldn't play this video — ffmpeg cannot read this format"},
		{"no ffmpeg", mediaStreamFailure{startErr: true, reason: "exec format error"},
			"The video player could not start ffmpeg — exec format error"},
		{"broke off", mediaStreamFailure{frames: 900, reason: "I/O error"}, "Playback stopped — I/O error"},
		{"nothing said", mediaStreamFailure{}, "Couldn't play this stream"},
	}
	for _, c := range cases {
		if got := mediaFailureToast(c.f); got != c.want {
			t.Errorf("%s: mediaFailureToast = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMediaStreamFailureResumeAt(t *testing.T) {
	if got := (mediaStreamFailure{startSec: 95}).resumeAt(); got != 95 {
		t.Errorf("a stream that never opened resumes where it was asked to start, got %v", got)
	}
	if got := (mediaStreamFailure{startSec: 10, frames: 1500, position: 60}).resumeAt(); got != 57 {
		t.Errorf("a stream that broke off resumes a little before the break, got %v", got)
	}
	if got := (mediaStreamFailure{startSec: 10, frames: 30, position: 11}).resumeAt(); got != 10 {
		t.Errorf("the resume never goes back before the start, got %v", got)
	}
}

func TestMediaStreamSkipMemory(t *testing.T) {
	mediaStreamHealthReset()
	t.Cleanup(mediaStreamHealthReset)
	now := time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC)
	other := "https://www.youtube.com/watch?v=9bZkp7q19f0"

	if mediaStreamSkipped(recoverTestPage, now) {
		t.Fatal("nothing has failed yet")
	}
	if mediaNoteStreamFailed(recoverTestPage, now) {
		t.Error("one failure must not turn streaming off everywhere")
	}
	if !mediaStreamSkipped(recoverTestPage, now.Add(time.Minute)) {
		t.Error("the page whose stream failed downloads first")
	}
	if mediaStreamSkipped(other, now.Add(time.Minute)) {
		t.Error("one failure is about one video")
	}
	if mediaStreamSkipped(recoverTestPage, now.Add(mediaSkipStreamFor+time.Second)) {
		t.Error("the page's skip ends")
	}

	// A YouTube stream that opened in between starts the run again...
	mediaStreamHealthReset()
	mediaNoteStreamFailed(recoverTestPage, now)
	mediaNoteStreamOpened(other)
	if mediaNoteStreamFailed("https://www.youtube.com/watch?v=jNQXAC9IVRw", now) {
		t.Error("a YouTube stream that opened in between breaks the run")
	}
	// ...another site's stream does not speak for YouTube...
	mediaStreamHealthReset()
	mediaNoteStreamFailed(recoverTestPage, now)
	mediaNoteStreamOpened("https://vimeo.com/76979871")
	if !mediaNoteStreamFailed(other, now) {
		t.Error("another site's stream opening must not break YouTube's run")
	}
	// ...and the same video failing again is one bad video, not two.
	mediaStreamHealthReset()
	mediaNoteStreamFailed(recoverTestPage, now)
	if mediaNoteStreamFailed(recoverTestPage, now.Add(time.Minute)) {
		t.Error("one video failing twice must not turn YouTube streaming off")
	}

	// Other sites' failures never add up to YouTube's.
	mediaStreamHealthReset()
	vimeo, channel := "https://vimeo.com/76979871", "http://iptv.example.com:8080/user/pass/123"
	if mediaNoteStreamFailed(vimeo, now) || mediaNoteStreamFailed(channel, now) {
		t.Error("two failures on other sites must not turn YouTube streaming off")
	}
	if !mediaStreamSkipped(vimeo, now.Add(time.Minute)) || mediaStreamSkipped(other, now.Add(time.Minute)) {
		t.Error("another site's failure skips that page only")
	}

	// Two YouTube streams in a row that never opened: every YouTube video
	// downloads first, and nothing else does.
	mediaStreamHealthReset()
	mediaNoteStreamFailed(recoverTestPage, now)
	if !mediaNoteStreamFailed(other, now) {
		t.Fatal("the second YouTube failure in a row turns YouTube streaming off for a while")
	}
	third := "https://youtu.be/jNQXAC9IVRw"
	if !mediaStreamSkipped(third, now.Add(time.Minute)) {
		t.Error("every YouTube video downloads first while streaming is off")
	}
	if mediaStreamSkipped(vimeo, now.Add(time.Minute)) {
		t.Error("another site keeps streaming")
	}
	if mediaStreamSkipped("https://example.com/live.m3u8", now.Add(time.Minute)) {
		t.Error("a direct link has no download to go to and is never skipped")
	}
	if mediaStreamSkipped("/mnt/SDCARD/Videos/clip.mp4", now.Add(time.Minute)) {
		t.Error("a file on the card is never skipped")
	}
	if mediaStreamSkipped(third, now.Add(mediaSkipStreamFor+time.Second)) {
		t.Error("streaming is tried again after the pause")
	}
}

func TestMediaClaimRecoveryGuardsALoop(t *testing.T) {
	mediaStreamHealthReset()
	t.Cleanup(mediaStreamHealthReset)
	now := time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC)
	if !mediaClaimRecovery(recoverTestPage, now) {
		t.Fatal("the first fallback for a page goes ahead")
	}
	if mediaClaimRecovery(recoverTestPage, now.Add(30*time.Second)) {
		t.Error("a page does not fall back twice in a short while")
	}
	if !mediaClaimRecovery("https://www.youtube.com/watch?v=9bZkp7q19f0", now.Add(30*time.Second)) {
		t.Error("the guard is per page")
	}
	if !mediaClaimRecovery(recoverTestPage, now.Add(mediaRecoverCooldown+time.Second)) {
		t.Error("the guard ends")
	}
}

func TestMediaRecoverStreamDownloadsAWebVideo(t *testing.T) {
	mediaStreamHealthReset()
	clearToastAfterTest(t)
	type call struct {
		page   string
		resume float64
	}
	var calls []call
	prev := mediaDownloadInstead
	mediaDownloadInstead = func(config *Config, page string, resume float64) {
		calls = append(calls, call{page, resume})
	}
	prevURL := currentPlaybackURL
	// The recovery stands aside for a video that is playing or loading; this
	// test starts from an idle player whatever an earlier test left behind.
	playbackMu.Lock()
	prevSession := playbackCurrent
	playbackCurrent = nil
	playbackMu.Unlock()
	videoPrepareMu.Lock()
	prevPrepare := videoPrepare
	videoPrepare = videoPrepareState{seq: prevPrepare.seq}
	videoPrepareMu.Unlock()
	prevOrigin := mediaPlayOriginName()
	t.Cleanup(func() {
		mediaDownloadInstead = prev
		currentPlaybackURL = prevURL
		mediaRestorePlayOrigin(prevOrigin)
		playbackMu.Lock()
		playbackCurrent = prevSession
		playbackMu.Unlock()
		videoPrepareMu.Lock()
		videoPrepare = prevPrepare
		videoPrepareMu.Unlock()
		mediaStreamHealthReset()
		forgetDirectStream(recoverTestPage)
		ytStreamFailures.mu.Lock()
		delete(ytStreamFailures.m, recoverTestPage)
		delete(ytStreamFailures.m, recoverTestSecondPage)
		ytStreamFailures.mu.Unlock()
	})
	config := &Config{}

	rememberDirectStream(recoverTestPage, recoverTestStream, 720)
	mediaRestorePlayOrigin("")
	mediaRecoverStream(config, &mediaStreamFailure{
		source:   recoverTestStream,
		pageURL:  recoverTestPage,
		startSec: 42,
		reason:   ffmpegFailureReason(ffmpeg61Forbidden),
		origin:   mediaHubSceneName,
	})
	if len(calls) != 1 || calls[0].page != recoverTestPage || calls[0].resume != 42 {
		t.Fatalf("a web video whose stream would not open is downloaded from where it was started, got %+v", calls)
	}
	if got := mediaPlayOriginName(); got != mediaHubSceneName {
		t.Errorf("the download keeps the scene the video was started from, got %q", got)
	}
	if _, ok := cachedDirectStream(recoverTestPage); ok {
		t.Error("the stream URL that failed is not reused")
	}
	if !strings.Contains(streamResolveFailure(recoverTestPage), "403 Forbidden") {
		t.Error("the refusal is kept for the message a failed download shows")
	}
	if !mediaStreamSkipped(recoverTestPage, time.Now()) {
		t.Error("the next play of this video goes straight to the download")
	}

	// The same page again at once is not downloaded a second time.
	mediaRecoverStream(config, &mediaStreamFailure{source: recoverTestStream, pageURL: recoverTestPage})
	if len(calls) != 1 {
		t.Errorf("a page falls back once in a short while, got %d downloads", len(calls))
	}

	// Files, downloads, direct links and a missing ffmpeg say why instead.
	for _, f := range []mediaStreamFailure{
		{source: "/mnt/SDCARD/Videos/a.mp4", pageURL: "/mnt/SDCARD/Videos/a.mp4", local: true},
		{source: "/mnt/SDCARD/Apps/JukaHub/video/jukahub-video-1.mp4", pageURL: "https://www.youtube.com/watch?v=9bZkp7q19f0", tempFile: true},
		{source: "https://example.com/live.m3u8", pageURL: "https://example.com/live.m3u8"},
		{source: recoverTestStream, pageURL: "https://www.youtube.com/watch?v=9bZkp7q19f0", startErr: true},
		{source: recoverTestStream, pageURL: "https://www.youtube.com/watch?v=9bZkp7q19f0", unplayable: true},
		// A live video: its playlist has no length and no file behind it.
		{source: "https://manifest.googlevideo.com/api/manifest/hls_playlist/expire/1/id/x/file/index.m3u8",
			pageURL: "https://www.youtube.com/watch?v=jfKfPfyJRdk"},
	} {
		mediaRecoverStream(config, &f)
	}
	if len(calls) != 1 {
		t.Errorf("only a web video's stream falls back to a download, got %d downloads", len(calls))
	}
	mediaRecoverStream(config, nil) // no failure: nothing to do

	mediaStreamHealthReset()
	// Another site's stream with no length is live (an IPTV channel behind an
	// address without an extension): no download, and no skip either.
	channel := "http://iptv.example.com:8080/user/pass/123"
	mediaRecoverStream(config, &mediaStreamFailure{source: "http://iptv.example.com:8080/live/123.ts?token=x", pageURL: channel})
	if len(calls) != 1 || mediaStreamSkipped(channel, time.Now()) {
		t.Errorf("a live channel is neither downloaded nor skipped, got %d downloads", len(calls))
	}

	// A stream that cut out while it floated over another screen only says so.
	broke := mediaStreamFailure{
		source:   recoverTestStream,
		pageURL:  recoverTestSecondPage,
		duration: 212,
		frames:   1500,
		position: 60,
		reason:   "I/O error",
		floating: true,
	}
	mediaRecoverStream(config, &broke)
	if len(calls) != 1 {
		t.Errorf("a floating player is not taken over by a download, got %d downloads", len(calls))
	}
	// On screen, it carries on from a little before the break, and the next
	// play still tries to stream: the stream did open.
	broke.floating = false
	mediaRecoverStream(config, &broke)
	if len(calls) != 2 || calls[1].page != recoverTestSecondPage || calls[1].resume != 57 {
		t.Fatalf("a stream that cut out on screen is downloaded from where it stopped, got %+v", calls)
	}
	if mediaStreamSkipped(recoverTestSecondPage, time.Now()) {
		t.Error("a stream that played before it broke off is not skipped next time")
	}
	if !strings.Contains(streamResolveFailure(recoverTestSecondPage), "broke off: I/O error") {
		t.Errorf("the note says the stream broke off, got %q", streamResolveFailure(recoverTestSecondPage))
	}
}
