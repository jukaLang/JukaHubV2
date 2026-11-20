package main

// profiles_switch.go — changing who is playing (profiles_store.go).
//
// A switch writes everything of the person leaving under their own paths,
// changes who is playing, puts the arriving person's settings and library into
// the live config and makes every store that keeps a copy in memory read
// again — the way a restored backup does (bkupReloadStores).
//
// jukauser.json stays the owner's file the whole time. While someone else
// plays, the live config holds their settings, so every save is filtered on
// its way to disk (profilesFilterUserConfig, called by saveUserConfigFile):
// the person's settings and library go to their own profile.json and the
// owner's, kept in memory since the owner stepped away, are what is written.
// Device-wide settings changed meanwhile (Wi-Fi, power, volume…) are saved as
// always.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// profilesOwner is the owner's settings and library while someone else
// plays: what jukauser.json keeps for them. nil while the owner plays. The
// value it points to is never changed once stored, so the writer goroutine
// may read it.
var profilesOwner struct {
	mu   sync.Mutex
	data *profileData
}

// profilesLast is the playing person's newest copy (not the owner's), so a
// save that carries only favourites keeps the rest of their file.
var profilesLast struct {
	mu   sync.Mutex
	data profileData
}

// profilesOwnerData returns the owner's copy, or nil.
func profilesOwnerData() *profileData {
	profilesOwner.mu.Lock()
	defer profilesOwner.mu.Unlock()
	return profilesOwner.data
}

func profilesSetOwnerData(d *profileData) {
	profilesOwner.mu.Lock()
	profilesOwner.data = d
	profilesOwner.mu.Unlock()
}

