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
// load the voice again. Those WAVs live in memory (/dev/shm, else the
// system's temporary folder — a tmpfs on the TrimUI firmwares), never on the
// card: Piper's sound reaches the speaker without touching the SD card. One
// WAV per sentence rather than one raw stream is what tells the sentences
// apart, so a stopped line ends Piper's work at a sentence and a warm Piper
// can be lent again with nothing left owed.
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
	out, err := ttsCreateTemp("jukahub-voice-*.wav")
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
// answer whole kept the user waiting in silence). Each sentence of a chunk is
// one input line (ttsPiperLines); with --output_dir Piper writes one WAV per
// line into a private folder and prints its path when the file is complete.
// emit gets each line's sound
// in order and returns false to stop. next gives the chunks — a live answer's
// as they are written (tts_live.go) — and each goes to Piper as soon as it
// comes, so the model is loaded and the first sentence playing while the rest
// of the answer is still on its way. It returns how many Piper lines were emitted.
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
		n, healthy, err := ttsPiperStreamWarm(ctx, p, first, next, clean, emit, true)
		ttsPiperWarmGive(p, healthy)
		return n, err
	}
	dir, err := ttsMkdirTemp("jukahub-voice-")
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
	ttsPiperOOMFirst(cmd.Process.Pid)
	ttsPiperPinCPUs(cmd.Process.Pid)
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
			// A sentence (or a part of a long one) per line, so each sound is
			// ready about when the one before it has played.
			for _, piece := range ttsPiperLines(line) {
				owed.Add(1)
				progress.Store(time.Now().UnixNano())
				if _, err := io.WriteString(stdin, piece+"\n"); err != nil {
					return
				}
				total++
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
	defer func() {
		cancel()
		<-wrote
	}()
	emitted := 0
	stopped := false
	cut := false // a sound was played before Piper had finished writing it
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
		data, whole, err := ttsReadFinishedWAV(ctx, wav)
		_ = os.Remove(wav)
		if err != nil {
			continue
		}
		cut = cut || !whole
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
	if stopped {
		return emitted, nil
	}
	if emitted >= total {
		if cut {
			return emitted, errTTSPartial
		}
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
	// A sentence's sound could not be read: the rest was heard.
	return emitted, errTTSPartial
}

// ttsReadFinishedWAV reads a WAV Piper has just announced. Piper prints the
// path before its file stream is closed, so the last buffered bytes may land
// a moment later: the header already holds the final data size (the sound is
// made before the file is written), and the read waits briefly until it is
// all there. Its bool is false when it gave up waiting: what there is still
// plays, but is not kept (tts_cache.go).
func ttsReadFinishedWAV(ctx context.Context, path string) ([]byte, bool, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, false, err
		}
		if done := ttsWAVComplete(data); done || time.Now().After(deadline) || ctx.Err() != nil {
			return data, done, nil
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
// ttsPiperWarmIdle after the conversation ends — the last line spoken and the
// orb gone (ttsKeepWarm), so its ~120 MB go back to games and menus;
// ttsPiperWarmIdleTyped after a Chat reply or a Voice command page answer,
// whose next question is typed or asked without the orb — when free memory
// runs low, when a game starts and with JukaHub.
//
// While it sits loaded and idle after a conversation, and no game runs, it
// makes the common lines the cache does not hold yet (ttsPiperSeedOne,
// tts_cache.go), one short line per second without raising the CPU. A line
// that wants Piper meanwhile raises the CPU and waits for that one
// (ttsPiperSeedWait) rather than load a second Piper.

const (
	ttsPiperWarmIdle      = 10 * time.Second
	ttsPiperWarmIdleTyped = 45 * time.Second
	ttsPiperLowWaterMB    = 60
	// ttsPiperSeedAfter is the quiet before the cache's lines are made, and
	// ttsPiperSeedLoaded how long a Piper has run before it makes them (its
	// voice is loaded by then). ttsPiperSeedMax bounds one; a line waits
	// for it as long (ttsPiperSeedWait), its end being near.
	ttsPiperSeedAfter  = 1500 * time.Millisecond
	ttsPiperSeedLoaded = 4 * time.Second
	ttsPiperSeedMax    = 5 * time.Second
	ttsPiperSeedWait   = ttsPiperSeedMax
)

// ttsPiperProc is one loaded Piper.
type ttsPiperProc struct {
	at     time.Time // when it started
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
	seeding  chan struct{} // closed when the cache's line being made is done (nil: none)
}

func ttsPiperKey(v ttsLocalVoice) string { return v.path + "\x00" + v.model }

// ttsPiperOOMFirst makes a Piper the first thing the kernel ends when memory
// runs out on the 1 GB handheld, rather than JukaHub or a paused game. Linux
// only; elsewhere the write fails harmlessly.
func ttsPiperOOMFirst(pid int) {
	_ = os.WriteFile(filepath.Join("/proc", strconv.Itoa(pid), "oom_score_adj"), []byte("800"), 0o644)
}

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
	dir, err := ttsMkdirTemp("jukahub-voice-warm-")
	if err != nil {
		return nil
	}
	cmd := exec.Command(v.path, "--model", v.model, "--output_dir", dir)
	cmd.Env = ttsLocalEnv(v)
	feToolProcAttr(cmd) // it dies with JukaHub
	p := &ttsPiperProc{at: time.Now(), key: ttsPiperKey(v), cmd: cmd, out: make(chan string, 64), exited: make(chan struct{}), dir: dir, stderr: &strings.Builder{}}
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
	ttsPiperOOMFirst(cmd.Process.Pid)
	ttsPiperPinCPUs(cmd.Process.Pid)
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
	if seeding := ttsPiperWarm.seeding; ttsPiperWarm.busy && seeding != nil {
		// A cache line is being made: a moment, far less than loading a
		// second Piper. The CPU goes up for it as for this line.
		ttsPiperWarm.mu.Unlock()
		voiceBoostHold(voiceBoostSpeak)
		wait := time.NewTimer(ttsPiperSeedWait)
		select {
		case <-seeding:
		case <-wait.C:
		}
		wait.Stop()
		ttsPiperWarm.mu.Lock()
	}
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
		busy, last := ttsPiperWarm.busy, ttsPiperWarm.lastUse
		ttsPiperWarm.mu.Unlock()
		// The idle time runs from the end of the conversation: an orb still
		// up (a slow offline answer being written) keeps the voice.
		if talk := ttsTalkLast(); talk.After(last) {
			last = talk
		}
		idle := time.Since(last)
		limit := ttsPiperWarmIdle
		if ttsTalkTyped.Load() {
			limit = ttsPiperWarmIdleTyped
		}
		lowMem := freeMemoryMB() < ttsPiperLowWaterMB
		// The cache's lines: made only with time left before the unload (a
		// long one would hold the voice past it).
		if room := limit - idle; !busy && !lowMem && idle >= ttsPiperSeedAfter && room > 2*time.Second &&
			time.Since(p.at) >= ttsPiperSeedLoaded && !ttsBusy.Load() && runningGameChildren.Load() == 0 {
			if room > ttsPiperSeedMax {
				room = ttsPiperSeedMax
			}
			ttsPiperSeedOne(p, room)
			continue
		}
		if !busy && (idle > limit || lowMem) {
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
// afterwards (it may be lent again). boost raises the CPU while it speaks (a
// line someone waits for; not the cache's lines made in the background).
func ttsPiperStreamWarm(ctx context.Context, p *ttsPiperProc, first string, next func(context.Context) (string, bool), clean func(string) string, emit func([]int16, int) bool, boost bool) (emitted int, healthy bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, ttsPiperTimeout+time.Duration(ttsSpokenMax/6)*time.Second)
	defer cancel()
	started := time.Now()
	var sent atomic.Int32
	var writeFailed atomic.Bool
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		line := first
		for {
			if boost {
				voiceBoostHold(voiceBoostSpeak)
			}
			// A sentence (or a part of a long one) per line (ttsPiperLines).
			for _, piece := range ttsPiperLines(line) {
				sent.Add(1)
				if _, err := io.WriteString(p.stdin, piece+"\n"); err != nil {
					writeFailed.Store(true)
					return
				}
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
	cut := false // a sound was missing or played before Piper had finished it
	for {
		if !writing && int(sent.Load()) == received {
			if cut || emitted < received {
				return emitted, !writeFailed.Load(), errTTSPartial
			}
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
			data, whole, err := ttsReadFinishedWAV(ctx, wav)
			_ = os.Remove(wav)
			if err != nil {
				continue
			}
			cut = cut || !whole
			samples, rate, err := ttsDecodeWAV(data, false)
			if err != nil {
				continue
			}
			if emitted == 0 && rate > 0 && boost {
				// How soon Juka's voice starts (a line, not a cache seed).
				took := time.Since(started)
				speech := float64(len(samples)) / float64(rate)
				logInfo("tts", "Piper: first sound after %s (%.1f s of speech, %d Hz; %.2f s waited per second of it)",
					took.Round(10*time.Millisecond), speech, rate, took.Seconds()/max(speech, 0.1))
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

// ttsPiperSeedOne makes one common line the cache does not hold yet with the
// idle loaded Piper p, and keeps its sound (tts_cache.go). It borrows p as a
// line would, without raising the CPU or counting as use (the idle time runs
// on), for at most limit. ttsPiperWarmWatch's goroutine.
func ttsPiperSeedOne(p *ttsPiperProc, limit time.Duration) {
	v, ok := ttsLocal()
	if !ok || v.kind != "piper" || ttsPiperKey(v) != p.key {
		return
	}
	ttsPiperWarm.mu.Lock()
	if ttsPiperWarm.p != p || ttsPiperWarm.busy {
		ttsPiperWarm.mu.Unlock()
		return
	}
	select {
	case <-p.exited:
		ttsPiperWarm.mu.Unlock()
		return
	default:
	}
	ttsPiperWarm.busy = true
	done := make(chan struct{})
	ttsPiperWarm.seeding = done
	ttsPiperWarm.mu.Unlock()
	healthy := true
	defer func() {
		ttsPiperWarm.mu.Lock()
		if ttsPiperWarm.p == p {
			ttsPiperWarm.busy = false
			if !healthy {
				ttsPiperWarm.p = nil
			}
		}
		if ttsPiperWarm.seeding == done {
			ttsPiperWarm.seeding = nil
		}
		ttsPiperWarm.mu.Unlock()
		close(done)
		if !healthy {
			_ = p.cmd.Process.Kill()
		}
	}()
	id := ttsCacheLocalID(v, "")
	text, ok := ttsCacheSeedNext(id)
	if !ok {
		return
	}
	clean := func(c string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(c, "\x00", "")), " ")
	}
	line := clean(text)
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	var made []int16
	rate, mixed := 0, false
	n, ok, err := ttsPiperStreamWarm(ctx, p, line, func(context.Context) (string, bool) { return "", false }, clean,
		func(samples []int16, r int) bool {
			if rate == 0 {
				rate = r
			}
			if r != rate {
				mixed = true
			}
			made = append(made, samples...)
			return true
		}, false)
	healthy = ok
	if err == nil && ok && !mixed && n == len(ttsPiperLines(line)) && len(made) > 0 {
		ttsCacheStore(id, text, made, rate)
		logDebug("tts", "kept the sound of %q for next time", text)
	}
}

// ttsMkdirTemp makes a private folder for an engine's sound files in memory:
// /dev/shm when it can be written there, else the system's temporary folder.
func ttsMkdirTemp(pattern string) (string, error) {
	if dir, err := os.MkdirTemp("/dev/shm", pattern); err == nil {
		return dir, nil
	}
	return os.MkdirTemp("", pattern)
}

// ttsCreateTemp is ttsMkdirTemp for one file.
func ttsCreateTemp(pattern string) (*os.File, error) {
	if f, err := os.CreateTemp("/dev/shm", pattern); err == nil {
		return f, nil
	}
	return os.CreateTemp("", pattern)
}
