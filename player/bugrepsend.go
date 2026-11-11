package main

// bugrepsend.go — getting a bug report off the device.
//
// A report is the user's own words (category, title, description) plus, when
// they leave it on, the redacted diagnostics report and the redacted tail of
// the log. Before anything is sent it is scrubbed again here: credentials,
// account names, absolute paths, IP and MAC addresses, e-mail addresses and
// URL paths are removed, so the payload carries no personal data, no tokens
// and no paths. The preview sheet shows exactly this text.
//
// Where it goes:
//
//   - Straight from the device to a collector, as a JSON POST: the
//     JUKAHUB_BUGREPORT_URL variable, a "bugReportURL" in the config, -ldflags
//     "-X main.bugrepEndpoint=https://…", the telemetry collector, or else the
//     JukaHub report service (bugrepDefaultEndpoint) — a function on the
//     builder's site, netlify/functions/bug-report.mjs, that files the report
//     as a public GitHub issue with the maintainers' token. Any 2xx is success;
//     a {"id", "issue", "url"} answer names the reference and the issue.
//   - Through the user's phone only when the device cannot send it: a QR code
//     opens a prefilled GitHub issue (a handheld has no browser of its own).
//     Reports used to take this path every time, because no build carried a
//     collector.
//
// The handheld is where sending usually fails, so each failure has its own
// answer: no network → the report waits in an outbox file under the app data
// folder and goes out on its own when the device is back online ("Queued");
// a clock reset to 1970 makes every certificate look invalid → "Set the date
// and time first"; a firmware without a certificate store → a CA bundle shipped
// next to the app or a firmware one is used, and if none exists the phone path
// is offered instead; a collector that is not there (no function, not set up)
// → the phone path, or the outbox with one try per start. The outbox backs off
// while the collector keeps failing and retires a report it could not deliver
// in a month to a file, so no report is lost and none is retried forever.

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// bugrepEndpoint is set by a release build that has a collector of its own:
// -ldflags "-X main.bugrepEndpoint=https://reports.example/v1/bug".
var bugrepEndpoint string

// bugrepDefaultEndpoint is the JukaHub report service every build uses when
// nothing else is configured: a function on the builder's site
// (netlify/functions/bug-report.mjs, docs/BUG_REPORTS.md) that files each
// report as a GitHub issue. It is a var so tests can take it away.
var bugrepDefaultEndpoint = "https://generate.jukalang.com/api/bug-report"

// bugrepConfigKey is the config/user setting that points reports elsewhere.
const bugrepConfigKey = "bugReportURL"

// bugrepEndpointFor picks the collector from the environment, the config's
// custom block, the build, the telemetry collector, or the JukaHub report
// service, in that order. Only https is accepted (http only for a collector on
// this machine, for testing).
func bugrepEndpointFor(custom map[string]interface{}) string {
	candidates := []string{strings.TrimSpace(os.Getenv("JUKAHUB_BUGREPORT_URL"))}
	if custom != nil {
		if v, ok := custom[bugrepConfigKey].(string); ok {
			candidates = append(candidates, strings.TrimSpace(v))
		}
	}
	candidates = append(candidates, strings.TrimSpace(bugrepEndpoint), telemetryEndpointURL(),
		strings.TrimSpace(bugrepDefaultEndpoint))
	for _, c := range candidates {
		if bugrepEndpointAllowed(c) {
			return c
		}
	}
	return ""
}

// bugrepEndpointAllowed accepts an https URL, or http to localhost.
func bugrepEndpointAllowed(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}
	return false
}

// bugrepEndpointURL is the collector for the live config. Main thread only:
// it reads the config's custom map.
func bugrepEndpointURL() string {
	var custom map[string]interface{}
	if appConfig != nil {
		custom = appConfig.Variables.Custom
	}
	return bugrepEndpointFor(custom)
}

// bugrepIsDefaultEndpoint reports whether reports go to the JukaHub report
// service rather than a collector this build or its settings chose.
func bugrepIsDefaultEndpoint(endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	return endpoint != "" && endpoint == strings.TrimSpace(bugrepDefaultEndpoint)
}

// bugrepServiceName names the collector a report goes to, for a sentence: "the
// JukaHub report service", or "the report collector at <host>" for one the
// environment, the config, the build or the telemetry setting named — whose
// handling of a report this app cannot promise anything about.
func bugrepServiceName(endpoint string) string {
	if endpoint == "" || bugrepIsDefaultEndpoint(endpoint) {
		return "the JukaHub report service"
	}
	if u, err := url.Parse(strings.TrimSpace(endpoint)); err == nil && u.Hostname() != "" {
		return "the report collector at " + u.Hostname()
	}
	return "the report collector this build names"
}

// ── Payload ──────────────────────────────────────────────────────────────

