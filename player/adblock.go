package main

// adblock.go — keeps ad and tracker requests out of the built-in browser.
//
// The reader is not a full browser: it downloads the page once, then fetches
// its stylesheets, images and scripts. Those resources are where ads and
// trackers live, and on handheld Wi-Fi a single ad network can cost more bytes
// and seconds than the article itself. Every subresource request therefore
// passes through browserRequestBlocked first.
//
// This is a deliberate starter list, not an EasyList clone: only hosts that are
// unambiguously advertising or analytics, and URL paths that only exist to
// serve them. Blocking too broadly would break pages that legitimately load
// their own assets, which for a reading browser is worse than letting an ad
// through. The list is matched by host suffix, so "ads.example.com" only
// matches an entry for "example.com", never "notexample.com".
//
// A page that breaks without one of its scripts can be excepted site by site:
// the toggle is stored in data/browser-block.json, keyed by host, and both the
// fetch pass and the script-stripping pass consult it. The toolbar reports the
// count for the page on screen, so a page that saved nothing says so too.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// browserAdHosts are advertising, tracking and analytics hosts. A request to
// one of these is never made: no HTML, CSS, image or script comes back from
// them, so the page loads faster and nothing reports the visit.
var browserAdHosts = []string{
	// Google advertising and measurement
	"doubleclick.net",
	"googlesyndication.com",
	"googleadservices.com",
	"adservice.google.com",
	"googletagmanager.com",
	"googletagservices.com",
	"google-analytics.com",
	"analytics.google.com",
	"2mdn.net",
	// Ad exchanges, SSPs and retargeting
	"adnxs.com",
	"adsrvr.org",
	"amazon-adsystem.com",
	"criteo.com",
	"criteo.net",
	"rubiconproject.com",
	"pubmatic.com",
	"openx.net",
	"casalemedia.com",
	"smartadserver.com",
	"taboola.com",
	"outbrain.com",
	"revcontent.com",
	"mgid.com",
	"zedo.com",
	"adform.net",
	"adroll.com",
	"bidswitch.net",
	"sharethrough.com",
	"teads.tv",
	"spotxchange.com",
	"yieldmo.com",
	// Mobile ad SDKs
	"unityads.unity3d.com",
	"applovin.com",
	"adcolony.com",
	"chartboost.com",
	"vungle.com",
	"ironsrc.com",
	// Analytics, session replay and audience measurement
	"scorecardresearch.com",
	"quantserve.com",
	"comscore.com",
	"nielsen.com",
	"hotjar.com",
	"fullstory.com",
	"mixpanel.com",
	"segment.io",
	"segment.com",
	"amplitude.com",
	"heap.io",
	"matomo.cloud",
	"newrelic.com",
	"nr-data.net",
	"bugsnag.com",
	"clarity.ms",
	"bat.bing.com",
	"connect.facebook.net",
	"facebook.com/tr",
	"analytics.twitter.com",
	"static.ads-twitter.com",
	"ads.linkedin.com",
	"px.ads.linkedin.com",
	"snap.licdn.com",
	"tiktok.com/i18n/pixel",
	"ads.tiktok.com",
	"analytics.tiktok.com",
}

// browserAdPathHints are URL paths that only ad servers and pixels use. They
// are matched against the lower-cased path (and the gtag/GTM script names),
// with slashes kept in the pattern so a genuine "/tracks/" page is not caught.
var browserAdPathHints = []string{
	"/ads/",
	"/adserver",
	"/adservice",
	"/pagead/",
	"/adsbygoogle",
	"/prebid",
	"/advert/",
	"/analytics/",
	"/analytics.js",
	"/gtag/js",
	"/gtm.js",
	"/tracking/",
	"/track/",
	"/pixel/",
	"/pixel.gif",
	"/pixel.png",
	"/beacon/",
	"/collect?",
	"/sponsored/",
}

// browserAdBlockCounts holds the per-page counter the reader reports. The
// fields are guarded because images and stylesheets are fetched concurrently.
var browserAdBlock = struct {
	mu      sync.Mutex
	blocked int
}{}

// browserAdBlockSitesFile is the on-disk per-site exception list. Only sites
// where filtering is off are stored, so the file stays a short list of the
// pages that needed help rather than a copy of every site ever visited.
type browserAdBlockSitesFile struct {
	SchemaVersion int      `json:"schemaVersion"`
	DisabledSites []string `json:"disabledSites"`
}

