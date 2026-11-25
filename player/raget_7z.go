package main

// raget_7z.go — TrimUI's packages that are 7z archives, for raget.go.
//
// TrimUI publishes most of its SD base packages as .7z archives: every one of
// the Smart Pro's (the full packages and the incremental ones), and by their
// set-up guides the Brick Pro's and the Smart Pro S's recovery package.
// raget.go read only zips, so a Smart Pro — whose own packages are all 7z —
// could take only another model's build, the Brick's of November 2024, and
// when that one did not install, nothing did (JH-X5T7PW, 2026-11-24: "cant
// install retroarch" on a Smart Pro whose card had none).
//
// A 7z keeps its directory at the end, as a zip does, so listing a package is
// still a few range requests: sevenzip.go reads the directory, with bounds for
// a whole card in one archive (raGet7zLimits). Its files are not packed one
// by one, though: a solid block ("folder" in 7z's words) is one LZMA or LZMA2
// stream, and a file in it is reached only by decoding the block from its
// start. So a 7z package is unpacked in one pass (raGet7zUnpack): every block
// holding a file JukaHub wants is decoded from its start to the end of the
// last such file — the files in between read and dropped, never written —
// and the wanted ones are written as they come into a folder of their own in
// JukaHub's data folder (raGet7zStage): the binary, and when this package's
// folder is the one the card gets (raGetFolderSource), the files of
// RetroArch's folder the card does not have yet. Only the blocks' packed
// bytes are downloaded, front to back in large range requests, but a block
// is often most of the package: that is why a first install takes the
// handheld's own model's package first and the update pass leaves other
// models' 7z packages alone (raget.go). Decoding keeps a core busy for
// minutes, so the pass waits while a game is on screen (raGet7zYield).
//
// The binary is then checked like a zip's (raGetCheck), and the folder's
// files are moved into the card's RetroArch folder (raGet7zPlaceFolder): a
// rename, on the card they were unpacked on. 7-Zip 23 and later pack every
// 64-bit ARM program — ra64.trimui, the cores — with its ARM64 filter in
// front of LZMA2, and sevenzip.go undoes it (and the ARM and ARM-Thumb ones);
// a block packed with something it does not decode — the x86 filters, PPMd,
// BZip2, encryption — fails that package with the methods named, and the next
// build is tried. A package that failed so is passed over for a week, unless
// the decoder learnt more since (raGet7zDecoder).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// raGet7zLimits bound a TrimUI package's directory and its decoding: the files
// of a whole card and a directory of megabytes, where a game's .7z
// (sevenzip.go's defaults) has a handful; a dictionary twice the one 7-Zip's
// strongest preset uses.
var raGet7zLimits = sevenZipLimits{Files: 200000, Header: 64 << 20, Dict: 128 << 20}

// raGet7zBlock is one range request while a package is unpacked: the pass
// reads front to back, so larger requests mean fewer round trips. A variable,
// so a test can unpack in small blocks.
var raGet7zBlock int64 = 8 << 20

// raGet7zCopyBuf is the buffer the pass reads through.
const raGet7zCopyBuf = 256 << 10

// raGet7zWanted is one file of a 7z package to unpack: its entry and its
// slash path in the folder it is unpacked into.
type raGet7zWanted struct {
	idx int
	rel string
}

// raGet7zOpenAsset reads a .7z package's directory and finds RetroArch's
// folder in it, as raGetOpenAsset does a zip's.
func raGet7zOpenAsset(ctx context.Context, client *http.Client, src raGetSource, tag string, a emuGitHubAsset) (raGetCandidate, bool, error) {
	remote := newRaGetRemote(ctx, client, strings.TrimSpace(a.BrowserDownloadURL), a.Size, raGetDirBudget)
	remote.setBlockSize(raGetDirBlock)
	arc, err := sevenZipOpenWith(remote, a.Size, raGet7zLimits)
	if err != nil {
		return raGetCandidate{}, false, fmt.Errorf("%s: %w", a.Name, err)
	}
	bin, binDir := raGet7zFindBinary(arc)
	if bin < 0 {
		return raGetCandidate{}, false, nil
	}
	e := arc.Entries[bin]
	if !e.HasCRC || e.Size == 0 {
		return raGetCandidate{}, false, fmt.Errorf("%s lists %s without its size and checksum", a.Name, raGetBinaryName)
	}
	c := raGetCandidate{Repo: src.Repo, Tag: tag, Asset: strings.TrimSpace(a.Name), Model: src.Model, When: e.MTime,
		sz: arc, szBin: bin, remote: remote}
	if binDir != "." {
		c.Prefix = binDir + "/"
	}
	folder := strings.EqualFold(path.Base(binDir), "RetroArch")
	for _, f := range arc.Entries {
		rel, ok := raGetUnder(f.Name, c.Prefix)
		if !ok || rel == "" || f.Dir {
			continue
		}
		c.Files++
		c.Bytes += int64(f.Size)
		if folder && strings.EqualFold(rel, "retroarch.cfg") {
			c.Full = true
		}
	}
	return c, true, nil
}

