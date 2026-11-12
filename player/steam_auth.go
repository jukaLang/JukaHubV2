package main

// steam_auth.go — "Sign in with the Steam app": the QR sign-in Steam's own
// Big Picture and Steam Deck use.
//
// The handheld asks Steam's login server for a sign-in code
// (IAuthenticationService/BeginAuthSessionViaQR) and shows it as a QR code.
// The user scans it with the Steam app on the phone (Steam Guard → the QR
// button) and approves; JukaHub polls PollAuthSessionStatus until Steam hands
// over a refresh token and an access token for the account. No password is
// ever typed on the handheld or seen by JukaHub, and Steam Guard is the
// approval itself.
//
// The tokens are stored encrypted in account.json (EncryptAPIToken, like the
// Web API key). The access token lasts about a day; GenerateAccessTokenForApp
// trades the refresh token for a new one. With it JukaHub reads a private
// profile's whole library (GetOwnedGames), and sees, installs and starts the
// games on the user's own Steam PCs (steam_remote.go).
//
// The requests use Steam's Web API transport: a protobuf message, base64 in
// the input_protobuf_encoded form field, with format=json for the answer. The
// protobuf side is a handful of fields, so it is encoded here by hand
// (steamPB*); the answer is read as JSON, or as protobuf when Steam sends
// that instead.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
)

const (
	steamAuthAPI = "https://api.steampowered.com/IAuthenticationService/"
	// steamPlatformMobileApp is EAuthTokenPlatformType.MobileApp: the tokens
	// Steam issues for it renew through GenerateAccessTokenForApp and work
	// with the Web API and the community site alike.
	steamPlatformMobileApp = 3
	// steamQRLifetime bounds one sign-in attempt; the code itself rotates
	// inside it (new_challenge_url).
	steamQRLifetime = 10 * time.Minute
	// steamTokenSlack renews an access token this long before it expires.
	steamTokenSlack = 10 * time.Minute
	// steamAuthAnswerLimit bounds one answer of the login server.
	steamAuthAnswerLimit = 64 << 10
)

// steamEResult is Steam's result code, sent in the X-eresult header.
type steamEResult int

const (
	steamEROK            steamEResult = 1
	steamERFail          steamEResult = 2
	steamERInvalidParam  steamEResult = 8
	steamERFileNotFound  steamEResult = 9
	steamERAccessDenied  steamEResult = 15
	steamERExpired       steamEResult = 27
	steamERRateLimit     steamEResult = 84
	steamERInvalidSignIn steamEResult = 5
)

// steamAuthError is a refusal from the login server.
type steamAuthError struct{ result steamEResult }

func (e *steamAuthError) Error() string {
	switch e.result {
	case steamERExpired, steamERFileNotFound:
		return "the sign-in code expired"
	case steamERRateLimit:
		return "Steam asked to slow down · try again in a few minutes"
	case steamERAccessDenied, steamERInvalidSignIn:
		return "Steam refused the sign-in"
	}
	return "Steam's login server answered " + strconv.Itoa(int(e.result))
}

// steamAuthExpired reports whether err means "start a new code".
func steamAuthExpired(err error) bool {
	var ae *steamAuthError
	return errors.As(err, &ae) && (ae.result == steamERExpired || ae.result == steamERFileNotFound)
}

// steamAuthRejected reports whether err is Steam refusing a token outright,
// as opposed to the network failing: only then is a stored token forgotten.
func steamAuthRejected(err error) bool {
	var ae *steamAuthError
	if errors.As(err, &ae) {
		switch ae.result {
		case steamERAccessDenied, steamERExpired, steamERInvalidSignIn:
			return true
		}
	}
	var he *steamHTTPError
	return errors.As(err, &he) && (he.code == http.StatusUnauthorized || he.code == http.StatusForbidden)
}

// steamAuthVague reports a generic refusal (Fail, InvalidParam): a revoked
// token, or a server hiccup. Only a few in a row forget the token.
func steamAuthVague(err error) bool {
	var ae *steamAuthError
	return errors.As(err, &ae) && (ae.result == steamERFail || ae.result == steamERInvalidParam)
}

