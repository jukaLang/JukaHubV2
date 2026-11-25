package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestParseTailscaleStatusPeerList covers NET-18: the row must show this
// device's own address and list the other nodes with name, address and online
// state, in a stable order.
func TestParseTailscaleStatusPeerList(t *testing.T) {
	data := []byte(`{
		"BackendState": "Running",
		"Self": {"HostName": "handheld", "TailscaleIPs": ["100.64.0.5"]},
		"Peer": {
			"key-b": {"HostName": "phone", "DNSName": "phone.tailnet.ts.net.", "TailscaleIPs": ["100.64.0.9"], "Online": false},
			"key-a": {"HostName": "laptop", "DNSName": "laptop.tailnet.ts.net.", "TailscaleIPs": ["100.64.0.7"], "Online": true}
		}
	}`)
	snap, err := parseTailscaleStatus(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.IPs) != 1 || snap.IPs[0] != "100.64.0.5" {
		t.Fatalf("self address = %v, want [100.64.0.5]", snap.IPs)
	}
	if len(snap.Peers) != 2 {
		t.Fatalf("peers = %+v, want 2", snap.Peers)
	}
	if snap.Peers[0].Name != "laptop.tailnet.ts.net" || !snap.Peers[0].Online {
		t.Fatalf("first peer = %+v, want the online laptop", snap.Peers[0])
	}
	if snap.Peers[1].Name != "phone.tailnet.ts.net" || snap.Peers[1].Online {
		t.Fatalf("second peer = %+v, want the offline phone", snap.Peers[1])
	}
	line := tailscalePeersLine(snap.Peers)
	for _, want := range []string{"laptop.tailnet.ts.net (100.64.0.7, online)", "phone.tailnet.ts.net (100.64.0.9, offline)"} {
		if !strings.Contains(line, want) {
			t.Fatalf("peer line %q does not name %q", line, want)
		}
	}
}

