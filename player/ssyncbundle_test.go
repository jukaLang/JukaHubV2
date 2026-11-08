package main

// ssyncbundle_test.go — the encrypted .jukasaves file: the chunked AEAD stream
// on its own, then a whole export and import (preview first, then merge), a
// wrong passphrase, and files that were changed, cut short or extended.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ssyncTestFastKDF uses the fewest PBKDF2 rounds a file may declare, so the
// tests stay quick.
func ssyncTestFastKDF(t *testing.T) {
	t.Helper()
	prev := ssyncBundleIterations
	ssyncBundleIterations = ssyncBundleMinIter
	t.Cleanup(func() { ssyncBundleIterations = prev })
}

// ssyncTestFrames splits a sealed stream into its chunks (length + payload).
func ssyncTestFrames(t *testing.T, sealed []byte) [][]byte {
	t.Helper()
	var frames [][]byte
	for len(sealed) > 0 {
		if len(sealed) < 4 {
			t.Fatal("a torn chunk length")
		}
		n := int(binary.BigEndian.Uint32(sealed[:4]))
		if len(sealed) < 4+n {
			t.Fatal("a torn chunk")
		}
		frames = append(frames, sealed[:4+n])
		sealed = sealed[4+n:]
	}
	return frames
}

// ssyncTestFlip returns a copy of data with one bit of byte i flipped.
func ssyncTestFlip(data []byte, i int) []byte {
	out := append([]byte(nil), data...)
	out[i] ^= 0x01
	return out
}

// ssyncTestFlipDigit returns a copy of data with the digit at i changed to
// another digit, so the JSON around it stays valid.
func ssyncTestFlipDigit(data []byte, i int) []byte {
	out := append([]byte(nil), data...)
	if out[i] >= '0' && out[i] <= '8' {
		out[i]++
	} else {
		out[i] = '0'
	}
	return out
}

