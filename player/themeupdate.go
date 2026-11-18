package main

// themeupdate.go — community themes that follow their author's new versions.
//
// A theme installed from the gallery (themecatalog.go) used to be a one-time
// copy: when its author fixed a colour or published a new look, the handheld
// kept the old file for ever. This file closes that loop over the air.
//
//   - Every install records which gallery theme the file came from and a hash
//     of exactly what was written (theme-updates.json in the data folder). The
//     hash is taken over the parsed theme, not the bytes, so re-indenting a file
//     does not count as an edit and a changed colour does.
//   - Files installed before this tracking existed are adopted the first time
//     the gallery is read: a file whose id is a gallery id and whose colours
//     match the gallery is tracked from then on. One that differs could be an
//     older version or the owner's own edits, and there is no way to tell them
//     apart, so it is treated as edited — never replaced without asking.
//   - Whenever the gallery index arrives (the panel's fetch, or the once-a-day
//     background check below), every tracked theme is compared with it. With
//     "Update themes automatically" on (the default) a new version the owner has
//     not edited is written straight away through the same path an install
//     uses, and the theme on screen repaints itself. A theme the owner edited is
//     only marked: the Community themes panel tags it, and A asks once more
//     before replacing the edits, keeping a copy of them.
//
// The hash comparison is the source of truth. The optional "updated" date an
// entry may carry is only shown to the user; a typo there cannot hold back or
// force an update.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	themeUpdateStateName = "theme-updates.json"
	// themeAutoUpdateKey is Settings → Display & theme → Update themes
	// automatically. It is on unless the owner switches it off.
	themeAutoUpdateKey = "themeAutoUpdate"
	// themeUpdateUnreadable stands in for the hash of a file that exists but
	// no longer parses as a theme: it can never equal a real hash, so such a
	// file counts as edited and is not overwritten without asking.
	themeUpdateUnreadable = "unreadable"
	// The background check: first look a little after start-up (the network
	// and the boot work settle first), then poll cheaply. A poll only reads
	// the connectivity flag; the gallery is fetched at most once a day, and a
	// failed fetch is not retried for half an hour.
	themeUpdateStartDelay = 2 * time.Minute
	themeUpdatePoll       = 2 * time.Minute
	themeUpdateInterval   = 24 * time.Hour
	themeUpdateRetryAfter = 30 * time.Minute
	// themeUpdateConfirmWindow is how long the "press A again" for replacing
	// edits stays armed.
	themeUpdateConfirmWindow = 6 * time.Second
)

// themeUpdateKind is what the gallery says about one installed theme.
type themeUpdateKind int

const (
	// themeUpdateNone: not installed, not a gallery theme, or edited on the
	// device while the gallery has nothing newer than what was installed.
	themeUpdateNone themeUpdateKind = iota
	// themeUpdateCurrent: the file is the gallery's current version.
	themeUpdateCurrent
	// themeUpdateAvailable: a newer version is published and the file is
	// still what JukaHub wrote, so it may be replaced.
	themeUpdateAvailable
	// themeUpdateEdited: a newer version is published, but the file was
	// changed on the device (or predates tracking), so only a press updates it.
	themeUpdateEdited
)

// themeUpdateRecord is one tracked theme in the state file.
type themeUpdateRecord struct {
	// Hash is the payload JukaHub last wrote for this theme. It is empty for a
	// file adopted from before tracking that did not match the gallery.
	Hash string `json:"hash"`
	// Offered is the gallery version the owner was last told about, so a
	// waiting update is announced once rather than every day.
	Offered string `json:"offered,omitempty"`
}

type themeUpdateStateFile struct {
	Version int `json:"version"`
	// CheckedAt is the last successful gallery read (RFC 3339). The clock may
	// be wrong before network time, so the scheduler also keeps its own
	// in-memory timer.
	CheckedAt string                       `json:"checkedAt,omitempty"`
	Themes    map[string]themeUpdateRecord `json:"themes"`
}

var themeUpdateState struct {
	mu     sync.Mutex
	loaded bool
	file   themeUpdateStateFile
	// status is the last comparison, per gallery id, for the panel's tags;
	// pending counts the ids with a version waiting.
	status    map[string]themeUpdateKind
	pending   int
	lastFetch time.Time
}

