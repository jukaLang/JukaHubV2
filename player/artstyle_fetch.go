package main

// artstyle_fetch.go — downloading screenshots and title screens, and the
// queue the Artwork Manager and Fill gaps put art fetches in.
//
// The box-art fetch (romsetup.go startArtworkFetch) is the one downloader:
// one run at a time, its progress, its stop (L1 / F in the library) and its
// "done" generation that makes the library reload its pictures. A screenshot
// or title-screen run uses all of that — artworkFetchState is the same — and
// differs only in what one game's step does: the thumbnail server's
// Named_Snaps or Named_Titles list instead of Named_Boxarts, matched with the
// box art's rules (exact name, the name without a release number, the game
// database's name, a sister console's list, then the closest title), written
// to data/game-art/.snap|.title/<System>/<stem>.png (artstyle.go). Its
// "not found" answers go in the same not-found file, under keys of their own
// ("Named_Snaps:<collection>/<name>"), so a miss is not asked again for two
// weeks either.
//
// The queue holds fetches waiting for the downloader: Fill gaps with Art type
// set to Screenshot queues the screenshots behind the covers, and the Artwork
// Manager queues one console or all of them. A goroutine starts each job when
// nothing else is fetching; a run the user stops (L1 / F, or the manager)
// empties the queue.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// artStyleFetchFolder is the thumbnail server's folder for a picture type.
func artStyleFetchFolder(kind string) string {
	switch kind {
	case artTypeSnap:
		return artworkSnaps
	case artTypeTitle:
		return artworkTitles
	}
	return artworkBoxarts
}

// artStyleMissKey is the not-found key of one lookup in a picture folder.
func artStyleMissKey(folder, collection, name string) string {
	return folder + ":" + artworkMissKey(collection, name)
}

// artStyleFetchable reports a ROM whose pictures come from the thumbnail
// server: a console the scanner maps to a collection.
func artStyleFetchable(rom scannedROM) bool {
	c := strings.TrimSpace(rom.Collection)
	return c != "" && c != "Plugins"
}

// artStyleMissingKind is the games of roms without a picture of kind, leaving
// out those whose cover the user removed (artwork_nocover.go). Off the render
// thread is fine: folders are listed once per console.
func artStyleMissingKind(roms []scannedROM, root, kind string) []scannedROM {
	if strings.TrimSpace(root) == "" || artStyleKindDir(kind) == "" {
		return nil
	}
	names := map[string]map[string]bool{}
	out := make([]scannedROM, 0, len(roms))
	for _, rom := range roms {
		if !artStyleFetchable(rom) || artworkNoCover(rom, root) {
			continue
		}
		have, ok := names[rom.System]
		if !ok {
			have = artStyleKindNames(root, kind, rom.System)
			names[rom.System] = have
		}
		if have[strings.ToLower(romGameStem(rom.Path)+".png")] {
			continue
		}
		out = append(out, rom)
	}
	return out
}

