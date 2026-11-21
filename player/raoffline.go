package main

// raoffline.go — Offline RetroAchievements: achievements keep unlocking in
// RetroArch games with no internet, and the unlocks reach
// retroachievements.org later. Built into JukaHub, no extra binary.
//
// How. RetroArch's rcheevos client sends every RetroAchievements request
// (POST /dorequest.php?r=login2, gameid, patch, achievementsets,
// startsession, awardachievement, ping…) to the server named by the hidden
// retroarch.cfg key cheevos_custom_host. For a RetroArch game started from
// the library, JukaHub points that key at a small HTTP server of its own on
// 127.0.0.1 (raoPrepareLaunch, through the scoped edit of racfgscope.go, so
// the key is gone again when the game ends and a game started from the
// firmware's menu never meets a proxy that is not running). The server:
//
//   - online, passes each request through to retroachievements.org (short
//     timeout) and keeps the answers a later offline game needs: the login,
//     a game hash's game ID, the achievement set, the session's unlock list,
//     badge pictures asked through it (raoffline_store.go);
//   - offline, or when the server does not answer, gives those answers from
//     the cache, says yes to pings, and keeps each unlock in a queue that is
//     sent when the internet is back (raoffline_replay.go), with how long ago
//     it happened where the protocol allows;
//   - answers anything else it has no copy of with a 503, like a server that
//     is down, so RetroArch says what it says when it is offline.
//
// Hardcore. Hardcore unlocks need the live server, so a hardcore launch with
// internet is never routed: RetroArch talks to the server itself. A hardcore
// launch with no internet whose game is cached offline goes through the proxy
// in Casual (the launch turns cheevos_hardcore_mode_enable off, scoped like
// the host): the player gets their unlocks, as Casual, which hardcore can
// still earn again later. A hardcore unlock that reaches the proxy anyway (the
// player switched hardcore on in RetroArch's menu) is passed through online
// and refused offline, never queued, so RetroArch retries it itself.
// Leaderboard entries are hardcore-only in RetroArch, so they are never
// queued either. The in-game settings (raach_settings.go) register
// their launch values before this file's, so the config read here already
// holds them.
//
// Safety. The server listens on 127.0.0.1 only, answers only requests from
// the loopback address with a loopback Host (no page in a browser on the
// handheld reaches it through DNS rebinding), bounds every request and
// answer, and needs RetroArch's own login token before it keeps an unlock or
// answers a session offline — a page that posts to it cannot unlock anything
// on the player's account. It runs only around RetroArch games: started
// before the launch, stopped raoIdleStop after the game is gone.
//
// "Wait for Wi-Fi", the Settings rows and "Cache recently played" are in
// raoffline_ui.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// raoKeyEnabled and raoKeyWaitWifi are the Settings switches (both on by
	// default).
	raoKeyEnabled  = "raOfflineAchievements"
	raoKeyWaitWifi = "raOfflineWaitWifi"

	// raoHostKey is RetroArch's setting for another RetroAchievements server.
	raoHostKey = "cheevos_custom_host"

	raoUpstreamBase = "https://retroachievements.org"
	raoMediaBase    = "https://media.retroachievements.org"

	// raoPreferredPort is tried first, so the host written into
	// retroarch.cfg is usually the same; a taken port falls back to any free
	// one (the key is written per launch, so any port works).
	raoPreferredPort = 47821
	// raoUpstreamTimeout bounds one pass-through: offline, RetroArch waits
	// this long at most before the cache answers.
	raoUpstreamTimeout = 8 * time.Second
	// raoRequestMax bounds a request from RetroArch, raoMediaMax a picture.
	raoRequestMax = 256 << 10
	raoMediaMax   = 1 << 20
	// raoIdleStop is how long the server outlives the last game.
	raoIdleStop = 90 * time.Second
	// raoLoginFailMax: after this many offline sign-ins with the wrong
	// password the server stops answering password sign-ins offline.
	raoLoginFailMax = 20
)

var (
	raoHTTP        = &http.Client{Timeout: raoUpstreamTimeout}
	errRaoUpstream = errors.New("retroachievements.org did not answer")
	raoLoginFails  atomic.Int32
)

