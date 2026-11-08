package main

// Tests for Watch Party's LAN discovery: the reply format, broadcast address
// arithmetic, a real query/answer round trip on the loopback interface, and
// the expiry of rooms that stop answering.

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestPartyParseBeacon(t *testing.T) {
	from := &net.UDPAddr{IP: net.ParseIP("192.168.1.42"), Port: 50000}
	room, ok := partyParseBeacon([]byte(`{"v":1,"app":"JukaHub","code":"abcd2","room":"Movie\u0007 Night","host":"Alice","port":8318,"members":3,"title":"Dune"}`), from)
	if !ok {
		t.Fatal("a well-formed answer was refused")
	}
	if room.Addr != "192.168.1.42:8318" || room.Code != "ABCD2" || room.Room != "Movie Night" ||
		room.Host != "Alice" || room.Members != 3 || room.Title != "Dune" {
		t.Fatalf("parsed room = %+v", room)
	}
	if got := room.Invite(); got != "192.168.1.42:8318 ABCD2" {
		t.Fatalf("invite = %q", got)
	}
	for _, bad := range []string{
		`{"v":2,"app":"JukaHub","code":"ABCD2","port":8318}`,
		`{"v":1,"app":"Other","code":"ABCD2","port":8318}`,
		`{"v":1,"app":"JukaHub","code":"ABC","port":8318}`,
		`{"v":1,"app":"JukaHub","code":"ABCD2","port":0}`,
		`{"v":1,"app":"JukaHub","code":"ABCD2","port":70000}`,
		`not json`,
	} {
		if _, ok := partyParseBeacon([]byte(bad), from); ok {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, ok := partyParseBeacon([]byte(`{"v":1,"app":"JukaHub","code":"ABCD2","port":8318}`), nil); ok {
		t.Error("accepted an answer with no sender")
	}
	// A hostile member count is clamped to what a room can hold.
	big, ok := partyParseBeacon([]byte(`{"v":1,"app":"JukaHub","code":"ABCD2","port":8318,"members":5000}`), from)
	if !ok || big.Members != partyMaxMembers {
		t.Fatalf("member count = %d, want it clamped to %d", big.Members, partyMaxMembers)
	}
}

func TestPartyDirectedBroadcast(t *testing.T) {
	_, n24, err := net.ParseCIDR("192.168.1.23/24")
	if err != nil {
		t.Fatal(err)
	}
	if got := partyDirectedBroadcast(n24); got != "192.168.1.255" {
		t.Fatalf("/24 broadcast = %q", got)
	}
	n16 := &net.IPNet{IP: net.ParseIP("10.0.5.7"), Mask: net.CIDRMask(16, 32)}
	if got := partyDirectedBroadcast(n16); got != "10.0.255.255" {
		t.Fatalf("/16 broadcast = %q", got)
	}
	// Some platforms report an IPv4 mask in 16 bytes.
	n16wide := &net.IPNet{IP: net.ParseIP("10.0.5.7").To4(), Mask: net.CIDRMask(112, 128)}
	if got := partyDirectedBroadcast(n16wide); got != "10.0.255.255" {
		t.Fatalf("/16 broadcast from a 16-byte mask = %q", got)
	}
	v6 := &net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}
	if got := partyDirectedBroadcast(v6); got != "" {
		t.Fatalf("an IPv6 network has no broadcast address, got %q", got)
	}
}

// A host answers a query on the loopback interface, and a hidden room does not.
func TestPartyDiscoveryRoundTrip(t *testing.T) {
	prevListen, prevTargets := partyBeaconListenAddr, partyDiscoveryTargetsOverride
	partyBeaconListenAddr = "127.0.0.1:0"
	t.Cleanup(func() {
		partyBeaconListenAddr = prevListen
		partyDiscoveryTargetsOverride = prevTargets
	})
	var mu sync.Mutex
	visible := true
	beacon, err := startPartyBeacon(func() (partyBeaconInfo, bool) {
		mu.Lock()
		defer mu.Unlock()
		return partyBeaconInfo{V: 1, App: "JukaHub", Code: "ABCD2", Room: "Movie Night", Host: "Alice", Port: 8318, Members: 2}, visible
	})
	if err != nil {
		t.Skipf("cannot open a UDP socket here: %v", err)
	}
	defer beacon.Stop()
	partyDiscoveryTargetsOverride = []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(beacon.Port()))}

	rooms, err := partyDiscoverOnce(context.Background(), 800*time.Millisecond)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(rooms) != 1 || rooms[0].Code != "ABCD2" || rooms[0].Addr != "127.0.0.1:8318" || rooms[0].Members != 2 {
		t.Fatalf("rooms = %+v, want the one loopback room", rooms)
	}

	mu.Lock()
	visible = false
	mu.Unlock()
	rooms, _ = partyDiscoverOnce(context.Background(), 400*time.Millisecond)
	if len(rooms) != 0 {
		t.Fatalf("a hidden room answered: %+v", rooms)
	}
}

// A cancelled scan stops at once instead of waiting out its window.
func TestPartyDiscoveryHonoursCancel(t *testing.T) {
	prevTargets := partyDiscoveryTargetsOverride
	partyDiscoveryTargetsOverride = []string{"127.0.0.1:9"}
	t.Cleanup(func() { partyDiscoveryTargetsOverride = prevTargets })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, _ = partyDiscoverOnce(ctx, 5*time.Second)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("a cancelled scan ran for %s", time.Since(start))
	}
}

func TestPartyNearbyMergeExpires(t *testing.T) {
	partyNearbyReset()
	t.Cleanup(partyNearbyReset)
	now := time.Now()
	partyNearbyMerge([]partyNearbyRoom{
		{Addr: "10.0.0.2:8318", Code: "ABCD2", Room: "Bravo"},
		{Addr: "10.0.0.3:8318", Code: "WXYZ3", Room: "Alpha"},
	}, nil, now)
	rooms, scans := partyNearbySnapshot()
	if scans != 1 || len(rooms) != 2 || rooms[0].Room != "Alpha" {
		t.Fatalf("rooms = %+v (scans %d), want both, sorted by name", rooms, scans)
	}
	if r, ok := partyFindNearbyCode("abcd2"); !ok || r.Addr != "10.0.0.2:8318" {
		t.Fatalf("code lookup = %+v %v", r, ok)
	}
	partyNearbyMerge(nil, nil, now.Add(partyNearbyTTL+time.Second))
	if rooms, _ := partyNearbySnapshot(); len(rooms) != 0 {
		t.Fatalf("rooms that stopped answering are still listed: %+v", rooms)
	}
}
