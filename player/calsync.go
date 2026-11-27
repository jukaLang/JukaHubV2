package main

// calsync.go — calendar sync: the events behind the clock's calendar
// (calendarpopover.go), the reminders (calsync_remind.go) and whatever else
// asks what is next (Home, the assistant).
//
// Sources, all from mailcal.json (mailcal_store.go) plus a folder:
//
//   - a mail account's CalDAV address, signed in with the account's own
//     user name and app password (iCloud, Fastmail, Yahoo …);
//   - a calendar subscription: an .ics / webcal:// link (Google's secret
//     iCal address, Outlook's published calendar) or a CalDAV server with
//     its own login;
//   - every *.ics file in mailcalDataDir()/calendars/ (what Files' "Add to
//     calendar" copies there, or what a person drops in over USB).
//
// A background loop syncs every poll() minutes while online and when asked
// (calSyncNow). Each source's events — expanded over a window from a month
// back to a year ahead — are cached in mailcalDataDir()/events-<source>.json,
// so the calendar still shows them offline and right after a start, before
// any network. A source that fails keeps its last good events and writes a
// sentence a person can act on into the store (CalError / Error), which the
// Settings page shows.
//
// Secrets: the subscription link of a Google calendar is as good as a
// password, so a link is never logged — only its host — and transport errors
// are stripped of the URL they carry. Event text is never logged at all.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// calEvent is one event instance as the calendar shows it.
type calEvent struct {
	UID, Title, Location, Notes, Source string // Source = calendar/account name
	Color                               string // "#rrggbb" or ""
	Start, End                          time.Time
	AllDay                              bool
	// CalDAV write-back (MAIL-2): the resource URL and its ETag.
	URL        string `json:"url,omitempty"`
	ETag       string `json:"etag,omitempty"`
	SourceKind string `json:"source_kind,omitempty"`
}

const (
	// calSyncBack and calSyncAhead are the sync window around now.
	calSyncBack  = 31 * 24 * time.Hour
	calSyncAhead = 366 * 24 * time.Hour
	// calSyncSourceWait bounds one source's sync.
	calSyncSourceWait = 90 * time.Second
	// calSyncMaxFiles bounds the loose .ics files read.
	calSyncMaxFiles = 32
	// calSyncETagFresh is how long a cached expansion may answer a 304: the
	// window moves with the days, so a week-old expansion is rebuilt.
	calSyncETagFresh = 7 * 24 * time.Hour
)

// calSource is one place events come from.
type calSource struct {
	key     string // cache key: "a-<account>", "c-<calendar>", "f-<file>"
	storeID string // account or calendar ID for the error ("" for a file)
	account bool
	kind    string // mailcalCalICS, mailcalCalCalDAV, "graph", or "file"
	name    string
	color   string
	url     string
	user    string
	pass    string
	path    string
	help    string // where to make an app password (account presets)
	// Microsoft Graph (MAIL-2)
	isMS      bool
	msAccount mailcalAccount
}

// calCacheFile is events-<key>.json.
type calCacheFile struct {
	V       int        `json:"v"`
	Name    string     `json:"name,omitempty"`
	ETag    string     `json:"etag,omitempty"`
	Fetched time.Time  `json:"fetched"`
	Events  []calEvent `json:"events"`
}

var calSync struct {
	start   sync.Once
	started atomic.Bool
	busy    atomic.Bool
	kick    chan string
	// rev counts index changes, so a screen can tell when to redraw.
	rev atomic.Uint64
	// hasSources is set by each run: something to read was configured.
	hasSources atomic.Bool

	mu       sync.Mutex
	bySource map[string][]calEvent // guarded by mu
	etags    map[string]string     // guarded by mu
	fetched  map[string]time.Time  // guarded by mu
	index    []calEvent            // merged, sorted by Start; guarded by mu
	failed   bool                  // the last run had a network failure; guarded by mu
	lastRun  time.Time             // guarded by mu
}

// calSyncStart starts the background sync once (startup).
func calSyncStart() {
	calSync.start.Do(func() {
		calSync.kick = make(chan string, 1)
		calSync.started.Store(true)
		go calSyncLoop()
	})
}

// calSyncNow asks for a sync soon. It never blocks.
func calSyncNow(reason string) {
	calSyncStart()
	select {
	case calSync.kick <- reason:
	default:
	}
}

// calSyncLoop is the background loop: caches first, a sync shortly after
// start, then every poll() minutes while online, at once when asked, and on
// getting back online after a run that could not reach a server.
func calSyncLoop() {
	calSyncLoadCaches()
	first := time.NewTimer(20 * time.Second)
	defer first.Stop()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	var last time.Time
	wasOnline := IsOnline()
	for {
		reason := ""
		select {
		case reason = <-calSync.kick:
		case <-first.C:
			reason = "start"
		case <-tick.C:
			online := IsOnline()
			calSync.mu.Lock()
			retry := online && !wasOnline && calSync.failed
			calSync.mu.Unlock()
			wasOnline = online
			poll := time.Duration(mailcalSettingsGet().poll()) * time.Minute
			if !retry && (!online || (!last.IsZero() && time.Since(last) < poll)) {
				continue
			}
			reason = "poll"
		}
		calSyncRun(reason)
		last = time.Now()
	}
}

