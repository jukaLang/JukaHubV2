package main

// racfgscope.go — JukaHub's retroarch.cfg changes last as long as JukaHub's
// own launch, not for ever.
//
// Before a RetroArch game starts from the library, JukaHub sets a few keys in
// retroarch.cfg: the two save-state switches behind Continue
// (savestate_resume.go), the folder RetroArch's record toggle writes to
// (recordings.go) and the OSD preset the user picked (osdpreset.go). Those
// edits used to stay. RetroArch reads one global config, so after a single
// JukaHub launch every game started from CrossMix's or the stock firmware's
// own menu also loaded its old auto state on boot and wrote one on exit — a
// game that "jumps" back to a stale state, fighting the firmware's own resume
// and save-on-MENU features — which is what a CrossMix tester saw as RetroArch
// "being weird".
//
// Now every launch-time edit goes through raScopeApply, which first writes a
// small journal in JukaHub's data folder (the key, whether the user had it,
// their value, and the value JukaHub wrote), then edits the file. When the
// last screen-taking child is gone (gamesuspend.go) the user's values go back.
// RetroArch may rewrite the whole file on exit (config_save_on_exit), so only
// JukaHub's keys are touched, and only where they still hold the value JukaHub
// wrote: a change the user made in RetroArch's menu during the game wins. A
// crash or a power cut mid-game leaves the journal behind, and the next start
// restores from it. A launch whose emulator never started is caught by a
// failsafe check a little later.
//
// Earlier versions left their edits in place with no journal. Once, at the
// first start of this version, the keys they managed are compared with the
// `retroarch.cfg.jukahub-backup` they kept (the file as it was before
// JukaHub's first edit): a key that still holds JukaHub's value goes back to
// the backup's.
//
// Which file: TrimUI's RetroArch reads retroarch.cfg in its own folder (the
// stock and CrossMix launchers start ra64.trimui there with HOME set to it); a
// .retroarch/retroarch.cfg beside it may exist too. JukaHub cannot ask the
// binary, so a launch edits every config of that one RetroArch install
// (raScopeTargets), which is harmless now that each edit is undone.
//
// One launch, one sync (2026-11-23, "improve emulator launching"): a library
// launch runs a dozen or more of these edits in a row — the save-state
// switches, the OSD, the log, the picture, the controls, the speed level… —
// and each used to sync the journal, its folder and the config to the card on
// its own: some thirty fsyncs on a FAT card before the emulator could even
// start. A launch now opens a batch (raScopeBatchBegin, from faststart.go):
// every edit is still written to its file at once, so a later hook that reads
// the config sees what an earlier one set, but nothing is synced until the
// batch ends — the journal first, then each config once — just before the game
// starts (screenChildStart calls raScopeBatchFlush). No restore runs while a
// batch is open: the launch's own exit (or the failsafe the batch end arms)
// restores everything, so a late restore from the last game can no longer undo
// the new launch's first edits halfway through its preparation.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// raScopeJournalName is the journal in JukaHub's data folder.
	raScopeJournalName = "retroarch-launch-scope.json"
	// raScopeMigratedName marks that the one-time repair of older versions'
	// permanent edits has run.
	raScopeMigratedName = "retroarch-scope-migrated"
	// raScopeJournalMax bounds the journal read; a real one is a few lines.
	raScopeJournalMax = 64 << 10
	// raScopeFailsafeDelay is when a launch is checked for an emulator that
	// never started (the start failed after the edit).
	raScopeFailsafeDelay = 20 * time.Second
	// raScopeWaitPoll and raScopeWaitMax bound the wait for a RetroArch that
	// outlived its launcher's process group before the values go back.
	raScopeWaitPoll = 2 * time.Second
	raScopeWaitMax  = 8 * time.Hour
	// raScopeBatchMax ends a launch batch nobody ended (a launch path that
	// returned without its deferred end): its syncs run then.
	raScopeBatchMax = 20 * time.Second
)

// raScopeEdit is one key JukaHub changed in one config for a launch.
type raScopeEdit struct {
	Path   string `json:"path"`
	Key    string `json:"key"`
	Had    bool   `json:"had"`
	Before string `json:"before,omitempty"`
	Wrote  string `json:"wrote"`
}

