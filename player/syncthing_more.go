package main

// syncthing_more.go — Syncthing beyond save files: more folders, and its web
// GUI on the local network.
//
// JukaHub's own Syncthing (syncthing.go) shares save folders only and keeps
// its GUI shut. Two additions on the Syncthing page, both off by default:
//
//   - "Also sync": Screenshots, Recordings, Music, Books and Roms, each its
//     own folder (ID jukahub-extra-<slug>, so two JukaHubs match up), built by
//     syncthingFolderObject and shared by syncthingShareFolders exactly like a
//     save folder — the same FAT-friendly settings, the same pauses, every
//     paired device. Turning one off takes it out of Syncthing; the files
//     stay.
//   - "Web GUI on this network": the GUI listens on 0.0.0.0 (a start flag, so
//     the instance restarts) with user "jukahub" and a generated password
//     shown on the page, with a QR code to http://<IP>:<port>. Off again, it
//     is back on 127.0.0.1 with a new password nobody knows.
//
// The choice is resolved on the main thread (the Recordings folder reads the
// config) and mirrored for the worker, which makes the folders and talks REST.

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	syncthingAlsoKey   = "syncthingAlsoSync"
	syncthingWebGUIKey = "syncthingWebGui"
	// syncthingExtraPrefix starts every extra folder's ID.
	syncthingExtraPrefix = "jukahub-extra-"
	// syncthingWebUser is the web GUI's user name.
	syncthingWebUser = "jukahub"
)

// The Syncthing page's rows for this file, well clear of syncthing_ui.go's.
const (
	syncthingRowExtra syncthingRowKind = 100 + iota
	syncthingRowWebGUI
	syncthingRowWebAddr
)

// syncthingExtra is one "Also sync" folder.
type syncthingExtra struct {
	slug   string
	name   string
	detail string
}

var syncthingExtras = [...]syncthingExtra{
	{"screenshots", "Screenshots", "Every screenshot reaches your phone or PC as you take it"},
	{"recordings", "Recordings", "Gameplay videos: big files, best at home on Wi-Fi"},
	{"music", "Music", "Both ways: add songs on a PC and they appear here"},
	{"books", "Books", "Both ways: books and comics for the reader"},
	{"roms", "Roms", "Every game, both ways. Many GB: the first sync takes hours and needs that much room on the other side"},
}

// syncthingExtraState is the main thread's choice, for the worker: the
// folders that are on, and every extra's folder for the rows. ready is set
// once the choice was read, so the worker never takes "nothing read yet" for
// "all off".
var syncthingExtraState struct {
	mu      sync.Mutex
	ready   bool
	folders []saveSyncFolder
	paths   map[string]string
}

// syncthingWebGUIOn mirrors "Web GUI on this network" for the worker.
var syncthingWebGUIOn atomic.Bool

// syncthingWebLoginNow is the web GUI's password and address while it is
// open, for the page. Written by the worker.
var syncthingWebLoginNow struct {
	mu   sync.Mutex
	pass string
	addr string
}

// ── Settings ─────────────────────────────────────────────────────────────

// syncthingAlsoSet is the "Also sync" choice: the slugs that are on.
func syncthingAlsoSet(config *Config) map[string]bool {
	on := map[string]bool{}
	if config == nil {
		return on
	}
	for _, part := range strings.Split(customStringValue(config.Variables.Custom, syncthingAlsoKey), ",") {
		if slug := strings.ToLower(strings.TrimSpace(part)); slug != "" {
			on[slug] = true
		}
	}
	return on
}

func syncthingWebGUISetting(config *Config) bool {
	if config == nil {
		return false
	}
	return customBool(config.Variables.Custom, syncthingWebGUIKey, false)
}

// syncthingExtrasBoot mirrors both choices (syncthingBoot, main thread). The
// folders are only looked up when one is on.
func syncthingExtrasBoot(config *Config) {
	syncthingWebGUIOn.Store(syncthingWebGUISetting(config))
	if len(syncthingAlsoSet(config)) > 0 {
		syncthingExtrasMirror(config)
		return
	}
	syncthingExtraState.mu.Lock()
	syncthingExtraState.ready = true
	syncthingExtraState.folders = nil
	syncthingExtraState.mu.Unlock()
}

