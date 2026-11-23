package main

// devicedrivers.go — what each TrimUI model is made of, in one table, and the
// registry the "Drivers & hardware" page reads.
//
// The four models share a firmware family but not a board:
//
//   - Smart Pro (TG5040), Brick (TG3040, the Hammer too) and Brick Pro (TG4040;
//     TrimUI's store calls it TG4070) are the Allwinner A133P: four Cortex-A53,
//     a PowerVR GE8300 driven through the vendor's fbdev EGL (/dev/fb0 plus
//     a PowerVR render node), the sunxi disp2 driver's /dev/disp for the backlight, and
//     a 4.9 BSP kernel.
//   - Smart Pro S (TG5050) is the Allwinner A523: eight Cortex-A55 in two
//     cpufreq policies, a Mali-G57 through the vendor's kbase (/dev/mali0) with
//     a DRM/KMS display, a sysfs backlight, an AXP717 PMIC, an AIC8800D80
//     Wi-Fi/Bluetooth chip, a pwm fan, and a 5.15 BSP kernel.
//
// The table is what was published or seen on the devices (TrimUI's specs and
// the devices themselves), gathered for the 2026-11-17 driver pass. It is not
// proof: every driver still probes its node first, and uses the table only to
// pick an order ("try the sysfs backlight first") or to say "this model has no
// fan" instead of "fan not found". A zero field means "not known".
//
// The registry half lets each subsystem's own file report how its driver is
// doing (registerDriverStatus from an init func); the page asks every
// registered function off the render thread, each under a short deadline.

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// deviceGPUStack is how a model's GPU and display reach userspace.
type deviceGPUStack string

const (
	// gpuStackPowerVRFbdev is the A133P's PowerVR GE8300 with the vendor's
	// fbdev EGL: /dev/fb0 is the display and a DRM render node or
	// /dev/pvr_sync is the GPU. SDL's
	// backend there is named "mali" (SDL's generic fbdev-EGL driver, not the
	// GPU).
	gpuStackPowerVRFbdev deviceGPUStack = "powervr-fbdev"
	// gpuStackMaliDRM is the A523's Mali-G57 through the vendor's kbase driver
	// (/dev/mali0) with a DRM/KMS display (/dev/dri/card0). The
	// kernel blanks the panel when the display is released.
	gpuStackMaliDRM deviceGPUStack = "mali-drm"
)

// The chips. deviceSoCFrom names one of these from the running board.
const (
	socA133P = "A133P"
	socA523  = "A523"
)

// deviceCPUCluster is one cpufreq policy's cores.
type deviceCPUCluster struct {
	Policy string // cpufreq policy directory: "policy0"
	Cores  int    // cores in the cluster
	Core   string // "Cortex-A53"
	MaxMHz int    // top clock observed or published; 0 = unknown (read cpufreq)
}

// deviceHW is one model's hardware as best known. A zero field is unknown.
// Live probes always win over it; see the file comment.
type deviceHW struct {
	Model       DeviceModel        // the model this row describes
	Code        string             // TrimUI model code: "TG5040"
	CodeAliases []string           // other codes seen for the board: "TG4070"
	Platform    string             // NextUI/MinUI PLATFORM its images use: "tg5040"
	SoC         string             // socA133P / socA523
	SoCTree     string             // Allwinner family in the device tree: "sun50iw10"
	Clusters    []deviceCPUCluster // cpufreq policies, in policy order
	GPU         string             // "PowerVR GE8300"
	GPUStack    deviceGPUStack     // how the GPU and display reach userspace
	GPUNode     string             // the GPU's expected device node on stock firmware
	DisplayNode string             // the display node: "/dev/fb0", "/dev/dri/card0"
	Kernel      string             // vendor kernel series: "4.9"
	PanelW      int32              // panel width in pixels, landscape
	PanelH      int32              // panel height in pixels, landscape
	PanelInch   float64            // panel diagonal in inches
	Backlight   string             // backend tried first: brightnessBackendDisp / brightnessBackendSysfs
	PMIC        string             // power-management IC: "AXP717"; "" unknown
	WiFiChip    string             // "XR829"; "" unknown
	BTChip      string             // "XR829"; "" unknown
	BTUART      string             // HCI UART the chip attaches on: "ttyS1"; "" unknown
	HasFan      bool               // a pwm fan (cooling_device type pwm-fan)
	HasSticks   bool               // analog sticks (trimui_inputd serial sticks)
	ClickSticks bool               // the sticks press in: L3 / R3 (not the Smart Pro's)
	HasLEDs     bool               // RGB zones under /sys/class/led_anim
	RumbleMaxUV int                // motor voltage ceiling in microvolts; 0 unknown
	VideoOut    string             // external display: "USB-C DP alt mode"; "" none
}