// calSyncWindow is the span synced at now.
func calSyncWindow(now time.Time) (time.Time, time.Time) {
	y, m, d := now.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	return day.Add(-calSyncBack), day.Add(calSyncAhead)
}

// calSyncSources lists what to read from the store and the folder.
func calSyncSources(s mailcalSettings) []calSource {
	var out []calSource
	for _, a := range s.Accounts {
		if strings.TrimSpace(a.CalDAVURL) == "" || a.CalOff || a.OAuth != "" || a.Password == "" {
			continue
		}
		src := calSource{
			key: "a-" + calSyncSafeName(a.ID), storeID: a.ID, account: true,
			kind: mailcalCalCalDAV, name: a.label(),
			url: strings.TrimSpace(a.CalDAVURL), user: a.user(),
			pass: mailcalOpen(a.Password, mailcalKeyPassword),
		}
		if p, ok := mailcalPresetByKey(a.Provider); ok {
			src.help = p.PasswordHelp
		}
		out = append(out, src)
	}
	// Microsoft accounts get a writable default calendar through Graph.
	for _, a := range s.Accounts {
		if a.OAuth != mailcalOAuthMicrosoft || a.CalOff {
			continue
		}
		out = append(out, calSource{
			key: "g-" + calSyncSafeName(a.ID), storeID: a.ID, account: true,
			kind: mailcalCalGraph, name: a.label() + " (Outlook)",
			isMS: true, msAccount: a,
		})
	}
	for _, c := range s.Calendars {
		if c.Off || (c.Kind != mailcalCalICS && c.Kind != mailcalCalCalDAV) {
			continue
		}
		out = append(out, calSource{
			key: "c-" + calSyncSafeName(c.ID), storeID: c.ID,
			kind: c.Kind, name: strings.TrimSpace(c.Name), color: caldavColor(c.Color),
			url:  mailcalOpen(c.URL, mailcalKeyCalURL),
			user: strings.TrimSpace(c.Username),
			pass: mailcalOpen(c.Password, mailcalKeyCalPassword),
		})
	}
	for _, path := range calSyncLocalFiles() {
		base := filepath.Base(path)
		h := fnv.New32a()
		_, _ = h.Write([]byte(base))
		out = append(out, calSource{
			key:  fmt.Sprintf("f-%08x", h.Sum32()),
			kind: "file", name: strings.TrimSuffix(base, filepath.Ext(base)), path: path,
		})
	}
	return out
}

// calSyncFolder is where loose .ics files live ("" without a data folder).
func calSyncFolder() string {
	dir := mailcalDataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "calendars")
}

// calSyncLocalFiles lists the folder's .ics files, sorted, bounded.
func calSyncLocalFiles() []string {
	dir := calSyncFolder()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".ics") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
		if len(out) >= calSyncMaxFiles {
			break
		}
	}
	sort.Strings(out)
	return out
}

// calSyncSafeName keeps an ID usable in a file name.
func calSyncSafeName(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
		if b.Len() >= 40 {
			break
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

// calSyncCachePath is a source's cache file ("" without a data folder).
func calSyncCachePath(key string) string {
	dir := mailcalDataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "events-"+key+".json")
}

// calSyncWriteFile writes a small file through a temporary one, so a power
// cut leaves the old cache or the new one, never half of one.
func calSyncWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// calSyncReadCache reads one source's cache.
func calSyncReadCache(key string) (calCacheFile, bool) {
	var c calCacheFile
	p := calSyncCachePath(key)
	if p == "" {
		return c, false
	}
	f, err := os.Open(p)
	if err != nil {
		return c, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 32<<20))
	if err != nil || json.Unmarshal(data, &c) != nil {
		return calCacheFile{}, false
	}
	for i := range c.Events {
		c.Events[i] = calSyncLocalize(c.Events[i])
	}
	return c, true
}

// calSyncLocalize puts a cached event back on the local clock. An all-day
// event is a date, not an instant: it is rebuilt as local midnight of its
// own date, so a change of time zone does not move birthdays.
func calSyncLocalize(e calEvent) calEvent {
	if e.AllDay {
		y, m, d := e.Start.Date()
		days := icsDayNumber(e.End.Date()) - icsDayNumber(y, m, d)
		if days < 1 {
			days = 1
		}
		e.Start = time.Date(y, m, d, 0, 0, 0, 0, time.Local)
		e.End = e.Start.AddDate(0, 0, days)
		return e
	}
	e.Start, e.End = e.Start.Local(), e.End.Local()
	return e
}

