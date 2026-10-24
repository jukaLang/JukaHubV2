package main

// Provisioning tests: the launcher installs what a launch is missing, in the
// right order, and only starts the app once the last piece is in. The install
// steps and the runtime probe are stubbed, so nothing here touches the network
// or downloads a real runtime.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWindowsRuntimePlanOrdersMissingPieces(t *testing.T) {
	cases := []struct {
		name   string
		status windowsRuntimeStatus
		want   []windowsRuntimeStep
	}{
		{
			name:   "nothing installed",
			status: windowsRuntimeStatus{},
			want:   []windowsRuntimeStep{windowsStepWine, windowsStepDisplay},
		},
		{
			name:   "wine present, no display",
			status: windowsRuntimeStatus{Wine: "/x/wine"},
			want:   []windowsRuntimeStep{windowsStepDisplay},
		},
		{
			name:   "ready on x11",
			status: windowsRuntimeStatus{Wine: "/x/wine", Display: windowsDisplayX11},
			want:   nil,
		},
		{
			name:   "dxvk wanted only with vulkan",
			status: windowsRuntimeStatus{Wine: "/x/wine", Display: windowsDisplayX11, Vulkan: true},
			want:   []windowsRuntimeStep{windowsStepDXVK},
		},
		{
			name:   "dxvk skipped without vulkan",
			status: windowsRuntimeStatus{Wine: "/x/wine", Display: windowsDisplayX11},
			want:   nil,
		},
		{
			name:   "installed dxvk is not reinstalled",
			status: windowsRuntimeStatus{Wine: "/x/wine", Display: windowsDisplayX11, Vulkan: true, DXVK: "/x/dxvk"},
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := windowsRuntimePlan(tc.status)
			if len(got) != len(tc.want) {
				t.Fatalf("plan = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("plan = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// provisionHarness drives the provisioning chain with stub installers.
type provisionHarness struct {
	mu        sync.Mutex
	status    windowsRuntimeStatus
	order     []string
	launched  string
	installCh chan struct{}
}

func newProvisionHarness(t *testing.T, status windowsRuntimeStatus) *provisionHarness {
	t.Helper()
	h := &provisionHarness{status: status, installCh: make(chan struct{}, 8)}

	prevStatus := windowsRuntimeStatusFor
	prevWine, prevDisplay, prevDXVK := windowsInstallWineFn, windowsInstallDisplayFn, windowsInstallDXVKFn
	prevLaunch := windowsLaunchAfterInstall
	t.Cleanup(func() {
		windowsRuntimeStatusFor = prevStatus
		windowsInstallWineFn, windowsInstallDisplayFn, windowsInstallDXVKFn = prevWine, prevDisplay, prevDXVK
		windowsLaunchAfterInstall = prevLaunch
		endWindowsRuntimeInstall()
		invalidateWindowsAppsCache()
	})

	windowsRuntimeStatusFor = func() windowsRuntimeStatus {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.status
	}
	windowsInstallWineFn = func() error {
		h.record("wine")
		h.mu.Lock()
		h.status.Wine = "/stub/wine"
		h.mu.Unlock()
		return nil
	}
	windowsInstallDisplayFn = func() error {
		h.record("weston")
		h.mu.Lock()
		h.status.Weston = "/stub/westonwrap.sh"
		h.status.Display = windowsDisplayWeston
		h.mu.Unlock()
		return nil
	}
	windowsInstallDXVKFn = func() error {
		h.record("dxvk")
		h.mu.Lock()
		h.status.DXVK = "/stub/dxvk"
		h.mu.Unlock()
		return nil
	}
	windowsLaunchAfterInstall = func(entry windowsAppEntry) error {
		h.mu.Lock()
		h.launched = entry.Title
		h.mu.Unlock()
		return nil
	}
	return h
}

func (h *provisionHarness) record(step string) {
	h.mu.Lock()
	h.order = append(h.order, step)
	h.mu.Unlock()
}

func (h *provisionHarness) snapshot() (order []string, launched string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.order...), h.launched
}

func (h *provisionHarness) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !windowsRuntimeInstalling() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("provisioning did not finish")
}

func TestWindowsProvisioningInstallsInOrderThenLaunches(t *testing.T) {
	h := newProvisionHarness(t, windowsRuntimeStatus{})
	if err := startWindowsProvisioning(&windowsAppEntry{Title: "Notepad", Path: "/apps/notepad.exe"}); err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t)

	order, launched := h.snapshot()
	if strings.Join(order, ",") != "wine,weston" {
		t.Fatalf("install order = %v, want wine then weston (no DXVK without Vulkan)", order)
	}
	if launched != "Notepad" {
		t.Fatalf("launched = %q, want Notepad once the runtime was ready", launched)
	}
	if status := windowsRuntimeInstallLine(); !strings.Contains(status, "Windows runtime ready") {
		t.Fatalf("install line = %q, want the ready state", status)
	}
}

func TestWindowsProvisioningAddsDXVKWhenVulkanExists(t *testing.T) {
	h := newProvisionHarness(t, windowsRuntimeStatus{Wine: "/x/wine", Display: windowsDisplayWeston, Vulkan: true})
	if err := startWindowsProvisioning(&windowsAppEntry{Title: "Game", Path: "/apps/game.exe"}); err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t)
	order, launched := h.snapshot()
	if strings.Join(order, ",") != "dxvk" {
		t.Fatalf("install order = %v, want only dxvk", order)
	}
	if launched != "Game" {
		t.Fatalf("launched = %q, want Game", launched)
	}
}

