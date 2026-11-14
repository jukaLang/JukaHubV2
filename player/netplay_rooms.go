package main

// netplay_rooms.go — finding netplay games instead of typing addresses, and
// playing beyond the home network.
//
// LAN rooms. While JukaHub hosts a LAN game it answers a tiny UDP query on
// netplayDiscoveryPort, the same way Watch Party rooms are found
// (partydiscovery.go): the joiner broadcasts "JUKANETPLAY?1" while the Netplay
// panel is open and every hosting JukaHub replies with one small JSON object
// (nick, game file, size, core, TCP port). The joiner builds the address from
// the reply's source address, finds the same ROM in its own library and starts
// RetroArch with the host's core, so joining is one button instead of typing
// an IP on a D-pad. The panel can be opened from any game: a room for another
// game is joined with the matching local ROM.
//
// Online rooms. RetroArch already has a public lobby (lobby.libretro.com) and
// relay servers that carry a game between two networks without port
// forwarding. "Host online" starts RetroArch with an --appendconfig that turns
// on the public announce and the chosen relay; "Online rooms" reads the
// lobby's JSON list and keeps the rooms whose game is in this library. A
// relayed room is joined with RetroArch's "host|port|session" --connect form
// (accepted since RetroArch 1.10), a direct one with --connect/--port. Rooms
// with a password are listed but refused with a reason: the password would
// have to be typed before launch, and RetroArch's own menu already does that.
//
// Everything that arrives from the network (beacon replies, the lobby list)
// is validated before it can reach a file path or RetroArch's argv: core names
// are plain file names, hosts are IPs or host names, sessions are base64.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// netplayDiscoveryQuery is the datagram a joiner broadcasts.
	netplayDiscoveryQuery = "JUKANETPLAY?1"
	// netplayDiscoveryMaxReply bounds one reply; a real one is ~250 bytes.
	netplayDiscoveryMaxReply = 1024
	// netplayRoomTTL keeps a room listed across a few dropped broadcasts.
	netplayRoomTTL = 9 * time.Second
	// netplayDiscoveryInterval is the pause between scans while the panel is up.
	netplayDiscoveryInterval = 2500 * time.Millisecond
	// netplayDiscoveryWait is how long one scan listens for answers.
	netplayDiscoveryWait = 1500 * time.Millisecond
	// netplayBeaconRepliesPerSecond caps how fast a host answers.
	netplayBeaconRepliesPerSecond = 20
	// netplayLobbyURL is RetroArch's public room list.
	netplayLobbyURL = "https://lobby.libretro.com/list/"
	// netplayLobbyPlainURL is its plain-http twin, used only when the
	// firmware cannot verify the certificate (the list is public data).
	netplayLobbyPlainURL = "http://lobby.libretro.com/list/"
	// netplayLobbyMaxBody bounds the list download.
	netplayLobbyMaxBody = 4 << 20
	// netplayLobbyMaxRooms bounds what is kept from one list.
	netplayLobbyMaxRooms = 1000
	// netplayRelayKey is the Custom key holding the chosen relay server.
	netplayRelayKey = "netplay_relay"
)

// netplayDiscoveryPort is the UDP port hosts answer on: two above the netplay
// TCP port, clear of RetroArch's own LAN discovery on the netplay port itself.
func netplayDiscoveryPort() int {
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_NETPLAY_DISCOVERY_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return netplayDefaultPort + 2
}

// ── Relay servers ─────────────────────────────────────────────────────────

// netplayRelay is one of RetroArch's public relay ("MITM") servers; ID is the
// value of retroarch.cfg's netplay_mitm_server.
type netplayRelay struct {
	ID    string
	Label string
}

var netplayRelays = []netplayRelay{
	{ID: "nyc", Label: "New York"},
	{ID: "madrid", Label: "Madrid"},
	{ID: "montreal", Label: "Montreal"},
	{ID: "saopaulo", Label: "Sao Paulo"},
}

