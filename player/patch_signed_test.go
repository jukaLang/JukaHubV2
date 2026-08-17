package main

// Tests for the trust + transaction layers. They use temporary directories
// and the embedded development key only; the real system is never touched.

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func makeSignedIndex(t *testing.T, priv ed25519.PrivateKey, pkgs []RepoPkg) *SignedRepoIndex {
	t.Helper()
	idx := &SignedRepoIndex{
		Format: RepoFormatVersion, KeyID: "test-key",
		IssuedAt:  "2026-01-01T00:00:00Z",
		ExpiresAt: "2099-01-01T00:00:00Z",
		Packages:  pkgs,
	}
	payload, err := canonicalRepoJSON(idx)
	if err != nil {
		t.Fatal(err)
	}
	idx.Signature = hex.EncodeToString(ed25519.Sign(priv, payload))
	return idx
}

func TestRepoSignatureVerification(t *testing.T) {
	_, priv := testKeyPair(t)
	pub := ed25519.PrivateKey(priv).Public().(ed25519.PublicKey)

	// The real embedded key must differ from the test key, and a test-signed
	// index must be rejected by the embedded key.
	idx := makeSignedIndex(t, priv, nil)
	if err := verifyRepoSignature(idx); err == nil {
		t.Fatal("test-signed index must be rejected by the embedded key")
	}
	_ = pub

	// A self-signed index with the embedded key verifies.
	privHex := "8c74d8b38017e2a4fb2df43c2fd2242588f386eb154ca257d0729cefdeee7911b2cfeb9df673658aa19e6212684c2d16a077b21920193c746e1ef112f13de859"
	priv2, err := hex.DecodeString(privHex)
	if err != nil {
		t.Fatal(err)
	}
	idx2 := &SignedRepoIndex{
		Format: RepoFormatVersion, KeyID: patchRepoKeyID,
		IssuedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2099-01-01T00:00:00Z",
	}
	payload, _ := canonicalRepoJSON(idx2)
	idx2.Signature = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(priv2), payload))
	if err := verifyRepoSignature(idx2); err != nil {
		t.Fatalf("embedded-key index must verify: %v", err)
	}
	// Tamper with a package entry -> signature must fail.
	idx2.Packages = append(idx2.Packages, RepoPkg{Name: "x", Version: "1.0.0"})
	if err := verifyRepoSignature(idx2); err == nil {
		t.Fatal("tampered index must fail verification")
	}
}

func TestManifestValidation(t *testing.T) {
	good := &PackageManifest{
		Schema: ManifestSchema, Name: "my-pkg", Version: "1.0.0",
		Title: "My", Description: "d", Architecture: "arm64",
		Devices: []string{"TG5040"}, OS: []string{"trimui-stock"},
		Risk: "low", Restart: "none",
		Operations: []PkgOperation{
			{Kind: "install", Src: "f", Dest: "/JukaHub/f"},
		},
		Rollback: "automatic",
	}
	if err := validateManifest(good); err != nil {
		t.Fatalf("good manifest rejected: %v", err)
	}
	// Unknown schema.
	bad := *good
	bad.Schema = 99
	if err := validateManifest(&bad); err == nil {
		t.Error("unknown schema must be rejected")
	}
	// Disallowed operation.
	bad2 := *good
	bad2.Operations = []PkgOperation{{Kind: "postinst", Dest: "/x"}}
	if err := validateManifest(&bad2); err == nil {
		t.Error("arbitrary lifecycle operation must be rejected")
	}
	// Protected path.
	bad3 := *good
	bad3.Operations = []PkgOperation{{Kind: "install", Src: "f", Dest: "/Roms/x.gba"}}
	if err := validateManifest(&bad3); err == nil {
		t.Error("operation targeting Roms must be rejected")
	}
	// Bad package name.
	bad4 := *good
	bad4.Name = "Bad Name!"
	if err := validateManifest(&bad4); err == nil {
		t.Error("bad package name must be rejected")
	}
}

