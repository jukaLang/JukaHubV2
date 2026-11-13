package main

// The Smart Pro's offline voice: downloaded once by itself after the startup
// tools (ffmpeg) are in place, or from Settings → Voice & AI. Piper's archive
// and the voice model are pinned by size and SHA-256. The voice's small
// settings file (en_US-kristin-medium.onnx.json) is not: it is plain data
// that only tells Piper the sample rate and phoneme table, so it is checked
// for exactly that shape instead (a pinned digest of it broke the install on
// 2026-11-12). The archive is unpacked into a private staging directory; only
// Piper's English speech dependencies are copied, and the ready directory
// appears in one rename.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	ttsVoiceConfigMax     int64 = 256 << 10 // the real file is about 5 KB
	ttsVoiceDownloadSpace int64 = 160 << 20 // cache, unpacked engine, model, and a margin
	// ttsVoiceUnsupportedMark, beside the voice folder, records that Piper
	// cannot load on this firmware, so startup stops downloading it again.
	ttsVoiceUnsupportedMark = ".juka-piper-unsupported"
)

var ttsVoiceInstall struct {
	sync.Mutex
	running bool
}

// ttsVoiceAutoTried is the one startup download per run (a reconnect after an
// offline start clears it, as it does for ffmpeg). ttsVoiceAutoHard is set
// when an automatic try failed for a reason a reconnect does not change (no
// space, a refused file), so waking from sleep does not show the same failure
// card again; the Settings row still tries. ttsToolsInstalling is the startup
// tools goroutines at work (netmon.go): the voice never starts in the gap
// between ffmpeg's card and the next tool's.
var (
	ttsVoiceAutoTried  atomic.Bool
	ttsVoiceAutoHard   atomic.Bool
	ttsToolsInstalling atomic.Int32
)

// errTTSVoiceUnsupported is Piper failing the loader check on this firmware.
var errTTSVoiceUnsupported = errors.New("Piper cannot run on this firmware")

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

// ttsVoiceInstalling reports whether the voice is downloading now.
func ttsVoiceInstalling() bool {
	ttsVoiceInstall.Lock()
	defer ttsVoiceInstall.Unlock()
	return ttsVoiceInstall.running
}

// ttsInstallOfflineVoice is the Settings row: install (or re-select) the voice
// and make it Juka's voice.
func ttsInstallOfflineVoice(*Config) {
	ttsStartVoiceInstall(false)
}

// ttsAutoInstallOfflineVoice is the startup download, called by the tools
// check once it has nothing of its own left to fetch (ffmpeg goes first). Any
// goroutine. It waits for another install card to finish, never toasts about
// itself unless it succeeds, and does not change the chosen voice: with no
// Groq key the offline voice is Juka's voice anyway (ttsVoiceOf), and with a
// key it takes over when Groq's voice cannot speak.
func ttsAutoInstallOfflineVoice() {
	if !ttsOfflineVoiceSupported() || ttsVoiceAutoHard.Load() || !ttsAutoOnline() {
		return
	}
	if _, ok := ttsManagedVoice(); ok {
		return
	}
	if req := P().RequiredDir(); req != "" {
		if _, err := os.Stat(filepath.Join(req, ttsVoiceUnsupportedMark)); err == nil {
			return
		}
	}
	if !ttsVoiceAutoTried.CompareAndSwap(false, true) {
		return
	}
	go ttsAutoVoiceWait()
}

// ttsAutoOnline is IsOnline with an address, as the boot tools check asks: a
// network monitor that has not probed yet reports online.
func ttsAutoOnline() bool {
	return IsOnline() && localIPv4() != ""
}

