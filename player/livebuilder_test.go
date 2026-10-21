package main

// livebuilder_test.go exercises the Wi-Fi design receiver end to end at the HTTP
// level: the same handlers the handheld serves, a temporary config file, and a
// reload callback standing in for the watcher.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const liveBuilderTestCode = "482913"

func liveBuilderTestConfig(sceneName string) []byte {
	return []byte(`{
  "AppName": "JukaHub",
  "Version": "0.4.0",
  "Width": 1280,
  "Height": 720,
  "variables": {"fontSizes": {"medium": 24}},
  "scenes": [{
    "name": "` + sceneName + `",
    "elements": [{"type": "label", "text": "Hello", "x": 10, "y": 10, "width": 200, "height": 40}]
  }]
}`)
}

func newLiveBuilderTestServer(t *testing.T, initial []byte) (*liveBuilderServer, string, *int32) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jukaconfig.json")
	if initial != nil {
		if err := os.WriteFile(path, initial, 0o644); err != nil {
			t.Fatalf("write initial config: %v", err)
		}
	}
	var reloads int32
	srv := newLiveBuilderServer(path, liveBuilderTestCode, func() { atomic.AddInt32(&reloads, 1) })
	return srv, path, &reloads
}

func liveBuilderRequest(t *testing.T, handler http.Handler, method, path string, body []byte, code string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if code != "" {
		req.Header.Set("X-Live-Builder-Code", code)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestLiveBuilderHealthIsPublicAndCORS(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	handler := srv.handler()

	rec := liveBuilderRequest(t, handler, http.MethodGet, "/api/health", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS origin = %q, want *", got)
	}
	var health liveBuilderHealth
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatalf("health JSON: %v", err)
	}
	if !health.OK || health.Service != "jukahub-live-builder" || !health.CodeRequired {
		t.Errorf("health = %+v, want ok live-builder requiring a code", health)
	}

	preflight := liveBuilderRequest(t, handler, http.MethodOptions, "/api/design", nil, "")
	if preflight.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", preflight.Code)
	}
	if got := preflight.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-Live-Builder-Code") {
		t.Errorf("preflight allow-headers = %q, want the pairing header", got)
	}
}

func TestLiveBuilderPushRequiresCorrectCode(t *testing.T) {
	initial := liveBuilderTestConfig("Original")
	srv, path, reloads := newLiveBuilderTestServer(t, initial)
	handler := srv.handler()

	for _, code := range []string{"", "000000", "48291"} {
		rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", liveBuilderTestConfig("Hijacked"), code)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("code %q: status = %d, want 401", code, rec.Code)
		}
	}
	if atomic.LoadInt32(reloads) != 0 {
		t.Error("unauthorised push triggered a reload")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, initial) {
		t.Error("unauthorised push modified the config file")
	}
}

func TestLiveBuilderPushWritesBacksUpAndReloads(t *testing.T) {
	initial := liveBuilderTestConfig("Original")
	srv, path, reloads := newLiveBuilderTestServer(t, initial)
	handler := srv.handler()

	body := []byte(`{
  "AppName": "JukaHub",
  "Version": "0.4.0",
  "Width": 1024,
  "Height": 768,
  "futureTopLevel": {"kept": true},
  "variables": {"fontSizes": {"big": 40}},
  "scenes": [{
    "name": "Pushed",
    "elements": [
      {"type": "button", "text": "Go", "x": 20, "y": 30, "width": 200, "height": 60, "futureElementField": "kept"}
    ]
  }]
}`)
	rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", body, liveBuilderTestCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("push status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var result liveBuilderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("push response: %v", err)
	}
	if !result.OK || result.Scenes != 1 || result.Elements != 1 || !result.Reloaded {
		t.Errorf("push response = %+v, want ok/1 scene/1 element/reloaded", result)
	}
	if atomic.LoadInt32(reloads) != 1 {
		t.Errorf("reload calls = %d, want 1", atomic.LoadInt32(reloads))
	}

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if !bytes.Equal(backup, initial) {
		t.Error("backup does not contain the previous design")
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(saved, &doc); err != nil {
		t.Fatalf("saved config is not JSON: %v", err)
	}
	if doc["schemaVersion"] != float64(ConfigSchemaVersion) {
		t.Errorf("schemaVersion = %v, want %d", doc["schemaVersion"], ConfigSchemaVersion)
	}
	if _, ok := doc["futureTopLevel"]; !ok {
		t.Error("unknown top-level field was stripped from the pushed design")
	}
	scenes := doc["scenes"].([]interface{})
	elements := scenes[0].(map[string]interface{})["elements"].([]interface{})
	if _, ok := elements[0].(map[string]interface{})["futureElementField"]; !ok {
		t.Error("unknown element field was stripped from the pushed design")
	}
}

