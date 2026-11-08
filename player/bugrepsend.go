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
//   - To the anonymous collector when the build has one: -ldflags
//     "-X main.bugrepEndpoint=https://…", the JUKAHUB_BUGREPORT_URL variable,
//     a "bugReportURL" in the config, or else the telemetry collector. The
//     report is a JSON POST; any 2xx is success, and a {"id": "…"} in the
//     answer replaces the reference the device generated.
//   - Without a collector, through the user's phone: a QR code opens a
//     prefilled GitHub issue (a handheld has no browser of its own).
//
// The handheld is where sending usually fails, so each failure has its own
// answer: no network → the report waits in an outbox file under the app data
// folder and goes out on its own when the device is back online ("Queued");
// a clock reset to 1970 makes every certificate look invalid → "Set the date
// and time first"; a firmware without a certificate store → a CA bundle shipped
// next to the app or a firmware one is used, and if none exists the phone path
// is offered instead.

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

// bugrepEndpoint is set by a release build that has a collector:
// -ldflags "-X main.bugrepEndpoint=https://reports.example/v1/bug".
var bugrepEndpoint string

// bugrepConfigKey is the config/user setting that points reports elsewhere.
const bugrepConfigKey = "bugReportURL"

// bugrepEndpointFor picks the collector from the environment, the config's
// custom block, the build, or the telemetry collector, in that order. Only
// https is accepted (http only for a collector on this machine, for testing).
func bugrepEndpointFor(custom map[string]interface{}) string {
	candidates := []string{strings.TrimSpace(os.Getenv("JUKAHUB_BUGREPORT_URL"))}
	if custom != nil {
		if v, ok := custom[bugrepConfigKey].(string); ok {
			candidates = append(candidates, strings.TrimSpace(v))
		}
	}
	candidates = append(candidates, strings.TrimSpace(bugrepEndpoint), telemetryEndpointURL())
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
	bugrepIPv4   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	bugrepMAC    = regexp.MustCompile(`\b[0-9A-Fa-f]{2}(?:[:-][0-9A-Fa-f]{2}){5}\b`)
	bugrepEmail  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	bugrepURL    = regexp.MustCompile(`\b(https?|rtsp|rtmp|ftp|sftp)://([^/\s"'<>]+)[^\s"'<>]*`)
	bugrepWinAbs = regexp.MustCompile(`\b[A-Za-z]:\\[^\s"'<>|]*`)
	bugrepUnix   = regexp.MustCompile(`(?:^|[\s"'=(])(/(?:mnt|home|root|userdata|storage|media|tmp|var|opt|usr|etc|sdcard|data|Users|Volumes|private)(?:/[^\s"'<>)]*)?)`)
)

// bugrepScrub removes what must never leave the device from text about to be
// sent: credentials and account names (redactDiagnosticText), then URL paths,
// e-mail, IP and MAC addresses and absolute paths.
func bugrepScrub(text string) string {
	text = redactDiagnosticText(text)
	text = bugrepURL.ReplaceAllString(text, "$1://$2/…")
	text = bugrepEmail.ReplaceAllString(text, "[email]")
	text = bugrepMAC.ReplaceAllString(text, "[mac]")
	text = bugrepIPv4.ReplaceAllStringFunc(text, func(ip string) string {
		if ip == "0.0.0.0" || ip == "127.0.0.1" || ip == "1.1.1.1" || ip == "8.8.8.8" {
			return ip
		}
		return "[ip]"
	})
	text = bugrepWinAbs.ReplaceAllString(text, "[path]")
	text = bugrepUnix.ReplaceAllStringFunc(text, func(m string) string {
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
	return &http.Client{Timeout: 25 * time.Second, Transport: tr}
}

// bugrepPostFunc is a test seam over bugrepPost.
var bugrepPostFunc = bugrepPost

// bugrepPost sends one report. It returns the reference to show (the
// collector's when it answers with one) and a classification of any failure.
func bugrepPost(endpoint string, r bugrepReport) (string, bugrepSendErr, error) {
	if bugrepClockWrong(time.Now()) {
		return "", bugrepErrClock, errors.New("the device clock is wrong")
	}
	body, err := json.Marshal(r)
	if err != nil {
		return "", bugrepErrRejected, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", bugrepErrRejected, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "JukaHub-BugReport/"+r.Version)
	resp, err := bugrepHTTPClient().Do(req)
	if err != nil {
		return "", bugrepClassifyError(err), err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		ref := r.ID
		var answer struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data, &answer) == nil && bugrepRefValid(answer.ID) {
			ref = answer.ID
		}
		return ref, bugrepErrNone, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return "", bugrepErrServer, fmt.Errorf("the report service answered HTTP %d", resp.StatusCode)
	}
	return "", bugrepErrRejected, fmt.Errorf("the report service refused the report (HTTP %d)", resp.StatusCode)
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

// bugrepUnqueue removes a sent (or refused) report.
func bugrepUnqueue(id string) {
	dir := bugrepOutboxDir()
	if dir == "" || !bugrepRefValid(id) {
		return
	}
	_ = os.Remove(filepath.Join(dir, id+".json"))
	bugrepRecount()
}

// bugrepQueued lists the waiting reports, oldest first.
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
		if json.Unmarshal(data, &r) != nil || !bugrepRefValid(r.ID) || r.ID+".json" != e.Name() {
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

// bugrepOutboxInterval is how often a non-empty outbox is retried while online.
const bugrepOutboxInterval = 90 * time.Second

// bugrepOutboxTick runs on the main loop. It counts the outbox once after
// startup and, while there is something in it, a collector and a network,
// sends it in the background every so often.
func bugrepOutboxTick() {
	bugrepOutbox.mu.Lock()
	if !bugrepOutbox.scanned {
		bugrepOutbox.scanned = true
		bugrepOutbox.mu.Unlock()
		go bugrepRecount()
		return
	}
	due := bugrepOutbox.count > 0 && !bugrepOutbox.flushing && time.Since(bugrepOutbox.lastTry) >= bugrepOutboxInterval
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
		for _, r := range bugrepQueued() {
			_, kind, err := bugrepPostFunc(endpoint, r)
			if err == nil {
				bugrepUnqueue(r.ID)
				sent++
				continue
			}
			if kind == bugrepErrRejected {
				// The collector will never take this one; keeping it would
				// retry it forever.
				logPatch("[bugreport] %s refused: %v", r.ID, err)
				bugrepUnqueue(r.ID)
				continue
			}
			break
		}
		bugrepOutbox.mu.Lock()
		bugrepOutbox.flushing = false
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
