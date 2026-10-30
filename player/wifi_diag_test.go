package main

// wifi_diag_test.go — the sheet that has to answer "why is Wi-Fi broken?" on a
// handheld with no shell.
//
// Three things matter: it names the part that failed (interface probe, control
// directory, socket, backend, radio, station, last error), it is reachable when
// the picker itself cannot load a list, and its keys and pills actually run its
// actions instead of the list's.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// resetWiFiDiagnostics clears the process-wide error and start logs so one test
// cannot read another's failure.
func resetWiFiDiagnostics(t *testing.T) {
	t.Helper()
	wifiLastErr.mu.Lock()
	wifiLastErr.text, wifiLastErr.source, wifiLastErr.at = "", "", time.Time{}
	wifiLastErr.mu.Unlock()
	wifiSupplicantStartLog.mu.Lock()
	wifiSupplicantStartLog.iface, wifiSupplicantStartLog.note = "", ""
	wifiSupplicantStartLog.mu.Unlock()
}

// diagEntry finds one row by its label prefix.
func diagEntry(report wifiDiagReport, label string) (wifiDiagEntry, bool) {
	for _, e := range report.Entries {
		if e.Label == label || strings.HasPrefix(e.Label, label+" ") {
			return e, true
		}
	}
	return wifiDiagEntry{}, false
}

