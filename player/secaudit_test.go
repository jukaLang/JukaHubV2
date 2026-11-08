package main

// secaudit_test.go — the Audit toolkit catalog, its per-system recipes, the
// resolver, and the parts of the installer that run without a network.
//
// The screen's value depends on the catalog being honest: every tool says how
// it installs on Windows, on the handheld and on desktop Linux, or why it
// cannot run there, and the resolver finds real installs instead of guessing.

import (
	"archive/zip"
	"context"
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
			t.Fatalf("%s: no install hint", tool.ID)
		}
		if len(tool.Binaries) == 0 && len(tool.Paths) == 0 {
			t.Fatalf("%s: nothing to look for", tool.ID)
		}
		// A command-line entry must have a version report: the Version
		// button runs it, and with no arguments a scanner would just start.
		if !tool.GUI() && len(tool.Version) == 0 {
			t.Fatalf("%s: command-line tool with no version arguments", tool.ID)
		}
		for _, p := range []auditPlatform{auditPlatformWindows, auditPlatformHandheld, auditPlatformLinux} {
			r, ok := tool.Recipes[p]
			if !ok {
				t.Errorf("%s: no recipe for %s", tool.ID, p)
				continue
			}
			switch r.Kind {
			case auditKindNone:
				if strings.TrimSpace(r.Reason) == "" {
					t.Errorf("%s/%s: not available, but no reason is given", tool.ID, p)
				}
			case auditKindWinget:
				if p != auditPlatformWindows || r.WingetID == "" || strings.ContainsAny(r.WingetID, " /\\") {
					t.Errorf("%s/%s: bad winget recipe %+v", tool.ID, p, r)
				}
			case auditKindGitHub:
				if !allowedToolRepos[r.Repo] || len(r.Assets) == 0 || r.Entry == "" {
					t.Errorf("%s/%s: GitHub recipe must use an approved repo, asset tokens and an entry: %+v", tool.ID, p, r)
				}
			case auditKindArchive, auditKindScript:
				if !strings.HasPrefix(r.URL, "https://") || r.Entry == "" {
					t.Errorf("%s/%s: download recipe needs an https URL and an entry: %+v", tool.ID, p, r)
				}
				if r.Kind == auditKindScript && r.Needs != "python" && r.Needs != "perl" {
					t.Errorf("%s/%s: script recipe needs python or perl, got %q", tool.ID, p, r.Needs)
				}
			case auditKindManual:
				if !strings.HasPrefix(r.Page, "https://") {
					t.Errorf("%s/%s: manual recipe needs an https download page", tool.ID, p)
				}
			}
			if r.Page != "" && !strings.HasPrefix(r.Page, "https://") {
				t.Errorf("%s/%s: page %q is not https", tool.ID, p, r.Page)
			}
		}
	}
	if len(seen) < 8 {
		t.Fatalf("catalog has %d entries, want the full list", len(seen))
	}
	if _, ok := secAuditFind("nope"); ok {
		t.Fatal("secAuditFind matched an entry that does not exist")
	}
	if first := secAuditCatalog()[0]; first.Name != "Burp Suite" {
		t.Fatalf("first entry = %q, want the catalog order to start with Burp Suite", first.Name)
	}
}

