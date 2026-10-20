package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func writeChiakiTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestSelectChiakiReleasePicksInstallZip(t *testing.T) {
	assets := []chiakiAsset{
		{Name: "Source code (zip)", URL: "https://github.com/example/source.zip"},
		{Name: "trimui-chiaki-ng-v0.3.40.zip", URL: "https://github.com/example/app.zip", Size: 123, Digest: "sha256:" + strings.Repeat("a", 64)},
		{Name: "trimui-chiaki-ng-v0.3.40.zip.sha256", URL: "https://github.com/example/app.zip.sha256"},
	}
	rel, err := selectChiakiRelease("v0.3.40", assets)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if rel.ZipName != "trimui-chiaki-ng-v0.3.40.zip" || rel.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("release = %+v", rel)
	}
	if rel.Size != 123 || rel.Tag != "v0.3.40" {
		t.Fatalf("release = %+v", rel)
	}
}

func TestSelectChiakiReleaseRejectsSourceOnly(t *testing.T) {
	_, err := selectChiakiRelease("v1", []chiakiAsset{{Name: "Source code (zip)"}})
	if err == nil {
		t.Fatal("source-only release should be rejected")
	}
}

func TestFindChiakiPackageRootToleratesAppAndApps(t *testing.T) {
	for _, dir := range []string{"Apps/Chiaki", "App/Chiaki", "Chiaki"} {
		root := t.TempDir()
		pkg := filepath.Join(root, filepath.FromSlash(dir))
		writeChiakiTestFile(t, filepath.Join(pkg, "launch.sh"), "#!/bin/sh\n")
		writeChiakiTestFile(t, filepath.Join(pkg, "app.py"), "print('ok')\n")
		got, err := findChiakiPackageRoot(root)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if got != pkg {
			t.Fatalf("%s: got %s, want %s", dir, got, pkg)
		}
	}
}

