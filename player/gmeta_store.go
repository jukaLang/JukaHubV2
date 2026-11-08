package main

// gmeta_store.go — what JukaHub knows about a game beyond its file.
//
// One small JSON document (data/game-meta/games.json) holds, per game: the
// details the libretro database gave (genre, developer, year, players), the
// description and notes the user wrote, a 1–5 rating, tags (Favorite, Playing,
// Completed, Backlog and any the user makes up) and the local milestones the
// game has earned on this device.
//
// A game is keyed by its console and its normalised title — the same folding
// the cover lookup uses, so "Zelda (USA).gba" and "zelda.gba" share a record —
// and the record keeps the ROM's CRC32 once it is known, so a renamed file
// still finds its record. Nothing is keyed by path: the ROM manager can move a
// game and its tags go with it.
//
// Writes are asynchronous and ordered: an edit changes the in-memory record at
// once and the file follows on a goroutine, newest snapshot last.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The built-in tags, in the order the tag sheet lists them.
const (
	gmetaTagFavorite  = "Favorite"
	gmetaTagPlaying   = "Playing"
	gmetaTagCompleted = "Completed"
	gmetaTagBacklog   = "Backlog"
)

var gmetaBuiltinTags = []string{gmetaTagFavorite, gmetaTagPlaying, gmetaTagCompleted, gmetaTagBacklog}

// gmetaMaxTags bounds the custom tags, gmetaMaxText a description or a note.
const (
	gmetaMaxTags    = 32
	gmetaMaxText    = 2000
	gmetaMaxTagName = 24
)

