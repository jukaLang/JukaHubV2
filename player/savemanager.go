package main

// savemanager.go — save management: find the folders emulators write saves
// into, snapshot them before they can be lost, restore a snapshot, mark the
// folders for Syncthing, and package the newest snapshot for a phone.
//
// The device is the only copy of most people's progress, and the SD card is
// the thing that fails first. Everything here is deliberately local: a
// snapshot is a plain directory with a JSON manifest and the same relative
// paths the emulator used, so it can be inspected, copied to a PC, or restored
// without this app. Syncthing is not bundled or configured behind the user's
// back — the panel writes the folder markers and the exact folder list, and
// the user adds those folders in Syncthing on both devices.

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	saveManagerDirName = "save-manager"
	// saveSnapshotKeep is how many point-in-time copies are retained. A save
	// state is usually a few megabytes; ten copies covers "it broke after the
	// last update" without filling a small card.
	saveSnapshotKeep = 10
	// A single file or a whole snapshot larger than these is almost certainly
	// a video or an ISO sitting in a save folder, not a save. Refuse instead
	// of silently copying a card full of data.
	saveManagerMaxFile  = 256 << 20
	saveManagerMaxTotal = 1 << 30
)

// Overridable for tests, like the other data-folder features.
var (
	saveManagerDirOverride    string
	saveManagerSDRootOverride string
)

var errSaveManagerEmpty = errors.New("the save folders are empty")

// saveRoot is one directory an emulator writes saves into.
type saveRoot struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// saveSnapshotFile is one file inside a snapshot, with the hash needed to tell
// a successful restore from a half-written one.
type saveSnapshotFile struct {
	Path   string `json:"path"` // slash-separated, relative to the root
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// saveSnapshotRoot groups the files that came from one emulator folder.
type saveSnapshotRoot struct {
	Name  string             `json:"name"`
	Slug  string             `json:"slug"` // folder name inside the snapshot
	Path  string             `json:"path"` // source path at capture time
	Bytes int64              `json:"bytes"`
	Files []saveSnapshotFile `json:"files"`
}

// saveSnapshotManifest is the readable index that makes a snapshot restorable
// without a database.
type saveSnapshotManifest struct {
	Version    int                `json:"version"`
	ID         string             `json:"id"`
	Label      string             `json:"label"`
	Created    time.Time          `json:"created"`
	TotalBytes int64              `json:"totalBytes"`
	Roots      []saveSnapshotRoot `json:"roots"`
}

// saveSnapshot is a manifest plus the directory that holds its files.
type saveSnapshot struct {
	Manifest saveSnapshotManifest
	Dir      string
}

func saveManagerDir() string {
	if saveManagerDirOverride != "" {
		return saveManagerDirOverride
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, saveManagerDirName)
	}
	return filepath.Join("data", saveManagerDirName)
}

func saveManagerSnapshotsDir() string { return filepath.Join(saveManagerDir(), "snapshots") }
func saveManagerSyncDir() string      { return filepath.Join(saveManagerDir(), "sync") }
func saveManagerResumeDir() string    { return filepath.Join(saveManagerDir(), "resume") }

// InitSaveManager creates the snapshot/sync/resume folders once. It is
// best-effort: a read-only card must not stop the hub from booting.
func InitSaveManager() {
	if err := ensureSaveManagerLayout(); err != nil {
		logWarn("saves", "setup failed: %v", err)
	}
}

