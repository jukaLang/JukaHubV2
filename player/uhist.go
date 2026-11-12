package main

// uhist.go — JukaHub's version history: the installs an update replaced, kept
// on the device so a bad release is one press from being undone.
//
// Nothing extra is copied for it. Before the swap helper runs, the journal
// already copies every file the swap overwrites into journals/files/<id> —
// that copy is what rolls back an interrupted update — and those copies used
// to be deleted at the next start once the helper had committed. Now
// uhistNoteSwap writes a small record beside them before the helper starts,
// and at the next start uhistAdoptCommittedSwaps moves the folder of a
// committed swap to versions/<id> with a version.json saying what it holds
// (version, release date, notes, files). A swap that was interrupted is left
// alone: the journal check rolls it back and its backups go with it.
//
// "Roll back to this version" builds the file list from a saved version and
// hands it to swapInstallFiles, the journaled helper path an update uses, so
// an interrupted rollback is recovered exactly like an interrupted update. The
// version a rollback replaces is saved in turn, so a rollback can be undone the
// same way.
//
// How many versions are kept is the "updateKeepVersions" setting (0–3, default
// 2), never at the cost of the card: below uhistMinFree free space the oldest
// go first. The release a device was rolled back from is not offered again
// until a newer one is published (uhistSkipped).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jukahub/JukaHub/internal/jdelta"
)

const (
	// uhistKeepSetting is the custom variable the Settings row writes (0–3).
	uhistKeepSetting = "updateKeepVersions"
	// uhistKeepDefault is how many versions are kept when it was never set.
	uhistKeepDefault = 2
	// uhistKeepMax is the most the setting offers: each saved version is a
	// whole JukaHub, tens of megabytes on the card.
	uhistKeepMax = 3
	// uhistNotesMax bounds the release notes stored with a version.
	uhistNotesMax = 16 << 10
	// uhistMinFree is the free space saved versions never eat into: on a card
	// below it the oldest saved version is deleted first.
	uhistMinFree = int64(256 << 20)
)

// Why a swap ran, recorded with what it replaced.
const (
	uhistReasonUpdate   = "update"
	uhistReasonRollback = "rollback"
)

// uhistKeep mirrors the setting for the swap goroutine; the Custom map belongs
// to the main thread (upchLoadPrefs and the Settings row fill it).
var uhistKeep atomic.Int32

func init() { uhistKeep.Store(uhistKeepDefault) }

// uhistFile is one file of a saved version: Name is its path in the
// installation (slash form), Stored its file name in the version's folder.
type uhistFile struct {
	Name   string `json:"name"`
	Stored string `json:"stored"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode,omitempty"`
}

// uhistEntry is one saved version, versions/<ID>/version.json. Tag, Date and
// Version describe the build; SavedAt is when a swap replaced it, ReplacedBy
// what replaced it, and Added the files that swap created (they stay in place
// after a rollback).
type uhistEntry struct {
	ID         string      `json:"id"`
	Tag        string      `json:"tag,omitempty"`
	Date       string      `json:"date,omitempty"`
	Version    string      `json:"version,omitempty"`
	SavedAt    string      `json:"saved_at"`
	Reason     string      `json:"reason,omitempty"`
	ReplacedBy string      `json:"replaced_by,omitempty"`
	Notes      string      `json:"notes,omitempty"`
	Files      []uhistFile `json:"files"`
	Added      []string    `json:"added,omitempty"`
	Bytes      int64       `json:"bytes"`
}

// uhistPending is written before the swap helper starts, beside the journal it
// belongs to. The From fields describe the running build (what the swap
// replaces), the To fields the build it installs, and RestoredID the saved
// version a rollback restores.
type uhistPending struct {
	JournalID   string      `json:"journal_id"`
	Reason      string      `json:"reason"`
	Keep        int         `json:"keep"`
	FromTag     string      `json:"from_tag,omitempty"`
	FromDate    string      `json:"from_date,omitempty"`
	FromVersion string      `json:"from_version,omitempty"`
	ToTag       string      `json:"to_tag,omitempty"`
	ToDate      string      `json:"to_date,omitempty"`
	ToNotes     string      `json:"to_notes,omitempty"`
	RestoredID  string      `json:"restored_id,omitempty"`
	Notes       string      `json:"notes,omitempty"`
	Files       []uhistFile `json:"files"`
	Added       []string    `json:"added,omitempty"`
	CreatedAt   string      `json:"created_at"`
}

