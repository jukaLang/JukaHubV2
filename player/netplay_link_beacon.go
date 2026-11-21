package main

// netplay_link_beacon.go — finding a link-cable or wireless-adapter partner on
// the same network, so joining is one button instead of typing an IP.
//
// A link session is not a RetroArch netplay room: on the Game Boy each
// handheld runs its own game in Gambatte and the cores talk over their own
// TCP link (port 56400), and a GBA wireless session lets each side run its own
// game (FireRed on one, LeafGreen on the other). The netplay beacon
// (netplay_rooms.go) advertises "same ROM, same core, RetroArch --connect",
// which is wrong for both, and an older JukaHub would try to join a Gambatte
// link port with RetroArch netplay. So link hosts answer a different query on
// a different UDP port: a joiner broadcasts "JUKALINK?1" to
// netplayLinkDiscoveryPort while the Netplay panel is open on a Game Boy or
// GBA game, and every JukaHub hosting a link answers with one small JSON
// object (mode, nick, game title, TCP port). Older JukaHubs never hear it.
//
// Everything that arrives is validated before it reaches RetroArch's options
// or argv: the mode is one of two words, the port is in range (Gambatte's link
// port only goes from 56400 to 56420), texts are cleaned and bounded, and the
// host is the reply's source address, never a field of the reply.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// netplayLinkQuery is the datagram a joiner broadcasts.
	netplayLinkQuery = "JUKALINK?1"
	// netplayLinkMaxReply bounds one reply; a real one is ~200 bytes.
	netplayLinkMaxReply = 768
	// netplayLinkMaxRooms bounds the sessions one scan keeps.
	netplayLinkMaxRooms = 32
)

// netplayLinkDiscoveryPort is the UDP port link hosts answer on: three above
// the netplay TCP port, beside the netplay beacon's (two above).
func netplayLinkDiscoveryPort() int { return netplayDefaultPort + 3 }

// netplayLinkBeaconInfo is the reply a link host sends.
type netplayLinkBeaconInfo struct {
	V      int    `json:"v"`
	App    string `json:"app"`
	Mode   string `json:"mode"` // netplayLinkGB or netplayLinkGBA
	Nick   string `json:"nick"`
	Title  string `json:"title"`
	System string `json:"system,omitempty"`
	Port   int    `json:"port"`
}

// netplayLinkRoom is one link session someone else is hosting.
type netplayLinkRoom struct {
	Mode   string
	Nick   string
	Title  string
	System string
	Host   string
	Port   int
	seen   time.Time
}

func (r netplayLinkRoom) key() string {
	return r.Mode + "|" + r.Host + "|" + strconv.Itoa(r.Port)
}

// netplayLinkPortOK reports whether port is usable for mode: Gambatte's link
// port option only has the values 56400 to 56420.
func netplayLinkPortOK(mode string, port int) bool {
	switch mode {
	case netplayLinkGB:
		return port >= netplayLinkGBPort && port <= netplayLinkGBPortMax
	case netplayLinkGBA:
		return port > 0 && port <= 65535
	}
	return false
}

// parseNetplayLinkBeacon validates one reply. Pure.
func parseNetplayLinkBeacon(payload []byte, from *net.UDPAddr) (netplayLinkRoom, bool) {
	if from == nil || from.IP == nil || len(payload) == 0 || len(payload) > netplayLinkMaxReply {
		return netplayLinkRoom{}, false
	}
	var info netplayLinkBeaconInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return netplayLinkRoom{}, false
	}
	if info.V != 1 || info.App != "JukaHub" || !netplayLinkPortOK(info.Mode, info.Port) {
		return netplayLinkRoom{}, false
	}
	// Gambatte's client takes the server as four IPv4 numbers.
	if info.Mode == netplayLinkGB && from.IP.To4() == nil {
		return netplayLinkRoom{}, false
	}
	title := netplayCleanText(info.Title, 80)
	if title == "" {
		title = "a game"
	}
	return netplayLinkRoom{
		Mode:   info.Mode,
		Nick:   netplayCleanText(info.Nick, 24),
		Title:  title,
		System: netplayCleanText(info.System, 40),
		Host:   from.IP.String(),
		Port:   info.Port,
	}, true
}

// ── Host side ────────────────────────────────────────────────────────────

type netplayLinkBeacon struct {
	conn net.PacketConn
	data []byte
	done chan struct{}
}

// startNetplayLinkBeacon answers link queries until Stop.
func startNetplayLinkBeacon(info netplayLinkBeaconInfo) (*netplayLinkBeacon, error) {
	info.V, info.App = 1, "JukaHub"
	data, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	if len(data) > netplayLinkMaxReply {
		return nil, errors.New("link beacon reply too large")
	}
	conn, err := net.ListenPacket("udp4", ":"+strconv.Itoa(netplayLinkDiscoveryPort()))
	if err != nil {
		return nil, err
	}
	b := &netplayLinkBeacon{conn: conn, data: data, done: make(chan struct{})}
	go b.serve()
	return b, nil
}

