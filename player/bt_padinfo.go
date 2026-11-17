package main

// bt_padinfo.go — what the Bluetooth page says about a connected controller
// beyond "connected": its battery, and the order the pads connected in.
//
// Battery. The kernel's HID drivers publish a Bluetooth pad's battery as a
// power supply of scope "Device" — hid-playstation as ps-controller-battery-
// <address>, hid-sony as sony_controller_battery_<address>, hid-nintendo,
// xpadneo and the generic HID battery as their own names — with a capacity in
// percent or, for some, only a level word (Low, Normal, Full). The handheld's
// own battery has no "Device" scope and is never one of them. The address is in
// the supply's name or, failing that, in its HID parent's uevent (HID_UNIQ).
//
// Order. RetroArch's udev driver and SDL walk the controllers in the order the
// kernel made them, which is the order of their event nodes; a pad that
// connected first has the lower number. The page numbers the extra pads that
// way (Pad 1, Pad 2), so "which one is player 2" has an answer.
//
// Both are plain reads of /sys and /proc, done on the page's two-second pad
// reading (never on the render thread) and by the background loop for the
// low-battery warning (bt_autoreconnect.go).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// btPowerSupplyRoot is where the kernel lists power supplies; tests replace
// it.
var btPowerSupplyRoot = "/sys/class/power_supply"

// btPadBattery is one controller's battery as the kernel reports it.
type btPadBattery struct {
	// MAC is the pad's address, upper case ("" when the kernel names none).
	MAC string
	// Name is the HID device's name ("Wireless Controller"), "" when unknown.
	Name string
	// Percent is 0..100, or -1 when the driver reports no level at all.
	Percent int
	// Charging is set while it charges or sits full on a cable.
	Charging bool
}

// btMACPattern finds a Bluetooth address in a power supply's name.
var btMACPattern = regexp.MustCompile(`(?i)([0-9a-f]{2}[:_-]){5}[0-9a-f]{2}`)

// btNormMAC is an address upper case with colons, or "" when s is not one.
func btNormMAC(s string) string {
	s = strings.ToUpper(strings.NewReplacer("_", ":", "-", ":").Replace(strings.TrimSpace(s)))
	if mac, ok := bzValidMAC(s); ok {
		return mac
	}
	return ""
}

// btReadSmall reads a short sysfs attribute, trimmed; "" when unreadable.
func btReadSmall(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return strings.TrimSpace(string(buf[:n]))
}

// btLevelPercent turns a capacity_level word into a rough percentage.
func btLevelPercent(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "full":
		return 100
	case "high":
		return 80
	case "normal":
		return 50
	case "low":
		return 15
	case "critical":
		return 5
	}
	return -1
}

// btPadBatteries reads every controller battery the kernel lists.
func btPadBatteries() []btPadBattery { return btPadBatteriesIn(btPowerSupplyRoot) }

// btPadBatteriesIn is btPadBatteries for one power_supply directory.
func btPadBatteriesIn(root string) []btPadBattery {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []btPadBattery
	for _, d := range dirs {
		dir := filepath.Join(root, d.Name())
		if !strings.EqualFold(btReadSmall(filepath.Join(dir, "scope")), "Device") {
			continue
		}
		if t := btReadSmall(filepath.Join(dir, "type")); t != "" && !strings.EqualFold(t, "Battery") {
			continue
		}
		b := btPadBattery{Percent: -1}
		if m := btMACPattern.FindString(d.Name()); m != "" {
			b.MAC = btNormMAC(m)
		}
		for _, line := range strings.Split(btReadSmall(filepath.Join(dir, "device", "uevent")), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			switch key {
			case "HID_UNIQ":
				if b.MAC == "" {
					b.MAC = btNormMAC(value)
				}
			case "HID_NAME":
				b.Name = cleanAudioOutputName(value)
			}
		}
		if v, err := strconv.Atoi(btReadSmall(filepath.Join(dir, "capacity"))); err == nil && v >= 0 && v <= 100 {
			b.Percent = v
		} else {
			b.Percent = btLevelPercent(btReadSmall(filepath.Join(dir, "capacity_level")))
		}
		switch strings.ToLower(btReadSmall(filepath.Join(dir, "status"))) {
		case "charging", "full":
			b.Charging = true
		}
		if b.MAC == "" && b.Name == "" {
			continue
		}
		out = append(out, b)
	}
	return out
}

