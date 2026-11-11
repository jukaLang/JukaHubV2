package main

// bkup.go — zero-config backup: your whole setup in one file.
//
// One press writes JukaHub-backup-<device>-<yyyymmdd-hhmm>.jkbackup: a zip
// holding a manifest (every file's size and SHA-256, the app version, the
// platform and the folders the backup was made from), the config document
// (cfgdoc.go), the user config, the theme files, the plugins, the input
// profiles, the experience packs and the library's stores — favourites,
// recents, playtime and trophies, alarms, notes, saved chat notes, the words
// the keyboard learned, bookmarks, package records — and, only when asked, the
// game saves. No ROMs and no media: the file is a setup, not a library, and it
// fits on any card or in any mail.
//
// A restore never trusts the file: the manifest must account for every entry,
// every entry must match its checksum, and every path must land inside the
// folder it names. A safety backup of whatever is about to be replaced is
// written first. Files go back to the same place on any device — the archive
// names places ("the data folder", "the plugins folder", "the save folder
// called RetroArch saves"), never absolute paths — and absolute paths inside
// the user config are translated from the backup's folders to this device's;
// files that only make sense on the platform that wrote them (the design file,
// installed package records, launcher overrides, plugin binaries) are skipped
// across Windows and TrimUI.
//
// The files are written off the render thread; the user config is merged and
// the config reloaded on the main thread, the way a hot reload installs one,
// and every store that keeps a copy in memory re-reads its file so nothing
// saves its old copy over the restored one.

import (
	"archive/zip"
	"context"
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
	bkupExt              = ".jkbackup"
	bkupFormat           = "jukahub-backup"
	bkupVersion          = 1
	bkupManifestName     = "manifest.json"
	bkupFolderName       = "JukaHub Backups"
	bkupMaxManifestBytes = 8 << 20
	// bkupMaxFileBytes caps one file: anything bigger is not a setting, and a
	// restore has to hold one file in memory on a 1 GB handheld.
	bkupMaxFileBytes = 64 << 20
	// bkupMaxPluginFile caps a plugin's own files; a plugin folder holding a
	// video is not worth copying to every backup.
	bkupMaxPluginFile = 2 << 20
	bkupAutoEvery     = 7 * 24 * time.Hour
	bkupAutoKeep      = 3
	bkupSafetyKeep    = 3
	// bkupAutoKey is the user-config switch for the weekly backup.
	bkupAutoKey = "bkupAuto"
)

// The categories a backup is made of, in the order they are listed.
const (
	bkupCatSettings = "settings"
	bkupCatLibrary  = "library"
	bkupCatThemes   = "themes"
	bkupCatPlugins  = "plugins"
	bkupCatInput    = "input"
	bkupCatPacks    = "packs"
	bkupCatSaves    = "saves"
)

var bkupCategoryOrder = []string{bkupCatSettings, bkupCatLibrary, bkupCatThemes, bkupCatPlugins, bkupCatInput, bkupCatPacks, bkupCatSaves}

// bkupCategoryLabel is a category's name on screen.
func bkupCategoryLabel(cat string) string {
	switch cat {
	case bkupCatSettings:
		return "Settings"
	case bkupCatLibrary:
		return "Library & history"
	case bkupCatThemes:
		return "Themes"
	case bkupCatPlugins:
		return "Plugins"
	case bkupCatInput:
		return "Input profiles"
	case bkupCatPacks:
		return "Experience packs"
	case bkupCatSaves:
		return "Game saves"
	}
	return cat
}

// bkupManifest is manifest.json inside a backup.
type bkupManifest struct {
	Format     string            `json:"format"`
	Version    int               `json:"version"`
	CreatedAt  string            `json:"createdAt"`
	AppVersion string            `json:"appVersion"`
	Platform   string            `json:"platform"`
	Device     string            `json:"device"`
	DeviceName string            `json:"deviceName"`
	Label      string            `json:"label,omitempty"`
	Roots      map[string]string `json:"roots"`
	Categories []string          `json:"categories"`
	Files      []bkupFile        `json:"files"`
	TotalBytes int64             `json:"totalBytes"`
}

