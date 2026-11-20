//go:build !linux

package main

// tvmode_other.go — no sysfs to watch: a PC's displays are the desktop's
// business. TV mode's On still shows the TV interface here (tvmode.go).

// tvProbeSupported says this system has a sysfs to watch.
const tvProbeSupported = false

// tvProbeSystem has nothing to read here.
func tvProbeSystem(bool) tvEvidence { return tvEvidence{} }

// tvSysfsClassNames has nothing to list here.
func tvSysfsClassNames() string { return "" }