// themeUpdateSaveMu orders the state file's writes: each write marshals the
// state while holding it, so the last write to land is always the newest.
var themeUpdateSaveMu sync.Mutex

var themeUpdateSchedulerOnce sync.Once

// themeUpdateArmed is the edited theme whose replacement is waiting for the
// second A. It is only touched on the UI thread.
var themeUpdateArmed struct {
	id string
	at time.Time
}

// communityThemePayload is the theme file an entry installs as: its own theme
// object, named after the entry when the object has no name.
func communityThemePayload(entry communityTheme) themeFile {
	file := entry.Theme
	if strings.TrimSpace(file.Name) == "" {
		file.Name = strings.TrimSpace(entry.Name)
	}
	return file
}

// themePayloadHash is the sha256 of a theme's canonical JSON. encoding/json
// writes struct fields in declaration order, so the same theme always gives
// the same hash however its file was indented.
func themePayloadHash(file themeFile) string {
	data, err := json.Marshal(file)
	if err != nil {
		return themeUpdateUnreadable
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// themeInstalledHash hashes themes/<id>.json as it is on disk now. ok is false
// when there is no such file; a file that does not parse hashes to
// themeUpdateUnreadable.
func themeInstalledHash(id string) (hash string, ok bool) {
	// The id names a file under themes/: only a slug can (the backup and the
	// install reach the disk through here or communityThemeSlug).
	if slug, err := communityThemeSlug(id); err != nil || slug != id {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(themeDir, id+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false
		}
		return themeUpdateUnreadable, true
	}
	var file themeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return themeUpdateUnreadable, true
	}
	return themePayloadHash(file), true
}

// themeUpdateDecide is the whole policy. installed is the file's hash now,
// recorded the hash JukaHub wrote ("" when unknown) and catalog the gallery's.
// Only a file that is still exactly what JukaHub wrote may be replaced without
// asking; anything else that differs from the gallery is the owner's. A file
// the owner edited while the gallery still has the version JukaHub wrote has
// nothing waiting: it is the owner's theme, not an update to offer.
func themeUpdateDecide(installed, recorded, catalog string) themeUpdateKind {
	switch {
	case installed == catalog:
		return themeUpdateCurrent
	case recorded != "" && installed == recorded:
		return themeUpdateAvailable
	case recorded != "" && catalog == recorded:
		return themeUpdateNone
	default:
		return themeUpdateEdited
	}
}

func themeUpdateStatePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, themeUpdateStateName)
}

// themeUpdateLoadLocked reads the state file once. A missing or damaged file
// is an empty state: the next gallery read adopts the installed themes again.
func themeUpdateLoadLocked() {
	if themeUpdateState.loaded {
		return
	}
	themeUpdateState.loaded = true
	if path := themeUpdateStatePath(); path != "" {
		_, _ = readJSONWithBackup(path, &themeUpdateState.file)
	}
	if themeUpdateState.file.Themes == nil {
		themeUpdateState.file.Themes = make(map[string]themeUpdateRecord)
	}
}

// themeUpdateEnsureLoaded reads the state file without holding the lock, so
// the panel's hints (which take it every frame) never wait on the SD card.
// The background paths call it first; their themeUpdateLoadLocked is then a
// no-op.
func themeUpdateEnsureLoaded() {
	themeUpdateState.mu.Lock()
	loaded := themeUpdateState.loaded
	themeUpdateState.mu.Unlock()
	if loaded {
		return
	}
	var file themeUpdateStateFile
	if path := themeUpdateStatePath(); path != "" {
		_, _ = readJSONWithBackup(path, &file)
	}
	themeUpdateState.mu.Lock()
	defer themeUpdateState.mu.Unlock()
	if themeUpdateState.loaded {
		return
	}
	themeUpdateState.loaded = true
	themeUpdateState.file = file
	if themeUpdateState.file.Themes == nil {
		themeUpdateState.file.Themes = make(map[string]themeUpdateRecord)
	}
}

