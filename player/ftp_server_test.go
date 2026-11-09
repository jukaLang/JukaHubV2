package main

// Tests for the Tools FTP server: sign-in, the address lockout, the virtual
// root of shares and its confinement, and real sessions over loopback
// (passive mode, STOR, RETR, LIST, SIZE, DELE).

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testFTPSession is a session over one share, "Roms", for resolve tests (no
// root is opened: resolve never touches the disk).
func testFTPSession(cwd string) *ftpSession {
	set := &ftpShareSet{list: []*ftpShare{{name: "Roms", dir: "/card/Roms"}, {name: "Music", dir: "/card/Music"}}}
	return &ftpSession{shares: set, cwd: cwd}
}

// The virtual root lists the shares; every path is cleaned as a rooted path,
// so ".." stops at "/", and only a share's name leads anywhere.
func TestFTPResolveConfinesToShares(t *testing.T) {
	cases := []struct {
		cwd, in   string
		virtual   string
		share     string // "" = the virtual root
		rel       string
		wantError error
	}{
		{cwd: "/", in: "/", virtual: "/"},
		{cwd: "/", in: "", virtual: "/"},
		{cwd: "/", in: "/Roms", virtual: "/Roms", share: "Roms", rel: "."},
		{cwd: "/", in: "roms/GBA", virtual: "/Roms/GBA", share: "Roms", rel: filepath.FromSlash("GBA")},
		{cwd: "/", in: `"/Music/My Album"`, virtual: "/Music/My Album", share: "Music", rel: "My Album"},
		{cwd: "/Roms/GBA", in: "../x.gba", virtual: "/Roms/x.gba", share: "Roms", rel: "x.gba"},
		{cwd: "/Roms", in: "..", virtual: "/"},
		{cwd: "/", in: "../secret.txt", wantError: errFTPNotFound},
		{cwd: "/", in: "/Roms/../../etc/passwd", wantError: errFTPNotFound},
		{cwd: "/Roms/GBA", in: "../../Apps/JukaHub/data/jukauser.json", wantError: errFTPNotFound},
		{cwd: "/", in: "/Apps", wantError: errFTPNotFound},
		{cwd: "/", in: "/System/starts/run.sh", wantError: errFTPNotFound},
		{cwd: "/", in: "bad\x00name", wantError: errFTPBadPath},
	}
	for _, tc := range cases {
		sess := testFTPSession(tc.cwd)
		got, err := sess.resolve(tc.in)
		if tc.wantError != nil {
			if !errors.Is(err, tc.wantError) {
				t.Errorf("resolve(%q) from %s: error %v, want %v", tc.in, tc.cwd, err, tc.wantError)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolve(%q) from %s: %v", tc.in, tc.cwd, err)
			continue
		}
		share := ""
		if got.share != nil {
			share = got.share.name
		}
		if got.virtual != tc.virtual || share != tc.share || (tc.share != "" && got.rel != tc.rel) {
			t.Errorf("resolve(%q) from %s = {%q %q %q}, want {%q %q %q}",
				tc.in, tc.cwd, got.virtual, share, got.rel, tc.virtual, tc.share, tc.rel)
		}
	}
}

// On Windows a component holding ':' or '\' could name a drive, a stream or a
// path of its own inside os.Root's argument.
func TestFTPResolveRefusesWindowsSeparators(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("':' and '\\' are ordinary name characters outside Windows")
	}
	sess := testFTPSession("/")
	for _, in := range []string{`/Roms/C:\Windows`, `/Roms/a\..\..\x`, "/Roms/file.txt:stream"} {
		if _, err := sess.resolve(in); !errors.Is(err, errFTPBadPath) {
			t.Errorf("resolve(%q) error = %v, want errFTPBadPath", in, err)
		}
	}
}

// Only the card's user folders are shared, matched in any case; programs the
// device runs (Apps, Emus, RetroArch, System…), plain files and symlinks are not.
func TestFTPSharesForPicksUserFolders(t *testing.T) {
	card := t.TempDir()
	for _, name := range []string{"Roms", "bios", "Saves", "Music", "Apps", "Emus", "RetroArch", "System", "PortMaster", "Themes"} {
		if err := os.Mkdir(filepath.Join(card, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(card, "Videos"), []byte("a file, not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(t.TempDir(), filepath.Join(card, "Pictures")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}
	var got []string
	for _, sh := range ftpSharesFor(card, true) {
		got = append(got, sh.name)
		if sh.dir != filepath.Join(card, sh.name) {
			t.Errorf("share %s maps to %s", sh.name, sh.dir)
		}
	}
	sort.Strings(got)
	want := []string{"Music", "Roms", "Saves", "bios"}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("shares = %v, want %v", got, want)
	}

	desk := ftpSharesFor(card, false)
	if len(desk) != 1 || desk[0].name != ftpDesktopShareName || desk[0].dir != card {
		t.Fatalf("desktop shares = %+v, want the folder itself as %q", desk, ftpDesktopShareName)
	}
	if n := len(ftpAddShare([]ftpShare{{name: "Roms"}}, ftpShare{name: "ROMS"})); n != 1 {
		t.Fatalf("a second share named ROMS was added next to Roms (%d shares)", n)
	}
	if _, err := openFTPShares(nil); err == nil {
		t.Fatal("starting with nothing to share must fail")
	}
}

// Without an SD card the server must not share the app folder (it holds
// jukauser.json and saved credentials); a dedicated "shared" folder is used.
func TestFTPRootDirUsesDesktopShare(t *testing.T) {
	if _, err := os.Stat("/mnt/SDCARD"); err == nil {
		t.Skip("this machine has the handheld SD card mount")
	}
	t.Setenv("JUKAHUB_FTP_ROOT", "")
	root := ftpRootDir()
	if filepath.Base(root) != "shared" {
		t.Fatalf("ftpRootDir() = %q, want a shared/ folder", root)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("shared folder %q is missing: %v", root, err)
	}
}

// A symlink inside a share that points out of it passes the lexical check;
// the share's os.Root is what refuses it.
func TestFTPShareRootRefusesEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs extra privileges on Windows")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inside.txt"), []byte("yes"), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := openFTPShares([]ftpShare{{name: "Roms", dir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(set.close)
	sess := &ftpSession{shares: set, cwd: "/"}
	for name, wantOK := range map[string]bool{"/Roms/inside.txt": true, "/Roms/link/secret.txt": false} {
		p, err := sess.resolve(name)
		if err != nil {
			t.Fatalf("resolve(%q): %v", name, err)
		}
		f, err := p.share.root.Open(p.rel)
		if f != nil {
			f.Close()
		}
		if (err == nil) != wantOK {
			t.Errorf("open %s: err = %v, want ok = %v", name, err, wantOK)
		}
	}
}

// A wrong user name or password is refused, anonymous included; the third
// wrong try ends the connection; the generated pair signs in.
func TestFTPRequiresThePassword(t *testing.T) {
	srv, _ := startTestFTP(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port()))
	if srv.Password() == "" || srv.Password() == srv.User() {
		t.Fatalf("no generated password: %q", srv.Password())
	}

	c := newFTPTestClient(t, addr)
	if resp := c.cmd("PWD"); !strings.HasPrefix(resp, "530") {
		t.Fatalf("PWD before signing in = %q, want 530", resp)
	}
	if resp := c.cmd("PASS %s", srv.Password()); !strings.HasPrefix(resp, "503") {
		t.Fatalf("PASS without USER = %q, want 503", resp)
	}
	c.cmd("USER anonymous")
	if resp := c.cmd("PASS guest@example.com"); !strings.HasPrefix(resp, "530") {
		t.Fatalf("anonymous login = %q, want 530", resp)
	}
	c.cmd("USER %s", srv.User())
	if resp := c.cmd("PASS wrong-password"); !strings.HasPrefix(resp, "530") {
		t.Fatalf("wrong password = %q, want 530", resp)
	}
	c.cmd("USER someone-else")
	if resp := c.cmd("PASS %s", srv.Password()); !strings.HasPrefix(resp, "421") {
		t.Fatalf("third wrong sign-in (right password, wrong user) = %q, want 421", resp)
	}
	if _, err := c.r.ReadString('\n'); err == nil {
		t.Fatal("the connection stayed open after three wrong sign-ins")
	}
	c.close()

	ok := newFTPTestClient(t, addr)
	defer ok.close()
	ok.login(srv)
	if resp := ok.cmd("PWD"); !strings.HasPrefix(resp, `257 "/"`) {
		t.Fatalf("PWD after signing in = %q", resp)
	}
	if got := srv.Clients(); len(got) != 1 || got[0].User != sshUserName {
		t.Fatalf("clients = %+v, want one signed-in client", got)
	}
}

// Ten wrong passwords from one address lock it out for the lockout period,
// across connections; other addresses are not affected.
func TestFTPLoginLimiterLocksAnAddress(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := &ftpLoginLimiter{now: func() time.Time { return now }}
	for i := 1; i < ftpLoginMaxFails; i++ {
		if l.fail("10.0.0.2") {
			t.Fatalf("locked out after %d wrong passwords", i)
		}
	}
	if !l.fail("10.0.0.2") {
		t.Fatalf("not locked out after %d wrong passwords", ftpLoginMaxFails)
	}
	if locked, left := l.locked("10.0.0.2"); !locked || left <= 0 || left > ftpLoginLockout {
		t.Fatalf("locked = %v, %v; want locked for at most %v", locked, left, ftpLoginLockout)
	}
	if locked, _ := l.locked("10.0.0.3"); locked {
		t.Fatal("another address is locked out too")
	}
	now = now.Add(ftpLoginLockout + time.Second)
	if locked, _ := l.locked("10.0.0.2"); locked {
		t.Fatal("still locked out after the lockout")
	}

	// Wrong passwords further apart than the window never add up.
	slow := &ftpLoginLimiter{now: func() time.Time { return now }}
	for i := 0; i < 3*ftpLoginMaxFails; i++ {
		now = now.Add(ftpLoginWindow + time.Second)
		if slow.fail("10.0.0.4") {
			t.Fatalf("locked out by failures %v apart", ftpLoginWindow+time.Second)
		}
	}
	// A good sign-in forgets the count.
	for i := 1; i < ftpLoginMaxFails; i++ {
		slow.fail("10.0.0.5")
	}
	slow.succeed("10.0.0.5")
	if slow.fail("10.0.0.5") {
		t.Fatal("the count survived a good sign-in")
	}
	// The table stays bounded however many addresses try.
	for i := 0; i < 3*ftpLoginMaxAddrs; i++ {
		slow.fail(fmt.Sprintf("10.1.%d.%d", i/250, i%250))
	}
	if n := len(slow.byIP); n > ftpLoginMaxAddrs {
		t.Fatalf("limiter remembers %d addresses, want at most %d", n, ftpLoginMaxAddrs)
	}
}

// "/" cannot be written: files and folders go inside a share, and the shares
// themselves cannot be deleted, renamed or removed.
func TestFTPRootIsReadOnly(t *testing.T) {
	srv, _ := startTestFTP(t)
	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()
	c.login(srv)
	for _, cmd := range []string{"MKD /New", "MKD /Roms", "DELE /Roms", "RMD /Roms", "RNFR /Roms", "RMD /", "SIZE /"} {
		if resp := c.cmd("%s", cmd); !strings.HasPrefix(resp, "550") {
			t.Errorf("%s = %q, want 550", cmd, resp)
		}
	}
	data := c.passive()
	if resp := c.cmd("STOR /dropped.txt"); !strings.HasPrefix(resp, "550") {
		t.Errorf("STOR at the top level = %q, want 550", resp)
	}
	_ = data.Close()

	data = c.passive()
	c.send("LIST /")
	if resp := c.read(); !strings.HasPrefix(resp, "150") {
		t.Fatalf("LIST / = %q", resp)
	}
	listing, _ := io.ReadAll(data)
	_ = data.Close()
	c.read()
	if !regexp.MustCompile(`(?m)^d\S+ .* Roms\r$`).Match(listing) {
		t.Fatalf("the root listing does not show Roms as a folder:\n%s", listing)
	}
}

// A line without a newline is drained, not collected: anyone could otherwise
// fill the handheld's memory before signing in.
func TestFTPReadLineRefusesHugeLines(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader(strings.Repeat("A", 3*ftpMaxLineBytes)+"\r\nNOOP\r\n"), ftpMaxLineBytes)
	if _, err := ftpReadLine(r); !errors.Is(err, errFTPLineTooLong) {
		t.Fatalf("a %d-byte line: err = %v, want errFTPLineTooLong", 3*ftpMaxLineBytes, err)
	}
	if line, err := ftpReadLine(r); err != nil || line != "NOOP\r\n" {
		t.Fatalf("the next line = %q, %v; want NOOP", line, err)
	}
}

// Folders are listed with the directory type letter; Perm() alone dropped it.
func TestFTPListLineMarksFolders(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	di, _ := os.Stat(dir)
	fi, _ := os.Stat(file)
	now := time.Now()
	if line := ftpListLine(di, "GBA", now); !strings.HasPrefix(line, "d") {
		t.Errorf("folder line = %q, want a leading d", line)
	}
	if line := ftpListLine(fi, "a.txt", now); !strings.HasPrefix(line, "-") {
		t.Errorf("file line = %q, want a leading -", line)
	}
	if line := ftpListLine(fi, "a.txt", now.AddDate(2, 0, 0)); !strings.Contains(line, fmt.Sprint(fi.ModTime().Year())) {
		t.Errorf("an old file's line = %q, want its year", line)
	}
}

// startTestFTP serves one share, "Roms", over a temp folder, with no delay on
// wrong passwords, and returns the server and the folder.
func startTestFTP(t *testing.T) (*ftpService, string) {
	t.Helper()
	prev := ftpAuthFailDelay
	ftpAuthFailDelay = 0
	t.Cleanup(func() { ftpAuthFailDelay = prev })
	dir := t.TempDir()
	srv := &ftpService{conns: make(map[net.Conn]struct{})}
	if err := srv.startShares(dir, []ftpShare{{name: "Roms", dir: dir}}, "127.0.0.1:0"); err != nil {
		t.Fatalf("start FTP: %v", err)
	}
	t.Cleanup(srv.Stop)
	if !srv.Running() {
		t.Fatal("server should report running after start")
	}
	return srv, dir
}

type ftpTestClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func newFTPTestClient(t *testing.T, addr string) *ftpTestClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	c := &ftpTestClient{t: t, conn: conn, r: bufio.NewReader(conn)}
	if resp := c.read(); !strings.HasPrefix(resp, "220") {
		t.Fatalf("greeting = %q, want 220", resp)
	}
	return c
}

func (c *ftpTestClient) close() { _ = c.conn.Close() }

func (c *ftpTestClient) send(format string, args ...interface{}) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, format+"\r\n", args...); err != nil {
		c.t.Fatalf("send %q: %v", format, err)
	}
}

