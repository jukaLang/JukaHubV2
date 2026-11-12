package main

// steam_signin.go — "Sign in through Steam" for the Steam library.
//
// Steam's sign-in for other sites is OpenID 2.0: the browser goes to
// steamcommunity.com, the user signs in there (password, Steam Guard, the
// Steam app's QR — all on Valve's page), and Steam sends the browser back to
// the site with a signed claim "this is SteamID64 7656119…". JukaHub is that
// site: the Phone Companion's own address.
//
//   GET /steam?p=PIN      (the phone or the PC's browser) makes a one-use
//                         token and redirects to Steam's sign-in page, with
//                         /steam/return?s=<token> on this server as the way
//                         back. The PIN never goes to Steam.
//   GET /steam/return     takes the token (once, for 15 minutes), checks the
//                         claim with Steam itself (check_authentication), and
//                         saves the Steam ID. JukaHub never sees a password.
//
// The companion page's Steam card also sends a profile link instead (a public
// profile needs no sign-in to read), the optional Web API key, and Sign out,
// to /api/steam behind the PIN.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	steamOpenIDEndpoint = "https://steamcommunity.com/openid/login"
	steamOpenIDNS       = "http://specs.openid.net/auth/2.0"
	steamOpenIDSelect   = "http://specs.openid.net/auth/2.0/identifier_select"
	// steamNonceTTL is how long a started sign-in may take.
	steamNonceTTL = 15 * time.Minute
	// steamNonceMax bounds the started sign-ins kept at once.
	steamNonceMax = 16
	// phoneCardSteam is the companion page's Steam card ("#steam").
	phoneCardSteam phoneCardKind = "steam"
)

var steamClaimedRe = regexp.MustCompile(`^https://steamcommunity\.com/openid/id/(7656119[0-9]{10})/?$`)

// steamSignedFields must all be covered by Steam's signature.
var steamSignedFields = []string{"op_endpoint", "claimed_id", "identity", "return_to", "response_nonce", "assoc_handle"}

// steamNonces are the sign-ins started and not yet back: token → the exact
// return_to address that was sent to Steam.
var steamNonces struct {
	mu sync.Mutex
	m  map[string]steamNonce
}

type steamNonce struct {
	returnTo string
	expires  time.Time
}

// steamNewSignIn starts a sign-in for a browser that reached this server at
// base ("http://host:port") and returns Steam's sign-in address.
func steamNewSignIn(base string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	returnTo := base + "/steam/return?s=" + token
	now := time.Now()
	steamNonces.mu.Lock()
	if steamNonces.m == nil {
		steamNonces.m = map[string]steamNonce{}
	}
	for k, n := range steamNonces.m {
		if now.After(n.expires) {
			delete(steamNonces.m, k)
		}
	}
	if len(steamNonces.m) >= steamNonceMax {
		// Drop the oldest: a new press wins over an abandoned one.
		oldest := ""
		for k, n := range steamNonces.m {
			if oldest == "" || n.expires.Before(steamNonces.m[oldest].expires) {
				oldest = k
			}
		}
		delete(steamNonces.m, oldest)
	}
	steamNonces.m[token] = steamNonce{returnTo: returnTo, expires: now.Add(steamNonceTTL)}
	steamNonces.mu.Unlock()

	v := url.Values{}
	v.Set("openid.ns", steamOpenIDNS)
	v.Set("openid.mode", "checkid_setup")
	v.Set("openid.return_to", returnTo)
	v.Set("openid.realm", base)
	v.Set("openid.identity", steamOpenIDSelect)
	v.Set("openid.claimed_id", steamOpenIDSelect)
	return steamOpenIDEndpoint + "?" + v.Encode(), nil
}

// steamTakeSignIn consumes a token and returns the return_to it was made for.
func steamTakeSignIn(token string) (string, bool) {
	if len(token) != 32 {
		return "", false
	}
	steamNonces.mu.Lock()
	defer steamNonces.mu.Unlock()
	n, ok := steamNonces.m[token]
	if !ok {
		return "", false
	}
	delete(steamNonces.m, token)
	if time.Now().After(n.expires) {
		return "", false
	}
	return n.returnTo, true
}

// steamSafeHost accepts a Host header that can be put in a URL as it is: a
// name or address with an optional port, nothing else.
func steamSafeHost(host string) bool {
	if host == "" || len(host) > 255 {
		return false
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == ':' || r == '[' || r == ']':
		default:
			return false
		}
	}
	return true
}