// raoSrv is the running server; every field under mu. gen counts the starts
// and launches, so a stop timer armed before a new launch does nothing.
var raoSrv struct {
	mu   sync.Mutex
	srv  *http.Server
	port int
	gen  uint64
	stop *time.Timer
}

func init() {
	gameLaunchRegisterGate(raoWifiGate)
	gameLaunchRegisterPrepare(raoPrepareLaunch)
	gameLaunchRegisterExited(raoGameExited)
	deferStartupTask("raoffline", func() { go raoStartup() })
	Cleanup = append(Cleanup, raoShutdown)
}

// raoEnabled reports whether RetroArch games go through the proxy. Main
// thread (reads Custom).
func raoEnabled(c *Config) bool {
	if c == nil {
		return false
	}
	return customBool(c.Variables.Custom, raoKeyEnabled, true)
}

// raoWaitEnabled reports whether an achievement game waits for Wi-Fi.
func raoWaitEnabled(c *Config) bool {
	if c == nil {
		return false
	}
	return customBool(c.Variables.Custom, raoKeyWaitWifi, true)
}

// raoStartup reads the store once JukaHub is up and sends what waits.
func raoStartup() {
	rao.mu.Lock()
	raoLoadLocked()
	waiting := len(rao.queue.Items)
	rao.mu.Unlock()
	if waiting > 0 {
		logInfo("raoffline", "%d achievement unlock(s) earned offline are waiting to be sent", waiting)
		raoReplayKick()
	}
	raoSweepOrphans()
}

// raoShutdown writes the unlock ages and stops the server at exit.
func raoShutdown() {
	raoPersistAges(true)
	raoSrv.mu.Lock()
	srv := raoSrv.srv
	raoSrv.srv = nil
	raoSrv.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

// ── Reading RetroArch's config ───────────────────────────────────────────

// raoCfgString is a retroarch.cfg value as RetroArch reads it (the first
// line for the key), trimmed.
func raoCfgString(cfg, key string) string {
	v, _ := raConfigGet(cfg, key)
	return strings.TrimSpace(v)
}

// raoCfgBool is a retroarch.cfg switch, def when the line is missing.
func raoCfgBool(cfg, key string, def bool) bool {
	v := raoCfgString(cfg, key)
	if v == "" {
		return def
	}
	return strings.EqualFold(v, "true") || v == "1"
}

// raoAchievementsOn reports whether RetroArch runs achievements with cfg:
// switched on and an account named.
func raoAchievementsOn(cfg string) bool {
	return raoCfgBool(cfg, "cheevos_enable", false) && raoCfgString(cfg, "cheevos_username") != ""
}

// raoLaunchConfig is the text of the first config the launcher's RetroArch
// reads, "" when none.
func raoLaunchConfig(launcher string) string {
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, retroArchConfigCandidates())) {
		if raw, err := os.ReadFile(path); err == nil {
			return string(raw)
		}
	}
	return ""
}

// raoPlan is what a launch does about the proxy.
type raoPlan struct {
	route  bool   // point cheevos_custom_host at the proxy
	casual bool   // and turn hardcore off for this launch
	why    string // why not, for the log
}

// raoPlanFor decides a launch from RetroArch's config as the launch values
// left it, whether the internet answers and whether the game is cached for
// offline. Pure.
func raoPlanFor(cfg string, online, ready bool) raoPlan {
	if !raoAchievementsOn(cfg) {
		return raoPlan{why: "achievements are off or nobody is signed in"}
	}
	if raoCfgString(cfg, raoHostKey) != "" {
		return raoPlan{why: "RetroArch already names another RetroAchievements server"}
	}
	// RetroArch's default since 1.10 is hardcore on when the line is missing.
	hard := raoCfgBool(cfg, "cheevos_hardcore_mode_enable", true)
	switch {
	case !hard:
		return raoPlan{route: true}
	case online:
		return raoPlan{why: "hardcore plays on the live server"}
	case ready:
		return raoPlan{route: true, casual: true}
	}
	return raoPlan{why: "hardcore with no internet, and the game is not cached for offline"}
}

