package main

// Tests for Report a bug's sending side: where reports go, what is scrubbed
// before anything leaves the device, how failures are told apart (offline,
// wrong clock, no certificate store, refused), and the outbox that holds
// reports while the device is offline.

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBugrepEndpointPrecedence(t *testing.T) {
	prevBuild, prevTele := bugrepEndpoint, telemetryEndpoint
	t.Cleanup(func() { bugrepEndpoint, telemetryEndpoint = prevBuild, prevTele })
	t.Setenv("JUKAHUB_BUGREPORT_URL", "")
	t.Setenv("JUKAHUB_TELEMETRY_URL", "")
	bugrepEndpoint, telemetryEndpoint = "", ""
	if got := bugrepEndpointFor(nil); got != "" {
		t.Fatalf("no collector is configured, got %q", got)
	}
	telemetryEndpoint = "https://telemetry.example/v1"
	if got := bugrepEndpointFor(nil); got != telemetryEndpoint {
		t.Fatalf("telemetry fallback = %q", got)
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

func TestBugrepClipKeepsRunesWhole(t *testing.T) {
	if got := bugrepClip("short", 10); got != "short" {
		t.Fatalf("clip of a short text = %q", got)
	}
	got := bugrepClip("héllo wörld", 2)
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "h\n") {
		t.Fatalf("clip cut a rune in half: %q", got)
	}
}

func TestBugrepBuildHonoursTheToggles(t *testing.T) {
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
	text = bugrepPreviewText(r)
	for _, want := range []string{"(JukaHub-20261101000000)", "## Diagnostics (redacted)\nfps 60", "## Recent log (redacted)\n[ui] opened"} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview lacks %q:\n%s", want, text)
		}
	}
}

func TestBugrepRefs(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		ref := bugrepNewRef()
		if !strings.HasPrefix(ref, "JH-") || len(ref) != 9 || !bugrepRefValid(ref) {
			t.Fatalf("reference %q is not JH- plus six characters", ref)
		}
		for _, c := range ref[3:] {
			if !strings.ContainsRune(bugrepRefAlphabet, c) {
				t.Fatalf("reference %q uses %q, a look-alike character", ref, c)
			}
		}
		seen[ref] = true
	}
	if len(seen) < 60 {
		t.Fatalf("only %d distinct references in 64", len(seen))
	}
	for _, bad := range []string{"", "JH 123", "../etc", "JH_123", strings.Repeat("A", 25)} {
		if bugrepRefValid(bad) {
			t.Errorf("accepted reference %q", bad)
		}
	}
}

func TestBugrepClockCheck(t *testing.T) {
	prev := buildDate
	t.Cleanup(func() { buildDate = prev })
	buildDate = ""
	if !bugrepClockWrong(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("a clock reset to 1970 was accepted")
	}
	if bugrepClockWrong(time.Now()) {
		t.Fatal("the current time was called wrong")
	}
	buildDate = "2026-06-01"
	if !bugrepClockWrong(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("a clock before the build date was accepted")
	}
	if bugrepClockWrong(time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("a clock within two days of the build date was called wrong (time zones)")
	}
}

func TestBugrepClassifyError(t *testing.T) {
	prev := buildDate
	t.Cleanup(func() { buildDate = prev })
	buildDate = ""
	wrap := func(err error) error {
		return &url.Error{Op: "Post", URL: "https://reports.example/bug", Err: err}
	}
	cases := []struct {
		err  error
		want bugrepSendErr
	}{
		{wrap(x509.UnknownAuthorityError{}), bugrepErrTrust},
		{wrap(x509.CertificateInvalidError{Reason: x509.Expired}), bugrepErrClock},
		{wrap(x509.CertificateInvalidError{Reason: x509.NotAuthorizedToSign}), bugrepErrTrust},
		{errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), bugrepErrTrust},
		{errors.New("x509: certificate has expired or is not yet valid: current time 1970-01-01"), bugrepErrClock},
		{wrap(errors.New("dial tcp: lookup reports.example: no such host")), bugrepErrOffline},
	}
	for i, c := range cases {
		if got := bugrepClassifyError(c.err); got != c.want {
			t.Errorf("case %d (%v) = %v, want %v", i, c.err, got, c.want)
		}
	}
}

