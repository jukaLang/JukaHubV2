package main

// browserpsl.go — the cookie jar's public-suffix list.
//
// net/http/cookiejar asks a PublicSuffixList which domains no site may set a
// cookie for. The jar was built with none (cookiejar.New(nil)), and then the
// only rule left is "the cookie's domain must be a suffix of the host": a page
// on evil.com could set a cookie for every .com site, one on any .co.uk host a
// cookie for all of co.uk — and the browser keeps cookies on the card, so such
// a supercookie (or a planted session) outlived the visit. The full list lives
// in golang.org/x/net/publicsuffix, which this tree does not vendor, so this is
// the conservative core of it: every top-level domain is a public suffix, and
// so are the common second-level registries and the shared hosting domains
// where strangers' sites sit side by side.

import "strings"

// browserKnownSuffixes are the multi-label public suffixes the jar knows.
var browserKnownSuffixes = func() map[string]bool {
	m := map[string]bool{}
	add := func(tld string, seconds ...string) {
		for _, s := range seconds {
			m[s+"."+tld] = true
		}
	}
	add("uk", "co", "org", "me", "ltd", "plc", "net", "sch", "ac", "gov", "nhs", "police")
	add("au", "com", "net", "org", "edu", "gov", "asn", "id")
	add("nz", "co", "net", "org", "govt", "ac", "school", "geek", "gen", "kiwi")
	add("jp", "co", "ne", "or", "ac", "ad", "ed", "go", "gr", "lg")
	add("kr", "co", "ne", "or", "re", "pe", "go", "ac")
	add("cn", "com", "net", "org", "gov", "edu", "ac")
	add("hk", "com", "net", "org", "edu", "gov", "idv")
	add("tw", "com", "net", "org", "edu", "gov", "idv")
	add("sg", "com", "net", "org", "edu", "gov")
	add("my", "com", "net", "org", "edu", "gov")
	add("id", "co", "or", "web", "ac", "go", "sch", "net", "my")
	add("th", "co", "in", "or", "ac", "go", "net")
	add("in", "co", "net", "org", "firm", "gen", "ind", "ac", "edu", "res", "gov")
	add("br", "com", "net", "org", "gov", "edu")
	add("mx", "com", "net", "org", "gob", "edu")
	add("ar", "com", "net", "org", "gob")
	add("co", "com", "net", "org")
	add("za", "co", "org", "net", "gov", "edu", "ac")
	add("tr", "com", "net", "org", "gov", "edu", "gen", "biz", "info", "k12")
	add("ua", "com", "net", "org", "gov", "edu", "in")
	add("il", "co", "org", "net", "ac", "gov", "muni")
	add("es", "com", "nom", "org", "gob", "edu")
	add("pl", "com", "net", "org", "biz", "info")
	for _, tld := range []string{"eg", "sa", "pk", "ng", "ph", "vn", "pe", "ve", "uy", "ec", "bd", "np", "lk", "qa", "kw"} {
		add(tld, "com", "net", "org", "edu", "gov")
	}
	// Shared hosting: every customer's site is a sibling under these.
	for _, s := range []string{
		"github.io", "gitlab.io", "herokuapp.com", "appspot.com", "blogspot.com",
		"cloudfront.net", "netlify.app", "vercel.app", "pages.dev", "workers.dev",
		"web.app", "firebaseapp.com", "azurewebsites.net", "s3.amazonaws.com",
		"glitch.me", "onrender.com", "fly.dev", "duckdns.org", "ddns.net",
		"no-ip.org", "dyndns.org", "ngrok.io", "ngrok-free.app", "neocities.org",
	} {
		m[s] = true
	}
	return m
}()

// browserPublicSuffixes implements cookiejar.PublicSuffixList.
type browserPublicSuffixes struct{}

// PublicSuffix returns the longest known public suffix of domain, or its
// top-level label when none is known.
func (browserPublicSuffixes) PublicSuffix(domain string) string {
	domain = strings.TrimSuffix(strings.ToLower(domain), ".")
	labels := strings.Split(domain, ".")
	for n := 3; n >= 2; n-- {
		if len(labels) >= n {
			if suffix := strings.Join(labels[len(labels)-n:], "."); browserKnownSuffixes[suffix] {
				return suffix
			}
		}
	}
	return labels[len(labels)-1]
}

func (browserPublicSuffixes) String() string { return "jukahub built-in public suffixes" }
