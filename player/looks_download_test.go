package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A second press on Download looks cancels the running one instead of saying
// it is already downloading (GAME-6).
func TestLooksDownloadCancelStopsTheRunningDownload(t *testing.T) {
	looksDL.Lock()
	oldRunning, oldStage, oldPercent, oldCancel := looksDL.running, looksDL.stage, looksDL.percent, looksDL.cancel
	looksDL.Unlock()
	t.Cleanup(func() {
		looksDL.Lock()
		looksDL.running, looksDL.stage, looksDL.percent, looksDL.cancel = oldRunning, oldStage, oldPercent, oldCancel
		looksDL.Unlock()
	})

	var called atomic.Bool
	looksDL.Lock()
	looksDL.running, looksDL.stage, looksDL.percent = true, "Downloading…", 10
	looksDL.cancel = func() { called.Store(true) }
	looksDL.Unlock()

	if !looksDownloadCancel() {
		t.Fatal("looksDownloadCancel = false with a download running")
	}
	if !called.Load() {
		t.Fatal("the running download's context was not cancelled")
	}
	if looksDownloadCancel() {
		t.Fatal("looksDownloadCancel cancelled a second time")
	}
}

// The download itself stops at the context, so a cancel does not wait for a
// slow server (GAME-6).
func TestLooksDownloadTempStopsWhenCancelled(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := looksDownloadTemp(ctx, srv.Client(), []string{srv.URL}, dir, ".cancel-*.zip", 1<<20, "Testing")
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".cancel-") {
				err = fmt.Errorf("left temporary file %s behind", e.Name())
				break
			}
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the local server never saw the download")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("looksDownloadTemp returned nil after its context was cancelled")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("looksDownloadTemp did not stop after the cancel")
	}
}

// libretro's buildbot overlays.zip has no top folder: borders/… sits at the
// root. The extractor must take that layout as well as SkyWalker541's single
// top folder, or the fallback downloads nothing (GAME-6).
func TestLooksOverlayPackHandlesAFlatBuildbotLayout(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"borders/test.cfg": "input_overlay = \"test.png\"\n",
		"borders/test.png": "not a real png, only the file layout is under test\n",
		"flat/other.png":   "a second top folder, as the buildbot pack has\n",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	oldZips, oldFallback := looksOverlayZips, looksOverlayFallbackZips
	looksOverlayZips, looksOverlayFallbackZips = []string{srv.URL + "/overlays.zip"}, nil
	t.Cleanup(func() { looksOverlayZips, looksOverlayFallbackZips = oldZips, oldFallback })

	dest := t.TempDir()
	man := &looksManifest{Files: map[string]string{}}
	n, _, err := looksFetchOverlays(context.Background(), srv.Client(), man, dest)
	if err != nil {
		t.Fatalf("looksFetchOverlays: %v", err)
	}
	if n == 0 {
		t.Fatal("a flat buildbot pack extracted no overlays")
	}
	if _, err := os.Stat(filepath.Join(dest, "borders", "test.png")); err != nil {
		t.Errorf("the flat pack's picture was not written: %v", err)
	}
}

// SkyWalker541's repository is tried first, but libretro's buildbot pack is
// the fallback the Overlay row promises (GAME-6).
func TestLooksOverlayFallbackIsTheBuildbotPack(t *testing.T) {
	if len(looksOverlayFallbackZips) == 0 {
		t.Fatal("no fallback overlay pack")
	}
	for _, u := range looksOverlayFallbackZips {
		if !strings.Contains(u, "buildbot.libretro.com/") || !strings.HasSuffix(u, "/overlays.zip") {
			t.Errorf("fallback %q is not libretro's overlays.zip", u)
		}
	}
}