// deviceHWTable is the table. Read it through deviceHardware.
var deviceHWTable = []deviceHW{
	{
		// Its sticks do not press in: no L3 / R3 (stickclicks.go).
		Model: DeviceTrimuiSmartPro, Code: "TG5040", Platform: "tg5040",
		SoC: socA133P, SoCTree: "sun50iw10",
		Clusters: []deviceCPUCluster{{Policy: "policy0", Cores: 4, Core: "Cortex-A53", MaxMHz: 2000}},
		GPU:      "PowerVR GE8300", GPUStack: gpuStackPowerVRFbdev, GPUNode: "/dev/dri/renderD128", DisplayNode: "/dev/fb0",
		Kernel: "4.9", PanelW: 1280, PanelH: 720, PanelInch: 4.96,
		Backlight: brightnessBackendDisp, PMIC: "AXP2202",
		WiFiChip: "XR829", BTChip: "XR829", BTUART: "ttyS1",
		HasSticks: true, HasLEDs: true, RumbleMaxUV: 3300000,
	},
	{
		Model: DeviceTrimuiBrick, Code: "TG3040", Platform: "tg5040",
		SoC: socA133P, SoCTree: "sun50iw10",
		Clusters: []deviceCPUCluster{{Policy: "policy0", Cores: 4, Core: "Cortex-A53", MaxMHz: 1800}},
		GPU:      "PowerVR GE8300", GPUStack: gpuStackPowerVRFbdev, GPUNode: "/dev/dri/renderD128", DisplayNode: "/dev/fb0",
		Kernel: "4.9", PanelW: 1024, PanelH: 768, PanelInch: 3.2,
		Backlight: brightnessBackendDisp, PMIC: "AXP",
		WiFiChip: "XR829", BTChip: "XR829", BTUART: "ttyS1",
		HasLEDs: true, RumbleMaxUV: 3300000,
	},
	{
		// The Hammer is a Brick variant on the same TG3040 board.
		Model: DeviceTrimuiHammer, Code: "TG3040", Platform: "tg5040",
		SoC: socA133P, SoCTree: "sun50iw10",
		Clusters: []deviceCPUCluster{{Policy: "policy0", Cores: 4, Core: "Cortex-A53", MaxMHz: 1800}},
		GPU:      "PowerVR GE8300", GPUStack: gpuStackPowerVRFbdev, GPUNode: "/dev/dri/renderD128", DisplayNode: "/dev/fb0",
		Kernel: "4.9", PanelW: 1024, PanelH: 768,
		Backlight: brightnessBackendDisp, PMIC: "AXP",
		WiFiChip: "XR829", BTChip: "XR829", BTUART: "ttyS1",
		HasLEDs: true, RumbleMaxUV: 3300000,
	},
	{
		// The A523 board (A523-PRO2-AXP717C). The two clusters' published top
		// clock is 2.0 GHz; which policy reaches it is read from cpufreq.
		Model: DeviceTrimuiSmartProS, Code: "TG5050", Platform: "tg5050",
		SoC: socA523, SoCTree: "sun55iw3",
		Clusters: []deviceCPUCluster{
			{Policy: "policy0", Cores: 4, Core: "Cortex-A55"},
			{Policy: "policy4", Cores: 4, Core: "Cortex-A55", MaxMHz: 2000},
		},
		GPU: "Mali-G57 MC1", GPUStack: gpuStackMaliDRM, GPUNode: "/dev/mali0", DisplayNode: "/dev/dri/card0",
		Kernel: "5.15", PanelW: 1280, PanelH: 720, PanelInch: 4.96,
		Backlight: brightnessBackendSysfs, PMIC: "AXP717",
		WiFiChip: "AIC8800D80", BTChip: "AIC8800D80", BTUART: "ttyAS1",
		HasFan: true, HasSticks: true, ClickSticks: true, HasLEDs: true, RumbleMaxUV: 3300000,
	},
	{
		// The Brick Pro is the Brick's A133P and GPU on a 4.9 kernel, with a
		// 3.95" panel at the Brick's resolution, Hall sticks with RGB rings,
		// USB-C video out, and a rumble motor capped at 2.5 V.
		Model: DeviceTrimuiBrickPro, Code: "TG4040", CodeAliases: []string{"TG4070"}, Platform: "tg5040",
		SoC: socA133P, SoCTree: "sun50iw10",
		Clusters: []deviceCPUCluster{{Policy: "policy0", Cores: 4, Core: "Cortex-A53", MaxMHz: 1800}},
		GPU:      "PowerVR GE8300", GPUStack: gpuStackPowerVRFbdev, GPUNode: "/dev/dri/renderD128", DisplayNode: "/dev/fb0",
		Kernel: "4.9", PanelW: 1024, PanelH: 768, PanelInch: 3.95,
		Backlight: brightnessBackendDisp,
		WiFiChip:  "XR829", BTChip: "XR829", BTUART: "ttyS1",
		HasSticks: true, ClickSticks: true, HasLEDs: true, RumbleMaxUV: 2500000,
		VideoOut: "USB-C DP alt mode",
	},
}

