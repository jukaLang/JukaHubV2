package main

// powerdns.go — the Tools → DNS lookup backend.
//
// The row used to resolve two hard-coded names and print their addresses,
// which answered nothing about the host the user cared about and hid every
// other record. A lookup now takes the name typed at the keyboard (or an IP,
// which is reversed into a PTR name) and reports both address families plus
// CNAME, MX, NS and TXT records, with the time the resolver took.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// powerDNSQuery is one parsed lookup: a host name, or an IP to reverse.
type powerDNSQuery struct {
	Host string
	IP   net.IP
}

// The resolver calls are variables so the report can be tested without a
// network, the same way the Wi-Fi runner is stubbed.
var (
	powerDNSLookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	powerDNSLookupCNAME = func(ctx context.Context, host string) (string, error) {
		return net.DefaultResolver.LookupCNAME(ctx, host)
	}
	powerDNSLookupMX = func(ctx context.Context, host string) ([]*net.MX, error) {
		return net.DefaultResolver.LookupMX(ctx, host)
	}
	powerDNSLookupNS = func(ctx context.Context, host string) ([]*net.NS, error) {
		return net.DefaultResolver.LookupNS(ctx, host)
	}
	powerDNSLookupTXT = func(ctx context.Context, host string) ([]string, error) {
		return net.DefaultResolver.LookupTXT(ctx, host)
	}
	powerDNSLookupAddr = func(ctx context.Context, addr string) ([]string, error) {
		return net.DefaultResolver.LookupAddr(ctx, addr)
	}
)

// powerDNSParseQuery turns typed text into a lookup. A pasted link or
// host:port is reduced to its host; anything carrying a path or spaces is
// refused, so the row stays a resolver and never becomes a fetcher.
func powerDNSParseQuery(raw string) (powerDNSQuery, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return powerDNSQuery{}, fmt.Errorf("type a host name or IP address first")
	}
	if strings.Contains(text, "://") {
		parsed, err := url.Parse(text)
		if err != nil || parsed.Host == "" {
			return powerDNSQuery{}, fmt.Errorf("that link has no host in it")
		}
		text = parsed.Host
	}
	if strings.ContainsAny(text, " \t\r\n/\\") {
		return powerDNSQuery{}, fmt.Errorf("type a host name or IP, not a path")
	}
	if host, _, err := net.SplitHostPort(text); err == nil {
		text = host
	}
	text = strings.Trim(strings.TrimSpace(text), "[]")
	if text == "" {
		return powerDNSQuery{}, fmt.Errorf("type a host name or IP address first")
	}
	if ip := net.ParseIP(text); ip != nil {
		return powerDNSQuery{Host: text, IP: ip}, nil
	}
	text = strings.TrimSuffix(text, ".")
	if text == "" || len(text) > 253 {
		return powerDNSQuery{}, fmt.Errorf("that does not look like a host name")
	}
	for _, r := range text {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' {
			continue
		}
		return powerDNSQuery{}, fmt.Errorf("host names use letters, digits, dots and hyphens")
	}
	for _, label := range strings.Split(text, ".") {
		if label == "" || len(label) > 63 {
			return powerDNSQuery{}, fmt.Errorf("that does not look like a host name")
		}
	}
	return powerDNSQuery{Host: text}, nil
}

// powerDNSValueCap bounds one record type in the report. A TXT-heavy domain
// returns dozens of strings, and the output pane is a text view on a handheld
// screen.
const powerDNSValueCap = 8

// powerDNSRecord is one printable line: the record type and its value.
type powerDNSRecord struct {
	Kind  string
	Value string
}

// powerDNSErrorText says why one record type came back empty without dumping
// Go's wrapped error chain into the pane.
func powerDNSErrorText(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsNotFound:
			return "no such record"
		case dnsErr.IsTimeout:
			return "resolver timed out"
		case dnsErr.IsTemporary:
			return "temporary resolver failure"
		}
	}
	if text := firstLine(err.Error()); text != "" {
		return text
	}
	return "lookup failed"
}

func powerDNSPlural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// powerDNSIPRecords splits a resolver answer into A and AAAA rows, IPv4
// first, each family in a stable order so two runs read the same.
func powerDNSIPRecords(ips []net.IP) ([]powerDNSRecord, map[string]int) {
	var v4, v6 []net.IP
	for _, ip := range ips {
		if four := ip.To4(); four != nil {
			v4 = append(v4, four)
		} else {
			v6 = append(v6, ip)
		}
	}
	sort.Slice(v4, func(i, j int) bool { return bytes.Compare(v4[i], v4[j]) < 0 })
	sort.Slice(v6, func(i, j int) bool { return bytes.Compare(v6[i], v6[j]) < 0 })

	records := make([]powerDNSRecord, 0, len(v4)+len(v6))
	extra := map[string]int{}
	add := func(kind string, list []net.IP) {
		for i, ip := range list {
			if i >= powerDNSValueCap {
				extra[kind]++
				continue
			}
			records = append(records, powerDNSRecord{Kind: kind, Value: ip.String()})
		}
	}
	add("A", v4)
	add("AAAA", v6)
	return records, extra
}

// powerDNSMXValue prints one mail exchanger. A host that is just the root is
// a null MX: the domain explicitly takes no mail, and printing "0 " for it read
// like a broken row.
func powerDNSMXValue(m *net.MX) string {
	if m == nil {
		return ""
	}
	host := strings.TrimSuffix(m.Host, ".")
	if host == "" {
		return fmt.Sprintf("%d . (no mail accepted)", m.Pref)
	}
	return fmt.Sprintf("%d %s", m.Pref, host)
}

