package main

// cheats_db.go — where a game's cheats come from (cheats.go has the format
// and the launch).
//
// libretro's cheat database is one .cht file per game, in a folder per
// console named the libretro way ("Nintendo - Game Boy Advance/Metroid
// Fusion (USA) (Code Breaker).cht"). RetroArch's Online Updater → Update
// Cheats unpacks it into cheat_database_path, so a card that ran it already
// has it. JukaHub looks there — the folder retroarch.cfg names, and cheats/
// beside the config and in the install — and in its own data folder.
//
// When there is none, the Cheats sheet offers "Download cheat database":
// libretro's cheats.zip from the buildbot (about 37 MB), saved as
// data/cheats/cheats.zip and NOT unpacked. The database is some fifteen
// thousand small files; written one by one to a TrimUI's SD card they took
// minutes and wear the card for nothing, while reading one entry straight out
// of the zip is instant. Nothing from the archive is ever written to a path
// built from an entry name, so there is no extraction to escape from; entry
// names are still checked (cheatsSafeEntry) because they become record keys.
// The download is HTTPS through downloadFileWithProgressCtx (the fallback
// certificate bundle the firmware lacks), on a goroutine, with the install
// bar for progress and cancel; the zip is checked and only then renamed into
// place, so a cut download never replaces a good database.
//
// Matching a ROM to a file reuses the box-art matcher (artwork_match.go):
// the database names are No-Intro names like the thumbnail server's, plus a
// tag for the cheat device ("(Game Genie)", "(Action Replay)"). The library
// console maps to its libretro folder through gmetaCollectionFor; the exact
// name wins, then the same title with tags, articles and punctuation folded,
// then the loose key; the ROM's own region tags order the releases
// (artworkCandidateScore). Every close file is offered under "Choose another
// cheat file", since one game often has a file per cheat device. Consoles
// that share cartridges (Game Boy and Game Boy Color) look in each other's
// folder when their own has nothing.

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	cheatsDBURL     = "https://buildbot.libretro.com/assets/frontend/cheats.zip"
	cheatsDBDirName = "cheats"
	cheatsDBZipName = "cheats.zip"

	// cheatsMaxCandidates bounds "Choose another cheat file".
	cheatsMaxCandidates = 12
	// cheatsDBMaxEntries refuses an archive that is not the cheat database.
	cheatsDBMaxEntries = 200000
	// cheatsDownloadTimeout bounds the whole transfer on slow handheld Wi-Fi.
	cheatsDownloadTimeout = 20 * time.Minute
)

// cheatSource is one cheat database on the card: a folder of console
// folders, or JukaHub's downloaded zip.
type cheatSource struct {
	Path string
	Zip  bool
}

// cheatRef is one .cht file: its database and the file's slash path inside.
type cheatRef struct {
	Source string
	Entry  string
}

func (r cheatRef) valid() bool { return r.Source != "" && r.Entry != "" }

// name is the file name without .cht.
func (r cheatRef) name() string {
	base := path.Base(r.Entry)
	return strings.TrimSuffix(base, path.Ext(base))
}

// folder is the console folder the file is in.
func (r cheatRef) folder() string {
	return path.Base(path.Dir(r.Entry))
}

// cheatsOwnDir is JukaHub's cheat folder in its data folder.
func cheatsOwnDir() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, cheatsDBDirName)
}

// cheatsOwnZip is the downloaded database, "" with no data folder.
func cheatsOwnZip() string {
	dir := cheatsOwnDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, cheatsDBZipName)
}

