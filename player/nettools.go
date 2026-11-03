package main

// nettools.go — the extra network rows in Tools → FTP & IP.
//
// The panel already answered "is this thing on the network?" with ping,
// traceroute, port scans and DNS. These four answer the questions that came
// next in practice:
//
//   - Public IP:  what the internet sees, and which edge it lands on.
//   - LAN neighbours: the devices the router has already talked to, read from
//     the kernel's neighbour table without probing anything.
//   - Listening ports: what this device itself has open, which is the first
//     thing to check when a phone cannot reach the FTP or SSH server.
//   - HTTP check: status, time, server and certificate expiry for one URL.
//
// Everything is read-only. The parsers are separate from the platform calls so
// they can be tested against captured output, the same way the Wi-Fi and netsh
// parsers are.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Public IP ──────────────────────────────────────────────────────────────

// publicIPInfo is one answer from Cloudflare's plain-text trace endpoint.
type publicIPInfo struct {
	IP      string
	Country string
	Colo    string
	TLS     string
	HTTP    string
	Warp    string
}

// netToolsTraceURL answers with key=value lines and needs no key of its own. It
// is the same host the status bar's connectivity probe uses.
const netToolsTraceURL = "https://one.one.one.one/cdn-cgi/trace"

// netToolsHTTPClient is shared by the two rows that speak HTTP. The timeout is
// the row's whole budget: a probe that hangs must not leave the output pane
// saying "Checking…" forever.
func netToolsHTTPClient() *http.Client {
	return &http.Client{Timeout: 12 * time.Second}
}

// parseCloudflareTrace reads the trace body. Only the fields shown are kept;
// the endpoint also reports gateway and warp policy details.
func parseCloudflareTrace(body string) publicIPInfo {
	var info publicIPInfo
	for _, line := range strings.Split(body, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "ip":
			info.IP = value
		case "loc":
			info.Country = value
		case "colo":
			info.Colo = value
		case "tls":
			info.TLS = value
		case "http":
			info.HTTP = value
		case "warp":
			info.Warp = value
		}
	}
	return info
}

// publicIPSummary fetches the trace and formats it. A network failure is
// reported with the reason rather than as an empty card.
func publicIPSummary() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, netToolsTraceURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := netToolsHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("no answer from %s: %w", "the trace service", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("trace service answered %s", resp.Status)
	}
	info := parseCloudflareTrace(string(body))
	if info.IP == "" {
		return "", errors.New("the trace service did not report an address")
	}
	return formatPublicIP(info), nil
}

// formatPublicIP is the text the output pane shows.
func formatPublicIP(info publicIPInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Public IP: %s\n", info.IP)
	if info.Country != "" {
		fmt.Fprintf(&b, "Country:   %s\n", info.Country)
	}
	if info.Colo != "" {
		fmt.Fprintf(&b, "Edge:      %s\n", info.Colo)
	}
	if info.TLS != "" || info.HTTP != "" {
		fmt.Fprintf(&b, "Protocol:  %s %s\n", info.HTTP, info.TLS)
	}
	if info.Warp == "on" {
		b.WriteString("WARP:      on — this is the tunnel's exit address\n")
	}
	b.WriteString("\nRead-only: one HTTPS request to Cloudflare's trace endpoint.")
	return strings.TrimRight(b.String(), "\n")
}

// ── LAN neighbours ─────────────────────────────────────────────────────────

// lanNeighbour is one entry of the kernel's or the OS's ARP table.
type lanNeighbour struct {
	IP     string
	MAC    string
	Device string
	State  string
}

// normaliseMAC makes the Windows dash form and the Linux colon form compare
// equal, and drops the addresses that are not devices. The all-zero entry is a
// failed lookup; an address with the group bit set in its first octet is a
// multicast or broadcast address, and Windows lists several of those (224.0.0.x,
// the subnet broadcast) as if they were hosts.
func normaliseMAC(mac string) string {
	mac = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(mac), "-", ":"))
	if mac == "" || mac == "00:00:00:00:00:00" || mac == "<incomplete>" {
		return ""
	}
	if len(mac) >= 2 {
		if first, err := strconv.ParseUint(mac[:2], 16, 8); err == nil && first&1 == 1 {
			return ""
		}
	}
	return mac
}

