package main

// Tests for the Tools DNS lookup: what the typed text is allowed to be, how
// each record type is rendered, and the keyboard path that starts a lookup.
// The resolver is stubbed in every test, so nothing here touches the network.

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
)

// stubDNSResolver replaces every resolver call for one test.
func stubDNSResolver(t *testing.T, stub func() (host string, kind string)) {
	t.Helper()
	prevIP, prevCNAME := powerDNSLookupIP, powerDNSLookupCNAME
	prevMX, prevNS := powerDNSLookupMX, powerDNSLookupNS
	prevTXT, prevAddr := powerDNSLookupTXT, powerDNSLookupAddr
	t.Cleanup(func() {
		powerDNSLookupIP, powerDNSLookupCNAME = prevIP, prevCNAME
		powerDNSLookupMX, powerDNSLookupNS = prevMX, prevNS
		powerDNSLookupTXT, powerDNSLookupAddr = prevTXT, prevAddr
	})

	notFound := func() error { return &net.DNSError{Err: "no such host", Name: "example", IsNotFound: true} }
	kindOf := func() string {
		_, kind := stub()
		return kind
	}
	hostOf := func() string {
		host, _ := stub()
		return host
	}

	powerDNSLookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		if hostOf() != host {
			return nil, fmt.Errorf("lookup for %q, want %q", host, hostOf())
		}
		switch kindOf() {
		case "addresses", "records":
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("2606:2800:220:1:248:1893:25c8:1946")}, nil
		case "empty":
			return nil, nil
		default:
			return nil, notFound()
		}
	}
	powerDNSLookupCNAME = func(ctx context.Context, host string) (string, error) {
		if kindOf() == "cname" {
			return "edge.example.net.", nil
		}
		return host + ".", nil
	}
	powerDNSLookupMX = func(ctx context.Context, host string) ([]*net.MX, error) {
		if kindOf() != "records" {
			return nil, notFound()
		}
		return []*net.MX{{Host: "alt2.example.net.", Pref: 20}, {Host: "alt1.example.net.", Pref: 10}}, nil
	}
	powerDNSLookupNS = func(ctx context.Context, host string) ([]*net.NS, error) {
		if kindOf() != "records" {
			return nil, notFound()
		}
		return []*net.NS{{Host: "ns2.example.net."}, {Host: "ns1.example.net."}}, nil
	}
	powerDNSLookupTXT = func(ctx context.Context, host string) ([]string, error) {
		if kindOf() != "records" {
			return nil, notFound()
		}
		return []string{"v=spf1 -all"}, nil
	}
	powerDNSLookupAddr = func(ctx context.Context, addr string) ([]string, error) {
		if kindOf() != "reverse" {
			return nil, notFound()
		}
		return []string{"dns.google."}, nil
	}
}

func TestPowerDNSParseQueryAcceptsHostsAndAddresses(t *testing.T) {
	cases := map[string]string{
		"github.com":                 "github.com",
		"  github.com  ":             "github.com",
		"github.com.":                "github.com",
		"https://github.com/a/b?c":   "github.com",
		"http://user@github.com:443": "github.com",
		"github.com:443":             "github.com",
		"[2606:4700::1111]":          "2606:4700::1111",
		"8.8.8.8":                    "8.8.8.8",
		"_dmarc.example.com":         "_dmarc.example.com",
	}
	for in, want := range cases {
		q, err := powerDNSParseQuery(in)
		if err != nil {
			t.Errorf("powerDNSParseQuery(%q) = %v, want %q", in, err, want)
			continue
		}
		if q.Host != want {
			t.Errorf("powerDNSParseQuery(%q).Host = %q, want %q", in, q.Host, want)
		}
		wantIP := net.ParseIP(want) != nil
		if (q.IP != nil) != wantIP {
			t.Errorf("powerDNSParseQuery(%q) treated the input as an IP: %v", in, q.IP != nil)
		}
	}

	for _, bad := range []string{"", "   ", "github.com/status", "https://", "two words", "host\tname", "..", "exam_ple..com", strings.Repeat("a", 64) + ".com"} {
		if q, err := powerDNSParseQuery(bad); err == nil {
			t.Errorf("powerDNSParseQuery(%q) = %+v, want a refusal", bad, q)
		}
	}
}

