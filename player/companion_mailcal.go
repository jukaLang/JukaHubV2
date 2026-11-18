package main

// companion_mailcal.go — the Phone Companion's Email and Calendars cards
// (docs/EMAIL_CALENDAR.md).
//
// An address and an app password are long strings nobody types with a D-pad,
// so they come from the phone, like the RetroAchievements and RomM cards
// (companion_accounts.go). The handheld's Email & calendar page shows a code
// that opens the page at the card ("#mail", "#cal", companion_phonecard.go);
// the page lists both cards all the time.
//
// The page learns the presets (mailcal_store.go) from the GET, so typing
// "@icloud.com" fills the servers and says where Apple makes app passwords
// before anything is sent. What the phone sends is checked before it is
// saved: the mail server must accept the login (mailCheckAccount), and a
// calendar must answer (calCheckSource). A server that refuses is sent back as
// the card's error; a handheld that cannot reach the server right now still
// saves, and says so. A calendar that fails beside a working mailbox saves the
// mailbox and reports the calendar.
//
// Nothing secret goes back to the page: an account is its address, provider
// and status; a calendar its name, the link's host and the link's last four
// characters (companionAccountTail). Passwords and links are sealed
// (mailcalSeal) before the store sees them.
//
// The HTTP goroutine checks and saves (the store takes its own lock); the toast,
// the first sync and closing the code go to the main loop with tbxPost.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	// phoneCardMail and phoneCardCal are the companion page's cards
	// ("mailCard", "calCard").
	phoneCardMail phoneCardKind = "mail"
	phoneCardCal  phoneCardKind = "cal"
	// mailcalCheckWait bounds each check of what the phone sent: a mail
	// server's login, then a calendar's first answer.
	mailcalCheckWait = 20 * time.Second
	// mailcalBodyMax bounds a request from the page.
	mailcalBodyMax = 8 << 10
	// mailcalURLMax bounds a calendar link (Google's secret addresses are
	// about 150 characters).
	mailcalURLMax = 2048
)

// mailcalPhoneBusy lets one add from the phone run at a time: two quick taps
// on Add must not log in twice or save two copies.
var mailcalPhoneBusy sync.Mutex

// mailcalMSAvailable is mailMSConfigured as of the last time the main loop
// asked (mailcalStartup, the Settings page, the phone code), so the HTTP
// goroutine never reads the config's maps itself.
var mailcalMSAvailable atomic.Bool

// ── Validation (pure) ────────────────────────────────────────────────────

// mailcalHasControl reports whether s holds a control character (a pasted
// line break included).
func mailcalHasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// mailcalCleanEmail checks an address: one "@", something before it, a domain
// after it, no spaces. Pure.
func mailcalCleanEmail(raw string) (string, error) {
	email := strings.TrimSpace(raw)
	if email == "" {
		return "", errors.New("Type your email address first")
	}
	if len(email) > 254 || mailcalHasControl(email) || strings.ContainsAny(email, " \t<>\"(),;:\\[]") ||
		strings.Count(email, "@") != 1 || mailcalDomain(email) == "" || strings.HasPrefix(email, "@") {
		return "", errors.New("That does not look like an email address - check it")
	}
	if !strings.Contains(mailcalDomain(email), ".") {
		return "", errors.New("The address needs its full domain, like name@example.com")
	}
	return email, nil
}

// mailcalCleanHost checks a server name typed under Advanced: a host name or
// an IP address, without a scheme, port or path. "" stays "" (the preset's).
// Pure.
func mailcalCleanHost(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" {
		return "", nil
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if net.ParseIP(host) != nil {
		return host, nil
	}
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasPrefix(host, "-") || strings.Contains(host, "..") {
		return "", errors.New("A server name looks like imap.example.com - no https:// and no port")
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return "", errors.New("A server name looks like imap.example.com - no https:// and no port")
		}
	}
	return strings.ToLower(strings.TrimSuffix(host, ".")), nil
}

// mailcalCleanPort reads a port typed under Advanced; 0 for an empty field
// (the preset's). Pure.
func mailcalCleanPort(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 65535 {
		return 0, errors.New("A port is a number from 1 to 65535")
	}
	return n, nil
}

