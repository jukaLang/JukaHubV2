package main

// The TrimUI package carries whisper.cpp's CPU-only CLI and its multilingual
// tiny model. WAV input and transcript output travel through process pipes, so
// recognized speech and recorded audio never need a temporary file.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	sttDirName      = "juka-whisper"
	sttModelName    = "ggml-tiny.bin"
	sttModelSize    = 77691713
	sttTimeout      = 120 * time.Second
	sttMinAvailable = 384 << 20 // model and working memory, with room for JukaHub
)

var errSTTUnavailable = errors.New("offline speech recognition is not installed")
var errSTTMemory = errors.New("not enough free memory for offline speech recognition")

func sttLocalFiles() (string, string) {
	dir := filepath.Join(P().RequiredDir(), sttDirName)
	return filepath.Join(dir, "whisper-cli"), filepath.Join(dir, sttModelName)
}

func sttLocalReady() bool {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || DetectDevice() != DeviceTrimuiSmartPro {
		return false
	}
	bin, model := sttLocalFiles()
	b, bErr := os.Stat(bin)
	m, mErr := os.Stat(model)
	return bErr == nil && b.Mode().IsRegular() && b.Mode().Perm()&0o111 != 0 &&
		mErr == nil && m.Mode().IsRegular() && m.Size() == sttModelSize
}

func sttCanListen() bool {
	return sttLocalReady() || strings.TrimSpace(groqAPIKey()) != "" && (IsOnline() || netReconnecting())
}

// sttWithFallback sends to Groq first when reachable. Missing keys, offline
// Wi-Fi and failed Groq requests use the local model when it is installed.
// A canceled request never starts another recognizer.
func sttWithFallback(ctx context.Context, key string, wav []byte, lang, prompt string, cloud func(context.Context, string, []byte) (string, error)) (string, error) {
	local := sttLocalReady()
	if !local && netReconnecting() && strings.TrimSpace(key) != "" {
		netWaitOnline(ctx, netWakeWaitMax)
	}
	return sttTranscribeChoice(ctx, key, wav, IsOnline(), local, cloud,
		func(ctx context.Context, wav []byte) (string, error) {
			return sttLocalTranscribe(ctx, wav, lang, prompt)
		})
}

func sttTranscribeChoice(ctx context.Context, key string, wav []byte, online, local bool,
	cloud func(context.Context, string, []byte) (string, error),
	localTranscribe func(context.Context, []byte) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
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

func sttLocalTranscribe(ctx context.Context, wav []byte, lang, prompt string) (string, error) {
	if !sttLocalReady() {
		return "", errSTTUnavailable
	}
	if err := sttCheckMemory(); err != nil {
		return "", err
	}
	bin, model := sttLocalFiles()
	text, err := sttRunLocal(ctx, bin, model, wav, lang, prompt)
	if err != nil && ctx.Err() == nil {
		logWarn("voice", "offline speech recognition failed: %v", err)
	}
	return text, err
}

func sttCheckMemory() error {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil // on unusual firmware, let the process itself decide
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		var kb int64
		if _, err := fmt.Sscanf(line, "MemAvailable: %d kB", &kb); err == nil && kb*1024 < sttMinAvailable {
			return errSTTMemory
		}
		break
	}
	return nil
}

func sttRunLocal(ctx context.Context, bin, model string, wav []byte, lang, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if lang == "" {
		lang = "auto"
	} else if lang == "nb" {
		lang = "no" // Whisper names Norwegian "no"; the keyboard layout uses "nb".
	}
	args := []string{"-m", model, "-f", "-", "-t", "2", "-l", lang, "-ng", "-np", "-nt", "-otxt", "-of", "-"}
	if prompt != "" {
		args = append(args, "--prompt", prompt)
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
	if out.Len() > 16<<10 {
		return "", errors.New("offline speech transcript is too long")
	}
	return strings.TrimSpace(out.String()), nil
}
