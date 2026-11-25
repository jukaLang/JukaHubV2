package main

// castaudio_tap.go — a game's sound on its way to Screen Share.
//
// ALSA's file plugin (castaudio_alsa.go) starts
//
//	JukaHub --share-audio-tap <port>
//
// for every sound stream the game opens and writes the stream to its standard
// input: a WAV header (the plugin's "wav" format, which is how the rate and
// the channel count arrive), then 16-bit samples for as long as the game
// plays. The tap reads it in 20 ms pieces and sends each as one datagram to
// 127.0.0.1:<port>, where JukaHub listens (castTapEnsure) and hands it to the
// mix (castAudioTapFeed).
//
// The tap must never make the game wait or fail: a pipe whose reader is gone
// kills the writer with SIGPIPE, and one that is not drained stalls the
// game's sound. So it reads until the game closes the stream, whatever
// happens to JukaHub — a send that fails is dropped, a header it cannot read
// leaves it draining — and its command line in the ALSA file ends in
// `|| exec cat >/dev/null`, which drains in its place if it cannot start or
// crashes.

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// castAudioTapFlag starts this binary as the tap.
	castAudioTapFlag = "--share-audio-tap"
	// castTapPreferredPort is the loopback port the receiver tries first: a
	// fixed one, so a game started before JukaHub restarted still reaches the
	// new JukaHub. Another program holding it gets a free port instead.
	castTapPreferredPort = 8327
	// castTapMagic opens every datagram.
	castTapMagic = "JHAT"
	// castTapHead is a datagram's header: the magic, the stream id (the tap's
	// process id), a sequence number, the rate, the channels and the bits.
	castTapHead = 20
	// castTapMaxPayload bounds one datagram's samples.
	castTapMaxPayload = 8192
)

// ── The receiver (JukaHub) ────────────────────────────────────────────────

// castTapRx is the loopback receiver. It is started on first use (a launch
// that gets the tap, or the tap check) and stays for the run: a datagram that
// arrives while nobody listens costs one read.
var castTapRx struct {
	mu   sync.Mutex
	port atomic.Int32
	conn *net.UDPConn
	err  string
}

// castTapEnsure starts the receiver once and returns its port, 0 when it
// could not listen.
func castTapEnsure() int {
	castTapRx.mu.Lock()
	defer castTapRx.mu.Unlock()
	if p := castTapRx.port.Load(); p > 0 {
		return int(p)
	}
	conn, err := castTapListen()
	if err != nil {
		if castTapRx.err != err.Error() {
			castTapRx.err = err.Error()
			logWarn("cast", "game sound for Screen Share: cannot listen on the loopback address: %v", err)
		}
		return 0
	}
	castTapRx.conn = conn
	port := conn.LocalAddr().(*net.UDPAddr).Port
	castTapRx.port.Store(int32(port))
	go castTapReadLoop(conn, castAudioTapFeed)
	return port
}

// castTapListen binds the loopback address, the preferred port first.
func castTapListen() (*net.UDPConn, error) {
	lo := net.IPv4(127, 0, 0, 1)
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: lo, Port: castTapPreferredPort})
	if err != nil {
		conn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: lo})
	}
	if err != nil {
		return nil, err
	}
	// A burst while the mix is busy waits in the kernel, not on the floor.
	_ = conn.SetReadBuffer(256 << 10)
	return conn, nil
}

// castTapReadLoop hands every well-formed datagram to feed until conn closes.
func castTapReadLoop(conn *net.UDPConn, feed func(stream uint32, rate, channels int, data []byte)) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("cast", "game sound receiver stopped: %v", r)
		}
	}()
	buf := make([]byte, castTapHead+castTapMaxPayload+64)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		stream, rate, channels, data, ok := castTapParse(buf[:n])
		if ok {
			feed(stream, rate, channels, data)
		}
	}
}

// castTapParse reads one datagram: ok is false for anything that is not a
// whole tap datagram of 16-bit samples.
func castTapParse(b []byte) (stream uint32, rate, channels int, data []byte, ok bool) {
	if len(b) < castTapHead || string(b[:4]) != castTapMagic {
		return 0, 0, 0, nil, false
	}
	stream = binary.LittleEndian.Uint32(b[4:])
	rate = int(binary.LittleEndian.Uint32(b[12:]))
	channels = int(binary.LittleEndian.Uint16(b[16:]))
	bits := binary.LittleEndian.Uint16(b[18:])
	data = b[castTapHead:]
	if bits != 16 || channels < 1 || channels > 8 || rate < 8000 || rate > 192000 || len(data)%(2*channels) != 0 {
		return 0, 0, 0, nil, false
	}
	return stream, rate, channels, data, true
}

