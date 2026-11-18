package main

// artwork_choose.go — fixing one game's cover by hand (GAME-7).
//
// A wrong or missing cover could only be fixed by renaming files, and the
// title aliases the matcher reads were built in (artworkAliasTitles,
// romsetup.go), so a new one needed a rebuild. The game options sheet now has
// two rows for it:
//
//   - Choose cover lists the five cover names on the thumbnail server most like
//     the game's (the same scores the automatic match uses, artwork_closest.go,
//     without its bar) and the closest pictures already in the console's Imgs
//     folder. The highlighted one is previewed — a server cover is downloaded
//     for it, an Imgs picture decoded — and A saves it as the game's cover in
//     JukaHub's own game-art folder. The firmware's Imgs files are only read.
//   - Name for covers opens the on-screen keyboard (submit-only: nothing is
//     stored until its Save) and keeps the name in data/artwork-aliases.json.
//     The matcher reads it before the built-in table, and a fetch looks the game
//     up by that name instead of the file's (artworkLookupName). Saving it
//     looks the game up again at once when its cover is JukaHub's own or there
//     is none; a cover the firmware or the user put on the card is left alone.
//
// The alias file is the user's: it is written whole through AtomicWrite (a
// .bak beside it), read with a size cap, and a file that cannot be read is
// never overwritten — the rows say so instead.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// artworkAliasFileName is the user's alias file in JukaHub's data folder.
	artworkAliasFileName = "artwork-aliases.json"
	// artworkAliasVersion is the file's layout; a newer one is not read.
	artworkAliasVersion = 1
	// artworkAliasMaxBytes bounds the read; a real file is a few kilobytes.
	artworkAliasMaxBytes = 1 << 20
	// artworkAliasMaxEntries and artworkAliasMaxLen bound what a hand-edited
	// file can make the matcher carry.
	artworkAliasMaxEntries = 5000
	artworkAliasMaxLen     = 200
	// artChooseServerPicks and artChooseLocalPicks are how many candidates
	// each source offers.
	artChooseServerPicks = 5
	artChooseLocalPicks  = 5
	// artChooseTimeout bounds the list's network work and one re-match.
	artChooseTimeout = 60 * time.Second
)

// artworkAliasVKTarget aims the on-screen keyboard at Name for covers.
const artworkAliasVKTarget vkTargetKind = 223

// artworkAliasBuffer is the name being typed; read only on the keyboard's Save.
var artworkAliasBuffer string

// artworkAliasPath is the alias file, "" when the app has no data folder. A
// variable so a test can point it elsewhere.
var artworkAliasPath = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, artworkAliasFileName)
}

// artworkAliasFile is the file's layout. Keys are the game's file name (any
// casing, tags allowed: they are normalised on load), values the name its
// covers are matched by.
type artworkAliasFile struct {
	Version int               `json:"version"`
	Aliases map[string]string `json:"aliases"`
}

// artworkUserAliases is the loaded file, shared by the fetch workers and the
// main thread.
var artworkUserAliases struct {
	sync.Mutex
	loaded bool
	// broken is set when a file is there but could not be read: it is the
	// user's, so it is not overwritten.
	broken bool
	byKey  map[string]string
}

// artworkAliasSaveMu orders the file writes; each writes the newest map.
var artworkAliasSaveMu sync.Mutex

// artworkAliasClean folds an alias's runs of spaces and line breaks into
// single spaces and refuses one that is empty, too long or carries other
// control characters.
func artworkAliasClean(name string) (string, bool) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || len(name) > artworkAliasMaxLen {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return "", false
		}
	}
	return name, true
}

// parseArtworkAliases reads the alias file. Keys are normalised the way the
// built-in table's are (artworkNormalizeTitle); entries that are empty, too
// long or hold control characters are dropped, and at most
// artworkAliasMaxEntries are kept. A missing version is version 1 (a file
// written by hand); a newer one is refused.
func parseArtworkAliases(data []byte) (map[string]string, error) {
	var f artworkAliasFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Version < 0 || f.Version > artworkAliasVersion {
		return nil, fmt.Errorf("layout %d is not one this build reads", f.Version)
	}
	keys := make([]string, 0, len(f.Aliases))
	for key := range f.Aliases {
		keys = append(keys, key)
	}
	sort.Strings(keys) // which entries the cap keeps does not depend on map order
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		k := artworkNormalizeTitle(key)
		v, ok := artworkAliasClean(f.Aliases[key])
		if k == "" || len(k) > artworkAliasMaxLen || !ok {
			continue
		}
		out[k] = v
		if len(out) >= artworkAliasMaxEntries {
			break
		}
	}
	return out, nil
}

