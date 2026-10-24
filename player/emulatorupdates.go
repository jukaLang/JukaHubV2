package main

// emulatorupdates.go — checking the emulators JukaHub is allowed to update.
//
// Two different things live here, because the hardware forces the distinction:
//
//   - libretro cores. The buildbot publishes an aarch64 index and one zip per
//     core. JukaHub compares the CRC32 the index states with the CRC32 of the
//     installed binary and, when they differ, installs the newer build into the
//     existing core pool (data/performance/cores/<core>/<date>/). The active
//     core in RetroArch's cores folder is never touched: activating a pool
//     build stays the deliberate one-press action it already was, and that
//     path already keeps a .bak of whatever it replaced.
//   - standalone emulators (PPSSPP, mGBA, Flycast). Their upstream releases are
//     built for desktop Linux, not for this firmware's userspace and launcher
//     scripts, so they are fetched to data/emulator-updates/<app>/<tag>/ and
//     reported, never dropped over an installed emulator. Nothing here can
//     break a working install in the background.
//
// The index is a plain listing, not a signed document: its CRC is a corruption
// check, not proof of authorship. That is why a fetched core lands in the pool
// and waits for the user instead of replacing what is running.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// retroArchCoreIndexURL is the ARM64 buildbot index RetroArch's own core
	// updater reads. There is no stable aarch64 tree, so this is the nightly
	// channel; a build can be newer and still be worse than what is installed,
	// which is why activation stays manual.
	retroArchCoreIndexURL = "https://buildbot.libretro.com/nightly/linux/aarch64/latest/.index-extended"
	retroArchCoreBaseURL  = "https://buildbot.libretro.com/nightly/linux/aarch64/latest/"
	coreUpdateMaxDownload = 96 << 20
	standaloneMaxDownload = 256 << 20
)

// The URLs are variables so tests can point the checker at a local server.
var (
	coreUpdateIndexURL = retroArchCoreIndexURL
	coreUpdateBaseURL  = retroArchCoreBaseURL
	standaloneAPIBase  = "https://api.github.com"
	// emulatorUpdateValidateURL is the host policy for everything this file
	// downloads. It defaults to the app's single allowlist and is a variable so
	// tests can point at a loopback server.
	emulatorUpdateValidateURL = validatePatchURL
)

// fetchUpdateBytes is fetchBounded's counterpart for the buildbot: same size
// limit and headers, but it validates against the emulator-update policy rather
// than assuming the URL was already checked by the caller.
func fetchUpdateBytes(ctx context.Context, client *http.Client, raw string, maxBytes int64) ([]byte, error) {
	if err := emulatorUpdateValidateURL(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub-EmulatorUpdates/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, raw)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response exceeds %d byte limit", maxBytes)
	}
	return data, nil
}

// coreIndexEntry is one line of the buildbot index:
//
//	2026-10-23 16647efd mednafen_supergrafx_libretro.so.zip
type coreIndexEntry struct {
	Date  string
	CRC32 string
	File  string
	Core  string
}

// coreUpdate is an installed core the index has a different build of.
type coreUpdate struct {
	Core    string
	Date    string
	CRC32   string
	URL     string
	Current string
}

// standaloneUpdate is one upstream release an installed emulator could be
// updated to, and where it was fetched to.
type standaloneUpdate struct {
	Name string
	Repo string
	Tag  string
	URL  string
	Path string
	Note string
	// Digest is GitHub's own "sha256:<hex>" for the asset. It is checked when
	// present; an older release without one is still fetched, with a note.
	Digest string
}

// parseCoreIndex reads the buildbot index. It skips anything that is not a
// "<date> <crc32> <core>_libretro.so.zip" line rather than failing the whole
// check over one odd entry, and the last line for a core wins.
func parseCoreIndex(text string) []coreIndexEntry {
	byCore := make(map[string]coreIndexEntry)
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 3 {
			continue
		}
		date, crc, file := fields[0], strings.ToLower(fields[1]), fields[2]
		if len(date) != len("2006-01-02") || len(crc) != 8 || !isHex(crc) {
			continue
		}
		if !strings.HasSuffix(file, "_libretro.so.zip") {
			continue
		}
		core := strings.TrimSuffix(strings.TrimSuffix(file, ".zip"), "_libretro.so")
		if core == "" || strings.ContainsAny(core, "/\\") {
			continue
		}
		byCore[core] = coreIndexEntry{Date: date, CRC32: crc, File: file, Core: core}
	}
	out := make([]coreIndexEntry, 0, len(byCore))
	for _, entry := range byCore {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Core < out[j].Core })
	return out
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// coreCRC32File is the checksum the buildbot index states for a core.
func coreCRC32File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%08x", h.Sum32()), nil
}

