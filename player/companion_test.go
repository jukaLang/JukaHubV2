package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/png"
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
	for _, want := range []string{"JukaHub Companion", "Send text", "/api/drop/plan", "data-key=up"} {
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

// The Game Boy Printer card serves only the files the scrapbook lists, from
// inside the scrapbook folder, behind the PIN: a crafted id or an edited list
// reaches nothing else, and the list names no paths.
func TestGBPrintCompanionServesOnlyScrapbookFiles(t *testing.T) {
	root := t.TempDir()
	prevRoot := gbpRootDir
	gbpRootDir = func() string { return root }
	gbpStore.mu.Lock()
	gbpStore.loaded = false
	gbpStore.mu.Unlock()
	t.Cleanup(func() {
		gbpRootDir = prevRoot
		gbpStore.mu.Lock()
		gbpStore.loaded, gbpStore.index = false, gbpIndex{}
		gbpStore.mu.Unlock()
	})
	var pic bytes.Buffer
	if err := png.Encode(&pic, image.NewGray(image.Rect(0, 0, 160, 16))); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(root, gbpScrapbookDirName, "game-abc123", "0001.png"): pic.Bytes(),
		filepath.Join(root, "secret.png"):                                   []byte("not for the phone"),
	}
	for name, data := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	index := `{"version":1,"next":5,"prints":[
		{"id":"p1","file":"game-abc123/0001.png","game":"Pokemon Yellow","rom":"/mnt/SDCARD/Roms/GB/Yellow.gb","order":1,"w":160,"h":16},
		{"id":"p2","file":"../secret.png","order":2},
		{"id":"p3","file":"/etc/passwd","order":3},
		{"id":"../p4","file":"game-abc123/0001.png","order":4}]}`
	if err := os.WriteFile(filepath.Join(root, gbpIndexName), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _ := newTestCompanion(t)
	h := srv.handler()
	get := func(target, pin string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, companionRequest(http.MethodGet, target, pin, nil))
		return rec
	}
	if rec := get("/api/gbprint/image?id=p1", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired picture: status %d, want 401", rec.Code)
	}
	if rec := get("/api/gbprint/image?id=p1", "123456"); rec.Code != http.StatusOK ||
		rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), pic.Bytes()) {
		t.Fatalf("print p1: status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, id := range []string{"p2", "p3", "p4", "../secret.png", "p1/../../secret.png", "%2e%2e%2fsecret.png", "/etc/passwd"} {
		if rec := get("/api/gbprint/image?id="+id, "123456"); rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "not for the phone") {
			t.Errorf("id %q: status %d, body %q", id, rec.Code, rec.Body.String())
		}
	}
	if rec := get("/api/gbprint/image?id=p1&scale=3", "123456"); rec.Code != http.StatusBadRequest {
		t.Errorf("scale 3: status %d, want 400", rec.Code)
	}
	rec := get("/api/gbprint/image?id=p1&scale=4&dl=1", "123456")
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("4x download disposition %q", rec.Header().Get("Content-Disposition"))
	}
	big, err := png.Decode(rec.Body)
	if rec.Code != http.StatusOK || err != nil || big.Bounds().Dx() != 640 || big.Bounds().Dy() != 64 {
		t.Fatalf("4x: status %d, err %v", rec.Code, err)
	}
	rec = get("/api/gbprint", "123456")
	var list struct {
		Prints []gbpCompanionPrint `json:"prints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Prints) != 1 || list.Prints[0].ID != "p1" {
		t.Fatalf("list: %s", rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "Roms") || strings.Contains(body, "secret") || strings.Contains(body, root) {
		t.Fatalf("the list names paths: %s", body)
	}
	for rel, want := range map[string]bool{
		"game-1a2b3c/0001.png": true, "0001.png": true, "../x.png": false, "a/b/c.png": false,
		`a\b.png`: false, ".hidden/x.png": false, "/x.png": false, "C:x.png": false, "a/./b.png": false, "a/b.txt": false,
	} {
		if got := gbpSafeRel(rel); got != want {
			t.Errorf("gbpSafeRel(%q) = %v, want %v", rel, got, want)
		}
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

// The Email and Calendars cards (companion_mailcal.go): the route needs the
// PIN, and what the phone types is checked before any server is asked.
func TestMailCalPhoneInputValidation(t *testing.T) {
	srv, _ := newTestCompanion(t)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodGet, "/api/mailcal", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired GET status = %d, want 401", rec.Code)
	}
	for _, c := range []struct {
		raw, want string
		ok        bool
	}{
		{"webcal://p01-calendars.icloud.com/x.ics", "https://p01-calendars.icloud.com/x.ics", true},
		{"calendar.google.com/calendar/ical/a/private-b/basic.ics", "https://calendar.google.com/calendar/ical/a/private-b/basic.ics", true},
		{"http://192.168.1.20:5232/user/cal/", "http://192.168.1.20:5232/user/cal/", true},
		{"http://nas.local/dav/", "http://nas.local/dav/", true},
		{"http://example.com/cal.ics", "", false},
		{"https://user:pass@example.com/cal.ics", "", false},
		{"ftp://example.com/cal.ics", "", false},
		{"https://example.com/a b.ics", "", false},
	} {
		got, err := mailcalCleanCalURL(c.raw)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("mailcalCleanCalURL(%q) = %q, %v; want %q, ok %v", c.raw, got, err, c.want, c.ok)
		}
	}
	for _, bad := range []string{"https://imap.example.com", "imap.example.com:993", "-x.com", "a..b"} {
		if _, err := mailcalCleanHost(bad); err == nil {
			t.Errorf("mailcalCleanHost(%q) accepted", bad)
		}
	}
	if h, err := mailcalCleanHost("IMAP.Example.com."); err != nil || h != "imap.example.com" {
		t.Errorf("mailcalCleanHost = %q, %v", h, err)
	}
	for _, p := range []string{"0", "65536", "99999", "abc", "-1"} {
		if _, err := mailcalCleanPort(p); err == nil {
			t.Errorf("mailcalCleanPort(%q) accepted", p)
		}
	}
	if _, err := mailcalCleanSec("ssl"); err == nil {
		t.Error("mailcalCleanSec accepted ssl")
	}
	for _, bad := range []string{"", "no-at-sign", "a@b", "a b@example.com", "a@@example.com", "a@example.com\nBcc: x"} {
		if _, err := mailcalCleanEmail(bad); err == nil {
			t.Errorf("mailcalCleanEmail(%q) accepted", bad)
		}
	}
	if _, err := mailcalCleanSecret("line\nbreak"); err == nil {
		t.Error("mailcalCleanSecret accepted a line break")
	}
}

// The drop card's routing (companion_drop_route.go) decides from what a phone
// sends: file names, a file's first bytes and a zip's directory.
func TestCompanionDropRoute(t *testing.T) {
	zipOf := func(names ...string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, n := range names {
			w, err := zw.Create(n)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(n, "/") {
				_, _ = w.Write([]byte("x"))
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	var many []string
	for i := 0; i < 400; i++ {
		many = append(many, strings.Repeat("track", 30)+itoa(i)+".bin")
	}
	bigZip := zipOf(many...)
	gbHead := make([]byte, 0x150)
	copy(gbHead[0x104:], rommGBLogo)
	psISO := make([]byte, companionDropHeadBytes)
	copy(psISO[16*2048:], "\x01CD001")
	copy(psISO[16*2048+8:], "PLAYSTATION")
	single := zipOf("Pokemon Blue.gb", "readme.txt")
	port := zipOf("Celeste.sh", "celeste/", "celeste/port.json")
	neogeo := zipOf("201-p1.p1", "201-c1.c1", "201-m1.m1")
	ff7 := "/Roms/PS/Final Fantasy VII/Final Fantasy VII (Disc 1).cue"
	base := companionDropEnv{
		Roms: "/Roms", RomsDirs: []string{"GB", "Game Boy Advance (GBA)", "PS", "Ports"},
		BIOS: "/BIOS", Music: "/Music", Video: "/Videos", Drop: "/PhoneDrop",
		Games: []companionDropGame{
			{Path: "/Roms/GB/Pokemon Red.gb", Stem: "Pokemon Red", System: "Game Boy"},
			{Path: "/Roms/GB/Tetris.gb", Stem: "Tetris", System: "Game Boy"},
			{Path: ff7, Stem: "Final Fantasy VII (Disc 1)", System: "PlayStation"},
		},
	}
	for _, c := range []struct {
		in     companionDropIn
		nextui bool
		noRoms bool
		kind   string
		dir    string
		file   string
		first  string // an "ask" answer's first choice, or a "need" answer's part
	}{
		{in: companionDropIn{Name: "Pokemon Blue.gb"}, kind: "game", dir: "/Roms/GB", file: "Pokemon Blue.gb"},
		{in: companionDropIn{Name: "Zelda.gba"}, kind: "game", dir: "/Roms/GBA"},
		{in: companionDropIn{Name: "Zelda.gba"}, nextui: true, kind: "game", dir: "/Roms/Game Boy Advance (GBA)"},
		{in: companionDropIn{Name: "Mario.gbc"}, kind: "game", dir: "/Roms/GB"},
		{in: companionDropIn{Name: "Zelda.gba"}, noRoms: true, kind: "drop", dir: "/PhoneDrop"},
		{in: companionDropIn{Name: "Pitfall.a26"}, kind: "drop", dir: "/PhoneDrop"},
		{in: companionDropIn{Name: "Pokemon Blue.gb", Plain: true}, kind: "drop", dir: "/PhoneDrop"},
		{in: companionDropIn{Name: "song.mp3"}, kind: "music", dir: "/Music"},
		{in: companionDropIn{Name: "clip.mp4"}, kind: "video", dir: "/Videos"},
		{in: companionDropIn{Name: "notes.txt"}, kind: "drop", dir: "/PhoneDrop"},
		{in: companionDropIn{Name: "README.md", Head: []byte("# notes")}, kind: "drop", dir: "/PhoneDrop"},
		{in: companionDropIn{Name: "scph5501.bin", Size: 512 << 10}, kind: "bios", dir: "/BIOS"},
		{in: companionDropIn{Name: "dc_boot.bin", Size: 2 << 20}, kind: "bios", dir: "/BIOS/dc"},
		{in: companionDropIn{Name: "neocd_f.rom", Size: 512 << 10}, kind: "bios", dir: "/BIOS/neocd"},
		{in: companionDropIn{Name: "bios7.bin", Size: 2 << 20}, kind: "need", first: "head"},
		{in: companionDropIn{Name: "game.bin", Size: 32 << 10, Head: gbHead}, kind: "game", dir: "/Roms/GB"},
		{in: companionDropIn{Name: "game.iso", Size: 4 << 30, Head: psISO}, kind: "game", dir: "/Roms/PS2"},
		{in: companionDropIn{Name: "game.iso", Size: 600 << 20, Head: psISO}, kind: "ask", first: "ps1"},
		{in: companionDropIn{Name: "game.chd"}, kind: "ask", first: "ps1"},
		{in: companionDropIn{Name: "game.chd", System: "ps1"}, kind: "game", dir: "/Roms/PS"},
		{in: companionDropIn{Name: "game.zip"}, kind: "need", first: "tail"},
		{in: companionDropIn{Name: "blue.zip", Size: int64(len(single)), Tail: single}, kind: "game", dir: "/Roms/GB", file: "blue.zip"},
		{in: companionDropIn{Name: "celeste.zip", Size: int64(len(port)), Tail: port}, kind: "port", dir: "/PhoneDrop"},
		{in: companionDropIn{Name: "mslug.zip", Size: int64(len(neogeo)), Tail: neogeo}, kind: "game", dir: "/Roms/NEOGEO"},
		{in: companionDropIn{Name: "big.zip", Size: int64(len(bigZip)), Tail: bigZip[len(bigZip)-10000:]}, kind: "ask", first: "nes"},
		{in: companionDropIn{Name: "Pokemon Red (USA).png"}, kind: "cover", dir: "/Roms/GB/Imgs", file: "Pokemon Red.png"},
		{in: companionDropIn{Name: "tetris.JPG"}, nextui: true, kind: "cover", dir: "/Roms/GB/.media", file: "Tetris.jpg"},
		{in: companionDropIn{Name: "Pokemon Rd.png"}, kind: "ask", first: "/Roms/GB/Pokemon Red.gb"},
		{in: companionDropIn{Name: "ff7.webp", Game: ff7}, kind: "cover", dir: "/Roms/PS/Final Fantasy VII/Imgs", file: "Final Fantasy VII (Disc 1).webp"},
	} {
		env := base
		env.NextUI = c.nextui
		if c.noRoms {
			env.Roms, env.RomsDirs = "", nil
		}
		got := companionDropRoute(c.in, env)
		if got.Kind != c.kind || got.Dir != c.dir || (c.file != "" && got.Name != c.file) {
			t.Errorf("%s: got %s %q %q, want %s %q %q", c.in.Name, got.Kind, got.Dir, got.Name, c.kind, c.dir, c.file)
			continue
		}
		switch c.kind {
		case "ask":
			if len(got.Choices) == 0 || got.Choices[0].ID != c.first {
				t.Errorf("%s: choices %+v, want %s first", c.in.Name, got.Choices, c.first)
			}
		case "need":
			if got.Need != c.first || got.NeedBytes <= 0 {
				t.Errorf("%s: need %q (%d bytes), want %s", c.in.Name, got.Need, got.NeedBytes, c.first)
			}
		}
	}
}

// A whole folder (companion_dropdir.go) is filed by what is inside it, from
// its listing alone.
func TestCompanionDropDirRoute(t *testing.T) {
	gbHead := make([]byte, 0x150)
	copy(gbHead[0x104:], rommGBLogo)
	env := companionDropDirEnv{
		companionDropEnv: companionDropEnv{
			Roms: "/Roms", RomsDirs: []string{"GB", "GBA", "PS", "DOS", "Ports"},
			BIOS: "/BIOS", Music: "/Music", Video: "/Videos", Drop: "/PhoneDrop",
		},
		Books: "/Books", Pictures: "/Pictures",
	}
	for _, c := range []struct {
		name  string
		paths []string
		as    string
		head  bool
		kind  string
		dir   string
		first string // an "ask" answer's first choice ("" none), or a "need" answer's part
	}{
		{name: "Asimov", paths: []string{"Foundation.epub", "I, Robot.epub", "cover.jpg"}, kind: "books", dir: "/Books/Asimov"},
		{name: "Books", paths: []string{"a.epub", "b.cbz"}, kind: "books", dir: "/Books"},
		{name: "Abbey Road", paths: []string{"01 Come Together.flac", "02 Something.flac", "Abbey Road.cue", "Abbey Road.log", "booklet.pdf", "cover.jpg"}, kind: "music", dir: "/Music/Abbey Road"},
		{name: "Live", paths: []string{"Live.wav", "Live.cue"}, kind: "music", dir: "/Music/Live"},
		{name: "Holiday", paths: []string{"IMG_1.jpg", "IMG_2.heic", "IMG_3.png", "notes.txt"}, kind: "pictures", dir: "/Pictures/Holiday"},
		{name: "Series", paths: []string{"S01/ep1.mkv", "S01/ep1.srt", "S01/ep2.mkv"}, kind: "video", dir: "/Videos/Series"},
		{name: "Mix", paths: []string{"a.mp3", "b.mp3", "c.mp4", "d.mp4"}, kind: "ask", first: "music"},
		{name: "Final Fantasy VII", paths: []string{"FF7 (Disc 1).cue", "FF7 (Disc 1).bin", "FF7 (Disc 2).cue", "FF7 (Disc 2).bin", "FF7.m3u"}, kind: "need", first: "head"},
		{name: "Final Fantasy VII", paths: []string{"FF7 (Disc 1).cue", "FF7 (Disc 1).bin"}, as: "ps1", kind: "game", dir: "/Roms/PS/Final Fantasy VII"},
		{name: "Crash (USA)", paths: []string{"Crash.chd"}, kind: "ask", first: "ps1"},
		{name: "PS", paths: []string{"Crash.chd", "Spyro.chd"}, kind: "game", dir: "/Roms/PS"},
		{name: "Shenmue", paths: []string{"Shenmue.gdi", "track01.bin", "track02.raw", "track03.bin"}, kind: "game", dir: "/Roms/DC/Shenmue"},
		{name: "Homebrew", paths: []string{"a.bin", "b.bin"}, head: true, kind: "game", dir: "/Roms/GB/Homebrew"},
		{name: "GBA", paths: []string{"Zelda.gba", "Metroid.gba"}, kind: "game", dir: "/Roms/GBA"},
		{name: "Pokemon Hacks", paths: []string{"a.gba", "b.gba", "Imgs/a.png", "readme.txt"}, kind: "game", dir: "/Roms/GBA/Pokemon Hacks"},
		{name: "Pocket", paths: []string{"a.gb", "b.gbc"}, kind: "game", dir: "/Roms/GB/Pocket"},
		{name: "Mixed", paths: []string{"a.gba", "b.nes"}, kind: "ask", first: ""},
		{name: "DOOM", paths: []string{"DOOM.EXE", "DOOM.WAD", "SETUP.EXE", "DEFAULT.CFG", "music/e1m1.mid"}, kind: "dos", dir: "/Roms/DOS/DOOM"},
		{name: "Prince", paths: []string{"PRINCE.EXE", "dosboxPrince.conf", "DOSBOX/DOSBox.exe", "DOSBOX/SDL.dll"}, kind: "dos", dir: "/Roms/DOS/Prince"},
		{name: "King's Quest IV", paths: []string{"SIERRA.EXE", "RESOURCE.MAP", "RESOURCE.001"}, kind: "scummvm", dir: "/Roms/SCUMMVM/King's Quest IV"},
		{name: "Steel Sky", paths: []string{"sky.dnr", "sky.dsk", "readme.txt"}, kind: "scummvm", dir: "/Roms/SCUMMVM/Steel Sky"},
		{name: "Celeste", paths: []string{"Celeste.sh", "celeste/port.json", "celeste/gamedata/data.bin"}, kind: "port", dir: "/PhoneDrop/Celeste"},
		{name: "Tool", paths: []string{"tool.so", "readme.txt"}, kind: "drop", dir: "/PhoneDrop/Tool"},
		{name: "Windows Game", paths: []string{"game.exe", "engine.dll", "data.pak"}, kind: "drop", dir: "/PhoneDrop/Windows Game"},
		{name: "Stuff", paths: []string{"a.docx", "b.xlsx"}, kind: "drop", dir: "/PhoneDrop/Stuff"},
		{name: "Papers", paths: []string{"a.pdf", "b.pdf"}, as: "books", kind: "books", dir: "/Books/Papers"},
		{name: "Papers", paths: []string{"a.pdf", "b.pdf"}, as: "drop", kind: "drop", dir: "/PhoneDrop/Papers"},
	} {
		var listing []companionDropDirFile
		for _, p := range c.paths {
			listing = append(listing, companionDropDirFile{Path: p, Size: 1 << 20})
		}
		in := companionDropDirIn{Name: c.name, Files: companionDropDirCheck(listing), As: c.as}
		if c.head {
			in.Head = gbHead
		}
		got := companionDropDirRoute(in, env)
		if got.Kind != c.kind || got.Dir != c.dir {
			t.Errorf("%s: got %s %q, want %s %q", c.name, got.Kind, got.Dir, c.kind, c.dir)
			continue
		}
		switch c.kind {
		case "ask":
			first := ""
			if len(got.Choices) > 0 {
				first = got.Choices[0].ID
			}
			if first != c.first {
				t.Errorf("%s: choices %+v, want %q first", c.name, got.Choices, c.first)
			}
		case "need":
			if got.Need != c.first || got.NeedBytes <= 0 || !strings.HasSuffix(in.Files[got.NeedFile].Rel, ".bin") {
				t.Errorf("%s: need %q of file %d (%d bytes), want the %s of a .bin", c.name, got.Need, got.NeedFile, got.NeedBytes, c.first)
			}
		}
	}
	if got := companionDropDirSummary("3 games", []companionDropDirDone{{Name: "Asimov", Kind: "books", Count: 42, Files: 43}}); got != "3 games and the folder Asimov (42 books)" {
		t.Errorf("summary: %q", got)
	}
}

// A folder's relative paths stay inside the folder and are names the card's
// FAT holds.
func TestCompanionDropRelPath(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Foundation.epub", "Foundation.epub"},
		{"Disc 1/Track 01.flac", "Disc 1/Track 01.flac"},
		{`Sub\file.txt`, "Sub/file.txt"},
		{"./a//b/./c.txt", "a/b/c.txt"},
		{"notes 10:15.txt", "notes 10_15.txt"},
		{"name. /x.", "name/x"},
		{"CON.txt", "_CON.txt"},
		{"saves/aux", "saves/_aux"},
		{"com1.bin", "_com1.bin"},
		{"COM10.bin", "COM10.bin"},
		{"../etc/passwd", ""},
		{"a/../../b", ""},
		{"/etc/passwd", ""},
		{`C:\Windows\x.dll`, ""},
		{"c:x", ""},
		{"a\x00b", ""},
		{"a\tb", ""},
		{".DS_Store", ""},
		{"__MACOSX/._a.jpg", ""},
		{"Pics/Thumbs.db", ""},
		{"...", ""},
		{"", ""},
		{strings.Repeat("d/", 12) + "f", ""},
	} {
		if got, _, _ := companionDropRelPath(c.in); got != c.want {
			t.Errorf("companionDropRelPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	got := companionDropDirCheck([]companionDropDirFile{{Path: "A.txt"}, {Path: "a.txt"}, {Path: "x"}, {Path: "x/y"}, {Path: ".hidden"}})
	if got[0].Rel != "A.txt" || got[1].Rel != "" || got[2].Rel != "" || got[3].Rel != "x/y" || got[4].Rel != "" || !got[4].Quiet {
		t.Errorf("companionDropDirCheck: %+v", got)
	}
}

// Renaming or deleting a folder JukaHub depends on needs "confirm": without it
// the server answers 409 with the reason and changes nothing; with it the
// same request goes through. An ordinary folder deeper down needs nothing
// (companion_guard.go).
func TestCompanionFilesImportantFolderNeedsConfirm(t *testing.T) {
	card := t.TempDir()
	for _, d := range []string{"Roms/GBA", "Roms/Mine/sub", "BIOS"} {
		if err := os.MkdirAll(filepath.Join(card, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prev := companionFileShares
	companionFileShares = func() []ftpShare {
		return []ftpShare{{name: "Roms", dir: filepath.Join(card, "Roms")}, {name: "BIOS", dir: filepath.Join(card, "BIOS")}}
	}
	t.Cleanup(func() { companionFileShares = prev })
	// The Files card asks the game profiles which folders run programs.
	prevProfiles := gameProfilesDirOverride
	gameProfilesDirOverride = t.TempDir()
	invalidateGameProfileCache()
	t.Cleanup(func() {
		gameProfilesDirOverride = prevProfiles
		invalidateGameProfileCache()
	})
	srv, _ := newTestCompanion(t)
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, path, "123456", strings.NewReader(body)))
		return rec
	}

	rec := post("/api/files/rename", `{"path":"/Roms/GBA","name":"GBA2"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("rename without confirm: status %d (%s), want 409", rec.Code, rec.Body.String())
	}
	var ans struct {
		NeedsConfirm bool   `json:"needsConfirm"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ans); err != nil || !ans.NeedsConfirm || !strings.Contains(ans.Reason, "Game Boy Advance") {
		t.Fatalf("409 answer = %+v (%v), want needsConfirm with the console's name", ans, err)
	}
	if _, err := os.Stat(filepath.Join(card, "Roms", "GBA")); err != nil {
		t.Fatalf("the folder changed without confirm: %v", err)
	}
	if rec = post("/api/files/rename", `{"path":"/Roms/GBA","name":"GBA2","confirm":true}`); rec.Code != http.StatusOK {
		t.Fatalf("rename with confirm: status %d (%s)", rec.Code, rec.Body.String())
	}
	if rec = post("/api/files/delete", `{"path":"/Roms/GBA2"}`); rec.Code != http.StatusConflict {
		t.Fatalf("delete without confirm: status %d, want 409", rec.Code)
	}
	if rec = post("/api/files/delete", `{"path":"/Roms/GBA2","confirm":true}`); rec.Code != http.StatusOK {
		t.Fatalf("delete with confirm: status %d (%s)", rec.Code, rec.Body.String())
	}
	if rec = post("/api/files/rename", `{"path":"/Roms/Mine/sub","name":"sub2"}`); rec.Code != http.StatusOK {
		t.Fatalf("an ordinary folder asked for confirm: status %d (%s)", rec.Code, rec.Body.String())
	}

	for _, c := range []struct {
		segs  []string
		dir   bool
		wants string
	}{
		{[]string{"Apps", "JukaHub", "jukauser.json"}, false, "settings"},
		{[]string{"Apps"}, true, "holds"},
		{[]string{"PORTMASTER. "}, true, "PortMaster"},
		{[]string{"Saves"}, true, "saves"},
		{[]string{"Roms", ".hidden"}, true, ""},
		{[]string{"Roms", "GBA", "game.gba"}, false, ""},
		{[]string{"Music", "Album"}, true, ""},
	} {
		got := companionGuardReasonFor(c.segs, c.dir, nil)
		if (c.wants == "") != (got == "") || !strings.Contains(got, c.wants) {
			t.Errorf("companionGuardReasonFor(%q) = %q, want one with %q", c.segs, got, c.wants)
		}
	}
}

// Download logs masks what identifies the person and the place — the names
// it is given, network names, e-mail, hardware and LAN addresses, tokens —
// and keeps what explains a problem (logredact.go redactLogPersonal).
func TestRedactLogPersonalMasksNamesAndAddresses(t *testing.T) {
	names := logPersonalNames{Wifi: []string{"Smith Home 5G"}, Accounts: []string{"jukafan"}}
	in := "wifi: join of Smith Home 5G failed; ssid=\"Cafe Net\" bssid=aa:bb:cc:dd:ee:ff ip 192.168.1.23 gw 10.0.0.1 dns 1.1.1.1\n" +
		"ra: signed in as jukafan (jukafanatic stays) mail bob@example.com bt /org/bluez/hci0/dev_11_22_33_44_55_66 fe80::1c2b:3cff:fe4d:5e6f%wlan0 token=abc123secret\n"
	out := redactLogPersonal(in, names)
	for _, gone := range []string{"Smith Home 5G", "Cafe Net", "aa:bb:cc", "192.168.1.23", "10.0.0.1", "bob@example.com", "11_22_33", "fe80::1c2b", "abc123secret", "as jukafan "} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"1.1.1.1", "jukafanatic", "wifi: join of", "[wifi]", "[account]", "[lan-ip]", "[mac]", "[email]", "dev_[mac]"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q missing:\n%s", kept, out)
		}
	}
}
