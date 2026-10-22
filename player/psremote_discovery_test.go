package main

// psremote_discovery_test.go — parsing a console's SRCH answer and one real
// scan against a fake responder bound to loopback.

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testPS4Response = "HTTP/1.1 200 Ok\r\n" +
	"host-id: 0A1B2C3D4E5F\r\n" +
	"host-type: PS4\r\n" +
	"host-name: Living Room\r\n" +
	"host-request-port: 997\r\n" +
	"device-discovery-protocol-version: 00020020\r\n" +
	"system-version: 11000000\r\n" +
	"running-app-name: \r\n\r\n"

const testPS5Response = "HTTP/1.1 620 Server Standby\r\n" +
	"host-id: FFEEDDCCBBAA\r\n" +
	"host-type: PS5\r\n" +
	"host-name: Studio\r\n" +
	"host-request-port: 9302\r\n" +
	"device-discovery-protocol-version: 00030010\r\n" +
	"system-version: 8050001\r\n\r\n"

func addr(t *testing.T, value string) net.Addr {
	t.Helper()
	resolved, err := net.ResolveUDPAddr("udp4", value)
	if err != nil {
		t.Fatalf("resolve %s: %v", value, err)
	}
	return resolved
}

func TestParseDiscoveryResponsePS4Ready(t *testing.T) {
	console, ok := psParseDiscoveryResponse([]byte(testPS4Response), addr(t, "192.168.1.40:987"))
	if !ok {
		t.Fatal("PS4 answer was not parsed")
	}
	if console.Host != "192.168.1.40" || console.Name != "Living Room" || console.PS5 {
		t.Fatalf("console = %+v", console)
	}
	if console.State != "ready" || console.Target != psTargetPS4FW10 {
		t.Fatalf("state=%q target=%d", console.State, console.Target)
	}
}

func TestParseDiscoveryResponsePS5Standby(t *testing.T) {
	console, ok := psParseDiscoveryResponse([]byte(testPS5Response), addr(t, "192.168.1.41:9302"))
	if !ok {
		t.Fatal("PS5 answer was not parsed")
	}
	if !console.PS5 || console.State != "standby" || console.Target != psTargetPS5_1 {
		t.Fatalf("console = %+v", console)
	}
	if console.Name != "Studio" {
		t.Fatalf("name = %q", console.Name)
	}
}

func TestParseDiscoveryResponseRejectsNoise(t *testing.T) {
	cases := [][]byte{
		[]byte("garbage"),
		[]byte("RES2"),
		[]byte("HTTP/1.1 abc OK\r\n\r\n"),
		[]byte(""),
	}
	for _, payload := range cases {
		if _, ok := psParseDiscoveryResponse(payload, addr(t, "192.168.1.1:1")); ok {
			t.Fatalf("%q should not parse as a console", payload)
		}
	}
	// A valid HTTP answer still needs an address to be useful.
	if _, ok := psParseDiscoveryResponse([]byte(testPS4Response), nil); ok {
		t.Fatal("an answer without a source address cannot be used")
	}
}

func TestDiscoveryPacketFormat(t *testing.T) {
	packet := psDiscoveryPacket(psProtoVersionPS5)
	text := string(packet)
	if !strings.HasPrefix(text, "SRCH * HTTP/1.1\n") || !strings.Contains(text, "00030010") {
		t.Fatalf("packet = %q", text)
	}
	if packet[len(packet)-1] != 0 {
		t.Fatalf("packet must end with NUL, got %q", text)
	}
}

func TestSortDiscoveredReadyFirst(t *testing.T) {
	list := []psDiscovered{
		{Name: "Zed", Host: "10.0.0.9", State: "standby"},
		{Name: "Bedroom", Host: "10.0.0.2", State: "ready"},
		{Name: "Alpha", Host: "10.0.0.3", State: "ready"},
		{Name: "Mystery", Host: "10.0.0.4", State: "unknown"},
	}
	sortDiscovered(list)
	if list[0].Name != "Alpha" || list[1].Name != "Bedroom" {
		t.Fatalf("ready consoles not first: %+v", list)
	}
	if list[2].Name != "Zed" || list[3].Name != "Mystery" {
		t.Fatalf("order after ready = %+v", list)
	}
}

func TestScanConsolesFindsFakeConsole(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer server.Close()
	serverPort := server.LocalAddr().(*net.UDPAddr).Port

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 512)
		_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, from, err := server.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !strings.HasPrefix(string(buf[:n]), "SRCH * HTTP/1.1") {
			return
		}
		_, _ = server.WriteToUDP([]byte(testPS5Response), from)
	}()

	oldTargets, oldEndpoints := psDiscoveryTargetsOverride, psDiscoveryEndpointsTest
	psDiscoveryTargetsOverride = []string{"127.0.0.1"}
	psDiscoveryEndpointsTest = []psDiscoveryEndpoint{{Port: serverPort, Version: psProtoVersionPS5}}
	t.Cleanup(func() {
		psDiscoveryTargetsOverride, psDiscoveryEndpointsTest = oldTargets, oldEndpoints
	})

	consoles, err := psScanConsoles(context.Background(), 700*time.Millisecond)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	<-done
	if len(consoles) != 1 {
		t.Fatalf("found %d consoles: %+v", len(consoles), consoles)
	}
	if consoles[0].Name != "Studio" || !consoles[0].PS5 || consoles[0].Host != "127.0.0.1" {
		t.Fatalf("console = %+v", consoles[0])
	}
}

func TestDiscoveryAddressesIncludeDefaultBroadcast(t *testing.T) {
	old := psDiscoveryTargetsOverride
	psDiscoveryTargetsOverride = nil
	t.Cleanup(func() { psDiscoveryTargetsOverride = old })
	addrs := psDiscoveryAddresses(987)
	if len(addrs) == 0 {
		t.Fatal("no addresses to scan")
	}
	if addrs[0] != net.JoinHostPort("255.255.255.255", strconv.Itoa(987)) {
		t.Fatalf("first address = %q", addrs[0])
	}
}
