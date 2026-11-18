package main

// aitools_files.go — Juka's file tools. They reach only aiFileRoots (the SD
// card, the ROM folders, JukaHub's data; the home folder on a PC) through
// aiPath, never a system folder. Moving and deleting ask first; "delete"
// moves into a trash folder on the same card (<root>/.jukahub-trash/<time>/)
// unless the model asked for a permanent delete, which is destructive and
// always asks.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// aiTrashDirName is the trash folder at the top of each storage root.
const aiTrashDirName = ".jukahub-trash"

// aiWalkMax bounds one walk of the card, so a huge library cannot stall a
// tool for minutes.
const aiWalkMax = 60000

func aiFileTools() []aiTool {
	return []aiTool{
		{
			Name: "list_dir",
			Desc: "List a folder on the SD card or in JukaHub's data: names, sizes, folders first. Paths may be absolute or relative to the card. Call with no path to see the storage roots.",
			Params: aiObj(map[string]any{
				"path":  aiStrP("Folder to list; empty = the storage roots"),
				"limit": aiIntP("Most entries to return, 1-150 (default 60)"),
			}),
			Risk: aiRiskRead,
			Run:  aiListDir,
		},
		{
			Name: "find_files",
			Desc: "Search files by name under a folder (default: the whole card). With duplicates=true, finds identical copies (same size and same content) instead, e.g. duplicate ROMs.",
			Params: aiObj(map[string]any{
				"query":       aiStrP("Text the file name contains (case-insensitive); may be empty with ext or duplicates"),
				"path":        aiStrP("Folder to search; empty = every storage root"),
				"ext":         aiStrListP("Only these extensions, e.g. [\"zip\",\"7z\"]"),
				"min_size_mb": aiIntP("Only files at least this big"),
				"duplicates":  aiBoolP("Find identical copies instead of a name search"),
				"limit":       aiIntP("Most results, 1-80 (default 40)"),
			}),
			Risk: aiRiskRead,
			Run:  aiFindFiles,
		},
		{
			Name: "disk_usage",
			Desc: "What fills a folder (default: the card): the biggest sub-folders and the biggest files.",
			Params: aiObj(map[string]any{
				"path":  aiStrP("Folder to measure; empty = the main storage root"),
				"limit": aiIntP("How many folders and files to list, 1-30 (default 10)"),
			}),
			Risk: aiRiskRead,
			Run:  aiDiskUsage,
		},
		{
			Name: "read_text_file",
			Desc: "Read a text file (a config, a log, a script). Binary files and files with keys or passwords are refused.",
			Params: aiObj(map[string]any{
				"path":   aiStrP("The file"),
				"tail":   aiBoolP("Read the end instead of the start"),
				"filter": aiStrP("Only lines containing this text"),
			}, "path"),
			Risk: aiRiskRead,
			Run:  aiReadTextFile,
		},
		{
			Name: "move_files",
			Desc: "Move files or folders into another folder on the same card (created if missing). The user is asked first.",
			Params: aiObj(map[string]any{
				"paths":    aiStrListP("What to move"),
				"dest_dir": aiStrP("The folder to move them into"),
			}, "paths", "dest_dir"),
			Risk: aiRiskChange,
			Run:  aiMoveFiles,
		},
		{
			Name: "rename_file",
			Desc: "Rename one file or folder (same folder, new name). The user is asked first.",
			Params: aiObj(map[string]any{
				"path":     aiStrP("The file or folder"),
				"new_name": aiStrP("The new name only, no folders"),
			}, "path", "new_name"),
			Risk: aiRiskChange,
			Run:  aiRenameFile,
		},
		{
			Name: "delete_files",
			Desc: "Delete files or folders: they are moved to the card's trash folder (.jukahub-trash), which frees no space until it is emptied. permanent=true deletes for good (use it to empty the trash, or when the user asked to free space now). The user is asked first.",
			Params: aiObj(map[string]any{
				"paths":     aiStrListP("What to delete"),
				"permanent": aiBoolP("Delete for good instead of moving to the trash"),
			}, "paths"),
			Risk: aiRiskChange,
			Run:  aiDeleteFiles,
		},
	}
}

