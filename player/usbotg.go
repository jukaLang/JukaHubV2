package main

// usbotg.go — what is on the USB-C port, and what to do with it.
//
// Firmwares built on ArkOS ship a handful of OTG tools: see what is plugged in,
// flip the port between host and device, mount a stick and pull games off it.
// The TrimUI handhelds have the port (the Smart Pro S does host mode on its
// USB-C socket) but the stock firmware shows nothing about it, so this file
// does the work behind the USB & OTG screen (usbotg_ui.go):
//
//   - Devices: every USB device the kernel lists in /sys/bus/usb/devices,
//     classified from its interface classes (a pad, a keyboard, a USB-C DAC, a
//     wired network adapter, a hub). The parser reads through injected
//     functions, so the rules are testable without a kernel.
//   - Port mode: the standard usb_role switch when the kernel has one, else
//     the Allwinner BSP's USB manager (usbc0/otg_role, usb_host, usb_device).
//     Only files that exist are touched, and only the documented way: the role
//     switch takes "host"/"device", a writable otg_role takes "usb_host"/
//     "usb_device", and on BSPs whose otg_role is read-only *reading* usb_host
//     or usb_device is what switches (Allwinner's Tina Linux USB guide). Those
//     two trigger files are therefore never read by a scan, only by a switch.
//   - Drives: /sys/block/sd* and their partitions, with label and filesystem
//     from /dev/disk/by-label or blkid, mounted at /mnt/usb/<name> and ejected
//     with sync + unmount. The syscalls live in usbotg_linux.go.
//   - Import games: a Roms/ folder on a stick is copied into the card's Roms
//     folder system by system (BIOS/ into the BIOS folder) without ever
//     overwriting or deleting a file.
//
// All of it reads the disk, so none of it runs on the render thread: the
// screen asks for a scan and draws the last snapshot.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ── Devices ──────────────────────────────────────────────────────────────

// usbKind is what a USB device is for, as far as its descriptors say.
type usbKind int

const (
	usbKindOther usbKind = iota
	usbKindController
	usbKindKeyboard
	usbKindMouse
	usbKindStorage
	usbKindAudio
	usbKindNetwork
	usbKindWiFi
	usbKindBluetooth
	usbKindHub
)

// label is the plain name of a kind, for a row's pill.
func (k usbKind) label() string {
	switch k {
	case usbKindController:
		return "Controller"
	case usbKindKeyboard:
		return "Keyboard"
	case usbKindMouse:
		return "Mouse"
	case usbKindStorage:
		return "Storage"
	case usbKindAudio:
		return "Audio"
	case usbKindNetwork:
		return "Network"
	case usbKindWiFi:
		return "Wi-Fi"
	case usbKindBluetooth:
		return "Bluetooth"
	case usbKindHub:
		return "Hub"
	}
	return "Other"
}

// icon is the glyph of a kind; every name is in icondefs.go.
func (k usbKind) icon() string {
	switch k {
	case usbKindController:
		return "gamepad"
	case usbKindKeyboard:
		return "keyboard"
	case usbKindMouse:
		return "devices"
	case usbKindStorage:
		return "disk"
	case usbKindAudio:
		return "headphones"
	case usbKindNetwork:
		return "network"
	case usbKindWiFi:
		return "wifi"
	case usbKindBluetooth:
		return "bluetooth"
	case usbKindHub:
		return "plug"
	}
	return "hardware"
}

// usbIface is one interface of a device: class, subclass and protocol as the
// kernel prints them (two lower-case hex digits), the bound driver, and
// whether the input layer made a joystick node for it.
type usbIface struct {
	Class    string
	Sub      string
	Proto    string
	Driver   string
	Joystick bool
}

// usbDevice is one device on the bus. Name is the kernel's port path ("1-1",
// "1-1.2" behind a hub); Speed is in Mb/s as sysfs prints it.
type usbDevice struct {
	Name         string
	VendorID     string
	ProductID    string
	Manufacturer string
	Product      string
	Speed        string
	Class        string
	Fixed        bool
	Ifaces       []usbIface
	Kind         usbKind
}

// title is what a row calls the device: its own product string, else the
// kind and the IDs, which is all a device without strings tells.
func (d usbDevice) title() string {
	if d.Product != "" {
		return d.Product
	}
	return fmt.Sprintf("USB %s (%s:%s)", strings.ToLower(d.Kind.label()), d.VendorID, d.ProductID)
}

// usbSysfs is the part of sysfs the device parser reads, as functions so a
// test can hand it a map instead of a kernel. Paths are relative to
// /sys/bus/usb/devices; link answers the base name of a symlink's target.
type usbSysfs struct {
	read func(rel string) (string, bool)
	list func(rel string) []string
	link func(rel string) string
}

// usbSysfsAt reads the real tree under root.
func usbSysfsAt(root string) usbSysfs {
	return usbSysfs{
		read: func(rel string) (string, bool) {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				return "", false
			}
			return strings.TrimSpace(string(data)), true
		},
		list: func(rel string) []string {
			entries, err := os.ReadDir(filepath.Join(root, rel))
			if err != nil {
				return nil
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return names
		},
		link: func(rel string) string {
			target, err := os.Readlink(filepath.Join(root, rel))
			if err != nil {
				return ""
			}
			return filepath.Base(target)
		},
	}
}

// parseUSBDevices lists the devices in a usb devices directory. Interfaces
// ("1-1:1.0") are read as part of their device; root hubs ("usb1") are the
// host controllers themselves and are left out.
func parseUSBDevices(fs usbSysfs) []usbDevice {
	names := fs.list("")
	sort.Strings(names)
	var out []usbDevice
	for _, name := range names {
		if strings.Contains(name, ":") || strings.HasPrefix(name, "usb") {
			continue
		}
		var ifaces []string
		for _, other := range names {
			if strings.HasPrefix(other, name+":") {
				ifaces = append(ifaces, other)
			}
		}
		if d, ok := parseUSBDevice(fs, name, ifaces); ok {
			out = append(out, d)
		}
	}
	return out
}

