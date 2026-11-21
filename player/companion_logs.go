package main

// companion_logs.go — "Download logs" on the Phone Companion page.
//
// GET /api/logs.zip (behind the PIN; the link carries it as "p", like the
// Files card's downloads) answers a zip of JukaHub's recent logs, for a bug
// report or a forum post:
//   - live-log.txt, the in-app log as it is now (the Logs screen's lines);
//   - the log files in the app's log folder (crashLogDir: errors.txt,
//     logs.txt, crash reports, autostart.log and every other *.log there —
//     never anything else from the app folder, which holds the settings);
//   - the last games' logs (gamediag: RetroArch, ports) and the game
//     profiles' launch logs.
// Newest first until companionLogsMaxBytes (uncompressed) is used up; a
// long file gives its newest part (at most companionLogsFileMax) and a file
// with NUL bytes in its start is taken as binary and skipped.
//
// Every file goes through redactLogPersonal (logredact.go): credentials, the
// Wi-Fi networks the handheld knows, the user's account and display names,
// e-mail, Bluetooth and Wi-Fi hardware addresses and LAN addresses are
// masked. The account names are read from the settings on the main thread
// (tbxPost); when the main loop does not answer in time the patterns alone
// run, and the log says so. One zip is made at a time.

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// companionLogsMaxBytes bounds the uncompressed logs in one zip.
	companionLogsMaxBytes = 20 << 20
	// companionLogsFileMax bounds one file's share (its newest part).
	companionLogsFileMax = 4 << 20
	// companionLogsMaxFiles bounds how many files one zip lists.
	companionLogsMaxFiles = 200
	// companionLogsBlock is how much text is masked at a time.
	companionLogsBlock = 256 << 10
	// companionLogsNamesWait is how long the main loop has to hand over the
	// account names.
	companionLogsNamesWait = 3 * time.Second
)

// companionLogsBusy lets one zip be made at a time.
var companionLogsBusy sync.Mutex

// companionLogFile is one file the zip may take.
type companionLogFile struct {
	path string
	name string // its name in the zip
	mod  time.Time
	size int64
}

// registerLogRoutes adds Download logs (companion.go's handler).
func (s *companionServer) registerLogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/logs.zip", s.guard(s.handleLogsZip))
}

// companionLogAppName reports a log file in the app's log folder by name.
// That folder is the app folder on the handheld, so this is an allowlist.
func companionLogAppName(name string) bool {
	lower := strings.ToLower(name)
	switch {
	case lower == "errors.txt" || lower == "logs.txt" || strings.HasPrefix(lower, "errors.txt.") || strings.HasPrefix(lower, "logs.txt."):
		return true
	case strings.HasPrefix(lower, "crash-") && strings.HasSuffix(lower, ".txt"):
		return true
	case strings.HasSuffix(lower, ".log") || strings.HasSuffix(lower, ".log.1") || strings.HasSuffix(lower, ".log.old"):
		return true
	}
	return false
}

// companionLogGameName reports a log in the game log folders.
func companionLogGameName(name string) bool {
	lower := strings.ToLower(name)
	return companionLogAppName(name) || strings.HasSuffix(lower, ".txt")
}

// companionLogCollect lists dir's log files (and, depth > 0, its folders'),
// each named prefix/<path inside dir> in the zip.
func companionLogCollect(out []companionLogFile, dir, prefix string, depth int, keep func(string) bool) []companionLogFile {
	if strings.TrimSpace(dir) == "" {
		return out
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if len(out) >= companionLogsMaxFiles*2 {
			break
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			if depth > 0 {
				out = companionLogCollect(out, full, prefix+"/"+name, depth-1, keep)
			}
			continue
		}
		if !e.Type().IsRegular() || !keep(name) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() == 0 {
			continue
		}
		out = append(out, companionLogFile{path: full, name: prefix + "/" + name, mod: info.ModTime(), size: info.Size()})
	}
	return out
}

