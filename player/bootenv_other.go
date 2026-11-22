//go:build !linux

package main

// bootenv_other.go — no bootloader settings off Linux.
//
// Silent boot changes the TrimUI bootloader's environment (bootenv.go,
// bootenv_linux.go). A desktop's firmware is not JukaHub's to touch, so here
// the Settings row stays hidden and every look says why.

func bootEnvPlatformSupported() bool { return false }

func bootEnvLoadPlatform() (bootEnvPlan, error) { return bootEnvPlan{}, errBootEnvUnsupported }

func bootEnvWriteCopyPlatform(bootEnvPlan, int, []byte, []byte) (bool, error) {
	return false, errBootEnvUnsupported
}
