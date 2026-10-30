// patch_engine.go is the Patch engine recovered from git history (c8aa566),
// minus the device-detection section which now lives in device.go.
// It provides the state directory, journal, backups, downloads, release
// metadata, versioning and repair/diagnostics used by the Patch scene.

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
// File permission constants
// ---------------------------------------------------------------------------

const (
	// PermDir is the permission for directories (rwxr-xr-x)
	PermDir = 0o755
	// PermExec is the permission for executable files (rwxr-xr-x)
	PermExec = 0o755
	// PermFile is the permission for regular files (rw-r-----)
	PermFile = 0o600
	// PermConfig is the permission for config files (rw-r--r--)
	PermConfig = 0o644
)

// --- Sentinel errors for common failure cases ---

// ErrNotFound is returned when a requested resource doesn't exist.
var ErrNotFound = errors.New("resource not found")

// ErrInvalidInput is returned when input validation fails.
var ErrInvalidInput = errors.New("invalid input")

// ErrNotSupported is returned when an operation isn't supported.
var ErrNotSupported = errors.New("not supported")

// ErrAlreadyExists is returned when trying to create something that exists.
var ErrAlreadyExists = errors.New("already exists")

// ErrPermissionDenied is returned when access is denied.
var ErrPermissionDenied = errors.New("permission denied")

// ErrTimeout is returned when an operation times out.
var ErrTimeout = errors.New("operation timed out")

// ErrCancelled is returned when an operation is cancelled.
var ErrCancelled = errors.New("operation cancelled")

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
	"github.com":                           true,
	"api.github.com":                       true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
	"codeload.github.com":                  true,
	// The libretro buildbot is where the aarch64 core builds come from
	// (emulatorupdates.go). It is the only non-GitHub host: nothing else has a
	// reason to be here, and it is still HTTPS-only.
	"buildbot.libretro.com": true,
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

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ---------------------------------------------------------------------------
// Patch state directory
// ---------------------------------------------------------------------------

// patchStateDirFn is overridable in tests so the real data directory is never
// touched by the test suite.
var patchStateDirFn = func() (string, error) {
	var base string
	if DetectDevice().IsTrimui() {
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
	// Release tracking for the automatic updater and the build-date display.
	LastUpdatePublishedAt string `json:"last_update_published_at,omitempty"`
	LastUpdateAsset       string `json:"last_update_asset,omitempty"`
	InstalledReleaseTag   string `json:"installed_release_tag,omitempty"`
	InstalledReleaseAt    string `json:"installed_release_at,omitempty"`
	AutoUpdateCheckedAt   string `json:"auto_update_checked_at,omitempty"`
	// CoreUpdateCheckedAt is the last automatic emulator-core update pass that
	// read the build index. A pass that failed to reach it does not stamp, so
	// an offline boot retries on the next launch instead of waiting a day.
	CoreUpdateCheckedAt string `json:"core_update_checked_at,omitempty"`
	// A newer release that was detected but deliberately not downloaded or
	// staged: installing waits for the user to confirm in the Patch scene.
	AvailableUpdateTag   string `json:"available_update_tag,omitempty"`
	AvailableUpdateAt    string `json:"available_update_published_at,omitempty"`
	AvailableUpdateAsset string `json:"available_update_asset,omitempty"`
	// Release notes are cached so the in-app changelog can open immediately
	// after a restart, before (or without) a network round trip. The body is
	// GitHub's release markdown, never user data.
	ReleaseNotesTag  string `json:"release_notes_tag,omitempty"`
	ReleaseNotesText string `json:"release_notes_text,omitempty"`
	ChangelogSeenTag string `json:"changelog_seen_tag,omitempty"`
}

var (
	patchStateMu  sync.Mutex
	patchState    = &PatchState{Schema: 1}
	patchStateDir string
)

// loadPatchState reads state.json from the patch directory (best effort).
// Callers must follow load with savePatchState when they make changes.
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
func savePatchState() error {
	patchStateMu.Lock()
	defer patchStateMu.Unlock()
	if patchStateDir == "" {
		return fmt.Errorf("patch home unavailable; cannot persist state")
	}
	data, err := json.MarshalIndent(patchState, "", "  ")
	if err != nil {
		return fmt.Errorf("patch state marshal: %w", err)
	}
	if err := AtomicWrite(filepath.Join(patchStateDir, "state.json"), data, 0o600); err != nil {
		return fmt.Errorf("patch state persist: %w", err)
	}
	return nil
}

// InitPatchModule loads persisted state and runs the startup journal check.
// It must be called once during startup, off the render loop. It never blocks
// startup or prevents JukaHub from launching.
func InitPatchModule() {
	loadPatchState()
	checkInterruptedJournal()
	confirmAppliedUpdate()
	clearStaleRestartMarkers()
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
		// Show the release build date next to the version (the tag alone is
		// a timestamp, so the semantic version goes stale between releases).
		if build := installedBuildDate(); build != "" {
			m[ComponentApp] += " · " + build
		}
		m[ComponentAssets] = m[ComponentApp]
	}
	if repo, err := LoadPackageRepo(); err == nil {
		m[ComponentPackages] = fmt.Sprintf("%d package(s)", len(repo.Packages))
	}
	if v := helperToolVersion("yt-dlp"); v != "" {
		m[ComponentTools] = "yt-dlp " + v
	} else if v := helperToolVersion("ffmpeg"); v != "" {
		m[ComponentTools] = "ffmpeg " + v
	} else {
		m[ComponentTools] = "Not installed"
	}
	return m
}

// EnsureHelperTool downloads and installs a helper tool (yt-dlp, ffmpeg or
// ffprobe) into the application tools directory (the "required" folder next to
// the binary) when it is missing. It is intentionally tolerant of transient
// network failures so the app can still launch offline; failures are reported
// with a retry action instead of aborting the app.
func EnsureHelperTool(tool string, config *Config) error {
	return ensureHelperTool(tool, config)
}

// helperInstallMu serializes installs so the install bar only ever shows one
// download at a time.
var helperInstallMu sync.Mutex

