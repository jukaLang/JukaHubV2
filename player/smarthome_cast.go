package main

// smarthome_cast.go — Google's own smart-home devices on the home network:
// Nest Mini, Nest Audio and Nest Hub, Chromecast, Google TV and speaker
// groups. They are Google Cast receivers, so they announce themselves over
// mDNS (_googlecast._tcp) and take Cast's local control channel — TLS on
// port 8009, length-prefixed protobuf CastMessages that carry JSON — with no
// Google account: what is playing, the volume, mute, pause and play, stop.
//
// Everything else in a Google Home (lights and plugs of other makers, Nest
// thermostats) lives in Google's cloud, which only Google's own apps may
// drive; those reach JukaHub through their own maker (TP-Link) or through
// Home Assistant (docs/SMART_HOME.md).
//
// The protobuf is three string fields and two enums, so it is written and read
// by hand here (gcastEncode / gcastDecode) rather than pulling in a protobuf
// runtime for it.

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/tidwall/gjson"
)

const (
	gcastService    = "_googlecast._tcp"
	gcastPort       = 8009
	gcastNSConn     = "urn:x-cast:com.google.cast.tp.connection"
	gcastNSHeart    = "urn:x-cast:com.google.cast.tp.heartbeat"
	gcastNSReceiver = "urn:x-cast:com.google.cast.receiver"
	gcastNSMedia    = "urn:x-cast:com.google.cast.media"
	gcastSender     = "sender-jukahub"
	gcastReceiver   = "receiver-0"
	// gcastMsgLimit bounds one message; gcastStep one request's wait.
	gcastMsgLimit = 64 << 10
	gcastStep     = 4 * time.Second
)

var (
	errGCastNothing = errors.New("nothing is playing")
	errGCastNoMedia = errors.New("the app that is playing cannot be paused from here")
)

// gcastFound is one receiver as mDNS described it, and as smarthome.json
// keeps it.
type gcastFound struct {
	ID    string `json:"id"`
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
	Kind  string `json:"kind"`
}

// gcastTXT reads mDNS TXT records ("k=v") into a map.
func gcastTXT(records []string) map[string]string {
	out := make(map[string]string, len(records))
	for _, r := range records {
		k, v, _ := strings.Cut(r, "=")
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

// gcastKind is what a receiver is, from its model and its capability bits
// (ca: 1 video out, 4 audio out, 32 a speaker group).
func gcastKind(model, caps string) string {
	ca, _ := strconv.Atoi(caps)
	switch {
	case strings.Contains(strings.ToLower(model), "group") || ca&32 != 0:
		return shubKindGroup
	case ca&1 != 0:
		return shubKindScreen
	}
	return shubKindSpeaker
}

// gcastDiscover browses mDNS for Cast receivers for wait (or until ctx ends).
// It blocks: call it from a goroutine.
func gcastDiscover(ctx context.Context, wait time.Duration) (found []gcastFound) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("smarthome", "Google Cast browse failed: %v", r)
		}
	}()
	resolver, err := zeroconf.NewResolver(zeroconf.SelectIPTraffic(zeroconf.IPv4))
	if err != nil {
		logInfo("smarthome", "Google Cast browse not possible here: %v", err)
		return nil
	}
	bctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry, 16)
	if err := resolver.Browse(bctx, gcastService, "local.", entries); err != nil {
		logInfo("smarthome", "Google Cast browse failed: %v", err)
		return nil
	}
	byID := map[string]gcastFound{}
	// The resolver closes entries when bctx ends.
	for e := range entries {
		if e == nil {
			continue
		}
		txt := gcastTXT(e.Text)
		id := strings.ToLower(strings.ReplaceAll(txt["id"], "-", ""))
		if id == "" {
			id = strings.ToLower(strings.TrimSpace(e.Instance))
		}
		if id == "" || len(id) > 80 {
			continue
		}
		host := ""
		for _, ip := range e.AddrIPv4 {
			if ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() && webdavLANIP(ip) {
				host = ip.String()
				break
			}
		}
		if host == "" {
			continue
		}
		port := e.Port
		if port <= 0 || port > 65535 {
			port = gcastPort
		}
		name := strings.TrimSpace(txt["fn"])
		if name == "" {
			name = mlMDNSName(e.Instance, e.HostName)
		}
		model := strings.TrimSpace(txt["md"])
		byID["gc:"+id] = gcastFound{ID: "gc:" + id, Host: host, Port: port, Name: name, Model: model, Kind: gcastKind(model, txt["ca"])}
	}
	for _, f := range byID {
		found = append(found, f)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return found
}

// ── The control channel ──────────────────────────────────────────────────

