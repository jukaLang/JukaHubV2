package main

// gdrive_auth.go — signing in to Google Drive on a handheld with no browser
// and no keyboard (docs/CLOUD_SYNC.md).
//
// Google's "TVs and Limited Input devices" sign-in (the OAuth 2.0 device
// authorization grant): JukaHub asks Google for a short code, shows it with a
// QR code of google.com/device, and polls until the player has approved it on
// a phone or a computer. The answer carries a refresh token, kept in
// gdrive.json beside the other settings (0600, encrypted with the player's key
// the way the Groq key is), from which a short-lived access token is made on
// demand. Sign out revokes the token at Google and deletes the file.
//
// The only scope asked for is drive.file: JukaHub sees the files it made
// itself ("JukaHub Saves" in My Drive) and nothing else in the account.
//
// Which OAuth client signs in: JUKAHUB_GOOGLE_CLIENT_ID/SECRET in the
// environment, else google_client_id/google_client_secret in the config,
// else the values the build stamped in with -ldflags "-X
// main.googleOAuthClientID=…". A build with none says so on the Save sync
// page instead of offering a sign-in that cannot work.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The OAuth client a release build signs in with, stamped by the build
// (build.bat, release.sh, .github/scripts/trimui-build.sh read
// JUKAHUB_GOOGLE_CLIENT_ID/SECRET). Empty in a build made without them.
var (
	googleOAuthClientID     string
	googleOAuthClientSecret string
)

const (
	gdriveScope         = "https://www.googleapis.com/auth/drive.file"
	gdriveDeviceCodeURL = "https://oauth2.googleapis.com/device/code"
	gdriveTokenURL      = "https://oauth2.googleapis.com/token"
	gdriveRevokeURL     = "https://oauth2.googleapis.com/revoke"
	gdriveDeviceGrant   = "urn:ietf:params:oauth:grant-type:device_code"
	gdriveAboutURL      = "https://www.googleapis.com/drive/v3/about?fields=user(displayName,emailAddress,permissionId)"
	// gdriveAnswerLimit bounds an OAuth or about answer.
	gdriveAnswerLimit = 64 << 10
)

var (
	errGdriveNoClient    = errors.New("this build has no Google sign-in set up")
	errGdriveSignedOut   = errors.New("not signed in to Google Drive")
	errGdriveExpired     = errors.New("the Google Drive sign-in expired — sign in again in Tools → Save sync")
	errGdriveDenied      = errors.New("the sign-in was declined on the phone")
	errGdriveCodeExpired = errors.New("the code expired before it was entered — start again")
)

// gdriveClient is the OAuth client a sign-in uses.
type gdriveClient struct {
	ID     string
	Secret string
	Source string
}

// gdriveClientFrom picks the OAuth client: environment, then config, then
// the build's. Main thread (it reads the config).
func gdriveClientFrom(config *Config) gdriveClient {
	if id := strings.TrimSpace(os.Getenv("JUKAHUB_GOOGLE_CLIENT_ID")); id != "" {
		return gdriveClient{ID: id, Secret: strings.TrimSpace(os.Getenv("JUKAHUB_GOOGLE_CLIENT_SECRET")), Source: "environment"}
	}
	if config != nil && config.Variables.Custom != nil {
		id, _ := config.Variables.Custom["google_client_id"].(string)
		if id = strings.TrimSpace(id); id != "" {
			secret, _ := config.Variables.Custom["google_client_secret"].(string)
			plain, err := DecryptString(strings.TrimSpace(secret))
			if err != nil {
				logWarn("gdrive", "google_client_secret in the config cannot be read: %v", err)
				plain = ""
			}
			return gdriveClient{ID: id, Secret: plain, Source: "config"}
		}
	}
	return gdriveClient{ID: strings.TrimSpace(googleOAuthClientID), Secret: strings.TrimSpace(googleOAuthClientSecret), Source: "build"}
}

// gdriveClientConfigured reports whether any OAuth client is set, without
// decrypting anything (the page asks a few times a second). Main thread.
func gdriveClientConfigured(config *Config) bool {
	if strings.TrimSpace(os.Getenv("JUKAHUB_GOOGLE_CLIENT_ID")) != "" || strings.TrimSpace(googleOAuthClientID) != "" {
		return true
	}
	if config == nil || config.Variables.Custom == nil {
		return false
	}
	id, _ := config.Variables.Custom["google_client_id"].(string)
	return strings.TrimSpace(id) != ""
}