// loadArtworkAliases reads the file at path. A missing file is an empty table;
// one that cannot be read is too, with broken set.
func loadArtworkAliases(path string) (aliases map[string]string, broken bool) {
	if path == "" {
		return map[string]string{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logWarn("art", "cannot read %s: %v", path, err)
			return map[string]string{}, true
		}
		return map[string]string{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, artworkAliasMaxBytes+1))
	if err == nil && len(data) > artworkAliasMaxBytes {
		err = fmt.Errorf("larger than %d KiB", artworkAliasMaxBytes>>10)
	}
	if err == nil {
		aliases, err = parseArtworkAliases(data)
	}
	if err != nil {
		logWarn("art", "ignoring %s: %v", path, err)
		return map[string]string{}, true
	}
	return aliases, false
}

// artworkUserAliasesLocked loads the file on first use. Caller holds the lock.
func artworkUserAliasesLocked() map[string]string {
	if !artworkUserAliases.loaded {
		artworkUserAliases.loaded = true
		artworkUserAliases.byKey, artworkUserAliases.broken = loadArtworkAliases(artworkAliasPath())
	}
	return artworkUserAliases.byKey
}

// artworkUserAliasFor is the name the user gave a game's covers, by the
// game's (or any) title. Safe on any goroutine.
func artworkUserAliasFor(name string) (string, bool) {
	key := artworkNormalizeTitle(name)
	if key == "" {
		return "", false
	}
	artworkUserAliases.Lock()
	defer artworkUserAliases.Unlock()
	alias, ok := artworkUserAliasesLocked()[key]
	return alias, ok && alias != ""
}

// artworkLookupName is the name a ROM's covers are looked up by: the user's
// alias, with the file's own tags when the alias has none (they pick the
// release), else the file's stem.
func artworkLookupName(stem string) string {
	alias, ok := artworkUserAliasFor(stem)
	if !ok {
		return stem
	}
	if !strings.ContainsAny(alias, "([") {
		if tags := artworkTagGroupsText(stem); tags != "" {
			return alias + " " + tags
		}
	}
	return alias
}

// artworkTagGroupsText is the bracketed groups of s as written: "(USA) [!]"
// for "Tetris (USA) [!]".
func artworkTagGroupsText(s string) string {
	var groups []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(', '[':
			if depth == 0 {
				start = i
			}
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
				if depth == 0 {
					groups = append(groups, s[start:i+1])
				}
			}
		}
	}
	return strings.Join(groups, " ")
}

// artworkSetUserAlias stores the name a ROM's covers are matched by, or with
// "" (or the file's own title) removes it, and writes the file on a
// goroutine. It reports false, storing nothing, when the file on the card
// could not be read.
func artworkSetUserAlias(stem, alias string) bool {
	key := artworkNormalizeTitle(stem)
	if key == "" {
		return false
	}
	artworkUserAliases.Lock()
	table := artworkUserAliasesLocked()
	if artworkUserAliases.broken {
		artworkUserAliases.Unlock()
		return false
	}
	clean, ok := artworkAliasClean(alias)
	if !ok || artworkNormalizeTitle(clean) == key {
		delete(table, key)
	} else {
		table[key] = clean
	}
	artworkUserAliases.Unlock()
	go artworkSaveUserAliases()
	return true
}

// artworkSaveUserAliases writes the newest table. A table emptied by the user
// is written as such rather than deleted, so the .bak keeps the last one.
func artworkSaveUserAliases() {
	path := artworkAliasPath()
	if path == "" {
		return
	}
	artworkAliasSaveMu.Lock()
	defer artworkAliasSaveMu.Unlock()
	artworkUserAliases.Lock()
	snapshot := make(map[string]string, len(artworkUserAliases.byKey))
	for k, v := range artworkUserAliases.byKey {
		snapshot[k] = v
	}
	artworkUserAliases.Unlock()
	payload, err := json.MarshalIndent(artworkAliasFile{Version: artworkAliasVersion, Aliases: snapshot}, "", "  ")
	if err == nil {
		err = AtomicWrite(path, append(payload, '\n'), 0o644)
	}
	if err != nil {
		logWarn("art", "cannot save %s: %v", path, err)
	}
}

