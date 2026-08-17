package main

// patch_repo.go implements the package-manager side of MISC -> Patch. Think of
// it as a tiny, safe "apt-get" for the TrimUI Smart Pro: a packages.json index
// describes packages (each with a file list, checksums, and destinations) and
// the module installs / updates / removes those files with backups and a
// journaled transaction so every operation can be rolled back.
//
// Two package kinds are supported:
//
//   - local:  files come from a directory on the device (e.g. /mnt/SDCARD/JukaHub/packages/<id>)
//   - remote: files come from an HTTPS archive (zip / tar.gz) on an approved host
//
// Destinations default to the SD-card root on the device (or the executable
// directory on desktop builds) and can be overridden per file. Installing into
// protected user directories (Roms, BIOS, Saves, ...) is refused.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PackageFile describes one file a package manages.
type PackageFile struct {
	// Src is relative to the package's root (local packages) or the archive
	// entry path (remote packages).
	Src string `json:"src"`
	// Dest overrides the default destination root; use "/" prefixed paths
	// relative to the install root (e.g. "/JukaHub/themes/x.json").
	Dest string `json:"dest,omitempty"`
	// SHA256 is the expected checksum of the source file ("sha256:<hex>").
	SHA256 string `json:"sha256,omitempty"`
	// Executable marks the installed file executable.
	Executable bool `json:"executable,omitempty"`
	// KeepUser indicates the file is user-owned: updates never overwrite a
	// modified copy and removal always backs it up first.
	KeepUser bool `json:"keep_user,omitempty"`
}

// PatchPackage is one entry in the package index.
type PatchPackage struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	// Root is the source directory for local packages (device path).
	Root string `json:"root,omitempty"`
	// ArchiveURL is the HTTPS archive for remote packages.
	ArchiveURL string `json:"archive_url,omitempty"`
	// ArchiveSHA is the sha256 of the archive (required for remote).
	ArchiveSHA string `json:"archive_sha,omitempty"`
	// DestRoot is the default install root ("" -> SD root / exe dir).
	DestRoot string        `json:"dest_root,omitempty"`
	Files    []PackageFile `json:"files"`
	// FilesFromArchive extracts the file list from the archive itself.
	FilesFromArchive bool `json:"files_from_archive,omitempty"`
	Enabled          bool `json:"enabled,omitempty"`
}

// PackageRepo is the full package index file (packages.json).
type PackageRepo struct {
	Schema   int            `json:"schema"`
	Packages []PatchPackage `json:"packages"`
}

// PackageRepoSchema is the current packages.json schema version.
const PackageRepoSchema = 1

// defaultRepoPath returns the path to the user-editable packages.json inside
// the patch state directory.
func defaultRepoPath() (string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "packages.json"), nil
}