// cheatsSafeEntry accepts a relative slash path with no "..", no backslash and
// nothing to clean: what a database file name may be.
func cheatsSafeEntry(entry string) bool {
	if entry == "" || strings.ContainsAny(entry, "\\\x00") || strings.HasPrefix(entry, "/") {
		return false
	}
	if path.Clean(entry) != entry || filepath.VolumeName(entry) != "" {
		return false
	}
	for _, part := range strings.Split(entry, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

// cheatsDirIsDB reports a folder holding console folders ("Sony -
// PlayStation"); RetroArch's per-core folders of game-specific files do not
// count.
func cheatsDirIsDB(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && strings.Contains(e.Name(), " - ") {
			return true
		}
	}
	return false
}

// cheatsSources lists the cheat databases on the card, JukaHub's download
// first. Off the render thread: it reads retroarch.cfg and lists folders.
func cheatsSources() []cheatSource {
	var out []cheatSource
	seen := map[string]bool{}
	addDir := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		key := strings.ToLower(dir)
		if seen[key] {
			return
		}
		seen[key] = true
		if cheatsDirIsDB(dir) {
			out = append(out, cheatSource{Path: dir})
		}
	}
	if z := cheatsOwnZip(); z != "" && regularFile(z) {
		out = append(out, cheatSource{Path: z, Zip: true})
	}
	addDir(cheatsOwnDir())
	for _, cfg := range raScopeExistingConfigs(retroArchConfigCandidates()) {
		if raw, err := os.ReadFile(cfg); err == nil {
			if v, ok := retroArchConfigValue(string(raw), "cheat_database_path"); ok {
				addDir(resolveRetroArchSaveDir(v, cfg))
			}
		}
		addDir(filepath.Join(filepath.Dir(cfg), "cheats"))
		install := raInstallDir(cfg)
		addDir(filepath.Join(install, "cheats"))
		addDir(filepath.Join(install, ".retroarch", "cheats"))
	}
	return out
}

// cheatsListFolder lists the .cht files of one console folder of a source.
func cheatsListFolder(src cheatSource, folder string) []cheatRef {
	if src.Zip {
		return cheatsZipFolder(src.Path, folder)
	}
	entries, err := os.ReadDir(src.Path)
	if err != nil {
		return nil
	}
	name := ""
	for _, e := range entries {
		if e.IsDir() && strings.EqualFold(e.Name(), folder) {
			name = e.Name()
			break
		}
	}
	if name == "" {
		return nil
	}
	files, err := os.ReadDir(filepath.Join(src.Path, name))
	if err != nil {
		return nil
	}
	out := make([]cheatRef, 0, len(files))
	for _, f := range files {
		if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ".cht") {
			continue
		}
		entry := name + "/" + f.Name()
		if cheatsSafeEntry(entry) {
			out = append(out, cheatRef{Source: src.Path, Entry: entry})
		}
	}
	return out
}

// cheatsZipIndex is the downloaded zip's file list by console folder, read
// once per archive (size and time tell a new download).
var cheatsZipIndex struct {
	mu      sync.Mutex
	path    string
	size    int64
	mod     time.Time
	folders map[string][]string
}

// cheatsZipFolders lists an archive's .cht entries by lower-case console
// folder.
func cheatsZipFolders(zipPath string) (map[string][]string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	if len(zr.File) > cheatsDBMaxEntries {
		return nil, fmt.Errorf("archive has %d entries (limit %d)", len(zr.File), cheatsDBMaxEntries)
	}
	out := map[string][]string{}
	for _, f := range zr.File {
		name := f.Name
		if f.FileInfo().IsDir() || !strings.EqualFold(path.Ext(name), ".cht") || !cheatsSafeEntry(name) {
			continue
		}
		dir := path.Dir(name)
		if dir == "." {
			continue
		}
		key := strings.ToLower(path.Base(dir))
		out[key] = append(out[key], name)
	}
	return out, nil
}

