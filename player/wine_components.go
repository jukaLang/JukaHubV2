package main

// wine_components.go — optional, user-listed WINE components, downloaded only
// when the user asks and only from approved HTTPS hosts.
//
// What makes this safe:
//
//   - There is no built-in download list. The user supplies
//     <data>/wine/components.json; nothing is fetched on a launch or at boot.
//   - Every entry must name an official host on the existing download
//     allowlist, a file name with no path, and a SHA-256. A missing or bad
//     digest is a hard error, never a "good enough" install.
//   - The file is only cached on the card. JukaHub never executes it: an
//     entry may name an environment variable (for example Wine's Mono or
//     Gecko cache variables) that points Wine at the verified file.
//
// This is deliberately not a marketplace. No third-party binary ships with
// JukaHub, nothing is downloaded quietly, and no component can replace a
// file Wine already uses.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	wineComponentsDirRel  = "wine"
	wineComponentsFile    = "components.json"
	wineComponentsVersion = 1
	wineComponentMaxBytes = 512 << 20
)

var wineComponentFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
var wineComponentHashRe = regexp.MustCompile(`(?i)^[0-9a-f]{64}$`)

// wineComponentsDir is replaceable in tests.
var wineComponentsDir = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, wineComponentsDirRel)
}

type wineComponent struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	File   string `json:"file"`
	// EnvDir, when set, names a variable set to the folder holding the verified
	// file. Wine's Mono and Gecko cache variables take a folder, not a file.
	EnvDir string `json:"envDir,omitempty"`
	// Local, when set, names a file the user already put in <data>/wine. It
	// is checked against SHA-256 and copied into the cache; no network is used.
	Local string `json:"local,omitempty"`
	// Detail says what the component is for, for the card. Problem is set by
	// the manifest check and never read from the file.
	Detail  string `json:"detail,omitempty"`
	Problem string `json:"-"`
}

type wineComponentsDoc struct {
	Version    int             `json:"version"`
	Components []wineComponent `json:"components"`
}

// wineComponentsValidateURL and wineComponentDownload are seams for tests:
// production uses the same HTTPS + approved-host check and the same resuming,
// hash-verifying downloader as every other download in the app.
var (
	wineComponentsValidateURL = validatePatchURL
	wineComponentDownload     = func(ctx context.Context, client *http.Client, raw, dest, sha string, max int64,
		report func(got, total int64)) error {
		return downloadToFileProgress(ctx, client, raw, dest, sha, max, report)
	}
)

// wineComponentsProgress is what the card shows while a download runs. The
// callback comes off the download goroutine; the card reads it under the lock.
var wineComponentsProgress struct {
	mu         sync.Mutex
	active     bool
	name       string
	got, total int64
}

// wineComponentsConfigPath is the user's manifest.
func wineComponentsConfigPath() string {
	dir := wineComponentsDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, wineComponentsFile)
}

// wineLoadComponents reads and checks the manifest. A bad entry is reported
// with its own name rather than making the whole card unreadable.
func wineLoadComponents() ([]wineComponent, error) {
	path := wineComponentsConfigPath()
	if path == "" {
		return nil, errors.New("no data folder")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("the component list is too large")
	}
	var doc wineComponentsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("components.json is not valid JSON: %w", err)
	}
	var out []wineComponent
	for _, c := range doc.Components {
		c.Name = strings.TrimSpace(c.Name)
		c.URL = strings.TrimSpace(c.URL)
		c.SHA256 = strings.TrimSpace(c.SHA256)
		c.File = strings.TrimSpace(c.File)
		c.EnvDir = strings.TrimSpace(c.EnvDir)
		c.Local = strings.TrimSpace(c.Local)
		switch {
		case c.Name == "":
			c.Name = c.File
		case len([]rune(c.Name)) > 60:
			c.Name = string([]rune(c.Name)[:60])
		}
		switch {
		case !wineComponentFileRe.MatchString(c.File) || c.File == "." || c.File == "..":
			c.Problem = "bad file name"
		case !wineComponentHashRe.MatchString(c.SHA256):
			c.Problem = "a SHA-256 is required"
		case c.Local != "" && !wineComponentFileRe.MatchString(c.Local):
			c.Problem = "bad local file name"
		case c.Local == "":
			if err := wineComponentsValidateURL(c.URL); err != nil {
				c.Problem = firstLine(err.Error())
			}
		}
		if c.Problem == "" && c.EnvDir != "" && !wineComponentFileRe.MatchString(c.EnvDir) {
			c.Problem = "bad environment variable name"
		}
		out = append(out, c)
	}
	return out, nil
}

