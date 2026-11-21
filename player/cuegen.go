package main

// cuegen.go — the CUE generator: a .cue sheet for every CD image that has
// none, so the emulator can start it.
//
// JukaHub's disc helpers
// (disctools.go) already gave a lone .bin a one-track sheet, with the track
// mode taken from the console's folder. Two cases were left over, and this
// file does them:
//
//   - The mode is read from the image itself: a raw CD data sector starts
//     with the sync pattern 00 FF×10 00 and carries its mode in byte 15
//     (1 → MODE1/2352, 2 → MODE2/2352). A Sega CD game filed under PS, or a
//     PlayStation game under SEGACD, gets the right sheet, and a .bin with
//     no sync — not a raw data track, so a one-track data sheet would be
//     wrong — gets none. A cooked image (2048-byte sectors, no sync) becomes
//     MODE1/2048 only in a Sega or NEC CD folder, whose cores read one; a
//     PlayStation disc needs its raw sectors. (An .iso needs no sheet.)
//   - Split rips: "Game (Track 1).bin", "Game (Track 2).bin" … (Redump's
//     layout) become one "Game.cue" with a FILE per track, in track order
//     (Track 10 after Track 9). Each track's mode is read the same way, from
//     the sector after the longest pregap — no sync is AUDIO, which is how a
//     PC Engine CD's first track is found. The
//     gaps follow Redump's sheets: track 1 starts at once; a data track after
//     an audio one has a 3-second pregap in its file (INDEX 00 00:00:00,
//     INDEX 01 00:03:00); an audio track after a data one 2 seconds; an
//     audio track after another audio one 2 seconds when its first two
//     seconds are silence (a PlayStation disc), none when the music starts
//     at once (most PC Engine CDs). A set with a missing track, a file that is
//     not whole 2352-byte sectors, or a name with a double quote (which a
//     cue cannot quote) is left alone.
//
// Like the rest of the disc helpers it only adds: a file a sheet already
// names is never given a second one, an existing .cue is never replaced,
// and a sheet is written to a hidden temporary file first and renamed into
// place, so a power cut never leaves half a sheet. Lines end in LF, as the
// helpers' other sheets do; every libretro CD core reads both.
//
// Where it runs: inside the disc helpers (writeDiscHelpers and the disc
// folders of writeDiscFolderPlaylist), so the library's automatic pass on a
// scan, "Combine multi-disc games" and the Tools hub's Disc helpers make
// these sheets before the .m3u step looks for discs; and on its own, sheets
// only, from the ROM manager's "Make missing .cue files" and the Tools hub's
// "CUE generator" tile (cueGenNow), which say "Made N .cue files". The disc
// consoles are the helpers' (PlayStation, Saturn, Sega CD, PC Engine CD) plus
// Neo Geo CD, added here.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	cueGenSectorRaw    = 2352
	cueGenSectorCooked = 2048
	// cueGenAudioPregap and cueGenDataPregap are Redump's gaps in sectors
	// (75 a second): 2 s before an audio track, 3 s before a data track that
	// follows audio.
	cueGenAudioPregap = 150
	cueGenDataPregap  = 225

	cueGenModeAudio = "AUDIO"
)

// cueGenSync is the 12-byte sync pattern every raw CD data sector starts with.
var cueGenSync = []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}

// cueGenTrackName matches a split rip's track marker at the end of a name:
// "Game (USA) (Track 02)" → "Game (USA)", 2.
var cueGenTrackName = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)^(.*?)\s*[\(\[]\s*track\s*0*([0-9]{1,2})\s*[\)\]]\s*$`)
})

// ── Reading an image ─────────────────────────────────────────────────────

// cueGenSectorMode is the mode a raw sector's header names: "MODE1/2352",
// "MODE2/2352", or "" when it does not start with the sync pattern (audio,
// a cooked image, not a disc). Pure.
func cueGenSectorMode(head []byte) string {
	if len(head) < 16 || !bytes.Equal(head[:12], cueGenSync) {
		return ""
	}
	switch head[15] {
	case 1:
		return "MODE1/2352"
	case 2:
		return "MODE2/2352"
	}
	return ""
}

// cueGenSingleMode is the mode of a lone .bin's one track from its first
// bytes, its size and the console folder's usual mode; ok is false when no
// one-track sheet fits it. Pure.
func cueGenSingleMode(head []byte, size int64, folderMode string) (string, bool) {
	if mode := cueGenSectorMode(head); mode != "" {
		return mode, true
	}
	if size > 0 && size%cueGenSectorCooked == 0 && size%cueGenSectorRaw != 0 && strings.HasPrefix(folderMode, "MODE1") {
		return "MODE1/2048", true
	}
	return "", false
}

// cueGenReadHead reads up to n bytes from the start of path.
func cueGenReadHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:got], nil
}

// cueGenSilent reports whether data is all zero bytes (silence). Pure.
func cueGenSilent(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

// cueGenSingleText is the one-track sheet for the lone .bin at path, ok
// false when none fits (disctools.go writes it).
func cueGenSingleText(path, folderMode string) (string, bool) {
	name := filepath.Base(path)
	if strings.Contains(name, `"`) {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	head, err := cueGenReadHead(path, 16)
	if err != nil {
		return "", false
	}
	mode, ok := cueGenSingleMode(head, info.Size(), folderMode)
	if !ok {
		logInfo("library", "cue generator: %s has no CD data sync; no one-track .cue for it", name)
		return "", false
	}
	return cueSheetText(name, mode), true
}

