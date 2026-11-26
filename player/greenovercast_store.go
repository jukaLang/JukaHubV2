package main

// greenovercast_store.go — where GreenOvercast keeps its settings and the
// sign-in it saves, and what JukaHub does when that sign-in cannot be read.
//
// GreenOvercast's launcher keeps everything in
// ${XDG_CONFIG_HOME:-$HOME/.config}/greenovercast: its settings, its own log,
// an artwork cache (artwork/), the H.264 start-up cache and the saved sign-in
// (tokens.bin and its key, tokens.key). It sets umask 077, chmods the folder
// 700 and stops when chmod fails. On a TrimUI that folder is on the card
// (NextUI's HOME is .userdata/<model> there), and a FAT32 or exFAT card keeps
// no permissions: chmod "works" and every file still reads rwxrwxrwx. On a
// Smart Pro S with such a card (report JH-AT4EEE, 2026-11-25) GreenOvercast
// printed "Credential store could not be read" and went from "cold_start"
// straight to "shutdown", three seconds after it started. Its source was not
// at hand, so which it was — the folder's permissions, or a damaged file (FAT
// has no journal) — is not proven; before each start JukaHub covers both:
//
//   - When the card cannot keep a folder private and the handheld's internal
//     storage (/mnt/UDISK, ext4) can, GreenOvercast's folder lives there: its
//     launch gets XDG_CONFIG_HOME=goPrivateHome. The first time, the folder's
//     small files are copied over — the saved sign-in too, unless the last
//     start could not read it — and artwork/ links back to the card, so the
//     cache never fills internal storage. The card's folder stays as it is.
//   - When the last start printed that line for the folder this start uses,
//     the saved sign-in's files are renamed aside (tokens.bin.unreadable-
//     <time>), so GreenOvercast starts signed out and shows a new code. A file
//     changed since that start is left alone, and after goStoreAsideMax
//     set-asides JukaHub stops and the card asks for a report instead.
//
// Every launch names the folder in its log notes (goSettingsNote); the next
// start and the diagnosis (gamediag_port.go) read it back. Static checks
// only; not yet run on a handheld.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// goSettingsDirName is GreenOvercast's folder under XDG_CONFIG_HOME.
	goSettingsDirName = "greenovercast"
	// goLogName is GreenOvercast's own log, in that folder.
	goLogName = "greenovercast.log"
	// goArtworkDirName is its artwork cache, in that folder.
	goArtworkDirName = "artwork"
	// goSettingsNote starts the launch note that names the folder.
	goSettingsNote = "GreenOvercast settings folder: "
	// goStoreFailText is GreenOvercast's line for a saved sign-in it could
	// not read, in lower case.
	goStoreFailText = "credential store could not be read"
	// goStoreAsideTag marks a file set aside: tokens.bin.unreadable-<time>.
	goStoreAsideTag = ".unreadable-"
	// goStoreAsideMax is how often a sign-in is set aside before the card
	// asks for a report instead.
	goStoreAsideMax = 3
	// goPrivateMinFree is the room internal storage needs for the first move.
	goPrivateMinFree = 64 << 20
	// goMoveFileMax and goMoveFilesMax bound what the first move copies.
	goMoveFileMax  = 4 << 20
	goMoveFilesMax = 64
)

// goStoreFiles are the saved sign-in's files, as the launcher names them
// (credential_file, credential_key_file).
var goStoreFiles = []string{"tokens.bin", "tokens.key"}

// goPrivateHome is the XDG_CONFIG_HOME a launch gets when the card cannot
// keep GreenOvercast's folder private: the Smart Pro's internal ext4 storage,
// where the Wine prefix lives for a like reason (wine_prefix.go).
var goPrivateHome = "/mnt/UDISK/jukahub/config"

// goPrivateSeen caches goKeepsPrivate's answer per folder for the session.
var goPrivateSeen struct {
	mu sync.Mutex
	ok map[string]bool
}

