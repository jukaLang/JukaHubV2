package main

// devsync.go — Device Sync: more than saves between two paired handhelds.
//
// Save sync (ssync.go) already makes the saves of two paired devices the
// same over the paired HTTPS connection. Device Sync adds categories, each a
// switch on the Save sync page (devsync_ui.go), all on the same connection,
// port and pairing — no new listener:
//
//   - Saves and Save states: the save folders split in two (a folder whose
//     name says "states" is a states folder); either can be left out. Same
//     rules as before: newest wins, both kept when both changed, a safety
//     snapshot first.
//   - BIOS: firmware files one device lacks are copied to it. A file that
//     exists on both sides is never replaced, even when it differs, and
//     nothing is ever deleted.
//   - ROMs (off by default): games one device lacks, console folder by
//     console folder, the sizes shown before anything is copied; never
//     replaced, never deleted. Large files resume where a dropped connection
//     left them (a hidden .jukadevsync folder holds the part).
//   - Collections, and Recents & play time: merged both ways
//     (devsync_data.go).
//   - Settings: a one-way "Send my settings" with a confirm on both screens.
//
// The device that compares asks GET devsync/caps first; an older JukaHub
// answers 404 and only its saves sync (the page says so). The answering side
// needs nothing switched on: the device that compares decides what moves. A
// USB cable cannot carry any of this: the stock TrimUI kernels have no USB
// network gadget (RNDIS/ECM).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// devsyncCatsKey lists the switched-on categories ("none" for none).
	devsyncCatsKey = "devsyncCategories"

	dsCatSaves       = "saves"
	dsCatStates      = "states"
	dsCatBIOS        = "bios"
	dsCatCollections = "collections"
	dsCatRecents     = "recents"
	dsCatROMs        = "roms"
	dsCatSettings    = "settings"

	devsyncCapsPath        = "/peer/v1/devsync/caps"
	devsyncFilesPath       = "/peer/v1/devsync/files"
	devsyncFilePath        = "/peer/v1/devsync/file"
	devsyncPartPath        = "/peer/v1/devsync/part"
	devsyncCollectionsPath = "/peer/v1/devsync/collections"
	devsyncRecentsPath     = "/peer/v1/devsync/recents"
	devsyncSettingsPath    = "/peer/v1/devsync/settings"

	// devsyncMaxFiles bounds one file list (a big ROM card is ~20 000 files).
	devsyncMaxFiles = 60000
	// devsyncMaxBIOS and devsyncMaxROM bound one file of each kind.
	devsyncMaxBIOS = int64(1) << 30
	devsyncMaxROM  = int64(64) << 30
	// devsyncSpare is the room left free on a card after a copy.
	devsyncSpare = int64(64) << 20
	// devsyncPartDir holds unfinished copies inside each folder.
	devsyncPartDir = ".jukadevsync"
)

var (
	errDevsyncTooMany  = errors.New("there are more files than one sync can list")
	errDevsyncPartBusy = errors.New("this file is being copied already")
)

// ── Categories ───────────────────────────────────────────────────────────

// devsyncCatSpec is one switch on the Save sync page.
type devsyncCatSpec struct {
	name, label, note, icon string
	on                      bool
}

var devsyncCatSpecs = []devsyncCatSpec{
	{dsCatSaves, "Saves", "Game saves: newest wins, both kept when both changed", "backup", true},
	{dsCatStates, "Save states", "Saved moments, same rules as saves", "copy", true},
	{dsCatBIOS, "BIOS", "Firmware one device lacks is copied; nothing is replaced or deleted", "files", true},
	{dsCatCollections, "Collections", "Merged by name: every game of both, in the newer list's order", "files", true},
	{dsCatRecents, "Recents & play time", "Continue lists merged by last played; play time keeps the higher figure", "sync", true},
	{dsCatROMs, "ROMs", "Games one device lacks, per console; the size is shown before copying", "download", false},
}

var devsyncCats struct {
	mu     sync.Mutex
	loaded bool
	on     map[string]bool
}

func init() {
	deferStartupTask("devsync-categories", func() { devsyncCatsLoad(appConfig) })
}

// devsyncCatsParse reads the stored list; ok=false when it was never set.
func devsyncCatsParse(c *Config) (map[string]bool, bool) {
	out := map[string]bool{}
	if c == nil {
		return out, false
	}
	raw, ok := customValue(c.Variables.Custom, devsyncCatsKey)
	if !ok || raw == nil {
		return out, false
	}
	for _, part := range strings.Split(fmt.Sprint(raw), ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		for _, s := range devsyncCatSpecs {
			if s.name == name {
				out[name] = true
			}
		}
	}
	return out, true
}

