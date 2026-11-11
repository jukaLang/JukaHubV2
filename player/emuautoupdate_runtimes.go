package main

// emuautoupdate_runtimes.go — the half of the daily Emulator updates pass that
// is not the libretro cores.
//
// Everything else JukaHub installs and can bring forward by itself is an
// emuRuntimeUpdater (emuautoupdate_registry.go): hakuX on the Smart Pro
// (emuautoupdate_hakux.go), the Windows emulators JukaHub installed on a PC
// (emuautoupdate_desktop.go), the Windows-app runtime and AndroidRT (registered
// by their own files). The pass in emulatorautoupdate.go runs them after the
// cores, at the same quiet moment and under the same setting:
//
//   - Install automatically: each one whose check finds a newer build installs
//     it through the install bar, one after another; a game that starts stops
//     the pass between them.
//   - Notify me: checks only. A new build is named once, in the pass's one
//     toast, and badged on the Emulators page.
//   - Off: nothing runs.
//
// What keeps that safe and cheap with nobody watching:
//
//   - Each updater keeps its own stamp (emu-autoupdate.json beside the patch
//     state), so it is asked once a day, and one that failed is asked again
//     without the others asking twice. A stamp the clock cannot vouch for (the
//     handheld has no RTC battery) reads as due, like the cores' stamp, and a
//     session guard on the monotonic clock keeps a clock stuck in 1970 from
//     asking at every poll. An updater that is nearly due rides along with a
//     pass that runs anyway, so several stamps do not drift into several passes
//     (and several install cards) a day.
//   - A failure is logged and tried again after an hour, then two, four… up to
//     a day. Only the first failed install in a row reaches the toast; a check
//     that could not reach its server is a log line, as the cores' is.
//   - GitHub answers 60 requests an hour per address to a client that does not
//     sign in, and nearly all of these ask GitHub. One pass asks about one
//     project once: emuPassTransport answers an install's second look at a
//     release from the check's answer, the Emulators page's own check is used
//     while it is fresh, and a 403 or 429 holds every GitHub lookup for an
//     hour. (A lookup asked before is asked conditionally, so an unchanged
//     release comes back as a bodiless 304 — that saves the connection, not the
//     hour's count: GitHub only spares a 304 to a client that signs in.) The
//     updaters a hold kept from running are not stamped, so they run when the
//     hour is over rather than tomorrow, and the oldest attempt goes first, so
//     one that keeps failing cannot spend the hour's requests for the others.
//   - An updater whose program is running (InUse: an Xbox game, an emulator
//     open on the PC) is skipped and runs at the next quiet moment.
//   - A build this pass installed that is no longer installed was put back by
//     the user — a rollback is a choice — so it is not installed again; the
//     next build is installed as usual. The same rule stops a loop if an
//     install ever reports success without taking effect.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// emuRuntimeStateName is the per-updater record, in the patch state
	// folder (data/patch on the handheld, the user data folder on a PC).
	emuRuntimeStateName = "emu-autoupdate.json"
	// emuRuntimeAlign is how early an updater may ride along with a pass that
	// runs anyway.
	emuRuntimeAlign = 4 * time.Hour
	// emuRuntimeRetry is the wait after a first failure; it doubles with each
	// failure in a row, up to coreAutoUpdateInterval.
	emuRuntimeRetry = time.Hour
	// emuGitHubHold is how long GitHub lookups wait after GitHub refused one.
	emuGitHubHold = time.Hour
	// emuRuntimeCheckTimeout bounds one check: a release lookup or two.
	emuRuntimeCheckTimeout = 2 * time.Minute
)

// emuRuntimeRecord is what is kept about one updater between runs.
type emuRuntimeRecord struct {
	// CheckedAt is the last check that answered, RFC 3339 in UTC.
	CheckedAt string `json:"checked_at,omitempty"`
	// Installed is the build the pass last installed, as its check named it.
	Installed string `json:"installed,omitempty"`
	// Announced is the build Notify mode last announced.
	Announced string `json:"announced,omitempty"`
}

// emuRuntimeFile is emu-autoupdate.json.
type emuRuntimeFile struct {
	Schema   int                         `json:"schema"`
	Updaters map[string]emuRuntimeRecord `json:"updaters,omitempty"`
	// DesktopNotice records that a PC was told, once, that JukaHub now keeps
	// the emulators it installed up to date by itself.
	DesktopNotice bool `json:"desktop_notice,omitempty"`
}

