package main

// davshare.go — WebDAV share: the card's shared folders as a drive that
// Windows Explorer ("Map network drive"), macOS Finder ("Connect to Server")
// and Linux file managers (dav://) open without extra software. A
// Samba-compatible server that Windows 11 accepts needs
// NTLMv2 and signing, which JukaHub does not write blind, so this is WebDAV
// (RFC 4918) over plain HTTP, in the standard library only.
//
// What it serves and how it is guarded is FTP's (ftp_server.go,
// fileserver_guard.go):
//   - the same shares — the card's user folders and JukaHub's downloads,
//     never Apps, Emus or System — each an os.Root, resolved through
//     ftpShareSet.resolvePath, so neither ".." nor a symlink leaves one, and
//     the folders whose files run as programs (Roms/Ports, game profiles,
//     script systems) are read-only, as they are over FTP;
//   - the watchdog: off after 15 idle minutes or on another network, with
//     the connected clients on the screen;
//   - sign-in through an unguessable key as the first folder of the address
//     (http://IP:PORT/xxxx-xxxx-xxxx-xxxx/), because Windows refuses Basic
//     sign-in over plain HTTP unless the registry is changed; Basic with the
//     FTP account name and a password made at every start works too (macOS,
//     Linux). Both are new at every start. Wrong ones wait a second each, and
//     ten from one address lock it out for ten minutes.
//
// The methods: OPTIONS (DAV: 1, 2 and MS-Author-Via; the server root's is
// answered without sign-in, since Windows asks there first), PROPFIND
// (depth 0/1; allprop, propname, prop), PROPPATCH (Windows' Win32 times are
// applied; the rest is answered as stored, which Windows and Finder need),
// GET/HEAD (http.ServeContent: ranges, If-Modified-Since), PUT (a hidden
// temporary file renamed into place, at most the free space), DELETE, MKCOL,
// COPY/MOVE (Destination, Overwrite, Depth) and LOCK/UNLOCK (write locks with
// tokens and timeouts, davshare_lock.go: Explorer and Finder will not write
// without them). The XML is in davshare_props.go, the screen in
// davshare_ui.go.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// davDefaultPort is the first port tried; the next few follow when it is
	// taken.
	davDefaultPort = 8325
	davPortTries   = 6
	// davMaxConns bounds the open connections; Explorer opens several.
	davMaxConns = 16
	// davIdleTimeout ends a transfer that stalls this long.
	davIdleTimeout = 2 * time.Minute
	// davMaxXML bounds a request's XML body.
	davMaxXML = 256 << 10
	// davFreeMargin is kept free on the card beyond an upload.
	davFreeMargin = 16 << 20
	// davMiniRedir starts the User-Agent of Windows' WebDAV client.
	davMiniRedir = "Microsoft-WebDAV-MiniRedir"
	// davAllow is every method the drive answers.
	davAllow = "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, MKCOL, COPY, MOVE, LOCK, UNLOCK"
	// davProtectedMsg is the answer to a write into a folder of programs.
	davProtectedMsg = "This folder holds programs the device runs (ports, scripts, game profiles), so the WebDAV share cannot change it. Use SFTP."
	davFixedMsg     = "The shared folders themselves cannot be changed. Open one first."
)

// davAuthFailDelay is how long a wrong key or password waits for its answer.
// A variable so the tests can run without the wait.
var davAuthFailDelay = time.Second

// davService is the running drive. Use davSrv.
type davService struct {
	mu       sync.Mutex
	srv      *http.Server
	shares   *ftpShareSet
	port     int
	token    string
	password string
	digest   []byte
	lastErr  string
	gen      uint64
	netBase  fsNetIdentity
	clients  fsClients
	activity fsActivity
	limiter  ftpLoginLimiter
	conns    map[net.Conn]uint64
	locks    davLocks
}

var davSrv = &davService{}

func init() {
	bootOnShutdown(func() { davSrv.Stop() })
}

// davConnKey carries a request's connection in its context.
type davConnKey struct{}

// Running reports whether the drive is serving.
func (s *davService) Running() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

// Port is the listening port (the default while stopped).
func (s *davService) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port > 0 {
		return s.port
	}
	return davDefaultPort
}

