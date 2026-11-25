package main

// smarthome_tplink.go — TP-Link Kasa and Tapo plugs, power strips, switches,
// dimmers, bulbs, light strips and hub sensors on the home network, found and
// controlled locally: nothing goes through TP-Link's cloud.
//
// Finding them (tplinkDiscover) is one burst of UDP broadcasts, sent three
// times: the old Kasa query (get_sysinfo, autokey XOR) to port 9999, which
// Kasa devices from before about 2021 answer with their whole state, and
// TP-Link's discovery probe to port 20002, which every Tapo device and newer
// Kasa hardware answers with its model, its MAC and the transport it speaks
// (mgt_encrypt_schm).
//
// Talking to one (tplinkDevice.query) goes through that transport
// (smarthome_tplink_crypto.go has the encodings):
//
//	xor   TCP 9999, no sign-in (old Kasa)
//	klap  HTTP; the handshake proves the TP-Link ID's hash (newer Kasa
//	      speaks the old "IOT" JSON through it, Tapo the "SMART" JSON)
//	aes   HTTP; an RSA handshake, then login_device with the TP-Link ID
//	      (Tapo firmware from before KLAP)
//	tpap  the newest firmware's protocol, which nothing outside TP-Link
//	      speaks: the device is listed with the fix — Third-Party
//	      Compatibility in the Tapo or Kasa app brings KLAP back on most
//
// A device that was never signed in to TP-Link's cloud, or was reset, takes
// TP-Link's well-known setup accounts instead of the user's; those are tried
// after the user's own, as the Kasa and Tapo apps do. A refused sign-in is not
// tried again until the TP-Link ID changes or a few minutes pass, so a wrong
// password never turns the refresh into a stream of failed logins.
//
// Credentials only ever go to an address on the home network (webdavLANIP):
// discovery answers come from the LAN, and smarthome.json's saved addresses
// are checked again before each connection.
//
// What a device is (plug, strip, switch, dimmer, bulb, light strip, hub) comes
// from its own answer; tplinkParseIOT and tplinkParseSmart turn each family's
// answer into tplinkState rows, one per thing that can be shown or switched
// (each socket of a strip and each sensor of a hub a row of its own).

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// tplinkTransport is how a device is spoken to.
type tplinkTransport string

const (
	tplinkXOR  tplinkTransport = "xor"
	tplinkKLAP tplinkTransport = "klap"
	tplinkAES  tplinkTransport = "aes"
	tplinkTPAP tplinkTransport = "tpap"
)

const (
	tplinkLegacyPort    = 9999
	tplinkDiscoveryPort = 20002
	tplinkHTTPPort      = 80
	// tplinkTimeout bounds one request; tplinkAnswerLimit one answer.
	tplinkTimeout     = 5 * time.Second
	tplinkAnswerLimit = 256 << 10
	// tplinkAuthBackoff is how long a refused sign-in is not tried again
	// with the same TP-Link ID.
	tplinkAuthBackoff = 5 * time.Minute
)

var (
	errTPLinkAuth = errors.New("needs your TP-Link ID")
	errTPLinkTPAP = errors.New("turn on Third-Party Compatibility in the Tapo or Kasa app")
	errTPLinkKind = errors.New("this TP-Link device cannot be controlled from here yet")
	// errTPLinkSession: the device ended the session; one new handshake is
	// tried before the error is shown.
	errTPLinkSession = errors.New("the device ended the session")
	errTPLinkNotLAN  = errors.New("the device is not on the home network")
)

// tplinkFound is one device as discovery described it, and as smarthome.json
// keeps it between runs.
type tplinkFound struct {
	ID        string          `json:"id"`
	Host      string          `json:"host"`
	Port      int             `json:"port"`
	Family    string          `json:"family"`
	Transport tplinkTransport `json:"transport"`
	LoginV    int             `json:"lv,omitempty"`
	Model     string          `json:"model,omitempty"`
	Type      string          `json:"type,omitempty"`
	Name      string          `json:"name,omitempty"`
	// sysinfo is an XOR answer's state, which came with it (not saved).
	sysinfo []byte
}

// tplinkCreds is a TP-Link ID: the e-mail address and password of the Kasa or
// Tapo app.
type tplinkCreds struct {
	User string
	Pass string
}

// tplinkSetupAccounts are the accounts a device takes before anyone signed it
// in to TP-Link's cloud (the Kasa and the Tapo setup accounts), base64 the way
// python-kasa keeps them; a blank account comes last.
var tplinkSetupAccounts = [...][2]string{
	{"a2FzYUB0cC1saW5rLm5ldA==", "a2FzYVNldHVw"},
	{"dGVzdEB0cC1saW5rLm5ldA==", "dGVzdA=="},
}

// tplinkAccounts is every account to try, the user's first.
func tplinkAccounts(creds tplinkCreds) []tplinkCreds {
	out := make([]tplinkCreds, 0, len(tplinkSetupAccounts)+2)
	if creds.User != "" {
		out = append(out, creds)
	}
	for _, a := range tplinkSetupAccounts {
		u, _ := base64.StdEncoding.DecodeString(a[0])
		p, _ := base64.StdEncoding.DecodeString(a[1])
		out = append(out, tplinkCreds{User: string(u), Pass: string(p)})
	}
	return append(out, tplinkCreds{})
}

// tplinkMAC normalises a MAC as the devices write it ("AA-BB-…", "aa:bb:…",
// "AABB…") to lower-case hex.
func tplinkMAC(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			b.WriteRune(r)
		}
	}
	if b.Len() != 12 {
		return ""
	}
	return b.String()
}

