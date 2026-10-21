package main

// smart_home.go — the self-hosting half of the app: Home Assistant, MQTT,
// Pi-hole, the router on the LAN, Tailscale and WireGuard.
//
// Everything here is a client, not a service: JukaHub never listens on a port of
// its own for this. Each integration is optional and configured through custom
// config keys, so a config without them costs nothing at boot — the panel is
// simply empty and says what is missing.
//
// Two rules the code holds to:
//
//   - Nothing blocks the render loop. Every probe runs on its own goroutine and
//     publishes into smartHomeState under a mutex; the panel reads a snapshot.
//   - Nothing pretends. A row whose binary, URL or token is missing renders
//     dimmed with the reason, and a failed request keeps the last good numbers
//     with their timestamp instead of painting zeros.
//
// Keys (all custom, so they live in jukauser.json and never in the shipped UI):
//
//	ha_url, ha_token, ha_insecure   Home Assistant base URL, long-lived token,
//	                                and whether to accept a self-signed cert
//	mqtt_host, mqtt_port, mqtt_user, mqtt_pass, mqtt_prefix
//	                                broker and the topic prefix used to mirror
//	                                Home Assistant toggles
//	pihole_url, pihole_token        Pi-hole web root and API token/password
//	router_host                     override for the router probe (default: the
//	                                default gateway)
//	tailscale_toggle                a Toggle row that runs `tailscale up/down`

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// smartHomeTimeout bounds every request and command probe. A handheld on hotel
// Wi-Fi must not hold a goroutine for the 30 s the download client allows.
const smartHomeTimeout = 6 * time.Second

// smartHomeHTTP is separate from httpClientLong so a slow Home Assistant cannot
// delay an update download or a YouTube lookup.
var smartHomeHTTP = &http.Client{
	Timeout: smartHomeTimeout,
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 4 * time.Second}).DialContext,
		TLSHandshakeTimeout: 4 * time.Second,
	},
}

// smartHomeInsecureHTTP is the same client with certificate verification off,
// used only when ha_insecure is set (Home Assistant on a LAN with a self-signed
// certificate is the common case, and refusing to connect is worse than an
// explicit opt-in).
var smartHomeInsecureHTTP = &http.Client{
	Timeout: smartHomeTimeout,
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 4 * time.Second}).DialContext,
		TLSHandshakeTimeout: 4 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // explicit opt-in
	},
}

// ──────────────────────────────────────────────────────────────────────
// Settings
// ──────────────────────────────────────────────────────────────────────

type smartHomeSettings struct {
	HAURL     string
	HAToken   string
	HAVerify  bool // false when ha_insecure is on
	MQTTHost  string
	MQTTPort  int
	MQTTUser  string
	MQTTPass  string
	MQTTPFX   string
	PiURL     string
	PiToken   string
	Router    string
	TSEnabled bool
}

