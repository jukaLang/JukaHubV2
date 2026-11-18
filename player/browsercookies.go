package main

// browsercookies.go — the reader's cookie jar, and its copy on the card.
//
// A page load is not one request: the reader fetches the HTML, then its
// stylesheets, images and scripts, each on its own. Sites that hand out a
// session cookie only work when all of those requests share one cookie store —
// a consent answer, an age gate, or a redirect that only sticks once the cookie
// is sent back. They also need the store consulted between redirect hops, which
// net/http does by itself when the client has a Jar: every hop's Set-Cookie is
// stored and the next hop's request is built from the jar.
//
// The jar is written to data/browser-cookies.json so a page that signed the
// user in is still signed in after the launcher restarts. That is the whole
// point of a session cookie on a handheld: the app is quit every time the
// device sleeps, and retyping a login on a D-pad is miserable. What it costs is
// a file that outlives the page, so the file is written 0600 (nothing else on
// the card may read a session token), it holds only what the jar accepted, and
// there is one press that forgets it (Browser → Forget).
//
// Nothing here keeps a history: no URLs, no page titles, no visit times. The
// two timestamps are "savedAt" (when the file was last written) and a
// per-cookie "firstSeen" (when that cookie was first stored), which the
// retention setting in Settings → General ages a record against. The host is
// the only address-like field, and it is required for the jar to work at all.
//
// Only the browser's own requests use the jar. Feeds, update checks, the ticker
// and the podcast fetchers keep their own clients, so a cookie a page set can
// never ride along on an unrelated request.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// browserCookieSchemaVersion versions the file so a later change can tell
	// an old document from a new one instead of guessing at missing fields.
	browserCookieSchemaVersion = 1
	// browserCookieMaxRecords is a ceiling, not a policy: a hostile or broken
	// site could otherwise grow the file without bound. Sorting before the cut
	// keeps the truncation deterministic, so the same file is rewritten the
	// same way instead of churning on every save.
	browserCookieMaxRecords = 256
	// browserCookieFileMode keeps a session token out of reach of anything else
	// on a multi-user install. On the handheld's FAT/exFAT card the bits may be
	// ignored — the host is the only user — but the write asks for the safe
	// mode anyway.
	browserCookieFileMode os.FileMode = 0o600
	// browserForgetConfirmWindow is how long the Browser's Forget chip stays
	// armed after the first press. It mirrors the Wi-Fi picker's forgotten
	// network: losing a login on a handheld means retyping it on a controller.
	browserForgetConfirmWindow = 5 * time.Second
)

// browserCookieRecord is one stored cookie as it is written to the card. The
// standard jar only hands back names and values, so every attribute the file
// needs is captured here when the cookie is first set: without them the jar
// could not be rebuilt — a Secure cookie restored over http would be dropped,
// and an expiry would be lost.
type browserCookieRecord struct {
	// Host is the request host the cookie was set from, without its port
	// (browserCookieRecordHost): the jar needs a URL, and this is the one it
	// already accepted the cookie for. A port-less IPv6 literal keeps its
	// brackets, because the jar files "[::1]" and "[::1]:8080" apart.
	Host     string `json:"host"`
	Domain   string `json:"domain,omitempty"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Value    string `json:"value"`
	Secure   bool   `json:"secure,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	// Expires is empty for a session cookie. Session cookies are saved on
	// purpose: a login that ends with the process is the thing this file
	// exists to prevent.
	Expires string `json:"expires,omitempty"`
	// FirstSeen is when this cookie was first stored, not when it was last
	// refreshed. It is what the retention setting ages a record against (see
	// browsercookies_retention.go). An empty value means "unknown", which is
	// how a file written before the field existed reads: it is never stale,
	// and the clock starts when the file is next loaded.
	FirstSeen string `json:"firstSeen,omitempty"`
}

// browserCookieFile is the on-disk document.
type browserCookieFile struct {
	SchemaVersion int                   `json:"schemaVersion"`
	SavedAt       string                `json:"savedAt,omitempty"`
	Cookies       []browserCookieRecord `json:"cookies"`
}

// browserCookieJar wraps the standard jar so every store can be mirrored to the
// card. The inner jar stays the source of truth for what is sent (it owns
// expiry and path/host matching); the records are the extra attributes it does
// not keep.
type browserCookieJar struct {
	mu   sync.Mutex
	jar  *cookiejar.Jar
	recs map[string]browserCookieRecord
	// path is captured when the jar is built, so a save cannot land in a
	// directory that moved underneath it (tests point this at a temp dir).
	path string
	// forgotten retires a jar that has been replaced by a Forget or a reset. A
	// page load can still be on the wire with the old jar in hand; without
	// this, its response would write the session back to the card a moment
	// after the user deleted it.
	forgotten bool
}

