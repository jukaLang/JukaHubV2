package main

// upch_test.go pins the offline update channel: what the cache keeps, when a
// background download may run, how a download is verified (signature, then
// digest, never a downgrade), that a cached copy is checked again before it is
// staged, that a delta rebuilds and verifies a tree, and that Install from
// file trusts a signed release.json over a checksum file over nothing.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"jukahub/JukaHub/internal/jdelta"
	"jukahub/JukaHub/internal/releasesign"
)

// upchTestSignedManifest signs a release.json with a fresh key and makes that
// key the one the app trusts for the rest of the test.
func upchTestSignedManifest(t *testing.T, targets []releasesign.Target, issued, expires time.Time) []byte {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	prev := patchRepoPublicKey
	patchRepoPublicKey = pub
	t.Cleanup(func() { patchRepoPublicKey = prev })
	m := releasesign.Manifest{
		Format:    releasesign.Format,
		KeyID:     patchRepoKeyID,
		Tag:       "JukaHub-20261101120000",
		IssuedAt:  issued.UTC().Format(time.RFC3339),
		ExpiresAt: expires.UTC().Format(time.RFC3339),
		Targets:   targets,
	}
	if err := releasesign.Sign(&m, priv); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestUpchPrunePlan(t *testing.T) {
	installed := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var entries []upchEntry
	for _, tag := range []string{"JukaHub-20261110120000", "JukaHub-20261105120000", "JukaHub-20261103120000",
		"JukaHub-20261101120000", "JukaHub-20261020120000"} {
		entries = append(entries, upchEntry{Tag: tag})
	}
	isInstalled := func(tag string) bool { return tag == "JukaHub-20261101120000" }
	kept, dropped := upchPrunePlan(entries, installed, true, isInstalled, 2)
	if len(kept) != 2 || kept[0].Tag != "JukaHub-20261110120000" || kept[1].Tag != "JukaHub-20261105120000" {
		t.Fatalf("kept %+v, want the two newest releases", kept)
	}
	if len(dropped) != 3 {
		t.Fatalf("dropped %d entries, want the third newest, the installed one and the older one", len(dropped))
	}
	// With the installed build unknown nothing counts as older; the bound
	// still holds.
	kept, _ = upchPrunePlan(entries, time.Time{}, false, func(string) bool { return false }, 2)
	if len(kept) != 2 {
		t.Fatalf("kept %d entries for an unknown build, want 2", len(kept))
	}
}

func TestUpchMoment(t *testing.T) {
	cases := []struct {
		name     string
		explicit bool
		autoOn   bool
		online   bool
		inGame   bool
		busy     bool
		plug     bool
		idle     time.Duration
		want     bool
	}{
		{"charging", false, true, true, false, false, true, 0, true},
		{"idle on battery", false, true, true, false, false, false, 3 * time.Minute, true},
		{"in use on battery", false, true, true, false, false, false, 10 * time.Second, false},
		{"offline", false, true, false, false, false, true, time.Hour, false},
		{"game on screen", false, true, true, true, false, true, time.Hour, false},
		{"another install", false, true, true, false, true, true, time.Hour, false},
		{"setting off", false, false, true, false, false, true, time.Hour, false},
		{"asked, setting off, in use", true, false, true, false, false, false, 0, true},
		{"asked but offline", true, true, false, false, false, true, 0, false},
		{"asked during a game", true, true, true, true, false, true, 0, false},
	}
	for _, tc := range cases {
		if got := upchMoment(tc.explicit, tc.autoOn, tc.online, tc.inGame, tc.busy, tc.plug, tc.idle); got != tc.want {
			t.Errorf("%s: upchMoment = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUpchExpectedDigestNeverDowngrades(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	other := strings.Repeat("cd", 32)
	asset := githubAsset{Name: "JukaHub-Trimui.zip", Size: 10, Digest: "sha256:" + sum}

	if got, _, verified, err := upchExpectedDigest(nil, asset); err != nil || got != sum || verified != upchVerifiedChecksum {
		t.Fatalf("digest only: %q %q %v", got, verified, err)
	}
	bare := githubAsset{Name: "JukaHub-Trimui.zip", Size: 10}
	if got, _, verified, err := upchExpectedDigest(nil, bare); err != nil || got != "" || verified != upchVerifiedNone {
		t.Fatalf("no digest: %q %q %v", got, verified, err)
	}
	signed := &releasesign.Manifest{Targets: []releasesign.Target{{Asset: "JukaHub-Trimui.zip", SHA256: sum, Size: 10}}}
	if got, size, verified, err := upchExpectedDigest(signed, asset); err != nil || got != sum || size != 10 || verified != upchVerifiedSigned {
		t.Fatalf("signed: %q %d %q %v", got, size, verified, err)
	}
	disagree := &releasesign.Manifest{Targets: []releasesign.Target{{Asset: "JukaHub-Trimui.zip", SHA256: other, Size: 10}}}
	if _, _, _, err := upchExpectedDigest(disagree, asset); err == nil {
		t.Fatal("a digest that disagrees with the signed manifest was accepted")
	}
	unlisted := &releasesign.Manifest{Targets: []releasesign.Target{{Asset: "JukaHub-Windows.zip", SHA256: sum, Size: 10}}}
	if _, _, _, err := upchExpectedDigest(unlisted, asset); err == nil {
		t.Fatal("a signed release that does not list the file was trusted anyway")
	}
}

func TestUpchVerifyManifestLocal(t *testing.T) {
	now := time.Now()
	targets := []releasesign.Target{{Asset: "JukaHub-Trimui.zip", SHA256: strings.Repeat("ab", 32), Size: 10}}

	data := upchTestSignedManifest(t, targets, now.Add(-time.Hour), now.Add(time.Hour))
	if _, expired, err := upchVerifyManifestLocal(data); err != nil || expired {
		t.Fatalf("a current manifest: expired=%v err=%v", expired, err)
	}
	// Past its window: still a valid signature, reported rather than refused.
	data = upchTestSignedManifest(t, targets, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	if _, expired, err := upchVerifyManifestLocal(data); err != nil || !expired {
		t.Fatalf("an expired manifest: expired=%v err=%v", expired, err)
	}
	// Tampered: the signature no longer covers it.
	tampered := bytes.Replace(data, []byte(strings.Repeat("ab", 32)), []byte(strings.Repeat("cd", 32)), 1)
	if _, _, err := upchVerifyManifestLocal(tampered); err == nil {
		t.Fatal("a tampered manifest verified")
	}
	// Signed by a key the app does not trust.
	foreign := upchTestSignedManifest(t, targets, now.Add(-time.Hour), now.Add(time.Hour))
	patchRepoPublicKey = make([]byte, ed25519.PublicKeySize)
	if _, _, err := upchVerifyManifestLocal(foreign); err == nil {
		t.Fatal("a manifest signed by another key verified")
	}
}

func TestUpchCheckHostBinary(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	upchWriteTestFile(t, good, upchTestExecutable(runtime.GOARCH))
	if err := upchCheckHostBinary(good); err != nil {
		t.Fatalf("this host's program was refused: %v", err)
	}
	if err := upchCheckHostBinary(""); err == nil {
		t.Fatal("no program at all was accepted")
	}
	checked := (runtime.GOOS == "linux" && upchELFMachine(runtime.GOARCH) != 0) ||
		(runtime.GOOS == "windows" && upchPEMachine(runtime.GOARCH) != 0)
	if !checked {
		// Only ELF and PE builds carry a machine to compare; anywhere else the
		// header check is all there is, and the program above passed it.
		return
	}
	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	foreign := filepath.Join(dir, "foreign")
	upchWriteTestFile(t, foreign, upchTestExecutable(other))
	if err := upchCheckHostBinary(foreign); err == nil {
		t.Fatal("a program for another processor was accepted")
	}
	junk := filepath.Join(dir, "junk")
	upchWriteTestFile(t, junk, []byte("not a program at all"))
	if err := upchCheckHostBinary(junk); err == nil {
		t.Fatal("a text file was accepted as a program")
	}
}

// A cached archive is checked again before it is staged; a copy damaged on
// the card is refused and dropped instead of installed.
func TestUpchStageEntryInstallsOnlyAnIntactCopy(t *testing.T) {
	dir := upchTestPatchDir(t)
	upchTestResetState(t)
	cache := filepath.Join(dir, "update-cache")
	tag := "JukaHub-20261101120000"
	entryDir := filepath.Join(cache, tag)
	archive := filepath.Join(entryDir, "JukaHub-Test.zip")
	files := map[string]string{}
	files["JukaHub-Trimui/"+releaseExecutableName()] = string(upchTestExecutable(runtime.GOARCH))
	files["JukaHub-Trimui/jukaconfig.json"] = `{"Version":"0.5.0"}`
	writeReleaseZip(t, archive, files)
	sum, err := SHA256File(archive)
	if err != nil {
		t.Fatal(err)
	}
	e := upchEntry{
		Tag:         tag,
		PublishedAt: "2026-11-01T12:00:00Z",
		Asset:       "JukaHub-Test.zip",
		Kind:        upchKindArchive,
		File:        "JukaHub-Test.zip",
		Size:        fileSize(archive),
		SHA256:      sum,
		Verified:    upchVerifiedChecksum,
	}
	if err := upchWriteJSON(filepath.Join(entryDir, "entry.json"), e); err != nil {
		t.Fatal(err)
	}
	entries := upchScanEntries(cache)
	if len(entries) != 1 {
		t.Fatalf("the cache lists %d entries, want 1", len(entries))
	}
	if err := upchStageEntry(context.Background(), cache, entries[0]); err != nil {
		t.Fatalf("upchStageEntry: %v", err)
	}
	staging, err := stagingDirFor(tag)
	if err != nil {
		t.Fatal(err)
	}
	if findStagedBinary(staging) == "" {
		t.Fatal("nothing was staged")
	}
	patchStateMu.Lock()
	staged := patchState.LastUpdateVer
	patchStateMu.Unlock()
	if staged != tag {
		t.Fatalf("the staged update is %q, want %q", staged, tag)
	}

	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(archive, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := upchStageEntry(context.Background(), cache, entries[0]); err == nil {
		t.Fatal("a damaged cached archive was staged")
	}
	if pathExists(entryDir) {
		t.Error("the damaged entry was kept in the cache")
	}
}

func TestUpchDeltaRebuildsAndVerifiesATree(t *testing.T) {
	upchTestPatchDir(t)
	exe := releaseExecutableName()
	base := t.TempDir()
	oldBin := append(upchTestExecutable(runtime.GOARCH), bytes.Repeat([]byte("old build "), 20000)...)
	newBin := append(upchTestExecutable(runtime.GOARCH), bytes.Repeat([]byte("new build!"), 20000)...)
	upchWriteTestFile(t, filepath.Join(base, exe), oldBin)
	var buf bytes.Buffer
	inputs := []jdelta.BundleInput{
		{Name: exe, Mode: 0o755, Old: oldBin, New: newBin, Delta: true},
		{Name: "jukaconfig.json", New: []byte(`{"Version":"0.5.0"}`)},
	}
	if _, err := jdelta.WriteBundle(&buf, jdelta.Bundle{FromTag: "JukaHub-1", ToTag: "JukaHub-2"}, inputs); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "update.jkdelta")
	upchWriteTestFile(t, bundle, buf.Bytes())

	b, err := jdelta.ReadBundleFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := jdeltaCheckBase(b, base); err != nil {
		t.Fatalf("the delta does not recognise its own base: %v", err)
	}
	tree := filepath.Join(t.TempDir(), "tree")
	if err := jdeltaMaterialize(context.Background(), bundle, base, tree); err != nil {
		t.Fatalf("jdeltaMaterialize: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(tree, exe)); err != nil || !bytes.Equal(got, newBin) {
		t.Fatalf("the rebuilt program differs from the release (%v)", err)
	}
	if err := jdeltaVerifyTree(bundle, tree); err != nil {
		t.Fatalf("jdeltaVerifyTree: %v", err)
	}
	upchWriteTestFile(t, filepath.Join(tree, "jukaconfig.json"), []byte(`{"Version":"6.6.6"}`))
	if err := jdeltaVerifyTree(bundle, tree); err == nil {
		t.Fatal("a tree changed after it was rebuilt passed its check")
	}

	// Another installed version: refused up front, and nothing is left behind.
	upchWriteTestFile(t, filepath.Join(base, exe), newBin)
	if err := jdeltaCheckBase(b, base); err == nil {
		t.Fatal("a delta for another version was accepted")
	}
	other := filepath.Join(t.TempDir(), "tree")
	if err := jdeltaMaterialize(context.Background(), bundle, base, other); err == nil {
		t.Fatal("a delta rebuilt from the wrong base")
	}
	if pathExists(other) {
		t.Error("a refused delta left a half-built tree")
	}
}

func TestUpchSideloadKind(t *testing.T) {
	cases := []struct {
		name string
		goos string
		want string
		ok   bool
	}{
		{"JukaHub-Trimui.zip", "linux", upchKindArchive, true},
		{"JukaHub-Windows.zip", "windows", upchKindArchive, true},
		{"JukaHub-Windows.zip", "linux", "", false},
		{"JukaHub-Trimui.zip", "windows", "", false},
		{"JukaHub-trimui-smart-pro-linux-arm64.tar.gz", "linux", upchKindArchive, true},
		{"JukaHub-Trimui-20261101120000.jkdelta", "linux", upchKindDelta, true},
		{"jukahub.zip", "windows", upchKindArchive, true},
		{"Roms.zip", "linux", "", false},
		{"JukaHub-Trimui.exe", "linux", "", false},
	}
	for _, tc := range cases {
		got, ok := upchSideloadKind(tc.name, tc.goos)
		if got != tc.want || ok != tc.ok {
			t.Errorf("upchSideloadKind(%q, %s) = %q, %v; want %q, %v", tc.name, tc.goos, got, ok, tc.want, tc.ok)
		}
	}
}

func TestUpchParseSums(t *testing.T) {
	sum := strings.Repeat("0f", 32)
	text := []byte("# checksums\n" + strings.Repeat("1a", 32) + "  other.zip\n" + strings.ToUpper(sum) + " *JukaHub-Trimui.zip\n")
	if got, ok := upchParseSums(text, "JukaHub-Trimui.zip", false); !ok || got != sum {
		t.Fatalf("SHA256SUMS: %q %v", got, ok)
	}
	if _, ok := upchParseSums(text, "missing.zip", false); ok {
		t.Fatal("a file the list does not name was matched")
	}
	if got, ok := upchParseSums([]byte(sum+"\n"), "JukaHub-Trimui.zip", true); !ok || got != sum {
		t.Fatalf("a bare .sha256: %q %v", got, ok)
	}
	if _, ok := upchParseSums([]byte(sum+"\n"), "JukaHub-Trimui.zip", false); ok {
		t.Fatal("a bare hash in a list was matched to a file it does not name")
	}
}

// Install from file trusts, in order: a signed release.json, a checksum file,
// nothing — and a release.json whose signature fails is never traded for a
// checksum.
func TestUpchVerifyFileTrustOrder(t *testing.T) {
	upchTestPatchDir(t)
	upchTestResetState(t)
	folder := t.TempDir()
	name := "JukaHub-Trimui.zip"
	if runtime.GOOS == "windows" {
		name = "JukaHub-Windows.zip"
	}
	archive := filepath.Join(folder, name)
	files := map[string]string{}
	files["JukaHub-X/"+releaseExecutableName()] = string(upchTestExecutable(runtime.GOARCH))
	files["JukaHub-X/"+releaseInfoFileName] = `{"tag":"JukaHub-20261101120000","date":"2026-11-01T12:00:00Z"}`
	writeReleaseZip(t, archive, files)
	sum, err := SHA256File(archive)
	if err != nil {
		t.Fatal(err)
	}

	v := upchVerifyFile(archive)
	if v.Err != "" || v.Verified != upchVerifiedNone || v.Tag != "JukaHub-20261101120000" {
		t.Fatalf("an archive on its own: %+v", v)
	}

	sums := filepath.Join(folder, "SHA256SUMS")
	upchWriteTestFile(t, sums, []byte(sum+"  "+name+"\n"))
	if v = upchVerifyFile(archive); v.Err != "" || v.Verified != upchVerifiedChecksum {
		t.Fatalf("with a matching SHA256SUMS: %+v", v)
	}
	upchWriteTestFile(t, sums, []byte(strings.Repeat("0", 64)+"  "+name+"\n"))
	if v = upchVerifyFile(archive); v.Err == "" {
		t.Fatal("a checksum that does not match was ignored")
	}

	now := time.Now()
	manifest := upchTestSignedManifest(t, []releasesign.Target{{Asset: name, SHA256: sum, Size: fileSize(archive)}},
		now.Add(-time.Hour), now.Add(time.Hour))
	upchWriteTestFile(t, filepath.Join(folder, releasesign.AssetName), manifest)
	if v = upchVerifyFile(archive); v.Err != "" || v.Verified != upchVerifiedSigned {
		t.Fatalf("with a signed release.json: %+v", v)
	}

	upchWriteTestFile(t, sums, []byte(sum+"  "+name+"\n"))
	forged := bytes.Replace(manifest, []byte(sum), []byte(strings.Repeat("a", 64)), 1)
	upchWriteTestFile(t, filepath.Join(folder, releasesign.AssetName), forged)
	if v = upchVerifyFile(archive); v.Err == "" {
		t.Fatalf("a release.json that fails its signature was passed over for the checksum: %+v", v)
	}

	empty := filepath.Join(t.TempDir(), name)
	writeReleaseZip(t, empty, map[string]string{"readme.txt": "no program in here"})
	if v := upchVerifyFile(empty); v.Err == "" {
		t.Fatal("an archive without JukaHub was accepted")
	}
}

// The channel's lines stay short enough for a Patch row on the small canvas.
func TestUpchStatusLines(t *testing.T) {
	upchTestPatchDir(t)
	upchTestResetState(t)
	withAvailableUpdate(t, "JukaHub-20261107120000")
	patchStateMu.Lock()
	patchState.InstalledReleaseAt = "2026-11-01T00:00:00Z"
	patchStateMu.Unlock()

	running := upchBuildView(true, "JukaHub-20261107120000", 6<<20, 15<<20, "", "", "")
	if !running.Running || running.Progress < 0.39 || running.Progress > 0.41 {
		t.Fatalf("a download at 6 of 15 MiB: %+v", running)
	}
	upchState.mu.Lock()
	upchState.entries = []upchEntry{{Tag: "JukaHub-20261107120000", PublishedAt: "2026-11-07T12:00:00Z", Verified: upchVerifiedSigned}}
	upchState.mu.Unlock()
	ready := upchBuildView(false, "", 0, 0, "", "", "")
	if !ready.Ready || !strings.Contains(ready.Line, "no internet needed") || ready.Verified != "signed by JukaHub" {
		t.Fatalf("a verified cached release: %+v", ready)
	}
	upchState.mu.Lock()
	upchState.entries = nil
	upchState.mu.Unlock()
	lines := []string{
		running.Line,
		ready.Line,
		upchBuildView(true, "JukaHub-20261107120000", 0, 0, "", "", "").Line,
		upchBuildView(false, "", 0, 0, "No internet connection — connect WiFi and try again.", "", "").Line,
		upchBuildView(false, "", 0, 0, "", "", "").Line,
	}
	for _, line := range lines {
		if n := len([]rune(line)); n == 0 || n > 90 {
			t.Errorf("a %d-character status line: %q", n, line)
		}
	}
}
