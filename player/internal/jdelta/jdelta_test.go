package jdelta

// jdelta_test.go pins the format's contract: every edit round-trips, a small
// edit makes a small patch, a patch for another base is refused before a byte
// is written, and a damaged or hostile patch or bundle is rejected instead of
// producing a file.

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// randomBytes returns n reproducible pseudo-random bytes.
func randomBytes(seed int64, n int) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Intn(256))
	}
	return b
}

func joinBytes(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// roundTrip diffs oldData against newData, applies the patch and checks the
// result. It returns the patch.
func roundTrip(t *testing.T, oldData, newData []byte) []byte {
	t.Helper()
	patch, err := Diff(oldData, newData)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	var out bytes.Buffer
	h, err := Apply(bytes.NewReader(oldData), int64(len(oldData)), bytes.NewReader(patch), &out)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.Equal(out.Bytes(), newData) {
		t.Fatalf("rebuilt %d bytes that differ from the %d expected", out.Len(), len(newData))
	}
	if h.NewSize != int64(len(newData)) || h.NewSHA != sha256.Sum256(newData) {
		t.Fatalf("the header does not describe the new file: %+v", h)
	}
	return patch
}

func TestDiffApplyRoundTrips(t *testing.T) {
	base := randomBytes(1, 300<<10)
	flipped := append([]byte(nil), base...)
	for _, at := range []int{0, 10, 5000, 150000, len(base) - 1} {
		flipped[at] ^= 0x5a
	}
	moved := joinBytes(base[200000:220000], base[:200000], base[220000:])
	cases := []struct {
		name     string
		oldData  []byte
		newData  []byte
		maxRatio float64 // patch bytes per new byte; 0 means no bound
	}{
		{"identical", base, base, 0.01},
		{"byte edits", base, flipped, 0.02},
		{"insertion", base, joinBytes(base[:120000], randomBytes(2, 3000), base[120000:]), 0.05},
		{"deletion", base, joinBytes(base[:80000], base[84096:]), 0.02},
		{"prepend", base, joinBytes(randomBytes(3, 1000), base), 0.02},
		{"append", base, joinBytes(base, randomBytes(4, 2000)), 0.02},
		{"moved block", base, moved, 0.02},
		{"unrelated", base, randomBytes(5, 200<<10), 0},
		{"empty old", nil, base[:5000], 0},
		{"empty new", base[:5000], nil, 0},
		{"both empty", nil, nil, 0},
		{"tiny", []byte("hello"), []byte("hello, world"), 0},
		{"repetitive", bytes.Repeat([]byte("abcd"), 50000), bytes.Repeat([]byte("abce"), 50000), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			patch := roundTrip(t, tc.oldData, tc.newData)
			if tc.maxRatio > 0 && float64(len(patch)) > tc.maxRatio*float64(len(tc.newData)) {
				t.Errorf("a %d byte patch for a %d byte file (%.4f per byte, want at most %.4f)",
					len(patch), len(tc.newData), float64(len(patch))/float64(len(tc.newData)), tc.maxRatio)
			}
		})
	}
}

// A release is rebuilt reproducibly: the same two files give the same bytes.
func TestDiffIsDeterministic(t *testing.T) {
	oldData := randomBytes(6, 100<<10)
	newData := joinBytes(oldData[:40000], randomBytes(7, 777), oldData[41000:])
	a, err := Diff(oldData, newData)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Diff(oldData, newData)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two diffs of the same files differ")
	}
}

// Content-defined chunking is what keeps a delta small after an insertion: the
// boundaries after the edit are the old ones, shifted, not new ones.
func TestChunkBoundariesResynchronise(t *testing.T) {
	data := randomBytes(8, 200<<10)
	shift := 100
	edited := joinBytes(data[:1000], randomBytes(9, shift), data[1000:])
	old := map[int]bool{}
	for _, end := range chunkEnds(data) {
		old[end] = true
	}
	total, same := 0, 0
	for _, end := range chunkEnds(edited) {
		if end < 20000 || end == len(edited) {
			continue
		}
		total++
		if old[end-shift] {
			same++
		}
	}
	if total == 0 || same*10 < total*9 {
		t.Fatalf("only %d of %d boundaries after the insertion survived it", same, total)
	}
	for _, end := range chunkEnds(data) {
		if end <= 0 || end > len(data) {
			t.Fatalf("chunk end %d is outside the data", end)
		}
	}
}

