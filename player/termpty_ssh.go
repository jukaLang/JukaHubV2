package main

// termpty_ssh.go — `ssh user@host` in the Terminal. When the system has an
// ssh client (Windows' OpenSSH, Dropbear, Linux packages' openssh-client) it
// runs in a session like any program; without one, this built-in client
// (golang.org/x/crypto/ssh) connects instead: password and
// keyboard-interactive sign-in typed in the session (plus a key in ~/.ssh
// when there is an unencrypted one), a terminal of the session's size that
// follows it when it changes, and trust on first use for host keys, stored in
// JukaHub's data folder. A host whose key changed is refused, with how to
// forget the old key when the change is expected (`ssh forget <host>`).
//
// `ssh on|off|status` stays the SFTP server's switch (termSSHCommand).

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

const (
	termSSHKnownHostsFile = "terminal_known_hosts"
	termSSHDialTimeout    = 15 * time.Second
)

// termSSHTarget is where the built-in client connects.
type termSSHTarget struct {
	user string
	host string
	port int
}

func (t termSSHTarget) addr() string { return net.JoinHostPort(t.host, strconv.Itoa(t.port)) }

// termSSHKnownID is the host's name in the known-hosts file.
func (t termSSHTarget) knownID() string {
	if t.port == 22 {
		return strings.ToLower(t.host)
	}
	return "[" + strings.ToLower(t.host) + "]:" + strconv.Itoa(t.port)
}

// termPtySSHCommand is the Terminal's ssh: the SFTP server's switch, `ssh
// forget <host>`, or a connection.
func termPtySSHCommand(args []string, line string) {
	if len(args) == 0 {
		termSSHCommand(args)
		return
	}
	switch strings.ToLower(args[0]) {
	case "on", "off", "start", "stop", "status", "help", "-h", "--help":
		termSSHCommand(args)
		return
	case "forget":
		termSSHForget(args[1:])
		return
	}
	if termPtyHasProgram("ssh") {
		termPtyLaunch(termPtyLineCommand(line), "ssh")
		return
	}
	target, err := termSSHParseTarget(args)
	if err != nil {
		termAppend(err.Error(), termKindError)
		termAppend("The built-in SSH client takes: ssh [-p port] [user@]host[:port]   ·   ssh on|off|status runs JukaHub's own SFTP server.", termKindSystem)
		return
	}
	if termPtyResumeIfAlive() {
		return
	}
	cols, rows := termPtyGridSize(appConfig)
	label := "ssh " + target.user + "@" + target.host
	err = termPtyStartSession(label, cols, rows, func(cols, rows int, s *termPtySession) (termPtyProc, error) {
		return newTermSSHProc(target, cols, rows), nil
	})
	if err != nil {
		termAppend("Could not start ssh: "+err.Error(), termKindError)
	}
}

// termSSHParseTarget reads ssh [-p port] [-l user] [user@]host[:port].
func termSSHParseTarget(args []string) (termSSHTarget, error) {
	t := termSSHTarget{port: 22, user: termUserName()}
	if t.user == "unknown" || t.user == "" {
		t.user = "root"
	}
	userSet := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" && i+1 < len(args):
			p, err := strconv.Atoi(args[i+1])
			if err != nil || p <= 0 || p > 65535 {
				return t, fmt.Errorf("ssh: bad port %q", args[i+1])
			}
			t.port = p
			i++
		case a == "-l" && i+1 < len(args):
			t.user, userSet = args[i+1], true
			i++
		case strings.HasPrefix(a, "-"):
			return t, fmt.Errorf("ssh: the option %s needs an ssh program (apt install openssh-client)", a)
		case t.host == "":
			hostPart := a
			if at := strings.LastIndexByte(a, '@'); at >= 0 {
				if !userSet {
					t.user = a[:at]
				}
				hostPart = a[at+1:]
			}
			if h, p, err := net.SplitHostPort(hostPart); err == nil {
				port, convErr := strconv.Atoi(p)
				if convErr != nil || port <= 0 || port > 65535 {
					return t, fmt.Errorf("ssh: bad port %q", p)
				}
				hostPart, t.port = h, port
			}
			t.host = strings.Trim(hostPart, "[]")
		default:
			return t, errors.New("ssh: running a remote command needs an ssh program (apt install openssh-client); the built-in client opens a shell")
		}
	}
	if t.host == "" || strings.ContainsAny(t.host, " /\\") || t.user == "" {
		return t, errors.New("ssh: which host? ssh user@host")
	}
	return t, nil
}

// ── The connection ───────────────────────────────────────────────────────