// bugrepCategories are the chips on the form, in order.
var bugrepCategories = [...]string{"Crash", "UI", "Controls", "Media", "Network", "Other"}

// bugrepReport is the wire format. It is also the outbox file format.
type bugrepReport struct {
	Schema      int    `json:"schema"`
	Kind        string `json:"kind"`
	App         string `json:"app"`
	ID          string `json:"id"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Release     string `json:"release,omitempty"`
	Device      string `json:"device"`
	Platform    string `json:"platform"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Diagnostics string `json:"diagnostics,omitempty"`
	Log         string `json:"log,omitempty"`
	Created     string `json:"created"`
}

const (
	bugrepTitleMax       = 120
	bugrepDescriptionMax = 4000
	bugrepDiagnosticsMax = 16 << 10
	bugrepLogMax         = 48 << 10
)

var (
	bugrepIPv4  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`) })
	bugrepMAC   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\b[0-9A-Fa-f]{2}(?:[:-][0-9A-Fa-f]{2}){5}\b`) })
	bugrepEmail = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`) })
	// bugrepURL matches a link in any letter case ("HTTPS://…" too); its host
	// stays, its path and query go.
	bugrepURL = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)\b(https?|rtsp|rtmp|ftp|sftp)://([^/\s"'<>]+)[^\s"'<>]*`)
	})
	// bugrepWWW is a link written without a scheme, "www.host/path?token=…":
	// the host stays, the rest goes, as for bugrepURL.
	bugrepWWW    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?i)\b(www\.[^/\s"'<>]+)/[^\s"'<>]*`) })
	bugrepWinAbs = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\b[A-Za-z]:\\[^\s"'<>|]*`) })
	bugrepUnix   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?:^|[\s"'=(])(/(?:mnt|home|root|userdata|storage|media|tmp|var|opt|usr|etc|sdcard|data|Users|Volumes|private)(?:/[^\s"'<>)]*)?)`) })
)

// bugrepScrub removes what must never leave the device from text about to be
// sent: credentials and account names (redactDiagnosticText), then URL paths
// (with or without a scheme, in any letter case), e-mail, IP and MAC addresses
// and absolute paths.
func bugrepScrub(text string) string {
	text = redactDiagnosticText(text)
	text = bugrepURL().ReplaceAllString(text, "$1://$2/…")
	text = bugrepWWW().ReplaceAllString(text, "$1/…")
	text = bugrepEmail().ReplaceAllString(text, "[email]")
	text = bugrepMAC().ReplaceAllString(text, "[mac]")
	text = bugrepIPv4().ReplaceAllStringFunc(text, func(ip string) string {
		if ip == "0.0.0.0" || ip == "127.0.0.1" || ip == "1.1.1.1" || ip == "8.8.8.8" {
			return ip
		}
		return "[ip]"
	})
	text = bugrepWinAbs().ReplaceAllString(text, "[path]")
	text = bugrepUnix().ReplaceAllStringFunc(text, func(m string) string {
		// Keep the delimiter the match started with.
		if len(m) > 0 && m[0] != '/' {
			return m[:1] + "[path]"
		}
		return "[path]"
	})
	return text
}

// bugrepClip bounds s to max bytes on a rune boundary.
func bugrepClip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n… (cut to fit)"
}

// bugrepRefAlphabet avoids look-alike characters, like the party codes.
const bugrepRefAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// bugrepNewRef is the reference shown after sending: "JH-7K2Q9M".
func bugrepNewRef() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("JH-%06d", time.Now().UnixNano()%1000000)
	}
	out := make([]byte, len(buf))
	for i, b := range buf {
		out[i] = bugrepRefAlphabet[int(b)%len(bugrepRefAlphabet)]
	}
	return "JH-" + string(out)
}

// bugrepForm is what the user filled in.
type bugrepForm struct {
	Category    int
	Title       string
	Description string
	Diagnostics bool
	Log         bool
}