// syncthingExtrasMirror resolves every extra's folder and hands the ones
// that are on to the worker. Main thread.
func syncthingExtrasMirror(config *Config) {
	on := syncthingAlsoSet(config)
	paths := make(map[string]string, len(syncthingExtras))
	var folders []saveSyncFolder
	for _, e := range syncthingExtras {
		p := syncthingExtraPath(config, e.slug)
		paths[e.slug] = p
		if on[e.slug] && p != "" {
			folders = append(folders, saveSyncFolder{Name: e.name, Path: p, FolderID: syncthingExtraPrefix + e.slug})
		}
	}
	syncthingExtraState.mu.Lock()
	syncthingExtraState.ready = true
	syncthingExtraState.folders = folders
	syncthingExtraState.paths = paths
	syncthingExtraState.mu.Unlock()
}

// syncthingExtraPaths is every extra's folder, for the rows; looked up the
// first time the page asks. Main thread.
func syncthingExtraPaths() map[string]string {
	syncthingExtraState.mu.Lock()
	paths := syncthingExtraState.paths
	syncthingExtraState.mu.Unlock()
	if paths == nil {
		syncthingExtrasMirror(appConfig)
		syncthingExtraState.mu.Lock()
		paths = syncthingExtraState.paths
		syncthingExtraState.mu.Unlock()
	}
	return paths
}

// syncthingExtraPath is the folder an extra syncs: the one JukaHub already
// uses for it (screenshots, recordings, the Roms folder games are read
// from), or Music and Books on the card (in the home folder on a PC).
func syncthingExtraPath(config *Config, slug string) string {
	switch slug {
	case "screenshots":
		return screenshotDir()
	case "recordings":
		return recordingsDirFor(config)
	case "roms":
		return rommPickRomsRoot(romLibraryRoots())
	case "music", "books":
		name := "Music"
		if slug == "books" {
			name = "Books"
		}
		if IsTrimuiDevice() {
			card := saveManagerSDRoot()
			if card == "" {
				return ""
			}
			if dir := companionDropDirCardDir(card, []string{name}); dir != "" {
				return dir
			}
			return filepath.Join(card, name)
		}
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		if slug == "books" {
			return filepath.Join(home, "Documents", "Books")
		}
		return filepath.Join(home, "Music")
	}
	return ""
}

// ── The worker's side ────────────────────────────────────────────────────

// syncthingExtraFolders are the extras that are on, each folder made when
// missing (Syncthing shares no path that is not there). Worker goroutine.
func syncthingExtraFolders() []saveSyncFolder {
	syncthingExtraState.mu.Lock()
	folders := append([]saveSyncFolder(nil), syncthingExtraState.folders...)
	syncthingExtraState.mu.Unlock()
	out := folders[:0]
	for _, f := range folders {
		if err := os.MkdirAll(f.Path, 0o755); err != nil {
			logInfo("syncthing", "extra folder %s: %v", f.Path, err)
			continue
		}
		out = append(out, f)
	}
	return out
}

// syncthingWithExtras is the save folders followed by the extras, in a new
// slice (the save list is a cached manifest's).
func syncthingWithExtras(saves, extras []saveSyncFolder) []saveSyncFolder {
	out := make([]saveSyncFolder, 0, len(saves)+len(extras))
	out = append(out, saves...)
	return append(out, extras...)
}

// syncthingPruneExtras takes extras that were turned off out of Syncthing's
// configuration (the files stay). byID is the folder configuration
// syncthingShareFolders read, extras the ones that are on. Worker goroutine.
func syncthingPruneExtras(ctx context.Context, api syncthingAPI, byID map[string]map[string]any, extras []saveSyncFolder) {
	syncthingExtraState.mu.Lock()
	ready := syncthingExtraState.ready
	syncthingExtraState.mu.Unlock()
	if !ready {
		return
	}
	keep := make(map[string]bool, len(extras))
	for _, f := range extras {
		keep[f.FolderID] = true
	}
	for id := range byID {
		if !strings.HasPrefix(id, syncthingExtraPrefix) || keep[id] {
			continue
		}
		if err := syncthingDropFolder(ctx, api, id); err != nil {
			logInfo("syncthing", "stop syncing %s: %v", id, err)
			continue
		}
		logInfo("syncthing", "stopped syncing %s (turned off; the files stay)", id)
	}
}

// syncthingDropFolder removes a folder from the configuration; one that is
// not there is no error.
func syncthingDropFolder(ctx context.Context, api syncthingAPI, id string) error {
	err := api.call(ctx, http.MethodDelete, "/rest/config/folders/"+url.PathEscape(id), nil, nil)
	if err != nil && strings.Contains(err.Error(), "404") {
		return nil
	}
	return err
}

// ── Web GUI ──────────────────────────────────────────────────────────────

