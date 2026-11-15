//go:build linux

package main

// wifi_resume_linux.go — the parts of the wake watcher (wifi_resume.go) that
// read the kernel's network state and signal the DHCP client.

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// wifiIfaceRouted reports whether iface has an IPv4 address and the default
// route goes through it. A link that went down and up keeps its address but
// loses the route, so the address alone does not say the network works. When
// the route table cannot be read the answer is yes: DHCP is not asked again on
// a guess.
func wifiIfaceRouted(iface string) bool {
	if !wifiIfaceHasIPv4(iface) {
		return false
	}
	table, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return true
	}
	return procRouteHasDefault(string(table), iface)
}

// wifiIfaceHasIPv4 reports whether iface holds a routable IPv4 address (not a
// link-local 169.254 one a failed DHCP leaves behind).
func wifiIfaceHasIPv4(iface string) bool {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return false
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() && !ip4.IsLoopback() {
			return true
		}
	}
	return false
}

// wifiRenewLease asks for the lease again and says what it did, for the log
// line: a udhcpc already serving the interface gets SIGUSR1 (renew now), so the
// firmware's own client keeps its job; with none running, the one-shot client
// a join runs (wifiRunDHCP).
//
// It does not log: its callers (the wake watcher, the keeper) write one line
// each with the answer, so the log does not say the same thing twice.
func wifiRenewLease(iface string) string {
	if wifiSignalRenew(iface) {
		return "DHCP renewed"
	}
	if _, err := wifiRunDHCP(iface); err != nil {
		logDebug("wifi", "DHCP on %s: %v", iface, err)
		return "DHCP failed"
	}
	return "DHCP asked again"
}

// wifiSignalRenew sends SIGUSR1 (renew now) to a udhcpc already serving iface
// and reports whether there was one to ask.
func wifiSignalRenew(iface string) bool {
	if pid := udhcpcPIDFor(iface); pid > 0 {
		if err := syscall.Kill(pid, syscall.SIGUSR1); err == nil {
			logDebug("wifi", "asked udhcpc (pid %d) to renew the lease on %s", pid, iface)
			return true
		}
	}
	return false
}

// udhcpcPIDFor finds a running udhcpc serving iface, or 0.
func udhcpcPIDFor(iface string) int {
	return procPIDMatching(func(argv []string) bool { return udhcpcServes(argv, iface) })
}

// supplicantPIDFor finds a running wpa_supplicant for iface, or 0: one that
// does not answer is still there, and a second one on the same radio fights it
// (ensureWifiBackend).
func supplicantPIDFor(iface string) int {
	return procPIDMatching(func(argv []string) bool { return supplicantServes(argv, iface) })
}

// procPIDMatching finds the first process (not this one, not init) whose
// command line match accepts, or 0.
func procPIDMatching(match func(argv []string) bool) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	self := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		if match(strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")) {
			return pid
		}
	}
	return 0
}