func newBrowserCookieJar(path string) *browserCookieJar {
	inner, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: browserPublicSuffixes{}})
	if err != nil {
		// Without a public suffix list there is no jar and no cookies, but a
		// page still loads.
		return nil
	}
	return &browserCookieJar{
		jar:  inner,
		recs: map[string]browserCookieRecord{},
		path: path,
	}
}

// SetCookies is the http.CookieJar entry point. It stores in the jar, mirrors
// the result to records, and saves when that changed anything. A retired jar
// stores nothing at all, so a request that finishes after a Forget leaves no
// trace.
func (j *browserCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if j == nil || u == nil {
		return
	}
	j.mu.Lock()
	if j.forgotten {
		j.mu.Unlock()
		return
	}
	j.jar.SetCookies(u, cookies)
	changed := j.trackLocked(u, cookies)
	j.mu.Unlock()
	if changed {
		j.save()
	}
}

// retire empties the jar and stops it writing anywhere. It is how a Forget
// closes the door on requests that were already in flight.
func (j *browserCookieJar) retire() {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.forgotten = true
	j.recs = map[string]browserCookieRecord{}
	j.mu.Unlock()
}

// Cookies is the http.CookieJar read half. It is a straight pass-through: the
// jar decides what a request may carry.
func (j *browserCookieJar) Cookies(u *url.URL) []*http.Cookie {
	if j == nil || u == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.jar.Cookies(u)
}

// trackLocked mirrors one Set-Cookie batch, which the caller has just handed
// to the jar, into the records. The caller holds j.mu. It reports whether
// anything changed, so an unchanged response does not rewrite the file.
func (j *browserCookieJar) trackLocked(u *url.URL, cookies []*http.Cookie) bool {
	if u.Scheme != "http" && u.Scheme != "https" {
		// The jar ignores every other scheme, so there is nothing to mirror.
		return false
	}
	host := browserCookieRecordHost(u.Host)
	if host == "" {
		return false
	}
	now := time.Now()
	changed := false
	for _, c := range cookies {
		if c == nil || c.Name == "" {
			continue
		}
		rec := browserCookieRecord{
			Host:     host,
			Domain:   c.Domain,
			Path:     browserCookiePath(c.Path, u.Path),
			Name:     c.Name,
			Value:    c.Value,
			Secure:   c.Secure,
			HTTPOnly: c.HttpOnly,
		}
		key, ok := browserCookieKeyOK(rec)
		if !ok {
			// A Domain the jar rejects outright ("." or "..x", non-ASCII):
			// it stored nothing, and the record must not take the key of the
			// host-only cookie it would otherwise collide with.
			continue
		}
		// A delete is a Set-Cookie with an expiry in the past (MaxAge < 0 is
		// the same statement). The jar drops it; the records have to as well,
		// or the next save would put the cookie back. Max-Age wins over
		// Expires exactly as in the jar (cookiejar's newEntry): a cookie with
		// a positive Max-Age is kept whatever its Expires says, so its record
		// (and the HttpOnly note on it) must be kept too, even when this
		// device's clock already thinks the Expires date has passed.
		if c.MaxAge < 0 || (c.MaxAge == 0 && !c.Expires.IsZero() && !c.Expires.After(now)) {
			if _, ok := j.recs[key]; ok {
				delete(j.recs, key)
				changed = true
			}
			continue
		}
		// The record's expiry is the jar's: a Max-Age counts from receipt and
		// wins over Expires, which counts only when Max-Age is absent. Without
		// it a persistent login would be saved, and restored, as a session
		// cookie that never expires on the device.
		if when := browserCookieExpiresAt(c, now); !when.IsZero() {
			rec.Expires = when.UTC().Format(time.RFC3339)
		}
		// Only what the jar took is mirrored: a cookie it refused (a Domain
		// that does not cover the host, a public suffix) must not leave a
		// record whose flags would then speak for another cookie.
		if !j.holdsLocked(rec) {
			continue
		}
		// A cookie that is set again keeps its original first-seen stamp: the
		// retention policy asks how long the session has existed, and a site
		// that refreshes a token every visit must not look new forever.
		old, exists := j.recs[key]
		if exists {
			rec.FirstSeen = old.FirstSeen
		}
		if rec.FirstSeen == "" {
			rec.FirstSeen = now.UTC().Format(time.RFC3339)
		}
		if exists && (old == rec || browserCookieOnlyRefreshed(old, rec, now)) {
			continue
		}
		j.recs[key] = rec
		changed = true
	}
	return changed
}