// emuRuntimeState is the record file and the session's memory. The session
// half runs on the monotonic clock: the last check of each updater that
// answered, the last attempt (the order), the earliest retry after a failure
// and how many failed in a row, GitHub's hold, and notes for the toast.
var emuRuntimeState struct {
	mu       sync.Mutex
	loaded   bool
	path     string
	file     emuRuntimeFile
	checked  map[string]time.Time
	attempt  map[string]time.Time
	retryAt  map[string]time.Time
	failures map[string]int
	hold     time.Time
	notes    []string
}

// emuRuntimeStatePath is where the record lives; a variable so a test can use
// a temporary folder. "" keeps the records in memory only.
var emuRuntimeStatePath = func() string {
	dir, err := PatchStateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, emuRuntimeStateName)
}

// emuRuntimeLoadLocked reads the record file the first time it is needed. A
// missing or unreadable file is an empty record: every updater is then due,
// which is the safe reading.
func emuRuntimeLoadLocked() {
	if emuRuntimeState.checked == nil {
		emuRuntimeState.checked = map[string]time.Time{}
		emuRuntimeState.attempt = map[string]time.Time{}
		emuRuntimeState.retryAt = map[string]time.Time{}
		emuRuntimeState.failures = map[string]int{}
	}
	if emuRuntimeState.loaded {
		return
	}
	path := emuRuntimeStatePath()
	emuRuntimeState.loaded = true
	emuRuntimeState.path = path
	emuRuntimeState.file = emuRuntimeFile{Schema: 1, Updaters: map[string]emuRuntimeRecord{}}
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return
	}
	var f emuRuntimeFile
	if json.Unmarshal(data, &f) != nil || f.Schema != 1 {
		return
	}
	if f.Updaters == nil {
		f.Updaters = map[string]emuRuntimeRecord{}
	}
	emuRuntimeState.file = f
}

