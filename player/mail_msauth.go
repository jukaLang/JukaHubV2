package main

// mail_msauth.go — signing in to Outlook.com and Microsoft 365 mail
// (docs/EMAIL_CALENDAR.md).
//
// Microsoft turned off password sign-in to IMAP and SMTP for most accounts,
// so an Outlook account signs in the way Google Drive does here: the OAuth
// 2.0 device-code flow. JukaHub shows a short code with a QR code of
// microsoft.com/link, the person approves on a phone, and the answer's
// refresh token is kept sealed in the account (mailcal.json). Access tokens
// are made from it on demand, held in memory only, and handed to IMAP and
// SMTP as XOAUTH2.
//
// The tenant is "common": one registration then serves outlook.com and
// hotmail.com addresses as well as work and school (Microsoft 365) mailboxes,
// which use the same IMAP and SMTP servers. That needs the app registered for
// "personal Microsoft accounts and accounts in any organizational
// directory"; a registration limited to personal accounts sets ms_tenant to
// "consumers" (config Custom, or JUKAHUB_MS_TENANT).
//
// Which client signs in: JUKAHUB_MS_CLIENT_ID in the environment, else
// ms_client_id in the config, else the ID the build stamped in with -ldflags
// "-X main.microsoftOAuthClientID=…". A public client: there is no secret.
// Without one, an Outlook account falls back to its password, which
// Microsoft will usually refuse (the error says so). Calendars from Microsoft
// are not read through this sign-in: Outlook calendars come in as ICS links.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// microsoftOAuthClientID is the Azure app registration a release build signs
// in with, stamped by the build (JUKAHUB_MS_CLIENT_ID). Empty without it.
var microsoftOAuthClientID string

const (
	msTenantDefault = "common"
	// msScopes are IMAP and SMTP for the signed-in mailbox, a refresh token,
	// and an ID token naming the address when the caller did not know it.
	msScopes      = "https://outlook.office.com/IMAP.AccessAsUser.All https://outlook.office.com/SMTP.Send offline_access openid profile"
	msDeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"
	msLinkURL     = "https://www.microsoft.com/link"
)

// msOAuth is the client ID and tenant as last read from the config on the
// main thread, for the sync goroutines (they must not read config maps).
var msOAuth struct {
	mu       sync.Mutex
	clientID string
	tenant   string
}