// ── Stored sign-in ──────────────────────────────────────────────────────────

// gdriveSettings is gdrive.json. ClientSecret and RefreshToken are stored
// encrypted ("ENC:…"); a refresh token only works with the client that made
// it, so the client is stored with it.
type gdriveSettings struct {
	V            int       `json:"v"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Account      string    `json:"account,omitempty"`
	Email        string    `json:"email,omitempty"`
	Name         string    `json:"name,omitempty"`
	Auto         bool      `json:"auto"`
	Expired      bool      `json:"expired,omitempty"`
	LastResult   string    `json:"last_result,omitempty"`
	LastAt       time.Time `json:"last_at"`
}

// signedIn reports whether a refresh token is stored.
func (s gdriveSettings) signedIn() bool { return s.RefreshToken != "" && s.ClientID != "" }

// ready reports whether syncs can run without asking the player anything.
func (s gdriveSettings) ready() bool { return s.signedIn() && !s.Expired && s.Account != "" }

// accountLabel is how the page names the account.
func (s gdriveSettings) accountLabel() string {
	switch {
	case s.Email != "":
		return s.Email
	case s.Name != "":
		return s.Name
	}
	return "your Google account"
}

var gdriveStore struct {
	mu     sync.Mutex
	loaded bool
	value  gdriveSettings
}

// gdriveSettingsPath is a test seam.
var gdriveSettingsPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "gdrive.json")
}

func gdriveLoadLocked() {
	if gdriveStore.loaded {
		return
	}
	gdriveStore.loaded = true
	p := gdriveSettingsPath()
	if p == "" {
		return
	}
	var s gdriveSettings
	if _, err := readJSONWithBackup(p, &s); err == nil {
		gdriveStore.value = s
	} else if !os.IsNotExist(err) {
		logWarn("gdrive", "ignoring gdrive.json: %v", err)
	}
}

// gdriveSettingsGet is the stored sign-in (read from disk once).
func gdriveSettingsGet() gdriveSettings {
	gdriveStore.mu.Lock()
	defer gdriveStore.mu.Unlock()
	gdriveLoadLocked()
	return gdriveStore.value
}

// gdriveSettingsUpdate changes the stored sign-in and writes it.
func gdriveSettingsUpdate(change func(*gdriveSettings)) error {
	gdriveStore.mu.Lock()
	defer gdriveStore.mu.Unlock()
	gdriveLoadLocked()
	next := gdriveStore.value
	change(&next)
	next.V = 1
	p := gdriveSettingsPath()
	if p == "" {
		return errors.New("the settings folder is unavailable")
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(p, append(data, '\n'), 0o600); err != nil {
		return err
	}
	gdriveStore.value = next
	return nil
}

// gdriveForgetSettings deletes the stored sign-in, backup included (it holds
// the token too).
func gdriveForgetSettings() {
	gdriveStore.mu.Lock()
	defer gdriveStore.mu.Unlock()
	gdriveStore.loaded, gdriveStore.value = true, gdriveSettings{}
	if p := gdriveSettingsPath(); p != "" {
		_ = os.Remove(p)
		_ = os.Remove(p + ".bak")
	}
}

// gdriveAutoReady reports whether the automatic game syncs should run.
func gdriveAutoReady() bool {
	s := gdriveSettingsGet()
	return s.ready() && s.Auto
}

// ── HTTP ────────────────────────────────────────────────────────────────────

var gdriveHTTP struct {
	once   sync.Once
	client *http.Client
}

// gdriveHTTPClient talks to Google with the firmware CA fallback a handheld
// needs (bugrepRootCAs). There is no overall timeout: every call carries a
// context deadline sized to what it moves.
func gdriveHTTPClient() *http.Client {
	gdriveHTTP.once.Do(func() {
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			MaxIdleConns:          4,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       60 * time.Second,
		}
		if pool := bugrepRootCAs(); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		gdriveHTTP.client = &http.Client{Transport: tr}
	})
	return gdriveHTTP.client
}

// gdriveNetError drops the address from a transport error (it can carry a
// file id) and keeps the cause.
func gdriveNetError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		if errors.Is(ue.Err, context.Canceled) || errors.Is(ue.Err, context.DeadlineExceeded) {
			return ue.Err
		}
		return errors.New(firstLine(ue.Err.Error()))
	}
	return err
}

// gdriveErrorText is a sync or sign-in error as a sentence for a toast.
func gdriveErrorText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "Google Drive did not answer in time"
	case errors.Is(err, context.Canceled):
		return "stopped"
	case strings.Contains(msg, "certificate has expired or is not yet valid"):
		// A handheld that booted before its clock was set (TrimUI has no RTC
		// battery on some units) cannot check Google's certificate.
		return "this device's clock looks wrong — connect to Wi-Fi so it can set the time, then try again"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "network is unreachable"):
		return "no internet connection"
	}
	return firstLine(msg)
}

// ── OAuth answers ───────────────────────────────────────────────────────────

// gdriveTokenAnswer is the token endpoint's JSON.
type gdriveTokenAnswer struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	Scope            string `json:"scope"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// gdriveOAuthError is an OAuth error code ("authorization_pending",
// "invalid_grant", …).
type gdriveOAuthError struct {
	Code        string
	Description string
}

func (e *gdriveOAuthError) Error() string {
	if e.Description != "" {
		return "Google: " + e.Code + " (" + firstLine(e.Description) + ")"
	}
	return "Google: " + e.Code
}

// gdriveParseTokenAnswer reads the token endpoint's answer (outside data). An
// OAuth error comes back as *gdriveOAuthError whatever the HTTP status (Google
// answers authorization_pending with 428, slow_down with 403…).
func gdriveParseTokenAnswer(status int, body []byte) (gdriveTokenAnswer, error) {
	var a gdriveTokenAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return gdriveTokenAnswer{}, fmt.Errorf("Google answered HTTP %d with something that is not JSON", status)
	}
	if a.Error != "" {
		return gdriveTokenAnswer{}, &gdriveOAuthError{Code: a.Error, Description: a.ErrorDescription}
	}
	if status != http.StatusOK {
		return gdriveTokenAnswer{}, fmt.Errorf("Google answered HTTP %d", status)
	}
	if a.AccessToken == "" {
		return gdriveTokenAnswer{}, errors.New("Google sent no access token")
	}
	if a.ExpiresIn <= 0 || a.ExpiresIn > 24*3600 {
		a.ExpiresIn = 3600
	}
	return a, nil
}