// tplinkDeviceID is the device's id in the hub: "tp:" and its MAC, or its
// device id when no MAC came with the answer.
func tplinkDeviceID(mac, deviceID string) string {
	if m := tplinkMAC(mac); m != "" {
		return "tp:" + m
	}
	id := strings.ToLower(strings.TrimSpace(deviceID))
	if id == "" || len(id) > 80 {
		return ""
	}
	return "tp:" + id
}

// tplinkModelName is a name for a device that has not said its own yet:
// "Tapo P110" from "P110(EU)".
func tplinkModelName(typ, model string) string {
	model = strings.TrimSpace(model)
	if i := strings.IndexByte(model, '('); i > 0 {
		model = strings.TrimSpace(model[:i])
	}
	if model == "" {
		model = "device"
	}
	switch t := strings.ToUpper(typ); {
	case strings.Contains(t, "TAPO"):
		return "Tapo " + model
	case strings.Contains(t, "KASA"), strings.HasPrefix(t, "IOT."):
		return "Kasa " + model
	}
	return "TP-Link " + model
}

// ── Discovery ────────────────────────────────────────────────────────────

// tplinkLegacyQuery is the old Kasa discovery query.
var tplinkLegacyQuery = []byte(`{"system":{"get_sysinfo":{}}}`)

// tplinkTDPBare is the 20002 probe with no body (older firmware answers it):
// TDP version 2, a probe, CRC-32 of the header with the CRC field set to
// 0x5A6B7C8D.
var tplinkTDPBare = []byte{0x02, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46, 0x3c, 0xb5, 0xd3}

// tplinkTDPQuery is the 20002 probe newer firmware wants: the same header
// with flags 17, a random serial and the length of a JSON body that carries
// JukaHub's public key; nil when no key could be made.
func tplinkTDPQuery() []byte {
	_, pem, err := tplinkRSAKey()
	if err != nil {
		return nil
	}
	body, err := json.Marshal(map[string]interface{}{"params": map[string]string{"rsa_key": pem}})
	if err != nil || len(body) > 0xFFFF {
		return nil
	}
	pkt := make([]byte, 16, 16+len(body))
	pkt[0] = 2                             // TDP version
	binary.BigEndian.PutUint16(pkt[2:], 1) // op code: probe
	binary.BigEndian.PutUint16(pkt[4:], uint16(len(body)))
	pkt[6] = 17 // flags
	_, _ = rand.Read(pkt[8:12])
	binary.BigEndian.PutUint32(pkt[12:], 0x5A6B7C8D)
	pkt = append(pkt, body...)
	binary.BigEndian.PutUint32(pkt[12:], crc32.ChecksumIEEE(pkt))
	return pkt
}

// tplinkDiscover broadcasts the queries three times and gathers the answers
// for wait (or until ctx ends): one tplinkFound per address, an old Kasa
// answer preferred (it needs no sign-in). It blocks: call it from a goroutine.
func tplinkDiscover(ctx context.Context, wait time.Duration) []tplinkFound {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		logInfo("smarthome", "TP-Link discovery not possible here: %v", err)
		return nil
	}
	defer conn.Close()
	legacy := tplinkXOREncrypt(tplinkLegacyQuery)
	keyed := tplinkTDPQuery()
	send := func() {
		for _, a := range psDiscoveryAddresses(tplinkLegacyPort) {
			if addr, err := net.ResolveUDPAddr("udp4", a); err == nil {
				_, _ = conn.WriteToUDP(legacy, addr)
			}
		}
		for _, a := range psDiscoveryAddresses(tplinkDiscoveryPort) {
			addr, err := net.ResolveUDPAddr("udp4", a)
			if err != nil {
				continue
			}
			_, _ = conn.WriteToUDP(tplinkTDPBare, addr)
			if keyed != nil {
				_, _ = conn.WriteToUDP(keyed, addr)
			}
		}
	}
	byHost := map[string]tplinkFound{}
	buf := make([]byte, 64<<10)
	deadline := time.Now().Add(wait)
	next, rounds := time.Now(), 0
	for ctx.Err() == nil {
		now := time.Now()
		if rounds < 3 && !now.Before(next) {
			send()
			rounds++
			next = now.Add(700 * time.Millisecond)
		}
		if !now.Before(deadline) {
			break
		}
		until := deadline
		if rounds < 3 && next.Before(until) {
			until = next
		}
		_ = conn.SetReadDeadline(until)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			break
		}
		if from == nil || !webdavLANIP(from.IP) {
			continue
		}
		var f tplinkFound
		var ok bool
		switch from.Port {
		case tplinkLegacyPort:
			f, ok = tplinkParseLegacyAnswer(tplinkXORDecrypt(buf[:n]))
		case tplinkDiscoveryPort:
			f, ok = tplinkParseTDPAnswer(buf[:n])
		}
		if !ok {
			continue
		}
		f.Host = from.IP.String()
		if old, seen := byHost[f.Host]; seen && old.Transport == tplinkXOR {
			continue
		}
		byHost[f.Host] = f
	}
	out := make([]tplinkFound, 0, len(byHost))
	seen := map[string]bool{}
	for _, f := range byHost {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		out = append(out, f)
	}
	return out
}

