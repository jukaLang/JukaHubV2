package main

// Optional text answers on the original Smart Pro. The model is kept loaded by
// llama-server while the voice orb is in use (offline_ai_server.go) and let go
// when it idles or a game starts; a package without the server, or a server
// that will not start, answers with a short-lived llama-cli run instead. It
// never receives a device action to execute.

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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	offlineAIDirName               = "juka-llm"
	offlineAIModelName             = "SmolLM2-135M-Instruct-Q4_0.gguf"
	offlineAIModelSize       int64 = 91893088
	offlineAILegacyModelName       = "SmolLM2-360M-Instruct-Q4_0.gguf"
	offlineAILegacyModelSize int64 = 229733280
	// offlineAIStartMB is the free memory a cold start needs (the model, its
	// buffers and a little room). voicePrewarm asks for more while the user
	// still talks (offlineAIPrewarmNeedMB).
	offlineAIStartMB       = 220
	offlineAILegacyStartMB = 360
	offlineAIReplyRunes    = 420
	offlineAIQuestionRunes = 400 // about 100 tokens: fits -c 384 beside the system prompt and the reply
	offlineAITimeout       = 60 * time.Second
	offlineAIOutputMax     = 8 << 10
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
	bin = filepath.Join(dir, "llama-cli")
	b, bErr := os.Stat(bin)
	if bErr != nil || !b.Mode().IsRegular() || b.Mode().Perm()&0o111 == 0 {
		return "", "", false
	}
	for _, spec := range []struct {
		name string
		size int64
	}{{offlineAIModelName, offlineAIModelSize}, {offlineAILegacyModelName, offlineAILegacyModelSize}} {
		path := filepath.Join(dir, spec.name)
		if m, err := os.Stat(path); err == nil && m.Mode().IsRegular() && m.Size() == spec.size {
			return bin, path, true
		}
	}
	return
}