// emuRuntimeSaveLocked writes the record file.
func emuRuntimeSaveLocked() {
	if emuRuntimeState.path == "" {
		return
	}
	data, err := json.MarshalIndent(emuRuntimeState.file, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(emuRuntimeState.path, append(data, '\n'), 0o600); err != nil {
		logWarn("emulators", "could not save %s: %v", emuRuntimeStateName, err)
	}
}

// emuRuntimeRecordFor returns the record of one updater.
func emuRuntimeRecordFor(key string) emuRuntimeRecord {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	emuRuntimeLoadLocked()
	return emuRuntimeState.file.Updaters[key]
}

// emuRuntimeUpdateRecord changes one updater's record and saves the file.
func emuRuntimeUpdateRecord(key string, change func(r *emuRuntimeRecord)) {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	emuRuntimeLoadLocked()
	rec := emuRuntimeState.file.Updaters[key]
	change(&rec)
	emuRuntimeState.file.Updaters[key] = rec
	emuRuntimeSaveLocked()
}

// emuRuntimeChecked records a check of key that answered (or an install that
// finished): the stamp, the session guard, and the end of a run of failures.
func emuRuntimeChecked(key string, now time.Time) {
	emuRuntimeUpdateRecord(key, func(r *emuRuntimeRecord) { r.CheckedAt = now.UTC().Format(time.RFC3339) })
	emuRuntimeState.mu.Lock()
	emuRuntimeState.checked[key] = time.Now()
	delete(emuRuntimeState.retryAt, key)
	delete(emuRuntimeState.failures, key)
	emuRuntimeState.mu.Unlock()
}

// emuRuntimeFailed records a failed check or install of key, starts its wait
// before the next try and returns how many have failed in a row.
func emuRuntimeFailed(key string) int {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	emuRuntimeLoadLocked()
	n := emuRuntimeState.failures[key] + 1
	emuRuntimeState.failures[key] = n
	emuRuntimeState.retryAt[key] = time.Now().Add(emuRuntimeRetryAfter(n))
	return n
}

// emuRuntimeRetryAfter is the wait after the n-th failure in a row: an hour,
// doubling, never more than a day.
func emuRuntimeRetryAfter(n int) time.Duration {
	wait := emuRuntimeRetry
	for i := 1; i < n && wait < coreAutoUpdateInterval; i++ {
		wait *= 2
	}
	if wait > coreAutoUpdateInterval {
		wait = coreAutoUpdateInterval
	}
	return wait
}

// emuRuntimeDefer keeps key out of the passes for d this session: an install
// the user cancelled from the card is a "not now" for that pass's rest.
func emuRuntimeDefer(key string, d time.Duration) {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	emuRuntimeLoadLocked()
	emuRuntimeState.retryAt[key] = time.Now().Add(d)
}

// emuRuntimeNoteAttempt records that key is being tried now (the order).
func emuRuntimeNoteAttempt(key string) {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	emuRuntimeLoadLocked()
	emuRuntimeState.attempt[key] = time.Now()
}

// emuRuntimeDue reports whether key may be checked now: it is not waiting out
// a failure, has not been checked this session within interval, and its stamp
// is older than interval or cannot be trusted.
func emuRuntimeDue(key string, now time.Time, interval time.Duration) bool {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	emuRuntimeLoadLocked()
	if at, ok := emuRuntimeState.retryAt[key]; ok && time.Now().Before(at) {
		return false
	}
	if at, ok := emuRuntimeState.checked[key]; ok && time.Since(at) < interval {
		return false
	}
	return emuRuntimeStampDue(emuRuntimeState.file.Updaters[key].CheckedAt, now, interval)
}

// emuRuntimeStampDue is coreAutoUpdateDue for any interval: a missing,
// garbled, future or pre-2020 stamp is due, because a handheld without an RTC
// battery can read any of those after a power cycle.
func emuRuntimeStampDue(last string, now time.Time, interval time.Duration) bool {
	t, ok := parseReleaseTimestamp(last)
	if !ok || now.Before(t) || t.Before(minSaneClock) || now.Before(minSaneClock) {
		return true
	}
	return now.Sub(t) >= interval
}

// emuRuntimeLeaveNote lets an updater add one line to the pass's toast:
// something the user should hear about an install beyond "updated" (hakuX
// replacing files the user had changed, and where their copies are).
func emuRuntimeLeaveNote(line string) {
	if line = strings.TrimSpace(line); line == "" {
		return
	}
	emuRuntimeState.mu.Lock()
	emuRuntimeState.notes = append(emuRuntimeState.notes, line)
	emuRuntimeState.mu.Unlock()
}

// emuRuntimeTakeNotes returns the notes left so far and forgets them.
func emuRuntimeTakeNotes() []string {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	out := emuRuntimeState.notes
	emuRuntimeState.notes = nil
	return out
}

// ── GitHub's hold ───────────────────────────────────────────────────────────

// emuGitHubHeld reports whether GitHub lookups are waiting out a refusal.
func emuGitHubHeld() bool {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	return !emuRuntimeState.hold.IsZero() && time.Now().Before(emuRuntimeState.hold)
}

// emuGitHubRefused starts the hour's hold (or extends it), saying so once.
func emuGitHubRefused(why string) {
	emuRuntimeState.mu.Lock()
	already := !emuRuntimeState.hold.IsZero() && time.Now().Before(emuRuntimeState.hold)
	emuRuntimeState.hold = time.Now().Add(emuGitHubHold)
	emuRuntimeState.mu.Unlock()
	if !already {
		logPatch("emulator updates: GitHub refused a lookup (%s); GitHub lookups wait an hour", why)
	}
}

// emuRateLimited reports an error that is GitHub refusing requests: relpack's
// sentinel, or the "HTTP 403/429 from <url>" fetchUpdateBytes and fetchBounded
// put in their errors when the URL is GitHub's. (The buildbot's refusals are
// failures like any other.)
func emuRateLimited(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errRelPackRateLimited) {
		return true
	}
	msg := strings.ToLower(err.Error())
	refused := strings.Contains(msg, "http 403") || strings.Contains(msg, "http 429") || strings.Contains(msg, "rate limit")
	return refused && strings.Contains(msg, "github")
}

// emuPassTransport is the HTTP transport of one pass (and of the Emulators
// page's background check). For GitHub's API — and nothing else — it:
//
//   - answers a question this pass already asked from memory: the check and
//     the install of hakuX, of a Windows emulator or of a runtime each ask for
//     the same release;
//   - asks conditionally where this process holds an earlier answer
//     (httpcache.go), unless the caller does that itself: an unchanged release
//     answers a bodiless 304, which spares the handheld's connection (not the
//     hour's count — GitHub spares that only to a client that signs in);
//   - starts the hour's hold on a 403 or 429, or when GitHub says none of the
//     hour's requests are left.
type emuPassTransport struct {
	base http.RoundTripper
	mu   sync.Mutex
	memo map[string]emuMemoEntry
}