// snapshotLocked returns the records the jar still holds, sorted and capped.
// Asking the jar keeps the file honest: a cookie that expired, was deleted, or
// was refused for its domain never reaches the card. The caller holds j.mu.
func (j *browserCookieJar) snapshotLocked() []browserCookieRecord {
	out := make([]browserCookieRecord, 0, len(j.recs))
	for key, rec := range j.recs {
		if !j.holdsLocked(rec) {
			delete(j.recs, key)
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(a, b int) bool { return browserCookieKey(out[a]) < browserCookieKey(out[b]) })
	if len(out) > browserCookieMaxRecords {
		logDebug("browser", "%d cookies stored, keeping the first %d", len(out), browserCookieMaxRecords)
		out = out[:browserCookieMaxRecords]
	}
	return out
}

// count reports how many cookies are held, for the Forget chip's prompt.
func (j *browserCookieJar) count() int {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.recs)
}

// browserCookieSite is one host the jar holds cookies for, how many of them,
// and when that site's own session began: the oldest first-seen stamp among
// its cookies, which is the age the picker puts on the row so one site's
// login can be judged before it is forgotten. Since is zero when none of the
// host's cookies carry a stamp (a file written before stamps existed and not
// yet re-loaded): unknown, rather than old.
type browserCookieSite struct {
	Domain string
	Count  int
	Since  time.Time
}

// sites reports the hosts the jar holds cookies for: the busiest first, ties
// broken by name, so a line built from this list does not reshuffle between
// frames.
func (j *browserCookieJar) sites() []browserCookieSite {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	counts := map[string]int{}
	// The oldest stamp per host is that site's own session start. It is
	// gathered in the same pass as the counts, so listing sites still costs
	// one lock and one walk of the records however many fields the caller
	// ends up reading.
	seen := map[string]time.Time{}
	for _, rec := range j.snapshotLocked() {
		host := browserCookieSiteName(rec)
		if host == "" {
			continue
		}
		counts[host]++
		if stamp, ok := cookieFirstSeen(rec); ok {
			if prev, has := seen[host]; !has || stamp.Before(prev) {
				seen[host] = stamp
			}
		}
	}
	out := make([]browserCookieSite, 0, len(counts))
	for host, n := range counts {
		out = append(out, browserCookieSite{Domain: host, Count: n, Since: seen[host]})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Count != out[b].Count {
			return out[a].Count > out[b].Count
		}
		return out[a].Domain < out[b].Domain
	})
	return out
}

// browserCookieSiteName is the host a record belongs to on screen: the domain
// the site scoped the cookie to when it named one (a cookie for .youtube.com
// belongs to youtube.com, not to whichever page happened to set it), otherwise
// the host that set it. The www. prefix is dropped because nobody thinks of it
// as part of the site's name.
func browserCookieSiteName(rec browserCookieRecord) string {
	host := strings.TrimSpace(rec.Domain)
	if host == "" {
		host = strings.TrimSpace(rec.Host)
	}
	return browserCookieSiteKey(host)
}

// browserCookieSiteKey normalises a host into the name the jar files cookies
// under. One function for both directions: the screen name a record is grouped
// by and the name a press names must be the same string, or "forget
// youtube.com" would quietly match nothing.
func browserCookieSiteKey(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, ".")
	host = strings.TrimPrefix(host, "www.")
	// A record keeps a port-less IPv6 literal in brackets ("[::1]") and one
	// that had a port without ("::1"); on screen both are ::1. Only a single
	// colon is a port: "fe80::1" is an address, not host fe80.
	switch {
	case strings.HasPrefix(host, "["):
		if end := strings.IndexByte(host, ']'); end > 0 {
			host = host[1:end]
		}
	case strings.Count(host, ":") == 1:
		host = host[:strings.IndexByte(host, ':')]
	}
	return host
}

// forgetSite drops every cookie one host holds and keeps the rest of the jar,
// returning how many went. The jar has no "delete one record" call, so the
// remaining records are restored into a fresh jar: that is what rebuilds the
// jar's own index of what it may send, which deleting from the records alone
// would leave stale.
func (j *browserCookieJar) forgetSite(site string) int {
	if j == nil {
		return 0
	}
	site = browserCookieSiteKey(site)
	if site == "" {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	records := j.snapshotLocked()
	kept := make([]browserCookieRecord, 0, len(records))
	dropped := 0
	for _, rec := range records {
		if browserCookieSiteName(rec) == site {
			dropped++
			continue
		}
		kept = append(kept, rec)
	}
	if dropped == 0 {
		return 0
	}
	inner, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: browserPublicSuffixes{}})
	if err != nil {
		logWarn("browser", "cannot rebuild the jar: %v", err)
		return 0
	}
	j.jar = inner
	j.recs = map[string]browserCookieRecord{}
	j.restoreLocked(kept)
	return dropped
}

