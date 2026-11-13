package main

// nettools2.go — four more probes for Tools → Network tools: a single-port
// check, a domain registration lookup, a clock check against NTP, and this
// device's own subnet plan.
//
// They answer the follow-up questions the first probes leave: "is this one port
// open", "who owns this domain", "is the handheld's clock right" (a wrong clock
// breaks TLS), and "what range is this device on". Everything is read-only;
// the parsers are split from the platform calls so they can be tested against
// captured data.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ── Single-port check ──────────────────────────────────────────────────────

// netPortParseTarget accepts "host:port", "[v6]:port" and a bare host followed
// by a port. A missing port is refused rather than defaulted: guessing 443 for
// an FTP server would report "closed" with confidence.
func netPortParseTarget(raw string) (string, int, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", 0, errors.New("type host:port first")
	}
	host, portText, err := net.SplitHostPort(text)
	if err != nil {
		return "", 0, errors.New("type host:port, for example 192.168.1.7:22")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("the port must be a number from 1 to 65535")
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return "", 0, errors.New("that target has no host in it")
	}
	if _, err := powerDNSParseQuery(host); err != nil {
		return "", 0, errors.New("that host name does not look right")
	}
	return host, port, nil
}

// portCheckText is the text one check produces. A refused port is a result, not
// an error: it says the host answered and nothing is listening.
func portCheckText(host string, port int, addr string, open bool, latency time.Duration, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Target:  %s:%d\n", host, port)
	if addr != "" && addr != host {
		fmt.Fprintf(&b, "Address: %s\n", addr)
	}
	if open {
		fmt.Fprintf(&b, "Port:    %d · open\n", port)
		fmt.Fprintf(&b, "Time:    %d ms\n", latency.Milliseconds())
		b.WriteString("\nRead-only: one TCP connect, closed immediately after it succeeded.")
	} else {
		fmt.Fprintf(&b, "Port:    %d · closed\n", port)
		if reason != "" {
			fmt.Fprintf(&b, "Reason:  %s\n", reason)
		}
		b.WriteString("\nThe host was reachable enough to answer, or the connect timed out at 4s.")
	}
	return b.String()
}

// portCheckRun dials one port within ctx and reports the text and whether the
// port answered.
func portCheckRun(ctx context.Context, raw string) (string, bool, error) {
	host, port, err := netPortParseTarget(raw)
	if err != nil {
		return "", false, err
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := net.Dialer{Timeout: 4 * time.Second}
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	elapsed := time.Since(start)
	if err != nil {
		reason := err.Error()
		switch {
		case strings.Contains(reason, "refused"):
			reason = "connection refused — nothing is listening on that port"
		case strings.Contains(reason, "timeout"), strings.Contains(reason, "i/o timeout"):
			reason = "no answer within 4s — filtered, or the host is down"
		case strings.Contains(reason, "no such host"):
			reason = "that name does not resolve"
		}
		return portCheckText(host, port, "", false, elapsed, reason), false, nil
	}
	resolved := ""
	if remote, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		resolved = remote.IP.String()
	}
	conn.Close()
	return portCheckText(host, port, resolved, true, elapsed, ""), true, nil
}

// ── Domain registration lookup ─────────────────────────────────────────────

// rdapEvent is one dated event in an RDAP answer.
type rdapEvent struct {
	Action string `json:"eventAction"`
	Date   string `json:"eventDate"`
}

// rdapDomain is the subset of the RDAP answer the row shows. RDAP is the plain
// JSON replacement for port-43 whois, so no extra protocol is needed.
type rdapDomain struct {
	LDHName     string      `json:"ldhName"`
	Handle      string      `json:"handle"`
	Status      []string    `json:"status"`
	Events      []rdapEvent `json:"events"`
	Nameservers []struct {
		LDHName string `json:"ldhName"`
	} `json:"nameservers"`
	Entities []rdapEntity `json:"entities"`
}

type rdapEntity struct {
	Roles      []string          `json:"roles"`
	VCardArray []json.RawMessage `json:"vcardArray"`
	Entities   []rdapEntity      `json:"entities"`
}