// termSSHProc is the built-in client as a session's program.
type termSSHProc struct {
	target termSSHTarget

	outR *io.PipeReader
	outW *io.PipeWriter
	keys chan []byte

	mu     sync.Mutex
	conn   net.Conn
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	cols   int
	rows   int

	// ctx is cancelled by Kill, so ending the session while it connects
	// does not wait out the dial's timeout.
	ctx    context.Context
	cancel context.CancelFunc
	// secret is set while a password (or an unechoed answer) is read.
	secret atomic.Bool

	done     chan struct{}
	killed   chan struct{}
	killOnce sync.Once
	code     int
	err      error
}

func newTermSSHProc(target termSSHTarget, cols, rows int) *termSSHProc {
	r, w := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	p := &termSSHProc{
		target: target,
		outR:   r,
		outW:   w,
		keys:   make(chan []byte, 256),
		cols:   cols,
		rows:   rows,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		killed: make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *termSSHProc) Read(b []byte) (int, error) { return p.outR.Read(b) }

// Write is what the user types: the remote shell's input once it is up, and
// the answers to the sign-in prompts before.
func (p *termSSHProc) Write(b []byte) (int, error) {
	p.mu.Lock()
	stdin := p.stdin
	p.mu.Unlock()
	if stdin != nil {
		return stdin.Write(b)
	}
	select {
	case p.keys <- append([]byte(nil), b...):
	default:
	}
	return len(b), nil
}

func (p *termSSHProc) Resize(cols, rows int) error {
	p.mu.Lock()
	p.cols, p.rows = cols, rows
	sess := p.sess
	p.mu.Unlock()
	if sess != nil {
		// A request on the connection: never on the render thread.
		go func() { _ = sess.WindowChange(rows, cols) }()
	}
	return nil
}

// EchoOff is true while a sign-in answer that is not shown is being typed.
// Once the remote shell runs, the server alone knows its echo.
func (p *termSSHProc) EchoOff() bool { return p.secret.Load() }

// Kill closes the connection (a dial still in progress included); there is
// nothing to ask first, so grace is not used.
func (p *termSSHProc) Kill(grace time.Duration) {
	p.killOnce.Do(func() {
		close(p.killed)
		p.cancel()
		p.mu.Lock()
		conn, client := p.conn, p.client
		p.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		if conn != nil {
			_ = conn.Close()
		}
		_ = p.outW.Close()
	})
}

func (p *termSSHProc) Wait() (int, error) {
	<-p.done
	return p.code, nil
}

func (p *termSSHProc) say(s string) {
	_, _ = p.outW.Write([]byte(s))
}

func (p *termSSHProc) run() {
	defer close(p.done)
	defer p.cancel()
	defer func() {
		if r := recover(); r != nil {
			logError("terminal", "ssh client panicked: %v", r)
			p.code = 255
		}
		p.secret.Store(false)
		_ = p.outW.Close()
	}()
	t := p.target
	p.say("Connecting to " + termSSHClean(t.addr()) + " …\r\n")
	cfg := &ssh.ClientConfig{
		User:            t.user,
		Auth:            p.authMethods(),
		HostKeyCallback: p.checkHostKey,
		Timeout:         termSSHDialTimeout,
		BannerCallback: func(msg string) error {
			p.say(termSSHCRLF(termSSHClean(msg)))
			return nil
		},
	}
	if algos := termSSHKnownAlgorithms(t.knownID()); len(algos) > 0 {
		cfg.HostKeyAlgorithms = algos
	}
	conn, err := (&net.Dialer{Timeout: termSSHDialTimeout}).DialContext(p.ctx, "tcp", t.addr())
	if err != nil {
		p.fail("ssh: " + err.Error())
		return
	}
	p.mu.Lock()
	p.conn = conn
	p.mu.Unlock()
	select {
	case <-p.killed:
		_ = conn.Close()
		return
	default:
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, t.addr(), cfg)
	if err != nil {
		_ = conn.Close()
		p.fail("ssh: " + err.Error())
		return
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()
	p.mu.Lock()
	p.client = client
	p.mu.Unlock()
	sess, err := client.NewSession()
	if err != nil {
		p.fail("ssh: " + err.Error())
		return
	}
	p.mu.Lock()
	cols, rows := p.cols, p.rows
	p.mu.Unlock()
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		p.fail("ssh: the server gave no terminal: " + err.Error())
		return
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		p.fail("ssh: " + err.Error())
		return
	}
	sess.Stdout = p.outW
	sess.Stderr = p.outW
	if err := sess.Shell(); err != nil {
		p.fail("ssh: " + err.Error())
		return
	}
	p.mu.Lock()
	p.sess, p.stdin = sess, stdin
	resized := p.cols != cols || p.rows != rows
	cols, rows = p.cols, p.rows
	p.mu.Unlock()
	if resized {
		_ = sess.WindowChange(rows, cols)
	}
	err = sess.Wait()
	var exitErr *ssh.ExitError
	switch {
	case errors.As(err, &exitErr):
		p.code = exitErr.ExitStatus()
	case err != nil:
		p.code = 255
	}
}

// fail ends a connection that could not start, with the reason on screen
// (the line terminal keeps it when the session closes).
func (p *termSSHProc) fail(msg string) {
	p.code = 255
	select {
	case <-p.killed:
		return
	default:
	}
	p.say(termSSHCRLF(termSSHClean(msg)) + "\r\n")
}

func (p *termSSHProc) authMethods() []ssh.AuthMethod {
	t := p.target
	var methods []ssh.AuthMethod
	if signers := termSSHHomeSigners(); len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	methods = append(methods,
		ssh.RetryableAuthMethod(ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			// The server's text, shown without its control sequences: a
			// hostile server must not be able to redraw JukaHub's screen
			// (or fake a local prompt) through a sign-in question.
			if name = termSSHClean(name); name != "" {
				p.say(termSSHCRLF(name) + "\r\n")
			}
			if instruction = termSSHClean(instruction); instruction != "" {
				p.say(termSSHCRLF(instruction) + "\r\n")
			}
			answers := make([]string, len(questions))
			for i, q := range questions {
				p.drainKeys()
				p.say(termSSHCRLF(termSSHClean(q)))
				echo := i < len(echos) && echos[i]
				a, err := p.readLine(echo)
				p.say("\r\n")
				if err != nil {
					return nil, err
				}
				answers[i] = a
			}
			return answers, nil
		}), 3),
		ssh.RetryableAuthMethod(ssh.PasswordCallback(func() (string, error) {
			p.drainKeys()
			p.say(termSSHClean(t.user+"@"+t.host) + "'s password: ")
			pw, err := p.readLine(false)
			p.say("\r\n")
			return pw, err
		}), 3),
	)
	return methods
}

