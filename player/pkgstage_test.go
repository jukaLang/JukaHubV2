package main

// pkgstage_test.go — the store's install safety: the catalogue's checksum is
// read strictly, a download that does not match it is refused, an archive
// that would write outside its folder is refused, and an update swaps in
// whole, keeps one rollback copy and the user's own files, and leaves the
// installed version alone when anything fails. No network: a local server
// stands in for the download host.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pkgTestZip builds a zip archive from name → content pairs, in order.
func pkgTestZip(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			t.Fatalf("zip %s: %v", e[0], err)
		}
		if _, err := w.Write([]byte(e[1])); err != nil {
			t.Fatalf("zip %s: %v", e[0], err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func pkgTestSum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// pkgTestRoot points the packages folder at a temporary directory for one test.
func pkgTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := pkgInstallDirOverride
	pkgInstallDirOverride = root
	t.Cleanup(func() { pkgInstallDirOverride = old })
	return root
}

func pkgTestRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

func TestPkgParseIntegrity(t *testing.T) {
	good := strings.Repeat("ab", 32)
	cases := []struct {
		entry   string
		sum     string
		size    int64
		problem bool
	}{
		{`{"name":"a"}`, "", 0, false},
		{`{"name":"a","sha256":"` + good + `","size":2048}`, good, 2048, false},
		{`{"name":"a","sha256":"SHA256:` + strings.ToUpper(good) + `","size":"4096"}`, good, 4096, false},
		{`{"name":"a","sha256":"` + good + `","size":"1.5 MB"}`, good, 0, false},
		{`{"name":"a","sha256":""}`, "", 0, false},
		{`{"name":"a","sha256":null}`, "", 0, false},
		{`{"name":"a","sha256":"abc123"}`, "", 0, true},
		{`{"name":"a","sha256":"` + strings.Repeat("zz", 32) + `"}`, "", 0, true},
		{`{"name":"a","sha256":42}`, "", 0, true},
		// A size without a hash is display-only: it is not enforced.
		{`{"name":"a","size":2048}`, "", 0, false},
	}
	for _, c := range cases {
		var p Package
		pkgApplyIntegrity(&p, json.RawMessage(c.entry))
		if p.SHA256 != c.sum || p.DownloadSize != c.size || (p.HashProblem != "") != c.problem {
			t.Errorf("%s: sha %q size %d problem %q", c.entry, p.SHA256, p.DownloadSize, p.HashProblem)
		}
	}
	entries, err := pkgParseCatalog([]byte(`[{"name":"Demo","download":"https://example.com/d.zip","sha256":"` + good + `"}]`))
	if err != nil || len(entries) != 1 || entries[0].Pkg.SHA256 != good {
		t.Fatalf("the catalogue parser did not carry the hash: %+v %v", entries, err)
	}
}

// A download that is not what the catalogue published is refused and leaves
// nothing behind; one that is, is marked verified.
func TestPkgDownloadChecksTheCatalogue(t *testing.T) {
	body := []byte("PK\x03\x04 the package")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "x.download")
	ctx := context.Background()

	wrong := Package{SHA256: strings.Repeat("00", 32)}
	if _, err := pkgDownloadTo(ctx, srv.URL+"/x.zip", dest, wrong, nil); !errors.Is(err, errPkgChecksum) {
		t.Fatalf("a wrong hash was not refused: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("a refused download was left on the card")
	}
	short := Package{SHA256: pkgTestSum(body), DownloadSize: int64(len(body)) + 10}
	if _, err := pkgDownloadTo(ctx, srv.URL+"/x.zip", dest, short, nil); !errors.Is(err, errPkgSize) {
		t.Fatalf("a wrong size was not refused: %v", err)
	}
	long := Package{SHA256: pkgTestSum(body), DownloadSize: 4}
	if _, err := pkgDownloadTo(ctx, srv.URL+"/x.zip", dest, long, nil); !errors.Is(err, errPkgSize) {
		t.Fatalf("a download longer than published was not refused: %v", err)
	}
	right := Package{SHA256: pkgTestSum(body), DownloadSize: int64(len(body))}
	res, err := pkgDownloadTo(ctx, srv.URL+"/x.zip", dest, right, nil)
	if err != nil || !res.Verified || res.SHA256 != right.SHA256 || pkgTestRead(t, dest) != string(body) {
		t.Fatalf("a matching download: %+v %v", res, err)
	}
}

// No archive entry can land outside the folder it is unpacked into.
func TestPkgExtractRefusesUnsafeEntries(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]byte{
		"climb":     pkgTestZip(t, [2]string{"ok.txt", "x"}, [2]string{"../pkg-escape.txt", "x"}),
		"deep":      pkgTestZip(t, [2]string{"a/../../pkg-escape.txt", "x"}),
		"backslash": pkgTestZip(t, [2]string{`..\pkg-escape.txt`, "x"}),
		"absolute":  pkgTestZip(t, [2]string{"/pkg-escape.txt", "x"}),
		"twice":     pkgTestZip(t, [2]string{"same.txt", "1"}, [2]string{"same.txt", "2"}),
	}
	var link bytes.Buffer
	zw := zip.NewWriter(&link)
	hdr := &zip.FileHeader{Name: "link"}
	hdr.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("/etc/passwd"))
	_ = zw.Close()
	cases["symlink"] = link.Bytes()

	for name, data := range cases {
		archive := filepath.Join(dir, name+".zip")
		if err := os.WriteFile(archive, data, 0o644); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(dir, name, "stage")
		if _, err := pkgExtractZip(context.Background(), archive, dest); err == nil {
			t.Errorf("%s: the archive was unpacked", name)
		}
		if _, err := os.Lstat(filepath.Join(dir, name, "pkg-escape.txt")); err == nil {
			t.Errorf("%s: a file escaped the staging folder", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "pkg-escape.txt")); err == nil {
		t.Error("a file escaped to the temporary folder")
	}

	good := filepath.Join(dir, "good.zip")
	if err := os.WriteFile(good, pkgTestZip(t, [2]string{"top/a.txt", "a"}, [2]string{"top/sub/b.txt", "b"}), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := pkgExtractZip(context.Background(), good, filepath.Join(dir, "good"))
	if err != nil || len(files) != 2 || files[0] != "top/a.txt" || files[1] != "top/sub/b.txt" {
		t.Fatalf("a good archive: %v %v", files, err)
	}
	if pkgTestRead(t, filepath.Join(dir, "good", "top", "sub", "b.txt")) != "b" {
		t.Fatal("a good archive's file was not written")
	}
}

// An update swaps in whole: the old version becomes the one rollback copy, the
// user's own file follows the installed version, Roll back swaps the two, and
// an install that fails — a wrong checksum, an unsafe archive — changes
// nothing.
func TestPkgInstallSwapKeepsRollbackAndUserFiles(t *testing.T) {
	root := pkgTestRoot(t)
	v1 := pkgTestZip(t, [2]string{"a.txt", "one"}, [2]string{"b.txt", "only in one"})
	v2 := pkgTestZip(t, [2]string{"a.txt", "two"}, [2]string{"c.txt", "only in two"})
	bad := pkgTestZip(t, [2]string{"a.txt", "three"}, [2]string{"../pkg-escape.txt", "x"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.zip":
			_, _ = w.Write(v1)
		case "/v2.zip":
			_, _ = w.Write(v2)
		case "/bad.zip":
			_, _ = w.Write(bad)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	live := filepath.Join(root, "Demo")
	back := filepath.Join(root, pkgRollbackDir, "Demo")

	p1 := Package{Name: "Demo", Version: "1.0", Download: srv.URL + "/v1.zip", SHA256: pkgTestSum(v1)}
	res, err := pkgInstallFrom(ctx, root, p1, nil, nil)
	if err != nil || res.Kept || !res.Verified {
		t.Fatalf("first install: %+v %v", res, err)
	}
	if err := os.WriteFile(filepath.Join(live, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	p2 := Package{Name: "Demo", Version: "2.0", Download: srv.URL + "/v2.zip", SHA256: pkgTestSum(v2)}
	res, err = pkgInstallFrom(ctx, root, p2, nil, nil)
	if err != nil || !res.Kept || res.Carried != 1 {
		t.Fatalf("update: %+v %v", res, err)
	}
	for path, want := range map[string]string{
		filepath.Join(live, "a.txt"):     "two",
		filepath.Join(live, "c.txt"):     "only in two",
		filepath.Join(live, "notes.txt"): "mine",
		filepath.Join(live, "b.txt"):     "<missing>",
		filepath.Join(back, "a.txt"):     "one",
		filepath.Join(back, "b.txt"):     "only in one",
		filepath.Join(back, "notes.txt"): "<missing>",
	} {
		if got := pkgTestRead(t, path); got != want {
			t.Errorf("after the update %s = %q, want %q", path, got, want)
		}
	}
	if got := pkgScanRollbacks(root); got["Demo"] != "1.0" {
		t.Errorf("rollback copies = %v", got)
	}

	// A failed install leaves both versions exactly where they were.
	wrong := Package{Name: "Demo", Version: "3.0", Download: srv.URL + "/v2.zip", SHA256: strings.Repeat("11", 32)}
	if _, err := pkgInstallFrom(ctx, root, wrong, nil, nil); !errors.Is(err, errPkgChecksum) {
		t.Fatalf("a wrong checksum: %v", err)
	}
	unsafe := Package{Name: "Demo", Version: "3.0", Download: srv.URL + "/bad.zip", SHA256: pkgTestSum(bad)}
	if _, err := pkgInstallFrom(ctx, root, unsafe, nil, nil); err == nil {
		t.Fatal("an archive that climbs out was installed")
	}
	if pkgTestRead(t, filepath.Join(live, "a.txt")) != "two" || pkgTestRead(t, filepath.Join(back, "a.txt")) != "one" {
		t.Fatal("a failed install changed the installed version or its rollback copy")
	}
	if _, err := os.Lstat(filepath.Join(root, "pkg-escape.txt")); err == nil {
		t.Fatal("a file escaped the packages folder")
	}
	if _, err := os.Lstat(filepath.Join(root, pkgStagingDir, "Demo")); err == nil {
		t.Fatal("a failed install left its staging folder")
	}

	m, err := pkgRollBack(root, "Demo")
	if err != nil || m.Version != "1.0" {
		t.Fatalf("roll back: %+v %v", m, err)
	}
	for path, want := range map[string]string{
		filepath.Join(live, "a.txt"):     "one",
		filepath.Join(live, "b.txt"):     "only in one",
		filepath.Join(live, "notes.txt"): "mine",
		filepath.Join(live, "c.txt"):     "<missing>",
		filepath.Join(back, "a.txt"):     "two",
		filepath.Join(back, "notes.txt"): "<missing>",
	} {
		if got := pkgTestRead(t, path); got != want {
			t.Errorf("after the rollback %s = %q, want %q", path, got, want)
		}
	}
}

// A swap cut short between its two renames is finished at the next start; a
// package folder the user deleted is not brought back from its rollback copy.
func TestPkgRecoverInterruptedSwap(t *testing.T) {
	root := pkgTestRoot(t)
	mk := func(dir, file, content string, m *pkgMarker) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if m != nil {
			if err := pkgWriteMarker(dir, *m); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Crash after the old version moved aside, before the new one moved in.
	a := pkgPathsFor(root, "Alpha")
	mk(a.back, "a.txt", "old", &pkgMarker{Name: "Alpha", Version: "1"})
	mk(a.stage, "a.txt", "new", &pkgMarker{Name: "Alpha", Version: "2", Origin: pkgOriginInstall})
	if err := os.WriteFile(a.journal, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Crash with the old version aside and no complete new one.
	b := pkgPathsFor(root, "Beta")
	mk(b.back, "b.txt", "old", &pkgMarker{Name: "Beta", Version: "1"})
	mk(b.stage, "half.txt", "half", nil)
	if err := os.WriteFile(b.journal, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// The user deleted the package folder; only its rollback copy is left.
	c := pkgPathsFor(root, "Gamma")
	mk(c.back, "c.txt", "old", &pkgMarker{Name: "Gamma", Version: "1"})

	pkgRecoverInterrupted(root)

	if got := pkgTestRead(t, filepath.Join(a.live, "a.txt")); got != "new" {
		t.Errorf("Alpha's interrupted install was not finished: %q", got)
	}
	if got := pkgTestRead(t, filepath.Join(b.live, "b.txt")); got != "old" {
		t.Errorf("Beta's installed version was not put back: %q", got)
	}
	if pkgIsDir(c.live) {
		t.Error("a deleted package came back from its rollback copy")
	}
	for _, p := range []string{a.stage, a.journal, b.stage, b.journal} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s survived recovery", p)
		}
	}
	if meta := pkgLoadInstalledMeta(); meta["Alpha"].Version != "2" || meta["Beta"].Version != "1" {
		t.Errorf("the installed-version record was not updated: %+v", meta)
	}
}

func TestPkgDownloadFileName(t *testing.T) {
	cases := map[string]string{
		"https://example.com/files/tool.juka":          "tool.juka",
		"https://example.com/files/My%20Tool.juka?x=1": "My_Tool.juka",
		"https://example.com/":                         "Demo",
		"https://example.com/.jukahub-package.json":    "_.jukahub-package.json",
		"https://example.com/..":                       "Demo",
	}
	for raw, want := range cases {
		if got := pkgDownloadFileName(Package{Name: "Demo", Download: raw}); got != want {
			t.Errorf("pkgDownloadFileName(%q) = %q, want %q", raw, got, want)
		}
	}
}

// The store's bookkeeping in staging never shares a name with a package's
// staging folder: a package called "x.swap" does not meet the journal of "x".
func TestPkgStagingNamesNeverCollide(t *testing.T) {
	root := t.TempDir()
	x := pkgPathsFor(root, packageDirName("x"))
	swapNamed := pkgPathsFor(root, packageDirName("x.swap"))
	downloadNamed := pkgPathsFor(root, packageDirName("x.download"))
	if x.journal == swapNamed.stage || x.download == downloadNamed.stage {
		t.Fatalf("staging names collide: %q %q %q %q", x.journal, swapNamed.stage, x.download, downloadNamed.stage)
	}
	if key, ok := pkgJournalKey(filepath.Base(x.journal)); !ok || key != "x" {
		t.Errorf("the journal %q reads as %q %v", filepath.Base(x.journal), key, ok)
	}
	for _, name := range []string{"x.swap", ".download-x", ".swap-", ".swap-.hidden", "x"} {
		if _, ok := pkgJournalKey(name); ok {
			t.Errorf("%q read as a journal", name)
		}
	}
}

// A swap that stopped between its renames and could not be undone (the old
// version in the rollback slot, the new one complete in staging, the journal
// left) is finished before the next install of the package, so the user's
// file reaches the version that ends up installed instead of staying behind
// in a rollback copy that a later update would delete.
func TestPkgInstallFinishesAStrandedSwapFirst(t *testing.T) {
	root := pkgTestRoot(t)
	v3 := pkgTestZip(t, [2]string{"a.txt", "three"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(v3)
	}))
	defer srv.Close()
	paths := pkgPathsFor(root, "Demo")
	for dir, files := range map[string][][2]string{
		paths.back:  {{"a.txt", "one"}, {"notes.txt", "mine"}},
		paths.stage: {{"a.txt", "two"}},
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f[0]), []byte(f[1]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := pkgWriteMarker(paths.back, pkgMarker{Name: "Demo", Version: "1.0", Listed: true, Files: []string{"a.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := pkgWriteMarker(paths.stage, pkgMarker{Name: "Demo", Version: "2.0", Origin: pkgOriginInstall, Listed: true, Files: []string{"a.txt"}, CarryPending: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.journal, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	p3 := Package{Name: "Demo", Version: "3.0", Download: srv.URL + "/v3.zip", SHA256: pkgTestSum(v3)}
	res, err := pkgInstallFrom(context.Background(), root, p3, nil, nil)
	if err != nil || !res.Kept {
		t.Fatalf("an install over a stranded swap: %+v %v", res, err)
	}
	for path, want := range map[string]string{
		filepath.Join(paths.live, "a.txt"):     "three",
		filepath.Join(paths.live, "notes.txt"): "mine",
		filepath.Join(paths.back, "a.txt"):     "two",
		filepath.Join(paths.back, "notes.txt"): "<missing>",
	} {
		if got := pkgTestRead(t, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if pkgExists(paths.journal) || pkgExists(paths.stage) {
		t.Error("the stranded swap's journal or copy survived")
	}
}

// The toast after a failed swap says what is really on the card, including an
// old rollback copy the swap kept aside before it failed.
func TestPkgInstallFailureSaysWhatIsInstalled(t *testing.T) {
	p := Package{Name: "Demo"}
	cause := errors.New("put the new version in place: access denied")
	aside := filepath.Join("packages", pkgRollbackDir, pkgKeptPrefix+"Demo-abc")
	cases := []struct {
		err  *pkgStepError
		card string
		tail string
	}{
		{&pkgStepError{Step: pkgStepSwap, Err: cause, Stranded: true}, "Stopped halfway", "install it again, or restart JukaHub and open Packages, to finish it"},
		{&pkgStepError{Step: pkgStepSwap, Err: cause, Installed: true}, "Could not replace the old version", "the installed version is unchanged"},
		{&pkgStepError{Step: pkgStepSwap, Err: cause}, "Not installed", "— not installed"},
		{&pkgStepError{Step: pkgStepSwap, Err: cause, Installed: true, Aside: aside}, "Could not replace the old version", "unchanged" + pkgAsideNote},
		{&pkgStepError{Step: pkgStepSwap, Err: cause, Stranded: true, Aside: aside}, "Stopped halfway", "your files are kept" + pkgAsideNote},
	}
	for _, c := range cases {
		card, toast := pkgInstallFailure(p, c.err)
		if card != c.card || !strings.Contains(toast, c.tail) {
			t.Errorf("stranded=%v installed=%v aside=%q: card %q, toast %q", c.err.Stranded, c.err.Installed, c.err.Aside, card, toast)
		}
		if c.err.Aside == "" && strings.Contains(toast, pkgAsideNote) {
			t.Errorf("stranded=%v installed=%v: toast %q mentions a copy kept aside", c.err.Stranded, c.err.Installed, toast)
		}
	}
}

// A removal trashes the rollback copy, the package folder and the staging copy
// back to back, all with one base name: every one of them gets into the trash
// under a name of its own, even when the clock has not moved in between
// (Windows' moves in steps of up to 15.6 ms, and the second rename used to
// meet the first one's name and fail).
func TestPkgTrashNamesNeverCollide(t *testing.T) {
	root := pkgTestRoot(t)
	seen := map[string]bool{}
	for _, parent := range []string{"a", "b", "c"} {
		dir := filepath.Join(root, parent, "Demo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(parent), 0o644); err != nil {
			t.Fatal(err)
		}
		pkgOpMu.Lock()
		dest, err := pkgTrash(root, dir)
		pkgOpMu.Unlock()
		if err != nil {
			t.Fatalf("trashing %s right after another Demo: %v", dir, err)
		}
		if seen[dest] || pkgExists(dir) || pkgTestRead(t, filepath.Join(dest, "f.txt")) != parent {
			t.Fatalf("%s went to %s (taken before: %v)", dir, dest, seen[dest])
		}
		seen[dest] = true
	}
}

// An installed version whose marker cannot be read cannot say whether its
// rollback copy still holds the user's files, so the next update keeps that
// copy aside whole instead of deleting it unchecked.
func TestPkgUnreadableMarkerKeepsTheRollbackCopy(t *testing.T) {
	root := pkgTestRoot(t)
	v1 := pkgTestZip(t, [2]string{"a.txt", "one"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(v1)
	}))
	defer srv.Close()
	ctx := context.Background()
	paths := pkgPathsFor(root, "Demo")
	p := Package{Name: "Demo", Version: "1.0", Download: srv.URL + "/v1.zip"}
	for i := 0; i < 2; i++ {
		if _, err := pkgInstallFrom(ctx, root, p, nil, nil); err != nil {
			t.Fatalf("install %d: %v", i+1, err)
		}
	}
	if !pkgIsDir(paths.back) {
		t.Fatal("the second install kept no rollback copy")
	}
	if err := os.WriteFile(filepath.Join(paths.back, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.live, pkgMarkerFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := pkgInstallFrom(ctx, root, p, nil, nil)
	if err != nil || res.Aside == "" {
		t.Fatalf("the update over an unreadable marker: %+v %v", res, err)
	}
	if pkgTestRead(t, filepath.Join(res.Aside, "notes.txt")) != "mine" {
		t.Fatal("the rollback copy was not kept whole")
	}
	if got := pkgKeptCopies(root, "Demo"); len(got) != 1 || got[0] != res.Aside {
		t.Errorf("kept copies = %v, want [%s]", got, res.Aside)
	}
	if pkgTestRead(t, filepath.Join(paths.live, "a.txt")) != "one" || !pkgIsDir(paths.back) {
		t.Error("the update did not put the new version in place with the old one as the rollback copy")
	}
}

// Removing a package takes its folder, its rollback copy, a copy kept aside
// and its staging leftovers — each by rename into the trash first — and
// nothing of another package.
func TestPkgRemoveDirTakesEveryCopy(t *testing.T) {
	root := pkgTestRoot(t)
	paths := pkgPathsFor(root, "Demo")
	kept := filepath.Join(root, pkgRollbackDir, pkgKeptPrefix+"Demo-abc")
	other := filepath.Join(root, pkgRollbackDir, pkgKeptPrefix+"Demo-2-abc")
	for _, dir := range []string{paths.live, paths.back, paths.stage, kept, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths.journal, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pkgRemoveDir("Demo"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, p := range []string{paths.live, paths.back, paths.stage, paths.journal, kept} {
		if pkgExists(p) {
			t.Errorf("%s survived the removal", p)
		}
	}
	if !pkgExists(other) {
		t.Error("the removal took the kept copy of another package (Demo-2)")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, pkgTrashDir)); len(entries) != 0 {
		t.Errorf("the trash still holds %d entries", len(entries))
	}
}

// User files that will not move out of the rollback copy stop an update, which
// names the file, until pkgCarryGiveUp updates have failed; then that copy is
// kept aside whole and the update goes ahead. A removal takes the kept copy.
func TestPkgStuckUserFilesAreKeptAside(t *testing.T) {
	root := pkgTestRoot(t)
	v1 := pkgTestZip(t, [2]string{"a.txt", "one"})
	v2 := pkgTestZip(t, [2]string{"a.txt", "two"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.zip" {
			_, _ = w.Write(v1)
			return
		}
		_, _ = w.Write(v2)
	}))
	defer srv.Close()
	ctx := context.Background()
	live := filepath.Join(root, "Demo")
	back := filepath.Join(root, pkgRollbackDir, "Demo")
	if _, err := pkgInstallFrom(ctx, root, Package{Name: "Demo", Version: "1.0", Download: srv.URL + "/v1.zip"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	move := pkgCarryMove
	pkgCarryMove = func(r *os.Root, from, to string) error {
		if filepath.Base(from) == "notes.txt" {
			return errors.New("in use")
		}
		return move(r, from, to)
	}
	t.Cleanup(func() { pkgCarryMove = move })

	if _, err := pkgInstallFrom(ctx, root, Package{Name: "Demo", Version: "2.0", Download: srv.URL + "/v2.zip"}, nil, nil); err != nil {
		t.Fatalf("the update itself: %v", err)
	}
	if pkgTestRead(t, filepath.Join(back, "notes.txt")) != "mine" {
		t.Fatal("the file that would not move left the rollback copy")
	}
	p3 := Package{Name: "Demo", Version: "3.0", Download: srv.URL + "/v2.zip"}
	for i := 1; i < pkgCarryGiveUp; i++ {
		if _, err := pkgInstallFrom(ctx, root, p3, nil, nil); err == nil || !strings.Contains(err.Error(), "notes.txt") {
			t.Fatalf("update attempt %d: %v, want a refusal naming notes.txt", i, err)
		}
		if pkgTestRead(t, filepath.Join(back, "notes.txt")) != "mine" || pkgTestRead(t, filepath.Join(live, "a.txt")) != "two" {
			t.Fatalf("refused update attempt %d changed the card", i)
		}
	}
	res, err := pkgInstallFrom(ctx, root, p3, nil, nil)
	if err != nil || res.Aside == "" {
		t.Fatalf("update after %d refusals: %+v %v", pkgCarryGiveUp-1, res, err)
	}
	if pkgTestRead(t, filepath.Join(res.Aside, "notes.txt")) != "mine" {
		t.Fatal("the copy kept aside does not hold the file")
	}
	if got := pkgKeptCopies(root, "Demo"); len(got) != 1 {
		t.Fatalf("kept copies = %v", got)
	}
	if got := pkgScanRollbacks(root); len(got) != 1 || got["Demo"] != "2.0" {
		t.Errorf("rollback copies = %v (a kept copy must not read as one)", got)
	}
	if err := pkgRemoveDir("Demo"); err != nil {
		t.Fatal(err)
	}
	if got := pkgKeptCopies(root, "Demo"); len(got) != 0 || pkgExists(live) || pkgExists(back) {
		t.Fatalf("the removal left %v", got)
	}
}

// The sheet's button says which way the kept copy goes: back to an older
// version, or forward again to the newer one a rollback replaced.
func TestPkgRollbackLabelFollowsTheDirection(t *testing.T) {
	entry := pkgEntry{Pkg: Package{Name: "Demo", Version: "2.0"}}
	in := pkgModelInput{
		Installed: map[string]bool{"Demo": true},
		Meta:      map[string]pkgInstalledMeta{"Demo": {Name: "Demo", Version: "1.0"}},
		Rollbacks: map[string]string{"Demo": "2.0"},
	}
	if it := pkgNewItem(pkgKindPackage, entry, "Demo", in); !it.RollbackNewer || it.RollbackLabel != "Switch back to v2.0" {
		t.Errorf("after a rollback: newer=%v label %q", it.RollbackNewer, it.RollbackLabel)
	}
	in.Meta["Demo"] = pkgInstalledMeta{Name: "Demo", Version: "2.0"}
	in.Rollbacks["Demo"] = "1.0"
	if it := pkgNewItem(pkgKindPackage, entry, "Demo", in); it.RollbackNewer || it.RollbackLabel != "Roll back to v1.0" {
		t.Errorf("after an update: newer=%v label %q", it.RollbackNewer, it.RollbackLabel)
	}
}

// The download refuses a card without room for it before writing a byte, and
// only a real zip signature reads as an archive.
func TestPkgZipSignature(t *testing.T) {
	cases := map[string]bool{
		"PK\x03\x04": true,
		"PK\x05\x06": true,
		"PKAB":       false,
		"PK\x07\x08": false,
		"<htm":       false,
	}
	for head, want := range cases {
		var b [4]byte
		copy(b[:], head)
		if got := pkgZipSignature(b); got != want {
			t.Errorf("pkgZipSignature(%q) = %v, want %v", head, got, want)
		}
	}
	if err := pkgCheckFreeSpace(t.TempDir(), 1<<62); err == nil {
		t.Error("a download larger than any card was not refused")
	}
}
