package main

// mail_imap.go — the small IMAP client behind the Mail app
// (docs/EMAIL_CALENDAR.md).
//
// Only what a handheld inbox needs, written against the standard library: the
// vendor tree cannot take a mail module, and the protocol subset is small.
//
//   - TLS from the first byte (993), or STARTTLS on 143. A server on 143 that
//     does not offer STARTTLS is refused: a password never crosses the
//     network in the clear. Certificates are always checked, against the
//     system roots plus the firmware fallback (bugrepRootCAs).
//   - Sign-in: AUTHENTICATE PLAIN when offered, else LOGIN (quoted, or as a
//     literal for a password with characters a quoted string cannot hold);
//     AUTHENTICATE XOAUTH2 for a Microsoft sign-in.
//   - SELECT/EXAMINE, SEARCH, FETCH, STORE, MOVE (or COPY + \Deleted +
//     EXPUNGE), LIST with the special-use flags, APPEND.
//
// Every read is bounded: a line between literals, one literal, one whole
// response, and the number of untagged answers a command may collect. Every
// command runs under a deadline, and the caller's context cuts the
// connection by pulling the deadline into the past. Nothing here logs a
// command, a password, a token or a message.

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// imapLineLimit is one response line between literals. A SEARCH answer
	// is a single line: UID SEARCH UNSEEN in an inbox with 100,000 unread
	// messages is about 700 KB of numbers.
	imapLineLimit     = 4 << 20
	imapLiteralLimit  = 4 << 20 // one literal (a body is fetched in a 2 MB window)
	imapResponseLimit = 8 << 20 // one whole response
	imapMaxResponses  = 5000    // untagged answers one command keeps
	imapMaxDepth      = 32      // nested parentheses
	imapDialTimeout   = 20 * time.Second
	imapCmdTimeout    = 40 * time.Second
)

// ── Errors ──────────────────────────────────────────────────────────────

// mailErrKind sorts mail failures into what the person should do about them.
type mailErrKind int

const (
	mailErrServer   mailErrKind = iota // the server said no to something else
	mailErrAuth                        // wrong password, app password needed, expired sign-in
	mailErrNet                         // offline, timeout, unknown host
	mailErrTLS                         // a certificate that does not check out
	mailErrInsecure                    // no STARTTLS: refused before any password went out
)

// mailError is a failure with a sentence a person can read. detail is the
// server's own words, kept for the log only when it carries no secret.
type mailError struct {
	kind   mailErrKind
	msg    string
	detail string
	err    error
}

func (e *mailError) Error() string { return e.msg }
func (e *mailError) Unwrap() error { return e.err }

// mailNetErr wraps a transport error. A context's end is passed through
// unchanged so callers can tell a cancel from a failure.
func mailNetErr(err error) error {
	if err == nil {
		return nil
	}
	var me *mailError
	if errors.As(err, &me) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	msg := err.Error()
	var ne net.Error
	switch {
	case strings.Contains(msg, "x509:") || strings.Contains(msg, "certificate"):
		if strings.Contains(msg, "expired or is not yet valid") {
			return &mailError{kind: mailErrTLS, msg: "this device's clock looks wrong — connect to Wi-Fi so it can set the time", err: err}
		}
		return &mailError{kind: mailErrTLS, msg: "the mail server's certificate could not be verified", err: err}
	case errors.As(err, &ne) && ne.Timeout():
		return &mailError{kind: mailErrNet, msg: "the mail server did not answer in time", err: err}
	case strings.Contains(msg, "no such host"):
		return &mailError{kind: mailErrNet, msg: "the mail server could not be found (offline, or the server name is wrong)", err: err}
	case strings.Contains(msg, "network is unreachable"), strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "connection reset"), strings.Contains(msg, "broken pipe"), errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF):
		return &mailError{kind: mailErrNet, msg: "the connection to the mail server dropped", err: err}
	}
	return &mailError{kind: mailErrNet, msg: firstLine(msg), err: err}
}

var errImapSyntax = errors.New("the mail server sent an answer this version cannot read")

// ── Parsed responses ────────────────────────────────────────────────────

// Kinds of imapValue.
const (
	imapAtom   byte = 'a'
	imapString byte = 's' // quoted string or literal
	imapNil    byte = 'n'
	imapList   byte = 'l'
)

