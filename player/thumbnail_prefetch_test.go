package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Prefetch must overlap requests: a single worker downloaded one thumbnail per
// round trip, which made results grids fill in slowly on Wi-Fi. The server only
// releases responses once two requests are in flight, so a serial worker
// cannot complete the test.
func TestThumbnailPrefetchRunsConcurrently(t *testing.T) {
	prevCh := thumbnailDownloadCh
	t.Cleanup(func() { thumbnailDownloadCh = prevCh })
	t.Chdir(t.TempDir())
	// Registered after Chdir so it runs before the directory is restored and
	// removed: an open bbolt file would block cleanup on Windows.
	t.Cleanup(closeSharedCache)

	thumbnailDownloadCh = make(chan string, 8)

	var inFlight int32
	var readyOnce sync.Once
	ready := make(chan struct{})
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&inFlight, 1) >= 2 {
			readyOnce.Do(func() { close(ready) })
		}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
		atomic.AddInt32(&inFlight, -1)
		_, _ = w.Write([]byte("thumbnail-bytes"))
	}))
	defer srv.Close()

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			thumbnailPrefetchWorker()
		}()
	}

	thumbnailDownloadCh <- srv.URL + "/a.jpg"
	thumbnailDownloadCh <- srv.URL + "/b.jpg"

	concurrent := true
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		concurrent = false
	}
	close(release)
	close(thumbnailDownloadCh)
	wg.Wait()

	if !concurrent {
		t.Fatal("prefetch did not run two downloads at the same time; grids stay serialized")
	}
	if got := thumbnailDataGet(srv.URL + "/a.jpg"); len(got) == 0 {
		t.Error("first thumbnail was not cached")
	}
	if got := thumbnailDataGet(srv.URL + "/b.jpg"); len(got) == 0 {
		t.Error("second thumbnail was not cached")
	}
}

// TestPrefetchThumbnailNeverBlocksOnAFullQueue is the 1 GB-device guarantee: a
// results grid re-enqueues its visible rows every frame, so a blocking send
// would stall the render loop whenever the network is slower than the UI. The
// contract is to drop the URL and let the on-demand path fetch it later.
func TestPrefetchThumbnailNeverBlocksOnAFullQueue(t *testing.T) {
	prevCh := thumbnailDownloadCh
	t.Cleanup(func() { thumbnailDownloadCh = prevCh })
	thumbnailDownloadCh = make(chan string, 1)
	thumbnailDownloadCh <- "https://example.invalid/already-queued.jpg"

	start := time.Now()
	for i := 0; i < 2000; i++ {
		PrefetchThumbnail(fmt.Sprintf("https://example.invalid/queued-%d.jpg", i))
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("2000 prefetches into a full queue took %v; enqueueing must drop, not block", elapsed)
	}
	if got := len(thumbnailDownloadCh); got != 1 {
		t.Errorf("queue length = %d, want the 1 pre-filled entry (nothing may be added)", got)
	}
}

// TestThumbnailFloodLeavesTheByteCacheBounded is the stress case for the
// prefetch queue: far more URLs than the in-memory byte cache can hold, pushed
// through the real workers. It pins the two properties the handheld depends on —
// every request is consumed (no worker wedged on a full queue) and the byte cache
// evicts instead of growing with the session.
func TestThumbnailFloodLeavesTheByteCacheBounded(t *testing.T) {
	prevCh := thumbnailDownloadCh
	t.Cleanup(func() { thumbnailDownloadCh = prevCh })
	t.Chdir(t.TempDir())
	// Registered after Chdir so it runs before the directory is restored and
	// removed: an open bbolt file would block cleanup on Windows.
	t.Cleanup(closeSharedCache)

	payload := bytes.Repeat([]byte("thumb"), 128)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	const total = thumbnailMemMaxEntries + 100
	thumbnailDownloadCh = make(chan string, 16)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			thumbnailPrefetchWorker()
		}()
	}
	for i := 0; i < total; i++ {
		thumbnailDownloadCh <- fmt.Sprintf("%s/flood-%d.jpg", srv.URL, i)
	}
	close(thumbnailDownloadCh)
	wg.Wait()

	thumbnailCacheMutex.Lock()
	cached := len(thumbnailDataCache)
	thumbnailCacheMutex.Unlock()
	if cached > thumbnailMemMaxEntries {
		t.Errorf("byte cache holds %d entries after %d downloads, cap is %d", cached, total, thumbnailMemMaxEntries)
	}
	if cached == 0 {
		t.Error("the flood left the byte cache empty; workers did not consume the queue")
	}
	newest := fmt.Sprintf("%s/flood-%d.jpg", srv.URL, total-1)
	if got := thumbnailDataGet(newest); len(got) != len(payload) {
		t.Errorf("newest thumbnail has %d bytes, want %d: the newest work must survive eviction", len(got), len(payload))
	}
}

// BenchmarkThumbnailPrefetch measures the same batch at several worker counts
// against a server with realistic Wi-Fi latency. Run with:
//
//	go test -bench ThumbnailPrefetch -benchtime 3x -run '^$'
func BenchmarkThumbnailPrefetch(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		_, _ = w.Write([]byte("thumbnail-bytes"))
	}))
	defer srv.Close()

	dir, err := os.MkdirTemp("", "thumbbench")
	if err != nil {
		b.Fatal(err)
	}
	prevWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		b.Fatal(err)
	}
	prevCh := thumbnailDownloadCh
	closeSharedCache()
	b.Cleanup(func() {
		closeSharedCache()
		thumbnailDownloadCh = prevCh
		_ = os.Chdir(prevWD)
		_ = os.RemoveAll(dir)
	})

	const batch = 8
	for _, workers := range []int{1, 2, 3} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				urls := make([]string, batch)
				for i := range urls {
					// The worker count is part of the path: otherwise the second
					// sub-benchmark would hit the disk cache the first one filled
					// and look artificially fast.
					urls[i] = fmt.Sprintf("%s/w%d-img-%d-%d.jpg", srv.URL, workers, n, i)
				}
				thumbnailDownloadCh = make(chan string, batch)
				var wg sync.WaitGroup
				for w := 0; w < workers; w++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						thumbnailPrefetchWorker()
					}()
				}
				for _, u := range urls {
					thumbnailDownloadCh <- u
				}
				close(thumbnailDownloadCh)
				wg.Wait()
			}
		})
	}
}