func (c *ftpTestClient) read() string {
	c.t.Helper()
	line, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read reply: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

func (c *ftpTestClient) cmd(format string, args ...interface{}) string {
	c.t.Helper()
	c.send(format, args...)
	return c.read()
}

// login signs in with the server's generated account.
func (c *ftpTestClient) login(srv *ftpService) {
	c.t.Helper()
	if resp := c.cmd("USER %s", srv.User()); !strings.HasPrefix(resp, "331") {
		c.t.Fatalf("USER = %q", resp)
	}
	if resp := c.cmd("PASS %s", srv.Password()); !strings.HasPrefix(resp, "230") {
		c.t.Fatalf("PASS = %q", resp)
	}
}

// pasvAddr sends PASV and returns the data address it names.
func (c *ftpTestClient) pasvAddr() string {
	c.t.Helper()
	resp := c.cmd("PASV")
	re := regexp.MustCompile(`\((\d+),(\d+),(\d+),(\d+),(\d+),(\d+)\)`)
	m := re.FindStringSubmatch(resp)
	if m == nil {
		c.t.Fatalf("PASV reply = %q", resp)
	}
	nums := make([]int, 6)
	for i := 0; i < 6; i++ {
		nums[i], _ = strconv.Atoi(m[i+1])
	}
	host := fmt.Sprintf("%d.%d.%d.%d", nums[0], nums[1], nums[2], nums[3])
	return net.JoinHostPort(host, strconv.Itoa(nums[4]*256+nums[5]))
}

// passive sends PASV, connects the data socket and returns it.
func (c *ftpTestClient) passive() net.Conn {
	c.t.Helper()
	conn, err := net.Dial("tcp", c.pasvAddr())
	if err != nil {
		c.t.Fatalf("connect data socket: %v", err)
	}
	return conn
}

func TestFTPServerUploadDownload(t *testing.T) {
	srv, root := startTestFTP(t)

	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()

	c.login(srv)
	if resp := c.cmd("PWD"); !strings.HasPrefix(resp, `257 "/"`) {
		t.Fatalf("PWD = %q", resp)
	}
	if resp := c.cmd("PORT 127,0,0,1,10,10"); !strings.HasPrefix(resp, "502") {
		t.Fatalf("PORT must be refused in passive-only mode, got %q", resp)
	}
	if resp := c.cmd("CWD Roms"); !strings.HasPrefix(resp, `250 "/Roms"`) {
		t.Fatalf("CWD Roms = %q", resp)
	}

	payload := "hello from the JukaHub FTP test"
	data := c.passive()
	c.send("STOR hello.txt")
	if resp := c.read(); !strings.HasPrefix(resp, "150") {
		t.Fatalf("STOR = %q", resp)
	}
	if _, err := io.WriteString(data, payload); err != nil {
		t.Fatalf("write data: %v", err)
	}
	if err := data.Close(); err != nil {
		t.Fatalf("close data: %v", err)
	}
	if resp := c.read(); !strings.HasPrefix(resp, "226") {
		t.Fatalf("STOR completion = %q", resp)
	}
	if got, err := os.ReadFile(filepath.Join(root, "hello.txt")); err != nil || string(got) != payload {
		t.Fatalf("stored file = %q (err %v), want %q", got, err, payload)
	}

	if resp := c.cmd("SIZE hello.txt"); resp != fmt.Sprintf("213 %d", len(payload)) {
		t.Fatalf("SIZE = %q", resp)
	}

	data = c.passive()
	c.send("RETR hello.txt")
	if resp := c.read(); !strings.HasPrefix(resp, "150") {
		t.Fatalf("RETR = %q", resp)
	}
	body, err := io.ReadAll(data)
	_ = data.Close()
	if err != nil {
		t.Fatalf("read downloaded data: %v", err)
	}
	if resp := c.read(); !strings.HasPrefix(resp, "226") {
		t.Fatalf("RETR completion = %q", resp)
	}
	if string(body) != payload {
		t.Fatalf("downloaded %q, want %q", body, payload)
	}

	data = c.passive()
	c.send("LIST")
	if resp := c.read(); !strings.HasPrefix(resp, "150") {
		t.Fatalf("LIST = %q", resp)
	}
	listing, err := io.ReadAll(data)
	_ = data.Close()
	if err != nil {
		t.Fatalf("read listing: %v", err)
	}
	if resp := c.read(); !strings.HasPrefix(resp, "226") {
		t.Fatalf("LIST completion = %q", resp)
	}
	if !strings.Contains(string(listing), "hello.txt") {
		t.Fatalf("listing does not mention the uploaded file:\n%s", listing)
	}

	if resp := c.cmd("DELE hello.txt"); !strings.HasPrefix(resp, "250") {
		t.Fatalf("DELE = %q", resp)
	}
	if _, err := os.Stat(filepath.Join(root, "hello.txt")); !os.IsNotExist(err) {
		t.Fatal("DELE did not remove the uploaded file")
	}

	if resp := c.cmd("QUIT"); !strings.HasPrefix(resp, "221") {
		t.Fatalf("QUIT = %q", resp)
	}
}

// REST before STOR resumes an upload at the offset (lftp `put -c`, Python's
// storbinary with rest=). The offset used to be ignored: the tail was written
// at byte 0 over a truncated file and the client reported success.
func TestFTPStoreHonoursRestart(t *testing.T) {
	srv, root := startTestFTP(t)
	if err := os.WriteFile(filepath.Join(root, "resume.bin"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()
	c.login(srv)

	data := c.passive()
	if resp := c.cmd("REST 5"); !strings.HasPrefix(resp, "350") {
		t.Fatalf("REST = %q", resp)
	}
	c.send("STOR /Roms/resume.bin")
	if resp := c.read(); !strings.HasPrefix(resp, "150") {
		t.Fatalf("STOR = %q", resp)
	}
	if _, err := io.WriteString(data, "abcde"); err != nil {
		t.Fatalf("write data: %v", err)
	}
	_ = data.Close()
	if resp := c.read(); !strings.HasPrefix(resp, "226") {
		t.Fatalf("STOR completion = %q", resp)
	}
	if got, err := os.ReadFile(filepath.Join(root, "resume.bin")); err != nil || string(got) != "01234abcde" {
		t.Fatalf("resumed file = %q (err %v), want %q", got, err, "01234abcde")
	}
}

// FileZilla and friends send LIST with flags when hidden files are enabled;
// those flags are not a path and must not break the listing.
func TestFTPListAcceptsFlags(t *testing.T) {
	srv, root := startTestFTP(t)
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()
	c.login(srv)
	c.cmd("CWD /Roms")

	for _, list := range []string{"LIST -la", "LIST -a -l", "LIST -a -l /Roms"} {
		data := c.passive()
		c.send("%s", list)
		if resp := c.read(); !strings.HasPrefix(resp, "150") {
			t.Fatalf("%s = %q, want a listing", list, resp)
		}
		listing, err := io.ReadAll(data)
		_ = data.Close()
		if err != nil {
			t.Fatalf("read listing: %v", err)
		}
		if resp := c.read(); !strings.HasPrefix(resp, "226") {
			t.Fatalf("%s completion = %q", list, resp)
		}
		if !strings.Contains(string(listing), "visible.txt") {
			t.Fatalf("%s did not list the share:\n%s", list, listing)
		}
	}
}

// Roms/Ports holds the PortMaster scripts the Ports panel runs with bash: FTP
// may read them but not change, add, remove or rename anything there — in any
// case, with the trailing dot FAT drops, or by moving a file in.
func TestFTPPortsFolderIsReadOnly(t *testing.T) {
	srv, root := startTestFTP(t)
	ports := filepath.Join(root, "PORTS")
	if err := os.MkdirAll(filepath.Join(ports, "game"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho port\n"
	if err := os.WriteFile(filepath.Join(ports, "Game.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "evil.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()
	c.login(srv)
	for _, cmd := range []string{
		"DELE /Roms/PORTS/Game.sh", "DELE /Roms/ports/Game.sh", "MKD /Roms/Ports/new", "MKD /Roms/Ports.",
		"RMD /Roms/PORTS/game", "RMD /Roms/PORTS", "RNFR /Roms/PORTS/Game.sh", "RNFR /Roms/PORTS",
	} {
		if resp := c.cmd("%s", cmd); !strings.HasPrefix(resp, "550") || !strings.Contains(resp, "SFTP") {
			t.Errorf("%s = %q, want a 550 that points at SFTP", cmd, resp)
		}
	}
	if resp := c.cmd("RNFR /Roms/evil.sh"); !strings.HasPrefix(resp, "350") {
		t.Fatalf("RNFR outside Ports = %q", resp)
	}
	if resp := c.cmd("RNTO /Roms/PORTS/evil.sh"); !strings.HasPrefix(resp, "550") {
		t.Errorf("moving a script into Ports = %q, want 550", resp)
	}
	data := c.passive()
	if resp := c.cmd("STOR /Roms/Ports/Game.sh"); !strings.HasPrefix(resp, "550") {
		t.Errorf("STOR into Ports = %q, want 550", resp)
	}
	_ = data.Close()
	if resp := c.cmd("SIZE /Roms/PORTS/Game.sh"); resp != fmt.Sprintf("213 %d", len(script)) {
		t.Errorf("reading Ports: SIZE = %q", resp)
	}
	if got, err := os.ReadFile(filepath.Join(ports, "Game.sh")); err != nil || string(got) != script {
		t.Errorf("the port script changed: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(ports, "evil.sh")); !os.IsNotExist(err) {
		t.Errorf("a script was moved into Ports: %v", err)
	}
	for _, rest := range []string{"Ports", "PORTS/x.sh", "ports./x", "Ports ./x"} {
		if !ftpProtected("Roms", rest) {
			t.Errorf("ftpProtected(Roms, %q) = false", rest)
		}
	}
	for _, tc := range [][2]string{{"Roms", "GBA/Ports"}, {"Roms", "Portsmouth.gba"}, {"Music", "Ports"}} {
		if ftpProtected(tc[0], tc[1]) {
			t.Errorf("ftpProtected(%s, %q) = true", tc[0], tc[1])
		}
	}
}

// A game profile's folder inside a share is as read-only as Ports: the folder,
// what is in it, and the folders above it a rename could swap. A profile whose
// folder is a whole share, or the card, does not lock everything.
func TestFTPLocksGameProfileFolders(t *testing.T) {
	card := filepath.Join(string(filepath.Separator), "card")
	roms := filepath.Join(card, "Roms")
	set := &ftpShareSet{list: []*ftpShare{{name: "Roms", dir: roms}}}
	game := filepath.Join(roms, "PC", "My Game")
	set.locked = set.keepInside([]string{
		ftpFoldPath(game), ftpFoldPath(roms), ftpFoldPath(card), ftpFoldPath(filepath.Join(card, "Games", "x")),
	})
	if len(set.locked) != 1 {
		t.Fatalf("locked = %v, want only the folder inside Roms", set.locked)
	}
	for _, p := range []string{game, filepath.Join(game, "game.sh"), filepath.Join(roms, "pc", "MY GAME."), filepath.Join(roms, "PC")} {
		if !set.lockedPath(p) {
			t.Errorf("lockedPath(%q) = false", p)
		}
	}
	for _, p := range []string{filepath.Join(roms, "PC", "Other"), filepath.Join(roms, "PC", "My Game 2"), filepath.Join(roms, "GBA", "a.gba")} {
		if set.lockedPath(p) {
			t.Errorf("lockedPath(%q) = true", p)
		}
	}
	sess := &ftpSession{shares: set, cwd: "/"}
	if p, err := sess.resolve("/Roms/PC/My Game/game.sh"); err != nil || !p.protected {
		t.Errorf("resolve into the profile folder: %+v, %v", p, err)
	}
	if p, err := sess.resolve("/Roms/GBA/a.gba"); err != nil || p.protected {
		t.Errorf("resolve elsewhere: %+v, %v", p, err)
	}
}

// RFC 959: REST applies to the transfer right after it and RNFR to the RNTO
// right after it; an unrelated command in between drops them.
func TestFTPDropsAStaleRestartAndRename(t *testing.T) {
	srv, root := startTestFTP(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()
	c.login(srv)
	if resp := c.cmd("REST 5"); !strings.HasPrefix(resp, "350") {
		t.Fatalf("REST = %q", resp)
	}
	c.cmd("CWD /Roms")
	data := c.passive()
	c.send("STOR a.txt")
	if resp := c.read(); !strings.HasPrefix(resp, "150") {
		t.Fatalf("STOR = %q", resp)
	}
	_, _ = io.WriteString(data, "new")
	_ = data.Close()
	if resp := c.read(); !strings.HasPrefix(resp, "226") {
		t.Fatalf("STOR completion = %q", resp)
	}
	if got, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(got) != "new" {
		t.Fatalf("after REST, CWD, STOR the file is %q (%v); the stale offset applied", got, err)
	}
	if resp := c.cmd("RNFR a.txt"); !strings.HasPrefix(resp, "350") {
		t.Fatalf("RNFR = %q", resp)
	}
	c.cmd("PWD")
	if resp := c.cmd("RNTO b.txt"); !strings.HasPrefix(resp, "503") {
		t.Fatalf("RNTO after an unrelated command = %q, want 503", resp)
	}
}

// Quotes in a path are doubled in 257/250 replies (RFC 959), and a path with
// a line break is refused instead of breaking the reply line.
func TestFTPQuotesPathsInReplies(t *testing.T) {
	if got := ftpQuoted(`/Roms/say "hi"`); got != `"/Roms/say ""hi"""` {
		t.Errorf("ftpQuoted = %s", got)
	}
	srv, root := startTestFTP(t)
	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()
	c.login(srv)
	if resp := c.cmd("%s", "MKD /Roms/a\rb"); !strings.HasPrefix(resp, "550") {
		t.Errorf("MKD with a CR = %q, want 550", resp)
	}
	if runtime.GOOS == "windows" {
		return // '"' is not allowed in Windows file names
	}
	if resp := c.cmd(`MKD /Roms/say "hi"`); resp != `257 "/Roms/say ""hi""" created` {
		t.Errorf("MKD reply = %q", resp)
	}
	if _, err := os.Stat(filepath.Join(root, `say "hi"`)); err != nil {
		t.Fatalf("MKD did not make the folder: %v", err)
	}
	c.cmd(`CWD /Roms/say "hi"`)
	if resp := c.cmd("PWD"); resp != `257 "/Roms/say ""hi""" is the current directory` {
		t.Errorf("PWD reply = %q", resp)
	}
}

// What clients send by themselves to keep a connection open is not use: it
// must not hold off the idle stop. A real command is.
func TestFTPKeepalivesDoNotCountAsUse(t *testing.T) {
	srv, _ := startTestFTP(t)
	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()
	c.login(srv)
	c.cmd("PWD")
	before := srv.activity.lastUsed()
	time.Sleep(20 * time.Millisecond)
	for _, cmd := range []string{"NOOP", "PWD", "XPWD", "TYPE I", "SYST", "OPTS UTF8 ON", "MODE S", "STRU F", "STAT", "HELP"} {
		c.cmd("%s", cmd)
	}
	if after := srv.activity.lastUsed(); !after.Equal(before) {
		t.Fatalf("keepalives moved the idle clock from %v to %v", before, after)
	}
	c.cmd("CWD /Roms")
	if after := srv.activity.lastUsed(); !after.After(before) {
		t.Fatal("CWD did not count as use")
	}
}

// A connection that never signs in is dropped ftpLoginWait after it
// connected, however often it sends a command.
func TestFTPSignInTimeCountsFromConnecting(t *testing.T) {
	prev := ftpAuthFailDelay
	ftpAuthFailDelay = 0
	t.Cleanup(func() { ftpAuthFailDelay = prev })
	dir := t.TempDir()
	srv := &ftpService{conns: make(map[net.Conn]struct{}), loginWait: 400 * time.Millisecond}
	if err := srv.startShares(dir, []ftpShare{{name: "Roms", dir: dir}}, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	// Before dialing: the server's sign-in time starts at its greeting, so a
	// start taken after reading the greeting could be late by a slow read and
	// see an on-time drop as too early.
	start := time.Now()
	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()
	for time.Since(start) < 5*time.Second {
		_ = c.conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := fmt.Fprintf(c.conn, "NOOP\r\n"); err != nil {
			return
		}
		if _, err := c.r.ReadString('\n'); err != nil {
			if time.Since(start) < 300*time.Millisecond {
				t.Fatalf("dropped after %v, before its sign-in time", time.Since(start))
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("a connection that never signed in stayed open by sending NOOPs")
}

// Stop ends what sessions have open on the data side too: a passive port
// waiting for its connection and a transfer under way.
func TestFTPStopEndsDataConnections(t *testing.T) {
	srv, _ := startTestFTP(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port()))
	waiting := newFTPTestClient(t, addr)
	defer waiting.close()
	waiting.login(srv)
	pending := waiting.pasvAddr()
	// LIST waits in Accept for a data connection that never comes; closing
	// the control connection alone would not end that wait.
	waiting.send("LIST")

	c := newFTPTestClient(t, addr)
	defer c.close()
	c.login(srv)
	data := c.passive()
	defer data.Close()
	c.send("STOR /Roms/big.bin")
	if resp := c.read(); !strings.HasPrefix(resp, "150") {
		t.Fatalf("STOR = %q", resp)
	}
	if _, err := io.WriteString(data, "partial"); err != nil {
		t.Fatal(err)
	}

	srv.Stop()
	_ = data.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := data.Read(make([]byte, 1)); err == nil {
		t.Error("the upload's data connection is still open after Stop")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Error("the upload's data connection is still open after Stop (read timed out)")
	}
	if conn, err := net.DialTimeout("tcp", pending, 3*time.Second); err == nil {
		_ = conn.Close()
		t.Error("the passive port still takes connections after Stop")
	}
}

// The address lockout ends with the run whose password it guarded, and the
// watchdog's stop of an old run leaves a newer one alone.
func TestFTPRestartForgetsLockoutsAndOldRuns(t *testing.T) {
	dir := t.TempDir()
	srv := &ftpService{conns: make(map[net.Conn]struct{})}
	start := func() {
		t.Helper()
		if err := srv.startShares(dir, []ftpShare{{name: "Roms", dir: dir}}, "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
	}
	start()
	t.Cleanup(srv.Stop)
	for i := 0; i < ftpLoginMaxFails; i++ {
		srv.limiter.fail("10.0.0.9")
	}
	if locked, _ := srv.limiter.locked("10.0.0.9"); !locked {
		t.Fatal("not locked out after the wrong passwords")
	}
	old := srv.watchTarget().gen
	srv.Stop()
	start()
	if locked, _ := srv.limiter.locked("10.0.0.9"); locked {
		t.Error("a lockout outlived the restart and its new password")
	}
	if srv.stopRun(old) || !srv.Running() {
		t.Fatal("stopping the old run stopped the new one")
	}
	if !srv.stopRun(srv.watchTarget().gen) || srv.Running() {
		t.Fatal("stopping the current run did not stop it")
	}
}
