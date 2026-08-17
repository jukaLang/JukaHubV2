package main

// patch_signed.go adds the trust layer to the Patch package manager: the
// repository index is signed with Ed25519 and every package digest is verified
// before install. HTTPS alone is never treated as authenticity.
//
// The embedded public key below is the development/default key for the
// JukaHub-maintained Patch repository. The matching private key lives only in
// the offline repository tooling (never in this repository, never in the app).
// A real deployment replaces patchRepoPublicKey with the official key; the
// tooling emits a clear warning when a package was signed by another key.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// patchRepoKeyID identifies the embedded verification key.
const patchRepoKeyID = "jukahub-patch-dev-2026"

// patchRepoPublicKey is the Ed25519 public key (hex) that verifies signed
// repository metadata. This is the development key; the official key is
// published with the production Patch repository.
var patchRepoPublicKey, _ = hex.DecodeString(
	"b2cfeb9df673658aa19e6212684c2d16a077b21920193c746e1ef112f13de859",
)

// RepoFormatVersion is the signed repository index format version.
const RepoFormatVersion = 1

// SignedRepoIndex is the signed repository metadata file (index.json).
type SignedRepoIndex struct {
	Format    int       `json:"format"`
	KeyID     string    `json:"key_id"`
	IssuedAt  string    `json:"issued_at"`
	ExpiresAt string    `json:"expires_at"`
	Packages  []RepoPkg `json:"packages"`
	// Signature is over the canonical JSON of everything except itself.
	Signature string `json:"signature"`
}

