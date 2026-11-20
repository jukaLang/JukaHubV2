package main

// mailcal_store.go — the accounts behind Email and Calendar sync
// (docs/EMAIL_CALENDAR.md).
//
// One file, mailcal.json beside the other settings (0600), holds:
//
//   - mail accounts: an address, the IMAP and SMTP servers, and either an app
//     password or (Outlook) a Microsoft sign-in's refresh token. An account
//     may also name a CalDAV address, because the same app password opens
//     the calendar at iCloud, Fastmail, Yahoo and most self-hosted servers;
//   - calendar subscriptions: an .ics / webcal:// link (Google's "secret
//     address in iCal format", Outlook's published calendar, a sports
//     schedule), or a CalDAV calendar with its own user name and password;
//   - the few switches the Settings page shows.
//
// Every secret is stored encrypted ("ENC:…", EncryptAPIToken) — passwords,
// refresh tokens, and subscription links too, since a Google secret address
// is a password in all but name. The page and the phone only ever learn the
// last four characters (companionAccountTail).
//
// mail_*.go (IMAP, SMTP, the Mail app), calsync_*.go (calendar sync, events in
// the clock's calendar) and mailcal_ui/companion_mailcal.go (the Settings page
// and the phone card) all read and change this one store.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// mailcalDefaultPoll is how often mail and calendars sync in the
	// background when the setting is left at zero.
	mailcalDefaultPoll = 15
	// mailcalDefaultReminder is how many minutes before an event its reminder
	// shows when the setting is left at zero.
	mailcalDefaultReminder = 10
	// mailcalMaxAccounts and mailcalMaxCalendars bound the store.
	mailcalMaxAccounts  = 8
	mailcalMaxCalendars = 16
)

// Security of a mail server connection.
const (
	mailcalTLS      = "tls"      // TLS from the first byte (993, 465)
	mailcalStartTLS = "starttls" // plain, then STARTTLS (143, 587); never sent in the clear
)

// mailcalOAuthMicrosoft marks an account that signs in with Microsoft
// (outlook.com, hotmail.com, live.com …): IMAP/SMTP use XOAUTH2 with an access
// token made from RefreshToken.
const mailcalOAuthMicrosoft = "microsoft"

// mailcalAccount is one mailbox. Password and RefreshToken are "ENC:…".
type mailcalAccount struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name,omitempty"`     // display name for From:
	Provider string `json:"provider,omitempty"` // mailcalPreset.Key, "" = custom
	Username string `json:"username,omitempty"` // "" = Email

	IMAPHost string `json:"imap_host"`
	IMAPPort int    `json:"imap_port"`
	IMAPSec  string `json:"imap_security"` // mailcalTLS or mailcalStartTLS
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"`
	SMTPSec  string `json:"smtp_security"`

	Password     string `json:"password,omitempty"`
	OAuth        string `json:"oauth,omitempty"` // "" or mailcalOAuthMicrosoft
	RefreshToken string `json:"refresh_token,omitempty"`

	// MailOff stops this account's mail sync without forgetting it.
	MailOff bool `json:"mail_off,omitempty"`
	// CalDAVURL is the account's calendar server ("" = none); it signs in
	// with the same user name and password.
	CalDAVURL string `json:"caldav_url,omitempty"`
	CalOff    bool   `json:"cal_off,omitempty"`

	// Written by the syncs: the last error a person should see ("" = fine).
	MailError string `json:"mail_error,omitempty"`
	CalError  string `json:"cal_error,omitempty"`
}

// user is the IMAP/SMTP/CalDAV login name.
func (a mailcalAccount) user() string {
	if u := strings.TrimSpace(a.Username); u != "" {
		return u
	}
	return strings.TrimSpace(a.Email)
}

// label is how screens name the account.
func (a mailcalAccount) label() string {
	if a.Email != "" {
		return a.Email
	}
	return "Mail account"
}

// hasSecret reports whether the account can sign in without asking.
func (a mailcalAccount) hasSecret() bool {
	if a.OAuth != "" {
		return a.RefreshToken != ""
	}
	return a.Password != ""
}

// Kinds of calendar subscription.
const (
	mailcalCalICS    = "ics"    // an .ics / webcal:// link, read-only
	mailcalCalCalDAV = "caldav" // a CalDAV server with its own login
)