// wineComponentPath is where a verified component is cached.
func wineComponentPath(c wineComponent) string {
	dir := wineComponentsDir()
	if dir == "" || !wineComponentFileRe.MatchString(c.File) {
		return ""
	}
	return filepath.Join(dir, c.File)
}

// wineComponentStamp is written after a download or an explicit Verify. It
// lets a launch notice a file that changed or was truncated without re-hashing
// a large component every time a program starts.
type wineComponentStamp struct {
	Size   int64  `json:"size"`
	MTimes int64  `json:"mtime"`
	SHA256 string `json:"sha256"`
}

// wineComponentStampPath is the verified stamp beside a cached component.
func wineComponentStampPath(c wineComponent) string {
	dir := wineComponentsDir()
	if dir == "" || !wineComponentFileRe.MatchString(c.File) {
		return ""
	}
	return filepath.Join(dir, ".stamps", c.File+".json")
}

// wineComponentStampOK compares a cached file against its stamp. A file with
// no stamp was put there before stamps existed: it stays usable, and Verify
// will give it one. A stamped file that changed is not trusted.
func wineComponentStampOK(c wineComponent) bool {
	stampPath := wineComponentStampPath(c)
	if stampPath == "" {
		return false
	}
	var stamp wineComponentStamp
	if !wineReadJSON(stampPath, &stamp) {
		// No stamp yet (an older install): leave it alone rather than block
		// a program the user was already running.
		if _, err := os.Stat(stampPath); err != nil {
			return true
		}
		return false
	}
	path := wineComponentPath(c)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != stamp.Size ||
		info.ModTime().UnixNano() != stamp.MTimes || !strings.EqualFold(stamp.SHA256, c.SHA256) {
		return false
	}
	return true
}

// wineComponentInstalled reports whether the cached file exists and, when it
// has a stamp, still matches it. Hashing is done by Verify, not on launch.
func wineComponentInstalled(c wineComponent) bool {
	path := wineComponentPath(c)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	return wineComponentStampOK(c)
}

// wineComponentHasStamp reports whether a cached component has a verified
// stamp. Older installs may not: the card offers to check them with X.
func wineComponentHasStamp(c wineComponent) bool {
	path := wineComponentStampPath(c)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// wineComponentVerify hashes a cached component, writes its stamp on a match
// and removes a bad file so A can download it again. It is the card's X
// action, never part of a launch.
func wineComponentVerify(c wineComponent) error {
	path := wineComponentPath(c)
	stampPath := wineComponentStampPath(c)
	if path == "" || stampPath == "" {
		return errors.New("no data folder for WINE components")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("that component is not installed")
	}
	sum, err := SHA256File(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, c.SHA256) {
		// A component whose cache file is the user's own supplied file is not
		// ours to delete: they can fix the manifest and verify again.
		if c.Local != "" && filepath.Clean(path) == filepath.Clean(filepath.Join(wineComponentsDir(), c.Local)) {
			return errors.New("the local file no longer matches its SHA-256; fix it in <data>/wine")
		}
		_ = os.Remove(path)
		if stamp := wineComponentStampPath(c); stamp != "" {
			_ = os.Remove(stamp)
		}
		return errors.New("the cached file no longer matches its SHA-256; it was removed")
	}
	if err := os.MkdirAll(filepath.Dir(stampPath), 0o755); err != nil {
		return err
	}
	return wineWriteJSON(stampPath, wineComponentStamp{
		Size: info.Size(), MTimes: info.ModTime().UnixNano(), SHA256: strings.ToLower(c.SHA256),
	})
}

// wineComponentWriteStamp records a verified download.
func wineComponentWriteStamp(c wineComponent) {
	path, stampPath := wineComponentPath(c), wineComponentStampPath(c)
	if path == "" || stampPath == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || os.MkdirAll(filepath.Dir(stampPath), 0o755) != nil {
		return
	}
	_ = wineWriteJSON(stampPath, wineComponentStamp{
		Size: info.Size(), MTimes: info.ModTime().UnixNano(), SHA256: strings.ToLower(c.SHA256),
	})
}

// wineComponentsMissing is the components the user listed but has not cached.
func wineComponentsMissing() []wineComponent {
	list, err := wineLoadComponents()
	if err != nil {
		return nil
	}
	var out []wineComponent
	for _, c := range list {
		if c.Problem == "" && !wineComponentInstalled(c) {
			out = append(out, c)
		}
	}
	return out
}

// wineComponentFetch downloads one component and verifies its SHA-256. A bad
// Tests call this seam directly; it is kept for their table cases.
// digest leaves no cached file: downloadToFile removes the partial on failure.
func wineComponentFetch(ctx context.Context, c wineComponent) error {
	return wineComponentFetchWithReport(ctx, c, nil)
}

// wineComponentFetchWithReport is wineComponentFetch with a caller's progress
// report, used by the card to drive the shared install bar.
func wineComponentFetchWithReport(ctx context.Context, c wineComponent, report func(got, total int64)) error {
	if !wineComponentFileRe.MatchString(c.File) || !wineComponentHashRe.MatchString(c.SHA256) {
		return errors.New("that component has no safe file name and SHA-256")
	}
	dest := wineComponentPath(c)
	if dest == "" {
		return errors.New("no data folder for WINE components")
	}
	if wineComponentInstalled(c) {
		return errors.New("that component is already installed")
	}
	if c.Local != "" {
		if !wineComponentFileRe.MatchString(c.Local) {
			return errors.New("bad local file name")
		}
		return wineComponentTakeLocal(c, dest)
	}
	if err := wineComponentsValidateURL(c.URL); err != nil {
		return err
	}
	if _, err := url.Parse(c.URL); err != nil {
		return fmt.Errorf("that component URL is not valid: %w", err)
	}
	client := &http.Client{}
	progress := func(got, total int64) {
		wineComponentsProgress.mu.Lock()
		wineComponentsProgress.got, wineComponentsProgress.total = got, total
		wineComponentsProgress.mu.Unlock()
		if report != nil {
			report(got, total)
		}
	}
	if err := wineComponentDownload(ctx, client, c.URL, dest, strings.ToLower(c.SHA256), wineComponentMaxBytes, progress); err != nil {
		return err
	}
	sum, err := SHA256File(dest)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, c.SHA256) {
		_ = os.Remove(dest)
		return errors.New("the downloaded file did not match its SHA-256")
	}
	wineComponentWriteStamp(c)
	return nil
}

