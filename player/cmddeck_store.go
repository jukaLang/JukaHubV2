package main

// cmddeck_store.go — the Command Deck's favorites, macros and recent slot
// values, kept in one small JSON file in the app's data directory
// (terminal_favorites.json).
//
// A favorite is a name and one or more command lines; more than one line is a
// macro, run as one shell line joined with " && " so it stops at the first
// failure (sh and cmd.exe both read && that way). Recent values remember the
// last folders, files and choices picked per slot, so the second time is one
// press.
//
// The file is outside data as far as the reader is concerned: it is parsed
// with hard caps (entries, lines, bytes), control characters are dropped, and
// a file that does not parse is moved aside as .bad and replaced by an empty
// store instead of breaking the Terminal. The file is read off the render
// thread; writes are atomic and happen off it too; the newest snapshot always
// wins.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	cmdStoreFileName   = "terminal_favorites.json"
	cmdStoreVersion    = 1
	cmdFavMax          = 40   // favorites kept
	cmdFavMaxLines     = 8    // lines in one macro
	cmdFavMaxLineBytes = 1024 // one command line
	cmdFavMaxNameBytes = 80
	cmdRecentMaxKeys   = 64
	cmdRecentMaxValues = 5
	cmdRecentMaxBytes  = 512
	cmdStoreMaxFile    = 256 << 10 // a bigger file is not ours
)

// cmdFav is one saved command or macro.
type cmdFav struct {
	Name  string   `json:"name"`
	Lines []string `json:"lines"`
}

// cmdDeckStore is the file's content.
type cmdDeckStore struct {
	Version   int                 `json:"version"`
	Favorites []cmdFav            `json:"favorites"`
	Recent    map[string][]string `json:"recent,omitempty"`
}

// cmdStore is the loaded store. Render thread only.
var cmdStore struct {
	loaded bool
	// noSave: the file exists but could not be read (a card hiccup, a
	// permission). Saving the empty store over it would lose every
	// favorite, so this session keeps its changes in memory only.
	noSave bool
	data   cmdDeckStore

	// loading: the file is being read off the render thread
	// (cmdStoreLoadStart). Until it lands, changes are kept in data and
	// merged with what was read (cmdStoreMerge), and a save waits for it
	// (saveDue), so a half-known store never overwrites the file.
	loading bool
	saveDue bool
}

// cmdStorePathFn is a variable so tests can point it at a temporary folder.
var cmdStorePathFn = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, cmdStoreFileName)
}

