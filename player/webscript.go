package main

// webscript.go — runs a page's own <script> tags for the built-in browser.
//
// Scripts run in an embedded ES5/ES6 engine (goja) with a small document
// object. document.write output is spliced back into the page where its
// script stood, so the text, link, stylesheet and image passes that follow see
// the markup the script produced. Every limit here exists for the handheld's
// 1 GB of RAM and slow CPU: a script can never hang the page load or grow the
// source without bound.

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime/metrics"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

const (
	webMaxScripts       = 16
	webScriptMaxBytes   = 512 << 10 // combined size of all scripts a page may run
	webScriptRunTimeout = 2 * time.Second
	webScriptBudget     = 5 * time.Second // wall-clock budget for the whole script pass
	// webScriptMaxOutput caps everything document.write may add to one page:
	// a loop of writes used to grow the source for the full two seconds, far
	// past what the handheld can hold.
	webScriptMaxOutput = 1 << 20
	// webScriptMaxTimers caps the setTimeout/requestAnimationFrame callbacks
	// one page may queue; each still runs under the pass budget.
	webScriptMaxTimers = 32
	// webScriptMaxStack bounds JavaScript recursion, which otherwise grows the
	// engine's stack on the Go heap until the timeout fires.
	webScriptMaxStack = 4096
	// webScriptMaxHeapGrowth is how much the live heap may grow while one
	// script runs before it is stopped: the engine has no memory limit of its
	// own, and two seconds of allocation is enough to get the hub killed.
	webScriptMaxHeapGrowth = 128 << 20
)

var (
	webScriptTagRe  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`) })
	webScriptSrcRe  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)\bsrc\s*=\s*["']?([^"'\s>]+)`) })
	webScriptTypeRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)\btype\s*=\s*["']?([^"'\s>]+)`) })
)

// runWebPageScripts executes the page's classic JavaScript in document order and
// returns the source with each script tag replaced by whatever it wrote.
func runWebPageScripts(source string, base *url.URL) string {
	matches := webScriptTagRe().FindAllStringSubmatchIndex(source, -1)
	if len(matches) == 0 {
		return source
	}
	deadline := time.Now().Add(webScriptBudget)
	rt := goja.New()
	rt.SetMaxCallStackSize(webScriptMaxStack)
	var written strings.Builder
	var timers []goja.Callable
	// outTotal is what earlier scripts already wrote into the page.
	outTotal := 0

	doc := rt.NewObject()
	write := func(newline string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			for _, arg := range call.Arguments {
				text := arg.String()
				if outTotal+written.Len()+len(text) > webScriptMaxOutput {
					rt.Interrupt("document.write limit")
					return goja.Undefined()
				}
				written.WriteString(text)
			}
			if outTotal+written.Len()+len(newline) > webScriptMaxOutput {
				rt.Interrupt("document.write limit")
				return goja.Undefined()
			}
			written.WriteString(newline)
			return goja.Undefined()
		}
	}
	_ = doc.Set("write", write(""))
	_ = doc.Set("writeln", write("\n"))
	noop := func(goja.FunctionCall) goja.Value { return goja.Undefined() }
	_ = doc.Set("addEventListener", noop)
	_ = doc.Set("getElementById", func(goja.FunctionCall) goja.Value { return goja.Null() })
	_ = doc.Set("querySelector", func(goja.FunctionCall) goja.Value { return goja.Null() })
	_ = doc.Set("createElement", func(goja.FunctionCall) goja.Value { return rt.NewObject() })
	_ = doc.Set("readyState", "complete")

	win := rt.NewObject()
	_ = win.Set("document", doc)
	_ = win.Set("addEventListener", noop)
	queue := func(call goja.FunctionCall) goja.Value {
		if fn, ok := goja.AssertFunction(call.Argument(0)); ok && len(timers) < webScriptMaxTimers {
			timers = append(timers, fn)
		}
		return goja.Undefined()
	}
	_ = win.Set("setTimeout", queue)
	_ = win.Set("requestAnimationFrame", queue)
	_ = rt.Set("window", win)
	_ = rt.Set("document", doc)
	_ = rt.Set("setTimeout", queue)
	console := rt.NewObject()
	for _, name := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(name, noop)
	}
	_ = rt.Set("console", console)

	var out strings.Builder
	last, budget, count := 0, webScriptMaxBytes, 0
	for _, m := range matches {
		out.WriteString(source[last:m[0]])
		last = m[1]
		attrs := source[m[2]:m[3]]
		if typ := webScriptTypeRe().FindStringSubmatch(attrs); typ != nil {
			kind := strings.ToLower(typ[1])
			if kind != "text/javascript" && kind != "application/javascript" && kind != "module" {
				continue
			}
		}
		if count >= webMaxScripts {
			break
		}
		count++
		code := source[m[4]:m[5]]
		if src := webScriptSrcRe().FindStringSubmatch(attrs); src != nil {
			code = fetchWebScript(base, htmlAttrValue(src[1]), budget, deadline)
		}
		if code == "" || len(code) > budget {
			continue
		}
		// The pass budget holds for running too, not only for fetching:
		// sixteen scripts at two seconds each was half a minute of a frozen
		// loading card.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			continue
		}
		budget -= len(code)
		written.Reset()
		runWebScript(rt, code, remaining)
		outTotal += written.Len()
		out.WriteString(written.String())
	}
	out.WriteString(source[last:])

	// Callbacks queued with setTimeout run once the page's scripts are done.
	for _, fn := range timers {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		written.Reset()
		runWebCallback(rt, fn, remaining)
		outTotal += written.Len()
		out.WriteString(written.String())
	}
	return out.String()
}