// raoClearStaleHost takes back a cheevos_custom_host JukaHub itself left in
// targets: one whose launch never finished (JukaHub stopped while RetroArch
// still ran, so the journal waits for a restore), or the proxy's address that
// RetroArch wrote back on exit after the journal had already been restored (a
// desktop RetroArch that outlived JukaHub). raoPlanFor would read either as
// the player's own server, so the game would neither go through the proxy nor
// reach the live server. A journal record is put back the way the restore does
// it (the player's value, or no line); the address on raoPreferredPort with no
// record is removed. Any other address is the player's and stays. Main thread,
// before this launch's own edit.
func raoClearStaleHost(targets []string) {
	ownURL := "http://127.0.0.1:" + strconv.Itoa(raoPreferredPort)
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	journal := raScopeLoadLocked()
	journalChanged := false
	for _, path := range targets {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		current := raoCfgString(string(raw), raoHostKey)
		if !strings.HasPrefix(current, "http://127.0.0.1:") {
			continue
		}
		var mine, rest []raScopeEdit
		for _, e := range journal.Edits {
			if e.Path == path && strings.EqualFold(e.Key, raoHostKey) && raValueEqual(e.Wrote, current) {
				mine = append(mine, e)
			} else {
				rest = append(rest, e)
			}
		}
		if len(mine) == 0 && current != ownURL {
			continue
		}
		edit := func(cfg string) (string, bool) { return raConfigUnset(cfg, raoHostKey) }
		if len(mine) > 0 {
			edit = func(cfg string) (string, bool) {
				out, changed := raScopeRestoreText(cfg, mine)
				// A record whose "player's value" was itself a stale address.
				if v, _ := raConfigGet(out, raoHostKey); strings.TrimSpace(v) == ownURL {
					var unset bool
					out, unset = raConfigUnset(out, raoHostKey)
					changed = changed || unset
				}
				return out, changed
			}
		}
		if _, err := editRetroArchConfig(path, edit); err != nil {
			logWarn("raoffline", "cannot take back an old offline achievements address in %s: %v", path, err)
			continue
		}
		if len(mine) > 0 {
			journal.Edits = rest
			journalChanged = true
		}
		logInfo("raoffline", "took back the offline achievements address an unfinished launch left in %s", path)
	}
	if journalChanged {
		if err := raScopeSaveLocked(journal); err != nil {
			logWarn("raoffline", "could not update the RetroArch launch journal: %v", err)
		}
	}
}

// ── The launch ───────────────────────────────────────────────────────────

// raoPrepareLaunch points a RetroArch launch at the proxy for this launch
// (gameLaunchRegisterPrepare). Main thread. It never fails a launch: when
// anything is missing RetroArch simply talks to the server itself.
func raoPrepareLaunch(launcher string, game libraryGame) {
	if !retroArchLauncher(launcher) {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, retroArchConfigCandidates()))
	if len(targets) == 0 {
		return
	}
	// Even with the switch off: a dead address of JukaHub's own must not
	// outlive the launch that wrote it.
	raoClearStaleHost(targets)
	if !raoEnabled(appConfig) {
		return
	}
	raw, err := os.ReadFile(targets[0])
	if err != nil {
		return
	}
	online := IsOnline()
	plan := raoPlanFor(string(raw), online, raoGameReadyOffline(game.Path))
	if !plan.route {
		if plan.why != "" {
			logDebug("raoffline", "%s goes to RetroAchievements directly: %s", game.Title, plan.why)
		}
		return
	}
	port, err := raoEnsureServer()
	if err != nil {
		logWarn("raoffline", "could not start the offline achievements proxy; RetroArch goes online directly: %v", err)
		return
	}
	values := []raScopeValue{{Key: raoHostKey, Value: "http://127.0.0.1:" + strconv.Itoa(port)}}
	if plan.casual {
		values = append(values, raScopeValue{Key: "cheevos_hardcore_mode_enable", Value: "false"})
	}
	applied := false
	for _, path := range targets {
		if _, err := raScopeApply(path, values); err != nil {
			logWarn("raoffline", "cannot route RetroAchievements through JukaHub in %s: %v", path, err)
			continue
		}
		applied = true
	}
	// The stop is armed now, not only at the game's exit: a launch that fails
	// after this hook never reaches the exit hook, and the timer re-arms itself
	// while the game or RetroArch runs (raoStopIfIdle).
	raoScheduleStop()
	if !applied {
		return
	}
	raoNoteLaunch(game.Path)
	mode := "online, answers cached for later"
	if !online {
		mode = "offline, answered from the cache"
	}
	if plan.casual {
		mode += ", Casual for this game (hardcore needs the internet)"
	}
	logInfo("raoffline", "%s: RetroAchievements through JukaHub (%s)", game.Title, mode)
	if plan.casual {
		// After the caller's own "Playing…" toast, which would replace it.
		tbxPost(func() {
			ShowToast("No internet: achievements unlock in Casual and are sent when you're back online", ToastKindInfo)
		})
	}
}

