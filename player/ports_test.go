package main

// PortMaster support tests: launcher detection, the installed-port catalog,
// archive installation (including the download path), and the panel wiring.

import (
	"archive/zip"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// setPortMasterTestRoot points PortMaster detection at a temp SD card and
// clears the caches that hold the previous answer.
func setPortMasterTestRoot(t *testing.T, root string) {
	t.Helper()
	old := portMasterSDRoot
	portMasterSDRoot = root
	invalidatePortsCache()
	invalidateEmulatorCache()
	t.Cleanup(func() {
		portMasterSDRoot = old
		invalidatePortsCache()
		invalidateEmulatorCache()
	})
}

func writePortTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPortMasterLauncherDetection(t *testing.T) {
	root := t.TempDir()
	if got := portMasterLauncher(root); got != "" {
		t.Fatalf("launcher = %q, want empty before install", got)
	}

	apps := filepath.Join(root, "Apps", "PortMaster", "launch.sh")
	writePortTestFile(t, apps, "#!/bin/sh\n")
	if got := portMasterLauncher(root); got != apps {
		t.Fatalf("launcher = %q, want %q", got, apps)
	}

	legacyRoot := t.TempDir()
	legacy := filepath.Join(legacyRoot, "Ports", "PortMaster.sh")
	writePortTestFile(t, legacy, "#!/bin/sh\n")
	if got := portMasterLauncher(legacyRoot); got != legacy {
		t.Fatalf("legacy launcher = %q, want %q", got, legacy)
	}
}

func TestBuildPortEntriesListsInstalledPorts(t *testing.T) {
	root := t.TempDir()
	launcher := filepath.Join(root, "Apps", "PortMaster", "launch.sh")
	writePortTestFile(t, launcher, "#!/bin/sh\n")
	writePortTestFile(t, filepath.Join(root, "Ports", "Celeste.sh"), "#!/bin/sh\n")
	writePortTestFile(t, filepath.Join(root, "Ports", "PortMaster.sh"), "#!/bin/sh\n")
	writePortTestFile(t, filepath.Join(root, "Roms", "PORTS", "Stardew.sh"), "#!/bin/sh\n")
	writePortTestFile(t, filepath.Join(root, "Ports", "celeste.sh"), "#!/bin/sh\n") // duplicate
	writePortTestFile(t, filepath.Join(root, "Data", "ports", "Doom.sh"), "#!/bin/sh\n")

	entries := buildPortEntries(launcher, portRoots(root))
	if len(entries) != 4 {
		t.Fatalf("entries = %+v, want PortMaster plus 3 ports", entries)
	}
	if !entries[0].Master || entries[0].Status != emuAvailable {
		t.Fatalf("first row = %+v, want the PortMaster app", entries[0])
	}
	var names []string
	for _, e := range entries[1:] {
		names = append(names, e.Name)
	}
	want := []string{"Celeste", "Doom", "Stardew"}
	if len(names) != len(want) {
		t.Fatalf("ports = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("ports = %v, want %v", names, want)
		}
	}
}

func TestBuildPortEntriesOffersInstallWhenMissing(t *testing.T) {
	entries := buildPortEntries("", portRoots(t.TempDir()))
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want a single install row", entries)
	}
	if !entries[0].Install || entries[0].Status != emuMissing {
		t.Fatalf("row = %+v, want the install action", entries[0])
	}
}

func TestBuildPortEntriesSaysWhenNoPortsExist(t *testing.T) {
	root := t.TempDir()
	launcher := filepath.Join(root, "Apps", "PortMaster", "launch.sh")
	writePortTestFile(t, launcher, "#!/bin/sh\n")
	entries := buildPortEntries(launcher, portRoots(root))
	if len(entries) != 2 || entries[1].Name != "No ports installed" {
		t.Fatalf("entries = %+v, want a no-ports hint", entries)
	}
}

func TestInstallPortMasterFromStage(t *testing.T) {
	stage := t.TempDir()
	inner := filepath.Join(stage, "Apps", "PortMaster")
	writePortTestFile(t, filepath.Join(inner, "launch.sh"), "#!/bin/sh\n")
	writePortTestFile(t, filepath.Join(inner, "PortMaster", "control.txt"), "# control\n")

	sd := t.TempDir()
	if err := installPortMasterFromStage(stage, sd); err != nil {
		t.Fatalf("install: %v", err)
	}
	target := filepath.Join(sd, "Apps", "PortMaster")
	if !regularFile(filepath.Join(target, "launch.sh")) {
		t.Fatal("launch.sh missing after install")
	}
	if !regularFile(filepath.Join(target, "PortMaster", "control.txt")) {
		t.Fatal("nested PortMaster files missing after install")
	}
	// Windows has no executable mode bits; the handheld build needs them.
	if info, err := os.Stat(filepath.Join(target, "launch.sh")); err != nil ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		t.Fatalf("launch.sh not executable: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(inner); !os.IsNotExist(err) {
		t.Fatalf("staged app still present after rename: %v", err)
	}

	// A second install must not overwrite the existing app.
	if err := installPortMasterFromStage(stage, sd); err == nil {
		t.Fatal("second install unexpectedly succeeded")
	}
}