// bugrepBuild assembles the report from the form. Main thread: it reads the
// config for the diagnostics report.
func bugrepBuild(config *Config, form bugrepForm, now time.Time) bugrepReport {
	cat := "Other"
	if form.Category >= 0 && form.Category < len(bugrepCategories) {
		cat = bugrepCategories[form.Category]
	}
	version := "unknown"
	if config != nil {
		version = versionString(config)
	}
	release := installedReleaseTag()
	if release == "" {
		release = readInstalledReleaseInfo().Version
	}
	r := bugrepReport{
		Schema:      1,
		Kind:        "bug_report",
		App:         "JukaHub",
		ID:          bugrepNewRef(),
		Category:    strings.ToLower(cat),
		Title:       bugrepClip(bugrepScrub(strings.TrimSpace(form.Title)), bugrepTitleMax),
		Description: bugrepClip(bugrepScrub(strings.TrimSpace(form.Description)), bugrepDescriptionMax),
		Version:     version,
		Release:     release,
		Device:      deviceDisplayName(DetectDevice()),
		Platform:    P().Name(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Created:     now.UTC().Format(time.RFC3339),
	}
	if form.Diagnostics {
		r.Diagnostics = bugrepClip(bugrepScrub(DiagnosticsText(config)), bugrepDiagnosticsMax)
	}
	if form.Log {
		tail := diagnosticLogTail(appLog.String())
		if strings.TrimSpace(tail) == "" {
			tail = "(the log is empty — the app may have just started)"
		}
		r.Log = bugrepClip(bugrepScrub(tail), bugrepLogMax)
	}
	return r
}

// bugrepPreviewText is the exact content the preview sheet shows.
func bugrepPreviewText(r bugrepReport) string {
	var sb strings.Builder
	sb.WriteString("## Your report\n")
	sb.WriteString("Category: " + r.Category + "\n")
	title := r.Title
	if title == "" {
		title = "(no title)"
	}
	sb.WriteString("Title: " + title + "\n")
	if r.Description != "" {
		sb.WriteString("\n" + r.Description + "\n")
	}
	sb.WriteString("\n## Device facts\n")
	sb.WriteString("JukaHub " + r.Version)
	if r.Release != "" {
		sb.WriteString(" (" + r.Release + ")")
	}
	sb.WriteString("\n" + r.Device + " · " + r.Platform + " · " + r.OS + "/" + r.Arch + "\n")
	// The issue's facts table shows these two as well, so the preview does.
	if r.ID != "" {
		sb.WriteString("Reference: " + r.ID + "\n")
	}
	if r.Created != "" {
		sb.WriteString("Created: " + r.Created + "\n")
	}
	if r.Diagnostics != "" {
		sb.WriteString("\n## Diagnostics (redacted)\n" + r.Diagnostics + "\n")
	} else {
		sb.WriteString("\n## Diagnostics\nNot included.\n")
	}
	if r.Log != "" {
		sb.WriteString("\n## Recent log (redacted)\n" + r.Log + "\n")
	}
	sb.WriteString("\n## Never included\nNames, account details, tokens and keys, file paths, network names and addresses, and your config values.\n")
	return sb.String()
}

// ── Sending ──────────────────────────────────────────────────────────────

// bugrepSendErr classifies a failed send for the screen.
type bugrepSendErr int

const (
	bugrepErrNone bugrepSendErr = iota
	// bugrepErrOffline: no route to the collector; keep it queued.
	bugrepErrOffline
	// bugrepErrClock: the device clock makes certificates look invalid.
	bugrepErrClock
	// bugrepErrTrust: no certificate store to verify the collector.
	bugrepErrTrust
	// bugrepErrRejected: the collector refused this report; retrying will not
	// help.
	bugrepErrRejected
	// bugrepErrServer: the collector failed; retry later.
	bugrepErrServer
	// bugrepErrUnavailable: there is no collector at that address, or it is
	// not set up (404, 405, 410, 501, 503, a web page instead of an answer,
	// or {"fallback": "phone"}). Retrying in a minute will not help; the phone
	// can, or the next start.
	bugrepErrUnavailable
)

// bugrepClockFloor is the earliest plausible "now": the build date when the
// build carries one. A handheld whose clock reset to 1970 fails every
// certificate check, and saying so is better than a TLS error.
func bugrepClockFloor() time.Time {
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(buildDate)); err == nil {
		return t.Add(-48 * time.Hour)
	}
	return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
}

// bugrepClockWrong reports a device clock set before the build existed.
func bugrepClockWrong(now time.Time) bool {
	return now.Before(bugrepClockFloor())
}

// bugrepPlausibleFloor is the earliest creation time a queued report can
// honestly carry: JukaHub's report format did not exist before it, so a report
// stamped earlier was made while the clock was wrong and has no age to judge.
// It is fixed on purpose. The build date (bugrepClockFloor) moves with every
// update, and judged by it every report queued before an update looked
// clock-wrong and was retried for ever instead of retiring after a month.
var bugrepPlausibleFloor = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// bugrepCAPaths are certificate bundles used when the system store is empty:
// one shipped next to the app first, then the usual firmware locations.
func bugrepCAPaths() []string {
	var out []string
	if dir := exeDir(); dir != "" {
		out = append(out,
			filepath.Join(dir, "cacert.pem"),
			filepath.Join(dir, "certs", "cacert.pem"),
			filepath.Join(dir, "certs", "ca-certificates.crt"),
		)
	}
	return append(out,
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/ssl/cert.pem",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/usr/share/ca-certificates/cacert.pem",
		"/mnt/SDCARD/System/etc/ssl/certs/ca-certificates.crt",
	)
}

