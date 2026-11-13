package main

// The optional Smart Pro voice is downloaded only when the user asks for it.
// Both upstream files are pinned by size and SHA-256. The archive is unpacked
// into a private staging directory; only Piper's English speech dependencies
// are copied, and the ready directory appears in one rename.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	ttsManagedDirName           = "juka-piper"
	ttsManagedMarker            = ".jukahub-voice"
	ttsPiperArchiveURL          = "https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_aarch64.tar.gz"
	ttsPiperArchiveSHA          = "fea0fd2d87c54dbc7078d0f878289f404bd4d6eea6e7444a77835d1537ab88eb"
	ttsPiperArchiveSize   int64 = 26004717
	ttsVoiceModelName           = "en_US-kristin-medium.onnx"
	ttsVoiceModelURL            = "https://huggingface.co/rhasspy/piper-voices/resolve/2f358b6a560e3a913ebf509b2f3344f20b367f94/en/en_US/kristin/medium/en_US-kristin-medium.onnx"
	ttsVoiceModelSHA            = "5849957f929cbf720c258f8458692d6103fff2f0e3d3b19c8259474bb06a18d4"
	ttsVoiceModelSize     int64 = 63531379
	ttsVoiceConfigURL           = "https://huggingface.co/rhasspy/piper-voices/resolve/2f358b6a560e3a913ebf509b2f3344f20b367f94/en/en_US/kristin/medium/en_US-kristin-medium.onnx.json"
	ttsVoiceConfigSHA           = "5681426d4aead22195de70531eeeddb46493cfaffc5764b2ea3db73428b651c"
	ttsVoiceConfigSize    int64 = 4968
	ttsVoiceDownloadSpace int64 = 160 << 20 // cache, unpacked engine, model, and a margin
)

var ttsVoiceInstall struct {
	sync.Mutex
	running bool
}

func ttsOfflineVoiceSupported() bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" && DetectDevice().IsTrimui()
}

func ttsManagedPath() string {
	return filepath.Join(P().RequiredDir(), ttsManagedDirName)
}

func ttsManagedVoice() (ttsLocalVoice, bool) {
	dir := ttsManagedPath()
	for _, name := range []string{"piper", "libespeak-ng.so.1", "libpiper_phonemize.so.1", "libonnxruntime.so.1.14.1", ttsVoiceModelName, ttsVoiceModelName + ".json", ttsManagedMarker} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return ttsLocalVoice{}, false
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "espeak-ng-data")); err != nil || !info.IsDir() {
		return ttsLocalVoice{}, false
	}
	return ttsLocalVoice{name: "Piper · Kristin", path: filepath.Join(dir, "piper"), kind: "piper", model: filepath.Join(dir, ttsVoiceModelName)}, true
}

func ttsOfflineVoiceValue(*Config) string {
	if _, ok := ttsManagedVoice(); ok {
		return "Ready"
	}
	ttsVoiceInstall.Lock()
	running := ttsVoiceInstall.running
	ttsVoiceInstall.Unlock()
	if running {
		return "Installing…"
	}
	return "Download"
}

func ttsOfflineVoiceDetail(*Config) string {
	if _, ok := ttsManagedVoice(); ok {
		return "Piper's Kristin voice is on the card · works offline with no daily limit"
	}
	return "Downloads Piper and Kristin (~90 MB); uses about 105 MB on the card"
}

func ttsInstallOfflineVoice(*Config) {
	if !ttsOfflineVoiceSupported() {
		return
	}
	if _, ok := ttsManagedVoice(); ok {
		ttsLocalRecheck()
		ttsStore(appConfig, ttsVoiceKey, ttsVoiceDevice)
		setToast("Offline voice selected — press Hear Juka to try it")
		return
	}
	if !IsOnline() {
		setToast("Connect to Wi-Fi to download the offline voice")
		return
	}
	if installBarActive() {
		setToast("Finish the current download, then try the offline voice")
		return
	}
	ttsVoiceInstall.Lock()
	if ttsVoiceInstall.running {
		ttsVoiceInstall.Unlock()
		return
	}
	ttsVoiceInstall.running = true
	ttsVoiceInstall.Unlock()
	run, ctx := installBarBeginRun("offline voice")
	go func() {
		defer func() {
			ttsVoiceInstall.Lock()
			ttsVoiceInstall.running = false
			ttsVoiceInstall.Unlock()
		}()
		ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		if err := ttsRunVoiceInstall(ctx, ttsManagedPath(), ttsInstallProgress(run)); err != nil {
			if errors.Is(err, context.Canceled) {
				installBarAbortRun(run)
			} else {
				logWarn("tts", "offline voice install: %v", err)
				installBarFailRun(run, installCardDetail(err.Error()))
			}
			return
		}
		ttsLocalRecheck()
		installBarDoneRun(run)
		tbxPost(func() {
			if appConfig != nil {
				ttsStore(appConfig, ttsVoiceKey, ttsVoiceDevice)
			}
			setToast("Offline voice ready — press Hear Juka to try it")
		})
	}()
}