// ttsAutoVoiceWait waits until no install card and no startup tool download
// has been busy for ten seconds (two looks five seconds apart), then starts
// the voice on the main thread. It gives up for this run after an hour.
func ttsAutoVoiceWait() {
	quiet := 0
	for i := 0; i < 720 && quiet < 2; i++ {
		time.Sleep(5 * time.Second)
		if installBarActive() || ttsToolsInstalling.Load() > 0 {
			quiet = 0
		} else {
			quiet++
		}
	}
	if quiet < 2 {
		ttsVoiceAutoTried.Store(false)
		return
	}
	tbxPost(func() {
		if !ttsStartVoiceInstall(true) {
			// Something got in first (a card, Wi-Fi gone): look again later.
			if ttsAutoOnline() {
				go ttsAutoVoiceWait()
			} else {
				ttsVoiceAutoTried.Store(false)
			}
		}
	})
}

// ttsVoiceAutoRetry lets a reconnect after an offline start try again.
func ttsVoiceAutoRetry() {
	if !ttsVoiceAutoHard.Load() {
		ttsVoiceAutoTried.Store(false)
	}
}

// ttsVoiceSoftFailure reports whether an install error is the network's (a
// reconnect may fix it) rather than the card's or the files'.
func ttsVoiceSoftFailure(err error) bool {
	var ue *url.Error
	var ne net.Error
	if errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "download ended at") || strings.Contains(msg, "HTTP 5") || strings.Contains(msg, "HTTP 429") ||
		strings.Contains(msg, "connection") || strings.Contains(msg, "resumed at the wrong byte")
}

