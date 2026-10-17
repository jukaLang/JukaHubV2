package main

// ftp_server.go — a small, dependency-free FTP server so files can be dropped
// onto the handheld from a PC on the same WiFi network.
//
// Design notes:
//   - Passive mode only (PASV/EPSV). The active PORT/EPRT commands are refused,
//     which also removes the FTP bounce class of attacks.
//   - Login is accepted for any USER/PASS pair: the server is meant for a home
//     LAN and is only started explicitly from Tools. The UI warns that anyone
//     on the network can read/write while it runs.
//   - Every path is resolved inside the shared root; ".." is clamped and
//     symlinks that point outside the root are refused.
//   - The root defaults to the SD card on the TrimUI and to the application
//     folder elsewhere. JUKAHUB_FTP_ROOT overrides it.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ftpDefaultPort  = 2121
	ftpMaxConns     = 8
	ftpMaxLineBytes = 4096
	ftpIdleTimeout  = 30 * time.Minute
	ftpAcceptWait   = 60 * time.Second
	ftpMaxListing   = 4 << 20
)

var errFTPBadPath = errors.New("path is outside the shared folder")

// ftpService is the running FTP server. Zero value is not usable; use ftpSrv.
type ftpService struct {
	mu      sync.Mutex
	ln      net.Listener
	root    string
	port    int
	lastErr string
	conns   map[net.Conn]struct{}
}

var ftpSrv = &ftpService{conns: make(map[net.Conn]struct{})}

// Running reports whether the listener is open.
func (s *ftpService) Running() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ln != nil
}

// Root returns the folder being shared ("" before the first start).
func (s *ftpService) Root() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root != "" {
		return s.root
	}
	return ftpRootDir()
}

// Port returns the listening port (the default before the first start).
func (s *ftpService) Port() int {
	if s == nil {
		return ftpDefaultPort
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port > 0 {
		return s.port
	}
	return ftpPort()
}

// LastError returns the most recent start error, if any.
func (s *ftpService) LastError() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Address is the URL a user should type on their PC. The host is the current
// LAN address, so it follows WiFi changes without restarting the server.
func (s *ftpService) Address() string {
	if !s.Running() {
		return ""
	}
	host := localIPv4()
	if host == "" {
		// No LAN address (WiFi dropped): do not advertise loopback, the PC
		// could never reach it.
		return ""
	}
	return fmt.Sprintf("ftp://%s:%d", host, s.Port())
}

// Start begins serving the default root on all interfaces.
func (s *ftpService) Start() error {
	if s.Running() {
		return nil
	}
	return s.startAt(ftpRootDir(), fmt.Sprintf("0.0.0.0:%d", ftpPort()))
}

// startAt is Start with an explicit root and listen address (used by tests).
func (s *ftpService) startAt(root, addr string) error {
	if root == "" {
		return errors.New("FTP shared folder is unavailable")
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Errorf("shared folder %s is not available", root)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		if s == ftpSrv {
			publishFTPState()
		}
		return fmt.Errorf("start FTP server on %s: %w", addr, err)
	}
	s.mu.Lock()
	if s.conns == nil {
		s.conns = make(map[net.Conn]struct{})
	}
	s.ln = ln
	s.root = root
	s.lastErr = ""
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tcp.Port
	}
	s.mu.Unlock()

	if s == ftpSrv {
		publishFTPState()
	}
	log.Printf("[FTP] serving %q on %s", root, ln.Addr())
	go s.acceptLoop(ln)
	return nil
}

// Stop closes the listener and every open control connection.
func (s *ftpService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	ln := s.ln
	s.ln = nil
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.conns = make(map[net.Conn]struct{})
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	if ln != nil {
		log.Printf("[FTP] stopped")
	}
	if s == ftpSrv {
		publishFTPState()
	}
}

func (s *ftpService) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.ln == nil {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		if len(s.conns) >= ftpMaxConns {
			s.mu.Unlock()
			_, _ = io.WriteString(conn, "421 Too many connections, try again later\r\n")
			_ = conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		root := s.root
		s.mu.Unlock()

		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				_ = conn.Close()
			}()
			serveFTPSession(conn, root)
		}()
	}
}

