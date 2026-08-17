// build-patch-repo assembles the JukaHub Patch repository.
//
// Usage:
//   PATCH_SIGN_KEY=<hex private key> go run ./tools/build-patch-repo --src ./patch-packages --out ./patch-repo
//
// Layout of --src:
//   <src>/<pkg-name>/manifest.json   (PackageManifest, schema 1)
//   <src>/<pkg-name>/payload/...     (files referenced by operation "src")
//
// Outputs under --out:
//   packages/<name>.zip     deterministic package archives
//   repository/index.json   signed repository index
//
// The private key is read from the PATCH_SIGN_KEY environment variable only —
// it is never written by this tool and never committed. The matching public
// key must be embedded in the application (patch_signed.go, patchRepoPublicKey).
package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// manifestSchema mirrors the app's PackageManifest (schema 1).
const manifestSchema = 1

type pkgManifest struct {
	Schema       int    `json:"schema"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Architecture string `json:"architecture"`
	Devices      []string `json:"devices"`
	OS           []string `json:"os"`
	Risk         string `json:"risk"`
	Depends      []string `json:"depends,omitempty"`
	Conflicts    []string `json:"conflicts,omitempty"`
	Provides     []string `json:"provides,omitempty"`
	Restart      string `json:"restart"`
	Operations   []struct {
		Kind string `json:"kind"`
		Src  string `json:"src,omitempty"`
		Dest string `json:"dest"`
	} `json:"operations"`
	Verify   []string `json:"verify,omitempty"`
	Rollback string   `json:"rollback"`
}

type repoIndex struct {
	Format    int         `json:"format"`
	KeyID     string      `json:"key_id"`
	IssuedAt  string      `json:"issued_at"`
	ExpiresAt string      `json:"expires_at"`
	Packages  []repoEntry `json:"packages"`
	Signature string      `json:"signature"`
}

type repoEntry struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Category      string   `json:"category,omitempty"`
	Architecture  string   `json:"architecture"`
	Devices       []string `json:"devices"`
	OS            []string `json:"os"`
	Risk          string   `json:"risk"`
	Depends       []string `json:"depends,omitempty"`
	Conflicts     []string `json:"conflicts,omitempty"`
	Provides      []string `json:"provides,omitempty"`
	Compressed    int64    `json:"compressed_size"`
	InstalledSize int64    `json:"installed_size"`
	Restart       string   `json:"restart"`
	PackageSHA    string   `json:"package_sha256"`
	Changelog     string   `json:"changelog,omitempty"`
}

func main() {
	src := flag.String("src", "./patch-packages", "package source directory")
	out := flag.String("out", "./patch-repo", "output directory")
	keyID := flag.String("key-id", "jukahub-patch-dev-2026", "key identifier")
	expiresIn := flag.Duration("expires", 90*24*time.Hour, "index validity window")
	flag.Parse()

	privHex := strings.TrimSpace(os.Getenv("PATCH_SIGN_KEY"))
	if privHex == "" {
		fatal("PATCH_SIGN_KEY environment variable (hex Ed25519 private key) is required")
	}
	priv, err := hex.DecodeString(privHex)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		fatal("PATCH_SIGN_KEY must be a 64-byte hex Ed25519 private key")
	}
	privateKey := ed25519.PrivateKey(priv)

	entries, err := buildPackages(*src, *out)
	if err != nil {
		fatal(err.Error())
	}
	idx := repoIndex{
		Format:    1,
		KeyID:     *keyID,
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: time.Now().UTC().Add(*expiresIn).Format(time.RFC3339),
		Packages:  entries,
	}
	payload, err := json.Marshal(idx)
	if err != nil {
		fatal(err.Error())
	}
	sig := ed25519.Sign(privateKey, payload)
	idx.Signature = hex.EncodeToString(sig)

	os.MkdirAll(filepath.Join(*out, "repository"), 0o755)
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		fatal(err.Error())
	}
	if err := os.WriteFile(filepath.Join(*out, "repository", "index.json"), data, 0o644); err != nil {
		fatal(err.Error())
	}
	fmt.Printf("Wrote signed repository index with %d package(s) to %s/repository/index.json\n", len(entries), *out)
	for _, e := range entries {
		fmt.Printf("  %-28s %s  (%s, %d bytes)\n", e.Name, e.Version, e.Risk, e.Compressed)
	}
}

func buildPackages(src, out string) ([]repoEntry, error) {
	entries := []repoEntry{}
	pkgDirs, err := os.ReadDir(src)
	if err != nil {
		return nil, fmt.Errorf("cannot read package source dir: %w", err)
	}
	names := map[string]bool{}
	for _, d := range pkgDirs {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		name := d.Name()
		if names[name] {
			return nil, fmt.Errorf("duplicate package %q", name)
		}
		names[name] = true
		entry, err := buildOnePackage(filepath.Join(src, name), out, name)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func buildOnePackage(dir, out, name string) (repoEntry, error) {
	var e repoEntry
	manData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return e, fmt.Errorf("%s: missing manifest.json: %w", name, err)
	}
	var man pkgManifest
	if err := json.Unmarshal(manData, &man); err != nil {
		return e, fmt.Errorf("%s: bad manifest: %w", name, err)
	}
	if man.Schema != manifestSchema || man.Name != name {
		return e, fmt.Errorf("%s: manifest schema/name mismatch", name)
	}
	// Validate referenced payload files exist.
	for _, op := range man.Operations {
		if op.Kind == "install" || op.Kind == "replace" {
			if _, err := os.Stat(filepath.Join(dir, "payload", filepath.FromSlash(op.Src))); err != nil {
				return e, fmt.Errorf("%s: payload file %q missing: %v", name, op.Src, err)
			}
		}
	}
	archiveDir := filepath.Join(out, "packages")
	os.MkdirAll(archiveDir, 0o755)
	zipPath := filepath.Join(archiveDir, name+".zip")
	if err := writeDeterministicZip(zipPath, dir, name); err != nil {
		return e, err
	}
	sha, size, err := fileSHAAndSize(zipPath)
	if err != nil {
		return e, err
	}
	installed := int64(0)
	_ = filepath.WalkDir(filepath.Join(dir, "payload"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, e2 := d.Info(); e2 == nil {
				installed += fi.Size()
			}
		}
		return nil
	})
	e = repoEntry{
		Name: man.Name, Version: man.Version, Title: man.Title,
		Description: man.Description, Architecture: man.Architecture,
		Devices: man.Devices, OS: man.OS, Risk: man.Risk,
		Depends: man.Depends, Conflicts: man.Conflicts, Provides: man.Provides,
		Compressed: size, InstalledSize: installed, Restart: man.Restart,
		PackageSHA: sha,
	}
	return e, nil
}

// writeDeterministicZip writes a zip with sorted entries and fixed times so
// the archive bytes are reproducible.
func writeDeterministicZip(path, pkgDir, name string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	// manifest.json first.
	manData, err := os.ReadFile(filepath.Join(pkgDir, "manifest.json"))
	if err != nil {
		return err
	}
	hdr := &zip.FileHeader{Name: "manifest.json", Method: zip.Deflate}
	hdr.SetModTime(time.Unix(0, 0))
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	if _, err := w.Write(manData); err != nil {
		return err
	}
	// Payload files sorted by path.
	var files []string
	_ = filepath.WalkDir(filepath.Join(pkgDir, "payload"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(filepath.Join(pkgDir, "payload"), p)
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(files)
	for _, rel := range files {
		hdr := &zip.FileHeader{Name: "payload/" + filepath.ToSlash(rel), Method: zip.Deflate}
		hdr.SetModTime(time.Unix(0, 0))
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(pkgDir, "payload", rel))
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return zw.Close()
}

func fileSHAAndSize(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "build-patch-repo:", msg)
	os.Exit(1)
}
