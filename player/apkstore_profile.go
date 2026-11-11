package main

// apkstore_profile.go — "Add to Games": an APK becomes an AndroidPak game
// profile without anyone writing JSON.
//
// Game profiles (gameprofiles.go) are the launch path AndroidPak already has:
// triage, the memory pre-flight, staging to tmpfs, the runtime contract, the
// launch log and the first-run report all hang off a profile. So the store
// does not grow a second launcher; it writes one profile per APK into the
// local layer, <data>/game-profiles/local/apkstore-<id>.json, and the Game
// Profiles panel (Start → Games → Game Profiles) lists it like any other —
// so does the Games Library's Android row (game_library_androidgames.go), with the
// launcher icon saved here as the game's cover. The file name prefix is how
// the store knows which profiles it may take away again: a profile someone
// wrote by hand is shown as "in Games" and can be played, but Remove leaves
// it alone.
//
// Everything here touches the disk (and launching stages the game), so the
// store calls it from goroutines and posts the outcome back (apkstore.go).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	apkStoreProfilePrefix = "apkstore-"
	// apkStoreTitleMaxBytes is validateGameProfile's title limit (bytes, not
	// characters: a Chinese or Japanese label reaches it at 40 characters).
	apkStoreTitleMaxBytes = 120
)

// apkStoreProfileID derives a stable profile id from the package name (or the
// file name when the manifest could not be read): "apk-com.mojang.minecraftpe".
func apkStoreProfileID(info apkStoreInfo) string {
	base := strings.ToLower(strings.TrimSpace(info.Manifest.Package))
	if base == "" {
		base = strings.ToLower(strings.TrimSuffix(filepath.Base(info.Path), filepath.Ext(info.Path)))
	}
	var b strings.Builder
	b.WriteString("apk-")
	dash := false
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			dash = false
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	id := strings.TrimRight(b.String(), "-._")
	if id == "apk" || id == "" {
		id = "apk-game"
	}
	if len(id) > 64 {
		id = strings.TrimRight(id[:64], "-._")
	}
	return id
}

// apkStoreProfilePath is the file the store writes for id.
func apkStoreProfilePath(id string) string {
	return filepath.Join(gameProfilesDir(), "local", apkStoreProfilePrefix+id+".json")
}

// apkStoreProfileTarget reads which APK the store's profile id launches.
func apkStoreProfileTarget(id string) (string, bool) {
	raw, err := os.ReadFile(apkStoreProfilePath(id))
	if err != nil {
		return "", false
	}
	var p GameProfile
	if json.Unmarshal(stripUTF8BOM(raw), &p) != nil || strings.TrimSpace(p.Executable) == "" {
		return "", false
	}
	return filepath.Join(filepath.FromSlash(p.Dir), filepath.FromSlash(p.Executable)), true
}

// apkStoreProfileIDFor is the id Add writes for info: the package's id, unless
// the store's profile with that id already launches another APK that is still
// on the card (two versions of one game, say) — then a short hash of this
// file's path is added, so neither profile overwrites the other.
func apkStoreProfileIDFor(info apkStoreInfo) string {
	id := apkStoreProfileID(info)
	other, ok := apkStoreProfileTarget(id)
	if !ok || apkStorePathKey(other) == apkStorePathKey(info.Path) || !regularFile(other) {
		return id
	}
	sum := sha256.Sum256([]byte(apkStorePathKey(info.Path)))
	suffix := "-" + hex.EncodeToString(sum[:3])
	if len(id)+len(suffix) > 64 {
		id = strings.TrimRight(id[:64-len(suffix)], "-._")
	}
	return id + suffix
}

// apkStoreClipBytes cuts s to at most n bytes without splitting a character.
func apkStoreClipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut])
}

