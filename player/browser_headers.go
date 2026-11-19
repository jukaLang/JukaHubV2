package main

import (
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Internet browser presents a reduced Chrome identity. Most sites receive
// the desktop variant; Wikipedia receives its mobile variant so its mobile
// hostname's redirect keeps the mobile skin. The low-entropy client hints are
// the headers Chrome sends before a site asks for anything more.
//
// Keep these values in step: a UA saying one version beside a hint saying
// another is a stronger fingerprint than either header alone.
const (
	browserChromeMajor           = "157"
	browserChromeUserAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + browserChromeMajor + ".0.0.0 Safari/537.36"
	browserChromeMobileUserAgent = "Mozilla/5.0 (Linux; Android 12; Mobile) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + browserChromeMajor + ".0.0.0 Mobile Safari/537.36"
	browserChromeBrands          = `"Not-A.Brand";v="99", "Google Chrome";v="` + browserChromeMajor + `", "Chromium";v="` + browserChromeMajor + `"`
)

const (
	browserAcceptDocument = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"
	browserAcceptStyle    = "text/css,*/*;q=0.1"
	browserAcceptImage    = "image/webp,image/apng,image/png,image/jpeg,image/gif,image/bmp"
	browserAcceptScript   = "*/*"
	// browserAcceptImageNoWebP is the image Accept of a build whose SDL_image
	// cannot decode WebP.
	browserAcceptImageNoWebP = "image/apng,image/png,image/jpeg,image/gif,image/bmp"
)

// browserImageAccept is the Accept header an image request sends. It names
// only formats the reader can draw: image CDNs (Next.js, Cloudflare, Shopify,
// WordPress optimisers) pick the format from this header, and while it said
// image/avif — which SDL_image here cannot decode — they answered with AVIF
// and every such picture drew as a placeholder.
func browserImageAccept() string {
	if webpDecodable() {
		return browserAcceptImage
	}
	return browserAcceptImageNoWebP
}

// browserURL parses an address already known to be http(s); a malformed one
// yields nil so the header helper simply omits origin-derived values.
func browserURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return u
}

// browserChromeIdentity sets the headers every browser request carries.
func browserChromeIdentity(h http.Header) {
	h.Set("User-Agent", browserChromeUserAgent)
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Sec-CH-UA", browserChromeBrands)
	h.Set("Sec-CH-UA-Mobile", "?0")
	h.Set("Sec-CH-UA-Platform", `"Windows"`)
}

// Wikipedia's mobile hostname redirects to the canonical desktop hostname.
// Its skin is then chosen from the browser identity, so a desktop identity
// leaves a handheld reader with Vector's wide sidebars and offscreen menus.
func browserWikipediaPage(page, target *url.URL) bool {
	for _, u := range []*url.URL{page, target} {
		if u == nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host == "wikipedia.org" || strings.HasSuffix(host, ".wikipedia.org") {
			return true
		}
	}
	return false
}

func browserChromePageIdentity(h http.Header, page, target *url.URL) {
	browserChromeIdentity(h)
	if browserWikipediaPage(page, target) {
		h.Set("User-Agent", browserChromeMobileUserAgent)
		h.Set("Sec-CH-UA-Mobile", "?1")
		h.Set("Sec-CH-UA-Platform", `"Android"`)
	}
}

// browserClientHintSet is one origin's Accept-CH opt-in. Device hints are
// high-entropy: a browser sends them only after the origin asks for them, so
// they are kept per origin rather than added to every request.
type browserClientHintSet struct {
	viewportWidth  bool
	viewportHeight bool
	dpr            bool
}

func (s browserClientHintSet) any() bool {
	return s.viewportWidth || s.viewportHeight || s.dpr
}

var browserClientHints = struct {
	sync.Mutex
	origins map[string]browserClientHintSet
}{origins: make(map[string]browserClientHintSet)}