// themeUpdateSaveAsync writes the state off the calling thread: an install runs
// on the UI thread, and the write fsyncs on the SD card.
func themeUpdateSaveAsync() {
	go func() {
		path := themeUpdateStatePath()
		if path == "" {
			return
		}
		themeUpdateSaveMu.Lock()
		defer themeUpdateSaveMu.Unlock()
		themeUpdateState.mu.Lock()
		themeUpdateState.file.Version = 1
		data, err := json.MarshalIndent(themeUpdateState.file, "", "  ")
		themeUpdateState.mu.Unlock()
		if err != nil {
			return
		}
		if err := AtomicWrite(path, data, 0o644); err != nil {
			logPatch("community themes: update state write failed: %v", err)
		}
	}()
}

func themeUpdateRecountLocked() {
	n := 0
	for _, kind := range themeUpdateState.status {
		if kind == themeUpdateAvailable || kind == themeUpdateEdited {
			n++
		}
	}
	themeUpdateState.pending = n
}

// themeUpdateNoteInstalled records what an install (or an update) just wrote.
// writeCommunityTheme calls it, so every write is tracked.
func themeUpdateNoteInstalled(id string, file themeFile) {
	themeUpdateEnsureLoaded()
	themeUpdateState.mu.Lock()
	themeUpdateLoadLocked()
	themeUpdateState.file.Themes[id] = themeUpdateRecord{Hash: themePayloadHash(file)}
	if themeUpdateState.status == nil {
		themeUpdateState.status = make(map[string]themeUpdateKind)
	}
	themeUpdateState.status[id] = themeUpdateCurrent
	themeUpdateRecountLocked()
	themeUpdateState.mu.Unlock()
	themeUpdateSaveAsync()
}

// themeUpdatesPending is how many installed gallery themes have a newer
// version waiting (edited ones included). It reads the last comparison, so it
// is cheap enough for a tile badge.
func themeUpdatesPending() int {
	themeUpdateState.mu.Lock()
	defer themeUpdateState.mu.Unlock()
	return themeUpdateState.pending
}

// themeUpdateStatusOf is the last comparison's verdict for one gallery id.
func themeUpdateStatusOf(id string) themeUpdateKind {
	themeUpdateState.mu.Lock()
	defer themeUpdateState.mu.Unlock()
	return themeUpdateState.status[id]
}

// themeUpdateScan compares the gallery with the installed files, adopts files
// from before tracking, and returns the updates that may be applied (clean)
// and the ones waiting behind the owner's edits. fromNetwork stamps the check.
// It reads files, so it runs off the UI thread.
func themeUpdateScan(entries []communityTheme, fromNetwork bool) (clean, edited []communityTheme) {
	type found struct {
		entry              communityTheme
		installed, catalog string
	}
	var installed []found
	for _, entry := range entries {
		// A built-in id is never installed from the gallery
		// (installCommunityTheme refuses it); its file is not ours.
		if builtinThemeIDs[entry.ID] {
			continue
		}
		hash, ok := themeInstalledHash(entry.ID)
		if !ok {
			continue
		}
		installed = append(installed, found{entry: entry, installed: hash, catalog: themePayloadHash(communityThemePayload(entry))})
	}

	themeUpdateEnsureLoaded()
	themeUpdateState.mu.Lock()
	themeUpdateLoadLocked()
	changed := false
	if fromNetwork {
		themeUpdateState.file.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		themeUpdateState.lastFetch = time.Now()
		changed = true
	}
	present := make(map[string]bool, len(installed))
	status := make(map[string]themeUpdateKind, len(installed))
	for _, f := range installed {
		id := f.entry.ID
		present[id] = true
		rec, tracked := themeUpdateState.file.Themes[id]
		if !tracked {
			// Adoption: a matching file is ours from now on; a different one
			// keeps an empty hash, which reads as edited.
			if f.installed == f.catalog {
				rec.Hash = f.catalog
			}
			changed = true
		}
		kind := themeUpdateDecide(f.installed, rec.Hash, f.catalog)
		if kind == themeUpdateCurrent && rec.Hash != f.catalog {
			// The owner's file now matches the gallery (or an adopted one was
			// updated by hand): track it as ours again.
			rec.Hash = f.catalog
			changed = true
		}
		themeUpdateState.file.Themes[id] = rec
		status[id] = kind
		switch kind {
		case themeUpdateAvailable:
			clean = append(clean, f.entry)
		case themeUpdateEdited:
			edited = append(edited, f.entry)
		}
	}
	// A gallery theme whose file was deleted is no longer tracked, so a file
	// made by hand under the same name later is not mistaken for ours.
	for _, entry := range entries {
		if _, tracked := themeUpdateState.file.Themes[entry.ID]; tracked && !present[entry.ID] {
			delete(themeUpdateState.file.Themes, entry.ID)
			changed = true
		}
	}
	themeUpdateState.status = status
	themeUpdateRecountLocked()
	themeUpdateState.mu.Unlock()
	if changed {
		themeUpdateSaveAsync()
	}
	return clean, edited
}