// errTermSSHAborted is Ctrl+C (or Ctrl+D) at a sign-in prompt.
var errTermSSHAborted = errors.New("cancelled")

// drainKeys drops what was typed before a prompt appeared (keys pressed
// while "Connecting…" showed), so it cannot answer a question not yet asked.
func (p *termSSHProc) drainKeys() {
	for {
		select {
		case <-p.keys:
		default:
			return
		}
	}
}

// readLine reads one answer typed in the session: Enter ends it, Backspace
// and Ctrl+U edit it, Ctrl+C cancels; a password is not shown (and the
// on-screen keyboard hides it too, through EchoOff).
func (p *termSSHProc) readLine(echo bool) (string, error) {
	if !echo {
		p.secret.Store(true)
		defer p.secret.Store(false)
	}
	var line []rune
	for {
		var b []byte
		select {
		case b = <-p.keys:
		case <-p.killed:
			return "", errTermSSHAborted
		}
		if len(b) > 0 && b[0] == 0x1b {
			continue // an arrow or function key: no line editing here
		}
		for len(b) > 0 {
			r, size := utf8.DecodeRune(b)
			b = b[size:]
			switch {
			case r == '\r' || r == '\n':
				return string(line), nil
			case r == 0x03 || r == 0x04:
				p.say("^C\r\n")
				return "", errTermSSHAborted
			case r == 0x7f || r == 0x08:
				if len(line) > 0 {
					line = line[:len(line)-1]
					if echo {
						p.say("\b \b")
					}
				}
			case r == 0x15:
				if echo {
					p.say(strings.Repeat("\b \b", len(line)))
				}
				line = line[:0]
			case r >= 0x20 && r != utf8.RuneError && len(line) < 1024:
				line = append(line, r)
				if echo {
					p.say(string(r))
				}
			}
		}
	}
}

// checkHostKey is trust on first use: a known host must show the key it
// showed before; a new one is shown by fingerprint and kept once accepted.
func (p *termSSHProc) checkHostKey(hostname string, remote net.Addr, key ssh.PublicKey) error {
	id := p.target.knownID()
	line := key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
	known := termSSHKnownKeys(id)
	for _, k := range known {
		if k == line {
			return nil
		}
	}
	fp := ssh.FingerprintSHA256(key)
	if len(known) > 0 {
		p.say("\r\n@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\n")
		p.say("@    WARNING: THE HOST KEY OF " + p.target.host + " HAS CHANGED!\r\n")
		p.say("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\n")
		p.say("Someone could be listening in, or the server was reinstalled.\r\n")
		p.say("It now shows " + key.Type() + " " + fp + ".\r\n")
		p.say("JukaHub refuses to connect. If the change is expected, run\r\n  ssh forget " + p.target.host + "\r\nin the Terminal and connect again.\r\n")
		return errors.New("host key changed — connection refused")
	}
	addr := ""
	if remote != nil {
		addr = " (" + remote.String() + ")"
	}
	p.say("The authenticity of host '" + p.target.host + addr + "' can't be established.\r\n")
	p.say(termSSHClean(key.Type()) + " key fingerprint is " + fp + ".\r\n")
	p.drainKeys()
	p.say("Trust it and connect? (yes/no) ")
	answer, err := p.readLine(true)
	p.say("\r\n")
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
	default:
		return errors.New("host key not trusted")
	}
	if err := termSSHRemember(id, line); err != nil {
		p.say("Could not save the key (" + err.Error() + "); it will be asked again.\r\n")
	} else {
		p.say("Added " + p.target.host + " to JukaHub's known hosts.\r\n")
	}
	return nil
}