func TestInstallChiakiTreePreservesSettingsAndPairing(t *testing.T) {
	sd := t.TempDir()
	target := filepath.Join(sd, "Apps", "Chiaki")
	writeChiakiTestFile(t, filepath.Join(target, "launch.sh"), "old launcher\n")
	writeChiakiTestFile(t, filepath.Join(target, "settings.json"), `{"language":"vi"}`+"\n")
	writeChiakiTestFile(t, filepath.Join(target, "paired_hosts.json"), `{"host":"ps4"}`+"\n")

	staged := t.TempDir()
	writeChiakiTestFile(t, filepath.Join(staged, "launch.sh"), "#!/bin/sh\necho new\n")
	writeChiakiTestFile(t, filepath.Join(staged, "app.py"), "print('new')\n")
	writeChiakiTestFile(t, filepath.Join(staged, "config.json"), "{}\n")
	writeChiakiTestFile(t, filepath.Join(staged, "settings.json"), `{"language":"en"}`+"\n")
	writeChiakiTestFile(t, filepath.Join(staged, "bin", "chiaki-stream"), "binary\n")
	writeChiakiTestFile(t, filepath.Join(staged, "libs", "brick-stock", "libSDL2-2.0.so.0"), "lib\n")

	rel := &chiakiRelease{Tag: "v0.3.40"}
	if err := installChiakiTree(staged, target, rel); err != nil {
		t.Fatalf("install: %v", err)
	}

	if got, _ := os.ReadFile(filepath.Join(target, "launch.sh")); string(got) != "#!/bin/sh\necho new\n" {
		t.Fatalf("launch.sh = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "settings.json")); string(got) != `{"language":"vi"}`+"\n" {
		t.Fatalf("settings should be preserved, got %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "paired_hosts.json")); string(got) != `{"host":"ps4"}`+"\n" {
		t.Fatalf("pairing should be preserved, got %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(target, chiakiVersionFile)); strings.TrimSpace(string(got)) != "v0.3.40" {
		t.Fatalf("version = %q", got)
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Fatalf("backup left behind: %v", err)
	}
}

func TestInstallChiakiTreeRejectsIncompletePackage(t *testing.T) {
	staged := t.TempDir()
	writeChiakiTestFile(t, filepath.Join(staged, "launch.sh"), "#!/bin/sh\n")
	if err := installChiakiTree(staged, filepath.Join(t.TempDir(), "Apps", "Chiaki"), nil); err == nil {
		t.Fatal("package without app.py should be rejected")
	}
}

func TestChiakiLaunchEnvUsesBundledRuntime(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Apps", "Chiaki")
	env := chiakiLaunchEnv(app, []string{"PATH=/bin"})
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"HOME=" + app,
		filepath.Join(app, "libs"),
		"CLR_OPENSSL_VERSION_OVERRIDE=1.1",
		"PATH=" + filepath.Join(app, "bin") + ":",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment missing %q:\n%s", want, joined)
		}
	}
}

func TestChiakiPreservedFilesAreTheUserStateOnes(t *testing.T) {
	for _, name := range []string{"settings.json", "paired_hosts.json", "chiaki.conf"} {
		if !chiakiPreservedNames[name] {
			t.Errorf("%s must survive an update", name)
		}
	}
}

func TestStartMenuHasPSRemotePlay(t *testing.T) {
	found := false
	for _, app := range getStartMenuApps(&Config{}) {
		if app.name == "PS Remote Play" {
			found = true
			if app.action == nil {
				t.Fatal("PS Remote Play has no action")
			}
		}
	}
	if !found {
		t.Fatal("Start menu is missing PS Remote Play")
	}
}

func TestAppsListHasPSRemotePlayAction(t *testing.T) {
	found := false
	for _, entry := range builtinAppEntries() {
		if entry.Name == "PS Remote Play (built-in)" {
			found = true
			if entry.Path != builtinActionPrefix+"chiaki" {
				t.Fatalf("path = %q", entry.Path)
			}
		}
	}
	if !found {
		t.Fatal("Apps list is missing the PS Remote Play applet")
	}
}

// withChiakiSDRoot points the installer at a temp SD card. chiakiSDRootDir
// honours JUKAHUB_SD_ROOT first, so this works on any test platform.
func withChiakiSDRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("JUKAHUB_SD_ROOT", root)
	return root
}

func chiakiRowByLabel(rows []chiakiRow, label string) (chiakiRow, bool) {
	for _, row := range rows {
		if row.label == label {
			return row, true
		}
	}
	return chiakiRow{}, false
}

func TestChiakiPanelOffersPS5BetaInstall(t *testing.T) {
	withChiakiSDRoot(t)
	rows := chiakiRows()

	row, ok := chiakiRowByLabel(rows, "Install PS5 beta")
	if !ok {
		t.Fatalf("no PS5 install row: %+v", rows)
	}
	if row.action != chiakiActionInstallPS5 {
		t.Fatalf("PS5 row action = %v, want install", row.action)
	}
	if !strings.Contains(row.detail, chiakiPS5Tag) {
		t.Fatalf("PS5 row should name the beta tag: %+v", row)
	}
	if _, ok := chiakiRowByLabel(rows, "PS5 setup"); !ok {
		t.Fatalf("no PS5 setup guide row: %+v", rows)
	}
}

func TestChiakiPanelOffersLaunchOncePS5IsInstalled(t *testing.T) {
	root := withChiakiSDRoot(t)
	app := filepath.Join(root, "Apps", "Chiaki-PS5")
	writeChiakiTestFile(t, filepath.Join(app, "launch.sh"), "#!/bin/sh\n")
	writeChiakiTestFile(t, filepath.Join(app, "app.py"), "print('ps5')\n")

	rows := chiakiRows()
	launch, ok := chiakiRowByLabel(rows, "Launch PS5 beta")
	if !ok || launch.action != chiakiActionLaunchPS5 {
		t.Fatalf("launch row = %+v (ok=%v)", launch, ok)
	}
	if _, ok := chiakiRowByLabel(rows, "Reinstall PS5 beta"); !ok {
		t.Fatalf("no reinstall row: %+v", rows)
	}
	if _, ok := chiakiRowByLabel(rows, "Install PS5 beta"); ok {
		t.Fatal("install row should disappear once the beta is installed")
	}
}