// gdrivePostForm posts an OAuth form and returns the status and body.
func gdrivePostForm(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := gdriveHTTPClient().Do(req)
	if err != nil {
		return 0, nil, gdriveNetError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, gdriveAnswerLimit))
	if err != nil {
		return 0, nil, gdriveNetError(err)
	}
	return resp.StatusCode, data, nil
}

// gdriveDeviceCode is Google's answer to "give me a code to show".
type gdriveDeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Error           string `json:"error"`
	ErrorDesc       string `json:"error_description"`
}

// url is where the player goes on the phone.
func (d gdriveDeviceCode) url() string {
	u := setOr(d.VerificationURL, d.VerificationURI)
	if !strings.HasPrefix(u, "https://") {
		return "https://www.google.com/device"
	}
	return u
}

// gdriveRequestDeviceCode asks Google for a code to show.
func gdriveRequestDeviceCode(ctx context.Context, client gdriveClient) (gdriveDeviceCode, error) {
	if client.ID == "" {
		return gdriveDeviceCode{}, errGdriveNoClient
	}
	status, data, err := gdrivePostForm(ctx, gdriveDeviceCodeURL, url.Values{
		"client_id": {client.ID},
		"scope":     {gdriveScope},
	})
	if err != nil {
		return gdriveDeviceCode{}, err
	}
	var dc gdriveDeviceCode
	if err := json.Unmarshal(data, &dc); err != nil {
		return gdriveDeviceCode{}, fmt.Errorf("Google answered HTTP %d with something that is not JSON", status)
	}
	if dc.Error != "" {
		// invalid_client: the client is not a "TVs and Limited Input
		// devices" one, or it was deleted (docs/CLOUD_SYNC.md).
		return gdriveDeviceCode{}, &gdriveOAuthError{Code: dc.Error, Description: dc.ErrorDesc}
	}
	if status != http.StatusOK || dc.DeviceCode == "" || dc.UserCode == "" {
		return gdriveDeviceCode{}, fmt.Errorf("Google answered HTTP %d without a code", status)
	}
	if dc.Interval < 1 || dc.Interval > 60 {
		dc.Interval = 5
	}
	if dc.ExpiresIn < 60 || dc.ExpiresIn > 3600 {
		dc.ExpiresIn = 1800
	}
	return dc, nil
}