// profilesCaptureLive reads the playing person's settings and library off the
// live config, detached from it. Main thread.
func profilesCaptureLive(c *Config) profileData {
	d := profileData{Version: profilesVersion, Keys: map[string]interface{}{}}
	if c == nil {
		return d
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	if playtime.loaded {
		c.Variables.Custom[playtimeCustomKey] = playtime.data
	}
	for _, k := range profilesPersonKeys() {
		v, ok := c.Variables.Custom[k]
		if !ok || volatileCustomVars[k] {
			continue
		}
		if p := xpackPlain(v); p != nil {
			d.Keys[k] = p
		}
	}
	d.Typed = profileTyped{
		Button:        c.Variables.ButtonColor,
		Label:         c.Variables.LabelColor,
		Input:         c.Variables.InputColor,
		Name:          c.Variables.TSPUsername,
		ReducedMotion: c.Variables.ReducedMotion,
		Clock12:       c.Variables.Clock12,
		DynamicBg:     c.Variables.DynamicBg,
		Wallpaper:     c.Variables.BackgroundImage,
	}
	favoritesMutex.Lock()
	fs := favoritesStore
	favoritesMutex.Unlock()
	d.Favorites = profilesCopyFavorites(&fs)
	return d
}

// profilesOwnerFromDisk reads the owner's settings and library out of
// jukauser.json, which only ever holds the owner's (profilesFilterUserConfig).
func profilesOwnerFromDisk() profileData {
	d, _ := profilesOwnerFromDiskOK()
	return d
}

// profilesOwnerFromDiskOK is profilesOwnerFromDisk; ok is false when the file
// could not be read (d is then empty, not the owner's).
func profilesOwnerFromDiskOK() (profileData, bool) {
	d := profileData{Version: profilesVersion, Keys: map[string]interface{}{}}
	uc, err := readUserConfigFile("jukauser.json")
	if err != nil || uc == nil {
		return d, false
	}
	for _, k := range profilesPersonKeys() {
		if v, ok := uc.Variables.Custom[k]; ok {
			d.Keys[k] = v
		}
	}
	v := uc.Variables
	d.Typed = profileTyped{Button: v.ButtonColor, Label: v.LabelColor, Input: v.InputColor, Name: v.TSPUsername,
		ReducedMotion: v.ReducedMotion, Clock12: v.Clock12, DynamicBg: v.DynamicBg, Wallpaper: xpackDefaultWallpaper}
	d.Favorites = uc.Favorites
	return d, true
}

// profilesSetDataName makes name the one Home greets in d: the typed field and
// its Custom spelling together. syncVariableOverrides copies the Custom
// "tspUsername" over the typed field at the next settings change, so a mirror
// left with the owner's (or the old) name would greet the person with it.
func profilesSetDataName(d *profileData, name string) {
	d.Typed.Name = name
	if d.Keys == nil {
		d.Keys = map[string]interface{}{}
	}
	for k := range d.Keys {
		if strings.EqualFold(k, "tspUsername") {
			delete(d.Keys, k)
		}
	}
	d.Keys["tspUsername"] = name
}

// profilesPrefsOnly is d without its library: what a new profile and a Guest
// session start from.
func profilesPrefsOnly(d profileData) profileData {
	out := profileData{Version: profilesVersion, Keys: map[string]interface{}{}, Typed: d.Typed}
	lib := map[string]bool{}
	for _, k := range profilesLibraryKeys() {
		lib[k] = true
	}
	for k, v := range d.Keys {
		if !lib[k] {
			out.Keys[k] = xpackPlain(v)
		}
	}
	return out
}

// profilesOwnerPrefs is the owner's settings, wherever they are now: on
// screen while the owner plays, in memory while someone else does.
func profilesOwnerPrefs(c *Config) profileData {
	if profilesCurrent() == nil {
		return profilesPrefsOnly(profilesCaptureLive(c))
	}
	if o := profilesOwnerData(); o != nil {
		return profilesPrefsOnly(*o)
	}
	return profileData{Version: profilesVersion, Keys: map[string]interface{}{}}
}

// profilesPutPerson puts d's settings and library keys and its typed fields
// into c, nothing else. Main thread.
func profilesPutPerson(c *Config, d profileData) {
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	for _, name := range profilesTypedMirrors {
		dropCustomMirror(c, name)
	}
	custom := c.Variables.Custom
	for _, k := range profilesPersonKeys() {
		if v, ok := d.Keys[k]; ok && v != nil {
			// A copy: the live config's maps are edited in place, and the
			// owner's copy must not change with them.
			custom[k] = xpackPlain(v)
		} else {
			delete(custom, k)
		}
	}
	t := d.Typed
	c.Variables.ButtonColor, c.Variables.LabelColor, c.Variables.InputColor = t.Button, t.Label, t.Input
	c.Variables.TSPUsername = t.Name
	c.Variables.ReducedMotion, c.Variables.Clock12, c.Variables.DynamicBg = t.ReducedMotion, t.Clock12, t.DynamicBg
}

// profilesAfterConfigReload keeps whoever is playing on their own settings
// when the configuration is reloaded (applyConfigReload, right after it merged
// jukauser.json in): a hot reload of jukaconfig.json, the Live Builder's push,
// a settings snapshot put back. jukauser.json holds the owner's settings, so
// without this the person would be shown the owner's look and the next save
// would file the owner's settings as theirs. The owner's copy in memory is
// taken from the file just read, so a snapshot put back reaches the owner's
// settings too. prev is the live config being replaced. Main thread.
func profilesAfterConfigReload(prev, next *Config) {
	if profilesCurrent() == nil || next == nil {
		return
	}
	if o, ok := profilesOwnerFromDiskOK(); ok {
		if old := profilesOwnerData(); old != nil {
			o.Typed.Wallpaper = old.Typed.Wallpaper
		}
		profilesSetOwnerData(&o)
	}
	var mine profileData
	if prev != nil && prev != next {
		mine = profilesCaptureLive(prev)
	} else {
		profilesLast.mu.Lock()
		mine = profilesLast.data
		profilesLast.mu.Unlock()
	}
	profilesPutPerson(next, mine)
}

// profilesApplyLive puts d into the live config and makes everything that
// keeps a copy read it again. Main thread.
func profilesApplyLive(c *Config, d profileData) {
	if c == nil {
		return
	}
	profilesPutPerson(c, d)
	custom := c.Variables.Custom
	t := d.Typed
	wall := strings.TrimSpace(t.Wallpaper)
	if wall == "" || !(appWallpaperIsShippedDefault(wall) || wallpaperFileExists(wall)) {
		wall = xpackDefaultWallpaper
	}
	if wall != c.Variables.BackgroundImage {
		c.Variables.BackgroundImage = wall
		xpackDropDynamicWallpaper()
	}

	// Favourites live in the cached user config's section (favorites.go).
	fav := profilesCopyFavorites(d.Favorites)
	if fav == nil {
		fav = &FavoritesStore{}
	}
	if userConfigCache != nil {
		userConfigCache.Favorites = fav
	}
	favoritesMutex.Lock()
	favoritesStore = FavoritesStore{}
	favoritesMutex.Unlock()
	loadFavorites()

	// The "recently played" marks: loadRecentlyPlayed reads the boot copy, so
	// they are taken from the config just set.
	rpMutex.Lock()
	recentlyPlayed = map[string]bool{}
	if list, ok := custom["recently_played"].([]interface{}); ok {
		for _, item := range list {
			if s, ok := item.(string); ok {
				recentlyPlayed[s] = true
			}
		}
	}
	rpMutex.Unlock()

	// The look, the language, the text size, the sounds, the playtime.
	uiLanguageApply(c)
	a11yInit(c)
	xpackRepaint(c)
	menuColourSync(c)
	bootSfxBind(c)
	xpackRefreshLabel(c)
	playtimeInit(c)
}

// profilesReloadDataStores makes the per-person stores of the data folder
// read their file again, now from the new person's folder (profilesRedirect).
func profilesReloadDataStores() {
	gameRecentsMu.Lock()
	gameRecentsLoaded, gameRecents = false, nil
	gameRecentsMu.Unlock()
	gameHistoryStore.mu.Lock()
	gameHistoryStore.loaded = false
	gameHistoryStore.mu.Unlock()
	homeConsoleRecents.Lock()
	homeConsoleRecents.loaded, homeConsoleRecents.items = false, nil
	homeConsoleRecents.gen++
	homeConsoleRecents.Unlock()
	vkLearned.mu.Lock()
	vkLearned.loaded, vkLearned.words = false, nil
	vkLearned.mu.Unlock()
	vkLearnedDirtyAt.Store(0)
	browserBookmarks.mu.Lock()
	browserBookmarks.loaded, browserBookmarks.list, browserBookmarks.path = false, nil, ""
	browserBookmarks.mu.Unlock()
}

// profilesFlushStores writes what the person leaving has in memory, under
// their paths, before anything points elsewhere. Main thread.
func profilesFlushStores(c *Config) {
	if path, data, ok := vkLearnedTakeSnapshot(); ok {
		vkLearnedWrite(path, data)
	}
	// The last seconds and any trophy events still pending are theirs.
	playtimeFlush(c)
	saveUserConfigDebounced(userConfigFromVariables(c))
	flushPendingUserConfig()
	gameHistoryFlush(2 * time.Second)
}

// profilesGameRunning reports whether a game is up (a switch waits for it).
func profilesGameRunning() bool {
	return gameScreenSuspended() || retroArchRunning()
}

// profilesSwitch makes id the one playing: "" is the owner, profilesGuestID a
// Guest session, anything else a profile's id. Refused while a game runs.
// Main thread.
func profilesSwitch(c *Config, id string) error {
	if profilesGameRunning() {
		return errors.New("Quit the game first, then switch")
	}
	return profilesSwitchWith(c, id, true)
}

// profilesSwitchWith is profilesSwitch; flush false is the start-up, where
// nothing has been changed yet that needs writing first.
func profilesSwitchWith(c *Config, id string, flush bool) error {
	if c == nil {
		return errors.New("no configuration is loaded")
	}
	cur := profilesCurrent()
	curID := ""
	if cur != nil {
		curID = cur.ID
	}
	if id == curID {
		return nil
	}
	doc := profilesSnapshot()
	var next *profilesActiveInfo
	switch id {
	case "":
	case profilesGuestID:
		next = &profilesActiveInfo{ID: profilesGuestID, Name: "Guest", Dir: profilesDirFor(profilesGuestID), Guest: true}
	default:
		p, ok := profilesFind(doc, id)
		if !ok {
			return errors.New("That profile was deleted")
		}
		next = &profilesActiveInfo{ID: p.ID, Name: p.Name, Colour: p.Colour, Dir: profilesDirFor(p.ID)}
	}
	if next != nil && next.Dir == "" {
		return errors.New("There is no data folder to keep a profile in")
	}

	// The person leaving: written under their own paths first.
	if flush {
		profilesFlushStores(c)
	} else {
		// A save queued during start-up carries the owner's settings: it lands
		// now, before anyone else's are in place, not into their file later.
		flushPendingUserConfig()
	}
	leaving := profilesCaptureLive(c)
	switch {
	case cur == nil:
		profilesSetOwnerData(&leaving)
	case cur.Guest:
		// A Guest keeps nothing.
	default:
		profilesWriteData(cur.Dir, leaving)
	}

	// The person arriving.
	var arriving profileData
	switch {
	case next == nil:
		if o := profilesOwnerData(); o != nil {
			arriving = *o
		} else {
			// Not expected (the owner's copy is kept from the moment they step
			// away); jukauser.json holds nothing but the owner's anyway.
			arriving = profilesOwnerFromDisk()
		}
	case next.Guest:
		profilesWipeGuest()
		arriving = profilesOwnerPrefs(c)
	default:
		d, ok := profilesReadData(next.Dir)
		if !ok {
			// Never played yet (its file went missing): the owner's look.
			d = profilesOwnerPrefs(c)
			profilesSetDataName(&d, next.Name)
		}
		arriving = d
	}

	profilesLast.mu.Lock()
	profilesLast.data = arriving
	profilesLast.mu.Unlock()
	if next != nil {
		profilesOthersPlayed = true
	}
	profilesActive.Store(next)
	if (next == nil || !next.Guest) && doc.Active != id {
		_ = profilesUpdate(func(d *profilesDoc) error {
			d.Active = id
			return nil
		})
	}
	profilesReloadDataStores()
	profilesApplyLive(c, arriving)
	if cur != nil && cur.Guest {
		profilesWipeGuest()
	}
	if next == nil {
		profilesSetOwnerData(nil)
	}
	// jukauser.json keeps the owner's, the profile's file the person's.
	saveUserConfigDebounced(userConfigFromVariables(c))
	logInfo("profiles", "now playing: %s", profilesLogName(next))
	return nil
}

// profilesLogName names a profile in the log without the person's name.
func profilesLogName(a *profilesActiveInfo) string {
	switch {
	case a == nil:
		return "owner"
	case a.Guest:
		return "guest"
	}
	return "profile " + a.ID
}

// profilesWipeGuest removes the Guest folder: its saves, history and settings.
func profilesWipeGuest() {
	dir := profilesDirFor(profilesGuestID)
	if dir == "" {
		return
	}
	profilesForgetWritten(dir)
	if err := os.RemoveAll(dir); err != nil {
		logWarn("profiles", "cannot clear the guest folder: %v", err)
	}
}

// profilesFilterUserConfig keeps jukauser.json the owner's while someone else
// plays. The person's settings and library in u go to their own profile.json
// (a Guest's to the folder wiped when the session ends) and are replaced, in
// u, by the owner's. sections is a save that carries only the favourites,
// chat and JukaLand sections: its settings are already the owner's. Called by
// saveUserConfigFile on u, its own copy, just before encoding; any goroutine.
func profilesFilterUserConfig(u *UserConfig, sections bool) {
	a := profilesCurrent()
	if a == nil || u == nil {
		return
	}
	owner := profilesOwnerData()
	if owner == nil {
		o := profilesOwnerFromDisk()
		profilesSetOwnerData(&o)
		owner = &o
	}
	if u.Variables.Custom == nil {
		u.Variables.Custom = map[string]interface{}{}
	}
	custom := u.Variables.Custom

	profilesLast.mu.Lock()
	mine := profilesLast.data
	if !sections {
		keys := make(map[string]interface{}, len(mine.Keys))
		for _, k := range profilesPersonKeys() {
			// Detached: a direct save hands over the live values (playtime's
			// maps among them), and this copy outlives the save.
			if v, ok := custom[k]; ok {
				if p := xpackPlain(v); p != nil {
					keys[k] = p
				}
			}
		}
		mine.Keys = keys
		wall := mine.Typed.Wallpaper
		mine.Typed = profileTyped{
			Button:        u.Variables.ButtonColor,
			Label:         u.Variables.LabelColor,
			Input:         u.Variables.InputColor,
			Name:          u.Variables.TSPUsername,
			ReducedMotion: u.Variables.ReducedMotion,
			Clock12:       u.Variables.Clock12,
			DynamicBg:     u.Variables.DynamicBg,
			Wallpaper:     wall,
		}
	}
	if u.Favorites != nil {
		mine.Favorites = profilesCopyFavorites(u.Favorites)
	}
	profilesLast.data = mine
	profilesLast.mu.Unlock()
	profilesWriteData(a.Dir, mine)

	// What goes into jukauser.json: the owner's.
	profilesPutOwner(u, owner)
}

// profilesOwnerView makes u, a copy of the live settings, the owner's: a
// backup made while someone else plays is still the owner's (bkup.go).
func profilesOwnerView(u *UserConfig) {
	if profilesCurrent() == nil || u == nil {
		return
	}
	owner := profilesOwnerData()
	if owner == nil {
		o := profilesOwnerFromDisk()
		owner = &o
	}
	if u.Variables.Custom == nil {
		u.Variables.Custom = map[string]interface{}{}
	}
	profilesPutOwner(u, owner)
}

// profilesRefuseUnlessOwner says why what cannot be done while someone other
// than the owner plays, and reports whether it was refused.
func profilesRefuseUnlessOwner(what string) bool {
	if profilesCurrent() == nil {
		return false
	}
	ShowToast("Switch to "+profilesOwnerLabel(profilesSnapshot())+" to "+what, ToastKindWarning)
	return true
}

// profilesPutOwner replaces, in u (a copy, never the live config), the
// person's settings and library with the owner's.
func profilesPutOwner(u *UserConfig, owner *profileData) {
	custom := u.Variables.Custom
	for k := range custom {
		for _, name := range profilesTypedMirrors {
			if strings.EqualFold(k, name) {
				delete(custom, k)
			}
		}
	}
	for _, k := range profilesPersonKeys() {
		if v, ok := owner.Keys[k]; ok && v != nil {
			custom[k] = v
		} else {
			delete(custom, k)
		}
	}
	t := owner.Typed
	u.Variables.ButtonColor, u.Variables.LabelColor, u.Variables.InputColor = t.Button, t.Label, t.Input
	u.Variables.TSPUsername = t.Name
	u.Variables.ReducedMotion, u.Variables.Clock12, u.Variables.DynamicBg = t.ReducedMotion, t.Clock12, t.DynamicBg
	u.Favorites = owner.Favorites
}

// profilesBoot runs once at start, after the stores have loaded: a Guest
// folder left by the last run goes, the person who played last is put back,
// and with "Ask who is playing at start" on the picker opens. With no profile
// made it only looks for the list. Main thread.
func profilesBoot(c *Config) {
	doc := profilesSnapshot()
	guestLeft := false
	if dir := profilesDirFor(profilesGuestID); dir != "" {
		if _, err := os.Lstat(dir); err == nil {
			guestLeft = true
		}
	}
	if guestLeft || len(doc.Profiles) > 0 {
		// The last run may have ended mid-game as someone else: their
		// RetroArch folders go back before anything can launch.
		profilesRecoverScope()
	}
	profilesWipeGuest()
	if len(doc.Profiles) == 0 {
		return
	}
	if doc.Active != "" {
		if err := profilesSwitchWith(c, doc.Active, false); err != nil {
			logWarn("profiles", "could not put back the last profile: %v", err)
		}
	}
	if doc.AskAtStart {
		profilesPickerOpen(c, true)
	}
}

// ── The list ─────────────────────────────────────────────────────────────

// profilesAdd makes a profile named name. It starts with the owner's look and
// an empty library, and its RetroArch folders are made at once.
func profilesAdd(c *Config, name string) (profileEntry, error) {
	name = profilesCleanName(name)
	if name == "" {
		return profileEntry{}, errors.New("Type a name first")
	}
	seed := profilesOwnerPrefs(c)
	profilesSetDataName(&seed, name)
	ownerName := profilesOwnerLabel(profilesSnapshot())
	var entry profileEntry
	err := profilesUpdate(func(doc *profilesDoc) error {
		if len(doc.Profiles) >= profilesMax {
			return errProfilesFull
		}
		taken := map[string]bool{}
		for _, p := range doc.Profiles {
			taken[p.ID] = true
			if strings.EqualFold(p.Name, name) {
				return fmt.Errorf("There is already a profile called %s", p.Name)
			}
		}
		if doc.OwnerName == "" {
			doc.OwnerName = ownerName
		}
		entry = profileEntry{ID: profilesNewID(taken), Name: name, Colour: (len(doc.Profiles) + 1) % len(profilesColours)}
		doc.Profiles = append(doc.Profiles, entry)
		return nil
	})
	if err != nil {
		return entry, err
	}
	dir := profilesDirFor(entry.ID)
	profilesWriteData(dir, seed)
	saves, states := profilesSaveDirs(dir)
	for _, d := range []string{saves, states} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			logWarn("profiles", "cannot create %s: %v", d, err)
		}
	}
	return entry, nil
}

