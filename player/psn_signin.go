package main

// psn_signin.go — "Sign in with PlayStation": the PSN Account-ID without
// hunting for it.
//
// A PS5, and a PS4 from firmware 7.0 on, pairs only with the Account-ID of the
// PSN account that plays on it: 8 bytes, written as 12 characters of Base64.
// It is not the sign-in name and not the 8-digit PIN, and no console menu
// shows it, which is why people could not find it. Sony hands it out after a
// sign-in, the way chiaki's scripts/psn-account-id.py gets it: the Remote Play
// app's OAuth login ends on a page that says "redirect" with a one-time code in
// its address; the code buys an access token, and the token's own record
// carries the account's numeric user_id. The Account-ID is that number as a
// little-endian uint64, in Base64.
//
// Sony's login page needs a real browser (scripts, a captcha), so on the
// handheld it happens on the phone: the PS Remote Play panel shows a QR code
// for the Phone Companion's /psn page, which walks the phone through the
// sign-in and posts the "redirect" address back. On a PC the system browser
// opens instead and the address is pasted into the Account-ID field. Either
// way the code is traded on this device and only the Account-ID is kept: the
// code and the tokens are never logged or written anywhere, and every error
// that could quote them goes through psnRedact first.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The Remote Play app's OAuth client. chiaki's psn-account-id script (and its
// Go twin) signs in with exactly these values, and Sony's login only returns
// to the redirect page below for this client.
const (
	psnClientID     = "ba495a24-818c-472b-b12d-ff231c1b5745"
	psnClientSecret = "mvaiZkRsAsI1IBkY"
	psnRedirectURI  = "https://remoteplay.dl.playstation.net/remoteplay/redirect"
	psnAuthBase     = "https://auth.api.sonyentertainmentnetwork.com/2.0/oauth/"
)

// psnLoginURL is Sony's sign-in page for that client, as the script builds it.
// prompt=always asks for the password even in a browser that is signed in
// already, so the Account-ID belongs to the account the person types.
const psnLoginURL = psnAuthBase + "authorize?service_entity=urn:service-entity:psn&response_type=code" +
	"&client_id=" + psnClientID + "&redirect_uri=" + psnRedirectURI +
	"&scope=psn:clientapp&request_locale=en_US&ui=pr&service_logo=ps&layout_type=popup" +
	"&smcid=remoteplay&prompt=always&PlatformPrivacyWs1=minimal&"

// psnTokenURLDefault is Sony's token endpoint: a POST trades a code for an
// access token, and a GET of <endpoint>/<token> describes that token.
const psnTokenURLDefault = psnAuthBase + "token"

const (
	// psnExchangeTimeout bounds the whole exchange, both requests together.
	psnExchangeTimeout = 20 * time.Second
	// psnBodyLimit bounds what is read of either answer: both are a few
	// hundred bytes of JSON.
	psnBodyLimit = 64 << 10
	// psnInputLimit bounds what may be pasted. A real "redirect" address is a
	// few hundred characters.
	psnInputLimit = 4 << 10
)

// companionEventPSN carries an Account-ID from a sign-in to the main loop,
// which saves it and moves the PS Remote Play panel on (psPanelAccountArrived).
const companionEventPSN = "psn"

// psnTokenURL is the token endpoint the exchange uses. Tests point it at an
// httptest TLS server; nothing else changes it.
var psnTokenURL = psnTokenURLDefault

// psnClientOverride replaces the HTTP client in tests (the TLS test server's
// own client, which trusts its certificate).
var psnClientOverride *http.Client

// errPSNUnreadable and errPSNTooLarge are answers that arrived but cannot be
// used. Their text is shown as it is.
var errPSNUnreadable = errors.New("Sony sent an answer JukaHub cannot read — try again later")

var errPSNTooLarge = errors.New("Sony sent an unexpectedly large answer — try again later")

// psnSignIn is the one exchange that may run at a time — from the phone page
// or from an address pasted on this device — and the last thing that went
// wrong. The Account-ID card shows both; whoever runs the exchange writes them.
var psnSignIn struct {
	busy    atomic.Bool
	mu      sync.Mutex
	lastErr string
}

// psnBeginSignIn claims the exchange slot; false means one is running.
func psnBeginSignIn() bool {
	return psnSignIn.busy.CompareAndSwap(false, true)
}

