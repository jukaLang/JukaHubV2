package main

// offline_ai_server.go — the offline model kept loaded while it may be asked.
//
// One llama-cli run per question loaded the 230 MB model from the SD card,
// evaluated the whole prompt on two cores and only then wrote a word: on the
// Smart Pro that was most of the wait. llama-server (in packages built since
// this file) loads the model once, while the user is still talking
// (voicePrewarm), keeps the fixed system prompt evaluated in its cache, and
// streams the answer as it is written. It listens on 127.0.0.1 only, on a
// free port picked at start, and wants a random key per start.
//
// It stops after offlineAIIdleStop without a question (offlineAIIdleInGame
// while a game is open), at once when a game starts (voiceReleaseForGame),
// when available memory falls below offlineAILowWaterMB, and with JukaHub.
// The kernel kills it first if memory runs out (oom_score_adj 1000), never
// JukaHub. A package without llama-server, or a server that will not start,
// answers with llama-cli as before (offline_ai.go).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"time"
)

const (
	offlineAIServerName = "llama-server"
	// offlineAIServerNeedMB is what a loaded server takes: the model (locked
	// in memory), a 512-token context and llama.cpp's buffers.
	offlineAIServerNeedMB = 330
	// offlineAIPrewarmMB is the free memory needed to load the model while
	// the user is still talking: the server plus room for the speech
	// recognizer, which may still be starting (voiceSTTReserveMB).
	offlineAIPrewarmMB = offlineAIServerNeedMB + voiceSTTReserveMB
	// offlineAILowWaterMB: below this an idle server lets its memory go.
	offlineAILowWaterMB  = 60
	offlineAIIdleStop    = 60 * time.Second
	offlineAIIdleInGame  = 10 * time.Second
	offlineAILoadTimeout = 45 * time.Second
	// offlineAIServerFailMax start failures in a row leave llama-cli to
	// answer for the rest of the run.
	offlineAIServerFailMax = 2
	offlineAIPredict       = 32
	offlineAIPrewarmDelay  = 800 * time.Millisecond
)

var errOfflineAIServerGone = errors.New("the offline AI server stopped")

// offlineAIServerProc is one running llama-server.
type offlineAIServerProc struct {
	cmd     *exec.Cmd
	base    string // http://127.0.0.1:<port>
	key     string
	ready   chan struct{} // closed once /health answers
	exited  chan struct{} // closed when the process is gone
	started time.Time
	log     *strings.Builder // the start of its messages, for a failure
	readyOK bool             // under offlineAIServer.mu
}

// offlineAIServer is the running server, if any. Any goroutine, under mu.
var offlineAIServer struct {
	mu       sync.Mutex
	p        *offlineAIServerProc
	busy     int
	lastUse  time.Time
	fails    int
	watching bool
	ahead    time.Time // the last model read-ahead (packages without a server)
}

// offlineAIHTTP talks to the loopback server only: no proxy, no timeout of
// its own (a stream is bounded by its context).
var offlineAIHTTP = &http.Client{Transport: &http.Transport{
	Proxy:               nil,
	DisableCompression:  true,
	MaxIdleConnsPerHost: 2,
	IdleConnTimeout:     30 * time.Second,
}}

// offlineAIServerIn is dir's llama-server, "" when the package predates it.
func offlineAIServerIn(dir string) string {
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, offlineAIServerName)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ""
	}
	return path
}

// offlineAIServerFiles is the server and model of the package in use ("" when
// there is no server).
func offlineAIServerFiles() (server, model string) {
	bin, model := offlineAIFiles()
	if bin == "" {
		return "", ""
	}
	if server = offlineAIServerIn(filepath.Dir(bin)); server == "" {
		return "", ""
	}
	return server, model
}

// offlineAIServerArgs are the server's options. build-llm.sh starts its smoke
// test with the same list, so an option the pinned llama.cpp lacks fails the
// build: keep the two the same. Three threads generate, leaving a core for
// the interface and the voice; all four read the prompt.
func offlineAIServerArgs(model string, port int, key string) []string {
	return []string{"-m", model, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--api-key", key,
		"-c", "512", "-np", "1", "-t", "3", "-tb", "4", "-b", "64", "-ub", "64", "-ngl", "0",
		"--mlock", "--no-warmup"}
}

