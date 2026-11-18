package main

// moonlight_pair.go — PIN pairing with a GameStream PC.
//
// This is the exchange every Moonlight client runs (moonlight-qt's
// NvPairingManager), so Vibepollo, Apollo, Sunshine and GeForce Experience
// all accept it. JukaHub shows a 4-digit PIN; the user types it on the PC
// (Sunshine / Apollo / Vibepollo: the web UI's PIN page). Both sides prove
// they know the PIN without sending it, and each signs a secret with its
// certificate's key, so a machine in the middle can neither learn the PIN nor
// swap a certificate in:
//
//  1. HTTP getservercert with a random salt and JukaHub's certificate. The PC
//     holds this request open until the PIN is typed, then answers with its
//     own certificate (empty: another pairing is in progress there).
//     AES-128-ECB key = hash(salt || PIN)[:16]; the hash is SHA-256 for a
//     host whose appversion major is 7 or more, SHA-1 before (GeForce
//     Experience's older generations).
//  2. A random challenge, encrypted -> the PC's answer decrypts to the hash it
//     made with the PIN and a challenge of its own.
//  3. JukaHub answers that challenge (hashed with its certificate's
//     signature and a secret of its own) -> the PC's secret and its RSA
//     signature of it. The signature must check out with the PC's
//     certificate, and the PC's hash must match what the PIN gives: a wrong
//     PIN fails here.
//  4. JukaHub's secret and its signature -> the PC accepts JukaHub.
//  5. HTTPS pairchallenge with the PC's certificate pinned and JukaHub's
//     certificate presented: both sides now trust each other.
//
// Any failure after step 1 sends /unpair, so the PC does not keep a half
// pairing. On success the PC's certificate is saved with the host and pinned
// for every HTTPS request after (moonlight_proto.go).

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" // #nosec G505 -- GameStream mandates SHA-1 for hosts before appversion 7
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// mlPairFirstTimeout bounds step 1, which the PC holds open until the PIN
	// is typed there.
	mlPairFirstTimeout = 120 * time.Second
	// mlPairStepTimeout bounds every later step.
	mlPairStepTimeout = 10 * time.Second
	mlPINDigits       = 4
)

var (
	errMLPairBusy     = errors.New("another pairing is in progress on the PC — finish or cancel it there, then try again")
	errMLWrongPIN     = errors.New("wrong PIN")
	errMLPairCertBad  = errors.New("the PC's certificate did not check out")
	errMLPairRefused  = errors.New("the PC refused to pair")
	errMLPairTimedOut = errors.New("the PIN was not typed on the PC in time — try again")
	errMLBadPIN       = errors.New("the PIN must be 4 digits")
	errMLUnpairOnPC   = errors.New("forgotten here — also remove JukaHub from the paired clients in the PC's web UI")
)

// mlNewPIN is a random 4-digit PIN for the user to type on the PC.
func mlNewPIN() string {
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		// crypto/rand does not fail on the systems JukaHub runs on; a fixed
		// PIN would still pair, only less privately.
		return "0000"
	}
	return fmt.Sprintf("%04d", n.Int64())
}

// mlDeviceName is how the PC lists JukaHub among its clients (Apollo and
// Vibepollo keep per-client permissions by it): "JukaHub" and the handheld's
// model, or the computer's name off a handheld.
func mlDeviceName() string {
	d := DetectDevice()
	name := ""
	if d.IsTrimui() {
		name = deviceDisplayName(d)
	} else if hn, err := os.Hostname(); err == nil {
		name = strings.TrimSpace(hn)
	}
	if name == "" {
		name = "PC"
	}
	return "JukaHub " + name
}

// mlPairHash is the pairing's hash: SHA-256 from appversion 7 on, SHA-1 before.
func mlPairHash(appVersion string) (func() hash.Hash, int) {
	if mlAppVersionMajor(appVersion) >= 7 {
		return sha256.New, sha256.Size
	}
	return sha1.New, sha1.Size // #nosec G401 -- protocol-mandated for old hosts
}

// mlHashOf hashes the parts in order.
func mlHashOf(newHash func() hash.Hash, parts ...[]byte) []byte {
	h := newHash()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// mlECB encrypts or decrypts whole 16-byte blocks with AES-128-ECB, the mode
// GameStream pairing uses (no padding: every input is a block multiple).
func mlECB(block cipher.Block, data []byte, decrypt bool) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("pairing data is not whole AES blocks")
	}
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += aes.BlockSize {
		end := i + aes.BlockSize
		if decrypt {
			block.Decrypt(out[i:end], data[i:end])
		} else {
			block.Encrypt(out[i:end], data[i:end])
		}
	}
	return out, nil
}

