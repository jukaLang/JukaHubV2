package main

// unzip_bounds_test.go covers the bounds of the Files extraction
// (files_extract.go): an archive the user picked is not signed or checked by
// anything, so the entry count, the space it unpacks to and its names are what
// stand between it and the card. Every refusal must leave the folder exactly
// as it was — no output and no staging folder.

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeUnzipTestArchive(t *testing.T, path string, fill func(zw *zip.Writer)) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	fill(zw)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// unzipTestRun extracts archive beside itself the way the queue does.
func unzipTestRun(archive string, mode feExtractMode) feExtractOutcome {
	return feExtractRun(context.Background(), feExtractJob{Archive: archive, Dest: filepath.Dir(archive), Mode: mode}, nil)
}

// unzipTestNames lists dir, sorted.
func unzipTestNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// unzipTestOnly fails unless dir holds exactly want.
func unzipTestOnly(t *testing.T, dir string, want ...string) {
	t.Helper()
	got := unzipTestNames(t, dir)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s holds %q, want %q", dir, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s holds %q, want %q", dir, got, want)
		}
	}
}

func TestUnzipRefusesTooManyEntries(t *testing.T) {
	prev := feExtractMaxEntries
	feExtractMaxEntries = 3
	t.Cleanup(func() { feExtractMaxEntries = prev })

	dir := t.TempDir()
	archive := filepath.Join(dir, "many.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	})

	if out := unzipTestRun(archive, feExtractSmart); out.Err == nil {
		t.Fatal("an archive over the entry limit must be refused")
	}
	unzipTestOnly(t, dir, "many.zip")
}

func TestUnzipRefusesArchiveThatDoesNotFit(t *testing.T) {
	prev := feExtractFreeSpace
	feExtractFreeSpace = func(string) (int64, error) { return feExtractSpaceMargin + 1<<20, nil }
	t.Cleanup(func() { feExtractFreeSpace = prev })

	dir := t.TempDir()
	archive := filepath.Join(dir, "big.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		for _, name := range []string{"a.bin", "b.bin"} {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(make([]byte, 600<<10)); err != nil {
				t.Fatal(err)
			}
		}
	})

	out := unzipTestRun(archive, feExtractSmart)
	var space feNoSpaceError
	if !errors.As(out.Err, &space) {
		t.Fatalf("an archive bigger than the free space must be refused, got %v", out.Err)
	}
	unzipTestOnly(t, dir, "big.zip")
}

func TestUnzipSkipsSymlinkEntries(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "links.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		hdr := &zip.FileHeader{Name: "escape", Method: zip.Store}
		hdr.SetMode(os.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		// A link entry carries its target as content, not file data.
		if _, err := w.Write([]byte("../outside")); err != nil {
			t.Fatal(err)
		}
		real, err := zw.Create("real.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := real.Write([]byte("kept")); err != nil {
			t.Fatal(err)
		}
	})

	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatalf("a zip with a link entry should unpack its regular files: %v", out.Err)
	}
	if out.Stats.Skips.Links != 1 {
		t.Fatalf("skipped links = %d, want 1", out.Stats.Skips.Links)
	}
	// One regular file is one top-level item: it lands beside the archive.
	unzipTestOnly(t, dir, "links.zip", "real.txt")
	body, err := os.ReadFile(filepath.Join(dir, "real.txt"))
	if err != nil {
		t.Fatalf("regular entry missing: %v", err)
	}
	if string(body) != "kept" {
		t.Fatalf("regular entry content = %q, want %q", body, "kept")
	}
}

func TestUnzipNeverWritesOutsideTheFolder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "inner")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "slip.zip")
	writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
		for _, name := range []string{"../escape.txt", "../../escape2.txt", "/abs.txt", "ok.txt"} {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("boom")); err != nil {
				t.Fatal(err)
			}
		}
	})

	out := unzipTestRun(archive, feExtractSmart)
	if out.Err != nil {
		t.Fatalf("the safe entries should still unpack: %v", out.Err)
	}
	if out.Stats.Skips.Unsafe != 2 {
		t.Fatalf("unsafe entries skipped = %d, want 2", out.Stats.Skips.Unsafe)
	}
	for _, escaped := range []string{filepath.Join(root, "escape.txt"), filepath.Join(filepath.Dir(root), "escape2.txt")} {
		if _, err := os.Stat(escaped); err == nil {
			t.Fatalf("traversal file %s was written", escaped)
		}
	}
	// "/abs.txt" loses its slash and lands inside, beside ok.txt, so the two
	// top-level items go into a folder named after the archive.
	unzipTestOnly(t, root, "inner")
	unzipTestOnly(t, dir, "slip", "slip.zip")
	unzipTestOnly(t, filepath.Join(dir, "slip"), "abs.txt", "ok.txt")
}