// calSyncLoadCaches fills the index from the caches of the current sources.
func calSyncLoadCaches() {
	sources := calSyncSources(mailcalSettingsGet())
	by := map[string][]calEvent{}
	etags := map[string]string{}
	fetched := map[string]time.Time{}
	for _, src := range sources {
		if src.kind == "file" {
			continue // read fresh by the first run
		}
		if c, ok := calSyncReadCache(src.key); ok {
			by[src.key] = c.Events
			etags[src.key] = c.ETag
			fetched[src.key] = c.Fetched
		}
	}
	calSync.mu.Lock()
	calSync.bySource, calSync.etags, calSync.fetched = by, etags, fetched
	calSyncRebuildLocked()
	calSync.mu.Unlock()
}

// calSyncRebuildLocked merges every source into the sorted index; the same
// event in two sources (a shared calendar subscribed twice) shows once.
func calSyncRebuildLocked() {
	var all []calEvent
	seen := map[string]bool{}
	keys := make([]string, 0, len(calSync.bySource))
	for k := range calSync.bySource {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, e := range calSync.bySource[k] {
			id := e.UID + "|" + e.Start.UTC().Format(time.RFC3339)
			if e.UID != "" && seen[id] {
				continue
			}
			seen[id] = true
			all = append(all, e)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].Start.Equal(all[j].Start) {
			return all[i].Start.Before(all[j].Start)
		}
		// All-day first on a day, then the shorter event.
		if all[i].AllDay != all[j].AllDay {
			return all[i].AllDay
		}
		return all[i].End.Before(all[j].End)
	})
	calSync.index = all
	calSync.rev.Add(1)
}

// calSyncRun syncs every source once. One run at a time.
func calSyncRun(reason string) {
	if !calSync.busy.CompareAndSwap(false, true) {
		return
	}
	defer calSync.busy.Store(false)
	defer func() {
		if r := recover(); r != nil {
			logWarn("calendar", "calendar sync stopped: %v", r)
		}
	}()
	s := mailcalSettingsGet()
	sources := calSyncSources(s)
	now := time.Now()
	from, to := calSyncWindow(now)
	online := IsOnline()

	calSync.mu.Lock()
	prev := calSync.bySource
	prevTags := calSync.etags
	prevFetched := calSync.fetched
	calSync.mu.Unlock()

	by := map[string][]calEvent{}
	etags := map[string]string{}
	fetched := map[string]time.Time{}
	errs := map[string]string{} // storeID (with a/c prefix) → sentence
	failedNet := false
	synced := 0
	for _, src := range sources {
		if src.kind == "file" {
			evs, _, err := calSyncReadFile(src.path, from, to)
			if err != nil {
				logInfo("calendar", "skipping %s: %v", filepath.Base(src.path), err)
				continue
			}
			by[src.key] = calSyncLabel(evs, src.name, "")
			continue
		}
		// Keep what the last good run had until this one does better.
		if evs, ok := prev[src.key]; ok {
			by[src.key], etags[src.key], fetched[src.key] = evs, prevTags[src.key], prevFetched[src.key]
		}
		if !online {
			// Retried as soon as the connection is back (calSyncLoop).
			failedNet = true
			continue
		}
		etag := ""
		if t := prevFetched[src.key]; !t.IsZero() && now.Sub(t) < calSyncETagFresh {
			etag = prevTags[src.key]
		}
		ctx, cancel := context.WithTimeout(context.Background(), calSyncSourceWait)
		res, err := calFetchSource(ctx, src, from, to, etag)
		cancel()
		errKey := "c-" + src.storeID
		if src.account {
			errKey = "a-" + src.storeID
		}
		if err != nil {
			if calSyncUnreachable(err) {
				failedNet = true
			}
			msg := err.Error()
			if src.help != "" && strings.Contains(msg, "app-specific password") {
				msg += " (" + src.help + ")"
			}
			errs[errKey] = msg
			logInfo("calendar", "%s calendar from %s did not sync: %s", src.kind, calSyncHost(src.url), msg)
			continue
		}
		errs[errKey] = ""
		synced++
		if res.notModified {
			continue
		}
		evs := calSyncLabel(res.events, src.name, src.color)
		by[src.key], etags[src.key], fetched[src.key] = evs, res.etag, now
		if p := calSyncCachePath(src.key); p != "" {
			if data, err := json.Marshal(calCacheFile{V: 1, Name: src.name, ETag: res.etag, Fetched: now, Events: evs}); err == nil {
				if err := calSyncWriteFile(p, data); err != nil {
					logWarn("calendar", "could not cache events: %v", err)
				}
			}
		}
	}

	calSync.mu.Lock()
	calSync.bySource, calSync.etags, calSync.fetched = by, etags, fetched
	calSync.failed = failedNet
	calSync.lastRun = now
	calSyncRebuildLocked()
	n := len(calSync.index)
	calSync.mu.Unlock()

	calSync.hasSources.Store(len(sources) > 0)
	calSyncDropStaleCaches(sources)
	calSyncWriteErrors(s, errs)
	logInfo("calendar", "calendar sync (%s): %d sources, %d synced, %d events", reason, len(sources), synced, n)
}