// offlineAIServerReady reports whether a loaded server is waiting.
func offlineAIServerReady() bool {
	offlineAIServer.mu.Lock()
	defer offlineAIServer.mu.Unlock()
	return offlineAIServer.p != nil && offlineAIServer.p.readyOK
}

// offlineAIThinkingTitle is the orb's line while an offline answer is on its
// way: the model may still be loading.
func offlineAIThinkingTitle() string {
	if server, _ := offlineAIServerFiles(); server != "" && !offlineAIServerReady() {
		return "Loading offline AI…"
	}
	return "Thinking offline…"
}

// offlineAIPrewarm loads the model in the background when there is room for
// it beside the speech recognizer (avail is MemAvailable in MB). It reports
// whether a server is running or starting. A package without a server gets
// its model file read ahead instead, so llama-cli finds it in the page cache.
func offlineAIPrewarm(avail uint64) bool {
	if !offlineAIReady() {
		return false
	}
	server, model := offlineAIServerFiles()
	if server == "" {
		if avail >= offlineAIPrewarmMB {
			offlineAIReadAhead()
		}
		return false
	}
	offlineAIServer.mu.Lock()
	running := offlineAIServer.p != nil
	if running {
		offlineAIServer.lastUse = time.Now()
	}
	retired := offlineAIServer.fails >= offlineAIServerFailMax
	offlineAIServer.mu.Unlock()
	if running {
		return true
	}
	if retired {
		return false
	}
	if avail < offlineAIPrewarmMB {
		logDebug("voice", "offline AI not preloaded: %d MB free, %d MB wanted", avail, offlineAIPrewarmMB)
		return false
	}
	// Speech recognition loads first: the card reads one file at a time
	// best, and its answer is needed first.
	go func() {
		time.Sleep(offlineAIPrewarmDelay)
		if runningGameChildren.Load() == 0 {
			offlineAIServerStart(server, model, "preload")
		}
	}()
	return true
}

// offlineAIReadAhead reads the model file once into the page cache, at most
// every 30 s. Any goroutine.
func offlineAIReadAhead() {
	_, model := offlineAIFiles()
	offlineAIServer.mu.Lock()
	if model == "" || time.Since(offlineAIServer.ahead) < 30*time.Second {
		offlineAIServer.mu.Unlock()
		return
	}
	offlineAIServer.ahead = time.Now()
	offlineAIServer.mu.Unlock()
	go func() {
		f, err := os.Open(model)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(f, offlineAIModelSize))
	}()
}

// offlineAIServerStart starts a server, or returns the one already running.
// nil: it cannot start (no free port, the program refused, too many failures).
func offlineAIServerStart(server, model, why string) *offlineAIServerProc {
	offlineAIServer.mu.Lock()
	defer offlineAIServer.mu.Unlock()
	if p := offlineAIServer.p; p != nil {
		offlineAIServer.lastUse = time.Now()
		return p
	}
	if offlineAIServer.fails >= offlineAIServerFailMax {
		return nil
	}
	port, err := offlineAIFreePort()
	if err != nil {
		logWarn("voice", "offline AI server: no free port: %v", err)
		offlineAIServer.fails++
		return nil
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil
	}
	p := &offlineAIServerProc{
		base:    "http://127.0.0.1:" + strconv.Itoa(port),
		key:     hex.EncodeToString(raw[:]),
		ready:   make(chan struct{}),
		exited:  make(chan struct{}),
		started: time.Now(),
		log:     &strings.Builder{},
	}
	cmd := exec.Command(server, offlineAIServerArgs(model, port, p.key)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &offlineAILogWriter{b: p.log}
	feToolProcAttr(cmd) // it dies with JukaHub
	if err := cmd.Start(); err != nil {
		logWarn("voice", "offline AI server did not start: %v", err)
		offlineAIServer.fails++
		return nil
	}
	p.cmd = cmd
	// When memory runs out, the kernel ends the model, not JukaHub.
	_ = os.WriteFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "oom_score_adj"), []byte("1000"), 0o644)
	offlineAIServer.p = p
	offlineAIServer.lastUse = time.Now()
	logDebug("voice", "offline AI server starting (%s)", why)
	go func() {
		err := cmd.Wait()
		offlineAIServer.mu.Lock()
		if offlineAIServer.p == p {
			offlineAIServer.p = nil
			if !p.readyOK {
				offlineAIServer.fails++
				logWarn("voice", "offline AI server exited while loading: %v (%s)", err, firstLine(strings.TrimSpace(p.log.String())))
			}
		}
		offlineAIServer.mu.Unlock()
		close(p.exited)
	}()
	go offlineAIServerWaitReady(p)
	if !offlineAIServer.watching {
		offlineAIServer.watching = true
		go offlineAIServerWatch()
	}
	return p
}