// bkupFile is one file in a backup: where it is in the archive, the place it
// belongs ("data:vk-words.json"), and what it must hash to.
type bkupFile struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Category string `json:"category"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Platform bool   `json:"platformSpecific,omitempty"`
	ModTime  string `json:"modTime,omitempty"`
}

// bkupOptions are the choices a backup is made with.
type bkupOptions struct {
	PluginFiles bool // whole plugin folders, not only their manifests
	Saves       bool // the game saves (off unless asked: they can be large)
}

// bkupSource is one file or folder a backup carries.
type bkupSource struct {
	Category string
	Token    string // the place: app, data, plugins, themes, packs, input, patch
	Rel      string // a file or folder under it; "" is the place itself
	Platform bool   // only meaningful on the platform that wrote it
}

// bkupSources is what a backup carries, by category.
func bkupSources() []bkupSource {
	return []bkupSource{
		{bkupCatSettings, "app", "jukauser.json", false},
		// The zone lives beside the binary, not in jukauser.json; carrying the
		// file is what lets a handheld restore keep its own clock settings.
		{bkupCatSettings, "app", "jukatimezone", false},
		{bkupCatSettings, "app", "jukaconfig.json", true},
		{bkupCatLibrary, "app", "jukaalarm.json", false},
		{bkupCatLibrary, "app", "notes.json", false},
		{bkupCatLibrary, "data", "game-recents.json", false},
		{bkupCatLibrary, "data", "game-options.json", false},
		{bkupCatLibrary, "data", "game-launchers.json", true},
		{bkupCatLibrary, "data", "vk-words.json", false},
		{bkupCatLibrary, "data", "browser-bookmarks.json", false},
		{bkupCatLibrary, "data", "browser-block.json", false},
		{bkupCatLibrary, "data", musicFavoritesFileName, false},
		{bkupCatLibrary, "data", musicPlaylistFileName, false},
		{bkupCatLibrary, "data", podcastSubsFileName, false},
		{bkupCatLibrary, "data", tvFavoritesFileName, false},
		{bkupCatLibrary, "data", ipStreamLibraryFileName, false},
		{bkupCatLibrary, "data", partyFriendsFileName, false},
		{bkupCatLibrary, "data", curationDirName, false},
		{bkupCatLibrary, "patch", "db/db.json", true},
		{bkupCatThemes, "themes", "", false},
		{bkupCatPlugins, "plugins", "", false},
		{bkupCatPacks, "packs", "", false},
		{bkupCatInput, "input", "", false},
	}
}

// bkupOtherDataDeny are files in the data folder a backup leaves out: caches
// that rebuild themselves, device state, and stores that hold credentials.
var bkupOtherDataDeny = map[string]bool{
	"theme-catalog.json": true, "weather-cache.json": true, "release-info.json": true,
	"startup-state.json": true, "retroachievements.json": true, "retroachievements-cache.json": true,
	"browser-cookies.json": true, "psremote.json": true, "bootguard.json": true,
	audioResumeFileName: true, cfgdocDefaultsFile: true, "icons.json": true,
}

// ── Places ────────────────────────────────────────────────────────────────

// Overrides for tests: the app, data and export folders, the folders a restore
// looks in, the save folders and Patch's state folder.
var (
	bkupAppDirOverride     string
	bkupDataDirOverride    string
	bkupExportDirOverride  string
	bkupSearchDirsOverride []string
	bkupSaveRoots          func() []saveRoot
	bkupPatchDir           func() (string, error)
)

func init() {
	// Assigned here rather than by the declarations, so no variable
	// initialiser reaches into the save manager or Patch.
	bkupSaveRoots, bkupPatchDir = saveManagerRoots, PatchStateDir
}

// bkupAppDir is the folder JukaHub runs in: the user config, the design file,
// the alarms and the notes are opened relative to it.
func bkupAppDir() string {
	if bkupAppDirOverride != "" {
		return bkupAppDirOverride
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// bkupDataDir is the app's data folder.
func bkupDataDir() string {
	if bkupDataDirOverride != "" {
		return bkupDataDirOverride
	}
	if dir, err := P().DataDir(); err == nil && strings.TrimSpace(dir) != "" {
		return dir
	}
	return filepath.Join(bkupAppDir(), "data")
}

// bkupAbs makes a folder the app names relatively absolute, the way the app
// itself resolves it (from the folder it runs in).
func bkupAbs(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(bkupAppDir(), p)
}

// bkupInputDir is where the input profiles live.
func bkupInputDir() string { return filepath.Join(bkupDataDir(), "input-profiles") }

// bkupSafetyDir holds the safety backups and the weekly backup's record.
func bkupSafetyDir() string { return filepath.Join(bkupDataDir(), "backups") }

// bkupRoot is the folder a place token stands for on this device.
func bkupRoot(token string) string {
	switch token {
	case "app":
		return bkupAppDir()
	case "data":
		return bkupDataDir()
	case "plugins":
		return bkupAbs(pluginDir)
	case "themes":
		return bkupAbs(themeDir)
	case "packs":
		return xpackDir()
	case "input":
		return bkupInputDir()
	case "patch":
		if dir, err := bkupPatchDir(); err == nil {
			return dir
		}
	}
	return ""
}

// bkupCardRoot is the SD card on the handheld, "" elsewhere.
func bkupCardRoot() string {
	if root := strings.TrimSpace(trimuiSDRootFn()); root != "" {
		return root
	}
	if !IsWindows() {
		for _, root := range []string{"/mnt/SDCARD", "/mnt/sdcard"} {
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				return root
			}
		}
	}
	return ""
}

// bkupExportDir is where a backup, a pack or the config document is written:
// the card's "JukaHub Backups" folder on the handheld, Downloads on a computer.
func bkupExportDir() string {
	if bkupExportDirOverride != "" {
		return bkupExportDirOverride
	}
	if !IsWindows() {
		if card := bkupCardRoot(); card != "" {
			return filepath.Join(card, bkupFolderName)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		downloads := filepath.Join(home, "Downloads")
		if info, err := os.Stat(downloads); err == nil && info.IsDir() {
			return downloads
		}
		return home
	}
	return filepath.Join(bkupAppDir(), bkupFolderName)
}

// bkupSearchDirs are the folders a restore (and an import) looks in: the export
// folder, the card's top level, Downloads, the packs folder and the safety
// backups. Only folders that exist are listed, each once.
func bkupSearchDirs() []string {
	if bkupSearchDirsOverride != nil {
		return append([]string(nil), bkupSearchDirsOverride...)
	}
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return
		}
		key := abs
		if IsWindows() {
			key = strings.ToLower(abs)
		}
		if seen[key] {
			return
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			return
		}
		seen[key] = true
		dirs = append(dirs, abs)
	}
	add(bkupExportDir())
	if card := bkupCardRoot(); card != "" {
		add(card)
		add(filepath.Join(card, bkupFolderName))
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		add(filepath.Join(home, "Downloads"))
	}
	add(xpackDir())
	add(bkupSafetyDir())
	return dirs
}

// bkupHereRoots are this device's folders, recorded in a backup so the paths
// inside its user config can be translated on another device.
func bkupHereRoots() map[string]string {
	roots := map[string]string{"app": bkupAppDir(), "data": bkupDataDir()}
	if card := bkupCardRoot(); card != "" {
		roots["card"] = card
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		roots["home"] = home
	}
	return roots
}

// bkupSaveSlugs names the save folders the way a backup does: by their name,
// made safe, with a number when two share one.
func bkupSaveSlugs() map[string]string {
	out := map[string]string{}
	for _, root := range bkupSaveRoots() {
		base := saveRootSlug(root.Name)
		slug := base
		for i := 2; out[slug] != ""; i++ {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		out[slug] = root.Path
	}
	return out
}

// bkupDeviceSlug is the device in a backup's file name.
func bkupDeviceSlug() string {
	if IsWindows() {
		return "windows"
	}
	d := DetectDevice()
	if d == DeviceUnknown || d == DeviceDevBuild {
		return "linux"
	}
	return strings.TrimPrefix(string(d), "trimui-")
}

// bkupFileName is JukaHub-backup-<device>-<yyyymmdd-hhmm>.jkbackup; a weekly
// backup ends in -auto and a safety backup starts JukaHub-safety-.
func bkupFileName(kind string, now time.Time) string {
	prefix := "JukaHub-backup-"
	if kind == "safety" {
		prefix = "JukaHub-safety-"
	}
	name := prefix + bkupDeviceSlug() + "-" + now.Format("20060102-1504")
	if kind == "auto" {
		name += "-auto"
	}
	return name + bkupExt
}

// bkupUniquePath is dir/name, numbered when a file of that name exists.
func bkupUniquePath(dir, name string) string {
	path := filepath.Join(dir, name)
	stem := strings.TrimSuffix(name, bkupExt)
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, bkupExt))
	}
}

// ── Collecting ────────────────────────────────────────────────────────────

// bkupItem is one file a backup will carry.
type bkupItem struct {
	Abs      string
	Target   string
	Category string
	Size     int64
	Platform bool
	ModTime  time.Time
}

// bkupIsBinary reports whether a file is a program (ELF, PE or Mach-O): a
// plugin's compiled helper only runs on the platform it was built for.
func bkupIsBinary(path string) bool {
	head := plugxReadHeader(path, 4)
	if len(head) < 2 {
		return false
	}
	switch {
	case len(head) >= 4 && string(head) == "\x7fELF":
		return true
	case head[0] == 'M' && head[1] == 'Z':
		return true
	case len(head) >= 4 && (string(head) == "\xfe\xed\xfa\xce" || string(head) == "\xfe\xed\xfa\xcf" ||
		string(head) == "\xce\xfa\xed\xfe" || string(head) == "\xcf\xfa\xed\xfe"):
		return true
	}
	return false
}

// bkupCollect lists the files of the chosen categories as they are on disk.
func bkupCollect(cats map[string]bool, opts bkupOptions) []bkupItem {
	var items []bkupItem
	seen := map[string]bool{}
	add := func(cat, token, rel, abs string, info os.FileInfo, platform bool) {
		// A name a restore could not write back (a colon, a backslash) is left
		// out rather than making the whole backup unreadable.
		if strings.ContainsAny(rel, ":\\") {
			return
		}
		key := strings.ToLower(abs)
		if seen[key] {
			return
		}
		seen[key] = true
		items = append(items, bkupItem{Abs: abs, Target: token + ":" + rel, Category: cat, Size: info.Size(), Platform: platform, ModTime: info.ModTime()})
	}
	walk := func(cat, token, root, start string, platform bool, keep func(path, rel string, info os.FileInfo) bool) {
		_ = filepath.WalkDir(start, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if p != start && strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() > bkupMaxFileBytes {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if keep != nil && !keep(p, rel, info) {
				return nil
			}
			add(cat, token, rel, p, info, platform)
			return nil
		})
	}
	for _, src := range bkupSources() {
		if !cats[src.Category] {
			continue
		}
		root := bkupRoot(src.Token)
		if root == "" {
			continue
		}
		start := root
		if src.Rel != "" {
			start = filepath.Join(root, filepath.FromSlash(src.Rel))
		}
		info, err := os.Lstat(start)
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() {
			if info.Size() <= bkupMaxFileBytes {
				rel := src.Rel
				if rel == "" {
					rel = filepath.Base(start)
				}
				add(src.Category, src.Token, rel, start, info, src.Platform)
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		var keep func(path, rel string, info os.FileInfo) bool
		if src.Category == bkupCatPlugins {
			keep = func(path, rel string, info os.FileInfo) bool {
				if filepath.Base(rel) == "plugin.json" {
					return true
				}
				return opts.PluginFiles && info.Size() <= bkupMaxPluginFile && !bkupIsBinary(path)
			}
		}
		walk(src.Category, src.Token, root, start, src.Platform, keep)
	}
	if cats[bkupCatLibrary] {
		// Other stores in the data folder — the library's own metadata among
		// them — come along unless they are caches or hold credentials.
		dataDir := bkupDataDir()
		if entries, err := os.ReadDir(dataDir); err == nil {
			for _, e := range entries {
				name := e.Name()
				lower := strings.ToLower(name)
				if e.IsDir() || !strings.HasSuffix(lower, ".json") || bkupOtherDataDeny[name] ||
					strings.Contains(lower, "cache") || strings.Contains(lower, "cookie") ||
					strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
					continue
				}
				info, err := e.Info()
				if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
					continue
				}
				add(bkupCatLibrary, "data", name, filepath.Join(dataDir, name), info, false)
			}
		}
	}
	if cats[bkupCatSaves] && opts.Saves {
		slugs := bkupSaveSlugs()
		names := make([]string, 0, len(slugs))
		for slug := range slugs {
			names = append(names, slug)
		}
		sort.Strings(names)
		for _, slug := range names {
			root := slugs[slug]
			walk(bkupCatSaves, "saves/"+slug, root, root, false, func(path, rel string, info os.FileInfo) bool {
				return isSaveFile(path)
			})
		}
	}
	return items
}

// bkupSize is how much one category would add to a backup.
type bkupSize struct {
	Files int
	Bytes int64
}

// bkupMeasure sizes every category without reading a byte of it.
func bkupMeasure(opts bkupOptions) map[string]bkupSize {
	all := map[string]bool{}
	for _, cat := range bkupCategoryOrder {
		all[cat] = true
	}
	opts.Saves = true
	out := map[string]bkupSize{}
	for _, it := range bkupCollect(all, opts) {
		s := out[it.Category]
		s.Files++
		s.Bytes += it.Size
		out[it.Category] = s
	}
	return out
}

// ── Writing ───────────────────────────────────────────────────────────────

// bkupEntryName is where a target is stored inside the archive.
func bkupEntryName(target string) string {
	return "files/" + strings.Replace(target, ":", "/", 1)
}

// bkupAddFile copies one file into the archive, hashing it on the way.
func bkupAddFile(zw *zip.Writer, it bkupItem) (bkupFile, error) {
	in, err := os.Open(it.Abs)
	if err != nil {
		return bkupFile{}, err
	}
	defer in.Close()
	name := bkupEntryName(it.Target)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: it.ModTime})
	if err != nil {
		return bkupFile{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(in, bkupMaxFileBytes+1))
	if err != nil {
		return bkupFile{}, err
	}
	if n > bkupMaxFileBytes {
		return bkupFile{}, fmt.Errorf("grew past %s while it was copied", humanBytes(bkupMaxFileBytes))
	}
	return bkupFile{
		Path:     name,
		Target:   it.Target,
		Category: it.Category,
		Size:     n,
		SHA256:   hex.EncodeToString(h.Sum(nil)),
		Platform: it.Platform,
		ModTime:  it.ModTime.UTC().Format(time.RFC3339),
	}, nil
}

// bkupWrite writes items (and extra, in-memory files by target) into a new
// archive at path. The archive is assembled in a temporary file beside it and
// renamed into place, so a full card or a power cut never leaves a half
// backup under the real name. ctx stops it between files.
func bkupWrite(ctx context.Context, path string, items []bkupItem, extra map[string][]byte, m bkupManifest, progress func(done, total int)) (bkupManifest, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return m, err
	}
	tmp, err := os.CreateTemp(dir, ".backup-*.part")
	if err != nil {
		return m, err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmpName)
		}
	}()
	zw := zip.NewWriter(tmp)
	fail := func(err error) (bkupManifest, error) {
		_ = zw.Close()
		_ = tmp.Close()
		return m, err
	}
	m.Files, m.TotalBytes = nil, 0
	total, count := len(items)+len(extra), 0
	for _, it := range items {
		if ctx != nil && ctx.Err() != nil {
			return fail(ctx.Err())
		}
		f, err := bkupAddFile(zw, it)
		if err != nil {
			if os.IsNotExist(err) {
				continue // gone since it was listed
			}
			return fail(fmt.Errorf("%s: %w", it.Abs, err))
		}
		m.Files = append(m.Files, f)
		m.TotalBytes += f.Size
		count++
		if progress != nil {
			progress(count, total)
		}
	}
	targets := make([]string, 0, len(extra))
	for target := range extra {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		data := extra[target]
		name := bkupEntryName(target)
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return fail(err)
		}
		if _, err := w.Write(data); err != nil {
			return fail(err)
		}
		sum := sha256.Sum256(data)
		m.Files = append(m.Files, bkupFile{Path: name, Target: target, Category: bkupCatSettings, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		m.TotalBytes += int64(len(data))
		count++
		if progress != nil {
			progress(count, total)
		}
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fail(err)
	}
	w, err := zw.CreateHeader(&zip.FileHeader{Name: bkupManifestName, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return fail(err)
	}
	if _, err := w.Write(manifest); err != nil {
		return fail(err)
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		return m, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return m, err
	}
	if err := tmp.Close(); err != nil {
		return m, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return m, err
	}
	done = true
	return m, nil
}

// bkupCategoryList is the chosen categories in their canonical order.
func bkupCategoryList(cats map[string]bool) []string {
	var out []string
	for _, cat := range bkupCategoryOrder {
		if cats[cat] {
			out = append(out, cat)
		}
	}
	return out
}

// bkupCreate collects the chosen categories and writes them, with doc (the
// config document) when given, as dir/name. It returns the archive's path.
func bkupCreate(ctx context.Context, dir, name string, cats map[string]bool, opts bkupOptions, label, appVersion string, doc []byte, progress func(done, total int)) (string, bkupManifest, error) {
	items := bkupCollect(cats, opts)
	m := bkupManifest{
		Format:     bkupFormat,
		Version:    bkupVersion,
		CreatedAt:  time.Now().Format(time.RFC3339),
		AppVersion: appVersion,
		Platform:   P().Name(),
		Device:     string(DetectDevice()),
		DeviceName: deviceDisplayName(DetectDevice()),
		Label:      label,
		Roots:      bkupHereRoots(),
		Categories: bkupCategoryList(cats),
	}
	var extra map[string][]byte
	if len(doc) > 0 && cats[bkupCatSettings] {
		extra = map[string][]byte{"doc:" + cfgdocFileName: doc}
	}
	path := bkupUniquePath(dir, name)
	m, err := bkupWrite(ctx, path, items, extra, m, progress)
	if err != nil {
		return "", m, err
	}
	return path, m, nil
}

// bkupPrune keeps the newest keep backups in dir whose names start with prefix
// and end with suffix+".jkbackup", and deletes the older ones.
func bkupPrune(dir, prefix, suffix string, keep int) {
	bkupPruneKeep(dir, prefix, suffix, keep, "")
}

// bkupPruneKeep is bkupPrune that never deletes keepPath, the archive the
// caller has just written, and counts it among the keep. "Newest" is the
// file's time: on a handheld without a clock a backup written after a cold
// boot can be the oldest by that time, and was the first one deleted.
func bkupPruneKeep(dir, prefix, suffix string, keep int, keepPath string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var list []candidate
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix+bkupExt) {
			continue
		}
		path := filepath.Join(dir, name)
		if keepPath != "" && sameFilePath(path, keepPath) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, candidate{path: path, mod: info.ModTime()})
	}
	if keepPath != "" && keep > 0 {
		keep--
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].mod.After(list[j].mod) })
	for i := keep; i < len(list); i++ {
		_ = os.Remove(list[i].path)
	}
}

// ── Reading and checking ──────────────────────────────────────────────────

// bkupManifestFrom reads the manifest out of an archive's entries.
func bkupManifestFrom(files []*zip.File) (bkupManifest, error) {
	for _, f := range files {
		if f.Name != bkupManifestName {
			continue
		}
		data, err := xpackReadZipFile(f, bkupMaxManifestBytes)
		if err != nil {
			return bkupManifest{}, err
		}
		var m bkupManifest
		if err := json.Unmarshal(data, &m); err != nil {
			return bkupManifest{}, fmt.Errorf("its manifest is damaged (%v)", err)
		}
		if m.Format != bkupFormat {
			return bkupManifest{}, errors.New("is not a JukaHub backup")
		}
		if m.Version > bkupVersion {
			return bkupManifest{}, fmt.Errorf("was made by a newer JukaHub (backup format %d)", m.Version)
		}
		return m, nil
	}
	return bkupManifest{}, errors.New("has no manifest")
}

// bkupReadManifest reads only a backup's manifest, for the restore list.
func bkupReadManifest(path string) (bkupManifest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return bkupManifest{}, errors.New("is not a JukaHub backup")
	}
	defer zr.Close()
	return bkupManifestFrom(zr.File)
}

// bkupCleanRel checks that rel is a plain relative slash path.
func bkupCleanRel(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("names no file")
	}
	if strings.ContainsAny(rel, "\\:\x00") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	return clean, nil
}

// bkupSplitTarget splits "token:rel" and checks both halves.
func bkupSplitTarget(target string) (string, string, error) {
	token, rel, ok := strings.Cut(target, ":")
	if !ok || token == "" {
		return "", "", errors.New("names no place")
	}
	switch {
	case token == "app", token == "data", token == "plugins", token == "themes", token == "packs",
		token == "input", token == "patch", token == "doc":
	case strings.HasPrefix(token, "saves/") && len(token) > len("saves/") &&
		!strings.ContainsAny(strings.TrimPrefix(token, "saves/"), "/\\:."):
	default:
		return "", "", fmt.Errorf("unknown place %q", token)
	}
	clean, err := bkupCleanRel(rel)
	if err != nil {
		return "", "", err
	}
	return token, clean, nil
}

// bkupHashZipFile hashes one archive entry, reading at most want+1 bytes.
func bkupHashZipFile(f *zip.File, want int64) (string, int64, error) {
	rc, err := f.Open()
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(rc, want+1))
	if err != nil {
		return "", n, fmt.Errorf("%s: %v", f.Name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// bkupVerify opens a backup and checks it all: the manifest parses, every file
// it lists is in the archive under its own name with the size and checksum it
// claims, every place is one this app knows, and the archive holds nothing the
// manifest does not list. A file changed by hand or damaged on the card fails
// here, before anything is written.
func bkupVerify(path string) (bkupManifest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return bkupManifest{}, errors.New("is not a JukaHub backup")
	}
	defer zr.Close()
	m, err := bkupManifestFrom(zr.File)
	if err != nil {
		return m, err
	}
	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		entries[f.Name] = f
	}
	listed := map[string]bool{bkupManifestName: true}
	for _, bf := range m.Files {
		if _, _, err := bkupSplitTarget(bf.Target); err != nil {
			return m, fmt.Errorf("%s: %v", bf.Path, err)
		}
		if bf.Path != bkupEntryName(bf.Target) {
			return m, fmt.Errorf("%s does not match the place it belongs", bf.Path)
		}
		f, ok := entries[bf.Path]
		if !ok {
			return m, fmt.Errorf("%s is missing from the archive", bf.Path)
		}
		listed[bf.Path] = true
		sum, n, err := bkupHashZipFile(f, bf.Size)
		if err != nil {
			return m, err
		}
		if n != bf.Size || !strings.EqualFold(sum, bf.SHA256) {
			return m, fmt.Errorf("%s does not match its checksum: the backup was changed or damaged", bf.Path)
		}
	}
	for name := range entries {
		if !listed[name] {
			return m, fmt.Errorf("%s is not in the manifest", name)
		}
	}
	return m, nil
}

// bkupListItem is a backup found on disk.
type bkupListItem struct {
	Path     string
	Name     string
	Size     int64
	ModTime  time.Time
	Manifest bkupManifest
	Problem  string
	Safety   bool
}

// bkupList finds the backups in the usual places, newest first.
func bkupList() []bkupListItem {
	var out []bkupListItem
	seen := map[string]bool{}
	for _, dir := range bkupSearchDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), bkupExt) {
				continue
			}
			full := filepath.Join(dir, name)
			key := strings.ToLower(full)
			if seen[key] {
				continue
			}
			seen[key] = true
			info, err := e.Info()
			if err != nil {
				continue
			}
			item := bkupListItem{Path: full, Name: name, Size: info.Size(), ModTime: info.ModTime(), Safety: strings.HasPrefix(name, "JukaHub-safety-")}
			if m, err := bkupReadManifest(full); err != nil {
				item.Problem = err.Error()
			} else {
				item.Manifest = m
			}
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out
}

// ── Restoring ─────────────────────────────────────────────────────────────

// bkupRestoreFile is one file of a restore and where it goes.
type bkupRestoreFile struct {
	File bkupFile
	Dest string // the file on this device; "" when it is skipped
	Skip string // why it is skipped
}

// bkupRestorePlan is a backup and what restoring the chosen categories does.
type bkupRestorePlan struct {
	Path          string
	Manifest      bkupManifest
	Files         []bkupRestoreFile
	CrossPlatform bool
}

// bkupPlatformLabel is a platform's name on screen.
func bkupPlatformLabel(platform string) string {
	switch platform {
	case "windows":
		return "Windows"
	case "trimui-linux":
		return "the TrimUI handheld"
	}
	return platform
}

// bkupPlanRestore works out where every file of the chosen categories goes on
// this device, and which files stay out and why.
func bkupPlanRestore(path string, m bkupManifest, cats map[string]bool) bkupRestorePlan {
	plan := bkupRestorePlan{Path: path, Manifest: m, CrossPlatform: m.Platform != P().Name()}
	slugs := bkupSaveSlugs()
	for _, f := range m.Files {
		rf := bkupRestoreFile{File: f}
		token, rel, err := bkupSplitTarget(f.Target)
		switch {
		case err != nil:
			rf.Skip = err.Error()
		case !cats[f.Category]:
			rf.Skip = "not chosen"
		case token == "doc":
			rf.Skip = "the config document is for reading and importing"
		case f.Platform && plan.CrossPlatform:
			rf.Skip = "made for " + bkupPlatformLabel(m.Platform)
		default:
			root := ""
			if strings.HasPrefix(token, "saves/") {
				root = slugs[strings.TrimPrefix(token, "saves/")]
				if root == "" {
					rf.Skip = "no save folder of that name here"
				}
			} else if root = bkupRoot(token); root == "" {
				rf.Skip = "no place for it here"
			}
			if rf.Skip == "" {
				dest, err := resolveWithinRoot(root, filepath.FromSlash(rel))
				if err != nil {
					rf.Skip = "unsafe path"
				} else {
					rf.Dest = dest
				}
			}
		}
		plan.Files = append(plan.Files, rf)
	}
	return plan
}

// bkupCatSummary is what restoring one category of a backup would do.
type bkupCatSummary struct {
	Category string
	Files    int
	Bytes    int64
	Skipped  int
	Reason   string // why the last file that stays out does
}

// bkupSummarize counts a restore plan by category, in the canonical order,
// leaving out the categories the backup does not hold.
func bkupSummarize(plan bkupRestorePlan) []bkupCatSummary {
	var out []bkupCatSummary
	for _, cat := range bkupCategoryOrder {
		s := bkupCatSummary{Category: cat}
		for _, rf := range plan.Files {
			if rf.File.Category != cat || strings.HasPrefix(rf.File.Target, "doc:") {
				continue
			}
			if rf.Dest == "" {
				s.Skipped++
				s.Reason = rf.Skip
				continue
			}
			s.Files++
			s.Bytes += rf.File.Size
		}
		if s.Files > 0 || s.Skipped > 0 {
			out = append(out, s)
		}
	}
	return out
}

// bkupRestoreResult is what the background half of a restore hands the main
// thread.
type bkupRestoreResult struct {
	UserData   []byte // the backup's jukauser.json, merged on the main thread
	HasUser    bool
	Zone       string  // the backup's jukatimezone, applied by its owner on the main thread
	Next       *Config // the design config as it is after the restore
	Written    int
	Skipped    []string
	SafetyPath string
	Cats       map[string]bool
	Manifest   bkupManifest
	Err        error
}

// bkupUserTarget is where the user config is stored in a backup.
const bkupUserTarget = "app:jukauser.json"

// bkupZoneTarget is where the chosen IANA zone is stored in a backup.
const bkupZoneTarget = "app:jukatimezone"

// bkupRestoreFiles is the background half of a restore: it checks the backup
// again, writes a safety backup of what the chosen categories would replace,
// then writes every file of the plan except the user config, which the main
// thread merges. It never writes anything when the safety backup fails.
func bkupRestoreFiles(ctx context.Context, plan bkupRestorePlan, cats map[string]bool, appVersion string, safetyDoc []byte) bkupRestoreResult {
	res := bkupRestoreResult{Cats: cats, Manifest: plan.Manifest}
	if _, err := bkupVerify(plan.Path); err != nil {
		res.Err = err
		return res
	}
	safetyName := bkupFileName("safety", time.Now())
	path, _, err := bkupCreate(ctx, bkupSafetyDir(), safetyName, cats,
		bkupOptions{PluginFiles: true, Saves: cats[bkupCatSaves]}, "safety", appVersion, safetyDoc, nil)
	if err != nil {
		res.Err = fmt.Errorf("the safety backup could not be written, so nothing was restored: %w", err)
		return res
	}
	res.SafetyPath = path
	bkupPruneKeep(bkupSafetyDir(), "JukaHub-safety-", "", bkupSafetyKeep, path)

	zr, err := zip.OpenReader(plan.Path)
	if err != nil {
		res.Err = err
		return res
	}
	defer zr.Close()
	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[f.Name] = f
	}
	for _, rf := range plan.Files {
		if rf.Dest == "" {
			if rf.Skip != "not chosen" && rf.File.Category != "" && !strings.HasPrefix(rf.File.Target, "doc:") {
				res.Skipped = append(res.Skipped, rf.File.Target+": "+rf.Skip)
			}
			continue
		}
		if ctx != nil && ctx.Err() != nil {
			res.Err = ctx.Err()
			return res
		}
		f, ok := entries[rf.File.Path]
		if !ok {
			res.Skipped = append(res.Skipped, rf.File.Target+": missing from the archive")
			continue
		}
		data, err := xpackReadZipFile(f, bkupMaxFileBytes)
		if err != nil {
			res.Skipped = append(res.Skipped, rf.File.Target+": "+err.Error())
			continue
		}
		if sum := sha256.Sum256(data); !strings.EqualFold(hex.EncodeToString(sum[:]), rf.File.SHA256) {
			res.Err = fmt.Errorf("%s changed while it was restored", rf.File.Path)
			return res
		}
		if rf.File.Target == bkupZoneTarget {
			res.Zone = strings.TrimSpace(string(data))
		}
		if rf.File.Target == bkupUserTarget {
			res.UserData, res.HasUser = data, true
			continue
		}
		if err := os.MkdirAll(filepath.Dir(rf.Dest), 0o755); err != nil {
			res.Skipped = append(res.Skipped, rf.File.Target+": "+err.Error())
			continue
		}
		if err := AtomicWrite(rf.Dest, data, 0o644); err != nil {
			res.Skipped = append(res.Skipped, rf.File.Target+": "+err.Error())
			continue
		}
		if rf.File.Target == "app:jukaconfig.json" && globalConfigWatcher != nil {
			// The reload below installs it; the watcher must not do it twice.
			// The watcher keys its paths as they were registered (the relative
			// "jukaconfig.json"), so name that as well as the destination.
			globalConfigWatcher.IgnoreWrite("jukaconfig.json", rf.Dest)
		}
		res.Written++
	}
	if next, _, err := LoadXMLConfigWithFallback(filepath.Join(bkupAppDir(), "jukaconfig.json")); err == nil {
		if NewConfigValidator().Validate(next) == nil {
			res.Next = next
		}
	}
	return res
}

// bkupLibraryKeys are the library's entries inside the user config.
var bkupLibraryKeys = []string{playtimeCustomKey, "recently_played"}

// bkupDeviceKey reports whether a user-config entry describes the device
// rather than a choice: kept as it is when a backup comes from the other
// platform.
func bkupDeviceKey(k string) bool {
	switch strings.ToLower(k) {
	case "fullscreen", "screenwidth", "screenheight", "swapabbuttons", "swapxybuttons", strings.ToLower(abSwapMigrationKey),
		"devicemodel", "targetplatform", "tools_path":
		return true
	}
	return false
}

// bkupLooksAbsolute reports whether s is an absolute path on either system.
func bkupLooksAbsolute(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\\`) {
		return true
	}
	return len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') &&
		((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z'))
}

// bkupTranslatePath maps an absolute path written under one of the backup's
// folders (from: data, app, card, home) to the same place on this device (to).
// A path under none of them keeps its meaning only on the same kind of system,
// so from the other platform it comes back empty with ok false.
func bkupTranslatePath(p string, from, to map[string]string, cross bool) (string, bool) {
	if !bkupLooksAbsolute(p) {
		return p, true
	}
	norm := func(s string) string { return strings.TrimRight(strings.ReplaceAll(s, `\`, "/"), "/") }
	np := norm(p)
	for _, key := range []string{"data", "app", "card", "home"} {
		src, dst := norm(from[key]), to[key]
		if src == "" || dst == "" {
			continue
		}
		fold := bkupLooksAbsolute(src) && len(src) >= 2 && src[1] == ':'
		match := np == src || (fold && strings.EqualFold(np, src))
		if match {
			return dst, true
		}
		prefix := src + "/"
		if strings.HasPrefix(np, prefix) || (fold && len(np) > len(prefix) && strings.EqualFold(np[:len(prefix)], prefix)) {
			return filepath.Join(dst, filepath.FromSlash(np[len(prefix):])), true
		}
	}
	if cross {
		return "", false
	}
	return p, true
}

// bkupMergeUser combines the user config in a backup with the one in use,
// section by section: the settings from the backup when Settings is restored,
// the library (favourites, chat and its saved notes, JukaLand, playtime and
// trophies, recently played) when Library is. The device's own levels stay,
// device facts stay when the backup comes from the other platform, and paths
// are translated from the backup's folders to this device's.
func bkupMergeUser(current, backup *UserConfig, cats map[string]bool, m bkupManifest, here map[string]string) *UserConfig {
	out := &UserConfig{}
	if current != nil {
		*out = *current
	}
	out.Variables.Custom = bkupCopyMap(out.Variables.Custom)
	if backup == nil {
		return out
	}
	cross := m.Platform != P().Name()
	if cats[bkupCatSettings] {
		keep := out.Variables
		out.Variables = backup.Variables
		out.Variables.Custom = bkupCopyMap(backup.Variables.Custom)
		out.Variables.Volume, out.Variables.VolumeSet = keep.Volume, keep.VolumeSet
		out.Variables.Brightness, out.Variables.BrightnessSet = keep.Brightness, keep.BrightnessSet
		if cross {
			out.Variables.Fullscreen = keep.Fullscreen
			for k := range out.Variables.Custom {
				if bkupDeviceKey(k) {
					delete(out.Variables.Custom, k)
				}
			}
			for k, v := range keep.Custom {
				if bkupDeviceKey(k) {
					out.Variables.Custom[k] = v
				}
			}
		}
		if !cats[bkupCatLibrary] {
			for _, k := range bkupLibraryKeys {
				if v, ok := keep.Custom[k]; ok {
					out.Variables.Custom[k] = v
				} else {
					delete(out.Variables.Custom, k)
				}
			}
		}
		if t, ok := bkupTranslatePath(out.Variables.FileExplorerRoot, m.Roots, here, cross); ok {
			out.Variables.FileExplorerRoot = t
		} else {
			out.Variables.FileExplorerRoot = keep.FileExplorerRoot
		}
		for k, v := range out.Variables.Custom {
			s, ok := v.(string)
			if !ok || !bkupLooksAbsolute(s) {
				continue
			}
			if t, ok := bkupTranslatePath(s, m.Roots, here, cross); ok {
				out.Variables.Custom[k] = t
			} else {
				delete(out.Variables.Custom, k)
			}
		}
	}
	if cats[bkupCatLibrary] {
		out.Favorites, out.Chat, out.JukaLand = backup.Favorites, backup.Chat, backup.JukaLand
		for _, k := range bkupLibraryKeys {
			if v, ok := backup.Variables.Custom[k]; ok {
				out.Variables.Custom[k] = v
			} else {
				delete(out.Variables.Custom, k)
			}
		}
	}
	return out
}

// bkupCopyMap copies a Custom map (one level: the values are not modified).
func bkupCopyMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// bkupDropPendingUserSave cancels a debounced jukauser.json write built from
// the settings in memory: a restore is about to replace the file, and the old
// save would land on top of it two seconds later.
func bkupDropPendingUserSave() {
	userConfigSaveMutex.Lock()
	defer userConfigSaveMutex.Unlock()
	if userConfigSaveTimer != nil {
		userConfigSaveTimer.Stop()
	}
	pendingUserConfig = nil
	pendingUserConfigDir = ""
}

// bkupCommit is the main-thread half of a restore: the user config is merged
// and saved, the config reloaded the way a hot reload installs one (in place,
// so every holder of the config sees it), and every store that keeps a copy
// in memory re-reads its file.
func bkupCommit(config *Config, res bkupRestoreResult) error {
	if config == nil {
		return errors.New("no configuration is loaded")
	}
	// The zone is applied on the main thread, before the stores reload, so the
	// alarm and playtime caches pick up the restored clock. On a desktop the
	// OS owns the zone and dateTimeApplyZone is deliberately not called.
	bkupApplyRestoredZone(res.Zone)
	if res.HasUser {
		var backup UserConfig
		if err := json.Unmarshal(res.UserData, &backup); err != nil {
			return fmt.Errorf("the backup's settings are damaged: %w", err)
		}
		if backup.Variables.Custom == nil {
			backup.Variables.Custom = map[string]interface{}{}
		}
		bkupDropPendingUserSave()
		current := loadUserConfig()
		merged := bkupMergeUser(current, &backup, res.Cats, res.Manifest, bkupHereRoots())
		// The cache takes the merged sections first: a save fills any section
		// it is not given from the cache.
		userConfigCache.Variables = merged.Variables
		userConfigCache.Favorites, userConfigCache.Chat, userConfigCache.JukaLand = merged.Favorites, merged.Chat, merged.JukaLand
		saveUserConfig(merged)
	}
	if res.Next != nil {
		if fresh := applyConfigReload(config, res.Next); fresh != nil && fresh != config {
			*config = *fresh
			appConfig = config
		}
	} else if res.HasUser {
		mergeUserConfig(config, userConfigCache)
		xpackRepaint(config)
	}
	bkupReloadStores(config)
	return nil
}

// bkupZoneOwner and bkupZoneApply are seams for the restore test; production
// uses the same functions the Date & time screen does.
var (
	bkupZoneOwner = dateTimeZoneOwner
	bkupZoneApply = dateTimeApplyZone
)

// bkupApplyRestoredZone applies a backed-up zone only where JukaHub owns it.
// A bad zone is reported and leaves the device on its current clock rather
// than making an otherwise valid restore fail.
func bkupApplyRestoredZone(zone string) {
	zone = strings.TrimSpace(zone)
	if zone == "" || bkupZoneOwner() != tzOwnerJukaHub {
		return
	}
	if err := bkupZoneApply(zone); err != nil {
		logWarn("clock", "restored time zone %s was ignored: %v", zone, err)
	}
}

// bkupReloadStores makes every store that keeps a copy in memory read its file
// again, so the restored data is what the screens show and what the next save
// writes.
func bkupReloadStores(config *Config) {
	loadFavorites()
	playtimeInit(config)
	loadChatMessages()
	rpMutex.Lock()
	recentlyPlayed = map[string]bool{}
	rpMutex.Unlock()
	loadRecentlyPlayed()
	alarmLoad()
	if notes.storePath != "" {
		notesLoad()
	}
	browserBookmarks.mu.Lock()
	browserBookmarks.loaded, browserBookmarks.list = false, nil
	browserBookmarks.mu.Unlock()
	vkLearned.mu.Lock()
	vkLearned.loaded, vkLearned.words = false, nil
	vkLearned.mu.Unlock()
	vkLearnedDirtyAt.Store(0)
	gameRecentsMu.Lock()
	gameRecentsLoaded = false
	gameRecentsMu.Unlock()
	gameOptionsMu.Lock()
	gameOptionsLoaded = false
	gameOptionsMu.Unlock()
	musicFavMu.Lock()
	musicFavData.loaded = false
	musicFavMu.Unlock()
	musicPlaylistState.mu.Lock()
	musicPlaylistState.loaded = false
	musicPlaylistState.mu.Unlock()
	podcastStore.mu.Lock()
	podcastStore.loaded = false
	podcastStore.mu.Unlock()
	partyFriendsStore.mu.Lock()
	partyFriendsStore.loaded = false
	partyFriendsStore.mu.Unlock()
	ipStreamsMu.Lock()
	ipStreamsLoaded = false
	ipStreamsMu.Unlock()
	if _, failed := LoadThemeFiles(themeDir); len(failed) > 0 {
		LogScene("bkup").Warn("restored theme files skipped", "count", len(failed))
	}
	LoadPlugins(config)
	xpackScanAsync()
	if len(qs.sliders) >= 2 && len(qs.cycles) > qsCycleRowPowerMode {
		qsLoadPersisted()
	}
	a11yInit(config)
	bootSfxBind(config)
	xpackRefreshLabel(config)
}

// ── Weekly backup ─────────────────────────────────────────────────────────

// bkupAutoState is when the weekly backup last ran.
type bkupAutoState struct {
	Last string `json:"last"`
}

// bkupAutoEnabled reads the weekly switch (off unless turned on).
func bkupAutoEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	v, ok := customValue(config.Variables.Custom, bkupAutoKey)
	return ok && coerceBool(v)
}

// bkupAutoDue reports whether the weekly backup should run now.
func bkupAutoDue(now time.Time) bool {
	var st bkupAutoState
	if _, err := readJSONWithBackup(filepath.Join(bkupSafetyDir(), "auto.json"), &st); err != nil {
		return true
	}
	last, err := time.Parse(time.RFC3339, st.Last)
	return err != nil || now.Sub(last) >= bkupAutoEvery
}

// bkupMaybeAutoBackup runs the weekly backup when it is switched on and due.
// main.go defers it past the first frame; the work runs on its own goroutine
// and keeps the three newest weekly backups.
func bkupMaybeAutoBackup(config *Config) {
	if !bkupAutoEnabled(config) {
		return
	}
	vars := cfgdocSnapshot(config)
	inputVars := cfgdocInputVars(config, nil)
	version := versionString(config)
	go func() {
		now := time.Now()
		if !bkupAutoDue(now) {
			return
		}
		rep := cfgdocReportFor(vars, inputVars, version)
		var doc []byte
		if rep.Err == nil {
			doc, _ = cfgdocMarshal(rep.Doc)
		}
		cats := map[string]bool{}
		for _, cat := range bkupCategoryOrder {
			cats[cat] = cat != bkupCatSaves
		}
		dir := bkupExportDir()
		path, _, err := bkupCreate(context.Background(), dir, bkupFileName("auto", now), cats, bkupOptions{PluginFiles: true}, "weekly", version, doc, nil)
		if err != nil {
			LogScene("bkup").Warn("weekly backup failed", "err", err)
			return
		}
		bkupPruneKeep(dir, "JukaHub-backup-", "-auto", bkupAutoKeep, path)
		data, err := json.Marshal(bkupAutoState{Last: now.Format(time.RFC3339)})
		if err == nil {
			_ = os.MkdirAll(bkupSafetyDir(), 0o755)
			_ = AtomicWrite(filepath.Join(bkupSafetyDir(), "auto.json"), data, 0o644)
		}
		ShowToast("Weekly backup saved in "+dir, ToastKindInfo)
	}()
}
