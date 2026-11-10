package main

import (
	"strings"
	"testing"
	"time"
)

// The causes are read from the text real failures leave: yt-dlp's stderr and
// Go transport errors, quoted from the field.
func TestClassifyYouTubeFailure(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		clockWrong bool
		want       ytFailure
	}{
		{"empty", "", false, ytFailUnknown},
		{"bot check", "ERROR: [youtube] dQw4w9WgXcQ: Sign in to confirm you’re not a bot. Use --cookies-from-browser", false, ytFailBotCheck},
		{"login required", "native client ANDROID stood down: LOGIN_REQUIRED", false, ytFailBotCheck},
		{"age gate is the video, not a bot check", "ERROR: [youtube] abc: Sign in to confirm your age. This video may be inappropriate for some users.", false, ytFailUnavailable},
		{"private", "ERROR: [youtube] abc: Private video. Sign in if you've been granted access to this video", false, ytFailUnavailable},
		{"403", "ERROR: unable to download video data: HTTP Error 403: Forbidden", false, ytFailForbidden},
		{"cdn 403", "the video CDN answered HTTP 403", false, ytFailForbidden},
		{"429", "ERROR: [youtube] abc: HTTP Error 429: Too Many Requests", false, ytFailRateLimited},
		{"no format", "ERROR: [youtube] 2Mar9m0DWOI: Requested format is not available. Use --list-formats", false, ytFailNoFormat},
		{"two urls", "android client: got 2 media URLs, want one muxed stream ()", false, ytFailNoFormat},
		{"outdated", "ERROR: [youtube] abc: Unable to extract initial player response; please report this issue on https://github.com/yt-dlp/yt-dlp/issues?q= , filling out the appropriate issue template. Confirm you are on the latest version using  yt-dlp -U", false, ytFailOutdated},
		{"no js runtime", "WARNING: [youtube] No supported JavaScript runtime could be found.", false, ytFailOutdated},
		{"dns", `Get "https://www.youtube.com/": dial tcp: lookup www.youtube.com: no such host`, false, ytFailOffline},
		{"resolver", "[Errno -3] Temporary failure in name resolution", false, ytFailOffline},
		{"timeout", "context deadline exceeded (Client.Timeout exceeded while awaiting headers)", false, ytFailTimeout},
		{"clock", "x509: certificate has expired or is not yet valid: current time 1970-01-01T00:02:11Z is before 2026-09-01T00:00:00Z", false, ytFailClock},
		{"clock from verdict", "tls: failed to verify certificate: x509: certificate signed by unknown authority", true, ytFailClock},
		{"no ca store", "tls: failed to verify certificate: x509: certificate signed by unknown authority", false, ytFailTrust},
		{"unpack", "[PYI-1234:ERROR] Failed to extract _MEIxyz: decompression resulted in return code -1", false, ytFailUnpack},
		{"old bootloader unpack", `Failed to extract C:\Users\me\AppData\Local\Temp\_MEI1234\python312.dll: failed to write data chunk`, false, ytFailUnpack},
		{"python library needs a newer c library", "[PYI-77:ERROR] Failed to load Python shared library '/tmp/_MEI1/libpython3.13.so.1.0': /lib/libc.so.6: version `GLIBC_2.38' not found", false, ytFailHelperBroken},
		{"private, player endpoint wording", "native client ANDROID refused: LOGIN_REQUIRED This video is private", false, ytFailUnavailable},
		// yt-dlp's own "Failed to extract" is YouTube changing something, which
		// an update fixes — not the bootloader, whose lines carry no
		// "ERROR: [site]" prefix.
		{"player response", "ERROR: [youtube] dQw4w9WgXcQ: Failed to extract any player response; please report this issue on  https://github.com/yt-dlp/yt-dlp/issues?q= , filling out the appropriate issue template. Confirm you are on the latest version using  yt-dlp -U", false, ytFailOutdated},
		{"other", "something nobody planned for", false, ytFailUnknown},
	}
	for _, c := range cases {
		if got := classifyYouTubeFailureAt(c.text, c.clockWrong); got != c.want {
			t.Errorf("%s: classify(%q) = %v, want %v", c.name, c.text, got, c.want)
		}
	}
}