// themeUpdateEvaluate compares and hands the result to the UI thread, which
// owns the config, the theme registry and the files the loader reads.
func themeUpdateEvaluate(entries []communityTheme, fromNetwork bool) {
	// An empty index from the network still counts as the day's check, or the
	// scheduler would fetch it again every half hour.
	if len(entries) == 0 && !fromNetwork {
		return
	}
	clean, edited := themeUpdateScan(entries, fromNetwork)
	if len(clean) == 0 && len(edited) == 0 {
		return
	}
	tbxPost(func() { themeUpdateApplyPending(appConfig, clean, edited) })
}

// themeUpdatesCatalogArrived is the gallery fetch's success hook
// (startThemeCatalogLoad). It runs on its own goroutine.
func themeUpdatesCatalogArrived(entries []communityTheme) {
	themeUpdateEvaluate(entries, true)
}

// themeUpdateRescan compares against the index already in memory (or the
// cached copy), without the network: the panel's tags are right even offline.
func themeUpdateRescan() {
	entries, _, _ := communityThemeEntries()
	if len(entries) == 0 {
		entries, _ = readThemeCatalogCache()
	}
	themeUpdateEvaluate(entries, false)
}

// themeUpdateCheckNow re-checks at once without blocking: the cached index
// first, then a fresh fetch when the device is online.
func themeUpdateCheckNow() {
	go func() {
		themeUpdateRescan()
		if IsOnline() && safeModeAllows(SafeFeatureNetwork) {
			startThemeCatalogLoad(true)
		}
	}()
}

// themeUpdateCheckDue reports whether a day has passed since the last gallery
// read, by the wall clock. A missing, garbled, future or pre-2020 stamp (a
// handheld that booted before network time) allows the check; the scheduler's
// in-memory timer then keeps it to once a day for this run.
func themeUpdateCheckDue(last string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	if now.Before(t) || t.Before(minSaneClock) || now.Before(minSaneClock) {
		return true
	}
	return now.Sub(t) >= themeUpdateInterval
}

// startThemeUpdateScheduler starts the once-a-day background check. It is
// idempotent. The poll only reads the connectivity flag, so joining Wi-Fi is
// noticed within a couple of minutes, and nothing is fetched offline, during a
// game, in safe mode or with JUKAHUB_NO_AUTOUPDATE set.
func startThemeUpdateScheduler() {
	themeUpdateSchedulerOnce.Do(func() {
		if !safeModeAllows(SafeFeatureNetwork) || strings.TrimSpace(os.Getenv("JUKAHUB_NO_AUTOUPDATE")) != "" {
			return
		}
		go func() {
			time.Sleep(themeUpdateStartDelay)
			// The counts are right from the cached index before any fetch.
			themeUpdateRescan()
			var lastTry time.Time
			for {
				if themeUpdateFetchDue(time.Now(), lastTry) {
					lastTry = time.Now()
					startThemeCatalogLoad(true)
				}
				time.Sleep(themeUpdatePoll)
			}
		}()
	})
}

