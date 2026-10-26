package main

// The three scanning tools (naabu, httpx, dnsx) are the only catalog entries
// that send traffic to another machine, so they are the only ones behind the
// one-time acknowledgement. These tests pin the gate's shape: local tools never
// see it, the first press asks, the second press accepts and records the
// answer, and the answer carries to every scanning tool from then on.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearAuthorizedUseArm(t *testing.T) {
	t.Helper()
	powerTools.authorizeKey = ""
	powerTools.authorizeAt = time.Time{}
	t.Cleanup(func() {
		powerTools.authorizeKey = ""
		powerTools.authorizeAt = time.Time{}
	})
}

func TestToolboxScanGateAsksOnce(t *testing.T) {
	clearAuthorizedUseArm(t)
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}

	naabu, ok := toolboxFind("naabu")
	if !ok || !naabu.NetworkScan {
		t.Fatal("naabu is missing from the catalog or not marked as a scanner")
	}
	httpx, _ := toolboxFind("httpx")
	jq, _ := toolboxFind("jq")
	if jq.NetworkScan {
		t.Fatal("jq is marked as a scanner; only the network tools may be")
	}

	if !toolboxAuthorizedUseGate(jq, config) {
		t.Fatal("a tool that only touches this device was gated")
	}

	// First press asks. Switching to another scanner asks again, because the
	// answer has not been given yet.
	if toolboxAuthorizedUseGate(naabu, config) {
		t.Fatal("the first press ran the scan instead of asking")
	}
	if toolboxAuthorizedUseAccepted(config) {
		t.Fatal("the first press recorded an acknowledgement")
	}
	if toolboxAuthorizedUseGate(httpx, config) {
		t.Fatal("a different scanner ran without asking")
	}

	// Second press on the same tool accepts and runs.
	if !toolboxAuthorizedUseGate(httpx, config) {
		t.Fatal("the second press did not accept")
	}
	if !toolboxAuthorizedUseAccepted(config) {
		t.Fatal("the acknowledgement was not recorded")
	}
	user := userConfigFromVariables(config)
	if user == nil || user.Variables.Custom[toolboxAuthorizedUseKey] != true {
		t.Fatal("the acknowledgement is not carried into the saved user config")
	}

	// And it carries: no more asking, for any scanner.
	if !toolboxAuthorizedUseGate(naabu, config) {
		t.Fatal("another scanner asked again after the answer was given")
	}
	if !toolboxAuthorizedUseGate(httpx, config) {
		t.Fatal("the answered tool asked again")
	}
}

// An armed prompt expires, so a press minutes later is a first press again.
func TestToolboxScanGateExpires(t *testing.T) {
	clearAuthorizedUseArm(t)
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	naabu, _ := toolboxFind("naabu")

	if toolboxAuthorizedUseGate(naabu, config) {
		t.Fatal("the first press ran the scan")
	}
	powerTools.authorizeAt = time.Now().Add(-2 * time.Minute)
	if toolboxAuthorizedUseGate(naabu, config) {
		t.Fatal("a stale arm accepted without asking again")
	}
	if toolboxAuthorizedUseAccepted(config) {
		t.Fatal("a stale arm recorded an acknowledgement")
	}
}

// The answer survives a relaunch: it is read from the custom block, in either
// the boolean the app writes or the string a hand edit leaves behind.
func TestToolboxScanGateReadsTheSavedAnswer(t *testing.T) {
	for _, value := range []interface{}{true, "true", "TRUE"} {
		config := &Config{}
		config.Variables.Custom = map[string]interface{}{toolboxAuthorizedUseKey: value}
		if !toolboxAuthorizedUseAccepted(config) {
			t.Errorf("%v in the config was not read as an acknowledgement", value)
		}
	}
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{toolboxAuthorizedUseKey: false}
	if toolboxAuthorizedUseAccepted(config) {
		t.Error("an explicit false was read as an acknowledgement")
	}
	if toolboxAuthorizedUseAccepted(nil) {
		t.Error("a nil config accepted the gate")
	}
}

// TestToolboxScanRowAsksBeforeItRuns is the wiring: pressing A on an installed
// scanner row arms the prompt instead of starting the tool, and the second
// press is what opens the output viewer.
func TestToolboxScanRowAsksBeforeItRuns(t *testing.T) {
	clearAuthorizedUseArm(t)
	dir := t.TempDir()
	old := toolboxToolsDirOverride
	toolboxToolsDirOverride = dir
	t.Cleanup(func() { toolboxToolsDirOverride = old })

	// A fake install: presence is all toolboxInstalled checks.
	if err := os.WriteFile(filepath.Join(dir, toolboxExeNameOf("naabu")), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	// The rows depend on which page the panel is showing, so the state comes
	// first and the cursor is picked from the rows it produces.
	withPowerToolsState(t, powerToolsState{page: powerPageToolbox})
	rows := powerToolRows(config)
	cursor := -1
	for i, row := range rows {
		if row.action == powerActionToolboxRun && row.arg == "naabu" {
			cursor = i
			break
		}
	}
	if cursor < 0 {
		t.Fatal("the toolbox page has no run row for naabu")
	}
	powerTools.cursor = cursor

	powerActivate(config)
	if powerTools.authorizeKey != "naabu" {
		t.Fatalf("arm key = %q, want naabu", powerTools.authorizeKey)
	}
	if powerTools.page != powerPageToolbox {
		t.Fatalf("the first press started the scan (page = %v)", powerTools.page)
	}
	if toolboxAuthorizedUseAccepted(config) {
		t.Fatal("the first press recorded the acknowledgement")
	}

	powerActivate(config)
	if powerTools.page != powerPageOutput {
		t.Fatalf("the second press did not run the tool (page = %v)", powerTools.page)
	}
	if !toolboxAuthorizedUseAccepted(config) {
		t.Fatal("the second press did not record the acknowledgement")
	}

	// That press launched the tool on a goroutine which resolves the fake
	// install through the tools-dir override this test's cleanup is about to
	// restore — and through powerTools itself, which withPowerToolsState also
	// swaps back. Wait the runner out before any cleanup can land on it.
	waitForPowerAsync(t)
}

// The catalog says which entries are scanners, and every scanner says so on its
// row before the first run.
func TestToolboxScanEntriesAreMarked(t *testing.T) {
	want := map[string]bool{"naabu": true, "httpx": true, "dnsx": true}
	for _, tool := range toolboxCatalog() {
		if want[tool.ID] != tool.NetworkScan {
			t.Errorf("%s: NetworkScan = %v, want %v", tool.ID, tool.NetworkScan, want[tool.ID])
		}
		if tool.NetworkScan && !strings.Contains(toolboxRowDetail(tool), "scans other hosts") {
			t.Errorf("%s: row detail %q does not warn", tool.ID, toolboxRowDetail(tool))
		}
	}
}
