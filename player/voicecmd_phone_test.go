package main

// voicecmd_phone_test.go — the phone's /voice page and /api/voice call,
// without a renderer: the main-loop hand-off is replaced by the test.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// voicePhoneTestSeams swaps the main-loop hand-off for the test and puts the
// production values back afterwards.
func voicePhoneTestSeams(t *testing.T, post func(func()), run func(voicePhoneRequest) voicePhoneReply, wait time.Duration) {
	t.Helper()
	// The remembered requests are global: a second -count pass (or another
	// test) must not replay this one's stored answers.
	voicePhoneJobs.mu.Lock()
	voicePhoneJobs.byID, voicePhoneJobs.order = nil, nil
	voicePhoneJobs.mu.Unlock()
	oldPost, oldRun, oldWait := voicePhonePost, voicePhoneRun, voicePhoneWait
	voicePhonePost, voicePhoneRun, voicePhoneWait = post, run, wait
	t.Cleanup(func() { voicePhonePost, voicePhoneRun, voicePhoneWait = oldPost, oldRun, oldWait })
}

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

func TestVoicePhoneAnswersFromTheMainLoop(t *testing.T) {
	var got voicePhoneRequest
	voicePhoneTestSeams(t,
		func(fn func()) { fn() },
		func(req voicePhoneRequest) voicePhoneReply {
			got = req
			return voicePhoneReply{OK: true, Say: "Opening Media", Heard: req.Text}
		},
		time.Second)
	srv, _ := newTestCompanion(t)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, "/api/voice", "123456",
		strings.NewReader(`{"text":"  open youtube  "}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got.Text != "open youtube" {
		t.Fatalf("the main loop got %q, want the trimmed command", got.Text)
	}
	var rep voicePhoneReply
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("reply is not JSON: %v", err)
	}
	if !rep.OK || rep.Say != "Opening Media" {
		t.Fatalf("reply %+v", rep)
	}
}

func TestVoicePhoneRejectsEmptyAndLongCommands(t *testing.T) {
	ran := false
	voicePhoneTestSeams(t, func(fn func()) { fn() }, func(voicePhoneRequest) voicePhoneReply {
		ran = true
		return voicePhoneReply{OK: true}
	}, time.Second)
	srv, _ := newTestCompanion(t)
	h := srv.handler()
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"text":"   "}`, http.StatusBadRequest},
		{`{"text":"` + strings.Repeat("a", voiceMaxCommandRunes+1) + `"}`, http.StatusRequestEntityTooLarge},
		{`not json`, http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/voice", "123456", strings.NewReader(tc.body)))
		if rec.Code != tc.want {
			t.Errorf("%.40q: %d, want %d", tc.body, rec.Code, tc.want)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, companionRequest(http.MethodGet, "/api/voice", "123456", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/voice: %d, want 405", rec.Code)
	}
	if ran {
		t.Fatal("a rejected request reached the main loop")
	}
}

// While a game has the screen the main loop is parked: the phone hears that,
// and the command it gave up on never runs later.
func TestVoicePhoneBusyCommandIsAbandoned(t *testing.T) {
	var parked []func()
	ran := false
	voicePhoneTestSeams(t,
		func(fn func()) { parked = append(parked, fn) },
		func(voicePhoneRequest) voicePhoneReply {
			ran = true
			return voicePhoneReply{OK: true}
		},
		30*time.Millisecond)
	srv, _ := newTestCompanion(t)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, "/api/voice", "123456",
		strings.NewReader(`{"text":"open youtube"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("busy handheld: %d, want 503", rec.Code)
	}
	var rep voicePhoneReply
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil || !strings.Contains(rep.Say, "busy") ||
		!strings.Contains(rep.Say, "Nothing ran") {
		t.Fatalf("busy reply %q (%v)", rec.Body.String(), err)
	}
	// The game ends; the main loop drains what was queued meanwhile.
	for _, fn := range parked {
		fn()
	}
	if ran {
		t.Fatal("a command the phone gave up on ran after the main loop came back")
	}
}

// A command the main loop already started is never reported as "busy": the
// handler waits on for its answer (a slow radio switch, say).
func TestVoicePhoneSlowCommandStillAnswers(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	voicePhoneTestSeams(t,
		func(fn func()) { go fn() },
		func(voicePhoneRequest) voicePhoneReply {
			close(started)
			<-release
			return voicePhoneReply{OK: true, Say: "Wi-Fi off"}
		},
		500*time.Millisecond)
	srv, _ := newTestCompanion(t)
	h := srv.handler()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/voice", "123456",
			strings.NewReader(`{"text":"wifi off","id":"slow-1"}`)))
		done <- rec
	}()
	// A runner too busy to start the command within the wait lets the handler
	// claim it first; that must fail the test, not hang it.
	select {
	case <-started:
	case rec := <-done:
		t.Fatalf("answered %d before the command started: %s", rec.Code, rec.Body.String())
	}
	// Past the first wait: the handler lost the claim to the running command.
	time.Sleep(600 * time.Millisecond)
	close(release)
	rec := <-done
	if rec.Code != http.StatusOK {
		t.Fatalf("a running command was answered %d (%s), want its own answer", rec.Code, rec.Body.String())
	}
	var rep voicePhoneReply
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil || rep.Say != "Wi-Fi off" {
		t.Fatalf("reply %q (%v)", rec.Body.String(), err)
	}
}

// Try again sends the same id: a command that ran answers again from memory
// and never runs twice; one that never ran (busy) runs on the retry.
func TestVoicePhoneRetryNeverRunsTwice(t *testing.T) {
	runs := 0
	voicePhoneTestSeams(t, func(fn func()) { fn() }, func(req voicePhoneRequest) voicePhoneReply {
		runs++
		return voicePhoneReply{OK: true, Say: "Volume 60%", Heard: req.Text}
	}, time.Second)
	srv, _ := newTestCompanion(t)
	h := srv.handler()
	var replies []string
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/voice", "123456",
			strings.NewReader(`{"text":"louder","id":"retry-twice-1"}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("send %d: %d", i+1, rec.Code)
		}
		replies = append(replies, rec.Body.String())
	}
	if runs != 1 {
		t.Fatalf("the same send ran %d times, want once", runs)
	}
	if replies[0] != replies[1] {
		t.Fatalf("the retry answered %q, want the first answer %q", replies[1], replies[0])
	}

	// Busy first: nothing ran, so the retry with that id runs it.
	var parked []func()
	voicePhoneTestSeams(t, func(fn func()) { parked = append(parked, fn) }, func(voicePhoneRequest) voicePhoneReply {
		runs++
		return voicePhoneReply{OK: true, Say: "Opening Media"}
	}, 20*time.Millisecond)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/voice", "123456",
		strings.NewReader(`{"text":"open youtube","id":"retry-busy-1"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("parked main loop: %d, want 503", rec.Code)
	}
	runs = 0
	voicePhoneTestSeams(t, func(fn func()) { fn() }, func(voicePhoneRequest) voicePhoneReply {
		runs++
		return voicePhoneReply{OK: true, Say: "Opening Media"}
	}, time.Second)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/voice", "123456",
		strings.NewReader(`{"text":"open youtube","id":"retry-busy-1"}`)))
	if rec.Code != http.StatusOK || runs != 1 {
		t.Fatalf("retry after busy: %d with %d runs, want 200 and one run", rec.Code, runs)
	}
	for _, fn := range parked {
		fn()
	}
	if runs != 1 {
		t.Fatalf("the abandoned first send ran after all (%d runs)", runs)
	}
}

func TestVoicePhoneCleanID(t *testing.T) {
	for id, want := range map[string]string{
		"abc-123_x":             "abc-123_x",
		"lq9x2-k3j4h5g6":        "lq9x2-k3j4h5g6",
		"":                      "",
		"has space":             "",
		"<script>":              "",
		strings.Repeat("a", 41): "",
	} {
		if got := voicePhoneCleanID(id); got != want {
			t.Errorf("voicePhoneCleanID(%q) = %q, want %q", id, got, want)
		}
	}
}

// A pick names a row of the list the phone was sent; an old list is refused.
func TestVoicePhonePickNeedsTheCurrentList(t *testing.T) {
	opened := ""
	matches := []voiceMatch{
		{Target: voiceTarget{Title: "Super Mario Bros 3", Where: "NES", Kind: voiceTargetGame,
			Action: func(*Config) { opened = "smb3" }}, Score: 859},
		{Target: voiceTarget{Title: "Super Mario World", Where: "SNES", Kind: voiceTargetGame,
			Action: func(*Config) { opened = "smw" }}, Score: 859},
	}
	choices, gen := voicePhoneRemember(matches)
	if len(choices) != 2 || choices[1].Title != "Super Mario World" || choices[1].Subtitle != "SNES" {
		t.Fatalf("choices %+v", choices)
	}
	if rep := voicePhonePick(nil, voicePhoneRequest{Pick: 2, Gen: gen + 1}); rep.OK || opened != "" {
		t.Fatalf("a stale list opened %q (%+v)", opened, rep)
	}
	if rep := voicePhonePick(nil, voicePhoneRequest{Pick: 3, Gen: gen}); rep.OK || opened != "" {
		t.Fatalf("a pick past the list opened %q (%+v)", opened, rep)
	}
	rep := voicePhonePick(nil, voicePhoneRequest{Pick: 2, Gen: gen})
	if !rep.OK || opened != "smw" {
		t.Fatalf("pick 2 opened %q (%+v), want Super Mario World", opened, rep)
	}
}

func TestVoicePhoneURLFromTheCompanionLink(t *testing.T) {
	old := companion.url
	t.Cleanup(func() { companion.url = old })
	companion.url = "http://192.168.1.20:8317/?p=123456"
	if got := voicePhoneURL(); got != "http://192.168.1.20:8317/voice?p=123456" {
		t.Fatalf("voicePhoneURL = %q", got)
	}
	companion.url = ""
	if got := voicePhoneURL(); got != "" {
		t.Fatalf("no companion address must give no link, got %q", got)
	}
}