// syncthingGuiHost is the address the GUI listens on: every interface while
// "Web GUI on this network" is on, else loopback only (syncthingStart). The
// password file must be there too: it is written once a configure has given
// the GUI a login, so a fresh home's first start (no user, no password yet)
// never listens on the network; syncthingApplyWebGUI restarts it then.
func syncthingGuiHost() string {
	if syncthingWebGUIOn.Load() && fileExists(syncthingWebPassFile()) {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

func syncthingWebPassFile() string { return filepath.Join(syncthingHome(), "jukahub-webgui-password") }

// syncthingWebNewPassword is a password to read off the screen: twelve
// letters and digits without look-alikes, in groups of four (60 bits).
func syncthingWebNewPassword() (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789" // 32: no modulo bias
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var out strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			out.WriteByte('-')
		}
		out.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return out.String(), nil
}

// syncthingApplyWebGUI sets the GUI's login for the choice: user "jukahub"
// and the shown password while it is open on the network, a new password
// nobody knows once it is closed again. syncthingConfigure, after every
// start; worker goroutine.
func syncthingApplyWebGUI(ctx context.Context, api syncthingAPI) {
	file := syncthingWebPassFile()
	if !syncthingWebGUIOn.Load() {
		syncthingWebLoginNow.mu.Lock()
		syncthingWebLoginNow.pass, syncthingWebLoginNow.addr = "", ""
		syncthingWebLoginNow.mu.Unlock()
		if _, err := os.Stat(file); err != nil {
			return // never shown: the password is still one nobody knows
		}
		pw, err := syncthingRandomHex(24)
		if err != nil {
			return
		}
		if err := api.call(ctx, http.MethodPatch, "/rest/config/gui", map[string]any{"user": syncthingWebUser, "password": pw}, nil); err != nil {
			logWarn("syncthing", "close the web GUI: %v", err)
			return
		}
		_ = os.Remove(file)
		return
	}
	pw := ""
	// Was the file there when this instance started? Then it listens on the
	// network (syncthingGuiHost); else it is on loopback until a restart.
	open := false
	if data, err := os.ReadFile(file); err == nil {
		pw = strings.TrimSpace(string(data))
		open = true
	}
	if len(pw) < 12 {
		fresh, err := syncthingWebNewPassword()
		if err != nil {
			logWarn("syncthing", "web GUI password: %v", err)
			return
		}
		if err := AtomicWrite(file, []byte(fresh+"\n"), 0o600); err != nil {
			logWarn("syncthing", "web GUI password: %v", err)
			return
		}
		pw = fresh
	}
	if err := api.call(ctx, http.MethodPatch, "/rest/config/gui", map[string]any{"user": syncthingWebUser, "password": pw}, nil); err != nil {
		logWarn("syncthing", "open the web GUI: %v", err)
		return
	}
	if !open {
		// The login is set now: a restart opens it on the network. Queued for
		// the worker, which runs it after this configure (non-blocking).
		syncthingPost("webgui", func(ctx context.Context, api syncthingAPI) (string, error) {
			syncthingStop(3 * time.Second)
			return "", nil
		})
		return
	}
	addr := ""
	if ip := localIPv4(); ip != "" {
		addr = "http://" + ip + ":" + strconv.Itoa(api.port)
	}
	syncthingWebLoginNow.mu.Lock()
	syncthingWebLoginNow.pass, syncthingWebLoginNow.addr = pw, addr
	syncthingWebLoginNow.mu.Unlock()
	logInfo("syncthing", "web GUI open on this network (port %d)", api.port)
}

// syncthingWebLogin is the open web GUI's password and address ("" while it
// is shut or not applied yet).
func syncthingWebLogin() (pass, addr string) {
	syncthingWebLoginNow.mu.Lock()
	defer syncthingWebLoginNow.mu.Unlock()
	return syncthingWebLoginNow.pass, syncthingWebLoginNow.addr
}

// ── The page ─────────────────────────────────────────────────────────────

// syncthingExtraRows adds the "Also sync" switches and the web GUI rows to
// the Syncthing page (syncthingPanelRows).
func syncthingExtraRows(s syncthingSnapshot, add func(ref syncthingRowRef, row saveManagerRow)) {
	paths := syncthingExtraPaths()
	on := syncthingAlsoSet(appConfig)
	for _, e := range syncthingExtras {
		detail := e.detail
		if p := paths[e.slug]; p != "" {
			detail += " · " + p
		}
		add(syncthingRowRef{kind: syncthingRowExtra, id: e.slug, name: e.name}, saveManagerRow{
			label:   "Also sync · " + e.name,
			value:   syncthingOnOff(on[e.slug]),
			detail:  detail,
			hint:    hintLine(FooterHint{"A", "Switch"}),
			enabled: true})
	}
	web := syncthingWebGUISetting(appConfig)
	add(syncthingRowRef{kind: syncthingRowWebGUI}, saveManagerRow{
		label:   "Web GUI on this network",
		value:   syncthingOnOff(web),
		detail:  "Add any folder from a PC's browser. Same Wi-Fi only, with a password shown here: leave it off on public Wi-Fi.",
		hint:    hintLine(FooterHint{"A", "Switch"}),
		enabled: true})
	if !web || s.Phase != syncthingPhaseRunning {
		return
	}
	if pass, addr := syncthingWebLogin(); pass != "" && addr != "" {
		add(syncthingRowRef{kind: syncthingRowWebAddr}, saveManagerRow{
			label:   "Open the web GUI",
			value:   addr,
			detail:  "User " + syncthingWebUser + " · password " + pass + " · A shows the address as a QR code",
			hint:    hintLine(FooterHint{"A", "QR code"}),
			enabled: true})
	}
}

// syncthingExtraActivate is A on one of those rows.
func syncthingExtraActivate(ref syncthingRowRef, s syncthingSnapshot) {
	switch ref.kind {
	case syncthingRowExtra:
		PlayActivateSound()
		syncthingExtraToggle(ref.id, ref.name, s)
	case syncthingRowWebGUI:
		PlayActivateSound()
		syncthingWebGUIToggle(s)
	case syncthingRowWebAddr:
		pass, addr := syncthingWebLogin()
		if ip := localIPv4(); ip != "" && s.Port > 0 {
			addr = "http://" + ip + ":" + strconv.Itoa(s.Port)
		}
		if addr == "" || pass == "" {
			ShowToast("Join a Wi-Fi network first", ToastKindInfo)
			return
		}
		PlayActivateSound()
		qrZoomOpenCardDetail(tbxQRModules(addr), "Syncthing web GUI", addr+" · user "+syncthingWebUser+" · password "+pass)
	}
}

// syncthingExtraToggle switches one "Also sync" folder. Main thread.
func syncthingExtraToggle(slug, name string, s syncthingSnapshot) {
	if appConfig == nil {
		return
	}
	on := syncthingAlsoSet(appConfig)
	turnOn := !on[slug]
	on[slug] = turnOn
	var list []string
	for _, e := range syncthingExtras {
		if on[e.slug] {
			list = append(list, e.slug)
		}
	}
	if appConfig.Variables.Custom == nil {
		appConfig.Variables.Custom = map[string]interface{}{}
	}
	appConfig.Variables.Custom[syncthingAlsoKey] = strings.Join(list, ",")
	saveUserConfigDebounced(userConfigFromVariables(appConfig))
	syncthingExtrasMirror(appConfig)
	if s.Phase != syncthingPhaseRunning {
		if turnOn {
			ShowToast(name+" will sync once Syncthing runs", ToastKindSuccess)
		} else {
			ShowToast(name+" will not sync", ToastKindInfo)
		}
		return
	}
	id := syncthingExtraPrefix + slug
	syncthingPost("also", func(ctx context.Context, api syncthingAPI) (string, error) {
		if !turnOn {
			return name + " no longer syncs — the files stay", syncthingDropFolder(ctx, api, id)
		}
		if _, err := syncthingShareFolders(ctx, api, syncthingSnapshotNow().MyID); err != nil {
			return "", err
		}
		return name + " is shared with your devices", nil
	})
}

// syncthingWebGUIToggle opens or shuts the web GUI. The listen address is a
// start flag, so a running instance restarts; the worker brings it back at
// once and syncthingConfigure sets the login.
func syncthingWebGUIToggle(s syncthingSnapshot) {
	on := !syncthingWebGUISetting(appConfig)
	syncthingSetSetting(appConfig, syncthingWebGUIKey, on)
	syncthingWebGUIOn.Store(on)
	if s.Phase != syncthingPhaseRunning {
		if on {
			ShowToast("Syncthing's web GUI opens on this network when Syncthing runs", ToastKindSuccess)
		} else {
			ShowToast("Syncthing's web GUI stays shut", ToastKindInfo)
		}
		return
	}
	syncthingPost("webgui", func(ctx context.Context, api syncthingAPI) (string, error) {
		syncthingStop(3 * time.Second)
		if on {
			return "Syncthing restarts with its web GUI open on this network", nil
		}
		return "Syncthing restarts with its web GUI shut", nil
	})
}
