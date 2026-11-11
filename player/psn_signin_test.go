package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The Account-ID is the user_id as 8 little-endian bytes in Base64: always 12
// characters, and the console's own decoder (ps4DecodeAccountID) reads the
// same number back.
func TestPSNAccountIDFromUserID(t *testing.T) {
	for _, tc := range []struct {
		id   uint64
		want string
	}{
		{1, "AQAAAAAAAAA="},
		{256, "AAEAAAAAAAA="},
		{1234567890123456789, "FYHpffQQIhE="},
		{^uint64(0), "//////////8="},
	} {
		got := psnAccountIDFromUserID(tc.id)
		if got != tc.want {
			t.Errorf("psnAccountIDFromUserID(%d) = %q, want %q", tc.id, got, tc.want)
		}
		if len(got) != 12 {
			t.Errorf("Account-ID %q is %d characters, want 12", got, len(got))
		}
		raw, offline, err := ps4DecodeAccountID(got)
		if err != nil || offline {
			t.Fatalf("ps4DecodeAccountID(%q) = offline %v, %v", got, offline, err)
		}
		if back := binary.LittleEndian.Uint64(raw[:]); back != tc.id {
			t.Errorf("%q decodes to user_id %d, want %d", got, back, tc.id)
		}
	}
}

// Whatever a person pastes where the Account-ID goes is read the same way:
// the Account-ID, the numbers other tools print, or the "redirect" address.
// The 8-digit PIN typed into the wrong field is caught, not sent to Sony.
func TestPSNParseAccountInput(t *testing.T) {
	const redirect = "https://remoteplay.dl.playstation.net/remoteplay/redirect"
	for _, tc := range []struct {
		in      string
		account string
		code    string
		bad     bool
	}{
		{in: "AQAAAAAAAAA=", account: "AQAAAAAAAAA="},
		{in: "  AQAAAAAAAAA \n", account: "AQAAAAAAAAA="},
		{in: "<AQAAAAAAAAA=>", account: "AQAAAAAAAAA="},
		{in: "1234567890123456789", account: "FYHpffQQIhE="},
		{in: "0x112210f47de98115", account: "FYHpffQQIhE="},
		{in: "0X112210F47DE98115", account: "FYHpffQQIhE="},
		{in: "__________8=", account: "//////////8="},
		{in: redirect + "?code=AbC123&cid=4f2a-11", code: "AbC123"},
		{in: "remoteplay.dl.playstation.net/remoteplay/redirect?code=AbC123", code: "AbC123"},
		{in: redirect + "#code=AbC123", code: "AbC123"},
		{in: "code=AbC123&cid=x", code: "AbC123"},
		{in: "?code=AbC123", code: "AbC123"},
		{in: "v3.AbC123", code: "v3.AbC123"},
		{in: "XyZ789", code: "XyZ789"},
		{in: "", bad: true},
		{in: "12345678", bad: true},
		{in: "1234 5678", bad: true},
		{in: "123", bad: true},
		{in: "AAAAAAAAAAA=", bad: true},
		{in: "AbCdEfGhIjKl", bad: true},
		{in: "hello world", bad: true},
		{in: psnLoginURL, bad: true},
		{in: redirect, bad: true},
		{in: redirect + "?error=access_denied&error_description=x", bad: true},
		{in: redirect + "?code=a%20b", bad: true},
		{in: strings.Repeat("A", psnInputLimit+1), bad: true},
	} {
		got, err := psnParseAccountInput(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("psnParseAccountInput(%q) = %+v, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("psnParseAccountInput(%q): %v", tc.in, err)
			continue
		}
		if got.AccountID != tc.account || got.Code != tc.code {
			t.Errorf("psnParseAccountInput(%q) = %+v, want account %q code %q", tc.in, got, tc.account, tc.code)
		}
	}
	if _, err := psnParseAccountInput("12345678"); err == nil || !strings.Contains(err.Error(), "PIN") {
		t.Errorf("the PIN in the Account-ID field must be named as the PIN, got %v", err)
	}
	if _, err := psnParseAccountInput(redirect + "?error=access_denied"); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("a refused sign-in must say why, got %v", err)
	}
}

