package main

// upch_sideload.go — Install from file: the air-gapped update.
//
// A device that never sees the internet can still be updated. Copy the
// release archive (JukaHub-Trimui.zip or JukaHub-Windows.zip), or a .jkdelta
// made for the installed version, to the SD card, a USB stick or Downloads and
// pick it from Patch → Install from file. The file is checked on the device
// before anything is asked:
//
//   - a release.json beside it must verify against JukaHub's embedded key and
//     list the file's sha256: "signed by JukaHub". A release.json whose
//     signature fails stops the install; it is never traded for a checksum;
//   - otherwise a <file>.sha256 or a SHA256SUMS beside it must match:
//     "checksum verified";
//   - otherwise the file is "not verified", and the confirmation says so.
//
// An archive must hold a JukaHub built for this device and a delta must match
// the installed files, and either way the install is the staging, journal and
// helper path of every update.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"jukahub/JukaHub/internal/jdelta"
	"jukahub/JukaHub/internal/releasesign"
)

// upchSideloadMax bounds the list: a card is not searched for hundreds.
const upchSideloadMax = 64

// upchFolder is one place a copied update is looked for.
type upchFolder struct {
	Path  string
	Label string
}

// upchFileCandidate is one update file found on the device.
type upchFileCandidate struct {
	Path    string
	Name    string
	Folder  string
	Size    int64
	ModTime time.Time
	Kind    string
}

// upchFileVerdict is what checking a picked file found. Err blocks the
// install; Verified and Detail say how far the file could be trusted.
type upchFileVerdict struct {
	Path     string
	Name     string
	Kind     string
	Size     int64
	SHA256   string
	Verified string
	Detail   string
	Tag      string
	Date     string
	FromTag  string
	Older    bool
	Expired  bool
	Err      string
}

// upchSideloadKind classifies a file name for this platform: a JukaHub release
// archive or delta. An archive named for the other platform is left out of the
// list; a renamed one is still caught by the content check.
func upchSideloadKind(name, goos string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(lower, "jukahub") {
		return "", false
	}
	if strings.Contains(lower, "remoteplay") || strings.Contains(lower, "remote-play") {
		// JukaHub-RemotePlay-linux-arm64.zip holds the Remote Play helpers,
		// not a JukaHub (psremote_helpers_install.go installs it).
		return "", false
	}
	windowsName := strings.Contains(lower, "windows") || strings.Contains(lower, "win-x64") || strings.Contains(lower, "win64")
	deviceName := strings.Contains(lower, "trimui") || strings.Contains(lower, "linux")
	if (goos == "windows" && deviceName) || (goos != "windows" && windowsName) {
		return "", false
	}
	switch {
	case strings.HasSuffix(lower, jdelta.Extension):
		return upchKindDelta, true
	case strings.HasSuffix(lower, ".zip"), strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return upchKindArchive, true
	}
	return "", false
}

// upchSideloadFolders lists where a copied update is looked for: JukaHub's
// updates/ folder and its own folder, the user's Downloads and Desktop, the SD
// card (its root, Downloads and updates/) and USB sticks on the handheld, and
// every drive's root on Windows.
func upchSideloadFolders() []upchFolder {
	var out []upchFolder
	seen := map[string]bool{}
	add := func(path, label string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		key := filepath.Clean(path)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return
		}
		seen[key] = true
		out = append(out, upchFolder{Path: path, Label: label})
	}
	if dir, err := upchExeDir(); err == nil {
		add(filepath.Join(dir, "updates"), "JukaHub · updates")
		add(dir, "JukaHub folder")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		add(filepath.Join(home, "Downloads"), "Downloads")
		add(filepath.Join(home, "Desktop"), "Desktop")
	}
	if runtime.GOOS == "windows" {
		for _, drive := range filesDrives() {
			add(drive.Path, drive.Path)
			add(filepath.Join(drive.Path, "JukaHub"), filepath.Join(drive.Path, "JukaHub"))
		}
		return out
	}
	for _, root := range []string{"/mnt/SDCARD", "/mnt/sdcard"} {
		add(root, "SD card")
		add(filepath.Join(root, "updates"), "SD card · updates")
		add(filepath.Join(root, "Downloads"), "SD card · Downloads")
	}
	for _, pattern := range []string{"/media/*", "/run/media/*/*", "/mnt/usb*", "/mnt/udisk*"} {
		matches, _ := filepath.Glob(pattern)
		for _, p := range matches {
			add(p, "USB · "+filepath.Base(p))
		}
	}
	return out
}

