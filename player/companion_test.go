package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
)

func newTestCompanion(t *testing.T) (*companionServer, *[]companionEvent) {
	t.Helper()
	events := &[]companionEvent{}
	srv := newCompanionServer("123456", t.TempDir(), func(ev companionEvent) {
		*events = append(*events, ev)
	})
	return srv, events
}

func companionRequest(method, target, pin string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	if pin != "" {
		req.Header.Set("X-Companion-Pin", pin)
	}
	return req
}

// The phone remote labels buttons by meaning (A = confirm, X = the X action).
// On a handheld the event queue swaps A/B and X/Y again, so the injected event
// must be pre-flipped or a phone A arrives at the scene as Back and a phone X
// as Y.
func TestCompanionButtonForMatchesAppSemantics(t *testing.T) {
	unswapped := &Config{Variables: Variables{Custom: map[string]interface{}{
		"swapABButtons": false, "swapXYButtons": false,
	}}}
	swapped := &Config{Variables: Variables{Custom: map[string]interface{}{
		"swapABButtons": true, "swapXYButtons": true,
	}}}
	for _, tc := range []struct {
		name   string
		config *Config
		want   uint8
	}{
		{"a", unswapped, sdl.CONTROLLER_BUTTON_A},
		{"b", unswapped, sdl.CONTROLLER_BUTTON_B},
		{"x", unswapped, sdl.CONTROLLER_BUTTON_X},
		{"y", unswapped, sdl.CONTROLLER_BUTTON_Y},
		// With the firmware swaps enabled, push the mirrored button so
		// applyButtonSwap turns it back before any scene handler sees it.
		{"a", swapped, sdl.CONTROLLER_BUTTON_B},
		{"b", swapped, sdl.CONTROLLER_BUTTON_A},
		{"x", swapped, sdl.CONTROLLER_BUTTON_Y},
		{"y", swapped, sdl.CONTROLLER_BUTTON_X},
		// The D-pad is never part of either swap.
		{"up", swapped, sdl.CONTROLLER_BUTTON_DPAD_UP},
	} {
		if got, ok := companionButtonFor(tc.name, tc.config); !ok || got != tc.want {
			t.Errorf("phone %q = %d, %v; want %d", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := companionButtonFor("not-a-button", unswapped); ok {
		t.Fatal("unknown phone key must not map to an SDL button")
	}
}

// Full pipeline check: a phone face button is pre-flipped for a handheld, then
// the main loop's applyButtonSwap flips it back. The scene must see what the
// phone pressed, never its mirror (A as B, X as Y).
func TestCompanionASurvivesTheHandheldButtonSwap(t *testing.T) {
	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		"swapABButtons": true, "swapXYButtons": true,
	}}}
	for _, tc := range []struct {
		name string
		want uint8
	}{
		{"a", sdl.CONTROLLER_BUTTON_A},
		{"b", sdl.CONTROLLER_BUTTON_B},
		{"x", sdl.CONTROLLER_BUTTON_X},
		{"y", sdl.CONTROLLER_BUTTON_Y},
	} {
		btn, ok := companionButtonFor(tc.name, config)
		if !ok {
			t.Fatalf("phone %q did not map to a controller button", tc.name)
		}
		event := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: btn}
		applyButtonSwap(config, event)
		if event.Button != tc.want {
			t.Fatalf("phone %q reached the scene as button %d, want %d", tc.name, event.Button, tc.want)
		}
	}
}

func TestSanitizeUploadName(t *testing.T) {
	cases := map[string]string{
		"holiday.mp4":                   "holiday.mp4",
		"../../etc/passwd":              "passwd",
		`C:\Users\me\video.mp4`:         "video.mp4",
		"..":                            "upload",
		".":                             "upload",
		"":                              "upload",
		"  spaced name.txt  ":           "spaced name.txt",
		"bad\x00name\x01.txt":           "badname.txt",
		"/absolute/path/photo (1).jpeg": "photo (1).jpeg",
		"a/b/c/../d.tar.gz":             "d.tar.gz",
		"notes 10:15?.txt":              "notes 10_15_.txt",
	}
	for in, want := range cases {
		if got := sanitizeUploadName(in); got != want {
			t.Errorf("sanitizeUploadName(%q) = %q, want %q", in, got, want)
		}
	}

	long := strings.Repeat("x", 120) + ".mp4"
	got := sanitizeUploadName(long)
	if utf8.RuneCountInString(got) > 80 {
		t.Fatalf("long name kept %d runes, want <= 80", utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("long name lost its extension: %q", got)
	}
}

func TestCreateCompanionFileNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	first, name, err := createCompanionFile(dir, "clip.mp4")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	io.WriteString(first, "one")
	first.Close()

	second, name2, err := createCompanionFile(dir, "clip.mp4")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	io.WriteString(second, "two")
	second.Close()

	if name != "clip.mp4" || name2 != "clip (2).mp4" {
		t.Fatalf("names = %q, %q", name, name2)
	}
	content, err := os.ReadFile(filepath.Join(dir, "clip.mp4"))
	if err != nil || string(content) != "one" {
		t.Fatalf("original file was overwritten: %q, %v", content, err)
	}
}

