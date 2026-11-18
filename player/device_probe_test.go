package main

// device_probe_test.go — identification from every source an image can offer.
//
// The Brick's stock image is the case these tests exist for: its kernel may
// describe only the SoC ("sun50iw10" / "allwinner,sun50i-a100"), its hostname
// may be a build string, and nothing in the device tree says TG3040. The model
// still has to be named — F1/F2, the handheld budgets and the 4:3 canvas all
// hang off it — so the vendor launcher, its release files, the launcher
// environment and finally the panel are consulted.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyDeviceFromEverySource(t *testing.T) {
	cases := []struct {
		name       string
		id         deviceIdentity
		want       DeviceModel
		wantSource string
	}{
		{
			name: "generic allwinner tree, trimui files and the 4:3 panel",
			id: deviceIdentity{
				Model:      "sun50iw10",
				Compatible: "allwinner,sun50i-a100",
				TrimUI:     true,
				PanelW:     1024, PanelH: 768,
			},
			want:       DeviceTrimuiBrick,
			wantSource: "trimui firmware + panel",
		},
		{
			name: "the vendor launcher names the model",
			id: deviceIdentity{
				Model:      "sun50iw10",
				Compatible: "allwinner,sun50i-a100",
				Names:      []string{"Trimui Brick"},
				TrimUI:     true,
				PanelW:     1024, PanelH: 768,
			},
			want:       DeviceTrimuiBrick,
			wantSource: "firmware name",
		},
		{
			name: "launcher DEVICE wins over nextui's tg5040 platform code",
			id: deviceIdentity{
				EnvDevice:   "brick",
				EnvPlatform: "tg5040",
			},
			want:       DeviceTrimuiBrick,
			wantSource: "launcher env",
		},
		{
			name:       "launcher platform code alone",
			id:         deviceIdentity{EnvPlatform: "tg3040"},
			want:       DeviceTrimuiBrick,
			wantSource: "launcher env",
		},
		{
			name:       "launcher device name with spaces",
			id:         deviceIdentity{EnvDevice: "smart pro s"},
			want:       DeviceTrimuiSmartProS,
			wantSource: "launcher env",
		},
		{
			name:       "launcher model hint",
			id:         deviceIdentity{EnvModel: "TrimUI Hammer"},
			want:       DeviceTrimuiHammer,
			wantSource: "launcher env",
		},
		{
			name: "a model code in the tree still decides exactly",
			id: deviceIdentity{
				Model:      "TRIMUI Brick",
				Compatible: "allwinner,sun50i-a100 tg3040",
				TrimUI:     true,
				PanelW:     1024, PanelH: 768,
			},
			want:       DeviceTrimuiBrick,
			wantSource: "device tree",
		},
		{
			name: "a model code in cpuinfo decides",
			id:   deviceIdentity{CPUInfo: "sun50iw10 tg5050", TrimUI: true},
			want: DeviceTrimuiSmartProS, wantSource: "cpuinfo",
		},
		{
			name: "hostname model code",
			id:   deviceIdentity{Hostname: "TG4040"},
			want: DeviceTrimuiBrickPro, wantSource: "hostname",
		},
		{
			name: "a name that contradicts the panel loses to the panel family",
			id: deviceIdentity{
				Names:  []string{"Trimui Smart Pro"},
				TrimUI: true,
				PanelW: 1024, PanelH: 768,
			},
			want:       DeviceTrimuiBrick,
			wantSource: "panel 1024x768",
		},
		{
			name: "a launcher shared by two families is split by the panel",
			id: deviceIdentity{
				Names:  []string{"Trimui Brick", "Trimui Smart Pro"},
				TrimUI: true,
				PanelW: 1280, PanelH: 720,
			},
			want:       DeviceTrimuiSmartPro,
			wantSource: "firmware name",
		},
		{
			name: "two variants in one family under-claim to the base model",
			id: deviceIdentity{
				Names:  []string{"Trimui Brick", "Trimui Hammer"},
				TrimUI: true,
				PanelW: 1024, PanelH: 768,
			},
			want:       DeviceTrimuiBrick,
			wantSource: "firmware name",
		},
		{
			name: "a smart pro s named only by its panel is told apart by its chip",
			id:   deviceIdentity{TrimUI: true, PanelW: 1280, PanelH: 720, SoC: socA523},
			want: DeviceTrimuiSmartProS, wantSource: "trimui firmware + panel + A523 chip",
		},
		{
			name: "a launcher carrying the smart pro name on an a523 is the pro s",
			id:   deviceIdentity{Names: []string{"Trimui Smart Pro"}, TrimUI: true, PanelW: 1280, PanelH: 720, SoC: socA523},
			want: DeviceTrimuiSmartProS, wantSource: "firmware name + A523 chip",
		},
		{
			name: "a pro s name on the a133p is the smart pro",
			id:   deviceIdentity{Names: []string{"Trimui Smart Pro S"}, TrimUI: true, PanelW: 1280, PanelH: 720, SoC: socA133P},
			want: DeviceTrimuiSmartPro, wantSource: "firmware name + A133P chip",
		},
		{
			name: "a trimui image on an a523 with no panel",
			id:   deviceIdentity{TrimUI: true, SoC: socA523},
			want: DeviceTrimuiSmartProS, wantSource: "trimui firmware + A523 chip",
		},
		{
			name: "the brick pro's store code",
			id:   deviceIdentity{Hostname: "TG4070"},
			want: DeviceTrimuiBrickPro, wantSource: "hostname",
		},
		{
			name: "both brick pro codes are one model, not a conflict",
			id:   deviceIdentity{Compatible: "allwinner,sun50i-a100 tg4040 tg4070"},
			want: DeviceTrimuiBrickPro, wantSource: "device tree",
		},
		{
			name: "generic kernel with no trimui evidence stays unidentified",
			id: deviceIdentity{
				Model:      "sun50iw10",
				Compatible: "allwinner,sun50i-a100",
				Hostname:   "buildroot",
				PanelW:     1024, PanelH: 768,
			},
			want: DeviceUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, source := classifyDevice(tc.id)
			if got != tc.want {
				t.Fatalf("classifyDevice() = %q (%s), want %q", got, source, tc.want)
			}
			if tc.wantSource != "" && source != tc.wantSource {
				t.Fatalf("classifyDevice() source = %q, want %q", source, tc.wantSource)
			}
		})
	}
}

