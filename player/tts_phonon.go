//go:build !windows

package main

// tts_phonon.go — speaking with the natural offline voice (Phonon 31M behind
// xn-ptts's speech server; tts_phonon_install.go installs it).
//
// One server stays loaded between lines, as Piper does (tts_local_other.go):
// voicePrewarm starts it while the user talks, a line borrows it
// (ttsPhononTake) and gives it back, and it lets go as Piper does once the
// conversation is over (ttsPiperWarmIdle), when free memory runs low, when a
// game starts and with JukaHub. It listens on a loopback port of its own and
// is asked for raw PCM, one piece of the answer at a time: the first sentence
// of a line in short pieces (ttsPiperLines), so the first words come soon, and
// the rest whole sentences, which the model reads with better melody. Each
// piece renders while the one before it plays (one piece ahead), so with a
// real-time factor under one the sentences follow each other without gaps.
//
// Anything that goes wrong before the first sound — no memory for it, a
// server that does not start, a refused request — returns an error with
// nothing emitted, and ttsLocalSpeakChunks hands the same chunks to Piper.
// Two such failures in a row retire it for the run (ttsPhononNoteFail).

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	// ttsPhononLoadTimeout bounds a start until the server answers /health:
	// the A53 reads the model from the card and builds its kernels.
	ttsPhononLoadTimeout = 60 * time.Second
	// ttsPhononPieceMax bounds one piece's sound (about 87 s at 24 kHz).
	ttsPhononPieceMax = 4 << 20
	// ttsPhononSentenceMax is the longest later piece, in bytes: a whole
	// sentence where it fits.
	ttsPhononSentenceMax = 220
	// ttsPhononBenchLine is what the install's probe times.
	ttsPhononBenchLine = "Hi, I'm Juka. This is my natural voice, and it works without the internet."
)

var (
	errTTSPhononMemory  = errors.New("not enough free memory for the natural voice")
	errTTSPhononStopped = errors.New("the natural voice was stopped")
	errTTSPhononBusy    = errors.New("natural voice: the server stayed busy")
)

// ttsPhononCounts reports whether err is the voice's own failure (two in a
// row retire it): not a lack of memory, not JukaHub stopping it, not a server
// still busy with a line the user cut short.
func ttsPhononCounts(err error) bool {
	return err != nil && !errors.Is(err, errTTSPhononMemory) && !errors.Is(err, errTTSPhononStopped) &&
		!errors.Is(err, errTTSPhononBusy) && !errors.Is(err, context.Canceled)
}

