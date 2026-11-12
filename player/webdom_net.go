package main

// webdom_net.go — the network as a page's own scripts see it.
//
// The DOM pass runs a page's JavaScript for real (webdom_runtime.go), and real
// pages build their content from the network: fetch() and XMLHttpRequest for
// the data, <script src> and ES module imports for the code. Every one of those
// requests comes through here, from several goroutines at once.
//
// That makes this file the reader's security boundary. A page script is
// third-party code, and a desktop browser surrounds its requests with rules
// that pages are written against and users rely on without knowing them: a
// page cannot read another site's answers unless that site allows it (CORS),
// cannot reach the router or the NAS on the user's own network, cannot ride the
// user's signed-in session on another site, cannot forge the headers that say
// who is asking. Without those rules the reader would be the weakest browser on
// the user's network, so each one is here, written to fail closed.
//
// Only http and https leave the device; data: URLs are decoded here. An https
// page cannot load plain http (mixed content: whoever runs the Wi-Fi could
// rewrite it). The ad and tracker filter sees every request, like the page's
// own loads do.
//
// A public page cannot reach this device or the LAN. The check runs on the
// address actually dialed (net.Dialer.Control), not on the name: a host that
// resolves to a public address for the page and to 192.168.x.x a second later
// (DNS rebinding) is still refused.
//
// Cookies come from the browser's shared jar, so a page's scripts carry the
// session its page load did. The standard jar knows nothing of SameSite, so
// every cookie is treated as SameSite=Lax: a request carries cookies only to
// the page's own site, checked again on every redirect hop, and only the
// page's own site can set them. HttpOnly cookies never reach document.cookie,
// and a script cannot overwrite or shadow one either.
//
// A page gets a bounded number of requests, bytes and connections, and every
// request a bounded time: a script in a retry loop costs the handheld a little
// Wi-Fi and nothing more.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// webJSNetMaxBody is the most one fetch() or XMLHttpRequest may download.
	// The JSON a page renders from is far smaller; a page asking for more is
	// pulling media the reader would not show anyway.
	webJSNetMaxBody = 4 << 20
	// webJSNetMaxScript caps one script file. Framework bundles are a few
	// hundred kilobytes, and three megabytes is already more than the engine
	// compiles inside its budget on the handheld.
	webJSNetMaxScript = 3 << 20
	// webJSNetMaxUpload caps a body a script sends: a page that uploads
	// megabytes from a reading browser is not loading its content.
	webJSNetMaxUpload = 1 << 20
	// webJSNetMaxURL caps an http(s) address; servers refuse far less.
	webJSNetMaxURL = 64 << 10
	// webJSNetMaxRequests and webJSNetMaxTotal bound one page: a script that
	// polls or retries in a loop must not turn the reader into a download
	// manager. Redirect hops count as requests; data: URLs do not.
	webJSNetMaxRequests = 160
	webJSNetMaxTotal    = 24 << 20
	// webJSNetMaxParallel is how many requests one page has on the wire at
	// once: what desktop browsers allow per host, and about what handheld
	// Wi-Fi carries before every request just gets slower.
	webJSNetMaxParallel = 6
	// webJSNetMaxWait bounds one request from the queue to the last body
	// byte, whatever the pass deadline would still allow.
	webJSNetMaxWait = 15 * time.Second
	// webJSNetDialTimeout bounds one connection attempt inside that.
	webJSNetDialTimeout = 10 * time.Second
	// webJSNetMaxRedirects is how many redirects one request follows.
	webJSNetMaxRedirects = 5
	// webJSNetMaxHeaders and webJSNetMaxHeaderValue bound what a script may
	// attach to a request; past them a header is dropped, not sent.
	webJSNetMaxHeaders     = 64
	webJSNetMaxHeaderValue = 8 << 10
	// webJSNetMaxResponseHeader caps a response's header block, which
	// net/http would otherwise accept ten megabytes of.
	webJSNetMaxResponseHeader = 256 << 10
	// webJSNetMaxCookieLine is the longest document.cookie assignment kept:
	// the 4 KiB per cookie browsers enforce.
	webJSNetMaxCookieLine = 4096
	// webJSNetMaxCookieSets is how many document.cookie assignments one page
	// may make. Every one that changes the jar rewrites the file on the card,
	// synchronously; a script setting cookies in a loop would otherwise stall
	// the pass on card writes and wear the card for nothing.
	webJSNetMaxCookieSets = 64
	// webJSNetMaxReferrer is the longest page address sent as Referer; a
	// longer one is cut to its origin, as browsers do.
	webJSNetMaxReferrer = 4096
	// webJSNetMaxLogLines is how many lines one page's requests may add to
	// the log, so a script failing in a loop cannot flood it.
	webJSNetMaxLogLines = 16
	// webJSNetUserAgent is what the page fetch sends (textbrowser.go): a site
	// must see the same browser asking for the page and for its data.
	webJSNetUserAgent = browserChromeUserAgent
	// webJSNetBlockedByFilter and webJSNetBlockedPrivate are the Err texts of
	// the two refusals the toolbar's blocked count includes.
	webJSNetBlockedByFilter = "blocked by the filter"
	webJSNetBlockedPrivate  = "blocked: private network address"
)

// webJSRequest is one request a page script makes, as the JavaScript side
// hands it over. The JavaScript side has resolved the URL and validated the
// options already; everything is checked again here regardless, because this
// is the side that talks to the network.
type webJSRequest struct {
	Method      string      // upper-case; "" = GET
	URL         string      // absolute (JS resolves relative URLs); http, https or data:
	Header      [][2]string // as the script set them, in order (filtered here)
	Body        []byte      // request body; a GET or HEAD request must not have one
	Credentials string      // "omit" | "same-origin" (default) | "include"
	Mode        string      // "cors" (default) | "no-cors" | "same-origin"
	Redirect    string      // "follow" (default) | "error" | "manual"
}

// webJSResponse is what the script may see of the answer. Err set means a
// network error and nothing else is filled in.
type webJSResponse struct {
	Err        string      // non-empty = network error (JS rejects with TypeError "Failed to fetch")
	Type       string      // "basic" | "cors" | "opaque" | "opaqueredirect"
	Status     int         // 0 for "opaque" and "opaqueredirect"
	StatusText string      // "OK", "Not Found" … (http.StatusText)
	URL        string      // final URL, without fragment ("" for "opaque")
	Redirected bool        // at least one redirect was followed
	Header     [][2]string // lower-case names; Set-Cookie never; cross-origin "cors": only safelisted + exposed
	Body       []byte      // nil for "opaque", "opaqueredirect" and HEAD
}

// webJSNet is one page's network: its address, its rules and its counters. It
// is safe for concurrent use; the fields above mu never change after
// newWebJSNet.
type webJSNet struct {
	page     *url.URL      // the page, without fragment
	pageText string        // page.String(): the ad filter keys its per-site switch by it
	origin   string        // serialized origin, "null" for a page that is not http(s)
	site     string        // scheme + registrable domain, "" when cookies do not apply
	referrer string        // what a request to the page's own origin sends as Referer
	guard    webJSNetGuard // how far the page's scripts may reach
	deadline time.Time     // end of the script pass (zero = none)
	sem      chan struct{} // webJSNetMaxParallel slots

	mu         sync.Mutex
	requests   int   // requests made (queued or sent), redirect hops included
	bytes      int64 // response body bytes read
	blocked    int   // refused by the filter or the private-network guard
	logged     int   // log lines written for this page
	cookieSets int   // document.cookie assignments made
}

// webJSNetGuard is how far a page's scripts may reach on the network, decided
// by where the page itself lives.
type webJSNetGuard int

const (
	webJSNetGuardPublic webJSNetGuard = iota // a public page: the public internet only
	webJSNetGuardLAN                         // a page on the LAN: the LAN too, never this device
	webJSNetGuardLocal                       // a page this device serves: anything
)

// webJSNetPlace is where an address leads, as coarsely as the guard needs.
type webJSNetPlace int

const (
	webJSNetPublic   webJSNetPlace = iota // the public internet
	webJSNetPrivate                       // the LAN: private, link-local, CGNAT, unique-local, multicast, reserved
	webJSNetLoopback                      // this device: loopback, unspecified, 0.0.0.0/8
)

// webJSNetKind is what a request is for: scripts follow their own rules.
type webJSNetKind int

const (
	webJSNetFetch   webJSNetKind = iota // fetch() and XMLHttpRequest
	webJSNetClassic                     // a classic <script src>
	webJSNetModule                      // an ES module script, CORS-checked like a fetch
)