func TestCompanionAuth(t *testing.T) {
	srv, _ := newTestCompanion(t)
	if !srv.authorized(companionRequest(http.MethodPost, "/api/key", "123456", nil)) {
		t.Fatal("correct header PIN was rejected")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/key?p=123456", nil)
	if !srv.authorized(req) {
		t.Fatal("query PIN from the QR link was rejected")
	}
	if srv.authorized(companionRequest(http.MethodPost, "/api/key", "12345", nil)) {
		t.Fatal("short PIN was accepted")
	}
	if srv.authorized(companionRequest(http.MethodPost, "/api/key", "654321", nil)) {
		t.Fatal("wrong PIN was accepted")
	}
	if srv.authorized(httptest.NewRequest(http.MethodPost, "/api/key", nil)) {
		t.Fatal("missing PIN was accepted")
	}
}

func TestCompanionPageRequiresPIN(t *testing.T) {
	srv, _ := newTestCompanion(t)
	handler := srv.handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired page status = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Send text") {
		t.Fatal("unpaired page leaked the companion UI")
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, companionRequest(http.MethodGet, "/?p=123456", "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("paired page status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"JukaHub Companion", "Send text", "/api/upload", "data-key=up"} {
		if !strings.Contains(body, want) {
			t.Errorf("companion page is missing %q", want)
		}
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Error("companion page must not be cached")
	}
}

func TestCompanionKeyWhitelist(t *testing.T) {
	srv, events := newTestCompanion(t)
	handler := srv.handler()

	good, _ := json.Marshal(map[string]string{"key": "A"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/key", "123456", bytes.NewReader(good)))
	if rec.Code != http.StatusOK {
		t.Fatalf("whitelisted key status = %d", rec.Code)
	}
	if len(*events) != 1 || (*events)[0] != (companionEvent{Kind: companionEventKey, Text: "a"}) {
		t.Fatalf("events = %+v", *events)
	}

	bad, _ := json.Marshal(map[string]string{"key": "f13"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/key", "123456", bytes.NewReader(bad)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown key status = %d, want 400", rec.Code)
	}
	if len(*events) != 1 {
		t.Fatalf("unknown key still queued an event: %+v", *events)
	}
}

func TestCompanionOpenURLRejectsNonLinks(t *testing.T) {
	srv, events := newTestCompanion(t)
	handler := srv.handler()

	for _, bad := range []string{"", "not a link", "javascript:alert(1)", "file:///etc/passwd"} {
		payload, _ := json.Marshal(map[string]string{"url": bad})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/open", "123456", bytes.NewReader(payload)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("url %q status = %d, want 400", bad, rec.Code)
		}
	}
	if len(*events) != 0 {
		t.Fatalf("rejected links queued events: %+v", *events)
	}

	payload, _ := json.Marshal(map[string]string{"url": "https://youtu.be/dQw4w9WgXcQ"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/open", "123456", bytes.NewReader(payload)))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid link status = %d", rec.Code)
	}
	if len(*events) != 1 || (*events)[0].Kind != companionEventURL {
		t.Fatalf("events = %+v", *events)
	}
}

func TestCompanionUploadSavesIntoDropFolder(t *testing.T) {
	srv, events := newTestCompanion(t)
	handler := srv.handler()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "../../holiday.mp4")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("video-bytes"))
	writer.Close()

	req := companionRequest(http.MethodPost, "/api/upload", "123456", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp.Name != "holiday.mp4" || resp.Size != int64(len("video-bytes")) {
		t.Fatalf("response = %+v", resp)
	}
	saved := filepath.Join(srv.root, companionDropDir, "holiday.mp4")
	content, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("uploaded file missing: %v", err)
	}
	if string(content) != "video-bytes" {
		t.Fatalf("saved content = %q", content)
	}
	if len(*events) != 1 || (*events)[0].Kind != companionEventUpload {
		t.Fatalf("events = %+v", *events)
	}

	// A second upload with the same name must not clobber the first.
	body.Reset()
	writer = multipart.NewWriter(&body)
	part, _ = writer.CreateFormFile("file", "holiday.mp4")
	part.Write([]byte("second"))
	writer.Close()
	req = companionRequest(http.MethodPost, "/api/upload", "123456", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second upload status = %d", rec.Code)
	}
	if content, _ := os.ReadFile(saved); string(content) != "video-bytes" {
		t.Fatalf("second upload overwrote the first: %q", content)
	}
}

