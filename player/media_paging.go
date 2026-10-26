package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// media_paging.go adds result paging to the Media grid.
//
// yt-dlp's ytsearch extractor is a playlist, so asking for a larger ytsearchN
// and slicing it with --playlist-start/--playlist-end returns the real next
// page of results (the same trick fetchTrendingVideos already used for its 12).
// A page fetch appends to the grid instead of replacing it, so scrolling past
// the last card grows the list rather than starting over.

// mediaResultsPage is the loaded window behind one search-results variable.
type mediaResultsPage struct {
	command  string // the command that produced the first page
	pageSize int    // results per page
	page     int    // highest page requested (0 = the first page)
	loading  bool   // a page fetch is in flight
	done     bool   // the last fetch returned no new videos
}

var mediaPaging = struct {
	mu    sync.Mutex
	byVar map[string]*mediaResultsPage
}{byVar: map[string]*mediaResultsPage{}}

var (
	ytSearchSpec     = regexp.MustCompile(`ytsearch(\d+):([^"]*)`)
	playlistStartArg = regexp.MustCompile(`--playlist-start\s+\d+`)
	playlistEndArg   = regexp.MustCompile(`--playlist-end\s+\d+`)
)

// rememberMediaPaging records the command and page size of a fresh search, so
// the next page can be built from the same query. Searches without a ytsearch
// spec (a pasted link, for example) clear the state: there is nothing to page.
func rememberMediaPaging(resultVar, command string) {
	mediaPaging.mu.Lock()
	defer mediaPaging.mu.Unlock()
	match := ytSearchSpec.FindStringSubmatch(command)
	if match == nil {
		delete(mediaPaging.byVar, resultVar)
		return
	}
	size, err := strconv.Atoi(match[1])
	if err != nil || size <= 0 {
		delete(mediaPaging.byVar, resultVar)
		return
	}
	mediaPaging.byVar[resultVar] = &mediaResultsPage{
		command:  command,
		pageSize: size,
	}
}

// mediaPageState returns a copy of the paging state for a results variable.
func mediaPageState(resultVar string) (mediaResultsPage, bool) {
	mediaPaging.mu.Lock()
	defer mediaPaging.mu.Unlock()
	state, ok := mediaPaging.byVar[resultVar]
	if !ok || state == nil {
		return mediaResultsPage{}, false
	}
	return *state, true
}

// mediaPageLoading reports whether a page fetch is already running.
func mediaPageLoading(resultVar string) bool {
	mediaPaging.mu.Lock()
	defer mediaPaging.mu.Unlock()
	state, ok := mediaPaging.byVar[resultVar]
	return ok && state != nil && state.loading
}

// markMediaPageRequested records that page n is being fetched.
func markMediaPageRequested(resultVar string, page int) {
	mediaPaging.mu.Lock()
	defer mediaPaging.mu.Unlock()
	if state, ok := mediaPaging.byVar[resultVar]; ok && state != nil {
		state.page = page
		state.loading = true
	}
}

// markMediaPageSettled clears the in-flight flag. done marks a page that
// produced no new videos, so the next button can say so instead of retrying.
func markMediaPageSettled(resultVar string, done bool) {
	mediaPaging.mu.Lock()
	defer mediaPaging.mu.Unlock()
	if state, ok := mediaPaging.byVar[resultVar]; ok && state != nil {
		state.loading = false
		if done {
			state.done = true
		}
	}
}

// buildMediaPageCommand rewrites a first-page search command into the command
// for page n: the ytsearch count grows to cover the page and the playlist
// window moves to the requested slice.
func buildMediaPageCommand(command string, pageSize, page int) string {
	if page <= 0 || pageSize <= 0 {
		return command
	}
	start := page*pageSize + 1
	end := (page + 1) * pageSize
	out := ytSearchSpec.ReplaceAllStringFunc(command, func(match string) string {
		sub := ytSearchSpec.FindStringSubmatch(match)
		return fmt.Sprintf("ytsearch%d:%s", end, sub[2])
	})
	if playlistStartArg.MatchString(out) {
		out = playlistStartArg.ReplaceAllString(out, fmt.Sprintf("--playlist-start %d", start))
	} else {
		out += fmt.Sprintf(" --playlist-start %d", start)
	}
	if playlistEndArg.MatchString(out) {
		out = playlistEndArg.ReplaceAllString(out, fmt.Sprintf("--playlist-end %d", end))
	} else {
		out += fmt.Sprintf(" --playlist-end %d", end)
	}
	return out
}

