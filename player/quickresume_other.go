//go:build !linux

package main

// quickresume_other.go — the no-op halves of quickresume_linux.go: off Linux
// JukaHub never sleeps the machine (powersleep_other.go), so there is no RTC
// alarm, no boot id and no suspend state to read.

// powerRTCAlarmPending cannot tell.
func powerRTCAlarmPending() (pending, known bool) { return false, false }

// powerRTCAlarmAt cannot tell.
func powerRTCAlarmAt() (int64, bool) { return 0, false }

// quickBootID has no boot id here.
func quickBootID() string { return "" }

// quickReadSuspendState has no kernel suspend here.
func quickReadSuspendState() string { return "" }

// quickReadRTCWake is false: nothing here wakes the machine.
func quickReadRTCWake() bool { return false }