// raoGameExited stops the server a little after the game and sends what the
// game queued (gameLaunchRegisterExited; reaper goroutine).
func raoGameExited(libraryGame, string, time.Duration, error) {
	raoScheduleStop()
	raoReplayKick()
}

// ── The server ───────────────────────────────────────────────────────────

// raoEnsureServer starts the server if it is not running and returns its
// port; a stop waiting to happen is called off.
func raoEnsureServer() (int, error) {
	raoSrv.mu.Lock()
	defer raoSrv.mu.Unlock()
	raoSrv.gen++
	if raoSrv.stop != nil {
		raoSrv.stop.Stop()
		raoSrv.stop = nil
	}
	if raoSrv.srv != nil {
		return raoSrv.port, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(raoPreferredPort)))
	if err != nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return 0, err
		}
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return 0, errors.New("no port")
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(raoServeHTTP),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	raoSrv.srv, raoSrv.port = srv, addr.Port
	go func() {
		err := srv.Serve(ln)
		raoSrv.mu.Lock()
		if raoSrv.srv == srv {
			raoSrv.srv = nil
		}
		raoSrv.mu.Unlock()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logWarn("raoffline", "the offline achievements proxy stopped: %v", err)
		}
	}()
	logInfo("raoffline", "offline achievements proxy listening on 127.0.0.1:%d", addr.Port)
	return addr.Port, nil
}

// raoScheduleStop arms the stop for raoIdleStop from now.
func raoScheduleStop() {
	raoSrv.mu.Lock()
	defer raoSrv.mu.Unlock()
	if raoSrv.srv == nil {
		return
	}
	if raoSrv.stop != nil {
		raoSrv.stop.Stop()
	}
	gen := raoSrv.gen
	raoSrv.stop = time.AfterFunc(raoIdleStop, func() { raoStopIfIdle(gen) })
}

// raoStopIfIdle stops the server unless a game or RetroArch still runs, or
// a launch came since the stop was armed.
func raoStopIfIdle(gen uint64) {
	if gameScreenSuspended() || retroArchRunning() {
		raoScheduleStop()
		return
	}
	raoSrv.mu.Lock()
	if raoSrv.gen != gen || raoSrv.srv == nil {
		raoSrv.mu.Unlock()
		return
	}
	srv := raoSrv.srv
	raoSrv.srv, raoSrv.stop = nil, nil
	raoSrv.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	raoPersistAges(true)
	logDebug("raoffline", "offline achievements proxy stopped (no game)")
}

// ── Requests ─────────────────────────────────────────────────────────────

// raoRequest is one request from RetroArch, read once.
type raoRequest struct {
	r      string // the endpoint (r=), lower case
	user   string
	token  string
	pass   string
	hash   string // m= for the hash endpoints, lower case
	game   int    // g=
	hard   bool   // h=1
	params url.Values
	raw    []byte // the POST body, forwarded as it came
	ctype  string // the POST body's Content-Type
	query  string
	post   bool
	agent  string
}

var errRaoTooLarge = errors.New("request too large")

