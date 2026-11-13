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
// a long answer starts playing after its first sentence; that process stays
// loaded between lines for a while (ttsPiperWarm), so the next line does not
// load the voice again.
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
	"strconv"
	"strings"
	"sync"
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
	// A Piper already loaded (voicePrewarm, or the line before) speaks the
	// first sentence without loading the voice again.
	if p := ttsPiperWarmTake(v); p != nil {
		n, healthy, err := ttsPiperStreamWarm(ctx, p, first, next, clean, emit)
		ttsPiperWarmGive(p, healthy)
		return n, err
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

// ── A Piper kept loaded ──────────────────────────────────────────────────
//
// Loading the voice took a second or more of every line on the Smart Pro.
// One Piper process now stays loaded between lines: voicePrewarm starts it
// while the user talks, ttsPiperStream borrows it (ttsPiperWarmTake) and
// gives it back. Piper reads one line per utterance and prints one WAV path
// per line, so a borrower that leaves with lines still owed (a stopped line)
// ends the process rather than leave stale sound for the next one. It lets go
// after ttsPiperWarmIdle unused (ttsPiperWarmInGame while a game is open),
// when free memory runs low, when a game starts and with JukaHub.

const (
	ttsPiperWarmIdle   = 45 * time.Second
	ttsPiperWarmInGame = 10 * time.Second
	ttsPiperLowWaterMB = 60
)

// ttsPiperProc is one loaded Piper.
type ttsPiperProc struct {
	key    string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    chan string   // the paths Piper prints; closed when it exits
	exited chan struct{} // closed once it is gone and its folder removed
	dir    string
	stderr *strings.Builder
}

// why is the start of Piper's messages once it has exited (they are written
// until then).
func (p *ttsPiperProc) why() string {
	select {
	case <-p.exited:
		return firstLine(strings.TrimSpace(p.stderr.String()))
	case <-time.After(2 * time.Second):
		return "no message"
	}
}

// ttsPiperWarm is the loaded Piper. Any goroutine, under mu.
var ttsPiperWarm struct {
	mu       sync.Mutex
	p        *ttsPiperProc
	busy     bool
	lastUse  time.Time
	watching bool
}

func ttsPiperKey(v ttsLocalVoice) string { return v.path + "\x00" + v.model }

// ttsPiperPrewarm loads the device's Piper voice, if that is its voice. Any
// goroutine.
func ttsPiperPrewarm() {
	v, ok := ttsLocal()
	if !ok || v.kind != "piper" || v.model == "" {
		return
	}
	ttsPiperWarm.mu.Lock()
	defer ttsPiperWarm.mu.Unlock()
	if p := ttsPiperWarm.p; p != nil && p.key == ttsPiperKey(v) {
		ttsPiperWarm.lastUse = time.Now()
		return
	}
	if ttsPiperWarm.busy {
		return
	}
	ttsPiperWarmStartLocked(v)
}

// ttsPiperWarmStartLocked starts a Piper for v in place of any other. mu held;
// nil when it would not start.
func ttsPiperWarmStartLocked(v ttsLocalVoice) *ttsPiperProc {
	if old := ttsPiperWarm.p; old != nil {
		ttsPiperWarm.p = nil
		_ = old.cmd.Process.Kill()
	}
	dir, err := os.MkdirTemp("", "jukahub-voice-warm-")
	if err != nil {
		return nil
	}
	cmd := exec.Command(v.path, "--model", v.model, "--output_dir", dir)
	cmd.Env = ttsLocalEnv(v)
	feToolProcAttr(cmd) // it dies with JukaHub
	p := &ttsPiperProc{key: ttsPiperKey(v), cmd: cmd, out: make(chan string, 64), exited: make(chan struct{}), dir: dir, stderr: &strings.Builder{}}
	cmd.Stderr = &ttsCappedWriter{b: p.stderr, max: 4 << 10}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		logWarn("tts", "Piper did not start: %v", err)
		return nil
	}
	p.stdin = stdin
	_ = os.WriteFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "oom_score_adj"), []byte("800"), 0o644)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 4096), 64<<10)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				p.out <- line
			}
		}
		if sc.Err() != nil {
			_ = cmd.Process.Kill()
			_, _ = io.Copy(io.Discard, stdout)
		}
		close(p.out)
		_ = cmd.Wait()
		_ = os.RemoveAll(dir)
		ttsPiperWarm.mu.Lock()
		if ttsPiperWarm.p == p {
			ttsPiperWarm.p = nil
			ttsPiperWarm.busy = false
		}
		ttsPiperWarm.mu.Unlock()
		close(p.exited)
	}()
	ttsPiperWarm.p = p
	ttsPiperWarm.lastUse = time.Now()
	if !ttsPiperWarm.watching {
		ttsPiperWarm.watching = true
		go ttsPiperWarmWatch()
	}
	logDebug("tts", "Piper loading for the next lines")
	return p
}

