package main

// Tests for Report a bug's sending side: where reports may go, what is
// scrubbed before anything leaves the device, that the preview shows exactly
// what is sent, how the collector's answers are read, and that the outbox
// neither loses a report nor retries one for ever.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBugrepEndpointPrecedence(t *testing.T) {
	prevBuild, prevTele, prevDefault := bugrepEndpoint, telemetryEndpoint, bugrepDefaultEndpoint
	t.Cleanup(func() { bugrepEndpoint, telemetryEndpoint, bugrepDefaultEndpoint = prevBuild, prevTele, prevDefault })
	t.Setenv("JUKAHUB_BUGREPORT_URL", "")
	t.Setenv("JUKAHUB_TELEMETRY_URL", "")
	bugrepEndpoint, telemetryEndpoint, bugrepDefaultEndpoint = "", "", ""
	if got := bugrepEndpointFor(nil); got != "" {
		t.Fatalf("no collector is configured, got %q", got)
	}
	bugrepDefaultEndpoint = "https://service.example/api/bug-report"
	if got := bugrepEndpointFor(nil); got != bugrepDefaultEndpoint {
		t.Fatalf("the JukaHub report service is the fallback, got %q", got)
	}
	telemetryEndpoint = "https://telemetry.example/v1"
	if got := bugrepEndpointFor(nil); got != telemetryEndpoint {
		t.Fatalf("telemetry must win over the report service, got %q", got)
	}
	bugrepEndpoint = "https://build.example/bug"
	if got := bugrepEndpointFor(nil); got != bugrepEndpoint {
		t.Fatalf("the build's collector must win over telemetry, got %q", got)
	}
	custom := map[string]interface{}{bugrepConfigKey: "https://config.example/bug"}
	if got := bugrepEndpointFor(custom); got != "https://config.example/bug" {
		t.Fatalf("the config must win over the build, got %q", got)
	}
	t.Setenv("JUKAHUB_BUGREPORT_URL", "https://env.example/bug")
	if got := bugrepEndpointFor(custom); got != "https://env.example/bug" {
		t.Fatalf("the environment must win, got %q", got)
	}
	// A plain-http collector on the network is skipped, not used.
	t.Setenv("JUKAHUB_BUGREPORT_URL", "http://reports.example/bug")
	if got := bugrepEndpointFor(custom); got != "https://config.example/bug" {
		t.Fatalf("an http collector was accepted: %q", got)
	}
	// A config value of the wrong type is ignored.
	if got := bugrepEndpointFor(map[string]interface{}{bugrepConfigKey: 42}); got != bugrepEndpoint {
		t.Fatalf("a non-string config value = %q", got)
	}
}

