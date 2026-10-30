package main

// device_probe.go — every place a TrimUI image may name its own hardware.
//
// Identification used to rest on two facts: a model code in the device tree and
// a hostname that says "TrimUI". Brick images are not obliged to provide
// either. The kernel it ships may describe only the SoC ("sun50iw10",
// "allwinner,sun50i-a100"), the hostname may be a build string, and the model
// code may appear nowhere the kernel prints. What the image does always have is
// its own launcher, and that binary names the model: a community launcher tells
// a Brick from a Smart Pro with `strings /usr/trimui/bin/MainUI | grep ^Trimui`.
//
// So detection collects evidence instead of trusting one file:
//
//  1. a launcher's exported DEVICE / PLATFORM (NextUI reports the Brick as
//     PLATFORM=tg5040 plus DEVICE=brick, so the code alone would be wrong),
//  2. model codes in the device tree, /proc/cpuinfo and the hostname,
//  3. model names in the firmware's own strings and release files,
//  4. the panel, which separates the 4:3 Brick family from the 16:9 Smart Pro
//     family but cannot name a variant.
//
// Every path and file is a variable so a test can stand in for a device, the
// same way the Wi-Fi probes are tested.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var (
	// deviceCPUInfoPath carries the "Hardware"/"Revision" lines on an Allwinner
	// BSP kernel; the board name lands there on some images.
	deviceCPUInfoPath = "/proc/cpuinfo"
	// deviceNameFiles are the small text files an image may name its model in.
	// They are read whole: none is more than a few kilobytes.
	deviceNameFiles = []string{
		"/etc/os-release",
		"/etc/trimui_version",
		"/etc/trimui/version",
		"/etc/version",
	}
	// deviceFirmwareDirs prove "this is a TrimUI image" even when no file says
	// which model it is.
	deviceFirmwareDirs = []string{
		"/usr/trimui",
		"/mnt/SDCARD/.system",
	}
	// deviceMainUIPath is TrimUI's own launcher. It is large, so it is scanned
	// in chunks and only when the cheaper sources have not named the model.
	deviceMainUIPath = "/usr/trimui/bin/MainUI"
	// devicePanelFiles describe the framebuffer mode. fb0/modes keeps the panel
	// geometry; virtual_size can be larger when the buffer is double-wide, so
	// its value is only used when it matches a known panel.
	devicePanelFiles = []string{
		"/sys/class/graphics/fb0/modes",
		"/sys/class/graphics/fb0/virtual_size",
	}
)

// firmwareModelNames are the strings the vendor UI carries, most specific first.
// "Trimui Brick" is a prefix of nothing else, but "Trimui Smart Pro" is a prefix
// of the Pro S, so the longer name is checked first.
var firmwareModelNames = []string{
	"Trimui Brick Pro",
	"Trimui Smart Pro S",
	"Trimui Smart Pro",
	"Trimui Brick",
	"Trimui Hammer",
}

// deviceIdentity is the union of what a platform exposes. An empty field means
// the source was missing, not that it said nothing.
type deviceIdentity struct {
	EnvModel    string // TRIMUI_MODEL, if the launcher exports one
	EnvDevice   string // DEVICE, as NextUI/MinUI set it ("brick")
	EnvPlatform string // PLATFORM, as NextUI/MinUI set it ("tg5040")
	Model       string // device-tree model
	Compatible  string // device-tree compatible list
	CPUInfo     string // /proc/cpuinfo Hardware/Revision values
	Hostname    string
	Names       []string // model strings found in the firmware
	PanelW      int32
	PanelH      int32
	TrimUI      bool // the image is demonstrably a TrimUI one
}

// readDeviceIdentity probes every identity source. It never fails: a source
// that cannot be read is an empty field.
func readDeviceIdentity() deviceIdentity {
	id := deviceIdentity{
		EnvModel:    os.Getenv("TRIMUI_MODEL"),
		EnvDevice:   os.Getenv("DEVICE"),
		EnvPlatform: os.Getenv("PLATFORM"),
		Hostname:    readDeviceModelFile("/etc/hostname"),
		CPUInfo:     cpuInfoIdentity(deviceCPUInfoPath),
	}
	if runtime.GOOS == "linux" {
		id.Model, id.Compatible = deviceTreeIdentity()
	}
	fw := probeFirmwareIdentity()
	id.Names = fw.Names
	id.TrimUI = fw.TrimUI
	id.PanelW, id.PanelH = readPanelSize()
	return id
}