// goConfigHome is ${XDG_CONFIG_HOME:-$HOME/.config} in env, "" when neither
// is set.
func goConfigHome(env []string) string {
	if v, ok := childEnvLookup(env, "XDG_CONFIG_HOME"); ok && strings.TrimSpace(v) != "" {
		return filepath.Clean(v)
	}
	if home, ok := childEnvLookup(env, "HOME"); ok && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".config")
	}
	return ""
}

// goKeepsPrivate reports whether a folder made under dir keeps mode 700: a
// FAT32 or exFAT card shows every file with the mount's mode instead. The
// test runs in the nearest folder that exists, once a session.
func goKeepsPrivate(dir string) bool {
	at := filepath.Clean(dir)
	for {
		if info, err := os.Stat(at); err == nil && info.IsDir() {
			break
		}
		up := filepath.Dir(at)
		if up == at {
			return false
		}
		at = up
	}
	goPrivateSeen.mu.Lock()
	ok, seen := goPrivateSeen.ok[at]
	goPrivateSeen.mu.Unlock()
	if seen {
		return ok
	}
	probe, err := os.MkdirTemp(at, ".jukahub-private-")
	if err != nil {
		return false // not writable now; asked again next time
	}
	_ = os.Chmod(probe, 0o700)
	info, err := os.Lstat(probe)
	ok = err == nil && info.Mode().Perm()&0o077 == 0
	_ = os.Remove(probe)
	goPrivateSeen.mu.Lock()
	if goPrivateSeen.ok == nil {
		goPrivateSeen.ok = map[string]bool{}
	}
	goPrivateSeen.ok[at] = ok
	goPrivateSeen.mu.Unlock()
	return ok
}

// goPrivateReady reports whether goPrivateHome can take GreenOvercast's
// folder now: internal storage is there, has room and keeps files private.
func goPrivateReady() bool {
	top := filepath.Dir(filepath.Dir(goPrivateHome)) // /mnt/UDISK
	if !dirExists(top) {
		return false
	}
	if free, err := freeBytes(top); err != nil || free < goPrivateMinFree {
		return false
	}
	if err := os.MkdirAll(goPrivateHome, 0o700); err != nil {
		return false
	}
	return goKeepsPrivate(goPrivateHome)
}

// goSameDir reports two folders that are the same, neither empty.
func goSameDir(a, b string) bool {
	return a != "" && b != "" && filepath.Clean(a) == filepath.Clean(b)
}

// goStoreFile reports one of the saved sign-in's files.
func goStoreFile(name string) bool {
	for _, f := range goStoreFiles {
		if name == f {
			return true
		}
	}
	return false
}

// goMoveSettings makes privDir from the card's folder: its small files (the
// saved sign-in only withStore), and artwork/ as a link to the card's. It is
// built beside privDir and renamed into place, so a failed move leaves
// nothing half made. The card's folder is not changed, except that its
// artwork/ is made when missing.
func goMoveSettings(cardDir, privDir string, withStore bool) error {
	tmp := privDir + ".new"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	fail := func(err error) error {
		_ = os.RemoveAll(tmp)
		return err
	}
	entries, err := os.ReadDir(cardDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	copied := 0
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || name == goLogName || strings.Contains(name, goStoreAsideTag) ||
			(!withStore && goStoreFile(name)) || copied >= goMoveFilesMax {
			continue
		}
		if info, err := e.Info(); err != nil || info.Size() > goMoveFileMax {
			continue
		}
		if err := copyFile(filepath.Join(cardDir, name), filepath.Join(tmp, name)); err != nil {
			return fail(err)
		}
		copied++
	}
	// The artwork cache stays on the card; internal storage keeps a link.
	cardArt := filepath.Join(cardDir, goArtworkDirName)
	if os.MkdirAll(cardArt, 0o755) == nil {
		_ = os.Symlink(cardArt, filepath.Join(tmp, goArtworkDirName))
	}
	if err := os.Rename(tmp, privDir); err != nil {
		return fail(err)
	}
	return nil
}

