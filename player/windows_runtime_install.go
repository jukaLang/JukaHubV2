package main

// windows_runtime_install.go — one-press runtime setup for Windows Apps.
//
// A stock TrimUI has no Wine, and asking a handheld user to assemble Wine,
// Box64 and a display stack by hand is unrealistic. The setup chain here
// installs what a launch still needs, in order, from one confirming press:
//
//   - the runtime: Box64 + Wine 10.0 on ARM64 Linux, Wine 10.0 on an x86-64
//     Linux PC without a Wine of its own (wine_pack.go). The Hangover installer
//     below stays available only as an opt-in (JUKAHUB_WINE_FLAVOR=hangover or
//     JUKAHUB_WINE_RUNTIME_URL) on a firmware whose glibc can load it: its
//     Debian 12 build needs glibc 2.36 and the Smart Pro has 2.33;
//   - the display: WestonPack, the PortMaster runtime ports use to show X11
//     and Wayland programs on devices with neither. PortMaster's own copy is
//     used when it is there; else the image is downloaded from PortMaster's
//     repository and checked (SquashFS header and size, not a Git LFS pointer),
//     and kept as an image for the launch to mount. A Linux PC that cannot
//     mount (not root) unpacks it with unsquashfs instead;
//   - DXVK, only where it can run: automatically on an x86-64 PC whose Vulkan
//     driver declares 1.3, by hand elsewhere. On the Smart Pro (Vulkan 1.1)
//     the setup page offers DXVK 1.10.3, the last release that runs on 1.1,
//     as an experiment; it is never part of the automatic chain there.
//
// Then the prefix is prepared (wine_prefix.go), so the first launch is quick.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ulikunitz/xz"
)

const (
	windowsRuntimeReleaseAPI = "https://api.github.com/repos/AndreRH/hangover/releases/latest"
	// windowsWestonRuntimeURL is WestonPack in PortMaster-New's repository.
	// GitHub answers it with a redirect to raw.githubusercontent.com (or to
	// media.githubusercontent.com for a file kept in Git LFS); westonDownloadClient
	// admits exactly those two places for this repository.
	windowsWestonRuntimeURL = "https://github.com/PortsMaster/PortMaster-New/raw/main/runtimes/weston_pkg_0.2.aarch64.squashfs"
	windowsDXVKReleaseAPI   = "https://api.github.com/repos/doitsujin/dxvk/releases/latest"
	windowsRuntimeMaxBytes  = int64(2 << 30) // 2 GiB safety limit
	windowsRuntimeMaxFiles  = 100000
	// The last DXVK release that runs on Vulkan 1.1, for devices whose driver
	// is no newer. GitHub publishes no digest for it; the MD5 is the one
	// Bottles' component index lists for this tarball.
	windowsDXVKLegacyURL     = "https://github.com/doitsujin/dxvk/releases/download/v1.10.3/dxvk-1.10.3.tar.gz"
	windowsDXVKLegacyVersion = "1.10.3"
	windowsDXVKLegacyMD5     = "1c07b9d70ecc5df6c3ba9d13efaa351e"
	// WestonPack 0.2 is about 53 MB; anything far outside this is not it.
	westonMinImage = int64(20 << 20)
	westonMaxImage = int64(200 << 20)
)

var (
	windowsRuntimeClient  = httpClientLong
	windowsRuntimeRelease = windowsRuntimeReleaseAPI
	windowsRuntimeVerify  = verifyWineRuntime
	windowsRuntimeState   struct {
		mu      sync.Mutex
		running bool
		target  string // "wine", "weston" or "dxvk", while running
		status  string
		lastErr string
		// failed holds, per target, why its last install failed.
		failed map[string]string
	}
)

type hangoverAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type hangoverRelease struct {
	TagName string          `json:"tag_name"`
	Assets  []hangoverAsset `json:"assets"`
}

// selectHangoverAsset picks the ARM64 runtime archive, not the separate DLL
// archive. Distribution names change between releases, so match on the
// architecture and archive suffix and prefer the Debian 12 build used by most
// ARM handhelds.
func selectHangoverAsset(assets []hangoverAsset) (string, string, error) {
	var fallbackName, fallbackURL string
	for _, a := range assets {
		lower := strings.ToLower(a.Name)
		if !strings.Contains(lower, "arm64") || strings.Contains(lower, "dll") {
			continue
		}
		if !strings.HasSuffix(lower, ".tar") && !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") {
			continue
		}
		if fallbackURL == "" {
			fallbackName, fallbackURL = a.Name, a.BrowserDownloadURL
		}
		if strings.Contains(lower, "debian12") || strings.Contains(lower, "bookworm") {
			return a.Name, a.BrowserDownloadURL, nil
		}
	}
	if fallbackURL != "" {
		return fallbackName, fallbackURL, nil
	}
	return "", "", errors.New("the latest Hangover release has no ARM64 runtime archive")
}

func setWindowsRuntimeStatus(status, lastErr string, running bool) {
	windowsRuntimeState.mu.Lock()
	windowsRuntimeState.status = status
	windowsRuntimeState.lastErr = lastErr
	windowsRuntimeState.running = running
	windowsRuntimeState.mu.Unlock()
}

// windowsRuntimeInstallLine is shown on the setup page so a background install
// has visible progress and a failure is not a toast the user can miss.
func windowsRuntimeInstallLine() string {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	if windowsRuntimeState.status == "" {
		return ""
	}
	if !windowsRuntimeState.running && windowsRuntimeState.lastErr != "" {
		return "Installer: " + windowsRuntimeState.status + " · " + windowsRuntimeState.lastErr
	}
	return "Installer: " + windowsRuntimeState.status
}

func windowsRuntimeInstalling() bool {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	return windowsRuntimeState.running
}

// windowsRuntimeInstallTarget names the runtime being installed so the setup
// page only marks the row the user activated as "installing".
func windowsRuntimeInstallTarget() string {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	return windowsRuntimeState.target
}