// RepoPkg is one package entry in the signed index.
type RepoPkg struct {
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

// ManifestSchema is the package manifest schema version.
const ManifestSchema = 1

// PkgOperation is one declarative file operation.
type PkgOperation struct {
	// Kind is one of: install, replace, remove, patch, executable, chmod.
	Kind string `json:"kind"`
	// Src is a path inside the package payload (for install/replace).
	Src string `json:"src,omitempty"`
	// Dest is the absolute destination path.
	Dest string `json:"dest"`
	// BaselineSHA is the expected current hash for replace (approval gate).
	BaselineSHA string `json:"baseline_sha256,omitempty"`
	// Value/Key are used by the patch kind (structured JSON or key=value).
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
	// Mode for executable/chmod.
	Mode string `json:"mode,omitempty"`
}

// PackageManifest is the declarative manifest inside a package archive.
type PackageManifest struct {
	Schema       int            `json:"schema"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	Architecture string         `json:"architecture"`
	Devices      []string       `json:"devices"`
	OS           []string       `json:"os"`
	Risk         string         `json:"risk"`
	Depends      []string       `json:"depends,omitempty"`
	Conflicts    []string       `json:"conflicts,omitempty"`
	Provides     []string       `json:"provides,omitempty"`
	Restart      string         `json:"restart"`
	Operations   []PkgOperation `json:"operations"`
	Verify       []string       `json:"verify,omitempty"`
	Rollback     string         `json:"rollback"`
}

// allowedPkgOps is the strict operation allowlist. Unrecognized operations
// (including any lifecycle script) are rejected outright.
var allowedPkgOps = map[string]bool{
	"install":    true,
	"replace":    true,
	"remove":     true,
	"patch":      true,
	"executable": true,
	"chmod":      true,
}

// safePkgName matches the restricted package-name pattern.
func safePkgName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// validateManifest validates a package manifest strictly.
func validateManifest(m *PackageManifest) error {
	if m == nil {
		return errors.New("nil manifest")
	}
	if m.Schema != ManifestSchema {
		return fmt.Errorf("manifest schema %d not supported (expected %d)", m.Schema, ManifestSchema)
	}
	if !safePkgName(m.Name) {
		return fmt.Errorf("invalid package name %q", m.Name)
	}
	if _, err := parseSemver(m.Version); err != nil {
		return fmt.Errorf("package %q bad version: %w", m.Name, err)
	}
	if m.Architecture != "arm64" && m.Architecture != "all" {
		return fmt.Errorf("package %q architecture %q not supported", m.Name, m.Architecture)
	}
	switch m.Risk {
	case "low", "medium", "system":
	default:
		return fmt.Errorf("package %q risk %q invalid", m.Name, m.Risk)
	}
	switch m.Restart {
	case "none", "jukahub", "device":
	default:
		return fmt.Errorf("package %q restart %q invalid", m.Name, m.Restart)
	}
	if len(m.Operations) == 0 {
		return fmt.Errorf("package %q has no operations", m.Name)
	}
	seen := map[string]bool{}
	for _, op := range m.Operations {
		if !allowedPkgOps[op.Kind] {
			return fmt.Errorf("package %q operation %q not allowed", m.Name, op.Kind)
		}
		if op.Kind == "install" || op.Kind == "replace" {
			if op.Src == "" || op.Dest == "" {
				return fmt.Errorf("package %q %s op needs src+dest", m.Name, op.Kind)
			}
		}
		if op.Kind == "remove" || op.Kind == "chmod" || op.Kind == "executable" {
			if op.Dest == "" {
				return fmt.Errorf("package %q %s op needs dest", m.Name, op.Kind)
			}
		}
		if op.Kind == "patch" && (op.Key == "" || op.Value == "") {
			return fmt.Errorf("package %q patch op needs key+value", m.Name)
		}
		if op.Dest != "" {
			lower := strings.ToLower(op.Dest)
			for _, prot := range protectedUserDirs {
				if strings.HasPrefix(lower, "/"+strings.ToLower(prot)) {
					return fmt.Errorf("package %q op targets protected user content (%s)", m.Name, prot)
				}
			}
		}
		if seen[op.Dest+"|"+op.Kind] {
			return fmt.Errorf("package %q duplicate operation for %s", m.Name, op.Dest)
		}
		seen[op.Dest+"|"+op.Kind] = true
	}
	return nil
}

// canonicalRepoJSON renders the repository index without the signature field.
func canonicalRepoJSON(idx *SignedRepoIndex) ([]byte, error) {
	clone := *idx
	clone.Signature = ""
	return json.Marshal(clone)
}

// verifyRepoSignature verifies the Ed25519 signature of a repository index.
// It returns an error when the signature is missing/invalid or the key is not
// the embedded key. It also rejects expired indexes.
func verifyRepoSignature(idx *SignedRepoIndex) error {
	if idx == nil {
		return errors.New("nil repository index")
	}
	if idx.Format != RepoFormatVersion {
		return fmt.Errorf("repository format %d not supported", idx.Format)
	}
	if idx.KeyID != patchRepoKeyID {
		return fmt.Errorf("repository signed by unknown key %q", idx.KeyID)
	}
	if idx.IssuedAt != "" {
		if t, err := time.Parse(time.RFC3339, idx.IssuedAt); err == nil {
			if time.Since(t) < 0 {
				return errors.New("repository index issued in the future")
			}
		}
	}
	if idx.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, idx.ExpiresAt); err == nil && time.Now().After(t) {
			return errors.New("repository index expired")
		}
	}
	sig, err := hex.DecodeString(idx.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("repository signature malformed")
	}
	payload, err := canonicalRepoJSON(idx)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(patchRepoPublicKey), payload, sig) {
		return errors.New("repository signature verification failed")
	}
	return nil
}

// verifyPackageDigest checks the digest of a downloaded package archive
// against the signed index entry.
func verifyPackageDigest(idx *SignedRepoIndex, name, archivePath string) error {
	entry := findRepoPkg(idx, name)
	if entry == nil {
		return fmt.Errorf("package %q not present in signed index", name)
	}
	if entry.PackageSHA == "" {
		return fmt.Errorf("package %q has no digest in index", name)
	}
	got, err := SHA256File(archivePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, entry.PackageSHA) {
		return fmt.Errorf("package %q digest mismatch (got %s)", name, got)
	}
	return nil
}

// findRepoPkg returns the index entry for a package name.
func findRepoPkg(idx *SignedRepoIndex, name string) *RepoPkg {
	if idx == nil {
		return nil
	}
	for i := range idx.Packages {
		if idx.Packages[i].Name == name {
			return &idx.Packages[i]
		}
	}
	return nil
}

// repoPkgCompatible evaluates device/OS/architecture constraints.
func repoPkgCompatible(p *RepoPkg) bool {
	if p.Architecture != "arm64" && p.Architecture != "all" {
		return false
	}
	if p.Architecture == "arm64" && !IsTSP() {
		return false
	}
	// OS + device constraints.
	for _, osName := range p.OS {
		if osName != "trimui-stock" && osName != "all" {
			return false
		}
	}
	if len(p.Devices) > 0 {
		ok := false
		for _, d := range p.Devices {
			if d == "TG5040" {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	// On a dev build, only JukaHub-owned low-risk packages are allowed.
	if IsWindows() {
		if p.Risk != "low" {
			return false
		}
	}
	return true
}

// --- helpers reused by the build tooling ---

// packageArchiveSHA computes the sha256 hex of a package archive.
func packageArchiveSHA(path string) string {
	s, err := SHA256File(path)
	if err != nil {
		return ""
	}
	return s
}

// bytesEqual is a tiny helper (kept for symmetry with the tooling).
func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }

// hashBytes computes sha256 hex of raw bytes.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