// devsyncCatsLoad reads the switches off the config (main thread).
func devsyncCatsLoad(c *Config) {
	on, ok := devsyncCatsParse(c)
	if !ok {
		for _, s := range devsyncCatSpecs {
			if s.on {
				on[s.name] = true
			}
		}
	}
	devsyncCats.mu.Lock()
	devsyncCats.on, devsyncCats.loaded = on, true
	devsyncCats.mu.Unlock()
}

// devsyncCatOn reports whether a category is switched on (any goroutine).
func devsyncCatOn(name string) bool {
	devsyncCats.mu.Lock()
	defer devsyncCats.mu.Unlock()
	if !devsyncCats.loaded {
		for _, s := range devsyncCatSpecs {
			if s.name == name {
				return s.on
			}
		}
		return false
	}
	return devsyncCats.on[name]
}

// devsyncSetCat switches a category and stores the list (main thread).
func devsyncSetCat(c *Config, name string, on bool) {
	devsyncCatsLoad(c)
	devsyncCats.mu.Lock()
	devsyncCats.on[name] = on
	var names []string
	for _, s := range devsyncCatSpecs {
		if devsyncCats.on[s.name] {
			names = append(names, s.name)
		}
	}
	devsyncCats.mu.Unlock()
	value := strings.Join(names, ",")
	if value == "" {
		value = "none"
	}
	setStoreString(c, devsyncCatsKey, value)
}

// devsyncIsStatesRoot reports whether a save folder holds save states.
func devsyncIsStatesRoot(name string) bool {
	return strings.Contains(strings.ToLower(name), "state")
}

// devsyncFilterSaves drops the save or state folders that are switched off
// from a comparison's plan. The files left out keep their sync base.
func devsyncFilterSaves(cmp ssyncComparison) ssyncPlan {
	saves, states := devsyncCatOn(dsCatSaves), devsyncCatOn(dsCatStates)
	if saves && states {
		return cmp.Plan
	}
	names := map[string]string{}
	for _, r := range cmp.Remote.Roots {
		names[r.Key] = r.Name
	}
	for _, r := range cmp.Local.Roots {
		names[r.Key] = r.Name
	}
	return ssyncFilterPlan(cmp.Plan, func(it ssyncItem) bool {
		if devsyncIsStatesRoot(names[it.Root]) {
			return states
		}
		return saves
	})
}

// ── Capabilities ─────────────────────────────────────────────────────────

// devsyncCaps is what devsync/caps answers.
type devsyncCaps struct {
	V    int      `json:"v"`
	Cats []string `json:"cats"`
}

// devsyncRoutes adds Device Sync to the paired API (peernet.go).
func devsyncRoutes(mux *http.ServeMux) {
	mux.HandleFunc(devsyncCapsPath, peerGuard(true, devsyncHandleCaps))
	mux.HandleFunc(devsyncFilesPath, peerGuard(true, devsyncHandleFiles))
	mux.HandleFunc(devsyncFilePath, peerGuard(true, devsyncHandleFile))
	mux.HandleFunc(devsyncPartPath, peerGuard(true, devsyncHandlePart))
	mux.HandleFunc(devsyncCollectionsPath, peerGuard(true, devsyncHandleCollections))
	mux.HandleFunc(devsyncRecentsPath, peerGuard(true, devsyncHandleRecents))
	mux.HandleFunc(devsyncSettingsPath, peerGuard(true, devsyncHandleSettings))
}

func devsyncHandleCaps(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	peerWriteJSONResponse(w, http.StatusOK, devsyncCaps{V: 1, Cats: []string{
		dsCatStates, dsCatBIOS, dsCatCollections, dsCatRecents, dsCatSettings, dsCatROMs,
	}})
}

// devsyncFetchCaps asks a device what it can sync. old is true for a JukaHub
// from before Device Sync (it has no such endpoint).
func devsyncFetchCaps(ctx context.Context, peerID string) (caps devsyncCaps, old bool, err error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err = peerDoJSON(cctx, peerID, http.MethodGet, devsyncCapsPath, nil, &caps, 0)
	var se *peerStatusError
	if errors.As(err, &se) && se.code == http.StatusNotFound {
		return devsyncCaps{}, true, nil
	}
	return caps, false, err
}

// ── Folders ──────────────────────────────────────────────────────────────

// devsyncRoot is one BIOS or ROM folder, under a key both devices agree on.
type devsyncRoot struct {
	Key string
	Dir string
}