// browserForgetSiteCookies drops the cookies one host holds and writes what is
// left back to the card (or deletes the file when that was the last site), so a
// relaunch cannot bring the dropped site back. Unlike a full Forget the jar
// stays alive: the other sites' sessions are the point of keeping it.
func browserForgetSiteCookies(site string) (int, error) {
	browserJar.mu.Lock()
	jar := browserJar.jar
	browserJar.mu.Unlock()
	if jar == nil {
		return 0, nil
	}
	dropped := jar.forgetSite(site)
	if dropped == 0 {
		return 0, nil
	}
	// The other sites' session stays, but this site's cookies must not survive
	// in the backup the save leaves behind.
	jar.saveDropBackup()
	return dropped, nil
}

// browserCookieSites is the hosts the browser is holding cookies for, read
// through the jar so a session saved on the card before the first page load is
// named too.
func browserCookieSites() []browserCookieSite {
	jar, ok := browserCookies().(*browserCookieJar)
	if !ok {
		return nil
	}
	return jar.sites()
}

// holdsLocked asks the jar whether it would still send this record's cookie for
// the URL it came from. A Secure cookie only matches over https, so the URL is
// rebuilt with the same scheme the record was saved under. The value has to
// match as well as the name: a host-only and a domain cookie of one name can
// both reach that URL, and a record whose cookie was replaced under another
// spelling of the host (bücher.example and xn--bcher-kva.example are one name
// to the jar) is stale, not held.
func (j *browserCookieJar) holdsLocked(rec browserCookieRecord) bool {
	u := browserCookieURL(rec)
	if u == nil {
		return false
	}
	for _, c := range j.jar.Cookies(u) {
		if c != nil && c.Name == rec.Name && c.Value == rec.Value {
			return true
		}
	}
	return false
}

// restore replays the file into a fresh jar and returns how many cookies the
// jar actually accepted. Anything it refuses (a cookie whose domain no longer
// matches, a public-suffix cookie, one that expired while the app was closed)
// is dropped instead of being carried forward forever.
func (j *browserCookieJar) restore(records []browserCookieRecord) int {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.restoreLocked(records)
}

// restoreLocked is the body of restore for a caller that already holds j.mu.
func (j *browserCookieJar) restoreLocked(records []browserCookieRecord) int {
	restored := 0
	for _, rec := range records {
		if strings.TrimSpace(rec.Name) == "" {
			continue
		}
		key, ok := browserCookieKeyOK(rec)
		if !ok {
			continue
		}
		u := browserCookieURL(rec)
		if u == nil {
			continue
		}
		c := &http.Cookie{
			Name:     rec.Name,
			Value:    rec.Value,
			Path:     u.Path,
			Secure:   rec.Secure,
			HttpOnly: rec.HTTPOnly,
		}
		if rec.Domain != "" {
			c.Domain = rec.Domain
		}
		if rec.Expires != "" {
			if when, err := time.Parse(time.RFC3339, rec.Expires); err == nil {
				c.Expires = when
			}
		}
		j.jar.SetCookies(u, []*http.Cookie{c})
		if !j.holdsLocked(rec) {
			continue
		}
		j.recs[key] = rec
		restored++
	}
	return restored
}

// browserCookieSaveMu serialises every write and delete of the file. Two
// responses landing together would otherwise fight over the same temporary
// path, and a Forget could interleave with a save and lose the delete.
var browserCookieSaveMu sync.Mutex

// save writes the records through the durable path — temp file, fsync, rename,
// and the previous session kept as .bak — so a power loss mid-write cannot
// leave half a JSON document behind. The forgotten flag is checked under the
// same mutex that Forget takes, so a jar retired mid-response writes nothing.
func (j *browserCookieJar) save() {
	if j == nil || strings.TrimSpace(j.path) == "" {
		return
	}
	browserCookieSaveMu.Lock()
	defer browserCookieSaveMu.Unlock()
	j.mu.Lock()
	if j.forgotten {
		j.mu.Unlock()
		return
	}
	records := j.snapshotLocked()
	j.mu.Unlock()
	if len(records) == 0 {
		// Nothing to remember: leaving an empty file behind would only be a
		// second place to look after a Forget.
		if err := removeBrowserCookieFile(j.path); err != nil {
			logWarn("browser", "cannot remove %s: %v", j.path, err)
		}
		return
	}
	writeBrowserCookies(j.path, records)
}

// saveDropBackup writes the jar and then removes the previous version. Use it
// whenever the new list is shorter because something was deliberately dropped:
// the .bak would otherwise restore exactly the cookies the user — or the
// retention policy — just got rid of.
func (j *browserCookieJar) saveDropBackup() {
	if j == nil {
		return
	}
	j.save()
	removeBrowserCookieBackup(j.path)
}