// parseUSBDevice reads one device and its interfaces. A directory without an
// idVendor is not a device (it went away mid-read) and is skipped.
func parseUSBDevice(fs usbSysfs, name string, ifaceNames []string) (usbDevice, bool) {
	vendor, ok := fs.read(name + "/idVendor")
	if !ok || vendor == "" {
		return usbDevice{}, false
	}
	get := func(rel string) string {
		v, _ := fs.read(rel)
		return strings.TrimSpace(v)
	}
	d := usbDevice{
		Name:         name,
		VendorID:     strings.ToLower(vendor),
		ProductID:    strings.ToLower(get(name + "/idProduct")),
		Manufacturer: get(name + "/manufacturer"),
		Product:      get(name + "/product"),
		Speed:        get(name + "/speed"),
		Class:        strings.ToLower(get(name + "/bDeviceClass")),
		Fixed:        get(name+"/removable") == "fixed",
	}
	for _, in := range ifaceNames {
		iface := usbIface{
			Class:  strings.ToLower(get(in + "/bInterfaceClass")),
			Sub:    strings.ToLower(get(in + "/bInterfaceSubClass")),
			Proto:  strings.ToLower(get(in + "/bInterfaceProtocol")),
			Driver: fs.link(in + "/driver"),
		}
		if iface.Class == "03" {
			iface.Joystick = usbHIDHasJoystick(fs, in)
		}
		d.Ifaces = append(d.Ifaces, iface)
	}
	d.Kind = usbClassify(d.Class, d.Ifaces)
	// A Sony pad also has its headset jack's audio interfaces; on a kernel
	// without joydev nothing else tells it from a DAC, so the IDs settle it
	// (DualShock 4 v1/v2, DualSense, DualSense Edge).
	if d.Kind == usbKindAudio && d.VendorID == "054c" {
		switch d.ProductID {
		case "05c4", "09cc", "0ce6", "0df2":
			d.Kind = usbKindController
		}
	}
	return d, true
}

// usbHIDHasJoystick looks for a jsN node under a HID interface
// (<iface>/<hid device>/input/inputN/jsN): the kernel's own verdict that the
// report descriptor has sticks and buttons, which a class code cannot say.
func usbHIDHasJoystick(fs usbSysfs, iface string) bool {
	for _, hid := range fs.list(iface) {
		// HID devices are named bus:vendor:product.instance (0003:054C:0CE6.0001).
		if strings.Count(hid, ":") != 2 {
			continue
		}
		base := iface + "/" + hid + "/input"
		for _, input := range fs.list(base) {
			if !strings.HasPrefix(input, "input") {
				continue
			}
			for _, node := range fs.list(base + "/" + input) {
				if strings.HasPrefix(node, "js") {
					return true
				}
			}
		}
	}
	return false
}

// usbNetDrivers are the kernel drivers of wired USB network adapters; most
// Realtek and ASIX adapters use a vendor class, so the class code alone misses
// them.
var usbNetDrivers = []string{"cdc_ether", "cdc_ncm", "cdc_mbim", "rndis_host", "r8152", "r8153_ecm", "ax88179_178a", "asix", "smsc95xx", "lan78xx", "aqc111", "qmi_wwan"}

// usbWiFiDriverParts are fragments of USB Wi-Fi driver names (in-tree and the
// vendor modules handheld kernels carry).
var usbWiFiDriverParts = []string{"rtl8", "rtw", "8188", "8192", "8723", "8821", "8822", "88x2", "aicwf", "aic8800", "mt76", "mt7601", "ath9k", "carl9170", "rt2800", "rt73", "zd1211", "ssv", "wlan"}

// usbClassify decides what a device is. The order matters: a controller with
// a headset jack (a DualSense) has audio interfaces, a USB-C headset has a HID
// interface for its volume keys, and a keyboard often adds a generic HID
// interface for media keys — the most specific evidence wins.
func usbClassify(devClass string, ifaces []usbIface) usbKind {
	has := func(match func(i usbIface) bool) bool {
		for _, i := range ifaces {
			if match(i) {
				return true
			}
		}
		return false
	}
	driverIn := func(i usbIface, names []string) bool {
		for _, n := range names {
			if i.Driver == n {
				return true
			}
		}
		return false
	}
	switch {
	// XInput (Xbox 360 and clones) and GIP (Xbox One and later) pads use a
	// vendor class with these well-known subclass/protocol pairs.
	case has(func(i usbIface) bool {
		return i.Class == "ff" && (i.Sub == "5d" || (i.Sub == "47" && i.Proto == "d0"))
	}):
		return usbKindController
	case has(func(i usbIface) bool { return i.Class == "03" && i.Joystick }):
		return usbKindController
	case has(func(i usbIface) bool { return i.Class == "01" }):
		return usbKindAudio
	case has(func(i usbIface) bool { return i.Class == "08" }):
		return usbKindStorage
	case has(func(i usbIface) bool {
		switch {
		case driverIn(i, usbNetDrivers):
			return true
		case i.Class == "02" && (i.Sub == "06" || i.Sub == "0d" || i.Sub == "0e"):
			return true
		case i.Class == "02" && i.Sub == "02" && i.Proto == "ff":
			return true
		case i.Class == "e0" && i.Sub == "01" && i.Proto == "03":
			return true
		case i.Class == "ef" && i.Sub == "04" && i.Proto == "01":
			return true
		}
		return false
	}):
		return usbKindNetwork
	case has(func(i usbIface) bool {
		return (i.Class == "e0" && i.Sub == "01" && i.Proto == "01") || strings.Contains(i.Driver, "btusb")
	}):
		return usbKindBluetooth
	case has(func(i usbIface) bool {
		for _, part := range usbWiFiDriverParts {
			if i.Driver != "" && strings.Contains(i.Driver, part) {
				return true
			}
		}
		return false
	}):
		return usbKindWiFi
	case has(func(i usbIface) bool { return i.Class == "03" && i.Sub == "01" && i.Proto == "01" }):
		return usbKindKeyboard
	case has(func(i usbIface) bool { return i.Class == "03" && i.Sub == "01" && i.Proto == "02" }):
		return usbKindMouse
	case has(func(i usbIface) bool { return i.Class == "03" }):
		// A HID device that is neither a keyboard nor a mouse is, on a
		// handheld, nearly always a pad whose joystick node is not there
		// (joydev not loaded).
		return usbKindController
	case devClass == "09" || has(func(i usbIface) bool { return i.Class == "09" }):
		return usbKindHub
	}
	return usbKindOther
}

// usbSpeedLabel turns sysfs's Mb/s into the name people know.
func usbSpeedLabel(mbps string) string {
	switch strings.TrimSpace(mbps) {
	case "1.5", "12":
		return "USB 1.1"
	case "480":
		return "USB 2.0"
	case "5000":
		return "USB 3 (5 Gb/s)"
	case "10000":
		return "USB 3 (10 Gb/s)"
	case "20000":
		return "USB 3 (20 Gb/s)"
	}
	return ""
}

