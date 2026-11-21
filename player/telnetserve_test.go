package main

// Tests for the Telnet server's protocol layer: IAC stripping, NAWS (also
// split across reads and with a doubled IAC inside), CR folding, bounded
// subnegotiations, one-time refusals, and the sign-in line reader.

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

// telnetTestParser records what the parser reports.
func telnetTestParser() (*telnetParser, *[][2]int, *bytes.Buffer) {
	sizes := &[][2]int{}
	replies := &bytes.Buffer{}
	p := newTelnetParser()
	p.onSize = func(c, r int) { *sizes = append(*sizes, [2]int{c, r}) }
	p.reply = func(b []byte) { replies.Write(b) }
	return p, sizes, replies
}

func TestTelnetParserDataAndNAWS(t *testing.T) {
	in := []byte{'l', 's', telnetIAC, telnetIAC, '\r', 0, 'a', '\r', '\n', 'b', '\n',
		telnetIAC, telnetSB, telnetOptNAWS, 0, 80, 0, 24, telnetIAC, telnetSE,
		telnetIAC, telnetSB, telnetOptNAWS, 0, telnetIAC, telnetIAC, 0, 30, telnetIAC, telnetSE,
		telnetIAC, telnetIP, 'z'}
	want := []byte{'l', 's', 0xff, '\r', 'a', '\r', 'b', '\n', 0x03, 'z'}
	wantSizes := [][2]int{{80, 24}, {255, 30}}

	// Whole, and one byte at a time: state carries across reads.
	for _, split := range []bool{false, true} {
		p, sizes, _ := telnetTestParser()
		var out []byte
		if split {
			for i := range in {
				out = p.feed(in[i:i+1], out)
			}
		} else {
			out = p.feed(in, nil)
		}
		if !bytes.Equal(out, want) {
			t.Errorf("split=%v: data %q, want %q", split, out, want)
		}
		if len(*sizes) != 2 || (*sizes)[0] != wantSizes[0] || (*sizes)[1] != wantSizes[1] {
			t.Errorf("split=%v: sizes %v, want %v", split, *sizes, wantSizes)
		}
	}
}

func TestTelnetParserBoundsAndNegotiation(t *testing.T) {
	p, sizes, replies := telnetTestParser()
	// An endless subnegotiation is capped and ignored; data after it flows.
	long := append([]byte{telnetIAC, telnetSB, telnetOptNAWS}, bytes.Repeat([]byte{1}, 500)...)
	long = append(long, telnetIAC, telnetSE, 'o', 'k')
	if out := p.feed(long, nil); string(out) != "ok" || len(*sizes) != 0 || len(p.sb) > telnetSBMax {
		t.Errorf("long SB: data %q, sizes %v, kept %d bytes", out, *sizes, len(p.sb))
	}
	// DO ECHO / DO SGA were offered: no answer. DO TTYPE is refused once;
	// WILL NAWS was asked for; WILL LINEMODE is refused once.
	p.feed([]byte{
		telnetIAC, telnetDO, telnetOptEcho, telnetIAC, telnetDO, telnetOptSGA,
		telnetIAC, telnetDO, 24, telnetIAC, telnetDO, 24,
		telnetIAC, telnetWILL, telnetOptNAWS,
		telnetIAC, telnetWILL, 34, telnetIAC, telnetWILL, 34,
	}, nil)
	want := []byte{telnetIAC, telnetWONT, 24, telnetIAC, telnetDONT, 34}
	if !bytes.Equal(replies.Bytes(), want) {
		t.Errorf("replies %v, want %v", replies.Bytes(), want)
	}
	if got := telnetEscapeIAC(nil, []byte{'a', 0xff, 'b'}); !bytes.Equal(got, []byte{'a', 0xff, 0xff, 'b'}) {
		t.Errorf("escape %v", got)
	}
}

func TestTelnetReadLine(t *testing.T) {
	p, _, _ := telnetTestParser()
	r := bufio.NewReader(strings.NewReader("pass\x7fs\xff\xfb\x18word\r\nnext\n"))
	if line, err := telnetReadLine(r, p, telnetLineMax); err != nil || line != "password" {
		t.Errorf("line %q, %v", line, err)
	}
	// The LF of the CR LF is folded away, not read as an empty line.
	if line, err := telnetReadLine(r, p, telnetLineMax); err != nil || line != "next" {
		t.Errorf("second line %q, %v", line, err)
	}
	r = bufio.NewReader(strings.NewReader(strings.Repeat("x", 500) + "\r"))
	if line, _ := telnetReadLine(r, p, 8); line != "xxxxxxxx" {
		t.Errorf("long line kept %d characters", len(line))
	}
}
