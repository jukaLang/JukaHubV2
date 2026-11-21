package main

// cardtidy.go — "Tidy the card at start": the litter a Mac or Windows leaves
// on the SD card is removed once per boot.
//
// Copying games from a Mac leaves a "._Name" AppleDouble file beside every
// file and a .DS_Store in every folder; Windows leaves Thumbs.db and
// desktop.ini; a Mac that mounted the card leaves .Trashes, .Spotlight-V100,
// .fseventsd and .TemporaryItems at its top. JukaHub's scans already skip them
// (filejunk.go), but other programs on the card list them as games, and they
// take space and folder entries on FAT. Once per boot, after the first frame
// and in the background (deferStartupTask), this removes:
//   - inside the user's content folders only (cardTidyTrees: Roms, Music,
//     Audiobooks, Videos, Pictures, Screenshots, Saves, BIOS, Overlays; never
//     Apps, Emus, RetroArch, System, PortMaster or any other program folder):
//     "._*" files that really are AppleDouble (they start with 00 05 16 07)
//     and are at most 64 KB, .DS_Store, Thumbs.db and desktop.ini;
//   - at the card's top: the same files beside the folders, and the macOS
//     folders .Trashes, .Spotlight-V100, .fseventsd and .TemporaryItems,
//     whole. Never "System Volume Information".
// It is bounded (cardTidyBudget, cardTidyMaxDepth, cardTidyMaxEntries),
// follows no symlink, stops after a run of failed removals, skips a card
// mounted read-only, runs on the handheld only, and logs what it removed.
// Settings → About → "Tidy the card at start" (beside Card check, on by
// default) turns it off.
// The Library doctor's "Remove junk files" (plugnative_tools.go) stays the
// by-hand clean of the Roms folders, __MACOSX folders included; this is the
// automatic one and stricter about "._" files (the magic, the size).

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// cardTidySetting is the Settings switch (Custom, default on).
	cardTidySetting = "cardTidyAtStart"
	// cardTidyBudget bounds one run.
	cardTidyBudget = 20 * time.Second
	// cardTidyMaxDepth bounds how deep a content folder is walked.
	cardTidyMaxDepth = 8
	// cardTidyMaxEntries bounds how many names one run looks at.
	cardTidyMaxEntries = 300000
	// cardTidyMaxAppleDouble is the largest "._" file taken as litter.
	cardTidyMaxAppleDouble = 64 << 10
	// cardTidyMaxFailures stops a run whose removals keep failing.
	cardTidyMaxFailures = 25
	// cardTidyMarker, in the temporary folder (emptied by a reboot), holds
	// the boot id of the boot the card was tidied in.
	cardTidyMarker = "jukahub-cardtidy.boot"
)

// cardTidyTrees are the content folders at the card's top that are tidied,
// matched in any case.
var cardTidyTrees = []string{"Roms", "Music", "Audiobooks", "Videos", "Pictures", "Screenshots", "Saves", "BIOS", "Overlays"}

// cardTidyRootDirs are the macOS folders removed whole at the card's top.
var cardTidyRootDirs = []string{".Trashes", ".Spotlight-V100", ".fseventsd", ".TemporaryItems"}

// cardTidyAppleDoubleMagic starts every AppleDouble file.
var cardTidyAppleDoubleMagic = []byte{0x00, 0x05, 0x16, 0x07}

// cardTidyRan keeps a session to one run, also where there is no boot id.
var cardTidyRan atomic.Bool

func init() {
	deferStartupTask("card-tidy", cardTidyStartup)
}

// cardTidyEnabled reads the switch (main thread).
func cardTidyEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	if v, ok := c.Variables.Custom[cardTidySetting]; ok {
		return coerceBool(v)
	}
	return true
}

// cardTidySetEnabled stores the switch (main thread).
func cardTidySetEnabled(c *Config, on bool) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[cardTidySetting] = on
	saveUserConfigDebounced(userConfigFromVariables(c))
}