// deviceHardware returns d's row. A model without one (a dev build, an
// unidentified board) gets a row with only Model set: every fact unknown.
func deviceHardware(d DeviceModel) deviceHW {
	for _, hw := range deviceHWTable {
		if hw.Model == d {
			return hw
		}
	}
	return deviceHW{Model: d}
}

// deviceHardwareCurrent is the running board's row.
func deviceHardwareCurrent() deviceHW { return deviceHardware(DetectDevice()) }

// ── The chip, from the running board ──────────────────────────────────────

// deviceSoCEvidence is what a running board says about its chip.
type deviceSoCEvidence struct {
	Tree     string   // device-tree model + compatible, cpuinfo Hardware
	CPUParts []string // every "CPU part" value in /proc/cpuinfo
	CPUs     int      // online CPUs
	Mali     bool     // a /dev/mali* node
	PowerVR  bool     // /dev/pvr_sync or a render node owned by pvrsrvkm
	Kernel   string   // kernel release: "5.15.147"
}

// deviceSoCFrom names the chip, or "" when the evidence does not agree. The
// device tree's own SoC name decides when it has one. Otherwise the CPU cores
// (A53 = 0xd03 on the A133P, A55 = 0xd05 on the A523) and the GPU node vote,
// and the core count and kernel series only count when nothing stronger
// spoke. why is the evidence in a few words, for the log.
func deviceSoCFrom(ev deviceSoCEvidence) (soc, why string) {
	tree := strings.ToLower(ev.Tree)
	switch {
	case strings.Contains(tree, "sun55i"), strings.Contains(tree, "a523"):
		return socA523, "device tree " + deviceFirstMatch(tree, "sun55iw3", "sun55i", "a523")
	case strings.Contains(tree, "sun50iw10"), strings.Contains(tree, "sun50i-a100"), strings.Contains(tree, "a133"):
		return socA133P, "device tree " + deviceFirstMatch(tree, "sun50iw10", "sun50i-a100", "a133")
	}
	var strongA523, strongA133 []string
	a55, a53, otherCore := 0, 0, 0
	for _, part := range ev.CPUParts {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "0xd05":
			a55++
		case "0xd03":
			a53++
		default:
			otherCore++
		}
	}
	switch {
	case a55 > 0:
		strongA523 = append(strongA523, "Cortex-A55 cores")
	case a53 > 0 && otherCore == 0:
		strongA133 = append(strongA133, "Cortex-A53 cores")
	}
	switch {
	case ev.Mali && !ev.PowerVR:
		strongA523 = append(strongA523, "Mali GPU")
	case ev.PowerVR && !ev.Mali:
		strongA133 = append(strongA133, "PowerVR GPU")
	}
	if len(strongA523) > 0 && len(strongA133) == 0 {
		return socA523, strings.Join(strongA523, ", ")
	}
	if len(strongA133) > 0 && len(strongA523) == 0 {
		return socA133P, strings.Join(strongA133, ", ")
	}
	if len(strongA523) > 0 {
		// The strong signs disagree: no answer is safer than a wrong one.
		return "", "conflicting: " + strings.Join(append(strongA523, strongA133...), ", ")
	}
	major := deviceKernelMajor(ev.Kernel)
	switch {
	case ev.CPUs == 8 && major >= 5:
		return socA523, "8 CPUs, kernel " + ev.Kernel
	case ev.CPUs > 0 && ev.CPUs <= 4 && major == 4:
		return socA133P, strconv.Itoa(ev.CPUs) + " CPUs, kernel " + ev.Kernel
	}
	return "", ""
}