// Token is this run's key ("" while stopped). Like the password it is shown
// on the device only: never published as a config variable, never logged.
func (s *davService) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// Password is this run's Basic sign-in password ("" while stopped).
func (s *davService) Password() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.password
}

// User is the account name for Basic sign-in: FTP's and SFTP's.
func (s *davService) User() string { return sshUserName }

// LastError is the most recent start error.
func (s *davService) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Clients lists the open connections.
func (s *davService) Clients() []fsClient { return s.clients.list() }

// Address is what a computer types: http://IP:PORT/KEY/ ("" while stopped or
// without a LAN address). The screen, the Settings row and the tiles ask every
// frame, so the address is the tools' cached one (toolsIPAddress: an interface
// walk at most every two seconds, which also follows a network change), as
// telnetserve.go's Address reads it.
func (s *davService) Address() string {
	token := s.Token()
	if token == "" {
		return ""
	}
	host := toolsIPAddress()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:%d/%s/", host, s.Port(), token)
}

// BaseAddress is the address without the key, for Basic sign-in (the cached
// address too).
func (s *davService) BaseAddress() string {
	if !s.Running() {
		return ""
	}
	host := toolsIPAddress()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:%d/", host, s.Port())
}

// watchTarget is the drive as the watchdog sees it.
func (s *davService) watchTarget() fsWatchTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fsWatchTarget{running: s.srv != nil, gen: s.gen, base: s.netBase, lastUsed: s.activity.lastUsed()}
}

// stopRun stops the drive if it is still run gen.
func (s *davService) stopRun(gen uint64) bool {
	s.mu.Lock()
	if s.srv == nil || s.gen != gen {
		s.mu.Unlock()
		return false
	}
	s.stopLocked()
	return true
}

// Start shares FTP's folders on every interface. The watchdog starts with it.
func (s *davService) Start() error {
	if s.Running() {
		return nil
	}
	base, card := ftpShareBase()
	shares := ftpSharesFor(base, card)
	if dl := downloadsRoot(); dirExists(dl) {
		shares = ftpAddShare(shares, ftpShare{name: ftpDownloadsShareName, dir: dl})
	}
	addrs := make([]string, 0, davPortTries)
	for i := 0; i < davPortTries; i++ {
		addrs = append(addrs, fmt.Sprintf("0.0.0.0:%d", davDefaultPort+i))
	}
	if err := s.startShares(shares, addrs); err != nil {
		if errors.Is(err, errFTPNothingToShare) && card {
			return fmt.Errorf("%w on the card (Roms, Saves, Music…)", err)
		}
		return err
	}
	if s == davSrv {
		fileServerWatchStart()
	}
	return nil
}

// startShares serves shares on the first of addrs that is free, with a new
// key and password. The tests call it with folders of their own.
func (s *davService) startShares(shares []ftpShare, addrs []string) error {
	if s.Running() {
		return nil
	}
	set, err := openFTPShares(shares)
	if err != nil {
		return err
	}
	if s == davSrv {
		set.live = &ftpLiveLocks{derive: set.runDirs}
	}
	token, err := generateSSHPassword()
	if err != nil {
		set.close()
		return fmt.Errorf("make the drive's key: %w", err)
	}
	password, err := generateSSHPassword()
	if err != nil {
		set.close()
		return fmt.Errorf("make the drive's password: %w", err)
	}
	var ln net.Listener
	for _, addr := range addrs {
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
	}
	if ln == nil {
		set.close()
		if err == nil {
			err = errors.New("no address to listen on")
		}
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return fmt.Errorf("start the WebDAV share: %w", err)
	}
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ConnState:         s.connState,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, davConnKey{}, c)
		},
	}
	s.limiter.reset()
	s.locks.reset()
	s.mu.Lock()
	s.srv = srv
	s.shares = set
	s.token = token
	s.password = password
	s.digest = sshPasswordDigest(password)
	s.lastErr = ""
	s.gen++
	s.netBase = fsNetIdentity{ip: localIPv4()}
	s.conns = make(map[net.Conn]uint64)
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tcp.Port
	}
	s.mu.Unlock()
	s.activity.touch()
	logInfo("davshare", "WebDAV share serving %s on %s", strings.Join(set.names(), ", "), ln.Addr())
	go func() {
		if err := srv.Serve(davLimitListener(ln, davMaxConns)); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logWarn("davshare", "WebDAV share stopped: %v", err)
		}
	}()
	return nil
}

