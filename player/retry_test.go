package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackoffDelayGrowsAndCaps(t *testing.T) {
	noJitter := func() float64 { return 0.5 }
	base := 100 * time.Millisecond
	max := 500 * time.Millisecond

	if got, want := backoffDelay(1, base, max, 0, noJitter), 100*time.Millisecond; got != want {
		t.Errorf("attempt 1 = %v, want %v", got, want)
	}
	if got, want := backoffDelay(2, base, max, 0, noJitter), 200*time.Millisecond; got != want {
		t.Errorf("attempt 2 = %v, want %v", got, want)
	}
	if got, want := backoffDelay(9, base, max, 0, noJitter), 500*time.Millisecond; got != want {
		t.Errorf("attempt 9 = %v, want capped %v", got, want)
	}
	if got := backoffDelay(1, 0, max, 0, noJitter); got != 0 {
		t.Errorf("zero base = %v, want 0", got)
	}
	// Jitter stays inside the configured band.
	for i := 0; i < 100; i++ {
		got := backoffDelay(1, base, max, 0.3, func() float64 { return 1 })
		if got < 70*time.Millisecond || got > 130*time.Millisecond {
			t.Fatalf("jittered delay %v outside 0.7..1.3x of %v", got, base)
		}
	}
}

func TestRetryAfterDelay(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, want := retryAfterDelay("2", now), 2*time.Second; got != want {
		t.Errorf("seconds = %v, want %v", got, want)
	}
	if got := retryAfterDelay("", now); got != 0 {
		t.Errorf("empty = %v, want 0", got)
	}
	if got := retryAfterDelay("not-a-date", now); got != 0 {
		t.Errorf("garbage = %v, want 0", got)
	}
	if got := retryAfterDelay("Wed, 21 Oct 2015 07:28:00 GMT", now); got != 0 {
		t.Errorf("past date = %v, want 0", got)
	}
	future := now.Add(3 * time.Second).Format(http.TimeFormat)
	if got := retryAfterDelay(future, now); got != 3*time.Second {
		t.Errorf("future date = %v, want 3s", got)
	}
}

func TestDoWithRetryRecoversFromServerErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream down"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := retryPolicy{Attempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	resp, err := doWithRetry(context.Background(), srv.Client(), req, policy)
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestDoWithRetryDoesNotRetryClientErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := doWithRetry(context.Background(), srv.Client(), req,
		retryPolicy{Attempts: 3, BaseDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestDoWithRetryGivesUp(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := doWithRetry(context.Background(), srv.Client(), req,
		retryPolicy{Attempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond})
	if err == nil || resp != nil {
		t.Fatalf("expected an error after exhausting attempts, got resp=%v err=%v", resp, err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestDoWithRetryHonorsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := doWithRetry(ctx, srv.Client(), req,
		retryPolicy{Attempts: 5, BaseDelay: 2 * time.Second, MaxDelay: 2 * time.Second})
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("cancellation took %v, want a prompt return", elapsed)
	}
}

// A request body that cannot be rewound must not be replayed.
func TestDoWithRetrySingleAttemptForUnrewindableBody(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	req.Body = io.NopCloser(strings.NewReader("payload"))
	req.GetBody = nil
	_, err := doWithRetry(context.Background(), srv.Client(), req,
		retryPolicy{Attempts: 3, BaseDelay: time.Millisecond})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (body cannot be replayed)", got)
	}
}