// uhistStateFile is versions/state.json.
type uhistStateFile struct {
	SkipTag string `json:"skip_tag,omitempty"`
}

// uhistNotesFile is versions/installed-notes.json: the running build's own
// release notes, kept so they can travel with it when an update replaces it.
type uhistNotesFile struct {
	Tag   string `json:"tag"`
	Notes string `json:"notes"`
}

// uhistCache is the history the UI reads every frame. It is filled at startup
// (InitPatchModule) and after every change, never from the render thread.
var uhistCache struct {
	mu         sync.Mutex
	loaded     bool
	entries    []uhistEntry
	version    uint64
	skipTag    string
	summary    string
	sumValid   bool
	sumVersion uint64
	sumKeep    int32
}

// uhistRollbackTarget is the saved version the next rollback swap restores.
// Patch operations run one at a time, so one slot is enough.
var uhistRollbackTarget struct {
	mu    sync.Mutex
	entry *uhistEntry
}

// ── Setting ──────────────────────────────────────────────────────────────────

// uhistKeepFromConfig reads the setting (main thread only).
func uhistKeepFromConfig(config *Config) int {
	if config == nil {
		return uhistKeepDefault
	}
	if v, ok := customValue(config.Variables.Custom, uhistKeepSetting); ok {
		if n, ok := coerceInt(v); ok {
			if n < 0 {
				return 0
			}
			if n > uhistKeepMax {
				return uhistKeepMax
			}
			return n
		}
	}
	return uhistKeepDefault
}

// uhistKeepOptions are the stops of the Settings row; the stored value is the
// stop's index.
func uhistKeepOptions() []setOption {
	return []setOption{
		{Value: "0", Label: "Off", Note: "Updates keep nothing to roll back to"},
		{Value: "1", Label: "1 version"},
		{Value: "2", Label: "2 versions", Note: "The default"},
		{Value: "3", Label: "3 versions"},
	}
}

// uhistKeepIndex is the Settings row's current stop.
func uhistKeepIndex(c *Config) int { return uhistKeepFromConfig(c) }

// uhistChooseKeep stores a new limit (main thread) and, when it is lower than
// what is kept, frees the oldest copies now rather than at the next update.
// The Settings row is a carousel that wraps — A on "3 versions" lands on Off —
// so a limit that would delete saved copies asks first; a rollback copy is the
// one thing a bad update cannot give back.
func uhistChooseKeep(c *Config, value string) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > uhistKeepMax {
		return
	}
	if kept, _ := uhistCount(); n < kept {
		drop := kept - n
		what := "the oldest saved version"
		if drop > 1 {
			what = "the " + strconv.Itoa(drop) + " oldest saved versions"
		}
		note := "Keeping " + strconv.Itoa(n) + " deletes " + what + " now"
		if n == 0 {
			note = "Turning this off deletes every saved version now"
		}
		setSheetOpen(&setHub.sheet, "Remove saved versions?", note,
			[]setOption{{Value: "keep", Label: "Keep them"}, {Value: "remove", Label: "Remove"}}, "keep",
			func(choice string) {
				if choice == "remove" {
					uhistApplyKeep(c, n)
				}
			})
		return
	}
	uhistApplyKeep(c, n)
}

// uhistApplyKeep is uhistChooseKeep once the choice is settled.
func uhistApplyKeep(c *Config, n int) {
	uhistKeep.Store(int32(n))
	if c != nil {
		if c.Variables.Custom == nil {
			c.Variables.Custom = map[string]interface{}{}
		}
		c.Variables.Custom[uhistKeepSetting] = n
		saveUserConfigDebounced(userConfigFromVariables(c))
	}
	go func() {
		dir, err := uhistDir()
		if err != nil {
			return
		}
		uhistPrune(dir, n)
		uhistReload(dir)
	}()
	switch n {
	case 0:
		ShowToast("Previous versions: off — saved copies are removed", ToastKindInfo)
	case 1:
		ShowToast("Keeping 1 previous version", ToastKindInfo)
	default:
		ShowToast("Keeping "+strconv.Itoa(n)+" previous versions", ToastKindInfo)
	}
}

