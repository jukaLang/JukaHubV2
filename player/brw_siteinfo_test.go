package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The three reports, in the shape nettools.go and nettools3.go write them.
const (
	brwTestHTTPReport = "Check:  https://example.com/\nStatus: 200 OK\nTime:   123 ms\nServer: nginx\n" +
		"Type:   text/html; charset=utf-8\nTLS:    Let's Encrypt · example.com · expires 2027-01-01 (45 days left)\n" +
		"\nRead-only: one request; the body is read only far enough to measure it."
	brwTestCertReport = "Target:  example.com:443\nTLS:     TLS 1.3 · TLS_AES_128_GCM_SHA256\n" +
		"Subject: CN=example.com\nIssuer:  CN=R11, O=Let's Encrypt, C=US\n" +
		"Valid:   2026-10-01 00:00 → 2027-01-01 00:00 UTC\nExpires: 2027-01-01 (45 days left)\n" +
		"Key:     ECDSA P-256\nSigning: ECDSA-SHA384\nNames:   example.com, www.example.com\n" +
		"Trust:   verified against this device's trusted roots\n\nRead-only: two handshakes, no request; nothing is sent after the greeting.\n"
	brwTestHeaderReport = "Check:   https://example.com/\nStatus:  200 OK\nTime:    80 ms\nHeaders: 4 of 6 present\n" +
		"  + Strict-Transport-Security   max-age=31536000\n" +
		"  - Content-Security-Policy     missing · limits what a page may load and run\n" +
		"  + X-Frame-Options             DENY\n" +
		"  + X-Content-Type-Options      nosniff\n" +
		"  + Referrer-Policy             no-referrer\n" +
		"  - Permissions-Policy          missing · switches browser features off\n" +
		"\nRead-only: one GET; the body is read only far enough to close the connection.\n"
)

func TestBrwInfoParsesTheReports(t *testing.T) {
	rows := brwInfoParse(brwTestHTTPReport)
	if got := brwInfoValue(rows, "Status"); got != "200 OK" {
		t.Fatalf("Status = %q", got)
	}
	if got := brwInfoValue(rows, "Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Type = %q", got)
	}
	last := rows[len(rows)-1]
	if last.Label != "" || !strings.HasPrefix(last.Value, "Read-only") {
		t.Fatalf("the footnote became a labelled row: %+v", last)
	}
	if status, tone := brwInfoStatus(brwInfoConnection, rows); status != "200 OK · 123 ms" || tone != brwToneGood {
		t.Fatalf("connection verdict = %q (%d)", status, tone)
	}

	rows = brwInfoParse(brwTestCertReport)
	if got := brwInfoValue(rows, "Valid"); !strings.Contains(got, "→") {
		t.Fatalf("Valid = %q: a value with a colon in it was cut", got)
	}
	if status, tone := brwInfoStatus(brwInfoCertificate, rows); status != "Trusted · 45 days left" || tone != brwToneGood {
		t.Fatalf("certificate verdict = %q (%d)", status, tone)
	}
	untrusted := strings.Replace(brwTestCertReport, "verified against this device's trusted roots", "not trusted: x509: certificate signed by unknown authority", 1)
	if _, tone := brwInfoStatus(brwInfoCertificate, brwInfoParse(untrusted)); tone != brwToneBad {
		t.Fatalf("an untrusted certificate read as tone %d", tone)
	}

	rows = brwInfoParse(brwTestHeaderReport)
	present, missing := 0, 0
	for _, row := range rows {
		switch row.Mark {
		case 1:
			present++
		case -1:
			missing++
			if !strings.HasPrefix(row.Value, "missing") {
				t.Errorf("missing header %s has value %q", row.Label, row.Value)
			}
		}
	}
	if present != 4 || missing != 2 {
		t.Fatalf("headers parsed as %d present / %d missing", present, missing)
	}
	if status, tone := brwInfoStatus(brwInfoHeaders, rows); status != "4 of 6 security headers" || tone != brwToneWarn {
		t.Fatalf("headers verdict = %q (%d)", status, tone)
	}
}

// brwInfoStubRunners stands in for the three network reports and counts calls.
func brwInfoStubRunners(t *testing.T) *[brwInfoTabs]int32 {
	t.Helper()
	var calls [brwInfoTabs]int32
	prev := brwInfoRunners
	reports := [brwInfoTabs]string{brwTestHTTPReport, brwTestCertReport, brwTestHeaderReport}
	for i := range brwInfoRunners {
		i := i
		brwInfoRunners[i] = func(string) (string, error) {
			atomic.AddInt32(&calls[i], 1)
			return reports[i], nil
		}
	}
	brwInfoCache.mu.Lock()
	prevEntries := brwInfoCache.entries
	brwInfoCache.entries = map[string]*brwInfoEntry{}
	brwInfoCache.mu.Unlock()
	t.Cleanup(func() {
		brwInfoRunners = prev
		brwInfoCache.mu.Lock()
		brwInfoCache.entries = prevEntries
		brwInfoCache.mu.Unlock()
		brwInfoForget()
	})
	return &calls
}