// devsyncRootKey names a folder by its place: "card-bios", "card-roms",
// "card-retroarch-retroarch-system". Folders off the card and the data folder
// (firmware's own /usr/trimui) get no key and are left out.
func devsyncRootKey(dir string) string {
	dir = filepath.Clean(dir)
	if card := saveManagerSDRoot(); card != "" {
		if rel, ok := colNextuiCardRel(card, dir); ok {
			return "card-" + saveRootSlug(rel)
		}
	}
	if data, err := P().DataDir(); err == nil && data != "" {
		if rel, ok := colNextuiCardRel(data, dir); ok {
			return "data-" + saveRootSlug(rel)
		}
	}
	return ""
}

// devsyncRoots is the folders a file category reads and writes here.
func devsyncRoots(cat string) []devsyncRoot {
	var dirs []string
	switch cat {
	case dsCatBIOS:
		dirs = biosDoctorRoots()
	case dsCatROMs:
		dirs = romLibraryRoots()
	}
	var out []devsyncRoot
	seen := map[string]bool{}
	for _, dir := range dirs {
		if !dirExists(dir) {
			continue
		}
		key := devsyncRootKey(dir)
		if key == "" || seen[key] || !ssyncValidKey(key) {
			continue
		}
		seen[key] = true
		out = append(out, devsyncRoot{Key: key, Dir: filepath.Clean(dir)})
	}
	return out
}

// devsyncRootMap is key → folder.
func devsyncRootMap(cat string) map[string]string {
	out := map[string]string{}
	for _, r := range devsyncRoots(cat) {
		out[r.Key] = r.Dir
	}
	return out
}

// devsyncMaxFile is the largest file a category carries.
func devsyncMaxFile(cat string) int64 {
	if cat == dsCatROMs {
		return devsyncMaxROM
	}
	return devsyncMaxBIOS
}

// devsyncCleanRel accepts a relative path from another device only when it
// is clean, stays inside its folder, has no hidden part and can exist on
// FAT32 and Windows.
func devsyncCleanRel(p string) (string, bool) {
	if p == "" || len(p) > 1024 || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
		return "", false
	}
	if len(p) >= 2 && p[1] == ':' {
		return "", false
	}
	if path.Clean(p) != p {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return "", false
		}
		if strings.ContainsAny(part, `:*?"<>|`) || strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") {
			return "", false
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f {
				return "", false
			}
		}
	}
	return p, true
}

// devsyncLocalPath is where rel lands under rootDir, refusing anything that
// would leave it (also through a link).
func devsyncLocalPath(rootDir, rel string) (string, error) {
	clean, ok := devsyncCleanRel(rel)
	if !ok {
		return "", errSsyncBadName
	}
	dst := filepath.Join(rootDir, filepath.FromSlash(clean))
	if !isPathWithin(dst, rootDir) {
		return "", errSsyncBadName
	}
	if !parentResolvesWithin(rootDir, dst) {
		return "", errors.New("the folder links outside itself")
	}
	return dst, nil
}

// devsyncPartStem names rel's parts in the part folder.
func devsyncPartStem(rel string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(rel)))
	return hex.EncodeToString(sum[:16])
}

// devsyncPartFile is where an unfinished copy of rel waits. The source file's
// size and time are in the name, so a part is only ever continued from the
// very file it was started from: a part of an older version is never glued
// to the new one's end.
func devsyncPartFile(rootDir, rel string, size, mtime int64) string {
	return filepath.Join(rootDir, devsyncPartDir,
		devsyncPartStem(rel)+"-"+strconv.FormatInt(size, 10)+"-"+strconv.FormatInt(mtime, 10)+".part")
}