func TestDependencyResolution(t *testing.T) {
	pkgs := map[string]*RepoPkg{
		"a": {Name: "a", Version: "1.0.0", Depends: []string{"b", "c"}},
		"b": {Name: "b", Version: "1.0.0"},
		"c": {Name: "c", Version: "1.0.0", Depends: []string{"b"}},
	}
	plan, err := resolveInstall("a", pkgs, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(plan.Steps))
	}
	// Dependencies must be ordered before the requester.
	if plan.Steps[0].Pkg != "b" && plan.Steps[0].Pkg != "c" {
		t.Fatalf("dependency not first: %+v", plan.Steps)
	}

	// Cycle.
	cyc := map[string]*RepoPkg{
		"x": {Name: "x", Depends: []string{"y"}},
		"y": {Name: "y", Depends: []string{"x"}},
	}
	if _, err := resolveInstall("x", cyc, func(string) bool { return false }); err == nil {
		t.Fatal("cycle must be detected")
	}
	// Missing dep.
	miss := map[string]*RepoPkg{"x": {Name: "x", Depends: []string{"nope"}}}
	if _, err := resolveInstall("x", miss, func(string) bool { return false }); err == nil {
		t.Fatal("missing dependency must be detected")
	}
	// Conflict with installed.
	conf := map[string]*RepoPkg{"x": {Name: "x", Conflicts: []string{"y"}}}
	if _, err := resolveInstall("x", conf, func(name string) bool { return name == "y" }); err == nil {
		t.Fatal("conflict must be detected")
	}
}

func TestReverseDependencyCheck(t *testing.T) {
	pkgs := map[string]*RepoPkg{
		"lib": {Name: "lib"},
		"app": {Name: "app", Depends: []string{"lib"}},
	}
	installed := func(n string) bool { return n == "lib" || n == "app" }
	if err := checkReverseDependencies("lib", pkgs, installed); err == nil {
		t.Fatal("removing lib must be blocked while app depends on it")
	}
	if err := checkReverseDependencies("app", pkgs, installed); err != nil {
		t.Fatalf("removing app must be allowed: %v", err)
	}
}

