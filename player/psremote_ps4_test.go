package main

// psremote_ps4_test.go — the PS4 pairing protocol.
//
// The payload digests below were produced by the reference implementation the
// TrimUI port used (a pure-Python version of chiaki's registration code) with
// the same PIN, account ID and ambassador. Matching them byte for byte is what
// proves this Go port did not drift from the wire format.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	testAmbassador = mustHex("00112233445566778899aabbccddeeff")
	testAccountID  = [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
)

func TestPS4PayloadMatchesReferenceImplementation(t *testing.T) {
	cases := []struct {
		pre10   bool
		wantSHA string
		wantKey string
	}{
		{false, "6ff3800eef72da1b9b37160e39f29106618995a1dbd3123877cba54dd4316b87", "cebcb640080776047b85e85bf350f52d"},
		{true, "91a2eaf5348971172eab01673665f61bc572f795079ee401d4d4e5dc6c87d1fa", "3fa0a5f8dcbb3ecc50baedef9734c7c9"},
	}
	for _, tc := range cases {
		payload, bright, err := ps4BuildPayload(12345678, testAccountID, testAmbassador, tc.pre10)
		if err != nil {
			t.Fatalf("build payload: %v", err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(payload))
		if sum != tc.wantSHA {
			t.Fatalf("pre10=%v payload sha = %s, want %s", tc.pre10, sum, tc.wantSHA)
		}
		if got := hex.EncodeToString(bright); got != tc.wantKey {
			t.Fatalf("pre10=%v bright = %s, want %s", tc.pre10, got, tc.wantKey)
		}
		if len(payload) < ps4InnerOffset || string(payload[:4]) != "AAAA" {
			t.Fatalf("payload header = %x", payload[:4])
		}
	}
}

func TestPS4ParsePinAndAccount(t *testing.T) {
	if _, err := ps4ParsePin("1234567"); err == nil {
		t.Fatal("a 7-digit PIN must be refused")
	}
	if _, err := ps4ParsePin("1234abcd"); err == nil {
		t.Fatal("a non-numeric PIN must be refused")
	}
	if pin, err := ps4ParsePin(" 12345678 "); err != nil || pin != 12345678 {
		t.Fatalf("pin = %d (%v)", pin, err)
	}
	if _, _, err := ps4DecodeAccountID("not base64!"); err == nil {
		t.Fatal("invalid base64 must be refused")
	}
	if _, _, err := ps4DecodeAccountID("AAAA"); err == nil {
		t.Fatal("a 3-byte account ID must be refused")
	}
	if _, offline, err := ps4DecodeAccountID(""); err != nil || !offline {
		t.Fatalf("empty account should be the offline default: %v %v", offline, err)
	}
	encoded := base64.StdEncoding.EncodeToString(testAccountID[:])
	decoded, offline, err := ps4DecodeAccountID(encoded)
	if err != nil || offline || decoded != testAccountID {
		t.Fatalf("account decode = %v %v %v", decoded, offline, err)
	}
}

func TestPS4ParseResultBody(t *testing.T) {
	// ps4-registkey carries the key's ASCII hex spelling.
	plain := "HTTP/1.1 200 OK\r\nContent-Length: 130\r\n" +
		"ps4-registkey: 6162636465663132\r\n" +
		"rp-key: 000102030405060708090a0b0c0d0e0f\r\n" +
		"ps4-mac: aabbccddeeff\r\nrp-keytype: 0\r\nps4-nickname: Living Room\r\n"
	console, err := ps4ParseResultBody([]byte(plain))
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if console.RegistKey != "abcdef12" || console.RPKey != testConsole().RPKey {
		t.Fatalf("console = %+v", console)
	}
	if console.ServerMAC != "aabbccddeeff" || console.Name != "Living Room" {
		t.Fatalf("console = %+v", console)
	}
	if !console.paired() {
		t.Fatalf("a parsed console must count as paired: %+v", console)
	}

	if _, err := ps4ParseResultBody([]byte("HTTP/1.1 403 Forbidden\r\nrp-application-reason: 80108b03\r\n\r\n")); err == nil ||
		!strings.Contains(err.Error(), "fresh PIN") {
		t.Fatalf("403 should suggest reopening Add Device, got %v", err)
	}
	if _, err := ps4ParseResultBody([]byte("HTTP/1.1 200 OK\r\nps4-registkey: 4142\r\n\r\n")); err == nil ||
		!strings.Contains(err.Error(), "rp-key") {
		t.Fatalf("missing keys should be reported, got %v", err)
	}
}

// fakePS4 answers the registration search on UDP and the registration request
// on TCP on the same port number, which is what a real console does.
type fakePS4 struct {
	udp      *net.UDPConn
	tcp      net.Listener
	response []byte
	status   string
}

func startFakePS4(t *testing.T, response []byte, status string) *fakePS4 {
	t.Helper()
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("udp listen: %v", err)
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	tcp, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		udp.Close()
		t.Skipf("cannot bind matching TCP port %d: %v", port, err)
	}
	fake := &fakePS4{udp: udp, tcp: tcp, response: response, status: status}
	t.Cleanup(func() {
		udp.Close()
		tcp.Close()
	})
	go func() {
		buf := make([]byte, 64)
		_ = udp.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, from, err := udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n >= 4 && string(buf[:4]) == "SRC2" {
			_, _ = udp.WriteToUDP([]byte("RES2"), from)
		}
	}()
	go func() {
		conn, err := tcp.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		// Read the request headers, then Content-Length bytes of payload.
		var data []byte
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				data = append(data, buf[:n]...)
			}
			if idx := bytes.Index(data, []byte("\r\n\r\n")); idx >= 0 {
				length := 0
				for _, line := range strings.Split(string(data[:idx]), "\r\n") {
					if strings.HasPrefix(strings.ToLower(line), "content-length:") {
						length, _ = strconv.Atoi(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
					}
				}
				if len(data) >= idx+4+length {
					break
				}
			}
			if err != nil {
				break
			}
		}
		if len(fake.response) > 0 {
			_, _ = conn.Write(fake.response)
		} else {
			_, _ = conn.Write([]byte("HTTP/1.1 " + status + "\r\nContent-Length: 0\r\n\r\n"))
		}
	}()
	return fake
}

