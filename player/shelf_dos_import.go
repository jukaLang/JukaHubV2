package main

// shelf_dos_import.go — Files' "Add to DOS games": a DOS game found anywhere
// (a folder off a PC, a .zip from a download) copied into this card's
// Roms/DOS, where the DOS shelf lists it.
//
// The copy runs on a goroutine with a progress toast, checks the free space
// first, never writes over anything (a taken name becomes "Name (2)"), copies
// no links, and removes its half-made copy when it fails; it copies under a
// hidden name and renames the copy when whole, so one cut short by a pulled
// card is never listed (the next import removes it). The destination is
// the DOS folder of the library's first Roms root (the SD card on a handheld,
// the portable Roms folder on a PC), created when there is none. A folder
// must hold a program (.exe, .com, .bat) or something DOSBox Pure opens;
// anything else is refused with a reason, not copied.
//
// The Files sheet's row is one more action kind, numbered past the sheet's
// own list (feActShelfDOS), with two small hooks in files_manager.go.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// feActShelfDOS is the Files sheet's "Add to DOS games" row, numbered well
// past files_manager.go's own kinds so a new kind there cannot collide.
const feActShelfDOS feActionKind = 1000

const (
	// shelfImportMargin is the free space a copy leaves on the card.
	shelfImportMargin = 32 << 20
	// shelfImportMaxDepth and shelfImportMaxFiles bound a folder's walk.
	shelfImportMaxDepth = 24
	shelfImportMaxFiles = 200000
	// shelfImportToastEvery spaces the progress toasts.
	shelfImportToastEvery = time.Second
	// shelfImportTempPrefix names the hidden folder (or file) a copy is made
	// in before it takes its name: the scan skips dot names, so a copy cut
	// short by a pulled card or a power cut is never listed, and the next
	// import clears it away.
	shelfImportTempPrefix = ".jukahub-adding-"
)

var shelfImport struct {
	mu      sync.Mutex
	running bool
}

// shelfDOSImportable reports whether a file is a whole DOS game DOSBox Pure
// opens as one (.zip, .dosz).
func shelfDOSImportable(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".zip" || ext == ".dosz"
}

// shelfDOSFilesAction is the Files sheet's row for the marked folders and DOS
// archives; ok is false (no row) for anything else or inside a DOS folder.
func shelfDOSFilesAction(targets []FileEntry) (feAction, bool) {
	if len(targets) == 0 {
		return feAction{}, false
	}
	for _, t := range targets {
		if t.Name == ".." || (!t.IsDir && !shelfDOSImportable(t.Path)) {
			return feAction{}, false
		}
		if _, system, ok := shelfConsoleOf(filepath.Join(t.Path, "x")); ok && system == "DOS" {
			return feAction{}, false // already there
		}
	}
	detail := targets[0].Name
	if len(targets) > 1 {
		detail = feItemCountLabel(len(targets))
	}
	shelfImport.mu.Lock()
	running := shelfImport.running
	shelfImport.mu.Unlock()
	if running {
		detail = "copying another game…"
	}
	return feAction{Kind: feActShelfDOS, Label: "Add to DOS games", Detail: detail, Enabled: !running}, true
}

// shelfDOSImportStart copies the marked entries on a goroutine.
func shelfDOSImportStart(targets []FileEntry) {
	var paths []string
	for _, t := range targets {
		if t.Name != ".." {
			paths = append(paths, t.Path)
		}
	}
	if len(paths) == 0 {
		return
	}
	shelfImport.mu.Lock()
	if shelfImport.running {
		shelfImport.mu.Unlock()
		ShowToast("Still copying the last game — one moment", ToastKindInfo)
		return
	}
	shelfImport.running = true
	shelfImport.mu.Unlock()
	ShowToast("Adding to DOS games…", ToastKindInfo)
	go shelfDOSImportRun(paths)
}

// shelfDOSImportRun is the copy's goroutine.
func shelfDOSImportRun(paths []string) {
	added := 0
	var firstErr error
	defer func() {
		if r := recover(); r != nil {
			firstErr = fmt.Errorf("stopped: %v", r)
		}
		shelfImport.mu.Lock()
		shelfImport.running = false
		shelfImport.mu.Unlock()
		done, err := added, firstErr
		tbxPost(func() {
			switch {
			case err != nil && done == 0:
				ShowToast("Cannot add to DOS games: "+err.Error(), ToastKindError)
			case err != nil:
				ShowToast(fmt.Sprintf("Added %d to DOS games, one failed: %v", done, err), ToastKindWarning)
			case done == 1:
				ShowToast("Added to DOS games — it is on the DOS shelf now", ToastKindSuccess)
			default:
				ShowToast(fmt.Sprintf("Added %d games to DOS games", done), ToastKindSuccess)
			}
			if done > 0 {
				libraryRescanSoon(libraryRescanTries)
			}
		})
	}()
	consoleDir, err := shelfDOSConsoleDir()
	if err != nil {
		firstErr = err
		return
	}
	shelfDOSImportClean(consoleDir)
	for _, src := range paths {
		if err := shelfDOSImportOne(src, consoleDir); err != nil {
			logWarn("library", "add to DOS games: %s: %v", src, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		added++
	}
}

// shelfDOSConsoleDir is the DOS folder of the library's first Roms root,
// made when there is none.
func shelfDOSConsoleDir() (string, error) {
	roots := romLibraryRoots()
	if len(roots) == 0 {
		return "", fmt.Errorf("no Roms folder")
	}
	root := roots[0]
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// A NextUI "Name (DOS)" folder only where NextUI runs: the scan
			// reads tagged folders nowhere else (scanROMLibrary).
			if s, tagged, ok := romFolderSystem(e.Name(), nil); ok && s.Name == "DOS" && (!tagged || nextuiActive()) {
				return filepath.Join(root, e.Name()), nil
			}
		}
	}
	dir := filepath.Join(root, "DOS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot make %s: %w", dir, err)
	}
	return dir, nil
}