// calSyncLabel stamps a source's name and colour on events that have none,
// and puts them on the local clock: an expansion keeps each event's own zone
// (UTC, the TZID's), and the month view and the reminders read local dates.
func calSyncLabel(evs []calEvent, name, color string) []calEvent {
	for i := range evs {
		evs[i] = calSyncLocalize(evs[i])
		if evs[i].Source == "" {
			evs[i].Source = name
		}
		if evs[i].Color == "" {
			evs[i].Color = color
		}
	}
	return evs
}

// calSyncWriteErrors records each source's outcome in the store, only when
// it changed, so a quiet sync does not rewrite mailcal.json.
func calSyncWriteErrors(s mailcalSettings, errs map[string]string) {
	clip := func(m string) string {
		if len(m) > 300 {
			m = m[:300] + "…"
		}
		return m
	}
	changed := false
	for _, a := range s.Accounts {
		if m, ok := errs["a-"+a.ID]; ok && clip(m) != a.CalError {
			changed = true
		}
	}
	for _, c := range s.Calendars {
		if m, ok := errs["c-"+c.ID]; ok && clip(m) != c.Error {
			changed = true
		}
	}
	if !changed {
		return
	}
	err := mailcalSettingsUpdate(func(next *mailcalSettings) {
		for i := range next.Accounts {
			if m, ok := errs["a-"+next.Accounts[i].ID]; ok {
				next.Accounts[i].CalError = clip(m)
			}
		}
		for i := range next.Calendars {
			if m, ok := errs["c-"+next.Calendars[i].ID]; ok {
				next.Calendars[i].Error = clip(m)
			}
		}
	})
	if err != nil {
		logWarn("calendar", "could not save the calendar status: %v", err)
	}
}

// calSyncDropStaleCaches removes caches of sources that are gone.
func calSyncDropStaleCaches(sources []calSource) {
	dir := mailcalDataDir()
	if dir == "" {
		return
	}
	keep := map[string]bool{}
	for _, s := range sources {
		keep["events-"+s.key+".json"] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "events-") || !strings.HasSuffix(n, ".json") || keep[n] {
			continue
		}
		_ = os.Remove(filepath.Join(dir, n))
	}
}

// calForgetSource drops the cached events of a removed account or calendar.
func calForgetSource(id string) {
	safe := calSyncSafeName(id)
	keys := []string{"a-" + safe, "c-" + safe}
	for _, k := range keys {
		if p := calSyncCachePath(k); p != "" {
			_ = os.Remove(p)
			_ = os.Remove(p + ".tmp")
		}
	}
	calSync.mu.Lock()
	for _, k := range keys {
		delete(calSync.bySource, k)
		delete(calSync.etags, k)
		delete(calSync.fetched, k)
	}
	calSyncRebuildLocked()
	calSync.mu.Unlock()
}

// ── Reading the index ─────────────────────────────────────────────────────

// calEventsBetween lists the events that touch [from, to), sorted by start.
// Any goroutine.
func calEventsBetween(from, to time.Time) []calEvent {
	calSync.mu.Lock()
	defer calSync.mu.Unlock()
	var out []calEvent
	for _, e := range calSync.index {
		if !e.Start.Before(to) {
			break
		}
		if calEventOverlaps(e, from, to) {
			out = append(out, e)
		}
	}
	return out
}

// calNextEvent is what is next at now: the first timed event in progress or
// to come, unless an all-day event's day comes first (a birthday tomorrow
// before a meeting next week; a meeting this afternoon before today's
// birthday). Any goroutine.
func calNextEvent(now time.Time) (calEvent, bool) {
	calSync.mu.Lock()
	defer calSync.mu.Unlock()
	var timed, allDay calEvent
	var haveTimed, haveAllDay bool
	for _, e := range calSync.index {
		if haveTimed && haveAllDay {
			break
		}
		end := e.End
		if !end.After(e.Start) {
			end = e.Start.Add(time.Minute)
		}
		if !end.After(now) {
			continue
		}
		if e.AllDay && !haveAllDay {
			allDay, haveAllDay = e, true
		} else if !e.AllDay && !haveTimed {
			timed, haveTimed = e, true
		}
	}
	switch {
	case haveTimed && (!haveAllDay || timed.Start.Before(allDay.End)):
		return timed, true
	case haveAllDay:
		return allDay, true
	}
	return calEvent{}, false
}