// raGet7zFindBinary is raGetOpenAsset's rule for a 7z's entries: the
// shallowest ra64.trimui in a folder named RetroArch, else the shallowest
// anywhere. -1 when there is none; dir is its folder ("." at the top).
func raGet7zFindBinary(arc *sevenZipArchive) (int, string) {
	depth := func(dir string) int {
		if dir == "." {
			return -1
		}
		return strings.Count(dir, "/")
	}
	bin, loose := -1, -1
	binDir, looseDir := "", ""
	for i, e := range arc.Entries {
		if e.Dir || e.folder < 0 || !strings.EqualFold(path.Base(e.Name), raGetBinaryName) {
			continue
		}
		dir := path.Dir(e.Name)
		if strings.EqualFold(path.Base(dir), "RetroArch") {
			if bin < 0 || depth(dir) < depth(binDir) {
				bin, binDir = i, dir
			}
			continue
		}
		if loose < 0 || depth(dir) < depth(looseDir) {
			loose, looseDir = i, dir
		}
	}
	if bin < 0 {
		return loose, looseDir
	}
	return bin, binDir
}

// raGet7zPlan lists the files of c's package an install unpacks: the binary
// (bin), as raGetBinaryName at the top, and with target the files of
// RetroArch's folder target does not have yet — never one it has — leaving
// out JukaHub's manifest and, with lean, raGetLeanSkip's parts (raGetFolderPlan
// for a zip). dirs are the folder's folders; total is the files' size.
func raGet7zPlan(c *raGetCandidate, target string, bin, lean bool) ([]raGet7zWanted, []string, int64, error) {
	arc := c.sz
	var want []raGet7zWanted
	var dirs []string
	var total int64
	if bin {
		e := arc.Entries[c.szBin]
		if e.Size > raGetMaxBinary {
			return nil, nil, 0, fmt.Errorf("%s is %s, more than a RetroArch can be", raGetBinaryName, formatInstallBytes(int64(e.Size)))
		}
		want = append(want, raGet7zWanted{idx: c.szBin, rel: raGetBinaryName})
		total += int64(e.Size)
	}
	if target == "" {
		return want, nil, total, nil
	}
	for i, e := range arc.Entries {
		under, ok := raGetUnder(e.Name, c.Prefix)
		if !ok {
			continue
		}
		rel, err := emuArchiveRel(under)
		if err != nil {
			return nil, nil, 0, err
		}
		if rel == "" {
			continue
		}
		slash := filepath.ToSlash(rel)
		lowSlash := strings.ToLower(slash)
		if lowSlash == raGetBinaryName || strings.EqualFold(path.Base(slash), raGetManifestName) ||
			strings.HasSuffix(lowSlash, raGetPartSuffix) {
			continue
		}
		if lean && slices.ContainsFunc(raGetLeanSkip, func(p string) bool { return strings.HasPrefix(lowSlash+"/", p) }) {
			continue
		}
		if e.Dir {
			dirs = append(dirs, slash)
			continue
		}
		if _, err := os.Lstat(filepath.Join(target, rel)); err == nil {
			continue // the card's own file stays
		}
		want = append(want, raGet7zWanted{idx: i, rel: slash})
		total += int64(e.Size)
	}
	if len(want) > raGetMaxFolderFiles || total > raGetMaxFolder {
		return nil, nil, 0, fmt.Errorf("RetroArch's folder in %s is %d files, %s: more than JukaHub unpacks", c.Asset, len(want), formatInstallBytes(total))
	}
	return want, dirs, total, nil
}