// ── Port mode ────────────────────────────────────────────────────────────

// Port mechanisms, in the order they are looked for.
const (
	usbPortRoleClass  = "role"
	usbPortSunxiRead  = "sunxi-read"
	usbPortSunxiWrite = "sunxi-write"
	usbPortSunxiFixed = "sunxi-fixed"
)

// usbPortState is the USB-C port's role and how (whether) it can be changed.
// Mode is "host", "device" or "none" ("" when no node says).
type usbPortState struct {
	Found      bool
	Mode       string
	Switchable bool
	Source     string
	kind       string
	rolePath   string
	hostPath   string
	devicePath string
}

// usbParseRole folds the different spellings to host/device/none: the role
// class prints "host"/"device"/"none", the Allwinner manager "usb_host"/
// "usb_device"/"null" (some versions with a prefix).
func usbParseRole(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case s == "":
		return ""
	case strings.Contains(s, "host"):
		return "host"
	case strings.Contains(s, "device") || strings.Contains(s, "peripheral") || strings.Contains(s, "gadget"):
		return "device"
	}
	return "none"
}

// usbWritable reports a file the kernel lets someone write.
func usbWritable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().Perm()&0o222 != 0
}

// usbSunxiDirs are where the Allwinner BSP puts its USB manager node. The
// platform bus link is stable; the soc node is "soc" or "soc@3000000"
// depending on the kernel.
func usbSunxiDirs() []string {
	dirs := []string{"/sys/bus/platform/devices/usbc0"}
	if globbed, err := filepath.Glob("/sys/devices/platform/soc*/usbc0"); err == nil {
		sort.Strings(globbed)
		dirs = append(dirs, globbed...)
	}
	dirs = append(dirs, "/sys/devices/platform/soc/usbc0")
	return dirs
}

// usbDetectPort finds the port's role switch. It only reads role files and
// stats the rest: on the Allwinner BSP reading usb_host or usb_device is the
// switch itself.
func usbDetectPort() usbPortState {
	if runtime.GOOS != "linux" {
		return usbPortState{}
	}
	// A role switch the kernel keeps read-only (no userspace control) still
	// says the mode; the Allwinner manager may be the one that can change it.
	var fixed usbPortState
	if roles, err := filepath.Glob("/sys/class/usb_role/*/role"); err == nil && len(roles) > 0 {
		sort.Strings(roles)
		for _, p := range roles {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			st := usbPortState{
				Found:      true,
				Mode:       usbParseRole(string(raw)),
				Switchable: usbWritable(p),
				Source:     "USB role switch " + filepath.Base(filepath.Dir(p)),
				kind:       usbPortRoleClass,
				rolePath:   p,
			}
			if st.Switchable {
				return st
			}
			if !fixed.Found {
				fixed = st
			}
		}
	}
	for _, dir := range usbSunxiDirs() {
		role := filepath.Join(dir, "otg_role")
		raw, err := os.ReadFile(role)
		if err != nil {
			continue
		}
		st := usbPortState{
			Found:    true,
			Mode:     usbParseRole(string(raw)),
			Source:   "Allwinner USB manager",
			kind:     usbPortSunxiFixed,
			rolePath: role,
		}
		host, device := filepath.Join(dir, "usb_host"), filepath.Join(dir, "usb_device")
		switch {
		case fileExists(host) && fileExists(device):
			st.kind, st.hostPath, st.devicePath, st.Switchable = usbPortSunxiRead, host, device, true
		case usbWritable(role):
			st.kind, st.Switchable = usbPortSunxiWrite, true
		}
		if st.Switchable || !fixed.Found {
			return st
		}
		break
	}
	return fixed
}

// usbApplyPortMode switches the port to mode ("host" or "device") through the
// node usbDetectPort found, and nothing else.
func usbApplyPortMode(st usbPortState, mode string) error {
	if mode != "host" && mode != "device" {
		return fmt.Errorf("unknown port mode %q", mode)
	}
	if !st.Found || !st.Switchable {
		return errors.New("the firmware does not let apps change the port")
	}
	switch st.kind {
	case usbPortRoleClass:
		return os.WriteFile(st.rolePath, []byte(mode), 0o644)
	case usbPortSunxiWrite:
		return os.WriteFile(st.rolePath, []byte("usb_"+mode), 0o644)
	case usbPortSunxiRead:
		// Reading the file is the switch (Allwinner's documented
		// "cat usb_host"); the content is only a confirmation line.
		path := st.hostPath
		if mode == "device" {
			path = st.devicePath
		}
		_, err := os.ReadFile(path)
		return err
	}
	return errors.New("the firmware does not let apps change the port")
}

// usbModeLabel is the pill text for a mode.
func usbModeLabel(mode string) string {
	switch mode {
	case "host":
		return "Host"
	case "device":
		return "Device"
	case "none":
		return "Off"
	}
	return "Unknown"
}

// ── Settings ─────────────────────────────────────────────────────────────

// usbOtgSettings is the user's choice: the port mode to restore at startup
// ("" or "auto" leaves the firmware in charge) and whether new drives mount
// by themselves.
type usbOtgSettings struct {
	Mode      string `json:"mode,omitempty"`
	AutoMount bool   `json:"autoMount,omitempty"`
}

var usbOtgSettingsState struct {
	once     sync.Once
	mu       sync.Mutex
	saveMu   sync.Mutex
	settings usbOtgSettings
}

// usbOtgAutoMount mirrors the setting for the watcher goroutine.
var usbOtgAutoMount atomic.Bool

func usbOtgSettingsPath() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "usb-otg.json")
	}
	return ""
}

// usbOtgLoadedSettings returns the saved settings, reading the file once.
func usbOtgLoadedSettings() usbOtgSettings {
	usbOtgSettingsState.once.Do(func() {
		var s usbOtgSettings
		if path := usbOtgSettingsPath(); path != "" {
			if _, err := readJSONWithBackup(path, &s); err != nil && !errors.Is(err, os.ErrNotExist) {
				logWarn("usbotg", "settings unreadable, using defaults: %v", err)
				s = usbOtgSettings{}
			}
		}
		if s.Mode != "host" && s.Mode != "device" {
			s.Mode = "auto"
		}
		usbOtgSettingsState.mu.Lock()
		usbOtgSettingsState.settings = s
		usbOtgSettingsState.mu.Unlock()
		usbOtgAutoMount.Store(s.AutoMount)
	})
	usbOtgSettingsState.mu.Lock()
	defer usbOtgSettingsState.mu.Unlock()
	return usbOtgSettingsState.settings
}