// Informational rows used to share the zero value of chiakiActionInstall, so
// pressing A on "PS5 status" silently started a download.
func TestChiakiInfoRowsDoNotTriggerInstall(t *testing.T) {
	withChiakiSDRoot(t)
	for _, row := range chiakiRows() {
		if row.info && row.action != chiakiActionNone {
			t.Fatalf("info row %q has action %v, want none", row.label, row.action)
		}
	}
}

func TestParseChiakiManifest(t *testing.T) {
	good := func() map[string]interface{} {
		files := []map[string]interface{}{}
		for i := 0; i < chiakiPS5MinFiles; i++ {
			files = append(files, map[string]interface{}{
				"path":   "f" + string(rune('a'+i)) + ".py",
				"sha256": strings.Repeat("b", 64),
				"size":   10,
			})
		}
		return map[string]interface{}{
			"version": "0.4.0-beta2",
			"note":    map[string]string{"en": "ps5 account id"},
			"files":   files,
		}
	}

	data, _ := json.Marshal(good())
	m, err := parseChiakiManifest(data)
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if m.Version != "0.4.0-beta2" || len(m.Files) != chiakiPS5MinFiles {
		t.Fatalf("parsed = %+v", m)
	}

	bad := func(mutate func(map[string]interface{})) []byte {
		doc := good()
		mutate(doc)
		b, _ := json.Marshal(doc)
		return b
	}
	files := func(doc map[string]interface{}) []map[string]interface{} {
		return doc["files"].([]map[string]interface{})
	}

	cases := map[string][]byte{
		"no version":     bad(func(d map[string]interface{}) { d["version"] = "" }),
		"too few files":  bad(func(d map[string]interface{}) { d["files"] = files(d)[:2] }),
		"absolute path":  bad(func(d map[string]interface{}) { files(d)[0]["path"] = "/etc/passwd" }),
		"parent escape":  bad(func(d map[string]interface{}) { files(d)[0]["path"] = "../escape.py" }),
		"bad digest":     bad(func(d map[string]interface{}) { files(d)[0]["sha256"] = "zz" }),
		"missing digest": bad(func(d map[string]interface{}) { files(d)[0]["sha256"] = "" }),
		"zero size":      bad(func(d map[string]interface{}) { files(d)[0]["size"] = 0 }),
		"huge size":      bad(func(d map[string]interface{}) { files(d)[0]["size"] = chiakiPS5MaxExpand + 1 }),
	}
	for name, data := range cases {
		if _, err := parseChiakiManifest(data); err == nil {
			t.Errorf("%s: manifest was accepted", name)
		}
	}

	if _, err := parseChiakiManifest([]byte("not json")); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
}

func TestChiakiManifestDetectsPS5Module(t *testing.T) {
	m := &chiakiManifest{Version: "0.4.0-beta2"}
	m.Files = append(m.Files, struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}{Path: "app.py", SHA256: strings.Repeat("a", 64), Size: 1})
	if m.supportsPS5() {
		t.Fatal("a manifest without the PS5 module must not claim PS5 support")
	}
	m.Files = append(m.Files, struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}{Path: chiakiPS5MarkModule, SHA256: strings.Repeat("a", 64), Size: 1})
	if !m.supportsPS5() {
		t.Fatal("manifest with rh/ps5_regist.py must report PS5 support")
	}
	var nilManifest *chiakiManifest
	if nilManifest.supportsPS5() {
		t.Fatal("nil manifest must not report PS5 support")
	}
}

