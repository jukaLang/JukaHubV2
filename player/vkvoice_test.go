package main

// Tests for voice typing (vkvoice.go): the WAV the recorder builds, the
// request Groq receives, the states the strip shows instead of failing
// silently, and how a transcript lands in the field.

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withVoiceSetup prepares a chat keyboard, a config with (or without) a Groq
// key, and a known microphone state; everything is restored afterwards.
func withVoiceSetup(t *testing.T, key string) *Config {
	t.Helper()
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := withKeyboardConfig(t)
	if key != "" {
		cfg.Variables.Custom["groq_api_key"] = key
	}
	prevMic := vkMic.Load()
	vkMic.Store(vkMicPresent)
	t.Cleanup(func() {
		vkMic.Store(prevMic)
		vkVoiceCancel()
	})
	vkTarget = vkTargetChat
	chatInputText = ""
	vkEnsureLayout()
	return cfg
}

// withNetwork pins the connectivity monitor's answer for one test.
func withNetwork(t *testing.T, online bool) {
	t.Helper()
	netMon.mu.Lock()
	prevChecked, prevOnline := netMon.checked, netMon.online
	netMon.checked, netMon.online = true, online
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.checked, netMon.online = prevChecked, prevOnline
		netMon.mu.Unlock()
	})
}

func TestVKEncodeWAVHeader(t *testing.T) {
	pcm := []byte{1, 0, 2, 0, 3, 0, 4, 0}
	wav := vkEncodeWAV(pcm, vkVoiceRate)
	if len(wav) != 44+len(pcm) {
		t.Fatalf("WAV is %d bytes, want %d", len(wav), 44+len(pcm))
	}
	checks := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"riff size", binary.LittleEndian.Uint32(wav[4:]), uint32(36 + len(pcm))},
		{"fmt size", binary.LittleEndian.Uint32(wav[16:]), 16},
		{"format", uint32(binary.LittleEndian.Uint16(wav[20:])), 1},
		{"channels", uint32(binary.LittleEndian.Uint16(wav[22:])), 1},
		{"rate", binary.LittleEndian.Uint32(wav[24:]), vkVoiceRate},
		{"byte rate", binary.LittleEndian.Uint32(wav[28:]), vkVoiceRate * 2},
		{"block align", uint32(binary.LittleEndian.Uint16(wav[32:])), 2},
		{"bits", uint32(binary.LittleEndian.Uint16(wav[34:])), 16},
		{"data size", binary.LittleEndian.Uint32(wav[40:]), uint32(len(pcm))},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[12:16]) != "fmt " || string(wav[36:40]) != "data" {
		t.Fatal("WAV chunk tags are wrong")
	}
	if string(wav[44:]) != string(pcm) {
		t.Fatal("WAV payload differs from the PCM")
	}
}

func TestVKPCMLevel(t *testing.T) {
	if got := vkPCMLevel(make([]byte, 64)); got != 0 {
		t.Fatalf("silence level %v", got)
	}
	loud := make([]byte, 64)
	for i := 0; i+1 < len(loud); i += 2 {
		v := int16(32767)
		if i%4 == 0 {
			v = -32767
		}
		binary.LittleEndian.PutUint16(loud[i:], uint16(v))
	}
	if got := vkPCMLevel(loud); got < 0.99 {
		t.Fatalf("full-scale level %v", got)
	}
}

func TestVKTranscribeSendsWhatGroqExpects(t *testing.T) {
	var gotAuth, gotModel, gotLang, gotType, gotName string
	var gotBytes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotModel = r.FormValue("model")
		gotLang = r.FormValue("language")
		if files := r.MultipartForm.File["file"]; len(files) == 1 {
			gotType = files[0].Header.Get("Content-Type")
			gotName = files[0].Filename
			if f, err := files[0].Open(); err == nil {
				b, _ := io.ReadAll(f)
				gotBytes = len(b)
				f.Close()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"  Hello there.  "}`)
	}))
	defer server.Close()
	prev := vkVoiceEndpoint
	vkVoiceEndpoint = server.URL
	t.Cleanup(func() { vkVoiceEndpoint = prev })

	wav := vkEncodeWAV(make([]byte, 3200), vkVoiceRate)
	text, err := vkTranscribe(context.Background(), "gsk_test", wav, "de")
	if err != nil {
		t.Fatalf("transcribe failed: %v", err)
	}
	if text != "Hello there." {
		t.Fatalf("transcript %q", text)
	}
	if gotAuth != "Bearer gsk_test" || gotModel != vkVoiceModel || gotLang != "de" {
		t.Fatalf("request: auth %q model %q language %q", gotAuth, gotModel, gotLang)
	}
	if gotType != "audio/wav" || gotName != "speech.wav" || gotBytes != len(wav) {
		t.Fatalf("file part: type %q name %q bytes %d", gotType, gotName, gotBytes)
	}

	// No language hint means auto-detect: the field is left out.
	gotLang = "unset"
	if _, err := vkTranscribe(context.Background(), "gsk_test", wav, ""); err != nil || gotLang != "" {
		t.Fatalf("auto-detect sent language %q (err %v)", gotLang, err)
	}
}

func TestVKTranscribeErrorsAreActionable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid API Key"}}`)
	}))
	defer server.Close()
	prev := vkVoiceEndpoint
	vkVoiceEndpoint = server.URL
	t.Cleanup(func() { vkVoiceEndpoint = prev })

	_, err := vkTranscribe(context.Background(), "bad", vkEncodeWAV(nil, vkVoiceRate), "")
	if err == nil {
		t.Fatal("a 401 was not an error")
	}
	msg, action := vkVoiceErrorText(err)
	if action != vkVoiceOpenKey || !strings.Contains(msg, "API key") {
		t.Fatalf("401 became %q (action %d)", msg, action)
	}
	if strings.Contains(err.Error(), "bad") {
		t.Fatal("the error text carries the key")
	}
	if msg, _ := vkVoiceErrorText(context.DeadlineExceeded); !strings.Contains(msg, "timed out") {
		t.Fatalf("timeout became %q", msg)
	}
}