// usbOtgSaveSettings changes the settings and writes them off the caller's
// thread (the file lives on the SD card).
func usbOtgSaveSettings(change func(s *usbOtgSettings)) {
	usbOtgLoadedSettings()
	usbOtgSettingsState.mu.Lock()
	change(&usbOtgSettingsState.settings)
	s := usbOtgSettingsState.settings
	usbOtgSettingsState.mu.Unlock()
	usbOtgAutoMount.Store(s.AutoMount)
	path := usbOtgSettingsPath()
	if path == "" {
		return
	}
	go func() {
		// One write at a time (AtomicWrite shares one temp name), each with
		// the newest settings, so two quick changes cannot land out of order.
		usbOtgSettingsState.saveMu.Lock()
		defer usbOtgSettingsState.saveMu.Unlock()
		usbOtgSettingsState.mu.Lock()
		s := usbOtgSettingsState.settings
		usbOtgSettingsState.mu.Unlock()
		data, err := json.Marshal(s)
		if err == nil {
			err = AtomicWrite(path, data, 0o644)
		}
		if err != nil {
			logWarn("usbotg", "could not save settings: %v", err)
		}
	}()
}

// usbOtgApplySavedMode restores a port mode the user chose (Host or Device)
// and starts the drive watcher when automatic mounting is on. Startup calls
// it; it returns at once and does nothing off Linux or with Auto.
func usbOtgApplySavedMode() {
	if runtime.GOOS != "linux" {
		return
	}
	go func() {
		s := usbOtgLoadedSettings()
		if s.AutoMount {
			usbOtgStartWatcher()
		}
		if s.Mode != "host" && s.Mode != "device" {
			return
		}
		// The BSP's USB manager settles its own role a few seconds after
		// boot and can undo an earlier switch, so the mode is checked again
		// once after a pause.
		for attempt := 0; attempt < 2; attempt++ {
			st := usbDetectPort()
			if !st.Switchable {
				return
			}
			if st.Mode != s.Mode {
				if err := usbApplyPortMode(st, s.Mode); err != nil {
					logWarn("usbotg", "could not restore port mode %s: %v", s.Mode, err)
					return
				}
				logInfo("usbotg", "port mode restored to %s via %s", s.Mode, st.Source)
			}
			time.Sleep(4 * time.Second)
		}
		usbOtgRefresh()
	}()
}

// ── Drives ───────────────────────────────────────────────────────────────

// usbOtgMountRoot is where this screen mounts drives.
const usbOtgMountRoot = "/mnt/usb"

// usbVolume is one mountable thing on a USB disk: a partition, or the whole
// disk when it has no partition table (a "superfloppy" stick).
type usbVolume struct {
	Name      string
	Disk      string
	Size      int64
	Vendor    string
	Model     string
	Label     string
	FSType    string
	MountedAt string
	RomsDir   string
	BIOSDir   string
}

// usbValidVolumeName accepts sdX and sdXN only, so a name from sysfs can never
// make a path that leaves /dev or /mnt/usb.
func usbValidVolumeName(name string) bool {
	if len(name) < 3 || !strings.HasPrefix(name, "sd") {
		return false
	}
	i := 2
	for i < len(name) && name[i] >= 'a' && name[i] <= 'z' {
		i++
	}
	if i == 2 {
		return false
	}
	for ; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

// usbMount is one line of /proc/mounts.
type usbMount struct {
	Source string
	Point  string
	Type   string
}

// usbParseMounts reads /proc/mounts text (octal escapes undone).
func usbParseMounts(text string) []usbMount {
	var out []usbMount
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		out = append(out, usbMount{Source: unescapeMountPath(f[0]), Point: unescapeMountPath(f[1]), Type: f[2]})
	}
	return out
}

// usbReadMounts maps a block device name ("sda1") to its mounts, whoever made
// them (this screen, the firmware or another daemon). Sources given through a
// /dev/disk/by-* link are resolved.
func usbReadMounts() map[string][]usbMount {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	out := map[string][]usbMount{}
	for _, m := range usbParseMounts(string(data)) {
		if !strings.HasPrefix(m.Source, "/dev/") {
			continue
		}
		src := m.Source
		if !strings.HasPrefix(src, "/dev/sd") {
			resolved, err := filepath.EvalSymlinks(src)
			if err != nil {
				continue
			}
			src = resolved
		}
		name := filepath.Base(src)
		if usbValidVolumeName(name) {
			out[name] = append(out[name], m)
		}
	}
	return out
}

