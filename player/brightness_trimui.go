package main

// brightness_trimui.go — the TrimUI firmware's own brightness level, and the
// settings-file plumbing it shares with the volume (volume_trimui.go).
//
// The firmware's keymon keeps the backlight level as "brightness" in
// /mnt/UDISK/system.json, in eleven steps (0..10, the scale MinUI and spruceOS
// use on these handhelds as well), moves it with its brightness keys, and
// re-reads the file when it changes: it watches it with inotify, which is how
// the stock settings reach it. Two things follow for JukaHub:
//
//   - a level the user sets here is written back to the file, debounced like
//     the volume's "vol", so the firmware's next key press continues from it,
//     and a reload of the file cannot put the old level back — keymon
//     re-reading the file after one of JukaHub's volume writes would otherwise
//     have done exactly that;
//   - the level shown is the file's while no write of JukaHub's is on its way:
//     it is what the firmware's keys set, in the firmware's own steps.
//
// The key and its scale come from those ports and from how keymon treats the
// file, not from a device on this desk. A file whose "brightness" lies outside
// 0..10 keeps it on some other scale: it is then neither read nor written for
// the rest of the session, and the panel's own level is used instead. The
// probe logs what it found (trimuiLogBrightnessEvidence), so the first bug
// report from a device settles the question.
//
// The file is rewritten in place, on the same inode (trimuiSystemJSONRewrite):
// a watch keymon holds on the file itself would not see a file renamed over
// it, and would stop seeing the file at all.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// trimuiBrightnessKey and trimuiBrightnessSteps are the firmware's level:
	// "brightness" in system.json, 0..10.
	trimuiBrightnessKey   = "brightness"
	trimuiBrightnessSteps = 10
)

// trimuiBrightnessScaleBad records that system.json keeps "brightness" on a
// scale other than 0..10 (see the file comment).
var trimuiBrightnessScaleBad atomic.Bool

// trimuiSystemBrightnessLevel reads the firmware's level (0..10). ok is false
// without a file or a key, and on a scale other than 0..10.
func trimuiSystemBrightnessLevel() (int, bool) {
	if trimuiBrightnessScaleBad.Load() {
		return 0, false
	}
	data, err := os.ReadFile(trimuiSystemJSONPath)
	if err != nil {
		return 0, false
	}
	v, ok, inRange := trimuiSystemJSONIntFrom(data, trimuiBrightnessKey, trimuiBrightnessSteps)
	if !ok {
		return 0, false
	}
	if !inRange {
		trimuiNoteBrightnessScaleBad()
		return 0, false
	}
	return v, true
}

// trimuiNoteBrightnessScaleBad stops reading and writing the firmware's level
// for the session, and says so once.
func trimuiNoteBrightnessScaleBad() {
	if !trimuiBrightnessScaleBad.Swap(true) {
		logInfo("brightness", "system.json keeps %q outside 0..%d; the firmware's level is left alone",
			trimuiBrightnessKey, trimuiBrightnessSteps)
	}
}

// trimuiSystemBrightnessStep is the firmware's level (0..10) when it is the
// one to show: not while a level set here is still on its way to the file,
// and not after that write failed, when the file is stale.
func trimuiSystemBrightnessStep() (int, bool) {
	if pending, failed := trimuiBrightnessSyncState(); pending || failed {
		return 0, false
	}
	return trimuiSystemBrightnessLevel()
}

// trimuiLogBrightnessEvidence logs, once at the probe, what the firmware's
// file holds next to the panel's raw level: "brightness" (the backlight in the
// Miyoo-style layout these files follow), "lumination" (a colour setting
// there) and the raw level, which together show whether the key and its
// 0..10 scale are what this file assumes.
func trimuiLogBrightnessEvidence(raw int) {
	data, err := os.ReadFile(trimuiSystemJSONPath)
	if err != nil {
		logInfo("brightness", "no firmware settings file (%v); panel raw level %d", err, raw)
		return
	}
	var doc map[string]interface{}
	if json.Unmarshal(data, &doc) != nil {
		logInfo("brightness", "the firmware settings file does not parse; panel raw level %d", raw)
		return
	}
	logInfo("brightness", "firmware settings: brightness=%v lumination=%v; panel raw level %d of %d",
		doc[trimuiBrightnessKey], doc["lumination"], raw, dispBrightnessMax)
}