// raScopeJournal is the file the edits are recorded in.
type raScopeJournal struct {
	Edits []raScopeEdit `json:"edits"`
}

// raScopeValue is one key and the value a launch wants it to have. A
// RaiseOnly value is a floor, not a setting: it is written only where the
// user's value is absent or zero, so a nonzero choice of theirs (a 60-second
// SRAM autosave, say) is never lowered or replaced (raScopeSkips).
type raScopeValue struct {
	Key       string
	Value     string
	RaiseOnly bool
}

// raScopeSkips reports whether v leaves cfg alone: a RaiseOnly value where the
// file already holds a nonzero number (or any non-numeric value, which is
// the user's business). An empty value (`key = ""`) is 0 to RetroArch, so it
// is not a choice either. Pure.
func raScopeSkips(cfg string, v raScopeValue) bool {
	if !v.RaiseOnly {
		return false
	}
	before, had := raConfigGet(cfg, v.Key)
	before = strings.TrimSpace(before)
	if !had || before == "" {
		return false
	}
	n, err := strconv.ParseFloat(before, 64)
	return err != nil || n != 0
}

// raScope serialises every journal and config change: the launch path runs on
// the main thread, the restore on a reaper goroutine.
var raScope struct {
	mu    sync.Mutex
	timer *time.Timer
}

// raScopeBatch is the launch batch in force, under raScope.mu: depth counts
// the begins not ended yet, gen tells one batch from the next (for the stale
// timer), journal reports a journal written without a sync, paths the configs
// written without one (each once), edits how many values changed.
var raScopeBatch struct {
	depth   int
	gen     uint64
	began   time.Time
	journal bool
	paths   []string
	edits   int
}

// raScopeBatchLive mirrors depth > 0, so the start's flush (screenChildStart,
// every launch) costs one atomic load when there is nothing to flush.
var raScopeBatchLive atomic.Bool

// raScopeBatchOff is JUKAHUB_LAUNCH_BATCH=0: every edit syncs on its own, as
// before the batch existed (a device check, or a card that misbehaves).
var raScopeBatchOff = os.Getenv("JUKAHUB_LAUNCH_BATCH") == "0"

// Test seams: the journal's place, the failsafe delay (0 turns it off), the
// two "is anything still running" questions and the configs the one-time
// repair looks at.
var (
	raScopeJournalPath = func() string {
		dir, err := P().DataDir()
		if err != nil || dir == "" {
			return ""
		}
		return filepath.Join(dir, raScopeJournalName)
	}
	raScopeFailsafe            = raScopeFailsafeDelay
	raScopeRetroArchRunning    = retroArchRunning
	raScopeChildRunning        = gameScreenSuspended
	raScopeMigrationCandidates = retroArchConfigCandidates
)

// ── retroarch.cfg lines ──────────────────────────────────────────────────

