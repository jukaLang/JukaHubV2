package main

// smbserve.go — the SMB share: the handheld's SD card as a network drive on
// Windows, macOS and Linux.
//
// FTP and SFTP need a client program; WebDAV (davshare) is built into every
// desktop but Windows' WebDAV client refuses files over 50 MB. SMB is what
// File Explorer and Finder speak natively, so a whole ROM set or a 4 GB disc
// image copies like it would to a USB stick.
//
// The server is pure Go (no Samba binary) and small on purpose:
//
//   - SMB 2.0.2 and 2.1 only (smbserve_conn.go), signing required on every
//     session (HMAC-SHA256 keyed by the NTLMv2 session key), so it works
//     with Windows 11 24H2, which refuses unsigned shares. No SMB1, no 3.x
//     encryption: the share is for the home network, like FTP and SFTP.
//   - One account, "jukahub", with a generated 12-character password kept in
//     the app data folder (0600) so Windows can remember it; "New password"
//     on the Network tools sheet replaces it and drops every connection.
//     Anonymous and guest sign-ins are refused, a wrong password waits a
//     second, and ten wrong ones in a minute lock the address out for a
//     minute (smbSrvLimiter).
//   - It answers private, loopback, link-local and tailnet (100.64.0.0/10)
//     addresses only, at most smbSrvMaxConns connections. Off by default,
//     and it stops itself like FTP/SFTP after fileServerIdleLimit unused and
//     when the device joins another network (smbServeWatch reuses
//     fileserver_guard.go's fsWatcher on its own goroutine).
//   - Share "JukaHub" is the SD card (fileServerCardRoot, SFTP's root),
//     confined with os.Root; "Read-only" (smb_readonly) refuses every write.
//   - The NetBIOS name service (smbserve_nbns.go) answers for the device
//     name, so \\NAME\JukaHub works as well as \\IP\JukaHub.
//
// Port 445 (and 137) need root, which JukaHub has on the handheld; on
// Windows the system owns 445, so smbserve_other.go says "handheld only".

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	smbServeUser      = "jukahub"
	smbServeShareName = "JukaHub"
	smbServePort      = 445
	smbServePassLen   = 12
	smbServePassFile  = "smb_password"
	smbServeROKey     = "smb_readonly"

	smbSrvMaxConns    = 4
	smbSrvMaxFails    = 10
	smbSrvFailWindow  = time.Minute
	smbSrvLockout     = time.Minute
	smbSrvMaxAddrs    = 64
	smbSrvLoginWait   = 60 * time.Second
	smbSrvIdleTimeout = 15 * time.Minute
	smbSrvWriteWait   = 30 * time.Second
)

// smbSrvFailDelay is how long a wrong password waits for its answer; a
// variable so the tests can run without the wait.
var smbSrvFailDelay = time.Second

// smbService is the running SMB server. Use smbSrv.
type smbService struct {
	mu       sync.Mutex
	ln       net.Listener
	nbns     net.PacketConn
	root     *os.Root
	dir      string
	conns    map[net.Conn]struct{}
	gen      uint64
	netBase  fsNetIdentity
	lastErr  string
	nbName   string
	guid     [16]byte
	password string
	clients  fsClients
	activity fsActivity
	limiter  smbSrvLimiter
	readOnly atomic.Bool
	sessSeq  atomic.Uint64
}

var smbSrv = &smbService{}

func init() { bootOnShutdown(smbServeShutdown) }

// smbServeShutdown closes the share when JukaHub exits.
func smbServeShutdown() { smbSrv.Stop() }

// Running reports whether the listener is open.
func (s *smbService) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ln != nil
}

// ReadOnly reports whether writes are refused (the switch, live).
func (s *smbService) ReadOnly() bool { return s.readOnly.Load() }

// LastError is the last failed start, if any.
func (s *smbService) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Clients lists the open connections for the Network tools sheet.
func (s *smbService) Clients() []fsClient { return s.clients.list() }

// NetBIOSName is the name the share answers to ("" while stopped).
func (s *smbService) NetBIOSName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil || s.nbns == nil {
		return ""
	}
	return s.nbName
}