// aiRootOf is the most specific storage root holding p.
func aiRootOf(p string, roots []string) string {
	best := ""
	for _, r := range roots {
		if isPathWithin(p, r) && len(r) > len(best) {
			best = r
		}
	}
	return best
}

// aiTreeSize is the bytes under p (a file's own size), walked with a bound.
func aiTreeSize(ctx context.Context, p string) (size int64, files int, complete bool) {
	info, err := os.Lstat(p)
	if err != nil {
		return 0, 0, false
	}
	if !info.IsDir() {
		return info.Size(), 1, true
	}
	n := 0
	complete = true
	_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		n++
		if n > aiWalkMax || ctx.Err() != nil {
			complete = false
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				size += fi.Size()
				files++
			}
		}
		return nil
	})
	return size, files, complete
}

// ── list_dir ─────────────────────────────────────────────────────────────

func aiListDir(ctx context.Context, a aiArgs) aiStep {
	roots := aiFileRoots()
	if a.str("path") == "" {
		var b strings.Builder
		b.WriteString("Storage roots Juka can reach:\n")
		for _, r := range roots {
			b.WriteString(r + "\n")
		}
		return aiStep{Result: b.String(), Status: "Looked at the storage roots"}
	}
	dir, err := aiResolvePath(a.str("path"), roots)
	if err != nil {
		return aiFail("%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return aiFail("can't list %s: %v", dir, err)
	}
	limit := a.num("limit", 60, 1, 150)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %d entries\n", dir, len(entries))
	for i, e := range entries {
		if i >= limit {
			fmt.Fprintf(&b, "… %d more\n", len(entries)-limit)
			break
		}
		if e.IsDir() {
			fmt.Fprintf(&b, "[dir] %s/\n", e.Name())
			continue
		}
		size := int64(0)
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		fmt.Fprintf(&b, "%s  %s\n", e.Name(), aiSize(size))
	}
	return aiStep{Result: b.String(), Status: "Listed " + filepath.Base(dir)}
}

// ── find_files ───────────────────────────────────────────────────────────

func aiSearchRoots(a aiArgs) ([]string, error) {
	roots := aiFileRoots()
	if p := a.str("path"); p != "" {
		dir, err := aiResolvePath(p, roots)
		if err != nil {
			return nil, err
		}
		return []string{dir}, nil
	}
	// Nested roots (the ROM folder inside the card) would be walked twice.
	var out []string
	for _, r := range roots {
		nested := false
		for _, o := range roots {
			if o != r && isPathWithin(r, o) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, r)
		}
	}
	return out, nil
}

