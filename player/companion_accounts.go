package main

// companion_accounts.go — the account settings typed on the phone: the
// RetroAchievements username and Web API key, and the RomM server address and
// client API token.
//
// Both are long strings nobody wants to type with a D-pad, so the Phone
// Companion page has a card for each, like the Groq key, Steam and VPN cards.
// The handheld shows a code that opens the page at the card ("#ra", "#romm",
// companion_phonecard.go) from the RetroAchievements settings and from RomM's
// Connection tab; the page itself lists both cards all the time.
//
// What the phone sends is checked with the service before it is saved: a key
// RetroAchievements refuses, an unknown username or a token RomM refuses is
// sent back as an error the card shows. A handheld that cannot reach the
// service right now still saves what came, and says so. Secrets never go back
// to the page: it learns whether a key or token is saved, its last four
// characters, and (not secret) the username and the server address.
//
// The HTTP goroutine saves the settings (both stores take their own locks)
// and posts the on-screen part — the panel, the toast, closing the code — to
// the main loop with tbxPost.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// phoneCardRA and phoneCardRomM are the companion page's cards
	// ("raCard", "rommCard").
	phoneCardRA   phoneCardKind = "ra"
	phoneCardRomM phoneCardKind = "romm"
	// companionAccountCheckWait bounds the call that checks what the phone
	// sent with RetroAchievements or the RomM server.
	companionAccountCheckWait = 10 * time.Second
	// companionRomMTokenMax bounds a token: RomM's are well under it, a pasted
	// paragraph is not.
	companionRomMTokenMax = 512
)

// companionAccountTail is the part of a secret the page may show: its last
// four characters, or nothing for a value too short to hide the rest. Pure.
func companionAccountTail(secret string) string {
	secret = strings.TrimSpace(secret)
	if len(secret) < 12 {
		return ""
	}
	return secret[len(secret)-4:]
}

// ── RetroAchievements ────────────────────────────────────────────────────

// companionCheckRA asks RetroAchievements for the user's profile with the key.
// It answers the account's own spelling of the name, and a verdict: rejected
// for a key RetroAchievements refuses or a name it does not know, unchecked
// when it could not be asked. The Web API checks the key alone ("u" is only
// the user looked up), so this catches typos in either field; it cannot prove
// the key belongs to that user. keySent says whether the phone sent the key,
// so a refusal of one the handheld already had says where it came from. A
// variable, so tests never touch the network.
var companionCheckRA = func(ctx context.Context, username, key string, keySent bool) (string, groqKeyVerdict, string) {
	var profile raProfile
	err := raAPIGet(ctx, raHTTPClient, "API_GetUserProfile.php", raCredentials{Username: username, APIKey: key}, nil, &profile)
	var apiErr *raAPIError
	switch {
	case err == nil && strings.TrimSpace(profile.User) != "":
		return strings.TrimSpace(profile.User), groqKeyAccepted, ""
	case err == nil, errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		return username, groqKeyRejected, "RetroAchievements has no user named " + username + " - check the spelling"
	case errors.Is(err, errRAAuth) && keySent:
		return username, groqKeyRejected, "RetroAchievements refused this Web API key - copy it again from retroachievements.org/settings"
	case errors.Is(err, errRAAuth):
		return username, groqKeyRejected, "RetroAchievements refused the Web API key the handheld has - paste yours from retroachievements.org/settings too"
	}
	return username, groqKeyUnchecked, ""
}

// companionRAStatus is what the page may know about the account.
func companionRAStatus() map[string]interface{} {
	creds := raEffectiveCredentials()
	return map[string]interface{}{
		"username":  creds.Username,
		"userFrom":  creds.UsernameSource,
		"key":       creds.APIKey != "",
		"keyTail":   companionAccountTail(creds.APIKey),
		"keyFrom":   creds.KeySource,
		"connected": creds.complete(),
	}
}

