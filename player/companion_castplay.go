package main

// companion_castplay.go — the viewer page's /play socket: Screen Share's
// sound to the viewer (castaudio.go), the viewer's presses back
// (castinput.go).
//
// One WebSocket per page, opened when the viewer switches Sound on or presses
// Play, behind the same guard as the picture (the link's token or the Phone
// Companion's PIN). The page speaks JSON:
//
//	{"t":"hello","key":"<the page's id>"}    first, within 10 seconds
//	{"t":"sound","on":true|false}            start or stop the audio blocks
//	{"t":"play"} / {"t":"stop"}              ask to play / stop playing
//	{"t":"pad","b":<bits>,"lx":…,"ly":…,"rx":…,"ry":…,"lt":…,"rt":…}
//
// and hears {"t":"hi",…} once, {"t":"ctl","state":…} whenever its turn
// changes, and the audio blocks as binary messages. Only this goroutine and
// its writer touch the socket: the writer alone writes, as the library
// requires. The socket ends when the page leaves, the link is replaced,
// sharing stops or the server that took it stops (sockets taken over from
// the HTTP server are not closed by its shutdown); every end lets go of the
// viewer's buttons.

import (
	"encoding/json"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// castPlayMax caps the sockets at once, like the streams.
	castPlayMax = castMaxViewers
	// castPlayReadLimit bounds one message from a page.
	castPlayReadLimit = 4 << 10
	// castPlayHelloWait is how long the page has to say hello.
	castPlayHelloWait = 10 * time.Second
	// castPlayIdle ends a socket that heard nothing, not even a pong, for so
	// long; castPlayPing keeps a quiet one alive well inside it.
	castPlayIdle = 45 * time.Second
	castPlayPing = 15 * time.Second
	// castPlayWriteWait drops a viewer whose network cannot take a message.
	castPlayWriteWait = 5 * time.Second
)

var castPlayUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	ReadBufferSize:   1024,
	WriteBufferSize:  4096,
	// The default origin check stays: only a page from this server opens it.
}

// castPlaySockets are the open sockets, so sharing switched off can close
// them all.
var castPlaySockets struct {
	mu    sync.Mutex
	conns map[*websocket.Conn]struct{}
}

// castPlayAdd registers conn; false when the cap is reached.
func castPlayAdd(conn *websocket.Conn) bool {
	castPlaySockets.mu.Lock()
	defer castPlaySockets.mu.Unlock()
	if len(castPlaySockets.conns) >= castPlayMax {
		return false
	}
	if castPlaySockets.conns == nil {
		castPlaySockets.conns = make(map[*websocket.Conn]struct{})
	}
	castPlaySockets.conns[conn] = struct{}{}
	return true
}

func castPlayRemove(conn *websocket.Conn) {
	castPlaySockets.mu.Lock()
	delete(castPlaySockets.conns, conn)
	castPlaySockets.mu.Unlock()
}

// castPlayCloseAll closes every socket; each one's goroutine then cleans up.
func castPlayCloseAll() {
	castPlaySockets.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(castPlaySockets.conns))
	for c := range castPlaySockets.conns {
		conns = append(conns, c)
	}
	castPlaySockets.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// castPlayMsg is one message from the page.
type castPlayMsg struct {
	T   string  `json:"t"`
	Key string  `json:"key"`
	On  bool    `json:"on"`
	B   uint32  `json:"b"`
	LX  float64 `json:"lx"`
	LY  float64 `json:"ly"`
	RX  float64 `json:"rx"`
	RY  float64 `json:"ry"`
	LT  float64 `json:"lt"`
	RT  float64 `json:"rt"`
}

// state is the message's pad state, every value in its range.
func (m castPlayMsg) state() castPadState {
	clamp := func(v, lo float64) float64 {
		if math.IsNaN(v) {
			return 0
		}
		return max(lo, min(1, v))
	}
	return castPadState{
		buttons: m.B & (1<<castBtnCount - 1),
		lx:      clamp(m.LX, -1),
		ly:      clamp(m.LY, -1),
		rx:      clamp(m.RX, -1),
		ry:      clamp(m.RY, -1),
		lt:      clamp(m.LT, 0),
		rt:      clamp(m.RT, 0),
	}
}