// emuMemoEntry is one answer kept for the rest of a pass: a release (200), or
// GitHub saying there is none (404 — "latest" of a project that publishes
// only pre-releases), which the install would otherwise ask again too.
type emuMemoEntry struct {
	status int
	body   []byte
}

// emuPassClient is a copy of client that goes through a new pass transport;
// the caller's client is not changed. A client without a transport gets the
// default one with the Patch client's 25-second limit on response headers, so
// a server that stops answering cannot hold the pass until its deadline (a
// body is bounded by the caller's context).
func emuPassClient(client *http.Client) *http.Client {
	out := &http.Client{}
	if client != nil {
		*out = *client
	}
	base := out.Transport
	if base == nil {
		base = http.DefaultTransport
		if def, ok := http.DefaultTransport.(*http.Transport); ok {
			tr := def.Clone()
			tr.ResponseHeaderTimeout = patchHTTPTimeout
			base = tr
		}
	}
	out.Transport = &emuPassTransport{base: base, memo: map[string]emuMemoEntry{}}
	return out
}

// emuGitHubAPIRequest reports a GET of GitHub's API.
func emuGitHubAPIRequest(req *http.Request) bool {
	return req != nil && req.Method == http.MethodGet && req.URL != nil &&
		strings.EqualFold(req.URL.Hostname(), "api.github.com")
}

// RoundTrip implements http.RoundTripper.
func (t *emuPassTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !emuGitHubAPIRequest(req) {
		return t.base.RoundTrip(req)
	}
	key := req.URL.String()
	// fetchBounded asks conditionally itself and answers its own 304.
	callerConditional := req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != ""
	original := req
	if !callerConditional {
		t.mu.Lock()
		kept, ok := t.memo[key]
		t.mu.Unlock()
		if ok {
			return emuMemoResponse(req, kept), nil
		}
		if etag, modified := conditionalCache.validators(key); etag != "" || modified != "" {
			req = req.Clone(req.Context())
			if etag != "" {
				req.Header.Set("If-None-Match", etag)
			}
			if modified != "" {
				req.Header.Set("If-Modified-Since", modified)
			}
		}
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		emuGitHubRefused(fmt.Sprintf("HTTP %d", resp.StatusCode))
		return resp, nil
	case resp.Header.Get("X-RateLimit-Remaining") == "0":
		emuGitHubRefused("none of the hour's requests are left")
	}
	if callerConditional {
		return resp, nil
	}
	switch resp.StatusCode {
	case http.StatusNotModified:
		_ = resp.Body.Close()
		if body, ok := conditionalBody(key); ok {
			kept := emuMemoEntry{status: http.StatusOK, body: body}
			t.remember(key, kept)
			return emuMemoResponse(req, kept), nil
		}
		// The earlier answer was dropped in between: ask again outright.
		return t.base.RoundTrip(original)
	case http.StatusOK, http.StatusNotFound:
	default:
		return resp, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, httpCacheMaxBody+1))
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	if len(body) > httpCacheMaxBody {
		// Larger than anything worth keeping: handed on whole, not remembered.
		resp.Body = emuJoinedBody{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), Closer: resp.Body}
		return resp, nil
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		storeConditionalResponse(key, resp, body)
	}
	t.remember(key, emuMemoEntry{status: resp.StatusCode, body: body})
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp, nil
}

// remember keeps one answer for the rest of the pass.
func (t *emuPassTransport) remember(key string, kept emuMemoEntry) {
	t.mu.Lock()
	t.memo[key] = kept
	t.mu.Unlock()
}

// emuJoinedBody is a response body whose start was already read: the rest is
// read from, and closed through, the original body.
type emuJoinedBody struct {
	io.Reader
	io.Closer
}

// emuMemoResponse is an answer from memory, shaped like the server's.
func emuMemoResponse(req *http.Request, kept emuMemoEntry) *http.Response {
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", kept.status, http.StatusText(kept.status)),
		StatusCode:    kept.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(kept.body)),
		ContentLength: int64(len(kept.body)),
		Request:       req,
	}
}