// raConfigNewline is the line ending a config already uses.
func raConfigNewline(cfg string) string {
	if strings.Contains(cfg, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// raConfigLine splits one `key = "value" # comment` line. ok is false for
// blank lines, comments and lines without '='. A quoted value ends at its
// closing quote, the way RetroArch reads it, so a '#' inside a path is part of
// the path; tail is whatever follows the value.
func raConfigLine(line string) (key, value, tail string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", "", false
	}
	eq := strings.IndexByte(trimmed, '=')
	if eq < 0 {
		return "", "", "", false
	}
	key = strings.TrimSpace(trimmed[:eq])
	rest := strings.TrimSpace(trimmed[eq+1:])
	switch {
	case strings.HasPrefix(rest, `"`):
		if end := strings.IndexByte(rest[1:], '"'); end >= 0 {
			value, tail = rest[1:1+end], strings.TrimSpace(rest[2+end:])
		} else {
			value = rest[1:]
		}
	case strings.IndexByte(rest, '#') >= 0:
		hash := strings.IndexByte(rest, '#')
		value, tail = strings.TrimSpace(rest[:hash]), rest[hash:]
	default:
		value = rest
	}
	return key, value, tail, key != ""
}

// raConfigGet is the value RetroArch applies for key: the first occurrence.
// RetroArch's config reader keeps the first line for a key and ignores later
// duplicates (a script that appended one changes nothing), so reading the last
// one would record — and put back — a value RetroArch never used.
func raConfigGet(cfg, key string) (string, bool) {
	for _, line := range strings.Split(cfg, "\n") {
		if k, v, _, ok := raConfigLine(line); ok && strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// raValueEqual compares two values the way RetroArch reads them: a boolean in
// any capitalisation, anything else exactly.
func raValueEqual(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if raBoolWord(a) && raBoolWord(b) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func raBoolWord(s string) bool {
	return strings.EqualFold(s, "true") || strings.EqualFold(s, "false")
}

// raConfigSet gives every occurrence of key the value, keeping indentation and
// a trailing comment; a missing key is appended, because RetroArch uses its
// built-in default when the line is absent. Nothing else in the file moves.
//
// Lines are split on "\n" and each keeps its own "\r": a file a Windows editor
// or a firmware script touched can mix CRLF and LF lines, and splitting on the
// file's first style would read two lines as one and join them on rewrite.
func raConfigSet(cfg, key, value string) (string, bool) {
	lines := strings.Split(cfg, "\n")
	changed, found := false, false
	for i, line := range lines {
		k, current, tail, ok := raConfigLine(line)
		if !ok || !strings.EqualFold(k, key) {
			continue
		}
		found = true
		if raValueEqual(current, value) {
			continue
		}
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if tail != "" {
			tail = " " + tail
		}
		lines[i] = indent + key + ` = "` + value + `"` + tail + cr
		changed = true
	}
	if found {
		if !changed {
			return cfg, false
		}
		return strings.Join(lines, "\n"), true
	}
	newline := raConfigNewline(cfg)
	if cfg != "" && !strings.HasSuffix(cfg, "\n") {
		cfg += newline
	}
	return cfg + key + ` = "` + value + `"` + newline, true
}

// raConfigUnset removes every line that sets key, so RetroArch falls back to
// its own default — what a key the user never had meant. Like raConfigSet it
// splits on "\n", so a removed line takes its own ending with it.
func raConfigUnset(cfg, key string) (string, bool) {
	lines := strings.Split(cfg, "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, line := range lines {
		if k, _, _, ok := raConfigLine(line); ok && strings.EqualFold(k, key) {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return cfg, false
	}
	return strings.Join(kept, "\n"), true
}

// ── The pure scope rules ─────────────────────────────────────────────────

// raScopePlan lists the edits a launch makes to one config: the values that
// differ from what the file holds now, with what it holds (or that it holds
// nothing) as the value to put back.
func raScopePlan(cfg, path string, values []raScopeValue) []raScopeEdit {
	var edits []raScopeEdit
	for _, v := range values {
		before, had := raConfigGet(cfg, v.Key)
		if (had && raValueEqual(before, v.Value)) || raScopeSkips(cfg, v) {
			continue
		}
		edits = append(edits, raScopeEdit{Path: path, Key: v.Key, Had: had, Before: before, Wrote: v.Value})
	}
	return edits
}

// raScopeApplyText writes the launch's values into one config's text.
func raScopeApplyText(cfg string, values []raScopeValue) (string, bool) {
	changed := false
	for _, v := range values {
		if raScopeSkips(cfg, v) {
			continue
		}
		var step bool
		cfg, step = raConfigSet(cfg, v.Key, v.Value)
		changed = changed || step
	}
	return cfg, changed
}

// raScopeRestoreText puts the user's values back into one config's text. A
// key that no longer holds what JukaHub wrote was changed since — in
// RetroArch's own menu during the game, or by the user — and is left alone.
func raScopeRestoreText(cfg string, edits []raScopeEdit) (string, bool) {
	changed := false
	for _, e := range edits {
		current, has := raConfigGet(cfg, e.Key)
		if !has || !raValueEqual(current, e.Wrote) {
			continue
		}
		var step bool
		if e.Had {
			cfg, step = raConfigSet(cfg, e.Key, e.Before)
		} else {
			cfg, step = raConfigUnset(cfg, e.Key)
		}
		changed = changed || step
	}
	return cfg, changed
}

// raScopeKeptHook hears about a key the restore leaves alone because it no
// longer holds what JukaHub wrote — the player changed it in RetroArch's own
// menu during the game and RetroArch saved it on exit (raffspeed.go keeps a
// fast-forward speed chosen there). It runs under raScope.mu on the restore's
// goroutine: a hook only records and posts (tbxPost), never edits a config.
var raScopeKeptHook func(e raScopeEdit, now string)

// raScopeNoteKept reports every edit of cfg the restore will leave alone.
func raScopeNoteKept(cfg string, edits []raScopeEdit) {
	hook := raScopeKeptHook
	if hook == nil {
		return
	}
	for _, e := range edits {
		if current, has := raConfigGet(cfg, e.Key); has && !raValueEqual(current, e.Wrote) {
			hook(e, current)
		}
	}
}

// raScopeMerge adds a launch's edits to the journal. A key already recorded
// keeps the user's value from the first record while the file still holds
// what JukaHub wrote then (an unfinished earlier launch, or a second edit in
// this one); a key whose value changed since gets the new record.
func raScopeMerge(journal, edits []raScopeEdit) []raScopeEdit {
	out := append([]raScopeEdit(nil), journal...)
	for _, e := range edits {
		merged := false
		for i := range out {
			if out[i].Path != e.Path || !strings.EqualFold(out[i].Key, e.Key) {
				continue
			}
			if e.Had && raValueEqual(e.Before, out[i].Wrote) {
				out[i].Wrote = e.Wrote
			} else {
				out[i] = e
			}
			merged = true
			break
		}
		if !merged {
			out = append(out, e)
		}
	}
	return out
}

// raScopeMigrateText is the one-time repair of an older version's permanent
// edit: a key that still holds the value JukaHub used to write, while the
// backup (the file before JukaHub's first edit) had another value or none,
// goes back to the backup's.
func raScopeMigrateText(cfg, backup string, legacy []raScopeValue) (string, bool) {
	changed := false
	for _, v := range legacy {
		current, has := raConfigGet(cfg, v.Key)
		if !has || !raValueEqual(current, v.Value) {
			continue
		}
		original, hadOriginal := raConfigGet(backup, v.Key)
		if hadOriginal && raValueEqual(original, v.Value) {
			continue
		}
		var step bool
		if hadOriginal {
			cfg, step = raConfigSet(cfg, v.Key, original)
		} else {
			cfg, step = raConfigUnset(cfg, v.Key)
		}
		changed = changed || step
	}
	return cfg, changed
}

// ── Which configs ────────────────────────────────────────────────────────

// raInstallDir is the RetroArch install a config belongs to: its folder, or
// the folder above a .retroarch one.
func raInstallDir(path string) string {
	dir := filepath.Dir(path)
	if strings.EqualFold(filepath.Base(dir), ".retroarch") {
		return filepath.Dir(dir)
	}
	// <RetroArch>/.config/retroarch/retroarch.cfg (raConfigsIn) belongs to the
	// RetroArch two folders up — when one is there: a desktop's
	// ~/.config/retroarch is an install of its own.
	if strings.EqualFold(filepath.Base(dir), "retroarch") && filepath.Base(filepath.Dir(dir)) == ".config" {
		top := filepath.Dir(filepath.Dir(dir))
		for _, name := range netplayRetroArchNames {
			if regularFile(filepath.Join(top, name)) {
				return top
			}
		}
	}
	return dir
}

// raScopeTargets picks the configs a launch edits from candidates (best
// first): the first that exists, and every other existing one of the same
// RetroArch install. One file reached under two names (FAT ignores case, and
// /mnt/sdcard can be a link to /mnt/SDCARD) is edited once.
func raScopeTargets(candidates []string) []string {
	var out []string
	var infos []os.FileInfo
	install := ""
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if install == "" {
			install = raInstallDir(path)
		} else if raInstallDir(path) != install {
			continue
		}
		dup := false
		for _, seen := range infos {
			if os.SameFile(seen, info) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, path)
			infos = append(infos, info)
		}
	}
	return out
}

// raLaunchCandidates puts the config of the RetroArch a launcher starts first
// when JukaHub knows it: a Windows core runs the retroarch.exe above its cores
// folder, which keeps retroarch.cfg beside itself; a handheld core started
// directly (emuchoices.go) runs the RetroArch that owns its cores folder, with
// HOME there, so its configs are the ones that folder holds (raConfigsIn).
func raLaunchCandidates(launcher string, candidates []string) []string {
	if strings.EqualFold(filepath.Ext(launcher), ".dll") {
		if exe := emuRetroArchForCore(launcher); exe != "" {
			return append([]string{filepath.Join(filepath.Dir(exe), "retroarch.cfg")}, candidates...)
		}
	}
	if emuIsCoreLaunch(launcher) {
		if bin := coreRetroArchBinary(filepath.Dir(launcher)); bin != "" {
			return append(raConfigsIn(filepath.Dir(bin)), candidates...)
		}
	}
	return candidates
}

// raScopeExistingConfigs is every existing candidate, each file once.
func raScopeExistingConfigs(candidates []string) []string {
	var out []string
	var infos []os.FileInfo
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		dup := false
		for _, seen := range infos {
			if os.SameFile(seen, info) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, path)
			infos = append(infos, info)
		}
	}
	return out
}

// ── The journal ──────────────────────────────────────────────────────────

func raScopeLoadLocked() raScopeJournal {
	var journal raScopeJournal
	path := raScopeJournalPath()
	if path == "" {
		return journal
	}
	f, err := os.Open(path)
	if err != nil {
		return journal
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, raScopeJournalMax+1))
	if err != nil || len(raw) > raScopeJournalMax {
		logWarn("retroarch", "ignoring the launch journal %s: unreadable or too large", path)
		return raScopeJournal{}
	}
	if err := json.Unmarshal(raw, &journal); err != nil {
		logWarn("retroarch", "ignoring the launch journal %s: %v", path, err)
		return raScopeJournal{}
	}
	return journal
}

