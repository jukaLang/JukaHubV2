package main

import (
	"path/filepath"
	"testing"
)

func TestSafeAbsolutePathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if got, err := safeAbsolutePath(filepath.Join(root, "state")); err != nil || got == "" {
		t.Fatalf("safe absolute path rejected: %q %v", got, err)
	}
	if _, err := safeAbsolutePath("relative/path"); err == nil {
		t.Fatal("relative path was accepted")
	}
	if _, err := safeAbsolutePath(root + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside"); err == nil {
		t.Fatal("traversal path was accepted")
	}
}

func TestPartyRejectsPublicHostsAndUnsafeURLs(t *testing.T) {
	if _, err := partyNormalizeBase("example.com:8318"); err == nil {
		t.Fatal("a public host was accepted as a party host")
	}
	if got, err := partyNormalizeBase("192.168.1.5:8318"); err != nil || got != "http://192.168.1.5:8318" {
		t.Fatalf("LAN host was rejected: %q %v", got, err)
	}
	if _, err := partySafeRequestURL("http://192.168.1.5:8318/api/party/state?code=ABCD2&member=x"); err != nil {
		t.Fatalf("safe party request was rejected: %v", err)
	}
	if _, err := partySafeRequestURL("http://example.com/api/party/state"); err == nil {
		t.Fatal("public party request was accepted")
	}
	if _, err := partySafeRequestURL("http://192.168.1.5:8318/other"); err == nil {
		t.Fatal("non-party path was accepted")
	}
}