// cardTidySettingsRow is the Settings row (About, beside Card check).
func cardTidySettingsRow() setRow {
	return setRow{
		ID: "cardTidy", Label: "Tidy the card at start", Kind: setKindToggle,
		Detail:  setStatic("Once per start: the ._ files, .DS_Store and Thumbs.db a Mac or PC leaves in your game and media folders"),
		Visible: func(*Config) bool { return cardCheckSupported() },
		Get:     cardTidyEnabled,
		Set: func(c *Config, on bool) {
			cardTidySetEnabled(c, on)
			setToast("Tidy the card at start: " + setOnOff(on))
		},
	}
}

// cardTidyStartup runs after the first frame (main thread): it reads the
// switch and hands the walk to a goroutine.
func cardTidyStartup() {
	if !cardTidyEnabled(appConfig) || !cardCheckSupported() || cardTidyRan.Load() {
		return
	}
	root := strings.TrimSpace(fileServerCardRoot())
	if root == "" {
		return
	}
	cardTidyRan.Store(true)
	go cardTidyOnce(root)
}

// cardTidyOnce runs the tidy unless this boot already did.
func cardTidyOnce(root string) {
	marker := filepath.Join(os.TempDir(), cardTidyMarker)
	boot := strings.TrimSpace(bootOSBootIDFn())
	if boot != "" {
		if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == boot {
			return
		}
	}
	if cardTidyReadOnly(root) {
		logInfo("cardtidy", "skipped: the card is mounted read-only")
		return
	}
	res := cardTidyRun(root, time.Now().Add(cardTidyBudget))
	if boot != "" {
		_ = os.WriteFile(marker, []byte(boot+"\n"), 0o644)
	}
	if res.total() == 0 && !res.stopped {
		logDebug("cardtidy", "nothing to remove (%d names looked at)", res.seen)
		return
	}
	logInfo("cardtidy", "removed %d AppleDouble ._ files, %d .DS_Store, %d Thumbs.db/desktop.ini and %d macOS folders; %d names looked at in %.1f s%s",
		res.appleDouble, res.dsStore, res.windows, res.macDirs, res.seen, res.took.Seconds(), res.note())
}

// cardTidyResult counts one run.
type cardTidyResult struct {
	appleDouble int
	dsStore     int
	windows     int
	macDirs     int
	failed      int
	seen        int
	stopped     bool   // the budget, the entry cap or failures ended it early
	why         string // what ended it early
	took        time.Duration
}

func (r *cardTidyResult) total() int { return r.appleDouble + r.dsStore + r.windows + r.macDirs }

func (r *cardTidyResult) note() string {
	s := ""
	if r.failed > 0 {
		s += "; some could not be removed"
	}
	if r.stopped {
		s += "; stopped early (" + r.why + ")"
	}
	return s
}

// cardTidyRun tidies the card at root until deadline.
func cardTidyRun(root string, deadline time.Time) cardTidyResult {
	start := time.Now()
	var res cardTidyResult
	entries, err := os.ReadDir(root)
	if err != nil {
		return res
	}
	var trees []string
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(root, name)
		res.seen++
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if e.IsDir() {
			if cardTidyIsRootDir(name) {
				if err := os.RemoveAll(full); err != nil {
					res.failed++
					logDebug("cardtidy", "cannot remove %s: %v", name, err)
				} else {
					res.macDirs++
				}
				continue
			}
			if cardTidyIsTree(name) {
				trees = append(trees, full)
			}
			continue
		}
		cardTidyFile(&res, full, name)
	}
	for _, dir := range trees {
		if !cardTidyWalk(&res, dir, 0, deadline) {
			break
		}
	}
	res.took = time.Since(start)
	return res
}

