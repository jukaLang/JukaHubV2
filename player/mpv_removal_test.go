package main

// Guards for the removal of the MPV and ffplay backends: playback is the
// built-in player fed by ffmpeg, so no shipped config may advertise another
// audio backend, and old configs that still contain the retired key must load
// cleanly and drop it.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestShippedConfigsHaveNoMPVSettings(t *testing.T) {
	for _, path := range []string{"jukaconfig.json", "trimui/jukaconfig.device.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !json.Valid(data) {
			t.Fatalf("%s is not valid JSON", path)
		}
		lower := strings.ToLower(string(data))
		for _, banned := range []string{"mpv", "audiobackend", "ffplay"} {
			if strings.Contains(lower, banned) {
				t.Errorf("%s still mentions %q", path, banned)
			}
		}
		if !strings.Contains(string(data), "ffmpeg (built in)") {
			t.Errorf("%s should describe playback as ffmpeg (built in)", path)
		}
	}
}

func TestLegacyAudioBackendKeyIsDropped(t *testing.T) {
	const legacy = `{
		"audioBackend": "mpv",
		"AudioBackend": "mpv",
		"playbackResolution": "720",
		"custom": {}
	}`
	var v Variables
	if err := json.Unmarshal([]byte(legacy), &v); err != nil {
		t.Fatalf("unmarshal legacy variables: %v", err)
	}
	if v.PlaybackResolution != "720" {
		t.Errorf("playbackResolution = %q, want \"720\" (other keys must still load)", v.PlaybackResolution)
	}
	for _, key := range []string{"audioBackend", "AudioBackend"} {
		if _, ok := v.Custom[key]; ok {
			t.Errorf("retired key %q was kept as a custom variable", key)
		}
	}
	if got := v.Get("audioBackend"); got != "" {
		t.Errorf("$audioBackend still resolves to %q", got)
	}
}
