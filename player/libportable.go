package main

// libportable.go — a handheld's SD card on a computer: its games in the
// desktop JukaHub's library, played with the card's own saves and BIOS
// files.
//
// Put the TrimUI's card in a PC's card reader and JukaHub on the PC finds it
// (a drive whose root has Roms, and RetroArch, Emus or Apps/JukaHub): its
// Roms folder joins the library ("Play from a handheld's card", Settings →
// Games & emulators, on by default). A game from the card that runs in the
// PC's RetroArch (a core started with -L) gets a one-launch --appendconfig
// that points RetroArch's save, state and system folders at the card's own —
// the folders the card's retroarch.cfg names — with the card's way of sorting
// saves into folders, so the save the PC writes is the very file the handheld
// loads next time, and the other way round. Nothing else on the card is
// touched; standalone emulators keep their own folders.
//
// Without the card in the PC, Device Sync (devsync.go) and the save handoff
// (savehandoff.go) move saves, BIOS files and games over Wi-Fi instead.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	portableCardKey = "portableCard"
	// portableCardTTL is how long a look for cards is reused.
	portableCardTTL = 20 * time.Second
)

// portableCardOn mirrors the setting for any goroutine (published from the
// main thread).
var portableCardOn atomic.Bool

var portableCardCache struct {
	mu    sync.Mutex
	at    time.Time
	cards []string
}

func init() {
	portableCardOn.Store(true)
	deferStartupTask("portable-card", func() { portableCardPublish(appConfig) })
}

// portableCardSetting reads the setting (default on). Main thread.
func portableCardSetting(c *Config) bool {
	if c == nil {
		return true
	}
	v, ok := customValue(c.Variables.Custom, portableCardKey)
	if !ok {
		return true
	}
	if b, ok := v.(bool); ok {
		return b
	}
	s, _ := v.(string)
	return !strings.EqualFold(strings.TrimSpace(s), "false")
}

// portableCardPublish copies the setting for the goroutines.
func portableCardPublish(c *Config) { portableCardOn.Store(portableCardSetting(c)) }

// portableCardIs reports whether a folder is a handheld card's root.
func portableCardIs(root string) bool {
	if !dirExists(filepath.Join(root, "Roms")) {
		return false
	}
	for _, sub := range []string{"RetroArch", "Emus", filepath.Join("Apps", "JukaHub")} {
		if dirExists(filepath.Join(root, sub)) {
			return true
		}
	}
	return false
}