// raScopeSaveLocked writes the journal through a temporary file, or removes
// it when nothing is left to restore.
func raScopeSaveLocked(journal raScopeJournal) error {
	return raScopeSaveJournalLocked(journal, true)
}

// raScopeSaveJournalLocked is raScopeSaveLocked with the sync optional: a
// launch batch writes the journal at once (it still comes before the edit it
// covers) and syncs it once, at the batch's end (raScopeBatchCommitLocked).
func raScopeSaveJournalLocked(journal raScopeJournal, durable bool) error {
	path := raScopeJournalPath()
	if path == "" {
		if len(journal.Edits) == 0 {
			return nil
		}
		return fmt.Errorf("no data folder for the launch journal")
	}
	if len(journal.Edits) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	write := writeFileSynced
	if !durable {
		write = os.WriteFile
	}
	if err := write(tmp, append(data, '\n'), 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if durable {
		syncDirectory(filepath.Dir(path))
	}
	return nil
}

// raScopeApply is the launch-time entry point: it records what values change
// in path, then writes them. It reports whether the file changed. A journal
// that cannot be written means no edit at all — an edit nobody could undo is
// the bug this file exists to remove.
func raScopeApply(path string, values []raScopeValue) (bool, error) {
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	edits := raScopePlan(string(raw), path, values)
	if len(edits) == 0 {
		return false, nil
	}
	journal := raScopeLoadLocked()
	journal.Edits = raScopeMerge(journal.Edits, edits)
	// Write-ahead: the user's values are on the card before the first byte of
	// theirs changes, so a power cut in between leaves something to restore
	// from instead of an edit nobody remembers. Inside a launch batch both
	// writes happen now, in the same order, and the batch's end syncs the
	// journal before any config.
	batched := raScopeBatch.depth > 0
	if err := raScopeSaveJournalLocked(journal, !batched); err != nil {
		return false, fmt.Errorf("record the values to restore: %w", err)
	}
	if batched {
		raScopeBatch.journal = true
	}
	changed, err := editRetroArchConfigWith(path, func(cfg string) (string, bool) {
		return raScopeApplyText(cfg, values)
	}, !batched)
	if err != nil {
		return false, err
	}
	if batched && changed {
		raScopeBatchNoteLocked(path, len(edits))
	}
	raScopeArmFailsafeLocked()
	return changed, nil
}

// ── The launch batch ─────────────────────────────────────────────────────

// raScopeBatchBegin opens a launch batch (or joins the one open): the edits
// until its end are written at once and synced together. Every begin needs
// its raScopeBatchEnd; raScopeBatchFlush ends the batch whatever its depth.
func raScopeBatchBegin() {
	if raScopeBatchOff {
		return
	}
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	raScopeBatch.depth++
	if raScopeBatch.depth > 1 {
		return
	}
	raScopeBatch.gen++
	raScopeBatch.began = time.Now()
	raScopeBatchLive.Store(true)
	gen := raScopeBatch.gen
	time.AfterFunc(raScopeBatchMax, func() { raScopeBatchExpire(gen) })
}

// raScopeBatchEnd is a begin's end; the last one syncs the batch. An end with
// no batch open (a flush already ended it) does nothing.
func raScopeBatchEnd() {
	if !raScopeBatchLive.Load() {
		return
	}
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	if raScopeBatch.depth == 0 {
		return
	}
	raScopeBatch.depth--
	if raScopeBatch.depth == 0 {
		raScopeBatchCommitLocked("launch")
	}
}

// raScopeBatchFlush ends the open batch now: the game is about to start and
// reads its config (screenChildStart). The launch's deferred end then finds
// nothing open.
func raScopeBatchFlush() {
	if !raScopeBatchLive.Load() {
		return
	}
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	if raScopeBatch.depth == 0 {
		return
	}
	raScopeBatch.depth = 0
	raScopeBatchCommitLocked("start")
}

// raScopeBatchExpire ends a batch left open for raScopeBatchMax.
func raScopeBatchExpire(gen uint64) {
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	if raScopeBatch.depth == 0 || raScopeBatch.gen != gen {
		return
	}
	logWarn("retroarch", "a launch left its RetroArch settings unsynced for %s; syncing them now", raScopeBatchMax)
	raScopeBatch.depth = 0
	raScopeBatchCommitLocked("timeout")
}

// raScopeBatchNoteLocked records a config the batch wrote without a sync.
func raScopeBatchNoteLocked(path string, edits int) {
	raScopeBatch.edits += edits
	for _, p := range raScopeBatch.paths {
		if p == path {
			return
		}
	}
	raScopeBatch.paths = append(raScopeBatch.paths, path)
}

// raScopeBatchCommitLocked syncs what the batch wrote: the journal and its
// folder first — the user's values are safe on the card before any of the
// launch's edits can be — then each config and its folder, once. A sync that
// fails is logged; the data is in the file either way, and the journal's
// records still cover every edit. Under raScope.mu, depth already 0.
func raScopeBatchCommitLocked(why string) {
	raScopeBatchLive.Store(false)
	journal, paths, edits, began := raScopeBatch.journal, raScopeBatch.paths, raScopeBatch.edits, raScopeBatch.began
	raScopeBatch.journal, raScopeBatch.paths, raScopeBatch.edits = false, nil, 0
	if !journal && len(paths) == 0 {
		return
	}
	start := time.Now()
	var dirs []string
	addDir := func(dir string) {
		for _, d := range dirs {
			if d == dir {
				return
			}
		}
		dirs = append(dirs, dir)
	}
	if path := raScopeJournalPath(); journal && path != "" {
		if err := raScopeSyncFile(path); err != nil && !os.IsNotExist(err) {
			logWarn("retroarch", "could not sync the launch journal: %v", err)
		}
		syncDirectory(filepath.Dir(path))
	}
	for _, p := range paths {
		if err := raScopeSyncFile(p); err != nil && !os.IsNotExist(err) {
			logWarn("retroarch", "could not sync %s: %v", p, err)
		}
		addDir(filepath.Dir(p))
	}
	for _, d := range dirs {
		syncDirectory(d)
	}
	logDebug("retroarch", "launch settings: %s in %s synced once at the %s in %s (batch open %s)",
		pluralizeCount(edits, "edit", "edits"), pluralizeCount(len(paths), "file", "files"), why,
		time.Since(start).Round(time.Millisecond), start.Sub(began).Round(time.Millisecond))
	// The failsafe for a launch that never starts, from the batch's end: an
	// edit made early in a long preparation still gets its full delay.
	raScopeArmFailsafeLocked()
}

// raScopeSyncFile flushes one file to the card. Opened for writing: Windows
// flushes only a handle that may write.
func raScopeSyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// raScopeRestoreLocked puts every recorded value back and clears the journal.
// A config that cannot be written keeps its records for the next attempt; a
// config that is gone (card swapped, RetroArch removed) has nothing to restore.
func raScopeRestoreLocked() (int, error) {
	if raScope.timer != nil {
		raScope.timer.Stop()
		raScope.timer = nil
	}
	journal := raScopeLoadLocked()
	if len(journal.Edits) == 0 {
		return 0, raScopeSaveLocked(journal)
	}
	var paths []string
	byPath := map[string][]raScopeEdit{}
	for _, e := range journal.Edits {
		if _, ok := byPath[e.Path]; !ok {
			paths = append(paths, e.Path)
		}
		byPath[e.Path] = append(byPath[e.Path], e)
	}
	var remaining []raScopeEdit
	var first error
	restored := 0
	for _, path := range paths {
		edits := byPath[path]
		if !regularFile(path) {
			continue
		}
		changed, err := editRetroArchConfig(path, func(cfg string) (string, bool) {
			raScopeNoteKept(cfg, edits)
			return raScopeRestoreText(cfg, edits)
		})
		if err != nil {
			remaining = append(remaining, edits...)
			if first == nil {
				first = err
			}
			continue
		}
		if changed {
			restored += len(edits)
		}
	}
	journal.Edits = remaining
	if err := raScopeSaveLocked(journal); err != nil && first == nil {
		first = err
	}
	return restored, first
}

// raScopeArmFailsafeLocked schedules the check for a launch whose emulator
// never started: game_library.go edits the config first and starts the
// emulator after, and a start that fails leaves no child whose exit would
// restore. One timer per launch; a later edit re-arms it.
func raScopeArmFailsafeLocked() {
	if raScopeFailsafe <= 0 {
		return
	}
	if raScope.timer != nil {
		raScope.timer.Stop()
	}
	raScope.timer = time.AfterFunc(raScopeFailsafe, raScopeFailsafeCheck)
}

func raScopeFailsafeCheck() {
	if raScopeChildRunning() || raScopeRetroArchRunning() {
		return // the child's exit restores
	}
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	// A launch preparing its edits: its batch's end arms the failsafe again.
	if raScopeChildRunning() || raScopeBatch.depth > 0 {
		return
	}
	if n, err := raScopeRestoreLocked(); err != nil {
		logWarn("retroarch", "could not put the RetroArch settings back after a launch that did not start: %v", err)
	} else if n > 0 {
		logInfo("retroarch", "launch did not start; %d RetroArch setting(s) put back", n)
	}
}

// ── Hooks ────────────────────────────────────────────────────────────────

// raScopeScreenFree is called when the last screen-taking child is gone
// (noteGameChildExited). It never blocks the reaper: with a journal on the card
// the restore runs on its own goroutine.
func raScopeScreenFree() {
	path := raScopeJournalPath()
	if path == "" || !fileExists(path) {
		return
	}
	go raScopeRestoreAfterExit()
}

// raScopeRestoreAfterExit waits for RetroArch itself to be gone — a launcher
// that left it running outside its group would otherwise get the values back
// under a live emulator, which writes its own on exit — then restores, unless
// a new launch has started meanwhile, or is preparing (its batch is open):
// that launch's exit restores everything.
func raScopeRestoreAfterExit() {
	deadline := time.Now().Add(raScopeWaitMax)
	for raScopeRetroArchRunning() && time.Now().Before(deadline) {
		time.Sleep(raScopeWaitPoll)
	}
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	if raScopeChildRunning() || raScopeBatch.depth > 0 {
		return
	}
	if n, err := raScopeRestoreLocked(); err != nil {
		logWarn("retroarch", "could not put the RetroArch settings back: %v", err)
	} else if n > 0 {
		logDebug("retroarch", "%d RetroArch setting(s) put back after the game", n)
	}
}

// raScopeStartupRecover runs once after the first frame: values a crash or a
// power cut left in retroarch.cfg go back, then the one-time repair of older
// versions' permanent edits runs. A RetroArch that is somehow still running
// keeps everything for the next start.
//
// The repair's values are read here, on the main thread (deferStartupTask):
// they come from the live Custom map, which the main thread keeps writing, and
// reading it from the goroutine could die with a fatal concurrent map access
// (extras.go saveUserConfigDebounced has the history).
func raScopeStartupRecover() {
	legacy := raScopeLegacyValues()
	go func() {
		if raScopeRetroArchRunning() {
			logInfo("retroarch", "RetroArch is running; its settings are restored at the next start")
			return
		}
		raScope.mu.Lock()
		defer raScope.mu.Unlock()
		// A game resumed at power-on may be preparing already: its exit puts
		// the old records back with its own, and the repair waits a start.
		if raScopeChildRunning() || raScopeBatch.depth > 0 {
			return
		}
		if n, err := raScopeRestoreLocked(); err != nil {
			logWarn("retroarch", "could not put back the RetroArch settings an unfinished launch left: %v", err)
		} else if n > 0 {
			logInfo("retroarch", "%d RetroArch setting(s) an unfinished launch left were put back", n)
		}
		raScopeMigrateLocked(legacy)
	}()
}

// raScopeMarkerPath is the one-time repair's marker, beside the journal.
func raScopeMarkerPath() string {
	path := raScopeJournalPath()
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), raScopeMigratedName)
}

