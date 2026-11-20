package main

// smb2.go — a small, read-only SMB2/3 client in pure Go, so JukaHub can
// browse and download games from a NAS (Synology, QNAP, TrueNAS/Samba), a
// Windows or macOS shared folder, or a router's USB disk, on the handheld
// and on a PC alike. The network-drive screens (nas*.go) use the API below.
//
// What it speaks (details in smb2_proto.go, smb2_ntlm.go, smb2_sign.go,
// smb2_rpc.go):
//
//   - SMB 2.0.2, 2.1, 3.0, 3.0.2 and 3.1.1 over TCP port 445; an SMB1-only
//     server gets errSMBv1Only;
//   - sign-in with a user name and password (NTLMv2), as guest, or
//     anonymously (empty user name: anonymous first, then "guest");
//   - signing (HMAC-SHA256 / AES-CMAC) on every authenticated session, and
//     AES-128-GCM encryption on SMB 3.1.1 when a server or share requires
//     it (AES-CCM for 3.0 servers is not supported: errSMBEncryptionRequired);
//   - share list over the srvsvc RPC pipe, folder listing, file info and
//     reads of up to 1 MiB per request.
//
// It never creates, writes, renames or deletes anything on the server.
// A dropped connection (NAS idle timeout, Wi-Fi sleep) is dialled again on
// the next call, and open files reopen themselves.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Errors callers test with errors.Is. Status errors from the server match
// them too (smb2StatusError.Is).
var (
	errSMBNoShareList        = errors.New("smb: the server did not list its shares")
	errSMBAuth               = errors.New("smb: sign-in failed")
	errSMBNotFound           = errors.New("smb: not found")
	errSMBShareNotFound      = errors.New("smb: share not found")
	errSMBAccessDenied       = errors.New("smb: access denied")
	errSMBEncryptionRequired = errors.New("smb: the server requires encryption this client cannot do")
	errSMBv1Only             = errors.New("smb: the server only speaks SMB1")
	errSMBBadPath            = errors.New("smb: path not allowed")
	errSMBProtocol           = errors.New("smb: malformed reply")
	errSMBSignature          = errors.New("smb: reply failed its signature check")
	errSMBClosed             = errors.New("smb: connection closed")
	errSMBUnsupported        = errors.New("smb: not supported")
)

// smbEntry is one file or folder.
type smbEntry struct {
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool

	// Hidden: the server marks it hidden or system, it is a dot file, or
	// it is NAS housekeeping (@eaDir, #recycle, $RECYCLE.BIN, ...).
	Hidden bool
}

// smbSession is a signed-in connection to one server. It is safe for
// concurrent use; requests take turns on the one connection.
type smbSession struct {
	addr     string
	uncHost  string
	user     string
	password string
	domain   string

	mu     sync.Mutex
	conn   *smb2Conn
	gen    uint64
	closed bool

	// shut and live let Close cut off a request in flight (a read stuck on
	// a dead network) instead of waiting up to 30 s for mu: Close sets shut
	// and then closes live's socket; run publishes live and then checks
	// shut, so one of them always sees the other.
	shut atomic.Bool
	live atomic.Pointer[smb2Conn]
}

// smbFile is an open file. Read, ReadAt, Seek and WriteTo may be used from
// several goroutines.
type smbFile struct {
	s     *smbSession
	share string
	path  string
	size  int64

	// Guarded by s.mu: the handle and the connection it belongs to.
	tree   *smb2Tree
	fid    [16]byte
	gen    uint64
	closed bool

	// Guarded by posMu: the Read/Seek/WriteTo position.
	posMu sync.Mutex
	pos   int64

	// Guarded by cacheMu: one read-ahead request, so small sequential
	// reads (io.Copy's 32 KiB, io.SectionReader) cost one round trip per
	// MiB rather than one per call. Lock order: posMu, cacheMu, s.mu.
	cacheMu  sync.Mutex
	cache    []byte
	cacheOff int64
}

// smb2StatusError is an NTSTATUS failure from the server.
type smb2StatusError struct {
	Op     string
	Name   string
	Status uint32
}

func (e *smb2StatusError) Error() string {
	s := "smb " + e.Op
	if e.Name != "" {
		s += " " + strconv.Quote(e.Name)
	}
	return fmt.Sprintf("%s: %s (0x%08x)", s, smb2StatusText(e.Status), e.Status)
}

