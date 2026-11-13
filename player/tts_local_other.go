//go:build !windows

package main

// tts_local_other.go — the handheld's (or a Linux PC's) own voice, when it has
// one.
//
// The TrimUI firmwares ship no speech engine. JukaHub can install Piper and a
// voice on the card on demand, or a user may add an engine to required/.
// A Linux PC often has one already:
// the first of espeak-ng, espeak, flite, pico2wave and piper found there or on
// PATH speaks when Groq's voice cannot, or always when Settings → Voice & AI
// → Voice is "This device". Piper also needs a voice model (*.onnx) next to
// it, in a piper/ or piper-voices/ folder beside it, or in required/piper/.
//
// Each engine renders the text into a temporary WAV that is read and deleted;
// Piper renders one WAV per chunk from a single process (ttsPiperStream), so
// a long answer starts playing after its first sentence.
// The text goes on the engine's standard input where it reads one (espeak,
// piper) and as the last argument after the option that names it otherwise,
// never through a shell.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// A neural voice can take longer on four Cortex-A53 cores than a rule-based
// engine. Both bounds still obey the session's cancellation immediately.
const (
	ttsLocalTimeout = 25 * time.Second
	ttsPiperTimeout = 75 * time.Second
)

// ttsLocalEngines are the engines looked for, best first among those that need
// nothing else installed.
var ttsLocalEngines = []string{"espeak-ng", "espeak", "flite", "pico2wave", "piper"}

