//go:build linux

package main

// quickresume_linux.go — the Linux facts quickresume.go reads: whether the
// RTC's wake alarm is still pending after a wake (it is cleared when it
// fires, so a pending one means something else woke the device), the boot id
// that tells a power-on from a restart, the suspend state the kernel offers
// and whether the RTC can wake the device at all.

import (
	"os"
	"strconv"
	"strings"
)

// quickBootIDPath is the kernel's random id for this boot.
const quickBootIDPath = "/proc/sys/kernel/random/boot_id"

// powerRTCAlarmPending reports whether the RTC's wake alarm is still armed,
// and whether that could be read. The kernel shows an armed alarm's time and
// nothing once it has fired or been cleared; a time the RTC has already
// passed counts as fired too (a driver may mark it done late, or never).
func powerRTCAlarmPending() (pending, known bool) {
	at, ok := powerRTCAlarmAt()
	if !ok {
		return false, false
	}
	if at == 0 {
		return false, true
	}
	if now, ok := powerRTCNow(); ok && at > 0 && at <= now {
		return false, true
	}
	return true, true
}

// powerRTCAlarmAt is the time the RTC's wake alarm is set for, in the RTC's
// seconds since the epoch: 0 when none is set, -1 when one is set but its
// time cannot be parsed. ok is false when it cannot be read.
func powerRTCAlarmAt() (int64, bool) {
	path := powerRTCWakealarmCached()
	if path == "" {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, true
	}
	at, err := strconv.ParseInt(text, 10, 64)
	if err != nil || at <= 0 {
		return -1, true
	}
	return at, true
}

// quickBootID is this boot's id, or "" when the kernel does not say.
func quickBootID() string {
	data, err := os.ReadFile(quickBootIDPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// quickReadSuspendState is the state suspendDevice picks (lowbattery.go),
// with "mem" read through /sys/power/mem_sleep: a kernel whose bracketed
// mem_sleep mode is s2idle freezes instead of suspending to RAM, so it is
// reported as "s2idle", the light sleep it really is.
func quickReadSuspendState() string {
	data, err := os.ReadFile("/sys/power/state")
	if err != nil {
		return ""
	}
	state := pickSuspendState(string(data))
	if state != "mem" {
		return state
	}
	if modes, err := os.ReadFile("/sys/power/mem_sleep"); err == nil && quickMemSleepMode(string(modes)) == "s2idle" {
		return "s2idle"
	}
	return state
}

// quickReadRTCWake reports whether an RTC has a wake alarm.
func quickReadRTCWake() bool { return powerRTCWakealarm() != "" }
