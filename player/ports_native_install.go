package main

// ports_native_install.go — JukaHub installs PortMaster ports itself: no
// Python, and PortMaster's app is never opened for it.
//
// Juka (2026-11-20): "Can we try to bypass PortMaster? … download required
// files directly via PortMaster API … launch the apps directly without
// needing to use PortMaster?" Ports already start without PortMaster's app:
// JukaHub runs a port's own script (ports_env.go), which needs PortMaster's
// folder of shared files (control.txt, gptokeyb, the runtimes in libs/) but
// never its menu. Browsing and downloading were JukaHub's too (the Ports
// Store reads PortMaster's catalogue, ports.json, and checks every zip against
// the MD5 it lists). What still went through PortMaster was the install
// itself: its Python installer (harbourmaster), or its menu (pugwash) reading
// the autoinstall folder — both need Python, which a stock TrimUI lacks, and
// the menu is where testers sat on a splash. So JukaHub now does
// harbourmaster's `install <zip>` itself:
//
//   - the zip is read first: launcher scripts at its top, the port's
//     folder(s), the port.json inside. Paths that climb out (`..`, absolute,
//     drive letters), a PortMaster folder (that is PortMaster's own update),
//     more than pnMaxEntries files or pnMaxTotal bytes refuse the whole zip
//     before anything is written; links and stray top-level files are left
//     out; the card must have room for it;
//   - folders go to PortMaster's ports folder and scripts to its scripts
//     folder: HM_PORTS_DIR / HM_SCRIPTS_DIR when PortMaster's own launcher
//     sets them, else where the card's installed ports are, else Data/ports
//     (harbourmaster's TrimUI default for both; CrossMix keeps scripts in
//     Roms/PORTS). An update goes where the port already is, and a script
//     with copies in several ports folders is updated in each;
//   - each file is written beside its target and renamed over it, so a file
//     is old or new, never half; files the zip does not have (saves, game
//     data the player copied in) stay, as with harbourmaster. The zip's CRC
//     is checked as each file is read;
//   - port.json gets the "status" block harbourmaster writes (source, the
//     zip's md5, "Installed"), so PortMaster, the update check and Uninstall
//     see the port as installed by PortMaster;
//   - each runtime the port declares (attr.runtime) that is not in
//     PortMaster's libs/ is downloaded from where PortMaster's catalogue
//     lists it (ports.json "utils"), and must match the MD5 listed there.
//
// The install queue (ports_store_install.go) tries this first and falls back
// to harbourmaster, then PortMaster's app, only for a zip it refuses. A port
// whose runtime is missing gets it before it starts (pnRuntimeGate): its
// script would otherwise call harbourmaster's runtime_check, which needs
// Python. Zips left in PortMaster's autoinstall folder are installed the same
// way when the Ports panel opens (pnAutoinstallSweep).

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// pnMaxEntries and pnMaxTotal bound what one zip may write.
	pnMaxEntries = 100000
	pnMaxTotal   = 16 << 30
	// pnFreeMargin is room left on the card after an install.
	pnFreeMargin = 64 << 20
	// pnRuntimeMax bounds one runtime download.
	pnRuntimeMax = 4 << 30
	// pnStatusSource is the "source" harbourmaster records for PortMaster's
	// own catalogue.
	pnStatusSource = "PortMaster"
)

// errPnNotAPort is a zip JukaHub leaves to PortMaster's own installer.
var errPnNotAPort = errors.New("not a port zip JukaHub installs itself")

// pnFile is one file of the zip that is written, at rel (a slash path under
// the ports folder).
type pnFile struct {
	f   *zip.File
	rel string
}

// pnPlan is what a port zip holds, checked.
type pnPlan struct {
	scripts  []pnFile // launcher scripts at the top (rel is the name)
	dirs     []string // top-level folders, in zip order
	files    []pnFile // files inside them
	empty    []string // folders inside them the zip lists (rel), made even when empty
	manifest string   // rel of port.json, else of an older <name>.port.json ("" none)
	total    uint64   // bytes to write
	largest  uint64   // the biggest file (an update needs room for one temporary copy)
	skipped  int      // links and stray top-level files left out
}

// add counts one file to write.
func (p *pnPlan) add(f *zip.File) {
	p.total += f.UncompressedSize64
	if f.UncompressedSize64 > p.largest {
		p.largest = f.UncompressedSize64
	}
}

