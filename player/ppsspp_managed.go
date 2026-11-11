package main

// ppsspp_managed.go — a PPSSPP of JukaHub's own for PSP games on the handheld.
//
// A tester asked which PPSSPP JukaHub uses, and why it never updated. The
// honest answer was: none of its own. PSP games ran whatever the card has —
// the stock SD package's PPSSPP (1.18.1 in the 2025-04-30 package) or one of
// the builds CrossMix keeps in Emus/PSP/PPSSPP_<version>/ — and the "update"
// only staged an upstream aarch64 archive that hrydgard/ppsspp does not
// publish, so nothing ever happened.
//
// A build made for this hardware does exist: ben16w/minui-psp packages
// spruceUI's PPSSPP builds for the A133P handhelds (Smart Pro, Brick: the
// PowerVR build, "tg5040") and the Smart Pro S (A523, Mali: "tg5050") as one
// release asset, PSP.pak.zip, for MinUI and NextUI. GitHub states a SHA-256
// digest for every asset. This file installs that package into JukaHub's own
// data folder and lets PSP games use it:
//
//   - Check: the GitHub API's latest release, its PSP.pak.zip and the digest
//     GitHub states for it. An asset without a digest is refused outright:
//     this is a binary that runs as root on the card.
//   - Install: streamed to the card, verified, unpacked through an os.Root
//     (emuExtractZip) into a staging folder, searched for the binary for this
//     model (PPSSPPSDL_tg5040, PPSSPPSDL_tg5050), its assets and the libraries
//     it bundles, checked against the firmware with nativeLoadProblemWith, and
//     only then renamed into data/emulators/ppsspp/<tag>/. One previous build
//     is kept; older ones are removed — but never a folder that still has a
//     mount inside it or saves in it (ppssppSafeRemove).
//   - Launch: each build gets a generated jukahub-ppsspp.sh. It runs PPSSPP
//     with its own HOME (so the firmware's ini, which may pick a GPU backend
//     this build lacks, cannot break it), the bundled libraries first, and the
//     firmware's own SAVEDATA and PPSSPP_STATE bind-mounted over its memstick —
//     the way minui-psp shares MinUI's saves — so both PPSSPPs see the same
//     saves and going back loses nothing. When the bind fails, or the binary
//     exits with an error within ten seconds (a build that does not run on
//     this firmware), the script writes why and starts the game with the
//     firmware's launcher instead, so the user still plays.
//   - Use: PSP games resolve their launcher through data/game-launchers.json
//     first (resolveGameLauncher). JukaHub writes its entry there, keeping
//     everything else in the file, and remembers what the entry replaced, so
//     "Use the firmware's PPSSPP" puts back exactly what was there. A build is
//     switched to by itself only when it is newer than the firmware's PPSSPP
//     or the user chose it, and a build its script marked as not starting is
//     switched away from at the next look, with one toast.
//   - Update: the Emulator updates setting (off, notify, install) drives a
//     daily check of its own with the core pass's rules (emulatorautoupdate.go):
//     online, never over a game or another install, while charging or idle
//     unless long overdue, and a stamp only after a check that read the
//     release.
//
// Windows is not involved: the Emulators page there installs the official
// Windows PPSSPP (emuwininstall.go). The page side — the PSP tile's line and
// the PPSSPP card — is ppsspp_managed_ui.go.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// ppssppPakRepo is the minui-psp project: a MinUI and NextUI pak that
	// wraps spruceUI's PPSSPP builds for the TrimUI handhelds.
	ppssppPakRepo = "ben16w/minui-psp"
	// ppssppPakAsset is the one release asset it publishes.
	ppssppPakAsset = "PSP.pak.zip"
	// ppssppLibrarySystem is the library's name for the PSP: romSystemAliases
	// files Roms/PSP, Roms/PPSSPP and the long names under it, and
	// game-launchers.json is keyed by that name, case-insensitively.
	ppssppLibrarySystem = "PlayStation Portable"
	// ppssppMaxDownload and ppssppMaxUnpacked bound one package. The pak is
	// tens of megabytes; these only stop a runaway transfer or a zip bomb.
	ppssppMaxDownload = int64(256 << 20)
	ppssppMaxUnpacked = int64(1 << 30)
	// ppssppQuickExit is how soon, in seconds, an error exit means "this
	// build does not start here" rather than a game that ended badly.
	ppssppQuickExit = 10
)

// The files each build folder holds besides the package, and the state file in
// the folder above them.
const (
	ppssppScriptName   = "jukahub-ppsspp.sh"
	ppssppManifestName = "jukahub-build.json"
	ppssppStatusName   = "jukahub-ppsspp.status"
	ppssppBadName      = "jukahub-ppsspp.bad"
	ppssppStateName    = "state.json"
)

// What the user asked for: nothing yet (JukaHub decides by version), JukaHub's
// PPSSPP, or the firmware's.
const (
	ppssppChoiceAuto     = ""
	ppssppChoiceJukaHub  = "jukahub"
	ppssppChoiceFirmware = "firmware"
)

const (
	// ppssppAutoPoll is how often the scheduler wakes: often enough that a
	// build which did not start is switched away from soon after the game.
	ppssppAutoPoll = 2 * time.Minute
	// ppssppReviewDelay keeps the first look out of the boot path while still
	// landing its toast shortly after the splash.
	ppssppReviewDelay = 30 * time.Second
	// ppssppStartupDelay keeps the first network check behind the core pass,
	// which wants the same patch slot.
	ppssppStartupDelay = coreAutoUpdateStartupDelay + time.Minute
	// ppssppFirmwareTTL is how long a firmware detection is trusted before the
	// tile asks for a fresh one.
	ppssppFirmwareTTL = 10 * time.Minute
	// ppssppStatusMax is the size the launcher's status log is trimmed to.
	ppssppStatusMax = 64 << 10
)

// ppssppDigestPattern is the only digest an install accepts: GitHub's own
// "sha256:<64 hex>" for the asset.
var ppssppDigestPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?i)^sha256:[0-9a-f]{64}$`) })

// errPPSSPPUnusable marks a release whose package cannot be used on this
// device at all (no build for the model, no assets, a binary this firmware
// cannot load): unlike a network failure, trying it again changes nothing, so
// the daily pass marks the tag and waits for the next release.
var errPPSSPPUnusable = errors.New("not usable on this device")

var (
	// ppssppNotesVersionRe finds the PPSSPP version a release names in its
	// title or notes ("update ppsspp 1.20.3", "PPSSPP to v1.20.4").
	ppssppNotesVersionRe = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)ppsspp[^0-9\n]{0,16}?v?([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)
	})
	// ppssppNameVersionRe finds the version in a file or folder name:
	// CrossMix's ppsspp_1.17.1_gl.sh and PPSSPP_1.18.1/.
	ppssppNameVersionRe = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)ppsspp[ _-]?v?([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)
	})
	// ppssppPlatformDirRe is a TrimUI platform folder name (tg5040, tg5050).
	ppssppPlatformDirRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^tg[0-9]{4}$`) })
)

// The paths and roots the manager works in. They are variables so the tests
// can run it in a temporary folder.
var (
	ppssppManagedRootFn   = ppssppDefaultRoot
	ppssppOverridesPathFn = gameLauncherOverridesPath
	ppssppCardRootFn      = ppssppDefaultCardRoot
	ppssppEmuRootsFn      = func() []string { return deviceEmuRoots }
)

// ── Types ────────────────────────────────────────────────────────────────────

// ppssppRelease is one installable minui-psp release. Version is the PPSSPP
// version its title or notes name ("" when they name none: the binary says it
// once installed).
type ppssppRelease struct {
	Tag     string
	Name    string
	Version string
	Asset   string
	URL     string
	Size    int64
	Digest  string
	Date    string
}

// ppssppGitHubRelease is the part of the GitHub release JSON the check reads.
type ppssppGitHubRelease struct {
	TagName    string           `json:"tag_name"`
	Name       string           `json:"name"`
	Body       string           `json:"body"`
	Draft      bool             `json:"draft"`
	Prerelease bool             `json:"prerelease"`
	Assets     []emuGitHubAsset `json:"assets"`
}

// ppssppLayout is where things are inside an unpacked package, as slash paths
// relative to the build folder: the binary for this model, the folder holding
// PPSSPP's assets (the launcher's working directory), the HOME the build runs
// with, the assets folder itself, SDL's controller database when the package
// ships one, and the folders of the libraries it bundles.
type ppssppLayout struct {
	Binary       string   `json:"binary"`
	Work         string   `json:"work"`
	Home         string   `json:"home"`
	Assets       string   `json:"assets"`
	ControllerDB string   `json:"controller_db,omitempty"`
	Libs         []string `json:"libs,omitempty"`
}

// ppssppBuild is one installed build and where it came from.
type ppssppBuild struct {
	Tag         string       `json:"tag"`
	Version     string       `json:"version,omitempty"`
	Platform    string       `json:"platform"`
	Dir         string       `json:"dir"`
	Layout      ppssppLayout `json:"layout"`
	Asset       string       `json:"asset"`
	URL         string       `json:"url"`
	Digest      string       `json:"digest"`
	SHA256      string       `json:"sha256"`
	InstalledAt string       `json:"installed_at"`
}

// ppssppBinaryScan caches the version read from a firmware binary, keyed by its
// size and modification time, so a 40 MB file is not read on every boot.
type ppssppBinaryScan struct {
	Path    string `json:"path,omitempty"`
	Size    int64  `json:"size,omitempty"`
	ModTime int64  `json:"mod_time,omitempty"`
	Version string `json:"version,omitempty"`
}

// ppssppState is the manager's memory, data/emulators/ppsspp/state.json:
//
//   - Builds: the installed builds, newest first (at most a couple).
//   - Active: the tag PSP games use ("" = the firmware's PPSSPP); Previous:
//     the one active before it ("" = the firmware's), the way back when Active
//     does not start.
//   - Choice: what the user picked (ppssppChoice*).
//   - Override: game-launchers.json holds JukaHub's PSP entry; Replaced: the
//     raw JSON of the PSP entries it replaced, by key; FileExisted: whether the
//     file was there before JukaHub first wrote it.
//   - CheckedAt, LatestTag, LatestVersion: the last check that read the
//     release; Announced: the tag the notify toast already named.
//   - Bad: tags whose launcher reported that they do not start, with why.
//   - FallbackSeen: the last "opened in the firmware's PPSSPP" reason toasted.
//   - FirmwareScan: the cached firmware binary version.
type ppssppState struct {
	Builds        []ppssppBuild     `json:"builds,omitempty"`
	Active        string            `json:"active,omitempty"`
	Previous      string            `json:"previous,omitempty"`
	Choice        string            `json:"choice,omitempty"`
	Override      bool              `json:"override,omitempty"`
	Replaced      map[string]string `json:"replaced,omitempty"`
	FileExisted   bool              `json:"file_existed,omitempty"`
	CheckedAt     string            `json:"checked_at,omitempty"`
	LatestTag     string            `json:"latest_tag,omitempty"`
	LatestVersion string            `json:"latest_version,omitempty"`
	Announced     string            `json:"announced,omitempty"`
	Bad           map[string]string `json:"bad,omitempty"`
	FallbackSeen  string            `json:"fallback_seen,omitempty"`
	FirmwareScan  ppssppBinaryScan  `json:"firmware_scan"`
}

// ppssppFirmwareInfo is the firmware's PPSSPP as JukaHub would run it without
// its own: the launcher PSP games resolve to (a launcher the user pinned in
// game-launchers.json counts), its version when it can be read, and the
// memstick its saves are in.
type ppssppFirmwareInfo struct {
	Launcher string
	Version  string
	Stick    string
	At       time.Time
}

// ppssppMgr guards the state. Downloads, unpacking and scanning the card run
// outside the lock; it covers changing the state and the small files that go
// with it (state.json, game-launchers.json, a launcher script), and removing an
// old build folder. Only background work ever waits for it: the render thread
// reads the published copy (ppssppSnap), which is there from the first load.
var ppssppMgr struct {
	mu     sync.Mutex
	loaded bool
	state  ppssppState
}

var (
	// ppssppSnap is a copy of the state published after every change, so the
	// render thread reads it without taking the lock.
	ppssppSnap atomic.Pointer[ppssppState]
	// ppssppFirmwareSnap is the last firmware detection.
	ppssppFirmwareSnap atomic.Pointer[ppssppFirmwareInfo]
	// ppssppFirmwareBusy is set while a detection runs in the background.
	ppssppFirmwareBusy atomic.Bool
	// ppssppAutoStarted makes the scheduler start once.
	ppssppAutoStarted atomic.Bool
	// ppssppRevertFailedAt is when switching away from a bad build last failed
	// (UnixNano, 0 = never), so the retry backs off.
	ppssppRevertFailedAt atomic.Int64
)

// ── Paths and device ─────────────────────────────────────────────────────────

// ppssppDefaultRoot is data/emulators/ppsspp: in JukaHub's own data folder on
// the card, never in Emus/ (the firmware's) and never over its PPSSPP.
func ppssppDefaultRoot() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "emulators", "ppsspp")
}

// ppssppDefaultCardRoot is the SD card the firmware's PPSSPP lives on.
func ppssppDefaultCardRoot() string {
	if root := strings.TrimSpace(trimuiSDRootFn()); root != "" {
		return root
	}
	if IsTrimuiDevice() && dirExists("/mnt/SDCARD") {
		return "/mnt/SDCARD"
	}
	return ""
}