var bugrepCAs struct {
	once sync.Once
	pool *x509.CertPool
}

// bugrepRootCAs returns a pool to verify the collector with, or nil to use the
// system store as-is. It only builds its own pool when the system store is
// empty, which is the case on firmwares without a CA bundle.
func bugrepRootCAs() *x509.CertPool {
	bugrepCAs.once.Do(func() {
		if pool, err := x509.SystemCertPool(); err == nil && pool != nil && !pool.Equal(x509.NewCertPool()) {
			return
		}
		pool := x509.NewCertPool()
		for _, p := range bugrepCAPaths() {
			if data, err := os.ReadFile(p); err == nil && pool.AppendCertsFromPEM(data) {
				bugrepCAs.pool = pool
				return
			}
		}
	})
	return bugrepCAs.pool
}

// bugrepHTTPClient is the client reports are sent with. Reports are rare, so
// each send uses a fresh transport with keep-alives off: no idle connection or
// its goroutines outlive the request.
func bugrepHTTPClient() *http.Client {
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}
	if pool := bugrepRootCAs(); pool != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: 25 * time.Second, Transport: tr, CheckRedirect: bugrepCheckRedirect}
}

// bugrepCheckRedirect follows a redirect only to an address a collector may
// have (https, or http on this machine), only twice, never from https down to
// plain http, and only while the request is still the POST carrying the report:
// a 301, 302 or 303 turns it into a GET without a body, and reading "taken"
// from the answer to that would drop a report nobody received. Anything else
// stops at the redirect itself, which bugrepReadAnswer reads as "no collector
// here".
func bugrepCheckRedirect(req *http.Request, via []*http.Request) error {
	if req.Method != http.MethodPost || len(via) > 2 || !bugrepEndpointAllowed(req.URL.String()) {
		return http.ErrUseLastResponse
	}
	if len(via) > 0 && strings.EqualFold(via[0].URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
		return http.ErrUseLastResponse
	}
	return nil
}

// bugrepSent is what the collector said about a report it took.
type bugrepSent struct {
	// Ref is the reference to show: the collector's own when it gave one.
	Ref string
	// Issue is the number of the public issue the report service filed, 0
	// when the collector did not say.
	Issue int
	// URL is that issue's address, "" unless it is an issue on GitHub (the
	// only address the sent sheet turns into a code).
	URL string
}

// bugrepPostFunc is a test seam over bugrepPost.
var bugrepPostFunc = bugrepPost

