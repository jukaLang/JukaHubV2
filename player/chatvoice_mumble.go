package main

// chatvoice_mumble.go — free voice rooms, with nothing to sign up for or host.
//
// Chat's Voice tab joins one shared room on a public Mumble server. Mumble is
// the free, open-source voice chat gamers use; public servers welcome anyone
// without an account. By default JukaHub uses GameMania's server, which is
// "free for everyone" and lets every visitor open a temporary channel under
// "Private User-Channels" — the server deletes it when the last person
// leaves. The community meets in "JukaHub" there; a private Voice Channel
// started from the People list is a channel with a random name next to it.
// Phones and PCs can join the same rooms with any Mumble app (Mumla on
// Android, Mumble on iOS and desktops). The server and the room are settings,
// so a self-hosted Mumble server works the same way.
//
// One TLS connection carries everything: control messages and the voice
// itself (Opus inside UDPTunnel messages), so no UDP port has to get through
// the router. Capture, playback, mixing, mute and reconnecting stay in
// chatvoicechannel.go; this file only turns 20 ms PCM frames into Mumble
// packets and back. A simple voice gate sends sound only while someone
// speaks, the way Mumble's own apps do, so the room hears no hiss and the
// server forwards no silence.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	chatVoiceServerKey     = "voiceServer"
	chatVoiceRoomKey       = "voiceRoom"
	chatVoiceDefaultServer = "mumble.gamemania.org:42069"
	chatVoiceDefaultRoom   = "Private User-Channels/JukaHub"
	chatVoiceMumblePort    = "64738"
	// chatVoicePrivatePrefix starts the name of a private Voice Channel.
	chatVoicePrivatePrefix = "JukaHub-"
	// chatVoiceGateHold is how long the gate stays open after the last loud
	// frame (25 × 20 ms), so pauses between words are not cut.
	chatVoiceGateHold = 25
	// chatVoiceSendQueueMumble holds 320 ms of microphone frames: the UI
	// thread fills it once a frame, which an idle handheld slows to ~15 fps.
	chatVoiceSendQueueMumble = 16
)

// chatVoiceErr is a disconnect with a sentence for the Voice page. stop ends
// the call's reconnect loop: retrying cannot help (the server refuses).
type chatVoiceErr struct {
	msg  string
	stop bool
}

func (e chatVoiceErr) Error() string { return e.msg }

// ── Settings and invitations ─────────────────────────────────────────────

// chatVoiceServerAddr checks a "host:port" (or a mumble:// address) and
// returns it as host:port. A missing port is Mumble's 64738.
func chatVoiceServerAddr(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if len(s) >= 9 && strings.EqualFold(s[:9], "mumble://") {
		s = s[9:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i] // a pasted room address: keep the server part
	}
	bad := errors.New("enter a voice server like mumble.example.com:64738")
	if s == "" || strings.ContainsAny(s, " @\\") {
		return "", bad
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		host, port = s, chatVoiceMumblePort
		if strings.Count(s, ":") > 1 && !strings.HasPrefix(s, "[") {
			return "", bad
		}
	}
	host = strings.Trim(host, "[]")
	n, err := strconv.Atoi(port)
	if host == "" || err != nil || n < 1 || n > 65535 {
		return "", bad
	}
	return net.JoinHostPort(strings.ToLower(host), strconv.Itoa(n)), nil
}

// chatVoiceRoomNameOK accepts what Mumble's default channel-name rule does:
// letters, digits, spaces and a few symbols.
func chatVoiceRoomNameOK(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > 64 || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" -_=#[]{}()@|", r) {
			return false
		}
	}
	return true
}

// chatVoiceRoomPath checks a room like "Private User-Channels/JukaHub": the
// channels to walk from the server's root, the last one being the room.
func chatVoiceRoomPath(raw string) (string, error) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(raw), "/"), "/")
	if len(parts) > 4 {
		return "", errors.New("a voice room is at most four channels deep")
	}
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
		if !chatVoiceRoomNameOK(parts[i]) {
			return "", errors.New("use letters, digits and spaces for the voice room, like Lobby/JukaHub")
		}
	}
	return strings.Join(parts, "/"), nil
}