// deviceFirstMatch returns the first of names that s contains.
func deviceFirstMatch(s string, names ...string) string {
	for _, n := range names {
		if strings.Contains(s, n) {
			return n
		}
	}
	return ""
}

// deviceKernelMajor is a release's major version ("5.15.147" → 5), 0 when unknown.
func deviceKernelMajor(release string) int {
	head, _, _ := strings.Cut(strings.TrimSpace(release), ".")
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0
	}
	return n
}

// deviceKernelRelease is the running kernel's release ("4.9.191"), "" where
// the platform has no procfs (Windows).
func deviceKernelRelease() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// deviceCPUParts returns every "CPU part" value of a cpuinfo text.
func deviceCPUParts(text string) []string {
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "CPU part" {
			parts = append(parts, strings.TrimSpace(value))
		}
	}
	return parts
}

// deviceMaliNode is the first /dev/mali* node, or "".
func deviceMaliNode() string {
	matches, _ := filepath.Glob("/dev/mali*")
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[0]
}

// devicePowerVRNode finds the PowerVR node, accepting both firmware layouts.
// Stock Smart Pro 1.1.0 has no /dev/pvr_sync: pvrsrvkm owns DRM's renderD128.
// Require the driver identity before treating a generic DRM node as PowerVR.
func devicePowerVRNode() string {
	if pathExists("/dev/pvr_sync") {
		return "/dev/pvr_sync"
	}
	if !pathExists("/dev/dri/renderD128") {
		return ""
	}
	if data, err := os.ReadFile("/sys/class/drm/card0/device/uevent"); err == nil &&
		strings.Contains(strings.ToLower(string(data)), "driver=pvrsrvkm") {
		return "/dev/dri/renderD128"
	}
	return ""
}

// probeDeviceSoC reads the chip evidence of the running board. tree is the
// device-tree model and compatible strings already read, plus cpuinfo's
// board lines.
func probeDeviceSoC(tree string) (soc, why, kernel string) {
	kernel = deviceKernelRelease()
	if runtime.GOOS != "linux" {
		return "", "", kernel
	}
	ev := deviceSoCEvidence{Tree: tree, CPUs: runtime.NumCPU(), Kernel: kernel}
	if data, err := os.ReadFile(deviceCPUInfoPath); err == nil {
		ev.CPUParts = deviceCPUParts(string(data))
	}
	ev.Mali = deviceMaliNode() != ""
	ev.PowerVR = devicePowerVRNode() != ""
	soc, why = deviceSoCFrom(ev)
	return soc, why, kernel
}