// removeBrowserCookieBackup deletes only the .bak beside a saved session. A
// path with no backup is not an error: the first save never writes one.
func removeBrowserCookieBackup(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	browserCookieSaveMu.Lock()
	defer browserCookieSaveMu.Unlock()
	if err := os.Remove(path + ".bak"); err != nil && !os.IsNotExist(err) {
		logWarn("browser", "cannot remove %s: %v", path+".bak", err)
	}
}

// removeBrowserCookieFile deletes the saved session and the backup the durable
// save keeps beside it. A Forget that left the .bak behind would leave the
// session sitting on the card — the exact opposite of what the chip says it
// did — and the next damaged read would restore it. The caller holds
// browserCookieSaveMu.
func removeBrowserCookieFile(path string) error {
	var first error
	for _, p := range []string{path, path + ".bak"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	return first
}

// writeBrowserCookies writes the document. The caller holds
// browserCookieSaveMu.
func writeBrowserCookies(path string, records []browserCookieRecord) {
	data, err := json.MarshalIndent(browserCookieFile{
		SchemaVersion: browserCookieSchemaVersion,
		SavedAt:       time.Now().UTC().Format(time.RFC3339),
		Cookies:       records,
	}, "", "  ")
	if err != nil {
		logWarn("browser", "cannot encode cookies: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logWarn("browser", "cannot create %s: %v", filepath.Dir(path), err)
		return
	}
	// Same crash-safe path as the config: temp file, fsync, rename, and the
	// previous session kept as .bak. A truncated jar used to read as "signed
	// out of every site" with no way back.
	if err := AtomicWrite(path, append(data, '\n'), browserCookieFileMode); err != nil {
		logWarn("browser", "cannot write %s: %v", path, err)
	}
}

// loadBrowserCookies reads the saved jar. A missing file is the normal first
// run; a broken one falls back to the .bak the durable save leaves beside it, so
// a bad edit or a pulled card costs the newest cookie rather than the session.
// migrated reports that records written under the old host-keyed names were
// merged, so the caller writes the file back once.
func loadBrowserCookies(path string) (records []browserCookieRecord, migrated bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}
	var file browserCookieFile
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !os.IsNotExist(err) {
			logWarn("browser", "ignoring %s: %v", path, err)
		}
		return nil, false
	}
	if file.SchemaVersion > browserCookieSchemaVersion {
		logDebug("browser", "%s is from a newer build (schema %d)", path, file.SchemaVersion)
		return nil, false
	}
	records, migrated = migrateBrowserCookies(file.Cookies)
	if migrated {
		logInfo("browser", "merged %d saved cookie record(s) the jar holds as one", len(file.Cookies)-len(records))
	}
	return records, migrated
}

// migrateBrowserCookies re-keys a file written while records were named by the
// host that set them (browserCookieKey's old form): records the jar holds as
// one cookie — a domain cookie set from two of its hosts, or a host spelled
// with and without its trailing dot — become one. Which of them the jar held
// last is not on the card, so the one that lives longer wins (the later in the
// file on a tie); it keeps HttpOnly if either had it, because hiding a cookie
// from scripts is the safe way to be wrong, and the older first-seen stamp, so
// the retention clock does not restart. The format itself did not change: a
// file with nothing to merge comes back as it was and reports false, which
// makes this safe to run on every load. Records restore would refuse anyway
// (a Domain the jar rejects) are passed through for it to drop.
func migrateBrowserCookies(records []browserCookieRecord) ([]browserCookieRecord, bool) {
	at := make(map[string]int, len(records))
	out := make([]browserCookieRecord, 0, len(records))
	merged := false
	for _, rec := range records {
		key, ok := browserCookieKeyOK(rec)
		if !ok {
			out = append(out, rec)
			continue
		}
		i, dup := at[key]
		if !dup {
			at[key] = len(out)
			out = append(out, rec)
			continue
		}
		merged = true
		prev := out[i]
		keep := rec
		if browserCookieExpiry(prev).After(browserCookieExpiry(rec)) {
			keep = prev
		}
		keep.HTTPOnly = prev.HTTPOnly || rec.HTTPOnly
		keep.FirstSeen = rec.FirstSeen
		if a, ok := cookieFirstSeen(prev); ok {
			if b, ok := cookieFirstSeen(rec); !ok || a.Before(b) {
				keep.FirstSeen = prev.FirstSeen
			}
		}
		out[i] = keep
	}
	return out, merged
}