// A device with a radio but no supplicant: the sheet must point at the missing
// control directory and backend, not just say "no networks".
func TestWiFiDiagnosticsNamesTheMissingBackend(t *testing.T) {
	root := wifiIfaceFixture(t)
	resetWiFiDiagnostics(t)
	if err := os.MkdirAll(filepath.Join(root, "wlan0", "phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}

	report := collectWiFiDiagnostics()
	if e, ok := diagEntry(report, "Interface"); !ok || e.Value != "wlan0" || e.State != wifiDiagOK {
		t.Fatalf("Interface row = %+v, want wlan0 and ok", e)
	}
	if e, ok := diagEntry(report, "Found by"); !ok || !strings.Contains(e.Value, "sysfs") {
		t.Fatalf("Found by row = %+v, want the sysfs probe", e)
	}
	if e, ok := diagEntry(report, "Backend"); !ok || e.State != wifiDiagErr || !strings.Contains(e.Value, "none") {
		t.Fatalf("Backend row = %+v, want an error naming no backend", e)
	}
	if e, ok := diagEntry(report, "Control dir"); !ok || e.State != wifiDiagWarn {
		t.Fatalf("Control dir row = %+v, want a warning", e)
	}
	if e, ok := diagEntry(report, "Last error"); !ok || !strings.Contains(e.Value, "none this session") {
		t.Fatalf("Last error row = %+v, want the empty-session answer", e)
	}
	if report.At.IsZero() {
		t.Error("the report has no timestamp, so its age and auto-refresh cannot work")
	}
}

// A working supplicant socket: the sheet must show the directory it found, the
// socket, the backend and the station.
func TestWiFiDiagnosticsReadsAWorkingSupplicant(t *testing.T) {
	resetWiFiDiagnostics(t)
	_ = newProbeFrame(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wlan0"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	prevDirs := wifiSupplicantDirs
	wifiSupplicantDirs = []string{dir}
	t.Cleanup(func() {
		wifiSupplicantDirs = prevDirs
		resetSupplicantInterfaceCache()
	})
	resetSupplicantInterfaceCache()
	resetWiFiInterfaceCache()

	stubWiFi(t, func(stdin, name string, args []string) (string, error) {
		switch wifiStubCommand(args) {
		case "status":
			return "wpa_state=COMPLETED\nssid=HomeNet\nip_address=192.168.1.20\n", nil
		}
		return "OK\n", nil
	})

	report := collectWiFiDiagnostics()
	if e, ok := diagEntry(report, "Backend"); !ok || e.State != wifiDiagOK || e.Value != wifiBackendCli {
		t.Fatalf("Backend row = %+v, want %q", e, wifiBackendCli)
	}
	if e, ok := diagEntry(report, "Control dir"); !ok || e.Value != dir {
		t.Fatalf("Control dir row = %+v, want %q", e, dir)
	}
	if e, ok := diagEntry(report, "Control socket"); !ok || !strings.Contains(e.Value, "(present)") {
		t.Fatalf("Control socket row = %+v, want the socket reported present", e)
	}
	if e, ok := diagEntry(report, "Station"); !ok || !strings.Contains(e.Value, "HomeNet") || e.State != wifiDiagOK {
		t.Fatalf("Station row = %+v, want the connected network", e)
	}
}

// The last error is recorded at the one choke point every command failure goes
// through, so the sheet and the panel cannot disagree about it.
func TestWiFiDiagnosticsKeepsTheLastCommandError(t *testing.T) {
	resetWiFiDiagnostics(t)
	if err := wifiCommandError("FAIL-AUTH\n", nil); err == nil {
		t.Fatal("a FAIL reply did not produce an error")
	}
	text, source, at := wifiLastError()
	if text != "FAIL-AUTH" || source != "command" {
		t.Fatalf("last error = %q from %q, want FAIL-AUTH from command", text, source)
	}
	if time.Since(at) > time.Minute {
		t.Fatalf("the remembered error is %v old", time.Since(at))
	}

	report := collectWiFiDiagnostics()
	e, ok := diagEntry(report, "Last error")
	if !ok || !strings.Contains(e.Value, "FAIL-AUTH") || !strings.Contains(e.Value, "ago") {
		t.Fatalf("Last error row = %+v, want the remembered failure and its age", e)
	}
	if e.State != wifiDiagErr {
		t.Fatalf("Last error row state = %v, want the error color", e.State)
	}
}

// A supplicant start JukaHub ran itself is part of the diagnosis: "not
// attempted" and "failed" are different answers.
func TestWiFiDiagnosticsShowsTheSupplicantStart(t *testing.T) {
	resetWiFiDiagnostics(t)
	recordSupplicantStart("wlan0", "failed: nl80211 not found")
	report := collectWiFiDiagnostics()
	e, ok := diagEntry(report, "Supplicant start")
	if !ok || !strings.Contains(e.Value, "failed") || e.State != wifiDiagErr {
		t.Fatalf("Supplicant start row = %+v, want the failure", e)
	}
	if !strings.Contains(e.Label, "wlan0") {
		t.Fatalf("Supplicant start label = %q, want the interface", e.Label)
	}
}

// The sheet opens with the key the footer advertises, scrolls, and B goes back
// to the list instead of closing the picker — including while the list has no
// backend, which is the case the sheet exists for.
func TestWiFiDiagnosticsKeyAndPadReach(t *testing.T) {
	prev := wifiPanel
	t.Cleanup(func() { wifiPanel = prev })
	wifiPanel = wifiPanelState{open: true, available: false, detail: "No wireless network interface on this device"}

	if !handleWiFiKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_i}}, &Config{}) {
		t.Fatal("I was not consumed by the picker")
	}
	if !wifiPanel.diagOpen {
		t.Fatal("I did not open the diagnostics sheet")
	}
	// Scrolling with nothing rendered yet must not panic or go negative.
	wifiDiagScroll(-3)
	if wifiPanel.diagScroll != 0 {
		t.Fatalf("scroll went to %d, want 0", wifiPanel.diagScroll)
	}
	if !handleWiFiKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}, &Config{}) {
		t.Fatal("Escape was not consumed by the sheet")
	}
	if wifiPanel.diagOpen || !wifiPanel.open {
		t.Fatalf("Escape left diag=%v panel=%v, want the list still open", wifiPanel.diagOpen, wifiPanel.open)
	}
	if !handleWiFiKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}, &Config{}) || wifiPanel.open {
		t.Fatal("Escape on the list did not close the panel")
	}

	// Pad: L1 opens the sheet, B backs out to the list, a second B closes.
	wifiPanel = wifiPanelState{open: true, available: false}
	ctrlWiFi(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_LEFTSHOULDER}, &Config{})
	if !wifiPanel.diagOpen {
		t.Fatal("L1 did not open the diagnostics sheet")
	}
	ctrlWiFi(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B}, &Config{})
	if wifiPanel.diagOpen || !wifiPanel.open {
		t.Fatalf("B left diag=%v panel=%v, want the list still open", wifiPanel.diagOpen, wifiPanel.open)
	}
	ctrlWiFi(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B}, &Config{})
	if wifiPanel.open {
		t.Fatal("B on the list did not close the panel")
	}
}

