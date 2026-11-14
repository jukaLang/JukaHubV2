package main

// browser_download.go — saving a web address to Downloads.
//
// The browser reads pages, so an address that answers with a PDF, an archive
// or a song stops at the "Can't show this file" card. Saving is kept out of
// that reader entirely: this file streams the response to a temporary file in
// Downloads, then gives it the name the server (or the address) asked for.
//
// The menu can save the page on screen too, and the link picker can save the
// file behind a link without opening it first. Every save is a row in the
// browser download registry, which the Downloads panel merges with the media
// queue: progress, completion, failures and cancellation live there instead of
// in a toast that disappears while a large file is still arriving.

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// browserDownloadMax is the largest file the browser will save. It is a
	// guard against a server that never ends, not a promise the disk can hold
	// it: the free-space check below is what stops a file that will not fit.
	browserDownloadMax = int64(2 << 30)
	// browserDownloadStall is how long a save may receive no bytes before it is
	// given up, matching the chat's transfers.
	browserDownloadStall = 60 * time.Second
	// browserDownloadProgressInterval coalesces byte counts: a fast local
	// server can produce thousands of reads a second, and redrawing the panel
	// that often costs more than the numbers are worth.
	browserDownloadProgressInterval = 150 * time.Millisecond
	// browserDownloadLimit keeps the finished rows bounded. The files stay in
	// Downloads; only the panel history is capped, like the media queue.
	browserDownloadLimit = 20
)

// browserDownloadItem is one browser save. Its fields are written under
// browserDownloads.mu; the panel reads copies (browserDownloadsSnapshot).
type browserDownloadItem struct {
	ID     int64
	URL    string
	Title  string
	Source string
	Status downloadStatus

	Done  int64
	Total int64
	Speed float64

	Err  string
	Path string

	added    time.Time
	finished time.Time
	dir      string
	hint     string
	cancel   context.CancelFunc
}

// browserDownloads is the browser's own queue. It is deliberately separate
// from downloads.go: a browser save must not wait behind a yt-dlp track, and
// pausing media must not stop it. The panel knows both registries.
type browserDownloadsQueue struct {
	mu     sync.Mutex
	items  []*browserDownloadItem
	nextID int64
}

var browserDownloads browserDownloadsQueue

// browserDownloadProgress is one progress callback from browserFetchToDir.
// Name arrives with the first callback, once the server's headers have named
// the file, and is what the panel shows instead of the link text.
type browserDownloadProgress struct {
	Done  int64
	Total int64
	Speed float64
	Name  string
}

// browserDownloadsSnapshot hands the panel copies of every browser save, in
// the same section order as the media queue: active, failed, then saved with
// the newest first.
func browserDownloadsSnapshot() []downloadItem {
	browserDownloads.mu.Lock()
	items := make([]browserDownloadItem, 0, len(browserDownloads.items))
	for _, it := range browserDownloads.items {
		items = append(items, *it)
	}
	browserDownloads.mu.Unlock()
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := downloadSortRank(items[i].Status), downloadSortRank(items[j].Status)
		if ri != rj {
			return ri < rj
		}
		if items[i].Status == downloadDone && !items[i].finished.Equal(items[j].finished) {
			return items[i].finished.After(items[j].finished)
		}
		return items[i].ID < items[j].ID
	})
	out := make([]downloadItem, 0, len(items))
	for _, it := range items {
		out = append(out, downloadItem{
			ID: it.ID, URL: it.URL, Kind: downloadKindFile,
			Title: it.Title, Source: it.Source, Status: it.Status,
			Done: it.Done, Total: it.Total, Speed: it.Speed,
			Err: it.Err, Path: it.Path,
		})
	}
	return out
}

