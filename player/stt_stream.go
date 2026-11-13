package main

// stt_stream.go — offline speech while it is spoken.
//
// The one-shot recognizer (stt_local.go sttRunFast) starts vosk-cli after the
// clip ends, loads the model from the SD card and only then decodes: about six
// seconds for a three-second question on a Smart Pro. Here vosk-cli runs as a
// warm server (vosk-cli --serve, player/native/vosk-cli.c): the model loads
// once, while the user is still talking, and every chunk of the microphone is
// decoded as it is recorded. When the clip ends only the last fraction of a
// second is left, so the words are ready almost at once.
//
//   - The server starts when something is about to listen (the voice orb, the
//     game assistant, the "Hey Juka" listener, wakeword.go) and stops after
//     sttServeIdle without use — at once when memory runs low, or when a game
//     takes the screen and nothing in it needs the recognizer. The wake word
//     keeps it loaded while it listens (sttServeKeep).
//   - One utterance at a time (sttStream): the main thread hands audio to
//     Feed, which never blocks; a goroutine passes it to the server and keeps
//     the newest partial words for the orb. Finish asks for the final words.
//   - A package from before streaming has a vosk-cli without --serve
//     (sttServeFiles reads its --help), a server that dies or falls behind,
//     and every language but English all fall back to the one-shot path,
//     which still has the whole clip.
//
// Nothing is written to disk: audio goes through the pipe and is dropped.

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// sttServeIdle is how long the warm recognizer stays loaded after its last
	// use: long enough for a follow-up question, short enough to give its
	// memory back soon after.
	sttServeIdle = 45 * time.Second
	// sttServeLoadWait bounds the model load from the SD card.
	sttServeLoadWait = 25 * time.Second
	// sttServeCallWait bounds one request; a server that does not answer is
	// killed (a hung decoder must not hold the orb).
	sttServeCallWait = 10 * time.Second
	// sttServeRetry is the pause after the server failed to start.
	sttServeRetry = time.Minute
	// sttServeStartAvail is the free memory a start needs (the small model
	// takes about 100-150 MB resident); sttServeKeepAvail is the floor under
	// which an idle server is stopped at once.
	sttServeStartAvail = 256 << 20
	sttServeKeepAvail  = 128 << 20
	// sttServeCheckEvery paces the idle and memory checks.
	sttServeCheckEvery = 5 * time.Second
	// sttServeMaxFrame matches the server's own limit.
	sttServeMaxFrame = 1 << 20
	// sttStreamQueue is how many chunks wait for the server (the orb's are
	// 128 ms: about a minute, far more than a model load).
	sttStreamQueue = 512
	// sttStreamPartialEvery paces the partial words when a stream asks for them.
	sttStreamPartialEvery = 250 * time.Millisecond
	// sttStreamStall drops a stream that nobody fed or ended for this long.
	sttStreamStall = 30 * time.Second

	sttServeUnknown int32 = 0
	sttServeYes     int32 = 1
	sttServeNo      int32 = 2
)

var (
	errSTTNoServe      = errors.New("this offline speech package has no streaming recognizer")
	errSTTServeDown    = errors.New("the offline speech server is not running")
	errSTTServeSlow    = errors.New("the offline speech model took too long to load")
	errSTTStreamBehind = errors.New("streamed recognition fell behind")
)

// ── Can it stream ────────────────────────────────────────────────────────

// sttServeCaps caches each vosk-cli's answer to --help, by size and time.
var sttServeCaps struct {
	mu sync.Mutex
	m  map[string]sttServeCap
}

type sttServeCap struct {
	size int64
	mod  time.Time
	ok   bool
}