// uhistKeepDetail is the line under the Settings row.
func uhistKeepDetail() string {
	n, total := uhistCount()
	switch {
	case n == 1:
		return "1 kept (" + humanBytes(total) + ") · roll back from Updates"
	case n > 1:
		return strconv.Itoa(n) + " kept (" + humanBytes(total) + ") · roll back from Updates"
	}
	return "For one-press rollback · bounded by free space"
}

// ── Cache ────────────────────────────────────────────────────────────────────

// uhistDir is versions/ in the Patch state folder.
func uhistDir() (string, error) {
	base, err := PatchStateDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "versions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// uhistScan reads every saved version under dir, newest first.
func uhistScan(dir string) []uhistEntry {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var entries []uhistEntry
	for _, it := range items {
		if !it.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, it.Name(), "version.json"))
		if err != nil {
			continue
		}
		var e uhistEntry
		if json.Unmarshal(data, &e) != nil || e.ID != it.Name() || len(e.Files) == 0 {
			continue
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].SavedAt > entries[j].SavedAt })
	return entries
}

// uhistReload refreshes the cache from dir. A saved copy of the build that is
// running now is redundant (a rollback restored it, or it was installed again)
// and is removed. "Running now" is the stamp next to the executable alone: at
// startup this runs before confirmAppliedUpdate, while the patch state still
// names the build that was just replaced — the one this history keeps.
func uhistReload(dir string) {
	running := strings.TrimSpace(readInstalledReleaseInfo().Tag)
	var kept []uhistEntry
	for _, e := range uhistScan(dir) {
		if running != "" && strings.EqualFold(e.Tag, running) {
			_ = os.RemoveAll(filepath.Join(dir, e.ID))
			continue
		}
		kept = append(kept, e)
	}
	skip := uhistReadState(dir).SkipTag
	uhistCache.mu.Lock()
	uhistCache.entries = kept
	uhistCache.loaded = true
	uhistCache.skipTag = skip
	uhistCache.version++
	uhistCache.mu.Unlock()
}

// uhistEntriesSnapshot returns the saved versions, newest first, and the
// cache's change counter.
func uhistEntriesSnapshot() ([]uhistEntry, uint64) {
	uhistCache.mu.Lock()
	defer uhistCache.mu.Unlock()
	return append([]uhistEntry(nil), uhistCache.entries...), uhistCache.version
}

// uhistVersion is the cache's change counter, cheap enough for every frame.
func uhistVersion() uint64 {
	uhistCache.mu.Lock()
	defer uhistCache.mu.Unlock()
	return uhistCache.version
}

// uhistEntryByID finds a saved version.
func uhistEntryByID(id string) (uhistEntry, bool) {
	uhistCache.mu.Lock()
	defer uhistCache.mu.Unlock()
	for _, e := range uhistCache.entries {
		if e.ID == id {
			return e, true
		}
	}
	return uhistEntry{}, false
}

// uhistCount is how many versions are saved and their size on the card.
func uhistCount() (int, int64) {
	uhistCache.mu.Lock()
	defer uhistCache.mu.Unlock()
	var total int64
	for _, e := range uhistCache.entries {
		total += e.Bytes
	}
	return len(uhistCache.entries), total
}

// uhistSkipped reports whether tag is the release this device was rolled back
// from, which is not offered again.
func uhistSkipped(tag string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return false
	}
	uhistCache.mu.Lock()
	defer uhistCache.mu.Unlock()
	return uhistCache.skipTag != "" && strings.EqualFold(uhistCache.skipTag, tag)
}

// uhistSkippedTag is the release that is not offered again, or "".
func uhistSkippedTag() string {
	uhistCache.mu.Lock()
	defer uhistCache.mu.Unlock()
	return uhistCache.skipTag
}