var (
	// webJSNetErrPrivate is the guard's refusal, from the dialer or from the
	// transport's proxy hook.
	webJSNetErrPrivate = errors.New("refused: a private network address")
	// webJSNetErrStop ends a redirect chain the request refused; the call's
	// fail field says why, in words a console message can use.
	webJSNetErrStop = errors.New("redirect refused")
	// webJSNetErrBudget stops a body read that went past the page's budget.
	webJSNetErrBudget = errors.New("the page's network budget is spent")
)

// newWebJSNet builds the network one page load's scripts use. page is the
// page's own address after its redirects; deadline is the end of the script
// pass, after which every request fails at once (zero: no pass deadline, the
// per-request cap still holds).
func newWebJSNet(page *url.URL, deadline time.Time) *webJSNet {
	p := &url.URL{Scheme: "about", Opaque: "blank"}
	if page != nil {
		p = webJSNetBareURL(page)
	}
	origin := webJSNetOrigin(p)
	return &webJSNet{
		page:     p,
		pageText: p.String(),
		origin:   origin,
		site:     webJSNetSite(p),
		referrer: webJSNetReferrerText(p, origin),
		guard:    webJSNetPageGuard(p),
		deadline: deadline,
		sem:      make(chan struct{}, webJSNetMaxParallel),
	}
}

// do runs one fetch() or XMLHttpRequest. It never panics, and an Err response
// carries nothing else: the script sees a network error.
func (n *webJSNet) do(ctx context.Context, req webJSRequest) webJSResponse {
	return n.fetch(ctx, req, webJSNetFetch)
}

// fetchScript downloads a <script src> or a module. A classic script is not
// read by the page the way a fetch is — it runs — so a cross-origin one needs
// no CORS permission, exactly as in a browser, and comes back as Type "opaque"
// with its body (the runtime may mute its error messages, as browsers do). A
// module is CORS-checked and sends cookies only to the page's own origin. A
// script's Header is for the runtime (the charset), never for page scripts.
// Either way an HTML page, media or a non-2xx answer is refused: running a
// login wall or a 404 page as JavaScript only throws.
func (n *webJSNet) fetchScript(ctx context.Context, rawURL string, module bool) webJSResponse {
	req := webJSRequest{Method: http.MethodGet, URL: rawURL, Credentials: "include", Mode: "no-cors", Redirect: "follow"}
	kind := webJSNetClassic
	if module {
		req.Credentials, req.Mode, kind = "same-origin", "cors", webJSNetModule
	}
	return n.fetch(ctx, req, kind)
}

// fetch is do and fetchScript: the checks, the request, and the filtering of
// the answer, in that order.
func (n *webJSNet) fetch(ctx context.Context, req webJSRequest, kind webJSNetKind) (resp webJSResponse) {
	// The runtime calls this from its own goroutines, where a panic would take
	// the whole launcher down with the page.
	defer func() {
		if r := recover(); r != nil {
			logDebug("browser", "page scripts: request failed inside the network layer: %v", r)
			resp = webJSNetFail("internal error")
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	raw := strings.TrimSpace(req.URL)
	if len(raw) >= 5 && strings.EqualFold(raw[:5], "data:") {
		return webJSNetDataResponse(raw, kind)
	}
	if len(raw) > webJSNetMaxURL {
		return webJSNetFail("address too long")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return webJSNetFail("not an absolute address")
	}
	u.Fragment, u.RawFragment = "", ""
	if why := n.refuse(u); why != "" {
		return webJSNetFail(why)
	}
	method, ok := webJSNetMethod(req.Method)
	if !ok {
		return webJSNetFail("method not allowed")
	}
	mode := webJSNetPick(req.Mode, "cors", "no-cors", "same-origin")
	creds := webJSNetPick(req.Credentials, "same-origin", "omit", "include")
	redirect := webJSNetPick(req.Redirect, "follow", "error", "manual")
	cross := !n.sameOrigin(u)
	switch {
	case len(req.Body) > 0 && (method == http.MethodGet || method == http.MethodHead):
		return webJSNetFail("a GET or HEAD request cannot have a body")
	case len(req.Body) > webJSNetMaxUpload:
		return webJSNetFail("request body too large")
	case mode == "same-origin" && cross:
		return webJSNetFail("a same-origin request to another origin")
	case mode == "no-cors" && !webJSNetSimpleMethod(method):
		return webJSNetFail("no-cors allows only GET, HEAD and POST")
	case mode == "no-cors" && redirect != "follow":
		return webJSNetFail("a no-cors request must follow redirects")
	}
	header, simple := webJSNetRequestHeader(req.Header, mode == "no-cors")
	if creds == "include" && (!simple || !webJSNetSimpleMethod(method)) {
		// A browser would preflight this request and send nothing unless the
		// other origin agreed. No preflight is sent here, so the request goes
		// without cookies to any origin but the page's own: whatever it asks
		// of another server, it asks as a stranger, not as the user.
		creds = "same-origin"
	}
	header.Set("User-Agent", webJSNetUserAgent)
	if header.Get("Accept") == "" {
		header.Set("Accept", "*/*")
	}
	n.hopHeaders(header, u, method, mode != "no-cors")
	browserChromeFetch(header, mode, n.page, u)

	until := time.Now().Add(webJSNetMaxWait)
	if !n.deadline.IsZero() && n.deadline.Before(until) {
		until = n.deadline
	}
	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	if ctx.Err() != nil {
		return webJSNetFail("the page's time for scripts is up")
	}
	// The request is charged before it queues for a connection: a script
	// calling fetch() in a loop finds the page's budget spent at once,
	// instead of parking a goroutine (and a copy of the request's body) per
	// call until the pass ends.
	if !n.takeRequest() {
		n.note("request limit of %d reached", webJSNetMaxRequests)
		return webJSNetFail("too many requests from this page")
	}
	select {
	case n.sem <- struct{}{}:
	case <-ctx.Done():
		return webJSNetFail("timed out waiting for a connection")
	}
	defer func() { <-n.sem }()
	if n.spent() {
		return webJSNetFail(webJSNetErrBudget.Error())
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	// The note is how the transport's proxy hook tells its dialer which dial
	// is the user's own proxy (webJSNetNewTransport).
	ctx = context.WithValue(ctx, webJSNetProxyKey{}, &webJSNetProxyNote{})
	hreq, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return webJSNetFail("the request cannot be built")
	}
	hreq.Header = header
	call := &webJSNetCall{n: n, mode: mode, creds: creds, redirect: redirect, crossed: cross}
	client := &http.Client{Transport: webJSNetTransport(n.guard), CheckRedirect: call.checkRedirect}
	if creds != "omit" {
		client.Jar = call
	}
	hresp, err := client.Do(hreq)
	if err != nil {
		if hresp != nil && hresp.Body != nil {
			// Do hands back the last response, its body already closed, when a
			// redirect is refused; closing it again is harmless.
			hresp.Body.Close()
		}
		return n.failure(ctx, call, u, err)
	}
	defer hresp.Body.Close()
	return n.respond(call, u, hresp, kind, method)
}

// respond turns a response into what the script may see of it.
func (n *webJSNet) respond(call *webJSNetCall, asked *url.URL, resp *http.Response, kind webJSNetKind, method string) webJSResponse {
	call.mu.Lock()
	crossed, sent, hops, manual := call.crossed, call.sent, call.hops, call.manual
	call.mu.Unlock()
	final := asked
	if resp.Request != nil && resp.Request.URL != nil {
		final = webJSNetBareURL(resp.Request.URL)
	}
	if !n.sameOrigin(final) {
		crossed = true
	}
	if manual {
		// The redirect itself, with nothing of it readable: the script learns
		// that there was one and where it was asked, not where it leads.
		return webJSResponse{Type: "opaqueredirect", URL: final.String()}
	}
	out := webJSResponse{
		Type:       "basic",
		Status:     resp.StatusCode,
		StatusText: http.StatusText(resp.StatusCode),
		URL:        final.String(),
		Redirected: hops > 0,
	}
	limit := int64(webJSNetMaxBody)
	if kind == webJSNetFetch {
		if crossed {
			if call.mode == "no-cors" {
				// An opaque answer: the request happened, nothing of the
				// response is the page's to read, so none of it is downloaded.
				return webJSResponse{Type: "opaque"}
			}
			if !webJSNetCORSAllowed(resp.Header, n.origin, sent) {
				n.note("CORS: %s is not shared with %s", webJSNetLogURL(final), n.origin)
				return webJSNetFail("CORS")
			}
			out.Type = "cors"
		}
		out.Header = webJSNetResponseHeader(resp.Header, crossed, sent)
		if method == http.MethodHead {
			return out
		}
	} else {
		limit = webJSNetMaxScript
		if kind == webJSNetModule && crossed && !webJSNetCORSAllowed(resp.Header, n.origin, sent) {
			n.note("CORS: module %s is not shared with %s", webJSNetLogURL(final), n.origin)
			return webJSNetFail("CORS")
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return webJSNetFail("HTTP " + strconv.Itoa(resp.StatusCode))
		}
		if why := webJSNetScriptTypeRefused(resp.Header, kind == webJSNetModule); why != "" {
			return webJSNetFail(why)
		}
		switch {
		case !crossed:
		case kind == webJSNetModule:
			out.Type = "cors"
		default:
			// A classic script from elsewhere runs, but the page may not
			// read it; the type says so, for the runtime to mute its errors.
			out.Type = "opaque"
		}
		// Only the runtime sees a script's headers (it decodes the script by
		// the Content-Type charset); no page script can read them, so they
		// are not CORS-filtered — but Set-Cookie still never leaves here.
		out.Header = webJSNetResponseHeader(resp.Header, false, sent)
	}
	body, why := n.readBody(resp, limit)
	if why != "" {
		return webJSNetFail(why)
	}
	out.Body = body
	return out
}

// failure turns a client error into the script's network error, counting and
// logging the refusals that matter.
func (n *webJSNet) failure(ctx context.Context, call *webJSNetCall, asked *url.URL, err error) webJSResponse {
	call.mu.Lock()
	why := call.fail
	call.mu.Unlock()
	switch {
	case why != "" && errors.Is(err, webJSNetErrStop):
		return webJSNetFail(why)
	case errors.Is(err, webJSNetErrPrivate):
		n.addBlocked()
		n.note("refused a private address behind %s", webJSNetLogURL(asked))
		return webJSNetFail(webJSNetBlockedPrivate)
	case ctx.Err() != nil:
		return webJSNetFail("timed out")
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		// url.Error repeats the whole address, query string included; only
		// the cause may go to the log.
		err = uerr.Err
	}
	n.note("%s failed: %v", webJSNetLogURL(asked), err)
	return webJSNetFail("network error")
}

// readBody reads the body through the page's byte meter, asking for one byte
// more than the limit: a body exactly at the limit passes, and one byte over it
// is refused instead of arriving cut short — a truncated JSON document or
// script would only fail later, and less clearly.
func (n *webJSNet) readBody(resp *http.Response, limit int64) ([]byte, string) {
	if resp.ContentLength > limit {
		return nil, "response too large"
	}
	body, err := io.ReadAll(io.LimitReader(webJSNetMeter{n: n, r: resp.Body}, limit+1))
	switch {
	case errors.Is(err, webJSNetErrBudget):
		n.note("byte budget of %d MiB spent", webJSNetMaxTotal>>20)
		return nil, webJSNetErrBudget.Error()
	case err != nil:
		return nil, "the response stopped arriving"
	case int64(len(body)) > limit:
		return nil, "response too large"
	}
	return body, ""
}

// webJSNetMeter counts body bytes against the page's budget as they arrive,
// so six parallel downloads cannot each start under the budget and together
// end far past it.
type webJSNetMeter struct {
	n *webJSNet
	r io.Reader
}

func (m webJSNetMeter) Read(p []byte) (int, error) {
	k, err := m.r.Read(p)
	if k > 0 && !m.n.addBytes(int64(k)) {
		return k, webJSNetErrBudget
	}
	return k, err
}

// refuse runs the checks every address must pass before anything is sent to
// it: the first one, and every redirect hop. "" means go ahead.
func (n *webJSNet) refuse(u *url.URL) string {
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "only http and https addresses can be fetched"
	case u.User != nil:
		// fetch() refuses these too: a password in the address is a way to
		// send credentials that no cookie rule would ever see.
		return "an address with a user name or password"
	case webJSNetHost(u.Hostname()) == "":
		return "an address without a host"
	case n.page.Scheme == "https" && u.Scheme == "http" && !webJSNetLoopbackName(u.Hostname()):
		// Mixed content, refused as browsers refuse it: whoever runs the
		// Wi-Fi could rewrite a plain-http script or answer, and the result
		// would run with the https page's origin — its cookies, its data.
		n.note("refused http content on an https page: %s", webJSNetLogURL(u))
		return "an https page cannot load http content"
	}
	if browserRequestBlocked(n.pageText, webJSNetFilterURL(u)) {
		n.addBlocked()
		n.note("filtered %s", webJSNetLogURL(u))
		return webJSNetBlockedByFilter
	}
	if !webJSNetNameAllowed(n.guard, u.Hostname()) {
		n.addBlocked()
		n.note("refused private address %s", webJSNetLogURL(u))
		return webJSNetBlockedPrivate
	}
	return ""
}

// takeRequest claims one of the page's requests; false once they are gone.
func (n *webJSNet) takeRequest() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.requests >= webJSNetMaxRequests {
		return false
	}
	n.requests++
	return true
}