// companionLogFiles lists the files the zip may take, newest first, each
// file once.
func companionLogFiles() []companionLogFile {
	var out []companionLogFile
	if dir, err := crashLogDir(); err == nil {
		out = companionLogCollect(out, dir, "app", 0, companionLogAppName)
	}
	if dir, err := P().ExecutableDir(); err == nil {
		// Another folder than the logs one on a computer: its own zip folder,
		// so two errors.txt never share a name (the same folder is deduped below).
		out = companionLogCollect(out, dir, "app-folder", 0, companionLogAppName)
	}
	out = companionLogCollect(out, gameDiagLogDir(), "game-logs", 2, companionLogGameName)
	out = companionLogCollect(out, gameProfilesLogsDir(), "game-profiles", 0, companionLogAppName)
	seen := map[string]bool{}
	uniq := out[:0]
	for _, f := range out {
		key := filepath.Clean(f.path)
		if real, err := filepath.EvalSymlinks(key); err == nil {
			key = real
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		uniq = append(uniq, f)
	}
	sort.SliceStable(uniq, func(i, j int) bool { return uniq[i].mod.After(uniq[j].mod) })
	if len(uniq) > companionLogsMaxFiles {
		uniq = uniq[:companionLogsMaxFiles]
	}
	return uniq
}

// companionLogReadTail reads the newest limit bytes of path, from a line
// start, and reports false for a binary file.
func companionLogReadTail(path string, limit int64) ([]byte, bool) {
	// Never through a link: the listing skipped them, and a file swapped for
	// one since is not the file it listed.
	link, err := os.Lstat(path)
	if err != nil || !link.Mode().IsRegular() {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(link, info) {
		return nil, false
	}
	size := info.Size()
	cut := size > limit
	if cut {
		if _, err := f.Seek(size-limit, io.SeekStart); err != nil {
			return nil, false
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil && len(data) == 0 {
		return nil, false
	}
	head := data
	if len(head) > 8<<10 {
		head = head[:8<<10]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, false
	}
	if cut {
		if i := bytes.IndexByte(data, '\n'); i >= 0 && i+1 < len(data) {
			data = data[i+1:]
		}
	}
	return data, true
}

// companionLogWriteMasked writes data to dst through redactLogPersonal, a
// block of whole lines at a time.
func companionLogWriteMasked(dst io.Writer, data []byte, names logPersonalNames) error {
	for len(data) > 0 {
		n := len(data)
		if n > companionLogsBlock {
			n = companionLogsBlock
			if i := bytes.LastIndexByte(data[:n], '\n'); i > 0 {
				n = i + 1
			}
		}
		if _, err := io.WriteString(dst, redactLogPersonal(string(data[:n]), names)); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// companionLogPersonalKey reports a setting whose value names a person, an
// account or a network.
func companionLogPersonalKey(key string) bool {
	k := strings.ToLower(key)
	for _, skip := range [...]string{"pin", "code", "enabled", "auto", "mode", "required", "show", "style", "font", "voice"} {
		if strings.Contains(k, skip) {
			return false
		}
	}
	for _, w := range [...]string{"username", "user_name", "nickname", "displayname", "display_name", "persona", "email", "login", "ssid", "account", "owner"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// companionLogAccountNamesOnMain reads the user's names from the settings.
// Main thread.
func companionLogAccountNamesOnMain(c *Config) []string {
	if c == nil {
		return nil
	}
	out := []string{userName(c), strings.TrimSpace(c.Variables.TSPUsername)}
	for key, raw := range c.Variables.Custom {
		v, ok := raw.(string)
		if !ok || !companionLogPersonalKey(key) {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" || strings.HasPrefix(v, "ENC:") || strings.Contains(v, "://") || logStoredSecretIsPath(v) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// companionLogPersonalNames gathers the exact names the zip masks: the Wi-Fi
// networks the handheld knows and the one it is on, the RetroAchievements
// user name found on the card, and the names in the settings (main thread).
func companionLogPersonalNames() logPersonalNames {
	var names logPersonalNames
	if nets, err := wifiReadSavedNetworks(); err == nil {
		for _, n := range nets {
			names.Wifi = append(names.Wifi, n.SSID)
		}
	}
	if st, err := wifiReadStatus(); err == nil && st.SSID != "" {
		names.Wifi = append(names.Wifi, st.SSID)
	}
	if user, _ := raDetectUsername(); user != "" {
		names.Accounts = append(names.Accounts, user)
	}
	// JukaHub's own sign-ins (both behind their own locks, any goroutine).
	names.Accounts = append(names.Accounts, raLoadSettings().Username, screenScraperLoadAccount().User)
	got := make(chan []string, 1)
	tbxPost(func() { got <- companionLogAccountNamesOnMain(appConfig) })
	select {
	case list := <-got:
		names.Accounts = append(names.Accounts, list...)
	case <-time.After(companionLogsNamesWait):
		logInfo("companion", "logs: the settings' names were not read in time; masking by pattern only")
	}
	return names
}

// handleLogsZip answers GET /api/logs.zip.
func (s *companionServer) handleLogsZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !companionLogsBusy.TryLock() {
		http.Error(w, "The logs are already being collected. Try again in a moment.", http.StatusTooManyRequests)
		return
	}
	defer companionLogsBusy.Unlock()
	// A phone that stops reading must not hold the one-zip lock for good.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
	names := companionLogPersonalNames()
	files := companionLogFiles()
	now := time.Now()

	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", companionFilesDisposition("jukahub-logs-"+now.Format("2006-01-02-1504")+".zip"))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")

	zw := zip.NewWriter(w)
	budget := int64(companionLogsMaxBytes)
	taken := 0
	add := func(name string, mod time.Time, data []byte) error {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mod})
		if err != nil {
			return err
		}
		budget -= int64(len(data))
		taken++
		return companionLogWriteMasked(fw, data, names)
	}
	readme := fmt.Sprintf("JukaHub logs, %s\nBuild: %s\n\nPasswords, keys and tokens, Wi-Fi network names, account and display names,\ne-mail addresses, Bluetooth and Wi-Fi hardware addresses and LAN addresses\nare masked ([wifi], [account], [email], [mac], [lan-ip], [redacted]).\nGame names and folders stay, so the logs still explain the problem:\nread them before you share them.\n\nNewest files first, %d MB at most; a long file keeps its newest part.\n",
		now.Format("2006-01-02 15:04"), s.companionUpdateCurrent(), companionLogsMaxBytes>>20)
	if fw, err := zw.CreateHeader(&zip.FileHeader{Name: "README.txt", Method: zip.Deflate, Modified: now}); err == nil {
		_, _ = io.WriteString(fw, readme)
	}
	if lines, _ := appLog.Snapshot(); len(lines) > 0 {
		live := []byte(strings.Join(lines, "\n") + "\n")
		if err := add("live-log.txt", now, live); err != nil {
			logWarn("companion", "logs: zip stopped: %v", err)
			return
		}
	}
	for _, f := range files {
		if budget < 4<<10 {
			break
		}
		limit := int64(companionLogsFileMax)
		if limit > budget {
			limit = budget
		}
		data, ok := companionLogReadTail(f.path, limit)
		if !ok || len(data) == 0 {
			continue
		}
		if err := add(f.name, f.mod, data); err != nil {
			// The phone went away mid-download: nothing more to send.
			logWarn("companion", "logs: zip stopped: %v", err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		logWarn("companion", "logs: zip not finished: %v", err)
		return
	}
	logInfo("companion", "logs: sent %d log files to the phone (%d KB before masking)", taken, (int64(companionLogsMaxBytes)-budget)>>10)
}