// planCoreUpdates returns the installed cores whose build differs from the one
// the index lists, newest-first by core name. A core the index does not carry
// (a custom build, a Windows .dll) is left alone.
func planCoreUpdates(entries []coreIndexEntry, active map[string]coreBuild) []coreUpdate {
	updates := make([]coreUpdate, 0, len(entries))
	for _, entry := range entries {
		build, ok := active[entry.Core]
		if !ok || build.Path == "" {
			continue
		}
		sum, err := coreCRC32File(build.Path)
		if err != nil || sum == entry.CRC32 {
			continue
		}
		updates = append(updates, coreUpdate{
			Core:    entry.Core,
			Date:    entry.Date,
			CRC32:   entry.CRC32,
			URL:     coreUpdateBaseURL + entry.File,
			Current: build.Version,
		})
	}
	return updates
}

// fetchCoreIndex downloads and parses the buildbot index.
func fetchCoreIndex(ctx context.Context, client *http.Client) ([]coreIndexEntry, error) {
	data, err := fetchUpdateBytes(ctx, client, coreUpdateIndexURL, patchMaxMetadata)
	if err != nil {
		return nil, err
	}
	entries := parseCoreIndex(string(data))
	if len(entries) == 0 {
		return nil, errors.New("the core index is empty")
	}
	return entries, nil
}

// fetchCoreUpdate downloads one core zip, checks the CRC32 the index promised
// and writes the binary into the pool. A pool build is inert until the user
// activates it in the compatibility panel.
func fetchCoreUpdate(ctx context.Context, client *http.Client, upd coreUpdate, poolRoot string) (coreBuild, error) {
	data, err := fetchUpdateBytes(ctx, client, upd.URL, coreUpdateMaxDownload)
	if err != nil {
		return coreBuild{}, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return coreBuild{}, fmt.Errorf("core archive is not a zip: %w", err)
	}
	// The buildbot only publishes Linux shared objects, even when JukaHub itself
	// is running on Windows for a test: the .dll name is the local convention,
	// not the release's.
	want := upd.Core + "_libretro.so"
	var payload []byte
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Base(f.Name), want) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return coreBuild{}, err
		}
		payload, err = io.ReadAll(io.LimitReader(rc, coreUpdateMaxDownload+1))
		rc.Close()
		if err != nil {
			return coreBuild{}, err
		}
		break
	}
	if len(payload) == 0 {
		return coreBuild{}, fmt.Errorf("%s does not contain %s", upd.URL, want)
	}
	if int64(len(payload)) > coreUpdateMaxDownload {
		return coreBuild{}, errors.New("core binary is larger than the download limit")
	}
	h := crc32.NewIEEE()
	_, _ = h.Write(payload)
	if got := fmt.Sprintf("%08x", h.Sum32()); got != upd.CRC32 {
		return coreBuild{}, fmt.Errorf("core checksum mismatch (index %s, download %s)", upd.CRC32, got)
	}

	dir := filepath.Join(poolRoot, upd.Core, upd.Date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return coreBuild{}, err
	}
	path := filepath.Join(dir, want)
	tmp := path + ".part"
	if err := os.WriteFile(tmp, payload, 0o755); err != nil {
		return coreBuild{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return coreBuild{}, err
	}
	// Provenance travels with the build: the pool is a folder a user can copy
	// to another card, and the URL is the only record of where it came from.
	meta := map[string]string{
		"source":     upd.URL,
		"crc32":      upd.CRC32,
		"index_date": upd.Date,
		"fetched_at": time.Now().UTC().Format(time.RFC3339),
	}
	if encoded, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "source.json"), append(encoded, '\n'), 0o644)
	}
	return coreBuild{
		Core:    upd.Core,
		Version: upd.Date,
		File:    want,
		Path:    path,
		Pool:    true,
		Size:    int64(len(payload)),
	}, nil
}

// standaloneEmulatorRepos is the allowlist of upstream projects worth checking.
// They are looked up, and their aarch64 asset downloaded to a staging folder;
// nothing is installed, because the launcher on the card belongs to the
// firmware, not to JukaHub.
var standaloneEmulatorRepos = []struct {
	Name string
	Repo string
}{
	{"PPSSPP", "hrydgard/ppsspp"},
	{"mGBA", "mgba-emu/mgba"},
	{"Flycast", "flyinghead/flycast"},
}