// mailcalCalendar is one calendar subscription that is not part of a mail
// account. URL and Password are "ENC:…".
type mailcalCalendar struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // mailcalCalICS or mailcalCalCalDAV
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Color    string `json:"color,omitempty"` // "#rrggbb", "" = theme accent
	Off      bool   `json:"off,omitempty"`
	Error    string `json:"error,omitempty"`
}

// mailcalSettings is mailcal.json.
type mailcalSettings struct {
	V         int               `json:"v"`
	Accounts  []mailcalAccount  `json:"accounts,omitempty"`
	Calendars []mailcalCalendar `json:"calendars,omitempty"`

	// PollMinutes is the background sync interval (0 = mailcalDefaultPoll).
	PollMinutes int `json:"poll_minutes,omitempty"`
	// NoMailNotify hides the "new mail" notifications.
	NoMailNotify bool `json:"no_mail_notify,omitempty"`
	// NoReminders hides event reminders; ReminderMinutes is how early they
	// show (0 = mailcalDefaultReminder).
	NoReminders     bool `json:"no_reminders,omitempty"`
	ReminderMinutes int  `json:"reminder_minutes,omitempty"`
}

// poll is the effective background sync interval in minutes.
func (s mailcalSettings) poll() int {
	if s.PollMinutes <= 0 {
		return mailcalDefaultPoll
	}
	if s.PollMinutes < 5 {
		return 5
	}
	return s.PollMinutes
}

// reminder is the effective reminder lead in minutes.
func (s mailcalSettings) reminder() int {
	if s.ReminderMinutes <= 0 {
		return mailcalDefaultReminder
	}
	return s.ReminderMinutes
}

// account finds an account by ID.
func (s mailcalSettings) account(id string) (mailcalAccount, bool) {
	for _, a := range s.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return mailcalAccount{}, false
}

var mailcalStore struct {
	mu     sync.Mutex
	loaded bool
	value  mailcalSettings
}

// mailcalSettingsPath is a test seam.
var mailcalSettingsPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "mailcal.json")
}

// mailcalDataDir is where the syncs keep their caches (mail headers,
// calendar events) and where loose .ics files may be dropped
// (mailcalDataDir()/calendars/*.ics). "" when there is no data folder.
func mailcalDataDir() string {
	p := mailcalSettingsPath()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "mailcal")
}

func mailcalLoadLocked() {
	if mailcalStore.loaded {
		return
	}
	mailcalStore.loaded = true
	p := mailcalSettingsPath()
	if p == "" {
		return
	}
	var s mailcalSettings
	if _, err := readJSONWithBackup(p, &s); err == nil {
		mailcalStore.value = s
	} else if !os.IsNotExist(err) {
		logWarn("mailcal", "ignoring mailcal.json: %v", err)
	}
}

// mailcalSettingsGet is a copy of the store (read from disk once). Any
// goroutine.
func mailcalSettingsGet() mailcalSettings {
	mailcalStore.mu.Lock()
	defer mailcalStore.mu.Unlock()
	mailcalLoadLocked()
	return mailcalCopy(mailcalStore.value)
}

// mailcalSettingsUpdate changes the store and writes it. The change runs
// under the lock on a copy: keep it short and never call back into the store
// from it. Any goroutine.
func mailcalSettingsUpdate(change func(*mailcalSettings)) error {
	mailcalStore.mu.Lock()
	defer mailcalStore.mu.Unlock()
	mailcalLoadLocked()
	next := mailcalCopy(mailcalStore.value)
	change(&next)
	next.V = 1
	if len(next.Accounts) > mailcalMaxAccounts {
		next.Accounts = next.Accounts[:mailcalMaxAccounts]
	}
	if len(next.Calendars) > mailcalMaxCalendars {
		next.Calendars = next.Calendars[:mailcalMaxCalendars]
	}
	p := mailcalSettingsPath()
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
	secretsChanged := logStoredSecretsMailFingerprint(next) != logStoredSecretsMailFingerprint(mailcalStore.value)
	mailcalStore.value = next
	if secretsChanged {
		// This function can run on the phone or sync goroutine. The main loop
		// rebuilds the exact-value redaction after the write, retrying if this
		// lock has not been released by the time it drains the queue.
		tbxPost(logStoredSecretsMailChangedOnMain)
	}
	return nil
}

// mailcalCopy deep-copies the slices so callers cannot alias the store.
func mailcalCopy(s mailcalSettings) mailcalSettings {
	s.Accounts = append([]mailcalAccount(nil), s.Accounts...)
	s.Calendars = append([]mailcalCalendar(nil), s.Calendars...)
	return s
}

