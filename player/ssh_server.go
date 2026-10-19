package main

// ssh_server.go — an SFTP-only SSH server so a laptop can browse the console's
// file system with any SFTP client (WinSCP, FileZilla, `sftp`, Cyberduck…).
//
// Design notes:
//   - Only the "sftp" subsystem is served. Shell, exec, pty, agent forwarding,
//     port forwarding and X11 are refused, so the service cannot be turned into
//     a remote shell or a tunnel into the LAN.
//   - Authentication is a per-start random password (shown in Tools) plus any
//     OpenSSH public keys in <data>/ssh_authorized_keys. Password auth is rate
//     limited by MaxAuthTries and the connection cap.
//   - The host key is an Ed25519 key persisted in the app data folder, so the
//     laptop sees the same fingerprint on every launch and can detect a
//     changed host. It is generated on first start.
//   - Paths are confined by sftp_root.go: ".." cannot escape and symlinks that
//     point outside the root are refused.
//   - The default root is the whole file system on Linux (the point of the
//     feature) and the user's home directory on Windows. JUKAHUB_SSH_ROOT
//     narrows it; JUKAHUB_SSH_READONLY=1 makes every session read-only.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	sshDefaultPort     = 2222
	sshMaxConns        = 4
	sshHandshakeLimit  = 15 * time.Second
	sshUserName        = "juka"
	sshHostKeyFileName = "ssh_host_ed25519_key"
	sshKeysFileName    = "ssh_authorized_keys"
)

// sshService is the running SSH server. Use the package-level sshSrv.
type sshService struct {
	mu          sync.Mutex
	ln          net.Listener
	root        string
	port        int
	password    string
	fingerprint string
	readOnly    bool
	lastErr     string
	conns       map[net.Conn]struct{}
}

var sshSrv = &sshService{conns: make(map[net.Conn]struct{})}

// Running reports whether the listener is open.
func (s *sshService) Running() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ln != nil
}

func (s *sshService) Root() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root != "" {
		return s.root
	}
	return sshRootDir()
}

func (s *sshService) Port() int {
	if s == nil {
		return sshDefaultPort
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port > 0 {
		return s.port
	}
	return sshPort()
}

// User is the fixed account name. There is one account, not a system user.
func (s *sshService) User() string { return sshUserName }

// Password is the random password generated for the current run ("" when the
// server has never started). It is intentionally not published as a config
// variable or written to the log.
func (s *sshService) Password() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.password
}

// Fingerprint is the SHA256 fingerprint of the host key, for the user to
// compare with the warning their SFTP client shows on first connect.
func (s *sshService) Fingerprint() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fingerprint
}

// ReadOnly reports whether the current session policy refuses writes.
func (s *sshService) ReadOnly() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readOnly
}

func (s *sshService) LastError() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Address is what a user types into an SFTP client. It follows WiFi changes.
func (s *sshService) Address() string {
	if !s.Running() {
		return ""
	}
	host := localIPv4()
	if host == "" {
		return ""
	}
	return fmt.Sprintf("sftp://%s@%s:%d", sshUserName, host, s.Port())
}

// Start serves the configured root on all interfaces.
func (s *sshService) Start() error {
	if s.Running() {
		return nil
	}
	return s.startAt(sshRootDir(), fmt.Sprintf("0.0.0.0:%d", sshPort()))
}

// startAt is Start with an explicit root and listen address (used by tests).
func (s *sshService) startAt(root, addr string) error {
	if s.Running() {
		return nil
	}
	if strings.TrimSpace(root) == "" {
		return errors.New("SSH shared root is unavailable")
	}
	confined, err := newSFTPRoot(root, sshReadOnly())
	if err != nil {
		return fmt.Errorf("SSH root %s is not available: %w", root, err)
	}

	keyDir := sshKeyDir()
	signer, fingerprint, err := loadOrCreateSSHHostKey(keyDir)
	if err != nil {
		return fmt.Errorf("SSH host key: %w", err)
	}
	password, err := generateSSHPassword()
	if err != nil {
		return fmt.Errorf("generate SSH password: %w", err)
	}
	authorized, err := loadSSHAuthorizedKeys(sshAuthorizedKeysPath(keyDir))
	if err != nil {
		return fmt.Errorf("SSH authorized keys: %w", err)
	}
	config := buildSSHServerConfig(signer, password, authorized)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		if s == sshSrv {
			publishSSHState()
		}
		return fmt.Errorf("start SSH server on %s: %w", addr, err)
	}

	s.mu.Lock()
	if s.conns == nil {
		s.conns = make(map[net.Conn]struct{})
	}
	s.ln = ln
	s.root = confined.root
	s.password = password
	s.fingerprint = fingerprint
	s.readOnly = confined.readOnly
	s.lastErr = ""
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tcp.Port
	}
	s.mu.Unlock()

	if s == sshSrv {
		publishSSHState()
	}
	log.Printf("[SSH] SFTP serving %q on %s (user %s, host key %s, keys %d)",
		confined.root, ln.Addr(), sshUserName, fingerprint, len(authorized))
	go s.acceptLoop(ln, config, confined)
	return nil
}

