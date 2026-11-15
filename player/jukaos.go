package main

// jukaos.go — JukaHub OS: the optional OS session, its layers and its journal.
//
// JukaHub already does most of what an operating system shell does on the
// TrimUI, so it can run as one: "JukaHub OS" is a session mode on top of the
// app, never a reflash. Nothing on the firmware's partitions is written.
// Everything JukaHub OS changes is either
//
//   - private to JukaHub: launch.sh starts "JukaHub --os-session" when OS mode
//     is on (os/os.json), and that start makes a private mount namespace,
//     bind-mounts the enabled layers' files over the system's (newer
//     libraries, fixed configs) and re-executes itself inside it
//     (jukaos_linux.go). JukaHub and every game it starts see the layers; the
//     firmware, MainUI and anything outside never do, and the namespace dies
//     with the last process in it; or
//   - global but temporary: kernel modules a layer carries (only when the
//     module was built for this exact kernel) and whitelisted sysfs/proc
//     values. Each change is written to a journal in /tmp (RAM) before the
//     next one is made, and undone in reverse order when JukaHub quits, when
//     the user returns to the stock menu, and at the next start when a crash
//     left the journal behind. A reboot clears both the changes and the
//     journal, so the two always agree.
//
// A reboot therefore always comes back to the stock system, and the session
// has three ways out that need no card reader: the power card's "Return to
// <firmware>", holding Select or B through the boot animation (the stock menu
// for this boot), and the OS guard — two OS starts in a row that did not get
// healthy start the next ones without layers until a layer is changed.
//
// This file is GOOS-neutral and holds the parsers of outside data (layer.json,
// a module's .modinfo, the journal), so the tests run on every host. The
// device side is jukaos_linux.go; jukaos_other.go is the stub elsewhere;
// jukaos_settings.go is the Settings section, the power card's label and the
// escape hatch. docs/JUKAHUB_OS.md is the user-facing description.

import (
	"bytes"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// jukaosDirName is the OS folder beside the executable: os.json,
	// guard.json and layers/.
	jukaosDirName    = "os"
	jukaosStateName  = "os.json"
	jukaosGuardName  = "guard.json"
	jukaosLayersName = "layers"
	jukaosLayerName  = "layer.json"
	// jukaosRunDir is in RAM: a reboot clears it together with everything the
	// journal records.
	jukaosRunDir        = "/tmp/jukaos"
	jukaosJournalName   = "journal.json"
	jukaosSessionName   = "session.json"
	jukaosStockOnceName = "stock-once"
	// jukaosBuiltinLibsID is the generated layer that hands JukaHub's own
	// libs/ folder to the games it starts.
	jukaosBuiltinLibsID = "jukahub-libs"
	// jukaosGuardLimit OS starts with layers in a row that did not become
	// healthy make the next start leave the layers out.
	jukaosGuardLimit = 2
	// Bounds on what is read from the card.
	jukaosLayerFileMax  = 256 << 10
	jukaosStateFileMax  = 256 << 10
	jukaosJournalMax    = 1 << 20
	jukaosJournalLimit  = 1024
	jukaosModuleMax     = 32 << 20
	jukaosSysfsValueMax = 128
	jukaosLayerIDMax    = 64
	jukaosLayersMax     = 64
	jukaosLayerItemsMax = 256

	// The session's environment. The first start (--os-session) sets them for
	// the JukaHub it executes; children never get them (jukaosChildEnv).
	jukaosEnvPrefix    = "JUKAHUB_OS_"
	jukaosEnvSession   = "JUKAHUB_OS_SESSION"    // "1" in an OS session
	jukaosEnvLayers    = "JUKAHUB_OS_LAYERS"     // the layers applied, comma-separated ids
	jukaosEnvChildLibs = "JUKAHUB_OS_CHILD_LIBS" // library folders for the games, ":"-separated
	jukaosEnvGuard     = "JUKAHUB_OS_GUARD"      // "1" when the OS guard left the layers out
	jukaosSessionArg   = "--os-session"
)

// jukaosState is os/os.json: the mode, the layers switched on, and the
// session's options. Written by the Settings section; launch.sh reads only
// whether "enabled" is true (it greps the line this encoder writes).
type jukaosState struct {
	Enabled bool            `json:"enabled"`
	Layers  map[string]bool `json:"layers,omitempty"`
	// DropCaches frees the kernel's page cache once the first screen is up, so
	// the games get the RAM (nil = on).
	DropCaches *bool `json:"dropCaches,omitempty"`
	// Edition is "os" on a card made from JukaHub-OS-Trimui.zip
	// (trimui/make-os-package.sh): JukaHub was installed as the OS, with OS mode
	// and the boot hook already on. Only shown; switching OS mode off works the
	// same as on an app install.
	Edition string `json:"edition,omitempty"`
}

// jukaosEditionOS is os.json's edition value for the OS package.
const jukaosEditionOS = "os"

// jukaosGuardState is os/guard.json: OS starts with layers in a row that did
// not become healthy, and whether the user was told about the last trip.
type jukaosGuardState struct {
	Failed int  `json:"failed"`
	Told   bool `json:"told,omitempty"`
}

