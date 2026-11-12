package main

// webdom_runtime.go — the built-in browser's JavaScript pass, with a real DOM.
//
// webscript.go used to hand a page's scripts a stub document whose every
// lookup returned null, so a page that builds its content in script — a
// framework app, a list fetched as JSON, a lazy image, a web component — drew
// as an empty shell. This pass gives those scripts a working document:
//
//  1. the page is parsed into a tree in Go (webdom_parse.go) and handed to the
//     DOM prelude (webdom/*.js, embedded) as JSON;
//  2. the page's scripts run in document order against that DOM — classic
//     scripts, then deferred and module scripts (webdom_module.go rewrites
//     import/export, which goja cannot parse), then async and dynamically
//     inserted ones — with timers, fetch/XHR (webdom_net.go) and events driven
//     by a small event loop on a virtual clock;
//  3. the DOM is serialised back to HTML, so the renderer, the link picker and
//     the stylesheet and image passes all see what the scripts built.
//
// The runtime is single-goroutine: only the goroutine that runs the pass ever
// touches goja. Network requests run on their own goroutines and hand their
// results back over a channel; the loop delivers them between tasks.
//
// Everything is bounded for the handheld (1 GB of RAM, a Cortex-A53): one
// wall-clock budget for the whole pass, a cap on each call into the engine, a
// ceiling on live-heap growth, caps on scripts, tasks and timers. A pass that
// cannot start, panics, or meets a page too large for it falls back to the old
// document.write-only pass (runWebPageScriptsLegacy in webscript.go).

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dlclark/regexp2/v2"
	"github.com/dop251/goja"
)

//go:embed webdom/*.js
var webDOMFiles embed.FS

const (
	// webDOMMaxSourceBytes is the largest page the DOM pass takes on. Parsing
	// is cheap; the engine's object for every node and the serialised copy
	// are what cost memory, so a bigger page keeps the old splice-only pass.
	webDOMMaxSourceBytes = 4 << 20
	// webDOMMaxNodes bounds the parsed tree, and every innerHTML parse.
	webDOMMaxNodes = 60000
	// webJSMaxFragmentBytes bounds one innerHTML/DOMParser input.
	webJSMaxFragmentBytes = 4 << 20
	// webJSSerializeGrace is the time serialisation still gets when the
	// budget ran out mid-page: what the scripts built so far is worth drawing.
	webJSSerializeGrace = 3 * time.Second
	// webJSMaxHeapGrowth is how far the live heap may grow during the pass
	// before it is stopped; goja has no memory limit of its own.
	webJSMaxHeapGrowth = 192 << 20
	// webJSMaxStack bounds JavaScript recursion.
	webJSMaxStack = 4096
	// webJSMaxScriptRuns and webJSMaxScriptBytes bound the code one page may
	// run: inline scripts, external ones, module bodies and string timers.
	webJSMaxScriptRuns  = 256
	webJSMaxScriptBytes = 6 << 20
	// webJSMaxScriptFetches bounds the distinct script URLs one page may load.
	webJSMaxScriptFetches = 160
	// webJSMaxPrefetch bounds one prefetch list from the prelude.
	webJSMaxPrefetch = 64
	// webJSMaxTasks bounds the calls into the engine (scripts, timers, events,
	// network completions) one page may cost.
	webJSMaxTasks = 5000
	// webJSMaxAdvance bounds the virtual time the event loop may skip; the
	// prelude stops much earlier (its timer horizon).
	webJSMaxAdvance = 10 * time.Minute
	// webJSMaxLogLines bounds console output copied into the debug log.
	webJSMaxLogLines = 40
	// webJSMaxConcurrentPasses bounds how many pages run scripts at once; a
	// load that finds no slot keeps its markup as it arrived.
	webJSMaxConcurrentPasses = 2
	// webJSTextLossMin and webJSTextLossFactor guard against scripts that
	// wipe a page: when the original had at least webJSTextLossMin characters
	// of text and the result kept less than 1/webJSTextLossFactor of it, the
	// original is drawn instead.
	webJSTextLossMin    = 2000
	webJSTextLossFactor = 3
	// webJSUserAgent is what navigator.userAgent reports. It matches the
	// header every browser request sends, so a site's server-side and
	// script-side checks agree.
	webJSUserAgent = browserChromeUserAgent
	// webJSRegexpTimeout bounds one regular-expression match (see init).
	webJSRegexpTimeout = 2 * time.Second
	// webJSMinBudget is the least time worth starting a pass with: a script
	// navigation's next page gets what is left of the load's time, and below
	// this it is drawn as it arrived.
	webJSMinBudget = 3 * time.Second
)

// init bounds every regular-expression match the engine runs. goja compiles
// every pattern with regexp2, a backtracking engine, and never sets a timeout
// of its own, while the watchdog's interrupt only lands between VM
// instructions: a page regex with catastrophic backtracking would run past
// every budget, holding a pass slot that even webJSStopRunning cannot free.
// regexp2 copies the default into each pattern when it compiles it — the
// prelude's literals are compiled after init too — and goja turns a timed-out
// match into a RangeError the page's own code may catch.
func init() {
	regexp2.DefaultMatchTimeout = webJSRegexpTimeout
}

// The pass's clocks are variables so the tests can shrink them. A pass copies
// them when it starts, so the goroutines it runs never read these.
var (
	// webJSBudget is the wall-clock budget of the whole pass, network waits
	// included. Most pages go idle long before it: the loop stops as soon as
	// no task, timer or request is left.
	webJSBudget = 10 * time.Second
	// webJSCallTimeout bounds one call into the engine (one script, one task),
	// so a runaway loop costs this much and the page moves on. Time a call
	// spends waiting on the network (sync XHR, a module graph) is not counted.
	webJSCallTimeout = 6 * time.Second
	// webJSFetchPatience is how long the event loop waits for one fetch or
	// XMLHttpRequest. Browsers never hold the page for them, and a long-poll
	// (a chat, a socket.io fallback — WebSocket is a stub here) would otherwise
	// keep the loop on the wire until the budget ran out. A request older than
	// this no longer keeps the loop waiting, nor stops it skipping virtual time;
	// a script download always does, the way a browser's parser waits.
	webJSFetchPatience = 5 * time.Second
)

// The kinds of completion a network goroutine hands back to the loop.
const (
	webJSDoneFetch = iota + 1
	webJSDoneScript
)

// webJSPageResult is what the script pass hands back to the page load.
type webJSPageResult struct {
	HTML     string // the markup to render (the original when nothing ran)
	Navigate string // where a script sent the page (location.href = …); "" for none
	Ran      bool   // the DOM pass ran the page's scripts
	Reason   string // why the pass was skipped or gave up, for the debug log
}

// browserJavaScriptKey is the Custom key of Settings → Browser → JavaScript.
const browserJavaScriptKey = "browserJavaScript"

// browserJavaScriptOff caches that setting for the page-load goroutine, which
// must not read the config's Custom map while the main thread may be writing
// it. refreshBrowserJavaScriptSetting runs on the main thread when a page load
// starts (browserLoadURL) and when Settings changes the value, so every load
// reads the value in force when it began; before the first load scripts are
// on, the default.
var browserJavaScriptOff atomic.Bool

// browserJavaScriptSetting reads the setting from a config. Main thread only.
func browserJavaScriptSetting(c *Config) bool {
	if c == nil || c.Variables.Custom == nil {
		return true
	}
	v, ok := customValue(c.Variables.Custom, browserJavaScriptKey)
	if !ok {
		return true
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "false", "0", "no", "off":
			return false
		}
	case float64:
		return t != 0
	}
	return true
}

// refreshBrowserJavaScriptSetting copies the setting into the cache the page
// load reads. Main thread only.
func refreshBrowserJavaScriptSetting(c *Config) {
	browserJavaScriptOff.Store(!browserJavaScriptSetting(c))
}

// browserJavaScriptEnabled reports whether page scripts run. Safe from any
// goroutine.
func browserJavaScriptEnabled() bool { return !browserJavaScriptOff.Load() }

// webJSSettingsRow is Settings → Browser → JavaScript. Off draws every page as
// its server sent it, which is also the quick test of whether a page that
// looks wrong was changed by its own scripts.
func webJSSettingsRow() setRow {
	return setRow{
		ID: "javaScript", Label: "JavaScript", Kind: setKindToggle,
		Detail: setStatic("Run page scripts, so pages that build their content show it"),
		Get:    browserJavaScriptSetting,
		Set: func(c *Config, on bool) {
			setStoreBool(c, browserJavaScriptKey, on)
			refreshBrowserJavaScriptSetting(c)
			setToast("JavaScript: " + setOnOff(on))
		},
	}
}

// webJSPasses is the slot pool behind webJSMaxConcurrentPasses.
var webJSPasses = make(chan struct{}, webJSMaxConcurrentPasses)

// webJSSlotWait is how long a page waits for a free slot before it is drawn
// without its scripts.
const webJSSlotWait = 2 * time.Second

// webJSSuperseded is the abort reason of a pass whose page the reader left.
const webJSSuperseded = "superseded"

