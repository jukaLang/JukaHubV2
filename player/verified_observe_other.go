//go:build !linux

package main

// verified_observe_other.go — the no-op half of verified_observe_linux.go:
// off Linux there is no /proc/asound to read which process plays sound, so
// the sound check is never recorded automatically here (the player's own
// result on the Device check sheet still is).

// vfoSoundSupported: no automatic sound check here.
func vfoSoundSupported() bool { return false }

// vfoSoundSeen cannot tell.
func vfoSoundSeen(int, bool) bool { return false }