// imapValue is one parsed item: an atom, a string, NIL or a list.
type imapValue struct {
	kind byte
	s    string
	list []imapValue
}

// str is the value as text: a string's or an atom's characters, "" for NIL
// and for a list.
func (v imapValue) str() string {
	if v.kind == imapAtom || v.kind == imapString {
		return v.s
	}
	return ""
}

// num is an atom read as a 32-bit number (0 when it is not one).
func (v imapValue) num() uint32 {
	n, err := strconv.ParseUint(v.s, 10, 32)
	if v.kind != imapAtom || err != nil {
		return 0
	}
	return uint32(n)
}

// imapResp is one server response.
type imapResp struct {
	tag    string // "*", "+" or the tag of the command it completes
	status string // OK, NO, BAD, BYE or PREAUTH for a status response
	code   string // a status response's [CODE …], without the brackets
	text   string // a status response's or a continuation's text
	num    uint32 // the number before EXISTS, EXPUNGE or FETCH
	kind   string // a data response's name, upper case: FETCH, LIST, SEARCH …
	fields []imapValue
}

// imapCut splits s at its first space.
func imapCut(s string) (string, string) {
	s = strings.TrimLeft(s, " ")
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// imapStatusText splits a status response's remainder into its bracketed
// code and its text. The text is free prose and is never tokenized: servers
// put unbalanced quotes and parentheses in it.
func imapStatusText(s string) (string, string) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		if end := strings.IndexByte(s, ']'); end > 0 {
			return s[1:end], strings.TrimSpace(s[end+1:])
		}
	}
	return "", s
}

// imapParseResp parses one response read as text parts with the literals
// that followed them (parts[i] was followed by lits[i]). Pure: outside data,
// covered by mail_parse_test.go.
func imapParseResp(parts []string, lits [][]byte) (*imapResp, error) {
	if len(parts) == 0 {
		return nil, errImapSyntax
	}
	tag, rest := imapCut(parts[0])
	r := &imapResp{tag: tag}
	switch tag {
	case "":
		return nil, errImapSyntax
	case "+":
		r.text = strings.TrimSpace(rest)
		return r, nil
	case "*":
		word, after := imapCut(rest)
		if n, err := strconv.ParseUint(word, 10, 32); err == nil {
			r.num = uint32(n)
			kind, more := imapCut(after)
			r.kind = strings.ToUpper(kind)
			after = more
		} else {
			upper := strings.ToUpper(word)
			switch upper {
			case "OK", "NO", "BAD", "BYE", "PREAUTH":
				r.status = upper
				r.code, r.text = imapStatusText(after)
				return r, nil
			}
			r.kind = upper
		}
		parts = append([]string{after}, parts[1:]...)
		fields, err := imapParseValues(parts, lits)
		if err != nil {
			return nil, err
		}
		r.fields = fields
		return r, nil
	}
	word, after := imapCut(rest)
	r.status = strings.ToUpper(word)
	r.code, r.text = imapStatusText(after)
	return r, nil
}

// imapTok is one lexical token.
type imapTok struct {
	kind byte // '(' ')' or an imapValue kind
	s    string
}