// parseProcNetARP reads /proc/net/arp. The header is skipped and entries with
// no hardware address (flag 0x0) are ignored: they are a failed lookup, not a
// device.
func parseProcNetARP(text string) []lanNeighbour {
	var out []lanNeighbour
	for i, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if i == 0 || len(fields) < 6 {
			continue
		}
		if net.ParseIP(fields[0]) == nil {
			continue
		}
		if strings.EqualFold(fields[2], "0x0") {
			continue
		}
		mac := normaliseMAC(fields[3])
		if mac == "" {
			continue
		}
		device := fields[5]
		if device == "*" {
			device = ""
		}
		out = append(out, lanNeighbour{IP: fields[0], MAC: mac, Device: device})
	}
	return out
}

// parseIPNeigh reads `ip neigh show`, which is the fallback when /proc/net/arp
// is absent.
func parseIPNeigh(text string) []lanNeighbour {
	var out []lanNeighbour
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || net.ParseIP(fields[0]) == nil {
			continue
		}
		entry := lanNeighbour{IP: fields[0]}
		for i := 1; i < len(fields); i++ {
			switch fields[i] {
			case "dev":
				if i+1 < len(fields) {
					entry.Device = fields[i+1]
					i++
				}
			case "lladdr":
				if i+1 < len(fields) {
					entry.MAC = normaliseMAC(fields[i+1])
					i++
				}
			default:
				entry.State = strings.ToLower(fields[i])
			}
		}
		if entry.MAC != "" {
			out = append(out, entry)
		}
	}
	return out
}

// parseWindowsARP reads `arp -a`, which prints one line per device under an
// interface heading.
func parseWindowsARP(text string) []lanNeighbour {
	var out []lanNeighbour
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || net.ParseIP(fields[0]) == nil {
			continue
		}
		mac := normaliseMAC(fields[1])
		if mac == "" {
			continue
		}
		kind := ""
		if len(fields) >= 3 {
			kind = strings.ToLower(fields[2])
		}
		out = append(out, lanNeighbour{IP: fields[0], MAC: mac, State: kind})
	}
	return out
}

// sortNeighbours orders the table by IPv4 address, then by name, so two reads
// of the same network read the same.
func sortNeighbours(list []lanNeighbour) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := net.ParseIP(list[i].IP), net.ParseIP(list[j].IP)
		if a != nil && b != nil {
			av, bv := a.To4(), b.To4()
			if av != nil && bv != nil && av.String() != bv.String() {
				return string(av) < string(bv)
			}
			return a.String() < b.String()
		}
		return list[i].IP < list[j].IP
	})
}

// formatNeighbours is the text the row shows. An empty table is a fact worth
// stating: nothing has been talked to yet, which is normal right after boot.
func formatNeighbours(list []lanNeighbour) string {
	if len(list) == 0 {
		return "No neighbours yet.\n\nThe table fills in as this device talks to the router and anything else on the LAN."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-15s  %-17s  %s\n", "IP", "MAC", "INTERFACE")
	for _, n := range list {
		device := n.Device
		if device == "" {
			device = n.State
		}
		fmt.Fprintf(&b, "%-15s  %-17s  %s\n", n.IP, n.MAC, device)
	}
	fmt.Fprintf(&b, "\n%d device%s · read-only: the neighbour table only, nothing is probed",
		len(list), pluralS(len(list)))
	return b.String()
}

// lanNeighbourSummary reads the platform's table. Reading it costs nothing and
// touches no radio: it is what the router conversation already left behind.
func lanNeighbourSummary() (string, error) {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("arp", "-a").Output()
		if err != nil {
			return "", fmt.Errorf("arp -a: %w", err)
		}
		list := parseWindowsARP(string(out))
		sortNeighbours(list)
		return formatNeighbours(list), nil
	}
	if data, err := os.ReadFile("/proc/net/arp"); err == nil {
		list := parseProcNetARP(string(data))
		sortNeighbours(list)
		return formatNeighbours(list), nil
	}
	out, err := exec.Command("ip", "neigh", "show").Output()
	if err != nil {
		return "", errors.New("no neighbour table on this device (no /proc/net/arp and no ip command)")
	}
	list := parseIPNeigh(string(out))
	sortNeighbours(list)
	return formatNeighbours(list), nil
}

