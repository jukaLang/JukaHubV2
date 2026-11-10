package main

// ytfailure.go — saying why YouTube did not work, and what happens next.
//
// Every failure on the YouTube paths used to reach the screen as one of three
// lines: the first line of yt-dlp's stderr ("ERROR: [youtube] dQw4w9WgXcQ:
// Sign in to confirm you're not a bot. Use --cookies-from-browser…"), "Download
// failed. Check network.", or "The service did not answer". None of them says
// what to do, and the second blames the Wi-Fi for what is almost always
// YouTube changing something under a helper that was installed once and never
// updated. A user looking at an empty grid then guesses at the one thing the
// app has never needed: a YouTube API key.
//
// classifyYouTubeFailure reads whatever text a failure left behind — yt-dlp's
// stderr, a Go transport error, a refusal from the native client — and names
// the cause. youTubeFailureMessage turns the cause into one short line that
// says what the app is doing about it (updating yt-dlp, waiting out a rate
// limit) or what the user can do (connect Wi-Fi, fix the clock). The causes
// that an extractor update can fix are the ones that start one
// (ytFailure.wantsHelperUpdate, see ytdlp_update.go).

import (
	"strings"
	"sync"
	"time"
)

// ytFailure is the cause of one failed YouTube operation.
type ytFailure int

const (
	ytFailUnknown      ytFailure = iota
	ytFailOffline                // no route, no DNS: the device is not online
	ytFailTimeout                // the network is up but the answer never came
	ytFailClock                  // certificates look invalid because the clock is wrong
	ytFailTrust                  // the firmware has no certificate store
	ytFailBotCheck               // "Sign in to confirm you're not a bot"
	ytFailRateLimited            // HTTP 429
	ytFailForbidden              // HTTP 403 from the player or the video CDN
	ytFailNoFormat               // no format the player can stream was offered
	ytFailOutdated               // the extractor no longer understands YouTube
	ytFailUnavailable            // private, removed, age-gated or region-blocked
	ytFailUnpack                 // the one-file helper could not unpack itself
	ytFailHelperBroken           // the helper cannot run on this system (C library too old, wrong CPU)
)

// ytNoAPIKeyNote is said wherever a YouTube failure is shown at length. The
// report this file answers ended "maybe it's just me not having my own API
// key": nothing in JukaHub needs one, and the error state is exactly where
// someone wonders.
const ytNoAPIKeyNote = "No API key is needed."

// ytWithAPIKeyNote ends a failure line with ytNoAPIKeyNote, as its own
// sentence.
func ytWithAPIKeyNote(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ytNoAPIKeyNote
	}
	if !strings.HasSuffix(msg, ".") && !strings.HasSuffix(msg, "…") && !strings.HasSuffix(msg, "?") && !strings.HasSuffix(msg, "!") {
		msg += "."
	}
	return msg + " " + ytNoAPIKeyNote
}

// String names a cause for the log.
func (k ytFailure) String() string {
	switch k {
	case ytFailOffline:
		return "offline"
	case ytFailTimeout:
		return "timeout"
	case ytFailClock:
		return "clock"
	case ytFailTrust:
		return "no-ca-store"
	case ytFailBotCheck:
		return "bot-check"
	case ytFailRateLimited:
		return "rate-limited"
	case ytFailForbidden:
		return "http-403"
	case ytFailNoFormat:
		return "no-format"
	case ytFailOutdated:
		return "outdated-extractor"
	case ytFailUnavailable:
		return "video-unavailable"
	case ytFailUnpack:
		return "helper-unpack"
	case ytFailHelperBroken:
		return "helper-cannot-run"
	}
	return "unknown"
}

// wantsHelperUpdate reports whether a newer yt-dlp is the likely fix. These
// are the shapes YouTube's changes take from the outside: a client it started
// refusing (403, the bot check), a format list a client no longer carries, a
// page the extractor can no longer parse. A dead network, a wrong clock or a
// private video are not, and updating for them would only spend the data.
func (k ytFailure) wantsHelperUpdate() bool {
	switch k {
	case ytFailForbidden, ytFailNoFormat, ytFailOutdated, ytFailBotCheck:
		return true
	}
	return false
}

