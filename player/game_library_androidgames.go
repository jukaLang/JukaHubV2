package main

// game_library_androidgames.go — the Games Library's Android row (AND-16).
//
// The APK Store turns an APK it can run into a game profile
// (apkstore_profile.go), and game profiles had only their own panel: the
// library never read them. This file lists the store's games in the library as
// one more console, "Android": the profile's title, and the launcher icon the
// store saved as the cover when it added the game. A on a game opens its page
// like any other; Play starts it through the same game-profile launch the
// store and the Game Profiles panel use (triage, the memory pre-flight,
// staging, AndroidRT, the launch log). The row is read from the store's own
// profile files on every library scan, so a game the store removes is gone
// from it at the next scan.
//
// Only the store's profiles (local/apkstore-*.json) are listed: a hand-written
// AndroidPak profile stays in Game Profiles. Reading them opens small JSON
// files only — no APK — so the library's background scan stays quick.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// androidLibrarySystem is the console name of the row.
	androidLibrarySystem = "Android"
	// androidLibraryLauncher stands in for an emulator path, so the library's
	// "is there a launcher for this console" checks pass; launchLibraryGame
	// hands an Android game to its game profile and never runs this name.
	androidLibraryLauncher = "androidpak:game-profile"
	// androidLibraryMaxProfiles bounds the profile files one scan reads.
	androidLibraryMaxProfiles = 300
)

// androidLibraryEntry is one APK Store profile whose APK is on the card.
type androidLibraryEntry struct {
	profile GameProfile
	apk     string
}

// androidLibraryArtPath is where an Android game's cover is saved: the
// library's artwork folder, under its console like a ROM's box art, named
// after the APK plus a short hash of its path. The name alone is not enough:
// every APK pulled from a phone is base.apk, and those games shared one cover
// (the last one added set it for all, and Remove deleted it for all).
func androidLibraryArtPath(apk string) string {
	art := androidLibraryLegacyArtPath(apk)
	if art == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(apkStorePathKey(apk)))
	return strings.TrimSuffix(art, ".png") + "-" + hex.EncodeToString(sum[:4]) + ".png"
}

// androidLibraryLegacyArtPath is the name covers had before they were unique:
// the APK's bare name.
func androidLibraryLegacyArtPath(apk string) string {
	return romArtworkPath(scannedROM{Path: apk, System: androidLibrarySystem}, libraryArtworkRoot())
}

// androidLibraryCoverPath is the cover to show for apk: its own, else one saved
// under the bare name before covers were unique (so no cover is lost), else
// its own path, where none is yet.
func androidLibraryCoverPath(apk string) string {
	art := androidLibraryArtPath(apk)
	if art != "" && !artworkCoverPresent(art) {
		if legacy := androidLibraryLegacyArtPath(apk); artworkCoverPresent(legacy) {
			return legacy
		}
	}
	return art
}

// androidLibraryRemoveCover deletes the cover saved for apk when its game
// leaves Games (after its profile is gone). A cover under the bare name goes
// too, unless another game of the store still has that name and may be
// showing it.
func androidLibraryRemoveCover(apk string) {
	if art := androidLibraryArtPath(apk); art != "" {
		_ = os.Remove(art)
	}
	legacy := androidLibraryLegacyArtPath(apk)
	if !artworkCoverPresent(legacy) {
		return
	}
	for _, e := range androidLibraryEntries() {
		if apkStorePathKey(androidLibraryLegacyArtPath(e.apk)) == apkStorePathKey(legacy) {
			return
		}
	}
	_ = os.Remove(legacy)
}

// androidLibraryReadProfile reads one of the store's profile files; ok is
// false for anything that is not a valid AndroidPak profile.
func androidLibraryReadProfile(path string) (GameProfile, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > gameProfileMaxFileBytes {
		return GameProfile{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return GameProfile{}, false
	}
	var p GameProfile
	if json.Unmarshal(stripUTF8BOM(raw), &p) != nil || p.Runtime != gameRuntimeAndroidPak ||
		strings.TrimSpace(p.Executable) == "" || validateGameProfile(&p) != nil {
		return GameProfile{}, false
	}
	return p, true
}

// androidLibraryEntries lists the store's profiles whose APK is still where
// the profile says, at most androidLibraryMaxProfiles of them.
func androidLibraryEntries() []androidLibraryEntry {
	dir := filepath.Join(gameProfilesDir(), "local")
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []androidLibraryEntry
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasPrefix(name, apkStoreProfilePrefix) || !strings.EqualFold(filepath.Ext(name), ".json") {
			continue
		}
		if len(out) >= androidLibraryMaxProfiles {
			break
		}
		p, ok := androidLibraryReadProfile(filepath.Join(dir, name))
		if !ok {
			continue
		}
		apk := filepath.Join(filepath.FromSlash(p.Dir), filepath.FromSlash(p.Executable))
		if !regularFile(apk) {
			continue
		}
		out = append(out, androidLibraryEntry{profile: p, apk: apk})
	}
	return out
}

// androidLibraryEnsureCover saves the launcher icon as the cover of a game
// added before covers were saved (or before they had unique names), when the
// APK Store's scan already holds it (its cache; no APK is opened here).
func androidLibraryEnsureCover(apk string) {
	art := androidLibraryArtPath(apk)
	if art == "" || artworkCoverPresent(art) {
		return
	}
	apkStoreCache.mu.Lock()
	info, ok := apkStoreCache.m[apk]
	apkStoreCache.mu.Unlock()
	if !ok || info.Icon == nil {
		return
	}
	if err := apkStoreSaveIcon(info.Icon, art); err != nil {
		logDebug("library", "Android cover for %s: %v", filepath.Base(apk), err)
	}
}