// errToolsOffline is returned instead of starting a download when the device
// has no connectivity. The install card shows the retry hint for it.
var errToolsOffline = errors.New("offline: connect to WiFi to install helper tools")

// minHelperToolBytes rejects obviously truncated downloads without executing
// them. Every bundled tool (yt-dlp, ffmpeg, ffprobe) is megabytes large.
const minHelperToolBytes = 256 << 10

func ensureHelperTool(tool string, config *Config) error {
	return ensureHelperToolCtx(context.Background(), tool, config)
}

// ensureHelperToolCtx is ensureHelperTool that can be cancelled. A cancelled
// install hides its progress card instead of leaving it on screen paused at the
// byte it had reached.
func ensureHelperToolCtx(ctx context.Context, tool string, config *Config) error {
	// ffplay is deliberately not a helper tool any more: nothing runs it (the
	// embedded player decodes with ffmpeg), and BtbN links it against X11, so
	// on the handheld it could not even start. Refusing the name keeps a stale
	// caller from bringing the ~130 MB binary back.
	if tool != "yt-dlp" && tool != "ffmpeg" && tool != "ffprobe" {
		return fmt.Errorf("unsupported helper tool %q (supported: ffmpeg, ffprobe, yt-dlp)", tool)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	helperInstallMu.Lock()
	defer helperInstallMu.Unlock()
	dir := P().ToolsDir()
	if dir == "" {
		return fmt.Errorf("tools directory unavailable (empty path)")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create tools directory %s: %w", dir, err)
	}
	exeName := tool
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	target := filepath.Join(dir, exeName)
	info, statErr := os.Stat(target)
	if statErr == nil {
		if info.Size() == 0 {
			// A previously interrupted install left an empty placeholder;
			// remove it so the download runs again.
			logPatch("[TOOLS] %s is empty at %s; reinstalling", tool, target)
			_ = os.Remove(target)
		} else {
			if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
				if err := os.Chmod(target, PermExec); err != nil {
					logPatch("[TOOLS] warn: cannot chmod %s: %v", target, err)
				}
			}
			return nil
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("tools directory check failed for %s: %w", target, statErr)
	}
	// Do not start a download that cannot succeed: on the TrimUI the app may be
	// launched before WiFi is connected. The card keeps the retry action so a
	// single A press after joining a network installs the tool.
	if !IsOnline() {
		logPatch("[TOOLS] %s missing but the device is offline; not downloading", tool)
		installBarBegin(tool)
		installBarFailRetry(tool, "No internet — connect WiFi, then press A")
		return errToolsOffline
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	logPatch("[TOOLS] %s missing from %s; attempting automatic download into %s", tool, target, dir)
	installBarBegin(tool)
	if ctx == context.Background() {
		// A caller with no richer cancellation (the Play preload passes its
		// own loading-card token) rides the card's run token, minted by the
		// begin above: the X on this install's chip then stops the transfer.
		ctx = installBarRunContext()
	}
	if err := downloadHelperToolCtx(ctx, tool, target, config); err != nil {
		logPatch("[TOOLS] %s install failed: %v", tool, err)
		if ctx.Err() != nil {
			// Cancelled from the loading card: this is not an install failure,
			// so the card is hidden rather than left showing an error.
			installBarAbort()
			return ctx.Err()
		}
		installBarFailRetry(tool, toolInstallFailureReason(err))
		return err
	}
	installBarDone()
	logPatch("[TOOLS] %s installed at %s", tool, target)
	return nil
}

// toolInstallFailureReason renders an install error as one short, actionable
// line for the install card and toast.
func toolInstallFailureReason(err error) string {
	if err == nil {
		return ""
	}
	if !IsOnline() {
		return "No internet — connect WiFi, then press A"
	}
	var reasoned *toolInstallError
	if errors.As(err, &reasoned) {
		return firstLine(reasoned.reason)
	}
	if isDiskFullError(err) {
		return "SD card is full — free some space, then press A"
	}
	line, _, _ := strings.Cut(err.Error(), "\n")
	return firstLine(stripURLs(line))
}

// stripURLs drops web addresses from an error line. The install card has room
// for one short row, and a GitHub release URL used to fill all of it, hiding
// the actual reason ("extract ffmpeg from https://github.com/BtbN/FFm...").
// The full error, URL included, is still in the log.
func stripURLs(s string) string {
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		bare := strings.ToLower(strings.TrimLeft(f, "\"'(<"))
		if !strings.HasPrefix(bare, "http://") && !strings.HasPrefix(bare, "https://") {
			out = append(out, f)
			continue
		}
		// "download ffmpeg from <url>: ..." reads "download ffmpeg: ...".
		if n := len(out); n > 0 && (out[n-1] == "from" || out[n-1] == "at") {
			out = out[:n-1]
		}
		if strings.HasSuffix(f, ":") && len(out) > 0 && !strings.HasSuffix(out[len(out)-1], ":") {
			out[len(out)-1] += ":"
		}
	}
	return strings.Join(out, " ")
}

// helperToolEnv builds the environment for a helper-tool child process.
// yt-dlp's standalone build is a PyInstaller one-file program that unpacks
// itself into TMPDIR before it can run; on the TrimUI /tmp is a small tmpfs, so
// that unpack failed and looked like a broken download. Point TMPDIR at a
// writable folder next to the tools (on the SD card) instead.
func helperToolEnv(toolPath string, extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		// Drop any inherited TMPDIR: duplicates would leave the child's
		// choice of temp directory up to the loader instead of ours.
		if runtime.GOOS != "windows" && strings.HasPrefix(kv, "TMPDIR=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, extra...)
	if tmp := helperTempDir(toolPath); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	return env
}

// minHelperUnpackBytes is roughly what a PyInstaller one-file helper needs
// while it unpacks itself. A folder with less room is only used when nothing
// better exists, and is logged so the failure can be understood afterwards.
const minHelperUnpackBytes = 160 << 20

// helperTempDir picks the directory a PyInstaller one-file helper can unpack
// into. Every candidate is created, probed with a real write and has
// stale unpack directories cleared first: a folder that merely exists can
// still be read-only (the card remounts read-only after a filesystem error) or
// full, and both failures surface much later as a cryptic decompression error.
// An empty result means "keep the inherited temp directory".
func helperTempDir(toolPath string) string {
	if runtime.GOOS == "windows" {
		// Windows one-file builds unpack into %TEMP%, which is already correct.
		return ""
	}
	var candidates []string
	if toolPath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(toolPath), ".tmp"))
	}
	if tmp := os.TempDir(); tmp != "" {
		candidates = append(candidates, filepath.Join(tmp, "jukahub-tmp"))
	}
	return pickHelperTempDir(candidates)
}