// runWebScript runs one script, interrupting it if it exceeds the per-script
// timeout (or what is left of the pass), or grows the heap too far. Errors are
// ignored: a broken script must not stop the page.
func runWebScript(rt *goja.Runtime, code string, remaining time.Duration) {
	stop := webScriptGuard(rt, remaining)
	_, _ = rt.RunString(code)
	stop()
	rt.ClearInterrupt()
}

func runWebCallback(rt *goja.Runtime, fn goja.Callable, remaining time.Duration) {
	stop := webScriptGuard(rt, remaining)
	_, _ = fn(goja.Undefined())
	stop()
	rt.ClearInterrupt()
}

// webScriptGuard interrupts rt when the run outlasts min(webScriptRunTimeout,
// remaining) or the live heap grows by webScriptMaxHeapGrowth. The returned
// function ends the watch; the caller clears any interrupt afterwards.
func webScriptGuard(rt *goja.Runtime, remaining time.Duration) func() {
	timeout := webScriptRunTimeout
	if remaining > 0 && remaining < timeout {
		timeout = remaining
	}
	done := make(chan struct{})
	base := webLiveHeapBytes()
	go func() {
		limit := time.NewTimer(timeout)
		defer limit.Stop()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-limit.C:
				rt.Interrupt("timeout")
				return
			case <-tick.C:
				// The live heap is only measured at a collection, so garbage a
				// script drops cannot trip this; memory it keeps does, as the
				// growth itself makes the collector run.
				if now := webLiveHeapBytes(); base > 0 && now > base+webScriptMaxHeapGrowth {
					rt.Interrupt("memory limit")
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

// webLiveHeapBytes is the live heap at the last collection (0 if the runtime
// does not report it).
func webLiveHeapBytes() uint64 {
	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return sample[0].Value.Uint64()
}

// fetchWebScript downloads an external script, bounded by the remaining byte
// budget and the pass deadline. Anything unusual yields "" and is skipped.
func fetchWebScript(base *url.URL, ref string, limit int, deadline time.Time) string {
	if base == nil || limit <= 0 || time.Now().After(deadline) {
		return ""
	}
	target, err := base.Parse(ref)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") {
		return ""
	}
	if browserRequestBlocked(base.String(), target.String()) {
		return ""
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "JukaHub Browser/1.1")
	// The deadline is on the context, so the client needs no timeout of its
	// own; it does need the jar, like every other browser request.
	resp, err := browserHTTPClient(0).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil || len(body) > limit {
		return ""
	}
	return string(body)
}
