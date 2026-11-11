package main

// wg_config.go — WireGuard client configurations in the wg-quick format VPN
// providers hand out (an "[Interface]" and one "[Peer]"), for the VPN
// (vpn.go): parsing with errors a person can act on, the summary Settings
// shows, the routes the tunnel needs, and the normalised text WireGuard for
// Windows is given.
//
// The scripts wg-quick can run (PreUp, PostUp, PreDown, PostDown) are never
// run: they are read, listed as skipped, and left out of everything JukaHub
// writes. A file is outside data, so nothing in it is trusted further than
// these rules.

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const (
	// wgConfigMaxBytes bounds what is read as a configuration; real ones are
	// a few hundred bytes.
	wgConfigMaxBytes = 64 << 10
	// wgDefaultMTU is wg-quick's default for a tunnel over a 1500-byte link.
	wgDefaultMTU = 1420
)

// wgConfig is one parsed configuration.
type wgConfig struct {
	PrivateKey   [32]byte
	Addresses    []netip.Prefix
	DNS          []netip.Addr
	DNSSearch    []string
	MTU          int // 0: wgDefaultMTU
	ListenPort   int
	PeerKey      [32]byte
	PresharedKey [32]byte
	HasPSK       bool
	Endpoint     string // host:port, as written
	EndpointHost string
	EndpointPort uint16
	AllowedIPs   []netip.Prefix
	Keepalive    int // seconds; 0: off
	// Skipped lists the keys JukaHub reads past (scripts, routing tables).
	Skipped []string
}

// wgLooksLikeConfig is the cheap test Files and the import list use before
// parsing: an [Interface] with a PrivateKey.
func wgLooksLikeConfig(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "[interface]") && strings.Contains(lower, "privatekey")
}

// wgParseKey reads a base64 WireGuard key.
func wgParseKey(s string) ([32]byte, error) {
	var k [32]byte
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != len(k) {
		return k, errors.New("is not a WireGuard key (44 characters of base64)")
	}
	copy(k[:], raw)
	return k, nil
}

func wgKeyString(k [32]byte) string { return base64.StdEncoding.EncodeToString(k[:]) }

// wgSplitList splits a comma-separated value, dropping empty items.
func wgSplitList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// wgParsePrefix reads "10.0.0.2/32" or a bare address (a single host).
func wgParsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not an address with a prefix length", s)
		}
		return p, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not an IP address", s)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// wgParseConfig reads a configuration. Its errors name the line and say what
// is wrong in words, because they end up in a toast.
func wgParseConfig(text string) (*wgConfig, error) {
	if len(text) > wgConfigMaxBytes {
		return nil, errors.New("that file is too big to be a WireGuard configuration")
	}
	text = strings.TrimPrefix(text, "\ufeff")
	c := &wgConfig{}
	section := ""
	peers := 0
	haveKey, havePeerKey := false, false
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if j := strings.IndexByte(line, '#'); j >= 0 {
			line = strings.TrimSpace(line[:j])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: %q is not a section name", i+1, line)
			}
			switch strings.ToLower(strings.TrimSpace(line[1 : len(line)-1])) {
			case "interface":
				section = "interface"
			case "peer":
				section = "peer"
				peers++
				if peers > 1 {
					return nil, errors.New("this configuration has more than one [Peer]; JukaHub connects to one server, so keep the one you want")
				}
			default:
				return nil, fmt.Errorf("line %d: unknown section %s", i+1, line)
			}
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected \"Key = Value\"", i+1)
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		if key == "" {
			return nil, fmt.Errorf("line %d: expected \"Key = Value\"", i+1)
		}
		var err error
		switch section {
		case "interface":
			err = c.setInterface(key, val, &haveKey)
		case "peer":
			err = c.setPeer(key, val, &havePeerKey)
		default:
			err = errors.New("settings come before [Interface]")
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	switch {
	case !haveKey:
		return nil, errors.New("the [Interface] has no PrivateKey")
	case len(c.Addresses) == 0:
		return nil, errors.New("the [Interface] has no Address")
	case peers == 0:
		return nil, errors.New("there is no [Peer] (the server) in this configuration")
	case !havePeerKey:
		return nil, errors.New("the [Peer] has no PublicKey")
	case c.Endpoint == "":
		return nil, errors.New("the [Peer] has no Endpoint, so there is no server to connect to")
	case len(c.AllowedIPs) == 0:
		return nil, errors.New("the [Peer] has no AllowedIPs")
	}
	if _, has6 := c.families(); has6 && c.MTU > 0 && c.MTU < 1280 {
		return nil, fmt.Errorf("MTU %d is too small for the IPv6 address (IPv6 needs 1280)", c.MTU)
	}
	if _, err := newWGIdentity(c.PrivateKey, c.PeerKey, c.PresharedKey); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *wgConfig) setInterface(key, val string, haveKey *bool) error {
	switch key {
	case "privatekey":
		k, err := wgParseKey(val)
		if err != nil {
			return fmt.Errorf("PrivateKey %v", err)
		}
		c.PrivateKey, *haveKey = k, true
	case "address":
		for _, item := range wgSplitList(val) {
			p, err := wgParsePrefix(item)
			if err != nil {
				return fmt.Errorf("Address: %v", err)
			}
			c.Addresses = append(c.Addresses, p)
		}
	case "dns":
		for _, item := range wgSplitList(val) {
			if a, err := netip.ParseAddr(item); err == nil {
				c.DNS = append(c.DNS, a)
				continue
			}
			if !wgValidDomain(item) {
				return fmt.Errorf("DNS: %q is neither an address nor a domain", item)
			}
			c.DNSSearch = append(c.DNSSearch, item)
		}
	case "mtu":
		n, err := strconv.Atoi(val)
		if err != nil || n < 576 || n > 9000 {
			return fmt.Errorf("MTU %q is not between 576 and 9000", val)
		}
		c.MTU = n
	case "listenport":
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("ListenPort %q is not a port number", val)
		}
		c.ListenPort = n
	default:
		// Table, FwMark, SaveConfig and the scripts are wg-quick's business;
		// anything else is a provider's extra. Neither stops the import.
		c.Skipped = append(c.Skipped, wgKeyName(key))
	}
	return nil
}