// mailcalNewID is a short random ID for an account or a calendar.
func mailcalNewID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "id" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// mailcalSeal encrypts a secret for the store ("" stays ""). keyName only
// labels log lines.
func mailcalSeal(plain, keyName string) (string, error) {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return "", nil
	}
	return EncryptAPIToken(plain, keyName)
}

// Key names that label sealed values in log lines (mailcalSeal, mailcalOpen):
// a mail account's password, and a calendar subscription's link and password.
const (
	mailcalKeyPassword    = "mail password"
	mailcalKeyCalURL      = "calendar link"
	mailcalKeyCalPassword = "calendar password"
)

// mailcalOpen decrypts a stored secret; "" when it is empty or unreadable
// (a settings file copied from another device). A value saved by hand in
// plain text is used as it is.
func mailcalOpen(stored, keyName string) string {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return ""
	}
	if !strings.HasPrefix(stored, "ENC:") {
		return stored
	}
	plain, err := DecryptAPIToken(stored, keyName)
	if err != nil {
		return ""
	}
	return plain
}

// ── Provider presets ─────────────────────────────────────────────────────

// mailcalPreset is what JukaHub knows about a mail provider, so the phone
// card only needs an address and an app password.
type mailcalPreset struct {
	Key      string
	Name     string
	IMAPHost string
	IMAPPort int
	IMAPSec  string
	SMTPHost string
	SMTPPort int
	SMTPSec  string
	// CalDAVURL is the provider's CalDAV start address ("" = it has none
	// that an app password opens; use an .ics link instead).
	CalDAVURL string
	// OAuth is mailcalOAuthMicrosoft for providers that refuse passwords.
	OAuth string
	// PasswordHelp is where to make an app password; CalendarHelp says how
	// to find the calendar link when CalDAVURL is empty.
	PasswordHelp string
	CalendarHelp string
}

var mailcalPresets = []struct {
	domains []string
	preset  mailcalPreset
}{
	{[]string{"gmail.com", "googlemail.com"}, mailcalPreset{
		Key: "gmail", Name: "Gmail",
		IMAPHost: "imap.gmail.com", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "smtp.gmail.com", SMTPPort: 465, SMTPSec: mailcalTLS,
		PasswordHelp: "myaccount.google.com/apppasswords (needs 2-Step Verification)",
		CalendarHelp: "Google Calendar on a computer → Settings → your calendar → Secret address in iCal format",
	}},
	{[]string{"outlook.com", "hotmail.com", "live.com", "msn.com", "outlook.co.uk", "hotmail.co.uk", "live.co.uk", "hotmail.fr", "outlook.fr", "hotmail.de", "outlook.de", "hotmail.it", "hotmail.es"}, mailcalPreset{
		Key: "outlook", Name: "Outlook",
		IMAPHost: "outlook.office365.com", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "smtp-mail.outlook.com", SMTPPort: 587, SMTPSec: mailcalStartTLS,
		OAuth:        mailcalOAuthMicrosoft,
		CalendarHelp: "outlook.com → Settings → Calendar → Shared calendars → Publish a calendar → ICS link",
	}},
	{[]string{"icloud.com", "me.com", "mac.com"}, mailcalPreset{
		Key: "icloud", Name: "iCloud",
		IMAPHost: "imap.mail.me.com", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "smtp.mail.me.com", SMTPPort: 587, SMTPSec: mailcalStartTLS,
		CalDAVURL:    "https://caldav.icloud.com/",
		PasswordHelp: "account.apple.com → Sign-In and Security → App-Specific Passwords",
	}},
	{[]string{"yahoo.com", "ymail.com", "rocketmail.com", "yahoo.co.uk", "yahoo.fr", "yahoo.de"}, mailcalPreset{
		Key: "yahoo", Name: "Yahoo Mail",
		IMAPHost: "imap.mail.yahoo.com", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "smtp.mail.yahoo.com", SMTPPort: 465, SMTPSec: mailcalTLS,
		CalDAVURL:    "https://caldav.calendar.yahoo.com/",
		PasswordHelp: "login.yahoo.com/account/security → Generate app password",
	}},
	{[]string{"fastmail.com", "fastmail.fm"}, mailcalPreset{
		Key: "fastmail", Name: "Fastmail",
		IMAPHost: "imap.fastmail.com", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "smtp.fastmail.com", SMTPPort: 465, SMTPSec: mailcalTLS,
		CalDAVURL:    "https://caldav.fastmail.com/",
		PasswordHelp: "Fastmail → Settings → Privacy & Security → App passwords",
	}},
	{[]string{"aol.com"}, mailcalPreset{
		Key: "aol", Name: "AOL Mail",
		IMAPHost: "imap.aol.com", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "smtp.aol.com", SMTPPort: 465, SMTPSec: mailcalTLS,
		PasswordHelp: "login.aol.com/account/security → Generate app password",
	}},
	{[]string{"gmx.com", "gmx.us"}, mailcalPreset{
		Key: "gmx", Name: "GMX",
		IMAPHost: "imap.gmx.com", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "mail.gmx.com", SMTPPort: 587, SMTPSec: mailcalStartTLS,
		PasswordHelp: "GMX → Settings → POP3 & IMAP → allow access",
	}},
	{[]string{"gmx.net", "gmx.de", "gmx.at", "gmx.ch"}, mailcalPreset{
		Key: "gmxnet", Name: "GMX",
		IMAPHost: "imap.gmx.net", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "mail.gmx.net", SMTPPort: 587, SMTPSec: mailcalStartTLS,
		PasswordHelp: "GMX → Einstellungen → POP3 & IMAP Abruf erlauben",
	}},
	{[]string{"zoho.com", "zohomail.com"}, mailcalPreset{
		Key: "zoho", Name: "Zoho Mail",
		IMAPHost: "imap.zoho.com", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "smtp.zoho.com", SMTPPort: 465, SMTPSec: mailcalTLS,
		CalDAVURL:    "https://calendar.zoho.com/caldav/",
		PasswordHelp: "accounts.zoho.com → Security → App Passwords",
	}},
	{[]string{"yandex.com", "yandex.ru", "ya.ru"}, mailcalPreset{
		Key: "yandex", Name: "Yandex Mail",
		IMAPHost: "imap.yandex.com", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "smtp.yandex.com", SMTPPort: 465, SMTPSec: mailcalTLS,
		CalDAVURL:    "https://caldav.yandex.ru/",
		PasswordHelp: "id.yandex.com → Security → App passwords",
	}},
	{[]string{"mail.ru", "inbox.ru", "list.ru", "bk.ru"}, mailcalPreset{
		Key: "mailru", Name: "Mail.ru",
		IMAPHost: "imap.mail.ru", IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "smtp.mail.ru", SMTPPort: 465, SMTPSec: mailcalTLS,
		PasswordHelp: "Mail.ru → Settings → Security → Passwords for external apps",
	}},
}