// Stop closes the listener, every connection and the shares.
func (s *davService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopLocked()
}

// stopLocked is Stop for a caller holding s.mu, which it releases.
func (s *davService) stopLocked() {
	srv := s.srv
	set := s.shares
	s.srv = nil
	s.shares = nil
	s.token, s.password, s.digest = "", "", nil
	s.conns = make(map[net.Conn]uint64)
	s.clients.clear()
	s.mu.Unlock()
	s.locks.reset()
	if srv != nil {
		_ = srv.Close()
		logInfo("davshare", "WebDAV share stopped")
	}
	set.close()
}

// connState keeps the client list: a connection is a client.
func (s *davService) connState(c net.Conn, st http.ConnState) {
	switch st {
	case http.StateNew:
		id := s.clients.add(c.RemoteAddr().String())
		s.mu.Lock()
		if s.conns == nil {
			s.conns = make(map[net.Conn]uint64)
		}
		s.conns[c] = id
		s.mu.Unlock()
	case http.StateClosed, http.StateHijacked:
		s.mu.Lock()
		id, ok := s.conns[c]
		delete(s.conns, c)
		s.mu.Unlock()
		if ok {
			s.clients.remove(id)
		}
	}
}

// clientID is the client id of r's connection (0 when unknown).
func (s *davService) clientID(r *http.Request) uint64 {
	c, _ := r.Context().Value(davConnKey{}).(net.Conn)
	if c == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[c]
}

// ── Connection limit ─────────────────────────────────────────────────────

// davLimited is a listener that holds at most n connections open.
type davLimited struct {
	net.Listener
	sem chan struct{}
}

func davLimitListener(ln net.Listener, n int) net.Listener {
	return &davLimited{Listener: ln, sem: make(chan struct{}, n)}
}

func (l *davLimited) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &davLimitedConn{Conn: c, release: func() { <-l.sem }}, nil
}

type davLimitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *davLimitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// ── Requests ─────────────────────────────────────────────────────────────

// davReq is one signed-in request: its shares, the address prefix the
// client used ("/KEY" or ""), and its path below that prefix.
type davReq struct {
	s      *davService
	set    *ftpShareSet
	prefix string
	rel    string
	client uint64
}

