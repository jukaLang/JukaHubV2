package main

// netplay_invite.go — GAME-10: shareable invites, locked rooms, and plain
// RetroArch hosts on the LAN.
//
// RetroArch answers its LAN discovery query on UDP 55435 with a fixed
// struct ad_packet (network/netplay/netplay_frontend.c): the RANS magic, the
// content CRC, the TCP port, a password flag, then NUL-padded nick, frontend,
// core, versions, content and subsystem names. A PC running plain RetroArch
// is therefore listed beside the JukaHubs that answer the JSON beacon.
//
// An invite is a jukahub://netplay link with the host, port, game and core.
// It names no password: a locked room still asks for it on this device, and
// the password reaches RetroArch only through a one-launch --appendconfig.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// netplayRetroArchDiscoveryPort is RetroArch's LAN discovery port.
	netplayRetroArchDiscoveryPort = 55435
	// netplayRetroArchResponseMagic is "RANS" in network byte order.
	netplayRetroArchResponseMagic = 0x52414E53
	// netplayRetroArchQuery is "RANQ" in network byte order.
	netplayRetroArchQuery = "\x52\x41\x4e\x51"
	// netplayRetroArchMinLen covers the header through the content name's
	// first byte: a shorter packet cannot name a game.
	netplayRetroArchMinLen = 176
	// netplayRetroArchCoreAt and netplayRetroArchContentAt are the ad_packet
	// field offsets (NETPLAY_NICK_LEN 32, NETPLAY_HOST_STR_LEN 32,
	// NETPLAY_HOST_LONGSTR_LEN 256).
	netplayRetroArchCoreAt    = 80
	netplayRetroArchContentAt = 176
	// netplayInviteScheme is the URL scheme of a shared room.
	netplayInviteScheme = "jukahub"
	// netplayPassVKTarget aims the on-screen keyboard at a room's password
	// (masked, submit only).
	netplayPassVKTarget vkTargetKind = 3610
)

// netplayPassBuffer is the room password being typed; cleared when the
// keyboard closes. Main thread.
var netplayPassBuffer string

// netplayPasswordRow is the locked room waiting for that password.
var netplayPasswordRow netplayRow

var errNetplayLocked = errors.New("this room has a password")

// netplayCString reads a NUL-padded field from an ad_packet.
func netplayCString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return netplayCleanText(string(b), len(b))
}

// parseRetroArchAnnouncement reads a plain RetroArch host's discovery reply.
// from is the datagram's source: that address is the host.
func parseRetroArchAnnouncement(payload []byte, from *net.UDPAddr) (netplayRoom, bool) {
	if from == nil || len(payload) < netplayRetroArchMinLen || len(payload) > 2048 {
		return netplayRoom{}, false
	}
	if binary.BigEndian.Uint32(payload[0:4]) != netplayRetroArchResponseMagic {
		return netplayRoom{}, false
	}
	port := int(int32(binary.BigEndian.Uint32(payload[8:12])))
	if port <= 0 || port > 65535 {
		return netplayRoom{}, false
	}
	locked := binary.BigEndian.Uint32(payload[12:16]) != 0
	nick := netplayCString(payload[16:48])
	coreEnd := netplayRetroArchCoreAt + 32
	if len(payload) < coreEnd {
		return netplayRoom{}, false
	}
	core := netplayCString(payload[netplayRetroArchCoreAt:coreEnd])
	if !netplaySafeCoreName(core) {
		return netplayRoom{}, false
	}
	contentEnd := netplayRetroArchContentAt + 256
	if len(payload) < contentEnd {
		contentEnd = len(payload)
	}
	content := netplayCString(payload[netplayRetroArchContentAt:contentEnd])
	content = strings.ReplaceAll(content, "\\", "/")
	file := netplayCleanText(filepath.Base(content), 160)
	if file == "" || file == "." || file == "/" {
		return netplayRoom{}, false
	}
	return netplayRoom{
		Source: netplayRoomLAN,
		Nick:   nick,
		Title:  gameDisplayTitle(file),
		File:   file,
		Core:   core,
		Host:   from.IP.String(),
		Port:   port,
		Locked: locked,
	}, true
}