// mailcalDomain is the lower-case part after the last "@", or "".
func mailcalDomain(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return ""
	}
	return email[at+1:]
}

// mailcalPresetFor is the preset for an address's provider. ok is false for
// a domain JukaHub does not know: the result is then the usual guess
// (imap.<domain>:993, smtp.<domain>:465) with Key "", which the mail sync may
// replace after asking the domain's autoconfig. Pure.
func mailcalPresetFor(email string) (mailcalPreset, bool) {
	domain := mailcalDomain(email)
	for _, p := range mailcalPresets {
		for _, d := range p.domains {
			if d == domain {
				return p.preset, true
			}
		}
	}
	if domain == "" {
		return mailcalPreset{}, false
	}
	return mailcalPreset{
		Name:     domain,
		IMAPHost: "imap." + domain, IMAPPort: 993, IMAPSec: mailcalTLS,
		SMTPHost: "smtp." + domain, SMTPPort: 465, SMTPSec: mailcalTLS,
	}, false
}

// mailcalPresetByKey is the preset with this Key.
func mailcalPresetByKey(key string) (mailcalPreset, bool) {
	if key == "" {
		return mailcalPreset{}, false
	}
	for _, p := range mailcalPresets {
		if p.preset.Key == key {
			return p.preset, true
		}
	}
	return mailcalPreset{}, false
}

// apply copies the preset's servers into an account.
func (p mailcalPreset) apply(a *mailcalAccount) {
	a.Provider = p.Key
	a.IMAPHost, a.IMAPPort, a.IMAPSec = p.IMAPHost, p.IMAPPort, p.IMAPSec
	a.SMTPHost, a.SMTPPort, a.SMTPSec = p.SMTPHost, p.SMTPPort, p.SMTPSec
	a.OAuth = p.OAuth
	if a.CalDAVURL == "" {
		a.CalDAVURL = p.CalDAVURL
	}
}
