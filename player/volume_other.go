//go:build !windows

package main

import "errors"

// volumePlatformBackend: outside Windows the mixer is found by volumeBackend
// (the TrimUI codec, PulseAudio or ALSA); there is no platform-only path.
func volumePlatformBackend() string { return "" }

// winmmSetVolume and winmmGetVolume exist so setSystemVolume's switch builds
// everywhere; volumeBackend never returns "winmm" off Windows.
func winmmSetVolume(int) error { return errors.New("winmm volume is Windows-only") }

func winmmGetVolume() (int, bool) { return 0, false }
