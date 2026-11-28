package main

// ftp_tls_test.go — explicit TLS for the FTP server (NET-15): AUTH TLS upgrades
// the control connection before sign-in, PBSZ/PROT P put the data channels
// behind TLS, and one persisted certificate is what both the client and the
// Network tools sheet see.

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startTestFTPWithCert serves a temp share and keeps the FTPS certificate in a
// temp data folder, so a test never touches the device's own.
func startTestFTPWithCert(t *testing.T) (*ftpService, string) {
	t.Helper()
	prev := bkupDataDirOverride
	bkupDataDirOverride = t.TempDir()
	t.Cleanup(func() { bkupDataDirOverride = prev })
	return startTestFTP(t)
}

// feat reads a FEAT reply to its end.
func (c *ftpTestClient) feat() []string {
	c.t.Helper()
	c.send("FEAT")
	if resp := c.read(); !strings.HasPrefix(resp, "211-") {
		c.t.Fatalf("FEAT = %q", resp)
	}
	var lines []string
	for {
		line := c.read()
		if strings.HasPrefix(line, "211 ") {
			return lines
		}
		lines = append(lines, strings.TrimSpace(line))
	}
}

// tlsUpgrade runs AUTH TLS and wraps the test client's control connection.
func (c *ftpTestClient) tlsUpgrade() *tls.Conn {
	c.t.Helper()
	if resp := c.cmd("AUTH TLS"); !strings.HasPrefix(resp, "234") {
		c.t.Fatalf("AUTH TLS = %q, want 234", resp)
	}
	tc := tls.Client(c.conn, &tls.Config{InsecureSkipVerify: true, ServerName: "127.0.0.1"})
	if err := tc.Handshake(); err != nil {
		c.t.Fatalf("TLS handshake: %v", err)
	}
	c.conn = tc
	c.r = bufio.NewReader(tc)
	return tc
}

// tlsData opens the passive data socket and hands it to TLS. The command is
// sent first on the control connection: the server accepts (and handshakes)
// only when it processes the transfer, and it replies 150 after that.
func (c *ftpTestClient) tlsData(command string) *tls.Conn {
	c.t.Helper()
	addr := c.pasvAddr()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		c.t.Fatalf("connect data socket: %v", err)
	}
	c.send("%s", command)
	dc := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, ServerName: "127.0.0.1"})
	if err := dc.Handshake(); err != nil {
		c.t.Fatalf("%s: TLS data handshake: %v", command, err)
	}
	return dc
}

func TestFTPAuthTLSProtectsControlAndData(t *testing.T) {
	srv, root := startTestFTPWithCert(t)
	want := srv.TLSCertFingerprint()
	if want == "" {
		t.Fatal("no FTPS certificate fingerprint after start")
	}

	c := newFTPTestClient(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port())))
	defer c.close()

	advertised := false
	for _, line := range c.feat() {
		if line == "AUTH TLS" {
			advertised = true
		}
	}
	if !advertised {
		t.Fatalf("FEAT does not advertise AUTH TLS")
	}

	tc := c.tlsUpgrade()
	state := tc.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("the client saw no server certificate")
	}
	if got := ftpCertFingerprint(state.PeerCertificates[0].Raw); got != want {
		t.Fatalf("client fingerprint = %s, sheet fingerprint = %s", got, want)
	}

	// Sign-in happens inside TLS.
	c.login(srv)
	if resp := c.cmd("CWD Roms"); !strings.HasPrefix(resp, "250") {
		t.Fatalf("CWD Roms = %q, want 250", resp)
	}
	if resp := c.cmd("PBSZ 0"); !strings.HasPrefix(resp, "200") {
		t.Fatalf("PBSZ = %q, want 200", resp)
	}
	if resp := c.cmd("PROT P"); !strings.HasPrefix(resp, "200") {
		t.Fatalf("PROT P = %q, want 200", resp)
	}

	payload := "hello over FTPS"
	dc := c.tlsData("STOR tls.txt")
	if resp := c.read(); !strings.HasPrefix(resp, "150") {
		t.Fatalf("STOR = %q, want 150", resp)
	}
	if _, err := io.WriteString(dc, payload); err != nil {
		t.Fatalf("write TLS data: %v", err)
	}
	if err := dc.Close(); err != nil {
		t.Fatalf("close TLS data: %v", err)
	}
	if resp := c.read(); !strings.HasPrefix(resp, "226") {
		t.Fatalf("STOR completion = %q, want 226", resp)
	}
	if got, err := os.ReadFile(filepath.Join(root, "tls.txt")); err != nil || string(got) != payload {
		t.Fatalf("stored file = %q (err %v), want %q", got, err, payload)
	}

	dc = c.tlsData("RETR tls.txt")
	if resp := c.read(); !strings.HasPrefix(resp, "150") {
		t.Fatalf("RETR = %q, want 150", resp)
	}
	body, err := io.ReadAll(dc)
	_ = dc.Close()
	if err != nil {
		t.Fatalf("read TLS data: %v", err)
	}
	if resp := c.read(); !strings.HasPrefix(resp, "226") {
		t.Fatalf("RETR completion = %q, want 226", resp)
	}
	if string(body) != payload {
		t.Fatalf("download = %q, want %q", body, payload)
	}
}