// The sign-in link is chiaki's, byte for byte: Sony only returns to the
// "redirect" page for that exact client and redirect.
func TestPSNLoginURLIsChiakis(t *testing.T) {
	const want = "https://auth.api.sonyentertainmentnetwork.com/2.0/oauth/authorize?service_entity=urn:service-entity:psn" +
		"&response_type=code&client_id=ba495a24-818c-472b-b12d-ff231c1b5745" +
		"&redirect_uri=https://remoteplay.dl.playstation.net/remoteplay/redirect&scope=psn:clientapp" +
		"&request_locale=en_US&ui=pr&service_logo=ps&layout_type=popup&smcid=remoteplay&prompt=always" +
		"&PlatformPrivacyWs1=minimal&"
	if psnLoginURL != want {
		t.Fatalf("psnLoginURL = %q\nwant          %q", psnLoginURL, want)
	}
	if psnTokenURLDefault != "https://auth.api.sonyentertainmentnetwork.com/2.0/oauth/token" {
		t.Fatalf("psnTokenURLDefault = %q", psnTokenURLDefault)
	}
}

// psnUseTestServer points the exchange at a TLS test server for one test.
func psnUseTestServer(t *testing.T, handler http.Handler) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	prevURL, prevClient := psnTokenURL, psnClientOverride
	psnTokenURL = srv.URL + "/2.0/oauth/token"
	psnClientOverride = srv.Client()
	t.Cleanup(func() {
		psnTokenURL, psnClientOverride = prevURL, prevClient
		srv.Close()
		psnEndSignIn(nil)
	})
}

// psnFakeSony answers like Sony's token endpoint. The code GOOD1 buys the
// token tok-good-123, whose record carries user_id 1234567890123456789; the
// other codes are the ways a real exchange goes wrong.
func psnFakeSony() http.Handler {
	const base = "/2.0/oauth/token"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != psnClientID || pass != psnClientSecret {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == base:
			body, _ := io.ReadAll(r.Body)
			form, err := url.ParseQuery(string(body))
			if err != nil || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
				form.Get("grant_type") != "authorization_code" || form.Get("redirect_uri") != psnRedirectURI {
				http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
				return
			}
			switch form.Get("code") {
			case "GOOD1":
				io.WriteString(w, `{"access_token":"tok-good-123","token_type":"bearer"}`)
			case "NUMBER1":
				io.WriteString(w, `{"access_token":"tok-number-1"}`)
			case "NOUSER1":
				io.WriteString(w, `{"access_token":"tok-nouser-1"}`)
			case "BADJSON1":
				io.WriteString(w, `this is not json`)
			case "NOTOKEN1":
				io.WriteString(w, `{"token_type":"bearer"}`)
			case "BUSY1":
				http.Error(w, `{"error":"server_error"}`, http.StatusServiceUnavailable)
			case "ECHO1":
				// A refusal that quotes the code must not carry it to the screen.
				http.Error(w, `{"error":"invalid_request","error_description":"code ECHO1 is wrong"}`, http.StatusBadRequest)
			default:
				http.Error(w, `{"error":"invalid_grant","error_description":"Invalid authorization code"}`, http.StatusBadRequest)
			}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"/"):
			switch strings.TrimPrefix(r.URL.Path, base+"/") {
			case "tok-good-123":
				io.WriteString(w, `{"user_id":"1234567890123456789","online_id":"someone"}`)
			case "tok-number-1":
				io.WriteString(w, `{"user_id":1234567890123456789}`)
			case "tok-nouser-1":
				io.WriteString(w, `{"online_id":"someone"}`)
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	})
}

