package main

// chatvoicechannel.go — native, live voice for Chat. The app captures, mixes
// and plays 20 ms PCM frames through the same SDL devices as the rest of the
// launcher. A call travels either through a free public Mumble server
// (chatvoice_mumble.go: the Voice tab's community room, and private Voice
// Channels when no relay is set) or through a self-hosted JukaHub relay
// (cmd/voice-relay). Discord only carries the invitation message, never the
// call's audio.

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/veandco/go-sdl2/sdl"
)

const (
	chatVoiceRelayKey    = "voiceRelayURL"
	chatVoiceInviteLine  = "Join in JukaHub: "
	chatVoiceFrameBytes  = 640 // 20 ms, 16 kHz mono, signed 16-bit PCM
	chatVoiceMaxMembers  = 8
	chatVoiceAudioQueue  = 256
	chatVoiceSendQueue   = 4
	chatVoiceOutputLimit = micRate * 2 / 5 // 200 ms maximum queued playback
)

var chatVoicePathRE = regexp.MustCompile(`^/voice/[0-9a-f]{32}$`)

type chatVoicePerson struct {
	Slot uint8  `json:"slot"`
	Name string `json:"name"`
	// Muted and Talking are filled in by JukaHub (Mumble rooms and the
	// snapshot), never read from a relay.
	Muted   bool `json:"-"`
	Talking bool `json:"-"`
}

type chatVoicePacket struct {
	slot uint8
	pcm  []byte
}

type chatVoiceSnapshot struct {
	Invite    string
	Status    string
	People    []chatVoicePerson
	Connected bool
	Muted     bool
	OwnSlot   uint8
	StartedAt time.Time
	// Ended: the server refused and the call stopped retrying.
	Ended bool
	// Community: this is the Voice tab's shared room (decided at join, so a
	// settings change mid-call does not relabel it).
	Community bool
}

type chatVoiceCall struct {
	mu           sync.RWMutex
	invite       string
	status       string
	people       []chatVoicePerson
	connected    bool
	speakerError bool
	muted        bool
	ownSlot      uint8
	startedAt    time.Time
	announce     []string
	announced    bool
	ctx          context.Context
	cancel       context.CancelFunc
	send         chan []byte
	receive      chan chatVoicePacket
	// heard is when each person's voice last arrived (by Slot), for the
	// talking dots on the Voice page.
	heard map[uint8]time.Time
	// nameTry and namePlain are touched only by the connection goroutine: a
	// Mumble server that says the name is taken gets a numbered one next
	// time, one that refuses its characters a plain JukaHub-xxxx name.
	nameTry   int
	namePlain bool
	// joinWaits counts connections in a row whose room request got no
	// answer at all (chatvoice_mumble.go reader), so a silent server ends
	// the call instead of retrying forever. Connection goroutine only.
	joinWaits int
	// ended is set (under mu) when the server refused and run gave up;
	// community marks the Voice tab's room.
	ended     bool
	community bool
	// mic and pending are touched only by chatVoiceChannelTick on the UI thread.
	mic     *micStream
	pending []byte
	scratch [micRate / 2]byte
}

// The pointer itself is only changed on the UI thread. Background workers
// retain their own call pointer and never refer to a replacement call.
var chatVoiceCurrent *chatVoiceCall

func init() { Cleanup = append(Cleanup, chatVoiceLeave) }

