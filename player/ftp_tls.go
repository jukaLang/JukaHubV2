package main

// ftp_tls.go — explicit TLS (FTPS) for the built-in FTP server.
//
// The server keeps one self-signed certificate per device in the app data
// folder, made on the first start and reused after that, so the fingerprint a
// user pinned in their FTP client keeps matching across restarts. Clients
// upgrade the control connection with AUTH TLS (RFC 4217) before signing in,
// and PROT P puts the passive data connections behind TLS as well; the server
// is always the TLS server, for data channels too. Nothing here trusts a CA:
// the certificate is self-signed, and the sheet shows its SHA-256 fingerprint
// for the client to check.
//
// Without a usable certificate the server still starts, and AUTH TLS answers
// 431 instead of breaking plain FTP.

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ftpCertFileName is the certificate and key, in the app's data folder.
const ftpCertFileName = "ftp_tls.pem"

// ftpCertPath is where the server's certificate lives. The tests point
// bkupDataDirOverride at a temp folder, so they never touch this one.
func ftpCertPath() string {
	dir := strings.TrimSpace(bkupDataDir())
	if dir == "" {
		dir = bkupAppDir()
	}
	return filepath.Join(dir, ftpCertFileName)
}

// authTLS runs AUTH TLS on a session that has not signed in: it answers 234
// and then wraps the control connection. A failed handshake ends the session
// (false); every other answer keeps it, plaintext, for a client that carries
// on with FTP. The certificate is this run's, so a restarted server presents
// the same fingerprint the user pinned.
func (s *ftpSession) authTLS(arg string) bool {
	switch {
	case s.authed:
		s.reply("503 Already signed in — start a new connection to use TLS")
	case !strings.EqualFold(strings.TrimSpace(arg), "TLS"):
		s.reply("504 Only AUTH TLS is supported")
	case s.tlsOn:
		s.reply("503 TLS is already on")
	case s.tlsConf == nil:
		s.reply("431 TLS is not available on this device")
	default:
		s.reply("234 AUTH TLS successful; starting TLS")
		tc := tls.Server(s.conn, s.tlsConf)
		// A client that sends its ClientHello and then waits: the deadline is
		// the same as a passive data accept's, so a stale socket cannot hold
		// a session slot forever.
		_ = tc.SetDeadline(time.Now().Add(ftpAcceptWait))
		if err := tc.Handshake(); err != nil {
			logDebug("ftp", "TLS handshake from %s failed: %v", s.remoteIP, err)
			_ = s.conn.Close()
			return false
		}
		_ = tc.SetDeadline(time.Time{})
		s.conn = tc
		s.r = bufio.NewReaderSize(tc, ftpMaxLineBytes)
		s.tlsOn = true
		// RFC 4217: AUTH TLS starts a new security state, so an account name
		// sent before it cannot be paired with a password sent after it.
		s.user = ""
		s.authed = false
	}
	return true
}

// ftpCertFingerprint is the SHA-256 of the certificate's DER bytes, written the
// way FTP clients and openssl show it: uppercase hex pairs, colon separated.
func ftpCertFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// ftpLoadOrCreateCert returns the server's certificate and fingerprint. A
// missing file, an unreadable one, or one that has less than 30 days left is
// replaced by a fresh certificate; the file is written 0600.
func ftpLoadOrCreateCert(path string) (tls.Certificate, string, error) {
	if cert, fp, ok := ftpReadCert(path); ok {
		return cert, fp, nil
	}
	return ftpCreateCert(path)
}

// ftpReadCert loads a usable certificate from path.
func ftpReadCert(path string) (tls.Certificate, string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return tls.Certificate{}, "", false
	}
	cert, err := tls.X509KeyPair(raw, raw)
	if err != nil {
		return tls.Certificate{}, "", false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", false
	}
	if time.Until(leaf.NotAfter) < 30*24*time.Hour {
		return tls.Certificate{}, "", false
	}
	cert.Leaf = leaf
	return cert, ftpCertFingerprint(leaf.Raw), true
}

// ftpCreateCert makes a self-signed ECDSA P-256 certificate for the server and
// stores it with its key in one PEM file.
func ftpCreateCert(path string) (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("generate FTP TLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("generate FTP TLS serial: %w", err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "JukaHub FTP", Organization: []string{"JukaHub"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", "jukahub.local"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if ip := localIPv4(); ip != "" {
		if parsed := net.ParseIP(ip); parsed != nil && !parsed.IsLoopback() {
			tmpl.IPAddresses = append(tmpl.IPAddresses, parsed)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("create FTP TLS certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("encode FTP TLS key: %w", err)
	}
	var buf strings.Builder
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := pem.Encode(&buf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return tls.Certificate{}, "", err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return tls.Certificate{}, "", fmt.Errorf("make FTP TLS folder: %w", err)
		}
	}
	// Write a sibling first, then rename, so a crash never leaves half a
	// certificate behind for the next start to trip over.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(buf.String()), 0o600); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("write FTP TLS certificate: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return tls.Certificate{}, "", fmt.Errorf("store FTP TLS certificate: %w", err)
	}
	cert, err := tls.X509KeyPair([]byte(buf.String()), []byte(buf.String()))
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("load new FTP TLS certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("parse new FTP TLS certificate: %w", err)
	}
	cert.Leaf = leaf
	return cert, ftpCertFingerprint(leaf.Raw), nil
}