func TestVKVoiceSaysWhyItCannotStart(t *testing.T) {
	cfg := withVoiceSetup(t, "")
	withNetwork(t, true)

	vkVoiceToggle(cfg)
	if vkVoice.phase != vkVoiceMessage || vkVoice.action != vkVoiceOpenKey || !strings.Contains(vkVoice.msg, "Groq") {
		t.Fatalf("no key: phase %d action %d %q", vkVoice.phase, vkVoice.action, vkVoice.msg)
	}
	if vkFocusZone != vkZoneStrip {
		t.Fatal("the fix for a missing key should be one A press away")
	}
	vkVoiceCancel()

	cfg.Variables.Custom["groq_api_key"] = "gsk_test"
	withNetwork(t, false)
	vkVoiceToggle(cfg)
	if vkVoice.phase != vkVoiceMessage || !strings.Contains(vkVoice.msg, "internet") {
		t.Fatalf("offline: phase %d %q", vkVoice.phase, vkVoice.msg)
	}
	vkVoiceCancel()

	vkMic.Store(vkMicAbsent)
	if vkVoiceAvailable() || vkToolEnabled(vkToolMic) {
		t.Fatal("the mic chip should dim without a microphone")
	}
	vkVoiceToggle(cfg)
	if vkVoice.phase != vkVoiceMessage || !strings.Contains(vkVoice.msg, "microphone") {
		t.Fatalf("no mic: phase %d %q", vkVoice.phase, vkVoice.msg)
	}
	vkVoiceCancel()

	vkMic.Store(vkMicPresent)
	prevPass := wifiPassphraseBuffer
	t.Cleanup(func() { wifiPassphraseBuffer = prevPass })
	vkTarget = vkTargetWiFi
	vkVoiceToggle(cfg)
	if !strings.Contains(vkVoice.msg, "off for this field") {
		t.Fatalf("password field: %q", vkVoice.msg)
	}
}

func TestVKVoiceTranscriptLandsAtTheCaret(t *testing.T) {
	cfg := withVoiceSetup(t, "gsk_test")
	vkInsertText("hello", cfg)

	// A finished request for this recording is applied by the frame tick.
	vkVoice.phase = vkVoiceSending
	vkVoice.gen++
	vkVoiceResult.mu.Lock()
	vkVoiceResult.ready, vkVoiceResult.gen, vkVoiceResult.text, vkVoiceResult.err = true, vkVoice.gen, "World.", nil
	vkVoiceResult.mu.Unlock()
	vkVoiceTick(cfg)
	if chatInputText != "hello World." {
		t.Fatalf("transcript in chat gave %q", chatInputText)
	}
	if vkVoice.phase != vkVoiceIdle {
		t.Fatal("the bar did not return to the strip")
	}

	// A stale result (the user cancelled and recorded again) is dropped.
	vkVoice.phase = vkVoiceSending
	vkVoice.gen++
	vkVoiceResult.mu.Lock()
	vkVoiceResult.ready, vkVoiceResult.gen, vkVoiceResult.text = true, vkVoice.gen-1, "stale"
	vkVoiceResult.mu.Unlock()
	vkVoiceTick(cfg)
	if strings.Contains(chatInputText, "stale") {
		t.Fatal("a stale transcript was inserted")
	}
	vkVoiceCancel()

	// In a search, Whisper's closing period is dropped.
	vkTarget = vkTargetGameSearch
	prevQuery := gameLibraryQuery()
	t.Cleanup(func() { setGameLibraryQuery(prevQuery) })
	setGameLibraryQuery("")
	vkVoiceInsert("Zelda.", cfg)
	if got := gameLibraryQuery(); got != "Zelda" {
		t.Fatalf("search transcript gave %q", got)
	}
}
