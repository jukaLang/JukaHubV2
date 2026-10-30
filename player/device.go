package main

// device.go identifies the runtime hardware. DeviceModel is a string so the
// release tooling can build asset names like
// "JukaHub-trimui-smart-pro-linux-arm64.tar.gz" directly from it.

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// deviceModelCodes maps each supported model to the code TrimUI stamps into
// its firmware and device tree.
var deviceModelCodes = map[DeviceModel]string{
	DeviceTrimuiSmartPro:  "TG5040",
	DeviceTrimuiSmartProS: "TG5050",
	DeviceTrimuiBrick:     "TG3040",
	DeviceTrimuiBrickPro:  "TG4040",
}

// hardwareModelID returns the TG/TR model identifier (e.g. "TG5040").
func (d DeviceModel) hardwareModelID() string {
	if d == DeviceTrimuiHammer {
		// The Hammer is a Brick variant on the same TG3040 hardware; there is
		// no separate Hammer model code in the firmware.
		return deviceModelCodes[DeviceTrimuiBrick]
	}
	return deviceModelCodes[d]
}

// DetectDevice returns the current device. On Windows it returns
// DeviceDevBuild. On Linux the evidence collected by readDeviceIdentity names
// the board: a launcher's exported device, a model code in the kernel's board
// description (or cpuinfo, or the hostname), then the model names the image
// ships, and finally the panel when the image is a TrimUI one but names no
// model.
func DetectDevice() DeviceModel {
	deviceModelOnce.Do(func() {
		deviceModelCache, deviceSourceCache, deviceEvidenceCache = detectDeviceIdentity()
	})
	return deviceModelCache
}

// deviceIdentityReport is the evidence behind DetectDevice, for diagnostics:
// which model was named, by which source, and what the platform exposed. It
// starts detection if it has not run yet.
func deviceIdentityReport() (DeviceModel, string, deviceIdentity) {
	DetectDevice()
	return deviceModelCache, deviceSourceCache, deviceEvidenceCache
}

var (
	deviceModelOnce     sync.Once
	deviceModelCache    DeviceModel
	deviceSourceCache   string
	deviceEvidenceCache deviceIdentity
)

// detectDevice is the uncached detection. It runs once per process: the board
// cannot change while the app is running, and DetectDevice sits on per-frame
// paths (the frame-rate controller and the texture budgets read it every
// frame), so it must not re-read sysfs there.
//
// Order: an explicit JUKAHUB_DEVICE_MODEL override, then the evidence collected
// by readDeviceIdentity and classified by classifyDevice (launcher env, kernel
// model codes, the firmware's own model strings, the panel). The override goes
// through both classifiers so a test can say "TG3040" as well as
// "trimui-brick".
func detectDevice() DeviceModel {
	device, _, _ := detectDeviceIdentity()
	return device
}

// detectDeviceIdentity is detectDevice plus the source and evidence it used.
func detectDeviceIdentity() (DeviceModel, string, deviceIdentity) {
	if override := strings.TrimSpace(os.Getenv("JUKAHUB_DEVICE_MODEL")); override != "" {
		if d := detectDeviceForSystem(runtime.GOOS, override); d != DeviceUnknown {
			return d, "override", deviceIdentity{EnvModel: override}
		}
		if d := detectDeviceForHardware(runtime.GOOS, override, ""); d != DeviceUnknown {
			return d, "override", deviceIdentity{EnvModel: override}
		}
	}
	if runtime.GOOS != "linux" {
		return detectDeviceForSystem(runtime.GOOS, ""), "", deviceIdentity{}
	}
	id := readDeviceIdentity()
	device, source := classifyDevice(id)
	logDeviceIdentity(device, source, id)
	return device, source, id
}

// logDeviceIdentity records how the board was named, with the raw strings it was
// named from. On a handheld the log is the only way to see what the firmware
// actually exposes, so a device that stays unidentified prints the same line and
// says which sources were missing.
func logDeviceIdentity(device DeviceModel, source string, id deviceIdentity) {
	name := string(device)
	if device == DeviceUnknown {
		name = "unidentified"
	} else if source != "" {
		name += " (from " + source + ")"
	}
	panel := "unknown"
	if id.PanelW > 0 && id.PanelH > 0 {
		panel = panelLabel(id.PanelW, id.PanelH)
	}
	firmware := strings.Join(id.Names, " | ")
	if len(firmware) > 220 {
		firmware = firmware[:220] + "…"
	}
	log.Printf("[device] %s: dt model=%q compatible=%q cpuinfo=%q hostname=%q firmware=%q trimui=%v panel=%s env(device=%q platform=%q model=%q)",
		name, id.Model, id.Compatible, id.CPUInfo, id.Hostname, firmware, id.TrimUI, panel,
		id.EnvDevice, id.EnvPlatform, id.EnvModel)
}

// deviceTreeDirs are the two ways the kernel exposes its board description:
// procfs and the sysfs mirror of the same tree.
var deviceTreeDirs = []string{"/proc/device-tree", "/sys/firmware/devicetree/base"}

// deviceTreeIdentity returns the board's model and compatible properties. Both
// are written by the kernel for the hardware it booted on, and TrimUI images
// carry the TG model code in them even when the model string is only a
// marketing name.
func deviceTreeIdentity() (model, compatible string) {
	for _, dir := range deviceTreeDirs {
		if model == "" {
			model = readDeviceTreeString(filepath.Join(dir, "model"))
		}
		if compatible == "" {
			compatible = readDeviceTreeString(filepath.Join(dir, "compatible"))
		}
		if model != "" && compatible != "" {
			break
		}
	}
	return model, compatible
}

// readDeviceTreeString reads one device-tree property. Properties are
// NUL-terminated and "compatible" packs several strings into one property, so
// the NULs become separators rather than trailing padding to trim.
func readDeviceTreeString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
}

// detectDeviceForHardware classifies the kernel's board description. Unlike a
// hostname, the device tree is written by the kernel for the board it booted
// on, so a bare model code there is enough to name the device: a TrimUI image
// whose hostname and model string never say "TrimUI" still lists "tg3040" in
// its compatible strings. A marketing name in the same string is still
// accepted, and a Hammer on the shared TG3040 board stays a Hammer.
func detectDeviceForHardware(goos, model, compatible string) DeviceModel {
	if goos != "linux" {
		return DeviceUnknown
	}
	joined := strings.ToUpper(model + " " + compatible)
	found := deviceCodesIn(joined)
	if len(found) != 1 {
		// No code names the board (fall back to the names), or two conflict.
		return DeviceUnknown
	}
	if containsToken(joined, "HAMMER") {
		if found[0] != DeviceTrimuiBrick {
			return DeviceUnknown
		}
		return DeviceTrimuiHammer
	}
	return found[0]
}

// deviceCodesIn returns the distinct model codes named in an upper-cased
// string. Two different codes mean the string contradicts itself.
func deviceCodesIn(s string) []DeviceModel {
	var found []DeviceModel
	for device, id := range deviceModelCodes {
		if containsToken(s, id) {
			found = append(found, device)
		}
	}
	return found
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
	// If two conflicting TG ids appear in the model string, refuse.
	found := deviceCodesIn(upper)
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