// gdrivePollDeviceToken waits until the player approved the code on the
// phone, polling at the interval Google asked for.
func gdrivePollDeviceToken(ctx context.Context, client gdriveClient, dc gdriveDeviceCode) (gdriveTokenAnswer, error) {
	interval := time.Duration(dc.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	form := url.Values{
		"client_id":     {client.ID},
		"client_secret": {client.Secret},
		"device_code":   {dc.DeviceCode},
		"grant_type":    {gdriveDeviceGrant},
	}
	for {
		if err := gdriveSleep(ctx, interval); err != nil {
			return gdriveTokenAnswer{}, err
		}
		if time.Now().After(deadline) {
			return gdriveTokenAnswer{}, errGdriveCodeExpired
		}
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		status, data, err := gdrivePostForm(pctx, gdriveTokenURL, form)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return gdriveTokenAnswer{}, ctx.Err()
			}
			// A dropped poll is tried again at the next tick.
			continue
		}
		if status >= 500 {
			// Google's own hiccup (OAuth refusals are 4xx): the code is still
			// good, so the next tick asks again.
			continue
		}
		ans, err := gdriveParseTokenAnswer(status, data)
		var oe *gdriveOAuthError
		if errors.As(err, &oe) {
			switch oe.Code {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5 * time.Second
				continue
			case "access_denied":
				return gdriveTokenAnswer{}, errGdriveDenied
			case "expired_token":
				return gdriveTokenAnswer{}, errGdriveCodeExpired
			}
		}
		if err != nil {
			return gdriveTokenAnswer{}, err
		}
		if ans.RefreshToken == "" {
			return gdriveTokenAnswer{}, errors.New("Google sent no refresh token")
		}
		return ans, nil
	}
}

// gdriveSleep waits d or until ctx ends.
func gdriveSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// gdriveAboutUser is the account a token belongs to.
type gdriveAboutUser struct {
	User struct {
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
		PermissionID string `json:"permissionId"`
	} `json:"user"`
}

// gdriveAbout asks Drive whose account an access token opens.
func gdriveAbout(ctx context.Context, access string) (gdriveAboutUser, error) {
	var out gdriveAboutUser
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gdriveAboutURL, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := gdriveHTTPClient().Do(req)
	if err != nil {
		return out, gdriveNetError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, gdriveAnswerLimit))
	if err != nil {
		return out, gdriveNetError(err)
	}
	if resp.StatusCode != http.StatusOK {
		return out, gdriveParseAPIError(resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, errors.New("Google Drive sent an account answer this version cannot read")
	}
	if out.User.PermissionID == "" {
		return out, errors.New("Google Drive did not say which account this is")
	}
	return out, nil
}

// gdriveFinishSignIn stores a fresh sign-in: the client, the refresh token
// (both encrypted), the account, and automatic sync switched on.
func gdriveFinishSignIn(ctx context.Context, client gdriveClient, ans gdriveTokenAnswer) (gdriveSettings, error) {
	about, err := gdriveAbout(ctx, ans.AccessToken)
	if err != nil {
		return gdriveSettings{}, err
	}
	refresh, err := EncryptAPIToken(ans.RefreshToken, "Google Drive refresh token")
	if err != nil {
		return gdriveSettings{}, err
	}
	secret := ""
	if client.Secret != "" {
		if secret, err = EncryptAPIToken(client.Secret, "Google OAuth client secret"); err != nil {
			return gdriveSettings{}, err
		}
	}
	err = gdriveSettingsUpdate(func(s *gdriveSettings) {
		prev := *s
		*s = gdriveSettings{
			ClientID:     client.ID,
			ClientSecret: secret,
			RefreshToken: refresh,
			Account:      about.User.PermissionID,
			Email:        about.User.EmailAddress,
			Name:         about.User.DisplayName,
			Auto:         true,
		}
		if prev.Account == about.User.PermissionID {
			// Signing in again to the same account keeps what the page said.
			s.LastResult, s.LastAt = prev.LastResult, prev.LastAt
		}
	})
	if err != nil {
		return gdriveSettings{}, err
	}
	gdriveToken.mu.Lock()
	gdriveToken.access, gdriveToken.refresh = ans.AccessToken, refresh
	gdriveToken.expires = time.Now().Add(time.Duration(ans.ExpiresIn) * time.Second)
	gdriveToken.mu.Unlock()
	logInfo("gdrive", "signed in to Google Drive (client from %s)", client.Source)
	return gdriveSettingsGet(), nil
}

