package main

// ytoembed.go — a title for a video that arrived without one.
//
// A YouTube link followed in the browser plays in Media with the link's text
// as its title (brwPlayYouTube). A thumbnail link, a bare address or a "Play"
// button carries no text worth keeping, so the video was recorded with no
// title at all, and Home's Continue card and Recents showed the raw watch URL
// in its place. YouTube's oEmbed endpoint answers "what is this video called"
// for any public video with one small GET — no API key, no account, no
// JavaScript — so the title is looked up in the background and written into
// the recent entry while the video plays.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ytOEmbedTimeout bounds the lookup; a title that is late is not worth more.
const ytOEmbedTimeout = 8 * time.Second

// parseYtOEmbed reads the title and channel out of an oEmbed answer.
func parseYtOEmbed(data []byte) (title, channel string, ok bool) {
	var answer struct {
		Title      string `json:"title"`
		AuthorName string `json:"author_name"`
	}
	if json.Unmarshal(data, &answer) != nil {
		return "", "", false
	}
	title = strings.Join(strings.Fields(answer.Title), " ")
	if title == "" {
		return "", "", false
	}
	return truncateRunesSuffix(title, 120, "…"), strings.TrimSpace(answer.AuthorName), true
}

// fetchYouTubeTitle asks the oEmbed endpoint for a video's title and channel.
func fetchYouTubeTitle(ctx context.Context, videoID string) (string, string, error) {
	watch := "https://www.youtube.com/watch?v=" + videoID
	endpoint := "https://www.youtube.com/oembed?format=json&url=" + url.QueryEscape(watch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	setWebBrowserHeaders(req, "none", "navigate", "document")
	req.Header.Set("Accept", "application/json")
	resp, err := ytClient().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 401/403 for a private or embedding-disabled video, 404 for a removed
		// one: no title to be had, and nothing to retry.
		return "", "", fmt.Errorf("oembed answered HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", "", err
	}
	title, channel, ok := parseYtOEmbed(data)
	if !ok {
		return "", "", fmt.Errorf("oembed answer carried no title")
	}
	return title, channel, nil
}

// ytFillMissingTitle looks up the title of a video that is being played
// without one and writes it into the recently-played list. It returns at once.
// A pasted or typed link (Play Link, the search bar, playSmartURL) arrives as
// a bare URL, so the id is read out of it when the entry has none.
func ytFillMissingTitle(v VideoInfo) {
	if strings.TrimSpace(v.Title) != "" {
		return
	}
	key := v.GetURL()
	id := v.ID
	if !brwYouTubeID(id) {
		id = youtubeVideoID(key)
	}
	if !brwYouTubeID(id) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), ytOEmbedTimeout)
		defer cancel()
		title, channel, err := fetchYouTubeTitle(ctx, id)
		if err != nil {
			logDebug("youtube", "no title for %s: %v", id, err)
			return
		}
		if updateRecentTitle(key, title, channel) {
			logDebug("youtube", "titled %s from oEmbed", id)
		}
	}()
}

// updateRecentTitle names a recent video that has no title yet. It never
// overwrites a title the entry already has, and reports whether it named one.
// The store is saved with everything else on the next save.
func updateRecentTitle(key, title, channel string) bool {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	for i := range favoritesStore.Recent {
		it := &favoritesStore.Recent[i]
		if it.Type != "video" {
			continue
		}
		v, ok := it.Data.(VideoInfo)
		if !ok || v.GetURL() != key || strings.TrimSpace(v.Title) != "" {
			continue
		}
		v.Title = title
		if v.Channel == "" {
			v.Channel = channel
		}
		if v.Uploader == "" {
			v.Uploader = channel
		}
		it.Data = v
		return true
	}
	return false
}