// ppssppTagDir is the folder name for a release tag. The tag comes from the
// network, so it is made a plain name, and one that cannot be mistaken for the
// manager's own hidden folders (.staging-…, .trash-…). Build folders live in
// their own builds/ folder, so no tag can collide with state.json or home/.
func ppssppTagDir(tag string) string {
	name := sanitizeFileName(tag)
	if strings.HasPrefix(name, ".") {
		name = "_" + name
	}
	return name
}

// ppssppBuildsDir holds one folder per installed build.
func ppssppBuildsDir() string {
	root := ppssppManagedRootFn()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "builds")
}

// ppssppHomeDir is the HOME every JukaHub build runs with. It sits outside the
// build folders on purpose: PPSSPP keeps the user's settings, cheats,
// screenshots, textures and homebrew under it (.config/ppsspp/PSP), so they
// carry from one build to the next by themselves and removing an old build can
// never take them along — and the firmware's saves are bind-mounted onto this
// memstick, never into a folder that pruning deletes.
func ppssppHomeDir() string {
	root := ppssppManagedRootFn()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "home")
}

// ppssppManagedMemstick is the PSP folder PPSSPP uses under that HOME on Linux.
func ppssppManagedMemstick() string {
	home := ppssppHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "ppsspp", "PSP")
}

// ppssppPlatformFor names the minui-psp build for a model: tg5040 for the
// A133P family — the Smart Pro, the Brick, and the Hammer on the Brick's board.
// NextUI files the Brick Pro (same chip and GPU) under tg5040 too, but no
// PPSSPP build was tested on it and standalone SDL apps have needed extra work
// for its pad, so it is offered as untested and never switched to by itself.
// tg5050 is the Smart Pro S (A523, Mali).
func ppssppPlatformFor(device DeviceModel) (platform string, untested bool) {
	switch device {
	case DeviceTrimuiSmartPro, DeviceTrimuiBrick, DeviceTrimuiHammer:
		return "tg5040", false
	case DeviceTrimuiBrickPro:
		return "tg5040", true
	case DeviceTrimuiSmartProS:
		return "tg5050", false
	}
	return "", false
}

var ppssppDeviceOnce sync.Once
var ppssppDeviceModel DeviceModel

// ppssppDevice is hakuxDevice, read once: detection can take a Smart Pro S
// named only by its panel for a Smart Pro, and an A133P build would not run on
// its A523.
func ppssppDevice() DeviceModel {
	ppssppDeviceOnce.Do(func() { ppssppDeviceModel = hakuxDevice() })
	return ppssppDeviceModel
}

// ppssppPlatform is the build this handheld takes ("" on Windows, a desktop,
// or a model nothing is published for).
func ppssppPlatform() (platform string, untested bool) {
	if IsWindows() || !IsTrimuiDevice() {
		return "", false
	}
	return ppssppPlatformFor(ppssppDevice())
}

// ppssppManagedEnabled reports whether the manager runs at all here.
func ppssppManagedEnabled() bool {
	return !IsWindows() && IsTrimuiDevice()
}

// ── State ────────────────────────────────────────────────────────────────────

func ppssppStatePath() string {
	root := ppssppManagedRootFn()
	if root == "" {
		return ""
	}
	return filepath.Join(root, ppssppStateName)
}

// ppssppLockedState returns the state, reading it on first use. The caller
// holds ppssppMgr.mu.
func ppssppLockedState() *ppssppState {
	if !ppssppMgr.loaded {
		ppssppMgr.loaded = true
		var st ppssppState
		if path := ppssppStatePath(); path != "" {
			if _, err := readJSONWithBackup(path, &st); err != nil {
				if !os.IsNotExist(err) {
					logWarn("ppsspp", "state unreadable, starting over: %v", err)
				}
				st = ppssppState{}
			}
		}
		ppssppMgr.state = st
		ppssppPublishLocked()
	}
	return &ppssppMgr.state
}

// ppssppPublishLocked publishes a copy of the state for lock-free readers.
func ppssppPublishLocked() {
	snap := ppssppMgr.state.clone()
	ppssppSnap.Store(&snap)
}

// ppssppSaveLocked publishes and writes the state.
func ppssppSaveLocked() error {
	ppssppPublishLocked()
	path := ppssppStatePath()
	if path == "" {
		return errors.New("JukaHub has no data folder for PPSSPP")
	}
	data, err := json.MarshalIndent(ppssppMgr.state, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o644)
}

// ppssppSnapshot is the current state for reading. It shares its slices and
// maps with the published copy, which is never changed after publishing.
func ppssppSnapshot() ppssppState {
	if p := ppssppSnap.Load(); p != nil {
		return *p
	}
	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	return ppssppLockedState().clone()
}

// ppssppResetForTest forgets the loaded state, so a test starts from its own
// folder.
func ppssppResetForTest() {
	ppssppMgr.mu.Lock()
	ppssppMgr.loaded = false
	ppssppMgr.state = ppssppState{}
	ppssppMgr.mu.Unlock()
	ppssppSnap.Store(nil)
	ppssppFirmwareSnap.Store(nil)
}

func (s ppssppState) clone() ppssppState {
	out := s
	out.Builds = append([]ppssppBuild(nil), s.Builds...)
	for i := range out.Builds {
		out.Builds[i].Layout.Libs = append([]string(nil), s.Builds[i].Layout.Libs...)
	}
	out.Replaced = ppssppCopyMap(s.Replaced)
	out.Bad = ppssppCopyMap(s.Bad)
	return out
}

func ppssppCopyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// build finds an installed build by tag.
func (s ppssppState) build(tag string) (ppssppBuild, bool) {
	if tag == "" {
		return ppssppBuild{}, false
	}
	for _, b := range s.Builds {
		if b.Tag == tag {
			return b, true
		}
	}
	return ppssppBuild{}, false
}

// newest is the most recently installed build.
func (s ppssppState) newest() (ppssppBuild, bool) {
	if len(s.Builds) == 0 {
		return ppssppBuild{}, false
	}
	return s.Builds[0], true
}

// path resolves a layout path inside the build folder ("" stays "").
func (b ppssppBuild) path(rel string) string {
	if rel == "" {
		return ""
	}
	return filepath.Join(b.Dir, filepath.FromSlash(rel))
}

func (b ppssppBuild) script() string { return filepath.Join(b.Dir, ppssppScriptName) }

func (b ppssppBuild) binary() string { return b.path(b.Layout.Binary) }

// home is the HOME the build runs with: the one all JukaHub builds share.
func (b ppssppBuild) home() string { return ppssppHomeDir() }

// memstick is the PSP folder PPSSPP keeps under that HOME.
func (b ppssppBuild) memstick() string { return ppssppManagedMemstick() }

// shippedSystem is the SYSTEM folder of the config the package ships in its
// own HOME ("" when it ships none): where a first install takes its defaults.
func (b ppssppBuild) shippedSystem() string {
	home := b.path(b.Layout.Home)
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "ppsspp", "PSP", "SYSTEM")
}

// label is how toasts and the card name the build.
func (b ppssppBuild) label() string {
	if b.Version != "" {
		return "PPSSPP " + b.Version
	}
	return "PPSSPP (minui-psp " + b.Tag + ")"
}

// usable reports whether the build's files are still there and its launcher
// has not reported that it does not start.
func (b ppssppBuild) usable(st ppssppState) bool {
	if b.Dir == "" || st.Bad[b.Tag] != "" || !regularFile(b.binary()) {
		return false
	}
	return !regularFile(filepath.Join(b.Dir, ppssppBadName))
}

// ── Versions ─────────────────────────────────────────────────────────────────

// ppssppNewer reports whether version a is known and newer than known b.
func ppssppNewer(a, b string) bool {
	return a != "" && b != "" && compareCoreVersions(a, b) > 0
}

// ppssppVersionFromNotes is the highest PPSSPP version the texts name: release
// notes that say "1.20.3 (was 1.19.3)" mean the newer one.
func ppssppVersionFromNotes(texts ...string) string {
	best := ""
	for _, text := range texts {
		for _, m := range ppssppNotesVersionRe().FindAllStringSubmatch(text, -1) {
			if best == "" || compareCoreVersions(m[1], best) > 0 {
				best = m[1]
			}
		}
	}
	return best
}

// ppssppVersionFromName is the version a file or folder name carries
// ("ppsspp_1.17.1_gl.sh", "PPSSPP_1.18.1"), or "".
func ppssppVersionFromName(name string) string {
	if m := ppssppNameVersionRe().FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// ppssppPlausibleVersion accepts what PPSSPP calls its releases: 1.<minor> or
// 1.<minor>.<patch> with a two-digit minor. Library version strings compiled
// into the same binary (zlib's 1.3, libpng's 1.6) never have one.
func ppssppPlausibleVersion(s string) string {
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "1" {
		return ""
	}
	for _, p := range parts {
		if p == "" || len(p) > 2 || strings.Trim(p, "0123456789") != "" {
			return ""
		}
	}
	minor, _ := strconv.Atoi(parts[1])
	if minor < 10 || minor > 39 {
		return ""
	}
	return s
}

// ppssppBestVersion returns the highest PPSSPP version string in data that is
// higher than best: "v1.18.1" as a C string, or "v1.18.1-12-gabcdef0" from a
// build between releases.
func ppssppBestVersion(data []byte, best string) string {
	marker := []byte("v1.")
	for i := 0; i < len(data); {
		j := bytes.Index(data[i:], marker)
		if j < 0 {
			break
		}
		at := i + j
		start := at + 1
		i = start
		if at > 0 {
			if c := data[at-1]; (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				continue
			}
		}
		k := start
		for k < len(data) && k-start < 12 && ((data[k] >= '0' && data[k] <= '9') || data[k] == '.') {
			k++
		}
		if k >= len(data) || (data[k] != 0 && data[k] != '-') {
			continue
		}
		if v := ppssppPlausibleVersion(string(data[start:k])); v != "" && (best == "" || compareCoreVersions(v, best) > 0) {
			best = v
		}
	}
	return best
}

// ppssppScanVersion reads a PPSSPP binary for its version string. The window
// carries the end of each chunk into the next, so a string cut by a read is
// still seen whole.
func ppssppScanVersion(r io.Reader) string {
	const chunk = 1 << 20
	const carry = 32
	buf := make([]byte, chunk)
	work := make([]byte, 0, chunk+carry)
	best := ""
	for {
		n, err := r.Read(buf)
		if n > 0 {
			work = append(work, buf[:n]...)
			best = ppssppBestVersion(work, best)
			if len(work) > carry {
				copy(work, work[len(work)-carry:])
				work = work[:carry]
			}
		}
		if err != nil {
			return best
		}
	}
}

// ppssppBinaryVersion is ppssppScanVersion for a file.
func ppssppBinaryVersion(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	return ppssppScanVersion(io.LimitReader(f, ppssppMaxUnpacked))
}

// ppssppIsELF reports whether a file starts with the ELF magic.
func ppssppIsELF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return string(magic[:]) == "\x7fELF"
}

// ── Release ──────────────────────────────────────────────────────────────────

// ppssppReleaseFrom reads one GitHub release: its PSP.pak.zip, which must come
// with a SHA-256 digest and fit the size limit, and the PPSSPP version its
// title or notes name.
func ppssppReleaseFrom(data []byte) (ppssppRelease, error) {
	var rel ppssppGitHubRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return ppssppRelease{}, errors.New("unreadable minui-psp release metadata")
	}
	tag := strings.TrimSpace(rel.TagName)
	if rel.Draft || tag == "" {
		return ppssppRelease{}, errors.New("the minui-psp release has no tag")
	}
	out := ppssppRelease{Tag: tag, Name: strings.TrimSpace(rel.Name), Version: ppssppVersionFromNotes(rel.Name, rel.Body)}
	for _, a := range rel.Assets {
		if !strings.EqualFold(strings.TrimSpace(a.Name), ppssppPakAsset) || strings.TrimSpace(a.BrowserDownloadURL) == "" {
			continue
		}
		digest := strings.TrimSpace(a.Digest)
		if !ppssppDigestPattern().MatchString(digest) {
			return ppssppRelease{}, fmt.Errorf("minui-psp %s publishes no SHA-256 digest for %s — an unverifiable build is not installed", tag, ppssppPakAsset)
		}
		if a.Size > ppssppMaxDownload {
			return ppssppRelease{}, fmt.Errorf("%s is %s, more than the %s limit", ppssppPakAsset, formatInstallBytes(a.Size), formatInstallBytes(ppssppMaxDownload))
		}
		out.Asset = strings.TrimSpace(a.Name)
		out.URL = strings.TrimSpace(a.BrowserDownloadURL)
		out.Size = a.Size
		out.Digest = strings.ToLower(digest)
		out.Date = strings.TrimSpace(a.UpdatedAt)
		return out, nil
	}
	return ppssppRelease{}, fmt.Errorf("minui-psp %s has no %s", tag, ppssppPakAsset)
}

// ppssppFetchRelease asks GitHub for the newest release. Every URL — the API
// and each redirect — is held to the download allowlist by fetchUpdateBytes.
func ppssppFetchRelease(ctx context.Context, client *http.Client) (ppssppRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", standaloneAPIBase, ppssppPakRepo)
	data, err := fetchUpdateBytes(ctx, client, url, patchMaxMetadata)
	if err != nil {
		return ppssppRelease{}, err
	}
	return ppssppReleaseFrom(data)
}

// ── Package layout ───────────────────────────────────────────────────────────