// addBytes counts body bytes; false once the page is past its budget.
func (n *webJSNet) addBytes(k int64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.bytes += k
	return n.bytes <= webJSNetMaxTotal
}

// spent reports whether the page has used its whole byte budget.
func (n *webJSNet) spent() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.bytes >= webJSNetMaxTotal
}

func (n *webJSNet) addBlocked() {
	n.mu.Lock()
	n.blocked++
	n.mu.Unlock()
}

// note logs one line about the page's requests, up to webJSNetMaxLogLines per
// page. Addresses go through webJSNetLogURL, which drops the query string.
func (n *webJSNet) note(format string, args ...any) {
	n.mu.Lock()
	n.logged++
	line := n.logged
	n.mu.Unlock()
	switch {
	case line <= webJSNetMaxLogLines:
		logDebug("browser", "page scripts: "+format, args...)
	case line == webJSNetMaxLogLines+1:
		logDebug("browser", "page scripts: further network notes for this page are not logged")
	}
}

// stats reports the page's requests made (redirect hops included; one that
// timed out waiting for a connection counts too), response body bytes read,
// and requests refused by the filter or the guard.
func (n *webJSNet) stats() (requests int, bytes int64, blocked int) {
	if n == nil {
		return 0, 0, 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.requests, n.bytes, n.blocked
}

// sameOrigin reports whether u has the page's origin. A page without an http(s)
// origin is same-origin with nothing.
func (n *webJSNet) sameOrigin(u *url.URL) bool {
	return n.origin != "null" && webJSNetOrigin(u) == n.origin
}

// sameSite reports whether u is on the page's site: same scheme and same
// registrable domain ("schemeful" same-site, as current browsers have it, so a
// script on a plain-http page cannot send the https site's cookies).
func (n *webJSNet) sameSite(u *url.URL) bool {
	return n.site != "" && webJSNetSite(u) == n.site
}

// referrerFor is the Referer a request to target carries, by the policy
// browsers default to (strict-origin-when-cross-origin): the page address to
// its own origin, only the origin to anyone else, nothing from https to http.
func (n *webJSNet) referrerFor(target *url.URL) string {
	switch {
	case n.referrer == "":
		return ""
	case n.page.Scheme == "https" && target.Scheme != "https":
		return ""
	case n.sameOrigin(target):
		return n.referrer
	}
	return n.origin + "/"
}

// hopHeaders sets the Origin and Referer one hop carries. They are worked out
// per hop rather than copied from the first request, which is what net/http
// does on a redirect: a Referer meant for the page's own origin must not travel
// on to a third party, or from https to http.
func (n *webJSNet) hopHeaders(h http.Header, target *url.URL, method string, cors bool) {
	h.Del("Origin")
	h.Del("Referer")
	if ref := n.referrerFor(target); ref != "" {
		h.Set("Referer", ref)
	}
	if (cors && !n.sameOrigin(target)) || (method != http.MethodGet && method != http.MethodHead) {
		h.Set("Origin", n.origin)
	}
}

// cookieString is document.cookie's getter: the cookies a request for the page
// itself would carry, minus every HttpOnly one.
func (n *webJSNet) cookieString() string {
	if n == nil || n.site == "" {
		return ""
	}
	jar, ok := browserCookies().(*browserCookieJar)
	if !ok {
		return ""
	}
	return jar.webJSNetDocumentCookie(n.page)
}

// setCookieString is document.cookie's setter. A script can never set an
// HttpOnly cookie, a Secure one only from an https page, and never one that
// would replace or sit beside an HttpOnly cookie of the same name.
func (n *webJSNet) setCookieString(line string) {
	if n == nil || n.site == "" || len(line) > webJSNetMaxCookieLine {
		return
	}
	n.mu.Lock()
	n.cookieSets++
	sets := n.cookieSets
	n.mu.Unlock()
	if sets > webJSNetMaxCookieSets {
		if sets == webJSNetMaxCookieSets+1 {
			n.note("document.cookie limit of %d reached", webJSNetMaxCookieSets)
		}
		return
	}
	c, err := http.ParseSetCookie(line)
	if err != nil || c == nil {
		return
	}
	c.HttpOnly = false
	if !webJSNetCookieAllowed(n.page, c) {
		return
	}
	jar, ok := browserCookies().(*browserCookieJar)
	if !ok {
		return
	}
	jar.webJSNetScriptSetCookie(n.page, c)
}

// webJSNetCall is one request on its way through its redirects. It is also the
// request's cookie jar: the shared browser jar, narrowed hop by hop to what the
// credentials mode and the SameSite rule allow. The client calls the jar and
// the redirect check one hop after another, but the fields are locked anyway:
// net/http does not promise which goroutine runs them. The fields above mu are
// set before the client starts and only read after.
type webJSNetCall struct {
	n        *webJSNet
	mode     string // "cors" | "no-cors" | "same-origin"
	creds    string // "omit" | "same-origin" | "include"
	redirect string // "follow" | "error" | "manual"

	mu      sync.Mutex
	crossed bool   // some hop left the page's origin
	sent    bool   // the latest hop carried cookies
	hops    int    // redirects followed
	manual  bool   // stopped at a redirect (redirect: "manual")
	fail    string // why a redirect was refused
}

// Cookies is the jar's read half, asked once per hop.
func (c *webJSNetCall) Cookies(u *url.URL) []*http.Cookie {
	var out []*http.Cookie
	if c.cookiesFor(u) {
		if jar := browserCookies(); jar != nil {
			out = jar.Cookies(u)
		}
	}
	c.mu.Lock()
	c.sent = len(out) > 0
	c.mu.Unlock()
	return out
}

// SetCookies is the jar's write half: a response stores cookies only where its
// request could have carried them, and only cookies a browser would accept
// from that address.
func (c *webJSNetCall) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if !c.cookiesFor(u) {
		return
	}
	keep := make([]*http.Cookie, 0, len(cookies))
	for _, ck := range cookies {
		if webJSNetCookieAllowed(u, ck) {
			keep = append(keep, ck)
		}
	}
	if len(keep) == 0 {
		return
	}
	if jar := browserCookies(); jar != nil {
		jar.SetCookies(u, keep)
	}
}

