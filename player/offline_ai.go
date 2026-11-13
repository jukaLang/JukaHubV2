package main

// Optional text answers on the original Smart Pro. The model runs as a short
// lived CPU process after speech recognition has finished, so its memory is
// returned before a game starts. It never receives a device action to execute.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	offlineAIDirName    = "juka-llm"
	offlineAIModelName  = "SmolLM2-360M-Instruct-Q4_K_M.gguf"
	offlineAIModelSize  = 270590880
	offlineAIMinFreeMB  = 500
	offlineAIReplyRunes = 420
	offlineAITimeout    = 120 * time.Second
)

var errOfflineAIMissing = errors.New("offline AI is not installed")
var errOfflineAIMemory = errors.New("not enough free memory for offline AI")

func offlineAISupported() bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" && DetectDevice() == DeviceTrimuiSmartPro
}

func offlineAIBundledDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), offlineAIDirName)
}

func offlineAIDownloadedDir() string {
	data, err := P().DataDir()
	if err != nil || data == "" {
		return ""
	}
	return filepath.Join(data, offlineAIDirName)
}

func offlineAIFilesIn(dir string) (bin, model string, ok bool) {
	if dir == "" {
		return "", "", false
	}
	bin, model = filepath.Join(dir, "llama-cli"), filepath.Join(dir, offlineAIModelName)
	b, bErr := os.Stat(bin)
	m, mErr := os.Stat(model)
	ok = bErr == nil && b.Mode().IsRegular() && b.Mode().Perm()&0o111 != 0 &&
		mErr == nil && m.Mode().IsRegular() && m.Size() == offlineAIModelSize
	return
}

func offlineAIFiles() (bin, model string) {
	if bin, model, ok := offlineAIFilesIn(offlineAIBundledDir()); ok {
		return bin, model
	}
	if bin, model, ok := offlineAIFilesIn(offlineAIDownloadedDir()); ok {
		return bin, model
	}
	return "", ""
}

func offlineAIReady() bool {
	if !offlineAISupported() {
		return false
	}
	bin, _ := offlineAIFiles()
	return bin != ""
}

// The model can fit beside the UI on a 1 GB device when games are closed.
// /proc may be unavailable on unusual firmware; the process then decides.
func offlineAICheckMemory() error {
	if freeMemoryMB() < offlineAIMinFreeMB {
		return errOfflineAIMemory
	}
	return nil
}

func offlineAIPrompt(question string, unmatchedAction bool) string {
	question = strings.NewReplacer("<|im_start|>", " ", "<|im_end|>", " ").Replace(question)
	system := "You are Juka, a voice assistant on a handheld with no internet. " +
		"Answer in one or two short plain sentences. Be honest when you do not know a current fact. " +
		"You cannot open apps, change settings, or take other actions."
	if unmatchedAction {
		system += " The app could not carry out this request. Say that it was not done, then give a useful suggestion."
	}
	return "<|im_start|>system\n" + system + "<|im_end|>\n" +
		"<|im_start|>user\n" + question + "<|im_end|>\n<|im_start|>assistant\n"
}

func offlineAIArgs(model, prompt string) []string {
	return []string{"-m", model, "-p", prompt, "-n", "80", "-c", "512", "-b", "128", "-ub", "128",
		"-t", "3", "-ngl", "0", "--temp", "0.2", "--no-conversation", "--single-turn",
		"--no-display-prompt", "--no-warmup", "--simple-io"}
}

func offlineAIClean(raw string) string {
	for _, token := range []string{"<|im_end|>", "<|im_start|>", "</s>"} {
		if i := strings.Index(raw, token); i >= 0 {
			raw = raw[:i]
		}
	}
	raw = orbPlain(strings.TrimSpace(raw))
	if utf8.RuneCountInString(raw) > offlineAIReplyRunes {
		raw = string([]rune(raw)[:offlineAIReplyRunes])
	}
	return strings.TrimSpace(raw)
}

func offlineAIAnswer(ctx context.Context, question string, unmatchedAction bool) (string, error) {
	if !offlineAIReady() {
		return "", errOfflineAIMissing
	}
	if err := offlineAICheckMemory(); err != nil {
		return "", err
	}
	bin, model := offlineAIFiles()
	ctx, cancel := context.WithTimeout(ctx, offlineAITimeout)
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(ctx, bin, offlineAIArgs(model, offlineAIPrompt(question, unmatchedAction))...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard // model diagnostics do not belong in the voice bubble
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("offline AI stopped: %w", err)
	}
	logDebug("voice", "offline AI answered in %s", time.Since(started).Round(time.Millisecond))
	if output.Len() > 8<<10 {
		return "", errors.New("offline AI answer was too long")
	}
	answer := offlineAIClean(output.String())
	if answer == "" {
		return "", errors.New("offline AI had no answer")
	}
	return answer, nil
}
