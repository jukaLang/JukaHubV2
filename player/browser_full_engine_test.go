package main

import "testing"

func TestBrowserFullEngineIsDefaultForPageEntry(t *testing.T) {
	oldHandheld, oldOpen := brwWpeHandheldFn, brwOpenDesktopPage
	defer func() { brwWpeHandheldFn, brwOpenDesktopPage = oldHandheld, oldOpen }()
	brwWpeHandheldFn = func() bool { return false }
	var opened string
	brwOpenDesktopPage = func(address string) error { opened = address; return nil }

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	if got := brwRenderMode(config); got != brwRenderModeFull {
		t.Fatalf("default render mode = %q, want full", got)
	}
	if !openBrowserAtURL(config, "https://example.com/story?q=one%20two") {
		t.Fatal("full browser entry failed")
	}
	if opened != "https://example.com/story?q=one%20two" {
		t.Fatalf("full browser received %q", opened)
	}
	if got := sceneIndexExact(config, browserSceneName); got >= 0 {
		t.Fatalf("full browser entry also opened the light reader at scene %d", got)
	}
	opened = ""
	openBrowserHome(config)
	if want := brwHomeURL(config); opened != want {
		t.Fatalf("Browser tile opened %q, want home page %q", opened, want)
	}
}

func TestBrowserReaderModeKeepsPagesInsideJukaHub(t *testing.T) {
	oldHandheld, oldOpen := brwWpeHandheldFn, brwOpenDesktopPage
	defer func() { brwWpeHandheldFn, brwOpenDesktopPage = oldHandheld, oldOpen }()
	brwWpeHandheldFn = func() bool { return false }
	called := false
	brwOpenDesktopPage = func(string) error { called = true; return nil }

	config := &Config{Variables: Variables{Custom: map[string]interface{}{brwRenderModeKey: brwRenderModeReader}}}
	if brwTryOpenFullEngine(config, "https://example.com/") || called {
		t.Fatal("reader mode handed a page to the desktop browser")
	}
	if got := brwRenderModeCurrent(config); got != 1 {
		t.Fatalf("reader setting selected stop %d", got)
	}
}

func TestBrowserFallsBackWhenWPEIsNotInstalled(t *testing.T) {
	oldHandheld, oldSupported, oldRoot, oldOpen := brwWpeHandheldFn, brwWpeSupportedFn, brwWpeSDRootFn, brwOpenDesktopPage
	defer func() {
		brwWpeHandheldFn, brwWpeSupportedFn, brwWpeSDRootFn, brwOpenDesktopPage = oldHandheld, oldSupported, oldRoot, oldOpen
	}()
	brwWpeHandheldFn = func() bool { return true }
	brwWpeSupportedFn = func() bool { return true }
	root := t.TempDir()
	brwWpeSDRootFn = func() string { return root }
	brwOpenDesktopPage = func(string) error {
		t.Fatal("handheld tried to open the computer's browser")
		return nil
	}

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	if brwTryOpenFullEngine(config, "https://example.com/") {
		t.Fatal("full browser reported success without WPE installed")
	}
}
