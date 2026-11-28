package main

// TEST-18 — the Box64 source record is exact and checkable.
//
// The build pins a commit, and the comment above it records where the release
// tarball came from and what AUR publishes for it. A reader must be able to tie
// the comment to the pin without guessing: the 40-hex commit it names has to be
// BOX64_COMMIT itself. The digest and the tarball tree were checked against the
// AUR PKGBUILD and a checkout of the pinned commit; this test keeps the record
// from drifting.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestBox64SourceRecord(t *testing.T) {
	b, err := os.ReadFile("native/build-runtimes.sh")
	if err != nil {
		t.Fatalf("read build-runtimes.sh: %v", err)
	}
	src := string(b)
	value := func(key string) string {
		marker := key + `="${` + key + `:-`
		at := strings.Index(src, marker)
		if at < 0 {
			t.Fatalf("%s is not defined in build-runtimes.sh", key)
		}
		rest := src[at+len(marker):]
		end := strings.Index(rest, `}"`)
		if end < 0 {
			t.Fatalf("%s has no closing quote", key)
		}
		return rest[:end]
	}
	tag, version, commit := value("BOX64_TAG"), value("BOX64_VERSION"), value("BOX64_COMMIT")
	if tag != "v"+version {
		t.Errorf("BOX64_TAG is %q, want v%s", tag, version)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		t.Fatalf("BOX64_COMMIT is %q, not a full 40-hex commit", commit)
	}
	at := strings.Index(src, "\n# Box64,")
	end := strings.Index(src, "\nBOX64_COMMIT=")
	if at < 0 || end < 0 || at >= end {
		t.Fatal("the Box64 source comment above BOX64_COMMIT is missing")
	}
	comment := src[at:end]
	if !strings.Contains(comment, commit) {
		t.Errorf("the source comment does not name the pinned commit %s", commit)
	}
	if !strings.Contains(comment, "aur.archlinux.org") || !strings.Contains(comment, "box64 "+version+"-1") {
		t.Error("the source comment does not name the AUR package and version")
	}
	if !strings.Contains(comment, "v"+version+".tar.gz") {
		t.Errorf("the source comment does not name the v%s release tarball", version)
	}
	if !regexp.MustCompile(`[0-9a-f]{64}`).MatchString(comment) {
		t.Error("the source comment has no SHA-256 digest")
	}
}