// The exchange is chiaki's two requests: code for token, token for user_id.
// Every way it fails is a plain sentence without the code or the token in it.
func TestPSNExchangeCode(t *testing.T) {
	psnUseTestServer(t, psnFakeSony())
	for _, tc := range []struct {
		code   string
		want   string
		errHas string
	}{
		{code: "GOOD1", want: "FYHpffQQIhE="},
		{code: "NUMBER1", want: "FYHpffQQIhE="},
		{code: "OLD1", errHas: "expired"},
		{code: "BADJSON1", errHas: "cannot read"},
		{code: "NOTOKEN1", errHas: "no access token"},
		{code: "NOUSER1", errHas: "no account number"},
		{code: "BUSY1", errHas: "HTTP 503"},
		{code: "ECHO1", errHas: "HTTP 400"},
	} {
		got, err := psnExchangeCode(context.Background(), tc.code)
		if tc.errHas == "" {
			if err != nil || got != tc.want {
				t.Errorf("psnExchangeCode(%s) = %q, %v; want %q", tc.code, got, err, tc.want)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.errHas) {
			t.Errorf("psnExchangeCode(%s) = %q, %v; want an error mentioning %q", tc.code, got, err, tc.errHas)
			continue
		}
		for _, secret := range []string{tc.code, "tok-good-123", "tok-nouser-1", "tok-number-1"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("psnExchangeCode(%s) error %q leaks %q", tc.code, err, secret)
			}
		}
	}
}

// The token travels in the account request's path: the exchange never talks
// to Sony over plain HTTP.
func TestPSNExchangeRefusesPlainHTTP(t *testing.T) {
	prev := psnTokenURL
	psnTokenURL = "http://127.0.0.1:9/2.0/oauth/token"
	t.Cleanup(func() { psnTokenURL = prev })
	if _, err := psnExchangeCode(context.Background(), "GOOD1"); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("a plain-HTTP endpoint must be refused, got %v", err)
	}
}

// net/http quotes the request URL in its errors, and the account request has
// the token in its path: neither it nor the code may reach the screen.
func TestPSNTransportErrorRedacts(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "https://auth.example/2.0/oauth/token/tok-secret-99",
		Err: errors.New("dial tcp 10.0.0.1:443: connect: connection refused (tok-secret-99, CODE-77)"),
	}
	msg := psnTransportError(err, "CODE-77", "tok-secret-99").Error()
	if strings.Contains(msg, "tok-secret-99") || strings.Contains(msg, "CODE-77") {
		t.Fatalf("transport error leaks a secret: %q", msg)
	}
	if !strings.Contains(msg, "connection refused") {
		t.Errorf("transport error lost its cause: %q", msg)
	}
	if got := psnTransportError(context.Canceled).Error(); !strings.Contains(got, "cancelled") {
		t.Errorf("cancel = %q", got)
	}
	if got := psnTransportError(context.DeadlineExceeded).Error(); !strings.Contains(got, "in time") {
		t.Errorf("timeout = %q", got)
	}
	if got := psnRedact(`Get "https://x/token/a%2Fb-secret": a/b-secret`, "a/b-secret"); strings.Contains(got, "secret") {
		t.Errorf("escaped secret survived: %q", got)
	}
	if got := psnRedact("an ordinary sentence", "an"); got != "an ordinary sentence" {
		t.Errorf("a short secret shredded the text: %q", got)
	}
}