// themeUpdateFetchDue is the scheduler's gate.
func themeUpdateFetchDue(now, lastTry time.Time) bool {
	if !IsOnline() || gameScreenSuspended() {
		return false
	}
	if !lastTry.IsZero() && now.Sub(lastTry) < themeUpdateRetryAfter {
		return false
	}
	themeUpdateEnsureLoaded()
	themeUpdateState.mu.Lock()
	themeUpdateLoadLocked()
	last := themeUpdateState.file.CheckedAt
	lastFetch := themeUpdateState.lastFetch
	themeUpdateState.mu.Unlock()
	if !lastFetch.IsZero() && now.Sub(lastFetch) < themeUpdateInterval {
		return false
	}
	return themeUpdateCheckDue(last, now)
}

// themeUpdateKindNow is the verdict for one entry from the file as it is right
// now, for a press: the cached status may predate an edit made since.
func themeUpdateKindNow(entry communityTheme) themeUpdateKind {
	installed, ok := themeInstalledHash(entry.ID)
	if !ok || isBuiltinThemeID(entry.ID) {
		return themeUpdateNone
	}
	themeUpdateEnsureLoaded()
	themeUpdateState.mu.Lock()
	themeUpdateLoadLocked()
	rec := themeUpdateState.file.Themes[entry.ID]
	themeUpdateState.mu.Unlock()
	// An untracked file has an empty hash: it is current when it matches the
	// gallery and edited otherwise, exactly as adoption would decide.
	return themeUpdateDecide(installed, rec.Hash, themePayloadHash(communityThemePayload(entry)))
}

// themeUpdateInstall writes the new version through the install path and, if
// it is the theme on screen, repaints with it. It does not switch themes: an
// update of a theme that is not in use stays a file until it is picked.
func themeUpdateInstall(config *Config, entry communityTheme) error {
	id, err := writeCommunityTheme(entry)
	if err != nil {
		return err
	}
	if config != nil && config.Variables.Custom != nil && themePresetName(config) == id {
		applyThemePreset(config, id, false)
	}
	return nil
}

// themeUpdateBackupEdits keeps the owner's edited file as
// themes/<id>.edited.json.bak. The install's own write also leaves
// themes/<id>.json.bak, but that one is replaced by the next update; this copy
// is only written when edits are about to be replaced. The loader reads .json
// files only, so neither backup shows up as a theme.
func themeUpdateBackupEdits(id string) error {
	data, err := os.ReadFile(filepath.Join(themeDir, id+".json"))
	if err != nil {
		return err
	}
	return writeFileSynced(filepath.Join(themeDir, id+".edited.json.bak"), data, 0o644)
}

func communityThemeTitle(entry communityTheme) string {
	if name := strings.TrimSpace(entry.Name); name != "" {
		return name
	}
	return entry.ID
}

// themeUpdateApplyPending runs on the UI thread after a comparison. With the
// setting on, clean updates are written now; everything else waiting is
// announced once per new version.
func themeUpdateApplyPending(config *Config, clean, edited []communityTheme) {
	if config == nil {
		return
	}
	offer := make([]communityTheme, 0, len(clean)+len(edited))
	offer = append(offer, edited...)
	var updated []string
	// Safe mode holds every automatic update back; the panel's A still works.
	if themeAutoUpdateOn(config) && safeModeAllows(SafeFeatureUpdates) {
		for _, entry := range clean {
			// Re-read the file: it may have been edited, or updated by an
			// earlier comparison, since the scan.
			if themeUpdateKindNow(entry) != themeUpdateAvailable {
				continue
			}
			if err := themeUpdateInstall(config, entry); err != nil {
				logPatch("community themes: update of %s failed: %v", entry.ID, err)
				continue
			}
			logPatch("community themes: updated %s", entry.ID)
			updated = append(updated, communityThemeTitle(entry))
		}
	} else {
		offer = append(offer, clean...)
	}
	switch len(updated) {
	case 0:
	case 1:
		ShowToast("Theme “"+updated[0]+"” updated", ToastKindSuccess)
	default:
		ShowToast(fmt.Sprintf("%d themes updated", len(updated)), ToastKindSuccess)
	}
	themeUpdateOffer(offer)
}

