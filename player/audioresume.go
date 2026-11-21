package main

// audioresume.go — long-form audio support for the music player: remember
// where an audiobook chapter or podcast episode was left, and remember the
// listening speed. Both live in one small JSON file next to the other app data
// so they survive updates.
//
// The rules and persistence are SDL-free so they can be tested headless.

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	audioResumeFileName = "audioresume.json"
	// audioResumeMaxEntries bounds the file: one entry per book/episode is
	// tiny, but a music folder played through once would otherwise add a
	// position for every song forever.
	audioResumeMaxEntries = 200
	// audioResumeMinSeconds is how far in a track has to be before it is
	// worth resuming. Shorter than this and starting over is what people
	// expect.
	audioResumeMinSeconds = 45
	// audioResumeTailSeconds treats the last seconds as "finished".
	audioResumeTailSeconds = 15
	// audioResumeLongFormSeconds separates audiobooks and podcasts from
	// music: songs restart from the top, long files resume.
	audioResumeLongFormSeconds = 8 * 60
)

// audioSpeedSteps are the speeds the player cycles through.
var audioSpeedSteps = []float64{0.75, 1, 1.25, 1.5, 1.75, 2}

type audioResumeEntry struct {
	Position float64 `json:"position"`
	Duration float64 `json:"duration,omitempty"`
	Updated  string  `json:"updated,omitempty"`
	// Chapter names the chapter Position is in, for a file with chapters
	// inside (audiobook_chapters.go).
	Chapter string `json:"chapter,omitempty"`
}

type audioPrefsData struct {
	// Speed is the listening speed for music, podcasts and every audiobook
	// without its own; BookSpeeds holds the books' own (key = the shelf's book
	// key, musicShelfBookKey). A store from before per-book speeds has only
	// Speed, which simply stays the default.
	Speed      float64                     `json:"speed,omitempty"`
	Entries    map[string]audioResumeEntry `json:"entries"`
	Books      map[string]audioBookEntry   `json:"books,omitempty"`
	BookSpeeds map[string]float64          `json:"bookSpeeds,omitempty"`
}

type audioBookEntry struct {
	Path     string `json:"path,omitempty"`
	Finished bool   `json:"finished,omitempty"`
}

var audioPrefs struct {
	mu         sync.Mutex
	loaded     bool
	speed      float64
	entries    map[string]audioResumeEntry
	books      map[string]audioBookEntry
	bookSpeeds map[string]float64
}

// audioBookSpeedMax bounds the per-book speeds kept, like the positions.
const audioBookSpeedMax = audioResumeMaxEntries

var audioBookRevision atomic.Uint64

// audioResumeFilePath is a variable so tests can point the store at a temp
// file instead of the app data folder.
var audioResumeFilePath = defaultAudioResumeFilePath

func defaultAudioResumeFilePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, audioResumeFileName)
}

// audioResumeKey normalises a path so the same file opened through different
// relative paths shares one entry.
func audioResumeKey(path string) string {
	clean := filepath.Clean(path)
	if abs, err := filepath.Abs(clean); err == nil {
		clean = abs
	}
	return clean
}

func audioPrefsLoadLocked() {
	if audioPrefs.loaded {
		return
	}
	audioPrefs.loaded = true
	audioPrefs.speed = 1
	audioPrefs.entries = make(map[string]audioResumeEntry)
	audioPrefs.books = make(map[string]audioBookEntry)
	audioPrefs.bookSpeeds = make(map[string]float64)

	// The atomic save leaves the previous version beside the live file, so a
	// truncated store resumes from the saved positions instead of silently
	// starting every book over.
	var stored audioPrefsData
	if _, err := readJSONWithBackup(audioResumeFilePath(), &stored); err != nil {
		return
	}
	if stored.Speed >= 0.5 && stored.Speed <= 2 {
		audioPrefs.speed = stored.Speed
	}
	for path, entry := range stored.Entries {
		if path == "" || entry.Position <= 0 {
			continue
		}
		audioPrefs.entries[path] = entry
	}
	for key, entry := range stored.Books {
		if key != "" {
			audioPrefs.books[key] = entry
		}
	}
	for key, speed := range stored.BookSpeeds {
		if key != "" && speed >= 0.5 && speed <= 2 {
			audioPrefs.bookSpeeds[key] = speed
		}
	}
}