// mediaResultsElement returns the search-results element of the scene on screen.
func mediaResultsElement(config *Config) (Element, bool) {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return Element{}, false
	}
	return sceneSearchResultsElement(config.Scenes[currentSceneIndex])
}

// mediaPageTarget is where a page jump lands: the first card of the next or
// previous page inside the loaded list, with needMore set when that page has
// not been fetched yet. Jumping back from the middle of a page lands on that
// page's first card, then on the previous page, so one press always moves.
func mediaPageTarget(focused, loaded, pageSize, delta int) (index int, needMore bool) {
	if pageSize < 1 {
		pageSize = 1
	}
	page := 0
	if focused >= 0 {
		page = focused / pageSize
	}
	if delta < 0 && focused > page*pageSize {
		return page * pageSize, false
	}
	target := page + delta
	if target < 0 {
		return 0, false
	}
	index = target * pageSize
	if index >= loaded {
		return loaded, true
	}
	return index, false
}

// mediaPageSize returns how many cards one page covers for this element: a
// screenful in both the grid and the list layout.
func mediaPageSize(element Element, elemHeight int32) int {
	cols, rows := gridColsRows(element)
	if cols > 1 {
		return int(cols) * int(rows)
	}
	visible := int((elemHeight + searchResultRowGap) / (searchResultRowH + searchResultRowGap))
	if visible < 1 {
		visible = 1
	}
	return visible
}

// mediaResultsPageJump moves the grid one page forward or back. Forward past
// the loaded list starts the next fetch; backward is instant because earlier
// pages are already in the list.
func mediaResultsPageJump(config *Config, delta int) bool {
	element, ok := mediaResultsElement(config)
	if !ok || delta == 0 {
		return false
	}
	videos, ok := config.Variables.Custom[element.Variable].([]VideoInfo)
	if !ok || len(videos) == 0 {
		return false
	}
	state, hasPages := mediaPageState(element.Variable)
	if !hasPages {
		ShowToast("This list has no more pages", ToastKindInfo)
		return false
	}
	elemHeight := getElementHeight(element, 456)
	pageSize := state.pageSize
	if pageSize < 1 {
		pageSize = mediaPageSize(element, elemHeight)
	}
	index, needMore := mediaPageTarget(focusedResultIndex, len(videos), pageSize, delta)
	if needMore {
		if delta < 0 {
			return false
		}
		return loadMoreMediaResults(config)
	}
	focusedResultIndex = index
	prefetchFocusedResult(config, videos)
	PlayNavSound()
	return true
}

// loadMoreMediaResults fetches and appends the next page of the visible list.
// It is the R2 action and the Next chip.
func loadMoreMediaResults(config *Config) bool {
	element, ok := mediaResultsElement(config)
	if !ok {
		return false
	}
	state, ok := mediaPageState(element.Variable)
	if !ok {
		ShowToast("This list has no more pages", ToastKindInfo)
		return false
	}
	// A page only makes sense appended to a list that is already on screen;
	// fetching page 2 of nothing would leave the grid with the wrong batch.
	if videos, ok := config.Variables.Custom[element.Variable].([]VideoInfo); !ok || len(videos) == 0 {
		ShowToast("Waiting for the first results…", ToastKindInfo)
		return false
	}
	if state.done {
		ShowToast("No more results", ToastKindInfo)
		return false
	}
	if mediaPageLoading(element.Variable) {
		ShowToast("Loading the next page…", ToastKindInfo)
		return true
	}
	next := state.page + 1
	command := buildMediaPageCommand(state.command, state.pageSize, next)
	markMediaPageRequested(element.Variable, next)
	executeYouTubeSearchPage(config, command, element.Variable, snapshotVars(config), true)
	return true
}

// videoMergeKeys lists the identities of a video for duplicate detection: the
// ID and the title, both lower-cased. yt-dlp occasionally reports an entry
// without an ID, and a re-fetched page can then only be matched by its title.
func videoMergeKeys(v VideoInfo) []string {
	keys := make([]string, 0, 2)
	if id := strings.ToLower(strings.TrimSpace(v.ID)); id != "" {
		keys = append(keys, "id:"+id)
	}
	if title := strings.ToLower(strings.TrimSpace(v.Title)); title != "" {
		keys = append(keys, "title:"+title)
	}
	return keys
}

