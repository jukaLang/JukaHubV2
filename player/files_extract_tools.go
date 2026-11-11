package main

// files_extract_tools.go — the archives Files hands to another program.
//
// 7z and RAR (and a zip compressed with something other than Deflate) are not
// read in-process; emu7z.go chose 7-Zip for the emulator installs because a 7z
// reader cannot be tested without a compiler at hand, and the same holds here.
// The program is 7-Zip — 7zz on the handheld (JukaCrossMix ships it in
// System/bin), an installed 7-Zip on Windows, or there, for a 7z, the official
// 7zr.exe fetched once and checked against its published digest — with Windows'
// own tar.exe (libarchive) for RAR when no full 7-Zip is installed.
//
// A program writes the files itself, so the rules are kept around it: 7-Zip
// lists the archive first (-slt), and the entry count, the space it unpacks
// to, encryption and links are checked before anything is written — an archive
// it cannot list is not unpacked; wildcards are off (-spd) and the output
// folder is the working folder, so no name in a path is read as a pattern;
// while it runs, the card's free space is watched and the run stopped at the
// margin; and afterwards what it wrote is walked and held to the built-in
// readers' rules (feExtractTidy). Windows' tar.exe (RAR only) cannot be asked
// for a listing it is held to; libarchive refuses to write through links on its
// own, and the space watch and the walk still apply.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// feToolKind is the external program an archive is handed to.
type feToolKind int

const (
	feToolNone feToolKind = iota
	feTool7Zip
	feToolWinTar
)

// feArchiveTool is a program found on this device.
type feArchiveTool struct {
	Path string
	Kind feToolKind
}

// fe7ZipNames are 7-Zip's command-line builds, the ones that read the most
// first.
var fe7ZipNames = []string{"7zz", "7zzs", "7z", "7za", "7zr"}

// fe7ZipDirs are where 7-Zip lives on the handheld when it is not on PATH:
// JukaCrossMix installs its runtime payload (7zz among it) in System/bin.
var fe7ZipDirs = []string{
	"/mnt/SDCARD/System/bin",
	"/mnt/SDCARD/System/usr/jukamix/bin",
	"/usr/local/bin",
	"/usr/bin",
}

// fe7ZipHandles reports whether the 7-Zip build at path reads kind: 7zr only
// knows 7z, 7za adds zip and the tar family but not RAR, and the full 7z and
// 7zz read them all.
func fe7ZipHandles(path string, kind feArchiveKind) bool {
	base := strings.ToLower(filepath.Base(path))
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "7zr":
		return kind == feArchive7z
	case "7za":
		return kind != feArchiveRar
	}
	return true
}

// feFind7ZipFor finds a 7-Zip build that reads kind. A weaker build first on
// PATH (a 7zr) does not hide a full 7zz in System/bin.
func feFind7ZipFor(kind feArchiveKind) string {
	if IsWindows() {
		// The installed 7-Zip, one on PATH or the 7zr.exe an emulator install
		// fetched; then a full 7-Zip on PATH that emu7zInstalled did not pick.
		if found := emu7zInstalled(); found != "" && fe7ZipHandles(found, kind) {
			return found
		}
		for _, name := range []string{"7z.exe", "7za.exe"} {
			if found, err := exec.LookPath(name); err == nil && found != "" && fe7ZipHandles(found, kind) {
				return found
			}
		}
		return ""
	}
	dirs := append([]string(nil), fe7ZipDirs...)
	if tools := strings.TrimSpace(P().ToolsDir()); tools != "" {
		dirs = append(dirs, tools)
	}
	for _, name := range fe7ZipNames {
		var candidates []string
		if found, err := exec.LookPath(name); err == nil && found != "" {
			candidates = append(candidates, found)
		}
		for _, dir := range dirs {
			if candidate := filepath.Join(dir, name); regularFile(candidate) {
				candidates = append(candidates, candidate)
			}
		}
		for _, candidate := range candidates {
			if fe7ZipHandles(candidate, kind) {
				return candidate
			}
		}
	}
	return ""
}