// publishFTPState mirrors the server state into custom variables so config
// scenes (and the Tools panel) can display it.
func publishFTPState() {
	status := "stopped"
	addr := ""
	if ftpSrv.Running() {
		status = "running"
		addr = ftpSrv.Address()
	}
	publishCustom("ftp_status", status)
	publishCustom("ftp_address", addr)
	publishCustom("ftp_root", ftpSrv.Root())
	publishCustom("local_ip", localIPv4())
}

// initToolsModule publishes the initial network/FTP values at startup.
func initToolsModule() {
	publishFTPState()
}

// ftpPort returns the configured port. A numeric "ftp_port" custom variable
// wins over JUKAHUB_FTP_PORT, which wins over 2121.
func ftpPort() int {
	if appConfig != nil {
		if raw, ok := customString(appConfig.Variables.Custom, "ftp_port"); ok {
			if p, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && p > 0 && p < 65536 {
				return p
			}
		}
	}
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_FTP_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return ftpDefaultPort
}

// ftpRootDir picks the folder to share: the SD card on the handheld, then the
// application folder. JUKAHUB_FTP_ROOT overrides both.
func ftpRootDir() string {
	if custom := strings.TrimSpace(os.Getenv("JUKAHUB_FTP_ROOT")); custom != "" {
		if info, err := os.Stat(custom); err == nil && info.IsDir() {
			return custom
		}
	}
	for _, candidate := range []string{"/mnt/SDCARD", "/sdcard"} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	// On a desktop the app folder holds jukauser.json and saved credentials.
	// Share a dedicated subfolder instead of exposing the whole install to the
	// LAN; create it so switching FTP on just works.
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), "shared")
		if err := os.MkdirAll(dir, 0o755); err == nil {
			return dir
		}
		return filepath.Dir(exe)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// localIPv4 returns the device's LAN IPv4 address, preferring wireless
// interfaces and ignoring loopback/link-local addresses.
func localIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	best := ""
	bestScore := -1
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(iface.Name)
		score := 0
		switch {
		case strings.HasPrefix(name, "wl"):
			score = 3
		case strings.HasPrefix(name, "en"), strings.HasPrefix(name, "eth"):
			score = 2
		case strings.HasPrefix(name, "usb"), strings.HasPrefix(name, "rndis"):
			score = 1
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			if score > bestScore {
				best, bestScore = ip4.String(), score
			}
		}
	}
	return best
}

// ──────────────────────────────────────────────────────────────────────
// Session
// ──────────────────────────────────────────────────────────────────────

type ftpSession struct {
	conn   net.Conn
	r      *bufio.Reader
	root   string
	cwd    string // virtual path, always "/..." (FTP separators)
	user   string
	authed bool

	dataLn net.Listener
	rest   int64
	rnfr   string
}

func serveFTPSession(conn net.Conn, root string) {
	sess := &ftpSession{
		conn: conn,
		r:    bufio.NewReaderSize(conn, ftpMaxLineBytes),
		root: root,
		cwd:  "/",
	}
	sess.reply("220 JukaHub FTP ready")
	for {
		_ = conn.SetReadDeadline(time.Now().Add(ftpIdleTimeout))
		line, err := sess.r.ReadString('\n')
		if err != nil {
			sess.closeData()
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) > ftpMaxLineBytes {
			sess.reply("500 Command too long")
			continue
		}
		cmd, arg := splitFTPCommand(line)
		if cmd == "" {
			continue
		}
		if !sess.handle(cmd, arg) {
			sess.closeData()
			return
		}
	}
}

func splitFTPCommand(line string) (string, string) {
	line = strings.TrimLeft(line, " \t")
	if line == "" {
		return "", ""
	}
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		return strings.ToUpper(line[:i]), strings.TrimSpace(line[i+1:])
	}
	return strings.ToUpper(line), ""
}

func (s *ftpSession) reply(format string, args ...interface{}) {
	_ = s.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, _ = fmt.Fprintf(s.conn, format+"\r\n", args...)
}

func (s *ftpSession) closeData() {
	if s.dataLn != nil {
		_ = s.dataLn.Close()
		s.dataLn = nil
	}
}