// ttsPhononClient talks to the loopback server: no proxy, no compression.
var ttsPhononClient = &http.Client{Transport: &http.Transport{
	Proxy:               nil,
	DialContext:         (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
	DisableCompression:  true,
	MaxIdleConnsPerHost: 2,
	IdleConnTimeout:     30 * time.Second,
}}

// ttsPhononProc is one running server.
type ttsPhononProc struct {
	key    string
	man    ttsPhononManifest
	cmd    *exec.Cmd
	base   string        // http://127.0.0.1:PORT
	ready  chan struct{} // closed once /health answered or it gave up
	okay   bool          // read after ready: it answered
	exited chan struct{} // closed once the process is gone and its scratch removed
	logs   *ttsPhononLogWriter
	// stopped: JukaHub ended it (a game, Settings, an install), which is
	// not the voice failing. noVoice: the server refused the package's voice
	// name once, so later pieces ask for its default at once. said counts
	// the pieces it rendered (the first one also warms it up).
	stopped atomic.Bool
	noVoice atomic.Bool
	said    atomic.Int32
}

func (p *ttsPhononProc) dead() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

// stop ends a server JukaHub no longer wants.
func (p *ttsPhononProc) stop() {
	p.stopped.Store(true)
	p.kill()
}

func (p *ttsPhononProc) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// why is the last line of the server's messages.
func (p *ttsPhononProc) why() string {
	msg := strings.TrimSpace(p.logs.String())
	if msg == "" {
		return "no message"
	}
	lines := strings.Split(msg, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// wait blocks until the server answers, gives up, or ctx ends.
func (p *ttsPhononProc) wait(ctx context.Context) error {
	select {
	case <-p.ready:
		if p.stopped.Load() {
			return errTTSPhononStopped
		}
		if !p.okay {
			return fmt.Errorf("the natural voice did not start (%s)", p.why())
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ttsPhononLogWriter keeps the last few KB of the server's messages. Its
// standard output and error share one, written from two goroutines.
type ttsPhononLogWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *ttsPhononLogWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func (w *ttsPhononLogWriter) Write(p []byte) (int, error) {
	n := len(p) // all of it is taken, whatever is kept: a short count would end the copy
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.b.Len()+len(p) > 8<<10 {
		keep := w.b.String()
		if len(keep) > 2<<10 {
			keep = keep[len(keep)-2<<10:]
		}
		w.b.Reset()
		w.b.WriteString(keep)
	}
	if len(p) > 4<<10 {
		p = p[len(p)-4<<10:]
	}
	w.b.Write(p)
	return n, nil
}

// ttsPhononFreePort is a loopback port nothing listens on right now.
func ttsPhononFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ttsPhononSpawn starts a server for v from its package's voice.json, without
// waiting for it (p.wait does).
func ttsPhononSpawn(v ttsLocalVoice, man ttsPhononManifest) (*ttsPhononProc, error) {
	port, err := ttsPhononFreePort()
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "jukahub-voice-scratch-")
	if err != nil {
		return nil, err
	}
	dir := v.model
	args := make([]string, 0, len(man.Args)+2)
	for _, a := range man.Args {
		args = append(args, ttsPhononExpand(a, dir, tmp))
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	args = append(args, man.AddrFlag, addr)
	cmd := exec.Command(v.path, args...)
	cmd.Dir = dir
	extra := []string{}
	lib := filepath.Join(dir, "lib")
	if old := os.Getenv("LD_LIBRARY_PATH"); old != "" {
		lib += string(os.PathListSeparator) + old
	}
	extra = append(extra, "LD_LIBRARY_PATH="+lib)
	for k, val := range man.Env {
		extra = append(extra, k+"="+ttsPhononExpand(val, dir, tmp))
	}
	cmd.Env = helperToolEnv(v.path, extra...)
	feToolProcAttr(cmd) // it dies with JukaHub
	logs := &ttsPhononLogWriter{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	ttsPiperOOMFirst(cmd.Process.Pid)
	p := &ttsPhononProc{
		key: v.path + "\x00" + v.model, man: man, cmd: cmd, base: "http://" + addr,
		ready: make(chan struct{}), exited: make(chan struct{}), logs: logs,
	}
	go func() {
		_ = cmd.Wait()
		_ = os.RemoveAll(tmp)
		ttsPhononWarm.mu.Lock()
		if ttsPhononWarm.p == p {
			ttsPhononWarm.p = nil
			ttsPhononWarm.users = 0
		}
		ttsPhononWarm.mu.Unlock()
		close(p.exited)
	}()
	go func() {
		defer close(p.ready)
		deadline := time.Now().Add(ttsPhononLoadTimeout)
		for time.Now().Before(deadline) {
			if p.dead() {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/health", nil)
			resp, err := ttsPhononClient.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
				resp.Body.Close()
			}
			cancel()
			if err == nil && resp.StatusCode == http.StatusOK {
				p.okay = true
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
		logWarn("tts", "natural voice: no answer within %s (%s)", ttsPhononLoadTimeout, p.why())
		p.kill()
	}()
	logDebug("tts", "natural voice loading on %s", addr)
	return p, nil
}

// ttsPhononWarm is the loaded server. Any goroutine, under mu. users counts
// the lines borrowing it (requests to one server may overlap).
var ttsPhononWarm struct {
	mu       sync.Mutex
	p        *ttsPhononProc
	users    int
	lastUse  time.Time
	watching bool
}

// ttsPhononTake lends the loaded server, starting it when there is room, and
// waits until it answers.
func ttsPhononTake(ctx context.Context, v ttsLocalVoice) (*ttsPhononProc, error) {
	if relPackRunning(ttsPhononPackKey) {
		return nil, fmt.Errorf("%w: it is being installed", errTTSPhononStopped)
	}
	key := v.path + "\x00" + v.model
	ttsPhononWarm.mu.Lock()
	p := ttsPhononWarm.p
	if p != nil && (p.key != key || p.dead()) {
		ttsPhononWarm.p, ttsPhononWarm.users = nil, 0
		p.stop()
		p = nil
	}
	if p == nil {
		man, err := ttsPhononReadManifest(v.model)
		if err != nil {
			ttsPhononWarm.mu.Unlock()
			return nil, err
		}
		if free := freeMemoryMB(); free > 0 && free < ttsPhononNeedMB(man)+ttsPhononLowWaterMB {
			ttsPhononWarm.mu.Unlock()
			return nil, fmt.Errorf("%w (%d MB free)", errTTSPhononMemory, free)
		}
		if p, err = ttsPhononSpawn(v, man); err != nil {
			ttsPhononWarm.mu.Unlock()
			return nil, err
		}
		ttsPhononWarm.p = p
		if !ttsPhononWarm.watching {
			ttsPhononWarm.watching = true
			go ttsPhononWatch()
		}
	}
	ttsPhononWarm.users++
	ttsPhononWarm.lastUse = time.Now()
	ttsPhononWarm.mu.Unlock()
	if err := p.wait(ctx); err != nil {
		ttsPhononGive(p, ctx.Err() != nil)
		return nil, err
	}
	return p, nil
}

// ttsPhononGive takes the server back after a line; one that misbehaved
// (healthy false) is ended.
func ttsPhononGive(p *ttsPhononProc, healthy bool) {
	ttsPhononWarm.mu.Lock()
	if ttsPhononWarm.p == p {
		if ttsPhononWarm.users > 0 {
			ttsPhononWarm.users--
		}
		ttsPhononWarm.lastUse = time.Now()
		if !healthy {
			ttsPhononWarm.p, ttsPhononWarm.users = nil, 0
		}
	}
	ttsPhononWarm.mu.Unlock()
	if !healthy {
		p.kill()
	}
}

// ttsPhononWarmStop ends the loaded server. Any goroutine.
func ttsPhononWarmStop() {
	ttsPhononWarm.mu.Lock()
	p := ttsPhononWarm.p
	ttsPhononWarm.p, ttsPhononWarm.users = nil, 0
	ttsPhononWarm.mu.Unlock()
	if p != nil {
		p.stop()
	}
}

// ttsPhononPrewarm loads the natural voice when it is the device's voice.
// Any goroutine; the load itself runs on its own.
func ttsPhononPrewarm() {
	v, ok := ttsLocal()
	if !ok || v.kind != "phonon" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), ttsPhononLoadTimeout+5*time.Second)
		defer cancel()
		if p, err := ttsPhononTake(ctx, v); err == nil {
			ttsPhononGive(p, true)
		} else {
			logDebug("tts", "natural voice not preloaded: %v", err)
		}
	}()
}

// ttsPhononWatch ends an idle server, and one whose memory is needed. One
// goroutine while a server is loaded.
func ttsPhononWatch() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		ttsPhononWarm.mu.Lock()
		p := ttsPhononWarm.p
		if p == nil {
			ttsPhononWarm.watching = false
			ttsPhononWarm.mu.Unlock()
			return
		}
		users, last := ttsPhononWarm.users, ttsPhononWarm.lastUse
		ttsPhononWarm.mu.Unlock()
		// Piper's rule (tts_local_other.go): the idle time runs from the end
		// of the conversation, longer after a typed one.
		if talk := ttsTalkLast(); talk.After(last) {
			last = talk
		}
		idle := time.Since(last)
		limit := ttsPiperWarmIdle
		if ttsTalkTyped.Load() {
			limit = ttsPiperWarmIdleTyped
		}
		if users == 0 && (idle > limit || freeMemoryMB() < ttsPhononLowWaterMB) {
			ttsPhononWarm.mu.Lock()
			if ttsPhononWarm.p == p && ttsPhononWarm.users == 0 {
				ttsPhononWarm.p = nil
			} else {
				p = nil
			}
			ttsPhononWarm.mu.Unlock()
			if p != nil {
				p.stop()
				logDebug("tts", "natural voice unloaded")
			}
		}
	}
}

// ttsPhononSay renders one piece with p: 16-bit samples and their rate. A
// voice name the server refuses is dropped for its default voice, for good
// on that server. A server busy with a cut-short line is asked again until
// ctx ends.
func ttsPhononSay(ctx context.Context, p *ttsPhononProc, text, voice string) ([]int16, int, error) {
	useVoice := voice != "" && !p.noVoice.Load()
	dropped := false // the voice name was left out after a refusal
	for attempt := 0; ; attempt++ {
		body := map[string]string{"input": text, "response_format": "pcm"}
		if p.man.Model != "" {
			body["model"] = p.man.Model
		}
		if useVoice {
			body["voice"] = voice
		}
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/v1/audio/speech", bytes.NewReader(data))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := ttsPhononClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			wait := time.Duration(attempt+1) * 150 * time.Millisecond
			if wait > time.Second {
				wait = time.Second
			}
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return nil, 0, fmt.Errorf("%w (%v)", errTTSPhononBusy, ctx.Err())
			}
		}
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			resp.Body.Close()
			if useVoice && resp.StatusCode >= 400 && resp.StatusCode < 500 {
				// Maybe the voice name, maybe the text: once more without
				// the name, which is dropped for good only if that works.
				useVoice, dropped = false, true
				continue
			}
			return nil, 0, fmt.Errorf("natural voice: HTTP %d (%s)", resp.StatusCode, firstLine(strings.TrimSpace(string(msg))))
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, ttsPhononPieceMax+1))
		resp.Body.Close()
		if err != nil {
			return nil, 0, err
		}
		if len(raw) > ttsPhononPieceMax {
			return nil, 0, errors.New("natural voice: a piece came back too long")
		}
		if dropped && !p.noVoice.Swap(true) {
			logWarn("tts", "natural voice: the server refused voice %q; using its default", voice)
		}
		p.said.Add(1)
		return ttsPhononDecode(raw, p.man.Rate)
	}
}

