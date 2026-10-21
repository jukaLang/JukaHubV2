package main

// port_compat_test.go — the PortMaster compatibility verdict: which manifest
// spellings are understood, and when the curated list wins.

import (
	"os"
	"path/filepath"
	"testing"
)

func writePortFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyArchNames(t *testing.T) {
	cases := map[string]archClass{
		"aarch64": archARM64, "arm64": archARM64, "ARM64-V8A": archARM64,
		"armhf": archARM32, "armv7": archARM32, "armv7l": archARM32, "arm": archARM32,
		"i386": archX86, "x86": archX86, "x86_64": archX86, "amd64": archX86,
		"all": archIndependent, "any": archIndependent, "noarch": archIndependent,
		"": archUnknown, "mips": archUnknown, "  ": archUnknown,
	}
	for name, want := range cases {
		if got := classifyArch(name); got != want {
			t.Errorf("classifyArch(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestClassifyPort(t *testing.T) {
	cases := []struct {
		name  string
		arch  []string
		state portCompatState
	}{
		{"no manifest", nil, portCompatUnknown},
		{"blank entries", []string{"", "  "}, portCompatUnknown},
		{"arm64", []string{"aarch64"}, portCompatSupported},
		{"arm64 and 32-bit", []string{"aarch64", "armhf"}, portCompatSupported},
		{"independent", []string{"all"}, portCompatSupported},
		{"armhf only", []string{"armhf"}, portCompat32BitOnly},
		{"armv7 only", []string{"armv7"}, portCompat32BitOnly},
		{"x86 only cannot run", []string{"x86_64"}, portCompat32BitOnly},
		{"unknown names are ignored", []string{"mips", "armhf"}, portCompat32BitOnly},
	}
	for _, tc := range cases {
		got := classifyPort(tc.arch)
		if got.State != tc.state {
			t.Errorf("%s: state = %v, want %v (%+v)", tc.name, got.State, tc.state, got)
		}
	}

	// Dedupe and sorting keep the panel text stable across refreshes.
	got := classifyPort([]string{"ARMHF", "armhf", "aarch64"})
	if len(got.Arch) != 2 || got.Arch[0] != "aarch64" {
		t.Errorf("normalised arch = %v", got.Arch)
	}
	if got.Note != "aarch64 build" {
		t.Errorf("supported note = %q", got.Note)
	}
	if n := classifyPort([]string{"armhf"}).Note; n != "armhf only" {
		t.Errorf("32-bit note = %q", n)
	}
	if n := classifyPort([]string{"x86_64"}).Note; n == "" || n == "x86_64 only" {
		t.Errorf("x86 note should explain there is no ARM build: %q", n)
	}
	if n := classifyPort([]string{"all"}).Note; n != "architecture independent" {
		t.Errorf("independent note = %q", n)
	}
}

func TestPortManifestArchShapes(t *testing.T) {
	t.Run("top level array", func(t *testing.T) {
		dir := t.TempDir()
		writePortFile(t, dir, "port.json", `{"arch":["aarch64","armhf"]}`)
		got := portManifestArch(dir)
		if len(got) != 2 || got[0] != "aarch64" || got[1] != "armhf" {
			t.Errorf("arch = %v", got)
		}
	})

	t.Run("nested items", func(t *testing.T) {
		dir := t.TempDir()
		writePortFile(t, dir, "port.json", `{"items":[{"name":"game","arch":["aarch64"]},{"name":"tools","arch":["armhf"]}]}`)
		got := portManifestArch(dir)
		if len(got) != 2 {
			t.Errorf("arch = %v", got)
		}
		if c := classifyPort(got); c.State != portCompatSupported {
			t.Errorf("verdict = %+v", c)
		}
	})

	t.Run("attr nested string", func(t *testing.T) {
		dir := t.TempDir()
		writePortFile(t, dir, "port.json", `{"attr":{"arch":"armhf"}}`)
		if got := portManifestArch(dir); len(got) != 1 || got[0] != "armhf" {
			t.Errorf("arch = %v", got)
		}
	})

	t.Run("info.txt fallback", func(t *testing.T) {
		dir := t.TempDir()
		writePortFile(t, dir, "port.json", `{"title":"Legacy port"}`)
		writePortFile(t, dir, "info.txt", "title=Legacy\nArch=armhf, armv7\n")
		got := portManifestArch(dir)
		if len(got) != 2 {
			t.Fatalf("arch = %v", got)
		}
		if c := classifyPort(got); c.State != portCompat32BitOnly {
			t.Errorf("verdict = %+v", c)
		}
	})

	t.Run("malformed json falls back to info.txt", func(t *testing.T) {
		dir := t.TempDir()
		writePortFile(t, dir, "port.json", `{"arch":["aarch64"`)
		writePortFile(t, dir, "info.txt", "arch=aarch64")
		if got := portManifestArch(dir); len(got) != 1 || got[0] != "aarch64" {
			t.Errorf("arch = %v", got)
		}
	})

	t.Run("malformed json alone is no manifest", func(t *testing.T) {
		dir := t.TempDir()
		writePortFile(t, dir, "port.json", `not json at all`)
		if got := portManifestArch(dir); got != nil {
			t.Errorf("arch = %v, want nil", got)
		}
		if c := portCompatibility(dir); c.State != portCompatUnknown {
			t.Errorf("verdict = %+v", c)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		dir := t.TempDir()
		if got := portManifestArch(dir); got != nil {
			t.Errorf("arch = %v", got)
		}
		if got := portCompatibility(dir); got.State != portCompatUnknown {
			t.Errorf("verdict = %+v", got)
		}
	})

	t.Run("missing directory", func(t *testing.T) {
		if got := portManifestArch(filepath.Join(t.TempDir(), "nope")); got != nil {
			t.Errorf("arch = %v", got)
		}
	})

	t.Run("not a directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "port.sh")
		writePortFile(t, filepath.Dir(file), "port.sh", "#!/bin/sh\n")
		if got := portManifestArch(file); got != nil {
			t.Errorf("arch = %v", got)
		}
	})
}

func TestPortCompatibilityCurated(t *testing.T) {
	root := t.TempDir()
	pinned := filepath.Join(root, "Sonic-Mania")
	if err := os.Mkdir(pinned, 0o755); err != nil {
		t.Fatal(err)
	}

	// A curated entry answers even with no manifest at all.
	got := portCompatibility(pinned)
	if got.State != portCompat32BitOnly {
		t.Fatalf("curated verdict = %+v", got)
	}
	if got.Note == "" {
		t.Error("curated verdict has no note")
	}

	// A curated entry also wins over a manifest that claims an ARM64 build:
	// a hand check is the better source when the two disagree.
	writePortFile(t, pinned, "port.json", `{"arch":["aarch64"]}`)
	if got := portCompatibility(pinned); got.State != portCompat32BitOnly {
		t.Errorf("manifest overrode the curated entry: %+v", got)
	}

	// A curated "supported" entry keeps the manifest's arch list as detail.
	good := filepath.Join(root, "gzdoom")
	if err := os.Mkdir(good, 0o755); err != nil {
		t.Fatal(err)
	}
	writePortFile(t, good, "port.json", `{"arch":["aarch64"]}`)
	entry := portCompatibility(good)
	if entry.State != portCompatSupported {
		t.Fatalf("curated supported = %+v", entry)
	}
	if len(entry.Arch) != 1 || entry.Arch[0] != "aarch64" {
		t.Errorf("arch detail = %v", entry.Arch)
	}

	// An unlisted folder is judged only by its manifest.
	plain := filepath.Join(root, "homebrew")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	writePortFile(t, plain, "port.json", `{"arch":["armhf"]}`)
	if got := portCompatibility(plain); got.State != portCompat32BitOnly {
		t.Errorf("plain port verdict = %+v", got)
	}
}

func TestPortCompatBadge(t *testing.T) {
	if got := portCompatBadge(portCompatUnknown); got != "" {
		t.Errorf("unknown badge = %q, want empty", got)
	}
	if got := portCompatBadge(portCompatSupported); got != "64-bit ready" {
		t.Errorf("supported badge = %q", got)
	}
	if got := portCompatBadge(portCompat32BitOnly); got != "32-bit only" {
		t.Errorf("32-bit badge = %q", got)
	}
}

// The real launcher folder is what the panel passes in, so the verdict has to be
// reachable from the script path alone.
func TestPortCompatibilityFromLauncherPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Ports", "Example Port")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePortFile(t, dir, "port.json", `{"arch":["armhf"]}`)
	launcher := filepath.Join(dir, "Example Port.sh")
	writePortFile(t, dir, "Example Port.sh", "#!/bin/sh\n")

	if got := portCompatibility(filepath.Dir(launcher)); got.State != portCompat32BitOnly {
		t.Errorf("verdict from launcher path = %+v", got)
	}
}