// Only the causes an extractor update can fix start one.
func TestYouTubeFailureWantsHelperUpdate(t *testing.T) {
	updates := []ytFailure{ytFailForbidden, ytFailNoFormat, ytFailOutdated, ytFailBotCheck}
	others := []ytFailure{ytFailOffline, ytFailTimeout, ytFailClock, ytFailTrust,
		ytFailRateLimited, ytFailUnavailable, ytFailUnpack, ytFailHelperBroken, ytFailUnknown}
	for _, kind := range updates {
		if !kind.wantsHelperUpdate() {
			t.Errorf("%v should start a yt-dlp update", kind)
		}
	}
	for _, kind := range others {
		if kind.wantsHelperUpdate() {
			t.Errorf("%v must not start a yt-dlp update", kind)
		}
	}
}

func TestHelperUnpackFailure(t *testing.T) {
	if !helperUnpackFailure("[PYI-55:ERROR] Failed to extract libz.so: decompression resulted in return code -1") {
		t.Error("the bootloader's own failure must read as an unpack failure")
	}
	if helperUnpackFailure("ERROR: [youtube] abcdefghijk: Failed to extract any player response; please report this issue") {
		t.Error("yt-dlp's extractor error must not read as an unpack failure")
	}
	if helperUnpackFailure("WARNING: [youtube] Failed to extract n function\nERROR: [youtube] abc: Requested format is not available") {
		t.Error("an extractor warning must not read as an unpack failure")
	}
	if helperUnpackFailure("[PYI-77:ERROR] Failed to load Python shared library '/tmp/_MEI1/libpython3.13.so.1.0': version `GLIBC_2.38' not found") {
		t.Error("a C library too old for the release is not an unpack failure")
	}
}

func TestYouTubeFailureMessage(t *testing.T) {
	// A failure only starts a check; the message must not promise an update.
	if got := youTubeFailureMessage(ytFailForbidden, "", ytUpdateStarted); !strings.Contains(got, "403") ||
		!strings.Contains(got, "checking for a newer yt-dlp") || strings.Contains(got, "updating") {
		t.Errorf("403 while checking: %q", got)
	}
	if got := youTubeFailureMessage(ytFailNoFormat, "", ytUpdateCurrent); !strings.Contains(got, "up to date") {
		t.Errorf("no format with a current helper: %q", got)
	}
	if got := youTubeFailureMessage(ytFailOutdated, "", ytUpdateUnmanaged); !strings.Contains(got, "update your yt-dlp") {
		t.Errorf("outdated, user-managed helper: %q", got)
	}
	if got := youTubeFailureMessage(ytFailClock, "", ytUpdateNone); !strings.Contains(got, "Date & time") {
		t.Errorf("clock: %q", got)
	}
	got := youTubeFailureMessage(ytFailUnavailable, "ERROR: [youtube] abc123def45: Private video. Sign in if you've been granted access", ytUpdateNone)
	if got != "This video can't play here: Private video" {
		t.Errorf("unavailable: %q", got)
	}
	if got := youTubeFailureMessage(ytFailUnknown, "", ytUpdateNone); got == "" {
		t.Error("an unknown failure must still say something")
	}
}

// A link to another site is never blamed on YouTube, and a failed search is
// never called a refused video stream.
func TestYouTubeFailureMessageSubjects(t *testing.T) {
	for _, kind := range []ytFailure{ytFailForbidden, ytFailNoFormat, ytFailOutdated, ytFailBotCheck,
		ytFailTimeout, ytFailRateLimited, ytFailUnknown} {
		if got := youTubeFailureMessageFor(ytSubjectLink, kind, "", ytUpdateNone); strings.Contains(got, "YouTube") {
			t.Errorf("link, %v: %q names YouTube", kind, got)
		}
	}
	got := youTubeFailureMessageFor(ytSubjectSearch, ytFailForbidden, "", ytUpdateNone)
	if !strings.Contains(got, "search") || strings.Contains(got, "video stream") {
		t.Errorf("search 403: %q", got)
	}
	if got := youTubeFailureMessageFor(ytSubjectVideo, ytFailForbidden, "", ytUpdateNone); !strings.Contains(got, "YouTube refused the video stream") {
		t.Errorf("video 403: %q", got)
	}
}

// A search re-runs after an update only while its failure is recent and on
// screen, nothing plays, and it would not take the screen from the user.
func TestYtRetryAllowed(t *testing.T) {
	now := time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC)
	recent := ytFailureNote{kind: ytFailForbidden, message: "refused", at: now.Add(-time.Minute)}
	old := ytFailureNote{kind: ytFailForbidden, message: "refused", at: now.Add(-10 * time.Minute)}
	if !ytRetryAllowed(recent, true, now, true, false) {
		t.Error("a recent failure with an idle player should retry")
	}
	if ytRetryAllowed(recent, false, now, true, false) {
		t.Error("a list that no longer shows the failure must not retry")
	}
	if ytRetryAllowed(old, true, now, true, false) {
		t.Error("a failure older than the window must not retry")
	}
	if ytRetryAllowed(recent, true, now, false, false) {
		t.Error("a retry must not start beside a playing video")
	}
	if ytRetryAllowed(recent, true, now, true, true) {
		t.Error("a retry must not pull the user onto the Results tab")
	}
}

