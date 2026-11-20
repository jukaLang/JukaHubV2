package main

// nas.go — Games on a home network drive: the drives themselves.
//
// Settings → Network → Network drives (nas_ui.go) keeps a short list of
// places on the home network that hold games: a Windows or NAS shared folder
// (SMB, nas_smb.go over smb2*.go's read-only client), a WebDAV folder
// (nas_webdav.go over webdav_client.go), an SFTP server (nas_sftp.go) and,
// on a PC, any folder Windows reaches, a \\nas\share path included
// (nas_folder.go). Each is one nasSource in <data>/nas.json (0600); its
// password is sealed the way mail passwords are (EncryptAPIToken).
//
// Every backend is a nasFS: list a folder, describe one entry, and stream a
// file from any byte (a download that stopped resumes where its .part ends).
// nas_index.go walks the drives' console folders into a cached game list the
// Games library shows beside the card's games; nas_download.go copies a game
// to Roms/<console>/ when it is asked for.
//
// Names on a drive are outside data: a file called "..\..\x" or "CON" must
// never become a path on the card. nasSafeName and nasLocalPath are the one
// gate every name goes through (security_paths_test.go).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	nasKindSMB    = "smb"
	nasKindWebDAV = "webdav"
	nasKindSFTP   = "sftp"
	// nasKindFolder is a folder the operating system reaches itself: on a PC
	// a mapped drive or a \\nas\share path (nas_folder.go).
	nasKindFolder = "folder"

	// nasMaxSources bounds the list: a handful of drives is what a home has.
	nasMaxSources = 8
	// nasPasswordKeyName labels the sealed password in log lines.
	nasPasswordKeyName = "network drive password"
	// nasSheetMaxBytes bounds how much of a remote .cue/.gdi/.m3u is read.
	nasSheetMaxBytes = 64 << 10
	// nasNameMax is the longest file or folder name taken from a drive.
	nasNameMax = 255
	// nasFieldMax bounds what the keyboard may store in one field.
	nasFieldMax = 512
	// nasSkipMapping is the mapping value of a folder that holds no games.
	nasSkipMapping = "-"
)

