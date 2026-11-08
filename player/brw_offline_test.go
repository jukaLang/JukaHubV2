package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// brwSetOnlineForTest pins the connectivity monitor for one test and keeps
// the card's background probe away from the real monitor.
func brwSetOnlineForTest(t *testing.T, online bool) {
	t.Helper()
	netMon.mu.Lock()
	prevOnline, prevChecked, prevReason := netMon.online, netMon.checked, netMon.reason
	netMon.online, netMon.checked = online, true
	netMon.mu.Unlock()
	prevRefresh, prevProbe := brwRefreshNetwork, brwProbeOnline
	brwRefreshNetwork = func() {}
	brwProbeOnline = func() bool { return online }
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.online, netMon.checked, netMon.reason = prevOnline, prevChecked, prevReason
		netMon.mu.Unlock()
		brwRefreshNetwork, brwProbeOnline = prevRefresh, prevProbe
	})
}

// No failure the browser can show carries the socket's own words.
func TestBrwFailuresNeverShowSocketErrors(t *testing.T) {
	brwSetOnlineForTest(t, true)
	const address = "https://lite.duckduckgo.com/lite/"
	notFound := &net.DNSError{Err: "no such host", Name: "lite.duckduckgo.com", IsNotFound: true}
	temporary := &net.DNSError{Err: "server misbehaving", Name: "lite.duckduckgo.com", IsTemporary: true}
	cases := []struct {
		name     string
		err      error
		timedOut bool
		online   bool
		want     brwFailKind
	}{
		{"lookup while offline", notFound, false, false, brwFailOffline},
		{"lookup while online", notFound, false, true, brwFailNotFound},
		{"resolver down", temporary, false, true, brwFailOffline},
		{"plain lookup text", errors.New("dial tcp: lookup lite.duckduckgo.com: no such host"), false, true, brwFailNotFound},
		{"deadline", errors.New("context deadline exceeded"), true, true, brwFailTimeout},
		{"refused", errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), false, true, brwFailConnect},
		{"windows refused", errors.New("dial tcp 1.2.3.4:443: connectex: No connection could be made because the target machine actively refused it."), false, true, brwFailConnect},
		{"no route", errors.New("dial tcp 1.2.3.4:443: connect: network is unreachable"), false, true, brwFailOffline},
		{"certificate", errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), false, true, brwFailTLS},
		{"reset", errors.New("read tcp 10.0.0.2:5000->1.2.3.4:443: read: connection reset by peer"), false, true, brwFailConnect},
		{"something else", errors.New("dial tcp 1.2.3.4:443: i/o weirdness"), false, true, brwFailOther},
	}
	for _, tc := range cases {
		f := brwClassifyError(address, tc.err, tc.timedOut, tc.online)
		if f.Kind != tc.want {
			t.Errorf("%s: kind %d, want %d", tc.name, f.Kind, tc.want)
		}
		for _, text := range []string{f.Title, f.Detail, f.text()} {
			lower := strings.ToLower(text)
			for _, bad := range []string{"dial ", "lookup ", "tcp", "connectex", "no such host"} {
				if strings.Contains(lower, bad) {
					t.Errorf("%s: %q shows the socket error (%q)", tc.name, text, bad)
				}
			}
		}
		if f.Title == "" || f.Detail == "" {
			t.Errorf("%s: the card would have no words: %+v", tc.name, f)
		}
	}
	for _, err := range []error{notFound, errors.New("Get \"https://x\": dial tcp 1.2.3.4:443: connect: weird"), errors.New("write tcp: broken pipe")} {
		if got := brwFriendlyNetError(err); strings.Contains(strings.ToLower(got), "dial") || strings.Contains(got, "tcp") {
			t.Errorf("friendly error for %v = %q", err, got)
		}
	}
}

// A timeout still says "Timed out" (the redirect test reads that), and a
// server's own error is reported by its status.
func TestBrwHTTPFailureKeepsTheStatus(t *testing.T) {
	brwSetOnlineForTest(t, true)
	resetBrowserStateForTest()
	t.Cleanup(resetBrowserStateForTest)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	page := browseWebPageResult(srv.URL + "/missing")
	if page.Fail == nil || page.Fail.Kind != brwFailHTTP {
		t.Fatalf("a 404 produced %+v", page.Fail)
	}
	if !strings.Contains(page.Text, "Could not load") || !strings.Contains(page.Fail.Detail, "404") {
		t.Fatalf("page text %q / detail %q", page.Text, page.Fail.Detail)
	}

	// A port nobody listens on: refused, in words.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String() + "/"
	ln.Close()
	page = browseWebPageResult(closed)
	if page.Fail == nil {
		t.Fatalf("a closed port produced a page: %q", page.Text)
	}
	if strings.Contains(page.Text, "dial tcp") || strings.Contains(page.Text, "connectex") {
		t.Fatalf("the refused connection leaked its socket error: %q", page.Text)
	}
}