// mailcalCleanSec reads a connection security choice; "" keeps the preset's.
// Pure.
func mailcalCleanSec(raw string) (string, error) {
	switch s := strings.ToLower(strings.TrimSpace(raw)); s {
	case "", mailcalTLS, mailcalStartTLS:
		return s, nil
	}
	return "", errors.New("Security is TLS or STARTTLS")
}

// mailcalLANHost reports whether host is on the home network: a private,
// loopback or link-local address, or a name with no dot or under .local,
// .lan, .home.arpa or .internal. Pure.
func mailcalLANHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}
	if !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".lan", ".home.arpa", ".internal"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// mailcalCleanCalURL checks a calendar address: https, or webcal:// (the same
// link, which is fetched over https); plain http only for a server on the home
// network. A user name and password go in their own fields, never in the
// link. Pure.
func mailcalCleanCalURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > mailcalURLMax || mailcalHasControl(raw) || strings.ContainsAny(raw, " \t") {
		return "", errors.New("That link is not a calendar address - copy it again")
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "webcals://"):
		raw = "https://" + raw[len("webcals://"):]
	case strings.HasPrefix(lower, "webcal://"):
		raw = "https://" + raw[len("webcal://"):]
	case !strings.Contains(raw, "://"):
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("That link is not a calendar address - copy it again")
	}
	if u.User != nil {
		return "", errors.New("Put the user name and password in their own fields, not in the link")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !mailcalLANHost(u.Hostname()) {
			return "", errors.New("Calendars on the internet need an https:// link; http:// is only for a server on your home network")
		}
	default:
		return "", errors.New("A calendar link starts with https:// or webcal://")
	}
	u.Fragment = ""
	return u.String(), nil
}

// mailcalCleanText checks a short free-text field (a name, a user name). Pure.
func mailcalCleanText(raw string, max int, what string) (string, error) {
	s := strings.TrimSpace(raw)
	if len(s) > max || mailcalHasControl(s) {
		return "", errors.New(what + " is too long or has a line break")
	}
	return s, nil
}

// mailcalCleanSecret checks a password: up to 256 characters on one line.
// Pure.
func mailcalCleanSecret(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if len(s) > 256 || strings.ContainsAny(s, "\r\n\x00") {
		return "", errors.New("That password is too long or has a line break - copy it again")
	}
	return s, nil
}

// mailcalValidID reports whether id can be an ID mailcalNewID made. Pure.
func mailcalValidID(id string) bool {
	if id == "" || len(id) > 40 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// mailcalURLHost is the host a page or a screen may show for a sealed link.
func mailcalURLHost(sealed, keyName string) string {
	u, err := url.Parse(mailcalOpen(sealed, keyName))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// mailcalCheckUnreachable sorts a check's error: true when the server could
// not be asked (offline, a timeout, a connection that failed), false when it
// answered and refused, or no server has that name while the handheld is
// online.
func mailcalCheckUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	// The mail sync sorts its own failures (mail_imap.go): only its network
	// kind can be "not reachable".
	var me *mailError
	if errors.As(err, &me) && me.kind != mailErrNet {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return !dnsErr.IsNotFound || !IsOnline()
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return !IsOnline()
}

// mailcalCheckReason is a refusal as the card shows it: the server's own
// words, first line, bounded, or what a missing server name means.
func mailcalCheckReason(err error, what string) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return "No server called " + dnsErr.Name + " - check the " + what + " server under Advanced"
	}
	msg := mailcalErrLine(err.Error())
	if msg == "" {
		msg = "the server refused"
	}
	return msg
}

// mailcalErrLine is an error as the card shows it: its first line, cut at 300
// characters. firstLine's 96 would cut the provider's help in half ("make an
// app password at …"). Pure.
func mailcalErrLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return truncateRunesSuffix(strings.TrimSpace(s), 300, "…")
}

// ── Status ───────────────────────────────────────────────────────────────

