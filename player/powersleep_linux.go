//go:build linux

package main

// powersleep_linux.go — the Linux half of sleepNow (powersleep.go): the RTC
// wake alarm, the clock that counts time asleep, and the /proc check that
// tells a game JukaHub may pause from one that is already paused by something
// else. (The backlight is devpower.go's backlightOff / backlightOn.)
//
// Nothing here was run on a device from this desk: the RTC's relative
// "+seconds" wakealarm form comes from the kernel's documentation.

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// powerRTCWakealarm is the first RTC's wake alarm. Written as "+N" it
	// wakes the device N seconds from now in the RTC's own time, so an RTC
	// kept in local time instead of UTC does not shift it.
	powerRTCWakealarm = "/sys/class/rtc/rtc0/wakealarm"
	// powerProcScanMax bounds the /proc walk; a handheld never runs this many
	// processes.
	powerProcScanMax = 1024
)

// powerSleepSupported reports whether JukaHub can sleep this device at all: a
// kernel suspend, or the screen-off sleep that the power key ends.
func powerSleepSupported() bool {
	if !devPowerSupported() {
		return false // a Linux PC sleeps through its desktop, not a launcher
	}
	if _, err := os.Stat("/sys/power/state"); err == nil {
		return true
	}
	return powerKeyWatcherLive()
}

// powerSleepAsleepClock is the time this boot has spent suspended:
// CLOCK_BOOTTIME minus CLOCK_MONOTONIC (wake_resume_linux.go reads both).
func powerSleepAsleepClock() (time.Duration, bool) {
	s, ok := wakeClocks()
	if !ok {
		return 0, false
	}
	return s.withSleep - s.mono, true
}

// powerRTCWakeIn arms the RTC to wake the device after d and returns what
// clears it. An RTC without a wake alarm (or no permission) is logged once
// per sleep and nothing is armed: the alarm then rings when the device is
// next woken, inside its catch-up window if that is soon enough.
func powerRTCWakeIn(d time.Duration) func() {
	secs := int64(d / time.Second)
	if secs < 1 {
		secs = 1
	}
	// A pending alarm has to be cleared before a new one is accepted.
	_ = os.WriteFile(powerRTCWakealarm, []byte("0"), 0o644)
	if err := os.WriteFile(powerRTCWakealarm, []byte("+"+strconv.FormatInt(secs, 10)), 0o644); err != nil {
		logWarn("sleep", "could not set the wake-up alarm for the next alarm: %v", err)
		return func() {}
	}
	logInfo("sleep", "the device wakes itself in %s for an alarm", (time.Duration(secs) * time.Second).String())
	return func() { _ = os.WriteFile(powerRTCWakealarm, []byte("0"), 0o644) }
}

// powerChildGroupStopped reports whether pid, or any process in the group pid
// leads, is already stopped (state T or t): something else paused the game,
// and the sleep must neither pause it nor, on the way back, resume it.
func powerChildGroupStopped(pid int) bool {
	if pid <= 0 {
		return false
	}
	if state, _, ok := powerProcStat(strconv.Itoa(pid)); ok && powerStateStopped(state) {
		return true
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		scanned++
		if scanned > powerProcScanMax {
			break
		}
		state, pgrp, ok := powerProcStat(name)
		if ok && pgrp == pid && powerStateStopped(state) {
			return true
		}
	}
	return false
}

// powerStateStopped is the stopped (T) and traced-stop (t) states.
func powerStateStopped(state byte) bool { return state == 'T' || state == 't' }

// powerProcStat reads a process's state and process group from
// /proc/<pid>/stat. The command name is in parentheses and may itself contain
// spaces or parentheses, so the fields are taken after the last ')'.
func powerProcStat(pid string) (state byte, pgrp int, ok bool) {
	data, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return 0, 0, false
	}
	return parseProcStatState(string(data))
}

// parseProcStatState is powerProcStat's parser: "pid (comm) S ppid pgrp ...".
func parseProcStatState(stat string) (state byte, pgrp int, ok bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 3 || len(fields[0]) != 1 {
		return 0, 0, false
	}
	g, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, false
	}
	return fields[0][0], g, true
}
