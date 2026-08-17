package main

// patch_engine.go implements the safe update engine behind the MISC -> Patch
// module. It is deliberately conservative:
//
//   - JukaHub application/config/tool updates are fully supported with
//     backups, a persisted transaction journal, and rollback.
//   - Official TrimUI TG5040 SD-asset and firmware updates are only *staged*
//     and reviewed; nothing is silently flashed or merged.
//   - The device must be positively identified before system-level actions are
//     offered. On uncertainty (or non-TSP platforms) only JukaHub-owned
//     updates are available.
//
// All network/filesystem work runs on worker goroutines (never the SDL render
// thread). The scene calls the asynchronous entry points and consumes the
// result through bounded state updates.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Semantic version comparison (SemVer 2.0.0 subset with build metadata)
// ---------------------------------------------------------------------------

// semver is a parsed semantic version. Build metadata is preserved for display
// but never used for ordering.
type semver struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease []string
	Build      string
	Raw        string
}

// parseSemver parses "v1.2.3" / "1.2.3-rc.1+build5" style versions. Versions
// without a numeric patch component (e.g. "1.2") are accepted with patch 0.
func parseSemver(s string) (semver, error) {
	v := strings.TrimSpace(s)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if v == "" {
		return semver{}, fmt.Errorf("empty version")
	}
	var vv semver
	vv.Raw = strings.TrimSpace(s)

	// Split off build metadata (+...).
	if i := strings.IndexByte(v, '+'); i >= 0 {
		vv.Build = v[i+1:]
		v = v[:i]
	}
	// Split off prerelease (-...).
	if i := strings.IndexByte(v, '-'); i >= 0 {
		pre := v[i+1:]
		if pre == "" {
			return semver{}, fmt.Errorf("empty prerelease in %q", s)
		}
		for _, part := range strings.Split(pre, ".") {
			if part == "" {
				return semver{}, fmt.Errorf("empty prerelease identifier in %q", s)
			}
		}
		vv.Prerelease = strings.Split(pre, ".")
		v = v[:i]
	}
	nums := strings.Split(v, ".")
	if len(nums) < 1 || len(nums) > 3 {
		return semver{}, fmt.Errorf("bad numeric part %q in %q", v, s)
	}
	at := func(i int) int {
		if i < len(nums) {
			n, err := strconv.Atoi(nums[i])
			if err != nil || n < 0 {
				return -1
			}
			return n
		}
		return 0
	}
	vv.Major = at(0)
	vv.Minor = at(1)
	vv.Patch = at(2)
	if vv.Major < 0 || vv.Minor < 0 || vv.Patch < 0 {
		return semver{}, fmt.Errorf("non-numeric version component in %q", s)
	}
	return vv, nil
}

// compareSemver orders two versions: -1, 0, or 1. Prerelease versions sort
// before their release (1.0.0-rc.1 < 1.0.0); build metadata is ignored.
func compareSemver(a, b string) (int, error) {
	av, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	bv, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	if av.Major != bv.Major {
		return sign(av.Major - bv.Major), nil
	}
	if av.Minor != bv.Minor {
		return sign(av.Minor - bv.Minor), nil
	}
	if av.Patch != bv.Patch {
		return sign(av.Patch - bv.Patch), nil
	}
	return comparePrerelease(av.Prerelease, bv.Prerelease), nil
}

// NewerVersion returns true when candidate is strictly newer than installed,
// treating unparseable versions as never-newer (fail closed).
func NewerVersion(installed, candidate string) bool {
	c, err := compareSemver(installed, candidate)
	if err != nil {
		return false
	}
	return c < 0
}