// ── Split rips ───────────────────────────────────────────────────────────

// cueGenTrack is one file of a split rip.
type cueGenTrack struct {
	Number int
	Name   string // the file's name in the folder
	Mode   string // "MODE1/2352", "MODE2/2352" or "AUDIO"
	Pregap int    // sectors before INDEX 01 inside the file
}

// cueGenSplitTrack separates "Game (USA) (Track 02)" into "Game (USA)" and 2.
// Pure.
func cueGenSplitTrack(stem string) (string, int, bool) {
	m := cueGenTrackName().FindStringSubmatch(stem)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	base := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(m[1]), "-"))
	if err != nil || n <= 0 || base == "" {
		return "", 0, false
	}
	return base, n, true
}

// cueGenTrackSet is one split rip in a folder.
type cueGenTrackSet struct {
	Base  string
	Files []string // in track order: Files[0] is track 1
}

// cueGenTrackSets groups a folder's .bin names into split rips, each with
// its tracks in numeric order. A set whose tracks are not 1…N without a gap
// or a twin is dropped: its sheet would name the wrong tracks. Pure.
func cueGenTrackSets(names []string) []cueGenTrackSet {
	type entry struct {
		base   string
		tracks map[int]string
		bad    bool
	}
	sets := map[string]*entry{}
	for _, name := range names {
		if !strings.EqualFold(filepath.Ext(name), ".bin") {
			continue
		}
		base, n, ok := cueGenSplitTrack(strings.TrimSuffix(name, filepath.Ext(name)))
		if !ok {
			continue
		}
		key := strings.ToLower(base)
		e := sets[key]
		if e == nil {
			e = &entry{base: base, tracks: map[int]string{}}
			sets[key] = e
		}
		if _, twin := e.tracks[n]; twin {
			e.bad = true
		}
		e.tracks[n] = name
	}
	keys := make([]string, 0, len(sets))
	for key := range sets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []cueGenTrackSet
	for _, key := range keys {
		e := sets[key]
		if e.bad {
			continue
		}
		files := make([]string, 0, len(e.tracks))
		for n := 1; n <= len(e.tracks); n++ {
			name, ok := e.tracks[n]
			if !ok {
				files = nil
				break
			}
			files = append(files, name)
		}
		if len(files) > 0 {
			out = append(out, cueGenTrackSet{Base: e.base, Files: files})
		}
	}
	return out
}

// cueGenPregap is a track's pregap from its own mode and the one before it,
// silent telling whether its first two seconds are silence. Pure.
func cueGenPregap(number int, mode, prevMode string, silent bool) int {
	switch {
	case number <= 1:
		return 0
	case mode != cueGenModeAudio && prevMode == cueGenModeAudio:
		return cueGenDataPregap
	case mode != cueGenModeAudio:
		if mode != prevMode {
			return cueGenAudioPregap
		}
		return 0
	case prevMode != cueGenModeAudio || silent:
		return cueGenAudioPregap
	}
	return 0
}

// cueGenMSF writes a sector count as a cue time, mm:ss:ff (75 frames a
// second). Pure.
func cueGenMSF(sectors int) string {
	return fmt.Sprintf("%02d:%02d:%02d", sectors/75/60, sectors/75%60, sectors%75)
}

// cueGenText is the sheet for a split rip: a FILE per track, in order. Pure.
func cueGenText(tracks []cueGenTrack) string {
	var b strings.Builder
	for _, t := range tracks {
		fmt.Fprintf(&b, "FILE \"%s\" BINARY\n", t.Name)
		fmt.Fprintf(&b, "  TRACK %02d %s\n", t.Number, t.Mode)
		if t.Pregap > 0 {
			b.WriteString("    INDEX 00 00:00:00\n")
			fmt.Fprintf(&b, "    INDEX 01 %s\n", cueGenMSF(t.Pregap))
			continue
		}
		b.WriteString("    INDEX 01 00:00:00\n")
	}
	return b.String()
}

