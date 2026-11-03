package main

// Tests for the four extra Tools rows. They check the parsers against captured
// output and the formatters, not the network: the live calls are one request or
// one table read on top of the code exercised here.

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseCloudflareTrace(t *testing.T) {
	body := "fl=123abc\nh=one.one.one.one\nip=203.0.113.9\nts=1700000000.000\nvisit_scheme=https\nuag=JukaHub\ncolo=SYD\nsliver=none\nhttp=http/2\nloc=AU\ntls=TLSv1.3\nsni=plaintext\nwarp=off\ngateway=off\n"
	info := parseCloudflareTrace(body)
	if info.IP != "203.0.113.9" || info.Country != "AU" || info.Colo != "SYD" {
		t.Fatalf("info = %+v, want the address, country and edge from the trace", info)
	}
	if info.TLS != "TLSv1.3" || info.HTTP != "http/2" {
		t.Fatalf("info = %+v, want the protocol fields", info)
	}
	text := formatPublicIP(info)
	for _, want := range []string{"Public IP: 203.0.113.9", "Country:   AU", "Edge:      SYD", "Protocol:  http/2 TLSv1.3"} {
		if !strings.Contains(text, want) {
			t.Fatalf("public IP text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "WARP") {
		t.Fatalf("public IP text mentions WARP when it is off:\n%s", text)
	}
}

func TestFormatPublicIPWarpIsCalledOut(t *testing.T) {
	text := formatPublicIP(publicIPInfo{IP: "198.51.100.4", Warp: "on"})
	if !strings.Contains(text, "WARP:      on") {
		t.Fatalf("text = %q, want the WARP note: an on tunnel means the address is the exit, not the line", text)
	}
}

func TestParseProcNetARP(t *testing.T) {
	body := `IP address       HW type     Flags       HW address            Mask     Device
192.168.1.1      0x1         0x2         a4:2b:8c:00:11:22     *        wlan0
192.168.1.9      0x1         0x0         00:00:00:00:00:00     *        wlan0
192.168.1.20     0x1         0x2         b8:27:eb:44:55:66     *        eth0
`
	list := parseProcNetARP(body)
	if len(list) != 2 {
		t.Fatalf("entries = %d (%+v), want the two resolved neighbours only", len(list), list)
	}
	if list[0].IP != "192.168.1.1" || list[0].MAC != "a4:2b:8c:00:11:22" || list[0].Device != "wlan0" {
		t.Fatalf("first = %+v, want the wlan0 neighbour", list[0])
	}
	if list[1].Device != "eth0" {
		t.Fatalf("second = %+v, want the eth0 neighbour", list[1])
	}
}

func TestParseWindowsARP(t *testing.T) {
	body := `Interface: 192.168.1.5 --- 0x5
  Internet Address      Physical Address      Type
  192.168.1.1           a4-2b-8c-00-11-22     dynamic
  192.168.1.20          b8-27-eb-44-55-66     static
  224.0.0.22            01-00-5e-00-00-16     static
`
	list := parseWindowsARP(body)
	if len(list) != 2 {
		t.Fatalf("entries = %d (%+v), want the two host rows; the multicast row is not a device", len(list), list)
	}
	if list[0].MAC != "a4:2b:8c:00:11:22" {
		t.Fatalf("mac = %q, want the dash form normalised to colons", list[0].MAC)
	}
	if list[1].State != "static" {
		t.Fatalf("state = %q, want static", list[1].State)
	}
}

func TestParseIPNeigh(t *testing.T) {
	body := `192.168.1.1 dev wlan0 lladdr a4:2b:8c:00:11:22 REACHABLE
192.168.1.7 dev wlan0  FAILED
192.168.1.20 dev eth0 lladdr b8:27:eb:44:55:66 STALE
`
	list := parseIPNeigh(body)
	if len(list) != 2 {
		t.Fatalf("entries = %d (%+v), want the two rows with a lladdr", len(list), list)
	}
	if list[0].Device != "wlan0" || list[0].State != "reachable" {
		t.Fatalf("first = %+v, want wlan0 reachable", list[0])
	}
	if list[1].MAC != "b8:27:eb:44:55:66" {
		t.Fatalf("second = %+v, want its MAC", list[1])
	}
}

func TestFormatNeighbours(t *testing.T) {
	if text := formatNeighbours(nil); !strings.Contains(text, "No neighbours yet") {
		t.Fatalf("empty table text = %q, want an explanation instead of a blank card", text)
	}
	list := []lanNeighbour{
		{IP: "192.168.1.20", MAC: "b8:27:eb:44:55:66", Device: "eth0"},
		{IP: "192.168.1.1", MAC: "a4:2b:8c:00:11:22", Device: "wlan0"},
	}
	sortNeighbours(list)
	if list[0].IP != "192.168.1.1" {
		t.Fatalf("sorted = %+v, want numeric address order", list)
	}
	text := formatNeighbours(list)
	for _, want := range []string{"192.168.1.1", "a4:2b:8c:00:11:22", "wlan0", "2 devices"} {
		if !strings.Contains(text, want) {
			t.Fatalf("neighbour text missing %q:\n%s", want, text)
		}
	}
}

func TestDecodeProcNetAddr(t *testing.T) {
	cases := map[string]string{
		"00000000":                         "0.0.0.0",
		"0100007F":                         "127.0.0.1",
		"0101A8C0":                         "192.168.1.1",
		"00000000000000000000000001000000": "::1",
		"00000000000000000000000000000000": "::",
	}
	for hexAddr, want := range cases {
		if got := decodeProcNetAddr(hexAddr); got != want {
			t.Fatalf("decodeProcNetAddr(%q) = %q, want %q", hexAddr, got, want)
		}
	}
	if decodeProcNetAddr("zz") != "" {
		t.Fatal("a malformed address should decode to empty, not to a plausible address")
	}
}

func TestParseProcNetTCPKeepsOnlyListeners(t *testing.T) {
	body := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1000 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1001 1 0000000000000000 100 0 0 10 0
   2: 00000000:0016 0100007F:9C40 01 00000000:00000000 00:00000000 00000000     0        0 1002 1 0000000000000000 100 0 0 10 0
`
	list := parseProcNetTCP(body)
	if len(list) != 2 {
		t.Fatalf("listeners = %d (%+v), want the two LISTEN rows", len(list), list)
	}
	if list[0].Port != 22 || list[0].Addr != "0.0.0.0" {
		t.Fatalf("first = %+v, want ssh on all interfaces", list[0])
	}
	if list[1].Port != 8080 || list[1].Addr != "127.0.0.1" {
		t.Fatalf("second = %+v, want the loopback listener", list[1])
	}
}

func TestParseNetstatListen(t *testing.T) {
	body := `Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1234
  TCP    127.0.0.1:5939         0.0.0.0:0              LISTENING       5678
  TCP    192.168.1.5:52344      93.184.216.34:443      ESTABLISHED     9012
  UDP    0.0.0.0:500            *:*                                    4321
`
	list := parseNetstatListen(body)
	if len(list) != 2 {
		t.Fatalf("listeners = %d (%+v), want the two LISTENING TCP rows", len(list), list)
	}
	if list[0].Port != 135 || list[0].PID != "1234" {
		t.Fatalf("first = %+v, want port 135 owned by 1234", list[0])
	}
	if list[1].Addr != "127.0.0.1" {
		t.Fatalf("second = %+v, want the loopback bind address", list[1])
	}
}

func TestFormatListeningPorts(t *testing.T) {
	if text := formatListeningPorts(nil); !strings.Contains(text, "Nothing is listening") {
		t.Fatalf("empty text = %q, want an explanation", text)
	}
	list := []listeningPort{
		{Port: 22, Addr: "0.0.0.0"},
		{Port: 22, Addr: "::"},
		{Port: 8080, Addr: "127.0.0.1", PID: "4242"},
	}
	text := formatListeningPorts(list)
	if strings.Count(text, "\n22 ") != 1 {
		t.Fatalf("port 22 appears more than once, want the v4/v6 pair collapsed:\n%s", text)
	}
	if !strings.Contains(text, "0.0.0.0, ::") {
		t.Fatalf("text missing both bind addresses:\n%s", text)
	}
	if !strings.Contains(text, "2 TCP ports listening") {
		t.Fatalf("text missing the count:\n%s", text)
	}
	if !strings.Contains(text, "4242") {
		t.Fatalf("text missing the PID:\n%s", text)
	}
}

func TestNetToolsParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"example.com", "https://example.com"},
		{"https://example.com/a?b=c", "https://example.com/a?b=c"},
		{"http://192.168.1.7:8080/roms", "http://192.168.1.7:8080/roms"},
		{"  example.com  ", "https://example.com"},
	}
	for _, c := range cases {
		parsed, err := netToolsParseURL(c.in)
		if err != nil {
			t.Fatalf("netToolsParseURL(%q) error: %v", c.in, err)
		}
		if parsed.String() != c.want {
			t.Fatalf("netToolsParseURL(%q) = %q, want %q", c.in, parsed.String(), c.want)
		}
	}
	for _, bad := range []string{"", "   ", "exa mple.com", "ftp://example.com", "https://", "file:///etc/passwd"} {
		if _, err := netToolsParseURL(bad); err == nil {
			t.Fatalf("netToolsParseURL(%q) accepted a URL the row must refuse", bad)
		}
	}
}

func TestFormatHTTPCheck(t *testing.T) {
	target, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	final, _ := url.Parse("https://www.example.com/")
	header := http.Header{}
	header.Set("Server", "cloudflare")
	header.Set("Content-Type", "text/html; charset=UTF-8")
	cert := &x509.Certificate{
		Issuer:   pkix.Name{Organization: []string{"Let's Encrypt"}},
		Subject:  pkix.Name{CommonName: "example.com"},
		NotAfter: time.Now().AddDate(0, 0, 45),
	}
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	text := formatHTTPCheck(target, final, "200 OK", 200, 214*time.Millisecond, header, 1331, state)
	for _, want := range []string{
		"Check:  https://example.com",
		"Status: 200 OK",
		"Time:   214 ms",
		"Final:  https://www.example.com/",
		"Server: cloudflare",
		"Type:   text/html",
		"TLS:    Let's Encrypt · example.com",
		"45 days left",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("HTTP check text missing %q:\n%s", want, text)
		}
	}
}

func TestFormatHTTPCheckErrorStatus(t *testing.T) {
	target, _ := url.Parse("https://example.com/missing")
	text := formatHTTPCheck(target, target, "404 Not Found", 404, time.Second, http.Header{}, 0, nil)
	if !strings.Contains(text, "error status") {
		t.Fatalf("text = %q, want the 404 called out as an error", text)
	}
	if strings.Contains(text, "TLS:") {
		t.Fatalf("text mentions TLS without a certificate:\n%s", text)
	}
}

func TestFormatCertificateExpired(t *testing.T) {
	cert := &x509.Certificate{
		Subject:  pkix.Name{CommonName: "old.example.com"},
		NotAfter: time.Now().AddDate(0, 0, -3),
	}
	text := formatCertificate(cert, time.Now())
	if !strings.Contains(text, "expired 3 days ago") {
		t.Fatalf("text = %q, want the expiry stated in the past", text)
	}
}

func TestHTTPCheckAgainstLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/roms" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Server", "juka-test")
		w.Write([]byte("hello from the box"))
	}))
	defer srv.Close()

	text, err := httpCheckSummary(srv.URL + "/roms")
	if err != nil {
		t.Fatalf("check failed against a live local server: %v", err)
	}
	for _, want := range []string{"Status: 200 OK", "Server: juka-test", "Type:   text/plain", "Size:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("check text missing %q:\n%s", want, text)
		}
	}

	missing, err := httpCheckSummary(srv.URL + "/nope")
	if err != nil {
		t.Fatalf("404 check returned an error instead of a result: %v", err)
	}
	if !strings.Contains(missing, "Status: 404 Not Found") || !strings.Contains(missing, "error status") {
		t.Fatalf("404 text = %q, want the status and the error note", missing)
	}
}

func TestNormaliseMACDropsGroupAddresses(t *testing.T) {
	for _, group := range []string{"01:00:5e:00:00:16", "01-00-5e-00-00-16", "ff:ff:ff:ff:ff:ff", "33:33:00:00:00:01"} {
		if got := normaliseMAC(group); got != "" {
			t.Fatalf("normaliseMAC(%q) = %q, want a group address dropped", group, got)
		}
	}
	for in, want := range map[string]string{
		"A4-2B-8C-00-11-22": "a4:2b:8c:00:11:22",
		"a4:2b:8c:00:11:22": "a4:2b:8c:00:11:22",
		"00:00:00:00:00:00": "",
	} {
		if got := normaliseMAC(in); got != want {
			t.Fatalf("normaliseMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatListeningPortsUnknownPID(t *testing.T) {
	text := formatListeningPorts([]listeningPort{{Port: 61629, Addr: "127.0.0.1", PID: "0"}})
	if strings.Contains(text, "  0\n") {
		t.Fatalf("text shows a zero PID, which is the system, not a process:\n%s", text)
	}
	if !strings.Contains(text, "61629   127.0.0.1               -") {
		t.Fatalf("text = %q, want the drag-free dash for an unknown owner", text)
	}
}
