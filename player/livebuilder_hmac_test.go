package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const liveBuilderTestAuthWindow = 5 * time.Minute

// liveBuilderSignedTestRequest signs one request the way the browser builder
// does: HMAC-SHA256 over the method, path, timestamp, nonce and body digest,
// keyed by the pairing code. The code itself is never put in a header.
func liveBuilderSignedTestRequest(t *testing.T, handler http.Handler, method, path string, body []byte, code, nonce string, at time.Time) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Host = "192.168.1.50:8422"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if code != "" {
		ts := strconv.FormatInt(at.Unix(), 10)
		bodySum := sha256.Sum256(body)
		msg := method + "\n" + path + "\n" + ts + "\n" + nonce + "\n" + hex.EncodeToString(bodySum[:])
		mac := hmac.New(sha256.New, []byte(code))
		_, _ = mac.Write([]byte(msg))
		req.Header.Set("X-Live-Builder-Timestamp", ts)
		req.Header.Set("X-Live-Builder-Nonce", nonce)
		req.Header.Set("X-Live-Builder-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestLiveBuilderAuthenticatesWithoutSendingTheCode(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	rec := liveBuilderSignedTestRequest(t, srv.handler(), http.MethodGet, "/api/design", nil,
		liveBuilderTestCode, "0011223344556677", time.Now())
	if rec.Code != http.StatusOK {
		t.Fatalf("signed pull: status = %d, body %s, want 200", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), liveBuilderTestCode) {
		t.Fatal("the pull answer contains the pairing code")
	}
}

func TestLiveBuilderRefusesAReplayedRequest(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	at := time.Now()
	first := liveBuilderSignedTestRequest(t, srv.handler(), http.MethodGet, "/api/design", nil,
		liveBuilderTestCode, "0011223344556677", at)
	if first.Code != http.StatusOK {
		t.Fatalf("first signed pull: status = %d, body %s", first.Code, first.Body.String())
	}
	again := liveBuilderSignedTestRequest(t, srv.handler(), http.MethodGet, "/api/design", nil,
		liveBuilderTestCode, "0011223344556677", at)
	if again.Code != http.StatusUnauthorized || !strings.Contains(again.Body.String(), "already used") {
		t.Fatalf("replayed pull: status = %d, body %s, want 401 already used", again.Code, again.Body.String())
	}
}

func TestLiveBuilderRefusesAStaleSignature(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	rec := liveBuilderSignedTestRequest(t, srv.handler(), http.MethodGet, "/api/design", nil,
		liveBuilderTestCode, "0011223344556677", time.Now().Add(-liveBuilderTestAuthWindow-time.Minute))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("stale signed pull: status = %d, body %s, want 401 expired", rec.Code, rec.Body.String())
	}
}