// netplayRelayIndex is the configured relay's position in netplayRelays.
func netplayRelayIndex(config *Config) int {
	if config != nil {
		if raw, ok := customString(config.Variables.Custom, netplayRelayKey); ok {
			want := strings.ToLower(strings.TrimSpace(raw))
			for i, r := range netplayRelays {
				if r.ID == want {
					return i
				}
			}
		}
	}
	return 0
}

// netplayRelayFor returns the configured relay.
func netplayRelayFor(config *Config) netplayRelay {
	return netplayRelays[netplayRelayIndex(config)]
}

// netplayCycleRelay moves the relay choice by delta and saves it.
func netplayCycleRelay(config *Config, delta int) netplayRelay {
	n := len(netplayRelays)
	i := ((netplayRelayIndex(config)+delta)%n + n) % n
	relay := netplayRelays[i]
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom[netplayRelayKey] = relay.ID
		saveUserConfigDebounced(userConfigFromVariables(config))
	}
	return relay
}

// netplayAppendConfigText is what --appendconfig layers over retroarch.cfg for
// a hosted game. A LAN game switches the public announce and the relay OFF, so
// a retroarch.cfg that has them on can neither publish a living-room game to
// the world nor route it through a server abroad; an online game turns both on.
func netplayAppendConfigText(online bool, relay string) string {
	flag := "false"
	if online {
		flag = "true"
	}
	if relay == "" {
		relay = netplayRelays[0].ID
	}
	text := "netplay_public_announce = \"" + flag + "\"\n" +
		"netplay_use_mitm_server = \"" + flag + "\"\n" +
		"netplay_mitm_server = \"" + relay + "\"\n"
	if online {
		// RetroArch saves its whole config on exit, overlay included: an
		// online session must not leave every later host from RetroArch's own
		// menu public and relayed.
		text += "config_save_on_exit = \"false\"\n"
	}
	return text
}

// netplayAppendConfigDirOverride is a test seam.
var netplayAppendConfigDirOverride string

