//go:build windows

package main

// volume_windows.go — the header volume button on Windows.
//
// pactl and amixer do not exist here, so the Quick Settings slider used to be
// dimmed ("no pactl/amixer") and a volume button would have had nothing to
// drive. winmm's waveOutSetVolume on device 0 is the classic, dependency-free
// way to set an application's own volume: since Vista it moves JukaHub's
// entry in the Windows volume mixer (its audio session), which every stream
// SDL opens belongs to. It is JukaHub's level, not the whole PC's — the
// system master stays the user's to set — and it has not been verified on a
// Windows machine from here.

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winmmDLL             = windows.NewLazySystemDLL("winmm.dll")
	procWaveOutGetVolume = winmmDLL.NewProc("waveOutGetVolume")
	procWaveOutSetVolume = winmmDLL.NewProc("waveOutSetVolume")
)

// volumePlatformBackend names Windows' own mixer when winmm can be loaded.
func volumePlatformBackend() string {
	if procWaveOutSetVolume.Find() != nil || procWaveOutGetVolume.Find() != nil {
		return ""
	}
	return "winmm"
}

// winmmSetVolume sets both channels of JukaHub's session to pct (0..100).
func winmmSetVolume(pct int) error {
	v := uint32(winmmLevelFor(pct))
	r, _, _ := procWaveOutSetVolume.Call(0, uintptr(v|v<<16))
	if r != 0 {
		return fmt.Errorf("waveOutSetVolume failed (MMRESULT %d)", r)
	}
	return nil
}

// winmmGetVolume reads JukaHub's session level: the louder channel, so a
// balance the user set in Windows does not read as a lower volume.
func winmmGetVolume() (int, bool) {
	var v uint32
	r, _, _ := procWaveOutGetVolume.Call(0, uintptr(unsafe.Pointer(&v)))
	if r != 0 {
		return 0, false
	}
	left, right := v&0xFFFF, v>>16
	if right > left {
		left = right
	}
	return winmmPercentFor(int(left)), true
}
