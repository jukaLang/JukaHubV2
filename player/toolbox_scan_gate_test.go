package main

// The three scanning tools (naabu, httpx, dnsx) are the only Toolbox entries
// that send traffic to another machine, so they are the only ones behind the
// one-time acknowledgement. The Toolbox screen asks it in a sheet the first
// time a scanner is started. These tests pin that: local tools never ask, a
// scanner asks until the answer is recorded, Cancel records nothing, and the
// answer carries to every scanner from then on.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolboxScanConsentAsksOnce(t *testing.T) {
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
	if tbxNeedsConsent(jq, config) {
		t.Fatal("a tool that only touches this device asked for the acknowledgement")
	}
	if !tbxNeedsConsent(naabu, config) || !tbxNeedsConsent(httpx, config) {
		t.Fatal("a scanner did not ask before its first run")
	}

	toolboxAcceptAuthorizedUse(config)
	if !toolboxAuthorizedUseAccepted(config) {
		t.Fatal("the acknowledgement was not recorded")
	}
	user := userConfigFromVariables(config)
	if user == nil || user.Variables.Custom[toolboxAuthorizedUseKey] != true {
		t.Fatal("the acknowledgement is not carried into the saved user config")
	}
	if tbxNeedsConsent(naabu, config) || tbxNeedsConsent(httpx, config) {
		t.Fatal("a scanner asked again after the answer was given")
	}
}

// TestToolboxScanRunAsksFirst is the wiring: starting an installed scanner
// opens the question instead of the tool, and Cancel leaves it unanswered.
func TestToolboxScanRunAsksFirst(t *testing.T) {
	dir := t.TempDir()
	old := toolboxToolsDirOverride
	toolboxToolsDirOverride = dir
	t.Cleanup(func() {
		toolboxToolsDirOverride = old
		tbxCloseSheet()
	})
	// A fake install: presence is all toolboxInstalled checks.
	if err := os.WriteFile(filepath.Join(dir, toolboxExeNameOf("naabu")), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}

	tbxRunTool(config, "naabu")
	if !tbxSheet.open || !strings.Contains(tbxSheet.spec.title, "Before you scan") {
		t.Fatalf("starting a scanner did not ask first (sheet %q, open %v)", tbxSheet.spec.title, tbxSheet.open)
	}
	if toolboxAuthorizedUseAccepted(config) {
		t.Fatal("asking the question recorded the answer")
	}
	tbxSheetActivate(0) // Cancel
	if tbxSheet.open {
		t.Fatal("Cancel left the question up")
	}
	if toolboxAuthorizedUseAccepted(config) {
		t.Fatal("Cancel recorded the acknowledgement")
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