func aiFindFiles(ctx context.Context, a aiArgs) aiStep {
	roots, err := aiSearchRoots(a)
	if err != nil {
		return aiFail("%v", err)
	}
	query := strings.ToLower(a.str("query"))
	var exts []string
	for _, e := range a.list("ext") {
		exts = append(exts, "."+strings.TrimPrefix(strings.ToLower(e), "."))
	}
	minSize := int64(a.num("min_size_mb", 0, 0, 1<<20)) << 20
	limit := a.num("limit", 40, 1, 80)
	dups := a.flag("duplicates")
	if !dups && query == "" && len(exts) == 0 && minSize == 0 {
		return aiFail("give a name to look for, an extension or a minimum size")
	}
	type hit struct {
		path string
		size int64
	}
	var hits []hit
	walked, cut := 0, false
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			walked++
			if walked > aiWalkMax || ctx.Err() != nil {
				cut = true
				return filepath.SkipAll
			}
			if d.IsDir() {
				if path != root && (d.Name() == aiTrashDirName || aiSystemPath(path)) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			name := strings.ToLower(d.Name())
			if query != "" && !strings.Contains(name, query) {
				return nil
			}
			if len(exts) > 0 {
				ok := false
				for _, e := range exts {
					if strings.HasSuffix(name, e) {
						ok = true
						break
					}
				}
				if !ok {
					return nil
				}
			}
			fi, err := d.Info()
			if err != nil || fi.Size() < minSize {
				return nil
			}
			if (!dups && len(hits) >= limit*4) || len(hits) >= 20000 {
				cut = true
				return filepath.SkipAll
			}
			hits = append(hits, hit{path, fi.Size()})
			return nil
		})
	}
	var b strings.Builder
	if dups {
		bySize := map[int64][]string{}
		for _, h := range hits {
			if h.size > 0 {
				bySize[h.size] = append(bySize[h.size], h.path)
			}
		}
		type group struct {
			size  int64
			paths []string
		}
		var groups []group
		for size, paths := range bySize {
			if len(paths) < 2 || ctx.Err() != nil {
				continue
			}
			byHash := map[string][]string{}
			for _, p := range paths {
				if h := aiQuickHash(p, size); h != "" {
					byHash[h] = append(byHash[h], p)
				}
			}
			for _, same := range byHash {
				if len(same) > 1 {
					sort.Strings(same)
					groups = append(groups, group{size, same})
				}
			}
		}
		sort.Slice(groups, func(i, j int) bool {
			return groups[i].size*int64(len(groups[i].paths)-1) > groups[j].size*int64(len(groups[j].paths)-1)
		})
		var wasted int64
		for _, g := range groups {
			wasted += g.size * int64(len(g.paths)-1)
		}
		fmt.Fprintf(&b, "%d sets of identical files; extra copies use %s.\n", len(groups), aiSize(wasted))
		shown := 0
		for _, g := range groups {
			if shown >= limit {
				b.WriteString("… more sets not shown\n")
				break
			}
			fmt.Fprintf(&b, "%s each:\n", aiSize(g.size))
			for _, p := range g.paths {
				b.WriteString("  " + p + "\n")
			}
			shown++
		}
	} else {
		sort.Slice(hits, func(i, j int) bool { return hits[i].size > hits[j].size })
		fmt.Fprintf(&b, "%d matches (biggest first):\n", len(hits))
		for i, h := range hits {
			if i >= limit {
				fmt.Fprintf(&b, "… %d more\n", len(hits)-limit)
				break
			}
			fmt.Fprintf(&b, "%s  %s\n", h.path, aiSize(h.size))
		}
	}
	if cut {
		b.WriteString("(search stopped early: too many files or out of time)\n")
	}
	status := "Searched the card"
	if dups {
		status = "Looked for duplicate files"
	}
	return aiStep{Result: b.String(), Status: status}
}

