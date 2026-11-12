package main

// gmeta_states.go — a game's RetroArch save states, shown before it starts.
//
// RetroArch names a state after the content it belongs to: "<rom>.state" is
// slot 0, "<rom>.state3" slot 3, "<rom>.state.auto" the state it writes on
// exit and loads on start when savestate_auto_load is on — which
// savestate_resume.go turns on for every RetroArch launch from the library.
// With savestate_thumbnail_enable each state has a "<state>.png" screenshot
// beside it. The states live in RetroArch's savestate_directory (savemanager.go
// knows where that is on each firmware), optionally sorted into per-core and
// per-content-folder sub-folders, or beside the ROM itself.
//
// Starting from a numbered slot uses that same resume mechanism instead of a
// command-line switch a stock launch.sh would not pass on: the chosen state is
// copied over the auto state, and RetroArch loads it as it starts. That works
// with every RetroArch version and every launcher, script or binary.
//
// The previous auto save. RetroArch overwrites "<rom>.state.auto" every time
// the game closes, so one bad exit (a boss fight lost, the wrong door) used to
// cost the only automatic save. There is now exactly one previous copy,
// "<rom>.state.auto.jukahub-prev" with its "<…>.png" beside it, and one rule
// for it: whenever the auto state is about to be replaced — by RetroArch at
// the end of a library launch (gmetaRotateAutoStates, from
// prepareGameMenuStates) or by a slot staged here — the auto state as it was
// becomes the previous copy, keeping its own date. A copy that already holds
// the same state is not written again, and a state staged here is not rotated
// a second time by the launch that follows it (gmetaStaged). The states
// sheet lists it as "Previous auto save" (gmetaPrevAutoSlot); starting from it
// swaps the two, so that start can be undone the same way. One copy only: the
// card is small and states of the larger systems are megabytes.

import (
	"context"
	"image"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gmetaState is one save state of a game.
type gmetaState struct {
	Path  string
	Slot  int // gmetaAutoSlot, gmetaPrevAutoSlot or the slot number
	Time  time.Time
	Thumb string // the screenshot beside it, "" when there is none
	Size  int64
}

const (
	// gmetaAutoSlot is the auto state's Slot, gmetaPrevAutoSlot the previous
	// auto save's. Both sort before the numbered slots.
	gmetaAutoSlot     = -1
	gmetaPrevAutoSlot = -2
	// gmetaPrevSuffix names the previous auto save after the auto state:
	// "<rom>.state.auto.jukahub-prev" (and ".png" after that for its picture).
	gmetaPrevSuffix = ".jukahub-prev"
	// gmetaMtimeSlack is how far two modification times may differ and still be
	// the same moment: FAT keeps them to two seconds.
	gmetaMtimeSlack = 2 * time.Second
)

// gmetaStaged is the auto state gmetaStageState just replaced for the launch
// about to start, which therefore must not rotate it again: the previous copy
// already holds what was there before. Cleared by that launch.
var gmetaStaged struct {
	mu   sync.Mutex
	path string
}

// gmetaMaxStates bounds the strip: RetroArch has 1000 slots, nobody uses them.
const gmetaMaxStates = 24

// gmetaStateSlot reads the slot from a state file's name: "<stem>.state" is 0,
// "<stem>.stateN" N, "<stem>.state.auto" the auto state (gmetaAutoSlot) and
// "<stem>.state.auto.jukahub-prev" the previous auto save (gmetaPrevAutoSlot).
// ok is false for anything else, the screenshots and temporary files included.
func gmetaStateSlot(name, stem string) (int, bool) {
	prefix := stem + ".state"
	if len(name) < len(prefix) || !strings.EqualFold(name[:len(prefix)], prefix) {
		return 0, false
	}
	rest := strings.ToLower(name[len(prefix):])
	switch {
	case rest == "":
		return 0, true
	case rest == ".auto":
		return gmetaAutoSlot, true
	case rest == ".auto"+gmetaPrevSuffix:
		return gmetaPrevAutoSlot, true
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}

// gmetaStateDirs lists the folders RetroArch may keep a game's states in: its
// states folders, then the ROM's own folder.
func gmetaStateDirs(romPath string) []string {
	var dirs []string
	for _, r := range saveManagerRoots() {
		if r.Name == "RetroArch states" {
			dirs = append(dirs, r.Path)
		}
	}
	return append(dirs, filepath.Dir(romPath))
}

// gmetaListStates finds a game's states. The states folders are searched two
// levels deep (RetroArch's per-core and per-content-folder sorting); the ROM's
// own folder only at its top, since it holds the library itself.
func gmetaListStates(romPath string, dirs []string) []gmetaState {
	stem := strings.TrimSuffix(filepath.Base(romPath), filepath.Ext(romPath))
	if stem == "" {
		return nil
	}
	romDir := filepath.Clean(filepath.Dir(romPath))
	seen := map[string]bool{}
	var out []gmetaState
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				if depth > 0 && !strings.HasPrefix(e.Name(), ".") {
					walk(filepath.Join(dir, e.Name()), depth-1)
				}
				continue
			}
			slot, ok := gmetaStateSlot(e.Name(), stem)
			if !ok {
				continue
			}
			path := filepath.Join(dir, e.Name())
			key := strings.ToLower(path)
			if seen[key] {
				continue
			}
			seen[key] = true
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				continue
			}
			st := gmetaState{Path: path, Slot: slot, Time: info.ModTime(), Size: info.Size()}
			if regularFile(path + ".png") {
				st.Thumb = path + ".png"
			}
			out = append(out, st)
		}
	}
	for _, dir := range dirs {
		clean := filepath.Clean(dir)
		depth := 2
		if strings.EqualFold(clean, romDir) {
			depth = 0
		}
		walk(clean, depth)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Slot < 0) != (out[j].Slot < 0) {
			return out[i].Slot < 0
		}
		return out[i].Time.After(out[j].Time)
	})
	if len(out) > gmetaMaxStates {
		out = out[:gmetaMaxStates]
	}
	return out
}