// jukaosLayerSpec is a layer.json as written by a layer's author.
type jukaosLayerSpec struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	Arch        string            `json:"arch"`
	Binds       []jukaosBindSpec  `json:"binds"`
	Libs        []string          `json:"libs"`
	Children    bool              `json:"children"`
	Modules     []string          `json:"modules"`
	Sysfs       map[string]string `json:"sysfs"`
	Core        bool              `json:"core"`
}

// jukaosBindSpec is one bind: a file or folder of the layer over a system
// path. RW leaves the mount writable (a folder of settings an emulator saves
// into); otherwise it is read-only, so nothing writes through it to the card.
type jukaosBindSpec struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
	RW  bool   `json:"rw,omitempty"`
}

// jukaosBind is a checked bind: Src a path on this machine inside the layer,
// Dst the system path (symlinks resolved) it covers.
type jukaosBind struct {
	Src string
	Dst string
	Dir bool
	RW  bool
}

// jukaosSysfsWrite is one checked sysfs/proc value.
type jukaosSysfsWrite struct {
	Path  string
	Value string
}

// jukaosModule is one checked kernel module.
type jukaosModule struct {
	Path     string // the .ko on the card
	Name     string // the name the kernel lists it under
	Vermagic string
}

// jukaosLayer is a layer as found on the card, checked against this machine.
// Problem makes the whole layer unusable; Notes are parts that are skipped.
type jukaosLayer struct {
	ID      string
	Dir     string
	Spec    jukaosLayerSpec
	Builtin bool
	Binds   []jukaosBind
	LibDirs []string
	Modules []jukaosModule
	Sysfs   []jukaosSysfsWrite
	Problem string
	Notes   []string
}

// jukaosHost is what the checks compare a layer with. Root is "" on the
// device; the tests point it at a folder that stands in for "/". AppDir and
// SDRoot are system paths (forward slashes).
type jukaosHost struct {
	Root    string
	Machine string // uname -m: "aarch64"
	Release string // uname -r: "4.9.191"
	AppDir  string
	SDRoot  string
}

// jukaosSessionInfo is /tmp/jukaos/session.json: what the --os-session start
// did, for the Settings status and the log. Only the process it names reads it.
type jukaosSessionInfo struct {
	PID          int      `json:"pid"`
	Namespace    bool     `json:"namespace"`
	NamespaceErr string   `json:"namespaceErr,omitempty"`
	Guarded      bool     `json:"guarded,omitempty"`
	Layers       []string `json:"layers,omitempty"`
	Binds        int      `json:"binds"`
	Skipped      []string `json:"skipped,omitempty"`
}

// jukaosJournal is /tmp/jukaos/journal.json: every global change of the
// session, in the order it was made.
type jukaosJournal struct {
	PID     int                  `json:"pid"`
	BootID  string               `json:"bootId,omitempty"`
	Entries []jukaosJournalEntry `json:"entries"`
}

// jukaosJournalEntry is one change: a module loaded ("module": Name, Path) or
// a value written ("sysfs": Path, Old, New).
type jukaosJournalEntry struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	Path string `json:"path"`
	Old  string `json:"old,omitempty"`
	New  string `json:"new,omitempty"`
}

const (
	jukaosKindModule = "module"
	jukaosKindSysfs  = "sysfs"
)

// ── Folders and the state files ────────────────────────────────────────────

// jukaosAppDir is the folder JukaHub runs from, symlinks resolved.
func jukaosAppDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// jukaosDir is <app>/os.
func jukaosDir(appDir string) string { return filepath.Join(appDir, jukaosDirName) }

// jukaosLayersDir is <app>/os/layers.
func jukaosLayersDir(appDir string) string {
	return filepath.Join(appDir, jukaosDirName, jukaosLayersName)
}

// jukaosDecodeState reads os.json. A damaged file is App mode with nothing on.
func jukaosDecodeState(data []byte) (jukaosState, error) {
	var s jukaosState
	if len(data) > jukaosStateFileMax {
		return jukaosState{}, errors.New("os.json is too large")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return jukaosState{}, err
	}
	for id := range s.Layers {
		if !jukaosValidID(id) {
			delete(s.Layers, id)
		}
	}
	return s, nil
}

// jukaosEncodeState writes os.json in the form launch.sh greps for:
// `"enabled": true` on a line of its own.
func jukaosEncodeState(s jukaosState) ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// jukaosLoadState reads <app>/os/os.json (missing = App mode).
func jukaosLoadState(appDir string) jukaosState {
	data, err := jukaosReadBounded(filepath.Join(jukaosDir(appDir), jukaosStateName), jukaosStateFileMax)
	if err != nil {
		return jukaosState{}
	}
	s, err := jukaosDecodeState(data)
	if err != nil {
		return jukaosState{}
	}
	return s
}

// jukaosSaveState writes <app>/os/os.json durably (it decides the next boot).
func jukaosSaveState(appDir string, s jukaosState) error {
	data, err := jukaosEncodeState(s)
	if err != nil {
		return err
	}
	return jukaosWriteDurable(filepath.Join(jukaosDir(appDir), jukaosStateName), data)
}

// jukaosDropCachesOn is the DropCaches option with its default.
func (s jukaosState) jukaosDropCachesOn() bool { return s.DropCaches == nil || *s.DropCaches }