// profilesRename renames a profile; "" renames the owner's label. The name
// Home greets follows when it was the old name.
func profilesRename(c *Config, id, name string) error {
	name = profilesCleanName(name)
	if name == "" {
		return errors.New("Type a name first")
	}
	old := ""
	err := profilesUpdate(func(doc *profilesDoc) error {
		if id == "" {
			old, doc.OwnerName = doc.OwnerName, name
			return nil
		}
		for i := range doc.Profiles {
			if doc.Profiles[i].ID == id {
				old, doc.Profiles[i].Name = doc.Profiles[i].Name, name
				return nil
			}
		}
		return errors.New("That profile was deleted")
	})
	if err != nil || id == "" {
		return err
	}
	if a := profilesCurrent(); a != nil && a.ID == id {
		next := *a
		next.Name = name
		profilesActive.Store(&next)
		if c != nil && strings.TrimSpace(c.Variables.TSPUsername) == old {
			setStoreString(c, "tspUsername", name)
		}
		return nil
	}
	dir := profilesDirFor(id)
	if d, ok := profilesReadData(dir); ok && strings.TrimSpace(d.Typed.Name) == old {
		profilesSetDataName(&d, name)
		profilesWriteData(dir, d)
	}
	return nil
}

// profilesSetColour sets a profile's avatar colour; "" is the owner's.
func profilesSetColour(id string, colour int) {
	colour = profilesClampColour(colour)
	_ = profilesUpdate(func(doc *profilesDoc) error {
		if id == "" {
			doc.OwnerColour = colour
			return nil
		}
		for i := range doc.Profiles {
			if doc.Profiles[i].ID == id {
				doc.Profiles[i].Colour = colour
			}
		}
		return nil
	})
	if a := profilesCurrent(); a != nil && a.ID == id {
		next := *a
		next.Colour = colour
		profilesActive.Store(&next)
	}
}