// cpuInfoIdentity returns the board-describing values of /proc/cpuinfo. An
// Allwinner BSP kernel prints the SoC under "Hardware" and sometimes the board
// under "Revision"; a generic ARM kernel prints "model name".
func cpuInfoIdentity(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var parts []string
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Hardware", "Revision", "model name", "Processor":
			if v := strings.TrimSpace(value); v != "" {
				parts = append(parts, v)
			}
		}
	}
	return strings.Join(parts, " ")
}

// firmwareIdentity is what the running image says about its own model.
type firmwareIdentity struct {
	Names  []string // every model string found, in firmwareModelNames order
	TrimUI bool     // the image has TrimUI's own files
}

// probeFirmwareIdentity reads the launcher binary and the release files. The
// binary scan only happens when a TrimUI file is present, so a desktop Linux
// build never reads a large file to find nothing.
func probeFirmwareIdentity() firmwareIdentity {
	var out firmwareIdentity
	for _, dir := range deviceFirmwareDirs {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			out.TrimUI = true
		}
	}
	for _, path := range deviceNameFiles {
		text := readDeviceModelFile(path)
		if text == "" {
			continue
		}
		out.Names = append(out.Names, text)
		if strings.Contains(strings.ToUpper(text), "TRIMUI") {
			out.TrimUI = true
		}
	}
	if out.TrimUI {
		out.Names = append(out.Names, mainUIModelNames(deviceMainUIPath)...)
	}
	return out
}

// mainUIModelNames scans the vendor launcher for the model strings it was built
// with. The file is tens of megabytes on some images, so it is read in chunks
// with an overlap just short of the longest name — a name that straddles two
// chunks is still found — and never loaded whole.
func mainUIModelNames(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	longest := 0
	for _, name := range firmwareModelNames {
		if len(name) > longest {
			longest = len(name)
		}
	}
	const chunk = 128 << 10
	// maxScan bounds a scan of a pathological file: the strings live in the
	// image's data, well inside the first few tens of megabytes.
	const maxScan = 64 << 20

	buf := make([]byte, chunk)
	var carry []byte
	matches := make([]bool, len(firmwareModelNames))
	var scanned int64
	for scanned < maxScan {
		n, readErr := f.Read(buf)
		if n > 0 {
			scanned += int64(n)
			window := make([]byte, 0, len(carry)+n)
			window = append(window, carry...)
			window = append(window, buf[:n]...)
			for i, name := range firmwareModelNames {
				if !matches[i] && bytes.Contains(window, []byte(name)) {
					matches[i] = true
				}
			}
			keep := longest - 1
			if keep > len(window) {
				keep = len(window)
			}
			carry = append(carry[:0], window[len(window)-keep:]...)
		}
		if readErr != nil {
			break
		}
	}

	var found []string
	for i, name := range firmwareModelNames {
		if !matches[i] {
			continue
		}
		// A longer model name contains the shorter one: "Trimui Smart Pro S"
		// holds "Trimui Smart Pro", and "Trimui Brick Pro" holds "Trimui
		// Brick". Keeping both would read one model as two, so only the longest
		// name at a site counts.
		contained := false
		for j, other := range firmwareModelNames {
			if i != j && matches[j] && strings.Contains(other, name) {
				contained = true
				break
			}
		}
		if !contained {
			found = append(found, name)
		}
	}
	return found
}

// readPanelSize reads the framebuffer geometry. A mode list is the panel's own
// size and is trusted as-is; virtual_size describes the buffer, which may be a
// double-wide pan surface (2048,768 for a 1024x768 panel), so it only counts
// when it names a known panel or exactly twice its width.
func readPanelSize() (int32, int32) {
	for _, path := range devicePanelFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		w, h, ok := parsePanelSize(string(data))
		if !ok {
			continue
		}
		trusted := strings.HasSuffix(filepath.Base(path), "modes")
		if trusted || deviceForPanel(w, h) != DeviceUnknown {
			return w, h
		}
		if deviceForPanel(w/2, h) != DeviceUnknown {
			return w / 2, h
		}
	}
	return 0, 0
}

