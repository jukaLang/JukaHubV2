package main

import (
	"sort"
	"strings"
	"testing"
)

// TestUSBOtgParsers covers the outside data the USB & OTG screen reads: the
// sysfs device tree (classification included), role files, blkid output and
// block device names.
func TestUSBOtgParsers(t *testing.T) {
	files := map[string]string{}
	for _, line := range []string{
		"usb1/idVendor=1d6b 1-0:1.0/bInterfaceClass=09",
		// An 8BitDo pad: generic HID with a joystick node.
		"1-1/idVendor=2dc8 1-1/idProduct=3106 1-1/product=8BitDo_Pro_2 1-1/bDeviceClass=00",
		"1-1:1.0/bInterfaceClass=03 1-1:1.0/bInterfaceSubClass=00 1-1:1.0/bInterfaceProtocol=00",
		"1-1:1.0/0003:2DC8:3106.0001/input/input3/js0/dev=13:0",
		// A keyboard with a media-key interface.
		"1-2/idVendor=046d 1-2/bDeviceClass=00",
		"1-2:1.0/bInterfaceClass=03 1-2:1.0/bInterfaceSubClass=01 1-2:1.0/bInterfaceProtocol=01",
		"1-2:1.1/bInterfaceClass=03 1-2:1.1/bInterfaceSubClass=00 1-2:1.1/bInterfaceProtocol=00",
		// A USB-C DAC whose volume keys are HID.
		"1-3/idVendor=05ac 1-3/bDeviceClass=00 1-3:1.0/bInterfaceClass=01 1-3:1.3/bInterfaceClass=03",
		// A Realtek Ethernet adapter (vendor class, known driver).
		"1-4/idVendor=0bda 1-4/bDeviceClass=00 1-4:1.0/bInterfaceClass=ff",
		// An Xbox 360 pad (XInput) and a built-in hub.
		"1-5/idVendor=045e 1-5/bDeviceClass=ff",
		"1-5:1.0/bInterfaceClass=ff 1-5:1.0/bInterfaceSubClass=5d 1-5:1.0/bInterfaceProtocol=01",
		"1-6/idVendor=05e3 1-6/bDeviceClass=09 1-6/removable=fixed",
	} {
		for _, kv := range strings.Fields(line) {
			k, v, _ := strings.Cut(kv, "=")
			files[k] = strings.ReplaceAll(v, "_", " ")
		}
	}
	links := map[string]string{"1-4:1.0/driver": "r8152"}
	fs := usbSysfs{
		read: func(rel string) (string, bool) { v, ok := files[rel]; return v, ok },
		list: func(rel string) []string {
			prefix := ""
			if rel != "" {
				prefix = rel + "/"
			}
			seen := map[string]bool{}
			var out []string
			for k := range files {
				if !strings.HasPrefix(k, prefix) {
					continue
				}
				name := strings.SplitN(strings.TrimPrefix(k, prefix), "/", 2)[0]
				if !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
			}
			sort.Strings(out)
			return out
		},
		link: func(rel string) string { return links[rel] },
	}
	want := map[string]usbKind{"1-1": usbKindController, "1-2": usbKindKeyboard, "1-3": usbKindAudio, "1-4": usbKindNetwork, "1-5": usbKindController, "1-6": usbKindHub}
	devices := parseUSBDevices(fs)
	if len(devices) != len(want) {
		t.Fatalf("got %d devices, want %d: %+v", len(devices), len(want), devices)
	}
	for _, d := range devices {
		if d.Kind != want[d.Name] {
			t.Errorf("%s: kind %s, want %s", d.Name, d.Kind.label(), want[d.Name].label())
		}
	}
	if devices[0].title() != "8BitDo Pro 2" || !devices[5].Fixed || usbOtgCountsUser(devices[5]) {
		t.Errorf("title/fixed wrong: %q fixed=%v", devices[0].title(), devices[5].Fixed)
	}

	for raw, mode := range map[string]string{"host\n": "host", "usb_device": "device", "null": "none", "": ""} {
		if got := usbParseRole(raw); got != mode {
			t.Errorf("usbParseRole(%q) = %q, want %q", raw, got, mode)
		}
	}
	if label, fstype := usbParseBlkid(`/dev/sda1: LABEL="MY GAMES" UUID="1A2B-3C4D" BLOCK_SIZE="512" TYPE="vfat" PARTUUID="0001-01"`); label != "MY GAMES" || fstype != "vfat" {
		t.Errorf("blkid: %q %q", label, fstype)
	}
	for name, ok := range map[string]bool{"sda": true, "sdb12": true, "sdaa1": true, "sd": false, "sda1x": false, "mmcblk0p1": false, "sd../a": false} {
		if usbValidVolumeName(name) != ok {
			t.Errorf("usbValidVolumeName(%q) != %v", name, ok)
		}
	}
}
