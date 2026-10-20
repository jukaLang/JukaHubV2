package main

import (
	"context"
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

func TestChiakiPanelRowsExplainPS5Limitation(t *testing.T) {
	rows := chiakiRows()
	var sawInstall, sawPS5 bool
	for _, row := range rows {
		switch row.label {
		case "Install Chiaki-ng", "Update Chiaki-ng":
			sawInstall = true
		case "PS5 status":
			sawPS5 = true
			if !strings.Contains(strings.ToLower(row.detail), "ps5") || !strings.Contains(strings.ToLower(row.detail), "not supported") {
				t.Fatalf("PS5 row should explain the limitation: %+v", row)
			}
		}
	}
	if !sawInstall || !sawPS5 {
		t.Fatalf("rows = %+v", rows)
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
