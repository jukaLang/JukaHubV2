//go:build !windows

package main

import "errors"

// brightnessProbeBlocks: off Windows the probe is a few file reads and one
// ioctl, so it runs on the first call (brightnessStartProbe).
const brightnessProbeBlocks = false

// brightnessPlatformProbe: outside Windows the backend is found by
// brightnessFindBackend's own chain (the display driver, sysfs, the software
// dimmer); there is no platform-only control.
func brightnessPlatformProbe() (string, int, bool) { return "", 0, false }

// brightnessNoteWindow: only DDC/CI on Windows needs the window's handle.
func brightnessNoteWindow() {}

// errBrightnessWindowsOnly: WMI and DDC/CI exist so setScreenBrightnessVia's
// switch builds everywhere; the probe never picks them off Windows.
var errBrightnessWindowsOnly = errors.New("WMI and DDC/CI brightness are Windows-only")

func wmiBrightness() (int, bool) { return 0, false }

func wmiSetBrightness(int) error { return errBrightnessWindowsOnly }

func ddcBrightness() (int, bool) { return 0, false }

func ddcSetBrightness(int) error { return errBrightnessWindowsOnly }