// ── Listening ports ────────────────────────────────────────────────────────

// listeningPort is one socket in LISTEN state on this device.
type listeningPort struct {
	Port int
	Addr string
	PID  string
}

// decodeProcNetAddr turns /proc/net/tcp's hex address into a printable one.
// Each 32-bit word is little-endian, so an IPv4 address is the four bytes in
// reverse order and an IPv6 address is four reversed words.
func decodeProcNetAddr(hexAddr string) string {
	switch len(hexAddr) {
	case 8:
		v, err := strconv.ParseUint(hexAddr, 16, 32)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("%d.%d.%d.%d", byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	case 32:
		var raw [16]byte
		for word := 0; word < 4; word++ {
			v, err := strconv.ParseUint(hexAddr[word*8:(word+1)*8], 16, 32)
			if err != nil {
				return ""
			}
			raw[word*4+0] = byte(v)
			raw[word*4+1] = byte(v >> 8)
			raw[word*4+2] = byte(v >> 16)
			raw[word*4+3] = byte(v >> 24)
		}
		ip := net.IP(raw[:])
		if ip.IsUnspecified() {
			return "::"
		}
		return ip.String()
	}
	return ""
}

// parseProcNetTCP reads one of /proc/net/tcp or /proc/net/tcp6 and keeps the
// LISTEN rows (state 0A).
func parseProcNetTCP(text string) []listeningPort {
	var out []listeningPort
	for i, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if i == 0 || len(fields) < 4 {
			continue
		}
		if !strings.EqualFold(fields[3], "0A") {
			continue
		}
		addr, portHex, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		port64, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		ip := decodeProcNetAddr(addr)
		if ip == "" {
			continue
		}
		out = append(out, listeningPort{Port: int(port64), Addr: ip})
	}
	return out
}

// parseNetstatListen reads `netstat -ano`. UDP rows and non-listening rows are
// dropped; the PID is the last column.
func parseNetstatListen(text string) []listeningPort {
	var out []listeningPort
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		listening := false
		for _, f := range fields {
			if strings.EqualFold(f, "LISTENING") {
				listening = true
				break
			}
		}
		if !listening {
			continue
		}
		host, portText, err := net.SplitHostPort(fields[1])
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			continue
		}
		out = append(out, listeningPort{Port: port, Addr: host, PID: fields[len(fields)-1]})
	}
	return out
}

// dedupeListening collapses the same port bound on several addresses (the
// usual v4 and v6 pair) into one row that names both.
func dedupeListening(list []listeningPort) []listeningPort {
	byPort := map[int]*listeningPort{}
	var ports []int
	for _, p := range list {
		existing, ok := byPort[p.Port]
		if !ok {
			copy := p
			byPort[p.Port] = &copy
			ports = append(ports, p.Port)
			continue
		}
		if existing.Addr != p.Addr && !strings.Contains(existing.Addr, p.Addr) {
			existing.Addr += ", " + p.Addr
		}
		if existing.PID == "" {
			existing.PID = p.PID
		}
	}
	sort.Ints(ports)
	out := make([]listeningPort, 0, len(ports))
	for _, port := range ports {
		out = append(out, *byPort[port])
	}
	return out
}

// formatListeningPorts is the text the row shows.
func formatListeningPorts(list []listeningPort) string {
	list = dedupeListening(list)
	if len(list) == 0 {
		return "Nothing is listening.\n\nNo TCP socket on this device is waiting for a connection."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-6s  %-22s  %s\n", "PORT", "BOUND TO", "PID")
	for _, p := range list {
		// Windows reports 0 for sockets the system holds rather than a
		// process, which reads as a PID that cannot be looked up.
		pid := p.PID
		if pid == "" || pid == "0" {
			pid = "-"
		}
		fmt.Fprintf(&b, "%-6d  %-22s  %s\n", p.Port, p.Addr, pid)
	}
	fmt.Fprintf(&b, "\n%d TCP port%s listening · read-only: read from the socket table",
		len(list), pluralS(len(list)))
	return b.String()
}

// listeningPortsSummary reads this device's socket table.
func listeningPortsSummary() (string, error) {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
		if err != nil {
			return "", fmt.Errorf("netstat: %w", err)
		}
		return formatListeningPorts(parseNetstatListen(string(out))), nil
	}
	var list []listeningPort
	read := false
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		read = true
		list = append(list, parseProcNetTCP(string(data))...)
	}
	if !read {
		out, err := exec.Command("netstat", "-ltn").Output()
		if err != nil {
			return "", errors.New("no socket table on this device (no /proc/net/tcp and no netstat)")
		}
		return formatListeningPorts(parseNetstatListen(string(out))), nil
	}
	return formatListeningPorts(list), nil
}