// steamVagueRefusals counts steamAuthVague renewals in a row (steamTokenMu).
var steamVagueRefusals int

// ── Protobuf, the five field kinds JukaHub needs ─────────────────────────

func steamPBVarint(b []byte, field int, v uint64) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3)
	return binary.AppendUvarint(b, v)
}

func steamPBBytes(b []byte, field int, data []byte) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3|2)
	b = binary.AppendUvarint(b, uint64(len(data)))
	return append(b, data...)
}

func steamPBString(b []byte, field int, s string) []byte {
	return steamPBBytes(b, field, []byte(s))
}

func steamPBFixed64(b []byte, field int, v uint64) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3|1)
	return binary.LittleEndian.AppendUint64(b, v)
}

// steamPBInt32 encodes a negative int32 the way protobuf does: as the
// ten-byte varint of its 64-bit two's complement.
func steamPBInt32(b []byte, field int, v int32) []byte {
	return steamPBVarint(b, field, uint64(int64(v)))
}

// steamPBField is one decoded field: a varint, fixed value or byte string.
type steamPBField struct {
	num   int
	wire  int
	value uint64
	data  []byte
}

// steamPBDecode reads the top-level fields of a message. It never panics on
// bad input: what cannot be read ends the walk with an error.
func steamPBDecode(b []byte) ([]steamPBField, error) {
	var out []steamPBField
	for len(b) > 0 && len(out) < 256 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return out, errors.New("bad protobuf tag")
		}
		b = b[n:]
		f := steamPBField{num: int(tag >> 3), wire: int(tag & 7)}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return out, errors.New("bad protobuf varint")
			}
			f.value, b = v, b[n:]
		case 1:
			if len(b) < 8 {
				return out, errors.New("short protobuf fixed64")
			}
			f.value, b = binary.LittleEndian.Uint64(b), b[8:]
		case 5:
			if len(b) < 4 {
				return out, errors.New("short protobuf fixed32")
			}
			f.value, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return out, errors.New("bad protobuf length")
			}
			f.data, b = b[n:n+int(l)], b[n+int(l):]
		default:
			return out, errors.New("unsupported protobuf wire type")
		}
		out = append(out, f)
	}
	return out, nil
}

// ── The login server's answers ───────────────────────────────────────────

// steamFlexUint reads a 64-bit id that JSON may carry as a number or a string.
type steamFlexUint uint64

func (u *steamFlexUint) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*u = 0
		return nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil || f < 0 || f > math.MaxUint64 {
			return err
		}
		v = uint64(f)
	}
	*u = steamFlexUint(v)
	return nil
}

// steamQRSession is BeginAuthSessionViaQR's answer, and the parts of a poll
// that rotate it.
type steamQRSession struct {
	ClientID     steamFlexUint `json:"client_id"`
	ChallengeURL string        `json:"challenge_url"`
	RequestID    string        `json:"request_id"` // base64 in JSON
	Interval     float64       `json:"interval"`
	requestRaw   []byte
}

// steamPollAnswer is PollAuthSessionStatus's answer.
type steamPollAnswer struct {
	NewClientID     steamFlexUint `json:"new_client_id"`
	NewChallengeURL string        `json:"new_challenge_url"`
	RefreshToken    string        `json:"refresh_token"`
	AccessToken     string        `json:"access_token"`
	Interaction     bool          `json:"had_remote_interaction"`
	AccountName     string        `json:"account_name"`
}

// steamAuthCall posts one IAuthenticationService method. The answer is
// JSON's "response" object, or the raw protobuf message when Steam sent
// that; jsonOut receives the former, and the latter is returned as pb.
var steamAuthCall = func(ctx context.Context, method string, msg []byte, jsonOut interface{}) (pb []steamPBField, err error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("input_protobuf_encoded", base64.StdEncoding.EncodeToString(msg)); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, steamAuthAPI+method+"/v1/?format=json", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	// The login server tells the Steam app's requests apart by these; the
	// tokens it issues then are the app's renewable kind.
	req.Header.Set("User-Agent", "okhttp/4.9.2")
	req.Header.Set("Cookie", "mobileClient=android; mobileClientVersion=777777 3.0.0")
	resp, err := steamHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, steamAuthAnswerLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &steamHTTPError{code: resp.StatusCode}
	}
	if s := strings.TrimSpace(resp.Header.Get("X-eresult")); s != "" {
		if r, err := strconv.Atoi(s); err == nil && steamEResult(r) != steamEROK {
			return nil, &steamAuthError{result: steamEResult(r)}
		}
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var wrap struct {
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal(trimmed, &wrap); err != nil {
			return nil, fmt.Errorf("reading %s: %w", method, err)
		}
		if len(wrap.Response) > 0 && jsonOut != nil {
			if err := json.Unmarshal(wrap.Response, jsonOut); err != nil {
				return nil, fmt.Errorf("reading %s: %w", method, err)
			}
		}
		return nil, nil
	}
	return steamPBDecode(data)
}