func cheatsZipFolder(zipPath, folder string) []cheatRef {
	info, err := os.Stat(zipPath)
	if err != nil {
		return nil
	}
	cheatsZipIndex.mu.Lock()
	defer cheatsZipIndex.mu.Unlock()
	if cheatsZipIndex.path != zipPath || cheatsZipIndex.size != info.Size() || !cheatsZipIndex.mod.Equal(info.ModTime()) {
		folders, err := cheatsZipFolders(zipPath)
		if err != nil {
			logWarn("cheats", "cannot read the cheat database %s: %v", zipPath, err)
			return nil
		}
		cheatsZipIndex.path = zipPath
		cheatsZipIndex.size = info.Size()
		cheatsZipIndex.mod = info.ModTime()
		cheatsZipIndex.folders = folders
	}
	names := cheatsZipIndex.folders[strings.ToLower(folder)]
	out := make([]cheatRef, 0, len(names))
	for _, n := range names {
		out = append(out, cheatRef{Source: zipPath, Entry: n})
	}
	return out
}

// cheatsReadRef reads one .cht file out of its folder or zip, at most
// chtMaxBytes.
func cheatsReadRef(ref cheatRef) ([]byte, error) {
	if !ref.valid() || !cheatsSafeEntry(ref.Entry) {
		return nil, errors.New("no cheat file chosen")
	}
	info, err := os.Stat(ref.Source)
	if err != nil {
		return nil, err
	}
	if info.Mode().IsRegular() {
		zr, err := zip.OpenReader(ref.Source)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.Name != ref.Entry {
				continue
			}
			if f.UncompressedSize64 > chtMaxBytes {
				return nil, fmt.Errorf("%s is too large for a cheat file", path.Base(ref.Entry))
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, chtMaxBytes))
		}
		return nil, os.ErrNotExist
	}
	p := filepath.Join(ref.Source, filepath.FromSlash(ref.Entry))
	if !isPathWithin(p, ref.Source) {
		return nil, errors.New("the cheat file is outside its database")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("not a cheat file")
	}
	data, err := io.ReadAll(io.LimitReader(f, chtMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > chtMaxBytes {
		return nil, fmt.Errorf("%s is too large for a cheat file", path.Base(ref.Entry))
	}
	return data, nil
}

// ── Matching ─────────────────────────────────────────────────────────────

// cheatsCandidates lists the database files close to a game, best first: in
// the console's own folder, else in a sibling console's.
func cheatsCandidates(game libraryGame, sources []cheatSource) []cheatRef {
	collection := gmetaCollectionFor(game.System)
	stem := gameResetStem(game.Path)
	if collection == "" || stem == "" || len(sources) == 0 {
		return nil
	}
	folders := append([]string{collection}, artworkSiblingCollections(collection)...)
	for _, folder := range folders {
		var refs []cheatRef
		seen := map[string]bool{}
		for _, src := range sources {
			for _, r := range cheatsListFolder(src, folder) {
				key := strings.ToLower(r.name())
				if seen[key] {
					continue // the same file in a second database: the first wins
				}
				seen[key] = true
				refs = append(refs, r)
			}
		}
		if got := cheatsRank(refs, stem); len(got) > 0 {
			return got
		}
	}
	return nil
}

// cheatsRank keeps the files whose title is the ROM's, ordered: the exact
// name, then by the ROM's tags (region, revision), then the shorter name.
func cheatsRank(refs []cheatRef, stem string) []cheatRef {
	stem = strings.TrimSpace(stem)
	if len(refs) == 0 || stem == "" {
		return nil
	}
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.name()
	}
	idx := newArtworkIndex(names)
	picked := map[int]bool{}
	var cands []int
	add := func(list []int) {
		for _, i := range list {
			if !picked[i] {
				picked[i] = true
				cands = append(cands, i)
			}
		}
	}
	exact := -1
	for _, s := range []string{stem, artworkServerName(stem)} {
		if i, ok := idx.exact[strings.ToLower(s)]; ok && exact < 0 {
			exact = i
			add([]int{i})
		}
	}
	if tokens := artworkMatchTokens(stem); len(tokens) > 0 {
		key := strings.Join(tokens, " ")
		add(idx.strict[key])
		if alias, ok := artworkAliasFor(stem); ok {
			add(idx.strict[strings.Join(artworkMatchTokens(alias), " ")])
		}
		if loose := artworkLooseKey(tokens); loose != "" {
			add(idx.loose[loose])
		}
		// "Zelda II" for "Zelda II - The Adventure of Link", as the box-art
		// matcher allows it: a numbered title whose files are all one game.
		if len(cands) == 0 && artworkNumbered(tokens) {
			if head := idx.head[key]; len(head) > 0 && idx.oneTitle(head) {
				add(head)
			}
		}
	}
	if len(cands) == 0 {
		return nil
	}
	tags := artworkTags(stem)
	scores := make(map[int]int, len(cands))
	for _, i := range cands {
		scores[i] = artworkCandidateScore(tags, names[i])
	}
	sort.SliceStable(cands, func(a, b int) bool {
		ia, ib := cands[a], cands[b]
		if (ia == exact) != (ib == exact) {
			return ia == exact
		}
		if scores[ia] != scores[ib] {
			return scores[ia] > scores[ib]
		}
		return artworkNameBefore(names[ia], names[ib])
	})
	if len(cands) > cheatsMaxCandidates {
		cands = cands[:cheatsMaxCandidates]
	}
	out := make([]cheatRef, len(cands))
	for i, c := range cands {
		out[i] = refs[c]
	}
	return out
}