func chatVoiceCustomString(config *Config, key string) string {
	if config == nil || config.Variables.Custom == nil {
		return ""
	}
	s, _ := config.Variables.Custom[key].(string)
	return s
}

// chatVoiceServerConfigured is the Mumble server Voice uses:
// JUKAHUB_VOICE_SERVER, then the setting, then the free public default.
func chatVoiceServerConfigured(config *Config) string {
	for _, raw := range []string{os.Getenv("JUKAHUB_VOICE_SERVER"), chatVoiceCustomString(config, chatVoiceServerKey)} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if addr, err := chatVoiceServerAddr(raw); err == nil {
			return addr
		}
	}
	return chatVoiceDefaultServer
}

// chatVoiceRoomConfigured is the community room's channel path.
func chatVoiceRoomConfigured(config *Config) string {
	if raw := chatVoiceCustomString(config, chatVoiceRoomKey); strings.TrimSpace(raw) != "" {
		if room, err := chatVoiceRoomPath(raw); err == nil {
			return room
		}
	}
	return chatVoiceDefaultRoom
}

// chatVoiceSetCustom saves (or, for an empty value, clears) a Voice setting.
func chatVoiceSetCustom(config *Config, key, value string) error {
	if config == nil {
		return errors.New("settings are not available")
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	if value == "" {
		delete(config.Variables.Custom, key)
	} else {
		config.Variables.Custom[key] = value
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
	return nil
}

func chatVoiceSetServer(config *Config, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return chatVoiceSetCustom(config, chatVoiceServerKey, "")
	}
	addr, err := chatVoiceServerAddr(raw)
	if err != nil {
		return err
	}
	if addr == chatVoiceDefaultServer {
		addr = ""
	}
	return chatVoiceSetCustom(config, chatVoiceServerKey, addr)
}

func chatVoiceSetRoom(config *Config, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return chatVoiceSetCustom(config, chatVoiceRoomKey, "")
	}
	room, err := chatVoiceRoomPath(raw)
	if err != nil {
		return err
	}
	if room == chatVoiceDefaultRoom {
		room = ""
	}
	return chatVoiceSetCustom(config, chatVoiceRoomKey, room)
}

// chatVoiceMumbleURL is a room's address in the mumble:// form Mumble apps
// open, so the same text joins from JukaHub, a phone or a PC.
func chatVoiceMumbleURL(server, room string) string {
	u := url.URL{Scheme: "mumble", Host: server, Path: "/" + room, RawQuery: "version=1.4.0"}
	return u.String()
}

// chatVoiceMumbleInvite reads a mumble:// room address. It carries no user
// name or password; the server and the room path must both be valid.
func chatVoiceMumbleInvite(raw string) (server, room string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || !strings.EqualFold(u.Scheme, "mumble") || u.User != nil || u.Fragment != "" {
		return "", "", false
	}
	if server, err = chatVoiceServerAddr(u.Host); err != nil {
		return "", "", false
	}
	if room, err = chatVoiceRoomPath(u.Path); err != nil {
		return "", "", false
	}
	return server, room, true
}

// chatVoiceCommunityInvite is the address of the community's shared room.
func chatVoiceCommunityInvite(config *Config) string {
	return chatVoiceMumbleURL(chatVoiceServerConfigured(config), chatVoiceRoomConfigured(config))
}

// chatVoiceNewPrivateInvite makes a private room next to the community room:
// same parent channel, an unguessable name.
func chatVoiceNewPrivateInvite(config *Config) (string, error) {
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	room := chatVoiceRoomConfigured(config)
	name := chatVoicePrivatePrefix + hex.EncodeToString(id)
	if i := strings.LastIndex(room, "/"); i >= 0 {
		name = room[:i+1] + name
	}
	return chatVoiceMumbleURL(chatVoiceServerConfigured(config), name), nil
}

// chatVoiceJoinCommunity is the Voice tab's one press: be in the community
// room (leaving another call first, which chatVoiceJoinInternal says).
func chatVoiceJoinCommunity(config *Config) {
	invite := chatVoiceCommunityInvite(config)
	snap := chatVoiceSnapshotNow()
	if snap.Invite == invite && !snap.Ended {
		return
	}
	chatVoiceJoin(invite)
}

