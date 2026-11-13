package main

// gmeta_dat.go — genre, developer, year and players from the libretro database.
//
// RetroArch's own database is built from clrmamepro DAT files, and the
// "metadat" part of libretro-database keeps one file per detail and per
// console: metadat/genre/Nintendo - Game Boy Advance.dat, metadat/developer/…,
// metadat/releaseyear/…, metadat/maxusers/…. Each lists games as
//
//	game ( comment "Advance Wars (USA)" genre "Strategy" rom ( crc 5AD0E571 ) )
//
// (older files say name instead of comment, and the player count is "users").
// They are fetched once per console, when the first game of that console is
// opened online, kept under data/game-meta/libretro/ and read from there ever
// after — offline included. A game is matched by its ROM's CRC32 first (the
// databases list cartridges by it), then by its exact name, then by its title
// with the dump tags folded away, preferring the USA, World and Europe
// releases. libretro has no descriptions: those are the user's to write.

import (
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// gmetaDatBaseURL is libretro-database's raw file root. A variable so tests can
// point it at an httptest server.
var gmetaDatBaseURL = "https://raw.githubusercontent.com/libretro/libretro-database/master"

// gmetaDatFields are the metadat folders read, in the order they are fetched.
var gmetaDatFields = []string{"genre", "developer", "releaseyear", "maxusers"}

const (
	// gmetaDatMaxBytes bounds one DAT: the biggest metadat files are a few
	// megabytes.
	gmetaDatMaxBytes = 24 << 20
	// gmetaDatRefresh is how old a cached DAT may get before a lookup online
	// fetches it again.
	gmetaDatRefresh = 120 * 24 * time.Hour
	// gmetaCRCMaxBytes is the largest ROM whose CRC32 is computed for the
	// lookup; disc images are matched by name.
	gmetaCRCMaxBytes = 64 << 20
)

// gmetaDatDir is where the DATs are kept. A variable so tests can point it at a
// temporary folder.
var gmetaDatDir = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "game-meta", "libretro")
}

// gmetaInfo is what the database says about one game.
type gmetaInfo struct {
	Name      string
	Genre     string
	Developer string
	Year      string
	Players   string
}

// gmetaDatEntry is one game block of a DAT.
type gmetaDatEntry struct {
	Name   string
	CRC    string
	Serial string
	Fields map[string]string
}

// gmetaToken is one word of a DAT: a parenthesis, a bare word or a quoted
// string.
type gmetaToken struct {
	text   string
	quoted bool
}

func (t gmetaToken) is(s string) bool { return !t.quoted && t.text == s }

// gmetaTokens splits a DAT into tokens.
func gmetaTokens(data []byte) []gmetaToken {
	toks := make([]gmetaToken, 0, len(data)/6)
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '(' || c == ')':
			toks = append(toks, gmetaToken{text: string(c)})
			i++
		case c == '"':
			var b strings.Builder
			j := i + 1
			for j < len(data) && data[j] != '"' {
				if data[j] == '\\' && j+1 < len(data) && data[j+1] == '"' {
					j++
				}
				b.WriteByte(data[j])
				j++
			}
			toks = append(toks, gmetaToken{text: b.String(), quoted: true})
			i = j + 1
		default:
			j := i
			for j < len(data) {
				d := data[j]
				if d == ' ' || d == '\t' || d == '\r' || d == '\n' || d == '(' || d == ')' || d == '"' {
					break
				}
				j++
			}
			toks = append(toks, gmetaToken{text: string(data[i:j])})
			i = j
		}
	}
	return toks
}

// gmetaParseDAT reads the game blocks of a clrmamepro DAT.
func gmetaParseDAT(r io.Reader) ([]gmetaDatEntry, error) {
	data, err := io.ReadAll(io.LimitReader(r, gmetaDatMaxBytes))
	if err != nil {
		return nil, err
	}
	toks := gmetaTokens(data)
	var out []gmetaDatEntry
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.quoted || !(strings.EqualFold(t.text, "game") || strings.EqualFold(t.text, "machine")) {
			continue
		}
		if i+1 >= len(toks) || !toks[i+1].is("(") {
			continue
		}
		entry, next := gmetaParseBlock(toks, i+2)
		i = next
		for _, key := range []string{"comment", "name", "description"} {
			if v := strings.TrimSpace(entry.Fields[key]); v != "" {
				entry.Name = v
				break
			}
		}
		if entry.Name != "" || entry.CRC != "" {
			out = append(out, entry)
		}
	}
	return out, nil
}