// offlineAILogWriter keeps the first 4 KB of the server's messages.
type offlineAILogWriter struct{ b *strings.Builder }

func (w *offlineAILogWriter) Write(p []byte) (int, error) {
	if room := 4<<10 - w.b.Len(); room > 0 {
		w.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// offlineAIFreePort is a loopback port nothing listens on now.
func offlineAIFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok || addr.Port == 0 {
		return 0, errors.New("no port")
	}
	return addr.Port, nil
}

// offlineAIServerWaitReady polls /health until the model is loaded, then
// evaluates the fixed start of the prompt, so a question only adds its own
// words. A server that does not load in offlineAILoadTimeout is stopped.
func offlineAIServerWaitReady(p *offlineAIServerProc) {
	deadline := time.Now().Add(offlineAILoadTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-p.exited:
			return
		case <-time.After(150 * time.Millisecond):
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/health", nil)
		if err != nil {
			cancel()
			break
		}
		// /health is public in this llama.cpp; the key costs nothing if a
		// later one guards it.
		req.Header.Set("Authorization", "Bearer "+p.key)
		resp, err := offlineAIHTTP.Do(req)
		ok := false
		if err == nil {
			ok = resp.StatusCode == http.StatusOK
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
		}
		cancel()
		if !ok {
			continue
		}
		offlineAIServer.mu.Lock()
		p.readyOK = true
		offlineAIServer.fails = 0
		offlineAIServer.lastUse = time.Now()
		offlineAIServer.mu.Unlock()
		close(p.ready)
		logInfo("voice", "offline AI loaded in %s", time.Since(p.started).Round(time.Millisecond))
		offlineAIServerPrime(p)
		return
	}
	logWarn("voice", "offline AI server did not load in %s", offlineAILoadTimeout)
	// Counted as a failed start (the wait goroutine does not count a stop it
	// did not see coming), so a server that never loads is retired.
	offlineAIServer.mu.Lock()
	if offlineAIServer.p == p {
		offlineAIServer.fails++
	}
	offlineAIServer.mu.Unlock()
	offlineAIServerStopProc(p, "load timeout")
}

// offlineAIServerPrime has the server read the system prompt once, so its
// cache already holds it when the question comes (cache_prompt).
func offlineAIServerPrime(p *offlineAIServerProc) {
	prompt := offlineAIPrompt("", false)
	if i := strings.LastIndex(prompt, "<|im_start|>user\n"); i >= 0 {
		prompt = prompt[:i+len("<|im_start|>user\n")]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := time.Now()
	resp, err := offlineAIServerPost(ctx, p, offlineAIRequestBody(prompt, 1, false))
	if err != nil {
		logDebug("voice", "offline AI prompt cache not primed: %v", err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	logDebug("voice", "offline AI prompt cached in %s", time.Since(started).Round(time.Millisecond))
}

// offlineAIRequestBody is a /completion request.
func offlineAIRequestBody(prompt string, predict int, stream bool) []byte {
	body, _ := json.Marshal(map[string]any{
		"prompt":       prompt,
		"n_predict":    predict,
		"temperature":  0.2,
		"cache_prompt": true,
		"stream":       stream,
		"stop":         []string{"<|im_end|>", "<|im_start|>"},
	})
	return body
}

// offlineAIServerPost sends a completion request; a status other than 200 is
// an error.
func offlineAIServerPost(ctx context.Context, p *offlineAIServerProc, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/completion", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.key)
	resp, err := offlineAIHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("offline AI server: %s (%s)", resp.Status, firstLine(strings.TrimSpace(string(msg))))
	}
	return resp, nil
}

// offlineAIReadStream copies the words of a streamed completion into w as
// they come: server-sent events whose data is {"content": …, "stop": …}. It
// returns at the last event, an error event, or the end of the stream. Pure
// apart from w.
func offlineAIReadStream(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 256<<10)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimSpace(line[len("data:"):])
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var ev struct {
				Content string `json:"content"`
				Stop    bool   `json:"stop"`
				Error   *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(payload), &ev) != nil {
				continue
			}
			if ev.Error != nil {
				return fmt.Errorf("offline AI server: %s", ev.Error.Message)
			}
			if ev.Content != "" {
				if _, err := w.Write([]byte(ev.Content)); err != nil {
					return err
				}
			}
			if ev.Stop {
				return nil
			}
		case strings.HasPrefix(line, "error:"):
			return fmt.Errorf("offline AI server: %s", strings.TrimSpace(line[len("error:"):]))
		}
	}
	return sc.Err()
}