func comparePrerelease(a, b []string) int {
	// A release (no prerelease) is newer than any prerelease of itself.
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 {
		return 1
	}
	if len(b) == 0 {
		return -1
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		an, aerr := strconv.Atoi(a[i])
		bn, berr := strconv.Atoi(b[i])
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return sign(an - bn)
			}
		case aerr == nil:
			return -1 // numeric identifiers sort before alphanumeric
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	// Fewer fields wins (1.0.0-alpha < 1.0.0-alpha.1).
	return sign(len(a) - len(b))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// Manifest (JukaHub update manifest, versioned, fail-closed on unknown schema)
// ---------------------------------------------------------------------------

// PatchManifestSchema is the current manifest schema version.
const PatchManifestSchema = 1

// PatchManifest describes a JukaHub release and its per-target artifacts.
type PatchManifest struct {
	Schema      int              `json:"schema"`
	Product     string           `json:"product"`
	Version     string           `json:"version"`
	Channel     string           `json:"channel"`
	PublishedAt string           `json:"published_at"`
	Targets     []ManifestTarget `json:"targets"`
}

// ManifestTarget is a single downloadable artifact for a specific device/OS.
type ManifestTarget struct {
	Device  string   `json:"device"`
	OS      string   `json:"os"`
	Arch    string   `json:"arch"`
	Asset   string   `json:"asset"`
	SHA256  string   `json:"sha256"`
	Size    int64    `json:"size"`
	MinFW   string   `json:"min_firmware,omitempty"`
	Files   []string `json:"files,omitempty"`
	Summary string   `json:"summary,omitempty"`
}

// ParsePatchManifest validates and decodes a manifest. Unknown schema versions
// fail closed: the caller must treat them as unusable.
func ParsePatchManifest(data []byte) (*PatchManifest, error) {
	var m PatchManifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Schema != PatchManifestSchema {
		return nil, fmt.Errorf("manifest schema %d is not supported (expected %d)", m.Schema, PatchManifestSchema)
	}
	if strings.TrimSpace(m.Product) == "" {
		return nil, errors.New("manifest: missing product")
	}
	if _, err := parseSemver(m.Version); err != nil {
		return nil, fmt.Errorf("manifest: bad version: %w", err)
	}
	if len(m.Targets) == 0 {
		return nil, errors.New("manifest: no targets")
	}
	for i, t := range m.Targets {
		if t.Asset == "" {
			return nil, fmt.Errorf("manifest: target %d missing asset", i)
		}
		if t.SHA256 != "" {
			if len(t.SHA256) != 64 {
				return nil, fmt.Errorf("manifest: target %d bad sha256 length", i)
			}
			if _, err := hex.DecodeString(t.SHA256); err != nil {
				return nil, fmt.Errorf("manifest: target %d bad sha256 hex", i)
			}
		}
		if t.Size < 0 {
			return nil, fmt.Errorf("manifest: target %d negative size", i)
		}
	}
	return &m, nil
}

// ManifestTargetFor returns the first target matching the given device, OS and
// architecture, or nil when none match.
func (m *PatchManifest) ManifestTargetFor(device, goos, arch string) *ManifestTarget {
	for i := range m.Targets {
		t := &m.Targets[i]
		if t.Device == device && t.OS == goos && t.Arch == arch {
			return t
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Approved hosts and URL policy
// ---------------------------------------------------------------------------

// allowedPatchHosts is the complete allowlist of download hosts. Anything else
// (including redirect targets) is rejected.
var allowedPatchHosts = map[string]bool{
	"github.com":                    true,
	"api.github.com":                true,
	"objects.githubusercontent.com": true,
	"codeload.github.com":           true,
}

// validatePatchURL rejects non-HTTPS URLs and URLs whose host is not on the
// allowlist. It also rejects URLs containing userinfo or query strings that
// look like credentials.
func validatePatchURL(raw string) error {
	if raw == "" {
		return errors.New("empty URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("bad URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing non-HTTPS URL %q", raw)
	}
	host := strings.ToLower(u.Hostname())
	if !allowedPatchHosts[host] {
		return fmt.Errorf("host %q is not on the approved download allowlist", host)
	}
	if u.User != nil {
		return errors.New("URL must not contain credentials")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Device detection
// ---------------------------------------------------------------------------

// DeviceModel describes a positively identified target device.
type DeviceModel string

const (
	DeviceUnknown   DeviceModel = "unknown"
	DeviceTrimuiTSP DeviceModel = "trimui-smart-pro" // TG5040 original Smart Pro
	DeviceDevBuild  DeviceModel = "dev-build"        // Windows / other dev hosts
)

// deviceCheckPaths are the probe files used to positively identify the device.
// Detection requires multiple corroborating signals on Linux/ARM64; /mnt/SDCARD
// alone is never sufficient.
var deviceCheckPaths = []string{
	"/mnt/SDCARD",
	"/usr/trimui",
	"/proc/device-tree/model",
}

// DetectDevice identifies the runtime device conservatively. When signals are
// missing or contradictory the result is DeviceUnknown so system-level actions
// stay disabled.
func DetectDevice() DeviceModel {
	if runtime.GOOS == "windows" {
		return DeviceDevBuild
	}
	if runtime.GOOS != "linux" {
		return DeviceUnknown
	}
	if runtime.GOARCH != "arm64" {
		return DeviceUnknown
	}
	sdcard := pathExists("/mnt/SDCARD")
	trimui := pathExists("/usr/trimui")
	dtModel := readTrimmedFile("/proc/device-tree/model")
	isTSP := strings.Contains(strings.ToLower(dtModel), "tg5040") ||
		strings.Contains(strings.ToLower(dtModel), "trimui smart pro") ||
		strings.Contains(strings.ToLower(dtModel), "smart pro")

	switch {
	case trimui && isTSP:
		return DeviceTrimuiTSP
	case sdcard && trimui && strings.Contains(strings.ToLower(dtModel), "trimui"):
		// A generic Trimui model file without a positive TG5040 match is not
		// enough to offer stock firmware staging.
		return DeviceUnknown
	case sdcard && trimui:
		// Trimui tree exists but the model is unknown -> do not guess.
		return DeviceUnknown
	default:
		return DeviceUnknown
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func readTrimmedFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// deviceDisplayName returns a human label for the detected model.
func deviceDisplayName(d DeviceModel) string {
	switch d {
	case DeviceTrimuiTSP:
		return "TrimUI Smart Pro (TG5040)"
	case DeviceDevBuild:
		return "Development build"
	default:
		return "Unknown"
	}
}

// ---------------------------------------------------------------------------
// Patch state directory
// ---------------------------------------------------------------------------

// patchStateDirFn is overridable in tests so the real data directory is never
// touched by the test suite.
var patchStateDirFn = func() (string, error) {
	var base string
	if IsTSP() {
		dir, err := P().ExecutableDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(dir, "patch")
	} else {
		data, err := P().DataDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(data, "patch")
	}
	return base, nil
}

// PatchStateDir returns the application-owned directory for Patch state,
// backups, staging and journals. On the device this lives next to the JukaHub
// installation; on Windows it lives under the user data directory. It is
// created on demand.
func PatchStateDir() (string, error) {
	base, err := patchStateDirFn()
	if err != nil {
		return "", err
	}
	for _, sub := range []string{"", "downloads", "staging", "journals", "backups", "reports"} {
		if err := os.MkdirAll(filepath.Join(base, sub), 0o700); err != nil {
			return "", err
		}
	}
	return base, nil
}

// ---------------------------------------------------------------------------
// Patch state (persisted)
// ---------------------------------------------------------------------------

// PatchComponent identifies an update class. Order matters for display.
type PatchComponent string

const (
	ComponentApp      PatchComponent = "app"
	ComponentAssets   PatchComponent = "assets"
	ComponentTools    PatchComponent = "tools"
	ComponentPackages PatchComponent = "packages"
)

// ComponentDisplayName returns the user-facing component label.
func ComponentDisplayName(c PatchComponent) string {
	switch c {
	case ComponentApp:
		return "JukaHub"
	case ComponentAssets:
		return "JukaHub config & assets"
	case ComponentTools:
		return "Helper tools"
	case ComponentPackages:
		return "Package repository"
	}
	return string(c)
}

// ComponentRisk returns a short risk label.
func ComponentRisk(c PatchComponent) string {
	return "Low"
}

// PatchRowStatus mirrors the UI status enum from the spec.
type PatchRowStatus string

const (
	StatusCurrent     PatchRowStatus = "Current"
	StatusAvailable   PatchRowStatus = "Available"
	StatusDownloading PatchRowStatus = "Downloading"
	StatusVerified    PatchRowStatus = "Verified"
	StatusStaged      PatchRowStatus = "Staged"
	StatusInstalled   PatchRowStatus = "Installed"
	StatusFailed      PatchRowStatus = "Failed"
	StatusRestartReq  PatchRowStatus = "Restart Required"
	StatusUnavailable PatchRowStatus = "Unavailable"
	StatusChecking    PatchRowStatus = "Checking…"
	StatusPlanning    PatchRowStatus = "Planning…"
	StatusBackingUp   PatchRowStatus = "Backing up…"
	StatusApplying    PatchRowStatus = "Applying…"
	StatusRollingBack PatchRowStatus = "Rolling back…"
	StatusCompleted   PatchRowStatus = "Completed"
	StatusOffline     PatchRowStatus = "Offline"
)

// PatchRow is a single update-class row shown in the Patch scene.
type PatchRow struct {
	Component   PatchComponent
	Installed   string // "Unknown" when not determinable
	Available   string // "" when unknown
	Source      string // "JukaHub" / "TrimUI official firmware" / "TrimUI official SD assets"
	Size        int64
	Status      PatchRowStatus
	Risk        string
	Detail      string // short machine line shown under the row
	Enabled     bool   // false -> action disabled (unknown device etc.)
	ActionLabel string // primary A action label
}

// PatchState is the persisted Patch module state.
type PatchState struct {
	Schema         int    `json:"schema"`
	InstalledVer   string `json:"installed_version"`
	LastCheckAt    string `json:"last_check_at,omitempty"`
	LastCheckError string `json:"last_check_error,omitempty"`
	LastUpdateVer  string `json:"last_update_version,omitempty"`
	LastUpdateAt   string `json:"last_update_at,omitempty"`
	StagedFWName   string `json:"staged_firmware,omitempty"`
}

var (
	patchStateMu  sync.Mutex
	patchState    = &PatchState{Schema: 1}
	patchStateDir string
)

// loadPatchState reads state.json from the patch directory (best effort).
func loadPatchState() {
	dir, err := PatchStateDir()
	if err != nil {
		return
	}
	patchStateDir = dir
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return
	}
	var st PatchState
	if json.Unmarshal(data, &st) == nil && st.Schema == 1 {
		patchStateMu.Lock()
		patchState = &st
		patchStateMu.Unlock()
	}
}

// savePatchState persists the patch state atomically.
func savePatchState() {
	patchStateMu.Lock()
	defer patchStateMu.Unlock()
	if patchStateDir == "" {
		return
	}
	data, err := json.MarshalIndent(patchState, "", "  ")
	if err != nil {
		return
	}
	_ = AtomicWrite(filepath.Join(patchStateDir, "state.json"), data, 0o600)
}

// InitPatchModule loads persisted state and runs the startup journal check.
// It must be called once during startup, off the render loop. It never blocks
// startup or prevents JukaHub from launching.
func InitPatchModule() {
	loadPatchState()
	checkInterruptedJournal()
	ensureDefaultRepo()
}

// ---------------------------------------------------------------------------
// Installed-version introspection
// ---------------------------------------------------------------------------

// installedPatchVersions returns the best-known installed version for each
// component. Everything is best-effort; unknown values read "Unknown".
func installedPatchVersions(config *Config) map[PatchComponent]string {
	m := map[PatchComponent]string{
		ComponentApp:      "Unknown",
		ComponentAssets:   "Unknown",
		ComponentTools:    "Unknown",
		ComponentPackages: "No index",
	}
	if config != nil {
		m[ComponentApp] = strings.TrimPrefix(strings.TrimSpace(config.Version), "v")
		if m[ComponentApp] == "" {
			m[ComponentApp] = "0.4.0"
		}
		m[ComponentAssets] = m[ComponentApp]
	}
	if repo, err := LoadPackageRepo(); err == nil {
		m[ComponentPackages] = fmt.Sprintf("%d package(s)", len(repo.Packages))
	}
	if v := helperToolVersion("yt-dlp"); v != "" {
		m[ComponentTools] = "yt-dlp " + v
	} else if v := helperToolVersion("ffplay"); v != "" {
		m[ComponentTools] = "ffplay " + v
	} else {
		m[ComponentTools] = "Not installed"
	}
	return m
}

// helperToolVersion returns a short version string for a bundled helper tool
// without executing it: it reads --version output only when the tool is known
// and present, and failures return "".
func helperToolVersion(tool string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(tool, ".exe") {
		tool += ".exe"
	}
	path, err := P().LookPath(tool)
	if err != nil {
		return ""
	}
	// Version probing is optional metadata; a failure is not fatal.
	return toolVersionFromPath(path)
}

// toolVersionFromPath returns a short version string for a local tool binary.
// It runs only well-known version probes with a short timeout and treats any
// failure as unknown (""). It never downloads or executes untrusted files.
func toolVersionFromPath(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// ---------------------------------------------------------------------------
// HTTP helpers (bounded, approved hosts only)
// ---------------------------------------------------------------------------

const (
	patchHTTPTimeout = 25 * time.Second
	patchMaxDownload = 512 << 20 // 512 MiB hard cap for any single artifact
	patchMaxMetadata = 8 << 20   // 8 MiB for JSON metadata
	githubReleaseAPI = "https://api.github.com/repos/jukaLang/JukaHubV2/releases/latest"
)

// newPatchClient returns an HTTP client that follows redirects only to
// approved HTTPS hosts. TLS is never disabled.
func newPatchClient() *http.Client {
	return &http.Client{
		Timeout: patchHTTPTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errors.New("too many redirects")
			}
			if err := validatePatchURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect rejected: %w", err)
			}
			return nil
		},
	}
}

// fetchBounded GETs a URL and returns the response body as bytes, enforcing
// the size limit. The caller must already have validated the URL.
func fetchBounded(ctx context.Context, client *http.Client, raw string, maxBytes int64) ([]byte, error) {
	if err := validatePatchURL(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "JukaHub-Patch/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, raw)
	}
	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response exceeds %d byte limit", maxBytes)
	}
	return data, nil
}

// downloadToFile streams a URL to path with a `.part` suffix, verifying the
// expected SHA-256 when provided ("" skips verification). On failure the
// partial file is removed. The file is fsynced before the final rename.
func downloadToFile(ctx context.Context, client *http.Client, raw, dest, expectSHA string, maxBytes int64) error {
	if err := validatePatchURL(raw); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	part := dest + ".part"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			_ = os.Remove(part)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub-Patch/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, raw)
	}

	limited := io.LimitReader(resp.Body, maxBytes+1)
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hasher), limited)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	if n > maxBytes {
		return fmt.Errorf("download exceeds %d byte limit", maxBytes)
	}
	if expectSHA != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, expectSHA) {
			return fmt.Errorf("sha256 mismatch: got %s want %s", got, expectSHA)
		}
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	ok = true
	return nil
}