// pnCleanName is a zip entry's name as a slash path, or ok false when it
// could land outside its folder (absolute, a drive letter, "..", an empty or
// "." element, NUL). A trailing "/" (a folder entry) is kept. Pure.
func pnCleanName(name string) (string, bool) {
	n := strings.ReplaceAll(name, `\`, "/")
	for strings.HasPrefix(n, "./") {
		n = n[2:]
	}
	if n == "" || strings.HasPrefix(n, "/") || strings.ContainsAny(n, "\x00:") {
		return "", false
	}
	for _, part := range strings.Split(strings.TrimSuffix(n, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	return n, true
}

// pnReservedTop reports a top-level name a port zip may not use: PortMaster's
// own folder or launcher, or the firmware's picture folder. FAT and exFAT
// drop trailing dots and spaces, so "PortMaster." is PortMaster too, and any
// name ending in one could land on another port's folder. Pure.
func pnReservedTop(name string) bool {
	if strings.TrimRight(name, ". ") != name {
		return true
	}
	for _, r := range []string{"PortMaster", "PortMaster.sh", "Imgs"} {
		if strings.EqualFold(name, r) {
			return true
		}
	}
	return false
}

// pnPlanZip checks a port zip's entries and sorts them into a plan. An unsafe
// path, a reserved top-level name (PortMaster's own update) refuses the whole
// zip (errPnNotAPort, so the queue can hand it to PortMaster); so does a zip
// without a launcher script. Pure.
func pnPlanZip(files []*zip.File) (pnPlan, error) {
	var p pnPlan
	seenDir := map[string]bool{}
	seenScript := map[string]bool{}
	alt := ""
	for _, f := range files {
		n, ok := pnCleanName(f.Name)
		if !ok {
			return pnPlan{}, fmt.Errorf("%w: %q has an unsafe path", errPnNotAPort, f.Name)
		}
		top, rest, nested := strings.Cut(n, "/")
		if strings.EqualFold(top, "__MACOSX") {
			continue
		}
		if pnReservedTop(top) {
			return pnPlan{}, fmt.Errorf("%w: it holds %q (PortMaster itself, or a name the card cannot keep)", errPnNotAPort, top)
		}
		if !nested {
			switch {
			case strings.EqualFold(path.Ext(top), ".sh") && !strings.HasPrefix(top, ".") && f.Mode()&os.ModeType == 0:
				if !seenScript[strings.ToLower(top)] {
					seenScript[strings.ToLower(top)] = true
					p.scripts = append(p.scripts, pnFile{f: f, rel: top})
					p.add(f)
				}
			default:
				p.skipped++
			}
			continue
		}
		if strings.HasPrefix(top, ".") {
			p.skipped++
			continue
		}
		if !seenDir[strings.ToLower(top)] {
			seenDir[strings.ToLower(top)] = true
			p.dirs = append(p.dirs, top)
		}
		if rest == "" || strings.HasSuffix(n, "/") || f.FileInfo().IsDir() {
			if rest = strings.TrimSuffix(rest, "/"); rest != "" {
				p.empty = append(p.empty, strings.TrimSuffix(n, "/"))
			}
			continue
		}
		if f.Mode()&os.ModeType != 0 { // a link, a device…
			p.skipped++
			continue
		}
		p.add(f)
		p.files = append(p.files, pnFile{f: f, rel: n})
		if len(p.files) > pnMaxEntries || p.total > pnMaxTotal {
			return pnPlan{}, fmt.Errorf("it unpacks to more than %d files or %d GB", pnMaxEntries, pnMaxTotal>>30)
		}
		switch low := strings.ToLower(rest); {
		case p.manifest == "" && low == "port.json":
			p.manifest = n
		case alt == "" && strings.HasSuffix(low, ".port.json") && !strings.Contains(low, "/"):
			alt = n // harbourmaster's older <name>.port.json
		}
	}
	if p.manifest == "" {
		p.manifest = alt
	}
	if len(p.scripts) == 0 {
		return pnPlan{}, fmt.Errorf("%w: no launcher script at its top", errPnNotAPort)
	}
	return p, nil
}

// pnDirs is where a port goes.
type pnDirs struct {
	ports   string
	scripts string // for a script that has no copy on the card yet
	note    string // how they were chosen, for the install log
}

// pnHMPaths reads HM_PORTS_DIR and HM_SCRIPTS_DIR from PortMaster's own
// launchers (pstoreHMExports), resolving $NAME / ${NAME} from earlier
// exports. A value that is not an absolute plain path afterwards is dropped.
func pnHMPaths(control string) map[string]string {
	vals := map[string]string{"controlfolder": control}
	for _, line := range pstoreHMExports(control) {
		rest := strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(rest, "=")
		if !ok {
			continue
		}
		vals[name] = pnExpand(strings.Trim(value, `"`), vals)
	}
	out := map[string]string{}
	for _, k := range []string{"HM_PORTS_DIR", "HM_SCRIPTS_DIR"} {
		if v := vals[k]; strings.HasPrefix(v, "/") && !strings.Contains(v, "$") {
			out[k] = filepath.Clean(v)
		}
	}
	return out
}

// pnExpand replaces ${NAME} and $NAME with known values; unknown ones stay.
// Pure.
func pnExpand(s string, vals map[string]string) string {
	return os.Expand(s, func(name string) string {
		if v, ok := vals[name]; ok {
			return v
		}
		return "${" + name + "}"
	})
}