// browserDownloadPageURL is the address the browser can save now: the page on
// screen, or the file whose "Can't show this file" card is up. An empty
// address means there is nothing to save yet.
func browserDownloadPageURL(elem Element) string {
	// A file the browser could not draw is the page on screen, even if a
	// previous document is still cached behind the card.
	if f, ok := brwFailureFor(elem.Variable); ok && f.Kind == brwFailContent {
		return strings.TrimSpace(f.URL)
	}
	if doc := browserDocumentFor(elem.Variable); doc != nil {
		if address := strings.TrimSpace(doc.URL); address != "" {
			return address
		}
	}
	return ""
}

// browserDownloadCurrentPage saves the page (or the failed file) the browser
// is showing. The menu row and the Download trigger both land here.
func browserDownloadCurrentPage(config *Config) bool {
	elem, ok := findBrowserElement(config)
	if !ok {
		showToast("Browser is not available on this screen", ToastError())
		return false
	}
	address := browserDownloadPageURL(elem)
	if address == "" {
		showToast("Open a page first", ToastInfo())
		return false
	}
	return browserStartDownload(address, browserBookmarkTitle(config, elem))
}

// browserStartDownload begins saving address. It reports whether a save was
// started, so the link picker can close only when it did.
func browserStartDownload(address, nameHint string) bool {
	address, err := normalizeTextBrowserURL(strings.TrimSpace(address))
	if err != nil {
		showToast("That address can't be downloaded", ToastError())
		return false
	}
	if !IsOnline() {
		showToast("Connect to the internet to download", ToastInfo())
		return false
	}
	dir := strings.TrimSpace(chatSaveDir())
	if dir == "" {
		showToast("Couldn't find the Downloads folder", ToastError())
		return false
	}

	browserDownloads.mu.Lock()
	if browserDownloadBusyLocked(address) {
		browserDownloads.mu.Unlock()
		showToast("That download is already running", ToastInfo())
		return false
	}
	browserDownloads.nextID++
	id := browserDownloads.nextID
	it := &browserDownloadItem{
		ID: id, URL: address, Title: safeDownloadName(nameHint), Source: browserDownloadHost(address),
		Status: downloadActive, added: time.Now(), dir: dir, hint: nameHint,
	}
	ctx, cancel := context.WithCancel(context.Background())
	it.cancel = cancel
	browserDownloads.items = append(browserDownloads.items, it)
	browserDownloads.trimLocked()
	browserDownloads.mu.Unlock()

	PlayActivateSound()
	showToast("Download started — see Downloads", ToastInfo())
	go browserRunDownload(id, ctx, address, dir, nameHint)
	return true
}

// browserDownloadBusyLocked reports a save for the same address that is still
// running. A second press would write the same URL twice.
func browserDownloadBusyLocked(address string) bool {
	for _, it := range browserDownloads.items {
		if it.URL == address && (it.Status == downloadActive || it.Status == downloadQueued) {
			return true
		}
	}
	return false
}

// browserDownloadFindLocked returns one item by id. The caller holds the mutex.
func browserDownloadFindLocked(id int64) (*browserDownloadItem, int) {
	for i, it := range browserDownloads.items {
		if it.ID == id {
			return it, i
		}
	}
	return nil, -1
}

// browserDownloadsTrimLocked drops old saved rows once the list is over its
// bound. The files remain in Downloads; only the panel forgets them.
func (d *browserDownloadsQueue) trimLocked() {
	done := 0
	for _, it := range d.items {
		if it.Status == downloadDone {
			done++
		}
	}
	drop := done - browserDownloadLimit
	if drop <= 0 {
		return
	}
	kept := d.items[:0]
	for _, it := range d.items {
		if it.Status == downloadDone && drop > 0 {
			drop--
			continue
		}
		kept = append(kept, it)
	}
	d.items = kept
}

