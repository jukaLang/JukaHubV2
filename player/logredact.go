package main

// logredact.go — credentials never reach a log line.
//
// Every line the app logs passes through redactLogSecrets in logRingHandler
// before it reaches the ring (the Logs screen, logs.txt, the bug report, the
// diagnostics bundle) or stderr (errors.txt on the card). logPatch and the
// JukaHub OS start, which write stderr directly, pass through it as well.
//
// It removes values whose name says they are a credential — "token=…",
// "password: …", "Authorization: Bearer …", "access_token" in JSON, a URL's
// "?key=…", "&code=…" or "user:pass@" — and tokens with a well-known shape
// (JWTs, Groq/OpenAI/GitHub/Slack/Google keys). It also removes the exact
// values of the secrets kept in JukaHub's settings (logStoredSecrets), and
// their URL-encoded forms, wherever they appear: a mail password inside a
// server's error, a calendar's secret link, a token pasted into free text.
// Secrets in other stores (Steam, Google Drive, RetroAchievements, RomM, PSN,
// Wi-Fi and VPN) are not in that list. It is a safety net for an error that
// echoes a request or a header: call sites still must not pass a secret, a PIN
// or a pairing code to a log call.
//
// Ordinary lines cost one allocation-free scan per stored value and one for
// the trigger words: the expressions run only when the line holds one.

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// The stored-value list's bounds: values shorter than the minimum (or longer
// than the maximum: a Microsoft refresh token runs to about 2 KB) are left to
// the patterns, and at most logStoredSecretMaxCount values are kept (each
// with up to two URL-encoded forms, so at most three times as many strings).
const (
	logStoredSecretMinLen   = 6
	logStoredSecretMaxLen   = 4096
	logStoredSecretMaxCount = 64
)

// logStoredSecrets holds the exact values of the secrets JukaHub has stored,
// decrypted, and their URL-encoded forms, longest first.
// logStoredSecretsRebuild replaces it on the main thread after a config load
// or save; redactLogSecrets reads it on any goroutine without a lock. A nil
// pointer (the zero value) means none yet: the package logs while it
// initialises, before any config exists, and an atomic.Pointer needs no
// initialiser.
var logStoredSecrets atomic.Pointer[[]string]

// logStoredSecretsFrom is the fingerprint of what the list was last built
// from (logStoredSecretsMailSum plus logStoredSecretsCustomSum). Main thread
// only.
var logStoredSecretsFrom uint64

// logStoredSecretsMailSum and logStoredSecretsMail are the mail and calendar
// half of the list as last read from mailcal's store: the fingerprint of its
// secrets as stored, and those secrets stored and opened
// (aiMailCalSecretsOf). Kept while the store is busy. Main thread only.
var (
	logStoredSecretsMailSum uint64
	logStoredSecretsMail    []string
)

// logStoredSecretsRebuild makes the stored-value list from the mail and
// calendar secrets (known credentials) and config's settings whose names say
// they are credentials (aiCustomSecretsOf, a guess). Main thread, after a
// config load or save. Most saves change no credential (playtime, levels):
// those keep the list without decrypting anything again, so a sealed value
// that will not open (a config from another device) is not reported once per
// save. It never logs a value: a seal that will not open names only its
// setting.
func logStoredSecretsRebuild(config *Config) {
	logStoredSecretsMailRefresh()
	from := logStoredSecretsMailSum + logStoredSecretsCustomSum(config)
	if from == logStoredSecretsFrom && logStoredSecrets.Load() != nil {
		return
	}
	logStoredSecretsFrom = from
	logStoredSecretsSet(logStoredSecretsMail, aiCustomSecretsOf(config))
}

// logStoredSecretsMailRefresh reads mailcal's secrets again when its store is
// free and they changed. The store holds its lock across a write to the card
// (mailcalSettingsUpdate, called by the sync goroutines on every poll), so the
// main thread only tries the lock and keeps the last read while it is busy;
// the next settings load or save tries again. Main thread.
func logStoredSecretsMailRefresh() {
	if !mailcalStore.mu.TryLock() {
		return
	}
	mailcalLoadLocked()
	s := mailcalCopy(mailcalStore.value)
	mailcalStore.mu.Unlock()
	var sum uint64
	for _, acc := range s.Accounts {
		sum += logFNV(acc.Password, acc.RefreshToken)
	}
	for _, c := range s.Calendars {
		sum += logFNV(c.URL, c.Password)
	}
	if sum == logStoredSecretsMailSum {
		return
	}
	logStoredSecretsMailSum = sum
	logStoredSecretsMail = aiMailCalSecretsOf(s)
}

// logStoredSecretsCustomSum sums a hash of every credential setting's name and
// stored value, in any order, without decrypting anything.
func logStoredSecretsCustomSum(config *Config) uint64 {
	var sum uint64
	if config != nil {
		for name, raw := range config.Variables.Custom {
			if v, ok := raw.(string); ok && aiSecretName(name) {
				sum += logFNV(name, v)
			}
		}
	}
	return sum
}

