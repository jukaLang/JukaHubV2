package main

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCastReceiverPictureFromPhone(t *testing.T) {
	srv, _ := newTestCompanion(t)
	dir := t.TempDir()
	old := castReceiverDir
	castReceiverDir = func() string { return dir }
	defer func() {
		castReceiverDir = old
		castReceiverReset()
	}()

	var buf bytes.Buffer
	pic := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	if err := png.Encode(&buf, pic); err != nil {
		t.Fatal(err)
	}
	url := "/api/cast-receiver?name=" + strings.ReplaceAll("A picture", " ", "%20")

	// No PIN: no picture on the handheld.
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, url, "", bytes.NewReader(buf.Bytes())))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without PIN = %d, want 401", rec.Code)
	}
	if open, _, _, _ := castReceiverSnapshot(); open {
		t.Fatal("a rejected POST opened the card")
	}

	rec = httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, url, "123456", bytes.NewReader(buf.Bytes())))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	// tbxPost hands the state change to the main loop; a test has no loop.
	// The saved file and the answer are the parts the endpoint owns.
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || filepath.Ext(files[0].Name()) != ".png" {
		t.Fatalf("saved files = %v, %v", files, err)
	}
	rec = httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodGet, "/cast", "", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Companion PIN") {
		t.Fatalf("phone page = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, companionRequest(http.MethodPost, url, "123456", strings.NewReader("not a picture")))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text POST = %d, want 415", rec.Code)
	}
}

func TestCastReceiverWaitingLines(t *testing.T) {
	castReceiverReset()
	castReceiverShow("", "")
	open, path, _, _ := castReceiverSnapshot()
	if !open || path != "" {
		t.Fatalf("waiting state = %v %q", open, path)
	}
	castReceiverClose()
	if open, _, _, _ := castReceiverSnapshot(); open {
		t.Fatal("the card did not close")
	}
}