// uhistSummaryLine is the Patch row's line under "Version history". It is
// rebuilt only when the history or the setting changed: the row is drawn every
// frame.
func uhistSummaryLine() string {
	keep := uhistKeep.Load()
	uhistCache.mu.Lock()
	defer uhistCache.mu.Unlock()
	if uhistCache.sumValid && uhistCache.sumVersion == uhistCache.version && uhistCache.sumKeep == keep {
		return uhistCache.summary
	}
	n := len(uhistCache.entries)
	var total int64
	for _, e := range uhistCache.entries {
		total += e.Bytes
	}
	line := "The next update keeps the version it replaces, for one-press rollback"
	switch {
	case n == 1:
		line = "1 earlier version kept (" + humanBytes(total) + ") · roll back in one press"
	case n > 1:
		line = strconv.Itoa(n) + " earlier versions kept (" + humanBytes(total) + ") · roll back in one press"
	case keep == 0:
		line = "Keeping previous versions is off · Settings → About"
	}
	uhistCache.summary = line
	uhistCache.sumValid = true
	uhistCache.sumVersion = uhistCache.version
	uhistCache.sumKeep = keep
	return line
}

// uhistLabel names a build for people: its release date and time, else its
// tag or version.
func uhistLabel(tag, date, version string) string {
	if t, ok := parseReleaseTimestamp(date); ok {
		return t.Format("2006-01-02 15:04")
	}
	if t, ok := releaseTimeFromTag(tag); ok {
		return t.Format("2006-01-02 15:04")
	}
	if strings.TrimSpace(tag) != "" {
		return tag
	}
	if strings.TrimSpace(version) != "" {
		return version
	}
	return "an earlier build"
}

// ── State files ──────────────────────────────────────────────────────────────