func brwWaitInfo(t *testing.T, address string, tab, state int) brwInfoResult {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		res := brwInfoResultFor(address, tab)
		if res.State == state {
			return res
		}
		if time.Now().After(deadline) {
			t.Fatalf("tab %d of %s stayed in state %d, want %d", tab, address, res.State, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The checks run once per URL for the session, a re-check runs them again,
// and an http:// page's certificate tab answers without a connection.
func TestBrwInfoRunsOncePerURL(t *testing.T) {
	brwSetOnlineForTest(t, true)
	calls := brwInfoStubRunners(t)

	const secure = "https://example.com/"
	brwInfoEnsure(secure, true)
	for tab := 0; tab < brwInfoTabs; tab++ {
		brwWaitInfo(t, secure, tab, brwInfoDone)
	}
	brwInfoEnsure(secure, true)
	for tab := 0; tab < brwInfoTabs; tab++ {
		if got := atomic.LoadInt32(&calls[tab]); got != 1 {
			t.Fatalf("tab %d ran %d times for one URL", tab, got)
		}
	}

	brwInfo = brwInfoState{open: true, url: secure, https: true}
	brwInfoRecheck()
	for tab := 0; tab < brwInfoTabs; tab++ {
		brwWaitInfo(t, secure, tab, brwInfoDone)
		if got := atomic.LoadInt32(&calls[tab]); got != 2 {
			t.Fatalf("tab %d ran %d times after a re-check, want 2", tab, got)
		}
	}

	const plain = "http://example.com/"
	brwInfoEnsure(plain, false)
	cert := brwInfoResultFor(plain, brwInfoCertificate)
	if cert.State != brwInfoDone || cert.Status != "Not encrypted" {
		t.Fatalf("http certificate tab = %+v", cert)
	}
	brwWaitInfo(t, plain, brwInfoConnection, brwInfoDone)
	if got := atomic.LoadInt32(&calls[brwInfoCertificate]); got != 2 {
		t.Fatalf("an http page made a TLS handshake (%d calls)", got)
	}
}

// Offline nothing starts; a failure is reported in words.
func TestBrwInfoOfflineAndFailures(t *testing.T) {
	brwSetOnlineForTest(t, false)
	calls := brwInfoStubRunners(t)
	brwInfoEnsure("https://example.org/", true)
	if res := brwInfoResultFor("https://example.org/", brwInfoConnection); res.State != brwInfoIdle {
		t.Fatalf("an offline check started: %+v", res)
	}
	if atomic.LoadInt32(&calls[brwInfoConnection]) != 0 {
		t.Fatal("an offline check went to the network")
	}

	brwSetOnlineForTest(t, true)
	brwInfoRunners[brwInfoHeaders] = func(string) (string, error) {
		return "", errors.New("no answer: Get \"https://example.org/\": dial tcp 93.184.216.34:443: connect: connection refused")
	}
	brwInfoEnsure("https://example.org/", true)
	res := brwWaitInfo(t, "https://example.org/", brwInfoHeaders, brwInfoFailed)
	if res.Err == "" || strings.Contains(res.Err, "dial tcp") {
		t.Fatalf("failure text = %q", res.Err)
	}
}

// The sheet needs a page, and the pad drives it: tabs wrap, B closes.
func TestBrwInfoOpenAndPad(t *testing.T) {
	brwSetOnlineForTest(t, true)
	brwInfoStubRunners(t)
	clearToastAfterTest(t)
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	brwInfoOpenFor(config)
	if brwInfo.open {
		t.Fatal("the sheet opened with no page on screen")
	}
	config.Variables.Custom[textBrowserURLVariable] = "https://example.com/docs"
	brwInfoOpenFor(config)
	if !brwInfo.open || brwInfo.host != "example.com" || !brwInfo.https {
		t.Fatalf("sheet state = %+v", brwInfo)
	}
	press := func(button uint8) {
		brwInfoController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}, config)
	}
	press(sdl.CONTROLLER_BUTTON_LEFTSHOULDER)
	if brwInfo.tab != brwInfoHeaders {
		t.Fatalf("L1 from the first tab went to %d", brwInfo.tab)
	}
	press(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER)
	if brwInfo.tab != brwInfoConnection {
		t.Fatalf("R1 from the last tab went to %d", brwInfo.tab)
	}
	press(sdl.CONTROLLER_BUTTON_B)
	if brwInfo.open {
		t.Fatal("B did not close the sheet")
	}
}
