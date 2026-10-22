package main

// Tests for the SSH/SFTP service. The end-to-end test drives the real
// x/crypto/ssh client and pkg/sftp client against a loopback listener, so the
// auth policy, host key, subsystem handling and file operations are exercised
// the way a laptop would use them.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func testSSHSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer
}

type testSSHMeta struct {
	user string
}

func (m testSSHMeta) User() string          { return m.user }
func (m testSSHMeta) SessionID() []byte     { return nil }
func (m testSSHMeta) ClientVersion() []byte { return []byte("test") }
func (m testSSHMeta) ServerVersion() []byte { return []byte("test") }
func (m testSSHMeta) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4242}
}
func (m testSSHMeta) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}
}

func TestSSHPasswordFormat(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 30; i++ {
		password, err := generateSSHPassword()
		if err != nil {
			t.Fatalf("generateSSHPassword: %v", err)
		}
		if len(password) != 19 {
			t.Fatalf("password %q length = %d, want 16 characters in 4 groups", password, len(password))
		}
		groups := strings.Split(password, "-")
		if len(groups) != 4 {
			t.Fatalf("password %q has %d groups, want 4", password, len(groups))
		}
		for _, group := range groups {
			if len(group) != 4 {
				t.Fatalf("password %q group %q length = %d, want 4", password, group, len(group))
			}
		}
		plain := strings.ReplaceAll(password, "-", "")
		for _, r := range plain {
			if !strings.ContainsRune("abcdefghjkmnpqrstuvwxyz23456789", r) {
				t.Fatalf("password %q contains ambiguous/unsupported character %q", password, r)
			}
		}
		if seen[password] {
			t.Fatalf("generateSSHPassword returned %q twice", password)
		}
		seen[password] = true
	}
}

func TestSSHAuthCallbacks(t *testing.T) {
	signer := testSSHSigner(t)
	authorized := testSSHSigner(t)
	other := testSSHSigner(t)
	const password = "abcd-efgh-jkmn-pqrs"

	config := buildSSHServerConfig(signer, password, []ssh.PublicKey{authorized.PublicKey()})

	if _, err := config.PasswordCallback(testSSHMeta{user: sshUserName}, []byte(password)); err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
	if _, err := config.PasswordCallback(testSSHMeta{user: sshUserName}, []byte("wrong")); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err := config.PasswordCallback(testSSHMeta{user: "root"}, []byte(password)); err == nil {
		t.Fatal("password accepted for a different user")
	}
	if config.PublicKeyCallback == nil {
		t.Fatal("no public key callback with an authorized key")
	}
	if _, err := config.PublicKeyCallback(testSSHMeta{user: sshUserName}, authorized.PublicKey()); err != nil {
		t.Fatalf("authorized key rejected: %v", err)
	}
	if _, err := config.PublicKeyCallback(testSSHMeta{user: sshUserName}, other.PublicKey()); err == nil {
		t.Fatal("unauthorized key accepted")
	}
	if _, err := config.PublicKeyCallback(testSSHMeta{user: "root"}, authorized.PublicKey()); err == nil {
		t.Fatal("authorized key accepted for a different user")
	}

	passwordOnly := buildSSHServerConfig(signer, password, nil)
	if passwordOnly.PublicKeyCallback != nil {
		t.Fatal("public key callback set without an authorized keys file")
	}
}

// A server with no password must not accept an empty one: sha256("") matched
// sha256(""), so every password attempt used to succeed.
func TestSSHRejectsEmptyPassword(t *testing.T) {
	signer := testSSHSigner(t)
	config := buildSSHServerConfig(signer, "", nil)
	if _, err := config.PasswordCallback(testSSHMeta{user: sshUserName}, nil); err == nil {
		t.Fatal("empty password accepted when none is configured")
	}
	if _, err := config.PasswordCallback(testSSHMeta{user: sshUserName}, []byte("")); err == nil {
		t.Fatal("blank password accepted when none is configured")
	}
}

