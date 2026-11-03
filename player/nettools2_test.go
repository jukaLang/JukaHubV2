package main

// Tests for the second set of Tools rows: the port check, the RDAP lookup, the
// NTP comparison and the subnet arithmetic. The parsers are fed captured data;
// the two network rows run against a local server so the result formatting is
// exercised without a live internet connection.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNetPortParseTarget(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
	}{
		{"192.168.1.7:22", "192.168.1.7", 22},
		{"example.com:443", "example.com", 443},
		{"[::1]:8080", "::1", 8080},
		{"  example.com:80  ", "example.com", 80},
	}
	for _, c := range cases {
		host, port, err := netPortParseTarget(c.in)
		if err != nil {
			t.Fatalf("netPortParseTarget(%q) error: %v", c.in, err)
		}
		if host != c.host || port != c.port {
			t.Fatalf("netPortParseTarget(%q) = %q/%d, want %q/%d", c.in, host, port, c.host, c.port)
		}
	}
	bad := []string{"", "   ", "example.com", "example.com:0", "example.com:70000", "example.com:http", ":22", "exa mple.com:22"}
	for _, in := range bad {
		if _, _, err := netPortParseTarget(in); err == nil {
			t.Fatalf("netPortParseTarget(%q) accepted a target the row must refuse", in)
		}
	}
}

func TestPortCheckText(t *testing.T) {
	open := portCheckText("example.com", 443, "93.184.216.34", true, 42*time.Millisecond, "")
	for _, want := range []string{"Target:  example.com:443", "Address: 93.184.216.34", "443 · open", "Time:    42 ms"} {
		if !strings.Contains(open, want) {
			t.Fatalf("open text missing %q:\n%s", want, open)
		}
	}
	closed := portCheckText("192.168.1.7", 22, "", false, time.Second, "connection refused — nothing is listening")
	if !strings.Contains(closed, "22 · closed") || !strings.Contains(closed, "nothing is listening") {
		t.Fatalf("closed text = %q, want the port and the reason", closed)
	}
	if strings.Contains(closed, "Time:") {
		t.Fatalf("closed text shows a timing that only means something for a completed connect:\n%s", closed)
	}
}

func TestPortCheckSummaryAgainstLocalListener(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, portText, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	text, err := portCheckSummary(net.JoinHostPort(host, portText))
	if err != nil {
		t.Fatalf("check failed against a live listener: %v", err)
	}
	if !strings.Contains(text, "open") {
		t.Fatalf("text = %q, want the port reported open", text)
	}

	// The same address after the listener closed must report closed, not error.
	closedAddr := net.JoinHostPort(host, portText)
	srv.Close()
	text, err = portCheckSummary(closedAddr)
	if err != nil {
		t.Fatalf("a closed port returned an error instead of a result: %v", err)
	}
	if !strings.Contains(text, "closed") {
		t.Fatalf("text = %q, want the port reported closed", text)
	}
}

func TestNetParseDomain(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":               "example.com",
		"Example.COM.":              "example.com",
		"https://example.com/x?y=1": "example.com",
		"http://sub.example.co.uk":  "sub.example.co.uk",
	} {
		got, err := netParseDomain(in)
		if err != nil || got != want {
			t.Fatalf("netParseDomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "localhost", "1.1.1.1", "exa mple.com", "user@example.com", "example.com/path"} {
		if _, err := netParseDomain(bad); err == nil {
			t.Fatalf("netParseDomain(%q) accepted something RDAP cannot look up", bad)
		}
	}
}

const rdapFixture = `{
  "ldhName": "EXAMPLE.COM",
  "handle": "2336799_DOMAIN_COM-VRSN",
  "status": ["client transfer prohibited"],
  "events": [
    {"eventAction": "registration", "eventDate": "1995-08-14T04:00:00Z"},
    {"eventAction": "expiration", "eventDate": "2026-08-13T04:00:00Z"},
    {"eventAction": "last changed", "eventDate": "2025-08-14T07:01:38Z"}
  ],
  "nameservers": [{"ldhName": "A.IANA-SERVERS.NET"}, {"ldhName": "B.IANA-SERVERS.NET"}],
  "entities": [
    {"roles": ["registrar"], "vcardArray": ["vcard", [["version", {}, "text", "4.0"], ["fn", {}, "text", "RESERVED-Internet Assigned Numbers Authority"]]]}
  ]
}`

func TestRDAPRegistrar(t *testing.T) {
	var info rdapDomain
	if err := json.Unmarshal([]byte(rdapFixture), &info); err != nil {
		t.Fatal(err)
	}
	if got := rdapRegistrar(info.Entities); got != "RESERVED-Internet Assigned Numbers Authority" {
		t.Fatalf("registrar = %q, want the vCard fn of the registrar entity", got)
	}
	text := whoisText("example.com", info, rdapRegistrar(info.Entities))
	for _, want := range []string{
		"Domain:    EXAMPLE.COM",
		"Registrar: RESERVED-Internet Assigned Numbers Authority",
		"Created:   1995-08-14",
		"Expires:   2026-08-13",
		"Name servers: a.iana-servers.net, b.iana-servers.net",
		"Status:    client transfer prohibited",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("whois text missing %q:\n%s", want, text)
		}
	}
}

func TestRDAPRegistrarNested(t *testing.T) {
	var info rdapDomain
	if err := json.Unmarshal([]byte(`{"entities":[{"roles":["registrar"],"entities":[{"roles":["registrar"],"vcardArray":["vcard",[["fn",{},"text","Nested Registrar"]]]}]}]}`), &info); err != nil {
		t.Fatal(err)
	}
	if got := rdapRegistrar(info.Entities); got != "Nested Registrar" {
		t.Fatalf("registrar = %q, want the nested entity's name", got)
	}
	if got := rdapRegistrar(nil); got != "" {
		t.Fatalf("registrar = %q, want empty for an answer with no registrar", got)
	}
}