func TestPowerDNSReportListsEveryRecordType(t *testing.T) {
	stubDNSResolver(t, func() (string, string) { return "example.com", "records" })
	text := powerDNSReport(powerDNSQuery{Host: "example.com"})

	for _, want := range []string{
		"Lookup: example.com",
		"A     93.184.216.34",
		"AAAA  2606:2800:220:1:248:1893:25c8:1946",
		"MX    10 alt1.example.net",
		"MX    20 alt2.example.net",
		"NS    ns1.example.net",
		"NS    ns2.example.net",
		"TXT   \"v=spf1 -all\"",
		"system resolver",
		"Read-only",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report is missing %q:\n%s", want, text)
		}
	}
	// IPv4 comes before IPv6, and MX rows are ordered by preference.
	if strings.Index(text, "A     93.") > strings.Index(text, "AAAA  ") {
		t.Errorf("address families are out of order:\n%s", text)
	}
	if strings.Index(text, "10 alt1") > strings.Index(text, "20 alt2") {
		t.Errorf("MX rows are not sorted by preference:\n%s", text)
	}
	if strings.Contains(text, "CNAME") {
		t.Errorf("a host that is its own canonical name reported a CNAME:\n%s", text)
	}
}

func TestPowerDNSReportShowsAliasAndReversesIPs(t *testing.T) {
	stubDNSResolver(t, func() (string, string) { return "www.example.com", "cname" })
	text := powerDNSReport(powerDNSQuery{Host: "www.example.com"})
	if !strings.Contains(text, "CNAME  edge.example.net") {
		t.Errorf("alias not reported:\n%s", text)
	}

	stubDNSResolver(t, func() (string, string) { return "8.8.8.8", "reverse" })
	text = powerDNSReport(powerDNSQuery{Host: "8.8.8.8", IP: net.ParseIP("8.8.8.8")})
	if !strings.Contains(text, "Reverse lookup: 8.8.8.8") || !strings.Contains(text, "PTR   dns.google") {
		t.Errorf("reverse lookup not reported:\n%s", text)
	}
	if strings.Contains(text, "CNAME") || strings.Contains(text, "MX") {
		t.Errorf("a reverse lookup asked for forward records:\n%s", text)
	}
}

// A null MX (a root host) is the domain saying it takes no mail; the row must
// say so rather than print "0 ".
func TestPowerDNSMXValue(t *testing.T) {
	if got := powerDNSMXValue(&net.MX{Pref: 10, Host: "mail.example.com."}); got != "10 mail.example.com" {
		t.Errorf("MX = %q", got)
	}
	if got := powerDNSMXValue(&net.MX{Pref: 0, Host: "."}); !strings.Contains(got, "no mail accepted") {
		t.Errorf("null MX = %q, want it explained", got)
	}
	if got := powerDNSMXValue(nil); got != "" {
		t.Errorf("nil MX = %q", got)
	}
}

// A host with no address record still reports the types that failed instead of
// printing an error and nothing else, and a name that resolves keeps its
// addresses even when every other type is missing.
func TestPowerDNSReportSeparatesMissingTypes(t *testing.T) {
	stubDNSResolver(t, func() (string, string) { return "gone.example", "missing" })
	text := powerDNSReport(powerDNSQuery{Host: "gone.example"})
	if !strings.Contains(text, "addresses: no such record") {
		t.Errorf("missing address record not explained:\n%s", text)
	}
	if !strings.Contains(text, "No records returned.") {
		t.Errorf("empty report did not say so:\n%s", text)
	}

	stubDNSResolver(t, func() (string, string) { return "example.com", "addresses" })
	text = powerDNSReport(powerDNSQuery{Host: "example.com"})
	if !strings.Contains(text, "A     93.184.216.34") {
		t.Errorf("addresses were lost when the other types failed:\n%s", text)
	}
	if strings.Contains(text, "addresses:") {
		t.Errorf("report blamed the addresses for another type's failure:\n%s", text)
	}
}