// ── HTTP check ─────────────────────────────────────────────────────────────

// netToolsParseURL accepts what people type: a full URL, a bare host, or a
// host with a path. Anything that is not http(s) is refused, so the row can
// never fetch a file:// path.
func netToolsParseURL(raw string) (*url.URL, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, errors.New("type a URL or host first")
	}
	if strings.ContainsAny(text, " \t\r\n") {
		return nil, errors.New("URLs cannot contain spaces")
	}
	if !strings.Contains(text, "://") {
		text = "https://" + text
	}
	parsed, err := url.Parse(text)
	if err != nil {
		return nil, errors.New("that does not look like a URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("only http:// and https:// are checked")
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("that URL has no host in it")
	}
	return parsed, nil
}

// httpCheckBodyLimit bounds what is read to measure the response. The row is a
// status check, not a downloader.
const httpCheckBodyLimit = 512 << 10

// formatHTTPCheck is the text the row shows: status, timing, where it ended
// up, the server's own description and the certificate's expiry.
func formatHTTPCheck(target *url.URL, final *url.URL, status string, code int, elapsed time.Duration, header http.Header, size int64, conn *tls.ConnectionState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Check:  %s\n", target.String())
	fmt.Fprintf(&b, "Status: %s\n", status)
	fmt.Fprintf(&b, "Time:   %d ms\n", elapsed.Milliseconds())
	if final != nil && final.String() != target.String() {
		fmt.Fprintf(&b, "Final:  %s\n", final.String())
	}
	if server := header.Get("Server"); server != "" {
		fmt.Fprintf(&b, "Server: %s\n", server)
	}
	if ctype := header.Get("Content-Type"); ctype != "" {
		fmt.Fprintf(&b, "Type:   %s\n", ctype)
	}
	if size > 0 {
		fmt.Fprintf(&b, "Size:   %s\n", formatBytes(uint64(size)))
	}
	if conn != nil && len(conn.PeerCertificates) > 0 {
		b.WriteString(formatCertificate(conn.PeerCertificates[0], time.Now()))
	}
	if code >= 400 {
		b.WriteString("\nThe server answered, but with an error status.")
	} else {
		b.WriteString("\nRead-only: one request; the body is read only far enough to measure it.")
	}
	return b.String()
}

// httpCheckSummary performs the check. A network failure is an error with the
// reason, so the pane never shows a half-filled result as success.
func httpCheckSummary(raw string) (string, error) {
	target, err := netToolsParseURL(raw)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "JukaHub-Tools/1.0")
	start := time.Now()
	resp, err := netToolsHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("no answer: %w", err)
	}
	defer resp.Body.Close()
	size, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, httpCheckBodyLimit))
	elapsed := time.Since(start)
	if resp.ContentLength > 0 {
		size = resp.ContentLength
	}
	return formatHTTPCheck(target, resp.Request.URL, resp.Status, resp.StatusCode, elapsed, resp.Header, size, resp.TLS), nil
}