// tplinkParseLegacyAnswer reads an old Kasa device's answer on 9999 (outside
// data): its get_sysinfo, which carries the whole state.
func tplinkParseLegacyAnswer(plain []byte) (tplinkFound, bool) {
	if !gjson.ValidBytes(plain) {
		return tplinkFound{}, false
	}
	sys := gjson.GetBytes(plain, "system.get_sysinfo")
	if !sys.IsObject() {
		return tplinkFound{}, false
	}
	mac := sys.Get("mac").String()
	if mac == "" {
		mac = sys.Get("mic_mac").String()
	}
	id := tplinkDeviceID(mac, sys.Get("deviceId").String())
	if id == "" {
		return tplinkFound{}, false
	}
	typ := sys.Get("type").String()
	if typ == "" {
		typ = sys.Get("mic_type").String()
	}
	return tplinkFound{
		ID: id, Port: tplinkLegacyPort, Family: "iot", Transport: tplinkXOR,
		Model: sys.Get("model").String(), Type: typ, Name: strings.TrimSpace(sys.Get("alias").String()),
		sysinfo: []byte(sys.Raw),
	}, true
}

// tplinkParseTDPAnswer reads an answer on 20002 (outside data): a 16-byte
// header, then JSON naming the device and its transport. Cameras and other
// devices that only speak HTTPS are left out.
func tplinkParseTDPAnswer(pkt []byte) (tplinkFound, bool) {
	if len(pkt) <= 16 || !gjson.ValidBytes(pkt[16:]) {
		return tplinkFound{}, false
	}
	res := gjson.GetBytes(pkt[16:], "result")
	if !res.IsObject() {
		return tplinkFound{}, false
	}
	typ := strings.ToUpper(strings.TrimSpace(res.Get("device_type").String()))
	id := tplinkDeviceID(res.Get("mac").String(), res.Get("device_id").String())
	if id == "" || typ == "" {
		return tplinkFound{}, false
	}
	schm := res.Get("mgt_encrypt_schm")
	if schm.Get("is_support_https").Bool() || strings.Contains(typ, "CAMERA") || strings.Contains(typ, "DOORBELL") ||
		strings.Contains(typ, "ROBOVAC") {
		return tplinkFound{}, false
	}
	f := tplinkFound{
		ID: id, Port: tplinkHTTPPort, Family: "smart", Model: res.Get("device_model").String(), Type: typ,
		LoginV: int(schm.Get("lv").Int()),
	}
	if strings.HasPrefix(typ, "IOT.") {
		f.Family = "iot"
	}
	if p := schm.Get("http_port").Int(); p > 0 && p < 65536 {
		f.Port = int(p)
	}
	switch strings.ToUpper(schm.Get("encrypt_type").String()) {
	case "KLAP":
		f.Transport = tplinkKLAP
	case "AES":
		f.Transport = tplinkAES
	case "XOR":
		f.Transport, f.Port = tplinkXOR, tplinkLegacyPort
	case "TPAP":
		f.Transport = tplinkTPAP
	default:
		return tplinkFound{}, false
	}
	f.Name = tplinkModelName(typ, f.Model)
	return f, true
}

// ── One device ───────────────────────────────────────────────────────────

// tplinkDevice is one device's connection: its address and transport and the
// open KLAP or secure-passthrough session, kept between calls (a handshake
// is two round trips, and for AES an RSA decryption). mu keeps one call at a
// time: a session's sequence numbers must arrive in order.
type tplinkDevice struct {
	mu    sync.Mutex
	found tplinkFound
	// fresh is the old Kasa discovery answer's state, read instead of asking
	// again the first time (under mu).
	fresh []byte
	// onlyUser offers the user's TP-Link ID and no setup account: the check
	// of a newly typed ID must not pass on a setup account.
	onlyUser bool

	klap   *tplinkKLAPSession
	aes    *tplinkAESSession
	token  string
	cookie string
	until  time.Time

	// The last refused sign-in: with which TP-Link ID, and when.
	refusedUser string
	refusedAt   time.Time
}

