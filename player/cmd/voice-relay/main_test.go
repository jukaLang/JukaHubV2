package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dialVoiceTest(t *testing.T, base, room, name string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(base, "http") + "/voice/" + room
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.WriteJSON(map[string]string{"type": "hello", "name": name}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var welcome struct {
		Type string `json:"type"`
		Slot int    `json:"slot"`
	}
	if err := conn.ReadJSON(&welcome); err != nil || welcome.Type != "welcome" || welcome.Slot == 0 {
		t.Fatalf("welcome = %+v, %v", welcome, err)
	}
	return conn
}

func TestVoiceRelayForwardsOnlyWithinRoom(t *testing.T) {
	srv := httptest.NewServer(newRelayServer())
	defer srv.Close()
	roomA := strings.Repeat("a", 32)
	roomB := strings.Repeat("b", 32)
	alex := dialVoiceTest(t, srv.URL, roomA, "Alex")
	var presence struct {
		Type   string `json:"type"`
		People []struct {
			Name string `json:"name"`
		} `json:"people"`
	}
	if err := alex.ReadJSON(&presence); err != nil || len(presence.People) != 1 {
		t.Fatalf("first presence = %+v, %v", presence, err)
	}
	bob := dialVoiceTest(t, srv.URL, roomA, "Bob")
	if err := alex.ReadJSON(&presence); err != nil || len(presence.People) != 2 {
		t.Fatalf("joined presence = %+v, %v", presence, err)
	}
	if err := bob.ReadJSON(&presence); err != nil || len(presence.People) != 2 {
		t.Fatalf("Bob presence = %+v, %v", presence, err)
	}
	other := dialVoiceTest(t, srv.URL, roomB, "Other")
	if err := other.ReadJSON(&presence); err != nil || len(presence.People) != 1 {
		t.Fatalf("other room presence = %+v, %v", presence, err)
	}
	pcm := make([]byte, frameBytes)
	pcm[0], pcm[1] = 0x34, 0x12
	if err := alex.WriteMessage(websocket.BinaryMessage, pcm); err != nil {
		t.Fatal(err)
	}
	kind, packet, err := bob.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || len(packet) != frameBytes+1 ||
		packet[0] == 0 || packet[1] != pcm[0] || packet[2] != pcm[1] {
		t.Fatalf("forwarded packet kind=%d len=%d err=%v", kind, len(packet), err)
	}
	other.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := other.ReadMessage(); err == nil {
		t.Fatal("audio leaked into another room")
	}
}

func TestVoiceRelayRejectsInvalidRoom(t *testing.T) {
	srv := httptest.NewServer(newRelayServer())
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/voice/not-a-room"
	if conn, response, err := websocket.DefaultDialer.Dial(url, nil); err == nil {
		conn.Close()
		t.Fatal("invalid room accepted")
	} else if response == nil || response.StatusCode != 404 {
		t.Fatalf("invalid room status = %v", response)
	}
}

func TestVoiceRelayLimitsTotalClients(t *testing.T) {
	s := newRelayServer()
	first := &relayPeer{name: "First", send: make(chan relayFrame, 32)}
	if !s.join(fmt.Sprintf("%032x", 0), first) {
		t.Fatal("first client rejected")
	}
	for i := 1; i < maxClients; i++ {
		if !s.join(fmt.Sprintf("%032x", i), &relayPeer{name: "Guest", send: make(chan relayFrame, 32)}) {
			t.Fatalf("client %d rejected before limit", i)
		}
	}
	if s.join(fmt.Sprintf("%032x", maxClients), &relayPeer{name: "Extra", send: make(chan relayFrame, 32)}) {
		t.Fatal("client beyond limit accepted")
	}
	s.leave(fmt.Sprintf("%032x", 0), first)
	if !s.join(fmt.Sprintf("%032x", maxClients), &relayPeer{name: "New", send: make(chan relayFrame, 32)}) {
		t.Fatal("slot was not released when client left")
	}
}
