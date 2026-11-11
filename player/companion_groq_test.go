package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testGroqKey = "gsk_0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLWXYZ"

// stubGroqCheck answers every key check with verdict and counts the calls.
func stubGroqCheck(t *testing.T, verdict groqKeyVerdict) *int {
	t.Helper()
	calls := new(int)
	orig := companionCheckGroqKey
	companionCheckGroqKey = func(ctx context.Context, key string) groqKeyVerdict {
		*calls++
		return verdict
	}
	t.Cleanup(func() { companionCheckGroqKey = orig })
	return calls
}

func postGroqKey(t *testing.T, srv *companionServer, pin, key string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"key": key})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, "/api/groq-key", pin, bytes.NewReader(body)))
	return rec
}

func TestCompanionCleanGroqKey(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"  " + testGroqKey + "\n", testGroqKey, true},
		{`"` + testGroqKey + `"`, testGroqKey, true},
		{"'" + testGroqKey + "' ", testGroqKey, true},
		{"", "", false},
		{"   ", "", false},
		{"gsk_short", "", false},
		{"gsk_0123456789 abcdefghijklmnop", "", false},
		{"gsk_0123456789abcdefghijklmnopé", "", false},
		{strings.Repeat("a", companionGroqKeyMax+1), "", false},
	} {
		got, err := companionCleanGroqKey(tc.in)
		if tc.ok != (err == nil) || got != tc.want {
			t.Errorf("companionCleanGroqKey(%q) = %q, %v; want %q, ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestCompanionGroqKeySavedWhenGroqAccepts(t *testing.T) {
	calls := stubGroqCheck(t, groqKeyAccepted)
	srv, events := newTestCompanion(t)
	rec := postGroqKey(t, srv, "123456", "  "+testGroqKey+"  ")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if *calls != 1 {
		t.Errorf("Groq was asked %d times, want once", *calls)
	}
	if len(*events) != 1 || (*events)[0] != (companionEvent{Kind: companionEventGroqKey, Text: testGroqKey}) {
		t.Fatalf("events = %+v", *events)
	}
	var reply struct {
		OK       bool   `json:"ok"`
		Verified bool   `json:"verified"`
		Tail     string `json:"tail"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.OK || !reply.Verified || reply.Tail != "WXYZ" || reply.Message == "" {
		t.Errorf("reply = %+v", reply)
	}
	if strings.Contains(rec.Body.String(), testGroqKey) {
		t.Error("the reply must never echo the key")
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Error("key replies must not be cached")
	}
}

func TestCompanionGroqKeyRejectedByGroqIsNotSaved(t *testing.T) {
	stubGroqCheck(t, groqKeyRejected)
	srv, events := newTestCompanion(t)
	rec := postGroqKey(t, srv, "123456", testGroqKey)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if len(*events) != 0 {
		t.Fatalf("a key Groq refused was saved: %+v", *events)
	}
	if !strings.Contains(rec.Body.String(), "not valid") {
		t.Errorf("the phone must be told why: %q", rec.Body.String())
	}
}

func TestCompanionGroqKeySavedWhenGroqIsUnreachable(t *testing.T) {
	stubGroqCheck(t, groqKeyUnchecked)
	srv, events := newTestCompanion(t)
	rec := postGroqKey(t, srv, "123456", testGroqKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(*events) != 1 {
		t.Fatalf("an offline handheld must still save the key: %+v", *events)
	}
	if !strings.Contains(rec.Body.String(), `"verified":false`) {
		t.Errorf("an unchecked key must not be reported as verified: %s", rec.Body.String())
	}
}

func TestCompanionGroqKeyNeedsThePIN(t *testing.T) {
	calls := stubGroqCheck(t, groqKeyAccepted)
	srv, events := newTestCompanion(t)
	rec := postGroqKey(t, srv, "", testGroqKey)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(*events) != 0 || *calls != 0 {
		t.Fatalf("an unpaired request reached the key (events %+v, Groq calls %d)", *events, *calls)
	}
	bad := postGroqKey(t, srv, "123456", "not a key")
	if bad.Code != http.StatusUnprocessableEntity || *calls != 0 {
		t.Fatalf("a malformed key: status %d, Groq calls %d; want 422 and no call", bad.Code, *calls)
	}
}

// TestCompanionGroqStatusNeverSendsTheKey: the page learns only whether a key
// is saved and its last four characters.
func TestCompanionGroqStatusNeverSendsTheKey(t *testing.T) {
	origConfig := appConfig
	t.Cleanup(func() {
		appConfig = origConfig
		companionGroqSeen.raw, companionGroqSeen.ok = "", false
		companionGroqStatus.Store(companionGroqStatusInfo{})
	})
	srv, _ := newTestCompanion(t)
	get := func() string {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, companionRequest(http.MethodGet, "/api/groq-key", "123456", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		return strings.TrimSpace(rec.Body.String())
	}

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	appConfig = cfg
	companionGroqSeen.raw, companionGroqSeen.ok = "", false
	companionRefreshGroqStatus(cfg)
	if got := get(); got != `{"set":false,"tail":""}` {
		t.Errorf("no key: %s", got)
	}

	cfg.Variables.Custom["groq_api_key"] = testGroqKey
	companionRefreshGroqStatus(cfg)
	got := get()
	if got != `{"set":true,"tail":"WXYZ"}` {
		t.Errorf("saved key: %s", got)
	}
	if strings.Contains(got, testGroqKey) {
		t.Fatal("the status must never carry the key")
	}
}

// TestCompanionQueueSavesGroqKey: the key from the phone is saved on the main
// loop like the Keyboard settings field saves it.
func TestCompanionQueueSavesGroqKey(t *testing.T) {
	origConfig := appConfig
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	appConfig = cfg
	resetCompanionQueue()
	t.Cleanup(func() {
		appConfig = origConfig
		resetCompanionQueue()
		companionGroqSeen.raw, companionGroqSeen.ok = "", false
		companionGroqStatus.Store(companionGroqStatusInfo{})
	})
	enqueueCompanion(companionEvent{Kind: companionEventGroqKey, Text: testGroqKey})
	drainCompanionQueue(cfg)
	if got, _ := cfg.Variables.Custom["groq_api_key"].(string); got != testGroqKey {
		t.Fatalf("groq_api_key after the queue = %q", got)
	}
	if st, _ := companionGroqStatus.Load().(companionGroqStatusInfo); !st.Set || st.Tail != "WXYZ" {
		t.Errorf("status copy after saving = %+v", st)
	}
}