// markWindowsRuntimeFailed records why target's install failed, for its
// setup row.
func markWindowsRuntimeFailed(target, reason string) {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	if windowsRuntimeState.failed == nil {
		windowsRuntimeState.failed = map[string]string{}
	}
	if reason == "" {
		reason = "failed"
	}
	windowsRuntimeState.failed[target] = reason
}

// windowsRuntimeFailed is why target's last install failed ("" when it did
// not, or is running again).
func windowsRuntimeFailed(target string) string {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	if windowsRuntimeState.running && windowsRuntimeState.target == target {
		return ""
	}
	return windowsRuntimeState.failed[target]
}

// windowsWineInstallFailed reports whether the last runtime install ended with
// an error, so the setup page can label the row "failed" rather than
// "missing".
func windowsWineInstallFailed() bool { return windowsRuntimeFailed("wine") != "" }

// windowsWestonInstallFailed is the display-runtime counterpart.
func windowsWestonInstallFailed() bool { return windowsRuntimeFailed("weston") != "" }

// windowsDXVKInstallFailed reports whether the last DXVK install ended with an
// error, so the setup page can say so on the row.
func windowsDXVKInstallFailed() bool { return windowsRuntimeFailed("dxvk") != "" }

// beginWindowsRuntimeInstall marks the target runtime as installing. It
// returns false when another install already owns the slot.
func beginWindowsRuntimeInstall(target, status string) bool {
	windowsRuntimeState.mu.Lock()
	defer windowsRuntimeState.mu.Unlock()
	if windowsRuntimeState.running {
		return false
	}
	windowsRuntimeState.running = true
	windowsRuntimeState.target = target
	windowsRuntimeState.status = status
	windowsRuntimeState.lastErr = ""
	delete(windowsRuntimeState.failed, target)
	return true
}

func endWindowsRuntimeInstall() {
	windowsRuntimeState.mu.Lock()
	windowsRuntimeState.running = false
	windowsRuntimeState.target = ""
	windowsRuntimeState.mu.Unlock()
}

// setWindowsRuntimeInstallTarget moves the progress line to the component the
// provisioning chain is working on now.
func setWindowsRuntimeInstallTarget(target string) {
	windowsRuntimeState.mu.Lock()
	windowsRuntimeState.target = target
	delete(windowsRuntimeState.failed, target)
	windowsRuntimeState.mu.Unlock()
}

// startWindowsRuntimeInstall runs the whole setup chain in the background
// (the setup page's runtime row): the runtime, the display, the prefix.
func startWindowsRuntimeInstall() error {
	return startWindowsProvisioning(nil)
}

// wineHangoverWanted reports the opt-in to Hangover instead of Box64 + Wine:
// JUKAHUB_WINE_FLAVOR=hangover, or JUKAHUB_WINE_RUNTIME_URL naming an
// archive.
func wineHangoverWanted() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("JUKAHUB_WINE_FLAVOR")), "hangover") ||
		strings.TrimSpace(os.Getenv("JUKAHUB_WINE_RUNTIME_URL")) != ""
}

// hostGlibcBelow reports positive evidence that this device's glibc is older
// than want ("GLIBC_2.36") and names the newest version it has. Like
// elfcompat.go it says nothing when it cannot tell.
func hostGlibcBelow(want string) (string, bool) {
	class, machine := elfHostArch(runtime.GOARCH)
	dirs := elfLibrarySearchDirs(nil, os.Getenv("JUKAHUB_ORIG_LD_LIBRARY_PATH"), os.Getenv("LD_LIBRARY_PATH"))
	info, state := elfFindLibrary("libc.so.6", class, machine, dirs)
	if state != elfLibFound || len(info.defs) == 0 {
		return "", false
	}
	have := elfHighestInFamily(info.defs, "GLIBC")
	if have == "" {
		return "", false
	}
	return have, elfVersionLess(have, want)
}

// windowsInstallRuntime installs the runtime step: JukaHub's Box64 + Wine (or
// Wine on an x86-64 PC), or Hangover when opted into and this firmware's
// glibc can load it. Progress goes to the installer line.
func windowsInstallRuntime(ctx context.Context) error {
	if IsWindows() {
		return errors.New("Windows runs .exe files directly; the Wine runtime is only needed on the handheld")
	}
	if wineProgramsRunning() {
		return errors.New("close the running Windows app first")
	}
	if wineHangoverWanted() {
		if have, old := hostGlibcBelow("GLIBC_2.36"); old {
			return fmt.Errorf("Hangover needs glibc 2.36 or newer, and this firmware has %s; unset JUKAHUB_WINE_FLAVOR to use Box64 + Wine", strings.TrimPrefix(have, "GLIBC_"))
		}
		return installWineRuntime(strings.TrimSpace(os.Getenv("JUKAHUB_WINE_RUNTIME_URL")))
	}
	return installWindowsRuntimeCore(ctx, nil, false, func(line string) {
		setWindowsRuntimeStatus(line, "", true)
	})
}