// buildChiakiTagTree writes a fake extracted tag: <top>/manifest.json + files/.
func buildChiakiTagTree(t *testing.T, top, tag string, includePS5 bool) {
	t.Helper()
	files := map[string]string{
		"app.py":            "print('app')\n",
		"launch.sh":         "#!/bin/sh\n",
		"config.json":       "{}\n",
		"icon.png":          "png",
		"bin/chiaki-stream": "bin",
		"bin/chiaki-regist": "bin",
		"rh/engine.py":      "engine\n",
		"rh/i18n.py":        "i18n\n",
	}
	if includePS5 {
		files[chiakiPS5MarkModule] = "ps5 regist\n"
	}
	type entry struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	var list []entry
	for rel, body := range files {
		path := filepath.Join(top, "files", filepath.FromSlash(rel))
		writeChiakiTestFile(t, path, body)
		sum := sha256.Sum256([]byte(body))
		list = append(list, entry{Path: rel, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))})
	}
	doc := map[string]interface{}{
		"version": strings.TrimPrefix(tag, "v"),
		"files":   list,
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeChiakiTestFile(t, filepath.Join(top, "manifest.json"), string(data))
}

func TestInstallChiakiFromTreeVerifiesAndInstalls(t *testing.T) {
	unpacked := t.TempDir()
	top := filepath.Join(unpacked, "trimui-chiaki-ng-v0.4.0-beta2")
	buildChiakiTagTree(t, top, chiakiPS5Tag, true)
	target := filepath.Join(t.TempDir(), "Apps", "Chiaki-PS5")

	if err := installChiakiFromTree(unpacked, target, chiakiPS5Tag); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, rel := range []string{"launch.sh", "app.py", chiakiPS5MarkModule} {
		if !regularFile(filepath.Join(target, filepath.FromSlash(rel))) {
			t.Errorf("installed tree missing %s", rel)
		}
	}
	if got := chiakiInstalledVersionAt(target); got != chiakiPS5Tag {
		t.Errorf("version file = %q, want %q", got, chiakiPS5Tag)
	}
}

func TestInstallChiakiFromTreeRejectsTamperedFile(t *testing.T) {
	unpacked := t.TempDir()
	top := filepath.Join(unpacked, "trimui-chiaki-ng-v0.4.0-beta2")
	buildChiakiTagTree(t, top, chiakiPS5Tag, true)
	// Corrupt one file after the manifest was written, keeping its length so the
	// digest check (not the size check) is what catches it.
	writeChiakiTestFile(t, filepath.Join(top, "files", "rh", "engine.py"), "enginX\n")

	target := filepath.Join(t.TempDir(), "Apps", "Chiaki-PS5")
	err := installChiakiFromTree(unpacked, target, chiakiPS5Tag)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered tree was not rejected: %v", err)
	}
	if info, statErr := os.Stat(filepath.Join(top, "files", "rh", "engine.py")); statErr != nil || info.Size() != 7 {
		t.Fatalf("test fixture should keep the file length: %v %v", info, statErr)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("a failed verification must not create the target: %v", statErr)
	}
}

func TestInstallChiakiFromTreeRequiresPS5Build(t *testing.T) {
	unpacked := t.TempDir()
	top := filepath.Join(unpacked, "trimui-chiaki-ng-v0.3.40")
	buildChiakiTagTree(t, top, "v0.3.40", false)
	target := filepath.Join(t.TempDir(), "Apps", "Chiaki-PS5")
	err := installChiakiFromTree(unpacked, target, "v0.3.40")
	if err == nil || !strings.Contains(err.Error(), "PS5") {
		t.Fatalf("a PS4-only tree must be rejected for the PS5 slot: %v", err)
	}
}

func TestInstallChiakiFromTreeRequiresMatchingVersion(t *testing.T) {
	unpacked := t.TempDir()
	top := filepath.Join(unpacked, "trimui-chiaki-ng-v0.4.0-beta2")
	buildChiakiTagTree(t, top, chiakiPS5Tag, true)
	target := filepath.Join(t.TempDir(), "Apps", "Chiaki-PS5")
	if err := installChiakiFromTree(unpacked, target, "v0.4.0-beta1"); err == nil {
		t.Fatal("a mismatched tag was installed")
	}
}