// trimuiSystemJSONModTime is the settings file's modification time, for the
// controller's once-a-second look (brightnessWatchFirmwareFile).
func trimuiSystemJSONModTime() (time.Time, bool) {
	info, err := os.Stat(trimuiSystemJSONPath)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// trimuiBrightnessStepFor turns a level into the firmware's tenths, rounded.
// The slider walks the firmware's own 10% steps there, so it is exact. Pure.
func trimuiBrightnessStepFor(pct int) int {
	return (brightnessClamp(pct)*trimuiBrightnessSteps + 50) / 100
}

// trimuiBrightnessSync debounces the system.json write the same way the
// volume's is (trimuiSystemSync): pending from the moment a level is asked
// for until it is on the card, seq telling a write in flight from a newer
// request, failed saying the last write did not reach the file.
var trimuiBrightnessSync struct {
	mu      sync.Mutex
	timer   *time.Timer
	level   int
	pending bool
	failed  bool
	seq     int
	retries int
}

func init() {
	// A level set just before quitting is still written: the exit path runs
	// the pending write instead of letting the timer die with the process.
	Cleanup = append(Cleanup, trimuiBrightnessFlush)
}

// trimuiBrightnessSyncLater schedules the firmware's "brightness" to follow a
// level the user set here, once the level has stopped moving.
func trimuiBrightnessSyncLater(pct int) {
	if trimuiBrightnessScaleBad.Load() {
		return
	}
	trimuiBrightnessSync.mu.Lock()
	defer trimuiBrightnessSync.mu.Unlock()
	trimuiBrightnessSync.level = trimuiBrightnessStepFor(pct)
	trimuiBrightnessSync.pending = true
	trimuiBrightnessSync.seq++
	trimuiBrightnessSync.retries = 0
	if trimuiBrightnessSync.timer != nil {
		trimuiBrightnessSync.timer.Stop()
	}
	trimuiBrightnessSync.timer = time.AfterFunc(trimuiSystemSyncDelay, trimuiBrightnessFlush)
}

// trimuiBrightnessSyncState reports whether a level asked for is not on the
// card yet, and whether the last write failed.
func trimuiBrightnessSyncState() (pending, failed bool) {
	trimuiBrightnessSync.mu.Lock()
	defer trimuiBrightnessSync.mu.Unlock()
	return trimuiBrightnessSync.pending, trimuiBrightnessSync.failed
}

// trimuiBrightnessFlush writes the pending level now: the debounce timer's job
// and the exit path's. A write the firmware raced is tried again after the
// delay, a couple of times, unless a newer level replaced it.
func trimuiBrightnessFlush() {
	trimuiBrightnessSync.mu.Lock()
	if trimuiBrightnessSync.timer != nil {
		trimuiBrightnessSync.timer.Stop()
		trimuiBrightnessSync.timer = nil
	}
	if !trimuiBrightnessSync.pending {
		trimuiBrightnessSync.mu.Unlock()
		return
	}
	level, seq := trimuiBrightnessSync.level, trimuiBrightnessSync.seq
	trimuiBrightnessSync.mu.Unlock()

	err := trimuiSystemJSONRewrite(func(data []byte) ([]byte, bool) {
		if _, ok, inRange := trimuiSystemJSONIntFrom(data, trimuiBrightnessKey, trimuiBrightnessSteps); ok && !inRange {
			trimuiNoteBrightnessScaleBad()
			return data, false
		}
		return trimuiSystemJSONWithInt(data, trimuiBrightnessKey, level, trimuiBrightnessSteps)
	})

	trimuiBrightnessSync.mu.Lock()
	defer trimuiBrightnessSync.mu.Unlock()
	if trimuiBrightnessSync.seq != seq {
		return // a newer level is on its way and will be written in turn
	}
	if errors.Is(err, errTrimuiSystemJSONChanged) && trimuiBrightnessSync.retries < trimuiSystemSyncRetries {
		trimuiBrightnessSync.retries++
		trimuiBrightnessSync.timer = time.AfterFunc(trimuiSystemSyncDelay, trimuiBrightnessFlush)
		return
	}
	trimuiBrightnessSync.pending = false
	trimuiBrightnessSync.failed = err != nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		logDebug("brightness", "system.json not updated: %v", err)
	}
}