// formatCertificate describes the certificate that answered: who issued it,
// which name it covers and when it expires. An expired or nearly expired
// certificate is the reason a "working" site stops loading.
func formatCertificate(cert *x509.Certificate, now time.Time) string {
	if cert == nil {
		return ""
	}
	issuer := ""
	if len(cert.Issuer.Organization) > 0 {
		issuer = cert.Issuer.Organization[0]
	} else if cert.Issuer.CommonName != "" {
		issuer = cert.Issuer.CommonName
	}
	name := cert.Subject.CommonName
	if name == "" && len(cert.DNSNames) > 0 {
		name = cert.DNSNames[0]
	}
	days := calendarDaysBetween(now, cert.NotAfter)
	var b strings.Builder
	b.WriteString("TLS:    ")
	if issuer != "" {
		b.WriteString(issuer + " · ")
	}
	if name != "" {
		b.WriteString(name + " · ")
	}
	fmt.Fprintf(&b, "expires %s", cert.NotAfter.Format("2006-01-02"))
	switch {
	case days < 0:
		fmt.Fprintf(&b, " (expired %d day%s ago)", -days, pluralS(-days))
	case days == 0:
		b.WriteString(" (today)")
	default:
		fmt.Fprintf(&b, " (%d day%s left)", days, pluralS(days))
	}
	b.WriteString("\n")
	return b.String()
}

// calendarDaysBetween counts whole calendar days from one date to another, so a
// certificate that expires on the 17th reads "45 days left" on the 2nd.
//
// It replaced a 24-hour division of the remaining duration, which truncated: a
// certificate due in exactly 45 days was 44 days and 23.999 hours away a moment
// later, so the count disagreed with the expiry date printed next to it. Both
// dates are normalised to midnight UTC, which also keeps a DST shift from
// stealing or adding a day.
func calendarDaysBetween(from, to time.Time) int {
	fy, fm, fd := from.Date()
	ty, tm, td := to.Date()
	start := time.Date(fy, fm, fd, 0, 0, 0, 0, time.UTC)
	end := time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC)
	return int(end.Sub(start).Hours() / 24)
}

// ── Rows ───────────────────────────────────────────────────────────────────

// toolsLastURL is the URL the HTTP check row remembers, so checking again is a
// correction instead of a retype. Touched on the UI thread only.
var toolsLastURL string

// toolsURLBuffer is the keyboard's working copy; the remembered URL above only
// changes when the field is submitted.
var toolsURLBuffer string

// toolsURLRowValue is what the HTTP row shows. Before the first check it says
// so instead of showing an example address, which read as a saved target.
func toolsURLRowValue() string {
	if url := strings.TrimSpace(toolsLastURL); url != "" {
		return url
	}
	return "Not checked yet"
}

// toolsHTTPCheckSubmit validates the typed URL before starting the check. A
// URL that cannot be parsed reopens the keyboard with the text still there.
func toolsHTTPCheckSubmit(raw string) {
	if _, err := netToolsParseURL(raw); err != nil {
		ShowToast(err.Error(), ToastKindWarning)
		vkOpenToolsURL(raw)
		return
	}
	value := strings.TrimSpace(raw)
	toolsLastURL = value
	powerRunFromTools(appConfig, "HTTP · "+value, "Checking "+value+"…", func() powerAsyncResult {
		text, err := httpCheckSummary(value)
		return powerAsyncResult{text: text, err: err}
	})
}

// powerProbePublicIP asks the trace endpoint what address the internet sees.
func powerProbePublicIP(config *Config) {
	powerRunFromTools(config, "Public IP", "Asking the trace service…", func() powerAsyncResult {
		text, err := publicIPSummary()
		return powerAsyncResult{text: text, err: err}
	})
}

// powerProbeLANNeighbours reads the neighbour table without probing.
func powerProbeLANNeighbours(config *Config) {
	powerRunFromTools(config, "LAN neighbours", "Reading the neighbour table…", func() powerAsyncResult {
		text, err := lanNeighbourSummary()
		return powerAsyncResult{text: text, err: err}
	})
}

// powerProbeListeningPorts lists this device's own open TCP ports.
func powerProbeListeningPorts(config *Config) {
	powerRunFromTools(config, "Listening ports", "Reading the socket table…", func() powerAsyncResult {
		text, err := listeningPortsSummary()
		return powerAsyncResult{text: text, err: err}
	})
}

// toolsUIURLPrompt is the HTTP check row's A action: ask for the URL, then
// check it. It lives here so toolsui.go stays a layout file.
func toolsUIURLPrompt() {
	vkOpenToolsURL("")
}