// browserCookieMaxAgeCap bounds the lifetime a Max-Age can give a record (the
// 400 days RFC 6265bis caps cookies at). It also keeps a huge Max-Age from
// overflowing the Duration or the four-digit year RFC3339 parses back.
const browserCookieMaxAgeCap = 400 * 24 * time.Hour

// browserCookieExpiresAt is when a cookie received at now expires, worked out
// as cookiejar's newEntry does: a positive Max-Age counts from now and wins
// over Expires, which counts only when Max-Age is absent. Zero means a session
// cookie (or one the caller deletes: Max-Age < 0). Pure.
func browserCookieExpiresAt(c *http.Cookie, now time.Time) time.Time {
	switch {
	case c.MaxAge > 0:
		age := browserCookieMaxAgeCap
		if int64(c.MaxAge) < int64(browserCookieMaxAgeCap/time.Second) {
			age = time.Duration(c.MaxAge) * time.Second
		}
		return now.Add(age)
	case c.MaxAge == 0 && !c.Expires.IsZero():
		return c.Expires
	}
	return time.Time{}
}

// browserCookieExpirySlack is how far a cookie's expiry may move before its
// saved record is rewritten.
const browserCookieExpirySlack = time.Hour

// browserCookieOnlyRefreshed reports whether rec differs from the saved old
// only by an expiry that moved by an hour or less — and by no more than half
// of rec's remaining life at now. A Max-Age cookie re-sent with every
// response moves its expiry by seconds each time; keeping the saved stamp
// stops every page load rewriting the file on the card. The jar in memory
// keeps the exact expiry; the file is at most an hour off, and a short-lived
// cookie (a 10-minute Max-Age) is saved at most half its life stale, so the
// saved stamp is never already in the past when the file is read back. A new
// value, new flags or a session cookie turning persistent still save. Pure.
func browserCookieOnlyRefreshed(old, rec browserCookieRecord, now time.Time) bool {
	if old.Expires == "" || rec.Expires == "" {
		return false
	}
	same := rec
	same.Expires = old.Expires
	if same != old {
		return false
	}
	before, after := browserCookieExpiry(old), browserCookieExpiry(rec)
	if before.IsZero() || after.IsZero() {
		return false
	}
	slack := browserCookieExpirySlack
	if half := after.Sub(now) / 2; half < slack {
		slack = half // a 10-minute cookie is saved at most 5 minutes stale, never expired
	}
	moved := after.Sub(before)
	return moved >= -slack && moved <= slack
}

// browserCookieExpiry is a record's expiry; zero for a session cookie or one
// whose stamp does not parse.
func browserCookieExpiry(rec browserCookieRecord) time.Time {
	when, err := time.Parse(time.RFC3339, rec.Expires)
	if err != nil {
		return time.Time{}
	}
	return when
}

var (
	// browserJar is the process-lifetime jar. The zero value is usable: the
	// first request builds it, and a build failure (no public suffix list
	// available) leaves cookies off rather than failing the page.
	browserJar = struct {
		mu   sync.Mutex
		jar  *browserCookieJar
		dead bool
	}{}
	// browserCookiesPath is an indirection so tests never read or write the
	// device's real data directory.
	browserCookiesPath = defaultBrowserCookiesPath
)

func defaultBrowserCookiesPath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "browser-cookies.json")
}

// browserCookies returns the shared jar, building it and restoring the saved
// session on first use.
func browserCookies() http.CookieJar {
	browserJar.mu.Lock()
	defer browserJar.mu.Unlock()
	if browserJar.jar != nil {
		return browserJar.jar
	}
	if browserJar.dead {
		return nil
	}
	jar := newBrowserCookieJar(browserCookiesPath())
	if jar == nil {
		browserJar.dead = true
		return nil
	}
	browserJar.jar = jar
	if records, migrated := loadBrowserCookies(jar.path); len(records) > 0 {
		// The retention policy is applied here, at the one moment the card's
		// session becomes the live one: a cookie that is older than the chosen
		// window is not restored, and a stamp added to a pre-policy file is
		// written back so its clock starts at this launch instead of every one.
		now := time.Now()
		stamped := stampBrowserCookies(records, now)
		kept, dropped := browserStaleCookieSplit(records, cookieRetentionDays(appConfig), now)
		if n := jar.restore(kept); n > 0 {
			logDebug("browser", "restored %d cookie(s) from %s", n, jar.path)
		}
		if dropped > 0 {
			logWarn("browser", "retention %s dropped %d saved cookie(s)", cookieRetentionLabel(cookieRetentionDays(appConfig)), dropped)
		}
		if stamped || migrated || dropped > 0 {
			if dropped > 0 {
				// The pruned list is the retention policy's output; the backup holds
				// the pre-prune session, and a damaged read must not undo the drop
				// the user asked for.
				jar.saveDropBackup()
			} else {
				jar.save()
			}
		}
	}
	return jar
}

