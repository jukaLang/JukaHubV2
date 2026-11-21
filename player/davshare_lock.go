package main

// davshare_lock.go — the network drive's write locks (RFC 4918 §6–7, §9.10).
//
// Windows Explorer locks a file before it writes it and macOS Finder mounts a
// drive read-only unless the server offers locks (DAV: 2), so the drive keeps
// a small lock table: exclusive or shared write locks, on one item or a whole
// folder (Depth: infinity), each with an opaquelocktoken and a timeout of at
// most an hour that a LOCK with the token refreshes. A write to a locked item
// must carry its token in the If header, or it is answered 423 Locked; the If
// header's other conditions (entity tags, Not) are not evaluated. The table
// lives in memory and is emptied when the drive starts or stops. Keys are
// virtual paths folded the way the card's FAT and exFAT compare names.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// davLockMax is the longest a lock lasts without a refresh.
	davLockMax = time.Hour
	davLockMin = 10 * time.Second
	// davLockLimit bounds the table.
	davLockLimit = 1024
)

// davLock is one lock.
type davLock struct {
	token    string // "opaquelocktoken:…"
	key      string // davLockKey of the locked item
	root     string // its href, as the client wrote it
	depthInf bool
	shared   bool
	owner    string // the owner's href or text, plain
	timeout  time.Duration
	expires  time.Time // monotonic
}

// davLocks is the table.
type davLocks struct {
	mu      sync.Mutex
	byToken map[string]*davLock
}

func (t *davLocks) reset() {
	t.mu.Lock()
	t.byToken = nil
	t.mu.Unlock()
}

// prune drops expired locks. Called with t.mu held.
func (t *davLocks) prune(now time.Time) {
	for tok, l := range t.byToken {
		if !now.Before(l.expires) {
			delete(t.byToken, tok)
		}
	}
}

// davKeyUnder reports whether key is base or inside it.
func davKeyUnder(key, base string) bool {
	return key == base || base == "/" || strings.HasPrefix(key, base+"/")
}

// covering lists the locks that hold key: on it, on a folder above it with
// Depth infinity, and — with children — on anything inside it.
func (t *davLocks) covering(key string, children bool) []davLock {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(time.Now())
	var out []davLock
	for _, l := range t.byToken {
		if l.key == key || (l.depthInf && davKeyUnder(key, l.key)) || (children && davKeyUnder(l.key, key)) {
			out = append(out, *l)
		}
	}
	return out
}

// add makes a lock unless one conflicts; it returns the conflict instead.
func (t *davLocks) add(l davLock) (davLock, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(time.Now())
	for _, have := range t.byToken {
		overlap := have.key == l.key || (have.depthInf && davKeyUnder(l.key, have.key)) ||
			(l.depthInf && davKeyUnder(have.key, l.key))
		if overlap && !(have.shared && l.shared) {
			return *have, false
		}
	}
	if len(t.byToken) >= davLockLimit {
		return davLock{}, false
	}
	if t.byToken == nil {
		t.byToken = make(map[string]*davLock)
	}
	l.expires = time.Now().Add(l.timeout)
	t.byToken[l.token] = &l
	return l, true
}

// refresh renews the lock token that holds key.
func (t *davLocks) refresh(token, key string, timeout time.Duration) (davLock, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(time.Now())
	l := t.byToken[token]
	if l == nil || !(l.key == key || (l.depthInf && davKeyUnder(key, l.key))) {
		return davLock{}, false
	}
	l.timeout = timeout
	l.expires = time.Now().Add(timeout)
	return *l, true
}

// remove drops token when it holds key.
func (t *davLocks) remove(token, key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.byToken[token]
	if l == nil || !(l.key == key || (l.depthInf && davKeyUnder(key, l.key))) {
		return false
	}
	delete(t.byToken, token)
	return true
}

