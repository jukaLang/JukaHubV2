// Package releasesign defines the signed release manifest that JukaHub's
// updater verifies before installing a new build, and the signing tool writes.
//
// It is a separate package so the app and the tool share one definition of the
// signed bytes: a manifest signed with slightly different field order would
// fail verification on every device.
package releasesign

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// Format is the manifest schema version.
	Format = 1
	// AssetName is the release asset that carries the manifest.
	AssetName = "release.json"
)

// Target is one installable archive inside a manifest.
type Target struct {
	Asset  string `json:"asset"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is release.json. The signature covers the canonical JSON of every
// field except Signature itself.
type Manifest struct {
	Format    int      `json:"format"`
	KeyID     string   `json:"key_id"`
	Tag       string   `json:"tag"`
	IssuedAt  string   `json:"issued_at,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	Targets   []Target `json:"targets"`
	Signature string   `json:"signature"`
}

// Canonical returns the signed payload: the manifest with an empty signature
// field. Field order comes from the struct, so this must never be reordered.
func Canonical(m *Manifest) ([]byte, error) {
	if m == nil {
		return nil, errors.New("empty release manifest")
	}
	clone := *m
	clone.Signature = ""
	return json.Marshal(clone)
}

// Sign fills in Signature using key.
func Sign(m *Manifest, key ed25519.PrivateKey) error {
	if m == nil {
		return errors.New("empty release manifest")
	}
	payload, err := Canonical(m)
	if err != nil {
		return err
	}
	m.Signature = hex.EncodeToString(ed25519.Sign(key, payload))
	return nil
}

// Verify checks format, key id, validity window and signature.
func Verify(m *Manifest, pub ed25519.PublicKey, keyID string, now time.Time) error {
	if m == nil {
		return errors.New("empty release manifest")
	}
	if m.Format != Format {
		return fmt.Errorf("release manifest format %d not supported", m.Format)
	}
	if m.KeyID != keyID {
		return fmt.Errorf("release manifest signed by unknown key %q", m.KeyID)
	}
	if m.IssuedAt != "" {
		if t, err := time.Parse(time.RFC3339, m.IssuedAt); err == nil && t.After(now) {
			return errors.New("release manifest issued in the future")
		}
	}
	if m.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, m.ExpiresAt); err == nil && now.After(t) {
			return errors.New("release manifest expired")
		}
	}
	if len(m.Targets) == 0 {
		return errors.New("release manifest lists no archives")
	}
	sig, err := hex.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("release manifest signature malformed")
	}
	payload, err := Canonical(m)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return errors.New("release manifest signature verification failed")
	}
	return nil
}

// IsSHA256Hex reports whether s is a complete SHA-256 in hex.
func IsSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// FindTarget returns the signed entry for asset, rejecting an entry without a
// usable hash or size: naming an archive is not the same as verifying it.
func FindTarget(m *Manifest, asset string) (Target, error) {
	if m == nil {
		return Target{}, errors.New("empty release manifest")
	}
	for _, t := range m.Targets {
		if !strings.EqualFold(t.Asset, asset) {
			continue
		}
		if !IsSHA256Hex(t.SHA256) {
			return Target{}, fmt.Errorf("release manifest has no valid sha256 for %s", asset)
		}
		if t.Size <= 0 {
			return Target{}, fmt.Errorf("release manifest has no size for %s", asset)
		}
		t.SHA256 = strings.ToLower(t.SHA256)
		return t, nil
	}
	return Target{}, fmt.Errorf("release manifest does not list %s", asset)
}