// raScopeLegacyValues are the values older versions wrote and never undid,
// for the features still switched on: what the repair looks for. Main thread
// only: it reads appConfig's Custom map.
func raScopeLegacyValues() []raScopeValue {
	var values []raScopeValue
	if savestateResumeEnabled() {
		values = append(values, savestateResumeValues()...)
	}
	if recordingsEnabled() {
		root := strings.TrimSpace(recordingsSDRoot())
		if root == "" {
			root = exeDir()
		}
		values = append(values, raScopeValue{Key: retroArchRecordingDirKey, Value: filepath.ToSlash(filepath.Join(root, recordingsFolderName))})
		if appConfig != nil {
			if v, ok := customValue(appConfig.Variables.Custom, recordingsDirConfigKey); ok {
				if s, ok := v.(string); ok {
					if dir := absoluteRecordingDir(s); dir != "" {
						values = append(values, raScopeValue{Key: retroArchRecordingDirKey, Value: filepath.ToSlash(dir)})
					}
				}
			}
		}
	}
	return append(values, osdPresetScopeValues(osdPresetSetting())...)
}

// raScopeMigrateLocked runs the one-time repair over every config that has a
// JukaHub backup beside it, looking for legacy (raScopeLegacyValues, read by
// the caller on the main thread). A config it cannot write leaves the marker
// unset, so the next start tries again.
func raScopeMigrateLocked(legacy []raScopeValue) {
	marker := raScopeMarkerPath()
	if marker == "" || fileExists(marker) {
		return
	}
	for _, path := range raScopeExistingConfigs(raScopeMigrationCandidates()) {
		backup, err := os.ReadFile(path + savestateBackupSuffix)
		if err != nil {
			continue // JukaHub never edited this file
		}
		changed, err := editRetroArchConfig(path, func(cfg string) (string, bool) {
			return raScopeMigrateText(cfg, string(backup), legacy)
		})
		if err != nil {
			logWarn("retroarch", "could not undo an older version's edit in %s: %v", path, err)
			return
		}
		if changed {
			Log().Info("RetroArch settings an older JukaHub left behind were put back", "config", path)
		}
	}
	if err := writeFileSynced(marker, []byte("1\n"), 0o644); err != nil {
		logWarn("retroarch", "could not record the RetroArch settings repair: %v", err)
	}
}
