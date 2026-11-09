package main

// ftp_server.go — a small, dependency-free FTP server so files can be dropped
// onto the handheld from a PC on the same WiFi network.
//
// Design notes:
//   - Passive mode only (PASV/EPSV). The active PORT/EPRT commands are refused,
//     which also removes the FTP bounce class of attacks, and a passive data
//     connection is accepted only from the address of its control connection.
//   - Sign-in is the account "juka" with a password generated on every start
//     (generateSSHPassword), shown on the Network tools page only and compared
//     through the salted digest in constant time. A wrong password waits two
//     seconds for its answer, the third ends the connection, and ten from one
//     address lock it out for ten minutes. There is no anonymous login. FTP is
//     still plaintext, so the page points at SFTP for networks the user does
//     not trust.
//   - "/" is a read-only list of the card's user folders (ftpShareNames: games,
//     BIOS files, saves, screenshots, media) plus JukaHub's downloads — never
//     the card itself, whose Apps, Emus, RetroArch and System folders hold the
//     programs the device runs. On a computer it is the "shared" folder next
//     to the app. Every share is an os.Root, so neither ".." nor a symlink can
//     leave it. Roms/Ports, whose PortMaster scripts the Ports panel runs, and
//     the game profiles' folders are read-only (ftpPath.protected): changing
//     those takes SFTP.
//   - The server stops itself after 15 idle minutes and when the device
//     changes network (fileserver_guard.go). JUKAHUB_FTP_ROOT names a folder
//     that takes the card's place: its user folders are shared.

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
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
	// ftpLoginWait is how long a connection may take to sign in.
	ftpLoginWait = 60 * time.Second
	// ftpSessionMaxFails wrong passwords end a connection.
	ftpSessionMaxFails = 3
)

// The address lockout: ftpLoginMaxFails wrong passwords from one address
// within ftpLoginWindow lock it out for ftpLoginLockout. At most
// ftpLoginMaxAddrs addresses are remembered.
const (
	ftpLoginMaxFails = 10
	ftpLoginWindow   = 10 * time.Minute
	ftpLoginLockout  = 10 * time.Minute
	ftpLoginMaxAddrs = 64
)

// ftpAuthFailDelay is how long a wrong password waits for its answer. It is a
// variable so the tests can run without the wait.
var ftpAuthFailDelay = 2 * time.Second

var (
	errFTPBadPath     = errors.New("path is outside the shared folders")
	errFTPNotFound    = errors.New("no such file or folder")
	errFTPLineTooLong = errors.New("command line too long")
)

// Replies for writes aimed at the virtual root or at a share itself.
const (
	ftpTopLevelReply    = "550 Open one of the shared folders first — nothing can be stored at the top level"
	ftpSharesFixedReply = "550 The shared folders themselves cannot be changed"
	ftpProtectedReply   = "550 This folder holds programs the device runs (ports, game profiles), so FTP cannot change it — use SFTP"
)

// errFTPNothingToShare is a start with no folder to share: "/" would be empty.
var errFTPNothingToShare = errors.New("no folder to share was found")

// ftpService is the running FTP server. Zero value is not usable; use ftpSrv.
// conns are the control connections and data the passive listeners and data
// connections, so a stop also ends a transfer and a pending passive accept.
// loginWait is how long a connection may take to sign in (0: ftpLoginWait).
type ftpService struct {
	mu        sync.Mutex
	ln        net.Listener
	root      string
	shares    *ftpShareSet
	port      int
	password  string
	digest    []byte
	lastErr   string
	conns     map[net.Conn]struct{}
	data      map[io.Closer]struct{}
	gen       uint64
	netBase   fsNetIdentity
	clients   fsClients
	activity  fsActivity
	limiter   ftpLoginLimiter
	loginWait time.Duration
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

// Root returns the folder the shares come from: the card, the override or the
// desktop share folder.
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

// User is the account to sign in with: the same name as SFTP's.
func (s *ftpService) User() string { return sshUserName }

// Password is this run's password ("" while stopped). Like SFTP's it is shown
// on the device only: never published as a config variable, never logged.
func (s *ftpService) Password() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.password
}

// ShareNames lists the folders "/" shows while the server runs.
func (s *ftpService) ShareNames() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	set := s.shares
	s.mu.Unlock()
	return set.names()
}

// Clients lists the open connections for the Network tools page.
func (s *ftpService) Clients() []fsClient {
	if s == nil {
		return nil
	}
	return s.clients.list()
}

// watchTarget is the server as the watchdog sees it.
func (s *ftpService) watchTarget() fsWatchTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fsWatchTarget{running: s.ln != nil, gen: s.gen, base: s.netBase, lastUsed: s.activity.lastUsed()}
}

// stopRun stops the server if it is still run gen: one restarted since the
// watchdog looked is left alone. The check and the stop share one critical
// section, so a start between them cannot be stopped in the old run's place.
func (s *ftpService) stopRun(gen uint64) bool {
	s.mu.Lock()
	if s.ln == nil || s.gen != gen {
		s.mu.Unlock()
		return false
	}
	s.stopLocked()
	return true
}