func TestParsePanelSize(t *testing.T) {
	cases := []struct {
		text string
		w, h int32
		ok   bool
	}{
		{"U:1024x768p-60", 1024, 768, true},
		{"1024x768\n", 1024, 768, true},
		{"1024,768", 1024, 768, true},
		{"U:1280x720p-60 D:1280x720p-50", 1280, 720, true},
		{"U:1920x1080p-0", 1920, 1080, true},
		{"", 0, 0, false},
		{"320,200", 0, 0, false},
		{"not a mode", 0, 0, false},
	}
	for _, tc := range cases {
		w, h, ok := parsePanelSize(tc.text)
		if ok != tc.ok || w != tc.w || h != tc.h {
			t.Errorf("parsePanelSize(%q) = %d,%d,%v, want %d,%d,%v",
				tc.text, w, h, ok, tc.w, tc.h, tc.ok)
		}
	}
}

// A model name is 11 bytes and the scan reads 128 KiB chunks, so it can land
// across two reads. The overlap has to carry the tail forward.
func TestMainUIModelNamesFindsANameAcrossTheChunkBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MainUI")
	padding := strings.Repeat("x", (128<<10)-5)
	if err := os.WriteFile(path, []byte(padding+"Trimui Brick"+strings.Repeat("y", 4096)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := mainUIModelNames(path)
	if len(got) != 1 || got[0] != "Trimui Brick" {
		t.Fatalf("mainUIModelNames() = %v, want [Trimui Brick]", got)
	}
}

// The Pro S name contains the Pro name; one model must not be read as two.
func TestMainUIModelNamesReportsOnlyTheLongestName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MainUI")
	if err := os.WriteFile(path, []byte("\x00\x00Trimui Smart Pro S\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := mainUIModelNames(path)
	if len(got) != 1 || got[0] != "Trimui Smart Pro S" {
		t.Fatalf("mainUIModelNames() = %v, want [Trimui Smart Pro S]", got)
	}
	if got := mainUIModelNames(filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Fatalf("a missing launcher returned %v, want nil", got)
	}
	// A launcher shared by the Brick and the Brick Pro carries both names at
	// two sites: both are reported, and the Brick is not read as a Pro.
	shared := filepath.Join(t.TempDir(), "MainUI")
	if err := os.WriteFile(shared, []byte("\x00Trimui Brick\x00\x00Trimui Brick Pro\x00Trimui Smart Pro Settings\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = mainUIModelNames(shared)
	if strings.Join(got, "|") != "Trimui Brick Pro|Trimui Smart Pro|Trimui Brick" {
		t.Fatalf("mainUIModelNames(shared) = %v", got)
	}
}

func TestDeviceSoCFrom(t *testing.T) {
	cases := []struct {
		name string
		ev   deviceSoCEvidence
		want string
	}{
		{"a523 tree", deviceSoCEvidence{Tree: "allwinner,sun55iw3p1 arm,sun55iw3p1"}, socA523},
		{"a133p tree", deviceSoCEvidence{Tree: "sun50iw10 allwinner,sun50i-a100"}, socA133P},
		{"a55 cores", deviceSoCEvidence{CPUParts: []string{"0xd05", "0xd05"}}, socA523},
		{"a53 cores and powervr", deviceSoCEvidence{CPUParts: []string{"0xd03"}, PowerVR: true}, socA133P},
		{"mali node", deviceSoCEvidence{Mali: true}, socA523},
		{"conflicting strong signs", deviceSoCEvidence{CPUParts: []string{"0xd03"}, Mali: true}, ""},
		{"eight cpus on 5.15", deviceSoCEvidence{CPUs: 8, Kernel: "5.15.147"}, socA523},
		{"four cpus on 4.9", deviceSoCEvidence{CPUs: 4, Kernel: "4.9.191"}, socA133P},
		{"nothing", deviceSoCEvidence{}, ""},
	}
	for _, tc := range cases {
		if got, why := deviceSoCFrom(tc.ev); got != tc.want {
			t.Errorf("%s: deviceSoCFrom() = %q (%s), want %q", tc.name, got, why, tc.want)
		}
	}
}

func TestProbeFirmwareIdentity(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "os-release")
	if err := os.WriteFile(release, []byte("PRETTY_NAME=\"TrimUI Brick\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainUI := filepath.Join(dir, "MainUI")
	if err := os.WriteFile(mainUI, []byte("...Trimui Brick..."), 0o644); err != nil {
		t.Fatal(err)
	}
	firmwareDir := filepath.Join(dir, "trimui")
	if err := os.MkdirAll(firmwareDir, 0o755); err != nil {
		t.Fatal(err)
	}

	prevDirs, prevNameFiles, prevUI := deviceFirmwareDirs, deviceNameFiles, deviceMainUIPath
	t.Cleanup(func() {
		deviceFirmwareDirs, deviceNameFiles, deviceMainUIPath = prevDirs, prevNameFiles, prevUI
	})
	deviceFirmwareDirs = []string{firmwareDir}
	deviceNameFiles = []string{release}
	deviceMainUIPath = mainUI

	got := probeFirmwareIdentity()
	if !got.TrimUI {
		t.Fatal("a TrimUI firmware directory did not mark the image")
	}
	joined := strings.Join(got.Names, " ")
	if !strings.Contains(joined, "TrimUI Brick") || !strings.Contains(joined, "Trimui Brick") {
		t.Fatalf("firmware names = %q, want the release file and the launcher", joined)
	}

	// Without any TrimUI marker the launcher is not even opened: a desktop
	// Linux build must not read tens of megabytes to find nothing.
	empty := t.TempDir()
	deviceFirmwareDirs = []string{filepath.Join(empty, "absent")}
	deviceNameFiles = nil
	deviceMainUIPath = filepath.Join(empty, "MainUI")
	if err := os.WriteFile(deviceMainUIPath, []byte("Trimui Brick"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := probeFirmwareIdentity(); got.TrimUI || len(got.Names) != 0 {
		t.Fatalf("a foreign image was read as TrimUI: %+v", got)
	}
}

func TestCPUInfoIdentityKeepsTheBoardLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cpuinfo")
	text := "processor\t: 0\nHardware\t: Allwinner sun50iw10 (Family: sun50i-a100)\n" +
		"Revision\t: 0000\nSerial\t\t: 1234\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	got := cpuInfoIdentity(path)
	if !strings.Contains(got, "sun50iw10") || !strings.Contains(got, "0000") {
		t.Fatalf("cpuInfoIdentity() = %q, want the Hardware and Revision values", got)
	}
	if strings.Contains(got, "1234") {
		t.Fatalf("cpuInfoIdentity() leaked the serial: %q", got)
	}
	if got := cpuInfoIdentity(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Fatalf("a missing cpuinfo read %q, want empty", got)
	}
}

// virtual_size can describe a double-wide pan buffer; the panel is the half
// that names a known size. The mode list is the panel itself and is taken
// as-is.
func TestReadPanelSize(t *testing.T) {
	dir := t.TempDir()
	modes := filepath.Join(dir, "modes")
	if err := os.WriteFile(modes, []byte("U:1024x768p-60\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	virtual := filepath.Join(dir, "virtual_size")
	if err := os.WriteFile(virtual, []byte("2048,768\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prev := devicePanelFiles
	t.Cleanup(func() { devicePanelFiles = prev })

	devicePanelFiles = []string{modes}
	if w, h := readPanelSize(); w != 1024 || h != 768 {
		t.Fatalf("readPanelSize(modes) = %dx%d, want 1024x768", w, h)
	}
	devicePanelFiles = []string{virtual}
	if w, h := readPanelSize(); w != 1024 || h != 768 {
		t.Fatalf("readPanelSize(double-wide virtual_size) = %dx%d, want 1024x768", w, h)
	}
	devicePanelFiles = []string{filepath.Join(dir, "absent")}
	if w, h := readPanelSize(); w != 0 || h != 0 {
		t.Fatalf("readPanelSize(missing) = %dx%d, want 0x0", w, h)
	}
}