// SHA256File computes the SHA-256 of a file.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// GitHub release metadata
// ---------------------------------------------------------------------------

// githubAsset is a minimal GitHub release asset record.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// githubRelease is a minimal GitHub release record.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Name    string        `json:"name"`
	Assets  []githubAsset `json:"assets"`
}

// FetchLatestJukaHubRelease returns the latest JukaHub release metadata.
func FetchLatestJukaHubRelease(ctx context.Context) (*githubRelease, error) {
	client := newPatchClient()
	data, err := fetchBounded(ctx, client, githubReleaseAPI, patchMaxMetadata)
	if err != nil {
		return nil, err
	}
	var rel githubRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, errors.New("release metadata missing tag")
	}
	return &rel, nil
}

// FindManifestAsset returns the release asset whose name matches the manifest
// naming, or nil.
func (r *githubRelease) FindManifestAsset() *githubAsset {
	for i := range r.Assets {
		if strings.EqualFold(r.Assets[i].Name, "manifest.json") {
			return &r.Assets[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Archive safety
// ---------------------------------------------------------------------------

// sanitizeArchiveEntry validates one archive entry path. It rejects absolute
// paths, traversal, NUL bytes, and device files.
func sanitizeArchiveEntry(name string, mode fs.FileMode, isDir bool) error {
	if strings.ContainsRune(name, '\x00') {
		return errors.New("archive entry contains NUL byte")
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return fmt.Errorf("archive entry has absolute path: %q", name)
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("archive entry escapes root: %q", name)
	}
	if !isDir && mode&os.ModeDevice != 0 {
		return fmt.Errorf("archive entry is a device file: %q", name)
	}
	if !isDir && mode&os.ModeNamedPipe != 0 {
		return fmt.Errorf("archive entry is a pipe: %q", name)
	}
	if !isDir && mode&os.ModeSocket != 0 {
		return fmt.Errorf("archive entry is a socket: %q", name)
	}
	if mode&os.ModeSymlink != 0 {
		// Symlinks are rejected outright: we never follow links from archives.
		return fmt.Errorf("archive entry is a symlink: %q", name)
	}
	return nil
}

// extractZipSafe extracts a zip archive into destDir after validating every
// entry. Extraction is bounded by maxEntries and maxTotalBytes.
func extractZipSafe(ctx context.Context, archive, destDir string, maxEntries int, maxTotalBytes int64) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > maxEntries {
		return fmt.Errorf("archive has %d entries (limit %d)", len(zr.File), maxEntries)
	}
	var total int64
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		mode := f.Mode()
		if err := sanitizeArchiveEntry(f.Name, mode, f.FileInfo().IsDir()); err != nil {
			return err
		}
		target := filepath.Join(destDir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(filepath.Separator)) {
			return fmt.Errorf("entry escapes staging dir: %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > maxTotalBytes {
			return fmt.Errorf("archive expands beyond %d bytes", maxTotalBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, maxTotalBytes+1))
		rc.Close()
		out.Close()
		if err != nil {
			return fmt.Errorf("extract %q: %w", f.Name, err)
		}
	}
	return nil
}

// extractTarGzSafe extracts a .tar.gz archive into destDir with the same
// safety checks as extractZipSafe.
func extractTarGzSafe(ctx context.Context, archive, destDir string, maxEntries int, maxTotalBytes int64) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := 0
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		entries++
		if entries > maxEntries {
			return fmt.Errorf("archive has more than %d entries", maxEntries)
		}
		if err := sanitizeArchiveEntry(hdr.Name, hdr.FileInfo().Mode(), hdr.FileInfo().IsDir()); err != nil {
			return err
		}
		target := filepath.Join(destDir, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(filepath.Separator)) {
			return fmt.Errorf("entry escapes staging dir: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxTotalBytes {
				return fmt.Errorf("archive expands beyond %d bytes", maxTotalBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, maxTotalBytes+1))
			out.Close()
			if err != nil {
				return fmt.Errorf("extract %q: %w", hdr.Name, err)
			}
		default:
			// Skip hardlinks/links/other types: never materialize them.
			continue
		}
	}
	return nil
}

// extractSafe dispatches on the archive extension.
func extractSafe(ctx context.Context, archive, destDir string, maxEntries int, maxTotalBytes int64) error {
	lower := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZipSafe(ctx, archive, destDir, maxEntries, maxTotalBytes)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarGzSafe(ctx, archive, destDir, maxEntries, maxTotalBytes)
	default:
		return fmt.Errorf("unsupported archive format: %s", archive)
	}
}