// ── The tap (a process of its own) ────────────────────────────────────────

// handleCastAudioTapCommand runs the tap when this binary was started as one
// (main.go, before anything else).
func handleCastAudioTapCommand(args []string) (handled bool, exitCode int) {
	if len(args) < 2 || args[0] != castAudioTapFlag {
		return false, 0
	}
	castAudioTapRun(args[1], os.Stdin)
	return true, 0
}

// castAudioTapRun forwards a WAV stream on in to the receiver on port, and
// returns only when in ends. Nothing that goes wrong stops the reading.
func castAudioTapRun(portArg string, in io.Reader) {
	r := bufio.NewReaderSize(in, 64<<10)
	defer func() {
		_ = recover()
		// Whatever stopped the forwarding, the game's stream is drained to
		// its end: a closed pipe would kill the game.
		_, _ = io.Copy(io.Discard, r)
	}()
	port, err := strconv.Atoi(portArg)
	if err != nil || port <= 0 || port > 65535 {
		return
	}
	rate, channels, bits, ok := castWAVHeader(r)
	if !ok || bits != 16 || channels < 1 || channels > 8 || rate < 8000 || rate > 192000 {
		return
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return
	}
	defer conn.Close()
	to := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	frame := 2 * channels
	// 20 ms a datagram, in whole frames, within castTapMaxPayload.
	chunk := max(frame, min(castTapMaxPayload/frame*frame, rate/50*frame))
	buf := make([]byte, castTapHead+chunk)
	copy(buf, castTapMagic)
	binary.LittleEndian.PutUint32(buf[4:], uint32(os.Getpid()))
	binary.LittleEndian.PutUint32(buf[12:], uint32(rate))
	binary.LittleEndian.PutUint16(buf[16:], uint16(channels))
	binary.LittleEndian.PutUint16(buf[18:], 16)
	var seq uint32
	for {
		n, rerr := io.ReadFull(r, buf[castTapHead:])
		if n -= n % frame; n > 0 {
			seq++
			binary.LittleEndian.PutUint32(buf[8:], seq)
			// A full socket or a JukaHub that is not listening: dropped.
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Millisecond))
			_, _ = conn.WriteToUDP(buf[:castTapHead+n], to)
		}
		if rerr != nil {
			return
		}
	}
}

// castWAVHeader reads a WAV header up to the start of its samples: the rate,
// the channels and the bits a sample. alsa-lib's file plugin writes RIFF,
// WAVE, a 16-byte "fmt " chunk and a "data" chunk whose length a pipe never
// learns, so the lengths are not trusted, only the chunk walk.
func castWAVHeader(r *bufio.Reader) (rate, channels, bits int, ok bool) {
	head := make([]byte, 12)
	if _, err := io.ReadFull(r, head); err != nil || string(head[:4]) != "RIFF" || string(head[8:]) != "WAVE" {
		return 0, 0, 0, false
	}
	gotFmt := false
	for i := 0; i < 16; i++ {
		ch := make([]byte, 8)
		if _, err := io.ReadFull(r, ch); err != nil {
			return 0, 0, 0, false
		}
		size := int(binary.LittleEndian.Uint32(ch[4:]))
		switch string(ch[:4]) {
		case "fmt ":
			if size < 16 || size > 256 {
				return 0, 0, 0, false
			}
			f := make([]byte, size+size%2)
			if _, err := io.ReadFull(r, f); err != nil {
				return 0, 0, 0, false
			}
			tag := binary.LittleEndian.Uint16(f[0:])
			if tag != 1 && tag != 0xFFFE { // PCM, or WAVE_FORMAT_EXTENSIBLE
				return 0, 0, 0, false
			}
			channels = int(binary.LittleEndian.Uint16(f[2:]))
			rate = int(binary.LittleEndian.Uint32(f[4:]))
			bits = int(binary.LittleEndian.Uint16(f[14:]))
			gotFmt = true
		case "data":
			return rate, channels, bits, gotFmt
		default:
			if size < 0 || size > 1<<20 {
				return 0, 0, 0, false
			}
			if _, err := r.Discard(size + size%2); err != nil {
				return 0, 0, 0, false
			}
		}
	}
	return 0, 0, 0, false
}