// netplayWriteAppendConfig writes the host overlay and returns its path.
func netplayWriteAppendConfig(online bool, relay string) (string, error) {
	dir := netplayAppendConfigDirOverride
	if dir == "" {
		d, err := P().DataDir()
		if err != nil || d == "" {
			d = os.TempDir()
		}
		dir = d
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "netplay-host.cfg")
	if err := os.WriteFile(path, []byte(netplayAppendConfigText(online, relay)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ── Rooms ─────────────────────────────────────────────────────────────────

type netplayRoomSource uint8

const (
	netplayRoomLAN netplayRoomSource = iota
	netplayRoomOnline
)

// netplayRoom is one game someone else is hosting.
type netplayRoom struct {
	Source  netplayRoomSource
	Nick    string
	Title   string // what the host calls the game (online: the content name)
	System  string // LAN only
	File    string // ROM file name (LAN) or content name (online)
	Size    int64  // ROM size in bytes, LAN only (0 = unknown)
	Core    string // LAN: core file stem ("mgba_libretro"); online: core name ("mGBA")
	Host    string
	Port    int
	Relay   string // relay session (online relayed rooms)
	Locked  bool   // password protected
	Country string
	seen    time.Time
}

func (r netplayRoom) key() string {
	return strconv.Itoa(int(r.Source)) + "|" + r.Host + "|" + strconv.Itoa(r.Port) + "|" + r.Relay
}

// netplaySafeCoreName accepts a plain core file stem: letters, digits and
// _ . + -, no path parts.
func netplaySafeCoreName(s string) bool {
	if s == "" || len(s) > 64 || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '.' || r == '+' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// netplaySafeHost accepts an IP address or a DNS host name.
func netplaySafeHost(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	if s == "" || len(s) > 253 || strings.HasPrefix(s, "-") || strings.HasPrefix(s, ".") {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// netplaySafeSession accepts a relay session id (base64 of 12 bytes today).
func netplaySafeSession(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '+' || r == '/' || r == '=' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// netplayCleanText strips control characters and bounds a label from the net.
func netplayCleanText(s string, limit int) string {
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

// ── LAN beacon (host side) ───────────────────────────────────────────────

// netplayBeaconInfo is the reply a hosting JukaHub sends.
type netplayBeaconInfo struct {
	V      int    `json:"v"`
	App    string `json:"app"`
	Nick   string `json:"nick"`
	Title  string `json:"title"`
	System string `json:"system,omitempty"`
	File   string `json:"file"`
	Size   int64  `json:"size,omitempty"`
	Core   string `json:"core"`
	Port   int    `json:"port"`
}

type netplayBeacon struct {
	conn net.PacketConn
	data []byte
	done chan struct{}
}

// netplayBeaconListenAddr is a test seam ("127.0.0.1:0").
var netplayBeaconListenAddr string

// startNetplayBeacon answers LAN queries until Stop. The reply never changes
// during a game, so it is marshalled once.
func startNetplayBeacon(info netplayBeaconInfo) (*netplayBeacon, error) {
	info.V, info.App = 1, "JukaHub"
	data, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	if len(data) > netplayDiscoveryMaxReply {
		return nil, errors.New("netplay beacon reply too large")
	}
	addr := netplayBeaconListenAddr
	if addr == "" {
		addr = ":" + strconv.Itoa(netplayDiscoveryPort())
	}
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return nil, err
	}
	b := &netplayBeacon{conn: conn, data: data, done: make(chan struct{})}
	go b.serve()
	return b, nil
}

func (b *netplayBeacon) serve() {
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
		if strings.TrimSpace(string(buf[:n])) != netplayDiscoveryQuery {
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

// Stop closes the socket and waits for the reader.
func (b *netplayBeacon) Stop() {
	if b == nil || b.conn == nil {
		return
	}
	_ = b.conn.Close()
	<-b.done
}

// netplayBeaconFor describes a hosted plan.
func netplayBeaconFor(plan netplayPlan) netplayBeaconInfo {
	game := plan.Session.Game
	info := netplayBeaconInfo{
		Nick:   plan.Nick,
		Title:  netplayCleanText(game.Label(), 80),
		System: netplayCleanText(game.System, 40),
		File:   netplayCleanText(filepath.Base(game.Path), 160),
		Core:   strings.TrimSuffix(filepath.Base(plan.Core), filepath.Ext(plan.Core)),
		Port:   plan.Session.Port,
	}
	if gamePath, err := safeAbsolutePath(game.Path); err == nil && !strings.Contains(gamePath, "..") {
		if st, statErr := os.Stat(gamePath); statErr == nil {
			info.Size = st.Size()
		}
	}
	return info
}

// ── LAN discovery (joiner side) ──────────────────────────────────────────

// parseNetplayBeacon validates one reply.
func parseNetplayBeacon(payload []byte, from *net.UDPAddr) (netplayRoom, bool) {
	if from == nil || len(payload) == 0 || len(payload) > netplayDiscoveryMaxReply {
		return netplayRoom{}, false
	}
	var info netplayBeaconInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return netplayRoom{}, false
	}
	if info.V != 1 || info.App != "JukaHub" || info.Port <= 0 || info.Port > 65535 ||
		!netplaySafeCoreName(info.Core) || info.Size < 0 {
		return netplayRoom{}, false
	}
	file := netplayCleanText(filepath.Base(strings.ReplaceAll(info.File, "\\", "/")), 160)
	if file == "" || file == "." || file == "/" {
		return netplayRoom{}, false
	}
	title := netplayCleanText(info.Title, 80)
	if title == "" {
		title = gameDisplayTitle(file)
	}
	return netplayRoom{
		Source: netplayRoomLAN,
		Nick:   netplayCleanText(info.Nick, 24),
		Title:  title,
		System: netplayCleanText(info.System, 40),
		File:   file,
		Size:   info.Size,
		Core:   info.Core,
		Host:   from.IP.String(),
		Port:   info.Port,
	}, true
}

// netplayDiscoverOnce broadcasts one query and collects answers.
func netplayDiscoverOnce(ctx context.Context, wait time.Duration) ([]netplayRoom, error) {
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
	query := []byte(netplayDiscoveryQuery)
	for _, target := range partyDiscoveryTargets(netplayDiscoveryPort()) {
		if addr, err := net.ResolveUDPAddr("udp4", target); err == nil {
			_, _ = conn.WriteToUDP(query, addr)
		}
	}
	deadline := time.Now().Add(wait)
	found := map[string]netplayRoom{}
	buf := make([]byte, netplayDiscoveryMaxReply+64)
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
		if room, ok := parseNetplayBeacon(buf[:n], from); ok {
			found[room.key()] = room
		}
	}
	out := make([]netplayRoom, 0, len(found))
	for _, r := range found {
		out = append(out, r)
	}
	return out, ctx.Err()
}

// netplayLAN holds the rooms heard while the panel is open.
var netplayLAN struct {
	mu     sync.Mutex
	rooms  map[string]netplayRoom
	cancel context.CancelFunc
	rev    uint64
}

func netplayLANMerge(rooms []netplayRoom, now time.Time) {
	netplayLAN.mu.Lock()
	defer netplayLAN.mu.Unlock()
	if netplayLAN.rooms == nil {
		netplayLAN.rooms = map[string]netplayRoom{}
	}
	changed := false
	for _, r := range rooms {
		r.seen = now
		old, had := netplayLAN.rooms[r.key()]
		old.seen = now
		if !had || old != r {
			changed = true
		}
		netplayLAN.rooms[r.key()] = r
	}
	for k, r := range netplayLAN.rooms {
		if now.Sub(r.seen) > netplayRoomTTL {
			delete(netplayLAN.rooms, k)
			changed = true
		}
	}
	// The panel rebuilds its rows on a new revision; a scan that heard the
	// same rooms again is not one.
	if changed {
		netplayLAN.rev++
	}
}

// netplayLANSnapshot returns the live rooms, sorted, and a revision that
// changes whenever a scan lands.
func netplayLANSnapshot() ([]netplayRoom, uint64) {
	netplayLAN.mu.Lock()
	defer netplayLAN.mu.Unlock()
	out := make([]netplayRoom, 0, len(netplayLAN.rooms))
	for _, r := range netplayLAN.rooms {
		out = append(out, r)
	}
	sortNetplayRooms(out)
	return out, netplayLAN.rev
}

func sortNetplayRooms(rooms []netplayRoom) {
	sort.SliceStable(rooms, func(i, j int) bool {
		a, b := strings.ToLower(rooms[i].Title), strings.ToLower(rooms[j].Title)
		if a != b {
			return a < b
		}
		return rooms[i].key() < rooms[j].key()
	})
}

// netplayStartDiscovery scans the LAN until netplayStopDiscovery.
func netplayStartDiscovery() {
	netplayStopDiscovery()
	ctx, cancel := context.WithCancel(context.Background())
	netplayLAN.mu.Lock()
	netplayLAN.cancel = cancel
	netplayLAN.rooms = map[string]netplayRoom{}
	netplayLAN.rev++
	netplayLAN.mu.Unlock()
	go func() {
		for {
			// No LAN address, no one to ask (as partyDiscoveryLoop does).
			var rooms []netplayRoom
			if localIPv4() != "" {
				rooms, _ = netplayDiscoverOnce(ctx, netplayDiscoveryWait)
			}
			if ctx.Err() != nil {
				return
			}
			netplayLANMerge(rooms, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-time.After(netplayDiscoveryInterval):
			}
		}
	}()
}

func netplayStopDiscovery() {
	netplayLAN.mu.Lock()
	cancel := netplayLAN.cancel
	netplayLAN.cancel = nil
	netplayLAN.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ── Online lobby ─────────────────────────────────────────────────────────

// netplayLobbyEntry is one room of lobby.libretro.com/list/.
type netplayLobbyEntry struct {
	Username    string `json:"username"`
	Country     string `json:"country"`
	GameName    string `json:"game_name"`
	CoreName    string `json:"core_name"`
	IP          string `json:"ip"`
	Port        int    `json:"port"`
	MitmIP      string `json:"mitm_ip"`
	MitmPort    int    `json:"mitm_port"`
	MitmSession string `json:"mitm_session"`
	HasPassword bool   `json:"has_password"`
	Connectable *bool  `json:"connectable"`
}

// parseNetplayLobby reads the lobby list. Entries are either flat objects
// (the Go lobby server) or wrapped in {"fields": {...}} (the older one).
func parseNetplayLobby(data []byte) ([]netplayRoom, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("lobby list: %w", err)
	}
	rooms := make([]netplayRoom, 0, len(raw))
	for _, item := range raw {
		if len(rooms) >= netplayLobbyMaxRooms {
			break
		}
		var wrap struct {
			Fields *netplayLobbyEntry `json:"fields"`
		}
		var e netplayLobbyEntry
		if json.Unmarshal(item, &wrap) == nil && wrap.Fields != nil {
			e = *wrap.Fields
		} else if json.Unmarshal(item, &e) != nil {
			continue
		}
		room := netplayRoom{
			Source:  netplayRoomOnline,
			Nick:    netplayCleanText(e.Username, 24),
			Title:   netplayCleanText(e.GameName, 120),
			File:    netplayCleanText(e.GameName, 160),
			Core:    netplayCleanText(e.CoreName, 48),
			Locked:  e.HasPassword,
			Country: netplayCleanText(e.Country, 4),
		}
		mitmIP := strings.TrimSpace(e.MitmIP)
		session := strings.TrimSpace(e.MitmSession)
		switch {
		case mitmIP != "" && session != "" && e.MitmPort > 0 && e.MitmPort <= 65535:
			if !netplaySafeHost(mitmIP) || !netplaySafeSession(session) {
				continue
			}
			room.Host, room.Port, room.Relay = mitmIP, e.MitmPort, session
		default:
			if e.Connectable != nil && !*e.Connectable {
				continue
			}
			ip := strings.TrimSpace(e.IP)
			if !netplaySafeHost(ip) || e.Port <= 0 || e.Port > 65535 {
				continue
			}
			room.Host, room.Port = ip, e.Port
		}
		if room.Title == "" || room.Core == "" {
			continue
		}
		rooms = append(rooms, room)
	}
	return rooms, nil
}

var netplayOnline struct {
	mu      sync.Mutex
	rooms   []netplayRoom
	fetched time.Time
	busy    bool
	err     string
	rev     uint64
}

// netplayOnlineSnapshot returns the last list, whether a fetch is running and
// the last error.
func netplayOnlineSnapshot() ([]netplayRoom, bool, string, uint64) {
	netplayOnline.mu.Lock()
	defer netplayOnline.mu.Unlock()
	return append([]netplayRoom(nil), netplayOnline.rooms...), netplayOnline.busy, netplayOnline.err, netplayOnline.rev
}

// netplayLobbyGetOverride is a test seam for the HTTP fetch.
var netplayLobbyGetOverride func(ctx context.Context) ([]byte, error)

func netplayLobbyClient() *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	if pool := bugrepRootCAs(); pool != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr, Timeout: 25 * time.Second}
}

func netplayLobbyGet(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lobby answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, netplayLobbyMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > netplayLobbyMaxBody {
		return nil, errors.New("lobby list too large")
	}
	return data, nil
}

func netplayFetchLobby(ctx context.Context) ([]netplayRoom, error) {
	var data []byte
	var err error
	if netplayLobbyGetOverride != nil {
		data, err = netplayLobbyGetOverride(ctx)
	} else {
		client := netplayLobbyClient()
		data, err = netplayLobbyGet(ctx, client, netplayLobbyURL)
		if err != nil && artworkCertError(err) {
			data, err = netplayLobbyGet(ctx, client, netplayLobbyPlainURL)
		}
	}
	if err != nil {
		return nil, err
	}
	return parseNetplayLobby(data)
}

// netplayRefreshLobby fetches the online list in the background. A fetch
// already running is not doubled.
func netplayRefreshLobby() {
	netplayOnline.mu.Lock()
	if netplayOnline.busy {
		netplayOnline.mu.Unlock()
		return
	}
	netplayOnline.busy = true
	netplayOnline.rev++
	netplayOnline.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rooms, err := netplayFetchLobby(ctx)
		netplayOnline.mu.Lock()
		defer netplayOnline.mu.Unlock()
		netplayOnline.busy = false
		netplayOnline.rev++
		if err != nil {
			netplayOnline.err = "Online rooms unavailable — " + netplayShortError(err)
			return
		}
		netplayOnline.err = ""
		netplayOnline.rooms = rooms
		netplayOnline.fetched = time.Now()
	}()
}

// netplayShortError turns a network error into a few words.
func netplayShortError(err error) string {
	var ne net.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return "the lobby did not answer"
	case strings.Contains(err.Error(), "no such host"):
		return "no internet connection"
	}
	return netplayCleanText(err.Error(), 60)
}

// ── Matching a room to this device ───────────────────────────────────────

// netplayNormName folds a ROM or content name for matching: the extension
// goes, then everything that is not a letter or digit.
func netplayNormName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if ext := filepath.Ext(name); ext != "" && len(ext) <= 6 && !strings.ContainsAny(ext, " )]") {
		name = strings.TrimSuffix(name, ext)
	}
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// netplayGameIndex maps folded ROM names to library games, rebuilt when the
// library is rescanned.
var netplayGameIndex struct {
	key    netplayIndexKey
	valid  bool
	byName map[string][]libraryGame
}

// netplayIndexKey identifies one built library. scanGen moves when a scan
// STARTS, so the systems slice itself is part of the key: the index follows
// the result when it lands.
type netplayIndexKey struct {
	gen     uint64
	systems *librarySystem
	n       int
	total   int
}

// netplayLibraryIndex builds or returns the index (render thread: it reads
// gameLib).
func netplayLibraryIndex() map[string][]libraryGame {
	key := netplayIndexKey{gen: gameLib.scanGen, n: len(gameLib.lib.Systems), total: gameLib.lib.Total}
	if key.n > 0 {
		key.systems = &gameLib.lib.Systems[0]
	}
	if netplayGameIndex.valid && netplayGameIndex.key == key {
		return netplayGameIndex.byName
	}
	byName := map[string][]libraryGame{}
	for _, system := range gameLib.lib.Systems {
		if system.Name == androidLibrarySystem {
			continue
		}
		for _, g := range system.Games {
			if g.System == androidLibrarySystem || strings.TrimSpace(g.Path) == "" {
				continue
			}
			if key := netplayNormName(g.Path); key != "" {
				byName[key] = append(byName[key], g)
			}
		}
	}
	netplayGameIndex.key, netplayGameIndex.valid, netplayGameIndex.byName = key, true, byName
	return byName
}

// netplayMatchGame finds this device's copy of a room's game. A LAN room
// carries the exact file name and size, so a same-name same-size file wins.
// An online room only has a name and a core, and "Tetris" exists for half the
// consoles: the copy whose console uses the room's core wins.
func netplayMatchGame(room netplayRoom, index map[string][]libraryGame) (libraryGame, bool) {
	cands := index[netplayNormName(room.File)]
	if len(cands) == 0 {
		return libraryGame{}, false
	}
	if room.Source == netplayRoomOnline {
		for _, g := range cands {
			if netplaySystemUsesCore(g.System, room.Core) {
				return g, true
			}
		}
	}
	if room.Source == netplayRoomLAN {
		for _, g := range cands {
			if !strings.EqualFold(filepath.Base(g.Path), room.File) {
				continue
			}
			if room.Size <= 0 {
				return g, true
			}
			if st, err := os.Stat(g.Path); err == nil && st.Size() == room.Size {
				return g, true
			}
		}
	}
	return cands[0], true
}

// netplayCoreNorm folds a core name for comparing the lobby's display name
// ("Genesis Plus GX", "Beetle PCE Fast") with a file stem
// ("genesis_plus_gx_libretro", "mednafen_pce_fast_libretro").
func netplayCoreNorm(name string) string {
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.TrimSuffix(name, "_libretro")
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	for _, prefix := range []string{"beetle", "mednafen"} {
		if strings.HasPrefix(out, prefix) && len(out) > len(prefix) {
			return strings.TrimPrefix(out, prefix)
		}
	}
	return out
}

// netplaySystemUsesCore reports whether core (a lobby name or a file stem) is
// one of the netplay cores mapped for system.
func netplaySystemUsesCore(system, core string) bool {
	want := netplayCoreNorm(core)
	if want == "" {
		return false
	}
	for _, c := range netplayCoreCandidates(system) {
		if netplayCoreNorm(c) == want {
			return true
		}
	}
	return false
}

// netplayInstalledCores lists core files in dirs, keyed by folded name.
func netplayInstalledCores(dirs []string) map[string]string {
	out := map[string]string{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if e.IsDir() || ext != ".so" && ext != ".dll" && ext != ".dylib" {
				continue
			}
			key := netplayCoreNorm(name)
			if _, dup := out[key]; key != "" && !dup {
				out[key] = filepath.Join(dir, name)
			}
		}
	}
	return out
}

// netplayRoomCore resolves the core a room needs: RetroArch only lets two
// players in when both run the same core.
func netplayRoomCore(room netplayRoom, installed map[string]string) (string, error) {
	if room.Source == netplayRoomLAN {
		if !netplaySafeCoreName(room.Core) {
			return "", errors.New("the host sent an unusable core name")
		}
		if p, ok := installed[netplayCoreNorm(room.Core)]; ok {
			return p, nil
		}
		return "", fmt.Errorf("needs the %s core the host uses — install it from Emulators",
			strings.TrimSuffix(room.Core, "_libretro"))
	}
	if p, ok := installed[netplayCoreNorm(room.Core)]; ok {
		return p, nil
	}
	return "", fmt.Errorf("needs the %s core — install it from Emulators", room.Core)
}

// netplayPlanForRoom builds a join for a discovered room. retroArch is the
// resolved binary ("" = not installed), looked up once per row rebuild.
func netplayPlanForRoom(room netplayRoom, index map[string][]libraryGame, installed map[string]string, retroArch string) (netplayPlan, error) {
	if room.Locked {
		return netplayPlan{}, errors.New("this room has a password — join it from RetroArch's own Netplay menu")
	}
	if !netplaySafeHost(room.Host) || room.Port <= 0 || room.Port > 65535 {
		return netplayPlan{}, errors.New("this room's address is not usable")
	}
	if room.Relay != "" && !netplaySafeSession(room.Relay) {
		return netplayPlan{}, errors.New("this room's relay session is not usable")
	}
	game, ok := netplayMatchGame(room, index)
	if !ok {
		return netplayPlan{}, fmt.Errorf("%s is not in your library", room.Title)
	}
	if room.Source == netplayRoomOnline && !netplaySystemUsesCore(game.System, room.Core) {
		// A same-named game for another console: launching it with this
		// core would fail inside RetroArch, or worse, load.
		return netplayPlan{}, fmt.Errorf("runs %s, which your %s copy cannot use", room.Core, game.System)
	}
	if !regularFile(game.Path) {
		return netplayPlan{}, fmt.Errorf("ROM is missing: %s", filepath.Base(game.Path))
	}
	plan := netplayPlan{Nick: netplayNickname(), RetroArch: retroArch}
	if plan.RetroArch == "" {
		return netplayPlan{}, errors.New("RetroArch not found — install it from Emulators, or set JUKAHUB_RETROARCH")
	}
	core, err := netplayRoomCore(room, installed)
	if err != nil {
		return netplayPlan{}, err
	}
	plan.Core = core
	plan.Session = netplaySession{Role: netplayJoin, Game: game, Host: room.Host, Port: room.Port, Relay: room.Relay}
	return plan, nil
}