// cookiesFor is the SameSite=Lax rule plus the credentials mode: "include"
// means the page's site, "same-origin" the page's origin, "omit" nothing.
func (c *webJSNetCall) cookiesFor(u *url.URL) bool {
	switch c.creds {
	case "include":
		return c.n.sameSite(u)
	case "same-origin":
		return c.n.sameOrigin(u)
	}
	return false
}

// checkRedirect is the client's CheckRedirect: every hop passes the same
// checks as the first address, uses one of the page's requests, and gets its
// own Origin and Referer.
func (c *webJSNetCall) checkRedirect(req *http.Request, via []*http.Request) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.redirect {
	case "error":
		c.fail = "redirected, and the request said redirect: \"error\""
		return webJSNetErrStop
	case "manual":
		c.manual = true
		return http.ErrUseLastResponse
	}
	if len(via) > webJSNetMaxRedirects {
		c.fail = "too many redirects"
		return webJSNetErrStop
	}
	if why := c.n.refuse(req.URL); why != "" {
		c.fail = why
		return webJSNetErrStop
	}
	cross := !c.n.sameOrigin(req.URL)
	if cross && c.mode == "same-origin" {
		c.fail = "a same-origin request redirected to another origin"
		return webJSNetErrStop
	}
	if !c.n.takeRequest() {
		c.fail = "too many requests from this page"
		return webJSNetErrStop
	}
	c.crossed = c.crossed || cross
	c.hops++
	c.n.hopHeaders(req.Header, req.URL, req.Method, c.mode != "no-cors")
	return nil
}

// webJSNetDocumentCookie is document.cookie for page: the jar's own choice
// (expiry, path, Secure), with every cookie a server set HttpOnly left out. The
// standard jar does not remember HttpOnly, so the records it mirrors to the card
// say which ones were: a jar cookie is hidden when an HttpOnly record with its
// name and value covers the page's host. Two cookies that share a name and a
// value are both hidden when either is HttpOnly — the safe way to be wrong.
// Hosts are compared in webJSNetCookieHost's form, which folds every spelling
// the jar itself treats as one host (case, a trailing dot, punycode); the jar
// is asked under webJSNetJarURL's spelling, the one the records can vouch for.
// A retired jar answers nothing: its records are gone, its cookies are not.
func (j *browserCookieJar) webJSNetDocumentCookie(page *url.URL) string {
	if j == nil || page == nil {
		return ""
	}
	at := webJSNetJarURL(page)
	if at == nil {
		return ""
	}
	host := webJSNetCookieHost(page.Hostname())
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.forgotten {
		return ""
	}
	hidden := map[string]bool{}
	for _, rec := range j.recs {
		if rec.HTTPOnly && webJSNetRecordCovers(rec, host) {
			hidden[rec.Name+"\x00"+rec.Value] = true
		}
	}
	var b strings.Builder
	for _, c := range j.jar.Cookies(at) {
		if c == nil || hidden[c.Name+"\x00"+c.Value] {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(c.Name)
		b.WriteByte('=')
		if c.Quoted {
			b.WriteByte('"')
			b.WriteString(c.Value)
			b.WriteByte('"')
		} else {
			b.WriteString(c.Value)
		}
	}
	return b.String()
}

// webJSNetScriptSetCookie stores a cookie document.cookie set, unless an
// HttpOnly cookie of the same name could be sent anywhere this one would be: a
// script that cannot read the session cookie must not overwrite it either, nor
// plant a same-named one that a server might read first. This is SetCookies
// with that check in front, under one hold of the lock, so a response landing
// in between cannot slip an HttpOnly cookie past it. The cookie is stored
// under webJSNetJarURL's spelling of the page, where the records answer for
// every cookie a script could replace.
func (j *browserCookieJar) webJSNetScriptSetCookie(page *url.URL, c *http.Cookie) bool {
	if j == nil || page == nil || c == nil {
		return false
	}
	at := webJSNetJarURL(page)
	if at == nil {
		return false
	}
	host := webJSNetCookieHost(page.Hostname())
	domain := webJSNetCookieDomain(c.Domain)
	j.mu.Lock()
	if j.forgotten {
		j.mu.Unlock()
		return false
	}
	for _, rec := range j.recs {
		if rec.HTTPOnly && rec.Name == c.Name && webJSNetScopesOverlap(host, domain, rec) && j.holdsLocked(rec) {
			j.mu.Unlock()
			return false
		}
	}
	cookies := []*http.Cookie{c}
	j.jar.SetCookies(at, cookies)
	changed := j.trackLocked(at, cookies)
	j.mu.Unlock()
	if changed {
		j.save()
	}
	return true
}

// webJSNetJarURL is the page's address as the cookie records spell it: the
// host lower-cased the way trackLocked stores it, and an explicit port. The
// standard jar keeps the brackets of a port-less http://[::1]/ in the name it
// files cookies under ("[::1]"), and encodes an upper-case BÜCHER.example to a
// punycode of its own, while a record's address, rebuilt from its host, leads
// to "::1" and to bücher's punycode — and save() drops every record the jar
// does not answer for at that address. An HttpOnly cookie filed under the
// first spelling loses the only note that it is HttpOnly. document.cookie
// reads and writes under the second, where every cookie has its record; for
// an ordinary host the two are the same.
func webJSNetJarURL(page *url.URL) *url.URL {
	host := strings.ToLower(page.Hostname())
	if host == "" {
		return nil
	}
	port := page.Port()
	if port == "" {
		port = "80"
		if page.Scheme == "https" {
			port = "443"
		}
	}
	return &url.URL{Scheme: page.Scheme, Host: net.JoinHostPort(host, port), Path: page.Path}
}

// webJSNetRecordCovers reports whether a stored cookie is sent to host (in
// webJSNetCookieHost's form): its own host for a host-only cookie, the domain
// and its subdomains otherwise.
func webJSNetRecordCovers(rec browserCookieRecord, host string) bool {
	if domain := webJSNetCookieDomain(rec.Domain); domain != "" {
		return webJSNetDomainMatch(host, domain)
	}
	return webJSNetCookieHost(rec.Host) == host
}

// webJSNetScopesOverlap reports whether a cookie set on host (for domain; ""
// means host-only; both in webJSNetCookieHost's form) could be sent to some
// host the record's cookie is sent to. Paths are ignored on purpose: overlap
// anywhere on the site is enough to refuse.
func webJSNetScopesOverlap(host, domain string, rec browserCookieRecord) bool {
	recHost := webJSNetCookieHost(rec.Host)
	recDomain := webJSNetCookieDomain(rec.Domain)
	switch {
	case domain == "" && recDomain == "":
		return host == recHost
	case domain == "":
		return webJSNetDomainMatch(host, recDomain)
	case recDomain == "":
		return webJSNetDomainMatch(recHost, domain)
	}
	return webJSNetDomainMatch(domain, recDomain) || webJSNetDomainMatch(recDomain, domain)
}

// webJSNetDomainMatch is the cookie domain match: host is domain or below it.
func webJSNetDomainMatch(host, domain string) bool {
	return domain != "" && (host == domain || strings.HasSuffix(host, "."+domain))
}

// webJSNetCookieAllowed applies what the standard jar does not know: a Secure
// cookie only from https (a network attacker on plain http must not plant
// one), and the __Secure- and __Host- name prefixes, which promise exactly
// that about a cookie.
func webJSNetCookieAllowed(u *url.URL, c *http.Cookie) bool {
	if u == nil || c == nil {
		return false
	}
	if c.Secure && u.Scheme != "https" {
		return false
	}
	name := strings.ToLower(c.Name)
	switch {
	case strings.HasPrefix(name, "__secure-"):
		return c.Secure
	case strings.HasPrefix(name, "__host-"):
		return c.Secure && c.Domain == "" && c.Path == "/"
	}
	return true
}

// webJSNetFail is a network error.
func webJSNetFail(why string) webJSResponse {
	return webJSResponse{Err: why}
}

// webJSNetPick normalises one of the request's enumerated options: def when it
// is empty or not one the fetch standard defines. The JavaScript side has
// validated it already; this is the fail-safe.
func webJSNetPick(v, def string, more ...string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, ok := range more {
		if v == ok {
			return v
		}
	}
	return def
}

// webJSNetMethod validates a request method. The common ones are upper-cased
// (servers compare them case-sensitively); CONNECT, TRACE and TRACK are never
// sent, as in every browser.
func webJSNetMethod(m string) (string, bool) {
	m = strings.TrimSpace(m)
	if m == "" {
		return http.MethodGet, true
	}
	if !webJSNetToken(m) {
		return "", false
	}
	upper := strings.ToUpper(m)
	switch upper {
	case "CONNECT", "TRACE", "TRACK":
		return "", false
	case http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut:
		return upper, true
	}
	return m, true
}

// webJSNetSimpleMethod reports a CORS-safelisted method: one a form or an image
// could send without any script.
func webJSNetSimpleMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodPost
}

