//go:build linux

package main

// rtcsync_linux.go — keeping network time across a power cycle (NET-12).
//
// Network time (ntpsync.go) only corrects the system clock, which lives in RAM:
// switch the handheld off and on and the kernel starts from whatever the
// hardware clock (RTC) holds. The A133 (Smart Pro) and the Brick's SoC have an
// RTC kept alive by the PMIC while the battery has charge, and the kernel loads
// it into the system clock at boot — but nothing on the stock firmware ever
// writes a corrected time back into it, so every cold boot started at the
// stale time again until Wi-Fi came up.
//
// So after a sync that set the clock, the RTC is written too: through the
// RTC_SET_TIME ioctl on /dev/rtc0 (golang.org/x/sys/unix, no shell, no
// hwclock), always in UTC, which is what the Linux RTC convention and the
// kernel's boot-time load expect. A sync that found the clock already right
// writes it once per run as well when the RTC itself is off by more than two
// seconds (a firmware that sets the system clock some other way, an RTC that
// drifted). Only where JukaHub sets the clock (clockSetSupported: the handheld,
// as root), only when the node exists; with no RTC nothing changes.
//
// rtcStartupReport logs what the RTC held at start, so a bug report says
// whether a wrong clock at boot is a dead RTC, a kernel that never loads it, or
// simply a first boot.

import (
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// rtcDevices are the RTC nodes tried, in order.
var rtcDevices = []string{"/dev/rtc0", "/dev/rtc"}

// rtcState is this run's bookkeeping: whether the RTC was written (or found
// right) already, and how many writes failed (the first one warns).
var rtcState struct {
	done     atomic.Bool
	failures atomic.Int32
}

// rtcDevicePath is the first RTC node present, "" when there is none.
func rtcDevicePath() string {
	for _, p := range rtcDevices {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// rtcFromTime is t in the kernel's struct rtc_time, in UTC: months from 0,
// years from 1900.
func rtcFromTime(t time.Time) unix.RTCTime {
	u := t.UTC()
	return unix.RTCTime{
		Sec:  int32(u.Second()),
		Min:  int32(u.Minute()),
		Hour: int32(u.Hour()),
		Mday: int32(u.Day()),
		Mon:  int32(u.Month()) - 1,
		Year: int32(u.Year() - 1900),
		Wday: int32(u.Weekday()),
		Yday: int32(u.YearDay() - 1),
	}
}

// rtcToTime reads a struct rtc_time as a UTC instant.
func rtcToTime(r *unix.RTCTime) time.Time {
	return time.Date(int(r.Year)+1900, time.Month(r.Mon+1), int(r.Mday),
		int(r.Hour), int(r.Min), int(r.Sec), 0, time.UTC)
}

// rtcRead returns the time the RTC holds.
func rtcRead(path string) (time.Time, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return time.Time{}, err
	}
	defer unix.Close(fd)
	r, err := unix.IoctlGetRTCTime(fd)
	if err != nil {
		return time.Time{}, err
	}
	return rtcToTime(r), nil
}

// rtcWrite sets the RTC from the system clock. The RTC counts whole seconds
// and starts the next one when it is written, so the write waits for the
// system clock's next whole second the way hwclock -w does: the two then agree
// to a few milliseconds instead of up to a second. It runs on the sync's own
// goroutine, never on the render thread.
func rtcWrite(path string) (time.Time, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return time.Time{}, err
	}
	defer unix.Close(fd)
	now := time.Now()
	time.Sleep(now.Truncate(time.Second).Add(time.Second).Sub(now))
	at := time.Now().Round(time.Second)
	v := rtcFromTime(at)
	// A sleep timed on the RTC must not count this jump (powersleep.go):
	// bumped on both sides of the write.
	rtcSetGen.Add(1)
	defer rtcSetGen.Add(1)
	if err := unix.IoctlSetRTCTime(fd, &v); err != nil {
		return time.Time{}, err
	}
	return at, nil
}

// rtcSyncAfterNetTime is netTimeSync's hook once a sync came to a verdict:
// a clock it set is written to the RTC; a clock it found right is written once
// per run, and only when the RTC is off. Any other verdict (the system owns the
// clock here) leaves the RTC alone.
func rtcSyncAfterNetTime(action clockAction) {
	switch action {
	case clockSet:
	case clockKeep:
		if rtcState.done.Load() {
			return
		}
	default:
		return
	}
	if !clockSetSupported() {
		return
	}
	path := rtcDevicePath()
	if path == "" {
		return
	}
	if action == clockKeep {
		if held, err := rtcRead(path); err == nil {
			// held has no monotonic reading: this compares wall clocks.
			if off := time.Since(held); off < clockSetThreshold && off > -clockSetThreshold {
				rtcState.done.Store(true)
				return
			}
		}
	}
	at, err := rtcWrite(path)
	if err != nil {
		if rtcState.failures.Add(1) == 1 {
			logWarn("clock", "could not write the hardware clock %s: %v", path, err)
		} else {
			logDebug("clock", "could not write the hardware clock %s: %v", path, err)
		}
		return
	}
	rtcState.done.Store(true)
	logInfo("clock", "hardware clock %s set to %s UTC: the time survives a power cycle", path, at.UTC().Format(time.RFC3339))
}

// rtcStartupReport logs what the RTC holds at start. Nothing is changed: when
// the kernel loads the RTC at boot the system clock already shows it, and a
// clock before this build is network time's to fix.
func rtcStartupReport() {
	if !IsTrimuiDevice() {
		return
	}
	path := rtcDevicePath()
	if path == "" {
		logInfo("clock", "no hardware clock (/dev/rtc0): the time is only right once network time sets it")
		return
	}
	held, err := rtcRead(path)
	if err != nil {
		logInfo("clock", "hardware clock %s could not be read: %v", path, err)
		return
	}
	now, floor := time.Now(), clockFloor()
	stamp := held.Format(time.RFC3339)
	switch {
	case held.Before(floor):
		logInfo("clock", "hardware clock %s holds %s, before this build: it lost power; network time will set it", path, stamp)
	case now.Before(floor):
		logInfo("clock", "system clock is before this build but the hardware clock %s holds %s: the kernel did not load it at boot", path, stamp)
	default:
		logInfo("clock", "hardware clock %s holds %s (system clock %s apart)", path, stamp, now.Sub(held).Round(time.Second))
	}
}
