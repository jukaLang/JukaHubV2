package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The Internet browser presents one coherent desktop Chrome identity. The
// User-Agent is reduced the way Chrome reduces it: the platform and engine are
// frozen, and only the major version changes. The low-entropy client hints are
// the headers Chrome sends before a site asks for anything more.
//
// Keep these values in step: a UA saying one version beside a hint saying
// another is a stronger fingerprint than either header alone.
const (
	browserChromeMajor     = "157"
	browserChromeUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + browserChromeMajor + ".0.0.0 Safari/537.36"
	browserChromeBrands    = `"Not-A.Brand";v="99", "Google Chrome";v="` + browserChromeMajor + `", "Chromium";v="` + browserChromeMajor + `"`
)

const (
	browserAcceptDocument = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"
	browserAcceptStyle    = "text/css,*/*;q=0.1"
	browserAcceptImage    = "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8"
	browserAcceptScript   = "*/*"
)

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

// browserChromeResource sets a document subresource's headers. page is the
// document that asked for it and target is the resource; the pair supplies the
// Referer and Sec-Fetch-Site values a browser derives from the origin.
func browserChromeResource(h http.Header, dest, accept string, page, target *url.URL) {
	browserChromeIdentity(h)
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
	browserChromeIdentity(h)
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
	if strings.EqualFold(page.Host, target.Host) {
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
	if strings.EqualFold(page.Host, target.Host) {
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