// The report is bounded: a TXT-heavy domain cannot flood the output pane.
func TestPowerDNSReportCapsRecordTypes(t *testing.T) {
	stubDNSResolver(t, func() (string, string) { return "example.com", "addresses" })
	prevTXT := powerDNSLookupTXT
	powerDNSLookupTXT = func(ctx context.Context, host string) ([]string, error) {
		out := make([]string, 0, powerDNSValueCap+3)
		for i := 0; i < powerDNSValueCap+3; i++ {
			out = append(out, fmt.Sprintf("txt-%d", i))
		}
		return out, nil
	}
	t.Cleanup(func() { powerDNSLookupTXT = prevTXT })

	text := powerDNSReport(powerDNSQuery{Host: "example.com"})
	if got := strings.Count(text, `"txt-`); got != powerDNSValueCap {
		t.Errorf("printed %d TXT records, want the cap of %d:\n%s", got, powerDNSValueCap, text)
	}
	if !strings.Contains(text, "… 3 more TXT records") {
		t.Errorf("hidden TXT records were not counted:\n%s", text)
	}
}

// The Tools row asks for a host on the keyboard and then runs the lookup for
// what was typed, which is the whole point of the change.
func TestDNSLookupRunsTheTypedHost(t *testing.T) {
	newProbeFrame(t)
	resetKeyboardState(t)
	stubDNSResolver(t, func() (string, string) { return "example.org", "records" })

	prevHost := powerDNSLastHost
	prevOpen := powerTools.open
	t.Cleanup(func() {
		waitForPowerAsync(t)
		powerDNSLastHost = prevHost
		powerTools.open = prevOpen
	})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	powerDNSLastHost = ""
	vkOpenDNSQuery("")
	if !virtualKeyboardActive || vkTarget != vkTargetDNS {
		t.Fatal("the DNS row did not open its keyboard")
	}
	if got := vkCurrentText(); got != "github.com" {
		t.Fatalf("the field was seeded with %q, want the example host", got)
	}
	if vkTitle() != "DNS LOOKUP" {
		t.Errorf("keyboard title = %q", vkTitle())
	}

	vkSetText("example.org", config)
	vkSubmit(config, false)
	if virtualKeyboardActive {
		t.Fatal("submitting did not close the keyboard")
	}
	if powerDNSLastHost != "example.org" {
		t.Fatalf("remembered host = %q", powerDNSLastHost)
	}
	waitForPowerAsync(t)
	name, text, _, _ := powerOutputSnapshot()
	if !strings.Contains(name, "example.org") {
		t.Errorf("output title = %q, want the typed host", name)
	}
	if !strings.Contains(text, "A     93.184.216.34") {
		t.Errorf("output is missing the resolved address:\n%s", text)
	}
	if got := powerDNSRowHost(); got != "example.org" {
		t.Errorf("the Tools row shows %q, want the host just looked up", got)
	}
}

// A typo must not be thrown away: the row puts it back in the field.
func TestDNSLookupKeepsATypoInTheField(t *testing.T) {
	newProbeFrame(t)
	resetKeyboardState(t)
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	prevHost := powerDNSLastHost
	t.Cleanup(func() { powerDNSLastHost = prevHost })
	powerDNSLastHost = ""

	vkOpenDNSQuery("")
	vkSetText("github.com/status", config)
	vkSubmit(config, false)
	if !virtualKeyboardActive || vkTarget != vkTargetDNS {
		t.Fatal("a refused query should reopen the keyboard")
	}
	if got := vkCurrentText(); got != "github.com/status" {
		t.Fatalf("field = %q, want the refused text kept for correction", got)
	}
	if powerDNSLastHost != "" {
		t.Errorf("a refused query was remembered as %q", powerDNSLastHost)
	}
	vkCloseKeyboard()
}

// The keyboard target must not disturb the ones around it.
func TestDNSKeyboardTargetIsIsolated(t *testing.T) {
	resetKeyboardState(t)
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	vkOpenDNSQuery("example.com")
	if got := toolArgsBuffer; got != "" {
		t.Errorf("tool argument buffer = %q", got)
	}
	vkSetText("8.8.8.8", config)
	if toolArgsBuffer != "" || filesInputBuffer != "" || podcastFeedInput != "" {
		t.Error("the DNS field wrote into another keyboard buffer")
	}
	if vkCurrentText() != "8.8.8.8" {
		t.Errorf("field = %q", vkCurrentText())
	}
	vkCloseKeyboard()
	if dnsQueryBuffer != "" {
		t.Errorf("closing left the host behind: %q", dnsQueryBuffer)
	}
	if !strings.Contains(vkPlaceholder(), "8.8.8.8") {
		t.Errorf("placeholder = %q, want it to name an IP example", vkPlaceholder())
	}
}