// steamCheckClaim checks what Steam sent back: the fields must name Steam,
// this sign-in's return address and an individual account, and Steam itself
// must confirm the signature (check_authentication). A variable so tests
// never reach Steam.
var steamCheckClaim = func(ctx context.Context, q url.Values, wantReturnTo string) (string, error) {
	switch q.Get("openid.mode") {
	case "id_res":
	case "cancel":
		return "", errors.New("the sign-in was cancelled")
	default:
		return "", errors.New("Steam did not finish the sign-in")
	}
	if q.Get("openid.ns") != steamOpenIDNS {
		return "", errors.New("the answer was not an OpenID 2.0 sign-in")
	}
	signed := map[string]bool{}
	for _, f := range strings.Split(q.Get("openid.signed"), ",") {
		signed[strings.TrimSpace(f)] = true
	}
	for _, f := range steamSignedFields {
		if !signed[f] {
			return "", errors.New("Steam's answer was not signed as expected")
		}
	}
	if q.Get("openid.op_endpoint") != steamOpenIDEndpoint {
		return "", errors.New("the answer did not come from Steam")
	}
	if q.Get("openid.return_to") != wantReturnTo {
		return "", errors.New("the answer was for another sign-in")
	}
	claimed := q.Get("openid.claimed_id")
	m := steamClaimedRe.FindStringSubmatch(claimed)
	if m == nil || q.Get("openid.identity") != claimed {
		return "", errors.New("Steam did not name an account")
	}
	form := url.Values{}
	for k, vs := range q {
		if strings.HasPrefix(k, "openid.") && len(vs) > 0 {
			form.Set(k, vs[0])
		}
	}
	form.Set("openid.mode", "check_authentication")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, steamOpenIDEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := steamHTTPClient().Do(req)
	if err != nil {
		return "", errors.New("JukaHub could not reach Steam to check the sign-in")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	valid := false
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "is_valid:true" {
			valid = true
		}
	}
	if resp.StatusCode != http.StatusOK || !valid {
		return "", errors.New("Steam did not confirm the sign-in")
	}
	return m[1], nil
}

// steamSignedIn runs on the main loop after any sign-in: the code that asked
// for it closes, the panel reloads, and a toast says who is signed in.
func steamSignedIn(message string) {
	phoneCardArrived(phoneCardSteam)
	steamPanelAccountChanged()
	if message != "" {
		ShowToast(message, ToastKindSuccess)
	}
}

// steamSignInPage is the small page the browser lands on.
func steamSignInPage(w http.ResponseWriter, status int, title, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1">`+
		`<title>JukaHub · Steam</title><style>body{font:16px/1.45 system-ui,sans-serif;background:#0f141b;color:#e8eef6;margin:0;padding:32px 20px}`+
		`.c{max-width:440px;margin:0 auto;background:#18212c;border-radius:16px;padding:22px}h1{font-size:20px;margin:0 0 8px}p{margin:0;color:#b6c2d1}</style></head>`+
		`<body><div class=c><h1>`+html.EscapeString(title)+`</h1><p>`+html.EscapeString(text)+`</p></div></body></html>`)
}

