package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebImageBudgetWaitsForUnusedReservations(t *testing.T) {
	image := pngBytes(t, 2, 2)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "image/png")
		w.Write(image)
	}))
	defer srv.Close()

	// CNN's saved homepage has 104 image tags. The old 48-image ceiling left
	// more than half of them as permanent placeholders.
	const count = 104
	var html strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&html, `<img src="/photo/%d.png">`, i)
	}
	doc := parseWebPage(html.String(), "", srv.URL+"/")
	fetchWebImages(doc)
	for i := 0; i < count; i++ {
		src := fmt.Sprintf("%s/photo/%d.png", srv.URL, i)
		if len(doc.imageBytes(src)) == 0 {
			t.Fatalf("small image %d was skipped while earlier downloads held the budget", i)
		}
	}
	if got := requests.Load(); got != count {
		t.Fatalf("requested %d images, want %d", got, count)
	}
}

func TestWebImageBudgetScalesWithFreeMemory(t *testing.T) {
	for _, tc := range []struct {
		availableMB uint64
		wantMiB     int64
	}{
		{0, 16},
		{96, 16},
		{192, 32},
		{300, 50},
		{512, 64},
		{^uint64(0), 64},
	} {
		if got := webImagePageBudgetFor(tc.availableMB); got != tc.wantMiB<<20 {
			t.Errorf("available %d MiB: image budget %d MiB, want %d MiB", tc.availableMB, got>>20, tc.wantMiB)
		}
	}
}
