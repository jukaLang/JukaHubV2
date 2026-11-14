package main

// RomM is a separate, user-owned server. The local ROM manager in romm_ui.go
// still organises files on the card; this client fetches games, covers, BIOS
// and negotiates save changes with a RomM server.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rommServerSettings struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	DeviceID string `json:"device_id,omitempty"`
}

var rommServerStore struct {
	sync.Mutex
	loaded bool
	value  rommServerSettings
}

var rommServerSettingsPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "romm-server.json")
}

func rommServerSettingsGet() rommServerSettings {
	rommServerStore.Lock()
	defer rommServerStore.Unlock()
	if !rommServerStore.loaded {
		rommServerStore.loaded = true
		if path := rommServerSettingsPath(); path != "" {
			_ = func() error {
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				return json.Unmarshal(b, &rommServerStore.value)
			}()
		}
	}
	return rommServerStore.value
}

func rommServerSettingsSave(next rommServerSettings) error {
	if next.URL != "" {
		base, err := rommServerBase(next.URL)
		if err != nil {
			return err
		}
		next.URL = base.String()
	}
	path := rommServerSettingsPath()
	if path == "" {
		return errors.New("settings folder is unavailable")
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	rommServerStore.Lock()
	rommServerStore.value, rommServerStore.loaded = next, true
	rommServerStore.Unlock()
	return nil
}

func rommServerBase(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("enter a full http:// or https:// RomM server address")
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/api")
	u.RawPath = ""
	return u, nil
}

type rommServerClient struct {
	base  *url.URL
	token string
	http  *http.Client
}

func newRommServerClient(settings rommServerSettings) (*rommServerClient, error) {
	base, err := rommServerBase(settings.URL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(settings.Token) == "" {
		return nil, errors.New("add a RomM client API token")
	}
	return &rommServerClient{base: base, token: strings.TrimSpace(settings.Token), http: &http.Client{}}, nil
}

func (c *rommServerClient) endpoint(path string, query url.Values) string {
	u := *c.base
	u.Path = strings.TrimSuffix(c.base.Path, "/") + "/api/" + strings.TrimPrefix(path, "/")
	u.RawPath = ""
	u.RawQuery = query.Encode()
	return u.String()
}

func (c *rommServerClient) request(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("RomM returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

func (c *rommServerClient) json(ctx context.Context, method, path string, query url.Values, input, output any) error {
	var body io.Reader
	contentType := ""
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body, contentType = bytes.NewReader(b), "application/json"
	}
	resp, err := c.request(ctx, method, path, query, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if output == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(output)
}

type rommServerGame struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	FSName           string `json:"fs_name"`
	PlatformSlug     string `json:"platform_slug"`
	PlatformFS       string `json:"platform_fs_slug"`
	PlatformName     string `json:"platform_display_name"`
	CoverSmall       string `json:"path_cover_small"`
	CoverLarge       string `json:"path_cover_large"`
	HasMultipleFiles bool   `json:"has_multiple_files"`
	Missing          bool   `json:"missing_from_fs"`
	Physical         bool   `json:"is_physical"`
	Size             int64  `json:"fs_size_bytes"`
}

func (g rommServerGame) title() string {
	if g.Name != "" {
		return g.Name
	}
	return g.FSName
}

type rommServerPage struct {
	Items []rommServerGame `json:"items"`
	Total *int             `json:"total"`
}

type rommServerFirmware struct {
	ID       int    `json:"id"`
	FileName string `json:"file_name"`
	Missing  bool   `json:"missing_from_fs"`
}

func (c *rommServerClient) games(ctx context.Context, offset int) (rommServerPage, error) {
	q := url.Values{"limit": {"100"}, "offset": {strconv.Itoa(offset)}, "with_filter_values": {"false"}, "with_rom_id_index": {"false"}, "with_char_index": {"false"}}
	var page rommServerPage
	err := c.json(ctx, http.MethodGet, "roms", q, nil, &page)
	return page, err
}

func (c *rommServerClient) firmware(ctx context.Context) ([]rommServerFirmware, error) {
	var rows []rommServerFirmware
	err := c.json(ctx, http.MethodGet, "firmware", nil, nil, &rows)
	return rows, err
}

func rommServerSafeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\:`) || strings.ContainsRune(name, '\x00') {
		return ""
	}
	return name
}

func rommServerSystem(g rommServerGame) (rommSystem, bool) {
	for _, s := range rommSystems {
		for _, key := range []string{g.PlatformFS, g.PlatformSlug} {
			key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
			if key == s.ID || key == strings.ToLower(s.Folder) {
				return s, true
			}
		}
	}
	for _, key := range []string{g.PlatformFS, g.PlatformSlug} {
		key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
		switch key {
		case "genesis", "megadrive", "segagenesis", "segamegadrive":
			return rommSystemByID("md")
		case "psx", "playstation", "sonyplaystation":
			return rommSystemByID("ps1")
		case "supernintendo", "supernintendoentertainmentsystem":
			return rommSystemByID("snes")
		case "nintendoentertainmentsystem":
			return rommSystemByID("nes")
		}
	}
	switch strings.ToLower(g.PlatformSlug) {
	case "nintendo-entertainment-system":
		return rommSystemByID("nes")
	case "super-nintendo-entertainment-system":
		return rommSystemByID("snes")
	case "sega-genesis", "sega-mega-drive":
		return rommSystemByID("md")
	case "sony-playstation":
		return rommSystemByID("ps1")
	case "sony-playstation-portable":
		return rommSystemByID("psp")
	}
	return rommSystem{}, false
}

// streamFile writes a remote object into a same-directory temporary file and
// never replaces an existing local file. A failed connection leaves no partial
// game or firmware visible to scanners.
func (c *rommServerClient) streamFile(ctx context.Context, path, dest string, limit int64) error {
	if regularFile(dest) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part, err := os.CreateTemp(filepath.Dir(dest), ".romm-*")
	if err != nil {
		return err
	}
	defer os.Remove(part.Name())
	defer part.Close()
	resp, err := c.request(ctx, http.MethodGet, path, nil, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("RomM is preparing the file (HTTP %d); try again shortly", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return errors.New("file exceeds the download size limit")
	}
	n, err := io.Copy(part, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("file exceeds the download size limit")
	}
	if err := part.Sync(); err != nil {
		return err
	}
	if err := part.Close(); err != nil {
		return err
	}
	if regularFile(dest) {
		return nil
	}
	return os.Rename(part.Name(), dest)
}

func (c *rommServerClient) downloadGame(ctx context.Context, g rommServerGame, romsRoot string) (string, error) {
	if g.ID <= 0 || g.Missing || g.Physical {
		return "", errors.New("RomM has no downloadable game file")
	}
	system, ok := rommServerSystem(g)
	if !ok {
		return "", errors.New("this console has no Brick games folder")
	}
	name := rommServerSafeName(g.FSName)
	if name == "" {
		return "", errors.New("RomM returned an invalid game name")
	}
	if g.HasMultipleFiles {
		return c.downloadGameFolder(ctx, g, filepath.Join(romsRoot, system.Folder), name)
	}
	dest := filepath.Join(romsRoot, system.Folder, name)
	path := fmt.Sprintf("roms/%d/content/%s", g.ID, name)
	if err := c.streamFile(ctx, path, dest, 16<<30); err != nil {
		return "", err
	}
	return dest, nil
}

func (c *rommServerClient) downloadGameFolder(ctx context.Context, g rommServerGame, systemDir, name string) (string, error) {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == "" {
		stem = name
	}
	dest := filepath.Join(systemDir, stem)
	if dirExists(dest) {
		if primary := rommServerPrimaryGame(dest); primary != "" {
			return primary, nil
		}
		return "", errors.New("the game folder exists but has no playable file")
	}
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(systemDir, ".romm-game-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	archiveName := stem + ".zip"
	archive := filepath.Join(stage, archiveName)
	if err := c.streamFile(ctx, fmt.Sprintf("roms/%d/content/%s", g.ID, archiveName), archive, 16<<30); err != nil {
		return "", err
	}
	unpack := filepath.Join(stage, "unpack")
	if err := extractZipSafe(ctx, archive, unpack, 4096, 32<<30); err != nil {
		return "", err
	}
	source := unpack
	if entries, err := os.ReadDir(unpack); err == nil && len(entries) == 1 && entries[0].IsDir() {
		source = filepath.Join(unpack, entries[0].Name())
	}
	if rommServerPrimaryGame(source) == "" {
		return "", errors.New("RomM archive had no playable game file")
	}
	if dirExists(dest) {
		if primary := rommServerPrimaryGame(dest); primary != "" {
			return primary, nil
		}
		return "", errors.New("the game folder exists but has no playable file")
	}
	if err := os.Rename(source, dest); err != nil {
		return "", err
	}
	primary := rommServerPrimaryGame(dest)
	if primary == "" {
		return "", errors.New("RomM archive had no playable game file")
	}
	return primary, nil
}

func rommServerPrimaryGame(root string) string {
	if !dirExists(root) {
		return ""
	}
	var best string
	bestRank := 100
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		rank := 90
		switch ext {
		case ".m3u":
			rank = 0
		case ".cue", ".gdi":
			rank = 1
		case ".chd", ".iso", ".cso", ".pbp":
			rank = 2
		case ".bin", ".img":
			rank = 3
		default:
			if rommIsROMExt(ext) {
				rank = 4
			}
		}
		if rank < bestRank || (rank == bestRank && path < best) {
			best, bestRank = path, rank
		}
		return nil
	})
	if bestRank >= 90 {
		return ""
	}
	return best
}

func (c *rommServerClient) downloadFirmware(ctx context.Context, f rommServerFirmware, biosRoot string) (string, error) {
	if f.ID <= 0 || f.Missing {
		return "", errors.New("RomM has no downloadable BIOS file")
	}
	name := rommServerSafeName(f.FileName)
	if name == "" {
		return "", errors.New("RomM returned an invalid BIOS name")
	}
	dest := filepath.Join(biosRoot, name)
	return dest, c.streamFile(ctx, fmt.Sprintf("firmware/%d/content/%s", f.ID, name), dest, 256<<20)
}

func (c *rommServerClient) downloadCover(ctx context.Context, g rommServerGame, romPath, artRoot string) (string, error) {
	path := g.CoverLarge
	if path == "" {
		path = g.CoverSmall
	}
	if path == "" {
		return "", errors.New("this game has no cover on RomM")
	}
	ref, err := url.Parse(path)
	if err != nil || ref.IsAbs() || ref.Host != "" || !strings.HasPrefix(ref.Path, "/") {
		return "", errors.New("RomM returned an unsupported cover path")
	}
	u := *c.base
	u.Path = ref.Path
	if !strings.HasPrefix(ref.Path, strings.TrimSuffix(c.base.Path, "/")+"/") {
		u.Path = strings.TrimSuffix(c.base.Path, "/") + ref.Path
	}
	u.RawQuery = ref.RawQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("RomM cover returned %d", resp.StatusCode)
	}
	if resp.ContentLength > 12<<20 {
		return "", errors.New("cover is too large")
	}
	picture, _, err := image.Decode(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return "", err
	}
	system, ok := rommServerSystem(g)
	if !ok {
		return "", errors.New("this console has no Brick artwork folder")
	}
	dest := romArtworkPath(scannedROM{Path: romPath, System: system.Name}, artRoot)
	if dest == "" {
		return "", errors.New("artwork folder is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	part, err := os.CreateTemp(filepath.Dir(dest), ".romm-cover-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(part.Name())
	defer part.Close()
	if err := png.Encode(part, picture); err != nil {
		return "", err
	}
	if err := part.Close(); err != nil {
		return "", err
	}
	return dest, os.Rename(part.Name(), dest)
}

type rommServerLocalSave struct {
	RomID     int    `json:"rom_id"`
	FileName  string `json:"file_name"`
	Slot      string `json:"slot"`
	Emulator  string `json:"emulator"`
	Hash      string `json:"content_hash"`
	UpdatedAt string `json:"updated_at"`
	Size      int64  `json:"file_size_bytes"`
	path      string
}

type rommServerSyncOp struct {
	Action   string `json:"action"`
	RomID    int    `json:"rom_id"`
	SaveID   *int   `json:"save_id"`
	FileName string `json:"file_name"`
	Slot     string `json:"slot"`
	Reason   string `json:"reason"`
}

type rommServerNegotiation struct {
	SessionID int                `json:"session_id"`
	Ops       []rommServerSyncOp `json:"operations"`
}

type rommServerSyncResult struct{ Uploaded, Downloaded, Conflicts, Skipped int }

func rommServerSaveName(romPath string, op rommServerSyncOp) string {
	stem := strings.TrimSuffix(filepath.Base(romPath), filepath.Ext(romPath))
	if strings.HasPrefix(op.Slot, ".") && rommServerSafeName(stem+op.Slot) != "" {
		return stem + op.Slot
	}
	name := rommServerSafeName(op.FileName)
	if strings.HasPrefix(name, stem+".") {
		return name
	}
	return ""
}

func rommServerSaveFiles(romID int, romPath, savesDir string) ([]rommServerLocalSave, error) {
	stem := strings.TrimSuffix(filepath.Base(romPath), filepath.Ext(romPath))
	entries, err := os.ReadDir(savesDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []rommServerLocalSave
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(entry.Name(), stem+".") || len(out) >= 16 {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".srm" && ext != ".sav" && ext != ".state" && !strings.HasPrefix(ext, ".state") {
			continue
		}
		path := filepath.Join(savesDir, entry.Name())
		info, err := entry.Info()
		if err != nil || info.Size() > 128<<20 {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, file)
		file.Close()
		if err != nil {
			continue
		}
		out = append(out, rommServerLocalSave{RomID: romID, FileName: entry.Name(), Slot: strings.TrimPrefix(entry.Name(), stem), Emulator: "RetroArch", Hash: hex.EncodeToString(h.Sum(nil)), UpdatedAt: info.ModTime().UTC().Format(time.RFC3339Nano), Size: info.Size(), path: path})
	}
	return out, nil
}

func (c *rommServerClient) registerDevice(ctx context.Context, settings *rommServerSettings) error {
	if settings.DeviceID != "" {
		return nil
	}
	var response struct {
		DeviceID string `json:"device_id"`
	}
	if err := c.json(ctx, http.MethodPost, "devices", nil, map[string]any{"name": "JukaHub Brick", "platform": "trimui-brick", "client": "JukaHub", "sync_mode": "api", "allow_existing": true}, &response); err != nil {
		return err
	}
	if response.DeviceID == "" {
		return errors.New("RomM did not return a device ID")
	}
	settings.DeviceID = response.DeviceID
	return rommServerSettingsSave(*settings)
}

// syncSaves is used by single-folder clients and tests. The handheld uses
// syncSavesFromRoots so save RAM and state files keep their own directories.
func (c *rommServerClient) syncSaves(ctx context.Context, settings rommServerSettings, g rommServerGame, romPath, savesDir string) (rommServerSyncResult, error) {
	return c.syncSavesFromRoots(ctx, settings, g, romPath, []saveRoot{{Name: "RetroArch saves", Path: savesDir}})
}

func rommServerSaveDestination(name string, roots []saveRoot) string {
	want := "RetroArch saves"
	if strings.Contains(strings.ToLower(name), ".state") {
		want = "RetroArch states"
	}
	for _, root := range roots {
		if root.Name == want && root.Path != "" {
			return filepath.Join(root.Path, name)
		}
	}
	for _, root := range roots {
		if root.Path != "" {
			return filepath.Join(root.Path, name)
		}
	}
	return ""
}

// syncSavesFromRoots is a deliberate, per-game action. It uses RomM's
// negotiation, preserves local copies on replacement, and keeps conflicts.
func (c *rommServerClient) syncSavesFromRoots(ctx context.Context, settings rommServerSettings, g rommServerGame, romPath string, roots []saveRoot) (rommServerSyncResult, error) {
	var result rommServerSyncResult
	if err := c.registerDevice(ctx, &settings); err != nil {
		return result, err
	}
	byName := map[string]rommServerLocalSave{}
	for _, root := range roots {
		files, err := rommServerSaveFiles(g.ID, romPath, root.Path)
		if err != nil {
			return result, err
		}
		for _, save := range files {
			if old, ok := byName[save.FileName]; ok {
				oldTime, _ := time.Parse(time.RFC3339Nano, old.UpdatedAt)
				newTime, _ := time.Parse(time.RFC3339Nano, save.UpdatedAt)
				if !newTime.After(oldTime) {
					continue
				}
			}
			byName[save.FileName] = save
		}
	}
	local := make([]rommServerLocalSave, 0, len(byName))
	for _, save := range byName {
		local = append(local, save)
	}
	var negotiation rommServerNegotiation
	payload := map[string]any{"device_id": settings.DeviceID, "rom_ids": []int{g.ID}, "saves": local, "emulators": []string{"RetroArch"}}
	if err := c.json(ctx, http.MethodPost, "sync/negotiate", nil, payload, &negotiation); err != nil {
		return result, err
	}
	failed := 0
	for _, op := range negotiation.Ops {
		if op.RomID != g.ID {
			result.Skipped++
			continue
		}
		name := rommServerSaveName(romPath, op)
		if name == "" {
			result.Skipped++
			continue
		}
		switch op.Action {
		case "upload":
			if save, ok := byName[name]; ok {
				if err := c.uploadSave(ctx, negotiation.SessionID, save); err != nil {
					failed++
				} else {
					result.Uploaded++
				}
			} else {
				failed++
			}
		case "download", "conflict":
			if op.SaveID == nil {
				failed++
				continue
			}
			dest := rommServerSaveDestination(name, roots)
			if save, ok := byName[name]; ok {
				dest = save.path
			}
			if dest == "" {
				failed++
				continue
			}
			if op.Action == "conflict" {
				dest += ".romm-conflict"
				result.Conflicts++
			}
			if err := c.downloadSave(ctx, *op.SaveID, dest); err != nil {
				failed++
			} else {
				result.Downloaded++
			}
		case "no_op", "delete":
			result.Skipped++ // never delete a local save without a separate review
		}
	}
	if negotiation.SessionID > 0 {
		if err := c.json(ctx, http.MethodPost, fmt.Sprintf("sync/sessions/%d/complete", negotiation.SessionID), nil, map[string]int{"operations_completed": result.Uploaded + result.Downloaded, "operations_failed": failed}, nil); err != nil {
			return result, fmt.Errorf("save transfers finished but RomM could not complete the sync: %w", err)
		}
	}
	if failed > 0 {
		return result, fmt.Errorf("%d save transfers failed", failed)
	}
	return result, nil
}

func (c *rommServerClient) downloadSave(ctx context.Context, saveID int, dest string) error {
	if saveID <= 0 {
		return errors.New("invalid save ID")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	// Fetch and finish the new copy before touching an existing local save.
	staged := dest + ".romm-new"
	_ = os.Remove(staged)
	defer os.Remove(staged)
	if err := c.streamFile(ctx, fmt.Sprintf("saves/%d/content", saveID), staged, 128<<20); err != nil {
		return err
	}
	backup := ""
	if regularFile(dest) {
		backup = dest + ".romm-backup-" + time.Now().UTC().Format("20060102T150405.000000000")
		if err := os.Rename(dest, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(staged, dest); err != nil {
		if backup != "" {
			_ = os.Rename(backup, dest)
		}
		return err
	}
	return nil
}

func (c *rommServerClient) uploadSave(ctx context.Context, session int, save rommServerLocalSave) error {
	file, err := os.Open(save.path)
	if err != nil {
		return err
	}
	defer file.Close()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("saveFile", save.FileName)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, io.LimitReader(file, 128<<20)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	q := url.Values{"rom_id": {strconv.Itoa(save.RomID)}, "slot": {save.Slot}, "emulator": {save.Emulator}, "content_hash": {save.Hash}, "session_id": {strconv.Itoa(session)}}
	resp, err := c.request(ctx, http.MethodPost, "saves", q, &body, w.FormDataContentType())
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
