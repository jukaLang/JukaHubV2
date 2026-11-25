package main

// hakux_fit.go — hakuX on every TrimUI model, not only the Smart Pro.
//
// Juka, 2026-11-21: "GameCube, PS2, and XBOX should all work on all Trimui
// devices (even if performance is not great)". The hakuX package JukaHub
// installs (hakux.go, ToastedFrog2's port) is made for the original Smart Pro:
// its emulator draws a 1280x720 picture only. spruceUI builds the same port
// from the same sources (github.com/spruceUI/hakuX-spruce, release tag
// beta-main, asset hakux-aarch64.tar.gz) for every 64-bit handheld with a
// Vulkan driver: glibc 2.31, tuned for the Cortex-A53, linked against the
// device's SDL2, and drawing at the panel's own mode — the Brick's and Brick
// Pro's 1024x768 as well as the Smart Pro S's 1280x720 on its Mali GPU. Its
// archive holds hakux.aarch64 (the emulator alone), licences and the commits
// it was built from.
//
// So on every model but the Smart Pro, an install or update of the package is
// followed by this step: the newest beta-main archive is downloaded, checked
// against the SHA-256 digest GitHub states for it (an asset without one is
// refused: this binary runs as root), its hakux.aarch64 must be a 64-bit ARM
// executable, and it replaces the package's own emulator binary — found as the
// largest 64-bit ARM executable in the launcher folder whose name says hakux.
// The package's launcher, settings, swap and controls stay as they are. The
// provenance file records the replacement (ScreenBuild) and the new file's
// hash, so the next update replaces it like any file JukaHub laid down and
// runs this step again. When the step cannot run (offline, no digest, no
// binary found), the package's own binary stays and the log says why.
//
// An install made before this step existed kept the Smart Pro's binary: a
// tester's Brick Pro, whose hakuX JukaHub had installed on 2026-11-21, played
// the game's music behind a screen that never moved past the menu (relayed by
// Juka on 2026-11-22, and asked about again on 2026-11-24). Nothing fitted it
// later: the step ran only with an install or an update of the package, and
// with no newer release there was no update (X on the tile did fit it, behind
// "already the newest version"). So an Xbox game's start now fits such an
// install first (hakuxFitGate): the start waits under the install card while
// the build downloads, once, and the game starts when it is in. The emulator
// it replaces is looked for among the files the install recorded as well as
// in the launcher folder, wherever the package keeps it (hakuxFitTarget).

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// hakuxFitRepo, hakuxFitTag and hakuxFitAsset name spruceUI's build.
	hakuxFitRepo  = "spruceUI/hakuX-spruce"
	hakuxFitTag   = "beta-main"
	hakuxFitAsset = "hakux-aarch64.tar.gz"
	// hakuxFitEntry is the emulator inside that archive.
	hakuxFitEntry = "hakux.aarch64"
	// hakuxFitMaxBytes bounds the download and the unpacked binary.
	hakuxFitMaxBytes = int64(256 << 20)
	// hakuxFitSearchDepth bounds the search for the package's binary.
	hakuxFitSearchDepth = 4
)

// hakuxNeedsFit reports whether device plays with spruceUI's build: every
// model but the original Smart Pro, which the package is made for.
func hakuxNeedsFit(device DeviceModel) bool {
	return device != DeviceTrimuiSmartPro
}

// hakuxIsARM64Exec reports whether head (a file's first bytes) is a 64-bit,
// little-endian ARM executable or position-independent executable. Pure.
func hakuxIsARM64Exec(head []byte) bool {
	if len(head) < 20 || string(head[:4]) != "\x7fELF" || head[4] != 2 || head[5] != 1 {
		return false
	}
	typ := binary.LittleEndian.Uint16(head[16:18])
	machine := binary.LittleEndian.Uint16(head[18:20])
	return (typ == 2 || typ == 3) && machine == 183 // EM_AARCH64
}

// hakuxFileIsARM64Exec reads p's ELF header.
func hakuxFileIsARM64Exec(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 20)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return hakuxIsARM64Exec(head)
}