// ttsPiperWarmTake lends the loaded Piper for one line, starting it when
// there is room. nil: another line has it, or it cannot start; the caller
// starts its own.
func ttsPiperWarmTake(v ttsLocalVoice) *ttsPiperProc {
	ttsPiperWarm.mu.Lock()
	defer ttsPiperWarm.mu.Unlock()
	if ttsPiperWarm.busy {
		return nil
	}
	p := ttsPiperWarm.p
	if p != nil {
		select {
		case <-p.exited:
			p = nil
		default:
		}
	}
	if p == nil || p.key != ttsPiperKey(v) {
		if freeMemoryMB() < voicePiperWarmMB+ttsPiperLowWaterMB {
			return nil
		}
		if p = ttsPiperWarmStartLocked(v); p == nil {
			return nil
		}
	}
	// Nothing should be waiting; a leftover path is dropped with its file.
	for drained := false; !drained; {
		select {
		case path, ok := <-p.out:
			if !ok {
				return nil
			}
			if wav := filepath.Clean(path); filepath.Dir(wav) == filepath.Clean(p.dir) {
				_ = os.Remove(wav)
			}
		default:
			drained = true
		}
	}
	ttsPiperWarm.busy = true
	return p
}

// ttsPiperWarmGive takes the Piper back after a line. One that still owes
// lines (healthy false) is ended.
func ttsPiperWarmGive(p *ttsPiperProc, healthy bool) {
	ttsPiperWarm.mu.Lock()
	if ttsPiperWarm.p == p {
		ttsPiperWarm.busy = false
		ttsPiperWarm.lastUse = time.Now()
		if !healthy {
			ttsPiperWarm.p = nil
		}
	}
	ttsPiperWarm.mu.Unlock()
	if !healthy {
		_ = p.cmd.Process.Kill()
	}
}

// ttsPiperWarmStop ends the loaded Piper. Any goroutine.
func ttsPiperWarmStop() {
	ttsPiperWarm.mu.Lock()
	p := ttsPiperWarm.p
	ttsPiperWarm.p = nil
	ttsPiperWarm.busy = false
	ttsPiperWarm.mu.Unlock()
	if p != nil {
		_ = p.cmd.Process.Kill()
	}
}

// ttsPiperWarmWatch ends an idle Piper, and one whose memory is needed. One
// goroutine while a Piper is loaded.
func ttsPiperWarmWatch() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		ttsPiperWarm.mu.Lock()
		p := ttsPiperWarm.p
		if p == nil {
			ttsPiperWarm.watching = false
			ttsPiperWarm.mu.Unlock()
			return
		}
		busy, idle := ttsPiperWarm.busy, time.Since(ttsPiperWarm.lastUse)
		ttsPiperWarm.mu.Unlock()
		limit := ttsPiperWarmIdle
		if runningGameChildren.Load() > 0 {
			limit = ttsPiperWarmInGame
		}
		if !busy && (idle > limit || freeMemoryMB() < ttsPiperLowWaterMB) {
			ttsPiperWarm.mu.Lock()
			if ttsPiperWarm.p == p && !ttsPiperWarm.busy {
				ttsPiperWarm.p = nil
			} else {
				p = nil
			}
			ttsPiperWarm.mu.Unlock()
			if p != nil {
				_ = p.cmd.Process.Kill()
				logDebug("tts", "Piper unloaded")
			}
		}
	}
}

// ttsPiperStreamWarm speaks a line's chunks with the loaded Piper p: first,
// then the rest as next gives them. healthy reports whether p owes no lines
// afterwards (it may be lent again).
func ttsPiperStreamWarm(ctx context.Context, p *ttsPiperProc, first string, next func(context.Context) (string, bool), clean func(string) string, emit func([]int16, int) bool) (emitted int, healthy bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, ttsPiperTimeout+time.Duration(ttsSpokenMax/6)*time.Second)
	defer cancel()
	var sent atomic.Int32
	var writeFailed atomic.Bool
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		line := first
		for {
			voiceBoostHold(voiceBoostSpeak)
			sent.Add(1)
			if _, err := io.WriteString(p.stdin, line+"\n"); err != nil {
				writeFailed.Store(true)
				return
			}
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
	// finish waits for the writer, so nothing reaches Piper after the line
	// is given back, and reports whether every line sent came back.
	received := 0
	finish := func() bool {
		cancel()
		<-writerDone
		return !writeFailed.Load() && int(sent.Load()) == received
	}
	stall := time.NewTimer(ttsPiperTimeout)
	defer stall.Stop()
	cleanDir := filepath.Clean(p.dir)
	writing := true
	for {
		if !writing && int(sent.Load()) == received {
			return emitted, !writeFailed.Load(), nil
		}
		var wd <-chan struct{}
		if writing {
			wd = writerDone
		}
		select {
		case <-wd:
			writing = false
			if writeFailed.Load() {
				_ = p.cmd.Process.Kill()
				return emitted, false, fmt.Errorf("piper stopped reading (%s)", p.why())
			}
		case path, ok := <-p.out:
			if !ok {
				finish()
				return emitted, false, fmt.Errorf("piper exited (%s)", p.why())
			}
			wav := filepath.Clean(path)
			if !filepath.IsAbs(wav) {
				wav = filepath.Join(cleanDir, wav)
			}
			// Only files Piper made in its folder are read.
			if filepath.Dir(wav) != cleanDir || !strings.EqualFold(filepath.Ext(wav), ".wav") {
				continue
			}
			received++
			if !stall.Stop() {
				select {
				case <-stall.C:
				default:
				}
			}
			stall.Reset(ttsPiperTimeout)
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
				return emitted, finish(), nil
			}
		case <-stall.C:
			if int(sent.Load()) > received {
				finish()
				return emitted, false, context.DeadlineExceeded
			}
			stall.Reset(ttsPiperTimeout)
		case <-ctx.Done():
			err := ctx.Err()
			return emitted, finish(), err
		}
	}
}
