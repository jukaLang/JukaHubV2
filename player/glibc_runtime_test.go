package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func TestPrivateGlibcPatchGreenOvercast(t *testing.T) {
	script := "#!/bin/bash\n" +
		"pm_platform_helper \"$GAMEDIR/webrtc_stream.aarch64\"\n" +
		"  " + greenOvercastExecLine + "\n" +
		"status=$?\n"
	patched, err := privateGlibcPatchGreenOvercast([]byte(script))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patched), "  "+greenOvercastPrivateExecLine+"\nstatus=$?") ||
		!strings.Contains(string(patched), "pm_platform_helper \"$GAMEDIR/webrtc_stream.aarch64\"") {
		t.Fatalf("launcher setup or game command changed unexpectedly:\n%s", patched)
	}
	if _, err := privateGlibcPatchGreenOvercast([]byte("#!/bin/bash\n")); err == nil {
		t.Fatal("changed upstream launcher should be rejected")
	}
}

func TestPrivateGlibcCatalogOnlyGreenOvercast(t *testing.T) {
	e := mcCatalogEntry{Zip: goZipName, MinGlibc: "2.38"}
	if !privateGlibcCatalogEligible(e, DeviceTrimuiSmartPro) {
		t.Fatal("the tested GreenOvercast build should be offered on TrimUI")
	}
	e.MinGlibc = "2.40"
	if privateGlibcCatalogEligible(e, DeviceTrimuiSmartPro) {
		t.Fatal("the private loader cannot satisfy glibc 2.40")
	}
	e.MinGlibc = "2.38"
	e.Zip = "another-port.zip"
	if privateGlibcCatalogEligible(e, DeviceTrimuiSmartPro) {
		t.Fatal("unverified ports must use normal compatibility rules")
	}
}

func TestPrivateGlibcExtractRejectsTraversal(t *testing.T) {
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	payload := []byte("bad")
	if err := tw.WriteHeader(&tar.Header{
		Name: privateGlibcTree + "../../escape", Typeflag: tar.TypeReg,
		Mode: 0o644, Size: int64(len(payload)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := privateGlibcExtractTar(tar.NewReader(&data), t.TempDir()); err == nil {
		t.Fatal("tar traversal should be rejected")
	}
}

// The newest runtime is several packages: libraries from both arm64 trees go
// flat, bash to bin/, and a soname link (libstdc++.so.6) becomes a copy —
// links are never created, and one that leaves its folder is dropped.
func TestPrivateRuntimeExtractPackages(t *testing.T) {
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	add := func(name string, typ byte, link, body string) {
		h := &tar.Header{Name: name, Typeflag: typ, Linkname: link, Mode: 0o644}
		if typ == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range privateGlibcRequired {
		add("./"+privateGlibcTree+name, tar.TypeReg, "", name)
	}
	add("./"+privateGlibcTree+"libstdc++.so.6.0.33", tar.TypeReg, "", "C++")
	add("./"+privateGlibcTree+"libstdc++.so.6", tar.TypeSymlink, "libstdc++.so.6.0.33", "")
	add("./lib/aarch64-linux-gnu/libgcc_s.so.1", tar.TypeReg, "", "GCC")
	add("./"+privateGlibcTree+"libtinfo.so.6", tar.TypeSymlink, "libtinfo.so.6.4", "")
	add("./"+privateGlibcTree+"libtinfo.so.6.4", tar.TypeReg, "", "TINFO")
	add("./"+privateGlibcTree+"libevil.so", tar.TypeSymlink, "../../../etc/passwd", "")
	add("./usr/share/doc/bash/README", tar.TypeReg, "", "doc")
	add("./bin/bash", tar.TypeReg, "", "BASH")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	x := privateRuntimeExtractor{dest: dest}
	if err := x.add(tar.NewReader(&data)); err != nil {
		t.Fatal(err)
	}
	if err := x.finish(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"libstdc++.so.6": "C++", "libtinfo.so.6": "TINFO", "libgcc_s.so.1": "GCC", "bin/bash": "BASH"} {
		p := filepath.Join(dest, filepath.FromSlash(name))
		if b, err := os.ReadFile(p); err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", name, b, err)
		}
		if info, err := os.Lstat(p); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s is not a plain file", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(dest, "libevil.so")); !os.IsNotExist(err) {
		t.Fatal("a link out of the runtime folder was kept")
	}
	if _, err := os.Stat(filepath.Join(dest, "README")); !os.IsNotExist(err) {
		t.Fatal("a file outside the library trees was copied")
	}
}

// This optional test runs against the exact official package used for the
// device probe without adding a multi-megabyte fixture to the repository.
func TestPrivateGlibcExtractOfficialPackage(t *testing.T) {
	deb := os.Getenv("JUKAHUB_GLIBC_DEB")
	if deb == "" {
		t.Skip("set JUKAHUB_GLIBC_DEB to the pinned Ubuntu arm64 package")
	}
	sum, err := privateGlibcFileSHA(deb)
	if err != nil || sum != privateGlibcSHA {
		t.Fatalf("package checksum: %s, %v", sum, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, deb)
	}))
	defer server.Close()
	downloaded := filepath.Join(t.TempDir(), privateGlibcPackage)
	if err := privateGlibcFetch(context.Background(), downloaded, server.URL); err != nil {
		t.Fatalf("checked download: %v", err)
	}
	stage := t.TempDir()
	if err := privateGlibcExtractDeb(downloaded, stage); err != nil {
		t.Fatal(err)
	}
	for _, name := range privateGlibcRequired {
		if !regularFile(filepath.Join(stage, name)) {
			t.Fatalf("missing %s", name)
		}
	}
}

// Linux packages (linuxpkg_*.go) reads Ubuntu's index: the version order
// decides what is newest and what needs an upgrade.
func TestLinuxPkgVersionCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0", "1.0-0", 0},
		{"1.0~rc1", "1.0", -1},
		{"1.0", "1.0+b1", -1},
		{"1.0-1", "1.0-1ubuntu1", -1},
		{"1:0.9", "2.0", 1},
		{"2.0", "1:0.1", -1},
		{"1.10", "1.9", 1},
		{"1.001", "1.1", 0},
		{"3.12.3-1ubuntu0.8", "3.12.3-1ubuntu0.10", -1},
		{"2.39-0ubuntu8.12", "2.39-0ubuntu8.3", 1},
		{"1.0a", "1.0", 1},
		{"1.0~~", "1.0~", -1},
		{"1.0~", "1.0", -1},
		{"7.94+git20230807.3be01efb1+dfsg-3build2", "7.94+git20230807.3be01efb1+dfsg-3build1", 1},
	} {
		got := linuxPkgVersionCompare(c.a, c.b)
		if (got < 0) != (c.want < 0) || (got > 0) != (c.want > 0) {
			t.Errorf("compare(%q, %q) = %d, want sign of %d", c.a, c.b, got, c.want)
		}
	}
}