// calSyncHasSources reports whether any calendar is set up (cheap enough
// for a frame: the folder's files are only known after a run).
func calSyncHasSources() bool {
	if calSync.hasSources.Load() {
		return true
	}
	s := mailcalSettingsGet()
	for _, a := range s.Accounts {
		if a.CalDAVURL != "" && !a.CalOff && a.OAuth == "" {
			return true
		}
	}
	for _, c := range s.Calendars {
		if !c.Off {
			return true
		}
	}
	return false
}

// calSyncKickIfStale asks for a sync when the last one is older than age
// (the popover opening).
func calSyncKickIfStale(age time.Duration) {
	calSync.mu.Lock()
	last := calSync.lastRun
	calSync.mu.Unlock()
	if calSync.busy.Load() || (!last.IsZero() && time.Since(last) < age) {
		return
	}
	if !calSyncHasSources() {
		return
	}
	calSyncNow("open")
}

// calSyncRev changes whenever the index does.
func calSyncRev() uint64 { return calSync.rev.Load() }

// calSyncBusy reports a sync in progress (the popover's "Syncing…").
func calSyncBusy() bool { return calSync.busy.Load() }

// ── Fetching ──────────────────────────────────────────────────────────────

var calHTTP struct {
	once   sync.Once
	links  *http.Client // .ics links: ordinary redirects
	caldav *http.Client // CalDAV: redirects by hand (calsync_caldav.go)
}

// calHTTPClients builds the two clients on one transport with the firmware
// CA fallback a handheld needs (bugrepRootCAs).
func calHTTPClients() (*http.Client, *http.Client) {
	calHTTP.once.Do(func() {
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 45 * time.Second,
			MaxIdleConns:          4,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       60 * time.Second,
		}
		if pool := bugrepRootCAs(); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		calHTTP.links = &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			// Go keeps the login on a redirect within the same domain, even
			// from https:// down to http://; a password never goes out in the
			// clear to the internet.
			if req.Header.Get("Authorization") != "" {
				if err := caldavCheckURL(req.URL); err != nil {
					return errors.New("the calendar link redirects to an unencrypted address; not sending the password there")
				}
			}
			return nil
		}}
		calHTTP.caldav = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	})
	return calHTTP.links, calHTTP.caldav
}

// calFetchResult is one source's answer.
type calFetchResult struct {
	events      []calEvent
	name        string
	etag        string
	notModified bool
}

// calFetchSource reads one network source. Errors are sentences a person can
// act on; network failures still wrap their cause (calSyncUnreachable).
func calFetchSource(ctx context.Context, src calSource, from, to time.Time, etag string) (res calFetchResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("the calendar could not be read")
			logWarn("calendar", "reading a calendar failed: %v", r)
		}
	}()
	switch src.kind {
	case mailcalCalICS:
		return calFetchICS(ctx, src.url, src.user, src.pass, etag, from, to)
	case mailcalCalCalDAV:
		return calFetchCalDAV(ctx, src.url, src.user, src.pass, from, to)
	case mailcalCalGraph:
		return calFetchGraph(ctx, src.msAccount, from, to)
	}
	return res, errors.New("unknown kind of calendar")
}

// calSyncLinkURL turns what a person pasted into the URL to fetch:
// webcal:// and webcals:// are https://.
func calSyncLinkURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	low := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(low, "webcals://"):
		raw = "https://" + raw[len("webcals://"):]
	case strings.HasPrefix(low, "webcal://"):
		raw = "https://" + raw[len("webcal://"):]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("the calendar link is not a web address")
	}
	if s := strings.ToLower(u.Scheme); s != "https" && s != "http" {
		return nil, errors.New("the calendar link must start with https:// or webcal://")
	}
	return u, nil
}

// calFetchICS downloads an .ics link (If-None-Match when the cache is
// fresh) and expands it.
func calFetchICS(ctx context.Context, raw, user, pass, etag string, from, to time.Time) (calFetchResult, error) {
	var res calFetchResult
	u, err := calSyncLinkURL(raw)
	if err != nil {
		return res, err
	}
	if pass != "" {
		if err := caldavCheckURL(u); err != nil {
			return res, err
		}
	}
	links, _ := calHTTPClients()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return res, errors.New("the calendar link is not a web address")
	}
	req.Header.Set("Accept", "text/calendar, */*;q=0.5")
	req.Header.Set("User-Agent", "JukaHub/"+versionString(nil))
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := links.Do(req)
	if err != nil {
		return res, calSyncReadable(err, u.Hostname(), false)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && etag != "" {
		res.notModified, res.etag = true, etag
		return res, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return res, calSyncReadable(&caldavStatusError{code: resp.StatusCode, host: u.Hostname()}, u.Hostname(), false)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, icsMaxBytes+1))
	if err != nil {
		return res, calSyncReadable(err, u.Hostname(), false)
	}
	if len(data) > icsMaxBytes {
		return res, errors.New("the calendar is larger than 5 MB")
	}
	cal, err := icsParse(data)
	if err != nil {
		if strings.Contains(strings.ToLower(string(data[:min(len(data), 512)])), "<html") {
			return res, errors.New("the link opens a web page, not a calendar — copy the iCal (.ics) address instead")
		}
		return res, err
	}
	res.events = cal.expand(from, to, icsMaxInstances)
	res.name = cal.Name
	res.etag = resp.Header.Get("ETag")
	return res, nil
}

