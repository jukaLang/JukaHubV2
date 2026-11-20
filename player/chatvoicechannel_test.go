package main

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestChatVoiceInvitesRequireSecureRelayAndRoom(t *testing.T) {
	base := "wss://voice.example.com/voice"
	invite, err := chatVoiceNewInvite(base)
	if err != nil || !strings.HasPrefix(invite, base+"/") {
		t.Fatalf("invite = %q, %v", invite, err)
	}
	if parsed, ok := chatVoiceInviteFromMessage("Voice Channel for Alex\n" + chatVoiceInviteLine + invite); !ok || parsed != invite {
		t.Fatalf("message invite = %q, %v", parsed, ok)
	}
	if second, err := chatVoiceNewInvite(base); err != nil || second == invite {
		t.Fatalf("second invite = %q, %v", second, err)
	}
	for _, raw := range []string{
		"ws://voice.example.com/voice",
		"https://voice.example.com/voice",
		"wss://user:pass@voice.example.com/voice",
		"wss://voice.example.com/voice?token=x",
		"wss://voice.example.com/other",
	} {
		if _, err := chatVoiceRelayBase(raw); err == nil {
			t.Errorf("accepted invalid relay %q", raw)
		}
	}
	if base, err := chatVoiceRelayBase("ws://127.0.0.1:8790/voice"); err != nil || base == "" {
		t.Fatalf("local relay = %q, %v", base, err)
	}
	if _, ok := chatVoiceInviteURL("wss://voice.example.com/voice/not-a-room"); ok {
		t.Fatal("accepted invalid room")
	}
}

func TestChatVoiceMixerWaitsForJitterAndClips(t *testing.T) {
	var queues [chatVoiceMaxMembers + 1][][]byte
	var warm [chatVoiceMaxMembers + 1]bool
	positive := make([]byte, chatVoiceFrameBytes)
	negative := make([]byte, chatVoiceFrameBytes)
	negativeSample := int16(-10000)
	for i := 0; i < chatVoiceFrameBytes; i += 2 {
		binary.LittleEndian.PutUint16(positive[i:], uint16(int16(25000)))
		binary.LittleEndian.PutUint16(negative[i:], uint16(negativeSample))
	}
	queues[1] = append(queues[1], positive)
	if got := chatVoiceMixFrame(&queues, &warm); got != nil {
		t.Fatal("played before jitter lead")
	}
	queues[1] = append(queues[1], positive)
	queues[2] = append(queues[2], negative, negative)
	frame := chatVoiceMixFrame(&queues, &warm)
	if len(frame) != chatVoiceFrameBytes || int16(binary.LittleEndian.Uint16(frame)) != 15000 {
		t.Fatalf("mixed sample = %d", int16(binary.LittleEndian.Uint16(frame)))
	}
	queues[2] = nil
	queues[1] = append(queues[1], positive)
	frame = chatVoiceMixFrame(&queues, &warm)
	if int16(binary.LittleEndian.Uint16(frame)) != 25000 {
		t.Fatalf("single speaker sample = %d", int16(binary.LittleEndian.Uint16(frame)))
	}
	queues[2] = append(queues[2], positive, positive)
	frame = chatVoiceMixFrame(&queues, &warm)
	if int16(binary.LittleEndian.Uint16(frame)) != 32767 {
		t.Fatalf("clipped sample = %d", int16(binary.LittleEndian.Uint16(frame)))
	}
}

func TestChatVoiceMumbleParsersRejectBadInput(t *testing.T) {
	invite := chatVoiceMumbleURL(chatVoiceDefaultServer, chatVoiceDefaultRoom)
	if server, room, ok := chatVoiceMumbleInvite(invite); !ok || server != chatVoiceDefaultServer || room != chatVoiceDefaultRoom {
		t.Fatalf("invite %q = %q %q %v", invite, server, room, ok)
	}
	if got, ok := chatVoiceInviteFromMessage("Voice Channel is open\n" + chatVoiceInviteLine + invite); !ok || got != invite {
		t.Fatalf("message invite = %q, %v", got, ok)
	}
	for _, raw := range []string{
		"mumble://user:pw@mumble.example.com/Room",
		"mumble://mumble.example.com:99999/Room",
		"mumble://mumble.example.com/",
		"mumble://mumble.example.com/Room/../../x",
		"mumble://mumble.example.com/" + strings.Repeat("a", 65),
		"http://mumble.example.com/Room",
	} {
		if _, _, ok := chatVoiceMumbleInvite(raw); ok {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, v := range []uint64{0, 0x7f, 0x80, 0x3fff, 0x4000, 0x1fffff, 0x200000, 0xfffffff, 0x10000000, 0xffffffff, 1 << 40} {
		b := mumbleAppendVarint(nil, v)
		if got, n := mumbleReadVarint(b); got != v || n != len(b) {
			t.Errorf("varint %#x: got %#x, %d of %d bytes", v, got, n, len(b))
		}
		if _, n := mumbleReadVarint(b[:len(b)-1]); len(b) > 1 && n != 0 {
			t.Errorf("varint %#x: short read accepted", v)
		}
	}
	// A server's packet: header, session 7, sequence 300, three Opus bytes
	// that end the talk spurt.
	pkt := []byte{mumbleVoiceOpus << 5}
	pkt = mumbleAppendVarint(pkt, 7)
	pkt = mumbleAppendVarint(pkt, 300)
	pkt = mumbleAppendVarint(pkt, 3|mumbleOpusTerminator)
	pkt = append(pkt, 1, 2, 3)
	v, ok := mumbleParseVoice(pkt)
	if !ok || v.session != 7 || string(v.opus) != "\x01\x02\x03" || !v.last {
		t.Fatalf("voice = %+v, %v", v, ok)
	}
	if _, ok := mumbleParseVoice(pkt[:len(pkt)-1]); ok {
		t.Error("accepted a cut voice packet")
	}
	if pbEach([]byte{0x1a, 0x05, 'a'}, func(pbField) {}) == nil {
		t.Error("accepted a field longer than the message")
	}
}
