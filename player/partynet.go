package main

// partynet.go — LAN transport and playback synchronisation for Watch Party.
//
// The host runs a small HTTP server; every device (including the host) talks to
// it the same way. State lives in partyRoom, so a client cannot drift into its
// own idea of the queue or the playhead. Polls are once per second and carry
// the watch-time delta the client measured locally.
//
// The listener is plain HTTP on the LAN, guarded by the five-character room
// code plus a random member ID. It is not meant to be exposed to the internet.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const partyMaxBody = 16 << 10

// partyRequestURLRE is deliberately narrow: the party transport only ever
// speaks http to a host and an /api/party/ endpoint. Keeping the shape here
// makes a malformed invite fail before it can reach the HTTP client.
var partyRequestURLRE = regexp.MustCompile(`^http://(?:[A-Za-z0-9][A-Za-z0-9.-]{0,252}|\[[0-9A-Fa-f:]+\])(?::[0-9]{1,5})?/api/party/[A-Za-z0-9._~/?=&%+-]*$`)
var partyHostnameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,252}$`)

func partyHostAllowed(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}
	if !partyHostnameRE.MatchString(host) {
		return false
	}
	return host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".lan") || !strings.Contains(host, ".")
}

func partySafeRequestURL(raw string) (string, error) {
	if !partyRequestURLRE.MatchString(raw) {
		return "", errors.New("the party address is not a safe local HTTP endpoint")
	}
	u, err := url.Parse(raw)
	if err != nil || !partyHostAllowed(u.Hostname()) {
		return "", errors.New("the party address is not a safe local HTTP endpoint")
	}
	return raw, nil
}

func partyNewRequest(method, raw string, body io.Reader) (*http.Request, error) {
	safe, err := partySafeRequestURL(raw)
	if err != nil {
		return nil, err
	}
	return http.NewRequest(method, safe, body)
}

// ── Server ──────────────────────────────────────────────────────────────────

type partyServer struct {
	room *partyRoom

	mu   sync.Mutex
	srv  *http.Server
	ln   net.Listener
	port int
}

func partyPort() int {
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_PARTY_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return partyDefaultPort
}

func newPartyServer(room *partyRoom) *partyServer {
	return &partyServer{room: room}
}

func (s *partyServer) Start() error {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(partyPort()))
	if err != nil {
		// A second JukaHub, or a stray process, may hold the preferred port.
		// Any free one works because the invite carries the real port.
		ln, err = net.Listen("tcp", ":0")
		if err != nil {
			return err
		}
	}
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	s.mu.Lock()
	s.ln, s.srv = ln, srv
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.mu.Unlock()

	host := localIPv4()
	if host == "" {
		host = "127.0.0.1"
	}
	s.room.SetHostAddr(net.JoinHostPort(host, strconv.Itoa(s.port)))

	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

func (s *partyServer) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

func (s *partyServer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

func (s *partyServer) Stop() {
	s.mu.Lock()
	srv := s.srv
	s.srv, s.ln = nil, nil
	s.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// Invite is what one person reads to another: address plus room code.
func (s *partyServer) Invite() string {
	return strings.TrimSpace(s.room.Snapshot().HostAddr + " " + s.room.Code())
}

func (s *partyServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/party", s.guard(s.handleLanding))
	mux.HandleFunc("/api/party/join", s.guard(s.handleJoin))
	mux.HandleFunc("/api/party/state", s.guard(s.handleState))
	mux.HandleFunc("/api/party/queue", s.guard(s.handleQueue))
	mux.HandleFunc("/api/party/select", s.guard(s.handleSelect))
	mux.HandleFunc("/api/party/remove", s.guard(s.handleRemove))
	mux.HandleFunc("/api/party/play", s.guard(s.handlePlay))
	mux.HandleFunc("/api/party/leave", s.guard(s.handleLeave))
	return mux
}

// guard sets the headers every response shares. Authentication is per handler:
// join only needs the room code, everything else also needs the member ID.
func (s *partyServer) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next(w, r)
	}
}

func partyWriteJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func partyDecodeJSON(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, partyMaxBody)
	dec := json.NewDecoder(io.LimitReader(r.Body, partyMaxBody))
	if err := dec.Decode(dst); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return false
	}
	return true
}

func partyError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, errPartyUnknownMember), errors.Is(err, errPartyUnknownRoom):
		status = http.StatusUnauthorized
	case errors.Is(err, errPartyFull):
		status = http.StatusConflict
	}
	http.Error(w, err.Error(), status)
}

// handleLanding is what the invite QR code opens on a phone: a plain page that
// says how to join from a handheld. The room's name and code are only shown to
// a link that already carries the code, so the page reveals nothing a scan of
// the screen did not.
func (s *partyServer) handleLanding(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	snap := s.room.Snapshot()
	known := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("code")), snap.Code)
	var body strings.Builder
	body.WriteString("<!doctype html><html><head><meta charset=\"utf-8\">")
	body.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	body.WriteString("<title>JukaHub Watch Party</title><style>")
	body.WriteString("body{font-family:system-ui,sans-serif;background:#0f172a;color:#e2e8f0;margin:0;padding:32px}")
	body.WriteString(".card{max-width:420px;margin:auto;background:#1e293b;border-radius:20px;padding:24px}")
	body.WriteString(".code{font-size:40px;letter-spacing:6px;font-weight:700;color:#55d8ff}p{line-height:1.5}")
	body.WriteString("</style></head><body><div class=\"card\"><h1>Watch Party</h1>")
	if known {
		room := snap.Name
		if room == "" {
			room = "JukaHub"
		}
		body.WriteString("<p>Room <b>" + html.EscapeString(room) + "</b> hosted by <b>" +
			html.EscapeString(partyHostName(snap)) + "</b>.</p>")
		body.WriteString("<p class=\"code\">" + html.EscapeString(snap.Code) + "</p>")
		body.WriteString("<p>On a JukaHub handheld on this Wi-Fi, open <b>Tools → Watch Party</b>: the room is listed under " +
			"<b>Nearby rooms</b>, or choose <b>Join with a code</b> and enter the code above.</p>")
		if snap.HostAddr != "" {
			body.WriteString("<p>Host address: " + html.EscapeString(snap.HostAddr) + "</p>")
		}
	} else {
		body.WriteString("<p>A JukaHub Watch Party is running on this device. Ask the host for the room code.</p>")
	}
	body.WriteString("</div></body></html>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body.String())
}

func (s *partyServer) handleJoin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !partyDecodeJSON(w, r, &body) {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(body.Code), s.room.Code()) {
		partyError(w, errPartyUnknownRoom)
		return
	}
	id, err := s.room.Join(body.Name)
	if err != nil {
		partyError(w, err)
		return
	}
	partyWriteJSON(w, http.StatusOK, map[string]interface{}{
		"member_id": id,
		"snapshot":  s.room.Snapshot(),
	})
}

func (s *partyServer) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	code := r.URL.Query().Get("code")
	member := r.URL.Query().Get("member")
	watch, _ := strconv.ParseFloat(r.URL.Query().Get("w"), 64)
	if err := s.room.Authorize(code, member); err != nil {
		partyError(w, err)
		return
	}
	if watch > partyMaxWatchPoll {
		watch = partyMaxWatchPoll
	}
	if watch < 0 {
		watch = 0
	}
	if err := s.room.Heartbeat(member, watch); err != nil {
		partyError(w, err)
		return
	}
	partyWriteJSON(w, http.StatusOK, s.room.Snapshot())
}

func (s *partyServer) handleQueue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code   string `json:"code"`
		Member string `json:"member"`
		URL    string `json:"url"`
		Title  string `json:"title"`
	}
	if !partyDecodeJSON(w, r, &body) {
		return
	}
	if err := s.room.Authorize(body.Code, body.Member); err != nil {
		partyError(w, err)
		return
	}
	if _, err := s.room.AddQueue(body.Member, body.URL, body.Title); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	partyWriteJSON(w, http.StatusOK, s.room.Snapshot())
}

func (s *partyServer) handleSelect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code   string `json:"code"`
		Member string `json:"member"`
		Index  int    `json:"index"`
	}
	if !partyDecodeJSON(w, r, &body) {
		return
	}
	if err := s.room.Authorize(body.Code, body.Member); err != nil {
		partyError(w, err)
		return
	}
	if err := s.room.SelectQueue(body.Member, body.Index); err != nil {
		partyError(w, err)
		return
	}
	partyWriteJSON(w, http.StatusOK, s.room.Snapshot())
}

func (s *partyServer) handleRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code   string `json:"code"`
		Member string `json:"member"`
		ID     int    `json:"id"`
	}
	if !partyDecodeJSON(w, r, &body) {
		return
	}
	if err := s.room.Authorize(body.Code, body.Member); err != nil {
		partyError(w, err)
		return
	}
	if err := s.room.RemoveQueue(body.Member, body.ID); err != nil {
		partyError(w, err)
		return
	}
	partyWriteJSON(w, http.StatusOK, s.room.Snapshot())
}

func (s *partyServer) handlePlay(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code     string  `json:"code"`
		Member   string  `json:"member"`
		URL      string  `json:"url"`
		Title    string  `json:"title"`
		Position float64 `json:"position"`
		Playing  bool    `json:"playing"`
	}
	if !partyDecodeJSON(w, r, &body) {
		return
	}
	if err := s.room.Authorize(body.Code, body.Member); err != nil {
		partyError(w, err)
		return
	}
	if err := s.room.SetPlayback(body.Member, body.URL, body.Title, body.Position, body.Playing); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	partyWriteJSON(w, http.StatusOK, s.room.Snapshot())
}

func (s *partyServer) handleLeave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code   string `json:"code"`
		Member string `json:"member"`
	}
	if !partyDecodeJSON(w, r, &body) {
		return
	}
	if err := s.room.Authorize(body.Code, body.Member); err != nil {
		partyError(w, err)
		return
	}
	s.room.Leave(body.Member)
	partyWriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── Client ──────────────────────────────────────────────────────────────────

type partyClient struct {
	mu sync.Mutex

	baseURL  string
	code     string
	memberID string

	http *http.Client

	last     partySnapshot
	received time.Time
	// lastOK is the last successful poll, so the screen can tell a host that
	// went away from one slow answer.
	lastOK time.Time

	lastErr  string
	inFlight bool
}

func newPartyHTTP() *http.Client {
	return &http.Client{Timeout: 2500 * time.Millisecond}
}

func newPartyClient(baseURL, code, memberID string) *partyClient {
	return &partyClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		code:     strings.ToUpper(strings.TrimSpace(code)),
		memberID: memberID,
		http:     newPartyHTTP(),
	}
}

// partyNormalizeBase accepts "1.2.3.4", "1.2.3.4:8318" or a full http URL and
// returns a URL the client can use.
func partyNormalizeBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("enter the host address")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("that does not look like an address")
	}
	if u.Scheme != "http" {
		return "", errors.New("the party address must be http")
	}
	host := u.Hostname()
	if !partyHostAllowed(host) {
		return "", errors.New("the party host must be a local or LAN address")
	}
	port := u.Port()
	if port == "" {
		port = strconv.Itoa(partyDefaultPort)
	}
	if p, err := strconv.Atoi(port); err != nil || p <= 0 || p > 65535 {
		return "", errors.New("bad port in the address")
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// partyParseInvite reads what the host shows: "192.168.1.5:8318 ABCD1".
func partyParseInvite(invite string) (baseURL, code string, err error) {
	fields := strings.Fields(strings.TrimSpace(invite))
	if len(fields) == 0 {
		return "", "", errors.New("paste the host address and code")
	}
	code = strings.ToUpper(fields[len(fields)-1])
	if len(fields) == 1 {
		// One word is either a bare host (the code is missing) or a bare code
		// (the host is missing). A bare code used to be taken for a host name,
		// "http://ABCD2:8318", and the join failed with "missing its code". The
		// Watch Party screen looks a bare code up among the nearby rooms before
		// it ever gets here.
		if partyCodeValid(code) {
			return "", "", errors.New("add the host address too, e.g. 192.168.1.5 " + code)
		}
		return "", "", errors.New("the invite also needs the 5-character code")
	} else if !partyCodeValid(code) {
		return "", "", errors.New("the room code is 5 characters")
	}
	hostPart := strings.Join(fields[:len(fields)-1], " ")
	baseURL, err = partyNormalizeBase(hostPart)
	if err != nil {
		return "", "", err
	}
	return baseURL, code, nil
}

func (c *partyClient) post(path string, payload interface{}) (partySnapshot, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return partySnapshot{}, err
	}
	target := c.baseURL + path
	if !partyRequestURLRE.MatchString(target) {
		return partySnapshot{}, errors.New("the party address is not a safe local HTTP endpoint")
	}
	u, err := url.Parse(target)
	if err != nil || !partyHostAllowed(u.Hostname()) {
		return partySnapshot{}, errors.New("the party address is not a safe local HTTP endpoint")
	}
	req, err := partyNewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return partySnapshot{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return partySnapshot{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		return partySnapshot{}, errors.New(msg)
	}
	var snap partySnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return partySnapshot{}, err
	}
	c.store(snap)
	return snap, nil
}

func (c *partyClient) store(snap partySnapshot) {
	// A JukaHub host never sends more than its own limits, so anything past
	// them is cut here. The snapshot comes off the network: the leaderboard is
	// folded into the history on the main loop with a linear lookup per row,
	// and a hostile host's 20,000 rows a second froze the joiner and grew the
	// saved history without end.
	if len(snap.Leaderboard) > partyLeaderboardLimit {
		snap.Leaderboard = snap.Leaderboard[:partyLeaderboardLimit]
	}
	if len(snap.Members) > partyMaxMembers {
		snap.Members = snap.Members[:partyMaxMembers]
	}
	if len(snap.Queue) > partyMaxQueue {
		snap.Queue = snap.Queue[:partyMaxQueue]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Polls and button presses travel in parallel, so an answer can arrive
	// after a newer one: a slow poll landing after "play" used to roll the
	// queue and the playhead back for a second. The room's version only grows,
	// so an older version from the same room is dropped.
	if c.last.Code != "" && c.last.Code == snap.Code {
		if snap.Version < c.last.Version ||
			(snap.Version == c.last.Version && snap.ServerTime < c.last.ServerTime) {
			c.lastErr = ""
			return
		}
	}
	c.last = snap
	c.received = time.Now()
	c.lastErr = ""
	// inFlight belongs to Poll: a button press finishing while a poll is out
	// used to clear it and let a second poll start beside the first.
}

// Poll refreshes the snapshot and sends the watch seconds observed since the
// previous successful poll. It reports false when another poll is already in
// flight, so the caller never queues requests.
func (c *partyClient) Poll(watch float64) bool {
	c.mu.Lock()
	if c.inFlight {
		c.mu.Unlock()
		return false
	}
	c.inFlight = true
	base, code, member := c.baseURL, c.code, c.memberID
	c.mu.Unlock()

	q := url.Values{}
	q.Set("code", code)
	q.Set("member", member)
	if watch > 0 {
		q.Set("w", strconv.FormatFloat(watch, 'f', 2, 64))
	}
	target := base + "/api/party/state?" + q.Encode()
	if !partyRequestURLRE.MatchString(target) {
		c.fail(errors.New("the party address is not a safe local HTTP endpoint"))
		return true
	}
	u, parseErr := url.Parse(target)
	if parseErr != nil || !partyHostAllowed(u.Hostname()) {
		c.fail(errors.New("the party address is not a safe local HTTP endpoint"))
		return true
	}
	req, err := partyNewRequest(http.MethodGet, target, nil)
	if err != nil {
		c.fail(err)
		return true
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.fail(err)
		return true
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	c.mu.Lock()
	c.inFlight = false
	c.mu.Unlock()
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		c.fail(errors.New(msg))
		return true
	}
	var snap partySnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		c.fail(err)
		return true
	}
	c.store(snap)
	c.mu.Lock()
	c.lastOK = time.Now()
	c.mu.Unlock()
	return true
}

func (c *partyClient) fail(err error) {
	c.mu.Lock()
	c.inFlight = false
	if err != nil {
		c.lastErr = err.Error()
	}
	c.mu.Unlock()
}

func (c *partyClient) Queue(url, title string) (partySnapshot, error) {
	return c.post("/api/party/queue", map[string]interface{}{
		"code": c.code, "member": c.memberID, "url": url, "title": title,
	})
}

func (c *partyClient) Select(index int) (partySnapshot, error) {
	return c.post("/api/party/select", map[string]interface{}{
		"code": c.code, "member": c.memberID, "index": index,
	})
}

func (c *partyClient) Remove(id int) (partySnapshot, error) {
	return c.post("/api/party/remove", map[string]interface{}{
		"code": c.code, "member": c.memberID, "id": id,
	})
}

func (c *partyClient) Play(url, title string, position float64, playing bool) (partySnapshot, error) {
	return c.post("/api/party/play", map[string]interface{}{
		"code": c.code, "member": c.memberID, "url": url, "title": title,
		"position": position, "playing": playing,
	})
}

func (c *partyClient) Leave() {
	_, _ = c.post("/api/party/leave", map[string]interface{}{
		"code": c.code, "member": c.memberID,
	})
}

// Snapshot returns the last state received and when it arrived.
func (c *partyClient) Snapshot() (partySnapshot, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last, c.received, !c.received.IsZero()
}

func (c *partyClient) Err() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// ── Session ─────────────────────────────────────────────────────────────────

// partySession is the live party on this device: the host server (nil when
// joining), the client, and the cached snapshot the UI renders.
var partySession struct {
	mu sync.Mutex

	server *partyServer
	client *partyClient
	// beacon answers LAN discovery for a room this device hosts (nil when
	// joining, or when the discovery port could not be opened).
	beacon *partyBeacon

	snapshot partySnapshot
	received time.Time
	hasState bool
	lastErr  string

	applying    bool
	lastPoll    time.Time
	lastWatch   time.Time // previous poll used for the watch-time delta
	lastHistory time.Time // snapshot already folded into the durable archive
	lastSeek    time.Time
	lastCommand int64 // last playback instruction applied remotely
	invite      string
	hosting     bool
	dismissed   string // URL the user left; do not reopen it until they act or a new video starts
}

// partyHost starts a room on this device and joins it locally.
func partyHost(name string) error {
	partyLeaveSession()
	room, hostID := newPartyRoom(name, name)
	// Sharing starts from what is already on screen: hosting while a stream
	// plays should put that stream and its position in the room instead of
	// showing friends an empty party.
	if looksLikeStreamURL(playbackPageURL()) {
		if phase := snapshotVideoState().phase; phase == "playing" || phase == "paused" {
			_ = room.SetPlayback(hostID, playbackPageURL(), "", partyLocalPosition(), phase == "playing")
		}
	}
	srv := newPartyServer(room)
	if err := srv.Start(); err != nil {
		return err
	}
	// Discovery is best effort: a second JukaHub on the same PC may hold the
	// port, and the room still works by address and code without it.
	beacon, err := startPartyBeacon(func() (partyBeaconInfo, bool) {
		if !partyDiscoverable.Load() {
			return partyBeaconInfo{}, false
		}
		snap := room.Snapshot()
		return partyBeaconInfo{
			V:       1,
			App:     "JukaHub",
			Code:    snap.Code,
			Room:    snap.Name,
			Host:    partyHostName(snap),
			Port:    srv.Port(),
			Members: len(snap.Members),
			Title:   snap.Playback.Title,
		}, true
	})
	if err != nil {
		logPatch("[party] discovery beacon unavailable: %v", err)
		beacon = nil
	}
	client := newPartyClient("http://127.0.0.1:"+strconv.Itoa(srv.Port()), room.Code(), hostID)
	snap := room.Snapshot()

	partySession.mu.Lock()
	partySession.server = srv
	partySession.beacon = beacon
	partySession.client = client
	partySession.snapshot = snap
	partySession.received = time.Now()
	partySession.hasState = true
	partySession.invite = srv.Invite()
	partySession.hosting = true
	partySession.lastErr = ""
	partySession.lastWatch = time.Now()
	partySession.dismissed = ""
	partySession.mu.Unlock()
	trophyEvent(tkWatchParty)
	return nil
}

// partyJoin joins a room from an invite string and returns the initial state.
func partyJoin(invite, name string) error {
	// Joining is a transition: a device that was hosting leaves its own room
	// instead of running two sessions at once.
	partyLeaveSession()
	baseURL, code, err := partyParseInvite(invite)
	if err != nil {
		return err
	}
	if code == "" {
		return errors.New("the invite is missing its code")
	}
	client := newPartyClient(baseURL, code, "")
	body, _ := json.Marshal(map[string]string{"code": code, "name": name})
	target := baseURL + "/api/party/join"
	if !partyRequestURLRE.MatchString(target) {
		return errors.New("the party address is not a safe local HTTP endpoint")
	}
	u, parseErr := url.Parse(target)
	if parseErr != nil || !partyHostAllowed(u.Hostname()) {
		return errors.New("the party address is not a safe local HTTP endpoint")
	}
	req, err := partyNewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the host: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		return errors.New(msg)
	}
	var joined struct {
		MemberID string        `json:"member_id"`
		Snapshot partySnapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(data, &joined); err != nil || joined.MemberID == "" {
		return errors.New("the host returned an unreadable answer")
	}
	client.memberID = joined.MemberID
	// The first snapshot goes through the same size limits as every poll: it
	// comes off the network too.
	client.store(joined.Snapshot)
	first, _, _ := client.Snapshot()

	partySession.mu.Lock()
	partySession.client = client
	partySession.server = nil
	partySession.snapshot = first
	partySession.received = time.Now()
	partySession.hasState = true
	partySession.invite = strings.TrimSpace(invite)
	partySession.hosting = false
	partySession.lastErr = ""
	partySession.lastWatch = time.Now()
	partySession.dismissed = ""
	partySession.mu.Unlock()
	trophyEvent(tkWatchParty)
	return nil
}

// partyLeaveSession leaves the room and stops a host listener.
func partyLeaveSession() {
	partySession.mu.Lock()
	client := partySession.client
	srv := partySession.server
	beacon := partySession.beacon
	partySession.client = nil
	partySession.server = nil
	partySession.beacon = nil
	partySession.hasState = false
	partySession.snapshot = partySnapshot{}
	partySession.received = time.Time{}
	partySession.invite = ""
	partySession.lastErr = ""
	partySession.hosting = false
	partySession.lastHistory = time.Time{}
	partySession.dismissed = ""
	partySession.mu.Unlock()

	if client != nil {
		go client.Leave()
	}
	if beacon != nil {
		beacon.Stop()
	}
	if srv != nil {
		srv.Stop()
	}
	// Watch time accrued since the last debounced write must survive leaving.
	partyHistoryFlush()
}

func partyActive() bool {
	partySession.mu.Lock()
	defer partySession.mu.Unlock()
	return partySession.client != nil
}

// partyState returns the latest snapshot, its receipt time and the last error.
func partyState() (partySnapshot, time.Time, bool, string) {
	partySession.mu.Lock()
	defer partySession.mu.Unlock()
	if partySession.client == nil {
		return partySnapshot{}, time.Time{}, false, ""
	}
	if snap, recv, ok := partySession.client.Snapshot(); ok {
		return snap, recv, true, partySession.client.Err()
	}
	return partySession.snapshot, partySession.received, partySession.hasState, partySession.client.Err()
}

func partyInviteText() string {
	partySession.mu.Lock()
	defer partySession.mu.Unlock()
	if partySession.invite != "" {
		return partySession.invite
	}
	if partySession.snapshot.HostAddr != "" {
		return partySession.snapshot.HostAddr + " " + partySession.snapshot.Code
	}
	return ""
}

func partyIsHosting() bool {
	partySession.mu.Lock()
	defer partySession.mu.Unlock()
	return partySession.hosting
}

// partyTick polls the host at most once a second and applies the remote
// playhead. It is called from the main loop, so it must never block: the HTTP
// round trip runs on its own goroutine.
func partyTick(config *Config) {
	// Network work finishes on its own goroutine; the result is applied here so
	// the UI state is only ever written from the main loop. The tool screens
	// share the queue, so an install or a submission finishing while another
	// screen is up still lands.
	tbxDrainUI()

	defer partyHistorySync()

	partySession.mu.Lock()
	client := partySession.client
	if client == nil {
		partySession.mu.Unlock()
		return
	}
	now := time.Now()
	due := now.Sub(partySession.lastPoll) >= 900*time.Millisecond
	watch := 0.0
	if due {
		partySession.lastPoll = now
		if phase := snapshotVideoState().phase; phase == "playing" {
			watch = now.Sub(partySession.lastWatch).Seconds()
			if watch < 0 || watch > partyMaxWatchPoll {
				watch = 0
			}
		}
		partySession.lastWatch = now
	}
	partySession.mu.Unlock()

	if due {
		// The poll result lands on the next tick; nothing here waits for the
		// LAN. It ran inline despite this promise: with the host asleep or off
		// Wi-Fi every due frame blocked for the 2.5 s HTTP timeout, and the
		// joiner could barely reach Leave. Poll's in-flight guard keeps it to
		// one request at a time.
		go client.Poll(watch)
	}
	partyApplyRemote(config)
}

// partyHistorySync folds each newly received room snapshot into the durable
// all-time archive. It runs at most once per poll, not once per frame.
func partyHistorySync() {
	snap, received, ok, _ := partyState()
	if !ok || snap.Code == "" || received.IsZero() {
		return
	}
	partySession.mu.Lock()
	if !received.After(partySession.lastHistory) {
		partySession.mu.Unlock()
		return
	}
	partySession.lastHistory = received
	invite := partySession.invite
	partySession.mu.Unlock()
	partyHistoryObserve(snap, invite)
}

// partyApplyRemote converges the local player on the room playhead.
func partyApplyRemote(config *Config) {
	snap, received, ok, _ := partyState()
	if !ok || snap.Playback.URL == "" {
		return
	}
	target := partyLivePosition(snap, received, time.Now())
	localURL := playbackPageURL()

	partySession.mu.Lock()
	applying := partySession.applying
	lastSeek := partySession.lastSeek
	lastCommand := partySession.lastCommand
	dismissed := partySession.dismissed
	partySession.mu.Unlock()
	if applying {
		return
	}
	// A new command is a real button press from someone; a poll of the same
	// command is not. Commands force one correction even when the drift is
	// below the continuous threshold.
	newCommand := snap.Playback.Command != 0 && snap.Playback.Command != lastCommand

	if localURL != snap.Playback.URL {
		// The user deliberately left this video. A poll repeating the same
		// room state must not drag them back into it; a new queue selection
		// (different URL) or starting it themselves does.
		if dismissed != "" && dismissed == snap.Playback.URL {
			return
		}
		partySession.mu.Lock()
		partySession.dismissed = ""
		partySession.applying = true
		partySession.lastSeek = time.Now()
		partySession.mu.Unlock()

		// Remote playback must be visible: switch to the video scene and stop
		// the music player that shares the audio device.
		musicStopIfPlaying()
		ensureVideoScene(config)
		setPlaybackPageURL(snap.Playback.URL)
		if target > 1 {
			StartEmbeddedPlaybackAt(config, snap.Playback.URL, target)
		} else {
			StartEmbeddedPlayback(config, snap.Playback.URL)
		}

		partySession.mu.Lock()
		partySession.applying = false
		partySession.lastCommand = snap.Playback.Command
		partySession.mu.Unlock()
		return
	}

	phase := snapshotVideoState().phase
	if phase == "idle" || phase == "downloading" || phase == "error" {
		// Remember the command so it is not replayed when the decoder is
		// ready; the per-frame play/pause and drift checks below take over.
		if newCommand {
			partySession.mu.Lock()
			partySession.lastCommand = snap.Playback.Command
			partySession.mu.Unlock()
		}
		return
	}
	playing := phase == "playing"
	if newCommand {
		partySession.mu.Lock()
		partySession.lastCommand = snap.Playback.Command
		partySession.mu.Unlock()
	}

	if snap.Playback.Playing != playing {
		partySession.mu.Lock()
		partySession.applying = true
		partySession.mu.Unlock()
		ToggleVideoPlayback()
		partySession.mu.Lock()
		partySession.applying = false
		partySession.mu.Unlock()
		return
	}

	if !snap.Playback.Playing || !playing {
		return
	}
	local := partyLocalPosition()
	threshold := 3.0
	if newCommand {
		threshold = 0.5
	}
	if !partyNeedsSeek(local, target, threshold) {
		return
	}
	if !newCommand && time.Since(lastSeek) < 4*time.Second {
		return
	}
	duration := snapshotVideoState().duration
	if duration <= 0 {
		return
	}
	partySession.mu.Lock()
	partySession.applying = true
	partySession.lastSeek = time.Now()
	partySession.mu.Unlock()
	fraction := target / duration
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	SeekVideo(fraction)
	partySession.mu.Lock()
	partySession.applying = false
	partySession.mu.Unlock()
}

// partyLocalPosition is the local playhead in seconds.
func partyLocalPosition() float64 {
	state := snapshotVideoState()
	if state.duration <= 0 {
		return 0
	}
	return state.progress * state.duration
}

// partyPublishPlayback sends the local player state to the room. It is called
// from the player's own actions; applying a remote update calls those actions
// with the suppression flag set, so the room cannot bounce a command back and
// forth.
func partyPublishPlayback() {
	partySession.mu.Lock()
	client := partySession.client
	applying := partySession.applying
	partySession.mu.Unlock()
	if client == nil || applying {
		return
	}
	url := playbackPageURL()
	if !looksLikeStreamURL(url) {
		// Local files are not shareable across devices; leave them out of the
		// room instead of spamming the host with rejected updates.
		return
	}
	phase := snapshotVideoState().phase
	if phase == "idle" || phase == "downloading" || phase == "error" {
		return
	}
	position := partyLocalPosition()
	playing := phase == "playing"
	go func() {
		if _, err := client.Play(url, "", position, playing); err != nil {
			partySession.mu.Lock()
			partySession.lastErr = err.Error()
			partySession.mu.Unlock()
		}
	}()
}

// partyPublishStart announces a newly opened video. It is separate from
// partyPublishPlayback because the player has not started decoding yet, so
// reading the playhead would report zero or the previous video's position.
func partyPublishStart(url string, position float64) {
	if !looksLikeStreamURL(url) {
		return
	}
	partySession.mu.Lock()
	client := partySession.client
	applying := partySession.applying
	partySession.mu.Unlock()
	if client == nil || applying {
		return
	}
	partySession.mu.Lock()
	if partySession.dismissed == url {
		partySession.dismissed = ""
	}
	partySession.mu.Unlock()
	go func() {
		if _, err := client.Play(url, "", position, true); err != nil {
			partySession.mu.Lock()
			partySession.lastErr = err.Error()
			partySession.mu.Unlock()
		}
	}()
}

// partyNoteStop remembers that the user left a video on purpose, so the next
// polls do not relaunch it while the room still reports it as current.
func partyNoteStop(url string) {
	if url == "" {
		return
	}
	partySession.mu.Lock()
	if partySession.client != nil {
		partySession.dismissed = url
	}
	partySession.mu.Unlock()
}

// partyQueueURL adds a shared link. It returns an error for an invalid URL
// instead of silently dropping the paste.
func partyQueueURL(url, title string) error {
	partySession.mu.Lock()
	client := partySession.client
	partySession.mu.Unlock()
	if client == nil {
		return errors.New("no party is open")
	}
	_, err := client.Queue(url, title)
	return err
}

// partySelectItem starts a queued video for everyone.
func partySelectItem(index int) error {
	partySession.mu.Lock()
	client := partySession.client
	partySession.mu.Unlock()
	if client == nil {
		return errors.New("no party is open")
	}
	_, err := client.Select(index)
	return err
}

// partyRemoveItem deletes a queued video.
func partyRemoveItem(id int) error {
	partySession.mu.Lock()
	client := partySession.client
	partySession.mu.Unlock()
	if client == nil {
		return errors.New("no party is open")
	}
	_, err := client.Remove(id)
	return err
}

// partyResumeRemote forgets that the user left the room's video, so the next
// tick starts it again at the room's position.
func partyResumeRemote() {
	partySession.mu.Lock()
	partySession.dismissed = ""
	partySession.mu.Unlock()
}

// partyWatchingRemote reports whether the local player shows the room's video.
func partyWatchingRemote(snap partySnapshot) bool {
	return snap.Playback.URL != "" && playbackPageURL() == snap.Playback.URL &&
		snapshotVideoState().phase != "idle"
}

// partyInviteURL is what the invite QR code carries: the host's landing page,
// which tells a phone how to join from a handheld.
func partyInviteURL(snap partySnapshot) string {
	addr := strings.TrimSpace(snap.HostAddr)
	if addr == "" || snap.Code == "" {
		return ""
	}
	return "http://" + addr + "/party?code=" + url.QueryEscape(snap.Code)
}

// partyHostUnreachableFor is how long the polls have failed in a row; zero
// while they succeed. A joiner whose host left sees this grow and is offered a
// way out instead of a room frozen on its last state.
func partyHostUnreachableFor() time.Duration {
	partySession.mu.Lock()
	client := partySession.client
	hosting := partySession.hosting
	partySession.mu.Unlock()
	if client == nil || hosting {
		return 0
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.lastErr == "" {
		return 0
	}
	since := client.lastOK
	if since.IsZero() {
		since = client.received
	}
	if since.IsZero() {
		return 0
	}
	return time.Since(since)
}

// partyServerPort is the TCP port of the room this device hosts, or 0.
func partyServerPort() int {
	partySession.mu.Lock()
	srv := partySession.server
	partySession.mu.Unlock()
	if srv == nil {
		return 0
	}
	return srv.Port()
}