// msTenantOK accepts a tenant name or ID (letters, digits, dots, dashes).
func msTenantOK(t string) bool {
	if t == "" || len(t) > 64 {
		return false
	}
	for _, c := range t {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// msOAuthLoad reads the client ID and tenant (environment, config, build)
// and remembers them for msOAuthCurrent. Main thread.
func msOAuthLoad(config *Config) (string, string) {
	id := strings.TrimSpace(os.Getenv("JUKAHUB_MS_CLIENT_ID"))
	tenant := strings.TrimSpace(os.Getenv("JUKAHUB_MS_TENANT"))
	if config != nil && config.Variables.Custom != nil {
		if id == "" {
			v, _ := config.Variables.Custom["ms_client_id"].(string)
			id = strings.TrimSpace(v)
		}
		if tenant == "" {
			v, _ := config.Variables.Custom["ms_tenant"].(string)
			tenant = strings.TrimSpace(v)
		}
	}
	if id == "" {
		id = strings.TrimSpace(microsoftOAuthClientID)
	}
	if !msTenantOK(tenant) {
		tenant = msTenantDefault
	}
	msOAuth.mu.Lock()
	msOAuth.clientID, msOAuth.tenant = id, tenant
	msOAuth.mu.Unlock()
	return id, tenant
}

// msOAuthCurrent is the client ID and tenant for a goroutine: the
// environment and the build need no config; the config's values are the ones
// msOAuthLoad saw last.
func msOAuthCurrent() (string, string) {
	msOAuth.mu.Lock()
	id, tenant := msOAuth.clientID, msOAuth.tenant
	msOAuth.mu.Unlock()
	if id == "" {
		id = strings.TrimSpace(os.Getenv("JUKAHUB_MS_CLIENT_ID"))
	}
	if id == "" {
		id = strings.TrimSpace(microsoftOAuthClientID)
	}
	if !msTenantOK(tenant) {
		tenant = msTenantDefault
	}
	return id, tenant
}

// mailMSConfigured reports whether a Microsoft client ID is available. Main
// thread.
func mailMSConfigured(config *Config) bool {
	id, _ := msOAuthLoad(config)
	return id != ""
}

// msEndpoint is the tenant's OAuth endpoint ("devicecode" or "token").
func msEndpoint(tenant, which string) string {
	return "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/" + which
}

// ── Answers ─────────────────────────────────────────────────────────────

// msOAuthError is an OAuth error code from Microsoft.
type msOAuthError struct {
	Code        string
	Description string
}

func (e *msOAuthError) Error() string {
	return "Microsoft: " + e.Code
}

// msNewOAuthError keeps an answer's error code and description as text safe
// to show: they reach toasts and the account's error line.
func msNewOAuthError(code, desc string) *msOAuthError {
	return &msOAuthError{Code: mailClean(code, 60), Description: mailClean(desc, 200)}
}

// msTokenAnswer is the token endpoint's JSON.
type msTokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	IDToken      string `json:"id_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// msParseToken reads a token answer (outside data). An OAuth error comes back
// as *msOAuthError whatever the HTTP status.
func msParseToken(status int, body []byte) (msTokenAnswer, error) {
	var a msTokenAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return msTokenAnswer{}, fmt.Errorf("Microsoft answered HTTP %d with something that is not JSON", status)
	}
	if a.Error != "" {
		return msTokenAnswer{}, msNewOAuthError(a.Error, a.ErrorDesc)
	}
	if status != http.StatusOK || a.AccessToken == "" {
		return msTokenAnswer{}, fmt.Errorf("Microsoft answered HTTP %d without a token", status)
	}
	if a.ExpiresIn <= 0 || a.ExpiresIn > 24*3600 {
		a.ExpiresIn = 3600
	}
	return a, nil
}

// msDeviceCode is the answer to "give me a code to show".
type msDeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Error           string `json:"error"`
	ErrorDesc       string `json:"error_description"`
}

// msRequestDeviceCode asks Microsoft for a code to show.
func msRequestDeviceCode(ctx context.Context, clientID, tenant string) (msDeviceCode, error) {
	status, data, err := gdrivePostForm(ctx, msEndpoint(tenant, "devicecode"), url.Values{
		"client_id": {clientID},
		"scope":     {msScopes},
	})
	if err != nil {
		return msDeviceCode{}, mailNetErr(err)
	}
	var dc msDeviceCode
	if err := json.Unmarshal(data, &dc); err != nil {
		return msDeviceCode{}, fmt.Errorf("Microsoft answered HTTP %d with something that is not JSON", status)
	}
	if dc.Error != "" {
		return msDeviceCode{}, msNewOAuthError(dc.Error, dc.ErrorDesc)
	}
	if status != http.StatusOK || dc.DeviceCode == "" || dc.UserCode == "" {
		return msDeviceCode{}, fmt.Errorf("Microsoft answered HTTP %d without a code", status)
	}
	if dc.Interval < 1 || dc.Interval > 60 {
		dc.Interval = 5
	}
	if dc.ExpiresIn < 60 || dc.ExpiresIn > 3600 {
		dc.ExpiresIn = 900
	}
	if !strings.HasPrefix(dc.VerificationURI, "https://") {
		dc.VerificationURI = msLinkURL
	}
	return dc, nil
}

var (
	errMSDenied  = errors.New("the sign-in was declined on the phone")
	errMSExpired = errors.New("the code expired before it was entered — start again")
)

// msPollDeviceToken waits until the code was approved.
func msPollDeviceToken(ctx context.Context, clientID, tenant string, dc msDeviceCode) (msTokenAnswer, error) {
	interval := time.Duration(dc.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	form := url.Values{
		"client_id":   {clientID},
		"device_code": {dc.DeviceCode},
		"grant_type":  {msDeviceGrant},
	}
	for {
		if err := gdriveSleep(ctx, interval); err != nil {
			return msTokenAnswer{}, err
		}
		if time.Now().After(deadline) {
			return msTokenAnswer{}, errMSExpired
		}
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		status, data, err := gdrivePostForm(pctx, msEndpoint(tenant, "token"), form)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return msTokenAnswer{}, ctx.Err()
			}
			continue // a dropped poll is tried again at the next tick
		}
		if status >= 500 {
			continue
		}
		ans, err := msParseToken(status, data)
		var oe *msOAuthError
		if errors.As(err, &oe) {
			switch oe.Code {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5 * time.Second
				continue
			case "authorization_declined", "access_denied":
				return msTokenAnswer{}, errMSDenied
			case "expired_token", "code_expired":
				return msTokenAnswer{}, errMSExpired
			}
		}
		if err != nil {
			return msTokenAnswer{}, err
		}
		if ans.RefreshToken == "" {
			return msTokenAnswer{}, errors.New("Microsoft sent no refresh token")
		}
		return ans, nil
	}
}

// msIDTokenEmail reads the address from an ID token's claims. Not verified:
// it only names an account the IMAP sign-in then proves.
func msIDTokenEmail(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 || len(parts[1]) > 16<<10 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	for _, v := range []string{claims.Email, claims.PreferredUsername} {
		if v = strings.TrimSpace(v); mailcalDomain(v) != "" {
			return mailClean(v, 200)
		}
	}
	return ""
}

// ── Access tokens ───────────────────────────────────────────────────────

// msToken is one account's access token; refresh names the stored (sealed)
// refresh token it came from, so a new sign-in drops it.
type msToken struct {
	access  string
	refresh string
	expires time.Time
}

var msTokens struct {
	mu sync.Mutex
	m  map[string]msToken
}

// errMSSignInExpired is the person's next step when Microsoft refuses the
// stored sign-in.
var errMSSignInExpired = &mailError{kind: mailErrAuth, msg: "the Microsoft sign-in expired or was removed — sign in again in Settings → Email & calendar"}

// mailMSAccessToken is a valid access token for a Microsoft account,
// refreshed when it has less than a minute left. Microsoft rotates refresh
// tokens: a new one is sealed into the store. Any goroutine.
func mailMSAccessToken(ctx context.Context, a mailcalAccount) (string, error) {
	msTokens.mu.Lock()
	defer msTokens.mu.Unlock()
	if msTokens.m == nil {
		msTokens.m = map[string]msToken{}
	}
	if t, ok := msTokens.m[a.ID]; ok && t.refresh == a.RefreshToken && time.Until(t.expires) > time.Minute {
		return t.access, nil
	}
	refresh := mailcalOpen(a.RefreshToken, "Microsoft refresh token")
	if refresh == "" {
		return "", &mailError{kind: mailErrAuth, msg: "the Microsoft sign-in cannot be read on this device — sign in again"}
	}
	clientID, tenant := msOAuthCurrent()
	if clientID == "" {
		return "", &mailError{kind: mailErrAuth, msg: "this build has no Microsoft sign-in set up (ms_client_id)"}
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	status, data, err := gdrivePostForm(rctx, msEndpoint(tenant, "token"), url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"scope":         {msScopes},
	})
	if err != nil {
		return "", mailNetErr(err)
	}
	ans, err := msParseToken(status, data)
	var oe *msOAuthError
	if errors.As(err, &oe) {
		switch oe.Code {
		case "invalid_grant", "interaction_required", "unauthorized_client", "invalid_client":
			logWarn("mail", "Microsoft refused the stored sign-in: %s", oe.Code)
			delete(msTokens.m, a.ID)
			return "", errMSSignInExpired
		}
		return "", &mailError{kind: mailErrServer, msg: "Microsoft refused: " + oe.Code}
	}
	if err != nil {
		return "", err
	}
	stored := a.RefreshToken
	if ans.RefreshToken != "" && ans.RefreshToken != refresh {
		if sealed, serr := mailcalSeal(ans.RefreshToken, "Microsoft refresh token"); serr == nil && sealed != "" {
			applied := false
			uerr := mailcalSettingsUpdate(func(s *mailcalSettings) {
				for i := range s.Accounts {
					// Only the sign-in that was refreshed: a new sign-in may
					// have landed meanwhile.
					if s.Accounts[i].ID == a.ID && s.Accounts[i].RefreshToken == a.RefreshToken {
						s.Accounts[i].RefreshToken = sealed
						applied = true
					}
				}
			})
			// The cached token names what the store now holds, so the next
			// caller's copy of the account matches it.
			if uerr == nil && applied {
				stored = sealed
			}
		}
	}
	msTokens.m[a.ID] = msToken{access: ans.AccessToken, refresh: stored, expires: time.Now().Add(time.Duration(ans.ExpiresIn) * time.Second)}
	return ans.AccessToken, nil
}

// msForgetToken drops an account's cached access token. Off the main thread:
// a refresh in flight holds the lock for up to 20 s.
func msForgetToken(id string) {
	go func() {
		msTokens.mu.Lock()
		delete(msTokens.m, id)
		msTokens.mu.Unlock()
	}()
}

// mailMSStoreAccount adds the signed-in mailbox, or updates the account with
// that address, with the sealed refresh token.
func mailMSStoreAccount(email, sealed string) (mailcalAccount, error) {
	var out mailcalAccount
	err := mailcalSettingsUpdate(func(s *mailcalSettings) {
		for i := range s.Accounts {
			if strings.EqualFold(strings.TrimSpace(s.Accounts[i].Email), email) {
				a := &s.Accounts[i]
				if a.IMAPHost == "" || a.SMTPHost == "" {
					p, _ := mailcalPresetByKey("outlook")
					p.apply(a)
				}
				a.OAuth, a.RefreshToken, a.MailError, a.MailOff = mailcalOAuthMicrosoft, sealed, "", false
				out = *a
				return
			}
		}
		if len(s.Accounts) >= mailcalMaxAccounts {
			return
		}
		a := mailcalAccount{ID: mailcalNewID(), Email: email}
		p, ok := mailcalPresetFor(email)
		if !ok || p.OAuth != mailcalOAuthMicrosoft {
			// A Microsoft 365 mailbox on its own domain: Outlook's servers.
			p, _ = mailcalPresetByKey("outlook")
		}
		p.apply(&a)
		a.OAuth, a.RefreshToken = mailcalOAuthMicrosoft, sealed
		s.Accounts = append(s.Accounts, a)
		out = a
	})
	if err == nil && out.ID == "" {
		err = fmt.Errorf("there are already %d mail accounts — remove one first", mailcalMaxAccounts)
	}
	return out, err
}

// ── Sign-in on screen ───────────────────────────────────────────────────

// mailMSSign is the sign-in the Mail screen shows. Main thread only; gen
// tells a finished sign-in's late answers apart from the next one's.
var mailMSSign struct {
	active  bool
	gen     int
	email   string
	code    string
	url     string
	qr      [][]bool
	expires time.Time
	cancel  context.CancelFunc
}

// mailMSSignIn signs in a Microsoft mailbox: the Mail screen opens with a
// code and a QR code of microsoft.com/link, and the account is added (or
// updated) when the phone says yes. email may be "" (the sign-in names it).
// Main thread.
func mailMSSignIn(config *Config, email string) {
	email = strings.TrimSpace(email)
	mailOpenApp()
	// The sign-in card is drawn on the list: a draft or an open message would
	// hide it. The draft itself stays in mailUI.compose.
	mailUI.view = mailViewList
	if mailMSSign.active {
		return
	}
	clientID, tenant := msOAuthLoad(config)
	if clientID == "" {
		mailOpenMSSetupSheet(config)
		return
	}
	if !IsOnline() {
		ShowToast("Connect to Wi-Fi first — signing in needs the internet", ToastKindWarning)
		return
	}
	PlayActivateSound()
	ctx, cancel := context.WithCancel(context.Background())
	mailMSSign.gen++
	gen := mailMSSign.gen
	mailMSSign.active, mailMSSign.cancel, mailMSSign.email = true, cancel, email
	mailMSSign.code, mailMSSign.url, mailMSSign.qr = "", "", nil
	mailUI.nav.focus = "ms-cancel"
	current := func() bool { return mailMSSign.active && mailMSSign.gen == gen }
	fail := func(err error) {
		tbxPost(func() {
			if current() {
				mailMSSignInEnded(err)
			}
		})
	}
	go func() {
		defer cancel()
		cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
		dc, err := msRequestDeviceCode(cctx, clientID, tenant)
		ccancel()
		if err != nil {
			fail(err)
			return
		}
		qr := tbxQRModules(dc.VerificationURI)
		tbxPost(func() {
			if !current() {
				return
			}
			mailMSSign.code, mailMSSign.url, mailMSSign.qr = dc.UserCode, dc.VerificationURI, qr
			mailMSSign.expires = time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
			if len(qr) > 0 {
				mailUI.nav.focus = "ms-qr"
			}
		})
		ans, err := msPollDeviceToken(ctx, clientID, tenant, dc)
		if err != nil {
			fail(err)
			return
		}
		addr := email
		if addr == "" {
			addr = msIDTokenEmail(ans.IDToken)
		}
		if mailcalDomain(addr) == "" {
			fail(errors.New("Microsoft did not say which address signed in — add the account with its address"))
			return
		}
		sealed, err := mailcalSeal(ans.RefreshToken, "Microsoft refresh token")
		if err != nil {
			fail(err)
			return
		}
		acct, err := mailMSStoreAccount(addr, sealed)
		if err != nil {
			fail(err)
			return
		}
		msTokens.mu.Lock()
		if msTokens.m == nil {
			msTokens.m = map[string]msToken{}
		}
		msTokens.m[acct.ID] = msToken{access: ans.AccessToken, refresh: sealed, expires: time.Now().Add(time.Duration(ans.ExpiresIn) * time.Second)}
		msTokens.mu.Unlock()
		logInfo("mail", "signed in a Microsoft mailbox")
		vctx, vcancel := context.WithTimeout(ctx, 45*time.Second)
		checkErr := mailCheckAccount(vctx, acct)
		vcancel()
		tbxPost(func() {
			if !current() {
				return
			}
			mailMSSignInEnded(nil)
			if checkErr != nil {
				PlayErrorSound()
				ShowToast("Signed in, but the mailbox did not open: "+mailErrorText(checkErr, acct), ToastKindWarning)
			} else {
				PlaySuccessSound()
				ShowToast("Signed in as "+acct.label(), ToastKindSuccess)
			}
			mailUI.account = acct.ID
			mailSyncNow("microsoft sign-in")
		})
	}()
}

// mailMSSignInEnded clears the sign-in card; err (other than a cancel) is
// shown.
func mailMSSignInEnded(err error) {
	if mailMSSign.cancel != nil {
		mailMSSign.cancel()
	}
	mailMSSign.active, mailMSSign.cancel = false, nil
	mailMSSign.code, mailMSSign.url, mailMSSign.qr = "", "", nil
	mailUI.nav.focus = ""
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	text := gdriveErrorText(err)
	var oe *msOAuthError
	if errors.As(err, &oe) {
		text = "Microsoft refused (" + oe.Code + ")"
		if oe.Code == "invalid_client" || oe.Code == "unauthorized_client" || oe.Code == "invalid_request" {
			text += " — the app registration may not allow this kind of account (ms_tenant)"
		}
	}
	logWarn("mail", "Microsoft sign-in failed: %s", text)
	ShowToast("Microsoft sign-in: "+text, ToastKindWarning)
}

// mailMSCancelSignIn stops waiting for the phone.
func mailMSCancelSignIn() {
	if mailMSSign.active {
		mailMSSignInEnded(context.Canceled)
	}
}

// mailOpenMSSetupSheet says why this build cannot sign in with Microsoft.
func mailOpenMSSetupSheet(config *Config) {
	tbxOpenSheet(config, tbxSheetSpec{
		title:    "Microsoft sign-in is not set up",
		subtitle: "This build has no Microsoft app registration",
		icon:     "message",
		body: "Outlook, Hotmail and Live mailboxes sign in with Microsoft. This build was made without the app registration that needs.\n\n" +
			"## What you can do\n" +
			"• Add the account with its password instead. Microsoft refuses passwords for most accounts, so this only works where it still allows them\n" +
			"• Whoever builds JukaHub can register a public client in Azure (Microsoft Entra), with the IMAP.AccessAsUser.All and SMTP.Send permissions, and build with JUKAHUB_MS_CLIENT_ID — or put ms_client_id in the config\n\n" +
			"Gmail, iCloud, Yahoo, Fastmail and most other providers work with an app password.",
		buttons: []tbxSheetButton{{label: "Close", primary: true}},
	})
}