// cueGenReadTracks reads a split rip's files into tracks; why says what is
// wrong when it cannot.
func cueGenReadTracks(dir string, set cueGenTrackSet) ([]cueGenTrack, string) {
	tracks := make([]cueGenTrack, 0, len(set.Files))
	prev := ""
	for i, name := range set.Files {
		if strings.Contains(name, `"`) {
			return nil, name + " has a double quote in its name"
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return nil, err.Error()
		}
		size := info.Size()
		if size <= 0 || size%cueGenSectorRaw != 0 {
			return nil, name + " is not whole 2352-byte sectors"
		}
		// The mode is read past the longest pregap (sector 225, or the last
		// one of a shorter file): a data track after audio starts its file
		// with a second of audio-type silence and no sync, and an audio
		// track's pregap can hold data-type sectors.
		head, err := cueGenReadHead(path, (cueGenDataPregap+1)*cueGenSectorRaw)
		if err != nil {
			return nil, err.Error()
		}
		at := min(len(head)/cueGenSectorRaw-1, cueGenDataPregap) * cueGenSectorRaw
		mode := ""
		if at >= 0 {
			mode = cueGenSectorMode(head[at:])
		}
		if mode == "" {
			mode = cueGenModeAudio
		}
		gap := cueGenAudioPregap * cueGenSectorRaw
		silent := len(head) >= gap && cueGenSilent(head[:gap])
		pregap := cueGenPregap(i+1, mode, prev, silent)
		if int64(pregap)*cueGenSectorRaw >= size {
			pregap = 0
		}
		tracks = append(tracks, cueGenTrack{Number: i + 1, Name: name, Mode: mode, Pregap: pregap})
		prev = mode
	}
	return tracks, ""
}

// cueGenTrackSetSheets writes the sheet of every split rip in dir that has
// none: no "<base>.cue" yet and no track named by another sheet. The tracks
// say their own modes, so the console folder's is not needed.
func cueGenTrackSetSheets(dir string) (made int, errs []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil // a folder that went away is not an error here
	}
	var names []string
	have := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
		have[strings.ToLower(e.Name())] = true
	}
	sets := cueGenTrackSets(names)
	if len(sets) == 0 {
		return 0, nil
	}
	sheets, _ := discSheetListed(dir, names)
nextSet:
	for _, set := range sets {
		cue := set.Base + ".cue"
		if have[strings.ToLower(cue)] {
			continue
		}
		for _, name := range set.Files {
			if sheets[strings.ToLower(filepath.Join(dir, name))] {
				continue nextSet
			}
		}
		tracks, why := cueGenReadTracks(dir, set)
		if why != "" {
			logInfo("library", "cue generator: %s left alone: %s", filepath.Join(dir, set.Base), why)
			continue
		}
		if err := cueGenWriteNew(filepath.Join(dir, cue), cueGenText(tracks)); err != nil {
			if !errors.Is(err, os.ErrExist) {
				errs = append(errs, err.Error())
			}
			continue
		}
		have[strings.ToLower(cue)] = true
		made++
		logInfo("library", "cue generator: %s (%d tracks)", filepath.Join(dir, cue), len(tracks))
	}
	return made, errs
}

// cueGenWriteNew writes a new sheet through a hidden temporary file and
// renames it into place, never over a file already there.
func cueGenWriteNew(path, text string) error {
	if _, err := os.Lstat(path); err == nil {
		return os.ErrExist
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".jukahub-tmp")
	if err := writeFileSynced(tmp, []byte(text), 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		_ = os.Remove(tmp)
		return os.ErrExist
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ── Sheets only, on request ──────────────────────────────────────────────

// cueGenDir makes the missing sheets in one folder: split rips, then lone
// .bin files (disctools.go's pass one, without its playlists).
func cueGenDir(dir, mode string) (made int, errs []string) {
	made, errs = cueGenTrackSetSheets(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return made, errs
	}
	var names []string
	have := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
		have[strings.ToLower(e.Name())] = true
	}
	sheets, _ := discSheetListed(dir, names)
	sort.Strings(names)
	for _, name := range names {
		if !strings.EqualFold(filepath.Ext(name), ".bin") || sheets[strings.ToLower(filepath.Join(dir, name))] ||
			discTrackPattern().MatchString(name) {
			continue
		}
		cue := strings.TrimSuffix(name, filepath.Ext(name)) + ".cue"
		if have[strings.ToLower(cue)] {
			continue
		}
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err != nil || info.Size() < discMinImageBytes {
			continue
		}
		text, ok := cueGenSingleText(path, mode)
		if !ok {
			continue
		}
		if err := cueGenWriteNew(filepath.Join(dir, cue), text); err != nil {
			if !errors.Is(err, os.ErrExist) {
				errs = append(errs, err.Error())
			}
			continue
		}
		have[strings.ToLower(cue)] = true
		made++
	}
	return made, errs
}

// cueGenDirsUnder is a disc console folder, its game folders and their
// "Disc N" folders: every place a disc image sits.
func cueGenDirsUnder(systemDir string) []string {
	out := []string{systemDir}
	for _, game := range discGameFolders(systemDir) {
		out = append(out, game)
		entries, err := os.ReadDir(game)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && romDiscFolder(e.Name()) {
				out = append(out, filepath.Join(game, e.Name()))
			}
		}
	}
	return out
}