// installWineRuntime downloads and installs the Hangover archive. releaseURL
// may be empty to resolve the latest release from GitHub; tests pass a local
// URL directly.
func installWineRuntime(releaseURL string) error {
	if releaseURL == "" {
		releaseURL = windowsRuntimeRelease
	}
	assetURL := releaseURL
	if strings.HasSuffix(strings.ToLower(releaseURL), "/latest") || strings.Contains(releaseURL, "api.github.com") {
		setWindowsRuntimeStatus("checking the latest Hangover release", "", true)
		asset, err := latestHangoverAsset(releaseURL)
		if err != nil {
			return err
		}
		assetURL = asset
	}
	if !strings.HasPrefix(assetURL, "http://") && !strings.HasPrefix(assetURL, "https://") {
		return fmt.Errorf("runtime URL must be http(s): %s", assetURL)
	}

	base := windowsRuntimeDir()
	if err := os.MkdirAll(filepath.Join(base, "downloads"), 0o755); err != nil {
		return fmt.Errorf("create runtime folder: %w", err)
	}
	name := filepath.Base(assetURL)
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	if name == "" || name == "." || name == "/" {
		name = "hangover.tar"
	}
	archive := filepath.Join(base, "downloads", name)
	setWindowsRuntimeStatus("downloading "+name, "", true)
	if err := downloadRuntimeFile(assetURL, archive); err != nil {
		return err
	}

	staging := filepath.Join(base, fmt.Sprintf("staging-%d", time.Now().UnixNano()))
	defer os.RemoveAll(staging)
	setWindowsRuntimeStatus("extracting "+name, "", true)
	if err := extractRuntimeArchive(archive, staging); err != nil {
		return err
	}
	// Hangover ships the runtime as Debian packages inside the release
	// tarball, so unwrap those before looking for wine.
	tree, err := unpackWineRelease(runtimeArchiveRoot(staging))
	if err != nil {
		return err
	}

	binary, prefix, err := locateWineBinary(tree)
	if err != nil {
		return err
	}
	payload := filepath.Join(base, "hangover")
	if filepath.Clean(tree) == filepath.Clean(payload) {
		return errors.New("the runtime archive has an unexpected layout")
	}
	if err := os.RemoveAll(payload); err != nil {
		return fmt.Errorf("replace previous runtime: %w", err)
	}
	if err := os.Rename(tree, payload); err != nil {
		// A cross-device staging folder is possible on some data layouts; fall
		// back to a copy so the install still completes.
		if copyErr := copyTree(tree, payload); copyErr != nil {
			return fmt.Errorf("install runtime: %w (copy fallback: %v)", err, copyErr)
		}
	}
	rel, err := filepath.Rel(tree, binary)
	if err != nil {
		return err
	}
	installedBinary := filepath.Join(payload, rel)
	installedPrefix := filepath.Join(payload, prefix)
	if err := writeWineWrapper(filepath.Join(base, "bin", "wine"), installedBinary, installedPrefix); err != nil {
		return err
	}
	setWindowsRuntimeStatus("checking Wine", "", true)
	if windowsRuntimeVerify != nil {
		if err := windowsRuntimeVerify(filepath.Join(base, "bin", "wine")); err != nil {
			return fmt.Errorf("Wine is installed but failed to start: %w", err)
		}
	}
	_ = os.Remove(archive)
	setWindowsRuntimeStatus("Wine installed", "", false)
	return nil
}

func latestHangoverAsset(apiURL string) (string, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "JukaHub/0.4")
	resp, err := windowsRuntimeClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("check Hangover release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("check Hangover release: HTTP %d", resp.StatusCode)
	}
	var release hangoverRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("read Hangover release: %w", err)
	}
	_, assetURL, err := selectHangoverAsset(release.Assets)
	return assetURL, err
}

func downloadRuntimeFile(url, dest string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub/0.4")
	resp, err := windowsRuntimeClient.Do(req)
	if err != nil {
		return fmt.Errorf("download runtime: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download runtime: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > windowsRuntimeMaxBytes {
		return fmt.Errorf("runtime archive is too large (%d bytes)", resp.ContentLength)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(resp.Body, windowsRuntimeMaxBytes+1))
	if err != nil {
		return err
	}
	if n > windowsRuntimeMaxBytes {
		return errors.New("runtime archive exceeds the size limit")
	}
	return out.Close()
}

// extractRuntimeArchive supports the uncompressed .tar releases used by
// Hangover, plus .tar.gz/.tgz and .zip for user-provided builds. Entries are
// confined to dest; symlinks are only accepted when their target stays inside
// the extraction root.
func extractRuntimeArchive(archive, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	lower := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractRuntimeZip(archive, dest)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		return extractRuntimeTar(gz, dest)
	case strings.HasSuffix(lower, ".tar"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		return extractRuntimeTar(f, dest)
	default:
		return fmt.Errorf("unsupported runtime archive %q (use .tar, .tar.gz or .zip; set JUKAHUB_WINE_RUNTIME_URL for a custom build)", filepath.Base(archive))
	}
}

// unpackWineRelease returns a directory that contains a wine binary. A plain
// Wine tree is used as-is; the Hangover release instead ships Debian .deb
// packages, which are unpacked userland into <root>/runtime without dpkg or
// root access.
func unpackWineRelease(root string) (string, error) {
	if _, _, err := locateWineBinary(root); err == nil {
		return root, nil
	}
	var debs []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(d.Name()), ".deb") {
			debs = append(debs, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(debs) == 0 {
		return "", errors.New("the runtime archive contains neither a wine binary nor .deb packages")
	}
	dest := filepath.Join(root, "runtime")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	for _, deb := range debs {
		setWindowsRuntimeStatus("unpacking "+filepath.Base(deb), "", true)
		if err := extractDebData(deb, dest); err != nil {
			return "", fmt.Errorf("unpack %s: %w", filepath.Base(deb), err)
		}
	}
	if _, _, err := locateWineBinary(dest); err != nil {
		return "", err
	}
	return dest, nil
}

// extractDebData reads a Debian package's ar container and extracts its
// data.tar member. Only the compression formats that can be decoded in pure Go
// are accepted; the bookworm/xz release uses data.tar.xz.
func extractDebData(deb, dest string) error {
	f, err := os.Open(deb)
	if err != nil {
		return err
	}
	defer f.Close()

	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "!<arch>\n" {
		return errors.New("not a Debian package (bad ar header)")
	}
	for {
		header := make([]byte, 60)
		if _, err := io.ReadFull(f, header); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(header[0:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(header[48:58])), 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("invalid ar member %q", name)
		}
		if !strings.HasPrefix(name, "data.tar") {
			if _, err := f.Seek(size+size%2, io.SeekCurrent); err != nil {
				return err
			}
			continue
		}
		member := io.LimitReader(f, size)
		var data io.Reader = member
		switch name {
		case "data.tar":
		case "data.tar.xz":
			xr, err := xz.NewReader(member)
			if err != nil {
				return fmt.Errorf("read data.tar.xz: %w", err)
			}
			data = xr
		case "data.tar.gz":
			gr, err := gzip.NewReader(member)
			if err != nil {
				return fmt.Errorf("read data.tar.gz: %w", err)
			}
			defer gr.Close()
			data = gr
		case "data.tar.bz2":
			data = bzip2.NewReader(member)
		default:
			return fmt.Errorf("unsupported package payload %s", name)
		}
		return extractRuntimeTar(data, dest)
	}
	return errors.New("package has no data.tar")
}

// runtimeArchiveRoot drops a single wrapper directory. Releases usually ship
// as hangover/bin, hangover/lib, ...; installing the inner tree keeps the
// runtime at data/windows-runtime/hangover/bin/wine instead of a doubled
// hangover/hangover path.
func runtimeArchiveRoot(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return dir
	}
	entry := entries[0]
	if entry.Type()&os.ModeSymlink != 0 {
		return dir
	}
	return filepath.Join(dir, entry.Name())
}

func extractRuntimeTar(r io.Reader, dest string) error {
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(r)
	count := 0
	var links []archiveLink
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return materializeArchiveLinks(root, dest, links)
		}
		if err != nil {
			return err
		}
		count++
		if count > windowsRuntimeMaxFiles {
			return errors.New("runtime archive has too many entries")
		}
		target, err := runtimeArchivePath(dest, hdr.Name)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dest, target)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(rel, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			out, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, windowsRuntimeMaxBytes)); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			resolved, err := archiveLinkTarget(dest, target, hdr.Linkname)
			if err != nil {
				return err
			}
			// Collected, never created here: while members are being written
			// there must be no link the archive supplied, or a later member
			// could be written through it and land outside the runtime folder.
			// Links are built once the tree is complete (SD cards are usually
			// FAT/exFAT, where they are copied instead).
			links = append(links, archiveLink{target: target, source: resolved, link: strings.TrimSpace(hdr.Linkname)})
		case tar.TypeLink:
			source, err := runtimeArchivePath(dest, hdr.Linkname)
			if err != nil {
				return err
			}
			sourceRel, err := filepath.Rel(dest, source)
			if err != nil {
				return err
			}
			info, err := root.Lstat(sourceRel)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("archive hard link source is not a regular file")
			}
			if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
				return err
			}
			if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := root.Link(sourceRel, rel); err != nil {
				// exFAT and FAT have no hard links: a copy reads the same.
				if copyErr := copyRootFile(root, sourceRel, rel, info.Mode().Perm()); copyErr != nil {
					return copyErr
				}
			}
		}
	}
}