// raGet7zStage runs the pass for an install: the binary (bin) and, with
// target, the files of RetroArch's folder the card's target does not have —
// all of them unless the card is short of space, then without what RetroArch
// runs without; extra is what else the install will write — into a folder of
// its own under stage (n names it). It returns the binary's path there (""
// without bin); c keeps the folder's files for raGet7zPlaceFolder.
func raGet7zStage(ctx context.Context, c *raGetCandidate, stage string, n int, target string, bin bool, extra int64, report func(done, total int64)) (string, error) {
	want, dirs, total, err := raGet7zPlan(c, target, bin, false)
	if err != nil {
		return "", err
	}
	lean := false
	if target != "" {
		// Unpacked here, then moved: room on the card for the folder (and on
		// the staging folder's disk, should that be another one).
		free, ok := emuMoreCoresLeastFree([]string{stage, filepath.Dir(target)})
		if ok && free > 0 && free < total+extra+raGetSpaceMargin {
			lean = true
			if want, dirs, total, err = raGet7zPlan(c, target, bin, true); err != nil {
				return "", err
			}
			if free < total+extra+raGetSpaceMargin {
				return "", fmt.Errorf("RetroArch needs about %s on the card, which has %s free",
					formatInstallBytes(total+extra+raGetSpaceMargin), formatInstallBytes(free))
			}
		}
	}
	// A folder-only pass gets a folder of its own: the binary an earlier pass
	// of the same package unpacked may be the one being installed.
	name := fmt.Sprintf("pkg-%d", n)
	if !bin {
		name = fmt.Sprintf("folder-%d", n)
	}
	dir := filepath.Join(stage, name)
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for _, d := range dirs {
		_ = root.MkdirAll(filepath.FromSlash(d), 0o755)
	}
	leanNote := ""
	if lean {
		leanNote = ", without what it runs without (the card is short of space)"
	}
	logInfo("emulators", "RetroArch install: unpacking %d files (%s) of %s %s %s%s — a 7z package is decoded from the start of each block, so most of it may be downloaded",
		len(want), formatInstallBytes(total), c.Repo, c.Tag, c.Asset, leanNote)
	before := c.remote.transferred()
	if err := raGet7zUnpack(ctx, c, root, want, report); err != nil {
		return "", err
	}
	logInfo("emulators", "RetroArch install: %d files unpacked from %s; %s downloaded of its %s",
		len(want), c.Asset, formatInstallBytes(c.remote.transferred()-before), formatInstallBytes(c.remote.size))
	c.staged, c.stagedFolder, c.stagedDirs, c.stagedLean = dir, target != "", dirs, lean
	c.stagedFiles = nil
	for _, w := range want {
		if !(bin && w.idx == c.szBin) {
			c.stagedFiles = append(c.stagedFiles, w)
		}
	}
	if !bin {
		return "", nil
	}
	return filepath.Join(dir, raGetBinaryName), nil
}

