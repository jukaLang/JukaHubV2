package main

// musicm3u8.go — M3U8 playlists, the format every other player writes.
//
// The saved playlists (`musicplaylists.go`) are JSON, which is fine inside
// JukaHub and useless outside it. This file converts between that store and an
// extended M3U file: `#EXTM3U`, `#EXTINF:<seconds>,<title>` and one path per
// line, relative to the playlist file where possible so the pair can be copied
// to a PC or another card together.
//
// Import resolves relative entries against the playlist's own folder and skips
// entries whose file is missing (the count is reported), because a playlist
// that came from a PC routinely names a music folder this card does not have.

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	errMusicPlaylistNotFound = errors.New("that playlist is not saved")
	errMusicPlaylistNoTracks = errors.New("the playlist has no playable tracks")
)

// isMusicPlaylistFile reports whether a file is an M3U playlist the player can
// open. The extension is case-insensitive because cards written on Windows
// carry both spellings.
func isMusicPlaylistFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".m3u8", ".m3u":
		return true
	}
	return false
}

// openMusicPlaylistFile imports an M3U8 picked in Files and starts it. The
// playlist is saved under the file's name, so it is also in Audio → Playlists
// afterwards; missing entries are counted rather than silently dropped.
func openMusicPlaylistFile(config *Config, path string) {
	resolved, err := resolveExplorerEntry(config, path)
	if err != nil {
		showToast("Cannot open "+filepath.Base(path)+": "+filesFriendlyError(err), ToastError())
		return
	}
	pl, missing, err := musicPlaylistImport(resolved, "")
	if err != nil {
		showToast("Cannot import "+filepath.Base(path)+": "+firstLine(err.Error()), ToastError())
		return
	}
	if !musicPlaySavedPlaylist(config, pl.Name) {
		showToast("The playlist has no playable tracks", ToastWarn())
		return
	}
	PlayActivateSound()
	msg := "Playing " + pl.Name
	if missing > 0 {
		msg += fmt.Sprintf(" · %d missing skipped", missing)
	}
	showToast(msg, ToastSuccess())
}

// musicExportSelectedPlaylist writes the playlist the Audio settings row is
// showing to Downloads/playlists as M3U8, the format a PC can read.
func musicExportSelectedPlaylist() {
	pl, ok := musicSelectedPlaylist()
	if !ok {
		showToast("No saved playlist to export — X saves the queue", ToastInfo())
		return
	}
	path, err := musicPlaylistExport(pl.Name, filepath.Join(downloadsRoot(), "playlists"))
	if err != nil {
		showToast("Could not export "+pl.Name+": "+firstLine(err.Error()), ToastError())
		return
	}
	PlayActivateSound()
	showToast("Exported "+filepath.Base(path), ToastSuccess())
}

// musicPlaylistM3U8 renders a saved playlist as extended M3U. Paths inside
// baseDir become relative; anything else (another volume, a URL) stays as it
// is, because a relative path that climbs out of the folder would break as soon
// as the playlist moved.
func musicPlaylistM3U8(pl musicPlaylist, baseDir string) []byte {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#PLAYLIST:" + strings.ReplaceAll(pl.Name, "\n", " ") + "\n")
	for _, tr := range pl.Tracks {
		name := strings.TrimSpace(tr.Name)
		if name == "" {
			name = filepath.Base(tr.Path)
		}
		name = strings.ReplaceAll(name, "\n", " ")
		fmt.Fprintf(&b, "#EXTINF:%d,%s\n", int(tr.Duration+0.5), name)
		p := tr.Path
		if baseDir != "" && !isHTTPMediaURL(p) && !strings.Contains(p, "://") {
			if rel, err := filepath.Rel(baseDir, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				p = filepath.ToSlash(rel)
			}
		}
		b.WriteString(p + "\n")
	}
	return []byte(b.String())
}

// musicParseM3U8 reads an extended or plain M3U. It returns the entries in
// order and how many named files were missing, and never fails on a malformed
// line: a playlist from another player is worth playing even when one entry is
// odd.
func musicParseM3U8(data []byte, baseDir string) ([]musicPlaylistEntry, int) {
	var out []musicPlaylistEntry
	missing := 0
	title := ""
	duration := 0.0
	sc := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			rest := strings.TrimPrefix(line, "#EXTINF:")
			if comma := strings.IndexByte(rest, ','); comma >= 0 {
				if secs, err := strconv.ParseFloat(strings.TrimSpace(rest[:comma]), 64); err == nil {
					duration = secs
				}
				title = strings.TrimSpace(rest[comma+1:])
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			// #EXTM3U, #PLAYLIST, #EXT-X-* and comments: not entries.
			continue
		}
		p := line
		if !isHTTPMediaURL(p) && !strings.Contains(p, "://") {
			if !filepath.IsAbs(p) {
				p = filepath.Join(baseDir, filepath.FromSlash(p))
			}
			p = filepath.Clean(p)
			if _, err := os.Stat(p); err != nil {
				missing++
				title, duration = "", 0
				continue
			}
		}
		name := strings.TrimSpace(title)
		if name == "" {
			if u, err := url.Parse(p); err == nil && u.Path != "" {
				name = filepath.Base(u.Path)
			} else {
				name = filepath.Base(p)
			}
		}
		out = append(out, musicPlaylistEntry{Path: p, Name: name, Duration: duration})
		title, duration = "", 0
	}
	return out, missing
}

// musicPlaylistExport writes the named playlist into dir and returns the file
// it wrote. An empty dir means the app's own data folder, which is where the
// JSON store lives.
func musicPlaylistExport(name, dir string) (string, error) {
	pl, ok := musicPlaylistNamed(name)
	if !ok {
		return "", errMusicPlaylistNotFound
	}
	if dir == "" {
		dir = filepath.Dir(musicPlaylistFilePath())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, musicPlaylistSlug(pl.Name)+".m3u8")
	if err := AtomicWrite(path, musicPlaylistM3U8(pl, dir), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// musicPlaylistImport reads an M3U8 into the saved store, replacing a playlist
// of the same name. name overrides the file's base name when it is not empty.
func musicPlaylistImport(path, name string) (musicPlaylist, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return musicPlaylist{}, 0, err
	}
	if strings.TrimSpace(name) == "" {
		base := filepath.Base(path)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	tracks, missing := musicParseM3U8(data, filepath.Dir(path))
	clean, ok := sanitizeMusicPlaylist(musicPlaylist{Name: name, Tracks: tracks, Updated: time.Now()})
	if !ok {
		return musicPlaylist{}, missing, errMusicPlaylistNoTracks
	}
	if err := musicPlaylistPut(clean); err != nil {
		return musicPlaylist{}, missing, err
	}
	return clean, missing, nil
}

// musicPlaylistSlug turns a playlist name into a file name without separators
// or characters Windows refuses.
func musicPlaylistSlug(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == ' ', r == '.', r == '(':
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		s = "playlist"
	}
	return s
}