// libraryAddAndroidGames adds the Android row to a built library (the scan
// goroutine calls it; seconds is the playtime snapshot the build used).
func libraryAddAndroidGames(lib *gameLibraryReport, seconds map[string]int64) {
	if lib == nil {
		return
	}
	found := androidLibraryEntries()
	if len(found) == 0 {
		return
	}
	games := make([]libraryGame, 0, len(found))
	for _, f := range found {
		title := strings.TrimSpace(f.profile.Title)
		if title == "" {
			title = gameDisplayTitle(f.apk)
		}
		androidLibraryEnsureCover(f.apk)
		art := androidLibraryCoverPath(f.apk)
		games = append(games, libraryGame{
			Path:   f.apk,
			System: androidLibrarySystem,
			Title:  title,
			Art:    art,
			HasArt: artworkCoverPresent(art),
		})
	}
	libraryDecorateGames(games, seconds)
	withArt := countCoveredGames(games)
	lib.Systems = append(lib.Systems, librarySystem{Name: androidLibrarySystem, Games: games, WithArt: withArt})
	lib.Total += len(games)
	lib.WithArt += withArt
	sortLibrarySystems(lib.Systems)
}

// libraryOpenNetplay is X in the library: LAN play for an emulator game. An
// Android game has no LAN play here, so it says so instead of opening a
// host/join panel that could only fail.
func libraryOpenNetplay() {
	if game, ok := selectedLibraryGame(); ok && isBuiltInLibraryGame(game) {
		ShowToast("LAN play is for emulator games — "+game.Title+" has none here", ToastKindInfo)
		return
	}
	if game, ok := selectedLibraryGame(); ok && game.System == androidLibrarySystem {
		ShowToast("LAN play is for emulator games — Android games have none here", ToastKindInfo)
		return
	}
	if game, ok := selectedLibraryGame(); ok && game.System == html5LibrarySystem {
		ShowToast("LAN play is for emulator games — HTML5 games have none here", ToastKindInfo)
		return
	}
	openNetplayForSelectedGame()
}

// androidLibraryProfileFor finds the store's profile that launches apk.
func androidLibraryProfileFor(apk string) (GameProfile, bool) {
	want := apkStorePathKey(apkStoreRealPath(apk))
	for _, e := range androidLibraryEntries() {
		if apkStorePathKey(apkStoreRealPath(e.apk)) == want {
			return e.profile, true
		}
	}
	return GameProfile{}, false
}

// launchAndroidLibraryGame starts an Android game from the library, its
// Continue row or Home's recents (render thread). The quick checks run here
// and come back as the error the caller shows; the launch itself — the
// profile's paths and runtime resolved, the APK triaged (its whole zip
// directory read from the card), the memory pre-flight, staging the game into
// memory, which takes seconds for a big game — runs in the background like the
// APK Store's Play, and a failure there opens the launch-failure dialog.
func launchAndroidLibraryGame(game libraryGame) error {
	p, ok := androidLibraryProfileFor(game.Path)
	if !ok {
		return errors.New("it is no longer in Games — add it again in the APK Store")
	}
	if pid := gameProfileRunningPID(p.ID); pid > 0 {
		return fmt.Errorf("%s is already running", p.Title)
	}
	// A second Play while the first is still staging: before the playtime
	// check, which counts a repeated press as the user's override.
	if gameProfileStartingNow(p.ID) {
		return fmt.Errorf("%s is already starting", p.Title)
	}
	if androidRuntimeMissingNow() {
		switch {
		case !androidRTSupported():
			return errors.New("Android games run on the handheld (arm64 Linux), not on this machine")
		case androidRTInstalling():
			return errors.New("AndroidRT is still being installed — try again when the install bar is done")
		}
		return errors.New("AndroidRT is not installed yet — install it in the APK Store's Runtime tab")
	}
	if err := playtimeAllowGameLaunch(game.Title); err != nil {
		return err
	}
	// The start is claimed here, before the background work, so no other Play
	// (here, the APK Store's or Game Profiles') starts the game a second time
	// while this one stages it. The time-lapse settings are read here too: the
	// launch goroutine must not read the config.
	if !gameProfileClaimStart(p.ID) {
		return fmt.Errorf("%s is already starting", p.Title)
	}
	tl := timelapseSettingsFor(appConfig)
	title, id := p.Title, p.ID
	go func() {
		err := func() error {
			// Released once the game has started (its PID is recorded by
			// then, and the running check takes over) or the start failed.
			defer gameProfileReleaseStart(id)
			// launchGameProfileClaimed resolves the profile exactly as the
			// catalog does (paths inside the game folders, the runtime
			// planned, the APK triaged) and returns the same errors, so the
			// APK is not triaged twice.
			return launchGameProfileClaimed(gameProfileEntry{Profile: p, Source: "local"}, tl)
		}()
		if err == nil {
			// Only a game that started joins the recents (the library's
			// Continue row and Home's), like a ROM. Its launch counts toward
			// trophies where every Android game's does (launchGameProfileClaimed,
			// startGameProfileCommand), so it is not counted twice.
			tbxPost(func() {
				gmetaNoteLaunch(game)
				recordGameRecent(game)
				if gameLib.open {
					gameLib.recents = recentGames()
					invalidateGameLibrarySystemsCache()
				}
			})
			return
		}
		logWarn("library", "launch %s failed: %v", id, err)
		logPath := filepath.Join(gameProfilesLogsDir(), id+".log")
		if !regularFile(logPath) {
			logPath = ""
		}
		tbxPost(func() {
			PlayErrorSound()
			showLaunchFailureDialog(title, err, logPath)
		})
	}()
	return nil
}