// Known to be offline, a navigation shows the offline card straight away
// without touching the network; a page in the cache still opens.
func TestBrowserOfflineShortCircuit(t *testing.T) {
	config, elem, calls := browserCacheFixture(t, func(rawURL string) browserPage {
		return browserPage{Text: "Title: cached\nURL: " + rawURL + "\n\nbody", FinalURL: rawURL}
	})
	brwSetOnlineForTest(t, true)
	loadTextBrowserURL(config, elem, "https://example.com/cached")
	waitBrowserText(t, config, func(s string) bool { return strings.Contains(s, "body") })

	brwSetOnlineForTest(t, false)
	before := atomic.LoadInt32(calls)
	loadTextBrowserURL(config, elem, "https://example.com/elsewhere")
	f, ok := brwFailureFor(elem.Variable)
	if !ok || f.Kind != brwFailOffline {
		t.Fatalf("offline navigation left failure %+v, %v", f, ok)
	}
	if got := atomic.LoadInt32(calls); got != before {
		t.Fatalf("offline navigation went to the network (%d fetches)", got-before)
	}
	if text, _ := config.Variables.Custom[elem.Variable].(string); !strings.HasPrefix(text, "Could not load") {
		t.Fatalf("page text = %q", text)
	}

	loadTextBrowserURL(config, elem, "https://example.com/cached")
	if _, failed := brwFailureFor(elem.Variable); failed {
		t.Fatal("a cached page did not replace the offline card")
	}

	// Try again goes to the network even while the monitor says offline.
	brwRetry(config, elem, "https://example.com/again", true)
	waitBrowserText(t, config, func(s string) bool { return strings.Contains(s, "example.com/again") })
	if got := atomic.LoadInt32(calls); got != before+1 {
		t.Fatalf("Try again made %d fetches, want 1", got-before)
	}
}

// A failure published by the fetch draws the card; the next good page clears
// it.
func TestBrowserFailureIsPublishedAndCleared(t *testing.T) {
	brwSetOnlineForTest(t, true)
	fail := brwNewFailure(brwFailNotFound, "https://nope.example/", "")
	config, elem, _ := browserCacheFixture(t, func(rawURL string) browserPage {
		if strings.Contains(rawURL, "nope") {
			return browserPage{Text: fail.text(), Fail: &fail}
		}
		return browserPage{Text: "Title: fine\n\nfine body", FinalURL: rawURL}
	})
	loadTextBrowserURL(config, elem, "https://nope.example/")
	waitBrowserText(t, config, func(s string) bool { return strings.HasPrefix(s, "Could not load") })
	if f, ok := brwFailureFor(elem.Variable); !ok || f.Kind != brwFailNotFound {
		t.Fatalf("failure after a failed load = %+v, %v", f, ok)
	}
	loadTextBrowserURL(config, elem, "https://fine.example/")
	waitBrowserText(t, config, func(s string) bool { return strings.Contains(s, "fine body") })
	if _, ok := brwFailureFor(elem.Variable); ok {
		t.Fatal("a good page left the failure card up")
	}
}

// The card's D-pad walks its buttons and lets the press through at the edges,
// so the toolbar and the chip row stay reachable.
func TestBrwCardMoveFallsThroughAtTheEdges(t *testing.T) {
	brwResetForTest()
	t.Cleanup(brwResetForTest)
	brwCard.count = 3
	brwCard.rows = [3]int{0, 1, 1}
	steps := []struct {
		dx, dy int
		moved  bool
		focus  int
	}{
		{0, -1, false, 0},
		{0, 1, true, 1},
		{1, 0, true, 2},
		{1, 0, false, 2},
		{-1, 0, true, 1},
		{0, 1, false, 1},
		{0, -1, true, 0},
	}
	for i, s := range steps {
		if moved := brwCardMove(s.dx, s.dy); moved != s.moved || brwCard.focus != s.focus {
			t.Fatalf("step %d (%d,%d): moved=%v focus=%d, want %v %d", i, s.dx, s.dy, moved, brwCard.focus, s.moved, s.focus)
		}
	}
}

// The footer names the card's and the game's own keys while they own the page
// area, and every variant keeps a way back.
func TestBrwFooterHintsFollowThePageArea(t *testing.T) {
	resetBrowserStateForTest()
	t.Cleanup(resetBrowserStateForTest)
	// The game's hints follow the last input device; pin the pad.
	prevKeyboard := jlandRT.keyboard
	jlandRT.keyboard = false
	t.Cleanup(func() { jlandRT.keyboard = prevKeyboard })
	scene := SceneConfig{Name: browserSceneName}
	if label, _ := hintLabel(footerHints(&Config{}, scene), "Left/Right"); label != "Previous / next link" {
		t.Fatalf("plain browser footer changed: %q", label)
	}
	fail := brwNewFailure(brwFailOffline, "https://example.com/", "")
	brwSetFailure(browserContentVariable, &fail)
	hints := footerHints(&Config{}, scene)
	if label, _ := hintLabel(hints, "A"); label != "Select" {
		t.Fatalf("card footer A = %q", label)
	}
	if label, ok := hintLabel(hints, "B"); !ok || label == "" {
		t.Fatal("the card footer offers no way back")
	}
	brwGame.active = true
	hints = footerHints(&Config{}, scene)
	if label, _ := hintLabel(hints, "Y"); label != "Mine" {
		t.Fatalf("game footer Y = %q", label)
	}
	if label, ok := hintLabel(hints, "B"); !ok || label == "" {
		t.Fatal("the game footer offers no way back")
	}
}