func TestBugrepEndpointAllowed(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://reports.example/v1/bug": true,
		"http://localhost:8080/bug":      true,
		"http://127.0.0.1:9000/bug":      true,
		"http://[::1]:9000/bug":          true,
		"http://reports.example/bug":     false,
		"ftp://reports.example/bug":      false,
		"https://":                       false,
		"reports.example/bug":            false,
		"":                               false,
	} {
		if got := bugrepEndpointAllowed(raw); got != want {
			t.Errorf("bugrepEndpointAllowed(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestBugrepScrubRemovesPersonalData(t *testing.T) {
	cases := []struct {
		in   string
		gone []string
		keep []string
	}{
		{"open http://bob:hunter2@media.example/private/list.m3u8?key=abc", []string{"hunter2", "bob", "private", "abc"}, []string{"media.example"}},
		{"write to alice@example.com", []string{"alice@example.com"}, []string{"write to"}},
		{"wlan0 aa:bb:cc:dd:ee:ff up", []string{"aa:bb:cc:dd:ee:ff"}, []string{"wlan0", "up"}},
		{"peer 10.1.2.3 joined", []string{"10.1.2.3"}, []string{"peer", "joined"}},
		{"dns 8.8.8.8 answered", nil, []string{"8.8.8.8"}},
		{`saved C:\Users\Bob\clip.mp4 ok`, []string{"Bob", "clip.mp4"}, []string{"saved", "ok"}},
		{"missing /mnt/SDCARD/Roms/game.zip now", []string{"SDCARD", "game.zip"}, []string{"missing", "now"}},
		{"config at /home/carol/.config/jukahub", []string{"carol"}, []string{"config at"}},
		{"password: swordfish", []string{"swordfish"}, []string{"password"}},
		// A scheme in capitals and a link with no scheme lose their paths too.
		{"see HTTPS://Media.example/Private/list?token=abc", []string{"Private", "token=abc"}, []string{"Media.example"}},
		{"visit www.example.com/secret/page?token=abc now", []string{"secret", "token"}, []string{"www.example.com", "now"}},
	}
	for _, c := range cases {
		got := bugrepScrub(c.in)
		for _, g := range c.gone {
			if strings.Contains(got, g) {
				t.Errorf("scrub(%q) = %q still contains %q", c.in, got, g)
			}
		}
		for _, k := range c.keep {
			if !strings.Contains(got, k) {
				t.Errorf("scrub(%q) = %q lost %q", c.in, got, k)
			}
		}
	}
}

func TestBugrepBuildHonoursTheToggles(t *testing.T) {
	withTempPatchState(t) // DiagnosticsText creates patch/ state folders
	config := &Config{Version: "1.2.3"}
	when := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	r := bugrepBuild(config, bugrepForm{Category: 99, Title: "  Hi  "}, when)
	if r.Category != "other" || r.Title != "Hi" || r.Diagnostics != "" || r.Log != "" || r.Created != "2026-11-08T10:00:00Z" {
		t.Fatalf("report with everything off = %+v", r)
	}
	r = bugrepBuild(config, bugrepForm{Category: 2, Diagnostics: true, Log: true}, when)
	if r.Category != "controls" || r.Diagnostics == "" || r.Log == "" {
		t.Fatalf("report with everything on = %+v", r)
	}
	if r.OS != runtime.GOOS || r.Arch != runtime.GOARCH || r.Schema != 1 || r.App != "JukaHub" {
		t.Fatalf("report facts = %+v", r)
	}
}

func TestBugrepPreviewShowsWhatIsSent(t *testing.T) {
	r := bugrepReport{Category: "media", Description: "Audio stutters", Version: "1.2.3", Device: "TrimUI Smart Pro", Platform: "TrimUI", OS: "linux", Arch: "arm64"}
	text := bugrepPreviewText(r)
	for _, want := range []string{"Category: media", "Title: (no title)", "Audio stutters", "JukaHub 1.2.3", "Not included.", "## Never included"} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview lacks %q:\n%s", want, text)
		}
	}
	r.Release, r.Diagnostics, r.Log = "JukaHub-20261101000000", "fps 60", "[ui] opened"
	r.ID, r.Created = "JH-7K2Q9M", "2026-11-10T12:00:00Z"
	text = bugrepPreviewText(r)
	for _, want := range []string{"(JukaHub-20261101000000)", "## Diagnostics (redacted)\nfps 60", "## Recent log (redacted)\n[ui] opened",
		// The issue's facts table shows both, so the preview must.
		"Reference: JH-7K2Q9M", "Created: 2026-11-10T12:00:00Z"} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview lacks %q:\n%s", want, text)
		}
	}
}

func TestBugrepDraftKeepsItsReference(t *testing.T) {
	prevForm, prevTouched, prevFocus, prevRef := bugrepScreen.form, bugrepScreen.touched, bugrepScreen.nav.focus, bugrepScreen.draftRef
	prevAt := bugrepScreen.draftAt
	t.Cleanup(func() {
		bugrepScreen.form, bugrepScreen.touched, bugrepScreen.nav.focus, bugrepScreen.draftRef = prevForm, prevTouched, prevFocus, prevRef
		bugrepScreen.draftAt = prevAt
	})
	bugrepScreen.draftRef = ""
	bugrepScreen.draftAt = time.Time{}
	first := bugrepDraftRef()
	if !bugrepRefValid(first) || bugrepDraftRef() != first {
		t.Fatalf("the draft's reference changed between preview and Submit: %q", first)
	}
	// The Created time is pinned the same way, so the preview shows the time
	// the report is sent with.
	created := bugrepDraftTime()
	if created.IsZero() || !bugrepDraftTime().Equal(created) {
		t.Fatalf("the draft's time changed between preview and Submit: %v", created)
	}
	bugrepFormDone()
	if bugrepScreen.draftRef != "" || !bugrepScreen.draftAt.IsZero() {
		t.Fatal("a finished report must start the next draft with a new reference and time")
	}
}

func TestBugrepDefaultEndpointIsTheReportService(t *testing.T) {
	if !bugrepEndpointAllowed(bugrepDefaultEndpoint) || !strings.HasPrefix(bugrepDefaultEndpoint, "https://") ||
		!strings.HasSuffix(bugrepDefaultEndpoint, "/api/bug-report") {
		t.Fatalf("the shipped report service is %q", bugrepDefaultEndpoint)
	}
}

