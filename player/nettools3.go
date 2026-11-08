package main

// nettools3.go — the checks that identify what is behind a port rather than
// whether the port is open: the TLS certificate a service presents and the
// security headers a web server returns (the Browser's site information calls
// certSummary and headerAuditSummary for the page it shows), and the codec the
// Network tools page offers for the strings those checks turn up.
//
// All three are read-only. The certificate check makes two handshakes (one to
// read an untrusted certificate, one to see whether the device would trust
// it); the headers check makes one GET and keeps none of the body; the codec
// touches nothing but its own argument.

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ── Targets ────────────────────────────────────────────────────────────────

// netTargetHostPort parses what the certificate row accepts: "example.com",
// "example.com:8443", "192.168.1.7:443", "[::1]:443" or a URL pasted from a
// browser. A target without a port takes the default, because the port is the
// part nobody wants to type when they are looking at a certificate.
func netTargetHostPort(raw string, defaultPort int) (string, int, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", 0, errors.New("type a host first, for example example.com")
	}
	if strings.ContainsAny(text, " \t\r\n") {
		return "", 0, errors.New("a host cannot contain spaces")
	}
	if i := strings.Index(text, "://"); i >= 0 {
		text = text[i+3:]
	}
	if i := strings.IndexAny(text, "/?#"); i >= 0 {
		text = text[:i]
	}
	if host, portText, err := net.SplitHostPort(text); err == nil {
		port, convErr := strconv.Atoi(portText)
		if convErr != nil || port < 1 || port > 65535 {
			return "", 0, errors.New("the port must be a number from 1 to 65535")
		}
		host = strings.Trim(host, "[]")
		if host == "" {
			return "", 0, errors.New("that target has no host in it")
		}
		return host, port, nil
	}
	// No port: a bare IPv6 literal lands here too, because every other form with
	// a port was handled above.
	host := strings.Trim(text, "[]")
	if host == "" {
		return "", 0, errors.New("that target has no host in it")
	}
	if _, err := powerDNSParseQuery(host); err != nil {
		return "", 0, errors.New("that host name does not look right")
	}
	return host, defaultPort, nil
}

// ── TLS certificate ────────────────────────────────────────────────────────

// certSummary describes the certificate a TLS port presents.
//
// The handshake runs twice on purpose. The first skips verification, so an
// expired or mismatched certificate still produces a report instead of an
// error — that certificate is exactly what the row is for. The second verifies,
// because "would a browser accept this" is the question that decides whether the
// stream on the card will play.
func certSummary(raw string) (string, error) {
	host, port, err := netTargetHostPort(raw, 443)
	if err != nil {
		return "", err
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 6 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	})
	if err != nil {
		return "", fmt.Errorf("no TLS answer from %s: %w", addr, err)
	}
	state := conn.ConnectionState()
	remote := ""
	if a := conn.RemoteAddr(); a != nil {
		remote = a.String()
	}
	conn.Close()

	trust := "verified against this device's trusted roots"
	if vc, verr := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: host}); verr == nil {
		vc.Close()
	} else {
		trust = "not trusted: " + firstLine(verr.Error())
	}

	if len(state.PeerCertificates) == 0 {
		return "", errors.New("the server completed a handshake without sending a certificate")
	}
	cert := state.PeerCertificates[0]

	var b strings.Builder
	fmt.Fprintf(&b, "Target:  %s:%d\n", host, port)
	if remote != "" && remote != addr {
		fmt.Fprintf(&b, "Address: %s\n", remote)
	}
	fmt.Fprintf(&b, "TLS:     %s · %s\n", tlsVersionName(state.Version), tls.CipherSuiteName(state.CipherSuite))
	fmt.Fprintf(&b, "Subject: %s\n", pkixName(cert.Subject))
	fmt.Fprintf(&b, "Issuer:  %s\n", pkixName(cert.Issuer))
	fmt.Fprintf(&b, "Valid:   %s → %s UTC\n",
		cert.NotBefore.UTC().Format("2006-01-02 15:04"),
		cert.NotAfter.UTC().Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "Expires: %s\n", certExpiryText(cert.NotAfter, time.Now()))
	fmt.Fprintf(&b, "Key:     %s\n", certKeyText(cert))
	fmt.Fprintf(&b, "Signing: %s\n", cert.SignatureAlgorithm.String())
	b.WriteString("Names:   " + certNamesText(cert) + "\n")
	if len(state.PeerCertificates) > 1 {
		fmt.Fprintf(&b, "Chain:   %d certificates sent\n", len(state.PeerCertificates))
	}
	fmt.Fprintf(&b, "Trust:   %s\n", trust)
	b.WriteString("\nRead-only: two handshakes, no request; nothing is sent after the greeting.\n")
	return b.String(), nil
}

// tlsVersionName is the version as people write it. A version this build does
// not know is printed as its number rather than as a blank.
func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS13:
		return "TLS 1.3"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS10:
		return "TLS 1.0"
	}
	return fmt.Sprintf("TLS 0x%04x", version)
}