// classifyYouTubeFailure names the cause in text, judging certificate errors
// against the device clock.
func classifyYouTubeFailure(text string) ytFailure {
	return classifyYouTubeFailureAt(text, bugrepClockWrong(time.Now()))
}

// ytContainsAny reports whether s contains any of the markers.
func ytContainsAny(s string, markers ...string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// classifyYouTubeFailureAt is classifyYouTubeFailure with the clock verdict
// passed in, so the rules are testable without touching the clock. The order
// matters: a video-specific refusal is checked before the generic ones that
// share its words ("Sign in to confirm your age" is not the bot check), and
// a certificate error is the clock's fault only when the clock is wrong or
// the error says "not yet valid".
func classifyYouTubeFailureAt(text string, clockWrong bool) ytFailure {
	if strings.TrimSpace(text) == "" {
		return ytFailUnknown
	}
	// Before the unpack check: one way a binary fails to load is worded with
	// the bootloader's unpack prefix (helperCannotRun).
	if helperCannotRun(text) {
		return ytFailHelperBroken
	}
	if helperUnpackFailure(text) {
		return ytFailUnpack
	}
	lower := strings.ToLower(text)
	// YouTube writes "you’re" with a typographic apostrophe in some answers.
	lower = strings.ReplaceAll(lower, "’", "'")
	if strings.Contains(lower, "not a bot") {
		return ytFailBotCheck
	}
	// "This video is private" is the player endpoint's wording (with
	// LOGIN_REQUIRED); "Private video" is yt-dlp's.
	if ytContainsAny(lower,
		"private video", "video is private", "video unavailable", "this video is unavailable",
		"has been removed", "members-only", "join this channel",
		"confirm your age", "age-restricted", "age restricted", "inappropriate for some users",
		"not available in your country", "blocked it in your country", "uploader has not made this video available",
		"copyright", "this live event will begin", "premieres in") {
		return ytFailUnavailable
	}
	// LOGIN_REQUIRED without an age reason is how the player endpoint words
	// the bot check for a client version it has started gating.
	if ytContainsAny(lower, "login_required", "sign in to confirm") {
		return ytFailBotCheck
	}
	certificate := ytContainsAny(lower, "x509", "certificate", "tls: ", "ssl: ")
	if certificate && (clockWrong || strings.Contains(lower, "not yet valid")) {
		return ytFailClock
	}
	if certificate && ytContainsAny(lower, "unknown authority", "unable to get local issuer", "certificate verify failed") {
		return ytFailTrust
	}
	if ytContainsAny(lower,
		"no such host", "temporary failure in name resolution", "name or service not known",
		"nodename nor servname", "getaddrinfo failed", "failed to resolve", "network is unreachable",
		"no route to host", "connection refused", "offline:") {
		return ytFailOffline
	}
	if ytContainsAny(lower, "http error 429", "429 too many requests", "too many requests", "http 429") {
		return ytFailRateLimited
	}
	if ytContainsAny(lower, "http error 403", "403: forbidden", "403 forbidden", "http 403", "status 403", "status code 403") {
		return ytFailForbidden
	}
	if ytContainsAny(lower,
		"requested format is not available", "no video formats found", "no muxed format",
		"media urls, want one muxed stream") {
		return ytFailNoFormat
	}
	if ytContainsAny(lower,
		"yt-dlp -u", "on the latest version", "please report this issue",
		"unable to extract", "failed to extract", "nsig extraction failed", "signature extraction failed",
		"precondition check failed", "failed to parse json", "jsondecodeerror",
		"no supported javascript runtime") {
		return ytFailOutdated
	}
	if ytContainsAny(lower, "timed out", "timeout", "deadline exceeded") {
		return ytFailTimeout
	}
	return ytFailUnknown
}

// ytUpdateState is what the message adds about the helper.
type ytUpdateState int

const (
	ytUpdateNone      ytUpdateState = iota // nothing to say about yt-dlp
	ytUpdateStarted                        // a check/update is running now
	ytUpdateCurrent                        // yt-dlp was found to be the latest
	ytUpdateUnmanaged                      // a yt-dlp JukaHub does not manage
)

// ytSubject is what failed, for the wording: the same 403 reads differently
// for a YouTube video, a YouTube search and a link to another site that the
// helper played.
type ytSubject int

const (
	ytSubjectVideo  ytSubject = iota // a YouTube video's stream
	ytSubjectSearch                  // a YouTube search behind a results grid
	ytSubjectLink                    // a link to another site, played through the helper
)

// youTubeFailureMessage is youTubeFailureMessageFor for a YouTube video, the
// case most callers report.
func youTubeFailureMessage(kind ytFailure, detail string, update ytUpdateState) string {
	return youTubeFailureMessageFor(ytSubjectVideo, kind, detail, update)
}

// youTubeFailureMessageFor is the line a toast or an error state shows for a
// cause. detail is the raw failure text, used only where the cause is the
// video itself (its own words say which of private/removed/blocked it is).
// A link to another site is never blamed on YouTube, and a failed search is
// never called a refused "video stream".
func youTubeFailureMessageFor(subject ytSubject, kind ytFailure, detail string, update ytUpdateState) string {
	site := "YouTube"
	if subject == ytSubjectLink {
		site = "The site"
	}
	msg := ""
	switch kind {
	case ytFailOffline:
		return "No internet connection — connect Wi-Fi, then try again"
	case ytFailTimeout:
		return site + " took too long to answer — check the Wi-Fi, then try again"
	case ytFailClock:
		return "The clock is wrong, so secure sites fail — set Settings › Date & time"
	case ytFailTrust:
		return "This firmware can't check website certificates — put cacert.pem next to JukaHub"
	case ytFailRateLimited:
		return site + " is limiting this network — wait a few minutes, then try again"
	case ytFailUnavailable:
		if reason := cleanYtDlpError(detail); reason != "" {
			return "This video can't play here: " + reason
		}
		return "This video is private, removed or blocked here"
	case ytFailUnpack:
		return "yt-dlp could not unpack itself — free space on the SD card, then try again"
	case ytFailHelperBroken:
		// Nothing on the device can fix this one: the release needs a newer
		// system than the firmware has. An update that caused it is rolled
		// back by the updater (classifyYtDlpProbe).
		return "This yt-dlp release can't run on this firmware (a system library is too old)"
	case ytFailBotCheck:
		msg = site + " asked this network to prove it isn't a bot"
	case ytFailForbidden:
		if subject == ytSubjectSearch {
			msg = "YouTube refused the search (HTTP 403)"
		} else {
			msg = site + " refused the video stream (HTTP 403)"
		}
	case ytFailNoFormat:
		if subject == ytSubjectSearch {
			msg = "YouTube sent a search answer yt-dlp can't use"
		} else {
			msg = site + " offered no format this player can stream"
		}
	case ytFailOutdated:
		msg = site + " changed something yt-dlp doesn't know yet"
	default:
		if reason := cleanYtDlpError(detail); reason != "" {
			return reason
		}
		return site + " did not answer — try again in a moment"
	}
	switch update {
	case ytUpdateStarted:
		// Only a check has started: the release may turn out to be the one
		// already installed, and the updater says so when it is.
		return msg + " — checking for a newer yt-dlp"
	case ytUpdateCurrent:
		if kind == ytFailBotCheck {
			return msg + " — try again later or on another Wi-Fi"
		}
		return msg + " — yt-dlp is up to date; try again later"
	case ytUpdateUnmanaged:
		return msg + " — update your yt-dlp"
	}
	return msg
}

// cleanYtDlpError shortens an extractor error to the sentence that explains
// it: "ERROR: [youtube] dQw4w9WgXcQ: Private video. Sign in if you've been
// granted access" reads "Private video". The log keeps the full text.
func cleanYtDlpError(text string) string {
	line := ""
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if line == "" {
			line = l
		}
		if strings.Contains(l, "ERROR:") {
			line = l
			break
		}
	}
	if i := strings.Index(line, "ERROR:"); i >= 0 {
		line = strings.TrimSpace(line[i+len("ERROR:"):])
	}
	// "[youtube] <id>: reason" and "[youtube:search] <query>: reason".
	if strings.HasPrefix(line, "[") {
		if end := strings.Index(line, "]"); end > 0 {
			line = strings.TrimSpace(line[end+1:])
			if colon := strings.Index(line, ": "); colon >= 0 && colon <= 64 {
				line = strings.TrimSpace(line[colon+2:])
			}
		}
	}
	// The first sentence is the reason; what follows is advice for a desktop
	// (cookies from a browser, a GitHub link) that does not apply here.
	if dot := strings.Index(line, ". "); dot > 0 {
		line = line[:dot]
	}
	line = strings.TrimSuffix(strings.TrimSpace(line), ".")
	line = redactDiagnosticText(line)
	return truncateRunesSuffix(line, 100, "…")
}