// ── What the sheet shows ─────────────────────────────────────────────────

// cheatsLoadResult is everything the Cheats sheet needs for one game, worked
// out on a goroutine (cheatsLoadFor).
type cheatsLoadResult struct {
	DB       bool // a cheat database is on the card
	OwnZip   bool // JukaHub's downloaded cheats.zip is there
	Cands    []cheatRef
	Chosen   cheatRef
	Cheats   []chtCheat
	Problem  string // why the list is empty although a file was found
	NotRA    string // the emulator, when the game does not start in RetroArch
	UserFile string // RetroArch's own game-specific file, which wins
}

// cheatsLoadFor finds the game's cheats: the forced file (picked in the
// sheet), else the one the record remembers, else the best match.
func cheatsLoadFor(game libraryGame, forced cheatRef) cheatsLoadResult {
	var res cheatsLoadResult
	sources := cheatsSources()
	res.DB = len(sources) > 0
	if z := cheatsOwnZip(); z != "" && regularFile(z) {
		res.OwnZip = true
	}
	if launcher := resolveGameLauncherFor(game); launcher != "" {
		if !retroArchLauncher(launcher) {
			res.NotRA = strings.TrimSuffix(filepath.Base(launcher), filepath.Ext(launcher))
		} else {
			res.UserFile = cheatsUserFileFor(launcher, game)
		}
	}
	res.Cands = cheatsCandidates(game, sources)
	chosen := forced
	if !chosen.valid() {
		if rec, ok := cheatsRecordFor(game.Path); ok && rec.Source != "" {
			chosen = cheatRef{Source: rec.Source, Entry: rec.Entry}
		}
	}
	if !chosen.valid() && len(res.Cands) > 0 {
		chosen = res.Cands[0]
	}
	if chosen.valid() {
		data, err := cheatsReadRef(chosen)
		if err != nil && !forced.valid() && len(res.Cands) > 0 && chosen != res.Cands[0] {
			// The remembered file is gone (a database moved or was replaced):
			// the best match stands in.
			chosen = res.Cands[0]
			data, err = cheatsReadRef(chosen)
		}
		if err != nil {
			res.Problem = "Cannot read " + chosen.name() + ": " + firstLine(err.Error())
		} else {
			res.Chosen = chosen
			res.Cheats = parseCht(data)
			if len(res.Cheats) == 0 {
				res.Problem = chosen.name() + " holds no cheats"
			}
		}
	}
	if res.Chosen.valid() {
		listed := false
		for _, c := range res.Cands {
			if c == res.Chosen {
				listed = true
				break
			}
		}
		if !listed {
			res.Cands = append([]cheatRef{res.Chosen}, res.Cands...)
		}
	}
	return res
}

