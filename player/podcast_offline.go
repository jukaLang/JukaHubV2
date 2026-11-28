package main

// podcast_offline.go — MED-8: subscriptions you can take with you.
//
// The podcast screen streams enclosures. This file adds the offline side:
// episodes go through the existing Downloads queue (yt-dlp handles a direct
// audio enclosure like any other link), OPML carries subscriptions in and out,
// a Podcasting 2.0 chapters file is parsed for the player's chapter list, and a
// downloaded episode that plays to its end leaves no stale file or queue row.
//
// Everything a test needs is a plain value or a temporary file. The download
// runner is the queue's existing injected seam, so the queue can be exercised
// with no network, no yt-dlp and no card.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	podcastOPMLMax     = 2 << 20
	podcastChaptersMax = 1 << 20
	podcastOPMLName    = "podcasts.opml"
)

// podcastOPMLPath is a variable so tests can point import/export at a temp
// file.
var podcastOPMLPath = defaultPodcastOPMLPath

func defaultPodcastOPMLPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, podcastOPMLName)
}

// ── OPML ──────────────────────────────────────────────────────────────────

type podcastOPMLDoc struct {
	XMLName xml.Name        `xml:"opml"`
	Version string          `xml:"version,attr"`
	Head    podcastOPMLHead `xml:"head"`
	Body    podcastOPMLBody `xml:"body"`
}

type podcastOPMLHead struct {
	Title string `xml:"title,omitempty"`
}

type podcastOPMLBody struct {
	Outlines []podcastOPMLOutline `xml:"outline"`
}

