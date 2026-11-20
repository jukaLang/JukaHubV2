package main

// partymode_net.go — Party Mode's web side: the page phones open from the QR
// code, the game's small JSON API, the cover crops, and the session that ties
// the listener to the Party Mode screen.
//
// The listener runs only while the Party Mode screen is up: entering the
// screen starts it (pmodeRun, a scene task), leaving the screen by any route
// cancels the task and stops it, and every phone sees "The party is over".
// It is plain HTTP on the LAN, like Watch Party: joining needs the room code
// the QR code carries, every later call the 128-bit token the join returned.
// It serves the game and nothing else — no files, no paths, no settings — and
// a phone only ever gets the stage of the cover the room has already shown.
//
// Phones long-poll: GET /api/pm/state?v=N answers at once when the room has
// moved past version N, else waits up to pmodePollWait for the next change,
// so a buzz shows on every phone within a round trip.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"mime"
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
	pmodeMaxBody   = 4 << 10
	pmodePollWait  = 15 * time.Second
	pmodeMaxWaiter = 3 * pmodeMaxPlayers
	// A wrong room code or an unknown token is cheap to try again; after
	// this many in the window the address gets 429 instead of an answer.
	pmodeFailLimit  = 30
	pmodeFailWindow = time.Minute
	pmodeFailTrack  = 256
	// pmodeCropEdge is a crop's side on the phone; pmodeFullEdge the whole
	// cover's longer side.
	pmodeCropEdge  = 360
	pmodeFullEdge  = 480
	pmodeTickEvery = 150 * time.Millisecond
	// pmodeMaxConns bounds open connections: a long poll, a picture and a
	// play per seat, with room to spare.
	pmodeMaxConns = 8 * pmodeMaxPlayers
)

// pmodeServer is the listener for one room.
type pmodeServer struct {
	room *pmodeRoom

	mu    sync.Mutex
	ln    net.Listener
	srv   *http.Server
	port  int
	done  chan struct{}
	fails map[string]*pmodeFails

	waiters atomic.Int32

	imgMu  sync.Mutex
	imgPic *image.NRGBA
	imgs   map[int][]byte
}

type pmodeFails struct {
	n     int
	since time.Time
}

func newPmodeServer(room *pmodeRoom) *pmodeServer {
	return &pmodeServer{room: room, done: make(chan struct{}), fails: map[string]*pmodeFails{}}
}

func pmodePort() int {
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_PARTYMODE_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return pmodeDefaultPort
}

func (s *pmodeServer) Start() error {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(pmodePort()))
	if err != nil {
		// Taken (a second JukaHub, or the previous visit still closing):
		// any free port works, the QR code carries the real one.
		ln, err = net.Listen("tcp", ":0")
		if err != nil {
			return err
		}
	}
	ln = &peerLimitListener{Listener: ln, sem: make(chan struct{}, pmodeMaxConns)}
	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      pmodePollWait + 10*time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	port := 0
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		port = tcp.Port
	}
	s.mu.Lock()
	s.ln, s.srv = ln, srv
	s.port = port
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logWarn("partymode", "server stopped: %v", err)
		}
	}()
	return nil
}

func (s *pmodeServer) Stop() {
	s.mu.Lock()
	srv := s.srv
	s.srv, s.ln = nil, nil
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.mu.Unlock()
	if srv == nil {
		return
	}
	// Waiting long polls end on done; anything else gets a short grace.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
}

func (s *pmodeServer) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

func (s *pmodeServer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

// pmodeJoinURL is what the QR code carries.
func pmodeJoinURL(host string, port int, code string) string {
	if host == "" || port <= 0 || code == "" {
		return ""
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/?room=" + url.QueryEscape(code)
}

func (s *pmodeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePage)
	mux.HandleFunc("/api/pm/join", s.handleJoin)
	mux.HandleFunc("/api/pm/state", s.handleState)
	mux.HandleFunc("/api/pm/buzz", s.handleBuzz)
	mux.HandleFunc("/api/pm/answer", s.handleAnswer)
	mux.HandleFunc("/api/pm/leave", s.handleLeave)
	mux.HandleFunc("/api/pm/img", s.handleImage)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		mux.ServeHTTP(w, r)
	})
}

