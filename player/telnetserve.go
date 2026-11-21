package main

// telnetserve.go — Telnet server: a shell on the handheld from any telnet
// client on the same network (PuTTY, the telnet command, a phone terminal).
//
// Telnet sends everything in clear text, the password included, so the
// server is built to be small and short-lived:
//
//   - Off by default; Tools → Network tools → Telnet starts it
//     (vncserver_ui.go), and the sheet says plainly that it is not
//     encrypted. It stops after fileServerIdleLimit with nobody connected,
//     when the device joins another network, at the startup lock and with
//     JukaHub (the VNC server's watchdog and hooks).
//   - Port 23 on the handheld, 2323 elsewhere (a computer reserves the low
//     ports for its administrator); telnet_port / JUKAHUB_TELNET_PORT
//     change it. LAN addresses only, as VNC: private, link-local, loopback
//     and Tailscale's range. At most telnetMaxConns sessions.
//   - A password made for each start, shown on screen, never saved or
//     logged. Three wrong tries end the connection, each after a 2 s wait,
//     and five in a minute lock the address out for a minute.
//   - The shell is the Terminal's (termpty.go: termPtyShellCommand, its
//     environment, termPtySpawn — a PTY on the handheld, a ConPTY on
//     Windows), started in the SD card (the home folder on a computer).
//
// The protocol part is the least a modern client needs: the server echoes
// (so the password stays off the client's screen) and suppresses go-ahead,
// asks for the window size (NAWS) and passes it to the PTY, refuses every
// other option once, strips IAC sequences, and folds CR NUL and CR LF to the
// CR a terminal's Enter sends.

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	telnetHandheldPort = 23
	telnetDesktopPort  = 2323
	telnetMaxConns     = 2
	// telnetLoginLimit bounds the time to sign in.
	telnetLoginLimit = 60 * time.Second
	telnetMaxTries   = 3
	telnetFailDelay  = 2 * time.Second
	// telnetLineMax bounds a typed password.
	telnetLineMax = 128
	// telnetInQueue bounds the typed chunks waiting for a shell program that
	// reads nothing.
	telnetInQueue = 64
	// telnetShotPassword is shown instead in screenshot mode.
	telnetShotPassword = "demo-demo-demo"
)

// Telnet commands and options (RFC 854, 857, 858, 1073).
const (
	telnetIAC  = 255
	telnetDONT = 254
	telnetDO   = 253
	telnetWONT = 252
	telnetWILL = 251
	telnetSB   = 250
	telnetIP   = 244
	telnetSE   = 240

	telnetOptEcho = 1
	telnetOptSGA  = 3
	telnetOptNAWS = 31

	// telnetSBMax bounds one subnegotiation; NAWS needs five bytes.
	telnetSBMax = 64
	// telnetMaxSide bounds a reported window side.
	telnetMaxSide = 1000
)

// telnetGreeting is what the server says first: it will echo and suppress
// go-ahead (character at a time, the password unechoed), and asks the client
// to report its window size.
var telnetGreeting = []byte{
	telnetIAC, telnetWILL, telnetOptEcho,
	telnetIAC, telnetWILL, telnetOptSGA,
	telnetIAC, telnetDO, telnetOptNAWS,
}

// telnetService is the running server. Use telnetSrv.
type telnetService struct {
	mu       sync.Mutex
	ln       net.Listener
	port     int
	gen      uint64
	password string
	lastErr  string
	netBase  fsNetIdentity
	conns    map[net.Conn]struct{}
	cmd      termPtyCommand
	shell    string
	env      []string
	dir      string
	clients  fsClients
	activity fsActivity
	fails    vncFailGate
}

var telnetSrv = &telnetService{}

// Running reports whether the listener is open.
func (s *telnetService) Running() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ln != nil
}

// Port is the port the server listens on.
func (s *telnetService) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// Password is this run's password ("" while stopped).
func (s *telnetService) Password() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.password
}

// Shell names the shell a session gets.
func (s *telnetService) Shell() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shell
}

// Address is "address:port" for the tile ("" while stopped or offline).
func (s *telnetService) Address() string {
	if !s.Running() {
		return ""
	}
	host := toolsIPAddress()
	if host == "" {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(s.Port()))
}

// Command is what to type on a computer: "telnet <address>", plus the port
// when it is not 23.
func (s *telnetService) Command() string {
	if !s.Running() {
		return ""
	}
	host := toolsIPAddress()
	if host == "" {
		return ""
	}
	if p := s.Port(); p != 23 {
		return "telnet " + host + " " + strconv.Itoa(p)
	}
	return "telnet " + host
}