func TestWindowsProvisioningStopsOnFailureAndDoesNotLaunch(t *testing.T) {
	h := newProvisionHarness(t, windowsRuntimeStatus{})
	failure := fmt.Errorf("download blocked")
	windowsInstallWineFn = func() error {
		h.record("wine")
		return failure
	}
	if err := startWindowsProvisioning(&windowsAppEntry{Title: "Notepad"}); err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t)
	order, launched := h.snapshot()
	if strings.Join(order, ",") != "wine" {
		t.Fatalf("install order = %v, want the chain to stop at wine", order)
	}
	if launched != "" {
		t.Fatalf("launched %q although the install failed", launched)
	}
	if line := windowsRuntimeInstallLine(); !strings.Contains(line, failure.Error()) {
		t.Fatalf("install line = %q, want the failing step's reason", line)
	}
	if !windowsWineInstallFailed() {
		t.Fatal("the Wine row would not show a failed install")
	}
}

func TestWindowsProvisioningRefusesAnInstallThatNeverSatisfiesItsStep(t *testing.T) {
	h := newProvisionHarness(t, windowsRuntimeStatus{})
	// The install reports success but leaves Wine missing: the loop must not
	// retry it forever.
	windowsInstallWineFn = func() error {
		h.record("wine")
		return nil
	}
	if err := startWindowsProvisioning(&windowsAppEntry{Title: "Notepad"}); err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t)
	order, _ := h.snapshot()
	if strings.Join(order, ",") != "wine" {
		t.Fatalf("install order = %v, want one attempt", order)
	}
	if line := windowsRuntimeInstallLine(); !strings.Contains(line, "still missing") {
		t.Fatalf("install line = %q, want the repeated-step explanation", line)
	}
}