// ── The game behind the sheet ─────────────────────────────────────────────

// romCollectionForSystem is the thumbnail-server collection of a library
// console, "" for one the scanner does not map.
func romCollectionForSystem(system string) string {
	system = strings.TrimSpace(system)
	if system == "" {
		return ""
	}
	for _, s := range romSystemAliases {
		if s.Name == system {
			return s.Collection
		}
	}
	return ""
}

// artChooseROM is the scanned ROM behind a library game, and false for one
// whose cover does not come from the thumbnail server or the Imgs folder
// (Android, HTML5, ports, built-in games, plugin consoles).
func artChooseROM(game libraryGame) (scannedROM, bool) {
	switch game.System {
	case androidLibrarySystem, html5LibrarySystem, portsLibrarySystem:
		return scannedROM{}, false
	}
	if isBuiltInLibraryGame(game) || strings.TrimSpace(game.Path) == "" {
		return scannedROM{}, false
	}
	collection := romCollectionForSystem(game.System)
	if collection == "" || collection == "Plugins" {
		return scannedROM{}, false
	}
	return scannedROM{Path: game.Path, System: game.System, Collection: collection}, true
}

// libraryCoverChanged shows a cover saved for rom: the library's entry points
// at the cover now in effect, and the texture cache (keyed by path, and the
// file under the path changed) starts over. Main thread.
func libraryCoverChanged(rom scannedROM) {
	art := romCoverPath(rom, libraryArtworkRoot())
	for i := range gameLib.lib.Systems {
		games := gameLib.lib.Systems[i].Games
		for j := range games {
			if games[j].Path == rom.Path {
				games[j].Art = art
			}
		}
	}
	if gameOptsUI.game.Path == rom.Path {
		gameOptsUI.game.Art = art
	}
	closeGameLibraryArt()
	recountLibraryArtwork()
}

// ── Choose cover ──────────────────────────────────────────────────────────

// artChooseCandidate is one cover on the list.
type artChooseCandidate struct {
	name  string // the server's cover name, or the Imgs file's name
	path  string // the Imgs file; "" for a server cover
	local bool
	data  []byte // a server cover once downloaded for its preview
}

var artChoose struct {
	open    bool
	loading bool
	saving  bool
	gen     uint64 // bumped per opening; a result from another is dropped
	game    libraryGame
	rom     scannedROM
	cands   []artChooseCandidate
	note    string // why the list is short
	cursor  int
	scroll  int
	visible int
	rowHits []gameLibRowHit
	panel   sdl.Rect

	// The preview: previewFor is the candidate the texture (or the failure)
	// shows; previewPic waits for the render thread to upload it.
	previewFor    int
	previewTex    *sdl.Texture
	previewPic    *image.NRGBA
	previewFailed bool
	previewBusy   int // the candidate a decode runs for, -1 none
	previewCancel context.CancelFunc
}

// artChooseIsOpen reports the chooser over the game options sheet.
func artChooseIsOpen() bool { return artChoose.open }

// artChooseBusy is a re-match started by Name for covers.
var artChooseBusy atomic.Bool

// openArtChoose opens the chooser for the sheet's game and starts the list.
func openArtChoose() {
	rom, ok := artChooseROM(gameOptsUI.game)
	if !ok {
		ShowToast("This game's picture comes from its app or port, not from box art", ToastKindInfo)
		return
	}
	if strings.TrimSpace(libraryArtworkRoot()) == "" {
		ShowToast("The artwork folder is unavailable.", ToastKindWarning)
		return
	}
	artChooseClose(false)
	artChoose.open = true
	artChoose.loading = true
	artChoose.gen++
	artChoose.game = gameOptsUI.game
	artChoose.rom = rom
	artChoose.previewFor = -1
	artChoose.previewBusy = -1
	gen := artChoose.gen
	stem := romGameStem(rom.Path)
	PlayActivateSound()
	go func() {
		cands, note := artChooseGather(rom, stem)
		tbxPost(func() {
			if !artChoose.open || artChoose.gen != gen {
				return
			}
			artChoose.loading = false
			artChoose.cands = cands
			artChoose.note = note
		})
	}()
}

