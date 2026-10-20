package main

// release_sign.go verifies the signed description of a published JukaHub
// release. The GitHub asset digest proves the download was not corrupted, but a
// digest served by the same account that uploaded the archive cannot prove who
// published it. release.json carries the archive hashes and is signed with
// JukaHub's own Ed25519 key, using the same key, key id and canonical-JSON
// scheme as the Patch repository index (patch_signed.go): one key to protect,
// one format to reason about.
//
// The updater treats a release without release.json as unsigned — older
// releases and the URL fallback have no manifest — and then relies on the
// GitHub digest. A manifest that is present but does not verify is fatal for
// that install: a bad signature is never downgraded to "no signature".

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"jukahub/JukaHub/internal/releasesign"
)

// errNoReleaseManifest means the release simply does not publish a signed
// manifest, which is allowed but never silent (the caller logs the downgrade).
var errNoReleaseManifest = errors.New("release has no signed manifest")

// verifyReleaseManifest checks a manifest against the embedded Patch key and
// the device clock.
func verifyReleaseManifest(m *releasesign.Manifest) error {
	return releasesign.Verify(m, ed25519.PublicKey(patchRepoPublicKey), patchRepoKeyID, time.Now())
}

// signedReleaseTarget fetches and verifies the release's signed manifest, when
// it publishes one, and returns the hash and size it states for asset. It
// returns errNoReleaseManifest (unwrapped) when the release has no manifest, so
// the caller can decide to fall back to the GitHub digest.
func signedReleaseTarget(ctx context.Context, client *http.Client, rel *githubRelease, asset string) (string, int64, error) {
	if rel == nil {
		return "", 0, errNoReleaseManifest
	}
	manifestURL := ""
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, releasesign.AssetName) {
			manifestURL = a.BrowserDownloadURL
			break
		}
	}
	if manifestURL == "" {
		return "", 0, errNoReleaseManifest
	}
	data, err := fetchBounded(ctx, client, manifestURL, patchMaxMetadata)
	if err != nil {
		return "", 0, fmt.Errorf("fetching signed release manifest: %w", err)
	}
	var m releasesign.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return "", 0, fmt.Errorf("parsing signed release manifest: %w", err)
	}
	if err := verifyReleaseManifest(&m); err != nil {
		return "", 0, err
	}
	if m.Tag != "" && m.Tag != rel.TagName {
		return "", 0, fmt.Errorf("release manifest is for %s, not %s", m.Tag, rel.TagName)
	}
	target, err := releasesign.FindTarget(&m, asset)
	if err != nil {
		return "", 0, err
	}
	return target.SHA256, target.Size, nil
}