func TestApplyRejectsAWrongBase(t *testing.T) {
	oldData := randomBytes(10, 64<<10)
	newData := joinBytes(oldData[:30000], randomBytes(11, 500), oldData[30000:])
	patch, err := Diff(oldData, newData)
	if err != nil {
		t.Fatal(err)
	}
	edited := append([]byte(nil), oldData...)
	edited[100] ^= 0xff
	cases := []struct {
		name string
		base []byte
	}{
		{"edited base", edited},
		{"shorter base", oldData[:len(oldData)-1]},
		{"empty base", nil},
		{"other file", randomBytes(12, 64<<10)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := Apply(bytes.NewReader(tc.base), int64(len(tc.base)), bytes.NewReader(patch), &out)
			if !errors.Is(err, ErrWrongBase) {
				t.Fatalf("err = %v, want ErrWrongBase", err)
			}
			if out.Len() != 0 {
				t.Fatalf("%d bytes were written before the base was refused", out.Len())
			}
		})
	}
}

func TestApplyRejectsDamagedPatches(t *testing.T) {
	oldData := randomBytes(20, 40<<10)
	newData := joinBytes(oldData[:9000], randomBytes(21, 3000), oldData[9500:])
	patch, err := Diff(oldData, newData)
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(f func(p []byte)) []byte {
		p := append([]byte(nil), patch...)
		f(p)
		return p
	}
	cases := []struct {
		name  string
		patch []byte
	}{
		{"empty", nil},
		{"wrong magic", mutate(func(p []byte) { p[0] = 'X' })},
		{"future version", mutate(func(p []byte) { p[8] = 9 })},
		{"reserved bytes", mutate(func(p []byte) { p[10] = 1 })},
		{"wrong result checksum", mutate(func(p []byte) { p[70] ^= 0x01 })},
		{"absurd size", mutate(func(p []byte) { binary.BigEndian.PutUint64(p[52:60], 1<<40) })},
		{"header only", patch[:patchHeaderSize]},
		{"truncated body", patch[:len(patch)-5]},
		{"damaged body", mutate(func(p []byte) { p[patchHeaderSize+(len(p)-patchHeaderSize)/2] ^= 0x55 })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if _, err := Apply(bytes.NewReader(oldData), int64(len(oldData)), bytes.NewReader(tc.patch), &out); err == nil {
				t.Fatal("a damaged patch was applied")
			}
		})
	}
}

// craft builds a patch from a header and a hand-written operation stream, the
// shape a damaged or hostile file could take.
func craft(t *testing.T, h Header, ops []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(h.marshal())
	zw, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(ops); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rawCopy and rawAdd encode one operation.
func rawCopy(off, n uint64) []byte {
	b := binary.AppendUvarint([]byte{opCopy}, off)
	return binary.AppendUvarint(b, n)
}

func rawAdd(data []byte) []byte {
	b := binary.AppendUvarint([]byte{opAdd}, uint64(len(data)))
	return append(b, data...)
}

func TestApplyBoundsEveryOperation(t *testing.T) {
	oldData := []byte("0123456789")
	newData := []byte("01234abcde")
	h := Header{
		OldSize: int64(len(oldData)),
		OldSHA:  sha256.Sum256(oldData),
		NewSize: int64(len(newData)),
		NewSHA:  sha256.Sum256(newData),
	}
	end := []byte{opEnd}
	valid := craft(t, h, joinBytes(rawCopy(0, 5), rawAdd([]byte("abcde")), end))
	var out bytes.Buffer
	_, err := Apply(bytes.NewReader(oldData), int64(len(oldData)), bytes.NewReader(valid), &out)
	if err != nil || out.String() != string(newData) {
		t.Fatalf("the hand-written patch did not apply: %v (%q)", err, out.String())
	}

	cases := []struct {
		name string
		ops  []byte
	}{
		{"copy past the base", joinBytes(rawCopy(8, 5), end)},
		{"copy offset past the base", joinBytes(rawCopy(11, 1), end)},
		{"empty copy", joinBytes(rawCopy(0, 0), end)},
		{"add past the result", joinBytes(rawAdd([]byte("0123456789abcdef")), end)},
		{"empty add", joinBytes(rawAdd(nil), end)},
		{"short result", joinBytes(rawCopy(0, 5), end)},
		{"missing end", joinBytes(rawCopy(0, 5), rawAdd([]byte("abcde")))},
		{"data after end", joinBytes(rawCopy(0, 5), rawAdd([]byte("abcde")), end, []byte{7})},
		{"unknown operation", []byte{9}},
		{"add without its bytes", binary.AppendUvarint([]byte{opAdd}, 5)},
		{"truncated copy", []byte{opCopy}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			patch := craft(t, h, tc.ops)
			var out bytes.Buffer
			_, err := Apply(bytes.NewReader(oldData), int64(len(oldData)), bytes.NewReader(patch), &out)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
		})
	}
}