// netplayInviteLink is the link a hosted game is shared as. It carries no
// password; a locked room asks for it on the device joining.
func netplayInviteLink(plan netplayPlan) string {
	session := plan.Session
	port := session.Port
	if port <= 0 || port > 65535 {
		port = netplayDefaultPort
	}
	core := strings.TrimSuffix(filepath.Base(plan.Core), filepath.Ext(plan.Core))
	q := url.Values{}
	q.Set("host", session.Host)
	q.Set("port", strconv.Itoa(port))
	q.Set("game", filepath.Base(session.Game.Path))
	q.Set("core", core)
	if nick := strings.TrimSpace(plan.Nick); nick != "" {
		q.Set("nick", nick)
	}
	if session.Relay != "" {
		q.Set("relay", session.Relay)
	}
	return netplayInviteScheme + "://netplay?" + q.Encode()
}

// parseNetplayInvite reads a link back into a room. Everything that reaches
// RetroArch's argv is validated here.
func parseNetplayInvite(raw string) (netplayRoom, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return netplayRoom{}, errors.New("that is not a JukaHub invite link")
	}
	if !strings.EqualFold(u.Scheme, netplayInviteScheme) || !strings.EqualFold(u.Host, "netplay") {
		return netplayRoom{}, errors.New("that is not a JukaHub invite link")
	}
	q := u.Query()
	host := strings.TrimSpace(q.Get("host"))
	if !netplaySafeHost(host) {
		return netplayRoom{}, errors.New("the invite's address is not usable")
	}
	port, err := strconv.Atoi(strings.TrimSpace(q.Get("port")))
	if err != nil || port <= 0 || port > 65535 {
		return netplayRoom{}, errors.New("the invite's port is not usable")
	}
	game := strings.TrimSpace(q.Get("game"))
	if game == "" || game == "." || game == "/" || filepath.Base(game) != game {
		return netplayRoom{}, errors.New("the invite does not name a game")
	}
	core := strings.TrimSpace(q.Get("core"))
	if !netplaySafeCoreName(core) {
		return netplayRoom{}, errors.New("the invite's core is not usable")
	}
	relay := strings.TrimSpace(q.Get("relay"))
	if relay != "" && !netplaySafeSession(relay) {
		return netplayRoom{}, errors.New("the invite's relay session is not usable")
	}
	return netplayRoom{
		Source: netplayRoomLAN,
		Nick:   netplayCleanText(q.Get("nick"), 24),
		Title:  gameDisplayTitle(game),
		File:   game,
		Core:   core,
		Host:   host,
		Port:   port,
		Relay:  relay,
	}, nil
}

// netplayPluginInviteLink is netplayInviteLink for a room whose plan is not
// resolved yet (the panel's Share row), with the same validation.
func netplayRoomInviteLink(room netplayRoom) string {
	core := strings.TrimSuffix(filepath.Base(room.Core), filepath.Ext(room.Core))
	q := url.Values{}
	q.Set("host", room.Host)
	q.Set("port", strconv.Itoa(room.Port))
	q.Set("game", filepath.Base(room.File))
	q.Set("core", core)
	if room.Nick != "" {
		q.Set("nick", room.Nick)
	}
	if room.Relay != "" {
		q.Set("relay", room.Relay)
	}
	return netplayInviteScheme + "://netplay?" + q.Encode()
}

// netplayPasswordConfigText is the one-launch overlay that gives RetroArch a
// room's password. A quote or line break cannot be stored in its config
// format, so it is refused rather than written wrong.
func netplayPasswordConfigText(password string) (string, error) {
	if password == "" {
		return "", errors.New("the password is empty")
	}
	if len(password) > 128 || strings.ContainsAny(password, "\"\r\n") {
		return "", errors.New("that password cannot travel in RetroArch's settings")
	}
	return "netplay_password = \"" + password + "\"\n", nil
}

// netplayWritePasswordConfig writes that overlay and returns its path.
func netplayWritePasswordConfig(password string) (string, error) {
	text, err := netplayPasswordConfigText(password)
	if err != nil {
		return "", err
	}
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
	path := filepath.Join(dir, "netplay-join.cfg")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// netplayJoinInvite opens a shared link on this device: the game and core are
// resolved exactly as a discovered room's are.
func netplayJoinInvite(raw string) (netplayPlan, error) {
	room, err := parseNetplayInvite(raw)
	if err != nil {
		return netplayPlan{}, err
	}
	return netplayPlanForRoom(room, netplayLibraryIndex(), netplayInstalledCores(netplayCoreDirs()), netplayRetroArchPath(), "")
}

// netplayInviteIsLink reports whether raw is a JukaHub netplay invite, for the
// companion's Open field and the panel's address row.
func netplayInviteIsLink(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(u.Scheme, netplayInviteScheme) && strings.EqualFold(u.Host, "netplay")
}

// netplayInviteErrorText keeps a bad link's reason short enough for a toast.
func netplayInviteErrorText(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