// profilesDelete removes a profile from the list and moves its folder (saves,
// states, history, settings) to <data>/profiles/.deleted/, where it can be
// copied back from Files. The profile playing cannot be deleted. It returns
// where the folder went.
func profilesDelete(id string) (string, error) {
	if a := profilesCurrent(); a != nil && a.ID == id {
		return "", errors.New("Switch to someone else first")
	}
	name := ""
	err := profilesUpdate(func(doc *profilesDoc) error {
		kept := doc.Profiles[:0]
		for _, p := range doc.Profiles {
			if p.ID == id {
				name = p.Name
				continue
			}
			kept = append(kept, p)
		}
		if name == "" {
			return errors.New("That profile was deleted")
		}
		doc.Profiles = kept
		if doc.Active == id {
			doc.Active = ""
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	dir := profilesDirFor(id)
	if dir == "" {
		return "", nil
	}
	profilesForgetWritten(dir)
	if _, err := os.Stat(dir); err != nil {
		return "", nil
	}
	trash := filepath.Join(profilesRoot(), profilesTrashName)
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return "", err
	}
	dest := bkupUniquePath(trash, profilesFolderLabel(name)+"-"+id)
	if err := os.Rename(dir, dest); err != nil {
		return "", fmt.Errorf("its folder stays where it was: %v", err)
	}
	return dest, nil
}

// profilesFolderLabel makes a name safe as part of a folder name.
func profilesFolderLabel(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "profile"
	}
	return b.String()
}

// profilesSetAskAtStart switches the start-up question.
func profilesSetAskAtStart(on bool) {
	_ = profilesUpdate(func(doc *profilesDoc) error {
		doc.AskAtStart = on
		return nil
	})
}
