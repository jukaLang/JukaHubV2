package main

// hakux_test.go — the hakuX install's promises, without a network: only the
// TG5040 package is ever picked from a release, a package merges under Data/
// and Emus/ and nowhere else, a merge keeps every file the package does not
// ship (and the ones it replaces), a failed merge leaves the card as it was, a
// rollback puts the replaced files back, and each model gets the offer or the
// reason it should.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHakuxPickAsset(t *testing.T) {
	assets := []emuGitHubAsset{
		{Name: "xbox-trimui-smart-pro-v0.1.3.zip.sha256", BrowserDownloadURL: "u/sum"},
		{Name: "xbox-trimui-smart-pro-s-v0.1.3.zip", BrowserDownloadURL: "u/pro-s"},
		{Name: "xbox-trimui-smart-pro-v0.1.2.zip", BrowserDownloadURL: "u/old"},
		{Name: "XBOX-TRIMUI-SMART-PRO-V0.1.3.ZIP", BrowserDownloadURL: "u/new"},
		{Name: "xbox-trimui-smart-pro-v0.1.4.zip"},
	}
	if a, ok := hakuxPickAsset(assets, "v0.1.3"); !ok || a.BrowserDownloadURL != "u/new" {
		t.Fatalf("tag v0.1.3 picked %+v (%v), want the v0.1.3 package", a, ok)
	}
	// A tag that names no asset falls back to the newest package that can be
	// downloaded; the v0.1.4 entry has no URL.
	if a, ok := hakuxPickAsset(assets, "halo-fix"); !ok || a.BrowserDownloadURL != "u/new" {
		t.Fatalf("untagged pick = %+v (%v), want the newest downloadable package", a, ok)
	}
	if a, ok := hakuxPickAsset(assets[:2], "v0.1.3"); ok {
		t.Fatalf("picked %q: a checksum side-car or the Smart Pro S package is not the TG5040 package", a.Name)
	}
	if got := hakuxAssetVersion("xbox-trimui-smart-pro-v0.1.3-rc1.zip"); got != "0.1.3-rc1" {
		t.Fatalf("version = %q, want 0.1.3-rc1", got)
	}
}

func TestHakuxPlanMerge(t *testing.T) {
	plan, err := hakuxPlanMerge(
		[]string{"README.md", "emus/XBOX/launch.sh", "Emus/XBOX/config.json", "Data/XBOX/bin/hakux", "Emus/Other/launch.sh"},
		[]string{"Data", "Data/XBOX", "Data/XBOX/saves", "emus", "emus/XBOX", "docs"},
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Launcher != "Emus/XBOX/launch.sh" {
		t.Fatalf("launcher = %q, want Emus/XBOX/launch.sh", plan.Launcher)
	}
	want := []hakuxPlanFile{
		{From: "Data/XBOX/bin/hakux", To: "Data/XBOX/bin/hakux"},
		{From: "Emus/Other/launch.sh", To: "Emus/Other/launch.sh"},
		{From: "Emus/XBOX/config.json", To: "Emus/XBOX/config.json"},
		{From: "emus/XBOX/launch.sh", To: "Emus/XBOX/launch.sh"},
	}
	if !reflect.DeepEqual(plan.Files, want) {
		t.Fatalf("files = %+v, want %+v", plan.Files, want)
	}
	if !reflect.DeepEqual(plan.Dirs, []string{"Data/XBOX", "Data/XBOX/saves", "Emus/XBOX"}) {
		t.Fatalf("dirs = %v", plan.Dirs)
	}
	if !reflect.DeepEqual(plan.Skipped, []string{"README.md", "docs"}) {
		t.Fatalf("skipped = %v, want README.md and docs", plan.Skipped)
	}

	for _, bad := range [][]string{
		{"Emus/XBOX/launch.sh", "../evil.sh"},
		{"Emus/XBOX/launch.sh", "Data/../../etc/passwd"},
		{"Emus/XBOX/launch.sh", "/etc/passwd"},
		{"Emus/XBOX/launch.sh", `Data\..\..\x`},
		{"Emus/XBOX/launch.sh", "C:/Windows/x"},
		{"Emus/XBOX/launch.sh", "Emus/XBOX/LAUNCH.SH"},
		{"Emus/XBOX/launch.sh", "Data"},
		{"Data/XBOX/only.bin"},
	} {
		if _, err := hakuxPlanMerge(bad, nil); err == nil {
			t.Errorf("hakuxPlanMerge(%q) accepted a package it must refuse", bad)
		}
	}
}

// hakuxTestCard is a temporary SD card with JukaHub's hakuX folder on it.
func hakuxTestCard(t *testing.T) (hakuxPaths, func(rel, body string), func(rel string) string) {
	t.Helper()
	p := hakuxPaths{SDRoot: t.TempDir(), State: "Apps/JukaHub/data/hakux"}
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(p.SDRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(p.SDRoot, filepath.FromSlash(rel)))
		if err != nil {
			return "<missing>"
		}
		return string(data)
	}
	return p, write, read
}