// ---------------------------------------------------------------------------
// Free space
// ---------------------------------------------------------------------------

// freeBytes returns the free space on the filesystem containing path.
func freeBytes(path string) (int64, error) {
	var stat syscallStatfs
	if err := statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

// ---------------------------------------------------------------------------
// Backup sets
// ---------------------------------------------------------------------------

// BackupFile records one backed-up file.
type BackupFile struct {
	Path    string `json:"path"` // original path (absolute)
	Rel     string `json:"rel"`  // path inside the backup set
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	ModTime int64  `json:"mod_time"`
	SHA256  string `json:"sha256"`
}

// BackupManifest describes one backup set (one timestamped directory).
type BackupManifest struct {
	CreatedAt  string       `json:"created_at"`
	Reason     string       `json:"reason"`
	Version    string       `json:"version"`
	Files      []BackupFile `json:"files"`
	TotalBytes int64        `json:"total_bytes"`
}

// CreateBackupSet copies files into a fresh timestamped backup directory and
// writes a manifest. Backup manifests are written after the copy completes.
func CreateBackupSet(reason string, files []string) (*BackupManifest, string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return nil, "", err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	setDir := filepath.Join(dir, "backups", stamp)
	if err := os.MkdirAll(setDir, 0o700); err != nil {
		return nil, "", err
	}
	m := &BackupManifest{CreatedAt: stamp, Reason: reason, Version: versionString(appConfig)}
	for _, src := range files {
		info, err := os.Stat(src)
		if err != nil {
			continue // missing file: nothing to back up
		}
		if info.IsDir() {
			continue
		}
		sha, err := SHA256File(src)
		if err != nil {
			_ = os.RemoveAll(setDir)
			return nil, "", fmt.Errorf("hash %s: %w", src, err)
		}
		rel := filepath.Base(src)
		if m.Version != "" {
			rel = m.Version + "-" + rel
		}
		dst := filepath.Join(setDir, rel)
		if err := copyFile(src, dst); err != nil {
			_ = os.RemoveAll(setDir)
			return nil, "", err
		}
		m.Files = append(m.Files, BackupFile{
			Path:    src,
			Rel:     rel,
			Size:    info.Size(),
			Mode:    uint32(info.Mode().Perm()),
			ModTime: info.ModTime().Unix(),
			SHA256:  sha,
		})
		m.TotalBytes += info.Size()
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		_ = os.RemoveAll(setDir)
		return nil, "", err
	}
	if err := os.WriteFile(filepath.Join(setDir, "manifest.json"), data, 0o600); err != nil {
		_ = os.RemoveAll(setDir)
		return nil, "", err
	}
	return m, setDir, nil
}

// ListBackupSets returns backup directory names sorted newest-first.
func ListBackupSets() ([]string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "backups"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// RestoreBackupSet restores every file in a backup set to its original path.
// A journal is written first so an interrupted restore can be rolled back.
func RestoreBackupSet(name string) error {
	dir, err := PatchStateDir()
	if err != nil {
		return err
	}
	setDir := filepath.Join(dir, "backups", filepath.Base(name))
	manifestPath := filepath.Join(setDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("backup set %q has no manifest: %w", name, err)
	}
	var m BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	journal := newJournal("restore", name)
	defer journal.close()
	for _, bf := range m.Files {
		src := filepath.Join(setDir, filepath.Base(bf.Rel))
		if _, err := os.Stat(src); err != nil {
			continue
		}
		// Record the pre-restore state so the restore itself can be rolled back.
		if err := journal.recordReplace(bf.Path); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(bf.Path), 0o755); err != nil {
			return err
		}
		if err := copyFile(src, bf.Path); err != nil {
			return fmt.Errorf("restore %s: %w", bf.Path, err)
		}
		if bf.Mode != 0 {
			_ = os.Chmod(bf.Path, fs.FileMode(bf.Mode))
		}
	}
	return journal.commit()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ---------------------------------------------------------------------------
// Transaction journal
// ---------------------------------------------------------------------------

// journalEntry is one destructive step recorded before it happens.
type journalEntry struct {
	Kind   string `json:"kind"` // "replace" | "remove"
	Path   string `json:"path"`
	Backup string `json:"backup,omitempty"` // path where the old file was copied
	Done   bool   `json:"done"`
}

// journal is a persisted transaction journal. It supports rollback after an
// interruption by replaying recorded entries in reverse.
type journal struct {
	mu       sync.Mutex
	ID       string
	Kind     string // "juka-update" | "sd-merge" | "restore" | "config-migrate"
	Target   string
	Entries  []journalEntry
	path     string
	commited bool
}

func newJournal(kind, target string) *journal {
	dir, err := PatchStateDir()
	if err != nil {
		dir = ""
	}
	id := fmt.Sprintf("%s-%d", strings.ReplaceAll(kind, "/", "_"), time.Now().UnixNano())
	j := &journal{
		ID:     id,
		Kind:   kind,
		Target: target,
		path:   filepath.Join(dir, "journals", id+".json"),
	}
	return j
}

func (j *journal) persist() {
	if j.path == "" {
		return
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(j.path, data, 0o600)
}

// recordReplace snapshots the file at path into the journal's backup area and
// records the entry. The snapshot happens *before* the destructive step.
func (j *journal) recordReplace(path string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		// File does not exist yet; record a remove-style entry so rollback
		// knows to delete whatever takes its place.
		j.Entries = append(j.Entries, journalEntry{Kind: "remove", Path: path})
		j.persist()
		return nil
	}
	if info.IsDir() {
		return fmt.Errorf("journal refuses directory replacement: %s", path)
	}
	backup := filepath.Join(filepath.Dir(j.path), "files", j.ID, filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return err
	}
	if err := copyFile(path, backup); err != nil {
		return fmt.Errorf("journal backup %s: %w", path, err)
	}
	j.Entries = append(j.Entries, journalEntry{Kind: "replace", Path: path, Backup: backup})
	j.persist()
	return nil
}

// close is a no-op cleanup hook kept for symmetry; the journal is removed
// only by commit/rollback.
func (j *journal) close() {}

// commit marks the journal complete and removes it.
func (j *journal) commit() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.commited = true
	if j.path != "" {
		_ = os.Remove(j.path)
	}
	return nil
}

// rollback replays the journal in reverse, restoring backups. Returns the
// first error encountered while still attempting every entry.
func (j *journal) rollback() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var firstErr error
	for i := len(j.Entries) - 1; i >= 0; i-- {
		e := j.Entries[i]
		if e.Kind == "replace" && e.Backup != "" {
			if _, err := os.Stat(e.Backup); err == nil {
				if err := copyFile(e.Backup, e.Path); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		} else if e.Kind == "remove" {
			// The original did not exist; remove whatever is there now.
			if err := os.Remove(e.Path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
		}
	}
	_ = os.RemoveAll(filepath.Join(filepath.Dir(j.path), "files", j.ID))
	if j.path != "" {
		_ = os.Remove(j.path)
	}
	return firstErr
}

// pendingJournals lists journal files that were not committed (interrupted).
func pendingJournals() []string {
	dir, err := PatchStateDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(dir, "journals"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(dir, "journals", e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// checkInterruptedJournal is the startup recovery hook: it detects an
// incomplete journal and rolls it back automatically so JukaHub always
// launches in a consistent state. A toast is shown when recovery ran.
func checkInterruptedJournal() {
	pending := pendingJournals()
	if len(pending) == 0 {
		return
	}
	recovered := 0
	for _, p := range pending {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var j journal
		if json.Unmarshal(data, &j) != nil {
			continue
		}
		if err := j.rollback(); err == nil {
			recovered++
		}
	}
	if recovered > 0 {
		logPatch("recovered %d interrupted update transaction(s); files restored", recovered)
	}
}

// logPatch writes a Patch-module log line (console/log files only — never a
// permanent on-screen box).
func logPatch(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "[patch] "+format+"\n", args...)
}

// ---------------------------------------------------------------------------
// Config migration (JukaHub managed assets)
// ---------------------------------------------------------------------------

// MigrateConfigFile validates and migrates jukaconfig.json in place while
// preserving unknown fields and user data. It never touches jukauser.json.
// Returns a short human description of what changed.
func MigrateConfigFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("jukaconfig.json is not valid JSON: %w", err)
	}
	before, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	// Migration rules (schema-aware, unknown fields preserved by map round-trip):
	// 1. Ensure Version is present and normalized (no leading "v").
	if v, ok := raw["Version"].(string); !ok || strings.TrimSpace(v) == "" {
		raw["Version"] = "0.4.0"
	}
	if v, ok := raw["Version"].(string); ok {
		raw["Version"] = strings.TrimPrefix(strings.TrimSpace(v), "v")
	}
	// 2. Ensure a scenes array exists with at least one scene.
	if scenes, ok := raw["scenes"].([]interface{}); !ok || len(scenes) == 0 {
		return "", errors.New("jukaconfig.json has no scenes; refusing to guess")
	}
	after, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return "", err
	}
	if string(before) == string(after) {
		return "config is current", nil
	}
	// Validate before committing.
	if err := json.Unmarshal(after, &raw); err != nil {
		return "", fmt.Errorf("migrated config failed validation: %w", err)
	}
	if err := AtomicWrite(path, after, 0o644); err != nil {
		return "", err
	}
	return "config migrated and validated", nil
}

// ---------------------------------------------------------------------------
// Repair helpers
// ---------------------------------------------------------------------------

// PatchRepairReport collects the results of the local repair actions.
type PatchRepairReport struct {
	Checks []string `json:"checks"`
	Fixed  []string `json:"fixed"`
	Warned []string `json:"warned"`
}

// RepairJukaHub runs the conservative local repair pass. It never touches
// user data (jukauser.json, favorites, media) and never changes permissions
// recursively.
func RepairJukaHub(config *Config) PatchRepairReport {
	var r PatchRepairReport
	r.Checks = append(r.Checks, "jukaconfig.json validated", "patch state directory writable")
	// 1. Config validation.
	if _, err := MigrateConfigFile(P().ConfigPath()); err != nil {
		r.Warned = append(r.Warned, "config: "+err.Error())
	} else {
		r.Fixed = append(r.Fixed, "jukaconfig.json normalized")
	}
	// 2. Patch directories writable.
	if dir, err := PatchStateDir(); err == nil {
		probe := filepath.Join(dir, "repair-probe.tmp")
		if err := os.WriteFile(probe, []byte("ok"), 0o600); err == nil {
			_ = os.Remove(probe)
			r.Checks = append(r.Checks, "patch state directory writable")
		} else {
			r.Warned = append(r.Warned, "patch state directory not writable: "+err.Error())
		}
	} else {
		r.Warned = append(r.Warned, "patch state directory: "+err.Error())
	}
	// 3. Helper tool presence/architecture (without executing downloads).
	if IsTSP() {
		for _, tool := range []string{"yt-dlp", "ffplay"} {
			p, err := P().LookPath(tool)
			if err != nil {
				r.Warned = append(r.Warned, tool+" not found (will fall back to system PATH)")
				continue
			}
			if info, err := os.Stat(p); err == nil {
				if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
					_ = os.Chmod(p, 0o755)
					r.Fixed = append(r.Fixed, tool+" execute bit restored")
				}
			}
		}
	}
	// 4. Clear only Patch-owned temporary files.
	if dir, err := PatchStateDir(); err == nil {
		removed := 0
		for _, sub := range []string{"downloads", "staging"} {
			entries, _ := os.ReadDir(filepath.Join(dir, sub))
			for _, e := range entries {
				if e.IsDir() {
					if os.RemoveAll(filepath.Join(dir, sub, e.Name())) == nil {
						removed++
					}
				} else {
					if os.Remove(filepath.Join(dir, sub, e.Name())) == nil {
						removed++
					}
				}
			}
		}
		if removed > 0 {
			r.Fixed = append(r.Fixed, fmt.Sprintf("cleared %d stale Patch download/staging file(s)", removed))
		}
	}
	// 5. Font/asset presence.
	for _, asset := range []string{"Inter-Regular.ttf", "background.jpg"} {
		p := resolvePath(asset)
		if _, err := os.Stat(p); err != nil {
			r.Warned = append(r.Warned, "missing bundled asset: "+asset)
		}
	}
	return r
}