// feFindWindowsTar is Windows' own tar.exe (libarchive), which reads RAR. It is
// taken from System32 by full path, never from PATH.
func feFindWindowsTar() string {
	if !IsWindows() {
		return ""
	}
	root := strings.TrimSpace(os.Getenv("SystemRoot"))
	if root == "" {
		root = `C:\Windows`
	}
	if candidate := filepath.Join(root, "System32", "tar.exe"); regularFile(candidate) {
		return candidate
	}
	return ""
}

// feArchiveToolFor is the program that unpacks kind on this device, or a zero
// tool when there is none (on Windows a .7z can still get 7zr.exe fetched; see
// feExtractWithTool).
func feArchiveToolFor(kind feArchiveKind) feArchiveTool {
	if found := feFind7ZipFor(kind); found != "" {
		return feArchiveTool{Path: found, Kind: feTool7Zip}
	}
	if kind == feArchiveRar {
		if found := feFindWindowsTar(); found != "" {
			return feArchiveTool{Path: found, Kind: feToolWinTar}
		}
	}
	return feArchiveTool{}
}

// feTailBuffer keeps the last few kilobytes a program printed.
type feTailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *feTailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - 8192; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *feTailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// fe7ZipNoPassword is handed to 7-Zip as the password, so an encrypted archive
// fails at once ("Wrong password") instead of waiting for one to be typed.
const fe7ZipNoPassword = "-pjukahub-no-password"

// --- Listing -------------------------------------------------------------------

// fe7ZipEntry is one item of a 7-Zip listing.
type fe7ZipEntry struct {
	Path      string
	Size      int64
	Dir       bool
	Link      bool
	Encrypted bool
}

// errFe7ZipTooMany stops a listing at feExtractMaxEntries.
var errFe7ZipTooMany = errors.New("too many items")

// fe7ZipParseListing reads 7-Zip's technical listing (l -slt): after the
// "----------" line, one block of "Key = Value" lines per item, blocks parted
// by blank lines. It stops with errFe7ZipTooMany past max items.
func fe7ZipParseListing(r io.Reader, max int) ([]fe7ZipEntry, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var entries []fe7ZipEntry
	var cur fe7ZipEntry
	have, inItems := false, false
	flush := func() error {
		if have {
			entries = append(entries, cur)
			if len(entries) > max {
				return errFe7ZipTooMany
			}
		}
		cur, have = fe7ZipEntry{}, false
		return nil
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if !inItems {
			inItems = line == "----------"
			continue
		}
		if line == "" {
			if err := flush(); err != nil {
				return entries, err
			}
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if !ok && strings.HasSuffix(line, " =") {
			key, value, ok = strings.TrimSuffix(line, " ="), "", true
		}
		if !ok {
			continue
		}
		switch key {
		case "Path":
			if err := flush(); err != nil {
				return entries, err
			}
			cur.Path, have = value, true
		case "Size":
			cur.Size, _ = strconv.ParseInt(value, 10, 64)
		case "Folder":
			cur.Dir = value == "+"
		case "Attributes":
			// "D drwxr-xr-x", "A -rw-r--r--", "A lrwxrwxrwx", "AL": the Windows
			// letters (D a folder, L a reparse point — a link), then the Unix
			// mode when the archive kept one.
			for i, field := range strings.Fields(value) {
				if i == 0 && !strings.ContainsAny(field, "-") {
					if strings.Contains(field, "D") {
						cur.Dir = true
					}
					if strings.Contains(field, "L") {
						cur.Link = true
					}
				}
				if len(field) == 10 && field[0] == 'l' {
					cur.Link = true
				}
				if len(field) == 10 && field[0] == 'd' {
					cur.Dir = true
				}
			}
		case "Symbolic Link", "Hard Link":
			if value != "" {
				cur.Link = true
			}
		case "Encrypted":
			cur.Encrypted = value == "+"
		}
	}
	if err := flush(); err != nil {
		return entries, err
	}
	if err := sc.Err(); err != nil {
		return entries, err
	}
	if !inItems {
		return nil, errors.New("7-Zip printed no listing")
	}
	return entries, nil
}

// feList7Zip lists archive with 7-Zip. An archive whose very listing needs a
// password (encrypted headers) answers errFeArchiveEncrypted.
func feList7Zip(ctx context.Context, tool, archive string) ([]fe7ZipEntry, error) {
	listCtx, stop := context.WithTimeout(ctx, 5*time.Minute)
	defer stop()
	cmd := exec.CommandContext(listCtx, tool, "l", "-slt", "-spd", fe7ZipNoPassword, "--", archive)
	feToolProcAttr(cmd)
	var errOut feTailBuffer
	cmd.Stderr = &errOut
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w (7-Zip did not start: %v)", errFeToolFailed, err)
	}
	entries, parseErr := fe7ZipParseListing(stdout, feExtractMaxEntries)
	if parseErr != nil {
		// Stop it rather than read the rest of a listing that is refused.
		stop()
	}
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if errors.Is(parseErr, errFe7ZipTooMany) {
		return nil, fmt.Errorf("%w (the limit is %d)", errFeTooMany, feExtractMaxEntries)
	}
	if lower := strings.ToLower(errOut.String()); strings.Contains(lower, "wrong password") ||
		strings.Contains(lower, "can not open encrypted archive") {
		return nil, errFeArchiveEncrypted
	}
	if waitErr != nil || parseErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		detail := emu7zFailure(errOut.String())
		if detail == "" && waitErr != nil {
			detail = waitErr.Error()
		}
		if detail == "" && parseErr != nil {
			detail = parseErr.Error()
		}
		return nil, fmt.Errorf("%w (7-Zip: %s)", errFeToolFailed, detail)
	}
	return entries, nil
}