// ytAPIKeyNoted keeps the API-key note to one log line per run.
var ytAPIKeyNoted sync.Once

// ytNoteIgnoredAPIKey logs, once, that a google_api_key in the config is not
// used. Nothing in JukaHub needs one: search and playback use the site's own
// public endpoints (youtubesearch.go, youtubenative.go) and yt-dlp, and
// yt-dlp has no argument a Data API key could go into.
func ytNoteIgnoredAPIKey(config *Config) {
	if config == nil {
		return
	}
	if v, ok := config.Variables.Custom["google_api_key"].(string); !ok || strings.TrimSpace(v) == "" {
		return
	}
	ytAPIKeyNoted.Do(func() {
		logInfo("youtube", "google_api_key is set but unused: YouTube needs no API key in JukaHub")
	})
}

// ytFailureNote is the last failure of one results variable.
type ytFailureNote struct {
	kind    ytFailure
	message string
	at      time.Time
}

// The last failure per results variable. search_error is one variable shared
// by the search, Trending and Shorts, so the Trending and Shorts tabs could
// only say "The service did not answer"; this keeps each list's own reason.
var ytLastFailures = struct {
	mu sync.Mutex
	m  map[string]ytFailureNote
}{m: map[string]ytFailureNote{}}

// noteYouTubeFailure records why resultVar's fetch failed.
func noteYouTubeFailure(resultVar string, kind ytFailure, message string) {
	if resultVar == "" {
		return
	}
	ytLastFailures.mu.Lock()
	ytLastFailures.m[resultVar] = ytFailureNote{kind: kind, message: message, at: time.Now()}
	ytLastFailures.mu.Unlock()
}