// browserRunDownload performs one save and moves its row through active,
// failed or saved. A row removed while it ran (cancel) has no row left, so the
// goroutine simply drops the result.
func browserRunDownload(id int64, ctx context.Context, address, dir, hint string) {
	client := browserHTTPClient(0)
	client.CheckRedirect = textBrowserRedirectPolicy
	path, name, err := browserFetchToDir(ctx, client, address, dir, hint, func(p browserDownloadProgress) {
		browserDownloadApplyProgress(id, p)
	})

	browserDownloads.mu.Lock()
	it, _ := browserDownloadFindLocked(id)
	if it == nil {
		browserDownloads.mu.Unlock()
		return
	}
	it.cancel = nil
	it.Speed = 0
	if err != nil {
		it.Status = downloadFailed
		it.finished = time.Now()
		if ctx.Err() != nil {
			it.Err = "Download canceled"
		} else {
			it.Err = browserDownloadProblem(err)
		}
		problem := it.Err
		browserDownloads.mu.Unlock()
		if ctx.Err() == nil {
			showToast(problem, ToastError())
		}
		return
	}
	it.Status = downloadDone
	it.Path = path
	if name != "" {
		it.Title = name
	}
	it.finished = time.Now()
	if info, statErr := os.Stat(path); statErr == nil {
		it.Done = info.Size()
		if it.Total <= 0 {
			it.Total = it.Done
		}
	}
	browserDownloads.trimLocked()
	browserDownloads.mu.Unlock()

	showToast("Saved "+filepath.Base(path)+" in Downloads", ToastSuccess())
}

// browserDownloadApplyProgress copies one callback's numbers into the row. It
// ignores a row that was removed or finished while the callback was in flight.
func browserDownloadApplyProgress(id int64, p browserDownloadProgress) {
	browserDownloads.mu.Lock()
	defer browserDownloads.mu.Unlock()
	it, _ := browserDownloadFindLocked(id)
	if it == nil || it.Status != downloadActive {
		return
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		it.Title = name
	}
	if p.Total > 0 {
		it.Total = p.Total
	}
	if p.Done >= 0 {
		it.Done = p.Done
	}
	if p.Speed > 0 {
		it.Speed = p.Speed
	}
}

