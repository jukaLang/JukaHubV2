package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A runaway server must produce an error, not an out-of-memory handheld.
func TestFetchURLRejectsOversizedBody(t *testing.T) {
	chunk := strings.Repeat("x", 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < (fetchURLMaxBytes>>20)+2; i++ {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	if _, err := fetchURL(srv.URL, 30*time.Second); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("fetchURL over the cap: err = %v, want a size error", err)
	}
	if _, err := httpsGetBytes(srv.URL, 30*time.Second); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("httpsGetBytes over the cap: err = %v, want a size error", err)
	}
}

func TestFetchURLSmallBodyStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	got, err := fetchURL(srv.URL, 5*time.Second)
	if err != nil || got != "hello" {
		t.Fatalf("fetchURL = %q, %v; want hello", got, err)
	}
}