// ttsPhononDecode turns the server's answer into samples: raw 16-bit little
// endian PCM at rate, or a WAV should a server send one anyway. Pure.
func ttsPhononDecode(raw []byte, rate int) ([]int16, int, error) {
	if len(raw) >= 12 && string(raw[0:4]) == "RIFF" {
		return ttsDecodeWAV(raw, true)
	}
	n := len(raw) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	return out, rate, nil
}

// ttsPhononPieces cuts a chunk into what one request says: the line's first
// chunk in Piper's short pieces (the first words come sooner), later chunks
// in whole sentences up to ttsPhononSentenceMax bytes. Pure.
func ttsPhononPieces(chunk string, first bool) []string {
	if first {
		return ttsPiperLines(chunk)
	}
	var out []string
	for _, s := range ttsSentences(chunk) {
		out = append(out, ttsSplitLong(s, ttsPhononSentenceMax)...)
	}
	if len(out) == 0 && strings.TrimSpace(chunk) != "" {
		out = []string{strings.TrimSpace(chunk)}
	}
	return out
}

// ttsPhononHasWords reports whether text has a letter or digit to say (a
// piece of symbols alone may rightly come back silent). Pure.
func ttsPhononHasWords(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

// ttsPhononNoteFail counts a failure of the voice itself; the second in a
// row retires it for the run, and the device's voice is looked up again
// (Piper).
func ttsPhononNoteFail(err error) {
	if !ttsPhononCounts(err) {
		return
	}
	if ttsPhononFails.Add(1) >= 2 && !ttsPhononRetired.Swap(true) {
		logWarn("tts", "natural voice failed twice (%v); Piper speaks for the rest of this run", err)
		ttsPhononWarmStop()
		ttsLocalRecheck()
	}
}

// ttsPhononNoteSpeed records the speed of a piece the server rendered after
// its first (which also warms it up), when no speed is on record. Too slow,
// and the natural voice steps aside for Piper from the next line on — for
// this run only, and without a record: a live answer's first piece may have
// shared the CPU with the offline AI still writing the answer.
func ttsPhononNoteSpeed(p *ttsPhononProc, took time.Duration, samples, rate int) {
	if rate <= 0 || samples < rate*3/2 || p.said.Load() < 2 || ttsPhononMeasured.Load() {
		return
	}
	if _, known := ttsPhononSpeed(); known {
		ttsPhononMeasured.Store(true)
		return
	}
	ttsPhononMeasured.Store(true)
	rtf := took.Seconds() / (float64(samples) / float64(rate))
	logInfo("tts", "natural voice: %.2f s of work per second of speech on this device (first use)", rtf)
	if rtf > ttsPhononMaxRTF {
		logWarn("tts", "natural voice: too slow just now; Piper speaks for the rest of this run")
		ttsPhononSlowNow.Store(true)
		ttsLocalRecheck()
		return
	}
	ttsPhononSetMark(ttsPhononSpeedName, fmt.Sprintf("rtf %.2f", rtf), true)
}

// ttsPhononStream speaks chunks with the natural voice, like ttsPiperStream:
// next gives the chunks in order (waiting while a live answer is written),
// emit gets each piece's sound and returns false to stop. It returns how many
// pieces were emitted and, after a failure, what was not said yet of the
// chunks next had given (the failed piece on): with the chunks next still has,
// that is what Piper says instead. next is only ever called from one goroutine
// at a time, and never after this returns.
func ttsPhononStream(ctx context.Context, v ttsLocalVoice, gender string, next func(context.Context) (string, bool), emit func([]int16, int) bool) (int, []string, error) {
	clean := func(c string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(c, "\x00", "")), " ")
	}
	first := ""
	for first == "" {
		c, ok := next(ctx)
		if !ok {
			return 0, nil, nil // nothing to say
		}
		first = clean(c)
	}
	voiceBoostHold(voiceBoostSpeak)
	p, err := ttsPhononTake(ctx, v)
	if err != nil {
		if ctx.Err() == nil {
			ttsPhononNoteFail(err)
		}
		return 0, []string{first}, err
	}
	voice := ttsPhononVoiceFor(p.man, gender)
	type piece struct {
		text string
		s    []int16
		rate int
		took time.Duration
		err  error
		rest []string // on err: this piece and the rest of its chunk
	}
	sub, cancel := context.WithCancel(ctx)
	made := make(chan piece, 1)
	// The renderer works one piece ahead of the one playing.
	go func() {
		defer close(made)
		line, firstChunk := first, true
		for {
			pieces := ttsPhononPieces(line, firstChunk)
			for i, text := range pieces {
				voiceBoostHold(voiceBoostSpeak)
				pctx, pcancel := context.WithTimeout(sub, ttsPiperTimeout)
				start := time.Now()
				s, rate, err := ttsPhononSay(pctx, p, text, voice)
				if err != nil && errors.Is(pctx.Err(), context.DeadlineExceeded) && sub.Err() == nil {
					// Hung, or busy for the whole of it: either way stuck.
					err = fmt.Errorf("natural voice: a piece took over %s: %w", ttsPiperTimeout, context.DeadlineExceeded)
				}
				pcancel()
				pc := piece{text: text, s: s, rate: rate, took: time.Since(start), err: err}
				if err != nil {
					pc.rest = append([]string(nil), pieces[i:]...)
				}
				select {
				case made <- pc:
				case <-sub.Done():
					return
				}
				if err != nil {
					return
				}
			}
			line, firstChunk = "", false
			for line == "" {
				c, ok := next(sub)
				if !ok {
					return
				}
				line = clean(c)
			}
		}
	}()
	// finish stops the renderer and waits for it, so next is never called
	// after this returns.
	finish := func() {
		cancel()
		for range made {
		}
	}
	emitted := 0
	var failed error
	var rest []string
	// unsaid: pieces with words that came back silent since the last sound,
	// for Piper to say should nothing follow them.
	var unsaid []string
	for pc := range made {
		if pc.err != nil {
			failed, rest = pc.err, append(unsaid, pc.rest...)
			break
		}
		ttsPhononNoteSpeed(p, pc.took, len(pc.s), pc.rate)
		if ttsTrim(pc.s, pc.rate) == nil {
			if ttsPhononHasWords(pc.text) {
				unsaid = append(unsaid, pc.text)
			}
			continue
		}
		unsaid = nil
		emitted++
		if !emit(pc.s, pc.rate) {
			break
		}
	}
	finish()
	if failed == nil && emitted == 0 && len(unsaid) > 0 && ctx.Err() == nil {
		failed = errors.New("natural voice: it made only silence")
		rest = unsaid
	}
	if failed != nil && p.stopped.Load() {
		// JukaHub ended it (a game, Settings, an install): not its failure.
		failed = fmt.Errorf("%w (%v)", errTTSPhononStopped, failed)
	}
	switch {
	case failed == nil:
		ttsPhononFails.Store(0)
		ttsPhononGive(p, true)
		return emitted, nil, nil
	case ctx.Err() != nil:
		ttsPhononGive(p, true)
		return emitted, nil, ctx.Err()
	}
	// A server that died or hung ends; one that only refused a piece stays.
	hung := errors.Is(failed, context.DeadlineExceeded)
	ttsPhononGive(p, !p.dead() && !hung)
	if emitted == 0 || p.dead() || hung {
		ttsPhononNoteFail(failed)
	}
	return emitted, rest, failed
}