// goCheckArtworkLink keeps privDir's artwork link usable: the launcher's
// mkdir -p fails on a link whose folder is gone (it then stops), so the
// folder is made again, or else the link goes and the cache starts over in
// internal storage.
func goCheckArtworkLink(privDir string) {
	link := filepath.Join(privDir, goArtworkDirName)
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return
	}
	if st, err := os.Stat(link); err == nil && st.IsDir() {
		return
	}
	if target, err := os.Readlink(link); err == nil && filepath.IsAbs(target) && os.MkdirAll(target, 0o755) == nil {
		return
	}
	_ = os.Remove(link)
}

// goStoreFailedIn reads the last launch's log (portLogOpen's, at logPath)
// and returns the settings folder whose saved sign-in GreenOvercast could
// not read then, and when that launch ended; "" when it did not say so. A
// log from before the folder was noted means the launcher's own choice,
// $HOME/.config/greenovercast (JukaHub did not set XDG_CONFIG_HOME then).
func goStoreFailedIn(logPath string) (string, time.Time) {
	if logPath == "" {
		return "", time.Time{}
	}
	info, err := os.Stat(logPath)
	if err != nil {
		return "", time.Time{}
	}
	in, failed := false, false
	home, dir := "", ""
	for _, line := range gameDiagReadTail(logPath, time.Time{}) {
		if !in {
			if v, ok := strings.CutPrefix(line, "# HOME="); ok && v != "(unset)" {
				home = strings.TrimSpace(v)
			}
			if n, ok := strings.CutPrefix(line, "# note: "); ok {
				if d := goSettingsFolderFromNotes([]string{strings.TrimSpace(n)}); d != "" {
					dir = d
				}
			}
			in = strings.TrimSpace(line) == portLogOutputMarker
			continue
		}
		if strings.Contains(strings.ToLower(line), goStoreFailText) {
			failed = true
		}
	}
	if !failed {
		return "", time.Time{}
	}
	if dir == "" && home != "" {
		dir = filepath.Join(home, ".config", goSettingsDirName)
	}
	return dir, info.ModTime()
}

// goStoreAsideCount is how often the saved sign-in in dir was set aside: the
// most .unreadable- copies one of its files has.
func goStoreAsideCount(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	most := 0
	for _, f := range goStoreFiles {
		n := 0
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), f+goStoreAsideTag) {
				n++
			}
		}
		most = max(most, n)
	}
	return most
}