// gmetaStateLabel names a state on its card.
func gmetaStateLabel(st gmetaState) string {
	switch {
	case st.Slot == gmetaPrevAutoSlot:
		return "Previous auto save"
	case st.Slot < 0:
		return "Resume"
	}
	return "Slot " + strconv.Itoa(st.Slot)
}

// gmetaWhen says when something happened, briefly.
func gmetaWhen(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	case d < 24*time.Hour && t.Day() == now.Day():
		return "today " + t.Format("15:04")
	case d < 48*time.Hour && t.Day() == now.AddDate(0, 0, -1).Day():
		return "yesterday " + t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("2 Jan 15:04")
	}
	return t.Format("2 Jan 2006")
}

// gmetaCopyFile copies src over dst through a temporary file, so a reader
// never sees half a state.
func gmetaCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".jukahub-tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	for _, e := range []error{copyErr, syncErr, closeErr} {
		if e != nil {
			os.Remove(tmp)
			return e
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// gmetaStageState makes a state the auto state, so the next start of the game
// loads it. A numbered slot is copied over the auto state, which is kept as
// the previous auto save first (gmetaKeepPrevAuto); the previous auto save
// trades places with the auto state; the auto state itself needs nothing.
func gmetaStageState(st gmetaState) error {
	switch {
	case st.Slot == gmetaPrevAutoSlot:
		return gmetaSwapPrevAuto(st.Path)
	case st.Slot < 0:
		return nil
	}
	name := filepath.Base(st.Path)
	cut := strings.LastIndex(strings.ToLower(name), ".state")
	if cut <= 0 {
		return os.ErrInvalid
	}
	auto := filepath.Join(filepath.Dir(st.Path), name[:cut]+".state.auto")
	// An auto state an earlier stage wrote (its launch never ran) is only a
	// copy of a slot: the previous auto save already holds the real one.
	gmetaStaged.mu.Lock()
	restage := gmetaStaged.path != "" && strings.EqualFold(gmetaStaged.path, filepath.Clean(auto))
	gmetaStaged.mu.Unlock()
	if !restage {
		if err := gmetaKeepPrevAuto(auto); err != nil {
			return err
		}
	}
	if err := gmetaCopyFile(st.Path, auto); err != nil {
		return err
	}
	// The slot's own date goes with it, so the Resume card says when that
	// state was made rather than when it was copied.
	_ = os.Chtimes(auto, st.Time, st.Time)
	// The screenshot follows, so the next look at the strip shows the state
	// the auto slot now holds; a slot without one leaves no stale picture.
	if st.Thumb != "" {
		if gmetaCopyFile(st.Thumb, auto+".png") == nil {
			_ = os.Chtimes(auto+".png", st.Time, st.Time)
		}
	} else {
		_ = os.Remove(auto + ".png")
	}
	gmetaMarkStaged(auto)
	return nil
}

// gmetaSwapPrevAuto trades the previous auto save and the auto state (and
// their pictures) by renaming them, which keeps both dates and costs no copy.
func gmetaSwapPrevAuto(prev string) error {
	if !strings.HasSuffix(prev, gmetaPrevSuffix) {
		return os.ErrInvalid
	}
	auto := strings.TrimSuffix(prev, gmetaPrevSuffix)
	if err := gmetaSwapFiles(prev, auto); err != nil {
		return err
	}
	_ = gmetaSwapFiles(prev+".png", auto+".png")
	gmetaMarkStaged(auto)
	return nil
}

// gmetaSwapFiles swaps a and b; when only one exists it moves to the other's
// name. Renames in one folder, so a power cut leaves at worst the temporary
// name, which no listing reads.
func gmetaSwapFiles(a, b string) error {
	aOK, bOK := regularFile(a), regularFile(b)
	switch {
	case aOK && bOK:
		tmp := b + ".jukahub-swap"
		if err := os.Rename(b, tmp); err != nil {
			return err
		}
		if err := os.Rename(a, b); err != nil {
			_ = os.Rename(tmp, b)
			return err
		}
		return os.Rename(tmp, a)
	case aOK:
		return os.Rename(a, b)
	case bOK:
		return os.Rename(b, a)
	}
	return os.ErrNotExist
}

// gmetaMarkStaged notes the auto state a stage just wrote (gmetaStaged).
func gmetaMarkStaged(auto string) {
	gmetaStaged.mu.Lock()
	gmetaStaged.path = filepath.Clean(auto)
	gmetaStaged.mu.Unlock()
}

// gmetaKeepPrevAuto makes the auto state at auto the previous auto save, with
// its own date, unless there is no auto state or the previous copy already
// holds it (same size, same date: RetroArch has not written it since).
func gmetaKeepPrevAuto(auto string) error {
	info, err := os.Stat(auto)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return nil
	}
	prev := auto + gmetaPrevSuffix
	if p, err := os.Stat(prev); err == nil && p.Mode().IsRegular() && p.Size() == info.Size() {
		if d := p.ModTime().Sub(info.ModTime()); d > -gmetaMtimeSlack && d < gmetaMtimeSlack {
			return nil
		}
	}
	if err := gmetaCopyFile(auto, prev); err != nil {
		return err
	}
	_ = os.Chtimes(prev, info.ModTime(), info.ModTime())
	if pic, err := os.Stat(auto + ".png"); err == nil && pic.Mode().IsRegular() {
		if gmetaCopyFile(auto+".png", prev+".png") == nil {
			_ = os.Chtimes(prev+".png", pic.ModTime(), pic.ModTime())
		}
	} else {
		_ = os.Remove(prev + ".png")
	}
	return nil
}