func raoParse(r *http.Request) (raoRequest, error) {
	req := raoRequest{post: r.Method == http.MethodPost, query: r.URL.RawQuery, agent: r.UserAgent(),
		ctype: r.Header.Get("Content-Type")}
	params := r.URL.Query()
	if req.post {
		raw, err := io.ReadAll(io.LimitReader(r.Body, raoRequestMax+1))
		if err != nil {
			return req, err
		}
		if len(raw) > raoRequestMax {
			return req, errRaoTooLarge
		}
		req.raw = raw
		if form, err := url.ParseQuery(string(raw)); err == nil {
			for k, v := range form {
				params[k] = v
			}
		}
	}
	req.params = params
	req.r = strings.ToLower(strings.TrimSpace(params.Get("r")))
	req.user = strings.TrimSpace(params.Get("u"))
	req.token = params.Get("t")
	req.pass = params.Get("p")
	req.hash = strings.ToLower(strings.TrimSpace(params.Get("m")))
	req.game, _ = strconv.Atoi(params.Get("g"))
	req.hard = params.Get("h") == "1"
	return req, nil
}

// raoLoopback reports whether addr (host:port) is a loopback address.
func raoLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// raoHostOK accepts the Host header of a request made to the loopback
// address, by number or as localhost: a page whose name was pointed at
// 127.0.0.1 sends its own name and is refused.
func raoHostOK(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = strings.Trim(host, "[]")
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// raoServeHTTP is the server's one handler.
func raoServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if p := recover(); p != nil {
			logWarn("raoffline", "a RetroAchievements request failed: %v", p)
			raoWrite(w, http.StatusInternalServerError, "", raoErrorBody("JukaHub's offline achievements failed"))
		}
	}()
	if !raoLoopback(r.RemoteAddr) || !raoHostOK(r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch {
	case r.URL.Path == "/dorequest.php" && (r.Method == http.MethodPost || r.Method == http.MethodGet):
		raoServeRequest(w, r)
	case r.Method == http.MethodGet && raoMediaPath(r.URL.Path) != "":
		raoServeMedia(w, r)
	default:
		http.NotFound(w, r)
	}
}

// raoServeRequest answers one /dorequest.php request.
func raoServeRequest(w http.ResponseWriter, r *http.Request) {
	req, err := raoParse(r)
	if err != nil {
		raoWrite(w, http.StatusRequestEntityTooLarge, "", raoErrorBody("The request is too large"))
		return
	}
	online := IsOnline()
	switch req.r {
	case "login", "login2":
		raoServeLogin(w, req, online)
	case "gameid":
		if !raoValidHash(req.hash) {
			raoPassOrFail(w, req, online)
			return
		}
		raoServeCached(w, req, online, "gameid:"+req.hash, func(body []byte) int {
			raoNoteGameHash(req.hash, raoJSONInt(body, "GameID"))
			return 0
		})
	case "achievementsets":
		key := ""
		switch {
		case raoValidHash(req.hash):
			key = "sets:" + req.hash
		case req.game > 0:
			key = "sets:g" + strconv.Itoa(req.game)
		}
		if key == "" {
			raoPassOrFail(w, req, online)
			return
		}
		raoServeCached(w, req, online, key, func(body []byte) int {
			game := raoJSONInt(body, "GameID")
			if game <= 0 {
				game = req.game
			}
			if req.hash != "" {
				raoNoteGameHash(req.hash, game)
			}
			return game
		})
	case "patch":
		if req.game <= 0 {
			raoPassOrFail(w, req, online)
			return
		}
		raoServeCached(w, req, online, "patch:"+strconv.Itoa(req.game), func([]byte) int { return req.game })
	case "startsession":
		raoServeSession(w, req, online, false)
	case "unlocks":
		raoServeSession(w, req, online, true)
	case "ping", "postactivity":
		if online && raoPass(w, req) {
			return
		}
		raoWrite(w, http.StatusOK, "", []byte(`{"Success":true}`))
	case "awardachievement":
		raoServeAward(w, req, online)
	default:
		// Leaderboard entries (hardcore-only) and everything else: the
		// server's answer, or "unavailable".
		raoPassOrFail(w, req, online)
	}
}