func (b *netplayLinkBeacon) serve() {
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
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if strings.TrimSpace(string(buf[:n])) != netplayLinkQuery {
			continue
		}
		if now := time.Now(); now.Sub(window) >= time.Second {
			window, replies = now, 0
		}
		if replies >= netplayBeaconRepliesPerSecond {
			continue
		}
		replies++
		_, _ = b.conn.WriteTo(b.data, from)
	}
}

// Stop closes the socket and waits for the reader. Safe on nil.
func (b *netplayLinkBeacon) Stop() {
	if b == nil || b.conn == nil {
		return
	}
	_ = b.conn.Close()
	<-b.done
}

// ── Joiner side ──────────────────────────────────────────────────────────

// netplayLinkDiscoverOnce broadcasts one query and collects answers.
func netplayLinkDiscoverOnce(ctx context.Context, wait time.Duration) []netplayLinkRoom {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil
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
	query := []byte(netplayLinkQuery)
	for _, target := range partyDiscoveryTargets(netplayLinkDiscoveryPort()) {
		if addr, err := net.ResolveUDPAddr("udp4", target); err == nil {
			_, _ = conn.WriteToUDP(query, addr)
		}
	}
	deadline := time.Now().Add(wait)
	found := map[string]netplayLinkRoom{}
	buf := make([]byte, netplayLinkMaxReply+64)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() || ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		// A flood of answers from one network cannot grow the panel without
		// bound: a scan keeps the first netplayLinkMaxRooms sessions.
		if room, ok := parseNetplayLinkBeacon(buf[:n], from); ok && (len(found) < netplayLinkMaxRooms || found[room.key()].Mode != "") {
			found[room.key()] = room
		}
	}
	out := make([]netplayLinkRoom, 0, len(found))
	for _, r := range found {
		out = append(out, r)
	}
	return out
}

// netplayLinkLAN holds the link rooms heard while the panel is open.
var netplayLinkLAN struct {
	mu     sync.Mutex
	rooms  map[string]netplayLinkRoom
	cancel context.CancelFunc
	rev    uint64
}

func netplayLinkLANMerge(rooms []netplayLinkRoom, now time.Time) {
	netplayLinkLAN.mu.Lock()
	defer netplayLinkLAN.mu.Unlock()
	if netplayLinkLAN.rooms == nil {
		netplayLinkLAN.rooms = map[string]netplayLinkRoom{}
	}
	changed := false
	for _, r := range rooms {
		r.seen = now
		old, had := netplayLinkLAN.rooms[r.key()]
		old.seen = now
		if !had || old != r {
			changed = true
		}
		netplayLinkLAN.rooms[r.key()] = r
	}
	for k, r := range netplayLinkLAN.rooms {
		if now.Sub(r.seen) > netplayRoomTTL {
			delete(netplayLinkLAN.rooms, k)
			changed = true
		}
	}
	if changed {
		netplayLinkLAN.rev++
	}
}

// netplayLinkLANSnapshot returns the live rooms of one mode, sorted, and a
// revision that moves whenever the list changed.
func netplayLinkLANSnapshot(mode string) ([]netplayLinkRoom, uint64) {
	netplayLinkLAN.mu.Lock()
	defer netplayLinkLAN.mu.Unlock()
	out := make([]netplayLinkRoom, 0, len(netplayLinkLAN.rooms))
	for _, r := range netplayLinkLAN.rooms {
		if r.Mode == mode {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Nick+out[i].Title), strings.ToLower(out[j].Nick+out[j].Title)
		if a != b {
			return a < b
		}
		return out[i].key() < out[j].key()
	})
	return out, netplayLinkLAN.rev
}

// netplayLinkRev is the list revision, for the panel's rebuild signature.
func netplayLinkRev() uint64 {
	netplayLinkLAN.mu.Lock()
	defer netplayLinkLAN.mu.Unlock()
	return netplayLinkLAN.rev
}

// netplayLinkStartDiscovery scans the network until netplayLinkStopDiscovery.
func netplayLinkStartDiscovery() {
	netplayLinkStopDiscovery()
	ctx, cancel := context.WithCancel(context.Background())
	netplayLinkLAN.mu.Lock()
	netplayLinkLAN.cancel = cancel
	netplayLinkLAN.rooms = map[string]netplayLinkRoom{}
	netplayLinkLAN.rev++
	netplayLinkLAN.mu.Unlock()
	go func() {
		for {
			var rooms []netplayLinkRoom
			if localIPv4() != "" {
				rooms = netplayLinkDiscoverOnce(ctx, netplayDiscoveryWait)
			}
			if ctx.Err() != nil {
				return
			}
			netplayLinkLANMerge(rooms, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-time.After(netplayDiscoveryInterval):
			}
		}
	}()
}

func netplayLinkStopDiscovery() {
	netplayLinkLAN.mu.Lock()
	cancel := netplayLinkLAN.cancel
	netplayLinkLAN.cancel = nil
	netplayLinkLAN.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