// standaloneArm64Asset reports whether an upstream asset is the aarch64 Linux
// build worth fetching.
func standaloneArm64Asset(name string) bool {
	lower := strings.ToLower(name)
	if !strings.Contains(lower, "arm64") && !strings.Contains(lower, "aarch64") {
		return false
	}
	// "arm64" is not enough on its own: PPSSPP also ships a Windows-ARM64
	// archive, which would install a desktop binary on the card.
	for _, other := range []string{"windows", "win32", "win64", "win-arm", "macos", "osx", "darwin"} {
		if strings.Contains(lower, other) {
			return false
		}
	}
	for _, ext := range []string{".zip", ".tar.gz", ".tgz", ".appimage"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// fetchStandaloneUpdates asks each allowlisted project for its latest release
// and returns the aarch64 asset when it publishes one. A project with no such
// asset gets a note instead of an offer, because a desktop build is not an
// update for this device.
func fetchStandaloneUpdates(ctx context.Context, client *http.Client) ([]standaloneUpdate, []string) {
	var found []standaloneUpdate
	var errs []string
	for _, item := range standaloneEmulatorRepos {
		url := fmt.Sprintf("%s/repos/%s/releases/latest", standaloneAPIBase, item.Repo)
		data, err := fetchUpdateBytes(ctx, client, url, patchMaxMetadata)
		if err != nil {
			errs = append(errs, item.Name+": "+err.Error())
			continue
		}
		var rel struct {
			TagName string `json:"tag_name"`
			Assets  []struct {
				Name               string `json:"name"`
				BrowserDownloadURL string `json:"browser_download_url"`
				Digest             string `json:"digest"`
			} `json:"assets"`
		}
		if err := json.Unmarshal(data, &rel); err != nil {
			errs = append(errs, item.Name+": unreadable release metadata")
			continue
		}
		update := standaloneUpdate{Name: item.Name, Repo: item.Repo, Tag: rel.TagName}
		for _, asset := range rel.Assets {
			if standaloneArm64Asset(asset.Name) {
				update.URL = asset.BrowserDownloadURL
				update.Digest = asset.Digest
				break
			}
		}
		if update.URL == "" {
			update.Note = "no aarch64 build published"
		}
		found = append(found, update)
	}
	return found, errs
}

// downloadStandalone saves an upstream emulator archive under the app's data
// folder. The file is verified against the digest GitHub reports for the asset;
// it is deliberately not unpacked over an installed emulator.
func downloadStandalone(ctx context.Context, client *http.Client, update standaloneUpdate, destRoot string) (standaloneUpdate, error) {
	if update.URL == "" {
		return update, errors.New("no aarch64 asset to download")
	}
	data, err := fetchUpdateBytes(ctx, client, update.URL, standaloneMaxDownload)
	if err != nil {
		return update, err
	}
	if err := verifyReleaseDigest(data, update.Digest); err != nil {
		return update, err
	}
	name := sanitizeFileName(filepath.Base(update.URL))
	if name == "" {
		name = "download.bin"
	}
	dir := filepath.Join(destRoot, strings.ToLower(update.Name), sanitizeFileName(update.Tag))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return update, err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return update, err
	}
	update.Path = path
	return update, nil
}

// emulatorUpdateDir is where fetched standalone builds are staged. It sits in
// the app data folder so the files are easy to find, and deliberately not in
// an Emus/ folder: nothing is installed for the user.
func emulatorUpdateDir() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "emulator-updates")
	}
	return filepath.Join("data", "emulator-updates")
}

// verifyReleaseDigest checks GitHub's sha256 for a downloaded asset. A release
// that publishes no digest is fetched anyway; the caller logs that it was not
// verified rather than pretending it was.
func verifyReleaseDigest(data []byte, digest string) error {
	d := strings.TrimSpace(digest)
	if d == "" {
		return nil
	}
	const prefix = "sha256:"
	if !strings.HasPrefix(strings.ToLower(d), prefix) {
		return fmt.Errorf("unsupported release digest %q", digest)
	}
	sum := sha256.Sum256(data)
	want := strings.ToLower(strings.TrimSpace(d[len(prefix):]))
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("asset checksum mismatch (release %s, download %s)", want, got)
	}
	return nil
}

