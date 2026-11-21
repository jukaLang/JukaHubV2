package main

// usbawake.go — the handheld stays awake while a computer is copying files.
//
// A long copy over USB file transfer (usbmtp.go) is use without input: the
// idle screen came up over it and Auto sleep suspended the handheld halfway
// through, which drops the MTP session and leaves a half-written file on the
// card. So while the MTP link has a computer on it (usbMtpNoteConnected sets
// usbMtp.connected, cleared when the cable or the service goes),
// idleScreenHoldOff says the device is in use, which keeps both the idle
// screen (screensaver.go) and Auto sleep (devpower.go autoSleepBlocker) away.
//
// USB storage mode (usbstorage.go) needs nothing here: JukaHub quits while
// the card is lent out, and its helper script runs alone until the computer
// ejects the drive.
//
// The check runs every frame on the event thread, so the MTP state is read
// under its lock at most once a second and kept in an atomic in between.

import (
	"sync/atomic"
	"time"
)

// usbAwakeRecheck is how long a read of the MTP state is trusted.
const usbAwakeRecheck = time.Second

var usbAwake struct {
	mtp       atomic.Bool  // the last read of usbMtp.connected
	checkedAt atomic.Int64 // when it was read (UnixNano)
}

// usbAwakeHolds reports a computer on the USB file transfer link, read at
// most once per usbAwakeRecheck. Any goroutine.
func usbAwakeHolds() bool {
	now := time.Now().UnixNano()
	if last := usbAwake.checkedAt.Load(); last != 0 && now >= last && now-last < int64(usbAwakeRecheck) {
		return usbAwake.mtp.Load()
	}
	usbMtp.mu.Lock()
	on := usbMtp.connected && usbMtp.running
	usbMtp.mu.Unlock()
	usbAwake.mtp.Store(on)
	usbAwake.checkedAt.Store(now)
	return on
}