// Address is what Windows' File Explorer takes: \\IP\JukaHub ("" while
// stopped or offline).
func (s *smbService) Address() string {
	if !s.Running() {
		return ""
	}
	if ip := localIPv4(); ip != "" {
		return `\\` + ip + `\` + smbServeShareName
	}
	return ""
}

// MacAddress is the same share for Finder's Connect to Server and Linux file
// managers: smb://IP/JukaHub.
func (s *smbService) MacAddress() string {
	if !s.Running() {
		return ""
	}
	if ip := localIPv4(); ip != "" {
		return "smb://" + ip + "/" + smbServeShareName
	}
	return ""
}

// Password is the share's password, made on first use and kept in the app
// data folder. It is shown on the device only: never logged or published.
func (s *smbService) Password() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.password == "" {
		pw, err := smbServeLoadPassword()
		if err != nil {
			logWarn("smb", "password: %v", err)
			return ""
		}
		s.password = pw
	}
	return s.password
}

// NewPassword replaces the password and ends every connection, so a PC that
// remembered the old one has to sign in again.
func (s *smbService) NewPassword() error {
	pw, err := smbServeNewPassword()
	if err != nil {
		return err
	}
	if err := AtomicWrite(smbServePassPath(), []byte(pw+"\n"), 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	s.password = pw
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return nil
}

// smbServePassPath is where the password lives (next to SFTP's host key).
func smbServePassPath() string { return filepath.Join(sshKeyDir(), smbServePassFile) }

// smbServeLoadPassword reads the kept password, making one when there is
// none (or the file holds something that is not one of ours).
func smbServeLoadPassword() (string, error) {
	if b, err := os.ReadFile(smbServePassPath()); err == nil {
		if pw := strings.TrimSpace(string(b)); len(pw) >= 8 && len(pw) <= 64 {
			return pw, nil
		}
	}
	pw, err := smbServeNewPassword()
	if err != nil {
		return "", err
	}
	if err := AtomicWrite(smbServePassPath(), []byte(pw+"\n"), 0o600); err != nil {
		return "", err
	}
	return pw, nil
}

// smbServeNewPassword is smbServePassLen characters from an alphabet without
// look-alikes (0/O, 1/l/I), drawn without modulo bias.
func smbServeNewPassword() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	const limit = 256 - 256%len(alphabet)
	out := make([]byte, 0, smbServePassLen)
	var buf [32]byte
	for len(out) < smbServePassLen {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		for _, v := range buf {
			if int(v) < limit && len(out) < smbServePassLen {
				out = append(out, alphabet[int(v)%len(alphabet)])
			}
		}
	}
	return string(out), nil
}

// smbServeNetBIOSName turns the device name (peer.go) into a NetBIOS name:
// upper case, letters, digits and dashes, at most 15 characters.
func smbServeNetBIOSName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 15 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 15 {
		out = strings.TrimRight(out[:15], "-")
	}
	if out == "" {
		return "JUKAHUB"
	}
	return out
}

// smbServePeerAllowed reports whether ip may connect: the home network
// (private, link-local), this device, and tailnet addresses (100.64.0.0/10;
// the Tailscale userspace daemon forwards from loopback).
func smbServePeerAllowed(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && ip4[1]&0xc0 == 64 {
			return true
		}
		ip = ip4
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// smbServeReadOnlySetting is the saved switch. Reads the config: render
// thread only.
func smbServeReadOnlySetting() bool {
	if appConfig == nil {
		return false
	}
	return customBool(appConfig.Variables.Custom, smbServeROKey, false)
}

// Start shares the SD card on port 445 and starts the name service and the
// watchdog. The read-only switch is set by the caller (toggleSMBServer).
func (s *smbService) Start() error {
	if s.Running() {
		return nil
	}
	if err := smbServeSupported(); err != nil {
		return err
	}
	dir := fileServerCardRoot()
	if dir == "" {
		return errors.New("no SD card to share was found")
	}
	pw := s.Password()
	if pw == "" {
		return errors.New("the share's password could not be saved")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", smbServePort))
	if err != nil {
		_ = root.Close()
		msg := err.Error()
		if strings.Contains(msg, "address already in use") {
			msg = "port 445 is taken — another SMB server (Samba) already runs on this device"
		}
		s.mu.Lock()
		s.lastErr = msg
		s.mu.Unlock()
		return errors.New(msg)
	}
	name := smbServeNetBIOSName(peerDeviceName())
	nb, nbErr := net.ListenPacket("udp4", "0.0.0.0:137")
	if nbErr != nil {
		logInfo("smb", "NetBIOS name service off: %v", nbErr)
		nb = nil
	}
	var guid [16]byte
	_, _ = rand.Read(guid[:])
	s.limiter.reset()
	s.mu.Lock()
	s.ln = ln
	s.nbns = nb
	s.root = root
	s.dir = dir
	s.conns = make(map[net.Conn]struct{})
	s.gen++
	gen := s.gen
	s.netBase = fsNetIdentity{ip: localIPv4()}
	s.lastErr = ""
	s.nbName = name
	s.guid = guid
	s.mu.Unlock()
	s.activity.touch()
	logInfo("smb", "sharing %s as \\\\%s\\%s (read-only %v)", dir, name, smbServeShareName, s.ReadOnly())
	go s.acceptLoop(ln, root, gen)
	if nb != nil {
		go smbServeNBNS(nb, name)
	}
	go smbServeWatch(gen)
	return nil
}

// Stop closes the listener, the name service, every connection and the root.
func (s *smbService) Stop() {
	s.mu.Lock()
	s.stopLocked()
}

// stopRun stops run gen only (the watchdog's stop, which must not hit a
// newer start).
func (s *smbService) stopRun(gen uint64) bool {
	s.mu.Lock()
	if s.ln == nil || s.gen != gen {
		s.mu.Unlock()
		return false
	}
	s.stopLocked()
	return true
}

// stopLocked is Stop for a caller holding s.mu, which it releases.
func (s *smbService) stopLocked() {
	ln, nb, root := s.ln, s.nbns, s.root
	s.ln, s.nbns, s.root = nil, nil, nil
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
	if nb != nil {
		_ = nb.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	if root != nil {
		// Open handles hold their own files; closing the root only stops
		// new lookups, which the closed connections no longer make.
		_ = root.Close()
		logInfo("smb", "stopped")
	}
}

// watchTarget is the server as fsWatcher sees it.
func (s *smbService) watchTarget() fsWatchTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fsWatchTarget{running: s.ln != nil, gen: s.gen, base: s.netBase, lastUsed: s.activity.lastUsed()}
}

// acceptLoop takes connections for run gen until its listener closes.
func (s *smbService) acceptLoop(ln net.Listener, root *os.Root, gen uint64) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		ip := ""
		if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); ok && smbServePeerAllowed(tcp.IP) {
			ip = tcp.IP.String()
		}
		if ip == "" || s.limiter.locked(ip) {
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.ln != ln || s.gen != gen {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		if len(s.conns) >= smbSrvMaxConns {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		name, guid := s.nbName, s.guid
		s.mu.Unlock()
		go func() {
			id := s.clients.add(conn.RemoteAddr().String())
			defer func() {
				if r := recover(); r != nil {
					logError("smb", "connection from %s: %v", ip, r)
				}
				s.clients.remove(id)
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				_ = conn.Close()
			}()
			c := newSmbSrvConn(s, conn, root, ip, id, name, guid)
			c.serve()
		}()
	}
}

// smbServeWatch stops run gen when nobody used it for fileServerIdleLimit or
// the device left the network it started on — fileserver_guard.go's rules,
// decided by the same fsWatcher, on a goroutine of its own.
func smbServeWatch(gen uint64) {
	defer func() {
		if r := recover(); r != nil {
			logError("smb", "watchdog stopped: %v", r)
			// Fail closed: a share nobody watches would outlive a move to a
			// café's network.
			if smbSrv.stopRun(gen) {
				ShowToast(fsStopMessage([]string{"SMB"}, fsStopUnwatched), ToastKindWarning)
			}
		}
	}()
	w := &fsWatcher{idleLimit: fileServerIdleLimit}
	ticker := time.NewTicker(fileServerWatchInterval)
	defer ticker.Stop()
	for range ticker.C {
		t := smbSrv.watchTarget()
		if !t.running || t.gen != gen {
			return
		}
		reason := w.decide(time.Now(), fsReadNetIdentity(), []fsWatchTarget{t})[0]
		if reason == "" || !smbSrv.stopRun(gen) {
			continue
		}
		logInfo("smb", "SMB server stopped: %s", reason)
		kind := ToastKindWarning
		if reason == fsStopIdle {
			kind = ToastKindInfo
		}
		ShowToast(fsStopMessage([]string{"SMB"}, reason), kind)
		return
	}
}

// ── Sign-in limiter ───────────────────────────────────────────────────────

// smbSrvLimiter locks an address out for smbSrvLockout after smbSrvMaxFails
// wrong passwords within smbSrvFailWindow.
type smbSrvLimiter struct {
	mu   sync.Mutex
	byIP map[string]*smbSrvFails
}

type smbSrvFails struct {
	n     int
	first time.Time
	until time.Time
}

func (l *smbSrvLimiter) locked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.byIP[ip]
	return rec != nil && time.Now().Before(rec.until)
}

func (l *smbSrvLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.byIP == nil {
		l.byIP = make(map[string]*smbSrvFails)
	}
	rec := l.byIP[ip]
	if rec == nil {
		if len(l.byIP) >= smbSrvMaxAddrs {
			for k, r := range l.byIP {
				if !now.Before(r.until) && now.Sub(r.first) > smbSrvFailWindow {
					delete(l.byIP, k)
				}
			}
			if len(l.byIP) >= smbSrvMaxAddrs {
				return
			}
		}
		rec = &smbSrvFails{first: now}
		l.byIP[ip] = rec
	}
	if now.Sub(rec.first) > smbSrvFailWindow {
		rec.n, rec.first = 0, now
	}
	rec.n++
	if rec.n >= smbSrvMaxFails {
		rec.until = now.Add(smbSrvLockout)
		rec.n, rec.first = 0, rec.until
		logInfo("smb", "too many wrong passwords from %s: refused for a minute", ip)
	}
}

func (l *smbSrvLimiter) succeed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

func (l *smbSrvLimiter) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byIP = nil
}