// hakuxFitTarget finds the package's emulator: the largest 64-bit ARM
// executable whose name contains "hakux" (not a library), among the files the
// install recorded in m (under the card at sdRoot: the package's Data/ folder
// as well as its Emus/ one) and in the launcher folder.
func hakuxFitTarget(sdRoot, folder string, m hakuxManifest) (string, error) {
	var best string
	var bestSize int64
	consider := func(p string, info fs.FileInfo) {
		name := strings.ToLower(info.Name())
		if !info.Mode().IsRegular() || !strings.Contains(name, "hakux") || strings.Contains(name, ".so") ||
			info.Size() <= bestSize || !hakuxFileIsARM64Exec(p) {
			return
		}
		best, bestSize = p, info.Size()
	}
	if strings.TrimSpace(sdRoot) != "" {
		for _, f := range m.Files {
			rel, err := hakuxCardRel(f.Path)
			if err != nil {
				continue
			}
			p := filepath.Join(sdRoot, hakuxOS(rel))
			if info, err := os.Lstat(p); err == nil {
				consider(p, info)
			}
		}
	}
	base := filepath.Clean(folder)
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if rel, _ := filepath.Rel(base, p); rel != "." && strings.Count(filepath.ToSlash(rel), "/") >= hakuxFitSearchDepth-1 {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil {
			consider(p, info)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if best == "" {
		return "", fmt.Errorf("no hakuX emulator binary found in %s or among the files its install recorded", folder)
	}
	return best, nil
}

// hakuxFitRelease reads spruceUI's beta-main release and picks its archive.
func hakuxFitRelease(ctx context.Context, client *http.Client) (emuWinRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", standaloneAPIBase, hakuxFitRepo, hakuxFitTag)
	data, err := fetchUpdateBytes(ctx, client, url, patchMaxMetadata)
	if err != nil {
		return emuWinRelease{}, err
	}
	var rel emuGitHubRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return emuWinRelease{}, errors.New("unreadable hakuX-spruce release metadata")
	}
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, hakuxFitAsset) && strings.TrimSpace(a.BrowserDownloadURL) != "" {
			return emuReleaseFrom(rel, a), nil
		}
	}
	return emuWinRelease{}, fmt.Errorf("the %s release of %s has no %s", hakuxFitTag, hakuxFitRepo, hakuxFitAsset)
}

// hakuxFitUnpack copies hakux.aarch64 out of a .tar.gz into dest, bounded.
func hakuxFitUnpack(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s is not a gzip archive: %w", filepath.Base(archive), err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s holds no %s", hakuxFitAsset, hakuxFitEntry)
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != hakuxFitEntry {
			continue
		}
		if h.Size <= 0 || h.Size > hakuxFitMaxBytes {
			return fmt.Errorf("%s in %s has an unlikely size (%d bytes)", hakuxFitEntry, hakuxFitAsset, h.Size)
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, h.Size))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if !hakuxFileIsARM64Exec(dest) {
			return fmt.Errorf("%s in %s is not a 64-bit ARM program", hakuxFitEntry, hakuxFitAsset)
		}
		return nil
	}
}

// hakuxFitApply replaces the package's emulator (folder is the launcher
// folder, on the card at p.SDRoot) with spruceUI's build and records it in m.
// progress, when not nil, gets the download's bytes.
func hakuxFitApply(ctx context.Context, client *http.Client, p hakuxPaths, folder string, m *hakuxManifest, progress func(done, total int64)) error {
	target, err := hakuxFitTarget(p.SDRoot, folder, *m)
	if err != nil {
		return err
	}
	rel, err := hakuxFitRelease(ctx, client)
	if err != nil {
		return err
	}
	if strings.TrimSpace(rel.Digest) == "" {
		return fmt.Errorf("GitHub states no checksum for %s, so it is not installed", hakuxFitAsset)
	}
	if rel.Size > hakuxFitMaxBytes {
		return fmt.Errorf("%s is %s, more than the %s limit", hakuxFitAsset, formatInstallBytes(rel.Size), formatInstallBytes(hakuxFitMaxBytes))
	}
	// Staged beside the install's own staging folder, on the same card, so the
	// last step is a rename.
	work := filepath.Join(p.SDRoot, hakuxOS(path.Join(p.State, "fit")))
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	archive := filepath.Join(work, hakuxFitAsset)
	sum, err := emuDownload(ctx, client, rel.URL, archive, hakuxFitMaxBytes, progress)
	if err != nil {
		return err
	}
	if err := emuDigestMatches(sum, rel.Digest); err != nil {
		return err
	}
	bin := filepath.Join(work, hakuxFitEntry)
	if err := hakuxFitUnpack(archive, bin); err != nil {
		return err
	}
	_ = os.Remove(archive)
	data, err := os.ReadFile(bin)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if err := ctx.Err(); err != nil {
		return err
	}
	mode := os.FileMode(0o755)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm() | 0o111
	}
	if err := os.Rename(bin, target); err != nil {
		return err
	}
	_ = os.Chmod(target, mode)
	if relPath, err := filepath.Rel(p.SDRoot, target); err == nil {
		relPath = filepath.ToSlash(relPath)
		for i := range m.Files {
			if strings.EqualFold(m.Files[i].Path, relPath) {
				m.Files[i].Size = int64(len(data))
				m.Files[i].SHA256 = hex.EncodeToString(digest[:])
			}
		}
	}
	m.ScreenBuild = hakuxFitRepo + " " + hakuxFitTag + " " + rel.Digest
	logInfo("emulators", "hakuX: %s replaced with %s's %s build (%s, %s) for this model's screen and GPU",
		target, hakuxFitRepo, hakuxFitTag, rel.AssetDate, rel.Digest)
	return nil
}