// ExportDiagnostics writes a redacted diagnostics report into the patch
// reports directory and returns its path.
func ExportDiagnostics(config *Config) (string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("JukaHub Patch diagnostics\n")
	sb.WriteString("=========================\n\n")
	sb.WriteString("Version      : " + versionString(config) + "\n")
	sb.WriteString("Platform     : " + P().Name() + " " + runtime.GOOS + "/" + runtime.GOARCH + "\n")
	sb.WriteString("Device       : " + deviceDisplayName(DetectDevice()) + "\n")
	backups, _ := ListBackupSets()
	sb.WriteString(fmt.Sprintf("Backups      : %d set(s)\n", len(backups)))
	for _, b := range backups {
		sb.WriteString("  - " + b + "\n")
	}
	sb.WriteString("\nNo credentials, tokens, or private paths are included.\n")
	name := "diagnostics-" + time.Now().UTC().Format("20060102T150405Z") + ".txt"
	path := filepath.Join(dir, "reports", name)
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "Unknown"
	}
	return s
}

// ---------------------------------------------------------------------------
// High-level flows used by the Patch scene
// ---------------------------------------------------------------------------

// PatchSnapshot is a thread-safe view of the module state consumed by the UI.
type PatchSnapshot struct {
	Device      DeviceModel
	Installed   map[PatchComponent]string
	Rows        []PatchRow
	Packages    []PatchPackage
	Backups     []string
	Diagnostics string
}