// shelfDOSImportClean removes what a copy cut short left in consoleDir: only
// the names starting with shelfImportTempPrefix, which are this feature's
// own. One import runs at a time, so none of them is in use.
func shelfDOSImportClean(consoleDir string) {
	entries, err := os.ReadDir(consoleDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), shelfImportTempPrefix) {
			continue
		}
		p := filepath.Join(consoleDir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			logWarn("library", "cannot remove the unfinished copy %s: %v", p, err)
		}
	}
}

// shelfDOSCleanName is a name the card's file system takes: no characters
// FAT refuses, no trailing dots or spaces, single spaces.
func shelfDOSCleanName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32 || strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	clean := strings.Join(strings.Fields(b.String()), " ")
	clean = strings.TrimRight(clean, ". ")
	if clean == "" {
		clean = "DOS game"
	}
	return clean
}

// shelfDOSImportOne copies one folder or archive into consoleDir.
func shelfDOSImportOne(src, consoleDir string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	name := filepath.Base(src)
	if info.Mode()&os.ModeSymlink != 0 {
		// The copy copies no links: a linked folder would arrive empty.
		return fmt.Errorf("%s is a link — add the folder or file it points to", name)
	}
	if info.IsDir() {
		if rel, err := filepath.Rel(src, consoleDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%s holds the DOS folder itself", name)
		}
		progs, others := shelfDOSPrograms(src)
		if len(progs) == 0 && len(others) == 0 {
			return fmt.Errorf("%s has no DOS program in it", name)
		}
		name = shelfDOSCleanName(name)
	} else {
		ext := strings.ToLower(filepath.Ext(name))
		name = shelfDOSCleanName(strings.TrimSuffix(name, filepath.Ext(name))) + ext
	}
	total, err := shelfImportSize(src)
	if err != nil {
		return err
	}
	if free, err := filesFreeSpace(consoleDir); err == nil && free < total+shelfImportMargin {
		return fmt.Errorf("%s needs %s and the card has %s free", name, humanBytes(total), humanBytes(free))
	}
	// The copy is made under a hidden name of this feature's own, so only
	// that is ever removed on a failure, and it takes the game's name when
	// whole.
	tmp := filepath.Join(consoleDir, shelfImportTempPrefix+name)
	prog := &shelfImportProgress{total: total, name: strings.TrimSuffix(name, filepath.Ext(name)), last: time.Now()}
	if err := shelfImportCopy(src, tmp, prog, 0); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	// The name is picked now, after the copy, so a game added meanwhile is
	// not written over.
	dst, err := feFreeTarget(consoleDir, name)
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	syncDirectory(consoleDir)
	logInfo("library", "added to DOS games: %s → %s (%s)", src, dst, humanBytes(total))
	return nil
}

// shelfImportSize is the bytes a copy writes (links are not followed).
func shelfImportSize(src string) (int64, error) {
	var total int64
	files := 0
	err := filepath.WalkDir(src, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		files++
		if files > shelfImportMaxFiles {
			return fmt.Errorf("more than %d files", shelfImportMaxFiles)
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// shelfImportProgress counts the bytes written and toasts now and then.
type shelfImportProgress struct {
	total, done int64
	name        string
	last        time.Time
}

func (p *shelfImportProgress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.total > 0 && time.Since(p.last) >= shelfImportToastEvery {
		p.last = time.Now()
		msg := fmt.Sprintf("Adding %s to DOS games… %d%%", p.name, p.done*100/p.total)
		tbxPost(func() { ShowToast(msg, ToastKindInfo) })
	}
	return len(b), nil
}

// shelfImportCopy copies src to dst, which must not exist.
func shelfImportCopy(src, dst string, prog *shelfImportProgress, depth int) error {
	if depth > shelfImportMaxDepth {
		return fmt.Errorf("folders nested too deep")
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := os.Mkdir(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := shelfImportCopy(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), prog, depth+1); err != nil {
				return err
			}
		}
		return nil
	case !info.Mode().IsRegular():
		return nil // links and devices stay behind
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.MultiWriter(out, prog), in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
