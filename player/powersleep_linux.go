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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// powerRTCClass holds the RTCs. A wake alarm written as "+N" wakes the
	// device N seconds from now in the RTC's own time, so an RTC kept in
	// local time instead of UTC does not shift it.
	powerRTCClass = "/sys/class/rtc"
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
	wakealarm := powerRTCWakealarm()
	if wakealarm == "" {
		logWarn("sleep", "no RTC with a wake alarm: the next alarm rings when the device is woken")
		return func() {}
	}
	// A pending alarm has to be cleared before a new one is accepted.
	_ = os.WriteFile(wakealarm, []byte("0"), 0o644)
	if err := os.WriteFile(wakealarm, []byte("+"+strconv.FormatInt(secs, 10)), 0o644); err != nil {
		logWarn("sleep", "could not set the RTC wake-up alarm: %v", err)
		return func() {}
	}
	logInfo("sleep", "the device wakes itself in %s (an alarm, or the power off after sleeping)", (time.Duration(secs) * time.Second).String())
	return func() { _ = os.WriteFile(wakealarm, []byte("0"), 0o644) }
}

// powerRTCWakeSet arms the wake alarm secs from now in the RTC's own time,
// without a log line (nextui_sleep.go arms it as a NextUI game goes to
// sleep). It reports whether the alarm was written.
func powerRTCWakeSet(secs int64) bool {
	wakealarm := powerRTCWakealarmCached()
	if wakealarm == "" || secs < 1 {
		return false
	}
	// A pending alarm has to be cleared before a new one is accepted.
	_ = os.WriteFile(wakealarm, []byte("0"), 0o644)
	return os.WriteFile(wakealarm, []byte("+"+strconv.FormatInt(secs, 10)), 0o644) == nil
}

// powerRTCWakeClear clears the wake alarm, quietly.
func powerRTCWakeClear() {
	if wakealarm := powerRTCWakealarmCached(); wakealarm != "" {
		_ = os.WriteFile(wakealarm, []byte("0"), 0o644)
	}
}

// powerRTCWakealarmPath caches powerRTCWakealarm once it found one: the RTCs
// do not change while JukaHub runs, and nextui_sleep.go reads the clock four
// times a second. Not found is looked for again next time.
var powerRTCWakealarmPath atomic.Pointer[string]

// powerRTCWakealarmCached is powerRTCWakealarm through that cache.
func powerRTCWakealarmCached() string {
	if p := powerRTCWakealarmPath.Load(); p != nil {
		return *p
	}
	p := powerRTCWakealarm()
	if p != "" {
		powerRTCWakealarmPath.Store(&p)
	}
	return p
}

// powerRTCNow is the time the waking RTC (the one powerRTCWakealarm arms)
// holds, in whole seconds since the epoch — the clock its alarm runs on,
// which keeps counting through a suspend whatever the kernel's clocks do.
// ok is false where it cannot be read.
func powerRTCNow() (int64, bool) {
	wakealarm := powerRTCWakealarmCached()
	if wakealarm == "" {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(wakealarm), "since_epoch"))
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// powerRTCWakealarm is the wake alarm of the first RTC that has one: rtc0 on
// the A133P handhelds; on a board where rtc0 is a PMIC clock without an alarm
// (the Smart Pro S's 5.15 BSP may register the SoC's RTC second) the next
// one. "" when no RTC can wake the device.
func powerRTCWakealarm() string {
	names, err := sysfsClassChildren(powerRTCClass)
	if err != nil {
		return ""
	}
	sort.SliceStable(names, func(i, j int) bool { return powerRTCIndex(names[i]) < powerRTCIndex(names[j]) })
	for _, name := range names {
		path := filepath.Join(powerRTCClass, name, "wakealarm")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// powerRTCIndex is N in "rtcN" (rtc10 after rtc2), large for other names.
func powerRTCIndex(name string) int {
	if n, err := strconv.Atoi(strings.TrimPrefix(name, "rtc")); err == nil && n >= 0 {
		return n
	}
	return 1 << 30
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