// checkLogin reports whether user and pass are this run's account. Both are
// always checked; the password through the salted digest, in constant time.
func (s *ftpService) checkLogin(user, pass string) bool {
	s.mu.Lock()
	digest := s.digest
	s.mu.Unlock()
	if len(digest) == 0 {
		return false
	}
	userOK := strings.EqualFold(strings.TrimSpace(user), sshUserName)
	passOK := subtle.ConstantTimeCompare(sshPasswordDigest(pass), digest) == 1
	return userOK && passOK
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

// Start serves the card's user folders (and JukaHub's downloads) on all
// interfaces. The watchdog that stops it again starts with it.
func (s *ftpService) Start() error {
	if s.Running() {
		return nil
	}
	base, card := ftpShareBase()
	shares := ftpSharesFor(base, card)
	if dl := downloadsRoot(); dirExists(dl) {
		shares = ftpAddShare(shares, ftpShare{name: ftpDownloadsShareName, dir: dl})
	}
	if err := s.startShares(base, shares, fmt.Sprintf("0.0.0.0:%d", ftpPort())); err != nil {
		if errors.Is(err, errFTPNothingToShare) {
			// Say which folders were looked for: the card's, or the one a
			// computer shares.
			if card {
				return fmt.Errorf("%w on the card (Roms, Saves, Music…)", err)
			}
			return fmt.Errorf("%w: the shared folder next to JukaHub could not be made", err)
		}
		return err
	}
	if s == ftpSrv {
		fileServerWatchStart()
	}
	return nil
}

// startShares serves shares on addr with a new password; base is the folder
// they were picked from. The tests call it with folders of their own.
func (s *ftpService) startShares(base string, shares []ftpShare, addr string) error {
	if s.Running() {
		return nil
	}
	set, err := openFTPShares(shares)
	if err != nil {
		return err
	}
	if s == ftpSrv {
		set.locked = set.keepInside(ftpProfileDirs())
	}
	password, err := generateSSHPassword()
	if err != nil {
		set.close()
		return fmt.Errorf("generate FTP password: %w", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		set.close()
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		if s == ftpSrv {
			publishFTPState()
		}
		return fmt.Errorf("start FTP server on %s: %w", addr, err)
	}
	netBase := fsNetIdentity{ip: localIPv4()}
	// Every start makes a new password, so lockouts earned guessing the last
	// one are over.
	s.limiter.reset()
	s.mu.Lock()
	if s.conns == nil {
		s.conns = make(map[net.Conn]struct{})
	}
	if s.data == nil {
		s.data = make(map[io.Closer]struct{})
	}
	s.ln = ln
	s.root = base
	s.shares = set
	s.password = password
	s.digest = sshPasswordDigest(password)
	s.lastErr = ""
	s.gen++
	s.netBase = netBase
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tcp.Port
	}
	s.mu.Unlock()
	s.activity.touch()

	if s == ftpSrv {
		publishFTPState()
	}
	logDebug("ftp", "sharing %s on %s", strings.Join(set.names(), ", "), ln.Addr())
	go s.acceptLoop(ln, set)
	return nil
}

// Stop closes the listener, every open connection — control and data, so a
// transfer or a pending passive accept ends too — and the shares.
func (s *ftpService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopLocked()
}

// stopLocked is Stop for a caller holding s.mu, which it releases. The run is
// taken apart inside the lock — a start right after gets a free port and its
// own client list — and its connections are closed after it, since their
// goroutines take the lock to deregister.
func (s *ftpService) stopLocked() {
	ln := s.ln
	s.ln = nil
	set := s.shares
	s.shares = nil
	s.password = ""
	s.digest = nil
	closers := make([]io.Closer, 0, len(s.conns)+len(s.data))
	for c := range s.conns {
		closers = append(closers, c)
	}
	for c := range s.data {
		closers = append(closers, c)
	}
	s.conns = make(map[net.Conn]struct{})
	s.data = make(map[io.Closer]struct{})
	if ln != nil {
		_ = ln.Close()
	}
	s.clients.clear()
	s.mu.Unlock()

	for _, c := range closers {
		_ = c.Close()
	}
	set.close()
	if ln != nil {
		logDebug("ftp", "stopped")
	}
	if s == ftpSrv {
		publishFTPState()
	}
}

// trackData registers a passive listener or data connection of the run whose
// shares are set, so Stop closes it. When that run is over it closes c and
// reports false.
func (s *ftpService) trackData(set *ftpShareSet, c io.Closer) bool {
	s.mu.Lock()
	if s.ln == nil || s.shares != set {
		s.mu.Unlock()
		_ = c.Close()
		return false
	}
	if s.data == nil {
		s.data = make(map[io.Closer]struct{})
	}
	s.data[c] = struct{}{}
	s.mu.Unlock()
	return true
}

// untrackData forgets c; its session closes it.
func (s *ftpService) untrackData(c io.Closer) {
	s.mu.Lock()
	delete(s.data, c)
	s.mu.Unlock()
}

func (s *ftpService) acceptLoop(ln net.Listener, set *ftpShareSet) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// An address locked out for wrong passwords is told so and dropped
		// before it takes one of the connection slots.
		if locked, wait := s.limiter.locked(ftpRemoteIP(conn)); locked {
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, _ = fmt.Fprintf(conn, "421 Too many failed sign-ins from your address — try again in %d min\r\n", ftpMinutesCeil(wait))
			_ = conn.Close()
			continue
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
		s.mu.Unlock()

		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				_ = conn.Close()
			}()
			serveFTPSession(conn, s, set)
		}()
	}
}

// ftpRemoteIP is the address part of a connection's peer.
func ftpRemoteIP(conn net.Conn) string {
	if conn == nil || conn.RemoteAddr() == nil {
		return ""
	}
	if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return tcp.IP.String()
	}
	return fsClientHost(conn.RemoteAddr().String())
}

// ftpMinutesCeil is d in whole minutes, rounded up, at least 1.
func ftpMinutesCeil(d time.Duration) int {
	if m := int((d + time.Minute - 1) / time.Minute); m > 1 {
		return m
	}
	return 1
}

// publishFTPState mirrors the server state into custom variables so config
// scenes (and the Tools panel) can display it. The password is deliberately
// left out: custom variables are saved into the user config. ftp_root names
// the shared folders ("Roms, BIOS, Saves…", "" while stopped): the folder they
// come from is the whole card, which FTP does not share.
func publishFTPState() {
	status := "stopped"
	addr := ""
	if ftpSrv.Running() {
		status = "running"
		addr = ftpSrv.Address()
	}
	publishCustom("ftp_status", status)
	publishCustom("ftp_address", addr)
	publishCustom("ftp_user", sshUserName)
	publishCustom("ftp_root", strings.Join(ftpSrv.ShareNames(), ", "))
	publishCustom("local_ip", localIPv4())
}

// ── Login limiter ─────────────────────────────────────────────────────────

// ftpLoginLimiter locks an address out after too many wrong passwords across
// connections, so a client that reconnects after every third try still stops.
type ftpLoginLimiter struct {
	mu   sync.Mutex
	now  func() time.Time // nil means time.Now; the tests set a clock
	byIP map[string]*ftpLoginRecord
}