// webJSRunning is the set of passes in flight. A pass registers once its
// runtime exists, so webJSStopRunning can interrupt it from another goroutine.
var webJSRunning = struct {
	sync.Mutex
	sessions map[*webJSSession]struct{}
}{sessions: map[*webJSSession]struct{}{}}

// webJSGeneration counts page loads: webJSStopRunning moves it on. A pass
// belongs to the generation its load started in (browseWebPageResult reads it
// once, at its start) and is not started, or gives up, once a newer load has
// begun — whether it was still downloading, waiting for a slot or running.
var webJSGeneration atomic.Uint64

// webJSLoadGeneration is the generation a page load starting now belongs to.
func webJSLoadGeneration() uint64 { return webJSGeneration.Load() }

// errWebJSSuperseded is start()'s answer when a newer load began while the
// pass was getting ready.
var errWebJSSuperseded = errors.New("a newer page load began")

// webJSStopRunning ends every script pass in flight. The browser calls it when
// a new load starts: the page a running pass belongs to is no longer the one
// the reader is waiting for (its result would be dropped as stale), and on the
// handheld's four slow cores it would otherwise keep running scripts for up
// to the whole budget beside the page that is. The generation moves first, so
// a pass that registers after the loop below sees it and stops itself.
func webJSStopRunning() {
	webJSGeneration.Add(1)
	webJSRunning.Lock()
	defer webJSRunning.Unlock()
	for s := range webJSRunning.sessions {
		s.stop(webJSSuperseded)
	}
}

// stop aborts the pass from any goroutine: the running call is interrupted,
// network waits end, and the phases that follow see stopping().
func (s *webJSSession) stop(reason string) {
	s.mu.Lock()
	if s.abort == "" {
		s.abort = reason
	}
	if s.rt != nil && !s.callStart.IsZero() {
		s.rt.Interrupt(reason)
	}
	s.mu.Unlock()
	s.cancel()
}

// webJSRunPage runs a page's scripts against a DOM and returns the markup the
// renderer should draw, with the whole budget and the current generation.
func webJSRunPage(source string, base *url.URL) webJSPageResult {
	return webJSRunPageFor(source, base, webJSLoadGeneration(), time.Time{})
}

// webJSRunPageFor is webJSRunPage for one page load: gen is the generation the
// load started in, and a non-zero deadline caps the pass (a script
// navigation's next page gets only what is left of the load's time). It never
// fails: anything that goes wrong leaves the page as the legacy pass (or the
// network) delivered it, and a load the reader has left gets its markup back
// untouched.
func webJSRunPageFor(source string, base *url.URL, gen uint64, deadline time.Time) (res webJSPageResult) {
	res.HTML = source
	budget, short := webJSBudget, false
	if !deadline.IsZero() {
		budget = min(budget, time.Until(deadline))
		short = budget < webJSMinBudget
	}
	switch {
	case base == nil:
		res.Reason = "no page address"
		return res
	case !browserJavaScriptEnabled():
		res.Reason = "JavaScript is off"
		return res
	case webJSGeneration.Load() != gen:
		res.Reason = "stopped: a new page is loading"
		return res
	case !webJSPageNeedsDOM(source):
		res.Reason = "nothing to run"
		return res
	case short:
		res.Reason = "too little time left for scripts"
		return res
	case len(source) > webDOMMaxSourceBytes:
		res.HTML = webJSLegacyPass(source, base)
		res.Reason = "page too large for the DOM pass"
		return res
	}
	// A new page load stops the passes still running for pages the reader
	// left (webJSStopRunning), so a slot is normally free; the short wait
	// covers a stopped pass that is still unwinding.
	slot := time.NewTimer(webJSSlotWait)
	select {
	case webJSPasses <- struct{}{}:
		slot.Stop()
		defer func() { <-webJSPasses }()
	case <-slot.C:
		res.Reason = "another page is running its scripts"
		return res
	}
	// The reader may have moved on while this load waited for its slot.
	if webJSGeneration.Load() != gen {
		res.Reason = "stopped: a new page is loading"
		return res
	}
	defer func() {
		if r := recover(); r != nil {
			logWarn("browser", "script pass failed on %s: %v", webJSLogURL(base), r)
			res = webJSPageResult{HTML: webJSLegacyPass(source, base), Reason: fmt.Sprintf("panic: %v", r)}
		}
	}()
	out, err := runWebDOMPage(source, base, gen, budget)
	if err != nil {
		logDebug("browser", "DOM script pass not used on %s: %v", webJSLogURL(base), err)
		return webJSPageResult{HTML: webJSLegacyPass(source, base), Reason: err.Error()}
	}
	return out
}

// webJSLegacyPass is the document.write-only pass, with its own recover: it is
// the fallback, so it must not take the page load down either.
func webJSLegacyPass(source string, base *url.URL) (out string) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("browser", "legacy script pass failed on %s: %v", webJSLogURL(base), r)
			out = source
		}
	}()
	return runWebPageScriptsLegacy(source, base)
}