// psnEndSignIn releases the slot and records how the exchange ended.
func psnEndSignIn(err error) {
	psnSignIn.mu.Lock()
	psnSignIn.lastErr = ""
	if err != nil {
		psnSignIn.lastErr = err.Error()
	}
	psnSignIn.mu.Unlock()
	psnSignIn.busy.Store(false)
}

// psnForgetLastError drops the recorded failure once an Account-ID has been
// saved some other way, so the card stops showing an error that no longer
// matters.
func psnForgetLastError() {
	psnSignIn.mu.Lock()
	psnSignIn.lastErr = ""
	psnSignIn.mu.Unlock()
}

// psnSignInState reports whether an exchange is running and the last failure.
func psnSignInState() (bool, string) {
	psnSignIn.mu.Lock()
	defer psnSignIn.mu.Unlock()
	return psnSignIn.busy.Load(), psnSignIn.lastErr
}

// ── The Account-ID ───────────────────────────────────────────────────────

// psnAccountIDFromUserID writes a PSN user_id the way the console checks it:
// the number as 8 little-endian bytes, in standard Base64 — always 12
// characters, the last one "=".
func psnAccountIDFromUserID(userID uint64) string {
	var raw [8]byte
	binary.LittleEndian.PutUint64(raw[:], userID)
	return base64.StdEncoding.EncodeToString(raw[:])
}

// psnInput is what the Account-ID field was given: an Account-ID ready to use,
// or a sign-in code that still has to be traded for one.
type psnInput struct {
	AccountID string
	Code      string
}

// psnParseAccountInput reads whatever a person pastes or types where the
// Account-ID goes, without touching the network:
//
//   - the Account-ID itself, 12 characters of Base64 (also without its "=",
//     or in the URL-safe alphabet some tools print);
//   - the decimal user_id other tools show ("1234567890123456789");
//   - the same number in hex, "0x…";
//   - the address of the page that says "redirect" after the sign-in, the
//     code=… part of it, or the bare code.
func psnParseAccountInput(s string) (psnInput, error) {
	text := strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "\"'<>"))
	if text == "" {
		return psnInput{}, errors.New("Paste the Account-ID, or the address of the page that says \"redirect\"")
	}
	if len(text) > psnInputLimit {
		return psnInput{}, errors.New("That is too long for an Account-ID or a sign-in address")
	}
	if code, ok, err := psnCodeFromRedirect(text); ok {
		if err != nil {
			return psnInput{}, err
		}
		return psnInput{Code: code}, nil
	}
	if id, ok := psnAccountIDFromText(text); ok {
		return psnInput{AccountID: id}, nil
	}
	if len(text) == 8 && psnAllDigits(text) {
		// The PIN and the Account-ID are the two things pairing asks for, and
		// the easiest pair to mix up.
		return psnInput{}, errors.New("That looks like the 8-digit PIN, which comes next — the Account-ID is 12 characters (sign in to get it)")
	}
	if psnLooksLikeCode(text) {
		return psnInput{Code: text}, nil
	}
	return psnInput{}, errors.New("That is not an Account-ID (12 characters, like AbCdEfGhIjk=) or the address of the \"redirect\" page")
}

// psnAccountIDFromText reads the ways an Account-ID is written down. A run of
// digits is always a decimal user_id, and only a long one: real ids are large
// 64-bit numbers, 8 digits is the console's PIN, and an all-digit Base64 id
// would be a one-in-a-billion accident. An id of zero is the "no account"
// placeholder, never an account.
func psnAccountIDFromText(text string) (string, bool) {
	if psnAllDigits(text) {
		if len(text) < 12 || len(text) > 20 {
			return "", false
		}
		id, err := strconv.ParseUint(text, 10, 64)
		if err != nil || id == 0 {
			return "", false
		}
		return psnAccountIDFromUserID(id), true
	}
	if len(text) > 2 && (text[:2] == "0x" || text[:2] == "0X") {
		id, err := strconv.ParseUint(text[2:], 16, 64)
		if err != nil || id == 0 {
			return "", false
		}
		return psnAccountIDFromUserID(id), true
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		raw, err := enc.DecodeString(text)
		if err == nil && len(raw) == 8 && binary.LittleEndian.Uint64(raw) != 0 {
			return base64.StdEncoding.EncodeToString(raw), true
		}
	}
	return "", false
}

func psnAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// psnCodeFromRedirect finds the sign-in code in what was copied from the
// "redirect" page: its whole address, the address without "https://", or the
// code=… part. ok is false when the text is none of those; err is set when it
// is one but carries no usable code — the sign-in was refused, or the address
// came from the wrong page (the sign-in page itself has no code yet).
func psnCodeFromRedirect(text string) (string, bool, error) {
	lower := strings.ToLower(text)
	damaged := errors.New("That address is damaged — copy it again from the page that says \"redirect\"")
	var query url.Values
	switch {
	case strings.Contains(lower, "://"), strings.HasPrefix(lower, "remoteplay."),
		strings.Contains(lower, "playstation.net/"), strings.Contains(lower, "sonyentertainmentnetwork.com/"):
		raw := text
		if !strings.Contains(lower, "://") {
			raw = "https://" + text
		}
		u, err := url.Parse(raw)
		if err != nil {
			return "", true, damaged
		}
		query = u.Query()
		// Some sign-ins hand the code back after "#" rather than "?".
		if fragment, ferr := url.ParseQuery(u.Fragment); ferr == nil {
			for key, values := range fragment {
				if query.Get(key) == "" {
					query[key] = values
				}
			}
		}
	case strings.HasPrefix(lower, "?"), strings.HasPrefix(lower, "code="), strings.Contains(lower, "&code="),
		strings.HasPrefix(lower, "error="):
		parsed, err := url.ParseQuery(strings.TrimPrefix(text, "?"))
		if err != nil {
			return "", true, damaged
		}
		query = parsed
	default:
		return "", false, nil
	}
	if code := strings.TrimSpace(query.Get("code")); code != "" {
		if !psnCodeCharsOK(code) {
			return "", true, errors.New("The sign-in code in that address looks damaged — copy the whole address again")
		}
		return code, true, nil
	}
	if reason := psnCleanWord(query.Get("error")); reason != "" {
		return "", true, errors.New("Sony did not sign you in (" + reason + ") — sign in again")
	}
	return "", true, errors.New("That address has no sign-in code — sign in first, then copy the address of the page that says \"redirect\"")
}

// psnCodeCharsOK accepts what a sign-in code can be: up to 512 printable
// characters, none of which could end or split a form field.
func psnCodeCharsOK(code string) bool {
	if code == "" || len(code) > 512 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte("\"'<>\\&#?", c) >= 0 {
			return false
		}
	}
	return true
}

// psnCleanWord keeps an OAuth error word ("access_denied") short and plain
// before it is shown.
func psnCleanWord(word string) string {
	word = strings.TrimSpace(word)
	if len(word) > 40 {
		word = word[:40]
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return -1
	}, word)
}

// psnLooksLikeCode accepts a bare sign-in code: a handful of letters and
// digits, sometimes behind "v3.", with at least one letter. Anything longer or
// stranger is far more likely a mistyped Account-ID (11 or 12 characters), and
// that deserves its own error rather than a sign-in Sony refuses.
func psnLooksLikeCode(text string) bool {
	body := strings.TrimPrefix(text, "v3.")
	if len(body) < 4 || len(body) > 10 {
		return false
	}
	letter := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			letter = true
		case c >= '0' && c <= '9':
			// Digits are fine; a code of digits alone is not taken.
		default:
			return false
		}
	}
	return letter
}

// ── The exchange ────────────────────────────────────────────────────────

// psnNoRedirect stops the client at a redirect. The account request carries
// the access token in its path, and following a redirect would hand it to
// wherever the answer pointed.
func psnNoRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// psnHTTPClient is the client both requests use: keep-alives off (two requests,
// then nothing), the fallback certificate bundle on a firmware without one
// (bugrepRootCAs), and no redirects.
func psnHTTPClient() *http.Client {
	if psnClientOverride != nil {
		return psnClientOverride
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}
	if pool := bugrepRootCAs(); pool != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Timeout:       psnExchangeTimeout,
		Transport:     tr,
		CheckRedirect: psnNoRedirect,
	}
}