// artChooseClose closes the chooser and frees its preview.
func artChooseClose(sound bool) {
	if artChoose.previewCancel != nil {
		artChoose.previewCancel()
		artChoose.previewCancel = nil
	}
	if artChoose.previewTex != nil {
		artChoose.previewTex.Destroy()
		artChoose.previewTex = nil
	}
	wasOpen := artChoose.open
	artChoose.open = false
	artChoose.loading = false
	artChoose.cands = nil
	artChoose.note = ""
	artChoose.cursor, artChoose.scroll, artChoose.visible = 0, 0, 0
	artChoose.rowHits = artChoose.rowHits[:0]
	artChoose.previewPic = nil
	artChoose.previewFor, artChoose.previewBusy = -1, -1
	artChoose.previewFailed = false
	artChoose.gen++
	if sound && wasOpen {
		PlayBackSound()
	}
}

// artChooseGather builds the list off the main thread: the server's closest
// names first, then the Imgs folder's pictures. note says why a source gave
// nothing.
func artChooseGather(rom scannedROM, stem string) ([]artChooseCandidate, string) {
	lookup := artworkLookupName(stem)
	var out []artChooseCandidate
	var notes []string

	ctx, cancel := context.WithTimeout(context.Background(), artChooseTimeout)
	defer cancel()
	idx, err := artworkIndexFor(ctx, artworkHTTPClient(), strings.TrimRight(artworkBaseURL, "/"), rom.Collection)
	switch {
	case err != nil:
		notes = append(notes, "the thumbnail server could not be reached")
	case idx == nil:
		notes = append(notes, "the thumbnail server has no list for this console")
	default:
		names := idx.closestN(lookup, artChooseServerPicks)
		if len(names) == 0 {
			notes = append(notes, "no server cover is named like this game")
		}
		for _, name := range names {
			out = append(out, artChooseCandidate{name: name})
		}
	}

	local := artChooseLocalPictures(rom, stem, lookup)
	out = append(out, local...)
	if len(local) == 0 {
		notes = append(notes, "no picture in the Imgs folder is named like it")
	}
	note := ""
	if len(notes) > 0 {
		note = strings.Join(notes, "; ")
		note = strings.ToUpper(note[:1]) + note[1:] + "."
	}
	return out, note
}

// artChooseLocalPictures lists the pictures in the console's Imgs folder (the
// game folder's own, then the console folder's) most like the game's name:
// the one named exactly like the file first, then up to artChooseLocalPicks
// that share a word with it. PNG and JPEG only: they are what a cover can be
// saved from.
func artChooseLocalPictures(rom scannedROM, stem, lookup string) []artChooseCandidate {
	dir := filepath.Dir(rom.Path)
	dirs := []string{filepath.Join(dir, "Imgs")}
	if parent := filepath.Dir(dir); parent != dir {
		dirs = append(dirs, filepath.Join(parent, "Imgs"))
	}
	rom1 := artworkClosestWords(artworkMatchTokens(lookup))
	type scored struct {
		cand  artChooseCandidate
		score float64
		exact bool
	}
	var found []scored
	seen := map[string]bool{}
	for _, d := range dirs {
		for lower, onDisk := range artworkDirNames(d) {
			ext := filepath.Ext(lower)
			if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
				continue
			}
			path := filepath.Join(d, onDisk)
			if seen[path] {
				continue
			}
			seen[path] = true
			name := strings.TrimSuffix(onDisk, filepath.Ext(onDisk))
			exact := strings.EqualFold(name, stem)
			score, _ := artworkClosestScore(rom1, artworkClosestWords(artworkMatchTokens(name)))
			if !exact && score <= 0 {
				continue
			}
			found = append(found, scored{artChooseCandidate{name: onDisk, path: path, local: true}, score, exact})
		}
	}
	sort.Slice(found, func(a, b int) bool {
		if found[a].exact != found[b].exact {
			return found[a].exact
		}
		if found[a].score != found[b].score {
			return found[a].score > found[b].score
		}
		return found[a].cand.path < found[b].cand.path
	})
	out := make([]artChooseCandidate, 0, artChooseLocalPicks)
	for _, f := range found {
		if len(out) >= artChooseLocalPicks {
			break
		}
		out = append(out, f.cand)
	}
	return out
}

