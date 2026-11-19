package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBtAttachCommandsUseXR829StartupBaud(t *testing.T) {
	u, ok := btChipUART(DeviceTrimuiSmartPro)
	if !ok {
		t.Fatal("Smart Pro has no XR829 UART")
	}
	got := btAttachCommands(u)
	want := []btAttachCommand{
		{tool: "hciattach", args: []string{"-n", "ttyS1", "xradio"}},
		{tool: "rtk_hciattach", args: []string{"-n", "-s", "115200", "ttyS1", "xradio"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("attach commands = %#v; want %#v", got, want)
	}
}

func TestModulesDepHasReadsModuleNames(t *testing.T) {
	dep := "kernel/drivers/net/wireless/aic8800/aic8800_fdrv.ko: kernel/drivers/net/wireless/aic8800/aic8800_bsp.ko\n" +
		"extra/aic8800_btlpm.ko.xz:\n" +
		"kernel/net/bluetooth/hidp/hidp.ko: kernel/net/bluetooth/bluetooth.ko\n"
	if !modulesDepHas(dep, "aic8800_btlpm") || !modulesDepHas(dep, "aic8800_fdrv") {
		t.Fatal("a listed module was missed")
	}
	if modulesDepHas(dep, "aic8800_bsp") || modulesDepHas(dep, "aic8800") || modulesDepHas(dep, "bluetooth") {
		t.Fatal("a dependency or a name prefix counted as a listed module")
	}
}

func TestBtWakeChipRestoresFailedAttach(t *testing.T) {
	path := filepath.Join(t.TempDir(), "btwrite")
	if err := os.WriteFile(path, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := btWakeChip(path)
	if restore == nil {
		t.Fatal("btWakeChip did not wake the chip")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "1" {
		t.Fatalf("awake btwrite = %q, %v; want 1", got, err)
	}
	restore()
	if got, err := os.ReadFile(path); err != nil || string(got) != "0" {
		t.Fatalf("restored btwrite = %q, %v; want 0", got, err)
	}
}

func TestBtRfkillPulseUsesFirmwareStateFile(t *testing.T) {
	root := t.TempDir()
	write := func(name, field, value string) {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, field), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name, field string) string {
		t.Helper()
		value, err := os.ReadFile(filepath.Join(root, name, field))
		if err != nil {
			t.Fatal(err)
		}
		return string(value)
	}
	write("rfkill0", "type", "bluetooth\n")
	write("rfkill0", "state", "1")
	write("rfkill0", "soft", "0")
	write("rfkill1", "type", "wlan\n")
	write("rfkill1", "state", "1")
	write("rfkill2", "type", "bluetooth\n")
	write("rfkill2", "soft", "0")
	steps := 0
	sleep := func(_ context.Context, d time.Duration) bool {
		if d != time.Second {
			t.Errorf("pulse delay = %s; want 1s", d)
		}
		steps++
		if steps == 1 {
			if got := read("rfkill0", "state"); got != "0" {
				t.Errorf("legacy state during reset = %q; want 0", got)
			}
			if got := read("rfkill2", "soft"); got != "1" {
				t.Errorf("soft block during reset = %q; want 1", got)
			}
		}
		return true
	}
	if !btRfkillPulseAt(context.Background(), root, sleep) {
		t.Fatal("rfkill pulse failed")
	}
	if steps != 2 || read("rfkill0", "state") != "1" || read("rfkill0", "soft") != "0" ||
		read("rfkill1", "state") != "1" || read("rfkill2", "soft") != "0" {
		t.Fatal("rfkill pulse did not restore the Bluetooth switches while leaving Wi-Fi alone")
	}
}

// The radio switches without the rfkill program (rfkill_sysfs.go): a device
// whose rfkill exits 127 is read and switched through these files alone.
func TestRfkillSysfsSwitchesByKind(t *testing.T) {
	root := t.TempDir()
	files := map[string]map[string]string{
		// A board's own switch: the Bluetooth chip's power.
		"rfkill0": {"type": "bluetooth\n", "name": "sunxi-bt\n", "soft": "0", "hard": "0"},
		"rfkill1": {"type": "wlan\n", "name": "phy0\n", "soft": "0", "hard": "0"},
		// An old kernel: state only.
		"rfkill2": {"type": "wlan\n", "state": "1"},
		"rfkill3": {"type": "bluetooth\n", "name": "hci0\n", "soft": "0", "hard": "0"},
		// Not a switch name.
		"rfkillx": {"type": "wlan\n", "soft": "1"},
	}
	for name, attrs := range files {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for attr, value := range attrs {
			if err := os.WriteFile(filepath.Join(dir, attr), []byte(value), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	soft := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, name, "soft"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if blocked, ok := rfkillSysfsBlocked(root, rfkillWlan); !ok || blocked {
		t.Fatalf("wlan before = blocked %v, ok %v; want on", blocked, ok)
	}
	if err := rfkillSysfsSet(root, rfkillWlan, true, false); err != nil {
		t.Fatal(err)
	}
	if blocked, ok := rfkillSysfsBlocked(root, rfkillWlan); !ok || !blocked {
		t.Fatalf("wlan after block = blocked %v, ok %v; want off", blocked, ok)
	}
	if blocked, _ := rfkillSysfsBlocked(root, rfkillBluetooth); blocked {
		t.Fatal("blocking Wi-Fi blocked Bluetooth too")
	}
	if on, ok := rfkillSysfsAirplane(root, false); !ok || on {
		t.Fatalf("airplane with Bluetooth on = %v, %v; want off", on, ok)
	}
	if err := rfkillSysfsSet(root, rfkillAnyKind, true, false); err != nil {
		t.Fatal(err)
	}
	if on, _ := rfkillSysfsAirplane(root, false); !on {
		t.Fatal("every switch blocked does not read as Airplane Mode")
	}
	if err := rfkillSysfsSet(root, rfkillAnyKind, false, false); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := rfkillSysfsBlocked(root, rfkillAnyKind); blocked {
		t.Fatal("unblocking every switch left one blocked")
	}
	if err := rfkillSysfsSet(t.TempDir(), rfkillWlan, false, false); !errors.Is(err, errRfkillNoSwitch) {
		t.Fatalf("no switches = %v; want errRfkillNoSwitch", err)
	}
	// The handhelds: off through the stack switches alone, the chip powered.
	if err := rfkillSysfsSet(root, rfkillBluetooth, true, true); err != nil {
		t.Fatal(err)
	}
	if soft("rfkill0") != "0" || soft("rfkill3") != "1" {
		t.Fatalf("stack-only Bluetooth off: power %q, hci0 %q; want 0, 1", soft("rfkill0"), soft("rfkill3"))
	}
	if err := rfkillSysfsSet(root, rfkillAnyKind, true, true); err != nil {
		t.Fatal(err)
	}
	if on, _ := rfkillSysfsAirplane(root, true); !on {
		t.Fatal("stack switches blocked do not read as the handheld's Airplane Mode")
	}
	if err := rfkillSysfsSet(root, rfkillAnyKind, false, true); err != nil {
		t.Fatal(err)
	}
	if soft("rfkill1") != "0" || soft("rfkill3") != "0" {
		t.Fatal("stack-only Airplane Mode off left a stack switch blocked")
	}
	// No hci0 registered: nothing is attached, and the power switch moves.
	if err := os.RemoveAll(filepath.Join(root, "rfkill3")); err != nil {
		t.Fatal(err)
	}
	if err := rfkillSysfsSet(root, rfkillBluetooth, true, true); err != nil || soft("rfkill0") != "1" {
		t.Fatalf("Bluetooth off with no stack switch = %v, power %q; want nil, 1", err, soft("rfkill0"))
	}
	if err := rfkillSysfsSet(root, rfkillBluetooth, false, false); err != nil {
		t.Fatal(err)
	}
	// A physical switch: Wi-Fi on cannot be done, Airplane Mode off can.
	if err := os.WriteFile(filepath.Join(root, "rfkill1", "hard"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := rfkillSysfsBlocked(root, rfkillWlan); !blocked {
		t.Fatal("a hard-blocked radio reads on")
	}
	if err := rfkillSysfsSet(root, rfkillWlan, false, false); !errors.Is(err, errRfkillHard) {
		t.Fatalf("unblocking a hard-blocked radio = %v; want errRfkillHard", err)
	}
	if err := rfkillSysfsSet(root, rfkillAnyKind, false, false); err != nil {
		t.Fatalf("Airplane Mode off with a hard block = %v; want nil", err)
	}
}

func TestBtRfkillPulseRestoresPowerWhenInterrupted(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "rfkill0")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"type": "bluetooth", "state": "1"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sleep := func(context.Context, time.Duration) bool { return false }
	if btRfkillPulseAt(context.Background(), root, sleep) {
		t.Fatal("interrupted reset reported success")
	}
	state, err := os.ReadFile(filepath.Join(dir, "state"))
	if err != nil || string(state) != "1" {
		t.Fatalf("radio state after interrupted reset = %q, %v; want 1", state, err)
	}
}
