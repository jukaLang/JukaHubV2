package main

// Tests for the VNC server's protocol layer: VNC Authentication against
// vectors computed outside Go (OpenSSL DES-ECB with the bit-reversed key),
// the handshake offering nothing but a password, the client-message bounds,
// and the pixel-format conversion.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"
)

var vncTestChallenge = []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

func TestVNCAuthResponseVectors(t *testing.T) {
	for _, tc := range []struct{ password, want string }{
		{"password", "b866924125c8eebb9debc1db61c538e2"},
		{"jukahub", "f718962a472b73a937df30549267c256"},       // zero-padded key
		{"password-long", "b866924125c8eebb9debc1db61c538e2"}, // only 8 bytes count
	} {
		got, err := vncAuthResponse(tc.password, vncTestChallenge)
		if err != nil || hex.EncodeToString(got) != tc.want {
			t.Errorf("vncAuthResponse(%q) = %x, %v; want %s", tc.password, got, err, tc.want)
		}
		if !vncAuthCheck(tc.password, vncTestChallenge, got) || vncAuthCheck("wrongpw1", vncTestChallenge, got) {
			t.Errorf("vncAuthCheck(%q) does not tell the right response from a wrong password", tc.password)
		}
	}
	if vncAuthCheck("", vncTestChallenge, make([]byte, 16)) {
		t.Error("an empty password must never match")
	}
}

// The server offers VNC Authentication alone and tells a wrong password so;
// Apple's 3.889 is served as 3.8.
func TestVNCHandshakeOffersOnlyPassword(t *testing.T) {
	for _, tc := range []struct {
		password string
		ok       bool
	}{{"abcdefgh", true}, {"zzzzzzzz", false}} {
		srv, cli := net.Pipe()
		done := make(chan bool, 1)
		go func() {
			_, ok, _ := vncHandshake(srv, "abcdefgh", vncTestChallenge, nil)
			done <- ok
		}()
		_ = cli.SetDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 16)
		if _, err := io.ReadFull(cli, buf[:12]); err != nil || string(buf[:12]) != vncServerVersion {
			t.Fatalf("version %q, %v", buf[:12], err)
		}
		_, _ = cli.Write([]byte("RFB 003.889\n"))
		if _, err := io.ReadFull(cli, buf[:2]); err != nil || buf[0] != 1 || buf[1] != vncSecVNCAuth {
			t.Fatalf("security types %v, %v; want exactly [VNC auth]", buf[:2], err)
		}
		_, _ = cli.Write([]byte{vncSecVNCAuth})
		if _, err := io.ReadFull(cli, buf); err != nil || !bytes.Equal(buf, vncTestChallenge) {
			t.Fatalf("challenge %x, %v", buf, err)
		}
		resp, _ := vncAuthResponse(tc.password, buf)
		_, _ = cli.Write(resp)
		if _, err := io.ReadFull(cli, buf[:4]); err != nil {
			t.Fatal(err)
		}
		if result := binary.BigEndian.Uint32(buf[:4]); (result == 0) != tc.ok {
			t.Errorf("password %q: SecurityResult %d", tc.password, result)
		}
		if !tc.ok {
			// 3.8 says why.
			if _, err := io.ReadFull(cli, buf[:4]); err == nil {
				_, _ = io.ReadFull(cli, make([]byte, binary.BigEndian.Uint32(buf[:4])))
			}
		}
		if got := <-done; got != tc.ok {
			t.Errorf("password %q: handshake ok = %v", tc.password, got)
		}
		_ = cli.Close()
		_ = srv.Close()
	}
}

func TestVNCClientMessageBounds(t *testing.T) {
	cutText := func(n int32, body []byte) []byte {
		b := []byte{vncMsgCutText, 0, 0, 0}
		return append(binary.BigEndian.AppendUint32(b, uint32(n)), body...)
	}
	key := []byte{vncMsgKeyEvent, 1, 0, 0, 0, 0, 0xff, 0x0d}

	// Text over the cap is read past, and the next message still parses.
	long := append(cutText(vncMaxCutText+1, make([]byte, vncMaxCutText+1)), key...)
	r := bufio.NewReader(bytes.NewReader(long))
	if m, err := vncReadClientMessage(r, nil); err != nil || !m.skipped || m.text != "" {
		t.Fatalf("long cut text: %+v, %v", m.skipped, err)
	}
	if m, err := vncReadClientMessage(r, nil); err != nil || m.kind != vncMsgKeyEvent || !m.down || m.key != 0xff0d {
		t.Fatalf("message after the long text: %+v, %v", m, err)
	}

	for name, msg := range map[string][]byte{
		"cut text over the skip limit": cutText(vncMaxCutTextSkip+1, nil),
		"negative cut text":            cutText(-4, nil),
		"too many encodings":           {vncMsgSetEncodings, 0, 0x02, 0x01},
		"unknown message":              {42},
	} {
		if _, err := vncReadClientMessage(bufio.NewReader(bytes.NewReader(msg)), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// Latin-1 clipboard text becomes UTF-8.
	m, err := vncReadClientMessage(bufio.NewReader(bytes.NewReader(cutText(3, []byte{'c', 'a', 0xe9}))), nil)
	if err != nil || m.text != "caé" {
		t.Errorf("Latin-1 text = %q, %v", m.text, err)
	}
}

func TestVNCPixelFormatConversion(t *testing.T) {
	px := []byte{0x11, 0x22, 0x33, 0xff} // RGBX
	for _, tc := range []struct {
		name string
		pf   vncPixelFormat
		want string // one pixel, then its ZRLE CPIXEL
	}{
		{"server native", vncNativeFormat, "112233ff 112233"},
		{"BGRX little-endian", vncPixelFormat{bpp: 32, depth: 24, trueColour: true, rMax: 255, gMax: 255, bMax: 255, rShift: 16, gShift: 8, bShift: 0},
			"33221100 332211"},
		{"XRGB big-endian", vncPixelFormat{bpp: 32, depth: 24, bigEndian: true, trueColour: true, rMax: 255, gMax: 255, bMax: 255, rShift: 16, gShift: 8, bShift: 0},
			"00112233 112233"},
		{"RGB565 big-endian", vncPixelFormat{bpp: 16, depth: 16, bigEndian: true, trueColour: true, rMax: 31, gMax: 63, bMax: 31, rShift: 11, gShift: 5, bShift: 0},
			"1106 1106"},
		{"BGR233", vncPixelFormat{bpp: 8, depth: 8, trueColour: true, rMax: 7, gMax: 7, bMax: 3, rShift: 0, gShift: 3, bShift: 6},
			"48 48"},
	} {
		if !tc.pf.usable() {
			t.Errorf("%s: not usable", tc.name)
			continue
		}
		c := newVncConv(tc.pf)
		full := c.appendRaw(nil, px, 4, vncRect{w: 1, h: 1})
		cpix := c.putC(nil, c.value(px))
		if got := hex.EncodeToString(full) + " " + hex.EncodeToString(cpix); got != tc.want {
			// The native format sends the stored X byte as padding; the
			// others build the value from the channels alone.
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	if (vncPixelFormat{bpp: 8, depth: 8, rMax: 7, gMax: 7, bMax: 3}).usable() {
		t.Error("a colour-map format must not be usable")
	}
}