// imapParseValues tokenizes the parts (each followed by its literal) and
// builds the nested lists.
func imapParseValues(parts []string, lits [][]byte) ([]imapValue, error) {
	var toks []imapTok
	for i, p := range parts {
		var err error
		if toks, err = imapLex(p, toks); err != nil {
			return nil, err
		}
		if i < len(lits) {
			toks = append(toks, imapTok{kind: imapString, s: string(lits[i])})
		}
	}
	stack := [][]imapValue{nil}
	for _, t := range toks {
		switch t.kind {
		case '(':
			if len(stack) > imapMaxDepth {
				return nil, errImapSyntax
			}
			stack = append(stack, []imapValue{})
		case ')':
			if len(stack) == 1 {
				return nil, errImapSyntax
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			stack[len(stack)-1] = append(stack[len(stack)-1], imapValue{kind: imapList, list: top})
		default:
			stack[len(stack)-1] = append(stack[len(stack)-1], imapValue{kind: t.kind, s: t.s})
		}
	}
	if len(stack) != 1 {
		return nil, errImapSyntax
	}
	return stack[0], nil
}

// imapLex appends the tokens of one text part. An atom that opens a bracket
// runs to the matching close, spaces and parentheses included, so a fetch
// item such as BODY[HEADER.FIELDS (FROM SUBJECT)]<0> stays one atom.
func imapLex(s string, toks []imapTok) ([]imapTok, error) {
	for i := 0; i < len(s); {
		ch := s[i]
		switch {
		case ch == ' ' || ch == '\t':
			i++
		case ch == '(' || ch == ')':
			toks = append(toks, imapTok{kind: ch})
			i++
		case ch == '"':
			var b strings.Builder
			j := i + 1
			closed := false
			for j < len(s) {
				c := s[j]
				if c == '\\' && j+1 < len(s) {
					b.WriteByte(s[j+1])
					j += 2
					continue
				}
				if c == '"' {
					closed = true
					j++
					break
				}
				b.WriteByte(c)
				j++
			}
			if !closed {
				return toks, errImapSyntax
			}
			toks = append(toks, imapTok{kind: imapString, s: b.String()})
			i = j
		default:
			j := i
			depth := 0
			for j < len(s) {
				c := s[j]
				if c == '[' {
					depth++
				} else if c == ']' && depth > 0 {
					depth--
				} else if depth == 0 && (c == ' ' || c == '(' || c == ')') {
					break
				}
				j++
			}
			word := s[i:j]
			if strings.EqualFold(word, "NIL") {
				toks = append(toks, imapTok{kind: imapNil})
			} else {
				toks = append(toks, imapTok{kind: imapAtom, s: word})
			}
			i = j
		}
	}
	return toks, nil
}

// imapLiteralSuffix reports the size of the literal a line announces at its
// end ("… {123}"), and where the announcement starts.
func imapLiteralSuffix(line string) (int, int, bool) {
	if !strings.HasSuffix(line, "}") {
		return 0, 0, false
	}
	open := strings.LastIndexByte(line, '{')
	if open < 0 {
		return 0, 0, false
	}
	digits := strings.TrimSuffix(line[open+1:len(line)-1], "+")
	if digits == "" || len(digits) > 10 {
		return 0, 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 0 {
		return 0, 0, false
	}
	start := open
	if start > 0 && line[start-1] == '~' {
		start-- // literal8 (BINARY): the same bytes for our purposes
	}
	return n, start, true
}

// imapFetchItems maps a FETCH response's items by name. Section names are
// shortened: HEADER for BODY[HEADER…], TEXT for BODY[TEXT]…, BODY for BODY[].
func imapFetchItems(r *imapResp) map[string]imapValue {
	out := map[string]imapValue{}
	if r == nil || r.kind != "FETCH" || len(r.fields) == 0 || r.fields[0].kind != imapList {
		return out
	}
	l := r.fields[0].list
	for i := 0; i+1 < len(l); i += 2 {
		key := strings.ToUpper(l[i].s)
		switch {
		case strings.HasPrefix(key, "BODY[HEADER"), strings.HasPrefix(key, "RFC822.HEADER"):
			key = "HEADER"
		case strings.HasPrefix(key, "BODY[TEXT]"):
			key = "TEXT"
		case strings.HasPrefix(key, "BODY[]"), key == "RFC822":
			key = "BODY"
		}
		out[key] = l[i+1]
	}
	return out
}

// imapFlags lists a FLAGS value's flags, upper case.
func imapFlags(v imapValue) []string {
	var out []string
	for _, f := range v.list {
		if s := f.str(); s != "" {
			out = append(out, strings.ToUpper(s))
		}
	}
	return out
}

// imapHasFlag reports whether flags (upper case) holds flag.
func imapHasFlag(flags []string, flag string) bool {
	flag = strings.ToUpper(flag)
	for _, f := range flags {
		if f == flag {
			return true
		}
	}
	return false
}

// imapQuote writes s as a quoted string; ok is false when s needs a literal
// (a line break, a NUL or a byte past ASCII).
func imapQuote(s string) (string, bool) {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0 || c == '\r' || c == '\n' || c >= 0x80 {
			return "", false
		}
		if c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	b.WriteByte('"')
	return b.String(), true
}

// imapUIDSet writes UIDs as a compact sequence set ("4,7:9").
func imapUIDSet(uids []uint32) string {
	if len(uids) == 0 {
		return ""
	}
	s := append([]uint32(nil), uids...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var b strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j+1 < len(s) && (s[j+1] == s[j]+1 || s[j+1] == s[j]) {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatUint(uint64(s[i]), 10))
		if s[j] != s[i] {
			b.WriteByte(':')
			b.WriteString(strconv.FormatUint(uint64(s[j]), 10))
		}
		i = j + 1
	}
	return b.String()
}

// ── Connection ──────────────────────────────────────────────────────────

// imapConn is one signed-in (or signing-in) IMAP session. Not safe for
// concurrent use: each sync or action opens its own.
type imapConn struct {
	conn net.Conn
	r    *bufio.Reader
	host string
	ctx  context.Context
	stop func() bool
	tag  int
	caps map[string]bool
}

// mailTLSConfig checks the server's certificate for host against the system
// roots and the firmware fallback.
// mailTestRoots, when set, is the trust store the mail clients use. Only a
// test sets it, to trust the certificate of a loopback server.
var mailTestRoots func() *x509.CertPool

func mailTLSConfig(host string) *tls.Config {
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if mailTestRoots != nil {
		cfg.RootCAs = mailTestRoots()
		return cfg
	}
	if pool := bugrepRootCAs(); pool != nil {
		cfg.RootCAs = pool
	}
	return cfg
}

// imapDial connects, reads the greeting and, for STARTTLS, upgrades before
// anything else is said. The context bounds the whole session.
func imapDial(ctx context.Context, host string, port int, security string) (*imapConn, error) {
	host = strings.TrimSpace(host)
	if host == "" || port <= 0 || port > 65535 {
		return nil, &mailError{kind: mailErrServer, msg: "the incoming mail (IMAP) server is not set"}
	}
	d := &net.Dialer{Timeout: imapDialTimeout, KeepAlive: 30 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, mailNetErr(err)
	}
	c := &imapConn{conn: raw, host: host, ctx: ctx}
	// Pulling the deadline into the past ends any read or write at once.
	c.stop = context.AfterFunc(ctx, func() { _ = raw.SetDeadline(time.Unix(1, 0)) })
	_ = raw.SetDeadline(time.Now().Add(imapDialTimeout + 10*time.Second))
	starttls := security == mailcalStartTLS
	if !starttls {
		// TLS is the default for anything not explicitly STARTTLS.
		if err := c.handshake(); err != nil {
			c.close()
			return nil, err
		}
	} else {
		c.r = bufio.NewReaderSize(raw, 16<<10)
	}
	greet, err := c.readResp()
	if err != nil {
		c.close()
		return nil, err
	}
	if greet.tag != "*" || (greet.status != "OK" && greet.status != "PREAUTH") {
		c.close()
		return nil, &mailError{kind: mailErrServer, msg: "the mail server turned the connection away: " + mailClean(greet.text, 120)}
	}
	c.capsFromCode(greet.code)
	if starttls {
		if err := c.loadCaps(); err != nil {
			c.close()
			return nil, err
		}
		if !c.caps["STARTTLS"] {
			c.close()
			return nil, &mailError{kind: mailErrInsecure, msg: "the server on port " + strconv.Itoa(port) +
				" does not offer encryption (STARTTLS), so JukaHub will not send your password to it — use port 993 with TLS"}
		}
		if _, err := c.run(imapCmdTimeout, "STARTTLS"); err != nil {
			c.close()
			return nil, err
		}
		if c.r.Buffered() > 0 {
			// Bytes that arrived before the handshake were not encrypted and
			// could have been injected (CVE-2011-0411 style): refuse.
			c.close()
			return nil, &mailError{kind: mailErrInsecure, msg: "the mail server sent unexpected data before encryption started"}
		}
		if err := c.handshake(); err != nil {
			c.close()
			return nil, err
		}
		// What was said before encryption is forgotten (RFC 3501 §6.2.1).
		c.caps = nil
	}
	if c.caps == nil {
		if err := c.loadCaps(); err != nil {
			c.close()
			return nil, err
		}
	}
	return c, nil
}

// handshake wraps the connection in TLS.
func (c *imapConn) handshake() error {
	tc := tls.Client(c.conn, mailTLSConfig(c.host))
	if err := tc.HandshakeContext(c.ctx); err != nil {
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		return mailNetErr(err)
	}
	c.conn = tc
	c.r = bufio.NewReaderSize(tc, 16<<10)
	return nil
}

// close drops the connection without a goodbye.
func (c *imapConn) close() {
	if c == nil {
		return
	}
	if c.stop != nil {
		c.stop()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// logout says goodbye and closes.
func (c *imapConn) logout() {
	if c == nil {
		return
	}
	_, _ = c.run(5*time.Second, "LOGOUT")
	c.close()
}

// capsFromCode reads a [CAPABILITY …] response code.
func (c *imapConn) capsFromCode(code string) {
	word, rest := imapCut(code)
	if !strings.EqualFold(word, "CAPABILITY") {
		return
	}
	c.caps = map[string]bool{}
	for _, f := range strings.Fields(rest) {
		c.caps[strings.ToUpper(f)] = true
	}
}

// loadCaps asks for the capabilities.
func (c *imapConn) loadCaps() error {
	resps, err := c.run(imapCmdTimeout, "CAPABILITY")
	if err != nil {
		return err
	}
	c.caps = map[string]bool{}
	for _, r := range resps {
		if r.kind != "CAPABILITY" {
			continue
		}
		for _, f := range r.fields {
			if s := f.str(); s != "" {
				c.caps[strings.ToUpper(s)] = true
			}
		}
	}
	return nil
}

// readLine reads one CRLF-terminated line, bounded.
func (c *imapConn) readLine() (string, error) {
	var buf []byte
	for {
		chunk, err := c.r.ReadSlice('\n')
		if len(buf)+len(chunk) > imapLineLimit {
			return "", &mailError{kind: mailErrServer, msg: "the mail server sent a line too long to read"}
		}
		buf = append(buf, chunk...)
		if err == nil {
			break
		}
		if err != bufio.ErrBufferFull {
			return "", c.ioErr(err)
		}
	}
	return strings.TrimRight(string(buf), "\r\n"), nil
}

// ioErr is a read or write failure, or the context's end that caused it.
func (c *imapConn) ioErr(err error) error {
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	return mailNetErr(err)
}

// readResp reads one whole response with its literals.
func (c *imapConn) readResp() (*imapResp, error) {
	line, err := c.readLine()
	if err != nil {
		return nil, err
	}
	var parts []string
	var lits [][]byte
	total := len(line)
	for {
		n, at, ok := imapLiteralSuffix(line)
		if !ok {
			parts = append(parts, line)
			break
		}
		if n > imapLiteralLimit || total+n > imapResponseLimit {
			return nil, &mailError{kind: mailErrServer, msg: "the mail server sent more than JukaHub reads at once"}
		}
		parts = append(parts, line[:at])
		lit := make([]byte, n)
		if _, err := io.ReadFull(c.r, lit); err != nil {
			return nil, c.ioErr(err)
		}
		lits = append(lits, lit)
		total += n
		if line, err = c.readLine(); err != nil {
			return nil, err
		}
		total += len(line)
	}
	return imapParseResp(parts, lits)
}

// write sends raw bytes.
func (c *imapConn) write(s string) error {
	if _, err := io.WriteString(c.conn, s); err != nil {
		return c.ioErr(err)
	}
	return nil
}

// begin starts a command: the next tag and the deadline.
func (c *imapConn) begin(timeout time.Duration) (string, error) {
	if err := c.ctx.Err(); err != nil {
		return "", err
	}
	c.tag++
	_ = c.conn.SetDeadline(time.Now().Add(timeout))
	// Checked again after the deadline moved: a cancel that landed in between
	// must not be undone by it.
	if err := c.ctx.Err(); err != nil {
		return "", err
	}
	return "J" + strconv.Itoa(c.tag), nil
}

// run sends a command with no literal and collects its untagged answers.
func (c *imapConn) run(timeout time.Duration, cmd string) ([]*imapResp, error) {
	return c.runLit(timeout, []string{cmd}, nil)
}

// runLit sends a command whose text is split around literals: segs[i] is
// followed by lits[i]. Synchronizing literals wait for the server's "+"
// unless it allows LITERAL+.
func (c *imapConn) runLit(timeout time.Duration, segs []string, lits [][]byte) ([]*imapResp, error) {
	tag, err := c.begin(timeout)
	if err != nil {
		return nil, err
	}
	var out []*imapResp
	for i, seg := range segs {
		line := seg
		if i == 0 {
			line = tag + " " + seg
		}
		if i >= len(lits) {
			if err := c.write(line + "\r\n"); err != nil {
				return nil, err
			}
			continue
		}
		n := len(lits[i])
		nonSync := c.caps["LITERAL+"] || (c.caps["LITERAL-"] && n <= 4096)
		if nonSync {
			line += "{" + strconv.Itoa(n) + "+}\r\n"
		} else {
			line += "{" + strconv.Itoa(n) + "}\r\n"
		}
		if err := c.write(line); err != nil {
			return nil, err
		}
		if !nonSync {
			for {
				r, err := c.readResp()
				if err != nil {
					return nil, err
				}
				if r.tag == "+" {
					break
				}
				if r.tag == tag {
					return out, c.statusErr(r)
				}
				if len(out) < imapMaxResponses {
					out = append(out, r)
				}
			}
		}
		if _, err := c.conn.Write(lits[i]); err != nil {
			return nil, c.ioErr(err)
		}
	}
	return c.finish(tag, out)
}

// finish reads until the command's tagged answer.
func (c *imapConn) finish(tag string, out []*imapResp) ([]*imapResp, error) {
	for {
		r, err := c.readResp()
		if err != nil {
			return out, err
		}
		switch r.tag {
		case tag:
			if r.status == "OK" {
				return out, nil
			}
			return out, c.statusErr(r)
		case "+":
			// A continuation nobody asked for: the error details of a failed
			// XOAUTH2. An empty line lets the server finish with its NO.
			if err := c.write("\r\n"); err != nil {
				return out, err
			}
		case "*":
			if r.status == "BYE" {
				return out, &mailError{kind: mailErrServer, msg: "the mail server closed the connection: " + mailClean(r.text, 120)}
			}
			if len(out) < imapMaxResponses {
				out = append(out, r)
			}
		}
	}
}

// statusErr turns a NO or BAD into an error.
func (c *imapConn) statusErr(r *imapResp) error {
	code, _ := imapCut(r.code)
	text := mailClean(r.text, 160)
	switch strings.ToUpper(code) {
	case "AUTHENTICATIONFAILED", "AUTHORIZATIONFAILED", "EXPIRED", "PRIVACYREQUIRED":
		return &mailError{kind: mailErrAuth, msg: setOr(text, "the server refused the sign-in"), detail: text}
	case "UNAVAILABLE":
		return &mailError{kind: mailErrNet, msg: setOr(text, "the mail server is busy — try again later"), detail: text}
	}
	return &mailError{kind: mailErrServer, msg: setOr(text, "the mail server said no"), detail: text}
}

// ── Sign-in ─────────────────────────────────────────────────────────────

// authenticate runs a SASL exchange with one client message (PLAIN,
// XOAUTH2). Any refusal is a sign-in error.
func (c *imapConn) authenticate(mech string, msg []byte) error {
	b64 := base64.StdEncoding.EncodeToString(msg)
	tag, err := c.begin(imapCmdTimeout)
	if err != nil {
		return err
	}
	if c.caps["SASL-IR"] {
		err = c.write(tag + " AUTHENTICATE " + mech + " " + b64 + "\r\n")
	} else {
		if err = c.write(tag + " AUTHENTICATE " + mech + "\r\n"); err == nil {
			var r *imapResp
			for {
				if r, err = c.readResp(); err != nil || r.tag == "+" || r.tag == tag {
					break
				}
			}
			if err == nil && r.tag == tag {
				return c.authErr(c.statusErr(r))
			}
			if err == nil {
				err = c.write(b64 + "\r\n")
			}
		}
	}
	if err != nil {
		return err
	}
	_, err = c.finish(tag, nil)
	return c.authErr(err)
}

// authErr marks a server refusal during sign-in as a sign-in error.
func (c *imapConn) authErr(err error) error {
	var me *mailError
	if errors.As(err, &me) && me.kind == mailErrServer {
		me.kind = mailErrAuth
	}
	return err
}

// login signs in with a password.
func (c *imapConn) login(user, pass string) error {
	if c.caps["AUTH=PLAIN"] {
		return c.authenticate("PLAIN", []byte("\x00"+user+"\x00"+pass))
	}
	if c.caps["LOGINDISABLED"] {
		return &mailError{kind: mailErrAuth, msg: "the server does not accept a password here"}
	}
	segs, lits := imapArgs("LOGIN", user, pass)
	_, err := c.runLit(imapCmdTimeout, segs, lits)
	return c.authErr(err)
}

// imapArgs writes a command with string arguments, each quoted or, when a
// quoted string cannot hold it, sent as a literal.
func imapArgs(cmd string, args ...string) ([]string, [][]byte) {
	var segs []string
	var lits [][]byte
	cur := cmd
	for _, a := range args {
		if q, ok := imapQuote(a); ok {
			cur += " " + q
			continue
		}
		segs = append(segs, cur+" ")
		lits = append(lits, []byte(a))
		cur = ""
	}
	return append(segs, cur), lits
}

// mailXOAuth2 is the XOAUTH2 client message.
func mailXOAuth2(user, token string) []byte {
	return []byte("user=" + user + "\x01auth=Bearer " + token + "\x01\x01")
}

// mailIMAPOpen connects and signs in to an account's server.
func mailIMAPOpen(ctx context.Context, a mailcalAccount) (*imapConn, error) {
	c, err := imapDial(ctx, a.IMAPHost, a.IMAPPort, a.IMAPSec)
	if err != nil {
		return nil, err
	}
	if a.OAuth == mailcalOAuthMicrosoft && a.RefreshToken != "" {
		token, err := mailMSAccessToken(ctx, a)
		if err == nil {
			err = c.authenticate("XOAUTH2", mailXOAuth2(a.user(), token))
		}
		if err == nil {
			err = c.loadCaps()
		}
		if err != nil {
			c.close()
			return nil, err
		}
		return c, nil
	}
	pass := mailcalOpen(a.Password, mailcalKeyPassword)
	if pass == "" {
		c.close()
		return nil, &mailError{kind: mailErrAuth, msg: "no password is saved for this account"}
	}
	// The capabilities are asked again once signed in: Gmail, for one, only
	// names MOVE, UIDPLUS and LITERAL- after the sign-in.
	err = c.login(a.user(), pass)
	if err == nil {
		err = c.loadCaps()
	}
	if err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

// ── Mailboxes and messages ──────────────────────────────────────────────

// imapBox is what SELECT/EXAMINE says about a mailbox.
type imapBox struct {
	validity uint32
	next     uint32
	exists   uint32
}

// selectBox opens a mailbox, read-only for EXAMINE.
func (c *imapConn) selectBox(name string, readOnly bool) (imapBox, error) {
	cmd := "SELECT"
	if readOnly {
		cmd = "EXAMINE"
	}
	segs, lits := imapArgs(cmd, name)
	resps, err := c.runLit(imapCmdTimeout, segs, lits)
	if err != nil {
		return imapBox{}, err
	}
	var b imapBox
	for _, r := range resps {
		if r.kind == "EXISTS" {
			b.exists = r.num
		}
		word, rest := imapCut(r.code)
		n, _ := strconv.ParseUint(strings.TrimSpace(rest), 10, 32)
		switch strings.ToUpper(word) {
		case "UIDVALIDITY":
			b.validity = uint32(n)
		case "UIDNEXT":
			b.next = uint32(n)
		}
	}
	return b, nil
}

// searchUIDs runs UID SEARCH with criteria.
func (c *imapConn) searchUIDs(criteria string) ([]uint32, error) {
	resps, err := c.run(imapCmdTimeout, "UID SEARCH "+criteria)
	if err != nil {
		return nil, err
	}
	var out []uint32
	for _, r := range resps {
		if r.kind != "SEARCH" {
			continue
		}
		for _, f := range r.fields {
			if n := f.num(); n > 0 && len(out) < 200000 {
				out = append(out, n)
			}
		}
	}
	return out, nil
}

// fetch runs FETCH (or UID FETCH) and returns each message's items.
func (c *imapConn) fetch(timeout time.Duration, cmd string) ([]map[string]imapValue, error) {
	resps, err := c.run(timeout, cmd)
	if err != nil {
		return nil, err
	}
	var out []map[string]imapValue
	for _, r := range resps {
		if r.kind == "FETCH" {
			out = append(out, imapFetchItems(r))
		}
	}
	return out, nil
}

// storeFlags adds (+) or removes (-) a flag on a message.
func (c *imapConn) storeFlags(uid uint32, add bool, flag string) error {
	op := "-FLAGS.SILENT"
	if add {
		op = "+FLAGS.SILENT"
	}
	_, err := c.run(imapCmdTimeout, "UID STORE "+strconv.FormatUint(uint64(uid), 10)+" "+op+" ("+flag+")")
	return err
}

// storeFlagsSet is storeFlags for several messages.
func (c *imapConn) storeFlagsSet(uids []uint32, add bool, flag string) error {
	if len(uids) == 0 {
		return nil
	}
	op := "-FLAGS.SILENT"
	if add {
		op = "+FLAGS.SILENT"
	}
	_, err := c.run(imapCmdTimeout, "UID STORE "+imapUIDSet(uids)+" "+op+" ("+flag+")")
	return err
}

// move moves a message to another mailbox: MOVE when the server has it, else
// COPY, \Deleted and an expunge (UID EXPUNGE with UIDPLUS, so nothing else
// marked deleted goes with it).
func (c *imapConn) move(uid uint32, dest string) error {
	set := strconv.FormatUint(uint64(uid), 10)
	if c.caps["MOVE"] {
		segs, lits := imapArgs("UID MOVE "+set, dest)
		_, err := c.runLit(imapCmdTimeout, segs, lits)
		return err
	}
	segs, lits := imapArgs("UID COPY "+set, dest)
	if _, err := c.runLit(imapCmdTimeout, segs, lits); err != nil {
		return err
	}
	if err := c.storeFlags(uid, true, `\Deleted`); err != nil {
		return err
	}
	if c.caps["UIDPLUS"] {
		_, err := c.run(imapCmdTimeout, "UID EXPUNGE "+set)
		return err
	}
	_, err := c.run(imapCmdTimeout, "EXPUNGE")
	return err
}

// imapFolder is one LIST entry.
type imapFolder struct {
	name  string
	attrs []string // upper case
}

// list lists every mailbox (bounded).
func (c *imapConn) list() ([]imapFolder, error) {
	resps, err := c.run(imapCmdTimeout, `LIST "" "*"`)
	if err != nil {
		return nil, err
	}
	var out []imapFolder
	for _, r := range resps {
		if r.kind != "LIST" || len(r.fields) < 3 || len(out) >= 1000 {
			continue
		}
		f := imapFolder{name: r.fields[2].str(), attrs: imapFlags(r.fields[0])}
		if f.name != "" {
			out = append(out, f)
		}
	}
	return out, nil
}

// appendMsg stores a message in a mailbox, marked read.
func (c *imapConn) appendMsg(box string, msg []byte) error {
	q, ok := imapQuote(box)
	if !ok {
		return &mailError{kind: mailErrServer, msg: "the Sent folder's name cannot be written"}
	}
	_, err := c.runLit(60*time.Second, []string{"APPEND " + q + ` (\Seen) `, ""}, [][]byte{msg})
	return err
}

// mailFolders are the special mailboxes an account's actions use.
type mailFolders struct {
	Trash   string `json:"trash,omitempty"`
	Archive string `json:"archive,omitempty"`
	Sent    string `json:"sent,omitempty"`
}

// imapPickFolders finds Trash, Archive and Sent: by the special-use flags
// first (RFC 6154), then by the usual names. Gmail's archive is All Mail:
// moving a message there takes it out of the inbox and nothing else.
func imapPickFolders(list []imapFolder) mailFolders {
	var f mailFolders
	var all string
	for _, e := range list {
		switch {
		case f.Trash == "" && imapHasFlag(e.attrs, `\Trash`):
			f.Trash = e.name
		case f.Archive == "" && imapHasFlag(e.attrs, `\Archive`):
			f.Archive = e.name
		case all == "" && imapHasFlag(e.attrs, `\All`):
			all = e.name
		case f.Sent == "" && imapHasFlag(e.attrs, `\Sent`):
			f.Sent = e.name
		}
	}
	byName := func(names ...string) string {
		for _, want := range names {
			for _, e := range list {
				n := strings.ToLower(e.name)
				if n == want || strings.HasSuffix(n, "/"+want) || strings.HasSuffix(n, "."+want) {
					return e.name
				}
			}
		}
		return ""
	}
	if f.Trash == "" {
		f.Trash = byName("trash", "deleted items", "deleted messages", "deleted", "bin")
	}
	if f.Archive == "" {
		f.Archive = setOr(all, byName("archive", "archives", "all mail"))
	}
	if f.Sent == "" {
		f.Sent = byName("sent", "sent items", "sent messages", "sent mail")
	}
	return f
}
