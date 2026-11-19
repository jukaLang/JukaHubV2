package main

// pkglaunch.go starts installed packages. A package in the JukaLang registry
// is an app: on a TrimUI handheld a folder with config.json and launch.sh,
// which its archive keeps at the top, under <Name>/ or under Apps/<Name>/.
// The store used to offer only "Open folder" for it. pkgFindLaunch finds the
// program once per scan of the card (off the render thread), the Packages
// screen offers Run (Play for games) when it found one, and the press starts it
// through launchInstalledApp — the same guarded launcher the Apps screen uses,
// which counts an installed package folder as an app folder
// (pkgLaunchablePath). The same search lists the installed packages on the
// Apps screen (pkgAppEntries).

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"
)

// pkgDefaultMain is the program a TrimUI app starts with. Every registry
// entry names it as its "mainfilename" today, and a folder installed before
// the marker kept that name is searched for it too.
const pkgDefaultMain = "launch.sh"

// pkgLaunchMaxDepth and pkgLaunchMaxEntries bound the search inside one
// package folder: Apps/<Name>/launch.sh is two folders down, and a package
// that ships a folder of ROMs cannot stall the scan. The Apps screen scans on
// the main thread, so its walks share pkgAppsScanBudget entries, at most
// pkgAppsScanPerPackage of them for one package.
const (
	pkgLaunchMaxDepth     = 4
	pkgLaunchMaxEntries   = 4000
	pkgAppsScanBudget     = 3000
	pkgAppsScanPerPackage = 800
)

// pkgRunScripts says whether a package's .sh program can start here: TrimUI
// apps start arm64 Linux programs, so a PC (or an x86 Linux build) offers Run
// only for the Windows kinds. A variable so a test can exercise the handheld
// path anywhere.
var pkgRunScripts = runtime.GOOS == "linux" && runtime.GOARCH == "arm64"

// pkgLaunchSkipDirs are TrimUI SD-card folders a package archive may carry
// that hold no app: an emulator's launch.sh under Emus/ wants a ROM to start.
var pkgLaunchSkipDirs = map[string]bool{"emus": true, "roms": true, "imgs": true, "bios": true}

// pkgLaunchTarget is how an installed package starts: the program (an
// absolute path), the name to show for it and the app folder's icon ("" when
// it has none).
type pkgLaunchTarget struct {
	Path  string
	Label string
	Icon  string
}

// pkgLaunchRoot is the packages folder. packageInstallDir creates the folder;
// a launch guard and the Apps scan must not, so they ask this instead.
func pkgLaunchRoot() string {
	if pkgInstallDirOverride != "" {
		return pkgInstallDirOverride
	}
	return filepath.Join(mustExecutableDir(), "packages")
}

// pkgLaunchablePath reports whether path lies inside an installed package
// folder: below a folder of the packages folder that is listed under its own
// name (so a Windows 8.3 alias of a dot-folder does not pass), and not one of
// the store's own dot-folders (the staging area, rollback copies, the trash,
// the artwork cache), whose copies are never the installed app.
func pkgLaunchablePath(p string) bool {
	root := pkgLaunchRoot()
	if !isPathWithin(p, root) {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	first, rest, nested := strings.Cut(filepath.ToSlash(rel), "/")
	if !nested || rest == "" || first == "" || first == ".." || skipScanEntryName(first) {
		return false
	}
	entries, err := os.ReadDir(absRoot)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || skipScanEntryName(e.Name()) {
			continue
		}
		if e.Name() == first || (runtime.GOOS == "windows" && strings.EqualFold(e.Name(), first)) {
			return true
		}
	}
	return false
}