func TestPS4RegisterAgainstFakeConsole(t *testing.T) {
	plain := []byte("HTTP/1.1 200 OK\r\nContent-Length: 130\r\n" +
		"ps4-registkey: 6162636465663132\r\n" +
		"rp-key: 000102030405060708090a0b0c0d0e0f\r\n" +
		"ps4-mac: aabbccddeeff\r\nrp-keytype: 0\r\nps4-nickname: Living Room\r\n\r\n")
	_, bright, err := ps4BuildPayload(12345678, testAccountID, testAmbassador, false)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	encrypted, err := ps4CFB(plain, bright, testAmbassador, false, false)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// The console wraps the encrypted block in its own HTTP response.
	response := []byte(fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(encrypted)))
	response = append(response, encrypted...)
	fake := startFakePS4(t, response, "200 OK")

	// Point the client at the fake console and make the ambassador deterministic.
	oldPort, oldAmbassador := ps4SearchPortOverride, ps4AmbassadorOverride
	ps4SearchPortOverride = fake.udp.LocalAddr().(*net.UDPAddr).Port
	ps4AmbassadorOverride = testAmbassador
	t.Cleanup(func() {
		ps4SearchPortOverride, ps4AmbassadorOverride = oldPort, oldAmbassador
	})

	console, err := ps4Register("127.0.0.1", "12345678", base64.StdEncoding.EncodeToString(testAccountID[:]), psTargetPS4FW10, 3*time.Second)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if console.Host != "127.0.0.1" || console.Name != "Living Room" {
		t.Fatalf("console = %+v", console)
	}
	if console.RegistKey != "abcdef12" || console.RPKey != testConsole().RPKey {
		t.Fatalf("credentials = %+v", console)
	}
	if console.Target != psTargetPS4FW10 || console.PS5 {
		t.Fatalf("target = %d ps5=%v", console.Target, console.PS5)
	}
}

func TestPS4RegisterReportsRefusal(t *testing.T) {
	fake := startFakePS4(t, nil, "403 Forbidden")
	oldPort, oldAmbassador := ps4SearchPortOverride, ps4AmbassadorOverride
	ps4SearchPortOverride = fake.udp.LocalAddr().(*net.UDPAddr).Port
	ps4AmbassadorOverride = testAmbassador
	t.Cleanup(func() {
		ps4SearchPortOverride, ps4AmbassadorOverride = oldPort, oldAmbassador
	})
	_, err := ps4Register("127.0.0.1", "12345678", "", psTargetPS4FW10, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("a 403 must be reported, got %v", err)
	}
}

func TestPS4RegisterRejectsBadPINBeforeTouchingTheNetwork(t *testing.T) {
	if _, err := ps4Register("127.0.0.1", "12", "", psTargetPS4FW10, time.Second); err == nil ||
		!strings.Contains(err.Error(), "8 digits") {
		t.Fatalf("bad PIN should fail locally, got %v", err)
	}
}
