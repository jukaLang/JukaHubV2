package main

// chatvoice_opus.go — the Opus codec for Voice rooms (chatvoice_mumble.go).
//
// Mumble servers and apps speak Opus. github.com/thesyncim/gopus is a pure-Go
// Opus encoder and decoder (no cgo), so the TrimUI and Windows builds need no
// extra library. Everything that touches the codec's API is in this file.
//
// Audio stays 16 kHz mono, the rate the microphone and the call mixer already
// use (chatvoicechannel.go): Opus accepts it directly, and a 20 ms frame is
// 320 samples. A Mumble app may send 10–60 ms per packet; the decoder hands
// back whatever length the packet holds.

import (
	"encoding/binary"
	"math"

	"github.com/thesyncim/gopus"
)

const (
	chatOpusRate    = micRate
	chatOpusSamples = chatVoiceFrameBytes / 2 // one 20 ms frame
	// chatOpusMaxOut is 120 ms at 48 kHz, the longest packet Opus allows at
	// its highest rate, so the decoder never runs short of room.
	chatOpusMaxOut    = 5760
	chatOpusMaxPacket = 1500
)

type chatOpusEncoder struct {
	enc *gopus.Encoder
	in  []float32
	out []byte
}

func newChatOpusEncoder() (*chatOpusEncoder, error) {
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: chatOpusRate, Channels: 1, Application: gopus.ApplicationVoIP})
	if err != nil {
		return nil, err
	}
	return &chatOpusEncoder{enc: enc, in: make([]float32, chatOpusSamples), out: make([]byte, chatOpusMaxPacket)}, nil
}

// encode turns one 20 ms frame of 16-bit little-endian PCM into an Opus
// packet. It returns nil while the encoder is still filling its look-ahead.
// The packet is only valid until the next call.
func (e *chatOpusEncoder) encode(pcm []byte) ([]byte, error) {
	if len(pcm) < chatOpusSamples*2 {
		return nil, nil
	}
	for i := range e.in {
		e.in[i] = float32(int16(binary.LittleEndian.Uint16(pcm[i*2:]))) / 32768
	}
	n, err := e.enc.Encode(e.in, e.out)
	if err != nil || n <= 0 {
		return nil, err
	}
	return e.out[:n], nil
}

type chatOpusDecoder struct {
	dec *gopus.Decoder
	out []float32
}

func newChatOpusDecoder() (*chatOpusDecoder, error) {
	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(chatOpusRate, 1))
	if err != nil {
		return nil, err
	}
	return &chatOpusDecoder{dec: dec, out: make([]float32, chatOpusMaxOut)}, nil
}

// decode appends one packet's audio to dst as 16-bit little-endian PCM.
func (d *chatOpusDecoder) decode(packet, dst []byte) ([]byte, error) {
	n, err := d.dec.Decode(packet, d.out)
	if err != nil {
		return dst, err
	}
	if n > len(d.out) {
		n = len(d.out)
	}
	for _, v := range d.out[:n] {
		switch {
		case math.IsNaN(float64(v)):
			v = 0
		case v > 1:
			v = 1
		case v < -1:
			v = -1
		}
		dst = binary.LittleEndian.AppendUint16(dst, uint16(int16(v*32767)))
	}
	return dst, nil
}
