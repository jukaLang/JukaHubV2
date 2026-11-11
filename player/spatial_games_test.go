package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// spatialGamesFixture is a RetroArch install, a plugin and a data folder in a
// temporary tree, with every seam pointed at them and the launch journal kept
// there too (raScopeTestSetup).
type spatialGamesFixture struct {
	root, raDir, cfg, bin, plugin, data, launcher string
}

// spatialBinBody is a stand-in RetroArch binary: filler with the two support
// strings where a real build keeps them.
func spatialBinBody(setting, dylib bool) []byte {
	var b bytes.Buffer
	b.WriteString("\x7fELF stand-in")
	b.Write(bytes.Repeat([]byte{0xAB}, 4096))
	if setting {
		b.WriteString("\x00audio_dsp_plugin\x00")
	}
	b.Write(bytes.Repeat([]byte{0xCD}, 4096))
	if dylib {
		b.WriteString("\x00dspfilter_get_implementation\x00")
	}
	b.Write(bytes.Repeat([]byte{0xEF}, 512))
	return b.Bytes()
}

// spatialGamesBaseConfig is the user's retroarch.cfg before any launch.
const spatialGamesBaseConfig = "video_driver = \"gl\"\n"

func spatialGamesTestSetup(t *testing.T, bin []byte, custom map[string]interface{}) spatialGamesFixture {
	t.Helper()
	raScopeTestSetup(t)
	root := t.TempDir()
	f := spatialGamesFixture{root: root, raDir: filepath.Join(root, "RetroArch")}
	f.cfg = filepath.Join(f.raDir, "retroarch.cfg")
	raScopeWrite(t, f.cfg, spatialGamesBaseConfig)
	if bin != nil {
		f.bin = filepath.Join(f.raDir, "ra64.trimui")
		raScopeWrite(t, f.bin, string(bin))
	}
	f.plugin = filepath.Join(root, "app", "lib", spatialPluginFileName())
	raScopeWrite(t, f.plugin, "plugin build one")
	f.data = filepath.Join(root, "data")
	f.launcher = filepath.Join(root, "Emus", "SFC", "launch.sh")
	raScopeWrite(t, f.launcher, "#!/bin/sh\ncd /mnt/SDCARD/RetroArch\n./ra64.trimui -L snes9x_libretro.so \"$1\"\n")

	prevCandidates, prevLocate, prevData, prevLook, prevScan := spatialRetroArchCandidates, spatialPluginLocate, spatialDataDir, spatialLookPath, spatialScan
	prevNow, prevWin := spatialNow, spatialWinRetroArch
	spatialRetroArchCandidates = func() []string { return []string{f.cfg} }
	spatialPluginLocate = func() string {
		if regularFile(f.plugin) {
			return f.plugin
		}
		return ""
	}
	spatialDataDir = func() (string, error) { return f.data, nil }
	spatialLookPath = nil
	spatialWinRetroArch = func() string { return "" }
	spatialProbeReset()
	prevConfig := appConfig
	if custom == nil {
		custom = map[string]interface{}{
			spatialGamesKey:    true,
			spatialModeKey:     "headphones",
			spatialStrengthKey: 75,
			spatialRoomKey:     "large",
		}
	}
	appConfig = &Config{Variables: Variables{Custom: custom}}
	t.Cleanup(func() {
		// A background probe may still be writing into the temporary tree.
		spatialProbeWG.Wait()
		spatialRetroArchCandidates, spatialPluginLocate, spatialDataDir, spatialLookPath, spatialScan = prevCandidates, prevLocate, prevData, prevLook, prevScan
		spatialNow, spatialWinRetroArch = prevNow, prevWin
		appConfig = prevConfig
		spatialProbeReset()
	})
	return f
}

// spatialProbeReset forgets every probe and the panel's config cache, as a
// fresh start would (the cache file on the card stays).
func spatialProbeReset() {
	spatialProbe.mu.Lock()
	spatialProbe.known = nil
	spatialProbe.running = nil
	spatialProbe.loaded = false
	spatialProbe.mu.Unlock()
	spatialLogged.mu.Lock()
	spatialLogged.seen = nil
	spatialLogged.mu.Unlock()
	spatialOwnFilterCache.mu.Lock()
	spatialOwnFilterCache.path, spatialOwnFilterCache.size, spatialOwnFilterCache.modNano, spatialOwnFilterCache.value = "", 0, 0, ""
	spatialOwnFilterCache.mu.Unlock()
}

