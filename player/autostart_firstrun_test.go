package main

import (
	"os"
	"path/filepath"
	"testing"
)

// autoStartFirstRunCard is a temporary SD card with JukaHub installed on it,
// and the first-start state reset around the test.
func autoStartFirstRunCard(t *testing.T) (hook string) {
	t.Helper()
	root := t.TempDir()
	useTrimuiAutostart(t, root, filepath.Join(root, "Apps", "JukaHub", "JukaHub"))
	prevNotice, prevSince, prevTour := autoStartNoticePending, autoStartNoticeSince, onboarding.active
	autoStartNoticePending, onboarding.active = false, false
	t.Cleanup(func() {
		autoStartNoticePending, autoStartNoticeSince, onboarding.active = prevNotice, prevSince, prevTour
	})
	return filepath.Join(root, "System", "starts", trimuiAutostartFile)
}

// A TrimUI where nobody has chosen yet boots into JukaHub from now on: the
// setting is switched on, the boot hook is written, and it is said once.
func TestAutoStartFirstStartOnTrimui(t *testing.T) {
	hook := autoStartFirstRunCard(t)
	cfg := &Config{}
	reconcileAutoStart(cfg)
	if !autoStartEnabled(cfg) {
		t.Fatal("the first start on a TrimUI left Start JukaHub at login off")
	}
	if _, err := os.Stat(hook); err != nil {
		t.Fatalf("the boot hook was not written: %v", err)
	}
	if !autoStartNoticePending {
		t.Fatal("the default was applied without telling the user")
	}
	if v, ok := customValue(cfg.Variables.Custom, autoStartDefaultedKey); !ok || !coerceBool(v) {
		t.Fatal("the first-start default was not recorded")
	}
}

// Turning it off is final: the default is applied once per device, never
// again — not on the next boot, not after replaying the tour.
func TestAutoStartFirstStartIsAppliedOnce(t *testing.T) {
	hook := autoStartFirstRunCard(t)
	cfg := &Config{}
	reconcileAutoStart(cfg)
	cfg.Variables.Custom["autoStart"] = "false" // the user switched it off
	autoStartNoticePending = false
	reconcileAutoStart(cfg)
	if autoStartEnabled(cfg) {
		t.Fatal("the default switched Start JukaHub at login back on")
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatal("the boot hook is still on the card after the user turned it off")
	}
	if autoStartNoticePending {
		t.Fatal("the notice was raised again")
	}
}

// A choice made before this build (or in the web builder) stands.
func TestAutoStartFirstStartRespectsAnEarlierChoice(t *testing.T) {
	hook := autoStartFirstRunCard(t)
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{"autoStart": "false"}
	reconcileAutoStart(cfg)
	if autoStartEnabled(cfg) {
		t.Fatal("an explicit off was overridden by the first-start default")
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatal("a boot hook was written against the user's choice")
	}
	if autoStartNoticePending {
		t.Fatal("a notice was raised for a default that was not applied")
	}
}

// During the first-run tour no toast is drawn over it. The tour's own step
// says it — and seeing that step drops the toast — but a tour skipped before
// the step still leaves the toast to say it.
func TestAutoStartFirstStartDuringTheTour(t *testing.T) {
	autoStartFirstRunCard(t)
	onboarding.active = true
	cfg := &Config{}
	reconcileAutoStart(cfg)
	if !autoStartEnabled(cfg) {
		t.Fatal("the default was not applied during the tour")
	}
	if !autoStartNoticePending {
		t.Fatal("no notice was kept for a tour that may be skipped before its step")
	}
	autoStartShowPendingNotice()
	if !autoStartNoticePending {
		t.Fatal("the notice was used up while the tour covered the screen")
	}
	if got := onboardingAutoStartStatus(cfg); got != "Start with the device: on" {
		t.Fatalf("tour status %q", got)
	}
	if autoStartNoticePending {
		t.Fatal("the tour's step said it, and the toast is still queued")
	}
}

// The tour's button flips the setting and the boot hook with it, and says so
// on the card.
func TestAutoStartTourButton(t *testing.T) {
	hook := autoStartFirstRunCard(t)
	cfg := &Config{}
	reconcileAutoStart(cfg)
	if msg := onboardingAutoStartToggle(cfg); msg != "JukaHub will not start with the device." {
		t.Fatalf("turning it off said %q", msg)
	}
	if autoStartEnabled(cfg) {
		t.Fatal("the button did not turn the setting off")
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatal("the boot hook survived the button")
	}
	if msg := onboardingAutoStartToggle(cfg); msg != "JukaHub will start with the device." {
		t.Fatalf("turning it back on said %q", msg)
	}
	if _, err := os.Stat(hook); err != nil {
		t.Fatalf("the boot hook was not written again: %v", err)
	}
}

// Desktops keep the old rule: off until the user turns it on.
func TestAutoStartFirstStartNotOnDesktop(t *testing.T) {
	prev := autostartIsTrimui
	autostartIsTrimui = func() bool { return false }
	t.Cleanup(func() { autostartIsTrimui = prev })
	cfg := &Config{}
	if applyAutoStartFirstRunDefault(cfg) {
		t.Fatal("the TrimUI default was applied on a desktop")
	}
	if _, ok := customValue(cfg.Variables.Custom, "autoStart"); ok {
		t.Fatal("a desktop got an autoStart value it never chose")
	}
}