// steamDeviceName is how the sign-in shows up under the account's
// authorized devices.
func steamDeviceName() string {
	name := "JukaHub"
	if IsWindows() {
		return name + " (PC)"
	}
	return name + " (handheld)"
}

// steamBeginQR starts a sign-in and returns the code to show.
func steamBeginQR(ctx context.Context) (steamQRSession, error) {
	var details []byte
	details = steamPBString(details, 1, steamDeviceName())
	details = steamPBVarint(details, 2, steamPlatformMobileApp)
	// EOSType "Android, version unknown" and the gaming device type the
	// Steam app reports.
	details = steamPBInt32(details, 3, -500)
	details = steamPBVarint(details, 4, 528)
	var msg []byte
	msg = steamPBString(msg, 1, steamDeviceName())
	msg = steamPBVarint(msg, 2, steamPlatformMobileApp)
	msg = steamPBBytes(msg, 3, details)
	msg = steamPBString(msg, 4, "Mobile")
	var s steamQRSession
	pb, err := steamAuthCall(ctx, "BeginAuthSessionViaQR", msg, &s)
	if err != nil {
		return s, err
	}
	for _, f := range pb {
		switch {
		case f.num == 1 && f.wire == 0:
			s.ClientID = steamFlexUint(f.value)
		case f.num == 2 && f.wire == 2:
			s.ChallengeURL = string(f.data)
		case f.num == 3 && f.wire == 2:
			s.requestRaw = append([]byte(nil), f.data...)
		case f.num == 4 && f.wire == 5:
			s.Interval = float64(math.Float32frombits(uint32(f.value)))
		}
	}
	if s.requestRaw == nil && s.RequestID != "" {
		raw, err := base64.StdEncoding.DecodeString(s.RequestID)
		if err != nil {
			return s, errors.New("Steam's sign-in code could not be read")
		}
		s.requestRaw = raw
	}
	if s.ClientID == 0 || len(s.requestRaw) == 0 || !strings.HasPrefix(s.ChallengeURL, "https://") {
		return s, errors.New("Steam did not hand out a sign-in code")
	}
	return s, nil
}

// steamPollQR asks whether the code was approved yet.
func steamPollQR(ctx context.Context, s steamQRSession) (steamPollAnswer, error) {
	var msg []byte
	msg = steamPBVarint(msg, 1, uint64(s.ClientID))
	msg = steamPBBytes(msg, 2, s.requestRaw)
	var a steamPollAnswer
	pb, err := steamAuthCall(ctx, "PollAuthSessionStatus", msg, &a)
	if err != nil {
		return a, err
	}
	for _, f := range pb {
		switch {
		case f.num == 1 && f.wire == 0:
			a.NewClientID = steamFlexUint(f.value)
		case f.num == 2 && f.wire == 2:
			a.NewChallengeURL = string(f.data)
		case f.num == 3 && f.wire == 2:
			a.RefreshToken = string(f.data)
		case f.num == 4 && f.wire == 2:
			a.AccessToken = string(f.data)
		case f.num == 5 && f.wire == 0:
			a.Interaction = f.value != 0
		case f.num == 6 && f.wire == 2:
			a.AccountName = string(f.data)
		}
	}
	return a, nil
}

