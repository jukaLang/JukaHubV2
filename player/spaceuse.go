package main

// spaceuse.go — "What's using space": one walk of a drive that sizes every
// folder, keeps the largest files of each and the largest files anywhere, so
// the screen (spaceuse_ui.go) can answer "what fills my card?" one folder at a
// time, without walking the card again for every folder opened.
//
// The Storage screen (systool_disk.go) says how full each drive is; this says
// why. Sizes are the files' own sizes, as Files shows them. What the drive
// reports as used beyond them — the file system's own records, the slack of a
// card's large clusters, folders that could not be read — is shown as one row
// of its own at the top of a drive, rather than quietly missing.
//
// The walk does not follow links, does not cross into another file system
// mounted inside the drive (on the handheld, scanning "System" would otherwise
// count the SD card again), and pauses while a game owns the screen. It runs on
// a goroutine of its own: leaving the screen does not stop it, Stop does, and
// the finished tree is kept until the next scan.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// spaceKeepFiles is how many of a folder's own largest files are kept,
	// and spaceKeepTop how many of the whole drive's.
	spaceKeepFiles = 12
	spaceKeepTop   = 60
	// spaceMaxDepth stops a pathological tree (or a loop the link test
	// missed) from recursing for ever.
	spaceMaxDepth = 64
)

// spaceFile is one of a folder's largest files.
type spaceFile struct {
	name string
	size int64
}

// spaceTopFile is one of the drive's largest files.
type spaceTopFile struct {
	path string
	size int64
}

// spaceNode is one folder of the finished tree. It is never written once the
// scan that built it has been published.
type spaceNode struct {
	name   string
	path   string
	size   int64 // every file below, in bytes
	files  int   // every file below
	own    int64 // the files directly in this folder
	ownN   int
	dirs   []*spaceNode // largest first
	big    []spaceFile  // largest first, at most spaceKeepFiles
	parent *spaceNode
	unread bool
}

// spaceProgress is what a running scan has counted so far.
type spaceProgress struct {
	files int
	dirs  int
	bytes int64
	at    string // the folder being read, relative to the drive
}

// spaceResult is one finished (or stopped) scan.
type spaceResult struct {
	root      *spaceNode
	volume    sysToolVolume
	mountRoot bool // the scanned path is a whole file system, not a folder of one
	top       []spaceTopFile
	files     int
	dirs      int
	unread    int
	skipped   int
	started   time.Time
	finished  time.Time
	stopped   bool
	err       string
}

// spaceScan is the scanner's state. version changes whenever something the
// screen shows changes.
var spaceScan struct {
	mu       sync.Mutex
	gen      uint64
	version  uint64
	running  bool
	cancel   context.CancelFunc
	volume   sysToolVolume
	progress spaceProgress
	result   *spaceResult
}

// spaceScanSnapshot copies what the screen needs.
func spaceScanSnapshot() (running bool, vol sysToolVolume, prog spaceProgress, res *spaceResult, version uint64) {
	spaceScan.mu.Lock()
	defer spaceScan.mu.Unlock()
	return spaceScan.running, spaceScan.volume, spaceScan.progress, spaceScan.result, spaceScan.version
}