// logFNV is FNV-1a over a, a separator byte and b.
func logFNV(a, b string) uint64 {
	const prime = 1099511628211
	h := uint64(14695981039346656037)
	for i := 0; i < len(a); i++ {
		h = (h ^ uint64(a[i])) * prime
	}
	h *= prime
	for i := 0; i < len(b); i++ {
		h = (h ^ uint64(b[i])) * prime
	}
	return h
}

// logStoredSecretsSet keeps the values a log line can be searched for: known
// credentials (the mail and calendar secrets) and guessed ones (settings whose
// names say they are credentials). Each is trimmed, within the length bounds,
// kept once, and neither a sealed "ENC:" form (the list holds the decrypted
// value) nor a file path (a calendar can be a local .ics file). A guessed
// value must also not read as a setting's label, choice or number
// (logStoredSecretIsLabel): "30 days" under "cookieRetentionLabel" or
// "screenshot" under "selectKey" would blank every line that names it. A known
// value is kept as it is: a Gmail app password is four lower-case words.
// Known values come first, then guesses, each longest first, at most
// logStoredSecretMaxCount in all; each kept value then brings its URL-encoded
// forms, since a password with "@" or a space is percent-encoded in a CalDAV
// or IMAP URL. The list is longest first, so a value that holds another is
// replaced whole.
func logStoredSecretsSet(known, guessed []string) {
	seen := make(map[string]bool, len(known)+len(guessed))
	pick := func(values []string, guess bool) []string {
		var out []string
		for _, v := range values {
			v = strings.TrimSpace(v)
			if len(v) < logStoredSecretMinLen || len(v) > logStoredSecretMaxLen || seen[v] ||
				strings.HasPrefix(v, "ENC:") || logStoredSecretIsPath(v) || (guess && logStoredSecretIsLabel(v)) {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
		logStoredSecretsSort(out)
		return out
	}
	list := pick(known, false)
	list = append(list, pick(guessed, true)...)
	if len(list) > logStoredSecretMaxCount {
		list = list[:logStoredSecretMaxCount]
	}
	n := len(list)
	for i := 0; i < n; i++ {
		v := list[i]
		for _, e := range [...]string{url.QueryEscape(v), url.PathEscape(v)} {
			if e != v && !seen[e] {
				seen[e] = true
				list = append(list, e)
			}
		}
	}
	logStoredSecretsSort(list)
	logStoredSecrets.Store(&list)
}

// logStoredSecretsSort orders values longest first, then alphabetically.
func logStoredSecretsSort(values []string) {
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
}

// logStoredSecretIsPath reports whether v is a file path ("/mnt/SDCARD/…",
// "~/…", `C:\…`): a setting that names a cookies file is not the secret.
func logStoredSecretIsPath(v string) bool {
	if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "~/") {
		return true
	}
	return len(v) >= 3 && v[1] == ':' && (v[2] == '\\' || v[2] == '/') &&
		(('a' <= v[0] && v[0] <= 'z') || ('A' <= v[0] && v[0] <= 'Z'))
}

// logStoredSecretIsLabel reports whether a value found under a credential-like
// setting name reads as a label, a choice or a number rather than a key, token
// or link: it holds a space or a tab ("30 days", "Keep · since Oct 30"), or
// only digits and ".:/,+-" (a size or a date; a weak PIN would blank every
// such number in a log and its place in the line would hint at it), or only
// lower-case letters and ":,+_.-" ("screenshot", "select:start,start:select").
func logStoredSecretIsLabel(v string) bool {
	if strings.ContainsAny(v, " \t") {
		return true
	}
	digits, word := true, true
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !('0' <= c && c <= '9') && strings.IndexByte(".:/,+-", c) < 0 {
			digits = false
		}
		if !('a' <= c && c <= 'z') && strings.IndexByte(":,+_.-", c) < 0 {
			word = false
		}
	}
	return digits || word
}

// logRedactStoredSecrets replaces every stored value in text by "[redacted]".
func logRedactStoredSecrets(text string) string {
	list := logStoredSecrets.Load()
	if list == nil {
		return text
	}
	for _, s := range *list {
		if strings.Contains(text, s) {
			text = strings.ReplaceAll(text, s, "[redacted]")
		}
	}
	return text
}

// The expressions are compiled on first use behind a zero-value sync.Once,
// not in package-level initialisers: cryptoKey (crypto.go) logs while the
// package is still initialising, through slog's Handler interface, and Go's
// initialisation order does not follow interface calls, so a
// `var x = sync.OnceValue(...)` here could still be nil at that moment.
var (
	logSecretOnce sync.Once
	logSecretRE   logSecretPatterns
)