var (
	browserAdBlockSites = struct {
		mu       sync.Mutex
		loaded   bool
		disabled map[string]bool
	}{disabled: map[string]bool{}}
	// browserAdBlockSitesPath is an indirection so tests never touch the
	// device's data directory.
	browserAdBlockSitesPath = defaultBrowserAdBlockSitesPath
)

func defaultBrowserAdBlockSitesPath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "browser-block.json")
}

// browserAdBlockSiteKey names the site a page belongs to: the lower-case host
// with a leading "www." removed, so www.example.com and example.com share one
// switch. A bare host is accepted too, which is what the saved file holds.
func browserAdBlockSiteKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil {
		if host := parsed.Hostname(); host != "" {
			return strings.TrimPrefix(strings.ToLower(host), "www.")
		}
	}
	host := raw
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
}

// ensureBrowserAdBlockSitesLocked loads the exception list once per process.
// The caller holds browserAdBlockSites.mu.
func ensureBrowserAdBlockSitesLocked() {
	if browserAdBlockSites.loaded {
		return
	}
	browserAdBlockSites.disabled = loadBrowserAdBlockSites(browserAdBlockSitesPath())
	browserAdBlockSites.loaded = true
}

// loadBrowserAdBlockSites reads the file. A missing file means filtering is on
// everywhere; a broken one is logged and ignored so a bad edit cannot make the
// browser unusable.
func loadBrowserAdBlockSites(path string) map[string]bool {
	disabled := map[string]bool{}
	if strings.TrimSpace(path) == "" {
		return disabled
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return disabled
	}
	var file browserAdBlockSitesFile
	if err := json.Unmarshal(raw, &file); err != nil {
		log.Printf("[browser] ignoring %s: %v", path, err)
		return disabled
	}
	for _, site := range file.DisabledSites {
		if key := browserAdBlockSiteKey(site); key != "" {
			disabled[key] = true
		}
	}
	return disabled
}