// TestTPLinkEncodingsKnownAnswers checks the TP-Link encodings
// (smarthome_tplink_crypto.go) against answers computed independently with
// Python's hashlib and cryptography from the published derivations: a wrong
// byte order or concatenation here locks every Kasa and Tapo device out.
func TestTPLinkEncodingsKnownAnswers(t *testing.T) {
	req := []byte(`{"system":{"get_sysinfo":{}}}`)
	if got := hex.EncodeToString(tplinkXOREncrypt(req)); got != "d0f281f88bff9af7d5ef94b6d1b4c09fec95e68fe187e8caf08bf68bf6" {
		t.Fatalf("XOR = %s", got)
	}
	if !bytes.Equal(tplinkXORDecrypt(tplinkXOREncrypt(req)), req) {
		t.Fatal("XOR does not round-trip")
	}
	if f := tplinkXORFrame(req); len(f) != 4+len(req) || binary.BigEndian.Uint32(f) != uint32(len(req)) {
		t.Fatalf("XOR frame = %x", f)
	}

	v1 := tplinkKLAPAuthHash(false, "user@example.com", "secret")
	v2 := tplinkKLAPAuthHash(true, "user@example.com", "secret")
	local, remote := make([]byte, 16), make([]byte, 16)
	for i := range local {
		local[i], remote[i] = byte(i), byte(16+i)
	}
	for _, c := range []struct {
		name string
		got  []byte
		want string
	}{
		{"auth v1", v1, "9d6ce791ce44316e9e52fc1bc9d36496"},
		{"auth v2", v2, "b039216532fc844e9ae0cc8fe3ea911c9ba09c641bb96a3e1f776d93f7b5ae9b"},
		{"server v1", tplinkKLAPServerHash(false, local, remote, v1), "c2d456076f946c707000874d2e226a1c09e89a21e459272b8ec0f01cdaf774cf"},
		{"server v2", tplinkKLAPServerHash(true, local, remote, v2), "8ed3b905e654fbee23900d0244156a33029c025b71b03bfcc601eff33aaebb70"},
		{"client v1", tplinkKLAPClientHash(false, local, remote, v1), "50ef3325d42cd7c999aa9e47e8cb0f8a7f6f239f1d51b39d46d581a4bb75c8c5"},
		{"client v2", tplinkKLAPClientHash(true, local, remote, v2), "1e590e7a4dc128da48ed17e06fe23ddb6ec646bb6da2eb00708d590f194201d7"},
	} {
		if got := hex.EncodeToString(c.got); got != c.want {
			t.Errorf("%s = %s, want %s", c.name, got, c.want)
		}
	}

	s := newTPLinkKLAPSession(local, remote, v2)
	if hex.EncodeToString(s.key) != "35dcf29cd535ce140a239c67f641d395" || hex.EncodeToString(s.iv) != "863e09a70667472d3fa152a4" ||
		hex.EncodeToString(s.sig) != "777443e2fdd5cb21052633a60e830f44c8654b3a701f449231ed11fa" || s.seq != -903964344 {
		t.Fatalf("KLAP session key %x iv %x sig %x seq %d", s.key, s.iv, s.sig, s.seq)
	}
	body, seq, err := s.encrypt(req)
	want := "ee33583262ebfd6b6977db181b8ba22a26f04b66d42c628224c0a40961942c64b2ea7862e52efd2186b9d6c73afb86a8b9821180993deb7e55bf8d03b44290d9"
	if err != nil || seq != -903964343 || hex.EncodeToString(body) != want {
		t.Fatalf("KLAP request = %x (seq %d, %v)", body, seq, err)
	}
	// An answer has the same shape: a signature, then the cipher text under
	// the request's IV.
	if plain, err := s.decrypt(body, seq); err != nil || !bytes.Equal(plain, req) {
		t.Fatalf("KLAP answer = %q, %v", plain, err)
	}

	key, iv := make([]byte, 16), make([]byte, 16)
	for i := range key {
		key[i], iv[i] = byte(100+i), byte(200+i)
	}
	aes := &tplinkAESSession{key: key, iv: iv}
	inner := `{"method":"get_device_info"}`
	enc, err := aes.encrypt([]byte(inner))
	if err != nil || enc != "cp3dYSkMh0oXRcOd4aJcH8a5mxjtfiOidHJ6f8bvbkk=" {
		t.Fatalf("passthrough = %s, %v", enc, err)
	}
	if back, err := aes.decrypt(enc); err != nil || string(back) != inner {
		t.Fatalf("passthrough answer = %q, %v", back, err)
	}
	login := tplinkAESCredentials(false, "user@example.com", "secret")
	if login["username"] != "NjNhNzEwNTY5MjYxYTI0YjM3NjYyNzViNzAwMGNlOGQ3YjMyZTJmNw==" || login["password"] != "c2VjcmV0" {
		t.Fatalf("login v1 = %v", login)
	}
	if got := tplinkAESCredentials(true, "user@example.com", "secret")["password2"]; got != "ZTVlOWZhMWJhMzFlY2QxYWU4NGY3NWNhYWE0NzRmM2E2NjNmMDVmNA==" {
		t.Fatalf("login v2 password2 = %s", got)
	}
	if _, err := tplinkPKCS7Unpad([]byte("0123456789abcde\x00")); err == nil {
		t.Fatal("zero padding was accepted")
	}

	// The bare discovery probe's CRC is the header's CRC-32 with the field
	// set to 0x5A6B7C8D, as the keyed probe's is computed.
	hdr := append([]byte(nil), tplinkTDPBare...)
	binary.BigEndian.PutUint32(hdr[12:], 0x5A6B7C8D)
	if crc32.ChecksumIEEE(hdr) != binary.BigEndian.Uint32(tplinkTDPBare[12:]) {
		t.Fatal("the bare discovery probe's CRC is wrong")
	}
	cookies := tplinkCookies([]string{"TP_SESSIONID=ABC123;TIMEOUT=1440"})
	if cookies["TP_SESSIONID"] != "ABC123" || tplinkSessionLife(cookies).Seconds() != 720 {
		t.Fatalf("cookies = %v", cookies)
	}
}

