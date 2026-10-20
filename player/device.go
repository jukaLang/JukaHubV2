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
	DeviceTrimuiHammer    DeviceModel = "trimui-hammer"
	DeviceDevBuild        DeviceModel = "dev-build"
	DeviceUnknown         DeviceModel = "unknown"
)

// IsTrimui reports whether the model is a supported TrimUI handheld.
func (d DeviceModel) IsTrimui() bool {
	switch d {
	case DeviceTrimuiSmartPro, DeviceTrimuiSmartProS, DeviceTrimuiBrick, DeviceTrimuiBrickPro, DeviceTrimuiHammer:
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
	case DeviceTrimuiHammer:
		// The Hammer is a Brick variant on the same TG3040 hardware; there is
		// no separate Hammer model code in the firmware.
		return "TG3040"
	default:
		return ""
	}
}

// DetectDevice returns the current device, reading the MODEL env var,
// /etc/hostname, or the device-tree model on TrimUI hardware. Firmware images
// do not all put the marketing name in the hostname, so the device tree is a
// second source for the Brick/Hammer family. On Windows it returns
// DeviceDevBuild.
func DetectDevice() DeviceModel {
	model := strings.TrimSpace(os.Getenv("JUKAHUB_DEVICE_MODEL"))
	if model == "" {
		model = readDeviceModelFile("/etc/hostname")
	}
	// Prefer a string the classifier already understands. If the hostname is
	// generic ("trimui", "localhost"), append the device-tree model instead of
	// letting a model-less name decide the device.
	if detectDeviceForSystem(runtime.GOOS, model) == DeviceUnknown {
		if tree := readDeviceModelFile("/proc/device-tree/model"); tree != "" {
			if model == "" {
				model = tree
			} else if !strings.EqualFold(model, tree) {
				model += " " + tree
			}
		}
	}
	return detectDeviceForSystem(runtime.GOOS, model)
}

// readDeviceModelFile reads a model string and strips the NUL padding used by
// device-tree properties.
func readDeviceModelFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.Trim(string(data), "\x00"))
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
	// "Hammer" is an explicit marketing name for a Brick variant. A TG3040
	// alongside it is the same hardware, not a conflict; any other TG id is
	// contradictory and stays unknown.
	hammer := containsToken(upper, "HAMMER")
	if len(found) == 1 {
		if hammer {
			if found[0] != DeviceTrimuiBrick {
				return DeviceUnknown
			}
			return DeviceTrimuiHammer
		}
		return found[0]
	}
	if len(found) > 1 {
		return DeviceUnknown
	}
	// Firmware images do not all stamp the TG id into the hostname or device
	// tree. The marketing names are unambiguous enough to classify, and the
	// Brick family all shares the compact canvas while the Smart Pro family
	// shares the 16:9 one, so a missing model id cannot pick the wrong UI.
	if hammer {
		return DeviceTrimuiHammer
	}
	if containsToken(upper, "BRICK") {
		if containsToken(upper, "PRO") {
			return DeviceTrimuiBrickPro
		}
		return DeviceTrimuiBrick
	}
	if containsToken(upper, "SMART") && containsToken(upper, "PRO") {
		if containsToken(upper, "S") {
			return DeviceTrimuiSmartProS
		}
		return DeviceTrimuiSmartPro
	}
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
	case DeviceTrimuiHammer:
		return "TrimUI Hammer (TG3040)"
	case DeviceDevBuild:
		return "Development build"
	default:
		return "Unknown"
	}
}
