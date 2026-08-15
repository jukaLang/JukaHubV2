package main

import (
	"encoding/json"
	"testing"
)

// Simulates the single-JSON output yt-dlp returns for a playlist
// (entries[].duration is a float, e.g. 210.0).
func TestVideoInfoParsesFloatDuration(t *testing.T) {
	sample := `{"id":"trending","title":"trending","_type":"playlist","entries":[
		{"_type":"url","ie_key":"Youtube","id":"1YnkZS9e8MA","url":"https://www.youtube.com/watch?v=1YnkZS9e8MA",
		 "title":"Moneybagg Yo - Trending","duration":210.0,"uploader":"DatPiff",
		 "webpage_url":"https://www.youtube.com/watch?v=1YnkZS9e8MA",
		 "thumbnails":[{"url":"https://i.ytimg.com/vi/1YnkZS9e8MA/hq720.jpg","height":202,"width":360}]}
	]}`

	var playlist struct {
		Entries []VideoInfo `json:"entries"`
	}
	if err := json.Unmarshal([]byte(sample), &playlist); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(playlist.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(playlist.Entries))
	}
	v := playlist.Entries[0]
	if v.ID != "1YnkZS9e8MA" || v.Title != "Moneybagg Yo - Trending" || v.Uploader != "DatPiff" {
		t.Errorf("unexpected fields: %+v", v)
	}
	if v.Duration != 210.0 {
		t.Errorf("expected duration 210.0, got %v", v.Duration)
	}
	if v.WebpageURL == "" {
		t.Errorf("expected webpage_url, got empty")
	}
	if v.Thumbnail != "https://i.ytimg.com/vi/1YnkZS9e8MA/hq720.jpg" {
		t.Errorf("expected thumbnail from thumbnails[0], got %q", v.Thumbnail)
	}
	if v.GetURL() != v.WebpageURL {
		t.Errorf("expected GetURL to return webpage_url, got %q", v.GetURL())
	}
}
