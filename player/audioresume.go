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
}

type audioPrefsData struct {
	Speed   float64                     `json:"speed,omitempty"`
	Entries map[string]audioResumeEntry `json:"entries"`
}

var audioPrefs struct {
	mu      sync.Mutex
	loaded  bool
	speed   float64
	entries map[string]audioResumeEntry
}

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
}

// audioPrefsSaveLocked writes the store through the same temp+fsync+rename path
// as the config. A rename alone kept a crash mid-write from truncating the
// file but left nothing to recover from when the card itself dropped a write;
// the previous version is now kept as .bak and the loader falls back to it.
func audioPrefsSaveLocked() error {
	data, err := json.MarshalIndent(audioPrefsData{
		Speed:   audioPrefs.speed,
		Entries: audioPrefs.entries,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(audioResumeFilePath(), data, 0o644); err != nil {
		return err
	}
	return nil
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

// audioRememberPosition stores the current position, pruning the oldest
// entries so the file stays bounded.
func audioRememberPosition(path string, position, duration float64) {
	if strings.TrimSpace(path) == "" || position <= 0 {
		return
	}
	key := audioResumeKey(path)
	audioPrefs.mu.Lock()
	defer audioPrefs.mu.Unlock()
	audioPrefsLoadLocked()
	audioPrefs.entries[key] = audioResumeEntry{
		Position: position,
		Duration: duration,
		Updated:  time.Now().UTC().Format(time.RFC3339),
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
// both are dropped on the bit-perfect path, which leaves the samples alone.
func musicFFmpegArgs(path string, startSec, speed float64, rate int32) []string {
	if rate <= 0 {
		rate = 44100
	}
	speed = audioClampSpeed(speed)
	args := []string{"-nostdin", "-v", "error"}
	if startSec > 0.05 {
		args = append(args, "-ss", strconv.FormatFloat(startSec, 'f', 3, 64))
	}
	args = append(args,
		"-i", path,
		"-vn", "-f", "s16le", "-acodec", "pcm_s16le", "-ac", "2",
		"-ar", strconv.Itoa(int(rate)))
	if chain := musicPEQCurrentChain(speed); chain != "" {
		args = append(args, "-af", chain)
	}
	return append(args, "pipe:1")
}

// musicMediaElapsed converts decoded PCM bytes into media seconds. With atempo
// the decoder produces speed times as much audio per media second, so the byte
// count has to be divided by the speed as well as the sample rate.
func musicMediaElapsed(pcmStart float64, pcmBytes int64, rate int32, speed float64) float64 {
	if rate <= 0 {
		rate = 44100
	}
	if speed <= 0 {
		speed = 1
	}
	return pcmStart + float64(pcmBytes)/(float64(rate)*4*speed)
}