func extractRuntimeZip(archive, dest string) error {
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) > windowsRuntimeMaxFiles {
		return errors.New("runtime archive has too many entries")
	}
	var links []archiveLink
	for _, f := range zr.File {
		target, err := runtimeArchivePath(dest, f.Name)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dest, target)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := root.MkdirAll(rel, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			link, readErr := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if readErr != nil {
				return readErr
			}
			linkName := strings.TrimSpace(string(link))
			resolved, err := archiveLinkTarget(dest, target, linkName)
			if err != nil {
				return err
			}
			// Deferred for the same reason as the tar case: nothing in the
			// archive may exist as a link while files are still being written.
			links = append(links, archiveLink{target: target, source: resolved, link: linkName})
			continue
		}
		if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			rc.Close()
			return err
		}
		out, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(rc, windowsRuntimeMaxBytes))
		rc.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return materializeArchiveLinks(root, dest, links)
}

func runtimeArchivePath(dest, name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe archive entry %q", name)
	}
	target := filepath.Join(dest, filepath.FromSlash(name))
	// The prefix test is the guard a reader (and a static analyser) can see
	// without following isPathWithin; the second check catches the Windows
	// case where the join keeps a drive letter from the entry name.
	if !strings.HasPrefix(target, filepath.Clean(dest)+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the runtime folder", name)
	}
	if !isPathWithin(target, dest) {
		return "", fmt.Errorf("archive entry %q escapes the runtime folder", name)
	}
	return target, nil
}

// archiveLink resolves a symlink's source and rejects targets that leave the
// extraction root, whether or not the filesystem can create the link itself.
func archiveLinkTarget(dest, target, link string) (string, error) {
	link = strings.ReplaceAll(strings.TrimSpace(link), "\\", "/")
	if link == "" || strings.HasPrefix(link, "/") {
		return "", fmt.Errorf("unsafe archive symlink %q", link)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(target), filepath.FromSlash(link)))
	if !strings.HasPrefix(resolved, filepath.Clean(dest)+string(filepath.Separator)) {
		return "", fmt.Errorf("archive symlink %q escapes the runtime folder", link)
	}
	if !isPathWithin(resolved, dest) {
		return "", fmt.Errorf("archive symlink %q escapes the runtime folder", link)
	}
	return resolved, nil
}

type archiveLink struct {
	target string // where the link should be
	source string // what it points at, already validated as inside dest
	link   string // the link text, kept relative exactly as the archive spelled it
}

// materializeArchiveLinks creates the links after every member has been
// extracted. Wine archives use them for the winecfg/notepad aliases; on a
// filesystem without symlink support (the FAT/exFAT card) the target is copied
// instead, and losing a link must not abort an otherwise valid install.
func materializeArchiveLinks(root *os.Root, dest string, links []archiveLink) error {
	var copyNeeded []archiveLink
	for _, link := range links {
		// Revalidate both ends before creating the relative link.
		if !isPathWithin(link.target, dest) || !isPathWithin(link.source, dest) {
			return errors.New("archive symlink targets an unexpected path")
		}
		target, err := filepath.Rel(dest, link.target)
		if err != nil {
			return err
		}
		if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := root.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := root.Symlink(filepath.FromSlash(link.link), target); err != nil {
			copyNeeded = append(copyNeeded, link)
		}
	}
	links = copyNeeded
	for pass := 0; pass < 4 && len(links) > 0; pass++ {
		var pending []archiveLink
		for _, link := range links {
			source, err := filepath.Rel(dest, link.source)
			if err != nil {
				return err
			}
			target, err := filepath.Rel(dest, link.target)
			if err != nil {
				return err
			}
			info, err := root.Stat(source)
			if err != nil || !info.Mode().IsRegular() {
				pending = append(pending, link)
				continue
			}
			in, err := root.Open(source)
			if err != nil {
				return err
			}
			if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				in.Close()
				return err
			}
			if err := root.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				in.Close()
				return err
			}
			out, err := root.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				in.Close()
				return err
			}
			_, copyErr := io.Copy(out, in)
			in.Close()
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		links = pending
	}
	if len(links) > 0 {
		return fmt.Errorf("could not create %d archive link(s) on this filesystem", len(links))
	}
	return nil
}

func copyArchiveFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// locateWineBinary finds the wine launcher and the prefix whose lib folders
// must be on LD_LIBRARY_PATH. The returned prefix is relative to staging.
func locateWineBinary(staging string) (binary, prefix string, err error) {
	var candidates []string
	_ = filepath.WalkDir(staging, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if name == "wine" || name == "wine64" {
			candidates = append(candidates, path)
		}
		return nil
	})
	if len(candidates) == 0 {
		return "", "", errors.New("the runtime archive does not contain a wine binary")
	}
	best := candidates[0]
	for _, c := range candidates {
		if strings.EqualFold(filepath.Base(c), "wine") {
			best = c
			break
		}
	}
	binDir := filepath.Dir(best)
	if strings.EqualFold(filepath.Base(binDir), "bin") {
		prefix = filepath.Dir(binDir)
	} else {
		prefix = binDir
	}
	relPrefix, err := filepath.Rel(staging, prefix)
	if err != nil {
		return "", "", err
	}
	return best, relPrefix, nil
}

// writeWineWrapper creates the stable data/windows-runtime/bin/wine entry point
// that resolveWine looks for, pointing at the extracted Hangover tree.
func writeWineWrapper(wrapper, binary, prefix string) error {
	if err := os.MkdirAll(filepath.Dir(wrapper), 0o755); err != nil {
		return err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\n" +
		"# Generated by JukaHub's Windows runtime installer.\n" +
		"RUNTIME_PREFIX=" + quote(prefix) + "\n" +
		"export LD_LIBRARY_PATH=\"$RUNTIME_PREFIX/lib:$RUNTIME_PREFIX/lib64:$RUNTIME_PREFIX/lib/aarch64-linux-gnu:$RUNTIME_PREFIX/usr/lib:$RUNTIME_PREFIX/usr/lib/aarch64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}\"\n" +
		"export WINEDLLPATH=\"$RUNTIME_PREFIX/lib/wine:$RUNTIME_PREFIX/lib64/wine:$RUNTIME_PREFIX/usr/lib/wine${WINEDLLPATH:+:$WINEDLLPATH}\"\n" +
		"exec " + quote(binary) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		return err
	}
	return os.Chmod(wrapper, 0o755)
}

func verifyWineRuntime(wrapper string) error {
	cmd := exec.Command(wrapper, "--version")
	done := make(chan error, 1)
	var output []byte
	go func() {
		out, err := cmd.CombinedOutput()
		output = out
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	case <-time.After(25 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return errors.New("wine --version timed out")
	}
}

// copyTree is the fallback when staging and the runtime folder are on different
// filesystems, where os.Rename cannot move a directory.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// copyRootFile copies src to dst inside root, for a hard link the file system
// cannot make.
func copyRootFile(root *os.Root, src, dst string, mode os.FileMode) error {
	in, err := root.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if mode == 0 {
		mode = 0o644
	}
	out, err := root.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ── Display runtime ──────────────────────────────────────────────────────

// westonRedirectAllowed admits the two places GitHub serves a repository file
// from, for PortMaster-New only: raw.githubusercontent.com for a plain file,
// media.githubusercontent.com for one kept in Git LFS. The download
// allowlist leaves both hosts out because every repository's files are there;
// this exception covers one repository and nothing else.
func westonRedirectAllowed(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	p := path.Clean("/" + u.Path)
	switch strings.ToLower(u.Hostname()) {
	case "raw.githubusercontent.com":
		return strings.HasPrefix(p, "/PortsMaster/PortMaster-New/")
	case "media.githubusercontent.com":
		return strings.HasPrefix(p, "/media/PortsMaster/PortMaster-New/")
	}
	return false
}

// westonDownloadClient is the Patch client with westonRedirectAllowed added to
// what its redirects may reach.
func westonDownloadClient() *http.Client {
	client := newPatchClient()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if westonRedirectAllowed(req.URL) {
			return nil
		}
		if err := validatePatchURL(req.URL.String()); err != nil {
			return fmt.Errorf("redirect rejected: %w", err)
		}
		return nil
	}
	return client
}

// westonImageProblem says why the file at p is not a WestonPack image, ""
// when it looks right: SquashFS's "hsqs" magic and a plausible size, and with
// deep a SquashFS 4 superblock whose block size, compression and length make
// sense. It reads 96 bytes at most.
func westonImageProblem(p string, deep bool) string {
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "it is not there"
	}
	f, err := os.Open(p)
	if err != nil {
		return "it cannot be read"
	}
	defer f.Close()
	head := make([]byte, 96)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("version https://git-lfs")):
		return "GitHub sent a Git LFS pointer instead of the image"
	case n < 4 || string(head[:4]) != "hsqs":
		return "it is not a SquashFS image"
	case info.Size() < westonMinImage || info.Size() > westonMaxImage:
		return fmt.Sprintf("it is %s, which is not WestonPack's size", formatInstallBytes(info.Size()))
	case !deep:
		return ""
	case n < 96:
		return "its SquashFS header is cut short"
	}
	le := binary.LittleEndian
	blockSize := le.Uint32(head[12:])
	compression := le.Uint16(head[20:])
	blockLog := le.Uint16(head[22:])
	major := le.Uint16(head[28:])
	used := le.Uint64(head[40:])
	switch {
	case major != 4:
		return "it is not a SquashFS 4 image"
	case blockSize < 4096 || blockSize > 1<<20 || blockLog > 20 || uint32(1)<<blockLog != blockSize:
		return "its SquashFS block size is wrong"
	case compression < 1 || compression > 6:
		return "it uses an unknown compression"
	case used == 0 || used > uint64(info.Size()):
		return "it is cut short"
	}
	return ""
}