// ttsStartVoiceInstall starts the download. Main thread. It reports whether
// the voice is installed, installing or now starting (false: Wi-Fi or another
// card kept it from starting).
func ttsStartVoiceInstall(auto bool) bool {
	if !ttsOfflineVoiceSupported() {
		return true
	}
	if _, ok := ttsManagedVoice(); ok {
		if auto {
			return true
		}
		ttsLocalRecheck()
		ttsStore(appConfig, ttsVoiceKey, ttsVoiceDevice)
		setToast("Offline voice selected — press Hear Juka to try it")
		return true
	}
	if !IsOnline() {
		if !auto {
			setToast("Connect to Wi-Fi to download the offline voice")
		}
		return false
	}
	if installBarActive() || auto && ttsToolsInstalling.Load() > 0 {
		if !auto {
			setToast("Finish the current download, then try the offline voice")
		}
		return false
	}
	ttsVoiceInstall.Lock()
	if ttsVoiceInstall.running {
		ttsVoiceInstall.Unlock()
		return true
	}
	ttsVoiceInstall.running = true
	ttsVoiceInstall.Unlock()
	if !auto {
		// The user asked: a firmware that refused Piper before is checked
		// again, and so is a failure that stopped the startup download.
		ttsVoiceAutoHard.Store(false)
		if req := P().RequiredDir(); req != "" {
			_ = os.Remove(filepath.Join(req, ttsVoiceUnsupportedMark))
		}
	}
	run, ctx := installBarBeginRun("offline voice")
	go func() {
		defer func() {
			ttsVoiceInstall.Lock()
			ttsVoiceInstall.running = false
			ttsVoiceInstall.Unlock()
		}()
		ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		target := ttsManagedPath()
		if err := ttsRunVoiceInstall(ctx, target, ttsInstallProgress(run)); err != nil {
			switch {
			case errors.Is(err, context.Canceled):
				installBarAbortRun(run)
				if auto {
					// Another install took the card (it cancels the run it
					// replaces): wait for it and go on. A cancel the user
					// pressed leaves the next try to a reconnect or Settings.
					if installBarActive() {
						go ttsAutoVoiceWait()
					} else {
						ttsVoiceAutoTried.Store(false)
					}
				}
			default:
				logWarn("tts", "offline voice install (auto=%v): %v", auto, err)
				if errors.Is(err, errTTSVoiceUnsupported) {
					_ = os.WriteFile(filepath.Join(filepath.Dir(target), ttsVoiceUnsupportedMark), []byte(err.Error()+"\n"), 0o644)
				}
				if auto && !ttsVoiceSoftFailure(err) {
					ttsVoiceAutoHard.Store(true)
				}
				installBarFailRun(run, installCardDetail(err.Error()))
			}
			return
		}
		ttsLocalRecheck()
		installBarDoneRun(run)
		tbxPost(func() {
			if auto {
				setToast("Offline voice ready — Juka can talk without Wi-Fi")
				return
			}
			if appConfig != nil {
				ttsStore(appConfig, ttsVoiceKey, ttsVoiceDevice)
			}
			setToast("Offline voice ready — press Hear Juka to try it")
		})
	}()
	return true
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
		return fmt.Errorf("%w: %s", errTTSVoiceUnsupported, problem)
	}
	model := filepath.Join(cache, ttsVoiceModelName)
	progress(installPhaseDownload+" voice", 0, ttsVoiceModelSize)
	if err := ttsFetchPinned(ctx, client, ttsVoiceModelURL, model, ttsVoiceModelSHA, ttsVoiceModelSize, func(n int64) {
		progress(installPhaseDownload+" voice", n, ttsVoiceModelSize)
	}); err != nil {
		return fmt.Errorf("voice model download: %w", err)
	}
	config := filepath.Join(cache, ttsVoiceModelName+".json")
	if err := ttsFetchVoiceConfig(ctx, client, ttsVoiceConfigURL, config); err != nil {
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

// ttsFetchVoiceConfig downloads the voice's settings file and keeps it only
// when it has the shape Piper reads (ttsCheckVoiceConfig). A good copy from an
// earlier try is reused.
func ttsFetchVoiceConfig(ctx context.Context, client *http.Client, raw, dest string) error {
	if data, err := os.ReadFile(dest); err == nil && ttsCheckVoiceConfig(data) == nil {
		return nil
	}
	_ = os.Remove(dest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub-Voice/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, ttsVoiceConfigMax+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > ttsVoiceConfigMax {
		return errors.New("the voice's settings file is too large")
	}
	if err := ttsCheckVoiceConfig(data); err != nil {
		return err
	}
	part := dest + ".part"
	if err := os.WriteFile(part, data, 0o600); err != nil {
		return err
	}
	return os.Rename(part, dest)
}

// ttsCheckVoiceConfig accepts a Piper voice settings file for an English
// voice: a sample rate, an espeak voice, a symbol count and a phoneme table.
// Pure.
func ttsCheckVoiceConfig(data []byte) error {
	var cfg struct {
		Audio struct {
			SampleRate int `json:"sample_rate"`
		} `json:"audio"`
		Espeak struct {
			Voice string `json:"voice"`
		} `json:"espeak"`
		NumSymbols   int                        `json:"num_symbols"`
		PhonemeIDMap map[string]json.RawMessage `json:"phoneme_id_map"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return errors.New("the voice's settings file is not valid JSON")
	}
	switch {
	case cfg.Audio.SampleRate < 8000 || cfg.Audio.SampleRate > 48000:
		return errors.New("the voice's settings file has no usable sample rate")
	case !strings.HasPrefix(strings.ToLower(cfg.Espeak.Voice), "en"):
		return errors.New("the voice's settings file is not for an English voice")
	case cfg.NumSymbols <= 0 || len(cfg.PhonemeIDMap) == 0:
		return errors.New("the voice's settings file has no phoneme table")
	}
	return nil
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
	// espeak-ng needs its phoneme tables as well as the English dictionary; a
	// package whose tables were links would fail here, not on the first answer.
	for _, name := range []string{"piper", "libespeak-ng.so.1", "libpiper_phonemize.so.1", "libonnxruntime.so.1.14.1",
		"espeak-ng-data/en_dict", "espeak-ng-data/phontab", "espeak-ng-data/phondata", "espeak-ng-data/phonindex", "espeak-ng-data/intonations"} {
		info, err := os.Stat(filepath.Join(dest, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("Piper package is missing %s", name)
		}
	}
	return nil
}