// handleRetroAchievements answers the page's RetroAchievements card: GET
// tells the username and whether a key is saved (never the key), POST checks
// a username and key with RetroAchievements and saves them. Either field may
// be left empty to keep the one the handheld already has.
func (s *companionServer) handleRetroAchievements(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeCompanionJSON(w, companionRAStatus())
	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Key      string `json:"key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		sentUser, sentKey := strings.TrimSpace(body.Username) != "", strings.TrimSpace(body.Key) != ""
		if !sentUser && !sentKey {
			http.Error(w, "Type your username and paste your Web API key first", http.StatusUnprocessableEntity)
			return
		}
		current := raEffectiveCredentials()
		username, key := current.Username, current.APIKey
		if sentUser {
			name, err := raNormalizeUsername(body.Username)
			if err != nil {
				http.Error(w, "That username cannot be used - type it as it is on retroachievements.org", http.StatusUnprocessableEntity)
				return
			}
			username = name
		}
		if sentKey {
			k, err := raNormalizeAPIKey(body.Key)
			if err != nil {
				http.Error(w, "The Web API key must be 32 letters or digits - copy it again from retroachievements.org/settings", http.StatusUnprocessableEntity)
				return
			}
			key = k
		}
		verdict := groqKeyUnchecked
		if username != "" && key != "" {
			ctx, cancel := context.WithTimeout(r.Context(), companionAccountCheckWait)
			canonical, v, reason := companionCheckRA(ctx, username, key, sentKey)
			cancel()
			if v == groqKeyRejected {
				http.Error(w, reason, http.StatusUnprocessableEntity)
				return
			}
			verdict = v
			if v == groqKeyAccepted && sentUser && strings.EqualFold(canonical, username) {
				username = canonical
			}
		}
		// The store is read again after the check, which can take seconds: a
		// field edited on the handheld meanwhile keeps its new value, and only
		// what the phone sent changes.
		next := raLoadSettings()
		if sentUser {
			next.Username = username
		}
		if sentKey {
			next.APIKey = key
		}
		if err := raSaveSettings(next); err != nil {
			logRA("cannot save settings from the phone: %v", err)
			http.Error(w, "The handheld could not save the settings", http.StatusInternalServerError)
			return
		}
		tbxPost(raPhoneArrived)
		logInfo("companion", "RetroAchievements account received from the phone (checked: %v)", verdict == groqKeyAccepted)
		msg := "Saved on the handheld. RetroAchievements accepted the account."
		switch {
		case username == "" || key == "":
			msg = "Saved on the handheld. Add the other field to finish."
		case verdict != groqKeyAccepted:
			msg = "Saved on the handheld. JukaHub could not reach RetroAchievements to check it just now; it syncs as soon as the handheld is online."
		}
		out := companionRAStatus()
		out["ok"], out["verified"], out["message"] = true, verdict == groqKeyAccepted, msg
		writeCompanionJSON(w, out)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// raFromPhone shows the code that opens the companion's RetroAchievements
// card (the panel's "From your phone" row).
func raFromPhone() {
	phoneCardOpen(phoneCardRA, "RetroAchievements · type it on your phone",
		"Scan it, enter your username and Web API key, then tap Save")
}

// raPhoneArrived applies an account the phone saved, on the main loop: the
// same steps as saving it in the panel (raSubmitCredential), then the code
// closes.
func raPhoneArrived() {
	invalidateEmulatorCache()
	raOnCredentialsChanged()
	raEnsureCacheLoaded()
	phoneCardArrived(phoneCardRA)
	creds := raEffectiveCredentials()
	if !creds.complete() {
		ShowToast("RetroAchievements: add the other field to finish", ToastKindInfo)
		return
	}
	// The account view replaces the settings unless the keyboard is open on
	// one of their fields: that edit stays the user's to finish or cancel.
	if raPanel.open && !(virtualKeyboardActive && vkTarget == vkTargetRA) {
		raPanel.settings = false
		raPanel.edit = raEditNone
		raPanel.cursor, raPanel.scroll = 0, 0
	}
	raMaybeRefresh(true)
	ShowToast("RetroAchievements connected as "+creds.Username, ToastKindSuccess)
}

// ── RomM ─────────────────────────────────────────────────────────────────

// companionCheckRomM asks the server for one game with the token: the
// catalogue call the Games tab makes, cut to a single row, and roms.read is a
// scope every token for this screen needs (docs/ROMM_SERVER.md). Rejected is
// a 401/403 (a wrong token, or one without that scope) or a 404 (no RomM at
// that address); unchecked is anything else. A variable, so tests never touch
// the network.
var companionCheckRomM = func(ctx context.Context, settings rommServerSettings) (groqKeyVerdict, string) {
	client, err := newRommServerClient(settings)
	if err != nil {
		return groqKeyRejected, err.Error()
	}
	resp, err := client.request(ctx, http.MethodGet, "roms", url.Values{"limit": {"1"}}, nil, "")
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}
	switch rommServerStatus(err) {
	case 0:
		if err == nil {
			return groqKeyAccepted, ""
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		return groqKeyRejected, "The RomM server refused this token - create a client API token with read and write access (roms.read at least) in RomM"
	case http.StatusNotFound:
		return groqKeyRejected, "No RomM server answered at that address - check it and the port"
	}
	return groqKeyUnchecked, ""
}

// companionRomMStatus is what the page may know about the connection.
func companionRomMStatus() map[string]interface{} {
	cur := rommServerSettingsGet()
	return map[string]interface{}{
		"url":       cur.URL,
		"token":     strings.TrimSpace(cur.Token) != "",
		"tokenTail": companionAccountTail(cur.Token),
	}
}

// handleRomM answers the page's RomM card: GET tells the server address and
// whether a token is saved (never the token), POST checks a new address and
// token with the server and saves them. Either field may be left empty to keep
// the saved one.
func (s *companionServer) handleRomM(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeCompanionJSON(w, companionRomMStatus())
	case http.MethodPost:
		var body struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		cur := rommServerSettingsGet()
		next := cur
		sentURL, sentToken := false, false
		if raw := strings.TrimSpace(body.URL); raw != "" {
			sentURL = true
			if !strings.Contains(raw, "://") {
				// A phone keyboard rarely gets "http://" typed: a bare host
				// is a server on the home network.
				raw = "http://" + raw
			}
			base, err := rommServerBase(raw)
			if err != nil {
				http.Error(w, "Enter the server's full address, like http://192.168.1.20:8080", http.StatusUnprocessableEntity)
				return
			}
			next.URL = base.String()
		}
		if token := strings.TrimSpace(strings.Trim(strings.TrimSpace(body.Token), "\"'`")); token != "" {
			if len(token) > companionRomMTokenMax || strings.ContainsAny(token, " \t\r\n") {
				http.Error(w, "That does not look like a RomM client API token - copy it again", http.StatusUnprocessableEntity)
				return
			}
			next.Token, sentToken = token, true
		}
		if !sentURL && !sentToken {
			http.Error(w, "Type the server address or paste a token first", http.StatusUnprocessableEntity)
			return
		}
		verdict := groqKeyUnchecked
		if next.URL != "" && next.Token != "" {
			ctx, cancel := context.WithTimeout(r.Context(), companionAccountCheckWait)
			v, reason := companionCheckRomM(ctx, next)
			cancel()
			if v == groqKeyRejected {
				http.Error(w, reason, http.StatusUnprocessableEntity)
				return
			}
			verdict = v
		}
		// The store is read again after the check, which can take seconds:
		// what the handheld changed meanwhile (the automatic sync switch, its
		// own fields) stays, and only what the phone sent changes.
		latest := rommServerSettingsGet()
		changed := false
		if sentURL && latest.URL != next.URL {
			latest.URL, changed = next.URL, true
		}
		if sentToken && latest.Token != next.Token {
			latest.Token, changed = next.Token, true
		}
		if changed {
			// A new server or token registers this device again (as the
			// handheld's own fields do, rommCloudApplyEdit).
			latest.DeviceID = ""
		}
		next = latest
		if err := rommServerSettingsSave(next); err != nil {
			http.Error(w, "The handheld could not save it: "+firstLine(err.Error()), http.StatusInternalServerError)
			return
		}
		tbxPost(rommPhoneArrived)
		logInfo("companion", "RomM connection received from the phone (checked: %v)", verdict == groqKeyAccepted)
		msg := "Saved on the handheld. The RomM server accepted the token."
		switch {
		case next.URL == "" || next.Token == "":
			msg = "Saved on the handheld. Add the other field to finish."
		case verdict != groqKeyAccepted:
			msg = "Saved on the handheld. JukaHub could not reach the server to check it just now."
		}
		out := companionRomMStatus()
		out["ok"], out["verified"], out["message"] = true, verdict == groqKeyAccepted, msg
		writeCompanionJSON(w, out)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// rommFromPhone shows the code that opens the companion's RomM card (the
// Connection tab's "From your phone" row).
func rommFromPhone() {
	phoneCardOpen(phoneCardRomM, "RomM · type it on your phone",
		"Scan it, enter the server address and token, then tap Save")
}

// rommPhoneArrived applies a connection the phone saved, on the main loop:
// the code closes, and when the server or token changed the RomM screen
// forgets the old server's lists (as rommCloudApplyEdit does). A download or
// sync still running keeps its screen state: the handheld's own fields wait
// for it (rommCloudActivate's busy check), and dropping its result would lose
// the "done" note and let a second job start beside it.
func rommPhoneArrived() {
	next := rommServerSettingsGet()
	prev := rommCloud.settings
	rommCloud.settings = next
	if !rommCloud.busy && (prev.URL != next.URL || prev.Token != next.Token) {
		rommCloud.gen++ // a response from the previous connection cannot repopulate the screen
		rommCloud.games, rommCloud.firmware = nil, nil
		rommCloud.total = 0
	}
	phoneCardArrived(phoneCardRomM)
	if next.URL == "" || next.Token == "" {
		ShowToast("RomM: add the other field to finish", ToastKindInfo)
		return
	}
	ShowToast("RomM connection saved from your phone", ToastKindSuccess)
}