// openData accepts the passive data connection the client opened after
// PASV/EPSV. The listener is single-use, as the RFC intends.
func (s *ftpSession) openData() (net.Conn, error) {
	if s.dataLn == nil {
		return nil, errors.New("use PASV or EPSV first")
	}
	ln := s.dataLn
	s.dataLn = nil
	if tl, ok := ln.(*net.TCPListener); ok {
		_ = tl.SetDeadline(time.Now().Add(ftpAcceptWait))
	}
	conn, err := ln.Accept()
	_ = ln.Close()
	return conn, err
}

func (s *ftpSession) handle(cmd, arg string) bool {
	switch cmd {
	case "USER":
		s.user = arg
		s.reply("331 Any password will do — this server is for your local network")
	case "PASS":
		s.authed = true
		s.reply("230 Logged in")
	case "QUIT":
		s.reply("221 Bye")
		return false
	case "NOOP":
		s.reply("200 OK")
	case "SYST":
		s.reply("215 UNIX Type: L8")
	case "FEAT":
		s.reply("211-Features")
		_, _ = io.WriteString(s.conn, " EPSV\r\n PASV\r\n SIZE\r\n MDTM\r\n REST STREAM\r\n UTF8\r\n211 End\r\n")
	case "OPTS":
		s.reply("200 UTF8 enabled")
	case "TYPE":
		s.reply("200 Binary mode")
	case "MODE":
		if strings.EqualFold(strings.TrimSpace(arg), "S") {
			s.reply("200 Stream mode")
		} else {
			s.reply("504 Only stream mode is supported")
		}
	case "STRU":
		if strings.EqualFold(strings.TrimSpace(arg), "F") {
			s.reply("200 File structure")
		} else {
			s.reply("504 Only file structure is supported")
		}
	case "PWD", "XPWD":
		s.reply(`257 "%s" is the current directory`, s.cwd)
	case "CWD", "XCWD":
		if !s.requireAuth() {
			return true
		}
		s.changeDir(arg)
	case "CDUP", "XCUP":
		if !s.requireAuth() {
			return true
		}
		s.changeDir("..")
	case "PASV":
		if !s.requireAuth() {
			return true
		}
		s.enterPassive(false)
	case "EPSV":
		if !s.requireAuth() {
			return true
		}
		s.enterPassive(true)
	case "PORT", "EPRT":
		s.reply("502 Active mode is disabled — use PASV or EPSV")
	case "LIST", "NLST":
		if !s.requireAuth() {
			return true
		}
		s.list(arg, cmd == "NLST")
	case "RETR":
		if !s.requireAuth() {
			return true
		}
		s.retrieve(arg)
	case "STOR":
		if !s.requireAuth() {
			return true
		}
		s.store(arg)
	case "SIZE":
		if !s.requireAuth() {
			return true
		}
		s.fileSize(arg)
	case "MDTM":
		if !s.requireAuth() {
			return true
		}
		s.fileModTime(arg)
	case "DELE":
		if !s.requireAuth() {
			return true
		}
		s.deleteFile(arg)
	case "MKD", "XMKD":
		if !s.requireAuth() {
			return true
		}
		s.makeDir(arg)
	case "RMD", "XRMD":
		if !s.requireAuth() {
			return true
		}
		s.removeDir(arg)
	case "RNFR":
		if !s.requireAuth() {
			return true
		}
		s.renameFrom(arg)
	case "RNTO":
		if !s.requireAuth() {
			return true
		}
		s.renameTo(arg)
	case "REST":
		if !s.requireAuth() {
			return true
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64); err == nil && n >= 0 {
			s.rest = n
			s.reply("350 Restarting at %d", n)
		} else {
			s.reply("501 Invalid restart position")
		}
	case "ABOR":
		s.closeData()
		s.reply("226 Abort successful")
	default:
		s.reply("502 Command not implemented")
	}
	return true
}

func (s *ftpSession) requireAuth() bool {
	if s.authed {
		return true
	}
	s.reply("530 Please log in first")
	return false
}

func (s *ftpSession) changeDir(name string) {
	target, err := s.resolve(name)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		s.reply("550 Not a directory")
		return
	}
	rel, err := filepath.Rel(s.root, target)
	if err != nil {
		s.reply("550 Not a directory")
		return
	}
	v := "/" + filepath.ToSlash(rel)
	if rel == "." {
		v = "/"
	}
	s.cwd = path.Clean(v)
	s.reply(`250 "%s" is the current directory`, s.cwd)
}