func (c *wgConfig) setPeer(key, val string, havePeerKey *bool) error {
	switch key {
	case "publickey":
		k, err := wgParseKey(val)
		if err != nil {
			return fmt.Errorf("PublicKey %v", err)
		}
		c.PeerKey, *havePeerKey = k, true
	case "presharedkey":
		k, err := wgParseKey(val)
		if err != nil {
			return fmt.Errorf("PresharedKey %v", err)
		}
		c.PresharedKey, c.HasPSK = k, true
	case "endpoint":
		host, port, err := net.SplitHostPort(val)
		if err != nil || host == "" {
			return fmt.Errorf("Endpoint %q is not host:port", val)
		}
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return fmt.Errorf("Endpoint %q has no usable port", val)
		}
		if _, err := netip.ParseAddr(host); err != nil && !wgValidDomain(host) {
			return fmt.Errorf("Endpoint %q is not a host name or address", val)
		}
		c.Endpoint, c.EndpointHost, c.EndpointPort = val, host, uint16(n)
	case "allowedips":
		for _, item := range wgSplitList(val) {
			p, err := wgParsePrefix(item)
			if err != nil {
				return fmt.Errorf("AllowedIPs: %v", err)
			}
			c.AllowedIPs = append(c.AllowedIPs, p.Masked())
		}
	case "persistentkeepalive":
		if strings.EqualFold(val, "off") {
			c.Keepalive = 0
			break
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("PersistentKeepalive %q is not a number of seconds", val)
		}
		c.Keepalive = n
	default:
		c.Skipped = append(c.Skipped, wgKeyName(key))
	}
	return nil
}

// wgKeyName gives a skipped key back its usual spelling for the summary.
func wgKeyName(key string) string {
	for _, k := range []string{"PreUp", "PostUp", "PreDown", "PostDown", "Table", "FwMark", "SaveConfig"} {
		if strings.EqualFold(k, key) {
			return k
		}
	}
	return key
}

