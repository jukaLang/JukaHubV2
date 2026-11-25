package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRommServerAddressAndNames(t *testing.T) {
	u, err := rommServerBase("https://example.org/romm/api/")
	if err != nil || u.String() != "https://example.org/romm" {
		t.Fatalf("base = %v, %v", u, err)
	}
	for _, bad := range []string{"", "ftp://example.org", "https://user:pass@example.org", "https://example.org/?token=secret"} {
		if _, err := rommServerBase(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, bad := range []string{"../game.gba", `C:\\game.gba`, "game/part.gba", ".."} {
		if rommServerSafeName(bad) != "" {
			t.Errorf("accepted filename %q", bad)
		}
	}
}

func TestRommServerTokenEntryIsSecret(t *testing.T) {
	oldTarget, oldEdit := vkTarget, rommCloud.editToken
	t.Cleanup(func() { vkTarget, rommCloud.editToken = oldTarget, oldEdit })
	vkTarget, rommCloud.editToken = vkTargetRommServer, true
	if !vkSecretTarget() || vkStripModeNow() != vkStripHidden {
		t.Fatal("RomM token input exposed its text or suggestions")
	}
	rommCloud.editToken = false
	if vkSecretTarget() {
		t.Fatal("RomM address input was treated as a secret")
	}
}

func TestRommServerFetchGameCoverAndBIOS(t *testing.T) {
	var imageData bytes.Buffer
	imageFile := image.NewRGBA(image.Rect(0, 0, 2, 2))
	imageFile.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&imageData, imageFile); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rmm_test" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/romm/api/roms":
			_, _ = io.WriteString(w, `{"items":[{"id":7,"fs_name":"Game (USA).gba","platform_fs_slug":"gba","platform_display_name":"Game Boy Advance","path_cover_large":"/assets/cover.png"}],"total":1}`)
		case "/romm/api/roms/7/content/Game (USA).gba":
			_, _ = io.WriteString(w, "game bytes")
		case "/romm/assets/cover.png":
			_, _ = w.Write(imageData.Bytes())
		case "/romm/api/firmware":
			_, _ = io.WriteString(w, `[{"id":3,"file_name":"scph1001.bin"}]`)
		case "/romm/api/firmware/3/content/scph1001.bin":
			_, _ = io.WriteString(w, "bios bytes")
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := newRommServerClient(rommServerSettings{URL: server.URL + "/romm/api/", Token: "rmm_test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	page, err := client.games(ctx, 0)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("games: %+v, %v", page, err)
	}
	root := t.TempDir()
	romPath, err := client.downloadGame(ctx, page.Items[0], filepath.Join(root, "Roms"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(romPath); err != nil || string(b) != "game bytes" {
		t.Fatalf("game = %q, %v", b, err)
	}
	cover, err := client.downloadCover(ctx, page.Items[0], romPath, filepath.Join(root, "art"))
	if err != nil {
		t.Fatal(err)
	}
	if !artworkCoverPresent(cover) {
		t.Fatal("RomM cover was not saved as a library PNG")
	}
	firmware, err := client.firmware(ctx)
	if err != nil || len(firmware) != 1 {
		t.Fatalf("firmware = %+v, %v", firmware, err)
	}
	biosPath, err := client.downloadFirmware(ctx, firmware[0], filepath.Join(root, "BIOS"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(biosPath); err != nil || string(b) != "bios bytes" {
		t.Fatalf("bios = %q, %v", b, err)
	}
}

func TestRommServerSaveNegotiationPreservesLocalCopy(t *testing.T) {
	settingsFile := filepath.Join(t.TempDir(), "romm-server.json")
	oldPath := rommServerSettingsPath
	rommServerSettingsPath = func() string { return settingsFile }
	t.Cleanup(func() { rommServerSettingsPath = oldPath })
	saves := t.TempDir()
	oldSave := filepath.Join(saves, "Game.srm")
	if err := os.WriteFile(oldSave, []byte("old save"), 0o644); err != nil {
		t.Fatal(err)
	}
	var uploaded, completed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/devices":
			_, _ = io.WriteString(w, `{"device_id":"device-1"}`)
		case "/api/sync/negotiate":
			var payload struct {
				DeviceID string                `json:"device_id"`
				Saves    []rommServerLocalSave `json:"saves"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.DeviceID != "device-1" || len(payload.Saves) != 1 {
				t.Errorf("negotiate = %+v, %v", payload, err)
			}
			_, _ = io.WriteString(w, `{"session_id":9,"operations":[{"action":"download","rom_id":7,"save_id":4,"file_name":"Game.srm"}]}`)
		case "/api/saves/4/content":
			_, _ = io.WriteString(w, "new save")
		case "/api/saves/4/downloaded":
			_, _ = io.WriteString(w, `{}`)
		case "/api/sync/sessions/9/complete":
			completed = true
			_, _ = io.WriteString(w, `{}`)
		case "/api/saves":
			uploaded = true
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	settings := rommServerSettings{URL: server.URL, Token: "rmm_test"}
	client, err := newRommServerClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.syncSaves(context.Background(), settings, rommServerGame{ID: 7}, filepath.Join(t.TempDir(), "Game.gba"), saves)
	if err != nil || result.Downloaded != 1 || !completed || uploaded {
		t.Fatalf("sync = %+v, completed=%v uploaded=%v err=%v", result, completed, uploaded, err)
	}
	if b, _ := os.ReadFile(oldSave); string(b) != "new save" {
		t.Fatalf("downloaded save = %q", b)
	}
	entries, err := os.ReadDir(saves)
	if err != nil {
		t.Fatal(err)
	}
	foundBackup := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "Game.srm.romm-backup-") {
			b, _ := os.ReadFile(filepath.Join(saves, entry.Name()))
			foundBackup = string(b) == "old save"
		}
	}
	if !foundBackup {
		t.Fatal("local save backup is missing")
	}
}

func TestRommServerImportsMultiFileGameTogether(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, content := range map[string]string{"Disc/Disc.cue": "FILE Disc.bin BINARY", "Disc/Disc.bin": "disc data"} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(entry, content)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/roms/8/content/Disc.zip" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archive.Bytes())
	}))
	defer server.Close()
	client, err := newRommServerClient(rommServerSettings{URL: server.URL, Token: "rmm_test"})
	if err != nil {
		t.Fatal(err)
	}
	path, err := client.downloadGame(context.Background(), rommServerGame{ID: 8, FSName: "Disc", PlatformFS: "ps", HasMultipleFiles: true}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "Disc.cue" || !regularFile(filepath.Join(filepath.Dir(path), "Disc.bin")) {
		t.Fatalf("multi-file import selected %s without its track", path)
	}
}

func TestRommServerSyncRoutesSaveRAMAndStates(t *testing.T) {
	settingsFile := filepath.Join(t.TempDir(), "romm-server.json")
	oldPath := rommServerSettingsPath
	rommServerSettingsPath = func() string { return settingsFile }
	t.Cleanup(func() { rommServerSettingsPath = oldPath })
	root := t.TempDir()
	saves := filepath.Join(root, "saves")
	states := filepath.Join(root, "states")
	if err := os.MkdirAll(saves, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(states, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saves, "Game.srm"), []byte("local RAM"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(states, "Game.state1"), []byte("local state"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/devices":
			_, _ = io.WriteString(w, `{"device_id":"device-2"}`)
		case "/api/sync/negotiate":
			var payload struct {
				Saves []rommServerLocalSave `json:"saves"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Saves) != 2 {
				t.Errorf("expected save RAM and state in negotiation: %+v, %v", payload, err)
			}
			_, _ = io.WriteString(w, `{"session_id":10,"operations":[{"action":"download","rom_id":7,"save_id":11,"file_name":"Game.srm"},{"action":"download","rom_id":7,"save_id":12,"file_name":"Game.state1"},{"action":"download","rom_id":8,"save_id":13,"file_name":"Game.srm"}]}`)
		case "/api/saves/11/content":
			_, _ = io.WriteString(w, "server RAM")
		case "/api/saves/12/content":
			_, _ = io.WriteString(w, "server state")
		case "/api/saves/11/downloaded", "/api/saves/12/downloaded":
			_, _ = io.WriteString(w, `{}`)
		case "/api/sync/sessions/10/complete":
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	settings := rommServerSettings{URL: server.URL, Token: "rmm_test"}
	client, err := newRommServerClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.syncSavesFromRoots(context.Background(), settings, rommServerGame{ID: 7}, filepath.Join(root, "Game.gba"), []saveRoot{{Name: "RetroArch saves", Path: saves}, {Name: "RetroArch states", Path: states}})
	if err != nil || result.Downloaded != 2 || result.Skipped != 1 {
		t.Fatalf("sync result = %+v, %v", result, err)
	}
	for path, want := range map[string]string{filepath.Join(saves, "Game.srm"): "server RAM", filepath.Join(states, "Game.state1"): "server state"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", path, got, err)
		}
	}
}

// RomM's ROM JSON (hashes for auto-sync matching), the auto-sync links file
// (damaged input starts empty) and the save names sync may write.
func TestRommServerParsesROMHashesAndAutoSyncIndex(t *testing.T) {
	var page rommServerPage
	raw := `{"items":[{"id":12,"fs_name":"Game (USA).gba","platform_id":3,"fs_size_bytes":4194304,"crc_hash":"0a1b2c3d","md5_hash":"9e107d9d372bb6826bd81d3542a419d6","sha1_hash":null}],"total":1}`
	if err := json.Unmarshal([]byte(raw), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if g := page.Items[0]; g.PlatformID != 3 || g.Size != 4194304 || g.CRC != "0a1b2c3d" || g.MD5 == "" || g.SHA1 != "" {
		t.Fatalf("rom = %+v", g)
	}
	for _, bad := range []string{"", "{", `{"games":[1,2]}`, `{"games":{"a":{"rom_id":0,"server":"s"},"b":{"rom_id":5}}}`} {
		idx, _ := rommAutoSyncParseIndex([]byte(bad))
		if idx.Games == nil || idx.Misses == nil || len(idx.Games) != 0 {
			t.Errorf("index from %q = %+v", bad, idx)
		}
	}
	idx, err := rommAutoSyncParseIndex([]byte(`{"version":1,"games":{"/r/gba/a.gba":{"rom_id":5,"size":9,"server":"https://romm.example"}},"misses":{"/r/gba/b.gba":{"server":"https://romm.example","size":3,"at":1}}}`))
	if err != nil || idx.Games["/r/gba/a.gba"].RomID != 5 || len(idx.Misses) != 1 {
		t.Fatalf("index = %+v, %v", idx, err)
	}
	for rest, ok := range map[string]bool{".srm": true, ".state": true, ".state3": true, ".state.auto": true, ".SAV": true,
		".srm.romm-backup-20261117T120000.000000000": false, ".srm.romm-conflict": false, ".Bros.srm": false, ".gba": false, ".state1234": false} {
		if rommServerSaveSlotOK(rest) != ok {
			t.Errorf("save name %q accepted=%v", rest, !ok)
		}
	}
}

// TestSyncPhonePairInputs covers what the phone's Sync page sends — RomM's
// pairing code or link and a Syncthing device ID — and RomM's answer to the
// code exchange (companion_sync.go).
func TestSyncPhonePairInputs(t *testing.T) {
	for _, c := range []struct{ url, code, server, want string }{
		{"romm.local:8080", "abcd-1234", "http://romm.local:8080", "abcd-1234"},
		{"", "https://romm.example.com/pair?code=ABCD-1234", "https://romm.example.com", "ABCD-1234"},
		{"https://h.example/romm/pair?code=X1Y2Z3W4", "", "https://h.example/romm", "X1Y2Z3W4"},
		{"https://h.example/#/pair?code=Q1W2E3R4", "", "https://h.example", "Q1W2E3R4"},
	} {
		server, code := rommPairInput(c.url, c.code)
		if server != c.server || code != c.want {
			t.Errorf("rommPairInput(%q, %q) = %q, %q; want %q, %q", c.url, c.code, server, code, c.server, c.want)
		}
	}
	for raw, want := range map[string]string{"abcd-1234": "ABCD1234", "12 34 56 78": "12345678", "abc": "", "abcd_1234": ""} {
		got, ok := rommCleanPairCode(raw)
		if (want == "") == ok || (ok && got != want) {
			t.Errorf("rommCleanPairCode(%q) = %q, %v", raw, got, ok)
		}
	}
	id := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	for raw, ok := range map[string]bool{strings.ToLower(id): true, strings.ReplaceAll(id, "-", ""): true, id[:20]: false, strings.Replace(id, "A", "1", 1): false} {
		got, good := syncthingCleanID(raw)
		if good != ok || (ok && got != id) {
			t.Errorf("syncthingCleanID(%q) = %q, %v", raw, got, good)
		}
	}
	token := "rmm_" + strings.Repeat("a", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.Method != http.MethodPost || r.URL.Path != "/romm/api/client-tokens/exchange" || r.Header.Get("Authorization") != "":
			http.Error(w, "wrong call", http.StatusBadRequest)
		case body.Code == "GOOD1234":
			_ = json.NewEncoder(w).Encode(map[string]any{"raw_token": token, "scopes": []string{"roms.read"}})
		default:
			http.Error(w, `{"detail":"not found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	base, err := rommServerBase(srv.URL + "/romm")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rommExchangePairCode(context.Background(), base, "GOOD1234"); err != nil || got != token {
		t.Errorf("exchange = %q, %v", got, err)
	}
	if _, err := rommExchangePairCode(context.Background(), base, "BAD12345"); err == nil || !strings.Contains(err.Error(), "works once") {
		t.Errorf("unknown code: %v", err)
	}
}