// artChooseDecodeBytes decodes a downloaded cover for the preview, capped
// like the library's (decodeLibraryArt).
func artChooseDecodeBytes(ctx context.Context, data []byte) (*image.NRGBA, error) {
	rgba, err := decodeImageRGBA(ctx, data)
	if err != nil {
		return nil, err
	}
	w, h := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	if edge := max(w, h); edge > libraryArtDecodeMaxEdge {
		scale := float64(libraryArtDecodeMaxEdge) / float64(edge)
		w = max(1, int(math.Round(float64(w)*scale)))
		h = max(1, int(math.Round(float64(h)*scale)))
	}
	return imgvScale(rgba, w, h), nil
}

// artChooseRequestPreview starts the highlighted candidate's preview when it
// has none and none is running. Main thread; cheap per frame.
func artChooseRequestPreview() {
	i := artChoose.cursor
	if artChoose.loading || i < 0 || i >= len(artChoose.cands) || artChoose.previewFor == i || artChoose.previewBusy == i {
		return
	}
	if artChoose.previewCancel != nil {
		artChoose.previewCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), artChooseTimeout)
	artChoose.previewCancel = cancel
	artChoose.previewBusy = i
	gen := artChoose.gen
	cand := artChoose.cands[i]
	collection := artChoose.rom.Collection
	go func() {
		defer cancel()
		var pic *image.NRGBA
		var data []byte
		var err error
		if cand.local {
			pic, err = decodeLibraryArt(cand.path, ctx)
		} else {
			data = cand.data
			if data == nil {
				data, err = artworkDownloadCover(ctx, artworkHTTPClient(), strings.TrimRight(artworkBaseURL, "/"), collection, cand.name)
			}
			if err == nil {
				pic, err = artChooseDecodeBytes(ctx, data)
			}
		}
		if errors.Is(err, context.Canceled) {
			return // the highlight moved on; its own preview is on the way
		}
		tbxPost(func() {
			if !artChoose.open || artChoose.gen != gen || i >= len(artChoose.cands) {
				return
			}
			if data != nil && !artChoose.cands[i].local {
				artChoose.cands[i].data = data
			}
			if artChoose.previewBusy == i {
				artChoose.previewBusy = -1
			}
			if artChoose.cursor != i {
				return
			}
			artChoose.previewFor = i
			artChoose.previewPic = pic
			artChoose.previewFailed = err != nil || pic == nil
			if err != nil {
				logDebug("art", "cover preview %q: %v", cand.name, err)
			}
		})
	}()
}