func TestBugrepPostUsesTheCollectorsReference(t *testing.T) {
	type received struct {
		report      bugrepReport
		contentType string
	}
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rep bugrepReport
		_ = json.NewDecoder(r.Body).Decode(&rep)
		got <- received{report: rep, contentType: r.Header.Get("Content-Type")}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"SRV-42"}`)
	}))
	defer srv.Close()
	report := bugrepReport{Schema: 1, Kind: "bug_report", App: "JukaHub", ID: "JH-ABCDEF", Category: "ui", Version: "1.0.0"}
	ref, kind, err := bugrepPost(srv.URL, report)
	if err != nil || kind != bugrepErrNone {
		t.Fatalf("post = %v (%v)", err, kind)
	}
	if ref != "SRV-42" {
		t.Fatalf("reference = %q, want the collector's", ref)
	}
	select {
	case rec := <-got:
		if rec.report.ID != "JH-ABCDEF" || rec.report.Kind != "bug_report" || !strings.Contains(rec.contentType, "application/json") {
			t.Fatalf("collector received %+v (%s)", rec.report, rec.contentType)
		}
	default:
		t.Fatal("the collector received nothing")
	}
}

func TestBugrepPostClassifiesAnswers(t *testing.T) {
	var status atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	report := bugrepReport{ID: "JH-ABCDEF", Version: "1.0.0"}
	for code, want := range map[int]bugrepSendErr{
		http.StatusNoContent:             bugrepErrNone,
		http.StatusBadRequest:            bugrepErrRejected,
		http.StatusRequestEntityTooLarge: bugrepErrRejected,
		http.StatusTooManyRequests:       bugrepErrServer,
		http.StatusServiceUnavailable:    bugrepErrServer,
	} {
		status.Store(int32(code))
		ref, kind, _ := bugrepPost(srv.URL, report)
		if kind != want {
			t.Errorf("HTTP %d = %v, want %v", code, kind, want)
		}
		if want == bugrepErrNone && ref != "JH-ABCDEF" {
			t.Errorf("a 2xx without an id must keep the device's reference, got %q", ref)
		}
	}
	// A collector that is not there reads as offline, so the report is queued.
	srv.Close()
	if _, kind, err := bugrepPost(srv.URL, report); err == nil || kind != bugrepErrOffline {
		t.Fatalf("closed collector = %v (%v), want offline", err, kind)
	}
}

// bugrepWaitIdle waits for a background flush to finish.
func bugrepWaitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		bugrepOutbox.mu.Lock()
		busy := bugrepOutbox.flushing
		bugrepOutbox.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the outbox flush did not finish")
}

// Reports wait in the outbox while sending fails, a refused report is dropped
// instead of retried forever, and a delivered one leaves the folder.
func TestBugrepOutboxQueuesAndFlushes(t *testing.T) {
	dir := t.TempDir()
	prevDir, prevPost := bugrepOutboxDirOverride, bugrepPostFunc
	bugrepOutboxDirOverride = dir
	t.Cleanup(func() {
		bugrepOutboxDirOverride, bugrepPostFunc = prevDir, prevPost
		bugrepOutbox.mu.Lock()
		bugrepOutbox.count, bugrepOutbox.scanned, bugrepOutbox.lastTry = 0, false, time.Time{}
		bugrepOutbox.mu.Unlock()
	})

	older := bugrepReport{Schema: 1, ID: "JH-AAAAAA", Category: "crash", Created: "2026-11-01T10:00:00Z"}
	newer := bugrepReport{Schema: 1, ID: "JH-BBBBBB", Category: "ui", Created: "2026-11-02T10:00:00Z"}
	for _, r := range []bugrepReport{newer, older} {
		if err := bugrepQueue(r); err != nil {
			t.Fatalf("queue %s: %v", r.ID, err)
		}
	}
	// Files that are not reports, or whose name does not match their id, are
	// left alone.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "JH-CCCCCC.json"), []byte(`{"id":"JH-DDDDDD"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	queued := bugrepQueued()
	if len(queued) != 2 || queued[0].ID != older.ID || queued[1].ID != newer.ID {
		t.Fatalf("queued = %+v, want the two reports, oldest first", queued)
	}
	if n := bugrepOutboxCount(); n != 2 {
		t.Fatalf("outbox count = %d, want 2", n)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, older.ID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("outbox file mode = %o, want 600", perm)
		}
	}

	// Offline: the first attempt fails and the rest are not tried.
	var calls atomic.Int32
	bugrepPostFunc = func(endpoint string, r bugrepReport) (string, bugrepSendErr, error) {
		calls.Add(1)
		return "", bugrepErrOffline, errors.New("network is unreachable")
	}
	bugrepFlush("https://reports.example/bug", nil)
	bugrepWaitIdle(t)
	if calls.Load() != 1 || bugrepOutboxCount() != 2 {
		t.Fatalf("offline flush: %d attempt(s), %d left; want 1 attempt and both kept", calls.Load(), bugrepOutboxCount())
	}

	// Back online: the collector refuses the older report and takes the newer.
	bugrepPostFunc = func(endpoint string, r bugrepReport) (string, bugrepSendErr, error) {
		if r.ID == older.ID {
			return "", bugrepErrRejected, errors.New("HTTP 400")
		}
		return r.ID, bugrepErrNone, nil
	}
	sent := make(chan int, 1)
	bugrepFlush("https://reports.example/bug", func(n int) { sent <- n })
	bugrepWaitIdle(t)
	got := -1
	for deadline := time.Now().Add(5 * time.Second); got < 0 && time.Now().Before(deadline); {
		tbxDrainUI()
		select {
		case got = <-sent:
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got != 1 {
		t.Fatalf("done reported %d sent, want 1", got)
	}
	if n := bugrepOutboxCount(); n != 0 || len(bugrepQueued()) != 0 {
		t.Fatalf("outbox still holds %d report(s)", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "JH-CCCCCC.json")); err != nil {
		t.Fatal("a file that is not a queued report was removed")
	}
}

// The phone path's link always ends, even when the title alone is over the
// budget, and an empty title still reads well.
func TestBugrepIssueURLAlwaysEnds(t *testing.T) {
	r := bugrepReport{
		ID: "JH-ABCDEF", Version: "1.0.0", Device: "TrimUI Smart Pro", OS: "linux", Arch: "arm64",
		Title:       strings.Repeat("Very long title ", 7),
		Description: strings.Repeat("words ", 400),
	}
	done := make(chan string, 1)
	go func() { done <- bugrepIssueURL(r, 40) }()
	select {
	case raw := <-done:
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		body := u.Query().Get("body")
		if strings.Contains(body, "words words") {
			t.Fatalf("the description was not dropped to fit: %q", body)
		}
		if !strings.Contains(body, "JH-ABCDEF") {
			t.Fatalf("the reference was dropped: %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bugrepIssueURL did not finish with a budget smaller than the title")
	}

	u, err := url.Parse(bugrepIssueURL(bugrepReport{ID: "JH-ABCDEF"}, 900))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("title"); got != "[Bug] Other problem" {
		t.Fatalf("title for an empty report = %q", got)
	}
}