// tplinkHTTP is the devices' client: no proxy, no redirects, a new connection
// per request (a device that closes an idle one would otherwise fail the next
// POST, which is never retried).
var tplinkHTTP = &http.Client{
	Timeout: tplinkTimeout,
	Transport: &http.Transport{
		DialContext:       (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		DisableKeepAlives: true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// tplinkAESHeaders are the secure-passthrough requests' headers, as the Tapo
// app sends them.
var tplinkAESHeaders = map[string]string{"requestByApp": "true", "Accept": "application/json"}

// baseURL is the device's HTTP root.
func (d *tplinkDevice) baseURL() string {
	port := d.found.Port
	if port <= 0 {
		port = tplinkHTTPPort
	}
	return "http://" + net.JoinHostPort(d.found.Host, strconv.Itoa(port))
}

// lanOK reports whether the device's address is on the home network.
func (d *tplinkDevice) lanOK() bool {
	ip := net.ParseIP(d.found.Host)
	return ip != nil && webdavLANIP(ip)
}

// tplinkPost sends one POST and returns the status, the body (bounded) and
// the session cookies the device set.
func tplinkPost(ctx context.Context, url string, body []byte, contentType, cookie string, headers map[string]string) (int, []byte, map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := tplinkHTTP.Do(req)
	if err != nil {
		return 0, nil, nil, errors.New("the device did not answer")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, tplinkAnswerLimit))
	if err != nil {
		return 0, nil, nil, errors.New("the device's answer was cut off")
	}
	return resp.StatusCode, data, tplinkCookies(resp.Header.Values("Set-Cookie")), nil
}

// tplinkCookies reads TP_SESSIONID and TIMEOUT from Set-Cookie headers. The
// devices put both in one header ("TP_SESSIONID=…;TIMEOUT=86400"), which a
// cookie parser takes as one cookie with an attribute, so they are split by
// hand.
func tplinkCookies(headers []string) map[string]string {
	out := map[string]string{}
	for _, h := range headers {
		for _, part := range strings.Split(h, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				continue
			}
			switch k = strings.TrimSpace(k); k {
			case "TP_SESSIONID", "TIMEOUT":
				out[k] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// tplinkSessionLife is how long a session is used: the device's TIMEOUT less
// a margin, a day when it gave none.
func tplinkSessionLife(cookies map[string]string) time.Duration {
	secs, err := strconv.Atoi(cookies["TIMEOUT"])
	if err != nil || secs <= 0 {
		secs = 86400
	}
	if secs > 2400 {
		secs -= 1200
	} else {
		secs /= 2
	}
	return time.Duration(secs) * time.Second
}

// tplinkCookieHeader is the Cookie header that carries the session.
func tplinkCookieHeader(cookies map[string]string) string {
	if id := cookies["TP_SESSIONID"]; id != "" {
		return "TP_SESSIONID=" + id
	}
	return ""
}

// refused reports whether creds were refused a short while ago.
func (d *tplinkDevice) refused(creds tplinkCreds) bool {
	return !d.refusedAt.IsZero() && d.refusedUser == creds.User+"\x00"+creds.Pass &&
		time.Since(d.refusedAt) < tplinkAuthBackoff
}

// noteRefused remembers a refused sign-in.
func (d *tplinkDevice) noteRefused(creds tplinkCreds) {
	d.refusedUser, d.refusedAt = creds.User+"\x00"+creds.Pass, time.Now()
}

// dropSession forgets the open session.
func (d *tplinkDevice) dropSession() {
	d.klap, d.aes, d.token, d.cookie, d.until = nil, nil, "", "", time.Time{}
}

// query sends one request in the device's own JSON family and returns the
// answer, opening a session first when needed.
func (d *tplinkDevice) query(ctx context.Context, creds tplinkCreds, req []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.lanOK() {
		return nil, errTPLinkNotLAN
	}
	switch d.found.Transport {
	case tplinkXOR:
		return tplinkXORQuery(ctx, d.found.Host, d.found.Port, req)
	case tplinkKLAP, tplinkAES:
		if d.refused(creds) {
			return nil, errTPLinkAuth
		}
		out, err := d.sessionQuery(ctx, creds, req)
		if errors.Is(err, errTPLinkSession) {
			d.dropSession()
			out, err = d.sessionQuery(ctx, creds, req)
		}
		if errors.Is(err, errTPLinkAuth) {
			d.noteRefused(creds)
		}
		return out, err
	case tplinkTPAP:
		return nil, errTPLinkTPAP
	}
	return nil, errTPLinkKind
}

// sessionQuery is one request over KLAP or secure passthrough.
func (d *tplinkDevice) sessionQuery(ctx context.Context, creds tplinkCreds, req []byte) ([]byte, error) {
	if d.found.Transport == tplinkKLAP {
		if d.klap == nil || time.Now().After(d.until) {
			d.dropSession()
			if err := d.klapHandshake(ctx, creds); err != nil {
				return nil, err
			}
		}
		body, seq, err := d.klap.encrypt(req)
		if err != nil {
			return nil, err
		}
		url := d.baseURL() + "/app/request?seq=" + strconv.Itoa(int(seq))
		st, resp, _, err := tplinkPost(ctx, url, body, "application/octet-stream", d.cookie, nil)
		if err != nil {
			return nil, err
		}
		switch st {
		case http.StatusOK:
			return d.klap.decrypt(resp, seq)
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, errTPLinkSession
		}
		return nil, fmt.Errorf("the device answered %d", st)
	}
	if d.aes == nil || d.token == "" || time.Now().After(d.until) {
		d.dropSession()
		if err := d.aesLogin(ctx, creds); err != nil {
			return nil, err
		}
	}
	return d.aesPassthrough(ctx, d.baseURL()+"/app?token="+d.token, req)
}

// klapHandshake opens a KLAP session. handshake1's answer proves which
// account the device knows — the user's TP-Link ID or a setup account, in
// either hash form — without a single wrong guess reaching the device.
func (d *tplinkDevice) klapHandshake(ctx context.Context, creds tplinkCreds) error {
	local := make([]byte, 16)
	if _, err := rand.Read(local); err != nil {
		return err
	}
	st, body, cookies, err := tplinkPost(ctx, d.baseURL()+"/app/handshake1", local, "application/octet-stream", "", nil)
	if err != nil {
		return err
	}
	if st != http.StatusOK || len(body) < 48 {
		return fmt.Errorf("the device refused the handshake (%d)", st)
	}
	remote, server := body[:16], body[16:48]
	forms := []bool{true, false}
	if d.found.Family == "iot" {
		forms = []bool{false, true}
	}
	accounts := tplinkAccounts(creds)
	if d.onlyUser {
		accounts = []tplinkCreds{creds}
	}
	var auth []byte
	v2 := false
	for _, form := range forms {
		for _, a := range accounts {
			h := tplinkKLAPAuthHash(form, a.User, a.Pass)
			if bytes.Equal(tplinkKLAPServerHash(form, local, remote, h), server) {
				auth, v2 = h, form
				break
			}
		}
		if auth != nil {
			break
		}
	}
	if auth == nil {
		return errTPLinkAuth
	}
	cookie := tplinkCookieHeader(cookies)
	st, _, _, err = tplinkPost(ctx, d.baseURL()+"/app/handshake2", tplinkKLAPClientHash(v2, local, remote, auth),
		"application/octet-stream", cookie, nil)
	if err != nil {
		return err
	}
	if st != http.StatusOK {
		return fmt.Errorf("the device refused the second handshake (%d)", st)
	}
	d.klap, d.cookie, d.until = newTPLinkKLAPSession(local, remote, auth), cookie, time.Now().Add(tplinkSessionLife(cookies))
	return nil
}

// aesHandshake sends JukaHub's public key and opens the session the device
// answers with.
func (d *tplinkDevice) aesHandshake(ctx context.Context) error {
	key, pem, err := tplinkRSAKey()
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]interface{}{"method": "handshake", "params": map[string]string{"key": pem}})
	if err != nil {
		return err
	}
	st, resp, cookies, err := tplinkPost(ctx, d.baseURL()+"/app", body, "application/json", "", tplinkAESHeaders)
	if err != nil {
		return err
	}
	if st != http.StatusOK || !gjson.ValidBytes(resp) {
		return fmt.Errorf("the device refused the handshake (%d)", st)
	}
	if code := gjson.GetBytes(resp, "error_code").Int(); code != 0 {
		return fmt.Errorf("the device refused the handshake (error %d)", code)
	}
	sess, err := tplinkAESSessionFrom(key, gjson.GetBytes(resp, "result.key").String())
	if err != nil {
		return err
	}
	d.aes, d.token, d.cookie, d.until = sess, "", tplinkCookieHeader(cookies), time.Now().Add(tplinkSessionLife(cookies))
	return nil
}

// tplinkAESAuthCodes are the answers that mean "wrong account".
func tplinkAESAuthCode(code int64) bool { return code == -1501 || code == 1111 }

// tplinkAESSessionCode: the session is over (or never took); a new handshake
// may help.
func tplinkAESSessionCode(code int64) bool {
	switch code {
	case 9999, 1002, 1003, 1100, 1112, -1005, -1:
		return true
	}
	return false
}

// aesLogin signs in: the user's TP-Link ID, then (only when it was refused or
// none is set) the Tapo setup account, each after a fresh handshake.
func (d *tplinkDevice) aesLogin(ctx context.Context, creds tplinkCreds) error {
	tries := []tplinkCreds{}
	if creds.User != "" {
		tries = append(tries, creds)
	}
	for _, a := range tplinkAccounts(tplinkCreds{}) {
		if a.User == "test@tp-link.net" && !d.onlyUser {
			tries = append(tries, a)
		}
	}
	for _, a := range tries {
		if err := d.aesHandshake(ctx); err != nil {
			return err
		}
		params := tplinkAESCredentials(d.found.LoginV == 2, a.User, a.Pass)
		inner, err := json.Marshal(map[string]interface{}{
			"method": "login_device", "params": params, "request_time_milis": time.Now().UnixMilli(),
		})
		if err != nil {
			return err
		}
		resp, err := d.aesPassthrough(ctx, d.baseURL()+"/app", inner)
		if err != nil {
			d.dropSession()
			return err
		}
		code := gjson.GetBytes(resp, "error_code").Int()
		token := gjson.GetBytes(resp, "result.token").String()
		if code == 0 && token != "" {
			d.token = token
			return nil
		}
		d.dropSession()
		if !tplinkAESAuthCode(code) {
			return fmt.Errorf("the device refused the sign-in (error %d)", code)
		}
	}
	return errTPLinkAuth
}

// aesPassthrough sends one encrypted request in its JSON envelope.
func (d *tplinkDevice) aesPassthrough(ctx context.Context, url string, inner []byte) ([]byte, error) {
	if d.aes == nil {
		return nil, errTPLinkSession
	}
	enc, err := d.aes.encrypt(inner)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]interface{}{"method": "securePassthrough", "params": map[string]string{"request": enc}})
	if err != nil {
		return nil, err
	}
	st, resp, _, err := tplinkPost(ctx, url, body, "application/json", d.cookie, tplinkAESHeaders)
	if err != nil {
		return nil, err
	}
	if st == http.StatusUnauthorized || st == http.StatusForbidden {
		return nil, errTPLinkSession
	}
	if st != http.StatusOK || !gjson.ValidBytes(resp) {
		return nil, fmt.Errorf("the device answered %d", st)
	}
	if code := gjson.GetBytes(resp, "error_code").Int(); code != 0 {
		if tplinkAESSessionCode(code) {
			return nil, errTPLinkSession
		}
		if tplinkAESAuthCode(code) {
			return nil, errTPLinkAuth
		}
		return nil, fmt.Errorf("the device answered error %d", code)
	}
	return d.aes.decrypt(gjson.GetBytes(resp, "result.response").String())
}

// tplinkXORQuery is one request to an old Kasa device over TCP.
func tplinkXORQuery(ctx context.Context, host string, port int, req []byte) ([]byte, error) {
	if port <= 0 {
		port = tplinkLegacyPort
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, errors.New("the device did not answer")
	}
	defer conn.Close()
	deadline := time.Now().Add(tplinkTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(tplinkXORFrame(req)); err != nil {
		return nil, errors.New("the device did not answer")
	}
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, errors.New("the device did not answer")
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > tplinkAnswerLimit {
		return nil, errors.New("the device's answer has a bad length")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, errors.New("the device's answer was cut off")
	}
	return tplinkXORDecrypt(buf), nil
}

// ── The two JSON families ────────────────────────────────────────────────

// tplinkTerminal is the "terminal_uuid" of SMART requests: base64 of an MD5
// of random bytes, made once.
var tplinkTerminal = sync.OnceValue(func() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	h := md5.Sum(b[:])
	return base64.StdEncoding.EncodeToString(h[:])
})

// tplinkSmartRequest is one SMART (Tapo) request.
func tplinkSmartRequest(method string, params interface{}) []byte {
	m := map[string]interface{}{
		"method": method, "request_time_milis": time.Now().UnixMilli(), "terminal_uuid": tplinkTerminal(),
	}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return b
}

// smartCall sends a SMART request and returns its result.
func (d *tplinkDevice) smartCall(ctx context.Context, creds tplinkCreds, method string, params interface{}) (gjson.Result, error) {
	resp, err := d.query(ctx, creds, tplinkSmartRequest(method, params))
	if err != nil {
		return gjson.Result{}, err
	}
	if !gjson.ValidBytes(resp) {
		return gjson.Result{}, errors.New("the device's answer is not JSON")
	}
	if code := gjson.GetBytes(resp, "error_code").Int(); code != 0 {
		return gjson.Result{}, fmt.Errorf("the device answered error %d", code)
	}
	return gjson.GetBytes(resp, "result"), nil
}

// iotCall sends an IOT (Kasa) request and returns the answer, refusing one
// whose err_code says the command failed.
func (d *tplinkDevice) iotCall(ctx context.Context, creds tplinkCreds, req string) (gjson.Result, error) {
	resp, err := d.query(ctx, creds, []byte(req))
	if err != nil {
		return gjson.Result{}, err
	}
	if !gjson.ValidBytes(resp) {
		return gjson.Result{}, errors.New("the device's answer is not JSON")
	}
	root := gjson.ParseBytes(resp)
	if err := tplinkIOTErr(root, 0); err != nil {
		return gjson.Result{}, err
	}
	return root, nil
}

// tplinkIOTErr finds a non-zero err_code a few levels down an IOT answer.
func tplinkIOTErr(v gjson.Result, depth int) error {
	if depth > 3 || !v.IsObject() {
		return nil
	}
	if c := v.Get("err_code"); c.Exists() && c.Int() != 0 {
		msg := strings.TrimSpace(v.Get("err_msg").String())
		if msg == "" {
			msg = "error " + strconv.FormatInt(c.Int(), 10)
		}
		return errors.New("the device answered: " + msg)
	}
	var found error
	v.ForEach(func(_, child gjson.Result) bool {
		found = tplinkIOTErr(child, depth+1)
		return found == nil
	})
	return found
}

// tplinkState is one row a device shows: the device itself, one socket of a
// strip or one sensor of a hub. DeviceID is a Kasa strip's own id, which its
// sockets' context may need in front of theirs.
type tplinkState struct {
	Child    string
	DeviceID string
	Name     string
	Kind     string
	Model    string
	Parent   string
	On       bool
	CanOn    bool
	Level    int
	CanLevel bool
	CanTemp  bool
	TempMin  int
	TempMax  int
	Sensor   string
}

// tplinkParseIOT reads a Kasa get_sysinfo (outside data).
func tplinkParseIOT(sys gjson.Result) []tplinkState {
	typ := strings.ToLower(sys.Get("type").String())
	if typ == "" {
		typ = strings.ToLower(sys.Get("mic_type").String())
	}
	model := strings.TrimSpace(sys.Get("model").String())
	name := strings.TrimSpace(sys.Get("alias").String())
	if name == "" {
		name = tplinkModelName("IOT.", model)
	}
	devName := strings.ToLower(sys.Get("dev_name").String())
	switch {
	case strings.Contains(typ, "smartbulb"):
		ls := sys.Get("light_state")
		st := tplinkState{
			Name: name, Kind: shubKindBulb, Model: model, On: ls.Get("on_off").Int() == 1, CanOn: true, Level: -1,
			CanLevel: sys.Get("is_dimmable").Int() == 1, CanTemp: sys.Get("is_variable_color_temp").Int() == 1,
		}
		if sys.Get("length").Exists() {
			st.Kind = shubKindLightStrip
		}
		if b := ls.Get("brightness"); b.Exists() {
			st.Level = int(b.Int())
		} else if b := ls.Get("dft_on_state.brightness"); b.Exists() {
			st.Level = int(b.Int())
		}
		if st.CanTemp {
			st.TempMin, st.TempMax = 2700, 6500
		}
		return []tplinkState{st}
	case strings.Contains(typ, "plug") || strings.Contains(typ, "switch"):
		if ch := sys.Get("children"); ch.IsArray() && len(ch.Array()) > 0 {
			out := make([]tplinkState, 0, len(ch.Array()))
			for i, c := range ch.Array() {
				cname := strings.TrimSpace(c.Get("alias").String())
				if cname == "" {
					cname = name + " · socket " + strconv.Itoa(i+1)
				}
				out = append(out, tplinkState{
					Child: c.Get("id").String(), DeviceID: sys.Get("deviceId").String(), Name: cname, Kind: shubKindOutlet,
					Model: model, Parent: name, On: c.Get("state").Int() == 1, CanOn: true, Level: -1,
				})
			}
			return out
		}
		st := tplinkState{Name: name, Kind: shubKindPlug, Model: model, On: sys.Get("relay_state").Int() == 1, CanOn: true, Level: -1}
		switch b := sys.Get("brightness"); {
		case b.Exists() || strings.Contains(devName, "dimmer"):
			st.Kind, st.CanLevel = shubKindDimmer, true
			if b.Exists() {
				st.Level = int(b.Int())
			}
		case strings.Contains(devName, "light") || strings.Contains(devName, "switch"):
			st.Kind = shubKindSwitch
		}
		return []tplinkState{st}
	}
	return []tplinkState{{Name: name, Kind: shubKindHub, Model: model, Level: -1}}
}

// tplinkNickname decodes a SMART nickname (base64; a plain one is kept).
func tplinkNickname(raw string) string {
	raw = strings.TrimSpace(raw)
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) > 0 {
		return strings.TrimSpace(strings.ToValidUTF8(string(b), "?"))
	}
	return raw
}