// ── Choosing and running the updaters ───────────────────────────────────────

// emuAllRuntimeUpdaters is every updater there is right now: the registered
// ones and the Windows emulators JukaHub knows how to install, built fresh from
// emuWinSpecs so an emulator added to that list is covered without a change
// here.
func emuAllRuntimeUpdaters() []emuRuntimeUpdater {
	list := emuRuntimeUpdatersSnapshot()
	seen := make(map[string]bool, len(list))
	for _, u := range list {
		seen[u.Key] = true
	}
	for _, u := range emuWinRuntimeUpdaters() {
		if !seen[u.Key] {
			list = append(list, u)
		}
	}
	return list
}

// emuRuntimesForPass is the updaters a pass would run now, decided without
// the network: those that apply here, are installed and are due — and, when
// riding (a pass runs anyway) or one is due, those that are nearly due — minus
// any whose program is running; none while GitHub is refusing. Installed (a few
// file reads) and InUse (a process list on a PC) are asked only of updaters
// whose stamp is due. The oldest attempt comes first.
func emuRuntimesForPass(now time.Time, riding bool) []emuRuntimeUpdater {
	if emuGitHubHeld() {
		return nil
	}
	var due, near []emuRuntimeUpdater
	for _, u := range emuAllRuntimeUpdaters() {
		if u.Supported != nil && !u.Supported() {
			continue
		}
		full := emuRuntimeDue(u.Key, now, coreAutoUpdateInterval)
		if !full && !emuRuntimeDue(u.Key, now, coreAutoUpdateInterval-emuRuntimeAlign) {
			continue
		}
		if !u.ready() || u.busy() {
			continue
		}
		if full {
			due = append(due, u)
		} else {
			near = append(near, u)
		}
	}
	if len(due) == 0 && !riding {
		return nil
	}
	list := append(due, near...)
	emuRuntimeOrder(list)
	return list
}

// emuRuntimeOrder sorts updaters by their last attempt this session, never
// tried first, then by key.
func emuRuntimeOrder(list []emuRuntimeUpdater) {
	emuRuntimeState.mu.Lock()
	emuRuntimeLoadLocked()
	attempts := make(map[string]time.Time, len(list))
	for _, u := range list {
		attempts[u.Key] = emuRuntimeState.attempt[u.Key]
	}
	emuRuntimeState.mu.Unlock()
	sort.SliceStable(list, func(i, j int) bool {
		a, b := attempts[list[i].Key], attempts[list[j].Key]
		if !a.Equal(b) {
			return a.Before(b)
		}
		return list[i].Key < list[j].Key
	})
}

// emuRuntimesOverdue reports whether one of list has gone without an answered
// check for coreAutoUpdateOverdue — or, never checked, has waited a poll — so
// the pass stops waiting for the charger or an idle minute.
func emuRuntimesOverdue(list []emuRuntimeUpdater, now time.Time, waited time.Duration) bool {
	for _, u := range list {
		if coreAutoUpdateOverdueFor(emuRuntimeRecordFor(u.Key).CheckedAt, now, waited) {
			return true
		}
	}
	return false
}

// emuRuntimeReport is what the updaters did in one pass.
type emuRuntimeReport struct {
	// Updated names the builds installed ("hakuX v0.1.5"); Available, in
	// Notify mode, the new builds not announced before.
	Updated   []string
	Available []string
	// Failed names the updaters whose install failed for the first time in a
	// row (later failures are logged only); FailedNote is the first one's
	// "<title>: <reason>".
	Failed     []string
	FailedNote string
	// Held reports that GitHub refused and the rest wait out the hour.
	Held bool
	// Interrupted reports a pass stopped part-way: a game started, the
	// install card was cancelled, or another run took the card. Resume says
	// the rest should follow at the next quiet moment (not after a cancel,
	// which is a "not now").
	Interrupted bool
	Resume      bool
	// Notes are lines updaters left for the toast.
	Notes []string
}

// emuAutoBar is a pass's install card, begun the first time the pass has
// something to download, so a pass that finds everything current puts no card
// on screen. A nil bar never begins (Notify mode).
type emuAutoBar struct {
	label string
	begun bool
	gen   uint64
}

// begin shows the card if it is not up yet.
func (b *emuAutoBar) begin() {
	if b == nil || b.begun {
		return
	}
	b.begun = true
	installBarBeginLabelled("Emulators", b.label)
	b.gen = installBarGeneration()
}