func TestLinuxPkgParseDeps(t *testing.T) {
	groups := linuxPkgParseDeps("libc6 (>= 2.34), python3:any (<< 3.13), debconf (>= 0.5) | debconf-2.0, libfoo [amd64], libbar [!amd64] (= 1.0)")
	if len(groups) != 4 {
		t.Fatalf("groups: %+v", groups)
	}
	if d := groups[0][0]; d.Name != "libc6" || d.Op != ">=" || d.Version != "2.34" {
		t.Fatalf("first: %+v", d)
	}
	if d := groups[1][0]; d.Name != "python3" || d.Op != "<<" {
		t.Fatalf(":any qualifier: %+v", d)
	}
	if len(groups[2]) != 2 || groups[2][1].Name != "debconf-2.0" {
		t.Fatalf("alternatives: %+v", groups[2])
	}
	if d := groups[3][0]; d.Name != "libbar" || d.Op != "=" || d.Version != "1.0" {
		t.Fatalf("arch restriction: %+v", groups[3])
	}
	if !linuxPkgProvidesSatisfies("perlapi-5.38.2, perl-base (= 5.38.2-3.2build2)", linuxPkgDep{Name: "perlapi-5.38.2"}) ||
		linuxPkgProvidesSatisfies("awk", linuxPkgDep{Name: "awk", Op: ">=", Version: "1"}) ||
		!linuxPkgProvidesSatisfies("libfoo (= 2.0)", linuxPkgDep{Name: "libfoo", Op: ">=", Version: "1.5"}) {
		t.Fatal("provides semantics")
	}
}

func TestLinuxPkgReadPackages(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	index := "Package: python3\nVersion: 3.12.3-0ubuntu2\nArchitecture: arm64\nPre-Depends: python3-minimal (= 3.12.3-0ubuntu2)\n" +
		"Depends: python3.12 (>= 3.12.3-0~),\n libpython3-stdlib (= 3.12.3-0ubuntu2)\nFilename: pool/main/p/python3-defaults/python3_3.12.3-0ubuntu2_arm64.deb\n" +
		"Size: 23558\nSHA256: " + sum + "\nInstalled-Size: 92\nDescription: interactive high-level object-oriented language (default python3 version)\n Long text.\n\n" +
		"Package: evil\nVersion: 1\nArchitecture: arm64\nFilename: pool/../../etc/passwd\nSize: 1\nSHA256: " + sum + "\n\n" +
		"Package: other-arch\nVersion: 1\nArchitecture: amd64\nFilename: pool/main/o/o.deb\nSize: 1\nSHA256: " + sum + "\n\n" +
		"Package: nosum\nVersion: 1\nArchitecture: all\nFilename: pool/main/n/n.deb\nSize: 1\nSHA256: xyz\n"
	var got []linuxPkgEntry
	skipped, err := linuxPkgReadPackages(strings.NewReader(index), func(e linuxPkgEntry) error {
		got = append(got, e)
		return nil
	})
	if err != nil || skipped != 3 || len(got) != 1 {
		t.Fatalf("got %d entries, %d skipped, %v", len(got), skipped, err)
	}
	e := got[0]
	if e.Name != "python3" || e.InstalledKB != 92 || e.Size != 23558 || !strings.Contains(e.Depends, "libpython3-stdlib") ||
		e.Desc != "interactive high-level object-oriented language (default python3 version)" {
		t.Fatalf("entry: %+v", e)
	}
	back, ok := linuxPkgParseTSV(e.tsv())
	if !ok || back != e {
		t.Fatalf("index line round trip: %+v", back)
	}
}

