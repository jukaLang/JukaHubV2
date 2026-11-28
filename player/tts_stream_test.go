package main

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"testing"
	"time"
)

type ttsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ttsRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// ttsTestWAV is a 44-byte header for 24 kHz mono 16-bit PCM.
func ttsTestWAV(dataLen int) []byte {
	b := make([]byte, 44)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+dataLen))
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 24000)
	binary.LittleEndian.PutUint32(b[28:], 48000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(dataLen))
	return b
}

// The first PCM block must reach the feeder while Groq is still sending the
// rest: that is what the streaming path buys, and what reading the whole body
// before decoding cannot do.
func TestGroqWAVStreamsBeforeTheResponseEnds(t *testing.T) {
	data := make([]byte, 24000*2)
	for i := range data {
		data[i] = byte(i % 120)
	}
	pr, pw := io.Pipe()
	release := make(chan struct{})
	go func() {
		half := len(data) / 2
		_, _ = pw.Write(append(ttsTestWAV(len(data)), data[:half]...))
		<-release
		_, _ = pw.Write(data[half:])
		_ = pw.Close()
	}()

	old := ttsGroqHTTP
	ttsGroqHTTP = &http.Client{Transport: ttsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}, nil
	})}
	defer func() { ttsGroqHTTP = old }()

	got := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := ttsGroqStream(context.Background(), "key", "hannah", "hello", func(block []int16, rate int) bool {
			if len(block) > 0 {
				select {
				case got <- struct{}{}:
				default:
				}
			}
			return true
		})
		done <- err
	}()

	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("no PCM reached the feeder before the response ended")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