// calCalDAVClient checks a CalDAV address and login and makes the client.
// caldavWriteHTTPClient is the seam tests use to inject a TLS client that trusts
// a loopback CA. Defaults to the normal CalDAV client.
var caldavWriteHTTPClient = func() *http.Client {
	_, c := calHTTPClients()
	return c
}

func calCalDAVClient(raw, user, pass string) (*caldavClient, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("the CalDAV address is not valid")
	}
	if err := caldavCheckURL(u); err != nil {
		return nil, err
	}
	if user == "" || pass == "" {
		return nil, errors.New("CalDAV needs a user name and an app password")
	}
	_, client := calHTTPClients()
	return &caldavClient{origin: u, user: user, pass: pass, http: client}, nil
}

// calDiscoverCalDAV signs in and names the calendars found.
func calDiscoverCalDAV(ctx context.Context, raw, user, pass string) (string, error) {
	c, err := calCalDAVClient(raw, user, pass)
	if err != nil {
		return "", err
	}
	cals, err := c.discover(ctx, c.origin.String())
	if err != nil {
		return "", calSyncReadable(err, c.origin.Hostname(), true)
	}
	name := cals[0].Name
	if name == "" {
		name = "Calendar"
	}
	if len(cals) > 1 {
		name = fmt.Sprintf("%s and %d more", name, len(cals)-1)
	}
	return name, nil
}

// calFetchCalDAV discovers a server's calendars and reads them all.
func calFetchCalDAV(ctx context.Context, raw, user, pass string, from, to time.Time) (calFetchResult, error) {
	var res calFetchResult
	c, err := calCalDAVClient(raw, user, pass)
	if err != nil {
		return res, err
	}
	u := c.origin
	cals, err := c.discover(ctx, u.String())
	if err != nil {
		return res, calSyncReadable(err, u.Hostname(), true)
	}
	var firstErr error
	for i, cal := range cals {
		evs, err := c.events(ctx, cal, from, to)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		name := cal.Name
		if name == "" {
			name = fmt.Sprintf("Calendar %d", i+1)
		}
		res.events = append(res.events, calSyncLabel(evs, name, cal.Color)...)
		if res.name == "" {
			res.name = name
		}
		if len(res.events) >= icsMaxInstances {
			break
		}
	}
	if res.name == "" && firstErr != nil {
		// Not one calendar answered.
		return res, calSyncReadable(firstErr, u.Hostname(), true)
	}
	if len(cals) > 1 {
		res.name = fmt.Sprintf("%s and %d more", res.name, len(cals)-1)
	}
	return res, nil
}

// calSyncReadFile reads a loose .ics file.
func calSyncReadFile(path string, from, to time.Time) (evs []calEvent, name string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("the calendar file could not be read")
			logWarn("calendar", "reading a calendar file failed: %v", r)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, icsMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > icsMaxBytes {
		return nil, "", errors.New("the calendar file is larger than 5 MB")
	}
	cal, err := icsParse(data)
	if err != nil {
		return nil, "", err
	}
	return cal.expand(from, to, icsMaxInstances), cal.Name, nil
}

// calSyncHost is the host of a link, for logs (never the link itself).
func calSyncHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = "x" + raw[i:]
	} else {
		raw = "x://" + raw
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "a server"
}

// calSyncUnreachable reports an error that means "could not ask" rather
// than "the server said no".
func calSyncUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// calSyncReadable turns a transport or server error into a sentence. The URL
// a *url.Error quotes (a secret link) is dropped; network causes stay
// wrapped, so the phone card can tell "unreachable" from "refused".
func calSyncReadable(err error, host string, caldav bool) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	var se *caldavStatusError
	if errors.As(err, &se) {
		switch {
		case se.code == 401:
			return errors.New("the server refused the user name or password — use an app-specific password if the account has two-step sign-in")
		case se.code == 403:
			return errors.New("the server refused access to this calendar (HTTP 403)")
		case se.code == 404 || se.code == 410:
			if caldav {
				return errors.New("no calendar server at that address (HTTP 404) — check the CalDAV address")
			}
			return errors.New("nothing is at that link any more (HTTP 404) — copy the calendar link again")
		case (se.code == 405 || se.code == 501) && caldav:
			return errors.New("that address is not a CalDAV server")
		case se.code >= 500:
			return fmt.Errorf("the calendar server has a problem (HTTP %d); JukaHub will try again", se.code)
		}
		return fmt.Errorf("the calendar server answered HTTP %d", se.code)
	}
	var unknownCA x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostErr x509.HostnameError
	switch {
	case errors.As(err, &unknownCA):
		return errors.New("the server's certificate is not trusted on this device")
	case errors.As(err, &invalid):
		return errors.New("the server's certificate is not valid — is the device's date right?")
	case errors.As(err, &hostErr):
		return errors.New("the server's certificate is for another name")
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("cannot find the server %s: %w", host, err)
	}
	if calSyncUnreachable(err) {
		return fmt.Errorf("cannot reach %s: %w", host, err)
	}
	return err
}

