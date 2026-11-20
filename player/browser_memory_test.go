package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestBrowserReleaseMemoryKeepsOnlyNavigationAndSavedSiteData(t *testing.T) {
	oldScene, oldHome, oldResume := currentSceneIndex, browserHomeLoaded, browserResumeURL
	oldScroll := textBrowserScrollY
	oldRows, oldOrder, oldPending := browserScrollMemory.rows, browserScrollMemory.order, browserPending
	currentSceneIndex, browserHomeLoaded, browserResumeURL = 0, true, ""
	textBrowserScrollY = 12
	defer func() {
		currentSceneIndex, browserHomeLoaded, browserResumeURL = oldScene, oldHome, oldResume
		textBrowserScrollY = oldScroll
		browserScrollMemory.rows, browserScrollMemory.order, browserPending = oldRows, oldOrder, oldPending
		browserClearPageCache()
	}()

	address := "https://example.com/article"
	variable := browserContentVariable
	config := &Config{
		Scenes: []SceneConfig{{Name: browserSceneName, Elements: []Element{{Type: "textbrowser", Text: "web", Variable: variable}}}},
		Variables: Variables{Custom: map[string]interface{}{
			variable:               "a large rendered page",
			textBrowserURLVariable: address,
			"unrelated":            "keep",
		}},
	}
	previousDoc := browserDocumentFor(variable)
	defer setBrowserDocument(variable, previousDoc)
	webJSStorage.Lock()
	oldStorage, oldUsed := webJSStorage.data, webJSStorage.used
	webJSStorage.data = map[string]string{"session https://example.com": `{"q":"temporary"}`, "local https://example.com": `{"theme":"dark"}`}
	webJSStorage.used = []string{"session https://example.com", "local https://example.com"}
	webJSStorage.Unlock()
	defer func() {
		webJSStorage.Lock()
		webJSStorage.data, webJSStorage.used = oldStorage, oldUsed
		webJSStorage.Unlock()
	}()

	browserState.mu.Lock()
	oldHistory, oldIndex, oldLinks := browserState.history, browserState.index, browserState.links
	browserState.history, browserState.index = []string{address}, 0
	browserState.links = map[string][]browserLink{variable: {{Text: "next", URL: address + "/next"}}}
	browserState.mu.Unlock()
	defer func() {
		browserState.mu.Lock()
		browserState.history, browserState.index, browserState.links = oldHistory, oldIndex, oldLinks
		browserState.mu.Unlock()
	}()

	doc := &webDocument{URL: address, Images: map[string][]byte{address + "/image": make([]byte, 1024)}}
	setBrowserDocument(variable, doc)
	browserCachePage(address, browserPage{FinalURL: address, Text: "article", Doc: doc})
	browserReleaseMemory(config)

	if got := browserDocumentFor(variable); got != nil {
		t.Fatal("parsed browser document still retained")
	}
	if _, ok := browserCachedPage(address); ok {
		t.Fatal("back/forward cache still retained the page")
	}
	if _, ok := config.Variables.Custom[variable]; ok {
		t.Fatal("rendered page text still retained in Custom")
	}
	if got := config.Variables.Custom["unrelated"]; got != "keep" {
		t.Fatalf("unrelated state changed: %v", got)
	}
	if browserResumeURL != address || browserHomeLoaded {
		t.Fatalf("browser cannot refetch its last page: resume=%q loaded=%t", browserResumeURL, browserHomeLoaded)
	}
	if got := brwHistoryCurrent(); got != address {
		t.Fatalf("small URL history was lost: %q", got)
	}
	if got := browserScrollMemoryGet(address); got != 12 {
		t.Fatalf("current reading position was lost: %d", got)
	}
	if got := webJSStorageGet("session https://example.com"); got != "" {
		t.Fatalf("tab session storage remains: %q", got)
	}
	if got := webJSStorageGet("local https://example.com"); got == "" {
		t.Fatal("saved site storage was removed")
	}
}

func TestBrowserTextureEvictionLeavesOtherScenesAlone(t *testing.T) {
	cache := newThumbnailTextureCache(1024)
	destroyed := 0
	cache.destroy = func(*sdl.Texture) { destroyed++ }
	cache.Put("webimg:article", nil, 2, 2)
	cache.Put("media:preview", nil, 2, 2)
	cache.ClearPrefix("webimg:")
	_, webRemains := cache.entries["webimg:article"]
	_, mediaRemains := cache.entries["media:preview"]
	if webRemains || !mediaRemains || cache.Len() != 1 || destroyed != 1 || cache.bytes != 16 {
		t.Fatalf("selective eviction failed: entries=%d destroyed=%d", cache.Len(), destroyed)
	}
}

func TestBrowserPageFetchCancelsOnExit(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		browseWebPageResult(browserNavigation{URL: srv.URL, Context: ctx}, webJSLoadGeneration())
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("page request did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("page request kept running after cancellation")
	}
}