// --- Running -----------------------------------------------------------------

// fe7ZipFollowProgress reads 7-Zip's -bsp1 meter ("  42% 7 - name", redrawn in
// place with backspaces) and records the percentage that leads each redraw; a
// number in a file name further along the line is not one. The meter only
// switches to percent once one arrives, so a 7-Zip that prints none shows as
// working rather than as stuck at 0%.
func fe7ZipFollowProgress(r io.Reader, meter *feExtractMeter) {
	buf := make([]byte, 4096)
	digits, value := 0, 0
	lead, seen := true, false
	for {
		n, err := r.Read(buf)
		for _, c := range buf[:n] {
			switch {
			case c == '\b' || c == '\r' || c == '\n':
				digits, value, lead = 0, 0, true
			case c == ' ' && digits == 0:
				// Spaces before the number keep the start of the line.
			case c >= '0' && c <= '9' && (lead || digits > 0):
				if digits < 3 {
					value = value*10 + int(c-'0')
				}
				digits++
				lead = false
			case c == '%' && digits > 0 && digits <= 3 && value <= 100:
				if !seen {
					meter.setTotal(100, false, true)
					seen = true
				}
				meter.setDone(int64(value), 100)
				digits, value, lead = 0, 0, false
			default:
				digits, value, lead = 0, 0, false
			}
		}
		if err != nil {
			return
		}
	}
}

// feRun7Zip unpacks archive into out with 7-Zip. "x" keeps the folders; -y
// answers every prompt; -bso0 -bsp1 leave only the progress meter on stdout;
// -spd turns wildcards off, and out is the working folder ("-o."), because
// 7-Zip reads a "*" in an -o path as the archive's name; the dummy password
// makes an encrypted archive fail at once. Exit code 1 is 7-Zip's "warning":
// the files were written, and the warning is returned for the toast.
func feRun7Zip(ctx context.Context, tool, archive, out string, meter *feExtractMeter) (string, error) {
	cmd := exec.CommandContext(ctx, tool, "x", "-y", "-bso0", "-bsp1", "-spd", fe7ZipNoPassword, "-o.", "--", archive)
	cmd.Dir = out
	feToolProcAttr(cmd)
	var errOut feTailBuffer
	cmd.Stderr = &errOut
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%w (7-Zip did not start: %v)", errFeToolFailed, err)
	}
	fe7ZipFollowProgress(stdout, meter)
	err = cmd.Wait()
	if err == nil {
		return "", nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	output := errOut.String()
	detail := emu7zFailure(output)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		if detail == "" {
			detail = "some files had problems"
		}
		return detail, nil
	}
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "wrong password"), strings.Contains(lower, "can not open encrypted archive"):
		return "", errFeArchiveEncrypted
	case strings.Contains(lower, "no space left"), strings.Contains(lower, "not enough space"):
		return "", errFeCardFull
	case strings.Contains(lower, "unsupported method"):
		return "", fmt.Errorf("%w (this 7-Zip cannot unpack its compression: %s)", errFeToolFailed, detail)
	case strings.Contains(lower, "can not open output file") &&
		(strings.Contains(lower, "invalid argument") || strings.Contains(lower, "illegal byte")):
		return "", fmt.Errorf("%w (a name in it cannot be stored on this card — 7-Zip: %s)", errFeToolFailed, detail)
	}
	if detail == "" {
		detail = err.Error()
	}
	return "", fmt.Errorf("%w (7-Zip: %s)", errFeToolFailed, detail)
}

