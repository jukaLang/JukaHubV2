//go:build !linux

package main

// wifi_resume_other.go — off Linux the operating system brings Wi-Fi back
// after a sleep and JukaHub only probes fast (wifi_resume.go never starts its
// watcher there). These keep the shared code building.

// wifiIfaceRouted answers yes: there is nothing here to ask DHCP for.
func wifiIfaceRouted(string) bool { return true }

// wifiIfaceHasIPv4 is never asked off Linux.
func wifiIfaceHasIPv4(string) bool { return true }

// wifiRenewLease does nothing off Linux.
func wifiRenewLease(string) string { return "" }

// udhcpcPIDFor finds nothing: there is no udhcpc off Linux.
func udhcpcPIDFor(string) int { return 0 }

// wifiSignalRenew has no udhcpc to signal off Linux.
func wifiSignalRenew(string) bool { return false }

// supplicantPIDFor finds nothing: off Linux no wpa_supplicant is started.
func supplicantPIDFor(string) int { return 0 }
