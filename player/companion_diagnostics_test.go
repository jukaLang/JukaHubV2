package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompanionDiagnosticsLinkRequiresLiveToken(t *testing.T) {
	server := newCompanionServer("123456", t.TempDir(), nil)
	server.diagnosticToken = "one-time-support-token"
	server.diagnosticText = "JukaHub diagnostics\nVersion: test\n"
	server.diagnosticUntil = time.Now().Add(time.Minute)
	httpServer := httptest.NewServer(server.handler())
	defer httpServer.Close()

	resp, err := http.Get(httpServer.URL + "/diagnostics?token=one-time-support-token")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, "jukahub-diagnostics.txt") {
		t.Fatalf("Content-Disposition = %q", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Version: test") {
		t.Fatalf("report body = %q", body)
	}

	bad, err := http.Get(httpServer.URL + "/diagnostics?token=wrong")
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d, want 401", bad.StatusCode)
	}
}

func TestCompanionDiagnosticsLinkExpires(t *testing.T) {
	server := newCompanionServer("123456", t.TempDir(), nil)
	server.diagnosticToken = "expired-token"
	server.diagnosticText = "private report"
	server.diagnosticUntil = time.Now().Add(-time.Second)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/diagnostics?token=expired-token", nil)
	server.handleDiagnostics(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d, want 401", recorder.Code)
	}
}