// chatMumbleUsername makes a display name acceptable to Mumble's default
// user-name rule (no spaces). try > 0 adds a number after a "name in use";
// plain gives a JukaHub-xxxx name for a server whose rule refused the name.
func chatMumbleUsername(display string, try int, plain bool) string {
	var b strings.Builder
	if !plain {
		for _, r := range strings.TrimSpace(display) {
			switch {
			case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-=[]{}()@|._", r):
				b.WriteRune(r)
			case b.Len() > 0 && !strings.HasSuffix(b.String(), "_"):
				b.WriteByte('_')
			}
			if utf8.RuneCountInString(b.String()) >= 28 {
				break
			}
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		id := make([]byte, 2)
		_, _ = rand.Read(id)
		name = "JukaHub-" + hex.EncodeToString(id)
	}
	if try > 0 {
		name += "-" + strconv.Itoa(10+(try*37)%90)
	}
	return name
}

// ── This device's Mumble identity ────────────────────────────────────────

// Mumble servers know a guest by a client certificate. Murmur refuses to
// open a channel for a user without one (it records the creator's
// certificate in the new channel's rules), and every Mumble app makes its own.
// JukaHub makes one self-signed certificate per device, once, and keeps it in
// the data folder.
var chatMumbleIdentityCache struct {
	sync.Mutex
	cert *tls.Certificate
}

func chatMumbleIdentityPath() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "voice", "mumble-identity.pem")
	}
	return ""
}

// chatMumbleIdentity returns the device's certificate, making it on first
// use (an RSA key takes a moment on a handheld; this runs on the connection
// goroutine). nil means none could be made: joining rooms that already exist
// still works without one.
func chatMumbleIdentity() *tls.Certificate {
	chatMumbleIdentityCache.Lock()
	defer chatMumbleIdentityCache.Unlock()
	if chatMumbleIdentityCache.cert != nil {
		return chatMumbleIdentityCache.cert
	}
	path := chatMumbleIdentityPath()
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if cert, err := tls.X509KeyPair(data, data); err == nil {
				chatMumbleIdentityCache.cert = &cert
				return &cert
			}
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		logWarn("chat", "voice identity key: %v", err)
		return nil
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil
	}
	// Fixed dates: a handheld can start before its clock is set, and a
	// certificate "not yet valid" by a wrong clock would be useless.
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "JukaHub"},
		NotBefore:    time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		logWarn("chat", "voice identity certificate: %v", err)
		return nil
	}
	cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	if path != "" {
		data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})...)
		if err := AtomicWrite(path, data, 0o600); err != nil {
			logWarn("chat", "voice identity not saved: %v", err)
		}
	}
	chatMumbleIdentityCache.cert = cert
	return cert
}

// ── The connection ───────────────────────────────────────────────────────

type chatMumbleUser struct {
	name       string
	channel    uint32
	selfMute   bool
	serverMute bool
}

type chatMumbleChannel struct {
	parent    uint32
	hasParent bool
	name      string
}

// chatMumbleSpeaker is one remote talker's decoder and mixer slot.
type chatMumbleSpeaker struct {
	dec     *chatOpusDecoder
	slot    uint8
	pending []byte
	heard   time.Time
	warned  bool
}

type chatMumble struct {
	call *chatVoiceCall
	conn net.Conn
	wmu  sync.Mutex

	parentPath []string
	roomName   string

	// Read and written by the reader goroutine only.
	synced      bool
	joinStart   time.Time
	codecWarned bool
	channels    map[uint32]*chatMumbleChannel
	users       map[uint32]*chatMumbleUser
	room        uint32
	hasRoom     bool
	createSent  bool
	moveSent    bool
	speakers    map[uint32]*chatMumbleSpeaker
	slots       [chatVoiceMaxMembers + 1]uint32
	// display is each person's place in the people list (1–255), kept for as
	// long as they stay, so a talking dot never jumps to someone else.
	display map[uint32]uint8

	// Shared with the writer goroutine.
	session    atomic.Uint32
	joined     atomic.Bool
	ownSlot    atomic.Uint32
	remoteLast atomic.Int64 // unix ns of the last remote voice packet
}