// gmetaParseBlock reads one game block from toks[i:] up to its closing
// parenthesis, and returns the entry and that parenthesis' index. A sub-block
// (rom, disk) gives the entry its CRC and serial.
func gmetaParseBlock(toks []gmetaToken, i int) (gmetaDatEntry, int) {
	e := gmetaDatEntry{Fields: map[string]string{}}
	for i < len(toks) {
		t := toks[i]
		if t.is(")") {
			return e, i
		}
		if t.quoted || t.is("(") {
			i++
			continue
		}
		key := strings.ToLower(t.text)
		if i+1 >= len(toks) {
			return e, len(toks)
		}
		v := toks[i+1]
		switch {
		case v.is("("):
			j := i + 2
			for j < len(toks) && !toks[j].is(")") {
				if !toks[j].quoted && !toks[j].is("(") && j+1 < len(toks) && !toks[j+1].is(")") && !toks[j+1].is("(") {
					sub, val := strings.ToLower(toks[j].text), strings.TrimSpace(toks[j+1].text)
					if key == "rom" || key == "disk" {
						switch sub {
						case "crc":
							if e.CRC == "" {
								e.CRC = strings.ToUpper(val)
							}
						case "serial":
							if e.Serial == "" {
								e.Serial = val
							}
						}
					}
					j += 2
					continue
				}
				j++
			}
			i = j + 1
		case v.is(")"):
			return e, i + 1
		default:
			if _, seen := e.Fields[key]; !seen {
				e.Fields[key] = v.text
			}
			i += 2
		}
	}
	return e, i
}

// gmetaDatIndex is one console's merged database.
type gmetaDatIndex struct {
	byCRC  map[string]*gmetaInfo
	byName map[string]*gmetaInfo
	byNorm map[string]*gmetaInfo
	rank   map[string]int
}

// gmetaRegionRank prefers the releases a library most often holds when two
// regions share a title.
func gmetaRegionRank(name string) int {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "(usa"):
		return 0
	case strings.Contains(lower, "(world"):
		return 1
	case strings.Contains(lower, "(europe"):
		return 2
	case strings.Contains(lower, "(japan"):
		return 4
	}
	return 3
}

// gmetaFieldValue reads a detail from a DAT entry, under any of the names the
// metadat files have used for it.
func gmetaFieldValue(field string, e gmetaDatEntry) string {
	keys := []string{field}
	switch field {
	case "releaseyear":
		keys = []string{"releaseyear", "year"}
	case "maxusers":
		keys = []string{"users", "maxusers"}
	}
	for _, k := range keys {
		if v := strings.TrimSpace(e.Fields[k]); v != "" {
			return v
		}
	}
	return ""
}

// gmetaBuildIndex merges the per-detail DATs of one console into one index.
// Entries meet on their CRC when they have one, else on their exact name.
func gmetaBuildIndex(files map[string][]gmetaDatEntry) *gmetaDatIndex {
	idx := &gmetaDatIndex{
		byCRC:  map[string]*gmetaInfo{},
		byName: map[string]*gmetaInfo{},
		byNorm: map[string]*gmetaInfo{},
		rank:   map[string]int{},
	}
	for _, field := range gmetaDatFields {
		for _, e := range files[field] {
			value := gmetaFieldValue(field, e)
			if value == "" {
				continue
			}
			var info *gmetaInfo
			if e.CRC != "" {
				info = idx.byCRC[e.CRC]
			}
			lower := strings.ToLower(e.Name)
			if info == nil && lower != "" {
				info = idx.byName[lower]
			}
			if info == nil {
				info = &gmetaInfo{Name: e.Name}
			}
			if e.CRC != "" {
				idx.byCRC[e.CRC] = info
			}
			if lower != "" {
				if _, ok := idx.byName[lower]; !ok {
					idx.byName[lower] = info
				}
				norm := artworkNormalizeTitle(e.Name)
				r := gmetaRegionRank(e.Name)
				if best, ok := idx.rank[norm]; norm != "" && (!ok || r < best) {
					idx.byNorm[norm] = info
					idx.rank[norm] = r
				}
			}
			switch field {
			case "genre":
				info.Genre = value
			case "developer":
				info.Developer = value
			case "releaseyear":
				info.Year = value
			case "maxusers":
				info.Players = value
			}
		}
	}
	return idx
}