// JukaLand draws into whatever rectangle it is given — the browser's page
// area — and leaves everything outside it alone.
func TestJlandDrawsOnlyInsideItsRect(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := layoutTestConfig(t)
	frame := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)
	prev := jlandRT
	t.Cleanup(func() { jlandRT = prev })
	jlandRT = jlandRuntime{game: newJlandGame(11)}

	for _, view := range []sdl.Rect{{X: 31, Y: 140, W: 1218, H: 420}, {X: 200, Y: 300, W: 420, H: 260}} {
		renderer.SetDrawColor(1, 2, 3, 255)
		renderer.Clear()
		jlandTick(config, view, false)
		jlandDraw(renderer, config, view, view.Y+view.H-10)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels: %v", err)
		}
		inside, outside := 0, 0
		for y := int32(0); y < probeH; y += 4 {
			for x := int32(0); x < probeW; x += 4 {
				i := (y*probeW + x) * 4
				painted := frame.buf[i] != 3 || frame.buf[i+1] != 2 || frame.buf[i+2] != 1
				if pointInRect(x, y, view) {
					if painted {
						inside++
					}
				} else if painted {
					outside++
				}
			}
		}
		if inside == 0 {
			t.Errorf("view %+v: the game drew nothing", view)
		}
		if outside > 0 {
			t.Errorf("view %+v: %d sampled pixels outside the view were painted", view, outside)
		}
	}
}

// A finger's tap reaches the game only as a click event (it never shows in the
// polled mouse state): a tap in reach digs that cell, a tap far away walks the
// player there.
func TestJlandTapDigsAndWalks(t *testing.T) {
	prev := jlandRT
	t.Cleanup(func() { jlandRT = prev })
	world := jlandFlatWorld(40, 40, 30)
	jlandRT = jlandRuntime{game: jlandStandingGame(world, 10.5), tile: 32, camY: 20}
	view := sdl.Rect{X: 0, Y: 0, W: 1280, H: 720}
	ground := int32((30-20)*32 + 16)
	if !jlandTap(view, 11*32+16, ground) {
		t.Fatal("a tap on the world was not taken")
	}
	if world.at(11, 30) != jlandAir {
		t.Fatalf("the tapped cell is still %v", world.at(11, 30))
	}
	jlandTap(view, 35*32+16, ground)
	if !jlandRT.walking || jlandRT.walkTargetX != 35.5 {
		t.Fatalf("a far tap did not start a walk (walking=%v target=%.1f)", jlandRT.walking, jlandRT.walkTargetX)
	}
	jlandRT.noPointer = sdl.Rect{X: 0, Y: 600, W: 1280, H: 120}
	if jlandTap(view, 11*32+16, 650) {
		t.Fatal("a tap on the footer band acted on the world under it")
	}
}

// The world is saved when its screen is left, however it is left.
func TestJlandDepartureSavesTheWorld(t *testing.T) {
	prevRT, prevCache := jlandRT, userConfigCache
	t.Cleanup(func() { jlandRT, userConfigCache = prevRT, prevCache })
	userConfigCache = &UserConfig{}
	jlandRT = jlandRuntime{game: newJlandGame(5)}
	jlandRT.game.World.set(3, 3, jlandBrick)
	config := &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}, {Name: "JukaLand"}}}
	jlandSceneDeparture(config, "Main")
	if userConfigCache.JukaLand != nil {
		t.Fatal("leaving another screen saved JukaLand")
	}
	jlandSceneDeparture(config, "JukaLand")
	saved := userConfigCache.JukaLand
	if saved == nil {
		t.Fatal("leaving JukaLand did not save the world")
	}
	if back := jlandFromState(saved); back == nil || back.World.at(3, 3) != jlandBrick {
		t.Fatal("the saved world lost the block placed before leaving")
	}
	if jlandRT.game.dirty {
		t.Fatal("the world still reads as unsaved")
	}
	// A save is not repeated when nothing changed.
	userConfigCache.JukaLand = nil
	jlandSceneDeparture(config, "JukaLand")
	if userConfigCache.JukaLand != nil {
		t.Fatal("an unchanged world was saved again")
	}
}