// tplinkSensorLine is a hub sensor's reading.
func tplinkSensorLine(c gjson.Result) string {
	switch {
	case c.Get("current_temp").Exists():
		unit := "°C"
		if strings.HasPrefix(strings.ToLower(c.Get("temp_unit").String()), "f") {
			unit = "°F"
		}
		line := strconv.FormatFloat(c.Get("current_temp").Float(), 'f', 1, 64) + unit
		if h := c.Get("current_humidity"); h.Exists() {
			line += " · " + strconv.FormatInt(h.Int(), 10) + "%"
		}
		return line
	case c.Get("open").Exists():
		if c.Get("open").Bool() {
			return "Open"
		}
		return "Closed"
	case c.Get("detected").Exists():
		if c.Get("detected").Bool() {
			return "Motion"
		}
		return "No motion"
	case c.Get("water_leak_status").Exists():
		if strings.EqualFold(c.Get("water_leak_status").String(), "normal") {
			return "Dry"
		}
		return "Water leak!"
	}
	if strings.EqualFold(c.Get("status").String(), "offline") {
		return "Offline"
	}
	return "Connected"
}

// tplinkParseSmart reads a Tapo get_device_info and its children (outside
// data).
func tplinkParseSmart(info gjson.Result, children []gjson.Result) []tplinkState {
	typ := strings.ToUpper(info.Get("type").String())
	model := strings.TrimSpace(info.Get("model").String())
	name := tplinkNickname(info.Get("nickname").String())
	if name == "" {
		name = tplinkModelName(typ, model)
	}
	var out []tplinkState
	if on := info.Get("device_on"); on.Exists() {
		st := tplinkState{Name: name, Kind: shubKindPlug, Model: model, On: on.Bool(), CanOn: true, Level: -1}
		switch {
		case strings.Contains(typ, "BULB"):
			st.Kind = shubKindBulb
			if strings.HasPrefix(strings.ToUpper(model), "L9") {
				st.Kind = shubKindLightStrip
			}
		case strings.Contains(typ, "SWITCH"):
			st.Kind = shubKindSwitch
		}
		if b := info.Get("brightness"); b.Exists() {
			st.Level, st.CanLevel = int(b.Int()), true
			if st.Kind == shubKindPlug || st.Kind == shubKindSwitch {
				st.Kind = shubKindDimmer
			}
		}
		if r := info.Get("color_temp_range").Array(); len(r) == 2 && r[1].Int() > r[0].Int() && r[0].Int() > 0 {
			st.CanTemp, st.TempMin, st.TempMax = true, int(r[0].Int()), int(r[1].Int())
		}
		out = append(out, st)
	}
	for i, c := range children {
		cname := tplinkNickname(c.Get("nickname").String())
		ctyp := strings.ToUpper(c.Get("type").String())
		if cname == "" {
			pos := int(c.Get("position").Int())
			if pos <= 0 {
				pos = i + 1
			}
			cname = name + " · " + strconv.Itoa(pos)
		}
		cs := tplinkState{Child: c.Get("device_id").String(), Name: cname, Model: c.Get("model").String(), Parent: name, Level: -1}
		if cs.Child == "" {
			continue
		}
		if on := c.Get("device_on"); on.Exists() {
			cs.Kind, cs.On, cs.CanOn = shubKindOutlet, on.Bool(), true
			switch {
			case strings.Contains(ctyp, "BULB"):
				cs.Kind = shubKindBulb
			case strings.Contains(ctyp, "SWITCH"):
				cs.Kind = shubKindSwitch
			}
			if b := c.Get("brightness"); b.Exists() {
				cs.Level, cs.CanLevel = int(b.Int()), true
				if cs.Kind != shubKindBulb {
					cs.Kind = shubKindDimmer
				}
			}
		} else {
			cs.Kind, cs.Sensor = shubKindSensor, tplinkSensorLine(c)
		}
		out = append(out, cs)
	}
	if len(out) == 0 {
		out = append(out, tplinkState{Name: name, Kind: shubKindHub, Model: model, Level: -1})
	}
	return out
}

