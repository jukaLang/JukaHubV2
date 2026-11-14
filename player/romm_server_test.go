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