// clearYouTubeFailure forgets resultVar's failure once a fetch succeeded.
func clearYouTubeFailure(resultVar string) {
	ytLastFailures.mu.Lock()
	delete(ytLastFailures.m, resultVar)
	ytLastFailures.mu.Unlock()
}

// youTubeFailureFor returns resultVar's last failure, if it has one.
func youTubeFailureFor(resultVar string) (ytFailureNote, bool) {
	ytLastFailures.mu.Lock()
	defer ytLastFailures.mu.Unlock()
	note, ok := ytLastFailures.m[resultVar]
	return note, ok
}

// ytRetryWindow is how long after a failed search a yt-dlp update may still
// re-run it by itself. Later than that the user has moved on, and a list that
// refills (and a Results tab that takes the screen) out of nowhere is a
// surprise rather than a fix.
const ytRetryWindow = 3 * time.Minute

// youTubeRetryWanted reports whether the search behind resultVar should run
// again now that an update has landed: the list still shows its failure, the
// failure is recent, nothing is playing or loading (a retry starts a preload
// that would compete with the stream), and — for the search results while
// Media is on screen — the user is still on the Results tab, which the re-run
// would otherwise pull them onto.
func youTubeRetryWanted(resultVar string, now time.Time) bool {
	note, ok := youTubeFailureFor(resultVar)
	playerIdle := playerPhase() == "idle" && !videoPrepareActive()
	tabBlocks := resultVar == mediaResultsVar && mediaHubSearchRetryBlocked()
	return ytRetryAllowed(note, ok, now, playerIdle, tabBlocks)
}

// ytRetryAllowed is youTubeRetryWanted's decision with every input passed in,
// so it is testable without a player or a screen.
func ytRetryAllowed(note ytFailureNote, haveNote bool, now time.Time, playerIdle, tabBlocks bool) bool {
	if !haveNote || note.at.IsZero() || now.Sub(note.at) > ytRetryWindow {
		return false
	}
	return playerIdle && !tabBlocks
}