// pkixName renders a distinguished name the way a certificate viewer does:
// "CN=example.com, O=Example Ltd, C=US" rather than Go's map dump.
func pkixName(name pkix.Name) string {
	parts := make([]string, 0, 4)
	if name.CommonName != "" {
		parts = append(parts, "CN="+name.CommonName)
	}
	for _, org := range name.Organization {
		parts = append(parts, "O="+org)
	}
	for _, country := range name.Country {
		parts = append(parts, "C="+country)
	}
	if len(parts) == 0 {
		return "(no name in the certificate)"
	}
	return strings.Join(parts, ", ")
}

// certKeyText is the key type and its size, which is what a weak-key review
// looks at.
func certKeyText(cert *x509.Certificate) string {
	switch key := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA %d bits", key.N.BitLen())
	case *ecdsa.PublicKey:
		return fmt.Sprintf("ECDSA P-%d", key.Curve.Params().BitSize)
	case ed25519.PublicKey:
		return "Ed25519"
	}
	return cert.PublicKeyAlgorithm.String()
}

// certNamesText lists what the certificate covers, with a count for the rest so
// a wildcard certificate with fifty names cannot fill the pane.
func certNamesText(cert *x509.Certificate) string {
	names := append([]string{}, cert.DNSNames...)
	if len(names) == 0 && cert.Subject.CommonName != "" {
		return cert.Subject.CommonName
	}
	if len(names) == 0 {
		return "none listed"
	}
	const shown = 4
	if len(names) <= shown {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(names[:shown], ", "), len(names)-shown)
}

// certExpiryText is the same wording the HTTP check uses, so the two rows never
// disagree about how long a certificate has left.
func certExpiryText(notAfter, now time.Time) string {
	days := calendarDaysBetween(now, notAfter)
	switch {
	case days < 0:
		return fmt.Sprintf("%s (expired %d day%s ago)",
			notAfter.UTC().Format("2006-01-02"), -days, pluralS(-days))
	case days == 0:
		return notAfter.UTC().Format("2006-01-02") + " (today)"
	}
	return fmt.Sprintf("%s (%d day%s left)", notAfter.UTC().Format("2006-01-02"), days, pluralS(days))
}

// ── Security headers ───────────────────────────────────────────────────────

// securityHeaderAudit is the list the headers row reports on: the six a web
// server sends to turn off a browser feature that is on by default.
var securityHeaderAudit = []struct {
	Name string
	Why  string
}{
	{"Strict-Transport-Security", "keeps a browser on https"},
	{"Content-Security-Policy", "limits what a page may load and run"},
	{"X-Frame-Options", "stops another site framing the page"},
	{"X-Content-Type-Options", "stops the browser guessing a type"},
	{"Referrer-Policy", "controls the Referer sent onward"},
	{"Permissions-Policy", "switches browser features off"},
}

// headerAuditSummary GETs one URL and reports which of the hardening headers
// came back. The body is drained only far enough for the connection to be
// reusable and is never kept.
func headerAuditSummary(raw string) (string, error) {
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
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	elapsed := time.Since(start)
	return formatHeaderAudit(target, resp, elapsed), nil
}

// formatHeaderAudit renders the report. Present headers keep their value (a
// max-age or a policy name is the part worth reading); absent ones say why they
// were expected.
func formatHeaderAudit(target *url.URL, resp *http.Response, elapsed time.Duration) string {
	header := resp.Header
	var b strings.Builder
	fmt.Fprintf(&b, "Check:   %s\n", target.String())
	if final := resp.Request.URL; final != nil && final.String() != target.String() {
		fmt.Fprintf(&b, "Final:   %s\n", final.String())
	}
	fmt.Fprintf(&b, "Status:  %s\n", resp.Status)
	fmt.Fprintf(&b, "Time:    %d ms\n", elapsed.Milliseconds())
	if server := header.Get("Server"); server != "" {
		fmt.Fprintf(&b, "Server:  %s\n", headerLineValue(server))
	}

	present := 0
	for _, h := range securityHeaderAudit {
		if headerLineValue(header.Get(h.Name)) != "" {
			present++
		}
	}
	fmt.Fprintf(&b, "Headers: %d of %d present\n", present, len(securityHeaderAudit))
	for _, h := range securityHeaderAudit {
		value := headerLineValue(header.Get(h.Name))
		if value == "" {
			fmt.Fprintf(&b, "  - %-27s missing · %s\n", h.Name, h.Why)
			continue
		}
		fmt.Fprintf(&b, "  + %-27s %s\n", h.Name, value)
	}

	if target.Scheme == "http" {
		b.WriteString("\nNote:    this was http://, where a browser ignores HSTS — check the https:// URL for the real picture.\n")
	}
	if resp.StatusCode >= 400 {
		b.WriteString("\nThe server answered, but with an error status; its error pages may not carry the same headers.\n")
	}
	b.WriteString("\nRead-only: one GET; the body is read only far enough to close the connection.\n")
	return b.String()
}

// headerLineValue trims a header to one readable line of bounded length, so a
// 4 KB Content-Security-Policy does not push the rest of the report off screen.
func headerLineValue(value string) string {
	// Fields splits on every whitespace run and drops the ends, so a
	// folded header becomes one line with single spaces instead of
	// doubled ones.
	text := strings.Join(strings.Fields(value), " ")
	const limit = 64
	if len(text) > limit {
		return strings.TrimSpace(text[:limit]) + "…"
	}
	return text
}

