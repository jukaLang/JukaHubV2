package main

import (
	"net/url"
	"strings"
)

// externalURLAllowed validates a link before it is handed to the desktop.
//
// A URL used to reach cmd.exe as `cmd /c start "" <url>` from a config trigger:
// the shell then read `&`, `|` and `^` in the query string as its own syntax,
// so a URL could start a program, and ordinary links were cut at the first `&`.
// Only real web and mail links are accepted here; the caller opens them without
// a shell.
func externalURLAllowed(raw string) (string, bool) {
	link := strings.TrimSpace(raw)
	if link == "" {
		return "", false
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme == "" {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		// A scheme with no host is not a page ("https://" alone): the desktop
		// handler has nothing to open and some handlers guess a local path.
		if strings.TrimSpace(u.Host) == "" {
			return "", false
		}
		return link, true
	case "mailto":
		return link, true
	}
	return "", false
}
