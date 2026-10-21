package main

// ppsspp_calibration_test.go — the deadzone wizard's backend: where the ini
// lives, how it is edited without losing anything, and what the maths writes.

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPPSSPPConfigCandidates(t *testing.T) {
	roots := []string{"/mnt/SDCARD", " /mnt/SDCARD ", "/media/card", ""}
	got := ppssppConfigCandidates(roots)
	if len(got) != 6 {
		t.Fatalf("candidates = %d, want 6 (two usable roots, three layouts each): %v", len(got), got)
	}
	want := []string{
		filepath.Join("/mnt/SDCARD", "PSP", "SYSTEM", "ppsspp.ini"),
		filepath.Join("/mnt/SDCARD", "ppsspp", "PSP", "SYSTEM", "ppsspp.ini"),
		filepath.Join("/mnt/SDCARD", "Apps", "PPSSPP", "PSP", "SYSTEM", "ppsspp.ini"),
		filepath.Join("/media/card", "PSP", "SYSTEM", "ppsspp.ini"),
	}
	for i, w := range want[:3] {
		if got[i] != w {
			t.Errorf("candidate %d = %q, want %q", i, got[i], w)
		}
	}
	if got[3] != want[3] {
		t.Errorf("second root candidate = %q, want %q", got[3], want[3])
	}
	// A duplicate root must not produce duplicate paths.
	if len(ppssppConfigCandidates([]string{"/x", "/x"})) != len(ppssppConfigRelPaths) {
		t.Error("duplicate roots produced duplicate candidates")
	}
	if len(ppssppConfigCandidates(nil)) != 0 {
		t.Error("no roots produced candidates")
	}
}

func TestFindPPSSPPConfig(t *testing.T) {
	root := t.TempDir()
	if got := findPPSSPPConfig([]string{root}); got != "" {
		t.Fatalf("found %q on an empty card", got)
	}

	// The standalone memstick folder wins over the Apps copy.
	appPath := filepath.Join(root, "Apps", "PPSSPP", "PSP", "SYSTEM")
	if err := os.MkdirAll(appPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appPath, "ppsspp.ini"), []byte("[ControlMapping]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	memstick := filepath.Join(root, "PSP", "SYSTEM")
	if err := os.MkdirAll(memstick, 0o755); err != nil {
		t.Fatal(err)
	}
	memstickIni := filepath.Join(memstick, "ppsspp.ini")
	if err := os.WriteFile(memstickIni, []byte("[ControlMapping]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findPPSSPPConfig([]string{root}); got != memstickIni {
		t.Errorf("found %q, want the memstick copy %q", got, memstickIni)
	}

	// A directory named like the ini is not a config.
	dirRoot := t.TempDir()
	asDir := filepath.Join(dirRoot, "PSP", "SYSTEM", "ppsspp.ini")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findPPSSPPConfig([]string{dirRoot}); got != "" {
		t.Errorf("a directory was accepted as a config: %q", got)
	}
}

func TestPPSSPPINIGetAndSet(t *testing.T) {
	lines := []string{
		"[Graphics]",
		"AnalogDeadzone = 0.900",
		"; a comment that must survive",
		"",
		"[ControlMapping]",
		"; controller tuning",
		"AnalogDeadzone = 0.100",
		"AnalogSensitivity = 1.100",
		"",
	}

	// Reading is scoped to [ControlMapping]: the Graphics lookalike is ignored.
	value, ok := ppssppINIValue(lines, "analogdeadzone")
	if !ok || value != "0.100" {
		t.Fatalf("value = %q, ok = %v; want the ControlMapping value", value, ok)
	}

	updated := setPPSSPPINIValue(lines, "AnalogDeadzone", "0.040")
	if got, _ := ppssppINIValue(updated, "AnalogDeadzone"); got != "0.040" {
		t.Errorf("after set: %q", got)
	}
	joined := strings.Join(updated, "\n")
	if !strings.Contains(joined, "AnalogDeadzone = 0.900") {
		t.Error("the key in [Graphics] was changed")
	}
	for _, want := range []string{"; a comment that must survive", "; controller tuning", "AnalogSensitivity = 1.100"} {
		if !strings.Contains(joined, want) {
			t.Errorf("line %q was lost", want)
		}
	}
	if len(updated) != len(lines) {
		t.Errorf("line count changed from %d to %d", len(lines), len(updated))
	}
	if n := strings.Count(joined, "AnalogDeadzone = 0.040"); n != 1 {
		t.Errorf("value written %d times", n)
	}

	// Setting the same value again is a no-op.
	again := setPPSSPPINIValue(updated, "AnalogDeadzone", "0.040")
	if strings.Join(again, "\n") != joined {
		t.Error("a second identical set changed the file")
	}
}

func TestSetPPSSPPINIValueAppends(t *testing.T) {
	// Missing key: appended at the end of its own section, not the file.
	lines := []string{"[ControlMapping]", "AnalogSensitivity = 1.100", "", "[Graphics]", "Resolution = 2"}
	got := setPPSSPPINIValue(lines, "AnalogDeadzone", "0.050")
	if v, ok := ppssppINIValue(got, "AnalogDeadzone"); !ok || v != "0.050" {
		t.Fatalf("value not added: %v", got)
	}
	joined := strings.Join(got, "\n")
	if strings.Index(joined, "AnalogDeadzone") > strings.Index(joined, "[Graphics]") {
		t.Errorf("key was appended after the next section:\n%s", joined)
	}

	// Missing section: created at the end, with the key under it.
	noSection := []string{"[Graphics]", "Resolution = 2", ""}
	got = setPPSSPPINIValue(noSection, "AnalogDeadzone", "0.060")
	joined = strings.Join(got, "\n")
	if !strings.Contains(joined, "[ControlMapping]") || !strings.Contains(joined, "AnalogDeadzone = 0.060") {
		t.Errorf("section was not created:\n%s", joined)
	}
	if v, ok := ppssppINIValue(got, "AnalogDeadzone"); !ok || v != "0.060" {
		t.Errorf("value in the new section = %q, %v", v, ok)
	}

	// Section header casing must still match.
	mixed := setPPSSPPINIValue([]string{"[controlmapping]", "AnalogSensitivity = 1.0"}, "AnalogDeadzone", "0.070")
	if v, ok := ppssppINIValue(mixed, "AnalogDeadzone"); !ok || v != "0.070" {
		t.Errorf("mixed-case section did not receive the key: %v", mixed)
	}
}

func TestDeadzoneFromSamples(t *testing.T) {
	cases := []struct {
		name    string
		samples []float64
		idle    float64
		want    float64
	}{
		{"no usable input falls back to the floor", nil, 0, 0.02},
		{"all NaN falls back to the floor", []float64{math.NaN(), math.NaN()}, 0.1, 0.02},
		{"quiet stick uses the noise margin", []float64{0.95, 0.92, 0.9}, 0.05, 0.08},
		{"noisy idle raises the deadzone", []float64{0.9, 0.95}, 0.12, 0.15},
		{"tiny deflection caps the deadzone", []float64{0.03, 0.04}, 0.0, 0.02},
		{"half-travel stick keeps the deadzone small", []float64{0.2, 0.22}, 0.02, 0.05},
		{"huge deflection is capped by the ceiling", []float64{1, 1}, 0.4, 0.25},
		{"negative samples are ignored", []float64{-0.5, 0.94}, 0.03, 0.06},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := deadzoneFromSamples(c.samples, c.idle)
			if math.Abs(got-c.want) > 1e-9 {
				t.Fatalf("deadzone = %v, want %v", got, c.want)
			}
			if got < 0.02 || got > 0.25 {
				t.Fatalf("deadzone %v escaped [0.02, 0.25]", got)
			}
			// Never above 60% of the smallest real deflection, except when the
			// 0.02 safety floor is the larger value.
			minSample := math.Inf(1)
			for _, s := range c.samples {
				if s == s && s >= 0 && s < minSample {
					minSample = s
				}
			}
			if !math.IsInf(minSample, 1) && got > math.Max(0.02, 0.6*minSample)+1e-9 {
				t.Errorf("deadzone %v is above 60%% of the smallest sample %v", got, minSample)
			}
		})
	}
}