// Stop closes the listener and every open SSH connection.
func (s *sshService) Stop() {
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
		log.Printf("[SSH] stopped")
	}
	if s == sshSrv {
		publishSSHState()
	}
}

func (s *sshService) acceptLoop(ln net.Listener, config *ssh.ServerConfig, root *sftpRoot) {
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
		if len(s.conns) >= sshMaxConns {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		s.mu.Unlock()

		go func(conn net.Conn) {
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				_ = conn.Close()
			}()
			s.serveConn(conn, config, root)
		}(conn)
	}
}

func (s *sshService) serveConn(conn net.Conn, config *ssh.ServerConfig, root *sftpRoot) {
	_ = conn.SetDeadline(time.Now().Add(sshHandshakeLimit))
	serverConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	defer serverConn.Close()
	log.Printf("[SSH] %s connected from %s", serverConn.User(), conn.RemoteAddr())

	// Answer keepalives so clients that rely on them stay connected, and refuse
	// every other global request (tcpip-forward, agent forwarding).
	go serveSSHGlobalRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only the sftp subsystem is enabled")
			continue
		}
		channel, channelReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.serveSession(channel, channelReqs, root)
	}
}

func serveSSHGlobalRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		ok := req.Type == "keepalive@openssh.com"
		_ = req.Reply(ok, nil)
	}
}

// serveSession accepts exactly one "subsystem: sftp" request per channel and
// rejects shell/exec/pty/env requests, then hands the channel to pkg/sftp.
func (s *sshService) serveSession(channel ssh.Channel, reqs <-chan *ssh.Request, root *sftpRoot) {
	defer channel.Close()
	for req := range reqs {
		if req.Type != "subsystem" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Name string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil || payload.Name != "sftp" {
			_ = req.Reply(false, nil)
			continue
		}
		_ = req.Reply(true, nil)
		handlers := &sftpRootHandlers{root: root}
		server := sftp.NewRequestServer(channel, sftp.Handlers{
			FileGet:  handlers,
			FilePut:  handlers,
			FileCmd:  handlers,
			FileList: handlers,
		})
		if err := server.Serve(); err != nil && !errors.Is(err, io.EOF) {
			log.Printf("[SSH] sftp session ended: %v", err)
		}
		_ = server.Close()
		return
	}
}

// ──────────────────────────────────────────────────────────────────────
// Authentication and host key
// ──────────────────────────────────────────────────────────────────────

func buildSSHServerConfig(hostKey ssh.Signer, password string, authorized []ssh.PublicKey) *ssh.ServerConfig {
	authorizedSet := make(map[string]struct{}, len(authorized))
	for _, key := range authorized {
		if key != nil {
			authorizedSet[string(key.Marshal())] = struct{}{}
		}
	}
	passwordHash := sha256.Sum256([]byte(password))

	config := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-JukaHub",
		MaxAuthTries:  3,
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if meta.User() != sshUserName {
				return nil, fmt.Errorf("unknown user")
			}
			sum := sha256.Sum256(pass)
			if subtle.ConstantTimeCompare(sum[:], passwordHash[:]) != 1 {
				log.Printf("[SSH] password rejected for %s from %s", meta.User(), meta.RemoteAddr())
				return nil, fmt.Errorf("password rejected")
			}
			return nil, nil
		},
	}
	if len(authorizedSet) > 0 {
		config.PublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != sshUserName {
				return nil, fmt.Errorf("unknown user")
			}
			if _, ok := authorizedSet[string(key.Marshal())]; !ok {
				return nil, fmt.Errorf("key not authorized")
			}
			return nil, nil
		}
	}
	config.AddHostKey(hostKey)
	return config
}

// generateSSHPassword returns a 16-character password grouped for reading off
// a small screen, e.g. "k7fm-2qxa-9rte-w3hd". The alphabet avoids characters
// that are easy to confuse (0/O, 1/l/I).
func generateSSHPassword() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, v := range raw {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return b.String(), nil
}