// usbParseBlkid reads LABEL and TYPE from one line of blkid output; util-linux
// and busybox both print `/dev/sda1: LABEL="X" UUID="…" TYPE="vfat"`.
func usbParseBlkid(out string) (label, fstype string) {
	s := strings.TrimSpace(out)
	if i := strings.Index(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	for {
		eq := strings.Index(s, "=\"")
		if eq < 0 {
			return label, fstype
		}
		key := strings.TrimSpace(s[:eq])
		if sp := strings.LastIndexByte(key, ' '); sp >= 0 {
			key = key[sp+1:]
		}
		rest := s[eq+2:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			return label, fstype
		}
		switch key {
		case "LABEL":
			label = rest[:end]
		case "TYPE":
			fstype = rest[:end]
		}
		s = rest[end+1:]
	}
}

// usbDecodeLabel undoes udev's \xNN escapes in /dev/disk/by-label names.
func usbDecodeLabel(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// usbBlkidCache keeps blkid's answer per volume and size, so a drive is
// probed once per plug-in rather than on every refresh.
var usbBlkidCache struct {
	mu   sync.Mutex
	info map[string][2]string
}

// usbProbeVolume asks blkid for a volume's label and filesystem. blkid is
// optional: a firmware without it leaves both empty.
func usbProbeVolume(name string, size int64) (label, fstype string) {
	key := name + "/" + strconv.FormatInt(size, 10)
	usbBlkidCache.mu.Lock()
	if v, ok := usbBlkidCache.info[key]; ok {
		usbBlkidCache.mu.Unlock()
		return v[0], v[1]
	}
	usbBlkidCache.mu.Unlock()
	bin, err := exec.LookPath("blkid")
	if err != nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := hiddenCommandContext(ctx, bin, "/dev/"+name).Output()
	if err == nil {
		label, fstype = usbParseBlkid(firstLine(string(out)))
	}
	if ctx.Err() == nil {
		usbBlkidCache.mu.Lock()
		if usbBlkidCache.info == nil {
			usbBlkidCache.info = map[string][2]string{}
		}
		usbBlkidCache.info[key] = [2]string{label, fstype}
		usbBlkidCache.mu.Unlock()
	}
	return label, fstype
}

// usbReadSysInt reads a decimal sysfs file.
func usbReadSysInt(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return v
}

func usbReadSysString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// usbListVolumes lists the volumes of the sd* disks: name → size in bytes. A
// disk of size 0 is a card reader with no card.
func usbListVolumes() (names []string, disks map[string]string, sizes map[string]int64) {
	disks, sizes = map[string]string{}, map[string]int64{}
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, disks, sizes
	}
	for _, e := range entries {
		disk := e.Name()
		if !usbValidVolumeName(disk) {
			continue
		}
		base := filepath.Join("/sys/block", disk)
		// Only disks on the USB bus: a board whose own storage is SCSI/UFS
		// (also sd*) must never have its system disk mounted or ejected here.
		// The /sys/block entry links into the device path; an older kernel
		// with real directories there cannot say and is trusted.
		if link, err := os.Readlink(base); err == nil && !strings.Contains(link, "/usb") {
			continue
		}
		size := usbReadSysInt(filepath.Join(base, "size")) * 512
		if size <= 0 {
			continue
		}
		parts := 0
		if subs, err := os.ReadDir(base); err == nil {
			for _, s := range subs {
				part := s.Name()
				if !strings.HasPrefix(part, disk) || !usbValidVolumeName(part) || !fileExists(filepath.Join(base, part, "partition")) {
					continue
				}
				psize := usbReadSysInt(filepath.Join(base, part, "size")) * 512
				if psize <= 0 {
					continue
				}
				names = append(names, part)
				disks[part], sizes[part] = disk, psize
				parts++
			}
		}
		if parts == 0 {
			names = append(names, disk)
			disks[disk], sizes[disk] = disk, size
		}
	}
	sort.Strings(names)
	return names, disks, sizes
}

// usbScanVolumes describes every volume: size, maker, label, filesystem, where
// it is mounted and whether it carries Roms/ or BIOS/ at its root.
func usbScanVolumes(mounts map[string][]usbMount) []usbVolume {
	names, disks, sizes := usbListVolumes()
	if len(names) == 0 {
		return nil
	}
	labels := map[string]string{}
	if entries, err := os.ReadDir("/dev/disk/by-label"); err == nil {
		for _, e := range entries {
			if target, err := os.Readlink(filepath.Join("/dev/disk/by-label", e.Name())); err == nil {
				labels[filepath.Base(target)] = usbDecodeLabel(e.Name())
			}
		}
	}
	out := make([]usbVolume, 0, len(names))
	for _, name := range names {
		disk := disks[name]
		v := usbVolume{
			Name:   name,
			Disk:   disk,
			Size:   sizes[name],
			Vendor: usbReadSysString(filepath.Join("/sys/block", disk, "device", "vendor")),
			Model:  usbReadSysString(filepath.Join("/sys/block", disk, "device", "model")),
			Label:  labels[name],
		}
		if ms := mounts[name]; len(ms) > 0 {
			v.MountedAt, v.FSType = ms[0].Point, ms[0].Type
			for _, m := range ms {
				// Prefer this screen's own mount when there are two.
				if strings.HasPrefix(m.Point, usbOtgMountRoot+"/") {
					v.MountedAt, v.FSType = m.Point, m.Type
				}
			}
		}
		if v.Label == "" || v.FSType == "" {
			label, fstype := usbProbeVolume(name, v.Size)
			if v.Label == "" {
				v.Label = label
			}
			if v.FSType == "" {
				v.FSType = fstype
			}
		}
		if v.MountedAt != "" {
			v.RomsDir, v.BIOSDir = usbFindImportDirs(v.MountedAt)
		}
		out = append(out, v)
	}
	return out
}

// usbFindImportDirs finds Roms/ and BIOS/ at a drive's root, in any case.
func usbFindImportDirs(root string) (roms, bios string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		switch strings.ToLower(e.Name()) {
		case "roms":
			roms = filepath.Join(root, e.Name())
		case "bios":
			bios = filepath.Join(root, e.Name())
		}
	}
	return roms, bios
}

// usbFSName is the filesystem as people know it.
func usbFSName(fstype string) string {
	switch strings.ToLower(fstype) {
	case "vfat", "fat", "msdos":
		return "FAT32"
	case "exfat":
		return "exFAT"
	case "ntfs", "ntfs3", "fuseblk":
		return "NTFS"
	case "":
		return ""
	}
	return fstype
}

// usbMountOrder is the filesystems a mount tries: blkid's answer first, then
// the usual stick formats.
func usbMountOrder(hint string) []string {
	all := []string{"vfat", "exfat", "ext4", "ntfs3", "ntfs"}
	var first []string
	switch strings.ToLower(hint) {
	case "vfat", "fat", "msdos":
		first = []string{"vfat"}
	case "exfat":
		first = []string{"exfat"}
	case "ext4", "ext3", "ext2":
		first = []string{"ext4"}
	case "ntfs", "ntfs3":
		first = []string{"ntfs3", "ntfs"}
	}
	out := append([]string(nil), first...)
	for _, fs := range all {
		dup := false
		for _, f := range first {
			if f == fs {
				dup = true
			}
		}
		if !dup {
			out = append(out, fs)
		}
	}
	return out
}

// usbMountOptions are the data options per filesystem: UTF-8 names and root
// ownership on the FAT family (which has no owners), nothing elsewhere.
func usbMountOptions(fstype string) string {
	switch fstype {
	case "vfat":
		return "utf8,uid=0,gid=0"
	case "exfat", "ntfs3":
		return "iocharset=utf8,uid=0,gid=0"
	}
	return ""
}