// mailcalPresetJSON is a preset as the page sees it, with the domains that
// pick it.
type mailcalPresetJSON struct {
	Domains      []string `json:"domains"`
	Key          string   `json:"key"`
	Name         string   `json:"name"`
	IMAPHost     string   `json:"imapHost"`
	IMAPPort     int      `json:"imapPort"`
	IMAPSec      string   `json:"imapSec"`
	SMTPHost     string   `json:"smtpHost"`
	SMTPPort     int      `json:"smtpPort"`
	SMTPSec      string   `json:"smtpSec"`
	CalDAV       string   `json:"caldav"`
	OAuth        string   `json:"oauth"`
	PasswordHelp string   `json:"passwordHelp"`
	CalendarHelp string   `json:"calendarHelp"`
}

// mailcalProviderName is how screens name an account's provider.
func mailcalProviderName(a mailcalAccount) string {
	if p, ok := mailcalPresetByKey(a.Provider); ok {
		return p.Name
	}
	return "Custom"
}

// companionMailCalStatus is what the page may know: the accounts and
// calendars without a secret, the presets, and whether the handheld can sign
// in with Microsoft.
func companionMailCalStatus() map[string]interface{} {
	s := mailcalSettingsGet()
	accounts := make([]map[string]interface{}, 0, len(s.Accounts))
	for _, a := range s.Accounts {
		accounts = append(accounts, map[string]interface{}{
			"id":        a.ID,
			"email":     a.Email,
			"name":      a.Name,
			"provider":  mailcalProviderName(a),
			"microsoft": a.OAuth == mailcalOAuthMicrosoft,
			"mail":      !a.MailOff,
			"calendar":  a.CalDAVURL != "" && !a.CalOff,
			"calHost":   mailcalURLHost(a.CalDAVURL, ""),
			"signedIn":  a.hasSecret(),
			"mailError": a.MailError,
			"calError":  a.CalError,
		})
	}
	calendars := make([]map[string]interface{}, 0, len(s.Calendars))
	for _, c := range s.Calendars {
		link := mailcalOpen(c.URL, mailcalKeyCalURL)
		host := ""
		if u, err := url.Parse(link); err == nil {
			host = u.Hostname()
		}
		calendars = append(calendars, map[string]interface{}{
			"id":    c.ID,
			"name":  c.Name,
			"kind":  c.Kind,
			"host":  host,
			"tail":  companionAccountTail(link),
			"on":    !c.Off,
			"error": c.Error,
		})
	}
	presets := make([]mailcalPresetJSON, 0, len(mailcalPresets))
	for _, p := range mailcalPresets {
		v := p.preset
		presets = append(presets, mailcalPresetJSON{
			Domains: p.domains, Key: v.Key, Name: v.Name,
			IMAPHost: v.IMAPHost, IMAPPort: v.IMAPPort, IMAPSec: v.IMAPSec,
			SMTPHost: v.SMTPHost, SMTPPort: v.SMTPPort, SMTPSec: v.SMTPSec,
			CalDAV: v.CalDAVURL, OAuth: v.OAuth,
			PasswordHelp: v.PasswordHelp, CalendarHelp: v.CalendarHelp,
		})
	}
	return map[string]interface{}{
		"accounts":     accounts,
		"calendars":    calendars,
		"presets":      presets,
		"microsoft":    mailcalMSAvailable.Load(),
		"maxAccounts":  mailcalMaxAccounts,
		"maxCalendars": mailcalMaxCalendars,
	}
}

// ── Handler ──────────────────────────────────────────────────────────────

// mailcalPhoneRequest is everything the two cards send; action says which
// part applies.
type mailcalPhoneRequest struct {
	Action string `json:"action"` // addAccount, addCalendar, removeAccount, removeCalendar
	ID     string `json:"id"`

	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Username string `json:"username"`
	IMAPHost string `json:"imapHost"`
	IMAPPort string `json:"imapPort"`
	IMAPSec  string `json:"imapSec"`
	SMTPHost string `json:"smtpHost"`
	SMTPPort string `json:"smtpPort"`
	SMTPSec  string `json:"smtpSec"`
	CalDAV   string `json:"caldav"`

	URL string `json:"url"`
}