// calCheckSource checks a subscription or a CalDAV login before it is saved
// and names what it found. Any goroutine.
func calCheckSource(ctx context.Context, kind, rawURL, user, pass string) (name string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("the calendar could not be read")
			logWarn("calendar", "checking a calendar failed: %v", r)
		}
	}()
	from, to := calSyncWindow(time.Now())
	var res calFetchResult
	switch kind {
	case mailcalCalICS:
		res, err = calFetchICS(ctx, rawURL, user, pass, "", from, to)
	case mailcalCalCalDAV:
		// Signing in and finding the calendars is the check; their events
		// come with the first sync, so a big calendar does not run out the
		// phone card's wait.
		res.name, err = calDiscoverCalDAV(ctx, rawURL, user, pass)
	default:
		return "", errors.New("unknown kind of calendar")
	}
	if err != nil {
		return "", err
	}
	name = calSafeText(res.name, 60)
	if name == "" {
		name = calSyncHost(rawURL)
	}
	return name, nil
}

// ── Files ─────────────────────────────────────────────────────────────────

// calSyncImportICS copies an .ics file into the calendar folder after
// checking it reads, and reports how many events it holds.
func calSyncImportICS(path string) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			n, err = 0, errors.New("the calendar file could not be read")
			logWarn("calendar", "importing a calendar file failed: %v", r)
		}
	}()
	dir := calSyncFolder()
	if dir == "" {
		return 0, errors.New("the settings folder is unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	data, err := io.ReadAll(io.LimitReader(f, icsMaxBytes+1))
	f.Close()
	if err != nil {
		return 0, err
	}
	if len(data) > icsMaxBytes {
		return 0, errors.New("the calendar file is larger than 5 MB")
	}
	cal, err := icsParse(data)
	if err != nil {
		return 0, err
	}
	for _, ev := range cal.events {
		if ev.recurKey == "" && !ev.cancelled {
			n++
		}
	}
	if n == 0 {
		return 0, errors.New("the file has no events")
	}
	if len(calSyncLocalFiles()) >= calSyncMaxFiles {
		return 0, fmt.Errorf("the calendar already has %d files; remove one first", calSyncMaxFiles)
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r < 32 {
			return '_'
		}
		return r
	}, base)
	if base == "" || base == "." || base == ".." {
		base = "calendar"
	}
	dest := filepath.Join(dir, base+".ics")
	for i := 2; ; i++ {
		existing, rerr := os.ReadFile(dest)
		if rerr != nil {
			break // free name
		}
		if string(existing) == string(data) {
			calSyncNow("import")
			return n, nil // the same file again
		}
		if i > 50 {
			return 0, errors.New("too many calendar files with this name")
		}
		dest = filepath.Join(dir, fmt.Sprintf("%s-%d.ics", base, i))
	}
	if err := calSyncWriteFile(dest, data); err != nil {
		return 0, err
	}
	calSyncNow("import")
	return n, nil
}

// calFilesConfirm is the .ics file Files pressed A on once.
var calFilesConfirm struct {
	path string
	at   time.Time
}

// calFilesOpen is A on a file in Files: an .ics file offers itself to the
// calendar on the first press and is added on a second one (like a WireGuard
// file, vpn.go). Files already in the calendar folder open as text. It
// reports whether it took the press. Main thread.
func calFilesOpen(_ *Config, path string) bool {
	if !strings.EqualFold(filepath.Ext(path), ".ics") {
		return false
	}
	if dir := calSyncFolder(); dir != "" && strings.EqualFold(filepath.Clean(filepath.Dir(path)), filepath.Clean(dir)) {
		return false
	}
	if calFilesConfirm.path != path || time.Since(calFilesConfirm.at) > 6*time.Second {
		calFilesConfirm.path, calFilesConfirm.at = path, time.Now()
		ShowToast(hintSentence("This is a calendar file. Press %s again to add it to the calendar", "A", "Add"), ToastKindInfo)
		return true
	}
	calFilesConfirm.path = ""
	go func() {
		// The toasts go back through the main loop: ShowToast also files the
		// message in the notification centre, which is the SDL thread's.
		n, err := calSyncImportICS(path)
		if err != nil {
			msg := "Could not add the calendar: " + err.Error()
			tbxPost(func() { ShowToast(msg, ToastKindWarning) })
			return
		}
		word := "events"
		if n == 1 {
			word = "event"
		}
		msg := fmt.Sprintf("Added to the calendar: %d %s", n, word)
		tbxPost(func() { ShowToast(msg, ToastKindSuccess) })
	}()
	return true
}