// ttsPhononSynth renders a whole line with the natural voice (the paths that
// want one sound, not a stream).
func ttsPhononSynth(ctx context.Context, v ttsLocalVoice, text, gender string) ([]int16, int, error) {
	var all []int16
	rate := 0
	given := false
	_, _, err := ttsPhononStream(ctx, v, gender, func(context.Context) (string, bool) {
		if given {
			return "", false
		}
		given = true
		return text, true
	}, func(s []int16, r int) bool {
		if rate != 0 && r != rate {
			s = ttsResample(s, r, rate)
		} else {
			rate = r
		}
		all = append(all, s...)
		return true
	})
	if err != nil {
		// Half a line is not the line: the caller has Piper say all of it.
		return nil, 0, err
	}
	if len(all) == 0 {
		return nil, 0, errors.New("natural voice: no sound")
	}
	return all, rate, nil
}

// ttsPhononBench starts a server from the package in dir (not the shared
// one) and returns its real-time factor on a test sentence, after one short
// line to warm it up. ctx is the install's: cancelling it ends the test.
func ttsPhononBench(ctx context.Context, dir string, man ttsPhononManifest) (float64, error) {
	v := ttsLocalVoice{path: filepath.Join(dir, man.Server), kind: "phonon", model: dir}
	p, err := ttsPhononSpawn(v, man)
	if err != nil {
		return 0, err
	}
	defer func() {
		p.stop()
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, ttsPhononLoadTimeout+2*ttsPiperTimeout)
	defer cancel()
	if err := p.wait(ctx); err != nil {
		return 0, err
	}
	voice := ttsPhononVoiceFor(man, "female")
	if _, _, err := ttsPhononSay(ctx, p, "Hello.", voice); err != nil {
		return 0, err
	}
	start := time.Now()
	s, rate, err := ttsPhononSay(ctx, p, ttsPhononBenchLine, voice)
	if err != nil {
		return 0, err
	}
	if rate <= 0 || len(s) < rate {
		return 0, fmt.Errorf("it made only %d samples", len(s))
	}
	return time.Since(start).Seconds() / (float64(len(s)) / float64(rate)), nil
}