func offlineAIFiles() (bin, model string) {
	// A verified download overrides a bundled copy of the same generation.
	// A warm server matters most; among server packages the smaller model wins.
	best := -1
	for _, dir := range []string{offlineAIDownloadedDir(), offlineAIBundledDir()} {
		b, m, ok := offlineAIFilesIn(dir)
		if !ok {
			continue
		}
		score := 0
		if offlineAIServerIn(dir) != "" {
			score += 2
		}
		if filepath.Base(m) == offlineAIModelName {
			score++
		}
		if score > best {
			bin, model, best = b, m, score
		}
	}
	return bin, model
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
func offlineAIStartNeedMB(model string) uint64 {
	if filepath.Base(model) == offlineAILegacyModelName {
		return offlineAILegacyStartMB
	}
	return offlineAIStartMB
}

// offlineAIGameBlocks reports whether a game runs without the assistant
// paused over it: the model must not load beside it. Any goroutine.
func offlineAIGameBlocks() bool {
	return runningGameChildren.Load() > 0 && !gameAssistOverlayActive()
}

func offlineAICheckMemory(model string) error {
	if freeMemoryMB() < offlineAIStartNeedMB(model) {
		return errOfflineAIMemory
	}
	return nil
}

func offlineAIPrompt(question string, unmatchedAction bool) string {
	system := "You are Juka. Answer in one short factual sentence. Say if unsure. You cannot take actions."
	if unmatchedAction {
		system = "You are Juka. Reply in one short sentence. You were unable to perform the user's request. " +
			"Name the failed action and give a manual step. Example: User: Open Dino Game. " +
			"Juka: I couldn't open Dino Game; try looking in Apps."
	}
	return offlineAIChatPrompt(system, question)
}

func offlineAIChatPrompt(system, question string) string {
	clean := strings.NewReplacer("<|im_start|>", " ", "<|im_end|>", " ")
	// A long dictation would not fit the small context with the instructions
	// and the answer: the runner then refuses every try.
	if utf8.RuneCountInString(question) > offlineAIQuestionRunes {
		question = string([]rune(question)[:offlineAIQuestionRunes])
	}
	// Bytes too: in Cyrillic or CJK 400 runes are far more tokens.
	for len(question) > offlineAIQuestionRunes {
		_, n := utf8.DecodeLastRuneInString(question)
		question = question[:len(question)-n]
	}
	return "<|im_start|>system\n" + clean.Replace(system) + "<|im_end|>\n" +
		"<|im_start|>user\n" + clean.Replace(question) + "<|im_end|>\n<|im_start|>assistant\n"
}

// The small offline model cannot see a paused game's picture. Give a useful
// general hint from its name, or ask for detail instead of inventing a move.
func offlineAIGamePrompt(game, platform, question string) string {
	system := "You are Juka, a game helper. Give one short practical hint. You cannot see the game screen offline. " +
		"Do not invent exact buttons, levels or locations. If unsure, ask for one detail."
	game = strings.TrimSpace(game)
	if game == "" {
		game = "Unknown game"
	}
	context := "Game: " + game
	if platform = strings.TrimSpace(platform); platform != "" {
		context += " (" + platform + ")"
	}
	return offlineAIChatPrompt(system, context+"\nQuestion: "+question)
}

// offlineAIArgs is the one-shot runner's command line (no server in the
// package, or it would not start): all four cores, nothing else runs then.
func offlineAIArgs(model, prompt string) []string {
	return []string{"-m", model, "-p", prompt, "-n", strconv.Itoa(offlineAIPredict), "-c", "384", "-b", "64", "-ub", "64",
		"-t", "4", "-ngl", "0", "--temp", "0.2", "--no-conversation", "--single-turn",
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

// A short first sentence is enough for a voice answer: generation stops as
// soon as one is complete, and Juka starts saying it.
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
		first := offlineAIFirstSentence(partial)
		// A stop that ends the words so far may be "3." of "3.14" or "U." of
		// "U.S.": it counts once the next token shows a space after it. The
		// end of the stream keeps the whole text anyway (offlineAIResult).
		if first != "" && len(first) == len(partial) && !offlineAIEndsInSpace(w.raw.Bytes()) {
			first = ""
		}
		if first != "" {
			partial = first
		}
		if partial != "" && partial != w.last {
			w.last = partial
			if w.onPartial != nil {
				w.onPartial(partial)
			}
		}
		if first != "" {
			w.sentence = first
			if w.stop != nil {
				w.stop()
			}
		}
	}
	return n, nil
}

// offlineAIEndsInSpace reports whether raw output ends with white space, so
// its last word is complete. Pure.
func offlineAIEndsInSpace(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	switch raw[len(raw)-1] {
	case ' ', '\n', '\t', '\r':
		return true
	}
	return false
}

func offlineAIAnswer(ctx context.Context, question string, unmatchedAction bool, onPartial func(string)) (string, error) {
	return offlineAIAnswerPrompt(ctx, offlineAIPrompt(question, unmatchedAction), onPartial)
}

func offlineAIGameAnswer(ctx context.Context, game, platform, question string, onPartial func(string)) (string, error) {
	return offlineAIAnswerPrompt(ctx, offlineAIGamePrompt(game, platform, question), onPartial)
}

func offlineAIAnswerPrompt(ctx context.Context, prompt string, onPartial func(string)) (string, error) {
	if !offlineAIReady() {
		return "", errOfflineAIMissing
	}
	bin, model := offlineAIFiles()
	runCtx, cancel := context.WithTimeout(ctx, offlineAITimeout)
	defer cancel()
	output := &offlineAIStreamWriter{onPartial: onPartial}
	if server, _ := offlineAIServerFiles(); server != "" {
		fallback, err := offlineAIServerAnswer(runCtx, server, model, prompt, output)
		if !fallback {
			if err != nil && output.sentence == "" && (ctx.Err() != nil || output.raw.Len() == 0) {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				return "", err
			}
			return offlineAIResult(output)
		}
		logWarn("voice", "offline AI server could not answer (%v); using the one-shot runner", err)
		output = &offlineAIStreamWriter{onPartial: onPartial}
	}
	// The server also stops when a game starts: the question went with it.
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if offlineAIGameBlocks() {
		return "", errOfflineAIMemory
	}
	if err := offlineAICheckMemory(model); err != nil {
		return "", err
	}
	output.stop = cancel
	started := time.Now()
	cmd := exec.CommandContext(runCtx, bin, offlineAIArgs(model, prompt)...)
	cmd.Stdout = output
	cmd.Stderr = io.Discard // model diagnostics do not belong in the voice bubble
	feToolProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("offline AI stopped: %w", err)
	}
	_ = os.WriteFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "oom_score_adj"), []byte("1000"), 0o644)
	if err := cmd.Wait(); err != nil {
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
	return offlineAIResult(output)
}

// offlineAIResult is the answer an output holds: its first sentence, else all
// of it cleaned.
func offlineAIResult(output *offlineAIStreamWriter) (string, error) {
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