// removeUnder drops the locks on key and inside it (it was deleted or moved).
func (t *davLocks) removeUnder(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for tok, l := range t.byToken {
		if davKeyUnder(l.key, key) {
			delete(t.byToken, tok)
		}
	}
}

// davIfTokens are the state tokens in an If header: the <…> inside its
// parenthesised lists (a <…> outside them names a resource).
func davIfTokens(h string) map[string]bool {
	out := map[string]bool{}
	depth := 0
	for i := 0; i < len(h); i++ {
		switch h[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case '<':
			end := strings.IndexByte(h[i:], '>')
			if end < 0 {
				return out
			}
			if depth > 0 {
				out[strings.TrimSpace(h[i+1:i+end])] = true
			}
			i += end
		case '[':
			// An entity tag may hold any character but ']'.
			end := strings.IndexByte(h[i:], ']')
			if end < 0 {
				return out
			}
			i += end
		}
	}
	return out
}

// lockOK answers 423 and reports false when the item at virtual (and, with
// children, anything inside it) is locked by a token the request does not
// carry.
func (q *davReq) lockOK(w http.ResponseWriter, r *http.Request, virtual string, children bool) bool {
	held := q.s.locks.covering(davLockKey(virtual), children)
	if len(held) == 0 {
		return true
	}
	tokens := davIfTokens(r.Header.Get("If"))
	for _, l := range held {
		if !tokens[l.token] {
			http.Error(w, "Locked", http.StatusLocked)
			return false
		}
	}
	return true
}

// davNewLockToken is a random opaquelocktoken (a version 4 UUID).
func davNewLockToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return "opaquelocktoken:" + h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// davParseTimeout reads a Timeout header ("Second-3600, Infinite").
func davParseTimeout(h string) time.Duration {
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if strings.EqualFold(part, "Infinite") {
			return davLockMax
		}
		if len(part) > 7 && strings.EqualFold(part[:7], "Second-") {
			if n, err := strconv.ParseInt(part[7:], 10, 64); err == nil && n > 0 {
				d := time.Duration(n) * time.Second
				if n > int64(davLockMax/time.Second) {
					d = davLockMax
				}
				if d < davLockMin {
					d = davLockMin
				}
				return d
			}
		}
	}
	return davLockMax
}

// davLockInfo is a LOCK request's body.
type davLockInfo struct {
	XMLName   xml.Name  `xml:"DAV: lockinfo"`
	Exclusive *struct{} `xml:"DAV: lockscope>exclusive"`
	Shared    *struct{} `xml:"DAV: lockscope>shared"`
	Owner     *struct {
		Href string `xml:"DAV: href"`
		Text string `xml:",chardata"`
	} `xml:"DAV: owner"`
}