// aiQuickHash fingerprints a file by its first and last 64 KB plus its size:
// two files of the same size and fingerprint are taken as copies.
func aiQuickHash(path string, size int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha1.New()
	fmt.Fprintf(h, "%d:", size)
	const chunk = 64 << 10
	if _, err := io.CopyN(h, f, chunk); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	if size > 2*chunk {
		if _, err := f.Seek(size-chunk, io.SeekStart); err != nil {
			return ""
		}
		if _, err := io.CopyN(h, f, chunk); err != nil && !errors.Is(err, io.EOF) {
			return ""
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ── disk_usage ───────────────────────────────────────────────────────────

func aiDiskUsage(ctx context.Context, a aiArgs) aiStep {
	roots := aiFileRoots()
	dir := ""
	if p := a.str("path"); p != "" {
		d, err := aiResolvePath(p, roots)
		if err != nil {
			return aiFail("%v", err)
		}
		dir = d
	} else if len(roots) > 0 {
		dir = roots[0]
	} else {
		return aiFail("no storage is reachable")
	}
	limit := a.num("limit", 10, 1, 30)
	wctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	type item struct {
		path string
		size int64
	}
	children := map[string]int64{}
	var files []item
	var total int64
	walked, cut := 0, false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		walked++
		if walked > aiWalkMax || wctx.Err() != nil {
			cut = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			if path != dir && aiSystemPath(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		total += fi.Size()
		rel, err := filepath.Rel(dir, path)
		if err == nil {
			first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
			if strings.Contains(filepath.ToSlash(rel), "/") {
				children[first+"/"] += fi.Size()
			}
		}
		files = append(files, item{path, fi.Size()})
		if len(files) > 4*limit+64 {
			sort.Slice(files, func(i, j int) bool { return files[i].size > files[j].size })
			files = files[:limit]
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].size > files[j].size })
	var folders []item
	for name, size := range children {
		folders = append(folders, item{name, size})
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].size > folders[j].size })
	var b strings.Builder
	fmt.Fprintf(&b, "%s holds %s\n", dir, aiSize(total))
	if total, free, err := sysToolVolumeUsage(dir); err == nil && total > 0 {
		fmt.Fprintf(&b, "Its drive: %s free of %s\n", aiSize(int64(free)), aiSize(int64(total)))
	}
	b.WriteString("Biggest folders:\n")
	for i, f := range folders {
		if i >= limit {
			break
		}
		fmt.Fprintf(&b, "  %s  %s\n", f.path, aiSize(f.size))
	}
	b.WriteString("Biggest files:\n")
	for i, f := range files {
		if i >= limit {
			break
		}
		fmt.Fprintf(&b, "  %s  %s\n", f.path, aiSize(f.size))
	}
	if cut {
		b.WriteString("(stopped early: the figures cover part of the folder)\n")
	}
	return aiStep{Result: b.String(), Status: "Checked storage"}
}

// ── read_text_file ───────────────────────────────────────────────────────

func aiReadTextFile(ctx context.Context, a aiArgs) aiStep {
	p, err := aiPath(a.str("path"))
	if err != nil {
		return aiFail("%v", err)
	}
	if aiSecretFile(p) {
		return aiFail("that file holds keys or passwords; Juka doesn't read it")
	}
	info, err := os.Stat(p)
	if err != nil {
		return aiFail("%v", err)
	}
	if info.IsDir() {
		return aiFail("that is a folder; use list_dir")
	}
	const window = 64 << 10
	f, err := os.Open(p)
	if err != nil {
		return aiFail("%v", err)
	}
	defer f.Close()
	if a.flag("tail") && info.Size() > window {
		if _, err := f.Seek(info.Size()-window, io.SeekStart); err != nil {
			return aiFail("%v", err)
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, window))
	if err != nil {
		return aiFail("%v", err)
	}
	head := data
	if len(head) > 8<<10 {
		head = head[:8<<10]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return aiFail("%s is not a text file", filepath.Base(p))
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if filter := strings.ToLower(a.str("filter")); filter != "" {
		var kept []string
		for _, l := range strings.Split(text, "\n") {
			if strings.Contains(strings.ToLower(l), filter) {
				kept = append(kept, l)
			}
		}
		text = strings.Join(kept, "\n")
		if text == "" {
			text = "(no line contains " + strconvQuote(filter) + ")"
		}
	} else if a.flag("tail") && len(text) > aiResultMax-200 {
		text = "…\n" + text[len(text)-(aiResultMax-200):]
	}
	return aiStep{Result: fmt.Sprintf("%s (%s):\n%s", p, aiSize(info.Size()), text), Status: "Read " + filepath.Base(p)}
}

// ── changes ──────────────────────────────────────────────────────────────

// aiChangeTargets resolves and checks every path of a move or delete.
func aiChangeTargets(paths []string, roots []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, errors.New("no paths given")
	}
	if len(paths) > 200 {
		return nil, errors.New("too many paths at once (200 at most)")
	}
	var out []string
	seen := map[string]bool{}
	for _, raw := range paths {
		p, err := aiResolvePath(raw, roots)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", raw, err)
		}
		if _, err := os.Lstat(p); err != nil {
			return nil, fmt.Errorf("%s doesn't exist", raw)
		}
		if why := aiProtectedForChange(p, roots); why != "" {
			return nil, fmt.Errorf("%s: %s", raw, why)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// aiDescribe is "3 items (1.2 GiB)" or the one name.
func aiDescribe(ctx context.Context, paths []string) (string, int64) {
	var total int64
	for _, p := range paths {
		s, _, _ := aiTreeSize(ctx, p)
		total += s
	}
	if len(paths) == 1 {
		return "“" + filepath.Base(paths[0]) + "” (" + aiSize(total) + ")", total
	}
	return fmt.Sprintf("%d items (%s)", len(paths), aiSize(total)), total
}

func aiMoveFiles(ctx context.Context, a aiArgs) aiStep {
	roots := aiFileRoots()
	srcs, err := aiChangeTargets(a.list("paths"), roots)
	if err != nil {
		return aiFail("%v", err)
	}
	dest, err := aiResolvePath(a.str("dest_dir"), roots)
	if err != nil {
		return aiFail("destination: %v", err)
	}
	if why := aiProtectedDest(dest); why != "" {
		return aiFail("destination: %s", why)
	}
	if info, err := os.Stat(dest); err == nil && !info.IsDir() {
		return aiFail("the destination is a file, not a folder")
	}
	for _, s := range srcs {
		if isPathWithin(dest, s) {
			return aiFail("can't move a folder into itself")
		}
	}
	what, _ := aiDescribe(ctx, srcs)
	return aiStep{
		Confirm: "Move " + what + " to " + dest + "?",
		Apply: func(ctx context.Context) string {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return "Error: " + err.Error()
			}
			return aiRenameEach(srcs, func(src string) string { return filepath.Join(dest, filepath.Base(src)) }, "Moved")
		},
	}
}

func aiRenameFile(ctx context.Context, a aiArgs) aiStep {
	roots := aiFileRoots()
	srcs, err := aiChangeTargets(a.list("path"), roots)
	if err != nil {
		return aiFail("%v", err)
	}
	name := strings.TrimSpace(a.str("new_name"))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:*?"<>|`) || len(name) > 200 {
		return aiFail("give a plain file name without folders or special characters")
	}
	src := srcs[0]
	dest := filepath.Join(filepath.Dir(src), name)
	if _, err := os.Lstat(dest); err == nil && !strings.EqualFold(dest, src) {
		return aiFail("%s already exists", name)
	}
	return aiStep{
		Confirm: "Rename “" + filepath.Base(src) + "” to “" + name + "”?",
		Apply: func(ctx context.Context) string {
			if err := os.Rename(src, dest); err != nil {
				return "Error: " + err.Error()
			}
			logInfo("aitools", "renamed a file at the user's OK")
			return "Renamed to " + name + "."
		},
	}
}

// aiRenameEach renames every source to target(src), refusing to overwrite.
func aiRenameEach(srcs []string, target func(string) string, verb string) string {
	done := 0
	var failed []string
	for _, src := range srcs {
		dst := target(src)
		if _, err := os.Lstat(dst); err == nil {
			failed = append(failed, filepath.Base(src)+": already exists there")
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			msg := err.Error()
			var le *os.LinkError
			if errors.As(err, &le) && strings.Contains(strings.ToLower(msg), "cross-device") {
				msg = "it is on another card or drive"
			}
			failed = append(failed, filepath.Base(src)+": "+msg)
			continue
		}
		done++
	}
	logInfo("aitools", "%s %d of %d items at the user's OK", strings.ToLower(verb), done, len(srcs))
	out := fmt.Sprintf("%s: %d of %d.", verb, done, len(srcs))
	if len(failed) > 0 {
		out += " Not done: " + strings.Join(failed, "; ")
	}
	return out
}

func aiDeleteFiles(ctx context.Context, a aiArgs) aiStep {
	roots := aiFileRoots()
	srcs, err := aiChangeTargets(a.list("paths"), roots)
	if err != nil {
		return aiFail("%v", err)
	}
	what, _ := aiDescribe(ctx, srcs)
	if a.flag("permanent") {
		return aiStep{
			Destructive: true,
			Confirm:     "Delete " + what + " for good? This can't be undone.",
			Apply: func(ctx context.Context) string {
				done := 0
				var failed []string
				for _, p := range srcs {
					if err := os.RemoveAll(p); err != nil {
						failed = append(failed, filepath.Base(p)+": "+err.Error())
						continue
					}
					done++
				}
				logInfo("aitools", "deleted %d of %d items for good at the user's OK", done, len(srcs))
				out := fmt.Sprintf("Deleted %d of %d for good.", done, len(srcs))
				if len(failed) > 0 {
					out += " Not done: " + strings.Join(failed, "; ")
				}
				return out
			},
		}
	}
	stamp := time.Now().Format("20060102-150405")
	return aiStep{
		Confirm: "Move " + what + " to the trash? It stays on the card until the trash is emptied.",
		Apply: func(ctx context.Context) string {
			return aiRenameEach(srcs, func(src string) string {
				root := aiRootOf(src, roots)
				rel, err := filepath.Rel(root, src)
				if err != nil || root == "" {
					rel = filepath.Base(src)
				}
				dst := filepath.Join(root, aiTrashDirName, stamp, rel)
				_ = os.MkdirAll(filepath.Dir(dst), 0o755)
				return dst
			}, "Moved to the trash")
		},
	}
}
