package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Browser → Forget has to take the .bak with it. The durable save keeps the
// previous session beside the file, so deleting only the live file leaves the
// cookies a damaged read restores a moment later: the exact opposite of what
// the chip says it did.
func TestBrowserCookieForgetTakesTheBackup(t *testing.T) {
	cases := []struct {
		name  string
		files []string // suffixes written before the forget
	}{
		{"session and its backup", []string{"", ".bak"}},
		{"backup left on its own", []string{".bak"}},
		{"nothing to forget", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "browser-cookies.json")
			for _, suffix := range tc.files {
				if err := os.WriteFile(path+suffix, []byte(`{"schemaVersion":1,"cookies":[]}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := removeBrowserCookieFile(path); err != nil {
				t.Fatalf("forget: %v", err)
			}
			for _, suffix := range []string{"", ".bak"} {
				if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
					t.Errorf("%s survived the forget: %v", filepath.Base(path+suffix), err)
				}
			}
		})
	}
}

// The same rule one layer up: a jar that is forgotten writes nothing, so a page
// load still on the wire cannot put the session back on the card.
func TestBrowserCookieJarForgetStopsLaterSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-cookies.json")
	j := newBrowserCookieJar(path)
	if j == nil {
		t.Fatal("no cookie jar")
	}
	j.restore([]browserCookieRecord{{
		Host: "example.com", Path: "/", Name: "session", Value: "abc",
	}})
	j.save()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the save wrote nothing: %v", err)
	}
	j.mu.Lock()
	j.forgotten = true
	j.mu.Unlock()
	if err := removeBrowserCookieFile(path); err != nil {
		t.Fatal(err)
	}
	// A response that lands after the forget still calls save.
	j.save()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a forgotten jar wrote the session back: %v", err)
	}
}