func (s *ftpSession) enterPassive(extended bool) {
	s.closeData()
	host := s.controlLocalIP()
	bind := "0.0.0.0:0"
	if host != nil && !host.IsUnspecified() {
		bind = net.JoinHostPort(host.String(), "0")
	}
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		// The interface may have changed since accept; fall back to any.
		ln, err = net.Listen("tcp", "0.0.0.0:0")
	}
	if err != nil {
		s.reply("425 Could not open a passive port")
		return
	}
	s.dataLn = ln
	port := ln.Addr().(*net.TCPAddr).Port
	if extended {
		s.reply("229 Entering Extended Passive Mode (|||%d|)", port)
		return
	}
	advertise := host
	if advertise == nil || advertise.IsUnspecified() {
		advertise = net.ParseIP("127.0.0.1")
	}
	v4 := advertise.To4()
	if v4 == nil {
		v4 = net.IPv4(127, 0, 0, 1).To4()
	}
	s.reply("227 Entering Passive Mode (%d,%d,%d,%d,%d,%d)",
		v4[0], v4[1], v4[2], v4[3], port/256, port%256)
}

func (s *ftpSession) controlLocalIP() net.IP {
	if addr, ok := s.conn.LocalAddr().(*net.TCPAddr); ok {
		return addr.IP
	}
	return nil
}

func (s *ftpSession) list(arg string, namesOnly bool) {
	// Clients ask for LIST -a or LIST -la when hidden files are enabled. The
	// flags only describe the listing, not a path: drop them instead of
	// answering 550 for a file literally called "-a".
	if strings.HasPrefix(arg, "-") {
		if i := strings.IndexByte(arg, ' '); i >= 0 {
			arg = strings.TrimSpace(arg[i+1:])
		} else {
			arg = ""
		}
	}
	conn, err := s.openData()
	if err != nil {
		s.reply("425 Use PASV or EPSV first")
		return
	}
	defer conn.Close()

	target, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		s.reply("550 No such file or directory")
		return
	}

	var b strings.Builder
	add := func(fi os.FileInfo, name string) bool {
		if b.Len() > ftpMaxListing {
			return false
		}
		if namesOnly {
			b.WriteString(name)
			b.WriteString("\r\n")
		} else {
			b.WriteString(ftpListLine(fi, name))
		}
		return true
	}

	if info.IsDir() {
		if !namesOnly {
			add(info, ".")
			if parent, err := os.Stat(filepath.Dir(target)); err == nil {
				add(parent, "..")
			}
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			s.reply("550 Cannot read directory")
			return
		}
		for _, ent := range entries {
			fi, err := ent.Info()
			if err != nil {
				continue
			}
			if !add(fi, ent.Name()) {
				break
			}
		}
	} else {
		add(info, filepath.Base(target))
	}

	s.reply("150 Here comes the directory listing")
	_ = conn.SetWriteDeadline(time.Now().Add(ftpAcceptWait))
	if _, err := io.WriteString(conn, b.String()); err != nil {
		log.Printf("[FTP] listing write failed: %v", err)
		s.reply("426 Transfer aborted")
		return
	}
	s.reply("226 Directory send OK")
}

func ftpListLine(fi os.FileInfo, name string) string {
	mode := fi.Mode().Perm().String()
	return fmt.Sprintf("%s 1 owner group %d %s %s\r\n",
		mode, fi.Size(), fi.ModTime().Format("Jan _2 15:04"), name)
}

func (s *ftpSession) retrieve(arg string) {
	target, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	f, err := os.Open(target)
	if err != nil {
		s.reply("550 Cannot open file")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		s.reply("550 Not a file")
		return
	}
	conn, err := s.openData()
	if err != nil {
		s.reply("425 Use PASV or EPSV first")
		return
	}
	defer conn.Close()

	offset := s.rest
	s.rest = 0
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			s.reply("550 Cannot seek")
			return
		}
	}
	s.reply("150 Opening data connection (%d bytes)", max64(info.Size()-offset, 0))
	_ = conn.SetWriteDeadline(time.Now().Add(ftpIdleTimeout))
	if _, err := io.Copy(conn, f); err != nil {
		log.Printf("[FTP] RETR %q failed: %v", path.Base(target), err)
		s.reply("426 Transfer aborted")
		return
	}
	s.reply("226 Transfer complete")
}