// pickHelperTempDir chooses the first candidate that is writable and has room
// for an unpack, falling back to whichever writable candidate has the most free
// space. It is split out from helperTempDir so the selection can be tested
// without depending on the host platform's temp layout.
func pickHelperTempDir(candidates []string) string {
	best := ""
	var bestFree int64 = -1
	for _, dir := range candidates {
		if err := os.MkdirAll(dir, PermDir); err != nil {
			logPatch("[TOOLS] temp dir %s is unusable: %v", dir, err)
			continue
		}
		purgeStaleHelperDirs(dir)
		if err := tempDirWritable(dir); err != nil {
			logPatch("[TOOLS] temp dir %s is not writable: %v", dir, err)
			continue
		}
		free, err := freeBytes(dir)
		if err != nil {
			// Writable but unmeasured: good enough, and better than giving up.
			return dir
		}
		if free >= minHelperUnpackBytes {
			logPatch("[TOOLS] helper tools unpack into %s (%s free)", dir, humanBytes(free))
			return dir
		}
		if free > bestFree {
			best, bestFree = dir, free
		}
	}
	if best != "" {
		logPatch("[TOOLS] helper temp %s has only %s free; unpacking may fail", best, humanBytes(bestFree))
	}
	return best
}

// purgeStaleHelperDirs deletes the _MEIxxxxxx directories a killed or crashed
// PyInstaller one-file run leaves behind. Each one is tens of megabytes, so on
// a handheld they are what fills the card and makes the next run fail to
// unpack. A directory whose owning process is still running is left alone: a
// long download keeps its unpacked files for the whole run.
func purgeStaleHelperDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-30 * time.Minute)
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, "_MEI") {
			continue
		}
		if pid, ok := helperUnpackPID(name); ok {
			if processAlive(pid) {
				continue
			}
		} else if info, err := entry.Info(); err != nil || info.ModTime().After(cutoff) {
			// Not a PID-named directory: fall back to age so a run that is
			// currently unpacking is not deleted from under itself.
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			logPatch("[TOOLS] could not remove stale unpack dir %s: %v", name, err)
			continue
		}
		logPatch("[TOOLS] removed stale unpack dir %s", name)
	}
}

// helperUnpackPID extracts the owning process ID from a PyInstaller one-file
// unpack directory name (_MEI<pid>).
func helperUnpackPID(name string) (int, bool) {
	digits, ok := strings.CutPrefix(name, "_MEI")
	if !ok || digits == "" {
		return 0, false
	}
	pid, err := strconv.Atoi(digits)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// tempDirWritable proves dir accepts files by writing one, which a read-only
// mount or a full card refuses even though stat() succeeds.
func tempDirWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, writeErr := f.WriteString("ok")
	closeErr := f.Close()
	_ = os.Remove(name)
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// helperUnpackFailure reports whether a helper died before it could run. These
// errors come from the PyInstaller bootloader unpacking itself, so they say
// nothing about the site, the query or the network.
func helperUnpackFailure(stderr string) bool {
	return strings.Contains(stderr, "Failed to extract") ||
		strings.Contains(stderr, "decompression resulted in return code") ||
		strings.Contains(stderr, "[PYI-")
}

// ytDlpFailureReason turns a failed yt-dlp run into one short line for the UI.
// The results area used to show the raw bootloader stderr, and the toast
// blamed the network, which sent people chasing Wi-Fi when the real cause was
// a full SD card or a leftover unpack directory.
func ytDlpFailureReason(stderr string, err error, toolPath string) string {
	if helperUnpackFailure(stderr) {
		if dir := helperTempDir(toolPath); dir != "" {
			if free, freeErr := freeBytes(dir); freeErr == nil {
				if free < minHelperUnpackBytes {
					return fmt.Sprintf("yt-dlp could not unpack itself (%s free) — free space on the SD card, then retry", humanBytes(free))
				}
				// There is room, so the unpack failed for another reason (a stale
				// or read-only mount, or a half-written tool binary).
				return "yt-dlp could not unpack itself — retry; if it keeps failing, restart JukaHub"
			}
		}
		return "yt-dlp could not unpack itself — free space on the SD card, then retry"
	}
	if line := firstLine(strings.TrimSpace(stderr)); line != "" {
		return line
	}
	if err != nil {
		return err.Error()
	}
	return "yt-dlp failed"
}

// retryHelperToolInstall starts a fresh background install after a failure.
// It is triggered from the install card (A) so a dropped connection does not
// force the user to restart the app.
func retryHelperToolInstall(tool string) {
	if tool == "" {
		return
	}
	installBarBegin(tool)
	go func() {
		if err := EnsureHelperTool(tool, appConfig); err != nil {
			logPatch("[TOOLS] retry of %s failed: %v", tool, err)
			ShowToast("Could not install "+tool+" — "+toolInstallFailureReason(err), ToastKindError)
			refreshToolHealth()
			return
		}
		if _, ok := checkToolPresence(tool); !ok {
			ShowToast("Could not install "+tool+" — press A to retry", ToastKindError)
			refreshToolHealth()
			return
		}
		ShowToast(tool+" installed — restart JukaHub to use it", ToastKindSuccess)
		noteAwayToolInstalled(tool)
		refreshToolHealth()
	}()
}

// helperFileUsable reports whether path already holds a non-empty tool binary.
func helperFileUsable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// firstLine shortens an error for the install card.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncateRunesSuffix(s, 96, "...")
}

func downloadHelperTool(tool, target string, config *Config) error {
	return downloadHelperToolCtx(context.Background(), tool, target, config)
}