// tplinkHasChildren reports whether a SMART device's children are worth
// asking for: a hub, or a strip whose own state has no switch.
func tplinkHasChildren(info gjson.Result) bool {
	typ := strings.ToUpper(info.Get("type").String())
	model := strings.ToUpper(info.Get("model").String())
	return strings.Contains(typ, "HUB") || !info.Get("device_on").Exists() ||
		strings.HasPrefix(model, "P30") || strings.HasPrefix(model, "P31") || strings.HasPrefix(model, "KP30") ||
		strings.HasPrefix(model, "EP40") || strings.HasPrefix(model, "HS30")
}

// state reads the device and returns its rows.
func (d *tplinkDevice) state(ctx context.Context, creds tplinkCreds) ([]tplinkState, error) {
	if d.found.Family == "iot" {
		d.mu.Lock()
		fresh := d.fresh
		d.fresh = nil
		d.mu.Unlock()
		if fresh != nil {
			// The discovery answer is a second old: no second round trip.
			return tplinkParseIOT(gjson.ParseBytes(fresh)), nil
		}
		root, err := d.iotCall(ctx, creds, `{"system":{"get_sysinfo":{}}}`)
		if err != nil {
			return nil, err
		}
		return tplinkParseIOT(root.Get("system.get_sysinfo")), nil
	}
	info, err := d.smartCall(ctx, creds, "get_device_info", nil)
	if err != nil {
		return nil, err
	}
	var children []gjson.Result
	if tplinkHasChildren(info) {
		var params interface{}
		for page := 0; page < 8; page++ {
			res, err := d.smartCall(ctx, creds, "get_child_device_list", params)
			if err != nil {
				break
			}
			list := res.Get("child_device_list").Array()
			children = append(children, list...)
			sum := int(res.Get("sum").Int())
			if len(list) == 0 || len(children) >= sum {
				break
			}
			params = map[string]int{"start_index": len(children)}
		}
	}
	return tplinkParseSmart(info, children), nil
}