func (s *ftpSession) store(arg string) {
	target, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		s.reply("550 Is a directory")
		return
	}
	if _, err := os.Stat(filepath.Dir(target)); err != nil {
		s.reply("550 Target folder does not exist")
		return
	}
	conn, err := s.openData()
	if err != nil {
		s.reply("425 Use PASV or EPSV first")
		return
	}
	defer conn.Close()

	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		s.reply("550 Cannot write file")
		return
	}
	s.reply("150 Ok to send data")
	_ = conn.SetReadDeadline(time.Now().Add(ftpIdleTimeout))
	n, copyErr := io.Copy(f, conn)
	closeErr := f.Close()
	s.rest = 0
	if copyErr != nil || closeErr != nil {
		log.Printf("[FTP] STOR %q failed: %v", path.Base(target), copyErr)
		s.reply("426 Transfer aborted")
		return
	}
	log.Printf("[FTP] stored %q (%d bytes)", path.Base(target), n)
	s.reply("226 Transfer complete")
}

func (s *ftpSession) fileSize(arg string) {
	target, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		s.reply("550 Not a file")
		return
	}
	s.reply("213 %d", info.Size())
}

func (s *ftpSession) fileModTime(arg string) {
	target, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		s.reply("550 Not found")
		return
	}
	s.reply("213 %s", info.ModTime().UTC().Format("20060102150405"))
}

func (s *ftpSession) deleteFile(arg string) {
	target, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		s.reply("550 Not a file")
		return
	}
	if err := os.Remove(target); err != nil {
		s.reply("550 Cannot delete file")
		return
	}
	s.reply("250 File deleted")
}

func (s *ftpSession) makeDir(arg string) {
	target, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		s.reply("550 Cannot create folder")
		return
	}
	s.reply(`257 "%s" created`, path.Base(target))
}

func (s *ftpSession) removeDir(arg string) {
	target, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if target == s.root {
		s.reply("550 Cannot remove the shared folder")
		return
	}
	if err := os.Remove(target); err != nil {
		s.reply("550 Cannot remove folder")
		return
	}
	s.reply("250 Folder removed")
}

func (s *ftpSession) renameFrom(arg string) {
	target, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if _, err := os.Stat(target); err != nil {
		s.reply("550 Not found")
		return
	}
	s.rnfr = target
	s.reply("350 Ready for RNTO")
}

func (s *ftpSession) renameTo(arg string) {
	if s.rnfr == "" {
		s.reply("503 Send RNFR first")
		return
	}
	target, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	from := s.rnfr
	s.rnfr = ""
	if err := os.Rename(from, target); err != nil {
		s.reply("550 Rename failed")
		return
	}
	s.reply("250 Rename successful")
}

// resolve maps an FTP path (relative to the current directory) to a local path
// inside the shared root. Absolute FTP paths are still rooted at the share, so
// "/etc/passwd" means <root>/etc/passwd and never the system file.
func (s *ftpSession) resolve(name string) (string, error) {
	name = strings.TrimSpace(name)
	// Some clients wrap paths in double quotes (MKD "My Folder").
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		name = strings.TrimSpace(name[1 : len(name)-1])
	}
	if strings.ContainsRune(name, 0) {
		return "", errFTPBadPath
	}
	virtual := name
	if virtual == "" {
		virtual = s.cwd
	}
	if !strings.HasPrefix(virtual, "/") {
		virtual = path.Join(s.cwd, virtual)
	}
	virtual = path.Clean("/" + strings.TrimPrefix(virtual, "/"))
	local := filepath.Join(s.root, filepath.FromSlash(strings.TrimPrefix(virtual, "/")))
	if !ftpPathWithin(s.root, local) {
		return "", errFTPBadPath
	}
	// Refuse symlinks that escape the share. New files are checked through
	// their parent folder, which is the part that must already exist.
	resolvedRoot, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return local, nil
	}
	probe := local
	if _, err := os.Lstat(probe); err != nil {
		probe = filepath.Dir(probe)
	}
	if resolved, err := filepath.EvalSymlinks(probe); err == nil && !ftpPathWithin(resolvedRoot, resolved) {
		return "", errFTPBadPath
	}
	return local, nil
}

// ftpPathWithin reports whether p is root or lives below it.
func ftpPathWithin(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