// feRunWindowsTar unpacks archive into out with Windows' tar.exe, which
// refuses absolute and ".." member names on its own.
func feRunWindowsTar(ctx context.Context, tool, archive, out string) error {
	cmd := exec.CommandContext(ctx, tool, "-x", "-f", archive, "-C", out)
	feToolProcAttr(cmd)
	var errOut feTailBuffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		output := errOut.String()
		if strings.Contains(strings.ToLower(output), "encrypt") {
			return errFeArchiveEncrypted
		}
		detail := emu7zFailure(output)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%w (tar: %s)", errFeToolFailed, detail)
	}
	return nil
}

// feToolSpaceWatch stops a program's run when the card's free space reaches
// the margin: a listing can understate what an archive unpacks to, and 7-Zip
// would otherwise write until the card is full to the last byte. It looks four
// times a second, so a run stops a few megabytes past the line on a card.
func feToolSpaceWatch(ctx context.Context, dir string, stop context.CancelFunc, full *atomic.Bool) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if free, err := feExtractFreeSpace(dir); err == nil && free < feExtractSpaceMargin {
				full.Store(true)
				stop()
				return
			}
		}
	}
}

// fe7ZipPreflight checks a 7-Zip listing against the rules before 7-Zip writes
// anything: an encrypted entry, too many items, more than the card has room
// for, and links — which 7-Zip would create (and an older 7-Zip could follow
// to write elsewhere), and which a FAT card cannot hold anyway — stop it. So
// does a listing 7-Zip cannot give: the listing is the only look at the links
// before they are written.
func fe7ZipPreflight(ctx context.Context, tool, archive, out string) error {
	entries, err := feList7Zip(ctx, tool, archive)
	switch {
	case errors.Is(err, errFeArchiveEncrypted), errors.Is(err, errFeTooMany), errors.Is(err, context.Canceled):
		return err
	case err != nil:
		return fmt.Errorf("%w — 7-Zip could not list what is in it (%v)", errFeToolFailed, err)
	}
	var total int64
	for _, e := range entries {
		switch {
		case e.Encrypted:
			return errFeArchiveEncrypted
		case e.Link:
			return errFeHasLinks
		}
		if !e.Dir && e.Size > 0 {
			total = satAddBytes(total, e.Size)
		}
	}
	if budget, free := feExtractBudget(out); total > budget {
		return feNoSpaceError{need: satAddBytes(total, feExtractSpaceMargin), free: free}
	}
	return nil
}

// feExtractWithTool unpacks archive into out with an external program, then
// tidies what it wrote to the same rules the built-in readers follow.
func feExtractWithTool(ctx context.Context, kind feArchiveKind, archive, out string, st *feExtractStats, meter *feExtractMeter) error {
	tool := feArchiveToolFor(kind)
	if tool.Path == "" && kind == feArchive7z && IsWindows() {
		// The official standalone 7zr.exe, fetched once and checked against
		// the digest GitHub publishes (emu7z.go).
		meter.setPhase("Getting 7-Zip")
		found, err := emu7zTool(ctx, emuWinClient())
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("%w, and it could not be downloaded (%s)", errFeArchiveNeedsTool, feNetReason(err))
		}
		tool = feArchiveTool{Path: found, Kind: feTool7Zip}
	}
	if tool.Path == "" {
		return errFeArchiveNeedsTool
	}
	// A meter the built-in reader started (a zip handed over for its
	// compression) starts again: the program reports in percent, if at all.
	meter.setTotal(0, false, false)
	meter.setDone(0, 0)
	meter.setPhase("Checking")
	if tool.Kind == feTool7Zip {
		if err := fe7ZipPreflight(ctx, tool.Path, archive, out); err != nil {
			return err
		}
	}
	meter.setPhase("Unpacking")
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var full atomic.Bool
	go feToolSpaceWatch(runCtx, out, stop, &full)
	var runErr error
	switch tool.Kind {
	case feTool7Zip:
		warning, err := feRun7Zip(runCtx, tool.Path, archive, out, meter)
		st.Warning = warning
		runErr = err
	case feToolWinTar:
		runErr = feRunWindowsTar(runCtx, tool.Path, archive, out)
	}
	stop()
	if full.Load() {
		return errFeCardFull
	}
	if runErr != nil {
		return runErr
	}
	return feExtractTidy(out, st)
}

