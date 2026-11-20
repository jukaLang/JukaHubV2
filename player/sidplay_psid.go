package main

// sidplay_psid.go — Commodore 64 SID tunes, played without ffmpeg's help.
//
// ffmpeg has no SID decoder, so JukaHub carries a small one of its own. A
// PSID file is a piece of C64 machine code with three addresses: where it
// loads, a routine that sets up song n (called once with n-1 in A) and a
// routine that plays one tick of music (called 50 times a second for a PAL
// tune, or at the rate the tune sets a CIA timer to). sidplay_cpu.go runs that
// code on an emulated 6510 whose writes to $D400-$D41C reach the emulated SID
// chip in sidplay_chip.go; the chip renders 44.1 kHz samples between ticks.
//
// The samples go to the music player's ffmpeg on its stdin as raw s16le
// stereo (chiptune_decode.go), so the equaliser, the speed control and the
// resampling to the device's rate apply exactly as for any other song, and
// the player's decode loop, gapless step and screen-off mode stay as they are.
//
// RSID tunes need a whole C64 (CIA and VIC interrupts, the KERNAL ROM, the
// tune's own main loop) and are refused with a plain message; so are the
// old Compute's Sidplayer (MUS) files. Second and third SID chips (PSID v3/v4)
// are not played: the tune's first chip is.
//
// Cost: the play routine is a few thousand 6510 cycles per tick and the chip
// a few dozen operations per sample — an estimated 1-2 % of one A133P core,
// not measured yet (CHIPTUNE-HW-1) — which is what lets it play with the
// screen off.

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

const (
	// sidMaxFile bounds what is read of a .sid: 64 KB of C64 memory plus the
	// header is the most a tune can hold.
	sidMaxFile = 0x10000 + 0x7C + 2
	sidRate    = 44100
	sidPAL     = 985248.0
	sidNTSC    = 1022727.0
	// sidInitBudget and sidPlayBudget bound one call in 6510 cycles, so a tune
	// that never returns (an RSID in disguise) cannot hang the decoder.
	sidInitBudget = 2000000
	sidPlayBudget = 60000
)

var (
	errSIDNeedsC64 = errors.New("This SID tune (RSID) needs a whole C64 to play; JukaHub plays PSID tunes")
	errSIDMUS      = errors.New("Compute's Sidplayer (MUS) tunes are not supported")
	errSIDHeader   = errors.New("not a SID tune")
	errSIDStale    = errors.New("the SID seek was overtaken by a newer request")
)

// sidTune is one parsed .sid file.
type sidTune struct {
	Version          int
	Load, Init, Play uint16
	Songs, Start     int
	Speed            uint32
	Flags            uint16
	Title, Author    string
	Released         string
	Data             []byte
	NTSC, Model8580  bool
	SecondSID, Third bool
	RSID, MUS        bool
	// ExtraSID are the second and third chips' base addresses (0 = none).
	ExtraSID [2]uint16
}

// sidParse reads a PSID/RSID header (v1-v4) and the C64 data after it.
func sidParse(b []byte) (sidTune, error) {
	var t sidTune
	if len(b) < 0x76 {
		return t, errSIDHeader
	}
	magic := string(b[:4])
	if magic != "PSID" && magic != "RSID" {
		return t, errSIDHeader
	}
	be16 := func(off int) uint16 { return binary.BigEndian.Uint16(b[off:]) }
	t.RSID = magic == "RSID"
	t.Version = int(be16(0x04))
	dataOff := int(be16(0x06))
	t.Load, t.Init, t.Play = be16(0x08), be16(0x0A), be16(0x0C)
	t.Songs, t.Start = chiptuneSongs(int(be16(0x0E)), int(be16(0x10)))
	t.Speed = binary.BigEndian.Uint32(b[0x12:])
	t.Title = chiptuneField(b, 0x16, 32)
	t.Author = chiptuneField(b, 0x36, 32)
	t.Released = chiptuneField(b, 0x56, 32)
	if t.Version >= 2 && len(b) >= 0x7C {
		t.Flags = be16(0x76)
		t.MUS = t.Flags&0x01 != 0
		t.NTSC = (t.Flags>>2)&3 == 2
		t.Model8580 = (t.Flags>>4)&3 == 2
		t.SecondSID = t.Version >= 3 && b[0x7A] != 0
		t.Third = t.Version >= 4 && b[0x7B] != 0
		// "$Dxx0" is stored as xx; one at or below $D410 would be the first
		// chip itself (an invalid header), so it is ignored.
		if t.SecondSID && b[0x7A] >= 0x42 {
			t.ExtraSID[0] = 0xD000 | uint16(b[0x7A])<<4
		}
		if t.Third && b[0x7B] >= 0x42 {
			t.ExtraSID[1] = 0xD000 | uint16(b[0x7B])<<4
		}
	}
	if dataOff < 0x76 || dataOff > len(b) {
		return t, errSIDHeader
	}
	data := b[dataOff:]
	if t.Load == 0 {
		if len(data) < 2 {
			return t, errSIDHeader
		}
		t.Load = binary.LittleEndian.Uint16(data)
		data = data[2:]
	}
	if int(t.Load)+len(data) > 0x10000 {
		data = data[:0x10000-int(t.Load)]
	}
	if len(data) == 0 {
		return t, errSIDHeader
	}
	t.Data = data
	if t.Init == 0 {
		t.Init = t.Load
	}
	return t, nil
}