// On the handheld the desktop applications say why they cannot run, and what
// can run there is offered: the static arm64 Nuclei build.
func TestAuditHandheldOnlyOffersWhatRuns(t *testing.T) {
	prev := auditPlatformOverride
	auditPlatformOverride = auditPlatformHandheld
	t.Cleanup(func() { auditPlatformOverride = prev })
	if auditCurrentPlatform() != auditPlatformHandheld {
		t.Fatal("the platform override was ignored")
	}
	for _, id := range []string{"burp", "zap", "wireshark", "hashcat", "metasploit"} {
		tool, ok := secAuditFind(id)
		if !ok {
			t.Fatalf("%s missing from the catalog", id)
		}
		if r := tool.Recipe(auditPlatformHandheld); r.Kind != auditKindNone || r.Reason == "" {
			t.Errorf("%s: the handheld must say why it is not available, got %+v", id, r)
		}
	}
	nuclei, ok := secAuditFind("nuclei")
	if !ok {
		t.Fatal("nuclei missing from the catalog")
	}
	r := nuclei.Recipe(auditPlatformHandheld)
	if r.Kind != auditKindGitHub {
		t.Fatalf("nuclei on the handheld = %+v, want the GitHub arm64 release", r)
	}
	assets := []releaseAsset{
		{Name: "nuclei_3.3.5_macOS_arm64.zip", URL: "mac"},
		{Name: "nuclei_3.3.5_checksums.txt", URL: "sums"},
		{Name: "nuclei_3.3.5_linux_arm64.zip", URL: "want"},
		{Name: "nuclei_3.3.5_linux_amd64.zip", URL: "x86"},
	}
	if picked, ok := pickToolAsset(assets, r.Assets); !ok || picked.URL != "want" {
		t.Fatalf("picked %+v, want the linux arm64 zip", picked)
	}
	if sum, single, ok := toolboxChecksumAsset(assets, "nuclei_3.3.5_linux_arm64.zip"); !ok || single || sum.URL != "sums" {
		t.Fatalf("checksum list = %+v single=%v ok=%v, want the release's checksums.txt", sum, single, ok)
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

// What JukaHub installed is found first, and the record survives a reload.
func TestSecAuditResolvePrefersJukaHubInstall(t *testing.T) {
	dir := t.TempDir()
	prevRoot := auditInstallRootOverride
	auditInstallRootOverride = dir
	resetRecords := func() {
		auditRecords.mu.Lock()
		auditRecords.loaded = false
		auditRecords.mu.Unlock()
	}
	resetRecords()
	t.Cleanup(func() {
		auditInstallRootOverride = prevRoot
		resetRecords()
	})

	prog := filepath.Join(dir, "john", "run", "john.exe")
	if err := os.MkdirAll(filepath.Dir(prog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prog, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	auditSaveRecord("john", auditRecord{Kind: "archive", Path: prog})
	tool, _ := secAuditFind("john")
	if got, ok := secAuditResolve(tool); !ok || got != prog {
		t.Fatalf("resolve = %q %v, want the JukaHub install %q", got, ok, prog)
	}
	resetRecords()
	if rec, ok := auditRecordFor("john"); !ok || rec.Path != prog {
		t.Fatalf("record after reload = %+v %v", rec, ok)
	}
	auditForgetRecord("john")
	if _, ok := auditRecordFor("john"); ok {
		t.Fatal("the record survived being forgotten")
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

// The Terminal opens with the program and its help command, the path quoted
// when it has spaces; nothing runs until Enter.
func TestAuditTerminalCommandQuotesPaths(t *testing.T) {
	tool := secAuditTool{ID: "nmap-test", Name: "Nmap", Start: []string{"-h"}, Version: []string{"--version"}}
	got := auditTerminalCommand(tool, `C:\Program Files\Nmap\nmap.exe`)
	if want := `"C:\Program Files\Nmap\nmap.exe" -h`; got != want {
		t.Fatalf("terminal command = %q, want %q", got, want)
	}
}

func writeAuditTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
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

// A GitHub branch archive unpacks without its "<repo>-<branch>/" folder, empty
// files included (a Python package's __init__.py), and the program is found by
// name wherever the archive keeps it.
func TestAuditExtractTree(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "src.zip")
	writeAuditTestZip(t, archive, map[string]string{
		"proj-master/sqlmap.py":          "print('ok')",
		"proj-master/lib/__init__.py":    "",
		"proj-master/run/john.exe":       "bin",
		"proj-master/run64/john.exe":     "bin64",
		"proj-master/doc/README.txt":     "readme",
		"proj-master/lib/core/common.py": "x = 1",
	})
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := auditExtractTree(context.Background(), archive, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "sqlmap.py")); err != nil || string(data) != "print('ok')" {
		t.Fatalf("sqlmap.py = %q %v", data, err)
	}
	if info, err := os.Stat(filepath.Join(dest, "lib", "__init__.py")); err != nil || info.Size() != 0 {
		t.Fatalf("empty __init__.py was not unpacked: %v", err)
	}
	if got := auditFindEntry(dest, "john.exe"); got != "run64/john.exe" {
		t.Fatalf("entry = %q, want the 64-bit build", got)
	}
	if got := auditFindEntry(dest, "missing.exe"); got != "" {
		t.Fatalf("a missing program was found at %q", got)
	}
}

func TestAuditExtractTreeRefusesTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.zip")
	writeAuditTestZip(t, archive, map[string]string{"../evil.txt": "x"})
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := auditExtractTree(context.Background(), archive, dest); err == nil {
		t.Fatal("an archive that writes outside its folder was unpacked")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatal("the traversal entry was written")
	}
}

func TestAuditCommonTopDir(t *testing.T) {
	mk := func(names ...string) []*zip.File {
		out := make([]*zip.File, 0, len(names))
		for _, n := range names {
			out = append(out, &zip.File{FileHeader: zip.FileHeader{Name: n}})
		}
		return out
	}
	if got := auditCommonTopDir(mk("a-main/x", "a-main/y/z")); got != "a-main/" {
		t.Fatalf("top dir = %q", got)
	}
	if got := auditCommonTopDir(mk("a-main/x", "b/y")); got != "" {
		t.Fatalf("two top folders = %q, want none", got)
	}
	if got := auditCommonTopDir(mk("x.exe", "a/y")); got != "" {
		t.Fatalf("a root file = %q, want none", got)
	}
}

// winget redraws its progress bars with carriage returns; the sheet shows the
// last state of each line and no bare spinner lines.
func TestAuditCleanConsole(t *testing.T) {
	in := "Found Wireshark [WiresharkFoundation.Wireshark]\r\n" +
		"  ██████▒▒▒▒  40%\r  ██████████ 100%\n" +
		"-\n\\\n|\n" +
		"Successfully installed\n"
	got := auditCleanConsole(in)
	if strings.Contains(got, "40%") {
		t.Fatalf("an overwritten progress state survived:\n%s", got)
	}
	if !strings.Contains(got, "Successfully installed") || !strings.Contains(got, "100%") {
		t.Fatalf("lost the real output:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "-" || strings.TrimSpace(line) == "|" {
			t.Fatalf("a spinner line survived:\n%s", got)
		}
	}
}