// Clients lists the sessions for the Network tools page.
func (s *telnetService) Clients() []fsClient { return s.clients.list() }

// telnetPort is the configured port. Main thread.
func telnetPort(config *Config) int {
	def := telnetDesktopPort
	if IsTrimuiDevice() {
		def = telnetHandheldPort
	}
	return vncPortSetting(config, "telnet_port", "JUKAHUB_TELNET_PORT", def)
}

// Start opens the server with a new password. Main thread: the shell and
// its environment are looked up here, where the Terminal looks them up.
func (s *telnetService) Start(config *Config) error {
	if s.Running() {
		return nil
	}
	cmd, shell, err := termPtyShellCommand("shell")
	if err != nil {
		return err
	}
	env := termPtyEnv()
	dir := fileServerCardRoot()
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	pass, err := vncGeneratePassword(12, 4)
	if err != nil {
		return fmt.Errorf("make a Telnet password: %w", err)
	}
	addr := fmt.Sprintf("0.0.0.0:%d", telnetPort(config))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return fmt.Errorf("start the Telnet server on %s: %w", addr, err)
	}
	s.mu.Lock()
	s.ln, s.password, s.lastErr = ln, pass, ""
	s.cmd, s.shell, s.env, s.dir = cmd, shell, env, dir
	s.gen++
	gen := s.gen
	s.netBase = fsNetIdentity{ip: localIPv4()}
	s.conns = make(map[net.Conn]struct{})
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tcp.Port
	}
	s.mu.Unlock()
	s.activity.touch()
	vncSheetSeq.Add(1)
	go s.acceptLoop(ln)
	vncWatchRun("Telnet", gen, s.watchTarget, s.stopRun)
	logInfo("telnet", "serving %s on %s (not encrypted)", shell, ln.Addr())
	return nil
}

// Stop closes the listener and every session (each session's shell is
// hung up when its connection ends).
func (s *telnetService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopLocked()
}

// stopRun stops the server if it is still run gen (the watchdog's stop).
func (s *telnetService) stopRun(gen uint64) bool {
	s.mu.Lock()
	if s.ln == nil || s.gen != gen {
		s.mu.Unlock()
		return false
	}
	s.stopLocked()
	return true
}

// stopLocked is Stop for a caller holding s.mu, which it releases.
func (s *telnetService) stopLocked() {
	ln := s.ln
	s.ln, s.password = nil, ""
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.conns = make(map[net.Conn]struct{})
	s.clients.clear()
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	if ln != nil {
		logInfo("telnet", "stopped")
		vncSheetSeq.Add(1)
	}
}

// watchTarget is the server as the watchdog sees it: an open session counts
// as use.
func (s *telnetService) watchTarget() fsWatchTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.activity.lastUsed()
	if len(s.conns) > 0 {
		last = time.Now()
	}
	return fsWatchTarget{running: s.ln != nil, gen: s.gen, base: s.netBase, lastUsed: last}
}

