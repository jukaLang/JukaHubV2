package main

// Kept from the wake tests when the suite was cut to its core: where the
// console's credential may be sent.

import (
	"net"
	"testing"
	"time"
)

// psWakeTestSeams gives a test a system-picked source port on the loopback
// address, an empty memory of what consoles said, the default worker timings
// and no wake in flight, and puts everything back after.
func psWakeTestSeams(t *testing.T) {
	t.Helper()
	prevPorts, prevOverride := psWakeLocalPorts, psWakePortOverride
	prevListen, prevAddrs, prevTimings := psWakeListenIP, psWakeAddrOverride, psWakeWorkerTimings
	psWakeLocalPorts = [2]int{0, 0}
	psWakeListenIP = net.IPv4(127, 0, 0, 1)
	psWakeAddrOverride = nil
	psWakeWorkerTimings = psWakeOptions{}
	psSeen.mu.Lock()
	prevSeen, prevDone := psSeen.m, psSeen.scanDone
	psSeen.m, psSeen.scanDone = nil, time.Time{}
	psSeen.mu.Unlock()
	t.Cleanup(func() {
		psWakeLocalPorts, psWakePortOverride = prevPorts, prevOverride
		psWakeListenIP, psWakeAddrOverride, psWakeWorkerTimings = prevListen, prevAddrs, prevTimings
		psSeen.mu.Lock()
		psSeen.m, psSeen.scanDone = prevSeen, prevDone
		psSeen.mu.Unlock()
		psWakeState.mu.Lock()
		psWakeState.run = nil
		psWakeState.mu.Unlock()
	})
}

// The credential goes to one console's address or nowhere: never to the
// unspecified address, a multicast group or a broadcast address.
func TestPSWakeTargetRefusesWhatReachesMany(t *testing.T) {
	psWakeTestSeams(t)
	for _, host := range []string{"255.255.255.255", "224.0.0.251", "0.0.0.0", "::", "ff02::1"} {
		if addr, err := psWakeTarget(host, true); err == nil {
			t.Errorf("psWakeTarget(%q) = %v, want a refusal", host, addr)
		}
	}
	for _, host := range []string{"192.0.2.10", "2001:db8::1"} {
		if _, err := psWakeTarget(host, true); err != nil {
			t.Errorf("psWakeTarget(%q): %v", host, err)
		}
	}
	// The directed broadcast of a network this machine is on, when it has one.
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil || len(ipNet.Mask) != net.IPv4len {
				continue
			}
			if ones, _ := ipNet.Mask.Size(); ones >= 31 {
				continue
			}
			broadcast := make(net.IP, net.IPv4len)
			for i := range broadcast {
				broadcast[i] = ipNet.IP.To4()[i] | ^ipNet.Mask[i]
			}
			if _, err := psWakeTarget(broadcast.String(), false); err == nil {
				t.Errorf("the directed broadcast %s was accepted", broadcast)
			}
			return
		}
	}
}
