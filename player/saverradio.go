package main

// saverradio.go — internet radio for the dock when the card has no music.
//
// The dock's Play button (ambient.go) ended in a toast — "No music found" — on
// a device with no songs copied to it, which left a bedside screen with nothing
// to play. It now tunes into music radio instead, built from three places:
//
//   - the IPStream library's own channels that are music or radio (their group
//     or name says so): the ones the user saved or imported come first;
//   - a random handful of the most popular stations of a public radio playlist
//     on GitHub (junguler/m3u-radio-music-playlists, one calm genre picked at
//     random, or the iptv-org music channels when no genre list can be read),
//     downloaded in the background when the dock opens and kept for a week;
//   - a few long-running stations built in, so a Play before the list has
//     arrived, or with GitHub out of reach, still plays something.
//
// They become one playlist in the music player, so the dock's card, Prev/Next,
// the sleep timer and Screen off all work as they do for songs. A station that
// does not play (a dead link, a web page instead of a stream, a host that never
// sends a byte) ends silent and the player moves on to the next one by itself
// (musicDecodeEnded), repeat-one included; so does one that goes quiet after
// playing, where any other live stream waits for Play. When the list runs out,
// the next Play tunes in to a fresh random set instead of retrying the last
// station.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	// saverRadioCacheFile keeps the downloaded station list in the data folder.
	saverRadioCacheFile = "saverradio.json"
	// saverRadioCacheMaxAge is how long a downloaded list is used before the
	// dock fetches a new one; stations come and go, but not by the day.
	saverRadioCacheMaxAge = 7 * 24 * time.Hour
	// saverRadioRetryAfter spaces out downloads after one that failed, so a
	// dock opened on a captive portal does not try again every few minutes.
	saverRadioRetryAfter = time.Hour
	// saverRadioKeep is how many stations of the list are kept: its most
	// popular, which are the ones most likely to still be on the air.
	saverRadioKeep = 300
	// saverRadioPickFrom is the top of the list the random picks come from.
	saverRadioPickFrom = 150
	// saverRadioPicks is how many downloaded stations one Play queues.
	saverRadioPicks = 14
	// saverRadioLibraryPicks is how many saved IPStream channels lead the list.
	saverRadioLibraryPicks = 3
	// saverRadioMinStations is the least a playlist must hold to be used.
	saverRadioMinStations = 8
	// saverRadioFetchLimit bounds one playlist download. The genre lists are
	// sorted by popularity, so the first megabyte holds far more than the
	// stations kept.
	saverRadioFetchLimit = 1 << 20
	// saverRadioRepo is where the genre playlists live (raw files on GitHub).
	saverRadioRepo = "https://raw.githubusercontent.com/junguler/m3u-radio-music-playlists/main/"
	// saverRadioIPTVMusic is the iptv-org music category, IPStream's own
	// source: music channels whose sound the player decodes without the
	// picture. It is the last list tried, for when no genre list answers.
	saverRadioIPTVMusic = "https://iptv-org.github.io/iptv/categories/music.m3u"
)

// saverRadioGenres are the genre playlists the download picks from: calm ones,
// for a screen that is often a bedside clock.
var saverRadioGenres = []string{"chillout", "ambient", "classical", "acid_jazz", "blues", "60s"}

// saverRadioBuiltin are stations that have kept the same address for years.
// They close every list, so a first Play plays even before a download.
var saverRadioBuiltin = []IPStream{
	{Name: "SomaFM · Groove Salad", URL: "https://ice2.somafm.com/groovesalad-128-mp3"},
	{Name: "SomaFM · Drone Zone", URL: "https://ice2.somafm.com/dronezone-128-mp3"},
	{Name: "SomaFM · Secret Agent", URL: "https://ice2.somafm.com/secretagent-128-mp3"},
	{Name: "SomaFM · Lush", URL: "https://ice2.somafm.com/lush-128-mp3"},
	{Name: "Radio Paradise · Main Mix", URL: "https://stream.radioparadise.com/mp3-128"},
	{Name: "Radio Paradise · Mellow Mix", URL: "https://stream.radioparadise.com/mellow-128"},
	{Name: "Radio Swiss Jazz", URL: "https://stream.srg-ssr.ch/m/rsj/mp3_128"},
	{Name: "FIP", URL: "https://icecast.radiofrance.fr/fip-midfi.mp3"},
}

// saverRadioCache is the downloaded list as it is kept on disk.
type saverRadioCache struct {
	Fetched  int64      `json:"fetched"` // unix seconds
	Source   string     `json:"source"`
	Stations []IPStream `json:"stations"`
}

