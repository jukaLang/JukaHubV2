package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ensureSavestateResume is the pure edit a launch makes (racfgscope.go).
func ensureSavestateResume(cfg string) (string, bool) {
	return raScopeApplyText(cfg, savestateResumeValues())
}

func TestEnsureSavestateResumeAppendsMissingSwitches(t *testing.T) {
	in := "# stock config\nvideo_fullscreen = \"true\"\n"
	out, changed := ensureSavestateResume(in)
	if !changed {
		t.Fatal("missing switches reported no change")
	}
	for _, key := range []string{savestateAutoSaveKey, savestateAutoLoadKey} {
		if v, ok := retroArchConfigValue(out, key); !ok || !strings.EqualFold(v, "true") {
			t.Errorf("%s = %q (found %t), want true\n%s", key, v, ok, out)
		}
	}
	if !strings.Contains(out, "# stock config") || !strings.Contains(out, `video_fullscreen = "true"`) {
		t.Errorf("unrelated config was not preserved:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("appended config lost its trailing newline")
	}
}

func TestEnsureSavestateResumeReplacesFalseAndKeepsComments(t *testing.T) {
	in := "savestate_auto_save = \"false\" # keep this note\r\n" +
		"savestate_auto_load = \"false\"\r\n" +
		"audio_volume = \"0.5\"\r\n"
	out, changed := ensureSavestateResume(in)
	if !changed {
		t.Fatal("false switches reported no change")
	}
	if !strings.Contains(out, `savestate_auto_save = "true" # keep this note`) {
		t.Errorf("inline comment or value not preserved:\n%q", out)
	}
	if !strings.Contains(out, "\r\n") {
		t.Errorf("CRLF line endings were not preserved:\n%q", out)
	}
	if !strings.Contains(out, `audio_volume = "0.5"`) {
		t.Errorf("unrelated setting was dropped:\n%q", out)
	}
}

func TestEnsureSavestateResumeIsIdempotent(t *testing.T) {
	in := "savestate_auto_save = \"true\"\nsavestate_auto_load = \"true\"\n"
	out, changed := ensureSavestateResume(in)
	if changed {
		t.Fatal("already-enabled config reported a change")
	}
	if out != in {
		t.Fatalf("text changed without a change report:\n%q", out)
	}
}

func TestEnableSavestateResumeBacksUpOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retroarch.cfg")
	original := "savestate_auto_save = \"false\"\nsavestate_auto_load = \"false\"\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	enableSavestateResume := func(path string) (bool, error) {
		return editRetroArchConfig(path, ensureSavestateResume)
	}
	changed, err := enableSavestateResume(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first enable reported no change")
	}
	backup, err := os.ReadFile(path + savestateBackupSuffix)
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want the original config", backup)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits; the mode is only meaningful on a
	// handheld or a Unix desktop.
	if !IsWindows() && info.Mode().Perm() != 0o640 {
		t.Errorf("config mode = %o, want 640", info.Mode().Perm())
	}

	// A second launch must not rewrite the file or replace the backup with the
	// already-edited text.
	changed, err = enableSavestateResume(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second enable rewrote an already-enabled config")
	}
	backup, _ = os.ReadFile(path + savestateBackupSuffix)
	if string(backup) != original {
		t.Errorf("backup was overwritten: %q", backup)
	}
}

