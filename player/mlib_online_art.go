package main

// Online artwork is the last fallback after a folder cover and embedded art.
// MusicBrainz identifies the album; Cover Art Archive supplies a small front
// image. Failed matches are remembered for a month so a library scan does not
// repeatedly send the same query.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	mlibOnlineArtRetry       = 30 * 24 * time.Hour
	mlibOnlineArtMaxPerScan  = 12
	mlibOnlineArtMaxDownload = 5 << 20
)

var (
	mlibMusicBrainzURL  = "https://musicbrainz.org/ws/2/release-group/"
	mlibCoverArtURL     = "https://coverartarchive.org/release-group/"
	mlibOnlineArtClient = &http.Client{Timeout: 10 * time.Second}
)

type mlibMBResponse struct {
	Groups []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Score int    `json:"score"`
	} `json:"release-groups"`
}

func mlibArtRequest(ctx context.Context, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub/2.0 (https://github.com/jukaLang/JukaHubV2)")
	return mlibOnlineArtClient.Do(req)
}

// mlibLookupAlbumArt returns a JPEG-sized source image and its release-group
// ID. Empty ID is a normal no-match; errors mean the network should be retried.
func mlibLookupAlbumArt(ctx context.Context, artist, album string) ([]byte, string, error) {
	artist, album = strings.TrimSpace(artist), strings.TrimSpace(album)
	if artist == "" || album == "" {
		return nil, "", nil
	}
	query := `releasegroup:"` + strings.ReplaceAll(album, `"`, "") + `" AND artist:"` + strings.ReplaceAll(artist, `"`, "") + `"`
	values := url.Values{"query": {query}, "fmt": {"json"}, "limit": {"5"}}
	resp, err := mlibArtRequest(ctx, mlibMusicBrainzURL+"?"+values.Encode())
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("MusicBrainz returned %d", resp.StatusCode)
	}
	var found mlibMBResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&found); err != nil {
		return nil, "", err
	}
	id := ""
	for _, group := range found.Groups {
		if group.Score >= 90 && strings.EqualFold(strings.TrimSpace(group.Title), album) && len(group.ID) == 36 {
			id = group.ID
			break
		}
	}
	if id == "" {
		return nil, "", nil
	}
	resp, err = mlibArtRequest(ctx, mlibCoverArtURL+id+"/front-250")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Cover Art Archive returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, mlibOnlineArtMaxDownload+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 || len(data) > mlibOnlineArtMaxDownload {
		return nil, "", errors.New("album art exceeds download limit")
	}
	return data, id, nil
}

func mlibScaleOnlineArt(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > mlibArtMaxPixels {
		return nil, errors.New("album art dimensions exceed limit")
	}
	picture, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, mlibShrink(picture, mlibArtWidth, mlibArtMaxH), &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// mlibFindMissingAlbumArt runs on the media scan worker. It only touches
// albums with real artist and album names and no local cover. Each successful
// image is published to the shelf before the next lookup starts.
func mlibFindMissingAlbumArt(ctx context.Context) {
	if !IsOnline() || mlibArtBudget() <= 0 {
		return
	}
	model := mlibCurrentModel()
	if model == nil {
		return
	}
	lastLookup := time.Time{}
	attempts := 0
	for _, group := range model.albums {
		if ctx.Err() != nil || !IsOnline() || attempts >= mlibOnlineArtMaxPerScan || mlibArtBudget() <= 0 {
			break
		}
		if group.art != "" || len(group.items) == 0 || group.sub == "Unknown artist" || group.title == "Unknown album" {
			continue
		}
		first := model.items[group.items[0]]
		if first.ArtLookup > 0 && time.Since(time.Unix(first.ArtLookup, 0)) < mlibOnlineArtRetry {
			continue
		}
		if !lastLookup.IsZero() {
			wait := time.Until(lastLookup.Add(time.Second))
			if wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
		}
		if !mlibPauseForPlayback(ctx) {
			return
		}
		lastLookup = time.Now()
		attempts++
		data, id, err := mlibLookupAlbumArt(ctx, first.Artist, first.Album)
		if err != nil {
			// An offline or busy service should not turn an album into a
			// 30-day miss. The next scan can try again.
			break
		}
		art := ""
		if len(data) > 0 {
			scaled, scaleErr := mlibScaleOnlineArt(data)
			if scaleErr != nil {
				continue
			}
			path := mlibArtPath("musicbrainz:"+id, int64(len(data)), 0)
			if mlibWriteArt(path, scaled) != nil {
				break
			}
			art = path
		}
		stamp := time.Now().Unix()
		mlibState.mu.Lock()
		for i := range mlibState.items {
			it := &mlibState.items[i]
			if !it.Video && !it.Book && mlibAlbumKey(it.Artist, it.Album) == group.key && it.Art == "" {
				it.ArtLookup = stamp
				it.Art = art
			}
		}
		mlibState.mu.Unlock()
		mlibRebuildModel()
		mlibSave()
	}
}
