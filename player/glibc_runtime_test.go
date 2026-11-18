package main

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateGlibcPatchGreenOvercast(t *testing.T) {
	script := "#!/bin/bash\n" +
		"pm_platform_helper \"$GAMEDIR/webrtc_stream.aarch64\"\n" +
		"  " + greenOvercastExecLine + "\n" +
		"status=$?\n"
	patched, err := privateGlibcPatchGreenOvercast([]byte(script))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patched), "  "+greenOvercastPrivateExecLine+"\nstatus=$?") ||
		!strings.Contains(string(patched), "pm_platform_helper \"$GAMEDIR/webrtc_stream.aarch64\"") {
		t.Fatalf("launcher setup or game command changed unexpectedly:\n%s", patched)
	}
	if _, err := privateGlibcPatchGreenOvercast([]byte("#!/bin/bash\n")); err == nil {
		t.Fatal("changed upstream launcher should be rejected")
	}
}

func TestPrivateGlibcCatalogOnlyGreenOvercast(t *testing.T) {
	e := mcCatalogEntry{Zip: goZipName, MinGlibc: "2.38"}
	if !privateGlibcCatalogEligible(e, DeviceTrimuiSmartPro) {
		t.Fatal("the tested GreenOvercast build should be offered on TrimUI")
	}
	e.MinGlibc = "2.40"
	if privateGlibcCatalogEligible(e, DeviceTrimuiSmartPro) {
		t.Fatal("the private loader cannot satisfy glibc 2.40")
	}
	e.MinGlibc = "2.38"
	e.Zip = "another-port.zip"
	if privateGlibcCatalogEligible(e, DeviceTrimuiSmartPro) {
		t.Fatal("unverified ports must use normal compatibility rules")
	}
}

func TestPrivateGlibcExtractRejectsTraversal(t *testing.T) {
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	payload := []byte("bad")
	if err := tw.WriteHeader(&tar.Header{
		Name: privateGlibcTree + "../../escape", Typeflag: tar.TypeReg,
		Mode: 0o644, Size: int64(len(payload)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := privateGlibcExtractTar(tar.NewReader(&data), t.TempDir()); err == nil {
		t.Fatal("tar traversal should be rejected")
	}
}

// This optional test runs against the exact official package used for the
// device probe without adding a multi-megabyte fixture to the repository.
func TestPrivateGlibcExtractOfficialPackage(t *testing.T) {
	deb := os.Getenv("JUKAHUB_GLIBC_DEB")
	if deb == "" {
		t.Skip("set JUKAHUB_GLIBC_DEB to the pinned Ubuntu arm64 package")
	}
	sum, err := privateGlibcFileSHA(deb)
	if err != nil || sum != privateGlibcSHA {
		t.Fatalf("package checksum: %s, %v", sum, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, deb)
	}))
	defer server.Close()
	downloaded := filepath.Join(t.TempDir(), privateGlibcPackage)
	if err := privateGlibcFetch(context.Background(), downloaded, server.URL); err != nil {
		t.Fatalf("checked download: %v", err)
	}
	stage := t.TempDir()
	if err := privateGlibcExtractDeb(downloaded, stage); err != nil {
		t.Fatal(err)
	}
	for _, name := range privateGlibcRequired {
		if !regularFile(filepath.Join(stage, name)) {
			t.Fatalf("missing %s", name)
		}
	}
}