// feNetReason is a download failure in a few words.
func feNetReason(err error) string {
	lower := strings.ToLower(err.Error())
	for _, word := range []string{"dial", "lookup", "no such host", "timeout", "connection", "network"} {
		if strings.Contains(lower, word) {
			return "no internet?"
		}
	}
	return ellipsizeRunes(err.Error(), 60)
}

// feExtractTidy walks what a program unpacked into out and holds it to the
// built-in rules: links, junctions, devices and pipes are removed, macOS
// metadata goes, a name that looks like a staging folder is renamed,
// folders are made writable (so the staging folder can always be deleted), and
// the files and bytes that stay are counted.
func feExtractTidy(out string, st *feExtractStats) error {
	// Names that look like a staging folder, at any depth, are renamed first —
	// deepest first, so a folder's path is still right when its turn comes. A
	// link or a reparse point is never walked into.
	var renames []string
	_ = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == out {
			return nil
		}
		if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && runtime.GOOS != "windows" {
			// A folder 7-Zip restored read-only could not be renamed in, or
			// emptied later.
			if info, err := d.Info(); err == nil && info.Mode().Perm()&0o700 != 0o700 {
				_ = os.Chmod(p, info.Mode().Perm()|0o700)
			}
		}
		if feExtractTopName(d.Name()) != d.Name() {
			renames = append(renames, p)
		}
		return nil
	})
	sort.Slice(renames, func(i, j int) bool { return len(renames[i]) > len(renames[j]) })
	for _, p := range renames {
		err := feRenameNoReplace(p, filepath.Join(filepath.Dir(p), feExtractTopName(filepath.Base(p))))
		switch {
		case errors.Is(err, fs.ErrExist):
			// The archive also holds the renamed name: the earlier one stays.
			st.Skips.Duplicate++
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		case err != nil:
			return err
		}
	}
	return filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == out {
			return nil
		}
		typ := d.Type()
		switch {
		case typ&fs.ModeSymlink != 0:
			st.Skips.Links++
			// Remove takes the link itself, never what it points at.
			return os.Remove(p)
		case typ&fs.ModeIrregular != 0:
			if _, err := os.Readlink(p); err == nil {
				// A junction or a mount point: removed as the link it is, and
				// never walked into.
				st.Skips.Links++
				if err := os.Remove(p); err != nil {
					return err
				}
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			// Anything else irregular (a cloud placeholder on Windows) is kept
			// as what it is.
			if d.IsDir() {
				st.Dirs++
			} else {
				st.Files++
			}
			return nil
		case d.IsDir():
			if d.Name() == "__MACOSX" && filepath.Dir(p) == out {
				st.Skips.Junk++
				if err := os.RemoveAll(p); err != nil {
					return err
				}
				return filepath.SkipDir
			}
			if runtime.GOOS != "windows" {
				if info, err := d.Info(); err == nil && info.Mode().Perm()&0o700 != 0o700 {
					_ = os.Chmod(p, info.Mode().Perm()|0o700)
				}
			}
			st.Dirs++
		case typ.IsRegular():
			if d.Name() == ".DS_Store" {
				st.Skips.Junk++
				return os.Remove(p)
			}
			st.Files++
			if info, err := d.Info(); err == nil {
				st.Bytes += info.Size()
			}
		default:
			st.Skips.Special++
			return os.Remove(p)
		}
		return nil
	})
}