// installWestonRuntime makes WestonPack available: PortMaster's copy when it
// is there, else the image downloaded from PortMaster's repository and checked,
// kept in data/windows-runtime for the launch to mount. A Linux PC that cannot
// mount (not root) unpacks it with unsquashfs instead, when it has one.
func installWestonRuntime() error {
	if IsWindows() {
		return errors.New("WestonPack is only used by the handheld/Linux build")
	}
	if src := findWestonPackSource(); src.Image != "" || src.Wrapper != "" {
		return nil
	}
	if runtime.GOARCH != "arm64" {
		return errors.New("WestonPack exists for ARM handhelds only; on this computer, start JukaHub inside an X11 or Wayland desktop")
	}
	base := windowsRuntimeDir()
	if err := os.MkdirAll(base, 0o755); err != nil {
		if disk := relPackDiskErr(err); disk != nil {
			return disk
		}
		return err
	}
	if free, err := freeBytes(base); err == nil && free < 2*westonMinImage+relPackSpare {
		return fmt.Errorf("%w · WestonPack needs about 60 MB, the card has %s", errRelPackNoSpace, formatInstallBytes(max64(free, 0)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	image := filepath.Join(base, westonPackImageName)
	spec := wineTarballSpec{Asset: westonPackImageName, URL: windowsWestonRuntimeURL}
	err := wineDownload(ctx, westonDownloadClient(), spec, image, func(got, total int64) {
		setWindowsRuntimeStatus("Downloading WestonPack · "+formatInstallBytes(got)+" of about 53 MB", "", true)
	})
	if err != nil {
		return err
	}
	if problem := westonImageProblem(image, true); problem != "" {
		_ = os.Remove(image)
		if strings.Contains(problem, "LFS") {
			return fmt.Errorf("%w: %s · install WestonPack through PortMaster instead", errRelPackUnverified, problem)
		}
		return fmt.Errorf("%w: the WestonPack download %s", errRelPackUnverified, problem)
	}
	logInfo("windowsapps", "WestonPack image saved in %s", image)
	if os.Geteuid() == 0 {
		return nil
	}
	unsquashfs, lookErr := exec.LookPath("unsquashfs")
	if lookErr != nil {
		return errors.New("WestonPack is downloaded, but mounting it needs root and unpacking it needs unsquashfs; install squashfs-tools, or set JUKAHUB_WESTON to an existing westonwrap.sh")
	}
	setWindowsRuntimeStatus("Unpacking WestonPack…", "", true)
	installDir := filepath.Join(base, "weston")
	staging := filepath.Join(base, fmt.Sprintf("staging-%d", time.Now().UnixNano()))
	defer os.RemoveAll(staging)
	if out, err := exec.CommandContext(ctx, unsquashfs, "-f", "-d", staging, image).CombinedOutput(); err != nil {
		return fmt.Errorf("unpack WestonPack: %w: %s", err, firstLine(strings.TrimSpace(string(out))))
	}
	wrapper, err := findWestonWrapper(staging)
	if err != nil {
		return err
	}
	// The unpacked tree goes where findWestonPackSource looks:
	// data/windows-runtime/weston/westonwrap.sh. A relay script would break
	// westonwrap.sh's lookups relative to its own folder.
	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	if err := os.Rename(filepath.Dir(wrapper), installDir); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Join(installDir, filepath.Base(wrapper)), 0o755)
	return nil
}

func findWestonWrapper(root string) (string, error) {
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), "westonwrap.sh") {
			found = path
			return io.EOF
		}
		return nil
	})
	if found == "" {
		return "", errors.New("WestonPack archive does not contain westonwrap.sh")
	}
	return found, nil
}

// ── DXVK ─────────────────────────────────────────────────────────────────

// selectDXVKAsset picks the release tarball that carries the D3D DLLs. Hash
// files and source archives are skipped: only the binary .tar.gz (or .zip)
// contains x64/d3d11.dll, which is what makes DXVK usable.
func selectDXVKAsset(assets []githubAsset) (githubAsset, error) {
	for _, a := range assets {
		lower := strings.ToLower(a.Name)
		if strings.Contains(lower, "sha256") || strings.Contains(lower, ".asc") || strings.Contains(lower, "source") || strings.Contains(lower, "native") {
			continue
		}
		if !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") && !strings.HasSuffix(lower, ".zip") {
			continue
		}
		return a, nil
	}
	return githubAsset{}, errors.New("the latest DXVK release has no .tar.gz binary archive")
}

// dxvkLegacyOnly reports a device whose Vulkan is known to be older than the
// 1.3 DXVK 2.x needs: there only DXVK 1.10.3 can try.
func dxvkLegacyOnly() bool {
	major, minor, known := wineVulkanLevel()
	return known && (major < 1 || (major == 1 && minor < 3))
}

// dxvkPick is the DXVK build to install here: 1.10.3 where Vulkan is older
// than 1.3, else the latest release (checked against GitHub's digest when it
// publishes one).
func dxvkPick(ctx context.Context, client *http.Client) (spec wineTarballSpec, version, md5sum string, err error) {
	if dxvkLegacyOnly() {
		return wineTarballSpec{Asset: path.Base(windowsDXVKLegacyURL), URL: windowsDXVKLegacyURL}, windowsDXVKLegacyVersion, windowsDXVKLegacyMD5, nil
	}
	data, err := fetchBounded(ctx, client, windowsDXVKRelease, patchMaxMetadata)
	if err != nil {
		return spec, "", "", relPackNetErr("checking the DXVK release", err)
	}
	var rel githubRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return spec, "", "", fmt.Errorf("read DXVK release: %w", err)
	}
	asset, err := selectDXVKAsset(rel.Assets)
	if err != nil {
		return spec, "", "", err
	}
	spec = wineTarballSpec{Asset: sanitizeFileName(asset.Name), URL: asset.BrowserDownloadURL, SHA256: releaseAssetDigest(asset.Digest), Size: asset.Size}
	return spec, strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v"), "", nil
}