// artChooseCoverBytes is what saving a candidate writes: the server's PNG as
// it came, or an Imgs picture — a small PNG copied, a larger one or a JPEG
// scaled down and saved as PNG, since the game-art folder's covers are .png
// files.
func artChooseCoverBytes(ctx context.Context, cand artChooseCandidate, collection string) ([]byte, error) {
	if !cand.local {
		if cand.data != nil {
			return cand.data, nil
		}
		return artworkDownloadCover(ctx, artworkHTTPClient(), strings.TrimRight(artworkBaseURL, "/"), collection, cand.name)
	}
	info, err := os.Stat(cand.path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > artworkImageMaxBytes {
		return nil, fmt.Errorf("%s is not a picture of usable size", filepath.Base(cand.path))
	}
	data, err := os.ReadFile(cand.path)
	if err != nil {
		return nil, err
	}
	switch {
	case artworkIsPNG(data):
		if cfg, err := png.DecodeConfig(bytes.NewReader(data)); err == nil && max(cfg.Width, cfg.Height) <= libraryArtDecodeMaxEdge {
			return data, nil // already no larger than the library draws it
		}
		return artChooseShrinkToPNG(ctx, data)
	case artworkIsJPEG(data):
		return artChooseShrinkToPNG(ctx, data)
	}
	return nil, fmt.Errorf("%s is neither PNG nor JPEG", filepath.Base(cand.path))
}

// artChooseShrinkToPNG is an Imgs picture as the PNG the game-art folder
// keeps, at most libraryArtDecodeMaxEdge on its longer side (the size the
// library decodes covers at): decoded under the decode permit and scaled
// before the encode, so a 16 MP photo becomes a ~512 px PNG instead of a
// 20-40 MB one. Runs on the save goroutine, never the render thread.
func artChooseShrinkToPNG(ctx context.Context, data []byte) ([]byte, error) {
	pic, err := artChooseDecodeBytes(ctx, data)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, pic); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// artChooseApply saves the highlighted candidate as the game's cover.
func artChooseApply() {
	i := artChoose.cursor
	if artChoose.loading || artChoose.saving || i < 0 || i >= len(artChoose.cands) {
		return
	}
	rom := artChoose.rom
	dest := romArtworkPath(rom, libraryArtworkRoot())
	if dest == "" {
		ShowToast("The artwork folder is unavailable.", ToastKindWarning)
		return
	}
	cand := artChoose.cands[i]
	gen := artChoose.gen
	label := artChoose.game.Label()
	artChoose.saving = true
	PlayActivateSound()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), artChooseTimeout)
		defer cancel()
		data, err := artChooseCoverBytes(ctx, cand, rom.Collection)
		if err == nil {
			err = gmetaWriteCache(dest, data)
		}
		tbxPost(func() {
			// Cleared whatever happened to the chooser meanwhile: B or closing
			// the options sheet bumps gen while this runs, and a flag left set
			// would show "Saving the cover…" and refuse every later save. Only
			// one save runs at a time (artChooseApply checks the flag).
			artChoose.saving = false
			if err != nil {
				logWarn("art", "cannot save the chosen cover for %s: %v", rom.Path, err)
				if !cand.local && errors.Is(err, os.ErrNotExist) {
					ShowToast("The server no longer has this cover", ToastKindWarning)
					return
				}
				ShowToast("The cover could not be saved: "+err.Error(), ToastKindWarning)
				return
			}
			logInfo("art", "%s: cover chosen by hand (%s)", rom.Path, cand.name)
			libraryCoverChanged(rom)
			if artChoose.gen == gen {
				artChooseClose(false)
			}
			if nextuiActive() && romLocalCoverPath(rom) != "" {
				// romCoverPath: on NextUI the card's own cover comes first.
				ShowToast("Cover saved · NextUI shows the card's own cover for "+label+" first", ToastKindInfo)
				return
			}
			ShowToast("Cover saved for "+label, ToastKindSuccess)
		})
	}()
}

// ── Name for covers ───────────────────────────────────────────────────────

// artworkAliasEdit is the game the keyboard names, kept from its opening.
var artworkAliasEdit scannedROM

// artworkAliasRowValue is the Name for covers row's value.
func artworkAliasRowValue(game libraryGame) string {
	if _, ok := artChooseROM(game); !ok {
		return "—"
	}
	if alias, ok := artworkUserAliasFor(romGameStem(game.Path)); ok {
		return alias
	}
	return "File name"
}

// vkOpenArtworkAlias opens the keyboard on the name the sheet's game's covers
// are matched by: the alias when there is one, else the file's title.
func vkOpenArtworkAlias() {
	if !gameOptsUI.open {
		return
	}
	rom, ok := artChooseROM(gameOptsUI.game)
	if !ok {
		ShowToast("This game's picture comes from its app or port, not from box art", ToastKindInfo)
		return
	}
	artworkUserAliases.Lock()
	artworkUserAliasesLocked()
	broken := artworkUserAliases.broken
	artworkUserAliases.Unlock()
	if broken {
		ShowToast(artworkAliasFileName+" could not be read; fix or delete it first", ToastKindWarning)
		return
	}
	artworkAliasEdit = rom
	stem := romGameStem(rom.Path)
	initial, ok := artworkUserAliasFor(stem)
	if !ok {
		initial = stripArtworkTagGroups(stem)
	}
	chatInputActive = false
	unitInputActive = false
	vkTarget = artworkAliasVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	artworkAliasBuffer = initial
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
}