// Is maps NTSTATUS codes to the package's sentinel errors.
func (e *smb2StatusError) Is(target error) bool {
	switch target {
	case errSMBAuth:
		switch e.Status {
		case smb2StatusLogonFailure, smb2StatusWrongPassword, smb2StatusNoSuchUser,
			smb2StatusAccountRestriction, smb2StatusInvalidLogonHours, smb2StatusInvalidWorkstation,
			smb2StatusPasswordExpired, smb2StatusAccountDisabled, smb2StatusLogonTypeNotGranted,
			smb2StatusPasswordMustChange, smb2StatusAccountLockedOut:
			return true
		case smb2StatusAccessDenied:
			return e.Op == "sign-in"
		}
	case errSMBAccessDenied:
		return e.Status == smb2StatusAccessDenied && e.Op != "sign-in"
	case errSMBShareNotFound:
		return e.Status == smb2StatusBadNetworkName
	case errSMBNotFound:
		switch e.Status {
		case smb2StatusBadNetworkName, smb2StatusObjectNameNotFound, smb2StatusObjectPathNotFound,
			smb2StatusNoSuchFile, smb2StatusNotADirectory, smb2StatusObjectNameInvalid,
			smb2StatusObjectPathSyntaxBad, smb2StatusDeletePending:
			return true
		}
	case errSMBUnsupported:
		return e.Status == smb2StatusPathNotCovered || e.Status == smb2StatusNotSupported
	}
	return false
}

func smb2StatusText(st uint32) string {
	switch st {
	case smb2StatusAccessDenied:
		return "access denied"
	case smb2StatusLogonFailure, smb2StatusWrongPassword, smb2StatusNoSuchUser:
		return "wrong user name or password"
	case smb2StatusAccountLockedOut:
		return "account locked"
	case smb2StatusAccountDisabled:
		return "account disabled"
	case smb2StatusPasswordExpired, smb2StatusPasswordMustChange:
		return "password expired"
	case smb2StatusAccountRestriction, smb2StatusInvalidLogonHours, smb2StatusInvalidWorkstation, smb2StatusLogonTypeNotGranted:
		return "account may not sign in from here"
	case smb2StatusBadNetworkName:
		return "share not found"
	case smb2StatusObjectNameNotFound, smb2StatusNoSuchFile:
		return "not found"
	case smb2StatusObjectPathNotFound:
		return "folder not found"
	case smb2StatusNotADirectory:
		return "not a folder"
	case smb2StatusFileIsADirectory:
		return "is a folder"
	case smb2StatusObjectNameInvalid, smb2StatusObjectPathSyntaxBad:
		return "invalid name"
	case smb2StatusSharingViolation:
		return "in use"
	case smb2StatusDeletePending:
		return "being deleted"
	case smb2StatusInsufficientResources, smb2StatusRequestNotAccepted:
		return "server busy"
	case smb2StatusPathNotCovered:
		return "DFS link (not supported)"
	case smb2StatusNotSupported:
		return "not supported"
	case smb2StatusInvalidParameter:
		return "invalid parameter"
	case smb2StatusNetworkSessionExpired, smb2StatusUserSessionDeleted:
		return "session expired"
	case smb2StatusNetworkNameDeleted:
		return "share went away"
	}
	return "error"
}

// smbDial connects to host (name or IP; port 0 means 445) and signs in.
// An empty user signs in anonymously, then as "guest". "DOMAIN\user" in
// user fills domain when domain is empty.
func smbDial(ctx context.Context, host string, port int, user, password, domain string) (*smbSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	h, err := smb2CleanHost(host)
	if err != nil {
		return nil, err
	}
	if port == 0 {
		port = 445
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("%w: port %d", errSMBBadPath, port)
	}
	if domain == "" {
		if i := strings.IndexByte(user, '\\'); i > 0 {
			domain, user = user[:i], user[i+1:]
		}
	}
	s := &smbSession{
		addr:     net.JoinHostPort(h, strconv.Itoa(port)),
		uncHost:  h,
		user:     user,
		password: password,
		domain:   domain,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.dialLocked(ctx)
	if err != nil {
		return nil, err
	}
	s.conn = c
	s.gen++
	s.live.Store(c)
	return s, nil
}

// dialLocked opens a new signed-in connection; s.mu is held.
func (s *smbSession) dialLocked(ctx context.Context) (*smb2Conn, error) {
	c, err := smb2Connect(ctx, s.addr, s.uncHost, s.user, s.password, s.domain)
	if err != nil && s.user == "" && s.password == "" && errors.Is(err, errSMBAuth) {
		if gc, gerr := smb2Connect(ctx, s.addr, s.uncHost, "guest", "", ""); gerr == nil {
			return gc, nil
		}
	}
	return c, err
}

// Guest reports whether the server let this session in as guest or
// anonymous (it then only sees public shares, and nothing is signed).
func (s *smbSession) Guest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil && s.conn.guest
}

