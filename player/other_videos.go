package main

// other_videos.go — Entertainment → Other Videos. One tile per known video
// site opens the link keyboard and queues a video download through the same
// yt-dlp path the Media tab uses. No site is privileged: the tiles are
// shortcuts for pasting a link, not a catalogue of content.

import (
	"net/url"
	"strings"
)

var otherVideosSite videoSite

// otherVideosOpen remembers which site's tile was pressed and opens the link
// keyboard with that site's address as the hint.
func otherVideosOpen(site videoSite) {
	// A site tile opens its own video screen: saved videos, favourites and
	// history from that domain, with search and paste tools on the page.
	siteVideosOpen(site)
}

// otherVideosOpenSearch and otherVideosOpenPlay make a typed domain behave
// like a video service: its own search and its own play field.
func otherVideosOpenSearch(site videoSite) {
	otherVideosSite = site
	vkOpenVideoSearch()
}

func otherVideosOpenPlay(site videoSite) {
	otherVideosSite = site
	vkOpenOtherVideosPlay()
}

// otherVideosPlaceholder is the keyboard's field hint.
func otherVideosPlaceholder() string {
	if host := strings.TrimSpace(otherVideosSite.Host); host != "" {
		host = strings.TrimPrefix(host, "*.")
		return "https://" + host + "/watch?v=…"
	}
	return "https://example.org/video"
}

// otherVideosLinkSubmit queues a video, exactly as the Media tab does, and
// says which site shortcut was used.
func otherVideosLinkSubmit(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if !IsOnline() {
		ShowToast("Connect to the internet before saving a video", ToastKindWarning)
		return
	}
	if _, err := downloadEnqueueKind(raw, downloadKindVideo); err != nil {
		mediaHubEnqueueRefused(err)
		return
	}
	site := otherVideosSite.Name
	if site == "" {
		site = "other sites"
	}
	ShowToast("Queued a video from "+site+" — see Downloads", ToastKindSuccess)
}

// otherVideosDomainSubmit remembers a domain typed on the virtual keyboard.
// It is stored in jukauser.json under "videoSites" and can be removed in
// Settings or from this folder.
func otherVideosDomainSubmit(raw string) {
	host := strings.ToLower(strings.TrimSpace(raw))
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?@: ") || strings.HasPrefix(host, "*.") {
		ShowToast("Type a domain only, for example vimeo.com", ToastKindWarning)
		return
	}
	if c := appConfig; c == nil || !videoSiteAdd(c, host) {
		ShowToast("That domain is already saved or is not valid", ToastKindWarning)
		return
	}
	otherVideosSite = videoSite{Name: host, Host: host, Note: "Your domain"}
	homeTabsTouch()
	ShowToast("Remembered "+host+" — play, search or save a link now", ToastKindSuccess)
}

// otherVideosSearchURL is a typed site's results page for a typed query. The
// site comes from a shortcut the user picked or typed; there is no built-in
// site list for search.
func otherVideosSearchURL(host, query string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	if !strings.HasPrefix(host, "www.") {
		host = "www." + host
	}
	return "https://" + host + "/search?q=" + url.QueryEscape(query)
}

// otherVideosSearchHost is the site for a search: the last shortcut picked,
// or the last domain the user typed.
func otherVideosSearchHost() string {
	if host := strings.TrimSpace(otherVideosSite.Host); host != "" {
		return host
	}
	return videoLastDomain(appConfig)
}

// otherVideosSearchSubmit opens a site's own search in the browser. The
// query is typed on the virtual keyboard, not baked into a settings list.
func otherVideosSearchSubmit(raw string) {
	query := strings.TrimSpace(raw)
	if query == "" {
		return
	}
	if !IsOnline() {
		ShowToast("Connect to the internet to search", ToastKindWarning)
		return
	}
	link := otherVideosSearchURL(otherVideosSearchHost(), query)
	if link == "" {
		ShowToast("Pick a site first, or type its domain in Other Videos", ToastKindInfo)
		return
	}
	if !browserOpenURL(appConfig, link) {
		ShowToast("Could not open the video site in the browser", ToastKindError)
	}
}

// otherVideosPlaySubmit plays a pasted link without saving it first.
func otherVideosPlaySubmit(raw string) {
	link := strings.TrimSpace(raw)
	if link == "" {
		return
	}
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	playVideoURL(appConfig, link)
}

// otherVideosChildren is the folder's tile list, rebuilt from the site list
// so user additions and plugin hosts appear without a code change.
func otherVideosChildren(c *Config) []homeTabItem {
	kids := make([]homeTabItem, 0, 12)
	kids = append(kids,
		homeTabItem{Key: "x:othervideos:domain", Title: "Type a video domain",
			Sub: "Save a site for shortcuts · stored in your settings", Glyph: "download",
			Activate: func(*Config) { vkOpenVideoDomain() }},
		homeTabItem{Key: "x:othervideos:play", Title: "Play a link now",
			Sub: "Paste a link from any yt-dlp site", Glyph: "youtube",
			Activate: func(*Config) { vkOpenOtherVideosPlay() }},
	)
	for _, site := range videoSitesAll(c) {
		// YouTube already has its own Entertainment tile; a second one here
		// would only be redundant.
		if strings.HasSuffix(site.Host, "youtube.com") || strings.HasSuffix(site.Host, "youtu.be") {
			continue
		}
		s := site
		kids = append(kids, homeTabItem{
			Key:      "x:othervideos:" + s.Host,
			Title:    s.Name,
			Sub:      strings.TrimSpace(s.Note + " · " + s.Host),
			Glyph:    "tv",
			Activate: func(*Config) { otherVideosOpen(s) },
		})
	}
	return kids
}