// pnCountRoots counts, per ports folder, the port folders (with a port.json)
// and the launcher scripts already on the card.
func pnCountRoots(roots []string) (dirs, scripts map[string]int) {
	dirs, scripts = map[string]int{}, map[string]int{}
	for _, root := range roots {
		items, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, it := range items {
			name := it.Name()
			switch {
			case strings.HasPrefix(name, "."):
			case it.IsDir():
				if regularFile(filepath.Join(root, name, "port.json")) {
					dirs[root]++
				}
			case strings.EqualFold(filepath.Ext(name), ".sh") && !strings.EqualFold(name, "PortMaster.sh"):
				scripts[root]++
			}
		}
	}
	return dirs, scripts
}

// pnMostUsed is the root with the highest count (the first of roots on a
// tie), or "" when all are zero. Pure.
func pnMostUsed(roots []string, counts map[string]int) string {
	best, n := "", 0
	for _, r := range roots {
		if counts[r] > n {
			best, n = r, counts[r]
		}
	}
	return best
}

// pnInstallDirs picks the ports and scripts folders for plan.
func pnInstallDirs(sdRoot, control string, plan pnPlan) pnDirs {
	roots := portRoots(sdRoot)
	def := filepath.Join(sdRoot, "Data", "ports")
	hm := pnHMPaths(control)
	dirCount, scriptCount := pnCountRoots(roots)
	var d pnDirs
	// An update goes where the port's folder already is.
	for _, root := range roots {
		for _, dir := range plan.dirs {
			if d.ports == "" && dirExists(filepath.Join(root, dir)) {
				d.ports, d.note = root, "ports: where "+dir+" already is"
			}
		}
	}
	switch {
	case d.ports != "":
	case hm["HM_PORTS_DIR"] != "":
		d.ports, d.note = hm["HM_PORTS_DIR"], "ports: HM_PORTS_DIR"
	case pnMostUsed(roots, dirCount) != "":
		d.ports, d.note = pnMostUsed(roots, dirCount), "ports: where installed ports are"
	default:
		d.ports, d.note = def, "ports: Data/ports (the TrimUI default)"
	}
	switch {
	case hm["HM_SCRIPTS_DIR"] != "":
		d.scripts, d.note = hm["HM_SCRIPTS_DIR"], d.note+"; scripts: HM_SCRIPTS_DIR"
	case pnMostUsed(roots, scriptCount) != "":
		d.scripts, d.note = pnMostUsed(roots, scriptCount), d.note+"; scripts: where installed scripts are"
	default:
		d.scripts, d.note = d.ports, d.note+"; scripts: beside the ports"
	}
	return d
}

// pnScriptTargets is every copy of script name on the card (any ports
// folder), else the one new place for it.
func pnScriptTargets(sdRoot, name, fallback string) []string {
	var out []string
	seen := map[string]bool{}
	for _, root := range append(portRoots(sdRoot), fallback) {
		items, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, it := range items {
			p := filepath.Join(root, it.Name())
			if !it.IsDir() && strings.EqualFold(it.Name(), name) && !seen[strings.ToLower(p)] {
				seen[strings.ToLower(p)] = true
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, filepath.Join(fallback, name))
	}
	return out
}

// pnCtxReader stops a copy when ctx ends.
type pnCtxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c pnCtxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}

// pnWriteZipFile writes f to dest through a temporary file beside it and a
// rename. progress gets the bytes written.
func pnWriteZipFile(ctx context.Context, f *zip.File, dest string, progress func(int64)) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return pnWriteReader(ctx, rc, int64(f.UncompressedSize64), f.Mode()&0o111 != 0 || strings.EqualFold(filepath.Ext(dest), ".sh"), dest, progress)
}

// pnWriteReader writes exactly size bytes of r to dest (temporary file, then
// rename).
func pnWriteReader(ctx context.Context, r io.Reader, size int64, exec bool, dest string, progress func(int64)) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// The zip entry name has already been sanitized by the caller; os.Root
	// additionally stops a symlink already in the ports folder redirecting the
	// staged write outside it (SEC-1).
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	tmpRel := fmt.Sprintf(".%s.jh%d.part", filepath.Base(dest), os.Getpid())
	out, err := root.OpenFile(tmpRel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	var w io.Writer = out
	if progress != nil {
		w = pnProgressWriter{w: out, fn: progress}
	}
	n, err := io.Copy(w, pnCtxReader{ctx: ctx, r: io.LimitReader(r, size+1)})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != size {
		err = fmt.Errorf("%s: the zip says %d bytes, it held %d", filepath.Base(dest), size, n)
	}
	if err != nil {
		_ = root.Remove(tmpRel)
		return err
	}
	if err := root.Rename(tmpRel, filepath.Base(dest)); err != nil {
		_ = root.Remove(tmpRel)
		return err
	}
	_ = os.Chmod(dest, mode) // FAT keeps no modes
	return nil
}

type pnProgressWriter struct {
	w  io.Writer
	fn func(int64)
}

func (p pnProgressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.fn(int64(n))
	return n, err
}