// raoServeCached passes a request through and keeps a good answer under key,
// or answers from the cache when the server is out of reach. seen is told
// every answer given and returns the game ID it belongs to.
func raoServeCached(w http.ResponseWriter, req raoRequest, online bool, key string, seen func(body []byte) int) {
	if online {
		status, ctype, body, err := raoUpstream(req)
		if err == nil && status < 500 {
			if status == http.StatusOK && raoSuccess(body) {
				raoCachePut(key, body, seen(body))
			}
			raoWrite(w, status, ctype, body)
			return
		}
	}
	if body, ok := raoCacheGet(key); ok {
		seen(body)
		raoWrite(w, http.StatusOK, "", body)
		return
	}
	raoUnavailable(w)
}

// raoServeLogin passes a sign-in through and keeps it, or signs RetroArch in
// from the cache when its token (or password) is the one the server accepted.
func raoServeLogin(w http.ResponseWriter, req raoRequest, online bool) {
	if req.user == "" {
		raoPassOrFail(w, req, online)
		return
	}
	if online {
		status, ctype, body, err := raoUpstream(req)
		if err == nil && status < 500 {
			if status == http.StatusOK && raoSuccess(body) {
				token := raoJSONString(body, "Token")
				if token == "" {
					token = req.token
				}
				raoLoginStore(req.user, token, req.pass, body, req.agent)
				raoReplayKick()
			}
			raoWrite(w, status, ctype, body)
			return
		}
	}
	l, ok := raoLoginFor(req.user)
	if ok && (req.pass == "" || raoLoginFails.Load() < raoLoginFailMax) && raoLoginMatches(l, req.token, req.pass) {
		logInfo("raoffline", "RetroArch signed in to RetroAchievements offline")
		raoWrite(w, http.StatusOK, "", l.Body)
		return
	}
	if ok && req.pass != "" {
		raoLoginFails.Add(1)
	}
	raoUnavailable(w)
}

// raoServeSession passes a game session's start through and keeps its list
// of unlocks, or answers it from the cache with the unlocks earned since
// added. legacy is an older RetroArch's "unlocks" request.
func raoServeSession(w http.ResponseWriter, req raoRequest, online, legacy bool) {
	if req.game <= 0 || req.user == "" {
		raoPassOrFail(w, req, online)
		return
	}
	key := "session:" + raoUserKey(req.user) + ":" + strconv.Itoa(req.game)
	if legacy {
		key = "unlocks:" + raoUserKey(req.user) + ":" + strconv.Itoa(req.game) + ":" + boolString(req.hard)
	}
	raoSetCurrentGame(req.user, req.game)
	if online {
		status, ctype, body, err := raoUpstream(req)
		if err == nil && status < 500 {
			if status == http.StatusOK && raoSuccess(body) {
				raoCachePut(key, body, req.game)
				if !legacy {
					raoSessionCached(req.user, req.game)
				}
			}
			raoWrite(w, status, ctype, body)
			return
		}
	}
	l, ok := raoLoginFor(req.user)
	if !ok || !raoLoginMatches(l, req.token, "") {
		raoUnavailable(w)
		return
	}
	body, cached := raoCacheGet(key)
	if !cached {
		// A game cached ahead (Cache recently played) has its achievements
		// but no session yet: an empty list starts it.
		if !raoGameCached(req.game) {
			raoUnavailable(w)
			return
		}
		body = []byte(`{"Success":true,"Unlocks":[],"HardcoreUnlocks":[]}`)
		if legacy {
			body = []byte(`{"Success":true,"UserUnlocks":[]}`)
		}
	}
	known := raoKnownUnlocks(req.user, req.game)
	if legacy {
		body = raoMergeLegacyUnlocks(body, known, req.hard)
	} else {
		body = raoMergeSession(body, known, raoServerNow())
	}
	raoWrite(w, http.StatusOK, "", body)
}

