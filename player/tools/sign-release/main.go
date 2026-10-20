// Command sign-release writes the signed release.json that JukaHub's updater
// verifies before installing a new build. Upload it beside the archives:
//
//	PATCH_SIGN_KEY=<hex Ed25519 private key> \
//	  go run ./tools/sign-release -tag JukaHub-20261020154603 \
//	    -asset JukaHub-Windows.zip=dist/JukaHub-Windows.zip \
//	    -asset JukaHub-Trimui.zip=dist/JukaHub-Trimui.zip \
//	    -out dist/release.json
//
// The private key is the same one the Patch repository tooling uses; it never
// goes in the app or the repository. The embedded public key in
// player/patch_signed.go verifies both.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jukahub/JukaHub/internal/releasesign"
)

// assetFlag collects repeated -asset name=path flags.
type assetFlag []releasesign.Target

func (a *assetFlag) String() string { return fmt.Sprint(*a) }

func (a *assetFlag) Set(v string) error {
	name, path, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(path) == "" {
		return fmt.Errorf("want name=path, got %q", v)
	}
	digest, size, err := hashFile(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	*a = append(*a, releasesign.Target{Asset: name, SHA256: digest, Size: size})
	return nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "sign-release:", msg)
	os.Exit(1)
}

func main() {
	if err := run(os.Args[1:], os.Getenv("PATCH_SIGN_KEY")); err != nil {
		fatal(err.Error())
	}
}

// run is main without the process exit, so the tool can be tested end to end.
func run(args []string, privHex string) error {
	fs := flag.NewFlagSet("sign-release", flag.ContinueOnError)
	tag := fs.String("tag", "", "release tag the manifest describes (required)")
	out := fs.String("out", "release.json", "output path")
	keyID := fs.String("key-id", "jukahub-patch-dev-2026", "signing key id, as embedded in the app")
	expiresIn := fs.Duration("expires", 365*24*time.Hour, "manifest validity window")
	var assets assetFlag
	fs.Var(&assets, "asset", "asset name=path to sign (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*tag) == "" {
		return fmt.Errorf("-tag is required")
	}
	if len(assets) == 0 {
		return fmt.Errorf("at least one -asset name=path is required")
	}
	priv, err := hex.DecodeString(strings.TrimSpace(privHex))
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("PATCH_SIGN_KEY must be a 64-byte hex Ed25519 private key")
	}

	now := time.Now().UTC()
	m := releasesign.Manifest{
		Format:    releasesign.Format,
		KeyID:     *keyID,
		Tag:       *tag,
		IssuedAt:  now.Format(time.RFC3339),
		ExpiresAt: now.Add(*expiresIn).Format(time.RFC3339),
		Targets:   assets,
	}
	if err := releasesign.Sign(&m, ed25519.PrivateKey(priv)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(*out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s for %s with %d signed archive(s)\n", *out, *tag, len(assets))
	for _, t := range assets {
		fmt.Printf("  %-24s %s  %d bytes\n", t.Asset, t.SHA256, t.Size)
	}
	return nil
}