// ppssppInspectZip checks a downloaded package before anything is unpacked:
// every name stays inside the folder (emuArchiveRel refuses traversal and
// absolute paths), no two entries name one file on a case-insensitive FAT card,
// and the whole fits ppssppMaxUnpacked. It returns the unpacked size.
func ppssppInspectZip(archive string) (int64, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		if zr != nil {
			_ = zr.Close()
		}
		return 0, fmt.Errorf("%w (open %s: %v)", errBadToolArchive, filepath.Base(archive), err)
	}
	defer zr.Close()
	if len(zr.File) > emuMaxArchiveEntries {
		return 0, fmt.Errorf("%w (%d entries)", errBadToolArchive, len(zr.File))
	}
	seen := map[string]string{}
	var total int64
	for _, f := range zr.File {
		rel, err := emuArchiveRel(f.Name)
		if err != nil {
			return 0, err
		}
		if rel == "" || !f.Mode().IsRegular() {
			continue
		}
		key := strings.ToLower(filepath.ToSlash(rel))
		if other, dup := seen[key]; dup {
			return 0, fmt.Errorf("%s names one file twice (%s, %s), which a FAT card cannot hold", ppssppPakAsset, other, f.Name)
		}
		seen[key] = f.Name
		total += int64(f.UncompressedSize64)
		if total > ppssppMaxUnpacked {
			return 0, fmt.Errorf("%s unpacks to more than %s", ppssppPakAsset, formatInstallBytes(ppssppMaxUnpacked))
		}
	}
	return total, nil
}

// ppssppSharedObject reports a library file name: libfoo.so or libfoo.so.6.
func ppssppSharedObject(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".so") || strings.Contains(lower, ".so.")
}

// ppssppLooksLikeAssets reports whether a folder named assets is PPSSPP's: it
// holds the PSP font and UI atlases, flash0 or the compatibility list.
func ppssppLooksLikeAssets(dir string) bool {
	for _, name := range []string{"ppge_atlas.zim", "ui_atlas.zim", "compat.ini"} {
		if regularFile(filepath.Join(dir, name)) {
			return true
		}
	}
	return dirExists(filepath.Join(dir, "flash0"))
}

// ppssppPickBinary chooses the PPSSPP binary for platform among the files whose
// name starts with PPSSPPSDL: PPSSPPSDL_<platform> before a bare PPSSPPSDL, one
// in a folder named after the platform before one elsewhere, the shallowest
// on a tie. A build named for another model, or sitting in another model's
// folder (bin/tg5050/ on a tg5040), is never taken, and nor is a file that is
// not an ELF executable.
func ppssppPickBinary(dir string, rels []string, platform string) string {
	best, bestScore, bestDepth := "", 0, 0
	for _, rel := range rels {
		slash := filepath.ToSlash(rel)
		name := strings.ToLower(path.Base(slash))
		score := 0
		switch name {
		case "ppssppsdl_" + platform:
			score = 4
		case "ppssppsdl":
			score = 2
		default:
			continue
		}
		parent := path.Dir(slash)
		segments := []string{}
		if parent != "." {
			segments = strings.Split(strings.ToLower(parent), "/")
		}
		foreign := false
		for _, seg := range segments {
			switch {
			case seg == platform:
				score++
			case ppssppPlatformDirRe().MatchString(seg):
				foreign = true
			}
		}
		if foreign || !ppssppIsELF(filepath.Join(dir, rel)) {
			continue
		}
		if best == "" || score > bestScore || (score == bestScore && len(segments) < bestDepth) {
			best, bestScore, bestDepth = slash, score, len(segments)
		}
	}
	return best
}

// ppssppDiscoverLayout finds, in an unpacked package, the binary for platform,
// PPSSPP's assets, the HOME its config sits under and the folders of the
// libraries it bundles. The package's own layout is not documented, so nothing
// is assumed about where these sit.
func ppssppDiscoverLayout(dir, platform string) (ppssppLayout, error) {
	var bins, assets, libs []string
	libSeen := map[string]bool{}
	hasStdcxx := map[string]bool{}
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.Count(slash, "/") >= 8 {
				return filepath.SkipDir
			}
			if strings.EqualFold(d.Name(), "assets") && ppssppLooksLikeAssets(p) {
				assets = append(assets, slash)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		if ppssppSharedObject(name) {
			libDir := path.Dir(slash)
			if strings.ContainsAny(libDir, ":") {
				return nil
			}
			if !libSeen[libDir] {
				libSeen[libDir] = true
				libs = append(libs, libDir)
			}
			if strings.HasPrefix(strings.ToLower(name), "libstdc++.so") {
				hasStdcxx[libDir] = true
			}
			return nil
		}
		if strings.HasPrefix(strings.ToLower(name), "ppssppsdl") {
			bins = append(bins, slash)
		}
		return nil
	})
	if walkErr != nil {
		return ppssppLayout{}, walkErr
	}
	binary := ppssppPickBinary(dir, bins, platform)
	if binary == "" {
		if len(bins) > 0 {
			return ppssppLayout{}, fmt.Errorf("the package has no PPSSPP build for %s (found %s)", platform, strings.Join(bins, ", "))
		}
		return ppssppLayout{}, errors.New("the package holds no PPSSPPSDL binary")
	}
	binDir := path.Dir(binary)
	asset := ""
	for _, a := range assets {
		if path.Dir(a) == binDir {
			asset = a
			break
		}
	}
	if asset == "" && len(assets) > 0 {
		sort.SliceStable(assets, func(i, j int) bool { return strings.Count(assets[i], "/") < strings.Count(assets[j], "/") })
		asset = assets[0]
	}
	if asset == "" {
		return ppssppLayout{}, errors.New("the package holds no PPSSPP assets folder")
	}
	layout := ppssppLayout{Binary: binary, Assets: asset, Work: path.Dir(asset)}
	layout.Home = layout.Work
	for _, cand := range []string{layout.Work, binDir} {
		if dirExists(filepath.Join(dir, filepath.FromSlash(cand), ".config", "ppsspp")) {
			layout.Home = cand
			break
		}
	}
	if db := path.Join(asset, "gamecontrollerdb.txt"); regularFile(filepath.Join(dir, filepath.FromSlash(db))) {
		layout.ControllerDB = db
	}
	kept := libs[:0]
	for _, l := range libs {
		// The assets folder is data; a library there is never one PPSSPP loads.
		if l == asset || strings.HasPrefix(l, asset+"/") {
			continue
		}
		kept = append(kept, l)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if hasStdcxx[kept[i]] != hasStdcxx[kept[j]] {
			return hasStdcxx[kept[i]]
		}
		return strings.Count(kept[i], "/") < strings.Count(kept[j], "/")
	})
	if len(kept) > 8 {
		kept = kept[:8]
	}
	if len(kept) > 0 {
		layout.Libs = append([]string(nil), kept...)
	}
	return layout, nil
}

// ── Removing builds safely ───────────────────────────────────────────────────

// ppssppUnescapeMount decodes the octal escapes /proc/mounts uses in paths.
func ppssppUnescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

// ppssppMountPointsWithin lists the mount points in a /proc/mounts text that are
// dir or inside it. Both are slash paths: the table only exists on Linux.
func ppssppMountPointsWithin(mounts, dir string) []string {
	clean := path.Clean(filepath.ToSlash(dir))
	var out []string
	for _, line := range strings.Split(mounts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mp := path.Clean(ppssppUnescapeMount(fields[1]))
		if mp == clean || strings.HasPrefix(mp, clean+"/") {
			out = append(out, mp)
		}
	}
	return out
}

// ppssppMountsWithin reads the live mount table.
func ppssppMountsWithin(dir string) []string {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		if data, err = os.ReadFile("/proc/mounts"); err != nil {
			return nil
		}
	}
	return ppssppMountPointsWithin(string(data), dir)
}

// ppssppSavesWithin returns a SAVEDATA or PPSSPP_STATE folder under dir that is
// not empty, or "".
func ppssppSavesWithin(dir string) string {
	found := ""
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), "SAVEDATA") || strings.EqualFold(d.Name(), "PPSSPP_STATE") {
			if entries, rerr := os.ReadDir(p); rerr == nil && len(entries) > 0 {
				found = p
				return fs.SkipAll
			}
			return filepath.SkipDir
		}
		return nil
	})
	return found
}

// ppssppSafeRemove deletes a build (or staging) folder — only inside the
// manager's own folder, never the shared HOME, and never while anything is
// mounted inside it or it holds saves. Builds keep no mount points and no user
// data (both live in the shared HOME), so this is a second line of defence: a
// folder that leads into the firmware's saves through a bind must never be
// deleted, because RemoveAll would delete them through the mount.
//
// The folder is renamed out of the way first and checked again under its new
// name. A launch that resolves the old path from then on finds nothing to bind
// into, and one that bound just before the rename shows up in the second check
// (a mount moves with its folder).
func ppssppSafeRemove(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil
	}
	root := ppssppManagedRootFn()
	switch {
	case root == "" || !isPathWithin(dir, root) || filepath.Clean(dir) == filepath.Clean(root):
		return fmt.Errorf("refusing to remove %s: not a PPSSPP build folder", dir)
	case isPathWithin(ppssppHomeDir(), dir):
		return fmt.Errorf("refusing to remove %s: it holds JukaHub's PPSSPP settings", dir)
	}
	check := func(d string) error {
		if mounts := ppssppMountsWithin(d); len(mounts) > 0 {
			return fmt.Errorf("%s is still mounted inside %s", mounts[0], d)
		}
		if saves := ppssppSavesWithin(d); saves != "" {
			return fmt.Errorf("%s holds saves (%s)", d, saves)
		}
		return nil
	}
	if err := check(dir); err != nil {
		return err
	}
	trash := filepath.Join(filepath.Dir(dir), ".trash-"+filepath.Base(dir)+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := os.Rename(dir, trash); err != nil {
		return fmt.Errorf("could not move %s aside: %w", dir, err)
	}
	if err := check(trash); err != nil {
		if rerr := os.Rename(trash, dir); rerr != nil {
			logWarn("ppsspp", "left %s as %s: %v", dir, trash, rerr)
		}
		return err
	}
	return os.RemoveAll(trash)
}

// ppssppCleanLeftovers removes what an interrupted install or removal left in
// the builds folder (.staging-*, .replaced-*, .trash-*), the safe way.
func ppssppCleanLeftovers() {
	dir := ppssppBuildsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !(strings.HasPrefix(name, ".staging-") || strings.HasPrefix(name, ".replaced-") || strings.HasPrefix(name, ".trash-")) {
			continue
		}
		if err := ppssppSafeRemove(filepath.Join(dir, name)); err != nil {
			logWarn("ppsspp", "kept %s: %v", name, err)
		}
	}
}

// ppssppManagedProcessRunning reports whether a process runs a binary from the
// manager's folder (Linux /proc only).
func ppssppManagedProcessRunning(root string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil || root == "" {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || strings.Trim(e.Name(), "0123456789") != "" {
			continue
		}
		if exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe")); err == nil && isPathWithin(exe, root) {
			return true
		}
	}
	return false
}

// ppssppCleanStaleMounts takes down the binds a killed launch left behind, so
// nothing in JukaHub's data folder leads into the firmware's saves. It stands
// aside while one of the builds is running.
func ppssppCleanStaleMounts() {
	root := ppssppManagedRootFn()
	mounts := ppssppMountsWithin(root)
	if len(mounts) == 0 || ppssppManagedProcessRunning(root) {
		return
	}
	// Deepest first, so a mount inside another comes down before it.
	sort.Slice(mounts, func(i, j int) bool { return len(mounts[i]) > len(mounts[j]) })
	for _, mp := range mounts {
		if err := ppssppUnmount(mp); err != nil {
			logWarn("ppsspp", "could not unmount %s: %v", mp, err)
		} else {
			logInfo("ppsspp", "unmounted %s, left behind by a launch that was stopped", mp)
		}
	}
}

// ── Launcher script ──────────────────────────────────────────────────────────

// ppssppScriptSpec is what one launcher script needs: absolute paths of the
// build's pieces, the firmware memstick to share and the launcher to fall back
// to.
type ppssppScriptSpec struct {
	Build        string
	Binary       string
	Work         string
	Home         string
	Libs         []string
	ControllerDB string
	Stick        string
	Fallback     string
	Version      string
	Tag          string
}

// ppssppScriptComment keeps a value from the network on one comment line.
func ppssppScriptComment(s string) string {
	return ppssppBreakMarkers(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r < 0x20 {
			return ' '
		}
		return r
	}, s), " ")
}

// ppssppScriptMarkers are the words retroArchLauncher (savestate_resume.go)
// looks for in a launcher's text to treat it as RetroArch's, lower case.
var ppssppScriptMarkers = []string{"retroarch", "ra64.", "ra32.", "_libretro."}

// ppssppBreakMarkers puts sep after the first byte of every marker in text,
// matched case-insensitively (the markers are ASCII, so byte offsets hold).
func ppssppBreakMarkers(text, sep string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(text); {
		hit := 0
		for _, m := range ppssppScriptMarkers {
			if len(text)-i >= len(m) && strings.EqualFold(text[i:i+len(m)], m) {
				hit = len(m)
				break
			}
		}
		if hit == 0 {
			i++
			continue
		}
		b.WriteString(text[last : i+1])
		b.WriteString(sep)
		last = i + 1
		i += hit
	}
	b.WriteString(text[last:])
	return b.String()
}