// loadSmartHomeSettings reads the custom keys, with defaults that are safe on a
// config that has never heard of this feature.
func loadSmartHomeSettings(config *Config) smartHomeSettings {
	s := smartHomeSettings{
		MQTTPort: 1883,
		HAVerify: true,
	}
	if config == nil {
		return s
	}
	c := config.Variables.Custom
	str := func(key string) string {
		if v, ok := customString(c, key); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	// Tokens go through decryptCustomString so a value written with
	// `JukaHub -set-secret ha_token` works: it is stored as ENC:… in
	// jukauser.json and never has to exist in plaintext.
	secret := func(key string) string {
		if _, ok := customString(c, key); !ok {
			return ""
		}
		return strings.TrimSpace(decryptCustomString(config, key))
	}
	s.HAURL = str("ha_url")
	s.HAToken = secret("ha_token")
	s.HAVerify = !boolishValue(c, "ha_insecure")
	s.MQTTHost = str("mqtt_host")
	s.MQTTUser = str("mqtt_user")
	s.MQTTPass = secret("mqtt_pass")
	s.MQTTPFX = str("mqtt_prefix")
	s.PiURL = str("pihole_url")
	s.PiToken = secret("pihole_token")
	s.Router = str("router_host")
	s.TSEnabled = boolishValue(c, "tailscale_toggle")
	if raw := str("mqtt_port"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < 65536 {
			s.MQTTPort = n
		}
	}
	if s.MQTTPFX == "" {
		s.MQTTPFX = "jukahub"
	}
	return s
}

// boolishValue reads a toggle stored as a string, a real bool, or a number, so a
// hand-edited jukauser.json behaves the same as one the app wrote.
func boolishValue(c map[string]interface{}, key string) bool {
	if c == nil {
		return false
	}
	v, ok := c[key]
	if !ok {
		for k, candidate := range c {
			if strings.EqualFold(k, key) {
				v, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "yes", "on":
			return true
		}
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return false
}

// hasHA / hasPiHole / hasMQTT answer "should this integration be probed?".
func (s smartHomeSettings) hasHA() bool     { return s.HAURL != "" }
func (s smartHomeSettings) hasPiHole() bool { return s.PiURL != "" }
func (s smartHomeSettings) hasMQTT() bool   { return s.MQTTHost != "" }

// mqttAddr is host:port for the broker, defaulting the port.
func (s smartHomeSettings) mqttAddr() string {
	port := s.MQTTPort
	if port == 0 {
		port = 1883
	}
	return net.JoinHostPort(s.MQTTHost, strconv.Itoa(port))
}

// ──────────────────────────────────────────────────────────────────────
// URL helpers
// ──────────────────────────────────────────────────────────────────────

// smartHomeEndpoint normalises a URL typed into a settings field: a bare host
// becomes http://, a trailing slash disappears, and anything that is not HTTP is
// refused with a message a person can act on.
func smartHomeEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("no URL configured")
	}
	if strings.ContainsAny(raw, " \t\n") {
		return "", fmt.Errorf("URL contains a space")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("URL is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("URL must start with http:// or https://")
	}
	if u.Host == "" {
		return "", fmt.Errorf("URL has no host")
	}
	return strings.TrimRight(raw, "/"), nil
}

// haStatesURL is GET /api/states on the configured base.
func haStatesURL(base string) (string, error) {
	b, err := smartHomeEndpoint(base)
	if err != nil {
		return "", err
	}
	return b + "/api/states", nil
}

// haServiceURL is POST /api/services/<domain>/toggle.
func haServiceURL(base, domain string) (string, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" || strings.ContainsAny(domain, "/?#") {
		return "", fmt.Errorf("invalid domain %q", domain)
	}
	b, err := smartHomeEndpoint(base)
	if err != nil {
		return "", err
	}
	return b + "/api/services/" + domain + "/toggle", nil
}

// piholeV5URL is the legacy summary endpoint. The auth token is a query
// parameter there, which is why it is only ever built here and never logged.
func piholeV5URL(base, token string) (string, error) {
	b, err := smartHomeEndpoint(base)
	if err != nil {
		return "", err
	}
	u := b + "/admin/api.php?summaryRaw"
	if token != "" {
		u += "&auth=" + url.QueryEscape(token)
	}
	return u, nil
}

// piholeV6URL is the REST summary endpoint; the session id travels in a header.
func piholeV6URL(base string) (string, error) {
	b, err := smartHomeEndpoint(base)
	if err != nil {
		return "", err
	}
	return b + "/api/stats/summary", nil
}

// piholeAuthURL is the v6 password -> session exchange.
func piholeAuthURL(base string) (string, error) {
	b, err := smartHomeEndpoint(base)
	if err != nil {
		return "", err
	}
	return b + "/api/auth", nil
}

// ──────────────────────────────────────────────────────────────────────
// Home Assistant
// ──────────────────────────────────────────────────────────────────────

type haEntity struct {
	EntityID string
	Name     string
	Domain   string
	State    string
}

// haControlDomains are the domains a single "toggle" service is meaningful for.
// Sensors and binary_sensors are deliberately absent: the panel is a control
// surface, not a dashboard.
var haControlDomains = map[string]bool{
	"light":         true,
	"switch":        true,
	"fan":           true,
	"input_boolean": true,
	"cover":         true,
}

type haStateJSON struct {
	EntityID   string `json:"entity_id"`
	State      string `json:"state"`
	Attributes struct {
		FriendlyName string `json:"friendly_name"`
	} `json:"attributes"`
}

// parseHAStates turns GET /api/states into the controllable entities, sorted by
// name so the row order is stable between refreshes (a list that reshuffles on
// every poll makes a controller-driven panel unusable).
func parseHAStates(data []byte) ([]haEntity, error) {
	var states []haStateJSON
	if err := json.Unmarshal(data, &states); err != nil {
		return nil, fmt.Errorf("Home Assistant returned something that is not a state list")
	}
	out := make([]haEntity, 0, len(states))
	for _, st := range states {
		id := strings.TrimSpace(st.EntityID)
		dot := strings.IndexByte(id, '.')
		if dot <= 0 || dot == len(id)-1 {
			continue
		}
		domain := id[:dot]
		if !haControlDomains[domain] {
			continue
		}
		name := strings.TrimSpace(st.Attributes.FriendlyName)
		if name == "" {
			name = id[dot+1:]
			name = strings.ReplaceAll(name, "_", " ")
		}
		out = append(out, haEntity{
			EntityID: id,
			Name:     name,
			Domain:   domain,
			State:    strings.TrimSpace(st.State),
		})
	}
	sortHAEntities(out)
	return out, nil
}

// sortHAEntities orders by name, then id, so equal names never swap places.
// The comparison is case-insensitive: a friendly name that starts lowercase
// should not sort after every capitalised one.
func sortHAEntities(list []haEntity) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			a, b := list[j-1], list[j]
			an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
			if an < bn || (an == bn && a.EntityID <= b.EntityID) {
				break
			}
			list[j-1], list[j] = b, a
		}
	}
}