// deviceCorrectForSoC fixes the one mistake the names and the panel can make:
// the Smart Pro and the Smart Pro S share the 1280x720 panel, and a launcher
// binary may carry both names, so a Smart Pro S can be named a Smart Pro — and
// then get the A133P's PowerVR builds, Bluetooth attach and backlight. The
// chip settles it, both ways. A Brick-family name on an A523 is left alone
// (no such board exists) and only logged.
func deviceCorrectForSoC(d DeviceModel, source, soc string) (DeviceModel, string) {
	switch {
	case d == DeviceTrimuiSmartPro && soc == socA523:
		return DeviceTrimuiSmartProS, source + " + A523 chip"
	case d == DeviceTrimuiSmartProS && soc == socA133P:
		return DeviceTrimuiSmartPro, source + " + A133P chip"
	case deviceFamily(d) == DeviceTrimuiBrick && soc == socA523:
		// Only once logging exists: this runs inside DetectDevice's sync.Once,
		// and a first log line there would start InitLogging, which asks
		// DetectDevice for the level and waits on that Once forever (the
		// 2026-11-19 start-up hang). The identity line logged after
		// InitLogging carries the chip either way.
		if logger != nil {
			logWarn("device", "%s named by %s, but the chip looks like an A523; kept as named", d, source)
		}
	}
	return d, source
}

// ── Driver status registry ────────────────────────────────────────────────

// driverState is one subsystem's state on the Drivers & hardware page.
type driverState int

const (
	driverUnknown driverState = iota // not checked, or could not tell
	driverOK                         // found and answering
	driverMissing                    // this model should have it; not found
	driverNotUsed                    // this model has no such part
	driverProblem                    // found, but failing (Detail says how)
)

// driverStateLabel is the state as the page says it.
func driverStateLabel(s driverState) string {
	switch s {
	case driverOK:
		return "OK"
	case driverMissing:
		return "not found"
	case driverNotUsed:
		return "not used on this model"
	case driverProblem:
		return "problem"
	}
	return "unknown"
}

// driverStatus is what one subsystem reports. Keep each field short: the page
// shows "Name · state" with Driver, Node and Detail on the line under it.
type driverStatus struct {
	State  driverState
	Driver string // what drives it: "sunxi disp2 ioctl", "AIC8800D80 (aic8800_sdio)"
	Node   string // node or interface used: "/dev/disp", "wlan0"
	Detail string // one short fact: "level 60%", "2 zones", "attached on ttyS1"
}

// The row names the page orders by. A subsystem registers under one of these
// (or a new name, which is listed after them alphabetically).
const (
	driverRowModel     = "Model"
	driverRowDisplay   = "Display"
	driverRowBacklight = "Backlight"
	driverRowKernel    = "Kernel"
	driverRowFirmware  = "Firmware"
	driverRowCPU       = "CPU"
	driverRowAudio     = "Audio"
	driverRowBattery   = "Battery"
	driverRowCharger   = "Charger"
	driverRowFan       = "Fan"
	driverRowLEDs      = "LEDs"
	driverRowRumble    = "Rumble"
	driverRowSticks    = "Sticks"
	driverRowWiFi      = "Wi-Fi"
	driverRowBluetooth = "Bluetooth"
	driverRowVideoOut  = "Video out"
)

// driverRowOrder is the page's order.
var driverRowOrder = []string{
	driverRowModel, driverRowDisplay, driverRowBacklight, driverRowKernel, driverRowFirmware,
	driverRowCPU, driverRowAudio, driverRowBattery, driverRowCharger, driverRowFan,
	"Thermal", driverRowLEDs, driverRowRumble, driverRowSticks, driverRowWiFi,
	driverRowBluetooth, driverRowVideoOut,
}

var driverRegistry struct {
	mu  sync.Mutex
	fns map[string]func() driverStatus
}