// loadOrCreateSSHHostKey returns the persistent Ed25519 host key, generating
// and saving one when the file is absent or unreadable. A failure to save the
// new key is not fatal: the service keeps the in-memory key for this run.
func loadOrCreateSSHHostKey(dir string) (ssh.Signer, string, error) {
	path := filepath.Join(dir, sshHostKeyFileName)
	if data, err := os.ReadFile(path); err == nil {
		if signer, err := ssh.ParsePrivateKey(data); err == nil {
			return signer, ssh.FingerprintSHA256(signer.PublicKey()), nil
		}
		log.Printf("[SSH] host key %s is unreadable; generating a new one", path)
	}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, "", err
	}
	block, err := ssh.MarshalPrivateKey(private, "JukaHub host key")
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[SSH] cannot create key folder %s: %v", dir, err)
	} else if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		log.Printf("[SSH] cannot save host key %s: %v", path, err)
	} else {
		_ = os.Chmod(path, 0o600)
	}
	return signer, ssh.FingerprintSHA256(signer.PublicKey()), nil
}

// sshKeyDir is the writable folder that holds the host key and the optional
// authorized_keys file. The app data folder survives updates.
func sshKeyDir() string {
	if custom := strings.TrimSpace(os.Getenv("JUKAHUB_SSH_KEY_DIR")); custom != "" {
		return custom
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return dir
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func sshAuthorizedKeysPath(keyDir string) string {
	if custom := strings.TrimSpace(os.Getenv("JUKAHUB_SSH_AUTHORIZED_KEYS")); custom != "" {
		return custom
	}
	return filepath.Join(keyDir, sshKeysFileName)
}

// loadSSHAuthorizedKeys parses an OpenSSH authorized_keys file. A missing file
// simply means "password only".
func loadSSHAuthorizedKeys(path string) ([]ssh.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var keys []ssh.PublicKey
	for len(data) > 0 {
		key, _, _, rest, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			// Skip malformed lines but keep going, matching sshd's tolerant
			// behaviour for comments and blank lines.
			if idx := strings.IndexByte(string(data), '\n'); idx >= 0 {
				data = data[idx+1:]
				continue
			}
			break
		}
		keys = append(keys, key)
		data = rest
	}
	return keys, nil
}

// ──────────────────────────────────────────────────────────────────────
// Configuration
// ──────────────────────────────────────────────────────────────────────

// sshPort returns the configured port. A numeric "ssh_port" custom variable
// wins over JUKAHUB_SSH_PORT, which wins over 2222.
func sshPort() int {
	if appConfig != nil {
		if raw, ok := customString(appConfig.Variables.Custom, "ssh_port"); ok {
			if p, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && p > 0 && p < 65536 {
				return p
			}
		}
	}
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_SSH_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return sshDefaultPort
}

// sshRootDir picks the folder exposed to SFTP clients. The whole Linux file
// system is the point of the feature; on a desktop the user's home directory
// keeps the workspace and personal files out of reach by default.
func sshRootDir() string {
	// Return the override even when it does not exist: starting then fails
	// loudly instead of silently widening the share back to the whole system.
	if custom := strings.TrimSpace(os.Getenv("JUKAHUB_SSH_ROOT")); custom != "" {
		return custom
	}
	if runtime.GOOS == "linux" {
		return "/"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if info, err := os.Stat(home); err == nil && info.IsDir() {
			return home
		}
	}
	return ftpRootDir()
}

func sshReadOnly() bool {
	if appConfig != nil {
		if raw, ok := customString(appConfig.Variables.Custom, "ssh_readonly"); ok {
			if enabled, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
				return enabled
			}
		}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_SSH_READONLY"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ──────────────────────────────────────────────────────────────────────
// UI helpers
// ──────────────────────────────────────────────────────────────────────

// publishSSHState mirrors the server state into custom variables so config
// scenes can display it. The password and host key are deliberately excluded:
// custom variables are saved into the user config and shown in scene text.
func publishSSHState() {
	status := "stopped"
	addr := ""
	if sshSrv.Running() {
		status = "running"
		addr = sshSrv.Address()
	}
	publishCustom("ssh_status", status)
	publishCustom("ssh_address", addr)
	publishCustom("ssh_user", sshUserName)
	publishCustom("ssh_fingerprint", sshSrv.Fingerprint())
	publishCustom("ssh_root", sshSrv.Root())
	publishCustom("ssh_port", strconv.Itoa(sshSrv.Port()))
	publishCustom("local_ip", localIPv4())
}

// toggleSSHServer starts or stops the server and reports the result. It is used
// by the Tools panel, the "ssh_toggle" trigger and the terminal.
func toggleSSHServer() {
	if sshSrv.Running() {
		sshSrv.Stop()
		ShowToast("SSH/SFTP server stopped", ToastKindInfo)
		return
	}
	// A LAN address is the only requirement; SFTP works on a WiFi network with
	// no internet route, so this deliberately does not probe connectivity.
	if toolsIPAddress() == "" {
		ShowToast("Connect WiFi first — SFTP needs a network address", ToastKindWarning)
		return
	}
	if err := sshSrv.Start(); err != nil {
		ShowToast("SSH failed: "+firstLine(err.Error()), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast("SFTP on "+sshSrv.Address(), ToastKindSuccess)
}