func TestLinuxPkgParseRelease(t *testing.T) {
	sum := strings.Repeat("0f", 32)
	rel, err := linuxPkgParseRelease([]byte("Origin: Ubuntu\nSuite: noble-updates\nCodename: noble\nDate: Tue, 18 Nov 2025 10:00:00 UTC\nAcquire-By-Hash: yes\n" +
		"MD5Sum:\n 0123 99 main/binary-arm64/Packages.xz\nSHA256:\n " + sum + " 1234 main/binary-arm64/Packages.xz\n " + sum + " 99 universe/binary-arm64/Packages.gz\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Suite != "noble-updates" || !rel.ByHash || rel.Date.Year() != 2025 || len(rel.SHA256) != 2 ||
		rel.SHA256["main/binary-arm64/Packages.xz"] != (linuxPkgSum{SHA256: sum, Size: 1234}) {
		t.Fatalf("release: %+v", rel)
	}
	if _, err := linuxPkgParseRelease([]byte("Suite: noble\nSHA256:\n nothex 12 main/binary-arm64/Packages.xz\n")); err == nil {
		t.Fatal("a malformed SHA256 line must be an error, not a skipped file")
	}
}

// Only Ubuntu's archive key is accepted, whatever signs the Release.
func TestLinuxPkgReleaseSignature(t *testing.T) {
	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(linuxPkgArchiveKey))
	if err != nil || len(keys) != 1 || hex.EncodeToString(keys[0].PrimaryKey.Fingerprint[:]) != linuxPkgKeyFP {
		t.Fatalf("embedded archive key: %v", err)
	}
	other, err := openpgp.NewEntity("Not Ubuntu", "", "x@example.com", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	release := []byte("Suite: noble\nSHA256:\n " + strings.Repeat("00", 32) + " 1 main/binary-arm64/Packages.xz\n")
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, other, bytes.NewReader(release), nil); err != nil {
		t.Fatal(err)
	}
	if err := linuxPkgVerifyRelease(release, sig.Bytes(), time.Now()); err == nil {
		t.Fatal("a Release signed by another key was accepted")
	}
	// The verification clock's floor comes from the signature's own time.
	if made := linuxPkgSigTime(sig.Bytes()); made.IsZero() || time.Since(made) > time.Hour || time.Until(made) > time.Hour {
		t.Fatalf("signature time: %v", made)
	}
}

// A package can never write outside the root, through ".." or a link.
func TestLinuxPkgMemberPaths(t *testing.T) {
	for _, bad := range []string{"../etc/passwd", "./usr/../../x", "/etc/shadow", "usr/bin/a\\b", "usr/./bin", "usr/bin/a\nb", "usr/bin/a\tb"} {
		if _, ok := linuxPkgCleanMember(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if p, ok := linuxPkgCleanMember("./lib/aarch64-linux-gnu/libc.so.6"); !ok || linuxPkgUsrMerge(p) != "usr/lib/aarch64-linux-gnu/libc.so.6" {
		t.Fatalf("merged /usr: %q %v", p, ok)
	}
	for _, c := range []struct{ link, target, want string }{
		{"usr/bin/python3", "python3.12", "usr/bin/python3.12"},
		{"lib/ld-linux-aarch64.so.1", "aarch64-linux-gnu/ld-linux-aarch64.so.1", "usr/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1"},
		{"usr/lib/ssl/certs", "/etc/ssl/certs", "etc/ssl/certs"},
		{"bin/foo", "../usr/bin/foo", "usr/bin/foo"},
	} {
		if got, ok := linuxPkgLinkTarget(c.link, c.target); !ok || got != c.want {
			t.Errorf("link %s -> %s = %q %v, want %q", c.link, c.target, got, ok, c.want)
		}
	}
	for _, target := range []string{"../../../etc/passwd", "/../../root", ""} {
		if _, ok := linuxPkgLinkTarget("usr/bin/x", target); ok {
			t.Errorf("link target %q accepted", target)
		}
	}
}