// steamRenewAccessToken trades a refresh token for a new access token (and,
// sometimes, a new refresh token).
func steamRenewAccessToken(ctx context.Context, refresh, steamID string) (access, newRefresh string, err error) {
	id, err := strconv.ParseUint(steamID, 10, 64)
	if err != nil {
		return "", "", errors.New("no Steam ID for the token")
	}
	var msg []byte
	msg = steamPBString(msg, 1, refresh)
	msg = steamPBFixed64(msg, 2, id)
	// renewal_type 1: Steam may hand out a fresh refresh token too, so a
	// device in daily use never reaches the old one's expiry.
	msg = steamPBVarint(msg, 3, 1)
	var a struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	pb, err := steamAuthCall(ctx, "GenerateAccessTokenForApp", msg, &a)
	if err != nil {
		return "", "", err
	}
	for _, f := range pb {
		switch {
		case f.num == 1 && f.wire == 2:
			a.AccessToken = string(f.data)
		case f.num == 2 && f.wire == 2:
			a.RefreshToken = string(f.data)
		}
	}
	if a.AccessToken == "" {
		return "", "", errors.New("Steam did not renew the sign-in")
	}
	return a.AccessToken, a.RefreshToken, nil
}

// steamJWTClaims reads the claims JukaHub needs from a Steam token: the
// account (sub) and the expiry (exp). The signature is Steam's business:
// the token only ever goes back to Steam.
func steamJWTClaims(token string) (sub string, exp int64, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[1]) > 8192 {
		return "", 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", 0, false
	}
	var c struct {
		Sub string          `json:"sub"`
		Exp json.RawMessage `json:"exp"`
	}
	if json.Unmarshal(raw, &c) != nil || !steamValidID(c.Sub) {
		return "", 0, false
	}
	return c.Sub, int64(steamRawNumber(c.Exp)), true
}

// ── Stored tokens ────────────────────────────────────────────────────────

func steamEncryptToken(tok, name string) string {
	if tok == "" {
		return ""
	}
	enc, err := EncryptAPIToken(tok, name)
	if err != nil {
		logWarn("steam", "encrypting the %s failed: %v", name, err)
		return ""
	}
	return enc
}

// steamSignInWithTokens saves the account a QR sign-in produced.
func steamSignInWithTokens(id, refresh, access, accountName string) error {
	if !steamValidID(id) {
		return errors.New("Steam did not name an account")
	}
	_, exp, _ := steamJWTClaims(access)
	steamStateMu.Lock()
	defer steamStateMu.Unlock()
	a := steamLoadAccountLocked()
	if a.SteamID != id {
		a.Persona = ""
		_ = steamRemoveLibraryLocked()
	}
	a.SteamID, a.Via, a.NoLocal, a.TokenLost = id, "qr", false, false
	a.AccountName = steamCleanText(accountName, 64)
	a.RefreshToken = steamEncryptToken(refresh, "steam_refresh_token")
	a.AccessToken = steamEncryptToken(access, "steam_access_token")
	a.AccessExp = exp
	if a.RefreshToken == "" {
		return errors.New("the sign-in could not be stored safely")
	}
	if err := steamSaveAccountLocked(a); err != nil {
		return err
	}
	steamAccountGeneration++
	return nil
}

// steamTokenMu keeps two goroutines from renewing the token at once.
var steamTokenMu sync.Mutex

// errSteamNoToken means the account was not signed in with the Steam app.
var errSteamNoToken = errors.New("sign in with the Steam app first")