// raGet7zUnpack writes want's files of c's package into root (see the top of
// the file): the blocks holding them, each decoded from its start to the end
// of the last one wanted, every file checked against its size and CRC-32
// before it takes its name. report hears the bytes downloaded against the
// pass's estimate.
func raGet7zUnpack(ctx context.Context, c *raGetCandidate, root *os.Root, want []raGet7zWanted, report func(done, total int64)) error {
	arc := c.sz
	st := &arc.streams
	buf := make([]byte, raGet7zCopyBuf)
	byFolder := map[int][]raGet7zWanted{}
	var folders []int
	for _, w := range want {
		e := arc.Entries[w.idx]
		if e.folder < 0 {
			// An empty file: nothing to decode.
			if err := raGet7zWriteEntry(root, nil, e, w.rel, buf); err != nil {
				return fmt.Errorf("%s: %w", w.rel, err)
			}
			continue
		}
		if _, seen := byFolder[e.folder]; !seen {
			folders = append(folders, e.folder)
		}
		byFolder[e.folder] = append(byFolder[e.folder], w)
	}
	sort.Ints(folders)
	ends := make(map[int]uint64, len(folders))
	var planned, packed int64
	for _, fi := range folders {
		if fi >= len(st.folders) || st.folders[fi].packIndex >= len(st.packSize) {
			return errSevenZipFormat
		}
		list := byFolder[fi]
		sort.SliceStable(list, func(i, j int) bool { return arc.Entries[list[i].idx].offset < arc.Entries[list[j].idx].offset })
		var end uint64
		for _, w := range list {
			e := arc.Entries[w.idx]
			end = max(end, e.offset+e.Size)
		}
		ends[fi] = end
		f := st.folders[fi]
		size := int64(st.packSize[f.packIndex])
		packed += size
		// The download ends about where the last file does: packed and
		// unpacked bytes in proportion, near enough for a progress bar.
		if f.unpackSize > 0 {
			planned += int64(float64(size) * min(1, float64(end)/float64(f.unpackSize)))
		}
	}
	c.remote.setBlockSize(raGet7zBlock)
	base := c.remote.transferred()
	c.remote.setBudget(base + packed + 2*raGet7zBlock)
	if report != nil {
		c.remote.setOnFetch(func(fetched int64) {
			done := fetched - base
			report(done, max(planned, done))
		})
		defer c.remote.setOnFetch(nil)
	}
	for _, fi := range folders {
		rd, err := st.folderReaderDict(c.remote, arc.size, fi, ends[fi], raGet7zLimits.Dict)
		switch {
		case errors.Is(err, errSevenZipUnsupported):
			// Said with the methods, so a report shows what TrimUI used.
			return raGet7zFinal{fmt.Errorf("its RetroArch files are packed with %s, which JukaHub does not decode",
				sevenZipFolderMethods(st.folders[fi]))}
		case errors.Is(err, errSevenZipTooFar):
			return raGet7zFinal{fmt.Errorf("its blocks are packed with a dictionary larger than the %s JukaHub decodes with", formatInstallBytes(int64(raGet7zLimits.Dict)))}
		case errors.Is(err, errSevenZipFormat):
			return raGet7zFinal{err}
		case err != nil:
			return err
		}
		var pos uint64
		for _, w := range byFolder[fi] {
			if err := raGet7zYield(ctx); err != nil {
				return err
			}
			e := arc.Entries[w.idx]
			if e.offset < pos {
				return raGet7zFinal{fmt.Errorf("%w: files that overlap", errSevenZipFormat)}
			}
			if err := raGet7zSkip(ctx, rd, e.offset-pos, buf); err != nil {
				return err
			}
			if err := raGet7zWriteEntry(root, rd, e, w.rel, buf); err != nil {
				return fmt.Errorf("%s: %w", w.rel, err)
			}
			pos = e.offset + e.Size
		}
	}
	return nil
}

// raGet7zYield holds the pass while a game is on screen — decoding a package
// keeps a core busy for minutes, and a game started meanwhile (a port, a
// standalone emulator) should not share it — and reports ctx ending.
func raGet7zYield(ctx context.Context) error {
	for gameScreenSuspended() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return ctx.Err()
}

// raGet7zSkip reads n bytes of rd and drops them, stopping when ctx ends.
func raGet7zSkip(ctx context.Context, rd io.Reader, n uint64, buf []byte) error {
	for n > 0 {
		if err := raGet7zYield(ctx); err != nil {
			return err
		}
		k := min(n, uint64(len(buf)))
		got, err := io.ReadFull(rd, buf[:k])
		n -= uint64(got)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return raGet7zFinal{fmt.Errorf("%w: a block ends early", errSevenZipFormat)}
			}
			return err
		}
	}
	return nil
}