// saveBrowserAdBlockSites writes through a temporary file so a power loss
// cannot leave half a JSON document behind.
func saveBrowserAdBlockSites(path string, disabled map[string]bool) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	sites := make([]string, 0, len(disabled))
	for site := range disabled {
		sites = append(sites, site)
	}
	sort.Strings(sites)
	data, err := json.MarshalIndent(browserAdBlockSitesFile{
		SchemaVersion: 1,
		DisabledSites: sites,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// browserAdBlockOff reports whether filtering is disabled for the site a URL
// belongs to. It is safe to call for every request and every page.
func browserAdBlockOff(raw string) bool {
	site := browserAdBlockSiteKey(raw)
	if site == "" {
		return false
	}
	browserAdBlockSites.mu.Lock()
	defer browserAdBlockSites.mu.Unlock()
	ensureBrowserAdBlockSitesLocked()
	return browserAdBlockSites.disabled[site]
}

// setBrowserAdBlockOff stores one site's setting and writes the file. The
// caller's page is not reloaded here: the browser owns that decision.
func setBrowserAdBlockOff(raw string, off bool) error {
	site := browserAdBlockSiteKey(raw)
	if site == "" {
		return fmt.Errorf("that page has no site name")
	}
	browserAdBlockSites.mu.Lock()
	ensureBrowserAdBlockSitesLocked()
	if off {
		browserAdBlockSites.disabled[site] = true
	} else {
		delete(browserAdBlockSites.disabled, site)
	}
	next := make(map[string]bool, len(browserAdBlockSites.disabled))
	for key, value := range browserAdBlockSites.disabled {
		next[key] = value
	}
	path := browserAdBlockSitesPath()
	browserAdBlockSites.mu.Unlock()
	return saveBrowserAdBlockSites(path, next)
}

// resetBrowserAdBlockSitesForTest drops the loaded list so a test can point
// browserAdBlockSitesPath at a temporary file.
func resetBrowserAdBlockSitesForTest() {
	browserAdBlockSites.mu.Lock()
	browserAdBlockSites.loaded = false
	browserAdBlockSites.disabled = map[string]bool{}
	browserAdBlockSites.mu.Unlock()
}

// browserAdBlockView is what the toolbar knows about the page on screen: the
// site the switch belongs to, how many requests the load skipped, and whether
// its filtering is off.
type browserAdBlockView struct {
	Site    string
	Blocked int
	Off     bool
}

var browserAdBlockViews = struct {
	mu    sync.Mutex
	pages map[string]browserAdBlockView
}{pages: map[string]browserAdBlockView{}}

func setBrowserAdBlockView(variable string, view browserAdBlockView) {
	if variable == "" {
		return
	}
	browserAdBlockViews.mu.Lock()
	if browserAdBlockViews.pages == nil {
		browserAdBlockViews.pages = map[string]browserAdBlockView{}
	}
	browserAdBlockViews.pages[variable] = view
	browserAdBlockViews.mu.Unlock()
}

func browserAdBlockViewFor(variable string) browserAdBlockView {
	browserAdBlockViews.mu.Lock()
	defer browserAdBlockViews.mu.Unlock()
	return browserAdBlockViews.pages[variable]
}

func resetBrowserAdBlockViewsForTest() {
	browserAdBlockViews.mu.Lock()
	browserAdBlockViews.pages = map[string]browserAdBlockView{}
	browserAdBlockViews.mu.Unlock()
}

// browserAdBlockLabel is the toolbar chip's text. It stays short enough for a
// 104px chip even at three digits, because the chip shares the row with the
// address, Search and Go.
func browserAdBlockLabel(view browserAdBlockView) string {
	if view.Off {
		return "FILTER OFF"
	}
	if view.Blocked > 99 {
		return "99+ BLOCKED"
	}
	return fmt.Sprintf("%d BLOCKED", view.Blocked)
}

// resetBrowserAdBlockCount starts a new page's tally.
func resetBrowserAdBlockCount() {
	browserAdBlock.mu.Lock()
	browserAdBlock.blocked = 0
	browserAdBlock.mu.Unlock()
}

// browserBlockedRequests is how many subresource requests this page skipped.
func browserBlockedRequests() int {
	browserAdBlock.mu.Lock()
	defer browserAdBlock.mu.Unlock()
	return browserAdBlock.blocked
}

// browserRequestBlocked reports whether pageURL's load should skip rawURL,
// counting a skip so the reader can say what it saved.
//
// The page URL matters: filtering is switched off per site, and a request to an
// ad host from an excepted page is a request that page needs. Only http(s) URLs
// are inspectable; anything else is not a browser request and is left to the
// caller's own scheme checks.
func browserRequestBlocked(pageURL, rawURL string) bool {
	if browserAdBlockOff(pageURL) {
		return false
	}
	if browserURLIsAd(rawURL) {
		noteBrowserBlocked()
		return true
	}
	return false
}

// noteBrowserBlocked records one skipped request, including ad scripts removed
// from the source before the fetch layer ever sees them.
func noteBrowserBlocked() {
	browserAdBlock.mu.Lock()
	browserAdBlock.blocked++
	browserAdBlock.mu.Unlock()
}

// browserURLIsAd is the side-effect-free predicate, so it can be reused by
// tests and by callers that only want to know.
func browserURLIsAd(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	path := strings.ToLower(parsed.Path)
	if host != "" {
		for _, blocked := range browserAdHosts {
			if before, after, ok := strings.Cut(blocked, "/"); ok {
				// A host-plus-path entry (facebook.com/tr) only matches that
				// path prefix on the host or one of its subdomains.
				if (host == before || strings.HasSuffix(host, "."+before)) && strings.HasPrefix(path, "/"+after) {
					return true
				}
				continue
			}
			if host == blocked || strings.HasSuffix(host, "."+blocked) {
				return true
			}
		}
	}
	pathAndQuery := path + "?" + strings.ToLower(parsed.RawQuery)
	for _, hint := range browserAdPathHints {
		if strings.Contains(pathAndQuery, hint) {
			return true
		}
	}
	// Query-string markers used by tracking pixels.
	query := strings.ToLower(parsed.RawQuery)
	if strings.Contains(query, "utm_source=") && strings.Contains(path, "pixel") {
		return true
	}
	return false
}

// stripBlockedWebScripts removes script tags that point at an ad/tracker. The
// script pass already skips a src it cannot fetch, but deleting the tag up
// front means the JavaScript engine is never asked to run the inline fallback
// some ad snippets ship, and the page source stays smaller.
func stripBlockedWebScripts(source string, base *url.URL) string {
	if base == nil || !strings.Contains(strings.ToLower(source), "<script") {
		return source
	}
	if browserAdBlockOff(base.String()) {
		// The page is excepted: its scripts are its own, even the ones that
		// would normally be stripped.
		return source
	}
	return webScriptTagRe.ReplaceAllStringFunc(source, func(tag string) string {
		src := webScriptSrcRe.FindStringSubmatch(tag)
		if src == nil {
			return tag
		}
		target, err := base.Parse(strings.TrimSpace(src[1]))
		if err != nil {
			return tag
		}
		if browserURLIsAd(target.String()) {
			noteBrowserBlocked()
			return ""
		}
		return tag
	})
}