// bugrepEncode is the report as the collector receives it. json.Marshal's
// HTML-safe escaping would turn every <, > and & of a log into six bytes, and
// a log full of them could push a valid report past the collector's size cap,
// which refuses (and so retires) it.
func bugrepEncode(r bugrepReport) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// bugrepPost sends one report. It returns what the collector said about it
// and a classification of any failure.
func bugrepPost(endpoint string, r bugrepReport) (bugrepSent, bugrepSendErr, error) {
	if bugrepClockWrong(time.Now()) {
		return bugrepSent{}, bugrepErrClock, errors.New("the device clock is wrong")
	}
	body, err := bugrepEncode(r)
	if err != nil {
		return bugrepSent{}, bugrepErrRejected, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return bugrepSent{}, bugrepErrRejected, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "JukaHub-BugReport/"+r.Version)
	resp, err := bugrepHTTPClient().Do(req)
	if err != nil {
		return bugrepSent{}, bugrepClassifyError(err), err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return bugrepReadAnswer(resp.StatusCode, resp.Header.Get("Content-Type"), data, r.ID)
}

// bugrepAnswer is the collector's JSON answer. Every field is optional: a
// collector that answers 204 with no body has still taken the report.
type bugrepAnswer struct {
	ID       string `json:"id"`
	Issue    int    `json:"issue"`
	URL      string `json:"url"`
	Fallback string `json:"fallback"`
	Error    string `json:"error"`
}

// bugrepReadAnswer reads the collector's answer to report ref: what to show
// when it took the report, or which kind of failure it was.
//
// Only an answer in JSON can refuse a report, because a refusal retires it: a
// web page (a host without the function, a captive portal, a proxy's or a
// firewall's error page) or a bare 4xx with no explanation is somebody else
// answering, so the report is kept and the device treats the address as having
// no collector right now.
func bugrepReadAnswer(status int, contentType string, data []byte, ref string) (bugrepSent, bugrepSendErr, error) {
	var answer bugrepAnswer
	trimmed := bytes.TrimSpace(data)
	isJSON := len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
	if isJSON {
		// A field of an unexpected type leaves only that field empty.
		_ = json.Unmarshal(trimmed, &answer)
	}
	if strings.EqualFold(strings.TrimSpace(answer.Fallback), "phone") {
		return bugrepSent{}, bugrepErrUnavailable, fmt.Errorf("the report service is not set up (HTTP %d)", status)
	}
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout:
		// Busy or failing for now, whoever answered — a gateway's own error
		// page included: the outbox backs off and tries again rather than
		// giving up on the collector until the next start.
		return bugrepSent{}, bugrepErrServer, fmt.Errorf("the report service answered HTTP %d", status)
	}
	if strings.Contains(strings.ToLower(contentType), "text/html") || (!isJSON && bytes.HasPrefix(trimmed, []byte("<"))) {
		return bugrepSent{}, bugrepErrUnavailable, fmt.Errorf("the report address answered with a web page (HTTP %d)", status)
	}
	switch {
	case status >= 200 && status < 300:
		// An empty 204 or a plain "ok" has taken the report too; only the
		// optional fields are missing then.
		sent := bugrepSent{Ref: ref}
		if bugrepRefValid(answer.ID) {
			sent.Ref = answer.ID
		}
		if answer.Issue > 0 && answer.Issue < 1_000_000_000 {
			sent.Issue = answer.Issue
		}
		if bugrepIssueURLValid(answer.URL) {
			sent.URL = answer.URL
		}
		return sent, bugrepErrNone, nil
	case status >= 300 && status < 400:
		// A redirect that was not followed (bugrepCheckRedirect), whatever its
		// body says: the report did not reach a collector.
		return bugrepSent{}, bugrepErrUnavailable, fmt.Errorf("the report address redirected elsewhere (HTTP %d)", status)
	case status == http.StatusNotFound, status == http.StatusMethodNotAllowed, status == http.StatusGone,
		status == http.StatusNotImplemented, status == http.StatusServiceUnavailable:
		return bugrepSent{}, bugrepErrUnavailable, fmt.Errorf("the report service is not available (HTTP %d)", status)
	case status >= 500:
		return bugrepSent{}, bugrepErrServer, fmt.Errorf("the report service answered HTTP %d", status)
	case !isJSON:
		// An unexplained 4xx: not the collector's refusal.
		return bugrepSent{}, bugrepErrUnavailable, fmt.Errorf("the report address answered HTTP %d without a report service's answer", status)
	case status != http.StatusBadRequest && status != http.StatusForbidden && status != http.StatusRequestEntityTooLarge &&
		status != http.StatusUnsupportedMediaType && status != http.StatusUnprocessableEntity:
		// Only the refusals the report service documents retire a report; a
		// JSON 401 or 407 from a proxy or a gateway in between is somebody
		// else answering.
		return bugrepSent{}, bugrepErrUnavailable, fmt.Errorf("the report address answered HTTP %d without a report service's answer", status)
	}
	if why := bugrepAnswerError(answer.Error); why != "" {
		return bugrepSent{}, bugrepErrRejected, fmt.Errorf("the report service refused the report: %s (HTTP %d)", why, status)
	}
	return bugrepSent{}, bugrepErrRejected, fmt.Errorf("the report service refused the report (HTTP %d)", status)
}

// bugrepAnswerError is a collector's reason for refusing, fit for a toast: one
// line of printable text, at most 120 bytes, or "" when it sent none.
func bugrepAnswerError(s string) string {
	s = strings.Join(strings.Fields(firstLine(s)), " ")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(bugrepClipRunes(s, 120))
}

// bugrepClipRunes bounds s to max bytes on a rune boundary, without a marker.
func bugrepClipRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}

// bugrepIssueURLValid accepts the address of an issue on GitHub.
func bugrepIssueURLValid(raw string) bool {
	if raw == "" || len(raw) > 300 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && strings.EqualFold(u.Host, "github.com") && strings.Contains(u.Path, "/issues/")
}