// The phone page sits beside the companion page, on the same address and PIN.
func TestPSNPageURLFrom(t *testing.T) {
	for _, tc := range [][2]string{
		{"http://192.168.1.5:8317/?p=123456", "http://192.168.1.5:8317/psn?p=123456"},
		{"http://10.0.0.2:41234/?p=000001", "http://10.0.0.2:41234/psn?p=000001"},
		{"", ""},
		{"not a link", ""},
		{"ftp://192.168.1.5/?p=1", ""},
	} {
		if got := psnPageURLFrom(tc[0]); got != tc[1] {
			t.Errorf("psnPageURLFrom(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
	if got := psnPageCaption("http://192.168.1.5:8317/psn?p=123456"); got != "Or open 192.168.1.5:8317/psn?p=123456" {
		t.Errorf("psnPageCaption = %q", got)
	}
}

// The sign-in page is PIN-guarded like the companion page, carries Sony's
// link with its "&" escaped, and keeps its PIN out of the Referer it sends.
func TestCompanionPSNPageRequiresPIN(t *testing.T) {
	srv, _ := newTestCompanion(t)
	handler := srv.handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/psn", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired /psn status = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Sign in to PlayStation") {
		t.Fatal("unpaired /psn leaked the sign-in page")
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, companionRequest(http.MethodGet, "/psn?p=123456", "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("paired /psn status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Sign in to PlayStation", html.EscapeString(psnLoginURL), "/api/psn", "redirect"} {
		if !strings.Contains(body, want) {
			t.Errorf("sign-in page is missing %q", want)
		}
	}
	if strings.Contains(body, "{{LOGIN}}") {
		t.Error("sign-in page still has its placeholder")
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "connect-src 'self'") {
		t.Errorf("Content-Security-Policy = %q", got)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Error("sign-in page must not be cached")
	}
}

// /api/psn takes the PIN, then an Account-ID or a "redirect" address, and
// queues only a real Account-ID for the main loop.
func TestCompanionPSNAPI(t *testing.T) {
	psnUseTestServer(t, psnFakeSony())
	srv, events := newTestCompanion(t)
	handler := srv.handler()
	post := func(pin, input string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"input": input})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, companionRequest(http.MethodPost, "/api/psn", pin, bytes.NewReader(body)))
		return rec
	}

	if rec := post("", "AQAAAAAAAAA="); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired /api/psn status = %d, want 401", rec.Code)
	}
	if len(*events) != 0 {
		t.Fatalf("unpaired request queued an event: %+v", *events)
	}

	rec := post("123456", "AQAAAAAAAAA=")
	if rec.Code != http.StatusOK {
		t.Fatalf("Account-ID status = %d: %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		OK      bool   `json:"ok"`
		Account string `json:"account"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || !reply.OK || reply.Account != "AQAAAAAAAAA=" {
		t.Fatalf("reply = %+v, %v", reply, err)
	}
	if len(*events) != 1 || (*events)[0] != (companionEvent{Kind: companionEventPSN, Text: "AQAAAAAAAAA="}) {
		t.Fatalf("events = %+v", *events)
	}

	rec = post("123456", "https://remoteplay.dl.playstation.net/remoteplay/redirect?code=GOOD1&cid=x")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "FYHpffQQIhE=") {
		t.Fatalf("redirect address status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(*events) != 2 || (*events)[1].Text != "FYHpffQQIhE=" {
		t.Fatalf("events = %+v", *events)
	}

	rec = post("123456", "https://remoteplay.dl.playstation.net/remoteplay/redirect?code=OLD1")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("expired code status = %d: %s", rec.Code, rec.Body.String())
	}
	if busy, last := psnSignInState(); busy || !strings.Contains(last, "expired") {
		t.Errorf("sign-in state after a refusal = busy %v, %q", busy, last)
	}

	rec = post("123456", "12345678")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("PIN in the Account-ID field status = %d, want 422", rec.Code)
	}
	if len(*events) != 2 {
		t.Fatalf("a failed sign-in queued an event: %+v", *events)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, companionRequest(http.MethodGet, "/api/psn", "123456", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/psn status = %d, want 405", rec.Code)
	}

	// One exchange at a time: a second address waits for the first.
	if !psnBeginSignIn() {
		t.Fatal("the sign-in slot was left taken")
	}
	rec = post("123456", "code=GOOD1")
	psnEndSignIn(nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second sign-in status = %d, want 409", rec.Code)
	}
}