// sttServeCapable reports whether bin has the server mode (its --help names
// --serve). Runs the binary once per build; not on the main thread.
func sttServeCapable(bin string) bool {
	info, err := os.Stat(bin)
	if err != nil {
		return false
	}
	sttServeCaps.mu.Lock()
	c, hit := sttServeCaps.m[bin]
	sttServeCaps.mu.Unlock()
	if hit && c.size == info.Size() && c.mod.Equal(info.ModTime()) {
		return c.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, "--help").Output()
	ok := strings.Contains(string(out), "--serve")
	if !ok && ctx.Err() != nil {
		return false // a slow card at boot: ask again next time
	}
	sttServeCaps.mu.Lock()
	if sttServeCaps.m == nil {
		sttServeCaps.m = map[string]sttServeCap{}
	}
	sttServeCaps.m[bin] = sttServeCap{size: info.Size(), mod: info.ModTime(), ok: ok}
	sttServeCaps.mu.Unlock()
	return ok
}

// sttServeFiles is the recognizer and model of the first installed package
// that streams, or "" — an old bundled copy beside the binary does not hide a
// newer download. Not on the main thread.
func sttServeFiles() (string, string) {
	for _, dir := range []string{sttBundledDir(), sttDownloadedDir()} {
		if bin, model, ok := sttFastFilesIn(dir); ok && sttServeCapable(bin) {
			return bin, model
		}
	}
	return "", ""
}

// sttServeProbe is what the main thread knows about streaming here: unknown
// until a goroutine has looked (sttServeUsable starts one).
var sttServeProbe struct {
	state   atomic.Int32
	running atomic.Bool
}

// sttServeUsable reports whether the installed package streams. Main thread
// safe: the first call starts the look and answers false.
func sttServeUsable() bool {
	if !sttSupported() {
		return false
	}
	switch sttServeProbe.state.Load() {
	case sttServeYes:
		return true
	case sttServeUnknown:
		sttServeProbeStart()
	}
	return false
}

// sttServeProbeStart looks once in the background.
func sttServeProbeStart() {
	if !sttServeProbe.running.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer sttServeProbe.running.Store(false)
		if bin, _ := sttServeFiles(); bin != "" {
			sttServeProbe.state.Store(sttServeYes)
		} else {
			sttServeProbe.state.Store(sttServeNo)
		}
	}()
}

// sttServeForget is an install's word that the package changed: the server
// from the old one stops and the next use looks again.
func sttServeForget() {
	sttServeStop("offline speech was updated")
	sttServe.mu.Lock()
	sttServe.failedAt = time.Time{} // the new package gets its own try at once
	sttServe.mu.Unlock()
	sttServeProbe.state.Store(sttServeUnknown)
}

// sttMemAvailable is MemAvailable in bytes; 0 when unknown.
func sttMemAvailable() int64 {
	return androidPakMemAvailable("/proc/meminfo")
}

// ── The server ───────────────────────────────────────────────────────────

// sttServer is one running vosk-cli --serve.
type sttServer struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     *bufio.Reader
	ready   chan struct{} // closed once the model loaded or failed
	loadErr error         // set before ready closes
	exited  chan struct{}
	// ioMu: one request and its reply at a time. sessMu: one utterance at a
	// time (the server has one recognizer).
	ioMu    sync.Mutex
	sessMu  sync.Mutex
	busy    atomic.Int32 // streams using or waiting for it
	lastUse atomic.Int64 // UnixNano
	kill    sync.Once
	head    [5]byte
	// vocab caches the model's answer to known-word questions.
	vocabMu sync.Mutex
	vocab   map[string]bool
}

// sttServe is the one warm server.
var sttServe struct {
	mu       sync.Mutex
	srv      *sttServer
	failedAt time.Time
	reaping  bool
	// keep: the wake word listens, so idleness does not stop the server.
	keep atomic.Bool
}

// sttServeKeep says whether the wake word needs the server loaded.
func sttServeKeep(on bool) { sttServe.keep.Store(on) }