// netParseDomain accepts a domain name typed with or without a scheme, and
// refuses anything that is an address or a path: RDAP answers about names, not
// about hosts.
func netParseDomain(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", errors.New("type a domain first")
	}
	if strings.Contains(text, "://") {
		parsed, err := url.Parse(text)
		if err != nil || parsed.Host == "" {
			return "", errors.New("that link has no domain in it")
		}
		text = parsed.Hostname()
	}
	text = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(text), "."))
	if net.ParseIP(text) != nil {
		return "", errors.New("RDAP looks up names, not addresses")
	}
	if !strings.Contains(text, ".") || strings.ContainsAny(text, " /\\@") {
		return "", errors.New("type a full domain, for example example.com")
	}
	for _, r := range text {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			continue
		}
		return "", errors.New("that does not look like a domain name")
	}
	return text, nil
}

// rdapRegistrar walks the entity tree for the registrar's display name. The
// vCard is an array of [name, parameters, type, value] entries, with "fn" the
// formatted name.
func rdapRegistrar(entities []rdapEntity) string {
	for _, entity := range entities {
		for _, role := range entity.Roles {
			if role != "registrar" {
				continue
			}
			if name := rdapVCardName(entity.VCardArray); name != "" {
				return name
			}
			if name := rdapRegistrar(entity.Entities); name != "" {
				return name
			}
		}
	}
	return ""
}