// parsePanelSize reads the first plausible "WxH" out of an fbdev modes line
// ("U:1024x768p-60") or a virtual_size pair ("1024,768").
func parsePanelSize(text string) (int32, int32, bool) {
	for _, field := range strings.Fields(text) {
		field = strings.TrimPrefix(field, "U:")
		field = strings.TrimPrefix(field, "D:")
		sep := strings.IndexAny(field, "xX,")
		if sep <= 0 {
			continue
		}
		w, err := strconv.Atoi(field[:sep])
		if err != nil {
			continue
		}
		rest := field[sep+1:]
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		h, err := strconv.Atoi(rest[:end])
		if err != nil {
			continue
		}
		if w < 240 || h < 240 || w > 8192 || h > 8192 {
			continue
		}
		return int32(w), int32(h), true
	}
	return 0, 0, false
}

// deviceForPanel names the family a panel belongs to. It is deliberately a
// family, not a model: the 1024x768 panel is shared by the Brick, the Brick Pro
// and the Hammer, and 1280x720 by the Smart Pro and the Smart Pro S. Variant
// names only come from what the firmware says.
func deviceForPanel(w, h int32) DeviceModel {
	switch {
	case w == 1024 && h == 768:
		return DeviceTrimuiBrick
	case w == 1280 && h == 720, w == 720 && h == 1280:
		return DeviceTrimuiSmartPro
	}
	return DeviceUnknown
}

// deviceFamily is the base model a variant belongs to, which is the unit a
// panel can decide.
func deviceFamily(d DeviceModel) DeviceModel {
	switch d {
	case DeviceTrimuiBrick, DeviceTrimuiBrickPro, DeviceTrimuiHammer:
		return DeviceTrimuiBrick
	case DeviceTrimuiSmartPro, DeviceTrimuiSmartProS:
		return DeviceTrimuiSmartPro
	}
	return d
}

// deviceCandidate is one model a source named, with that source for the log.
type deviceCandidate struct {
	model  DeviceModel
	source string
}

// deviceFromEnvHints reads what a launcher exported. DEVICE is the device
// itself, so it is checked before PLATFORM: NextUI reports a Brick as
// PLATFORM=tg5040, DEVICE=brick, and trusting the platform code there is how a
// Brick becomes a Smart Pro.
func deviceFromEnvHints(device, platform, model string) DeviceModel {
	if d := envModelValue(model); d != DeviceUnknown {
		return d
	}
	if d := envModelValue(device); d != DeviceUnknown {
		return d
	}
	return DeviceModelForHardwareID(platform)
}

// envModelValue classifies one exported value: a model code, a launcher's bare
// device name ("brick"), or a marketing name.
func envModelValue(raw string) DeviceModel {
	if strings.TrimSpace(raw) == "" {
		return DeviceUnknown
	}
	if d := envDeviceName(raw); d != DeviceUnknown {
		return d
	}
	if d := detectDeviceForHardware("linux", raw, ""); d != DeviceUnknown {
		return d
	}
	return detectDeviceForSystem("linux", raw)
}

// envDeviceName maps the bare names MinUI-family launchers export. They carry
// no "TrimUI" and no model code, which the marketing classifier requires.
func envDeviceName(raw string) DeviceModel {
	name := strings.ToUpper(strings.TrimSpace(raw))
	name = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(name)
	switch name {
	case "BRICK":
		return DeviceTrimuiBrick
	case "BRICKPRO":
		return DeviceTrimuiBrickPro
	case "HAMMER":
		return DeviceTrimuiHammer
	case "SMARTPRO":
		return DeviceTrimuiSmartPro
	case "SMARTPROS":
		return DeviceTrimuiSmartProS
	}
	return DeviceUnknown
}