func chatVoiceSnapshotNow() chatVoiceSnapshot {
	c := chatVoiceCurrent
	if c == nil {
		return chatVoiceSnapshot{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	people := append([]chatVoicePerson(nil), c.people...)
	for i := range people {
		people[i].Talking = time.Since(c.heard[people[i].Slot]) < 350*time.Millisecond
	}
	return chatVoiceSnapshot{Invite: c.invite, Status: c.status,
		People: people, Connected: c.connected,
		Muted: c.muted, OwnSlot: c.ownSlot, StartedAt: c.startedAt, Ended: c.ended, Community: c.community}
}

func (c *chatVoiceCall) setStatus(status string) {
	c.mu.Lock()
	c.status = status
	c.mu.Unlock()
}

func (c *chatVoiceCall) setConnected(connected bool, status string) {
	c.mu.Lock()
	c.connected = connected
	c.status = status
	c.mu.Unlock()
}

// markConnected is the moment a call is live: the status says so and a
// waiting invitation is posted.
func (c *chatVoiceCall) markConnected() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.markConnectedLocked()
}

func (c *chatVoiceCall) markConnectedLocked() {
	c.connected = true
	if c.speakerError {
		c.status = "Listening unavailable — speaker could not open"
	} else {
		c.status = "Connected"
	}
	if !c.announced {
		c.announced = true
		names := append([]string(nil), c.announce...)
		chatOnMain(func(*Config) {
			if chatVoiceCurrent == c {
				postChatMessageFiles(chatDisplayName(), chatVoiceInviteLabel(names)+"\n"+chatVoiceInviteLine+c.invite, nil, true)
			}
		})
	}
}

func (c *chatVoiceCall) markHeard(slot uint8) {
	if slot == 0 {
		return
	}
	c.mu.Lock()
	if c.heard == nil {
		c.heard = map[uint8]time.Time{}
	}
	c.heard[slot] = time.Now()
	c.mu.Unlock()
}

func (c *chatVoiceCall) setPeople(people []chatVoicePerson, own uint8) {
	c.mu.Lock()
	c.people = people
	c.ownSlot = own
	c.mu.Unlock()
}

func chatVoiceInCall() bool { return chatVoiceCurrent != nil }

// chatVoiceRelayBase allows encrypted Internet calls and plain sockets only
// to local/private addresses. An invitation never carries credentials.
func chatVoiceRelayBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.User != nil || u.Hostname() == "" ||
		u.RawQuery != "" || u.Fragment != "" || u.Path != "/voice" {
		return "", errors.New("enter a relay address like wss://voice.example.com/voice")
	}
	if u.Scheme != "wss" {
		if u.Scheme != "ws" || !chatVoiceLocalHost(u.Hostname()) {
			return "", errors.New("Internet voice relays need a secure wss:// address")
		}
	}
	return u.String(), nil
}

func chatVoiceLocalHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}
	return strings.HasSuffix(strings.ToLower(host), ".local") || strings.HasSuffix(strings.ToLower(host), ".lan")
}

func chatVoiceInviteURL(raw string) (string, bool) {
	if server, room, ok := chatVoiceMumbleInvite(raw); ok {
		return chatVoiceMumbleURL(server, room), true
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || !chatVoicePathRE.MatchString(u.Path) {
		return "", false
	}
	base := *u
	base.Path = "/voice"
	if _, err := chatVoiceRelayBase(base.String()); err != nil {
		return "", false
	}
	return u.String(), true
}

func chatVoiceInviteFromMessage(text string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, chatVoiceInviteLine) {
			return chatVoiceInviteURL(strings.TrimPrefix(line, chatVoiceInviteLine))
		}
	}
	return "", false
}

func chatVoiceRelayConfigured(config *Config) string {
	if env := strings.TrimSpace(os.Getenv("JUKAHUB_VOICE_RELAY")); env != "" {
		if base, err := chatVoiceRelayBase(env); err == nil {
			return base
		}
	}
	if config != nil && config.Variables.Custom != nil {
		if raw, ok := config.Variables.Custom[chatVoiceRelayKey].(string); ok {
			if base, err := chatVoiceRelayBase(raw); err == nil {
				return base
			}
		}
	}
	return ""
}

