package main

// Tests for the Tools FTP server: path confinement and a real upload/download
// session over loopback (login, passive mode, STOR, RETR, LIST, SIZE, DELE).

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestFTPResolveClampsToRoot(t *testing.T) {
	root := t.TempDir()
	sess := &ftpSession{root: root, cwd: "/"}

	cases := map[string]string{
		"/":                 root,
		"":                  root,
		"/etc/passwd":       filepath.Join(root, "etc", "passwd"),
		"../secret.txt":     filepath.Join(root, "secret.txt"),
		"sub/../file.txt":   filepath.Join(root, "file.txt"),
		"/../../etc/shadow": filepath.Join(root, "etc", "shadow"),
		"sub/folder":        filepath.Join(root, "sub", "folder"),
	}
	for in, want := range cases {
		got, err := sess.resolve(in)
		if err != nil {
			t.Errorf("resolve(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("resolve(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := sess.resolve("bad\x00name"); err == nil {
		t.Error("a path containing NUL must be rejected")
	}
}

func TestFTPRootDirOverride(t *testing.T) {
	want := t.TempDir()
	t.Setenv("JUKAHUB_FTP_ROOT", want)
	if got := ftpRootDir(); got != want {
		t.Fatalf("ftpRootDir() = %q, want the override %q", got, want)
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

func TestFTPResolveRejectsEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs extra privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	sess := &ftpSession{root: root, cwd: "/"}
	if _, err := sess.resolve("link/secret.txt"); err == nil {
		t.Error("a symlink pointing outside the shared folder must be rejected")
	}
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

// passive sends PASV, connects the data socket and returns it.
func (c *ftpTestClient) passive() net.Conn {
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
	port := nums[4]*256 + nums[5]
	conn, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		c.t.Fatalf("connect data socket: %v", err)
	}
	return conn
}

func TestFTPServerUploadDownload(t *testing.T) {
	root := t.TempDir()
	srv := &ftpService{conns: make(map[net.Conn]struct{})}
	if err := srv.startAt(root, "127.0.0.1:0"); err != nil {
		t.Fatalf("start FTP: %v", err)
	}
	t.Cleanup(srv.Stop)
	if !srv.Running() {
		t.Fatal("server should report running after start")
	}

	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()

	if resp := c.cmd("USER test"); !strings.HasPrefix(resp, "331") {
		t.Fatalf("USER = %q", resp)
	}
	if resp := c.cmd("PASS test"); !strings.HasPrefix(resp, "230") {
		t.Fatalf("PASS = %q", resp)
	}
	if resp := c.cmd("PWD"); !strings.HasPrefix(resp, `257 "/"`) {
		t.Fatalf("PWD = %q", resp)
	}
	if resp := c.cmd("PORT 127,0,0,1,10,10"); !strings.HasPrefix(resp, "502") {
		t.Fatalf("PORT must be refused in passive-only mode, got %q", resp)
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

// FileZilla and friends send LIST with flags when hidden files are enabled;
// those flags are not a path and must not break the listing.
func TestFTPListAcceptsFlags(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &ftpService{conns: make(map[net.Conn]struct{})}
	if err := srv.startAt(root, "127.0.0.1:0"); err != nil {
		t.Fatalf("start FTP: %v", err)
	}
	t.Cleanup(srv.Stop)

	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()
	c.cmd("USER test")
	c.cmd("PASS test")

	data := c.passive()
	c.send("LIST -la")
	if resp := c.read(); !strings.HasPrefix(resp, "150") {
		t.Fatalf("LIST -la = %q, want a listing", resp)
	}
	listing, err := io.ReadAll(data)
	_ = data.Close()
	if err != nil {
		t.Fatalf("read listing: %v", err)
	}
	if resp := c.read(); !strings.HasPrefix(resp, "226") {
		t.Fatalf("LIST -la completion = %q", resp)
	}
	if !strings.Contains(string(listing), "visible.txt") {
		t.Fatalf("LIST -la did not list the share:\n%s", listing)
	}
}

func TestEnsureToolsMenuEntry(t *testing.T) {
	cfg := &Config{
		Height: 720,
		Scenes: []SceneConfig{{
			Name: "Misc",
			Elements: []Element{
				{Type: "button", Text: "Terminal", X: 24, Y: 72, Height: "55", Width: "296"},
			},
		}},
	}
	ensureToolsMenuEntry(cfg)
	if len(cfg.Scenes[0].Elements) != 2 {
		t.Fatalf("Misc elements = %d, want the Terminal button plus FTP & IP", len(cfg.Scenes[0].Elements))
	}
	added := cfg.Scenes[0].Elements[1]
	if added.Trigger != "tools_open" || added.Text != "FTP & IP" {
		t.Errorf("added element = %+v", added)
	}
	if added.Y < 72+55 {
		t.Errorf("added button overlaps the existing grid (y=%d)", added.Y)
	}
	// Idempotent: a reload must not keep adding buttons.
	ensureToolsMenuEntry(cfg)
	if len(cfg.Scenes[0].Elements) != 2 {
		t.Errorf("ensureToolsMenuEntry is not idempotent: %d elements", len(cfg.Scenes[0].Elements))
	}
	// No Misc scene: nothing to do and no panic.
	ensureToolsMenuEntry(&Config{Height: 720, Scenes: []SceneConfig{{Name: "Main"}}})
}