// classifyDevice names the board from the union of the evidence and says which
// source decided it. The order is by how much each source knows: an exported
// device name is the device itself, a kernel model code is the board, a model
// name in the firmware is the image's own label, and the panel only decides the
// family.
func classifyDevice(id deviceIdentity) (DeviceModel, string) {
	if d := deviceFromEnvHints(id.EnvDevice, id.EnvPlatform, id.EnvModel); d != DeviceUnknown {
		return d, "launcher env"
	}
	if d := detectDeviceForHardware("linux", id.Model, id.Compatible); d != DeviceUnknown {
		return d, "device tree"
	}
	if d := detectDeviceForHardware("linux", id.CPUInfo, ""); d != DeviceUnknown {
		return d, "cpuinfo"
	}
	if d := detectDeviceForHardware("linux", id.Hostname, ""); d != DeviceUnknown {
		return d, "hostname"
	}

	var candidates []deviceCandidate
	for _, name := range id.Names {
		if d := detectDeviceForSystem("linux", name); d != DeviceUnknown {
			candidates = append(candidates, deviceCandidate{d, "firmware name"})
			continue
		}
		if d := detectDeviceForHardware("linux", name, ""); d != DeviceUnknown {
			candidates = append(candidates, deviceCandidate{d, "firmware name"})
		}
	}
	if d := detectDeviceForSystem("linux", id.Hostname+" "+id.Model); d != DeviceUnknown {
		candidates = append(candidates, deviceCandidate{d, "hostname/model"})
	}
	if c, ok := nameDeviceFromCandidates(candidates, id.PanelW, id.PanelH); ok {
		return c.model, c.source
	}
	if id.TrimUI {
		if d := deviceForPanel(id.PanelW, id.PanelH); d != DeviceUnknown {
			return d, "trimui firmware + panel"
		}
	}
	return DeviceUnknown, ""
}

// nameDeviceFromCandidates narrows the named models to the family the panel
// proves and picks one. A shared launcher binary can carry several model names;
// the panel then rules out the other family, and when two names inside the
// family survive, the base model wins: under-claiming a variant is safer than
// claiming one the hardware is not.
func nameDeviceFromCandidates(candidates []deviceCandidate, panelW, panelH int32) (deviceCandidate, bool) {
	if len(candidates) == 0 {
		return deviceCandidate{}, false
	}
	if family := deviceForPanel(panelW, panelH); family != DeviceUnknown {
		var inFamily []deviceCandidate
		for _, c := range candidates {
			if deviceFamily(c.model) == family {
				inFamily = append(inFamily, c)
			}
		}
		if len(inFamily) == 0 {
			return deviceCandidate{model: family, source: "panel " + panelLabel(panelW, panelH)}, true
		}
		return pickBaseModel(inFamily), true
	}
	// No panel: only an unambiguous answer is used.
	first := candidates[0]
	for _, c := range candidates[1:] {
		if c.model != first.model {
			return deviceCandidate{}, false
		}
	}
	return first, true
}

// pickBaseModel returns the family's base model when it is among the candidates,
// so a shared binary's "Trimui Brick Pro" next to "Trimui Brick" names a Brick
// rather than upgrading the hardware.
func pickBaseModel(inFamily []deviceCandidate) deviceCandidate {
	for _, c := range inFamily {
		if deviceFamily(c.model) == c.model {
			return c
		}
	}
	return inFamily[0]
}

// panelLabel formats a panel size for logs.
func panelLabel(w, h int32) string {
	return fmt.Sprintf("%dx%d", w, h)
}

// identitySummary is the one-line evidence trail the diagnostics report carries:
// the strings that named the board and nothing that identifies the user. The
// hostname is left out on purpose — it is the one field that can be a person's
// name — and the launcher env is only shown when it contributed.
func identitySummary(id deviceIdentity) string {
	panel := "none"
	if id.PanelW > 0 && id.PanelH > 0 {
		panel = panelLabel(id.PanelW, id.PanelH)
	}
	model := strings.TrimSpace(id.Model)
	if model == "" {
		model = "none"
	}
	compatible := strings.TrimSpace(id.Compatible)
	if len(compatible) > 90 {
		compatible = compatible[:90] + "…"
	}
	if compatible == "" {
		compatible = "none"
	}
	firmware := "no"
	if id.TrimUI {
		firmware = "yes"
	}
	summary := fmt.Sprintf(" · dt=%q compat=%q panel=%s firmware=%s", model, compatible, panel, firmware)
	if env := strings.TrimSpace(id.EnvDevice + " " + id.EnvPlatform); env != "" {
		summary += fmt.Sprintf(" env=%q", env)
	}
	return summary
}