// run calls fn with a live connection, dialling first when the last one
// was dropped. When a connection that had been idle turns out dead, fn runs
// once more on a fresh one (every operation here is a read, so repeating
// it is safe).
func (s *smbSession) run(ctx context.Context, fn func(c *smb2Conn) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for attempt := 0; ; attempt++ {
		if s.closed || s.shut.Load() {
			return errSMBClosed
		}
		fresh := false
		if s.conn == nil {
			c, err := s.dialLocked(ctx)
			if err != nil {
				return err
			}
			s.conn, fresh = c, true
			s.gen++
			s.live.Store(c)
			if s.shut.Load() {
				return errSMBClosed // Close is waiting for mu and tidies up
			}
		}
		c := s.conn
		err := fn(c)
		if !c.broken {
			return err
		}
		c.nc.Close()
		s.conn = nil
		s.live.CompareAndSwap(c, nil)
		if err != nil && s.shut.Load() {
			return fmt.Errorf("%w (%w)", errSMBClosed, err)
		}
		if err == nil || fresh || attempt > 0 || ctx.Err() != nil || smb2IsTimeout(err) {
			return err
		}
		logInfo("smb", "connection dropped (%v); dialling again", err)
	}
}

// Close logs off and closes the connection. Open files stop working. A
// request in flight is cut off at once (it fails with errSMBClosed), so a
// copy cancelled through Close stops without waiting for a dead server.
func (s *smbSession) Close() error {
	cut := false
	if !s.mu.TryLock() {
		s.shut.Store(true)
		if c := s.live.Load(); c != nil {
			_ = c.nc.Close()
		}
		cut = true
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	s.shut.Store(true)
	if s.closed {
		return nil
	}
	s.closed = true
	c := s.conn
	s.conn = nil
	s.live.Store(nil)
	if c == nil {
		return nil
	}
	if cut {
		_ = c.nc.Close()
		return nil
	}
	if !c.broken {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c.logoff(ctx)
		cancel()
	}
	return c.nc.Close()
}

// ListShares returns the server's file shares (no printers, no IPC$, no
// hidden "$" shares), sorted. errSMBNoShareList (wrapped) means the server
// would not tell, or showed none; ask the user for the share name then.
func (s *smbSession) ListShares(ctx context.Context) ([]string, error) {
	var names []string
	err := s.run(ctx, func(c *smb2Conn) error {
		var err error
		names, err = c.listShares(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%w: no visible file shares", errSMBNoShareList)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names, nil
}

// ReadDir lists dir ("/"-separated, relative to the share root; "" is the
// root) in the server's order, without "." and "..".
func (s *smbSession) ReadDir(ctx context.Context, share, dir string) ([]smbEntry, error) {
	sh, p, err := smb2CleanSharePath(share, dir)
	if err != nil {
		return nil, err
	}
	var out []smbEntry
	err = s.run(ctx, func(c *smb2Conn) error {
		out = out[:0]
		t, err := c.diskTree(ctx, sh)
		if err != nil {
			return err
		}
		o, err := c.create(ctx, t, p, smb2AccessDir, 7, smb2OptDirectory)
		if err != nil {
			return err
		}
		defer c.closeFile(ctx, t, o.fid)
		for page := 0; page < smb2MaxDirPages; page++ {
			buf, done, err := c.queryDir(ctx, t, o.fid, page == 0)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			if out, err = smb2ParseDirInfo(buf, out); err != nil {
				return err
			}
		}
		return smb2Bad("endless folder listing")
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Stat describes one file or folder ("" is the share root).
func (s *smbSession) Stat(ctx context.Context, share, p string) (smbEntry, error) {
	sh, cp, err := smb2CleanSharePath(share, p)
	if err != nil {
		return smbEntry{}, err
	}
	name := sh
	if i := strings.LastIndexByte(cp, '\\'); i >= 0 {
		name = cp[i+1:]
	} else if cp != "" {
		name = cp
	}
	var e smbEntry
	err = s.run(ctx, func(c *smb2Conn) error {
		t, err := c.diskTree(ctx, sh)
		if err != nil {
			return err
		}
		o, err := c.create(ctx, t, cp, smb2AccessAttrs, 7, 0)
		if err != nil {
			return err
		}
		c.closeFile(ctx, t, o.fid)
		e = smbEntry{Name: name, ModTime: o.mtime, IsDir: o.attrs&smb2AttrDirectory != 0, Hidden: smb2HiddenEntry(name, o.attrs)}
		if !e.IsDir {
			e.Size = o.size
		}
		return nil
	})
	return e, err
}

// OpenFile opens a file for reading.
func (s *smbSession) OpenFile(ctx context.Context, share, p string) (*smbFile, error) {
	sh, cp, err := smb2CleanSharePath(share, p)
	if err != nil {
		return nil, err
	}
	if cp == "" {
		return nil, &smb2StatusError{Op: "open", Name: sh, Status: smb2StatusFileIsADirectory}
	}
	f := &smbFile{s: s, share: sh, path: cp}
	if err := s.run(ctx, func(c *smb2Conn) error { return f.openLocked(ctx, c) }); err != nil {
		return nil, err
	}
	return f, nil
}

// openLocked opens (or, after a reconnect, reopens) the handle; s.mu is
// held. A reopened file must still have the size it had.
func (f *smbFile) openLocked(ctx context.Context, c *smb2Conn) error {
	t, err := c.diskTree(ctx, f.share)
	if err != nil {
		return err
	}
	o, err := c.create(ctx, t, f.path, smb2AccessRead, 7, smb2OptNonDirectory)
	if err != nil {
		return err
	}
	if f.gen == 0 {
		f.size = o.size
	} else if o.size != f.size {
		c.closeFile(ctx, t, o.fid)
		return fmt.Errorf("smb: %q changed on the server while it was being read", strings.ReplaceAll(f.path, `\`, "/"))
	}
	f.tree, f.fid, f.gen = t, o.fid, f.s.gen
	return nil
}

// Size is the file's size when it was opened.
func (f *smbFile) Size() int64 { return f.size }

// readChunk does one READ of at most len(p) bytes at off.
func (f *smbFile) readChunk(ctx context.Context, p []byte, off int64) (int, error) {
	n := 0
	err := f.s.run(ctx, func(c *smb2Conn) error {
		if f.closed {
			return os.ErrClosed
		}
		if f.gen != f.s.gen {
			if err := f.openLocked(ctx, c); err != nil {
				return err
			}
		}
		data, err := c.read(ctx, f.tree, f.fid, off, min(len(p), c.readSize()))
		n = copy(p, data)
		return err
	})
	return n, err
}

// ReadAt implements io.ReaderAt (each request waits up to 30 s).
func (f *smbFile) ReadAt(p []byte, off int64) (int, error) {
	return f.ReadAtContext(context.Background(), p, off)
}

// ReadAtContext is ReadAt bounded by ctx.
func (f *smbFile) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("smb: negative offset %d", off)
	}
	n := 0
	for n < len(p) {
		at := off + int64(n)
		if at >= f.size {
			return n, io.EOF
		}
		want := p[n:min(int64(len(p)), int64(n)+f.size-at)]
		var m int
		var err error
		if len(want) < smb2MaxReadChunk/4 {
			m, err = f.cachedRead(ctx, want, at)
		} else {
			m, err = f.readChunk(ctx, want, at)
		}
		n += m
		if err != nil {
			return n, err
		}
		if m == 0 {
			return n, io.EOF
		}
	}
	return n, nil
}

// cachedRead serves a small read from the read-ahead request, fetching the
// next one (up to 1 MiB from at) when at is outside it.
func (f *smbFile) cachedRead(ctx context.Context, p []byte, at int64) (int, error) {
	f.cacheMu.Lock()
	defer f.cacheMu.Unlock()
	if at < f.cacheOff || at >= f.cacheOff+int64(len(f.cache)) {
		if cap(f.cache) < smb2MaxReadChunk {
			f.cache = make([]byte, smb2MaxReadChunk)
		}
		m, err := f.readChunk(ctx, f.cache[:cap(f.cache)], at)
		f.cache, f.cacheOff = f.cache[:m], at
		if m == 0 {
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
	}
	return copy(p, f.cache[at-f.cacheOff:]), nil
}

// Read implements io.Reader.
func (f *smbFile) Read(p []byte) (int, error) {
	f.posMu.Lock()
	defer f.posMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	n, err := f.ReadAtContext(context.Background(), p, f.pos)
	f.pos += int64(n)
	if n > 0 && err == io.EOF {
		err = nil
	}
	return n, err
}

// Seek implements io.Seeker.
func (f *smbFile) Seek(offset int64, whence int) (int64, error) {
	f.posMu.Lock()
	defer f.posMu.Unlock()
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += f.pos
	case io.SeekEnd:
		offset += f.size
	default:
		return 0, fmt.Errorf("smb: bad whence %d", whence)
	}
	if offset < 0 {
		return 0, fmt.Errorf("smb: negative offset %d", offset)
	}
	f.pos = offset
	return offset, nil
}

// WriteTo implements io.WriterTo, so io.Copy moves whole 1 MiB requests.
func (f *smbFile) WriteTo(w io.Writer) (int64, error) {
	f.posMu.Lock()
	defer f.posMu.Unlock()
	var total int64
	buf := make([]byte, smb2MaxReadChunk)
	for f.pos < f.size {
		m, err := f.ReadAtContext(context.Background(), buf[:min(int64(len(buf)), f.size-f.pos)], f.pos)
		if m > 0 {
			n, werr := w.Write(buf[:m])
			total += int64(n)
			f.pos += int64(n)
			if werr != nil {
				return total, werr
			}
		}
		if err == io.EOF || (err == nil && m == 0) {
			break
		}
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// Close closes the handle on the server.
func (f *smbFile) Close() error {
	s := f.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	c := s.conn
	if c == nil || c.broken || f.gen != s.gen {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.closeFile(ctx, f.tree, f.fid)
	if c.broken {
		c.nc.Close()
		s.conn = nil
		s.live.CompareAndSwap(c, nil)
	}
	return nil
}

// smb2CleanHost accepts "nas", "192.168.1.5", "[fe80::1]", "smb://nas" or
// `\\nas` and returns the bare host.
func smb2CleanHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	if len(h) >= 6 && strings.EqualFold(h[:6], "smb://") {
		h = h[6:]
	}
	h = strings.Trim(h, `\/`)
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if h == "" || len(h) > 255 || strings.ContainsAny(h, "/\\@ \t\r\n\x00") {
		return "", fmt.Errorf("%w: server address %q", errSMBBadPath, h)
	}
	return h, nil
}

// smb2CleanSharePath checks a share name and a "/"-separated path and
// returns the path in SMB form (backslashes, no leading separator).
func smb2CleanSharePath(share, p string) (string, string, error) {
	sh := strings.Trim(share, `/\`)
	if sh == "" || len(sh) > 80 || strings.ContainsAny(sh, "/\\:*?\"<>|") || smb2HasControl(sh) {
		return "", "", fmt.Errorf("%w: share name %q", errSMBBadPath, share)
	}
	cp, err := smb2CleanPath(p)
	return sh, cp, err
}

// smb2CleanPath turns "a/b/c" into `a\b\c`. It refuses ".." (and other
// all-dot names), NUL and control characters, alternate data streams
// (":") and wildcards, so a path can never leave the share root or name
// something other than one file. Backslashes count as separators too.
func smb2CleanPath(p string) (string, error) {
	if smb2HasControl(p) {
		return "", fmt.Errorf("%w: %q", errSMBBadPath, p)
	}
	parts := strings.Split(strings.ReplaceAll(p, `\`, "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if strings.Trim(part, ". ") == "" || strings.ContainsAny(part, ":*?\"<>|") {
			return "", fmt.Errorf("%w: %q", errSMBBadPath, p)
		}
		out = append(out, part)
	}
	return strings.Join(out, `\`), nil
}

func smb2HasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// smb2ValidName reports whether a name from a folder listing is one plain
// path element (a hostile server could send "..", "a/b" or "c:d") that
// smb2CleanPath would also accept, so every listed entry can be opened.
func smb2ValidName(name string) bool {
	if strings.Trim(name, ". ") == "" || smb2HasControl(name) {
		return false
	}
	return !strings.ContainsAny(name, "/\\:*?\"<>|")
}

// smb2HiddenEntry reports entries a game browser should not show by
// default.
func smb2HiddenEntry(name string, attrs uint32) bool {
	if attrs&(smb2AttrHidden|smb2AttrSystem) != 0 || strings.HasPrefix(name, ".") {
		return true
	}
	switch strings.ToLower(name) {
	case "@eadir", "#recycle", "#snapshot", "@recycle", "@recently-snapshot", "@sharebin", "@tmp",
		"$recycle.bin", "system volume information", "thumbs.db", "desktop.ini", "lost+found":
		return true
	}
	return false
}

// smbErrorText turns an error from this client into one plain sentence for
// the screen.
func smbErrorText(err error) string {
	if err == nil {
		return ""
	}
	var se *smb2StatusError
	errors.As(err, &se)
	var dnsErr *net.DNSError
	low := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.Canceled):
		return "Cancelled."
	case errors.Is(err, errSMBv1Only):
		return "This server only speaks SMB1, an old and unsafe protocol JukaHub does not use. Turn on SMB2 or SMB3 in the server's file sharing settings."
	case errors.Is(err, errSMBEncryptionRequired):
		return "The server requires encryption JukaHub cannot do here (it needs SMB 3.1.1 and a real account, not guest). Sign in with a user account, or turn off \"require encryption\" for this share."
	case errors.Is(err, errSMBAuth):
		if se != nil {
			switch se.Status {
			case smb2StatusAccountLockedOut:
				return "This account is locked on the server (too many wrong passwords). Unlock it there, then try again."
			case smb2StatusAccountDisabled:
				return "This account is turned off on the server."
			case smb2StatusPasswordExpired, smb2StatusPasswordMustChange:
				return "The password of this account has expired. Change it on the server, then try again."
			case smb2StatusAccountRestriction, smb2StatusInvalidLogonHours, smb2StatusInvalidWorkstation, smb2StatusLogonTypeNotGranted:
				return "The server does not let this account sign in from here."
			}
		}
		return "Wrong user name or password."
	case errors.Is(err, errSMBNoShareList):
		return "The server would not list its shares. Type the share name instead."
	case errors.Is(err, errSMBShareNotFound):
		return "The server has no share with that name. Check the spelling."
	case errors.Is(err, errSMBAccessDenied):
		return "The server refused access: this account may not read that share or file."
	case errors.Is(err, errSMBNotFound):
		return "That file or folder is not on the server."
	case errors.Is(err, errSMBBadPath):
		return "That name or path is not allowed."
	case errors.Is(err, errSMBSignature):
		return "The server's reply failed its security check, so it was not trusted. Try again; if it keeps happening, something on the network is changing the data."
	case errors.Is(err, errSMBUnsupported):
		return "The server asked for something this client does not support (" + err.Error() + ")."
	case smb2IsTimeout(err):
		return "The server did not answer in time. Check that it is switched on and on the same network."
	case errors.As(err, &dnsErr):
		return "No server with that name was found. Try its IP address instead."
	case strings.Contains(low, "refused"):
		return "The server refused the connection. Check that file sharing (SMB) is turned on and the port is right."
	case strings.Contains(low, "unreachable") || strings.Contains(low, "no route"):
		return "The server cannot be reached. Check the address and the Wi-Fi."
	case errors.Is(err, errSMBClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, os.ErrClosed) || strings.Contains(low, "reset"):
		return "The connection to the server was closed. Try again."
	case errors.Is(err, errSMBProtocol):
		return "The server sent a reply JukaHub could not understand."
	case se != nil:
		switch se.Status {
		case smb2StatusFileIsADirectory:
			return "That is a folder, not a file."
		case smb2StatusSharingViolation:
			return "Another program on the server is using that file."
		case smb2StatusInsufficientResources, smb2StatusRequestNotAccepted:
			return "The server is busy or has too many connections. Try again in a moment."
		}
	}
	return "Network drive error: " + err.Error()
}