// presetPath is where the fixture's launches write the preset.
func (f spatialGamesFixture) presetPath() string {
	return filepath.Join(f.data, spatialGamesDirName, spatialPresetName)
}

func TestSpatialPresetText(t *testing.T) {
	text := spatialPresetText(spatialDSPHeadphones, spatialDSPRoomLarge, 0.75)
	for key, want := range map[string]string{
		"filters":            "1",
		"filter0":            "jhspatial",
		"jhspatial_mode":     "1",
		"jhspatial_strength": "0.75",
		"jhspatial_room":     "2",
	} {
		if got, ok := raConfigGet(text, key); !ok || got != want {
			t.Errorf("%s = %q (found %v), want %q\n%s", key, got, ok, want, text)
		}
	}
	if got, _ := raConfigGet(spatialPresetText(2, 0, 1.7), "jhspatial_strength"); got != "1.00" {
		t.Errorf("strength 1.7 written as %q, want it clamped to 1.00", got)
	}
	if got, _ := raConfigGet(spatialPresetText(2, 0, -1), "jhspatial_strength"); got != "0.00" {
		t.Errorf("strength -1 written as %q, want 0.00", got)
	}
}

func TestSpatialScanBinaryFindsStringsAcrossChunks(t *testing.T) {
	dir := t.TempDir()
	// The entry-point name straddles the 1 MiB chunk boundary.
	body := bytes.Repeat([]byte{0x11}, 3<<20)
	copy(body[100:], "audio_dsp_plugin")
	at := (1 << 20) - 10
	copy(body[at:], "\x00dspfilter_get_implementation\x00")
	both := filepath.Join(dir, "both")
	if err := os.WriteFile(both, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if setting, dylib, err := spatialScanBinary(both); err != nil || !setting || !dylib {
		t.Fatalf("scan = %v, %v, %v; want both found", setting, dylib, err)
	}
	// A built-in filter's symbol name contains the entry point without the
	// NUL in front: that is not dynamic loading.
	builtin := filepath.Join(dir, "builtin")
	if err := os.WriteFile(builtin, []byte("xx audio_dsp_plugin\x00echo_dspfilter_get_implementation\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if setting, dylib, err := spatialScanBinary(builtin); err != nil || !setting || dylib {
		t.Fatalf("built-in scan = %v, %v, %v; want setting only", setting, dylib, err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if setting, dylib, err := spatialScanBinary(empty); err != nil || setting || dylib {
		t.Fatalf("empty scan = %v, %v, %v", setting, dylib, err)
	}
	if _, _, err := spatialScanBinary(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing file scanned without an error")
	}
}

func TestSpatialProbeSaysNo(t *testing.T) {
	// Only "the setting without the library loader" is a confident no.
	if no, why := spatialProbeSaysNo(spatialProbeEntry{Setting: true}); !no || !strings.Contains(why, "built-in") {
		t.Errorf("built-in filters only = %v %q; want a refusal with the reason", no, why)
	}
	for _, e := range []spatialProbeEntry{
		{Setting: true, Dylib: true}, // full support
		{},                           // neither string: a wrapper script, a packed binary
		{Dylib: true},                // odd, but proves nothing
		{Setting: true, Err: "read error"},
	} {
		if no, why := spatialProbeSaysNo(e); no || why != "" {
			t.Errorf("%+v = %v %q; want no refusal", e, no, why)
		}
	}
}

func TestSpatialFilterSupportCachesPerBinary(t *testing.T) {
	f := spatialGamesTestSetup(t, spatialBinBody(true, true), nil)
	scans := 0
	spatialScan = func(path string) (bool, bool, error) {
		scans++
		return spatialScanBinary(path)
	}
	if e, known := spatialFilterSupport(f.bin, true); !known || !e.Setting || !e.Dylib {
		t.Fatalf("first probe = %+v known=%v", e, known)
	}
	if _, known := spatialFilterSupport(f.bin, false); !known || scans != 1 {
		t.Fatalf("second ask: known=%v after %d scans; want the cached answer", known, scans)
	}
	// A fresh start reads the answer from the card instead of scanning again.
	spatialProbeReset()
	if e, known := spatialFilterSupport(f.bin, false); !known || !e.Dylib || scans != 1 {
		t.Fatalf("after a restart: %+v known=%v scans=%d; want the remembered answer", e, known, scans)
	}
	// A replaced binary is probed again.
	raScopeWrite(t, f.bin, string(spatialBinBody(true, false))+"longer")
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(f.bin, future, future); err != nil {
		t.Fatal(err)
	}
	if e, known := spatialFilterSupport(f.bin, true); !known || e.Dylib || scans != 2 {
		t.Fatalf("after an update: %+v known=%v scans=%d; want a new probe without dynamic filters", e, known, scans)
	}
}

func TestSpatialFilterSupportUnreadableBinary(t *testing.T) {
	f := spatialGamesTestSetup(t, spatialBinBody(true, true), nil)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	spatialNow = func() time.Time { return now }
	scans := 0
	spatialScan = func(string) (bool, bool, error) {
		scans++
		return false, false, os.ErrPermission
	}
	e, known := spatialFilterSupport(f.bin, true)
	if !known || e.Err == "" {
		t.Fatalf("an unreadable binary = %+v known=%v; want a remembered read error", e, known)
	}
	if no, _ := spatialProbeSaysNo(e); no {
		t.Fatal("a read error was taken as a confident no")
	}
	// The error stands for a while (no probe on every ask) ...
	now = now.Add(spatialProbeErrRetry / 2)
	if _, known := spatialFilterSupport(f.bin, false); !known || scans != 1 {
		t.Fatalf("within the retry time: known=%v scans=%d; want the remembered error", known, scans)
	}
	// ... then the next ask probes again.
	now = now.Add(spatialProbeErrRetry)
	spatialScan = func(path string) (bool, bool, error) {
		scans++
		return spatialScanBinary(path)
	}
	if e, known := spatialFilterSupport(f.bin, true); !known || !e.Dylib || e.Err != "" || scans != 2 {
		t.Fatalf("after the retry time: %+v known=%v scans=%d; want a real probe", e, known, scans)
	}
	// A read error is never written to the card: a fresh start probes again.
	spatialScan = func(string) (bool, bool, error) {
		scans++
		return false, false, errors.New("card busy")
	}
	raScopeWrite(t, f.bin, string(spatialBinBody(true, true))+"v2")
	if _, known := spatialFilterSupport(f.bin, true); !known {
		t.Fatal("no answer for the replaced binary")
	}
	spatialProbeReset()
	spatialScan = spatialScanBinary
	if e, known := spatialFilterSupport(f.bin, true); !known || !e.Dylib || e.Err != "" {
		t.Fatalf("after a restart: %+v known=%v; want a real probe", e, known)
	}
}

func TestSpatialGamesAvailability(t *testing.T) {
	f := spatialGamesTestSetup(t, spatialBinBody(true, true), nil)
	// The first ask starts the probe in the background and says so with a
	// reason that starts with "Checking", which the panel shows as a pending
	// state (spatial_ui.go), not as "not available".
	if ok, why := spatialGamesAvailability(); ok || !strings.HasPrefix(why, "Checking") {
		t.Fatalf("first answer = %v %q; want the probe running", ok, why)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, known := spatialFilterSupport(f.bin, false); known {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ok, why := spatialGamesAvailability(); !ok {
		t.Fatalf("full support = %v %q", ok, why)
	}

	// A RetroArch with only built-in filters.
	raScopeWrite(t, f.bin, string(spatialBinBody(true, false)))
	future := time.Now().Add(3 * time.Hour)
	if err := os.Chtimes(f.bin, future, future); err != nil {
		t.Fatal(err)
	}
	if _, known := spatialFilterSupport(f.bin, true); !known {
		t.Fatal("probe of the built-in build failed")
	}
	if ok, why := spatialGamesAvailability(); ok || !strings.Contains(why, "built-in") {
		t.Fatalf("built-in filters only = %v %q", ok, why)
	}

	// A binary with neither string (a wrapper, a packed binary) proves nothing.
	raScopeWrite(t, f.bin, string(spatialBinBody(false, false)))
	later := future.Add(time.Hour)
	if err := os.Chtimes(f.bin, later, later); err != nil {
		t.Fatal(err)
	}
	if _, known := spatialFilterSupport(f.bin, true); !known {
		t.Fatal("probe of the stringless binary failed")
	}
	if ok, why := spatialGamesAvailability(); !ok {
		t.Fatalf("a binary without either string = %v %q; want usable", ok, why)
	}

	// The user's own filter: the row says so.
	raScopeWrite(t, f.cfg, spatialGamesBaseConfig+"audio_dsp_plugin = \"/mnt/SDCARD/RetroArch/filters/audio/EQ.dsp\"\n")
	if ok, why := spatialGamesAvailability(); ok || !strings.Contains(why, "EQ.dsp") {
		t.Fatalf("own filter = %v %q; want it named", ok, why)
	}
	// JukaHub's own preset there is not the user's.
	raScopeWrite(t, f.cfg, spatialGamesBaseConfig+"audio_dsp_plugin = \""+filepath.ToSlash(f.presetPath())+"\"\n")
	if ok, why := spatialGamesAvailability(); !ok {
		t.Fatalf("JukaHub's own preset counted as the user's filter: %v %q", ok, why)
	}
	raScopeWrite(t, f.cfg, spatialGamesBaseConfig)

	// No plugin.
	if err := os.Remove(f.plugin); err != nil {
		t.Fatal(err)
	}
	if ok, why := spatialGamesAvailability(); ok || why == "" {
		t.Fatalf("no plugin = %v %q", ok, why)
	}

	// No RetroArch.
	raScopeWrite(t, f.plugin, "plugin")
	spatialRetroArchCandidates = func() []string { return []string{filepath.Join(f.root, "none", "retroarch.cfg")} }
	if ok, why := spatialGamesAvailability(); ok || !strings.Contains(why, "RetroArch was not found") {
		t.Fatalf("no RetroArch = %v %q", ok, why)
	}
}

func TestPrepareSpatialAudioSetsThePresetForOneLaunch(t *testing.T) {
	f := spatialGamesTestSetup(t, spatialBinBody(true, true), nil)
	if _, known := spatialFilterSupport(f.bin, true); !known {
		t.Fatal("probe failed")
	}
	if !prepareSpatialAudio(f.launcher) {
		t.Fatal("prepareSpatialAudio did not configure RetroArch")
	}
	preset := f.presetPath()
	cfg := raScopeRead(t, f.cfg)
	if got, ok := raConfigGet(cfg, spatialDSPConfigKey); !ok || got != filepath.ToSlash(preset) {
		t.Fatalf("audio_dsp_plugin = %q (%v), want %q\n%s", got, ok, filepath.ToSlash(preset), cfg)
	}
	text := raScopeRead(t, preset)
	for key, want := range map[string]string{"jhspatial_mode": "1", "jhspatial_room": "2", "jhspatial_strength": "0.75"} {
		if got, _ := raConfigGet(text, key); got != want {
			t.Errorf("preset %s = %q, want %q", key, got, want)
		}
	}
	copied := raScopeRead(t, filepath.Join(f.data, spatialGamesDirName, spatialPluginFileName()))
	if copied != "plugin build one" {
		t.Errorf("the plugin beside the preset holds %q", copied)
	}
	// The game exits: the journal puts the user's config back as it was.
	raScopeRestoreNow(t)
	if got := raScopeRead(t, f.cfg); got != spatialGamesBaseConfig {
		t.Fatalf("after the game the config is\n%s", got)
	}
}

func TestPrepareSpatialAudioKeepsTheUsersOwnFilter(t *testing.T) {
	f := spatialGamesTestSetup(t, spatialBinBody(true, true), nil)
	own := spatialGamesBaseConfig + "audio_dsp_plugin = \"/mnt/SDCARD/RetroArch/filters/audio/EQ.dsp\"\n"
	raScopeWrite(t, f.cfg, own)
	if prepareSpatialAudio(f.launcher) {
		t.Fatal("prepareSpatialAudio replaced the user's own filter")
	}
	if got := raScopeRead(t, f.cfg); got != own {
		t.Fatalf("config changed:\n%s", got)
	}
	// Nothing is staged when no config will use the preset.
	if fileExists(f.presetPath()) {
		t.Fatal("the preset was written although no config takes it")
	}
}

func TestPrepareSpatialAudioReplacesALeftoverPreset(t *testing.T) {
	cases := []struct {
		name, leftover string
	}{
		{"same path", ""}, // filled in below: the path this launch writes
		{"another folder", "/mnt/SDCARD/Apps/JukaHub-old/data/spatial-audio/JukaHub-Spatial.dsp"},
		{"Windows spelling", `C:\JukaHub\data\spatial-audio\JukaHub-Spatial.dsp`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := spatialGamesTestSetup(t, spatialBinBody(true, true), nil)
			leftover := tc.leftover
			if leftover == "" {
				leftover = filepath.ToSlash(f.presetPath())
			}
			// A lost journal left JukaHub's value in the config.
			raScopeWrite(t, f.cfg, spatialGamesBaseConfig+"audio_dsp_plugin = \""+leftover+"\"\n")
			if !prepareSpatialAudio(f.launcher) {
				t.Fatal("JukaHub's own leftover preset blocked the launch's preset")
			}
			if got, _ := raConfigGet(raScopeRead(t, f.cfg), spatialDSPConfigKey); got != filepath.ToSlash(f.presetPath()) {
				t.Fatalf("audio_dsp_plugin = %q during the game", got)
			}
			// The game exits: the leftover is not put back — the user had none.
			raScopeRestoreNow(t)
			if got := raScopeRead(t, f.cfg); got != spatialGamesBaseConfig {
				t.Fatalf("after the game the config is\n%s", got)
			}
		})
	}
}

func TestPrepareSpatialAudioClearsALeftoverWhenOff(t *testing.T) {
	cases := []struct {
		name   string
		custom map[string]interface{}
		mutate func(f spatialGamesFixture)
	}{
		{name: "games off", custom: map[string]interface{}{spatialModeKey: "headphones"}},
		{name: "mode off", custom: map[string]interface{}{spatialGamesKey: true, spatialModeKey: "off"}},
		{name: "no plugin", mutate: func(f spatialGamesFixture) { _ = os.Remove(f.plugin) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := spatialGamesTestSetup(t, spatialBinBody(true, true), tc.custom)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			raScopeWrite(t, f.cfg, spatialGamesBaseConfig+"audio_dsp_plugin = \"/somewhere/spatial-audio/JukaHub-Spatial.dsp\"\n")
			if prepareSpatialAudio(f.launcher) {
				t.Fatal("prepareSpatialAudio configured RetroArch")
			}
			if got := raScopeRead(t, f.cfg); got != spatialGamesBaseConfig {
				t.Fatalf("JukaHub's leftover preset stayed:\n%s", got)
			}
			// The user's own filter is never touched.
			own := spatialGamesBaseConfig + "audio_dsp_plugin = \"/mnt/SDCARD/RetroArch/filters/audio/EQ.dsp\"\n"
			raScopeWrite(t, f.cfg, own)
			prepareSpatialAudio(f.launcher)
			if got := raScopeRead(t, f.cfg); got != own {
				t.Fatalf("the user's filter changed:\n%s", got)
			}
		})
	}
}

func TestPrepareSpatialAudioSkips(t *testing.T) {
	cases := []struct {
		name   string
		custom map[string]interface{}
		bin    []byte
		probe  bool
		mutate func(f spatialGamesFixture)
	}{
		{name: "games off", custom: map[string]interface{}{spatialModeKey: "headphones"}, bin: spatialBinBody(true, true)},
		{name: "mode off", custom: map[string]interface{}{spatialGamesKey: true, spatialModeKey: "off"}, bin: spatialBinBody(true, true)},
		{name: "strength 0", custom: map[string]interface{}{spatialGamesKey: true, spatialModeKey: "speakers", spatialStrengthKey: "0"}, bin: spatialBinBody(true, true)},
		{name: "not RetroArch", bin: spatialBinBody(true, true), mutate: func(f spatialGamesFixture) {
			if err := os.WriteFile(f.launcher, []byte("#!/bin/sh\n./PPSSPPSDL \"$1\"\n"), 0o755); err != nil {
				panic(err)
			}
		}},
		{name: "built-in filters only", bin: spatialBinBody(true, false), probe: true},
		{name: "no plugin", bin: spatialBinBody(true, true), mutate: func(f spatialGamesFixture) { _ = os.Remove(f.plugin) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := spatialGamesTestSetup(t, tc.bin, tc.custom)
			if tc.probe {
				if _, known := spatialFilterSupport(f.bin, true); !known {
					t.Fatal("probe failed")
				}
			}
			if tc.mutate != nil {
				tc.mutate(f)
			}
			if prepareSpatialAudio(f.launcher) {
				t.Fatal("prepareSpatialAudio configured RetroArch")
			}
			if got := raScopeRead(t, f.cfg); got != spatialGamesBaseConfig {
				t.Fatalf("config changed:\n%s", got)
			}
			if fileExists(f.presetPath()) {
				t.Fatal("a preset was written for a launch that does not use it")
			}
		})
	}
}

func TestPrepareSpatialAudioTriesAStringlessBinary(t *testing.T) {
	// A RetroArch whose binary holds neither probe string (a wrapper script, a
	// packed binary) proves nothing: the launch still gets the preset.
	f := spatialGamesTestSetup(t, spatialBinBody(false, false), nil)
	if _, known := spatialFilterSupport(f.bin, true); !known {
		t.Fatal("probe failed")
	}
	if !prepareSpatialAudio(f.launcher) {
		t.Fatal("a stringless RetroArch binary blocked the preset")
	}
}

func TestPrepareSpatialAudioWithAnUnprobedRetroArch(t *testing.T) {
	// The probe has not answered yet: the launch still gets the preset (a
	// RetroArch that cannot load it plays normally), and the probe starts.
	f := spatialGamesTestSetup(t, spatialBinBody(true, true), nil)
	if !prepareSpatialAudio(f.launcher) {
		t.Fatal("an unprobed RetroArch did not get the preset")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, known := spatialFilterSupport(f.bin, false); known {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the launch did not start a probe")
}

func TestSpatialStageCopiesThePluginOnlyWhenItChanged(t *testing.T) {
	f := spatialGamesTestSetup(t, nil, nil)
	if _, err := spatialStage(f.plugin, 1, 1, 0.5); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(f.data, spatialGamesDirName, spatialPluginFileName())
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(dst, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := spatialStage(f.plugin, 2, 0, 1); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dst); err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("an identical plugin was copied again (%v)", err)
	}
	raScopeWrite(t, f.plugin, "plugin build two")
	if _, err := spatialStage(f.plugin, 2, 0, 1); err != nil {
		t.Fatal(err)
	}
	if got := raScopeRead(t, dst); got != "plugin build two" {
		t.Fatalf("an updated plugin was not copied: %q", got)
	}
	if entries, err := os.ReadDir(filepath.Dir(dst)); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Errorf("a temporary file was left behind: %s", e.Name())
			}
		}
	}
	// No data folder: a plain error, not "<nil>".
	spatialDataDir = func() (string, error) { return "", nil }
	if _, err := spatialStage(f.plugin, 1, 1, 0.5); err == nil || strings.Contains(err.Error(), "nil") {
		t.Fatalf("no data folder = %v", err)
	}
}

func TestSpatialIsOurPreset(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"/mnt/SDCARD/Apps/JukaHub/data/spatial-audio/JukaHub-Spatial.dsp", true},
		{`C:\JukaHub\data\spatial-audio\jukahub-spatial.dsp`, true},
		{"/mnt/SDCARD/RetroArch/filters/audio/EQ.dsp", false},
		{"", false},
		{"JukaHub-Spatial.dsp.old", false},
	}
	for _, tc := range cases {
		if got := spatialIsOurPreset(tc.value); got != tc.want {
			t.Errorf("spatialIsOurPreset(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestSpatialRetroArchBinaryFor(t *testing.T) {
	f := spatialGamesTestSetup(t, spatialBinBody(true, true), nil)
	if got := spatialRetroArchBinaryFor("", f.cfg); got != f.bin {
		t.Errorf("binary beside the config = %q, want %q", got, f.bin)
	}
	// A file named retroarch beside TrimUI's own build (often a wrapper
	// script) does not win over ra64.trimui.
	raScopeWrite(t, filepath.Join(f.raDir, "retroarch"), "#!/bin/sh\nexec ./ra64.trimui \"$@\"\n")
	if got := spatialRetroArchBinaryFor("", f.cfg); got != f.bin {
		t.Errorf("with a retroarch wrapper beside it = %q, want %q", got, f.bin)
	}
	direct := filepath.Join(f.root, "bin", "retroarch")
	if got := spatialRetroArchBinaryFor(direct, f.cfg); got != direct {
		t.Errorf("a direct RetroArch launch = %q, want the launcher", got)
	}
	if got := spatialRetroArchBinaryFor("", filepath.Join(f.root, "nowhere", "retroarch.cfg")); got != "" {
		t.Errorf("no binary anywhere = %q", got)
	}
}