func TestChiakiPanelRendersInsideCanvas(t *testing.T) {
	snapshotHomeGlobals(t)
	openChiakiPanel()
	t.Cleanup(func() { chiakiPanel = chiakiPanelState{} })

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	renderChiakiPanel(renderer, &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}})
	if chiakiPanelRect.W <= 0 || chiakiPanelRect.H <= 0 {
		t.Fatalf("panel rect not set: %+v", chiakiPanelRect)
	}
	if len(chiakiRowHits) == 0 {
		t.Fatal("panel drew no rows")
	}
	for _, hit := range chiakiRowHits {
		r := hit.rect
		if r.X < chiakiPanelRect.X || r.Y < chiakiPanelRect.Y ||
			r.X+r.W > chiakiPanelRect.X+chiakiPanelRect.W ||
			r.Y+r.H > chiakiPanelRect.Y+chiakiPanelRect.H {
			t.Errorf("row rect %+v escapes the panel %+v", r, chiakiPanelRect)
		}
	}
}

// TestChiakiPS5TagInstallIntegration installs the real PS5 beta from its tag.
// Opt-in: it downloads the ~12 MB source tarball. It proves the whole path,
// including that the manifest's digests match the files GitHub actually serves.
func TestChiakiPS5TagInstallIntegration(t *testing.T) {
	if os.Getenv("JUKAHUB_TEST_CHIAKI") != "1" {
		t.Skip("set JUKAHUB_TEST_CHIAKI=1 to download and install the real PS5 beta")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	root := t.TempDir()
	target := filepath.Join(root, "Apps", "Chiaki-PS5")
	if err := installChiakiFromTag(ctx, root, target, chiakiPS5Tag); err != nil {
		t.Fatalf("install %s: %v", chiakiPS5Tag, err)
	}
	for _, rel := range []string{
		"launch.sh", "app.py", chiakiPS5MarkModule,
		"bin/chiaki-regist", "bin/chiaki-stream",
		"libs/brick-stock/libSDL2-2.0.so.0",
	} {
		if !regularFile(filepath.Join(target, filepath.FromSlash(rel))) {
			t.Errorf("installed PS5 beta missing %s", rel)
		}
	}
	if got := chiakiInstalledVersionAt(target); got != chiakiPS5Tag {
		t.Errorf("version = %q, want %q", got, chiakiPS5Tag)
	}
}

// TestChiakiReleaseInstallIntegration exercises the real GitHub release and
// package layout. It is opt-in because it downloads the ~13 MB portable ZIP.
func TestChiakiReleaseInstallIntegration(t *testing.T) {
	if os.Getenv("JUKAHUB_TEST_CHIAKI") != "1" {
		t.Skip("set JUKAHUB_TEST_CHIAKI=1 to download and install the real release")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	release, err := fetchChiakiRelease(ctx)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if release.SHA256 == "" || len(release.SHA256) != 64 {
		t.Fatalf("release has no verifiable digest: %+v", release)
	}
	root := t.TempDir()
	archive := filepath.Join(root, "chiaki.zip")
	if err := downloadToFile(ctx, newPatchClient(), release.ZipURL, archive, release.SHA256, chiakiZipMaxBytes); err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := installChiakiPackage(ctx, root, archive, release); err != nil {
		t.Fatalf("install: %v", err)
	}
	app := filepath.Join(root, "Apps", "Chiaki")
	for _, rel := range []string{"launch.sh", "app.py", "bin/chiaki-stream", "bin/chiaki-regist", "libs/brick-stock/libSDL2-2.0.so.0"} {
		if !regularFile(filepath.Join(app, filepath.FromSlash(rel))) {
			t.Errorf("installed package missing %s", rel)
		}
	}
	if got := chiakiInstalledVersionAt(app); got != release.Tag {
		t.Errorf("installed version = %q, want %q", got, release.Tag)
	}
}
