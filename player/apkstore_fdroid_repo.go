package main

// F-Droid's Android client cannot run on stock TrimUI. This source reads its
// signed repository index so JukaHub can browse and download APKs directly.
// A downloaded APK still passes AndroidRT's native-entry check before Games
// receives a profile. The large upstream index is streamed, then condensed to
// a small local list for browsing on a 1 GB handheld.

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/openpgp"
)

//go:embed fdroid-repo-key.asc
var fdroidRepoKey []byte

const (
	fdroidBaseURL      = "https://f-droid.org/repo"
	fdroidRepoKeyFP    = "37d2c98789d8311948394e3e41e7044e1dba2e89"
	fdroidIndexMax     = int64(100 << 20)
	fdroidCacheMax     = int64(8 << 20)
	fdroidIndexEvery   = 24 * time.Hour
	fdroidIndexTimeout = 30 * time.Second
	fdroidMaxApps      = 10000
)

var fdroidAPKNamePattern = regexp.MustCompile(`^/[A-Za-z0-9._+-]+\.apk$`)
var fdroidPackagePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{1,199}$`)

type fdroidApp struct {
	Package     string   `json:"package"`
	Name        string   `json:"name"`
	Summary     string   `json:"summary"`
	Categories  []string `json:"categories,omitempty"`
	Version     string   `json:"version"`
	Channel     string   `json:"channel,omitempty"`
	VersionCode int64    `json:"versionCode"`
	APKName     string   `json:"apkName"`
	SHA256      string   `json:"sha256"`
	Size        int64    `json:"size"`
	NativeABIs  []string `json:"nativeABIs,omitempty"`
	MinSDK      int      `json:"minSDK,omitempty"`
}

func (a fdroidApp) canDownload() bool {
	return a.validFile() && a.Size <= patchMaxDownload
}

func (a fdroidApp) validFile() bool {
	return fdroidPackagePattern.MatchString(a.Package) && fdroidAPKNamePattern.MatchString(a.APKName) &&
		apkStoreSHA256Pattern().MatchString(a.SHA256) && a.Size > 0
}

func (a fdroidApp) hasTrimuiABI() bool {
	if len(a.NativeABIs) == 0 {
		return true // Java-only or architecture-independent; inspect after download.
	}
	for _, abi := range a.NativeABIs {
		if abi == "arm64-v8a" {
			return true
		}
	}
	return false
}

func (a fdroidApp) isGame() bool {
	for _, category := range a.Categories {
		if strings.Contains(strings.ToLower(category), "game") {
			return true
		}
	}
	return false
}

func (a fdroidApp) asCatalogEntry() apkStoreCatalogEntry {
	return apkStoreCatalogEntry{
		ID: "fdroid-" + a.SHA256[:32], Title: a.Name, Package: a.Package,
		Kind: "title", Status: apkStatusExperimental, Summary: a.Summary,
		URL: fdroidBaseURL + a.APKName, SHA256: a.SHA256, Size: a.Size,
	}
}

type fdroidCatalog struct {
	Timestamp int64       `json:"timestamp"`
	IndexSHA  string      `json:"indexSha256"`
	Apps      []fdroidApp `json:"apps"`
}

type fdroidEntry struct {
	Timestamp int64 `json:"timestamp"`
	Index     struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"index"`
}