// mlRandom is n bytes from crypto/rand.
func mlRandom(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// mlPairStep sends one HTTP pairing step and checks <paired>1</paired>.
func mlPairStep(ctx context.Context, host string, port int, query string, timeout time.Duration) (map[string]string, error) {
	root, err := mlGetXML(ctx, mlRequest{host: host, port: port, path: "pair", query: query, timeout: timeout})
	if err != nil {
		return nil, err
	}
	f := root.fields()
	if f["paired"] != "1" {
		return f, errMLPairRefused
	}
	return f, nil
}

// mlSendUnpair tells the PC to drop a pairing (HTTP /unpair), best effort and
// on its own short deadline: it runs after a failure, often after the
// caller's context has ended.
func mlSendUnpair(host string, port int) error {
	ctx, cancel := context.WithTimeout(context.Background(), mlPairStepTimeout)
	defer cancel()
	_, err := mlDo(ctx, mlRequest{host: host, port: port, path: "unpair", timeout: mlPairStepTimeout})
	return err
}

// mlPair runs the PIN pairing with the PC at addr. It blocks until the PIN is
// typed on the PC (the PC holds the first request open, up to two minutes) or
// ctx ends, and returns the host with ServerCert and Paired set, saved.
// Errors worth telling apart: errMLWrongPIN, errMLPairBusy, errMLPairCertBad,
// errMLPairTimedOut, errMLHostNotFound, ctx.Err().
func mlPair(ctx context.Context, h mlHost, addr, pin string) (mlHost, error) {
	pin = strings.TrimSpace(pin)
	if len(pin) != mlPINDigits || strings.Trim(pin, "0123456789") != "" {
		return h, errMLBadPIN
	}
	host := mlAddrFor(h, addr)
	if host == "" {
		return h, errMLHostNotFound
	}
	port := mlHTTPPortOf(h)
	id, err := mlIdentity()
	if err != nil {
		return h, err
	}
	info, err := mlServerInfoHTTP(ctx, host, port)
	if err != nil {
		if ctx.Err() != nil {
			return h, ctx.Err()
		}
		if mlHTTPCode(err) != 0 || errors.Is(err, errMLNotGameStream) {
			return h, err
		}
		return h, errMLHostNotFound
	}
	if h.UUID != "" && !strings.EqualFold(info.UUID, h.UUID) {
		return h, fmt.Errorf("another PC answers at %s", host)
	}
	newHash, hashLen := mlPairHash(info.AppVersion)
	salt, err := mlRandom(16)
	if err != nil {
		return h, err
	}
	block, err := aes.NewCipher(mlHashOf(newHash, salt, []byte(pin))[:16])
	if err != nil {
		return h, err
	}
	devName := "devicename=" + mlQueryEscape(mlDeviceName()) + "&updateState=1"

	// 1. The PC's certificate, once the PIN is typed there.
	f, err := mlPairStep(ctx, host, port, devName+"&phrase=getservercert&salt="+hex.EncodeToString(salt)+
		"&clientcert="+hex.EncodeToString(id.certPEM), mlPairFirstTimeout)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return h, ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			return h, errMLPairTimedOut
		}
		return h, err
	}
	certPEM, _ := hex.DecodeString(f["plaincert"])
	if len(certPEM) == 0 {
		_ = mlSendUnpair(host, port)
		return h, errMLPairBusy
	}
	pemBlock, _ := pem.Decode(certPEM)
	if pemBlock == nil {
		_ = mlSendUnpair(host, port)
		return h, errMLPairCertBad
	}
	serverCert, err := x509.ParseCertificate(pemBlock.Bytes)
	if err != nil {
		_ = mlSendUnpair(host, port)
		return h, errMLPairCertBad
	}
	fail := func(err error) (mlHost, error) {
		_ = mlSendUnpair(host, port)
		if ctx.Err() != nil {
			return h, ctx.Err()
		}
		return h, err
	}

	// 2. Our challenge; the PC's hash and its challenge back.
	challenge, err := mlRandom(16)
	if err != nil {
		return fail(err)
	}
	encChallenge, err := mlECB(block, challenge, false)
	if err != nil {
		return fail(err)
	}
	f, err = mlPairStep(ctx, host, port, devName+"&clientchallenge="+hex.EncodeToString(encChallenge), mlPairStepTimeout)
	if err != nil {
		return fail(err)
	}
	encResponse, err := hex.DecodeString(f["challengeresponse"])
	if err != nil || len(encResponse) < hashLen+16 {
		return fail(errMLPairRefused)
	}
	response, err := mlECB(block, encResponse, true)
	if err != nil {
		return fail(errMLPairRefused)
	}
	serverResponseHash := response[:hashLen]
	serverChallenge := response[hashLen : hashLen+16]

	// 3. Our answer to its challenge; its secret, signed, back.
	clientSecret, err := mlRandom(16)
	if err != nil {
		return fail(err)
	}
	answer := mlHashOf(newHash, serverChallenge, id.cert.Signature, clientSecret)
	padded := make([]byte, (len(answer)+aes.BlockSize-1)/aes.BlockSize*aes.BlockSize)
	copy(padded, answer)
	encAnswer, err := mlECB(block, padded, false)
	if err != nil {
		return fail(err)
	}
	f, err = mlPairStep(ctx, host, port, devName+"&serverchallengeresp="+hex.EncodeToString(encAnswer), mlPairStepTimeout)
	if err != nil {
		return fail(err)
	}
	pairingSecret, err := hex.DecodeString(f["pairingsecret"])
	if err != nil || len(pairingSecret) <= 16 {
		return fail(errMLPairRefused)
	}
	serverSecret, serverSig := pairingSecret[:16], pairingSecret[16:]
	pub, ok := serverCert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fail(errMLPairCertBad)
	}
	secretDigest := sha256.Sum256(serverSecret)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, secretDigest[:], serverSig); err != nil {
		logWarn("moonlight", "pairing with %s: the PC's signature did not verify: %v", host, err)
		return fail(errMLPairCertBad)
	}
	expected := mlHashOf(newHash, challenge, serverCert.Signature, serverSecret)
	if subtle.ConstantTimeCompare(expected, serverResponseHash) != 1 {
		return fail(errMLWrongPIN)
	}

	// 4. Our secret, signed.
	clientDigest := sha256.Sum256(clientSecret)
	clientSig, err := rsa.SignPKCS1v15(rand.Reader, id.key, crypto.SHA256, clientDigest[:])
	if err != nil {
		return fail(err)
	}
	if _, err := mlPairStep(ctx, host, port, devName+"&clientpairingsecret="+
		hex.EncodeToString(append(append([]byte(nil), clientSecret...), clientSig...)), mlPairStepTimeout); err != nil {
		return fail(err)
	}

	// 5. Both certificates over HTTPS.
	httpsPort := mlHTTPSPortOf(h)
	if info.HTTPSPort > 0 && info.HTTPSPort < 65536 {
		httpsPort = info.HTTPSPort
	}
	root, err := mlGetXML(ctx, mlRequest{host: host, port: httpsPort, https: true, pinned: serverCert.Raw,
		path: "pair", query: devName + "&phrase=pairchallenge", timeout: mlPairStepTimeout})
	if err != nil {
		return fail(err)
	}
	if root.fields()["paired"] != "1" {
		return fail(errMLPairRefused)
	}

	h = mlHostFromInfo(h, info, host)
	h.ServerCert = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Raw}))
	h.Paired = true
	saved, err := mlPutPairedHost(h)
	if err != nil {
		// Paired on the PC, but not remembered here: the next pairing would
		// replace it anyway, and the user has to know the save failed.
		return h, fmt.Errorf("paired, but saving it failed: %w", err)
	}
	logInfo("moonlight", "paired with %s (%s, %s)", saved.Label(), saved.ServerKind, host)
	return saved, nil
}