func chatMumbleTLS(host string, identity *tls.Certificate) *tls.Config {
	roots := bugrepRootCAs()
	return &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		// Offered whatever authorities the server names: Mumble servers accept
		// any client certificate.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			if identity == nil {
				return &tls.Certificate{}, nil
			}
			return identity, nil
		},
		// Most Mumble servers present the certificate the server software made
		// for itself, which no authority signed, and Mumble's own apps accept it.
		// Voice sends no password or account here and the rooms are open on the
		// server anyway, so an unsigned certificate is accepted and noted in the
		// log; one that a known authority signed is still checked below.
		InsecureSkipVerify: true, // #nosec G402 -- see the comment above
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the voice server sent no certificate")
			}
			opts := x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			if _, err := cs.PeerCertificates[0].Verify(opts); err != nil {
				logInfo("chat", "voice server %s: certificate not verified (%v)", host, err)
			}
			return nil
		},
	}
}

// connectMumble runs one connection to a Mumble room until it drops.
func (c *chatVoiceCall) connectMumble(server, room, name string) error {
	host, _, err := net.SplitHostPort(server)
	if err != nil {
		return err
	}
	c.setStatus("Connecting to the voice server…")
	identity := chatMumbleIdentity()
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	raw, err := dialer.DialContext(c.ctx, "tcp", server)
	if err != nil {
		return err
	}
	conn := tls.Client(raw, chatMumbleTLS(host, identity))
	defer conn.Close()
	closeOnCancel := make(chan struct{})
	go func() {
		select {
		case <-c.ctx.Done():
			conn.Close()
		case <-closeOnCancel:
		}
	}()
	defer close(closeOnCancel)
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := conn.HandshakeContext(c.ctx); err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Time{})

	parts := strings.Split(room, "/")
	m := &chatMumble{call: c, conn: conn, parentPath: parts[:len(parts)-1], roomName: parts[len(parts)-1],
		channels: map[uint32]*chatMumbleChannel{}, users: map[uint32]*chatMumbleUser{},
		speakers: map[uint32]*chatMumbleSpeaker{}, display: map[uint32]uint8{}}
	if err := m.send(mumbleMsgVersion, mumbleVersionBody(runtime.GOOS)); err != nil {
		return err
	}
	username := chatMumbleUsername(name, c.nameTry, c.namePlain)
	if err := m.send(mumbleMsgAuthenticate, mumbleAuthBody(username)); err != nil {
		return err
	}
	chatVoiceDiscardSend(c.send)
	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		m.writer(stop)
	}()
	err = m.reader()
	close(stop)
	conn.Close()
	<-writerDone
	return err
}

// send writes one control message; reader and writer both send.
func (m *chatMumble) send(kind uint16, body []byte) error {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	_ = m.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := m.conn.Write(mumbleFrame(kind, body))
	if err != nil {
		m.conn.Close()
	}
	return err
}

func (m *chatMumble) reader() error {
	var buf []byte
	for {
		if m.synced && !m.joined.Load() && time.Since(m.joinStart) > 20*time.Second {
			// Neither moved in nor refused (a dropped request, say): start over.
			return chatVoiceErr{msg: "The voice room did not open — retrying"}
		}
		_ = m.conn.SetReadDeadline(time.Now().Add(40 * time.Second))
		kind, body, err := mumbleReadMsg(m.conn, &buf)
		if err != nil {
			return err
		}
		switch kind {
		case mumbleMsgUDPTunnel:
			m.voiceIn(body)
		case mumbleMsgReject:
			return m.rejected(body)
		case mumbleMsgServerSync:
			session, ok := mumbleParseUint(body, 1)
			if !ok {
				return errMumbleProto
			}
			m.session.Store(uint32(session))
			m.synced = true
			m.joinStart = time.Now()
			m.call.setStatus("Joining the voice room…")
			m.publish()
			if err := m.joinRoom(); err != nil {
				return err
			}
		case mumbleMsgCodecVersion:
			if opus, ok := mumbleParseUint(body, 4); ok && opus == 0 && !m.codecWarned {
				m.codecWarned = true
				logWarn("chat", "voice server is not using Opus (an old client is connected); some people may not be heard")
			}
		case mumbleMsgChannelState:
			msg, err := mumbleParseChannel(body)
			if err != nil {
				continue
			}
			ch := m.channels[msg.id]
			if ch == nil {
				ch = &chatMumbleChannel{}
				m.channels[msg.id] = ch
			}
			if msg.hasParent {
				ch.parent, ch.hasParent = msg.parent, true
			}
			if msg.hasName {
				ch.name = msg.name
			}
			if err := m.joinRoom(); err != nil {
				return err
			}
		case mumbleMsgChannelRemove:
			if id, ok := mumbleParseUint(body, 1); ok {
				delete(m.channels, uint32(id))
				if m.hasRoom && uint32(id) == m.room {
					m.hasRoom, m.createSent, m.moveSent = false, false, false
					m.publish()
					if err := m.joinRoom(); err != nil {
						return err
					}
				}
			}
		case mumbleMsgUserState:
			if err := m.userState(body); err != nil {
				return err
			}
		case mumbleMsgUserRemove:
			session, ok := mumbleParseUint(body, 1)
			if !ok {
				continue
			}
			if uint32(session) == m.session.Load() && m.synced {
				reason := chatVoiceShort(mumbleParseString(body, 3))
				if reason == "" {
					reason = "The voice server removed you from the room"
				}
				return chatVoiceErr{msg: reason, stop: true}
			}
			delete(m.users, uint32(session))
			m.dropSpeaker(uint32(session))
			m.publish()
		case mumbleMsgPermissionDenied:
			if err := m.denied(body); err != nil {
				return err
			}
		}
	}
}