// termSSHClean is a server's text without control characters (Esc, C1 and
// the rest) other than tab and line breaks, so it can only print.
func termSSHClean(s string) string {
	s = strings.ToValidUTF8(s, "?")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, s)
}

// termSSHCRLF makes a server's text show on the terminal (bare \n → \r\n).
func termSSHCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// termSSHHomeSigners are the unencrypted private keys in ~/.ssh.
func termSSHHomeSigners() []ssh.Signer {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	var out []ssh.Signer
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		data, err := os.ReadFile(filepath.Join(home, ".ssh", name))
		if err != nil || len(data) > 64<<10 {
			continue
		}
		if s, err := ssh.ParsePrivateKey(data); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// ── Known hosts ──────────────────────────────────────────────────────────

var termSSHKnownMu sync.Mutex

func termSSHKnownPath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, termSSHKnownHostsFile)
}

// termSSHKnownLines reads the file: "<id> <type> <base64>" per line.
func termSSHKnownLines() []string {
	path := termSSHKnownPath()
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(io.LimitReader(f, 1<<20))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// termSSHKnownKeys are the keys stored for id, as "<type> <base64>".
func termSSHKnownKeys(id string) []string {
	termSSHKnownMu.Lock()
	defer termSSHKnownMu.Unlock()
	var out []string
	for _, line := range termSSHKnownLines() {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == id {
			out = append(out, f[1]+" "+f[2])
		}
	}
	return out
}

// termSSHKnownAlgorithms asks a known host for the key type it is known
// by, so a server with several keys does not look like a changed one.
func termSSHKnownAlgorithms(id string) []string {
	var algos []string
	for _, k := range termSSHKnownKeys(id) {
		typ, _, _ := strings.Cut(k, " ")
		if typ == ssh.KeyAlgoRSA {
			algos = append(algos, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		} else {
			algos = append(algos, typ)
		}
	}
	return algos
}

func termSSHRemember(id, key string) error {
	termSSHKnownMu.Lock()
	defer termSSHKnownMu.Unlock()
	path := termSSHKnownPath()
	if path == "" {
		return errors.New("no data folder")
	}
	lines := append(termSSHKnownLines(), id+" "+key)
	return termSSHWriteKnown(path, lines)
}

func termSSHWriteKnown(path string, lines []string) error {
	body := "# JukaHub Terminal: SSH host keys trusted on first use (ssh forget <host> removes one)\n"
	if len(lines) > 0 {
		body += strings.Join(lines, "\n") + "\n"
	}
	// Synced before the rename: a handheld's normal way to stop is the power
	// switch, and a trusted key must not come back as an empty file.
	return AtomicWrite(path, []byte(body), 0o600)
}

// termSSHForget removes a host's keys: `ssh forget host` (every port) or
// `ssh forget host:port`.
func termSSHForget(args []string) {
	if len(args) != 1 {
		termAppend("Usage: ssh forget <host>   (removes the host key JukaHub remembers)", termKindError)
		return
	}
	host, port := strings.ToLower(args[0]), ""
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		host, port = h, p
	}
	match := func(id string) bool {
		if port != "" {
			return id == termSSHTarget{host: host, port: termSSHAtoi(port, 22)}.knownID()
		}
		return id == host || strings.HasPrefix(id, "["+host+"]:")
	}
	termSSHKnownMu.Lock()
	defer termSSHKnownMu.Unlock()
	path := termSSHKnownPath()
	var keep []string
	removed := 0
	for _, line := range termSSHKnownLines() {
		if f := strings.Fields(line); len(f) > 0 && match(f[0]) {
			removed++
			continue
		}
		keep = append(keep, line)
	}
	if removed == 0 {
		termAppend("No key is remembered for "+args[0]+".", termKindSystem)
		return
	}
	if err := termSSHWriteKnown(path, keep); err != nil {
		termAppend("Could not update the known hosts: "+err.Error(), termKindError)
		return
	}
	termAppend("Forgot "+pluralizeCount(removed, "key", "keys")+" for "+args[0]+". The next connection asks to trust it again.", termKindSystem)
}

// termSSHAtoi is strconv.Atoi with a fallback.
func termSSHAtoi(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}