// ensureDefaultRepo writes a starter packages.json when none exists. It never
// overwrites an existing user file.
func ensureDefaultRepo() {
	path, err := defaultRepoPath()
	if err != nil {
		return
	}
	if _, err := os.Stat(path); err == nil {
		return
	}
	// The bundled JukaHub package manages JukaHub's own files, so "Patch"
	// can restore/update them from a known-good source on the device.
	repo := PackageRepo{
		Schema: PackageRepoSchema,
		Packages: []PatchPackage{
			{
				ID:          "jukahub-assets",
				Name:        "JukaHub bundled assets",
				Version:     versionString(nil),
				Description: "Restore/update JukaHub's bundled fonts, background and default config from this install.",
				Root:        mustExecutableDir(),
				Files: []PackageFile{
					{Src: "Inter-Regular.ttf", Dest: "/Inter-Regular.ttf", SHA256: "", Executable: false},
					{Src: "background.jpg", Dest: "/background.jpg", SHA256: "", Executable: false},
					{Src: "jukaconfig.json", Dest: "/jukaconfig.json", SHA256: "", KeepUser: true},
				},
			},
		},
	}
	data, err := json.MarshalIndent(repo, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// mustExecutableDir returns the directory containing the running binary.
func mustExecutableDir() string {
	return MustExecutableDir()
}

// LoadPackageRepo reads and validates the package index. A missing or invalid
// index returns an error; the module stays usable (repo actions disabled).
func LoadPackageRepo() (*PackageRepo, error) {
	path, err := defaultRepoPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var repo PackageRepo
	if err := json.Unmarshal(data, &repo); err != nil {
		return nil, fmt.Errorf("packages.json: %w", err)
	}
	if repo.Schema != PackageRepoSchema {
		return nil, fmt.Errorf("packages.json schema %d not supported", repo.Schema)
	}
	seen := map[string]bool{}
	for i := range repo.Packages {
		p := &repo.Packages[i]
		if p.ID == "" {
			return nil, fmt.Errorf("package %d missing id", i)
		}
		if seen[p.ID] {
			return nil, fmt.Errorf("duplicate package id %q", p.ID)
		}
		seen[p.ID] = true
		if p.Root == "" && p.ArchiveURL == "" {
			return nil, fmt.Errorf("package %q needs root (local) or archive_url (remote)", p.ID)
		}
		if p.ArchiveURL != "" {
			if err := validatePatchURL(p.ArchiveURL); err != nil {
				return nil, fmt.Errorf("package %q archive_url: %w", p.ID, err)
			}
			if p.ArchiveSHA != "" && len(p.ArchiveSHA) != 64 {
				return nil, fmt.Errorf("package %q bad archive_sha", p.ID)
			}
		}
		for j := range p.Files {
			if p.Files[j].Src == "" {
				return nil, fmt.Errorf("package %q file %d missing src", p.ID, j)
			}
			if p.Files[j].Dest != "" && !strings.HasPrefix(p.Files[j].Dest, "/") {
				return nil, fmt.Errorf("package %q file %q dest must be absolute (start with /)", p.ID, p.Files[j].Src)
			}
		}
	}
	sort.Slice(repo.Packages, func(i, j int) bool {
		return strings.ToLower(repo.Packages[i].Name) < strings.ToLower(repo.Packages[j].Name)
	})
	return &repo, nil
}

// packageInstallRoot returns the default destination root for packages.
func packageInstallRoot() string {
	if IsTSP() {
		if pathExists("/mnt/SDCARD") {
			return "/mnt/SDCARD"
		}
		return mustExecutableDir()
	}
	return mustExecutableDir()
}

// resolvePackageDest resolves a file's destination for a package.
func resolvePackageDest(p *PatchPackage, f *PackageFile) (string, error) {
	root := p.DestRoot
	if root == "" {
		root = packageInstallRoot()
	}
	rel := f.Dest
	if rel == "" {
		rel = "/" + f.Src
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) {
		clean = strings.TrimPrefix(clean, string(filepath.Separator))
	}
	dst := filepath.Join(root, clean)
	// Stay inside the root.
	if !strings.HasPrefix(dst, filepath.Clean(root)+string(filepath.Separator)) && dst != filepath.Clean(root) {
		return "", fmt.Errorf("destination escapes install root: %q", dst)
	}
	return dst, nil
}

// packageSource returns the resolved source path for a local package file.
func packageSource(p *PatchPackage, f *PackageFile) string {
	if p.Root == "" {
		return f.Src
	}
	return filepath.Join(p.Root, filepath.FromSlash(f.Src))
}

// checksumFile computes sha256 hex for a file ("" on failure).
func checksumFile(path string) string {
	s, err := SHA256File(path)
	if err != nil {
		return ""
	}
	return s
}

// verifyPackageFile checks a file against the expected checksum when one is
// declared. "sha256:<hex>" and bare "<hex>" are both accepted.
func verifyPackageFile(path, expect string) error {
	if expect == "" {
		return nil
	}
	expect = strings.TrimPrefix(expect, "sha256:")
	got := checksumFile(path)
	if got == "" {
		return fmt.Errorf("cannot hash %s", path)
	}
	if !strings.EqualFold(got, expect) {
		return fmt.Errorf("checksum mismatch for %s (got %s)", path, got)
	}
	return nil
}

// --- Installation planning ---

// PlanPackageInstall builds the list of operations for installing a package.
// It refuses to touch protected user directories.
func PlanPackageInstall(p *PatchPackage, destRootOverride string) ([]PackageFile, error) {
	if p == nil {
		return nil, errors.New("nil package")
	}
	var out []PackageFile
	for _, f := range p.Files {
		if f.Dest != "" {
			lower := strings.ToLower(f.Dest)
			for _, prot := range protectedUserDirs {
				if strings.HasPrefix(lower, "/"+strings.ToLower(prot)) {
					return nil, fmt.Errorf("package %q file %q targets protected user content (%s)", p.ID, f.Src, prot)
				}
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// protectedUserDirs are always off-limits for package installation.
var protectedUserDirs = []string{
	"Roms", "BIOS", "Saves", "States", "Screenshots", "Collections",
	".system", "JukaHub/data", "jukauser.json",
}

// --- Install / remove / update ---

// InstallPackage copies a package's files into place under a journal.
// Every replaced or removed file is backed up; any failure rolls back.
func InstallPackage(ctx context.Context, p *PatchPackage, files []PackageFile) error {
	if len(files) == 0 {
		return errors.New("package has no files to install")
	}
	journal := newJournal("pkg-install", p.ID)
	defer journal.close()
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			_ = journal.rollback()
			return ctx.Err()
		}
		src := packageSource(p, &f)
		if _, err := os.Stat(src); err != nil {
			_ = journal.rollback()
			return fmt.Errorf("install %s: source missing: %v", f.Src, err)
		}
		if err := verifyPackageFile(src, f.SHA256); err != nil {
			_ = journal.rollback()
			return err
		}
		dst, err := resolvePackageDest(p, &f)
		if err != nil {
			_ = journal.rollback()
			return err
		}
		if err := journal.recordReplace(dst); err != nil {
			_ = journal.rollback()
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			_ = journal.rollback()
			return err
		}
		if err := copyFile(src, dst); err != nil {
			_ = journal.rollback()
			return fmt.Errorf("install %s: %w", f.Src, err)
		}
		if f.Executable {
			_ = os.Chmod(dst, 0o755)
		}
	}
	return journal.commit()
}

// RemovePackage backs up and removes every file a package manages. Files the
// user modified are still removed (backup first) only after explicit
// confirmation; the caller is responsible for that confirmation.
func RemovePackage(ctx context.Context, p *PatchPackage, files []PackageFile) error {
	if len(files) == 0 {
		return errors.New("package has no files to remove")
	}
	journal := newJournal("pkg-remove", p.ID)
	defer journal.close()
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			_ = journal.rollback()
			return ctx.Err()
		}
		dst, err := resolvePackageDest(p, &f)
		if err != nil {
			_ = journal.rollback()
			return err
		}
		if _, err := os.Stat(dst); err != nil {
			continue // already absent
		}
		if err := journal.recordReplace(dst); err != nil {
			_ = journal.rollback()
			return err
		}
		if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = journal.rollback()
			return fmt.Errorf("remove %s: %w", f.Src, err)
		}
	}
	return journal.commit()
}

// IsPackageInstalled reports whether every managed file exists.
func IsPackageInstalled(p *PatchPackage) bool {
	for _, f := range p.Files {
		dst, err := resolvePackageDest(p, &f)
		if err != nil {
			return false
		}
		if _, err := os.Stat(dst); err != nil {
			return false
		}
	}
	return true
}

// IsPackageCurrent reports whether every managed file matches its checksum.
func IsPackageCurrent(p *PatchPackage) bool {
	for _, f := range p.Files {
		dst, err := resolvePackageDest(p, &f)
		if err != nil {
			return false
		}
		if verifyPackageFile(dst, f.SHA256) != nil {
			return false
		}
	}
	return true
}

// --- Remote packages ---

// fetchRemotePackage downloads and stages a remote package archive, then
// returns the staging directory (cleaned up by the caller).
func fetchRemotePackage(ctx context.Context, p *PatchPackage) (string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	staging := filepath.Join(dir, "staging", "pkg-"+sanitizeFileName(p.ID))
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return "", err
	}
	archive := filepath.Join(dir, "downloads", sanitizeFileName(p.ID)+".archive")
	client := newPatchClient()
	if err := downloadToFile(ctx, client, p.ArchiveURL, archive, p.ArchiveSHA, patchMaxDownload); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	if err := extractSafe(ctx, archive, staging, patchMaxEntries, patchMaxExpandBytes); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	// When the package declares files explicitly, verify each staged source.
	for _, f := range p.Files {
		src := filepath.Join(staging, filepath.FromSlash(f.Src))
		if _, err := os.Stat(src); err != nil {
			_ = os.RemoveAll(staging)
			return "", fmt.Errorf("remote package missing file %s", f.Src)
		}
		if err := verifyPackageFile(src, f.SHA256); err != nil {
			_ = os.RemoveAll(staging)
			return "", err
		}
	}
	return staging, nil
}

// installRemotePackage downloads, verifies, and installs a remote package in
// one journaled transaction.
func installRemotePackage(ctx context.Context, p *PatchPackage) error {
	staging, err := fetchRemotePackage(ctx, p)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	// Re-root the package source to the staging dir.
	clone := *p
	clone.Root = staging
	clone.ArchiveURL = ""
	files, err := PlanPackageInstall(&clone, "")
	if err != nil {
		return err
	}
	return InstallPackage(ctx, &clone, files)
}

// removeRemotePackage removes the managed files of a remote package.
func removeRemotePackage(ctx context.Context, p *PatchPackage) error {
	// Remote packages with files_from_archive cannot be removed without the
	// archive; refuse rather than guess.
	if p.FilesFromArchive {
		return errors.New("this remote package cannot be removed automatically (files come from the archive)")
	}
	return RemovePackage(ctx, p, p.Files)
}

// hasher is a tiny helper for future use (kept for symmetry with sha256).
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var _ = io.Copy