// lost reports that the card is no longer the pass's: cancelled from the card
// or the activity strip, or taken by another run (the helper-tool download
// starts outside the Patch lock, and its begin retires this run's token).
func (b *emuAutoBar) lost() (cancelled, taken bool) {
	if b == nil || !b.begun {
		return false, false
	}
	if installBarGeneration() != b.gen {
		return false, true
	}
	return !installBarActive(), false
}

// finish settles the card if the pass showed it and it is still the pass's.
func (b *emuAutoBar) finish() {
	if b == nil || !b.begun {
		return
	}
	installBarDoneRun(b.gen)
}

// emuRunRuntimeUpdaters runs list in mode inside a pass. pass is cancelled
// when a game starts; bar is the pass's card (nil in Notify mode); now is the
// pass's wall-clock time, for the stamps.
func emuRunRuntimeUpdaters(pass context.Context, mode coreUpdateMode, list []emuRuntimeUpdater, bar *emuAutoBar, now time.Time) emuRuntimeReport {
	var report emuRuntimeReport
	// One client for the whole pass, so its transport remembers each answer.
	client := emuPassClient(emuWinClient())
	_ = emuRuntimeTakeNotes()
	for i, u := range list {
		if pass.Err() != nil {
			report.Interrupted = true
			break
		}
		if emuGitHubHeld() {
			report.Held = true
			logPatch("emulator updates: %d left for when GitHub's hold is over", len(list)-i)
			break
		}
		if u.busy() {
			logPatch("emulator updates: %s is running; it is updated at the next quiet moment", u.Title)
			continue
		}
		emuRuntimeNoteAttempt(u.Key)
		checkCtx, cancelCheck := context.WithTimeout(pass, emuRuntimeCheckTimeout)
		newer, label, err := u.Check(checkCtx, client)
		cancelCheck()
		if err != nil {
			if pass.Err() != nil {
				report.Interrupted = true
				break
			}
			emuRuntimeFailure(&report, u, "check", err)
			continue
		}
		label = strings.TrimSpace(label)
		if !newer {
			emuRuntimeChecked(u.Key, now)
			logPatch("emulator updates: %s is current", u.Title)
			continue
		}
		rec := emuRuntimeRecordFor(u.Key)
		if label != "" && label == rec.Installed {
			emuRuntimeChecked(u.Key, now)
			logPatch("emulator updates: %s %s, which this pass installed, was put back by hand; it is not installed again", u.Title, label)
			continue
		}
		name := emuRuntimeBuildName(u.Title, label)
		if mode != coreUpdateInstall {
			emuRuntimeChecked(u.Key, now)
			logPatch("emulator updates: new build of %s (Notify me: nothing installed)", name)
			if label == "" || label != rec.Announced {
				report.Available = append(report.Available, name)
				emuRuntimeUpdateRecord(u.Key, func(r *emuRuntimeRecord) { r.Announced = label })
			}
			continue
		}
		// The check took a moment: the program may have started since.
		if u.busy() {
			logPatch("emulator updates: %s started; it is updated at the next quiet moment", u.Title)
			continue
		}
		// The card may have been dismissed while the checks ran, or taken by
		// another run: an install must not go on behind a card that is not
		// this pass's.
		if cancelled, taken := bar.lost(); cancelled || taken {
			emuRuntimeLostCard(&report, list[i:], taken)
			break
		}
		bar.begin()
		installBarResetProgress()
		installBarPhase(installPhaseResolving)
		installBarDetail(u.Title)
		// The install's deadline derives from the card's run token, so a
		// cancel from the card stops the transfer; a game starting stops it
		// through the pass.
		installCtx, cancelInstall := context.WithTimeout(installBarRunContext(), emuWinInstallTimeout)
		stopLink := context.AfterFunc(pass, cancelInstall)
		logPatch("emulator updates: installing %s", name)
		title := u.Title
		err = u.Install(installCtx, client, func(line string) {
			line = strings.TrimSpace(line)
			if line == "" {
				return
			}
			// A line that names what it installs ("Downloading the Windows
			// app runtime · 12 of 40 MB") is shown as it is.
			if !strings.Contains(strings.ToLower(line), strings.ToLower(title)) {
				line = title + " · " + line
			}
			installBarDetail(line)
		})
		tokenStopped := errors.Is(installCtx.Err(), context.Canceled) && pass.Err() == nil
		stopLink()
		cancelInstall()
		switch {
		case err == nil:
			emuRuntimeChecked(u.Key, now)
			emuRuntimeUpdateRecord(u.Key, func(r *emuRuntimeRecord) { r.Installed = label })
			report.Updated = append(report.Updated, name)
			logPatch("emulator updates: %s installed", name)
		case errors.Is(err, errEmuUpdaterCurrent):
			emuRuntimeChecked(u.Key, now)
			logPatch("emulator updates: %s turned out to be current", u.Title)
		case errors.Is(err, errRelPackBusy):
			// Its own page is installing it right now: nothing failed, and
			// the next pass looks again.
			logPatch("emulator updates: %s is being installed from its page", u.Title)
		case pass.Err() != nil:
			report.Interrupted, report.Resume = true, true
		case tokenStopped:
			_, taken := bar.lost()
			emuRuntimeLostCard(&report, list[i:], taken)
		default:
			emuRuntimeFailure(&report, u, "install", err)
		}
		if report.Interrupted {
			break
		}
	}
	if pass.Err() != nil {
		report.Resume = true
	}
	report.Notes = emuRuntimeTakeNotes()
	return report
}