// audioPrefsSaveLocked writes the store through the same temp+fsync+rename path
// as the config. A rename alone kept a crash mid-write from truncating the
// file but left nothing to recover from when the card itself dropped a write;
// the previous version is now kept as .bak and the loader falls back to it.
func audioPrefsSaveLocked() error {
	data, err := json.MarshalIndent(audioPrefsData{
		Speed:      audioPrefs.speed,
		Entries:    audioPrefs.entries,
		Books:      audioPrefs.books,
		BookSpeeds: audioPrefs.bookSpeeds,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(audioResumeFilePath(), data, 0o644); err != nil {
		return err
	}
	return nil
}

func audioBookProgress(key string) audioBookEntry {
	key = audioResumeKey(key)
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	return audioPrefs.books[key]
}

func audioBookRemember(key, path string, finished bool) {
	if strings.TrimSpace(key) == "" {
		return
	}
	key = audioResumeKey(key)
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	next := audioBookEntry{Path: path, Finished: finished}
	if audioPrefs.books[key] == next {
		return
	}
	audioPrefs.books[key] = next
	_ = audioPrefsSaveLocked()
	audioBookRevision.Add(1)
}

// audioStartPosition decides where playback starts for a saved position.
// Returning 0 means "from the beginning": too early to matter, already at the
// end, or a short music track where resuming would be more surprising than
// starting over.
func audioStartPosition(saved, duration float64) float64 {
	if saved < audioResumeMinSeconds {
		return 0
	}
	if duration > 0 {
		if saved >= duration-audioResumeTailSeconds {
			return 0
		}
		if duration < audioResumeLongFormSeconds {
			return 0
		}
	}
	return saved
}

// audioResumePosition returns the saved position for a file (0 when none).
func audioResumePosition(path string) float64 {
	key := audioResumeKey(path)
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	return audioPrefs.entries[key].Position
}

// audioResumeChapter names the chapter a file's saved place is in, "" when it
// has none (no saved place, or no chapters inside the file).
func audioResumeChapter(path string) string {
	key := audioResumeKey(path)
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	return audioPrefs.entries[key].Chapter
}

// audioRememberPosition stores the current position, pruning the oldest
// entries so the file stays bounded.
func audioRememberPosition(path string, position, duration float64) {
	if strings.TrimSpace(path) == "" || position <= 0 {
		return
	}
	key := audioResumeKey(path)
	chapter := audioChapterTitleAt(path, position) // before audioPrefs.mu: its own lock
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	audioPrefs.entries[key] = audioResumeEntry{
		Position: position,
		Duration: duration,
		Updated:  time.Now().UTC().Format(time.RFC3339),
		Chapter:  chapter,
	}
	pruneAudioPrefsLocked()
	_ = audioPrefsSaveLocked()
}

// audioForgetPosition removes a finished book/episode so it starts over.
func audioForgetPosition(path string) {
	key := audioResumeKey(path)
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	if _, ok := audioPrefs.entries[key]; !ok {
		return
	}
	delete(audioPrefs.entries, key)
	_ = audioPrefsSaveLocked()
}

func pruneAudioPrefsLocked() {
	if len(audioPrefs.entries) <= audioResumeMaxEntries {
		return
	}
	type pair struct {
		key     string
		updated string
	}
	list := make([]pair, 0, len(audioPrefs.entries))
	for key, entry := range audioPrefs.entries {
		list = append(list, pair{key, entry.Updated})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].updated < list[j].updated })
	for _, item := range list[:len(list)-audioResumeMaxEntries] {
		delete(audioPrefs.entries, item.key)
	}
}

// audioSavedSpeed returns the persisted listening speed (1 when unset).
func audioSavedSpeed() float64 {
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	return audioPrefs.speed
}

// audioSaveSpeed persists the listening speed.
func audioSaveSpeed(speed float64) {
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	if audioPrefs.speed == speed {
		return
	}
	audioPrefs.speed = speed
	_ = audioPrefsSaveLocked()
}