// ftpLoginRecord is one address's recent wrong passwords.
type ftpLoginRecord struct {
	fails       int
	first       time.Time
	lockedUntil time.Time
}

func (l *ftpLoginLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// locked reports whether ip is locked out now, and for how much longer.
func (l *ftpLoginLimiter) locked(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.byIP[ip]
	if rec == nil {
		return false, 0
	}
	if left := rec.lockedUntil.Sub(l.clock()); left > 0 {
		return true, left
	}
	return false, 0
}

// fail records a wrong password from ip and reports whether ip is locked out
// now. Failures count within ftpLoginWindow of the first one.
func (l *ftpLoginLimiter) fail(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	if l.byIP == nil {
		l.byIP = make(map[string]*ftpLoginRecord)
	}
	rec := l.byIP[ip]
	if rec == nil {
		l.prune(now)
		rec = &ftpLoginRecord{first: now}
		l.byIP[ip] = rec
	}
	if now.Before(rec.lockedUntil) {
		return true
	}
	if now.Sub(rec.first) > ftpLoginWindow {
		rec.fails, rec.first = 0, now
	}
	rec.fails++
	if rec.fails >= ftpLoginMaxFails {
		rec.lockedUntil = now.Add(ftpLoginLockout)
		rec.fails, rec.first = 0, rec.lockedUntil
		return true
	}
	return false
}

// succeed forgets ip's wrong passwords.
func (l *ftpLoginLimiter) succeed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

// reset forgets every address (a start, with a new password).
func (l *ftpLoginLimiter) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byIP = nil
}

// prune keeps the table bounded: records whose window and lockout are over go
// first, then the oldest. Called with l.mu held.
func (l *ftpLoginLimiter) prune(now time.Time) {
	if len(l.byIP) < ftpLoginMaxAddrs {
		return
	}
	for ip, rec := range l.byIP {
		if !now.Before(rec.lockedUntil) && now.Sub(rec.first) > ftpLoginWindow {
			delete(l.byIP, ip)
		}
	}
	for len(l.byIP) >= ftpLoginMaxAddrs {
		oldest := ""
		var at time.Time
		for ip, rec := range l.byIP {
			if oldest == "" || rec.first.Before(at) {
				oldest, at = ip, rec.first
			}
		}
		delete(l.byIP, oldest)
	}
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

// ── Shares ────────────────────────────────────────────────────────────────

// ftpShareNames are the card folders FTP shares, matched without regard to
// case: games, BIOS files, saves and states, screenshots and box art, and the
// user's media. The card's other top-level folders — Apps (JukaHub itself and
// its data), Emus and RetroArch (launch scripts and cores), PortMaster and
// System (boot hooks) — hold programs the device runs, and writing those over
// a plaintext protocol would hand the network a way to run code on it.
// PhoneDrop is where the Phone Companion saves what a phone sends
// (companionDropDir), so a PC can collect it.
var ftpShareNames = []string{
	"Roms", "BIOS", "Saves", "States", "Screenshots", "Imgs", "Collections",
	"Music", "Videos", "Movies", "Pictures", "Photos", "Images", "Books",
	"Documents", "Downloads", "Recordings", "Media", companionDropDir,
}

const (
	// ftpDownloadsShareName is JukaHub's own download folder (saved music and
	// videos), which lives inside the app's data folder.
	ftpDownloadsShareName = "JukaHub downloads"
	// ftpDesktopShareName is the folder shared on a computer.
	ftpDesktopShareName = "Shared"
)

// ftpShare is one folder "/" lists. root is opened when the server starts and
// closed when it stops; every file operation inside the share goes through it.
type ftpShare struct {
	name string
	dir  string
	root *os.Root
}

// ftpShareSet is one run's shares; it does not change while the server runs.
// locked are folders inside the shares FTP may read but not change: those of
// the game profiles (ftpProfileDirs), folded by ftpFoldPath.
type ftpShareSet struct {
	list   []*ftpShare
	locked []string
}

// lockedPath reports whether abs, a path inside a share, is a locked folder,
// inside one, or above one — renaming or removing a folder above would swap
// what the locked path finds.
func (set *ftpShareSet) lockedPath(abs string) bool {
	if set == nil || len(set.locked) == 0 {
		return false
	}
	key := ftpFoldPath(abs)
	sep := string(filepath.Separator)
	for _, dir := range set.locked {
		if key == dir || strings.HasPrefix(key, dir+sep) || strings.HasPrefix(dir, key+sep) {
			return true
		}
	}
	return false
}

// keepInside returns the folded dirs that lie strictly inside one of the
// shares, in the share's own spelling: a profile whose folder is a whole
// share, or the card, does not make every share read-only. A profile's folder
// is resolved through symlinks (gameprofiles.go realPath), so a share is
// compared through its resolved path too.
func (set *ftpShareSet) keepInside(dirs []string) []string {
	type shareBase struct{ base, real string }
	sep := string(filepath.Separator)
	bases := make([]shareBase, 0, len(set.list))
	for _, sh := range set.list {
		b := shareBase{base: ftpFoldPath(sh.dir)}
		if real, err := filepath.EvalSymlinks(sh.dir); err == nil {
			b.real = ftpFoldPath(real)
		}
		bases = append(bases, b)
	}
	var out []string
	for _, dir := range dirs {
		for _, b := range bases {
			if strings.HasPrefix(dir, b.base+sep) {
				out = append(out, dir)
				break
			}
			if b.real != "" && b.real != b.base && strings.HasPrefix(dir, b.real+sep) {
				out = append(out, b.base+dir[len(b.real):])
				break
			}
		}
	}
	return out
}

// ftpFoldPath is p as the card's FAT and exFAT file systems tell names apart:
// cleaned, in lower case, every name without its trailing dots and spaces.
func ftpFoldPath(p string) string {
	sep := string(filepath.Separator)
	parts := strings.Split(filepath.Clean(p), sep)
	for i, part := range parts {
		if part != "." && part != ".." {
			parts[i] = strings.TrimRight(part, ". ")
		}
	}
	return strings.ToLower(strings.Join(parts, sep))
}

// ftpProfileDirs lists the game profiles' folders (gameprofiles.go), folded:
// a profile runs the program in its folder wherever on the card that is, so a
// folder inside a share is kept as read-only as Roms/Ports. The declared
// folder counts as well as the resolved one: a folder that is missing now
// would be run from once someone made it. A profile added while the server
// runs is covered from its next start.
func ftpProfileDirs() []string {
	var out []string
	add := func(dir string) {
		if strings.TrimSpace(dir) != "" && dir != "." {
			out = append(out, ftpFoldPath(dir))
		}
	}
	roots := gameProfileRoots()
	for _, e := range gameProfileEntries() {
		add(e.GameDir)
		if e.Executable != "" {
			add(filepath.Dir(e.Executable))
		}
		dir := filepath.FromSlash(strings.TrimSpace(e.Profile.Dir))
		switch {
		case dir == "":
		case filepath.IsAbs(dir):
			add(dir)
		default:
			for _, root := range roots {
				add(filepath.Join(root, dir))
			}
		}
	}
	return out
}

// find looks a share up by name: exactly first, then in any case.
func (set *ftpShareSet) find(name string) *ftpShare {
	if set == nil {
		return nil
	}
	for _, sh := range set.list {
		if sh.name == name {
			return sh
		}
	}
	for _, sh := range set.list {
		if strings.EqualFold(sh.name, name) {
			return sh
		}
	}
	return nil
}

// names lists the shares in display order.
func (set *ftpShareSet) names() []string {
	if set == nil {
		return nil
	}
	out := make([]string, 0, len(set.list))
	for _, sh := range set.list {
		out = append(out, sh.name)
	}
	return out
}

// close releases every share's root.
func (set *ftpShareSet) close() {
	if set == nil {
		return
	}
	for _, sh := range set.list {
		if sh.root != nil {
			_ = sh.root.Close()
		}
	}
}

// ftpShareBase is the folder the shares come from, and whether it is a card
// (its user folders are shared) or the desktop share (shared whole).
func ftpShareBase() (string, bool) {
	if custom := strings.TrimSpace(os.Getenv("JUKAHUB_FTP_ROOT")); custom != "" && dirExists(custom) {
		return custom, true
	}
	if card := fileServerCardRoot(); card != "" {
		return card, true
	}
	return ftpDesktopShareDir(), false
}

// ftpDesktopShareDir is the "shared" folder next to the app on a computer,
// created on first use. It is never the app folder itself, which holds
// jukauser.json and saved credentials; "" when it cannot be made.
func ftpDesktopShareDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Join(filepath.Dir(exe), "shared")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logWarn("ftp", "cannot create the shared folder: %v", err)
		return ""
	}
	return dir
}