// ── Throttle ─────────────────────────────────────────────────────────────

func (s *pmodeServer) throttled(addr string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fails[addr]
	if f == nil {
		return false
	}
	if now.Sub(f.since) > pmodeFailWindow {
		delete(s.fails, addr)
		return false
	}
	return f.n >= pmodeFailLimit
}

func (s *pmodeServer) noteFail(addr string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fails[addr]
	if f == nil || now.Sub(f.since) > pmodeFailWindow {
		if len(s.fails) >= pmodeFailTrack {
			for k, v := range s.fails {
				if now.Sub(v.since) > pmodeFailWindow {
					delete(s.fails, k)
				}
			}
			if len(s.fails) >= pmodeFailTrack {
				s.evictOldestLocked()
			}
		}
		f = &pmodeFails{since: now}
		s.fails[addr] = f
	}
	f.n++
}

// evictOldestLocked drops the oldest entry, so a table full of live entries
// (many addresses) keeps counting the newest instead of being wiped.
func (s *pmodeServer) evictOldestLocked() {
	oldest := ""
	var at time.Time
	for k, v := range s.fails {
		if oldest == "" || v.since.Before(at) {
			oldest, at = k, v.since
		}
	}
	delete(s.fails, oldest)
}

// ── Helpers ──────────────────────────────────────────────────────────────

func pmodeWriteJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func pmodeFail(w http.ResponseWriter, status int, msg string) {
	pmodeWriteJSON(w, status, map[string]string{"error": msg})
}

// pmodeReadBody decodes a small JSON body from a POST. The body must say it
// is JSON: a web page elsewhere can send a plain-text POST to a LAN address
// without asking first, but not a JSON one.
func pmodeReadBody(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	if r.Method != http.MethodPost {
		pmodeFail(w, http.StatusMethodNotAllowed, "POST only")
		return false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		pmodeFail(w, http.StatusUnsupportedMediaType, "JSON only")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, pmodeMaxBody))
	if err := dec.Decode(dst); err != nil {
		pmodeFail(w, http.StatusBadRequest, "bad request")
		return false
	}
	return true
}

// gate is the throttle every API call passes first.
func (s *pmodeServer) gate(w http.ResponseWriter, r *http.Request) (string, bool) {
	addr := castHost(r.RemoteAddr)
	if s.throttled(addr, time.Now()) {
		pmodeFail(w, http.StatusTooManyRequests, "too many tries — wait a minute")
		return addr, false
	}
	return addr, true
}

// ── Handlers ─────────────────────────────────────────────────────────────

func (s *pmodeServer) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	_, _ = w.Write([]byte(pmodePageHTML))
}