// browserDownloadCancel stops a running save and removes its row. The partial
// file is deleted by browserFetchToDir's deferred cleanup.
func browserDownloadCancel(id int64) bool {
	browserDownloads.mu.Lock()
	it, idx := browserDownloadFindLocked(id)
	if it == nil || (it.Status != downloadActive && it.Status != downloadQueued) {
		browserDownloads.mu.Unlock()
		return false
	}
	cancel := it.cancel
	browserDownloads.items = append(browserDownloads.items[:idx], browserDownloads.items[idx+1:]...)
	browserDownloads.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

// browserDownloadForget removes a finished or failed browser row. The file is
// the user's own Downloads file, so forgetting the row never deletes it.
func browserDownloadForget(id int64) bool {
	browserDownloads.mu.Lock()
	defer browserDownloads.mu.Unlock()
	it, idx := browserDownloadFindLocked(id)
	if it == nil || it.Status == downloadActive || it.Status == downloadQueued {
		return false
	}
	browserDownloads.items = append(browserDownloads.items[:idx], browserDownloads.items[idx+1:]...)
	return true
}

// browserDownloadRetry starts a failed browser row again with a fresh context.
func browserDownloadRetry(id int64) bool {
	browserDownloads.mu.Lock()
	it, _ := browserDownloadFindLocked(id)
	if it == nil || it.Status != downloadFailed || browserDownloadBusyLocked(it.URL) {
		browserDownloads.mu.Unlock()
		return false
	}
	dir := strings.TrimSpace(it.dir)
	if dir == "" {
		dir = chatSaveDir()
	}
	ctx, cancel := context.WithCancel(context.Background())
	it.Status = downloadActive
	it.Done = 0
	it.Total = 0
	it.Speed = 0
	it.Err = ""
	it.Path = ""
	it.finished = time.Time{}
	it.added = time.Now()
	it.cancel = cancel
	address, hint := it.URL, it.hint
	title := it.Title
	browserDownloads.mu.Unlock()

	showToast("Retrying "+title, ToastInfo())
	go browserRunDownload(id, ctx, address, dir, hint)
	return true
}

// browserFetchToDir downloads one address into dir and returns the path it
// landed at and the file name the server gave it. The body goes to a .part
// file first, so a connection that drops halfway leaves no half file under the
// real name.
func browserFetchToDir(ctx context.Context, client *http.Client, address, dir, nameHint string, onProgress func(browserDownloadProgress)) (string, string, error) {
	if client == nil {
		client = browserHTTPClient(0)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("cannot open Downloads: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", "", fmt.Errorf("that address can't be opened: %w", err)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return "", "", fmt.Errorf("only http and https addresses can be downloaded")
	}
	browserChromeNavigate(req.Header)
	// A save is not a page load: let the server answer with the file itself
	// rather than negotiating for HTML.
	req.Header.Set("Accept", "*/*")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	touch, stop := chatStallWatch(cancel, browserDownloadStall)
	defer stop()
	resp, err := client.Do(req)
	if err != nil {
		return "", "", browserDownloadNetError(err, ctx)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("the server answered %s", resp.Status)
	}
	if resp.ContentLength > browserDownloadMax {
		return "", "", fmt.Errorf("that file is %s; downloads stop at %s",
			formatBytes(uint64(resp.ContentLength)), formatBytes(uint64(browserDownloadMax)))
	}
	if err := browserDownloadCheckSpace(dir, resp.ContentLength); err != nil {
		return "", "", err
	}

	base, ext := browserDownloadName(resp.Request.URL.String(), resp.Header, nameHint)
	name := base + ext
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	tmp, err := os.CreateTemp(dir, ".jukahub-*.part")
	if err != nil {
		return "", "", fmt.Errorf("cannot write to Downloads: %w", err)
	}
	tmpName := tmp.Name()
	kept := false
	defer func() {
		tmp.Close()
		if !kept {
			os.Remove(tmpName)
		}
	}()

	// Progress is coalesced here rather than in the panel: the panel may be
	// closed while the save runs, and every read would otherwise take the
	// registry lock.
	lastAt, lastBytes := time.Now(), int64(0)
	emit := func(done int64, force bool) {
		now := time.Now()
		if !force && now.Sub(lastAt) < browserDownloadProgressInterval {
			return
		}
		speed := 0.0
		if elapsed := now.Sub(lastAt).Seconds(); elapsed > 0 && done > lastBytes {
			speed = float64(done-lastBytes) / elapsed
		}
		lastAt, lastBytes = now, done
		if onProgress != nil {
			onProgress(browserDownloadProgress{Done: done, Total: total, Speed: speed, Name: name})
		}
	}
	emit(0, true)

	reader := &chatProgressReader{
		r: io.LimitReader(resp.Body, browserDownloadMax+1),
		on: func(n int64) {
			touch()
			emit(n, false)
		},
	}
	written, err := io.Copy(tmp, reader)
	if err != nil {
		return "", "", browserDownloadNetError(err, ctx)
	}
	if written > browserDownloadMax {
		return "", "", fmt.Errorf("that file is larger than the %s download limit", formatBytes(uint64(browserDownloadMax)))
	}
	if err := tmp.Close(); err != nil {
		return "", "", fmt.Errorf("cannot finish the file in Downloads: %w", err)
	}
	emit(written, true)
	// A cancel that arrived while the last bytes were landing must not move
	// the partial into the user's Downloads under the real name.
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	path, err := placeDownloadedFile(tmpName, dir, base, ext)
	if err != nil {
		return "", "", err
	}
	if err := ctx.Err(); err != nil {
		os.Remove(path)
		return "", "", err
	}
	kept = true
	return path, name, nil
}

// browserDownloadCheckSpace refuses a save the disk cannot hold with the
// minimum breathing room downloads keep. A platform that cannot report free
// space lets the write itself decide.
func browserDownloadCheckSpace(dir string, total int64) error {
	free, err := freeBytes(dir)
	if err != nil || free <= 0 {
		return nil
	}
	if free < downloadsMinFreeBytes {
		return fmt.Errorf("not enough space: %s free, a download needs at least %s",
			formatBytes(uint64(free)), formatBytes(downloadsMinFreeBytes))
	}
	if total > 0 && total > free-downloadsMinFreeBytes {
		return fmt.Errorf("not enough space: this file is %s and only %s is free",
			formatBytes(uint64(total)), formatBytes(uint64(free)))
	}
	return nil
}

// browserDownloadName keeps the server's Content-Disposition name, then the
// address's last path segment, then the page title. The result is a base name
// and an extension, safe to join to Downloads; the extension falls back to the
// response's type when the name did not carry one.
func browserDownloadName(rawURL string, header http.Header, nameHint string) (string, string) {
	contentType := ""
	filename := ""
	if header != nil {
		contentType = header.Get("Content-Type")
		filename = browserDownloadFilename(header.Get("Content-Disposition"))
	}
	if filename == "" {
		if u, err := url.Parse(rawURL); err == nil {
			if base := path.Base(u.Path); base != "" && base != "." && base != "/" {
				filename = base
			}
		}
	}
	var base, ext string
	if filename != "" {
		if decoded, err := url.PathUnescape(filename); err == nil {
			filename = decoded
		}
		base, ext = chatSaveName(filename)
	} else {
		base = safeDownloadName(nameHint)
	}
	if base == "" {
		base = "download"
	}
	if ext == "" {
		ext = browserDownloadExt(contentType)
	}
	return base, ext
}

// browserDownloadFilename reads the filename parameter of Content-Disposition,
// including the RFC 5987 filename* form mime.ParseMediaType decodes.
func browserDownloadFilename(disposition string) string {
	if strings.TrimSpace(disposition) == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(params["filename"])
}

// browserDownloadExt guesses the extension a server's content type implies
// when neither its header nor its address named one.
func browserDownloadExt(contentType string) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch mediaType {
	case "text/html", "application/xhtml+xml":
		return ".html"
	case "text/plain":
		return ".txt"
	case "application/pdf":
		return ".pdf"
	case "application/zip":
		return ".zip"
	case "application/json":
		return ".json"
	case "application/xml", "text/xml":
		return ".xml"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "audio/ogg":
		return ".ogg"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "application/octet-stream":
		return ".bin"
	}
	if mediaType != "" {
		if exts, err := mime.ExtensionsByType(mediaType); err == nil && len(exts) > 0 {
			return strings.ToLower(exts[0])
		}
	}
	switch {
	case strings.HasPrefix(mediaType, "text/"):
		return ".txt"
	case strings.HasPrefix(mediaType, "image/"):
		return ".img"
	case strings.HasPrefix(mediaType, "audio/"):
		return ".audio"
	case strings.HasPrefix(mediaType, "video/"):
		return ".video"
	}
	return ".bin"
}

// browserDownloadHost names the source under a row's title.
func browserDownloadHost(address string) string {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// browserDownloadProblem turns a save error into one short line for a row or
// toast.
func browserDownloadProblem(err error) string {
	if err == nil {
		return ""
	}
	line := strings.TrimSpace(err.Error())
	if line == "" {
		return "Couldn't download that file"
	}
	if runes := []rune(line); len(runes) > 140 {
		line = string(runes[:140]) + "…"
	}
	if strings.HasPrefix(line, "not enough space") || strings.HasPrefix(line, "that file") ||
		strings.HasPrefix(line, "the server answered") {
		return line
	}
	return "Couldn't download: " + line
}

func browserDownloadNetError(err error, ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("the download stopped: check the connection")
	}
	return fmt.Errorf("the download failed: %w", err)
}