// cardTidyWalk tidies dir and its folders; false once the run must stop.
func cardTidyWalk(res *cardTidyResult, dir string, depth int, deadline time.Time) bool {
	switch {
	case time.Now().After(deadline):
		res.stopped, res.why = true, "time"
		return false
	case res.seen >= cardTidyMaxEntries:
		res.stopped, res.why = true, "too many files"
		return false
	case res.failed >= cardTidyMaxFailures:
		res.stopped, res.why = true, "removals failing"
		return false
	}
	f, err := os.Open(dir)
	if err != nil {
		return true
	}
	var subdirs []string
	for {
		batch, err := f.ReadDir(256)
		for _, e := range batch {
			res.seen++
			name := e.Name()
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			full := filepath.Join(dir, name)
			if e.IsDir() {
				// "__MACOSX" folders hold a zip's own AppleDouble files; they
				// are the user's unpacked archive, so only their litter goes.
				if depth+1 < cardTidyMaxDepth {
					subdirs = append(subdirs, full)
				}
				continue
			}
			cardTidyFile(res, full, name)
		}
		if err != nil || len(batch) == 0 {
			break
		}
		if res.seen >= cardTidyMaxEntries || res.failed >= cardTidyMaxFailures {
			break
		}
		if time.Now().After(deadline) {
			// One huge folder must not outlast the budget either.
			res.stopped, res.why = true, "time"
			f.Close()
			return false
		}
	}
	f.Close()
	for _, sub := range subdirs {
		if !cardTidyWalk(res, sub, depth+1, deadline) {
			return false
		}
	}
	return true
}

// cardTidyFile removes full when it is litter.
func cardTidyFile(res *cardTidyResult, full, name string) {
	lower := strings.ToLower(name)
	var count *int
	switch {
	case lower == ".ds_store":
		count = &res.dsStore
	case lower == "thumbs.db" || lower == "desktop.ini":
		count = &res.windows
	case strings.HasPrefix(name, "._") && len(name) > 2:
		if !cardTidyIsAppleDouble(full) {
			return
		}
		count = &res.appleDouble
	default:
		return
	}
	if err := os.Remove(full); err != nil {
		if !os.IsNotExist(err) {
			res.failed++
			if res.failed <= 3 {
				logDebug("cardtidy", "cannot remove %s: %v", full, err)
			}
		}
		return
	}
	*count++
}

// cardTidyIsAppleDouble reports a regular file of at most
// cardTidyMaxAppleDouble bytes that starts with the AppleDouble magic.
func cardTidyIsAppleDouble(full string) bool {
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() || info.Size() < int64(len(cardTidyAppleDoubleMagic)) || info.Size() > cardTidyMaxAppleDouble {
		return false
	}
	f, err := os.Open(full)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(cardTidyAppleDoubleMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return bytes.Equal(head, cardTidyAppleDoubleMagic)
}

// cardTidyIsTree reports a content folder at the card's top.
func cardTidyIsTree(name string) bool {
	for _, t := range cardTidyTrees {
		if strings.EqualFold(name, t) {
			return true
		}
	}
	return false
}

// cardTidyIsRootDir reports a macOS folder removed whole at the card's top.
// "System Volume Information" is Windows' and is never one.
func cardTidyIsRootDir(name string) bool {
	for _, d := range cardTidyRootDirs {
		if name == d {
			return true
		}
	}
	return false
}

// cardTidyReadOnly reports whether root's file system is mounted read-only,
// from /proc/mounts (the longest mount point holding root). Unknown is
// "writable": the removals then fail and the run stops by itself.
func cardTidyReadOnly(root string) bool {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return false
	}
	defer f.Close()
	path := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		path = real
	}
	path = filepath.Clean(path)
	best, ro := "", false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		mnt := strings.ReplaceAll(fields[1], `\040`, " ")
		if mnt != "/" && path != mnt && !strings.HasPrefix(path, mnt+"/") {
			continue
		}
		if len(mnt) < len(best) {
			continue
		}
		best = mnt
		ro = false
		for _, opt := range strings.Split(fields[3], ",") {
			if opt == "ro" {
				ro = true
			}
		}
	}
	return ro
}