// audioSpeedFor is the listening speed for a playlist: an audiobook's own
// when it has one, else the global speed (music, podcasts, a book never
// changed). bookKey "" is not a book.
func audioSpeedFor(bookKey string) float64 {
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	if strings.TrimSpace(bookKey) != "" {
		if s, ok := audioPrefs.bookSpeeds[audioResumeKey(bookKey)]; ok {
			return s
		}
	}
	return audioPrefs.speed
}

// audioSaveSpeedFor persists a speed change: the book's own for an audiobook,
// the global one otherwise (what a speed change always did before).
func audioSaveSpeedFor(bookKey string, speed float64) {
	if strings.TrimSpace(bookKey) == "" {
		audioSaveSpeed(speed)
		return
	}
	key := audioResumeKey(bookKey)
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	if old, ok := audioPrefs.bookSpeeds[key]; ok && old == speed {
		return
	}
	if _, ok := audioPrefs.bookSpeeds[key]; !ok && len(audioPrefs.bookSpeeds) >= audioBookSpeedMax {
		// Bounded like the positions: drop a book that has no progress any
		// more (or, failing that, any one) to make room.
		drop := ""
		for k := range audioPrefs.bookSpeeds {
			if drop == "" {
				drop = k
			}
			if _, live := audioPrefs.books[k]; !live {
				drop = k
				break
			}
		}
		delete(audioPrefs.bookSpeeds, drop)
	}
	audioPrefs.bookSpeeds[key] = speed
	_ = audioPrefsSaveLocked()
}

// musicListeningSpeed is the speed a track of the playlist with bookKey
// plays at (musicPlayTrackAt): 1 on the bit-perfect path, which applies no
// tempo, else audioSpeedFor. Called with music.mu held — the order musicSceneEnter
// already uses (music.mu, then audioPrefs.mu).
func musicListeningSpeed(bookKey string) float64 {
	if musicEQ.bitPerfect {
		return 1
	}
	return audioClampSpeed(audioSpeedFor(bookKey))
}

// audioClampSpeed keeps a speed inside the range atempo accepts and the UI
// offers.
func audioClampSpeed(speed float64) float64 {
	if speed < audioSpeedSteps[0] {
		return audioSpeedSteps[0]
	}
	if speed > audioSpeedSteps[len(audioSpeedSteps)-1] {
		return audioSpeedSteps[len(audioSpeedSteps)-1]
	}
	return speed
}

// musicFFmpegArgs builds the decoder command line. The filter graph carries the
// 31-band PEQ and the atempo filter that keeps the pitch when the speed changes;
// both are dropped on the bit-perfect path, which leaves the samples alone. path
// is what ffmpeg opens (ffmpegReadableInput's answer).
func musicFFmpegArgs(path string, startSec, speed float64, rate int32) []string {
	if rate <= 0 {
		rate = 44100
	}
	speed = audioClampSpeed(speed)
	args := []string{"-nostdin", "-v", "error"}
	if startSec > 0.05 {
		args = append(args, "-ss", strconv.FormatFloat(startSec, 'f', 3, 64))
	}
	// An http(s) input (a podcast, a radio stream, or the relay's loopback
	// address for one) gets the video decoders' read timeout and reconnect
	// (ffmpegStreamInputArgs, videorecover.go): a stream that stopped sending
	// used to leave the player silent, with no error, for good.
	args = append(args, ffmpegStreamInputArgs(path, "")...)
	args = append(args,
		"-i", path,
		"-vn", "-f", "s16le", "-acodec", "pcm_s16le", "-ac", "2",
		"-ar", strconv.Itoa(int(rate)))
	if chain := musicPEQCurrentChain(speed); chain != "" {
		args = append(args, "-af", chain)
	}
	return append(args, "pipe:1")
}

// musicMediaElapsed converts decoded PCM bytes into media seconds. atempo=s
// plays s seconds of the track in each second of output, so the seconds of
// PCM (bytes over the sample rate and 4 bytes a stereo frame) are multiplied by
// the speed. It used to divide: at 2× the position crawled at a quarter of the
// real rate, and every speed change or resume restarted the track that far
// back.
func musicMediaElapsed(pcmStart float64, pcmBytes int64, rate int32, speed float64) float64 {
	if rate <= 0 {
		rate = 44100
	}
	if speed <= 0 {
		speed = 1
	}
	return pcmStart + float64(pcmBytes)*speed/(float64(rate)*4)
}
