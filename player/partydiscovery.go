package main

// partydiscovery.go — finding Watch Party rooms on the LAN without typing an
// address.
//
// Joining used to mean reading "192.168.1.5:8318 ABCD2" off a friend's screen
// and typing it on a pad. Now a host also answers a tiny UDP query, and a
// device that is not in a room asks the network every couple of seconds while
// the Watch Party screen is open, so the rooms around it are listed live and
// join with one press. Typing a bare room code works too: the code is looked up
// among the answers.
//
// The protocol is additive and separate from the room API: the query is the
// ASCII datagram "JUKAPARTY?1" sent to the discovery port (the party port + 1)
// as a broadcast, and a host replies to the sender with one small JSON object.
// The joiner builds the room address from the reply's source address and the
// advertised TCP port, so a host with several interfaces cannot advertise an
// address the joiner cannot reach. A build without discovery still joins by
// address and code, and a host that is not discoverable is simply not listed.
//
// Like the room listener this is a home-network feature: the reply carries the
// room code, so anyone on the same Wi-Fi who opens the screen can join. A host
// can hide the room from the list (the code still works when typed).

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// partyDiscoveryQuery is the datagram a joiner broadcasts.
	partyDiscoveryQuery = "JUKAPARTY?1"
	// partyDiscoveryMaxReply bounds a reply; a real one is about 200 bytes.
	partyDiscoveryMaxReply = 1024
	// partyNearbyTTL is how long a room stays listed after its last answer: a
	// few missed scans (a busy access point drops broadcasts) must not make it
	// flicker, and a room that closed must leave the list soon after.
	partyNearbyTTL = 9 * time.Second
	// partyDiscoveryInterval is the pause between scans while the screen is up.
	partyDiscoveryInterval = 2500 * time.Millisecond
	// partyDiscoveryWait is how long one scan listens for answers.
	partyDiscoveryWait = 1500 * time.Millisecond
	// partyBeaconRepliesPerSecond caps how fast a host answers, so a flood of
	// queries cannot turn the handheld into a reflector on its own network.
	partyBeaconRepliesPerSecond = 20
)

// partyDiscoveryPort is the UDP port hosts listen on.
func partyDiscoveryPort() int {
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_PARTY_DISCOVERY_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return partyDefaultPort + 1
}