// ── Access tokens ───────────────────────────────────────────────────────────

// gdriveToken caches the current access token; refresh names the stored
// refresh token it was made from, so a new sign-in drops it.
var gdriveToken struct {
	mu      sync.Mutex
	access  string
	refresh string
	expires time.Time
}

// gdriveAccessToken returns a valid access token, refreshing it when it has
// less than a minute left. A refresh Google refuses (invalid_grant: revoked
// in the account, or the 7-day limit of an app still in "Testing") marks the
// sign-in expired.
func gdriveAccessToken(ctx context.Context) (string, error) {
	gdriveToken.mu.Lock()
	defer gdriveToken.mu.Unlock()
	s := gdriveSettingsGet()
	switch {
	case !s.signedIn():
		return "", errGdriveSignedOut
	case s.Expired:
		return "", errGdriveExpired
	}
	if gdriveToken.access != "" && gdriveToken.refresh == s.RefreshToken && time.Until(gdriveToken.expires) > time.Minute {
		return gdriveToken.access, nil
	}
	refresh, err := DecryptString(s.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("the stored Google sign-in cannot be read (%v) — sign in again", err)
	}
	secret, err := DecryptString(s.ClientSecret)
	if err != nil {
		return "", fmt.Errorf("the stored Google client cannot be read (%v) — sign in again", err)
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	status, data, err := gdrivePostForm(rctx, gdriveTokenURL, url.Values{
		"client_id":     {s.ClientID},
		"client_secret": {secret},
		"refresh_token": {refresh},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return "", err
	}
	ans, err := gdriveParseTokenAnswer(status, data)
	var oe *gdriveOAuthError
	if errors.As(err, &oe) && (oe.Code == "invalid_grant" || oe.Code == "unauthorized_client" || oe.Code == "invalid_client") {
		logWarn("gdrive", "Google refused the stored sign-in: %s", oe.Code)
		gdriveToken.access = ""
		if uerr := gdriveSettingsUpdate(func(st *gdriveSettings) {
			// Only the sign-in that was refused: a sign-out and a new sign-in
			// can land while this refresh was on the wire.
			if st.RefreshToken == s.RefreshToken {
				st.Expired = true
			}
		}); uerr != nil {
			logWarn("gdrive", "could not record the expired sign-in: %v", uerr)
		}
		return "", errGdriveExpired
	}
	if err != nil {
		return "", err
	}
	gdriveToken.access, gdriveToken.refresh = ans.AccessToken, s.RefreshToken
	gdriveToken.expires = time.Now().Add(time.Duration(ans.ExpiresIn) * time.Second)
	return ans.AccessToken, nil
}

// gdriveDropAccessToken forgets the cached access token (Drive said 401).
func gdriveDropAccessToken() {
	gdriveToken.mu.Lock()
	gdriveToken.access = ""
	gdriveToken.mu.Unlock()
}

// gdriveSignOut forgets the sign-in here and revokes the token at Google in
// the background. The saves on Drive and this device's sync record stay.
func gdriveSignOut() {
	s := gdriveSettingsGet()
	gdriveForgetSettings()
	// Off the main thread: a refresh in flight holds gdriveToken.mu for up to
	// 20 s (gdriveAccessToken), and the forgotten settings already stop it.
	go gdriveDropAccessToken()
	if s.RefreshToken == "" {
		return
	}
	refresh, err := DecryptString(s.RefreshToken)
	if err != nil || refresh == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		status, _, err := gdrivePostForm(ctx, gdriveRevokeURL, url.Values{"token": {refresh}})
		if err != nil || status != http.StatusOK {
			// The token is gone from this device either way; Google also
			// lists it under the account's third-party access.
			logInfo("gdrive", "revoking the Google token did not complete (status %d, %v)", status, err)
		}
	}()
	logInfo("gdrive", "signed out of Google Drive")
}
