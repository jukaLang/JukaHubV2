//go:build !windows

package main

// tts_local_other.go — the handheld's (or a Linux PC's) own voice, when it has
// one.
//
// The TrimUI firmwares ship no speech engine, so on the handheld Juka's voice
// is Groq's alone, and nothing is ever downloaded for it. A Linux PC often
// has one, and a handheld user may drop one into JukaHub's required/ folder:
// the first of espeak-ng, espeak, flite, pico2wave and piper found there or on
// PATH speaks when Groq's voice cannot, or always when Settings → Voice & AI
// → Voice is "This device". Piper also needs a voice model (*.onnx) next to
// it, in a piper/ or piper-voices/ folder beside it, or in required/piper/.
//
// Each engine renders the text into a temporary WAV that is read and deleted.
// The text goes on the engine's standard input where it reads one (espeak,
// piper) and as the last argument after the option that names it otherwise,
// never through a shell.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ttsLocalTimeout bounds one synthesis (an engine renders far faster than it
// speaks; this only catches one that hangs).
const ttsLocalTimeout = 25 * time.Second

// ttsLocalEngines are the engines looked for, best first among those that need
// nothing else installed.
var ttsLocalEngines = []string{"espeak-ng", "espeak", "flite", "pico2wave", "piper"}

// ttsLocalFind is the first engine present.
func ttsLocalFind() (ttsLocalVoice, bool) {
	for _, name := range ttsLocalEngines {
		path, ok := checkToolPresence(name)
		if !ok {
			continue
		}
		v := ttsLocalVoice{name: name, path: path, kind: name}
		if name == "piper" {
			if v.model = ttsPiperModel(path); v.model == "" {
				continue
			}
		}
		return v, true
	}
	return ttsLocalVoice{}, false
}

// ttsPiperModel finds a piper voice model: next to the program, in a piper/
// or piper-voices/ folder beside it, or in required/piper. An English model
// ("en_…") is preferred.
func ttsPiperModel(piper string) string {
	dir := filepath.Dir(piper)
	dirs := []string{dir, filepath.Join(dir, "piper"), filepath.Join(dir, "piper-voices")}
	if req := P().RequiredDir(); req != "" {
		dirs = append(dirs, filepath.Join(req, "piper"), filepath.Join(req, "piper-voices"))
	}
	for _, d := range dirs {
		models, _ := filepath.Glob(filepath.Join(d, "*.onnx"))
		if len(models) == 0 {
			continue
		}
		sort.Strings(models)
		for _, m := range models {
			if strings.HasPrefix(strings.ToLower(filepath.Base(m)), "en") {
				return m
			}
		}
		return models[0]
	}
	return ""
}

// ttsLocalSynth renders text with the engine. gender is Groq's voice's, which
// these engines have no use for (Windows' synthesiser picks a voice by it).
func ttsLocalSynth(ctx context.Context, v ttsLocalVoice, text, gender string) ([]int16, int, error) {
	text = strings.ReplaceAll(text, "\x00", "")
	if strings.TrimSpace(text) == "" || v.path == "" {
		return nil, 0, errors.New("nothing to say")
	}
	// pico2wave only writes a file whose name ends in .wav.
	out, err := os.CreateTemp("", "jukahub-voice-*.wav")
	if err != nil {
		return nil, 0, err
	}
	wav := out.Name()
	_ = out.Close()
	defer os.Remove(wav)

	ctx, cancel := context.WithTimeout(ctx, ttsLocalTimeout)
	defer cancel()
	var cmd *exec.Cmd
	stdin := false
	switch v.kind {
	case "espeak-ng", "espeak":
		cmd = exec.CommandContext(ctx, v.path, "-w", wav, "--stdin")
		stdin = true
	case "flite":
		cmd = exec.CommandContext(ctx, v.path, "-o", wav, "-t", text)
	case "pico2wave":
		cmd = exec.CommandContext(ctx, v.path, "-l", "en-US", "-w", wav, "--", text)
	case "piper":
		cmd = exec.CommandContext(ctx, v.path, "--model", v.model, "--output_file", wav)
		stdin = true
	default:
		return nil, 0, errors.New("unknown voice engine " + v.kind)
	}
	var extra []string
	// An espeak copied into required/ brings its data folder along; tell it
	// where that is.
	for _, data := range []string{"espeak-ng-data", "espeak-data"} {
		if info, err := os.Stat(filepath.Join(filepath.Dir(v.path), data)); err == nil && info.IsDir() {
			extra = append(extra, "ESPEAK_DATA_PATH="+filepath.Dir(v.path))
			break
		}
	}
	cmd.Env = helperToolEnv(v.path, extra...)
	if stdin {
		cmd.Stdin = strings.NewReader(text + "\n")
	}
	if msg, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, fmt.Errorf("%s: %w (%s)", v.kind, err, firstLine(strings.TrimSpace(string(msg))))
	}
	data, err := os.ReadFile(wav)
	if err != nil {
		return nil, 0, err
	}
	return ttsDecodeWAV(data, false)
}