// downloadHelperToolCtx is downloadHelperTool that honours cancellation.
func downloadHelperToolCtx(ctx context.Context, tool, target string, config *Config) error {
	// ffmpeg and ffprobe ship together in one FFmpeg bundle, so a single
	// download installs both.
	if tool == "ffmpeg" || tool == "ffprobe" {
		return downloadFFmpegBundleCtx(ctx, target, config)
	}
	if tool == "yt-dlp" {
		return downloadYtDlpCtx(ctx, target, config)
	}
	return fmt.Errorf("no downloader implemented for %q", tool)
}

// ffmpegAssetNames maps a platform to the exact BtbN/FFmpeg-Builds release
// asset names that work there, preferred first. The GPL build carries the
// fullest codec set (x264/x265 encoders for yt-dlp merges); the LGPL build is
// the fallback if the GPL asset ever disappears.
func ffmpegAssetNames(goos, goarch string) ([]string, error) {
	switch goos {
	case "windows":
		switch goarch {
		case "amd64":
			return []string{"ffmpeg-master-latest-win64-gpl.zip", "ffmpeg-master-latest-win64-lgpl.zip"}, nil
		case "arm64":
			return []string{"ffmpeg-master-latest-winarm64-gpl.zip", "ffmpeg-master-latest-winarm64-lgpl.zip"}, nil
		}
	case "linux":
		switch goarch {
		case "amd64":
			return []string{"ffmpeg-master-latest-linux64-gpl.tar.xz", "ffmpeg-master-latest-linux64-lgpl.tar.xz"}, nil
		case "arm64":
			// TrimUI Smart Pro and other aarch64 handhelds.
			return []string{"ffmpeg-master-latest-linuxarm64-gpl.tar.xz", "ffmpeg-master-latest-linuxarm64-lgpl.tar.xz"}, nil
		}
	}
	return nil, fmt.Errorf("no ffmpeg build for %s/%s (supported: windows/linux on amd64, arm64)", goos, goarch)
}

// ffmpegArchiveExt returns the archive extension used in a release asset URL
// (BtbN ships .zip for Windows and .tar.xz for Linux builds).
func ffmpegArchiveExt(url string) string {
	lower := strings.ToLower(url)
	for _, ext := range []string{".tar.xz", ".tar.gz", ".zip"} {
		if strings.HasSuffix(lower, ext) {
			return ext
		}
	}
	return ".zip"
}

// downloadFFmpegBundle downloads the FFmpeg build for this platform and
// installs ffmpeg and ffprobe into the tools directory. The archive nests the
// binaries a few folders deep, so extraction is by base name.
func downloadFFmpegBundle(target string, config *Config) error {
	return downloadFFmpegBundleCtx(context.Background(), target, config)
}

// toolInstallError is an install failure with the one short line the install
// card and toast show for it. The underlying error stays attached for the log
// and for errors.Is.
type toolInstallError struct {
	reason string
	err    error
}

func (e *toolInstallError) Error() string {
	if e.err == nil {
		return e.reason
	}
	return e.reason + ": " + e.err.Error()
}

func (e *toolInstallError) Unwrap() error { return e.err }

// ffmpegFamilyTool reports whether tool ships in the FFmpeg bundle, so a
// failed bundle install is not repeated, download and all, for each member.
func ffmpegFamilyTool(tool string) bool {
	switch strings.TrimSuffix(strings.ToLower(tool), ".exe") {
	case "ffmpeg", "ffprobe", "ffplay":
		return true
	}
	return false
}

// ffmpegBundleWanted lists the binaries to take out of the FFmpeg bundle: the
// tool the install was started for, plus ffmpeg and ffprobe when they are
// missing. A tool that is already there is never replaced: a running player
// may be using it.
//
// BtbN's bundle also carries ffplay, which is never unpacked: nothing in
// JukaHub runs it (the embedded player decodes with ffmpeg), and on the
// handheld it could not start anyway, because BtbN links it against X11.
// Leaving it in the archive saves ~130 MB of SD card writes and space.
func ffmpegBundleWanted(dir, need, goos string) []string {
	suffix := ""
	if goos == "windows" {
		suffix = ".exe"
	}
	var wanted []string
	for _, name := range []string{strings.ToLower(need), "ffmpeg" + suffix, "ffprobe" + suffix} {
		if name == "" || helperFileUsable(filepath.Join(dir, name)) {
			continue
		}
		duplicate := false
		for _, w := range wanted {
			if w == name {
				duplicate = true
				break
			}
		}
		if !duplicate {
			wanted = append(wanted, name)
		}
	}
	return wanted
}

// ffmpegBundleCachePath is where a downloaded FFmpeg bundle waits while it is
// unpacked, and stays after an install that failed for a reason other than
// the archive itself (a full card, a cancel). The next attempt unpacks it
// again instead of downloading ~130 MB over handheld Wi-Fi once more: a failed
// install used to cost a full download on every retry, and again for each of
// ffplay, ffmpeg and ffprobe.
func ffmpegBundleCachePath(dir, ext string) string {
	return filepath.Join(dir, ".ffmpeg-bundle"+ext)
}

// ffmpegBundleCacheMaxAge bounds how old a kept bundle may be and still be
// unpacked. BtbN rebuilds its "latest" release daily, and after a couple of
// days a fresh download is the better install.
const ffmpegBundleCacheMaxAge = 48 * time.Hour

// ffmpegBundleCacheUsable reports whether the bundle kept at path can be
// unpacked instead of downloading again. Only a complete download is ever
// renamed to that path, so presence, a plausible size and its age decide.
func ffmpegBundleCacheUsable(path string, now time.Time) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1<<20 {
		return false
	}
	return now.Sub(info.ModTime()) <= ffmpegBundleCacheMaxAge
}

// ffmpegBundleSpaceNeeded estimates the free space an install needs: the
// archive unless it is already on disk, plus each binary it unpacks. A static
// BtbN binary is about as large as the whole compressed bundle, so each one
// is budgeted at the archive's size. That errs low on purpose: the check only
// stops an install that certainly cannot fit.
func ffmpegBundleSpaceNeeded(archiveSize int64, haveArchive bool, binaries int) int64 {
	if archiveSize <= 0 || binaries <= 0 {
		return 0
	}
	need := int64(binaries) * archiveSize
	if !haveArchive {
		need += archiveSize
	}
	return need
}

