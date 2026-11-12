package main

// apkstore_catalog.go — the Store tab: what is known about Android titles and
// kinds of APK, and where to get them legally.
//
// It is deliberately small and honest. JukaHub cannot hand out commercial
// APKs, and AndroidRT runs only a narrow native subset, so most entries
// say "experimental" or "won't run" and point at the route that works (your
// own APK, PortMaster, a full Android). An entry may carry a direct download
// — for free software whose author publishes the APK — but only with a
// SHA-256, and only from the HTTPS hosts JukaHub's downloader trusts
// (validatePatchURL): a download without a hash is refused, never "probably
// fine". The built-in list pins one device-tested game and one structurally
// eligible native candidate; an experimental label never promises that a new
// title ran on the handheld.
//
// The list is embedded (apkstore_catalog.json). A newer copy may be published
// at generate.jukalang.com/apk-catalog.json (the builder site is deployed from
// the repository root, which carries the same file); the store fetches it at
// most once a day, size-capped and schema-checked, and keeps the built-in list
// whenever the fetched one is not strictly better.

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed apkstore_catalog.json
var apkStoreCatalogJSON []byte

const (
	apkStoreCatalogSchema   = 1
	apkStoreCatalogMaxBytes = 256 << 10
	apkStoreCatalogMax      = 200
	apkStoreCatalogURL      = "https://generate.jukalang.com/apk-catalog.json"
	apkStoreCatalogHost     = "generate.jukalang.com"
	apkStoreCatalogTimeout  = 12 * time.Second
	apkStoreCatalogEvery    = 24 * time.Hour
	apkStoreCatalogFile     = "catalog.json"
)

// Catalogue statuses.
const (
	apkStatusExperimental = "experimental"
	apkStatusWontRun      = "wontrun"
	apkStatusWorks        = "works"
)

var (
	apkStoreCatalogIDPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`) })
	apkStoreSHA256Pattern    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[0-9a-fA-F]{64}$`) })
)