// acceptLoop admits LAN clients, up to telnetMaxConns.
func (s *telnetService) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		host := fsClientHost(conn.RemoteAddr().String())
		if !webdavLANIP(net.ParseIP(host)) {
			logWarn("telnet", "refused %s: not on this network", host)
			_ = conn.Close()
			continue
		}
		if s.fails.lockedOut(host, time.Now()) {
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.ln != ln {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		if len(s.conns) >= telnetMaxConns {
			s.mu.Unlock()
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.WriteString(conn, "\r\nTwo sessions are already open on this handheld.\r\n")
			_ = conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		pass, cmd, env, dir := s.password, s.cmd, s.env, s.dir
		s.mu.Unlock()
		go s.serve(conn, pass, cmd, env, dir)
	}
}

// serve signs one client in and runs its shell until either side ends.
func (s *telnetService) serve(conn net.Conn, password string, cmd termPtyCommand, env []string, dir string) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()
	host := fsClientHost(conn.RemoteAddr().String())
	_ = conn.SetDeadline(time.Now().Add(telnetLoginLimit))
	var sizeMu sync.Mutex
	cols, rows := 80, 24
	var proc termPtyProc
	p := newTelnetParser()
	p.reply = func(b []byte) { _, _ = conn.Write(b) }
	p.onSize = func(c, r int) {
		sizeMu.Lock()
		cols, rows = c, r
		running := proc
		sizeMu.Unlock()
		if running != nil {
			_ = running.Resize(c, r)
		}
	}
	if _, err := conn.Write(telnetGreeting); err != nil {
		return
	}
	_, _ = io.WriteString(conn, "\r\nJukaHub Telnet. This connection is NOT encrypted: use SSH on a network you do not trust.\r\n")
	br := bufio.NewReader(conn)
	for try := 1; ; try++ {
		if _, err := io.WriteString(conn, "Password: "); err != nil {
			return
		}
		line, err := telnetReadLine(br, p, telnetLineMax)
		if err != nil {
			return
		}
		if telnetPasswordOK(line, password) {
			break
		}
		s.fails.note(host, time.Now())
		logWarn("telnet", "wrong password from %s", host)
		time.Sleep(telnetFailDelay)
		if try >= telnetMaxTries || s.fails.lockedOut(host, time.Now()) {
			_, _ = io.WriteString(conn, "\r\nToo many wrong passwords.\r\n")
			return
		}
		_, _ = io.WriteString(conn, "\r\nWrong password.\r\n")
	}
	_, _ = io.WriteString(conn, "\r\n")
	_ = conn.SetDeadline(time.Time{})

	sizeMu.Lock()
	c, r := cols, rows
	sizeMu.Unlock()
	started, err := termPtySpawn(cmd, env, dir, c, r)
	if err != nil {
		_, _ = io.WriteString(conn, "No shell could start: "+err.Error()+"\r\n")
		logWarn("telnet", "shell for %s did not start: %v", host, err)
		return
	}
	sizeMu.Lock()
	proc = started
	sizeMu.Unlock()
	id := s.clients.add(conn.RemoteAddr().String())
	s.clients.setUser(id, "shell")
	defer s.clients.remove(id)
	logInfo("telnet", "shell session for %s", host)

	// The shell's output goes to the client with every 0xFF doubled; when
	// the shell ends, the connection ends with it.
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		defer conn.Close()
		buf := make([]byte, 16<<10)
		var esc []byte
		for {
			n, rerr := started.Read(buf)
			if n > 0 {
				s.activity.touch()
				esc = telnetEscapeIAC(esc[:0], buf[:n])
				_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if _, werr := conn.Write(esc); werr != nil {
					// The client is gone. The terminal is still read until
					// it closes: on Windows ClosePseudoConsole (Kill) waits
					// for its output to be read (termpty_windows.go).
					_ = conn.Close()
					for {
						if _, rerr := started.Read(buf); rerr != nil {
							return
						}
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	// What the client types goes to the shell, negotiation stripped. A
	// goroutine of its own writes it: a program that reads nothing (a paste
	// into `sleep`) blocks that write, and this loop must still see the
	// connection end so the shell is hung up. Past telnetInQueue chunks
	// waiting, typing is dropped, as the Terminal's queue does.
	typed := make(chan []byte, telnetInQueue)
	go func() {
		for b := range typed {
			if _, werr := started.Write(b); werr != nil {
				return
			}
		}
	}()
	buf := make([]byte, 4096)
	for {
		n, rerr := br.Read(buf)
		if n > 0 {
			s.activity.touch()
			if data := p.feed(buf[:n], nil); len(data) > 0 {
				select {
				case typed <- data:
				default:
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	close(typed)
	// Kill closes the terminal too, which ends a write still waiting.
	started.Kill(termPtyKillGrace)
	select {
	case <-outDone:
	case <-time.After(3 * time.Second):
	}
	go func() { _, _ = started.Wait() }()
	logInfo("telnet", "session for %s ended", host)
}

// telnetPasswordOK compares a typed password with this run's in constant
// time (through the SSH server's per-process digest). An empty password
// never matches.
func telnetPasswordOK(typed, password string) bool {
	if password == "" {
		return false
	}
	return subtle.ConstantTimeCompare(sshPasswordDigest(typed), sshPasswordDigest(password)) == 1
}

// telnetReadLine reads one typed line through p without echoing it: CR or
// LF ends it, Backspace and Delete take a character back, Ctrl+C gives up.
// At most max characters are kept.
func telnetReadLine(r io.ByteReader, p *telnetParser, max int) (string, error) {
	var line, data []byte
	var one [1]byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		one[0] = b
		data = p.feed(one[:], data[:0])
		for _, c := range data {
			switch {
			case c == '\r' || c == '\n':
				return string(line), nil
			case c == 0x7f || c == 0x08:
				if len(line) > 0 {
					line = line[:len(line)-1]
				}
			case c == 0x03:
				return "", errors.New("sign-in interrupted")
			case c >= 0x20 && len(line) < max:
				line = append(line, c)
			}
		}
	}
}

// telnetEscapeIAC appends src to dst with every 0xFF doubled, as data must
// travel.
func telnetEscapeIAC(dst, src []byte) []byte {
	for _, b := range src {
		if b == telnetIAC {
			dst = append(dst, telnetIAC)
		}
		dst = append(dst, b)
	}
	return dst
}

// ── Protocol ──────────────────────────────────────────────────────────────

// The parser's states.
const (
	telnetStData  = iota
	telnetStIAC   // after IAC
	telnetStOpt   // after WILL / WONT / DO / DONT: the option byte
	telnetStSB    // inside SB … SE
	telnetStSBIAC // an IAC inside SB
)

// telnetParser takes the telnet layer off what a client sends: data comes
// out of feed, window sizes go to onSize and negotiation answers to reply.
type telnetParser struct {
	state  uint8
	verb   byte
	cr     bool // the last data byte was CR
	sb     []byte
	sbOver bool
	// will: options the server does (ECHO, SGA). wontSent / dontSent: a
	// refusal already answered, so a client repeating its request cannot
	// start a loop.
	will     [256]bool
	wontSent [256]bool
	dontSent [256]bool
	onSize   func(cols, rows int)
	reply    func([]byte)
}

// newTelnetParser is a parser for a server that sent telnetGreeting.
func newTelnetParser() *telnetParser {
	p := &telnetParser{}
	p.will[telnetOptEcho], p.will[telnetOptSGA] = true, true
	return p
}

// feed appends the data in in to out and returns it: IAC sequences removed
// (IAC IAC is a data 0xFF), CR NUL and CR LF folded to CR, Interrupt Process
// turned into Ctrl+C. State carries over between calls, so a sequence may be
// split anywhere.
func (p *telnetParser) feed(in, out []byte) []byte {
	for _, b := range in {
		switch p.state {
		case telnetStData:
			if b == telnetIAC {
				p.state = telnetStIAC
				continue
			}
			if p.cr {
				p.cr = false
				if b == 0 || b == '\n' {
					continue
				}
			}
			p.cr = b == '\r'
			out = append(out, b)
		case telnetStIAC:
			p.state = telnetStData
			switch b {
			case telnetIAC:
				p.cr = false
				out = append(out, telnetIAC)
			case telnetWILL, telnetWONT, telnetDO, telnetDONT:
				p.verb, p.state = b, telnetStOpt
			case telnetSB:
				p.sb, p.sbOver, p.state = p.sb[:0], false, telnetStSB
			case telnetIP:
				out = append(out, 0x03)
			}
			// NOP, Data Mark, Go Ahead, Are You There… carry nothing a shell
			// needs.
		case telnetStOpt:
			p.state = telnetStData
			p.negotiate(p.verb, b)
		case telnetStSB:
			if b == telnetIAC {
				p.state = telnetStSBIAC
				continue
			}
			p.sbAdd(b)
		case telnetStSBIAC:
			switch b {
			case telnetSE:
				p.state = telnetStData
				p.subnegotiation()
			case telnetIAC:
				p.state = telnetStSB
				p.sbAdd(telnetIAC)
			default:
				// Neither IAC IAC nor IAC SE: a broken subnegotiation. It is
				// dropped and the stream read on as data.
				p.state = telnetStData
			}
		}
	}
	return out
}

// sbAdd keeps one subnegotiation byte, up to telnetSBMax.
func (p *telnetParser) sbAdd(b byte) {
	if len(p.sb) >= telnetSBMax {
		p.sbOver = true
		return
	}
	p.sb = append(p.sb, b)
}

// subnegotiation acts on a finished SB: NAWS's width and height.
func (p *telnetParser) subnegotiation() {
	if p.sbOver || len(p.sb) != 5 || p.sb[0] != telnetOptNAWS {
		return
	}
	cols := int(p.sb[1])<<8 | int(p.sb[2])
	rows := int(p.sb[3])<<8 | int(p.sb[4])
	if cols > 0 && rows > 0 && p.onSize != nil {
		p.onSize(min(cols, telnetMaxSide), min(rows, telnetMaxSide))
	}
}

// negotiate answers one option request. The server does ECHO and SGA
// (already said in the greeting) and wants NAWS from the client (asked
// for); everything else is refused, once.
func (p *telnetParser) negotiate(verb, opt byte) {
	switch verb {
	case telnetDO:
		if !p.will[opt] && !p.wontSent[opt] {
			p.wontSent[opt] = true
			p.send(telnetWONT, opt)
		}
	case telnetDONT:
		if p.will[opt] {
			p.will[opt] = false
			p.send(telnetWONT, opt)
		}
	case telnetWILL:
		if opt != telnetOptNAWS && !p.dontSent[opt] {
			p.dontSent[opt] = true
			p.send(telnetDONT, opt)
		}
	}
}

// send writes one negotiation answer.
func (p *telnetParser) send(verb, opt byte) {
	if p.reply != nil {
		p.reply([]byte{telnetIAC, verb, opt})
	}
}