// mlUnpair forgets the pairing: it asks the PC to drop JukaHub (HTTP
// /unpair) and clears the pinned certificate here whatever the PC answered.
// GeForce Experience forgets the client by its uniqueid (JukaHub's own, so
// no other device is touched); Sunshine, Apollo and Vibepollo have no unpair
// request at all and answer 404 — errMLUnpairOnPC then says to remove
// JukaHub in the PC's web UI. Blocks up to ~10 s.
func mlUnpair(ctx context.Context, h mlHost, addr string) error {
	host := mlAddrFor(h, addr)
	var err error
	if host == "" {
		err = errMLHostNotFound
	} else {
		cctx, cancel := context.WithTimeout(ctx, mlPairStepTimeout)
		_, err = mlDo(cctx, mlRequest{host: host, port: mlHTTPPortOf(h), path: "unpair", timeout: mlPairStepTimeout})
		cancel()
		if mlHTTPCode(err) == http.StatusNotFound {
			err = errMLUnpairOnPC
		}
	}
	if _, _, serr := mlUpdateHost(h.UUID, func(s *mlHost) {
		s.ServerCert, s.Paired = "", false
	}); serr != nil {
		logWarn("moonlight", "forgetting the pairing of %s failed: %v", h.Label(), serr)
		if err == nil {
			err = serr
		}
	}
	return err
}
