package main

// Tests for the scene-scoped task scope and the coordinated group helper.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

// The yt-dlp searches (results, Shorts, trending, paging) belong to the scene
// their list is shown on. executeYouTubeSearchContext must refuse to start
// work for a scope that is already dead: no wipe of the previous screen's
// results, no error line, no spinner.
func TestMediaSearchContextSkipsADeadSceneScope(t *testing.T) {
	config := layoutTestConfig(t)
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		executeYouTubeSearchContext(parent, config,
			`yt-dlp --flat-playlist --dump-single-json "ytsearch10:shorts"`,
			"shorts_list", snapshotVars(config), false)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a dead scene scope must not start a search run (it must return, not wait out the tool path resolution)")
	}
	drainCustom(config)
	if msg, ok := config.Variables.Custom["search_error"]; ok && msg != nil {
		t.Errorf("dead scope published an error line: %v", msg)
	}
	if config.Variables.LoadingSpinner {
		t.Error("dead scope left the loading spinner raised")
	}
}

// TestYtDlpSceneHelperProcess is the fake yt-dlp the cancel test below runs:
// a copy of the test binary parked on a marker-write plus a long sleep, so the
// cancellation has a real child process to kill. The gate is in the env so a
// normal test run only ever kills it, never lets it finish.
func TestYtDlpSceneHelperProcess(t *testing.T) {
	if os.Getenv("JUKAHUB_YTDLP_SCENE_HELPER") != "1" {
		t.Skip("helper process for TestSceneLeaveCancelsRunningYtDlpSearch")
	}
	if marker := os.Getenv("JUKAHUB_YTDLP_SCENE_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte("started"), 0o644)
	}
	time.Sleep(60 * time.Second)
}

// The wiring end to end: a yt-dlp search in flight on the Shorts scene dies
// when the scene is left — the child process is killed instead of the tool
// finishing 60 s later behind the home screen, nothing is published into the
// next screen, and the spinner is released.
func TestSceneLeaveCancelsRunningYtDlpSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a helper process")
	}
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	// The fake yt-dlp is a copy of this test binary: helperExecutable resolves
	// by Stat, so the copy is "installed", and exec runs it with the helper's
	// env inheritance (helperToolEnv keeps os.Environ).
	toolsDir := t.TempDir()
	exe := "yt-dlp"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	fake := filepath.Join(toolsDir, exe)
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatalf("read test binary for the fake yt-dlp: %v", err)
	}
	if err := os.WriteFile(fake, self, 0o755); err != nil {
		t.Fatalf("stage fake yt-dlp: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "helper-started")
	t.Setenv("JUKAHUB_YTDLP_SCENE_HELPER", "1")
	t.Setenv("JUKAHUB_YTDLP_SCENE_MARKER", marker)
	config.Variables.ToolsPath = toolsDir

	sceneWork = sceneWorkManager{}
	defer func() { sceneWork = sceneWorkManager{} }()
	enterSceneWork("Shorts")

	drainCustomForTest()
	command := "yt-dlp -test.run=TestYtDlpSceneHelper"
	vars := snapshotVars(config)
	// The shipped config may carry a playback-resolution cap, which appends
	// yt-dlp flags the helper does not know; the mechanism under test is
	// scene membership, not option building.
	vars["PlaybackResolution"] = ""
	executeYouTubeSearch(config, command, "shorts_list", vars)

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake yt-dlp never started; the search was not visited as scene work")
		}
		time.Sleep(20 * time.Millisecond)
	}

	enterSceneWork("Home") // leave the scene: the in-flight search must die
	diedBy := time.Now().Add(5 * time.Second)
	for {
		mediaSearchMu.Lock()
		busy := mediaSearchBusy
		mediaSearchMu.Unlock()
		if busy == 0 {
			break
		}
		if time.Now().After(diedBy) {
			t.Fatal("the yt-dlp search kept running after the scene was left")
		}
		time.Sleep(20 * time.Millisecond)
	}

	drainCustom(config)
	if config.Variables.LoadingSpinner {
		t.Error("loading spinner left raised after the cancelled search")
	}
	if got, ok := config.Variables.Custom["shorts_list"]; ok {
		if _, isVideos := got.([]VideoInfo); isVideos {
			t.Errorf("a cancelled search published results into the next scene: %d entries", len(got.([]VideoInfo)))
		}
	}
}