// meta is the tune's tags as the music player shows them.
func (t sidTune) meta() chiptuneMeta {
	return chiptuneMeta{Format: "SID", System: "C64", Songs: t.Songs, Start: t.Start,
		Game: t.Title, Artist: t.Author, Copyright: t.Released}
}

// playable says why the tune cannot be played here, or nil.
func (t sidTune) playable() error {
	switch {
	case t.RSID:
		return errSIDNeedsC64
	case t.MUS:
		return errSIDMUS
	}
	return nil
}

// sidLoadFile reads and parses a .sid file.
func sidLoadFile(path string) (sidTune, error) {
	f, err := os.Open(path)
	if err != nil {
		return sidTune{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, sidMaxFile))
	if err != nil {
		return sidTune{}, err
	}
	return sidParse(b)
}

// ── The stream ───────────────────────────────────────────────────────────────

// sidStream renders one song as 44.1 kHz s16le stereo, for ffmpeg's stdin.
// It ends (io.EOF) after the song's length; the fade is ffmpeg's afade.
type sidStream struct {
	m         *sidMachine
	tune      sidTune
	song      int
	cia       bool    // the song is timed by a CIA timer, not the 50/60 Hz frame
	clock     float64 // the C64's CPU clock
	perTick   float64 // samples between two play calls
	untilTick float64
	left      int64 // stereo frames still to produce
	irqVector bool  // play address 0: the tune's own interrupt handler is called
}

// newSIDStream sets the machine up, runs the song's init routine and skips
// startSec seconds (seeking runs the tune without making samples, which is
// several times faster than real time). seconds is how long it plays after
// that; model is "6581", "8580" or "" for what the tune asks for. stale (may be
// nil) is asked about once a second of tune while seeking: a long seek that a
// skip or a newer seek made pointless stops instead of running to its end.
func newSIDStream(t sidTune, song int, startSec, seconds float64, model string, stale func() bool) (*sidStream, error) {
	if err := t.playable(); err != nil {
		return nil, err
	}
	if song < 1 || song > t.Songs {
		song = t.Start
	}
	s := &sidStream{tune: t, song: song, clock: sidPAL}
	if t.NTSC {
		s.clock = sidNTSC
	}
	bit := song - 1
	if bit > 31 {
		bit = 31
	}
	s.cia = t.Speed&(1<<uint(bit)) != 0
	m := &sidMachine{extraSID: t.ExtraSID}
	m.chip.reset(s.clock, sidRate, model == "8580" || (model == "" && t.Model8580))
	m.loadKernalStubs()
	copy(m.ram[t.Load:], t.Data)
	// The bank the PSID convention starts a tune in: BASIC, KERNAL and I/O,
	// less whatever the tune's own code covers.
	switch {
	case t.Init < 0xA000:
		m.ram[1] = 0x37
	case t.Init < 0xD000:
		m.ram[1] = 0x36
	case t.Init >= 0xE000:
		m.ram[1] = 0x35
	default:
		m.ram[1] = 0x34
	}
	m.ram[0] = 0x2F
	m.ciaLatch = 0
	m.call(t.Init, byte(song-1), sidInitBudget)
	s.m = m
	s.irqVector = t.Play == 0
	s.perTick = s.tickSamples()
	if startSec > 0 {
		skip := int64(startSec * sidRate)
		check := skip - sidRate
		for skip > 0 {
			if skip <= check {
				if stale != nil && stale() {
					return nil, errSIDStale
				}
				check = skip - sidRate
			}
			if s.untilTick <= 0 {
				s.tick()
			}
			n := int64(s.untilTick)
			if n < 1 {
				n = 1
			}
			if n > skip {
				n = skip
			}
			for i := int64(0); i < n; i++ {
				m.chip.sample(false)
			}
			s.untilTick -= float64(n)
			skip -= n
		}
	}
	if seconds < 0.1 {
		seconds = 0.1
	}
	s.left = int64(seconds * sidRate)
	return s, nil
}

// tickSamples is the time between play calls in samples: the PAL/NTSC frame,
// or the CIA timer's period the tune set (the KERNAL's own 60 Hz when it set
// none).
func (s *sidStream) tickSamples() float64 {
	hz := 50.125
	if s.tune.NTSC {
		hz = 59.826
	}
	if s.cia {
		latch := float64(s.m.ciaLatch)
		if latch == 0 {
			latch = 0x4025
			if s.tune.NTSC {
				latch = 0x4295
			}
		}
		hz = s.clock / (latch + 1)
	}
	if hz < 10 {
		hz = 10
	}
	if hz > 1000 {
		hz = 1000
	}
	return sidRate / hz
}

// tick runs one play call and schedules the next.
func (s *sidStream) tick() {
	if s.irqVector {
		s.m.interrupt(sidPlayBudget)
	} else {
		s.m.call(s.tune.Play, 0, sidPlayBudget)
	}
	s.perTick = s.tickSamples()
	s.untilTick += s.perTick
}

// Read fills p with whole stereo frames.
func (s *sidStream) Read(p []byte) (int, error) {
	if s.left <= 0 {
		return 0, io.EOF
	}
	frames := len(p) / 4
	if int64(frames) > s.left {
		frames = int(s.left)
	}
	if frames == 0 {
		return 0, io.ErrShortBuffer
	}
	for i := 0; i < frames; i++ {
		if s.untilTick <= 0 {
			s.tick()
		}
		v := uint16(s.m.chip.sample(true))
		o := i * 4
		p[o], p[o+1], p[o+2], p[o+3] = byte(v), byte(v>>8), byte(v), byte(v>>8)
		s.untilTick--
	}
	s.left -= int64(frames)
	return frames * 4, nil
}