// An experience pack is a zip someone else made: every entry name must stay
// inside the pack, whatever prefix or separator it was written with.
func TestXpackZipEntryNamesStayInsideThePack(t *testing.T) {
	cases := []struct {
		name, prefix, want string
		bad                bool
	}{
		{"pack.json", "", "pack.json", false},
		{"art/wall.png", "", "art/wall.png", false},
		{"mypack/art/wall.png", "mypack/", "art/wall.png", false},
		{"other/file.txt", "mypack/", "", false},
		{"art/", "", "", false},
		{"../evil", "", "", true},
		{"/etc/passwd", "", "", true},
		{`a\..\b`, "", "", true},
		{"C:/x", "", "", true},
		{"a/../../b", "", "", true},
	}
	for _, c := range cases {
		got, err := xpackZipEntryName(c.name, c.prefix)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("xpackZipEntryName(%q, %q) = %q, %v; want %q, refused %v", c.name, c.prefix, got, err, c.want, c.bad)
		}
	}
}

// Java (javart_install.go, javarun.go): Temurin's release list, checksum
// files, jar manifests and class files all come from outside, and a Java
// archive must never write outside its stage folder.
func TestJavaInstallAndFileParsers(t *testing.T) {
	const dl = "https://github.com/adoptium/temurin25-binaries/releases/download/"
	asset := func(tag, name string) string {
		return `{"name":"` + name + `","browser_download_url":"` + dl + tag + `/` + name + `","size":52000000}`
	}
	jre := "OpenJDK25U-jre_aarch64_linux_hotspot_25.0.1_8.tar.gz"
	releases := `[
	{"tag_name":"jdk-25.0.2+3-ea-beta","prerelease":true,"assets":[` +
		asset("jdk-25.0.2%2B3-ea-beta", "OpenJDK25U-jre_aarch64_linux_hotspot_ea_25-0-2-3.tar.gz") + `,` +
		asset("jdk-25.0.2%2B3-ea-beta", "OpenJDK25U-jre_aarch64_linux_hotspot_ea_25-0-2-3.tar.gz.sha256.txt") + `]},
	{"tag_name":"jdk-25.0.2+10","assets":[` +
		asset("jdk-25.0.2%2B10", "OpenJDK25U-jre_aarch64_linux_hotspot_2025-10-22-07-16.tar.gz") + `,` +
		asset("jdk-25.0.2%2B10", "OpenJDK25U-jre_aarch64_linux_hotspot_2025-10-22-07-16.tar.gz.sha256.txt") + `,` +
		asset("jdk-25.0.2%2B10", "OpenJDK25U-jre_aarch64_linux_hotspot_25.0.2_10.tar.gz") + `,` +
		asset("jdk-25.0.2%2B10", "OpenJDK25U-jre_aarch64_alpine-linux_hotspot_25.0.2_10.tar.gz") + `,` +
		asset("jdk-25.0.2%2B10", "OpenJDK25U-jre_aarch64_alpine-linux_hotspot_25.0.2_10.tar.gz.sha256.txt") + `]},
	{"tag_name":"jdk-25.0.1+8","assets":[` +
		asset("jdk-25.0.1%2B8", "OpenJDK25U-debugimage_aarch64_linux_hotspot_25.0.1_8.tar.gz") + `,` +
		asset("jdk-25.0.1%2B8", "OpenJDK25U-debugimage_aarch64_linux_hotspot_25.0.1_8.tar.gz.sha256.txt") + `,` +
		asset("jdk-25.0.1%2B8", jre) + `,` + asset("jdk-25.0.1%2B8", jre+".sha256.txt") + `]}]`
	got, err := javaPickAsset([]byte(releases), 25, javaKindJRE, "linux", "aarch64")
	if err != nil || got.Name != jre || got.Tag != "jdk-25.0.1+8" || got.SumURL != got.URL+".sha256.txt" {
		t.Fatalf("picked %+v, %v; want %s (prerelease, nightly, no-checksum and alpine skipped)", got, err, jre)
	}
	for _, c := range []struct {
		out     string
		major   int
		version string
	}{
		{"openjdk version \"25.0.1\" 2025-10-21 LTS\nOpenJDK Runtime Environment Temurin-25.0.1+8 (build 25.0.1+8-LTS)\n", 25, "25.0.1+8"},
		{"openjdk version \"21.0.5\" 2024-10-15 LTS\n", 21, "21.0.5"},
		{"openjdk version \"17\" 2021-09-14\n", 17, "17"},
		{"openjdk version \"25\" 2025-09-16\n", 25, "25"},
		{"Picked up _JAVA_OPTIONS: -Xmx64m\nopenjdk version \"1.8.0_472\"\nOpenJDK Runtime Environment (Temurin)(build 1.8.0_472-b08)\n", 8, "1.8.0_472-b08"},
	} {
		if major, version, ok := javaParseVersion(c.out); !ok || major != c.major || version != c.version {
			t.Errorf("java -version %q = %d %q %v; want %d %q", c.out, major, version, ok, c.major, c.version)
		}
	}
	if _, _, ok := javaParseVersion("Error: could not find libjava.so\n"); ok {
		t.Error("an error message was read as a Java version")
	}
	if _, err := javaPickAsset([]byte(releases), 25, javaKindJDK, "linux", "aarch64"); err == nil {
		t.Error("a JDK was picked from a list that has none")
	}
	sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if s, err := javaParseSum([]byte(sum+"  "+jre+"\n"), jre); err != nil || s != sum {
		t.Errorf("checksum = %q, %v", s, err)
	}
	if _, err := javaParseSum([]byte(sum+"  other.tar.gz\n"), jre); err == nil {
		t.Error("a checksum for another file was accepted")
	}

	cases := []struct {
		manifest string
		kind     javaFileKind
		main     string
	}{
		{"Manifest-Version: 1.0\r\nMain-Class: com.example.Ma\r\n in\r\n\r\nName: x\r\nMain-Class: other\r\n", javaFileApp, "com.example.Main"},
		{"MIDlet-1: Game, /i.png, Game\nMicroEdition-Profile: MIDP-2.0\nMain-Class: x\n", javaFileMIDlet, ""},
		{"Manifest-Version: 1.0\n", javaFileLibrary, ""},
	}
	for _, c := range cases {
		if kind, main := javaManifestKind(javaParseManifest([]byte(c.manifest))); kind != c.kind || main != c.main {
			t.Errorf("manifest %q = %v %q; want %v %q", c.manifest, kind, main, c.kind, c.main)
		}
	}

	utf := func(s string) []byte { return append([]byte{1, byte(len(s) >> 8), byte(len(s))}, s...) }
	class := func(name string) []byte {
		b := []byte{0xCA, 0xFE, 0xBA, 0xBE, 0, 0, 0, 65, 0, 7}
		b = append(b, utf(name)...)
		b = append(b, 7, 0, 1)
		b = append(b, utf("java/lang/Object")...)
		b = append(b, 7, 0, 3)
		b = append(b, 5, 0, 0, 0, 0, 0, 0, 0, 42) // a Long takes two slots
		return append(b, 0, 0x21, 0, 2, 0, 4)
	}
	good := class("com/example/Hello")
	if name, major, err := javaClassInfo(good); err != nil || name != "com/example/Hello" || javaMajorForClass(major) != 21 {
		t.Errorf("class = %q %d, %v", name, major, err)
	}
	for _, bad := range [][]byte{good[:20], class("-version"), class("a//b"), {0xCA, 0xFE}} {
		if _, _, err := javaClassInfo(bad); err == nil {
			t.Errorf("class file %x was accepted", bad)
		}
	}
	file := filepath.Join("x", "com", "example", "Hello.class")
	if root, err := javaClassRoot(file, "com/example/Hello"); err != nil || root != "x" {
		t.Errorf("class root = %q, %v", root, err)
	}
	if _, err := javaClassRoot(filepath.Join("x", "other", "Hello.class"), "com/example/Hello"); err == nil {
		t.Error("a class outside its package folder was accepted")
	}

	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.zip")
	writeUnzipTestArchive(t, ok, func(zw *zip.Writer) {
		for _, n := range []string{"jdk-25.0.1+8-jre/", "jdk-25.0.1+8-jre/bin/java.exe", "jdk-25.0.1+8-jre/lib/modules"} {
			w, err := zw.Create(n)
			if err != nil {
				t.Fatal(err)
			}
			if n[len(n)-1] != '/' {
				_, _ = w.Write([]byte("x"))
			}
		}
	})
	stage := filepath.Join(dir, "stage")
	if err := javaExtractZip(context.Background(), ok, stage); err != nil {
		t.Fatalf("good archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "bin", "java.exe")); err != nil {
		t.Errorf("top folder not stripped: %v", err)
	}
	for i, names := range [][]string{
		{"jdk-25.0.1+8-jre/bin/java.exe", "jdk-25.0.1+8-jre/../../evil.txt"},
		{"jdk-25.0.1+8-jre/bin/java.exe", "/abs/evil.txt"},
		{"jdk-25.0.1+8-jre/bin/java.exe", "other-top/evil.txt"},
		{"evil/bin/java.exe"},
	} {
		archive := filepath.Join(dir, "bad"+string(rune('0'+i))+".zip")
		writeUnzipTestArchive(t, archive, func(zw *zip.Writer) {
			for _, n := range names {
				w, err := zw.Create(n)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte("x"))
			}
		})
		out := filepath.Join(dir, "out"+string(rune('0'+i)), "stage")
		if err := javaExtractZip(context.Background(), archive, out); err == nil {
			t.Errorf("archive %v was unpacked", names)
		}
		if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
			t.Fatalf("archive %v wrote outside its folder", names)
		}
	}
}