// tplinkIOTChildID is a socket's id as the context wants it: the full id (a
// strip that lists "00", "01" gets its own id in front).
func tplinkIOTChildID(deviceID, child string) string {
	if len(child) <= 2 && deviceID != "" {
		return deviceID + child
	}
	return child
}

// tplinkLightService names the Kasa light service and its set method for a
// bulb or a light strip.
func tplinkLightService(kind string) (string, string) {
	if kind == shubKindLightStrip {
		return "smartlife.iot.lightStrip", "set_light_state"
	}
	return "smartlife.iot.smartbulb.lightingservice", "transition_light_state"
}

// tplinkJSONString is s as a JSON string literal.
func tplinkJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// setOn switches the row st (the device, or the child it names).
func (d *tplinkDevice) setOn(ctx context.Context, creds tplinkCreds, st tplinkState, on bool) error {
	state := 0
	if on {
		state = 1
	}
	if d.found.Family == "iot" {
		var req string
		switch {
		case st.Child != "":
			req = `{"context":{"child_ids":[` + tplinkJSONString(tplinkIOTChildID(st.DeviceID, st.Child)) + `]},` +
				`"system":{"set_relay_state":{"state":` + strconv.Itoa(state) + `}}}`
		case st.Kind == shubKindBulb || st.Kind == shubKindLightStrip:
			// Switched on without a level, the bulb comes back as it was
			// (ignore_default 0); switched off it keeps that state.
			service, method := tplinkLightService(st.Kind)
			req = `{` + tplinkJSONString(service) + `:{` + tplinkJSONString(method) + `:{"on_off":` + strconv.Itoa(state) +
				`,"ignore_default":` + strconv.Itoa(1-state) + `}}}`
		default:
			req = `{"system":{"set_relay_state":{"state":` + strconv.Itoa(state) + `}}}`
		}
		_, err := d.iotCall(ctx, creds, req)
		return err
	}
	params := map[string]interface{}{"device_on": on}
	if st.Child != "" {
		_, err := d.smartCall(ctx, creds, "control_child", map[string]interface{}{
			"device_id": st.Child, "requestData": map[string]interface{}{"method": "set_device_info", "params": params},
		})
		return err
	}
	_, err := d.smartCall(ctx, creds, "set_device_info", params)
	return err
}