// bugrepRefValid accepts a short reference made of letters, digits and dashes.
func bugrepRefValid(ref string) bool {
	if ref == "" || len(ref) > 24 {
		return false
	}
	for _, c := range ref {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// bugrepClassifyError maps a transport error to what the screen should say.
func bugrepClassifyError(err error) bugrepSendErr {
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		if bugrepClockWrong(time.Now()) {
			return bugrepErrClock
		}
		return bugrepErrTrust
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		if invalid.Reason == x509.Expired {
			return bugrepErrClock
		}
		return bugrepErrTrust
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "certificate has expired or is not yet valid") {
		return bugrepErrClock
	}
	if strings.Contains(lower, "certificate signed by unknown authority") {
		return bugrepErrTrust
	}
	return bugrepErrOffline
}

// ── Outbox ───────────────────────────────────────────────────────────────

// bugrepOutboxDirOverride is a test seam.
var bugrepOutboxDirOverride string

// bugrepOutboxDir is where reports wait for the network: data/bugreports/outbox
// in the app's data folder.
func bugrepOutboxDir() string {
	if dir := strings.TrimSpace(bugrepOutboxDirOverride); dir != "" {
		return dir
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "bugreports", "outbox")
}

var bugrepOutbox struct {
	mu       sync.Mutex
	count    int
	scanned  bool
	flushing bool
	lastTry  time.Time
	// failures counts the flushes in a row the network or the collector
	// failed; each one doubles the wait before the next (bugrepOutboxDelay).
	failures int
	// stopped is set when the collector is not there at all (no function, not
	// set up) or cannot be trusted on this firmware: queued reports wait for
	// the next start instead of asking a dead address every few minutes.
	stopped bool
}

// bugrepQueue writes a report to the outbox (mode 0600).
func bugrepQueue(r bugrepReport) error {
	dir := bugrepOutboxDir()
	if dir == "" {
		return errors.New("no data folder for the outbox")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, r.ID+".json")
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	bugrepRecount()
	return nil
}

// bugrepSettled holds the reports that left the outbox this session — sent, or
// retired — whether or not their file could be removed. The outbox skips them,
// so a card remounted read-only cannot make a sent report go out (and be
// announced) again every few minutes. It is not saved: after a restart such a
// report is sent once more, and the collector recognises it as filed.
var bugrepSettled struct {
	mu  sync.Mutex
	ids map[string]bool
}

// bugrepSettle marks report id as gone from the outbox for this session.
func bugrepSettle(id string) {
	bugrepSettled.mu.Lock()
	if bugrepSettled.ids == nil {
		bugrepSettled.ids = map[string]bool{}
	}
	bugrepSettled.ids[id] = true
	bugrepSettled.mu.Unlock()
}

// bugrepIsSettled reports whether report id already left the outbox.
func bugrepIsSettled(id string) bool {
	bugrepSettled.mu.Lock()
	defer bugrepSettled.mu.Unlock()
	return bugrepSettled.ids[id]
}

// bugrepRemove and bugrepSaveCopyFunc are test seams over os.Remove and
// bugrepSaveCopy, so a full or read-only card can be played in a test.
var (
	bugrepRemove       = os.Remove
	bugrepSaveCopyFunc = bugrepSaveCopy
)

// bugrepUnqueue removes a sent report.
func bugrepUnqueue(id string) {
	dir := bugrepOutboxDir()
	if dir == "" || !bugrepRefValid(id) {
		return
	}
	bugrepSettle(id)
	if err := bugrepRemove(filepath.Join(dir, id+".json")); err != nil && !os.IsNotExist(err) {
		logPatch("[bugreport] %s was sent but its queued file could not be removed: %v", id, err)
	}
	bugrepRecount()
}

// bugrepQueued lists the waiting reports, oldest first. A report that already
// left the outbox this session (bugrepSettled) is not waiting, even when its
// file is still there.
func bugrepQueued() []bugrepReport {
	dir := bugrepOutboxDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []bugrepReport
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r bugrepReport
		if json.Unmarshal(data, &r) != nil || !bugrepRefValid(r.ID) || r.ID+".json" != e.Name() || bugrepIsSettled(r.ID) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	return out
}

// bugrepRecountMu serialises recounts: a background count that read the folder
// before a queue or a send finished must not overwrite the newer count.
var bugrepRecountMu sync.Mutex

func bugrepRecount() {
	bugrepRecountMu.Lock()
	defer bugrepRecountMu.Unlock()
	n := len(bugrepQueued())
	bugrepOutbox.mu.Lock()
	bugrepOutbox.count = n
	bugrepOutbox.scanned = true
	bugrepOutbox.mu.Unlock()
}

// bugrepOutboxCount is how many reports wait to be sent.
func bugrepOutboxCount() int {
	bugrepOutbox.mu.Lock()
	defer bugrepOutbox.mu.Unlock()
	return bugrepOutbox.count
}

// bugrepOutboxInterval is how often a non-empty outbox is retried while online
// and the collector is not failing.
const bugrepOutboxInterval = 90 * time.Second

// bugrepOutboxRetryMax caps the wait between tries of a failing collector.
const bugrepOutboxRetryMax = 6 * time.Hour

// bugrepOutboxMaxAge is how long a queued report is retried. One nobody could
// deliver in a month is retired to a file (bugrepRetire): better read from the
// card than retried for ever.
const bugrepOutboxMaxAge = 30 * 24 * time.Hour

// bugrepOutboxDelay is the wait before the next try after failures failed
// flushes in a row: the interval, doubled per failure, capped.
func bugrepOutboxDelay(failures int) time.Duration {
	d := bugrepOutboxInterval
	for i := 0; i < failures && d < bugrepOutboxRetryMax; i++ {
		d *= 2
	}
	if d > bugrepOutboxRetryMax {
		d = bugrepOutboxRetryMax
	}
	return d
}

// bugrepOutboxDueLocked reports whether the outbox should be tried at now.
// bugrepOutbox.mu must be held.
func bugrepOutboxDueLocked(now time.Time) bool {
	o := &bugrepOutbox
	return o.count > 0 && !o.flushing && !o.stopped && now.Sub(o.lastTry) >= bugrepOutboxDelay(o.failures)
}

// bugrepOutboxStopped reports whether queued reports are waiting for the next
// start because the collector is not there.
func bugrepOutboxStopped() bool {
	bugrepOutbox.mu.Lock()
	defer bugrepOutbox.mu.Unlock()
	return bugrepOutbox.stopped
}

// bugrepCollectorAnswered notes that the collector just took a report: what
// made the outbox wait is over, so anything queued goes out now. Main thread.
func bugrepCollectorAnswered(endpoint string) {
	bugrepOutbox.mu.Lock()
	bugrepOutbox.stopped = false
	bugrepOutbox.failures = 0
	waiting := bugrepOutbox.count > 0
	bugrepOutbox.mu.Unlock()
	if waiting && endpoint != "" {
		bugrepFlush(endpoint, nil)
	}
}

// bugrepCollectorUnavailable notes that the collector is not there, so the
// outbox does not ask it again before the next start.
func bugrepCollectorUnavailable() {
	bugrepOutbox.mu.Lock()
	bugrepOutbox.stopped = true
	bugrepOutbox.mu.Unlock()
}

// bugrepNoteSendFailed records a send from the form that just failed, before
// its report goes into the outbox: the outbox's next try then waits out the
// usual backoff from now, instead of posting the same report on the next
// frame to a service that has just failed (and, after a lost answer, filing
// it twice).
func bugrepNoteSendFailed(now time.Time) {
	bugrepOutbox.mu.Lock()
	bugrepOutbox.lastTry = now
	if bugrepOutbox.failures < 1 {
		bugrepOutbox.failures = 1
	}
	bugrepOutbox.mu.Unlock()
}

// bugrepOutboxTick runs on the main loop. It counts the outbox once after
// startup and, while there is something in it, a collector and a network,
// sends it in the background every so often — less often while the collector
// keeps failing, and not at all once it turned out not to be there.
func bugrepOutboxTick() {
	bugrepOutbox.mu.Lock()
	if !bugrepOutbox.scanned {
		bugrepOutbox.scanned = true
		bugrepOutbox.mu.Unlock()
		go bugrepRecount()
		return
	}
	due := bugrepOutboxDueLocked(time.Now())
	bugrepOutbox.mu.Unlock()
	if !due || !IsOnline() {
		return
	}
	endpoint := bugrepEndpointURL()
	if endpoint == "" {
		return
	}
	bugrepFlush(endpoint, nil)
}

// bugrepReportExpired reports whether a queued report is past its retries. A
// report made while the clock was wrong (stamped before bugrepPlausibleFloor)
// has no meaningful age, and a clock that is wrong now cannot judge one, so
// both stay.
func bugrepReportExpired(r bugrepReport, now time.Time) bool {
	created, err := time.Parse(time.RFC3339, strings.TrimSpace(r.Created))
	if err != nil || bugrepClockWrong(now) || created.Before(bugrepPlausibleFloor) {
		return false
	}
	return now.Sub(created) > bugrepOutboxMaxAge
}

// bugrepRetire takes a report out of the outbox for good: a readable copy goes
// next to the other support reports and the queued file moves to
// bugreports/retired, so nothing the user wrote is lost and nothing retries it.
//
// Nothing leaves the outbox without that copy. On a full or read-only card the
// queued file stays where it is; it is skipped for the rest of the session (so
// it is not posted again every few minutes) and the next start tries again.
func bugrepRetire(r bugrepReport, why string) {
	dir := bugrepOutboxDir()
	if dir == "" || !bugrepRefValid(r.ID) {
		return
	}
	bugrepSettle(r.ID)
	copyPath, copyErr := bugrepSaveCopyFunc(r)
	if copyErr != nil {
		bugrepRecount()
		logPatch("[bugreport] %s not retired (%s): no readable copy could be saved (%v); it stays queued for the next start",
			r.ID, why, copyErr)
		return
	}
	src := filepath.Join(dir, r.ID+".json")
	retired := filepath.Join(filepath.Dir(dir), "retired")
	moved := os.MkdirAll(retired, 0o700) == nil && os.Rename(src, filepath.Join(retired, r.ID+".json")) == nil
	if !moved {
		// The readable copy is kept, so the queued file may simply go.
		if err := bugrepRemove(src); err != nil && !os.IsNotExist(err) {
			logPatch("[bugreport] %s retired, but its queued file could not be removed: %v", r.ID, err)
		}
	}
	bugrepRecount()
	logPatch("[bugreport] %s retired (%s); %s in the Patch reports folder", r.ID, why, bugrepBaseName(copyPath))
}

// bugrepFlush sends every queued report in the background. done, when set, is
// called on the main loop with how many were sent.
func bugrepFlush(endpoint string, done func(sent int)) {
	bugrepOutbox.mu.Lock()
	if bugrepOutbox.flushing {
		bugrepOutbox.mu.Unlock()
		return
	}
	bugrepOutbox.flushing = true
	bugrepOutbox.lastTry = time.Now()
	bugrepOutbox.mu.Unlock()
	go func() {
		sent := 0
		failed, stop := false, false
		now := time.Now()
		for _, r := range bugrepQueued() {
			if bugrepReportExpired(r, now) {
				bugrepRetire(r, "it could not be sent for a month")
				continue
			}
			_, kind, err := bugrepPostFunc(endpoint, r)
			if err == nil {
				bugrepUnqueue(r.ID)
				sent++
				continue
			}
			if kind == bugrepErrRejected {
				// The collector will never take this one; keeping it would
				// retry it for ever.
				bugrepRetire(r, "the report service refused it: "+firstLine(err.Error()))
				continue
			}
			// The rest wait. A collector that is not there, or one this
			// firmware cannot trust, is not asked again before the next start;
			// a network or server failure backs off. A clock before the build
			// date costs no request (bugrepPost checks it first), so it is
			// simply checked again at the usual pace; a certificate that only
			// looks expired — the clock is wrong but past the build date, or the
			// certificate really expired — cost a handshake, so it backs off
			// like any other failure.
			stop = kind == bugrepErrUnavailable || kind == bugrepErrTrust
			failed = kind == bugrepErrOffline || kind == bugrepErrServer ||
				(kind == bugrepErrClock && !bugrepClockWrong(time.Now()))
			if stop {
				logPatch("[bugreport] the report service is not available (%v); queued reports wait for the next start", err)
			}
			break
		}
		bugrepOutbox.mu.Lock()
		bugrepOutbox.flushing = false
		switch {
		case stop:
			bugrepOutbox.stopped = true
		case failed:
			if bugrepOutbox.failures < 32 {
				bugrepOutbox.failures++
			}
		case sent > 0:
			bugrepOutbox.failures = 0
		}
		bugrepOutbox.mu.Unlock()
		if sent > 0 {
			logPatch("[bugreport] sent %d queued report(s)", sent)
			tbxPost(func() {
				if done != nil {
					done(sent)
					return
				}
				word := "report"
				if sent > 1 {
					word = "reports"
				}
				ShowToast(fmt.Sprintf("Sent %d queued bug %s", sent, word), ToastKindSuccess)
			})
		}
	}()
}

// ── Phone path ───────────────────────────────────────────────────────────

// bugrepIssueURL is a prefilled GitHub issue for the phone path. It stays short
// enough to scan from a QR code: the description is cut, and the full
// diagnostics are left to the copy saved on the device.
func bugrepIssueURL(r bugrepReport, maxLen int) string {
	title := r.Title
	if title == "" {
		category := r.Category
		if category == "" {
			category = "other"
		}
		title = strings.ToUpper(category[:1]) + category[1:] + " problem"
	}
	facts := "JukaHub " + r.Version + " · " + r.Device + " · " + r.OS + "/" + r.Arch + " · ref " + r.ID
	desc := r.Description
	build := func(d string) string {
		body := d + "\n\n" + facts
		return jukaHubIssuesNew + "?title=" + url.QueryEscape("[Bug] "+title) + "&body=" + url.QueryEscape(body)
	}
	out := build(desc)
	for len(out) > maxLen && desc != "" {
		// Each pass keeps at most n-2 runes plus the ellipsis, so the text
		// always gets shorter and the loop ends even when the title alone is
		// over the budget.
		runes := []rune(desc)
		keep := len(runes) * 3 / 4
		if keep > len(runes)-2 {
			keep = len(runes) - 2
		}
		if keep <= 0 {
			desc = ""
		} else {
			desc = strings.TrimSpace(string(runes[:keep])) + "…"
		}
		out = build(desc)
	}
	return out
}

// bugrepSaveCopy writes the full report next to the other support reports, so
// nothing is lost when only the short version fits a QR code.
func bugrepSaveCopy(r bugrepReport) (string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "reports", "bugreport-"+r.ID+".txt")
	if err := os.MkdirAll(filepath.Dir(path), PermDir); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(bugrepPreviewText(r)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
