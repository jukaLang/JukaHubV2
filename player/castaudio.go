package main

// castaudio.go — Screen Share's sound: what the handheld plays, in the
// viewer's browser.
//
// Two kinds of source feed one mix:
//
//   - JukaHub's own outputs (audioroute.go): every buffer a menu sound, the
//     music player, a video or Juka's voice queues is copied here as it is
//     queued (castAudioOwnQueue), with the output's rate and channel count,
//     and played out at the pace a sound card would take it. Pausing,
//     clearing and closing an output do the same here.
//   - A game's own sound (castaudio_alsa.go): ALSA's "default" of a child
//     started while sharing is on goes through a file plugin that pipes a copy
//     to `JukaHub --share-audio-tap`, which sends it here as datagrams on the
//     loopback address (castaudio_tap.go). Each stream is kept to a quarter of
//     a second, so a stall never piles up latency.
//
// The mix runs only while a viewer listens (castAudioLive): 48 kHz stereo, one
// 20 ms block at a time on the wall clock, coded as IMA ADPCM — a quarter of
// the bytes of 16-bit PCM, about 50 kB/s, and trivial to decode in a page's
// script — and handed to every listener's queue (companion_castplay.go sends
// them over the /play socket). Each block carries its decoder state, so a
// viewer can start on any block. Blocks of silence are not sent: the page
// knows a gap from its clock.
//
// The viewer hears what is played at full scale: the handheld's own volume
// keys move the speaker (and the firmware's "Game Volume"), which come after
// both taps, and the TV has a volume of its own.

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// castAudioRate and castAudioBlockFrames are the mix: 48 kHz, 20 ms blocks.
	castAudioRate        = 48000
	castAudioBlockFrames = castAudioRate / 50
	castAudioBlockTime   = 20 * time.Millisecond
	// castAudioTapKeep is the most of a game's stream kept unplayed: a tap
	// that stalls and then delivers a burst is cut back to castAudioTapTrim,
	// so the viewer stays close to the handheld.
	castAudioTapKeep = 250 * time.Millisecond
	castAudioTapTrim = 80 * time.Millisecond
	// castAudioTapPrime is what a game's stream gathers before it plays (and
	// again after it ran dry): ALSA delivers it a period at a time, and
	// playing each period the moment it lands would stutter.
	castAudioTapPrime = 40 * time.Millisecond
	// castAudioTapGone is how long a game's stream may stay silent before it
	// is forgotten (its program closed the sound, or ended).
	castAudioTapGone = 2 * time.Second
	// castAudioOwnKeep bounds one of JukaHub's outputs' unplayed backlog, the
	// way outFIFOMax bounds the headset mixer's.
	castAudioOwnKeep = 5 * time.Second
	// castAudioListenerQueue is how many blocks wait for one viewer's socket
	// (about 1.3 s); a viewer further behind skips blocks.
	castAudioListenerQueue = 64
	// castAudioMsgAudio marks an audio block on the /play socket.
	castAudioMsgAudio = 'A'
	// castAudioHeader is a block's fixed part; each channel adds 4 bytes of
	// decoder state.
	castAudioHeader = 16
)

// castAudioLive is true while someone listens: sharing on, sound on, and at
// least one viewer asked for it. The output hooks check it first, so a
// handheld nobody listens to pays one atomic load per queued buffer.
var castAudioLive atomic.Bool

// castAudioSource is one stream in the mix: stereo 16-bit samples at its own
// rate, pcm[head:] not played yet, phase the resampler's position between
// two of its frames.
type castAudioSource struct {
	rate   int
	pcm    []int16
	head   int
	phase  float64
	paused bool
	// tap marks a game's stream; primed whether it gathered castAudioTapPrime
	// since it last ran dry; last when it was last fed.
	tap    bool
	primed bool
	last   time.Time
}

// castAudioListener is one viewer's queue of coded blocks.
type castAudioListener struct {
	ch      chan []byte
	dropped atomic.Int32
}

var castAudio struct {
	mu        sync.Mutex
	sources   map[uint64]*castAudioSource
	listeners map[*castAudioListener]struct{}
	stop      chan struct{}
	enc       [2]castADPCM
	seq       uint32
	acc       []int32
	// lastSound is when a block last carried sound, lastGame when a game's
	// stream last delivered; the panel and /status read them.
	lastSound time.Time
	lastGame  time.Time
}

// Source keys: JukaHub's outputs by their virtual device id, a game's
// streams by the tap's stream id with this bit set.
const castAudioTapKeyBit = uint64(1) << 40

// ── Who listens ───────────────────────────────────────────────────────────

