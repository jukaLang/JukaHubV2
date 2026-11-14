package main

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortCatalogMetadataAndBrickFilter(t *testing.T) {
	raw := []byte(`{"ports":{"a.zip":{"attr":{"title":"A","desc":"A game","arch":["aarch64"],"runtime":["frt.squashfs"],"reqs":["analog_2"],"min_glibc":"2.17"},"items":["A.sh"],"source":{"md5":"` + strings.Repeat("a", 32) + `"}},"b.zip":{"attr":{"title":"B","arch":["aarch64"],"reqs":[]}}}}`)
	entries := mcParsePortsCatalog(raw)
	if len(entries) != 2 {
		t.Fatalf("parsed %d ports", len(entries))
	}
	a := entries[0]
	if a.Title != "A" || a.Description != "A game" || len(a.Runtime) != 1 || a.Runtime[0] != "frt.squashfs" || len(a.Items) != 1 {
		t.Fatalf("catalogue metadata lost: %+v", a)
	}
	if portCatalogMayRun(a, DeviceTrimuiBrick, "GLIBC_2.23") {
		t.Fatal("a two-stick port passed the Brick filter")
	}
	if !portCatalogMayRun(a, DeviceTrimuiSmartPro, "GLIBC_2.23") {
		t.Fatal("a compatible two-stick port was hidden on Smart Pro")
	}
	if !portCatalogMayRun(entries[1], DeviceTrimuiBrick, "GLIBC_2.23") {
		t.Fatal("a compatible port was hidden on Brick")
	}
	if !portCatalogIsInstalled(a, map[string]bool{"a": true}) {
		t.Fatal("catalogue launcher was not matched to the installed port")
	}
}

func TestPortCatalogRejectsNewerGlibcAndIncompatibleArch(t *testing.T) {
	for _, entry := range []mcCatalogEntry{
		{Arch: []string{"armhf"}},
		{Arch: []string{"aarch64"}, MinGlibc: "2.34"},
		{Arch: []string{"aarch64"}, Requirements: []string{"!trimui"}},
	} {
		if portCatalogMayRun(entry, DeviceTrimuiBrick, "GLIBC_2.23") {
			t.Fatalf("incompatible port passed: %+v", entry)
		}
	}
}

func TestPortCatalogQueuesOnlyVerifiedArchive(t *testing.T) {
	body := []byte(strings.Repeat("PortMaster archive", 512))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	inbox := t.TempDir()
	good := mcCatalogEntry{Zip: "good.zip", URL: server.URL, MD5: fmt.Sprintf("%x", md5.Sum(body)), Size: int64(len(body))}
	path, err := portCatalogDownload(context.Background(), 0, good, inbox)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(body) {
		t.Fatalf("queued archive = %q, %v", data, err)
	}
	bad := mcCatalogEntry{Zip: "bad.zip", URL: server.URL, MD5: strings.Repeat("0", 32)}
	if _, err := portCatalogDownload(context.Background(), 0, bad, inbox); err == nil {
		t.Fatal("accepted an archive with the wrong checksum")
	}
	if _, err := os.Stat(filepath.Join(inbox, bad.Zip)); !os.IsNotExist(err) {
		t.Fatalf("bad archive was queued: %v", err)
	}
}