// emuRuntimeLostCard ends a pass whose card is no longer its own. Cancelled
// from the card, it is a "not now" for rest, which would otherwise start again
// a poll later; taken by another run, rest follows at the next quiet moment.
func emuRuntimeLostCard(report *emuRuntimeReport, rest []emuRuntimeUpdater, taken bool) {
	report.Interrupted = true
	if taken {
		report.Resume = true
		logPatch("emulator updates: another install took the card; the rest at the next quiet moment")
		return
	}
	logPatch("emulator updates: cancelled from the card; the rest wait a day")
	for _, u := range rest {
		emuRuntimeDefer(u.Key, coreAutoUpdateInterval)
	}
}

// emuRuntimeFailure logs a failed check or install and starts its wait — or,
// when GitHub refused, the hour's hold, which leaves the updater unstamped so
// it runs when the hour is over. The first failed install in a row is named in
// the toast; a failed check (no server reached, usually) is a log line, as the
// cores' failed index fetch is.
func emuRuntimeFailure(report *emuRuntimeReport, u emuRuntimeUpdater, what string, err error) {
	logPatch("emulator updates: %s %s failed: %v", u.Title, what, err)
	if emuRateLimited(err) {
		emuGitHubRefused("rate limit")
		report.Held = true
		return
	}
	if emuRuntimeFailed(u.Key) == 1 && what == "install" {
		report.Failed = append(report.Failed, u.Title)
		if report.FailedNote == "" {
			report.FailedNote = u.Title + ": " + emuRuntimeFriendly(err)
		}
	}
}

// emuRuntimeFriendly is the short reason a failed install gives in the toast:
// relpack's own wording for a runtime package, the emulator installs' wording
// otherwise.
func emuRuntimeFriendly(err error) string {
	for _, sentinel := range []error{
		errRelPackNotPublished, errRelPackOffline, errRelPackCutOff, errRelPackRateLimited,
		errRelPackClock, errRelPackTrust, errRelPackNoSpace, errRelPackDiskWrite,
		errRelPackUnverified, errRelPackUnlisted, errRelPackWontRun, errRelPackBusy, errRelPackUnsupported,
	} {
		if errors.Is(err, sentinel) {
			return relPackFriendly(err)
		}
	}
	return emuFriendlyError(err)
}

// emuRuntimeBuildName names a build in a toast: "hakuX v0.1.5".
func emuRuntimeBuildName(title, label string) string {
	return strings.TrimSpace(title + " " + label)
}

// emuRuntimeList joins names for a toast, at most three and a count.
func emuRuntimeList(names []string) string {
	if len(names) <= 3 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:3], ", ") + fmt.Sprintf(" +%d more", len(names)-3)
}

// emuRuntimeSummary is the updaters' share of the pass's toast, starting in
// lower case so it can follow the cores' share ("" when there is no news).
func emuRuntimeSummary(r emuRuntimeReport) string {
	var parts []string
	if len(r.Updated) > 0 {
		parts = append(parts, "updated "+emuRuntimeList(r.Updated))
	}
	switch len(r.Failed) {
	case 0:
	case 1:
		parts = append(parts, r.FailedNote)
	default:
		parts = append(parts, fmt.Sprintf("%d updates failed (%s) · tried again later", len(r.Failed), emuRuntimeList(r.Failed)))
	}
	parts = append(parts, r.Notes...)
	return strings.Join(parts, " · ")
}