// castAudioListen adds a listener and starts the mix when it is the first.
func castAudioListen() *castAudioListener {
	l := &castAudioListener{ch: make(chan []byte, castAudioListenerQueue)}
	castAudio.mu.Lock()
	if castAudio.listeners == nil {
		castAudio.listeners = make(map[*castAudioListener]struct{})
	}
	castAudio.listeners[l] = struct{}{}
	castAudioUpdateLocked()
	castAudio.mu.Unlock()
	return l
}

// castAudioUnlisten removes a listener; the mix stops with the last one.
func castAudioUnlisten(l *castAudioListener) {
	if l == nil {
		return
	}
	castAudio.mu.Lock()
	delete(castAudio.listeners, l)
	castAudioUpdateLocked()
	castAudio.mu.Unlock()
}

// castAudioRefresh re-reads whether the mix should run: sharing or the Sound
// setting changed.
func castAudioRefresh() {
	castAudio.mu.Lock()
	castAudioUpdateLocked()
	castAudio.mu.Unlock()
}

// castAudioUpdateLocked starts or stops the mix. Stopping forgets every
// stream, so nothing stale plays when someone listens again.
func castAudioUpdateLocked() {
	want := len(castAudio.listeners) > 0 && castIsEnabled() && castLive.sound.Load()
	castAudioLive.Store(want)
	switch {
	case want && castAudio.stop == nil:
		castAudio.stop = make(chan struct{})
		castAudio.enc = [2]castADPCM{}
		go castAudioMixLoop(castAudio.stop)
	case !want && castAudio.stop != nil:
		close(castAudio.stop)
		castAudio.stop = nil
		castAudio.sources = nil
	}
}

// castAudioInfo is what the panel and /status say about the sound: how many
// listen, whether sound reached them in the last second, and whether a
// game's stream arrived in the last two.
func castAudioInfo() (listeners int, flowing, game bool) {
	castAudio.mu.Lock()
	defer castAudio.mu.Unlock()
	now := time.Now()
	return len(castAudio.listeners), now.Sub(castAudio.lastSound) < time.Second,
		now.Sub(castAudio.lastGame) < castAudioTapGone
}

// ── JukaHub's own outputs (audioroute.go) ─────────────────────────────────

// castAudioOwnQueue copies one buffer queued on JukaHub's output id: signed
// 16-bit native samples (every virtual output's format), mono or stereo, at
// rate. outAudio.mu is held: this only copies.
func castAudioOwnQueue(id sdl.AudioDeviceID, rate, channels int, paused bool, data []byte) {
	if !castAudioLive.Load() || rate <= 0 || (channels != 1 && channels != 2) {
		return
	}
	castAudio.mu.Lock()
	defer castAudio.mu.Unlock()
	s := castAudioSourceLocked(uint64(id), rate, false)
	if s == nil {
		return
	}
	s.paused = paused
	s.append(data, channels)
	if limit := 2 * int(float64(rate)*castAudioOwnKeep.Seconds()); len(s.pcm)-s.head > limit {
		s.head = len(s.pcm) - limit
		s.compact()
	}
}

// castAudioOwnPause follows outPause.
func castAudioOwnPause(id sdl.AudioDeviceID, paused bool) {
	if !castAudioLive.Load() {
		return
	}
	castAudio.mu.Lock()
	if s := castAudio.sources[uint64(id)]; s != nil {
		s.paused = paused
	}
	castAudio.mu.Unlock()
}

// castAudioOwnClear follows outClear and outClose: what was queued will not
// play.
func castAudioOwnClear(id sdl.AudioDeviceID) {
	if !castAudioLive.Load() {
		return
	}
	castAudio.mu.Lock()
	delete(castAudio.sources, uint64(id))
	castAudio.mu.Unlock()
}

// ── A game's streams (castaudio_tap.go) ───────────────────────────────────

// castAudioTapFeed adds one datagram of a game's stream: 16-bit little-endian
// samples, channels of them a frame (only the first two are heard).
func castAudioTapFeed(stream uint32, rate, channels int, data []byte) {
	if !castAudioLive.Load() || rate < 8000 || rate > 192000 || channels < 1 || channels > 8 {
		return
	}
	now := time.Now()
	castAudio.mu.Lock()
	defer castAudio.mu.Unlock()
	s := castAudioSourceLocked(castAudioTapKeyBit|uint64(stream), rate, true)
	if s == nil {
		return
	}
	s.last = now
	castAudio.lastGame = now
	s.append(data, channels)
	if keep := 2 * int(float64(rate)*castAudioTapKeep.Seconds()); len(s.pcm)-s.head > keep {
		s.head = len(s.pcm) - 2*int(float64(rate)*castAudioTapTrim.Seconds())
		s.phase = 0
		s.compact()
	}
}