// cheatsUserFileFor is the game-specific cheat file the user saved in
// RetroArch for this game, "" when there is none.
func cheatsUserFileFor(launcher string, game libraryGame) string {
	folder := cheatsCoreFolder(launcher, game.System)
	stem := gameResetStem(game.Path)
	if folder == "" || stem == "" {
		return ""
	}
	for _, cfg := range raScopeTargets(raLaunchCandidates(launcher, retroArchConfigCandidates())) {
		raw, err := os.ReadFile(cfg)
		if err != nil {
			continue
		}
		dir, _ := cheatsLaunchDir(string(raw), cfg)
		p := filepath.Join(dir, folder, stem+".cht")
		if regularFile(p) && cheatsUserFile(p) {
			return p
		}
	}
	return ""
}

// ── Download ─────────────────────────────────────────────────────────────

// cheatsDownloading keeps one download at a time; cheatsDBGen counts finished
// ones, so an open sheet reloads its list.
var (
	cheatsDownloading atomic.Bool
	cheatsDBGen       atomic.Uint64
)

// cheatsStartDownload fetches libretro's cheat database on a goroutine, with
// the install bar for progress and cancel. Render thread.
func cheatsStartDownload() bool {
	if !cheatsDownloading.CompareAndSwap(false, true) {
		ShowToast("The cheat database is still downloading", ToastKindInfo)
		return false
	}
	dir := cheatsOwnDir()
	if dir == "" {
		cheatsDownloading.Store(false)
		ShowToast("JukaHub has no data folder for the cheat database", ToastKindWarning)
		return false
	}
	gen, runCtx := installBarBeginRun("cheat database")
	go func() {
		defer cheatsDownloading.Store(false)
		n, err := cheatsDownload(runCtx, gen, dir)
		switch {
		case err == nil:
			installBarDoneRun(gen)
			cheatsDBGen.Add(1)
			ShowToast(fmt.Sprintf("Cheat database ready · cheats for %d games", n), ToastKindSuccess)
		case runCtx.Err() != nil:
			installBarAbortRun(gen)
		default:
			logWarn("cheats", "cheat database download failed: %v", err)
			installBarFailRun(gen, firstLine(err.Error()))
			ShowToast("Cheat database download failed: "+firstLine(err.Error()), ToastKindWarning)
		}
	}()
	return true
}

// cheatsDownload saves cheats.zip into dir and returns how many cheat files it
// holds. The archive is written under a hidden name, checked, flushed and only
// then renamed over the old one.
func cheatsDownload(parent context.Context, gen uint64, dir string) (int, error) {
	ctx, cancel := context.WithTimeout(parent, cheatsDownloadTimeout)
	defer cancel()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	final := filepath.Join(dir, cheatsDBZipName)
	part := filepath.Join(dir, "."+cheatsDBZipName+".part")
	defer os.Remove(part)
	installBarUpdateRun(gen, installPhaseDownload, 0, 0)
	if err := downloadFileWithProgressCtx(ctx, cheatsDBURL, part, func(done, total int64) {
		installBarUpdateRun(gen, installPhaseDownload, done, total)
	}); err != nil {
		return 0, err
	}
	installBarUpdateRun(gen, installPhaseVerify, 0, 0)
	folders, err := cheatsZipFolders(part)
	if err != nil {
		return 0, fmt.Errorf("not a cheat database: %w", err)
	}
	n := 0
	for _, list := range folders {
		n += len(list)
	}
	if n == 0 {
		return 0, errors.New("the download holds no cheat files")
	}
	if err := mcSyncFile(part); err != nil {
		return 0, err
	}
	if err := os.Rename(part, final); err != nil {
		return 0, err
	}
	syncDirectory(dir)
	return n, nil
}