// haEntityOn reports whether the state reads as "on". Covers report open/closed.
func haEntityOn(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "on", "open", "opening":
		return true
	}
	return false
}

// haEntityDomain splits an entity id, answering "" for anything malformed.
func haEntityDomain(entityID string) string {
	dot := strings.IndexByte(entityID, '.')
	if dot <= 0 || dot == len(entityID)-1 {
		return ""
	}
	return entityID[:dot]
}

// fetchHAEntities reads the entity list. The token is required: an instance
// without one answers 401 and explaining that is more useful than pretending the
// house has no lights.
func fetchHAEntities(s smartHomeSettings) ([]haEntity, error) {
	if !s.hasHA() {
		return nil, fmt.Errorf("set ha_url and ha_token")
	}
	if s.HAToken == "" {
		return nil, fmt.Errorf("set ha_token")
	}
	endpoint, err := haStatesURL(s.HAURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.HAToken)
	req.Header.Set("Accept", "application/json")
	client := smartHomeHTTP
	if !s.HAVerify {
		client = smartHomeInsecureHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Home Assistant unreachable")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("Home Assistant rejected the token")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Home Assistant answered %d", resp.StatusCode)
	}
	return parseHAStates(body)
}

// callHAService posts a toggle for one entity. It returns nil only when Home
// Assistant accepted the request, so the panel can leave the switch where it was
// on failure instead of lying for a frame.
func callHAService(s smartHomeSettings, entityID string) error {
	domain := haEntityDomain(entityID)
	if domain == "" {
		return fmt.Errorf("invalid entity %q", entityID)
	}
	endpoint, err := haServiceURL(s.HAURL, domain)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"entity_id": entityID})
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.HAToken)
	req.Header.Set("Content-Type", "application/json")
	client := smartHomeHTTP
	if !s.HAVerify {
		client = smartHomeInsecureHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Home Assistant unreachable")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("Home Assistant rejected the token")
	}
	return fmt.Errorf("Home Assistant answered %d", resp.StatusCode)
}

// ──────────────────────────────────────────────────────────────────────
// MQTT (publish only)
// ──────────────────────────────────────────────────────────────────────
//
// A full MQTT client is a subscription loop, keepalives and reconnect state; the
// panel does not need any of that. Toggling a Home Assistant entity mirrors the
// new state to <prefix>/<entity_id>, which is enough for dashboards, Node-RED
// flows and zigbee2mqtt-style bridges to follow along. Packets are built by the
// functions below so the encoding is unit-testable without a broker.

// mqttTopicFor is the topic a toggle is mirrored to.
func mqttTopicFor(prefix, entityID string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		prefix = "jukahub"
	}
	return prefix + "/" + entityID
}

// mqttEncodingLength is the remaining-length varint used by every MQTT 3.1.1
// control packet.
func mqttEncodingLength(n int) []byte {
	var out []byte
	for {
		digit := byte(n % 128)
		n /= 128
		if n > 0 {
			digit |= 0x80
		}
		out = append(out, digit)
		if n == 0 {
			return out
		}
	}
}

// mqttString is the length-prefixed UTF-8 string MQTT uses everywhere.
func mqttString(s string) []byte {
	buf := make([]byte, 2+len(s))
	binary.BigEndian.PutUint16(buf, uint16(len(s)))
	copy(buf[2:], s)
	return buf
}

