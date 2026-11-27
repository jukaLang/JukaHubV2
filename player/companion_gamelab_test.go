package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gameLabTestPlatform points P().DataDir at a temporary folder, so a handler
// test never reads or writes the developer's real Game Lab data.
type gameLabTestPlatform struct {
	trimuiLinuxPlatform
	dir string
}

func (p gameLabTestPlatform) DataDir() (string, error) { return p.dir, nil }

func withGameLabTestData(t *testing.T) string {
	t.Helper()
	prev := platform
	dir := t.TempDir()
	platform = gameLabTestPlatform{dir: dir}
	t.Cleanup(func() { platform = prev })
	return dir
}

func gameLabTestProfileJSON(t *testing.T) []byte {
	t.Helper()
	p := gameLabProfile{
		Schema:        gameLabProfileSchema,
		SchemaVersion: gameLabProfileVersion,
		Device:        gameLabProfileDevice{Model: "pc"},
		Game:          gameLabProfileGame{System: "GBA", Title: "Test Game"},
		Setup:         gameLabProfileSetup{Emulator: "mGBA", CPUProfile: "auto"},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func gameLabMultipart(t *testing.T, name string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("profile", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, w.FormDataContentType()
}

func TestCompanionGameLabURL(t *testing.T) {
	link := companionURL("192.168.1.23", 18080, "123456")
	got := companionGameLabURL(link)
	want := "http://192.168.1.23:18080/gamelab?p=123456"
	if got != want {
		t.Fatalf("companionGameLabURL(%q) = %q, want %q", link, got, want)
	}
	if got := companionGameLabURL("not a link"); got != "not a link" {
		t.Fatalf("unparseable link = %q, want it unchanged", got)
	}
}

func TestCompanionGameLabDownloadAndUpload(t *testing.T) {
	dir := withGameLabTestData(t)
	exportDir := filepath.Join(dir, "game-lab", "export")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := gameLabTestProfileJSON(t)
	if err := os.WriteFile(filepath.Join(exportDir, "GBA-Test Game.json"), profile, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newCompanionServer("123456", t.TempDir(), nil)
	handler := srv.handler()

	req := companionRequest(http.MethodGet, "/api/game-lab", "123456", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %s", rec.Code, rec.Body)
	}
	var list companionGameLabList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Exports) != 1 || list.Exports[0].Title != "Test Game" || list.Exports[0].System != "GBA" {
		t.Fatalf("list = %+v, want the exported profile", list)
	}

	req = companionRequest(http.MethodGet, "/api/game-lab/download?p=123456&file=GBA-Test%20Game.json", "", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Test Game"`) {
		t.Fatalf("download status = %d, body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Fatalf("download Content-Disposition = %q", got)
	}

	body, contentType := gameLabMultipart(t, "../../evil.json", profile)
	req = companionRequest(http.MethodPost, "/api/game-lab", "123456", body)
	req.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body %s", rec.Code, rec.Body)
	}
	var answer struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Name != "evil.json" {
		t.Fatalf("uploaded name = %q, want evil.json", answer.Name)
	}
	imported, err := os.ReadFile(filepath.Join(dir, "game-lab", "import", answer.Name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gameLabParseProfile(imported); err != nil {
		t.Fatalf("saved import does not parse: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "game-lab", "evil.json")); !os.IsNotExist(err) {
		t.Fatalf("a traversal upload escaped the import folder: %v", err)
	}

	// The same name again gets a suffix instead of replacing the first setup.
	body, contentType = gameLabMultipart(t, "evil.json", profile)
	req = companionRequest(http.MethodPost, "/api/game-lab", "123456", body)
	req.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second upload status = %d, body %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Name != "evil-2.json" {
		t.Fatalf("second uploaded name = %q, want evil-2.json", answer.Name)
	}
}

func TestCompanionGameLabRejectsBadUploads(t *testing.T) {
	withGameLabTestData(t)
	srv := newCompanionServer("123456", t.TempDir(), nil)
	handler := srv.handler()

	for _, tc := range []struct {
		name string
		data []byte
		want int
	}{
		{"not JSON", []byte("{"), http.StatusBadRequest},
		{"other JSON", []byte(`{"hello":"world"}`), http.StatusBadRequest},
		{"too large", bytes.Repeat([]byte("x"), gameLabImportMax+1), http.StatusRequestEntityTooLarge},
	} {
		body, contentType := gameLabMultipart(t, "profile.json", tc.data)
		req := companionRequest(http.MethodPost, "/api/game-lab", "123456", body)
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, rec.Code, tc.want, rec.Body)
		}
	}

	req := companionRequest(http.MethodGet, "/api/game-lab/download?p=123456&file=../secret.json", "", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("traversal download status = %d, want 400", rec.Code)
	}
}

func TestCompanionGameLabPage(t *testing.T) {
	srv := newCompanionServer("123456", t.TempDir(), nil)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/gamelab", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Game Lab") {
		t.Fatalf("page status = %d, body %.200s", rec.Code, rec.Body)
	}
}
