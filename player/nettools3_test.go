package main

// nettools3_test.go — the certificate and header reports (the Browser's site
// information uses them) and the encode / decode tool on the Network tools
// page. Every check here runs against a listener this test starts, or against
// the string the codec was given: nothing reaches the network.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

// The three prompts the Network tools page still opens (Port check, Domain
// whois, Encode / decode) keep their titles and examples on the keyboard.
func TestNetPromptTitlesForTheTypedTools(t *testing.T) {
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

// TestNetworkToolsPageCarriesTheCodecNotTheWebChecks keeps the move honest:
// Encode / decode is a tile of the Network tools page, while the Service
// banner was removed and the HTTP check, TLS certificate and Security headers
// belong to the Browser's site information, not to a tile.
func TestNetworkToolsPageCarriesTheCodecNotTheWebChecks(t *testing.T) {
	spec, ok := netToolsSpecFor("codec")
	if !ok {
		t.Fatal("Encode / decode is missing from the Network tools page")
	}
	if spec.label != "Encode / decode" || spec.mode != netToolsModeInput {
		t.Errorf("codec tile = %+v, want the typed Encode / decode tool", spec)
	}
	if spec.need != toolsHubNeedNothing {
		t.Errorf("codec tile needs %v; decoding a string needs no network", spec.need)
	}
	for _, spec := range netToolsSpecs {
		lower := strings.ToLower(spec.id + " " + spec.label)
		for _, gone := range []string{"banner", "certificate", "header", "http check", "httpcheck"} {
			if strings.Contains(lower, gone) {
				t.Errorf("tile %q (%s) is back on the Network tools page", spec.label, spec.id)
			}
		}
	}
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