// ffmpegBundleSpaceCheck refuses an install that cannot fit before the bundle
// is downloaded for nothing. Where free space cannot be measured (Windows) the
// install goes ahead.
func ffmpegBundleSpaceCheck(dir string, archiveSize int64, haveArchive bool, binaries int) error {
	need := ffmpegBundleSpaceNeeded(archiveSize, haveArchive, binaries)
	if need <= 0 {
		return nil
	}
	free, err := freeBytes(dir)
	if err != nil || free >= need {
		return nil
	}
	return &toolInstallError{reason: fmt.Sprintf("ffmpeg needs %s free space, only %s left", humanBytes(need), humanBytes(free))}
}

// toolScratchMaxAge is how old a partial download or unpack stage in the
// tools folder must be before it counts as a leftover. A younger one may
// belong to an install that is running right now (Toolbox installs unpack
// into the same kind of stage).
const toolScratchMaxAge = 30 * time.Minute

// purgeStaleToolScratch deletes what a killed install leaves in the tools
// folder: partial downloads and unpack stages. Each can be hundreds of
// megabytes, and on a handheld a few of them fill the card, after which every
// retry fails while unpacking.
func purgeStaleToolScratch(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".tool-stage-") && !strings.HasPrefix(name, ".ffmpeg-download-") &&
			!strings.HasPrefix(name, ".yt-dlp-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < toolScratchMaxAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			logPatch("[TOOLS] could not remove leftover %s: %v", name, err)
			continue
		}
		logPatch("[TOOLS] removed leftover %s from an interrupted install", name)
	}
}

// isDiskFullError reports an out-of-space failure by its text, which is what
// survives the exec and wrapping layers: ENOSPC's on Linux, ERROR_DISK_FULL's
// on Windows.
func isDiskFullError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no space left") || strings.Contains(s, "not enough space")
}

// downloadFFmpegArchive downloads the bundle beside the tools and renames it
// to cache only once it is complete, so a kept bundle is never a partial one.
// Staying on the SD card matters: on TrimUI /tmp is a small tmpfs on another
// filesystem.
func downloadFFmpegArchive(ctx context.Context, url, cache string) error {
	_ = os.Remove(cache)
	dir := filepath.Dir(cache)
	tmp, err := os.CreateTemp(dir, ".ffmpeg-download-*"+ffmpegArchiveExt(url))
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpName)
	logPatch("[TOOLS] downloading ffmpeg from %s", url)
	installBarPhase(installPhaseDownload)
	if err := downloadFileWithProgressCtx(ctx, url, tmpName, installBarProgress); err != nil {
		if isDiskFullError(err) {
			return &toolInstallError{reason: "SD card is full — free some space, then retry", err: err}
		}
		return fmt.Errorf("download ffmpeg: %w", err)
	}
	if err := os.Rename(tmpName, cache); err != nil {
		return fmt.Errorf("keep the ffmpeg download: %w", err)
	}
	return nil
}

// downloadFFmpegBundleCtx is downloadFFmpegBundle that stops when ctx is
// cancelled: a Play the user backed out of must not keep the bundle download
// running behind the scene they went back to.
func downloadFFmpegBundleCtx(ctx context.Context, target string, config *Config) error {
	if ctx == nil {
		ctx = context.Background()
	}
	assets, err := ffmpegAssetNames(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create tools directory %s: %w", dir, err)
	}
	purgeStaleToolScratch(dir, time.Now())
	wanted := ffmpegBundleWanted(dir, filepath.Base(target), runtime.GOOS)
	// Every candidate asset of a platform has the same extension, and the
	// unpacker picks its reader from the name.
	cache := ffmpegBundleCachePath(dir, ffmpegArchiveExt(assets[0]))
	reuse := ffmpegBundleCacheUsable(cache, time.Now())
	var archiveSize int64
	url := ""
	if reuse {
		archiveSize = fileSize(cache)
		logPatch("[TOOLS] unpacking the ffmpeg bundle kept from the last attempt (%s)", humanBytes(archiveSize))
	} else {
		// A stale or partial copy goes before free space is measured.
		_ = os.Remove(cache)
		installBarPhase(installPhaseResolving)
		asset, err := resolveToolAsset("BtbN/FFmpeg-Builds", "", assets...)
		if err != nil {
			return fmt.Errorf("find ffmpeg release: %w", err)
		}
		archiveSize, url = asset.Size, asset.URL
	}
	if err := ffmpegBundleSpaceCheck(dir, archiveSize, reuse, len(wanted)); err != nil {
		return err
	}
	if !reuse {
		if err := downloadFFmpegArchive(ctx, url, cache); err != nil {
			return err
		}
	}
	installBarResetProgress()
	installBarPhase(installPhaseExtract)
	installBarDetail("Unpacking — this can take a minute")
	found, err := extractToolFilesCtx(ctx, cache, dir, wanted, installBarProgress)
	if err != nil {
		damaged := errors.Is(err, errBadToolArchive)
		if damaged {
			// A damaged download is useless; the next attempt fetches a new one.
			_ = os.Remove(cache)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Cancelled: an intact download is kept for the next attempt.
			return ctxErr
		}
		logPatch("[TOOLS] unpacking %s failed: %v", filepath.Base(cache), err)
		if damaged {
			return &toolInstallError{reason: "Download was damaged — retry downloads it again", err: err}
		}
		if isDiskFullError(err) {
			return &toolInstallError{reason: "SD card is full — free some space, then retry", err: err}
		}
		return fmt.Errorf("unpack ffmpeg: %w", err)
	}
	// Whatever the checks below decide, this download has served its purpose.
	_ = os.Remove(cache)
	installBarDetail("")
	if runtime.GOOS != "windows" {
		for _, name := range found {
			if err := os.Chmod(filepath.Join(dir, name), PermExec); err != nil {
				logPatch("[TOOLS] warn: cannot chmod %s: %v", name, err)
			}
		}
	}
	// The tool that triggered this download must actually be in the bundle.
	need := strings.ToLower(filepath.Base(target))
	hasNeed := false
	for _, name := range found {
		if name == need {
			hasNeed = true
			break
		}
	}
	if !hasNeed {
		return fmt.Errorf("ffmpeg bundle does not contain %s (found %v)", need, found)
	}
	installBarPhase(installPhaseVerify)
	var broken []string
	for _, name := range found {
		p := filepath.Join(dir, name)
		if err := verifyHelperTool(p); err != nil {
			// A file that is the wrong format or truncated is worse than no
			// file: remove it so the next attempt downloads again.
			_ = os.Remove(p)
			broken = append(broken, name)
			logPatch("[TOOLS] %s failed validation and was removed: %v", name, err)
			continue
		}
		logPatch("[TOOLS] %s installed in %s (%d bytes)", name, dir, fileSize(p))
	}
	if len(broken) > 0 {
		return fmt.Errorf("could not install %s (check free space in %s, then retry)", strings.Join(broken, ", "), dir)
	}
	return nil
}