// powerDNSFormat renders the rows the pane shows. Duplicates are dropped: a
// resolver that answers both an alias and its target must not list the same
// address twice.
func powerDNSFormat(records []powerDNSRecord, extra map[string]int) []string {
	out := make([]string, 0, len(records)+len(extra))
	seen := make(map[string]bool, len(records))
	for _, r := range records {
		key := r.Kind + " " + r.Value
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, fmt.Sprintf("%-4s  %s", r.Kind, r.Value))
	}
	for _, kind := range []string{"A", "AAAA", "CNAME", "PTR", "MX", "NS", "TXT"} {
		if n := extra[kind]; n > 0 {
			out = append(out, fmt.Sprintf("%-4s  … %d more %s record%s", "", n, kind, powerDNSPlural(n)))
		}
	}
	return out
}

// powerDNSReport runs the lookups and writes the pane's text. A record type
// that fails is reported on its own line instead of failing the lookup: a
// domain with no MX still has addresses worth showing.
func powerDNSReport(q powerDNSQuery) string {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	var records []powerDNSRecord
	extra := map[string]int{}
	var notes []string

	if q.IP != nil {
		names, err := powerDNSLookupAddr(ctx, q.Host)
		if err != nil {
			notes = append(notes, "PTR: "+powerDNSErrorText(err))
		}
		sort.Strings(names)
		for i, name := range names {
			if i >= powerDNSValueCap {
				extra["PTR"]++
				continue
			}
			records = append(records, powerDNSRecord{Kind: "PTR", Value: strings.TrimSuffix(name, ".")})
		}
	} else {
		ips, ipErr := powerDNSLookupIP(ctx, q.Host)
		if ipErr == nil {
			ipRecords, ipExtra := powerDNSIPRecords(ips)
			records = append(records, ipRecords...)
			for kind, n := range ipExtra {
				extra[kind] += n
			}
		}

		// These record types are independent questions, so they are asked
		// together: on a slow handheld resolver this is one round trip instead
		// of four. Each answer is written by its own goroutine and read only
		// after Wait, so no lock is needed.
		var (
			cname    string
			mx       []*net.MX
			ns       []*net.NS
			txt      []string
			cnameErr error
			mxErr    error
			nsErr    error
			txtErr   error
			wg       sync.WaitGroup
		)
		wg.Add(4)
		go func() { defer wg.Done(); cname, cnameErr = powerDNSLookupCNAME(ctx, q.Host) }()
		go func() { defer wg.Done(); mx, mxErr = powerDNSLookupMX(ctx, q.Host) }()
		go func() { defer wg.Done(); ns, nsErr = powerDNSLookupNS(ctx, q.Host) }()
		go func() { defer wg.Done(); txt, txtErr = powerDNSLookupTXT(ctx, q.Host) }()
		wg.Wait()

		// LookupCNAME answers the name itself when there is no alias, so it is
		// only shown when it names something else.
		if cnameErr == nil {
			if canonical := strings.TrimSuffix(cname, "."); canonical != "" && !strings.EqualFold(canonical, q.Host) {
				records = append(records, powerDNSRecord{Kind: "CNAME", Value: canonical})
			}
		}
		if mxErr == nil {
			sort.SliceStable(mx, func(i, j int) bool {
				if mx[i].Pref != mx[j].Pref {
					return mx[i].Pref < mx[j].Pref
				}
				return mx[i].Host < mx[j].Host
			})
			for i, m := range mx {
				if i >= powerDNSValueCap {
					extra["MX"]++
					continue
				}
				records = append(records, powerDNSRecord{Kind: "MX", Value: powerDNSMXValue(m)})
			}
		}
		if nsErr == nil {
			sort.SliceStable(ns, func(i, j int) bool { return ns[i].Host < ns[j].Host })
			for i, n := range ns {
				if i >= powerDNSValueCap {
					extra["NS"]++
					continue
				}
				records = append(records, powerDNSRecord{Kind: "NS", Value: strings.TrimSuffix(n.Host, ".")})
			}
		}
		if txtErr == nil {
			for i, record := range txt {
				if i >= powerDNSValueCap {
					extra["TXT"]++
					continue
				}
				value := strings.Join(strings.Fields(record), " ")
				if len(value) > 96 {
					value = value[:95] + "…"
				}
				records = append(records, powerDNSRecord{Kind: "TXT", Value: `"` + value + `"`})
			}
		}
		if ipErr != nil {
			notes = append(notes, "addresses: "+powerDNSErrorText(ipErr))
		}
	}

	var b strings.Builder
	if q.IP != nil {
		fmt.Fprintf(&b, "Reverse lookup: %s\n\n", q.Host)
	} else {
		fmt.Fprintf(&b, "Lookup: %s\n\n", q.Host)
	}
	lines := powerDNSFormat(records, extra)
	if len(lines) == 0 {
		b.WriteString("No records returned.\n")
	}
	for _, line := range lines {
		b.WriteString(line + "\n")
	}
	if len(notes) > 0 {
		b.WriteString("\n")
		for _, note := range notes {
			b.WriteString(note + "\n")
		}
	}
	fmt.Fprintf(&b, "\n%d record%s in %d ms · system resolver", len(records), powerDNSPlural(len(records)), time.Since(start).Milliseconds())
	if q.IP == nil && len(records) > 0 {
		b.WriteString("\nRead-only: names and addresses only, no connections.")
	}
	return b.String()
}