// installDXVKRuntime downloads DXVK and installs its DLL trees in
// data/windows-runtime/dxvk, which resolveDXVK checks, recording the release
// so applyWineCompatibilityEnv can tell whether this device's Vulkan runs it.
// DXVK is only useful with a Vulkan loader, so installing it without one is
// refused rather than leaving a directory that silently changes nothing.
func installDXVKRuntime() error {
	if windowsResolveVulkan() == "" {
		return errors.New("DXVK needs a Vulkan loader (libvulkan.so.1); install a Vulkan driver first, or set JUKAHUB_VULKAN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	client := newPatchClient()
	setWindowsRuntimeStatus("Looking for DXVK on GitHub…", "", true)
	spec, version, md5sum, err := dxvkPick(ctx, client)
	if err != nil {
		return err
	}
	base := windowsRuntimeDir()
	downloads := filepath.Join(base, "downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		return fmt.Errorf("create runtime folder: %w", err)
	}
	archive := filepath.Join(downloads, spec.Asset)
	title := "DXVK " + version
	err = wineDownload(ctx, client, spec, archive, func(got, total int64) {
		if total > 0 {
			setWindowsRuntimeStatus(fmt.Sprintf("Downloading %s · %s of %s", title, formatInstallBytes(got), formatInstallBytes(total)), "", true)
			return
		}
		setWindowsRuntimeStatus("Downloading "+title+" · "+formatInstallBytes(got), "", true)
	})
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	if md5sum != "" {
		if got, err := windowsFileMD5(archive); err != nil || !strings.EqualFold(got, md5sum) {
			return fmt.Errorf("%w: %s does not match its published checksum", errRelPackUnverified, spec.Asset)
		}
	}
	staging := filepath.Join(base, fmt.Sprintf("staging-%d", time.Now().UnixNano()))
	defer os.RemoveAll(staging)
	setWindowsRuntimeStatus("Unpacking "+title+"…", "", true)
	if err := extractRuntimeArchive(archive, staging); err != nil {
		return err
	}
	tree := findDXVKTree(runtimeArchiveRoot(staging))
	if tree == "" {
		return errors.New("the DXVK archive contains no x64/d3d11.dll")
	}
	installDir := filepath.Join(base, "dxvk")
	stage := installDir + relPackStageSuffix
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	for _, dir := range []string{"x64", "x32"} {
		src := filepath.Join(tree, dir)
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			if err := copyTree(src, filepath.Join(stage, dir)); err != nil {
				return err
			}
		}
	}
	if _, err := os.Stat(filepath.Join(tree, "x64")); err != nil {
		// A flat archive has the DLLs at its root instead of inside x64/.
		entries, readErr := os.ReadDir(tree)
		if readErr != nil {
			return readErr
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".dll") {
				continue
			}
			if err := copyArchiveFile(filepath.Join(tree, entry.Name()), filepath.Join(stage, entry.Name()), 0o644); err != nil {
				return err
			}
		}
	}
	if !dxvkBuild(stage) {
		_ = os.RemoveAll(stage)
		return fmt.Errorf("the DXVK archive did not install a usable build in %s", installDir)
	}
	marker := map[string]string{"version": version, "url": spec.URL, "installed_at": time.Now().UTC().Format(time.RFC3339)}
	if err := wineWriteJSON(filepath.Join(stage, dxvkMarkerName), marker); err != nil {
		_ = os.RemoveAll(stage)
		return err
	}
	if err := relPackSwap(stage, installDir); err != nil {
		_ = os.RemoveAll(stage)
		return err
	}
	logInfo("windowsapps", "DXVK %s installed in %s", version, installDir)
	return nil
}

// windowsFileMD5 is a file's MD5, for a release whose only published checksum
// is one.
func windowsFileMD5(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// findDXVKTree locates the directory that holds x64/d3d11.dll inside an
// extracted release. Upstream keeps the DLLs under <version>/, but a flat
// archive (d3d11.dll at the root) is accepted too.
func findDXVKTree(root string) string {
	best := ""
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if info, err := os.Stat(filepath.Join(path, "x64", "d3d11.dll")); err == nil && info.Size() > 0 {
			best = path
			return io.EOF
		}
		return nil
	})
	if best != "" {
		return best
	}
	if info, err := os.Stat(filepath.Join(root, "d3d11.dll")); err == nil && info.Size() > 0 {
		return root
	}
	return ""
}

// ── Provisioning ─────────────────────────────────────────────────────────

// windowsRuntimeStep is one runtime component a launch may need.
type windowsRuntimeStep int

const (
	windowsStepWine windowsRuntimeStep = iota
	windowsStepDisplay
	windowsStepDXVK
)

// key is the value the install state and the setup rows use for a step.
func (s windowsRuntimeStep) key() string {
	switch s {
	case windowsStepWine:
		return "wine"
	case windowsStepDisplay:
		return "weston"
	default:
		return "dxvk"
	}
}

func (s windowsRuntimeStep) label() string {
	switch s {
	case windowsStepWine:
		return windowsRuntimeLabel()
	case windowsStepDisplay:
		return "WestonPack"
	default:
		return "DXVK"
	}
}

// windowsRuntimeLabel names the runtime a setup brings to this device.
func windowsRuntimeLabel() string {
	switch {
	case wineHangoverWanted():
		return "Hangover"
	case winePackSupported():
		return "Box64 + Wine " + wineExpectedVersion()
	case wineNativeSupported():
		return "Wine " + wineNativeSpec.Version
	}
	return "Wine"
}

// windowsDisplayNeeded reports an ARM Linux device with no X11 or Wayland
// session and no WestonPack yet.
func windowsDisplayNeeded(status windowsRuntimeStatus) bool {
	if status.Display != "" || !winePackSupported() {
		return false
	}
	if strings.TrimSpace(os.Getenv("DISPLAY")) != "" || strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != "" {
		return false
	}
	src := findWestonPackSource()
	return src.Image == "" && src.Wrapper == ""
}

// windowsDXVKAutomatic reports whether the automatic chain installs DXVK: on an
// x86-64 PC whose Vulkan driver declares 1.3, never on a handheld.
func windowsDXVKAutomatic(status windowsRuntimeStatus) bool {
	if runtime.GOARCH != "amd64" || status.DXVK != "" || !status.Vulkan || IsTrimuiDevice() {
		return false
	}
	major, minor, known := vulkanICDVersion()
	return known && (major > 1 || (major == 1 && minor >= 3))
}

