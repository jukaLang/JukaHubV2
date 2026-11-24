package main

// profiles_store.go — profiles for a shared handheld: the list of people, where
// each one's things live, and which stores and settings belong to a person.
//
// The data JukaHub has always kept IS the owner's profile, at the paths it has
// always used: nothing is moved, copied or rewritten for it, and with no other
// profile created JukaHub behaves exactly as before — no prompt, no chip, no
// Start menu row. Another person's profile keeps its own copy of the
// per-person stores under <data>/profiles/<id>/; a Guest session keeps them
// under <data>/profiles/.guest/, which is wiped when the session ends (a switch
// away, or the next start).
//
// Per person: the Continue list (game-recents.json), the game history, the
// console Home's recent apps, favourites, playtime and JukaHub's trophies,
// the "recently played" marks, the words the keyboard learned, browser
// bookmarks, RetroArch saves and states (profiles_saves.go) and the personal
// settings (profilesPrefKeys: theme, wallpaper, Home layout, language, text
// size and the rest of Accessibility, interface sounds, background music, the
// name Home greets). Everything else stays device-wide: Wi-Fi, VPN, DNS,
// Bluetooth, power, volume and brightness, the A/B layout fix, installed
// packages, emulators and their options, collections, notes, alarms, chat,
// and the RetroAchievements sign-in (the owner's; other profiles play with
// RetroAchievements off so nobody else's unlocks land on the owner's account).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

const (
	profilesDirName   = "profiles"
	profilesFileName  = "profiles.json"
	profilesDataName  = "profile.json"
	profilesGuestDir  = ".guest"
	profilesTrashName = ".deleted"
	// profilesGuestID is Guest's id in the picker and in profilesActive. It is
	// never written to profiles.json: a Guest session never outlives a start.
	profilesGuestID = "guest"
	profilesVersion = 1
	// profilesMax is how many profiles there can be besides the owner's.
	profilesMax     = 7
	profilesNameMax = 24
)

// profilesColours are the avatar colours, by index; the owner starts on 0.
var profilesColours = []struct {
	name string
	hex  string
}{
	{"Blue", "#3B82F6"}, {"Green", "#22C55E"}, {"Orange", "#F97316"}, {"Pink", "#EC4899"},
	{"Purple", "#8B5CF6"}, {"Teal", "#14B8A6"}, {"Red", "#EF4444"}, {"Yellow", "#EAB308"},
}

// profileEntry is one person besides the owner.
type profileEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Colour int    `json:"colour"`
}

// profilesDoc is profiles.json. It holds no personal data — only the list, the
// owner's label and colour, the start-up question and who played last.
type profilesDoc struct {
	Version     int            `json:"version"`
	OwnerName   string         `json:"owner_name,omitempty"`
	OwnerColour int            `json:"owner_colour"`
	Profiles    []profileEntry `json:"profiles"`
	AskAtStart  bool           `json:"ask_at_start,omitempty"`
	// Active is the profile in use when the list was last written ("" is the
	// owner). Guest is never recorded.
	Active string `json:"active,omitempty"`
}

// profileTyped holds the personal settings that live in typed fields rather
// than in the Custom map.
type profileTyped struct {
	Button        RGB    `json:"button"`
	Label         RGB    `json:"label"`
	Input         RGB    `json:"input"`
	Name          string `json:"name,omitempty"`
	ReducedMotion bool   `json:"reduced_motion,omitempty"`
	Clock12       bool   `json:"clock12,omitempty"`
	DynamicBg     bool   `json:"dynamic_bg,omitempty"`
	// Wallpaper is BackgroundImage, which jukauser.json does not keep (the
	// picker's choice is restored from wallpaperSavedKey); a profile keeps it so
	// a switch shows the person's wallpaper at once.
	Wallpaper string `json:"wallpaper,omitempty"`
}

// profileData is one person's settings and jukauser.json-resident library
// (<data>/profiles/<id>/profile.json, and the owner's in-memory copy while
// someone else plays).
type profileData struct {
	Version   int                    `json:"version"`
	Keys      map[string]interface{} `json:"keys,omitempty"`
	Typed     profileTyped           `json:"typed"`
	Favorites *FavoritesStore        `json:"favorites,omitempty"`
}

// profilesActiveInfo is who is playing now. nil (or an empty ID) is the owner.
type profilesActiveInfo struct {
	ID     string
	Name   string
	Colour int
	Dir    string // the profile's folder; "" for the owner
	Guest  bool
}

// profilesActive is read from any goroutine (the store path wrappers, the
// launch hooks, the jukauser.json writer); only the main thread stores it.
var profilesActive atomic.Pointer[profilesActiveInfo]