// upchScanSideload lists the update files in those folders, newest first. It
// reads each folder once and never descends: a card can hold thousands of
// games. It runs off the render thread.
func upchScanSideload() []upchFileCandidate {
	var out []upchFileCandidate
	for _, folder := range upchSideloadFolders() {
		if len(out) >= upchSideloadMax {
			break
		}
		items, err := os.ReadDir(folder.Path)
		if err != nil {
			continue
		}
		for _, it := range items {
			if len(out) >= upchSideloadMax {
				break
			}
			if it.IsDir() {
				continue
			}
			kind, ok := upchSideloadKind(it.Name(), runtime.GOOS)
			if !ok {
				continue
			}
			info, err := it.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
				continue
			}
			out = append(out, upchFileCandidate{
				Path:    filepath.Join(folder.Path, it.Name()),
				Name:    it.Name(),
				Folder:  folder.Label,
				Size:    info.Size(),
				ModTime: info.ModTime(),
				Kind:    kind,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out
}

// upchReadSmall reads a sidecar file of at most 1 MiB.
func upchReadSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

// upchParseSums finds name's sha256 in checksum text: lines of "<hex>  <name>"
// or "<hex> *<name>" (sha256sum's format), or — when lone is set, for a
// <file>.sha256 — a line holding only the hash.
func upchParseSums(data []byte, name string, lone bool) (string, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !releasesign.IsSHA256Hex(fields[0]) {
			continue
		}
		if len(fields) == 1 {
			if lone {
				return strings.ToLower(fields[0]), true
			}
			continue
		}
		file := strings.TrimPrefix(strings.Join(fields[1:], " "), "*")
		if strings.EqualFold(filepath.Base(filepath.FromSlash(file)), name) {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

// upchSidecarChecksum looks for a checksum of the file at path beside it, and
// returns it with the name of the file it came from.
func upchSidecarChecksum(path string) (string, string, bool) {
	name := filepath.Base(path)
	if data, err := upchReadSmall(path + ".sha256"); err == nil {
		if sum, ok := upchParseSums(data, name, true); ok {
			return sum, name + ".sha256", true
		}
	}
	for _, sums := range []string{"SHA256SUMS", "SHA256SUMS.txt", "sha256sums.txt"} {
		if data, err := upchReadSmall(filepath.Join(filepath.Dir(path), sums)); err == nil {
			if sum, ok := upchParseSums(data, name, false); ok {
				return sum, sums, true
			}
		}
	}
	return "", "", false
}

// upchManifestTarget finds a file in a signed manifest: by name, or — for a
// renamed copy ("JukaHub-Trimui (1).zip") — by its hash, which the signature
// covers either way.
func upchManifestTarget(m *releasesign.Manifest, name, sum string) (releasesign.Target, bool) {
	if t, err := releasesign.FindTarget(m, name); err == nil {
		return t, true
	}
	for _, t := range m.Targets {
		if releasesign.IsSHA256Hex(t.SHA256) && t.Size > 0 && strings.EqualFold(t.SHA256, sum) {
			return t, true
		}
	}
	return releasesign.Target{}, false
}

// upchInspectArchive reads what the confirmation needs from an archive without
// unpacking it: the release stamp the release workflow writes into it, and
// whether it holds a JukaHub for this platform.
func upchInspectArchive(path string) (releaseInfo, bool, error) {
	want := "jukahub"
	if runtime.GOOS == "windows" {
		want = "jukahub.exe"
	}
	var info releaseInfo
	found := false
	visit := func(name string, size int64, open func() (io.ReadCloser, error)) error {
		base := strings.ToLower(filepath.Base(filepath.FromSlash(name)))
		if base == want {
			found = true
		}
		if base != releaseInfoFileName || info.Tag != "" || size > 64<<10 {
			return nil
		}
		rc, err := open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(rc, 64<<10))
		rc.Close()
		if err == nil {
			_ = json.Unmarshal(data, &info)
		}
		return nil
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		zr, err := zip.OpenReader(path)
		if err != nil {
			return info, false, err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			file := f
			if err := visit(f.Name, int64(f.UncompressedSize64), func() (io.ReadCloser, error) { return file.Open() }); err != nil {
				return info, false, err
			}
		}
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		fh, err := os.Open(path)
		if err != nil {
			return info, false, err
		}
		defer fh.Close()
		gz, err := gzip.NewReader(fh)
		if err != nil {
			return info, false, err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for entries := 0; entries < patchMaxEntries; entries++ {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return info, false, err
			}
			if hdr.Typeflag != tar.TypeReg {
				continue
			}
			if err := visit(hdr.Name, hdr.Size, func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }); err != nil {
				return info, false, err
			}
		}
	default:
		return info, false, errors.New("not a .zip or .tar.gz archive")
	}
	return info, found, nil
}

// upchVerifyFile checks a picked file on the device, before the user is asked
// to confirm. It hashes the whole file, so it runs off the render thread.
func upchVerifyFile(path string) upchFileVerdict {
	v := upchFileVerdict{Path: path, Name: filepath.Base(path), Kind: upchKindArchive}
	if strings.HasSuffix(strings.ToLower(v.Name), jdelta.Extension) {
		v.Kind = upchKindDelta
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		v.Err = "The file is gone. Look again and pick it from the list."
		return v
	}
	v.Size = info.Size()
	if v.Size <= 0 || v.Size > patchMaxDownload {
		v.Err = "This file is empty or too large to be a JukaHub update."
		return v
	}
	sum, err := SHA256File(path)
	if err != nil {
		v.Err = "The file could not be read: " + firstLine(err.Error())
		return v
	}
	v.SHA256 = sum

	// Trust, strongest first. A manifest that is there but does not verify
	// is the end of it: never fall back to a weaker check.
	if data, err := upchReadSmall(filepath.Join(filepath.Dir(path), releasesign.AssetName)); err == nil {
		m, expired, err := upchVerifyManifestLocal(data)
		if err != nil {
			v.Err = "The release.json next to this file failed its signature check (" + firstLine(err.Error()) + "). Nothing was installed."
			return v
		}
		if t, ok := upchManifestTarget(m, v.Name, sum); ok {
			if !strings.EqualFold(t.SHA256, sum) || t.Size != v.Size {
				v.Err = "This file does not match the signed release.json next to it: it is damaged or was changed."
				return v
			}
			v.Verified = upchVerifiedSigned
			v.Detail = "Signed by JukaHub (release.json)"
			v.Tag = strings.TrimSpace(m.Tag)
			v.Expired = expired
		}
	}
	if v.Verified == "" {
		if want, source, ok := upchSidecarChecksum(path); ok {
			if !strings.EqualFold(want, sum) {
				v.Err = "The checksum in " + source + " does not match: the file is damaged or was changed."
				return v
			}
			v.Verified = upchVerifiedChecksum
			v.Detail = "Checksum matches " + source + " (not signed)"
		}
	}
	if v.Verified == "" {
		v.Verified = upchVerifiedNone
		v.Detail = "Not verified: no release.json or checksum file next to it"
	}

	// What it holds.
	if v.Kind == upchKindDelta {
		b, err := jdelta.ReadBundleFile(path)
		if err != nil {
			v.Err = "This is not a JukaHub delta: " + firstLine(err.Error())
			return v
		}
		exeDir, err := upchExeDir()
		if err != nil {
			v.Err = firstLine(err.Error())
			return v
		}
		if err := jdeltaCheckBase(b, exeDir); err != nil {
			v.Err = "This delta was made for another version (" + upchTagLabel(b.FromTag, "") + "), not the one installed: " + err.Error()
			return v
		}
		v.FromTag = b.FromTag
		if v.Tag == "" {
			v.Tag = strings.TrimSpace(b.ToTag)
		}
	} else {
		stamp, hasBinary, err := upchInspectArchive(path)
		if err != nil {
			v.Err = "This archive cannot be read: " + firstLine(err.Error())
			return v
		}
		if !hasBinary {
			v.Err = "This archive holds no JukaHub for this device."
			return v
		}
		if v.Tag == "" {
			v.Tag = strings.TrimSpace(stamp.Tag)
		}
		v.Date = stamp.Date
	}
	if v.Tag == "" {
		// An archive without a stamp (a hand-made build) still gets a stable
		// name, so the restart can tell it was installed.
		v.Tag = "JukaHub-file-" + sum[:12]
	}
	if installedReleaseTagIs(v.Tag) {
		v.Older = true
	} else if t, ok := releasePublishedTime(&githubRelease{TagName: v.Tag, PublishedAt: v.Date}); ok {
		if installed, known := installedReleaseTime(); known && !t.After(installed) {
			v.Older = true
		}
	}
	return v
}

// upchInstallFileOp installs a checked file (a Patch operation). The file is
// hashed again first: it may have changed since it was checked.
func upchInstallFileOp(ctx context.Context, config *Config, v upchFileVerdict) error {
	if v.Err != "" {
		return errors.New(v.Err)
	}
	installBarPhase(installPhaseVerify)
	patchRowStatus(ComponentApp, StatusChecking)
	sum, err := SHA256File(v.Path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, v.SHA256) {
		return errors.New("the file changed after it was checked — pick it again")
	}
	work, err := upchStageWorkDir("file")
	if err != nil {
		return err
	}
	installBarPhase(installPhaseExtract)
	if v.Kind == upchKindDelta {
		exeDir, err := upchExeDir()
		if err != nil {
			return err
		}
		if err := jdeltaMaterialize(ctx, v.Path, exeDir, work); err != nil {
			return err
		}
	} else if err := extractSafe(ctx, v.Path, work, patchMaxEntries, patchMaxExpandBytes); err != nil {
		_ = os.RemoveAll(work)
		return fmt.Errorf("unpacking the update: %w", err)
	}
	rel := &githubRelease{TagName: v.Tag, PublishedAt: v.Date}
	if err := upchStageTree(ctx, rel, work, v.Name); err != nil {
		_ = os.RemoveAll(work)
		patchRowStatus(ComponentApp, StatusFailed)
		return err
	}
	upchNoteInstallSource(v.Tag, "file")
	return OpApplyStagedSwap(ctx, config)
}