// psnExchangeCode trades a sign-in code for the Account-ID, the way chiaki's
// psn-account-id script does: POST the code to the token endpoint for an
// access token, then GET the token's own record, whose user_id is the
// account's number. Nothing of either answer is kept but that number, and the
// code and the token never reach an error message (psnRedact).
func psnExchangeCode(ctx context.Context, code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", errors.New("There is no sign-in code to check")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(psnTokenURL), "/")
	if !strings.HasPrefix(strings.ToLower(endpoint), "https://") {
		return "", errors.New("JukaHub only signs in to Sony over HTTPS")
	}
	ctx, cancel := context.WithTimeout(ctx, psnExchangeTimeout)
	defer cancel()
	client := psnHTTPClient()

	// The body is the script's, byte for byte, with the code escaped.
	form := "grant_type=authorization_code&code=" + url.QueryEscape(code) + "&redirect_uri=" + psnRedirectURI + "&"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form))
	if err != nil {
		return "", errors.New("Could not prepare the sign-in request")
	}
	req.SetBasicAuth(psnClientID, psnClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var token struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	status, err := psnDoJSON(client, req, &token)
	if err != nil {
		return "", psnTransportError(err, code)
	}
	if status != http.StatusOK {
		return "", psnRefusal(status, token.Error)
	}
	access := strings.TrimSpace(token.AccessToken)
	if access == "" {
		return "", errors.New("Sony's answer had no access token — sign in again")
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/"+url.PathEscape(access), nil)
	if err != nil {
		return "", errors.New("Could not prepare the account request")
	}
	req.SetBasicAuth(psnClientID, psnClientSecret)
	req.Header.Set("Accept", "application/json")
	var account struct {
		UserID json.RawMessage `json:"user_id"`
	}
	status, err = psnDoJSON(client, req, &account)
	if err != nil {
		return "", psnTransportError(err, code, access)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("Sony did not describe your account (HTTP %d) — sign in again", status)
	}
	userID, ok := psnParseUserID(account.UserID)
	if !ok {
		return "", errors.New("Sony's answer had no account number — sign in again")
	}
	return psnAccountIDFromUserID(userID), nil
}

// psnDoJSON sends req and decodes its JSON answer into v, reading at most
// psnBodyLimit bytes. A 2xx answer must decode; a refusal is decoded on a
// best-effort basis, for its "error" word. It returns the HTTP status.
func psnDoJSON(client *http.Client, req *http.Request, v any) (int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, psnBodyLimit+1))
	if err != nil {
		return 0, err
	}
	if len(data) > psnBodyLimit {
		return 0, errPSNTooLarge
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = json.Unmarshal(data, v)
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return 0, errPSNUnreadable
	}
	return resp.StatusCode, nil
}

// psnRefusal words a refused token request. invalid_grant is the one that
// matters: the code is single-use and lives a few minutes, so a slow paste or
// a second try with the same address ends here.
func psnRefusal(status int, word string) error {
	switch {
	case strings.TrimSpace(word) == "invalid_grant":
		return errors.New("That sign-in code has expired — sign in again")
	case status == http.StatusTooManyRequests:
		return errors.New("Sony asked JukaHub to slow down — wait a minute, then sign in again")
	case status >= 500:
		return fmt.Errorf("Sony's sign-in server is having trouble (HTTP %d) — try again later", status)
	}
	return fmt.Errorf("Sony refused the sign-in (HTTP %d)", status)
}

// psnParseUserID reads user_id, which Sony sends as a decimal string. A bare
// JSON number is accepted too, read from its digits: decoding it as a float64
// would round a 19-digit id.
func psnParseUserID(raw json.RawMessage) (uint64, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, false
	}
	if strings.HasPrefix(text, "\"") {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
		text = strings.TrimSpace(s)
	}
	id, err := strconv.ParseUint(text, 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// psnTransportError says in plain words why a request got no answer, and
// makes sure those words cannot carry the code or the token: net/http quotes
// the request URL in its errors, and the account request has the token in
// its path.
func psnTransportError(err error, secrets ...string) error {
	if errors.Is(err, errPSNUnreadable) || errors.Is(err, errPSNTooLarge) {
		return err
	}
	var invalid x509.CertificateInvalidError
	var unknown x509.UnknownAuthorityError
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return errors.New("The sign-in was cancelled")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return errors.New("Sony's sign-in server did not answer in time — check the internet connection, then sign in again")
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		return errors.New("Sony's certificate does not fit this device's date — set the date and time, then sign in again")
	case errors.As(err, &invalid), errors.As(err, &unknown):
		return errors.New("This device cannot check Sony's certificate — type the Account-ID instead, or sign in on a PC")
	case errors.As(err, &dnsErr):
		return errors.New("Could not find Sony's sign-in server — is this device online?")
	}
	inner := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		inner = urlErr.Err
	}
	return errors.New("Could not reach Sony's sign-in server (" + psnRedact(firstLine(inner.Error()), secrets...) + ")")
}