// offlineAIServerAnswer asks the server, starting it when it is not running.
// fallback: the server could not answer for a reason llama-cli may not share
// (it would not start, it stopped, it refused the request) and nothing was
// written yet.
func offlineAIServerAnswer(ctx context.Context, server, model, prompt string, output *offlineAIStreamWriter) (fallback bool, err error) {
	offlineAIServer.mu.Lock()
	p := offlineAIServer.p
	offlineAIServer.mu.Unlock()
	if p == nil {
		if freeMemoryMB() < offlineAIStartMB {
			return false, errOfflineAIMemory
		}
		if p = offlineAIServerStart(server, model, "question"); p == nil {
			return true, errOfflineAIServerGone
		}
	}
	offlineAIServer.mu.Lock()
	offlineAIServer.busy++
	offlineAIServer.mu.Unlock()
	defer func() {
		offlineAIServer.mu.Lock()
		offlineAIServer.busy--
		offlineAIServer.lastUse = time.Now()
		offlineAIServer.mu.Unlock()
	}()
	select {
	case <-p.ready:
	case <-p.exited:
		return true, errOfflineAIServerGone
	case <-ctx.Done():
		return false, ctx.Err()
	}
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output.stop = cancel // the first whole sentence ends the request
	started := time.Now()
	resp, err := offlineAIServerPost(reqCtx, p, offlineAIRequestBody(prompt, offlineAIPredict, true))
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, err
	}
	err = offlineAIReadStream(resp.Body, output)
	resp.Body.Close()
	logDebug("voice", "offline AI (server) answered in %s", time.Since(started).Round(time.Millisecond))
	switch {
	case output.sentence != "":
		return false, nil
	case ctx.Err() != nil:
		return false, ctx.Err()
	case err != nil && output.raw.Len() == 0:
		return true, err
	}
	return false, err
}

// offlineAIServerWatch stops an idle server, and one that holds memory the
// rest of the handheld needs. One goroutine while a server runs.
func offlineAIServerWatch() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		offlineAIServer.mu.Lock()
		p := offlineAIServer.p
		if p == nil {
			offlineAIServer.watching = false
			offlineAIServer.mu.Unlock()
			return
		}
		idle := time.Since(offlineAIServer.lastUse)
		busy := offlineAIServer.busy > 0
		offlineAIServer.mu.Unlock()
		limit := offlineAIIdleStop
		if runningGameChildren.Load() > 0 {
			limit = offlineAIIdleInGame
		}
		switch {
		case busy:
		case idle > limit:
			offlineAIServerStopProc(p, "idle")
		case freeMemoryMB() < offlineAILowWaterMB:
			offlineAIServerStopProc(p, "low memory")
		}
	}
}

// offlineAIServerStop ends the running server, if any. Any goroutine.
func offlineAIServerStop(why string) {
	offlineAIServer.mu.Lock()
	p := offlineAIServer.p
	offlineAIServer.mu.Unlock()
	if p != nil {
		offlineAIServerStopProc(p, why)
	}
}

// offlineAIServerStopProc ends p. Its wait goroutine clears the state.
func offlineAIServerStopProc(p *offlineAIServerProc, why string) {
	offlineAIServer.mu.Lock()
	if offlineAIServer.p == p {
		offlineAIServer.p = nil
	}
	offlineAIServer.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	logDebug("voice", "offline AI server stopped (%s)", why)
}
