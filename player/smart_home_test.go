package main

// smart_home_test.go — the parts of the smart-home integrations that can be
// proved without a house: URL handling, response parsing, the MQTT packet
// bytes, and the config defaults.

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSmartHomeEndpoint(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"192.168.1.20:8123", "http://192.168.1.20:8123", false},
		{"http://home.local:8123/", "http://home.local:8123", false},
		{"https://ha.example.com", "https://ha.example.com", false},
		{"  http://pi.lan/admin  ", "http://pi.lan/admin", false},
		{"ftp://pi.lan", "", true},
		{"http://", "", true},
		{"two words", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := smartHomeEndpoint(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("smartHomeEndpoint(%q) = %q, want an error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("smartHomeEndpoint(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestSmartHomeURLs(t *testing.T) {
	states, err := haStatesURL("http://ha.local:8123/")
	if err != nil || states != "http://ha.local:8123/api/states" {
		t.Errorf("states URL = %q, %v", states, err)
	}
	svc, err := haServiceURL("http://ha.local:8123", "light")
	if err != nil || svc != "http://ha.local:8123/api/services/light/toggle" {
		t.Errorf("service URL = %q, %v", svc, err)
	}
	if _, err := haServiceURL("http://ha.local:8123", "light/../config"); err == nil {
		t.Error("a domain with a slash was accepted")
	}
	if _, err := haServiceURL("http://ha.local:8123", ""); err == nil {
		t.Error("an empty domain was accepted")
	}

	v5, err := piholeV5URL("http://pi.lan", "s3cr t&x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v5, "http://pi.lan/admin/api.php?summaryRaw&auth=") {
		t.Errorf("v5 URL = %q", v5)
	}
	if strings.Contains(v5, "s3cr t") {
		t.Errorf("token was not escaped: %q", v5)
	}
	if noToken, _ := piholeV5URL("http://pi.lan", ""); strings.Contains(noToken, "auth=") {
		t.Errorf("empty token still added auth: %q", noToken)
	}
	if v6, err := piholeV6URL("http://pi.lan/"); err != nil || v6 != "http://pi.lan/api/stats/summary" {
		t.Errorf("v6 URL = %q, %v", v6, err)
	}
}

func TestParseHAStates(t *testing.T) {
	body := []byte(`[
	  {"entity_id":"light.kitchen","state":"on","attributes":{"friendly_name":"Kitchen"}},
	  {"entity_id":"sensor.temp","state":"21.5","attributes":{"friendly_name":"Temperature"}},
	  {"entity_id":"switch.garden_pump","state":"off","attributes":{}},
	  {"entity_id":"cover.garage","state":"open","attributes":{"friendly_name":"Garage"}},
	  {"entity_id":"input_boolean.guest","state":"off","attributes":{"friendly_name":"Guest mode"}},
	  {"entity_id":"fan","state":"on","attributes":{}},
	  {"entity_id":"light.","state":"on","attributes":{}}
	]`)
	got, err := parseHAStates(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("parsed %d entities, want 4: %+v", len(got), got)
	}
	// Sensors are filtered out and the list is sorted by display name.
	wantNames := []string{"Garage", "garden pump", "Guest mode", "Kitchen"}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Errorf("entity %d name = %q, want %q", i, got[i].Name, want)
		}
	}
	if got[1].EntityID != "switch.garden_pump" || got[1].Domain != "switch" {
		t.Errorf("underscore fallback did not produce a readable name: %+v", got[1])
	}
	if _, err := parseHAStates([]byte(`{"message":"API running."}`)); err == nil {
		t.Error("a non-list response was accepted")
	}
}

func TestHAEntityStateHelpers(t *testing.T) {
	for state, want := range map[string]bool{"on": true, "ON": true, "open": true, "off": false, "closed": false, "unavailable": false, "": false} {
		if got := haEntityOn(state); got != want {
			t.Errorf("haEntityOn(%q) = %v, want %v", state, got, want)
		}
	}
	if got := haEntityDomain("light.kitchen"); got != "light" {
		t.Errorf("domain = %q", got)
	}
	for _, bad := range []string{"", "light", ".lamp", "light."} {
		if got := haEntityDomain(bad); got != "" {
			t.Errorf("haEntityDomain(%q) = %q, want empty", bad, got)
		}
	}
}

func TestParsePihole(t *testing.T) {
	v5, err := parsePiholeV5([]byte(`{"dns_queries_today":1200,"ads_blocked_today":180,"ads_percentage_today":15.0,"status":"enabled"}`))
	if err != nil {
		t.Fatal(err)
	}
	if v5.Queries != 1200 || v5.Blocked != 180 || v5.Percent != 15 || v5.Status != "enabled" || !v5.Reachable {
		t.Errorf("v5 summary = %+v", v5)
	}

	v6, err := parsePiholeV6([]byte(`{"queries":{"total":900,"blocked":90,"percent_blocked":10.0},"blocking":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if v6.Queries != 900 || v6.Blocked != 90 || v6.Status != "enabled" {
		t.Errorf("v6 summary = %+v", v6)
	}
	// FTL has answered with a string in some builds; both must read the same.
	if got := piholeBlockingWord("disabled"); got != "disabled" {
		t.Errorf("string blocking = %q", got)
	}
	if got := piholeBlockingWord(false); got != "disabled" {
		t.Errorf("bool blocking = %q", got)
	}

	if _, err := parsePiholeV5([]byte(`<html>login</html>`)); err == nil {
		t.Error("an HTML login page was accepted as a summary")
	}

	line := piholeLine(piholeSnapshot{Reachable: true, Status: "enabled", Queries: 1200, Blocked: 180, Percent: 15})
	for _, want := range []string{"enabled", "1200", "180", "15.0%"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q is missing %q", line, want)
		}
	}
	if got := piholeLine(piholeSnapshot{Err: "Pi-hole unreachable"}); got != "Pi-hole unreachable" {
		t.Errorf("unreachable line = %q", got)
	}
}

func TestMQTTPackets(t *testing.T) {
	cases := map[int]int{0: 1, 127: 1, 128: 2, 16383: 2, 16384: 3, 2097151: 3, 2097152: 4}
	for n, wantLen := range cases {
		if got := len(mqttEncodingLength(n)); got != wantLen {
			t.Errorf("mqttEncodingLength(%d) = %d bytes, want %d", n, got, wantLen)
		}
	}

	connect := mqttConnectPacket("jukahub-1", "user", "pass")
	if connect[0] != 0x10 {
		t.Errorf("CONNECT header = %#x", connect[0])
	}
	if !bytes.Contains(connect, []byte("MQTT")) {
		t.Error("CONNECT has no protocol name")
	}
	// Clean session (0x02) plus username and password flags.
	idx := bytes.Index(connect, []byte("MQTT")) + 4
	if connect[idx] != 4 {
		t.Errorf("protocol level = %d, want 4", connect[idx])
	}
	if connect[idx+1] != 0xC2 {
		t.Errorf("connect flags = %#x, want 0xc2", connect[idx+1])
	}
	// Without a username the password flag must not be set either.
	anon := mqttConnectPacket("jukahub-1", "", "")
	idx = bytes.Index(anon, []byte("MQTT")) + 4
	if anon[idx+1] != 0x02 {
		t.Errorf("anonymous connect flags = %#x, want 0x02", anon[idx+1])
	}

	pub := mqttPublishPacket("jukahub/light.kitchen", "ON", false)
	if pub[0] != 0x30 {
		t.Errorf("PUBLISH header = %#x", pub[0])
	}
	if !bytes.Contains(pub, []byte("jukahub/light.kitchen")) || !bytes.HasSuffix(pub, []byte("ON")) {
		t.Errorf("PUBLISH body is wrong: %q", pub)
	}
	if retained := mqttPublishPacket("t", "x", true); retained[0] != 0x31 {
		t.Errorf("retained PUBLISH header = %#x, want 0x31", retained[0])
	}

	if got := mqttTopicFor("", "light.kitchen"); got != "jukahub/light.kitchen" {
		t.Errorf("default topic = %q", got)
	}
	if got := mqttTopicFor("/home/", "light.kitchen"); got != "home/light.kitchen" {
		t.Errorf("prefix topic = %q", got)
	}
	if !strings.Contains(mqttConnackReason(4), "password") {
		t.Errorf("connack reason 4 = %q", mqttConnackReason(4))
	}
}

func TestParseTailscale(t *testing.T) {
	snap, err := parseTailscaleStatus([]byte(`{"BackendState":"Running","Self":{"HostName":"handheld","TailscaleIPs":["100.101.102.103","fd7a::1"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Installed || !snap.Running || snap.State != "Running" {
		t.Errorf("running status = %+v", snap)
	}
	if len(snap.IPs) != 2 {
		t.Errorf("IPs = %v", snap.IPs)
	}
	stopped, err := parseTailscaleStatus([]byte(`{"BackendState":"Stopped","Self":{}}`))
	if err != nil || stopped.Running {
		t.Errorf("stopped status = %+v, %v", stopped, err)
	}
	if _, err := parseTailscaleStatus([]byte(`not json`)); err == nil {
		t.Error("invalid JSON was accepted")
	}

	if got := strings.Join(tailscaleToggleArgs(true), " "); got != "up" {
		t.Errorf("connect args = %q", got)
	}
	if got := strings.Join(tailscaleToggleArgs(false), " "); got != "down" {
		t.Errorf("disconnect args = %q", got)
	}
}

func TestWireGuardInterfaces(t *testing.T) {
	got := parseWireGuardInterfaces("wg0 wg1 wg0\n")
	if len(got) != 2 || got[0] != "wg0" || got[1] != "wg1" {
		t.Errorf("interfaces = %v", got)
	}
	if len(parseWireGuardInterfaces("  \n")) != 0 {
		t.Error("empty output produced interfaces")
	}
	if line := wireguardLine(wireguardSnapshot{Installed: true}); line != "no tunnel up" {
		t.Errorf("empty line = %q", line)
	}
}

func TestRouterRows(t *testing.T) {
	t.Setenv("JUKAHUB_GATEWAY", "192.168.1.1")
	if got := defaultGateway(); got != "192.168.1.1" {
		t.Errorf("gateway override = %q", got)
	}
	if line := routerLine(routerSnapshot{Host: "10.0.0.1", Reachable: true, LatencyMS: 3}); !strings.Contains(line, "3 ms") {
		t.Errorf("router line = %q", line)
	}
	if line := routerLine(routerSnapshot{Host: "10.0.0.1", Err: "unreachable"}); !strings.Contains(line, "unreachable") {
		t.Errorf("unreachable line = %q", line)
	}
}

func TestSmartHomeSettings(t *testing.T) {
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{
		"ha_url":           " http://ha.local:8123 ",
		"ha_token":         "token",
		"ha_insecure":      "true",
		"mqtt_host":        "broker.lan",
		"mqtt_port":        "1884",
		"pihole_url":       "http://pi.lan",
		"tailscale_toggle": true,
	}}}
	s := loadSmartHomeSettings(cfg)
	if s.HAURL != "http://ha.local:8123" || s.HAToken != "token" {
		t.Errorf("Home Assistant settings = %+v", s)
	}
	if s.HAVerify {
		t.Error("ha_insecure did not disable verification")
	}
	if s.MQTTPort != 1884 || !s.hasMQTT() {
		t.Errorf("MQTT settings = %+v", s)
	}
	if !s.hasPiHole() || !s.TSEnabled {
		t.Errorf("Pi-hole/Tailscale settings = %+v", s)
	}
	if s.MQTTPFX != "jukahub" {
		t.Errorf("default topic prefix = %q", s.MQTTPFX)
	}
	if got := s.mqttAddr(); got != "broker.lan:1884" {
		t.Errorf("broker address = %q", got)
	}

	// Defaults for a config that has never heard of this feature.
	empty := loadSmartHomeSettings(&Config{Variables: Variables{Custom: map[string]interface{}{}}})
	if empty.HAVerify != true || empty.MQTTPort != 1883 || empty.hasHA() || empty.hasPiHole() {
		t.Errorf("defaults = %+v", empty)
	}
	if got := loadSmartHomeSettings(nil); got.MQTTPort != 1883 {
		t.Errorf("nil config defaults = %+v", got)
	}

	// An encrypted token is never used as ciphertext. Without the key it cannot
	// be decrypted, so the client must see "no token" and say so, not send
	// "ENC:…" as a bearer token.
	enc := &Config{Variables: Variables{Custom: map[string]interface{}{
		"ha_url":   "http://ha.local:8123",
		"ha_token": "ENC:default-v3-enhanced/0011223344",
	}}}
	if got := loadSmartHomeSettings(enc); got.HAToken != "" {
		t.Errorf("undecryptable token was used raw: %q", got.HAToken)
	}
	if !smartHomeConfigured(s) || smartHomeConfigured(empty) {
		t.Error("configured() did not follow the settings")
	}
}

func TestBoolishValue(t *testing.T) {
	c := map[string]interface{}{
		"a": true, "b": "TRUE", "c": false, "d": "no", "e": float64(1), "f": "yes",
		"MixedCase": "on",
	}
	for key, want := range map[string]bool{"a": true, "b": true, "c": false, "d": false, "e": true, "f": true, "mixedcase": true, "missing": false} {
		if got := boolishValue(c, key); got != want {
			t.Errorf("boolishValue(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestSmartHomeSnapshotIsACopy(t *testing.T) {
	prev := smartHomeSnapshot()
	t.Cleanup(func() {
		smartHome.mu.Lock()
		smartHome.smartHomeSnapshotData = prev
		smartHome.mu.Unlock()
	})

	smartHome.mu.Lock()
	smartHome.entities = []haEntity{{EntityID: "light.kitchen", Name: "Kitchen", Domain: "light", State: "on"}}
	smartHome.pihole = piholeSnapshot{Reachable: true, Queries: 5}
	smartHome.mu.Unlock()

	snap := smartHomeSnapshot()
	snap.entities[0].State = "off"
	if got := smartHomeSnapshot().entities[0].State; got != "on" {
		t.Errorf("mutating a snapshot changed the store: %q", got)
	}
}

// ── live clients against local fakes ─────────────────────────────────────
//
// The parsers above prove the JSON handling; these run the real request code
// against a server on loopback, so the auth header, the service path and the
// MQTT handshake are exercised the way a house would exercise them.

func TestFetchHAEntitiesAndToggle(t *testing.T) {
	var gotAuth string
	var toggleBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/states":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[
			  {"entity_id":"light.kitchen","state":"on","attributes":{"friendly_name":"Kitchen"}},
			  {"entity_id":"sensor.temp","state":"21","attributes":{}}
			]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/services/light/toggle":
			json.NewDecoder(r.Body).Decode(&toggleBody)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	settings := smartHomeSettings{HAURL: srv.URL, HAToken: "secret-token", HAVerify: true}
	entities, err := fetchHAEntities(settings)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(entities) != 1 || entities[0].EntityID != "light.kitchen" {
		t.Fatalf("entities = %+v", entities)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization header = %q", gotAuth)
	}

	if err := callHAService(settings, "light.kitchen"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if toggleBody["entity_id"] != "light.kitchen" {
		t.Errorf("toggle body = %v", toggleBody)
	}

	// A refused token must surface as an error, not as an empty house.
	bad := settings
	bad.HAToken = "wrong"
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv2.Close()
	bad.HAURL = srv2.URL
	if _, err := fetchHAEntities(bad); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("unauthorised fetch error = %v", err)
	}
}

func TestFetchPiholeV6Session(t *testing.T) {
	var sidHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["password"] != "hunter2" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"session":{"sid":"session-1","csrf":"c"}}`)
		case "/api/stats/summary":
			sidHeader = r.Header.Get("X-FTL-SID")
			io.WriteString(w, `{"queries":{"total":500,"blocked":50,"percent_blocked":10},"blocking":"enabled"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	snap, err := fetchPihole(smartHomeSettings{PiURL: srv.URL, PiToken: "hunter2"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if snap.Queries != 500 || snap.Blocked != 50 || snap.Status != "enabled" {
		t.Errorf("snapshot = %+v", snap)
	}
	if sidHeader != "session-1" {
		t.Errorf("session header = %q", sidHeader)
	}

	// Wrong password: the error has to name the credential, not the network.
	if _, err := fetchPihole(smartHomeSettings{PiURL: srv.URL, PiToken: "wrong"}); err == nil {
		t.Error("wrong Pi-hole password was accepted")
	}
}

func TestPiholeFallsBackToV5(t *testing.T) {
	// A v5 instance has no /api/stats/summary; it must still produce numbers.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/admin/api.php":
			if r.URL.Query().Get("auth") != "token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"dns_queries_today":80,"ads_blocked_today":8,"ads_percentage_today":10,"status":"enabled"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	snap, err := fetchPihole(smartHomeSettings{PiURL: srv.URL, PiToken: "token"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if snap.Queries != 80 || snap.Percent != 10 {
		t.Errorf("v5 snapshot = %+v", snap)
	}
}

// fakeMQTTBroker accepts one connection, answers CONNACK, and reports the first
// PUBLISH packet it receives.
type fakeBroker struct {
	addr    string
	publish chan string
	connect chan []byte
}

func startFakeBroker(t *testing.T) *fakeBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	b := &fakeBroker{addr: ln.Addr().String(), publish: make(chan string, 1), connect: make(chan []byte, 1)}
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		header := make([]byte, 2)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		rest := make([]byte, int(header[1]))
		if _, err := io.ReadFull(conn, rest); err != nil {
			return
		}
		b.connect <- append(header, rest...)
		conn.Write([]byte{0x20, 0x02, 0x00, 0x00})

		head := make([]byte, 2)
		if _, err := io.ReadFull(conn, head); err != nil {
			return
		}
		body := make([]byte, int(head[1]))
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		// Topic length is the first two bytes of the PUBLISH body.
		tl := int(body[0])<<8 | int(body[1])
		if len(body) < 2+tl {
			return
		}
		b.publish <- string(body[2:2+tl]) + "=" + string(body[2+tl:])
	}()
	return b
}

func TestMQTTPublishToBroker(t *testing.T) {
	broker := startFakeBroker(t)
	host, port, err := net.SplitHostPort(broker.addr)
	if err != nil {
		t.Fatal(err)
	}
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	settings := smartHomeSettings{MQTTHost: host, MQTTPort: p}
	if err := mqttPublishTo(settings, mqttTopicFor("", "light.kitchen"), "ON"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case got := <-broker.publish:
		if got != "jukahub/light.kitchen=ON" {
			t.Errorf("broker received %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no PUBLISH reached the broker")
	}
	select {
	case connect := <-broker.connect:
		if !bytes.Contains(connect, []byte("jukahub-")) {
			t.Errorf("CONNECT did not carry a client id: %q", connect)
		}
	case <-time.After(time.Second):
		t.Fatal("no CONNECT reached the broker")
	}

	// Nothing listening: the error must say so instead of hanging.
	dead := smartHomeSettings{MQTTHost: "127.0.0.1", MQTTPort: 1}
	if err := mqttPublishTo(dead, "t", "x"); err == nil {
		t.Error("publishing to a closed port succeeded")
	}
}

func TestProbeRouterAgainstListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	snap := probeRouter(ln.Addr().String())
	if !snap.Reachable || snap.Host == "" {
		t.Fatalf("probe = %+v", snap)
	}
	if !strings.Contains(routerLine(snap), "ms") {
		t.Errorf("line = %q", routerLine(snap))
	}
	if dead := probeRouter("127.0.0.1:1"); dead.Reachable {
		t.Errorf("closed port probed as reachable: %+v", dead)
	}
}

// The whole flow a user performs: open the panel (one refresh), then press A on
// a light. This runs the real goroutine + mutex path with a fake Home Assistant
// and a fake broker, so a deadlock or a lost mirror shows up here.
func TestRefreshAndToggleEndToEnd(t *testing.T) {
	t.Setenv("JUKAHUB_GATEWAY", "127.0.0.1:1") // keep the router probe off the LAN

	var toggled []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/states":
			io.WriteString(w, `[{"entity_id":"light.kitchen","state":"off","attributes":{"friendly_name":"Kitchen"}}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/services/light/toggle":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			toggled = append(toggled, body["entity_id"])
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	broker := startFakeBroker(t)
	host, portStr, err := net.SplitHostPort(broker.addr)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{
		"ha_url":    srv.URL,
		"ha_token":  "token",
		"mqtt_host": host,
		"mqtt_port": portStr,
	}}}

	prev := smartHomeSnapshot()
	t.Cleanup(func() {
		smartHome.mu.Lock()
		smartHome.smartHomeSnapshotData = prev
		smartHome.mu.Unlock()
		invalidateSmartHomeSettings()
	})
	invalidateSmartHomeSettings()

	refreshSmartHome(cfg)
	deadline := time.Now().Add(3 * time.Second)
	for {
		snap := smartHomeSnapshot()
		if snap.loaded && len(snap.entities) == 1 {
			if snap.entities[0].State != "off" {
				t.Fatalf("entity state = %q", snap.entities[0].State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("refresh did not populate the state: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := toggleSmartHomeEntity(cfg, "light.kitchen"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if len(toggled) != 1 || toggled[0] != "light.kitchen" {
		t.Fatalf("Home Assistant saw %v", toggled)
	}
	select {
	case got := <-broker.publish:
		if got != "jukahub/light.kitchen=ON" {
			t.Errorf("mirror topic/payload = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the toggle was not mirrored to MQTT")
	}
	if state := smartHomeSnapshot().entities[0].State; state != "on" {
		t.Errorf("optimistic state = %q, want on", state)
	}
}
