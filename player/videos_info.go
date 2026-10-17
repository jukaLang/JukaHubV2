package main

// videos_info.go defines the video metadata type used by favorites, shorts and
// the embedded player bridge. Shape recovered from git history (c8aa566
// player/main.go) so all existing call sites match.

import (
	"encoding/json"
)

// --- Video info struct ---
type VideoInfo struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Uploader    string   `json:"uploader"`
	Channel     string   `json:"channel"`
	Duration    float64  `json:"duration"`
	Thumbnail   string   `json:"thumbnail"`
	Thumbnails  []string `json:"-"`
	Description string   `json:"description"`
	ViewCount   int64    `json:"view_count"`
	UploadDate  string   `json:"upload_date"`
	WebpageURL  string   `json:"webpage_url"`
	URL         string   `json:"url"`
	// Position is the last watched offset in seconds (0 = not started). It is
	// persisted with favorites so the Continue card can resume playback.
	Position float64 `json:"position,omitempty"`
}

func (v *VideoInfo) UnmarshalJSON(data []byte) error {
	type Alias VideoInfo
	aux := &struct {
		Thumbnails []struct {
			URL string `json:"url"`
		} `json:"thumbnails"`
		*Alias
	}{
		Alias: (*Alias)(v),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if len(aux.Thumbnails) > 0 {
		v.Thumbnail = aux.Thumbnails[0].URL
		for _, t := range aux.Thumbnails {
			if t.URL != "" {
				v.Thumbnails = append(v.Thumbnails, t.URL)
			}
		}
	}
	return nil
}

func (v *VideoInfo) GetURL() string {
	if v.WebpageURL != "" {
		return v.WebpageURL
	}
	if v.URL != "" {
		return v.URL
	}
	if v.ID != "" {
		return "https://www.youtube.com/watch?v=" + v.ID
	}
	return ""
}