// webJSPageNeedsDOM reports whether the DOM pass has anything to do: a script
// it would run, or markup only it can unfold (declarative shadow roots, lazy
// images). A page with neither is returned exactly as it arrived.
func webJSPageNeedsDOM(source string) bool {
	lower := asciiLower(source)
	for from := 0; from < len(lower); {
		i := strings.Index(lower[from:], "<script")
		if i < 0 {
			break
		}
		i += from
		end := findTextBrowserTagEnd(source, i+len("<script"))
		if end < 0 {
			break
		}
		from = end + 1
		// <scripts> or <script-card> are other elements.
		switch lower[i+len("<script")] {
		case ' ', '\t', '\n', '\r', '\f', '>', '/':
		default:
			continue
		}
		if webJSRunnableScriptTag(source[i+1 : end]) {
			return true
		}
	}
	// "data-src" also finds data-srcset, and an attribute that starts a line.
	for _, marker := range []string{"shadowrootmode", "data-src", "data-lazy-src", "data-original", "data-lazy-srcset"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// webJSRunnableScriptTag reports whether a <script> start tag (its text
// between the angle brackets) is one a browser would run.
func webJSRunnableScriptTag(tag string) bool {
	typ := strings.ToLower(strings.TrimSpace(htmlAttrValue(htmlTagAttribute(tag, "type"))))
	if i := strings.IndexByte(typ, ';'); i >= 0 {
		typ = strings.TrimSpace(typ[:i])
	}
	return webJSRunnableType(typ)
}

// webJSRunnableType is the HTML standard's list of JavaScript MIME types, plus
// "module" and the empty type.
func webJSRunnableType(typ string) bool {
	switch typ {
	case "", "module", "text/javascript", "application/javascript", "application/ecmascript",
		"application/x-ecmascript", "application/x-javascript", "text/ecmascript",
		"text/javascript1.0", "text/javascript1.1", "text/javascript1.2", "text/javascript1.3",
		"text/javascript1.4", "text/javascript1.5", "text/jscript", "text/livescript",
		"text/x-ecmascript", "text/x-javascript":
		return true
	}
	return false
}

// webDOMPrelude is the DOM prelude, compiled once per process. A goja Program
// is immutable and may be run by any number of runtimes.
var webDOMPrelude = sync.OnceValues(func() (*goja.Program, error) {
	src, err := webDOMPreludeSource()
	if err != nil {
		return nil, err
	}
	return goja.Compile("webdom.js", src, false)
})

// webDOMPreludeWarm compiles the prelude ahead of the first page that needs
// it. It runs on a goroutine of its own, where a panic would end the app, so
// it swallows one: the pass itself calls webDOMPrelude behind its recover and
// falls back to the legacy pass.
func webDOMPreludeWarm() {
	defer func() { _ = recover() }()
	_, _ = webDOMPrelude()
}

// webDOMPreludeSource concatenates webdom/*.js in file-name order inside the
// one function scope they share. The natives object is captured as N; the
// prelude deletes the global it arrived in.
func webDOMPreludeSource() (string, error) {
	entries, err := fs.ReadDir(webDOMFiles, "webdom")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("(function (global, N) { 'use strict';\n")
	parts := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		data, err := webDOMFiles.ReadFile("webdom/" + e.Name())
		if err != nil {
			return "", err
		}
		b.Write(data)
		b.WriteString("\n")
		parts++
	}
	if parts == 0 {
		return "", errors.New("no prelude files embedded")
	}
	b.WriteString("})(this, this.__jhNative);\n")
	return b.String(), nil
}

// runWebDOMPage is one DOM pass. An error means the pass could not start and
// the legacy pass should run instead; once the page's scripts have run, the
// result always comes back (with the original markup if it had to). A pass
// that was stopped while it started — the reader left, the budget or the
// memory ceiling ran out — is over, not failed: its markup comes back as it
// arrived, without a legacy pass that would spend the same time again.
func runWebDOMPage(source string, base *url.URL, gen uint64, budget time.Duration) (webJSPageResult, error) {
	prog, err := webDOMPrelude()
	if err != nil {
		return webJSPageResult{}, fmt.Errorf("prelude: %w", err)
	}
	tree, _, truncated := webDOMParseDocument(source, webDOMMaxNodes)
	if truncated {
		return webJSPageResult{}, fmt.Errorf("page has more than %d nodes", webDOMMaxNodes)
	}
	s := newWebJSSession(base, gen, budget)
	defer s.close()
	if err := s.start(prog); err != nil {
		if stopped, ok := s.stoppedResult(source, err); ok {
			return stopped, nil
		}
		return webJSPageResult{}, err
	}
	return s.runPage(tree, source)
}

// stoppedResult is the result of a pass that was stopped before its page ran:
// ok is false when err is a real failure the legacy pass should cover.
func (s *webJSSession) stoppedResult(source string, err error) (webJSPageResult, bool) {
	if errors.Is(err, errWebJSSuperseded) {
		return webJSPageResult{HTML: source, Reason: "stopped: a new page is loading"}, true
	}
	s.mu.Lock()
	abort := s.abort
	s.mu.Unlock()
	if abort == "" {
		return webJSPageResult{}, false
	}
	if abort == webJSSuperseded {
		return webJSPageResult{HTML: source, Reason: "stopped: a new page is loading"}, true
	}
	return webJSPageResult{HTML: source, Reason: "stopped: " + abort}, true
}

// webJSHost holds the prelude's entry points (its __jhHost object).
type webJSHost struct {
	boot               goja.Callable
	nextParserScript   goja.Callable
	execScript         goja.Callable
	nextDeferredScript goja.Callable
	ready              goja.Callable
	runTask            goja.Callable
	fetchDone          goja.Callable
	scriptLoaded       goja.Callable
	serialize          goja.Callable
	stats              goja.Callable
}

// webJSDone is one network completion on its way back to the loop.
type webJSDone struct {
	kind int
	id   int64
	resp webJSResponse
}

// webJSScriptFetch is one script download, shared by everyone who asks for the
// same URL: the parser, a module graph, a prefetch hint.
type webJSScriptFetch struct {
	ready chan struct{}
	resp  webJSResponse
}

// webJSStats is the prelude's account of the pass (host.stats()).
type webJSStats struct {
	Scripts      int      `json:"scripts"`
	ScriptErrors int      `json:"scriptErrors"`
	Errors       []string `json:"errors"`
	Timers       int      `json:"timers"`
	Fetches      int      `json:"fetches"`
	Nodes        int      `json:"nodes"`
	TextBefore   int      `json:"textBefore"`
	TextAfter    int      `json:"textAfter"`
}

// webJSSession is one page's pass: the runtime, its clock, the watchdog state
// and the network bookkeeping.
type webJSSession struct {
	rt   *goja.Runtime
	page *url.URL
	net  *webJSNet
	host webJSHost

	// gen is the page load's generation (webJSGeneration); callTimeout and
	// fetchPatience are webJSCallTimeout and webJSFetchPatience as the pass
	// began, so the watchdog never reads a package variable a test may be
	// changing.
	gen           uint64
	callTimeout   time.Duration
	fetchPatience time.Duration

	ctx       context.Context
	cancel    context.CancelFunc
	closed    chan struct{}
	closeOnce sync.Once

	// wallStart anchors the virtual clock; advanced is the time the event loop
	// skipped. Both are only touched on the runtime goroutine.
	wallStart time.Time
	advanced  time.Duration

	// mu guards the watchdog state: the running call's start (zero between
	// calls), whether a native is waiting on the network, the deadline (moved
	// once, for serialisation) and why the pass was stopped.
	mu        sync.Mutex
	callStart time.Time
	blocked   bool
	deadline  time.Time
	abort     string
	heapBase  uint64

	// done carries network completions to the loop. fetches holds the start
	// of every fetch/XHR still on the wire (by the prelude's request id) and
	// scriptLoads counts script downloads still owed to the loop; both are
	// only touched on the runtime goroutine.
	done        chan webJSDone
	fetches     map[int64]time.Time
	scriptLoads int

	scriptMu sync.Mutex
	scripts  map[string]*webJSScriptFetch

	scriptRuns  int
	scriptBytes int
	tasks       int
	callErrors  int
	logLines    int
	nav         string
}

func newWebJSSession(page *url.URL, gen uint64, budget time.Duration) *webJSSession {
	now := time.Now()
	deadline := now.Add(budget)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	s := &webJSSession{
		page:          page,
		gen:           gen,
		callTimeout:   webJSCallTimeout,
		fetchPatience: webJSFetchPatience,
		ctx:           ctx,
		cancel:        cancel,
		closed:        make(chan struct{}),
		wallStart:     now,
		deadline:      deadline,
		heapBase:      webLiveHeapBytes(),
		done:          make(chan webJSDone, 64),
		fetches:       map[int64]time.Time{},
		scripts:       map[string]*webJSScriptFetch{},
	}
	s.net = newWebJSNet(page, deadline)
	return s
}

// close ends the pass: goroutines still on the wire stop delivering, and the
// requests behind them are cancelled.
func (s *webJSSession) close() {
	s.closeOnce.Do(func() {
		webJSRunning.Lock()
		delete(webJSRunning.sessions, s)
		webJSRunning.Unlock()
		close(s.closed)
		s.cancel()
	})
}

// start builds the runtime, installs the natives, runs the prelude and picks
// up its entry points.
func (s *webJSSession) start(prog *goja.Program) error {
	rt := goja.New()
	s.rt = rt
	// Registered only now that the runtime exists: stop reads s.rt, and the
	// registry's lock is what orders that read after this write.
	webJSRunning.Lock()
	webJSRunning.sessions[s] = struct{}{}
	webJSRunning.Unlock()
	// A load that began before the registration did not see this pass; its
	// generation did move, so the check after registering cannot miss it.
	if webJSGeneration.Load() != s.gen {
		s.stop(webJSSuperseded)
		return errWebJSSuperseded
	}
	rt.SetMaxCallStackSize(webJSMaxStack)
	rt.SetTimeSource(s.now)
	if err := rt.Set("__jhNative", s.natives()); err != nil {
		return fmt.Errorf("natives: %w", err)
	}
	go s.watch()
	s.beginCall()
	_, err := rt.RunProgram(prog)
	s.endCall()
	if err != nil {
		return fmt.Errorf("prelude: %w", err)
	}
	hostValue := rt.Get("__jhHost")
	if hostValue == nil || goja.IsUndefined(hostValue) || goja.IsNull(hostValue) {
		return errors.New("prelude defined no host")
	}
	host := hostValue.ToObject(rt)
	_ = rt.GlobalObject().Delete("__jhHost")
	for _, entry := range []struct {
		name string
		dst  *goja.Callable
	}{
		{"boot", &s.host.boot},
		{"nextParserScript", &s.host.nextParserScript},
		{"execScript", &s.host.execScript},
		{"nextDeferredScript", &s.host.nextDeferredScript},
		{"ready", &s.host.ready},
		{"runTask", &s.host.runTask},
		{"fetchDone", &s.host.fetchDone},
		{"scriptLoaded", &s.host.scriptLoaded},
		{"serialize", &s.host.serialize},
		{"stats", &s.host.stats},
	} {
		fn, ok := goja.AssertFunction(host.Get(entry.name))
		if !ok {
			return fmt.Errorf("prelude host has no %s", entry.name)
		}
		*entry.dst = fn
	}
	return nil
}

// runPage drives the page through the phases a browser goes through: parser
// scripts, interactive, deferred and module scripts, DOMContentLoaded, the
// event loop, load, the event loop again — then serialises.
func (s *webJSSession) runPage(tree, source string) (webJSPageResult, error) {
	res := webJSPageResult{HTML: source, Ran: true}
	v, err := s.call(s.host.boot, s.rt.ToValue(tree), s.rt.ToValue(s.page.String()), s.rt.ToValue(s.configJSON()))
	if err != nil {
		if stopped, ok := s.stoppedResult(source, err); ok {
			return stopped, nil
		}
		return webJSPageResult{}, fmt.Errorf("boot: %w", err)
	}
	s.prefetchBoot(v)
	s.runScripts(s.host.nextParserScript)
	if !s.stopping() {
		_, _ = s.call(s.host.ready, s.rt.ToValue("interactive"))
	}
	s.runScripts(s.host.nextDeferredScript)
	if !s.stopping() {
		_, _ = s.call(s.host.ready, s.rt.ToValue("domcontentloaded"))
	}
	s.loop()
	if !s.stopping() {
		_, _ = s.call(s.host.ready, s.rt.ToValue("complete"))
		s.loop()
	}

	// Whatever the scripts built is worth drawing even when the budget ran
	// out mid-page, so serialisation gets its own few seconds — unless memory
	// is what stopped the pass, when building one more copy is the wrong move.
	s.mu.Lock()
	if grace := time.Now().Add(webJSSerializeGrace); grace.After(s.deadline) {
		s.deadline = grace
	}
	abort := s.abort
	s.mu.Unlock()
	res.Navigate = s.nav
	if abort == "memory" {
		res.Reason = "stopped: memory limit"
		logDebug("browser", "scripts on %s stopped at the memory limit", webJSLogURL(s.page))
		return res, nil
	}
	if abort == webJSSuperseded {
		// The reader has moved on and this result will be dropped as stale:
		// serialising it would only take CPU from the page now loading.
		res.Navigate = ""
		res.Reason = "stopped: a new page is loading"
		return res, nil
	}
	hv, err := s.call(s.host.serialize)
	if err != nil || hv == nil || goja.IsUndefined(hv) || goja.IsNull(hv) {
		res.Reason = "serialisation failed"
		return res, nil
	}
	out := hv.String()
	st := s.pageStats()
	switch {
	case out == "":
		res.Reason = "serialisation was empty"
	case st.TextBefore >= webJSTextLossMin && st.TextAfter*webJSTextLossFactor < st.TextBefore:
		res.Reason = fmt.Sprintf("scripts left %d of %d characters of text", st.TextAfter, st.TextBefore)
	default:
		res.HTML = out
		res.Reason = abort
	}
	requests, bytes, blocked := s.net.stats()
	logDebug("browser", "scripts on %s: %d run (%d failed), %d tasks, %d timers, %d requests (%d KiB, %d blocked), %d nodes, %s%s",
		webJSLogURL(s.page), st.Scripts, st.ScriptErrors, s.tasks, st.Timers, requests, bytes>>10, blocked,
		st.Nodes, time.Since(s.wallStart).Round(time.Millisecond), webJSReasonSuffix(res.Reason))
	for i, msg := range st.Errors {
		if i >= 5 {
			break
		}
		logDebug("browser", "script error on %s: %s", webJSLogURL(s.page), webJSClip(msg, 300))
	}
	return res, nil
}

// webJSReasonSuffix formats a reason for the summary line.
func webJSReasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

// pageStats reads host.stats(); a failure reads as an empty account.
func (s *webJSSession) pageStats() webJSStats {
	var st webJSStats
	v, err := s.call(s.host.stats)
	if err != nil || v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return st
	}
	_ = json.Unmarshal([]byte(v.String()), &st)
	return st
}