type podcastOPMLOutline struct {
	Text     string               `xml:"text,attr,omitempty"`
	Title    string               `xml:"title,attr,omitempty"`
	Type     string               `xml:"type,attr,omitempty"`
	XMLURL   string               `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string               `xml:"htmlUrl,attr,omitempty"`
	URL      string               `xml:"url,attr,omitempty"`
	Outlines []podcastOPMLOutline `xml:"outline,omitempty"`
}

// podcastOPMLExport builds a standard OPML 2.0 subscription list. titles maps a
// feed URL to its shown title; a missing title falls back to the host.
func podcastOPMLExport(feeds []string, titles map[string]string) ([]byte, error) {
	feeds = sanitizePodcastFeeds(feeds)
	body := podcastOPMLBody{Outlines: make([]podcastOPMLOutline, 0, len(feeds))}
	for _, feed := range feeds {
		title := strings.TrimSpace(titles[feed])
		if title == "" {
			title = podcastFeedLabel(feed)
		}
		body.Outlines = append(body.Outlines, podcastOPMLOutline{
			Text: title, Title: title, Type: "rss", XMLURL: feed,
		})
	}
	doc := podcastOPMLDoc{Version: "2.0", Head: podcastOPMLHead{Title: "JukaHub Podcasts"}, Body: body}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}

// podcastOPMLImport reads feeds from nested OPML outlines. It accepts both the
// usual xmlUrl and the older url spelling, keeps only HTTP(S) feeds and drops
// duplicates and empty entries.
func podcastOPMLImport(data []byte) ([]string, error) {
	if len(data) == 0 || len(data) > podcastOPMLMax {
		return nil, fmt.Errorf("OPML file is empty or too large")
	}
	var doc podcastOPMLDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("read OPML: %w", err)
	}
	var found []string
	var walk func([]podcastOPMLOutline)
	walk = func(outlines []podcastOPMLOutline) {
		for _, o := range outlines {
			feed := strings.TrimSpace(o.XMLURL)
			if feed == "" {
				// Some older exporters used a bare url attribute for the feed.
				feed = strings.TrimSpace(o.URL)
			}
			if feed != "" {
				found = append(found, feed)
			}
			walk(o.Outlines)
		}
	}
	walk(doc.Body.Outlines)
	found = sanitizePodcastFeeds(found)
	if len(found) == 0 {
		return nil, errors.New("the OPML file has no podcast feeds")
	}
	return found, nil
}

func podcastOPMLExportFile(path string) (int, error) {
	shows := podcastShowsInFeedOrder()
	titles := make(map[string]string, len(shows))
	for _, show := range shows {
		if show.Title != "" {
			titles[show.FeedURL] = show.Title
		}
	}
	data, err := podcastOPMLExport(podcastFeeds(), titles)
	if err != nil {
		return 0, err
	}
	if err := AtomicWrite(path, data, 0o644); err != nil {
		return 0, err
	}
	return len(podcastFeeds()), nil
}

func podcastOPMLImportFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	feeds, err := podcastOPMLImport(data)
	if err != nil {
		return 0, err
	}
	added := podcastMergeFeeds(feeds)
	if added > 0 {
		if err := savePodcastSubscriptions(); err != nil {
			return added, err
		}
	}
	return added, nil
}

// podcastMergeFeeds adds feeds to the subscription list and reports how many
// were new. It does not save: the caller decides when the list is committed.
func podcastMergeFeeds(feeds []string) int {
	added := 0
	for _, feed := range sanitizePodcastFeeds(feeds) {
		if addPodcastFeed(feed) {
			added++
		}
	}
	return added
}

// ── Podcasting 2.0 chapters ───────────────────────────────────────────────

var podcastChaptersURLPattern = regexp.MustCompile(`(?is)<podcast:chapters\b[^>]*\burl\s*=\s*["']([^"']+)["']`)

// podcastChaptersURL finds an episode's external chapters JSON. The list is
// fetched when the episode plays, not while a feed refresh is running.
func podcastChaptersURL(block string) string {
	if m := podcastChaptersURLPattern.FindStringSubmatch(block); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

type podcastChaptersDoc struct {
	Chapters []struct {
		StartTime      float64 `json:"startTime"`
		StartTimeSnake float64 `json:"start_time"`
		EndTime        float64 `json:"endTime"`
		EndTimeSnake   float64 `json:"end_time"`
		Title          string  `json:"title"`
	} `json:"chapters"`
}

// podcastParseChaptersJSON reads the Podcasting 2.0 chapter format. It is pure:
// the caller supplies the JSON and the episode duration, so no network or
// ffprobe is needed to check the list. Entries are sorted, duplicate starts are
// collapsed, titles are bounded and missing ends are filled from the next
// chapter (or the episode length).
func podcastParseChaptersJSON(data []byte, duration float64) []audioChapter {
	if len(data) == 0 || len(data) > podcastChaptersMax {
		return nil
	}
	var doc podcastChaptersDoc
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	out := make([]audioChapter, 0, min(len(doc.Chapters), audioChapterMax))
	for _, c := range doc.Chapters {
		if len(out) >= audioChapterMax {
			break
		}
		start := c.StartTime
		if start == 0 && c.StartTimeSnake != 0 {
			start = c.StartTimeSnake
		}
		end := c.EndTime
		if end == 0 && c.EndTimeSnake != 0 {
			end = c.EndTimeSnake
		}
		if start < 0 || start != start || end != end || end < 0 {
			continue
		}
		out = append(out, audioChapter{
			Title: audioChapterCleanTitle(c.Title),
			Start: start,
			End:   end,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	kept := out[:0]
	for _, c := range out {
		if n := len(kept); n > 0 && c.Start-kept[n-1].Start < 0.5 {
			continue
		}
		kept = append(kept, c)
		if len(kept) >= audioChapterMax {
			break
		}
	}
	for i := range kept {
		if kept[i].End <= kept[i].Start {
			switch {
			case i+1 < len(kept):
				kept[i].End = kept[i+1].Start
			case duration > kept[i].Start:
				kept[i].End = duration
			default:
				kept[i].End = kept[i].Start + 1
			}
		}
		if kept[i].Title == "" {
			kept[i].Title = fmt.Sprintf("Chapter %d", i+1)
		}
	}
	return kept
}

// podcastChapterFetch is swapped by tests: production reads the chapter file
// over the same bounded HTTP path as a feed.
var podcastChapterFetch = func(ctx context.Context, rawURL string) string {
	return httpGetTextFallbackContext(ctx, rawURL, 8*time.Second)
}

// podcastLoadEpisodeChapters fetches an episode's external chapter list once
// and leaves it in the same cache the player's chapter rows read. A failed or
// malformed file simply leaves the episode without chapters.
func podcastLoadEpisodeChapters(ep podcastEpisode, path string) {
	if path == "" || strings.TrimSpace(ep.ChaptersURL) == "" || len(audioChaptersFor(path)) > 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body := podcastChapterFetch(ctx, ep.ChaptersURL)
	if strings.TrimSpace(body) == "" {
		return
	}
	list := podcastParseChaptersJSON([]byte(body), ep.Duration)
	if len(list) > 0 {
		audioChaptersStore(path, list)
	}
}

// ── Offline downloads ─────────────────────────────────────────────────────

// podcastOfflinePath is the saved file for an episode URL, when a finished
// Downloads row still points at a real file. The player prefers it to the
// remote enclosure.
func podcastOfflinePath(raw string) string {
	key := dlLibraryKey(raw)
	if key == "" {
		return ""
	}
	for _, it := range downloadsSnapshot() {
		if it.Status == downloadDone && it.Kind == downloadKindAudio &&
			dlLibraryKey(it.URL) == key && regularFile(it.Path) {
			return it.Path
		}
	}
	return ""
}

// podcastDownloadEpisode queues an episode through the normal Downloads
// queue. A saved copy is not queued twice.
func podcastDownloadEpisode(ep podcastEpisode) (downloadItem, error) {
	if local := podcastOfflinePath(ep.URL); local != "" {
		return downloadItem{}, errors.New("this episode is already saved offline")
	}
	return downloadEnqueueKind(ep.URL, downloadKindAudio)
}

// podcastPathOwned reports whether path is inside the app's audio download
// folder. Cleanup never deletes a user's own file.
func podcastPathOwned(path string) bool {
	if strings.TrimSpace(path) == "" || isHTTPMediaURL(path) {
		return false
	}
	root := filepath.Clean(downloadsMusicDir())
	target := filepath.Clean(path)
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// podcastEpisodeCleanup removes a played episode's saved file and its
// Downloads row. It is a no-op for a streamed episode.
func podcastEpisodeCleanup(raw, path string) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(path) == "" || isHTTPMediaURL(path) {
		return
	}
	key := dlLibraryKey(raw)
	var id int64
	var saved string
	downloads.mu.Lock()
	for _, it := range downloads.items {
		if key != "" && it.Status == downloadDone && it.Kind == downloadKindAudio && dlLibraryKey(it.URL) == key {
			id, saved = it.ID, it.Path
			break
		}
	}
	downloads.mu.Unlock()
	if id != 0 {
		downloadRemove(id)
	}
	// A row trimmed from the queue leaves its file in the library; remove it
	// too, but only when it is ours and still the file we played.
	if podcastPathOwned(path) && (saved == "" || filepath.Clean(saved) == filepath.Clean(path)) && regularFile(path) {
		_ = os.Remove(path)
	}
	if audioChaptersKnown(path) {
		audioChaptersStore(path, nil)
	}
}

// ── Panel actions ────────────────────────────────────────────────────────

func podcastPanelDownloadSelected() bool {
	shows := podcastPanelShows()
	if podPanel.show < 0 || podPanel.show >= len(shows) {
		return false
	}
	show := shows[podPanel.show]
	if podPanel.episode < 0 || podPanel.episode >= len(show.Episodes) {
		return false
	}
	ep := show.Episodes[podPanel.episode]
	if local := podcastOfflinePath(ep.URL); local != "" {
		showToast("Already saved offline", ToastInfo())
		return true
	}
	if _, err := podcastDownloadEpisode(ep); err != nil {
		showToast("Download: "+firstLine(err.Error()), ToastWarn())
		return true
	}
	showToast("Downloading "+ep.Title, ToastSuccess())
	return true
}

func podcastPanelExportOPML() bool {
	path := podcastOPMLPath()
	n, err := podcastOPMLExportFile(path)
	if err != nil {
		showToast("OPML export: "+firstLine(err.Error()), ToastError())
		return false
	}
	showToast(fmt.Sprintf("Exported %d feeds to %s", n, filepath.Base(path)), ToastSuccess())
	return true
}

func podcastPanelImportOPML() bool {
	path := podcastOPMLPath()
	n, err := podcastOPMLImportFile(path)
	if err != nil {
		showToast("OPML import: "+firstLine(err.Error()), ToastWarn())
		return false
	}
	if n == 0 {
		showToast("No new feeds in "+filepath.Base(path), ToastInfo())
		return true
	}
	trophyEvent(tkPodcastSub)
	showToast(fmt.Sprintf("Imported %d feeds", n), ToastSuccess())
	podcastPanelRefresh()
	return true
}