// windowsRuntimePlan lists what a Wine launch still needs, in install order:
// the runtime first, then the display bridge, then DXVK where it belongs in
// the automatic chain. An empty plan means nothing is left to install.
func windowsRuntimePlan(status windowsRuntimeStatus) []windowsRuntimeStep {
	if IsWindows() {
		return nil
	}
	var plan []windowsRuntimeStep
	wine := strings.TrimSpace(status.Wine)
	if (wine == "" || (status.Box64 == "" && needsBox64(wine))) && (windowsRuntimeInstallable() || wineHangoverWanted()) {
		plan = append(plan, windowsStepWine)
	}
	if windowsDisplayNeeded(status) {
		plan = append(plan, windowsStepDisplay)
	}
	if windowsDXVKAutomatic(status) {
		plan = append(plan, windowsStepDXVK)
	}
	return plan
}

// windowsPlanLabel names the missing pieces for the setup-page notice.
func windowsPlanLabel(plan []windowsRuntimeStep) string {
	names := make([]string, 0, len(plan))
	for _, step := range plan {
		names = append(names, step.label())
	}
	return strings.Join(names, " and ")
}

// The install steps and the status lookup are variables so the provisioning
// chain can be tested without a network or a 100 MB download.
var (
	windowsRuntimeStatusFor   = currentWindowsRuntimeStatus
	windowsInstallWineFn      = func() error { return windowsWithTimeout(windowsInstallRuntime) }
	windowsInstallDisplayFn   = installWestonRuntime
	windowsInstallDXVKFn      = installDXVKRuntime
	windowsPrepareFn          = func() error { return windowsWithTimeout(windowsPrepare) }
	windowsLaunchAfterInstall = launchWindowsApp
	windowsResolveVulkan      = resolveVulkan
	windowsDXVKRelease        = windowsDXVKReleaseAPI
)

// windowsWithTimeout runs one setup step with an hour's bound: downloads
// resume, but a step must not hold the installer forever.
func windowsWithTimeout(step func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	return step(ctx)
}

// windowsPrepare prepares the shared prefix, with its line on the installer
// status.
func windowsPrepare(ctx context.Context) error {
	return prepareWineRuntime(ctx, func(line string) { setWindowsRuntimeStatus(line, "", true) })
}

// startWindowsProvisioning installs every missing piece in the background,
// prepares the prefix, and launches pending when all of it succeeded. It
// returns an error when an install is already running or there is nothing to
// do.
func startWindowsProvisioning(pending *windowsAppEntry) error {
	if IsWindows() {
		return errors.New("Windows runs .exe files directly; nothing needs installing")
	}
	status := windowsRuntimeStatusFor()
	plan := windowsRuntimePlan(status)
	facts := currentWineRuntimeFacts()
	if facts.PrefixProblem != "" {
		// Known before anything is downloaded: there is nowhere to keep
		// Windows, so the runtime would be of no use.
		return errors.New(facts.PrefixProblem)
	}
	if len(plan) == 0 {
		switch {
		case pending != nil && facts.PrefixReady:
			return windowsLaunchAfterInstall(*pending)
		case facts.Flavor == "":
			return errors.New("JukaHub has no Windows runtime for this device")
		case pending == nil && facts.PrefixReady:
			return errors.New("the Windows runtime is already installed")
		}
	}
	if wineProgramsRunning() {
		return errors.New("close the running Windows app first")
	}
	target := "wine"
	if len(plan) > 0 {
		target = plan[0].key()
	}
	if !beginWindowsRuntimeInstall(target, "checking the Windows runtime") {
		return errors.New("a runtime install is already running")
	}
	if pending != nil {
		copy := *pending
		pending = &copy
	}
	go windowsProvisionLoop(pending)
	return nil
}

// windowsStepFailed records and announces a step that failed.
func windowsStepFailed(key, label string, err error) {
	reason := relPackFriendly(err)
	markWindowsRuntimeFailed(key, reason)
	setWindowsRuntimeStatus(label+" failed", reason, false)
	ShowToast(label+" failed: "+reason, ToastKindError)
}

// windowsProvisionLoop re-checks what is missing before every step, so an
// install that also provides a later piece (or a component someone added by
// hand meanwhile) is not installed twice. A step that reports success but
// leaves its component missing stops the chain instead of looping forever.
// The prefix is prepared last, so the app's first start is quick.
func windowsProvisionLoop(pending *windowsAppEntry) {
	defer func() {
		endWindowsRuntimeInstall()
		invalidateWindowsAppsCache()
	}()
	attempted := map[windowsRuntimeStep]bool{}
	for {
		status := windowsRuntimeStatusFor()
		plan := windowsRuntimePlan(status)
		if len(plan) == 0 {
			break
		}
		step := plan[0]
		if attempted[step] {
			windowsStepFailed(step.key(), step.label()+" install", errors.New("it is still missing after installing it"))
			return
		}
		attempted[step] = true
		setWindowsRuntimeInstallTarget(step.key())
		setWindowsRuntimeStatus("Installing "+step.label()+"…", "", true)
		var err error
		switch step {
		case windowsStepWine:
			err = windowsInstallWineFn()
		case windowsStepDisplay:
			err = windowsInstallDisplayFn()
		case windowsStepDXVK:
			err = windowsInstallDXVKFn()
		}
		invalidateWindowsAppsCache()
		if err != nil {
			windowsStepFailed(step.key(), step.label()+" install", err)
			return
		}
	}
	setWindowsRuntimeInstallTarget("wine")
	if err := windowsPrepareFn(); err != nil {
		windowsStepFailed("wine", "Preparing Windows", err)
		return
	}
	setWindowsRuntimeStatus("Windows runtime ready", "", false)
	invalidateWindowsAppsCache()
	if pending == nil {
		ShowToast("Windows runtime ready", ToastKindSuccess)
		return
	}
	if err := windowsLaunchAfterInstall(*pending); err != nil {
		showLaunchFailureDialog(pending.Title, err, "")
		return
	}
	ShowToast("Launching "+pending.Title, ToastKindSuccess)
}