func TestPatchDBSaveLoad(t *testing.T) {
	origStateDir := patchStateDir
	origFn := patchStateDirFn
	dir := t.TempDir()
	patchStateDir = dir
	patchStateDirFn = func() (string, error) { return dir, nil }
	defer func() {
		patchStateDir = origStateDir
		patchStateDirFn = origFn
	}()
	for _, sub := range []string{"backups", "journals", "downloads", "staging", "reports", "db"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o700)
	}

	// Simulate a crashed half-write: db.json.tmp exists but db.json does not.
	path, _ := patchDBPath()
	if err := os.WriteFile(path+".tmp", []byte(`{"schema":1,"installed":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// load must not read the .tmp file.
	loadPatchDB()
	if len(patchDB.Installed) != 0 {
		t.Fatal("db loaded partial data")
	}

	// Record an install and re-load.
	if err := dbRecordInstall(DBInstalledPkg{
		Name: "p1", Version: "1.0.0", Title: "P1", KeyID: "k",
		Files: []DBInstalledFile{{Path: "/tmp/f", SHA256: "abc", Origin: "package"}},
	}); err != nil {
		t.Fatal(err)
	}
	patchDB = &PatchDB{Schema: 1}
	loadPatchDB()
	if !dbIsInstalled("p1") {
		t.Fatal("db round-trip failed")
	}
	// Hold + history.
	if err := dbSetHeld("p1", true); err != nil {
		t.Fatal(err)
	}
	patchDB = &PatchDB{Schema: 1}
	loadPatchDB()
	if !dbIsHeld("p1") {
		t.Fatal("hold flag did not persist")
	}
	if err := dbAppendTransaction(DBTransaction{ID: "t1", Op: "install", Result: "committed"}); err != nil {
		t.Fatal(err)
	}
	patchDB = &PatchDB{Schema: 1}
	loadPatchDB()
	txs := dbTransactions()
	if len(txs) != 1 || txs[0].ID != "t1" {
		t.Fatalf("history did not persist: %+v", txs)
	}
}

func TestSignedPackageFlow(t *testing.T) {
	// Point state at a temp dir.
	origFn := patchStateDirFn
	dir := t.TempDir()
	patchStateDirFn = func() (string, error) { return dir, nil }
	defer func() { patchStateDirFn = origFn }()
	for _, sub := range []string{"backups", "journals", "downloads", "staging", "reports", "db", "repository", "cache"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o700)
	}

	// Build a package archive with manifest + payload.
	privHex := "8c74d8b38017e2a4fb2df43c2fd2242588f386eb154ca257d0729cefdeee7911b2cfeb9df673658aa19e6212684c2d16a077b21920193c746e1ef112f13de859"
	priv, err := hex.DecodeString(privHex)
	if err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(dir, "src", "p1")
	os.MkdirAll(filepath.Join(pkgDir, "payload"), 0o755)
	man := PackageManifest{
		Schema: ManifestSchema, Name: "p1", Version: "1.0.0", Title: "P1",
		Description: "d", Architecture: "all", Devices: []string{"TG5040"},
		OS: []string{"trimui-stock"}, Risk: "low", Restart: "none",
		Operations: []PkgOperation{{Kind: "install", Src: "f.txt", Dest: "/JukaHub/p1-f.txt"}},
		Rollback:   "automatic",
	}
	manData, _ := json.Marshal(man)
	os.WriteFile(filepath.Join(pkgDir, "manifest.json"), manData, 0o644)
	os.WriteFile(filepath.Join(pkgDir, "payload", "f.txt"), []byte("hello"), 0o644)
	// Build the archive deterministically by hand (zip, manifest + payload).
	archive := filepath.Join(dir, "p1.zip")
	if err := writeTestZip(archive, pkgDir); err != nil {
		t.Fatal(err)
	}
	sha := packageArchiveSHA(archive)

	// Signed index with the package.
	idx := &SignedRepoIndex{
		Format: RepoFormatVersion, KeyID: patchRepoKeyID,
		IssuedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2099-01-01T00:00:00Z",
		Packages: []RepoPkg{{
			Name: "p1", Version: "1.0.0", Title: "P1", Architecture: "all",
			Devices: []string{"TG5040"}, OS: []string{"trimui-stock"},
			Risk: "low", Restart: "none", PackageSHA: sha,
		}},
	}
	payload, _ := canonicalRepoJSON(idx)
	idx.Signature = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(priv), payload))
	idxData, _ := json.MarshalIndent(idx, "", "  ")
	os.WriteFile(filepath.Join(dir, "repository", "index.json"), idxData, 0o644)

	// Place the archive in the cache (offline mode) and verify digest.
	os.WriteFile(filepath.Join(dir, "cache", "p1.zip"), func() []byte { b, _ := os.ReadFile(archive); return b }(), 0o600)
	if err := verifyPackageDigest(idx, "p1", filepath.Join(dir, "cache", "p1.zip")); err != nil {
		t.Fatalf("digest verify: %v", err)
	}

	// Apply the package (dry: validate manifest loads).
	if _, err := loadSignedRepoIndex(); err != nil {
		t.Fatalf("signed index not loadable: %v", err)
	}
	// Digest verification of the cached archive.
	if err := verifyPackageDigest(idx, "p1", filepath.Join(dir, "cache", "p1.zip")); err != nil {
		t.Fatalf("cached digest verify: %v", err)
	}
}

func writeTestZip(path, pkgDir string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	man, err := os.ReadFile(filepath.Join(pkgDir, "manifest.json"))
	if err != nil {
		return err
	}
	w, err := zw.Create("manifest.json")
	if err != nil {
		return err
	}
	w.Write(man)
	w2, err := zw.Create("payload/f.txt")
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(pkgDir, "payload", "f.txt"))
	if err != nil {
		return err
	}
	w2.Write(data)
	return zw.Close()
}

var _ = strings.TrimSpace
var _ = context.Background