// An old install must not update a newer install card's phase or byte count.
func ttsInstallProgress(run uint64) func(string, int64, int64) {
	return func(phase string, done, total int64) {
		installBarUpdateRun(run, phase, done, total)
	}
}

func ttsRunVoiceInstall(ctx context.Context, target string, progress func(string, int64, int64)) error {
	required := filepath.Dir(target)
	if err := os.MkdirAll(required, 0o755); err != nil {
		return err
	}
	if err := pkgCheckFreeSpace(required, ttsVoiceDownloadSpace); err != nil {
		return err
	}
	cache := filepath.Join(required, ".juka-voice-download")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return err
	}
	client := ttsVoiceClient()
	archive := filepath.Join(cache, "piper_linux_aarch64.tar.gz")
	progress(installPhaseDownload+" Piper", 0, ttsPiperArchiveSize)
	if err := ttsFetchPinned(ctx, client, ttsPiperArchiveURL, archive, ttsPiperArchiveSHA, ttsPiperArchiveSize, func(n int64) {
		progress(installPhaseDownload+" Piper", n, ttsPiperArchiveSize)
	}); err != nil {
		return fmt.Errorf("Piper download: %w", err)
	}
	stage, err := os.MkdirTemp(required, ".juka-voice-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	engine := filepath.Join(stage, ttsManagedDirName)
	if err := os.MkdirAll(engine, 0o755); err != nil {
		return err
	}
	progress(installPhaseExtract, 0, 0)
	if err := ttsExtractPiper(ctx, archive, engine); err != nil {
		return fmt.Errorf("Piper package: %w", err)
	}
	if problem := nativeLoadProblemWith(filepath.Join(engine, "piper"), []string{engine}); problem != "" {
		return fmt.Errorf("Piper cannot run on this firmware: %s", problem)
	}
	model := filepath.Join(cache, ttsVoiceModelName)
	progress(installPhaseDownload+" voice", 0, ttsVoiceModelSize)
	if err := ttsFetchPinned(ctx, client, ttsVoiceModelURL, model, ttsVoiceModelSHA, ttsVoiceModelSize, func(n int64) {
		progress(installPhaseDownload+" voice", n, ttsVoiceModelSize)
	}); err != nil {
		return fmt.Errorf("voice model download: %w", err)
	}
	config := filepath.Join(cache, ttsVoiceModelName+".json")
	if err := ttsFetchPinned(ctx, client, ttsVoiceConfigURL, config, ttsVoiceConfigSHA, ttsVoiceConfigSize, nil); err != nil {
		return fmt.Errorf("voice settings download: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	progress(installPhaseVerify, 0, 0)
	if err := os.Rename(model, filepath.Join(engine, ttsVoiceModelName)); err != nil {
		return err
	}
	if err := os.Rename(config, filepath.Join(engine, ttsVoiceModelName+".json")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(engine, ttsManagedMarker), []byte("Piper 2023.11.14-2; en_US-kristin-medium\n"), 0o644); err != nil {
		return err
	}
	if err := ttsCommitVoiceInstall(engine, target); err != nil {
		return err
	}
	_ = os.RemoveAll(cache)
	return nil
}

func ttsCommitVoiceInstall(stage, target string) error {
	backup := target + ".old"
	if _, err := os.Lstat(target); err == nil {
		if _, err := os.Stat(filepath.Join(target, ttsManagedMarker)); err != nil {
			return errors.New("an existing voice folder is not managed by JukaHub")
		}
		if _, err := os.Lstat(backup); err == nil {
			return errors.New("the voice's recovery folder already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(target, backup); err != nil {
			return err
		}
		if err := os.Rename(stage, target); err != nil {
			_ = os.Rename(backup, target)
			return err
		}
		_ = os.RemoveAll(backup)
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(stage, target)
}

func ttsVoiceClient() *http.Client {
	return &http.Client{
		Transport: ytTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 || req.URL.Scheme != "https" || req.URL.User != nil {
				return errors.New("voice download redirected outside HTTPS")
			}
			return nil
		},
	}
}

// ttsFetchPinned keeps a partial for a later retry. A complete file is renamed
// into place only after its exact byte count and digest match the pinned file.
func ttsFetchPinned(ctx context.Context, client *http.Client, raw, dest, expectedSHA string, expectedSize int64, progress func(int64)) error {
	if ttsFileMatches(dest, expectedSize, expectedSHA) {
		if progress != nil {
			progress(expectedSize)
		}
		return nil
	}
	_ = os.Remove(dest)
	part := dest + ".part"
	if ttsFileMatches(part, expectedSize, expectedSHA) {
		if err := os.Rename(part, dest); err != nil {
			return err
		}
		if progress != nil {
			progress(expectedSize)
		}
		return nil
	}
	h := sha256.New()
	var offset int64
	if info, err := os.Stat(part); err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() < expectedSize {
		f, err := os.Open(part)
		if err == nil {
			offset, err = io.Copy(h, f)
			_ = f.Close()
		}
		if err != nil {
			offset = 0
			h.Reset()
		}
	}
	if offset == 0 {
		_ = os.Remove(part)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub-Voice/"+versionString(nil))
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0
		h.Reset()
	case http.StatusPartialContent:
		if !brwWpeRangeStartsAt(resp.Header.Get("Content-Range"), offset) {
			return errors.New("download resumed at the wrong byte")
		}
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := out.Truncate(offset); err != nil {
		_ = out.Close()
		return err
	}
	if _, err := out.Seek(offset, io.SeekStart); err != nil {
		_ = out.Close()
		return err
	}
	if progress != nil {
		progress(offset)
	}
	buf := make([]byte, 128<<10)
	for offset < expectedSize {
		if err := ctx.Err(); err != nil {
			_ = out.Close()
			return err
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if int64(n) > expectedSize-offset {
				_ = out.Close()
				_ = os.Remove(part)
				return errors.New("download exceeds expected size")
			}
			if _, err := out.Write(buf[:n]); err != nil {
				_ = out.Close()
				return err
			}
			_, _ = h.Write(buf[:n])
			offset += int64(n)
			if progress != nil {
				progress(offset)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if offset == expectedSize {
					break // the final Read may return the last bytes with EOF
				}
				_ = out.Close()
				return fmt.Errorf("download ended at %d of %d bytes", offset, expectedSize)
			}
			_ = out.Close()
			return readErr
		}
	}
	var extra [1]byte
	if n, _ := resp.Body.Read(extra[:]); n != 0 {
		_ = out.Close()
		_ = os.Remove(part)
		return errors.New("download exceeds expected size")
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expectedSHA) {
		_ = os.Remove(part)
		return errors.New("download checksum did not match")
	}
	return os.Rename(part, dest)
}

func ttsFileMatches(file string, size int64, sha string) bool {
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return false
	}
	got, err := SHA256File(file)
	return err == nil && strings.EqualFold(got, sha)
}

// Piper's tarball contains links and many helpers we do not need. Materialize
// only regular files under espeak-ng-data and four exact ARM64 binaries, with
// the soname filenames that the dynamic loader expects on an exFAT SD card.
func ttsExtractPiper(ctx context.Context, archive, dest string) error {
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
	tr := tar.NewReader(gz)
	const maxEntries = 500
	const maxBytes int64 = 64 << 20
	var total int64
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if entries > maxEntries {
			return errors.New("Piper package has too many files")
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if path.Clean(name) != name || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
			return errors.New("Piper package has an unsafe filename")
		}
		if hdr.Typeflag != tar.TypeReg {
			// Archive links are skipped. The needed sonames are written directly.
			continue
		}
		var rel string
		switch name {
		case "piper/piper":
			rel = "piper"
		case "piper/libespeak-ng.so.1.52.0.1":
			rel = "libespeak-ng.so.1"
		case "piper/libpiper_phonemize.so.1.2.0":
			rel = "libpiper_phonemize.so.1"
		case "piper/libonnxruntime.so.1.14.1":
			rel = "libonnxruntime.so.1.14.1"
		default:
			if strings.HasPrefix(name, "piper/espeak-ng-data/") {
				rel = strings.TrimPrefix(name, "piper/")
			} else {
				continue
			}
		}
		if hdr.Size < 0 || hdr.Size > maxBytes-total {
			return errors.New("Piper package expands beyond its size limit")
		}
		total += hdr.Size
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(filepath.Separator)) {
			return errors.New("Piper package escapes its folder")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if rel == "piper" {
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		n, copyErr := io.CopyN(out, tr, hdr.Size)
		closeErr := out.Close()
		if copyErr != nil || n != hdr.Size || closeErr != nil {
			return errors.New("Piper package has an incomplete file")
		}
	}
	for _, name := range []string{"piper", "libespeak-ng.so.1", "libpiper_phonemize.so.1", "libonnxruntime.so.1.14.1", "espeak-ng-data/en_dict"} {
		info, err := os.Stat(filepath.Join(dest, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("Piper package is missing %s", name)
		}
	}
	return nil
}