// gmetaRecord is one game's metadata.
type gmetaRecord struct {
	System      string            `json:"system"`
	Title       string            `json:"title"`
	CRC         string            `json:"crc,omitempty"`
	Description string            `json:"description,omitempty"`
	Genre       string            `json:"genre,omitempty"`
	Developer   string            `json:"developer,omitempty"`
	Year        string            `json:"year,omitempty"`
	Players     string            `json:"players,omitempty"`
	Rating      int               `json:"rating,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	Scraped     string            `json:"scraped,omitempty"`
	Milestones  map[string]string `json:"milestones,omitempty"`
	// Seen is the game's playtime, in seconds, when its milestones were last
	// checked: a later check celebrates only the hours crossed since.
	Seen int64 `json:"seen,omitempty"`
}

// gmetaFile is the document on disk.
type gmetaFile struct {
	Version int                     `json:"version"`
	Games   map[string]*gmetaRecord `json:"games"`
	// Tags are the custom tags in the order they were made, so the tag sheet
	// lists them the same way every time.
	Tags []string `json:"tags,omitempty"`
}

const gmetaFileVersion = 1

// gmetaStore is the loaded document. rev moves on every change, so the
// library's tag filter knows when to rebuild.
var gmetaStore struct {
	mu     sync.Mutex
	loaded bool
	games  map[string]*gmetaRecord
	tags   []string
	rev    uint64
	seq    uint64
}

// gmetaWrites orders the asynchronous saves: a snapshot older than the last
// one written is dropped.
var gmetaWrites struct {
	mu      sync.Mutex
	written uint64
}

// gmetaSyncWrites makes saves synchronous; the tests set it.
var gmetaSyncWrites = false

// gmetaStorePath is where the document lives. A variable so tests can point it
// at a temporary folder.
var gmetaStorePath = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "game-meta", "games.json")
}

// gmetaKey is a game's key: its console and its normalised title.
func gmetaKey(system, title string) string {
	return strings.ToLower(strings.TrimSpace(system)) + "|" + artworkNormalizeTitle(title)
}

// gmetaEnsureLocked loads the document once. The caller holds gmetaStore.mu.
func gmetaEnsureLocked() {
	if gmetaStore.loaded {
		return
	}
	gmetaStore.loaded = true
	gmetaStore.games = map[string]*gmetaRecord{}
	gmetaStore.tags = nil
	path := gmetaStorePath()
	if strings.TrimSpace(path) == "" {
		return
	}
	var file gmetaFile
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[gmeta] ignoring %s: %v", path, err)
		}
		return
	}
	for key, rec := range file.Games {
		if rec == nil || strings.TrimSpace(key) == "" {
			continue
		}
		gmetaNormalize(rec)
		gmetaStore.games[key] = rec
	}
	for _, tag := range file.Tags {
		if name := gmetaCleanTag(tag); name != "" && !gmetaIsBuiltin(name) && !gmetaContains(gmetaStore.tags, name) {
			gmetaStore.tags = append(gmetaStore.tags, name)
		}
	}
}

// gmetaPreload loads the document off the render thread, so the first details
// sheet opens without a read.
func gmetaPreload() {
	go func() {
		gmetaStore.mu.Lock()
		gmetaEnsureLocked()
		gmetaStore.mu.Unlock()
	}()
}

// gmetaResetForTest forgets the loaded document.
func gmetaResetForTest() {
	gmetaStore.mu.Lock()
	gmetaStore.loaded = false
	gmetaStore.games = nil
	gmetaStore.tags = nil
	gmetaStore.rev++
	gmetaStore.mu.Unlock()
}

// gmetaCleanTag tidies a tag name: one line, single spaces, a bounded length,
// and the built-in spelling for a built-in tag.
func gmetaCleanTag(tag string) string {
	tag = strings.Join(strings.Fields(tag), " ")
	if runes := []rune(tag); len(runes) > gmetaMaxTagName {
		tag = strings.TrimSpace(string(runes[:gmetaMaxTagName]))
	}
	for _, b := range gmetaBuiltinTags {
		if strings.EqualFold(b, tag) {
			return b
		}
	}
	return tag
}

func gmetaIsBuiltin(tag string) bool {
	for _, b := range gmetaBuiltinTags {
		if b == tag {
			return true
		}
	}
	return false
}

// gmetaContains reports whether list holds s, ignoring case.
func gmetaContains(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// gmetaCutText bounds a description or a note.
func gmetaCutText(s string) string {
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > gmetaMaxText {
		s = strings.TrimSpace(string(runes[:gmetaMaxText]))
	}
	return s
}

// gmetaNormalize keeps a record tidy: tags cleaned and unique, the rating in
// range, the texts bounded.
func gmetaNormalize(rec *gmetaRecord) {
	tags := rec.Tags[:0]
	for _, t := range rec.Tags {
		if name := gmetaCleanTag(t); name != "" && !gmetaContains(tags, name) {
			tags = append(tags, name)
		}
	}
	rec.Tags = tags
	if rec.Rating < 0 {
		rec.Rating = 0
	}
	if rec.Rating > 5 {
		rec.Rating = 5
	}
	rec.Description = gmetaCutText(rec.Description)
	rec.Notes = gmetaCutText(rec.Notes)
	rec.CRC = strings.ToUpper(strings.TrimSpace(rec.CRC))
}

// gmetaCopy is a deep copy, so a caller can keep a record without racing the
// store.
func gmetaCopy(rec *gmetaRecord) gmetaRecord {
	out := *rec
	out.Tags = append([]string(nil), rec.Tags...)
	if rec.Milestones != nil {
		out.Milestones = make(map[string]string, len(rec.Milestones))
		for k, v := range rec.Milestones {
			out.Milestones[k] = v
		}
	}
	return out
}

// gmetaFindLocked finds a game's record: by key, then by its CRC on the same
// console (the file was renamed). The caller holds gmetaStore.mu.
func gmetaFindLocked(system, title, crc string) (string, *gmetaRecord) {
	key := gmetaKey(system, title)
	if rec, ok := gmetaStore.games[key]; ok {
		return key, rec
	}
	crc = strings.ToUpper(strings.TrimSpace(crc))
	if crc == "" {
		return key, nil
	}
	for k, rec := range gmetaStore.games {
		if rec.CRC == crc && strings.EqualFold(rec.System, system) {
			return k, rec
		}
	}
	return key, nil
}

// gmetaGet returns a copy of a game's record.
func gmetaGet(system, title string) (gmetaRecord, bool) {
	gmetaStore.mu.Lock()
	defer gmetaStore.mu.Unlock()
	gmetaEnsureLocked()
	_, rec := gmetaFindLocked(system, title, "")
	if rec == nil {
		return gmetaRecord{System: system, Title: title}, false
	}
	return gmetaCopy(rec), true
}

// gmetaUpdate changes a game's record (creating it) and saves the document. It
// returns the record as stored.
func gmetaUpdate(system, title string, change func(rec *gmetaRecord)) gmetaRecord {
	gmetaStore.mu.Lock()
	gmetaEnsureLocked()
	key, rec := gmetaFindLocked(system, title, "")
	if rec == nil {
		rec = &gmetaRecord{System: system, Title: title}
		gmetaStore.games[key] = rec
	}
	if change != nil {
		change(rec)
	}
	gmetaNormalize(rec)
	out := gmetaCopy(rec)
	snapshot, seq := gmetaSnapshotLocked()
	gmetaStore.mu.Unlock()
	gmetaPersist(snapshot, seq)
	return out
}

// gmetaAttachCRC stores a game's CRC32 on its record, once it has been read. A
// title the store has no CRC for yet whose CRC matches another record on the
// same console is that game renamed: it takes over what is missing from that
// record (the tags, rating, texts, details and milestones), so what the user
// wrote follows the game rather than the file name. The old record stays,
// for a second copy that may still carry the old name.
func gmetaAttachCRC(system, title, crc string) gmetaRecord {
	crc = strings.ToUpper(strings.TrimSpace(crc))
	gmetaStore.mu.Lock()
	gmetaEnsureLocked()
	key, rec := gmetaFindLocked(system, title, "")
	if rec == nil {
		rec = &gmetaRecord{System: system, Title: title}
		gmetaStore.games[key] = rec
	}
	if rec.CRC == "" && crc != "" {
		var matches []string
		for k, old := range gmetaStore.games {
			if k != key && old.CRC == crc && strings.EqualFold(old.System, system) {
				matches = append(matches, k)
			}
		}
		sort.Strings(matches)
		if len(matches) > 0 {
			gmetaMerge(rec, gmetaStore.games[matches[0]])
		}
		rec.CRC = crc
	}
	gmetaNormalize(rec)
	out := gmetaCopy(rec)
	snapshot, seq := gmetaSnapshotLocked()
	gmetaStore.mu.Unlock()
	gmetaPersist(snapshot, seq)
	return out
}

// gmetaMerge fills what dst lacks from src. The playtime baseline (Seen) is not
// taken: the playtime ledger counts by title, so the new name starts afresh.
func gmetaMerge(dst, src *gmetaRecord) {
	fill := func(to *string, from string) {
		if strings.TrimSpace(*to) == "" {
			*to = from
		}
	}
	fill(&dst.Description, src.Description)
	fill(&dst.Notes, src.Notes)
	fill(&dst.Genre, src.Genre)
	fill(&dst.Developer, src.Developer)
	fill(&dst.Year, src.Year)
	fill(&dst.Players, src.Players)
	fill(&dst.Scraped, src.Scraped)
	if dst.Rating == 0 {
		dst.Rating = src.Rating
	}
	for _, t := range src.Tags {
		if !gmetaContains(dst.Tags, t) {
			dst.Tags = append(dst.Tags, t)
		}
	}
	for id, at := range src.Milestones {
		if dst.Milestones == nil {
			dst.Milestones = map[string]string{}
		}
		if dst.Milestones[id] == "" {
			dst.Milestones[id] = at
		}
	}
}

// gmetaSnapshotLocked copies the document for a save. The caller holds
// gmetaStore.mu.
func gmetaSnapshotLocked() (gmetaFile, uint64) {
	gmetaStore.rev++
	gmetaStore.seq++
	file := gmetaFile{
		Version: gmetaFileVersion,
		Games:   make(map[string]*gmetaRecord, len(gmetaStore.games)),
		Tags:    append([]string(nil), gmetaStore.tags...),
	}
	for k, rec := range gmetaStore.games {
		c := gmetaCopy(rec)
		file.Games[k] = &c
	}
	return file, gmetaStore.seq
}

// gmetaPersist writes a snapshot, unless a newer one was written first.
func gmetaPersist(file gmetaFile, seq uint64) {
	path := gmetaStorePath()
	if strings.TrimSpace(path) == "" {
		return
	}
	write := func() {
		gmetaWrites.mu.Lock()
		defer gmetaWrites.mu.Unlock()
		if seq < gmetaWrites.written {
			return
		}
		data, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			log.Printf("[gmeta] cannot encode the game data: %v", err)
			return
		}
		if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
			log.Printf("[gmeta] cannot save the game data: %v", err)
			return
		}
		gmetaWrites.written = seq
	}
	if gmetaSyncWrites {
		write()
		return
	}
	go write()
}

// gmetaRev is the store's change counter.
func gmetaRev() uint64 {
	gmetaStore.mu.Lock()
	defer gmetaStore.mu.Unlock()
	return gmetaStore.rev
}

// ── Tags ────────────────────────────────────────────────────────────────────

// gmetaHasTag reports whether a record carries tag.
func gmetaHasTag(rec gmetaRecord, tag string) bool {
	return gmetaContains(rec.Tags, tag)
}

// gmetaAllTags is every tag the sheet offers: the built-in four, then the
// custom ones in the order they were made.
func gmetaAllTags() []string {
	gmetaStore.mu.Lock()
	defer gmetaStore.mu.Unlock()
	gmetaEnsureLocked()
	out := append([]string(nil), gmetaBuiltinTags...)
	return append(out, gmetaStore.tags...)
}

// gmetaAddTag registers a custom tag and returns its clean name ("" when the
// name is empty or the list is full).
func gmetaAddTag(name string) string {
	name = gmetaCleanTag(name)
	if name == "" {
		return ""
	}
	gmetaStore.mu.Lock()
	gmetaEnsureLocked()
	if gmetaIsBuiltin(name) {
		gmetaStore.mu.Unlock()
		return name
	}
	for _, t := range gmetaStore.tags {
		if strings.EqualFold(t, name) {
			gmetaStore.mu.Unlock()
			return t
		}
	}
	if len(gmetaStore.tags) >= gmetaMaxTags {
		gmetaStore.mu.Unlock()
		return ""
	}
	gmetaStore.tags = append(gmetaStore.tags, name)
	snapshot, seq := gmetaSnapshotLocked()
	gmetaStore.mu.Unlock()
	gmetaPersist(snapshot, seq)
	return name
}

// gmetaSetTag adds or removes one tag on a game and returns the stored record.
func gmetaSetTag(system, title, tag string, on bool) gmetaRecord {
	tag = gmetaCleanTag(tag)
	return gmetaUpdate(system, title, func(rec *gmetaRecord) {
		kept := rec.Tags[:0]
		for _, t := range rec.Tags {
			if !strings.EqualFold(t, tag) {
				kept = append(kept, t)
			}
		}
		rec.Tags = kept
		if on && tag != "" {
			rec.Tags = append(rec.Tags, tag)
		}
	})
}

// gmetaTaggedPaths is the set of library paths whose game carries tag.
func gmetaTaggedPaths(systems []librarySystem, tag string) map[string]bool {
	out := map[string]bool{}
	if tag == "" {
		return out
	}
	gmetaStore.mu.Lock()
	defer gmetaStore.mu.Unlock()
	gmetaEnsureLocked()
	for _, system := range systems {
		for _, game := range system.Games {
			if rec, ok := gmetaStore.games[gmetaKey(game.System, game.Title)]; ok && gmetaContains(rec.Tags, tag) {
				out[game.Path] = true
			}
		}
	}
	return out
}

// gmetaTagCounts counts the library's games per tag.
func gmetaTagCounts(systems []librarySystem) map[string]int {
	out := map[string]int{}
	gmetaStore.mu.Lock()
	defer gmetaStore.mu.Unlock()
	gmetaEnsureLocked()
	for _, system := range systems {
		for _, game := range system.Games {
			rec, ok := gmetaStore.games[gmetaKey(game.System, game.Title)]
			if !ok {
				continue
			}
			for _, t := range rec.Tags {
				out[t]++
			}
		}
	}
	return out
}

// gmetaFilterLibrary keeps only the games in paths, dropping the consoles left
// empty and the Continue row (a view over games that are already listed).
func gmetaFilterLibrary(systems []librarySystem, paths map[string]bool) []librarySystem {
	out := make([]librarySystem, 0, len(systems))
	for _, system := range systems {
		if system.Name == continueSystemName {
			continue
		}
		// Grown as matches turn up: a tag usually holds a handful of a
		// console's games, and this runs every frame while a filter is on.
		var games []libraryGame
		for _, game := range system.Games {
			if paths[game.Path] {
				games = append(games, game)
			}
		}
		if len(games) == 0 {
			continue
		}
		out = append(out, librarySystem{Name: system.Name, Games: games, WithArt: countCoveredGames(games)})
	}
	return out
}

// ── Milestones ──────────────────────────────────────────────────────────────

// gmetaMilestone is one local, per-device milestone a game can earn.
type gmetaMilestone struct {
	ID      string
	Title   string
	Short   string
	Seconds int64 // the playtime it needs; 0 for the ones earned otherwise
	Tier    trophyTier
}

const (
	gmetaMilestoneFirst     = "first"
	gmetaMilestoneCompleted = "completed"
)

// gmetaMilestones are listed in the order the details sheet shows them.
var gmetaMilestones = []gmetaMilestone{
	{ID: gmetaMilestoneFirst, Title: "First launch", Short: "First launch", Tier: trophyBronze},
	{ID: "1h", Title: "One hour in", Short: "1 hour", Seconds: 3600, Tier: trophyBronze},
	{ID: "5h", Title: "Five hours in", Short: "5 hours", Seconds: 5 * 3600, Tier: trophySilver},
	{ID: "10h", Title: "Ten hours in", Short: "10 hours", Seconds: 10 * 3600, Tier: trophyGold},
	{ID: gmetaMilestoneCompleted, Title: "Completed", Short: "Completed", Tier: trophyGold},
}

// gmetaMilestoneByID finds a milestone.
func gmetaMilestoneByID(id string) (gmetaMilestone, bool) {
	for _, m := range gmetaMilestones {
		if m.ID == id {
			return m, true
		}
	}
	return gmetaMilestone{}, false
}

// gmetaTimeMilestones splits the playtime milestones a game has not recorded
// yet: crossed are the ones passed between prev and now (worth celebrating),
// past the ones already behind prev (recorded quietly — that is history from
// before the game was being watched, not news).
func gmetaTimeMilestones(prev, now int64, have map[string]string) (crossed, past []gmetaMilestone) {
	for _, m := range gmetaMilestones {
		if m.Seconds <= 0 || have[m.ID] != "" {
			continue
		}
		switch {
		case m.Seconds <= prev:
			past = append(past, m)
		case m.Seconds <= now:
			crossed = append(crossed, m)
		}
	}
	return crossed, past
}

// gmetaCelebrations is a test seam: the suite launches games in other areas'
// tests, and their first launches must not leave trophy cards and
// notifications behind for the tests that follow.
var gmetaCelebrations = true

// gmetaCelebrate queues a milestone's card through the trophy celebration and
// files it in the notification centre.
func gmetaCelebrate(m gmetaMilestone, game string) {
	if !gmetaCelebrations {
		return
	}
	trophyEnqueue(trophyToast{
		title:   m.Title,
		caption: "GAME MILESTONE",
		detail:  game,
		tier:    m.Tier,
	})
	notifRecord("Milestone: "+m.Title+" · "+game, notifSuccess)
}

// gmetaStamp is the time a milestone is recorded with.
func gmetaStamp() string { return time.Now().Format(time.RFC3339) }

// gmetaNoteLaunch runs when the library starts a game: the first launch is a
// milestone, and the game's playtime right now is the baseline the hour
// milestones are measured from. A game that was played before (it is in the
// recents, or has time on the ledger) records its first launch quietly.
func gmetaNoteLaunch(game libraryGame) {
	if strings.TrimSpace(game.Title) == "" || strings.TrimSpace(game.System) == "" {
		return
	}
	played := false
	for _, r := range recentGames() {
		if r.Path == game.Path {
			played = true
			break
		}
	}
	secs := playtime.data.Games[game.Title]
	if secs > 0 {
		played = true
	}
	celebrate := false
	gmetaUpdate(game.System, game.Title, func(rec *gmetaRecord) {
		if rec.Milestones == nil {
			rec.Milestones = map[string]string{}
		}
		stamp := gmetaStamp()
		if rec.Milestones[gmetaMilestoneFirst] == "" {
			rec.Milestones[gmetaMilestoneFirst] = stamp
			celebrate = !played
		}
		_, past := gmetaTimeMilestones(secs, secs, rec.Milestones)
		for _, m := range past {
			rec.Milestones[m.ID] = stamp
		}
		rec.Seen = secs
	})
	if celebrate {
		if m, ok := gmetaMilestoneByID(gmetaMilestoneFirst); ok {
			gmetaCelebrate(m, game.Title)
		}
	}
}

// gmetaCheckPlaytime records the hour milestones a game's playtime has reached
// and celebrates the ones crossed since the last check. A game the store has
// never seen launched is recorded quietly.
func gmetaCheckPlaytime(game libraryGame, secs int64) {
	if rec, ok := gmetaGet(game.System, game.Title); ok && rec.Seen >= secs {
		// Nothing was played since the last check (every milestone up to Seen
		// is already recorded), so the file is not written again.
		return
	}
	var due []gmetaMilestone
	gmetaUpdate(game.System, game.Title, func(rec *gmetaRecord) {
		if rec.Milestones == nil {
			rec.Milestones = map[string]string{}
		}
		watched := rec.Milestones[gmetaMilestoneFirst] != ""
		prev := rec.Seen
		if !watched {
			prev = secs
		}
		crossed, past := gmetaTimeMilestones(prev, secs, rec.Milestones)
		stamp := gmetaStamp()
		for _, m := range past {
			rec.Milestones[m.ID] = stamp
		}
		for _, m := range crossed {
			rec.Milestones[m.ID] = stamp
		}
		due = crossed
		if secs > rec.Seen {
			rec.Seen = secs
		}
	})
	for _, m := range due {
		gmetaCelebrate(m, game.Title)
	}
}

// gmetaSortedTags returns tags in the sheet's order: built-in first, then the
// custom ones alphabetically.
func gmetaSortedTags(tags []string) []string {
	out := append([]string(nil), tags...)
	rank := func(t string) int {
		for i, b := range gmetaBuiltinTags {
			if b == t {
				return i
			}
		}
		return len(gmetaBuiltinTags)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i]), rank(out[j])
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}
