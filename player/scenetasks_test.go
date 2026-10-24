package main

// Tests for the scene-scoped task scope and the coordinated group helper.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitClosed fails the test when ch is not closed within d.
func waitClosed(t *testing.T, ch <-chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v", what, d)
	}
}

func TestSceneWorkCancelsThePreviousScenesTasks(t *testing.T) {
	var m sceneWorkManager
	m.enter("Tools")
	started := make(chan struct{})
	stopped := make(chan struct{})
	var cancelled atomic.Bool
	if !m.spawn(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		cancelled.Store(true)
		close(stopped)
	}) {
		t.Fatal("spawn refused before shutdown")
	}
	waitClosed(t, started, time.Second, "task start")

	m.enter("Home")
	waitClosed(t, stopped, time.Second, "task stop")
	if !cancelled.Load() {
		t.Fatal("task returned without observing cancellation")
	}
	if got := m.scene(); got != "Home" {
		t.Fatalf("scene = %q, want Home", got)
	}
	if err := m.context().Err(); err != nil {
		t.Fatalf("new scene context already done: %v", err)
	}
}

func TestSceneWorkCancelIsIdempotentAndCoversLateTasks(t *testing.T) {
	var m sceneWorkManager
	m.enter("Podcasts")
	m.cancel()
	m.cancel()
	if err := m.context().Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error = %v, want canceled", err)
	}
	// A task started after the scene was left still belongs to the cancelled
	// generation, so it sees a dead context instead of running unchecked.
	seen := make(chan error, 1)
	m.spawn(func(ctx context.Context) { seen <- ctx.Err() })
	select {
	case err := <-seen:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("task saw %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("task did not run")
	}
	m.wait()
}

func TestSceneWorkWaitJoinsRunningTasks(t *testing.T) {
	var m sceneWorkManager
	m.enter("Stocks")
	var finished atomic.Int32
	for i := 0; i < 8; i++ {
		m.spawn(func(ctx context.Context) {
			<-time.After(10 * time.Millisecond)
			finished.Add(1)
		})
	}
	m.wait()
	if got := finished.Load(); got != 8 {
		t.Fatalf("joined %d tasks, want 8", got)
	}
}

func TestSceneWorkShutdownCancelsAndRefusesNewTasks(t *testing.T) {
	var m sceneWorkManager
	m.enter("Benchmarks")
	stopped := make(chan struct{})
	m.spawn(func(ctx context.Context) {
		<-ctx.Done()
		close(stopped)
	})
	m.shutdown()
	waitClosed(t, stopped, time.Second, "shutdown cancellation")
	if m.spawn(func(context.Context) {}) {
		t.Fatal("spawn accepted a task after shutdown")
	}
	if m.scene() != "" {
		t.Fatalf("scene = %q after shutdown, want empty", m.scene())
	}
}

func TestSceneWorkStartsWithALazyScope(t *testing.T) {
	var m sceneWorkManager
	if m.scene() != "" {
		t.Fatalf("scene = %q before any transition", m.scene())
	}
	done := make(chan struct{})
	if !m.spawn(func(ctx context.Context) {
		if ctx == nil {
			t.Error("task got a nil context")
		}
		close(done)
	}) {
		t.Fatal("spawn refused before any transition")
	}
	waitClosed(t, done, time.Second, "lazy task")

	// The first real transition cancels work that was started before it.
	m.enter("Home")
	wait := make(chan struct{})
	m.spawn(func(ctx context.Context) {
		<-ctx.Done()
		close(wait)
	})
	m.cancel()
	waitClosed(t, wait, time.Second, "late task cancellation")
	m.wait()
}

func TestRunCancelableGroupCancelsSiblingsOnFirstError(t *testing.T) {
	sentinel := errors.New("boom")
	siblingSawCancel := make(chan struct{})
	err := runCancelableGroup(context.Background(),
		func(context.Context) error { return sentinel },
		func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				close(siblingSawCancel)
				return ctx.Err()
			case <-time.After(2 * time.Second):
				return errors.New("sibling was not cancelled")
			}
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("group error = %v, want the first failure", err)
	}
	waitClosed(t, siblingSawCancel, time.Second, "sibling cancellation")
}

func TestRunCancelableGroupStopsOnParentCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{}, 2)
	fn := func(ctx context.Context) error {
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- runCancelableGroup(parent, fn, fn) }()
	<-entered
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("group error = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("group did not return after the parent was cancelled")
	}
}

func TestRunCancelableGroupWithDeadContextRunsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Bool
	err := runCancelableGroup(ctx, func(context.Context) error {
		ran.Store(true)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("group error = %v, want canceled", err)
	}
	if ran.Load() {
		t.Fatal("group ran work for a cancelled context")
	}
	if err := runCancelableGroup(nil); err != nil {
		t.Fatalf("empty group error = %v", err)
	}
}

// The speed test and benchmark scenes rely on the group returning only after
// every sibling has left, so no task can publish into the next screen.
func TestRunCancelableGroupRunsTasksConcurrently(t *testing.T) {
	var running, peak int
	var mu sync.Mutex
	fns := make([]func(context.Context) error, 6)
	for i := range fns {
		fns[i] = func(context.Context) error {
			mu.Lock()
			running++
			if running > peak {
				peak = running
			}
			mu.Unlock()
			<-time.After(15 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return nil
		}
	}
	if err := runCancelableGroup(context.Background(), fns...); err != nil {
		t.Fatalf("group error = %v", err)
	}
	if peak < 2 {
		t.Fatalf("peak concurrency = %d, want the tasks to overlap", peak)
	}
	if running != 0 {
		t.Fatalf("%d tasks still running after the group returned", running)
	}
}

// httpGetTextContext is the building block every adopted fetch uses: a
// cancelled scene must close the request instead of leaving it to time out.
func TestHTTPGetTextContextStopsOnCancel(t *testing.T) {
	handlerStarted := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerStarted <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- httpGetTextContext(ctx, srv.URL, 10*time.Second) }()
	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("the test server never saw the request")
	}
	cancel()
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("cancelled fetch returned %q, want empty", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the scene did not stop the fetch")
	}
	if got := httpGetTextContext(ctx, srv.URL, time.Second); got != "" {
		t.Fatalf("fetch with a cancelled context returned %q", got)
	}
}

// The wiring end to end: a scene-entry feed fetch is scene work, so leaving the
// screen closes its request and the remaining feeds are never asked for.
func TestSceneExitStopsThePodcastFetch(t *testing.T) {
	prevFeeds := curatedPodcasts
	t.Cleanup(func() {
		curatedPodcasts = prevFeeds
		sceneWork = sceneWorkManager{}
	})

	firstRequest := make(chan struct{}, 1)
	secondRequest := make(chan struct{}, 1)
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if served.Add(1) > 1 {
			secondRequest <- struct{}{}
			return
		}
		firstRequest <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()

	curatedPodcasts = []string{srv.URL + "/one", srv.URL + "/two"}
	sceneWork = sceneWorkManager{}
	enterSceneWork("Podcasts")
	loadPodcasts(&Config{})
	select {
	case <-firstRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("the feed fetch never started")
	}

	enterSceneWork("Home")
	select {
	case <-secondRequest:
		t.Fatal("the next feed was fetched after the scene was left")
	case <-time.After(200 * time.Millisecond):
	}
	if got := served.Load(); got != 1 {
		t.Fatalf("server saw %d requests, want only the cancelled first feed", got)
	}
}
