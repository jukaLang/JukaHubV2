package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApkStoreWriteControlsPreservesProfile(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "demo.apk")
	path := filepath.Join(dir, "profile.json")
	p := GameProfile{
		SchemaVersion: gameProfileSchemaVersion,
		ID:            "apk-org.example.demo", Title: "Demo", Runtime: gameRuntimeAndroidPak,
		Dir: dir, Executable: "demo.apk", MainLib: "libdemo.so",
		Env: map[string]string{"KEEP": "yes"},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := apkStoreWriteControls(path, p.ID, apk, "keyboard"); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got GameProfile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Env["ANDROIDPAK_GAMEPAD_MAP"] != "keyboard" || got.Env["KEEP"] != "yes" ||
		got.MainLib != p.MainLib || got.Executable != p.Executable {
		t.Fatalf("controls changed other profile fields: %+v", got)
	}
	if err := apkStoreWriteControls(path, p.ID, apk, ""); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got = GameProfile{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Env["ANDROIDPAK_GAMEPAD_MAP"]; ok || got.Env["KEEP"] != "yes" {
		t.Fatalf("automatic mode did not restore the profile: %+v", got.Env)
	}
	if err := apkStoreWriteControls(path, p.ID, filepath.Join(dir, "other.apk"), "native"); err == nil {
		t.Fatal("changed a profile pointing at another APK")
	}
	if err := apkStoreWriteControls(path, p.ID, apk, "unknown"); err == nil {
		t.Fatal("accepted an unknown controller mode")
	}
}
