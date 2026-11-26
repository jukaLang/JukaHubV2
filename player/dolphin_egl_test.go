package main

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDolphinEGLShimIsVerifiedARM64(t *testing.T) {
	sum := sha256.Sum256(dolphinEGLShim)
	if got := hex.EncodeToString(sum[:]); got != dolphinEGLSHA256 {
		t.Fatalf("embedded helper hash = %s, want verified %s", got, dolphinEGLSHA256)
	}
	f, err := elf.NewFile(bytes.NewReader(dolphinEGLShim))
	if err != nil {
		t.Fatal(err)
	}
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_AARCH64 || f.Type != elf.ET_DYN {
		t.Fatalf("embedded helper is not an ARM64 shared library: %s %s %s", f.Class, f.Machine, f.Type)
	}
	symbols, err := f.DynamicSymbols()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, symbol := range symbols {
		if symbol.Name == "eglGetProcAddress" && symbol.Section != elf.SHN_UNDEF {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("embedded helper does not export eglGetProcAddress")
	}
	needed, err := f.DynString(elf.DT_NEEDED)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(needed, "libdl.so.2") || !slices.Contains(needed, "libpthread.so.0") {
		t.Fatalf("embedded helper lost its known firmware dependencies: %v", needed)
	}
}

func TestDolphinEGLInstallRepairsAlteredCopy(t *testing.T) {
	base := t.TempDir()
	path, err := dolphinEGLInstallAt(base)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, dolphinEGLFolder, dolphinEGLFile); path != want {
		t.Fatalf("installed at %q, want %q", path, want)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, dolphinEGLShim) {
		t.Fatalf("installed helper differs from verified image: %v", err)
	}
	if err := os.WriteFile(path, []byte("altered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := dolphinEGLInstallAt(base); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, dolphinEGLShim) {
		t.Fatalf("altered helper was not repaired: %v", err)
	}
}

func TestDolphinEGLPreloadPreservesExistingHooks(t *testing.T) {
	const shim = "/data/dolphin-egl/libjukadolphin-eglproc.so"
	env := []string{"HOME=/retroarch", "LD_PRELOAD=/bin/libjukaosd.so:/other/hook.so", "TZ=America/New_York"}
	withShim := dolphinEGLPreloadEnv(env, shim)
	if got, _ := childEnvLookup(withShim, "LD_PRELOAD"); got != shim+":/bin/libjukaosd.so:/other/hook.so" {
		t.Fatalf("preload = %q", got)
	}
	if got, _ := childEnvLookup(withShim, "HOME"); got != "/retroarch" {
		t.Fatalf("HOME changed to %q", got)
	}
	if got, _ := childEnvLookup(withShim, "TZ"); got != "America/New_York" {
		t.Fatalf("TZ changed to %q", got)
	}
	if twice := dolphinEGLPreloadEnv(withShim, shim); !slices.Equal(twice, withShim) {
		t.Fatalf("second Dolphin preload duplicated the helper: %v", twice)
	}
}