// psnRedact replaces every secret in text, raw or URL-escaped, with
// "[redacted]". Secrets shorter than four characters are left alone: they
// identify nothing and replacing them would shred ordinary words.
func psnRedact(text string, secrets ...string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if len(secret) < 4 {
			continue
		}
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			text = strings.ReplaceAll(text, form, "[redacted]")
		}
	}
	return text
}

// ── The phone page ──────────────────────────────────────────────────────

// psnPageURLFrom turns the Phone Companion's pairing link into the link of
// its PlayStation sign-in page: same address, same PIN, path /psn. "" when
// there is no usable link.
func psnPageURLFrom(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.Path = "/psn"
	u.RawPath = ""
	u.Fragment = ""
	return u.String()
}

// psnPageCaption is the line under the enlarged code, for a phone that cannot
// scan: the address to type, PIN included.
func psnPageCaption(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Host == "" {
		return ""
	}
	caption := "Or open " + u.Host + u.Path
	if u.RawQuery != "" {
		caption += "?" + u.RawQuery
	}
	return caption
}

// handlePSNPage serves the phone's PlayStation sign-in page. It is PIN-guarded
// like the main page, with the same locked card for a stale QR code.
func (s *companionServer) handlePSNPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// The page links out to Sony, and its own address carries the PIN.
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.throttled() {
		http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}
	if !s.authorized(r) {
		s.noteFailure()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, companionLockedPage)
		return
	}
	s.noteSuccess()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", psnPageCSP)
	_, _ = io.WriteString(w, psnSignInPage)
}

// handlePSN takes what the phone page sends — the "redirect" address, or an
// Account-ID typed by hand — and answers with the Account-ID or a sentence
// saying what went wrong. The exchange with Sony runs on this request's
// goroutine, but not on its context: a phone that locks its screen or leaves
// the tab must not cancel a sign-in that is a second from done. The
// Account-ID reaches the panel through the queue, like every phone action.
func (s *companionServer) handlePSN(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2*psnInputLimit)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	in, err := psnParseAccountInput(body.Input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	account := in.AccountID
	if in.Code != "" {
		if !psnBeginSignIn() {
			http.Error(w, "A sign-in is already being checked — wait a moment", http.StatusConflict)
			return
		}
		account, err = psnExchangeCode(context.Background(), in.Code)
		psnEndSignIn(err)
		if err != nil {
			logWarn("psremote", "PSN sign-in from the phone failed: %v", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		logInfo("psremote", "PSN sign-in from the phone produced an Account-ID")
	}
	s.sink(companionEvent{Kind: companionEventPSN, Text: account})
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "account": account})
}

// psnPageCSP keeps the sign-in page to itself: its own inline style and
// script, requests to this handheld only, no frames and no form posting away.
const psnPageCSP = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// psnSignInPage is the phone's sign-in page, with the sign-in link put in the
// way an HTML attribute wants it ("&" escaped).
var psnSignInPage = strings.Replace(psnSignInPageTemplate, "{{LOGIN}}", html.EscapeString(psnLoginURL), 1)