// prefetchBoot starts the downloads boot() listed: the parser-blocking and
// deferred classic scripts, and the module entries and modulepreload hints, so
// they arrive side by side instead of one after another.
func (s *webJSSession) prefetchBoot(v goja.Value) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return
	}
	var plan struct {
		Scripts []string `json:"scripts"`
		Modules []string `json:"modules"`
	}
	if json.Unmarshal([]byte(v.String()), &plan) != nil {
		return
	}
	for i, src := range plan.Scripts {
		if i >= webJSMaxPrefetch {
			break
		}
		s.prefetchScript(src, false)
	}
	for i, src := range plan.Modules {
		if i >= webJSMaxPrefetch {
			break
		}
		s.prefetchScript(src, true)
	}
}

// runScripts runs the scripts one of the prelude's iterators hands out, in
// order, waiting for each download. Nothing else runs in between: a
// parser-blocking script blocks.
func (s *webJSSession) runScripts(next goja.Callable) {
	for i := 0; i < webJSMaxScriptRuns && !s.stopping(); i++ {
		v, err := s.call(next)
		if err != nil || v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			return
		}
		info := v.ToObject(s.rt)
		id := webJSValue(info.Get("id"))
		src := webJSString(info.Get("src"))
		if src == "" {
			_, _ = s.call(s.host.execScript, id, goja.Null(), goja.Null(), goja.Null())
			continue
		}
		resp := s.awaitScript(s.prefetchScript(src, webJSBool(info.Get("module"))))
		if resp.Err != "" {
			_, _ = s.call(s.host.execScript, id, goja.Null(), s.rt.ToValue(webJSOr(resp.URL, src)), s.rt.ToValue(resp.Err))
			continue
		}
		_, _ = s.call(s.host.execScript, id, s.rt.ToValue(webJSDecodeScript(resp)), s.rt.ToValue(webJSOr(resp.URL, src)), goja.Null())
	}
}

// loop is the event loop: deliver what the network finished, run one task,
// and when nothing is runnable either wait for the network or stop. Virtual
// time is only fast-forwarded while nothing worth waiting for is on the wire,
// so a page's "give up after ten seconds" timer cannot beat a response that is
// 300 ms away. A script download holds the loop until the budget runs out, a
// fetch only for webJSFetchPatience: a long-poll must not hold the page.
func (s *webJSSession) loop() {
	failures := 0
	for !s.stopping() {
		s.drain()
		if s.stopping() {
			return
		}
		pending, patienceEnds := s.pendingWork(time.Now())
		v, err := s.call(s.host.runTask, s.rt.ToValue(!pending))
		if err != nil {
			// A task that threw or hit the per-call cap is over; the page goes
			// on. A loop that keeps failing is a prelude bug, not a page.
			failures++
			if failures > 50 {
				return
			}
			continue
		}
		wait := int64(-1)
		if v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
			wait = v.ToInteger()
		}
		if wait == -2 {
			continue
		}
		if !pending {
			return
		}
		s.mu.Lock()
		timeout := time.Until(s.deadline)
		s.mu.Unlock()
		if wait >= 0 && time.Duration(wait)*time.Millisecond < timeout {
			timeout = time.Duration(wait) * time.Millisecond
		}
		if !patienceEnds.IsZero() {
			timeout = min(timeout, time.Until(patienceEnds))
		}
		if timeout < time.Millisecond {
			timeout = time.Millisecond
		}
		timer := time.NewTimer(timeout)
		select {
		case ev := <-s.done:
			timer.Stop()
			s.deliver(ev)
		case <-timer.C:
		case <-s.ctx.Done():
			timer.Stop()
			if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
				s.noteAbort("time budget")
			}
			return
		}
	}
}

// pendingWork reports whether the loop should wait on the network: a script
// download is owed, or a fetch younger than the fetch patience is on the
// wire. patienceEnds is when the last such fetch stops counting — zero when a
// script download is being waited for (that wait lasts the whole budget) or
// nothing is.
func (s *webJSSession) pendingWork(now time.Time) (pending bool, patienceEnds time.Time) {
	if s.scriptLoads > 0 {
		return true, time.Time{}
	}
	for _, started := range s.fetches {
		if ends := started.Add(s.fetchPatience); ends.After(now) {
			pending = true
			if ends.After(patienceEnds) {
				patienceEnds = ends
			}
		}
	}
	return pending, patienceEnds
}

// noteAbort records why the pass stopped; the first reason stays.
func (s *webJSSession) noteAbort(reason string) {
	s.mu.Lock()
	if s.abort == "" {
		s.abort = reason
	}
	s.mu.Unlock()
}

// drain delivers every completion already waiting, without blocking.
func (s *webJSSession) drain() {
	for {
		select {
		case ev := <-s.done:
			s.deliver(ev)
			if s.stopping() {
				return
			}
		default:
			return
		}
	}
}

// deliver hands one completion to the prelude as a task of its own.
func (s *webJSSession) deliver(ev webJSDone) {
	switch ev.kind {
	case webJSDoneFetch:
		delete(s.fetches, ev.id)
	case webJSDoneScript:
		s.scriptLoads--
	}
	if s.stopping() {
		return
	}
	id := s.rt.ToValue(ev.id)
	switch ev.kind {
	case webJSDoneFetch:
		_, _ = s.call(s.host.fetchDone, id, s.jsResponse(ev.resp))
	case webJSDoneScript:
		if ev.resp.Err != "" {
			_, _ = s.call(s.host.scriptLoaded, id, goja.Null(), s.rt.ToValue(ev.resp.URL), s.rt.ToValue(ev.resp.Err))
			return
		}
		_, _ = s.call(s.host.scriptLoaded, id, s.rt.ToValue(webJSDecodeScript(ev.resp)), s.rt.ToValue(ev.resp.URL), goja.Null())
	}
}

// stopping reports whether the pass is over: out of time or tasks, stopped by
// the watchdog or a new load, or a script navigated away. Running out of time
// is recorded here too, so the summary line says why the page stopped even
// when no call was running at the deadline.
func (s *webJSSession) stopping() bool {
	if s.nav != "" || s.tasks >= webJSMaxTasks {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.abort == "" && time.Now().After(s.deadline) {
		s.abort = "time budget"
	}
	return s.abort != ""
}

// call makes one top-level call into the engine under the watchdog. Promise
// jobs the call queued run before it returns (goja drains them on the way
// out), so each call is a task plus its microtasks.
func (s *webJSSession) call(fn goja.Callable, args ...goja.Value) (goja.Value, error) {
	if fn == nil {
		return nil, errors.New("no such host function")
	}
	s.beginCall()
	v, err := fn(goja.Undefined(), args...)
	s.endCall()
	s.tasks++
	if err != nil {
		s.noteCallError(err)
	}
	return v, err
}

// beginCall opens the watchdog's window for one call. A pass already stopped
// for good (the reader left, the heap ceiling) has the call interrupted at its
// first instruction: stop only interrupts a call that is running, and the
// phases after it still make a few calls of their own.
func (s *webJSSession) beginCall() {
	s.mu.Lock()
	s.callStart = time.Now()
	if s.abort == webJSSuperseded || s.abort == "memory" {
		s.rt.Interrupt(s.abort)
	}
	s.mu.Unlock()
}

// endCall closes the watchdog's window and clears an interrupt that may have
// landed as the call was returning, so it cannot hit the next one. Both happen
// under the lock the watchdog interrupts under.
func (s *webJSSession) endCall() {
	s.mu.Lock()
	s.callStart = time.Time{}
	s.blocked = false
	s.rt.ClearInterrupt()
	s.mu.Unlock()
}

// block marks the running call as waiting on the network; the returned func
// ends the wait and moves the call's start forward by its length, so the
// per-call cap only counts time spent running script.
func (s *webJSSession) block() func() {
	t0 := time.Now()
	s.mu.Lock()
	s.blocked = true
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.blocked = false
		if !s.callStart.IsZero() {
			s.callStart = s.callStart.Add(time.Since(t0))
		}
		s.mu.Unlock()
	}
}