// gmetaLookup matches a game in an index: by any of its CRCs, then by the file
// name's stem exactly, then by its folded title.
func gmetaLookup(idx *gmetaDatIndex, crcs []string, stem string) (gmetaInfo, bool) {
	if idx == nil {
		return gmetaInfo{}, false
	}
	for _, c := range crcs {
		if info, ok := idx.byCRC[strings.ToUpper(strings.TrimSpace(c))]; ok {
			return *info, true
		}
	}
	if info, ok := idx.byName[strings.ToLower(strings.TrimSpace(stem))]; ok {
		return *info, true
	}
	if norm := artworkNormalizeTitle(stem); norm != "" {
		if info, ok := idx.byNorm[norm]; ok {
			return *info, true
		}
	}
	return gmetaInfo{}, false
}

// gmetaPlayersLabel turns a player count into words.
func gmetaPlayersLabel(players string) string {
	players = strings.TrimSpace(players)
	switch players {
	case "":
		return ""
	case "1":
		return "1 player"
	}
	return players + " players"
}

// ── Fetching ────────────────────────────────────────────────────────────────

// gmetaDatPath is where one detail file of one console is kept.
func gmetaDatPath(dir, field, collection string) string {
	return filepath.Join(dir, field, collection+".dat")
}

// gmetaFetchDAT downloads one detail file of one console. A console the
// database has no such file for is remembered with a ".missing" marker, so it
// is not asked again.
func gmetaFetchDAT(ctx context.Context, client *http.Client, base, field, collection, dest string) error {
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	u := strings.TrimRight(base, "/") + "/metadat/" + field + "/" + url.PathEscape(collection) + ".dat"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return gmetaWriteCache(dest+".missing", []byte(time.Now().UTC().Format(time.RFC3339)+"\n"))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("libretro-database answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, gmetaDatMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > gmetaDatMaxBytes {
		return fmt.Errorf("the %s list is larger than expected", field)
	}
	if err := gmetaWriteCache(dest, data); err != nil {
		return err
	}
	// The file exists now: an old "not there" marker must not hide it.
	_ = os.Remove(dest + ".missing")
	return nil
}