// handleSteamStart is GET /steam: the PIN is checked like the other pages,
// then the browser goes to Steam's sign-in page.
func (s *companionServer) handleSteamStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	addr := castHost(r.RemoteAddr)
	now := time.Now()
	if s.throttled(addr, now) {
		http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}
	if !s.authorized(r) {
		s.noteFailure(addr, now)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, companionLockedPage)
		return
	}
	s.noteSuccess(addr)
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !steamSafeHost(r.Host) {
		http.Error(w, "bad host", http.StatusBadRequest)
		return
	}
	target, err := steamNewSignIn("http://" + r.Host)
	if err != nil {
		http.Error(w, "could not start the sign-in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// handleSteamReturn is GET /steam/return: Steam sends the browser here. The
// one-use token stands in for the PIN (Steam must never see the PIN); a wrong
// one counts as a failed attempt like a wrong PIN.
func (s *companionServer) handleSteamReturn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	addr := castHost(r.RemoteAddr)
	now := time.Now()
	if s.throttled(addr, now) {
		http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	returnTo, ok := steamTakeSignIn(q.Get("s"))
	if !ok {
		s.noteFailure(addr, now)
		steamSignInPage(w, http.StatusBadRequest, "This sign-in has expired",
			"Start again from the Steam card on the companion page, or from Apps → Steam on the handheld.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	id, err := steamCheckClaim(ctx, q, returnTo)
	cancel()
	if err != nil {
		logWarn("steam", "sign-in from %s failed: %v", addr, err)
		steamSignInPage(w, http.StatusBadRequest, "Not signed in", capitalizeFirst(err.Error())+". Try again from the Steam card.")
		return
	}
	if err := steamSignInAs(id, "openid"); err != nil {
		logWarn("steam", "saving the sign-in failed: %v", err)
		steamSignInPage(w, http.StatusInternalServerError, "Not signed in", "The handheld could not save the sign-in: "+err.Error())
		return
	}
	logInfo("steam", "signed in through Steam as %s", id)
	tbxPost(func() { steamSignedIn("Signed in to Steam") })
	steamSignInPage(w, http.StatusOK, "Signed in to Steam",
		"JukaHub is checking your library. If Steam requires a login to show its games, use the Steam app sign-in or add a Web API key on the companion page. JukaHub only keeps your Steam ID, never your password.")
}

var (
	steamProfileIDRe   = regexp.MustCompile(`steamcommunity\.com/profiles/(7656119[0-9]{10})`)
	steamProfileNameRe = regexp.MustCompile(`steamcommunity\.com/id/([A-Za-z0-9_-]{2,32})`)
	steamVanityRe      = regexp.MustCompile(`^[A-Za-z0-9_-]{2,32}$`)
	steamXMLIDRe       = regexp.MustCompile(`<steamID64>\s*(7656119[0-9]{10})\s*</steamID64>`)
	steamAPIKeyRe      = regexp.MustCompile(`^[0-9A-Fa-f]{32}$`)
)

// steamResolveProfile turns what a phone pasted — a SteamID64, a profile
// link or a custom profile name — into a SteamID64.
func steamResolveProfile(ctx context.Context, raw string) (string, error) {
	in := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "\"'`"))
	if steamValidID(in) {
		return in, nil
	}
	if m := steamProfileIDRe.FindStringSubmatch(in); m != nil {
		return m[1], nil
	}
	vanity := ""
	if m := steamProfileNameRe.FindStringSubmatch(in); m != nil {
		vanity = m[1]
	} else if steamVanityRe.MatchString(in) {
		vanity = in
	}
	if vanity == "" {
		return "", errors.New("paste your profile link, like steamcommunity.com/id/yourname")
	}
	data, err := steamGet(ctx, "https://steamcommunity.com/id/"+vanity+"/?xml=1", 1<<20)
	if err != nil {
		return "", errors.New("Steam could not be asked about that profile: " + steamErrorText(err))
	}
	m := steamXMLIDRe.FindSubmatch(data)
	if m == nil {
		return "", errors.New("Steam has no profile called " + vanity)
	}
	return string(m[1]), nil
}

// handleSteamAPI is the companion card: GET says who is signed in (never the
// key), POST signs in with a profile link, saves a Web API key, or signs out.
func (s *companionServer) handleSteamAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		acct, key := steamCurrentAccount()
		tail := ""
		if len(key) >= 12 {
			tail = key[len(key)-4:]
		}
		writeCompanionJSON(w, map[string]interface{}{
			"signedIn": acct.SteamID != "",
			"persona":  acct.Persona,
			"steamId":  acct.SteamID,
			"via":      acct.Via,
			"key":      key != "",
			"keyTail":  tail,
		})
	case http.MethodPost:
		var body struct {
			Profile string `json:"profile"`
			Key     string `json:"key"`
			SignOut bool   `json:"signOut"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if body.SignOut {
			if err := steamSignOut(); err != nil {
				http.Error(w, "the handheld could not save that: "+err.Error(), http.StatusInternalServerError)
				return
			}
			logInfo("steam", "signed out from the phone")
			tbxPost(func() {
				steamPanelAccountChanged()
				ShowToast("Signed out of Steam", ToastKindInfo)
			})
			writeCompanionJSON(w, map[string]interface{}{"ok": true, "message": "Signed out. The handheld forgot the account, the key and the library."})
			return
		}
		var said []string
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// The key is checked before anything is saved, so a bad key sent with
		// a profile link changes nothing.
		k := strings.TrimSpace(strings.Trim(strings.TrimSpace(body.Key), "\"'`"))
		keyChecked := false
		if k != "" {
			if !steamAPIKeyRe.MatchString(k) {
				http.Error(w, "A Steam Web API key is 32 letters and digits - copy it again from steamcommunity.com/dev/apikey", http.StatusUnprocessableEntity)
				return
			}
			ok, checked := steamCheckKey(ctx, k)
			if checked && !ok {
				http.Error(w, "Steam says this key is not valid - copy it again from steamcommunity.com/dev/apikey", http.StatusUnprocessableEntity)
				return
			}
			keyChecked = checked
		}
		if p := strings.TrimSpace(body.Profile); p != "" {
			id, err := steamResolveProfile(ctx, p)
			if err != nil {
				http.Error(w, capitalizeFirst(err.Error()), http.StatusUnprocessableEntity)
				return
			}
			if err := steamSignInAs(id, "profile"); err != nil {
				http.Error(w, "the handheld could not save that: "+err.Error(), http.StatusInternalServerError)
				return
			}
			logInfo("steam", "profile %s set from the phone", id)
			said = append(said, "Profile saved.")
		}
		if k != "" {
			if err := steamSaveKey(k); err != nil {
				http.Error(w, "the handheld could not save the key: "+err.Error(), http.StatusInternalServerError)
				return
			}
			logInfo("steam", "Web API key received from the phone (checked with Steam: %v)", keyChecked)
			if keyChecked {
				said = append(said, "Steam accepted the key.")
			} else {
				said = append(said, "Key saved; Steam could not be reached to check it.")
			}
		}
		if len(said) == 0 {
			http.Error(w, "Paste a profile link or a key first", http.StatusUnprocessableEntity)
			return
		}
		tbxPost(func() { steamSignedIn("Steam account updated from your phone") })
		writeCompanionJSON(w, map[string]interface{}{"ok": true, "message": strings.Join(said, " ") + " The handheld is loading your library."})
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}