// noteCallError logs the first few failed calls. Script errors are the page's
// business and arrive through the prelude's stats; these are interrupts and
// prelude bugs.
func (s *webJSSession) noteCallError(err error) {
	s.callErrors++
	if s.callErrors > 5 {
		return
	}
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		logDebug("browser", "script on %s interrupted: %v", webJSLogURL(s.page), interrupted.Value())
		return
	}
	logDebug("browser", "script pass call failed on %s: %s", webJSLogURL(s.page), webJSClip(err.Error(), 300))
}

// watch is the watchdog: it interrupts a call that outlives the per-call cap,
// any call once the pass is past its deadline, and stops the pass when the
// live heap has grown too far.
func (s *webJSSession) watch() {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for n := 1; ; n++ {
		select {
		case <-s.closed:
			return
		case <-tick.C:
		}
		var heap uint64
		if n%4 == 0 {
			heap = webLiveHeapBytes()
		}
		now := time.Now()
		s.mu.Lock()
		if !s.callStart.IsZero() {
			switch {
			case now.After(s.deadline):
				if s.abort == "" {
					s.abort = "time budget"
				}
				s.rt.Interrupt("time budget")
			case heap > 0 && s.heapBase > 0 && heap > s.heapBase+webJSMaxHeapGrowth:
				s.abort = "memory"
				s.rt.Interrupt("memory limit")
			case !s.blocked && now.Sub(s.callStart) > s.callTimeout:
				s.rt.Interrupt("call timeout")
			}
		}
		s.mu.Unlock()
	}
}

// now is the virtual clock Date and the timers run on: real time plus what
// the loop skipped. Runtime goroutine only.
func (s *webJSSession) now() time.Time {
	return s.wallStart.Add(time.Since(s.wallStart) + s.advanced)
}

// perfNow is performance.now(): milliseconds since the pass began, virtual.
func (s *webJSSession) perfNow() float64 {
	return float64(time.Since(s.wallStart)+s.advanced) / float64(time.Millisecond)
}

// configJSON is what boot() learns about the device and the reader.
func (s *webJSSession) configJSON() string {
	w, h := int(screenWidth), int(screenHeight)
	if w <= 0 || h <= 0 {
		w, h = 1280, 720
	}
	cfg := map[string]interface{}{
		"userAgent": webJSUserAgent,
		"language":  "en-US",
		"languages": []string{"en-US", "en"},
		"width":     w,
		"height":    h,
		"timeZone":  webJSTimeZone(),
		"platform":  webJSPlatform(),
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// prefetchScript starts (or joins) the download of one script. nil means the
// page has asked for more scripts than it may load.
func (s *webJSSession) prefetchScript(rawURL string, module bool) *webJSScriptFetch {
	key := "c " + rawURL
	if module {
		key = "m " + rawURL
	}
	s.scriptMu.Lock()
	defer s.scriptMu.Unlock()
	if f, ok := s.scripts[key]; ok {
		return f
	}
	if len(s.scripts) >= webJSMaxScriptFetches {
		return nil
	}
	f := &webJSScriptFetch{ready: make(chan struct{})}
	s.scripts[key] = f
	go func() {
		defer close(f.ready)
		defer func() {
			if r := recover(); r != nil {
				logWarn("browser", "script download failed: %v", r)
				f.resp = webJSResponse{Err: "script download failed"}
			}
		}()
		f.resp = s.net.fetchScript(s.ctx, rawURL, module)
	}()
	return f
}

// awaitScript waits for a download, or for the pass to run out of time.
func (s *webJSSession) awaitScript(f *webJSScriptFetch) webJSResponse {
	if f == nil {
		return webJSResponse{Err: "too many scripts"}
	}
	select {
	case <-f.ready:
		return f.resp
	case <-s.ctx.Done():
		return webJSResponse{Err: "timed out"}
	}
}

// netDo is one fetch/XHR request, with N's code behind a recover: it runs on
// its own goroutine, where a panic would take the whole app down.
func (s *webJSSession) netDo(req webJSRequest) (resp webJSResponse) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("browser", "page request failed: %v", r)
			resp = webJSResponse{Err: "request failed"}
		}
	}()
	return s.net.do(s.ctx, req)
}

// natives builds the __jhNative object: the prelude's only way out of the
// engine.
func (s *webJSSession) natives() *goja.Object {
	n := s.rt.NewObject()
	set := func(name string, fn func(goja.FunctionCall) goja.Value) { _ = n.Set(name, fn) }
	set("parseDocument", s.nParseDocument)
	set("parseFragment", s.nParseFragment)
	set("runScript", s.nRunScript)
	set("fetch", s.nFetch)
	set("fetchSync", s.nFetchSync)
	set("loadScript", s.nLoadScript)
	set("prefetch", s.nPrefetch)
	set("fetchScriptSync", s.nFetchScriptSync)
	set("transformModule", s.nTransformModule)
	set("rewriteDynamicImport", s.nRewriteDynamicImport)
	set("cookieGet", s.nCookieGet)
	set("cookieSet", s.nCookieSet)
	set("storageGet", s.nStorageGet)
	set("storageSet", s.nStorageSet)
	set("now", s.nNow)
	set("perfNow", s.nPerfNow)
	set("advance", s.nAdvance)
	set("navigate", s.nNavigate)
	set("log", s.nLog)
	set("utf8Encode", s.nUTF8Encode)
	set("utf8Decode", s.nUTF8Decode)
	set("atob", s.nAtob)
	set("btoa", s.nBtoa)
	set("randomBytes", s.nRandomBytes)
	set("urlParse", s.nURLParse)
	return n
}

// nParseDocument parses a whole document (DOMParser, createHTMLDocument).
func (s *webJSSession) nParseDocument(call goja.FunctionCall) goja.Value {
	src := call.Argument(0).String()
	if len(src) > webJSMaxFragmentBytes {
		return s.rt.ToValue("[]")
	}
	tree, _, _ := webDOMParseDocument(src, webDOMMaxNodes)
	return s.rt.ToValue(tree)
}

// nParseFragment parses markup in an element's context (innerHTML,
// insertAdjacentHTML, document.write).
func (s *webJSSession) nParseFragment(call goja.FunctionCall) goja.Value {
	src := call.Argument(0).String()
	if len(src) > webJSMaxFragmentBytes {
		return s.rt.ToValue("[]")
	}
	tree, _, _ := webDOMParseFragment(src, call.Argument(1).String(), int(call.Argument(2).ToInteger()), webDOMMaxNodes)
	return s.rt.ToValue(tree)
}

// nRunScript runs code as a classic script in the global scope and returns its
// completion value (the module loader reads the factory a rewritten module
// evaluates to). Scripts share one global lexical scope, so a `const` one
// <script> declares is visible to the next — which an indirect eval would not
// give them. A thrown exception comes back as {jhError, error, message, line,
// col}; an interrupt returns undefined with the runtime's interrupt flag still
// set, so the caller stops at its next step.
func (s *webJSSession) nRunScript(call goja.FunctionCall) goja.Value {
	code := call.Argument(0).String()
	name := call.Argument(1).String()
	if s.scriptRuns >= webJSMaxScriptRuns || s.scriptBytes+len(code) > webJSMaxScriptBytes {
		return s.errorDesc("the page ran more script than the handheld allows")
	}
	s.scriptRuns++
	s.scriptBytes += len(code)
	v, err := s.rt.RunScript(name, code)
	if err == nil {
		return webJSValue(v)
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		thrown := ex.Value()
		desc := s.errorDesc(s.thrownMessage(thrown))
		_ = desc.Set("error", webJSValue(thrown))
		// Where it was thrown, for the ErrorEvent's lineno/colno: the innermost
		// frame (a native frame has no position and is left out).
		if stack := ex.Stack(); len(stack) > 0 {
			if pos := stack[0].Position(); pos.Line > 0 {
				_ = desc.Set("line", pos.Line)
				_ = desc.Set("col", pos.Column)
			}
		}
		return desc
	}
	var overflow *goja.StackOverflowError
	if errors.As(err, &overflow) {
		return s.errorDesc("RangeError: Maximum call stack size exceeded")
	}
	return goja.Undefined()
}