// webJSNetToken reports whether s is an HTTP token (a valid header name or
// method).
func webJSNetToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// webJSNetHeaderValueOK reports whether a header value can be sent as it is:
// no control characters, so no CR/LF to inject a header of a script's own.
func webJSNetHeaderValueOK(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// webJSNetForbidden are the request headers no script may set: the ones that
// describe the connection, the ones the browser vouches for (cookies, Origin,
// Referer, the sec- metadata) and the CORS preflight's own. The proxy-,
// sec- and access-control-request- families are matched by prefix.
var webJSNetForbidden = map[string]bool{
	"accept-charset":    true,
	"accept-encoding":   true,
	"connection":        true,
	"content-length":    true,
	"cookie":            true,
	"cookie2":           true,
	"date":              true,
	"dnt":               true,
	"expect":            true,
	"host":              true,
	"keep-alive":        true,
	"origin":            true,
	"referer":           true,
	"set-cookie":        true,
	"te":                true,
	"trailer":           true,
	"transfer-encoding": true,
	"upgrade":           true,
	"user-agent":        true,
	"via":               true,
}

// webJSNetForbiddenHeader reports whether a script's header is dropped. The
// method-override headers are dropped only when they name a forbidden method,
// as browsers do, so TRACE cannot come in through the back door. User-Agent
// is in the list because the page fetch's own is always sent instead.
func webJSNetForbiddenHeader(name, value string) bool {
	if webJSNetForbidden[name] || strings.HasPrefix(name, "proxy-") || strings.HasPrefix(name, "sec-") ||
		strings.HasPrefix(name, "access-control-request-") {
		return true
	}
	switch name {
	case "x-http-method", "x-http-method-override", "x-method-override":
		for _, m := range strings.Split(value, ",") {
			switch strings.ToUpper(strings.TrimSpace(m)) {
			case "CONNECT", "TRACE", "TRACK":
				return true
			}
		}
	}
	return false
}

// webJSNetLanguageChars are the bytes a safelisted Accept-Language or
// Content-Language value may hold.
const webJSNetLanguageChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz *,-.;="

// webJSNetSafelistedRequest reports a CORS-safelisted request header: one a
// plain HTML form could send, so no server can be surprised by it.
func webJSNetSafelistedRequest(name, value string) bool {
	if len(value) > 128 {
		return false
	}
	switch name {
	case "accept", "content-type":
		for i := 0; i < len(value); i++ {
			if c := value[i]; (c < 0x20 && c != '\t') || c == 0x7f || strings.IndexByte("\"():<>?@[\\]{}", c) >= 0 {
				return false
			}
		}
		if name == "accept" {
			return true
		}
		essence, _, _ := strings.Cut(strings.ToLower(value), ";")
		switch strings.TrimSpace(essence) {
		case "application/x-www-form-urlencoded", "multipart/form-data", "text/plain":
			return true
		}
	case "accept-language", "content-language":
		for i := 0; i < len(value); i++ {
			if strings.IndexByte(webJSNetLanguageChars, value[i]) < 0 {
				return false
			}
		}
		return true
	case "range":
		return strings.HasPrefix(value, "bytes=") && !strings.Contains(value, ",")
	}
	return false
}

// webJSNetRequestHeader filters a script's headers: invalid and forbidden ones
// are dropped, and in no-cors mode everything but the safelisted ones. simple
// reports whether every header kept is safelisted — with a safelisted method,
// a request that needs no preflight.
func webJSNetRequestHeader(pairs [][2]string, noCORS bool) (http.Header, bool) {
	h := http.Header{}
	simple := true
	kept := 0
	for _, kv := range pairs {
		name := strings.ToLower(strings.TrimSpace(kv[0]))
		value := strings.Trim(kv[1], " \t")
		if !webJSNetToken(name) || !webJSNetHeaderValueOK(value) || len(value) > webJSNetMaxHeaderValue ||
			webJSNetForbiddenHeader(name, value) {
			continue
		}
		safe := webJSNetSafelistedRequest(name, value)
		if noCORS && (!safe || name == "range") {
			continue
		}
		if kept >= webJSNetMaxHeaders {
			break
		}
		kept++
		simple = simple && safe
		h.Add(name, value)
	}
	return h, simple
}

// webJSNetSafelistedResponse are the response headers any cross-origin reader
// may see without the server listing them.
var webJSNetSafelistedResponse = map[string]bool{
	"cache-control":    true,
	"content-language": true,
	"content-length":   true,
	"content-type":     true,
	"expires":          true,
	"last-modified":    true,
	"pragma":           true,
}

// webJSNetResponseHeader is the header list the script sees, sorted by name as
// a browser's Headers iterates it: never Set-Cookie, and for a cross-origin
// ("cors") answer only the safelisted names and the ones the server exposed
// (Access-Control-Expose-Headers; "*" exposes all, but not to a credentialed
// request).
func webJSNetResponseHeader(h http.Header, cors, credentialed bool) [][2]string {
	all := !cors
	exposed := map[string]bool{}
	if cors {
		for _, v := range h.Values("Access-Control-Expose-Headers") {
			for _, name := range strings.Split(v, ",") {
				name = strings.ToLower(strings.TrimSpace(name))
				if name == "*" && !credentialed {
					all = true
				}
				exposed[name] = true
			}
		}
	}
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Slice(names, func(a, b int) bool {
		la, lb := strings.ToLower(names[a]), strings.ToLower(names[b])
		if la != lb {
			return la < lb
		}
		return names[a] < names[b]
	})
	var out [][2]string
	for _, name := range names {
		lower := strings.ToLower(name)
		if lower == "set-cookie" || lower == "set-cookie2" {
			continue
		}
		if !all && !webJSNetSafelistedResponse[lower] && !exposed[lower] {
			continue
		}
		for _, v := range h[name] {
			out = append(out, [2]string{lower, v})
		}
	}
	return out
}

// webJSNetCORSAllowed is the CORS read check for a cross-origin answer: the
// server must name the page's origin, or say "*" for a request that carried
// no cookies; one that carried cookies also needs
// Access-Control-Allow-Credentials: true. Two Allow-Origin headers are a
// refusal, as in browsers.
func webJSNetCORSAllowed(h http.Header, origin string, credentialed bool) bool {
	values := h.Values("Access-Control-Allow-Origin")
	if len(values) != 1 {
		return false
	}
	allow := strings.TrimSpace(values[0])
	if allow == "*" {
		return !credentialed
	}
	if allow != origin {
		return false
	}
	return !credentialed || strings.TrimSpace(h.Get("Access-Control-Allow-Credentials")) == "true"
}

// webJSNetScriptTypeRefused is why a script answer is not run, "" when it may
// be: an HTML page (a soft 404, a login wall, a captcha) is never a script,
// media never is, and a server that says nosniff is taken at its word — which
// keeps a JSON API with cookies from being run as a script to read it.
func webJSNetScriptTypeRefused(h http.Header, module bool) string {
	essence, _, _ := strings.Cut(strings.ToLower(h.Get("Content-Type")), ";")
	essence = strings.TrimSpace(essence)
	switch {
	case essence == "text/html", essence == "application/xhtml+xml":
		return "the server sent an HTML page, not a script"
	case strings.HasPrefix(essence, "image/"), strings.HasPrefix(essence, "audio/"),
		strings.HasPrefix(essence, "video/"), essence == "text/csv":
		return "the server sent media, not a script"
	}
	if !module && strings.EqualFold(strings.TrimSpace(h.Get("X-Content-Type-Options")), "nosniff") && !webJSNetScriptMIME(essence) {
		return "the server marked the answer nosniff, and it is not JavaScript"
	}
	return ""
}

// webJSNetScriptMIME reports a JavaScript MIME type, by its essence.
func webJSNetScriptMIME(essence string) bool {
	return strings.Contains(essence, "javascript") || strings.Contains(essence, "ecmascript") ||
		essence == "text/jscript" || essence == "text/livescript"
}

// webJSNetDataResponse answers a data: URL without the network. A script wrote
// the data itself, so the answer is the page's own ("basic"), and it is never
// counted as a request.
func webJSNetDataResponse(raw string, kind webJSNetKind) webJSResponse {
	limit := webJSNetMaxBody
	if kind != webJSNetFetch {
		limit = webJSNetMaxScript
	}
	mediaType, body, why := webJSNetDecodeDataURL(raw, limit)
	if why != "" {
		return webJSNetFail(why)
	}
	if kind != webJSNetFetch {
		h := http.Header{}
		h.Set("Content-Type", mediaType)
		if refused := webJSNetScriptTypeRefused(h, kind == webJSNetModule); refused != "" {
			return webJSNetFail(refused)
		}
	}
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	return webJSResponse{
		Type:       "basic",
		Status:     http.StatusOK,
		StatusText: http.StatusText(http.StatusOK),
		URL:        raw,
		Header:     [][2]string{{"content-type", mediaType}},
		Body:       body,
	}
}

// webJSNetDecodeDataURL decodes a data: URL the way the fetch standard does:
// the part after the first comma is percent-decoded, then base64-decoded
// (forgivingly) when the media type ends in ";base64". An empty media type is
// text/plain;charset=US-ASCII. why is non-empty when the URL cannot be decoded
// or decodes to more than limit bytes.
func webJSNetDecodeDataURL(raw string, limit int) (mediaType string, body []byte, why string) {
	if len(raw) < 5 || !strings.EqualFold(raw[:5], "data:") {
		return "", nil, "not a data: URL"
	}
	rest := raw[5:]
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return "", nil, "a data: URL without a comma"
	}
	// Percent-encoding at most triples a byte and base64 adds a third more, so
	// a payload this much longer than the limit cannot decode to fit it:
	// refuse before allocating for it.
	if len(payload) > 4*limit+16 {
		return "", nil, "data: URL too large"
	}
	data := webJSNetPercentDecode(payload)
	meta = strings.Trim(meta, " \t\n\f\r")
	if cut, ok := webJSNetCutBase64(meta); ok {
		decoded, ok := webJSNetForgivingBase64(data)
		if !ok {
			return "", nil, "bad base64 in a data: URL"
		}
		meta, data = cut, decoded
	}
	if len(data) > limit {
		return "", nil, "data: URL too large"
	}
	// No type, or one that is not type/subtype, is the standard's default;
	// parameters with no type before them belong to text/plain.
	typ, _, _ := strings.Cut(meta, ";")
	switch {
	case strings.HasPrefix(meta, ";"):
		mediaType = "text/plain" + meta
	case !strings.Contains(typ, "/"):
		mediaType = "text/plain;charset=US-ASCII"
	default:
		mediaType = meta
	}
	return mediaType, data, ""
}