// artStyleStartKindFetch downloads kind's pictures for roms through the
// box-art fetch's state (progress, stop, completion). False when a fetch is
// already running or there is no art folder.
func artStyleStartKindFetch(roms []scannedROM, kind string) bool {
	dest := romArtworkDir()
	folder := artStyleFetchFolder(kind)
	if strings.TrimSpace(dest) == "" || folder == artworkBoxarts {
		return false
	}
	misses := loadArtworkMisses(dest)
	artworkFetchState.Lock()
	if artworkFetchState.running {
		artworkFetchState.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	artworkFetchState.running = true
	artworkFetchState.stopping = false
	artworkFetchState.stopped = false
	artworkFetchState.total = len(roms)
	artworkFetchState.done = 0
	artworkFetchState.fetched = 0
	artworkFetchState.skipped = 0
	artworkFetchState.missing = 0
	artworkFetchState.cancel = cancel
	artworkFetchState.Unlock()
	go func() {
		fetched, skipped, missing, stopped := artStyleFetchKindTracked(ctx, roms, kind, dest, misses, func(done int) {
			artworkFetchState.Lock()
			artworkFetchState.done = done
			artworkFetchState.Unlock()
		})
		cancel()
		if err := saveArtworkMisses(dest, misses); err != nil {
			logWarn("art", "cannot save the not-found cache: %v", err)
		}
		logInfo("art", "%s fetch: %d saved, %d present, %d not found, stopped=%v", folder, fetched, skipped, missing, stopped)
		artworkFetchState.Lock()
		artworkFetchState.running = false
		artworkFetchState.stopping = false
		artworkFetchState.stopped = stopped
		artworkFetchState.fetched = fetched
		artworkFetchState.skipped = skipped
		artworkFetchState.missing = missing
		artworkFetchState.cancel = nil
		artworkFetchState.finished++
		artworkFetchState.Unlock()
	}()
	return true
}

// artStyleFetchKindTracked is fetchROMArtworkTracked for a screenshot or
// title-screen folder: the same workers, buckets and interim not-found saves.
func artStyleFetchKindTracked(ctx context.Context, roms []scannedROM, kind, dest string, misses map[string]time.Time, progress func(done int)) (fetched, skipped, missing int, stopped bool) {
	client := artworkHTTPClient()
	folder := artStyleFetchFolder(kind)
	now := time.Now()
	const (
		artFetched = iota
		artSkipped
		artMissing
	)
	var missesMu sync.Mutex
	claimed := map[string]bool{}
	classify := func(rom scannedROM) int {
		path := artStyleKindPath(rom, dest, kind)
		if path == "" || !artStyleFetchable(rom) {
			return artMissing
		}
		if artworkCoverPresent(path) || artworkNoCover(rom, dest) {
			return artSkipped
		}
		missesMu.Lock()
		taken := claimed[path]
		claimed[path] = true
		missesMu.Unlock()
		if taken {
			return artSkipped
		}
		stem := romGameStem(rom.Path)
		if stem == "" {
			return artMissing
		}
		key := artStyleMissKey(folder, rom.Collection, artworkLookupName(stem))
		missesMu.Lock()
		recent := artworkRecentlyMissing(misses, key, now, artworkMissTTL)
		missesMu.Unlock()
		if recent {
			return artMissing
		}
		data, err := artStyleFetchKindImage(ctx, client, artworkBaseURL, rom, stem, folder)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missesMu.Lock()
				misses[key] = now
				missesMu.Unlock()
			}
			return artMissing
		}
		if err := gmetaWriteCache(path, data); err != nil {
			return artMissing
		}
		missesMu.Lock()
		delete(misses, key)
		missesMu.Unlock()
		return artFetched
	}
	var (
		countMu sync.Mutex
		done    int
		next    atomic.Int64
		wg      sync.WaitGroup
		saveMu  sync.Mutex
	)
	saveMisses := func() {
		saveMu.Lock()
		defer saveMu.Unlock()
		missesMu.Lock()
		snapshot := make(map[string]time.Time, len(misses))
		for k, at := range misses {
			snapshot[k] = at
		}
		missesMu.Unlock()
		if err := saveArtworkMisses(dest, snapshot); err != nil {
			logWarn("art", "cannot save the not-found cache: %v", err)
		}
	}
	workers := min(artworkFetchWorkers, len(roms))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(roms) {
					return
				}
				kindOf := classify(roms[i])
				countMu.Lock()
				switch kindOf {
				case artFetched:
					fetched++
				case artSkipped:
					skipped++
				default:
					missing++
				}
				done++
				if progress != nil {
					progress(done)
				}
				saveNow := done%artworkMissSaveEvery == 0 && done < len(roms)
				countMu.Unlock()
				if saveNow {
					saveMisses()
				}
			}
		}()
	}
	wg.Wait()
	return fetched, skipped, missing, ctx.Err() != nil
}