// errorDesc is the descriptor nRunScript returns for a failed script. The
// jhError marker tells it apart from a script whose completion value merely
// looks like one.
func (s *webJSSession) errorDesc(msg string) *goja.Object {
	desc := s.rt.NewObject()
	_ = desc.Set("jhError", true)
	_ = desc.Set("message", msg)
	return desc
}

// thrownMessage is the page-visible text of a thrown value, the way a
// browser's ErrorEvent shows it: "TypeError: x is not a function" for an
// error, the value itself for a primitive. goja's own Exception.Error() would
// add " at fn (url:line:col(pc))", which is not what a page's onerror expects.
// Reading name and message runs page code (getters, a toString), so it runs
// under Try: a value whose toString throws reads "Script error." instead of
// throwing out of the native, uncounted.
func (s *webJSSession) thrownMessage(v goja.Value) string {
	if v == nil {
		return "Script error."
	}
	msg := "Script error."
	if ex := s.rt.Try(func() {
		obj, isObj := v.(*goja.Object)
		if !isObj {
			msg = v.String()
			return
		}
		name, message := webJSString(obj.Get("name")), webJSString(obj.Get("message"))
		switch {
		case name != "" && message != "":
			msg = name + ": " + message
		case message != "":
			msg = message
		case name != "":
			msg = name
		default:
			msg = obj.String()
		}
	}); ex != nil {
		return "Script error."
	}
	return msg
}

// nFetch starts an asynchronous request; the response arrives as
// host.fetchDone(id, resp).
func (s *webJSSession) nFetch(call goja.FunctionCall) goja.Value {
	id := call.Argument(0).ToInteger()
	req, ok := s.jsRequest(call.Argument(1))
	if !ok {
		return s.rt.ToValue(false)
	}
	s.fetches[id] = time.Now()
	go func() {
		resp := s.netDo(req)
		select {
		case s.done <- webJSDone{kind: webJSDoneFetch, id: id, resp: resp}:
		case <-s.closed:
		}
	}()
	return s.rt.ToValue(true)
}

// nFetchSync is a synchronous XMLHttpRequest: the script waits, the per-call
// cap does not.
func (s *webJSSession) nFetchSync(call goja.FunctionCall) goja.Value {
	req, ok := s.jsRequest(call.Argument(0))
	if !ok {
		return s.jsResponse(webJSResponse{Err: "bad request"})
	}
	unblock := s.block()
	resp := s.netDo(req)
	unblock()
	return s.jsResponse(resp)
}

// nLoadScript downloads a script asynchronously (an async or dynamically
// inserted <script>); it arrives as host.scriptLoaded(id, code, url, err).
func (s *webJSSession) nLoadScript(call goja.FunctionCall) goja.Value {
	id := call.Argument(0).ToInteger()
	f := s.prefetchScript(call.Argument(1).String(), call.Argument(2).ToBoolean())
	if f == nil {
		return s.rt.ToValue(false)
	}
	s.scriptLoads++
	go func() {
		var resp webJSResponse
		select {
		case <-f.ready:
			resp = f.resp
		case <-s.closed:
			return
		}
		select {
		case s.done <- webJSDone{kind: webJSDoneScript, id: id, resp: resp}:
		case <-s.closed:
		}
	}()
	return s.rt.ToValue(true)
}

// nPrefetch warms the script cache for URLs the page will want next (a
// module's imports, modulepreload hints).
func (s *webJSSession) nPrefetch(call goja.FunctionCall) goja.Value {
	module := call.Argument(1).ToBoolean()
	list, _ := call.Argument(0).Export().([]interface{})
	for i, item := range list {
		if i >= webJSMaxPrefetch {
			break
		}
		if src, ok := item.(string); ok && src != "" {
			s.prefetchScript(src, module)
		}
	}
	return goja.Undefined()
}

// nFetchScriptSync is how the module loader walks a graph: it waits for the
// download (usually already prefetched) and returns {code, url, error}.
func (s *webJSSession) nFetchScriptSync(call goja.FunctionCall) goja.Value {
	src := call.Argument(0).String()
	module := call.Argument(1).ToBoolean()
	unblock := s.block()
	resp := s.awaitScript(s.prefetchScript(src, module))
	unblock()
	o := s.rt.NewObject()
	_ = o.Set("url", webJSOr(resp.URL, src))
	if resp.Err != "" {
		_ = o.Set("code", goja.Null())
		_ = o.Set("error", resp.Err)
		return o
	}
	_ = o.Set("code", webJSDecodeScript(resp))
	_ = o.Set("error", goja.Null())
	return o
}

// nTransformModule rewrites an ES module for goja (webdom_module.go) and
// returns {code, imports: [{spec, type}], error}.
func (s *webJSSession) nTransformModule(call goja.FunctionCall) goja.Value {
	o := s.rt.NewObject()
	code, imports, err := webJSSafeTransform(call.Argument(0).String())
	if err != nil {
		_ = o.Set("code", goja.Null())
		_ = o.Set("imports", s.rt.NewArray())
		_ = o.Set("error", err.Error())
		return o
	}
	items := make([]interface{}, 0, len(imports))
	for _, im := range imports {
		e := s.rt.NewObject()
		_ = e.Set("spec", im.Spec)
		_ = e.Set("type", im.Type)
		items = append(items, e)
	}
	_ = o.Set("code", code)
	_ = o.Set("imports", s.rt.NewArray(items...))
	_ = o.Set("error", goja.Null())
	return o
}

// webJSSafeTransform is webJSTransformModule behind a recover.
func webJSSafeTransform(src string) (code string, imports []webJSImport, err error) {
	defer func() {
		if r := recover(); r != nil {
			code, imports, err = "", nil, fmt.Errorf("module transform failed: %v", r)
		}
	}()
	return webJSTransformModule(src)
}

// nRewriteDynamicImport rewrites import() and import.meta in a classic script;
// the code comes back unchanged when it has neither.
func (s *webJSSession) nRewriteDynamicImport(call goja.FunctionCall) goja.Value {
	src := call.Argument(0).String()
	out, ok := webJSSafeRewrite(src)
	if !ok {
		return s.rt.ToValue(src)
	}
	return s.rt.ToValue(out)
}

// webJSSafeRewrite is webJSRewriteDynamicImport behind a recover.
func webJSSafeRewrite(src string) (out string, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			out, ok = src, false
		}
	}()
	return webJSRewriteDynamicImport(src)
}

// nCookieGet is the document.cookie getter (HttpOnly cookies stay hidden).
func (s *webJSSession) nCookieGet(goja.FunctionCall) goja.Value {
	return s.rt.ToValue(s.net.cookieString())
}

// nCookieSet is the document.cookie setter.
func (s *webJSSession) nCookieSet(call goja.FunctionCall) goja.Value {
	line := call.Argument(0).String()
	if len(line) <= 8<<10 {
		s.net.setCookieString(line)
	}
	return goja.Undefined()
}

// nStorageGet returns the page origin's saved localStorage/sessionStorage as
// JSON, "" when there is none.
func (s *webJSSession) nStorageGet(call goja.FunctionCall) goja.Value {
	return s.rt.ToValue(webJSStorageGet(webJSStorageKey(call.Argument(0).String(), s.page)))
}

// nStorageSet saves an origin's storage; false when it is over the quota.
func (s *webJSSession) nStorageSet(call goja.FunctionCall) goja.Value {
	return s.rt.ToValue(webJSStorageSet(webJSStorageKey(call.Argument(0).String(), s.page), call.Argument(1).String()))
}

// nNow is Date.now() on the virtual clock, in milliseconds.
func (s *webJSSession) nNow(goja.FunctionCall) goja.Value {
	return s.rt.ToValue(float64(s.now().UnixNano()) / float64(time.Millisecond))
}

// nPerfNow is performance.now().
func (s *webJSSession) nPerfNow(goja.FunctionCall) goja.Value {
	return s.rt.ToValue(s.perfNow())
}

// nAdvance moves the virtual clock forward (the loop skipping to the next
// timer) and returns the new performance.now().
func (s *webJSSession) nAdvance(call goja.FunctionCall) goja.Value {
	ms := call.Argument(0).ToFloat()
	if ms > 0 && ms < float64(webJSMaxAdvance/time.Millisecond) {
		if d := time.Duration(ms * float64(time.Millisecond)); s.advanced+d <= webJSMaxAdvance {
			s.advanced += d
		}
	}
	return s.rt.ToValue(s.perfNow())
}

// nNavigate records where a script sent the page. Only an http(s) address
// that is not this page (the fragment aside) counts, and only the first. The
// reader follows it without a press, like a redirect, so a public page may not
// send it where its own requests may not go: this device's services or the
// LAN (webdom_net.go's guard, judged on the name as the request layer does
// before any lookup).
func (s *webJSSession) nNavigate(call goja.FunctionCall) goja.Value {
	target, err := s.page.Parse(strings.TrimSpace(call.Argument(0).String()))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return s.rt.ToValue(false)
	}
	if !webJSNetNameAllowed(webJSNetPageGuard(s.page), target.Hostname()) {
		return s.rt.ToValue(false)
	}
	if webJSSameDocument(target, s.page) {
		return s.rt.ToValue(false)
	}
	if s.nav == "" {
		s.nav = target.String()
	}
	return s.rt.ToValue(true)
}