func TestWritePPSSPPDeadzone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ppsspp.ini")
	original := "[ControlMapping]\r\nAnalogDeadzone = 0.100\r\nAnalogSensitivity = 1.100\r\n\r\n[Graphics]\r\nResolution = 2\r\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writePPSSPPDeadzone(path, 0.04); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "AnalogDeadzone = 0.040") {
		t.Errorf("value not written:\n%s", text)
	}
	if !strings.Contains(text, "AnalogSensitivity = 1.100") || !strings.Contains(text, "[Graphics]") {
		t.Errorf("unrelated keys were lost:\n%s", text)
	}
	if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Errorf("line endings were not preserved:\n%q", text)
	}

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup: %v", err)
	}
	if string(backup) != original {
		t.Error("the backup does not hold the original file")
	}
	if runtime.GOOS != "windows" { // Windows reports synthetic permission bits
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("permissions changed: %v, %v", info, err)
		}
	}

	// A second calibration must not overwrite the factory backup.
	if err := writePPSSPPDeadzone(path, 0.08); err != nil {
		t.Fatal(err)
	}
	backup, err = os.ReadFile(path + ".bak")
	if err != nil || string(backup) != original {
		t.Error("the second write replaced the original backup")
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "AnalogDeadzone = 0.080") {
		t.Errorf("second value not written:\n%s", raw)
	}
}

func TestPPSSPPDeadzoneSummary(t *testing.T) {
	if got := ppssppDeadzoneSummary(""); got != "not installed" {
		t.Errorf("empty path summary = %q", got)
	}
	if got := ppssppDeadzoneSummary(filepath.Join(t.TempDir(), "missing.ini")); got != "not installed" {
		t.Errorf("missing file summary = %q", got)
	}

	path := filepath.Join(t.TempDir(), "ppsspp.ini")
	body := "[ControlMapping]\nAnalogDeadzone = 0.1\nAnalogSensitivity = 1.1\n[Graphics]\nResolution = 2\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ppssppDeadzoneSummary(path)
	if got != "deadzone 0.100 · 2 entries" {
		t.Errorf("summary = %q", got)
	}

	noValue := filepath.Join(t.TempDir(), "ppsspp.ini")
	if err := os.WriteFile(noValue, []byte("[ControlMapping]\nAnalogSensitivity = 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ppssppDeadzoneSummary(noValue); got != "deadzone not set · 1 entries" {
		t.Errorf("unset summary = %q", got)
	}
}