func ensureSaveManagerLayout() error {
	for _, dir := range []string{saveManagerDir(), saveManagerSnapshotsDir(), saveManagerSyncDir(), saveManagerResumeDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// saveManagerSDRoot finds the card the app is installed on. The environment
// override and the executable-relative lookup come from the autostart code;
// the fixed paths are the fallback on a desktop or a fresh checkout.
func saveManagerSDRoot() string {
	if saveManagerSDRootOverride != "" {
		return saveManagerSDRootOverride
	}
	if root := strings.TrimSpace(trimuiSDRootFn()); root != "" {
		return root
	}
	for _, root := range []string{"/mnt/SDCARD", "/mnt/sdcard"} {
		if dirExists(root) {
			return root
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// resolveRetroArchSaveDir turns a retroarch.cfg directory value into a real
// path. `:` is RetroArch's "config directory" prefix, `~` is the home folder,
// and a relative path is resolved beside the cfg file, which is where
// RetroArch resolves it in practice.
func resolveRetroArchSaveDir(value, cfgPath string) string {
	v := strings.TrimSpace(strings.Trim(value, `"'`))
	// Leading ":" means "the folder holding retroarch.cfg", so it anchors to
	// the config file rather than to the filesystem root.
	configRelative := strings.HasPrefix(v, ":")
	v = strings.TrimPrefix(v, ":")
	if v == "" || strings.EqualFold(v, "default") || strings.EqualFold(v, "disabled") {
		return ""
	}
	if configRelative && cfgPath != "" {
		return filepath.Clean(filepath.Join(filepath.Dir(cfgPath), strings.TrimLeft(v, `/\`)))
	}
	if strings.HasPrefix(v, "~") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			v = filepath.Join(home, strings.TrimPrefix(v, "~"))
		}
	}
	if !filepath.IsAbs(v) {
		if cfgPath == "" {
			return ""
		}
		v = filepath.Join(filepath.Dir(cfgPath), v)
	}
	return filepath.Clean(v)
}

// saveRootsFor is the pure discovery step. It only returns directories that
// actually exist, so the panel never offers an empty path that would later
// fail. configText/configPath may be empty (no RetroArch installed).
func saveRootsFor(sdRoot, configText, configPath string) []saveRoot {
	var roots []saveRoot
	var infos []os.FileInfo // infos[i] describes roots[i]
	add := func(name, path string) {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "" || path == "." {
			return
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return
		}
		// A snapshot directory inside a save root would copy itself. The data
		// folder normally lives under Apps/JukaHub, but a portable build puts
		// it beside the executable, which can be the SD root.
		if isPathWithin(path, saveManagerDir()) || isPathWithin(saveManagerDir(), path) {
			return
		}
		// One folder under two spellings is one root: FAT ignores case, so
		// RetroArch/saves and retroarch/saves can be the same folder, which was
		// then copied twice and synced under two keys.
		for i, existing := range roots {
			if existing.Path == path || os.SameFile(infos[i], info) {
				return
			}
		}
		roots = append(roots, saveRoot{Name: name, Path: path})
		infos = append(infos, info)
	}

	if configText != "" {
		if v, ok := retroArchConfigValue(configText, "savefile_directory"); ok {
			add("RetroArch saves", resolveRetroArchSaveDir(v, configPath))
		}
		if v, ok := retroArchConfigValue(configText, "savestate_directory"); ok {
			add("RetroArch states", resolveRetroArchSaveDir(v, configPath))
		}
	}

	if sdRoot != "" {
		for _, c := range []struct{ name, rel string }{
			{"RetroArch saves", "RetroArch/saves"},
			{"RetroArch states", "RetroArch/states"},
			{"RetroArch saves", "RetroArch/.retroarch/saves"},
			{"RetroArch states", "RetroArch/.retroarch/states"},
			{"RetroArch saves", "retroarch/saves"},
			{"RetroArch states", "retroarch/states"},
			{"PPSSPP savedata", "PSP/SAVEDATA"},
			{"PPSSPP states", "PSP/PPSSPP_STATE"},
			{"PPSSPP savedata", "ppsspp/PSP/SAVEDATA"},
			{"PPSSPP states", "ppsspp/PSP/PPSSPP_STATE"},
			{"PPSSPP savedata", "Apps/PPSSPP/PSP/SAVEDATA"},
			{"PPSSPP states", "Apps/PPSSPP/PSP/PPSSPP_STATE"},
			{"PPSSPP savedata", "Emus/PPSSPP/PSP/SAVEDATA"},
			{"PPSSPP states", "Emus/PPSSPP/PSP/PPSSPP_STATE"},
			{"DraStic backups", "DraStic/backup"},
			{"DraStic states", "DraStic/savestates"},
			{"DraStic backups", "drastic/backup"},
			{"DraStic states", "drastic/savestates"},
			{"Flycast VMU", "Flycast/vmu"},
			{"Flycast VMU", "flycast/vmu"},
			{"Flycast saves", "Flycast/saves"},
		} {
			add(c.name, filepath.Join(sdRoot, filepath.FromSlash(c.rel)))
		}
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, c := range []struct{ name, rel string }{
			{"PPSSPP savedata", ".config/ppsspp/PSP/SAVEDATA"},
			{"PPSSPP states", ".config/ppsspp/PSP/PPSSPP_STATE"},
			{"PPSSPP savedata", ".local/share/ppsspp/PSP/SAVEDATA"},
			{"PPSSPP states", ".local/share/ppsspp/PSP/PPSSPP_STATE"},
			{"RetroArch saves", ".config/retroarch/saves"},
			{"RetroArch states", ".config/retroarch/states"},
		} {
			add(c.name, filepath.Join(home, filepath.FromSlash(c.rel)))
		}
	}

	if sdRoot != "" {
		addPPSSPPMemstickRoots(sdRoot, roots, add)
	}

	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Name != roots[j].Name {
			return roots[i].Name < roots[j].Name
		}
		return roots[i].Path < roots[j].Path
	})
	return roots
}

// addPPSSPPMemstickRoots adds the saves of every PPSSPP kept in an emulator
// folder on the card: CrossMix's Emus/PSP/PPSSPP_<version>/.config/ppsspp/PSP,
// <SD>/.config/ppsspp/PSP on the first CrossMix images (ppssppEmuMemsticks).
// Without them a CrossMix card's PSP saves were in no snapshot, backup or sync
// set. roots is what saveRootsFor found before; add is its adder.
//
// The plain "PPSSPP savedata" and "PPSSPP states" names are what snapshot and
// backup slugs and the cross-device sync keys are made of, with -2, -3 handed
// out in path order, so only one memstick may use them, and it has to stay the
// same one from launch to launch: the first memstick that holds saves, in
// ppssppEmuMemsticks order (the newest version first) — never the build played
// last, which would move the key each time a game picks another build. And
// only when no root found before already has the plain names: a folder that
// had a key keeps it. Every other memstick is named after its place on the
// card, so its key is its own.
func addPPSSPPMemstickRoots(sdRoot string, roots []saveRoot, add func(name, path string)) {
	memsticks := ppssppEmuMemsticks([]string{sdRoot})
	if len(memsticks) == 0 {
		return
	}
	owner := ""
	for _, memstick := range memsticks {
		if dirExists(filepath.Join(memstick.Dir, "SAVEDATA")) || dirExists(filepath.Join(memstick.Dir, "PPSSPP_STATE")) {
			owner = memstick.Dir
			break
		}
	}
	plainTaken := false
	for _, root := range roots {
		if root.Name == "PPSSPP savedata" || root.Name == "PPSSPP states" {
			plainTaken = true
		}
	}
	for _, memstick := range memsticks {
		suffix := ""
		if plainTaken || memstick.Dir != owner {
			suffix = " (" + ppssppMemstickLabel(sdRoot, memstick) + ")"
		}
		add("PPSSPP savedata"+suffix, filepath.Join(memstick.Dir, "SAVEDATA"))
		add("PPSSPP states"+suffix, filepath.Join(memstick.Dir, "PPSSPP_STATE"))
	}
}

// ppssppMemstickLabel names a memstick by its place on the card: its home
// ("Emus/PSP/PPSSPP_1.15.4", "card root") for the .config/ppsspp/PSP layout,
// and the folder itself ("Emus/PSP/PPSSPP_1.15.4/PSP") for a bare PSP folder,
// so the two layouts of one home never share a name.
func ppssppMemstickLabel(sdRoot string, memstick ppssppMemstick) string {
	place := memstick.Home
	if filepath.Dir(memstick.Dir) == filepath.Clean(memstick.Home) {
		place = memstick.Dir
	}
	rel, err := filepath.Rel(sdRoot, place)
	if err != nil || rel == "." {
		return "card root"
	}
	return filepath.ToSlash(rel)
}

// saveManagerRoots reads the live discoverable set. The RetroArch config is
// read once so an override in retroarch.cfg is honoured.
func saveManagerRoots() []saveRoot {
	cfgText, cfgPath, _ := retroArchConfig()
	return saveRootsFor(saveManagerSDRoot(), cfgText, cfgPath)
}

// saveRootSlug is the folder name used inside a snapshot. It is stable for a
// root name and safe on FAT32.
func saveRootSlug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "root"
	}
	return out
}

func uniqueSnapshotID(dir string) string {
	base := time.Now().Format("20060102-150405")
	id := base
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, id)); os.IsNotExist(err) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

// copySaveFile copies one file and hashes it in the same pass. The limit is
// checked before the copy so a huge file is refused instead of filling the
// card with a partial snapshot.
func copySaveFile(src, dst string, max int64) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("not a regular file")
	}
	if max > 0 && info.Size() > max {
		return "", 0, fmt.Errorf("file is larger than %s", humanBytes(max))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return "", n, copyErr
	}
	if syncErr != nil {
		return "", n, syncErr
	}
	if closeErr != nil {
		return "", n, closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// isSaveFile reports whether a path should be captured. Syncthing's own marker
// files, OS metadata and partial downloads are not saves.
func isSaveFile(name string) bool {
	base := filepath.Base(name)
	switch strings.ToLower(base) {
	case ".stfolder", ".stignore", ".ds_store", "desktop.ini", "thumbs.db":
		return false
	}
	return !strings.HasSuffix(strings.ToLower(base), ".tmp") &&
		!strings.HasSuffix(strings.ToLower(base), ".part")
}

// createSaveSnapshot copies every root into a new timestamped folder. The
// manifest is written last, inside the staging folder, and the folder is
// renamed into place only after the copy succeeded, so a full card or a
// yanked cable leaves no half snapshot behind.
func createSaveSnapshot(roots []saveRoot, label string) (saveSnapshot, error) {
	return createSaveSnapshotKeeping(roots, label)
}

// createSaveSnapshotKeeping is createSaveSnapshot whose pruning spares the
// snapshots named in keep as well as the new one: a restore's safety snapshot
// must not delete the snapshot that restore is about to read.
func createSaveSnapshotKeeping(roots []saveRoot, label string, keep ...string) (saveSnapshot, error) {
	return createSaveSnapshotFiltered(roots, label, nil, keep...)
}

// createSaveSnapshotOf is createSaveSnapshot limited to the files only accepts
// (the folder's path as listed in roots, and the slash-separated path inside
// it). Save sync (ssync.go) keeps exactly the saves it is about to overwrite
// this way, instead of copying every save folder before each sync. Restoring
// it writes those files back and leaves every other save alone. A filter that
// matches nothing returns errSaveManagerEmpty.
func createSaveSnapshotOf(roots []saveRoot, label string, only func(rootPath, rel string) bool) (saveSnapshot, error) {
	return createSaveSnapshotFiltered(roots, label, only)
}

// createSaveSnapshotFiltered is the snapshot itself; a nil only takes every
// save file.
func createSaveSnapshotFiltered(roots []saveRoot, label string, only func(rootPath, rel string) bool, keep ...string) (saveSnapshot, error) {
	if err := ensureSaveManagerLayout(); err != nil {
		return saveSnapshot{}, err
	}
	if len(roots) == 0 {
		return saveSnapshot{}, errors.New("no save folders found yet — start a game once")
	}
	id := uniqueSnapshotID(saveManagerSnapshotsDir())
	staging := filepath.Join(saveManagerSnapshotsDir(), ".tmp-"+id)
	if err := os.RemoveAll(staging); err != nil {
		return saveSnapshot{}, err
	}
	created := time.Now().UTC()
	// A handheld that lost its clock (no network time yet after a power cycle)
	// stamps a new snapshot before the ones it already keeps. Sorted by that
	// stamp the new one was the oldest, and pruning deleted it the moment it
	// was written. The stamp never goes back past the newest kept snapshot.
	if kept := listSaveSnapshots(); len(kept) > 0 && !created.After(kept[0].Manifest.Created) {
		created = kept[0].Manifest.Created.Add(time.Second)
	}
	manifest := saveSnapshotManifest{
		Version: 1,
		ID:      id,
		Label:   strings.TrimSpace(label),
		Created: created,
	}
	if manifest.Label == "" {
		manifest.Label = "manual"
	}
	usedSlugs := map[string]int{}
	total := int64(0)
	for _, root := range roots {
		if !dirExists(root.Path) {
			continue
		}
		slug := saveRootSlug(root.Name)
		usedSlugs[slug]++
		if usedSlugs[slug] > 1 {
			slug = fmt.Sprintf("%s-%d", slug, usedSlugs[slug])
		}
		snapRoot := saveSnapshotRoot{Name: root.Name, Slug: slug, Path: root.Path}
		walkErr := filepath.WalkDir(root.Path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				// A directory can disappear between ReadDir and Stat. A save
				// folder is live data; skip what vanished, fail on real I/O.
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() {
				return nil
			}
			if !isSaveFile(path) {
				return nil
			}
			rel, err := filepath.Rel(root.Path, path)
			if err != nil || strings.HasPrefix(rel, "..") {
				return nil
			}
			if only != nil && !only(root.Path, filepath.ToSlash(rel)) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if info.Size() > saveManagerMaxFile {
				return fmt.Errorf("%s is larger than %s — is it really a save?", filepath.Base(path), humanBytes(saveManagerMaxFile))
			}
			if total+info.Size() > saveManagerMaxTotal {
				return fmt.Errorf("save folders exceed the %s snapshot limit", humanBytes(saveManagerMaxTotal))
			}
			dst := filepath.Join(staging, "files", slug, filepath.FromSlash(rel))
			sum, n, err := copySaveFile(path, dst, saveManagerMaxFile)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			total += n
			snapRoot.Bytes += n
			snapRoot.Files = append(snapRoot.Files, saveSnapshotFile{
				Path:   filepath.ToSlash(rel),
				Size:   n,
				SHA256: sum,
			})
			return nil
		})
		if walkErr != nil {
			_ = os.RemoveAll(staging)
			return saveSnapshot{}, walkErr
		}
		if len(snapRoot.Files) > 0 {
			manifest.Roots = append(manifest.Roots, snapRoot)
		}
	}
	if total == 0 {
		_ = os.RemoveAll(staging)
		return saveSnapshot{}, errSaveManagerEmpty
	}
	manifest.TotalBytes = total
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = os.RemoveAll(staging)
		return saveSnapshot{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), data, 0o600); err != nil {
		_ = os.RemoveAll(staging)
		return saveSnapshot{}, err
	}
	final := filepath.Join(saveManagerSnapshotsDir(), id)
	if err := os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
		return saveSnapshot{}, err
	}
	_ = pruneSaveSnapshots(saveSnapshotKeep, append(keep, id)...)
	return loadSaveSnapshot(final)
}

func loadSaveSnapshot(dir string) (saveSnapshot, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return saveSnapshot{}, err
	}
	var manifest saveSnapshotManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return saveSnapshot{}, err
	}
	if manifest.Version <= 0 || manifest.ID == "" {
		return saveSnapshot{}, fmt.Errorf("snapshot manifest is missing its id")
	}
	return saveSnapshot{Manifest: manifest, Dir: dir}, nil
}

// listSaveSnapshots returns newest first. Broken or hand-edited snapshots are
// skipped rather than breaking the whole panel.
func listSaveSnapshots() []saveSnapshot {
	entries, err := os.ReadDir(saveManagerSnapshotsDir())
	if err != nil {
		return nil
	}
	out := make([]saveSnapshot, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || skipScanEntryName(entry.Name()) {
			continue
		}
		snap, err := loadSaveSnapshot(filepath.Join(saveManagerSnapshotsDir(), entry.Name()))
		if err != nil {
			continue
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Manifest.Created.Equal(out[j].Manifest.Created) {
			return out[i].Manifest.Created.After(out[j].Manifest.Created)
		}
		return out[i].Manifest.ID > out[j].Manifest.ID
	})
	return out
}

// pruneSaveSnapshots keeps the newest keep snapshots. The IDs in protect are
// never deleted, even past that count.
func pruneSaveSnapshots(keep int, protect ...string) error {
	if keep < 1 {
		keep = 1
	}
	snaps := listSaveSnapshots()
	if len(snaps) <= keep {
		return nil
	}
	spared := map[string]bool{}
	for _, id := range protect {
		spared[id] = true
	}
	for _, snap := range snaps[keep:] {
		if spared[snap.Manifest.ID] || !isPathWithin(snap.Dir, saveManagerSnapshotsDir()) {
			continue
		}
		if err := os.RemoveAll(snap.Dir); err != nil {
			return err
		}
	}
	return nil
}

func deleteSaveSnapshot(snap saveSnapshot) error {
	if !isPathWithin(snap.Dir, saveManagerSnapshotsDir()) {
		return errors.New("snapshot path is outside the save manager")
	}
	return os.RemoveAll(snap.Dir)
}

// saveRestorePlan is one file a restore has already proved readable and
// intact. A restore builds the whole plan before the first live byte moves.
type saveRestorePlan struct {
	Rel    string // slash-separated path inside the root, for messages
	Src    string
	Dst    string
	SHA256 string
}

// parentResolvesWithin reports whether the folder dst will be written into,
// with every link resolved, is still inside root. A save folder can contain a
// symlink (some setups point one game's folder at another card) and writing
// through it would land outside the folder the user agreed to restore into.
func parentResolvesWithin(root, dst string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		// The root is not there yet, so there is no link to follow; the
		// lexical containment check already refused an escaping path.
		return true
	}
	parent := filepath.Dir(dst)
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			return isPathWithin(resolved, resolvedRoot)
		}
		if !os.IsNotExist(err) {
			return false
		}
		// The folder itself is new; check the nearest parent that exists, which
		// is where a link would have to be for the write to escape.
		next := filepath.Dir(parent)
		if next == parent {
			return false
		}
		parent = next
	}
}

// planSaveRestore proves the snapshot can be written before anything is: every
// root is matched, every entry stays inside both folders, and every source file
// is present at the size and SHA-256 the snapshot recorded. A truncated copy, a
// hand-edited manifest or a card that lost a file therefore fails here, with
// the live saves still untouched.
func planSaveRestore(snap saveSnapshot, roots []saveRoot) ([]saveRestorePlan, error) {
	if len(snap.Manifest.Roots) == 0 {
		return nil, errors.New("this snapshot is empty")
	}
	// Roots are matched by their original path first, because one label can
	// describe several folders (RetroArch saves in both SDCARD/RetroArch and
	// ~/.config/retroarch). Falling back to the label is only safe when it is
	// unique; writing two snapshots into the same folder would destroy saves.
	byPath := make(map[string]saveRoot, len(roots))
	byName := make(map[string][]saveRoot, len(roots))
	for _, root := range roots {
		byPath[filepath.Clean(root.Path)] = root
		byName[root.Name] = append(byName[root.Name], root)
	}
	var plan []saveRestorePlan
	for _, snapRoot := range snap.Manifest.Roots {
		root, ok := byPath[filepath.Clean(snapRoot.Path)]
		if !ok {
			matches := byName[snapRoot.Name]
			switch len(matches) {
			case 1:
				root, ok = matches[0], true
			case 0:
				return nil, fmt.Errorf("%s folder is no longer present", snapRoot.Name)
			default:
				return nil, fmt.Errorf("%s has more than one folder now — restore it by hand from %s", snapRoot.Name, snap.Dir)
			}
		}
		slug := snapRoot.Slug
		if slug == "" {
			slug = saveRootSlug(snapRoot.Name)
		}
		for _, file := range snapRoot.Files {
			if strings.TrimSpace(file.Path) == "" {
				continue
			}
			src := filepath.Join(snap.Dir, "files", slug, filepath.FromSlash(file.Path))
			dst := filepath.Join(root.Path, filepath.FromSlash(file.Path))
			if !isPathWithin(src, snap.Dir) || !isPathWithin(dst, root.Path) {
				return nil, fmt.Errorf("snapshot entry %q escapes its folder", file.Path)
			}
			if !parentResolvesWithin(root.Path, dst) {
				return nil, fmt.Errorf("%s would be written through a link outside %s", file.Path, snapRoot.Name)
			}
			info, err := os.Stat(src)
			if err != nil {
				if os.IsNotExist(err) {
					return nil, fmt.Errorf("%s is missing from snapshot %s", file.Path, snap.Manifest.ID)
				}
				return nil, fmt.Errorf("%s: %w", file.Path, err)
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("%s is not a file in the snapshot", file.Path)
			}
			if file.Size > 0 && info.Size() != file.Size {
				return nil, fmt.Errorf("%s is %s in the snapshot but was recorded as %s",
					file.Path, humanBytes(info.Size()), humanBytes(file.Size))
			}
			// The checksum is what separates a restore from "copy whatever is
			// on the card now", so a manifest without one cannot be proved and
			// is refused rather than trusted.
			if strings.TrimSpace(file.SHA256) == "" {
				return nil, fmt.Errorf("%s has no checksum in the snapshot", file.Path)
			}
			sum, err := SHA256File(src)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", file.Path, err)
			}
			if !strings.EqualFold(sum, file.SHA256) {
				return nil, fmt.Errorf("%s is corrupt (checksum mismatch)", file.Path)
			}
			plan = append(plan, saveRestorePlan{Rel: file.Path, Src: src, Dst: dst, SHA256: sum})
		}
	}
	return plan, nil
}

// restoreSaveSnapshot writes the snapshot's files back into the live roots.
// The whole snapshot is verified first, and only then is a safety snapshot
// taken, so a restore that turns out to be the wrong one can itself be undone
// while one that fails verification never touches a live file. Only files
// present in the snapshot are written; extra files already in the save folder
// are left alone.
func restoreSaveSnapshot(snap saveSnapshot, roots []saveRoot) (saveSnapshot, error) {
	plan, err := planSaveRestore(snap, roots)
	if err != nil {
		// The panel already says which action failed, so lead with the part the
		// user needs to hear on a small screen: their saves are still there.
		return saveSnapshot{}, fmt.Errorf("nothing was changed — %w", err)
	}
	// The safety snapshot spares the one being restored: with the usual full
	// set, that is often the oldest, and its pruning deleted it right before
	// the copy — the restore failed and the backup was gone.
	pre, preErr := createSaveSnapshotKeeping(roots, "before restore", snap.Manifest.ID)
	if preErr != nil && !errors.Is(preErr, errSaveManagerEmpty) {
		return saveSnapshot{}, fmt.Errorf("could not take a safety snapshot: %w", preErr)
	}
	for _, entry := range plan {
		sum, _, err := copySaveFile(entry.Src, entry.Dst, saveManagerMaxFile)
		if err != nil {
			return pre, fmt.Errorf("restore %s: %w", entry.Rel, err)
		}
		// The hash is computed as the bytes are written, so a card that filled
		// up part way through is caught here, not at the next boot.
		if !strings.EqualFold(sum, entry.SHA256) {
			return pre, fmt.Errorf("%s did not land intact — is the card full?", entry.Rel)
		}
	}
	return pre, nil
}

// latestSaveSnapshot returns the newest snapshot, if any.
func latestSaveSnapshot() (saveSnapshot, bool) {
	snaps := listSaveSnapshots()
	if len(snaps) == 0 {
		return saveSnapshot{}, false
	}
	return snaps[0], true
}

// ── Syncthing preparation ──────────────────────────────────────────────────

type saveSyncFolder struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	FolderID string `json:"folderId"`
}

type saveSyncManifest struct {
	Version int              `json:"version"`
	Created time.Time        `json:"created"`
	Folders []saveSyncFolder `json:"folders"`
}

// prepareSaveSync writes the folder list and the markers Syncthing looks for.
// It does not download, install or configure Syncthing: that needs a binary
// and an account/device ID, and silently touching a user's Syncthing config is
// not acceptable. What it removes is the guesswork about which folders and
// which IDs to use.
func prepareSaveSync(roots []saveRoot) (saveSyncManifest, error) {
	if len(roots) == 0 {
		return saveSyncManifest{}, errors.New("no save folders found yet")
	}
	if err := ensureSaveManagerLayout(); err != nil {
		return saveSyncManifest{}, err
	}
	manifest := saveSyncManifest{Version: 1, Created: time.Now().UTC()}
	seen := map[string]bool{}
	for _, root := range roots {
		if !dirExists(root.Path) {
			continue
		}
		id := "jukahub-" + saveRootSlug(root.Name)
		for n := 2; seen[id]; n++ {
			id = fmt.Sprintf("jukahub-%s-%d", saveRootSlug(root.Name), n)
		}
		seen[id] = true
		manifest.Folders = append(manifest.Folders, saveSyncFolder{
			Name:     root.Name,
			Path:     root.Path,
			FolderID: id,
		})
		// These files are what Syncthing uses to recognise a folder. They are
		// small and harmless if Syncthing is never installed.
		marker := filepath.Join(root.Path, ".stfolder")
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			_ = os.WriteFile(marker, []byte("JukaHub save folder\n"), 0o644)
		}
		ignore := filepath.Join(root.Path, ".stignore")
		if _, err := os.Stat(ignore); os.IsNotExist(err) {
			_ = os.WriteFile(ignore, []byte("(?d).DS_Store\n(?d)desktop.ini\n(?d)Thumbs.db\n(?d)*.tmp\n(?d)*.part\n"), 0o644)
		}
	}
	if len(manifest.Folders) == 0 {
		return saveSyncManifest{}, errors.New("no save folders found yet")
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return saveSyncManifest{}, err
	}
	// Atomic, with a .bak: this is the list of folders the user shared with
	// their other devices, and a card pulled mid-write used to leave a file
	// that read as "save sync was never set up".
	if err := AtomicWrite(filepath.Join(saveManagerSyncDir(), "folders.json"), data, 0o600); err != nil {
		return saveSyncManifest{}, err
	}
	var readme strings.Builder
	readme.WriteString("JukaHub save sync\n================\n\n")
	readme.WriteString("This folder list was written by JukaHub. Install Syncthing on this device\n")
	readme.WriteString("and on your PC/Steam Deck, then add the folders below with the listed\n")
	readme.WriteString("Folder ID on both devices and share them with each other.\n\n")
	for _, folder := range manifest.Folders {
		fmt.Fprintf(&readme, "- %s\n  Path: %s\n  Folder ID: %s\n", folder.Name, folder.Path, folder.FolderID)
	}
	readme.WriteString("\nThe .stfolder marker in each save folder tells Syncthing the folder is\n")
	readme.WriteString("ready. JukaHub never edits Syncthing's own configuration: device IDs and\n")
	readme.WriteString("shared-folder acceptance stay in Syncthing's UI, where they belong.\n")
	if err := os.WriteFile(filepath.Join(saveManagerSyncDir(), "README.txt"), []byte(readme.String()), 0o644); err != nil {
		return saveSyncManifest{}, err
	}
	return manifest, nil
}

func saveSyncStatus() (saveSyncManifest, bool) {
	var manifest saveSyncManifest
	if _, err := readJSONWithBackup(filepath.Join(saveManagerSyncDir(), "folders.json"), &manifest); err != nil {
		return saveSyncManifest{}, false
	}
	if len(manifest.Folders) == 0 {
		return saveSyncManifest{}, false
	}
	return manifest, true
}

// ── Resume bundle ──────────────────────────────────────────────────────────

// createResumeBundle zips one snapshot for a phone or a PC. It is the
// cross-device half that can be done honestly on this side: the phone gets
// the real save files and a manifest, not a fake "launched the emulator"
// promise. JukaHub's existing Companion file transfer can pull it.
func createResumeBundle(snap saveSnapshot) (string, error) {
	if err := ensureSaveManagerLayout(); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(saveManagerResumeDir(), ".resume-*.zip")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	defer os.Remove(tmpName)

	out, err := os.Create(tmpName)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(out)
	writeZipFile := func(name, src string) error {
		info, err := os.Stat(src)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = name
		header.Method = zip.Deflate
		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	}
	if err := writeZipFile("manifest.json", filepath.Join(snap.Dir, "manifest.json")); err != nil {
		_ = zw.Close()
		_ = out.Close()
		return "", err
	}
	resume := fmt.Sprintf("JukaHub resume bundle\nSnapshot: %s\nCreated: %s\n\nCopy the files/ tree into the matching emulator save folders on the\nother device. The manifest keeps the original folder names.\n",
		snap.Manifest.ID, snap.Manifest.Created.Format(time.RFC3339))
	w, err := zw.Create("RESUME.txt")
	if err != nil {
		_ = zw.Close()
		_ = out.Close()
		return "", err
	}
	if _, err := w.Write([]byte(resume)); err != nil {
		_ = zw.Close()
		_ = out.Close()
		return "", err
	}
	for _, snapRoot := range snap.Manifest.Roots {
		slug := snapRoot.Slug
		if slug == "" {
			slug = saveRootSlug(snapRoot.Name)
		}
		for _, file := range snapRoot.Files {
			src := filepath.Join(snap.Dir, "files", slug, filepath.FromSlash(file.Path))
			name := filepath.ToSlash(filepath.Join("files", slug, file.Path))
			if err := writeZipFile(name, src); err != nil {
				_ = zw.Close()
				_ = out.Close()
				return "", err
			}
		}
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	final := filepath.Join(saveManagerResumeDir(), "JukaHub-resume-"+snap.Manifest.ID+".zip")
	if err := os.Rename(tmpName, final); err != nil {
		return "", err
	}
	return final, nil
}

// latestResumeBundle returns the newest .zip the panel created, if any.
func latestResumeBundle() (string, bool) {
	entries, err := os.ReadDir(saveManagerResumeDir())
	if err != nil {
		return "", false
	}
	best := ""
	var bestAt time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".zip") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestAt) {
			best = filepath.Join(saveManagerResumeDir(), entry.Name())
			bestAt = info.ModTime()
		}
	}
	return best, best != ""
}

// saveManagerSummary is the one-line panel header.
func saveManagerSummary(roots []saveRoot, snaps []saveSnapshot) string {
	if len(roots) == 0 {
		return "No save folders found yet — start a game once"
	}
	parts := []string{pluralizeCount(len(roots), "save folder", "save folders")}
	if len(snaps) == 0 {
		parts = append(parts, "no snapshots")
	} else {
		parts = append(parts, pluralizeCount(len(snaps), "snapshot", "snapshots"))
	}
	if sync, ok := saveSyncStatus(); ok {
		parts = append(parts, fmt.Sprintf("sync prepared for %d", len(sync.Folders)))
	}
	return strings.Join(parts, " · ")
}