// registerDriverStatus adds (or replaces) the status function of one
// subsystem. Call it from an init func in the subsystem's own file. fn runs
// off the render thread, under a deadline (driverStatusTimeout); it should be
// a few file reads, never a write and never a process that can hang.
func registerDriverStatus(name string, fn func() driverStatus) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		return
	}
	driverRegistry.mu.Lock()
	defer driverRegistry.mu.Unlock()
	if driverRegistry.fns == nil {
		driverRegistry.fns = make(map[string]func() driverStatus)
	}
	driverRegistry.fns[name] = fn
}

// driverStatusTimeout bounds one status function; the page is never held
// longer than this by a sysfs read that hangs.
const driverStatusTimeout = 1500 * time.Millisecond

// driverStatusRow is one line of the page.
type driverStatusRow struct {
	Name   string
	Status driverStatus
}

// driverStatusEnv is what the caller read on the main thread (SDL's video
// backend) for the display row.
type driverStatusEnv struct {
	VideoDriver string
}

// collectDriverStatus asks every subsystem, this file's own rows included,
// at once and each under driverStatusTimeout. It blocks for at most about
// that long; call it off the render thread.
func collectDriverStatus(env driverStatusEnv) []driverStatusRow {
	fns := map[string]func() driverStatus{
		driverRowModel:     driverModelStatus,
		driverRowDisplay:   func() driverStatus { return driverDisplayStatus(env.VideoDriver) },
		driverRowBacklight: driverBacklightStatus,
		driverRowKernel:    driverKernelStatus,
		driverRowFirmware:  driverFirmwareStatus,
	}
	hw := deviceHardwareCurrent()
	if hw.Model.IsTrimui() {
		fns[driverRowVideoOut] = driverVideoOutStatus
	}
	driverRegistry.mu.Lock()
	for name, fn := range driverRegistry.fns {
		fns[name] = fn
	}
	driverRegistry.mu.Unlock()
	// On a handheld every part gets a line: one nobody registered says what
	// the table knows ("no fan on this model", "not checked"). A desktop
	// build lists only what answered.
	if hw.Model.IsTrimui() {
		for _, name := range driverRowOrder {
			if _, ok := fns[name]; !ok {
				name := name
				fns[name] = func() driverStatus { return driverTableStatus(name, hw) }
			}
		}
	}

	type answer struct {
		name string
		st   driverStatus
	}
	results := make(chan answer, len(fns))
	for name, fn := range fns {
		// A check still running from an earlier opening (a read that hung)
		// is not started again: reopening the page cannot pile goroutines up
		// behind one stuck node.
		if !driverInflightStart(name) {
			results <- answer{name, driverStatus{State: driverUnknown, Detail: "still busy since the last check"}}
			continue
		}
		go func(name string, fn func() driverStatus) {
			defer driverInflightDone(name)
			defer func() {
				if r := recover(); r != nil {
					logWarn("drivers", "%s status failed: %v", name, r)
					results <- answer{name, driverStatus{State: driverProblem, Detail: "status check failed"}}
				}
			}()
			results <- answer{name, fn()}
		}(name, fn)
	}
	got := make(map[string]driverStatus, len(fns))
	deadline := time.NewTimer(driverStatusTimeout)
	defer deadline.Stop()
wait:
	for len(got) < len(fns) {
		select {
		case a := <-results:
			got[a.name] = a.st
		case <-deadline.C:
			break wait
		}
	}
	names := make([]string, 0, len(fns))
	for name := range fns {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		ri, rj := driverRowRank(names[i]), driverRowRank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	rows := make([]driverStatusRow, 0, len(names))
	for _, name := range names {
		st, ok := got[name]
		if !ok {
			st = driverStatus{State: driverUnknown, Detail: "did not answer in time"}
			logWarn("drivers", "%s status did not answer within %s", name, driverStatusTimeout)
		}
		rows = append(rows, driverStatusRow{Name: name, Status: st})
	}
	return rows
}

// driverInflight names the status functions still running, so at most one
// call of each is ever outstanding.
var driverInflight struct {
	mu    sync.Mutex
	names map[string]bool
}

// driverInflightStart marks name running; false when it already is.
func driverInflightStart(name string) bool {
	driverInflight.mu.Lock()
	defer driverInflight.mu.Unlock()
	if driverInflight.names[name] {
		return false
	}
	if driverInflight.names == nil {
		driverInflight.names = make(map[string]bool)
	}
	driverInflight.names[name] = true
	return true
}

// driverInflightDone clears name's mark when its call returns.
func driverInflightDone(name string) {
	driverInflight.mu.Lock()
	delete(driverInflight.names, name)
	driverInflight.mu.Unlock()
}

// driverRowRank is a name's place in driverRowOrder; other names come after.
func driverRowRank(name string) int {
	for i, n := range driverRowOrder {
		if strings.EqualFold(n, name) {
			return i
		}
	}
	return len(driverRowOrder)
}

// driverTableStatus is the row for a subsystem nobody registered: what the
// table says this model has, without a live check.
func driverTableStatus(name string, hw deviceHW) driverStatus {
	has := func(on bool, what string) driverStatus {
		if !on {
			return driverStatus{State: driverNotUsed, Detail: "no " + what + " on this model"}
		}
		return driverStatus{State: driverUnknown, Detail: "not checked"}
	}
	if !hw.Model.IsTrimui() {
		return driverStatus{State: driverUnknown, Detail: "not a TrimUI handheld"}
	}
	switch name {
	case driverRowFan:
		return has(hw.HasFan, "fan")
	case driverRowSticks:
		return has(hw.HasSticks, "analog sticks")
	case driverRowLEDs:
		return has(hw.HasLEDs, "RGB lights")
	case driverRowCPU:
		st := driverStatus{State: driverUnknown, Detail: "not checked"}
		if len(hw.Clusters) > 0 {
			st.Driver = hw.SoC + " · " + strconv.Itoa(driverCores(hw)) + "× " + hw.Clusters[0].Core
		}
		return st
	case driverRowWiFi:
		return driverStatus{State: driverUnknown, Driver: hw.WiFiChip, Detail: "not checked"}
	case driverRowBluetooth:
		return driverStatus{State: driverUnknown, Driver: hw.BTChip, Detail: "not checked"}
	}
	return driverStatus{State: driverUnknown, Detail: "not checked"}
}

// driverCores is the table's core count.
func driverCores(hw deviceHW) int {
	n := 0
	for _, c := range hw.Clusters {
		n += c.Cores
	}
	return n
}

// driverModelStatus: the model and how it was named.
func driverModelStatus() driverStatus {
	model, source, id := deviceIdentityReport()
	st := driverStatus{Driver: deviceDisplayName(model)}
	switch {
	case model == DeviceDevBuild:
		st.State = driverNotUsed
		st.Detail = "development build"
	case model == DeviceUnknown:
		st.State = driverMissing
		st.Detail = "not identified" + identitySummary(id)
	default:
		st.State = driverOK
		st.Detail = "from " + setOr(source, "override")
	}
	if id.SoC != "" {
		st.Node = id.SoC + " chip (" + id.SoCWhy + ")"
	}
	return st
}

// driverDisplayStatus: the display and GPU nodes, and SDL's backend.
func driverDisplayStatus(videoDriver string) driverStatus {
	if runtime.GOOS != "linux" {
		return driverStatus{State: driverOK, Driver: setOr(videoDriver, "SDL"), Detail: "desktop display"}
	}
	hw := deviceHardwareCurrent()
	var nodes []string
	for _, n := range []string{"/dev/dri/card0", "/dev/dri/renderD128", "/dev/fb0", "/dev/pvr_sync"} {
		if pathExists(n) {
			nodes = append(nodes, n)
		}
	}
	if m := deviceMaliNode(); m != "" {
		nodes = append(nodes, m)
	}
	st := driverStatus{Node: strings.Join(nodes, ", ")}
	driver := hw.GPU
	switch hw.GPUStack {
	case gpuStackPowerVRFbdev:
		driver += " · fbdev EGL"
	case gpuStackMaliDRM:
		driver += " · DRM/KMS"
	}
	if videoDriver != "" {
		driver = strings.TrimPrefix(driver+" · SDL "+videoDriver, " · ")
	}
	st.Driver = driver
	_, _, id := deviceIdentityReport()
	if id.PanelW > 0 && id.PanelH > 0 {
		st.Detail = "panel " + panelLabel(id.PanelW, id.PanelH)
	}
	switch {
	case len(nodes) == 0:
		st.State = driverMissing
		st.Detail = "no display node found"
	case hw.GPUStack == gpuStackPowerVRFbdev && devicePowerVRNode() == "":
		st.State = driverProblem
		st.Detail = strings.TrimSpace(st.Detail + " · PowerVR render node not found")
	case hw.GPUStack != gpuStackPowerVRFbdev && hw.GPUNode != "" && !pathExists(hw.GPUNode):
		st.State = driverProblem
		st.Detail = strings.TrimSpace(st.Detail + " · expected " + hw.GPUNode)
	default:
		st.State = driverOK
	}
	return st
}

// driverBacklightStatus: the brightness backend the probe settled on.
func driverBacklightStatus() driverStatus {
	b := brightnessKnownBackend()
	st := driverStatus{Driver: brightnessBackendLabel(b)}
	hw := deviceHardwareCurrent()
	if hw.Backlight != "" {
		st.Detail = "this model tries " + hw.Backlight + " first"
	}
	switch b {
	case "":
		st.State = driverUnknown
		st.Driver = "not probed yet"
	case brightnessBackendDisp:
		st.State = driverOK
		st.Node = "/dev/disp"
	case brightnessBackendSysfs:
		st.State = driverOK
		st.Node = backlightRoot
	case brightnessBackendSoft:
		st.State = driverOK
		if hw.Model.IsTrimui() {
			st.State = driverMissing
			st.Detail = "no backlight answered; the picture is dimmed instead"
		}
	default:
		st.State = driverOK
	}
	return st
}

// driverKernelStatus: the running kernel against the model's series.
func driverKernelStatus() driverStatus {
	release := deviceKernelRelease()
	hw := deviceHardwareCurrent()
	if release == "" {
		if runtime.GOOS != "linux" {
			return driverStatus{State: driverNotUsed, Driver: runtime.GOOS}
		}
		return driverStatus{State: driverUnknown, Detail: "release not readable"}
	}
	st := driverStatus{State: driverOK, Driver: "Linux " + release}
	if hw.Kernel != "" {
		if strings.HasPrefix(release, hw.Kernel+".") || release == hw.Kernel {
			st.Detail = "the model's " + hw.Kernel + " BSP kernel"
		} else {
			st.Detail = "this model usually runs " + hw.Kernel
		}
	}
	return st
}

// driverFirmwareStatus: the model names the image carries.
func driverFirmwareStatus() driverStatus {
	_, _, id := deviceIdentityReport()
	names := firmwareEvidenceNames(id)
	switch {
	case runtime.GOOS != "linux":
		return driverStatus{State: driverNotUsed, Driver: runtime.GOOS}
	case !id.TrimUI:
		return driverStatus{State: driverUnknown, Detail: "not a TrimUI image"}
	case len(names) == 0:
		return driverStatus{State: driverOK, Driver: "TrimUI image", Detail: "names no model"}
	}
	return driverStatus{State: driverOK, Driver: "TrimUI image", Detail: strings.Join(names, ", ")}
}

// driverVideoOutStatus: external display. Only the Brick Pro has one (USB-C
// DP alt mode); JukaHub does not drive it, the firmware does. The row says
// what TV mode's probe reads now (tvmode.go): a display connected, nothing on
// the status files found, or no such file at all.
func driverVideoOutStatus() driverStatus {
	hw := deviceHardwareCurrent()
	if hw.VideoOut == "" {
		if hw.Model.IsTrimui() {
			return driverStatus{State: driverNotUsed, Detail: "no video out on this model"}
		}
		return driverStatus{State: driverUnknown, Detail: "not a TrimUI handheld"}
	}
	return tvModeDriverStatus(hw.VideoOut)
}