// mqttConnectPacket builds CONNECT with a clean session and a 30 s keepalive.
// Username/password are included only when a username is set, which is the rule
// brokers expect.
func mqttConnectPacket(clientID, user, pass string) []byte {
	var payload []byte
	payload = append(payload, mqttString(clientID)...)
	if user != "" {
		payload = append(payload, mqttString(user)...)
		payload = append(payload, mqttString(pass)...)
	}

	var header []byte
	header = append(header, mqttString("MQTT")...)
	header = append(header, 4) // protocol level 3.1.1
	flags := byte(0x02)        // clean session
	if user != "" {
		flags |= 0x80 | 0x40 // username + password
	}
	header = append(header, flags)
	header = append(header, 0, 30) // keepalive

	body := append(header, payload...)
	pkt := []byte{0x10} // CONNECT
	pkt = append(pkt, mqttEncodingLength(len(body))...)
	return append(pkt, body...)
}

// mqttPublishPacket builds a QoS 0 PUBLISH. QoS 0 has no packet id and no
// acknowledgement, so this stays fire-and-forget: the panel reports a failure
// only when the TCP write or the CONNECT fails.
func mqttPublishPacket(topic, payload string, retain bool) []byte {
	var body []byte
	body = append(body, mqttString(topic)...)
	body = append(body, []byte(payload)...)
	header := byte(0x30) // PUBLISH, QoS 0
	if retain {
		header |= 0x01
	}
	pkt := []byte{header}
	pkt = append(pkt, mqttEncodingLength(len(body))...)
	return append(pkt, body...)
}

// mqttPublishTo connects, publishes one message and disconnects. The connection
// is not pooled: a toggle happens a few times an hour, and a stale socket to a
// broker that restarted is worse than a new TCP handshake.
func mqttPublishTo(s smartHomeSettings, topic, payload string) error {
	if !s.hasMQTT() {
		return fmt.Errorf("set mqtt_host")
	}
	if strings.TrimSpace(topic) == "" {
		return fmt.Errorf("empty MQTT topic")
	}
	conn, err := net.DialTimeout("tcp", s.mqttAddr(), smartHomeTimeout)
	if err != nil {
		return fmt.Errorf("MQTT broker unreachable")
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(smartHomeTimeout))

	clientID := "jukahub-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	if _, err := conn.Write(mqttConnectPacket(clientID, s.MQTTUser, s.MQTTPass)); err != nil {
		return fmt.Errorf("MQTT connect failed")
	}
	// CONNACK: fixed header 0x20, remaining length 2, then flags and return code.
	ack := make([]byte, 4)
	if _, err := io.ReadFull(conn, ack); err != nil {
		return fmt.Errorf("MQTT broker did not answer")
	}
	if ack[0] != 0x20 {
		return fmt.Errorf("MQTT broker sent an unexpected packet")
	}
	if ack[3] != 0 {
		return fmt.Errorf("MQTT broker refused the connection (%s)", mqttConnackReason(ack[3]))
	}
	if _, err := conn.Write(mqttPublishPacket(topic, payload, false)); err != nil {
		return fmt.Errorf("MQTT publish failed")
	}
	conn.Write([]byte{0xE0, 0x00}) // DISCONNECT
	return nil
}

// mqttConnackReason names the refusal codes so a row can say "bad password"
// instead of "refused (5)".
func mqttConnackReason(code byte) string {
	switch code {
	case 1:
		return "bad protocol version"
	case 2:
		return "client id rejected"
	case 3:
		return "server unavailable"
	case 4:
		return "bad username or password"
	case 5:
		return "not authorised"
	}
	return "code " + strconv.Itoa(int(code))
}

// ──────────────────────────────────────────────────────────────────────
// Pi-hole
// ──────────────────────────────────────────────────────────────────────

type piholeSnapshot struct {
	Configured bool
	Reachable  bool
	Status     string // "enabled", "disabled" or "" when unknown
	Queries    int
	Blocked    int
	Percent    float64
	Err        string
}

type piholeV5JSON struct {
	Queries     int     `json:"dns_queries_today"`
	Blocked     int     `json:"ads_blocked_today"`
	Percent     float64 `json:"ads_percentage_today"`
	Status      string  `json:"status"`
	GravitySize int     `json:"domains_being_blocked"`
}