func TestSsyncSealedStream(t *testing.T) {
	aead, err := ssyncBundleAEAD(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	header := []byte(ssyncBundleMagic + "test header")
	prefix := [4]byte{1, 2, 3, 4}
	const chunk = 1024
	sealAll := func(plain []byte) []byte {
		var out bytes.Buffer
		sw := newSsyncSealWriter(&out, aead, prefix, header, chunk)
		if _, err := sw.Write(plain); err != nil {
			t.Fatal(err)
		}
		if err := sw.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	unseal := func(sealed, hdr []byte) ([]byte, error) {
		return io.ReadAll(newSsyncOpenReader(bytes.NewReader(sealed), aead, prefix, hdr, chunk))
	}
	for _, size := range []int{0, 1, chunk - 1, chunk, chunk + 1, 3*chunk + 17} {
		plain := make([]byte, size)
		for i := range plain {
			plain[i] = byte(i * 7)
		}
		got, err := unseal(sealAll(plain), header)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("%d bytes: got %d back, %v", size, len(got), err)
		}
	}

	// 3200 bytes make three full chunks and a last one.
	sealed := sealAll(bytes.Repeat([]byte("0123456789abcdef"), 200))
	frames := ssyncTestFrames(t, sealed)
	if len(frames) != 4 {
		t.Fatalf("%d chunks, want 4", len(frames))
	}
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	cases := []struct {
		name   string
		stream []byte
		hdr    []byte
		want   error
	}{
		{"chunks swapped", join(frames[0], frames[2], frames[1], frames[3]), header, errSsyncAuth},
		{"middle chunk dropped", join(frames[0], frames[1], frames[3]), header, errSsyncAuth},
		{"last chunk dropped", join(frames[0], frames[1], frames[2]), header, errSsyncTruncated},
		{"cut inside a chunk", sealed[:len(sealed)-5], header, errSsyncTruncated},
		{"bytes after the end", join(sealed, []byte{0}), header, errSsyncAuth},
		{"another header", sealed, []byte(ssyncBundleMagic + "test headex"), errSsyncAuth},
		{"flipped bit", ssyncTestFlip(sealed, 40), header, errSsyncAuth},
	}
	for _, tc := range cases {
		if _, err := unseal(tc.stream, tc.hdr); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestSsyncBundleExportImport exports one device's saves and imports them on
// another: nothing is readable without the passphrase, a wrong one is refused
// before anything is written, the merge follows the sync rules, and the same
// file imported again changes nothing.
func TestSsyncBundleExportImport(t *testing.T) {
	localDir, remoteDir := ssyncTestState(t)
	ssyncTestFastKDF(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)
	t100 := t0.Add(100 * time.Second)
	metroid := strings.Repeat("metroid-", 25600) // 200 KiB: several chunks

	// The device that exports.
	ssyncTestWrite(t, remoteDir, "Zelda.srm", "zelda save", t0)
	ssyncTestWrite(t, remoteDir, "gba/Metroid.state1", metroid, t0)
	ssyncTestWrite(t, remoteDir, "old.srm", "newer there", t100)
	ssyncTestWrite(t, remoteDir, "both.srm", "both there", t0)
	ssyncTestWrite(t, remoteDir, "newer-here.srm", "older there", t0)
	// The device that imports.
	ssyncTestWrite(t, localDir, "old.srm", "old here", t0)
	ssyncTestWrite(t, localDir, "both.srm", "both here", t1)
	ssyncTestWrite(t, localDir, "newer-here.srm", "newer here", t100)
	ssyncTestWrite(t, localDir, "mine.srm", "only here", t0)

	exportDir := t.TempDir()
	const pass = "correct horse battery"
	if _, _, err := ssyncExport(ctx, ssyncTestRoots(remoteDir), exportDir, "short", nil); err == nil {
		t.Fatal("a five-character passphrase was accepted")
	}
	calls := 0
	bundle, n, err := ssyncExport(ctx, ssyncTestRoots(remoteDir), exportDir, pass, func(done, total int) { calls++ })
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || calls != 5 || filepath.Dir(bundle) != exportDir || !strings.HasSuffix(bundle, ssyncBundleExt) {
		t.Fatalf("exported %q with %d saves (%d progress calls)", bundle, n, calls)
	}
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"zelda save", "Zelda.srm", "metroid-metroid-"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("%q is readable in the file", secret)
		}
	}
	if hdr, err := ssyncPeekBundle(bundle); err != nil || hdr.KDF != "pbkdf2-sha256" || hdr.Cipher != "aes-256-gcm" || hdr.Iter != ssyncBundleMinIter {
		t.Fatalf("header %+v, %v", hdr, err)
	}
	if entries, _ := os.ReadDir(exportDir); len(entries) != 1 {
		t.Errorf("%d files in the export folder, want just the saves file", len(entries))
	}

	if _, err := ssyncImportPreviewFile(ctx, bundle, "wrong passphrase", ssyncTestRoots(localDir)); !errors.Is(err, errSsyncAuth) {
		t.Fatalf("a wrong passphrase: %v", err)
	}
	prev, err := ssyncImportPreviewFile(ctx, bundle, pass, ssyncTestRoots(localDir))
	if err != nil {
		t.Fatal(err)
	}
	plan := prev.Plan
	if plan.Pull != 3 || plan.Conflict != 1 || plan.Push != 0 || prev.Kept != 1 || len(prev.Bundle.Files) != 5 {
		t.Fatalf("preview: %d pull, %d conflicts, %d push, %d kept, %d in the file", plan.Pull, plan.Conflict, plan.Push, prev.Kept, len(prev.Bundle.Files))
	}
	copyPath := ""
	for _, it := range plan.Items {
		if it.Action == ssyncConflict {
			copyPath = it.CopyPath
		}
	}
	if !strings.HasPrefix(copyPath, "both (from ") {
		t.Fatalf("conflict copy %q", copyPath)
	}
	// Nothing is written until the import is confirmed.
	if _, err := os.Stat(filepath.Join(localDir, "Zelda.srm")); !os.IsNotExist(err) {
		t.Fatal("the preview wrote a save")
	}
	if _, err := ssyncImportApply(ctx, prev, "wrong passphrase", ssyncTestRoots(localDir), nil); !errors.Is(err, errSsyncAuth) {
		t.Fatalf("importing with a wrong passphrase: %v", err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "Zelda.srm")); !os.IsNotExist(err) {
		t.Fatal("a wrong passphrase wrote a save")
	}

	res, err := ssyncImportApply(ctx, prev, pass, ssyncTestRoots(localDir), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pulled != 3 || res.Conflicts != 1 || res.Failed != 0 || res.Snapshot == "" {
		t.Fatalf("result %+v", res)
	}
	want := [][2]string{
		{"Zelda.srm", "zelda save"},
		{"gba/Metroid.state1", metroid},
		{"old.srm", "newer there"},
		{"both.srm", "both here"},
		{copyPath, "both there"},
		{"newer-here.srm", "newer here"},
		{"mine.srm", "only here"},
	}
	for _, w := range want {
		if got := ssyncTestRead(t, localDir, w[0]); got != w[1] {
			t.Errorf("%s does not hold what it should (%d bytes, want %d)", w[0], len(got), len(w[1]))
		}
	}
	if left, _ := filepath.Glob(filepath.Join(ssyncDirOverride, "import-*")); len(left) != 0 {
		t.Errorf("staging left behind: %v", left)
	}

	// The same file again changes nothing: its conflict copy is already here.
	again, err := ssyncImportPreviewFile(ctx, bundle, pass, ssyncTestRoots(localDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Plan.Items) != 0 || again.Kept != 2 {
		t.Errorf("a second import plans %+v with %d kept, want nothing to do and 2 kept", again.Plan.Items, again.Kept)
	}
}

// TestSsyncBundleRefusesDamagedFiles changes, cuts and extends a saves file.
// Damage to the header fails at once; damage after the save list still lets
// the preview read the list, but the import checks the whole file before it
// merges a single save.
func TestSsyncBundleRefusesDamagedFiles(t *testing.T) {
	localDir, remoteDir := ssyncTestState(t)
	ssyncTestFastKDF(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	ssyncTestWrite(t, remoteDir, "Zelda.srm", "zelda save", t0)
	ssyncTestWrite(t, remoteDir, "gba/Metroid.state1", strings.Repeat("metroid-", 25600), t0)
	const pass = "correct horse battery"
	bundle, _, err := ssyncExport(ctx, ssyncTestRoots(remoteDir), t.TempDir(), pass, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := ssyncImportPreviewFile(ctx, bundle, pass, ssyncTestRoots(localDir))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name+ssyncBundleExt)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	at := bytes.Index(data, []byte(`"created":"`))
	if at < 0 {
		t.Fatal("no creation time in the header")
	}
	changed := ssyncTestFlipDigit(data, at+len(`"created":"`))
	if _, err := ssyncImportPreviewFile(ctx, write("header", changed), pass, ssyncTestRoots(localDir)); !errors.Is(err, errSsyncAuth) {
		t.Errorf("a changed header: %v", err)
	}
	if _, err := ssyncImportPreviewFile(ctx, write("junk", []byte("hello, not saves")), pass, ssyncTestRoots(localDir)); !errors.Is(err, errSsyncNotBundle) {
		t.Errorf("a file that is not a saves file: %v", err)
	}
	if _, err := ssyncImportPreviewFile(ctx, write("tag", ssyncTestFlip(data, len(data)-5)), pass, ssyncTestRoots(localDir)); err != nil {
		t.Errorf("the preview reads only the list, which is intact: %v", err)
	}

	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"flipped tag", ssyncTestFlip(data, len(data)-5), errSsyncAuth},
		{"cut short", data[:len(data)-10], errSsyncTruncated},
		{"bytes appended", append(append([]byte(nil), data...), 0), errSsyncAuth},
	}
	for _, tc := range cases {
		damaged := prev
		damaged.Path = write(strings.ReplaceAll(tc.name, " ", "-"), tc.data)
		if _, err := ssyncImportApply(ctx, damaged, pass, ssyncTestRoots(localDir), nil); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
		if _, err := os.Stat(filepath.Join(localDir, "Zelda.srm")); !os.IsNotExist(err) {
			t.Fatalf("%s: a save was merged from a damaged file", tc.name)
		}
	}
}