// themeUpdateOffer says once per new version that an update waits in the
// Community themes panel.
func themeUpdateOffer(entries []communityTheme) {
	var fresh []communityTheme
	themeUpdateState.mu.Lock()
	themeUpdateLoadLocked()
	for _, entry := range entries {
		rec, tracked := themeUpdateState.file.Themes[entry.ID]
		if !tracked {
			continue
		}
		catalog := themePayloadHash(communityThemePayload(entry))
		if rec.Offered == catalog {
			continue
		}
		rec.Offered = catalog
		themeUpdateState.file.Themes[entry.ID] = rec
		fresh = append(fresh, entry)
	}
	themeUpdateState.mu.Unlock()
	if len(fresh) == 0 {
		return
	}
	themeUpdateSaveAsync()
	where := "Settings → Theme & look → Community themes"
	if len(fresh) == 1 {
		ShowToast("“"+communityThemeTitle(fresh[0])+"” has a new version — "+where, ToastKindInfo)
		return
	}
	ShowToast(fmt.Sprintf("%d themes have new versions — %s", len(fresh), where), ToastKindInfo)
}

// themeUpdateRowHint is the panel's tag for a theme with a version waiting, or
// "" to keep the panel's own hint.
func themeUpdateRowHint(id string) string {
	switch themeUpdateStatusOf(id) {
	case themeUpdateAvailable:
		return "A update"
	case themeUpdateEdited:
		return "Edited · A update"
	}
	return ""
}

// themeUpdateActivate handles A on a panel row with a version waiting and
// reports whether it did. A theme the owner edited needs a second A within a
// few seconds; their file is copied aside first.
func themeUpdateActivate(config *Config, entry communityTheme) bool {
	kind := themeUpdateKindNow(entry)
	if kind != themeUpdateAvailable && kind != themeUpdateEdited {
		return false
	}
	title := communityThemeTitle(entry)
	if kind == themeUpdateEdited {
		if themeUpdateArmed.id != entry.ID || time.Since(themeUpdateArmed.at) > themeUpdateConfirmWindow {
			themeUpdateArmed.id, themeUpdateArmed.at = entry.ID, time.Now()
			ShowToast("“"+title+"” was changed on this device. Press A again to replace your edits (a copy is kept).", ToastKindWarning)
			PlayNavSound()
			return true
		}
		if err := themeUpdateBackupEdits(entry.ID); err != nil {
			ShowToast("Couldn't keep a copy of your edits, so the theme was left alone: "+err.Error(), ToastKindWarning)
			return true
		}
	}
	themeUpdateArmed.id = ""
	if err := themeUpdateInstall(config, entry); err != nil {
		ShowToast("Couldn't update that theme: "+err.Error(), ToastKindWarning)
		return true
	}
	ShowToast("Theme “"+title+"” updated", ToastKindSuccess)
	PlayActivateSound()
	return true
}

// communityThemeUpdatedSuffix is " · updated 17 Nov 2026" for an entry with a
// valid "updated" date (YYYY-MM-DD), else "".
func communityThemeUpdatedSuffix(entry communityTheme) string {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(entry.Updated))
	if err != nil {
		return ""
	}
	return " · updated " + formatDate(nil, t)
}

// themeAutoUpdateOn reads the setting; it is on by default.
func themeAutoUpdateOn(c *Config) bool {
	return c != nil && customBool(c.Variables.Custom, themeAutoUpdateKey, true)
}

// themeAutoUpdateRow is the Settings → Display & theme switch. Turning it on
// applies whatever is waiting straight away.
func themeAutoUpdateRow() setRow {
	return setRow{
		ID: "themeAutoUpdate", Label: "Update themes automatically", Kind: setKindToggle,
		Detail: func(c *Config) string {
			switch n := themeUpdatesPending(); {
			case n == 1:
				return "1 gallery theme has a new version — see Community themes"
			case n > 1:
				return fmt.Sprintf("%d gallery themes have new versions — see Community themes", n)
			}
			return "Themes installed from the gallery get their author's new versions"
		},
		Get: themeAutoUpdateOn,
		Set: func(c *Config, on bool) {
			setStoreBool(c, themeAutoUpdateKey, on)
			setToast("Update themes automatically: " + setOnOff(on))
			if on {
				themeUpdateCheckNow()
			}
		},
	}
}