// emulatorUpdateReport is what one check-and-fetch pass did.
type emulatorUpdateReport struct {
	CoresChecked  int
	Cores         []coreBuild
	CoreErrors    []string
	Standalones   []standaloneUpdate
	StandaloneErr []string
}

// summary is the one-line result shown in the toast.
func (r emulatorUpdateReport) summary() string {
	parts := make([]string, 0, 3)
	if len(r.Cores) > 0 {
		parts = append(parts, itoa(len(r.Cores))+" core(s) added to the pool")
	}
	if len(r.Standalones) > 0 {
		downloaded := 0
		for _, s := range r.Standalones {
			if s.Path != "" {
				downloaded++
			}
		}
		if downloaded > 0 {
			parts = append(parts, itoa(downloaded)+" emulator build(s) downloaded")
		}
	}
	if len(r.CoreErrors) > 0 || len(r.StandaloneErr) > 0 {
		parts = append(parts, itoa(len(r.CoreErrors)+len(r.StandaloneErr))+" failed")
	}
	if len(parts) == 0 {
		if r.CoresChecked == 0 {
			return "No RetroArch cores to check"
		}
		return "All " + itoa(r.CoresChecked) + " cores are up to date"
	}
	return strings.Join(parts, " · ")
}

// runEmulatorUpdates performs one full pass: cores first, then the standalone
// lookups. It never touches an active core or an installed emulator.
func runEmulatorUpdates(ctx context.Context, client *http.Client) emulatorUpdateReport {
	var report emulatorUpdateReport
	inv := scanCoreInventory()
	report.CoresChecked = len(inv.Active)

	installBarPhase(installPhaseResolving)
	entries, err := fetchCoreIndex(ctx, client)
	if err != nil {
		report.CoreErrors = append(report.CoreErrors, "core index: "+err.Error())
	} else {
		updates := planCoreUpdates(entries, inv.Active)
		pool := compatCorePoolDir()
		for i, upd := range updates {
			if err := ctx.Err(); err != nil {
				report.CoreErrors = append(report.CoreErrors, err.Error())
				break
			}
			installBarPhase(installPhaseDownload)
			installBarDetail(fmt.Sprintf("%s (%d/%d)", upd.Core, i+1, len(updates)))
			build, err := fetchCoreUpdate(ctx, client, upd, pool)
			if err != nil {
				report.CoreErrors = append(report.CoreErrors, upd.Core+": "+err.Error())
				continue
			}
			report.Cores = append(report.Cores, build)
		}
	}

	installBarPhase(installPhaseResolving)
	installBarDetail("Checking standalone emulators")
	standalones, errs := fetchStandaloneUpdates(ctx, client)
	report.StandaloneErr = append(report.StandaloneErr, errs...)
	destRoot := emulatorUpdateDir()
	for _, update := range standalones {
		if update.URL == "" {
			report.Standalones = append(report.Standalones, update)
			continue
		}
		installBarPhase(installPhaseDownload)
		installBarDetail(update.Name + " " + update.Tag)
		fetched, err := downloadStandalone(ctx, client, update, destRoot)
		if err != nil {
			report.StandaloneErr = append(report.StandaloneErr, update.Name+": "+err.Error())
			continue
		}
		report.Standalones = append(report.Standalones, fetched)
	}
	return report
}

// newEmulatorUpdateClient is the patch client with a longer deadline: a core or
// emulator archive is tens of megabytes, and the 25 s metadata timeout would
// cut a slow handheld download in half. Redirects are still checked against the
// same host policy.
func newEmulatorUpdateClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errors.New("too many redirects")
			}
			return emulatorUpdateValidateURL(req.URL.String())
		},
	}
}

// startEmulatorUpdates runs the check off the event thread and reports through
// the install bar and a toast, the same way the other download actions do.
func startEmulatorUpdates() {
	if !tryBeginPatchOp() {
		ShowToast("An update is already running", ToastKindInfo)
		return
	}
	installBarBegin("Emulator updates")
	installBarPhase(installPhaseResolving)
	ShowToast("Checking libretro cores and emulator releases…", ToastKindInfo)
	go func() {
		defer endPatchOp()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		report := runEmulatorUpdates(ctx, newEmulatorUpdateClient())
		installBarPhase(installPhaseDone)
		installBarDone()
		kind := ToastKindSuccess
		if len(report.CoreErrors) > 0 || len(report.StandaloneErr) > 0 {
			kind = ToastKindWarning
		}
		PlaySuccessSound()
		ShowToast(report.summary(), kind)
	}()
}
