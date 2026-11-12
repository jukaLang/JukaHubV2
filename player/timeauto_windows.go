//go:build windows

package main

// timeauto_windows.go — whether Windows sets its own clock from the internet,
// for Settings → Date & time. The Windows Time service keeps its mode in the
// registry: "NoSync" is Settings' "Set time automatically" off; NTP (a home
// PC) or NT5DS (a domain) is on. Reading it needs no administrator.

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// systemTimeAutoRead reports Windows' "Set time automatically" switch, and
// whether it could be read at all.
func systemTimeAutoRead() (on, known bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\W32Time\Parameters`, registry.QUERY_VALUE)
	if err != nil {
		return false, false
	}
	defer k.Close()
	v, _, err := k.GetStringValue("Type")
	if err != nil {
		return false, false
	}
	return !strings.EqualFold(strings.TrimSpace(v), "NoSync"), true
}
