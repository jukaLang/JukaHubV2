package main

// offlineaware_test.go — the offline classification, the press gate and the
// plain wording for resolver failures.

import (
	"errors"
	"net"
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// forceOfflineForTest pins the connectivity monitor offline for one test.
func forceOfflineForTest(t *testing.T) {
	t.Helper()
	netMon.mu.Lock()
	online, checked, reason := netMon.online, netMon.checked, netMon.reason
	netMon.online, netMon.checked, netMon.reason = false, true, "no route"
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.online, netMon.checked, netMon.reason = online, checked, reason
		netMon.mu.Unlock()
	})
}

// Internet screens are refused offline with a reason; local apps — JukaLand
// above all — and LAN tools stay available, because greying a game that never
// touches the network would be worse than saying nothing.
func TestOfflineSceneClassification(t *testing.T) {
	for _, name := range []string{"LiveTV", "Live TV", "Podcasts", "Shorts", "Tickers",
		"YouTube search", "Trending", "Speed Test", "Network Tools", "Ping"} {
		if offlineReasonFor(name) == "" {
			t.Errorf("%q was not recognised as needing the network", name)
		}
	}
	// The Media screen ("Tube") opens offline on the downloads it can play.
	for _, name := range []string{"JukaLand", "Browser", "MusicPlayer", "Files", "Notes",
		"Games", "Settings", "Alarm", "Terminal", "FTP", "SSH", "Companion", "Netplay",
		"Downloads", "Weather", "Tube", "Media", ""} {
		if reason := offlineReasonFor(name); reason != "" {
			t.Errorf("%q was greyed out offline (%q), but it works without internet", name, reason)
		}
	}
}

// The gate only refuses while the monitor says offline, and it carries the
// sentence the toast shows.
func TestOfflineSceneBlockGate(t *testing.T) {
	forceOfflineForTest(t)
	reason, blocked := offlineSceneBlock("LiveTV")
	if !blocked || !strings.Contains(reason, "WiFi") {
		t.Fatalf("LiveTV offline = %q, %v; want a WiFi reason", reason, blocked)
	}
	if reason, blocked := offlineSceneBlock("JukaLand"); blocked {
		t.Fatalf("JukaLand was blocked offline with %q", reason)
	}
	if reason, blocked := offlineSceneBlock("Browser"); blocked {
		t.Fatalf("the Browser was blocked offline with %q", reason)
	}

	// A tile can be blocked by the scene it opens even when its own label says
	// nothing about the network.
	tile := Element{Type: "button", Text: "Watch", Trigger: "change_scene:Podcasts"}
	if reason, blocked := offlineTileBlocked(tile); !blocked || !strings.Contains(reason, "Podcast") {
		t.Fatalf("a tile opening Podcasts = %q, %v", reason, blocked)
	}
	if reason, blocked := offlineTileBlocked(Element{Text: "JukaLand", Trigger: "change_scene:JukaLand"}); blocked {
		t.Fatalf("the JukaLand tile was blocked with %q", reason)
	}
}

// A resolver failure must never reach the screen as a syscall.
func TestPlainNetworkErrorHidesResolverDetail(t *testing.T) {
	forceOfflineForTest(t)
	err := &net.DNSError{Err: "no such host", Name: "api.github.com", IsNotFound: true}
	if got := plainNetworkError(err); got != offlinePlainNotice {
		t.Fatalf("offline DNS error = %q, want the plain notice", got)
	}
	if got := plainNetworkError(errors.New("dial tcp: lookup api.github.com: no such host")); got != offlinePlainNotice {
		t.Fatalf("raw lookup text = %q, want the plain notice", got)
	}
	// Back online, a resolver failure is still not worth the syscall text.
	netMon.mu.Lock()
	netMon.online = true
	netMon.mu.Unlock()
	if got := plainNetworkError(err); strings.Contains(got, "no such host") || got != offlinePlainNotice {
		t.Fatalf("online DNS error = %q, want the plain notice", got)
	}
	if got := plainNetworkError(errors.New("401 unauthorized")); got != "401 unauthorized" {
		t.Fatalf("non-network error = %q, want its own line", got)
	}
}

// A dimmed tile has to look dimmer than a live one, or the greying is invisible.
func TestOfflineMenuTileDimsAndTags(t *testing.T) {
	forceOfflineForTest(t)
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	config := probeBootConfig()
	tile := func(elem Element) sdl.Rect {
		renderer.SetDrawColor(20, 20, 24, 255)
		renderer.Clear()
		renderMenuTile(renderer, config, elem, false, false, 0, false)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
			t.Fatalf("ReadPixels failed: %v", err)
		}
		return sdl.Rect{X: elem.X, Y: elem.Y, W: 220, H: 110}
	}

	live := tile(Element{Type: "button", Text: "JukaLand", Trigger: "change_scene:JukaLand", X: 40, Y: 40})
	// The label sits in the lower third of the tile. A disabled tile inks it in
	// the tertiary colour, so that band is where the greying has to show; the
	// warn badge in the corner would only add brightness to a whole-tile measure.
	labelBand := sdl.Rect{X: live.X + 8, Y: live.Y + live.H*2/3, W: live.W - 16, H: live.H / 3}
	liveLit := f.litIn(labelBand)
	liveTag := f.litIn(tagBand(live))

	blockedElem := Element{Type: "button", Text: "Live TV", Trigger: "change_scene:LiveTV", X: 40, Y: 40}
	tile(blockedElem)
	blocked := sdl.Rect{X: blockedElem.X, Y: blockedElem.Y, W: live.W, H: live.H}
	blockedLit := f.litIn(labelBand)
	blockedTag := f.litIn(tagBand(blocked))
	if liveLit == 0 || blockedLit == 0 {
		t.Fatalf("the label band drew nothing (live %d, blocked %d lit samples)", liveLit, blockedLit)
	}
	if blockedLit >= liveLit {
		t.Fatalf("the offline label is not dimmer (blocked %d, live %d lit samples)", blockedLit, liveLit)
	}
	if blockedTag <= liveTag {
		t.Fatalf("no Offline tag was drawn (blocked %d, live %d lit samples)", blockedTag, liveTag)
	}
}

// tagBand is the top-right corner where the Offline pill is painted.
func tagBand(rect sdl.Rect) sdl.Rect {
	return sdl.Rect{X: rect.X + rect.W - 96, Y: rect.Y + 4, W: 92, H: 30}
}

// litIn counts pixels in rect that are meaningfully brighter than the backdrop,
// which is what "the tile is drawn" and "the tile is dimmed" both mean here.
func (f *probeFrame) litIn(rect sdl.Rect) int {
	n := 0
	for y := rect.Y; y < rect.Y+rect.H && y < probeH; y++ {
		for x := rect.X; x < rect.X+rect.W && x < probeW; x++ {
			i := (int(y)*int(probeW) + int(x)) * 4
			lum := int(f.buf[i+2]) + int(f.buf[i+1]) + int(f.buf[i])
			if lum > 210 { // the 20,20,24 backdrop sums to 64
				n++
			}
		}
	}
	return n
}