// setLevel sets a light's brightness (1–100), switching it on.
func (d *tplinkDevice) setLevel(ctx context.Context, creds tplinkCreds, st tplinkState, level int) error {
	level = max(1, min(100, level))
	if d.found.Family == "iot" {
		var req string
		if st.Kind == shubKindDimmer {
			req = `{"smartlife.iot.dimmer":{"set_brightness":{"brightness":` + strconv.Itoa(level) + `}}}`
			if !st.On {
				if err := d.setOn(ctx, creds, st, true); err != nil {
					return err
				}
			}
		} else {
			service, method := tplinkLightService(st.Kind)
			req = `{` + tplinkJSONString(service) + `:{` + tplinkJSONString(method) + `:{"brightness":` + strconv.Itoa(level) +
				`,"on_off":1,"ignore_default":1}}}`
		}
		_, err := d.iotCall(ctx, creds, req)
		return err
	}
	params := map[string]interface{}{"brightness": level, "device_on": true}
	if st.Child != "" {
		_, err := d.smartCall(ctx, creds, "control_child", map[string]interface{}{
			"device_id": st.Child, "requestData": map[string]interface{}{"method": "set_device_info", "params": params},
		})
		return err
	}
	_, err := d.smartCall(ctx, creds, "set_device_info", params)
	return err
}

// setTemp sets a white light's colour temperature in kelvin, switching it on.
func (d *tplinkDevice) setTemp(ctx context.Context, creds tplinkCreds, st tplinkState, kelvin int) error {
	if st.TempMax > st.TempMin && st.TempMin > 0 {
		kelvin = max(st.TempMin, min(st.TempMax, kelvin))
	}
	if d.found.Family == "iot" {
		service, method := tplinkLightService(st.Kind)
		req := `{` + tplinkJSONString(service) + `:{` + tplinkJSONString(method) + `:{"color_temp":` + strconv.Itoa(kelvin) +
			`,"on_off":1,"ignore_default":1}}}`
		_, err := d.iotCall(ctx, creds, req)
		return err
	}
	_, err := d.smartCall(ctx, creds, "set_device_info", map[string]interface{}{"color_temp": kelvin, "device_on": true})
	return err
}
