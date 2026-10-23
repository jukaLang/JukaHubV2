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
	"strings"
	"time"

	"github.com/dop251/goja"
)

const (
	webMaxScripts       = 16
	webScriptMaxBytes   = 512 << 10 // combined size of all scripts a page may run
	webScriptRunTimeout = 2 * time.Second
	webScriptBudget     = 5 * time.Second // wall-clock budget for the whole script pass
)

var (
	webScriptTagRe  = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	webScriptSrcRe  = regexp.MustCompile(`(?is)\bsrc\s*=\s*["']?([^"'\s>]+)`)
	webScriptTypeRe = regexp.MustCompile(`(?is)\btype\s*=\s*["']?([^"'\s>]+)`)
)

// runWebPageScripts executes the page's classic JavaScript in document order and
// returns the source with each script tag replaced by whatever it wrote.
func runWebPageScripts(source string, base *url.URL) string {
	matches := webScriptTagRe.FindAllStringSubmatchIndex(source, -1)
	if len(matches) == 0 {
		return source
	}
	deadline := time.Now().Add(webScriptBudget)
	rt := goja.New()
	var written strings.Builder
	var timers []goja.Callable

	doc := rt.NewObject()
	write := func(newline string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			for _, arg := range call.Arguments {
				written.WriteString(arg.String())
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
		if fn, ok := goja.AssertFunction(call.Argument(0)); ok {
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
		if typ := webScriptTypeRe.FindStringSubmatch(attrs); typ != nil {
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
		if src := webScriptSrcRe.FindStringSubmatch(attrs); src != nil {
			code = fetchWebScript(base, src[1], budget, deadline)
		}
		if code == "" || len(code) > budget {
			continue
		}
		budget -= len(code)
		written.Reset()
		runWebScript(rt, code)
		out.WriteString(written.String())
	}
	out.WriteString(source[last:])

	// Callbacks queued with setTimeout run once the page's scripts are done.
	for _, fn := range timers {
		written.Reset()
		runWebCallback(rt, fn)
		out.WriteString(written.String())
	}
	return out.String()
}

// runWebScript runs one script, interrupting it if it exceeds the per-script
// timeout. Errors are ignored: a broken script must not stop the page.
func runWebScript(rt *goja.Runtime, code string) {
	timer := time.AfterFunc(webScriptRunTimeout, func() { rt.Interrupt("timeout") })
	_, _ = rt.RunString(code)
	timer.Stop()
	rt.ClearInterrupt()
}

func runWebCallback(rt *goja.Runtime, fn goja.Callable) {
	timer := time.AfterFunc(webScriptRunTimeout, func() { rt.Interrupt("timeout") })
	_, _ = fn(goja.Undefined())
	timer.Stop()
	rt.ClearInterrupt()
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
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "JukaHub Browser/1.1")
	resp, err := (&http.Client{}).Do(req)
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
