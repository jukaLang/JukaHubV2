package main

import (
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

func TestVKVoicePreviewDraftAtCaret(t *testing.T) {
	tests := []struct {
		name     string
		draft    string
		partial  string
		caret    int
		selected bool
		want     string
		wantAt   int
	}{
		{"empty", "", "hello world", 0, false, "hello world", 11},
		{"after word", "Hello", "friend", 5, false, "Hello friend", 12},
		{"inside draft", "Hello world", "friend", 5, false, "Hello friend world", 12},
		{"after space", "Hello ", "friend", 6, false, "Hello friend", 12},
		{"selected draft", "old words", "new words", 4, true, "new words", 9},
		{"unicode", "café!", "世界", 5, false, "café! 世界", 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, at := vkVoicePreviewDraft(tt.draft, tt.partial, tt.caret, tt.selected)
			if got != tt.want || at != tt.wantAt {
				t.Fatalf("preview = %q at %d; want %q at %d", got, at, tt.want, tt.wantAt)
			}
		})
	}
}

func TestVKVoicePreviewIsNotCommitted(t *testing.T) {
	oldTarget, oldDraft, oldCaret, oldSelected := vkTarget, chatInputText, vkCaret, vkTextSelected
	oldPhase, oldPreview := vkVoice.phase, vkVoice.previewText
	t.Cleanup(func() {
		vkTarget, chatInputText, vkCaret, vkTextSelected = oldTarget, oldDraft, oldCaret, oldSelected
		vkVoice.phase, vkVoice.previewText = oldPhase, oldPreview
	})
	vkTarget = vkTargetChat
	chatInputText = "A draft"
	vkCaret = -1
	vkTextSelected = false
	vkVoice.phase = vkVoiceRecording
	vkVoice.previewText = "spoken words"

	if shown, _, ok := vkVoiceDisplayText(); !ok || shown != "A draft spoken words" {
		t.Fatalf("displayed preview = %q, %v", shown, ok)
	}
	if chatInputText != "A draft" {
		t.Fatalf("preview changed the committed draft to %q", chatInputText)
	}
	vkVoice.phase = vkVoiceIdle
	if _, _, ok := vkVoiceDisplayText(); ok {
		t.Fatal("canceled voice preview is still displayed")
	}
}

func TestVKVoicePreviewIgnoresOldRecording(t *testing.T) {
	oldTarget, oldKeyboard := vkTarget, virtualKeyboardActive
	oldPhase, oldGen, oldPeak := vkVoice.phase, vkVoice.gen, vkVoice.peak
	oldText, oldBusy, oldDisabled := vkVoice.previewText, vkVoice.previewBusy, vkVoice.previewDisabled
	vkVoicePreviewResult.mu.Lock()
	oldReplies := vkVoicePreviewResult.replies
	vkVoicePreviewResult.replies = []vkVoicePreviewReply{
		{gen: 40, text: "stale words"},
		{gen: 41, text: "current words"},
	}
	vkVoicePreviewResult.mu.Unlock()
	t.Cleanup(func() {
		vkTarget, virtualKeyboardActive = oldTarget, oldKeyboard
		vkVoice.phase, vkVoice.gen, vkVoice.peak = oldPhase, oldGen, oldPeak
		vkVoice.previewText, vkVoice.previewBusy, vkVoice.previewDisabled = oldText, oldBusy, oldDisabled
		vkVoicePreviewResult.mu.Lock()
		vkVoicePreviewResult.replies = oldReplies
		vkVoicePreviewResult.mu.Unlock()
	})
	vkTarget, virtualKeyboardActive = vkTargetChat, true
	vkVoice.phase, vkVoice.gen, vkVoice.peak = vkVoiceRecording, 41, 0
	vkVoice.previewBusy, vkVoice.previewDisabled = true, false
	vkVoicePreviewTick()
	if vkVoice.previewText != "current words" || vkVoice.previewBusy {
		t.Fatalf("preview = %q, busy = %v", vkVoice.previewText, vkVoice.previewBusy)
	}
}

func TestVKVoicePreviewRespectsDraftLimit(t *testing.T) {
	draft := strings.Repeat("a", vkMaxTextLen-2)
	shown, _ := vkVoicePreviewDraft(draft, "longer speech", utf8.RuneCountInString(draft), false)
	if len(shown) > vkMaxTextLen || !strings.HasPrefix(shown, draft) {
		t.Fatalf("preview exceeded the field or changed its original text: %d bytes", len(shown))
	}
}

func TestVKVoicePreviewTranscribesWhileRecording(t *testing.T) {
	requests := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing clip", http.StatusBadRequest)
			return
		}
		wav, _ := io.ReadAll(file)
		file.Close()
		if len(wav) < 44 || string(wav[:4]) != "RIFF" {
			http.Error(w, "not a WAV", http.StatusBadRequest)
			return
		}
		requests <- r.FormValue("model")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"heard so far"}`)
	}))
	defer srv.Close()

	oldVoice, oldTarget, oldKeyboard := vkVoice, vkTarget, virtualKeyboardActive
	oldConfig, oldEndpoint := appConfig, vkVoiceEndpoint
	vkVoicePreviewResult.mu.Lock()
	oldReplies := vkVoicePreviewResult.replies
	vkVoicePreviewResult.replies = nil
	vkVoicePreviewResult.mu.Unlock()
	t.Cleanup(func() {
		vkVoicePreviewReset()
		vkVoice, vkTarget, virtualKeyboardActive = oldVoice, oldTarget, oldKeyboard
		appConfig, vkVoiceEndpoint = oldConfig, oldEndpoint
		vkVoicePreviewResult.mu.Lock()
		vkVoicePreviewResult.replies = oldReplies
		vkVoicePreviewResult.mu.Unlock()
	})
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{
		"groq_api_key": "test-key", vkKeyVoiceLang: "auto",
	}}}
	vkVoiceEndpoint = srv.URL
	vkTarget, virtualKeyboardActive = vkTargetChat, true
	vkVoice.phase, vkVoice.gen, vkVoice.peak = vkVoiceRecording, 101, 0.1
	vkVoice.started = sdl.GetTicks64() - uint64(vkVoicePreviewFirstMs+100)
	vkVoice.previewText, vkVoice.previewAt = "", 0
	vkVoice.previewBusy, vkVoice.previewDisabled = false, false
	vkVoice.buf = make([]byte, vkVoiceRate*2*2)
	for i := 0; i < len(vkVoice.buf); i += 2 {
		binary.LittleEndian.PutUint16(vkVoice.buf[i:], 5000)
	}

	vkVoicePreviewTick()
	if !vkVoice.previewBusy {
		t.Fatal("recording did not start an interim transcription")
	}
	select {
	case model := <-requests:
		if model != vkVoiceModel {
			t.Fatalf("preview model = %q", model)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interim clip did not reach the transcription service")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		vkVoicePreviewResult.mu.Lock()
		ready := len(vkVoicePreviewResult.replies) > 0
		vkVoicePreviewResult.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("interim transcript did not return")
		}
		time.Sleep(10 * time.Millisecond)
	}
	vkVoice.previewDisabled = true // consume the reply without starting another request
	vkVoicePreviewTick()
	if vkVoice.previewText != "heard so far" || vkVoice.previewBusy {
		t.Fatalf("preview = %q, busy = %v", vkVoice.previewText, vkVoice.previewBusy)
	}
}
