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
	offlineAIModelName  = "SmolLM2-360M-Instruct-Q4_0.gguf"
	offlineAIModelSize  = 229733280
	offlineAIMinFreeMB  = 500
	offlineAIReplyRunes = 420
	offlineAITimeout    = 60 * time.Second
	offlineAIOutputMax  = 8 << 10
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
	system := "You are Juka. Answer in one short factual sentence. Say if unsure. You cannot take actions."
	if unmatchedAction {
		system = "You are Juka. Reply in one short sentence. You were unable to perform the user's request. " +
			"Name the failed action and give a manual step. Example: User: Open Dino Game. " +
			"Juka: I couldn't open Dino Game; try looking in Apps."
	}
	return "<|im_start|>system\n" + system + "<|im_end|>\n" +
		"<|im_start|>user\n" + question + "<|im_end|>\n<|im_start|>assistant\n"
}

func offlineAIArgs(model, prompt string) []string {
	return []string{"-m", model, "-p", prompt, "-n", "32", "-c", "256", "-b", "64", "-ub", "64",
		"-t", "2", "-ngl", "0", "--temp", "0.2", "--no-conversation", "--single-turn",
		"--no-display-prompt", "--no-warmup", "--simple-io"}
}

func offlineAIClean(raw string) string {
	for _, token := range []string{"<|", "</s>", "[end of text]"} {
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

// A short first sentence is enough for a voice answer. The Smart Pro can take
// several seconds per token, so stop generation as soon as one is complete.
func offlineAIFirstSentence(text string) string {
	for i, r := range text {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		end := i + len(string(r))
		if end < len(text) && text[end] != ' ' && text[end] != '\n' {
			continue
		}
		first := strings.TrimSpace(text[:end])
		if utf8.RuneCountInString(first) >= 12 && len(strings.Fields(first)) >= 2 {
			return first
		}
	}
	return ""
}

// cmd.Stdout writes here as tokens arrive. Showing them immediately means the
// handheld never sits at "Thinking offline" until every word has been made.
type offlineAIStreamWriter struct {
	raw       bytes.Buffer
	last      string
	sentence  string
	overflow  bool
	onPartial func(string)
	stop      func()
}

func (w *offlineAIStreamWriter) Write(p []byte) (int, error) {
	n := len(p)
	remaining := offlineAIOutputMax - w.raw.Len()
	if len(p) > remaining {
		w.overflow = true
		p = p[:max(0, remaining)]
	}
	_, _ = w.raw.Write(p)
	if w.sentence == "" && utf8.Valid(w.raw.Bytes()) {
		partial := offlineAIClean(w.raw.String())
		if partial != "" && partial != w.last {
			w.last = partial
			if w.onPartial != nil {
				w.onPartial(partial)
			}
		}
		if first := offlineAIFirstSentence(partial); first != "" {
			w.sentence = first
			if w.stop != nil {
				w.stop()
			}
		}
	}
	return n, nil
}

func offlineAIAnswer(ctx context.Context, question string, unmatchedAction bool, onPartial func(string)) (string, error) {
	if !offlineAIReady() {
		return "", errOfflineAIMissing
	}
	if err := offlineAICheckMemory(); err != nil {
		return "", err
	}
	bin, model := offlineAIFiles()
	runCtx, cancel := context.WithTimeout(ctx, offlineAITimeout)
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(runCtx, bin, offlineAIArgs(model, offlineAIPrompt(question, unmatchedAction))...)
	output := &offlineAIStreamWriter{onPartial: onPartial, stop: cancel}
	cmd.Stdout = output
	cmd.Stderr = io.Discard // model diagnostics do not belong in the voice bubble
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if output.sentence == "" {
			if runCtx.Err() != nil {
				return "", runCtx.Err()
			}
			return "", fmt.Errorf("offline AI stopped: %w", err)
		}
	}
	logDebug("voice", "offline AI answered in %s", time.Since(started).Round(time.Millisecond))
	if output.overflow {
		return "", errors.New("offline AI answer was too long")
	}
	answer := output.sentence
	if answer == "" {
		answer = offlineAIClean(output.raw.String())
	}
	if answer == "" {
		return "", errors.New("offline AI had no answer")
	}
	return answer, nil
}