// ytDlpAssetName maps a platform to the official yt-dlp release asset that runs
// standalone there. TrimUI devices have no Python runtime, so the self-contained
// linux builds (not the "yt-dlp" python zipimport script) are the correct
// choice. The downloaded asset is renamed to the canonical tool name
// ("yt-dlp" / "yt-dlp.exe") so every caller can rely on it.
func ytDlpAssetName(goos, goarch string) (string, error) {
	switch goos {
	case "windows":
		switch goarch {
		case "amd64", "arm64":
			return "yt-dlp.exe", nil
		case "386":
			return "yt-dlp_x86.exe", nil
		}
	case "linux":
		switch goarch {
		case "arm64":
			// TrimUI Smart Pro and other aarch64 handhelds.
			return "yt-dlp_linux_aarch64", nil
		case "arm":
			return "yt-dlp_linux_armv7l", nil
		case "amd64":
			return "yt-dlp_linux", nil
		}
	case "darwin":
		return "yt-dlp_macos", nil
	}
	return "", fmt.Errorf("no standalone yt-dlp build for %s/%s", goos, goarch)
}

// versionProbeArgs returns the version flag the given tool understands:
// the ffmpeg family takes a single-dash "-version", yt-dlp takes "--version".
func versionProbeArgs(base string) []string {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(base), ".exe"))
	switch name {
	case "ffmpeg", "ffplay", "ffprobe":
		return []string{"-version"}
	}
	return []string{"--version"}
}

// verifyHelperTool validates a freshly installed binary without executing it.
//
// Running the tool here was the failure mode on the TrimUI: yt-dlp's
// standalone build unpacks itself into TMPDIR, so a perfectly good download
// failed its --version probe (small or read-only /tmp) and was deleted, only to
// be downloaded again on the next launch. Presence, a plausible size and a
// valid executable header are enough to accept an install; if the binary
// cannot run for some other reason, the error surfaces when the tool is used
// and the file stays on disk instead of silently disappearing.
func verifyHelperTool(target string) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", filepath.Base(target))
	}
	if info.Size() < minHelperToolBytes {
		return fmt.Errorf("%s looks truncated (%d bytes)", filepath.Base(target), info.Size())
	}
	f, err := os.Open(target)
	if err != nil {
		return err
	}
	header := make([]byte, 8)
	n, readErr := io.ReadFull(f, header)
	f.Close()
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		return readErr
	}
	if !executableHeaderOK(header[:n]) {
		return fmt.Errorf("%s is not a valid executable for this platform", filepath.Base(target))
	}
	return nil
}

// executableHeaderOK checks the magic bytes of a downloaded binary. It only
// validates the platform formats JukaHub ships for (ELF on Linux, PE on
// Windows); other platforms accept any non-empty file.
func executableHeaderOK(header []byte) bool {
	switch runtime.GOOS {
	case "windows":
		return len(header) >= 2 && header[0] == 'M' && header[1] == 'Z'
	case "linux":
		return len(header) >= 4 && header[0] == 0x7f && header[1] == 'E' && header[2] == 'L' && header[3] == 'F'
	default:
		return true
	}
}

// downloadYtDlp downloads the platform-appropriate standalone yt-dlp build,
// installs it under the canonical tool name, makes it executable on Unix, and
// validates the file (size and ELF/PE header) before reporting success.
//
// The download is staged *inside the tools directory*, not the system temp
// directory: on TrimUI, /tmp is a different filesystem from the SD card, so
// renaming a temp file into place fails with EXDEV (invalid cross-device
// link). That left yt-dlp downloaded but never installed, while the ffmpeg
// bundle (which is copied, not renamed) worked.
func downloadYtDlp(target string, config *Config) error {
	return downloadYtDlpCtx(context.Background(), target, config)
}

// downloadYtDlpCtx is downloadYtDlp bounded by ctx (see downloadFileWithProgressCtx).
func downloadYtDlpCtx(ctx context.Context, target string, config *Config) error {
	asset, err := ytDlpAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	installBarPhase(installPhaseResolving)
	url, err := resolveToolURL("yt-dlp/yt-dlp", "", asset)
	if err != nil {
		return fmt.Errorf("find yt-dlp release asset %q: %w", asset, err)
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create tools directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".yt-dlp-*.part")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()
	logPatch("[TOOLS] downloading %s from %s", asset, url)
	installBarPhase(installPhaseDownload)
	if err := downloadFileWithProgressCtx(ctx, url, tmpPath, installBarProgress); err != nil {
		return fmt.Errorf("download yt-dlp from %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, PermExec); err != nil {
		logPatch("[TOOLS] warn: cannot chmod staged yt-dlp: %v", err)
	}
	// Same directory, so this rename never crosses a filesystem.
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("install yt-dlp to %s: %w", target, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, PermExec); err != nil {
			logPatch("[TOOLS] warn: cannot chmod yt-dlp: %v", err)
		}
	}
	installBarPhase(installPhaseVerify)
	if err := verifyHelperTool(target); err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("yt-dlp download is unusable and was removed: %w", err)
	}
	logPatch("[TOOLS] yt-dlp installed at %s (asset %s, %d bytes)", target, asset, fileSize(target))
	return nil
}

