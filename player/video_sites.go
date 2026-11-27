package main

// video_sites.go — a short list of video sites yt-dlp is known to handle,
// plus the user's own additions. Downloads have never been limited to YouTube:
// this list is for labels and for a one-press "add a site" setting, not a host
// gate. A plugin can advertise more sites through its "videoHosts" manifest.

import (
	"net/url"
	"strings"
)

const videoSitesCustomKey = "videoSites"

type videoSite struct {
	Name string
	Host string
	Note string
}

// videoBuiltinSites are familiar sites with public video pages. None of these
// entries grants access to anything: the user still supplies the link.
// No site is built in. The bundled yt-dlp's search/extract behaviour depends
// on the network and the site's anti-bot rules, so a shortcut that works on
// one machine fails on another. A site appears only after the user types its
// domain (or a plugin adds it); "any http(s) link yt-dlp can read" still
// works without a shortcut.
var videoBuiltinSites = []videoSite{
	{"Bilibili", "bilibili.com", "Anime, gaming and creator videos"},
	{"SoundCloud", "soundcloud.com", "Tracks and sets"},
}

// videoUserSites is the user's comma-separated list from jukauser.json.
func videoUserSites(c *Config) []string {
	if c == nil {
		return nil
	}
	raw, _ := c.Variables.Custom[videoSitesCustomKey].(string)
	var out []string
	for _, host := range strings.Split(raw, ",") {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" && plugxHostPattern().MatchString(host) {
			out = append(out, host)
		}
	}
	return out
}

// videoSitesAll is the union of built-in, user and plugin sites, in that
// order, without duplicates.
func videoSitesAll(c *Config) []videoSite {
	out := append([]videoSite(nil), videoBuiltinSites...)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Host] = true
	}
	for _, host := range videoUserSites(c) {
		if !seen[host] {
			seen[host] = true
			out = append(out, videoSite{Host: host, Name: host, Note: "Added in Settings"})
		}
	}
	for _, h := range plugxVideoHosts() {
		if !seen[h.Host] && !seen[strings.TrimPrefix(h.Host, "*.")] {
			seen[h.Host] = true
			out = append(out, videoSite{Host: h.Host, Name: h.Host + " (" + h.Plugin + ")", Note: "From a plugin"})
		}
	}
	return out
}

// videoSiteSummary is the settings read-out, kept short enough for one line.
func videoSiteSummary(c *Config) string {
	sites := videoSitesAll(c)
	names := make([]string, 0, len(sites))
	for _, s := range sites {
		names = append(names, s.Name)
	}
	return strings.Join(names, " · ")
}

// videoSiteForURL names a site a queued link came from, for the download row.
// It returns "" for a link no known site claims.
func videoSiteForURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	for _, s := range videoSitesAll(appConfig) {
		want := s.Host
		if strings.HasPrefix(want, "*.") {
			if strings.HasSuffix(host, want[1:]) {
				return s.Name
			}
			continue
		}
		if host == want || strings.HasSuffix(host, "."+want) {
			return s.Name
		}
	}
	return ""
}

// videoCustomSites is only the domains the user typed themselves, for the
// remove row and for showing what is kept in jukauser.json.
func videoCustomSites(c *Config) []string {
	return videoUserSites(c)
}

// videoSiteRemove forgets one typed domain. Built-in sites cannot be removed.
func videoSiteRemove(c *Config, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if c == nil || host == "" {
		return false
	}
	kept := make([]string, 0, 2)
	found := false
	for _, have := range videoUserSites(c) {
		if have == host {
			found = true
			continue
		}
		kept = append(kept, have)
	}
	if !found {
		return false
	}
	setStoreString(c, videoSitesCustomKey, strings.Join(kept, ","))
	return true
}

// videoLastDomain is the most recently typed domain. Search uses it until
// another site is picked, so entering a domain is enough to make "Search this
// site" work without an extra selection step.
func videoLastDomain(c *Config) string {
	list := videoCustomSites(c)
	if len(list) == 0 {
		return ""
	}
	return list[len(list)-1]
}

// videoSiteRemoveOptions is the carousel's stops: only domains the user typed.
func videoSiteRemoveOptions(c *Config) []setOption {
	var out []setOption
	for _, host := range videoCustomSites(c) {
		out = append(out, setOption{Value: host, Label: host, Note: "Removes this shortcut from your settings"})
	}
	return out
}

// videoSiteAddOptions is the carousel's stops: the built-in sites the user
// has not added yet. User additions are kept in a plain custom string.
func videoSiteAddOptions(c *Config) []setOption {
	have := map[string]bool{}
	for _, host := range videoUserSites(c) {
		have[host] = true
	}
	for _, s := range videoSitesAll(c) {
		if !strings.HasPrefix(s.Host, "*.") {
			have[s.Host] = true
		}
	}
	var out []setOption
	for _, s := range videoBuiltinSites {
		if have[s.Host] {
			continue
		}
		out = append(out, setOption{Value: s.Host, Label: s.Name, Note: s.Note + " · " + s.Host})
	}
	return out
}

// videoSiteAdd remembers one built-in host. It is idempotent and validates
// the value again, since a setting can be changed from a config file too.
func videoSiteAdd(c *Config, value string) bool {
	host := strings.ToLower(strings.TrimSpace(value))
	if c == nil || !plugxHostPattern().MatchString(host) {
		return false
	}
	existing := videoUserSites(c)
	for _, have := range existing {
		if have == host {
			return false
		}
	}
	existing = append(existing, host)
	setStoreString(c, videoSitesCustomKey, strings.Join(existing, ","))
	return true
}