func TestSelectDXVKAssetSkipsChecksums(t *testing.T) {
	name, url, err := selectDXVKAsset([]hangoverAsset{
		{Name: "dxvk-2.6.tar.gz.sha256", BrowserDownloadURL: "sha"},
		{Name: "dxvk-2.6-source.tar.gz", BrowserDownloadURL: "src"},
		{Name: "dxvk-2.6.tar.gz", BrowserDownloadURL: "bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "dxvk-2.6.tar.gz" || url != "bin" {
		t.Fatalf("asset = %q %q, want the binary tarball", name, url)
	}
	if _, _, err := selectDXVKAsset([]hangoverAsset{{Name: "dxvk-2.6.tar.zst", BrowserDownloadURL: "z"}}); err == nil {
		t.Fatal("an unsupported archive format was accepted")
	}
}

// dxvkArchive builds the shape upstream publishes: one wrapper directory with
// x64/ and x32/ trees.
func dxvkArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name, body string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("dxvk-2.6/x64/d3d11.dll", "x64 dll")
	add("dxvk-2.6/x32/d3d11.dll", "x32 dll")
	add("dxvk-2.6/x64/dxgi.dll", "dxgi")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallDXVKRuntimeInstallsTheRelease(t *testing.T) {
	archive := dxvkArchive(t)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"tag_name":"v2.6","assets":[{"name":"dxvk-2.6.tar.gz","size":%d,"browser_download_url":%q}]}`,
				len(archive), server.URL+"/dxvk-2.6.tar.gz")
		case "/dxvk-2.6.tar.gz":
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	prevDir := windowsRuntimeDirOverride
	prevRelease := windowsDXVKRelease
	prevVulkan := windowsResolveVulkan
	windowsRuntimeDirOverride = dir
	windowsDXVKRelease = server.URL + "/release"
	windowsResolveVulkan = func() string { return "/lib/libvulkan.so.1" }
	t.Cleanup(func() {
		windowsRuntimeDirOverride = prevDir
		windowsDXVKRelease = prevRelease
		windowsResolveVulkan = prevVulkan
		setWindowsRuntimeStatus("", "", false)
	})

	if err := installDXVKRuntime(); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(dir, "dxvk")
	if !dxvkBuild(build) {
		t.Fatalf("installed build is not recognised: %s", build)
	}
	for _, rel := range []string{"x64/d3d11.dll", "x32/d3d11.dll", "x64/dxgi.dll"} {
		if _, err := os.Stat(filepath.Join(build, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func TestInstallDXVKRefusesWithoutVulkan(t *testing.T) {
	prevVulkan := windowsResolveVulkan
	windowsResolveVulkan = func() string { return "" }
	t.Cleanup(func() { windowsResolveVulkan = prevVulkan })
	err := installDXVKRuntime()
	if err == nil || !strings.Contains(err.Error(), "Vulkan") {
		t.Fatalf("error = %v, want a Vulkan requirement", err)
	}
}

// Pressing A on an app that cannot start yet opens setup on the missing row
// with a notice, and remembers the app; it must not start a download by itself.
func TestWindowsAppsLaunchWithMissingRuntimeOpensSetup(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notepad.exe"), []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevRoots := windowsAppsRootsOverride
	windowsAppsRootsOverride = []string{root}
	prevStatus := windowsRuntimeStatusFor
	windowsRuntimeStatusFor = func() windowsRuntimeStatus { return windowsRuntimeStatus{} }
	t.Cleanup(func() {
		windowsAppsRootsOverride = prevRoots
		windowsRuntimeStatusFor = prevStatus
		invalidateWindowsAppsCache()
		winAppsPanel = windowsAppsPanelState{}
	})

	invalidateWindowsAppsCache()
	openWindowsAppsPanel(false)
	windowsAppsPanelActivate()

	if !winAppsPanel.help {
		t.Fatal("a launch without the runtime did not open the setup page")
	}
	if winAppsPanel.pending == nil || !strings.EqualFold(winAppsPanel.pending.Title, "notepad") {
		t.Fatalf("pending launch = %+v, want the notepad entry", winAppsPanel.pending)
	}
	if !strings.Contains(winAppsPanel.notice, "Wine") || !strings.Contains(winAppsPanel.notice, "WestonPack") {
		t.Fatalf("notice = %q, want both missing pieces named", winAppsPanel.notice)
	}
	if setupCursor != windowsSetupRowWine {
		t.Fatalf("setup cursor = %d, want the Wine row", setupCursor)
	}
	if windowsRuntimeInstalling() {
		t.Fatal("opening setup started an install without the confirming press")
	}

	// Leaving setup cancels the waiting launch.
	windowsAppsPanelToggleHelp()
	if winAppsPanel.pending != nil || winAppsPanel.notice != "" {
		t.Fatal("closing setup kept the pending launch")
	}
}

func TestWindowsSetupRowsOfferDXVKOnlyWithVulkan(t *testing.T) {
	without := windowsAppsSetupRows(windowsRuntimeStatus{Wine: "/x/wine", Weston: "/x/westonwrap.sh"})
	if len(without) != windowsSetupRowCount {
		t.Fatalf("rows = %d, want %d", len(without), windowsSetupRowCount)
	}
	if got := without[windowsSetupRowDXVK].Status; !strings.Contains(got, "Vulkan") {
		t.Fatalf("DXVK row without Vulkan = %q, want a Vulkan hint", got)
	}
	with := windowsAppsSetupRows(windowsRuntimeStatus{
		Wine: "/x/wine", Weston: "/x/westonwrap.sh", Display: windowsDisplayWeston,
		Vulkan: true, DXVK: "/x/dxvk",
	})
	if with[windowsSetupRowDXVK].Bad || with[windowsSetupRowDXVK].Status != "installed" {
		t.Fatalf("installed DXVK row = %+v, want installed", with[windowsSetupRowDXVK])
	}
}