// helperToolPath returns the absolute path to a required helper tool, auto-downloading it
// when missing. It is intentionally tolerant of transient network failures so the app
// can still launch in offline environments and retry later.
func helperToolPath(tool string, config *Config) (string, error) {
	return helperToolPathCtx(context.Background(), tool, config)
}

// helperToolPathCtx is helperToolPath that abandons the auto-download when ctx
// is cancelled, so pressing B during a cold start stops the transfer instead of
// letting it finish behind the press.
func helperToolPathCtx(ctx context.Context, tool string, config *Config) (string, error) {
	if tool == "" {
		return "", fmt.Errorf("empty tool name")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	toolPath := getToolPath(tool, config)
	if toolPath == "" {
		return "", fmt.Errorf("unable to resolve tools path for %s", tool)
	}
	info, statErr := os.Stat(toolPath)
	if statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return toolPath, nil
	}
	if statErr == nil && info.Size() == 0 {
		// Broken placeholder from an interrupted install.
		_ = os.Remove(toolPath)
		statErr = os.ErrNotExist
	}
	if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("tools path %s exists but is not accessible: %w", toolPath, statErr)
	}
	logPatch("[TOOLS] %s missing at %q; attempting download", tool, toolPath)
	if err := ensureHelperToolCtx(ctx, tool, config); err != nil {
		logPatch("[TOOLS] auto-download of %s failed: %v", tool, err)
		return "", fmt.Errorf("missing %s and auto-download failed: %w", tool, err)
	}
	verified := getToolPath(tool, config)
	if verified == "" {
		return "", fmt.Errorf("download reported success but %s path is still unavailable", tool)
	}
	if _, err := os.Stat(verified); err != nil {
		return "", fmt.Errorf("download reported success but %s is still missing: %w", tool, err)
	}
	return verified, nil
}

// helperToolVersion returns the version string for a helper tool when available.
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
	// The ffmpeg family only understands "-version"; yt-dlp takes "--version".
	cmd := exec.CommandContext(ctx, path, versionProbeArgs(path)...)
	cmd.Env = helperToolEnv(path)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	s = strings.ReplaceAll(s, "\n", " ")
	return truncateRunes(s, 60)
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
	// A release manifest that has not changed answers 304, which keeps the
	// update check cheap on the device's connection.
	applyConditionalHeaders(req, raw)
	resp, err := doWithRetry(ctx, client, req, defaultRetryPolicy())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if body, ok := conditionalBody(raw); ok {
			return body, nil
		}
		return nil, fmt.Errorf("HTTP 304 from %s without a cached copy", raw)
	}
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
	storeConditionalResponse(raw, resp, data)
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
	// Digest is published by the releases API as "sha256:<hex>" for uploaded
	// assets. It is the only integrity statement the updater can check without
	// a second signed document, so an absent digest simply means no check.
	Digest string `json:"digest"`
}

// githubRelease is a minimal GitHub release record.
type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	PublishedAt string        `json:"published_at"`
	Body        string        `json:"body"`
	Assets      []githubAsset `json:"assets"`
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

