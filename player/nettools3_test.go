package main

// nettools3_test.go — the four recon rows added to the Tools panel. Every check
// here runs against a listener this test starts, or against the string the
// codec was given: nothing reaches the network.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNetTargetHostPortFillsTheDefaultPort(t *testing.T) {
	cases := []struct {
		in     string
		host   string
		port   int
		badIn  bool
		reason string
	}{
		{in: "example.com", host: "example.com", port: 443},
		{in: " example.com ", host: "example.com", port: 443},
		{in: "example.com:8443", host: "example.com", port: 8443},
		{in: "https://example.com/login?x=1", host: "example.com", port: 443},
		{in: "192.168.1.7:5000", host: "192.168.1.7", port: 5000},
		{in: "[::1]:9443", host: "::1", port: 9443},
		{in: "", badIn: true, reason: "type a host"},
		{in: "exa mple.com", badIn: true, reason: "spaces"},
		{in: "example.com:0", badIn: true, reason: "1 to 65535"},
		{in: "example.com:https", badIn: true, reason: "1 to 65535"},
		{in: "http://", badIn: true, reason: "no host"},
	}
	for _, c := range cases {
		host, port, err := netTargetHostPort(c.in, 443)
		if c.badIn {
			if err == nil {
				t.Errorf("netTargetHostPort(%q) = %q:%d, want an error about %s", c.in, host, port, c.reason)
			} else if !strings.Contains(err.Error(), c.reason) {
				t.Errorf("netTargetHostPort(%q) error = %q, want it to mention %q", c.in, err, c.reason)
			}
			continue
		}
		if err != nil {
			t.Errorf("netTargetHostPort(%q) failed: %v", c.in, err)
			continue
		}
		if host != c.host || port != c.port {
			t.Errorf("netTargetHostPort(%q) = %q:%d, want %q:%d", c.in, host, port, c.host, c.port)
		}
	}
}

func TestCertSummaryReadsATLSServer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	_, portText, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	text, err := certSummary(net.JoinHostPort("127.0.0.1", portText))
	if err != nil {
		t.Fatalf("certSummary failed against a live TLS listener: %v", err)
	}
	for _, want := range []string{
		// httptest's TLS server presents a self-signed test certificate; the
		// report has to describe it, not refuse to talk about it.
		"Subject: ",
		"Issuer:  ",
		"TLS:     TLS 1.",
		"Expires: ",
		"Key:     ",
		"Names:   example.com",
		// The test certificate is signed by itself, so the trust line must say
		// so rather than reporting a clean bill of health.
		"Trust:   not trusted",
		"Read-only:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report is missing %q\n%s", want, text)
		}
	}
}

func TestServerBannerIsReadWithoutWritingAnything(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	greeting := "SSH-2.0-OpenSSH_9.6\r\n"
	written := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(greeting))
		// Whatever the client sends would land here; the row must never send
		// anything, so nothing should ever arrive.
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		written <- string(buf[:n])
	}()

	text, err := bannerSummary(ln.Addr().String())
	if err != nil {
		t.Fatalf("bannerSummary failed against a live listener: %v", err)
	}
	if !strings.Contains(text, "SSH-2.0-OpenSSH_9.6") {
		t.Errorf("banner missing from the report:\n%s", text)
	}
	if !strings.Contains(text, "Open:   yes") {
		t.Errorf("report does not say the port answered:\n%s", text)
	}
	if sent := <-written; sent != "" {
		t.Errorf("the banner row wrote %q to the socket; it must only read", sent)
	}
}

func TestBannerSummaryReportsASilentService(t *testing.T) {
	// No defer: the helper registers its own cleanup, and deferring the call
	// would shorten the window only after the row had already run.
	restoreBannerWindow(t, 150*time.Millisecond)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Hold the connection open without speaking: HTTP and most databases
		// behave exactly like this.
		time.Sleep(400 * time.Millisecond)
		conn.Close()
	}()

	text, err := bannerSummary(ln.Addr().String())
	if err != nil {
		t.Fatalf("a silent service is a result, not an error: %v", err)
	}
	if !strings.Contains(text, "Banner: none") {
		t.Errorf("report does not say there was no banner:\n%s", text)
	}
	if !strings.Contains(text, "never spoke first") {
		t.Errorf("report does not explain a silent service:\n%s", text)
	}
}

func TestBannerSummaryReportsAClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	text, err := bannerSummary(addr)
	if err != nil {
		t.Fatalf("a closed port returned an error instead of a result: %v", err)
	}
	if !strings.Contains(text, "Open:   no") || !strings.Contains(text, "nothing is listening") {
		t.Errorf("report does not describe a refused port:\n%s", text)
	}
}

func TestHeaderAuditCountsPresentAndMissing(t *testing.T) {
	// Plain http: the shared client verifies certificates, and a test
	// certificate would fail the fetch before a header was ever read.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	text, err := headerAuditSummary(srv.URL)
	if err != nil {
		t.Fatalf("headerAuditSummary failed against a live server: %v", err)
	}
	if !strings.Contains(text, "Headers: 2 of 6 present") {
		t.Errorf("report does not count the two headers that were sent:\n%s", text)
	}
	if !strings.Contains(text, "+ Strict-Transport-Security") || !strings.Contains(text, "max-age=31536000") {
		t.Errorf("report lost the HSTS value:\n%s", text)
	}
	if !strings.Contains(text, "- Content-Security-Policy") {
		t.Errorf("report does not mark the missing CSP:\n%s", text)
	}
}

func TestHeaderAuditWarnsThatHTTPCannotUseHSTS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	text, err := headerAuditSummary(srv.URL)
	if err != nil {
		t.Fatalf("headerAuditSummary failed against a plain HTTP server: %v", err)
	}
	if !strings.Contains(text, "http://, where a browser ignores HSTS") {
		t.Errorf("report does not warn that HSTS means nothing over http:\n%s", text)
	}
}

func TestHeaderAuditTruncatesALongPolicy(t *testing.T) {
	long := "default-src 'self'; " + strings.Repeat("script-src https://example.com; ", 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", long)
	}))
	defer srv.Close()

	text, err := headerAuditSummary(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "+ Content-Security-Policy") && !strings.HasSuffix(line, "…") {
			t.Fatalf("a long policy was not shortened: %q", line)
		}
	}
	if !strings.Contains(text, "…") {
		t.Error("expected a shortened policy, found none")
	}
}

func TestCodecSummaryDecodesEachEncoding(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		wants []string
	}{
		{
			name:  "base64",
			in:    "aGVsbG8=",
			wants: []string{"Input:      aGVsbG8=", "Base64:     hello"},
		},
		{
			name:  "url-safe base64 without padding",
			in:    "aGVsbG8",
			wants: []string{"Base64:     hello"},
		},
		{
			// Also valid base64, which is why the base64 line must say the
			// bytes are not text instead of printing them as mojibake.
			name:  "hex",
			in:    "68656c6c6f",
			wants: []string{"Hex:        hello", "Base64:     7 bytes, not text"},
		},
		{
			name:  "url encoded",
			in:    "hello%20world%21",
			wants: []string{"URL decode: hello world!", "Base64:     not valid base64"},
		},
	}
	for _, c := range cases {
		text, err := codecSummary(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		for _, want := range c.wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s: report is missing %q\n%s", c.name, want, text)
			}
		}
	}
}

func TestCodecSummaryShowsTheEncodingsBack(t *testing.T) {
	text, err := codecSummary("hi there")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"As base64:  aGkgdGhlcmU=",
		"As hex:     6869207468657265",
		"As URL:     hi+there",
		"Nothing left this device",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report is missing %q\n%s", want, text)
		}
	}
}

func TestCodecSummaryRejectsEmptyInput(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		if _, err := codecSummary(in); err == nil {
			t.Errorf("codecSummary(%q) accepted an empty string", in)
		}
	}
}

func TestCodecSummaryKeepsBinaryOnOneLine(t *testing.T) {
	// 0x00ff00 prints as decoded bytes: the pane must not receive raw controls.
	text, err := codecSummary("AP8A")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(text, "\x00") {
		t.Errorf("report contains a NUL byte: %q", text)
	}
	if !strings.Contains(text, "Base64:     3 bytes, not text (hex 00ff00)") {
		t.Errorf("binary payload was not labelled as binary:\n%s", text)
	}
}