// browserCookiesCount is the number of cookies the browser is holding. It goes
// through the jar rather than reading the field, so a browser opened for the
// first time reports the session saved on the card — "No cookies are stored"
// while a file sits there would be a lie the Forget chip then acts on.
func browserCookiesCount() int {
	jar, ok := browserCookies().(*browserCookieJar)
	if !ok {
		return 0
	}
	return jar.count()
}

// browserResetCookies drops every cookie in memory without touching the card.
// The browser tests start from a clean session with it; the user-facing forget
// is browserForgetCookies.
func browserResetCookies() {
	browserJar.mu.Lock()
	old := browserJar.jar
	if jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: browserPublicSuffixes{}}); err == nil {
		browserJar.jar = &browserCookieJar{
			jar:  jar,
			recs: map[string]browserCookieRecord{},
			path: browserCookiesPath(),
		}
		browserJar.dead = false
	} else {
		browserJar.jar = nil
		browserJar.dead = true
	}
	browserJar.mu.Unlock()
	// Whatever was holding the old jar can no longer write through it.
	old.retire()
}

// browserForgetCookies drops every cookie, in memory and on the card, and
// reports how many were dropped. It is what the Browser's Forget chip runs on
// its second press.
func browserForgetCookies() (int, error) {
	browserJar.mu.Lock()
	old := browserJar.jar
	var dropped int
	var path string
	if old != nil {
		dropped = old.count()
		path = old.path
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: browserPublicSuffixes{}})
	if err != nil {
		browserJar.jar = nil
		browserJar.dead = true
		browserJar.mu.Unlock()
		return dropped, fmt.Errorf("cannot start a fresh session: %w", err)
	}
	browserJar.jar = &browserCookieJar{
		jar:  jar,
		recs: map[string]browserCookieRecord{},
		path: browserCookiesPath(),
	}
	browserJar.dead = false
	browserJar.mu.Unlock()

	// A page load that was already on the wire still holds the old jar. Retire
	// it before the delete, so the delete is the last word on the file.
	old.retire()
	// Pages fetched with the session are in the Back/Forward cache: Back after
	// a Forget showed the signed-in page (an inbox, an account screen) as if
	// the session were still there.
	browserClearPageCache()

	if strings.TrimSpace(path) == "" {
		return dropped, nil
	}
	browserCookieSaveMu.Lock()
	defer browserCookieSaveMu.Unlock()
	// The backup goes with the live file: a forgotten session must not survive
	// on the card just because the durable save kept a copy of it.
	if err := removeBrowserCookieFile(path); err != nil {
		return dropped, fmt.Errorf("cannot delete %s: %w", path, err)
	}
	return dropped, nil
}

// browserCookiePath picks the path a cookie is stored under: the Path attribute
// when it is valid, otherwise the RFC 6265 default (the request's directory).
func browserCookiePath(attribute, requestPath string) string {
	if strings.HasPrefix(attribute, "/") {
		return attribute
	}
	if !strings.HasPrefix(requestPath, "/") {
		return "/"
	}
	if i := strings.LastIndex(requestPath, "/"); i > 0 {
		return requestPath[:i]
	}
	return "/"
}

// browserCookieURL rebuilds the URL a record belongs to. The scheme follows
// Secure, because that is the only way the jar will hand the cookie back. The
// host goes in as the record spells it — brackets and all — so the jar files
// and finds the cookie under the very name it was set under.
func browserCookieURL(rec browserCookieRecord) *url.URL {
	host := strings.TrimSpace(rec.Host)
	if host == "" {
		return nil
	}
	scheme := "http"
	if rec.Secure {
		scheme = "https"
	}
	return &url.URL{Scheme: scheme, Host: host, Path: browserCookiePath(rec.Path, "/")}
}

// browserCookieKey names a stored cookie the way the jar does: its domain (or,
// for a host-only cookie, its host), path and name together. Two sites can
// share a cookie name, one site can keep the same name under two paths, and a
// host-only and a domain cookie of one name set from the same host are two
// cookies — keyed by the host that set them, they shared one record, and the
// second one's flags overwrote the first's HttpOnly.
func browserCookieKey(rec browserCookieRecord) string {
	key, _ := browserCookieKeyOK(rec)
	return key
}