// hakuxTestPlan plans the package unpacked at stage (relative to the card).
func hakuxTestPlan(t *testing.T, p hakuxPaths, stage string) hakuxMergePlan {
	t.Helper()
	files, dirs, err := hakuxListTree(filepath.Join(p.SDRoot, filepath.FromSlash(stage)))
	if err != nil {
		t.Fatalf("list %s: %v", stage, err)
	}
	plan, err := hakuxPlanMerge(files, dirs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

func TestHakuxMergeKeepsUserFilesAndRollsBack(t *testing.T) {
	p, write, read := hakuxTestCard(t)
	// The card: v1 of the package, plus a setting and a save it never ships.
	write("Emus/XBOX/launch.sh", "v1 launcher")
	write("Emus/XBOX/config.json", "same")
	write("Emus/XBOX/hakux.ini", "user settings")
	write("Data/XBOX/saves/halo.sav", "user save")
	// The unpacked v2: a new launcher, an unchanged file, a new library.
	stage := path.Join(p.stage(), "tree")
	write(stage+"/Emus/XBOX/launch.sh", "v2 launcher")
	write(stage+"/Emus/XBOX/config.json", "same")
	write(stage+"/Data/XBOX/lib/libnew.so", "new lib")
	plan := hakuxTestPlan(t, p, stage)

	root, err := os.OpenRoot(p.SDRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// What v1's install recorded for its launcher: not what is on the card now,
	// so the user edited it.
	sum := sha256.Sum256([]byte("v1 launcher as installed"))
	known := map[string]string{"Emus/XBOX/launch.sh": hex.EncodeToString(sum[:])}
	res, err := hakuxApplyMerge(root, stage, plan, p.pending(), known)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if read("Emus/XBOX/launch.sh") != "v2 launcher" || read("Data/XBOX/lib/libnew.so") != "new lib" {
		t.Fatal("the merge did not lay the new package down")
	}
	if read("Emus/XBOX/hakux.ini") != "user settings" || read("Data/XBOX/saves/halo.sav") != "user save" {
		t.Fatal("the merge touched files the package does not ship")
	}
	if len(res.Replaced) != 1 || len(res.Added) != 1 || len(res.Unchanged) != 1 || len(res.Records) != 3 {
		t.Fatalf("result = %+v, want 1 replaced, 1 added, 1 unchanged, 3 records", res)
	}
	if !reflect.DeepEqual(res.Edited, []string{"Emus/XBOX/launch.sh"}) {
		t.Fatalf("edited = %v, want the launcher the user changed", res.Edited)
	}
	if !reflect.DeepEqual(res.Created, []string{"Data/XBOX/lib"}) {
		t.Fatalf("created = %v, want the one folder the package added", res.Created)
	}

	info := hakuxBackupInfo{Tag: "v0.1.2", Launcher: plan.Launcher, Replaced: res.Replaced, Added: res.Added, Created: res.Created}
	if err := hakuxKeepBackup(root, p, info); err != nil {
		t.Fatalf("keep: %v", err)
	}
	if got := hakuxKeptTag(p); got != "v0.1.2" {
		t.Fatalf("kept tag = %q, want v0.1.2", got)
	}
	if _, err := hakuxRollback(root, p); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if read("Emus/XBOX/launch.sh") != "v1 launcher" || read("Data/XBOX/lib/libnew.so") != "<missing>" {
		t.Fatal("the rollback did not restore v1")
	}
	if _, err := os.Stat(filepath.Join(p.SDRoot, "Data", "XBOX", "lib")); !os.IsNotExist(err) {
		t.Fatalf("the rollback left the folder the update made (%v)", err)
	}
	if read("Emus/XBOX/hakux.ini") != "user settings" || read("Data/XBOX/saves/halo.sav") != "user save" {
		t.Fatal("the rollback touched the user's files")
	}
	if hakuxKeptTag(p) != "" {
		t.Fatal("the kept copy should be used up by the rollback")
	}
}

func TestHakuxMergeFailureLeavesCardAsItWas(t *testing.T) {
	p, write, read := hakuxTestCard(t)
	write("Emus/XBOX/launch.sh", "v1 launcher")
	// A folder of the user's sits where the package puts a file. The merge
	// meets it last (files go in name order), after it has already replaced
	// the launcher and added a file in a folder of its own.
	write("Emus/XBOX/zz.bin/keep.txt", "user folder")
	stage := path.Join(p.stage(), "tree")
	write(stage+"/Emus/XBOX/launch.sh", "v2 launcher")
	write(stage+"/Emus/XBOX/new/added.bin", "added")
	write(stage+"/Emus/XBOX/zz.bin", "blocked")
	plan := hakuxTestPlan(t, p, stage)

	root, err := os.OpenRoot(p.SDRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := hakuxApplyMerge(root, stage, plan, p.pending(), nil); err == nil {
		t.Fatal("a folder in the way of a package file must stop the merge")
	}
	if read("Emus/XBOX/launch.sh") != "v1 launcher" || read("Emus/XBOX/zz.bin/keep.txt") != "user folder" {
		t.Fatal("a failed merge changed the card")
	}
	for _, left := range []string{"Emus/XBOX/new", p.pending()} {
		if _, err := os.Stat(filepath.Join(p.SDRoot, filepath.FromSlash(left))); !os.IsNotExist(err) {
			t.Errorf("a failed merge left %s behind (%v)", left, err)
		}
	}
}

func TestHakuxSupportFor(t *testing.T) {
	if s := hakuxSupportFor(DeviceTrimuiSmartPro); !s.Offer || s.Untested != "" {
		t.Fatalf("Smart Pro = %+v, want a plain offer", s)
	}
	if s := hakuxSupportFor(DeviceTrimuiBrickPro); !s.Offer || s.Untested == "" {
		t.Fatalf("Brick Pro = %+v, want an offer marked untested", s)
	}
	for _, d := range []DeviceModel{DeviceTrimuiSmartProS, DeviceTrimuiBrick, DeviceTrimuiHammer, DeviceUnknown, DeviceDevBuild} {
		if s := hakuxSupportFor(d); s.Offer || s.Reason == "" {
			t.Errorf("%s = %+v, want no offer and a reason", d, s)
		}
	}
	if reason := hakuxSupportFor(DeviceTrimuiSmartProS).Reason; !strings.Contains(reason, "A523") {
		t.Errorf("the Smart Pro S reason should name its chip: %q", reason)
	}
}

func TestHakuxConfigFirmware(t *testing.T) {
	toml := "[general]\nshow_welcome = false\n\n[sys.files]\nbootrom_path = 'bios/mcpx_1.0.bin'\n" +
		"flashrom_path = \"/mnt/SDCARD/Bios/XBOX/Complex_4627.bin\"\nhdd_path = ''\n"
	got := hakuxConfigFirmware("xemu.toml", toml)
	want := map[string]string{"bootrom": "bios/mcpx_1.0.bin", "flashrom": "/mnt/SDCARD/Bios/XBOX/Complex_4627.bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("toml = %v, want %v", got, want)
	}
	ini := "[System]\nhdd = 1\nbios = Bios/XBOX/Complex_4627.bin\n; mcpx = commented.bin\n"
	got = hakuxConfigFirmware("hakux.ini", ini)
	if !reflect.DeepEqual(got, map[string]string{"flashrom": "Bios/XBOX/Complex_4627.bin"}) {
		t.Fatalf("ini = %v, want only the flash ROM (a switch and a comment are not files)", got)
	}
}