// webJSNetCutBase64 strips a trailing ";base64" (any case, spaces allowed
// before the word) from a data: URL's media type.
func webJSNetCutBase64(meta string) (string, bool) {
	if len(meta) < 6 || !strings.EqualFold(meta[len(meta)-6:], "base64") {
		return meta, false
	}
	head := strings.TrimRight(meta[:len(meta)-6], " ")
	if !strings.HasSuffix(head, ";") {
		return meta, false
	}
	return strings.TrimSpace(head[:len(head)-1]), true
}

// webJSNetForgivingBase64 is the web's base64 decoder: whitespace ignored,
// padding optional, anything else outside the alphabet an error.
func webJSNetForgivingBase64(in []byte) ([]byte, bool) {
	s := make([]byte, 0, len(in))
	for _, b := range in {
		switch b {
		case ' ', '\t', '\n', '\f', '\r':
		default:
			s = append(s, b)
		}
	}
	if len(s)%4 == 0 {
		switch {
		case len(s) >= 2 && s[len(s)-1] == '=' && s[len(s)-2] == '=':
			s = s[:len(s)-2]
		case len(s) >= 1 && s[len(s)-1] == '=':
			s = s[:len(s)-1]
		}
	}
	if len(s)%4 == 1 {
		return nil, false
	}
	out := make([]byte, base64.RawStdEncoding.DecodedLen(len(s)))
	k, err := base64.RawStdEncoding.Decode(out, s)
	if err != nil {
		return nil, false
	}
	return out[:k], true
}

// webJSNetPercentDecode decodes %XX escapes; a "%" not followed by two hex
// digits stays as it is, as browsers leave it.
func webJSNetPercentDecode(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' && i+2 < len(s) {
			hi, okHi := webJSNetUnhex(s[i+1])
			lo, okLo := webJSNetUnhex(s[i+2])
			if okHi && okLo {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// webJSNetUnhex is one hex digit's value.
func webJSNetUnhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// webJSNetHost is the one spelling of a host every rule in this file compares:
// no surrounding space, lower case, no IPv6 brackets, and without the trailing
// dot of a fully qualified name — "Example.COM." is example.com, as it is to
// DNS and to the cookie jar. Only one dot goes, as in the jar, for which
// "example.com.." is another name.
func webJSNetHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.TrimSuffix(h, ".")
}

// webJSNetCookieHost is webJSNetHost with internationalised labels in one
// form, for the HttpOnly checks only. The jar files bücher.example and
// xn--bcher-kva.example under one name, and a record keeps whichever spelling
// its request had, so a punycode label is decoded and lower-cased: both
// spellings meet as "bücher". This can only make more names equal, which for
// hiding and refusing is the safe way to be wrong; the origin and site rules,
// which grant, keep the spellings apart instead.
func webJSNetCookieHost(host string) string {
	h := webJSNetHost(host)
	if !strings.Contains(h, "xn--") {
		return h
	}
	labels := strings.Split(h, ".")
	for i, label := range labels {
		if strings.HasPrefix(label, "xn--") {
			if decoded, ok := webJSNetPunycode(label[len("xn--"):]); ok {
				labels[i] = strings.ToLower(decoded)
			}
		}
	}
	return strings.Join(labels, ".")
}

// webJSNetCookieDomain is a cookie's Domain attribute in webJSNetCookieHost's
// form, without the leading dot RFC 6265 ignores; "" for a host-only cookie.
func webJSNetCookieDomain(domain string) string {
	return webJSNetCookieHost(strings.TrimLeft(strings.TrimSpace(domain), "."))
}

// webJSNetPunycode decodes the punycode of one IDNA label, the part after
// "xn--" (RFC 3492, section 6.2). ok is false for anything that is not the
// encoding of a non-ASCII label: other bytes than ASCII, no encoded character
// at all, a digit missing, an overflow, or a decoded character that is ASCII,
// a surrogate or past Unicode.
func webJSNetPunycode(s string) (string, bool) {
	const (
		base, tmin, tmax, skew, damp = 36, 1, 26, 38, 700
		limit                        = 1<<31 - 1
	)
	if s == "" || len(s) > 255 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return "", false
		}
	}
	var out []rune
	digits := s
	if cut := strings.LastIndexByte(s, '-'); cut > 0 {
		for i := 0; i < cut; i++ {
			out = append(out, rune(s[i]))
		}
		digits = s[cut+1:]
	}
	if digits == "" {
		return "", false
	}
	n, bias, i := int64(128), int64(72), int64(0)
	for p := 0; p < len(digits); {
		old, w := i, int64(1)
		for k := int64(base); ; k += base {
			if p == len(digits) {
				return "", false
			}
			d := webJSNetPunyDigit(digits[p])
			p++
			if d < 0 {
				return "", false
			}
			if i += d * w; i > limit {
				return "", false
			}
			t := k - bias
			if t < tmin {
				t = tmin
			} else if t > tmax {
				t = tmax
			}
			if d < t {
				break
			}
			if w *= base - t; w > limit {
				return "", false
			}
		}
		count := int64(len(out) + 1)
		// The bias adaptation (section 6.1).
		delta := i - old
		if old == 0 {
			delta /= damp
		} else {
			delta /= 2
		}
		delta += delta / count
		k := int64(0)
		for delta > (base-tmin)*tmax/2 {
			delta /= base - tmin
			k += base
		}
		bias = k + (base-tmin+1)*delta/(delta+skew)
		n += i / count
		i %= count
		if n < 0x80 || n > 0x10ffff || (n >= 0xd800 && n <= 0xdfff) {
			return "", false
		}
		out = append(out, 0)
		copy(out[i+1:], out[i:])
		out[i] = rune(n)
		i++
	}
	return string(out), true
}

