package main

// moonlight_discovery.go — finding GameStream PCs, and waking them.
//
// Vibepollo, Apollo, Sunshine and GeForce Experience announce themselves over
// mDNS as _nvstream._tcp, so one browse finds every PC on the network without
// an address typed. The browse is one-shot and bounded by its timeout; the
// panel runs it from a goroutine, never on the render loop.
//
// Wake-on-LAN is the standard magic packet (six 0xFF bytes, then the PC's MAC
// sixteen times) to the limited broadcast and to every up interface's directed
// broadcast (psDiscoveryAddresses, as PS Remote Play's scan sends), on UDP 9
// and 7, plus straight to the addresses the PC last answered at, which is what
// reaches it through a router that forwards a port.

import (
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	mlMDNSService      = "_nvstream._tcp"
	mlDiscoverMin      = 500 * time.Millisecond
	mlDiscoverMax      = 15 * time.Second
	mlDiscoverDefault  = 3 * time.Second
	mlWakeLookupBudget = 2 * time.Second
)

var errMLNoMAC = errors.New("the PC's network address (MAC) is not known yet — it is learned while the PC is on")

// mlDiscovered is one PC that answered the browse: its mDNS name, IPv4
// address and HTTP port.
type mlDiscovered struct {
	Name string
	Addr string
	Port int
}

// mlDiscover browses mDNS for GameStream PCs for timeout (or until ctx ends)
// and returns what answered, one entry per address, sorted by name. It blocks
// for the whole timeout: call it from a goroutine.
func mlDiscover(ctx context.Context, timeout time.Duration) (found []mlDiscovered) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("moonlight", "mDNS browse failed: %v", r)
		}
	}()
	// The client identity is made the first time it is needed, which takes a
	// second or two on a handheld: a browse is the first thing the panel does,
	// so the key is made while it waits for answers.
	go func() { _, _ = mlIdentity() }()
	switch {
	case timeout <= 0:
		timeout = mlDiscoverDefault
	case timeout < mlDiscoverMin:
		timeout = mlDiscoverMin
	case timeout > mlDiscoverMax:
		timeout = mlDiscoverMax
	}
	// IPv4 only: a handheld without IPv6 cannot join the IPv6 group, and the
	// resolver gives up on both when one fails.
	resolver, err := zeroconf.NewResolver(zeroconf.SelectIPTraffic(zeroconf.IPv4))
	if err != nil {
		logInfo("moonlight", "mDNS browse not possible here: %v", err)
		return nil
	}
	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry, 16)
	if err := resolver.Browse(bctx, mlMDNSService, "local.", entries); err != nil {
		logInfo("moonlight", "mDNS browse failed: %v", err)
		return nil
	}
	seen := map[string]bool{}
	// The resolver closes entries when bctx ends.
	for e := range entries {
		if e == nil {
			continue
		}
		name := mlMDNSName(e.Instance, e.HostName)
		for _, ip := range e.AddrIPv4 {
			if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
				continue
			}
			addr := ip.String()
			if seen[addr] {
				continue
			}
			seen[addr] = true
			port := e.Port
			if port <= 0 || port > 65535 {
				port = mlHTTPPortDefault
			}
			found = append(found, mlDiscovered{Name: name, Addr: addr, Port: port})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := strings.ToLower(found[i].Name), strings.ToLower(found[j].Name)
		if a != b {
			return a < b
		}
		return found[i].Addr < found[j].Addr
	})
	return found
}

// mlMDNSName is a PC's name as mDNS gives it: the instance name with its DNS
// escapes undone (mlDNSUnescape), else the host name without ".local.".
func mlMDNSName(instance, hostName string) string {
	name := strings.TrimSpace(mlDNSUnescape(strings.TrimSpace(instance)))
	if name == "" {
		name = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(hostName), "."), ".local")
	}
	return psSessionValue(name)
}

// mlDNSUnescape undoes the escapes of a DNS name in presentation form, as the
// mDNS library writes it: \DDD (three decimal digits) is the byte DDD, \c is
// the character c ("Den\226\128\153s\ PC" is "Den’s PC"). The bytes are read
// as UTF-8, any that are not becoming "?".
func mlDNSUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return strings.ToValidUTF8(s, "?")
	}
	digit := func(c byte) bool { return c >= '0' && c <= '9' }
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b = append(b, c)
			continue
		}
		if i+3 < len(s) && digit(s[i+1]) && digit(s[i+2]) && digit(s[i+3]) {
			if n := int(s[i+1]-'0')*100 + int(s[i+2]-'0')*10 + int(s[i+3]-'0'); n <= 255 {
				b = append(b, byte(n))
				i += 3
				continue
			}
		}
		b = append(b, s[i+1])
		i++
	}
	return strings.ToValidUTF8(string(b), "?")
}

// mlMagicPacket is the Wake-on-LAN packet for mac.
func mlMagicPacket(mac net.HardwareAddr) []byte {
	pkt := make([]byte, 0, 6+16*len(mac))
	for i := 0; i < 6; i++ {
		pkt = append(pkt, 0xFF)
	}
	for i := 0; i < 16; i++ {
		pkt = append(pkt, mac...)
	}
	return pkt
}

// mlWake sends Wake-on-LAN magic packets for the PC: broadcast on every
// network this device is on, and straight to its last known addresses (host
// names are looked up in the background, briefly). The PC needs Wake-on-LAN
// switched on in its firmware and network adapter. Returns at once.
func mlWake(h mlHost) error {
	mac, err := net.ParseMAC(strings.TrimSpace(h.MAC))
	if err != nil || len(mac) != 6 || mlNormalizeMAC(h.MAC) == "" {
		return errMLNoMAC
	}
	pkt := mlMagicPacket(mac)
	sent := 0
	for _, port := range []int{9, 7} {
		for _, target := range psDiscoveryAddresses(port) {
			if mlSendUDP(target, pkt) == nil {
				sent++
			}
		}
	}
	var names []string
	for _, addr := range mlAddresses(h) {
		if net.ParseIP(addr) == nil {
			names = append(names, addr)
			continue
		}
		for _, port := range []int{9, 7} {
			if mlSendUDP(net.JoinHostPort(addr, strconv.Itoa(port)), pkt) == nil {
				sent++
			}
		}
	}
	if len(names) > 0 {
		go mlWakeNames(names, pkt)
	}
	if sent == 0 && len(names) == 0 {
		return errors.New("no network to send the wake-up on")
	}
	logInfo("moonlight", "Wake-on-LAN for %s (%s): %d packets", h.Label(), mlNormalizeMAC(h.MAC), sent)
	return nil
}

// mlWakeNames sends the packet to host names (a DDNS name with a forwarded
// port, say), each looked up with a short deadline.
func mlWakeNames(names []string, pkt []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), mlWakeLookupBudget)
	defer cancel()
	for _, name := range names {
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, name)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if !psWakeUnicast(ip.IP) {
				continue
			}
			for _, port := range []int{9, 7} {
				_ = mlSendUDP(net.JoinHostPort(ip.IP.String(), strconv.Itoa(port)), pkt)
			}
		}
	}
}

// mlSendUDP sends one datagram (Go turns broadcast on for UDP sockets).
func mlSendUDP(target string, pkt []byte) error {
	addr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, err = conn.Write(pkt)
	return err
}