// pkgUnderLaunchRoot reports whether p names something inside the packages
// folder however it is spelled: the TrimUI SD card does not tell "packages"
// from "Packages" or from its 8.3 alias, so besides the lexical test every
// folder above p is compared with the packages folder itself. A launch guard
// asks it first, and pkgLaunchablePath (which only accepts the exact
// spelling) then has the last word.
func pkgUnderLaunchRoot(p string) bool {
	root := pkgLaunchRoot()
	if isPathWithin(p, root) {
		return true
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	for dir := filepath.Dir(abs); ; {
		if info, err := os.Stat(dir); err == nil && os.SameFile(info, rootInfo) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// pkgRunnableHere reports whether launchInstalledApp can start a program of
// this kind on this system (see pkgRunScripts).
func pkgRunnableHere(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	if runtime.GOOS == "windows" {
		switch ext {
		case ".exe", ".bat", ".cmd", ".lnk", ".url":
			return true
		}
		return false
	}
	return ext == ".sh" && pkgRunScripts
}

// pkgMainNames is what a package's program may be called: the registry's
// mainfilename (kept in the folder's marker at install) when it is a plain
// relative name, and launch.sh. Names compare as slash paths.
func pkgMainNames(main string) []string {
	names := make([]string, 0, 2)
	if m := strings.TrimSpace(main); m != "" && filepath.IsLocal(filepath.FromSlash(m)) {
		names = append(names, filepath.ToSlash(filepath.Clean(filepath.FromSlash(m))))
	}
	if len(names) == 0 || !strings.EqualFold(names[0], pkgDefaultMain) {
		names = append(names, pkgDefaultMain)
	}
	return names
}

// pkgMainMatch reports whether a file, by its slash path below the package
// folder, is one of names: a name with a folder in it must match the whole
// path, a bare name any file of that name.
func pkgMainMatch(rel string, names []string) bool {
	base := path.Base(rel)
	for _, n := range names {
		if strings.Contains(n, "/") {
			if strings.EqualFold(rel, n) {
				return true
			}
			continue
		}
		if strings.EqualFold(base, n) {
			return true
		}
	}
	return false
}

// pkgFoldName keeps only the letters and digits of a name, lower-cased, so
// "Formula 1 G&W", "Formula_1_G&W" and "formula1gw" compare equal.
func pkgFoldName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// pkgLaunchCand is one program pkgFindLaunch could start.
type pkgLaunchCand struct {
	t     pkgLaunchTarget
	app   bool // an app folder (config.json) declared it
	named bool // its app or folder is named like the package
	depth int
	rel   string
}

// pkgLaunchBetter orders candidates: an app folder's declared program before a
// bare script, the one named like the package first, then the shallowest,
// then the first by path, so the choice is the same on every scan.
func pkgLaunchBetter(a, b pkgLaunchCand) bool {
	if a.app != b.app {
		return a.app
	}
	if a.named != b.named {
		return a.named
	}
	if a.depth != b.depth {
		return a.depth < b.depth
	}
	return a.rel < b.rel
}

// pkgLaunchFound is what a search of one package folder found. Run is the
// program the store's Run button starts; ok is false when nothing in the
// folder can start here, and also when the folder holds several apps and none
// is named like the package (a bundle: Run would have to guess). Apps lists
// every app folder's program, so the Apps screen can offer each app of a
// bundle. Seen is how many entries the search looked at.
type pkgLaunchFound struct {
	Run  pkgLaunchTarget
	OK   bool
	Apps []pkgLaunchTarget
	Seen int
}

// pkgFindLaunch searches a package folder, looking at no more than limit
// entries. An app folder (config.json naming its launch script, apps.go's
// scanAppSubdir) is one app: its program is taken and the folder is not
// searched further. Otherwise a file named like the package's main file
// counts. With nothing found the store offers Open folder only.
func pkgFindLaunch(dir, name, main string, limit int) pkgLaunchFound {
	names := pkgMainNames(main)
	want := pkgFoldName(name)
	isNamed := func(labels ...string) bool {
		if want == "" {
			return false
		}
		for _, n := range labels {
			if pkgFoldName(n) == want {
				return true
			}
		}
		return false
	}
	var found pkgLaunchFound
	var cands []pkgLaunchCand
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil // an unreadable entry: skip it
		}
		found.Seen++
		if found.Seen > limit {
			return filepath.SkipAll
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		depth := 0
		if rel != "." {
			depth = strings.Count(rel, "/") + 1
		}
		if d.IsDir() {
			if p != dir && (skipScanEntryName(d.Name()) || depth > pkgLaunchMaxDepth || pkgLaunchSkipDirs[strings.ToLower(d.Name())]) {
				return filepath.SkipDir
			}
			// An app folder is one app even when its program cannot start
			// here: a helper script inside it is never the package's program.
			if app, ok := scanAppSubdir(p); ok {
				if pkgRunnableHere(app.Path) {
					cands = append(cands, pkgLaunchCand{
						t:     pkgLaunchTarget{Path: app.Path, Label: app.Name, Icon: app.Icon},
						app:   true,
						named: isNamed(app.Name, d.Name()),
						depth: depth,
						rel:   rel,
					})
				}
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || skipScanEntryName(d.Name()) {
			return nil
		}
		if !pkgMainMatch(rel, names) || !pkgRunnableHere(p) {
			return nil
		}
		folder := filepath.Dir(p)
		label := filepath.Base(folder)
		if folder == dir {
			label = name
		}
		icon := iconInFolder(folder)
		if icon == "" {
			icon = sidecarIcon(p)
		}
		cands = append(cands, pkgLaunchCand{
			t:     pkgLaunchTarget{Path: p, Label: label, Icon: icon},
			named: folder == dir || isNamed(label),
			depth: depth,
			rel:   rel,
		})
		return nil
	})
	if len(cands) == 0 {
		return found
	}
	sort.Slice(cands, func(i, j int) bool { return pkgLaunchBetter(cands[i], cands[j]) })
	apps := 0
	for _, c := range cands {
		if c.app {
			apps++
			found.Apps = append(found.Apps, c.t)
		}
	}
	best := cands[0]
	if best.app && !best.named && (apps > 1 || found.Seen > limit) {
		// A bundle of apps, none of them the package itself — or a search
		// cut short that cannot tell whether more apps follow.
		return found
	}
	found.Run, found.OK = best.t, true
	return found
}

// pkgInstalledName is the store's name for the installed folder of key and
// its registry main file: the marker's, else the folder name.
func pkgInstalledName(dir, key string) (name, main string) {
	name = strings.ReplaceAll(key, "_", " ")
	if m, ok := pkgReadMarker(dir); ok {
		if n := strings.TrimSpace(m.Name); n != "" {
			name = n
		}
		main = m.Main
	}
	return name, main
}

// pkgFindInstalledLaunch is the program Run starts for the installed folder
// of key, labelled with the package's name so it reads like the store card.
func pkgFindInstalledLaunch(dir, key string) (pkgLaunchTarget, bool) {
	name, main := pkgInstalledName(dir, key)
	f := pkgFindLaunch(dir, name, main, pkgLaunchMaxEntries)
	if !f.OK {
		return pkgLaunchTarget{}, false
	}
	t := f.Run
	t.Label = name
	if t.Icon == "" {
		t.Icon = iconInFolder(dir)
	}
	return t, true
}

// pkgAppEntries lists the installed packages that can start here as Apps
// rows, so an app installed from Packages is on the Apps screen too: one row
// named like the package, or one row per app of a bundle. It runs on the
// main thread, so the walks share one budget of entries.
func pkgAppEntries() []FileEntry {
	root := pkgLaunchRoot()
	items, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []FileEntry
	budget := pkgAppsScanBudget
	for _, item := range items {
		if budget <= 0 {
			break
		}
		if !item.IsDir() || skipScanEntryName(item.Name()) {
			continue
		}
		dir := filepath.Join(root, item.Name())
		name, main := pkgInstalledName(dir, item.Name())
		f := pkgFindLaunch(dir, name, main, min(budget, pkgAppsScanPerPackage))
		budget -= f.Seen
		switch {
		case f.OK:
			icon := f.Run.Icon
			if icon == "" {
				icon = iconInFolder(dir)
			}
			out = append(out, FileEntry{Name: name, Path: f.Run.Path, Icon: icon})
		case len(f.Apps) > 0:
			for _, a := range f.Apps {
				out = append(out, FileEntry{Name: a.Label, Path: a.Path, Icon: a.Icon})
			}
		}
	}
	// By name, then by path: collectAppEntries keeps the first of two rows
	// with one name, and that must be the same row on every scan.
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// pkgRunLabel is the Run button's label: Play for a game, Run for the rest.
func pkgRunLabel(it *pkgItem) string {
	if it != nil && it.Category == "games" {
		return "Play"
	}
	return "Run"
}

// pkgRun starts an installed package's program. A press on a package that is
// being removed or rolled back opens its sheet instead (pkgPrimaryAction); a
// program that is gone since the last scan has the card read again, so the
// button goes back to Open folder.
func pkgRun(config *Config, it *pkgItem) {
	_ = config
	if it == nil || it.Kind == pkgKindApp || !it.Installed || it.Launch == "" {
		return
	}
	if pkgBusyKey(it.Key) {
		PlayErrorSound()
		return
	}
	PlayActivateSound()
	logInfo("packages", "running %s (%s)", it.Pkg.Name, it.Launch)
	if !launchInstalledApp(it.Launch) {
		runSceneTask(pkgLoadLocal)
	}
}