// The digest is salted per process, so a value captured from one run cannot be
// replayed against the next.
func TestSSHPasswordDigestDependsOnTheSalt(t *testing.T) {
	const password = "abcd-efgh-jkmn-pqrs"
	salted := sshPasswordDigest(password)
	plain := sha256.Sum256([]byte(password))
	if bytes.Equal(salted, plain[:]) {
		t.Fatal("password digest is an unsalted SHA-256")
	}
	prev := sshPasswordSalt
	t.Cleanup(func() { sshPasswordSalt = prev })
	sshPasswordSalt = [32]byte{1}
	if bytes.Equal(sshPasswordDigest(password), salted) {
		t.Fatal("digest does not change with the salt")
	}
}

func TestSSHAuthorizedKeysFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, sshKeysFileName)
	key := testSSHSigner(t)

	// Comments and blank lines must be skipped, and one broken line must not
	// hide the valid keys around it.
	content := "# laptop\n\nnot-a-key\n" + string(ssh.MarshalAuthorizedKey(key.PublicKey()))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write keys: %v", err)
	}
	keys, err := loadSSHAuthorizedKeys(path)
	if err != nil {
		t.Fatalf("load keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(keys))
	}
	if string(keys[0].Marshal()) != string(key.PublicKey().Marshal()) {
		t.Fatal("loaded key does not match the authorized key")
	}

	missing, err := loadSSHAuthorizedKeys(filepath.Join(dir, "missing"))
	if err != nil || missing != nil {
		t.Fatalf("missing file = %v, %v; want nil, nil", missing, err)
	}
}

func startTestSSHServer(t *testing.T, root string) {
	t.Helper()
	t.Setenv("JUKAHUB_SSH_KEY_DIR", t.TempDir())
	t.Setenv("JUKAHUB_SSH_READONLY", "")
	if err := sshSrv.startAt(root, "127.0.0.1:0"); err != nil {
		t.Fatalf("start SSH server: %v", err)
	}
	t.Cleanup(sshSrv.Stop)
}

func dialTestSSH(t *testing.T, password string, hostKey *ssh.PublicKey) (*ssh.Client, error) {
	t.Helper()
	config := &ssh.ClientConfig{
		User: sshUserName,
		Auth: []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			if hostKey != nil {
				*hostKey = key
			}
			return nil
		},
		Timeout: 5 * time.Second,
	}
	return ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sshSrv.Port()), config)
}

// The terminal is the console fallback when the Tools panel is not open.
func TestTerminalSSHStatusAndStop(t *testing.T) {
	t.Setenv("JUKAHUB_SSH_KEY_DIR", t.TempDir())
	if err := sshSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start SSH: %v", err)
	}
	defer sshSrv.Stop()

	prevLines, prevInput, prevCursor, prevHistory := term.lines, term.input, term.cursor, term.history
	term.lines, term.input, term.cursor, term.history = nil, "", 0, nil
	t.Cleanup(func() {
		term.lines, term.input, term.cursor, term.history = prevLines, prevInput, prevCursor, prevHistory
	})

	termSSHCommand([]string{"status"})
	var out strings.Builder
	for _, line := range term.lines {
		out.WriteString(line.text)
		out.WriteByte('\n')
	}
	text := out.String()
	for _, want := range []string{"running", sshSrv.Password(), sshSrv.Fingerprint(), "sftp://"} {
		if !strings.Contains(text, want) {
			t.Fatalf("ssh status output is missing %q:\n%s", want, text)
		}
	}

	termSSHCommand([]string{"off"})
	if sshSrv.Running() {
		t.Fatal("ssh off left the server running")
	}
	if !strings.Contains(term.lines[len(term.lines)-1].text, "stopped") {
		t.Fatalf("ssh off output = %q", term.lines[len(term.lines)-1].text)
	}
}

