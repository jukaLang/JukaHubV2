package main

// offlineaware_word_test.go — the offline classification matches whole words,
// and an online timeout is not reported as a missing Wi-Fi.

import (
	"errors"
	"testing"
)

// "ping" is the network tool, not a fragment of "mapping" or "typing": tiles
// that merely contain the letters stay available offline.
func TestOfflineReasonMatchesShortKeysAsWords(t *testing.T) {
	for _, name := range []string{"Ping", "ping tool", "change_scene:Ping", "Whois lookup", "RDAP"} {
		if offlineReasonFor(name) == "" {
			t.Errorf("%q was not recognised as a network tool", name)
		}
	}
	for _, name := range []string{"Button mapping", "Typing test", "Shopping list", "Sleeping mode", "Mapping"} {
		if reason := offlineReasonFor(name); reason != "" {
			t.Errorf("%q was greyed out offline (%q)", name, reason)
		}
	}
	for _, tc := range []struct {
		s, word string
		want    bool
	}{
		{"ping", "ping", true},
		{"a ping b", "ping", true},
		{"mapping", "ping", false},
		{"pinger", "ping", false},
		{"pingping ping", "ping", true},
		{"", "ping", false},
		{"ping", "", false},
	} {
		if got := offlineHasWord(tc.s, tc.word); got != tc.want {
			t.Errorf("offlineHasWord(%q, %q) = %v, want %v", tc.s, tc.word, got, tc.want)
		}
	}
}

// A timeout while online is a slow server, not a missing connection.
func TestPlainNetworkErrorOnlineTimeout(t *testing.T) {
	netMon.mu.Lock()
	online, checked := netMon.online, netMon.checked
	netMon.online, netMon.checked = true, true
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.online, netMon.checked = online, checked
		netMon.mu.Unlock()
	})
	for _, msg := range []string{
		"Get \"https://example.com\": dial tcp 1.2.3.4:443: i/o timeout",
		"net/http: TLS handshake timeout",
	} {
		if got := plainNetworkError(errors.New(msg)); got != offlineSlowNotice {
			t.Errorf("online %q = %q, want the slow-server notice", msg, got)
		}
	}
	if got := plainNetworkError(errors.New("dial tcp: lookup example.com: no such host")); got != offlinePlainNotice {
		t.Errorf("online resolver failure = %q, want the offline notice", got)
	}
}
