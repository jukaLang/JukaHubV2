package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestVoiceCommandSpanishPhrases is the second-language table: a Spanish
// command has to reach the same intents as its English translation, without
// touching the English grammar's names.
func TestVoiceTranscriptionAsksForKeyboardsLanguage(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		got <- r.FormValue("language")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer srv.Close()
	oldEndpoint, oldHTTP, oldLang := vkVoiceEndpoint, vkVoiceHTTP, voiceCommandLanguageFn
	vkVoiceEndpoint, vkVoiceHTTP = srv.URL, srv.Client()
	voiceCommandLanguageFn = func() string { return "es" }
	defer func() { vkVoiceEndpoint, vkVoiceHTTP, voiceCommandLanguageFn = oldEndpoint, oldHTTP, oldLang }()
	if text, err := voiceTranscribe(context.Background(), "key", []byte("wav"), ""); err != nil || text != "ok" {
		t.Fatalf("voiceTranscribe = %q, %v", text, err)
	}
	if lang := <-got; lang != "es" {
		t.Fatalf("Whisper language = %q, want es", lang)
	}
}

func TestVoiceCommandSpanishPhrases(t *testing.T) {
	cases := []struct {
		raw    string
		kind   voiceIntentKind
		verb   string
		target string
		value  int
	}{
		{"abre youtube", voiceIntentOpen, "open", "youtube", 0},
		{"abre el navegador", voiceIntentOpen, "open", "navegador", 0},
		{"juega a mario kart", voiceIntentOpen, "play", "mario kart", 0},
		{"sube el volumen", voiceIntentVolumeStep, "", "", voiceStepDefault},
		{"baja el volumen", voiceIntentVolumeStep, "", "", -voiceStepDefault},
		{"silencia", voiceIntentMute, "", "", 0},
		{"quita el silencio", voiceIntentUnmute, "", "", 0},
		{"qué hora es", voiceIntentTime, "", "", 0},
		{"qué fecha es", voiceIntentDate, "", "", 0},
		{"batería", voiceIntentBattery, "", "", 0},
		{"captura de pantalla", voiceIntentScreenshot, "", "", 0},
		{"cierra todo", voiceIntentCloseAll, "", "", 0},
		{"por favor abre el navegador", voiceIntentOpen, "open", "navegador", 0},
	}
	for _, tc := range cases {
		got := voiceParse(tc.raw)
		if got.Kind != tc.kind || got.Verb != tc.verb || got.Target != tc.target || got.Value != tc.value {
			t.Errorf("voiceParse(%q) = {%v %q %q %d}, want {%v %q %q %d}",
				tc.raw, got.Kind, got.Verb, got.Target, got.Value, tc.kind, tc.verb, tc.target, tc.value)
		}
	}
}