// usbOtgMountVolume mounts a volume at /mnt/usb/<name> and returns where it is
// mounted (an existing mount, from anyone, is reused).
func usbOtgMountVolume(name, fsHint string) (string, error) {
	if !usbValidVolumeName(name) {
		return "", fmt.Errorf("%q is not a USB drive", name)
	}
	if ms := usbReadMounts()[name]; len(ms) > 0 {
		return ms[0].Point, nil
	}
	dev := "/dev/" + name
	if !fileExists(dev) {
		return "", fmt.Errorf("the system has no %s yet — unplug the drive and plug it in again", dev)
	}
	target := filepath.Join(usbOtgMountRoot, name)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	var lastErr error
	for _, fstype := range usbMountOrder(fsHint) {
		err := usbTryMount(dev, target, fstype)
		if err == nil {
			return target, nil
		}
		lastErr = err
		if kind := usbOtgErrKind(err); kind != "wrongfs" && kind != "nofs" {
			break
		}
	}
	// Only the empty folder this call made; Remove refuses a folder with
	// anything in it.
	_ = os.Remove(target)
	switch usbOtgErrKind(lastErr) {
	case "wrongfs", "nofs":
		return "", errors.New("the drive's format is not one this system can read (FAT32, exFAT, ext4 or NTFS)")
	case "busy":
		return "", errors.New("the drive is busy — another program may be mounting it")
	}
	return "", lastErr
}

// usbTryMount mounts one filesystem type: with its options, then without (an
// older driver refuses an option it does not know with the same error as a
// wrong filesystem), and read-only when the drive is write-protected.
func usbTryMount(dev, target, fstype string) error {
	opts := usbMountOptions(fstype)
	err := usbOtgMountFS(dev, target, fstype, opts, false)
	if err != nil && opts != "" && usbOtgErrKind(err) == "wrongfs" {
		err = usbOtgMountFS(dev, target, fstype, "", false)
	}
	if err != nil && usbOtgErrKind(err) == "readonly" {
		err = usbOtgMountFS(dev, target, fstype, opts, true)
	}
	return err
}

// usbOtgEjectDisk syncs and unmounts every mounted volume of disk. lazy is
// true when one was still in use and had to be detached.
func usbOtgEjectDisk(disk string) (lazy bool, err error) {
	usbOtgSync()
	for name, ms := range usbReadMounts() {
		if name != disk && !(strings.HasPrefix(name, disk) && len(name) > len(disk) && name[len(disk)] >= '0' && name[len(disk)] <= '9') {
			continue
		}
		// Unmount the newest mount first: /proc/mounts lists in mount order.
		for i := len(ms) - 1; i >= 0; i-- {
			point := ms[i].Point
			uerr := usbOtgUnmount(point, false)
			if uerr != nil && usbOtgErrKind(uerr) == "busy" {
				uerr = usbOtgUnmount(point, true)
				if uerr == nil {
					lazy = true
				}
			}
			if uerr != nil {
				return lazy, fmt.Errorf("could not unmount %s: %w", point, uerr)
			}
			if filepath.Dir(point) == usbOtgMountRoot {
				_ = os.Remove(point)
			}
		}
	}
	usbOtgSync()
	return lazy, nil
}

// usbOtgDropStaleMounts detaches this screen's mounts (/mnt/usb/<name>) whose
// drive was pulled without Eject: the kernel keeps such a mount until someone
// unmounts it, Files would keep listing a dead location, and the stick comes
// back under a new name. It reports whether it changed anything.
func usbOtgDropStaleMounts(mounts map[string][]usbMount) bool {
	changed := false
	if len(mounts) == 0 || !dirExists("/sys/class/block") {
		// Without sysfs nothing can say a drive is gone.
		return false
	}
	for name, ms := range mounts {
		if fileExists(filepath.Join("/sys/class/block", name)) {
			continue
		}
		for i := len(ms) - 1; i >= 0; i-- {
			point := ms[i].Point
			if filepath.Dir(point) != usbOtgMountRoot {
				continue
			}
			if err := usbOtgUnmount(point, true); err != nil {
				logWarn("usbotg", "could not detach %s after its drive went away: %v", point, err)
				continue
			}
			_ = os.Remove(point)
			changed = true
		}
	}
	return changed
}

// ── Snapshot ─────────────────────────────────────────────────────────────

// usbOtgSnapshot is one scan: what the screen and the Tools tile show.
type usbOtgSnapshot struct {
	gen     uint64
	at      time.Time
	devices []usbDevice
	volumes []usbVolume
	port    usbPortState
}

var usbOtgState struct {
	mu       sync.Mutex
	snap     usbOtgSnapshot
	scanning bool
	again    bool
	tile     string
	watcher  sync.Once
}

// usbOtgScan reads everything once. Linux only; elsewhere it is empty.
func usbOtgScan() usbOtgSnapshot {
	snap := usbOtgSnapshot{at: time.Now()}
	if runtime.GOOS != "linux" {
		return snap
	}
	// The screen reads the settings once a snapshot exists; loading them
	// here keeps that read off the render thread.
	usbOtgLoadedSettings()
	snap.devices = parseUSBDevices(usbSysfsAt("/sys/bus/usb/devices"))
	mounts := usbReadMounts()
	if usbOtgDropStaleMounts(mounts) {
		mounts = usbReadMounts()
	}
	snap.volumes = usbScanVolumes(mounts)
	snap.port = usbDetectPort()
	return snap
}

// usbOtgRefresh starts a scan on a goroutine unless one is running.
func usbOtgRefresh() {
	if runtime.GOOS != "linux" {
		return
	}
	usbOtgState.mu.Lock()
	if usbOtgState.scanning {
		// The running scan may have read the mounts before a mount or an
		// eject finished; one more pass follows it.
		usbOtgState.again = true
		usbOtgState.mu.Unlock()
		return
	}
	usbOtgState.scanning = true
	usbOtgState.mu.Unlock()
	go func() {
		for {
			snap := usbOtgScan()
			usbOtgState.mu.Lock()
			snap.gen = usbOtgState.snap.gen + 1
			usbOtgState.snap = snap
			usbOtgState.tile = usbOtgTileText(snap)
			again := usbOtgState.again
			usbOtgState.again = false
			usbOtgState.scanning = again
			usbOtgState.mu.Unlock()
			if !again {
				return
			}
		}
	}()
}

// usbOtgCurrent is the last snapshot. Its slices are never changed after a
// scan publishes them, so sharing them is safe.
func usbOtgCurrent() usbOtgSnapshot {
	usbOtgState.mu.Lock()
	defer usbOtgState.mu.Unlock()
	return usbOtgState.snap
}

// usbOtgCountsUser counts what a person plugged in: hubs and devices the
// kernel marks as fixed (built into the handheld) are not "plugged in".
func usbOtgCountsUser(d usbDevice) bool {
	return d.Kind != usbKindHub && !d.Fixed
}