// artworkAliasSubmit stores the typed name (the keyboard's Save) and looks
// the game's cover up again when that can change what it shows.
func artworkAliasSubmit(text string) {
	rom := artworkAliasEdit
	artworkAliasEdit = scannedROM{}
	stem := romGameStem(rom.Path)
	if stem == "" {
		return
	}
	if !artworkSetUserAlias(stem, text) {
		ShowToast(artworkAliasFileName+" could not be read; fix or delete it first", ToastKindWarning)
		return
	}
	alias, named := artworkUserAliasFor(stem)
	if !named {
		ShowToast("Covers are matched by the file name again", ToastKindInfo)
		return
	}
	// A cover the firmware or the user put on the card is theirs: the fetcher
	// never replaces it, and neither does this.
	if romLocalCoverPath(rom) != "" && !artworkCoverPresent(romArtworkPath(rom, libraryArtworkRoot())) {
		ShowToast("Saved · this game shows the card's own cover, so nothing is looked up", ToastKindInfo)
		return
	}
	if artworkFetchRunning() || !artChooseBusy.CompareAndSwap(false, true) {
		ShowToast("Saved · the next cover fetch looks for "+alias, ToastKindInfo)
		return
	}
	ShowToast("Saved · looking for a cover named "+alias+"…", ToastKindInfo)
	go artworkRematch(rom, alias)
}

// artworkRematch looks one game up again under its new name and replaces its
// JukaHub cover only when one is found.
func artworkRematch(rom scannedROM, alias string) {
	defer artChooseBusy.Store(false)
	dest := romArtworkPath(rom, libraryArtworkRoot())
	if dest == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), artChooseTimeout)
	defer cancel()
	data, err := fetchROMArtworkFor(ctx, nil, artworkBaseURL, rom, romGameStem(rom.Path))
	if err == nil {
		err = gmetaWriteCache(dest, data)
	}
	tbxPost(func() {
		switch {
		case err == nil:
			libraryCoverChanged(rom)
			ShowToast("Cover found for "+alias, ToastKindSuccess)
		case errors.Is(err, os.ErrNotExist):
			ShowToast("No cover named like "+alias+" · Choose cover lists the closest", ToastKindInfo)
		default:
			logWarn("art", "re-match %s as %q: %v", rom.Path, alias, err)
			ShowToast("The thumbnail server could not be reached · the next fetch tries again", ToastKindWarning)
		}
	})
}

// ── The chooser's input and drawing ───────────────────────────────────────

func artChooseMove(delta int) {
	n := len(artChoose.cands)
	if n == 0 || delta == 0 {
		return
	}
	artChoose.cursor += delta
	if artChoose.cursor < 0 {
		artChoose.cursor = 0
	}
	if artChoose.cursor >= n {
		artChoose.cursor = n - 1
	}
	PlayNavSound()
}

func handleArtChooseKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		artChooseMove(-1)
	case sdl.K_DOWN:
		artChooseMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		artChooseApply()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		artChooseClose(true)
	}
	return true
}

func handleArtChooseController(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		artChooseMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		artChooseMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		artChooseApply()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		artChooseClose(true)
	default:
		return false
	}
	return true
}

// handleArtChooseMouse: a tap highlights a row (and shows its picture), a tap
// on the highlighted row saves it, a tap off the card goes back.
func handleArtChooseMouse(mx, my int32) bool {
	for _, hit := range artChoose.rowHits {
		if pointInRect(mx, my, hit.rect) {
			if hit.index == artChoose.cursor {
				artChooseApply()
			} else {
				artChoose.cursor = hit.index
				PlayNavSound()
			}
			return true
		}
	}
	if artChoose.panel.W > 0 && pointInRect(mx, my, artChoose.panel) {
		return true
	}
	artChooseClose(true)
	return true
}

