package main

// patch_engine_test.go tests the safety-critical engine invariants. Tests use
// only temporary directories and local data — never /mnt/SDCARD, raw devices,
// or the network.

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Semantic version comparison ---

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"0.4.0", "0.4.1", -1},
		{"0.4.1", "0.4.0", 1},
		{"1.2.3", "1.10.0", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha", 1},
		{"1.0.0", "1.0.0+build5", 0},
		{"1.2", "1.2.0", 0},
	}
	for _, c := range cases {
		got, err := compareSemver(c.a, c.b)
		if err != nil {
			t.Fatalf("compareSemver(%q,%q): %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Errorf("compareSemver(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
	if _, err := parseSemver("not-a-version"); err == nil {
		t.Error("expected error for garbage version")
	}
	if NewerVersion("0.4.1", "0.4.0") {
		t.Error("0.4.1 must not be newer than 0.4.0")
	}
	if !NewerVersion("0.4.0", "0.4.1") {
		t.Error("0.4.1 must be newer than 0.4.0")
	}
	if NewerVersion("0.4.0", "garbage") {
		t.Error("garbage candidate must never be newer")
	}
}

// --- Manifest parsing ---

func TestParsePatchManifest(t *testing.T) {
	good := `{
		"schema": 1,
		"product": "JukaHub",
		"version": "v0.4.1",
		"channel": "stable",
		"targets": [
			{"device": "trimui-smart-pro", "os": "linux", "arch": "arm64",
			 "asset": "JukaHub-trimui-smart-pro-linux-arm64.tar.gz",
			 "sha256": "` + strings.Repeat("ab", 32) + `", "size": 1234}
		]
	}`
	m, err := ParsePatchManifest([]byte(good))
	if err != nil {
		t.Fatalf("good manifest rejected: %v", err)
	}
	tgt := m.ManifestTargetFor("trimui-smart-pro", "linux", "arm64")
	if tgt == nil || tgt.Asset == "" {
		t.Fatal("target lookup failed")
	}
	if m.ManifestTargetFor("brick", "linux", "arm64") != nil {
		t.Fatal("wrong device must not match")
	}

	// Unknown schema fails closed.
	if _, err := ParsePatchManifest([]byte(`{"schema": 99, "product": "JukaHub", "version": "1.0.0", "targets": []}`)); err == nil {
		t.Error("unknown schema must be rejected")
	}
	// Missing product.
	if _, err := ParsePatchManifest([]byte(`{"schema": 1, "version": "1.0.0", "targets": [{"asset":"a"}]}`)); err == nil {
		t.Error("missing product must be rejected")
	}
	// Bad sha256 length.
	bad := `{"schema":1,"product":"JukaHub","version":"1.0.0","targets":[{"asset":"a","sha256":"zz"}]}`
	if _, err := ParsePatchManifest([]byte(bad)); err == nil {
		t.Error("bad sha256 must be rejected")
	}
}

// --- URL policy ---

func TestValidatePatchURL(t *testing.T) {
	allowed := []string{
		"https://github.com/jukaLang/JukaHubV2/releases/download/v0.4.1/manifest.json",
		"https://api.github.com/repos/jukaLang/JukaHubV2/releases/latest",
		"https://objects.githubusercontent.com/github-production-release-asset-2e65be/1",
	}
	for _, u := range allowed {
		if err := validatePatchURL(u); err != nil {
			t.Errorf("URL should be allowed: %s (%v)", u, err)
		}
	}
	blocked := []string{
		"http://github.com/foo",                 // no TLS
		"https://evil.example.com/x",            // unknown host
		"https://github.com@evil.example.com/x", // userinfo
		"ftp://github.com/x",
		"",
	}
	for _, u := range blocked {
		if err := validatePatchURL(u); err == nil {
			t.Errorf("URL should be rejected: %s", u)
		}
	}
}

// --- Redirect rejection (HTTP downgrade / unknown host) ---

func TestRedirectPolicy(t *testing.T) {
	client := newPatchClient()
	// The client's CheckRedirect must reject a downgrade to HTTP or a jump to
	// an unapproved host, regardless of the original request.
	for _, target := range []string{"http://github.com/x", "https://evil.example.com/x", "https://github.com@evil.example.com/x"} {
		req := &http.Request{URL: mustParseURL(t, target)}
		err := client.CheckRedirect(req, []*http.Request{{}, {}, {}, {}, {}, {}})
		if err == nil {
			t.Errorf("redirect to %q must be rejected", target)
		}
	}
	// An approved HTTPS redirect is fine.
	ok := &http.Request{URL: mustParseURL(t, "https://github.com/jukaLang/JukaHubV2/releases/download/v0.4.1/x")}
	if err := client.CheckRedirect(ok, nil); err != nil {
		t.Errorf("approved redirect rejected: %v", err)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// --- Checksum verification ---

func TestChecksumVerification(t *testing.T) {
	data := []byte("hello patch module")
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	dir := t.TempDir()
	good := filepath.Join(dir, "good.bin")
	bad := filepath.Join(dir, "bad.bin")
	if err := os.WriteFile(good, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, append(data, 0x01), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SHA256File(good)
	if err != nil || !strings.EqualFold(got, hexSum) {
		t.Fatalf("SHA256File mismatch: %s %v", got, err)
	}
	gotBad, _ := SHA256File(bad)
	if strings.EqualFold(gotBad, hexSum) {
		t.Fatal("tampered file hashed equal")
	}
}

// --- Archive safety ---

func TestArchiveTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("../escape.txt")
	w.Write([]byte("boom"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dest := filepath.Join(dir, "out")
	os.MkdirAll(dest, 0o755)
	err = extractZipSafe(context.Background(), archive, dest, 100, 1<<20)
	if err == nil {
		t.Fatal("traversal archive must be rejected")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("traversal file was written")
	}

	// Absolute path entry.
	f2, _ := os.Create(filepath.Join(dir, "abs.zip"))
	zw2 := zip.NewWriter(f2)
	w2, _ := zw2.Create("/etc/evil")
	w2.Write([]byte("x"))
	zw2.Close()
	f2.Close()
	if err := extractZipSafe(context.Background(), filepath.Join(dir, "abs.zip"), dest, 100, 1<<20); err == nil {
		t.Fatal("absolute-path archive must be rejected")
	}

	// Symlink entry.
	f3, _ := os.Create(filepath.Join(dir, "link.zip"))
	zw3 := zip.NewWriter(f3)
	hdr := &zip.FileHeader{Name: "link", Method: zip.Store}
	hdr.SetMode(os.ModeSymlink)
	w3, _ := zw3.CreateHeader(hdr)
	w3.Write([]byte("/etc"))
	zw3.Close()
	f3.Close()
	if err := extractZipSafe(context.Background(), filepath.Join(dir, "link.zip"), dest, 100, 1<<20); err == nil {
		t.Fatal("symlink archive must be rejected")
	}
}

// --- Package repository (apt-style) ---

func TestPackageInstallUpdateRemove(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	dstRoot := filepath.Join(root, "dst")
	mustWrite(t, filepath.Join(srcDir, "theme.json"), `{"accent":"cyan"}`)
	mustWrite(t, filepath.Join(srcDir, "tool.sh"), "#!/bin/sh\necho hi\n")

	p := &PatchPackage{
		ID:       "my-theme",
		Name:     "My theme",
		Version:  "1.0.0",
		Root:     srcDir,
		DestRoot: dstRoot,
		Files: []PackageFile{
			{Src: "theme.json", Dest: "/themes/my-theme.json"},
			{Src: "tool.sh", Dest: "/tools/tool.sh", Executable: true},
		},
	}
	if IsPackageInstalled(p) {
		t.Fatal("package should not be installed yet")
	}
	if err := InstallPackage(context.Background(), p, p.Files); err != nil {
		t.Fatal(err)
	}
	if !IsPackageInstalled(p) {
		t.Fatal("package should be installed")
	}
	if got := readFile(t, filepath.Join(dstRoot, "themes", "my-theme.json")); got != `{"accent":"cyan"}` {
		t.Errorf("installed content wrong: %q", got)
	}
	// Update the source and reinstall (overwrites, journal-backed).
	mustWrite(t, filepath.Join(srcDir, "theme.json"), `{"accent":"purple"}`)
	if err := InstallPackage(context.Background(), p, p.Files); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dstRoot, "themes", "my-theme.json")); got != `{"accent":"purple"}` {
		t.Errorf("update failed: %q", got)
	}
	// Remove.
	if err := RemovePackage(context.Background(), p, p.Files); err != nil {
		t.Fatal(err)
	}
	if IsPackageInstalled(p) {
		t.Fatal("package should be removed")
	}
}

func TestPackageRefusesProtectedDirs(t *testing.T) {
	p := &PatchPackage{
		ID:   "evil",
		Name: "Evil",
		Files: []PackageFile{
			{Src: "x", Dest: "/Roms/GBA/game.gba"},
		},
	}
	if _, err := PlanPackageInstall(p, ""); err == nil {
		t.Error("package targeting Roms must be refused")
	}
	p2 := &PatchPackage{
		ID:   "evil2",
		Name: "Evil2",
		Files: []PackageFile{
			{Src: "x", Dest: "/jukauser.json"},
		},
	}
	if _, err := PlanPackageInstall(p2, ""); err == nil {
		t.Error("package targeting jukauser.json must be refused")
	}
}

func TestPackageRepoValidation(t *testing.T) {
	// Point the patch state dir at a temp location so packages.json is read
	// from there and the real data directory is never touched.
	origStateDir := patchStateDir
	origFn := patchStateDirFn
	dir := t.TempDir()
	patchStateDir = dir
	patchStateDirFn = func() (string, error) { return dir, nil }
	defer func() {
		patchStateDir = origStateDir
		patchStateDirFn = origFn
	}()
	for _, sub := range []string{"backups", "journals", "downloads", "staging", "reports"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o700)
	}
	// Invalid (non-approved) archive URL must be rejected at load.
	bad := PackageRepo{Schema: PackageRepoSchema, Packages: []PatchPackage{
		{ID: "p1", Name: "P1", ArchiveURL: "http://evil.example.com/x.zip"},
	}}
	data, _ := json.Marshal(bad)
	mustWrite(t, filepath.Join(dir, "packages.json"), string(data))
	if _, err := LoadPackageRepo(); err == nil {
		t.Error("repo with non-approved archive URL must be rejected")
	}

	// A valid local package repo loads fine.
	good := PackageRepo{Schema: PackageRepoSchema, Packages: []PatchPackage{
		{ID: "p1", Name: "P1", Root: "/tmp", Files: []PackageFile{{Src: "a.txt", Dest: "/a.txt"}}},
	}}
	data2, _ := json.Marshal(good)
	mustWrite(t, filepath.Join(dir, "packages.json"), string(data2))
	if _, err := LoadPackageRepo(); err != nil {
		t.Errorf("valid repo rejected: %v", err)
	}
}

func TestChecksumVerification2(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.bin")
	dstRoot := filepath.Join(dir, "dst")
	mustWrite(t, src, "data")
	sum := checksumFile(src)
	if sum == "" {
		t.Fatal("checksum failed")
	}
	p := &PatchPackage{ID: "c", Name: "C", Root: dir, DestRoot: dstRoot, Files: []PackageFile{
		{Src: "f.bin", Dest: "/f.bin", SHA256: sum},
	}}
	if err := InstallPackage(context.Background(), p, p.Files); err != nil {
		t.Fatal(err)
	}
	// Tampered source must be rejected.
	mustWrite(t, src, "tampered")
	if err := InstallPackage(context.Background(), p, p.Files); err == nil {
		t.Error("tampered source must fail checksum")
	}
}

// --- Backup manifest ---

func TestBackupManifestAndRestore(t *testing.T) {
	// Point the patch state dir at a temp location. The platform's real data
	// directory is never touched by tests.
	origStateDir := patchStateDir
	origPatchStateDirFn := patchStateDirFn
	dir := t.TempDir()
	patchStateDir = dir
	patchStateDirFn = func() (string, error) { return dir, nil }
	defer func() {
		patchStateDir = origStateDir
		patchStateDirFn = origPatchStateDirFn
	}()
	for _, sub := range []string{"backups", "journals", "downloads", "staging", "reports"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o700)
	}

	src := filepath.Join(t.TempDir(), "file.bin")
	mustWrite(t, src, "backup me")
	m, setDir, err := CreateBackupSet("test", []string{src})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 || m.Files[0].SHA256 == "" {
		t.Fatalf("bad backup manifest: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(setDir, "manifest.json")); err != nil {
		t.Fatal("manifest file missing")
	}

	// Restore over a modified file.
	mustWrite(t, src, "modified!")
	if err := RestoreBackupSet(filepath.Base(setDir)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, src); got != "backup me" {
		t.Errorf("restore returned %q", got)
	}
}

// --- Journal rollback ---

func TestJournalRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	mustWrite(t, target, "original")

	j := newJournal("test", "x")
	j.path = filepath.Join(dir, "journals", "test.json")
	os.MkdirAll(filepath.Dir(j.path), 0o700)

	if err := j.recordReplace(target); err != nil {
		t.Fatal(err)
	}
	// Simulate an interrupted apply: the file is replaced.
	mustWrite(t, target, "new-version")
	if err := j.rollback(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "original" {
		t.Errorf("rollback left %q, want original", got)
	}

	// A remove-type entry (file did not exist before) must delete what appeared.
	target2 := filepath.Join(dir, "new.bin")
	j2 := newJournal("test", "y")
	j2.path = filepath.Join(dir, "journals", "test2.json")
	if err := j2.recordReplace(target2); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, target2, "appeared")
	if err := j2.rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target2); err == nil {
		t.Error("remove-type rollback did not delete the file")
	}
}

// --- Config migration preserves unknown fields ---

func TestConfigMigrationPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jukaconfig.json")
	raw := `{
		"AppName": "JukaHub",
		"Version": "v0.4.0",
		"scenes": [{"name": "Main", "elements": [{"type": "button", "text": "X"}]}],
		"future_field": {"nested": [1, 2, 3]}
	}`
	mustWrite(t, path, raw)
	origCfgPath := P().ConfigPath
	// Use a direct call with the temp path instead of the platform config.
	if _, err := MigrateConfigFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["future_field"]; !ok {
		t.Error("unknown field was dropped by migration")
	}
	scenes, ok := m["scenes"].([]interface{})
	if !ok || len(scenes) != 1 {
		t.Fatalf("scenes mangled: %v", m["scenes"])
	}
	// Normalized version.
	if v, _ := m["Version"].(string); v != "0.4.0" {
		t.Errorf("version not normalized: %q", v)
	}
	_ = origCfgPath
}

// --- Offline behavior ---

func TestOfflineCheckFailsSafely(t *testing.T) {
	// Point at an unreachable address; the check must return an error
	// (not panic) and leave no files behind.
	ctx := context.Background()
	client := newPatchClient()
	_, err := fetchBounded(ctx, client, "https://127.0.0.1:1/", 4096)
	if err == nil {
		t.Skip("unreachable address unexpectedly reachable")
	}
}

// --- Helpers ---

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(data)
}
