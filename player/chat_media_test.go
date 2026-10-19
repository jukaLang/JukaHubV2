package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestChatImageURLDetection(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"look https://example.com/cat.png", "https://example.com/cat.png"},
		{"(https://example.com/cat.jpg?w=400)", "https://example.com/cat.jpg?w=400"},
		{"https://example.com/page and https://example.com/dog.webp", "https://example.com/dog.webp"},
		{"https://example.com/page", ""},
		{"no links here", ""},
		{"https://example.com/cat.png.", "https://example.com/cat.png"},
	}
	for _, tc := range cases {
		if got := firstChatImageURL(tc.text); got != tc.want {
			t.Errorf("firstChatImageURL(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// A chat attachment stores a local path; the preview must read that file
// directly instead of treating it as an HTTP URL.
func TestChatImageLocalAttachmentLoads(t *testing.T) {
	prevDownloads, prevFetching := chatImageDownloads, chatImageFetching
	chatImageDownloads = make(map[string][]byte)
	chatImageFetching = make(map[string]bool)
	t.Cleanup(func() {
		chatImageDownloads, chatImageFetching = prevDownloads, prevFetching
	})

	path := filepath.Join(t.TempDir(), "attached.png")
	payload := []byte("not-a-real-png-but-enough-for-the-fetch")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	fetchChatImage(path)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		chatImageDLMutex.Lock()
		data := chatImageDownloads[path]
		chatImageDLMutex.Unlock()
		if len(data) > 0 {
			if string(data) != string(payload) {
				t.Fatalf("preview data = %q, want the file contents", data)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("local attachment was never read")
}

func TestChatPlayableURLDetection(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"watch https://www.youtube.com/watch?v=abc123 now", "https://www.youtube.com/watch?v=abc123"},
		{"https://youtu.be/abc123", "https://youtu.be/abc123"},
		{"song https://cdn.example.com/track.mp3", "https://cdn.example.com/track.mp3"},
		{"https://example.com/article", ""},
		{"https://example.com/article and https://youtu.be/xyz", "https://youtu.be/xyz"},
	}
	for _, tc := range cases {
		if got := chatPlayableURL(tc.text); got != tc.want {
			t.Errorf("chatPlayableURL(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}