// gmetaRotateAutoStates keeps the previous auto save of a game about to start
// from the library (prepareGameMenuStates): RetroArch replaces the auto state
// when it closes, so what it holds now becomes the previous copy. The auto
// state itself stays where it is, since RetroArch loads it as the game starts.
// A state just staged from the states sheet is left alone (gmetaStaged).
func gmetaRotateAutoStates(romPath string) {
	// The mark is taken once, before the walk: a game with an auto state in
	// two folders must not lose it on the first, unstaged one and then rotate
	// the staged copy over the real previous save. A mark for another game
	// (its launch failed) is not carried over either.
	gmetaStaged.mu.Lock()
	staged := gmetaStaged.path
	gmetaStaged.path = ""
	gmetaStaged.mu.Unlock()
	for _, st := range gmetaListStates(romPath, gmetaStateDirs(romPath)) {
		if st.Slot != gmetaAutoSlot {
			continue
		}
		if staged != "" && strings.EqualFold(staged, filepath.Clean(st.Path)) {
			continue
		}
		if err := gmetaKeepPrevAuto(st.Path); err != nil {
			logWarn("resume", "could not keep the previous auto save of %s: %v", filepath.Base(romPath), err)
		}
	}
}

// The box a state screenshot is scaled into for the strip.
const (
	gmetaThumbW = 240
	gmetaThumbH = 136
)

// gmetaLoadThumb decodes and shrinks a state screenshot off the render thread,
// under the shared decode permit.
func gmetaLoadThumb(ctx context.Context, path string) (*image.NRGBA, error) {
	if !imageDecodeSem.Acquire(ctx, 1) {
		return nil, ctx.Err()
	}
	defer imageDecodeSem.Release(1)
	pic, err := imgvDecodeFile(path)
	if err != nil {
		return nil, err
	}
	b := pic.Bounds()
	w, h := imgvFitInside(b.Dx(), b.Dy(), gmetaThumbW, gmetaThumbH)
	return imgvScale(pic, w, h), nil
}
