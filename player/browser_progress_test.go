package main

// browser_progress_test.go covers the reader's two-stage first paint: the
// parsed page is published before images, and each image becomes visible as
// its bytes arrive. Both are what keep a TrimUI from showing an empty loading
// card while a hero photograph crawls over Wi-Fi.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowsePublishesPageBeforeImages(t *testing.T) {
	var imageRequests atomic.Int32
	picture := pngBytes(t, 2, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<html><head><title>Progressive page</title></head><body>`+
				`<img src="/pic.png" alt="A picture"><p>Body copy</p></body></html>`)
		case "/pic.png":
			imageRequests.Add(1)
			w.Header().Set("Content-Type", "image/png")
			w.Write(picture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var partial browserPage
	page := browseWebPageResult(browserNavigation{URL: srv.URL + "/"}, webJSLoadGeneration(), func(p browserPage) {
		partial = p
		if imageRequests.Load() != 0 {
			t.Errorf("page was published after image requests had already started")
		}
	})
	if partial.Doc == nil || len(partial.Doc.Blocks) == 0 {
		t.Fatalf("partial page had no document: %+v", partial)
	}
	if !strings.Contains(partial.Text, "Body copy") {
		t.Fatalf("partial page text lost its content: %q", partial.Text)
	}
	if imageRequests.Load() != 1 {
		t.Fatalf("image pass ran %d times, want one", imageRequests.Load())
	}
	if page.Doc == nil || page.Doc != partial.Doc {
		t.Fatalf("final page did not keep the document the partial paint used")
	}
	page.Doc.imageMu.Lock()
	_, haveImage := page.Doc.Images[srv.URL+"/pic.png"]
	page.Doc.imageMu.Unlock()
	if !haveImage {
		t.Fatal("final document did not receive the fetched image")
	}
}

func TestFetchWebImagesPublishesBeforePassCompletes(t *testing.T) {
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	picture := pngBytes(t, 2, 2)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSlow) }) }
	defer release()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		switch r.URL.Path {
		case "/slow.png":
			close(slowStarted)
			<-releaseSlow
			w.Write(picture)
		case "/fast.png":
			w.Write(picture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	doc := parseWebPage(`<img src="/fast.png"><img src="/slow.png">`, "", srv.URL+"/")
	done := make(chan struct{})
	go func() {
		fetchWebImages(doc)
		close(done)
	}()

	select {
	case <-slowStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("image pass never requested the slow image")
	}

	fastKey := srv.URL + "/fast.png"
	deadline := time.Now().Add(3 * time.Second)
	for {
		doc.imageMu.Lock()
		_, haveImage := doc.Images[fastKey]
		doc.imageMu.Unlock()
		if haveImage {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fast image did not reach the document while the slow image was still in flight")
		}
		time.Sleep(10 * time.Millisecond)
	}

	release()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("image pass did not finish after the slow image was released")
	}
}