type logSecretPatterns struct {
	// urlParam is a query parameter whose name ends in a credential word:
	// ?key=, &access_token=, &code= (OAuth and party codes), &sig=. The value
	// stops at the next parameter so the rest of the URL stays readable.
	urlParam *regexp.Regexp
	// urlUser is the "user:password@" of a URL.
	urlUser *regexp.Regexp
	// named is "name: value" or "name=value" (also "name":"value") where the
	// name is a credential: discord_token, api_key, x-api-key, groq_key,
	// rp_key, client_secret, PrivateKey, PresharedKey, npsso, psk, pin,
	// Cookie... A bare "key" is not one (key=13 is a button). The value is a
	// quoted string or runs to whitespace, a comma, a semicolon or an
	// ampersand; "Bearer"/"Basic" in front of it go with it.
	named *regexp.Regexp
	// bearer is the credential in "Bearer <x>" when the header name was
	// already removed or never written.
	bearer *regexp.Regexp
	// shaped is a token recognisable by its shape alone: a JWT, a
	// Groq/OpenAI/GitHub/Slack key or a Google OAuth access token.
	shaped *regexp.Regexp
}

func logSecretPatternsCompiled() *logSecretPatterns {
	logSecretOnce.Do(func() {
		logSecretRE = logSecretPatterns{
			urlParam: regexp.MustCompile(`(?i)([?&][a-z0-9_.\-]*(?:key|token|secret|passwd|password|pass|pwd|code|npsso|sig|signature|auth|session|sessionid|sid|ticket)=)[^&\s"'#<>\\]+`),
			urlUser:  regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`),
			named:    regexp.MustCompile(`(?i)\b((?:[a-z0-9]+[_.-])*(?:api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|auth[_-]?token|id[_-]?token|token|client[_-]?secret|secret|password|passwd|passphrase|psk|npsso|private[_-]?key|privatekey|preshared[_-]?key|presharedkey|authorization|set-cookie|cookie|session[_-]?id|pairing[_-]?code|pin)|(?:[a-z0-9]+[_-])+key)\b(["']?\s*[:=]\s*)(?:(?:bearer|basic)\s+)?("[^"\n]*"|'[^'\n]*'|[^\s,;&]+)`),
			bearer:   regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{8,}`),
			shaped:   regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*|\b(?:gsk_|sk-|ghp_|gho_|ghs_|ghu_|github_pat_|xox[abpr]-|ya29\.)[A-Za-z0-9_\-.]{16,}`),
		}
	})
	return &logSecretRE
}

// redactLogSecrets returns text with credentials replaced by "[redacted]" and
// URL userinfo by "[credentials]". Paths, addresses and names stay: the Logs
// screen needs them (diagnostics_bundle.go strips account names on export).
// The stored values go first, wherever they are, before the patterns.
func redactLogSecrets(text string) string {
	text = logRedactStoredSecrets(text)
	if !logMayHoldSecret(text) {
		return text
	}
	re := logSecretPatternsCompiled()
	text = re.urlParam.ReplaceAllString(text, "${1}[redacted]")
	text = re.urlUser.ReplaceAllString(text, "://[credentials]@")
	// The named field runs before the bare bearer token: "Authorization: Bearer
	// <jwt>" must become one [redacted], not "[redacted] [redacted]".
	text = re.named.ReplaceAllString(text, "${1}${2}[redacted]")
	text = re.bearer.ReplaceAllString(text, "${1} [redacted]")
	text = re.shaped.ReplaceAllString(text, "[redacted]")
	return text
}

// logMayHoldSecret reports whether text contains a trigger word, ignoring
// ASCII case, without allocating. The triggers are the lower-case words
// without which no expression above can match; the short ones carry their
// separator ("sid=", not "sid") so "inside" or "ping" do not wake the
// expressions on every line. The list is local for the same reason the
// expressions are lazy: it must exist while the package initialises.
func logMayHoldSecret(text string) bool {
	triggers := [...]string{
		"key", "token", "secret", "pass", "pwd", "npsso", "psk", "auth", "bearer",
		"cookie", "session", "code=", "pairing", "pin=", "pin:", "pin\"", "pin'", "pin ", "pin\t", "sig=",
		"signature=", "sid=", "ticket=", "@", "eyj", "sk-", "gsk_", "ghp_", "gho_",
		"ghs_", "ghu_", "github_pat_", "xox", "ya29.",
	}
	for i := range triggers {
		if logContainsFold(text, triggers[i]) {
			return true
		}
	}
	return false
}

// logContainsFold is strings.Contains for a lower-case ASCII needle that
// ignores the case of s.
func logContainsFold(s, lower string) bool {
	n := len(lower)
	if n == 0 {
		return true
	}
	first := lower[0]
	for i := 0; i+n <= len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != first {
			continue
		}
		if strings.EqualFold(s[i:i+n], lower) {
			return true
		}
	}
	return false
}
