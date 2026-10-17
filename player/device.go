package main

// device.go identifies the runtime hardware. DeviceModel is a string so the
// release tooling can build asset names like
// "JukaHub-trimui-smart-pro-linux-arm64.tar.gz" directly from it.

import (
	"os"
	"runtime"
	"strings"
)

type DeviceModel string

const (
	DeviceTrimuiSmartPro  DeviceModel = "trimui-smart-pro"
	DeviceTrimuiSmartProS DeviceModel = "trimui-smart-pro-s"
	DeviceTrimuiBrick     DeviceModel = "trimui-brick"
	DeviceTrimuiBrickPro  DeviceModel = "trimui-brick-pro"
	DeviceDevBuild        DeviceModel = "dev-build"
	DeviceUnknown         DeviceModel = "unknown"
)

// IsTrimui reports whether the model is a supported TrimUI handheld.
func (d DeviceModel) IsTrimui() bool {
	switch d {
	case DeviceTrimuiSmartPro, DeviceTrimuiSmartProS, DeviceTrimuiBrick, DeviceTrimuiBrickPro:
		return true
	default:
		return false
	}
}

// hardwareModelID returns the TG/TR model identifier (e.g. "TG5040").
func (d DeviceModel) hardwareModelID() string {
	switch d {
	case DeviceTrimuiSmartPro:
		return "TG5040"
	case DeviceTrimuiSmartProS:
		return "TG5050"
	case DeviceTrimuiBrick:
		return "TG3040"
	case DeviceTrimuiBrickPro:
		return "TG4040"
	default:
		return ""
	}
}

// DetectDevice returns the current device, reading /etc/hostname or the
// MODEL env var on TrimUI hardware. On Windows it returns DeviceDevBuild.
func DetectDevice() DeviceModel {
	model := ""
	if v := os.Getenv("JUKAHUB_DEVICE_MODEL"); v != "" {
		model = v
	} else if data, err := os.ReadFile("/etc/hostname"); err == nil {
		model = strings.TrimSpace(string(data))
	}
	return detectDeviceForSystem(runtime.GOOS, model)
}

// detectDeviceForSystem is the testable core of DetectDevice.
func detectDeviceForSystem(goos, model string) DeviceModel {
	if goos == "windows" {
		return DeviceDevBuild
	}
	if goos != "linux" {
		return DeviceUnknown
	}
	upper := strings.ToUpper(model)
	if !strings.Contains(upper, "TRIMUI") {
		return DeviceUnknown
	}
	// A model-specific match requires BOTH the marketing name and a TG id.
	// The exact-token check is enforced by DeviceModelForHardwareID.
	ids := map[DeviceModel]string{
		DeviceTrimuiSmartPro:  "TG5040",
		DeviceTrimuiSmartProS: "TG5050",
		DeviceTrimuiBrick:     "TG3040",
		DeviceTrimuiBrickPro:  "TG4040",
	}
	// If two conflicting TG ids appear in the model string, refuse.
	var found []DeviceModel
	for device, id := range ids {
		if containsToken(upper, id) {
			found = append(found, device)
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	if len(found) > 1 {
		return DeviceUnknown
	}
	// Marketing-only match (no TG id) is deliberately DeviceUnknown: a
	// "TrimUI Smart Pro S" with no TG5050 is not safely distinguishable
	// from the original model.
	return DeviceUnknown
}

// DeviceModelForHardwareID maps a TG/TR id to a DeviceModel.
func DeviceModelForHardwareID(id string) DeviceModel {
	switch strings.TrimSpace(strings.ToUpper(id)) {
	case "TG5040":
		return DeviceTrimuiSmartPro
	case "TG5050":
		return DeviceTrimuiSmartProS
	case "TG3040":
		return DeviceTrimuiBrick
	case "TG4040":
		return DeviceTrimuiBrickPro
	default:
		return DeviceUnknown
	}
}

// containsToken reports whether token appears in s as a whole word.
func containsToken(s, token string) bool {
	i := strings.Index(s, token)
	for i >= 0 {
		before := i == 0 || !isWordByte(s[i-1])
		after := i+len(token) == len(s) || !isWordByte(s[i+len(token)])
		if before && after {
			return true
		}
		next := strings.Index(s[i+1:], token)
		if next < 0 {
			return false
		}
		i += 1 + next
	}
	return false
}

func isWordByte(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// DeviceTrimuiTSP is kept for source compatibility with the original model.
const DeviceTrimuiTSP DeviceModel = DeviceTrimuiSmartPro

// deviceDisplayName returns a human label for the detected model.
func deviceDisplayName(d DeviceModel) string {
	switch d {
	case DeviceTrimuiSmartPro:
		return "TrimUI Smart Pro (TG5040)"
	case DeviceTrimuiSmartProS:
		return "TrimUI Smart Pro S (TG5050)"
	case DeviceTrimuiBrick:
		return "TrimUI Brick (TG3040)"
	case DeviceTrimuiBrickPro:
		return "TrimUI Brick Pro (TG4040)"
	case DeviceDevBuild:
		return "Development build"
	default:
		return "Unknown"
	}
}