// goStoreSetAside renames the saved sign-in in dir aside when GreenOvercast
// could not read it at the launch that ended at failedAt; a file changed
// since then is a newer sign-in and stays. It returns the launch note, ""
// when nothing was there to move.
func goStoreSetAside(dir string, failedAt time.Time) string {
	if n := goStoreAsideCount(dir); n >= goStoreAsideMax {
		return "GreenOvercast's saved sign-in was not set aside again: JukaHub did that " + strconv.Itoa(n) + " times already"
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	var moved []string
	for _, name := range goStoreFiles {
		p := filepath.Join(dir, name)
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		// FAT keeps times to two seconds.
		if !failedAt.IsZero() && info.ModTime().After(failedAt.Add(2*time.Second)) {
			continue
		}
		if err := os.Rename(p, p+goStoreAsideTag+stamp); err != nil {
			logWarn("greenovercast", "setting aside %s: %v", p, err)
			continue
		}
		moved = append(moved, name)
	}
	if len(moved) == 0 {
		return ""
	}
	logInfo("greenovercast", "set aside the saved sign-in its last start could not read: %s in %s", strings.Join(moved, ", "), dir)
	return "JukaHub set aside the saved sign-in GreenOvercast could not read last time (" + strings.Join(moved, ", ") +
		" → *" + goStoreAsideTag + stamp + "): it shows a new sign-in code"
}

// goSettingsFolderFromNotes is the folder a launch's notes name for
// GreenOvercast's settings, "" when they name none.
func goSettingsFolderFromNotes(notes []string) string {
	for _, n := range notes {
		if rest, ok := strings.CutPrefix(n, goSettingsNote); ok {
			dir, _, _ := strings.Cut(rest, " (")
			return strings.TrimSpace(dir)
		}
	}
	return ""
}

// goLogFromNotes is GreenOvercast's own log for a launch of script with
// these notes, "" for another port or when the notes name no folder.
func goLogFromNotes(script string, notes []string) string {
	if !x1PortName(filepath.Base(script)) {
		return ""
	}
	if dir := goSettingsFolderFromNotes(notes); dir != "" {
		return filepath.Join(dir, goLogName)
	}
	return ""
}

// goStoreFailDetail is the card's advice when a port said it could not read
// its saved sign-in; folder is GreenOvercast's settings folder for the
// launch ("" when unknown).
func goStoreFailDetail(script, folder string) string {
	if !x1PortName(filepath.Base(script)) {
		return "It closes at start when it cannot read the sign-in it saved. Remove that saved sign-in from its settings folder and it asks you to sign in again."
	}
	if folder != "" {
		if n := goStoreAsideCount(folder); n >= goStoreAsideMax {
			return "JukaHub has set GreenOvercast's saved sign-in aside " + strconv.Itoa(n) +
				" times and it still cannot read a new one, so the files are not the cause. Send this report so it can be found."
		}
		there := false
		for _, f := range goStoreFiles {
			if regularFile(filepath.Join(folder, f)) {
				there = true
			}
		}
		if !there {
			return "No saved sign-in is in this folder, yet GreenOvercast could not read one, so the files are not the cause. Send this report so it can be found."
		}
	}
	return "GreenOvercast closes at start when it cannot read the sign-in it saved. Start it again: JukaHub first sets that sign-in aside (nothing is deleted), and GreenOvercast shows a new code to sign in with on your phone."
}

// goPrepareSettings picks GreenOvercast's settings folder for a launch and
// sets aside a saved sign-in its last start could not read (the top of this
// file). Main thread, before portLogOpen empties the last launch's log.
func goPrepareSettings(plan *portLaunchPlan, name, script string) {
	if plan == nil || !goOnHandheld() || !x1PortName(filepath.Base(script)) {
		return
	}
	cardHome := goConfigHome(plan.Env)
	if cardHome == "" {
		return
	}
	cardDir := filepath.Join(cardHome, goSettingsDirName)
	failedDir, failedAt := goStoreFailedIn(portLogPath(name, script))
	dir, why := cardDir, ""
	if !goKeepsPrivate(cardDir) {
		privDir := filepath.Join(goPrivateHome, goSettingsDirName)
		usePriv := dirExists(privDir)
		if !usePriv && goPrivateReady() {
			withStore := !goSameDir(failedDir, cardDir)
			if err := goMoveSettings(cardDir, privDir, withStore); err != nil {
				logWarn("greenovercast", "moving its settings to %s: %v", privDir, err)
			} else {
				usePriv = true
				logInfo("greenovercast", "settings moved from %s to %s: the card cannot keep them private (saved sign-in copied: %t)",
					cardDir, privDir, withStore)
				if !withStore {
					plan.Notes = append(plan.Notes, "GreenOvercast's saved sign-in stayed on the card: its last start could not read it")
				}
			}
		}
		if usePriv {
			goCheckArtworkLink(privDir)
			plan.Env = mergeGameProfileEnv(plan.Env, map[string]string{"XDG_CONFIG_HOME": goPrivateHome})
			dir, why = privDir, "internal storage, as the card cannot keep it private; artwork stays on the card"
		} else {
			why = "on the card, which cannot keep it private; internal storage could not take it"
		}
	}
	if goSameDir(failedDir, dir) {
		if note := goStoreSetAside(dir, failedAt); note != "" {
			plan.Notes = append(plan.Notes, note)
		}
	}
	note := goSettingsNote + dir
	if why != "" {
		note += " (" + why + ")"
	}
	plan.Notes = append(plan.Notes, note)
}