// raGet7zWriteEntry writes the next e.Size bytes of rd (nothing for an empty
// file) to rel inside root, beside its name first, checked against e's size
// and CRC-32.
func raGet7zWriteEntry(root *os.Root, rd io.Reader, e sevenZipEntry, rel string, buf []byte) error {
	name := filepath.FromSlash(rel)
	if parent := filepath.Dir(name); parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	part := name + raGetPartSuffix
	out, err := root.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	h := crc32.NewIEEE()
	var n int64
	if e.Size > 0 && rd != nil {
		n, err = io.CopyBuffer(io.MultiWriter(out, h), io.LimitReader(rd, int64(e.Size)), buf)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
	case uint64(n) != e.Size:
		err = raGet7zFinal{fmt.Errorf("it ended after %d of its %d bytes", n, e.Size)}
	case e.HasCRC && h.Sum32() != e.CRC:
		err = raGet7zFinal{errors.New("it does not match its CRC-32")}
	}
	if err != nil {
		_ = root.Remove(part)
		return err
	}
	return root.Rename(part, name)
}

// raGet7zFolderInto is raGetZipFolderInto for a 7z package (index n in the
// install's list): its folder, unpacked with its binary by raGetPick or now
// (binSize is then the binary another package gives), moved into target. A
// folder that cannot be unpacked marks the package szFailed, so the install
// takes the next package's (raGetFolderSource).
func raGet7zFolderInto(ctx context.Context, folder *raGetCandidate, stage string, n int, target string, binSize int64, report func(phase string, done, total int64)) ([]string, bool, bool, error) {
	if !folder.stagedFolder {
		_, err := raGet7zStage(ctx, folder, stage, n, target, false, binSize, func(done, total int64) { report(installPhaseDownload, done, total) })
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, false, ctx.Err()
			}
			folder.szFailed = "its folder could not be unpacked: " + firstLine(err.Error())
			raGetRefuseIfFinal(folder, err)
			return nil, false, false, fmt.Errorf("unpacking RetroArch's folder from %s: %w", folder.Asset, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, false, err
	}
	madeTarget := !dirExists(target)
	created, err := raGet7zPlaceFolder(ctx, folder, target, func(done, all int64) { report(installPhaseExtract, done, all) })
	if err != nil {
		raGetUndo(target, created, madeTarget)
		return nil, false, false, fmt.Errorf("writing RetroArch's folder: %w", err)
	}
	return created, folder.stagedLean, madeTarget, nil
}

// raGet7zPlaceFolder moves the folder files c's pass unpacked into target — a
// rename on the card they were unpacked on — the ones target still does not
// have, and returns them for raGetUndo.
func raGet7zPlaceFolder(ctx context.Context, c *raGetCandidate, target string, report func(done, total int64)) ([]string, error) {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, err
	}
	for _, d := range c.stagedDirs {
		_ = os.MkdirAll(filepath.Join(target, filepath.FromSlash(d)), 0o755)
	}
	var total, done int64
	for _, w := range c.stagedFiles {
		total += int64(c.sz.Entries[w.idx].Size)
	}
	var created []string
	for _, w := range c.stagedFiles {
		if err := ctx.Err(); err != nil {
			return created, err
		}
		local := filepath.FromSlash(w.rel)
		dst := filepath.Join(target, local)
		done += int64(c.sz.Entries[w.idx].Size)
		if _, err := os.Lstat(dst); err == nil {
			continue // the card's own file stays
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return created, err
		}
		if err := raGetMoveFile(filepath.Join(c.staged, local), dst); err != nil {
			return created, fmt.Errorf("%s: %w", w.rel, err)
		}
		created = append(created, w.rel)
		report(done, total)
	}
	logInfo("emulators", "RetroArch install: %d files of %s's RetroArch folder moved into place", len(created), c.Asset)
	return created, nil
}

// ── Packages that failed for good ───────────────────────────────────────────

// raGet7zFinal is a pass that fails the same way every time — a method
// JukaHub does not decode, a damaged block, a file whose checksum is wrong —
// rather than one the network cut short.
type raGet7zFinal struct{ err error }

func (f raGet7zFinal) Error() string { return f.err.Error() }
func (f raGet7zFinal) Unwrap() error { return f.err }

// raGetRefusedTTL is how long a 7z package that failed for good, or whose
// build did not start here, is passed over by a first install: its pass costs
// most of the package, and the start-up would pay it at every start. A new
// build in the package (its key names the binary's checksum), or JukaHub's
// glibc coming or going, tries it again sooner.
const raGetRefusedTTL = 7 * 24 * time.Hour