// jukaosLoadGuard reads <app>/os/guard.json.
func jukaosLoadGuard(appDir string) jukaosGuardState {
	var g jukaosGuardState
	data, err := jukaosReadBounded(filepath.Join(jukaosDir(appDir), jukaosGuardName), 4096)
	if err != nil || json.Unmarshal(data, &g) != nil || g.Failed < 0 {
		return jukaosGuardState{}
	}
	if g.Failed > 99 {
		g.Failed = 99
	}
	return g
}

// jukaosSaveGuard writes <app>/os/guard.json durably: a start that dies right
// after it must still have been counted.
func jukaosSaveGuard(appDir string, g jukaosGuardState) error {
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return jukaosWriteDurable(filepath.Join(jukaosDir(appDir), jukaosGuardName), append(data, '\n'))
}

// jukaosReadBounded reads at most limit bytes of a regular file.
func jukaosReadBounded(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", filepath.Base(p))
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s is too large", filepath.Base(p))
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}

// jukaosWriteDurable replaces a small file through a synced temporary file in
// the same folder, so a power cut leaves the old or the new one.
func jukaosWriteDurable(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(p)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// ── Layers ─────────────────────────────────────────────────────────────────

// jukaosValidID is a layer folder name the state file may refer to: letters,
// digits, dot, dash and underscore, not starting with a dot. "enabled" is
// reserved: launch.sh greps os.json for `"enabled": true`, and a layer of
// that name switched on would read as OS mode.
func jukaosValidID(id string) bool {
	if id == "" || len(id) > jukaosLayerIDMax || id[0] == '.' || id == "enabled" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// jukaosDecodeLayerSpec parses a layer.json. Unknown fields are allowed (a
// newer layer format adds some); an oversized list is not.
func jukaosDecodeLayerSpec(data []byte) (jukaosLayerSpec, error) {
	var spec jukaosLayerSpec
	if len(data) > jukaosLayerFileMax {
		return spec, errors.New("layer.json is too large")
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return jukaosLayerSpec{}, fmt.Errorf("layer.json: %v", err)
	}
	if len(spec.Binds) > jukaosLayerItemsMax || len(spec.Libs) > jukaosLayerItemsMax ||
		len(spec.Modules) > jukaosLayerItemsMax || len(spec.Sysfs) > jukaosLayerItemsMax {
		return jukaosLayerSpec{}, errors.New("layer.json lists too many entries")
	}
	spec.Name = jukaosOneLine(spec.Name, 64)
	spec.Description = jukaosOneLine(spec.Description, 200)
	spec.Version = jukaosOneLine(spec.Version, 32)
	spec.Arch = strings.ToLower(jukaosOneLine(spec.Arch, 32))
	return spec, nil
}

// jukaosOneLine keeps a display string to one line of at most max runes.
func jukaosOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// jukaosLoadLayers reads every layer folder under layersDir (sorted by id)
// and checks each against host. The built-in layer is not among them
// (jukaosBuiltinLayer).
func jukaosLoadLayers(h jukaosHost, layersDir string) []jukaosLayer {
	entries, err := os.ReadDir(layersDir)
	if err != nil {
		return nil
	}
	var out []jukaosLayer
	for _, e := range entries {
		if len(out) >= jukaosLayersMax {
			break
		}
		if !e.IsDir() || !jukaosValidID(e.Name()) || e.Name() == jukaosBuiltinLibsID {
			continue
		}
		dir := filepath.Join(layersDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, jukaosLayerName)); err != nil {
			continue
		}
		out = append(out, jukaosLoadLayer(h, e.Name(), dir))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// jukaosLoadLayer reads and checks one layer folder.
func jukaosLoadLayer(h jukaosHost, id, dir string) (l jukaosLayer) {
	// A file that trips up a parser (debug/elf on a damaged .ko) makes the
	// layer unusable, never the start: the --os-session start reads the
	// layers before any guard could count a crash.
	defer func() {
		if r := recover(); r != nil {
			l = jukaosLayer{ID: id, Dir: dir, Problem: fmt.Sprintf("could not be read (%v)", r)}
		}
	}()
	l = jukaosLayer{ID: id, Dir: dir}
	data, err := jukaosReadBounded(filepath.Join(dir, jukaosLayerName), jukaosLayerFileMax)
	if err != nil {
		l.Problem = err.Error()
		return l
	}
	spec, err := jukaosDecodeLayerSpec(data)
	if err != nil {
		l.Problem = err.Error()
		return l
	}
	l.Spec = spec
	if l.Spec.Name == "" {
		l.Spec.Name = id
	}
	jukaosCheckLayer(h, &l)
	return l
}

// jukaosCheckLayer resolves and checks every part of l.Spec. A part that
// fails is left out with a note; a layer for another machine is unusable.
func jukaosCheckLayer(h jukaosHost, l *jukaosLayer) {
	if !jukaosArchFits(l.Spec.Arch, h.Machine) {
		l.Problem = fmt.Sprintf("built for %s, this handheld is %s", l.Spec.Arch, h.Machine)
		return
	}
	base, err := filepath.EvalSymlinks(l.Dir)
	if err != nil {
		l.Problem = "layer folder unreadable: " + err.Error()
		return
	}
	for _, b := range l.Spec.Binds {
		bind, err := jukaosCheckBind(h, base, b, l.Spec.Core)
		if err != nil {
			l.Notes = append(l.Notes, fmt.Sprintf("bind %s: %v", jukaosOneLine(b.Dst, 80), err))
			continue
		}
		l.Binds = append(l.Binds, bind)
	}
	for _, rel := range l.Spec.Libs {
		dir, err := jukaosInsideLayer(base, rel)
		if err == nil {
			if fi, serr := os.Stat(dir); serr != nil || !fi.IsDir() {
				err = errors.New("not a folder")
			}
		}
		if err != nil {
			l.Notes = append(l.Notes, fmt.Sprintf("libs %s: %v", jukaosOneLine(rel, 80), err))
			continue
		}
		l.LibDirs = append(l.LibDirs, dir)
	}
	for _, rel := range l.Spec.Modules {
		m, err := jukaosCheckModule(h, base, rel)
		if err != nil {
			l.Notes = append(l.Notes, fmt.Sprintf("driver %s: %v", jukaosOneLine(rel, 80), err))
			continue
		}
		l.Modules = append(l.Modules, m)
	}
	keys := make([]string, 0, len(l.Spec.Sysfs))
	for k := range l.Spec.Sysfs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := l.Spec.Sysfs[k]
		if err := jukaosCheckSysfs(k, v); err != nil {
			l.Notes = append(l.Notes, fmt.Sprintf("value %s: %v", jukaosOneLine(k, 80), err))
			continue
		}
		l.Sysfs = append(l.Sysfs, jukaosSysfsWrite{Path: k, Value: strings.TrimSpace(v)})
	}
	if len(l.Binds)+len(l.LibDirs)+len(l.Modules)+len(l.Sysfs) == 0 {
		if len(l.Notes) > 0 {
			l.Problem = "nothing in it can be used here (" + l.Notes[0] + ")"
		} else {
			l.Problem = "the layer is empty"
		}
	}
}

// jukaosArchFits reports whether a layer built for arch suits machine. An
// empty arch or "any" suits every machine; an aarch64 handheld also takes
// 32-bit ARM layers (its 32-bit games), whose binds the ELF check still
// compares file by file.
func jukaosArchFits(arch, machine string) bool {
	a, m := jukaosArchFamily(arch), jukaosArchFamily(machine)
	switch {
	case a == "" || a == "any":
		return true
	case a == m:
		return true
	case m == "aarch64" && a == "arm":
		return true
	}
	return false
}

// jukaosArchFamily folds the names an architecture goes by.
func jukaosArchFamily(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); {
	case s == "arm64" || s == "aarch64":
		return "aarch64"
	case s == "armhf" || s == "armel" || strings.HasPrefix(s, "armv") || s == "arm":
		return "arm"
	case s == "amd64" || s == "x86_64" || s == "x64":
		return "x86_64"
	case s == "i386" || s == "i686" || s == "x86":
		return "x86"
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// jukaosMachineELF is the ELF machine type of a uname machine name.
func jukaosMachineELF(machine string) elf.Machine {
	switch jukaosArchFamily(machine) {
	case "aarch64":
		return elf.EM_AARCH64
	case "arm":
		return elf.EM_ARM
	case "x86_64":
		return elf.EM_X86_64
	case "x86":
		return elf.EM_386
	case "riscv64":
		return elf.EM_RISCV
	}
	return elf.EM_NONE
}

// jukaosInsideLayer resolves rel (a relative path with forward slashes)
// inside the layer folder base (symlinks resolved) and refuses anything that
// leaves it: an absolute path, "..", or a symlink pointing out.
func jukaosInsideLayer(base, rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") || filepath.IsAbs(rel) {
		return "", errors.New("must be a path inside the layer")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", errors.New("must not leave the layer (\"..\")")
		}
	}
	p := filepath.Join(base, filepath.FromSlash(rel))
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", errors.New("not found in the layer")
	}
	if !jukaosWithinHost(base, resolved) {
		return "", errors.New("points outside the layer")
	}
	return resolved, nil
}