// saverRadio is the downloaded list and the radio playlist the player holds.
// Lock order: music.mu may be held while this lock is taken, never the reverse.
var saverRadio struct {
	mu       sync.Mutex
	loaded   bool // the cache file was read
	cache    saverRadioCache
	failedAt time.Time       // the last download that found no list
	listed   map[string]bool // the stations of the radio playlist last started
	deadEnd  bool            // that playlist ran out without a station playing on
}

// saverRadioFetching is set while a download runs; one at a time.
var saverRadioFetching atomic.Bool

// saverRadioCachePath is the station list's file, next to the other app data.
func saverRadioCachePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, saverRadioCacheFile)
}

// saverRadioLoadLocked reads the cached list once. A missing or unreadable file
// is no list: it is only a cache, and the next dock opening downloads it again.
func saverRadioLoadLocked() {
	if saverRadio.loaded {
		return
	}
	saverRadio.loaded = true
	data, err := os.ReadFile(saverRadioCachePath())
	if err != nil {
		if !os.IsNotExist(err) {
			logWarn("saverradio", "reading the station list: %v", err)
		}
		return
	}
	var c saverRadioCache
	if err := json.Unmarshal(data, &c); err != nil {
		logWarn("saverradio", "ignoring an unreadable station list: %v", err)
		return
	}
	c.Stations = saverRadioClean(c.Stations, saverRadioKeep)
	saverRadio.cache = c
}