// cmdCleanLine trims a stored line and refuses one with control characters
// or over the size cap.
func cmdCleanLine(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > max {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return s, true
}

// cmdSanitizeStore applies every cap to a decoded store: the same rules for
// what came from disk and what is about to be written.
func cmdSanitizeStore(in cmdDeckStore) cmdDeckStore {
	out := cmdDeckStore{Version: cmdStoreVersion}
	seen := map[string]bool{}
	for _, f := range in.Favorites {
		if len(out.Favorites) >= cmdFavMax {
			break
		}
		var lines []string
		for _, l := range f.Lines {
			if len(lines) >= cmdFavMaxLines {
				break
			}
			if c, ok := cmdCleanLine(l, cmdFavMaxLineBytes); ok {
				lines = append(lines, c)
			}
		}
		if len(lines) == 0 {
			continue
		}
		key := strings.Join(lines, "\n")
		if seen[key] {
			continue
		}
		seen[key] = true
		name, ok := cmdCleanLine(f.Name, cmdFavMaxNameBytes)
		if !ok {
			name = cmdFavDefaultName(lines)
		}
		out.Favorites = append(out.Favorites, cmdFav{Name: name, Lines: lines})
	}
	if len(in.Recent) > 0 {
		keys := make([]string, 0, len(in.Recent))
		for k := range in.Recent {
			if c, ok := cmdCleanLine(k, 64); ok && c == k {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		if len(keys) > cmdRecentMaxKeys {
			keys = keys[:cmdRecentMaxKeys]
		}
		for _, k := range keys {
			var vals []string
			for _, v := range in.Recent[k] {
				if len(vals) >= cmdRecentMaxValues {
					break
				}
				if c, ok := cmdCleanLine(v, cmdRecentMaxBytes); ok {
					vals = append(vals, c)
				}
			}
			if len(vals) > 0 {
				if out.Recent == nil {
					out.Recent = map[string][]string{}
				}
				out.Recent[k] = vals
			}
		}
	}
	return out
}

// cmdFavDefaultName names a favorite after its first line ("+2" for a macro).
func cmdFavDefaultName(lines []string) string {
	if len(lines) == 0 {
		return "Command"
	}
	name := lines[0]
	if len(name) > 60 {
		name = trimToRuneBoundary(name, 60) + "…"
	}
	if len(lines) > 1 {
		name += " (+" + itoaSmall(len(lines)-1) + ")"
	}
	return name
}

// trimToRuneBoundary cuts s to at most n bytes without splitting a rune.
func trimToRuneBoundary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// itoaSmall formats a small non-negative int without fmt.
func itoaSmall(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 && i > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// parseCmdDeckStore decodes the file. An empty file is an empty store; a
// file that is not our JSON is an error (the caller moves it aside).
func parseCmdDeckStore(data []byte) (cmdDeckStore, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return cmdDeckStore{Version: cmdStoreVersion}, nil
	}
	if len(data) > cmdStoreMaxFile {
		return cmdDeckStore{Version: cmdStoreVersion}, errors.New("favorites file too large")
	}
	var raw cmdDeckStore
	if err := json.Unmarshal(data, &raw); err != nil {
		return cmdDeckStore{Version: cmdStoreVersion}, err
	}
	return cmdSanitizeStore(raw), nil
}

// formatCmdDeckStore encodes the store for the file.
func formatCmdDeckStore(s cmdDeckStore) ([]byte, error) {
	s = cmdSanitizeStore(s)
	return json.MarshalIndent(s, "", "  ")
}

// cmdStoreLoadStart reads the store once per session, off the render thread
// (a card read can stall): the Terminal starts it on entry and every use
// asks again, which costs nothing once it is under way. The result lands on
// the main thread (tbxPost) like the deck's device probe. Main thread.
func cmdStoreLoadStart() {
	if cmdStore.loaded || cmdStore.loading {
		return
	}
	path := cmdStorePathFn()
	if path == "" {
		cmdStore.loaded = true
		return
	}
	cmdStore.loading = true
	go func() {
		s, err := cmdStoreRead(path)
		tbxPost(func() { cmdStoreLanded(s, err) })
	}()
}

// cmdStoreRead reads and parses the file at path. A missing file is an empty
// store; one that does not parse is moved aside as .bad and is an empty store
// too. The error is a file that exists but could not be read. Any goroutine.
func cmdStoreRead(path string) (cmdDeckStore, error) {
	empty := cmdDeckStore{Version: cmdStoreVersion}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return empty, err
	}
	s, perr := parseCmdDeckStore(data)
	if perr != nil {
		logWarn("terminal", "favorites file unreadable, moved aside: %v", perr)
		_ = os.Rename(path, path+".bad")
		return empty, nil
	}
	return s, nil
}

// cmdStoreLanded takes the read store on the main thread: what changed in the
// meantime is merged in, a save that waited is made, and the deck's rows are
// rebuilt (the Favorites category fills in). Main thread.
func cmdStoreLanded(s cmdDeckStore, err error) {
	cmdStore.loading = false
	cmdStore.loaded = true
	if err != nil {
		logWarn("terminal", "could not read favorites, not saving them this session: %v", err)
		cmdStore.noSave = true
	} else {
		cmdStore.data = cmdStoreMerge(s, cmdStore.data)
	}
	if cmdStore.saveDue {
		cmdStore.saveDue = false
		cmdStoreSave()
	}
	if cmdDeck.entries != nil {
		cmdDeckRebuild()
	}
}

// cmdStoreMerge puts what changed in memory before the file was read (mem) on
// top of what was read (disk): favorites saved meanwhile first (newest first,
// as cmdFavAdd keeps them), values picked meanwhile at the front of their
// slot's list. Duplicates are dropped and the caps applied (cmdSanitizeStore).
func cmdStoreMerge(disk, mem cmdDeckStore) cmdDeckStore {
	if len(mem.Favorites) == 0 && len(mem.Recent) == 0 {
		return disk
	}
	out := cmdDeckStore{Version: cmdStoreVersion, Recent: map[string][]string{}}
	out.Favorites = append(append([]cmdFav(nil), mem.Favorites...), disk.Favorites...)
	for k, v := range disk.Recent {
		out.Recent[k] = v
	}
	for k, v := range mem.Recent {
		var vals []string
		for _, x := range append(append([]string(nil), v...), disk.Recent[k]...) {
			if !slices.Contains(vals, x) {
				vals = append(vals, x)
			}
		}
		out.Recent[k] = vals
	}
	return cmdSanitizeStore(out)
}

var (
	cmdStoreSaveMu  sync.Mutex
	cmdStoreSaveGen atomic.Uint64
)

// cmdStoreSave writes the store off the render thread. Saves are numbered; a
// write that is no longer the newest is skipped, so the file always ends as
// the last state, whatever order the goroutines run in.
func cmdStoreSave() {
	if cmdStore.noSave {
		return
	}
	if !cmdStore.loaded {
		// The file is still being read: save once it is merged in
		// (cmdStoreLanded), never over it.
		cmdStore.saveDue = true
		cmdStoreLoadStart()
		return
	}
	path := cmdStorePathFn()
	if path == "" {
		return
	}
	data, err := formatCmdDeckStore(cmdStore.data)
	if err != nil {
		logWarn("terminal", "could not encode favorites: %v", err)
		return
	}
	gen := cmdStoreSaveGen.Add(1)
	go func() {
		cmdStoreSaveMu.Lock()
		defer cmdStoreSaveMu.Unlock()
		if cmdStoreSaveGen.Load() != gen {
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			logWarn("terminal", "could not save favorites: %v", err)
			return
		}
		if err := AtomicWrite(path, data, 0o644); err != nil {
			logWarn("terminal", "could not save favorites: %v", err)
		}
	}()
}

// cmdFavAdd saves lines as a favorite (newest first). It reports false when
// the same command is already saved or nothing valid was given.
func cmdFavAdd(name string, lines []string) bool {
	cmdStoreLoadStart()
	var clean []string
	for _, l := range lines {
		if c, ok := cmdCleanLine(l, cmdFavMaxLineBytes); ok && len(clean) < cmdFavMaxLines {
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 {
		return false
	}
	key := strings.Join(clean, "\n")
	for _, f := range cmdStore.data.Favorites {
		if strings.Join(f.Lines, "\n") == key {
			return false
		}
	}
	if strings.TrimSpace(name) == "" {
		name = cmdFavDefaultName(clean)
	}
	favs := append([]cmdFav{{Name: name, Lines: clean}}, cmdStore.data.Favorites...)
	if len(favs) > cmdFavMax {
		favs = favs[:cmdFavMax]
	}
	cmdStore.data.Favorites = favs
	cmdStoreSave()
	return true
}

// cmdFavRemove forgets favorite i.
func cmdFavRemove(i int) bool {
	cmdStoreLoadStart()
	if i < 0 || i >= len(cmdStore.data.Favorites) {
		return false
	}
	cmdStore.data.Favorites = append(cmdStore.data.Favorites[:i], cmdStore.data.Favorites[i+1:]...)
	cmdStoreSave()
	return true
}

// cmdFavLine is what running favorite f executes: its line, or a macro's
// lines joined so the first failure stops the rest.
func cmdFavLine(f cmdFav) string {
	return strings.Join(f.Lines, " && ")
}

// cmdRecentValues returns the remembered values for a slot key, newest first.
func cmdRecentValues(key string) []string {
	cmdStoreLoadStart()
	if key == "" {
		return nil
	}
	return cmdStore.data.Recent[key]
}

// cmdRecentNote remembers value for key, newest first, without saving; the
// caller saves once after noting all of a command's slots.
func cmdRecentNote(key, value string) bool {
	cmdStoreLoadStart()
	v, ok := cmdCleanLine(value, cmdRecentMaxBytes)
	if key == "" || !ok {
		return false
	}
	old := cmdStore.data.Recent[key]
	if len(old) > 0 && old[0] == v {
		return false
	}
	next := make([]string, 0, cmdRecentMaxValues)
	next = append(next, v)
	for _, o := range old {
		if o != v && len(next) < cmdRecentMaxValues {
			next = append(next, o)
		}
	}
	if cmdStore.data.Recent == nil {
		cmdStore.data.Recent = map[string][]string{}
	}
	if _, exists := cmdStore.data.Recent[key]; !exists && len(cmdStore.data.Recent) >= cmdRecentMaxKeys {
		return false
	}
	cmdStore.data.Recent[key] = next
	return true
}