// ftpSharesFor lists the shares under base. On a card those are the allowed
// top-level folders that are real directories — a symlink there could point
// anywhere, so it is skipped; elsewhere base itself is the one share.
func ftpSharesFor(base string, card bool) []ftpShare {
	if strings.TrimSpace(base) == "" {
		return nil
	}
	if !card {
		return []ftpShare{{name: ftpDesktopShareName, dir: base}}
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		logWarn("ftp", "cannot list the card: %v", err)
		return nil
	}
	var out []ftpShare
	for _, e := range entries {
		if !e.IsDir() || !ftpShareAllowed(e.Name()) {
			continue
		}
		out = ftpAddShare(out, ftpShare{name: e.Name(), dir: filepath.Join(base, e.Name())})
	}
	return out
}

// ftpShareAllowed reports whether a card folder is one FTP may share.
func ftpShareAllowed(name string) bool {
	for _, allowed := range ftpShareNames {
		if strings.EqualFold(name, allowed) {
			return true
		}
	}
	return false
}

// ftpAddShare appends sh unless a share of that name, in any case, is there:
// a case-sensitive card can hold both "Roms" and "ROMS".
func ftpAddShare(list []ftpShare, sh ftpShare) []ftpShare {
	for _, have := range list {
		if strings.EqualFold(have.name, sh.name) {
			return list
		}
	}
	return append(list, sh)
}

// openFTPShares opens an os.Root on every share; one that cannot be opened is
// left out. Nothing to share is errFTPNothingToShare: "/" would be an empty
// folder (Start says which folders it looked for).
func openFTPShares(shares []ftpShare) (*ftpShareSet, error) {
	set := &ftpShareSet{}
	for _, sh := range shares {
		root, err := os.OpenRoot(sh.dir)
		if err != nil {
			logWarn("ftp", "cannot share %s: %v", sh.name, err)
			continue
		}
		set.list = append(set.list, &ftpShare{name: sh.name, dir: sh.dir, root: root})
	}
	if len(set.list) == 0 {
		return nil, errFTPNothingToShare
	}
	return set, nil
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
	conn     net.Conn
	r        *bufio.Reader
	srv      *ftpService
	shares   *ftpShareSet
	cwd      string // virtual path, always "/..." (FTP separators)
	user     string
	authed   bool
	fails    int
	remoteIP string
	clientID uint64

	// loginBy is when a connection that has not signed in is dropped.
	loginBy time.Time

	dataLn net.Listener
	rest   int64
	rnfr   *ftpPath
}

