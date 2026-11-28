package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// A community message that is only a picture still offers Ask Juka: the text
// Ask Juka action used to need words, so a photo on its own could not be sent
// to Juka at all (SYS-21).
func TestChatPictureOnlyMessageOffersAskJuka(t *testing.T) {
	chatMutex.Lock()
	oldMsgs, oldTab, oldViews := chatMessages, chatActiveTab.Load(), chatViews
	chatMessages = []ChatMessage{{
		ID:       "pic-1",
		Sender:   "Alice",
		ImageURL: "https://example.test/cat.png",
	}}
	chatActiveTab.Store(int32(chatTabCommunity))
	chatViews[chatTabCommunity].files = nil
	chatMutex.Unlock()
	t.Cleanup(func() {
		chatMutex.Lock()
		chatMessages, chatViews = oldMsgs, oldViews
		chatActiveTab.Store(oldTab)
		chatMutex.Unlock()
	})

	actions := chatSelectionActions(0)
	for _, a := range actions {
		if a.kind == "ask" && a.label == "Ask Juka" {
			return
		}
	}
	t.Fatalf("actions = %+v, want an Ask Juka action for the picture", actions)
}

// A remote picture is downloaded to a real local file before Juka is asked:
// vision_chat.go reads a path, and a Discord link expires.
func TestJukaPictureFromURLDownloadsThePicture(t *testing.T) {
	oldDir := chatSaveDirOverride
	chatSaveDirOverride = t.TempDir()
	t.Cleanup(func() { chatSaveDirOverride = oldDir })

	want := []byte("\x89PNG\r\n\x1a\npicture-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(want)
	}))
	defer srv.Close()

	pic, err := jukaPictureFromURL(srv.URL + "/cat.png")
	if err != nil {
		t.Fatalf("jukaPictureFromURL: %v", err)
	}
	if pic.Path == "" || !strings.HasSuffix(strings.ToLower(pic.Name), ".png") {
		t.Fatalf("picture = %+v, want a local .png path and name", pic)
	}
	if pic.Type != "image/png" {
		t.Errorf("Type = %q, want image/png", pic.Type)
	}
	got, err := os.ReadFile(pic.Path)
	if err != nil {
		t.Fatalf("read downloaded picture: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("downloaded bytes = %q, want %q", got, want)
	}
}

// A link with no suffix gets one from the response, and a query string never
// becomes part of the saved name.
func TestJukaPictureNameUsesTheResponseType(t *testing.T) {
	cases := []struct {
		url, ctype, want string
	}{
		{"https://cdn.test/image?token=abc", "image/jpeg", "image.jpg"},
		{"https://cdn.test/cat.png?x=1", "application/octet-stream", "cat.png"},
		{"https://cdn.test/pic", "image/gif", "pic.gif"},
	}
	for _, c := range cases {
		if got := jukaPictureName(c.url, c.ctype); got != c.want {
			t.Errorf("jukaPictureName(%q, %q) = %q, want %q", c.url, c.ctype, got, c.want)
		}
	}
}

// A video's transcript is uploaded as FLAC (or Opus) when this ffmpeg can
// write one, saving most of the 19 MB WAV upload (SYS-21).
func TestVisionSpeechEncoderPicksTheBestAvailable(t *testing.T) {
	withFLAC := "Encoders:\n A....D flac                FLAC (Free Lossless Audio Codec)\n A....D libopus              libopus Opus\n"
	if name, ext := visionPickSpeechEncoder(withFLAC); name != "flac" || ext != ".flac" {
		t.Fatalf("with FLAC: got %q %q, want flac .flac", name, ext)
	}
	withOpus := "Encoders:\n A....D libopus              libopus Opus\n"
	if name, ext := visionPickSpeechEncoder(withOpus); name != "libopus" || ext != ".opus" {
		t.Fatalf("with Opus: got %q %q, want libopus .opus", name, ext)
	}
	if name, ext := visionPickSpeechEncoder("Encoders:\n A....D aac   AAC\n"); name != "" || ext != "" {
		t.Fatalf("with neither: got %q %q, want no encoder", name, ext)
	}
}

// The multipart header names the compressed file and its type, not sound.wav.
func TestVisionSpeechUploadNamesTheCompressedFile(t *testing.T) {
	cases := map[string]string{
		"sound.wav":  "audio/wav",
		"sound.flac": "audio/flac",
		"sound.opus": "audio/ogg",
	}
	for name, want := range cases {
		if got := visionSpeechMIME(name); got != want {
			t.Errorf("visionSpeechMIME(%q) = %q, want %q", name, got, want)
		}
	}
}