func TestInstallPortMasterFromStageRejectsMissingLauncher(t *testing.T) {
	stage := t.TempDir()
	writePortTestFile(t, filepath.Join(stage, "Apps", "PortMaster", "README.txt"), "no launcher")
	if err := installPortMasterFromStage(stage, t.TempDir()); err == nil {
		t.Fatal("install unexpectedly accepted a package without launch.sh")
	}
}

func writePortMasterTestZip(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// downloadFileWithProgress rejects implausibly small responses, so the
	// archive carries incompressible padding to stay above that floor.
	padding := make([]byte, 8192)
	rand.New(rand.NewSource(1)).Read(padding)
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"Apps/PortMaster/launch.sh":              "#!/bin/sh\n",
		"Apps/PortMaster/config.json":            `{"label":"PortMaster"}`,
		"Apps/PortMaster/PortMaster/control.txt": "# control\n",
		"Apps/PortMaster/payload.bin":            string(padding),
	} {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(0o755)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallPortMasterDownloadsAndUnpacks(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "portmaster.zip")
	writePortMasterTestZip(t, archive)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	sd := t.TempDir()
	if err := installPortMaster(sd, srv.URL+"/trimui.portmaster.zip"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !regularFile(filepath.Join(sd, "Apps", "PortMaster", "launch.sh")) {
		t.Fatal("downloaded package was not installed")
	}
	remaining, err := filepath.Glob(filepath.Join(sd, ".portmaster-install-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("staging directories left behind: %v", remaining)
	}
}

func TestPortsPanelReturnsToEmulators(t *testing.T) {
	setPortMasterTestRoot(t, t.TempDir())
	emuPanel = emuPanelState{open: true, cursor: 3, scroll: 1}
	t.Cleanup(func() { emuPanel = emuPanelState{} })

	openPortsPanel(true)
	if !portsPanel.open || emuPanel.open {
		t.Fatal("opening Ports did not replace the emulator list")
	}
	closePortsPanel()
	if portsPanel.open || !emuPanel.open || emuPanel.cursor != 3 || emuPanel.scroll != 1 {
		t.Fatalf("closing did not restore the emulator list: ports=%+v emu=%+v", portsPanel, emuPanel)
	}
}

func TestPortsPanelRendersRows(t *testing.T) {
	root := t.TempDir()
	writePortTestFile(t, filepath.Join(root, "Apps", "PortMaster", "launch.sh"), "#!/bin/sh\n")
	writePortTestFile(t, filepath.Join(root, "Ports", "Celeste.sh"), "#!/bin/sh\n")
	setPortMasterTestRoot(t, root)

	snapshotHomeGlobals(t)
	openPortsPanel(false)
	t.Cleanup(func() {
		portsPanel = portsPanelState{}
		invalidatePortsCache()
	})

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	renderPortsPanel(renderer, &Config{Scenes: []SceneConfig{{Name: "Main", Layout: "home"}}})
	if portsPanelRect.W <= 0 || portsPanelRect.H <= 0 {
		t.Fatalf("panel rect not set: %+v", portsPanelRect)
	}
	if len(portRowHits) != 2 {
		t.Fatalf("row hits = %d, want PortMaster plus Celeste", len(portRowHits))
	}
	for _, hit := range portRowHits {
		r := hit.rect
		if r.X < portsPanelRect.X || r.Y < portsPanelRect.Y ||
			r.X+r.W > portsPanelRect.X+portsPanelRect.W ||
			r.Y+r.H > portsPanelRect.Y+portsPanelRect.H {
			t.Errorf("row rect %+v escapes the panel %+v", r, portsPanelRect)
		}
	}
}

func TestPortMasterEmuEntryReflectsInstall(t *testing.T) {
	setPortMasterTestRoot(t, t.TempDir())
	if e := portMasterEmuEntry(); e.Status != emuMissing || !e.Pinned {
		t.Fatalf("entry = %+v, want a pinned install row", e)
	}

	root := t.TempDir()
	writePortTestFile(t, filepath.Join(root, "Apps", "PortMaster", "launch.sh"), "#!/bin/sh\n")
	writePortTestFile(t, filepath.Join(root, "Ports", "Celeste.sh"), "#!/bin/sh\n")
	setPortMasterTestRoot(t, root)
	if e := portMasterEmuEntry(); e.Status != emuAvailable || e.Name != portMasterEntryName {
		t.Fatalf("entry = %+v, want an available Ports row", e)
	}
}