func TestBugrepReadAnswerClassifies(t *testing.T) {
	const ref = "JH-ABCDEF"
	issue := "https://github.com/jukaLang/JukaHubV2/issues/42"
	cases := []struct {
		name   string
		status int
		ctype  string
		body   string
		kind   bugrepSendErr
		want   bugrepSent
	}{
		{"filed", 201, "application/json", `{"id":"JH-ZZZZZZ","issue":42,"url":"` + issue + `"}`, bugrepErrNone, bugrepSent{Ref: "JH-ZZZZZZ", Issue: 42, URL: issue}},
		{"duplicate", 200, "application/json", `{"id":"JH-ZZZZZZ","issue":42,"url":"` + issue + `","duplicate":true}`, bugrepErrNone, bugrepSent{Ref: "JH-ZZZZZZ", Issue: 42, URL: issue}},
		{"empty 204", 204, "", ``, bugrepErrNone, bugrepSent{Ref: ref}},
		{"foreign address dropped", 201, "application/json", `{"issue":7,"url":"https://evil.example/x/issues/7"}`, bugrepErrNone, bugrepSent{Ref: ref, Issue: 7}},
		{"plain http address dropped", 201, "application/json", `{"issue":7,"url":"http://github.com/a/b/issues/7"}`, bugrepErrNone, bugrepSent{Ref: ref, Issue: 7}},
		{"bad id ignored", 200, "application/json", `{"id":"<b>hi</b>","issue":-3}`, bugrepErrNone, bugrepSent{Ref: ref}},
		{"web page", 200, "text/html; charset=utf-8", `<html><body>JukaHub</body></html>`, bugrepErrUnavailable, bugrepSent{}},
		{"no function", 404, "text/html", `Not Found`, bugrepErrUnavailable, bugrepSent{}},
		{"method", 405, "", ``, bugrepErrUnavailable, bugrepSent{}},
		{"gone", 410, "", ``, bugrepErrUnavailable, bugrepSent{}},
		{"not set up", 503, "application/json", `{"error":"not set up","fallback":"phone"}`, bugrepErrSetup, bugrepSent{}},
		{"configured false", 503, "application/json", `{"configured":false}`, bugrepErrSetup, bugrepSent{}},
		{"fallback wins", 500, "application/json", `{"fallback":"phone"}`, bugrepErrSetup, bugrepSent{}},
		{"busy", 429, "application/json", `{"error":"slow down"}`, bugrepErrServer, bugrepSent{}},
		{"github down", 502, "application/json", `{}`, bugrepErrServer, bugrepSent{}},
		{"server error", 500, "", ``, bugrepErrServer, bugrepSent{}},
		{"refused", 422, "application/json", `{"error":"unknown category"}`, bugrepErrRejected, bugrepSent{}},
		{"forbidden", 403, "application/json", `{"error":"only the JukaHub app sends reports here"}`, bugrepErrRejected, bugrepSent{}},
		{"github refused", 422, "application/json", `{"error":"GitHub refused the issue"}`, bugrepErrRejected, bugrepSent{}},
		// Only the collector itself, answering in JSON, may refuse (and so
		// retire) a report: anybody else's page or bare status keeps it.
		{"proxy page", 403, "text/html", `<html><body>Access denied</body></html>`, bugrepErrUnavailable, bugrepSent{}},
		{"page without a type", 401, "", `<!DOCTYPE html><title>Login</title>`, bugrepErrUnavailable, bugrepSent{}},
		{"bare refusal", 403, "", ``, bugrepErrUnavailable, bugrepSent{}},
		{"plain-text refusal", 400, "text/plain", `Bad Request`, bugrepErrUnavailable, bugrepSent{}},
		{"redirect not followed", 307, "", ``, bugrepErrUnavailable, bugrepSent{}},
		{"redirect with a JSON body", 302, "application/json", `{"error":"moved"}`, bugrepErrUnavailable, bugrepSent{}},
		// A proxy's or gateway's JSON refusal is not the collector's.
		{"proxy auth in JSON", 407, "application/json", `{"error":"proxy authentication required"}`, bugrepErrUnavailable, bugrepSent{}},
		{"gateway auth in JSON", 401, "application/json", `{"error":"unauthorized"}`, bugrepErrUnavailable, bugrepSent{}},
		// A gateway's own error page is a pause, not a missing collector.
		{"gateway page", 502, "text/html", `<html><body>Bad gateway</body></html>`, bugrepErrServer, bugrepSent{}},
		{"gateway timeout page", 504, "text/html", `<html><body>Timed out</body></html>`, bugrepErrServer, bugrepSent{}},
		{"plain ok", 200, "text/plain", `ok`, bugrepErrNone, bugrepSent{Ref: ref}},
	}
	for _, c := range cases {
		got, kind, err := bugrepReadAnswer(c.status, c.ctype, []byte(c.body), ref)
		if kind != c.kind || got != c.want {
			t.Errorf("%s: got %+v kind %d (%v), want %+v kind %d", c.name, got, kind, err, c.want, c.kind)
		}
		if (kind == bugrepErrNone) != (err == nil) {
			t.Errorf("%s: kind %d came with error %v", c.name, kind, err)
		}
	}
	_, _, err := bugrepReadAnswer(422, "application/json", []byte(`{"error":"unknown category\nsecond line"}`), ref)
	if err == nil || !strings.Contains(err.Error(), "unknown category") || strings.Contains(err.Error(), "second line") {
		t.Fatalf("the refusal reason = %v", err)
	}
}