// devsyncDropStaleParts removes rel's parts from other versions of the
// source file when a copy starts from the beginning; part itself stays.
func devsyncDropStaleParts(part string) {
	dir := filepath.Dir(part)
	stem, _, _ := strings.Cut(filepath.Base(part), "-")
	entries, _ := os.ReadDir(dir)
	for _, de := range entries {
		name := de.Name()
		if strings.HasPrefix(name, stem+"-") && strings.HasSuffix(name, ".part") && de.Type().IsRegular() &&
			filepath.Join(dir, name) != part {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// devsyncParts are the files being copied right now: one copy per file at a
// time, so a pull from this device and a push from another (or a retry while
// the dropped try still writes) never write the same part together.
var devsyncParts struct {
	mu   sync.Mutex
	busy map[string]bool
}

func devsyncPartKey(rootDir, rel string) string {
	return strings.ToLower(filepath.Clean(rootDir) + "\x00" + rel)
}

// devsyncClaimPart reserves rel for one copy; false while another runs.
func devsyncClaimPart(rootDir, rel string) bool {
	key := devsyncPartKey(rootDir, rel)
	devsyncParts.mu.Lock()
	defer devsyncParts.mu.Unlock()
	if devsyncParts.busy[key] {
		return false
	}
	if devsyncParts.busy == nil {
		devsyncParts.busy = map[string]bool{}
	}
	devsyncParts.busy[key] = true
	return true
}

func devsyncReleasePart(rootDir, rel string) {
	devsyncParts.mu.Lock()
	delete(devsyncParts.busy, devsyncPartKey(rootDir, rel))
	devsyncParts.mu.Unlock()
}

// devsyncTaken reports whether anything is at p — a file, a folder, even a
// link that leads nowhere — so nothing there is ever replaced. An error other
// than "not there" counts as taken.
func devsyncTaken(p string) bool {
	_, err := os.Lstat(p)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// devsyncSkipDir is a folder of pictures or papers inside a ROM folder.
func devsyncSkipDir(cat, name string) bool {
	if cat != dsCatROMs {
		return false
	}
	switch strings.ToLower(name) {
	case "imgs", "images", "media", "snap", "snaps", "manuals", "videos", "boxart", "thumbnails", "covers", "previews":
		return true
	}
	return devsyncPortsDir(name)
}

// devsyncPortsDir is a ports folder: its games are launch scripts and
// programs, which another device never plants here.
func devsyncPortsDir(name string) bool {
	lower := strings.ToLower(name)
	return lower == "ports" || lower == "portmaster" || strings.HasPrefix(lower, "ports (")
}

// devsyncRunnable is a ROM path that would run as a program: anything in a
// ports folder, or a shell script.
func devsyncRunnable(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if devsyncPortsDir(part) {
			return true
		}
	}
	return strings.HasSuffix(strings.ToLower(rel), ".sh")
}

// devsyncSkipFile is a list, cache or half-copied file, not a game or BIOS.
func devsyncSkipFile(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "map.txt", "gamelist.xml", "miyoogamelist.xml":
		return true
	}
	for _, ext := range []string{".db", ".tmp", ".part", ".jukasync"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// ── File lists ───────────────────────────────────────────────────────────

// devsyncEntry is one file in a list.
type devsyncEntry struct {
	Root  string `json:"root"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

// devsyncManifest is a device's BIOS or ROM files, with the free space of
// each folder's card.
type devsyncManifest struct {
	V     int              `json:"v"`
	Cat   string           `json:"cat"`
	Roots []string         `json:"roots"`
	Files []devsyncEntry   `json:"files"`
	Free  map[string]int64 `json:"free,omitempty"`
}

// devsyncBuildManifest lists a category's files here: regular files only,
// no hidden ones, no picture folders; ROMs only inside a console folder.
func devsyncBuildManifest(ctx context.Context, cat string) (devsyncManifest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m := devsyncManifest{V: 1, Cat: cat, Free: map[string]int64{}}
	maxFile := devsyncMaxFile(cat)
	for _, root := range devsyncRoots(cat) {
		m.Roots = append(m.Roots, root.Key)
		if free, err := freeBytes(root.Dir); err == nil {
			m.Free[root.Key] = free
		}
		rootDir := root.Dir
		key := root.Key
		err := filepath.WalkDir(rootDir, func(p string, d os.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				if p == rootDir {
					return err
				}
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if p != rootDir && (skipScanEntryName(name) || devsyncSkipDir(cat, name)) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || skipScanEntryName(name) || devsyncSkipFile(name) {
				return nil
			}
			rel, err := filepath.Rel(rootDir, p)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if _, ok := devsyncCleanRel(rel); !ok || (cat == dsCatROMs && (!strings.Contains(rel, "/") || devsyncRunnable(rel))) {
				return nil
			}
			// An empty file is no game or firmware (and an empty upload
			// cannot carry the length the other device checks).
			info, err := d.Info()
			if err != nil || info.Size() <= 0 || info.Size() > maxFile {
				return nil
			}
			if len(m.Files) >= devsyncMaxFiles {
				return errDevsyncTooMany
			}
			m.Files = append(m.Files, devsyncEntry{Root: key, Path: rel, Size: info.Size(), MTime: info.ModTime().Unix()})
			return nil
		})
		if err != nil {
			return devsyncManifest{}, err
		}
	}
	return m, nil
}

// ── Plans ────────────────────────────────────────────────────────────────

// devsyncGroup is one console (ROMs) or folder (BIOS) of a file plan.
type devsyncGroup struct {
	Name      string
	Pull      int
	Push      int
	PullBytes int64
	PushBytes int64
}

// devsyncFilePlan is what copying the missing files of a category means.
type devsyncFilePlan struct {
	Cat       string
	Pull      []devsyncEntry
	Push      []devsyncEntry
	PullBytes int64
	PushBytes int64
	Groups    []devsyncGroup
	FreeHere  map[string]int64
	FreeThere map[string]int64
}

// devsyncGroupOf is the console folder of a ROM, or the folder key of a BIOS.
func devsyncGroupOf(cat string, e devsyncEntry) string {
	if cat == dsCatROMs {
		if i := strings.IndexByte(e.Path, '/'); i > 0 {
			return e.Path[:i]
		}
	}
	return e.Root
}

// devsyncPlanFiles compares two lists: what is only there comes here, what is
// only here goes there, into folders both devices have. A file both have is
// left alone, whatever it holds. Names are compared without case (FAT).
func devsyncPlanFiles(cat string, local, remote devsyncManifest) devsyncFilePlan {
	plan := devsyncFilePlan{Cat: cat, FreeHere: local.Free, FreeThere: remote.Free}
	localRoots, remoteRoots := map[string]bool{}, map[string]bool{}
	for _, k := range local.Roots {
		localRoots[k] = true
	}
	for _, k := range remote.Roots {
		remoteRoots[k] = true
	}
	key := func(e devsyncEntry) string { return strings.ToLower(e.Root + "/" + e.Path) }
	here, there := map[string]bool{}, map[string]bool{}
	for _, e := range local.Files {
		here[key(e)] = true
	}
	groups := map[string]*devsyncGroup{}
	group := func(e devsyncEntry) *devsyncGroup {
		name := devsyncGroupOf(cat, e)
		g := groups[strings.ToLower(name)]
		if g == nil {
			g = &devsyncGroup{Name: name}
			groups[strings.ToLower(name)] = g
		}
		return g
	}
	maxFile := devsyncMaxFile(cat)
	for _, e := range remote.Files {
		if _, ok := devsyncCleanRel(e.Path); !ok || !ssyncValidKey(e.Root) || e.Size <= 0 || e.Size > maxFile ||
			(cat == dsCatROMs && !strings.Contains(e.Path, "/")) {
			continue
		}
		there[key(e)] = true
		if here[key(e)] || !localRoots[e.Root] {
			continue
		}
		plan.Pull = append(plan.Pull, e)
		plan.PullBytes += e.Size
		g := group(e)
		g.Pull++
		g.PullBytes += e.Size
	}
	for _, e := range local.Files {
		if there[key(e)] || !remoteRoots[e.Root] {
			continue
		}
		plan.Push = append(plan.Push, e)
		plan.PushBytes += e.Size
		g := group(e)
		g.Push++
		g.PushBytes += e.Size
	}
	for _, g := range groups {
		plan.Groups = append(plan.Groups, *g)
	}
	sort.Slice(plan.Groups, func(a, b int) bool {
		return strings.ToLower(plan.Groups[a].Name) < strings.ToLower(plan.Groups[b].Name)
	})
	return plan
}

// devsyncCompareFiles fetches the other device's list of a category and plans
// the copies.
func devsyncCompareFiles(ctx context.Context, peerID, cat string) (devsyncFilePlan, error) {
	var remote devsyncManifest
	mctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	err := peerDoJSON(mctx, peerID, http.MethodGet, devsyncFilesPath+"?cat="+url.QueryEscape(cat), nil, &remote, 32<<20)
	cancel()
	if err != nil {
		return devsyncFilePlan{}, err
	}
	if remote.V != 1 || remote.Cat != cat || len(remote.Files) > devsyncMaxFiles {
		return devsyncFilePlan{}, errors.New("the other device sent a list this version cannot read")
	}
	local, err := devsyncBuildManifest(ctx, cat)
	if err != nil {
		return devsyncFilePlan{}, err
	}
	return devsyncPlanFiles(cat, local, remote), nil
}

// ── Copying ──────────────────────────────────────────────────────────────

// devsyncRunFiles copies a file plan, leaving out the groups in skip. Pulls
// stop when this card would get too full; pushes when the other device says
// so. It returns how many files were copied and the errors met.
func devsyncRunFiles(ctx context.Context, peerID string, plan devsyncFilePlan, skip map[string]bool, progress func(done, total int, current string)) (copied int, errs []string) {
	roots := devsyncRootMap(plan.Cat)
	var pulls, pushes []devsyncEntry
	for _, e := range plan.Pull {
		if !skip[strings.ToLower(devsyncGroupOf(plan.Cat, e))] {
			pulls = append(pulls, e)
		}
	}
	for _, e := range plan.Push {
		if !skip[strings.ToLower(devsyncGroupOf(plan.Cat, e))] {
			pushes = append(pushes, e)
		}
	}
	total := len(pulls) + len(pushes)
	done := 0
	note := func(err error, e devsyncEntry) {
		if err != nil && len(errs) < 20 {
			errs = append(errs, path.Base(e.Path)+": "+firstLine(err.Error()))
		}
	}
	for _, e := range pulls {
		if ctx.Err() != nil {
			return copied, append(errs, "Stopped — the rest is left for the next sync")
		}
		if progress != nil {
			progress(done, total, e.Path)
		}
		done++
		rootDir, ok := roots[e.Root]
		if !ok {
			continue
		}
		dst, err := devsyncLocalPath(rootDir, e.Path)
		if err != nil {
			note(err, e)
			continue
		}
		if devsyncTaken(dst) {
			continue
		}
		if free, ferr := freeBytes(rootDir); ferr == nil && free < e.Size+devsyncSpare {
			errs = append(errs, "This card is full — the rest was not copied")
			break
		}
		if err := devsyncPull(ctx, peerID, plan.Cat, e, rootDir, dst); err != nil {
			note(err, e)
			continue
		}
		copied++
	}
	for _, e := range pushes {
		if ctx.Err() != nil {
			return copied, append(errs, "Stopped — the rest is left for the next sync")
		}
		if progress != nil {
			progress(done, total, e.Path)
		}
		done++
		rootDir, ok := roots[e.Root]
		if !ok {
			continue
		}
		src, err := devsyncLocalPath(rootDir, e.Path)
		if err != nil {
			note(err, e)
			continue
		}
		if err := devsyncPush(ctx, peerID, plan.Cat, e, src); err != nil {
			var se *peerStatusError
			if errors.As(err, &se) && se.code == http.StatusInsufficientStorage {
				errs = append(errs, "The other device's card is full — the rest was not sent")
				break
			}
			note(err, e)
			continue
		}
		copied++
	}
	if progress != nil {
		progress(total, total, "")
	}
	return copied, errs
}

// devsyncQuery is the query naming one file of a category.
func devsyncQuery(cat string, e devsyncEntry) url.Values {
	q := url.Values{}
	q.Set("cat", cat)
	q.Set("root", e.Root)
	q.Set("path", e.Path)
	return q
}

// devsyncPull copies the other device's file here, going on from an earlier
// try's part when there is one.
func devsyncPull(ctx context.Context, peerID, cat string, e devsyncEntry, rootDir, dst string) error {
	if !devsyncClaimPart(rootDir, e.Path) {
		return errDevsyncPartBusy
	}
	defer devsyncReleasePart(rootDir, e.Path)
	part := devsyncPartFile(rootDir, e.Path, e.Size, e.MTime)
	var have int64
	if info, err := os.Lstat(part); err == nil && info.Mode().IsRegular() && info.Size() < e.Size {
		have = info.Size()
	} else if err == nil {
		_ = os.Remove(part)
	}
	if have == 0 {
		devsyncDropStaleParts(part)
	}
	ctx, cancel := context.WithTimeout(ctx, ssyncTransferTimeout(e.Size-have))
	defer cancel()
	req, client, err := peerRequest(ctx, peerID, http.MethodGet, devsyncFilePath+"?"+devsyncQuery(cat, e).Encode(), nil, -1)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := peerSend(peerID, client, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// The length must be what the list said: a file that changed there since
	// would otherwise be cut to the old size and kept here for good.
	lengthIs := func(want int64) bool { return resp.ContentLength < 0 || resp.ContentLength == want }
	flags := os.O_WRONLY | os.O_CREATE
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0 &&
		strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes "+strconv.FormatInt(have, 10)+"-") &&
		lengthIs(e.Size-have):
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK && lengthIs(e.Size):
		flags |= os.O_TRUNC
		have = 0
	case resp.StatusCode == http.StatusOK, resp.StatusCode == http.StatusPartialContent:
		return errSsyncChanged
	default:
		return peerResponseError(resp)
	}
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, e.Size-have))
	syncErr := f.Sync()
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return copyErr
	case syncErr != nil:
		return syncErr
	case closeErr != nil:
		return closeErr
	}
	if have+n != e.Size {
		return fmt.Errorf("got %s of %s — sync again to go on", humanBytes(have+n), humanBytes(e.Size))
	}
	return devsyncFinish(part, dst, e.Size, e.MTime)
}

// devsyncFinish puts a finished part in place — unless something got there
// first, which is never replaced — after checking it holds the whole file.
func devsyncFinish(part, dst string, size, mtime int64) error {
	if devsyncTaken(dst) {
		_ = os.Remove(part)
		return nil
	}
	if info, err := os.Lstat(part); err != nil || !info.Mode().IsRegular() || info.Size() != size {
		_ = os.Remove(part)
		return errSsyncChanged
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if mtime > 0 {
		t := time.Unix(mtime, 0)
		_ = os.Chtimes(part, t, t)
	}
	return os.Rename(part, dst)
}

// devsyncPartAnswer is devsync/part: how much of a file the device already
// holds from an earlier try, and whether the whole file is there.
type devsyncPartAnswer struct {
	Size   int64 `json:"size"`
	Exists bool  `json:"exists"`
}

// devsyncPush sends this device's file to the other one, going on from the
// part it kept from an earlier try.
func devsyncPush(ctx context.Context, peerID, cat string, e devsyncEntry, src string) error {
	q := devsyncQuery(cat, e)
	// Size and time name the part there (devsyncPartFile).
	q.Set("size", strconv.FormatInt(e.Size, 10))
	q.Set("mtime", strconv.FormatInt(e.MTime, 10))
	var part devsyncPartAnswer
	pctx, pcancel := context.WithTimeout(ctx, 20*time.Second)
	err := peerDoJSON(pctx, peerID, http.MethodGet, devsyncPartPath+"?"+q.Encode(), nil, &part, 0)
	pcancel()
	if err != nil {
		return err
	}
	if part.Exists {
		return nil
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != e.Size || info.ModTime().Unix() != e.MTime {
		return errSsyncChanged
	}
	off := part.Size
	if off < 0 || off >= e.Size {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	q.Set("offset", strconv.FormatInt(off, 10))
	ctx, cancel := context.WithTimeout(ctx, ssyncTransferTimeout(e.Size-off))
	defer cancel()
	resp, err := peerDo(ctx, peerID, http.MethodPut, devsyncFilePath+"?"+q.Encode(), io.LimitReader(f, e.Size-off), e.Size-off)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peerResponseError(resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// ── Serving the other device ─────────────────────────────────────────────

// devsyncFileCat reads and checks a request's category.
func devsyncFileCat(w http.ResponseWriter, r *http.Request) (string, bool) {
	cat := r.URL.Query().Get("cat")
	if cat != dsCatBIOS && cat != dsCatROMs {
		http.Error(w, "unknown category", http.StatusBadRequest)
		return "", false
	}
	// This device's own switch decides too: BIOS or ROMs switched off here
	// means no list, no copy in and no copy out.
	if !devsyncCatOn(cat) {
		http.Error(w, "switched off on the other handheld — turn it on there too", http.StatusForbidden)
		return "", false
	}
	if gameScreenSuspended() {
		http.Error(w, "a game is running there — close it first", http.StatusConflict)
		return "", false
	}
	return cat, true
}

// devsyncHandleFiles answers with a category's file list.
func devsyncHandleFiles(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	cat, ok := devsyncFileCat(w, r)
	if !ok {
		return
	}
	m, err := devsyncBuildManifest(r.Context(), cat)
	if err != nil {
		http.Error(w, firstLine(err.Error()), http.StatusInternalServerError)
		return
	}
	peerWriteJSONResponse(w, http.StatusOK, m)
}

// devsyncTarget is the file a request names here.
func devsyncTarget(w http.ResponseWriter, r *http.Request, cat string) (rootDir, dst string, ok bool) {
	q := r.URL.Query()
	rootDir, found := devsyncRootMap(cat)[q.Get("root")]
	if !found {
		http.Error(w, "no such folder here", http.StatusNotFound)
		return "", "", false
	}
	dst, err := devsyncLocalPath(rootDir, q.Get("path"))
	if err == nil && cat == dsCatROMs && (!strings.Contains(q.Get("path"), "/") || devsyncRunnable(q.Get("path"))) {
		// Games only travel inside a console folder, and never as something
		// that runs (devsyncBuildManifest).
		err = errSsyncBadName
	}
	if err != nil {
		http.Error(w, firstLine(err.Error()), http.StatusBadRequest)
		return "", "", false
	}
	return rootDir, dst, true
}

// devsyncHandlePart says how much of a file is here already.
func devsyncHandlePart(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	cat, ok := devsyncFileCat(w, r)
	if !ok {
		return
	}
	rootDir, dst, ok := devsyncTarget(w, r, cat)
	if !ok {
		return
	}
	q := r.URL.Query()
	size, err := strconv.ParseInt(q.Get("size"), 10, 64)
	mtime, _ := strconv.ParseInt(q.Get("mtime"), 10, 64)
	if err != nil || size <= 0 || size > devsyncMaxFile(cat) {
		http.Error(w, "bad size", http.StatusBadRequest)
		return
	}
	ans := devsyncPartAnswer{Exists: devsyncTaken(dst)}
	if info, err := os.Lstat(devsyncPartFile(rootDir, q.Get("path"), size, mtime)); err == nil && info.Mode().IsRegular() {
		ans.Size = info.Size()
	}
	peerWriteJSONResponse(w, http.StatusOK, ans)
}

// devsyncHandleFile sends (GET, with ranges) or receives (PUT) one file.
func devsyncHandleFile(w http.ResponseWriter, r *http.Request, call peerCall) {
	cat, ok := devsyncFileCat(w, r)
	if !ok {
		return
	}
	rootDir, dst, ok := devsyncTarget(w, r, cat)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		// The file itself only: a link in the folder may lead off it (the
		// list never names one either).
		if li, err := os.Lstat(dst); err != nil || !li.Mode().IsRegular() {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		f, err := os.Open(dst)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > devsyncMaxFile(cat) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "", info.ModTime(), f)
	case http.MethodPut:
		devsyncReceive(w, r, call, cat, rootDir, dst)
	default:
		http.Error(w, "GET or PUT", http.StatusMethodNotAllowed)
	}
}

// devsyncReceive stores a file another device sends, only where nothing is
// yet: an existing file is never replaced. A dropped transfer keeps its part
// for the next try.
func devsyncReceive(w http.ResponseWriter, r *http.Request, call peerCall, cat, rootDir, dst string) {
	q := r.URL.Query()
	if devsyncTaken(dst) {
		peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true, "exists": true})
		return
	}
	size, err1 := strconv.ParseInt(q.Get("size"), 10, 64)
	offset, err2 := strconv.ParseInt(q.Get("offset"), 10, 64)
	mtime, _ := strconv.ParseInt(q.Get("mtime"), 10, 64)
	if err1 != nil || err2 != nil || size <= 0 || size > devsyncMaxFile(cat) || offset < 0 || offset >= size || r.ContentLength != size-offset {
		http.Error(w, "bad size", http.StatusBadRequest)
		return
	}
	if free, err := freeBytes(rootDir); err == nil && free < size-offset+devsyncSpare {
		http.Error(w, "not enough space on this card", http.StatusInsufficientStorage)
		return
	}
	rel := q.Get("path")
	if !devsyncClaimPart(rootDir, rel) {
		http.Error(w, errDevsyncPartBusy.Error(), http.StatusConflict)
		return
	}
	defer devsyncReleasePart(rootDir, rel)
	part := devsyncPartFile(rootDir, rel, size, mtime)
	var have int64
	if info, err := os.Lstat(part); err == nil && info.Mode().IsRegular() {
		have = info.Size()
	}
	flags := os.O_WRONLY | os.O_CREATE
	switch {
	case offset == 0:
		flags |= os.O_TRUNC
		devsyncDropStaleParts(part)
	case offset == have:
		flags |= os.O_APPEND
	default:
		http.Error(w, "the part here changed — start again", http.StatusConflict)
		return
	}
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		http.Error(w, firstLine(err.Error()), http.StatusInternalServerError)
		return
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		http.Error(w, firstLine(err.Error()), http.StatusInternalServerError)
		return
	}
	body := http.MaxBytesReader(w, r.Body, size-offset)
	n, copyErr := io.Copy(f, body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || offset+n != size {
		http.Error(w, "the copy was cut short — the next sync goes on from here", http.StatusBadRequest)
		return
	}
	if err := devsyncFinish(part, dst, size, mtime); err != nil {
		http.Error(w, firstLine(err.Error()), http.StatusInternalServerError)
		return
	}
	devsyncNoteIncoming(call.rec.Name, cat)
	peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// devsyncIncoming says once per few minutes that files are arriving.
var devsyncIncoming struct {
	mu   sync.Mutex
	last time.Time
}

func devsyncNoteIncoming(from, cat string) {
	devsyncIncoming.mu.Lock()
	first := time.Since(devsyncIncoming.last) > 5*time.Minute
	devsyncIncoming.last = time.Now()
	devsyncIncoming.mu.Unlock()
	if !first {
		return
	}
	what := "BIOS files"
	if cat == dsCatROMs {
		what = "games"
	}
	tbxPost(func() { ShowToast("Receiving "+what+" from "+from+"…", ToastKindInfo) })
}