// gmetaWriteCache writes a cached file through a temporary file and a rename.
// Unlike AtomicWrite it keeps no .bak: a cache is fetched again, not recovered,
// and a second copy of every list would only fill the card.
func gmetaWriteCache(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// gmetaMarkedMissing reports whether the database was found to have no such
// file. Offline any marker counts; online, one older than gmetaDatRefresh is
// asked about again — the database grows, and a 404 may have been a hiccup.
func gmetaMarkedMissing(path string, online bool) bool {
	info, err := os.Stat(path + ".missing")
	if err != nil {
		return false
	}
	return !online || time.Since(info.ModTime()) < gmetaDatRefresh
}

// gmetaFetchResult says how a console's index was obtained.
type gmetaFetchResult int

const (
	gmetaFetchOK      gmetaFetchResult = iota // read from the cache, fetched where needed
	gmetaFetchOffline                         // nothing cached, and no connection
	gmetaFetchFailed                          // nothing cached, and the download failed
	gmetaFetchNone                            // the database has nothing for this console
)

// gmetaIndexCache keeps the last consoles' indexes in memory: a few hundred
// kilobytes each, and the library is usually browsed one console at a time.
var gmetaIndexCache struct {
	mu    sync.Mutex
	order []string
	byKey map[string]*gmetaDatIndex
}

// gmetaFetchMu serialises the fetches, so two sheets opened quickly do not
// download the same files twice.
var gmetaFetchMu sync.Mutex

const gmetaIndexCacheSize = 2

// gmetaIndexFor returns one console's index, fetching the files that are
// missing (or old) first when online is true. It never fetches offline: the
// cached files are what there is.
func gmetaIndexFor(ctx context.Context, collection string, online bool) (*gmetaDatIndex, gmetaFetchResult) {
	if strings.TrimSpace(collection) == "" {
		return nil, gmetaFetchNone
	}
	gmetaIndexCache.mu.Lock()
	if idx, ok := gmetaIndexCache.byKey[collection]; ok {
		gmetaIndexCache.mu.Unlock()
		return idx, gmetaFetchOK
	}
	gmetaIndexCache.mu.Unlock()

	gmetaFetchMu.Lock()
	defer gmetaFetchMu.Unlock()
	// Another cover download may have read this console while we waited.
	gmetaIndexCache.mu.Lock()
	if idx, ok := gmetaIndexCache.byKey[collection]; ok {
		gmetaIndexCache.mu.Unlock()
		return idx, gmetaFetchOK
	}
	gmetaIndexCache.mu.Unlock()
	dir := gmetaDatDir()
	if dir == "" {
		return nil, gmetaFetchFailed
	}
	files := map[string][]gmetaDatEntry{}
	anyFile, missingAll, fetchFailed := false, true, false
	for _, field := range gmetaDatFields {
		path := gmetaDatPath(dir, field, collection)
		info, err := os.Stat(path)
		fresh := err == nil && time.Since(info.ModTime()) < gmetaDatRefresh
		absent := gmetaMarkedMissing(path, online)
		if !fresh && !absent && online && ctx.Err() == nil {
			if ferr := gmetaFetchDAT(ctx, nil, gmetaDatBaseURL, field, collection, path); ferr != nil {
				fetchFailed = true
			}
			absent = gmetaMarkedMissing(path, online)
		}
		if absent {
			continue
		}
		missingAll = false
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		entries, perr := gmetaParseDAT(f)
		f.Close()
		if perr == nil {
			files[field] = entries
			anyFile = true
		}
	}
	if !anyFile {
		switch {
		case missingAll:
			return nil, gmetaFetchNone
		case !online:
			return nil, gmetaFetchOffline
		case fetchFailed:
			return nil, gmetaFetchFailed
		}
		return nil, gmetaFetchNone
	}
	idx := gmetaBuildIndex(files)
	gmetaIndexCache.mu.Lock()
	if gmetaIndexCache.byKey == nil {
		gmetaIndexCache.byKey = map[string]*gmetaDatIndex{}
	}
	gmetaIndexCache.byKey[collection] = idx
	gmetaIndexCache.order = append(gmetaIndexCache.order, collection)
	for len(gmetaIndexCache.order) > gmetaIndexCacheSize {
		delete(gmetaIndexCache.byKey, gmetaIndexCache.order[0])
		gmetaIndexCache.order = gmetaIndexCache.order[1:]
	}
	gmetaIndexCache.mu.Unlock()
	return idx, gmetaFetchOK
}

// gmetaForgetIndex drops a console's index, so the next lookup reads the files
// again (after a refresh fetched them).
func gmetaForgetIndex(collection string) {
	gmetaIndexCache.mu.Lock()
	defer gmetaIndexCache.mu.Unlock()
	delete(gmetaIndexCache.byKey, collection)
	kept := gmetaIndexCache.order[:0]
	for _, c := range gmetaIndexCache.order {
		if c != collection {
			kept = append(kept, c)
		}
	}
	gmetaIndexCache.order = kept
}

// gmetaCollectionFor is the libretro collection a library console's files are
// named after ("" when the library does not know the console).
func gmetaCollectionFor(system string) string {
	for _, alias := range romSystemAliases {
		if alias.Name == system && alias.Collection != "" {
			return alias.Collection
		}
	}
	return ""
}

// ── CRC ─────────────────────────────────────────────────────────────────────

// gmetaROMCRCs is a ROM's CRC32 the way the databases may list it: the whole
// file, and the file without its iNES header or SNES copier header (the sets
// disagree on which). A zip gives its single ROM's CRC from its directory. Disc
// images and anything over gmetaCRCMaxBytes are matched by name instead.
func gmetaROMCRCs(path string) []string {
	if strings.EqualFold(filepath.Ext(path), ".zip") {
		if det := rommDetectZip(path); det.HasCRC {
			return []string{fmt.Sprintf("%08X", det.CRC)}
		}
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > gmetaCRCMaxBytes {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	full := crc32.NewIEEE()
	bare := crc32.NewIEEE()
	skip := int64(-1)
	ext := strings.ToLower(filepath.Ext(path))
	buf := make([]byte, 64<<10)
	var offset int64
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if offset == 0 {
				switch {
				case rommHasAt(chunk, 0, []byte("NES\x1a")):
					skip = 16
				case info.Size()%1024 == 512 && (ext == ".sfc" || ext == ".smc" || ext == ".swc" || ext == ".fig"):
					skip = 512
				}
			}
			full.Write(chunk)
			if skip >= 0 {
				if from := skip - offset; from < int64(len(chunk)) {
					if from < 0 {
						from = 0
					}
					bare.Write(chunk[from:])
				}
			}
			offset += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
	}
	out := []string{fmt.Sprintf("%08X", full.Sum32())}
	if skip > 0 {
		out = append(out, fmt.Sprintf("%08X", bare.Sum32()))
	}
	return out
}
