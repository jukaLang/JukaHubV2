// voice-relay is the small Internet service for JukaHub Voice Channels. Put it
// behind a TLS reverse proxy and point JukaHub's voiceRelayURL at its wss URL.
// It forwards live audio frames between people in the same unguessable room;
// it does not store, inspect or decode recordings.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	frameBytes   = 640 // 20 ms of 16 kHz mono signed 16-bit PCM
	maxMembers   = 8
	maxRooms     = 256
	maxClients   = 256
	maxFramesSec = 75
)

var roomPath = regexp.MustCompile(`^/voice/([0-9a-f]{32})$`)

type relayFrame struct {
	kind int
	data []byte
}

type relayPeer struct {
	slot uint8
	name string
	conn *websocket.Conn
	send chan relayFrame
}

type relayRoom struct {
	peers map[uint8]*relayPeer
}

type relayServer struct {
	mu      sync.Mutex
	rooms   map[string]*relayRoom
	clients int
}

func newRelayServer() *relayServer { return &relayServer{rooms: make(map[string]*relayRoom)} }

func cleanName(raw string) string {
	var out strings.Builder
	for _, r := range raw {
		if r >= 32 && r != 127 && out.Len() < 40 {
			out.WriteRune(r)
		}
	}
	name := strings.TrimSpace(out.String())
	if name == "" {
		return "Guest"
	}
	return name
}

func (s *relayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	parts := roomPath.FindStringSubmatch(r.URL.Path)
	if r.Method != http.MethodGet || len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	// Only JukaHub's native client uses the service. A browser page must not
	// open a socket using an invite that happened to be pasted into it.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "Native client only", http.StatusForbidden)
		return
	}
	upgrader := websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024,
		CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1024)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, hello, err := conn.ReadMessage()
	if err != nil || kind != websocket.TextMessage {
		return
	}
	var greeting struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(hello, &greeting) != nil || greeting.Type != "hello" {
		return
	}
	peer := &relayPeer{name: cleanName(greeting.Name), conn: conn, send: make(chan relayFrame, 32)}
	roomID := parts[1]
	if !s.join(roomID, peer) {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "room full or unavailable"),
			time.Now().Add(time.Second))
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		peer.writeLoop()
	}()
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	window, frames := time.Now(), 0
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if kind != websocket.BinaryMessage || len(data) != frameBytes {
			break
		}
		if time.Since(window) >= time.Second {
			window, frames = time.Now(), 0
		}
		frames++
		if frames > maxFramesSec {
			break
		}
		s.forward(roomID, peer.slot, data)
	}
	s.leave(roomID, peer)
	close(peer.send)
	<-done
}

func (s *relayServer) join(id string, peer *relayPeer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients >= maxClients {
		return false
	}
	room := s.rooms[id]
	if room == nil {
		if len(s.rooms) >= maxRooms {
			return false
		}
		room = &relayRoom{peers: make(map[uint8]*relayPeer)}
		s.rooms[id] = room
	}
	if len(room.peers) >= maxMembers {
		return false
	}
	for slot := uint8(1); slot <= maxMembers; slot++ {
		if room.peers[slot] == nil {
			peer.slot = slot
			break
		}
	}
	room.peers[peer.slot] = peer
	s.clients++
	peer.send <- relayFrame{kind: websocket.TextMessage, data: []byte(`{"type":"welcome","slot":` + itoa(int(peer.slot)) + `}`)}
	s.presenceLocked(room)
	return true
}

func (s *relayServer) leave(id string, peer *relayPeer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.rooms[id]
	if room == nil || room.peers[peer.slot] != peer {
		return
	}
	delete(room.peers, peer.slot)
	s.clients--
	if len(room.peers) == 0 {
		delete(s.rooms, id)
		return
	}
	s.presenceLocked(room)
}

func (s *relayServer) presenceLocked(room *relayRoom) {
	type participant struct {
		Slot uint8  `json:"slot"`
		Name string `json:"name"`
	}
	msg := struct {
		Type   string        `json:"type"`
		People []participant `json:"people"`
	}{Type: "presence", People: make([]participant, 0, len(room.peers))}
	for slot := uint8(1); slot <= maxMembers; slot++ {
		if peer := room.peers[slot]; peer != nil {
			msg.People = append(msg.People, participant{Slot: slot, Name: peer.name})
		}
	}
	data, _ := json.Marshal(msg)
	for _, peer := range room.peers {
		select {
		case peer.send <- relayFrame{kind: websocket.TextMessage, data: data}:
		default:
		}
	}
}

func (s *relayServer) forward(id string, sender uint8, pcm []byte) {
	packet := make([]byte, 1+frameBytes)
	packet[0] = sender
	copy(packet[1:], pcm)
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.rooms[id]
	if room == nil {
		return
	}
	for slot, peer := range room.peers {
		if slot == sender {
			continue
		}
		select {
		case peer.send <- relayFrame{kind: websocket.BinaryMessage, data: packet}:
		default: // a slow listener loses old audio instead of delaying everyone
		}
	}
}

func (p *relayPeer) writeLoop() {
	defer p.conn.Close()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case frame, ok := <-p.send:
			if !ok {
				return
			}
			_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if p.conn.WriteMessage(frame.kind, frame.data) != nil {
				return
			}
		case <-ping.C:
			if p.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}

func itoa(n int) string { return string(rune('0' + n)) }

func main() {
	addr := os.Getenv("JUKAHUB_VOICE_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8790"
	}
	server := &http.Server{Addr: addr, Handler: newRelayServer(), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("JukaHub voice relay listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}