// ttsLocalFind is the first engine present.
func ttsLocalFind() (ttsLocalVoice, bool) {
	if v, ok := ttsManagedVoice(); ok {
		return v, true
	}
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

	timeout := ttsLocalTimeout
	if v.kind == "piper" {
		timeout = ttsPiperTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
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
	cmd.Env = ttsLocalEnv(v)
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

// ttsLocalEnv is the engine's environment. An espeak or Piper copied into
// required/ brings its data folder along; tell it where that is. Piper's own
// libraries sit beside it (the Smart Pro install), so the loader looks there
// first rather than relying on the binary's RPATH.
func ttsLocalEnv(v ttsLocalVoice) []string {
	dir := filepath.Dir(v.path)
	var extra []string
	for _, data := range []string{"espeak-ng-data", "espeak-data"} {
		if info, err := os.Stat(filepath.Join(dir, data)); err == nil && info.IsDir() {
			extra = append(extra, "ESPEAK_DATA_PATH="+dir)
			break
		}
	}
	if v.kind == "piper" {
		if _, err := os.Stat(filepath.Join(dir, "libpiper_phonemize.so.1")); err == nil {
			lib := dir
			if old := os.Getenv("LD_LIBRARY_PATH"); old != "" {
				lib += string(os.PathListSeparator) + old
			}
			extra = append(extra, "LD_LIBRARY_PATH="+lib)
		}
	}
	return helperToolEnv(v.path, extra...)
}

// ttsPiperStream speaks chunks with one Piper process, so the first sentence
// plays while the rest are still being made (a neural voice on the Smart
// Pro's four A53 cores is about as slow as speech itself; rendering a long
// answer whole kept the user waiting in silence). Each chunk is one input
// line; with --output_dir Piper writes one WAV per line into a private folder
// and prints its path when the file is complete. emit gets each chunk's sound
// in order and returns false to stop. next gives the chunks — a live answer's
// as they are written (tts_live.go) — and each goes to Piper as soon as it
// comes, so the model is loaded and the first sentence playing while the rest
// of the answer is still on its way. It returns how many chunks were emitted.
func ttsPiperStream(ctx context.Context, v ttsLocalVoice, next func(context.Context) (string, bool), emit func([]int16, int) bool) (int, error) {
	if v.kind != "piper" || v.path == "" || v.model == "" {
		return 0, errors.New("not a Piper voice")
	}
	clean := func(c string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(c, "\x00", "")), " ")
	}
	// The first line is there before Piper starts: a line with nothing to
	// say costs no process.
	first := ""
	for first == "" {
		c, ok := next(ctx)
		if !ok {
			return 0, errors.New("nothing to say")
		}
		first = clean(c)
	}
	dir, err := os.MkdirTemp("", "jukahub-voice-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	// Model load plus the longest answer spoken; generous, and a stop still
	// ends it at once.
	timeout := ttsPiperTimeout + time.Duration(ttsSpokenMax/6)*time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, v.path, "--model", v.model, "--output_dir", dir)
	cmd.Env = ttsLocalEnv(v)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	var stderr strings.Builder
	cmd.Stderr = &ttsCappedWriter{b: &stderr, max: 4 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	// A Piper that stops answering is ended after ttsPiperTimeout without
	// progress while it owes a line (the model load counts against the first):
	// a short line no longer waits out the whole-answer cap above. Waiting for
	// the answer's next sentence owes nothing.
	var owed atomic.Int32
	var progress atomic.Int64
	var stalled atomic.Bool
	progress.Store(time.Now().UnixNano())
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if owed.Load() > 0 && time.Since(time.Unix(0, progress.Load())) > ttsPiperTimeout {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	// The writer hands Piper one line per chunk as the chunks come and closes
	// its input after the last; total is read only once it is done.
	total := 0
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		defer stdin.Close()
		line := first
		for {
			owed.Add(1)
			progress.Store(time.Now().UnixNano())
			if _, err := io.WriteString(stdin, line+"\n"); err != nil {
				return
			}
			total++
			line = ""
			for line == "" {
				c, ok := next(ctx)
				if !ok {
					return
				}
				line = clean(c)
			}
		}
	}()
	defer func() {
		cancel()
		<-wrote
	}()
	emitted := 0
	stopped := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 4096), 64<<10)
	cleanDir := filepath.Clean(dir)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		progress.Store(time.Now().UnixNano())
		if line == "" || stopped {
			continue
		}
		wav := filepath.Clean(line)
		if !filepath.IsAbs(wav) {
			wav = filepath.Join(cleanDir, wav)
		}
		// Only files Piper made in our folder are read.
		if filepath.Dir(wav) != cleanDir || !strings.EqualFold(filepath.Ext(wav), ".wav") {
			continue
		}
		owed.Add(-1)
		data, err := ttsReadFinishedWAV(ctx, wav)
		_ = os.Remove(wav)
		if err != nil {
			continue
		}
		samples, rate, err := ttsDecodeWAV(data, false)
		if err != nil {
			continue
		}
		emitted++
		if !emit(samples, rate) {
			// Stopped: end Piper, but keep reading so it is not blocked on a
			// full pipe while it exits.
			stopped = true
			cancel()
		}
	}
	if sc.Err() != nil {
		// A runaway line stopped the reading: end Piper rather than wait for
		// the timeout with nobody draining its output.
		cancel()
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()
	// Piper is gone: the writer stops too, and only then is total final. A
	// deadline or a stop is read before that cancel hides it.
	ctxErr := ctx.Err()
	if stalled.Load() {
		ctxErr = context.DeadlineExceeded
	}
	cancel()
	<-wrote
	if stopped || emitted >= total {
		return emitted, nil
	}
	if ctxErr != nil {
		return emitted, ctxErr
	}
	if waitErr != nil {
		return emitted, fmt.Errorf("piper: %w (%s)", waitErr, firstLine(strings.TrimSpace(stderr.String())))
	}
	if emitted == 0 {
		return 0, fmt.Errorf("piper made no sound (%s)", firstLine(strings.TrimSpace(stderr.String())))
	}
	return emitted, nil
}

// ttsReadFinishedWAV reads a WAV Piper has just announced. Piper prints the
// path before its file stream is closed, so the last buffered bytes may land
// a moment later: the header already holds the final data size (the sound is
// made before the file is written), and the read waits briefly until it is
// all there.
func ttsReadFinishedWAV(ctx context.Context, path string) ([]byte, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if ttsWAVComplete(data) || time.Now().After(deadline) || ctx.Err() != nil {
			return data, nil
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// ttsCappedWriter keeps the first max bytes of an engine's messages.
type ttsCappedWriter struct {
	b   *strings.Builder
	max int
}

func (w *ttsCappedWriter) Write(p []byte) (int, error) {
	if room := w.max - w.b.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.b.Write(p[:room])
	}
	return len(p), nil
}
