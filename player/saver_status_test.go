package main

// The idle screens' status chips: what the Wi-Fi chip says for every state the
// radio and the supplicant can be in, and that the reading comes from a worker
// rather than the render thread.

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// saverPinWifiForTest gives the Wi-Fi chip a fixed answer for one test: the
// collector is replaced, so a render test never runs wpa_cli or rfkill on the
// machine the suite runs on, and the reading cannot change between frames.
func saverPinWifiForTest(t *testing.T) saverWifiState {
	t.Helper()
	probe := saverWifiProbe{hardware: true, statusOK: true, status: wifiStatus{State: "COMPLETED", SSID: "Juka Home", Connected: true}, level: 3}
	pinned := saverWifiDecide(probe)
	prevCollect := saverWifiCollect
	saverWifiCollect = func() saverWifiProbe { return probe }
	saverWifi.mu.Lock()
	prevState, prevAsked := saverWifi.state, saverWifi.asked
	saverWifi.state, saverWifi.asked = pinned, time.Now()
	saverWifi.mu.Unlock()
	t.Cleanup(func() {
		// A refresh a frame started may still be finishing: wait it out, so
		// the restored reading is the last word.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			saverWifi.mu.Lock()
			running := saverWifi.running
			saverWifi.mu.Unlock()
			if !running {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		saverWifiCollect = prevCollect
		saverWifi.mu.Lock()
		saverWifi.state, saverWifi.asked = prevState, prevAsked
		saverWifi.mu.Unlock()
	})
	return pinned
}

func TestSaverWifiDecide(t *testing.T) {
	connected := wifiStatus{State: "COMPLETED", SSID: "Juka Home", Connected: true}
	cases := []struct {
		name  string
		probe saverWifiProbe
		kind  saverWifiKind
		ssid  string
		level int
	}{
		{"desktop on ethernet", saverWifiProbe{online: true}, saverWifiWired, "", 0},
		{"no radio, no network", saverWifiProbe{}, saverWifiOffline, "", 0},
		{"radio switched off", saverWifiProbe{hardware: true, radioKnown: true, radioOn: false, statusOK: true, status: connected}, saverWifiOff, "", 0},
		{"connected with bars", saverWifiProbe{hardware: true, radioKnown: true, radioOn: true, statusOK: true, status: connected, level: 4}, saverWifiConnected, "Juka Home", 4},
		{"connected, strength unknown", saverWifiProbe{hardware: true, statusOK: true, status: connected, level: statusWiFiNoLink}, saverWifiConnected, "Juka Home", statusWiFiUnknown},
		{"supplicant up, not associated", saverWifiProbe{hardware: true, statusOK: true, status: wifiStatus{State: "DISCONNECTED"}}, saverWifiDisconnected, "", 0},
		{"scanning", saverWifiProbe{hardware: true, statusOK: true, status: wifiStatus{State: "SCANNING"}}, saverWifiDisconnected, "", 0},
		{"handshaking", saverWifiProbe{hardware: true, statusOK: true, status: wifiStatus{State: "4WAY_HANDSHAKE"}}, saverWifiConnecting, "", 0},
		{"interface disabled", saverWifiProbe{hardware: true, statusOK: true, status: wifiStatus{State: "INTERFACE_DISABLED"}}, saverWifiOff, "", 0},
		{"no supplicant, link measured", saverWifiProbe{hardware: true, level: 2}, saverWifiConnected, "", 2},
		{"no supplicant, no link", saverWifiProbe{hardware: true, level: statusWiFiNoLink}, saverWifiOff, "", 0},
	}
	for _, c := range cases {
		got := saverWifiDecide(c.probe)
		if got.kind != c.kind || got.ssid != c.ssid || (c.kind == saverWifiConnected && got.level != c.level) {
			t.Errorf("%s: got %+v, want kind %v ssid %q level %d", c.name, got, c.kind, c.ssid, c.level)
		}
	}
}

// The chip names the network and says the three off-states apart.
func TestSaverWifiLabelsAndGlyphs(t *testing.T) {
	cases := []struct {
		st    saverWifiState
		label string
		muted bool
		icon  string
		dim   bool
	}{
		{saverWifiState{kind: saverWifiConnected, ssid: "Juka Home"}, "Juka Home", false, "wifi", false},
		{saverWifiState{kind: saverWifiConnected}, "Connected", false, "wifi", false},
		{saverWifiState{kind: saverWifiDisconnected}, "Not connected", true, "wifi-off", true},
		{saverWifiState{kind: saverWifiOff}, "Wi-Fi off", true, "wifi-off", true},
		{saverWifiState{kind: saverWifiWired}, "Online", false, "globe", false},
		{saverWifiState{kind: saverWifiOffline}, "Offline", true, "wifi-off", true},
	}
	for _, c := range cases {
		label, muted := saverWifiLabel(c.st)
		icon, dim := saverWifiIcon(c.st)
		if label != c.label || muted != c.muted || icon != c.icon || dim != c.dim {
			t.Errorf("%+v: label %q muted %v icon %q dim %v; want %q %v %q %v", c.st, label, muted, icon, dim, c.label, c.muted, c.icon, c.dim)
		}
	}
	// Before the first reading there is no chip at all, not a guess.
	if label, _ := saverWifiLabel(saverWifiState{}); label != "" {
		t.Errorf("an unread state labelled itself %q", label)
	}
}

// The render thread only reads the cache: a due refresh runs the collector on
// a worker, and the reading is what the next frame gets.
func TestSaverWifiNowRefreshesOffThread(t *testing.T) {
	saverPinWifiForTest(t)
	calls := make(chan struct{}, 4)
	block := make(chan struct{})
	released := false
	release := func() {
		if !released {
			released = true
			close(block)
		}
	}
	// Runs before the pin's own cleanup, which waits for the worker to end.
	t.Cleanup(release)
	saverWifiCollect = func() saverWifiProbe {
		calls <- struct{}{}
		<-block
		return saverWifiProbe{hardware: true, statusOK: true, status: wifiStatus{State: "DISCONNECTED"}}
	}
	saverWifiInvalidate()
	before := saverWifiNow(time.Now())
	if before.kind != saverWifiConnected {
		t.Fatalf("the cached reading was not served while refreshing: %+v", before)
	}
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("an invalidated reading did not start a refresh")
	}
	// While the worker is still asking, another frame neither blocks nor
	// starts a second worker.
	saverWifiNow(time.Now())
	select {
	case <-calls:
		t.Fatal("a second refresh started while one was running")
	default:
	}
	release()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		saverWifi.mu.Lock()
		st, running := saverWifi.state, saverWifi.running
		saverWifi.mu.Unlock()
		if !running && st.kind == saverWifiDisconnected {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the refreshed reading never reached the cache")
}

// The status row really draws both chips in the top-right corner.
func TestSaverStatusRowDrawsChips(t *testing.T) {
	newProbeFrame(t)
	saverPinWifiForTest(t)
	ambientStatusCache.mu.Lock()
	prevAt, prevLast := ambientStatusCache.at, ambientStatusCache.last
	ambientStatusCache.at, ambientStatusCache.last = time.Now().Add(time.Hour), ambientStatus{battery: 64, charging: true}
	ambientStatusCache.mu.Unlock()
	t.Cleanup(func() {
		ambientStatusCache.mu.Lock()
		ambientStatusCache.at, ambientStatusCache.last = prevAt, prevLast
		ambientStatusCache.mu.Unlock()
	})

	config := &Config{}
	var left int32
	f := componentFrame(t, func(r *sdl.Renderer) {
		left = saverDrawStatusRow(r, config, probeW-40, 30, time.Now())
	})
	if left >= probeW-40-100 {
		t.Fatalf("the status row is only %dpx wide, want both chips", probeW-40-left)
	}
	changed := 0
	for y := int32(30); y < 30+saverChipSize().h; y++ {
		for x := left; x < probeW-40; x++ {
			if !f.isBG(x, y) {
				changed++
			}
		}
	}
	if changed < 500 {
		t.Fatalf("only %d pixels of the status row were drawn", changed)
	}
}