func TestFTPProtPNeedsAuthTLS(t *testing.T) {
	srv, _ := startTestFTPWithCert(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.Port()))

	c := newFTPTestClient(t, addr)
	defer c.close()
	if resp := c.cmd("PBSZ 0"); !strings.HasPrefix(resp, "503") {
		t.Fatalf("PBSZ before AUTH TLS = %q, want 503", resp)
	}
	if resp := c.cmd("PROT P"); !strings.HasPrefix(resp, "503") {
		t.Fatalf("PROT P before AUTH TLS = %q, want 503", resp)
	}

	c.login(srv)
	if resp := c.cmd("AUTH TLS"); !strings.HasPrefix(resp, "503") {
		t.Fatalf("AUTH TLS after sign-in = %q, want 503", resp)
	}
	// The refused upgrade leaves the signed-in session usable.
	if resp := c.cmd("PWD"); !strings.HasPrefix(resp, `257 "/"`) {
		t.Fatalf("PWD after a refused AUTH TLS = %q", resp)
	}

	// A wrong AUTH argument is answered without taking the connection down.
	c2 := newFTPTestClient(t, addr)
	defer c2.close()
	if resp := c2.cmd("AUTH SSL"); !strings.HasPrefix(resp, "504") {
		t.Fatalf("AUTH SSL = %q, want 504", resp)
	}
	c2.login(srv)
}

func TestFTPTLSCertificateIsPersisted(t *testing.T) {
	prev := bkupDataDirOverride
	bkupDataDirOverride = t.TempDir()
	t.Cleanup(func() { bkupDataDirOverride = prev })

	path := ftpCertPath()
	cert, fp, err := ftpLoadOrCreateCert(path)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	if fp == "" || len(cert.Certificate) == 0 || cert.Leaf == nil {
		t.Fatalf("created certificate is incomplete: fp=%q certs=%d", fp, len(cert.Certificate))
	}
	if !cert.Leaf.NotAfter.After(time.Now().AddDate(9, 0, 0)) {
		t.Fatalf("certificate expires %s, want the ten-year validity", cert.Leaf.NotAfter)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("certificate file: %v", err)
	}

	// The second start loads the same file: the fingerprint a client pinned
	// keeps matching across restarts.
	_, again, err := ftpLoadOrCreateCert(path)
	if err != nil {
		t.Fatalf("reload certificate: %v", err)
	}
	if again != fp {
		t.Fatalf("reloaded fingerprint = %s, want %s", again, fp)
	}

	// A damaged file is replaced, so FTPS comes back instead of staying down.
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("damage certificate: %v", err)
	}
	_, replaced, err := ftpLoadOrCreateCert(path)
	if err != nil {
		t.Fatalf("replace damaged certificate: %v", err)
	}
	if replaced == "" || replaced == fp {
		t.Fatalf("damaged certificate was not replaced: %q", replaced)
	}
}