// ppssppScriptValue is shellQuote for a value the script assigns, with every
// marker split by an empty quoted string: 'Retro”Arch' is the same word to the
// shell. A firmware launcher, a pinned launcher or a tag that happens to name
// RetroArch must not make JukaHub configure RetroArch for a PSP game. A marker
// never holds a quote or a backslash, so it always sits inside quotes.
func ppssppScriptValue(s string) string {
	return ppssppBreakMarkers(shellQuote(s), "''")
}

// ppssppFallbackRunsWithSh mirrors gameLaunchCommandArgs: a .sh launcher, or one
// without an extension, is a script for sh; anything else is run directly.
func ppssppFallbackRunsWithSh(launcher string) bool {
	switch strings.ToLower(filepath.Ext(launcher)) {
	case ".sh", "":
		return true
	}
	return false
}

// ppssppLauncherScript writes the launcher for one build (POSIX sh, LF). Every
// path is single-quoted (ppssppScriptValue). It takes the arguments a game launcher
// gets (gameLaunchCommandArgs: the ROM, then the game's own options) and hands
// all of them on, to PPSSPP or to the firmware's launcher. The script:
//
//   - goes straight to the firmware's launcher when this build already failed
//     to start, its binary is gone, or there is no firmware memstick to share;
//   - takes down a bind a killed run left behind, then bind-mounts the
//     firmware's SAVEDATA and PPSSPP_STATE over the build's memstick — a bind
//     that fails hands the game to the firmware's launcher rather than letting
//     PPSSPP start on an empty memstick;
//   - runs PPSSPP with its own HOME, its bundled libraries first, SDL's
//     controller database when the package has one, and --pause-menu-exit so
//     Exit in its pause menu comes back here instead of its own menu. These
//     are set on that one command only: the firmware's launcher, when the game
//     falls back to it, must see the environment JukaHub gave this script, or
//     it would open this build's memstick instead of its own saves;
//   - always takes the binds down again (traps for HUP, INT and TERM; JukaHub
//     stops a game with SIGINT to its process group);
//   - marks the build bad and falls back when PPSSPP exits with an error within
//     ppssppQuickExit seconds — not when it was stopped by a signal.
func ppssppLauncherScript(s ppssppScriptSpec) string {
	label := "PPSSPP"
	if s.Version != "" {
		label += " " + s.Version
	}
	libs := append(append([]string(nil), s.Libs...), "/usr/trimui/lib")
	fallbackSh := "0"
	if ppssppFallbackRunsWithSh(s.Fallback) {
		fallbackSh = "1"
	}
	var b strings.Builder
	line := func(parts ...string) {
		for _, p := range parts {
			b.WriteString(p)
		}
		b.WriteByte('\n')
	}
	line("#!/bin/sh")
	line("# ", ppssppScriptName, ": written by JukaHub for ", ppssppScriptComment(label), " (minui-psp ", ppssppScriptComment(s.Tag), ").")
	line("# PSP games that JukaHub starts with its own PPSSPP run through this script.")
	line("# JukaHub writes it again on every install and switch, so edits do not last.")
	line("# It shares the firmware's PSP saves (bind mounts over this build's memstick)")
	line("# and hands the game to the firmware's launcher when this build cannot start.")
	line()
	line(`ROM="$1"`)
	line("BUILD=", ppssppScriptValue(s.Build))
	line("PPSSPP_BIN=", ppssppScriptValue(s.Binary))
	line("PPSSPP_HOME=", ppssppScriptValue(s.Home))
	line("PPSSPP_WORK=", ppssppScriptValue(s.Work))
	line("PPSSPP_LIBS=", ppssppScriptValue(strings.Join(libs, ":")))
	line("PPSSPP_PADS=", ppssppScriptValue(s.ControllerDB))
	line("FIRMWARE_STICK=", ppssppScriptValue(s.Stick))
	line("FALLBACK=", ppssppScriptValue(s.Fallback))
	line("FALLBACK_SH=", fallbackSh)
	line("VERSION=", ppssppScriptValue(s.Version))
	line(`STATUS="$BUILD/`, ppssppStatusName, `"`)
	line(`BAD="$BUILD/`, ppssppBadName, `"`)
	line(`STICK="$PPSSPP_HOME/.config/ppsspp/PSP"`)
	line("SAVE_BOUND=0")
	line("STATE_BOUND=0")
	line()
	line("note() {")
	line(`	printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S' 2>/dev/null)" "$*" >>"$STATUS" 2>/dev/null`)
	line("}")
	line()
	line("unbind() {")
	line(`	if [ "$STATE_BOUND" = 1 ]; then`)
	line(`		umount "$STICK/PPSSPP_STATE" 2>/dev/null || umount -l "$STICK/PPSSPP_STATE" 2>/dev/null`)
	line("		STATE_BOUND=0")
	line("	fi")
	line(`	if [ "$SAVE_BOUND" = 1 ]; then`)
	line(`		umount "$STICK/SAVEDATA" 2>/dev/null || umount -l "$STICK/SAVEDATA" 2>/dev/null`)
	line("		SAVE_BOUND=0")
	line("	fi")
	line("}")
	line()
	line("bind() {")
	line(`	mount --bind "$1" "$2" 2>/dev/null || mount -o bind "$1" "$2" 2>/dev/null`)
	line("}")
	line()
	line("# firmware REASON [ARGS...] hands the game (ARGS: the ROM and its options)")
	line("# to the firmware's launcher, the way JukaHub would start it without this one.")
	line("firmware() {")
	line("	unbind")
	line(`	note "firmware PPSSPP: $1"`)
	line("	shift")
	line(`	if [ -n "$FALLBACK" ] && [ -f "$FALLBACK" ]; then`)
	line(`		cd "$(dirname "$FALLBACK")" 2>/dev/null`)
	line(`		if [ "$FALLBACK_SH" = 1 ]; then`)
	line(`			exec /bin/sh "$FALLBACK" "$@"`)
	line("		fi")
	line(`		exec "$FALLBACK" "$@"`)
	line("	fi")
	line(`	note "no firmware PSP launcher to fall back to"`)
	line("	exit 1")
	line("}")
	line()
	line("trap 'unbind' EXIT")
	line("trap 'unbind; exit 129' HUP")
	line("trap 'unbind; exit 130' INT")
	line("trap 'unbind; exit 143' TERM")
	line()
	line(`[ -f "$BAD" ] && firmware "this build did not start last time" "$@"`)
	line(`[ -f "$PPSSPP_BIN" ] || firmware "the PPSSPP binary is missing" "$@"`)
	line(`mkdir -p "$STICK/SAVEDATA" "$STICK/PPSSPP_STATE" 2>/dev/null`)
	line("# A bind that a killed run left behind comes down first, so nothing stacks.")
	line("n=0")
	line(`while [ "$n" -lt 4 ] && umount "$STICK/PPSSPP_STATE" 2>/dev/null; do n=$((n + 1)); done`)
	line("n=0")
	line(`while [ "$n" -lt 4 ] && umount "$STICK/SAVEDATA" 2>/dev/null; do n=$((n + 1)); done`)
	line(`if [ -n "$FIRMWARE_STICK" ]; then`)
	line(`	mkdir -p "$FIRMWARE_STICK/SAVEDATA" "$FIRMWARE_STICK/PPSSPP_STATE" 2>/dev/null`)
	line("	# Marked before the bind, so a signal during it still takes it down.")
	line("	SAVE_BOUND=1")
	line(`	bind "$FIRMWARE_STICK/SAVEDATA" "$STICK/SAVEDATA" || firmware "could not share the firmware's PSP saves (bind mount failed)" "$@"`)
	line("	STATE_BOUND=1")
	line(`	bind "$FIRMWARE_STICK/PPSSPP_STATE" "$STICK/PPSSPP_STATE" || firmware "could not share the firmware's PSP save states (bind mount failed)" "$@"`)
	line(`elif [ -n "$FALLBACK" ] && [ -f "$FALLBACK" ]; then`)
	line(`	firmware "the firmware's PSP save folder was not found" "$@"`)
	line("else")
	line("	# No firmware PPSSPP on this card at all: nothing to share, and nothing")
	line("	# to fall back to. This build keeps the saves in its own memstick.")
	line(`	note "no firmware PPSSPP on this card: saves stay in JukaHub's PPSSPP"`)
	line("fi")
	line()
	line(`PADS="${SDL_GAMECONTROLLERCONFIG_FILE:-}"`)
	line(`if [ -n "$PPSSPP_PADS" ] && [ -f "$PPSSPP_PADS" ]; then`)
	line(`	PADS="$PPSSPP_PADS"`)
	line("fi")
	line(`LIBS="$PPSSPP_LIBS${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"`)
	line(`cd "$PPSSPP_WORK" || firmware "cannot enter the PPSSPP folder" "$@"`)
	line()
	line(`note "start PPSSPP $VERSION: $ROM"`)
	line("START=$(date +%s 2>/dev/null)")
	line(`case "$START" in ''|*[!0-9]*) START=0 ;; esac`)
	line(`if [ "$#" -gt 0 ]; then`)
	line(`	HOME="$PPSSPP_HOME" XDG_CONFIG_HOME="$PPSSPP_HOME/.config" LD_LIBRARY_PATH="$LIBS" SDL_GAMECONTROLLERCONFIG_FILE="$PADS" "$PPSSPP_BIN" --pause-menu-exit "$@"`)
	line("else")
	line(`	HOME="$PPSSPP_HOME" XDG_CONFIG_HOME="$PPSSPP_HOME/.config" LD_LIBRARY_PATH="$LIBS" SDL_GAMECONTROLLERCONFIG_FILE="$PADS" "$PPSSPP_BIN"`)
	line("fi")
	line("RC=$?")
	line("END=$(date +%s 2>/dev/null)")
	line(`case "$END" in ''|*[!0-9]*) END=0 ;; esac`)
	line("unbind")
	line("ELAPSED=$((END - START))")
	line(`note "PPSSPP exited with status $RC after ${ELAPSED}s"`)
	line(`case "$RC" in`)
	line(`	0|129|130|137|143) exit "$RC" ;;`)
	line("esac")
	line(`if [ "$START" -gt 0 ] && [ "$ELAPSED" -lt `, strconv.Itoa(ppssppQuickExit), ` ]; then`)
	line(`	printf 'PPSSPP %s exited with status %s after %ss\n' "$VERSION" "$RC" "$ELAPSED" >"$BAD" 2>/dev/null`)
	line(`	firmware "PPSSPP $VERSION exited with status $RC after ${ELAPSED}s" "$@"`)
	line("fi")
	line(`exit "$RC"`)
	return b.String()
}

// ppssppScriptSpecFor is the spec for one build with the firmware as it is now.
func ppssppScriptSpecFor(b ppssppBuild, fw ppssppFirmwareInfo) ppssppScriptSpec {
	libs := make([]string, 0, len(b.Layout.Libs))
	for _, l := range b.Layout.Libs {
		libs = append(libs, b.path(l))
	}
	return ppssppScriptSpec{
		Build:        b.Dir,
		Binary:       b.binary(),
		Work:         b.path(b.Layout.Work),
		Home:         b.home(),
		Libs:         libs,
		ControllerDB: b.path(b.Layout.ControllerDB),
		Stick:        fw.Stick,
		Fallback:     fw.Launcher,
		Version:      b.Version,
		Tag:          b.Tag,
	}
}

// ppssppWriteScript writes a build's launcher. A "firmware" memstick inside
// JukaHub's own folder, or one that holds the shared memstick, would make the
// bind mount onto itself; that is refused, and so is a "firmware" launcher that
// is one of JukaHub's own scripts.
func ppssppWriteScript(b ppssppBuild, fw ppssppFirmwareInfo) error {
	fw = ppssppSanitizeFirmware(fw)
	if fw.Stick == "" && fw.Launcher == "" {
		logInfo("ppsspp", "no firmware PPSSPP found: %s keeps its saves in %s", b.label(), b.memstick())
	}
	text := ppssppLauncherScript(ppssppScriptSpecFor(b, fw))
	target := b.script()
	tmp := target + ".tmp"
	if err := writeFileSynced(tmp, []byte(text), 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	ppssppTrimStatus(filepath.Join(b.Dir, ppssppStatusName))
	return nil
}

// ppssppTrimStatus keeps the launcher's status log to its last part.
func ppssppTrimStatus(file string) {
	info, err := os.Stat(file)
	if err != nil || info.Size() <= ppssppStatusMax {
		return
	}
	data, err := os.ReadFile(file)
	if err != nil || len(data) <= ppssppStatusMax {
		return
	}
	tail := data[len(data)-ppssppStatusMax/4:]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	_ = os.WriteFile(file, tail, 0o644)
}

// ── game-launchers.json ──────────────────────────────────────────────────────

// ppssppPSPKey reports whether a game-launchers.json key names the PSP.
func ppssppPSPKey(key string) bool {
	return strings.EqualFold(strings.TrimSpace(key), ppssppLibrarySystem)
}

// ppssppIsManagedLauncher reports whether a launcher path is one of the
// manager's own scripts.
func ppssppIsManagedLauncher(p string) bool {
	p = strings.TrimSpace(p)
	root := ppssppManagedRootFn()
	if p == "" || root == "" {
		return false
	}
	return strings.EqualFold(filepath.Base(p), ppssppScriptName) && isPathWithin(p, root)
}

// ppssppJSONString is a raw JSON value as a string ("" when it is not one).
func ppssppJSONString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// ppssppReadOverrides reads the launcher file as raw values, so entries JukaHub
// does not own survive a rewrite unchanged. A missing file is an empty map; a
// file that is not a JSON object is an error, because rewriting it would
// destroy whatever its author meant.
func ppssppReadOverrides(file string) (map[string]json.RawMessage, bool, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, false, nil
		}
		return nil, false, err
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))
	m := map[string]json.RawMessage{}
	if len(trimmed) == 0 {
		return m, true, nil
	}
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, true, fmt.Errorf("%s is not a JSON object (%v) — fix or remove it first", filepath.Base(file), err)
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, true, nil
}