// renderArtChooseSheet draws the chooser over the game options.
func renderArtChooseSheet(renderer *sdl.Renderer, config *Config) {
	if !artChoose.open || renderer == nil || config == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	if artChoose.previewPic != nil {
		if artChoose.previewTex != nil {
			artChoose.previewTex.Destroy()
		}
		artChoose.previewTex = imgvTexture(renderer, artChoose.previewPic)
		artChoose.previewPic = nil
	}
	artChooseRequestPreview()

	smallH := int32(font.Height())
	medH := int32(medium.Height())
	const pad = OverlayPadX
	rowH := max32(54, medH+smallH+20)
	header := UIPanelHeader("Choose cover", artChoose.game.Label()+" · "+artChoose.game.System, "gamepad")
	headerH := UIPanelHeaderBand(config, header, 14)
	noteH := int32(12) + smallH
	footerH := int32(14) + smallH
	panelW := min(int32(860), screenWidth-48)
	panelH := screenHeight - 48
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	artChoose.panel = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	bodyY := panelY + headerH
	bodyH := panelH - headerH - noteH - footerH
	previewW := min(bodyH, (panelW-2*pad)*2/5)
	listW := panelW - 2*pad - previewW - 16
	focusColor := installHighlight()

	n := len(artChoose.cands)
	artChoose.rowHits = artChoose.rowHits[:0]
	switch {
	case artChoose.loading:
		renderText(renderer, config, medium, "Looking for covers…", ColorTextSecondary(), panelX+pad, bodyY+8)
	case n == 0:
		renderText(renderer, config, medium, "No covers to choose from", ColorTextPrimary(), panelX+pad, bodyY+8)
	default:
		visible := max(1, int(bodyH/rowH))
		visible = min(visible, n)
		if artChoose.cursor >= n {
			artChoose.cursor = n - 1
		}
		if artChoose.cursor < artChoose.scroll {
			artChoose.scroll = artChoose.cursor
		}
		if artChoose.cursor >= artChoose.scroll+visible {
			artChoose.scroll = artChoose.cursor - visible + 1
		}
		artChoose.scroll = max(0, min(artChoose.scroll, n-visible))
		artChoose.visible = visible
		for v := 0; v < visible; v++ {
			i := artChoose.scroll + v
			cand := artChoose.cands[i]
			rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(v)*rowH, W: listW, H: rowH - 8}
			artChoose.rowHits = append(artChoose.rowHits, gameLibRowHit{index: i, rect: rect})
			selected := i == artChoose.cursor
			bg := WithAlpha(ColorSurfaceRow, 165)
			if selected {
				bg = WithAlpha(focusColor, 46)
			}
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
			if selected {
				strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
			}
			source := "Thumbnail server"
			if cand.local {
				source = "Imgs folder on the card"
			}
			renderText(renderer, config, medium, ellipsize(medium, cand.name, rect.W-28), ColorTextPrimary(), rect.X+14, rect.Y+6)
			renderText(renderer, config, font, source, ColorTextTertiary(), rect.X+14, rect.Y+6+medH+2)
		}
	}

	// The preview of the highlighted cover.
	box := sdl.Rect{X: panelX + panelW - pad - previewW, Y: bodyY, W: previewW, H: previewW}
	fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 12, WithAlpha(ColorSurfaceRow, 160))
	strokeRoundedRect(renderer, box.X, box.Y, box.W, box.H, 12, 1, ColorBorderDefault)
	showing := artChoose.previewFor == artChoose.cursor && n > 0
	switch {
	case showing && artChoose.previewTex != nil && !artChoose.previewFailed:
		if _, _, tw, th, err := artChoose.previewTex.Query(); err == nil && tw > 0 && th > 0 {
			dst := gaFit(tw, th, box.W-16, box.H-16)
			dst.X += box.X + 8
			dst.Y += box.Y + 8
			_ = renderer.Copy(artChoose.previewTex, nil, &dst)
		}
	case n > 0:
		msg := "Loading the picture…"
		if showing && artChoose.previewFailed {
			msg = "No picture to show"
		}
		w, _, _ := font.SizeUTF8(msg)
		renderText(renderer, config, font, msg, ColorTextTertiary(), box.X+(box.W-int32(w))/2, box.Y+(box.H-smallH)/2)
	}

	note := "A saves it as this game's cover in JukaHub's game-art folder; the card's own files are only read."
	if artChoose.saving {
		note = "Saving the cover…"
	} else if artChoose.note != "" && !artChoose.loading {
		note = artChoose.note
	}
	renderText(renderer, config, font, ellipsize(font, note, panelW-2*pad), ColorTextTertiary(), panelX+pad, bodyY+bodyH+6)
	hint := hintLine(FooterHint{"D-Pad", "Cover"}, FooterHint{"A", "Use this cover"}, FooterHint{"B", "Back"})
	renderText(renderer, config, font, ellipsize(font, hint, panelW-2*pad), ColorTextSecondary(), panelX+pad, panelY+panelH-footerH+2)
}
