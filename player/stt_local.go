package main

// Offline speech on the Smart Pro: fast English recognition with Vosk and a
// multilingual Whisper fallback, downloaded once into <data>/juka-whisper
// (stt_install.go; older packages bundled it beside the binary, still used). WAV input and transcript output travel
// through process pipes, so recorded audio never needs a temporary file.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

const (
	sttDirName = "juka-whisper"
	// sttLightModel is Whisper tiny with 8-bit weights (about 44 MB), which
	// build-stt.sh makes from the pinned model: lighter to load and faster on
	// the A53. Packages built before it carry the 78 MB sttModelName instead;
	// they keep working, and stt_install.go replaces them once.
	sttLightModel   = "ggml-tiny-q8_0.bin"
	sttModelName    = "ggml-tiny.bin"
	sttModelSize    = 77691713
	sttFastModel    = "vosk-model-small-en-us-0.15"
	sttTimeout      = 120 * time.Second
	sttGroqWait     = 15 * time.Second // leave time for local recognition if Groq stalls
	sttMinAvailable = 384 << 20        // model and working memory, with room for JukaHub
	sttLightAvail   = 320 << 20        // the same with the 8-bit model, 34 MB smaller
	sttModeKey      = "speechRecognition"
	sttModeAuto     = "auto"
	sttModeOffline  = "offline"
	sttModeGroq     = "groq"
)

// sttQuickContextBytes is the longest clip (14 s of 16 kHz mono) a quick
// Whisper run encodes with a shortened audio context.
const sttQuickContextBytes = 14 * 16000 * 2

var errSTTUnavailable = errors.New("offline speech recognition is not installed")
var errSTTMemory = errors.New("not enough free memory for offline speech recognition")
var errSTTGroqKey = errors.New("Groq speech recognition needs an API key")
var errSTTGroqOffline = errors.New("Groq speech recognition needs Wi-Fi")

// sttModeSnap is the recognition setting as the main thread last read it
// (sttModeRemember, every frame from hjTick). Transcription goroutines read it
// here: config.Variables.Custom is not safe to read off the main thread.
// 0: not read yet, then 1 + the index in sttModes.
var sttModeSnap atomic.Int32

var sttModes = [...]string{sttModeAuto, sttModeOffline, sttModeGroq}

// sttModeRemember reads the setting on the main thread for sttModeNow. Every
// frame, so it does not allocate (sttModeOf formats the value).
func sttModeRemember(config *Config) {
	n := int32(1)
	if config != nil {
		switch v, _ := config.Variables.Custom[sttModeKey].(string); v {
		case sttModeOffline:
			n = 2
		case sttModeGroq:
			n = 3
		}
	}
	sttModeSnap.Store(n)
}

// sttModeNow is the setting for any goroutine.
func sttModeNow() string {
	if n := sttModeSnap.Load(); n > 0 && int(n) <= len(sttModes) {
		return sttModes[n-1]
	}
	return sttModeOf(appConfig) // before the first frame: nothing else runs yet
}

// Auto is the default, including for configs saved before this setting existed.
func sttModeOf(config *Config) string {
	if config != nil {
		switch customStringValue(config.Variables.Custom, sttModeKey) {
		case sttModeOffline:
			return sttModeOffline
		case sttModeGroq:
			return sttModeGroq
		}
	}
	return sttModeAuto
}

func sttSupported() bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" && DetectDevice() == DeviceTrimuiSmartPro
}

func sttBundledDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), sttDirName)
}

func sttDownloadedDir() string {
	data, err := P().DataDir()
	if err != nil || data == "" {
		return ""
	}
	return filepath.Join(data, sttDirName)
}

func sttFilesIn(dir string) (string, string, bool) {
	if dir == "" {
		return "", "", false
	}
	bin := filepath.Join(dir, "whisper-cli")
	b, bErr := os.Stat(bin)
	if bErr != nil || !b.Mode().IsRegular() || b.Mode().Perm()&0o111 == 0 {
		return bin, "", false
	}
	// The package's SHA256SUMS fixed the 8-bit model's bytes when it was
	// installed; a size in range is enough to tell it is all there.
	light := filepath.Join(dir, sttLightModel)
	if m, err := os.Stat(light); err == nil && m.Mode().IsRegular() && m.Size() > 20<<20 && m.Size() < 60<<20 {
		return bin, light, true
	}
	model := filepath.Join(dir, sttModelName)
	m, mErr := os.Stat(model)
	return bin, model, mErr == nil && m.Mode().IsRegular() && m.Size() == sttModelSize
}