// webJSSameDocument reports whether two URLs differ only in the fragment.
func webJSSameDocument(a, b *url.URL) bool {
	x, y := *a, *b
	x.Fragment, x.RawFragment = "", ""
	y.Fragment, y.RawFragment = "", ""
	return x.String() == y.String()
}

// nLog copies a few console lines into the debug log.
func (s *webJSSession) nLog(call goja.FunctionCall) goja.Value {
	if s.logLines >= webJSMaxLogLines {
		return goja.Undefined()
	}
	s.logLines++
	logDebug("browser", "js %s: %s", webJSClip(call.Argument(0).String(), 8), webJSClip(call.Argument(1).String(), 300))
	return goja.Undefined()
}

// nUTF8Encode is TextEncoder.encode: the string's UTF-8 bytes.
func (s *webJSSession) nUTF8Encode(call goja.FunctionCall) goja.Value {
	return s.rt.ToValue(s.rt.NewArrayBuffer([]byte(call.Argument(0).String())))
}

// nUTF8Decode is TextDecoder.decode and Response.text(): bytes in one of the
// encodings pages actually use. null means invalid input in fatal mode.
func (s *webJSSession) nUTF8Decode(call goja.FunctionCall) goja.Value {
	text, ok := webJSDecodeText(webJSBytes(call.Argument(0)), call.Argument(1).String(),
		call.Argument(2).ToBoolean(), call.Argument(3).ToBoolean())
	if !ok {
		return goja.Null()
	}
	return s.rt.ToValue(text)
}

// nAtob decodes base64 into a binary string (one character per byte); null
// means the input is not base64 and the prelude throws InvalidCharacterError.
func (s *webJSSession) nAtob(call goja.FunctionCall) goja.Value {
	data, ok := webJSAtob(call.Argument(0).String())
	if !ok {
		return goja.Null()
	}
	runes := make([]rune, len(data))
	for i, c := range data {
		runes[i] = rune(c)
	}
	return s.rt.ToValue(string(runes))
}

// webJSAtob is the forgiving-base64 decode of the HTML standard.
func webJSAtob(in string) ([]byte, bool) {
	var b strings.Builder
	b.Grow(len(in))
	for i := 0; i < len(in); i++ {
		switch c := in[i]; c {
		case ' ', '\t', '\n', '\f', '\r':
		default:
			b.WriteByte(c)
		}
	}
	text := b.String()
	if len(text)%4 == 0 {
		text = strings.TrimSuffix(text, "=")
		text = strings.TrimSuffix(text, "=")
	}
	if len(text)%4 == 1 {
		return nil, false
	}
	data, err := base64.RawStdEncoding.DecodeString(text)
	if err != nil {
		return nil, false
	}
	return data, true
}

// nBtoa encodes a binary string; null means a character above U+00FF.
func (s *webJSSession) nBtoa(call goja.FunctionCall) goja.Value {
	str := call.Argument(0).String()
	data := make([]byte, 0, len(str))
	for _, r := range str {
		if r > 0xFF {
			return goja.Null()
		}
		data = append(data, byte(r))
	}
	return s.rt.ToValue(base64.StdEncoding.EncodeToString(data))
}

// nRandomBytes backs crypto.getRandomValues and randomUUID.
func (s *webJSSession) nRandomBytes(call goja.FunctionCall) goja.Value {
	n := call.Argument(0).ToInteger()
	if n < 0 {
		n = 0
	}
	if n > 65536 {
		n = 65536
	}
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return s.rt.ToValue(s.rt.NewArrayBuffer(b))
}

// nURLParse is the URL parser behind URL, <a href> and every resolved address.
func (s *webJSSession) nURLParse(call goja.FunctionCall) goja.Value {
	base := ""
	if v := call.Argument(1); !goja.IsUndefined(v) && !goja.IsNull(v) {
		base = v.String()
	}
	return s.rt.ToValue(webJSURLParse(call.Argument(0).String(), base))
}

// jsRequest reads the prelude's request object.
func (s *webJSSession) jsRequest(v goja.Value) (webJSRequest, bool) {
	var req webJSRequest
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return req, false
	}
	o := v.ToObject(s.rt)
	req.Method = strings.ToUpper(webJSString(o.Get("method")))
	req.URL = webJSString(o.Get("url"))
	req.Credentials = webJSString(o.Get("credentials"))
	req.Mode = webJSString(o.Get("mode"))
	req.Redirect = webJSString(o.Get("redirect"))
	req.Body = webJSBytes(o.Get("body"))
	if hv := o.Get("headers"); hv != nil && !goja.IsUndefined(hv) && !goja.IsNull(hv) {
		list, _ := hv.Export().([]interface{})
		for _, item := range list {
			pair, _ := item.([]interface{})
			if len(pair) != 2 {
				continue
			}
			name, _ := pair[0].(string)
			value, _ := pair[1].(string)
			if name != "" && len(req.Header) < 64 {
				req.Header = append(req.Header, [2]string{name, value})
			}
		}
	}
	return req, req.URL != ""
}

// jsResponse builds the prelude's response object.
func (s *webJSSession) jsResponse(r webJSResponse) *goja.Object {
	o := s.rt.NewObject()
	// Own data properties, not Set: Set walks the prototype chain and calls a
	// setter a page may have planted on Object.prototype ("status", "body"…).
	// deliver builds this object before the call that hands it over starts,
	// outside the watchdog's window, where a looping setter could not be
	// interrupted at all.
	put := func(name string, v interface{}) {
		_ = o.DefineDataProperty(name, s.rt.ToValue(v), goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_TRUE)
	}
	put("err", r.Err)
	put("type", r.Type)
	put("status", r.Status)
	put("statusText", r.StatusText)
	put("url", r.URL)
	put("redirected", r.Redirected)
	headers := make([]interface{}, 0, len(r.Header))
	for _, h := range r.Header {
		headers = append(headers, s.rt.NewArray(h[0], h[1]))
	}
	put("headers", s.rt.NewArray(headers...))
	put("body", s.rt.NewArrayBuffer(r.Body))
	return o
}

// webJSValue turns a missing property (nil) into undefined: goja calls must
// never be handed a nil Value.
func webJSValue(v goja.Value) goja.Value {
	if v == nil {
		return goja.Undefined()
	}
	return v
}

// webJSString reads a string property; missing, undefined and null read "".
func webJSString(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

// webJSBool reads a boolean property; missing reads false.
func webJSBool(v goja.Value) bool {
	if v == nil {
		return false
	}
	return v.ToBoolean()
}

// webJSOr returns a, or b when a is empty.
func webJSOr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// webJSBytes copies the bytes of an ArrayBuffer, a Uint8Array or a string. The
// copy matters: a request body is read on another goroutine while the page's
// script may already be writing into the same buffer.
func webJSBytes(v goja.Value) []byte {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	switch t := v.Export().(type) {
	case goja.ArrayBuffer:
		return append([]byte(nil), t.Bytes()...)
	case []byte:
		return append([]byte(nil), t...)
	case string:
		return []byte(t)
	}
	return nil
}

// webJSDecodeScript turns a downloaded script into source text: the charset
// the server named, else UTF-8, else (for old pages saved in Latin-1)
// windows-1252.
func webJSDecodeScript(resp webJSResponse) string {
	label := webJSHeaderCharset(resp.Header)
	if label == "" && !utf8.Valid(resp.Body) {
		label = "windows-1252"
	}
	text, _ := webJSDecodeText(resp.Body, label, false, false)
	return text
}

// webJSHeaderCharset is the charset parameter of a response's Content-Type.
func webJSHeaderCharset(header [][2]string) string {
	for _, h := range header {
		if !strings.EqualFold(h[0], "content-type") {
			continue
		}
		for _, param := range strings.Split(h[1], ";")[1:] {
			name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(strings.TrimSpace(name), "charset") {
				return strings.Trim(strings.TrimSpace(value), `"'`)
			}
		}
		return ""
	}
	return ""
}

// webJSDecodeText decodes bytes in the few encodings the web still uses for
// scripts and API responses. ok is false only for invalid input in fatal mode.
func webJSDecodeText(b []byte, label string, ignoreBOM, fatal bool) (string, bool) {
	switch webJSCharset(label) {
	case "utf-16le", "utf-16be":
		be := webJSCharset(label) == "utf-16be"
		if !ignoreBOM && len(b) >= 2 && ((!be && b[0] == 0xFF && b[1] == 0xFE) || (be && b[0] == 0xFE && b[1] == 0xFF)) {
			b = b[2:]
		}
		if len(b)%2 == 1 && fatal {
			return "", false
		}
		units := make([]uint16, len(b)/2)
		for i := range units {
			if be {
				units[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
			} else {
				units[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
			}
		}
		text := string(utf16.Decode(units))
		if len(b)%2 == 1 {
			text += "�"
		}
		return text, true
	case "windows-1252":
		var out strings.Builder
		out.Grow(len(b))
		for _, c := range b {
			switch {
			case c < 0x80:
				out.WriteByte(c)
			case c < 0xA0:
				out.WriteRune(webJS1252[c-0x80])
			default:
				out.WriteRune(rune(c))
			}
		}
		return out.String(), true
	}
	if !ignoreBOM && len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:]
	}
	if utf8.Valid(b) {
		return string(b), true
	}
	if fatal {
		return "", false
	}
	return strings.ToValidUTF8(string(b), "�"), true
}

// webJSCharset maps an encoding label to the decoder webJSDecodeText has for
// it; every other label reads as UTF-8.
func webJSCharset(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "utf-16le", "utf-16", "ucs-2", "unicode", "csunicode", "iso-10646-ucs-2", "unicodefeff":
		return "utf-16le"
	case "utf-16be", "unicodefffe":
		return "utf-16be"
	case "windows-1252", "latin1", "iso-8859-1", "iso8859-1", "iso_8859-1", "l1", "cp1252", "x-cp1252",
		"ascii", "us-ascii", "ansi_x3.4-1968", "iso-ir-100", "cp819", "ibm819", "csisolatin1":
		return "windows-1252"
	}
	return "utf-8"
}