const psnSignInPageTemplate = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0d12">
<meta name="referrer" content="no-referrer">
<title>JukaHub · Sign in with PlayStation</title>
<style>
:root{--bg:#0b0d12;--card:#151923;--line:#232a38;--ink:#e6e8ef;--dim:#8b93a7;--accent:#6f86ff;--ok:#3ecf8e;--bad:#ff6b6b}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.45 system-ui,-apple-system,sans-serif}
body{padding:16px 16px 40px;max-width:560px;margin:0 auto}
h1{font-size:21px;margin:8px 0 6px}
p{margin:0 0 10px}
.lead{color:var(--dim);margin-bottom:16px}
.card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px;margin-bottom:14px}
h2{font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin:0 0 10px}
textarea,input{width:100%;background:#0f131b;border:1px solid var(--line);border-radius:10px;color:var(--ink);padding:12px;font:15px/1.4 ui-monospace,monospace}
textarea{min-height:86px;resize:vertical;word-break:break-all}
.btn{display:block;width:100%;text-align:center;text-decoration:none;font:600 16px system-ui,sans-serif;color:#fff;background:var(--accent);border:0;border-radius:10px;padding:13px 16px;min-height:46px;margin-top:10px}
.btn.ghost{background:#1d2330;color:var(--ink);border:1px solid var(--line)}
.btn:disabled{opacity:.6}
small{color:var(--dim);display:block;margin-top:8px;font-size:13px}
b.word{background:#1d2330;border-radius:6px;padding:1px 6px}
code{font:15px ui-monospace,monospace;background:#0f131b;border-radius:6px;padding:2px 6px;word-break:break-all}
#done{border-color:var(--ok)}
#done h2{color:var(--ok)}
.msg{min-height:1.4em;margin-top:10px;font-size:14px}
.msg.bad{color:var(--bad)}
</style>
<h1>Sign in with PlayStation</h1>
<p class=lead>Pairing a PS5 (or a PS4) needs your PSN Account-ID once. Signing in to Sony here fetches it. JukaHub keeps only that number; your password stays with Sony.</p>

<div class=card>
  <h2>Step 1 · Sign in</h2>
  <a class=btn id=login href="{{LOGIN}}" target=_blank rel="noopener noreferrer">Sign in to PlayStation</a>
  <small>Sony's page opens in a new tab. Use the account you play on the console.</small>
</div>

<div class=card>
  <h2>Step 2 · Send the "redirect" page</h2>
  <p>When you are signed in, that tab shows a page that says <b class=word>redirect</b>. Copy its address (tap the address bar, then Copy), come back to this tab and paste it here.</p>
  <textarea id=addr placeholder="https://remoteplay.dl.playstation.net/remoteplay/redirect?code=…" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false></textarea>
  <button class=btn id=sendAddr>Send to handheld</button>
  <div class=msg id=msgAddr></div>
</div>

<div class=card id=done hidden>
  <h2>Done</h2>
  <p>Your Account-ID <code id=acct></code> is saved on the handheld. Go back to it: it now asks for the 8 numbers the console shows under Link Device (on a PS4: Add Device).</p>
  <a class="btn ghost" id=typePin href="/">Type the PIN from this phone</a>
</div>

<div class=card>
  <h2>Already have your Account-ID?</h2>
  <input id=id placeholder="12 characters, like AbCdEfGhIjk=" autocomplete=off autocapitalize=off autocorrect=off spellcheck=false>
  <button class="btn ghost" id=sendId>Save on handheld</button>
  <div class=msg id=msgId></div>
  <small>Desktop chiaki-ng shows it after PSN Login. The long account number some tools print works too.</small>
</div>

<script>
const pin = new URL(location.href).searchParams.get('p') || '';
document.getElementById('typePin').href = '/?p=' + encodeURIComponent(pin);
function say(el, text, bad) { el.textContent = text; el.className = bad ? 'msg bad' : 'msg'; }
async function send(field, btn, msg) {
  const input = field.value.trim();
  if (!input) { say(msg, 'Paste it first', true); return; }
  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = 'Checking…';
  say(msg, '');
  try {
    const res = await fetch('/api/psn', {method: 'POST', headers: {'X-Companion-Pin': pin, 'Content-Type': 'application/json'}, body: JSON.stringify({input: input})});
    const text = (await res.text()).trim();
    if (!res.ok) throw new Error(text || res.statusText);
    const data = JSON.parse(text);
    document.getElementById('acct').textContent = data.account || '';
    const done = document.getElementById('done');
    done.hidden = false;
    done.scrollIntoView({behavior: 'smooth', block: 'center'});
    field.value = '';
    say(msg, 'Saved on the handheld.');
  } catch (e) {
    say(msg, (e && e.message) || String(e), true);
  } finally {
    btn.disabled = false;
    btn.textContent = label;
  }
}
document.getElementById('sendAddr').onclick = () => send(document.getElementById('addr'), document.getElementById('sendAddr'), document.getElementById('msgAddr'));
document.getElementById('sendId').onclick = () => send(document.getElementById('id'), document.getElementById('sendId'), document.getElementById('msgId'));
</script>
</html>`
