package main

// autostart_firstrun.go — on a TrimUI, JukaHub starts with the device.
//
// A handheld that boots into the stock menu and makes you find JukaHub under
// Apps every time is not using JukaHub as its launcher. So on a TrimUI "Start
// JukaHub at login" is switched on the first time JukaHub starts with nobody
// having chosen yet — a fresh install, or an update on a device where the
// switch was never touched — and that is said once: by the first-run tour's
// "Start with your device" step, or by a toast on the first frame otherwise.
//
// The default is recorded (autoStartDefaultedKey) the moment it is considered,
// so it is applied at most once per device: turning the switch off is final,
// and a choice made before — on the device or in the web builder — is never
// overridden. Desktops keep the old rule: off until the user turns it on.

import "time"

const autoStartDefaultedKey = "autoStartDefaulted"

// autoStartNoticePending is the toast for a default applied at boot. The loop
// is not drawing yet when reconcileAutoStart runs, and a toast raised then
// expires before the first frame, so a widgets tick shows it a moment after the
// screen is up (autoStartNoticeDelay), when the boot's own toasts are done.
// Seeing the tour's step clears it. All of it runs on the main goroutine.
var (
	autoStartNoticePending bool
	autoStartNoticeSince   time.Time
)

// autoStartNoticeDelay is how long the screen has been up before the toast.
const autoStartNoticeDelay = 3 * time.Second

// applyAutoStartFirstRunDefault turns the setting on for a TrimUI's first
// start and reports whether it did. Every call after the first is a no-op.
func applyAutoStartFirstRunDefault(config *Config) bool {
	if config == nil || !autostartIsTrimui() || !autostartSupported() {
		return false
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	if v, ok := customValue(config.Variables.Custom, autoStartDefaultedKey); ok && coerceBool(v) {
		return false
	}
	config.Variables.Custom[autoStartDefaultedKey] = true
	_, chosen := customValue(config.Variables.Custom, "autoStart")
	if !chosen {
		// The toggle's own encoding (setStoreBool).
		config.Variables.Custom["autoStart"] = boolString(true)
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
	return !chosen
}

// autoStartShowPendingNotice shows the first-start toast on a frame the user
// can see: not under a running game, not over the tour.
func autoStartShowPendingNotice() {
	if !autoStartNoticePending || onboarding.active || gameScreenSuspended() {
		autoStartNoticeSince = time.Time{}
		return
	}
	if autoStartNoticeSince.IsZero() {
		autoStartNoticeSince = time.Now()
	}
	if time.Since(autoStartNoticeSince) < autoStartNoticeDelay {
		return
	}
	autoStartNoticePending = false
	if !autoStartInstalled() {
		// reconcileAutoStart logged why the hook could not be written.
		return
	}
	ShowToast("JukaHub now starts with your TrimUI · Settings → General turns it off", ToastKindInfo)
}

// onboardingAutoStartToggle is the tour step's button: it flips the setting
// the way the Settings switch does, and says on the card (not in a toast over
// it) what came of it.
func onboardingAutoStartToggle(config *Config) string {
	if config == nil {
		return ""
	}
	if !autostartSupported() {
		return "This device manages its startup apps itself."
	}
	on := !autoStartEnabled(config)
	setStoreBool(config, "autoStart", on)
	if err := setAutoStart(on); err != nil {
		return "Could not change it: " + err.Error()
	}
	if on {
		return "JukaHub will start with the device."
	}
	return "JukaHub will not start with the device."
}

// onboardingAutoStartStatus is the tour step's status line. The step on
// screen is the announcement of a first-start default, so the toast that would
// otherwise say it is dropped.
func onboardingAutoStartStatus(config *Config) string {
	autoStartNoticePending = false
	switch {
	case !autostartSupported():
		return "Start with the device: not available here"
	case autoStartEnabled(config):
		return "Start with the device: on"
	default:
		return "Start with the device: off"
	}
}