// steamAccessToken is a usable access token for the signed-in account,
// renewed when it is about to expire. A token Steam refuses is forgotten
// (the account and its library stay; the screen asks to sign in again).
func steamAccessToken(ctx context.Context) (token, steamID string, err error) {
	steamTokenMu.Lock()
	defer steamTokenMu.Unlock()
	steamStateMu.Lock()
	a := steamLoadAccountLocked()
	steamStateMu.Unlock()
	if a.SteamID == "" || a.RefreshToken == "" {
		return "", "", errSteamNoToken
	}
	if a.AccessExp > time.Now().Add(steamTokenSlack).Unix() {
		if tok := steamDecryptKey(a.AccessToken); tok != "" {
			return tok, a.SteamID, nil
		}
	}
	refresh := steamDecryptKey(a.RefreshToken)
	if refresh == "" {
		return "", "", errSteamNoToken
	}
	if _, exp, ok := steamJWTClaims(refresh); ok && exp > 0 && exp < time.Now().Unix() {
		steamForgetTokens(a.SteamID, "the sign-in is older than Steam allows")
		return "", "", errSteamSignedOut
	}
	access, newRefresh, err := steamRenewAccessToken(ctx, refresh, a.SteamID)
	if err != nil {
		if steamAuthVague(err) {
			steamVagueRefusals++
		}
		if steamAuthRejected(err) || steamVagueRefusals >= 3 {
			steamVagueRefusals = 0
			steamForgetTokens(a.SteamID, err.Error())
			return "", "", errSteamSignedOut
		}
		return "", "", err
	}
	steamVagueRefusals = 0
	_, exp, _ := steamJWTClaims(access)
	if exp == 0 {
		exp = time.Now().Add(time.Hour).Unix()
	}
	steamStateMu.Lock()
	cur := steamLoadAccountLocked()
	if cur.SteamID != a.SteamID || cur.RefreshToken != a.RefreshToken || cur.NoLocal {
		steamStateMu.Unlock()
		return "", "", errSteamNoToken
	}
	cur.AccessToken = steamEncryptToken(access, "steam_access_token")
	cur.AccessExp = exp
	cur.TokenLost = false
	if newRefresh != "" {
		if enc := steamEncryptToken(newRefresh, "steam_refresh_token"); enc != "" {
			cur.RefreshToken = enc
		}
	}
	if err := steamSaveAccountLocked(cur); err != nil {
		logWarn("steam", "saving the renewed sign-in failed: %v", err)
	}
	steamStateMu.Unlock()
	return access, a.SteamID, nil
}

// errSteamSignedOut is a sign-in Steam no longer accepts.
var errSteamSignedOut = errors.New("Steam signed this device out · sign in with the Steam app again")

// steamForgetTokens drops the tokens of id (Steam refused them, or they are
// too old). The account stays, so the public profile is still read.
func steamForgetTokens(id, why string) {
	logWarn("steam", "forgetting the Steam app sign-in: %s", why)
	steamStateMu.Lock()
	defer steamStateMu.Unlock()
	a := steamLoadAccountLocked()
	if a.SteamID != id {
		return
	}
	a.RefreshToken, a.AccessToken, a.AccessExp = "", "", 0
	a.TokenLost = true
	if err := steamSaveAccountLocked(a); err != nil {
		logWarn("steam", "saving account.json failed: %v", err)
	} else {
		steamAccountGeneration++
	}
}

// ── The sign-in on screen ────────────────────────────────────────────────

// steamQRState is the sign-in code the Account page shows. The worker
// goroutine owns the session; the main loop owns this struct (tbxPost).
type steamQRState struct {
	active  bool
	gen     int
	url     string
	bitmap  [][]bool
	core    [][]bool
	status  string
	err     string
	scanned bool
	cancel  context.CancelFunc
}

var steamQR steamQRState

// steamQRBegin starts (or restarts) the sign-in code. Main loop.
func steamQRBegin() {
	steamQRStop()
	steamQR.gen++
	gen := steamQR.gen
	ctx, cancel := context.WithTimeout(context.Background(), steamQRLifetime)
	steamQR.active, steamQR.cancel = true, cancel
	steamQR.status = "Asking Steam for a sign-in code…"
	go steamQRWorker(ctx, gen)
}

// steamQRStop abandons the sign-in in progress, if any. Main loop.
func steamQRStop() {
	if steamQR.cancel != nil {
		steamQR.cancel()
	}
	steamQR = steamQRState{gen: steamQR.gen}
}

// steamQRSetCode shows url as the code. Main loop.
func steamQRSetCode(gen int, link string) {
	if gen != steamQR.gen || !steamQR.active || link == steamQR.url {
		return
	}
	code, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		steamQR.err = "The sign-in code could not be drawn"
		return
	}
	steamQR.url = link
	steamQR.bitmap = code.Bitmap()
	steamQR.core = qrCoreModules(steamQR.bitmap)
	steamQR.err = ""
	if !steamQR.scanned {
		steamQR.status = "Waiting for the Steam app…"
	}
	// An enlarged code shows the new one too.
	if qrZoomActive() && qrZoom.title == steamQRTitle {
		qrZoom.modules = steamQR.bitmap
	}
}

