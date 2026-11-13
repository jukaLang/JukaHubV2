package main

// tts_live.go — Juka starts talking while the answer is still being written.
//
// The orb and the game assistant stream Juka's answer from Groq word by word.
// Before, the voice waited for the whole answer, then fetched its first chunk
// of sound: a two-sentence reply meant a second or two of silence after the
// text was already on screen. Now the line starts as soon as the first
// sentence is complete (ttsLive.Update), and every later sentence joins the
// same line as it arrives — the producer waits for it (ttsSource.at) instead
// of ending. A sentence counts as complete once the next one has begun, so
// "It costs $3." is never spoken before ".50" arrives.
//
// A ttsSource is a line's chunks. A fixed line (every jukaSpeak) has them all
// up front; a live one gets sentences pushed while the producer pulls, and
// packs whatever sentences are waiting into one chunk when the producer asks
// for the next — the first chunk short (ttsFirstChunk) so it is quick to make,
// later ones as full as ttsChunkMax allows, so a reply costs few requests.
// The same ttsSpokenMax cap as a fixed line applies.

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// ttsLiveIdleMax ends a live line whose answer stopped arriving without ever
// being finished (a request that died silently): the orb must not wait on it
// for ever.
const ttsLiveIdleMax = 20 * time.Second

// ttsSource is the text of one line, chunk by chunk. Any goroutine.
type ttsSource struct {
	mu      sync.Mutex
	chunks  []string // handed to the producer, in order
	pending []string // sentences (each ≤ ttsChunkMax bytes) not packed yet
	done    bool     // nothing more will come
	wake    chan struct{}
}

// ttsFixedSource is a line whose chunks are all known.
func ttsFixedSource(chunks []string) *ttsSource {
	return &ttsSource{chunks: chunks, done: true, wake: make(chan struct{})}
}

// ttsOpenSource is a line still being written.
func ttsOpenSource() *ttsSource {
	return &ttsSource{wake: make(chan struct{})}
}

// push adds sentences, and ends the line when done.
func (s *ttsSource) push(sentences []string, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	for _, t := range sentences {
		s.pending = append(s.pending, ttsSplitLong(t, ttsChunkMax)...)
	}
	if done {
		s.done = true
	}
	close(s.wake)
	s.wake = make(chan struct{})
}

// at is chunk i (asked for in order from 0), waiting while the line is still
// being written. false: the line has no chunk i, or ctx ended.
func (s *ttsSource) at(ctx context.Context, i int) (string, bool) {
	idle := time.NewTimer(ttsLiveIdleMax)
	defer idle.Stop()
	for {
		s.mu.Lock()
		if i < len(s.chunks) {
			c := s.chunks[i]
			s.mu.Unlock()
			return c, true
		}
		if i == len(s.chunks) && len(s.pending) > 0 {
			c := s.packLocked()
			s.chunks = append(s.chunks, c)
			s.mu.Unlock()
			return c, true
		}
		if s.done {
			s.mu.Unlock()
			return "", false
		}
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return "", false
		case <-idle.C:
			logWarn("tts", "Juka's answer stopped arriving; ending the line")
			s.mu.Lock()
			s.done = true
			s.mu.Unlock()
		}
	}
}

// packLocked joins waiting sentences into one chunk, as ttsChunks does: the
// first chunk of a line short, the others up to ttsChunkMax bytes. s.mu held,
// s.pending not empty.
func (s *ttsSource) packLocked() string {
	cur := s.pending[0]
	n := 1
	for n < len(s.pending) {
		next := s.pending[n]
		if len(cur)+1+len(next) > ttsChunkMax || (len(s.chunks) == 0 && len(cur) >= ttsFirstChunk) {
			break
		}
		cur += " " + next
		n++
	}
	s.pending = s.pending[n:]
	return cur
}

// ttsLive speaks one answer while it streams in. Update and Finish may be
// called from the request goroutine; Stop from the main thread.
type ttsLive struct {
	origin ttsOrigin
	kind   ttsKind

	mu      sync.Mutex
	src     *ttsSource
	used    int // sentences of the answer already pushed
	total   int // characters pushed (for ttsSpokenMax)
	closed  bool
	stopped atomic.Bool
}

// ttsNewLive prepares a live line; nothing is spoken until a sentence is
// complete.
func ttsNewLive(origin ttsOrigin, kind ttsKind) *ttsLive {
	return &ttsLive{origin: origin, kind: kind}
}

// Update takes the whole answer so far.
func (l *ttsLive) Update(text string) { l.feed(text, false) }

// Finish takes the whole answer: the rest is spoken and the line ends. A line
// that never started starts now, as jukaSpeak would.
func (l *ttsLive) Finish(text string) { l.feed(text, true) }

// Stop drops the answer: nothing more is pushed and a line not begun never
// starts. What is already speaking is the caller's to silence
// (jukaSpeakStop), and a newer line has replaced it anyway.
func (l *ttsLive) Stop() {
	if l == nil {
		return
	}
	l.stopped.Store(true)
	l.mu.Lock()
	if l.src != nil && !l.closed {
		l.src.push(nil, true)
	}
	l.closed = true
	l.mu.Unlock()
}

func (l *ttsLive) feed(text string, final bool) {
	if l == nil || l.stopped.Load() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	sents := ttsSentences(ttsSpeakable(text))
	if !final && len(sents) > 0 {
		// The last sentence may still grow.
		sents = sents[:len(sents)-1]
	}
	var add []string
	capped := false
	if len(sents) > l.used {
		add, capped = ttsLiveCap(sents[l.used:], &l.total)
		l.used = len(sents)
	}
	done := final || capped
	if done {
		l.closed = true
	}
	if l.src == nil {
		if len(add) == 0 {
			return
		}
		l.src = ttsOpenSource()
		l.src.push(add, done)
		ttsSpeakSource(l.origin, l.kind, l.src)
		return
	}
	if len(add) > 0 || done {
		l.src.push(add, done)
	}
}

// ttsLiveCap keeps the fresh sentences that fit what is left of ttsSpokenMax
// (total counts what was already pushed), and says the rest is on the screen
// when one does not fit (capped). Pure apart from total.
func ttsLiveCap(fresh []string, total *int) (out []string, capped bool) {
	for _, s := range fresh {
		n := utf8.RuneCountInString(s)
		if *total+n <= ttsSpokenMax {
			out = append(out, s)
			*total += n + 1
			continue
		}
		if *total == 0 {
			for _, piece := range ttsSplitLong(s, ttsChunkMax) {
				n := utf8.RuneCountInString(piece)
				if *total+n > ttsSpokenMax {
					break
				}
				out = append(out, piece)
				*total += n + 1
			}
		}
		return append(out, ttsRestOnScreen), true
	}
	return out, false
}