func serveFTPSession(conn net.Conn, srv *ftpService, set *ftpShareSet) {
	sess := &ftpSession{
		conn:     conn,
		r:        bufio.NewReaderSize(conn, ftpMaxLineBytes),
		srv:      srv,
		shares:   set,
		cwd:      "/",
		remoteIP: ftpRemoteIP(conn),
	}
	if srv != nil {
		sess.clientID = srv.clients.add(conn.RemoteAddr().String())
		defer srv.clients.remove(sess.clientID)
	}
	sess.reply("220 JukaHub FTP ready — sign in as %s with the password shown on the device", sshUserName)
	sess.loginBy = time.Now().Add(sess.loginWait())
	for {
		// A connection that has not signed in gets a minute from connecting,
		// however it spends it: a wait re-armed on every line let a client
		// that sends one now and then hold a slot for good, and eight of them
		// every slot. A signed-in one may idle for ftpIdleTimeout per command.
		deadline := time.Now().Add(ftpIdleTimeout)
		if !sess.authed {
			deadline = sess.loginBy
		}
		_ = conn.SetReadDeadline(deadline)
		line, err := ftpReadLine(sess.r)
		if errors.Is(err, errFTPLineTooLong) {
			sess.reply("500 Command too long")
			continue
		}
		if err != nil {
			sess.closeData()
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg := splitFTPCommand(line)
		if cmd == "" {
			continue
		}
		// Use keeps the server from its idle stop; the keepalives clients send
		// on their own to hold a forgotten connection open do not.
		if sess.authed && !ftpKeepalive(cmd) && srv != nil {
			srv.activity.touch()
		}
		if !sess.handle(cmd, arg) {
			sess.closeData()
			return
		}
	}
}

// loginWait is how long this connection may take to sign in.
func (s *ftpSession) loginWait() time.Duration {
	if s.srv != nil && s.srv.loginWait > 0 {
		return s.srv.loginWait
	}
	return ftpLoginWait
}

// ftpKeepalive reports whether cmd is one clients send by themselves to keep a
// connection open or to look at its state (FileZilla rotates NOOP, TYPE and
// PWD): none is someone using the server, so none holds off the idle stop.
func ftpKeepalive(cmd string) bool {
	switch cmd {
	case "NOOP", "PWD", "XPWD", "TYPE", "SYST", "FEAT", "OPTS", "MODE", "STRU", "STAT", "HELP":
		return true
	}
	return false
}

// ftpReadLine reads one command line. A line longer than the reader's buffer
// is drained and reported as errFTPLineTooLong instead of being collected:
// ReadString kept buffering a stream without a newline until the handheld ran
// out of memory, which anyone on the network could do before signing in.
func ftpReadLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		for errors.Is(err, bufio.ErrBufferFull) {
			_, err = r.ReadSlice('\n')
		}
		if err != nil {
			return "", err
		}
		return "", errFTPLineTooLong
	}
	if err != nil {
		return "", err
	}
	return string(line), nil
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

// reply sends one reply line. The format is passed to Sprintf as it is, so vet
// can check it against the arguments.
func (s *ftpSession) reply(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	_ = s.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, _ = io.WriteString(s.conn, msg+"\r\n")
}

func (s *ftpSession) closeData() {
	if s.dataLn != nil {
		s.untrackData(s.dataLn)
		_ = s.dataLn.Close()
		s.dataLn = nil
	}
}

// trackData hands a passive listener or data connection to the server, so a
// stop closes it; false means the server stopped meanwhile (c is closed).
func (s *ftpSession) trackData(c io.Closer) bool {
	if s.srv == nil {
		return true
	}
	return s.srv.trackData(s.shares, c)
}

func (s *ftpSession) untrackData(c io.Closer) {
	if s.srv != nil {
		s.srv.untrackData(c)
	}
}

// ftpTrackedConn is an accepted data connection the server knows about;
// closing it also takes it off the server's list.
type ftpTrackedConn struct {
	net.Conn
	sess *ftpSession
}

func (c ftpTrackedConn) Close() error {
	c.sess.untrackData(c.Conn)
	return c.Conn.Close()
}

// openData accepts the passive data connection the client opened after
// PASV/EPSV. The listener is single-use, as the RFC intends, and it only takes
// a connection from the control connection's address: another device on the
// network that raced the client to the port would otherwise receive the file
// (or write the upload).
func (s *ftpSession) openData() (net.Conn, error) {
	if s.dataLn == nil {
		return nil, errors.New("use PASV or EPSV first")
	}
	ln := s.dataLn
	s.dataLn = nil
	defer func() {
		s.untrackData(ln)
		_ = ln.Close()
	}()
	if tl, ok := ln.(*net.TCPListener); ok {
		_ = tl.SetDeadline(time.Now().Add(ftpAcceptWait))
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil, err
		}
		if s.remoteIP == "" || ftpRemoteIP(conn) == s.remoteIP {
			if !s.trackData(conn) {
				return nil, errors.New("the server is stopping")
			}
			return ftpTrackedConn{Conn: conn, sess: s}, nil
		}
		// Debug, not a warning: anyone on the network could fill the log
		// by knocking on the port.
		logDebug("ftp", "refused a data connection from %s for a session from %s", ftpRemoteIP(conn), s.remoteIP)
		_ = conn.Close()
	}
}

// dataConn wraps a data connection so every buffer extends its deadline and
// counts as use of the server.
func (s *ftpSession) dataConn(conn net.Conn) ftpIdleConn {
	var act *fsActivity
	if s.srv != nil {
		act = &s.srv.activity
	}
	return ftpIdleConn{Conn: conn, act: act}
}

// track shows what this connection is moving on the Network tools page.
func (s *ftpSession) track(what string) {
	if s.srv != nil {
		s.srv.clients.setTransfer(s.clientID, what)
	}
}

func (s *ftpSession) untrack(what string) {
	if s.srv != nil {
		s.srv.clients.clearTransfer(s.clientID, what)
	}
}