func TestWhoisTextWithoutEvents(t *testing.T) {
	text := whoisText("fresh.example", rdapDomain{}, "")
	if !strings.Contains(text, "Domain:    fresh.example") {
		t.Fatalf("text = %q, want the typed domain used when the answer has no name", text)
	}
	if strings.Contains(text, "Registrar:") || strings.Contains(text, "Expires:") {
		t.Fatalf("text invents fields the answer did not have:\n%s", text)
	}
}

func TestClockText(t *testing.T) {
	now := time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)
	behind := clockText(now, now.Add(4*time.Second), 4*time.Second, 30*time.Millisecond, 2)
	if !strings.Contains(behind, "Offset:  +4000 ms") || !strings.Contains(behind, "behind") {
		t.Fatalf("behind text = %q, want a positive offset and the warning", behind)
	}
	ahead := clockText(now, now.Add(-4*time.Second), -4*time.Second, 30*time.Millisecond, 2)
	if !strings.Contains(ahead, "Offset:  -4000 ms") || !strings.Contains(ahead, "ahead") {
		t.Fatalf("ahead text = %q, want a negative offset and the warning", ahead)
	}
	synced := clockText(now, now.Add(120*time.Millisecond), 120*time.Millisecond, 20*time.Millisecond, 3)
	if !strings.Contains(synced, "in sync") {
		t.Fatalf("synced text = %q, want the in-sync line", synced)
	}
}

func TestNTPPacket(t *testing.T) {
	packet := netNTPRequest()
	if len(packet) != 48 {
		t.Fatalf("packet is %d bytes, want the 48-byte SNTP request", len(packet))
	}
	if packet[0]&0x7 != 3 || packet[0]>>3&0x7 != 4 {
		t.Fatalf("packet[0] = %#02x, want mode 3 (client) and version 4", packet[0])
	}

	// A transmit timestamp of 2026-11-02T00:00:00Z, with a half-second fraction.
	reply := make([]byte, 48)
	reply[1] = 2
	seconds := uint32(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC).Unix() + ntpEpochOffset)
	reply[40] = byte(seconds >> 24)
	reply[41] = byte(seconds >> 16)
	reply[42] = byte(seconds >> 8)
	reply[43] = byte(seconds)
	reply[44] = 0x80
	stamp, ok := netNTPTimestamp(reply, 40)
	if !ok {
		t.Fatal("timestamp was not read")
	}
	if got := stamp.UTC().Format("2006-01-02 15:04:05"); got != "2026-11-02 00:00:00" {
		t.Fatalf("timestamp = %q, want the encoded time", got)
	}
	if netNTPStratum(reply) != 2 {
		t.Fatalf("stratum = %d, want 2", netNTPStratum(reply))
	}
	empty := make([]byte, 48)
	if _, ok := netNTPTimestamp(empty, 40); ok {
		t.Fatal("an all-zero timestamp was accepted; that is the kiss-of-death shape")
	}
}

func TestSubnetText(t *testing.T) {
	ip := net.ParseIP("192.168.1.111")
	mask := net.CIDRMask(24, 32)
	text, err := subnetText("wlan0", ip, mask)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Interface: wlan0",
		"Address:   192.168.1.111/24",
		"Mask:      255.255.255.0",
		"Network:   192.168.1.0",
		"Broadcast: 192.168.1.255",
		"Usable:    192.168.1.1 – 192.168.1.254",
		"Hosts:     254",
		"private (RFC 1918)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("subnet text missing %q:\n%s", want, text)
		}
	}

	slash31, err := subnetText("", net.ParseIP("10.0.0.1"), net.CIDRMask(31, 32))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(slash31, "point-to-point (/31)") {
		t.Fatalf("/31 text = %q, want the point-to-point note", slash31)
	}
	slash32, err := subnetText("", net.ParseIP("8.8.8.8"), net.CIDRMask(32, 32))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(slash32, "none (/32)") || !strings.Contains(slash32, "Scope:     public") {
		t.Fatalf("/32 text = %q, want a single-address note and the public scope", slash32)
	}

	if _, err := subnetText("", net.ParseIP("fe80::1"), net.CIDRMask(64, 128)); err == nil {
		t.Fatal("an IPv6 address was accepted by the IPv4 subnet row")
	}
}

func TestNetPromptKeyboardTarget(t *testing.T) {
	resetKeyboardState(t)
	netPrompt = netPromptPort
	netPromptBuffer = "192.168.1.7:22"
	vkTarget = vkTargetNetwork
	if got := vkTitle(); got != "PORT CHECK" {
		t.Fatalf("title = %q, want PORT CHECK for the port row", got)
	}
	if got := vkPlaceholder(); got != "192.168.1.7:22" {
		t.Fatalf("placeholder = %q, want a host:port example", got)
	}
	if got := vkCurrentText(); got != "192.168.1.7:22" {
		t.Fatalf("current text = %q, want the buffer", got)
	}

	netPrompt = netPromptWhois
	if got := vkTitle(); got != "WHOIS LOOKUP" {
		t.Fatalf("title = %q, want WHOIS LOOKUP for the domain row", got)
	}
	if got := vkPlaceholder(); got != "example.com" {
		t.Fatalf("placeholder = %q, want a domain example", got)
	}
	resetKeyboardState(t)
}