// castPlayHi is the first word to the page: what the handheld allows.
type castPlayHi struct {
	T       string `json:"t"`
	Sound   bool   `json:"sound"`
	Control bool   `json:"control"`
	Games   bool   `json:"games"`
}

// castPlayCtl tells the page about its turn.
type castPlayCtl struct {
	T     string `json:"t"`
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
	Games bool   `json:"games"`
}

// handlePlay serves /play.
func (s *companionServer) handlePlay(w http.ResponseWriter, r *http.Request, cred castCredential) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !castEnabled.Load() {
		http.Error(w, "Screen Share is off on the handheld", http.StatusForbidden)
		return
	}
	conn, err := castPlayUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader answered the request
	}
	defer conn.Close()
	if !castPlayAdd(conn) {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "Screen Share is full"), time.Now().Add(time.Second))
		return
	}
	defer castPlayRemove(conn)
	conn.SetReadLimit(castPlayReadLimit)

	// The hello names the page, so a reconnect is the same viewer.
	_ = conn.SetReadDeadline(time.Now().Add(castPlayHelloWait))
	var hello castPlayMsg
	if kind, data, err := conn.ReadMessage(); err != nil || kind != websocket.TextMessage ||
		json.Unmarshal(data, &hello) != nil || hello.T != "hello" {
		return
	}
	client := castCtlNewClient(hello.Key, r.UserAgent(), r.RemoteAddr, cred.token == "", cred.token)
	defer castInputLeave(client)

	sound := make(chan bool, 4)
	done := make(chan struct{})
	wrote := make(chan struct{})
	go castPlayWriter(s, conn, cred, client, sound, done, wrote)
	defer func() {
		close(done)
		<-wrote
	}()

	_ = conn.SetReadDeadline(time.Now().Add(castPlayIdle))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(castPlayIdle)) })
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(castPlayIdle))
		if kind != websocket.TextMessage {
			continue
		}
		var m castPlayMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.T {
		case "sound":
			select {
			case sound <- m.On:
			default:
			}
		case "play":
			castInputRequest(client)
		case "stop":
			castInputLeave(client)
		case "pad":
			castInputApply(client, m.state())
		}
	}
}

// castPlayWriter is the socket's only writer: the first word, the turn's
// events, the audio blocks while the page wants them, and the pings; it
// closes the socket when the link, the share or the server goes.
func castPlayWriter(s *companionServer, conn *websocket.Conn, cred castCredential, c *castCtlClient,
	sound <-chan bool, done <-chan struct{}, wrote chan<- struct{}) {
	defer close(wrote)
	var listener *castAudioListener
	var audio <-chan []byte
	defer func() {
		castAudioUnlisten(listener)
	}()
	ping := time.NewTicker(castPlayPing)
	defer ping.Stop()
	check := time.NewTicker(time.Second)
	defer check.Stop()
	send := func(kind int, data []byte) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(castPlayWriteWait))
		if conn.WriteMessage(kind, data) != nil {
			_ = conn.Close()
			return false
		}
		return true
	}
	sendJSON := func(v any) bool {
		data, err := json.Marshal(v)
		return err == nil && send(websocket.TextMessage, data)
	}
	if !sendJSON(castPlayHi{T: "hi", Sound: castLive.sound.Load(), Control: castLive.control.Load(), Games: castPadReachesGames()}) {
		return
	}
	for {
		select {
		case <-done:
			return
		case on := <-sound:
			switch {
			case on && listener == nil:
				listener = castAudioListen()
				audio = listener.ch
			case !on && listener != nil:
				castAudioUnlisten(listener)
				listener, audio = nil, nil
			}
		case block := <-audio:
			if !send(websocket.BinaryMessage, block) {
				return
			}
		case ev := <-c.events:
			if !sendJSON(castPlayCtl{T: "ctl", State: ev.State, Note: ev.Note, Games: ev.Games}) {
				return
			}
		case <-ping.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(castPlayWriteWait)) != nil {
				_ = conn.Close()
				return
			}
		case <-check.C:
			if !castEnabled.Load() || !cred.stillValid() || !s.Running() {
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Screen Share stopped"), time.Now().Add(time.Second))
				_ = conn.Close()
				return
			}
		}
	}
}