// jukaosWithinHost reports whether p is dir or inside it (paths of this
// machine).
func jukaosWithinHost(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// jukaosWithin reports whether the system path p is dir or inside it.
func jukaosWithin(dir, p string) bool {
	dir, p = path.Clean(dir), path.Clean(p)
	if dir == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// jukaosReal is the path on this machine for the system path p.
func (h jukaosHost) jukaosReal(p string) string {
	if h.Root == "" {
		return p
	}
	return filepath.Join(h.Root, filepath.FromSlash(p))
}

// jukaosSystem is the system path for a resolved path on this machine.
func (h jukaosHost) jukaosSystem(real string) (string, bool) {
	if h.Root == "" {
		return real, true
	}
	root, err := filepath.EvalSymlinks(h.Root)
	if err != nil {
		root = h.Root
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return "/", true
	}
	return "/" + filepath.ToSlash(rel), true
}

// jukaosRefusedTarget says why a system path can never be bound over, ""
// when it can: the root, the kernel's own trees, the card's root and
// everything above it, JukaHub's folder and everything above or in it, and
// /tmp (JukaHub's run state and the firmware's command queue live there).
func (h jukaosHost) jukaosRefusedTarget(p string) string {
	p = path.Clean(p)
	switch {
	case p == "/":
		return "the whole system cannot be replaced"
	case jukaosWithin("/proc", p) || jukaosWithin("/sys", p) || jukaosWithin("/dev", p):
		return "kernel folders (/proc, /sys, /dev) cannot be replaced"
	case jukaosWithin(p, "/tmp") || jukaosWithin("/tmp", p):
		return "/tmp cannot be replaced"
	}
	if h.AppDir != "" && (jukaosWithin(p, h.AppDir) || jukaosWithin(h.AppDir, p)) {
		return "JukaHub's own folder cannot be replaced"
	}
	if h.SDRoot != "" && jukaosWithin(p, h.SDRoot) {
		return "the SD card's root cannot be replaced"
	}
	return ""
}

// jukaosCoreName reports whether a file name belongs to the C runtime, which
// only a layer marked "core" may replace.
func jukaosCoreName(name string) bool {
	for _, prefix := range []string{"ld-linux", "ld-musl", "libc.so", "libc-", "libpthread", "libdl.", "libdl-", "libm.so", "libm-", "librt.", "librt-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// jukaosCheckBind checks one bind of a layer whose folder is base.
func jukaosCheckBind(h jukaosHost, base string, b jukaosBindSpec, core bool) (jukaosBind, error) {
	src, err := jukaosInsideLayer(base, b.Src)
	if err != nil {
		return jukaosBind{}, fmt.Errorf("source %v", err)
	}
	dst := b.Dst
	if !strings.HasPrefix(dst, "/") || strings.ContainsRune(dst, 0) || strings.Contains(dst, "\\") {
		return jukaosBind{}, errors.New("target must be an absolute system path")
	}
	for _, part := range strings.Split(dst, "/") {
		if part == ".." {
			return jukaosBind{}, errors.New("target must not contain \"..\"")
		}
	}
	dst = path.Clean(dst)
	if why := h.jukaosRefusedTarget(dst); why != "" {
		return jukaosBind{}, errors.New(why)
	}
	realDst, err := filepath.EvalSymlinks(h.jukaosReal(dst))
	if err != nil {
		return jukaosBind{}, errors.New("target does not exist on this system")
	}
	resolved, ok := h.jukaosSystem(realDst)
	if !ok {
		return jukaosBind{}, errors.New("target points outside the system")
	}
	if why := h.jukaosRefusedTarget(resolved); why != "" {
		return jukaosBind{}, errors.New(why)
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		return jukaosBind{}, errors.New("source unreadable")
	}
	dstInfo, err := os.Stat(realDst)
	if err != nil {
		return jukaosBind{}, errors.New("target unreadable")
	}
	switch {
	case srcInfo.IsDir() != dstInfo.IsDir():
		return jukaosBind{}, errors.New("source and target must both be files or both folders")
	case !srcInfo.IsDir() && (!srcInfo.Mode().IsRegular() || !dstInfo.Mode().IsRegular()):
		return jukaosBind{}, errors.New("only regular files and folders can be bound")
	}
	if !core {
		if jukaosCoreName(path.Base(dst)) || jukaosCoreName(path.Base(resolved)) {
			return jukaosBind{}, errors.New("replaces the C runtime; the layer needs \"core\": true")
		}
		if dstInfo.IsDir() && jukaosTreeHasCore(realDst) {
			return jukaosBind{}, errors.New("covers the C runtime's folder; the layer needs \"core\": true")
		}
	}
	if !srcInfo.IsDir() {
		if err := jukaosCheckELFPair(h, src, realDst); err != nil {
			return jukaosBind{}, err
		}
	}
	return jukaosBind{Src: src, Dst: resolved, Dir: srcInfo.IsDir(), RW: b.RW}, nil
}

// jukaosTreeHasCore reports whether a folder holds a C runtime file, in it or
// in the library folders below it, up to three levels down (/usr covers
// /usr/lib/<triplet>/libc.so.6). Only folders named like library folders are
// entered, so a big tree of data (shaders, ROMs) costs one read. Symlinked
// folders are not followed. A folder that cannot be read counts as holding
// one: the safe answer.
func jukaosTreeHasCore(dir string) bool {
	const maxDepth, maxDirs = 3, 64
	type level struct {
		path  string
		depth int
	}
	queue := []level{{dir, 1}}
	for read := 0; len(queue) > 0; read++ {
		if read >= maxDirs {
			return true
		}
		cur := queue[0]
		queue = queue[1:]
		entries, err := os.ReadDir(cur.path)
		if err != nil {
			return true
		}
		for _, e := range entries {
			if jukaosCoreName(e.Name()) {
				return true
			}
			if e.IsDir() && cur.depth < maxDepth && jukaosLibFolderName(e.Name()) {
				queue = append(queue, level{filepath.Join(cur.path, e.Name()), cur.depth + 1})
			}
		}
	}
	return false
}

// jukaosLibFolderName reports whether a folder name is one the C runtime
// lives in: usr, lib, lib32, lib64, local, or a multiarch triplet
// (aarch64-linux-gnu, arm-linux-gnueabihf).
func jukaosLibFolderName(name string) bool {
	switch name {
	case "usr", "lib", "lib32", "lib64", "local":
		return true
	}
	return strings.Contains(name, "-linux-")
}

// jukaosELFMachineOf is the machine of an ELF file; ok is false for a file
// that is not ELF.
func jukaosELFMachineOf(p string) (elf.Machine, bool) {
	f, err := os.Open(p)
	if err != nil {
		return elf.EM_NONE, false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != elf.ELFMAG {
		return elf.EM_NONE, false
	}
	ef, err := elf.NewFile(f)
	if err != nil {
		return elf.EM_NONE, true
	}
	return ef.Machine, true
}

// jukaosCheckELFPair refuses an ELF source built for another machine than
// the file it covers (or, over a non-ELF file, than this machine).
func jukaosCheckELFPair(h jukaosHost, src, realDst string) error {
	srcM, srcELF := jukaosELFMachineOf(src)
	if !srcELF {
		return nil
	}
	want, dstELF := jukaosELFMachineOf(realDst)
	if !dstELF || want == elf.EM_NONE {
		want = jukaosMachineELF(h.Machine)
	}
	if srcM == elf.EM_NONE {
		return errors.New("source is a damaged ELF file")
	}
	if want != elf.EM_NONE && srcM != want {
		return fmt.Errorf("source is built for %s, the file it covers for %s", jukaosELFName(srcM), jukaosELFName(want))
	}
	return nil
}

// jukaosELFName is a short name for an ELF machine.
func jukaosELFName(m elf.Machine) string {
	switch m {
	case elf.EM_AARCH64:
		return "aarch64"
	case elf.EM_ARM:
		return "32-bit ARM"
	case elf.EM_X86_64:
		return "x86_64"
	case elf.EM_386:
		return "x86"
	}
	return strings.TrimPrefix(m.String(), "EM_")
}

// ── Kernel modules ─────────────────────────────────────────────────────────

// jukaosModinfo reads the key=value strings of an ELF kernel module's
// .modinfo section, and the module's machine. A key seen twice keeps the
// first value (vermagic, name and license appear once).
func jukaosModinfo(r io.ReaderAt) (map[string]string, elf.Machine, error) {
	f, err := elf.NewFile(r)
	if err != nil {
		return nil, elf.EM_NONE, errors.New("not a kernel module (no ELF header)")
	}
	sec := f.Section(".modinfo")
	if sec == nil {
		return nil, f.Machine, errors.New("not a kernel module (no .modinfo)")
	}
	if sec.Size > 1<<20 {
		return nil, f.Machine, errors.New(".modinfo is too large")
	}
	data, err := sec.Data()
	if err != nil {
		return nil, f.Machine, fmt.Errorf(".modinfo unreadable: %v", err)
	}
	info := map[string]string{}
	for _, field := range bytes.Split(data, []byte{0}) {
		key, value, ok := strings.Cut(string(field), "=")
		if !ok || key == "" {
			continue
		}
		if _, seen := info[key]; !seen {
			info[key] = value
		}
	}
	return info, f.Machine, nil
}

// jukaosVermagicFits reports whether a module's vermagic ("4.9.191 SMP
// preempt mod_unload aarch64") was built for this kernel: the release must
// match exactly, and an architecture word in it must be this machine's.
func jukaosVermagicFits(vermagic, release, machine string) (bool, string) {
	fields := strings.Fields(vermagic)
	if len(fields) == 0 {
		return false, "the driver has no vermagic"
	}
	if release == "" {
		return false, "this kernel's version is unknown"
	}
	if fields[0] != release {
		return false, fmt.Sprintf("built for kernel %s, this is %s", fields[0], release)
	}
	for _, word := range fields[1:] {
		fam := jukaosArchFamily(word)
		switch fam {
		case "aarch64", "x86_64", "x86", "riscv64":
			if fam != jukaosArchFamily(machine) {
				return false, fmt.Sprintf("built for %s, this is %s", word, machine)
			}
		}
	}
	return true, ""
}

// jukaosModuleName is the name the kernel lists a module under: .modinfo's
// "name", else the file name without .ko, dashes as underscores.
func jukaosModuleName(info map[string]string, file string) string {
	name := strings.TrimSpace(info["name"])
	if name == "" {
		name = strings.TrimSuffix(path.Base(filepath.ToSlash(file)), ".ko")
	}
	return strings.ReplaceAll(name, "-", "_")
}

// jukaosCheckModule checks one .ko of a layer: inside it, an ELF module for
// this machine, built for this exact kernel.
func jukaosCheckModule(h jukaosHost, base, rel string) (jukaosModule, error) {
	if !strings.HasSuffix(rel, ".ko") {
		return jukaosModule{}, errors.New("must be a .ko file")
	}
	p, err := jukaosInsideLayer(base, rel)
	if err != nil {
		return jukaosModule{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return jukaosModule{}, errors.New("unreadable")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > jukaosModuleMax {
		return jukaosModule{}, errors.New("not a regular file of a sensible size")
	}
	info, machine, err := jukaosModinfo(f)
	if err != nil {
		return jukaosModule{}, err
	}
	if want := jukaosMachineELF(h.Machine); want != elf.EM_NONE && machine != want {
		return jukaosModule{}, fmt.Errorf("built for %s, this is %s", jukaosELFName(machine), h.Machine)
	}
	vermagic := strings.TrimSpace(info["vermagic"])
	if ok, why := jukaosVermagicFits(vermagic, h.Release, h.Machine); !ok {
		return jukaosModule{}, errors.New(why)
	}
	return jukaosModule{Path: p, Name: jukaosModuleName(info, rel), Vermagic: vermagic}, nil
}

// ── sysfs / proc values ────────────────────────────────────────────────────

// jukaosSysfsAllowed reports whether a layer may set this value: the memory
// manager's tunables, the scheduler's, a block device's queue, transparent
// huge pages and module parameters. The CPU governor and clocks are not on
// the list — JukaHub's power modes own them and would undo a layer's choice.
func jukaosSysfsAllowed(p string) bool {
	if p != path.Clean(p) || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " \t\n\x00\\") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	switch {
	case len(parts) == 4 && parts[0] == "proc" && parts[1] == "sys" && parts[2] == "vm":
		return parts[3] != "drop_caches" && parts[3] != "compact_memory"
	case len(parts) == 4 && parts[0] == "proc" && parts[1] == "sys" && parts[2] == "kernel":
		return strings.HasPrefix(parts[3], "sched_")
	case len(parts) == 5 && parts[0] == "sys" && parts[1] == "block" && parts[3] == "queue":
		switch parts[4] {
		case "scheduler", "read_ahead_kb", "nr_requests", "add_random", "iostats":
			return true
		}
	case len(parts) == 5 && parts[0] == "sys" && parts[1] == "kernel" && parts[2] == "mm" && parts[3] == "transparent_hugepage":
		return parts[4] == "enabled" || parts[4] == "defrag"
	case len(parts) == 5 && parts[0] == "sys" && parts[1] == "module" && parts[3] == "parameters":
		return true
	}
	return false
}

// jukaosCheckSysfs checks one path and value of a layer's "sysfs".
func jukaosCheckSysfs(p, value string) error {
	if !jukaosSysfsAllowed(p) {
		return errors.New("not one of the values a layer may set")
	}
	v := strings.TrimSpace(value)
	if v == "" || len(v) > jukaosSysfsValueMax || strings.ContainsAny(v, "\n\r\x00") {
		return errors.New("value must be one short line")
	}
	return nil
}

// jukaosSysfsCurrent is a value as read back: trimmed, and for a choice file
// ("noop [deadline] cfq") the bracketed choice.
func jukaosSysfsCurrent(raw string) string {
	v := strings.TrimSpace(raw)
	if i := strings.IndexByte(v, '['); i >= 0 {
		if j := strings.IndexByte(v[i:], ']'); j > 1 {
			return v[i+1 : i+j]
		}
	}
	return v
}

// jukaosSysfsSame reports whether a value as read back is the one written: a
// yes/no module parameter is written as 1/0 or y/n but reads back as Y or N.
func jukaosSysfsSame(read, written string) bool {
	a, b := jukaosSysfsCurrent(read), jukaosSysfsCurrent(written)
	if a == b {
		return true
	}
	yesNo := func(v string) string {
		switch strings.ToLower(v) {
		case "y", "1":
			return "y"
		case "n", "0":
			return "n"
		}
		return ""
	}
	return yesNo(a) != "" && yesNo(a) == yesNo(b)
}

// ── The journal ────────────────────────────────────────────────────────────

// jukaosEncodeJournal is the journal's file form.
func jukaosEncodeJournal(j jukaosJournal) ([]byte, error) {
	if j.Entries == nil {
		j.Entries = []jukaosJournalEntry{}
	}
	return json.MarshalIndent(j, "", "  ")
}

// jukaosDecodeJournal reads a journal; entries of an unknown kind or without
// a path are dropped (they cannot be undone safely), as is everything past
// jukaosJournalLimit.
func jukaosDecodeJournal(data []byte) (jukaosJournal, error) {
	var j jukaosJournal
	if len(data) > jukaosJournalMax {
		return j, errors.New("journal is too large")
	}
	if err := json.Unmarshal(data, &j); err != nil {
		return jukaosJournal{}, err
	}
	kept := j.Entries[:0]
	for _, e := range j.Entries {
		if len(kept) >= jukaosJournalLimit {
			break
		}
		switch e.Kind {
		case jukaosKindModule:
			if !jukaosValidID(e.Name) || strings.Contains(e.Name, ".") {
				continue
			}
		case jukaosKindSysfs:
			if !jukaosSysfsAllowed(e.Path) {
				continue
			}
		default:
			continue
		}
		kept = append(kept, e)
	}
	j.Entries = kept
	return j, nil
}

// jukaosRevertOps are the effects a revert needs; the device's are in
// jukaos_linux.go, the tests pass fakes.
type jukaosRevertOps struct {
	ReadValue    func(p string) (string, error)
	WriteValue   func(p, v string) error
	UnloadModule func(name string) error
}

// jukaosRevertEntries undoes a journal's entries newest first and reports
// one line per entry. A value someone else changed since (it no longer reads
// as what the session wrote) is left as it is.
func jukaosRevertEntries(entries []jukaosJournalEntry, ops jukaosRevertOps) []string {
	var report []string
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		switch e.Kind {
		case jukaosKindSysfs:
			if ops.ReadValue == nil || ops.WriteValue == nil {
				continue
			}
			cur, err := ops.ReadValue(e.Path)
			if err != nil {
				report = append(report, fmt.Sprintf("%s: unreadable (%v)", e.Path, err))
				continue
			}
			if !jukaosSysfsSame(cur, e.New) {
				report = append(report, fmt.Sprintf("%s: changed since (%s), left alone", e.Path, jukaosSysfsCurrent(cur)))
				continue
			}
			if err := ops.WriteValue(e.Path, e.Old); err != nil {
				report = append(report, fmt.Sprintf("%s: not put back (%v)", e.Path, err))
				continue
			}
			report = append(report, fmt.Sprintf("%s: back to %s", e.Path, e.Old))
		case jukaosKindModule:
			if ops.UnloadModule == nil {
				continue
			}
			if err := ops.UnloadModule(e.Name); err != nil {
				report = append(report, fmt.Sprintf("driver %s: still loaded (%v); a reboot removes it", e.Name, err))
				continue
			}
			report = append(report, fmt.Sprintf("driver %s: unloaded", e.Name))
		}
	}
	return report
}

// ── The running session ────────────────────────────────────────────────────

// jukaosLive is this process's view of the session. Fields after init are
// guarded by mu.
var jukaosLive struct {
	mu        sync.Mutex
	session   bool // this JukaHub runs as JukaHub OS
	guarded   bool // the OS guard left the layers out
	layers    []string
	info      jukaosSessionInfo
	modules   int // modules this session loaded
	healthy   *time.Timer
	escapeArm time.Time // "Return to …" row's first press
}

// jukaosSessionActive reports whether this JukaHub runs as JukaHub OS.
func jukaosSessionActive() bool {
	jukaosLive.mu.Lock()
	defer jukaosLive.mu.Unlock()
	return jukaosLive.session
}

// jukaosEnvList splits a ":"-separated list, dropping empty entries.
func jukaosEnvList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ":") {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return out
}

// jukaosChildEnv is a game's environment in an OS session: the library
// folders of the layers marked "children" go first on LD_LIBRARY_PATH (the
// last occurrence, which is the one a child sees). JukaHub OS's own variables
// never reach a child. Outside a session only that removal happens.
func jukaosChildEnv(environ []string) []string {
	session, childLibs := "", ""
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, jukaosEnvPrefix) {
			switch key {
			case jukaosEnvSession:
				session = value
			case jukaosEnvChildLibs:
				childLibs = value
			}
			continue
		}
		out = append(out, kv)
	}
	libs := jukaosEnvList(childLibs)
	if session != "1" || len(libs) == 0 {
		return out
	}
	last := -1
	for i, kv := range out {
		if key, _, ok := strings.Cut(kv, "="); ok && key == "LD_LIBRARY_PATH" {
			last = i
		}
	}
	prefix := strings.Join(libs, ":")
	if last < 0 {
		return append(out, "LD_LIBRARY_PATH="+prefix)
	}
	_, value, _ := strings.Cut(out[last], "=")
	kept := []string{}
	have := map[string]bool{}
	for _, dir := range libs {
		have[filepath.Clean(dir)] = true
	}
	for _, part := range strings.Split(value, ":") {
		if strings.TrimSpace(part) == "" || have[filepath.Clean(part)] {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) > 0 {
		prefix += ":" + strings.Join(kept, ":")
	}
	out[last] = "LD_LIBRARY_PATH=" + prefix
	return out
}

// jukaosSplitIDs reads JUKAHUB_OS_LAYERS.
func jukaosSplitIDs(v string) []string {
	var out []string
	for _, id := range strings.Split(v, ",") {
		if id = strings.TrimSpace(id); jukaosValidID(id) {
			out = append(out, id)
		}
	}
	return out
}

// jukaosEnabledLayers is the layers of all that state switches on and that
// are usable.
func jukaosEnabledLayers(all []jukaosLayer, s jukaosState) []jukaosLayer {
	var out []jukaosLayer
	for _, l := range all {
		if s.Layers[l.ID] && l.Problem == "" {
			out = append(out, l)
		}
	}
	return out
}

// jukaosBuiltinLayer is the generated layer "JukaHub libraries for games":
// the libs/ folder beside JukaHub (where launch.sh already looks first for
// JukaHub itself) handed to the games and apps it starts as well, so a newer
// SDL2 or similar dropped there reaches them with one switch. Off by default.
func jukaosBuiltinLayer(appDir string) jukaosLayer {
	dir := filepath.Join(appDir, "libs")
	l := jukaosLayer{
		ID: jukaosBuiltinLibsID, Dir: dir, Builtin: true,
		Spec: jukaosLayerSpec{
			Name:        "JukaHub libraries for games",
			Description: "Games and apps use the libraries in JukaHub's libs folder (a newer SDL2 and others) before the system's",
			Children:    true,
		},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		l.Problem = "no libs folder beside JukaHub yet: copy newer .so files into libs/"
		return l
	}
	for _, e := range entries {
		if !e.IsDir() && strings.Contains(e.Name(), ".so") {
			l.LibDirs = []string{dir}
			return l
		}
	}
	l.Problem = "libs/ holds no libraries (.so files) yet"
	return l
}

// jukaosSDRootOf is the card JukaHub is installed on (<SD>/Apps/JukaHub), as a
// system path; "" when the app is not in an Apps folder.
func jukaosSDRootOf(appDir string) string {
	apps := filepath.Dir(appDir)
	if !strings.EqualFold(filepath.Base(apps), "Apps") {
		return ""
	}
	return filepath.ToSlash(filepath.Dir(apps))
}
