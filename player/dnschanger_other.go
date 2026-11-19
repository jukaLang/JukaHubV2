//go:build !linux

package main

// dnschanger_other.go — off Linux the DNS choice covers JukaHub's own
// lookups only (dnsResolverDial): a PC's DNS is the PC's own setting, and
// changing it would need the system's permission prompt for every change.

import "net/netip"

func dnsSystemSupported() bool { return false }

func dnsSystemHolds() bool { return false }

func dnsSystemReconcile(string, []netip.Addr) dnsSysResult { return dnsSysResult{} }

func dnsSystemRelease(string) (string, error) { return "nothing to put back", nil }