func TestPrepareSavestateResumeWritesTheDiscoveredConfig(t *testing.T) {
	raScopeTestSetup(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "retroarch.cfg")
	if err := os.WriteFile(cfgPath, []byte("savestate_auto_save = \"false\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec retroarch \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	prevCandidates := savestateResumeCandidates
	savestateResumeCandidates = func() []string { return []string{cfgPath} }
	t.Cleanup(func() { savestateResumeCandidates = prevCandidates })

	if !prepareSavestateResume(launcher) {
		t.Fatal("prepareSavestateResume did not find the RetroArch config")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := retroArchConfigValue(string(raw), savestateAutoLoadKey); !ok || !strings.EqualFold(v, "true") {
		t.Errorf("auto-load = %q (found %t), want true\n%s", v, ok, raw)
	}
	// The switches belong to this launch only: once the game is over, the
	// firmware's own menu finds the user's config again.
	raScopeRestoreNow(t)
	if raw, _ = os.ReadFile(cfgPath); string(raw) != "savestate_auto_save = \"false\"\n" {
		t.Errorf("after the game the config is %q, want the user's back", raw)
	}
}

// TrimUI's own binary name does not contain "retroarch"; a launch script that
// only names ra64.trimui or a libretro core is still a RetroArch launch.
func TestRetroArchLauncherKnowsTrimUIBinaries(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"ra.sh":   "#!/bin/sh\ncd $RA_DIR\nHOME=$RA_DIR/ $RA_DIR/ra64.trimui -v -L $CORES/mgba.so \"$*\"\n",
		"core.sh": "#!/bin/sh\n./emu -L cores/snes9x_libretro.so \"$1\"\n",
	} {
		launcher := filepath.Join(dir, name)
		if err := os.WriteFile(launcher, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		if !retroArchLauncher(launcher) {
			t.Errorf("%s is not seen as a RetroArch launcher", name)
		}
	}
}

func TestPrepareSavestateResumeRespectsOptOut(t *testing.T) {
	raScopeTestSetup(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "retroarch.cfg")
	if err := os.WriteFile(cfgPath, []byte("video_fullscreen = \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec retroarch \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	prevCandidates := savestateResumeCandidates
	savestateResumeCandidates = func() []string { return []string{cfgPath} }
	t.Cleanup(func() { savestateResumeCandidates = prevCandidates })

	prevConfig := appConfig
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{
		savestateResumeConfigKey: false,
	}}}
	t.Cleanup(func() { appConfig = prevConfig })

	if prepareSavestateResume(launcher) {
		t.Error("resume was configured even though the user switched it off")
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), savestateAutoSaveKey) {
		t.Errorf("config was edited despite the opt-out:\n%s", raw)
	}
}

func TestPrepareSavestateResumeLeavesStandaloneEmulatorsAlone(t *testing.T) {
	raScopeTestSetup(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "retroarch.cfg")
	if err := os.WriteFile(cfgPath, []byte("video_fullscreen = \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "mgba.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec mgba \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	prevCandidates := savestateResumeCandidates
	savestateResumeCandidates = func() []string { return []string{cfgPath} }
	t.Cleanup(func() { savestateResumeCandidates = prevCandidates })

	if prepareSavestateResume(launcher) {
		t.Error("a standalone emulator reported RetroArch resume as configured")
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), savestateAutoSaveKey) {
		t.Errorf("standalone launch edited retroarch.cfg:\n%s", raw)
	}
}

// TestRALocateReadsLaunchScripts: the wider RetroArch search (rafind.go) finds
// a RetroArch kept outside the usual folders through what a launch script
// names — a folder variable, $(dirname "$0") — and by walking the card, and it
// never takes a wrapper script named like RetroArch for the binary.
func TestRALocateReadsLaunchScripts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the handheld's layout")
	}
	prevDirs, prevRunning := raSystemDirs, raRunningBinaryFn
	raSystemDirs, raRunningBinaryFn = nil, func() string { return "" }
	t.Cleanup(func() { raSystemDirs, raRunningBinaryFn = prevDirs, prevRunning })
	elf := []byte("\x7fELF\x02\x01\x01")
	write := func(p string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	card := t.TempDir()
	bin := filepath.Join(card, "System", "RA", "ra64.trimui")
	write(bin, elf)
	write(filepath.Join(card, "Emus", "GBA", "launch.sh"),
		[]byte("#!/bin/sh\nRA_DIR=${RA_DIR:-"+filepath.ToSlash(filepath.Join(card, "System", "RA"))+"}\ncd \"$RA_DIR\"\n\"$RA_DIR\"/ra64.trimui -L x \"$1\"\n"))
	if loc := raLocateSearch([]string{card}); loc.Bin != bin {
		t.Fatalf("folder variable: found %q (%s), want %q", loc.Bin, loc.Via, bin)
	}

	card = t.TempDir()
	bin = filepath.Join(card, "Other", "retroarch64")
	write(bin, elf)
	write(filepath.Join(card, "Emus", "SFC", "launch.sh"),
		[]byte("#!/bin/sh\nHERE=$(dirname \"$0\")\nexec \"$HERE/../../Other/retroarch64\" \"$1\"\n"))
	if loc := raLocateSearch([]string{card}); loc.Bin != bin {
		t.Fatalf("dirname: found %q (%s), want %q", loc.Bin, loc.Via, bin)
	}

	card = t.TempDir()
	write(filepath.Join(card, "Tools", "retroarch"), []byte("#!/bin/sh\nexec /nowhere/ra64.trimui \"$@\"\n"))
	bin = filepath.Join(card, "Tools", "Emu", "RA", "ra64.trimui")
	write(bin, elf)
	write(filepath.Join(card, "Roms", "GBA", "ra64.trimui"), elf)
	if loc := raLocateSearch([]string{card}); loc.Bin != bin {
		t.Fatalf("card walk: found %q (%s), want %q", loc.Bin, loc.Via, bin)
	}
	if got := raCoresDirFor(bin); got != filepath.Join(filepath.Dir(bin), ".retroarch", "cores") {
		t.Errorf("cores folder = %q", got)
	}
}

// TestSessionSafeRecordAndEmulatorConfigs covers "Never lose a session"
// (sessionsafe.go): the record of a game closed for the battery survives a
// round trip and a damaged one is none; only a state the emulator loads by
// itself becomes a power-on resume; and the standalone emulators' own
// configs are read the way they write them.
func TestSessionSafeRecordAndEmulatorConfigs(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionSafeMarkerName)
	if _, ok := sessionSafeReadMarkerAt(path); ok {
		t.Fatal("a missing record was read")
	}
	want := sessionSafeMarker{Path: "/mnt/SDCARD/Roms/PSP/Game.iso", System: "PSP", Title: "Game",
		Launcher: "/mnt/SDCARD/Emus/PSP/launch.sh", Emulator: "ppsspp", Label: "PPSSPP",
		State: "/x/PSP/PPSSPP_STATE/ULUS10000_1.00_0.ppst", AutoLoad: true, Reason: "low battery"}
	if err := sessionSafeWriteMarkerAt(path, want); err != nil {
		t.Fatal(err)
	}
	if got, ok := sessionSafeReadMarkerAt(path); !ok || got != want {
		t.Fatalf("round trip: %+v (%t), want %+v", got, ok, want)
	}
	if qm, ok := sessionSafeQuickMarker(want); !ok || qm.Path != want.Path || qm.Launcher != want.Launcher {
		t.Fatalf("an auto-loaded state gave no power-on resume: %+v", qm)
	}
	manual := want
	manual.AutoLoad = false
	if _, ok := sessionSafeQuickMarker(manual); ok {
		t.Fatal("a state the emulator does not load by itself claimed a resume")
	}
	for _, bad := range []string{"{", `{"path":"/a.iso"}`, ""} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := sessionSafeReadMarkerAt(path); ok {
			t.Errorf("damaged record %q was read", bad)
		}
	}

	type flags struct{ save, load bool }
	for _, tc := range []struct {
		id, cfg string
		want    flags
	}{
		{"ppsspp", "[General]\r\nSaveStateOnExit = True\r\nAutoLoadSaveState = 2\r\n", flags{true, true}},
		{"ppsspp", "[General]\nAutoLoadSaveState = 0\n", flags{false, false}},
		{"drastic", "enable_autosavestate = 1\nautosavestate_slot = 10\n", flags{true, false}},
		{"flycast", "[config]\nDreamcast.AutoLoadState = yes\nDreamcast.AutoSaveState = yes\n", flags{true, true}},
		{"flycast", "[config]\nDreamcast.AutoSaveState = no\n", flags{false, false}},
		{"mednafen", ";Automatically load/save state\nautosave 1\n", flags{true, true}},
		{"duckstation", "", flags{true, false}},
		{"duckstation", "[Main]\nSaveStateOnExit = false\n", flags{false, false}},
	} {
		r, ok := sessionSafeRecipeFor(tc.id)
		if !ok || r.Flags == nil {
			t.Fatalf("no recipe for %s", tc.id)
		}
		if s, l := r.Flags(tc.cfg); (flags{s, l}) != tc.want {
			t.Errorf("%s %q: save %t load %t, want %+v", tc.id, tc.cfg, s, l, tc.want)
		}
	}
	if r, ok := sessionSafeRecipeFor("PPSSPPSDL_tg504", "/mnt/SDCARD/Emus/PSP/PPSSPPSDL_tg5040"); !ok || r.ID != "ppsspp" {
		t.Errorf("PPSSPP's TrimUI binary was not recognised (%t)", ok)
	}
	if _, _, class := sessionSafeClassify([]sessionSafeProc{{pid: 9, comm: "sh"}, {pid: 10, comm: "ra64.trimui"}}); class != sessionSafeRetroArch {
		t.Errorf("RetroArch in the group was not left to its own path: %v", class)
	}
	// A launch script named after its emulator is neither the emulator nor
	// signalled as one; only the emulator's own process is.
	procs := []sessionSafeProc{{pid: 9, comm: "PPSSPP.sh", argv: []string{"/mnt/SDCARD/Emus/PSP/PPSSPP.sh"}, exe: "/bin/busybox"},
		{pid: 12, comm: "PPSSPPSDL", exe: "/mnt/SDCARD/Emus/PSP/PPSSPPSDL", start: 77}}
	best, r, _ := sessionSafeClassify(procs)
	if r == nil || r.ID != "ppsspp" || best.pid != 12 {
		t.Fatalf("the launch script was taken for the emulator: pid %d", best.pid)
	}
	if got := sessionSafeTargets(procs, r); len(got) != 1 || got[0] != (sessionSafeTarget{pid: 12, start: 77}) {
		t.Errorf("targets = %+v, want the emulator only", got)
	}
}