// lock answers LOCK: a new lock (creating an empty file at an address that
// has none) or, with no body, a refresh of the If header's lock.
func (q *davReq) lock(w http.ResponseWriter, r *http.Request) {
	p, err := q.set.resolvePath("/", q.rel)
	if err != nil {
		http.Error(w, "That path is outside the shared folders.", http.StatusForbidden)
		return
	}
	if !davWritable(w, p) {
		return
	}
	key := davLockKey(p.virtual)
	timeout := davParseTimeout(r.Header.Get("Timeout"))
	body, err := io.ReadAll(io.LimitReader(r.Body, davMaxXML+1))
	if err != nil || len(body) > davMaxXML {
		http.Error(w, "The request is too large.", http.StatusRequestEntityTooLarge)
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		for tok := range davIfTokens(r.Header.Get("If")) {
			if l, ok := q.s.locks.refresh(tok, key, timeout); ok {
				davWriteLock(w, http.StatusOK, l, false)
				return
			}
		}
		http.Error(w, "No lock to refresh.", http.StatusPreconditionFailed)
		return
	}
	var info davLockInfo
	if err := xml.Unmarshal(body, &info); err != nil {
		http.Error(w, "The lock request could not be read.", http.StatusBadRequest)
		return
	}
	root := p.share.root
	st, statErr := root.Lstat(p.rel)
	depthInf := !strings.EqualFold(strings.TrimSpace(r.Header.Get("Depth")), "0")
	if statErr == nil && !st.IsDir() {
		depthInf = false
	}
	owner := ""
	if info.Owner != nil {
		owner = strings.TrimSpace(info.Owner.Href)
		if owner == "" {
			owner = strings.TrimSpace(info.Owner.Text)
		}
		owner = devCheckClip(owner, 200)
	}
	token, err := davNewLockToken()
	if err != nil {
		http.Error(w, "No lock token.", http.StatusInternalServerError)
		return
	}
	dir := statErr == nil && st.IsDir()
	l, ok := q.s.locks.add(davLock{
		token: token, key: key, root: q.href(p.virtual, dir), depthInf: depthInf,
		shared: info.Shared != nil && info.Exclusive == nil, owner: owner, timeout: timeout,
	})
	if !ok {
		http.Error(w, "Locked", http.StatusLocked)
		return
	}
	status := http.StatusOK
	if statErr != nil {
		// A lock on an address with nothing there makes an empty file
		// (RFC 4918 §9.10.4): Explorer locks a new file before it writes it.
		if info, err := root.Stat(filepath.Dir(p.rel)); err != nil || !info.IsDir() {
			q.s.locks.remove(token, key)
			http.Error(w, "The folder does not exist.", http.StatusConflict)
			return
		}
		f, err := root.OpenFile(p.rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			q.s.locks.remove(token, key)
			http.Error(w, "Cannot make the file.", http.StatusInternalServerError)
			return
		}
		_ = f.Close()
		status = http.StatusCreated
	}
	davWriteLock(w, status, l, true)
}

// davWriteLock answers a LOCK with the lock's discovery body.
func davWriteLock(w http.ResponseWriter, status int, l davLock, header bool) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:prop xmlns:D="DAV:"><D:lockdiscovery>`)
	davActiveLockXML(&b, l)
	b.WriteString(`</D:lockdiscovery></D:prop>`)
	h := w.Header()
	if header {
		h.Set("Lock-Token", "<"+l.token+">")
	}
	h.Set("Content-Type", `application/xml; charset="utf-8"`)
	h.Set("Content-Length", strconv.Itoa(b.Len()))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, b.String())
}

// davActiveLockXML writes one D:activelock.
func davActiveLockXML(b *strings.Builder, l davLock) {
	scope := "exclusive"
	if l.shared {
		scope = "shared"
	}
	depth := "0"
	if l.depthInf {
		depth = "infinity"
	}
	b.WriteString(`<D:activelock><D:locktype><D:write/></D:locktype><D:lockscope><D:` + scope + `/></D:lockscope><D:depth>` + depth + `</D:depth>`)
	if l.owner != "" {
		b.WriteString(`<D:owner>`)
		davEscape(b, l.owner)
		b.WriteString(`</D:owner>`)
	}
	b.WriteString(`<D:timeout>Second-` + strconv.FormatInt(int64(l.timeout/time.Second), 10) + `</D:timeout>`)
	b.WriteString(`<D:locktoken><D:href>`)
	davEscape(b, l.token)
	b.WriteString(`</D:href></D:locktoken><D:lockroot><D:href>`)
	davEscape(b, l.root)
	b.WriteString(`</D:href></D:lockroot></D:activelock>`)
}

// unlock answers UNLOCK.
func (q *davReq) unlock(w http.ResponseWriter, r *http.Request) {
	p, err := q.set.resolvePath("/", q.rel)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	tok := strings.TrimSpace(r.Header.Get("Lock-Token"))
	tok = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(tok, "<"), ">"))
	if tok == "" || !q.s.locks.remove(tok, davLockKey(p.virtual)) {
		http.Error(w, "That lock is not held here.", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