// cueGenerateAll makes the missing sheets under every disc console folder in
// roots. Sheets only: the playlists are the disc helpers' job.
func cueGenerateAll(roots []string) (made int, errs []string) {
	discWriteMu.Lock()
	defer discWriteMu.Unlock()
	dirs, modes := discSystemDirs(roots)
	for i, dir := range dirs {
		for _, d := range cueGenDirsUnder(dir) {
			n, e := cueGenDir(d, modes[i])
			made += n
			errs = append(errs, e...)
		}
	}
	return made, errs
}

// cueGenSummary is the toast after a run.
func cueGenSummary(made int, errs []string) string {
	switch {
	case made == 0 && len(errs) > 0:
		return "Could not write a .cue: " + firstLine(errs[0])
	case made == 0:
		return "Every disc image already has its .cue"
	}
	line := "Made " + pluralizeCount(made, ".cue file", ".cue files")
	if len(errs) > 0 {
		line += " · " + strconv.Itoa(len(errs)) + " failed"
	}
	return line
}

// cueGenBusy is set while a run started from a button is going.
var cueGenBusy atomic.Bool

// cueGenNow runs the generator over the library in the background. A toast
// says what it made, an open library rescans, and done (main thread, may be
// nil) runs afterwards. Main thread.
func cueGenNow(done func(made int)) {
	if !cueGenBusy.CompareAndSwap(false, true) {
		ShowToast("Still making .cue files…", ToastKindInfo)
		return
	}
	ShowToast("Looking for disc images without a .cue…", ToastKindInfo)
	roots := append([]string(nil), romLibraryRoots()...)
	go func() {
		made := 0
		var errs []string
		defer func() {
			if r := recover(); r != nil {
				logWarn("library", "cue generator stopped: %v", r)
				errs = append(errs, "the run stopped unexpectedly")
			}
			cueGenBusy.Store(false)
			summary := cueGenSummary(made, errs)
			tbxPost(func() {
				kind := ToastKindInfo
				switch {
				case made == 0 && len(errs) > 0:
					kind = ToastKindError
				case len(errs) > 0:
					kind = ToastKindWarning
				case made > 0:
					kind = ToastKindSuccess
				}
				ShowToast(summary, kind)
				if made > 0 {
					libraryRescanSoon(libraryRescanTries)
				}
				if done != nil {
					done(made)
				}
			})
		}()
		made, errs = cueGenerateAll(roots)
		logInfo("library", "cue generator: %s", cueGenSummary(made, errs))
	}()
}

// ── The two buttons ──────────────────────────────────────────────────────

// cueGenRommRow is the ROM manager's "Make missing .cue files" row
// (romm_extras.go).
func cueGenRommRow() rommExtraRow {
	spec := tbxRowSpec{icon: "disc", title: "Make missing .cue files",
		subtitle: "Disc images (.bin) without a .cue sheet don't start in most emulators", pill: "Run", tone: tbxToneNeutral}
	if cueGenBusy.Load() {
		spec.pill = "Working…"
	}
	return rommExtraRow{id: "x:cue", spec: spec}
}

// cueGenRommRun is that row's press: the ROM manager's page scans again when
// sheets were made.
func cueGenRommRun() {
	PlayActivateSound()
	cueGenNow(func(made int) {
		if made > 0 && tbxCurrentScene(appConfig) == rommSceneName && rommUI.phase == rommPhaseReady {
			rommStartScan()
		}
	})
}

// cueGenToolsTile is the Tools hub's "CUE generator" tile (toolshub.go).
func cueGenToolsTile() toolsHubTile {
	tile := toolsHubTile{id: "cuegen", label: "CUE generator", icon: "disc", value: "Make missing .cue files for .bin discs"}
	if cueGenBusy.Load() {
		tile.value, tile.tone = "Making .cue files…", toolsHubToneBusy
	}
	return tile
}

func init() {
	// Neo Geo CD discs are CD-ROMs like Sega CD's (MODE1 data, audio tracks).
	if _, ok := discSystemModes["Neo Geo CD"]; !ok {
		discSystemModes["Neo Geo CD"] = "MODE1/2352"
	}
}