// login checks PASS against this run's account. A wrong password waits
// ftpAuthFailDelay for its answer, the ftpSessionMaxFails-th failed sign-in
// ends the connection, and the limiter locks the address out across
// connections. Attempts are logged without the name or password typed.
func (s *ftpSession) login(pass string) bool {
	if s.user == "" {
		s.reply("503 Send USER first")
		return true
	}
	if s.srv == nil {
		s.reply("530 Sign-in is not available")
		return true
	}
	if locked, wait := s.srv.limiter.locked(s.remoteIP); locked {
		s.reply("421 Too many failed sign-ins from your address — try again in %d min", ftpMinutesCeil(wait))
		return false
	}
	if s.srv.checkLogin(s.user, pass) {
		s.authed = true
		s.fails = 0
		s.srv.limiter.succeed(s.remoteIP)
		s.srv.clients.setUser(s.clientID, sshUserName)
		s.srv.activity.touch()
		logDebug("ftp", "%s signed in", s.remoteIP)
		s.reply("230 Logged in")
		return true
	}
	s.authed = false
	s.fails++
	// Only a guess at the real account's password waits and counts toward the
	// address lockout. Any other name can never sign in — Explorer and
	// browsers try "anonymous" on their own before they ask — so it is no
	// guess, and locking a PC out for its own client's habit would only hurt.
	guess := strings.EqualFold(s.user, sshUserName)
	lockedNow := false
	if guess {
		lockedNow = s.srv.limiter.fail(s.remoteIP)
		logWarn("ftp", "wrong password from %s", s.remoteIP)
		if ftpAuthFailDelay > 0 {
			time.Sleep(ftpAuthFailDelay)
		}
	}
	if lockedNow || s.fails >= ftpSessionMaxFails {
		s.reply("421 Too many failed sign-ins — closing the connection")
		return false
	}
	if !guess {
		s.reply("530 Sign in as %s with the password shown on the device", sshUserName)
		return true
	}
	s.reply("530 Wrong password — it is on the device's Network tools page")
	return true
}