// sttServeWarm starts the server in the background so its model loads while
// the user talks. Any goroutine; free when it already runs.
func sttServeWarm() {
	if !sttSupported() || sttServeProbe.state.Load() == sttServeNo {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("voice", "offline speech warm-up stopped: %v", r)
			}
		}()
		if s, err := sttServerGet(); err == nil {
			s.touch()
		}
	}()
}

// sttServerGet returns the running server, starting one when there is none.
// Never on the main thread: it may run vosk-cli --help and start a process.
func sttServerGet() (*sttServer, error) {
	sttServe.mu.Lock()
	defer sttServe.mu.Unlock()
	if s := sttServe.srv; s != nil && !s.dead() {
		// A new user: the reaper must not take it as idle before busy counts it.
		s.touch()
		return s, nil
	}
	sttServe.srv = nil
	if !sttServe.failedAt.IsZero() && time.Since(sttServe.failedAt) < sttServeRetry {
		return nil, errSTTServeDown
	}
	if !sttLocalReady() {
		return nil, errSTTUnavailable
	}
	if avail := sttMemAvailable(); avail > 0 && avail < sttServeStartAvail {
		return nil, errSTTMemory
	}
	bin, model := sttServeFiles()
	if bin == "" {
		sttServeProbe.state.Store(sttServeNo)
		return nil, errSTTNoServe
	}
	sttServeProbe.state.Store(sttServeYes)
	s, err := sttServerStart(bin, model)
	if err != nil {
		sttServe.failedAt = time.Now()
		return nil, err
	}
	sttServe.failedAt = time.Time{}
	sttServe.srv = s
	if !sttServe.reaping {
		sttServe.reaping = true
		go sttServeReap()
	}
	return s, nil
}

func sttServerStart(bin, model string) (*sttServer, error) {
	cmd := exec.Command(bin, "--serve", "-m", model)
	// The decoder is single-threaded; so are its maths libraries here, which
	// leaves the other cores to the interface and a game.
	cmd.Env = append(os.Environ(), "OPENBLAS_NUM_THREADS=1", "OMP_NUM_THREADS=1")
	cmd.Stderr = nil // diagnostics can contain recognized speech
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("offline speech server: %w", err)
	}
	s := &sttServer{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 64<<10),
		ready: make(chan struct{}), exited: make(chan struct{})}
	s.touch()
	started := time.Now()
	go func() {
		_ = cmd.Wait()
		close(s.exited)
	}()
	go func() {
		typ, body, err := s.readFrame()
		switch {
		case err != nil:
			s.loadErr = fmt.Errorf("offline speech server: %w", err)
		case typ != 'K':
			s.loadErr = fmt.Errorf("offline speech server: %s", strings.TrimSpace(string(body)))
		}
		if s.loadErr != nil {
			s.stop()
			logWarn("voice", "%v", s.loadErr)
			// A model that will not load must not be started again for every
			// wake-word segment: the same back-off as a failed start.
			sttServe.mu.Lock()
			sttServe.failedAt = time.Now()
			sttServe.mu.Unlock()
		} else {
			logInfo("voice", "offline speech server ready in %s (pid %d, %s resident)",
				time.Since(started).Round(time.Millisecond), cmd.Process.Pid, sttProcRSS(cmd.Process.Pid))
		}
		close(s.ready)
	}()
	return s, nil
}

// sttProcRSS is a process's resident memory as /proc says it, for the log.
func sttProcRSS(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return "unknown"
}

func (s *sttServer) touch() { s.lastUse.Store(time.Now().UnixNano()) }

func (s *sttServer) dead() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}

// stop ends the process; safe to call twice and from any goroutine.
func (s *sttServer) stop() {
	s.kill.Do(func() {
		_ = s.in.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	})
}

// waitReady waits for the model, at most sttServeLoadWait.
func (s *sttServer) waitReady() error {
	t := time.NewTimer(sttServeLoadWait)
	defer t.Stop()
	select {
	case <-s.ready:
		return s.loadErr
	case <-s.exited:
		return errSTTServeDown
	case <-t.C:
		s.stop()
		return errSTTServeSlow
	}
}

