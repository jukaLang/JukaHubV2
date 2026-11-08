package main

// offlineaware.go — what still works with no network, said before the press.
//
// The handheld is usually started before it has joined WiFi, and several screens
// only work with internet: Live TV, Podcasts, Shorts, Tickers, video search and
// the internet diagnostics. Opening one of those offline used to be allowed, and
// the user found out from a raw dial error ("lookup api.github.com: no such
// host") halfway down a screen. This file answers the question up front:
//
//   - offlineReasonFor classifies a scene, tool or tile by name, so a menu can
//     draw it dimmed with an "Offline" tag and a press can say which part needs
//     the connection.
//   - offlineSceneBlock is the single gate every navigation goes through.
//   - plainNetworkError turns resolver and dial failures into a sentence a person
//     can act on.
//
// What is deliberately NOT classified: JukaLand (a local Go game), the Browser
// (cached pages and local files still render), the music player, files, notes,
// games and settings (all local), and FTP/SSH/companion/netplay/cast, which only
// need the LAN — IsOnline() probes the internet, so greying those on a LAN-only
// network would be a lie.

import (
	"errors"
	"net"
	"strings"
)

const (
	// offlinePlainNotice is what a network failure says instead of the resolver
	// error. It names the fix, not the syscall.
	offlinePlainNotice = "No internet connection — connect WiFi and try again."
	// offlineSlowNotice is a timeout while the device is online.
	offlineSlowNotice = "The server did not answer in time — try again in a moment."
	// offlineTileTag is the small badge drawn on a dimmed tile.
	offlineTileTag = "Offline"
)

// offlineReasonFor maps a scene name, tool label or trigger to why it needs a
// connection, or "" when it works offline. The match is on a lower-cased key so
// a config author's spelling ("Live TV", "livetv", "IPTV") all land together.
func offlineReasonFor(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return ""
	}
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(k, w) {
				return true
			}
		}
		return false
	}
	// A short key only counts as a whole word: "ping" inside "Button mapping",
	// "Typing test" or "Sleeping" is not the network tool, and a substring
	// match greyed those tiles out offline.
	hasWord := func(words ...string) bool {
		for _, w := range words {
			if offlineHasWord(k, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("livetv", "live tv", "live-tv", "iptv", "ip stream", "ipstream", "live stream", "livestream"):
		return "Offline — Live TV needs WiFi"
	case has("podcast"):
		return "Offline — Podcasts need WiFi"
	case has("shorts"):
		return "Offline — Shorts need WiFi"
	case has("ticker"):
		return "Offline — Tickers need WiFi"
	// The Media screen itself ("Tube") is not here: offline it opens on the
	// downloads that play without a connection (media_hub.go). YouTube search
	// tiles and the Trending shortcut still need the network.
	case has("youtube", "trending", "media search", "video search", "search results"):
		return "Offline — Video search needs WiFi"
	case has("speed test", "speedtest", "bandwidth test"):
		return "Offline — The speed test needs WiFi"
	case hasWord("ping", "rdap", "whois") || has("traceroute", "port check", "port scan", "dns lookup",
		"network tools", "net tools", "internet check"):
		return "Offline — Network tools need WiFi"
	}
	return ""
}

// offlineHasWord reports whether word occurs in s with no letter or digit
// directly before or after it.
func offlineHasWord(s, word string) bool {
	if word == "" {
		return false
	}
	isWordByte := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}
	for from := 0; from <= len(s)-len(word); {
		i := strings.Index(s[from:], word)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(word)
		if (start == 0 || !isWordByte(s[start-1])) && (end == len(s) || !isWordByte(s[end])) {
			return true
		}
		from = start + 1
	}
	return false
}

// offlineSceneBlock reports whether navigating to name must be refused right
// now, with the sentence to show. It is the only entry point the navigation code
// needs, so a new screen is one keyword away from being offline-aware.
func offlineSceneBlock(name string) (string, bool) {
	if IsOnline() {
		return "", false
	}
	reason := offlineReasonFor(name)
	return reason, reason != ""
}

// offlineTileBlocked is offlineSceneBlock for a menu tile: the reason can come
// from the label, the trigger, or the scene it opens.
func offlineTileBlocked(elem Element) (string, bool) {
	if IsOnline() {
		return "", false
	}
	key := elem.Text + " " + elem.Trigger + " " + elem.TriggerTarget
	if target, ok := strings.CutPrefix(elem.Trigger, "change_scene:"); ok {
		key += " " + target
	}
	if reason := offlineReasonFor(key); reason != "" {
		return reason, true
	}
	return "", false
}

// plainNetworkError rewrites a network failure for the screen: a resolver or
// route failure is "connect WiFi", and anything else keeps its own one line
// rather than a stack of syscall detail.
func plainNetworkError(err error) string {
	if err == nil {
		return ""
	}
	if !IsOnline() {
		return offlinePlainNotice
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return offlinePlainNotice
	}
	lower := strings.ToLower(err.Error())
	for _, needle := range []string{
		"no such host",
		"temporary failure in name resolution",
		"server misbehaving",
		"network is unreachable",
		"no route to host",
		"dial tcp: lookup",
	} {
		if strings.Contains(lower, needle) {
			return offlinePlainNotice
		}
	}
	// Online, a timeout is a slow or unreachable server, not a missing Wi-Fi:
	// telling the user to connect a Wi-Fi they are on is the wrong fix.
	var netErr net.Error
	if (errors.As(err, &netErr) && netErr.Timeout()) || strings.Contains(lower, "i/o timeout") ||
		strings.Contains(lower, "tls handshake timeout") || strings.Contains(lower, "connection timed out") {
		return offlineSlowNotice
	}
	return firstLine(err.Error())
}