func TestToggleSSHServerStops(t *testing.T) {
	t.Setenv("JUKAHUB_SSH_KEY_DIR", t.TempDir())
	if err := sshSrv.startAt(t.TempDir(), "127.0.0.1:0"); err != nil {
		t.Fatalf("start SSH: %v", err)
	}
	t.Cleanup(sshSrv.Stop)
	toggleSSHServer()
	if sshSrv.Running() {
		t.Fatal("toggle left a running server running")
	}
}

func TestSSHServerEndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "hello.txt"), []byte("hello from device"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	startTestSSHServer(t, root)

	// A wrong password must not get a connection.
	if wrong, err := dialTestSSH(t, "definitely-wrong", nil); err == nil {
		wrong.Close()
		t.Fatal("server accepted a wrong password")
	}

	var hostKey ssh.PublicKey
	conn, err := dialTestSSH(t, sshSrv.Password(), &hostKey)
	if err != nil {
		t.Fatalf("dial with the generated password: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	if hostKey == nil {
		t.Fatal("client did not receive a host key")
	}
	if got, want := ssh.FingerprintSHA256(hostKey), sshSrv.Fingerprint(); got != want {
		t.Fatalf("client saw host key %s, server advertises %s", got, want)
	}

	client, err := sftp.NewClient(conn)
	if err != nil {
		t.Fatalf("start sftp subsystem: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	// The root is /: host paths are never exposed.
	if cwd, err := client.Getwd(); err != nil || cwd != "/" {
		t.Fatalf("Getwd = %q, %v; want /", cwd, err)
	}
	if real, err := client.RealPath("/sub/../sub"); err != nil || real != "/sub" {
		t.Fatalf("RealPath = %q, %v; want /sub", real, err)
	}
	if info, err := client.Stat("/sub/hello.txt"); err != nil || info.Size() != int64(len("hello from device")) {
		t.Fatalf("Stat = %+v, %v", info, err)
	}
	file, err := client.Open("/sub/hello.txt")
	if err != nil {
		t.Fatalf("open seeded file: %v", err)
	}
	data, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(data) != "hello from device" {
		t.Fatalf("read seeded file = %q, %v", data, err)
	}

	if err := client.Mkdir("/uploads"); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	written, err := client.Create("/uploads/note.txt")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if _, err := written.Write([]byte("from the laptop")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := written.Close(); err != nil {
		t.Fatalf("close written file: %v", err)
	}
	if onDisk, err := os.ReadFile(filepath.Join(root, "uploads", "note.txt")); err != nil || string(onDisk) != "from the laptop" {
		t.Fatalf("file on disk = %q, %v", onDisk, err)
	}
	if err := client.Rename("/uploads/note.txt", "/uploads/renamed.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	entries, err := client.ReadDir("/uploads")
	if err != nil || len(entries) != 1 || entries[0].Name() != "renamed.txt" {
		t.Fatalf("ReadDir = %+v, %v", entries, err)
	}
	if err := client.Remove("/uploads/renamed.txt"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := client.RemoveDirectory("/uploads"); err != nil {
		t.Fatalf("remove directory: %v", err)
	}

	// The service is SFTP only: a shell/exec request must fail.
	session, err := conn.NewSession()
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer session.Close()
	if err := session.Run("id"); err == nil {
		t.Fatal("server executed a shell command")
	}

	// A symlink inside the root that points outside must not be readable.
	if err := os.Symlink(filepath.Join(os.TempDir(), "jukahub-ssh-outside"), filepath.Join(root, "escape")); err == nil {
		if leaked, err := client.Open("/escape"); err == nil {
			leaked.Close()
			t.Fatal("SFTP followed a symlink outside the root")
		}
	}

	// A second launch with the same key folder must present the same host key,
	// otherwise the laptop warning would be useless.
	first := sshSrv.Fingerprint()
	conn.Close()
	client.Close()
	sshSrv.Stop()
	if err := sshSrv.startAt(root, "127.0.0.1:0"); err != nil {
		t.Fatalf("restart SSH server: %v", err)
	}
	if second := sshSrv.Fingerprint(); second != first {
		t.Fatalf("host key changed across restarts: %q then %q", first, second)
	}
}