// ppssppWriteOverrides writes the launcher file with its entries in the order
// the file on the card has them (json.Marshal would sort a hand-ordered file),
// the PSP entry where the file's PSP entry was, anything new at the end, and
// every value as it was written.
func ppssppWriteOverrides(file string, m map[string]json.RawMessage) error {
	var old []string
	if raw, err := os.ReadFile(file); err == nil {
		old = ppssppObjectKeys(bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))))
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range ppssppOrderedKeys(old, m) {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(k)
		if err != nil {
			return err
		}
		buf.WriteString("\n  ")
		buf.Write(key)
		buf.WriteString(": ")
		if err := json.Indent(&buf, m[k], "  ", "  "); err != nil {
			return err
		}
	}
	if len(m) > 0 {
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")
	return AtomicWrite(file, buf.Bytes(), 0o644)
}

// ppssppObjectKeys lists the top-level keys of a JSON object in file order
// (as far as it reads; nil when data is not an object).
func ppssppObjectKeys(data []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		key, ok := tok.(string)
		if !ok {
			return keys
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return keys
		}
	}
	return keys
}

// ppssppOrderedKeys is the order to write m's keys in: old's order, with m's
// PSP entries in the place of the first PSP entry m no longer has, and the keys
// old does not name after the rest, sorted.
func ppssppOrderedKeys(old []string, m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	done := make(map[string]bool, len(m))
	emit := func(k string) {
		if _, ok := m[k]; ok && !done[k] {
			done[k] = true
			out = append(out, k)
		}
	}
	for _, k := range old {
		if _, ok := m[k]; !ok && ppssppPSPKey(k) {
			for _, p := range ppssppSortedPSPKeys(m) {
				emit(p)
			}
			continue
		}
		emit(k)
	}
	rest := make([]string, 0, len(m)-len(out))
	for k := range m {
		if !done[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// ppssppSortedPSPKeys lists the keys of m that name the PSP, in order.
func ppssppSortedPSPKeys(m map[string]json.RawMessage) []string {
	var keys []string
	for k := range m {
		if ppssppPSPKey(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// ppssppPointPSPLocked makes launcher the PSP's entry in game-launchers.json.
// The PSP entries it replaces are remembered the first time — and again when
// someone pinned another launcher by hand after JukaHub's entry, because that
// is then the value to come back to. JukaHub's own older entries are never
// remembered.
func ppssppPointPSPLocked(st *ppssppState, launcher string) error {
	file := ppssppOverridesPathFn()
	if file == "" {
		return errors.New("JukaHub has no data folder for game-launchers.json")
	}
	m, existed, err := ppssppReadOverrides(file)
	if err != nil {
		return err
	}
	foreign := map[string]string{}
	for _, k := range ppssppSortedPSPKeys(m) {
		if !ppssppIsManagedLauncher(ppssppJSONString(m[k])) {
			foreign[k] = string(m[k])
		}
		delete(m, k)
	}
	if !st.Override {
		st.Replaced = foreign
		st.FileExisted = existed
	} else if len(foreign) > 0 {
		st.Replaced = foreign
	}
	if len(st.Replaced) == 0 {
		st.Replaced = nil
	}
	quoted, err := json.Marshal(launcher)
	if err != nil {
		return err
	}
	m[ppssppLibrarySystem] = quoted
	if err := ppssppWriteOverrides(file, m); err != nil {
		return err
	}
	st.Override = true
	return nil
}

// ppssppRestorePSPLocked takes JukaHub's PSP entry out of game-launchers.json
// and puts back exactly what it replaced. A PSP entry someone wrote by hand
// since is theirs and stays. The file goes away again when JukaHub created it
// and nothing else is left in it.
func ppssppRestorePSPLocked(st *ppssppState) error {
	file := ppssppOverridesPathFn()
	if file == "" {
		return errors.New("JukaHub has no data folder for game-launchers.json")
	}
	m, existed, err := ppssppReadOverrides(file)
	if err != nil {
		return err
	}
	foreign := false
	var ours []string
	for _, k := range ppssppSortedPSPKeys(m) {
		if ppssppIsManagedLauncher(ppssppJSONString(m[k])) {
			ours = append(ours, k)
		} else {
			foreign = true
		}
	}
	for _, k := range ours {
		delete(m, k)
	}
	restored := false
	if !foreign {
		for k, v := range st.Replaced {
			m[k] = json.RawMessage(v)
			restored = true
		}
	}
	switch {
	case st.Override && existed && len(m) == 0 && !st.FileExisted:
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = os.Remove(file + ".bak")
	case len(ours) > 0 || restored:
		if err := ppssppWriteOverrides(file, m); err != nil {
			return err
		}
	}
	st.Override = false
	st.Replaced = nil
	st.FileExisted = false
	return nil
}

// ppssppPinnedLauncherLocked is the launcher the user pinned for PSP games
// themselves, if any: one written by hand in the file now, else what JukaHub's
// entry replaced.
func ppssppPinnedLauncherLocked(st *ppssppState) string {
	if m, _, err := ppssppReadOverrides(ppssppOverridesPathFn()); err == nil {
		for _, k := range ppssppSortedPSPKeys(m) {
			if p := ppssppJSONString(m[k]); p != "" && !ppssppIsManagedLauncher(p) {
				return p
			}
		}
	}
	if st.Override {
		keys := make([]string, 0, len(st.Replaced))
		for k := range st.Replaced {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if p := ppssppJSONString(json.RawMessage(st.Replaced[k])); p != "" {
				return p
			}
		}
	}
	return ""
}

// ppssppUserPinnedLocked reports whether the file holds a PSP launcher someone
// wrote by hand right now. An automatic switch leaves such a pin alone.
func ppssppUserPinnedLocked() bool {
	m, _, err := ppssppReadOverrides(ppssppOverridesPathFn())
	if err != nil {
		// A file JukaHub cannot read is not one it may rewrite either.
		return true
	}
	for _, k := range ppssppSortedPSPKeys(m) {
		if p := ppssppJSONString(m[k]); p != "" && !ppssppIsManagedLauncher(p) {
			return true
		}
	}
	return false
}

// ── The firmware's PPSSPP ────────────────────────────────────────────────────

// ppssppPSPishName reports a folder name that could be the PSP's launcher
// folder for any of findGameLauncher's passes.
func ppssppPSPishName(name string) bool {
	n := normaliseLauncherName(name)
	return strings.Contains(n, "psp") || strings.Contains(n, "ppsspp") || strings.Contains(n, "playstationportable")
}

// ppssppFirmwareLauncherIn is the launcher the catalog would give PSP games:
// the same folder scan and the same matching (findGameLauncher), limited to the
// folders whose name could match.
func ppssppFirmwareLauncherIn(emuRoots []string) string {
	var entries []emuEntry
	for _, root := range emuRoots {
		dirEntries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, de := range dirEntries {
			if !de.IsDir() || skipScanEntryName(de.Name()) || !ppssppPSPishName(de.Name()) {
				continue
			}
			folder := filepath.Join(root, de.Name())
			if launcher := emuFolderLauncher(folder, emuFolderConfig(folder)); launcher != "" {
				entries = append(entries, emuEntry{Name: de.Name(), Status: emuAvailable, Launch: launcher})
			}
		}
	}
	return findGameLauncher(ppssppLibrarySystem, entries)
}

// ppssppFirmwareBinary is the PPSSPP executable beside a launcher: the largest
// ELF file whose name starts with "ppsspp", in the launcher's folder or one
// PPSSPP* folder below it.
func ppssppFirmwareBinary(dir string) string {
	best, bestSize := "", int64(0)
	consider := func(p string, info fs.FileInfo) {
		if info.Mode().IsRegular() && info.Size() > bestSize && !ppssppSharedObject(info.Name()) && ppssppIsELF(p) {
			best, bestSize = p, info.Size()
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if skipScanEntryName(e.Name()) || !strings.HasPrefix(strings.ToLower(e.Name()), "ppsspp") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				consider(p, info)
			}
			continue
		}
		inner, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, f := range inner {
			if f.IsDir() || !strings.HasPrefix(strings.ToLower(f.Name()), "ppsspp") {
				continue
			}
			if info, err := f.Info(); err == nil {
				consider(filepath.Join(p, f.Name()), info)
			}
		}
	}
	return best
}

// ppssppLauncherVersion reads the version of the PPSSPP a launcher starts: from
// its own name (CrossMix's ppsspp_1.17.1_gl.sh), from a PPSSPP_<version> folder
// the script names, else from the binary beside it, whose scan is cached in
// scan by size and modification time.
func ppssppLauncherVersion(launcher string, scan *ppssppBinaryScan) string {
	if v := ppssppVersionFromName(filepath.Base(launcher)); v != "" {
		return v
	}
	if f, err := os.Open(launcher); err == nil {
		head, _ := io.ReadAll(io.LimitReader(f, 64<<10))
		_ = f.Close()
		best := ""
		for _, m := range ppssppNameVersionRe().FindAllStringSubmatch(string(head), -1) {
			if best == "" || compareCoreVersions(m[1], best) > 0 {
				best = m[1]
			}
		}
		if best != "" {
			return best
		}
	}
	bin := ppssppFirmwareBinary(filepath.Dir(launcher))
	if bin == "" {
		return ""
	}
	info, err := os.Stat(bin)
	if err != nil {
		return ""
	}
	if scan != nil && scan.Path == bin && scan.Size == info.Size() && scan.ModTime == info.ModTime().Unix() {
		return scan.Version
	}
	version := ppssppBinaryVersion(bin)
	if scan != nil {
		*scan = ppssppBinaryScan{Path: bin, Size: info.Size(), ModTime: info.ModTime().Unix(), Version: version}
	}
	return version
}

// ppssppFirmwareStick is the memstick the firmware's PPSSPP keeps its saves in,
// the one JukaHub's build shares:
//
//  1. the memstick of the build the launcher names (CrossMix's
//     ppsspp_1.17.1_gl.sh plays from Emus/PSP/PPSSPP_1.17.1), because that is
//     the PPSSPP the fallback starts — even before it has run once;
//  2. else, of the memsticks that hold saves, the one whose ini is newest: the
//     build actually played (PPSSPP writes its ini when it exits);
//  3. else the newest ini at all (the stock package ships one);
//  4. else the first memstick with saves, else none.
func ppssppFirmwareStick(sdRoot, launcher string) string {
	if strings.TrimSpace(sdRoot) == "" {
		return ""
	}
	version := ""
	if launcher != "" {
		version = ppssppVersionFromName(filepath.Base(launcher))
	}
	memsticks := ppssppEmuMemsticks([]string{sdRoot})
	if version != "" {
		named := ""
		for _, m := range memsticks {
			if !strings.EqualFold(filepath.Dir(filepath.Clean(m.Home)), filepath.Dir(filepath.Clean(launcher))) ||
				ppssppVersionFromName(filepath.Base(m.Home)) != version {
				continue
			}
			if dirExists(m.Dir) {
				return m.Dir
			}
			if named == "" {
				named = m.Dir
			}
		}
		if named != "" {
			return named
		}
	}
	dirs := make([]string, 0, len(memsticks)+3)
	for _, m := range memsticks {
		dirs = append(dirs, m.Dir)
	}
	for _, rel := range []string{"PSP", filepath.Join("ppsspp", "PSP"), filepath.Join("Apps", "PPSSPP", "PSP")} {
		dirs = append(dirs, filepath.Join(sdRoot, rel))
	}
	type cand struct {
		dir   string
		saves bool
		ini   time.Time
	}
	var cands []cand
	seen := map[string]bool{}
	for _, d := range dirs {
		key := strings.ToLower(filepath.Clean(d))
		if seen[key] || !dirExists(d) {
			continue
		}
		seen[key] = true
		c := cand{dir: d}
		c.saves = dirExists(filepath.Join(d, "SAVEDATA")) || dirExists(filepath.Join(d, "PPSSPP_STATE"))
		if info, err := os.Stat(ppssppMemstickIni(d)); err == nil && info.Mode().IsRegular() {
			c.ini = info.ModTime()
		}
		cands = append(cands, c)
	}
	pick := func(withSaves bool) string {
		best := ""
		var bestTime time.Time
		for _, c := range cands {
			if (withSaves && !c.saves) || c.ini.IsZero() {
				continue
			}
			if best == "" || c.ini.After(bestTime) {
				best, bestTime = c.dir, c.ini
			}
		}
		return best
	}
	if d := pick(true); d != "" {
		return d
	}
	if d := pick(false); d != "" {
		return d
	}
	for _, c := range cands {
		if c.saves {
			return c.dir
		}
	}
	return ""
}

// ppssppDetectFirmware works out the firmware's PPSSPP. pinned is a launcher
// the user pinned for PSP games themselves, which wins over the catalog's.
func ppssppDetectFirmware(sdRoot string, emuRoots []string, pinned string, scan *ppssppBinaryScan) ppssppFirmwareInfo {
	info := ppssppFirmwareInfo{At: time.Now()}
	if pinned != "" && regularFile(pinned) && !ppssppIsManagedLauncher(pinned) {
		info.Launcher = pinned
	} else {
		info.Launcher = ppssppFirmwareLauncherIn(emuRoots)
	}
	if info.Launcher != "" {
		info.Version = ppssppLauncherVersion(info.Launcher, scan)
	}
	info.Stick = ppssppFirmwareStick(sdRoot, info.Launcher)
	return info
}

// ppssppDetectFirmwareNow runs a detection (card work: call it off the render
// thread), caches the binary scan in the state and publishes the result.
func ppssppDetectFirmwareNow() ppssppFirmwareInfo {
	ppssppMgr.mu.Lock()
	st := ppssppLockedState()
	pinned := ppssppPinnedLauncherLocked(st)
	scan := st.FirmwareScan
	ppssppMgr.mu.Unlock()

	info := ppssppDetectFirmware(ppssppCardRootFn(), ppssppEmuRootsFn(), pinned, &scan)

	ppssppMgr.mu.Lock()
	if st := ppssppLockedState(); st.FirmwareScan != scan {
		st.FirmwareScan = scan
		if err := ppssppSaveLocked(); err != nil {
			logDebug("ppsspp", "could not save the firmware scan: %v", err)
		}
	}
	ppssppMgr.mu.Unlock()
	ppssppFirmwareSnap.Store(&info)
	return info
}

// ppssppFirmwareCached is the last detection, if any.
func ppssppFirmwareCached() (ppssppFirmwareInfo, bool) {
	if p := ppssppFirmwareSnap.Load(); p != nil {
		return *p, true
	}
	return ppssppFirmwareInfo{}, false
}

// ppssppRequestFirmwareRefresh starts a detection in the background when the
// cached one is missing or old; the page's catalog is rebuilt when it lands.
func ppssppRequestFirmwareRefresh() {
	if p := ppssppFirmwareSnap.Load(); p != nil && time.Since(p.At) < ppssppFirmwareTTL {
		return
	}
	if !ppssppFirmwareBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer ppssppFirmwareBusy.Store(false)
		ppssppDetectFirmwareNow()
		invalidateEmulatorCache()
	}()
}

// ── Installing ───────────────────────────────────────────────────────────────

// ppssppCopyFile copies a small file (at most limit bytes).
func ppssppCopyFile(src, dst string, limit int64) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	_ = f.Close()
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("%s is larger than %d bytes", src, limit)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// ppssppSeedHome prepares the shared HOME for a build: the memstick's SAVEDATA
// and PPSSPP_STATE (the mount points the launcher binds over) and, while the
// memstick has no ppsspp.ini yet (a first install; PPSSPP writes one when it
// exits), its settings: the config the package ships (minui-psp tunes it for
// these handhelds), then from the firmware's PPSSPP what cannot break this
// build, where the package brings none — its controls.ini (the TrimUI-tuned
// button mapping lives there) and its per-game files (<GAMEID>_ppsspp.ini,
// <GAMEID>_controls.ini; CrossMix ships tuned ones for many games). Never the
// firmware's ppsspp.ini: it may choose a GPU backend this build does not have,
// which is why JukaHub's builds have a HOME of their own at all. After that the
// user's settings stay as they are, because every later build reads this HOME.
func ppssppSeedHome(b ppssppBuild, fwStick string) {
	stick := b.memstick()
	if stick == "" {
		return
	}
	sys := filepath.Join(stick, "SYSTEM")
	for _, d := range []string{sys, filepath.Join(stick, "SAVEDATA"), filepath.Join(stick, "PPSSPP_STATE")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			logWarn("ppsspp", "could not create %s: %v", d, err)
		}
	}
	if regularFile(filepath.Join(sys, "ppsspp.ini")) {
		return
	}
	copyFrom := func(from string, wanted func(lower string) bool) {
		entries, err := os.ReadDir(from)
		if err != nil {
			return
		}
		for _, e := range entries {
			lower := strings.ToLower(e.Name())
			if e.IsDir() || !wanted(lower) || regularFile(filepath.Join(sys, e.Name())) {
				continue
			}
			if err := ppssppCopyFile(filepath.Join(from, e.Name()), filepath.Join(sys, e.Name()), 1<<20); err != nil {
				logWarn("ppsspp", "could not copy %s into JukaHub's PPSSPP settings: %v", e.Name(), err)
			}
		}
	}
	if shipped := b.shippedSystem(); shipped != "" {
		copyFrom(shipped, func(lower string) bool { return strings.HasSuffix(lower, ".ini") })
	}
	if fwStick != "" {
		copyFrom(filepath.Join(fwStick, "SYSTEM"), func(lower string) bool {
			return lower == "controls.ini" || strings.HasSuffix(lower, "_ppsspp.ini") || strings.HasSuffix(lower, "_controls.ini")
		})
	}
}

// ppssppWriteManifest records where a build came from, beside it.
func ppssppWriteManifest(b ppssppBuild) {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(b.Dir, ppssppManifestName), append(data, '\n'), 0o644); err != nil {
		logWarn("ppsspp", "could not record the build: %v", err)
	}
}

// ppssppInstall downloads, verifies and unpacks one release into its own build
// folder, writes its launcher and records it. It does not switch PSP games to
// it: the caller decides that. The last point a cancel leaves the card as it was
// is just before the staging folder is renamed into place.
func ppssppInstall(ctx context.Context, client *http.Client, rel ppssppRelease, platform string, fw ppssppFirmwareInfo, progress emuWinProgress) (ppssppBuild, error) {
	report := func(phase string, done, total int64) {
		if progress != nil {
			progress(phase, done, total)
		}
	}
	root := ppssppManagedRootFn()
	switch {
	case root == "":
		return ppssppBuild{}, errors.New("JukaHub has no data folder to install PPSSPP into")
	case platform == "":
		return ppssppBuild{}, errors.New("no PPSSPP build is published for this model")
	case !ppssppDigestPattern().MatchString(rel.Digest):
		return ppssppBuild{}, fmt.Errorf("%s comes without a SHA-256 digest and is not installed", ppssppPakAsset)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return ppssppBuild{}, err
	}
	need := 3 * rel.Size
	if need <= 0 {
		need = 192 << 20
	}
	if free, err := freeBytes(root); err == nil && free > 0 && free < need {
		return ppssppBuild{}, fmt.Errorf("the card needs about %s free for PPSSPP and has %s", formatInstallBytes(need), formatInstallBytes(free))
	}

	builds := ppssppBuildsDir()
	if err := os.MkdirAll(builds, 0o755); err != nil {
		return ppssppBuild{}, err
	}
	dirName := ppssppTagDir(rel.Tag)
	staging := filepath.Join(builds, ".staging-"+dirName)
	if err := ppssppSafeRemove(staging); err != nil {
		return ppssppBuild{}, err
	}
	defer func() {
		if err := ppssppSafeRemove(staging); err != nil {
			logWarn("ppsspp", "left %s behind: %v", staging, err)
		}
	}()
	archive := filepath.Join(root, ".download", ppssppPakAsset)
	defer os.Remove(archive)

	report(installPhaseDownload, 0, rel.Size)
	sum, err := emuDownload(ctx, client, rel.URL, archive, ppssppMaxDownload, func(done, total int64) {
		report(installPhaseDownload, done, total)
	})
	if err != nil {
		return ppssppBuild{}, err
	}
	report(installPhaseVerify, 0, 0)
	if err := emuDigestMatches(sum, rel.Digest); err != nil {
		return ppssppBuild{}, err
	}
	report(installPhaseExtract, 0, 0)
	unpacked, err := ppssppInspectZip(archive)
	if err != nil {
		return ppssppBuild{}, err
	}
	if free, err := freeBytes(root); err == nil && free > 0 && free < unpacked+(32<<20) {
		return ppssppBuild{}, fmt.Errorf("PPSSPP unpacks to %s and the card has %s free", formatInstallBytes(unpacked), formatInstallBytes(free))
	}
	if err := emuExtractZip(ctx, archive, staging, func(done, total int64) {
		report(installPhaseExtract, done, total)
	}); err != nil {
		return ppssppBuild{}, err
	}
	_ = os.Remove(archive)

	layout, err := ppssppDiscoverLayout(staging, platform)
	if err != nil {
		return ppssppBuild{}, fmt.Errorf("minui-psp %s is %w: %v", rel.Tag, errPPSSPPUnusable, err)
	}
	build := ppssppBuild{
		Tag:         rel.Tag,
		Platform:    platform,
		Dir:         staging,
		Layout:      layout,
		Asset:       rel.Asset,
		URL:         rel.URL,
		Digest:      rel.Digest,
		SHA256:      sum,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}
	// FAT ignores the bit; a card formatted ext4 needs it.
	_ = os.Chmod(build.binary(), 0o755)
	libDirs := make([]string, 0, len(layout.Libs))
	for _, l := range layout.Libs {
		libDirs = append(libDirs, build.path(l))
	}
	if problem := nativeLoadProblemWith(build.binary(), libDirs); problem != "" {
		return ppssppBuild{}, fmt.Errorf("minui-psp %s is %w: its PPSSPP %s", rel.Tag, errPPSSPPUnusable, problem)
	}
	build.Version = ppssppBinaryVersion(build.binary())
	if build.Version == "" {
		build.Version = rel.Version
	}
	ppssppSeedHome(build, fw.Stick)

	if err := ctx.Err(); err != nil {
		return ppssppBuild{}, err
	}
	final := filepath.Join(builds, dirName)
	aside := ""
	if _, err := os.Lstat(final); err == nil {
		aside = filepath.Join(builds, ".replaced-"+dirName+"-"+strconv.FormatInt(time.Now().Unix(), 10))
		if err := os.Rename(final, aside); err != nil {
			return ppssppBuild{}, fmt.Errorf("could not move the old %s build aside: %w", rel.Tag, err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		if aside != "" {
			_ = os.Rename(aside, final)
		}
		return ppssppBuild{}, err
	}
	build.Dir = final
	ppssppWriteManifest(build)
	if err := ppssppWriteScript(build, fw); err != nil {
		return ppssppBuild{}, fmt.Errorf("could not write the PPSSPP launcher: %w", err)
	}
	if aside != "" {
		if err := ppssppSafeRemove(aside); err != nil {
			logWarn("ppsspp", "kept the replaced build: %v", err)
		}
	}

	ppssppMgr.mu.Lock()
	st := ppssppLockedState()
	ppssppRecordBuildLocked(st, build)
	if st.Bad != nil {
		delete(st.Bad, build.Tag)
	}
	_ = os.Remove(filepath.Join(build.Dir, ppssppBadName))
	err = ppssppSaveLocked()
	ppssppMgr.mu.Unlock()
	if err != nil {
		logWarn("ppsspp", "could not save the PPSSPP state: %v", err)
	}
	logInfo("ppsspp", "installed %s (minui-psp %s, %s) in %s", build.label(), build.Tag, platform, build.Dir)
	return build, nil
}

// ppssppRecordBuildLocked puts b first in the state and removes every build
// that is neither b nor the one build kept before it: the build PSP games use
// now (it becomes the way back), else the one they used before, else the newest
// other. A build ppssppSafeRemove refuses to delete stays recorded.
func ppssppRecordBuildLocked(st *ppssppState, b ppssppBuild) {
	others := make([]ppssppBuild, 0, len(st.Builds))
	for _, old := range st.Builds {
		if old.Tag != b.Tag {
			others = append(others, old)
		}
	}
	keep := ""
	for _, want := range []string{st.Active, st.Previous} {
		if want == "" || want == b.Tag {
			continue
		}
		if _, ok := st.build(want); ok {
			keep = want
			break
		}
	}
	if keep == "" && len(others) > 0 {
		keep = others[0].Tag
	}
	builds := []ppssppBuild{b}
	for _, old := range others {
		if old.Tag == keep {
			builds = append(builds, old)
			continue
		}
		if err := ppssppSafeRemove(old.Dir); err != nil {
			logWarn("ppsspp", "kept old build %s: %v", old.Tag, err)
			builds = append(builds, old)
			continue
		}
		if st.Previous == old.Tag {
			st.Previous = ""
		}
		logInfo("ppsspp", "removed old build %s", old.Tag)
	}
	st.Builds = builds
}

// ── Switching ────────────────────────────────────────────────────────────────

// ppssppUseBuildLocked points PSP games at one installed build. Its launcher is
// written again first, with the firmware's launcher and memstick as they are
// now.
func ppssppUseBuildLocked(st *ppssppState, b ppssppBuild, fw ppssppFirmwareInfo) error {
	if err := ppssppWriteScript(b, fw); err != nil {
		return err
	}
	if err := ppssppPointPSPLocked(st, b.script()); err != nil {
		return err
	}
	if st.Active != b.Tag {
		st.Previous = st.Active
		st.Active = b.Tag
	}
	return nil
}

// ppssppUseFirmwareLocked gives PSP games back to the firmware's PPSSPP.
func ppssppUseFirmwareLocked(st *ppssppState) error {
	if err := ppssppRestorePSPLocked(st); err != nil {
		return err
	}
	if st.Active != "" {
		st.Previous = st.Active
		st.Active = ""
	}
	return nil
}

// ppssppSwitchToBuild is "Use JukaHub's PPSSPP": the newest installed build, or
// tag when given. A user's choice clears what the launcher reported about the
// build, so a build that failed once can be tried again on purpose.
func ppssppSwitchToBuild(tag, choice string, fw ppssppFirmwareInfo) (ppssppBuild, error) {
	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	st := ppssppLockedState()
	var b ppssppBuild
	var ok bool
	if tag != "" {
		b, ok = st.build(tag)
	} else {
		b, ok = st.newest()
	}
	if !ok || !regularFile(b.binary()) {
		return ppssppBuild{}, errors.New("JukaHub's PPSSPP is not installed — install it first")
	}
	if choice != ppssppChoiceAuto {
		if st.Bad != nil {
			delete(st.Bad, b.Tag)
		}
		_ = os.Remove(filepath.Join(b.Dir, ppssppBadName))
		st.Choice = choice
	}
	if err := ppssppUseBuildLocked(st, b, fw); err != nil {
		return ppssppBuild{}, err
	}
	return b, ppssppSaveLocked()
}

// ppssppSwitchToFirmware is "Use the firmware's PPSSPP".
func ppssppSwitchToFirmware(choice string) error {
	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	st := ppssppLockedState()
	if choice != ppssppChoiceAuto {
		st.Choice = choice
	}
	if err := ppssppUseFirmwareLocked(st); err != nil {
		return err
	}
	return ppssppSaveLocked()
}

// ppssppRemoveBuilds is "Remove JukaHub's PPSSPP": PSP games go back to the
// firmware's PPSSPP first — and the catalog forgets the tile that could still
// name a build's launcher — then every build folder is removed the safe way
// (ppssppSafeRemove); one it refuses stays recorded and is named in kept. The
// shared HOME (the user's PPSSPP settings) stays. The choice becomes the
// firmware's, so install mode does not bring the build back by itself.
func ppssppRemoveBuilds() (removed int, kept []string, err error) {
	ppssppMgr.mu.Lock()
	st := ppssppLockedState()
	if st.Active != "" {
		if err := ppssppUseFirmwareLocked(st); err != nil {
			ppssppMgr.mu.Unlock()
			return 0, nil, err
		}
	}
	st.Previous = ""
	st.Choice = ppssppChoiceFirmware
	err = ppssppSaveLocked()
	ppssppMgr.mu.Unlock()
	if err != nil {
		return 0, nil, err
	}
	invalidateEmulatorCache()

	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	st = ppssppLockedState()
	var left []ppssppBuild
	for _, b := range st.Builds {
		if err := ppssppSafeRemove(b.Dir); err != nil {
			logWarn("ppsspp", "kept build %s: %v", b.Tag, err)
			kept = append(kept, b.Tag)
			left = append(left, b)
			continue
		}
		removed++
	}
	st.Builds = left
	return removed, kept, ppssppSaveLocked()
}

// ppssppFirmwareLabel names the firmware's PPSSPP for a message.
func ppssppFirmwareLabel(fw ppssppFirmwareInfo) string {
	if fw.Version != "" {
		return "PPSSPP " + fw.Version
	}
	return "the firmware's PPSSPP"
}

// ppssppRevertBad handles a build whose launcher reported that it does not
// start: it is marked bad, and PSP games go back to the build used before it
// when that one is still fine (and no launcher was pinned by hand meanwhile,
// which is never overwritten), else to the firmware's PPSSPP. It returns the
// toast to show ("" when tag was no longer the active build).
func ppssppRevertBad(tag, reason string, fw ppssppFirmwareInfo) (string, error) {
	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	st := ppssppLockedState()
	if st.Active != tag || tag == "" {
		return "", nil
	}
	if st.Bad == nil {
		st.Bad = map[string]string{}
	}
	st.Bad[tag] = reason
	failed, _ := st.build(tag)
	failedLabel := failed.label()
	if failed.Tag == "" {
		failedLabel = "JukaHub's PPSSPP"
	}
	back := ppssppFirmwareLabel(fw)
	var err error
	if prev, ok := st.build(st.Previous); ok && prev.Tag != tag && prev.usable(*st) && !ppssppUserPinnedLocked() {
		err = ppssppUseBuildLocked(st, prev, fw)
		back = prev.label()
	} else {
		err = ppssppUseFirmwareLocked(st)
	}
	// Going back to the build that just failed is never the way back.
	st.Previous = ""
	if serr := ppssppSaveLocked(); serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		return "", err
	}
	logWarn("ppsspp", "%s did not start (%s); PSP games use %s again", failedLabel, reason, back)
	return failedLabel + " did not start on this device — PSP games use " + back + " again", nil
}

// ppssppReadFirstLine reads the first line of a small file.
func ppssppReadFirstLine(file string) (string, bool) {
	f, err := os.Open(file)
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 4<<10))
	text := strings.TrimSpace(string(data))
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	if text == "" {
		text = "it exited with an error"
	}
	return text, true
}

// ppssppLastStatusLine is the last line of a build's status log.
func ppssppLastStatusLine(b ppssppBuild) string {
	f, err := os.Open(filepath.Join(b.Dir, ppssppStatusName))
	if err != nil {
		return ""
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > 4<<10 {
		_, _ = f.Seek(info.Size()-4<<10, io.SeekStart)
	}
	data, _ := io.ReadAll(io.LimitReader(f, 4<<10))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// ppssppFallbackReason is why the last launch went to the firmware's PPSSPP,
// from a status line ("<date> <time> firmware PPSSPP: <reason>"), or "".
func ppssppFallbackReason(line string) string {
	const marker = "firmware PPSSPP: "
	if i := strings.Index(line, marker); i >= 0 {
		return strings.TrimSpace(line[i+len(marker):])
	}
	return ""
}

// ppssppReviewLastRun looks at what the launcher of the build PSP games use left
// behind: a build that did not start is switched away from, with one toast, and
// a game the script handed to the firmware's PPSSPP for another reason (a bind
// that failed) is said once per reason.
func ppssppReviewLastRun() {
	st := ppssppSnapshot()
	b, ok := st.build(st.Active)
	if !ok {
		return
	}
	if reason, bad := ppssppReadFirstLine(filepath.Join(b.Dir, ppssppBadName)); bad {
		// A switch that failed (an unreadable launcher file) is tried again
		// now and then, not at every wake.
		if last := ppssppRevertFailedAt.Load(); last != 0 && time.Since(time.Unix(0, last)) < 30*time.Minute {
			return
		}
		fw := ppssppDetectFirmwareNow()
		msg, err := ppssppRevertBad(b.Tag, reason, fw)
		if err != nil {
			ppssppRevertFailedAt.Store(time.Now().UnixNano())
			logWarn("ppsspp", "could not switch away from %s: %v", b.Tag, err)
			return
		}
		ppssppRevertFailedAt.Store(0)
		if msg != "" {
			invalidateEmulatorCache()
			ShowToast(msg, ToastKindWarning)
		}
		return
	}
	reason := ppssppFallbackReason(ppssppLastStatusLine(b))
	if reason == "" || reason == st.FallbackSeen {
		return
	}
	ppssppMgr.mu.Lock()
	ppssppLockedState().FallbackSeen = reason
	_ = ppssppSaveLocked()
	ppssppMgr.mu.Unlock()
	ShowToast("A PSP game opened in the firmware's PPSSPP: "+reason, ToastKindWarning)
}

// ppssppActiveIni is the ini JukaHub's PPSSPP reads while it plays PSP games,
// or "" while the firmware's does (ppsspp_calibration.go edits that one then).
func ppssppActiveIni() string {
	if !ppssppManagedEnabled() {
		return ""
	}
	st := ppssppSnapshot()
	b, ok := st.build(st.Active)
	if !ok {
		return ""
	}
	if ini := ppssppMemstickIni(b.memstick()); regularFile(ini) {
		return ini
	}
	return ""
}

// ── Automatic updates ────────────────────────────────────────────────────────

// ppssppAutoAction is what the daily check does with a release.
type ppssppAutoAction int

const (
	ppssppAutoNothing ppssppAutoAction = iota
	ppssppAutoNotify
	ppssppAutoInstall
)

// ppssppAutoDecide decides for one release. A release is worth having when a
// JukaHub build is installed and this is a new tag, or when it names a PPSSPP
// newer than the firmware's (or the firmware's version is unknown). Install
// mode installs only what it can judge: with nothing installed yet it needs
// both versions, and an untested model or a user who chose the firmware's
// PPSSPP is told rather than installed for.
func ppssppAutoDecide(mode coreUpdateMode, st ppssppState, rel ppssppRelease, fwVersion string, untested bool) ppssppAutoAction {
	if mode == coreUpdateOff || rel.Tag == "" || st.Bad[rel.Tag] != "" {
		return ppssppAutoNothing
	}
	newest, installed := st.newest()
	if installed && newest.Tag == rel.Tag {
		return ppssppAutoNothing
	}
	worth := installed || (rel.Version != "" && (fwVersion == "" || ppssppNewer(rel.Version, fwVersion)))
	if !worth {
		return ppssppAutoNothing
	}
	if mode == coreUpdateNotify || untested || st.Choice == ppssppChoiceFirmware {
		return ppssppAutoNotify
	}
	if !installed && (rel.Version == "" || fwVersion == "") {
		return ppssppAutoNotify
	}
	return ppssppAutoInstall
}

// ppssppShouldSwitch decides whether a build installed without the user asking
// for it plays PSP games from now on: never over a launcher pinned by hand or
// when the user chose the firmware's PPSSPP; always when they chose JukaHub's or
// already play a JukaHub build; otherwise only when it is newer than the
// firmware's.
func ppssppShouldSwitch(st ppssppState, b ppssppBuild, fwVersion string, userPinned bool) bool {
	if userPinned || st.Choice == ppssppChoiceFirmware {
		return false
	}
	if st.Choice == ppssppChoiceJukaHub || st.Active != "" {
		return true
	}
	return ppssppNewer(b.Version, fwVersion)
}

// ppssppRecordLatest remembers what the release check found (for the tile and
// the card) and returns the state after it.
func ppssppRecordLatest(rel ppssppRelease) ppssppState {
	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	st := ppssppLockedState()
	st.LatestTag, st.LatestVersion = rel.Tag, rel.Version
	if err := ppssppSaveLocked(); err != nil {
		logDebug("ppsspp", "could not save the check: %v", err)
	}
	return st.clone()
}

// ppssppStampCheck records a daily check that is done with its release, so the
// next one waits a day.
func ppssppStampCheck(now time.Time) {
	ppssppAutoMarkPass()
	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	ppssppLockedState().CheckedAt = now.UTC().Format(time.RFC3339)
	if err := ppssppSaveLocked(); err != nil {
		logDebug("ppsspp", "could not save the check stamp: %v", err)
	}
}

// ppssppMarkBad records why a release cannot be used, so the daily pass does
// not try it again.
func ppssppMarkBad(tag, reason string) {
	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	st := ppssppLockedState()
	if st.Bad == nil {
		st.Bad = map[string]string{}
	}
	st.Bad[tag] = reason
	_ = ppssppSaveLocked()
}

// ppssppSanitizeFirmware drops a "firmware" memstick inside JukaHub's own
// folder (or holding the shared memstick), which would bind a folder onto
// itself, and a "firmware" launcher that is one of JukaHub's own scripts.
func ppssppSanitizeFirmware(fw ppssppFirmwareInfo) ppssppFirmwareInfo {
	root := ppssppManagedRootFn()
	if fw.Stick != "" && (isPathWithin(fw.Stick, root) || isPathWithin(ppssppManagedMemstick(), fw.Stick)) {
		logWarn("ppsspp", "firmware memstick %s overlaps JukaHub's PPSSPP folder; not sharing it", fw.Stick)
		fw.Stick = ""
	}
	if fw.Launcher != "" && isPathWithin(fw.Launcher, root) {
		fw.Launcher = ""
	}
	return fw
}

// ppssppRefreshActiveScript writes the launcher of the build PSP games use
// again when the firmware's PPSSPP moved — another default build on CrossMix, a
// launcher pinned by hand — so the two keep sharing one save folder and the
// fallback still exists.
func ppssppRefreshActiveScript() {
	fw, ok := ppssppFirmwareCached()
	if !ok {
		return
	}
	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	st := ppssppLockedState()
	b, ok := st.build(st.Active)
	if !ok || !regularFile(b.binary()) {
		return
	}
	want := ppssppLauncherScript(ppssppScriptSpecFor(b, ppssppSanitizeFirmware(fw)))
	if current, err := os.ReadFile(b.script()); err == nil && string(current) == want {
		return
	}
	if err := ppssppWriteScript(b, fw); err != nil {
		logWarn("ppsspp", "could not rewrite the launcher of %s: %v", b.label(), err)
		return
	}
	logInfo("ppsspp", "rewrote the launcher of %s for the firmware's PPSSPP as it is now", b.label())
}

// ppssppCheckPlatform handles a card moved to another TrimUI model: a build
// made for another chip cannot run here, so PSP games go back to the firmware's
// PPSSPP and those builds are removed. An unidentified model changes nothing.
func ppssppCheckPlatform() {
	platform, _ := ppssppPlatform()
	if platform == "" {
		return
	}
	foreign := func(b ppssppBuild) bool { return b.Platform != "" && b.Platform != platform }
	st := ppssppSnapshot()
	found := false
	for _, b := range st.Builds {
		found = found || foreign(b)
	}
	if !found {
		return
	}
	ppssppMgr.mu.Lock()
	s := ppssppLockedState()
	switched := false
	if b, ok := s.build(s.Active); ok && foreign(b) {
		if err := ppssppUseFirmwareLocked(s); err != nil {
			ppssppMgr.mu.Unlock()
			logWarn("ppsspp", "could not move PSP games off a %s build: %v", b.Platform, err)
			return
		}
		s.Previous = ""
		switched = true
	}
	_ = ppssppSaveLocked()
	ppssppMgr.mu.Unlock()
	invalidateEmulatorCache()

	ppssppMgr.mu.Lock()
	s = ppssppLockedState()
	var left []ppssppBuild
	for _, b := range s.Builds {
		if foreign(b) {
			if err := ppssppSafeRemove(b.Dir); err == nil {
				logInfo("ppsspp", "removed build %s, made for %s, on a %s", b.Tag, b.Platform, platform)
				continue
			}
		}
		left = append(left, b)
	}
	s.Builds = left
	_ = ppssppSaveLocked()
	ppssppMgr.mu.Unlock()
	if switched {
		ShowToast("JukaHub's PPSSPP was made for another TrimUI model — PSP games use the firmware's PPSSPP; install it again from the PSP tile", ToastKindWarning)
	}
}

// ppssppUserPinnedNow is ppssppUserPinnedLocked with the lock taken.
func ppssppUserPinnedNow() bool {
	ppssppMgr.mu.Lock()
	defer ppssppMgr.mu.Unlock()
	return ppssppUserPinnedLocked()
}

// ppssppReleaseLabel names what a release brings.
func ppssppReleaseLabel(rel ppssppRelease) string {
	if rel.Version != "" {
		return "PPSSPP " + rel.Version
	}
	return "a new PPSSPP build (minui-psp " + rel.Tag + ")"
}

// ppssppNotifyMessage is the notify mode's toast for a new release.
func ppssppNotifyMessage(rel ppssppRelease, fw ppssppFirmwareInfo) string {
	msg := ppssppReleaseLabel(rel) + " is available for PSP games"
	if fw.Version != "" {
		msg += " (the firmware has " + fw.Version + ")"
	}
	return msg + " · " + hintSentence("install it from the PSP tile on the Emulators page (%s)", "X", "PPSSPP builds")
}

// startPPSSPPAutoUpdate starts the PPSSPP scheduler on the handheld. The mode
// comes from the shared Emulator updates setting, which startCoreAutoUpdate has
// already read from config into emuCoreUpdateModeNow; config itself is only
// read there, on the main thread.
func startPPSSPPAutoUpdate(config *Config) {
	_ = config
	if !ppssppManagedEnabled() || !ppssppAutoStarted.CompareAndSwap(false, true) {
		return
	}
	go ppssppAutoUpdateLoop()
}

// ppssppAutoUpdateLoop wakes every ppssppAutoPoll. Each time, outside a game,
// it reviews the last launch (cheap: a stat or two), and every ppssppFirmwareTTL
// it looks at the firmware's PPSSPP again and refreshes the active launcher.
// Once the startup delay has passed it runs the update tick every
// coreAutoUpdatePoll — at every wake while a due check waits for its moment.
func ppssppAutoUpdateLoop() {
	time.Sleep(ppssppReviewDelay)
	// The first look takes mounts down, rewrites a launcher and scans the
	// card: never over a game, which a resume at boot can have started.
	for gameScreenSuspended() {
		time.Sleep(ppssppAutoPoll)
	}
	start := time.Now()
	ppssppCleanStaleMounts()
	// A .staging- folder is only a leftover while no install holds the patch
	// slot: one the user started from the card may be unpacking into it now.
	if tryBeginPatchOp() {
		ppssppCleanLeftovers()
		endPatchOp()
	}
	ppssppDetectFirmwareNow()
	ppssppCheckPlatform()
	ppssppRefreshActiveScript()
	lastRefresh := time.Now()
	var lastTick time.Time
	waiting := false
	for {
		now := time.Now()
		if !gameScreenSuspended() {
			ppssppReviewLastRun()
			if now.Sub(lastRefresh) >= ppssppFirmwareTTL {
				lastRefresh = now
				ppssppDetectFirmwareNow()
				ppssppRefreshActiveScript()
			}
		}
		if now.Sub(start) >= ppssppStartupDelay-ppssppReviewDelay && (waiting || lastTick.IsZero() || now.Sub(lastTick) >= coreAutoUpdatePoll) {
			lastTick = now
			_, waiting = ppssppAutoUpdateTick(now)
		}
		time.Sleep(ppssppAutoPoll)
	}
}

// ppssppAutoSession is the scheduler's memory of this run by the monotonic
// clock, with coreAutoUpdateSession's rules: when a due check started waiting
// for its moment, and when the last check that read the release finished. The
// second is the guard a stamp cannot be on a handheld whose clock reads 1970
// until it reaches the network: every stamp looks due then.
var ppssppAutoSession struct {
	mu           sync.Mutex
	waitingSince time.Time
	lastPass     time.Time
}

// ppssppAutoWaited starts the waiting clock if it is not running and reports
// how long the check has been waiting.
func ppssppAutoWaited() time.Duration {
	ppssppAutoSession.mu.Lock()
	defer ppssppAutoSession.mu.Unlock()
	if ppssppAutoSession.waitingSince.IsZero() {
		ppssppAutoSession.waitingSince = time.Now()
		return 0
	}
	return time.Since(ppssppAutoSession.waitingSince)
}

// ppssppAutoStopWaiting resets the waiting clock: the check ran, or is not due.
func ppssppAutoStopWaiting() {
	ppssppAutoSession.mu.Lock()
	ppssppAutoSession.waitingSince = time.Time{}
	ppssppAutoSession.mu.Unlock()
}

// ppssppAutoRanRecently reports a check inside the last interval of this
// session, whatever the wall clock says.
func ppssppAutoRanRecently() bool {
	ppssppAutoSession.mu.Lock()
	defer ppssppAutoSession.mu.Unlock()
	last := ppssppAutoSession.lastPass
	return !last.IsZero() && time.Since(last) < coreAutoUpdateInterval
}

// ppssppAutoMarkPass records a check that read the release.
func ppssppAutoMarkPass() {
	ppssppAutoSession.mu.Lock()
	ppssppAutoSession.lastPass = time.Now()
	ppssppAutoSession.waitingSince = time.Time{}
	ppssppAutoSession.mu.Unlock()
}

// ppssppAutoOutcome is how install mode's work for one release ended.
type ppssppAutoOutcome int

const (
	// ppssppAutoInstalled: the build is installed (and switched to or not).
	ppssppAutoInstalled ppssppAutoOutcome = iota
	// ppssppAutoInterrupted: a game started or another install held the card —
	// the check is still due at the next quiet moment.
	ppssppAutoInterrupted
	// ppssppAutoUnusable: this release cannot be used here; the tag is marked
	// and the pass waits for the next one.
	ppssppAutoUnusable
	// ppssppAutoFailed: a download or card error, or time ran out, worth a few
	// more tries.
	ppssppAutoFailed
	// ppssppAutoCancelled: cancelled from the install card — a "not now",
	// stamped like the core pass does, not started again at the next wake.
	ppssppAutoCancelled
)

// ppssppAutoFailures counts this session's failed installs of the due release.
var ppssppAutoFailures atomic.Int32

// ppssppAutoUpdateTick runs the daily check once if it is allowed, due and the
// moment is right. ran reports that it reached the network; waiting that the
// check is still due and the scheduler should look again at its next wake (the
// moment was wrong, the network or the patch slot was not there, or an install
// was interrupted). The check is stamped only once it is done with the release:
// told, nothing to do, installed, found unusable or cancelled from the card — or
// after a few failures. A check that could not read the release is asked again
// at the next poll.
func ppssppAutoUpdateTick(now time.Time) (ran, waiting bool) {
	mode := emuCoreUpdateModeNow()
	if mode == coreUpdateOff {
		ppssppAutoStopWaiting()
		return false, false
	}
	platform, untested := ppssppPlatform()
	if !coreAutoUpdateAllowed(coreAutoUpdateDisabled(), platform != "", IsTrimuiDevice(), true) {
		return false, false
	}
	st := ppssppSnapshot()
	if !coreAutoUpdateDue(st.CheckedAt, now) || ppssppAutoRanRecently() {
		ppssppAutoStopWaiting()
		return false, false
	}
	waited := ppssppAutoWaited()
	if !IsOnline() {
		return false, true
	}
	busy := installBarActive() || videoHoldsScreenAwake()
	if !coreAutoUpdateMoment(gameScreenSuspended(), busy, getBatteryCharging(),
		userInputAge(now), coreAutoUpdateOverdueFor(st.CheckedAt, now, waited)) {
		return false, true
	}
	// Install mode takes the patch slot before the check, so a busy slot is a
	// retry at the next wake rather than a stamped check that waits a day.
	if mode == coreUpdateInstall {
		if !tryBeginPatchOp() {
			// Looked at every wake while due: the debug log only.
			logDebug("ppsspp", "auto-update waiting, another Patch operation is running")
			return false, true
		}
		defer endPatchOp()
		if installBarActive() {
			return false, true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	rel, err := ppssppFetchRelease(ctx, newEmulatorUpdateClient())
	cancel()
	if err != nil {
		// Not stamped, so still due — but asked again at the next poll, as the
		// core pass does, not at every wake: a refusal that lasts (a release
		// without a digest, GitHub's rate limit) would hit the API all day.
		logPatch("ppsspp update check: %v", err)
		return true, false
	}
	st = ppssppRecordLatest(rel)
	invalidateEmulatorCache()

	fw := ppssppDetectFirmwareNow()
	switch ppssppAutoDecide(mode, st, rel, fw.Version, untested) {
	case ppssppAutoNotify:
		ppssppStampCheck(now)
		if st.Announced != rel.Tag {
			ppssppMgr.mu.Lock()
			ppssppLockedState().Announced = rel.Tag
			_ = ppssppSaveLocked()
			ppssppMgr.mu.Unlock()
			msg := ppssppNotifyMessage(rel, fw)
			ShowToast(msg, ToastKindInfo)
			logPatch("ppsspp update check: %s", msg)
		}
		return true, false
	case ppssppAutoNothing:
		ppssppStampCheck(now)
		logPatch("ppsspp update check: minui-psp %s, nothing to do", rel.Tag)
		return true, false
	}
	switch ppssppRunAutoInstall(rel, platform, fw) {
	case ppssppAutoInterrupted:
		return true, true
	case ppssppAutoFailed:
		if ppssppAutoFailures.Add(1) < 3 {
			return true, true
		}
	}
	ppssppAutoFailures.Store(0)
	ppssppStampCheck(now)
	return true, false
}

// ppssppRunAutoInstall is install mode's work for one release: install it
// through the install bar (a game that starts cancels it between steps), then
// switch PSP games to it when ppssppShouldSwitch says so. The caller holds the
// patch slot.
func ppssppRunAutoInstall(rel ppssppRelease, platform string, fw ppssppFirmwareInfo) ppssppAutoOutcome {
	// The check above can take a while; a helper download may own the install
	// bar by now, and a begin would retire its token and stop it.
	if gameScreenSuspended() || installBarActive() {
		return ppssppAutoInterrupted
	}
	logPatch("ppsspp auto-update: installing minui-psp %s", rel.Tag)
	installBarBeginLabelled("PPSSPP", "Updating PPSSPP for PSP games")
	ctx, cancel := context.WithTimeout(installBarRunContext(), 30*time.Minute)
	defer cancel()
	var stoppedForGame atomic.Bool
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if gameScreenSuspended() {
					logPatch("ppsspp auto-update: a game started, stopping")
					stoppedForGame.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	build, err := ppssppInstall(ctx, emuWinClient(), rel, platform, fw, emuInstallBarReport)
	if err != nil {
		installBarFail(installCardDetail(emuFriendlyError(err)))
		logPatch("ppsspp auto-update: %v", err)
		switch {
		case stoppedForGame.Load():
			return ppssppAutoInterrupted
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			// Out of time on a slow line: counted as a failure, so a download
			// that never fits is not started again at every wake.
			return ppssppAutoFailed
		case ctx.Err() != nil:
			return ppssppAutoCancelled
		case errors.Is(err, errPPSSPPUnusable):
			ppssppMarkBad(rel.Tag, err.Error())
			ShowToast("PPSSPP update: "+emuFriendlyError(err), ToastKindWarning)
			return ppssppAutoUnusable
		}
		return ppssppAutoFailed
	}
	installBarPhase(installPhaseDone)
	installBarDone()

	ppssppMgr.mu.Lock()
	st := ppssppLockedState()
	switched := false
	var serr error
	if ppssppShouldSwitch(*st, build, fw.Version, ppssppUserPinnedLocked()) {
		if serr = ppssppUseBuildLocked(st, build, fw); serr == nil {
			switched = true
		}
		_ = ppssppSaveLocked()
	}
	ppssppMgr.mu.Unlock()
	invalidateEmulatorCache()
	PlaySuccessSound()
	switch {
	case serr != nil:
		ShowToast(build.label()+" installed, but PSP games could not be switched to it: "+serr.Error(), ToastKindWarning)
	case switched:
		ShowToast("PSP games now use "+build.label()+" · the build before is kept, and the firmware's PPSSPP takes over if it does not start", ToastKindSuccess)
	default:
		ShowToast(build.label()+" installed · PSP games keep "+ppssppFirmwareLabel(fw)+" — switch on the Emulators page", ToastKindSuccess)
	}
	logPatch("ppsspp auto-update: installed %s (switched %v)", build.label(), switched)
	return ppssppAutoInstalled
}

// ppssppUnmount takes one mount point down with the system's umount, lazily
// when a plain unmount is refused.
func ppssppUnmount(mountPoint string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "umount", mountPoint).CombinedOutput()
	if err == nil {
		return nil
	}
	lazy, lerr := exec.CommandContext(ctx, "umount", "-l", mountPoint).CombinedOutput()
	if lerr == nil {
		return nil
	}
	return fmt.Errorf("%v (%s %s)", lerr, strings.TrimSpace(string(out)), strings.TrimSpace(string(lazy)))
}