// raoServeAward passes an unlock through, or keeps it to send later.
func raoServeAward(w http.ResponseWriter, req raoRequest, online bool) {
	id, _ := strconv.Atoi(req.params.Get("a"))
	game := raoCurrentGame(req.user)
	if online {
		status, ctype, body, err := raoUpstream(req)
		if err == nil && status < 500 && status != http.StatusTooManyRequests {
			if status == http.StatusOK && (raoSuccess(body) || raoAlreadyUnlocked(body)) {
				raoNoteUnlock(req.user, game, id, req.hard)
			}
			raoWrite(w, status, ctype, body)
			return
		}
	}
	if req.hard {
		raoWrite(w, http.StatusServiceUnavailable, "", raoErrorBody("Hardcore unlocks need the internet; RetroArch sends it again"))
		return
	}
	l, ok := raoLoginFor(req.user)
	if id <= 0 || !ok || !raoLoginMatches(l, req.token, "") {
		raoUnavailable(w)
		return
	}
	params := map[string]string{}
	for k := range req.params {
		if k != "t" {
			params[k] = req.params.Get(k)
		}
	}
	if !raoQueueAdd(req.user, game, params, req.agent) {
		raoUnavailable(w)
		return
	}
	logInfo("raoffline", "achievement %d unlocked offline; it is sent when the internet is back", id)
	raoReplayKick()
	answer, _ := json.Marshal(map[string]int64{
		"AchievementID": int64(id),
		"Score":         int64(raoJSONInt(l.Body, "Score")),
		"SoftcoreScore": int64(raoJSONInt(l.Body, "SoftcoreScore")),
	})
	raoWrite(w, http.StatusOK, "", raoWithSuccess(answer))
}

// raoWithSuccess adds "Success":true to a JSON object made here.
func raoWithSuccess(obj []byte) []byte {
	if len(obj) < 2 || obj[0] != '{' {
		return []byte(`{"Success":true}`)
	}
	if len(obj) == 2 {
		return []byte(`{"Success":true}`)
	}
	return append([]byte(`{"Success":true,`), obj[1:]...)
}

// raoPass passes a request through and reports whether the server answered.
func raoPass(w http.ResponseWriter, req raoRequest) bool {
	status, ctype, body, err := raoUpstream(req)
	if err != nil || status >= 500 {
		return false
	}
	raoWrite(w, status, ctype, body)
	return true
}

// raoPassOrFail passes a request through online, else answers 503.
func raoPassOrFail(w http.ResponseWriter, req raoRequest, online bool) {
	if online && raoPass(w, req) {
		return
	}
	raoUnavailable(w)
}

// raoServeMedia answers a badge, game icon or user picture from the cache, or
// fetches and keeps it online.
func raoServeMedia(w http.ResponseWriter, r *http.Request) {
	media := raoMediaPath(r.URL.Path)
	key := "media:" + media
	if body, ok := raoCacheGet(key); ok {
		raoWrite(w, http.StatusOK, "image/png", body)
		return
	}
	if !IsOnline() {
		http.NotFound(w, r)
		return
	}
	hr, err := http.NewRequest(http.MethodGet, raoMediaBase+"/"+media+".png", nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	status, _, body, err := raoDo(hr, r.UserAgent(), raoMediaMax)
	if err != nil || status != http.StatusOK || !bytes.HasPrefix(body, []byte("\x89PNG")) {
		http.NotFound(w, r)
		return
	}
	raoCachePut(key, body, 0)
	raoWrite(w, http.StatusOK, "image/png", body)
}

// raoMediaPath is the picture a path asks for ("Badge/12345_lock"), "" for
// any other path. Only plain names are accepted: the name becomes part of a
// URL and a cache key, never a file path.
func raoMediaPath(p string) string {
	for _, kind := range []string{"Badge", "Images", "UserPic"} {
		prefix := "/" + kind + "/"
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		name, ok := strings.CutSuffix(strings.TrimPrefix(p, prefix), ".png")
		if !ok || name == "" || len(name) > 64 {
			return ""
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return ""
			}
		}
		return kind + "/" + name
	}
	return ""
}

// ── The server's side ────────────────────────────────────────────────────

