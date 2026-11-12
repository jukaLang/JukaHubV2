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

	"github.com/miekg/dns"
)

func TestCompanionBrowserAddresses(t *testing.T) {
	for _, tc := range []struct {
		host string
		port int
		base string
	}{
		{"jukahub.local", 80, "http://jukahub.local"},
		{"jukahub.local", 8317, "http://jukahub.local:8317"},
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

func TestCompanionLocalNameAnswersBrowserQuery(t *testing.T) {
	service, err := companionMDNSRecords("192.168.1.42", 8317)
	if err != nil {
		t.Fatal(err)
	}
	records := service.Records(dns.Question{Name: "jukahub.local.", Qtype: dns.TypeA, Qclass: dns.ClassINET})
	if len(records) != 1 {
		t.Fatalf("host lookup returned %d records, want one A record", len(records))
	}
	a, ok := records[0].(*dns.A)
	if !ok || !a.A.Equal(net.ParseIP("192.168.1.42")) {
		t.Fatalf("host lookup = %v, want 192.168.1.42", records)
	}
	if service.HostName != "jukahub.local." || service.Port != 8317 {
		t.Fatalf("service = %s:%d", service.HostName, service.Port)
	}
}

func TestCompanionTypedPINOpensPage(t *testing.T) {
	srv, _ := newTestCompanion(t)
	handler := srv.handler()
	locked := httptest.NewRecorder()
	handler.ServeHTTP(locked, httptest.NewRequest(http.MethodGet, "http://jukahub.local/", nil))
	if locked.Code != http.StatusUnauthorized || !strings.Contains(locked.Body.String(), "name=p") {
		t.Fatalf("unpaired browser did not get the PIN form: %d", locked.Code)
	}
	paired := httptest.NewRecorder()
	handler.ServeHTTP(paired, httptest.NewRequest(http.MethodGet, "http://jukahub.local/?p=123456", nil))
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
