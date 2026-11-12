package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withProbeServer points the probe at one test server for the duration of a
// test and restores the real endpoints afterwards.
func withProbeServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	oldEndpoints, oldClient := netProbeEndpoints, netProbeClient
	netProbeEndpoints = []string{srv.URL + "/generate_204"}
	netProbeClient = &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	t.Cleanup(func() {
		netProbeEndpoints, netProbeClient = oldEndpoints, oldClient
	})
	return srv
}

// A real network answers the generate_204 endpoint with 204 and no body.
func TestProbeConnectivityOnline(t *testing.T) {
	withProbeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/generate_204" {
			t.Errorf("probe asked for %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	result := probeConnectivity()
	if !result.online || result.portal != "" || result.reason != "" {
		t.Fatalf("probe = %+v, want a clean online answer", result)
	}
}

// A portal hijacks the probe and returns its own login page with 200: the
// device is connected but not online, and the page is where the user must go.
func TestProbeConnectivityCaptivePortalPage(t *testing.T) {
	srv := withProbeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>Hotel Wi-Fi — sign in</body></html>"))
	})
	result := probeConnectivity()
	if result.online {
		t.Fatal("a portal page was counted as internet access")
	}
	if result.portal != srv.URL+"/generate_204" {
		t.Fatalf("portal = %q, want the page that answered", result.portal)
	}
	if result.reason != "sign-in required" {
		t.Fatalf("reason = %q, want sign-in required", result.reason)
	}
}

// Many portals redirect to a login page; the redirect target is what "Sign in"
// must open, not the probe endpoint.
func TestProbeConnectivityPortalRedirect(t *testing.T) {
	srv := withProbeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/login") {
			_, _ = w.Write([]byte("login"))
			return
		}
		http.Redirect(w, r, "/login?next=wifi", http.StatusFound)
	})
	result := probeConnectivity()
	if result.online || result.portal != srv.URL+"/login?next=wifi" {
		t.Fatalf("probe = %+v, want the redirect target %q", result, srv.URL+"/login?next=wifi")
	}
}

// A server error is not a portal and not internet: say offline, without a page
// to open.
func TestProbeConnectivityServerError(t *testing.T) {
	withProbeServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	})
	result := probeConnectivity()
	if result.online || result.portal != "" {
		t.Fatalf("probe = %+v, want offline with no portal", result)
	}
	if result.reason == "" {
		t.Fatal("offline answer has no reason")
	}
}

// An unreachable endpoint (connection refused) must answer offline promptly.
func TestProbeConnectivityUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL + "/generate_204"
	srv.Close() // now nothing listens on the port

	oldEndpoints, oldClient := netProbeEndpoints, netProbeClient
	netProbeEndpoints = []string{url}
	netProbeClient = &http.Client{Timeout: 2 * time.Second}
	t.Cleanup(func() { netProbeEndpoints, netProbeClient = oldEndpoints, oldClient })

	result := probeConnectivity()
	if result.online || result.portal != "" || result.reason == "" {
		t.Fatalf("probe = %+v, want offline", result)
	}
}

// Netplay rooms come from the network (netplay_rooms.go): the lobby list and
// LAN beacon replies must parse in both lobby shapes and never let a path,
// a "|" or a shell-ish host through to RetroArch's argv.
func TestNetplayRoomParsers(t *testing.T) {
	lobby := `[
	 {"fields":{"username":"ann","country":"us","game_name":"Pokemon Red (USA)","core_name":"Gambatte",
	  "ip":"203.0.113.5","port":55435,"mitm_ip":"","mitm_port":0,"mitm_session":"","has_password":false}},
	 {"username":"bo","game_name":"Tetris","core_name":"mGBA","ip":"","port":0,
	  "mitm_ip":"relay.example.org","mitm_port":55435,"mitm_session":"q83vEjRWeJASNFZ4"},
	 {"username":"x","game_name":"Bad","core_name":"mGBA","ip":"1.2.3.4|9|s","port":55435},
	 {"username":"y","game_name":"BadRelay","core_name":"mGBA","mitm_ip":"r.example","mitm_port":1,"mitm_session":"a|b"},
	 {"username":"z","game_name":"Closed","core_name":"mGBA","ip":"1.2.3.4","port":55435,"connectable":false}
	]`
	rooms, err := parseNetplayLobby([]byte(lobby))
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 2 {
		t.Fatalf("got %d rooms, want 2: %+v", len(rooms), rooms)
	}
	if r := rooms[0]; r.Host != "203.0.113.5" || r.Port != 55435 || r.Relay != "" || r.Core != "Gambatte" {
		t.Fatalf("direct room = %+v", r)
	}
	if r := rooms[1]; r.Host != "relay.example.org" || r.Relay != "q83vEjRWeJASNFZ4" {
		t.Fatalf("relayed room = %+v", r)
	}
	plan := netplayPlan{RetroArch: "ra", Core: "c.so", Session: netplaySession{
		Role: netplayJoin, Game: libraryGame{Path: "g.gb"}, Host: rooms[1].Host, Port: rooms[1].Port, Relay: rooms[1].Relay}}
	if _, args := netplayCommand(plan); strings.Join(args, " ") != "-L c.so g.gb --connect relay.example.org|55435|q83vEjRWeJASNFZ4" {
		t.Fatalf("relay argv = %q", args)
	}

	from := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 9), Port: 40000}
	good := `{"v":1,"app":"JukaHub","nick":"ann","title":"Tetris","file":"Tetris (World).gb","size":32768,"core":"gambatte_libretro","port":55435}`
	room, ok := parseNetplayBeacon([]byte(good), from)
	if !ok || room.Host != "192.168.1.9" || room.File != "Tetris (World).gb" || room.Core != "gambatte_libretro" {
		t.Fatalf("beacon = %+v ok=%v", room, ok)
	}
	for _, bad := range []string{
		`{"v":1,"app":"JukaHub","file":"a.gb","core":"../../bin/sh","port":55435}`,
		`{"v":1,"app":"JukaHub","file":"a.gb","core":"x/y","port":55435}`,
		`{"v":1,"app":"JukaHub","file":"a.gb","core":"mgba_libretro","port":70000}`,
		`{"v":2,"app":"JukaHub","file":"a.gb","core":"mgba_libretro","port":55435}`,
	} {
		if _, ok := parseNetplayBeacon([]byte(bad), from); ok {
			t.Fatalf("accepted %s", bad)
		}
	}
	if room, ok := parseNetplayBeacon([]byte(`{"v":1,"app":"JukaHub","file":"../../etc/x.gb","core":"mgba_libretro","port":1}`), from); !ok || room.File != "x.gb" {
		t.Fatalf("file not reduced to a base name: %+v", room)
	}
	if netplayCoreNorm("Genesis Plus GX") != netplayCoreNorm("genesis_plus_gx_libretro.so") ||
		netplayCoreNorm("Beetle PCE Fast") != netplayCoreNorm("mednafen_pce_fast_libretro.dll") {
		t.Fatal("lobby core names do not fold onto core files")
	}
}