// bundleFixture writes an old installation and returns it with the bundle
// that updates it and the files the bundle must produce.
func bundleFixture(t *testing.T) (oldDir, bundlePath string, want map[string][]byte) {
	t.Helper()
	oldDir = t.TempDir()
	oldBin := randomBytes(30, 200<<10)
	newBin := joinBytes(oldBin[:50000], randomBytes(31, 2000), oldBin[52000:])
	dll := randomBytes(32, 80<<10)
	inputs := []BundleInput{
		{Name: "JukaHub", Mode: 0o755, Old: oldBin, New: newBin, Delta: true},
		{Name: "SDL2.dll", Mode: 0o644, Old: dll, New: dll, Delta: true},
		{Name: "jukaconfig.json", Mode: 0o644, New: []byte(`{"Version":"0.5.0"}`)},
		{Name: "fonts/Inter-Regular.ttf", New: randomBytes(33, 5000)},
		{Name: "empty.txt", New: nil},
	}
	for _, in := range inputs {
		if !in.Delta {
			continue
		}
		path := filepath.Join(oldDir, filepath.FromSlash(in.Name))
		if err := os.WriteFile(path, in.Old, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	index, err := WriteBundle(&buf, Bundle{Platform: "Trimui", FromTag: "JukaHub-1", ToTag: "JukaHub-2"}, inputs)
	if err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	if len(index.Files) != len(inputs) || index.Format != BundleFormat {
		t.Fatalf("index lists %d files (format %d)", len(index.Files), index.Format)
	}
	bundlePath = filepath.Join(t.TempDir(), AssetName("Trimui", "JukaHub-1"))
	if err := os.WriteFile(bundlePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	want = map[string][]byte{}
	for _, in := range inputs {
		want[in.Name] = in.New
	}
	// An unchanged file costs a patch, not a copy of itself.
	for _, f := range index.Files {
		if f.Name == "SDL2.dll" && f.Length > 512 {
			t.Errorf("an unchanged 80 KiB file took a %d byte patch", f.Length)
		}
	}
	return oldDir, bundlePath, want
}

func TestBundleRebuildsARelease(t *testing.T) {
	oldDir, bundlePath, want := bundleFixture(t)
	outDir := filepath.Join(t.TempDir(), "tree")
	files, err := ApplyBundle(context.Background(), bundlePath, oldDir, outDir)
	if err != nil {
		t.Fatalf("ApplyBundle: %v", err)
	}
	if len(files) != len(want) {
		t.Fatalf("rebuilt %d files, want %d", len(files), len(want))
	}
	for name, data := range want {
		got, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("%s was not rebuilt: %v", name, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("%s differs from the new release", name)
		}
	}
	b, err := ReadBundleFile(bundlePath)
	if err != nil {
		t.Fatalf("ReadBundleFile: %v", err)
	}
	if b.FromTag != "JukaHub-1" || b.ToTag != "JukaHub-2" || b.Platform != "Trimui" {
		t.Fatalf("index metadata = %+v", b)
	}
}

func TestBundleRefusesAnotherInstallation(t *testing.T) {
	oldDir, bundlePath, _ := bundleFixture(t)
	binPath := filepath.Join(oldDir, "JukaHub")
	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	data[1234] ^= 0x01
	if err := os.WriteFile(binPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), "tree")
	if _, err := ApplyBundle(context.Background(), bundlePath, oldDir, outDir); !errors.Is(err, ErrWrongBase) {
		t.Fatalf("err = %v, want ErrWrongBase", err)
	}
	for _, name := range []string{"JukaHub", "JukaHub.jdpart"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err == nil {
			t.Errorf("%s was left behind by a refused bundle", name)
		}
	}

	// A base that is gone is the same refusal.
	if err := os.Remove(binPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBundle(context.Background(), bundlePath, oldDir, filepath.Join(t.TempDir(), "tree")); !errors.Is(err, ErrWrongBase) {
		t.Fatalf("missing base: err = %v, want ErrWrongBase", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ApplyBundle(ctx, bundlePath, oldDir, filepath.Join(t.TempDir(), "tree")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: err = %v", err)
	}
}

// rebuildBundle writes a bundle around an edited index and the original
// patches.
func rebuildBundle(t *testing.T, index Bundle, payload []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	var head [bundleHeaderSize]byte
	copy(head[0:8], BundleMagic)
	head[8] = Version
	binary.BigEndian.PutUint32(head[12:16], uint32(len(raw)))
	return joinBytes(head[:], raw, payload)
}

func TestReadBundleIndexRejectsBadBundles(t *testing.T) {
	_, bundlePath, _ := bundleFixture(t)
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	index, payloadAt, err := ReadBundleIndex(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the fixture does not read back: %v", err)
	}
	payload := data[payloadAt:]
	edit := func(f func(b *Bundle)) []byte {
		b := *index
		b.Files = append([]BundleFile(nil), index.Files...)
		f(&b)
		return rebuildBundle(t, b, payload)
	}
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"wrong magic", joinBytes([]byte("NOTABNDL"), data[8:])},
		{"index past the end", data[:bundleHeaderSize+10]},
		{"truncated payload", data[:len(data)-20]},
		{"escaping name", edit(func(b *Bundle) { b.Files[0].Name = "../evil" })},
		{"absolute name", edit(func(b *Bundle) { b.Files[0].Name = "/etc/passwd" })},
		{"backslash name", edit(func(b *Bundle) { b.Files[0].Name = `bin\JukaHub` })},
		{"duplicate name", edit(func(b *Bundle) { b.Files[1].Name = b.Files[0].Name })},
		{"unknown base", edit(func(b *Bundle) { b.Files[0].Base = "elsewhere" })},
		{"bad checksum", edit(func(b *Bundle) { b.Files[0].NewSHA256 = "xyz" })},
		{"offset outside", edit(func(b *Bundle) { b.Files[0].Offset = int64(len(payload)) })},
		{"no files", edit(func(b *Bundle) { b.Files = nil })},
		{"future format", edit(func(b *Bundle) { b.Format = 99 })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ReadBundleIndex(bytes.NewReader(tc.data), int64(len(tc.data))); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestWriteBundleRejectsBadInputs(t *testing.T) {
	cases := []struct {
		name   string
		inputs []BundleInput
	}{
		{"nothing", nil},
		{"escaping name", []BundleInput{{Name: "../x", New: []byte("x")}}},
		{"duplicate", []BundleInput{{Name: "a", New: []byte("1")}, {Name: "A", New: []byte("2")}}},
		{"delta without a base", []BundleInput{{Name: "a", New: []byte("1"), Delta: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if _, err := WriteBundle(&buf, Bundle{}, tc.inputs); err == nil {
				t.Fatal("a bad bundle was written")
			}
		})
	}
}

func TestValidName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"JukaHub", true},
		{"fonts/Inter-Regular.ttf", true},
		{"libs/brick-stock/a.so", true},
		{"", false},
		{"/abs", false},
		{"../up", false},
		{"a/../b", false},
		{"a//b", false},
		{"./a", false},
		{`a\b`, false},
		{"C:x", false},
		{"a\x00b", false},
	}
	for _, tc := range cases {
		if got := ValidName(tc.name); got != tc.want {
			t.Errorf("ValidName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAssetNames(t *testing.T) {
	platforms := []struct {
		asset, want string
	}{
		{"JukaHub-Trimui.zip", "Trimui"},
		{"JukaHub-Windows.zip", "Windows"},
		{"JukaHub-win-x64.zip", "win-x64"},
		{"JukaHub-Trimui.tgz", "Trimui"},
		{"Other.zip", ""},
		{"JukaHub-.zip", ""},
		{"JukaHub-a b.zip", ""},
		{"JukaHub-Trimui.exe", ""},
	}
	for _, tc := range platforms {
		if got := PlatformFromArchive(tc.asset); got != tc.want {
			t.Errorf("PlatformFromArchive(%q) = %q, want %q", tc.asset, got, tc.want)
		}
	}
	names := []struct {
		platform, tag, want string
	}{
		{"Trimui", "JukaHub-20261017120000", "JukaHub-Trimui-20261017120000.jkdelta"},
		{"Windows", "v0.5.0", "JukaHub-Windows-v0.5.0.jkdelta"},
		{"Trimui", "jukahub-20261017120000", "JukaHub-Trimui-20261017120000.jkdelta"},
		{"Trimui", "", ""},
		{"", "JukaHub-20261017120000", ""},
		{"Trimui", "JukaHub-../x", ""},
	}
	for _, tc := range names {
		if got := AssetName(tc.platform, tc.tag); got != tc.want {
			t.Errorf("AssetName(%q, %q) = %q, want %q", tc.platform, tc.tag, got, tc.want)
		}
	}
}

// ── Fuzzing ──────────────────────────────────────────────────────────────────
//
// Patches and bundles arrive from the network, so the decoders are fuzzed as
// well as unit-tested. `go test` runs each target over its seeds only; the
// weekly workflow mutates them for a short while (go test -fuzz). Sizes are
// capped so one input stays well under a millisecond.

const fuzzMaxInput = 32 << 10

// FuzzDiffApply checks that every pair of files round-trips through a patch.
func FuzzDiffApply(f *testing.F) {
	f.Add([]byte(""), []byte(""))
	f.Add([]byte("0123456789"), []byte("01234abcde"))
	f.Add(randomBytes(40, 3000), joinBytes(randomBytes(40, 3000)[:1000], []byte("inserted"), randomBytes(40, 3000)[1000:]))
	f.Fuzz(func(t *testing.T, oldData, newData []byte) {
		if len(oldData) > fuzzMaxInput || len(newData) > fuzzMaxInput {
			t.Skip()
		}
		roundTrip(t, oldData, newData)
	})
}

// FuzzApplyOps feeds arbitrary operations behind a header that matches the
// base, so the fuzzer reaches the COPY/ADD decoder instead of stopping at the
// base checksum. Whatever the operations say, Apply must never write more than
// the header promises, and a run that decoded to the end (ErrResult or
// success) must have written exactly that many bytes.
func FuzzApplyOps(f *testing.F) {
	end := []byte{opEnd}
	f.Add([]byte("0123456789"), uint16(10), joinBytes(rawCopy(0, 5), rawAdd([]byte("abcde")), end))
	f.Add([]byte("0123456789"), uint16(10), joinBytes(rawCopy(8, 5), end))
	f.Add([]byte(""), uint16(3), joinBytes(rawAdd([]byte("abc")), end))
	f.Add([]byte("abc"), uint16(0), end)
	f.Fuzz(func(t *testing.T, oldData []byte, newSize uint16, ops []byte) {
		if len(oldData) > fuzzMaxInput || len(ops) > fuzzMaxInput {
			t.Skip()
		}
		h := Header{
			OldSize: int64(len(oldData)),
			OldSHA:  sha256.Sum256(oldData),
			NewSize: int64(newSize),
		}
		patch := craft(t, h, ops)
		var out bytes.Buffer
		_, err := Apply(bytes.NewReader(oldData), int64(len(oldData)), bytes.NewReader(patch), &out)
		if int64(out.Len()) > h.NewSize {
			t.Fatalf("wrote %d bytes, the header allows %d", out.Len(), h.NewSize)
		}
		if err == nil || errors.Is(err, ErrResult) {
			if int64(out.Len()) != h.NewSize {
				t.Fatalf("decoded to the end after %d bytes, want %d (err %v)", out.Len(), h.NewSize, err)
			}
			if err == nil && sha256.Sum256(out.Bytes()) != h.NewSHA {
				t.Fatal("Apply accepted a result that does not match its checksum")
			}
			return
		}
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("err = %v, want ErrCorrupt or ErrResult", err)
		}
	})
}

// FuzzApplyRaw hands Apply a whole patch, header included: anything that is not
// a patch for this base must be refused without a panic.
func FuzzApplyRaw(f *testing.F) {
	oldData := []byte("0123456789")
	if patch, err := Diff(oldData, []byte("01234abcde")); err == nil {
		f.Add(oldData, patch)
	}
	f.Add(oldData, []byte(PatchMagic))
	f.Fuzz(func(t *testing.T, oldData, patch []byte) {
		if len(oldData) > fuzzMaxInput || len(patch) > fuzzMaxInput {
			t.Skip()
		}
		var out bytes.Buffer
		h, err := Apply(bytes.NewReader(oldData), int64(len(oldData)), bytes.NewReader(patch), &out)
		if err == nil && (int64(out.Len()) != h.NewSize || sha256.Sum256(out.Bytes()) != h.NewSHA) {
			t.Fatal("Apply accepted a result the header does not describe")
		}
	})
}

// FuzzReadBundleIndex checks that an index which reads back only ever names
// safe paths and patches inside the bundle.
func FuzzReadBundleIndex(f *testing.F) {
	var buf bytes.Buffer
	inputs := []BundleInput{
		{Name: "JukaHub", Mode: 0o755, Old: []byte("old binary"), New: []byte("new binary"), Delta: true},
		{Name: "lib/extra.so", Mode: 0o644, New: []byte("whole file")},
	}
	if _, err := WriteBundle(&buf, Bundle{Platform: "Trimui", FromTag: "JukaHub-1", ToTag: "JukaHub-2"}, inputs); err == nil {
		f.Add(buf.Bytes())
	}
	f.Add([]byte(BundleMagic))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzMaxInput {
			t.Skip()
		}
		b, payloadAt, err := ReadBundleIndex(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
			return
		}
		if payloadAt < bundleHeaderSize || payloadAt > int64(len(data)) {
			t.Fatalf("payload offset %d is outside the %d-byte bundle", payloadAt, len(data))
		}
		payload := int64(len(data)) - payloadAt
		for _, file := range b.Files {
			if !ValidName(file.Name) {
				t.Fatalf("accepted unsafe name %q", file.Name)
			}
			if file.Offset < 0 || file.Length < 0 || file.Offset+file.Length > payload {
				t.Fatalf("%s points outside the bundle: %d+%d of %d", file.Name, file.Offset, file.Length, payload)
			}
		}
	})
}

// FuzzValidName checks ValidName against its contract: a name it accepts can
// never escape the directory it is joined to.
func FuzzValidName(f *testing.F) {
	for _, s := range []string{"JukaHub", "lib/a.so", "../x", "a/../b", "/etc/passwd", `a\b`, "C:x", "a//b", "."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if !ValidName(name) {
			return
		}
		joined := filepath.Join("base", filepath.FromSlash(name))
		if rel, err := filepath.Rel("base", joined); err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			t.Fatalf("accepted %q, which resolves to %q (%v)", name, rel, err)
		}
	})
}
