package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestCompanionPINIsSixDigits(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		pin, err := companionNewPIN()
		if err != nil {
			t.Fatalf("companionNewPIN: %v", err)
		}
		if len(pin) != 6 {
			t.Fatalf("PIN %q has length %d, want 6", pin, len(pin))
		}
		for _, r := range pin {
			if r < '0' || r > '9' {
				t.Fatalf("PIN %q contains a non-digit", pin)
			}
		}
		seen[pin] = true
	}
	// Fifty draws from a million values colliding every time would mean the
	// generator is not random at all.
	if len(seen) < 40 {
		t.Fatalf("only %d distinct PINs in 50 draws", len(seen))
	}
}

func TestCompanionURLCarriesPIN(t *testing.T) {
	got := companionURL("192.168.1.42", 8317, "123456")
	want := "http://192.168.1.42:8317/?p=123456"
	if got != want {
		t.Fatalf("companionURL = %q, want %q", got, want)
	}
}

// The phone remote labels buttons by meaning (A = confirm). On a handheld the
// event queue swaps A/B again, so the injected event must be pre-flipped or a
// phone A arrives at the scene as Back.
func TestCompanionButtonForMatchesAppSemantics(t *testing.T) {
	if got, ok := companionButtonFor("a", false); !ok || got != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("phone A on unswapped config = %d, %v; want SDL A", got, ok)
	}
	if got, ok := companionButtonFor("b", false); !ok || got != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("phone B on unswapped config = %d, %v; want SDL B", got, ok)
	}
	// With the firmware swap enabled, push B for phone A so applyButtonSwap
	// turns it back into A before any scene handler sees it.
	if got, ok := companionButtonFor("a", true); !ok || got != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("phone A on swapped config = %d, %v; want pre-swapped SDL B", got, ok)
	}
	if got, ok := companionButtonFor("b", true); !ok || got != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("phone B on swapped config = %d, %v; want pre-swapped SDL A", got, ok)
	}
	if got, ok := companionButtonFor("up", true); !ok || got != sdl.CONTROLLER_BUTTON_DPAD_UP {
		t.Fatalf("D-pad must not be swapped, got %d, %v", got, ok)
	}
	if _, ok := companionButtonFor("not-a-button", false); ok {
		t.Fatal("unknown phone key must not map to an SDL button")
	}
}

// Full pipeline check: a phone A is pre-flipped for a handheld, then the main
// loop's applyButtonSwap flips it back. The scene must see A, never B.
func TestCompanionASurvivesTheHandheldButtonSwap(t *testing.T) {
	config := &Config{Variables: Variables{Custom: map[string]interface{}{"swapABButtons": true}}}
	btn, ok := companionButtonFor("a", abButtonsSwapped(config))
	if !ok {
		t.Fatal("phone A did not map to a controller button")
	}
	event := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: btn}
	applyButtonSwap(config, event)
	if event.Button != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("phone A reached the scene as button %d, want A", event.Button)
	}

	btn, _ = companionButtonFor("b", abButtonsSwapped(config))
	event = &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: btn}
	applyButtonSwap(config, event)
	if event.Button != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("phone B reached the scene as button %d, want B", event.Button)
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

func TestCompanionTextChunksRespectRunes(t *testing.T) {
	text := "héllo wörld"
	chunks := companionTextChunks(text, 4)
	if strings.Join(chunks, "") != text {
		t.Fatalf("chunks lost characters: %q", chunks)
	}
	for _, c := range chunks {
		if len(c) > 4 {
			t.Fatalf("chunk %q is %d bytes, over the limit", c, len(c))
		}
		if !utf8.ValidString(c) {
			t.Fatalf("chunk %q is not valid UTF-8", c)
		}
	}

	// A multi-byte rune larger than the budget must still come through whole.
	emoji := companionTextChunks("🎮", 2)
	if len(emoji) != 1 || emoji[0] != "🎮" {
		t.Fatalf("emoji chunking = %q", emoji)
	}
	if got := companionTextChunks("", 4); len(got) != 0 {
		t.Fatalf("empty text produced %q", got)
	}
}