type fdroidIndexPackage struct {
	Metadata struct {
		Name       map[string]string `json:"name"`
		Summary    map[string]string `json:"summary"`
		Categories []string          `json:"categories"`
	} `json:"metadata"`
	Versions map[string]struct {
		File struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"file"`
		Manifest struct {
			VersionName string   `json:"versionName"`
			VersionCode int64    `json:"versionCode"`
			NativeCode  []string `json:"nativecode"`
			UsesSDK     struct {
				Min int `json:"minSdkVersion"`
			} `json:"usesSdk"`
		} `json:"manifest"`
		ReleaseChannels []string `json:"releaseChannels"`
	} `json:"versions"`
}

func fdroidLocalized(m map[string]string, fallback string) string {
	for _, key := range []string{"en-US", "en-GB", "en"} {
		if s := strings.TrimSpace(m[key]); s != "" {
			return s
		}
	}
	for _, s := range m {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return fallback
}

func fdroidChooseApp(pkg string, p fdroidIndexPackage) (fdroidApp, bool) {
	if !fdroidPackagePattern.MatchString(pkg) {
		return fdroidApp{}, false
	}
	var chosen fdroidApp
	var found, bestCompatible, bestStable bool
	for _, v := range p.Versions {
		file := v.File
		candidate := fdroidApp{Package: pkg, APKName: file.Name, SHA256: strings.ToLower(file.SHA256), Size: file.Size,
			Version: v.Manifest.VersionName, VersionCode: v.Manifest.VersionCode,
			NativeABIs: v.Manifest.NativeCode, MinSDK: v.Manifest.UsesSDK.Min}
		if !candidate.validFile() || candidate.VersionCode <= 0 {
			continue
		}
		compatible, stable := candidate.hasTrimuiABI(), len(v.ReleaseChannels) == 0
		if !stable {
			candidate.Channel = strings.Join(v.ReleaseChannels, ", ")
		}
		// Prefer builds the TrimUI can read, then stable releases, then the
		// newest version. A beta or wrong-CPU build remains visible if it is
		// the only version in the repository.
		if !found || compatible && !bestCompatible || compatible == bestCompatible && stable && !bestStable ||
			compatible == bestCompatible && stable == bestStable && candidate.VersionCode > chosen.VersionCode ||
			compatible == bestCompatible && stable == bestStable && candidate.VersionCode == chosen.VersionCode && candidate.SHA256 < chosen.SHA256 {
			chosen, found, bestCompatible, bestStable = candidate, true, compatible, stable
		}
	}
	if !found {
		return fdroidApp{}, false
	}
	chosen.Name = apkStoreCatalogText(fdroidLocalized(p.Metadata.Name, pkg), "title")
	chosen.Summary = apkStoreCatalogText(fdroidLocalized(p.Metadata.Summary, "F-Droid app"), "summary")
	chosen.Categories = p.Metadata.Categories
	return chosen, true
}

// fdroidParseIndex decodes one package at a time. The caller first checks the
// complete index against the SHA-256 in the signed entry file.
func fdroidParseIndex(r io.Reader, timestamp int64, indexSHA string) (fdroidCatalog, error) {
	d := fdroidCatalog{Timestamp: timestamp, IndexSHA: strings.ToLower(indexSHA)}
	dec := json.NewDecoder(r)
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return fdroidCatalog{}, errors.New("F-Droid index is not an object")
	}
	found := false
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return fdroidCatalog{}, err
		}
		if key != "packages" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return fdroidCatalog{}, err
			}
			continue
		}
		found = true
		t, err := dec.Token()
		if err != nil || t != json.Delim('{') {
			return fdroidCatalog{}, errors.New("F-Droid packages is not an object")
		}
		for dec.More() {
			pkg, err := dec.Token()
			if err != nil {
				return fdroidCatalog{}, err
			}
			var p fdroidIndexPackage
			if err := dec.Decode(&p); err != nil {
				return fdroidCatalog{}, err
			}
			if app, ok := fdroidChooseApp(pkg.(string), p); ok {
				d.Apps = append(d.Apps, app)
				if len(d.Apps) > fdroidMaxApps {
					return fdroidCatalog{}, errors.New("F-Droid index has too many apps")
				}
			}
		}
		if _, err := dec.Token(); err != nil {
			return fdroidCatalog{}, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return fdroidCatalog{}, err
	}
	if !found || len(d.Apps) == 0 {
		return fdroidCatalog{}, errors.New("F-Droid index has no usable apps")
	}
	sort.Slice(d.Apps, func(i, j int) bool {
		a, b := strings.ToLower(d.Apps[i].Name), strings.ToLower(d.Apps[j].Name)
		if a != b {
			return a < b
		}
		return d.Apps[i].Package < d.Apps[j].Package
	})
	return d, nil
}

func fdroidCachePath() string {
	if dir, err := P().DataDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "apkstore", "fdroid-catalog.json")
	}
	return filepath.Join("data", "apkstore", "fdroid-catalog.json")
}

// fdroidCachePathOverride keeps tests off the user's data directory.
var fdroidCachePathOverride string

func fdroidLocalCachePath() string {
	if fdroidCachePathOverride != "" {
		return fdroidCachePathOverride
	}
	return fdroidCachePath()
}

func fdroidReadCache() (fdroidCatalog, error) {
	f, err := os.Open(fdroidLocalCachePath())
	if err != nil {
		return fdroidCatalog{}, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, fdroidCacheMax+1))
	if err != nil || int64(len(raw)) > fdroidCacheMax {
		return fdroidCatalog{}, errors.New("F-Droid saved list is too large")
	}
	var d fdroidCatalog
	if err := json.Unmarshal(raw, &d); err != nil || d.Timestamp <= 0 ||
		!apkStoreSHA256Pattern().MatchString(d.IndexSHA) || len(d.Apps) == 0 || len(d.Apps) > fdroidMaxApps {
		return fdroidCatalog{}, errors.New("F-Droid saved list is invalid")
	}
	for _, app := range d.Apps {
		if !app.validFile() {
			return fdroidCatalog{}, errors.New("F-Droid saved list has an invalid APK")
		}
	}
	return d, nil
}

func fdroidClient() *http.Client {
	return &http.Client{Timeout: fdroidIndexTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Hostname(), "f-droid.org") {
			return errors.New("F-Droid redirected outside its repository")
		}
		return nil
	}}
}

func fdroidReadSmall(ctx context.Context, client *http.Client, path string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fdroidBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub-APKStore/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("F-Droid returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil || int64(len(raw)) > max {
		return nil, errors.New("F-Droid entry is too large")
	}
	return raw, nil
}

func fdroidVerifyEntry(raw, signature []byte) (fdroidEntry, error) {
	var entry fdroidEntry
	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(fdroidRepoKey))
	if err != nil || len(keys) != 1 || hex.EncodeToString(keys[0].PrimaryKey.Fingerprint[:]) != fdroidRepoKeyFP {
		return entry, errors.New("F-Droid repository key does not match its pinned fingerprint")
	}
	if _, err := openpgp.CheckArmoredDetachedSignature(keys, bytes.NewReader(raw), bytes.NewReader(signature)); err != nil {
		return entry, fmt.Errorf("F-Droid repository signature failed: %w", err)
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return entry, err
	}
	if entry.Timestamp <= 0 || entry.Index.Name != "/index-v2.json" ||
		!apkStoreSHA256Pattern().MatchString(entry.Index.SHA256) || entry.Index.Size <= 0 || entry.Index.Size > fdroidIndexMax {
		return fdroidEntry{}, errors.New("F-Droid signed entry has no usable index")
	}
	return entry, nil
}

// fdroidRefresh verifies the signed entry, its index hash, then saves only a
// compact browser list. A failed refresh leaves the last verified list intact.
func fdroidRefresh(ctx context.Context, force bool) (fdroidCatalog, error) {
	cached, cacheErr := fdroidReadCache()
	if !force && cacheErr == nil {
		if st, err := os.Stat(fdroidLocalCachePath()); err == nil && time.Since(st.ModTime()) < fdroidIndexEvery {
			return cached, nil
		}
	}
	client := fdroidClient()
	raw, err := fdroidReadSmall(ctx, client, "/entry.json", 1<<20)
	if err != nil {
		return cached, err
	}
	sig, err := fdroidReadSmall(ctx, client, "/entry.json.asc", 64<<10)
	if err != nil {
		return cached, err
	}
	entry, err := fdroidVerifyEntry(raw, sig)
	if err != nil {
		return cached, err
	}
	if cacheErr == nil {
		if entry.Timestamp < cached.Timestamp {
			return cached, errors.New("F-Droid returned an older signed list")
		}
		if strings.EqualFold(entry.Index.SHA256, cached.IndexSHA) {
			now := time.Now()
			_ = os.Chtimes(fdroidLocalCachePath(), now, now)
			return cached, nil
		}
	}
	path := fdroidLocalCachePath() + ".index-v2.json"
	defer os.Remove(path)
	if err := downloadToFile(ctx, client, fdroidBaseURL+entry.Index.Name, path, entry.Index.SHA256, entry.Index.Size); err != nil {
		return cached, err
	}
	f, err := os.Open(path)
	if err != nil {
		return cached, err
	}
	d, parseErr := fdroidParseIndex(io.LimitReader(f, fdroidIndexMax+1), entry.Timestamp, entry.Index.SHA256)
	f.Close()
	if parseErr != nil {
		return cached, parseErr
	}
	encoded, err := json.Marshal(d)
	if err != nil || int64(len(encoded)) > fdroidCacheMax {
		return cached, errors.New("F-Droid browser list is too large")
	}
	if err := AtomicWrite(fdroidLocalCachePath(), encoded, 0o644); err != nil {
		return cached, err
	}
	return d, nil
}

// fdroidDownloadAndCheck saves a hash-verified APK. Only a native ARM64 APK
// with the expected package gets a Games profile; all others stay in Your APKs
// with the scanner's verdict.
func fdroidDownloadAndCheck(ctx context.Context, app fdroidApp) (apkStoreInfo, GameProfile, bool, error) {
	if !app.canDownload() || !app.hasTrimuiABI() {
		return apkStoreInfo{}, GameProfile{}, false, errors.New("this F-Droid build is not available for this device")
	}
	e := app.asCatalogEntry()
	path, err := apkStoreDownload(ctx, e)
	if err != nil {
		return apkStoreInfo{}, GameProfile{}, false, err
	}
	info := apkStoreAnalyze(path, true)
	if info.Manifest.Package != app.Package {
		return info, GameProfile{}, false, fmt.Errorf("APK package is %s, expected %s", info.Manifest.Package, app.Package)
	}
	if !androidRTSupported() {
		return info, GameProfile{}, false, nil // the PC can inspect, not install to Games
	}
	if err := apkStoreCheckInstall(e, info); err != nil {
		return info, GameProfile{}, false, nil // saved for inspection; AndroidRT cannot add it
	}
	profile, err := apkStoreAddProfile(info)
	if err != nil {
		return info, GameProfile{}, false, err
	}
	return info, profile, true, nil
}