// rdapVCardName pulls the "fn" value out of a vCard array.
func rdapVCardName(vcard []json.RawMessage) string {
	if len(vcard) < 2 {
		return ""
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(vcard[1], &entries); err != nil {
		return ""
	}
	for _, raw := range entries {
		var entry []json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil || len(entry) < 4 {
			continue
		}
		var name string
		if err := json.Unmarshal(entry[0], &name); err != nil || name != "fn" {
			continue
		}
		var value string
		if err := json.Unmarshal(entry[3], &value); err != nil {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// rdapEventDate picks one event by action and formats its date.
func rdapEventDate(events []rdapEvent, action string) string {
	for _, event := range events {
		if !strings.EqualFold(event.Action, action) {
			continue
		}
		if stamp, err := time.Parse(time.RFC3339, event.Date); err == nil {
			return stamp.Format("2006-01-02")
		}
		return event.Date
	}
	return ""
}

// whoisText formats the answer.
func whoisText(domain string, info rdapDomain, registrar string) string {
	var b strings.Builder
	name := info.LDHName
	if name == "" {
		name = domain
	}
	fmt.Fprintf(&b, "Domain:    %s\n", name)
	if registrar != "" {
		fmt.Fprintf(&b, "Registrar: %s\n", registrar)
	}
	if registered := rdapEventDate(info.Events, "registration"); registered != "" {
		fmt.Fprintf(&b, "Created:   %s\n", registered)
	}
	if expires := rdapEventDate(info.Events, "expiration"); expires != "" {
		fmt.Fprintf(&b, "Expires:   %s\n", expires)
		if stamp, err := time.Parse("2006-01-02", expires); err == nil {
			days := int(time.Until(stamp).Hours() / 24)
			if days >= 0 {
				fmt.Fprintf(&b, "           %d day%s left\n", days, pluralS(days))
			} else {
				fmt.Fprintf(&b, "           expired %d day%s ago\n", -days, pluralS(-days))
			}
		}
	}
	if changed := rdapEventDate(info.Events, "last changed"); changed != "" {
		fmt.Fprintf(&b, "Changed:   %s\n", changed)
	}
	if len(info.Nameservers) > 0 {
		names := make([]string, 0, len(info.Nameservers))
		for _, ns := range info.Nameservers {
			if ns.LDHName != "" {
				names = append(names, strings.ToLower(ns.LDHName))
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "Name servers: %s\n", strings.Join(names, ", "))
		}
	}
	if len(info.Status) > 0 {
		fmt.Fprintf(&b, "Status:    %s\n", strings.Join(info.Status, ", "))
	}
	b.WriteString("\nRead-only: one RDAP query to the registry's public answer.")
	return b.String()
}

// whoisQuery asks RDAP for a domain within ctx and returns the parsed answer.
func whoisQuery(ctx context.Context, raw string) (string, rdapDomain, error) {
	domain, err := netParseDomain(raw)
	if err != nil {
		return "", rdapDomain{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://rdap.org/domain/"+url.PathEscape(domain), nil)
	if err != nil {
		return "", rdapDomain{}, err
	}
	req.Header.Set("Accept", "application/rdap+json")
	resp, err := netToolsHTTPClient().Do(req)
	if err != nil {
		return "", rdapDomain{}, fmt.Errorf("no answer from RDAP: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", rdapDomain{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", rdapDomain{}, fmt.Errorf("no registration is published for %s", domain)
	}
	if resp.StatusCode != http.StatusOK {
		return "", rdapDomain{}, fmt.Errorf("RDAP answered %s", resp.Status)
	}
	var info rdapDomain
	if err := json.Unmarshal(body, &info); err != nil {
		return "", rdapDomain{}, fmt.Errorf("RDAP answer could not be read: %w", err)
	}
	return domain, info, nil
}

// ── Clock check ────────────────────────────────────────────────────────────

// ntpEpochOffset is the number of seconds between 1900 (NTP's epoch) and 1970.
const ntpEpochOffset = 2208988800

// clockText formats the comparison.
func clockText(device, server time.Time, offset, rtt time.Duration, stratum int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Device:  %s\n", device.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Server:  %s\n", server.Format("2006-01-02 15:04:05"))
	ms := offset.Milliseconds()
	sign := "+"
	if ms < 0 {
		sign = ""
	}
	fmt.Fprintf(&b, "Offset:  %s%d ms\n", sign, ms)
	switch {
	case offset > 2*time.Second:
		b.WriteString("         the device clock is behind; TLS checks fail when it is far off\n")
	case offset < -2*time.Second:
		b.WriteString("         the device clock is ahead; TLS checks fail when it is far off\n")
	default:
		b.WriteString("         in sync: within two seconds of the server\n")
	}
	fmt.Fprintf(&b, "Trip:    %d ms round trip\n", rtt.Milliseconds())
	if stratum > 0 {
		fmt.Fprintf(&b, "Stratum: %d\n", stratum)
	}
	b.WriteString("\nRead-only: one SNTP request (pool.ntp.org, or the next server when it\n" +
		"does not answer). Settings → General keeps the clock right.")
	return b.String()
}

// clockReading is one SNTP comparison.
type clockReading struct {
	device, server time.Time
	offset, rtt    time.Duration
	stratum        int
}

// clockMeasure runs one SNTP exchange within ctx, with the client the network
// time sync uses (ntpsync.go): the first of its servers that answers, and the
// offset from all four timestamps rather than the reply's send time alone.
// "Device" is this device's clock in the middle of the exchange that answered
// (not of the whole search, which may have waited on servers that did not).
// The result is also what Settings → Date & time shows as the last check —
// on a PC, the only check JukaHub makes, and only on request.
func clockMeasure(ctx context.Context) (clockReading, error) {
	r, server, err := sntpQueryAny(ctx)
	if err != nil {
		return clockReading{}, fmt.Errorf("no NTP server answered: %w", err)
	}
	clockNoteCheck(clockCheck{At: time.Now(), Offset: r.Offset, Source: server})
	return clockReading{
		device:  r.Local,
		server:  r.Local.Add(r.Offset),
		offset:  r.Offset,
		rtt:     r.Delay,
		stratum: r.Stratum,
	}, nil
}

// ── Subnet plan ────────────────────────────────────────────────────────────

// subnetText lays out the range this device sits on. It is arithmetic on the
// address and mask, so it is testable without a network.
func subnetText(iface string, ip net.IP, mask net.IPMask) (string, error) {
	ip4 := ip.To4()
	if ip4 == nil {
		return "", errors.New("this row needs an IPv4 address")
	}
	ones, bits := mask.Size()
	if bits != 32 {
		return "", errors.New("that mask is not an IPv4 mask")
	}
	network := ip4.Mask(mask)
	broadcast := make(net.IP, len(network))
	for i := range network {
		broadcast[i] = network[i] | ^mask[i]
	}
	var first, last string
	hosts := 0
	if ones <= 30 {
		firstIP := make(net.IP, len(network))
		copy(firstIP, network)
		firstIP[len(firstIP)-1]++
		lastIP := make(net.IP, len(broadcast))
		copy(lastIP, broadcast)
		lastIP[len(lastIP)-1]--
		first, last = firstIP.String(), lastIP.String()
		hosts = 1<<(32-ones) - 2
	}
	var b strings.Builder
	if iface != "" {
		fmt.Fprintf(&b, "Interface: %s\n", iface)
	}
	fmt.Fprintf(&b, "Address:   %s/%d\n", ip4.String(), ones)
	fmt.Fprintf(&b, "Mask:      %s\n", net.IP(mask).String())
	fmt.Fprintf(&b, "Network:   %s\n", network.String())
	fmt.Fprintf(&b, "Broadcast: %s\n", broadcast.String())
	if hosts > 0 {
		fmt.Fprintf(&b, "Usable:    %s – %s\n", first, last)
		fmt.Fprintf(&b, "Hosts:     %d\n", hosts)
	} else if ones == 31 {
		b.WriteString("Usable:    point-to-point (/31): both addresses are hosts\n")
	} else {
		b.WriteString("Usable:    none (/32): a single address\n")
	}
	scope := "public"
	if ip4.IsPrivate() {
		scope = "private (RFC 1918)"
	} else if ip4.IsLinkLocalUnicast() {
		scope = "link-local (APIPA): no DHCP answer"
	}
	fmt.Fprintf(&b, "Scope:     %s\n", scope)
	b.WriteString("\nRead-only: computed from this device's own address and mask.")
	return b.String(), nil
}

// subnetInterface is the first up, non-loopback interface with an IPv4
// address, and that address with its mask.
func subnetInterface() (string, *net.IPNet, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", nil, err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			return iface.Name, ipnet, nil
		}
	}
	return "", nil, errors.New("no IPv4 address on this device: connect Wi-Fi first")
}

// ── The keyboard's targets ─────────────────────────────────────────────────

// netPromptKind says which tool asked for text on the keyboard.
type netPromptKind int

const (
	netPromptPort netPromptKind = iota
	netPromptWhois
	// Cert, Banner and Headers were Tools rows. The banner read is gone and the
	// certificate and header checks moved to the Browser's site information;
	// the kinds stay because the keyboard's title and seed tables still name
	// them, and nothing opens the keyboard with them any more.
	netPromptCert
	netPromptBanner
	netPromptHeaders
	netPromptCodec
)

// netPromptBuffer is the keyboard's working copy; the remembered values below
// change only on submit.
var (
	netPromptBuffer string
	netPrompt       = netPromptPort
	netLastPort     string
	netLastDomain   string
	netLastCert     string
	netLastBanner   string
	netLastHeaders  string
	netLastCodec    string
)

// netPromptSubmit validates what the keyboard submitted for the tool that
// opened it and runs the tool on the Network tools page. Text that does not
// parse reopens the keyboard with it still there, so a typo is an edit.
func netPromptSubmit(raw string) {
	value := strings.TrimSpace(raw)
	switch netPrompt {
	case netPromptPort:
		if _, _, err := netPortParseTarget(raw); err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			vkOpenNetPrompt(netPromptPort, raw)
			return
		}
		netLastPort = value
		netToolsSubmit("portcheck", value)
	case netPromptWhois:
		if _, err := netParseDomain(raw); err != nil {
			ShowToast(err.Error(), ToastKindWarning)
			vkOpenNetPrompt(netPromptWhois, raw)
			return
		}
		netLastDomain = value
		netToolsSubmit("whois", value)
	case netPromptCodec:
		if value == "" {
			ShowToast("Type or paste a string first", ToastKindWarning)
			vkOpenNetPrompt(netPromptCodec, raw)
			return
		}
		netLastCodec = value
		netToolsSubmit("codec", value)
	}
}