type piholeV6JSON struct {
	Queries struct {
		Total   int     `json:"total"`
		Blocked int     `json:"blocked"`
		Percent float64 `json:"percent_blocked"`
	} `json:"queries"`
	Gravity struct {
		Domains int `json:"domains_being_blocked"`
	} `json:"gravity"`
	Blocking interface{} `json:"blocking"`
}

// piholeBlockingWord normalises the blocking state across the two API
// generations, where the same idea is a bool in one place and a string in
// another.
func piholeBlockingWord(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case bool:
		if t {
			return "enabled"
		}
		return "disabled"
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		switch s {
		case "enabled", "enabled_blocking", "true":
			return "enabled"
		case "disabled", "false":
			return "disabled"
		}
		return s
	case float64:
		if t != 0 {
			return "enabled"
		}
		return "disabled"
	}
	return ""
}

// parsePiholeV5 reads the legacy summaryRaw response.
func parsePiholeV5(data []byte) (piholeSnapshot, error) {
	var v5 piholeV5JSON
	if err := json.Unmarshal(data, &v5); err != nil {
		return piholeSnapshot{}, fmt.Errorf("Pi-hole returned an unreadable summary")
	}
	status := strings.ToLower(strings.TrimSpace(v5.Status))
	switch status {
	case "enabled", "disabled":
	default:
		status = ""
	}
	return piholeSnapshot{
		Configured: true,
		Reachable:  true,
		Status:     status,
		Queries:    v5.Queries,
		Blocked:    v5.Blocked,
		Percent:    v5.Percent,
	}, nil
}

// parsePiholeV6 reads the REST summary response.
func parsePiholeV6(data []byte) (piholeSnapshot, error) {
	var v6 piholeV6JSON
	if err := json.Unmarshal(data, &v6); err != nil {
		return piholeSnapshot{}, fmt.Errorf("Pi-hole returned an unreadable summary")
	}
	return piholeSnapshot{
		Configured: true,
		Reachable:  true,
		Status:     piholeBlockingWord(v6.Blocking),
		Queries:    v6.Queries.Total,
		Blocked:    v6.Queries.Blocked,
		Percent:    v6.Queries.Percent,
	}, nil
}

// fetchPihole probes v6 first and falls back to v5. A v6 instance answers
// /admin/api.php with a redirect to the login page instead of JSON, so the
// fallback is chosen by a failed parse, not only by a 404.
func fetchPihole(s smartHomeSettings) (piholeSnapshot, error) {
	if !s.hasPiHole() {
		return piholeSnapshot{}, fmt.Errorf("set pihole_url")
	}
	if snap, err := piholeV6Summary(s); err == nil {
		return snap, nil
	}
	return piholeV5Summary(s)
}

