package main

// pkgart.go — registry artwork for the Packages store.
//
// A registry entry may name a picture ("icon": "https://…/icon.png"). The
// store fetches those lazily, in the background, after the list arrives:
// HTTPS only, one megabyte at most, only bytes that sniff as an image, a
// bounded number per pass and a bounded folder on the card. Once a picture
// is cached the card draws it through the shared, bounded app-icon texture
// cache (appicons.go); until then — and for every package that names no
// picture — the card keeps its monogram tile. A package that is installed
// and ships its own icon in its folder uses that one instead.

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pkgArtDirName  = ".jukahub-art"
	pkgArtMaxBytes = 1 << 20
	pkgArtMaxFiles = 96
	// The current registry has more than 24 icons. Leave room for new entries
	// while keeping one catalog pass bounded on a slow connection.
	pkgArtPerPass = 48
)

// pkgArtURL is the picture a registry entry names, when it is a plain HTTPS
// link; nothing else is fetched.
func pkgArtURL(e pkgEntry) string {
	u := strings.TrimSpace(e.Extra.Icon)
	if !strings.HasPrefix(strings.ToLower(u), "https://") {
		return ""
	}
	return u
}

// pkgArtWanted lists the pictures a catalog names, keyed by package folder.
func pkgArtWanted(entries []pkgEntry) map[string]string {
	wanted := map[string]string{}
	for _, e := range entries {
		if u := pkgArtURL(e); u != "" {
			wanted[packageDirName(e.Pkg.Name)] = u
		}
	}
	return wanted
}

// pkgArtPath is where a picture is cached: named by a hash of its URL, so the
// same picture is fetched once whichever package names it. The extension is
// kept when it is an image's, so the decoder can tell the format by name too.
func pkgArtPath(rawURL string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(rawURL))
	ext := strings.ToLower(filepath.Ext(strings.SplitN(rawURL, "?", 2)[0]))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".bmp":
	default:
		ext = ".png"
	}
	return filepath.Join(packageInstallDir(), pkgArtDirName, strconv.FormatUint(h.Sum64(), 16)+ext)
}

// pkgArtCount is how many pictures the cache folder holds.
func pkgArtCount() int {
	entries, err := os.ReadDir(filepath.Join(packageInstallDir(), pkgArtDirName))
	if err != nil {
		return 0
	}
	return len(entries)
}

// pkgFetchArt downloads one picture into the cache.
func pkgFetchArt(ctx context.Context, rawURL, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub/1.0")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the server answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, pkgArtMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > pkgArtMaxBytes || !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return errors.New("not a picture")
	}
	// Written to a temporary file and renamed: a picture cut off by a power
	// loss must not pass for a cached one forever.
	return AtomicWrite(path, data, 0o644)
}

// pkgArtMu keeps two artwork passes of one visit (the cached list's and the
// fresh one's) from fetching and writing the same files at the same time.
var pkgArtMu sync.Mutex

// pkgLoadArt finds the cached pictures for the packages that name one and,
// online, fetches more, handing paths to the screen in small batches. It stops
// with the visit: leaving the screen cancels ctx.
func pkgLoadArt(ctx context.Context, wanted map[string]string) {
	pkgArtMu.Lock()
	defer pkgArtMu.Unlock()
	batch := make(map[string]string, 4)
	budget := pkgArtPerPass
	room := pkgArtMaxFiles - pkgArtCount()
	for key, u := range wanted {
		if ctx.Err() != nil {
			return
		}
		path := pkgArtPath(u)
		if _, err := os.Stat(path); err == nil {
			batch[key] = path
		} else {
			if budget <= 0 || room <= 0 || !IsOnline() {
				continue
			}
			budget--
			if err := pkgFetchArt(ctx, u, path); err != nil {
				continue
			}
			room--
			batch[key] = path
		}
		if len(batch) == 4 {
			pkgPost(pkgEvent{kind: pkgEvArt, icons: batch})
			batch = make(map[string]string, 4)
		}
	}
	if ctx.Err() == nil && len(batch) > 0 {
		pkgPost(pkgEvent{kind: pkgEvArt, icons: batch})
	}
}