func (s *pmodeServer) handleJoin(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.gate(w, r)
	if !ok {
		return
	}
	var req struct {
		Room string `json:"room"`
		Name string `json:"name"`
	}
	if !pmodeReadBody(w, r, &req) {
		return
	}
	now := time.Now()
	if !s.room.CodeMatches(req.Room) {
		s.noteFail(addr, now)
		pmodeFail(w, http.StatusForbidden, errPmodeBadCode.Error())
		return
	}
	token, err := s.room.Join(req.Name, addr, now)
	if err != nil {
		pmodeFail(w, http.StatusConflict, err.Error())
		return
	}
	pmodeWriteJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *pmodeServer) handleState(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.gate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		pmodeFail(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	q := r.URL.Query()
	token := q.Get("t")
	state, known := s.room.State(token, time.Now())
	if !known {
		s.noteFail(addr, time.Now())
		pmodeFail(w, http.StatusUnauthorized, errPmodeUnknownPlayer.Error())
		return
	}
	if have, err := strconv.ParseUint(q.Get("v"), 10, 64); err == nil && have == state.V {
		// Nothing new yet: wait for the next change, the end of the wait,
		// the phone hanging up or the party closing.
		wait := pmodePollWait
		if s.waiters.Add(1) > pmodeMaxWaiter {
			// Too many open polls (one phone in many tabs): answer after a
			// pause instead of holding the connection, so they cannot spin.
			wait = time.Second
		}
		ver, changed := s.room.Changed()
		if ver == have {
			timer := time.NewTimer(wait)
			select {
			case <-changed:
			case <-timer.C:
			case <-r.Context().Done():
			case <-s.done:
			}
			timer.Stop()
		}
		s.waiters.Add(-1)
		if r.Context().Err() != nil {
			return
		}
		if state, known = s.room.State(token, time.Now()); !known {
			pmodeFail(w, http.StatusUnauthorized, errPmodeUnknownPlayer.Error())
			return
		}
	}
	pmodeWriteJSON(w, http.StatusOK, state)
}

// pmodePlayError is the status and message for a refused play.
func pmodePlayError(err error) (int, string) {
	switch {
	case errors.Is(err, errPmodeUnknownPlayer):
		return http.StatusUnauthorized, err.Error()
	case errors.Is(err, errPmodeBadChoice):
		return http.StatusBadRequest, err.Error()
	}
	return http.StatusConflict, err.Error()
}

func (s *pmodeServer) handleBuzz(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.gate(w, r)
	if !ok {
		return
	}
	var req struct {
		T string `json:"t"`
	}
	if !pmodeReadBody(w, r, &req) {
		return
	}
	if err := s.room.Buzz(req.T, time.Now()); err != nil {
		status, msg := pmodePlayError(err)
		if status == http.StatusUnauthorized {
			s.noteFail(addr, time.Now())
		}
		pmodeFail(w, status, msg)
		return
	}
	pmodeWriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *pmodeServer) handleAnswer(w http.ResponseWriter, r *http.Request) {
	addr, ok := s.gate(w, r)
	if !ok {
		return
	}
	var req struct {
		T      string `json:"t"`
		Choice *int   `json:"choice"`
	}
	if !pmodeReadBody(w, r, &req) {
		return
	}
	if req.Choice == nil {
		pmodeFail(w, http.StatusBadRequest, errPmodeBadChoice.Error())
		return
	}
	right, err := s.room.Answer(req.T, *req.Choice, time.Now())
	if err != nil {
		status, msg := pmodePlayError(err)
		if status == http.StatusUnauthorized {
			s.noteFail(addr, time.Now())
		}
		pmodeFail(w, status, msg)
		return
	}
	pmodeWriteJSON(w, http.StatusOK, map[string]bool{"ok": true, "right": right})
}

func (s *pmodeServer) handleLeave(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.gate(w, r); !ok {
		return
	}
	var req struct {
		T string `json:"t"`
	}
	if !pmodeReadBody(w, r, &req) {
		return
	}
	s.room.Leave(req.T, time.Now())
	w.WriteHeader(http.StatusNoContent)
}

func (s *pmodeServer) handleImage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.gate(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	round, errR := strconv.Atoi(q.Get("r"))
	stage, errS := strconv.Atoi(q.Get("s"))
	if errR != nil || errS != nil {
		http.NotFound(w, r)
		return
	}
	pic, crop, ok := s.room.ImageFor(q.Get("t"), round, stage, time.Now())
	if !ok {
		// An old stage after the round moved on, or a peek ahead: the page
		// asks again with what the next state names.
		http.NotFound(w, r)
		return
	}
	data, err := s.imageBytes(pic, crop, stage)
	if err != nil {
		http.Error(w, "could not prepare the picture", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	// The URL names one token, round and stage, so the phone may keep it.
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(data)
}

// imageBytes encodes one stage of a cover once; every phone gets the same
// bytes. Only the newest cover's stages are kept.
func (s *pmodeServer) imageBytes(pic *image.NRGBA, crop image.Rectangle, stage int) ([]byte, error) {
	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	if s.imgPic != pic {
		s.imgPic = pic
		s.imgs = map[int][]byte{}
	}
	if data, ok := s.imgs[stage]; ok {
		return data, nil
	}
	sub, ok := pic.SubImage(crop).(*image.NRGBA)
	if !ok || crop.Empty() {
		return nil, errors.New("empty crop")
	}
	w, h := pmodeCropEdge, pmodeCropEdge
	if stage >= pmodeLastStage {
		cw, ch := crop.Dx(), crop.Dy()
		if cw >= ch {
			w, h = pmodeFullEdge, max(1, ch*pmodeFullEdge/cw)
		} else {
			w, h = max(1, cw*pmodeFullEdge/ch), pmodeFullEdge
		}
	}
	// imgvScale samples the nearest pixels when it enlarges, so a tiny crop
	// arrives as crisp blocks — the zoomed-in look — not a blur.
	scaled := imgvScale(sub, w, h)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	data := buf.Bytes()
	s.imgs[stage] = data
	return data, nil
}

// ── Session ──────────────────────────────────────────────────────────────

// pmodeSession is the room and listener of the current Party Mode visit.
var pmodeSession struct {
	mu     sync.Mutex
	room   *pmodeRoom
	server *pmodeServer
	err    string
}

// pmodeCurrent returns the visit's room and server (nil before pmodeRun has
// set them up, and after the screen was left).
func pmodeCurrent() (*pmodeRoom, *pmodeServer, string) {
	pmodeSession.mu.Lock()
	defer pmodeSession.mu.Unlock()
	return pmodeSession.room, pmodeSession.server, pmodeSession.err
}

// pmodeHoldsScreenAwake keeps the idle screen and Auto sleep away while
// players are seated: they play on their phones, not on the handheld.
func pmodeHoldsScreenAwake() bool {
	room, srv, _ := pmodeCurrent()
	return room != nil && srv != nil && srv.Running() && room.PresentPhones(time.Now()) > 0
}

// pmodeRun is the visit's scene task: it opens the room and its listener,
// reads the library into a deck, decodes covers ahead of the rounds and
// ticks the room, until the screen is left.
func pmodeRun(ctx context.Context) {
	room := newPmodeRoom()
	srv := newPmodeServer(room)
	errText := ""
	if err := srv.Start(); err != nil {
		errText = "Could not open Party Mode's port: " + firstLine(err.Error())
		logWarn("partymode", "listen: %v", err)
		srv = nil
	}
	pmodeSession.mu.Lock()
	pmodeSession.room, pmodeSession.server, pmodeSession.err = room, srv, errText
	pmodeSession.mu.Unlock()
	defer func() {
		// Forget the room first: a quick return to the screen must not show
		// this closing room's code while its listener shuts down.
		pmodeSession.mu.Lock()
		if pmodeSession.room == room {
			pmodeSession.room, pmodeSession.server, pmodeSession.err = nil, nil, ""
		}
		pmodeSession.mu.Unlock()
		if srv != nil {
			srv.Stop()
		}
	}()
	if srv == nil {
		<-ctx.Done()
		return
	}
	logInfo("partymode", "room %s open on port %d", room.Code(), srv.Port())

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("partymode", "reading the library failed: %v", r)
				room.SetDeck(nil)
			}
		}()
		lib := buildGameLibraryWith(scanROMLibrary(romLibraryRoots()), libraryArtworkRoot(), map[string]int64{})
		if ctx.Err() != nil {
			return
		}
		deck := pmodeBuildDeck(lib)
		logInfo("partymode", "%d of %d games have box art for Guess the Game", len(deck), lib.Total)
		room.SetDeck(deck)
	}()
	go pmodePictureWorker(ctx, room)

	tick := time.NewTicker(pmodeTickEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			logInfo("partymode", "room %s closed", room.Code())
			return
		case now := <-tick.C:
			room.Tick(now)
		}
	}
}

// pmodePictureWorker decodes the covers the rounds need, one at a time, off
// the render thread, and sleeps until the room changes when none is needed.
// A cover that does not decode marks its round, which then takes another
// game.
func pmodePictureWorker(ctx context.Context, room *pmodeRoom) {
	for {
		_, changed := room.Changed()
		rd, art := room.NeedPicture()
		if rd == nil {
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
			continue
		}
		pic, err := pmodeDecodeCover(ctx, art)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logDebug("partymode", "cover %s: %v", art, err)
			pic = nil
		}
		room.SetPicture(rd, pic, time.Now())
	}
}

// pmodeDecodeCover is decodeLibraryArt with a decoder crash turned into a
// broken cover, so one bad file cannot stop the worker for the visit.
func pmodeDecodeCover(ctx context.Context, art string) (pic *image.NRGBA, err error) {
	defer func() {
		if p := recover(); p != nil {
			logWarn("partymode", "cover decoder crashed on %s: %v", art, p)
			pic, err = nil, errors.New("decoder crashed")
		}
	}()
	return decodeLibraryArt(art, ctx)
}