// piholeV6Summary authenticates (when a password is set) and reads the summary.
func piholeV6Summary(s smartHomeSettings) (piholeSnapshot, error) {
	summaryURL, err := piholeV6URL(s.PiURL)
	if err != nil {
		return piholeSnapshot{}, err
	}
	header := http.Header{}
	if s.PiToken != "" {
		authURL, err := piholeAuthURL(s.PiURL)
		if err != nil {
			return piholeSnapshot{}, err
		}
		body, _ := json.Marshal(map[string]string{"password": s.PiToken})
		resp, err := smartHomeHTTP.Post(authURL, "application/json", bytes.NewReader(body))
		if err != nil {
			return piholeSnapshot{}, fmt.Errorf("Pi-hole unreachable")
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return piholeSnapshot{}, fmt.Errorf("Pi-hole rejected the password")
		}
		var session struct {
			Session struct {
				SID string `json:"sid"`
			} `json:"session"`
		}
		if err := json.Unmarshal(raw, &session); err != nil || session.Session.SID == "" {
			return piholeSnapshot{}, fmt.Errorf("Pi-hole did not return a session")
		}
		header.Set("X-FTL-SID", session.Session.SID)
	}
	req, err := http.NewRequest(http.MethodGet, summaryURL, nil)
	if err != nil {
		return piholeSnapshot{}, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := smartHomeHTTP.Do(req)
	if err != nil {
		return piholeSnapshot{}, fmt.Errorf("Pi-hole unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return piholeSnapshot{}, fmt.Errorf("Pi-hole answered %d", resp.StatusCode)
	}
	return parsePiholeV6(raw)
}

// piholeV5Summary reads the legacy endpoint, which carries the token in the URL.
func piholeV5Summary(s smartHomeSettings) (piholeSnapshot, error) {
	endpoint, err := piholeV5URL(s.PiURL, s.PiToken)
	if err != nil {
		return piholeSnapshot{}, err
	}
	resp, err := smartHomeHTTP.Get(endpoint)
	if err != nil {
		return piholeSnapshot{}, fmt.Errorf("Pi-hole unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return piholeSnapshot{}, fmt.Errorf("Pi-hole rejected the token")
	}
	if resp.StatusCode != http.StatusOK {
		return piholeSnapshot{}, fmt.Errorf("Pi-hole answered %d", resp.StatusCode)
	}
	return parsePiholeV5(raw)
}

// piholeLine is the one-line summary the panel shows.
func piholeLine(snap piholeSnapshot) string {
	if !snap.Reachable {
		if snap.Err != "" {
			return snap.Err
		}
		return "not configured"
	}
	status := snap.Status
	if status == "" {
		status = "blocking"
	}
	return fmt.Sprintf("%s · %d queries · %d blocked (%.1f%%)", status, snap.Queries, snap.Blocked, snap.Percent)
}

// ──────────────────────────────────────────────────────────────────────
// Tailscale and WireGuard
// ──────────────────────────────────────────────────────────────────────

type tailscaleSnapshot struct {
	Installed bool
	Running   bool
	State     string
	IPs       []string
	Err       string
}

type tailscaleStatusJSON struct {
	BackendState string
	Self         struct {
		HostName     string
		TailscaleIPs []string
	}
}

// parseTailscaleStatus reads `tailscale status --json`.
func parseTailscaleStatus(data []byte) (tailscaleSnapshot, error) {
	var st tailscaleStatusJSON
	if err := json.Unmarshal(data, &st); err != nil {
		return tailscaleSnapshot{}, fmt.Errorf("tailscale status is unreadable")
	}
	return tailscaleSnapshot{
		Installed: true,
		Running:   strings.EqualFold(st.BackendState, "Running"),
		State:     st.BackendState,
		IPs:       st.Self.TailscaleIPs,
	}, nil
}

// tailscaleToggleArgs is the command for the requested state. `up` with no
// flags reuses the stored prefs, so a node that was logged in comes back the way
// it was; `down` disconnects without logging out.
func tailscaleToggleArgs(running bool) []string {
	if running {
		return []string{"up"}
	}
	return []string{"down"}
}

// probeTailscale runs the CLI. A missing binary is "not installed", not an
// error to show in red.
func probeTailscale() tailscaleSnapshot {
	path, err := exec.LookPath("tailscale")
	if err != nil {
		return tailscaleSnapshot{}
	}
	out, err := runCommandTimeout(smartHomeTimeout, path, "status", "--json")
	if err != nil {
		return tailscaleSnapshot{Installed: true, Err: "status failed"}
	}
	snap, err := parseTailscaleStatus(out)
	if err != nil {
		return tailscaleSnapshot{Installed: true, Err: err.Error()}
	}
	return snap
}

// setTailscale connects or disconnects, returning the CLI's own message on
// failure so the panel can show why (usually "access denied").
func setTailscale(running bool) error {
	path, err := exec.LookPath("tailscale")
	if err != nil {
		return fmt.Errorf("tailscale is not installed")
	}
	out, err := runCommandTimeout(15*time.Second, path, tailscaleToggleArgs(running)...)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if i := strings.IndexByte(msg, '\n'); i > 0 {
			msg = msg[:i]
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

type wireguardSnapshot struct {
	Installed  bool
	Interfaces []string
	Err        string
}

// parseWireGuardInterfaces reads `wg show interfaces`, which prints a
// space-separated list on one line.
func parseWireGuardInterfaces(out string) []string {
	fields := strings.Fields(out)
	seen := make(map[string]bool, len(fields))
	list := make([]string, 0, len(fields))
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		list = append(list, f)
	}
	return list
}

// probeWireGuard lists interfaces. Bringing one up needs root, so the panel
// shows status only and says so rather than offering a switch that would fail.
func probeWireGuard() wireguardSnapshot {
	path, err := exec.LookPath("wg")
	if err != nil {
		return wireguardSnapshot{}
	}
	out, err := runCommandTimeout(smartHomeTimeout, path, "show", "interfaces")
	if err != nil {
		return wireguardSnapshot{Installed: true, Err: "wg failed (needs root?)"}
	}
	return wireguardSnapshot{Installed: true, Interfaces: parseWireGuardInterfaces(string(out))}
}

// wireguardLine is the panel's summary for the WireGuard row.
func wireguardLine(snap wireguardSnapshot) string {
	if !snap.Installed {
		return "wg is not installed"
	}
	if snap.Err != "" {
		return snap.Err
	}
	if len(snap.Interfaces) == 0 {
		return "no tunnel up"
	}
	return strings.Join(snap.Interfaces, ", ")
}

// ──────────────────────────────────────────────────────────────────────
// Router
// ──────────────────────────────────────────────────────────────────────

type routerSnapshot struct {
	Host      string
	Reachable bool
	LatencyMS int
	Err       string
}

// defaultGateway returns the IPv4 default gateway without needing root: the
// proc file on Linux and `route print` on Windows are both readable.
func defaultGateway() string {
	if gw := strings.TrimSpace(os.Getenv("JUKAHUB_GATEWAY")); gw != "" {
		return gw
	}
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		// The gateway is little-endian hex, e.g. 0102A8C0 -> 192.168.2.1.
		v, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil {
			continue
		}
		return fmt.Sprintf("%d.%d.%d.%d", byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}
	return ""
}

// probeRouter measures a TCP handshake to the gateway's web port. It is a
// reachability and latency check, not a login: no credentials, no scraping.
func probeRouter(host string) routerSnapshot {
	host = strings.TrimSpace(host)
	if host == "" {
		host = defaultGateway()
	}
	snap := routerSnapshot{Host: host}
	if host == "" {
		snap.Err = "no gateway found"
		return snap
	}
	target := host
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(host, "80")
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		snap.Err = "unreachable"
		return snap
	}
	snap.LatencyMS = int(time.Since(start).Milliseconds())
	snap.Reachable = true
	conn.Close()
	return snap
}

// routerLine is the panel's summary for the router row.
func routerLine(snap routerSnapshot) string {
	if snap.Host == "" {
		return snap.Err
	}
	if !snap.Reachable {
		return snap.Host + " · " + snap.Err
	}
	return fmt.Sprintf("%s · %d ms", snap.Host, snap.LatencyMS)
}

// ──────────────────────────────────────────────────────────────────────
// Shared state
// ──────────────────────────────────────────────────────────────────────

// smartHomeSnapshotData is everything the panel reads. It is a plain struct so
// it can be copied out of the lock and passed by value; the mutex lives on
// smartHomeState and is never copied.
type smartHomeSnapshotData struct {
	loading   bool
	loaded    bool
	entities  []haEntity
	haErr     string
	pihole    piholeSnapshot
	tailscale tailscaleSnapshot
	wireguard wireguardSnapshot
	router    routerSnapshot
	updatedAt time.Time
}

type smartHomeState struct {
	mu sync.RWMutex
	smartHomeSnapshotData
}

var smartHome smartHomeState

// smartHomeSettingsCache saves re-reading the config on every refresh.
var smartHomeSettingsCache struct {
	mu   sync.Mutex
	conf *Config
	set  smartHomeSettings
}

// currentSmartHomeSettings loads the settings for this config, re-reading only
// when the config pointer changes (a settings edit reloads the config object).
func currentSmartHomeSettings(config *Config) smartHomeSettings {
	smartHomeSettingsCache.mu.Lock()
	defer smartHomeSettingsCache.mu.Unlock()
	if smartHomeSettingsCache.conf != config {
		smartHomeSettingsCache.conf = config
		smartHomeSettingsCache.set = loadSmartHomeSettings(config)
	}
	return smartHomeSettingsCache.set
}

// invalidateSmartHomeSettings drops the cache after a settings edit.
func invalidateSmartHomeSettings() {
	smartHomeSettingsCache.mu.Lock()
	smartHomeSettingsCache.conf = nil
	smartHomeSettingsCache.mu.Unlock()
}

// smartHomeSnapshot is a consistent copy for the renderer, so no frame ever
// reads a half-updated struct.
func smartHomeSnapshot() smartHomeSnapshotData {
	smartHome.mu.RLock()
	defer smartHome.mu.RUnlock()
	cp := smartHomeSnapshotData{
		loading:   smartHome.loading,
		loaded:    smartHome.loaded,
		haErr:     smartHome.haErr,
		pihole:    smartHome.pihole,
		tailscale: smartHome.tailscale,
		wireguard: smartHome.wireguard,
		router:    smartHome.router,
		updatedAt: smartHome.updatedAt,
	}
	cp.entities = append([]haEntity(nil), smartHome.entities...)
	return cp
}

// refreshSmartHome probes every configured integration in parallel and stores
// the result. Concurrent calls collapse into the running one: the panel's
// refresh key can be mashed without spawning a goroutine per press.
func refreshSmartHome(config *Config) {
	smartHome.mu.Lock()
	if smartHome.loading {
		smartHome.mu.Unlock()
		return
	}
	smartHome.loading = true
	smartHome.mu.Unlock()

	settings := currentSmartHomeSettings(config)

	go func() {
		var wg sync.WaitGroup
		wg.Add(4)

		var ha []haEntity
		var haErr error
		go func() {
			defer wg.Done()
			if settings.hasHA() {
				ha, haErr = fetchHAEntities(settings)
			}
		}()

		var pi piholeSnapshot
		var piErr error
		go func() {
			defer wg.Done()
			if settings.hasPiHole() {
				pi, piErr = fetchPihole(settings)
			}
		}()

		var ts tailscaleSnapshot
		go func() {
			defer wg.Done()
			ts = probeTailscale()
		}()

		var wgState wireguardSnapshot
		var router routerSnapshot
		go func() {
			defer wg.Done()
			wgState = probeWireGuard()
			router = probeRouter(settings.Router)
		}()

		wg.Wait()

		if piErr != nil {
			pi.Configured = settings.hasPiHole()
			pi.Err = piErr.Error()
		}

		smartHome.mu.Lock()
		smartHome.loading = false
		smartHome.loaded = true
		smartHome.updatedAt = time.Now()
		smartHome.tailscale = ts
		smartHome.wireguard = wgState
		smartHome.router = router
		smartHome.pihole = pi
		if settings.hasHA() {
			if haErr != nil {
				smartHome.haErr = haErr.Error()
			} else {
				smartHome.haErr = ""
				smartHome.entities = ha
			}
		} else {
			smartHome.haErr = ""
			smartHome.entities = nil
		}
		smartHome.mu.Unlock()
	}()
}

// toggleSmartHomeEntity sends the toggle and mirrors the result to MQTT. The
// local state is updated optimistically only after Home Assistant accepted the
// call, and a refresh follows so the real state wins if a scene overrode it.
func toggleSmartHomeEntity(config *Config, entityID string) error {
	settings := currentSmartHomeSettings(config)
	if err := callHAService(settings, entityID); err != nil {
		return err
	}
	state := ""
	smartHome.mu.Lock()
	for i := range smartHome.entities {
		if smartHome.entities[i].EntityID == entityID {
			if haEntityOn(smartHome.entities[i].State) {
				smartHome.entities[i].State = "off"
			} else {
				smartHome.entities[i].State = "on"
			}
			state = smartHome.entities[i].State
			break
		}
	}
	smartHome.mu.Unlock()

	if settings.hasMQTT() && state != "" {
		payload := strings.ToUpper(state)
		if state == "open" || state == "opening" {
			payload = "OPEN"
		}
		// A broker that is down must not look like a failed toggle: the light
		// did change, the mirror is best effort.
		if err := mqttPublishTo(settings, mqttTopicFor(settings.MQTTPFX, entityID), payload); err != nil {
			logPatch("smart home: MQTT mirror failed (%v)", err)
		}
	}
	go refreshSmartHome(config)
	return nil
}

// toggleTailscale flips the tunnel and refreshes the row.
func toggleTailscale(config *Config) error {
	snap := smartHomeSnapshot()
	if err := setTailscale(!snap.tailscale.Running); err != nil {
		return err
	}
	go refreshSmartHome(config)
	return nil
}

// smartHomeConfigured reports whether any integration has settings, which is how
// the panel decides between "nothing configured" and a row-less dashboard.
func smartHomeConfigured(s smartHomeSettings) bool {
	return s.hasHA() || s.hasPiHole() || s.hasMQTT() || s.TSEnabled
}

// runCommandTimeout runs a command with a hard deadline, returning combined
// output. exec.CommandContext handles the kill, so a hung `tailscale` cannot
// pin the refresh goroutine forever.
func runCommandTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	done := make(chan struct{})
	var out []byte
	var err error
	cmd := exec.Command(name, args...)
	if startErr := cmd.Start(); startErr != nil {
		return nil, startErr
	}
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
		return out, err
	case <-time.After(timeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return nil, fmt.Errorf("timed out")
	}
}