// wineComponentTakeLocal copies a verified file the user already put in
// <data>/wine into the cache. No network is used, and the SHA-256 is checked
// before anything is copied.
func wineComponentTakeLocal(c wineComponent, dest string) error {
	dir := wineComponentsDir()
	if dir == "" {
		return errors.New("no data folder for WINE components")
	}
	src := filepath.Join(dir, c.Local)
	info, err := os.Stat(src)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("the local component file is missing")
	}
	if info.Size() == 0 || info.Size() > wineComponentMaxBytes {
		return errors.New("the local component file is the wrong size")
	}
	sum, err := SHA256File(src)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, c.SHA256) {
		return errors.New("the local file did not match its SHA-256")
	}
	if filepath.Clean(src) != filepath.Clean(dest) {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := AtomicWrite(dest, data, 0o644); err != nil {
			return err
		}
	}
	wineComponentWriteStamp(c)
	return nil
}

// wineComponentsEnv names the variables that point Wine at cached components.
// It only reports files that are there; a missing download is never advertised
// to a launch.
func wineComponentsEnv() map[string]string {
	out := map[string]string{}
	list, err := wineLoadComponents()
	if err != nil {
		return out
	}
	for _, c := range list {
		if c.EnvDir == "" || !wineComponentInstalled(c) {
			continue
		}
		if !hasEnvKey(out, c.EnvDir) {
			out[c.EnvDir] = filepath.Dir(wineComponentPath(c))
		}
	}
	return out
}

var wineComponentsSummaryCache struct {
	mu   sync.Mutex
	at   time.Time
	text string
}

// wineComponentsSummary is a line for the Windows Apps setup page. It reads
// the small manifest at most once every few seconds, not every frame.
func wineComponentsSummary() string {
	wineComponentsSummaryCache.mu.Lock()
	defer wineComponentsSummaryCache.mu.Unlock()
	if time.Since(wineComponentsSummaryCache.at) < 3*time.Second {
		return wineComponentsSummaryCache.text
	}
	wineComponentsSummaryCache.at, wineComponentsSummaryCache.text = time.Now(), wineComponentsSummaryNow()
	return wineComponentsSummaryCache.text
}