// sttLightInstalled reports whether the offline speech in use has the 8-bit
// Whisper model (false: none, or an older package with the full one).
func sttLightInstalled() bool {
	_, model := sttLocalFiles()
	return filepath.Base(model) == sttLightModel
}

func sttLocalFiles() (string, string) {
	if bin, model, ok := sttFilesIn(sttDownloadedDir()); ok {
		return bin, model
	}
	if bin, model, ok := sttFilesIn(sttBundledDir()); ok {
		return bin, model
	}
	return "", ""
}

func sttFastFilesIn(dir string) (string, string, bool) {
	if dir == "" {
		return "", "", false
	}
	bin, model := filepath.Join(dir, "vosk-cli"), filepath.Join(dir, sttFastModel)
	for _, name := range []string{bin, filepath.Join(dir, "libvosk.so"),
		filepath.Join(model, "am", "final.mdl"), filepath.Join(model, "conf", "model.conf"),
		filepath.Join(model, "graph", "HCLr.fst")} {
		info, err := os.Stat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "", "", false
		}
	}
	info, err := os.Stat(bin)
	return bin, model, err == nil && info.Mode().Perm()&0o111 != 0
}

func sttFastFiles() (string, string) {
	if bin, model, ok := sttFastFilesIn(sttDownloadedDir()); ok {
		return bin, model
	}
	if bin, model, ok := sttFastFilesIn(sttBundledDir()); ok {
		return bin, model
	}
	return "", ""
}

func sttLocalReady() bool {
	if !sttSupported() {
		return false
	}
	bin, _ := sttLocalFiles()
	return bin != ""
}

func sttCanListen() bool {
	return sttCanListenFor(appConfig)
}

func sttCanListenFor(config *Config) bool {
	return sttListenAvailable(sttModeOf(config), sttLocalReady(), groqAPIKey(), IsOnline() || netReconnecting())
}

func sttListenAvailable(mode string, local bool, key string, online bool) bool {
	cloud := strings.TrimSpace(key) != "" && online
	switch mode {
	case sttModeOffline:
		return local
	case sttModeGroq:
		return cloud
	default:
		return local || cloud
	}
}

// Preview requests run while a Chat microphone is open. Keep the choice here
// so Offline mode cannot accidentally send an interim clip to Groq.
func sttPreviewChoice(mode, key string, online, local bool) (cloud, localFirst, localFallback bool) {
	cloud = mode != sttModeOffline && strings.TrimSpace(key) != "" && online
	localFirst = mode != sttModeGroq && local && !cloud
	localFallback = mode == sttModeAuto && local && cloud
	return
}

// sttWithFallback follows the chosen provider. Automatic tries Groq first and
// uses the local model when the key, connection or request fails. A canceled
// request never starts another recognizer.
func sttWithFallback(ctx context.Context, key string, wav []byte, lang, prompt string, cloud func(context.Context, string, []byte) (string, error)) (string, error) {
	return sttWithFallbackLocal(ctx, key, wav, cloud, func(ctx context.Context, wav []byte) (string, error) {
		return sttLocalTranscribe(ctx, wav, lang, prompt)
	})
}

// sttWithFallbackLocal is sttWithFallback with its own offline step (the
// orb's streamed words, stt_stream.go).
func sttWithFallbackLocal(ctx context.Context, key string, wav []byte, cloud func(context.Context, string, []byte) (string, error),
	localTranscribe func(context.Context, []byte) (string, error)) (string, error) {
	mode := sttModeNow() // a goroutine: not the settings map itself
	local := sttLocalReady()
	if mode != sttModeOffline && (!local || mode == sttModeGroq) && netReconnecting() && strings.TrimSpace(key) != "" {
		netWaitOnline(ctx, netWakeWaitMax)
	}
	if mode == sttModeAuto && local {
		upstream := cloud
		cloud = func(ctx context.Context, key string, wav []byte) (string, error) {
			cloudCtx, cancel := context.WithTimeout(ctx, sttGroqWait)
			defer cancel()
			return upstream(cloudCtx, key, wav)
		}
	}
	return sttTranscribeChoice(ctx, mode, key, wav, IsOnline(), local, cloud, localTranscribe)
}