// usbOtgTileText is the Tools hub line for a snapshot.
func usbOtgTileText(snap usbOtgSnapshot) string {
	devices := 0
	for _, d := range snap.devices {
		// Wi-Fi and Bluetooth chips are left out too: handhelds often wire
		// their own to the internal USB bus without marking them fixed, and
		// the tile would never say "Nothing plugged in".
		if usbOtgCountsUser(d) && d.Kind != usbKindStorage && d.Kind != usbKindWiFi && d.Kind != usbKindBluetooth {
			devices++
		}
	}
	drives := map[string]bool{}
	for _, v := range snap.volumes {
		drives[v.Disk] = true
	}
	var parts []string
	if devices > 0 {
		parts = append(parts, pluralizeCount(devices, "device", "devices"))
	}
	if len(drives) > 0 {
		parts = append(parts, pluralizeCount(len(drives), "drive", "drives"))
	}
	if len(parts) == 0 {
		return "Nothing plugged in"
	}
	return strings.Join(parts, " · ")
}

// usbOtgTileValue is the Tools hub tile's live line. No I/O: it answers the
// cached text and, when that is older than a few seconds, asks for a scan
// on a goroutine.
func usbOtgTileValue() string {
	if runtime.GOOS != "linux" {
		return "On the handheld"
	}
	usbOtgState.mu.Lock()
	tile, at, scanning := usbOtgState.tile, usbOtgState.snap.at, usbOtgState.scanning
	usbOtgState.mu.Unlock()
	if !scanning && time.Since(at) > 5*time.Second {
		usbOtgRefresh()
	}
	if tile == "" {
		return "Looking…"
	}
	return tile
}

// ── Watcher ──────────────────────────────────────────────────────────────

// usbOtgStartWatcher starts the automatic-mount loop once. It polls /sys/block
// every 3 seconds while JukaHub runs and mounts a new volume when the setting
// is on; with it off the loop only keeps its list current.
func usbOtgStartWatcher() {
	if runtime.GOOS != "linux" {
		return
	}
	usbOtgState.watcher.Do(func() { go usbOtgWatchLoop() })
}

func usbOtgWatchLoop() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	// seen is nil until the first pass: drives already in at startup count
	// as new, so a stick left in across a restart is mounted too.
	var seen map[string]bool
	pending := map[string]bool{}
	for range ticker.C {
		if exitRequested.Load() {
			return
		}
		if gameScreenSuspended() {
			continue
		}
		names, _, _ := usbListVolumes()
		now := make(map[string]bool, len(names))
		for _, n := range names {
			now[n] = true
		}
		// A drive pulled without Eject leaves its mount behind.
		for n := range seen {
			if !now[n] {
				if usbOtgDropStaleMounts(usbReadMounts()) {
					usbOtgRefresh()
				}
				break
			}
		}
		if !usbOtgAutoMount.Load() {
			seen = now
			pending = map[string]bool{}
			continue
		}
		// A volume new on this pass waits one more pass: the firmware or a
		// hotplug daemon may mount it first, and two mounts of one stick
		// help nobody.
		var ready []string
		for n := range pending {
			if now[n] {
				ready = append(ready, n)
			}
		}
		pending = map[string]bool{}
		for _, n := range names {
			if seen == nil || !seen[n] {
				pending[n] = true
			}
		}
		seen = now
		if len(ready) == 0 {
			continue
		}
		sort.Strings(ready)
		mounts := usbReadMounts()
		mounted := 0
		for _, n := range ready {
			if len(mounts[n]) > 0 {
				continue
			}
			if _, err := usbOtgMountVolume(n, ""); err != nil {
				logWarn("usbotg", "automatic mount of %s failed: %v", n, err)
				ShowToast("USB drive "+n+": "+firstLine(err.Error()), ToastKindWarning)
				continue
			}
			mounted++
		}
		if mounted > 0 {
			ShowToast(pluralizeCount(mounted, "USB drive", "USB drives")+" mounted — see Tools → USB & OTG", ToastKindSuccess)
			usbOtgRefresh()
		}
	}
}

// ── Import games ─────────────────────────────────────────────────────────

// usbImportItem is one file to copy.
type usbImportItem struct {
	src  string
	dst  string
	rel  string
	size int64
}

// usbImportPlan is what an import would do.
type usbImportPlan struct {
	items      []usbImportItem
	bytes      int64
	present    int
	kept       int
	biosFiles  int
	systems    []string
	newSystems []string
	romRoot    string
	biosRoot   string
}

// usbImportResult is what an import did.
type usbImportResult struct {
	plan     usbImportPlan
	copied   int
	bytes    int64
	failed   int
	firstErr string
	canceled bool
	err      error
}

// usbImportRomRoot is the card's ROM folder: the first library root that
// exists (the Games library scans the same list).
func usbImportRomRoot() string {
	roots := romLibraryRoots()
	for _, r := range roots {
		if dirExists(r) {
			return r
		}
	}
	if len(roots) > 0 {
		return roots[0]
	}
	return resolvePath("Roms")
}

// usbImportBIOSRoot is the folder the emulators read BIOS files from (the
// BIOS doctor's list, RetroArch's own setting first).
func usbImportBIOSRoot() string {
	for _, r := range biosDoctorRoots() {
		if dirExists(r) {
			return r
		}
	}
	if runtime.GOOS == "linux" {
		return filepath.Join(filepath.Dir(deviceRomsRoot), "BIOS")
	}
	return resolvePath("BIOS")
}

// usbMatchSystemDir maps a system folder on the stick to the card's: the same
// name in any case, else the same name once separators are ignored ("Game
// Boy" and "game_boy"), else the stick's own name as a new folder.
func usbMatchSystemDir(existing []string, name string) (string, bool) {
	for _, e := range existing {
		if strings.EqualFold(e, name) {
			return e, true
		}
	}
	if key := romPluginFolderKey(name); key != "" {
		for _, e := range existing {
			if romPluginFolderKey(e) == key {
				return e, true
			}
		}
	}
	return name, false
}

// usbSkipName leaves out what is not a game: hidden files and the "._*"
// shadows macOS writes on FAT sticks.
func usbSkipName(name string) bool {
	return strings.HasPrefix(name, ".")
}