func (s *ftpSession) handle(cmd, arg string) bool {
	// RFC 959: REST is followed at once by the transfer it restarts and RNFR by
	// RNTO; anything else in between drops them, so an offset or a rename
	// source left from an abandoned attempt cannot reach a later STOR or RNTO.
	// Setting up the data connection or a keepalive in between is tolerated.
	switch cmd {
	case "REST", "RETR", "STOR", "PASV", "EPSV", "TYPE", "MODE", "STRU", "NOOP":
	default:
		s.rest = 0
	}
	if cmd != "RNFR" && cmd != "RNTO" {
		s.rnfr = nil
	}
	switch cmd {
	case "USER":
		// A new USER starts a new sign-in, as RFC 959 allows, with its own
		// time to finish.
		if s.authed {
			s.loginBy = time.Now().Add(s.loginWait())
		}
		s.user = strings.TrimSpace(arg)
		s.authed = false
		s.reply("331 Password required")
	case "PASS":
		return s.login(arg)
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
		if !s.requireAuth() {
			return true
		}
		s.reply("257 %s is the current directory", ftpQuoted(s.cwd))
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
	p, err := s.resolve(name)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if !p.isRoot() {
		info, err := p.share.root.Stat(p.rel)
		if err != nil || !info.IsDir() {
			s.reply("550 Not a directory")
			return
		}
	}
	s.cwd = p.virtual
	s.reply("250 %s is the current directory", ftpQuoted(s.cwd))
}

// ftpQuoted is a path for a 257 or 250 reply: in double quotes, with every
// quote in it doubled, as RFC 959 asks. resolve refuses CR and LF, so no path
// here can break the reply line.
func ftpQuoted(p string) string {
	return `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
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
	if !s.trackData(ln) {
		s.reply("425 The server is stopping")
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
	// Clients ask for LIST -a, LIST -la or LIST -a -l when hidden files are
	// enabled. The flags only describe the listing, not a path: drop every
	// one instead of answering 550 for a file literally called "-l".
	for strings.HasPrefix(arg, "-") {
		i := strings.IndexAny(arg, " \t")
		if i < 0 {
			arg = ""
			break
		}
		arg = strings.TrimSpace(arg[i+1:])
	}
	conn, err := s.openData()
	if err != nil {
		s.reply("425 Use PASV or EPSV first")
		return
	}
	defer conn.Close()

	p, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}

	var b strings.Builder
	now := time.Now()
	add := func(fi os.FileInfo, name string) bool {
		if b.Len() > ftpMaxListing {
			return false
		}
		if strings.ContainsAny(name, "\r\n") {
			// A line break in a name would end its line early and start a
			// forged one; such a file cannot be named over FTP anyway.
			return true
		}
		if namesOnly {
			b.WriteString(name)
			b.WriteString("\r\n")
		} else {
			b.WriteString(ftpListLine(fi, name, now))
		}
		return true
	}

	if p.isRoot() {
		// The virtual root: one folder per share, nothing else.
		if s.shares != nil {
			for _, sh := range s.shares.list {
				if fi, err := sh.root.Stat("."); err == nil {
					add(fi, sh.name)
				}
			}
		}
	} else {
		f, err := p.share.root.Open(p.rel)
		if err != nil {
			s.reply("550 No such file or directory")
			return
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			s.reply("550 No such file or directory")
			return
		}
		if !info.IsDir() {
			f.Close()
			add(info, path.Base(p.virtual))
		} else {
			entries, err := f.ReadDir(-1)
			f.Close()
			if err != nil {
				s.reply("550 Cannot read directory")
				return
			}
			if !namesOnly {
				add(info, ".")
				add(info, "..")
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
		}
	}

	s.reply("150 Here comes the directory listing")
	_ = conn.SetWriteDeadline(time.Now().Add(ftpAcceptWait))
	if _, err := io.WriteString(conn, b.String()); err != nil {
		logWarn("ftp", "listing write failed: %v", err)
		s.reply("426 Transfer aborted")
		return
	}
	s.reply("226 Directory send OK")
}

// ftpListLine is one `ls -l` style row. The type letter comes from the mode
// itself: Perm() drops the directory bit, and every folder used to be listed
// as a plain file ("-rwxr-xr-x"), which clients then offered to download.
// Like ls, a date more than six months away shows its year instead of a time.
func ftpListLine(fi os.FileInfo, name string, now time.Time) string {
	kind := byte('-')
	switch {
	case fi.IsDir():
		kind = 'd'
	case fi.Mode()&os.ModeSymlink != 0:
		kind = 'l'
	}
	perm := fi.Mode().Perm().String()
	mod := fi.ModTime()
	stamp := mod.Format("Jan _2 15:04")
	if d := now.Sub(mod); d > 182*24*time.Hour || d < -182*24*time.Hour {
		stamp = mod.Format("Jan _2  2006")
	}
	return fmt.Sprintf("%c%s 1 owner group %d %s %s\r\n", kind, perm[1:], fi.Size(), stamp, name)
}

// ftpIdleConn extends a data connection's deadline on every read and write, so
// ftpIdleTimeout bounds a stall rather than the whole transfer. One deadline
// set before the copy cut off every transfer that took longer — a multi-GB
// file over handheld Wi-Fi does — and an overwriting upload had already
// truncated the original by then. Every buffer also counts as use of the
// server (act), so a long transfer never trips the idle stop.
type ftpIdleConn struct {
	net.Conn
	act *fsActivity
}

func (c ftpIdleConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(ftpIdleTimeout))
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.act.touch()
	}
	return n, err
}

func (c ftpIdleConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(ftpIdleTimeout))
	c.act.touch()
	return c.Conn.Write(p)
}

func (s *ftpSession) retrieve(arg string) {
	// A REST offset applies to this transfer only, whichever way it ends; read
	// it now so an early refusal cannot leave it for the next command.
	offset := s.rest
	s.rest = 0
	p, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if p.isRoot() || p.isShareTop() {
		s.reply("550 Not a file")
		return
	}
	f, err := p.share.root.Open(p.rel)
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

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			s.reply("550 Cannot seek")
			return
		}
	}
	what := "downloading " + p.display()
	s.track(what)
	defer s.untrack(what)
	s.reply("150 Opening data connection (%d bytes)", max64(info.Size()-offset, 0))
	if _, err := io.Copy(s.dataConn(conn), f); err != nil {
		logWarn("ftp", "RETR %q failed: %v", path.Base(p.virtual), err)
		s.reply("426 Transfer aborted")
		return
	}
	s.reply("226 Transfer complete")
}

func (s *ftpSession) store(arg string) {
	offset := s.rest
	s.rest = 0
	p, err := s.resolve(arg)
	if errors.Is(err, errFTPNotFound) && path.Dir(p.virtual) == "/" {
		// A file dropped on "/" itself: say where it can go.
		s.reply(ftpTopLevelReply)
		return
	}
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if p.isRoot() || p.isShareTop() {
		s.reply(ftpTopLevelReply)
		return
	}
	if p.protected {
		s.reply(ftpProtectedReply)
		return
	}
	if info, err := p.share.root.Stat(p.rel); err == nil && info.IsDir() {
		s.reply("550 Is a directory")
		return
	}
	if info, err := p.share.root.Stat(filepath.Dir(p.rel)); err != nil || !info.IsDir() {
		s.reply("550 Target folder does not exist")
		return
	}
	conn, err := s.openData()
	if err != nil {
		s.reply("425 Use PASV or EPSV first")
		return
	}
	defer conn.Close()

	// A resume needs the file it resumes: without O_CREATE, a REST to a file
	// that is not there fails here instead of leaving an empty one behind
	// after the 554 below.
	flags := os.O_WRONLY
	if offset == 0 {
		flags |= os.O_CREATE | os.O_TRUNC
	}
	f, err := p.share.root.OpenFile(p.rel, flags, 0o644)
	if err != nil {
		s.reply("550 Cannot write file")
		return
	}
	if offset > 0 {
		// REST then STOR resumes an upload at the offset, as FEAT's "REST
		// STREAM" promises. The offset used to be ignored: the client's tail
		// was written at byte 0 over a truncated file, and the client
		// reported a successful resume of a file now missing its head.
		info, statErr := f.Stat()
		if statErr != nil || offset > info.Size() {
			f.Close()
			s.reply("554 Restart position is past the end of the file")
			return
		}
		if err := f.Truncate(offset); err != nil {
			f.Close()
			s.reply("550 Cannot resume the file")
			return
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			s.reply("550 Cannot resume the file")
			return
		}
	}
	what := "uploading " + p.display()
	s.track(what)
	defer s.untrack(what)
	s.reply("150 Ok to send data")
	n, copyErr := io.Copy(f, s.dataConn(conn))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		logWarn("ftp", "STOR %q failed: %v", path.Base(p.virtual), copyErr)
		s.reply("426 Transfer aborted")
		return
	}
	logDebug("ftp", "stored %q (%d bytes)", path.Base(p.virtual), n)
	s.reply("226 Transfer complete")
}

func (s *ftpSession) fileSize(arg string) {
	p, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if p.isRoot() {
		s.reply("550 Not a file")
		return
	}
	info, err := p.share.root.Stat(p.rel)
	if err != nil || info.IsDir() {
		s.reply("550 Not a file")
		return
	}
	s.reply("213 %d", info.Size())
}

func (s *ftpSession) fileModTime(arg string) {
	p, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if p.isRoot() {
		s.reply("550 Not found")
		return
	}
	info, err := p.share.root.Stat(p.rel)
	if err != nil {
		s.reply("550 Not found")
		return
	}
	s.reply("213 %s", info.ModTime().UTC().Format("20060102150405"))
}

func (s *ftpSession) deleteFile(arg string) {
	p, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if p.isRoot() || p.isShareTop() {
		s.reply(ftpSharesFixedReply)
		return
	}
	if p.protected {
		s.reply(ftpProtectedReply)
		return
	}
	// Lstat: a link is removed itself, never what it points at.
	if info, err := p.share.root.Lstat(p.rel); err != nil || info.IsDir() {
		s.reply("550 Not a file")
		return
	}
	if err := p.share.root.Remove(p.rel); err != nil {
		s.reply("550 Cannot delete file")
		return
	}
	s.reply("250 File deleted")
}

func (s *ftpSession) makeDir(arg string) {
	p, err := s.resolve(arg)
	if errors.Is(err, errFTPNotFound) && path.Dir(p.virtual) == "/" {
		s.reply("550 Folders can only be made inside one of the shared folders")
		return
	}
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if p.isRoot() || p.isShareTop() {
		s.reply("550 Folders can only be made inside one of the shared folders")
		return
	}
	if p.protected {
		s.reply(ftpProtectedReply)
		return
	}
	if err := p.share.root.Mkdir(p.rel, 0o755); err != nil {
		s.reply("550 Cannot create folder")
		return
	}
	s.reply("257 %s created", ftpQuoted(p.virtual))
}

func (s *ftpSession) removeDir(arg string) {
	p, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if p.isRoot() || p.isShareTop() {
		s.reply(ftpSharesFixedReply)
		return
	}
	if p.protected {
		s.reply(ftpProtectedReply)
		return
	}
	if info, err := p.share.root.Lstat(p.rel); err != nil || !info.IsDir() {
		s.reply("550 Not a folder")
		return
	}
	if err := p.share.root.Remove(p.rel); err != nil {
		s.reply("550 Cannot remove folder (is it empty?)")
		return
	}
	s.reply("250 Folder removed")
}

func (s *ftpSession) renameFrom(arg string) {
	s.rnfr = nil
	p, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if p.isRoot() || p.isShareTop() {
		s.reply(ftpSharesFixedReply)
		return
	}
	if p.protected {
		s.reply(ftpProtectedReply)
		return
	}
	if _, err := p.share.root.Lstat(p.rel); err != nil {
		s.reply("550 Not found")
		return
	}
	s.rnfr = &p
	s.reply("350 Ready for RNTO")
}

func (s *ftpSession) renameTo(arg string) {
	from := s.rnfr
	s.rnfr = nil
	if from == nil {
		s.reply("503 Send RNFR first")
		return
	}
	p, err := s.resolve(arg)
	if err != nil {
		s.reply("550 %s", err.Error())
		return
	}
	if p.isRoot() || p.isShareTop() {
		s.reply(ftpSharesFixedReply)
		return
	}
	// A script moved into Ports would be run like the ones there.
	if p.protected {
		s.reply(ftpProtectedReply)
		return
	}
	// Each share is its own os.Root: a move between two of them would have to
	// leave one root to reach the other.
	if p.share != from.share {
		s.reply("550 Files cannot be moved between shared folders — download and upload instead")
		return
	}
	if err := p.share.root.Rename(from.rel, p.rel); err != nil {
		s.reply("550 Rename failed")
		return
	}
	s.reply("250 Rename successful")
}

// ftpPath is an FTP path resolved against the shares: the virtual root
// (share == nil), a share itself (rel "."), or a path inside a share in the
// form its os.Root takes. protected marks a path FTP may read but not change
// (ftpProtected, ftpShareSet.lockedPath).
type ftpPath struct {
	virtual   string
	share     *ftpShare
	rel       string
	protected bool
}

func (p ftpPath) isRoot() bool     { return p.share == nil }
func (p ftpPath) isShareTop() bool { return p.share != nil && p.rel == "." }

// display is the path without its leading slash, for logs and the client list.
func (p ftpPath) display() string { return strings.TrimPrefix(p.virtual, "/") }

// resolve maps an FTP path (relative to the current directory) onto the
// shares. The path is cleaned as a rooted path first, so ".." stops at "/";
// its first component must name a share, and the rest is handed to that
// share's os.Root, which refuses anything — a ".." the cleaning missed, a
// symlink — that would leave it. An unknown top-level name answers
// errFTPNotFound with only virtual set, so a write there can say where to go.
func (s *ftpSession) resolve(name string) (ftpPath, error) {
	name = strings.TrimSpace(name)
	// Some clients wrap paths in double quotes (MKD "My Folder").
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		name = strings.TrimSpace(name[1 : len(name)-1])
	}
	// NUL ends a path for the system; CR or LF in one would break the reply
	// lines it is quoted in (PWD, MKD) and the listing.
	if strings.ContainsAny(name, "\x00\r\n") {
		return ftpPath{}, errFTPBadPath
	}
	virtual := name
	if virtual == "" {
		virtual = s.cwd
	}
	if !strings.HasPrefix(virtual, "/") {
		virtual = path.Join(s.cwd, virtual)
	}
	virtual = path.Clean("/" + strings.TrimPrefix(virtual, "/"))
	if virtual == "/" {
		return ftpPath{virtual: "/"}, nil
	}
	top, rest, _ := strings.Cut(virtual[1:], "/")
	share := s.shares.find(top)
	if share == nil {
		return ftpPath{virtual: virtual}, errFTPNotFound
	}
	p := ftpPath{virtual: "/" + share.name, share: share, rel: "."}
	if rest == "" {
		return p, nil
	}
	for _, part := range strings.Split(rest, "/") {
		if !ftpNameOK(part) {
			return ftpPath{}, errFTPBadPath
		}
	}
	rel := filepath.FromSlash(rest)
	if !filepath.IsLocal(rel) {
		return ftpPath{}, errFTPBadPath
	}
	p.virtual += "/" + rest
	p.rel = rel
	p.protected = ftpProtected(share.name, rest) || s.shares.lockedPath(filepath.Join(share.dir, rel))
	return p, nil
}

// ftpProtectedDirs are folders inside a share that hold scripts JukaHub runs:
// PortMaster's launchers in Roms/Ports (Roms/PORTS on some cards), which the
// Ports panel starts with bash (ports.go portRoots), with the game files they
// load beside them. FTP is plaintext, so a password read off the network must
// not be enough to replace one; they are read-only over FTP and SFTP can
// change them. (Ports/ and Data/ports at the card's top are not shared.)
var ftpProtectedDirs = []struct{ share, dir string }{
	{share: "Roms", dir: "Ports"},
}

// ftpProtected reports whether rest, a "/"-separated path inside share, is one
// of ftpProtectedDirs or inside one. Names compare in any case and without the
// trailing dots and spaces the card's FAT and exFAT drivers (and Windows)
// drop, since "Ports." opens Ports there.
func ftpProtected(share, rest string) bool {
	first, _, _ := strings.Cut(rest, "/")
	first = strings.TrimRight(first, ". ")
	for _, d := range ftpProtectedDirs {
		if strings.EqualFold(share, d.share) && strings.EqualFold(first, d.dir) {
			return true
		}
	}
	return false
}

// ftpNameOK reports whether one path component may be handed to os.Root: not
// empty or a dot name, and on Windows free of ':' (drives, alternate data
// streams) and '\', which Windows reads as a separator inside the component.
func ftpNameOK(part string) bool {
	if part == "" || part == "." || part == ".." {
		return false
	}
	return runtime.GOOS != "windows" || !strings.ContainsAny(part, `:\`)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