// mustRepoPath returns the packages.json path (best effort).
func mustRepoPath() string {
	p, err := defaultRepoPath()
	if err != nil {
		return "packages.json"
	}
	return p
}

// patchBusy guards the single-operation rule.
var patchBusy struct {
	sync.Mutex
	active bool
}

func tryBeginPatchOp() bool {
	patchBusy.Lock()
	defer patchBusy.Unlock()
	if patchBusy.active {
		return false
	}
	patchBusy.active = true
	return true
}

func endPatchOp() {
	patchBusy.Lock()
	patchBusy.active = false
	patchBusy.Unlock()
}

// BuildPatchSnapshot computes the current rows for the scene. It performs
// local disk checks only (no network).
func BuildPatchSnapshot(config *Config) *PatchSnapshot {
	s := &PatchSnapshot{
		Device:    DetectDevice(),
		Installed: installedPatchVersions(config),
	}
	backups, _ := ListBackupSets()
	s.Backups = backups

	device := s.Device
	rows := make([]PatchRow, 0, 5)

	// JukaHub app
	rows = append(rows, PatchRow{
		Component:   ComponentApp,
		Installed:   s.Installed[ComponentApp],
		Source:      "JukaHub",
		Status:      StatusCurrent,
		Risk:        ComponentRisk(ComponentApp),
		Enabled:     true,
		ActionLabel: "Check for updates",
	})

	// Config & assets
	rows = append(rows, PatchRow{
		Component:   ComponentAssets,
		Installed:   s.Installed[ComponentAssets],
		Source:      "JukaHub",
		Status:      StatusCurrent,
		Risk:        ComponentRisk(ComponentAssets),
		Enabled:     true,
		ActionLabel: "Verify & migrate",
	})

	// Helper tools
	rows = append(rows, PatchRow{
		Component:   ComponentTools,
		Installed:   s.Installed[ComponentTools],
		Source:      "JukaHub",
		Status:      StatusCurrent,
		Risk:        ComponentRisk(ComponentTools),
		Enabled:     true,
		ActionLabel: "Verify tools",
	})

	// Package repository (user-defined packages, apt-style)
	repoStatus := StatusUnavailable
	repoDetail := "No packages.json index found"
	repoEnabled := false
	if repo, err := LoadPackageRepo(); err == nil {
		repoStatus = StatusCurrent
		repoDetail = fmt.Sprintf("%d package(s) defined · %s", len(repo.Packages), mustRepoPath())
		repoEnabled = len(repo.Packages) > 0
	}
	rows = append(rows, PatchRow{
		Component:   ComponentPackages,
		Installed:   s.Installed[ComponentPackages],
		Source:      "User packages",
		Status:      repoStatus,
		Risk:        ComponentRisk(ComponentPackages),
		Detail:      repoDetail,
		Enabled:     repoEnabled,
		ActionLabel: "Manage packages",
	})
	s.Rows = rows

	// Package list for the scene's package section.
	if repo, err := LoadPackageRepo(); err == nil {
		s.Packages = repo.Packages
	}
	_ = device

	// Diagnostics report path if one exists.
	if dir, err := PatchStateDir(); err == nil {
		if entries, err := os.ReadDir(filepath.Join(dir, "reports")); err == nil && len(entries) > 0 {
			s.Diagnostics = filepath.Join(dir, "reports", entries[len(entries)-1].Name())
		}
	}
	return s
}