// webJS1252 is windows-1252's 0x80–0x9F row (the WHATWG index); the rest of
// the encoding is Latin-1.
var webJS1252 = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

// webJSURLParse parses input against base the way the URL API reports it, as
// JSON {href, origin, protocol, username, password, host, hostname, port,
// pathname, search, hash}; "" means invalid. It is Go's parser with the WHATWG
// normalisations pages notice: lower-case host, default port dropped, "/" as
// the path of an empty http(s) URL, backslashes as slashes.
func webJSURLParse(input, base string) string {
	input = strings.TrimFunc(input, func(r rune) bool { return r <= ' ' })
	input = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(input)
	input, base = webJSFixPercent(input), webJSFixPercent(base)
	var u *url.URL
	if base != "" {
		b, err := url.Parse(base)
		if err != nil || b.Scheme == "" {
			return ""
		}
		if webJSSpecialScheme(b.Scheme) {
			input = strings.ReplaceAll(input, "\\", "/")
		}
		ref, err := url.Parse(input)
		if err != nil {
			return ""
		}
		if ref.Scheme == "" && b.Opaque != "" && !strings.HasPrefix(input, "#") {
			return ""
		}
		u = b.ResolveReference(ref)
	} else {
		parsed, err := url.Parse(input)
		if err != nil || parsed.Scheme == "" {
			return ""
		}
		if webJSSpecialScheme(strings.ToLower(parsed.Scheme)) && strings.Contains(input, "\\") {
			if parsed, err = url.Parse(strings.ReplaceAll(input, "\\", "/")); err != nil {
				return ""
			}
		}
		u = parsed
	}
	u.Scheme = strings.ToLower(u.Scheme)
	special := webJSSpecialScheme(u.Scheme)
	if special && u.Opaque == "" && u.Host == "" && u.Scheme != "file" {
		return ""
	}
	hostname := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && port == webJSDefaultPort(u.Scheme) {
		port = ""
	}
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	host := hostname
	if port != "" {
		host += ":" + port
	}
	if special {
		u.Host = host
		if u.Path == "" && u.Opaque == "" {
			u.Path = "/"
		}
	}
	parts := map[string]string{
		"href":     u.String(),
		"protocol": u.Scheme + ":",
		"hostname": hostname,
		"port":     port,
		"host":     host,
	}
	if u.User != nil {
		parts["username"] = u.User.Username()
		parts["password"], _ = u.User.Password()
	} else {
		parts["username"], parts["password"] = "", ""
	}
	if u.Opaque != "" {
		parts["pathname"] = u.Opaque
	} else {
		parts["pathname"] = u.EscapedPath()
	}
	parts["search"] = ""
	if u.RawQuery != "" {
		parts["search"] = "?" + u.RawQuery
	}
	parts["hash"] = ""
	if u.Fragment != "" {
		parts["hash"] = "#" + u.EscapedFragment()
	}
	switch {
	case special && u.Scheme != "file":
		parts["origin"] = u.Scheme + "://" + host
	case u.Scheme == "blob":
		parts["origin"] = "null"
		if inner, err := url.Parse(u.Opaque); err == nil && (inner.Scheme == "http" || inner.Scheme == "https") {
			parts["origin"] = strings.ToLower(inner.Scheme) + "://" + strings.ToLower(inner.Host)
		}
	default:
		parts["origin"] = "null"
	}
	out, err := json.Marshal(parts)
	if err != nil {
		return ""
	}
	return string(out)
}

// webJSFixPercent escapes a '%' that does not begin a %XX escape in the path
// or the fragment. Browsers keep such a URL (`/100%/x`, `#50%`), while Go's
// parser rejects the whole address, which left new URL(), link targets and
// fetch() failing on it. The query is left as it is: Go keeps it raw, the way
// a browser sends it.
func webJSFixPercent(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	query, fragment := strings.IndexByte(s, '?'), strings.IndexByte(s, '#')
	if query >= 0 && fragment >= 0 && fragment < query {
		query = -1 // a '?' inside the fragment is part of the fragment
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		inQuery := query >= 0 && i > query && (fragment < 0 || i < fragment)
		if c == '%' && !inQuery && !(i+2 < len(s) && webJSIsHex(s[i+1]) && webJSIsHex(s[i+2])) {
			b.WriteString("%25")
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// webJSIsHex reports whether c is a hexadecimal digit.
func webJSIsHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// webJSSpecialScheme is the URL standard's "special" schemes.
func webJSSpecialScheme(scheme string) bool {
	switch scheme {
	case "http", "https", "ws", "wss", "ftp", "file":
		return true
	}
	return false
}

// webJSDefaultPort is a special scheme's default port, which URLs drop.
func webJSDefaultPort(scheme string) string {
	switch scheme {
	case "http", "ws":
		return "80"
	case "https", "wss":
		return "443"
	case "ftp":
		return "21"
	}
	return ""
}

// webJSStorage is localStorage and sessionStorage for every origin, in memory
// only: a page's saved state survives a reload or a revisit while the hub
// runs, and nothing a page stores ever reaches the card.
var webJSStorage = struct {
	sync.Mutex
	data map[string]string
	// used holds the keys, least recently used first.
	used []string
}{data: map[string]string{}}

// webJSStorageMaxBytes and webJSStorageMaxEntries bound what pages may keep.
const (
	webJSStorageMaxBytes   = 512 << 10
	webJSStorageMaxEntries = 64
)

// webJSStorageKey names one origin's store of one kind.
func webJSStorageKey(kind string, page *url.URL) string {
	if kind != "session" {
		kind = "local"
	}
	return kind + " " + strings.ToLower(page.Scheme) + "://" + strings.ToLower(page.Host)
}

func webJSStorageGet(key string) string {
	webJSStorage.Lock()
	defer webJSStorage.Unlock()
	return webJSStorage.data[key]
}

// webJSStorageSet stores one origin's JSON; false when it is over the quota.
func webJSStorageSet(key, value string) bool {
	if len(value) > webJSStorageMaxBytes {
		return false
	}
	webJSStorage.Lock()
	defer webJSStorage.Unlock()
	for i, k := range webJSStorage.used {
		if k == key {
			webJSStorage.used = append(webJSStorage.used[:i], webJSStorage.used[i+1:]...)
			break
		}
	}
	if value == "" || value == "{}" {
		delete(webJSStorage.data, key)
		return true
	}
	webJSStorage.data[key] = value
	webJSStorage.used = append(webJSStorage.used, key)
	for len(webJSStorage.used) > webJSStorageMaxEntries {
		delete(webJSStorage.data, webJSStorage.used[0])
		webJSStorage.used = webJSStorage.used[1:]
	}
	return true
}

// webJSTimeZone is the zone Intl reports: TZ when it names a real zone, the
// local zone's name, or a fixed Etc/GMT offset.
func webJSTimeZone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" && !strings.HasPrefix(tz, ":") {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	_, offset := time.Now().Zone()
	switch {
	case offset == 0:
		return "UTC"
	case offset%3600 != 0:
		return "UTC"
	case offset > 0:
		return fmt.Sprintf("Etc/GMT-%d", offset/3600)
	}
	return fmt.Sprintf("Etc/GMT+%d", -offset/3600)
}

// webJSPlatform is navigator.platform for this build.
func webJSPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "Win32"
	case "darwin":
		return "MacIntel"
	}
	switch runtime.GOARCH {
	case "arm64":
		return "Linux aarch64"
	case "arm":
		return "Linux armv7l"
	case "amd64":
		return "Linux x86_64"
	}
	return "Linux"
}

// webJSLogURL is a page address for the log: no query string or fragment,
// which is where tokens and search terms live.
func webJSLogURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// webJSClip shortens a log string to at most n bytes without cutting a rune.
func webJSClip(text string, n int) string {
	if len(text) <= n {
		return text
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}
