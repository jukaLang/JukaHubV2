package main

// release_sign_test.go pins how the updater uses a signed release manifest:
// a release without one falls back to the GitHub digest (and says so), while a
// manifest claiming to be JukaHub's but signed by another key is rejected.
// Fetching is not exercised here — the transport only runs when the release
// lists a manifest asset, and real releases are covered by the opt-in
// integration test.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"jukahub/JukaHub/internal/releasesign"
)

func TestSignedReleaseTargetWithoutManifest(t *testing.T) {
	rel := &githubRelease{
		TagName: "JukaHub-20261020154603",
		Assets: []githubAsset{
			{Name: "JukaHub-Windows.zip", Size: 10},
			{Name: "JukaHub-Trimui.zip", Size: 10},
		},
	}
	sha, size, err := signedReleaseTarget(context.Background(), nil, rel, "JukaHub-Windows.zip")
	if !errors.Is(err, errNoReleaseManifest) {
		t.Fatalf("err = %v, want errNoReleaseManifest", err)
	}
	if sha != "" || size != 0 {
		t.Fatalf("unsigned release produced sha=%q size=%d", sha, size)
	}
}

func TestVerifyReleaseManifestUsesTheEmbeddedKey(t *testing.T) {
	now := time.Now().UTC()
	_, otherKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := &releasesign.Manifest{
		Format:   releasesign.Format,
		KeyID:    patchRepoKeyID,
		Tag:      "JukaHub-20261020154603",
		IssuedAt: now.Add(-time.Hour).Format(time.RFC3339),
		Targets: []releasesign.Target{{
			Asset:  "JukaHub-Windows.zip",
			SHA256: strings.Repeat("a", 64),
			Size:   1024,
		}},
	}
	if err := releasesign.Sign(m, otherKey); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseManifest(m); err == nil {
		t.Fatal("a manifest signed by another key was accepted")
	}

	// The same manifest under a different key id is rejected before the
	// signature is even tried: only JukaHub's Patch key signs releases.
	m.KeyID = "jukahub-release-dev"
	if err := verifyReleaseManifest(m); err == nil {
		t.Fatal("a manifest with an unknown key id was accepted")
	}
}