func uhistReadState(dir string) uhistStateFile {
	var st uhistStateFile
	if data, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func uhistWriteState(dir string, st uhistStateFile) {
	if err := upchWriteJSON(filepath.Join(dir, "state.json"), st); err != nil {
		logPatch("version history: %v", err)
	}
}

// uhistWriteInstalledNotes remembers the notes of the build now installed.
func uhistWriteInstalledNotes(dir, tag, notes string) {
	path := filepath.Join(dir, "installed-notes.json")
	if strings.TrimSpace(tag) == "" || strings.TrimSpace(notes) == "" {
		_ = os.Remove(path)
		return
	}
	if err := upchWriteJSON(path, uhistNotesFile{Tag: tag, Notes: uhistTrimNotes(notes)}); err != nil {
		logPatch("version history: %v", err)
	}
}

// uhistNotesFor returns the release notes known for tag: the installed build's
// own, or the cached notes of the newest release when they are for tag.
func uhistNotesFor(dir, tag string) string {
	if strings.TrimSpace(tag) == "" {
		return ""
	}
	if data, err := os.ReadFile(filepath.Join(dir, "installed-notes.json")); err == nil {
		var nf uhistNotesFile
		if json.Unmarshal(data, &nf) == nil && strings.EqualFold(nf.Tag, tag) {
			return uhistTrimNotes(nf.Notes)
		}
	}
	if notesTag, notes := cachedReleaseNotes(); strings.EqualFold(notesTag, tag) {
		return uhistTrimNotes(notes)
	}
	return ""
}

// uhistTrimNotes bounds stored notes without cutting a character in half.
func uhistTrimNotes(notes string) string {
	return truncateRunesSuffix(strings.TrimSpace(notes), uhistNotesMax, "")
}

// ── Swap hooks ───────────────────────────────────────────────────────────────

func uhistPendingPath(dir, journalID string) string {
	return filepath.Join(dir, "pending-"+journalID+".json")
}

// uhistNoteSwap records what a swap replaces, before the helper runs. j has
// just copied every file the swap overwrites; if the helper commits, the next
// start keeps that copy (uhistAdoptCommittedSwaps).
func uhistNoteSwap(j *journal, exeDir, tag, reason string) {
	if j == nil || j.ID == "" {
		return
	}
	dir, err := uhistDir()
	if err != nil {
		logPatch("version history: %v", err)
		return
	}
	info := readInstalledReleaseInfo()
	p := uhistPending{
		JournalID:   j.ID,
		Reason:      reason,
		Keep:        int(uhistKeep.Load()),
		FromTag:     info.Tag,
		FromDate:    info.Date,
		FromVersion: info.Version,
		ToTag:       tag,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if p.FromVersion == "" {
		p.FromVersion = versionString(appConfig)
	}
	if p.FromDate == "" {
		if t, ok := installedReleaseTime(); ok {
			p.FromDate = t.UTC().Format(time.RFC3339)
		}
	}
	p.Notes = uhistNotesFor(dir, p.FromTag)
	if reason == uhistReasonRollback {
		if target := uhistTakeRollbackTarget(); target != nil {
			p.ToTag, p.ToDate, p.ToNotes, p.RestoredID = target.Tag, target.Date, target.Notes, target.ID
		}
	} else {
		patchStateMu.Lock()
		p.ToDate = patchState.LastUpdatePublishedAt
		patchStateMu.Unlock()
		if notesTag, notes := cachedReleaseNotes(); strings.EqualFold(notesTag, tag) {
			p.ToNotes = uhistTrimNotes(notes)
		}
	}
	for _, e := range j.Entries {
		rel, err := filepath.Rel(exeDir, e.Path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		name := filepath.ToSlash(rel)
		switch e.Kind {
		case "replace":
			if e.Backup == "" {
				continue
			}
			st, err := os.Stat(e.Backup)
			if err != nil {
				continue
			}
			f := uhistFile{Name: name, Stored: filepath.Base(e.Backup), Size: st.Size()}
			if cur, err := os.Stat(e.Path); err == nil {
				f.Mode = uint32(cur.Mode().Perm())
			}
			p.Files = append(p.Files, f)
		case "remove":
			p.Added = append(p.Added, name)
		}
	}
	if err := upchWriteJSON(uhistPendingPath(dir, j.ID), p); err != nil {
		logPatch("version history: could not note the swap: %v", err)
	}
}

// uhistForgetSwap drops the record of a swap whose helper never started.
func uhistForgetSwap(j *journal) {
	if j == nil || j.ID == "" {
		return
	}
	if dir, err := uhistDir(); err == nil {
		_ = os.Remove(uhistPendingPath(dir, j.ID))
	}
	uhistTakeRollbackTarget()
}

func uhistSetRollbackTarget(e uhistEntry) {
	uhistRollbackTarget.mu.Lock()
	uhistRollbackTarget.entry = &e
	uhistRollbackTarget.mu.Unlock()
}

func uhistTakeRollbackTarget() *uhistEntry {
	uhistRollbackTarget.mu.Lock()
	defer uhistRollbackTarget.mu.Unlock()
	e := uhistRollbackTarget.entry
	uhistRollbackTarget.entry = nil
	return e
}

// uhistAdoptCommittedSwaps runs at startup, before the journal check. A swap
// whose journal the helper deleted committed: its backups become a saved
// version. A swap whose journal is still there was interrupted and is the
// journal check's to roll back; one with neither left was already rolled back.
func uhistAdoptCommittedSwaps() {
	base, err := PatchStateDir()
	if err != nil {
		return
	}
	dir, err := uhistDir()
	if err != nil {
		return
	}
	pendings, _ := filepath.Glob(filepath.Join(dir, "pending-*.json"))
	sort.Strings(pendings)
	for _, pp := range pendings {
		var p uhistPending
		data, err := os.ReadFile(pp)
		if err != nil || json.Unmarshal(data, &p) != nil || p.JournalID == "" || filepath.Base(p.JournalID) != p.JournalID {
			_ = os.Remove(pp)
			continue
		}
		journalFile := filepath.Join(base, "journals", p.JournalID+".json")
		filesDir := filepath.Join(base, "journals", "files", p.JournalID)
		if pathExists(journalFile) || !pathExists(filesDir) {
			_ = os.Remove(pp)
			continue
		}
		uhistSwapCommitted(dir, p)
		if p.Keep > 0 && len(p.Files) > 0 {
			if err := uhistAdopt(dir, filesDir, p); err != nil {
				logPatch("version history: could not keep %s: %v", uhistLabel(p.FromTag, p.FromDate, p.FromVersion), err)
			} else {
				uhistPrune(dir, p.Keep)
			}
		}
		_ = os.Remove(pp)
	}
	uhistReload(dir)
}

// uhistSwapCommitted settles the state a committed swap leaves behind. After
// a rollback the patch state still names the release that was replaced; it is
// pointed at the restored build, the restored copy (now the running build) is
// dropped, and the release rolled back from is not offered again.
func uhistSwapCommitted(dir string, p uhistPending) {
	if p.Reason == uhistReasonRollback {
		patchStateMu.Lock()
		patchState.InstalledReleaseTag = p.ToTag
		patchState.InstalledReleaseAt = p.ToDate
		patchStateMu.Unlock()
		_ = savePatchState()
		if p.RestoredID != "" && filepath.Base(p.RestoredID) == p.RestoredID && !strings.HasPrefix(p.RestoredID, ".") {
			_ = os.RemoveAll(filepath.Join(dir, p.RestoredID))
		}
		uhistWriteState(dir, uhistStateFile{SkipTag: p.FromTag})
		if p.FromTag != "" && strings.EqualFold(availableUpdateTag(), p.FromTag) {
			clearAvailableUpdate()
		}
		label := uhistLabel(p.ToTag, p.ToDate, "")
		logPatch("version history: rolled back to %s", label)
		notifPush("Rolled back to JukaHub "+label+" — the version you left is in Version history", notifInfo)
	} else {
		// Installing any release ends the "not again" for the one rolled back from.
		uhistWriteState(dir, uhistStateFile{})
	}
	uhistWriteInstalledNotes(dir, p.ToTag, p.ToNotes)
}

// uhistAdopt moves a committed swap's backups into versions/ and describes
// them.
func uhistAdopt(dir, filesDir string, p uhistPending) error {
	id := uhistEntryID(p.FromTag, p.CreatedAt)
	dest := filepath.Join(dir, id)
	_ = os.RemoveAll(dest)
	if err := os.Rename(filesDir, dest); err != nil {
		return err
	}
	e := uhistEntry{
		ID:         id,
		Tag:        p.FromTag,
		Date:       p.FromDate,
		Version:    p.FromVersion,
		SavedAt:    p.CreatedAt,
		Reason:     p.Reason,
		ReplacedBy: p.ToTag,
		Notes:      p.Notes,
		Files:      p.Files,
		Added:      p.Added,
	}
	for _, f := range e.Files {
		e.Bytes += f.Size
	}
	if err := upchWriteJSON(filepath.Join(dest, "version.json"), e); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	logPatch("version history: kept %s (%s)", uhistLabel(e.Tag, e.Date, e.Version), humanBytes(e.Bytes))
	return nil
}

// uhistEntryID is a saved version's folder name: the tag (or "build") and when
// it was replaced.
func uhistEntryID(tag, createdAt string) string {
	label := uhistSafeName(tag)
	if label == "" {
		label = "build"
	}
	stamp := uhistSafeName(strings.NewReplacer("-", "", ":", "").Replace(createdAt))
	if stamp == "" {
		stamp = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return label + "_" + stamp
}

// uhistSafeName keeps what a folder name may hold everywhere.
func uhistSafeName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// uhistPrune keeps the newest keep saved versions, then deletes the oldest of
// those while the card is below uhistMinFree.
func uhistPrune(dir string, keep int) {
	entries := uhistScan(dir)
	if keep < 0 {
		keep = 0
	}
	for i := keep; i < len(entries); i++ {
		_ = os.RemoveAll(filepath.Join(dir, entries[i].ID))
	}
	if len(entries) > keep {
		entries = entries[:keep]
	}
	for i := len(entries) - 1; i >= 0; i-- {
		free, err := freeBytes(dir)
		if err != nil || free >= uhistMinFree {
			break
		}
		logPatch("version history: the card is nearly full; removing the saved %s", uhistLabel(entries[i].Tag, entries[i].Date, entries[i].Version))
		_ = os.RemoveAll(filepath.Join(dir, entries[i].ID))
	}
}

// uhistDelete removes one saved version.
func uhistDelete(id string) error {
	if id == "" || filepath.Base(id) != id || strings.HasPrefix(id, ".") {
		return errors.New("unknown saved version")
	}
	dir, err := uhistDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(dir, id)); err != nil {
		return err
	}
	uhistReload(dir)
	return nil
}

// ── Rollback ─────────────────────────────────────────────────────────────────

// uhistRollbackOp installs a saved version through the journaled helper path
// and restarts JukaHub. It runs as a Patch operation.
func uhistRollbackOp(ctx context.Context, config *Config, id string) error {
	e, ok := uhistEntryByID(id)
	if !ok {
		return errors.New("that saved version is no longer on the device")
	}
	dir, err := uhistDir()
	if err != nil {
		return err
	}
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return err
	}
	exeDir := filepath.Dir(exePath)
	patchRowStatus(ComponentApp, StatusChecking)
	files, err := uhistRollbackFiles(dir, e, exeDir)
	if err != nil {
		patchRowStatus(ComponentApp, StatusFailed)
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	base, err := PatchStateDir()
	if err != nil {
		return err
	}
	scriptDir := filepath.Join(base, "staging", "rollback")
	_ = os.RemoveAll(scriptDir)
	if err := os.MkdirAll(scriptDir, 0o700); err != nil {
		return err
	}
	// A version saved from an install without a release stamp gets one, so the
	// restored build does not keep reporting the release it replaced.
	if !uhistHasStamp(files) {
		stamp := releaseInfo{Tag: e.Tag, Date: e.Date, Version: e.Version}
		if err := writeReleaseInfo(scriptDir, stamp); err != nil {
			return err
		}
		_ = os.Remove(releaseInfoPath(scriptDir) + ".bak")
		files = append(files, stagedInstallFile{
			Src:  releaseInfoPath(scriptDir),
			Name: releaseInfoFileName,
			Dest: filepath.Join(exeDir, releaseInfoFileName),
		})
	}
	tag := e.Tag
	if tag == "" {
		tag = e.ID
	}
	logPatch("version history: rolling back to %s", uhistLabel(e.Tag, e.Date, e.Version))
	uhistSetRollbackTarget(e)
	if err := swapInstallFiles(tag, files, scriptDir, exePath, uhistReasonRollback); err != nil {
		uhistTakeRollbackTarget()
		patchRowStatus(ComponentApp, StatusFailed)
		return err
	}
	return nil
}

// uhistRollbackFiles lists what a rollback copies, after checking every saved
// file is still whole and the saved JukaHub runs on this device. The release
// stamp goes last, as in an update: it marks a finished copy.
func uhistRollbackFiles(dir string, e uhistEntry, exeDir string) ([]stagedInstallFile, error) {
	src := filepath.Join(dir, e.ID)
	files := make([]stagedInstallFile, 0, len(e.Files)+1)
	for _, f := range e.Files {
		if !jdelta.ValidName(f.Name) || f.Stored == "" || filepath.Base(f.Stored) != f.Stored {
			return nil, fmt.Errorf("the saved version lists an unusable path %q", f.Name)
		}
		if isPreservedInstallPath(filepath.FromSlash(f.Name)) {
			continue
		}
		p := filepath.Join(src, f.Stored)
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("the saved copy of %s is missing", f.Name)
		}
		if f.Size > 0 && info.Size() != f.Size {
			return nil, fmt.Errorf("the saved copy of %s is damaged", f.Name)
		}
		if upchIsExecutableName(f.Name) {
			if err := upchCheckHostBinary(p); err != nil {
				return nil, fmt.Errorf("the saved JukaHub cannot run here: %w", err)
			}
		}
		rel := filepath.FromSlash(f.Name)
		files = append(files, stagedInstallFile{Src: p, Name: rel, Dest: filepath.Join(exeDir, rel)})
	}
	if len(files) == 0 {
		return nil, errors.New("the saved version holds no files")
	}
	if len(files) > autoUpdateMaxFiles {
		return nil, fmt.Errorf("the saved version has %d files (limit %d)", len(files), autoUpdateMaxFiles)
	}
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Name != releaseInfoFileName && files[j].Name == releaseInfoFileName
	})
	return files, nil
}

func uhistHasStamp(files []stagedInstallFile) bool {
	for _, f := range files {
		if f.Name == releaseInfoFileName {
			return true
		}
	}
	return false
}