// browserClientHintsSecure follows the Client Hints secure-context rule and
// still permits loopback HTTP, where local development servers legitimately
// negotiate them.
func browserClientHintsSecure(u *url.URL) bool {
	if u == nil {
		return false
	}
	if strings.EqualFold(u.Scheme, "https") {
		return true
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func browserClientHintOrigin(u *url.URL) string {
	if u == nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// browserClientHintsNote records the device hints an origin asked for in an
// Accept-CH response. Unknown tokens are ignored: this reader only has the
// viewport and DPR values it can report honestly.
func browserClientHintsNote(h http.Header, u *url.URL) {
	if h == nil || !browserClientHintsSecure(u) {
		return
	}
	origin := browserClientHintOrigin(u)
	if origin == "" {
		return
	}
	var add browserClientHintSet
	for _, value := range h.Values("Accept-CH") {
		for _, token := range strings.Split(value, ",") {
			switch strings.ToLower(strings.TrimSpace(token)) {
			case "sec-ch-viewport-width", "viewport-width":
				add.viewportWidth = true
			case "sec-ch-viewport-height", "viewport-height":
				add.viewportHeight = true
			case "sec-ch-dpr", "dpr":
				add.dpr = true
			}
		}
	}
	if !add.any() {
		return
	}
	browserClientHints.Lock()
	defer browserClientHints.Unlock()
	set := browserClientHints.origins[origin]
	set.viewportWidth = set.viewportWidth || add.viewportWidth
	set.viewportHeight = set.viewportHeight || add.viewportHeight
	set.dpr = set.dpr || add.dpr
	if len(browserClientHints.origins) >= 128 {
		for old := range browserClientHints.origins {
			if old != origin {
				delete(browserClientHints.origins, old)
				break
			}
		}
	}
	browserClientHints.origins[origin] = set
}

// browserClientHintsApply adds the negotiated hints to a request. Values come
// from the same viewport the layout and page scripts use, so a server and the
// CSS media queries agree about the page width.
func browserClientHintsApply(h http.Header, target *url.URL) {
	if h == nil || !browserClientHintsSecure(target) {
		return
	}
	origin := browserClientHintOrigin(target)
	browserClientHints.Lock()
	set := browserClientHints.origins[origin]
	browserClientHints.Unlock()
	if !set.any() {
		return
	}
	w, height := webMediaViewport()
	if set.viewportWidth {
		h.Set("Sec-CH-Viewport-Width", strconv.Itoa(int(math.Ceil(w))))
	}
	if set.viewportHeight {
		h.Set("Sec-CH-Viewport-Height", strconv.Itoa(int(math.Ceil(height))))
	}
	if set.dpr {
		h.Set("Sec-CH-DPR", strconv.FormatFloat(webViewportPixelScale(), 'f', -1, 64))
	}
}

// browserChromeNavigate sets a top-level document navigation's headers,
// including the Fetch Metadata Chrome sends for a user-typed or opened load.
func browserChromeNavigate(h http.Header) {
	browserChromeIdentity(h)
	h.Set("Accept", browserAcceptDocument)
	h.Set("Upgrade-Insecure-Requests", "1")
	h.Set("Sec-Fetch-Site", "none")
	h.Set("Sec-Fetch-Mode", "navigate")
	h.Set("Sec-Fetch-User", "?1")
	h.Set("Sec-Fetch-Dest", "document")
	h.Set("Priority", "u=0, i")
}

// browserChromeNavigateURL is a navigation to a known origin, including any
// device client hints that origin negotiated on an earlier response.
func browserChromeNavigateURL(h http.Header, target *url.URL) {
	browserChromeNavigate(h)
	browserChromePageIdentity(h, nil, target)
	browserClientHintsApply(h, target)
}

// A form navigation comes from the page on screen, so its Referer and fetch
// site describe that page. POST also sends its origin, which many sites use to
// reject cross-site submissions. The body never appears in these headers.
func browserChromeFormNavigate(h http.Header, source, target *url.URL, method string) {
	if source == nil || target == nil || source.Host == "" {
		return
	}
	if site := browserFetchSite(source, target); site != "" {
		h.Set("Sec-Fetch-Site", site)
	}
	if ref := browserReferer(source, target); ref != "" {
		h.Set("Referer", ref)
	}
	if method == http.MethodPost && (source.Scheme == "http" || source.Scheme == "https") {
		h.Set("Origin", source.Scheme+"://"+source.Host)
	}
}

// browserChromeResource sets a document subresource's headers. page is the
// document that asked for it and target is the resource; the pair supplies the
// Referer and Sec-Fetch-Site values a browser derives from the origin.
func browserChromeResource(h http.Header, dest, accept string, page, target *url.URL) {
	browserChromePageIdentity(h, page, target)
	browserClientHintsApply(h, target)
	if accept != "" {
		h.Set("Accept", accept)
	}
	h.Set("Sec-Fetch-Dest", dest)
	h.Set("Sec-Fetch-Mode", "no-cors")
	if site := browserFetchSite(page, target); site != "" {
		h.Set("Sec-Fetch-Site", site)
	}
	if ref := browserReferer(page, target); ref != "" {
		h.Set("Referer", ref)
	}
}

// browserChromeFetch sets the browser-vouched headers on a page script's fetch
// or XHR after hopHeaders has settled that hop's Origin and Referer.
func browserChromeFetch(h http.Header, mode string, page, target *url.URL) {
	browserChromePageIdentity(h, page, target)
	browserClientHintsApply(h, target)
	h.Set("Sec-Fetch-Dest", "empty")
	if mode == "" {
		mode = "cors"
	}
	h.Set("Sec-Fetch-Mode", mode)
	if site := browserFetchSite(page, target); site != "" {
		h.Set("Sec-Fetch-Site", site)
	}
}

// browserFetchSite is same-origin or cross-site. Chrome also has same-site,
// which needs a public-suffix lookup; calling a same-site request cross-site
// is the conservative answer and never claims a relationship that is absent.
func browserFetchSite(page, target *url.URL) string {
	if page == nil || target == nil || page.Host == "" || target.Host == "" {
		return ""
	}
	if page.Scheme == target.Scheme && strings.EqualFold(page.Host, target.Host) {
		return "same-origin"
	}
	return "cross-site"
}

// browserReferer follows Chrome's default strict-origin-when-cross-origin
// policy: the full page address to its own origin, only the origin elsewhere,
// and nothing from HTTPS down to HTTP.
func browserReferer(page, target *url.URL) string {
	if page == nil || target == nil || page.Host == "" {
		return ""
	}
	if page.Scheme == "https" && target.Scheme != "https" {
		return ""
	}
	if page.Scheme == target.Scheme && strings.EqualFold(page.Host, target.Host) {
		return page.String()
	}
	return page.Scheme + "://" + page.Host + "/"
}

// browserTransport is the document/subresource client transport. Cloning the
// default keeps its proxy, dialer and timeouts; ForceAttemptHTTP2 makes ALPN
// negotiate h2 on HTTPS exactly as it does for the default client. Go's
// transport speaks HTTP/1.1 and HTTP/2 (not HTTP/3), which is the protocol set
// this browser can honestly advertise.
func browserTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		tr := base.Clone()
		tr.ForceAttemptHTTP2 = true
		tr.TLSNextProto = nil
		return tr
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}
