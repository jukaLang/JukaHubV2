//go:build !linux && !windows

package main

// vpn_other.go — the VPN where JukaHub has no tunnel: the rows explain it and
// nothing runs (vpn.go).

import "errors"

func handleVPNCommand([]string) (bool, int) { return false, 0 }

func vpnPlatformSupport() (bool, string) {
	return false, "The VPN runs on the handheld and on Windows"
}

func vpnPlatformAuto() bool { return false }

func vpnPlatformRecover() {}

func vpnPlatformApply([]byte) error { return errors.New("the VPN runs on the handheld and on Windows") }

func vpnPlatformStop() error { return nil }

func vpnPlatformProbe() vpnProbe { return vpnProbe{} }
