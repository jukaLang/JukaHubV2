package main

import (
	"context"
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