// ── Codec ──────────────────────────────────────────────────────────────────

// codecLimit bounds each decoded form shown: enough to read a token or a query
// string, not enough to turn the viewer into a hex dump.
const codecLimit = 240

// codecSummary decodes the string the way a payload from a log, a URL or a
// challenge is read, and shows the encodings back so a value can be reversed.
// Every field is labelled even when it does not apply, so the shape of the
// report is the same every time.
func codecSummary(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", errors.New("type or paste a string first")
	}

	urlDecoded, urlErr := url.QueryUnescape(text)
	base64Decoded, base64Note := decodeBase64(text)
	hexDecoded, hexNote := decodeHex(text)

	var b strings.Builder
	fmt.Fprintf(&b, "Input:      %s\n", printableText(text, codecLimit))
	switch {
	case urlErr != nil:
		b.WriteString("URL decode: not valid: " + firstLine(urlErr.Error()) + "\n")
	case urlDecoded == text:
		b.WriteString("URL decode: unchanged (no %xx escapes or + signs)\n")
	default:
		fmt.Fprintf(&b, "URL decode: %s\n", printableText(urlDecoded, codecLimit))
	}
	if base64Note != "" {
		fmt.Fprintf(&b, "Base64:     %s\n", base64Note)
	} else {
		fmt.Fprintf(&b, "Base64:     %s\n", describeDecoded(base64Decoded, codecLimit))
	}
	if hexNote != "" {
		fmt.Fprintf(&b, "Hex:        %s\n", hexNote)
	} else {
		fmt.Fprintf(&b, "Hex:        %s\n", describeDecoded(hexDecoded, codecLimit))
	}

	b.WriteString("\n")
	fmt.Fprintf(&b, "As base64:  %s\n", base64.StdEncoding.EncodeToString([]byte(text)))
	fmt.Fprintf(&b, "As hex:     %s\n", hex.EncodeToString([]byte(text)))
	fmt.Fprintf(&b, "As URL:     %s\n", url.QueryEscape(text))
	b.WriteString("\nNothing left this device: the codec works on the string only.\n")
	return b.String(), nil
}

// decodeBase64 decodes standard and URL-safe base64, with or without padding,
// because a token from a URL or a JWT segment usually has one of the other
// alphabets. The note is non-empty when the string is not base64 at all.
func decodeBase64(text string) ([]byte, string) {
	trimmed := strings.TrimSpace(text)
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(trimmed)
		if err != nil || len(decoded) == 0 {
			continue
		}
		return decoded, ""
	}
	return nil, "not valid base64"
}

// decodeHex decodes an even run of hex digits, with an optional 0x prefix.
func decodeHex(text string) ([]byte, string) {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(text), "0x"), "0X")
	trimmed = strings.Join(strings.Fields(trimmed), "")
	if trimmed == "" {
		return nil, "not valid hex"
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil || len(decoded) == 0 {
		return nil, "not valid hex"
	}
	return decoded, ""
}

// describeDecoded is what the pane shows for decoded bytes: the text when the
// bytes are text, and a byte count with the head in hex when they are not.
//
// The second half matters because both decoders accept the other's input: a hex
// string like "68656c6c6f" is also valid base64, and printing that decode as
// text put seven bytes of mojibake in the pane where the answer ("hello") was
// on the next line. Binary is now labelled as binary instead of guessed at.
func describeDecoded(decoded []byte, limit int) string {
	if !mostlyText(decoded) {
		head := decoded
		if len(head) > 12 {
			head = head[:12]
			return fmt.Sprintf("%d bytes, not text (hex %s…)", len(decoded), hex.EncodeToString(head))
		}
		return fmt.Sprintf("%d bytes, not text (hex %s)", len(decoded), hex.EncodeToString(head))
	}
	return printableText(string(decoded), limit)
}

// mostlyText reports whether decoded bytes are worth showing as text: valid
// UTF-8, and at most one non-printing rune in ten.
func mostlyText(decoded []byte) bool {
	if !utf8.Valid(decoded) {
		return false
	}
	total, unprintable := 0, 0
	for _, r := range string(decoded) {
		total++
		switch {
		case r == '\n' || r == '\r' || r == '\t':
		case !unicode.IsPrint(r):
			unprintable++
		}
	}
	return total > 0 && unprintable*10 <= total
}

// printableText keeps a decoded value on one line: a decoded token is often
// binary, and raw control bytes in the pane read as a rendering bug.
func printableText(text string, limit int) string {
	var b strings.Builder
	runes := []rune(text)
	for _, r := range runes {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			b.WriteRune('.')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if len(runes) <= limit {
		return out
	}
	// Truncate what survived the sanitising, not the input: the two lengths
	// differ as soon as a control byte became a dot.
	trimmed := []rune(out)
	if len(trimmed) > limit {
		trimmed = trimmed[:limit]
	}
	return string(trimmed) + "…"
}