// A refusal about the video ends the native attempt; the bot check and an
// answer with no muxed format do not.
func TestYtVideoRefused(t *testing.T) {
	answer := func(status, reason string) *ytPlayerResponse {
		p := &ytPlayerResponse{}
		p.PlayabilityStatus.Status = status
		p.PlayabilityStatus.Reason = reason
		return p
	}
	if !ytVideoRefused(answer("UNPLAYABLE", "This video is private")) {
		t.Error("a private video is a refusal about the video")
	}
	if !ytVideoRefused(answer("LOGIN_REQUIRED", "Sign in to confirm your age")) {
		t.Error("an age gate is a refusal about the video")
	}
	if ytVideoRefused(answer("LOGIN_REQUIRED", "Sign in to confirm you’re not a bot")) {
		t.Error("the bot check is about the client")
	}
	if ytVideoRefused(answer("OK", "")) || ytVideoRefused(nil) {
		t.Error("an OK answer (with or without a muxed format) is not a refusal")
	}
	if ytIsBotCheck("LOGIN_REQUIRED", "Sign in to confirm your age. This video may be inappropriate for some users.") {
		t.Error("an age gate must not bench the client as a bot check")
	}
	if ytIsBotCheck("LOGIN_REQUIRED", "This video is private") {
		t.Error("a private video must not bench the client as a bot check")
	}
	if !ytVideoRefused(answer("LOGIN_REQUIRED", "This video is private")) {
		t.Error("a private video answered with LOGIN_REQUIRED is a refusal about the video")
	}
}

func TestCleanYtDlpError(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ERROR: [youtube] dQw4w9WgXcQ: Video unavailable. This video has been removed", "Video unavailable"},
		{"WARNING: something\nERROR: [youtube:search] cats: HTTP Error 429: Too Many Requests", "HTTP Error 429: Too Many Requests"},
		{"plain line", "plain line"},
		{"", ""},
	}
	for _, c := range cases {
		if got := cleanYtDlpError(c.in); got != c.want {
			t.Errorf("cleanYtDlpError(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestYtWithAPIKeyNote(t *testing.T) {
	if got := ytWithAPIKeyNote("YouTube did not answer"); got != "YouTube did not answer. "+ytNoAPIKeyNote {
		t.Errorf("got %q", got)
	}
	if got := ytWithAPIKeyNote("Try again in a moment."); got != "Try again in a moment. "+ytNoAPIKeyNote {
		t.Errorf("got %q", got)
	}
	if got := ytWithAPIKeyNote(""); got != ytNoAPIKeyNote {
		t.Errorf("got %q", got)
	}
}

// Each results variable keeps its own reason, and an answer clears it.
func TestYouTubeFailureNotes(t *testing.T) {
	noteYouTubeFailure("test_trending", ytFailForbidden, "refused")
	note, ok := youTubeFailureFor("test_trending")
	if !ok || note.kind != ytFailForbidden || note.message != "refused" {
		t.Fatalf("note = %+v, %v", note, ok)
	}
	if _, ok := youTubeFailureFor("test_other"); ok {
		t.Error("a variable with no failure reported one")
	}
	clearYouTubeFailure("test_trending")
	if _, ok := youTubeFailureFor("test_trending"); ok {
		t.Error("the note survived a clear")
	}
}

func TestStreamResolveFailureNotes(t *testing.T) {
	src := "https://www.youtube.com/watch?v=abcdefghijk"
	noteStreamResolveFailure(src, "android client: HTTP Error 403: Forbidden")
	if got := streamResolveFailure(src); !strings.Contains(got, "403") {
		t.Errorf("streamResolveFailure = %q", got)
	}
	// The stream attempts' 403 explains the download's missing format, and it
	// is the cause the combined text names.
	if got := classifyYouTubeFailureAt("ERROR: Requested format is not available\n"+streamResolveFailure(src), false); got != ytFailForbidden {
		t.Errorf("combined text classified as %v, want %v", got, ytFailForbidden)
	}
}