// apkStoreCatalogEntry is one row of the Store tab.
type apkStoreCatalogEntry struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Package string `json:"package,omitempty"`
	Kind    string `json:"kind,omitempty"` // "title" or "category"
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Get     string `json:"get,omitempty"`   // how to get the APK legally
	Notes   string `json:"notes,omitempty"` // what to expect
	Route   string `json:"route,omitempty"` // the route that works when this does not
	License string `json:"license,omitempty"`
	// A direct download: all three, or none.
	URL    string `json:"url,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// downloadable reports whether the entry carries a download the store will
// run.
func (e apkStoreCatalogEntry) downloadable() bool {
	return e.URL != "" && e.SHA256 != "" && e.Size > 0
}

// installable means a verified download has a package identity and is offered
// for the native AndroidRT path. A catalogue entry without these facts can
// still be downloaded for inspection, but it is not silently added to Games.
func (e apkStoreCatalogEntry) installable() bool {
	return e.downloadable() && e.Package != "" && e.Kind != "category" && e.Status != apkStatusWontRun
}

// apkStoreCatalogLocalMatch links a catalogue title to a scanned APK already
// on the card. A package name is only a match for discovery: the APK's own
// scan verdict still decides whether AndroidRT may add it to Games.
type apkStoreCatalogLocalMatch struct {
	Info    apkStoreInfo
	Found   bool
	InGames bool
}

func apkStoreCatalogMatchOnCard(e apkStoreCatalogEntry, apks []apkStoreInfo, games map[string]apkStoreGameState) apkStoreCatalogLocalMatch {
	if e.Package == "" || e.Kind == "category" {
		return apkStoreCatalogLocalMatch{}
	}
	var best apkStoreCatalogLocalMatch
	bestScore := -1
	for _, info := range apks {
		if info.Manifest.Package != e.Package {
			continue
		}
		_, inGames := games[info.Path]
		score := 0
		if info.Tier.canAdd() {
			score += 2
		}
		if inGames {
			score++
		}
		if score > bestScore || score == bestScore && info.Manifest.VersionCode > best.Info.Manifest.VersionCode {
			best = apkStoreCatalogLocalMatch{Info: info, Found: true, InGames: inGames}
			bestScore = score
		}
	}
	return best
}

// apkStoreNativeCandidate reports a scanned APK whose native start is worth
// showing in the Store, even when its CPU or split packaging blocks AndroidRT.
// Java games with native helper libraries are not native-start candidates.
func apkStoreNativeCandidate(info apkStoreInfo) bool {
	if info.Manifest.Package == "" || info.ManifestErr != "" || info.Tier == apkTierBroken {
		return false
	}
	if info.Tier.canAdd() || info.Entry == apkEntryFound {
		return true
	}
	nativeLauncher := info.Manifest.Launcher == "android.app.NativeActivity" ||
		info.Manifest.Launcher == "com.google.androidgamesdk.GameActivity"
	nativeLibrary := strings.TrimSpace(info.Manifest.NativeLib) != ""
	if info.Tier == apkTierSplit && (nativeLauncher || nativeLibrary) {
		return true // the base piece may have no library; its splits carry it
	}
	if len(info.ABIs) == 0 {
		return false
	}
	return !info.Manifest.HasCode || nativeLauncher || nativeLibrary
}

// apkStoreEntries adds locally scanned native candidates to the Store,
// including those AndroidRT cannot install. A package already in the
// catalogue keeps its authored entry and local-match actions.
func apkStoreEntries(catalog []apkStoreCatalogEntry, apks []apkStoreInfo) []apkStoreCatalogEntry {
	entries := append([]apkStoreCatalogEntry(nil), catalog...)
	known := make(map[string]bool, len(catalog))
	for _, e := range catalog {
		if e.Package != "" {
			known[e.Package] = true
		}
	}
	for i, info := range apks {
		pkg := info.Manifest.Package
		if known[pkg] || !apkStoreNativeCandidate(info) {
			continue
		}
		known[pkg] = true
		get := "Already on your card. Open the local APK or add it to Games here."
		if !info.Tier.canAdd() {
			get = "Already on your card. Open the local APK to see why AndroidRT cannot add it."
		}
		entries = append(entries, apkStoreCatalogEntry{
			ID:      fmt.Sprintf("local-%d", i),
			Title:   info.Label,
			Package: pkg,
			Kind:    "title",
			Status:  apkStatusExperimental,
			Summary: info.Verdict,
			Get:     get,
		})
	}
	return entries
}

// apkStoreCatalogData is the file: a dated list.
type apkStoreCatalogData struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Updated       string                 `json:"updated"`
	Entries       []apkStoreCatalogEntry `json:"entries"`
	// dropped counts the entries validation threw away (not in the file).
	dropped int
}

// apkStoreCatalogFutureSlack is how far past today a fetched or saved list's
// date may be (time zones, a list published early in the day). A later date
// is a mistake or a trick to pin that list for good, and the list is refused.
const apkStoreCatalogFutureSlack = 3 * 24 * time.Hour

// apkStoreCatalogNow is the clock the date check reads; tests set it.
var apkStoreCatalogNow = time.Now

// apkStoreCatalogTooNew reports a list dated implausibly far ahead: later than
// both today and the built-in list's date by more than the slack. A handheld
// without a clock battery may think it is 1970 until it reaches the network,
// so the built-in list's date stands in for a today that is behind it. The
// built-in list itself is never checked.
func apkStoreCatalogTooNew(updated, builtin string) bool {
	date, err := time.Parse("2006-01-02", updated)
	if err != nil {
		return true
	}
	ref := apkStoreCatalogNow()
	if b, err := time.Parse("2006-01-02", builtin); err == nil && b.After(ref) {
		ref = b
	}
	return date.After(ref.Add(apkStoreCatalogFutureSlack))
}

// usable reports whether a fetched or saved list may replace another: it has
// entries, and at least as many survived validation as were dropped (a list
// mostly made of entries this build cannot read is not "newer", it is
// unreadable).
func (d apkStoreCatalogData) usable() bool {
	return len(d.Entries) > 0 && len(d.Entries) >= d.dropped
}

// apkStoreCatalogLimits bound each text field: the file may come from the
// network, and everything in it is drawn.
var apkStoreCatalogLimits = map[string]int{
	"title": 80, "package": 200, "summary": 400, "get": 600, "notes": 800, "route": 600, "license": 60,
}

// parseApkStoreCatalog reads and validates a catalogue. An entry that breaks a
// rule is dropped (the rest stay); a download that breaks one is removed from
// its entry, which stays as information. The file itself must parse, carry
// this schema version and a date.
func parseApkStoreCatalog(raw []byte) (apkStoreCatalogData, error) {
	var data apkStoreCatalogData
	if len(raw) > apkStoreCatalogMaxBytes {
		return data, fmt.Errorf("catalogue is larger than %d bytes", apkStoreCatalogMaxBytes)
	}
	if err := json.Unmarshal(stripUTF8BOM(raw), &data); err != nil {
		return apkStoreCatalogData{}, err
	}
	if data.SchemaVersion != apkStoreCatalogSchema {
		return apkStoreCatalogData{}, fmt.Errorf("catalogue schemaVersion %d (this build reads %d)", data.SchemaVersion, apkStoreCatalogSchema)
	}
	if _, err := time.Parse("2006-01-02", data.Updated); err != nil {
		return apkStoreCatalogData{}, errors.New("catalogue has no valid updated date")
	}
	seen := map[string]bool{}
	kept := make([]apkStoreCatalogEntry, 0, len(data.Entries))
	for _, e := range data.Entries {
		if len(kept) >= apkStoreCatalogMax {
			break
		}
		e.ID = strings.TrimSpace(e.ID)
		if !apkStoreCatalogIDPattern().MatchString(e.ID) || seen[e.ID] {
			continue
		}
		e.Title = apkStoreCatalogText(e.Title, "title")
		e.Summary = apkStoreCatalogText(e.Summary, "summary")
		if e.Title == "" || e.Summary == "" {
			continue
		}
		e.Package = apkStoreCatalogText(e.Package, "package")
		e.Get = apkStoreCatalogText(e.Get, "get")
		e.Notes = apkStoreCatalogText(e.Notes, "notes")
		e.Route = apkStoreCatalogText(e.Route, "route")
		e.License = apkStoreCatalogText(e.License, "license")
		switch e.Status = strings.ToLower(strings.TrimSpace(e.Status)); e.Status {
		case apkStatusExperimental, apkStatusWontRun, apkStatusWorks:
		default:
			continue
		}
		switch e.Kind = strings.ToLower(strings.TrimSpace(e.Kind)); e.Kind {
		case "", "title", "category":
		default:
			e.Kind = "title"
		}
		if !apkStoreCatalogDownloadValid(e) {
			e.URL, e.SHA256, e.Size = "", "", 0
		}
		e.SHA256 = strings.ToLower(e.SHA256)
		seen[e.ID] = true
		kept = append(kept, e)
	}
	data.dropped = len(data.Entries) - len(kept)
	data.Entries = kept
	return data, nil
}

// apkStorePinBuiltins keeps known titles and executable downloads anchored to
// this JukaHub build. An online catalogue may add entries and update category
// guidance, but cannot hide a built-in game, claim its blocker has been fixed,
// or point the handheld's root process at a different APK.
func apkStorePinBuiltins(data, builtin apkStoreCatalogData) apkStoreCatalogData {
	pinned := make(map[string]apkStoreCatalogEntry, len(builtin.Entries))
	for _, e := range builtin.Entries {
		if e.Kind == "title" || e.downloadable() {
			pinned[e.ID] = e
		}
	}
	seen := make(map[string]bool, len(data.Entries))
	for i := range data.Entries {
		e := &data.Entries[i]
		seen[e.ID] = true
		if b, ok := pinned[e.ID]; ok {
			*e = b
		} else if e.downloadable() {
			e.URL, e.SHA256, e.Size = "", "", 0
		}
	}
	for _, e := range builtin.Entries {
		if _, keep := pinned[e.ID]; !keep || seen[e.ID] {
			continue
		}
		if len(data.Entries) < apkStoreCatalogMax {
			data.Entries = append(data.Entries, e)
			continue
		}
		for i := len(data.Entries) - 1; i >= 0; i-- {
			if _, keep := pinned[data.Entries[i].ID]; !keep {
				data.Entries[i] = e
				break
			}
		}
	}
	return data
}

// apkStoreCatalogText trims a field, drops control characters and bounds it.
func apkStoreCatalogText(s, field string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if max := apkStoreCatalogLimits[field]; max > 0 {
		if r := []rune(s); len(r) > max {
			s = string(r[:max-1]) + "…"
		}
	}
	return s
}

// apkStoreCatalogDownloadValid checks a download: an allowed HTTPS URL ending
// in .apk, a SHA-256 and a size within the downloader's cap.
func apkStoreCatalogDownloadValid(e apkStoreCatalogEntry) bool {
	if e.URL == "" && e.SHA256 == "" && e.Size == 0 {
		return false
	}
	if validatePatchURL(e.URL) != nil || !apkStoreSHA256Pattern().MatchString(e.SHA256) {
		return false
	}
	if e.Size <= 0 || e.Size > patchMaxDownload {
		return false
	}
	u, err := url.Parse(e.URL)
	return err == nil && strings.EqualFold(filepath.Ext(u.Path), ".apk")
}

// apkStoreCatalogState is the list in use.
var apkStoreCatalogState struct {
	mu     sync.Mutex
	loaded bool
	data   apkStoreCatalogData
	source string // "built in" or "updated <date>"
}

// apkStoreCatalogPathOverride points the saved copy elsewhere (tests).
var apkStoreCatalogPathOverride string

// apkStoreCatalogPath is where a fetched copy is kept.
func apkStoreCatalogPath() string {
	if apkStoreCatalogPathOverride != "" {
		return apkStoreCatalogPathOverride
	}
	if dir, err := P().DataDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "apkstore", apkStoreCatalogFile)
	}
	return filepath.Join("data", "apkstore", apkStoreCatalogFile)
}

// apkStoreCatalog returns the list in use: a saved newer copy when there is a
// valid one, else the built-in list.
func apkStoreCatalog() (apkStoreCatalogData, string) {
	apkStoreCatalogState.mu.Lock()
	defer apkStoreCatalogState.mu.Unlock()
	if !apkStoreCatalogState.loaded {
		apkStoreCatalogState.loaded = true
		builtin, err := parseApkStoreCatalog(apkStoreCatalogJSON)
		if err != nil {
			logWarn("apkstore", "built-in catalogue does not parse: %v", err)
		}
		apkStoreCatalogState.data, apkStoreCatalogState.source = builtin, "built in"
		if raw, err := os.ReadFile(apkStoreCatalogPath()); err == nil {
			if saved, err := parseApkStoreCatalog(raw); err == nil && saved.Updated > builtin.Updated && saved.usable() &&
				!apkStoreCatalogTooNew(saved.Updated, builtin.Updated) {
				saved = apkStorePinBuiltins(saved, builtin)
				apkStoreCatalogState.data, apkStoreCatalogState.source = saved, "updated "+saved.Updated
			}
		}
	}
	return apkStoreCatalogState.data, apkStoreCatalogState.source
}

// apkStoreCatalogDue reports whether a refresh is worth trying: online, and
// the saved copy older than a day (or absent).
func apkStoreCatalogDue(now time.Time) bool {
	if !IsOnline() {
		return false
	}
	st, err := os.Stat(apkStoreCatalogPath())
	return err != nil || now.Sub(st.ModTime()) > apkStoreCatalogEvery
}

// apkStoreCatalogClient only talks HTTPS to the catalogue's host, redirects
// included.
func apkStoreCatalogClient() *http.Client {
	return &http.Client{
		Timeout: apkStoreCatalogTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Hostname(), apkStoreCatalogHost) {
				return fmt.Errorf("redirect to %s refused", req.URL.Host)
			}
			return nil
		},
	}
}

// apkStoreRefreshCatalog fetches the published list. It replaces the one in
// use only when it parses, is newer and is not empty, and it touches the saved
// copy either way so a failing host is asked once a day, not on every visit.
func apkStoreRefreshCatalog(ctx context.Context, client *http.Client, rawURL string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "JukaHub-APKStore/"+versionString(nil))
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		apkStoreTouchCatalog()
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, apkStoreCatalogMaxBytes+1))
	if err != nil {
		return false, err
	}
	fetched, err := parseApkStoreCatalog(raw)
	if err != nil {
		apkStoreTouchCatalog()
		return false, err
	}
	current, _ := apkStoreCatalog()
	builtin, _ := parseApkStoreCatalog(apkStoreCatalogJSON)
	if apkStoreCatalogTooNew(fetched.Updated, builtin.Updated) {
		apkStoreTouchCatalog()
		return false, fmt.Errorf("catalogue is dated %s, in the future", fetched.Updated)
	}
	if fetched.Updated <= current.Updated || !fetched.usable() {
		apkStoreTouchCatalog()
		return false, nil
	}
	fetched = apkStorePinBuiltins(fetched, builtin)
	if err := AtomicWrite(apkStoreCatalogPath(), raw, 0o644); err != nil {
		return false, err
	}
	apkStoreCatalogState.mu.Lock()
	apkStoreCatalogState.data, apkStoreCatalogState.source = fetched, "updated "+fetched.Updated
	apkStoreCatalogState.mu.Unlock()
	return true, nil
}

// apkStoreTouchCatalog marks the saved copy as checked now (creating an empty
// marker when there is none, which the loader ignores: it does not parse).
func apkStoreTouchCatalog() {
	p := apkStoreCatalogPath()
	now := time.Now()
	if err := os.Chtimes(p, now, now); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		_ = os.WriteFile(p, []byte("{}\n"), 0o644)
	}
}

// apkStoreDownloadName is the file a catalogue download is saved as: its id
// (already limited to [a-z0-9._-], 64 characters) — whole, so two long ids
// that share a beginning never share a file.
func apkStoreDownloadName(e apkStoreCatalogEntry) string {
	return e.ID + ".apk"
}

// apkStoreDownloadDest picks where a download goes: <id>.apk, or, when a
// different file already has that name, <id>-<first 8 of the SHA-256>.apk —
// a download never replaces a file it did not write. done reports that the
// verified file is already there.
func apkStoreDownloadDest(dir string, e apkStoreCatalogEntry) (dest string, done bool, err error) {
	names := []string{apkStoreDownloadName(e), e.ID + "-" + strings.ToLower(e.SHA256[:8]) + ".apk"}
	for _, name := range names {
		p := filepath.Join(dir, name)
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p, false, nil
		}
		if regularFile(p) {
			if sum, err := SHA256File(p); err == nil && strings.EqualFold(sum, e.SHA256) {
				return p, true, nil
			}
		}
	}
	return "", false, fmt.Errorf("%s already holds other files named %s — move them away first", dir, names[0])
}

// apkStoreDownload fetches an entry's APK into the save folder, verified
// against its SHA-256 (downloadToFile resumes a partial file and removes one
// that does not match). It returns the saved path.
func apkStoreDownload(ctx context.Context, e apkStoreCatalogEntry) (string, error) {
	if !e.downloadable() || !apkStoreCatalogDownloadValid(e) {
		return "", errors.New("this entry has no verified download")
	}
	dir := apkStoreSaveDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest, done, err := apkStoreDownloadDest(dir, e)
	if err != nil || done {
		return dest, err
	}
	limit := e.Size + e.Size/20 + 1<<20
	if limit > patchMaxDownload {
		limit = patchMaxDownload
	}
	if err := downloadToFile(ctx, newPatchClient(), e.URL, dest, e.SHA256, limit); err != nil {
		return "", err
	}
	return dest, nil
}
