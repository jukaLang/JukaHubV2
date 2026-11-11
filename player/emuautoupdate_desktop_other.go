//go:build !windows

package main

// emuautoupdate_desktop_other.go — the PC pass's questions off Windows. There
// are no JukaHub-managed Windows emulator folders here to ask about, and the
// battery is the kernel's power-supply class.

// emuProcessUnder says no: off Windows no managed emulator folder exists for
// the pass to ask about.
func emuProcessUnder(string) bool { return false }

// emuDesktopOnBattery reports a laptop whose battery says it is discharging.
// The TrimUI handhelds never ask: their pass has its own charger rule.
func emuDesktopOnBattery() bool {
	dir := batterySysfsDirIn(powerSupplyRoot)
	return dir != "" && batteryStatusIn(dir) == "discharging"
}