// ServeHTTP signs a request in and runs its method.
func (s *davService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	set, token, digest := s.shares, s.token, s.digest
	s.mu.Unlock()
	if set == nil {
		http.Error(w, "The WebDAV share is off.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("MS-Author-Via", "DAV")
	// Windows asks the server's root what it speaks before the folder it
	// was given; that answer names no file, so it needs no sign-in. OPTIONS
	// anywhere else signs in like every other method.
	if r.Method == http.MethodOptions && r.URL.Path == "/" {
		davOptions(w)
		return
	}
	ip := fsClientHost(r.RemoteAddr)
	if locked, wait := s.limiter.locked(ip); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		http.Error(w, "Too many wrong sign-ins from your address. Try again later.", http.StatusTooManyRequests)
		return
	}
	req, ok := s.authorize(w, r, token, digest)
	if !ok {
		return
	}
	req.set = set
	req.client = s.clientID(r)
	switch r.Method {
	case http.MethodOptions:
		davOptions(w)
	case http.MethodGet, http.MethodHead:
		req.get(w, r)
	case "PROPFIND":
		// Opening a folder is use; a client's look at one item (depth 0)
		// is how it polls, and does not keep the drive on.
		if strings.TrimSpace(r.Header.Get("Depth")) != "0" {
			s.activity.touch()
		}
		req.propfind(w, r)
	case "PROPPATCH":
		req.proppatch(w, r)
	case http.MethodPut:
		s.activity.touch()
		req.put(w, r)
	case http.MethodDelete:
		s.activity.touch()
		req.delete(w, r)
	case "MKCOL":
		s.activity.touch()
		req.mkcol(w, r)
	case "COPY", "MOVE":
		s.activity.touch()
		req.copyMove(w, r)
	case "LOCK":
		req.lock(w, r)
	case "UNLOCK":
		req.unlock(w, r)
	default:
		w.Header().Set("Allow", davAllow)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// davOptions answers OPTIONS.
func davOptions(w http.ResponseWriter) {
	h := w.Header()
	h.Set("DAV", "1, 2")
	h.Set("Allow", davAllow)
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// davTokenShaped reports a first folder typed as a key (four groups of four
// letters and digits), so a wrong one counts as a wrong sign-in.
func davTokenShaped(seg string) bool {
	if len(seg) != 19 {
		return false
	}
	for i, c := range seg {
		if i%5 == 4 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// authorize signs r in by the key in its first folder or by Basic. It
// answers the request itself when it cannot.
func (s *davService) authorize(w http.ResponseWriter, r *http.Request, token string, digest []byte) (*davReq, bool) {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\r\n") {
		http.Error(w, "Bad path", http.StatusBadRequest)
		return nil, false
	}
	first, after, _ := strings.Cut(p[1:], "/")
	if token != "" && len(first) == len(token) &&
		subtle.ConstantTimeCompare([]byte(strings.ToLower(first)), []byte(token)) == 1 {
		s.clients.setUser(s.clientID(r), "key")
		return &davReq{s: s, prefix: "/" + first, rel: "/" + after}, true
	}
	user, pass, hasBasic := r.BasicAuth()
	if hasBasic && len(digest) > 0 && strings.EqualFold(strings.TrimSpace(user), sshUserName) &&
		subtle.ConstantTimeCompare(sshPasswordDigest(pass), digest) == 1 {
		s.limiter.succeed(fsClientHost(r.RemoteAddr))
		s.clients.setUser(s.clientID(r), sshUserName)
		return &davReq{s: s, prefix: "", rel: p}, true
	}
	// Windows looks at the server's root before the folder it was given,
	// without a sign-in it could send over plain HTTP: the root alone, with
	// nothing in it, is all it sees.
	if r.Method == "PROPFIND" && p == "/" && !hasBasic && strings.HasPrefix(r.UserAgent(), davMiniRedir) {
		davEmptyRoot(w)
		return nil, false
	}
	if (hasBasic && (user != "" || pass != "")) || davTokenShaped(first) {
		time.Sleep(davAuthFailDelay)
		if s.limiter.fail(fsClientHost(r.RemoteAddr)) {
			logInfo("davshare", "an address is locked out after wrong sign-ins")
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="JukaHub WebDAV share", charset="UTF-8"`)
	http.Error(w, "Sign in with the address or the password shown on the device.", http.StatusUnauthorized)
	return nil, false
}

// resolve maps the request's path onto the shares, answering the request
// when it cannot.
func (q *davReq) resolve(w http.ResponseWriter, rel string) (ftpPath, bool) {
	p, err := q.set.resolvePath("/", rel)
	if err != nil {
		if errors.Is(err, errFTPNotFound) {
			http.Error(w, "Not found", http.StatusNotFound)
		} else {
			http.Error(w, "That path is outside the shared folders.", http.StatusForbidden)
		}
		return ftpPath{}, false
	}
	return p, true
}

// writable answers the request and reports false when p may not be created,
// changed or removed.
func davWritable(w http.ResponseWriter, p ftpPath) bool {
	if p.isRoot() || p.isShareTop() {
		http.Error(w, davFixedMsg, http.StatusForbidden)
		return false
	}
	if p.protected || companionFilesAliasProtected(p) {
		http.Error(w, davProtectedMsg, http.StatusForbidden)
		return false
	}
	return true
}

// davHidden reports a name listings leave out: uploads still arriving.
func davHidden(name string) bool {
	return companionFilesIsPart(name)
}

// davLockKey is p's key in the lock table: the virtual path as the card's
// file systems compare names.
func davLockKey(virtual string) string {
	k := strings.ToLower(path.Clean("/" + virtual))
	parts := strings.Split(k, "/")
	for i, part := range parts {
		parts[i] = strings.TrimRight(part, ". ")
	}
	return strings.Join(parts, "/")
}

// href is the address of virtual path v as the client used it.
func (q *davReq) href(v string, dir bool) string {
	var b strings.Builder
	b.WriteString(q.prefix)
	for _, seg := range strings.Split(strings.Trim(v, "/"), "/") {
		if seg == "" {
			continue
		}
		b.WriteByte('/')
		b.WriteString(url.PathEscape(seg))
	}
	if dir || b.Len() == len(q.prefix) {
		b.WriteByte('/')
	}
	return b.String()
}

// ── GET / HEAD ───────────────────────────────────────────────────────────

func (q *davReq) get(w http.ResponseWriter, r *http.Request) {
	p, ok := q.resolve(w, q.rel)
	if !ok {
		return
	}
	if p.isRoot() {
		q.listHTML(w, r, p)
		return
	}
	f, err := p.share.root.Open(p.rel)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if info.IsDir() {
		q.listHTML(w, r, p)
		return
	}
	name := path.Base(p.virtual)
	w.Header().Set("ETag", davETag(info))
	w.Header().Set("Content-Type", davContentType(name))
	// A web page on the card opened in a browser runs no script with the
	// drive's address (and key) as its origin.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	what := "downloading " + p.display()
	if r.Method == http.MethodGet {
		q.s.clients.setTransfer(q.client, what)
		defer q.s.clients.clearTransfer(q.client, what)
	}
	http.ServeContent(davIdleWriter{ResponseWriter: w, rc: http.NewResponseController(w), act: &q.s.activity}, r, name, info.ModTime(), f)
}

// listHTML is a plain page of a folder, for a web browser.
func (q *davReq) listHTML(w http.ResponseWriter, r *http.Request, p ftpPath) {
	entries, err := q.children(p)
	if err != nil {
		http.Error(w, "This folder cannot be read.", http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	title := html.EscapeString(p.virtual)
	b.WriteString("<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width\"><title>JukaHub " + title + "</title><h1>" + title + "</h1><ul>")
	if !p.isRoot() {
		b.WriteString("<li><a href=\"" + html.EscapeString(q.href(path.Dir(p.virtual), true)) + "\">..</a>")
	}
	for _, e := range entries {
		label := e.name
		if e.dir {
			label += "/"
		}
		b.WriteString("<li><a href=\"" + html.EscapeString(q.href(path.Join(p.virtual, e.name), e.dir)) + "\">" + html.EscapeString(label) + "</a>")
	}
	b.WriteString("</ul>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(b.Len()))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.WriteString(w, b.String())
}

// davEntry is one child of a folder.
type davEntry struct {
	name string
	dir  bool
	info os.FileInfo
}

// children lists p's children: the shares for the root, the folder's files
// and folders otherwise. A link is followed inside its share; one that
// leaves it, or leads nowhere, is left out.
func (q *davReq) children(p ftpPath) ([]davEntry, error) {
	var out []davEntry
	if p.isRoot() {
		for _, sh := range q.set.list {
			info, err := sh.root.Stat(".")
			if err != nil {
				continue
			}
			out = append(out, davEntry{name: sh.name, dir: true, info: info})
		}
		return out, nil
	}
	f, err := p.share.root.Open(p.rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	list, err := f.ReadDir(-1)
	if err != nil && len(list) == 0 {
		return nil, err
	}
	for _, e := range list {
		name := e.Name()
		if davHidden(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if info, err = p.share.root.Stat(filepath.Join(p.rel, name)); err != nil {
				continue
			}
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		out = append(out, davEntry{name: name, dir: info.IsDir(), info: info})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].name) < strings.ToLower(out[j].name) })
	return out, nil
}

// davETag is a file's entity tag: its size and modification time.
func davETag(info os.FileInfo) string {
	return `"` + strconv.FormatInt(info.ModTime().UnixNano(), 16) + "-" + strconv.FormatInt(info.Size(), 16) + `"`
}

// davContentType is a file's type by its extension.
func davContentType(name string) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// davIdleWriter extends the connection's write deadline on every write, so
// davIdleTimeout bounds a stall rather than the whole download, and counts
// each write as use of the drive.
type davIdleWriter struct {
	http.ResponseWriter
	rc  *http.ResponseController
	act *fsActivity
}

func (w davIdleWriter) Write(p []byte) (int, error) {
	_ = w.rc.SetWriteDeadline(time.Now().Add(davIdleTimeout))
	w.act.touch()
	return w.ResponseWriter.Write(p)
}

// davIdleReader is davIdleWriter for an upload's body.
type davIdleReader struct {
	r   io.Reader
	rc  *http.ResponseController
	act *fsActivity
}

func (r davIdleReader) Read(p []byte) (int, error) {
	_ = r.rc.SetReadDeadline(time.Now().Add(davIdleTimeout))
	r.act.touch()
	return r.r.Read(p)
}

// ── PUT ──────────────────────────────────────────────────────────────────

func (q *davReq) put(w http.ResponseWriter, r *http.Request) {
	p, err := q.set.resolvePath("/", q.rel)
	if errors.Is(err, errFTPNotFound) && path.Dir(p.virtual) == "/" {
		http.Error(w, davFixedMsg, http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, "That path is outside the shared folders.", http.StatusForbidden)
		return
	}
	if !davWritable(w, p) || !q.lockOK(w, r, p.virtual, false) {
		return
	}
	root := p.share.root
	existed := false
	if info, err := root.Lstat(p.rel); err == nil {
		if info.IsDir() {
			http.Error(w, "A folder of that name is already here.", http.StatusMethodNotAllowed)
			return
		}
		existed = true
	}
	dir := filepath.Dir(p.rel)
	if info, err := root.Stat(dir); err != nil || !info.IsDir() {
		http.Error(w, "The folder does not exist.", http.StatusConflict)
		return
	}
	limit := int64(-1)
	if _, free, _, err := sdMoveStatfs(p.share.dir); err == nil {
		limit = free - davFreeMargin
		if limit < 0 {
			limit = 0
		}
	}
	expect := r.ContentLength
	if expect < 0 {
		// Finder streams a body without a length and says it here.
		if v, err := strconv.ParseInt(r.Header.Get("X-Expected-Entity-Length"), 10, 64); err == nil {
			expect = v
		}
	}
	if limit >= 0 && expect > limit {
		http.Error(w, "The card is full.", http.StatusInsufficientStorage)
		return
	}
	tmpRel, out, err := companionFilesCreateTemp(root, dir)
	if err != nil {
		logWarn("davshare", "cannot write in %s: %v", path.Dir(p.display()), err)
		http.Error(w, "Cannot write in this folder.", http.StatusInternalServerError)
		return
	}
	what := "uploading " + p.display()
	q.s.clients.setTransfer(q.client, what)
	defer q.s.clients.clearTransfer(q.client, what)
	var body io.Reader = davIdleReader{r: r.Body, rc: http.NewResponseController(w), act: &q.s.activity}
	if limit >= 0 {
		body = io.LimitReader(body, limit+1)
	}
	n, copyErr := io.Copy(out, body)
	closeErr := out.Close()
	if copyErr == nil && limit >= 0 && n > limit {
		copyErr = errors.New("no space left on the card")
	}
	if copyErr != nil || closeErr != nil {
		_ = root.Remove(tmpRel)
		if isDiskFullError(copyErr) || isDiskFullError(closeErr) {
			http.Error(w, "The card is full.", http.StatusInsufficientStorage)
			return
		}
		logWarn("davshare", "upload of %s failed: %v %v", path.Base(p.virtual), copyErr, closeErr)
		http.Error(w, "The upload was interrupted.", http.StatusInternalServerError)
		return
	}
	if err := root.Rename(tmpRel, p.rel); err != nil {
		if existed && (errors.Is(err, os.ErrExist) || runtime.GOOS == "windows") {
			err = companionFilesReplace(root, dir, tmpRel, p.rel)
		}
		if err != nil {
			_ = root.Remove(tmpRel)
			logWarn("davshare", "cannot save %s: %v", p.display(), err)
			http.Error(w, "Cannot save the file.", http.StatusInternalServerError)
			return
		}
	}
	if info, err := root.Stat(p.rel); err == nil {
		w.Header().Set("ETag", davETag(info))
	}
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// ── DELETE / MKCOL ───────────────────────────────────────────────────────

func (q *davReq) delete(w http.ResponseWriter, r *http.Request) {
	p, ok := q.resolve(w, q.rel)
	if !ok || !davWritable(w, p) {
		return
	}
	info, err := p.share.root.Lstat(p.rel)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if !q.lockOK(w, r, p.virtual, info.IsDir()) {
		return
	}
	if info.IsDir() {
		err = p.share.root.RemoveAll(p.rel)
	} else {
		err = p.share.root.Remove(p.rel)
	}
	if err != nil {
		logWarn("davshare", "cannot delete %s: %v", p.display(), err)
		http.Error(w, "Cannot delete it.", http.StatusInternalServerError)
		return
	}
	q.s.locks.removeUnder(davLockKey(p.virtual))
	w.WriteHeader(http.StatusNoContent)
}

func (q *davReq) mkcol(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > 0 {
		http.Error(w, "MKCOL takes no body", http.StatusUnsupportedMediaType)
		return
	}
	p, err := q.set.resolvePath("/", q.rel)
	if errors.Is(err, errFTPNotFound) && path.Dir(p.virtual) == "/" {
		http.Error(w, davFixedMsg, http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, "That path is outside the shared folders.", http.StatusForbidden)
		return
	}
	if p.isRoot() || p.isShareTop() {
		http.Error(w, "Already there", http.StatusMethodNotAllowed)
		return
	}
	if !davWritable(w, p) || !q.lockOK(w, r, p.virtual, false) {
		return
	}
	root := p.share.root
	if _, err := root.Lstat(p.rel); err == nil {
		http.Error(w, "Already there", http.StatusMethodNotAllowed)
		return
	}
	if info, err := root.Stat(filepath.Dir(p.rel)); err != nil || !info.IsDir() {
		http.Error(w, "The folder above does not exist.", http.StatusConflict)
		return
	}
	if err := root.Mkdir(p.rel, 0o755); err != nil {
		logWarn("davshare", "cannot make %s: %v", p.display(), err)
		http.Error(w, "Cannot make the folder.", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// ── COPY / MOVE ──────────────────────────────────────────────────────────

// destination reads the Destination header into a path below the request's
// prefix; it must name this server (the Host the request came to) and use
// the same key the request did. A Destination on another server is
// answered 502 (RFC 4918 §9.8.5), any other bad one 400.
func (q *davReq) destination(r *http.Request) (string, int) {
	raw := strings.TrimSpace(r.Header.Get("Destination"))
	if raw == "" {
		return "", http.StatusBadRequest
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", http.StatusBadRequest
	}
	if u.Scheme != "" && !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", http.StatusBadRequest
	}
	if u.Host != "" && !strings.EqualFold(u.Host, r.Host) {
		return "", http.StatusBadGateway
	}
	p := u.Path
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\r\n") {
		return "", http.StatusBadRequest
	}
	if q.prefix == "" {
		return p, 0
	}
	first, after, _ := strings.Cut(p[1:], "/")
	if !strings.EqualFold("/"+first, q.prefix) {
		return "", http.StatusBadRequest
	}
	return "/" + after, 0
}

func (q *davReq) copyMove(w http.ResponseWriter, r *http.Request) {
	move := r.Method == "MOVE"
	src, ok := q.resolve(w, q.rel)
	if !ok {
		return
	}
	if src.isRoot() || src.isShareTop() {
		http.Error(w, davFixedMsg, http.StatusForbidden)
		return
	}
	if move && !davWritable(w, src) {
		return
	}
	srcInfo, err := src.share.root.Lstat(src.rel)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	destRel, status := q.destination(r)
	if status != 0 {
		http.Error(w, "The Destination header is missing or on another drive.", status)
		return
	}
	dst, err := q.set.resolvePath("/", destRel)
	if err != nil {
		http.Error(w, "The destination is outside the shared folders.", http.StatusForbidden)
		return
	}
	if !davWritable(w, dst) {
		return
	}
	srcKey, dstKey := davLockKey(src.virtual), davLockKey(dst.virtual)
	sameItem := false
	dstInfo, dstErr := dst.share.root.Lstat(dst.rel)
	if dstErr == nil && dst.share == src.share && os.SameFile(srcInfo, dstInfo) {
		// A case-only rename on a card that ignores case finds itself.
		sameItem = true
		if src.virtual == dst.virtual || !move {
			http.Error(w, "The source and the destination are the same.", http.StatusForbidden)
			return
		}
	}
	if !sameItem && (dstKey == srcKey || strings.HasPrefix(dstKey, srcKey+"/")) {
		http.Error(w, "A folder cannot go inside itself.", http.StatusForbidden)
		return
	}
	if info, err := dst.share.root.Stat(filepath.Dir(dst.rel)); err != nil || !info.IsDir() {
		http.Error(w, "The destination folder does not exist.", http.StatusConflict)
		return
	}
	overwrite := !strings.EqualFold(strings.TrimSpace(r.Header.Get("Overwrite")), "F")
	existed := dstErr == nil && !sameItem
	if existed && !overwrite {
		http.Error(w, "The destination exists.", http.StatusPreconditionFailed)
		return
	}
	if move && !q.lockOK(w, r, src.virtual, srcInfo.IsDir()) {
		return
	}
	if !q.lockOK(w, r, dst.virtual, existed && dstInfo.IsDir()) {
		return
	}
	depth := strings.ToLower(strings.TrimSpace(r.Header.Get("Depth")))
	if move && srcInfo.IsDir() && depth != "" && depth != "infinity" {
		http.Error(w, "A folder moves with everything in it (Depth: infinity).", http.StatusBadRequest)
		return
	}
	if existed {
		var err error
		if dstInfo.IsDir() {
			err = dst.share.root.RemoveAll(dst.rel)
		} else {
			err = dst.share.root.Remove(dst.rel)
		}
		if err != nil {
			http.Error(w, "The destination could not be replaced.", http.StatusInternalServerError)
			return
		}
		q.s.locks.removeUnder(dstKey)
	}
	what := "copying " + src.display()
	q.s.clients.setTransfer(q.client, what)
	defer q.s.clients.clearTransfer(q.client, what)
	switch {
	case move && src.share == dst.share:
		err = src.share.root.Rename(src.rel, dst.rel)
	default:
		err = davCopyTree(r.Context(), src.share.root, src.rel, dst.share.root, dst.rel, !move && depth == "0", &q.s.activity)
		if err == nil && move {
			err = src.share.root.RemoveAll(src.rel)
		}
	}
	if err != nil {
		logWarn("davshare", "%s %s failed: %v", strings.ToLower(r.Method), src.display(), err)
		if isDiskFullError(err) {
			http.Error(w, "The card is full.", http.StatusInsufficientStorage)
			return
		}
		verb := "copied"
		if move {
			verb = "moved"
		}
		http.Error(w, "It could not be "+verb+".", http.StatusInternalServerError)
		return
	}
	if move {
		q.s.locks.removeUnder(srcKey)
	}
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// davCopyTree copies src (a file or a folder) to dst, which must not exist.
// shallow copies a folder without what is in it. Links are left out.
func davCopyTree(ctx context.Context, from *os.Root, src string, to *os.Root, dst string, shallow bool, act *fsActivity) error {
	info, err := from.Lstat(src)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch {
	case info.IsDir():
		if err := to.Mkdir(dst, 0o755); err != nil {
			return err
		}
		if shallow {
			return nil
		}
		f, err := from.Open(src)
		if err != nil {
			return err
		}
		names, err := f.Readdirnames(-1)
		f.Close()
		if err != nil {
			return err
		}
		for _, name := range names {
			if davHidden(name) {
				continue
			}
			if err := davCopyTree(ctx, from, filepath.Join(src, name), to, filepath.Join(dst, name), false, act); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		in, err := from.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := to.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, davActReader{r: in, act: act, ctx: ctx})
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			_ = to.Remove(dst)
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		}
		_ = to.Chtimes(dst, time.Time{}, info.ModTime())
		return nil
	}
	return nil
}

// davActReader counts a copy's reads as use and stops with ctx.
type davActReader struct {
	r   io.Reader
	act *fsActivity
	ctx context.Context
}

func (r davActReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	r.act.touch()
	return r.r.Read(p)
}