// The sheet is drawable at both canvas sizes, its pills are touch targets inside
// the panel, and it leaves no stale list row behind (a row rect would be a tap
// target on a list that is not on screen).
func TestWiFiDiagnosticsRendersItsActions(t *testing.T) {
	_ = newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	prevPanel, prevActions, prevRows := wifiPanel, wifiActionRects, wifiRowRects
	t.Cleanup(func() {
		wifiPanel, wifiActionRects, wifiRowRects = prevPanel, prevActions, prevRows
		ClearTextCache()
	})

	for _, size := range []struct{ w, h int32 }{{probeW, probeH}, {800, 480}} {
		withScreen(t, size.w, size.h)
		wifiPanel = wifiPanelState{open: true, available: false, diagOpen: true,
			detail: "No wireless network interface on this device"}
		wifiPanel.diag = wifiDiagReport{
			At: time.Now(),
			Entries: []wifiDiagEntry{
				{Label: "Interface", Value: "none found", State: wifiDiagErr},
				{Label: "Found by", Value: "nothing found: no sysfs marker, no kernel list, no control socket", State: wifiDiagInfo},
				{Label: "Last error", Value: "wpa_cli did not answer (exit status 255) · 12s ago", State: wifiDiagErr},
			},
		}
		renderer.SetDrawColor(9, 14, 25, 255)
		renderer.Clear()
		renderWiFiPanel(renderer, &Config{})

		if len(wifiRowRects) != 0 {
			t.Fatalf("%dx%d: the sheet left %d list row targets behind", size.w, size.h, len(wifiRowRects))
		}
		got := map[wifiActionID]sdl.Rect{}
		for _, hit := range wifiActionRects {
			got[hit.id] = hit.rect
			if hit.rect.W <= 0 || hit.rect.H <= 0 {
				t.Fatalf("%dx%d: action %v has no size", size.w, size.h, hit.id)
			}
			if !pointInRect(hit.rect.X+hit.rect.W/2, hit.rect.Y+hit.rect.H/2, wifiPanelRect) {
				t.Fatalf("%dx%d: action %v %+v is outside the panel %+v",
					size.w, size.h, hit.id, hit.rect, wifiPanelRect)
			}
		}
		for _, want := range []wifiActionID{wifiActionDiagRefresh, wifiActionDiagBack} {
			if _, ok := got[want]; !ok {
				t.Fatalf("%dx%d: the sheet did not draw action %v", size.w, size.h, want)
			}
		}
	}
}

// The list keeps Diagnostics reachable when no backend answered; without that
// pill the one state the sheet is for could not open it on a touch screen.
func TestWiFiListDrawsTheDiagnosticsPillWithoutABackend(t *testing.T) {
	_ = newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	prevPanel, prevActions := wifiPanel, wifiActionRects
	t.Cleanup(func() {
		wifiPanel, wifiActionRects = prevPanel, prevActions
		ClearTextCache()
	})

	withScreen(t, probeW, probeH)
	wifiPanel = wifiPanelState{open: true, available: false, loadedAt: time.Now(),
		detail: "Wi-Fi control needs wpa_cli or a wpa_supplicant control socket, and this image has neither"}
	renderer.SetDrawColor(9, 14, 25, 255)
	renderer.Clear()
	renderWiFiPanel(renderer, &Config{})

	seen := map[wifiActionID]bool{}
	for _, hit := range wifiActionRects {
		seen[hit.id] = true
	}
	if !seen[wifiActionDiag] || !seen[wifiActionClose] {
		t.Fatalf("list actions = %v, want Diagnostics and Close", seen)
	}
	if seen[wifiActionRescan] {
		t.Fatal("an unavailable picker offered Rescan")
	}
}

// A report collected on a worker reaches the panel through the drain, which is
// the only path that may write panel state.
func TestWiFiDiagnosticsUpdateReachesThePanel(t *testing.T) {
	prev := wifiPanel
	t.Cleanup(func() { wifiPanel = prev })
	wifiPanel = wifiPanelState{open: true}
	wifiWorkPending = true
	wifiGeneration++
	gen := wifiGeneration

	wifiUpdates <- wifiUpdate{kind: wifiUpdateDiag, gen: gen,
		diag: &wifiDiagReport{At: time.Now(), Entries: []wifiDiagEntry{{Label: "Interface", Value: "wlan0"}}}}
	wifiDrainUpdates()

	if wifiWorkPending {
		t.Error("the drain left the panel busy after a diagnostics result")
	}
	if len(wifiPanel.diag.Entries) != 1 || wifiPanel.diag.Entries[0].Value != "wlan0" {
		t.Fatalf("panel report = %+v, want the drained one", wifiPanel.diag)
	}
}