func TestLiveBuilderRejectsInvalidDesignWithoutTouchingFile(t *testing.T) {
	initial := liveBuilderTestConfig("Original")
	srv, path, reloads := newLiveBuilderTestServer(t, initial)
	handler := srv.handler()

	cases := []struct {
		name string
		body []byte
	}{
		{"not json", []byte(`{"scenes": [`)},
		{"array instead of object", []byte(`[]`)},
		{"no scenes", []byte(`{"AppName":"JukaHub","scenes":[]}`)},
		{"empty element type", []byte(`{"scenes":[{"name":"Main","elements":[{"type":"","x":0,"y":0}]}]}`)},
		{"newer schema", []byte(`{"schemaVersion":99,"scenes":[{"name":"Main","elements":[]}]}`)},
	}
	for _, tc := range cases {
		rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", tc.body, liveBuilderTestCode)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", tc.name, rec.Code)
		}
	}
	if atomic.LoadInt32(reloads) != 0 {
		t.Error("invalid design triggered a reload")
	}
	onDisk, _ := os.ReadFile(path)
	if !bytes.Equal(onDisk, initial) {
		t.Error("invalid design modified the config file")
	}
}

func TestLiveBuilderPullsCurrentDesign(t *testing.T) {
	initial := liveBuilderTestConfig("Pulled")
	srv, _, _ := newLiveBuilderTestServer(t, initial)
	handler := srv.handler()

	rec := liveBuilderRequest(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("pull content-type = %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), initial) {
		t.Error("pull did not return the config bytes")
	}
	if rec := liveBuilderRequest(t, handler, http.MethodGet, "/api/design", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unpaired pull status = %d, want 401", rec.Code)
	}
}

func TestLiveBuilderRejectsOversizedBody(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	big := bytes.Repeat([]byte("x"), liveBuilderMaxBody+1)
	rec := liveBuilderRequest(t, srv.handler(), http.MethodPost, "/api/design", big, liveBuilderTestCode)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized push status = %d, want 413", rec.Code)
	}
}

func TestLiveBuilderPairingCodePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), liveBuilderCodeFile)
	first, err := loadOrCreateLiveBuilderCode(path)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d{6}$`).MatchString(first) {
		t.Errorf("generated code %q is not six digits", first)
	}
	second, err := loadOrCreateLiveBuilderCode(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("code changed between reads: %q then %q", first, second)
	}
}

func TestLiveBuilderStatusPageOnlyShowsCodeToTheDevice(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	handler := srv.handler()

	remote := httptest.NewRequest(http.MethodGet, "/", nil)
	remote.RemoteAddr = "192.168.1.77:4567"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, remote)
	if rec.Code != http.StatusOK {
		t.Fatalf("status page = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), liveBuilderTestCode) {
		t.Error("status page exposed the pairing code to a remote client")
	}

	local := httptest.NewRequest(http.MethodGet, "/", nil)
	local.RemoteAddr = "127.0.0.1:4567"
	localRec := httptest.NewRecorder()
	handler.ServeHTTP(localRec, local)
	if !strings.Contains(localRec.Body.String(), liveBuilderTestCode) {
		t.Error("status page on the device did not show the pairing code")
	}
}

func TestNotifyWrittenReloadsBeforeThePollInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jukaconfig.json")
	if err := os.WriteFile(path, liveBuilderTestConfig("First"), 0o644); err != nil {
		t.Fatal(err)
	}
	watcher := NewConfigWatcher(path)
	reloaded := make(chan string, 1)
	watcher.SetOnReload(func(cfg *Config) {
		if len(cfg.Scenes) > 0 {
			reloaded <- cfg.Scenes[0].Name
		}
	})
	watcher.Start()
	defer watcher.Stop()

	// Let Start record the current mtime.
	time.Sleep(50 * time.Millisecond)
	if err := writeFileForTest(path, liveBuilderTestConfig("Second")); err != nil {
		t.Fatal(err)
	}
	watcher.NotifyWritten(path)

	select {
	case name := <-reloaded:
		if name != "Second" {
			t.Errorf("reloaded scene = %q, want Second", name)
		}
	case <-time.After(1200 * time.Millisecond):
		// The poll interval is 2s, so a timeout here means the immediate path
		// did not fire; the watcher would still catch it later.
		t.Fatal("NotifyWritten did not reload within 1.2s (before the 2s poll)")
	}
}

func writeFileForTest(path string, data []byte) error {
	tmp := path + ".testtmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
