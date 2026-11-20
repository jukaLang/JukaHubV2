package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitForChatImageDownload(t *testing.T, wantURL string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		chatImageDLMutex.Lock()
		_, ready := chatImageDownloads[wantURL]
		chatImageDLMutex.Unlock()
		if ready {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("chat image %q was not queued", wantURL)
}

func TestChatImageDownloadHasOnePendingSlot(t *testing.T) {
	discardPendingChatImageDownloads()
	t.Cleanup(discardPendingChatImageDownloads)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/first" {
			close(firstStarted)
			<-releaseFirst
		}
		_, _ = w.Write([]byte("image bytes"))
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(releaseFirst) })

	first, second := server.URL+"/first", server.URL+"/second"
	getChatImageTexture(nil, first)
	select {
	case <-firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first download did not start")
	}
	getChatImageTexture(nil, second)
	if got := requests.Load(); got != 1 {
		t.Fatalf("%d requests while first image is downloading, want 1", got)
	}
	releaseOnce.Do(func() { close(releaseFirst) })
	waitForChatImageDownload(t, first)
	getChatImageTexture(nil, second)
	if got := requests.Load(); got != 1 {
		t.Fatalf("%d requests while first image is queued, want 1", got)
	}

	discardPendingChatImageDownloads()
	chatImageDLMutex.Lock()
	queued := len(chatImageDownloads)
	chatImageDLMutex.Unlock()
	if queued != 0 {
		t.Fatalf("%d queued images after discard, want 0", queued)
	}
	getChatImageTexture(nil, second)
	waitForChatImageDownload(t, second)
	if got := requests.Load(); got != 2 {
		t.Fatalf("%d requests after freeing the slot, want 2", got)
	}
}

func TestChatImageDiscardIgnoresOldDownload(t *testing.T) {
	discardPendingChatImageDownloads()
	t.Cleanup(discardPendingChatImageDownloads)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("old image bytes"))
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })

	url := server.URL + "/stale"
	getChatImageTexture(nil, url)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("download did not start")
	}
	discardPendingChatImageDownloads()
	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		chatImageDLMutex.Lock()
		fetching := len(chatImageFetching)
		queued := len(chatImageDownloads)
		_, failed := chatImageFailed[url]
		chatImageDLMutex.Unlock()
		if fetching == 0 {
			if queued != 0 || failed {
				t.Fatalf("discarded download returned: queued=%d failed=%v", queued, failed)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("canceled download kept its pending slot")
}

func waitForChatAvatarState(t *testing.T, pending, queued int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		chatAvatarDLMutex.Lock()
		gotPending, gotQueued := len(chatAvatarPending), len(chatAvatarData)
		chatAvatarDLMutex.Unlock()
		if gotPending == pending && gotQueued == queued {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("avatar downloads did not reach pending=%d queued=%d", pending, queued)
}

func TestChatAvatarDownloadsHaveFourPendingSlots(t *testing.T) {
	discardPendingChatAvatarDownloads()
	t.Cleanup(discardPendingChatAvatarDownloads)
	release := make(chan struct{})
	var releaseOnce sync.Once
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/fifth" {
			<-release
		}
		_, _ = w.Write([]byte("avatar bytes"))
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })

	for i := range chatAvatarPendingMax {
		startChatAvatarFetch(server.URL + "/" + strconv.Itoa(i))
	}
	waitForChatAvatarState(t, chatAvatarPendingMax, 0)
	deadline := time.Now().Add(3 * time.Second)
	for requests.Load() < chatAvatarPendingMax && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := requests.Load(); got != chatAvatarPendingMax {
		t.Fatalf("%d avatar requests started, want %d", got, chatAvatarPendingMax)
	}
	fifth := server.URL + "/fifth"
	startChatAvatarFetch(fifth)
	if got := requests.Load(); got != chatAvatarPendingMax {
		t.Fatalf("%d requests while four avatars are downloading, want %d", got, chatAvatarPendingMax)
	}
	releaseOnce.Do(func() { close(release) })
	waitForChatAvatarState(t, 0, chatAvatarPendingMax)
	startChatAvatarFetch(fifth)
	if got := requests.Load(); got != chatAvatarPendingMax {
		t.Fatalf("%d requests while four avatars are queued, want %d", got, chatAvatarPendingMax)
	}

	discardPendingChatAvatarDownloads()
	waitForChatAvatarState(t, 0, 0)
	startChatAvatarFetch(fifth)
	waitForChatAvatarState(t, 0, 1)
	if got := requests.Load(); got != chatAvatarPendingMax+1 {
		t.Fatalf("%d requests after freeing slots, want %d", got, chatAvatarPendingMax+1)
	}
}

func TestChatAvatarDiscardIgnoresOldDownload(t *testing.T) {
	discardPendingChatAvatarDownloads()
	t.Cleanup(discardPendingChatAvatarDownloads)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("old avatar bytes"))
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })

	url := server.URL + "/stale"
	startChatAvatarFetch(url)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("avatar download did not start")
	}
	discardPendingChatAvatarDownloads()
	releaseOnce.Do(func() { close(release) })
	waitForChatAvatarState(t, 0, 0)
	chatAvatarDLMutex.Lock()
	failed := chatAvatarFailed[url]
	chatAvatarDLMutex.Unlock()
	if failed {
		t.Fatal("discarded avatar was marked failed")
	}
}

func TestChatAvatarRejectsOversizedDecode(t *testing.T) {
	_, err := decodeChatAvatar(pngHeaderClaiming(5000, 5000))
	if err == nil || !strings.Contains(err.Error(), "decode limit") {
		t.Fatalf("oversized avatar error = %v, want pixel limit", err)
	}
}