func TestCompanionAuth(t *testing.T) {
	srv, _ := newTestCompanion(t)
	req := httptest.NewRequest(http.MethodPost, "/api/key", nil)
	if !srv.authorized(companionRequest(http.MethodPost, "/api/key", "123456", nil)) {
		t.Fatal("correct header PIN was rejected")
	}
	req = httptest.NewRequest(http.MethodPost, "/api/key?p=123456", nil)
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

func TestCompanionTextSendsLinesAndEnter(t *testing.T) {
	srv, events := newTestCompanion(t)
	payload, _ := json.Marshal(map[string]interface{}{"text": "ls -la\ncd /", "enter": true})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, "/api/text", "123456", bytes.NewReader(payload)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	want := []companionEvent{
		{Kind: companionEventText, Text: "ls -la"},
		{Kind: companionEventKey, Text: "enter"},
		{Kind: companionEventText, Text: "cd /"},
		{Kind: companionEventKey, Text: "enter"},
	}
	if len(*events) != len(want) {
		t.Fatalf("events = %+v, want %+v", *events, want)
	}
	for i := range want {
		if (*events)[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, (*events)[i], want[i])
		}
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

func TestCompanionStatusLine(t *testing.T) {
	saved := companion
	defer func() { companion = saved }()

	companion = companionOverlay{}
	if got := companionStatusLine(); got != "Stopped" {
		t.Fatalf("closed status = %q", got)
	}
	companion = companionOverlay{open: true, pin: "123456"}
	if got := companionStatusLine(); !strings.Contains(got, "123456") {
		t.Fatalf("open status = %q, want the PIN", got)
	}
	companion = companionOverlay{open: true, err: "port busy"}
	if got := companionStatusLine(); !strings.Contains(got, "port busy") {
		t.Fatalf("error status = %q", got)
	}
}

func TestCompanionURLRoundTrips(t *testing.T) {
	srv, _ := newTestCompanion(t)
	u := companionURL("127.0.0.1", 8317, srv.pin)
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("generated URL does not parse: %v", err)
	}
	if got := parsed.Query().Get("p"); got != srv.pin {
		t.Fatalf("URL PIN = %q, want %q", got, srv.pin)
	}
}

// TestCompanionEnsureStartedBuildsHomeQR covers the background path: the
// listener and QR must exist before anyone opens the pairing card, because the
// home header is the primary pairing affordance.
func TestCompanionEnsureStartedBuildsHomeQR(t *testing.T) {
	if localIPv4() == "" {
		t.Skip("no LAN address on this machine")
	}
	prev := companion
	prevRetry, prevRefresh := companionNextRetry, companionNextRefresh
	prevRect := companionHeaderRect
	t.Cleanup(func() {
		shutdownCompanion()
		companion = prev
		companionNextRetry, companionNextRefresh = prevRetry, prevRefresh
		companionHeaderRect = prevRect
	})
	companion = companionOverlay{}
	companionNextRetry, companionNextRefresh = time.Time{}, time.Time{}

	companionEnsureStarted()
	if companion.err != "" {
		t.Fatalf("background companion failed to start: %s", companion.err)
	}
	if companion.srv == nil || !companion.srv.Running() {
		t.Fatal("background companion did not bind a listener")
	}
	if len(companion.pin) != 6 {
		t.Fatalf("PIN = %q, want six digits", companion.pin)
	}
	if len(companion.qr) == 0 {
		t.Fatal("background companion did not build the home QR")
	}
	if !strings.Contains(companion.url, companion.pin) {
		t.Fatalf("QR URL %q does not carry the PIN", companion.url)
	}

	// Closing the large card must not take the home QR offline.
	closeCompanionOverlay()
	if companion.srv == nil || !companion.srv.Running() {
		t.Fatal("closing the pairing card stopped the background listener")
	}
}

// TestCompanionOverlayStartsAndServes walks the real open path the Tools row
// uses: generate a PIN, bind the listener, build the QR, answer the page, and
// release the port when the card closes.
func TestCompanionOverlayStartsAndServes(t *testing.T) {
	if localIPv4() == "" {
		t.Skip("no LAN address on this machine")
	}
	prev := companion
	t.Cleanup(func() {
		shutdownCompanion()
		companion = prev
	})
	companion = companionOverlay{}

	openCompanionOverlay()
	if !companion.open {
		t.Fatal("openCompanionOverlay did not mark the card open")
	}
	if companion.err != "" {
		t.Fatalf("companion failed to start: %s", companion.err)
	}
	if companion.srv == nil || !companion.srv.Running() {
		t.Fatal("companion server is not running")
	}
	if len(companion.pin) != 6 {
		t.Fatalf("PIN = %q, want six digits", companion.pin)
	}
	if len(companion.qr) == 0 {
		t.Fatal("no QR bitmap was built")
	}
	if !strings.Contains(companion.url, companion.pin) {
		t.Fatalf("URL %q does not carry the PIN", companion.url)
	}

	resp, err := http.Get(companion.url)
	if err != nil {
		t.Fatalf("GET %s: %v", companion.url, err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(page), "JukaHub Companion") {
		t.Fatal("LAN page did not return the companion UI")
	}

	// The QR the user scans must decode to the URL the server actually serves.
	if got := companionURL(localIPv4(), companion.srv.Port(), companion.pin); got != companion.url {
		t.Fatalf("QR URL %q does not match the listening address %q", companion.url, got)
	}

	toggleCompanionOverlay() // open -> close
	if companion.open {
		t.Fatal("card stayed open after toggling")
	}
	// The listener now stays up so the home-header QR keeps working; it is
	// released by shutdownCompanion or a new PIN, not by closing the card.
	if companion.srv == nil || !companion.srv.Running() {
		t.Fatal("closing the card released the listener needed by the home QR")
	}
	if len(companion.qr) == 0 {
		t.Fatal("closing the card discarded the home QR bitmap")
	}
}