func chatVoiceSetRelay(config *Config, raw string) error {
	if strings.TrimSpace(raw) == "" {
		// No relay: private Voice Channels use the voice server.
		return chatVoiceSetCustom(config, chatVoiceRelayKey, "")
	}
	base, err := chatVoiceRelayBase(raw)
	if err != nil {
		return err
	}
	if config == nil {
		return errors.New("settings are not available")
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[chatVoiceRelayKey] = base
	saveUserConfigDebounced(userConfigFromVariables(config))
	return nil
}

func chatVoiceNewInvite(base string) (string, error) {
	base, err := chatVoiceRelayBase(base)
	if err != nil {
		return "", err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	return base + "/" + hex.EncodeToString(id), nil
}

// chatVoiceStartFor opens a native call and announces the invitation in the
// community conversation. Names select who is asked; the room URL is a secret
// bearer link, visible to anyone who can read that channel. A room on a
// public Mumble server is also listed, by its random name, to everyone on
// that server.
func chatVoiceStartFor(config *Config, names []string) {
	if snap := chatVoiceSnapshotNow(); snap.Invite != "" && !snap.Ended && !snap.Community {
		// Already in a private call: ask these people into it.
		if len(names) > 0 {
			postChatMessageFiles(chatDisplayName(), chatVoiceInviteLabel(names)+"\n"+chatVoiceInviteLine+snap.Invite, nil, true)
		}
		chatVoiceOpenScene(config)
		return
	}
	// A new private room (the voice room, if joined, is left for it).
	var invite string
	var err error
	if base := chatVoiceRelayConfigured(config); base != "" {
		invite, err = chatVoiceNewInvite(base)
	} else {
		// No relay of one's own: a private room on the free voice server.
		invite, err = chatVoiceNewPrivateInvite(config)
	}
	if err != nil {
		showToast("Could not start the Voice Channel", ToastWarn())
		return
	}
	chatVoiceJoinWithAnnouncement(invite, names)
	chatVoiceOpenScene(config)
}

func chatVoicePromptRelay(config *Config, after func()) {
	seed := chatVoiceRelayConfigured(config)
	tbxOpenPrompt("Voice relay address", "wss://voice.example.com/voice", seed, func(value string) {
		if err := chatVoiceSetRelay(config, value); err != nil {
			showToast(err.Error(), ToastWarn())
			return
		}
		if after != nil {
			after()
		}
	})
}

func chatVoiceJoin(invite string) bool {
	return chatVoiceJoinInternal(invite, nil, false)
}

func chatVoiceJoinWithAnnouncement(invite string, names []string) bool {
	return chatVoiceJoinInternal(invite, names, true)
}

func chatVoiceJoinInternal(invite string, names []string, announce bool) bool {
	valid, ok := chatVoiceInviteURL(invite)
	if !ok {
		showToast("This Voice Channel invite is not valid", ToastWarn())
		return false
	}
	if snap := chatVoiceSnapshotNow(); chatVoiceCurrent != nil && snap.Invite == valid && !snap.Ended {
		return true
	}
	sendQueue := chatVoiceSendQueue
	if server, _, ok := chatVoiceMumbleInvite(valid); ok {
		if server != chatVoiceServerConfigured(appConfig) {
			// Everyone who should meet uses the server chosen in settings; an
			// invitation for another one is not followed silently.
			showToast("This invitation uses another voice server ("+server+"). Pick it under Voice → Voice server to join.", ToastWarn())
			return false
		}
		sendQueue = chatVoiceSendQueueMumble
	}
	if prev := chatVoiceSnapshotNow(); prev.Invite != "" && prev.Invite != valid {
		if prev.Community {
			showToast("Left the voice room", ToastInfo())
		} else {
			showToast("Left the previous Voice Channel", ToastInfo())
		}
	}
	chatVoiceLeave()
	ctx, cancel := context.WithCancel(context.Background())
	c := &chatVoiceCall{invite: valid, status: "Connecting…", startedAt: time.Now(),
		ctx: ctx, cancel: cancel, send: make(chan []byte, sendQueue),
		receive: make(chan chatVoicePacket, chatVoiceAudioQueue), announced: !announce,
		announce: append([]string(nil), names...), community: valid == chatVoiceCommunityInvite(appConfig)}
	chatVoiceCurrent = c
	go c.run(chatDisplayName())
	go c.playback()
	return true
}

// chatVoiceRetry starts a refused call again: the voice room by the current
// settings, a private channel with its invitation still owed.
func chatVoiceRetry(config *Config) {
	c := chatVoiceCurrent
	if c == nil {
		return
	}
	c.mu.RLock()
	invite, community, announced := c.invite, c.community, c.announced
	names := append([]string(nil), c.announce...)
	c.mu.RUnlock()
	if community {
		chatVoiceJoinCommunity(config)
		return
	}
	chatVoiceJoinInternal(invite, names, !announced)
}

func chatVoiceLeave() {
	c := chatVoiceCurrent
	if c == nil {
		return
	}
	chatVoiceCurrent = nil
	c.cancel()
	if c.mic != nil {
		c.mic.Close()
		c.mic = nil
	}
}

// Other voice features get the microphone immediately. The live channel
// resumes capture after they finish, while remote audio remains audible.
func chatVoicePauseCapture() {
	c := chatVoiceCurrent
	if c == nil || c.mic == nil {
		return
	}
	c.mic.Close()
	c.mic = nil
	c.pending = nil
}

func chatVoiceToggleMute() {
	c := chatVoiceCurrent
	if c == nil {
		return
	}
	c.mu.Lock()
	c.muted = !c.muted
	muted := c.muted
	c.mu.Unlock()
	if muted && c.mic != nil {
		c.mic.Close()
		c.mic = nil
		c.pending = nil
	}
	if muted {
		chatVoiceDiscardSend(c.send)
	}
}

func chatVoiceDiscardSend(send chan []byte) {
	for {
		select {
		case <-send:
		default:
			return
		}
	}
}

func (c *chatVoiceCall) snapshotConnected() (connected, muted bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected, c.muted
}

// Called each frame on the UI thread. Capture is not tied to the Voice
// Channel scene: people can return to Chat while still talking.
func chatVoiceChannelTick() {
	c := chatVoiceCurrent
	if c == nil {
		return
	}
	connected, muted := c.snapshotConnected()
	if !connected || muted || chatVoiceRecording() || vkVoiceActive() ||
		(c.mic == nil && micVoiceUsers.Load() > 0) {
		if c.mic != nil {
			c.mic.Close()
			c.mic = nil
			c.pending = nil
		}
		if !connected || muted {
			chatVoiceDiscardSend(c.send)
		}
		return
	}
	if c.mic == nil {
		mic, err := micOpen()
		if err != nil {
			c.mu.Lock()
			c.status = "Listening only — microphone unavailable"
			c.muted = true
			c.mu.Unlock()
			return
		}
		c.mic = mic
	}
	for i := 0; i < 4; i++ {
		n := c.mic.Read(c.scratch[:])
		if n <= 0 {
			break
		}
		c.pending = append(c.pending, c.scratch[:n]...)
		for len(c.pending) >= chatVoiceFrameBytes {
			frame := append([]byte(nil), c.pending[:chatVoiceFrameBytes]...)
			c.pending = c.pending[chatVoiceFrameBytes:]
			select {
			case c.send <- frame:
			default:
				// Keep the newest speech; an old packet adds more delay than a
				// dropped packet costs in an interactive call.
				select {
				case <-c.send:
				default:
				}
				select {
				case c.send <- frame:
				default:
				}
			}
		}
	}
}

func (c *chatVoiceCall) run(name string) {
	backoff := time.Second
	for c.ctx.Err() == nil {
		connectedAt := time.Now()
		err := c.connect(name)
		if c.ctx.Err() != nil {
			return
		}
		if time.Since(connectedAt) >= 10*time.Second {
			backoff = time.Second
		}
		c.mu.Lock()
		c.connected = false
		c.people = nil
		c.ownSlot = 0
		c.status = "Reconnecting…"
		var closeErr *websocket.CloseError
		if errors.As(err, &closeErr) && closeErr.Code == websocket.ClosePolicyViolation {
			c.status = "Channel full or relay unavailable — retrying"
		}
		var said chatVoiceErr
		stopped := false
		if errors.As(err, &said) {
			c.status, stopped = said.msg, said.stop
		}
		c.ended = stopped
		c.mu.Unlock()
		if err != nil {
			logWarn("chat", "voice channel disconnected: %v", err)
		}
		if stopped {
			// The server refuses; retrying cannot help. Cancelling closes the
			// speaker too; the call stays listed until Leave or Try again.
			c.cancel()
			return
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
}

func (c *chatVoiceCall) connect(name string) error {
	if server, room, ok := chatVoiceMumbleInvite(c.invite); ok {
		return c.connectMumble(server, room, name)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(c.ctx, c.invite, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	closeOnCancel := make(chan struct{})
	go func() {
		select {
		case <-c.ctx.Done():
			conn.Close()
		case <-closeOnCancel:
		}
	}()
	defer close(closeOnCancel)
	if err := conn.WriteJSON(map[string]string{"type": "hello", "name": name}); err != nil {
		return err
	}
	chatVoiceDiscardSend(c.send)
	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	writerDone := make(chan struct{})
	stopWriter := make(chan struct{})
	go func() {
		defer close(writerDone)
		ping := time.NewTicker(20 * time.Second)
		pace := time.NewTicker(20 * time.Millisecond)
		defer ping.Stop()
		defer pace.Stop()
		for {
			select {
			case <-pace.C:
				var frame []byte
				select {
				case frame = <-c.send:
				default:
					continue
				}
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if conn.WriteMessage(websocket.BinaryMessage, frame) != nil {
					conn.Close()
					return
				}
			case <-ping.C:
				if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
					conn.Close()
					return
				}
			case <-stopWriter:
				return
			}
		}
	}()
	var readErr error
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			readErr = err
			break
		}
		switch kind {
		case websocket.TextMessage:
			c.readStatus(data)
		case websocket.BinaryMessage:
			if len(data) != chatVoiceFrameBytes+1 || data[0] == 0 || data[0] > chatVoiceMaxMembers {
				continue
			}
			packet := chatVoicePacket{slot: data[0], pcm: append([]byte(nil), data[1:]...)}
			select {
			case c.receive <- packet:
			default:
			}
		}
	}
	close(stopWriter)
	conn.Close()
	<-writerDone
	return readErr
}

func (c *chatVoiceCall) readStatus(data []byte) {
	var msg struct {
		Type   string            `json:"type"`
		Slot   uint8             `json:"slot"`
		People []chatVoicePerson `json:"people"`
	}
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch msg.Type {
	case "welcome":
		c.ownSlot = msg.Slot
		c.markConnectedLocked()
	case "presence":
		c.people = append([]chatVoicePerson(nil), msg.People...)
	}
}

func (c *chatVoiceCall) playback() {
	want := sdl.AudioSpec{Freq: micRate, Format: sdl.AUDIO_S16SYS, Channels: 1, Samples: 512}
	var got sdl.AudioSpec
	device, err := outOpen(&want, &got, 0)
	if err != nil || device == 0 {
		c.mu.Lock()
		c.speakerError = true
		c.status = "Listening unavailable — speaker could not open"
		c.mu.Unlock()
		return
	}
	defer outClose(device)
	outPause(device, false)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var queues [chatVoiceMaxMembers + 1][][]byte
	var warm [chatVoiceMaxMembers + 1]bool
	for {
		select {
		case <-c.ctx.Done():
			return
		case pkt := <-c.receive:
			q := &queues[pkt.slot]
			*q = append(*q, pkt.pcm)
			if len(*q) > 8 {
				*q = (*q)[len(*q)-8:]
			}
		case <-ticker.C:
			mixed := chatVoiceMixFrame(&queues, &warm)
			if mixed == nil {
				continue
			}
			if outQueued(device) > chatVoiceOutputLimit {
				outClear(device)
			}
			_ = outQueue(device, mixed)
		}
	}
}

// One frame from each speaker is mixed with saturation. A two-frame lead
// absorbs ordinary Wi-Fi jitter without letting old speech pile up.
func chatVoiceMixFrame(queues *[chatVoiceMaxMembers + 1][][]byte, warm *[chatVoiceMaxMembers + 1]bool) []byte {
	var sum [chatVoiceFrameBytes / 2]int32
	active := false
	for slot := 1; slot <= chatVoiceMaxMembers; slot++ {
		q := &queues[slot]
		if len(*q) >= 2 {
			warm[slot] = true
		}
		if len(*q) == 0 {
			// Ran dry (a pause, or the end of a talk spurt): the next sound
			// waits for the two-frame lead again.
			warm[slot] = false
			continue
		}
		if !warm[slot] {
			continue
		}
		frame := (*q)[0]
		*q = (*q)[1:]
		active = true
		for i := 0; i < len(sum); i++ {
			sum[i] += int32(int16(binary.LittleEndian.Uint16(frame[i*2:])))
		}
	}
	if !active {
		return nil
	}
	out := make([]byte, chatVoiceFrameBytes)
	for i, sample := range sum {
		if sample > 32767 {
			sample = 32767
		} else if sample < -32768 {
			sample = -32768
		}
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(sample)))
	}
	return out
}

func chatVoiceInviteLabel(names []string) string {
	if len(names) == 0 {
		return "Voice Channel is open"
	}
	return fmt.Sprintf("Voice Channel for %s", strings.Join(names, ", "))
}