// nasSource is one network drive.
type nasSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Host is the server's name or address (SMB, SFTP), the WebDAV address
	// (https://nas.local/dav/) or the folder (nasKindFolder).
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	// Share is the SMB shared folder.
	Share string `json:"share,omitempty"`
	// Root is the folder that holds the games, "/"-separated, below the share,
	// the WebDAV address, the SFTP login folder or Host's folder ("" is the top).
	Root     string `json:"root,omitempty"`
	User     string `json:"user,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Password string `json:"password,omitempty"`
	// HostKey is the SFTP server's SHA256 fingerprint the player trusted.
	HostKey string `json:"host_key,omitempty"`
	// Map assigns folders below Root (by name, "." for Root itself) to a
	// library console, or nasSkipMapping. A folder not listed is matched by
	// its name (GBA, SFC, PS …) the way the card's Roms folders are.
	Map map[string]string `json:"map,omitempty"`
}

// nasSettings is nas.json.
type nasSettings struct {
	V       int         `json:"v"`
	Sources []nasSource `json:"sources"`
}

var nasStore struct {
	mu     sync.Mutex
	loaded bool
	value  nasSettings
}

// nasDataPath is a file in JukaHub's data folder ("" when there is none). A
// test seam.
var nasDataPath = func(name string) string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

func nasLoadLocked() {
	if nasStore.loaded {
		return
	}
	nasStore.loaded = true
	p := nasDataPath("nas.json")
	if p == "" {
		return
	}
	var s nasSettings
	if _, err := readJSONWithBackup(p, &s); err == nil {
		nasStore.value = nasCleanSettings(s)
	} else if !os.IsNotExist(err) {
		logWarn("nas", "ignoring nas.json: %v", err)
	}
}

// nasCleanSettings drops what a hand-edited or older file cannot mean: an
// entry with no id or an unknown kind, ids twice, more than nasMaxSources.
func nasCleanSettings(s nasSettings) nasSettings {
	out := nasSettings{V: 1}
	seen := map[string]bool{}
	for _, src := range s.Sources {
		if src.ID == "" || seen[src.ID] || !nasKnownKind(src.Kind) {
			continue
		}
		seen[src.ID] = true
		out.Sources = append(out.Sources, nasCopySource(src))
		if len(out.Sources) >= nasMaxSources {
			break
		}
	}
	return out
}

func nasKnownKind(kind string) bool {
	switch kind {
	case nasKindSMB, nasKindWebDAV, nasKindSFTP, nasKindFolder:
		return true
	}
	return false
}

// nasCopySource deep-copies the mapping so callers cannot alias the store.
func nasCopySource(src nasSource) nasSource {
	if src.Map != nil {
		m := make(map[string]string, len(src.Map))
		for k, v := range src.Map {
			m[k] = v
		}
		src.Map = m
	}
	return src
}

// nasSettingsGet is the stored list (read from disk once). Any goroutine.
func nasSettingsGet() nasSettings {
	nasStore.mu.Lock()
	defer nasStore.mu.Unlock()
	nasLoadLocked()
	out := nasSettings{V: nasStore.value.V}
	for _, src := range nasStore.value.Sources {
		out.Sources = append(out.Sources, nasCopySource(src))
	}
	return out
}

// nasSourceByID is one stored drive.
func nasSourceByID(id string) (nasSource, bool) {
	for _, src := range nasSettingsGet().Sources {
		if src.ID == id {
			return src, true
		}
	}
	return nasSource{}, false
}

// nasSettingsUpdate changes the stored list and writes it (0600: it holds
// sealed passwords). Any goroutine.
func nasSettingsUpdate(change func(*nasSettings)) error {
	nasStore.mu.Lock()
	defer nasStore.mu.Unlock()
	nasLoadLocked()
	next := nasSettings{V: 1}
	for _, src := range nasStore.value.Sources {
		next.Sources = append(next.Sources, nasCopySource(src))
	}
	change(&next)
	next = nasCleanSettings(next)
	p := nasDataPath("nas.json")
	if p == "" {
		return errors.New("the settings folder is unavailable")
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(p, append(data, '\n'), 0o600); err != nil {
		return err
	}
	nasStore.value = next
	return nil
}

// nasSaveSource stores src, replacing the drive with its id or adding it.
func nasSaveSource(src nasSource) error {
	return nasSettingsUpdate(func(s *nasSettings) {
		for i := range s.Sources {
			if s.Sources[i].ID == src.ID {
				s.Sources[i] = nasCopySource(src)
				return
			}
		}
		s.Sources = append(s.Sources, nasCopySource(src))
	})
}

// nasRemoveSource forgets a drive (its listing goes with it, nas_index.go).
func nasRemoveSource(id string) error {
	return nasSettingsUpdate(func(s *nasSettings) {
		kept := s.Sources[:0]
		for _, src := range s.Sources {
			if src.ID != id {
				kept = append(kept, src)
			}
		}
		s.Sources = kept
	})
}

// nasNewID is a short random id for a drive.
func nasNewID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "n" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// nasSeal encrypts a password for the store ("" stays "").
func nasSeal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	return EncryptAPIToken(plain, nasPasswordKeyName)
}

// nasOpenSecret decrypts a stored password; "" when it is empty or cannot
// be read here (a file copied from another device). A value typed into the
// file by hand is used as it is.
func nasOpenSecret(stored string) string {
	if stored == "" {
		return ""
	}
	if !strings.HasPrefix(stored, "ENC:") {
		return stored
	}
	plain, err := DecryptAPIToken(stored, nasPasswordKeyName)
	if err != nil {
		return ""
	}
	return plain
}

// ── Names and labels ─────────────────────────────────────────────────────────

// nasKindLabel is how a page names a kind of drive.
func nasKindLabel(kind string) string {
	switch kind {
	case nasKindSMB:
		return "Shared folder (SMB)"
	case nasKindWebDAV:
		return "WebDAV"
	case nasKindSFTP:
		return "SFTP"
	case nasKindFolder:
		return "Folder"
	}
	return kind
}

// nasDisplayName is the drive's name: the one the player gave it, else its
// host and share.
func nasDisplayName(src nasSource) string {
	if name := strings.TrimSpace(src.Name); name != "" {
		return name
	}
	switch src.Kind {
	case nasKindSMB:
		if src.Share != "" {
			return src.Host + "/" + src.Share
		}
	case nasKindWebDAV:
		if u := nasHostOfURL(src.Host); u != "" {
			return u
		}
	}
	if src.Host != "" {
		return src.Host
	}
	return "Network drive"
}

// nasWhere is the drive's address and games folder in one line.
func nasWhere(src nasSource) string {
	where := src.Host
	switch src.Kind {
	case nasKindSMB:
		where = `\\` + src.Host
		if src.Share != "" {
			where += `\` + src.Share
		}
		if src.Root != "" {
			where += `\` + strings.ReplaceAll(src.Root, "/", `\`)
		}
		return where
	case nasKindSFTP:
		where = "sftp://" + src.Host
		if src.Port > 0 && src.Port != 22 {
			where += ":" + strconv.Itoa(src.Port)
		}
	}
	if src.Root != "" {
		where = strings.TrimSuffix(where, "/") + "/" + src.Root
	}
	return where
}

// nasHostOfURL is the host of a WebDAV address, "" when it does not parse.
func nasHostOfURL(raw string) string {
	u, err := webdavCleanURL(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// nasDialAddress is the host:port a drive answers on, for the reachability
// probe; "" for a folder the operating system reaches.
func nasDialAddress(src nasSource) string {
	switch src.Kind {
	case nasKindSMB:
		port := src.Port
		if port <= 0 {
			port = 445
		}
		return net.JoinHostPort(src.Host, strconv.Itoa(port))
	case nasKindSFTP:
		port := src.Port
		if port <= 0 {
			port = 22
		}
		return net.JoinHostPort(src.Host, strconv.Itoa(port))
	case nasKindWebDAV:
		u, err := webdavCleanURL(src.Host)
		if err != nil {
			return ""
		}
		port := u.Port()
		if port == "" {
			port = "443"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		return net.JoinHostPort(u.Hostname(), port)
	}
	return ""
}

// nasSafeName reports whether one file or folder name from a drive may become
// part of a path on the card: not empty, ".", ".." or overlong; no slash,
// backslash, colon, control character or other character FAT and Windows
// refuse; no trailing dot or space (Windows drops them, so two names would be
// one file); not a Windows device name (CON, NUL, COM1 …, with or without an
// extension).
func nasSafeName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > nasNameMax {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return false
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") || strings.HasPrefix(name, " ") {
		return false
	}
	stem := strings.ToUpper(name)
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	stem = strings.TrimRight(stem, " ")
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9' {
		return false
	}
	return true
}

// nasSafeRel reports whether rel, a "/"-separated path below a folder, is made
// only of safe names.
func nasSafeRel(rel string) bool {
	if rel == "" {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if !nasSafeName(seg) {
			return false
		}
	}
	return true
}

// nasLocalPath is where a game's file rel (a "/"-separated path below its
// console folder on the drive) goes on the card: romsRoot/folder/rel. Every
// name must pass nasSafeName and the result must stay inside
// romsRoot/folder, else it is refused.
func nasLocalPath(romsRoot, folder, rel string) (string, error) {
	romsRoot = strings.TrimSpace(romsRoot)
	if romsRoot == "" {
		return "", errors.New("no games folder on the card")
	}
	if !nasSafeName(folder) {
		return "", errors.New("the console folder's name cannot be used on the card")
	}
	if !nasSafeRel(rel) {
		return "", errors.New("the file's name cannot be used on the card")
	}
	base := filepath.Join(romsRoot, folder)
	out := filepath.Join(base, filepath.FromSlash(rel))
	if out == base || !isPathWithin(out, base) {
		return "", errors.New("the file would land outside its console folder")
	}
	return out, nil
}

// nasJoin joins "/"-separated pieces of a path on a drive. A leading "/"
// (an SFTP path from the server's top) is kept; "" pieces are skipped.
func nasJoin(parts ...string) string {
	abs := false
	var keep []string
	for i, p := range parts {
		p = strings.ReplaceAll(p, `\`, "/")
		if i == 0 && strings.HasPrefix(p, "/") {
			abs = true
		}
		p = strings.Trim(p, "/")
		if p != "" && p != "." {
			keep = append(keep, p)
		}
	}
	joined := path.Clean(strings.Join(keep, "/"))
	if joined == "." {
		joined = ""
	}
	if abs {
		return "/" + joined
	}
	return joined
}

// nasCleanRoot is a games folder as the player typed or picked it: "/"
// separated, no "." or ".." names. ok is false for a path that climbs.
func nasCleanRoot(root string, keepAbs bool) (string, bool) {
	root = strings.TrimSpace(strings.ReplaceAll(root, `\`, "/"))
	abs := keepAbs && strings.HasPrefix(root, "/")
	var keep []string
	for _, seg := range strings.Split(root, "/") {
		switch seg {
		case "", ".":
			continue
		case "..":
			return "", false
		}
		keep = append(keep, seg)
	}
	out := strings.Join(keep, "/")
	if abs {
		out = "/" + out
	}
	return out, true
}

// nasSheetRefs reads what a .cue, .gdi or .m3u names (outside data: the
// sheet came from a drive) as "/"-separated paths relative to the sheet's own
// folder. A name that is absolute, has a drive letter or climbs out of the
// sheet's folder is dropped: a game's parts stay beside it or below it.
func nasSheetRefs(name string, data []byte) []string {
	ext := strings.ToLower(path.Ext(name))
	if ext != ".cue" && ext != ".gdi" && ext != ".m3u" {
		return nil
	}
	if len(data) > nasSheetMaxBytes {
		data = data[:nasSheetMaxBytes]
	}
	var out []string
	seen := map[string]bool{}
	add := func(ref string) {
		ref = strings.TrimSpace(strings.ReplaceAll(ref, `\`, "/"))
		if ref == "" || strings.HasPrefix(ref, "/") || strings.ContainsAny(ref, ":\x00") {
			return
		}
		clean := path.Clean(ref)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return
		}
		if key := strings.ToLower(clean); !seen[key] {
			seen[key] = true
			out = append(out, clean)
		}
	}
	quoted := func(s string) (string, bool) {
		start := strings.IndexByte(s, '"')
		if start < 0 {
			return "", false
		}
		end := strings.IndexByte(s[start+1:], '"')
		if end < 0 {
			return "", false
		}
		return s[start+1 : start+1+end], true
	}
	for _, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch ext {
		case ".m3u":
			if strings.HasPrefix(line, "#") {
				continue
			}
			if bar := strings.IndexByte(line, '|'); bar >= 0 {
				line = line[:bar]
			}
			add(line)
		case ".cue":
			if len(line) < 5 || !strings.EqualFold(line[:5], "FILE ") {
				continue
			}
			if ref, ok := quoted(line); ok {
				add(ref)
			} else if fields := strings.Fields(line); len(fields) > 1 {
				add(fields[1])
			}
		case ".gdi":
			if ref, ok := quoted(line); ok {
				add(ref)
			} else if fields := strings.Fields(line); len(fields) >= 6 {
				add(fields[4])
			}
		}
	}
	return out
}

// ── Consoles ─────────────────────────────────────────────────────────────────

// nasFolderSystem is the console a folder on a drive holds: the player's
// assignment first (nasSkipMapping: none), else the folder's name read the way
// the card's Roms folders are (GBA, SFC, "Game Boy Advance (GBA)").
func nasFolderSystem(name string, assigned map[string]string, pluginByFolder map[string]romSystem) (romSystem, bool) {
	if v, ok := assigned[name]; ok {
		if v == nasSkipMapping || v == "" {
			return romSystem{}, false
		}
		return romSystem{Name: v, Collection: romCollectionForSystem(v)}, true
	}
	if name == "." {
		return romSystem{}, false
	}
	sys, _, ok := romFolderSystem(name, pluginByFolder)
	if !ok || sys.Name == "" {
		return romSystem{}, false
	}
	return sys, true
}

// nasSystemChoices is every console a folder can be given, in the library's
// order (the well-known ones first).
func nasSystemChoices() []string {
	seen := map[string]bool{}
	var out []string
	for _, sys := range romSystemAliases {
		if sys.Name != "" && !seen[sys.Name] {
			seen[sys.Name] = true
			out = append(out, sys.Name)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, iKnown := consoleOrder[out[i]]
		rj, jKnown := consoleOrder[out[j]]
		switch {
		case iKnown && jKnown:
			return ri < rj
		case iKnown != jKnown:
			return iKnown
		}
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

// nasSystemFromTyped reads a console the player typed: a folder name the card
// knows (gba, snes, psx) or a console's own name ("Game Boy Advance").
func nasSystemFromTyped(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	if sys, ok := romSystemAliases[strings.ToLower(text)]; ok {
		return sys.Name, true
	}
	for _, name := range nasSystemChoices() {
		if strings.EqualFold(name, text) || normaliseLauncherName(name) == normaliseLauncherName(text) {
			return name, true
		}
	}
	return "", false
}

// nasDefaultLocalFolder is the Roms folder a console's games go to when the
// card has none for it yet: the drive's own folder name when the card's scan
// reads it as that console, else the console's shortest folder name in upper
// case (GBA, SFC, PS — the stock firmware's names).
func nasDefaultLocalFolder(system, serverFolder string) string {
	if serverFolder != "" && serverFolder != "." && nasSafeName(serverFolder) {
		if sys, ok := romSystemAliases[strings.ToLower(serverFolder)]; ok && sys.Name == system {
			return serverFolder
		}
	}
	best := ""
	for alias, sys := range romSystemAliases {
		if sys.Name != system || strings.ContainsAny(alias, " -") {
			continue
		}
		if best == "" || len(alias) < len(best) || (len(alias) == len(best) && alias < best) {
			best = alias
		}
	}
	if best == "" {
		// A console only a plugin or a NextUI tag names: its own name.
		best = strings.Map(func(r rune) rune {
			if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
				return -1
			}
			return r
		}, system)
		return strings.TrimSpace(best)
	}
	return strings.ToUpper(best)
}

// nasLocalSystemFolders maps each console to the folder the card already has
// for it in romsRoot (the first in name order when there are two). Reads the
// card: off the render thread.
func nasLocalSystemFolders(romsRoot string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(romsRoot)
	if err != nil {
		return out
	}
	onNextUI := nextuiActive()
	for _, e := range entries {
		if !e.IsDir() || skipScanEntryName(e.Name()) {
			continue
		}
		sys, tagged, ok := romFolderSystem(e.Name(), nil)
		if !ok || sys.Name == "" || (tagged && !onNextUI) {
			continue
		}
		if _, have := out[sys.Name]; !have {
			out[sys.Name] = e.Name()
		}
	}
	return out
}

// nasRomsRoot is the card's games folder downloads go to: the same one RomM
// downloads use (the first Roms folder that exists).
func nasRomsRoot() string {
	return rommPickRomsRoot(romLibraryRoots())
}

// ── The file systems ─────────────────────────────────────────────────────────

// nasEntry is one name in a folder on a drive.
type nasEntry struct {
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// nasFS is one open connection to a drive. Paths are "/"-separated below the
// backend's top (the share, the WebDAV address, the SFTP login folder or the
// folder itself); "" is the top.
type nasFS interface {
	List(ctx context.Context, dir string) ([]nasEntry, error)
	Stat(ctx context.Context, p string) (nasEntry, error)
	// ReadFrom streams the file p from byte off; size is the whole file's.
	ReadFrom(ctx context.Context, p string, off int64) (rc io.ReadCloser, size int64, err error)
	Close() error
}

var (
	errNasNotFound = errors.New("not found on the network drive")
	errNasAuth     = errors.New("the network drive refused the user name or password")
	errNasNoShare  = errors.New("choose the shared folder first")
	errNasTooBig   = errors.New("that folder holds too many files to copy as one game")
	// errNasPartTooBig: the file on the drive is now smaller than the .part
	// on the card, so the copy starts that file again (nasCopyFile).
	errNasPartTooBig = errors.New("the file on the drive is smaller than the part already copied")
	// errNasHostKeyUnknown: an SFTP server nobody has trusted yet (nas_sftp.go).
	errNasHostKeyUnknown = errors.New("this SFTP server is not trusted yet — open Network drives and test it to see its fingerprint")
)

// nasDial opens a connection to src.
func nasDial(ctx context.Context, src nasSource) (nasFS, error) {
	switch src.Kind {
	case nasKindSMB:
		return nasDialSMB(ctx, src)
	case nasKindWebDAV:
		return nasDialWebDAV(src)
	case nasKindSFTP:
		return nasDialSFTP(ctx, src, nil)
	case nasKindFolder:
		return nasDialFolder(src)
	}
	return nil, errors.New("this kind of network drive is not known here")
}

// nasReadSmall reads at most limit bytes of p (a sheet).
func nasReadSmall(ctx context.Context, fs nasFS, p string, limit int64) ([]byte, error) {
	rc, _, err := fs.ReadFrom(ctx, p, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

// nasErrorText is a drive error as a plain sentence.
func nasErrorText(src nasSource, err error) string {
	if err == nil {
		return ""
	}
	name := nasDisplayName(src)
	var ne net.Error
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return name + " did not answer in time — is it switched on and on this network?"
	case errors.Is(err, context.Canceled):
		return "Stopped"
	case errors.Is(err, errNasAuth):
		return name + " refused the user name or password"
	case errors.Is(err, errNasNotFound):
		return "That folder or file is not on " + name + " (any more)"
	case errors.Is(err, errNasHostKeyUnknown), errors.Is(err, errNasNoShare):
		return firstLine(msg)
	case strings.Contains(msg, "no such host"):
		return "No device called " + src.Host + " answers on this network — try its IP address"
	case strings.Contains(msg, "connection refused"):
		return name + " is on, but nothing answers for " + nasKindLabel(src.Kind) + " — is sharing switched on?"
	case strings.Contains(msg, "network is unreachable"), strings.Contains(msg, "no route to host"), strings.Contains(msg, "host is down"):
		return name + " is not reachable — connect to your home Wi-Fi"
	}
	return firstLine(msg)
}