// wgValidDomain accepts a DNS name: letters, digits, '-' and dots.
func wgValidDomain(s string) bool {
	if s == "" || len(s) > 253 || strings.HasPrefix(s, ".") || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !(r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// mtu is the tunnel's MTU.
func (c *wgConfig) mtu() int {
	if c.MTU > 0 {
		return c.MTU
	}
	return wgDefaultMTU
}

// families reports which IP versions the tunnel has an address for.
func (c *wgConfig) families() (has4, has6 bool) {
	for _, p := range c.Addresses {
		if p.Addr().Is4() {
			has4 = true
		} else {
			has6 = true
		}
	}
	return has4, has6
}

// wgRoutePlan is how the system's traffic is pointed at the tunnel.
type wgRoutePlan struct {
	// Routes go into the main table through the tunnel. Everything is split
	// into two halves (0.0.0.0/1 and 128.0.0.0/1, ::/1 and 8000::/1): more
	// specific than the default route, so it wins without being replaced,
	// while the Wi-Fi network's own route stays more specific still and the
	// LAN stays reachable.
	Routes []netip.Prefix
	// Reject is IPv6 the tunnel cannot carry (it has no IPv6 address), routed
	// to "unreachable" so it cannot go around the VPN: a connection fails at
	// once and the program uses IPv4.
	Reject []netip.Prefix
	// Allowed is what the peer may send from and we send to it.
	Allowed []netip.Prefix
}

// wgHalves are the two /1 routes that cover a family.
func wgHalves(v6 bool) []netip.Prefix {
	if v6 {
		return []netip.Prefix{netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1")}
	}
	return []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}
}

// routePlan is the plan for "everything" (all) or for the configuration's own
// AllowedIPs. A family the tunnel has no address for is never carried — a
// packet must have a source address the server accepts — and never left to go
// around it either: IPv6 is routed to "unreachable", so connections fail at
// once and programs use IPv4; IPv4 is routed into the tunnel, which drops it.
// (An unreachable IPv4 route would also catch the tunnel's own packets to an
// IPv4 server: bound to the Wi-Fi, they would then be looked for on the local
// network.)
func (c *wgConfig) routePlan(all bool) wgRoutePlan {
	has4, has6 := c.families()
	var plan wgRoutePlan
	place := func(p netip.Prefix) {
		routes := []netip.Prefix{p}
		if p.Bits() == 0 {
			routes = wgHalves(p.Addr().Is6())
		}
		switch {
		case (p.Addr().Is4() && has4) || (p.Addr().Is6() && has6):
			plan.Routes = append(plan.Routes, routes...)
			plan.Allowed = append(plan.Allowed, p)
		case p.Addr().Is6():
			plan.Reject = append(plan.Reject, routes...)
		default:
			plan.Routes = append(plan.Routes, routes...)
		}
	}
	if all {
		place(netip.MustParsePrefix("0.0.0.0/0"))
		place(netip.MustParsePrefix("::/0"))
		return plan
	}
	for _, p := range c.AllowedIPs {
		place(p)
	}
	return plan
}

// coversEverything reports whether the configuration's own AllowedIPs already
// send all IPv4 traffic through the tunnel.
func (c *wgConfig) coversEverything() bool {
	for _, p := range c.AllowedIPs {
		if p.Addr().Is4() && p.Bits() == 0 {
			return true
		}
	}
	return false
}

// publicKey is our public key, which the server's operator registers.
func (c *wgConfig) publicKey() string {
	k, err := ecdh.X25519().NewPrivateKey(c.PrivateKey[:])
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

func wgJoinPrefixes(ps []netip.Prefix) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.String()
	}
	return strings.Join(parts, ", ")
}

// quickText is the configuration in wg-quick format with only the keys
// JukaHub understands — no scripts — and, for "everything", AllowedIPs that
// send all traffic while keeping the local network reachable.
func (c *wgConfig) quickText(all bool) string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	b.WriteString("PrivateKey = " + wgKeyString(c.PrivateKey) + "\n")
	b.WriteString("Address = " + wgJoinPrefixes(c.Addresses) + "\n")
	if len(c.DNS)+len(c.DNSSearch) > 0 {
		var dns []string
		for _, a := range c.DNS {
			dns = append(dns, a.String())
		}
		dns = append(dns, c.DNSSearch...)
		b.WriteString("DNS = " + strings.Join(dns, ", ") + "\n")
	}
	if c.MTU > 0 {
		b.WriteString("MTU = " + strconv.Itoa(c.MTU) + "\n")
	}
	if c.ListenPort > 0 {
		b.WriteString("ListenPort = " + strconv.Itoa(c.ListenPort) + "\n")
	}
	b.WriteString("\n[Peer]\n")
	b.WriteString("PublicKey = " + wgKeyString(c.PeerKey) + "\n")
	if c.HasPSK {
		b.WriteString("PresharedKey = " + wgKeyString(c.PresharedKey) + "\n")
	}
	b.WriteString("Endpoint = " + c.Endpoint + "\n")
	// A /0 is written as its two halves: the same addresses, but WireGuard
	// for Windows switches on its LAN-blocking kill switch for a lone /0, and
	// the LAN (the phone companion, a console at home) must stay reachable.
	var allowed []netip.Prefix
	for _, p := range c.AllowedIPs {
		if p.Bits() == 0 {
			allowed = append(allowed, wgHalves(p.Addr().Is6())...)
		} else {
			allowed = append(allowed, p)
		}
	}
	if all {
		// Both families, whatever the addresses: a family the tunnel has no
		// address for is routed in too, so it cannot go around the VPN; it
		// goes nowhere instead.
		allowed = append(wgHalves(false), wgHalves(true)...)
	}
	b.WriteString("AllowedIPs = " + wgJoinPrefixes(allowed) + "\n")
	if c.Keepalive > 0 {
		b.WriteString("PersistentKeepalive = " + strconv.Itoa(c.Keepalive) + "\n")
	}
	return b.String()
}