// btBatteryByMAC indexes readings by address (readings without one are left
// out: the page lists devices by address).
func btBatteryByMAC(list []btPadBattery) map[string]btPadBattery {
	out := make(map[string]btPadBattery, len(list))
	for _, b := range list {
		if b.MAC != "" {
			out[b.MAC] = b
		}
	}
	return out
}

// btBatteryWords is a reading as the page writes it ("80% battery",
// "charging · 40%"), "" when there is no level.
func btBatteryWords(b btPadBattery) string {
	if b.Percent < 0 {
		if b.Charging {
			return "charging"
		}
		return ""
	}
	pct := strconv.Itoa(b.Percent) + "%"
	if b.Charging {
		return "charging · " + pct
	}
	return pct + " battery"
}

// btEventNumber is the number of a device's event node, -1 when it has none.
func btEventNumber(handlers string) int {
	for _, h := range strings.Fields(handlers) {
		if rest, ok := strings.CutPrefix(h, "event"); ok {
			if n, err := strconv.Atoi(rest); err == nil {
				return n
			}
		}
	}
	return -1
}

// btPadSlots numbers the extra controllers with an address (Bluetooth pads,
// a PlayStation pad on a cable) in the order the kernel made them: 1 for the
// one that connected first. wordBits is the kernel's long size.
func btPadSlots(devs []bzInputDev, wordBits int) map[string]int {
	type pad struct {
		mac   string
		event int
	}
	var pads []pad
	seen := map[string]bool{}
	for _, d := range bzExternalPads(devs, wordBits) {
		mac := btNormMAC(d.Uniq)
		if mac == "" || seen[mac] {
			continue
		}
		seen[mac] = true
		pads = append(pads, pad{mac: mac, event: btEventNumber(d.Handlers)})
	}
	sort.SliceStable(pads, func(i, j int) bool {
		a, b := pads[i].event, pads[j].event
		if a < 0 || b < 0 {
			return a >= 0 && b < 0
		}
		return a < b
	})
	out := make(map[string]int, len(pads))
	for i, p := range pads {
		out[p.mac] = i + 1
	}
	return out
}

// btPadEventNode is the event node of the controller with address mac
// ("/dev/input/event7"), "" when it has none.
func btPadEventNode(devs []bzInputDev, mac string, wordBits int) string {
	for _, d := range bzExternalPads(devs, wordBits) {
		if btNormMAC(d.Uniq) != mac {
			continue
		}
		if n := btEventNumber(d.Handlers); n >= 0 {
			return "/dev/input/event" + strconv.Itoa(n)
		}
	}
	return ""
}

// parseBluealsaCodec finds the codec of addr's A2DP stream in a dbus-send
// --print-reply answer of BlueALSA's GetManagedObjects or GetPCMs: the
// "Codec" property of the PCM whose path names the address. BlueALSA 4 sends
// the codec's name ("SBC", "AAC", "aptX"); older releases a number (0 SBC,
// 2 AAC). "" when the answer does not say.
func parseBluealsaCodec(out, addr string) string {
	mac := btNormMAC(addr)
	if mac == "" {
		return ""
	}
	want := "/dev_" + strings.ReplaceAll(mac, ":", "_") + "/a2dp"
	inPCM, codecNext := false, false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "object path ") {
			inPCM = strings.Contains(strings.ToUpper(line), strings.ToUpper(want)) && strings.Contains(line, "/sink")
			codecNext = false
			continue
		}
		if !inPCM {
			continue
		}
		if strings.Contains(line, `string "Codec"`) {
			codecNext = true
			continue
		}
		if !codecNext || !strings.HasPrefix(line, "variant") {
			continue
		}
		codecNext = false
		value := strings.TrimSpace(strings.TrimPrefix(line, "variant"))
		if rest, ok := strings.CutPrefix(value, "string "); ok {
			return cleanAudioOutputName(strings.Trim(strings.TrimSpace(rest), `"`))
		}
		if f := strings.Fields(value); len(f) == 2 {
			switch f[1] {
			case "0":
				return "SBC"
			case "1":
				return "MP3"
			case "2":
				return "AAC"
			}
		}
		return ""
	}
	return ""
}

// btCodecHint is what the page says about a headset codec's delay.
func btCodecHint(codec string) string {
	switch strings.ToLower(codec) {
	case "":
		return ""
	case "aptx-ll", "aptx ll", "lc3", "faststream":
		return codec + " — a low-latency codec: sound keeps up with the picture"
	case "aptx", "aptx-hd", "aptx hd":
		return codec + " — about a tenth of a second behind the picture; fine for most games"
	}
	return codec + " — about a fifth of a second behind the picture is normal for it: fine for music and video, felt in rhythm games"
}