// emuAutoPassToast shows the pass's one toast — the cores' summary
// (emuAutoUpdateSummary), then the updaters' — and nothing when neither has
// news. A toast replaces the one before it at once, so a pass never shows two.
func emuAutoPassToast(core *emuInstallReport, extras emuRuntimeReport) {
	var parts []string
	kind := ToastKindSuccess
	updated := false
	if core != nil {
		parts = append(parts, emuAutoUpdateSummary(*core))
		updated = len(core.Updated) > 0
		if len(core.Errors) > 0 || len(core.Updated) == 0 || core.LowSpace != "" {
			kind = ToastKindWarning
		}
	}
	if s := emuRuntimeSummary(extras); s != "" {
		parts = append(parts, s)
		updated = updated || len(extras.Updated) > 0
		if len(extras.Failed) > 0 {
			kind = ToastKindWarning
		}
	}
	if len(parts) == 0 {
		return
	}
	if !updated {
		kind = ToastKindWarning
	}
	msg := capitalizeFirst(strings.Join(parts, " · "))
	if updated {
		PlaySuccessSound()
	}
	ShowToast(msg, kind)
	logPatch("emulator auto-update: %s", msg)
}

// emuAutoNotifyToast is Notify mode's one toast: the cores with a different
// build (coreUpdateNotifyMessage's sentence) and the new builds of everything
// else, each said once.
func emuAutoNotifyToast(coreMsg string, available []string) {
	msg := coreMsg
	switch {
	case len(available) == 0:
	case msg != "":
		msg += " · new versions of " + emuRuntimeList(available) + " too"
	default:
		msg = "New versions: " + emuRuntimeList(available) + " · nothing was installed (Emulator updates: Notify me)"
	}
	if msg == "" {
		return
	}
	ShowToast(msg, ToastKindInfo)
	logPatch("emulator update check: %s", msg)
}

// ── What the setting covers ─────────────────────────────────────────────────

// emuAutoUpdateCoverage caches what the setting covers: the answer depends on
// the device and the registrations, which do not change while JukaHub runs,
// and the Settings row asks on every frame it draws.
var emuAutoUpdateCoverage struct {
	once        sync.Once
	long, short string
}

// emuAutoUpdateCovers names what the Emulator updates setting looks after on
// this device, for the texts that explain it: "emulator cores, PPSSPP, hakuX,
// runtimes" on the Smart Pro, "Windows emulators, RetroArch cores" on a PC
// ("runtimes" being the Windows-app runtime, AndroidRT and whatever else
// registers). "" when nothing here is updated by itself.
func emuAutoUpdateCovers() string {
	emuAutoUpdateCoverageLoad()
	return emuAutoUpdateCoverage.long
}

// emuAutoUpdateCoversShort is the same in as few letters as it can be, for
// the boot card's row: "cores, PPSSPP, hakuX, runtimes".
func emuAutoUpdateCoversShort() string {
	emuAutoUpdateCoverageLoad()
	return emuAutoUpdateCoverage.short
}

func emuAutoUpdateCoverageLoad() {
	emuAutoUpdateCoverage.once.Do(func() {
		var long, short []string
		add := func(l, s string) {
			long = append(long, l)
			short = append(short, s)
		}
		switch {
		case IsTrimuiDevice():
			add("emulator cores", "cores")
			if ppssppManagedEnabled() {
				add("PPSSPP", "PPSSPP")
			}
		case IsWindows():
			add("Windows emulators", "emulators")
			add("RetroArch cores", "cores")
		}
		runtimes := false
		for _, u := range emuRuntimeUpdatersSnapshot() {
			if u.Supported != nil && !u.Supported() {
				continue
			}
			if u.Key == "hakux" {
				add(u.Title, u.Title)
				continue
			}
			runtimes = true
		}
		if runtimes {
			add("runtimes", "runtimes")
		}
		emuAutoUpdateCoverage.long = strings.Join(long, ", ")
		emuAutoUpdateCoverage.short = strings.Join(short, ", ")
	})
}