func sttTranscribeChoice(ctx context.Context, mode, key string, wav []byte, online, local bool,
	cloud func(context.Context, string, []byte) (string, error),
	localTranscribe func(context.Context, []byte) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	if mode == sttModeOffline {
		if local {
			return localTranscribe(ctx, wav)
		}
		return "", errSTTUnavailable
	}
	if mode == sttModeGroq {
		if key == "" {
			return "", errSTTGroqKey
		}
		if !online {
			return "", errSTTGroqOffline
		}
		return cloud(ctx, key, wav)
	}
	if key != "" && (online || !local) {
		text, err := cloud(ctx, key, wav)
		if err == nil {
			return text, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if !local {
			return "", err
		}
		logWarn("voice", "Groq transcription failed; trying offline recognition: %v", err)
	}
	if local {
		return localTranscribe(ctx, wav)
	}
	return "", errSTTUnavailable
}

func sttVoiceTypingTranscribe(ctx context.Context, key string, wav []byte, lang string) (string, error) {
	return sttWithFallback(ctx, key, wav, lang, "", func(ctx context.Context, key string, wav []byte) (string, error) {
		return vkTranscribe(ctx, key, wav, lang)
	})
}

func sttCommandTranscribe(ctx context.Context, key string, wav []byte, prompt string) (string, error) {
	return sttWithFallback(ctx, key, wav, "en", prompt, func(ctx context.Context, key string, wav []byte) (string, error) {
		return voiceTranscribe(ctx, key, wav, prompt)
	})
}

// sttCommandTranscribeStream is sttCommandTranscribe for a clip the warm
// recognizer decoded while it was recorded (stt_stream.go): the offline step
// takes the stream's words, and only a stream that failed or fell behind runs
// the one-shot recognizer on the whole clip. st may be nil; Finish has run.
func sttCommandTranscribeStream(ctx context.Context, key string, wav []byte, prompt string, st *sttStream) (string, error) {
	if st == nil {
		return sttCommandTranscribe(ctx, key, wav, prompt)
	}
	// Groq may answer first: the stream then stops decoding.
	defer st.Abandon()
	return sttWithFallbackLocal(ctx, key, wav, func(ctx context.Context, key string, wav []byte) (string, error) {
		return voiceTranscribe(ctx, key, wav, prompt)
	}, func(ctx context.Context, wav []byte) (string, error) {
		started := time.Now()
		text, err := st.Result(ctx)
		if err == nil {
			logDebug("voice", "streamed offline recognition finished %s after the clip", time.Since(started).Round(time.Millisecond))
			return text, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		logInfo("voice", "streamed offline recognition failed (%v); recognizing the whole clip", err)
		// The warm server just failed this clip: straight to the one-shot path.
		return sttLocalTranscribeRun(ctx, wav, "en", prompt, prompt != "", false)
	})
}

func sttLocalTranscribe(ctx context.Context, wav []byte, lang, prompt string) (string, error) {
	return sttLocalTranscribeSearch(ctx, wav, lang, prompt, prompt != "")
}

// A provisional Chat preview favors speed. The final transcript still runs
// through sttLocalTranscribe with the normal search.
func sttLocalPreviewTranscribe(ctx context.Context, wav []byte, lang string) (string, error) {
	return sttLocalTranscribeSearch(ctx, wav, lang, "", true)
}

func sttLocalTranscribeSearch(ctx context.Context, wav []byte, lang, prompt string, quick bool) (string, error) {
	return sttLocalTranscribeRun(ctx, wav, lang, prompt, quick, true)
}

// sttLocalTranscribeRun recognizes a whole clip. English goes to Vosk: the
// warm server when serve allows (a model already in memory answers at once;
// stt_stream.go), else a one-shot vosk-cli; Whisper takes the rest and any
// Vosk failure.
func sttLocalTranscribeRun(ctx context.Context, wav []byte, lang, prompt string, quick, serve bool) (string, error) {
	if !sttLocalReady() {
		return "", errSTTUnavailable
	}
	if lang == "en" {
		if serve {
			text, err := sttServeTranscribe(ctx, wav)
			if err == nil || ctx.Err() != nil {
				return text, err
			}
			if !errors.Is(err, errSTTNoServe) {
				logInfo("voice", "warm offline recognition failed (%v); starting the recognizer for this clip", err)
			}
		}
		if bin, model := sttFastFiles(); bin != "" {
			// Vosk's small model needs what the warm server needs, not
			// Whisper's larger allowance.
			if err := sttCheckMemoryFor(ctx, sttServeStartAvail); err != nil {
				return "", err
			}
			text, err := sttRunFast(ctx, bin, model, wav)
			if err == nil || ctx.Err() != nil {
				return text, err
			}
			logWarn("voice", "fast offline recognition failed; trying Whisper: %v", err)
		}
	}
	bin, model := sttLocalFiles()
	need := int64(sttMinAvailable)
	if filepath.Base(model) == sttLightModel {
		need = sttLightAvail
	}
	if err := sttCheckMemoryFor(ctx, need); err != nil {
		return "", err
	}
	text, err := sttRunLocal(ctx, bin, model, wav, lang, prompt, quick)
	if err != nil && ctx.Err() == nil {
		logWarn("voice", "offline speech recognition failed: %v", err)
	}
	return text, err
}

func sttRunFast(ctx context.Context, bin, model string, wav []byte) (string, error) {
	started := time.Now()
	cmd := exec.CommandContext(ctx, bin, "-m", model, "-f", "-")
	cmd.Stdin = bytes.NewReader(wav)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("fast offline recognition failed: %w", err)
	}
	if out.Len() > 16<<10 {
		return "", errors.New("offline speech transcript is too long")
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		return "", fmt.Errorf("fast offline transcript: %w", err)
	}
	logDebug("voice", "fast offline recognition took %s (clip=%d bytes)", time.Since(started).Round(time.Millisecond), len(wav))
	return strings.TrimSpace(result.Text), nil
}

// sttCheckMemoryFor makes room when less than need bytes are free (memfree.go:
// caches, idle helpers and, with no game on screen, apps left running in the
// background) and reports errSTTMemory only when that was not enough. Without
// /proc/meminfo (unusual firmware) the process itself decides. Off the main
// thread: the free-up waits.
func sttCheckMemoryFor(ctx context.Context, need int64) error {
	if !memFreeEnsure(ctx, uint64(need>>20), memFreeSpeech, "offline speech") {
		if err := ctx.Err(); err != nil {
			return err
		}
		return errSTTMemory
	}
	return nil
}

func sttRunLocal(ctx context.Context, bin, model string, wav []byte, lang, prompt string, quick bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	started := time.Now()
	if lang == "" {
		lang = "auto"
	} else if lang == "nb" {
		lang = "no" // Whisper names Norwegian "no"; the keyboard layout uses "nb".
	}
	args := sttLocalArgs(model, lang, prompt, quick)
	if quick && len(wav) <= 44+sttQuickContextBytes {
		// Whisper encodes a full 30 s window (1500 frames) by default, most of
		// the wait on the A53. A clip that fits 768 frames (15.36 s) is encoded
		// in about half the time; dictation keeps the full window.
		args = append(args, "-ac", "768")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = bytes.NewReader(wav)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil // CLI diagnostics can contain recognized speech
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("offline speech recognition failed: %w", err)
	}
	logDebug("voice", "offline recognition took %s (fast=%v, clip=%d bytes)", time.Since(started).Round(time.Millisecond), quick, len(wav))
	if out.Len() > 16<<10 {
		return "", errors.New("offline speech transcript is too long")
	}
	return strings.TrimSpace(out.String()), nil
}

func sttLocalArgs(model, lang, prompt string, quick bool) []string {
	// The Smart Pro has four A53 cores: three workers leave one for the
	// interface (dictation used two, and waited longer for it).
	args := []string{"-m", model, "-f", "-", "-t", "3", "-l", lang, "-ng", "-np", "-nt", "-otxt", "-of", "-"}
	if quick {
		// A short command: greedy decoding (one guess per word, not three)
		// and -nf, no temperature fallback — noise or silence otherwise makes
		// the tiny model decode the clip up to five more times.
		args = append(args, "-bs", "1", "-bo", "1", "-nf")
	} else {
		// Dictation keeps the fallback but searches two ways, not five.
		args = append(args, "-bs", "2", "-bo", "2")
	}
	if prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	return args
}