// apkStoreBuildProfile is the profile for an APK, validated the way the
// loader validates it. The APK must sit inside the folders game profiles may
// use (the card on a handheld), or the profile would load with an issue.
func apkStoreBuildProfile(info apkStoreInfo, roots []string) (GameProfile, error) {
	if !info.Tier.canAdd() {
		return GameProfile{}, errors.New("this APK cannot run on AndroidRT: " + info.Verdict)
	}
	dir := filepath.Dir(info.Path)
	if !filepath.IsAbs(dir) {
		return GameProfile{}, fmt.Errorf("%s is not an absolute path", info.Path)
	}
	if _, err := containedDir(dir, roots); err != nil {
		return GameProfile{}, fmt.Errorf("move the APK into %s first: game profiles only launch from there", strings.Join(roots, " or "))
	}
	title := apkStoreClipBytes(strings.TrimSpace(info.Label), apkStoreTitleMaxBytes)
	notes := "Added by the APK Store from " + filepath.Base(info.Path) + ". Experimental: AndroidRT has not run a game " +
		"on a handheld yet; if this one stops, its launch log names what is missing (docs/ANDROID.md)."
	p := GameProfile{
		SchemaVersion: gameProfileSchemaVersion,
		ID:            apkStoreProfileIDFor(info),
		Title:         title,
		Runtime:       gameRuntimeAndroidPak,
		Dir:           dir,
		Executable:    filepath.Base(info.Path),
		MainLib:       info.MainLib,
		Notes:         notes,
	}
	if err := validateGameProfile(&p); err != nil {
		return GameProfile{}, err
	}
	return p, nil
}

// apkStoreAddProfile writes the profile and drops the profile cache, so the
// Game Profiles panel and the store see it at once.
func apkStoreAddProfile(info apkStoreInfo) (GameProfile, error) {
	p, err := apkStoreBuildProfile(info, gameProfileRoots())
	if err != nil {
		return GameProfile{}, err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return GameProfile{}, err
	}
	if err := ensureGameProfilesLayout(); err != nil {
		return GameProfile{}, err
	}
	if err := AtomicWrite(apkStoreProfilePath(p.ID), append(data, '\n'), 0o644); err != nil {
		return GameProfile{}, err
	}
	invalidateGameProfileCache()
	// The launcher icon becomes the game's cover in the Games Library (best
	// effort: a game without one shows the library's placeholder).
	if err := apkStoreSaveIcon(info.Icon, androidLibraryArtPath(info.Path)); err != nil {
		logDebug("apkstore", "cover for %s: %v", p.ID, err)
	}
	return p, nil
}

// apkStoreSaveIcon writes an APK's launcher icon as a PNG at art, where the
// Games Library looks for the game's cover. Nothing to write is not an error.
// The store's Add and the library's scan can both write one cover at once, so
// each write goes through a temporary file of its own and a rename
// (writeFileReplace): a shared ".tmp" let one writer truncate the other's.
func apkStoreSaveIcon(icon *image.NRGBA, art string) error {
	if icon == nil || strings.TrimSpace(art) == "" {
		return nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, icon); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(art), 0o755); err != nil {
		return err
	}
	return writeFileReplace(art, buf.Bytes())
}

// apkStoreRemoveProfile deletes the store's own profile for id (and the
// backup AtomicWrite keeps beside it), and the cover it saved for the Games
// Library. A missing file is not an error.
func apkStoreRemoveProfile(id string) error {
	apk, hasAPK := apkStoreProfileTarget(id)
	p := apkStoreProfilePath(id)
	err := os.Remove(p)
	_ = os.Remove(p + ".bak")
	invalidateGameProfileCache()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if hasAPK {
		// After the profile: a cover under the APK's bare name stays while
		// another of the store's games still has that name.
		androidLibraryRemoveCover(apk)
	}
	return nil
}

// apkStoreGameState is how an APK relates to the game profiles.
type apkStoreGameState struct {
	ID      string // the profile's id, "" when the APK is in none
	Ours    bool   // the store wrote it (Remove is offered)
	Ready   bool   // the profile can launch now
	Issue   string // why not
	Title   string
	LogPath string // the launch log, when a run left one
	// LastRun says how the most recent run ended, read from its log: the
	// first thing AndroidRT was missing when it failed ("" without a log).
	LastRun string
}