// partyBeaconInfo is the reply a host sends.
type partyBeaconInfo struct {
	V       int    `json:"v"`
	App     string `json:"app"`
	Code    string `json:"code"`
	Room    string `json:"room"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Members int    `json:"members"`
	Title   string `json:"title,omitempty"`
}

// partyNearbyRoom is one room heard on the LAN.
type partyNearbyRoom struct {
	// Addr is host:port built from the reply's source address.
	Addr    string
	Code    string
	Room    string
	Host    string
	Members int
	Title   string
	Seen    time.Time
}

// Invite is the invite line partyJoin takes.
func (r partyNearbyRoom) Invite() string { return r.Addr + " " + r.Code }

// partyDiscoverable is whether this device's room answers queries. A host turns
// it off to keep a room private; the code still works when typed.
var partyDiscoverable atomic.Bool

func init() { partyDiscoverable.Store(true) }

// partyCleanLabel strips control characters and bounds a label from the
// network: it is drawn on screen and must not carry escape codes or a page of
// text.
func partyCleanLabel(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if runes := []rune(s); len(runes) > limit {
		s = string(runes[:limit])
	}
	return s
}

// partyParseBeacon reads one reply. from is the sender; ok is false for
// anything that is not a well-formed JukaHub answer.
func partyParseBeacon(payload []byte, from *net.UDPAddr) (partyNearbyRoom, bool) {
	if from == nil || from.IP == nil || len(payload) == 0 || len(payload) > partyDiscoveryMaxReply {
		return partyNearbyRoom{}, false
	}
	var info partyBeaconInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return partyNearbyRoom{}, false
	}
	if info.V != 1 || info.App != "JukaHub" || !partyCodeValid(info.Code) || info.Port <= 0 || info.Port > 65535 {
		return partyNearbyRoom{}, false
	}
	members := info.Members
	if members < 0 {
		members = 0
	}
	if members > partyMaxMembers {
		members = partyMaxMembers
	}
	return partyNearbyRoom{
		Addr:    net.JoinHostPort(from.IP.String(), strconv.Itoa(info.Port)),
		Code:    strings.ToUpper(info.Code),
		Room:    partyCleanLabel(info.Room, partyNameLimit),
		Host:    partyCleanLabel(info.Host, partyNameLimit),
		Members: members,
		Title:   partyCleanLabel(info.Title, partyQueueTitleLimit),
		Seen:    time.Now(),
	}, true
}

// ── Host side ─────────────────────────────────────────────────────────────

// partyBeacon answers discovery queries for the room this device hosts.
type partyBeacon struct {
	conn net.PacketConn
	info func() (partyBeaconInfo, bool)
	done chan struct{}
}

// partyBeaconListenAddr is a test seam: tests bind to 127.0.0.1:0 instead of the
// fixed port on every interface.
var partyBeaconListenAddr string

// startPartyBeacon starts answering. info is read on every query, so the reply
// always carries the current member count and title.
func startPartyBeacon(info func() (partyBeaconInfo, bool)) (*partyBeacon, error) {
	addr := partyBeaconListenAddr
	if addr == "" {
		addr = ":" + strconv.Itoa(partyDiscoveryPort())
	}
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return nil, err
	}
	b := &partyBeacon{conn: conn, info: info, done: make(chan struct{})}
	go b.serve()
	return b, nil
}

// Port is the UDP port the beacon listens on.
func (b *partyBeacon) Port() int {
	if b == nil || b.conn == nil {
		return 0
	}
	if ua, ok := b.conn.LocalAddr().(*net.UDPAddr); ok {
		return ua.Port
	}
	return 0
}

func (b *partyBeacon) serve() {
	defer close(b.done)
	buf := make([]byte, 64)
	window := time.Now()
	replies := 0
	for {
		n, from, err := b.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Windows reports an ICMP "port unreachable" from an earlier reply
			// as a read error on the next read; it is not fatal.
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if strings.TrimSpace(string(buf[:n])) != partyDiscoveryQuery {
			continue
		}
		if now := time.Now(); now.Sub(window) >= time.Second {
			window, replies = now, 0
		}
		if replies >= partyBeaconRepliesPerSecond {
			continue
		}
		info, ok := b.info()
		if !ok {
			continue
		}
		data, err := json.Marshal(info)
		if err != nil || len(data) > partyDiscoveryMaxReply {
			continue
		}
		replies++
		_, _ = b.conn.WriteTo(data, from)
	}
}

// Stop closes the socket and waits for the reader to exit.
func (b *partyBeacon) Stop() {
	if b == nil || b.conn == nil {
		return
	}
	_ = b.conn.Close()
	<-b.done
}

// ── Joiner side ───────────────────────────────────────────────────────────

// partyDiscoveryTargetsOverride is a test seam: a scan sends to these
// addresses instead of broadcasting.
var partyDiscoveryTargetsOverride []string

// partyDiscoveryTargets lists where a scan is sent: the limited broadcast, plus
// each interface's directed broadcast for networks that drop the former.
func partyDiscoveryTargets(port int) []string {
	if len(partyDiscoveryTargetsOverride) > 0 {
		return append([]string(nil), partyDiscoveryTargetsOverride...)
	}
	p := strconv.Itoa(port)
	out := []string{net.JoinHostPort("255.255.255.255", p)}
	seen := map[string]bool{out[0]: true}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if b := partyDirectedBroadcast(ipNet); b != "" {
				target := net.JoinHostPort(b, p)
				if !seen[target] {
					seen[target] = true
					out = append(out, target)
				}
			}
		}
	}
	return out
}

// partyDirectedBroadcast is the broadcast address of an IPv4 network, or "".
// Masks come as 4 or 16 bytes depending on the platform.
func partyDirectedBroadcast(n *net.IPNet) string {
	if n == nil {
		return ""
	}
	ip := n.IP.To4()
	if ip == nil {
		return ""
	}
	mask := n.Mask
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	}
	if len(mask) != net.IPv4len {
		return ""
	}
	out := make(net.IP, net.IPv4len)
	for i := 0; i < net.IPv4len; i++ {
		out[i] = ip[i] | ^mask[i]
	}
	return out.String()
}

// partyDiscoverOnce broadcasts one query and collects answers until wait ends
// or ctx is cancelled.
func partyDiscoverOnce(ctx context.Context, wait time.Duration) ([]partyNearbyRoom, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	query := []byte(partyDiscoveryQuery)
	for _, target := range partyDiscoveryTargets(partyDiscoveryPort()) {
		addr, err := net.ResolveUDPAddr("udp4", target)
		if err != nil {
			continue
		}
		_, _ = conn.WriteToUDP(query, addr)
	}

	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	found := map[string]partyNearbyRoom{}
	buf := make([]byte, partyDiscoveryMaxReply+64)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break
			}
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if room, ok := partyParseBeacon(buf[:n], from); ok {
			found[room.Addr+"|"+room.Code] = room
		}
	}
	out := make([]partyNearbyRoom, 0, len(found))
	for _, r := range found {
		out = append(out, r)
	}
	partySortNearby(out)
	return out, ctx.Err()
}

// partySortNearby orders rooms by name, then address, so the list does not
// shuffle between scans.
func partySortNearby(rooms []partyNearbyRoom) {
	sort.Slice(rooms, func(i, j int) bool {
		a, b := strings.ToLower(rooms[i].Room), strings.ToLower(rooms[j].Room)
		if a != b {
			return a < b
		}
		return rooms[i].Addr < rooms[j].Addr
	})
}

// partyNearby is the live list the screen draws.
var partyNearby struct {
	mu       sync.Mutex
	rooms    map[string]partyNearbyRoom
	lastScan time.Time
	scans    int
	err      string
}

// partyNearbyMerge folds one scan into the list and drops rooms not heard from
// within the TTL.
func partyNearbyMerge(rooms []partyNearbyRoom, scanErr error, now time.Time) {
	partyNearby.mu.Lock()
	defer partyNearby.mu.Unlock()
	if partyNearby.rooms == nil {
		partyNearby.rooms = make(map[string]partyNearbyRoom)
	}
	for _, r := range rooms {
		r.Seen = now
		partyNearby.rooms[r.Addr+"|"+r.Code] = r
	}
	for key, r := range partyNearby.rooms {
		if now.Sub(r.Seen) > partyNearbyTTL {
			delete(partyNearby.rooms, key)
		}
	}
	partyNearby.lastScan = now
	partyNearby.scans++
	partyNearby.err = ""
	if scanErr != nil && !errors.Is(scanErr, context.Canceled) {
		partyNearby.err = scanErr.Error()
	}
}

// partyNearbySnapshot returns the rooms heard recently and how many scans have
// completed (0 means the first scan is still running).
func partyNearbySnapshot() ([]partyNearbyRoom, int) {
	partyNearby.mu.Lock()
	defer partyNearby.mu.Unlock()
	out := make([]partyNearbyRoom, 0, len(partyNearby.rooms))
	now := time.Now()
	for _, r := range partyNearby.rooms {
		if now.Sub(r.Seen) <= partyNearbyTTL {
			out = append(out, r)
		}
	}
	partySortNearby(out)
	return out, partyNearby.scans
}

// partyNearbyReset forgets the list, so a new visit starts with a fresh scan
// rather than rooms heard minutes ago.
func partyNearbyReset() {
	partyNearby.mu.Lock()
	partyNearby.rooms = nil
	partyNearby.scans = 0
	partyNearby.err = ""
	partyNearby.mu.Unlock()
}

// partyFindNearbyCode returns the listed room with code.
func partyFindNearbyCode(code string) (partyNearbyRoom, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	rooms, _ := partyNearbySnapshot()
	for _, r := range rooms {
		if r.Code == code {
			return r, true
		}
	}
	return partyNearbyRoom{}, false
}

// partyDiscoveryLoop scans while ctx lives: the Watch Party screen starts it as
// a scene task, so leaving the screen stops it. It pauses while the device is
// in a room or has no network address.
func partyDiscoveryLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if !partyActive() && partyLANAddress() != "" {
			rooms, err := partyDiscoverOnce(ctx, partyDiscoveryWait)
			if ctx.Err() != nil {
				return
			}
			partyNearbyMerge(rooms, err, time.Now())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(partyDiscoveryInterval):
		}
	}
}

// partyLANCache keeps the LAN address for a couple of seconds: the screen asks
// every frame and an interface walk is a syscall per interface.
var partyLANCache struct {
	mu sync.Mutex
	ip string
	at time.Time
}

// partyLANAddress is this device's LAN IPv4 address, or "" with no network.
func partyLANAddress() string {
	partyLANCache.mu.Lock()
	defer partyLANCache.mu.Unlock()
	if !partyLANCache.at.IsZero() && time.Since(partyLANCache.at) < 2*time.Second {
		return partyLANCache.ip
	}
	partyLANCache.ip = localIPv4()
	partyLANCache.at = time.Now()
	return partyLANCache.ip
}