// webJSNetPunyDigit is a punycode digit's value (a-z 0-25, 0-9 26-35, either
// case), -1 for anything else.
func webJSNetPunyDigit(c byte) int64 {
	switch {
	case c >= 'a' && c <= 'z':
		return int64(c - 'a')
	case c >= 'A' && c <= 'Z':
		return int64(c - 'A')
	case c >= '0' && c <= '9':
		return int64(c-'0') + 26
	}
	return -1
}

// webJSNetOrigin serializes u's origin the way browsers do (scheme://host,
// with the port unless it is the scheme's default), or "null" for anything
// that is not http(s): a null origin is same-origin with nothing. The host is
// webJSNetHost's, so the page and a request compare the way the cookie jar and
// DNS see them.
func webJSNetOrigin(u *url.URL) string {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "null"
	}
	host := webJSNetHost(u.Hostname())
	if host == "" {
		return "null"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host
}

// webJSNetSite is u's site for the SameSite rule: scheme plus registrable
// domain ("https://example.co.uk" for https://www.example.co.uk/), "" for
// anything that is not http(s).
func webJSNetSite(u *url.URL) string {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	host := webJSNetHost(u.Hostname())
	if host == "" {
		return ""
	}
	return u.Scheme + "://" + webJSNetRegistrable(host)
}

// webJSNetRegistrable is host's registrable domain (eTLD+1) by the jar's own
// public-suffix list, so the cookie rule and the jar agree on where one site
// ends. An IP address, a single label or a public suffix is its own site.
func webJSNetRegistrable(host string) string {
	if _, err := netip.ParseAddr(host); err == nil || !strings.Contains(host, ".") {
		return host
	}
	suffix := browserPublicSuffixes{}.PublicSuffix(host)
	if suffix == "" || suffix == host || !strings.HasSuffix(host, "."+suffix) {
		return host
	}
	rest := strings.TrimSuffix(host, "."+suffix)
	if i := strings.LastIndexByte(rest, '.'); i >= 0 {
		rest = rest[i+1:]
	}
	if rest == "" {
		return host
	}
	return rest + "." + suffix
}

// webJSNetReferrerText is the Referer a request to the page's own origin
// carries: the page address without fragment or user name, cut to the origin
// when it is too long.
func webJSNetReferrerText(page *url.URL, origin string) string {
	if origin == "null" {
		return ""
	}
	text := webJSNetBareURL(page).String()
	if len(text) > webJSNetMaxReferrer {
		return origin + "/"
	}
	return text
}

// webJSNetBareURL is a copy of u without user name or fragment.
func webJSNetBareURL(u *url.URL) *url.URL {
	cp := *u
	cp.User = nil
	cp.Fragment, cp.RawFragment = "", ""
	return &cp
}

// webJSNetLogURL is the part of an address that may go to the log: scheme,
// host and (cut) path, never the query, where tokens and personal data travel.
func webJSNetLogURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	path := u.EscapedPath()
	if len(path) > 64 {
		path = path[:64] + "…"
	}
	return u.Scheme + "://" + u.Host + path
}

// webJSNetFilterURL is the address the ad filter is asked about: u without the
// trailing dot of a fully qualified host. The filter's lists name hosts
// without one, and "doubleclick.net." is doubleclick.net to DNS — a script
// must not get past the filter by adding a dot.
func webJSNetFilterURL(u *url.URL) string {
	host := u.Hostname()
	if !strings.HasSuffix(host, ".") {
		return u.String()
	}
	cp := *u
	cp.Host = strings.TrimSuffix(host, ".")
	if port := u.Port(); port != "" {
		cp.Host += ":" + port
	}
	return cp.String()
}

// webJSNetSpecialNets are the ranges the standard library's predicates miss
// that still never mean "a server on the internet": carrier-grade NAT (also
// Tailscale's addresses), the IETF and documentation blocks, benchmarking,
// the reserved 240/4 with the broadcast address, local-use NAT64, discard-only,
// and the old IPv6 site-local range.
var webJSNetSpecialNets = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fec0::/10"),
}

// The IPv6 ranges that carry an IPv4 address inside: a NAT64 gateway or a 6to4
// relay delivers to that IPv4 address, so it is the one judged.
var (
	webJSNetNAT64    = netip.MustParsePrefix("64:ff9b::/96")
	webJSNet6to4     = netip.MustParsePrefix("2002::/16")
	webJSNetV4Compat = netip.MustParsePrefix("::/96")
)

// webJSNetPlaceOf classifies an address. IPv4-mapped and IPv4-embedding IPv6
// forms are judged by the IPv4 address they lead to, so ::ffff:127.0.0.1 is
// this device and 64:ff9b::c0a8:101 is the LAN. An invalid address counts as
// this device: refused unless anything goes.
func webJSNetPlaceOf(ip netip.Addr) webJSNetPlace {
	if !ip.IsValid() {
		return webJSNetLoopback
	}
	ip = ip.WithZone("").Unmap()
	if ip.Is6() && !ip.IsLoopback() && !ip.IsUnspecified() {
		if inner, ok := webJSNetEmbeddedIPv4(ip); ok {
			ip = inner
		}
	}
	switch {
	case ip.IsLoopback(), ip.IsUnspecified():
		return webJSNetLoopback
	case ip.Is4() && ip.As4()[0] == 0:
		// 0.0.0.0/8 is "this network"; Linux connects 0.x.x.x to itself.
		return webJSNetLoopback
	case ip.IsPrivate(), ip.IsLinkLocalUnicast(), ip.IsMulticast():
		return webJSNetPrivate
	}
	for _, p := range webJSNetSpecialNets {
		if p.Contains(ip) {
			return webJSNetPrivate
		}
	}
	return webJSNetPublic
}

// webJSNetEmbeddedIPv4 extracts the IPv4 address a NAT64, 6to4 or
// IPv4-compatible IPv6 address carries.
func webJSNetEmbeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	b := ip.As16()
	switch {
	case webJSNetNAT64.Contains(ip), webJSNetV4Compat.Contains(ip):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case webJSNet6to4.Contains(ip):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return ip, false
}

// webJSNetAllows is the whole guard policy: a public page reaches only the
// public internet, a LAN page the LAN too but never this device, and a page
// this device serves anything.
func webJSNetAllows(guard webJSNetGuard, place webJSNetPlace) bool {
	switch guard {
	case webJSNetGuardLocal:
		return true
	case webJSNetGuardLAN:
		return place != webJSNetLoopback
	}
	return place == webJSNetPublic
}