func TestNetPromptTitlesForTheReconRows(t *testing.T) {
	defer resetKeyboardState(t)
	resetKeyboardState(t)
	vkTarget = vkTargetNetwork

	cases := []struct {
		kind        netPromptKind
		title       string
		placeholder string
	}{
		{netPromptPort, "PORT CHECK", "192.168.1.7:22"},
		{netPromptWhois, "WHOIS LOOKUP", "example.com"},
		{netPromptCert, "TLS CERTIFICATE", "example.com or example.com:8443"},
		{netPromptBanner, "SERVICE BANNER", "192.168.1.7:22"},
		{netPromptHeaders, "SECURITY HEADERS", "example.com or https://…"},
		{netPromptCodec, "ENCODE / DECODE", "paste a token, a URL or hex"},
	}
	for _, c := range cases {
		netPrompt = c.kind
		if got := vkTitle(); got != c.title {
			t.Errorf("vkTitle for kind %d = %q, want %q", c.kind, got, c.title)
		}
		if got := vkPlaceholder(); got != c.placeholder {
			t.Errorf("vkPlaceholder for kind %d = %q, want %q", c.kind, got, c.placeholder)
		}
	}
}

// TestReconRowsAreInTheToolsPanel keeps the four new rows reachable: a row that
// no longer exists is a feature the docs promise and the panel does not show.
func TestReconRowsAreInTheToolsPanel(t *testing.T) {
	rows := toolsRows(&Config{Width: 1024, Height: 600})
	want := map[toolsAction]string{
		toolsActionCert:    "TLS certificate",
		toolsActionBanner:  "Service banner",
		toolsActionHeaders: "Security headers",
		toolsActionCodec:   "Encode / decode",
	}
	seen := map[toolsAction]bool{}
	for _, row := range rows {
		if label, ok := want[row.action]; ok {
			if row.label != label {
				t.Errorf("row %d labelled %q, want %q", row.action, row.label, label)
			}
			if !row.enabled {
				t.Errorf("%q row is disabled: it needs only a typed target", label)
			}
			seen[row.action] = true
		}
	}
	for action, label := range want {
		if !seen[action] {
			t.Errorf("%q is missing from the Tools panel", label)
		}
	}
}

// restoreBannerWindow puts the shared read window back after a test shortens it.
func restoreBannerWindow(t *testing.T, window time.Duration) {
	t.Helper()
	original := bannerReadWindow
	bannerReadWindow = window
	t.Cleanup(func() { bannerReadWindow = original })
}

func TestTLSVersionNameCoversTheSeveritiesPeopleSee(t *testing.T) {
	for version, want := range map[uint16]string{
		0x0304: "TLS 1.3",
		0x0303: "TLS 1.2",
		0x0301: "TLS 1.0",
		0x0199: "TLS 0x0199",
	} {
		if got := tlsVersionName(version); got != want {
			t.Errorf("tlsVersionName(0x%04x) = %q, want %q", version, got, want)
		}
	}
}

func TestHeaderLineValueFitsOneLine(t *testing.T) {
	if got := headerLineValue("a\r\nb"); got != "a b" {
		t.Errorf("headerLineValue folded line breaks to %q", got)
	}
	long := strings.Repeat("x", 200)
	got := headerLineValue(long)
	if len([]rune(got)) > 65 || !strings.HasSuffix(got, "…") {
		t.Errorf("headerLineValue left a %d-rune value: %q", len(got), got)
	}
}

func TestBannerPrintableKeepsOnlyTheHead(t *testing.T) {
	input := strings.Repeat("A", 40) + "\x00\x07\r\nsecond\nthird\nfourth"
	got := bannerPrintable(input)
	if strings.ContainsAny(got, "\x00\x07") {
		t.Errorf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "second") || strings.Contains(got, "fourth") {
		t.Errorf("bannerPrintable kept the wrong lines: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("bannerPrintable left a newline in %q", got)
	}
}

func TestBannerSummaryRejectsATargetWithoutAPort(t *testing.T) {
	if _, err := bannerSummary("example.com"); err == nil {
		t.Fatal("a banner needs a port; example.com must be refused")
	} else if !strings.Contains(err.Error(), "host:port") {
		t.Fatalf("error = %q, want it to ask for host:port", err)
	}
	if _, _, err := netPortParseTarget(":" + strconv.Itoa(70000)); err == nil {
		t.Fatal("an out-of-range port must be refused")
	}
}