// raoUpstream forwards a request to retroachievements.org as RetroArch made
// it: the same query, body, Content-Type and User-Agent.
func raoUpstream(req raoRequest) (int, string, []byte, error) {
	var hr *http.Request
	var err error
	target := raoUpstreamBase + "/dorequest.php"
	if req.query != "" {
		target += "?" + req.query
	}
	if req.post {
		hr, err = http.NewRequest(http.MethodPost, target, bytes.NewReader(req.raw))
		if err == nil {
			ctype := req.ctype
			if ctype == "" {
				ctype = "application/x-www-form-urlencoded"
			}
			hr.Header.Set("Content-Type", ctype)
		}
	} else {
		hr, err = http.NewRequest(http.MethodGet, target, nil)
	}
	if err != nil {
		return 0, "", nil, errRaoUpstream
	}
	return raoDo(hr, req.agent, raoEntryMaxBytes)
}

// raoPostForm sends a request JukaHub builds (a queued unlock, a cache
// fetch) and returns the status and body.
func raoPostForm(form url.Values, agent string) (int, []byte, error) {
	hr, err := http.NewRequest(http.MethodPost, raoUpstreamBase+"/dorequest.php", strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, errRaoUpstream
	}
	hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	status, _, body, err := raoDo(hr, agent, raoEntryMaxBytes)
	return status, body, err
}

// raoDo runs one request with a bounded answer. Its errors never carry the
// http package's text: that quotes the URL, which may hold a token.
func raoDo(hr *http.Request, agent string, max int) (int, string, []byte, error) {
	if strings.TrimSpace(agent) == "" {
		agent = "JukaHub/1.0"
	}
	hr.Header.Set("User-Agent", agent)
	resp, err := raoHTTP.Do(hr)
	if err != nil {
		return 0, "", nil, errRaoUpstream
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
	if err != nil || len(body) > max {
		return 0, "", nil, errRaoUpstream
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), body, nil
}

// ── Answers ──────────────────────────────────────────────────────────────

func raoWrite(w http.ResponseWriter, status int, ctype string, body []byte) {
	if ctype == "" {
		ctype = "application/json"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// raoErrorBody is an rcheevos-style refusal.
func raoErrorBody(msg string) []byte {
	out, _ := json.Marshal(map[string]any{"Success": false, "Error": msg, "Code": "offline"})
	return out
}

// raoUnavailable is the answer for something the cache cannot give offline:
// what a server that is down says, so RetroArch treats it as one.
func raoUnavailable(w http.ResponseWriter) {
	raoWrite(w, http.StatusServiceUnavailable, "", raoErrorBody("No internet: RetroAchievements cannot be reached"))
}

// raoSuccess reports an answer with "Success": true.
func raoSuccess(body []byte) bool {
	var ans struct {
		Success bool `json:"Success"`
	}
	return json.Unmarshal(body, &ans) == nil && ans.Success
}

// raoAlreadyUnlocked reports the refusal of an unlock the account already
// has, which is as good as a yes.
func raoAlreadyUnlocked(body []byte) bool {
	var ans struct {
		Error string `json:"Error"`
	}
	return json.Unmarshal(body, &ans) == nil && strings.Contains(strings.ToLower(ans.Error), "already")
}

// raoJSONField finds a top-level field by name in any capitalisation (the
// server spells GameID and GameId in different answers).
func raoJSONField(body []byte, name string) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return nil, false
	}
	if raw, ok := obj[name]; ok {
		return raw, true
	}
	for k, raw := range obj {
		if strings.EqualFold(k, name) {
			return raw, true
		}
	}
	return nil, false
}

// raoJSONInt is a top-level number (or numeric string), 0 when absent.
func raoJSONInt(body []byte, name string) int {
	raw, ok := raoJSONField(body, name)
	if !ok {
		return 0
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if v, err := n.Int64(); err == nil {
			return int(v)
		}
		if f, err := n.Float64(); err == nil {
			return int(f)
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.Atoi(strings.TrimSpace(s))
		return v
	}
	return 0
}

// raoJSONString is a top-level string, "" when absent.
func raoJSONString(body []byte, name string) string {
	raw, ok := raoJSONField(body, name)
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// raoServerNow is the "now" a cached session answer is given: the clock when
// it has been set (a TrimUI boots before NTP), else 0 to keep the cached one.
// It is only what RetroArch shows as an unlock's moment.
func raoServerNow() int64 {
	now := time.Now()
	if now.Year() < 2025 {
		return 0
	}
	return now.Unix()
}
