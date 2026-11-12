package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func telemetryIssueTestSetup(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telemetry", "launches.json")
	prevPath, prevPost, prevOnline := telemetryIssueState.pathOverride, telemetryIssuePost, telemetryIssueOnline
	prevCache, prevWrites := userConfigCache, userConfigWritesEnabled
	prevEndpoint := bugrepDefaultEndpoint
	telemetryIssueState.pathOverride = path
	userConfigCache = &UserConfig{Variables: UserVariables{Custom: map[string]interface{}{telemetryOptInKey: true}}}
	userConfigWritesEnabled = false
	telemetryIssueOnline = func() bool { return true }
	bugrepDefaultEndpoint = "https://reports.example/api/bug-report"
	t.Cleanup(func() {
		telemetryIssueState.pathOverride, telemetryIssuePost, telemetryIssueOnline = prevPath, prevPost, prevOnline
		userConfigCache, userConfigWritesEnabled = prevCache, prevWrites
		bugrepDefaultEndpoint = prevEndpoint
	})
	return path
}

func telemetryTestReport() bugrepReport {
	return bugrepReport{
		Schema: 1, Kind: "anonymous_diagnostics", ID: bugrepNewRef(),
		Created: time.Now().UTC().Format(time.RFC3339),
	}
}

func TestTelemetryIssueEveryTenLaunches(t *testing.T) {
	path := telemetryIssueTestSetup(t)
	sends := 0
	telemetryIssuePost = func(_ string, report bugrepReport) (bugrepSent, bugrepSendErr, error) {
		sends++
		if report.Kind != "anonymous_diagnostics" {
			t.Errorf("sent kind %q", report.Kind)
		}
		return bugrepSent{Issue: 42}, bugrepErrNone, nil
	}
	for i := 1; i <= 20; i++ {
		telemetryIssueLaunch(telemetryTestReport())
		progress := telemetryReadIssueProgress(path)
		if progress.Launches != i%telemetryIssueInterval || progress.Pending != nil {
			t.Fatalf("launch %d: progress = %+v", i, progress)
		}
		if sends != i/telemetryIssueInterval {
			t.Fatalf("launch %d: sent %d issue(s)", i, sends)
		}
	}
}

func TestTelemetryIssueRetriesAndOptOutClearsPending(t *testing.T) {
	path := telemetryIssueTestSetup(t)
	telemetryIssueOnline = func() bool { return false }
	for i := 0; i < telemetryIssueInterval; i++ {
		telemetryIssueLaunch(telemetryTestReport())
	}
	pending := telemetryReadIssueProgress(path).Pending
	if pending == nil {
		t.Fatal("tenth launch did not save a pending issue")
	}
	telemetryIssueOnline = func() bool { return true }
	telemetryIssuePost = func(_ string, report bugrepReport) (bugrepSent, bugrepSendErr, error) {
		if report.ID != pending.ID || report.Created != pending.Created {
			t.Errorf("retry changed the reference: %+v", report)
		}
		return bugrepSent{}, bugrepErrServer, errors.New("offline")
	}
	telemetryIssueLaunch(telemetryTestReport())
	if got := telemetryReadIssueProgress(path).Pending; got == nil || got.ID != pending.ID {
		t.Fatalf("failed send lost pending report: %+v", got)
	}
	telemetryIssuePost = func(_ string, report bugrepReport) (bugrepSent, bugrepSendErr, error) {
		if report.ID != pending.ID {
			t.Errorf("successful retry changed the reference: %+v", report)
		}
		return bugrepSent{Issue: 43}, bugrepErrNone, nil
	}
	telemetryIssueLaunch(telemetryTestReport())
	if got := telemetryReadIssueProgress(path); got.Launches != 0 || got.Pending != nil {
		t.Fatalf("successful retry did not clear pending issue: %+v", got)
	}
	telemetryIssueOnline = func() bool { return false }
	for i := 0; i < telemetryIssueInterval; i++ {
		telemetryIssueLaunch(telemetryTestReport())
	}
	if telemetryReadIssueProgress(path).Pending == nil {
		t.Fatal("next interval did not create a pending issue")
	}
	if err := setTelemetryOptedIn(&Config{}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("opt-out left pending report: %v", err)
	}
	telemetryIssueLaunch(telemetryTestReport())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("opted-out launch recreated report: %v", err)
	}
	if err := setTelemetryOptedIn(&Config{}, true); err != nil {
		t.Fatal(err)
	}
	telemetryIssueLaunch(telemetryTestReport())
	if got := telemetryReadIssueProgress(path); got.Launches != 1 || got.Pending != nil {
		t.Fatalf("opt-in did not restart count: %+v", got)
	}
}

func TestTelemetryIssueReportExcludesPrivateConfig(t *testing.T) {
	report := telemetryIssueReport(&Config{
		Version: "1.2.3", Author: "Alice", SchemaVersion: 2,
		Variables: Variables{Custom: map[string]interface{}{"apiKey": "secret-token"}},
		Scenes:    []SceneConfig{{Name: "private scene", Elements: []Element{{Text: "private text"}}}},
	}, time.Now())
	if report.Kind != "anonymous_diagnostics" || report.Log != "" ||
		!strings.Contains(report.Diagnostics, "1 scene, 1 element, schema 2") {
		t.Fatalf("bad informational report: %+v", report)
	}
	for _, secret := range []string{"Alice", "secret-token", "private scene", "private text"} {
		if strings.Contains(report.Diagnostics+report.Description+report.Title, secret) {
			t.Fatalf("informational report leaked %q", secret)
		}
	}
	if got := telemetryIssueVersion("vAlice-private-build"); got != "unknown" {
		t.Fatalf("user-authored version was included: %q", got)
	}
	if got := telemetryIssueRelease("JukaHub-20261111120000-Alice"); got != "" {
		t.Fatalf("user-authored release was included: %q", got)
	}
}