// raGet7zDecoder counts what sevenzip.go decodes: 2 since it undoes the ARM64,
// ARM and ARM-Thumb filters. A package refused by an earlier decoder is tried
// again — the builds before it refused every Smart Pro package "for good".
const raGet7zDecoder = 2

// raGetRefusal is one package passed over; Dec is the decoder that refused it.
type raGetRefusal struct {
	At      time.Time `json:"at"`
	Why     string    `json:"why"`
	Runtime bool      `json:"runtime"`
	Dec     int       `json:"dec,omitempty"`
}

// raGetRefusedMu keeps the file whole between goroutines.
var raGetRefusedMu sync.Mutex

// raGetRefusedPath is the file the packages passed over are kept in ("" with
// no data folder).
func raGetRefusedPath() string {
	data, err := P().DataDir()
	if err != nil || strings.TrimSpace(data) == "" {
		return ""
	}
	return filepath.Join(data, "retroarch-get-refused.json")
}

// raGetRefusedKey names a package's build: the package and its binary's
// checksum.
func raGetRefusedKey(c *raGetCandidate) string {
	return c.Repo + "/" + c.Tag + "/" + c.Asset + "#" + c.crc()
}

// raGetRefusedLoad reads the file (raGetRefusedMu held); a missing or
// unreadable one is empty.
func raGetRefusedLoad(file string) map[string]raGetRefusal {
	out := map[string]raGetRefusal{}
	if data, err := os.ReadFile(file); err == nil && len(data) <= 256<<10 {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

// raGetPassOver marks the 7z packages of an install's list that failed for
// good here within raGetRefusedTTL, with JukaHub's glibc as it is now: their
// pass is not paid for again (raGetPick, raGetFolderSource skip them).
func raGetPassOver(cands []raGetCandidate) {
	file := raGetRefusedPath()
	if file == "" {
		return
	}
	_, _, ready := raGlibcRuntime()
	raGetRefusedMu.Lock()
	all := raGetRefusedLoad(file)
	raGetRefusedMu.Unlock()
	now := time.Now()
	for i := range cands {
		c := &cands[i]
		if c.sz == nil {
			continue
		}
		r, ok := all[raGetRefusedKey(c)]
		// A time ahead of the clock is a clock set back (TrimUI starts before
		// the network time): the package is tried again, as is one an older
		// decoder refused.
		if !ok || r.Runtime != ready || r.Dec < raGet7zDecoder || r.At.After(now.Add(time.Hour)) || now.Sub(r.At) > raGetRefusedTTL {
			continue
		}
		c.szFailed = "passed over: on " + r.At.Local().Format("2006-01-02") + " " + r.Why
		logInfo("emulators", "RetroArch packages: %s %s is passed over: it failed here on %s (%s)", c.Repo, c.Asset, r.At.Local().Format("2006-01-02 15:04"), r.Why)
	}
}

// raGetRefuse remembers that c's build did not start here, or that its pass
// failed for good (why), for raGetPassOver.
func raGetRefuse(c *raGetCandidate, why string) {
	file := raGetRefusedPath()
	if file == "" || c.sz == nil {
		return
	}
	_, _, ready := raGlibcRuntime()
	raGetRefusedMu.Lock()
	defer raGetRefusedMu.Unlock()
	all := raGetRefusedLoad(file)
	now := time.Now()
	for k, r := range all {
		if now.Sub(r.At) > raGetRefusedTTL || r.At.After(now.Add(time.Hour)) {
			delete(all, k)
		}
	}
	all[raGetRefusedKey(c)] = raGetRefusal{At: now.UTC(), Why: firstLine(why), Runtime: ready, Dec: raGet7zDecoder}
	data, err := json.MarshalIndent(all, "", "  ")
	if err == nil {
		err = linuxPkgWriteFile(file, append(data, '\n'))
	}
	if err != nil {
		logDebug("emulators", "RetroArch packages: could not remember %s: %v", raGetRefusedKey(c), err)
	}
}

// raGetRefuseIfFinal is raGetRefuse for a pass's error that will not change by
// trying again.
func raGetRefuseIfFinal(c *raGetCandidate, err error) {
	var final raGet7zFinal
	if errors.As(err, &final) {
		raGetRefuse(c, err.Error())
	}
}