// saverRadioClean keeps the entries of a downloaded playlist the music player
// can open: an http(s) stream link, each once, with a short readable name, at
// most limit of them, in the playlist's order. A link to another playlist
// (.pls, .m3u, .asx, …) is dropped: ffmpeg cannot follow those, and each would
// cost a failed attempt.
func saverRadioClean(list []IPStream, limit int) []IPStream {
	seen := make(map[string]bool, len(list))
	out := make([]IPStream, 0, min(len(list), max(limit, 0)))
	for _, s := range list {
		link := strings.TrimSpace(s.URL)
		if !isHTTPMediaURL(link) || strings.ContainsAny(link, " \t\r\n") ||
			saverRadioPlaylistLink(link) || seen[link] {
			continue
		}
		seen[link] = true
		name := strings.Join(strings.Fields(s.Name), " ")
		if runes := []rune(name); len(runes) > 80 {
			name = strings.TrimSpace(string(runes[:80]))
		}
		if name == "" || name == link {
			name = channelNameFromURL(link)
		}
		out = append(out, IPStream{Name: name, URL: link})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// saverRadioParse reads a downloaded playlist into the stations kept: Live TV's
// parser (BOM, CRLF, quoted commas, tvg-name, duplicates), then
// saverRadioClean.
func saverRadioParse(data string) []IPStream {
	chans := parseM3UChannels(data)
	list := make([]IPStream, 0, len(chans))
	for _, ch := range chans {
		list = append(list, IPStream{Name: ch.Name, URL: ch.URL})
	}
	return saverRadioClean(list, saverRadioKeep)
}

// saverRadioPlaylistLink reports a link to a playlist file rather than to a
// stream. HLS (.m3u8) is a stream to ffmpeg and stays.
func saverRadioPlaylistLink(link string) bool {
	path := strings.ToLower(link)
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	for _, ext := range []string{".pls", ".m3u", ".asx", ".xspf", ".ram", ".wax"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// saverRadioMusical reports a saved IPStream channel that is music or radio:
// its group (iptv-org writes "Music", or "Entertainment;Music") or a word of
// its name says so.
func saverRadioMusical(s IPStream) bool {
	if strings.Contains(strings.ToLower(s.Group), "music") {
		return true
	}
	words := strings.FieldsFunc(strings.ToLower(s.Group+" "+s.Name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, w := range words {
		switch w {
		case "music", "musik", "musique", "musica", "música", "radio", "fm", "jazz", "lofi", "chillout":
			return true
		}
	}
	return false
}

// saverRadioLibraryChannels is the IPStream library's music and radio
// channels the player can open, without the ones whose last try failed.
func saverRadioLibraryChannels() []IPStream {
	var out []IPStream
	for _, s := range ipStreamList() {
		if isHTTPMediaURL(s.URL) && s.LastResult != ipStreamFailed && saverRadioMusical(s) {
			out = append(out, s)
		}
	}
	return out
}

// saverRadioShuffled returns a shuffled copy of list.
func saverRadioShuffled(list []IPStream) []IPStream {
	out := append([]IPStream(nil), list...)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// saverRadioPlaylist builds the list one Play starts: a few saved music
// channels, then random stations from the top of the downloaded list, then
// the built-in ones, each link once.
func saverRadioPlaylist(library, downloaded []IPStream) []musicTrack {
	var tracks []musicTrack
	seen := make(map[string]bool)
	add := func(list []IPStream, n int) {
		for _, s := range list {
			if n <= 0 {
				return
			}
			if s.URL == "" || seen[s.URL] {
				continue
			}
			seen[s.URL] = true
			tracks = append(tracks, musicTrack{Path: s.URL, Name: s.Name})
			n--
		}
	}
	add(saverRadioShuffled(library), saverRadioLibraryPicks)
	if len(downloaded) > saverRadioPickFrom {
		downloaded = downloaded[:saverRadioPickFrom]
	}
	add(saverRadioShuffled(downloaded), saverRadioPicks)
	add(saverRadioShuffled(saverRadioBuiltin), len(saverRadioBuiltin))
	return tracks
}

// saverRadioPlay starts internet radio in the music player. The dock's Play
// calls it when the card has no music; it reports whether it started, which
// it does not do offline. Main thread.
func saverRadioPlay(config *Config) bool {
	if !IsOnline() && !netReconnecting() {
		return false
	}
	saverRadio.mu.Lock()
	saverRadioLoadLocked()
	downloaded := append([]IPStream(nil), saverRadio.cache.Stations...)
	saverRadio.mu.Unlock()
	tracks := saverRadioPlaylist(saverRadioLibraryChannels(), downloaded)
	if !musicPlayPlaylist(tracks, 0) {
		return false
	}
	// Stations: the dock's Play stops them rather than pause (music_transport.go).
	musicMarkLive(tracks)
	// Marked once the player holds the list, so a decoder of the previous
	// list that ends in between is not taken for a station of this one.
	listed := make(map[string]bool, len(tracks))
	for _, t := range tracks {
		listed[t.Path] = true
	}
	saverRadio.mu.Lock()
	saverRadio.listed = listed
	saverRadio.deadEnd = false
	saverRadio.mu.Unlock()
	logInfo("saverradio", "no music on the card: tuning in to %q (%d stations, %d downloaded)",
		tracks[0].Name, len(tracks), len(downloaded))
	musicPlayTrack(0)
	// A first Play before the list arrived played the built-in stations; the
	// download runs now for the next one.
	saverRadioPrefetch(config)
	return true
}

// saverRadioListed reports whether path is a station of the radio playlist the
// dock started last, while the player still holds that playlist: a list the
// user started since is not radio, even when it holds one of the same links.
// It takes music.mu, so it is never called with music.mu held.
func saverRadioListed(path string) bool {
	music.mu.Lock()
	n, first := len(music.tracks), ""
	if n > 0 {
		first = music.tracks[0].Path
	}
	music.mu.Unlock()
	saverRadio.mu.Lock()
	defer saverRadio.mu.Unlock()
	return n > 0 && n == len(saverRadio.listed) && saverRadio.listed[first] && saverRadio.listed[path]
}

// saverRadioMarkDeadEnd records that the radio playlist ran out (musicDecodeEnded):
// the dock's next Play tunes in to fresh stations instead of replaying the
// last one.
func saverRadioMarkDeadEnd() {
	saverRadio.mu.Lock()
	saverRadio.deadEnd = true
	saverRadio.mu.Unlock()
}

// saverRadioCurrentPath is the current track's link while the player holds the
// dock's radio playlist, else "".
func saverRadioCurrentPath() string {
	music.mu.Lock()
	path := ""
	if music.current >= 0 && music.current < len(music.tracks) {
		path = music.tracks[music.current].Path
	}
	music.mu.Unlock()
	if path == "" || !saverRadioListed(path) {
		return ""
	}
	return path
}

// saverRadioNeedsRetune reports a stopped radio playlist that ran out — every
// station tried failed, typically while the Wi-Fi was away, or the last one
// went quiet. Play then tunes in to fresh random stations: replaying the
// stopped one would only fail again. A station stopped by hand or by the sleep
// timer is resumed as any track is.
func saverRadioNeedsRetune() bool {
	music.mu.Lock()
	stopped := !music.playing && !music.paused
	music.mu.Unlock()
	if !stopped || saverRadioCurrentPath() == "" {
		return false
	}
	saverRadio.mu.Lock()
	defer saverRadio.mu.Unlock()
	return saverRadio.deadEnd
}

// saverRadioStateText is the dock card's state line while the radio plays:
// "Tuning in…" until the station's first sound, then "Live radio". "" for
// anything else, and while paused (the card says Paused).
func saverRadioStateText() string {
	if saverRadioCurrentPath() == "" {
		return ""
	}
	music.mu.Lock()
	playing, paused, heard := music.playing, music.paused, music.pcmBytes > 0
	music.mu.Unlock()
	switch {
	case !playing || paused:
		return ""
	case !heard:
		return "Tuning in…"
	}
	return "Live radio"
}

// saverRadioDue reports whether the station list should be downloaded: there
// is none, or it is a week old (or from a clock that has since been set back),
// and no download failed within the last hour.
func saverRadioDue(c saverRadioCache, failedAt, now time.Time) bool {
	// Both times carry the monotonic clock, so a clock set back by the first
	// NTP answer does not stretch or skip this wait.
	if !failedAt.IsZero() && now.Sub(failedAt) < saverRadioRetryAfter {
		return false
	}
	if len(c.Stations) == 0 || c.Fetched <= 0 {
		return true
	}
	age := now.Sub(time.Unix(c.Fetched, 0))
	return age > saverRadioCacheMaxAge || age < -24*time.Hour
}

// saverRadioPrefetch downloads the station list in the background when the dock
// opens with nothing loaded in the player, so Play has it at once. It does
// nothing offline, while a download runs, while a fresh list is kept, within an
// hour of a download that failed, or when the card has music to play. Main
// thread (it reads the config); the work is on its own goroutine.
func saverRadioPrefetch(config *Config) {
	if !IsOnline() {
		return
	}
	music.mu.Lock()
	loaded := len(music.tracks) > 0
	music.mu.Unlock()
	if loaded && saverRadioCurrentPath() == "" {
		return
	}
	// The goroutine looks for music through a config of its own: only the
	// Files root is read, and the live config belongs to this thread.
	probe := &Config{}
	if config != nil {
		probe.Variables.FileExplorerRoot = config.Variables.FileExplorerRoot
	}
	if !saverRadioFetching.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer saverRadioFetching.Store(false)
		saverRadio.mu.Lock()
		saverRadioLoadLocked()
		due := saverRadioDue(saverRadio.cache, saverRadio.failedAt, time.Now())
		saverRadio.mu.Unlock()
		if !due || saverFindMusicDir(probe) != "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cache, err := saverRadioDownload(ctx)
		saverRadio.mu.Lock()
		if err != nil {
			saverRadio.failedAt = time.Now()
		} else {
			saverRadio.cache = cache
			saverRadio.failedAt = time.Time{}
		}
		saverRadio.mu.Unlock()
		if err != nil {
			logWarn("saverradio", "no station list: %v", err)
			return
		}
		data, err := json.Marshal(cache)
		if err == nil {
			err = AtomicWrite(saverRadioCachePath(), data, 0o644)
		}
		if err != nil {
			logWarn("saverradio", "saving the station list: %v", err)
		}
		logInfo("saverradio", "%d stations from %s", len(cache.Stations), cache.Source)
	}()
}

// saverRadioSources is the order the download tries: three genre lists picked
// at random (a genre the repository dropped answers 404 at once), then the
// iptv-org music channels.
func saverRadioSources() []string {
	genres := saverRadioShuffledStrings(saverRadioGenres)
	out := make([]string, 0, 4)
	for _, g := range genres[:min(3, len(genres))] {
		out = append(out, saverRadioRepo+g+".m3u")
	}
	return append(out, saverRadioIPTVMusic)
}

// saverRadioShuffledStrings returns a shuffled copy of list.
func saverRadioShuffledStrings(list []string) []string {
	out := append([]string(nil), list...)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// saverRadioDownload fetches the first source that lists enough stations.
func saverRadioDownload(ctx context.Context) (saverRadioCache, error) {
	var lastErr error
	for _, src := range saverRadioSources() {
		if ctx.Err() != nil {
			break
		}
		data, err := saverRadioFetch(ctx, src)
		if err != nil {
			lastErr = err
			continue
		}
		list := saverRadioParse(data)
		if len(list) < saverRadioMinStations {
			lastErr = fmt.Errorf("%s lists %d playable stations", src, len(list))
			continue
		}
		return saverRadioCache{Fetched: time.Now().Unix(), Source: src, Stations: list}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no playlist could be read")
	}
	return saverRadioCache{}, lastErr
}

// saverRadioFetch downloads one playlist, at most saverRadioFetchLimit bytes. A
// longer one is cut at its last whole line, so a link cut in half is not kept.
func saverRadioFetch(ctx context.Context, link string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s replied %s", link, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, saverRadioFetchLimit+1))
	if err != nil {
		return "", err
	}
	if len(body) > saverRadioFetchLimit {
		body = body[:saverRadioFetchLimit]
		if cut := strings.LastIndexByte(string(body), '\n'); cut >= 0 {
			body = body[:cut+1]
		}
	}
	return string(body), nil
}
