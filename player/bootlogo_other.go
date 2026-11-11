//go:build !linux

package main

// bootlogo_other.go — no firmware boot logo off Linux.
//
// The boot logo JukaHub can replace is the TrimUI bootloader's (bootlogo.go,
// bootlogo_linux.go). A desktop's firmware splash is not JukaHub's to touch,
// so here the Settings row stays hidden and every probe says why.

func bootLogoPlatformSupported() bool { return false }

func bootLogoOpenPlatform(bool) (bootLogoTarget, error) {
	return nil, errBootLogoUnsupported
}