// calSafeText makes outside text drawable: control and format characters
// go, runs of white space become one space, and it is cut to max runes.
func calSafeText(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		if max > 0 && n >= max {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// ── MAIL-2 write-back (CalDAV) ────────────────────────────────────────────

// calsyncCalDAVWritable reports whether a source accepts writes. Only CalDAV
// calendars do; .ics subscriptions and loose files are read-only.
func calsyncCalDAVWritable(src calSource) bool {
	return src.kind == mailcalCalCalDAV
}

// calsyncWritable reports whether a source accepts writes (CalDAV or Graph).
func calsyncWritable(src calSource) bool {
	return src.kind == mailcalCalCalDAV || src.kind == mailcalCalGraph
}

// calEventToICS serialises one event as a minimal VCALENDAR. Used to create or
// replace a resource on a CalDAV server.
func calEventToICS(ev calEvent) string {
	uid := strings.TrimSpace(ev.UID)
	if uid == "" {
		uid = fmt.Sprintf("jukahub-%d", time.Now().UnixNano())
	}
	fmtTime := func(t time.Time, allDay bool) string {
		if allDay {
			return t.UTC().Format("20060102")
		}
		return t.UTC().Format("20060102T150405Z")
	}
	lines := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//JukaHub//Calendar//EN",
		"BEGIN:VEVENT",
		"UID:" + uid,
		"SUMMARY:" + ev.Title,
	}
	if ev.AllDay {
		lines = append(lines, "DTSTART;VALUE=DATE:"+fmtTime(ev.Start, true))
		lines = append(lines, "DTEND;VALUE=DATE:"+fmtTime(ev.End, true))
	} else {
		lines = append(lines, "DTSTART:"+fmtTime(ev.Start, false))
		lines = append(lines, "DTEND:"+fmtTime(ev.End, false))
	}
	if loc := strings.TrimSpace(ev.Location); loc != "" {
		lines = append(lines, "LOCATION:"+loc)
	}
	if notes := strings.TrimSpace(ev.Notes); notes != "" {
		lines = append(lines, "DESCRIPTION:"+notes)
	}
	lines = append(lines, "END:VEVENT", "END:VCALENDAR")
	return strings.Join(lines, "\r\n") + "\r\n"
}

// calsyncPushEvent creates or updates one event on a writable CalDAV source.
// On success it returns the event with its resource URL and ETag filled in.
// A 412 conflict is returned as *caldavConflictError so the caller can re-sync.
func calsyncPushEvent(ctx context.Context, src calSource, ev calEvent) (calEvent, error) {
	if src.kind == mailcalCalGraph {
		return calsyncPushGraph(ctx, src, ev)
	}
	if !calsyncCalDAVWritable(src) {
		return ev, errors.New("this calendar is read-only")
	}
	if strings.TrimSpace(src.url) == "" || strings.TrimSpace(src.user) == "" {
		return ev, errors.New("this calendar has no CalDAV address")
	}
	c := &caldavClient{origin: caldavMustParseURL(src.url), user: src.user, pass: src.pass, http: caldavWriteHTTPClient()}
	ics := calEventToICS(ev)
	if ev.UID == "" {
		ev.UID = calEventUIDFromICS(ics)
	}
	href := strings.TrimSpace(ev.URL)
	if href == "" {
		base := strings.TrimRight(src.url, "/")
		href = base + "/" + ev.UID + ".ics"
	}
	newETag, err := c.putEvent(ctx, href, ics, ev.ETag)
	if err != nil {
		return ev, err
	}
	ev.URL = href
	ev.ETag = newETag
	return ev, nil
}

// calsyncDeleteEvent removes one event resource from a writable CalDAV source.
func calsyncDeleteEvent(ctx context.Context, src calSource, ev calEvent) error {
	if src.kind == mailcalCalGraph {
		return calsyncDeleteGraph(ctx, src, ev)
	}
	if !calsyncCalDAVWritable(src) {
		return errors.New("this calendar is read-only")
	}
	if strings.TrimSpace(ev.URL) == "" {
		return errors.New("this event has no CalDAV resource")
	}
	c := &caldavClient{origin: caldavMustParseURL(src.url), user: src.user, pass: src.pass, http: caldavWriteHTTPClient()}
	return c.deleteEvent(ctx, ev.URL, ev.ETag)
}

func calEventUIDFromICS(ics string) string {
	for _, line := range strings.Split(ics, "\n") {
		if strings.HasPrefix(line, "UID:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "UID:"))
		}
	}
	return ""
}

func caldavMustParseURL(raw string) *url.URL {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return &url.URL{}
	}
	return u
}