// browserCookieKeyOK is browserCookieKey, and false for a Domain attribute the
// jar refuses before it looks any further: "." or "..x" (empty, or a second
// leading dot), a trailing dot and a non-ASCII domain. Such a record would
// otherwise be keyed like the host-only cookie of the same name. On an IP
// address the jar (domainAndType, isIP) takes a Domain only when it is the
// host spelled exactly — no leading dot dropped — and files the cookie
// host-only, so "Domain=.127.0.0.1" is refused here too: it must not reach,
// and rewrite the flags of, the host-only record of the same name.
func browserCookieKeyOK(rec browserCookieRecord) (string, bool) {
	scope := browserCookieJarHost(rec.Host)
	ok := true
	if rec.Domain != "" && (strings.ContainsAny(scope, ":%") || net.ParseIP(scope) != nil) {
		ok = rec.Domain == scope
	} else if rec.Domain != "" {
		domain := strings.TrimPrefix(rec.Domain, ".")
		ok = domain != "" && domain[0] != '.' && domain[len(domain)-1] != '.' && browserCookieASCII(domain)
		scope = browserCookieASCIILower(domain)
	}
	return scope + "\x00" + rec.Path + "\x00" + rec.Name, ok
}

// browserCookieRecordHost is a request's host as a record keeps it: the port
// cut off by the jar's own rule and ASCII letters lower-cased, nothing else.
// The jar keeps the brackets of a port-less IPv6 literal and drops them with a
// port — "[::1]" and "[::1]:8080" are two hosts to it — so this does too, and
// trailing dots stay, because the jar drops only one ("example.com.." is
// another name). Handed back to the jar in a URL it names the same host the
// request did. "" when the jar could not read the host either.
func browserCookieRecordHost(hostport string) string {
	host := strings.TrimSpace(hostport)
	if browserCookieHasPort(host) {
		h, _, err := net.SplitHostPort(host)
		if err != nil {
			return ""
		}
		host = h
	}
	return browserCookieASCIILower(host)
}

// browserCookieJarHost is the name the jar files a host-only cookie under (its
// canonicalHost): the record's host with one trailing dot dropped. The one
// step left out is punycode: the jar encodes a Unicode label, and since
// lower-casing the ASCII letters first gives the same encoding, the only names
// this keeps apart that the jar merges are a Unicode label and its xn-- form —
// two records for one cookie, which holdsLocked's value check retires.
func browserCookieJarHost(hostport string) string {
	return strings.TrimSuffix(browserCookieRecordHost(hostport), ".")
}

// browserCookieHasPort is the jar's hasPort: one colon is a port, more than one
// is an IPv6 literal unless it is bracketed and followed by one.
func browserCookieHasPort(host string) bool {
	switch strings.Count(host, ":") {
	case 0:
		return false
	case 1:
		return true
	}
	return host[0] == '[' && strings.Contains(host, "]:")
}

// browserCookieASCII reports whether s is plain ASCII.
func browserCookieASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// browserCookieASCIILower lower-cases the ASCII letters of s and leaves every
// other byte alone, as the jar does to a name it has already encoded.
func browserCookieASCIILower(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			b := []byte(s)
			for k := i; k < len(b); k++ {
				if b[k] >= 'A' && b[k] <= 'Z' {
					b[k] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// browserHTTPClient builds the client every browser request goes through, so a
// cookie set by one request is visible to the next — including across a
// redirect, where the client stores each hop's Set-Cookie before following it.
//
// The caller keeps its own timeout and redirect policy; the jar and the
// transport are shared. A transport per client meant every stylesheet and
// every image paid its own DNS lookup, TCP connect and TLS handshake — on
// handheld Wi-Fi that pushed a page's pictures past the image pass's deadline,
// and they drew as grey placeholders. One pooled transport reuses the
// connection (one HTTP/2 connection per host carries all of a CDN's images).
func browserHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Jar: browserCookies(), Transport: browserSharedTransport()}
}

var browserSharedTransportState struct {
	once sync.Once
	tr   *http.Transport
}

// browserSharedTransport is the one pooled transport behind browserHTTPClient.
func browserSharedTransport() *http.Transport {
	browserSharedTransportState.once.Do(func() {
		tr := browserTransport()
		// Image and stylesheet passes fetch up to six at a time from one host;
		// over HTTP/1.1 the default of two idle connections per host closed
		// the rest after every request.
		tr.MaxIdleConnsPerHost = 8
		// A pooled connection can die silently with the radio (sleep, a
		// Wi-Fi or VPN change). Idle ones are dropped sooner than the default
		// 90 s, an HTTP/2 connection that stops answering pings is closed
		// rather than holding every request to that host until its timeout,
		// and netWakeNoted empties the pool on wake.
		tr.IdleConnTimeout = 30 * time.Second
		tr.HTTP2 = &http.HTTP2Config{SendPingTimeout: 15 * time.Second, PingTimeout: 10 * time.Second}
		browserSharedTransportState.tr = tr
	})
	return browserSharedTransportState.tr
}