// copyFile copies a file from src to dst, creating directories as needed.
// It ensures the copy is synced to disk before returning.
// Returns an error if the source doesn't exist, can't be read, or the
// destination can't be written/synced.
func copyFile(src, dst string) error {
	// Validate inputs
	if src == "" || dst == "" {
		return fmt.Errorf("copyFile: src and dst must be non-empty")
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copyFile: open source %s: %w", src, err)
	}
	defer in.Close()

	// Create destination directory if needed
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("copyFile: create directory %s: %w", dstDir, err)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("copyFile: create destination %s: %w", dst, err)
	}
	defer func() {
		// Best-effort close, but don't mask prior errors
		if closeErr := out.Close(); closeErr != nil && err == nil {
			logPatch("[PATCH] copyFile: close error for %s: %v", dst, closeErr)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copyFile: copy %s -> %s: %w", src, dst, err)
	}

	if err := out.Sync(); err != nil {
		return fmt.Errorf("copyFile: sync %s: %w", dst, err)
	}

	return nil
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
	skipPath string // running executable: never restored by a startup rollback
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
		if j.skipPath != "" && sameFilePath(e.Path, j.skipPath) {
			// The journal is being replayed by that very executable. Online
			// replacement of a running image fails on Windows (and would throw
			// away a successful update anyway), so leave it alone.
			continue
		}
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

// sameFilePath reports whether two paths name the same file, tolerating
// relative paths and Windows casing.
func sameFilePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
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
	self, _ := os.Executable()
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
		// path is runtime state, so a journal read back from disk has none.
		// Without it rollback can neither delete the journal nor its backup
		// folder, and the same interrupted update replays on every launch,
		// silently restoring the old binary and jukaconfig.json each time.
		j.path = p
		j.skipPath = self
		if err := j.rollback(); err == nil {
			recovered++
		} else {
			logPatch("journal %s rolled back with errors: %v", filepath.Base(p), err)
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
// Returns a short human description of what changed; missing files are
// reported explicitly rather than as a generic read error.
func MigrateConfigFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("config file missing: %s", path)
		}
		return "", fmt.Errorf("config read: %w", err)
	}
	// Decode into a generic map so unknown fields survive the migration.
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("config is not valid JSON: %w", err)
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
	// These are the tools playback runs; ffplay is not one of them.
	if DetectDevice().IsTrimui() {
		for _, tool := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
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
			if entries, err := os.ReadDir(filepath.Join(dir, sub)); err == nil {
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

// DiagnosticsText builds the redacted report shared by the Patch export and
// the in-app bug report. It deliberately contains no config values, URLs,
// usernames, tokens or private paths: only build, device and health facts.
func DiagnosticsText(config *Config) string {
	var sb strings.Builder
	sb.WriteString("JukaHub diagnostics\n")
	sb.WriteString("===================\n\n")
	sb.WriteString("Version      : " + versionString(config) + "\n")
	if build := installedBuildTimeLabel(); build != "" {
		sb.WriteString("Build        : " + build + "\n")
	}
	if tag := readInstalledReleaseInfo().Tag; tag != "" {
		sb.WriteString("Release      : " + tag + "\n")
	}
	sb.WriteString("Platform     : " + P().Name() + " " + runtime.GOOS + "/" + runtime.GOARCH + "\n")
	sb.WriteString("Device       : " + deviceDisplayName(DetectDevice()) + "\n")
	sb.WriteString(fmt.Sprintf("CPUs         : %d\n", runtime.NumCPU()))
	if config != nil {
		elements := 0
		for _, sc := range config.Scenes {
			elements += len(sc.Elements)
		}
		sb.WriteString(fmt.Sprintf("Config       : %d scene(s), %d element(s), schema %d\n",
			len(config.Scenes), elements, config.SchemaVersion))
	}
	// Support triage facts. Still no paths, addresses, names or config values:
	// free space, connectivity state, how many emulators were detected and how
	// much of the detected list is actually usable.
	storage := "unknown"
	if dir, err := PatchStateDir(); err == nil {
		if free, err := freeBytes(dir); err == nil {
			storage = fmt.Sprintf("%d MiB", free>>20)
		}
	}
	sb.WriteString("Storage free : " + storage + "\n")
	if sd := sdHealthCached(); sd.Total > 0 || sd.Free >= 0 || sd.FSType != "" {
		sb.WriteString("SD card      : " + sdHealthLine(sd) + "\n")
		if sd.Probed {
			verdict := "read back verified"
			if !sd.Verified {
				verdict = "read back FAILED"
			}
			sb.WriteString(fmt.Sprintf("SD write     : %.1f MB/s (%s)\n", sd.WriteMBps, verdict))
		}
		for _, finding := range sd.Findings {
			sb.WriteString("SD warning   : " + finding + "\n")
		}
	}
	if battery := batteryHealthReport(); batteryHealthRelevant(battery) {
		sb.WriteString("Battery      : " + batteryHealthLine(battery) + "\n")
		for _, finding := range battery.Findings {
			sb.WriteString("Battery note : " + finding + "\n")
		}
	}
	// Which release archive this build would install. A device whose model
	// could not be read still has a target; saying so here saves a support
	// round trip when an update is refused for another reason.
	if target, err := jukaHubUpdateTargetFor(DetectDevice(), runtime.GOOS, runtime.GOARCH); err == nil {
		sb.WriteString("Update asset : " + target.Asset + "\n")
	} else {
		sb.WriteString("Update asset : none (" + err.Error() + ")\n")
	}
	sb.WriteString("Network      : " + NetworkStatus() + "\n")
	if tag := availableUpdateTag(); tag != "" {
		sb.WriteString("Update ready : " + availableUpdateLabel() + "\n")
	}
	usable, unusable, listed := 0, 0, 0
	for _, e := range emulatorEntries() {
		listed++
		switch e.Status {
		case emuAvailable:
			usable++
		case emuBroken:
			unusable++
		}
	}
	if listed > 0 {
		sb.WriteString(fmt.Sprintf("Emulators    : %d usable, %d unusable, %d listed\n", usable, unusable, listed))
	}
	if snap := configHealth.Snapshot(); snap != nil {
		loadErrs, _ := snap["load_errors"].(int)
		saveErrs, _ := snap["save_errors"].(int)
		if loadErrs > 0 || saveErrs > 0 {
			sb.WriteString(fmt.Sprintf("Config health: %d load error(s), %d save error(s)\n", loadErrs, saveErrs))
		}
	}
	// Frame cost as the device measured it. The desktop benchmarks answer "is
	// this slower than before"; only this line answers "does it hold 60 fps on
	// the hardware", which is what a stutter report is really about.
	for _, line := range frameStats.snapshot().diagnosticsLines() {
		sb.WriteString(line + "\n")
	}
	if safeModeActive() {
		sb.WriteString("Safe mode    : on\n")
	}
	if line := safebootDiagnostic(); line != "" {
		sb.WriteString(line + "\n")
	}
	backups, _ := ListBackupSets()
	sb.WriteString(fmt.Sprintf("Backups      : %d set(s)\n", len(backups)))
	for _, b := range backups {
		sb.WriteString("  - " + b + "\n")
	}
	// Component versions close the report: a maintainer can match the app,
	// launcher, helper tools and installed packages against the published
	// digests without asking the reporter to run anything.
	sb.WriteString("\n" + componentVersionsText(config, true) + "\n")
	sb.WriteString("\nNo credentials, tokens, config values or private paths are included.\n")
	return sb.String()
}

// ExportDiagnostics writes a redacted diagnostics report into the patch
// reports directory and returns its path.
func ExportDiagnostics(config *Config) (string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	name := "diagnostics-" + time.Now().UTC().Format("20060102T150405Z") + ".txt"
	path := filepath.Join(dir, "reports", name)
	if err := os.WriteFile(path, []byte(DiagnosticsText(config)), 0o600); err != nil {
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

	rows := make([]PatchRow, 0, 5)

	// JukaHub app. A staged update or an advertised-but-unconfirmed release
	// is reflected in the row so the scene offers the right action.
	patchStateMu.Lock()
	stagedTag := patchState.LastUpdateVer
	availableTag := patchState.AvailableUpdateTag
	patchStateMu.Unlock()
	appStatus := StatusCurrent
	appAction := "Check for updates"
	switch {
	case stagedTag != "":
		appStatus = StatusStaged
		appAction = "Apply staged update"
	case availableTag != "":
		appStatus = StatusAvailable
		appAction = "Install update"
	}
	rows = append(rows, PatchRow{
		Component:   ComponentApp,
		Installed:   s.Installed[ComponentApp],
		Source:      "JukaHub",
		Status:      appStatus,
		Risk:        ComponentRisk(ComponentApp),
		Enabled:     true,
		ActionLabel: appAction,
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
	// Diagnostics report path if one exists.
	if dir, err := PatchStateDir(); err == nil {
		if entries, err := os.ReadDir(filepath.Join(dir, "reports")); err == nil && len(entries) > 0 {
			s.Diagnostics = filepath.Join(dir, "reports", entries[len(entries)-1].Name())
		}
	}
	return s
}
