package main

// secaudit_test.go — the audit toolkit catalog and its lookups.
//
// The page is a launcher: its value depends on the catalog being honest about
// what a tool is and on the resolver finding real installs instead of guessing.
// These check the catalog's shape and both resolution paths, without needing any
// of the tools to be installed.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSecAuditCatalogIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	categories := map[string]bool{"Web & API": true, "Network": true, "Validation": true}
	for _, tool := range secAuditCatalog() {
		if tool.ID == "" || strings.ContainsAny(tool.ID, " \t/\\") {
			t.Fatalf("catalog entry %q has an unusable ID", tool.Name)
		}
		if seen[tool.ID] {
			t.Fatalf("duplicate catalog ID %q", tool.ID)
		}
		seen[tool.ID] = true
		if strings.TrimSpace(tool.Name) == "" {
			t.Fatalf("%s: empty name", tool.ID)
		}
		if !categories[tool.Category] {
			t.Fatalf("%s: unexpected category %q", tool.ID, tool.Category)
		}
		if strings.TrimSpace(tool.Summary) == "" {
			t.Fatalf("%s: empty summary", tool.ID)
		}
		if strings.TrimSpace(tool.Install) == "" {
			t.Fatalf("%s: no install hint to copy when it is missing", tool.ID)
		}
		if len(tool.Binaries) == 0 && len(tool.Paths) == 0 {
			t.Fatalf("%s: nothing to look for", tool.ID)
		}
		// A GUI entry opens a window; a command-line entry runs a version
		// report. A CLI entry with no arguments would just start a scanner
		// with no target, which is exactly what this page must not do.
		if !tool.GUI() && len(tool.Version) == 0 {
			t.Fatalf("%s: command-line tool with no version arguments", tool.ID)
		}
	}
	if len(seen) < 8 {
		t.Fatalf("catalog has %d entries, want the full list", len(seen))
	}
	if _, ok := secAuditFind("nope"); ok {
		t.Fatal("secAuditFind matched an entry that does not exist")
	}
}

func TestSecAuditResolveFindsBinaryOnPath(t *testing.T) {
	dir := t.TempDir()
	name := "juka-secaudit-probe"
	if runtime.GOOS == "windows" {
		// LookPath only considers PATHEXT extensions on Windows.
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake tool: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tool := secAuditTool{ID: "probe", Name: "Probe", Binaries: []string{strings.TrimSuffix(name, ".exe")}}
	got, ok := secAuditResolve(tool)
	if !ok {
		t.Fatalf("resolve did not find %s on PATH", name)
	}
	if filepath.Base(got) != name {
		t.Fatalf("resolve returned %q, want the file named %q", got, name)
	}
}

func TestSecAuditResolveUsesInstallerPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "GUI Tool.exe")
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatalf("write fake installer path: %v", err)
	}
	t.Setenv("JUKA_AUDIT_TEST_DIR", dir)

	tool := secAuditTool{
		ID:       "gui",
		Name:     "GUI tool",
		Binaries: []string{"juka-secaudit-does-not-exist"},
		Paths:    []string{filepath.Join("$JUKA_AUDIT_TEST_DIR", "GUI Tool.exe")},
	}
	got, ok := secAuditResolve(tool)
	if !ok {
		t.Fatal("resolve ignored the expanded installer path")
	}
	if got != path {
		t.Fatalf("resolve returned %q, want %q", got, path)
	}

	// An empty or stale path must not resolve to something.
	if _, ok := secAuditResolve(secAuditTool{ID: "x", Name: "x", Paths: []string{filepath.Join(dir, "missing.exe")}}); ok {
		t.Fatal("resolve accepted a path that does not exist")
	}
}

// The page itself: one row per catalog entry, every row pressable, and the
// panel must draw on a real renderer without falling over — the row hints and
// values come from detection, which touches the filesystem.
func TestSecAuditPageRowsAndRender(t *testing.T) {
	withPowerToolsState(t, powerToolsState{page: powerPageAudit})
	rows := powerToolRows(&Config{})
	if len(rows) != len(secAuditCatalog()) {
		t.Fatalf("rows = %d, want one per catalog entry (%d)", len(rows), len(secAuditCatalog()))
	}
	for i, row := range rows {
		if row.action != powerActionSecAuditCheck || row.arg == "" {
			t.Fatalf("row %d (%q) = action %v arg %q, want an audit check", i, row.label, row.action, row.arg)
		}
		if _, ok := secAuditFind(row.arg); !ok {
			t.Fatalf("row %d (%q) points at unknown catalog ID %q", i, row.label, row.arg)
		}
		if row.hint == "" || row.detail == "" || row.value == "" {
			t.Fatalf("row %d (%q) is missing its hint, detail or value: %+v", i, row.label, row)
		}
		if !strings.Contains(row.detail, "·") {
			t.Fatalf("row %d (%q) detail %q does not name its category", i, row.label, row.detail)
		}
	}
	if rows[0].label != "Burp Suite" {
		t.Fatalf("first row = %q, want the catalog order to start with Burp Suite", rows[0].label)
	}

	// The count the menu row and the Tools row show must agree with the rows.
	installed := 0
	for _, row := range rows {
		if row.value == "Installed" {
			installed++
		}
	}
	if got := secAuditInstalledCount(); got != installed {
		t.Fatalf("installed count = %d, but %d rows say Installed", got, installed)
	}

	frame := renderPowerProbe(t, &Config{}, powerPageAudit, 0)
	if frame == nil {
		t.Fatal("the audit page did not render")
	}
}

func TestSecAuditCommandLineQuotesSpaces(t *testing.T) {
	tool := secAuditTool{ID: "t", Name: "T", Version: []string{"--version"}}
	got := secAuditCommandLine(tool, "/opt/Burp Suite/burp")
	want := `"/opt/Burp Suite/burp" --version`
	if got != want {
		t.Fatalf("command line = %q, want %q", got, want)
	}
	if got := secAuditCommandLine(tool, "/usr/bin/sqlmap"); got != "/usr/bin/sqlmap --version" {
		t.Fatalf("plain command line = %q", got)
	}
}