// apkStoreGameStates maps every APK path to the profile that launches it.
// It reads the loaded catalog (gameProfileEntries builds it when needed), so
// it runs off the render thread.
func apkStoreGameStates(apks []apkStoreInfo) map[string]apkStoreGameState {
	out := map[string]apkStoreGameState{}
	if len(apks) == 0 {
		return out
	}
	byExe := map[string]gameProfileEntry{}
	byID := map[string]gameProfileEntry{}
	for _, e := range gameProfileEntries() {
		if e.Profile.Runtime != gameRuntimeAndroidPak {
			continue
		}
		byID[e.Profile.ID] = e
		if e.Executable != "" {
			byExe[apkStorePathKey(e.Executable)] = e
		}
	}
	for _, info := range apks {
		id := apkStoreProfileID(info)
		e, ok := byID[id]
		if !ok || !apkStoreSameFile(e, info.Path) {
			e, ok = byExe[apkStorePathKey(apkStoreRealPath(info.Path))]
		}
		if !ok {
			continue
		}
		st := apkStoreGameState{ID: e.Profile.ID, Ready: e.ready(), Issue: e.Issue, Title: e.Profile.Title}
		st.Ours = regularFile(apkStoreProfilePath(e.Profile.ID))
		logPath := filepath.Join(gameProfilesLogsDir(), e.Profile.ID+".log")
		if regularFile(logPath) {
			st.LogPath = logPath
			st.LastRun = apkStoreLastRun(e.Profile.ID, logPath)
		}
		out[info.Path] = st
	}
	return out
}

// apkStoreSameFile reports whether a loaded profile launches the file at p.
// A profile whose paths did not resolve (it has an issue) matches by its own
// dir and executable instead.
func apkStoreSameFile(e gameProfileEntry, p string) bool {
	if e.Executable != "" {
		return apkStorePathKey(e.Executable) == apkStorePathKey(apkStoreRealPath(p))
	}
	want := filepath.Join(filepath.FromSlash(e.Profile.Dir), filepath.FromSlash(e.Profile.Executable))
	return apkStorePathKey(want) == apkStorePathKey(p)
}

// apkStoreRealPath resolves links, so a profile resolved through a symlink
// still matches the file the scan found.
func apkStoreRealPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// apkStorePathKey compares paths the way the platform does.
func apkStorePathKey(p string) string {
	p = filepath.Clean(p)
	if IsWindows() {
		return strings.ToLower(p)
	}
	return p
}

// apkStoreFindEntry returns the loaded profile with id, rebuilt from disk.
// Rebuilding reads every profile and triages every AndroidPak APK, so it runs
// off the render thread (apkStorePlay's goroutine).
func apkStoreFindEntry(id string) (gameProfileEntry, bool) {
	invalidateGameProfileCache()
	for _, e := range gameProfileEntries() {
		if e.Profile.ID == id {
			return e, true
		}
	}
	return gameProfileEntry{}, false
}

// apkStoreLastRun words the most recent run in a profile's launch log: it
// ended normally, or what AndroidRT was missing when it did not (the same
// reading the failure toast uses, androidPakRunMessage).
func apkStoreLastRun(id, logPath string) string {
	text := androidPakLastRunLog(id)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	exit := ""
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "exit: "); ok {
			exit = strings.TrimSpace(rest)
		}
	}
	switch exit {
	case "0":
		return "The last run ended normally."
	case "":
		return "The last run left no exit line — it may still be running, or the device stopped mid-game."
	}
	report := parseAndroidPakRunReport(text)
	msg, _ := androidPakRunMessage(id, report, logPath)
	// The generic wording counts the undefined symbols; the first one is the
	// lead worth showing on the sheet.
	if len(report.Undefined) > 0 && !strings.Contains(msg, report.Undefined[0]) {
		msg += " First missing symbol: " + report.Undefined[0] + "."
	}
	return msg
}