// wineComponentsSummaryNow builds the line from the manifest.
func wineComponentsSummaryNow() string {
	list, err := wineLoadComponents()
	if err != nil || len(list) == 0 {
		return ""
	}
	missing := 0
	for _, c := range list {
		if c.Problem == "" && !wineComponentInstalled(c) {
			missing++
		}
	}
	if missing == 0 {
		return itoa(len(list)) + " optional WINE component(s) installed"
	}
	return pluralizeCount(missing, "optional WINE component", "optional WINE components") +
		" not installed — Community → WINE components"
}

// wineComponentsLines is the card's text. It says where the list comes from
// and that nothing is downloaded without a press.
func wineComponentsLines(cursor int) []string {
	lines := []string{
		"WINE components are optional files you list yourself.",
		"Put components.json in <data>/wine with name, https URL, SHA-256 and file.",
		"A installs, X verifies, Y writes an example list if you have none.",
		"Nothing is downloaded at boot or on launch; files are never run by JukaHub.",
	}
	list, err := wineLoadComponents()
	if err != nil {
		return append(lines, err.Error())
	}
	// One pass over the manifest and the cache, then the lines below are pure
	// string work: a card redraws every frame and a handheld card is slow to
	// stat and read twice per component.
	type componentView struct {
		item             wineComponent
		present, stamped bool
		size             int64
		source           string
	}
	views := make([]componentView, 0, len(list))
	installed, cached := 0, int64(0)
	for _, c := range list {
		view := componentView{item: c}
		if c.Local != "" {
			view.source = " · local: " + c.Local
		} else if u, err := url.Parse(c.URL); err == nil && u.Host != "" {
			view.source = " · from " + u.Hostname()
		}
		if c.Problem == "" {
			view.present = wineComponentInstalled(c)
			if view.present {
				view.stamped = wineComponentHasStamp(c)
				if info, statErr := os.Stat(wineComponentPath(c)); statErr == nil {
					view.size = info.Size()
					installed++
					cached += info.Size()
				}
			}
		}
		views = append(views, view)
	}
	if len(list) > 0 {
		lines = append(lines, "Files downloaded: "+itoa(installed)+" of "+itoa(len(list))+
			" · "+formatSize(cached)+" on the card")
	}
	lines = append(lines, "Each file is capped at "+formatSize(wineComponentMaxBytes)+". Del/L1 removes one.")
	if active, name, got, total := wineComponentsProgressSnapshot(); active {
		line := "Downloading " + name + ": " + formatSize(got)
		if total > 0 {
			pct := float64(got) * 100 / float64(total)
			if pct > 100 {
				pct = 100
			}
			filled := int(pct / 100 * 12)
			if filled > 12 {
				filled = 12
			}
			bar := strings.Repeat("=", filled) + strings.Repeat("-", 12-filled)
			line += " / " + formatSize(total) + " · " + fmt.Sprintf("%.0f%%", pct) + " " + bar
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")
	if len(list) == 0 {
		return append(lines, "No components listed yet.")
	}
	for i, view := range views {
		c := view.item
		mark := " "
		if i == cursor {
			mark = "›"
		}
		line := mark + " " + c.Name
		if c.Problem != "" {
			line += " · " + c.Problem
		} else if view.present {
			if view.stamped {
				line += " · verified"
			} else {
				line += " · installed, X to verify"
			}
			if view.size > 0 {
				line += " (" + formatSize(view.size) + ")"
			}
		}
		line += view.source
		lines = append(lines, line)
		if i == cursor && c.Detail != "" {
			lines = append(lines, "    "+c.Detail)
		}
	}
	return lines
}

// wineComponentsMove and wineComponentsActivate drive the card.
var wineComponentsUI struct{ cursor int }

// wineComponentsProgressSnapshot is the card's view of an active download.
func wineComponentsProgressSnapshot() (active bool, name string, got, total int64) {
	wineComponentsProgress.mu.Lock()
	defer wineComponentsProgress.mu.Unlock()
	return wineComponentsProgress.active, wineComponentsProgress.name,
		wineComponentsProgress.got, wineComponentsProgress.total
}

func wineComponentsMove(delta int) {
	list, err := wineLoadComponents()
	if err != nil || len(list) == 0 {
		return
	}
	wineComponentsUI.cursor = (wineComponentsUI.cursor + delta + len(list)) % len(list)
	PlayNavSound()
}

const wineComponentsTemplate = `{
  "version": 1,
  "components": [
    {
      "name": "Example — fill this in",
      "url": "https://dl.winehq.org/path/to/component.msi",
      "sha256": "",
      "file": "example.msi",
      "envDir": "WINE_MONO_CACHE_DIR",
      "detail": "Replace this entry with a real file and the digest published with it",
      "local": ""
    }
  ]
}
`

// wineComponentsCreateTemplate writes an example manifest only when there is
// none. Its empty digest means nothing can be downloaded until the user fills
// in the real details.
func wineComponentsCreateTemplate() error {
	path := wineComponentsConfigPath()
	if path == "" {
		return errors.New("no data folder for WINE components")
	}
	if _, err := os.Stat(path); err == nil {
		return errors.New("components.json already exists")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return AtomicWrite(path, []byte(wineComponentsTemplate), 0o644)
}

// wineComponentRemove deletes a cached component and its stamp. It never
// touches a file the user supplied in the data folder (the "local" source).
func wineComponentRemove(c wineComponent) error {
	path, stamp := wineComponentPath(c), wineComponentStampPath(c)
	if path == "" {
		return errors.New("no data folder for WINE components")
	}
	if c.Local != "" && filepath.Clean(path) == filepath.Clean(filepath.Join(wineComponentsDir(), c.Local)) {
		return errors.New("this file belongs to you, not to the cache")
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("that component is not installed")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if stamp != "" {
		_ = os.Remove(stamp)
	}
	return nil
}

// wineComponentsCachedBytes is how much space the cached components use, so
// Tests call this seam directly; it is kept for their table cases.
// the card can say what removing one would free on a small card.
func wineComponentsCachedBytes() int64 {
	list, err := wineLoadComponents()
	if err != nil {
		return 0
	}
	var total int64
	for _, c := range list {
		if !wineComponentInstalled(c) {
			continue
		}
		if info, err := os.Stat(wineComponentPath(c)); err == nil {
			total += info.Size()
		}
	}
	return total
}

func wineComponentsRemove() {
	list, err := wineLoadComponents()
	if err != nil || wineComponentsUI.cursor < 0 || wineComponentsUI.cursor >= len(list) {
		return
	}
	c := list[wineComponentsUI.cursor]
	if err := wineComponentRemove(c); err != nil {
		ShowToast("Could not remove "+c.Name+": "+firstLine(err.Error()), ToastKindError)
		return
	}
	ShowToast(c.Name+" removed from the cache", ToastKindInfo)
}

func wineComponentsVerify() {
	list, err := wineLoadComponents()
	if err != nil || wineComponentsUI.cursor < 0 || wineComponentsUI.cursor >= len(list) {
		return
	}
	c := list[wineComponentsUI.cursor]
	if c.Problem != "" {
		ShowToast("Fix this component entry first: "+c.Problem, ToastKindError)
		return
	}
	if err := wineComponentVerify(c); err != nil {
		ShowToast("Verify failed: "+firstLine(err.Error()), ToastKindError)
		return
	}
	ShowToast(c.Name+" verified", ToastKindSuccess)
}

func wineComponentsActivate() {
	list, err := wineLoadComponents()
	if err != nil || wineComponentsUI.cursor < 0 || wineComponentsUI.cursor >= len(list) {
		return
	}
	c := list[wineComponentsUI.cursor]
	if c.Problem != "" {
		ShowToast("Fix this component entry in components.json: "+c.Problem, ToastKindError)
		return
	}
	if wineComponentInstalled(c) {
		ShowToast(c.Name+" is already installed", ToastKindInfo)
		return
	}
	wineComponentsProgress.mu.Lock()
	wineComponentsProgress.active, wineComponentsProgress.name = true, c.Name
	wineComponentsProgress.got, wineComponentsProgress.total = 0, -1
	wineComponentsProgress.mu.Unlock()
	gen, _ := installBarBeginRunLabelled(c.Name, "WINE component")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := wineComponentFetchWithReport(ctx, c, func(got, total int64) {
			installBarUpdateRun(gen, installPhaseDownload, got, total)
		})
		tbxPost(func() {
			wineComponentsProgress.mu.Lock()
			wineComponentsProgress.active = false
			wineComponentsProgress.mu.Unlock()
			if err != nil {
				installBarFailRun(gen, firstLine(err.Error()))
				ShowToast("Could not install "+c.Name+": "+firstLine(err.Error()), ToastKindError)
				return
			}
			installBarDoneRunDetail(gen, "Verified and cached — no restart needed")
			ShowToast(c.Name+" installed and verified", ToastKindSuccess)
		})
	}()
}