// ── The settings file, shared with the volume ──────────────────────────────

// trimuiSystemJSONIntFrom reads the top-level number key out of the settings
// document, rounded and clamped to 0..max. ok is false when the document is
// not an object or holds no such number; inRange is false when the number lay
// outside 0..max before it was clamped. Pure.
func trimuiSystemJSONIntFrom(data []byte, key string, max int) (v int, ok, inRange bool) {
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, false, false
	}
	f, isNum := doc[key].(float64)
	if !isNum || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false, false
	}
	v = int(math.Round(f))
	inRange = v >= 0 && v <= max
	if v < 0 {
		v = 0
	}
	if v > max {
		v = max
	}
	return v, true, inRange
}

// trimuiSystemJSONWithInt returns the settings document with the number key
// set to v, changing nothing else: not the key order, not the spacing, not
// another number's spelling. Re-encoding the whole document would have
// rewritten the firmware's file in Go's style. It reports false (and returns
// data as it was) when the document is not an object with exactly one numeric
// key, or when v is already the value. Pure.
func trimuiSystemJSONWithInt(data []byte, key string, v, max int) ([]byte, bool) {
	current, ok, _ := trimuiSystemJSONIntFrom(data, key, max)
	if !ok {
		return data, false
	}
	member := regexp.MustCompile(`("` + regexp.QuoteMeta(key) + `"\s*:\s*)(-?[0-9]+(?:\.[0-9]+)?)`)
	matches := member.FindAllSubmatchIndex(data, -1)
	if len(matches) != 1 {
		return data, false
	}
	m := matches[0]
	if current == v && string(data[m[4]:m[5]]) == strconv.Itoa(v) {
		return data, false
	}
	out := make([]byte, 0, len(data)+2)
	out = append(out, data[:m[4]]...)
	out = append(out, strconv.Itoa(v)...)
	out = append(out, data[m[5]:]...)
	return out, true
}

// trimuiSystemJSONMu keeps the two writers of the firmware's file — the
// volume's sync and the brightness's — from interleaving: each must see the
// other's change before it writes.
var trimuiSystemJSONMu sync.Mutex

// trimuiSystemJSONRewrite applies edit to system.json, in place: the same file
// (the same inode) is overwritten from its first byte, never truncated and
// never replaced. keymon watches the file with inotify, and a watch held on
// the file itself is lost when another file is renamed over it — the
// temp-and-rename write this replaced could leave the firmware deaf to the
// file, its own later changes included, until a reboot. To keep the write a
// single overwrite that never leaves a stale tail behind, a document that got
// shorter is padded with spaces at its end (JSON allows trailing whitespace),
// and one that got longer first gives up padding a previous write left there.
// edit reporting false means there is nothing to write.
//
// A symlinked settings file is written where it really lives, and its mode
// stays. A file that does not parse (the firmware may be half-way through
// writing it) and a file that changed since it was read are not written:
// errTrimuiSystemJSONChanged tells the caller to try again a moment later.
func trimuiSystemJSONRewrite(edit func([]byte) ([]byte, bool)) error {
	trimuiSystemJSONMu.Lock()
	defer trimuiSystemJSONMu.Unlock()
	path := trimuiSystemJSONPath
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		f.Close()
		return err
	}
	if !json.Valid(data) {
		f.Close()
		return errTrimuiSystemJSONChanged
	}
	out, changed := edit(data)
	if !changed {
		return f.Close()
	}
	out = trimuiFitLength(out, len(data))
	// The file at the path must still be this one, unchanged: a firmware that
	// replaced it (a new inode) or wrote it since it was read wins.
	if now, err := os.Stat(path); err != nil || !os.SameFile(info, now) || now.Size() != info.Size() || !now.ModTime().Equal(info.ModTime()) {
		f.Close()
		return errTrimuiSystemJSONChanged
	}
	if _, err := f.WriteAt(out, 0); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// trimuiFitLength brings a rewritten document as close to n bytes as its own
// padding allows: spaces are added at its end when it is shorter, and trailing
// spaces (an earlier write's padding) are dropped when it is longer. Pure.
func trimuiFitLength(out []byte, n int) []byte {
	if len(out) < n {
		return append(out, bytes.Repeat([]byte{' '}, n-len(out))...)
	}
	for len(out) > n && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return out
}