// youTubeSearchFailure turns a failed yt-dlp search into the line the grid
// and the toast show. When the cause is one an extractor update fixes, the
// update starts here, and retry (when not nil) runs once a newer yt-dlp has
// been installed, so the search the user asked for is answered without a
// second press.
func youTubeSearchFailure(config *Config, resultVar, stderr string, err error, toolPath string, retry func()) string {
	if helperUnpackFailure(stderr) {
		msg := ytDlpFailureReason(stderr, err, toolPath)
		noteYouTubeFailure(resultVar, ytFailUnpack, msg)
		return msg
	}
	text := stderr
	if err != nil {
		text = strings.TrimSpace(text + "\n" + err.Error())
	}
	kind := classifyYouTubeFailure(text)
	if kind == ytFailUnknown && !IsOnline() {
		kind = ytFailOffline
	}
	update := ytUpdateNone
	if kind.wantsHelperUpdate() {
		update = requestYtDlpUpdate(config, ytdlpTriggerFailure, func(outcome ytdlpUpdateOutcome) {
			if outcome == ytdlpOutcomeUpdated && retry != nil {
				retry()
			}
		})
	}
	msg := youTubeFailureMessageFor(ytSubjectSearch, kind, text, update)
	noteYouTubeFailure(resultVar, kind, msg)
	logWarn("youtube", "search for %s failed (%s): %s", resultVar, kind, msg)
	return msg
}

// The last stream-resolution failure per source. A play that cannot stream
// ends in the whole-file download, and when that fails too its stderr is often
// the less telling of the two ("Requested format is not available" where the
// stream attempts saw the 403 that explains it), so the final message reads
// both.
var ytStreamFailures = struct {
	mu sync.Mutex
	m  map[string]string
}{m: map[string]string{}}

// noteStreamResolveFailure remembers why source could not be streamed.
func noteStreamResolveFailure(source, text string) {
	source = strings.TrimSpace(source)
	if source == "" {
		return
	}
	ytStreamFailures.mu.Lock()
	if len(ytStreamFailures.m) >= 64 {
		// A preload walks whole grids; the map only needs the recent ones.
		ytStreamFailures.m = map[string]string{}
	}
	ytStreamFailures.m[source] = text
	ytStreamFailures.mu.Unlock()
}

// streamResolveFailure returns what noteStreamResolveFailure kept for source.
func streamResolveFailure(source string) string {
	ytStreamFailures.mu.Lock()
	defer ytStreamFailures.mu.Unlock()
	return ytStreamFailures.m[strings.TrimSpace(source)]
}

// youTubePlaybackFailureMessage is the toast for a play that neither streamed
// nor downloaded. It used to say "Download failed. Check network." whatever
// happened; a YouTube cause that an update fixes starts the update here, and
// the toast says so (the updater's own toast says when to try again).
func youTubePlaybackFailureMessage(config *Config, source, stderr string) string {
	if !IsOnline() {
		return youTubeFailureMessage(ytFailOffline, "", ytUpdateNone)
	}
	text := strings.TrimSpace(stderr + "\n" + streamResolveFailure(source))
	kind := classifyYouTubeFailure(text)
	isYouTube := youtubeVideoID(source) != ""
	update := ytUpdateNone
	if kind.wantsHelperUpdate() && isYouTube {
		update = requestYtDlpUpdate(config, ytdlpTriggerFailure, nil)
	}
	subject := ytSubjectVideo
	if !isYouTube {
		// Not a YouTube link (an IPTV channel, a podcast, another video
		// site): a 403 there is that site's, not YouTube's.
		subject = ytSubjectLink
	}
	msg := youTubeFailureMessageFor(subject, kind, text, update)
	if kind == ytFailUnknown && !isYouTube {
		// Its own words, with what was being tried.
		msg = "Could not play this link — " + msg
	}
	logWarn("youtube", "playback of %s failed (%s): %s", sanitizeLogURL(source), kind, msg)
	return msg
}