// spaceScanStart starts a scan of vol. It reports false while one runs.
func spaceScanStart(vol sysToolVolume) bool {
	if strings.TrimSpace(vol.Path) == "" {
		return false
	}
	spaceScan.mu.Lock()
	if spaceScan.running {
		spaceScan.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	spaceScan.gen++
	gen := spaceScan.gen
	spaceScan.running = true
	spaceScan.cancel = cancel
	spaceScan.volume = vol
	spaceScan.progress = spaceProgress{}
	spaceScan.version++
	spaceScan.mu.Unlock()
	go spaceScanRun(ctx, cancel, gen, vol)
	return true
}

// spaceScanStop stops a running scan; what it counted so far is kept as a
// stopped result.
func spaceScanStop() {
	spaceScan.mu.Lock()
	defer spaceScan.mu.Unlock()
	if spaceScan.running && spaceScan.cancel != nil {
		spaceScan.cancel()
	}
}

// spaceScanRun walks vol and publishes the result.
func spaceScanRun(ctx context.Context, cancel context.CancelFunc, gen uint64, vol sysToolVolume) {
	defer cancel()
	res := &spaceResult{volume: vol, started: time.Now()}
	defer func() {
		if r := recover(); r != nil {
			logWarn("spaceuse", "the scan stopped: %v", r)
			res.err = "The scan stopped unexpectedly"
			res.root = nil
			spaceScanPublish(gen, res)
		}
	}()
	mounts := spaceMountPoints()
	root := filepath.Clean(vol.Path)
	res.mountRoot = spaceIsMountRoot(root, mounts)
	w := &spaceWalker{ctx: ctx, gen: gen, res: res, mounts: mounts, root: root, lastPub: time.Now()}
	node := &spaceNode{name: vol.Name, path: root}
	w.walk(node, 0)
	res.root = node
	res.finished = time.Now()
	res.stopped = ctx.Err() != nil
	spaceScanPublish(gen, res)
	logInfo("spaceuse", "scanned %s: %d files, %d folders, %s in %.1f s (stopped %v)",
		vol.Name, res.files, res.dirs, formatSize(node.size), res.finished.Sub(res.started).Seconds(), res.stopped)
}

// spaceScanPublish makes res the last result, if gen is still the scan.
func spaceScanPublish(gen uint64, res *spaceResult) {
	spaceScan.mu.Lock()
	defer spaceScan.mu.Unlock()
	if spaceScan.gen != gen {
		return
	}
	spaceScan.running = false
	spaceScan.cancel = nil
	spaceScan.result = res
	spaceScan.version++
}

// spaceWalker carries one scan's counters through the recursion.
type spaceWalker struct {
	ctx     context.Context
	gen     uint64
	res     *spaceResult
	mounts  map[string]bool
	root    string
	bytes   int64
	seen    int
	lastPub time.Time
}

// walk sizes node and everything below it.
func (w *spaceWalker) walk(node *spaceNode, depth int) {
	if w.ctx.Err() != nil {
		return
	}
	entries, err := os.ReadDir(node.path)
	if err != nil && len(entries) == 0 {
		node.unread = true
		w.res.unread++
		return
	}
	w.res.dirs++
	for _, e := range entries {
		if w.ctx.Err() != nil {
			return
		}
		w.tick(node)
		full := filepath.Join(node.path, e.Name())
		typ := e.Type()
		if typ&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			// A link takes no space worth naming, and following one could
			// count a folder twice or leave the drive.
			w.res.skipped++
			continue
		}
		if e.IsDir() {
			if depth+1 >= spaceMaxDepth || w.mounts[full] {
				w.res.skipped++
				continue
			}
			child := &spaceNode{name: e.Name(), path: full, parent: node}
			w.walk(child, depth+1)
			node.dirs = append(node.dirs, child)
			node.size += child.size
			node.files += child.files
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		size := info.Size()
		if size < 0 {
			size = 0
		}
		node.own += size
		node.ownN++
		node.size += size
		node.files++
		w.res.files++
		w.bytes += size
		node.big = spaceKeepLargest(node.big, spaceFile{name: e.Name(), size: size}, spaceKeepFiles)
		w.res.top = spaceKeepTopFile(w.res.top, spaceTopFile{path: full, size: size}, spaceKeepTop)
	}
	sort.SliceStable(node.dirs, func(i, j int) bool { return node.dirs[i].size > node.dirs[j].size })
}

// tick publishes the progress every few hundred entries, and waits while a
// game owns the screen: the card is the game's then.
func (w *spaceWalker) tick(node *spaceNode) {
	w.seen++
	if w.seen%256 != 0 {
		return
	}
	for gameScreenSuspended() && w.ctx.Err() == nil {
		time.Sleep(500 * time.Millisecond)
	}
	if time.Since(w.lastPub) < 200*time.Millisecond {
		return
	}
	w.lastPub = time.Now()
	rel, err := filepath.Rel(w.root, node.path)
	if err != nil || rel == "." {
		rel = ""
	}
	spaceScan.mu.Lock()
	if spaceScan.gen == w.gen {
		spaceScan.progress = spaceProgress{files: w.res.files, dirs: w.res.dirs, bytes: w.bytes, at: filepath.ToSlash(rel)}
		spaceScan.version++
	}
	spaceScan.mu.Unlock()
}

// spaceKeepLargest inserts f into list (largest first) and keeps at most max.
// Pure.
func spaceKeepLargest(list []spaceFile, f spaceFile, max int) []spaceFile {
	if len(list) >= max && f.size <= list[len(list)-1].size {
		return list
	}
	i := sort.Search(len(list), func(i int) bool { return list[i].size < f.size })
	list = append(list, spaceFile{})
	copy(list[i+1:], list[i:])
	list[i] = f
	if len(list) > max {
		list = list[:max]
	}
	return list
}

// spaceKeepTopFile is spaceKeepLargest for the drive's list. Pure.
func spaceKeepTopFile(list []spaceTopFile, f spaceTopFile, max int) []spaceTopFile {
	if len(list) >= max && f.size <= list[len(list)-1].size {
		return list
	}
	i := sort.Search(len(list), func(i int) bool { return list[i].size < f.size })
	list = append(list, spaceTopFile{})
	copy(list[i+1:], list[i:])
	list[i] = f
	if len(list) > max {
		list = list[:max]
	}
	return list
}

// spaceMountPoints lists the mount points Linux knows (the handheld and a
// Linux computer); nil elsewhere. The walk does not step into one: it is
// another file system, counted on its own drive.
func spaceMountPoints() map[string]bool {
	if runtime.GOOS == "windows" {
		return nil
	}
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		if data, err = os.ReadFile("/proc/mounts"); err != nil {
			return nil
		}
	}
	return spaceParseMounts(string(data))
}

// spaceParseMounts reads the mount points out of a mounts table, undoing the
// octal escapes the kernel writes for spaces and tabs. Pure.
func spaceParseMounts(table string) map[string]bool {
	out := map[string]bool{}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		out[filepath.Clean(unescape.Replace(fields[1]))] = true
	}
	return out
}

// spaceIsMountRoot reports whether path is a whole file system: "/", a drive
// root on Windows, or a mount point. Only then does "used minus the files"
// mean the file system's own overhead rather than other folders.
func spaceIsMountRoot(path string, mounts map[string]bool) bool {
	clean := filepath.Clean(path)
	if filepath.Dir(clean) == clean {
		return true
	}
	return mounts[clean]
}

// spaceUnaccounted is what the drive reports as used beyond the files the
// scan sized (never negative). Pure.
func spaceUnaccounted(res *spaceResult) int64 {
	if res == nil || res.root == nil || res.volume.Total == 0 {
		return 0
	}
	used := int64(res.volume.used())
	if d := used - res.root.size; d > 0 {
		return d
	}
	return 0
}