// castAudioSourceLocked finds or makes the source under key. A source whose
// rate changed starts again.
func castAudioSourceLocked(key uint64, rate int, tap bool) *castAudioSource {
	if castAudio.stop == nil {
		return nil
	}
	if castAudio.sources == nil {
		castAudio.sources = make(map[uint64]*castAudioSource)
	}
	s := castAudio.sources[key]
	if s == nil || s.rate != rate {
		s = &castAudioSource{rate: rate, tap: tap}
		castAudio.sources[key] = s
	}
	return s
}

// append adds little-endian 16-bit frames of channels samples as stereo.
func (s *castAudioSource) append(data []byte, channels int) {
	frame := 2 * channels
	n := len(data) / frame
	for i := 0; i < n; i++ {
		off := i * frame
		l := int16(binary.LittleEndian.Uint16(data[off:]))
		r := l
		if channels > 1 {
			r = int16(binary.LittleEndian.Uint16(data[off+2:]))
		}
		s.pcm = append(s.pcm, l, r)
	}
}

// compact drops the played part once it is the larger half.
func (s *castAudioSource) compact() {
	if s.head <= 0 {
		return
	}
	if s.head >= len(s.pcm) {
		s.pcm, s.head = s.pcm[:0], 0
		return
	}
	if s.head > 16<<10 && s.head > len(s.pcm)/2 {
		n := copy(s.pcm, s.pcm[s.head:])
		s.pcm, s.head = s.pcm[:n], 0
	}
}

// frames is how many stereo frames wait.
func (s *castAudioSource) frames() int { return (len(s.pcm) - s.head) / 2 }

// mixInto adds up to frames output frames at castAudioRate to acc (stereo),
// resampling linearly from the source's rate, and returns how many it added.
// As in outDev.mixInto the last frame is played on its own rather than held
// back, so a stream always drains.
func (s *castAudioSource) mixInto(acc []int32, frames int) int {
	n := s.frames()
	if n == 0 || s.rate <= 0 {
		return 0
	}
	step := float64(s.rate) / castAudioRate
	pos := s.phase
	pcm := s.pcm[s.head:]
	i := 0
	for ; i < frames; i++ {
		i0 := int(pos)
		if i0 >= n {
			break
		}
		i1 := i0 + 1
		if i1 >= n {
			i1 = i0
		}
		frac := pos - float64(i0)
		l0, r0 := float64(pcm[2*i0]), float64(pcm[2*i0+1])
		l1, r1 := float64(pcm[2*i1]), float64(pcm[2*i1+1])
		acc[2*i] += int32(l0 + (l1-l0)*frac)
		acc[2*i+1] += int32(r0 + (r1-r0)*frac)
		pos += step
	}
	used := int(pos)
	if used >= n {
		s.head += 2 * n
		s.phase = 0
	} else {
		s.head += 2 * used
		s.phase = pos - float64(used)
	}
	s.compact()
	return i
}

// ── The mix ───────────────────────────────────────────────────────────────

// castAudioMixLoop makes a block every 20 ms of wall clock until stop closes.
// A loop that fell behind (the handheld was busy) skips ahead instead of
// catching up in a burst the viewer would hear as a jump anyway.
func castAudioMixLoop(stop <-chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("cast", "screen share sound stopped: %v", r)
		}
	}()
	tick := time.NewTicker(castAudioBlockTime)
	defer tick.Stop()
	start := time.Now()
	var made int64
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		due := int64(time.Since(start).Seconds()*castAudioRate) - made
		if due < castAudioBlockFrames {
			continue
		}
		if due > 5*castAudioBlockFrames {
			made += due - castAudioBlockFrames
		}
		made += castAudioBlockFrames
		castAudioMixOnce(stop)
	}
}