func (s *sttServer) readFrame() (byte, []byte, error) {
	if _, err := io.ReadFull(s.out, s.head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(s.head[1:])
	if n > sttServeMaxFrame {
		return 0, nil, errors.New("reply too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(s.out, body); err != nil {
		return 0, nil, err
	}
	return s.head[0], body, nil
}

// call sends one request and reads its reply. A server that does not answer
// within sttServeCallWait is killed.
func (s *sttServer) call(typ byte, payload []byte) (byte, []byte, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	if s.dead() {
		return 0, nil, errSTTServeDown
	}
	if len(payload) > sttServeMaxFrame {
		return 0, nil, errors.New("offline speech request too large")
	}
	timer := time.AfterFunc(sttServeCallWait, s.stop)
	defer timer.Stop()
	var head [5]byte
	head[0] = typ
	binary.LittleEndian.PutUint32(head[1:], uint32(len(payload)))
	if _, err := s.in.Write(head[:]); err != nil {
		s.stop()
		return 0, nil, fmt.Errorf("offline speech server: %w", err)
	}
	if len(payload) > 0 {
		if _, err := s.in.Write(payload); err != nil {
			s.stop()
			return 0, nil, fmt.Errorf("offline speech server: %w", err)
		}
	}
	rt, body, err := s.readFrame()
	if err != nil {
		s.stop()
		return 0, nil, fmt.Errorf("offline speech server: %w", err)
	}
	s.touch()
	if rt == 'X' {
		return rt, body, fmt.Errorf("offline speech server: %s", strings.TrimSpace(string(body)))
	}
	return rt, body, nil
}

// knownWords is the subset of words the model can recognize (a grammar word
// outside its vocabulary is skipped by Vosk). Cached per server.
func (s *sttServer) knownWords(words []string) ([]string, error) {
	s.vocabMu.Lock()
	if s.vocab == nil {
		s.vocab = map[string]bool{}
	}
	var ask []string
	for _, w := range words {
		if _, seen := s.vocab[w]; !seen {
			ask = append(ask, w)
		}
	}
	s.vocabMu.Unlock()
	if len(ask) > 0 {
		_, body, err := s.call('W', []byte(strings.Join(ask, "\n")))
		if err != nil {
			return nil, err
		}
		known := map[string]bool{}
		for _, w := range strings.Split(string(body), "\n") {
			known[strings.TrimSpace(w)] = true
		}
		s.vocabMu.Lock()
		for _, w := range ask {
			s.vocab[w] = known[w]
		}
		s.vocabMu.Unlock()
	}
	s.vocabMu.Lock()
	defer s.vocabMu.Unlock()
	var out []string
	for _, w := range words {
		if s.vocab[w] {
			out = append(out, w)
		}
	}
	return out, nil
}

// sttServeStop stops the warm server now, if one runs.
func sttServeStop(why string) {
	sttServe.mu.Lock()
	s := sttServe.srv
	sttServe.srv = nil
	sttServe.mu.Unlock()
	if s != nil {
		s.stop()
		logInfo("voice", "offline speech server stopped: %s", why)
	}
}

// sttServeReap stops the server when it is not needed: idle past
// sttServeIdle (unless the wake word keeps it), a game on screen with nothing
// in it listening, or memory running low. One goroutine per running server.
func sttServeReap() {
	for {
		time.Sleep(sttServeCheckEvery)
		sttServe.mu.Lock()
		s := sttServe.srv
		if s == nil || s.dead() {
			sttServe.srv = nil
			sttServe.reaping = false
			sttServe.mu.Unlock()
			return
		}
		why := ""
		if s.busy.Load() == 0 {
			idle := time.Since(time.Unix(0, s.lastUse.Load()))
			keep := sttServe.keep.Load()
			switch avail := sttMemAvailable(); {
			case avail > 0 && avail < sttServeKeepAvail:
				why = "memory is low"
			case !keep && gameScreenSuspended() && !gameAssistOverlayActive():
				why = "a game has the screen"
			case !keep && idle >= sttServeIdle:
				why = "not used for a while"
			}
		}
		if why != "" {
			sttServe.srv = nil
			sttServe.reaping = false
		}
		sttServe.mu.Unlock()
		if why != "" {
			s.stop()
			logInfo("voice", "offline speech server stopped: %s", why)
			return
		}
	}
}

// ── One utterance ────────────────────────────────────────────────────────

// sttStreamOpts shapes one utterance.
type sttStreamOpts struct {
	grammar      []string      // phrases the recognizer may hear (nil: free speech)
	words        bool          // word times and confidences in the result
	background   bool          // decode at low priority (the wake word)
	partialEvery time.Duration // 0: no partial words
}

// sttWord is one recognized word with its confidence and times (seconds from
// the start of the utterance's audio).
type sttWord struct {
	Word  string  `json:"word"`
	Conf  float64 `json:"conf"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// sttStream is one utterance being recognized. Feed, Finish and Cancel belong
// to the main thread; Result, Abandon and the read-outs are safe anywhere.
type sttStream struct {
	opts     sttStreamOpts
	audio    chan []byte
	ended    bool
	cancel   atomic.Bool
	broken   atomic.Bool
	endpoint atomic.Bool
	partial  atomic.Value // string
	done     chan struct{}
	text     string
	words    []sttWord
	err      error
}

// sttStreamStart begins an utterance; the server starts (or is waited for) on
// the stream's own goroutine, and audio fed meanwhile waits in the queue.
func sttStreamStart(opts sttStreamOpts) *sttStream {
	s := &sttStream{opts: opts, audio: make(chan []byte, sttStreamQueue), done: make(chan struct{})}
	s.partial.Store("")
	go s.run()
	return s
}

// Feed queues 16 kHz mono PCM. It never blocks: a queue that is full means
// the recognizer fell behind, and the caller's one-shot fallback takes over.
func (s *sttStream) Feed(pcm []byte) {
	if s == nil || s.ended || len(pcm) == 0 {
		return
	}
	select {
	case s.audio <- append([]byte(nil), pcm...):
	default:
		s.broken.Store(true)
	}
}

// Finish ends the audio; the final words follow on Done.
func (s *sttStream) Finish() {
	if s == nil || s.ended {
		return
	}
	s.ended = true
	close(s.audio)
}

// Cancel drops the utterance.
func (s *sttStream) Cancel() {
	if s == nil {
		return
	}
	s.cancel.Store(true)
	s.Finish()
}

// Abandon drops the utterance from any goroutine once Finish has run.
func (s *sttStream) Abandon() {
	if s != nil {
		s.cancel.Store(true)
	}
}

// Done is closed when the result is in.
func (s *sttStream) Done() <-chan struct{} { return s.done }

// Partial is the words heard so far ("" before any).
func (s *sttStream) Partial() string {
	if s == nil {
		return ""
	}
	p, _ := s.partial.Load().(string)
	return p
}

// Endpoint reports that the recognizer heard an utterance end and nothing
// new since: the speaker has finished a sentence.
func (s *sttStream) Endpoint() bool { return s != nil && s.endpoint.Load() }

// Result waits for the final words.
func (s *sttStream) Result(ctx context.Context) (string, error) {
	select {
	case <-s.done:
	case <-ctx.Done():
		s.Abandon()
		return "", ctx.Err()
	}
	if s.err != nil {
		return "", s.err
	}
	if s.broken.Load() {
		return "", errSTTStreamBehind
	}
	return s.text, nil
}

// Words is the final result's words (with opts.words). After Done.
func (s *sttStream) Words() ([]sttWord, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.words, nil
}

func (s *sttStream) run() {
	defer close(s.done)
	defer func() {
		if r := recover(); r != nil {
			s.err = fmt.Errorf("offline speech stream stopped: %v", r)
		}
	}()
	srv, err := sttServerGet()
	if err != nil {
		s.err = err
		return
	}
	srv.busy.Add(1)
	defer srv.busy.Add(-1)
	if err := srv.waitReady(); err != nil {
		s.err = err
		return
	}
	srv.sessMu.Lock()
	defer srv.sessMu.Unlock()
	defer srv.touch()
	if s.cancel.Load() {
		// Dropped while the model loaded or another utterance ran.
		s.err = context.Canceled
		return
	}
	flags := byte(0)
	if s.opts.words {
		flags |= 1
	}
	if s.opts.background {
		flags |= 2
	}
	begin := []byte{flags}
	if len(s.opts.grammar) > 0 {
		g, _ := json.Marshal(s.opts.grammar)
		begin = append(begin, g...)
	}
	if _, _, err := srv.call('B', begin); err != nil {
		s.err = err
		return
	}
	var segs []string
	var lastPartial time.Time
	stall := time.NewTimer(sttStreamStall)
	defer stall.Stop()
	for open := true; open; {
		var chunk []byte
		select {
		case chunk, open = <-s.audio:
		case <-stall.C:
			// Nobody fed or ended it: a caller lost track of it.
			s.cancel.Store(true)
			open = false
		}
		if !open {
			break
		}
		if !stall.Stop() {
			select {
			case <-stall.C:
			default:
			}
		}
		stall.Reset(sttStreamStall)
		if s.cancel.Load() {
			continue
		}
		typ := byte('A')
		if s.opts.partialEvery > 0 && time.Since(lastPartial) >= s.opts.partialEvery {
			typ, lastPartial = 'P', time.Now()
		}
		rt, body, err := srv.call(typ, chunk)
		if err != nil {
			s.err = err
			return
		}
		switch rt {
		case 'R':
			text, words := sttParseResult(body)
			if text != "" {
				segs = append(segs, text)
			}
			s.words = append(s.words, words...)
			s.endpoint.Store(true)
			s.partial.Store(strings.Join(segs, " "))
		case 'P':
			var p struct {
				Partial string `json:"partial"`
			}
			if json.Unmarshal(body, &p) == nil {
				p.Partial = strings.TrimSpace(p.Partial)
				if p.Partial != "" {
					s.endpoint.Store(false)
				}
				s.partial.Store(strings.TrimSpace(strings.Join(append(segs[:len(segs):len(segs)], p.Partial), " ")))
			}
		}
	}
	rt, body, err := srv.call('E', nil)
	if s.cancel.Load() {
		s.err = context.Canceled
		return
	}
	if err != nil {
		s.err = err
		return
	}
	if rt != 'F' {
		s.err = errors.New("offline speech server: unexpected reply")
		return
	}
	text, words := sttParseResult(body)
	if text != "" {
		segs = append(segs, text)
	}
	s.words = append(s.words, words...)
	s.text = strings.TrimSpace(strings.Join(segs, " "))
}

// sttParseResult reads a Vosk result: its text and, when asked for, its
// words. Pure.
func sttParseResult(body []byte) (string, []sttWord) {
	var r struct {
		Text   string    `json:"text"`
		Result []sttWord `json:"result"`
	}
	if json.Unmarshal(body, &r) != nil {
		return "", nil
	}
	return strings.TrimSpace(r.Text), r.Result
}

// sttStreamWanted reports whether the orb should stream its clip: the
// offline recognizer will be the one asked first (Offline, or Automatic
// without a Groq key or Wi-Fi), it is installed and it can stream.
func sttStreamWanted(config *Config) bool {
	if !sttSupported() || !sttLocalReady() || sttServeProbe.state.Load() == sttServeNo {
		return false
	}
	_, localFirst, _ := sttPreviewChoice(sttModeOf(config), groqAPIKey(), IsOnline(), true)
	return localFirst
}