// TestSmartHomeDeviceAnswers reads what devices send (outside data): the
// TP-Link discovery answers, a Kasa strip's and bulb's state, a Tapo bulb's,
// and Google Cast's messages.
func TestSmartHomeDeviceAnswers(t *testing.T) {
	tdp := func(js string) []byte { return append(make([]byte, 16), js...) }
	f, ok := tplinkParseTDPAnswer(tdp(`{"result":{"device_id":"abc","device_type":"SMART.TAPOPLUG","device_model":"P110(EU)",` +
		`"mac":"AA-BB-CC-DD-EE-FF","mgt_encrypt_schm":{"is_support_https":false,"encrypt_type":"KLAP","http_port":80,"lv":2}},"error_code":0}`))
	if !ok || f.ID != "tp:aabbccddeeff" || f.Transport != tplinkKLAP || f.Family != "smart" || f.LoginV != 2 || f.Port != 80 || f.Name != "Tapo P110" {
		t.Fatalf("Tapo answer = %+v, %v", f, ok)
	}
	if f, ok := tplinkParseTDPAnswer(tdp(`{"result":{"device_type":"SMART.TAPOPLUG","mac":"AA-BB-CC-DD-EE-01","mgt_encrypt_schm":{"encrypt_type":"TPAP"}}}`)); !ok || f.Transport != tplinkTPAP {
		t.Fatalf("TPAP answer = %+v, %v", f, ok)
	}
	if _, ok := tplinkParseTDPAnswer(tdp(`{"result":{"device_type":"SMART.IPCAMERA","mac":"AA-BB-CC-DD-EE-02","mgt_encrypt_schm":{"is_support_https":true,"encrypt_type":"AES"}}}`)); ok {
		t.Fatal("a camera was listed")
	}

	lf, ok := tplinkParseLegacyAnswer([]byte(`{"system":{"get_sysinfo":{"alias":"Desk","model":"HS300(US)","mac":"50:C7:BF:00:11:22",` +
		`"deviceId":"8006AB","type":"IOT.SMARTPLUGSWITCH","children":[{"id":"00","state":1,"alias":"Lamp"},{"id":"01","state":0,"alias":""}]}}}`))
	if !ok || lf.ID != "tp:50c7bf001122" || lf.Transport != tplinkXOR || lf.Port != tplinkLegacyPort {
		t.Fatalf("Kasa answer = %+v, %v", lf, ok)
	}
	rows := tplinkParseIOT(gjson.ParseBytes(lf.sysinfo))
	if len(rows) != 2 || rows[0].Name != "Lamp" || !rows[0].On || rows[1].On || rows[1].Name != "Desk · socket 2" ||
		tplinkIOTChildID(rows[0].DeviceID, rows[0].Child) != "8006AB00" {
		t.Fatalf("strip rows = %+v", rows)
	}
	bulb := tplinkParseIOT(gjson.Parse(`{"alias":"Hall","mic_type":"IOT.SMARTBULB","is_dimmable":1,"light_state":{"on_off":0,"dft_on_state":{"brightness":40}}}`))
	if len(bulb) != 1 || bulb[0].Kind != shubKindBulb || bulb[0].On || bulb[0].Level != 40 || !bulb[0].CanLevel {
		t.Fatalf("bulb = %+v", bulb)
	}
	tapo := tplinkParseSmart(gjson.Parse(`{"type":"SMART.TAPOBULB","model":"L530","nickname":"RGVzayBsYW1w","device_on":true,`+
		`"brightness":70,"color_temp_range":[2500,6500]}`), nil)
	if len(tapo) != 1 || tapo[0].Name != "Desk lamp" || tapo[0].Kind != shubKindBulb || !tapo[0].On || tapo[0].Level != 70 || !tapo[0].CanTemp {
		t.Fatalf("Tapo bulb = %+v", tapo)
	}

	m, err := gcastDecode(gcastEncode("a", "b", gcastNSReceiver, `{"type":"PING"}`))
	if err != nil || m.src != "a" || m.dst != "b" || m.ns != gcastNSReceiver || m.payload != `{"type":"PING"}` {
		t.Fatalf("Cast message = %+v, %v", m, err)
	}
	if _, err := gcastDecode([]byte{0x12, 0x7f}); err == nil {
		t.Fatal("a cut Cast message was read")
	}
	st := gcastParseReceiver(gjson.Parse(`{"status":{"volume":{"level":0.35,"muted":false},"applications":[{"displayName":"Spotify",` +
		`"isIdleScreen":false,"sessionId":"s","transportId":"t","namespaces":[{"name":"urn:x-cast:com.google.cast.media"}]}]}}`))
	if st.Volume != 35 || st.App != "Spotify" || !st.Media || st.Transport != "t" || st.Session != "s" {
		t.Fatalf("receiver status = %+v", st)
	}
}