// mergeMediaResults appends new videos to the loaded list, dropping duplicates
// by ID or title. It reports how many were added.
func mergeMediaResults(existing, incoming []VideoInfo) ([]VideoInfo, int) {
	merged := make([]VideoInfo, 0, len(existing)+len(incoming))
	seen := make(map[string]bool, len(existing)+len(incoming))
	keep := func(v VideoInfo) bool {
		keys := videoMergeKeys(v)
		if len(keys) == 0 {
			return false
		}
		for _, k := range keys {
			if seen[k] {
				return false
			}
		}
		for _, k := range keys {
			seen[k] = true
		}
		return true
	}
	for _, v := range existing {
		if keep(v) {
			merged = append(merged, v)
		}
	}
	before := len(merged)
	for _, v := range incoming {
		if keep(v) {
			merged = append(merged, v)
		}
	}
	return merged, len(merged) - before
}

// mediaNextPageRect is the Next chip's hit area. It sits in the bottom-right
// corner of the results viewport, clear of the last card.
func mediaNextPageRect(element Element, elemWidth, elemHeight int32) sdl.Rect {
	chipW := int32(150)
	chipH := int32(32)
	return sdl.Rect{
		X: element.X + elemWidth - chipW - 14,
		Y: element.Y + elemHeight - chipH - 10,
		W: chipW,
		H: chipH,
	}
}

// mediaNextPageHit reports whether a click landed on the Next chip, using the
// same defaults the renderer measures with.
func mediaNextPageHit(config *Config, element Element, videos []VideoInfo, mx, my int32) bool {
	if !mediaNextPageVisible(config, element, videos) {
		return false
	}
	rect := mediaNextPageRect(element, getElementWidth(element, 1232), getElementHeight(element, 456))
	return mx >= rect.X && mx <= rect.X+rect.W && my >= rect.Y && my <= rect.Y+rect.H
}

// mediaNextPageVisible reports whether the Next chip should be drawn: the
// focused card is on the last loaded row and the list can grow.
func mediaNextPageVisible(config *Config, element Element, videos []VideoInfo) bool {
	if len(videos) == 0 || focusedResultIndex < 0 {
		return false
	}
	if _, ok := mediaPageState(element.Variable); !ok {
		return false
	}
	cols, _ := gridColsRows(element)
	if cols < 1 {
		cols = 1
	}
	if cols == 1 {
		return focusedResultIndex >= len(videos)-1
	}
	lastRow := (len(videos) - 1) / int(cols)
	return focusedResultIndex/int(cols) >= lastRow
}

// renderMediaNextPageButton draws the "Next" affordance over the results
// viewport while the user is at the end of what has been loaded. Clicking it
// and pressing R2 do the same thing.
func renderMediaNextPageButton(renderer *sdl.Renderer, config *Config, element Element, videos []VideoInfo, elemWidth, elemHeight int32) {
	if !mediaNextPageVisible(config, element, videos) {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	state, _ := mediaPageState(element.Variable)
	label := "Next page"
	if state.loading {
		label = "Loading…"
	} else if state.done {
		label = "No more results"
	}
	rect := mediaNextPageRect(element, elemWidth, elemHeight)
	fill := WithAlpha(ColorSurfaceRaised, 235)
	border := WithAlpha(accentColor, 150)
	if state.loading || state.done {
		fill = WithAlpha(ColorSurfacePanel, 220)
		border = WithAlpha(ColorBorder, 160)
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 1, border)

	textCol := ColorTextSecondary()
	if !state.loading && !state.done {
		textCol = ColorTextPrimary()
		// A small accent chevron signals "more to come".
		chevronX := rect.X + 12
		chevronY := rect.Y + rect.H/2
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 230)
		renderer.DrawLine(chevronX, chevronY-4, chevronX+4, chevronY)
		renderer.DrawLine(chevronX+4, chevronY, chevronX, chevronY+4)
	}
	tw, _, _ := font.SizeUTF8(label)
	renderText(renderer, config, font, label, textCol,
		rect.X+(rect.W-int32(tw))/2+6, rect.Y+(rect.H-int32(font.Height()))/2)
}