// usbPlanImport walks the stick's Roms/ and BIOS/ folders.
func usbPlanImport(ctx context.Context, romsDir, biosDir string) (usbImportPlan, error) {
	plan := usbImportPlan{romRoot: usbImportRomRoot(), biosRoot: usbImportBIOSRoot()}
	add := func(srcRoot, dstRoot string, bios bool) error {
		return filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				// An unreadable folder is skipped, not fatal.
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if path != srcRoot && usbSkipName(d.Name()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(srcRoot, path)
			if err != nil {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			dst := filepath.Join(dstRoot, rel)
			if have, err := os.Lstat(dst); err == nil {
				if have.Size() == info.Size() {
					plan.present++
				} else {
					plan.kept++
				}
				return nil
			}
			plan.items = append(plan.items, usbImportItem{src: path, dst: dst, rel: rel, size: info.Size()})
			plan.bytes += info.Size()
			if bios {
				plan.biosFiles++
			}
			return nil
		})
	}
	if romsDir != "" {
		var existing []string
		if entries, err := os.ReadDir(plan.romRoot); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					existing = append(existing, e.Name())
				}
			}
		}
		entries, err := os.ReadDir(romsDir)
		if err != nil {
			return plan, err
		}
		for _, e := range entries {
			// Loose files at the top of Roms/ belong to no system; the
			// library would not find them on the card either.
			if !e.IsDir() || usbSkipName(e.Name()) {
				continue
			}
			target, known := usbMatchSystemDir(existing, e.Name())
			before := len(plan.items)
			if err := add(filepath.Join(romsDir, e.Name()), filepath.Join(plan.romRoot, target), false); err != nil {
				return plan, err
			}
			if len(plan.items) > before {
				plan.systems = append(plan.systems, target)
				if !known {
					plan.newSystems = append(plan.newSystems, target)
				}
			}
		}
	}
	if biosDir != "" {
		if err := add(biosDir, plan.biosRoot, true); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

// errUSBImportExists means a file with the target name appeared while the copy
// ran; the copy is dropped rather than replacing it.
var errUSBImportExists = errors.New("a file with that name appeared on the card")

// usbCopyFile copies src to dst through a temporary name, so a cut copy never
// leaves a half file under the real name, and never replaces a file.
func usbCopyFile(ctx context.Context, src, dst string, progress func(n int64)) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// A fresh hidden name (CreateTemp opens with O_EXCL), so the copy can
	// never truncate a file the user already has, and the library skips it.
	out, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*.jhpart")
	if err != nil {
		return err
	}
	tmp := out.Name()
	_ = out.Chmod(0o644)
	defer func() {
		if err != nil {
			out.Close()
			// The temporary file is this copy's own; the user's files are
			// never removed.
			_ = os.Remove(tmp)
		}
	}()
	buf := make([]byte, 1<<20)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, err = out.Write(buf[:n]); err != nil {
				return err
			}
			progress(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			err = rerr
			return err
		}
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Rename would replace a file that appeared meanwhile (another copy, a
	// download); look once more right before it.
	if _, statErr := os.Lstat(dst); statErr == nil {
		_ = os.Remove(tmp)
		return errUSBImportExists
	}
	if err = os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// usbRunImport plans and copies. progress is called at most every 250 ms with
// done/total files and bytes and the file being copied.
func usbRunImport(ctx context.Context, romsDir, biosDir string, progress func(doneFiles, totalFiles int, doneBytes, totalBytes int64, current string)) usbImportResult {
	var res usbImportResult
	plan, err := usbPlanImport(ctx, romsDir, biosDir)
	res.plan = plan
	if err != nil {
		res.canceled = ctx.Err() != nil
		res.err = err
		return res
	}
	if len(plan.items) == 0 {
		return res
	}
	// The card must hold it all: a copy that fills the card mid-way leaves
	// the library and the saves without room.
	probe := plan.romRoot
	if !dirExists(probe) {
		probe = filepath.Dir(probe)
	}
	if _, free, err := diskUsage(probe); err == nil && free > 0 && plan.bytes > free-64<<20 {
		res.err = fmt.Errorf("the card has %s free and the games need %s", humanBytes(free), humanBytes(plan.bytes))
		return res
	}
	var last time.Time
	for i, it := range plan.items {
		report := func(n int64) {
			res.bytes += n
			if time.Since(last) >= 250*time.Millisecond {
				last = time.Now()
				progress(i, len(plan.items), res.bytes, plan.bytes, it.rel)
			}
		}
		report(0)
		err := usbCopyFile(ctx, it.src, it.dst, report)
		if err != nil {
			if ctx.Err() != nil {
				res.canceled = true
				return res
			}
			res.failed++
			if res.firstErr == "" {
				res.firstErr = it.rel + ": " + firstLine(err.Error())
			}
			continue
		}
		res.copied++
	}
	return res
}

// usbImportSummary is the result sheet's text.
func usbImportSummary(res usbImportResult) string {
	var b strings.Builder
	p := res.plan
	switch {
	case res.err != nil && !res.canceled:
		b.WriteString("The import could not run: " + firstLine(res.err.Error()) + "\n\n")
	case res.canceled:
		b.WriteString("Stopped. " + pluralizeCount(res.copied, "file was", "files were") + " copied before that and stay on the card.\n\n")
	case len(p.items) == 0:
		b.WriteString("Nothing new to copy: the card already has every file on this drive.\n\n")
	default:
		b.WriteString(fmt.Sprintf("Copied %s (%s).\n\n", pluralizeCount(res.copied, "file", "files"), humanBytes(res.bytes)))
	}
	if len(p.systems) > 0 {
		b.WriteString("## Systems\n" + strings.Join(p.systems, ", ") + "\n\n")
	}
	if len(p.newSystems) > 0 {
		b.WriteString("## New folders\n" + strings.Join(p.newSystems, ", ") + " — the card had no folder of that name. If the library does not list them, rename the folder to the one your firmware uses.\n\n")
	}
	if p.present > 0 {
		b.WriteString("• Already on the card: " + pluralizeCount(p.present, "file", "files") + "\n")
	}
	if p.kept > 0 {
		b.WriteString("• Left alone: " + pluralizeCount(p.kept, "file", "files") + " with the same name but a different size — the card's copy was kept\n")
	}
	if p.biosFiles > 0 {
		b.WriteString("• BIOS files to " + p.biosRoot + ": " + strconv.Itoa(p.biosFiles) + "\n")
	}
	if res.failed > 0 {
		b.WriteString("• Could not copy: " + pluralizeCount(res.failed, "file", "files") + " (first: " + res.firstErr + ")\n")
	}
	if p.romRoot != "" {
		b.WriteString("\nGames go to " + p.romRoot + ". Nothing on the card or the drive was deleted or replaced.")
	}
	return strings.TrimSpace(b.String())
}

