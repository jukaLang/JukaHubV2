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