// portableCardCandidates are the places a card shows up on this computer.
func portableCardCandidates() []string {
	var out []string
	if IsWindows() {
		for _, d := range filesDrives() {
			if strings.EqualFold(filepath.VolumeName(d.Path)+`\`, os.Getenv("SystemDrive")+`\`) {
				continue // the PC's own C:\ is no card
			}
			if d.Detail == "Network drive" || d.Detail == "CD/DVD drive" {
				continue
			}
			out = append(out, d.Path)
		}
		return out
	}
	user := os.Getenv("USER")
	for _, base := range []string{filepath.Join("/media", user), filepath.Join("/run/media", user), "/media"} {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, filepath.Join(base, e.Name()))
			}
		}
	}
	return out
}

// portableCards are the handheld cards in this computer now (never on the
// handheld itself, and none while the setting is off).
func portableCards() []string {
	if IsTrimuiDevice() || !portableCardOn.Load() {
		return nil
	}
	portableCardCache.mu.Lock()
	defer portableCardCache.mu.Unlock()
	if !portableCardCache.at.IsZero() && time.Since(portableCardCache.at) < portableCardTTL {
		return portableCardCache.cards
	}
	var cards []string
	for _, root := range portableCardCandidates() {
		if portableCardIs(root) {
			cards = append(cards, filepath.Clean(root))
		}
	}
	if len(cards) != len(portableCardCache.cards) {
		for _, c := range cards {
			logInfo("library", "a handheld's SD card is in this computer: %s (its games join the library)", c)
		}
	}
	portableCardCache.cards, portableCardCache.at = cards, time.Now()
	return cards
}

// portableCardRomRoots are the cards' Roms folders, for the library.
func portableCardRomRoots() []string {
	var out []string
	for _, card := range portableCards() {
		out = append(out, filepath.Join(card, "Roms"))
	}
	return out
}

// portableCardFor is the card a game's file is on.
func portableCardFor(gamePath string) (string, bool) {
	for _, card := range portableCards() {
		if isPathWithin(gamePath, card) {
			return card, true
		}
	}
	return "", false
}

// portableCardConfig is the card's own RetroArch config.
func portableCardConfig(card string) (text, cfgPath string, ok bool) {
	for _, rel := range []string{"RetroArch/retroarch.cfg", "RetroArch/.retroarch/retroarch.cfg", "retroarch/retroarch.cfg"} {
		p := filepath.Join(card, filepath.FromSlash(rel))
		if data, err := os.ReadFile(p); err == nil {
			return string(data), p, true
		}
	}
	return "", "", false
}

// portableCardResolve turns one of the card config's folders into the
// folder on this computer: "/mnt/SDCARD/…" is the card, "~", ":" and a
// relative path are the card's RetroArch folder (the handheld starts
// RetroArch with HOME there). "" for "default" or nothing. Pure apart from
// raInstallDir's look for the RetroArch binary.
func portableCardResolve(card, cfgPath, value string) string {
	v := strings.TrimSpace(strings.Trim(strings.TrimSpace(value), `"'`))
	if v == "" || strings.EqualFold(v, "default") || strings.EqualFold(v, "disabled") {
		return ""
	}
	for _, prefix := range []string{"/mnt/SDCARD/", "/mnt/sdcard/", "/mnt/SDCARD", "/mnt/sdcard"} {
		if strings.HasPrefix(v, prefix) {
			return filepath.Join(card, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(v, prefix), "/")))
		}
	}
	if strings.HasPrefix(v, "/") {
		return "" // somewhere on the handheld's own system, not the card
	}
	v = strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(v, "~"), ":"), `/\`)
	return filepath.Join(raInstallDir(cfgPath), filepath.FromSlash(v))
}

// portableCardSortKeys are the card config's lines that decide how saves are
// filed into folders; the PC's RetroArch uses the card's.
var portableCardSortKeys = []string{
	"sort_savefiles_enable", "sort_savestates_enable",
	"sort_savefiles_by_content_enable", "sort_savestates_by_content_enable",
}

// portableCardOverlay is the one-launch config for a game on card: the
// card's save, state and system folders and its sorting. Empty when the card
// says nothing JukaHub can use.
func portableCardOverlay(card string) string {
	text, cfgPath, ok := portableCardConfig(card)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, key := range []string{"savefile_directory", "savestate_directory", "system_directory"} {
		v, ok := raConfigGet(text, key)
		if !ok {
			continue
		}
		if dir := portableCardResolve(card, cfgPath, v); dir != "" && dirExists(dir) {
			b.WriteString(key + " = \"" + filepath.ToSlash(dir) + "\"\n")
		}
	}
	for _, key := range portableCardSortKeys {
		if v, ok := raConfigGet(text, key); ok {
			b.WriteString(key + " = \"" + strings.Trim(strings.TrimSpace(v), `"`) + "\"\n")
		}
	}
	return b.String()
}

// portableCardLaunchArgs adds the card's folders to a launch of a game on a
// handheld's card in the computer's RetroArch (a core launcher). Main
// thread (the launch path).
func portableCardLaunchArgs(launcher string, game libraryGame, extra []string) []string {
	switch strings.ToLower(filepath.Ext(launcher)) {
	case ".dll", ".so", ".dylib":
	default:
		return extra
	}
	card, ok := portableCardFor(game.Path)
	if !ok {
		return extra
	}
	overlay := portableCardOverlay(card)
	if overlay == "" {
		return extra
	}
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return extra
	}
	path := filepath.Join(dir, "portable-card.cfg")
	if err := os.WriteFile(path, []byte("# One launch of a game on a handheld's card (libportable.go)\n"+overlay), 0o644); err != nil {
		logWarn("library", "could not point RetroArch at the card's saves: %v", err)
		return extra
	}
	logInfo("library", "%s plays with the saves on the handheld's card (%s)", saveLedgerTitle(game.Title, game.Path), card)
	return hotseatMergeAppendConfig(extra, path)
}

// portableCardSettingsRow is Settings → Games & emulators → Play from a
// handheld's card (shown on a computer only).
func portableCardSettingsRow() setRow {
	return setRow{
		ID: portableCardKey, Label: "Play from a handheld's card", Kind: setKindToggle,
		Visible: func(*Config) bool { return !P().IsHandheld() },
		Detail: func(c *Config) string {
			if !portableCardSetting(c) {
				return "Off: a TrimUI card in this computer stays out of the library"
			}
			if cards := portableCards(); len(cards) > 0 {
				return "Playing from " + strings.Join(cards, ", ") + " with its own saves and BIOS"
			}
			return "Put the handheld's SD card in this computer: its games join the library, with its saves"
		},
		Get: portableCardSetting,
		Set: func(c *Config, on bool) {
			setStoreBool(c, portableCardKey, on)
			portableCardPublish(c)
			portableCardCache.mu.Lock()
			portableCardCache.at = time.Time{}
			portableCardCache.mu.Unlock()
			setToast("Play from a handheld's card: " + setOnOff(on))
		},
	}
}