// pnStatusJSON is port.json with harbourmaster's "status" block set: the
// manifest's other fields are kept as they are. A missing or unreadable
// manifest becomes a minimal one naming the zip and its items. Pure.
func pnStatusJSON(raw []byte, zipName, sum string, items []string) ([]byte, error) {
	doc := map[string]interface{}{}
	if len(raw) > 0 {
		if err := json.Unmarshal(stripUTF8BOM(raw), &doc); err != nil || doc == nil {
			doc = map[string]interface{}{}
		}
	}
	if len(doc) == 0 {
		doc["version"] = 2
		doc["name"] = strings.ToLower(zipName)
		doc["items"] = items
		doc["items_opt"] = []string{}
		doc["attr"] = map[string]interface{}{"title": strings.TrimSuffix(zipName, filepath.Ext(zipName)), "runtime": []string{}}
	}
	status, _ := doc["status"].(map[string]interface{})
	if status == nil {
		status = map[string]interface{}{}
	}
	status["source"] = pnStatusSource
	status["md5"] = strings.ToLower(sum)
	status["status"] = "Installed"
	doc["status"] = status
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "    ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// pnResult is what an install did.
type pnResult struct {
	Scripts    []string // launcher scripts written
	PortsDir   string
	Runtimes   []string // runtimes downloaded
	RuntimeErr error    // a declared runtime that could not be fetched (the port is installed)
}

// pnInstall installs a checked port zip (portCatalogDownload or a caller's
// own MD5) the way harbourmaster would. zipName is the catalogue's name for
// it ("" uses the file's). gen is the status strip's run for progress.
// errPnNotAPort: the zip is left for PortMaster. Off the main thread.
func pnInstall(ctx context.Context, gen uint64, zipPath, zipName string) (res pnResult, err error) {
	if runtime.GOOS != "linux" {
		return res, errors.New("ports install on the handheld")
	}
	control := portRealControlFolder()
	if control == "" {
		// PortMaster's own installer may know its folder better.
		return res, fmt.Errorf("%w: PortMaster's folder (control.txt) is not where JukaHub looks", errPnNotAPort)
	}
	zr, oerr := zip.OpenReader(zipPath)
	if oerr != nil {
		// Go hands back the reader with ErrInsecurePath too: closed either way.
		if zr != nil {
			zr.Close()
		}
		if errors.Is(oerr, zip.ErrFormat) || errors.Is(oerr, zip.ErrAlgorithm) || errors.Is(oerr, zip.ErrInsecurePath) {
			return res, fmt.Errorf("%w: %v", errPnNotAPort, oerr)
		}
		return res, oerr
	}
	defer zr.Close()
	plan, err := pnPlanZip(zr.File)
	if err != nil {
		return res, err
	}
	sum, err := mcFileMD5(zipPath)
	if err != nil {
		return res, err
	}
	if zipName == "" {
		zipName = filepath.Base(zipPath)
	}
	dirs := pnInstallDirs(portMasterSDRoot, control, plan)
	// Nothing may land on PortMaster's own folder or around it.
	for _, d := range append(append([]string(nil), plan.dirs...), "") {
		target := filepath.Join(dirs.ports, d)
		if d == "" {
			target = dirs.scripts
		}
		if pnPathWithin(control, target) || (d != "" && pnPathWithin(target, control)) {
			return res, fmt.Errorf("%w: %s would overlap PortMaster's folder", errPnNotAPort, target)
		}
	}
	if err := os.MkdirAll(dirs.ports, 0o755); err != nil {
		return res, err
	}
	// A new port needs room for all of it; an update mostly replaces files,
	// one temporary copy at a time.
	need := plan.total
	for _, d := range plan.dirs {
		if dirExists(filepath.Join(dirs.ports, d)) {
			need = plan.largest
			break
		}
	}
	if free, ferr := freeBytes(dirs.ports); ferr == nil && free >= 0 && uint64(free) < need+pnFreeMargin {
		return res, fmt.Errorf("the card needs %d MB free for it, it has %d MB", (need+pnFreeMargin)>>20, free>>20)
	}
	logf := pstoreOpenInstallLog(zipPath, nil)
	if logf != nil {
		defer logf.Close()
		defer func() {
			if err != nil {
				fmt.Fprintf(logf, "# JukaHub's own installer stopped: %v\n\n", err)
			}
		}()
		fmt.Fprintf(logf, "# JukaHub's own installer: %d files, %d MB; %s (%s → %s)\n",
			len(plan.files)+len(plan.scripts), plan.total>>20, dirs.note, dirs.ports, dirs.scripts)
		if plan.skipped > 0 {
			fmt.Fprintf(logf, "# left out: %d links or stray files at the top\n", plan.skipped)
		}
	}
	var done int64
	total := int64(plan.total)
	progress := func(n int64) {
		done += n
		installBarUpdateRun(gen, installPhaseExtract, done, total)
	}
	installBarUpdateRun(gen, installPhaseExtract, 0, total)
	within := func(rel string) (string, error) {
		dest := filepath.Join(dirs.ports, filepath.FromSlash(rel))
		top := filepath.Join(dirs.ports, strings.SplitN(rel, "/", 2)[0])
		if !strings.HasPrefix(dest, top+string(filepath.Separator)) {
			return "", fmt.Errorf("%s would land outside its folder", rel)
		}
		return dest, nil
	}
	for _, rel := range plan.empty {
		dest, err := within(rel)
		if err != nil {
			return res, err
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return res, err
		}
	}
	for _, pf := range plan.files {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		dest, err := within(pf.rel)
		if err != nil {
			return res, err
		}
		if err := pnWriteZipFile(ctx, pf.f, dest, progress); err != nil {
			return res, err
		}
	}
	var items []string
	for _, sf := range plan.scripts {
		items = append(items, sf.rel)
		for _, dest := range pnScriptTargets(portMasterSDRoot, sf.rel, dirs.scripts) {
			if err := pnWriteZipFile(ctx, sf.f, dest, progress); err != nil {
				return res, err
			}
			res.Scripts = append(res.Scripts, dest)
		}
	}
	for _, d := range plan.dirs {
		items = append(items, d+"/")
	}
	res.PortsDir = dirs.ports
	// harbourmaster's status block, in the port's own port.json.
	manifest := plan.manifest
	if manifest == "" && len(plan.dirs) == 1 {
		manifest = plan.dirs[0] + "/port.json"
	}
	var meta portMeta
	if manifest != "" {
		mpath := filepath.Join(dirs.ports, filepath.FromSlash(manifest))
		raw, _ := portReadSmall(mpath)
		out, jerr := pnStatusJSON(raw, zipName, sum, items)
		if jerr == nil {
			if werr := pnWriteReader(ctx, bytes.NewReader(out), int64(len(out)), false, mpath, nil); werr != nil {
				logWarn("portsstore", "%s: writing port.json: %v", zipName, werr)
			} else {
				_ = mcSyncFile(mpath)
			}
			meta, _ = parsePortJSON(out)
		}
	}
	portMetaForget()
	// The runtimes it declares. The port is installed whatever happens here:
	// a runtime still missing is fetched again before it starts.
	for _, rt := range meta.Runtime {
		fetched, rerr := pnEnsureRuntime(ctx, gen, control, rt)
		if rerr != nil {
			if ctx.Err() != nil {
				rerr = errors.New("stopped")
			}
			res.RuntimeErr = fmt.Errorf("runtime %s: %w", rt, rerr)
			break
		}
		if fetched {
			res.Runtimes = append(res.Runtimes, rt)
		}
	}
	if logf != nil {
		fmt.Fprintf(logf, "# installed: %s; runtimes fetched: %s; runtime problem: %v\n\n",
			strings.Join(res.Scripts, ", "), setOr(strings.Join(res.Runtimes, ", "), "none"), res.RuntimeErr)
	}
	logInfo("portsstore", "%s installed by JukaHub: %d files to %s, scripts %s", zipName, len(plan.files), dirs.ports, strings.Join(res.Scripts, ", "))
	return res, nil
}

// pnPathWithin reports whether p is dir or inside it, compared without case
// (the card is FAT or exFAT). Pure.
func pnPathWithin(dir, p string) bool {
	d := strings.ToLower(filepath.Clean(dir))
	q := strings.ToLower(filepath.Clean(p))
	return q == d || strings.HasPrefix(q, d+string(filepath.Separator))
}

// ── Runtimes ─────────────────────────────────────────────────────────────

// pnRuntime is one runtime PortMaster's catalogue lists.
type pnRuntime struct {
	URL   string
	Parts []string // a runtime split into pieces, joined in order
	MD5   string
	Size  int64
	Arch  string
}

// pnHex32 reports an MD5 written as 32 hex digits. Pure.
func pnHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range strings.ToLower(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// pnParseRuntimes reads the runtimes in PortMaster's ports.json ("utils",
// or "runtimes"), by lower-case file name with and without ".squashfs". Like
// the port parser it trusts no single layout: a map keyed by file name or a
// list, "url"/"download_url" or a nested "source", "urls"/"parts" for a
// runtime in pieces. Only https links with an MD5 and, when an arch is
// named, aarch64 builds are kept. Pure.
func pnParseRuntimes(raw []byte) map[string]pnRuntime {
	var doc map[string]interface{}
	if json.Unmarshal(stripUTF8BOM(raw), &doc) != nil || doc == nil {
		return nil
	}
	out := map[string]pnRuntime{}
	for _, key := range []string{"utils", "runtimes"} {
		switch t := doc[key].(type) {
		case map[string]interface{}:
			for k, v := range t {
				if o, ok := v.(map[string]interface{}); ok {
					pnAddRuntime(out, k, o)
				}
			}
		case []interface{}:
			for _, v := range t {
				if o, ok := v.(map[string]interface{}); ok {
					pnAddRuntime(out, "", o)
				}
			}
		}
	}
	return out
}

// pnAddRuntime adds one catalogue object under every name it goes by.
func pnAddRuntime(out map[string]pnRuntime, key string, o map[string]interface{}) {
	r := pnRuntime{
		URL:  mcJSONString(o, "download_url", "url"),
		MD5:  mcJSONString(o, "md5"),
		Size: mcJSONInt(o, "download_size", "size"),
		Arch: strings.ToLower(mcJSONString(o, "runtime_arch", "arch")),
	}
	if src, ok := o["source"].(map[string]interface{}); ok {
		if r.URL == "" {
			r.URL = mcJSONString(src, "download_url", "url")
		}
		if r.MD5 == "" {
			r.MD5 = mcJSONString(src, "md5")
		}
		if r.Size == 0 {
			r.Size = mcJSONInt(src, "download_size", "size")
		}
	}
	for _, k := range []string{"urls", "parts"} {
		r.Parts = append(r.Parts, mcJSONStrings(o[k])...)
	}
	if !pnHex32(r.MD5) || (r.URL == "" && len(r.Parts) == 0) {
		return
	}
	for _, u := range append([]string{r.URL}, r.Parts...) {
		if u != "" && !strings.HasPrefix(u, "https://") {
			return
		}
	}
	if r.Arch != "" && r.Arch != "aarch64" && r.Arch != "arm64" {
		return
	}
	names := []string{key, mcJSONString(o, "name"), mcJSONString(o, "runtime_name"), mcJSONString(o, "file", "filename")}
	if r.URL != "" {
		if u, err := url.Parse(r.URL); err == nil {
			names = append(names, path.Base(u.Path))
		}
	}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || strings.ContainsAny(n, `/\ `) {
			continue
		}
		for _, k := range []string{n, strings.TrimSuffix(n, ".squashfs")} {
			if old, ok := out[k]; !ok || (old.Arch == "" && r.Arch != "") {
				out[k] = r
			}
		}
	}
}

// pnRuntimeLookup finds a port.json runtime name in the index. Pure.
func pnRuntimeLookup(idx map[string]pnRuntime, name string) (pnRuntime, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	base := strings.TrimSuffix(n, ".squashfs")
	for _, k := range []string{n, base, base + ".squashfs", base + ".aarch64.squashfs", base + "-aarch64.squashfs", base + ".aarch64"} {
		if r, ok := idx[k]; ok {
			return r, true
		}
	}
	return pnRuntime{}, false
}

var pnRuntimeCache struct {
	mu  sync.Mutex
	at  time.Time
	idx map[string]pnRuntime
}

// pnRuntimeMu lets one runtime download run at a time (the queue and a
// port's start may both want one).
var pnRuntimeMu sync.Mutex

// pnRuntimeIndex is the catalogue's runtimes: from memory, the Ports Store's
// saved copy while fresh, else the network (saved), else any saved copy.
func pnRuntimeIndex(ctx context.Context) (map[string]pnRuntime, error) {
	pnRuntimeCache.mu.Lock()
	if len(pnRuntimeCache.idx) > 0 && time.Since(pnRuntimeCache.at) < mcCatalogTTL {
		idx := pnRuntimeCache.idx
		pnRuntimeCache.mu.Unlock()
		return idx, nil
	}
	pnRuntimeCache.mu.Unlock()
	saved := filepath.Join(pstoreDataDir(), "ports.json")
	var idx map[string]pnRuntime
	if info, err := os.Stat(saved); err == nil && time.Since(info.ModTime()) < mcCatalogTTL {
		if raw, err := os.ReadFile(saved); err == nil {
			idx = pnParseRuntimes(raw)
		}
	}
	var ferr error
	if len(idx) == 0 {
		raw, err := pstoreGet(ctx, mcPortsCatalogURL, mcCatalogMax)
		ferr = err
		if err == nil {
			if idx = pnParseRuntimes(raw); len(idx) > 0 && len(mcParsePortsCatalog(raw)) > 0 {
				_ = pstoreWriteFile(saved, raw)
			}
		}
	}
	if len(idx) == 0 {
		if raw, err := os.ReadFile(saved); err == nil {
			idx = pnParseRuntimes(raw)
		}
	}
	if len(idx) == 0 {
		if ferr == nil {
			ferr = errors.New("PortMaster's catalogue lists no runtimes this version can read")
		}
		return nil, ferr
	}
	pnRuntimeCache.mu.Lock()
	pnRuntimeCache.idx, pnRuntimeCache.at = idx, time.Now()
	pnRuntimeCache.mu.Unlock()
	return idx, nil
}

// pnRuntimeFile is the name a runtime gets in libs/: port scripts look for
// libs/<runtime>.squashfs. "" for a name that is not a plain file name. Pure.
func pnRuntimeFile(name string) string {
	n := strings.TrimSpace(name)
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, `/\`+"\x00") {
		return ""
	}
	if !strings.HasSuffix(strings.ToLower(n), ".squashfs") {
		n += ".squashfs"
	}
	return n
}

// pnEnsureRuntime makes sure PortMaster's libs/ has the runtime a port.json
// names, downloading it (MD5 checked) when it does not. fetched is true when
// it was downloaded now. Off the main thread.
func pnEnsureRuntime(ctx context.Context, gen uint64, control, name string) (bool, error) {
	file := pnRuntimeFile(name)
	if file == "" || portRuntimePresent(control, name) {
		return false, nil
	}
	pnRuntimeMu.Lock()
	defer pnRuntimeMu.Unlock()
	if portRuntimePresent(control, name) { // fetched while this one waited
		return false, nil
	}
	installBarUpdateRun(gen, installPhaseResolving, 0, 0)
	idx, err := pnRuntimeIndex(ctx)
	if err != nil {
		return false, err
	}
	r, ok := pnRuntimeLookup(idx, name)
	if !ok {
		return false, errors.New("PortMaster's catalogue does not list it")
	}
	if r.Size > pnRuntimeMax {
		return false, fmt.Errorf("it is %d MB, more than JukaHub downloads", r.Size>>20)
	}
	libs := filepath.Join(control, "libs")
	if err := os.MkdirAll(libs, 0o755); err != nil {
		return false, err
	}
	if free, ferr := freeBytes(libs); ferr == nil && r.Size > 0 && free < r.Size+pnFreeMargin {
		return false, fmt.Errorf("the card needs %d MB free for it", (r.Size+pnFreeMargin)>>20)
	}
	part := filepath.Join(libs, "."+file+".part")
	defer os.Remove(part)
	urls := r.Parts
	if len(urls) == 0 {
		urls = []string{r.URL}
	}
	if err := pnDownloadJoined(ctx, gen, urls, part, r.Size); err != nil {
		return false, err
	}
	installBarUpdateRun(gen, installPhaseVerify, 0, 0)
	if info, err := os.Stat(part); err != nil || info.Size() > pnRuntimeMax {
		return false, errors.New("the download is too big or gone")
	}
	sum, err := mcFileMD5(part)
	if err != nil || !strings.EqualFold(sum, r.MD5) {
		return false, errors.New("it does not match the MD5 PortMaster's catalogue lists")
	}
	if err := mcSyncFile(part); err != nil {
		return false, err
	}
	if err := os.Rename(part, filepath.Join(libs, file)); err != nil {
		return false, err
	}
	logInfo("portsstore", "runtime %s downloaded into %s", file, libs)
	return true, nil
}

// pnDownloadJoined downloads urls one after another into dest (a runtime in
// pieces is joined in order), at most pnRuntimeMax bytes in all. size, when
// the catalogue gives it, is the progress total. Its own client: the shared
// downloader stops at 512 MB and refuses pieces under 4 KB.
func pnDownloadJoined(ctx context.Context, gen uint64, urls []string, dest string, size int64) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	var written int64
	report := func(n int64) {
		written += n
		installBarUpdateRun(gen, installPhaseDownload, written, size)
	}
	client := &http.Client{Timeout: 45 * time.Minute, Transport: ytTransport()}
	for _, u := range urls {
		if err = pnFetchInto(ctx, client, u, out, pnRuntimeMax-written, report); err != nil {
			break
		}
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// pnFetchInto appends u's body to out, at most max bytes.
func pnFetchInto(ctx context.Context, client *http.Client, u string, out io.Writer, max int64, report func(int64)) error {
	if max <= 0 {
		return errors.New("the runtime is bigger than JukaHub downloads")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub-PortsStore/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", req.URL.Host, resp.StatusCode)
	}
	if resp.ContentLength > max {
		return errors.New("the runtime is bigger than JukaHub downloads")
	}
	n, err := io.Copy(pnProgressWriter{w: out, fn: report}, pnCtxReader{ctx: ctx, r: io.LimitReader(resp.Body, max+1)})
	if err != nil {
		return err
	}
	if n > max {
		return errors.New("the runtime is bigger than JukaHub downloads")
	}
	return nil
}

// ── Before a port starts ─────────────────────────────────────────────────

var pnGate struct {
	mu       sync.Mutex
	running  bool
	tried    map[string]bool // runtimes a download finished for (or failed) this session
	inflight map[string]bool // runtimes downloading now
}

// pnRuntimeGate runs on the main thread before a port starts: a runtime its
// port.json declares that is not in PortMaster's libs/ is downloaded first
// (its script would call harbourmaster's runtime_check, which needs Python),
// and retry starts it afterwards (errPortPreparing). Each runtime is tried
// once a session; offline, or while another download runs, the port starts
// as it is.
func pnRuntimeGate(name, script string, retry func()) error {
	if runtime.GOOS != "linux" || !IsTrimuiDevice() {
		return nil
	}
	control := portRealControlFolder()
	if control == "" {
		return nil
	}
	pnGate.mu.Lock()
	defer pnGate.mu.Unlock()
	var missing []string
	for _, rt := range portMetaFor(script).Runtime {
		key := strings.ToLower(rt)
		if pnRuntimeFile(rt) != "" && !portRuntimePresent(control, rt) && (!pnGate.tried[key] || pnGate.inflight[key]) {
			missing = append(missing, rt)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if pnGate.running {
		// One download at a time; this port is not retried by itself.
		ShowToast(name+" needs "+strings.TrimSuffix(missing[0], ".squashfs")+
			" — start it again when the runtime download on the status strip has finished", ToastKindInfo)
		return errPortPreparing
	}
	if !IsOnline() || installBarActive() {
		return nil
	}
	label := strings.TrimSuffix(missing[0], ".squashfs")
	gen, card := installBarBeginRun(label)
	ShowToast(name+" needs "+label+" — downloading it first", ToastKindInfo)
	if pnGate.inflight == nil {
		pnGate.inflight = map[string]bool{}
	}
	for _, rt := range missing {
		pnGate.inflight[strings.ToLower(rt)] = true
	}
	pnGate.running = true
	go func() {
		ctx, cancel := mcRunContext(gen, card)
		defer cancel()
		var failure error
		defer func() {
			if r := recover(); r != nil {
				failure = fmt.Errorf("%v", r)
			}
			cancelled := ctx.Err() != nil
			switch {
			case cancelled:
				installBarAbortRun(gen)
			case failure != nil:
				installBarFailRun(gen, firstLine(failure.Error()))
			default:
				installBarDoneRun(gen)
			}
			pnGate.mu.Lock()
			pnGate.running = false
			if pnGate.tried == nil {
				pnGate.tried = map[string]bool{}
			}
			for _, rt := range missing {
				delete(pnGate.inflight, strings.ToLower(rt))
				// Tried once a session — a cancelled download is not a try.
				if !cancelled {
					pnGate.tried[strings.ToLower(rt)] = true
				}
			}
			pnGate.mu.Unlock()
			if cancelled {
				return
			}
			tbxPost(func() {
				if failure != nil {
					logWarn("portsstore", "%s: %v", name, failure)
					ShowToast("Could not download "+label+" ("+firstLine(failure.Error())+") — starting "+name+" anyway", ToastKindWarning)
				}
				if gameScreenSuspended() {
					return
				}
				retry()
			})
		}()
		for _, rt := range missing {
			if _, err := pnEnsureRuntime(ctx, gen, control, rt); err != nil {
				failure = fmt.Errorf("%s: %w", rt, err)
				return
			}
		}
	}()
	return errPortPreparing
}

// ── PortMaster's autoinstall folder ─────────────────────────────────────

// pnSweepSeen holds the autoinstall zips already handed to the queue this
// session (path, size and time). Main thread only.
var pnSweepSeen = map[string]bool{}

// pnAutoinstallSweep installs the port zips waiting in PortMaster's
// autoinstall folder (copied there by hand, or left by an older JukaHub)
// through the queue, without opening PortMaster. A zip JukaHub does not
// install itself (PortMaster's own update, a runtime) stays for PortMaster.
// Main thread; the zips are read on a goroutine.
func pnAutoinstallSweep() {
	if runtime.GOOS != "linux" || !IsTrimuiDevice() {
		return
	}
	// The player chose PortMaster's installer or app: its folder is its own.
	if m := pstoreInstallerMode(); m != "auto" && m != "jukahub" {
		return
	}
	inbox := mcAutoinstallDir(portMasterLauncher(portMasterSDRoot))
	if inbox == "" {
		return
	}
	items, err := os.ReadDir(inbox)
	if err != nil {
		return
	}
	var paths []string
	for _, it := range items {
		name := it.Name()
		if it.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".zip") {
			continue
		}
		info, err := it.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		key := fmt.Sprintf("%s|%d|%d", name, info.Size(), info.ModTime().UnixNano())
		if pnSweepSeen[key] {
			continue
		}
		if v, ok := pstoreJobLookup(name); ok && pstoreJobActive(v.state) {
			continue
		}
		pnSweepSeen[key] = true
		paths = append(paths, filepath.Join(inbox, name))
	}
	if len(paths) == 0 {
		return
	}
	go func() {
		for _, p := range paths {
			zr, err := zip.OpenReader(p)
			if err != nil {
				if zr != nil {
					zr.Close()
				}
				continue
			}
			_, perr := pnPlanZip(zr.File)
			zr.Close()
			if perr != nil {
				logDebug("portsstore", "autoinstall: %s stays for PortMaster (%v)", filepath.Base(p), perr)
				continue
			}
			zpath := p
			tbxPost(func() {
				logInfo("portsstore", "autoinstall: installing %s", filepath.Base(zpath))
				// Keep: the zip is the player's; a failed install leaves it there.
				portsInstallZip(portsInstallRequest{Name: strings.TrimSuffix(filepath.Base(zpath), filepath.Ext(zpath)), Zip: zpath, Keep: true})
			})
		}
	}()
}
