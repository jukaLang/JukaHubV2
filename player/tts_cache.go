package main

// tts_cache.go — Juka's short, everyday lines kept as sound.
//
// "Going Home", "Taking a screenshot", "Music paused", the launch greeting:
// most of what Juka says is a handful of short lines said over and over. Each
// is made once — by Groq's voice or the device's own — and kept as a small
// WAV in the app's data folder (voice-cache/), named by a hash of the voice
// and the words. The next time the same voice says the same words they play
// from that file: no request, no Piper, no model to load, almost no battery.
//
// Only short lines are kept (ttsCacheable): a long answer is rarely said
// twice. The folder keeps the most recently heard ttsCacheMaxFiles files
// within ttsCacheMaxBytes; playing a file marks it as heard. While the
// device's voice sits loaded after a conversation it also makes the common
// lines that are not kept yet (ttsCacheSeedLines, tts_local_other.go), one at
// a time, so even their first time plays at once.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// A line is kept when it is at most ttsCacheMaxChars bytes and
	// ttsCacheMaxWords words, and its sound at most ttsCacheMaxSeconds long.
	ttsCacheMaxChars   = 72
	ttsCacheMaxWords   = 12
	ttsCacheMaxSeconds = 8
	// The folder's bounds: the least recently heard files go first.
	ttsCacheMaxFiles = 160
	ttsCacheMaxBytes = 12 << 20
)

// ttsCacheSeedLines are lines Juka says often enough to make ahead of time:
// the voice commands' answers, exactly as voicecmd_exec.go and
// voicesettings.go say them.
var ttsCacheSeedLines = []string{
	"Going Home",
	"Going back",
	"Taking a screenshot",
	"Music paused",
	"Next song",
	"Previous song",
	"Nothing is playing",
	"Opening Quick Settings",
	"Opening Music",
	"Sound off",
	"Turning the screen off",
	"Didn't catch a command — say what to open, like “open YouTube”",
}

// ttsCacheState serialises the folder's writes and remembers the seed lines
// already tried this run (made, or failed: not tried again). Any goroutine,
// under mu.
var ttsCacheState struct {
	mu    sync.Mutex
	tried map[string]bool
}

// ttsCacheDir is the folder, "" when the platform has no data folder.
func ttsCacheDir() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "voice-cache")
}

// ttsCacheable reports whether a chunk is short enough to keep. Pure.
func ttsCacheable(text string) bool {
	t := strings.TrimSpace(text)
	return t != "" && len(t) <= ttsCacheMaxChars && len(strings.Fields(t)) <= ttsCacheMaxWords
}

// ttsCacheGroqID names Groq's voice for the cache.
func ttsCacheGroqID(voice string) string { return "groq/" + ttsGroqModel + "/" + voice }

// ttsCacheLocalID names the device's voice for the cache: its engine and
// voice model, by name, size and date, so a model replaced under the same
// name is not answered with the old one's sound. Only Windows' synthesiser
// picks a voice by gender; every other engine sounds the same whatever
// Groq's voice would have been.
func ttsCacheLocalID(v ttsLocalVoice, gender string) string {
	id := "local/" + v.kind + "/" + filepath.Base(v.path) + "/" + filepath.Base(v.model)
	if v.model != "" {
		if info, err := os.Stat(v.model); err == nil {
			id += "/" + strconv.FormatInt(info.Size(), 10) + "/" + strconv.FormatInt(info.ModTime().Unix(), 10)
		}
	}
	if v.kind == "sapi" || v.kind == "phonon" {
		id += "/" + gender // both pick a voice by it
	}
	return id
}

// cacheID is the session's voice for the cache: Groq's while it is asked,
// the device's otherwise.
func (s *ttsSession) cacheID(groq bool) string {
	if groq {
		return ttsCacheGroqID(s.voice)
	}
	return ttsCacheLocalID(s.local, s.gender)
}

// ttsCachePath is where voiceID's sound of text is kept, "" with no folder.
func ttsCachePath(voiceID, text string) string {
	dir := ttsCacheDir()
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(voiceID + "\x00" + strings.TrimSpace(text)))
	return filepath.Join(dir, hex.EncodeToString(sum[:12])+".wav")
}

// ttsCacheHas reports whether the sound is kept.
func ttsCacheHas(voiceID, text string) bool {
	path := ttsCachePath(voiceID, text)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 44
}

// ttsCacheLoad is the kept sound of text in voiceID's voice. A file that
// does not read as a whole WAV is removed.
func ttsCacheLoad(voiceID, text string) ([]int16, int, bool) {
	path := ttsCachePath(voiceID, text)
	if path == "" {
		return nil, 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false
	}
	samples, rate, err := ttsDecodeWAV(data, false)
	if err != nil || len(samples) == 0 || rate <= 0 || !ttsWAVComplete(data) {
		_ = os.Remove(path)
		return nil, 0, false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now) // heard: kept longest
	return samples, rate, true
}

// ttsCacheStore keeps samples as voiceID's sound of text. It copies them at
// once and writes on its own goroutine, so the caller may change them. A clip
// that is empty or too long is not kept.
func ttsCacheStore(voiceID, text string, samples []int16, rate int) {
	if !ttsCacheable(text) || len(samples) == 0 || rate <= 0 || len(samples) > rate*ttsCacheMaxSeconds {
		return
	}
	path := ttsCachePath(voiceID, text)
	if path == "" {
		return
	}
	pcm := make([]byte, len(samples)*2)
	for i, v := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	wav := vkEncodeWAV(pcm, rate)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("tts", "keeping a line's sound failed: %v", r)
			}
		}()
		ttsCacheState.mu.Lock()
		defer ttsCacheState.mu.Unlock()
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		// Written whole, then named: a half-written file is never read.
		tmp := path + ".part"
		if err := os.WriteFile(tmp, wav, 0o644); err != nil {
			_ = os.Remove(tmp)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return
		}
		ttsCachePruneLocked(dir)
	}()
}

// ttsCachePruneLocked keeps the folder within its bounds, the most recently
// heard first, and removes what an interrupted write left. ttsCacheState.mu
// is held.
func ttsCachePruneLocked(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type kept struct {
		path string
		size int64
		mod  time.Time
	}
	var files []kept
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		switch {
		case strings.HasSuffix(e.Name(), ".part"):
			if time.Since(info.ModTime()) > time.Minute {
				_ = os.Remove(path)
			}
		case strings.HasSuffix(e.Name(), ".wav"):
			files = append(files, kept{path, info.Size(), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	var total int64
	for i, f := range files {
		total += f.size
		if i >= ttsCacheMaxFiles || total > ttsCacheMaxBytes {
			_ = os.Remove(f.path)
		}
	}
}

// ttsCacheSeedNext is a common line voiceID has not made yet, as the line's
// one chunk, and marks it tried. false: every one is kept or was tried.
func ttsCacheSeedNext(voiceID string) (string, bool) {
	ttsCacheState.mu.Lock()
	defer ttsCacheState.mu.Unlock()
	if ttsCacheState.tried == nil {
		ttsCacheState.tried = map[string]bool{}
	}
	for _, line := range ttsCacheSeedLines {
		chunks := ttsPrepare(line)
		if len(chunks) != 1 || !ttsCacheable(chunks[0]) {
			continue
		}
		key := voiceID + "\x00" + chunks[0]
		if ttsCacheState.tried[key] {
			continue
		}
		ttsCacheState.tried[key] = true
		if ttsCacheHas(voiceID, chunks[0]) {
			continue
		}
		return chunks[0], true
	}
	return "", false
}