func TestBugrepIssueURLValid(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://github.com/jukaLang/JukaHubV2/issues/42", true},
		{"https://GitHub.com/jukaLang/JukaHubV2/issues/1", true},
		{"http://github.com/jukaLang/JukaHubV2/issues/42", false},
		{"https://github.com/jukaLang/JukaHubV2/pull/42", false},
		{"https://github.com.evil.example/a/b/issues/1", false},
		{"https://evil.example/github.com/issues/1", false},
		{"", false},
		{"https://github.com/a/b/issues/" + strings.Repeat("1", 300), false},
	}
	for _, c := range cases {
		if got := bugrepIssueURLValid(c.raw); got != c.want {
			t.Errorf("bugrepIssueURLValid(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestBugrepPostSendsTheReport(t *testing.T) {
	var gotUA, gotType, raw string
	var got bugrepReport
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotUA, gotType = req.Header.Get("User-Agent"), req.Header.Get("Content-Type")
		data, _ := io.ReadAll(req.Body)
		raw = string(data)
		_ = json.Unmarshal(data, &got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"JH-7K2Q9M","issue":12,"url":"https://github.com/jukaLang/JukaHubV2/issues/12"}`))
	}))
	defer srv.Close()
	r := bugrepReport{Schema: 1, Kind: "bug_report", App: "JukaHub", ID: "JH-7K2Q9M", Category: "media", Title: "Hi <b>&</b>", Version: "v1.2.3"}
	sent, kind, err := bugrepPost(srv.URL, r)
	if err != nil || kind != bugrepErrNone || sent.Issue != 12 || sent.Ref != "JH-7K2Q9M" || sent.URL == "" {
		t.Fatalf("post = %+v, kind %d, %v", sent, kind, err)
	}
	if gotUA != "JukaHub-BugReport/v1.2.3" || gotType != "application/json" || got.ID != r.ID || got.Title != r.Title {
		t.Fatalf("the collector saw UA %q, type %q, report %+v", gotUA, gotType, got)
	}
	// Six-byte HTML escapes would push a log full of < > & past the
	// collector's size cap.
	if !strings.Contains(raw, `"title":"Hi <b>&</b>"`) || strings.HasSuffix(raw, "\n") {
		t.Fatalf("the report went out as %q", raw)
	}

	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	if _, kind, err := bugrepPost(missing.URL, r); kind != bugrepErrUnavailable || err == nil {
		t.Fatalf("a missing collector = kind %d, %v", kind, err)
	}
}

func TestBugrepPostFollowsNoRedirectThePreviewDidNotPromise(t *testing.T) {
	var asked atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer elsewhere.Close()
	// A plain-http address that is not this machine is no place for a report.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "http://reports.example/bug", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	r := bugrepReport{Schema: 1, ID: "JH-7K2Q9M", Version: "v1"}
	if _, kind, err := bugrepPost(srv.URL, r); kind != bugrepErrUnavailable || err == nil {
		t.Fatalf("a redirect off the allowed addresses = kind %d, %v", kind, err)
	}
	// A redirect to an allowed address (http on this machine) is followed,
	// with the report.
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, elsewhere.URL+"/bug", http.StatusTemporaryRedirect)
	}))
	defer hop.Close()
	if _, kind, err := bugrepPost(hop.URL, r); kind != bugrepErrNone || err != nil || asked.Load() != 1 {
		t.Fatalf("an allowed redirect = kind %d, %v, asked %d", kind, err, asked.Load())
	}
	// A 302 would turn the POST into a GET without the report; an answer to
	// that must not read as "taken", so the redirect is not followed.
	found := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, elsewhere.URL+"/bug", http.StatusFound)
	}))
	defer found.Close()
	if _, kind, err := bugrepPost(found.URL, r); kind != bugrepErrUnavailable || err == nil || asked.Load() != 1 {
		t.Fatalf("a 302 = kind %d, %v, asked %d (the GET must not be sent)", kind, err, asked.Load())
	}
}

func TestBugrepOutboxDelayBacksOff(t *testing.T) {
	if bugrepOutboxDelay(0) != bugrepOutboxInterval || bugrepOutboxDelay(1) != 2*bugrepOutboxInterval ||
		bugrepOutboxDelay(3) != 8*bugrepOutboxInterval {
		t.Fatalf("delays %v %v %v", bugrepOutboxDelay(0), bugrepOutboxDelay(1), bugrepOutboxDelay(3))
	}
	if bugrepOutboxDelay(40) != bugrepOutboxRetryMax || bugrepOutboxDelay(1<<20) != bugrepOutboxRetryMax {
		t.Fatalf("the wait must be capped at %v", bugrepOutboxRetryMax)
	}
}

func TestBugrepReportExpired(t *testing.T) {
	prev := buildDate
	buildDate = ""
	t.Cleanup(func() { buildDate = prev })
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) bugrepReport {
		return bugrepReport{Created: now.Add(-d).Format(time.RFC3339)}
	}
	if !bugrepReportExpired(at(31*24*time.Hour), now) {
		t.Error("a month-old report must retire")
	}
	if bugrepReportExpired(at(29*24*time.Hour), now) {
		t.Error("a report under a month old must keep trying")
	}
	if bugrepReportExpired(bugrepReport{Created: "not a time"}, now) {
		t.Error("an unreadable date is no reason to retire a report")
	}
	if bugrepReportExpired(bugrepReport{Created: "1970-01-01T00:00:12Z"}, now) {
		t.Error("a report made while the clock was wrong has no age")
	}
	if bugrepReportExpired(at(31*24*time.Hour), time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("a wrong clock now cannot judge a report's age")
	}
	// An update moves the build date past the report: it still retires once it
	// is a month old, instead of looking clock-wrong for ever.
	buildDate = "2026-11-09"
	queued := bugrepReport{Created: "2026-11-01T09:00:00Z"}
	if !bugrepReportExpired(queued, time.Date(2026, 12, 20, 12, 0, 0, 0, time.UTC)) {
		t.Error("a report queued before the installed build was made must still retire after a month")
	}
	if bugrepReportExpired(queued, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)) {
		t.Error("a report under a month old must keep trying after an update")
	}
}

// bugrepTestOutbox points the outbox at a temporary folder and puts its state
// and the send seam back afterwards.
func bugrepTestOutbox(t *testing.T) string {
	t.Helper()
	withTempPatchState(t) // bugrepRetire keeps a readable copy there
	root := t.TempDir()
	prevDir, prevPost, prevBuild := bugrepOutboxDirOverride, bugrepPostFunc, buildDate
	prevRemove, prevCopy := bugrepRemove, bugrepSaveCopyFunc
	bugrepOutboxDirOverride = filepath.Join(root, "outbox")
	buildDate = ""
	forgetSettled := func() {
		bugrepSettled.mu.Lock()
		bugrepSettled.ids = nil
		bugrepSettled.mu.Unlock()
	}
	forgetSettled()
	t.Cleanup(func() {
		bugrepOutboxDirOverride, bugrepPostFunc, buildDate = prevDir, prevPost, prevBuild
		bugrepRemove, bugrepSaveCopyFunc = prevRemove, prevCopy
		forgetSettled()
		bugrepOutbox.mu.Lock()
		bugrepOutbox.count, bugrepOutbox.flushing, bugrepOutbox.stopped = 0, false, false
		bugrepOutbox.failures, bugrepOutbox.lastTry = 0, time.Time{}
		bugrepOutbox.mu.Unlock()
	})
	return root
}

// bugrepWaitFlush waits for the background flush to finish.
func bugrepWaitFlush(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		bugrepOutbox.mu.Lock()
		busy := bugrepOutbox.flushing
		bugrepOutbox.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the outbox flush did not finish")
}

func TestBugrepFlushStopsForAnUnavailableCollector(t *testing.T) {
	bugrepTestOutbox(t)
	r := bugrepReport{Schema: 1, ID: "JH-AAAAAA", Title: "waits", Created: time.Now().UTC().Format(time.RFC3339)}
	if err := bugrepQueue(r); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	bugrepPostFunc = func(string, bugrepReport) (bugrepSent, bugrepSendErr, error) {
		calls.Add(1)
		return bugrepSent{}, bugrepErrUnavailable, errors.New("HTTP 404")
	}
	bugrepFlush("https://service.example/api/bug-report", nil)
	bugrepWaitFlush(t)
	bugrepOutbox.mu.Lock()
	stopped := bugrepOutbox.stopped
	dueLater := bugrepOutboxDueLocked(time.Now().Add(48 * time.Hour))
	bugrepOutbox.mu.Unlock()
	if !stopped || dueLater || calls.Load() != 1 {
		t.Fatalf("after an unavailable collector: stopped %v, due later %v, calls %d", stopped, dueLater, calls.Load())
	}
	if queued := bugrepQueued(); len(queued) != 1 || queued[0].ID != r.ID {
		t.Fatalf("the report must stay queued, have %+v", queued)
	}

	// A report the collector takes again starts the outbox at once.
	bugrepPostFunc = func(string, bugrepReport) (bugrepSent, bugrepSendErr, error) {
		calls.Add(1)
		return bugrepSent{Ref: r.ID}, bugrepErrNone, nil
	}
	bugrepCollectorAnswered("https://service.example/api/bug-report")
	bugrepWaitFlush(t)
	if len(bugrepQueued()) != 0 || bugrepOutboxStopped() || calls.Load() != 2 {
		t.Fatalf("after the collector answered: queued %d, stopped %v, calls %d", len(bugrepQueued()), bugrepOutboxStopped(), calls.Load())
	}
}

func TestBugrepFlushBacksOffWhileTheCollectorFails(t *testing.T) {
	bugrepTestOutbox(t)
	if err := bugrepQueue(bugrepReport{Schema: 1, ID: "JH-BBBBBB", Created: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	bugrepPostFunc = func(string, bugrepReport) (bugrepSent, bugrepSendErr, error) {
		return bugrepSent{}, bugrepErrServer, errors.New("HTTP 502")
	}
	for i := 1; i <= 3; i++ {
		bugrepFlush("https://service.example/api/bug-report", nil)
		bugrepWaitFlush(t)
		bugrepOutbox.mu.Lock()
		failures, stopped := bugrepOutbox.failures, bugrepOutbox.stopped
		soon := bugrepOutboxDueLocked(bugrepOutbox.lastTry.Add(bugrepOutboxInterval))
		bugrepOutbox.mu.Unlock()
		if failures != i || stopped || soon {
			t.Fatalf("flush %d: failures %d, stopped %v, due after one interval %v", i, failures, stopped, soon)
		}
	}
	if len(bugrepQueued()) != 1 {
		t.Fatal("a failing collector must not cost the report")
	}
}

func TestBugrepFlushRetriesAfterServiceSetup(t *testing.T) {
	bugrepTestOutbox(t)
	r := bugrepReport{Schema: 1, ID: "JH-SETUP1", Created: time.Now().UTC().Format(time.RFC3339)}
	if err := bugrepQueue(r); err != nil {
		t.Fatal(err)
	}
	bugrepPostFunc = func(string, bugrepReport) (bugrepSent, bugrepSendErr, error) {
		return bugrepSent{}, bugrepErrSetup, errors.New("GitHub token missing")
	}
	bugrepFlush("https://service.example/api/bug-report", nil)
	bugrepWaitFlush(t)
	bugrepOutbox.mu.Lock()
	stopped, failures := bugrepOutbox.stopped, bugrepOutbox.failures
	due := bugrepOutboxDueLocked(bugrepOutbox.lastTry.Add(bugrepOutboxDelay(failures)))
	bugrepOutbox.mu.Unlock()
	if stopped || failures != 1 || !due || len(bugrepQueued()) != 1 {
		t.Fatalf("setup failure: stopped %v, failures %d, due %v, queued %d", stopped, failures, due, len(bugrepQueued()))
	}
	bugrepPostFunc = func(string, bugrepReport) (bugrepSent, bugrepSendErr, error) {
		return bugrepSent{Ref: r.ID, Issue: 42}, bugrepErrNone, nil
	}
	bugrepFlush("https://service.example/api/bug-report", func(int) {})
	bugrepWaitFlush(t)
	if len(bugrepQueued()) != 0 {
		t.Fatal("the saved report did not send after setup")
	}
}

func TestBugrepQueuedReportAppearsInNotificationsWhenSent(t *testing.T) {
	bugrepTestOutbox(t)
	previous, added := notifHistoryState()
	t.Cleanup(func() {
		notifHistoryMu.Lock()
		nc.history, notifAdded = previous, added
		notifHistoryMu.Unlock()
	})
	r := bugrepReport{Schema: 1, ID: "JH-NOTIFY", Created: time.Now().UTC().Format(time.RFC3339)}
	if err := bugrepQueue(r); err != nil {
		t.Fatal(err)
	}
	bugrepPostFunc = func(string, bugrepReport) (bugrepSent, bugrepSendErr, error) {
		return bugrepSent{Ref: r.ID, Issue: 73}, bugrepErrNone, nil
	}
	bugrepFlush("https://service.example/api/bug-report", nil)
	bugrepWaitFlush(t)
	tbxDrainUI()
	rows, _ := notifHistoryState()
	if len(rows) == 0 || rows[0].Message != "Bug report sent · issue #73" || rows[0].Read || rows[0].Priority != notifSuccess {
		t.Fatalf("the sent report did not raise an unread success notification: %+v", rows)
	}
}

func TestBugrepFlushRetiresRefusedAndStaleReports(t *testing.T) {
	root := bugrepTestOutbox(t)
	stale := bugrepReport{Schema: 1, ID: "JH-CCCCCC", Title: "stale", Created: time.Now().Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339)}
	refused := bugrepReport{Schema: 1, ID: "JH-DDDDDD", Title: "refused", Created: time.Now().UTC().Format(time.RFC3339)}
	for _, r := range []bugrepReport{stale, refused} {
		if err := bugrepQueue(r); err != nil {
			t.Fatal(err)
		}
	}
	var asked atomic.Value
	bugrepPostFunc = func(_ string, r bugrepReport) (bugrepSent, bugrepSendErr, error) {
		asked.Store(r.ID)
		return bugrepSent{}, bugrepErrRejected, errors.New("HTTP 422")
	}
	bugrepFlush("https://service.example/api/bug-report", nil)
	bugrepWaitFlush(t)
	if n := len(bugrepQueued()); n != 0 {
		t.Fatalf("%d report(s) still queued", n)
	}
	if id, _ := asked.Load().(string); id != refused.ID {
		t.Fatalf("only the fresh report may be sent, the collector saw %q", id)
	}
	for _, r := range []bugrepReport{stale, refused} {
		if _, err := os.Stat(filepath.Join(root, "retired", r.ID+".json")); err != nil {
			t.Errorf("%s was not kept in retired/: %v", r.ID, err)
		}
	}
}

func TestBugrepFlushBacksOffForACertificateThatLooksExpired(t *testing.T) {
	bugrepTestOutbox(t) // buildDate "": the clock is past the build date
	if err := bugrepQueue(bugrepReport{Schema: 1, ID: "JH-EEEEEE", Created: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	bugrepPostFunc = func(string, bugrepReport) (bugrepSent, bugrepSendErr, error) {
		return bugrepSent{}, bugrepErrClock, errors.New("x509: certificate has expired or is not yet valid")
	}
	bugrepFlush("https://service.example/api/bug-report", nil)
	bugrepWaitFlush(t)
	bugrepOutbox.mu.Lock()
	failures := bugrepOutbox.failures
	soon := bugrepOutboxDueLocked(bugrepOutbox.lastTry.Add(bugrepOutboxInterval))
	bugrepOutbox.mu.Unlock()
	// That answer cost a TLS handshake, so it waits like any other failure
	// instead of trying again every 90 seconds for as long as it lasts.
	if failures != 1 || soon {
		t.Fatalf("after an expired-looking certificate: failures %d, due after one interval %v", failures, soon)
	}
}

func TestBugrepFlushDoesNotResendWhatItCouldNotRemove(t *testing.T) {
	root := bugrepTestOutbox(t)
	r := bugrepReport{Schema: 1, ID: "JH-FFFFFF", Created: time.Now().UTC().Format(time.RFC3339)}
	if err := bugrepQueue(r); err != nil {
		t.Fatal(err)
	}
	bugrepRemove = func(string) error { return errors.New("read-only file system") }
	var calls atomic.Int32
	bugrepPostFunc = func(string, bugrepReport) (bugrepSent, bugrepSendErr, error) {
		calls.Add(1)
		return bugrepSent{Ref: r.ID}, bugrepErrNone, nil
	}
	for i := 0; i < 2; i++ {
		bugrepFlush("https://service.example/api/bug-report", func(int) {})
		bugrepWaitFlush(t)
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", r.ID+".json")); err != nil {
		t.Fatalf("the test needs the file to stay: %v", err)
	}
	if calls.Load() != 1 || bugrepOutboxCount() != 0 || len(bugrepQueued()) != 0 {
		t.Fatalf("a sent report whose file stayed: posted %d time(s), %d still counted", calls.Load(), bugrepOutboxCount())
	}
}

func TestBugrepRetireKeepsAReportItCannotCopy(t *testing.T) {
	root := bugrepTestOutbox(t)
	stale := bugrepReport{Schema: 1, ID: "JH-GGGGGG", Created: time.Now().Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339)}
	if err := bugrepQueue(stale); err != nil {
		t.Fatal(err)
	}
	bugrepSaveCopyFunc = func(bugrepReport) (string, error) { return "", errors.New("no space left on device") }
	bugrepPostFunc = func(string, bugrepReport) (bugrepSent, bugrepSendErr, error) {
		t.Error("a month-old report must not be sent")
		return bugrepSent{}, bugrepErrServer, errors.New("unexpected")
	}
	bugrepFlush("https://service.example/api/bug-report", nil)
	bugrepWaitFlush(t)
	if _, err := os.Stat(filepath.Join(root, "outbox", stale.ID+".json")); err != nil {
		t.Fatalf("without a readable copy the queued file is the only one and must stay: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "retired", stale.ID+".json")); err == nil {
		t.Fatal("the report was retired without a readable copy")
	}
	if len(bugrepQueued()) != 0 {
		t.Fatal("the report must wait for the next start, not be retried this session")
	}
}

func TestBugrepFailedSendDelaysTheOutbox(t *testing.T) {
	bugrepTestOutbox(t)
	if err := bugrepQueue(bugrepReport{Schema: 1, ID: "JH-HHHHHH", Created: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	bugrepNoteSendFailed(now)
	bugrepOutbox.mu.Lock()
	nextFrame := bugrepOutboxDueLocked(now.Add(time.Second))
	later := bugrepOutboxDueLocked(now.Add(bugrepOutboxDelay(1)))
	bugrepOutbox.mu.Unlock()
	// A report Submit could not send goes into the outbox: the outbox must not
	// post it again on the next frame.
	if nextFrame || !later {
		t.Fatalf("after a failed Submit: due next frame %v, due after the backoff %v", nextFrame, later)
	}
}

func TestBugrepSetupResponseRestartsAStoppedOutbox(t *testing.T) {
	bugrepTestOutbox(t)
	if err := bugrepQueue(bugrepReport{Schema: 1, ID: "JH-HH0000", Created: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	bugrepCollectorUnavailable()
	now := time.Now()
	bugrepNoteServiceSetupPending(now)
	bugrepOutbox.mu.Lock()
	stopped := bugrepOutbox.stopped
	tooSoon := bugrepOutboxDueLocked(now.Add(time.Second))
	due := bugrepOutboxDueLocked(now.Add(bugrepOutboxDelay(1)))
	bugrepOutbox.mu.Unlock()
	if stopped || tooSoon || !due {
		t.Fatalf("a setup response left the outbox stopped %v, due too soon %v, due later %v", stopped, tooSoon, due)
	}
}

func TestBugrepQueuedSheetOffersPhoneLink(t *testing.T) {
	withTempPatchState(t)
	previousSheet, previousScene := tbxSheet, currentSceneIndex
	t.Cleanup(func() {
		tbxSheet, currentSceneIndex = previousSheet, previousScene
	})
	config := &Config{Scenes: []SceneConfig{{Name: bugrepSceneName}}}
	currentSceneIndex = 0
	report := bugrepReport{ID: "JH-PHONE1", Title: "Menu is blank", Created: time.Now().UTC().Format(time.RFC3339)}
	bugrepQueuedSheet(config, report, bugrepHoldSetup, false)
	if len(tbxSheet.spec.buttons) != 2 || tbxSheet.spec.buttons[0].label != "Use phone" ||
		tbxSheet.spec.buttons[1].label != "Done" {
		t.Fatalf("queued sheet buttons = %+v", tbxSheet.spec.buttons)
	}
	tbxSheet.spec.buttons[0].run()
	if tbxSheet.spec.title != "Send it from your phone" || len(tbxSheet.spec.qr) == 0 {
		t.Fatal("Use phone did not open a prefilled GitHub QR code")
	}
}

func TestBugrepWhereSaysWhereTheReportGoes(t *testing.T) {
	service := bugrepDefaultEndpoint
	if line := bugrepWhereLine(service, false); !strings.Contains(line, "public GitHub issue") || strings.Contains(line, "phone") {
		t.Fatalf("with the report service: %q", line)
	}
	if line := bugrepWhereLine("", false); !strings.Contains(line, "phone") {
		t.Fatalf("without a collector: %q", line)
	}
	if line := bugrepWhereLine(service, true); !strings.Contains(line, "next start") {
		t.Fatalf("while the service is not answering: %q", line)
	}
	if text := bugrepWhereText(service); !strings.HasPrefix(text, "## Where it goes\n") || !strings.Contains(text, "public") {
		t.Fatalf("preview section: %q", text)
	}
	// Another collector is named by its host; nothing is promised about it.
	other := "https://reports.example/v1/bug"
	for _, text := range []string{bugrepWhereLine(other, false), bugrepWhereText(other), bugrepWhereLine(other, true)} {
		if !strings.Contains(text, "reports.example") || strings.Contains(text, "GitHub issue") || strings.Contains(text, "public issue") {
			t.Fatalf("for another collector: %q", text)
		}
	}
}