// artStyleFetchKindImage finds and downloads one ROM's picture in folder
// (Named_Snaps or Named_Titles) with the box art's matching steps.
// os.ErrNotExist means the server has none for it.
func artStyleFetchKindImage(ctx context.Context, client *http.Client, baseURL string, rom scannedROM, stem, folder string) ([]byte, error) {
	base := strings.TrimRight(baseURL, "/")
	if base == "" || !artStyleFetchable(rom) || strings.TrimSpace(stem) == "" {
		return nil, os.ErrNotExist
	}
	lookup := artworkLookupName(stem)
	userNamed := lookup != stem
	bare, numbered := artworkStripReleaseNumber(lookup)
	idx, err := artworkIndexOf(ctx, client, base, rom.Collection, folder)
	if err != nil {
		return nil, err
	}
	if idx == nil {
		// No list from the server: the names guessed from the file, then
		// without its release number.
		names := []string{lookup}
		if numbered {
			names = append(names, bare)
		}
		for _, name := range names {
			for _, variant := range artworkTitleVariants(name) {
				data, err := artworkDownloadFrom(ctx, client, base, rom.Collection, folder, variant)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return data, err
			}
		}
		return nil, os.ErrNotExist
	}
	known, knownDone := "", userNamed
	databaseName := func() string {
		if !knownDone {
			knownDone = true
			look := lookup
			if numbered {
				look = bare
			}
			known = artworkDatabaseName(ctx, rom, look)
		}
		return known
	}
	exact := func(list *artworkIndex) (string, bool) {
		name, ok := list.match(lookup)
		if !ok && numbered {
			name, ok = list.match(bare)
		}
		if !ok {
			if db := databaseName(); db != "" {
				name, ok = list.match(db)
			}
		}
		return name, ok
	}
	name, ok := exact(idx)
	// otherErr is a sister list that could not be read: with no picture found
	// the game is then "no answer", not a miss filed for two weeks.
	var otherErr error
	if !ok {
		// A sister console's list (a Game Boy game filed under Game Boy Color).
		data, err := artworkFromOtherLists(ctx, client, base, lookup, artworkSiblingCollections(rom.Collection), []string{folder}, exact)
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			otherErr = err
		}
		if db := databaseName(); db != "" {
			name, ok = idx.closest(db)
		}
		if !ok && numbered {
			name, ok = idx.closest(bare)
		}
		if !ok {
			name, ok = idx.closest(lookup)
		}
	}
	if !ok {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if otherErr != nil {
			return nil, otherErr
		}
		return nil, os.ErrNotExist
	}
	data, err := artworkDownloadFrom(ctx, client, base, rom.Collection, folder, name)
	if errors.Is(err, os.ErrNotExist) && !idx.recent(time.Now()) {
		// The list named a picture the server no longer has: read a fresh
		// list next time rather than filing a miss.
		artworkForgetIndex(base, rom.Collection, folder, idx)
		return nil, fmt.Errorf("%s %q is listed but missing; the list is refreshed", folder, name)
	}
	return data, err
}

// ── The queue ────────────────────────────────────────────────────────────

// artQueueJob is one fetch waiting for the downloader: the missing pictures
// of kind for one console ("" system: several consoles at once).
type artQueueJob struct {
	system string
	kind   string
	roms   []scannedROM
}

var artQueue struct {
	mu      sync.Mutex
	jobs    []artQueueJob
	pumping bool
	// current is the job the downloader runs for the queue now.
	current    artQueueJob
	hasCurrent bool
}

// artQueuePoll is how often the queue looks whether the downloader is free.
const artQueuePoll = 500 * time.Millisecond

