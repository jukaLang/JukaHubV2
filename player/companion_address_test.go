package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCompanionBrowserAddresses(t *testing.T) {
	for _, tc := range []struct {
		host string
		port int
		base string
	}{
		{"192.168.1.42", 80, "http://192.168.1.42"},
		{"192.168.1.42", 8317, "http://192.168.1.42:8317"},
	} {
		if got := companionBaseURL(tc.host, tc.port); got != tc.base {
			t.Errorf("base address = %q, want %q", got, tc.base)
		}
		if got := companionURL(tc.host, tc.port, "123456"); got != tc.base+"/?p=123456" {
			t.Errorf("pairing link = %q, want %q", got, tc.base+"/?p=123456")
		}
	}
}

func TestCompanionTypedPINOpensPage(t *testing.T) {
	srv, _ := newTestCompanion(t)
	handler := srv.handler()
	locked := httptest.NewRecorder()
	handler.ServeHTTP(locked, httptest.NewRequest(http.MethodGet, "http://192.168.1.42/", nil))
	if locked.Code != http.StatusUnauthorized || !strings.Contains(locked.Body.String(), "name=p") {
		t.Fatalf("unpaired browser did not get the PIN form: %d", locked.Code)
	}
	paired := httptest.NewRecorder()
	handler.ServeHTTP(paired, httptest.NewRequest(http.MethodGet, "http://192.168.1.42/?p=123456", nil))
	if paired.Code != http.StatusOK || !strings.Contains(paired.Body.String(), "JukaHub Companion") {
		t.Fatalf("typed PIN did not open the companion: %d", paired.Code)
	}
}

func TestCompanionBrowserPortServesPairingPage(t *testing.T) {
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	_ = reserve.Close()
	t.Setenv("JUKAHUB_COMPANION_PORT", strconv.Itoa(port))
	srv, _ := newTestCompanion(t)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/?p=123456", srv.BrowserPort()))
	if err != nil {
		t.Fatalf("shown browser port did not serve the page: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("shown browser port returned %d", response.StatusCode)
	}
}