// castAudioMixOnce mixes, codes and hands out one block.
func castAudioMixOnce(stop <-chan struct{}) {
	castAudio.mu.Lock()
	if castAudio.stop != stop {
		castAudio.mu.Unlock()
		return
	}
	frames := castAudioBlockFrames
	if cap(castAudio.acc) < 2*frames {
		castAudio.acc = make([]int32, 2*frames)
	}
	acc := castAudio.acc[:2*frames]
	for i := range acc {
		acc[i] = 0
	}
	now := time.Now()
	mixed := 0
	for key, s := range castAudio.sources {
		if s.tap {
			if now.Sub(s.last) > castAudioTapGone && s.frames() == 0 {
				delete(castAudio.sources, key)
				continue
			}
			if !s.primed {
				if s.frames() < int(float64(s.rate)*castAudioTapPrime.Seconds()) {
					continue
				}
				s.primed = true
			}
		}
		if s.paused {
			continue
		}
		if n := s.mixInto(acc, frames); n > mixed {
			mixed = n
		}
		if s.tap && s.frames() == 0 {
			s.primed = false
		}
	}
	if mixed == 0 {
		castAudio.mu.Unlock()
		return
	}
	castAudio.lastSound = now
	castAudio.seq++
	block := castAudioEncodeBlock(castAudio.seq, acc, frames, &castAudio.enc)
	listeners := make([]*castAudioListener, 0, len(castAudio.listeners))
	for l := range castAudio.listeners {
		listeners = append(listeners, l)
	}
	castAudio.mu.Unlock()
	for _, l := range listeners {
		select {
		case l.ch <- block:
		default:
			l.dropped.Add(1)
		}
	}
}

// ── IMA ADPCM ─────────────────────────────────────────────────────────────

// castADPCMSteps and castADPCMIndex are the IMA ADPCM tables (the page's
// decoder in companion_cast.go has the same two).
var castADPCMSteps = [89]int32{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130, 143, 157, 173, 190, 209, 230,
	253, 279, 307, 337, 371, 408, 449, 494, 544, 598, 658, 724, 796, 876, 963,
	1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066, 2272, 2499, 2749, 3024, 3327,
	3660, 4026, 4428, 4871, 5358, 5894, 6484, 7132, 7845, 8630, 9493, 10442,
	11487, 12635, 13899, 15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794,
	32767,
}

var castADPCMIndex = [16]int32{-1, -1, -1, -1, 2, 4, 6, 8, -1, -1, -1, -1, 2, 4, 6, 8}

// castADPCM is one channel's coder: the predicted sample and the step index,
// exactly what its decoder tracks.
type castADPCM struct {
	pred  int32
	index int32
}

// code turns one sample into its 4-bit code and moves the state the way the
// decoder will.
func (c *castADPCM) code(sample int32) byte {
	step := castADPCMSteps[c.index]
	diff := sample - c.pred
	var nib int32
	if diff < 0 {
		nib = 8
		diff = -diff
	}
	vp := step >> 3
	if diff >= step {
		nib |= 4
		diff -= step
		vp += step
	}
	if diff >= step>>1 {
		nib |= 2
		diff -= step >> 1
		vp += step >> 1
	}
	if diff >= step>>2 {
		nib |= 1
		vp += step >> 2
	}
	if nib&8 != 0 {
		c.pred -= vp
	} else {
		c.pred += vp
	}
	c.pred = max(-32768, min(32767, c.pred))
	c.index = max(0, min(88, c.index+castADPCMIndex[nib]))
	return byte(nib)
}

// castAudioEncodeBlock codes frames stereo frames of acc (sums, clipped to
// 16 bits here) as one /play message:
//
//	0      'A'
//	1      version (1)
//	2      channels (2)
//	3      0
//	4..7   rate, little-endian
//	8..11  block number
//	12..13 frames
//	14..15 0
//	then per channel: the predicted sample (int16) and step index (uint8)
//	before the block, and a 0 byte; then per channel its codes, two a byte,
//	the earlier frame in the low four bits.
func castAudioEncodeBlock(seq uint32, acc []int32, frames int, enc *[2]castADPCM) []byte {
	const channels = 2
	per := (frames + 1) / 2
	out := make([]byte, castAudioHeader+4*channels+channels*per)
	out[0], out[1], out[2] = castAudioMsgAudio, 1, channels
	binary.LittleEndian.PutUint32(out[4:], castAudioRate)
	binary.LittleEndian.PutUint32(out[8:], seq)
	binary.LittleEndian.PutUint16(out[12:], uint16(frames))
	for ch := 0; ch < channels; ch++ {
		st := out[castAudioHeader+4*ch:]
		binary.LittleEndian.PutUint16(st, uint16(int16(enc[ch].pred)))
		st[2] = byte(enc[ch].index)
		data := out[castAudioHeader+4*channels+ch*per:]
		for i := 0; i < frames; i++ {
			v := acc[2*i+ch]
			v = max(-32768, min(32767, v))
			nib := enc[ch].code(v)
			if i&1 == 0 {
				data[i/2] = nib
			} else {
				data[i/2] |= nib << 4
			}
		}
	}
	return out
}