// profilesState is the list as loaded. Guarded by mu; main thread mostly.
var profilesState struct {
	mu     sync.Mutex
	loaded bool
	doc    profilesDoc
}

// profilesRootOverride points the profiles folder elsewhere in tests.
var profilesRootOverride string

// profilesRoot is <data>/profiles, or "" when there is no data folder.
func profilesRoot() string {
	if profilesRootOverride != "" {
		return profilesRootOverride
	}
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, profilesDirName)
}

// profilesValidID reports whether id is one profilesNewID could have made:
// "p" and eight lowercase hex digits. It is used as a folder name, so an id
// read from the file must pass this before it is joined to a path.
func profilesValidID(id string) bool {
	if len(id) != 9 || id[0] != 'p' {
		return false
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// profilesNewID makes a fresh id that is not in taken.
func profilesNewID(taken map[string]bool) string {
	for i := 0; i < 16; i++ {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			break
		}
		id := "p" + hex.EncodeToString(b[:])
		if !taken[id] {
			return id
		}
	}
	// Practically unreachable; still a valid, unused id.
	for n := 0; ; n++ {
		id := "p" + strings.Repeat("0", 7) + string(rune('0'+n%10))
		if !taken[id] {
			return id
		}
	}
}

// profilesCleanName trims a typed name: no control characters, at most
// profilesNameMax characters.
func profilesCleanName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > profilesNameMax {
		out = strings.TrimSpace(string([]rune(out)[:profilesNameMax]))
	}
	return out
}

// profilesClampColour keeps a colour index on the palette.
func profilesClampColour(i int) int {
	if i < 0 || i >= len(profilesColours) {
		return 0
	}
	return i
}

// profilesParse reads profiles.json and brings it to this version: a file
// without a version (hand-made, or from a test build) is read as version 1;
// an id that could escape the folder, a duplicate, an empty name, a colour off
// the palette, an entry over the limit and an Active that names nobody are
// mended. It reports whether the result differs from the file, so the caller
// writes the mended list back. A file from a newer version is read for what
// this version knows. Pure.
func profilesParse(data []byte) (profilesDoc, bool, error) {
	var doc profilesDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return profilesDoc{Version: profilesVersion}, false, err
	}
	changed := false
	if doc.Version < profilesVersion {
		doc.Version = profilesVersion
		changed = true
	}
	if c := profilesClampColour(doc.OwnerColour); c != doc.OwnerColour {
		doc.OwnerColour, changed = c, true
	}
	if n := profilesCleanName(doc.OwnerName); n != doc.OwnerName {
		doc.OwnerName, changed = n, true
	}
	seen := map[string]bool{}
	kept := make([]profileEntry, 0, len(doc.Profiles))
	for _, p := range doc.Profiles {
		if !profilesValidID(p.ID) || seen[p.ID] || len(kept) >= profilesMax {
			changed = true
			continue
		}
		seen[p.ID] = true
		name := profilesCleanName(p.Name)
		if name == "" {
			name = "Player " + string(rune('2'+len(kept)))
		}
		colour := profilesClampColour(p.Colour)
		if name != p.Name || colour != p.Colour {
			changed = true
		}
		kept = append(kept, profileEntry{ID: p.ID, Name: name, Colour: colour})
	}
	doc.Profiles = kept
	if doc.Active != "" && !seen[doc.Active] {
		doc.Active, changed = "", true
	}
	return doc, changed, nil
}

// profilesLoadLocked reads the list once. A missing file is "no profiles";
// a damaged one falls back to its .bak (AtomicWrite keeps one).
func profilesLoadLocked() {
	if profilesState.loaded {
		return
	}
	profilesState.loaded = true
	profilesState.doc = profilesDoc{Version: profilesVersion}
	root := profilesRoot()
	if root == "" {
		return
	}
	path := filepath.Join(root, profilesFileName)
	var raw json.RawMessage
	if _, err := readJSONWithBackup(path, &raw); err != nil {
		if !os.IsNotExist(err) {
			logWarn("profiles", "ignoring %s: %v", path, err)
		}
		return
	}
	doc, changed, err := profilesParse(raw)
	if err != nil {
		logWarn("profiles", "ignoring %s: %v", path, err)
		return
	}
	profilesState.doc = doc
	if changed {
		profilesSaveLocked()
	}
}

