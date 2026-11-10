package main

import (
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