const steamQRTitle = "Steam · scan with the Steam app"

func steamQRPost(gen int, f func()) {
	tbxPost(func() {
		if gen == steamQR.gen && steamQR.active {
			f()
		}
	})
}

// steamQRWorker runs one sign-in and shows how it ended: a new code is
// offered after an expiry, a refusal or a failure to reach Steam.
func steamQRWorker(ctx context.Context, gen int) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("steam", "sign-in worker stopped: %v", r)
		}
	}()
	msg := steamQRRun(ctx, gen)
	if msg == "" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		msg = "The code expired"
	}
	if msg != "" {
		msg += " · press " + hintKeyFor("A", "New code") + " for a new one"
		steamQRPost(gen, func() {
			steamQR.err = msg
			steamQR.status = ""
		})
	}
}

// steamQRRun is one sign-in: begin, show, poll until approved; an expired
// code is replaced until the lifetime runs out. It returns why it stopped,
// or "" when it signed in or was cancelled.
func steamQRRun(ctx context.Context, gen int) string {
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		s, err := steamBeginQR(cctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ""
			}
			logWarn("steam", "starting the QR sign-in failed: %v", err)
			return steamErrorText(err)
		}
		link := s.ChallengeURL
		steamQRPost(gen, func() { steamQRSetCode(gen, link) })
		interval := time.Duration(s.Interval * float64(time.Second))
		if interval < 2*time.Second || interval > 30*time.Second {
			interval = 5 * time.Second
		}
		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				return ""
			case <-time.After(interval):
			}
			pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			a, err := steamPollQR(pctx, s)
			cancel()
			if err != nil {
				var ae *steamAuthError
				switch {
				case ctx.Err() != nil:
					return ""
				case steamAuthExpired(err):
					restart = true
				case errors.As(err, &ae):
					logWarn("steam", "QR sign-in refused: %v", err)
					return capitalizeFirst(err.Error())
				default:
					// A dropped poll is retried; the code stays on screen.
					logDebug("steam", "QR poll: %v", err)
				}
				continue
			}
			if a.NewChallengeURL != "" {
				s.ChallengeURL = a.NewChallengeURL
				if a.NewClientID != 0 {
					s.ClientID = a.NewClientID
				}
				link := a.NewChallengeURL
				steamQRPost(gen, func() { steamQRSetCode(gen, link) })
			}
			if a.Interaction {
				steamQRPost(gen, func() {
					steamQR.scanned = true
					steamQR.status = "Scanned · approve the sign-in in the Steam app"
				})
			}
			if a.RefreshToken == "" {
				continue
			}
			return steamQRFinish(gen, a)
		}
	}
	return ""
}

// steamQRFinish saves an approved sign-in. The panel hears of it even when
// the user left the code page meanwhile: the account changed either way.
func steamQRFinish(gen int, a steamPollAnswer) string {
	id, _, ok := steamJWTClaims(a.RefreshToken)
	if !ok {
		id, _, ok = steamJWTClaims(a.AccessToken)
	}
	if !ok {
		return "Steam's answer did not name an account"
	}
	access := a.AccessToken
	if access == "" {
		rctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		var err error
		access, _, err = steamRenewAccessToken(rctx, a.RefreshToken, id)
		cancel()
		if err != nil {
			logWarn("steam", "first access token: %v", err)
		}
	}
	if err := steamSignInWithTokens(id, a.RefreshToken, access, a.AccountName); err != nil {
		return "The sign-in could not be saved: " + err.Error()
	}
	logInfo("steam", "signed in with the Steam app as %s", id)
	name := steamCleanText(a.AccountName, 64)
	tbxPost(func() {
		if gen == steamQR.gen {
			steamQRStop()
		}
		if qrZoomActive() && qrZoom.title == steamQRTitle {
			qrZoomCloseCard()
		}
		msg := "Signed in to Steam"
		if name != "" {
			msg += " as " + name
		}
		steamSignedIn(msg)
	})
	return ""
}