// webJSNetLocalSuffixes are names only a local resolver answers: mDNS (.local),
// RFC 8375's home.arpa, ICANN's private-use .internal, and the names routers
// and offices use that no registry will ever delegate.
var webJSNetLocalSuffixes = []string{"local", "lan", "home", "home.arpa", "internal", "intranet", "corp", "localdomain", "private"}

// webJSNetNamePlace classifies a host before any lookup. Literal addresses,
// localhost names and names only a local resolver answers are known here;
// known=false is an ordinary DNS name, whose place only the dial can tell. A
// host whose last label is a number is not a name at all but an IPv4 address
// in a form (127.1, 0x7f.1, 2130706433) only some resolvers read: it counts
// as this device rather than be guessed at.
func webJSNetNamePlace(host string) (place webJSNetPlace, known bool) {
	h := webJSNetHost(host)
	if h == "" {
		return webJSNetLoopback, true
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		return webJSNetPlaceOf(ip), true
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || webJSNetNumericLabel(h[strings.LastIndexByte(h, '.')+1:]) {
		return webJSNetLoopback, true
	}
	if !strings.Contains(h, ".") {
		// A single label only resolves through the local network's own
		// resolver or search domain.
		return webJSNetPrivate, true
	}
	for _, s := range webJSNetLocalSuffixes {
		if strings.HasSuffix(h, "."+s) {
			return webJSNetPrivate, true
		}
	}
	return webJSNetPublic, false
}

// webJSNetNumericLabel reports whether a host label is a number, decimal or
// 0x hex. No top-level domain is one.
func webJSNetNumericLabel(label string) bool {
	if label == "" {
		return false
	}
	if len(label) >= 2 && label[0] == '0' && (label[1] == 'x' || label[1] == 'X') {
		for i := 2; i < len(label); i++ {
			if _, ok := webJSNetUnhex(label[i]); !ok {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(label); i++ {
		if label[i] < '0' || label[i] > '9' {
			return false
		}
	}
	return true
}

// webJSNetLoopbackName reports a host that names this device: localhost or a
// literal loopback address. Plain http to it never crosses a network, which
// is why browsers let an https page use it.
func webJSNetLoopbackName(host string) bool {
	h := webJSNetHost(host)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && ip.Unmap().IsLoopback()
}

// webJSNetNameAllowed is the guard's verdict on a host name before any lookup:
// what the name alone says is judged here, and an ordinary DNS name passes to
// be judged again, on the address it resolves to, when it is dialed.
func webJSNetNameAllowed(guard webJSNetGuard, host string) bool {
	place, known := webJSNetNamePlace(host)
	return !known || webJSNetAllows(guard, place)
}

// webJSNetPageGuard picks a page's guard from its address. Only what the
// address itself proves counts: a literal loopback address or the name
// localhost is this device, a private address or a local-only name is the LAN,
// and every other page is public — including a public name that happens to
// resolve to a LAN address, because resolving it again here would hand the
// answer to whoever runs that name's DNS (the rebinding the dial check exists
// for). A numeric host in an odd form proves nothing and is public too, and so
// is a name under .localhost: the hosts file answers for localhost itself, but
// app.localhost may well be asked of the network's DNS, and a page that DNS
// placed there must not be handed this device's services. (As a request's
// target, such a name still counts as this device and is refused.)
func webJSNetPageGuard(page *url.URL) webJSNetGuard {
	if page == nil || (page.Scheme != "http" && page.Scheme != "https") {
		return webJSNetGuardPublic
	}
	host := webJSNetHost(page.Hostname())
	if host == "" {
		return webJSNetGuardPublic
	}
	if _, err := netip.ParseAddr(host); err != nil &&
		(strings.HasSuffix(host, ".localhost") || webJSNetNumericLabel(host[strings.LastIndexByte(host, '.')+1:])) {
		return webJSNetGuardPublic
	}
	// The raw name, so it is canonicalised once: "localhost.." is not localhost.
	place, known := webJSNetNamePlace(page.Hostname())
	switch {
	case !known:
		return webJSNetGuardPublic
	case place == webJSNetLoopback:
		return webJSNetGuardLocal
	case place == webJSNetPrivate:
		return webJSNetGuardLAN
	}
	return webJSNetGuardPublic
}

// webJSNetCheckDial is the dialer's check on the address it is about to
// connect to — already resolved, so whatever DNS said is what is judged.
func webJSNetCheckDial(guard webJSNetGuard, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return webJSNetErrPrivate
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !webJSNetAllows(guard, webJSNetPlaceOf(ip)) {
		return webJSNetErrPrivate
	}
	return nil
}

// webJSNetTransports holds one transport per guard, built on first use. One
// per guard rather than per page: keep-alive connections are what make a
// page's dozen API calls cheap on handheld Wi-Fi. A connection dialed under
// one guard is never handed to a request under another, because the idle pool
// belongs to the transport.
var webJSNetTransports [3]struct {
	once sync.Once
	tr   *http.Transport
}

// webJSNetTransport returns the guard's transport.
func webJSNetTransport(guard webJSNetGuard) *http.Transport {
	if guard < webJSNetGuardPublic || guard > webJSNetGuardLocal {
		guard = webJSNetGuardPublic
	}
	slot := &webJSNetTransports[guard]
	slot.once.Do(func() { slot.tr = webJSNetNewTransport(guard) })
	return slot.tr
}

// webJSNetNewTransport clones the default transport (its proxy, HTTP/2 and
// timeouts are what every other browser request uses) and puts the guard into
// its dialer. Every dial goes through Control on the resolved address, except
// the one dial the proxy hook vouched for: the user's own proxy, whose address
// may well be on the LAN, and whose target was judged by name instead.
func webJSNetNewTransport(guard webJSNetGuard) *http.Transport {
	var tr *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = base.Clone()
	} else {
		// Something replaced the default transport: start from net/http's
		// own defaults rather than go without the guard.
		tr = &http.Transport{
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
	}
	guarded := &net.Dialer{
		Timeout:   webJSNetDialTimeout,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			return webJSNetCheckDial(guard, address)
		},
	}
	direct := &net.Dialer{Timeout: webJSNetDialTimeout, KeepAlive: 30 * time.Second}
	tr.Proxy = webJSNetProxyFunc(guard)
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if note, ok := ctx.Value(webJSNetProxyKey{}).(*webJSNetProxyNote); ok && note.is(addr) {
			return direct.DialContext(ctx, network, addr)
		}
		return guarded.DialContext(ctx, network, addr)
	}
	// A TLS dialer of its own would bypass DialContext for https.
	tr.DialTLSContext = nil
	tr.ForceAttemptHTTP2 = true
	tr.TLSNextProto = nil
	tr.MaxIdleConnsPerHost = webJSNetMaxParallel
	tr.ResponseHeaderTimeout = webJSNetMaxWait
	tr.MaxResponseHeaderBytes = webJSNetMaxResponseHeader
	return tr
}

// webJSNetProxyKey is the context key the proxy note travels under. The
// transport dials with the request's context values, so the note reaches the
// dialer.
type webJSNetProxyKey struct{}

// webJSNetProxyNote is where the proxy hook tells the dialer which address is
// the user's proxy for the hop being sent ("" when the hop goes direct).
type webJSNetProxyNote struct {
	mu   sync.Mutex
	addr string
}

func (p *webJSNetProxyNote) set(addr string) {
	p.mu.Lock()
	p.addr = addr
	p.mu.Unlock()
}

func (p *webJSNetProxyNote) is(addr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.addr != "" && strings.EqualFold(p.addr, addr)
}

// webJSNetProxyFunc is the transport's Proxy hook. Whatever the proxy setting,
// the target's name is judged first (a literal LAN address or a localhost name
// is refused before any connection); then, when the environment names a proxy
// for the hop, the note tells the dialer to let that one connection through.
// The proxy resolves the target itself, so a proxied request is judged by its
// name alone.
func webJSNetProxyFunc(guard webJSNetGuard) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if req == nil || req.URL == nil || !webJSNetNameAllowed(guard, req.URL.Hostname()) {
			return nil, webJSNetErrPrivate
		}
		proxy, err := http.ProxyFromEnvironment(req)
		if note, ok := req.Context().Value(webJSNetProxyKey{}).(*webJSNetProxyNote); ok {
			addr := ""
			if err == nil && proxy != nil {
				addr = webJSNetProxyAddr(proxy)
			}
			note.set(addr)
		}
		return proxy, err
	}
}

// webJSNetProxyAddr is the host:port the transport dials for a proxy URL,
// with the scheme's default port filled in the way net/http does it.
func webJSNetProxyAddr(proxy *url.URL) string {
	port := proxy.Port()
	if port == "" {
		switch proxy.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(proxy.Hostname(), port)
}