// profilesSaveLocked writes the list (small; written on a change only).
func profilesSaveLocked() {
	root := profilesRoot()
	if root == "" {
		return
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		logWarn("profiles", "cannot create %s: %v", root, err)
		return
	}
	data, err := json.MarshalIndent(profilesState.doc, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(filepath.Join(root, profilesFileName), append(data, '\n'), 0o644); err != nil {
		logWarn("profiles", "cannot save the profile list: %v", err)
	}
}

// profilesSnapshot is a copy of the list.
func profilesSnapshot() profilesDoc {
	profilesState.mu.Lock()
	defer profilesState.mu.Unlock()
	profilesLoadLocked()
	doc := profilesState.doc
	doc.Profiles = append([]profileEntry(nil), doc.Profiles...)
	return doc
}

// profilesUpdate changes the list under the lock and writes it.
func profilesUpdate(fn func(doc *profilesDoc) error) error {
	profilesState.mu.Lock()
	defer profilesState.mu.Unlock()
	profilesLoadLocked()
	next := profilesState.doc
	next.Profiles = append([]profileEntry(nil), next.Profiles...)
	if err := fn(&next); err != nil {
		return err
	}
	profilesState.doc = next
	profilesSaveLocked()
	return nil
}

// profilesAny reports whether anyone besides the owner has a profile. With
// none, nothing about profiles shows anywhere but the Settings row. Asked
// every frame by the top bar's chip, so it copies nothing.
func profilesAny() bool {
	profilesState.mu.Lock()
	defer profilesState.mu.Unlock()
	profilesLoadLocked()
	return len(profilesState.doc.Profiles) > 0
}

// profilesFind returns the entry with id.
func profilesFind(doc profilesDoc, id string) (profileEntry, bool) {
	for _, p := range doc.Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return profileEntry{}, false
}

// profilesOwnerLabel is the owner's name in the list: the one stored with the
// list, else the name Home greets while the owner is playing, else "Owner".
func profilesOwnerLabel(doc profilesDoc) string {
	if doc.OwnerName != "" {
		return doc.OwnerName
	}
	if profilesCurrent() == nil && appConfig != nil {
		if n := profilesCleanName(userName(appConfig)); n != "" {
			return n
		}
	}
	return "Owner"
}

// profilesCurrent is who is playing, or nil for the owner.
func profilesCurrent() *profilesActiveInfo {
	if a := profilesActive.Load(); a != nil && a.ID != "" {
		return a
	}
	return nil
}

// profilesGuestActive reports whether a Guest session is on.
func profilesGuestActive() bool {
	a := profilesCurrent()
	return a != nil && a.Guest
}

// profilesCurrentName is the name of whoever is playing.
func profilesCurrentName() string {
	if a := profilesCurrent(); a != nil {
		return a.Name
	}
	return profilesOwnerLabel(profilesSnapshot())
}

// profilesDirFor is the folder that holds profile id's things.
func profilesDirFor(id string) string {
	root := profilesRoot()
	switch {
	case root == "":
		return ""
	case id == profilesGuestID:
		return filepath.Join(root, profilesGuestDir)
	case profilesValidID(id):
		return filepath.Join(root, id)
	}
	return ""
}

// ── Per-person stores in the data folder ─────────────────────────────────

// profilesRedirect is where a per-person store of the data folder lives for
// whoever is playing: the owner's own path, or the same file name in the
// profile's folder.
func profilesRedirect(ownerPath string) string {
	a := profilesCurrent()
	if a == nil || a.Dir == "" || ownerPath == "" {
		return ownerPath
	}
	return filepath.Join(a.Dir, filepath.Base(ownerPath))
}

func init() {
	// The stores that keep their file behind a replaceable path function are
	// pointed at the profile's folder by wrapping it; the owner's path is
	// unchanged. Tests that replace these functions replace the wrapper too,
	// and so see the owner's behaviour.
	recents, history, consoleRecents := gameRecentsFile, gameHistoryPath, homeConsoleRecentsFile
	words, bookmarks := vkLearnedPath, browserBookmarkPath
	gameRecentsFile = func() string { return profilesRedirect(recents()) }
	gameHistoryPath = func() string { return profilesRedirect(history()) }
	homeConsoleRecentsFile = func() string { return profilesRedirect(consoleRecents()) }
	vkLearnedPath = func() string { return profilesRedirect(words()) }
	browserBookmarkPath = func() string { return profilesRedirect(bookmarks()) }
}

// ── Which settings belong to a person ────────────────────────────────────

// profilesPrefKeys are the personal settings: the experience-pack set (theme,
// colours, Home arrangement, interface sounds, idle screen, wallpaper), the
// language, the typeface, the menu colour, the glass and the water under it,
// the Home tiles and tabs, background music, the name Home greets and every
// Accessibility setting. Everything not listed is the device's.
func profilesPrefKeys() []string {
	keys := append([]string(nil), xpackStateKeys()...)
	return append(keys, xpackUndoKey,
		uiLanguageKey, uiFontKey, menuColourKey, glassStyleKey, liquidKey, liquidTiltKey,
		colourBackgroundKey, colourSurfaceKey, colourTextKey, colourFocusKey, glassOpacityKey, animSpeedKey,
		homeTilesKey, homeTabsViewsKey, homeTabsViewsLibraryKey, homeTabsShownKey, homeTabsRememberKey, homeTabsPositionKey,
		homeTabsStartKey, shutdownScreenKey, shutdownPictureKey,
		bgmEnabledKey, bgmSourceKey, bgmVolumeKey, bgmSelectedKey,
		"reducedMotion", "clock12", "dynamicBg", "tspUsername",
		a11yKeyFontScale, a11yKeyContrast, a11yKeyColorblind, a11yKeySwapAB, a11yKeySwapXY,
		a11yKeyPrevTheme, a11yKeyOneHanded, a11yKeyChordBtn, a11yKeySlowRepeat, a11yKeyKeymap,
		a11yKeyReduceTransparency, a11yKeyIncreaseContrast, a11yKeyBoldText, a11yKeyMonoAudio,
		a11yKeyAudioChannels, a11yKeyAudioBalance, a11yKeyCaptionsAlways, a11yKeyCaptionSize, a11yKeyCaptionBackground,
		a11yKeyLongPress, a11yKeyRepeatDelay, a11yKeyRepeatRate, a11yKeyToastDuration,
		a11yKeyStickyChords, a11yKeyMagnifier, a11yKeyMagnifierZoom, a11yKeyLegibleFont,
		a11yKeyLegibleFace, a11yKeySwitchMode, a11yKeySwitchInterval)
}

// profilesLibraryKeys are the person's library inside jukauser.json: playtime
// and trophies, the "recently played" marks and the trophy cup's count.
func profilesLibraryKeys() []string {
	return []string{playtimeCustomKey, "recently_played", trophySeenKey}
}

// profilesPersonKeys is both lists.
func profilesPersonKeys() []string {
	return append(profilesPrefKeys(), profilesLibraryKeys()...)
}

// profilesTypedMirrors are the Custom spellings of the typed personal fields,
// dropped (in any case) before a person's typed values are put in place.
var profilesTypedMirrors = []string{"reducedMotion", "clock12", "dynamicBg", "tspUsername"}

// ── One person's file ────────────────────────────────────────────────────

// profilesReadData reads a profile's profile.json; ok is false when there is
// none yet.
func profilesReadData(dir string) (profileData, bool) {
	if dir == "" {
		return profileData{}, false
	}
	var d profileData
	if _, err := readJSONWithBackup(filepath.Join(dir, profilesDataName), &d); err != nil {
		if !os.IsNotExist(err) {
			logWarn("profiles", "cannot read %s: %v", dir, err)
		}
		return profileData{}, false
	}
	if d.Keys == nil {
		d.Keys = map[string]interface{}{}
	}
	return d, true
}

// profilesDataWrite serialises the writes of profile.json files.
var profilesDataWrite struct {
	mu   sync.Mutex
	last map[string][]byte
}

// profilesWriteData writes a profile's profile.json when it changed.
func profilesWriteData(dir string, d profileData) {
	if dir == "" {
		return
	}
	d.Version = profilesVersion
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return
	}
	profilesDataWrite.mu.Lock()
	defer profilesDataWrite.mu.Unlock()
	if prev, ok := profilesDataWrite.last[dir]; ok && string(prev) == string(data) {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logWarn("profiles", "cannot create %s: %v", dir, err)
		return
	}
	if err := AtomicWrite(filepath.Join(dir, profilesDataName), append(data, '\n'), 0o644); err != nil {
		logWarn("profiles", "cannot save a profile: %v", err)
		return
	}
	if profilesDataWrite.last == nil {
		profilesDataWrite.last = map[string][]byte{}
	}
	profilesDataWrite.last[dir] = data
}

// profilesForgetWritten drops the remembered bytes of dir (a wiped Guest
// folder, a deleted profile), so the next write is not skipped.
func profilesForgetWritten(dir string) {
	profilesDataWrite.mu.Lock()
	delete(profilesDataWrite.last, dir)
	profilesDataWrite.mu.Unlock()
}

// profilesCopyFavorites deep-copies favourites (their items carry typed data
// that the JSON form restores, favorites.go).
func profilesCopyFavorites(fs *FavoritesStore) *FavoritesStore {
	if fs == nil {
		return nil
	}
	data, err := json.Marshal(fs)
	if err != nil {
		return nil
	}
	var out FavoritesStore
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return &out
}

// errProfilesFull is the list at its limit.
var errProfilesFull = errors.New("there are as many profiles as fit")