// chatVoiceShort keeps a server's sentence to one line of the Voice page.
func chatVoiceShort(s string) string {
	s = strings.Join(strings.Fields(htmlToPlainVoice(s)), " ")
	if utf8.RuneCountInString(s) > 90 {
		s = string([]rune(s)[:89]) + "…"
	}
	return s
}

// htmlToPlainVoice drops the HTML tags Mumble servers put in their messages.
func htmlToPlainVoice(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
			b.WriteByte(' ')
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (m *chatMumble) rejected(body []byte) error {
	kind, _ := mumbleParseUint(body, 1)
	reason := chatVoiceShort(mumbleParseString(body, 2))
	switch kind {
	case mumbleRejectNameInUse, mumbleRejectWrongUserPW:
		// Someone has this name (our last connection may still hold it, or a
		// registered user owns it): the next try adds a number.
		m.call.nameTry++
		if m.call.nameTry > 6 {
			return chatVoiceErr{msg: "No free name on the voice server — rename yourself in Chat", stop: true}
		}
		return chatVoiceErr{msg: "Name taken on the voice server — retrying"}
	case mumbleRejectInvalidName:
		if !m.call.namePlain {
			// The server's name rule is stricter than Mumble's default.
			m.call.namePlain = true
			return chatVoiceErr{msg: "Name not allowed on the voice server — retrying"}
		}
		return chatVoiceErr{msg: "The voice server does not accept JukaHub's names", stop: true}
	case mumbleRejectServerFull:
		return chatVoiceErr{msg: "The voice server is full — retrying"}
	case mumbleRejectServerPW:
		return chatVoiceErr{msg: "This voice server needs a password", stop: true}
	}
	if reason == "" {
		reason = "The voice server refused the connection"
	}
	return chatVoiceErr{msg: reason, stop: true}
}

// denied handles PermissionDenied (Mumble.proto DenyType numbers).
func (m *chatMumble) denied(body []byte) error {
	kind, _ := mumbleParseUint(body, 5)
	logWarn("chat", "voice server refused: type %d %q", kind, mumbleParseString(body, 4))
	if m.joined.Load() {
		return nil // something unrelated to being in the room
	}
	switch kind {
	case 9: // ChannelFull
		return chatVoiceErr{msg: "The voice room is full", stop: true}
	case 7: // MissingCertificate
		return chatVoiceErr{msg: "This voice server only lets registered users open rooms", stop: true}
	case 3, 6, 10, 11: // ChannelName, TemporaryChannel, NestingLimit, ChannelCountLimit
		if m.createSent && !m.hasRoom {
			return chatVoiceErr{msg: "This voice server would not open the room. Try another room in Voice settings.", stop: true}
		}
	case 1: // Permission
		if m.hasRoom {
			return chatVoiceErr{msg: "The room is locked on this voice server", stop: true}
		}
		return chatVoiceErr{msg: "This voice server does not let guests open rooms here", stop: true}
	}
	return nil
}

// findChild returns the channel named name under parent.
func (m *chatMumble) findChild(parent uint32, name string) (uint32, bool) {
	best, found := uint32(0), false
	for id, ch := range m.channels {
		if ch.hasParent && ch.parent == parent && id != parent && strings.EqualFold(ch.name, name) {
			if !found || id < best {
				best, found = id, true
			}
		}
	}
	return best, found
}

// findAnywhere returns a channel named name at any depth (the lowest id), for
// a room path whose first channel is not directly under the root.
func (m *chatMumble) findAnywhere(name string) (uint32, bool) {
	best, found := uint32(0), false
	for id, ch := range m.channels {
		if ch.hasParent && strings.EqualFold(ch.name, name) && (!found || id < best) {
			best, found = id, true
		}
	}
	return best, found
}

// joinRoom walks to the room's parent channel and moves in, or asks the
// server to open the room when no one is there yet.
func (m *chatMumble) joinRoom() error {
	if !m.synced || m.joined.Load() {
		return nil
	}
	parent := uint32(0) // the root channel
	for i, name := range m.parentPath {
		id, ok := m.findChild(parent, name)
		if !ok && i == 0 {
			id, ok = m.findAnywhere(name)
		}
		if !ok {
			return chatVoiceErr{msg: chatVoiceShort("This voice server has no “" + name + "” channel. Change the room in Voice settings."), stop: true}
		}
		parent = id
	}
	if id, ok := m.findChild(parent, m.roomName); ok {
		if !m.hasRoom || m.room != id {
			m.room, m.hasRoom = id, true
			m.publish()
		}
		if u := m.users[m.session.Load()]; u != nil && u.channel == id {
			// Already there (the server's default channel, say).
			if !m.joined.Swap(true) {
				m.call.markConnected()
			}
			return nil
		}
		if !m.moveSent {
			m.moveSent = true
			return m.send(mumbleMsgUserState, mumbleMoveBody(m.session.Load(), id))
		}
		return nil
	}
	if m.createSent {
		return nil
	}
	// Murmur moves the creator of a temporary channel in by itself.
	m.createSent = true
	return m.send(mumbleMsgChannelState, mumbleCreateRoomBody(parent, m.roomName))
}

func (m *chatMumble) userState(body []byte) error {
	msg, err := mumbleParseUser(body)
	if err != nil {
		return nil
	}
	u := m.users[msg.session]
	if u == nil {
		u = &chatMumbleUser{}
		m.users[msg.session] = u
	}
	if msg.hasName {
		u.name = msg.name
	}
	if msg.hasChannel {
		u.channel = msg.channel
	}
	if msg.hasSelfMute {
		u.selfMute = msg.selfMute
	}
	if msg.hasMute {
		u.serverMute = msg.serverMute
	}
	own := m.synced && msg.session == m.session.Load()
	if !own && msg.hasChannel && (!m.hasRoom || msg.channel != m.room) {
		m.dropSpeaker(msg.session)
	}
	if own && msg.hasChannel {
		inRoom := m.hasRoom && msg.channel == m.room
		was := m.joined.Swap(inRoom)
		switch {
		case inRoom && !was:
			m.call.markConnected()
		case !inRoom && was:
			// Moved out of the room (by an admin, say): go back in.
			m.call.setConnected(false, "Rejoining the voice room…")
			m.moveSent = false
			m.joinStart = time.Now()
			if err := m.joinRoom(); err != nil {
				return err
			}
		}
	}
	m.publish()
	return nil
}

// publish copies the people in the room to the call, in a stable order.
func (m *chatMumble) publish() {
	var members []uint32
	if m.hasRoom {
		for s, u := range m.users {
			if u.channel == m.room && u.name != "" {
				members = append(members, s)
			}
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
	present := make(map[uint32]bool, len(members))
	for _, s := range members {
		present[s] = true
	}
	var used [256]bool
	for s, slot := range m.display {
		if !present[s] {
			delete(m.display, s)
			continue
		}
		used[slot] = true
	}
	for _, s := range members {
		if _, ok := m.display[s]; ok {
			continue
		}
		for slot := 1; slot < len(used); slot++ {
			if !used[slot] {
				used[slot] = true
				m.display[s] = uint8(slot)
				break
			}
		}
	}
	people := make([]chatVoicePerson, 0, len(members))
	own := uint8(0)
	for _, s := range members {
		u := m.users[s]
		slot := m.display[s] // 0 past 255 people: listed, never "talking"
		people = append(people, chatVoicePerson{Slot: slot, Name: u.name, Muted: u.selfMute || u.serverMute})
		if s == m.session.Load() {
			own = slot
		}
	}
	m.ownSlot.Store(uint32(own))
	m.call.setPeople(people, own)
}

// speaker returns the talker's decoder and mixer slot, opening them on the
// first packet. When all slots are busy the quietest one is reused; nil
// means everyone is talking at once and this voice waits.
func (m *chatMumble) speaker(session uint32) *chatMumbleSpeaker {
	if sp := m.speakers[session]; sp != nil {
		return sp
	}
	slot := uint8(0)
	for s := uint8(1); s <= chatVoiceMaxMembers; s++ {
		if m.slots[s] == 0 {
			slot = s
			break
		}
	}
	if slot == 0 {
		oldest := time.Now().Add(-2 * time.Second)
		for s := uint8(1); s <= chatVoiceMaxMembers; s++ {
			if sp := m.speakers[m.slots[s]]; sp != nil && sp.heard.Before(oldest) {
				oldest, slot = sp.heard, s
			}
		}
		if slot == 0 {
			return nil
		}
		m.dropSpeaker(m.slots[slot])
	}
	dec, err := newChatOpusDecoder()
	if err != nil {
		logWarn("chat", "voice decoder: %v", err)
		return nil
	}
	sp := &chatMumbleSpeaker{dec: dec, slot: slot}
	m.speakers[session] = sp
	m.slots[slot] = session
	return sp
}

func (m *chatMumble) dropSpeaker(session uint32) {
	sp := m.speakers[session]
	if sp == nil {
		return
	}
	delete(m.speakers, session)
	if m.slots[sp.slot] == session {
		m.slots[sp.slot] = 0
	}
}

// voiceIn decodes one forwarded packet into 20 ms frames for the mixer.
func (m *chatMumble) voiceIn(p []byte) {
	v, ok := mumbleParseVoice(p)
	if !ok || !m.joined.Load() || v.session == m.session.Load() {
		return
	}
	sp := m.speaker(v.session)
	if sp == nil {
		return
	}
	now := time.Now()
	sp.heard = now
	m.remoteLast.Store(now.UnixNano())
	if slot, ok := m.display[v.session]; ok {
		m.call.markHeard(slot)
	}
	if len(v.opus) > 0 {
		pcm, err := sp.dec.decode(v.opus, sp.pending)
		if err != nil {
			if !sp.warned {
				sp.warned = true
				logWarn("chat", "voice decode (session %d): %v", v.session, err)
			}
			return
		}
		sp.pending = pcm
	}
	if v.last && len(sp.pending) > 0 && len(sp.pending) < chatVoiceFrameBytes {
		// The end of a spurt: pad the tail to a whole frame rather than lose it.
		sp.pending = append(sp.pending, make([]byte, chatVoiceFrameBytes-len(sp.pending))...)
	}
	off := 0
	for len(sp.pending)-off >= chatVoiceFrameBytes {
		frame := append([]byte(nil), sp.pending[off:off+chatVoiceFrameBytes]...)
		off += chatVoiceFrameBytes
		select {
		case m.call.receive <- chatVoicePacket{slot: sp.slot, pcm: frame}:
		default:
		}
	}
	sp.pending = append(sp.pending[:0], sp.pending[off:]...)
}

// writer sends the microphone's frames through the voice gate and keeps the
// connection alive with pings. Frames are sent as soon as they arrive (up
// to a queue's worth per tick): the server forwards them and every listener
// buffers, so a frame held back here would only add delay.
func (m *chatMumble) writer(stop <-chan struct{}) {
	ping := time.NewTicker(10 * time.Second)
	pace := time.NewTicker(20 * time.Millisecond)
	defer ping.Stop()
	defer pace.Stop()
	var enc *chatOpusEncoder
	var gate chatVoiceGate
	var seq uint64
	var pkt []byte
	sentMute := false
	lastFrame := time.Now()
	for {
		select {
		case <-stop:
			return
		case <-ping.C:
			if m.send(mumbleMsgPing, mumblePingBody(uint64(time.Now().UnixMicro()))) != nil {
				return
			}
		case <-pace.C:
			if !m.joined.Load() {
				chatVoiceDiscardSend(m.call.send)
				gate.reset()
				continue
			}
			_, muted := m.call.snapshotConnected()
			if muted != sentMute {
				sentMute = muted
				if m.send(mumbleMsgUserState, mumbleSelfMuteBody(m.session.Load(), muted)) != nil {
					return
				}
			}
			if muted || time.Since(lastFrame) > 300*time.Millisecond {
				// No sound is coming (muted, or the microphone paused for another
				// voice feature): the spurt is over, start the next one fresh.
				gate.reset()
			}
			for i := 0; i < chatVoiceSendQueueMumble; i++ {
				var frame []byte
				select {
				case frame = <-m.call.send:
				default:
				}
				if frame == nil {
					break
				}
				lastFrame = time.Now()
				seq += 2 // Mumble counts 10 ms frames
				echo := time.Since(time.Unix(0, m.remoteLast.Load())) < 600*time.Millisecond
				frames, start, last := gate.step(frame, echo)
				if len(frames) == 0 {
					continue
				}
				if start || enc == nil {
					// Mumble's apps start every talk spurt with a fresh encoder.
					next, err := newChatOpusEncoder()
					if err != nil {
						logWarn("chat", "voice encoder: %v", err)
						continue
					}
					enc = next
				}
				first := seq - uint64(2*(len(frames)-1))
				for j, f := range frames {
					opus, err := enc.encode(f)
					if err != nil || opus == nil {
						continue
					}
					pkt = mumbleVoicePacket(pkt, first+uint64(2*j), opus, last && j == len(frames)-1)
					if m.send(mumbleMsgUDPTunnel, pkt) != nil {
						return
					}
				}
				m.call.markHeard(uint8(m.ownSlot.Load()))
			}
		}
	}
}

// chatVoiceGate decides which microphone frames are speech. It follows the
// room's noise floor slowly and opens well above it; while others are heard
// it needs a louder voice, so a speaker's sound coming back into the
// microphone does not echo to the room.
type chatVoiceGate struct {
	floor float64
	hold  int
	open  bool
	pre   [][]byte
}

// reset ends a spurt without sending anything (listeners time it out) and
// forgets the frames kept from before it; the noise floor stays.
func (g *chatVoiceGate) reset() {
	g.open, g.hold, g.pre = false, 0, nil
}

// step takes one 20 ms frame and returns the frames to send now (with up to
// two frames from just before the voice started), whether they start a talk
// spurt and whether they end it.
func (g *chatVoiceGate) step(frame []byte, echo bool) (frames [][]byte, start, last bool) {
	level := chatVoiceRMS(frame)
	if g.floor == 0 || level < g.floor {
		g.floor = math.Max(level, 0.0005)
	} else {
		g.floor += (level - g.floor) * 0.0015
	}
	threshold := math.Max(g.floor*3.5, 0.008)
	if echo {
		threshold *= 2.5
	}
	if level > threshold {
		g.hold = chatVoiceGateHold
		if !g.open {
			g.open = true
			out := append(g.pre, frame)
			g.pre = nil
			return out, true, false
		}
		return [][]byte{frame}, false, false
	}
	if g.open {
		g.hold--
		if g.hold <= 0 {
			g.open = false
			return [][]byte{frame}, false, true
		}
		return [][]byte{frame}, false, false
	}
	g.pre = append(g.pre, frame)
	if len(g.pre) > 2 {
		g.pre = g.pre[len(g.pre)-2:]
	}
	return nil, false, false
}

// chatVoiceRMS is a frame's loudness, 0..1.
func chatVoiceRMS(frame []byte) float64 {
	n := len(frame) / 2
	if n == 0 {
		return 0
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		s := float64(int16(binary.LittleEndian.Uint16(frame[i*2:]))) / 32768
		sum += s * s
	}
	return math.Sqrt(sum / float64(n))
}
