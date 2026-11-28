package main

// profiles_extra_test.go — PROF-2: a profile's own achievements account and
// PPSSPP home, and no plaintext password on disk.

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestProfilesRAAccountIsTheProfilesAtLaunch(t *testing.T) {
	dir := t.TempDir()
	if err := profilesRASave(dir, "ann", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(profilesRAPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret-pass") {
		t.Fatalf("the password is in the profile file in plaintext:\n%s", raw)
	}
	if info, err := os.Stat(profilesRAPath(dir)); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		// Windows keeps only the read-only bit, so 0600 cannot be checked there.
		t.Fatalf("the account file is %v, want 0600", info.Mode().Perm())
	}
	values := make(map[string]string)
	for _, v := range profilesRetroArchValues(dir) {
		values[strings.ToLower(v.Key)] = v.Value
	}
	for key, want := range map[string]string{
		"cheevos_enable":   "true",
		"cheevos_username": "ann",
		"cheevos_password": "s3cret-pass",
		"cheevos_token":    "",
	} {
		if got := values[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	empty := make(map[string]string)
	for _, v := range profilesRetroArchValues(t.TempDir()) {
		empty[strings.ToLower(v.Key)] = v.Value
	}
	if empty["cheevos_enable"] != "false" || empty["cheevos_username"] != "" {
		t.Fatalf("a profile without an account must play with achievements off, got %v", empty)
	}
}

func TestProfilesPPSSPPHomeIsInsideTheProfile(t *testing.T) {
	dir := t.TempDir()
	want := dir + string(os.PathSeparator) + "ppsspp"
	if got := profilesPPSSPPHome(dir); got != want {
		t.Fatalf("profilesPPSSPPHome = %q, want %q", got, want)
	}

	profilesActive.Store(&profilesActiveInfo{ID: "p1", Name: "Ann", Dir: dir})
	defer profilesActive.Store(nil)
	env, changed := ppssppProfileHomeEnv([]string{"PATH=/bin", ppssppEnvHome + "=/old"})
	if !changed {
		t.Fatal("an active profile's PPSSPP launch got no home")
	}
	found := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, ppssppEnvHome+"=") {
			if found != "" {
				t.Fatalf("%s appears twice: %v", ppssppEnvHome, env)
			}
			found = kv
		}
	}
	if found != ppssppEnvHome+"="+want {
		t.Fatalf("%s = %q, want %q (env %v)", ppssppEnvHome, found, want, env)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("the profile's PPSSPP home was not created: %v", err)
	}

	profilesActive.Store(nil)
	ownerEnv, ownerChanged := ppssppProfileHomeEnv([]string{"PATH=/bin"})
	if ownerChanged || len(ownerEnv) != 1 {
		t.Fatalf("the owner's launch must keep its own home, got %v (changed %v)", ownerEnv, ownerChanged)
	}
}