// artQueueAdd queues jobs (a console and type already waiting is not queued
// twice) and starts the goroutine that feeds them to the downloader.
func artQueueAdd(jobs ...artQueueJob) int {
	artQueue.mu.Lock()
	defer artQueue.mu.Unlock()
	added := 0
	for _, job := range jobs {
		if len(job.roms) == 0 {
			continue
		}
		dup := false
		for _, q := range artQueue.jobs {
			if q.system == job.system && q.kind == job.kind {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		artQueue.jobs = append(artQueue.jobs, job)
		added++
	}
	if added > 0 && !artQueue.pumping {
		artQueue.pumping = true
		go artQueuePump()
	}
	return added
}

// artQueueState says whether system's pictures of kind are queued or being
// fetched for the queue now.
func artQueueState(system, kind string) (queued, running bool) {
	artQueue.mu.Lock()
	defer artQueue.mu.Unlock()
	if artQueue.hasCurrent && artQueue.current.kind == kind &&
		(artQueue.current.system == system || artQueue.current.system == "") {
		running = artworkFetchRunning()
	}
	for _, q := range artQueue.jobs {
		if q.kind == kind && (q.system == system || q.system == "") {
			queued = true
		}
	}
	return queued, running
}

// artQueueLen is how many jobs wait.
func artQueueLen() int {
	artQueue.mu.Lock()
	defer artQueue.mu.Unlock()
	return len(artQueue.jobs)
}

// artQueueClear drops every waiting job (the running one goes on until it
// is stopped).
func artQueueClear() {
	artQueue.mu.Lock()
	artQueue.jobs = nil
	artQueue.mu.Unlock()
}

// artworkFetchWasStopped reports that the last fetch ended on a stop.
func artworkFetchWasStopped() bool {
	artworkFetchState.Lock()
	defer artworkFetchState.Unlock()
	return artworkFetchState.stopped
}

// artQueuePump starts the queued jobs one by one whenever no fetch runs (the
// library's Fill gaps or the automatic pass may hold the downloader). A stop
// ends the whole queue: the user asked for the fetching to stop.
func artQueuePump() {
	for {
		waited := false
		for artworkFetchRunning() {
			waited = true
			time.Sleep(artQueuePoll)
		}
		if waited && !artworkFetchWasStopped() {
			// Let the run's owner see it end before a queued job takes the
			// downloader: the automatic pass looks every boxArtWatchEvery and
			// would take a run started at once for its own batch (and stop it
			// when a game starts). Its next batch, if any, is waited out too.
			time.Sleep(boxArtWatchEvery + artQueuePoll)
			if artworkFetchRunning() {
				continue
			}
		}
		artQueue.mu.Lock()
		if waited && artworkFetchWasStopped() {
			// The run this queue waited behind (Fill gaps' covers) was
			// stopped: so is what was queued after it.
			artQueue.jobs = nil
		}
		if len(artQueue.jobs) == 0 {
			artQueue.pumping = false
			artQueue.hasCurrent = false
			artQueue.mu.Unlock()
			return
		}
		job := artQueue.jobs[0]
		artQueue.jobs = artQueue.jobs[1:]
		artQueue.current, artQueue.hasCurrent = job, true
		artQueue.mu.Unlock()

		var started bool
		if job.kind == artTypeBox {
			started = startLibraryArtworkFetchAs(job.roms, true)
		} else {
			started = artStyleStartKindFetch(job.roms, job.kind)
		}
		if !started && strings.TrimSpace(romArtworkDir()) == "" {
			// No art folder: no job can ever start, so the queue ends rather
			// than retrying every half second.
			artQueue.mu.Lock()
			artQueue.jobs = nil
			artQueue.pumping = false
			artQueue.hasCurrent = false
			artQueue.mu.Unlock()
			return
		}
		if !started {
			// Another fetch started first: this job waits for it.
			artQueue.mu.Lock()
			artQueue.jobs = append([]artQueueJob{job}, artQueue.jobs...)
			artQueue.hasCurrent = false
			artQueue.mu.Unlock()
			time.Sleep(artQueuePoll)
			continue
		}
		for artworkFetchRunning() {
			time.Sleep(artQueuePoll)
		}
		if artworkFetchWasStopped() {
			artQueue.mu.Lock()
			artQueue.jobs = nil
			artQueue.pumping = false
			artQueue.hasCurrent = false
			artQueue.mu.Unlock()
			return
		}
	}
}

// artStyleFillQueue is Fill gaps' second half (fetchMissingLibraryArtwork):
// with Art type set to Screenshot or Title screen, those pictures are queued
// behind the covers. boxMissing is how many covers that Fill gaps fetches.
// It reports whether it said anything (queued, or "all there"), so Fill gaps
// does not also say every game has box art. Render thread.
func artStyleFillQueue(games []scannedROM, boxMissing int) bool {
	kind := artStyleType(nil)
	if kind == artTypeBox || !artStyleArtOn(nil) {
		return false
	}
	missing := artStyleMissingKind(games, libraryArtworkRoot(), kind)
	if len(missing) == 0 {
		if boxMissing == 0 {
			ShowToast("Every scanned game already has box art and a "+artStyleTypeNoun(kind, false)+".", ToastKindSuccess)
			return true
		}
		return false
	}
	first := ""
	if system, ok := selectedLibrarySystem(); ok {
		first = system.Name
	}
	artQueueAdd(artQueueJob{kind: kind, roms: artworkFetchOrder(missing, first)})
	if boxMissing == 0 {
		ShowToast("Fetching "+pluralizeCount(len(missing), "missing "+artStyleTypeNoun(kind, false), "missing "+artStyleTypeNoun(kind, true))+
			" — L1 / F stops and resumes", ToastKindInfo)
	}
	return true
}