// mailcalHTTPError is a refusal with the status the page shows it under.
type mailcalHTTPError struct {
	status int
	msg    string
}

func (e *mailcalHTTPError) Error() string { return e.msg }

func mailcalUnprocessable(msg string) *mailcalHTTPError {
	return &mailcalHTTPError{status: http.StatusUnprocessableEntity, msg: msg}
}

// handleMailCal answers the page's Email and Calendars cards: GET is the
// status (no secrets), POST adds or removes an account or a calendar.
func (s *companionServer) handleMailCal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeCompanionJSON(w, companionMailCalStatus())
	case http.MethodPost:
		var body mailcalPhoneRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, mailcalBodyMax)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		var (
			msg string
			err error
		)
		switch body.Action {
		case "addAccount", "addCalendar":
			if !mailcalPhoneBusy.TryLock() {
				http.Error(w, "The handheld is still checking the last one - wait a moment", http.StatusConflict)
				return
			}
			if body.Action == "addAccount" {
				msg, err = mailcalPhoneAddAccount(r.Context(), body)
			} else {
				msg, err = mailcalPhoneAddCalendar(r.Context(), body)
			}
			mailcalPhoneBusy.Unlock()
		case "removeAccount", "removeCalendar":
			msg, err = mailcalPhoneRemove(body.Action == "removeAccount", body.ID)
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		if err != nil {
			status := http.StatusInternalServerError
			var he *mailcalHTTPError
			if errors.As(err, &he) {
				status = he.status
			}
			http.Error(w, mailcalErrLine(err.Error()), status)
			return
		}
		out := companionMailCalStatus()
		out["ok"], out["message"] = true, msg
		writeCompanionJSON(w, out)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// mailcalPhoneAddAccount checks and saves a mailbox from the phone. An
// address the handheld already has is replaced (same ID, so its cache and
// switches stay); an empty password keeps the saved one.
func mailcalPhoneAddAccount(ctx context.Context, body mailcalPhoneRequest) (string, error) {
	email, err := mailcalCleanEmail(body.Email)
	if err != nil {
		return "", mailcalUnprocessable(err.Error())
	}
	password, err := mailcalCleanSecret(body.Password)
	if err != nil {
		return "", mailcalUnprocessable(err.Error())
	}
	name, err := mailcalCleanText(body.Name, 100, "The name")
	if err != nil {
		return "", mailcalUnprocessable(err.Error())
	}
	username, err := mailcalCleanText(body.Username, 254, "The user name")
	if err != nil {
		return "", mailcalUnprocessable(err.Error())
	}
	imapHost, err := mailcalCleanHost(body.IMAPHost)
	if err != nil {
		return "", mailcalUnprocessable("IMAP: " + err.Error())
	}
	smtpHost, err := mailcalCleanHost(body.SMTPHost)
	if err != nil {
		return "", mailcalUnprocessable("SMTP: " + err.Error())
	}
	imapPort, err := mailcalCleanPort(body.IMAPPort)
	if err != nil {
		return "", mailcalUnprocessable("IMAP: " + err.Error())
	}
	smtpPort, err := mailcalCleanPort(body.SMTPPort)
	if err != nil {
		return "", mailcalUnprocessable("SMTP: " + err.Error())
	}
	imapSec, err := mailcalCleanSec(body.IMAPSec)
	if err != nil {
		return "", mailcalUnprocessable("IMAP: " + err.Error())
	}
	smtpSec, err := mailcalCleanSec(body.SMTPSec)
	if err != nil {
		return "", mailcalUnprocessable("SMTP: " + err.Error())
	}
	caldav, err := mailcalCleanCalURL(body.CalDAV)
	if err != nil {
		return "", mailcalUnprocessable("CalDAV: " + err.Error())
	}

	cur := mailcalSettingsGet()
	var prev mailcalAccount
	replacing := false
	for _, a := range cur.Accounts {
		if strings.EqualFold(a.Email, email) {
			prev, replacing = a, true
			break
		}
	}
	if !replacing && len(cur.Accounts) >= mailcalMaxAccounts {
		return "", mailcalUnprocessable("The handheld holds up to " + strconv.Itoa(mailcalMaxAccounts) + " accounts - remove one first")
	}

	acc := mailcalAccount{ID: prev.ID, Email: email, Name: name, Username: username, CalDAVURL: caldav,
		MailOff: prev.MailOff, CalOff: prev.CalOff}
	if acc.ID == "" {
		acc.ID = mailcalNewID()
	}
	if replacing {
		// A field left empty keeps what the handheld has, as on the
		// RetroAchievements and RomM cards: adding the address again to
		// change its password must not drop its name, its user name or its
		// calendar.
		if acc.Name == "" {
			acc.Name = prev.Name
		}
		if acc.Username == "" {
			acc.Username = prev.Username
		}
		if acc.CalDAVURL == "" {
			acc.CalDAVURL = prev.CalDAVURL
		}
	}
	preset, known := mailcalPresetFor(email)
	preset.apply(&acc)
	if replacing && prev.IMAPHost != "" && prev.SMTPHost != "" {
		// The servers it was saved with (typed under Advanced, or found by
		// the mail sync's autoconfig) beat the preset's; what the phone
		// typed beats both.
		acc.IMAPHost, acc.IMAPPort, acc.IMAPSec = prev.IMAPHost, prev.IMAPPort, prev.IMAPSec
		acc.SMTPHost, acc.SMTPPort, acc.SMTPSec = prev.SMTPHost, prev.SMTPPort, prev.SMTPSec
	}
	if imapHost != "" {
		acc.IMAPHost = imapHost
	}
	if imapPort != 0 {
		acc.IMAPPort = imapPort
	}
	if imapSec != "" {
		acc.IMAPSec = imapSec
	}
	if smtpHost != "" {
		acc.SMTPHost = smtpHost
	}
	if smtpPort != 0 {
		acc.SMTPPort = smtpPort
	}
	if smtpSec != "" {
		acc.SMTPSec = smtpSec
	}
	if acc.IMAPHost == "" || acc.SMTPHost == "" || acc.IMAPPort == 0 || acc.SMTPPort == 0 {
		return "", mailcalUnprocessable("Fill in the IMAP and SMTP servers under Advanced")
	}

	// Google shows its app passwords in groups of four; the spaces are not
	// part of it.
	if known && preset.Key == "gmail" {
		password = strings.ReplaceAll(password, " ", "")
	}
	switch {
	case password != "":
		// A password means a password login, even at a provider that
		// normally signs in with Microsoft (a custom setup).
		acc.OAuth = ""
		sealed, err := mailcalSeal(password, mailcalKeyPassword)
		if err != nil {
			return "", &mailcalHTTPError{status: http.StatusInternalServerError, msg: "The handheld could not encrypt the password"}
		}
		acc.Password = sealed
	case replacing && prev.OAuth == "" && prev.Password != "":
		// No password typed: the saved one stays, and so does its login.
		acc.OAuth, acc.Password = "", prev.Password
		password = mailcalOpen(prev.Password, mailcalKeyPassword)
	case replacing && prev.OAuth != "" && prev.OAuth == acc.OAuth && prev.RefreshToken != "":
		acc.RefreshToken = prev.RefreshToken
	case acc.OAuth == mailcalOAuthMicrosoft && mailcalMSAvailable.Load():
		return "", mailcalUnprocessable("Outlook addresses sign in with Microsoft: on the handheld, open Email & calendar and choose Sign in with Microsoft")
	case acc.OAuth == mailcalOAuthMicrosoft:
		return "", mailcalUnprocessable("Outlook.com does not take passwords from mail apps, and this build has no Microsoft sign-in. Add your Outlook calendar's ICS link in the Calendars card instead")
	default:
		return "", mailcalUnprocessable("Paste the app password too")
	}

	// The login, checked before anything is saved.
	mctx, cancel := context.WithTimeout(ctx, mailcalCheckWait)
	err = mailCheckAccount(mctx, acc)
	cancel()
	if ctx.Err() != nil {
		// The phone left before the answer: nothing it could see is saved.
		return "", ctx.Err()
	}
	mailChecked := err == nil
	sendNote := ""
	switch {
	case err == nil:
	case strings.HasPrefix(err.Error(), "Incoming mail works"):
		// Reading works and sending does not (a separate SMTP password, a
		// blocked port): the mailbox is still worth having. Asked before
		// "unreachable", so a blocked SMTP port says so instead of "could
		// not reach the mail server" right after the inbox opened.
		mailChecked = true
		sendNote = " " + strings.TrimSuffix(mailcalCheckReason(err, "SMTP"), ".") + "."
	case mailcalCheckUnreachable(err):
	default:
		// mailCheckAccount's error is already a sentence that names the
		// provider and where its app passwords are made.
		logInfo("mailcal", "phone account (%s) refused by its mail server", mailcalProviderName(acc))
		return "", mailcalUnprocessable(mailcalCheckReason(err, "IMAP"))
	}
	acc.MailError = ""

	// The calendar on the same login, when the account has one.
	calNote := ""
	if acc.CalDAVURL != "" && acc.OAuth == "" {
		cctx, ccancel := context.WithTimeout(ctx, mailcalCheckWait)
		_, cerr := calCheckSource(cctx, mailcalCalCalDAV, acc.CalDAVURL, acc.user(), password)
		ccancel()
		switch {
		case cerr == nil:
			calNote = " The calendar answered too."
		case mailcalCheckUnreachable(cerr):
			calNote = " The calendar could not be reached just now; it syncs when it can."
		default:
			acc.CalError = mailcalCheckReason(cerr, "CalDAV")
			calNote = " The calendar was refused: " + acc.CalError
		}
	}

	full := false
	if err := mailcalSettingsUpdate(func(s *mailcalSettings) {
		for i := range s.Accounts {
			if s.Accounts[i].ID == acc.ID || strings.EqualFold(s.Accounts[i].Email, acc.Email) {
				// The switches may have changed on the handheld meanwhile.
				acc.MailOff, acc.CalOff = s.Accounts[i].MailOff, s.Accounts[i].CalOff
				acc.ID = s.Accounts[i].ID
				// Microsoft hands out a new refresh token as it uses one:
				// the store's is the newest.
				if acc.OAuth != "" && acc.OAuth == s.Accounts[i].OAuth {
					acc.RefreshToken = s.Accounts[i].RefreshToken
				}
				s.Accounts[i] = acc
				return
			}
		}
		// The store cuts anything past the limit: an account added on
		// the handheld during the check must not make this one vanish
		// after "Saved".
		if len(s.Accounts) >= mailcalMaxAccounts {
			full = true
			return
		}
		s.Accounts = append(s.Accounts, acc)
	}); err != nil {
		logWarn("mailcal", "cannot save an account from the phone: %v", err)
		return "", &mailcalHTTPError{status: http.StatusInternalServerError, msg: "The handheld could not save it: " + firstLine(err.Error())}
	}
	if full {
		return "", mailcalUnprocessable("The handheld holds up to " + strconv.Itoa(mailcalMaxAccounts) + " accounts - remove one first")
	}
	logInfo("companion", "mail account received from the phone (%s, checked: %v)", mailcalProviderName(acc), mailChecked)
	label := acc.label()
	tbxPost(func() { mailcalPhoneArrived(phoneCardMail, "Mail: "+label+" added", true) })
	if !mailChecked {
		return "Saved on the handheld. JukaHub could not reach the mail server to check it just now; it syncs as soon as it can." + calNote, nil
	}
	if sendNote != "" {
		return "Saved on the handheld." + sendNote + calNote, nil
	}
	return "Saved on the handheld. The mail server accepted the login." + calNote, nil
}

// mailcalPhoneAddCalendar checks and saves a calendar subscription from the
// phone: a link (.ics, webcal://) when no user name or password came, CalDAV
// otherwise. The same link again replaces the saved one.
func mailcalPhoneAddCalendar(ctx context.Context, body mailcalPhoneRequest) (string, error) {
	link, err := mailcalCleanCalURL(body.URL)
	if err != nil {
		return "", mailcalUnprocessable(err.Error())
	}
	if link == "" {
		return "", mailcalUnprocessable("Paste the calendar's link first")
	}
	name, err := mailcalCleanText(body.Name, 80, "The name")
	if err != nil {
		return "", mailcalUnprocessable(err.Error())
	}
	user, err := mailcalCleanText(body.Username, 254, "The user name")
	if err != nil {
		return "", mailcalUnprocessable(err.Error())
	}
	pass, err := mailcalCleanSecret(body.Password)
	if err != nil {
		return "", mailcalUnprocessable(err.Error())
	}
	kind := mailcalCalICS
	if user != "" || pass != "" {
		kind = mailcalCalCalDAV
		if user == "" || pass == "" {
			return "", mailcalUnprocessable("A CalDAV calendar needs both the user name and the password; leave both empty for a link")
		}
	}

	cur := mailcalSettingsGet()
	prevID := ""
	for _, c := range cur.Calendars {
		if c.Kind == kind && mailcalOpen(c.URL, mailcalKeyCalURL) == link {
			prevID = c.ID
			break
		}
	}
	if prevID == "" && len(cur.Calendars) >= mailcalMaxCalendars {
		return "", mailcalUnprocessable("The handheld holds up to " + strconv.Itoa(mailcalMaxCalendars) + " calendars - remove one first")
	}

	cctx, cancel := context.WithTimeout(ctx, mailcalCheckWait)
	found, cerr := calCheckSource(cctx, kind, link, user, pass)
	cancel()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	checked := cerr == nil
	if cerr != nil && !mailcalCheckUnreachable(cerr) {
		return "", mailcalUnprocessable("The calendar was refused: " + mailcalCheckReason(cerr, "calendar"))
	}
	if name == "" {
		name = strings.TrimSpace(found)
	}
	if name == "" {
		if u, err := url.Parse(link); err == nil {
			name = u.Hostname()
		}
	}
	name = truncateRunesSuffix(name, 80, "")

	sealedURL, err := mailcalSeal(link, mailcalKeyCalURL)
	if err != nil {
		return "", &mailcalHTTPError{status: http.StatusInternalServerError, msg: "The handheld could not encrypt the link"}
	}
	sealedPass, err := mailcalSeal(pass, mailcalKeyCalPassword)
	if err != nil {
		return "", &mailcalHTTPError{status: http.StatusInternalServerError, msg: "The handheld could not encrypt the password"}
	}
	cal := mailcalCalendar{ID: prevID, Name: name, Kind: kind, URL: sealedURL, Username: user, Password: sealedPass}
	if cal.ID == "" {
		cal.ID = mailcalNewID()
	}
	full := false
	if err := mailcalSettingsUpdate(func(s *mailcalSettings) {
		for i := range s.Calendars {
			if s.Calendars[i].ID == cal.ID {
				cal.Color, cal.Off = s.Calendars[i].Color, s.Calendars[i].Off
				s.Calendars[i] = cal
				return
			}
		}
		if len(s.Calendars) >= mailcalMaxCalendars {
			full = true
			return
		}
		s.Calendars = append(s.Calendars, cal)
	}); err != nil {
		logWarn("mailcal", "cannot save a calendar from the phone: %v", err)
		return "", &mailcalHTTPError{status: http.StatusInternalServerError, msg: "The handheld could not save it: " + firstLine(err.Error())}
	}
	if full {
		return "", mailcalUnprocessable("The handheld holds up to " + strconv.Itoa(mailcalMaxCalendars) + " calendars - remove one first")
	}
	logInfo("companion", "calendar received from the phone (%s, checked: %v)", kind, checked)
	tbxPost(func() { mailcalPhoneArrived(phoneCardCal, "Calendar "+name+" added", false) })
	if !checked {
		return "Saved on the handheld. JukaHub could not reach the calendar to check it just now; it syncs as soon as it can.", nil
	}
	return "Saved on the handheld. The calendar answered.", nil
}

// mailcalPhoneRemove forgets an account or a calendar the page lists.
func mailcalPhoneRemove(account bool, id string) (string, error) {
	if !mailcalValidID(id) {
		return "", &mailcalHTTPError{status: http.StatusBadRequest, msg: "bad id"}
	}
	label := ""
	if account {
		label = mailcalRemoveAccount(id)
	} else {
		label = mailcalRemoveCalendar(id)
	}
	if label == "" {
		return "", &mailcalHTTPError{status: http.StatusNotFound, msg: "The handheld does not have that one any more"}
	}
	return "Removed " + label + " from the handheld.", nil
}

// mailcalRemoveAccount drops an account from the store and has the mail sync
// (and the calendar sync, for its CalDAV calendar) forget it on the main
// loop. It answers the account's label, "" when there was no such account.
// Any goroutine.
func mailcalRemoveAccount(id string) string {
	var gone mailcalAccount
	err := mailcalSettingsUpdate(func(s *mailcalSettings) {
		kept := s.Accounts[:0]
		for _, a := range s.Accounts {
			if a.ID == id {
				gone = a
				continue
			}
			kept = append(kept, a)
		}
		s.Accounts = kept
	})
	if err != nil || gone.ID == "" {
		if err != nil {
			logWarn("mailcal", "cannot remove an account: %v", err)
		}
		return ""
	}
	hadCal := gone.CalDAVURL != ""
	tbxPost(func() {
		mailForgetAccount(id)
		if hadCal {
			calForgetSource(id)
		}
		mailcalUIInvalidate()
	})
	return gone.label()
}

// mailcalRemoveCalendar drops a calendar from the store and has the calendar
// sync forget its events on the main loop. Any goroutine.
func mailcalRemoveCalendar(id string) string {
	var gone mailcalCalendar
	err := mailcalSettingsUpdate(func(s *mailcalSettings) {
		kept := s.Calendars[:0]
		for _, c := range s.Calendars {
			if c.ID == id {
				gone = c
				continue
			}
			kept = append(kept, c)
		}
		s.Calendars = kept
	})
	if err != nil || gone.ID == "" {
		if err != nil {
			logWarn("mailcal", "cannot remove a calendar: %v", err)
		}
		return ""
	}
	tbxPost(func() {
		calForgetSource(id)
		mailcalUIInvalidate()
	})
	if gone.Name != "" {
		return gone.Name
	}
	return "the calendar"
}

// ── On the handheld ──────────────────────────────────────────────────────

// mailcalSetupFromPhone shows the code that opens the companion's Email card.
// Main thread.
func mailcalSetupFromPhone() {
	mailcalRefreshMS(appConfig)
	phoneCardOpen(phoneCardMail, "Email · add it on your phone",
		"Scan it, type your address and app password, then tap Add")
}

// mailcalCalendarFromPhone shows the code that opens the companion's
// Calendars card. Main thread.
func mailcalCalendarFromPhone() {
	phoneCardOpen(phoneCardCal, "Calendar · add it on your phone",
		"Scan it, paste the calendar's link, then tap Add")
}

// mailcalStartup starts the mail and calendar syncs, a frame or two after the
// first (main.go's deferred startup tasks). Main thread.
func mailcalStartup(config *Config) {
	mailcalRefreshMS(config)
	mailStart()
	calSyncStart()
}

// mailcalRefreshMS notes whether Microsoft sign-in is possible, for the HTTP
// goroutine. Main thread.
func mailcalRefreshMS(config *Config) {
	if config == nil {
		return
	}
	mailcalMSAvailable.Store(mailMSConfigured(config))
}

// mailcalPhoneArrived applies what the phone saved, on the main loop: the code
// closes, the page and the syncs pick it up.
func mailcalPhoneArrived(kind phoneCardKind, toast string, mail bool) {
	phoneCardArrived(kind)
	mailcalUIInvalidate()
	if mail {
		mailSyncNow("phone")
	}
	calSyncNow("phone")
	ShowToast(toast, ToastKindSuccess)
}