func TestCompanionRejectsUnpairedAPI(t *testing.T) {
	srv, events := newTestCompanion(t)
	payload := []byte(`{"key":"a"}`)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, "/api/key", "", bytes.NewReader(payload)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(*events) != 0 {
		t.Fatalf("unpaired request queued an event: %+v", *events)
	}
}

func TestCompanionThrottlesGuessing(t *testing.T) {
	srv, _ := newTestCompanion(t)
	handler := srv.handler()
	for i := 0; i < companionFailLimit; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, companionRequest(http.MethodGet, "/?p=000000", "", nil))
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, companionRequest(http.MethodGet, "/?p=123456", "", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status after %d failures = %d, want 429", companionFailLimit, rec.Code)
	}
}

// Each spent PIN budget locks guessing out for twice as long as the last, so a
// script on the network cannot keep trying forty PINs a minute.
func TestCompanionLockoutEscalates(t *testing.T) {
	srv, _ := newTestCompanion(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	const addr = "10.0.0.5"
	for i := 0; i < companionFailLimit; i++ {
		srv.noteFailure(addr, now)
	}
	// 45 s later the 30 s window has passed: the first lockout is over and
	// the next one doubles.
	if srv.throttled(addr, now.Add(45*time.Second)) {
		t.Fatal("the first lockout outlived its 30 s window")
	}
	for i := 0; i < companionFailLimit; i++ {
		srv.noteFailure(addr, now.Add(45*time.Second))
	}
	if !srv.throttled(addr, now.Add(45*time.Second)) {
		t.Fatal("the second lockout did not escalate past 30 s")
	}
	srv.noteSuccess(addr)
	if srv.throttled(addr, now.Add(46*time.Second)) {
		t.Fatal("the right PIN did not clear the escalation")
	}
}

func TestCompanionQueueIsDrainedAndCapped(t *testing.T) {
	resetCompanionQueue()
	for i := 0; i < companionQueueSize+10; i++ {
		enqueueCompanion(companionEvent{Kind: companionEventKey, Text: "a"})
	}
	events := drainCompanionEvents()
	if len(events) != companionQueueSize {
		t.Fatalf("queue kept %d events, want %d", len(events), companionQueueSize)
	}
	if again := drainCompanionEvents(); again != nil {
		t.Fatalf("second drain returned %d events", len(again))
	}
	enqueueCompanion(companionEvent{Kind: companionEventText, Text: "x"})
	resetCompanionQueue()
	if events := drainCompanionEvents(); len(events) != 0 {
		t.Fatalf("reset left %d events", len(events))
	}
}

// The Home header draws the pairing code without the bitmap's own four-module
// border, so the module size comes from the tile: the ~100px tile the band has
// gives a real pairing URL 3px modules, where the old tile drew 1px ones.
func TestCompanionHeaderQRFillsItsTile(t *testing.T) {
	code, err := qrcode.New(companionURL("192.168.100.200", 18080, "123456"), qrcode.Low)
	if err != nil {
		t.Fatal(err)
	}
	bitmap := code.Bitmap()
	core := qrCoreModules(bitmap)
	n := len(core)
	if n != len(bitmap)-8 || len(core[0]) != n {
		t.Fatalf("core is %dx%d from a %d-module bitmap, want its 4-module border gone", n, len(core[0]), len(bitmap))
	}
	if !core[0][0] || !core[n-1][0] || !core[0][n-1] {
		t.Fatal("the core does not start on the finder patterns")
	}
	if m := headerQRModule(100, int32(n)); m < 3 {
		t.Fatalf("a 100px tile draws %dpx modules for a %d-module code, want 3", m, n)
	}
	if m := headerQRModule(30, int32(n)); m != 0 {
		t.Fatalf("a 30px tile still draws %dpx modules", m)
	}

	// The rects cover exactly the dark modules, one row of modules each.
	dark := 0
	for _, row := range core {
		for _, on := range row {
			if on {
				dark++
			}
		}
	}
	area := int32(0)
	for _, r := range qrModuleRects(core, 10, 20, 3) {
		if r.H != 3 || r.W%3 != 0 || (r.Y-20)%3 != 0 || (r.X-10)%3 != 0 {
			t.Fatalf("rect %+v is off the 3px module grid", r)
		}
		area += r.W * r.H
	}
	if area != int32(dark)*9 {
		t.Fatalf("rects cover %d px, the %d dark modules are %d px", area, dark, dark*9)
	}

	blank := [][]bool{{false, false}, {false, false}}
	if got := qrCoreModules(blank); len(got) != 2 {
		t.Fatalf("a bitmap with no dark module was trimmed to %d rows", len(got))
	}
}

// The header band really gives the code that room: on the Smart Pro's and the
// Brick's canvases the tile Home lays out (under a status corner ending at
// y=44) draws a pairing URL's 29-module code with 3px modules, and stays clear
// of the ring a focused, lifted Media card draws under it.
func TestHomeHeaderQRTileFitsTheBand(t *testing.T) {
	for _, c := range []struct{ w, h int32 }{{1280, 720}, {960, 720}} {
		layout := NewHomeLayout(c.w, c.h)
		qr := layout.headerQRRect(c.w-scaleX(homeSideMargin, c.w), 44)
		if m := headerQRModule(qr.W, 29); m < 3 {
			t.Fatalf("%dx%d: tile %+v draws %dpx modules, want 3", c.w, c.h, qr, m)
		}
		media := layout.Items[HomeFocusMedia].Rect
		ringTop := media.Y - 3 - (max32(2, scaleX(homeFocusRingGap, c.w)) + homeFocusRingWidth)
		if qr.Y+qr.H > ringTop {
			t.Fatalf("%dx%d: tile %+v reaches the focused Media card's ring at y=%d", c.w, c.h, qr, ringTop)
		}
		if qr.Y < 44 {
			t.Fatalf("%dx%d: tile %+v climbs into the status corner", c.w, c.h, qr)
		}
	}
}

// The phone's voice page and its /api/voice call both want the Companion PIN,
// and the page keeps its requests on the handheld.
func TestVoicePhonePageNeedsThePIN(t *testing.T) {
	srv, _ := newTestCompanion(t)
	h := srv.handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, companionRequest(http.MethodGet, "/voice", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/voice without a PIN: %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, companionRequest(http.MethodGet, "/voice?p=123456", "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/voice with the PIN: %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/api/voice") || !strings.Contains(body, "enterkeyhint=send") {
		t.Fatal("the voice page does not post to /api/voice from a send field")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") {
		t.Fatalf("voice page CSP %q must keep requests on the handheld", csp)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/voice", "", strings.NewReader(`{"text":"open youtube"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/voice without a PIN: %d, want 401", rec.Code)
	}
}

// The PlayStation card's /api/remote-play wants the Companion PIN, and its
// state carries the saved Account-ID only shortened, never whole.
func TestRemotePlayCardNeedsThePINAndHidesTheAccountID(t *testing.T) {
	psUseTempHosts(t)
	if err := savePSHosts(psHostsFile{Settings: psRemoteSettings{Account: "FYHpffQQIhE="}}); err != nil {
		t.Fatal(err)
	}
	srv, events := newTestCompanion(t)
	h := srv.handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, companionRequest(http.MethodGet, "/api/remote-play", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET without a PIN: %d, want 401", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/remote-play", "654321", strings.NewReader(`{"action":"install"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST with a wrong PIN: %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, companionRequest(http.MethodGet, "/api/remote-play", "123456", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"shown":"FYHp…E="`) {
		t.Fatalf("GET with the PIN: %d %s", rec.Code, body)
	}
	if strings.Contains(body, "FYHpffQQIhE=") {
		t.Fatal("the card's state carried the whole Account-ID")
	}
	if len(*events) != 0 {
		t.Fatalf("the card queued phone events: %+v", *events)
	}
}