// gcastPBString appends a length-delimited field.
func gcastPBString(b []byte, field int, s string) []byte {
	b = append(b, byte(field<<3|2))
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// gcastEncode is one CastMessage: protocol version 0 and payload type STRING
// are written out (they are required fields of a proto2 message), then
// source, destination, namespace and the JSON payload.
func gcastEncode(src, dst, ns, payload string) []byte {
	b := make([]byte, 0, 64+len(dst)+len(ns)+len(payload))
	b = append(b, 0x08, 0x00) // protocol_version = CASTV2_1_0
	b = gcastPBString(b, 2, src)
	b = gcastPBString(b, 3, dst)
	b = gcastPBString(b, 4, ns)
	b = append(b, 0x28, 0x00) // payload_type = STRING
	return gcastPBString(b, 6, payload)
}

// gcastMsg is a decoded CastMessage.
type gcastMsg struct {
	src, dst, ns, payload string
}

// gcastDecode reads one CastMessage (outside data): the string fields it
// needs, every other field skipped by its wire type.
func gcastDecode(msg []byte) (gcastMsg, error) {
	var m gcastMsg
	bad := errors.New("the device sent a message JukaHub cannot read")
	for i := 0; i < len(msg); {
		key, n := binary.Uvarint(msg[i:])
		if n <= 0 {
			return m, bad
		}
		i += n
		switch key & 7 {
		case 0:
			_, n := binary.Uvarint(msg[i:])
			if n <= 0 {
				return m, bad
			}
			i += n
		case 1:
			i += 8
		case 5:
			i += 4
		case 2:
			l, n := binary.Uvarint(msg[i:])
			if n <= 0 || l > uint64(len(msg)-i-n) {
				return m, bad
			}
			i += n
			v := string(msg[i : i+int(l)])
			i += int(l)
			switch key >> 3 {
			case 2:
				m.src = v
			case 3:
				m.dst = v
			case 4:
				m.ns = v
			case 6:
				m.payload = v
			}
		default:
			return m, bad
		}
		if i > len(msg) {
			return m, bad
		}
	}
	return m, nil
}

// gcastConn is one open control channel.
type gcastConn struct {
	c   net.Conn
	rid int
}

// gcastDial opens the channel to a receiver on the home network and joins
// its platform receiver. Cast devices present self-signed certificates, so
// the certificate is not checked; nothing secret goes over this channel.
func gcastDial(ctx context.Context, host string, port int) (*gcastConn, error) {
	ip := net.ParseIP(host)
	if ip == nil || !webdavLANIP(ip) {
		return nil, errors.New("the device is not on the home network")
	}
	if port <= 0 || port > 65535 {
		port = gcastPort
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, errors.New("the device did not answer")
	}
	tc := tls.Client(raw, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // Cast receivers' certificates are self-signed
	_ = tc.SetDeadline(time.Now().Add(gcastStep))
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, errors.New("the device refused the connection")
	}
	g := &gcastConn{c: tc}
	if err := g.send(gcastReceiver, gcastNSConn, map[string]interface{}{"type": "CONNECT"}); err != nil {
		g.close()
		return nil, errors.New("the device refused the connection")
	}
	return g, nil
}

// close says goodbye and closes the channel.
func (g *gcastConn) close() {
	_ = g.send(gcastReceiver, gcastNSConn, map[string]interface{}{"type": "CLOSE"})
	_ = g.c.Close()
}

// send writes one message.
func (g *gcastConn) send(dst, ns string, payload map[string]interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	msg := gcastEncode(gcastSender, dst, ns, string(body))
	frame := make([]byte, 4, 4+len(msg))
	binary.BigEndian.PutUint32(frame, uint32(len(msg)))
	_, err = g.c.Write(append(frame, msg...))
	return err
}

// read reads one message.
func (g *gcastConn) read() (gcastMsg, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(g.c, hdr[:]); err != nil {
		return gcastMsg{}, errors.New("the device did not answer")
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > gcastMsgLimit {
		return gcastMsg{}, errors.New("the device sent a message JukaHub cannot read")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(g.c, buf); err != nil {
		return gcastMsg{}, errors.New("the device's message was cut off")
	}
	return gcastDecode(buf)
}

// request sends payload with a new requestId and waits for the answer that
// carries it, answering the device's pings meanwhile.
func (g *gcastConn) request(dst, ns string, payload map[string]interface{}) (gjson.Result, error) {
	g.rid++
	id := g.rid
	payload["requestId"] = id
	_ = g.c.SetDeadline(time.Now().Add(gcastStep))
	if err := g.send(dst, ns, payload); err != nil {
		return gjson.Result{}, errors.New("the device closed the connection")
	}
	for tries := 0; tries < 40; tries++ {
		m, err := g.read()
		if err != nil {
			return gjson.Result{}, err
		}
		if !gjson.Valid(m.payload) {
			continue
		}
		res := gjson.Parse(m.payload)
		if m.ns == gcastNSHeart && res.Get("type").String() == "PING" {
			_ = g.send(m.src, gcastNSHeart, map[string]interface{}{"type": "PONG"})
			continue
		}
		if res.Get("requestId").Int() != int64(id) {
			continue
		}
		switch t := res.Get("type").String(); t {
		case "INVALID_REQUEST", "LAUNCH_ERROR", "LOAD_FAILED", "INVALID_PLAYER_STATE":
			why := strings.TrimSpace(res.Get("reason").String())
			if why == "" {
				why = strings.ToLower(strings.ReplaceAll(t, "_", " "))
			}
			return res, errors.New("the device refused: " + strings.ToLower(why))
		}
		return res, nil
	}
	return gjson.Result{}, errors.New("the device did not answer")
}

// gcastStatus is what a receiver is doing.
type gcastStatus struct {
	Volume       int
	Muted        bool
	App          string
	Idle         bool
	Session      string
	Transport    string
	Media        bool
	Player       string
	MediaSession int64
	Title        string
}

// gcastParseReceiver reads RECEIVER_STATUS (outside data).
func gcastParseReceiver(res gjson.Result) gcastStatus {
	vol := res.Get("status.volume")
	st := gcastStatus{Volume: int(math.Round(vol.Get("level").Float() * 100)), Muted: vol.Get("muted").Bool()}
	st.Volume = max(0, min(100, st.Volume))
	apps := res.Get("status.applications").Array()
	if len(apps) == 0 {
		return st
	}
	app := apps[0]
	st.App = strings.TrimSpace(app.Get("displayName").String())
	st.Idle = app.Get("isIdleScreen").Bool()
	st.Session = app.Get("sessionId").String()
	st.Transport = app.Get("transportId").String()
	for _, ns := range app.Get("namespaces").Array() {
		if ns.Get("name").String() == gcastNSMedia {
			st.Media = true
		}
	}
	return st
}

// gcastParseMedia reads MEDIA_STATUS (outside data) into st.
func gcastParseMedia(st *gcastStatus, res gjson.Result) {
	list := res.Get("status").Array()
	if len(list) == 0 {
		return
	}
	m := list[0]
	st.Player = strings.ToUpper(m.Get("playerState").String())
	st.MediaSession = m.Get("mediaSessionId").Int()
	if t := strings.TrimSpace(m.Get("media.metadata.title").String()); t != "" {
		st.Title = t
		if a := strings.TrimSpace(m.Get("media.metadata.artist").String()); a != "" {
			st.Title = a + " — " + t
		}
	}
}

// gcastReadStatus reads a receiver's volume and app and, when the app plays
// media, what it plays.
func gcastReadStatus(ctx context.Context, f gcastFound) (gcastStatus, error) {
	g, err := gcastDial(ctx, f.Host, f.Port)
	if err != nil {
		return gcastStatus{}, err
	}
	defer g.close()
	rs, err := g.request(gcastReceiver, gcastNSReceiver, map[string]interface{}{"type": "GET_STATUS"})
	if err != nil {
		return gcastStatus{}, err
	}
	st := gcastParseReceiver(rs)
	if st.Media && !st.Idle && st.Transport != "" {
		if g.send(st.Transport, gcastNSConn, map[string]interface{}{"type": "CONNECT"}) == nil {
			if ms, err := g.request(st.Transport, gcastNSMedia, map[string]interface{}{"type": "GET_STATUS"}); err == nil {
				gcastParseMedia(&st, ms)
			}
		}
	}
	return st, nil
}

// gcastDo runs one command on a receiver: "volume" (level 0–1), "mute",
// "unmute", "stop", "play" or "pause".
func gcastDo(ctx context.Context, f gcastFound, act string, level float64) error {
	g, err := gcastDial(ctx, f.Host, f.Port)
	if err != nil {
		return err
	}
	defer g.close()
	switch act {
	case "volume":
		level = math.Max(0, math.Min(1, level))
		_, err = g.request(gcastReceiver, gcastNSReceiver, map[string]interface{}{
			"type": "SET_VOLUME", "volume": map[string]interface{}{"level": level},
		})
		return err
	case "mute", "unmute":
		_, err = g.request(gcastReceiver, gcastNSReceiver, map[string]interface{}{
			"type": "SET_VOLUME", "volume": map[string]interface{}{"muted": act == "mute"},
		})
		return err
	}
	rs, err := g.request(gcastReceiver, gcastNSReceiver, map[string]interface{}{"type": "GET_STATUS"})
	if err != nil {
		return err
	}
	st := gcastParseReceiver(rs)
	if st.Session == "" || st.Idle {
		return errGCastNothing
	}
	if act == "stop" {
		_, err = g.request(gcastReceiver, gcastNSReceiver, map[string]interface{}{"type": "STOP", "sessionId": st.Session})
		return err
	}
	if !st.Media || st.Transport == "" {
		return errGCastNoMedia
	}
	if err := g.send(st.Transport, gcastNSConn, map[string]interface{}{"type": "CONNECT"}); err != nil {
		return errors.New("the device closed the connection")
	}
	ms, err := g.request(st.Transport, gcastNSMedia, map[string]interface{}{"type": "GET_STATUS"})
	if err != nil {
		return err
	}
	gcastParseMedia(&st, ms)
	if st.MediaSession == 0 {
		return errGCastNothing
	}
	typ := "PAUSE"
	if act == "play" {
		typ = "PLAY"
	}
	_, err = g.request(st.Transport, gcastNSMedia, map[string]interface{}{"type": typ, "mediaSessionId": st.MediaSession})
	return err
}
