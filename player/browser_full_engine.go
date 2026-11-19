package main

// The SDL reader remains useful offline and on devices without WebKit. For
// ordinary Browser entry points, a full engine is the default when one is
// available: WPE WebKit on a supported handheld, or the OS browser on a PC.

import "strings"

var brwOpenDesktopPage = openExternalURL

const (
	brwRenderModeKey    = "browserRenderMode"
	brwRenderModeFull   = "full"
	brwRenderModeReader = "reader"
)

func brwRenderMode(c *Config) string {
	if strings.EqualFold(brwSetting(c, brwRenderModeKey), brwRenderModeReader) {
		return brwRenderModeReader
	}
	return brwRenderModeFull
}

func brwRenderModeOptions(*Config) []setOption {
	return []setOption{
		{Value: brwRenderModeFull, Label: "Full web browser"},
		{Value: brwRenderModeReader, Label: "Light reader"},
	}
}

func brwRenderModeCurrent(c *Config) int {
	if brwRenderMode(c) == brwRenderModeReader {
		return 1
	}
	return 0
}

func brwRenderModeDetail(*Config) string {
	if brwWpeHandheldFn() {
		if brwWpeStatusNow().Installed {
			return "WPE WebKit opens full pages · the light reader is available here"
		}
		return "WPE WebKit opens full pages once installed · the light reader works now"
	}
	return "Your computer's web browser opens full pages · the light reader stays in JukaHub"
}

func brwChooseRenderMode(c *Config, value string) {
	if value != brwRenderModeFull && value != brwRenderModeReader {
		return
	}
	brwStore(c, brwRenderModeKey, value)
	if value == brwRenderModeReader {
		setToast("Browser opens in the light reader")
		return
	}
	if brwWpeHandheldFn() && !brwWpeStatusNow().Installed {
		setToast("Full pages need WPE Browser · get it from the Browser menu")
		return
	}
	setToast("Browser opens full web pages")
}

// brwTryOpenFullEngine returns false when the full engine is unavailable or
// could not start. The caller then opens the same address in the SDL reader.
func brwTryOpenFullEngine(c *Config, address string) bool {
	if brwRenderMode(c) != brwRenderModeFull {
		return false
	}
	if brwWpeHandheldFn() {
		if !brwWpeProbe().Installed {
			return false
		}
		if brwWpeTarget(address) == "" {
			return false
		}
		return brwWpeLaunch(c, address)
	}
	if err := brwOpenDesktopPage(address); err != nil {
		logWarn("browser", "full browser did not open: %v", err)
		return false
	}
	return true
}
