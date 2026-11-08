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
// copied over the auto state (the auto state it replaces is kept as
// ".jukahub-prev"), and RetroArch loads it as it starts. That works with every
// RetroArch version and every launcher, script or binary.

import (
	"context"
	"image"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// gmetaState is one save state of a game.
type gmetaState struct {
	Path  string
	Slot  int // -1 for the auto state
	Time  time.Time
	Thumb string // the screenshot beside it, "" when there is none
	Size  int64
}

// gmetaMaxStates bounds the strip: RetroArch has 1000 slots, nobody uses them.
const gmetaMaxStates = 24

// gmetaStateSlot reads the slot from a state file's name: "<stem>.state" is 0,
// "<stem>.stateN" N, "<stem>.state.auto" the auto state (-1). ok is false for
// anything else, the screenshots included.
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
		return -1, true
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
	if st.Slot < 0 {
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

// gmetaStageState makes a numbered slot the auto state, so the next start of
// the game loads it. The auto state it replaces is kept as
// "<rom>.state.auto.jukahub-prev"; the auto state itself needs nothing.
func gmetaStageState(st gmetaState) error {
	if st.Slot < 0 {
		return nil
	}
	name := filepath.Base(st.Path)
	cut := strings.LastIndex(strings.ToLower(name), ".state")
	if cut <= 0 {
		return os.ErrInvalid
	}
	auto := filepath.Join(filepath.Dir(st.Path), name[:cut]+".state.auto")
	if regularFile(auto) {
		if err := gmetaCopyFile(auto, auto+".jukahub-prev"); err != nil {
			return err
		}
	}
	if err := gmetaCopyFile(st.Path, auto); err != nil {
		return err
	}
	// The screenshot follows, so the next look at the strip shows the state
	// the auto slot now holds.
	if st.Thumb != "" {
		_ = gmetaCopyFile(st.Thumb, auto+".png")
	}
	return nil
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
