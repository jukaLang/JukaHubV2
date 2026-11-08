package main

// ssync_test.go — Save sync's rules and their execution: which side wins, how
// a true conflict keeps both copies, which names another device may write,
// and a whole sync between two folders with the other device played by a fake
// that copies files the way the HTTPS transfer does.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// ssyncTestState gives a test its own data folders (hash cache, sync bases,
// Save Manager snapshots, peer identity) and two empty save folders: this
// device's and the other device's.
func ssyncTestState(t *testing.T) (localDir, remoteDir string) {
	t.Helper()
	peerTestState(t)
	data := t.TempDir()
	prevDir, prevSaves := ssyncDirOverride, saveManagerDirOverride
	ssyncDirOverride = filepath.Join(data, "ssync")
	saveManagerDirOverride = filepath.Join(data, "save-manager")
	ssyncHashes.mu.Lock()
	prevLoaded, prevEntries := ssyncHashes.loaded, ssyncHashes.entries
	ssyncHashes.loaded, ssyncHashes.entries = false, nil
	ssyncHashes.mu.Unlock()
	t.Cleanup(func() {
		ssyncDirOverride, saveManagerDirOverride = prevDir, prevSaves
		ssyncHashes.mu.Lock()
		ssyncHashes.loaded, ssyncHashes.entries = prevLoaded, prevEntries
		ssyncHashes.mu.Unlock()
	})
	localDir = filepath.Join(data, "here")
	remoteDir = filepath.Join(data, "there")
	for _, dir := range []string{localDir, remoteDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return localDir, remoteDir
}

func ssyncTestSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func ssyncTestWrite(t *testing.T, dir, rel, content string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func ssyncTestRead(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(data)
}

func ssyncTestRoots(dir string) []saveRoot {
	return []saveRoot{{Name: "RetroArch saves", Path: dir}}
}

// ssyncTestPlan describes both folders and compares them.
func ssyncTestPlan(t *testing.T, localDir, remoteDir string, base map[string]string) ssyncPlan {
	t.Helper()
	ctx := context.Background()
	local, err := ssyncBuildManifest(ctx, ssyncTestRoots(localDir))
	if err != nil {
		t.Fatal(err)
	}
	remote, err := ssyncBuildManifest(ctx, ssyncTestRoots(remoteDir))
	if err != nil {
		t.Fatal(err)
	}
	return ssyncBuildPlan(local, remote, base, 0, "Here", "There")
}

// ssyncFakePeer is the other device as a folder on this one. A pull or push
// lands through ssyncWriteFile with the checksum and time from the plan, as it
// does on both ends of the real transfer.
type ssyncFakePeer struct {
	roots    map[string]string
	prepared []string
	failPrep bool
	corrupt  bool
}

func (p *ssyncFakePeer) prepare(_ context.Context, keys []string) error {
	if p.failPrep {
		return errors.New("the card is read-only")
	}
	p.prepared = append(p.prepared, keys...)
	return nil
}

func (p *ssyncFakePeer) pull(_ context.Context, f ssyncFile, dst string) error {
	src, err := ssyncLocalPath(p.roots[f.Root], f.Path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if p.corrupt && len(data) > 0 {
		data[0] ^= 0xff
	}
	return ssyncWriteFile(dst, bytes.NewReader(data), f.Size, f.SHA, f.MTime)
}

func (p *ssyncFakePeer) push(_ context.Context, f ssyncFile, src string) error {
	dst, err := ssyncLocalPath(p.roots[f.Root], f.Path)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return ssyncWriteFile(dst, in, f.Size, f.SHA, f.MTime)
}

func TestSsyncDecide(t *testing.T) {
	const t0 = int64(1767225600) // 2026-01-01
	file := func(sha string, mtime int64) *ssyncFile {
		return &ssyncFile{Root: "retroarch-saves", Path: "Zelda.srm", Size: 4, MTime: mtime, SHA: sha}
	}
	shaA, shaB, shaBase := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	cases := []struct {
		name      string
		local     *ssyncFile
		remote    *ssyncFile
		base      string
		skew      int64
		action    ssyncAction
		localWins bool
	}{
		{"missing here", nil, file(shaB, t0), "", 0, ssyncPull, false},
		{"missing there", file(shaA, t0), nil, "", 0, ssyncPush, true},
		{"identical", file(shaA, t0), file(strings.ToUpper(shaA), t0+999), "", 0, ssyncSame, false},
		{"newer here", file(shaA, t0+60), file(shaB, t0), "", 0, ssyncPush, true},
		{"newer there", file(shaA, t0), file(shaB, t0+60), "", 0, ssyncPull, false},
		{"too close to order", file(shaA, t0+1), file(shaB, t0), "", 0, ssyncConflict, true},
		{"clock reset here", file(shaA, 1000), file(shaB, t0), "", 0, ssyncConflict, true},
		{"only there changed since the last sync", file(shaBase, t0+600), file(shaB, t0), shaBase, 0, ssyncPull, false},
		{"only here changed since the last sync", file(shaA, t0), file(shaBase, t0+600), shaBase, 0, ssyncPush, true},
		{"both changed, there newer", file(shaA, t0), file(shaB, t0+60), shaBase, 0, ssyncConflict, false},
		{"both changed, here newer", file(shaA, t0+60), file(shaB, t0), shaBase, 0, ssyncConflict, true},
		{"their clock is an hour ahead", file(shaA, t0), file(shaB, t0+3000), "", 3600, ssyncPush, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, localWins, reason := ssyncDecide(tc.local, tc.remote, tc.base, tc.skew)
			if action != tc.action || localWins != tc.localWins {
				t.Fatalf("got action %d, local wins %v (%s); want %d, %v", action, localWins, reason, tc.action, tc.localWins)
			}
		})
	}
}

func TestSsyncConflictName(t *testing.T) {
	taken := map[string]bool{"gba/Zelda (from Brick).srm": true}
	cases := []struct {
		rel    string
		device string
		want   string
	}{
		{"Zelda.srm", "Brick", "Zelda (from Brick).srm"},
		{"gba/Zelda.srm", "Brick", "gba/Zelda (from Brick 2).srm"},
		{"Zelda", "Brick", "Zelda (from Brick)"},
		{"Zelda.state.auto", "Smart Pro", "Zelda.state (from Smart Pro).auto"},
		{"Zelda.srm", `PC: "A/B"*`, "Zelda (from PC- -A-B--).srm"},
		{"Zelda.srm", " \x01 ", "Zelda (from other device).srm"},
		{"Zelda.srm", "Brick...", "Zelda (from Brick).srm"},
	}
	for _, tc := range cases {
		got := ssyncConflictName(tc.rel, tc.device, func(p string) bool { return taken[p] })
		if got != tc.want {
			t.Errorf("ssyncConflictName(%q, %q) = %q, want %q", tc.rel, tc.device, got, tc.want)
		}
		if _, ok := ssyncCleanRel(got); !ok {
			t.Errorf("%q is not a name a save folder can hold", got)
		}
	}
}

func TestSsyncCleanRel(t *testing.T) {
	for _, p := range []string{"Zelda.srm", "gba/Pokemon Emerald.sav", "PSP/SAVEDATA/ULUS10041/DATA.BIN", "Zelda (from Brick).srm"} {
		if got, ok := ssyncCleanRel(p); !ok || got != p {
			t.Errorf("%q was refused", p)
		}
	}
	bad := []string{
		"", "/abs.srm", "../up.srm", "gba/../../up.srm", "a//b.srm", "./a.srm", "a/b.srm/",
		`gba\Zelda.srm`, "C:/Zelda.srm", "c:Zelda.srm", "what?.srm", "name /x.srm", "Zelda.srm.",
		"bell\x07.srm", "upload.tmp", "Thumbs.db", ".stfolder", strings.Repeat("a", 513),
	}
	for _, p := range bad {
		if _, ok := ssyncCleanRel(p); ok {
			t.Errorf("%q was accepted", p)
		}
	}
}

func TestSsyncLocalPathStaysInsideTheFolder(t *testing.T) {
	root := t.TempDir()
	if p, err := ssyncLocalPath(root, "gba/Zelda.srm"); err != nil || p != filepath.Join(root, "gba", "Zelda.srm") {
		t.Fatalf("got %q, %v", p, err)
	}
	for _, rel := range []string{"../Zelda.srm", "gba/../../Zelda.srm", "/etc/passwd"} {
		if _, err := ssyncLocalPath(root, rel); !errors.Is(err, errSsyncBadName) {
			t.Errorf("%q: %v, want a refusal", rel, err)
		}
	}
}

func TestSsyncRootKeysAgreeAcrossDevices(t *testing.T) {
	roots := []saveRoot{
		{Name: "RetroArch saves", Path: "a"},
		{Name: "PPSSPP savedata", Path: "b"},
		{Name: "RetroArch Saves", Path: "c"},
	}
	got := strings.Join(ssyncRootKeys(roots), ",")
	if got != "retroarch-saves,ppsspp-savedata,retroarch-saves-2" {
		t.Fatalf("keys %s", got)
	}
	for _, k := range ssyncRootKeys(roots) {
		if !ssyncValidKey(k) {
			t.Errorf("%q is not a valid key", k)
		}
	}
}

func TestSsyncGameStemAndMatch(t *testing.T) {
	if got := ssyncGameStem(`C:\Roms\GBA\Zelda.gba`); got != "zelda" {
		t.Errorf("Windows path stem %q", got)
	}
	if got := ssyncGameStem("/mnt/SDCARD/Roms/GBA/Pokemon Emerald.zip"); got != "pokemon emerald" {
		t.Errorf("handheld path stem %q", got)
	}
	cases := []struct {
		rel   string
		stem  string
		match bool
	}{
		{"gba/Zelda.srm", "zelda", true},
		{"Zelda.state1", "zelda", true},
		{"Zelda.state.auto", "zelda", true},
		{"Zelda (from Brick).srm", "zelda", true},
		{"Zelda II.srm", "zelda", false},
		{"Zelda.srm", "", false},
	}
	for _, tc := range cases {
		if got := ssyncMatchesGame(tc.rel, tc.stem); got != tc.match {
			t.Errorf("ssyncMatchesGame(%q, %q) = %v", tc.rel, tc.stem, got)
		}
	}
}

func TestSsyncBuildPlanRefusesBadRemoteEntries(t *testing.T) {
	sum := ssyncTestSHA("x")
	const mtime = int64(1767225600)
	local := ssyncManifest{V: 1, Roots: []ssyncRoot{{Key: "retroarch-saves", Name: "RetroArch saves"}}}
	remote := ssyncManifest{V: 1, Roots: []ssyncRoot{{Key: "retroarch-saves"}, {Key: "ppsspp-savedata"}}, Files: []ssyncFile{
		{Root: "retroarch-saves", Path: "../escape.srm", Size: 1, MTime: mtime, SHA: sum},
		{Root: "retroarch-saves", Path: `..\escape.srm`, Size: 1, MTime: mtime, SHA: sum},
		{Root: "Retro Arch", Path: "ok.srm", Size: 1, MTime: mtime, SHA: sum},
		{Root: "retroarch-saves", Path: "nosum.srm", Size: 1, MTime: mtime, SHA: "zz"},
		{Root: "retroarch-saves", Path: "huge.srm", Size: saveManagerMaxFile + 1, MTime: mtime, SHA: sum},
		{Root: "ppsspp-savedata", Path: "ULUS10041/DATA.BIN", Size: 1, MTime: mtime, SHA: sum},
		{Root: "retroarch-saves", Path: "fine.srm", Size: 1, MTime: mtime, SHA: strings.ToUpper(sum)},
	}}
	plan := ssyncBuildPlan(local, remote, nil, 0, "Here", "There")
	if len(plan.Items) != 1 || plan.Items[0].Path != "fine.srm" || plan.Items[0].Action != ssyncPull {
		t.Fatalf("items %+v, want only fine.srm pulled", plan.Items)
	}
	if plan.Items[0].Remote.SHA != sum {
		t.Errorf("checksum kept as %q", plan.Items[0].Remote.SHA)
	}
	if len(plan.Skipped) != 6 {
		t.Errorf("skipped %q, want the six entries this device cannot take", plan.Skipped)
	}
}

// TestSsyncSyncTwoFolders runs a whole sync: new files travel both ways, the
// newer copy wins without a base, the sync base decides when it knows which
// side changed, and true conflicts keep both copies on both devices. The
// files replaced here are snapshotted first, the other device is asked to
// snapshot its own, and a second comparison afterwards finds nothing to do.
func TestSsyncSyncTwoFolders(t *testing.T) {
	localDir, remoteDir := ssyncTestState(t)
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)
	t50 := t0.Add(50 * time.Second)
	t100 := t0.Add(100 * time.Second)
	t500 := t0.Add(500 * time.Second)

	ssyncTestWrite(t, localDir, "a.srm", "only here", t0)
	ssyncTestWrite(t, localDir, "same.srm", "same", t0)
	ssyncTestWrite(t, localDir, "ln.srm", "local newer", t100)
	ssyncTestWrite(t, localDir, "rn.srm", "local older", t0)
	ssyncTestWrite(t, localDir, "c.srm", "local c", t1)
	ssyncTestWrite(t, localDir, "d.srm", "local d", t0)
	ssyncTestWrite(t, localDir, "e.srm", "base e", t500)
	ssyncTestWrite(t, localDir, "gba/sub.state1", "nested local", t0)

	ssyncTestWrite(t, remoteDir, "b.srm", "only there", t0)
	ssyncTestWrite(t, remoteDir, "same.srm", "same", t50)
	ssyncTestWrite(t, remoteDir, "ln.srm", "remote older", t0)
	ssyncTestWrite(t, remoteDir, "rn.srm", "remote newer", t100)
	ssyncTestWrite(t, remoteDir, "c.srm", "remote c", t0)
	ssyncTestWrite(t, remoteDir, "d.srm", "remote d", t100)
	ssyncTestWrite(t, remoteDir, "e.srm", "remote e", t0)
	ssyncTestWrite(t, remoteDir, "bad.tmp", "not a save", t0)

	// The last sync left d.srm as "base d" (both changed it since) and e.srm
	// as "base e" (only the other device changed it, although this copy has
	// the newer time).
	base := map[string]string{
		"retroarch-saves/d.srm": ssyncTestSHA("base d"),
		"retroarch-saves/e.srm": ssyncTestSHA("base e"),
	}
	plan := ssyncTestPlan(t, localDir, remoteDir, base)
	if plan.Push != 3 || plan.Pull != 3 || plan.Conflict != 2 || len(plan.Same) != 1 {
		t.Fatalf("plan: %d push, %d pull, %d conflicts, %d same; want 3, 3, 2, 1", plan.Push, plan.Pull, plan.Conflict, len(plan.Same))
	}
	byPath := map[string]ssyncItem{}
	for _, it := range plan.Items {
		byPath[it.Path] = it
	}
	if it := byPath["c.srm"]; it.Action != ssyncConflict || !it.LocalWins || it.CopyPath != "c (from There).srm" {
		t.Errorf("c.srm: %+v", it)
	}
	if it := byPath["d.srm"]; it.Action != ssyncConflict || it.LocalWins || it.CopyPath != "d (from Here).srm" {
		t.Errorf("d.srm: %+v", it)
	}
	if it := byPath["e.srm"]; it.Action != ssyncPull {
		t.Errorf("e.srm did not change here since the last sync, so it must be pulled: %+v", it)
	}

	peer := &ssyncFakePeer{roots: map[string]string{"retroarch-saves": remoteDir}}
	steps := 0
	res, updates := ssyncExecute(context.Background(), plan, peer, ssyncTestRoots(localDir), "There", "before sync with There",
		func(done, total int, current string) { steps++ })
	if res.Failed != 0 || res.Pulled != 3 || res.Pushed != 3 || res.Conflicts != 2 {
		t.Fatalf("result %+v", res)
	}
	if steps != len(plan.Items)+1 {
		t.Errorf("progress reported %d times, want %d", steps, len(plan.Items)+1)
	}
	if got := strings.Join(peer.prepared, ","); got != "retroarch-saves/c.srm,retroarch-saves/ln.srm" {
		t.Errorf("the other device was asked to snapshot %q", got)
	}

	// The snapshot here holds exactly the three files the sync replaced, as
	// they were.
	if res.Snapshot == "" {
		t.Fatal("no safety snapshot was taken before saves here were replaced")
	}
	snap, err := loadSaveSnapshot(filepath.Join(saveManagerSnapshotsDir(), res.Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, r := range snap.Manifest.Roots {
		for _, f := range r.Files {
			kept = append(kept, f.Path)
		}
	}
	sort.Strings(kept)
	if got := strings.Join(kept, ","); got != "d.srm,e.srm,rn.srm" {
		t.Errorf("the snapshot holds %s, want the three files replaced here", got)
	}
	if snap.Manifest.Label != "before sync with There" {
		t.Errorf("snapshot label %q", snap.Manifest.Label)
	}
	if got := ssyncTestRead(t, filepath.Join(snap.Dir, "files", "retroarch-saves"), "rn.srm"); got != "local older" {
		t.Errorf("the snapshot kept %q for rn.srm, want the old copy", got)
	}

	want := [][2]string{
		{"a.srm", "only here"},
		{"b.srm", "only there"},
		{"same.srm", "same"},
		{"ln.srm", "local newer"},
		{"rn.srm", "remote newer"},
		{"c.srm", "local c"},
		{"c (from There).srm", "remote c"},
		{"d.srm", "remote d"},
		{"d (from Here).srm", "local d"},
		{"e.srm", "remote e"},
		{"gba/sub.state1", "nested local"},
	}
	for _, dir := range []string{localDir, remoteDir} {
		for _, w := range want {
			if got := ssyncTestRead(t, dir, w[0]); got != w[1] {
				t.Errorf("%s in %s holds %q, want %q", w[0], filepath.Base(dir), got, w[1])
			}
		}
	}
	if got := ssyncTestRead(t, remoteDir, "bad.tmp"); got != "not a save" {
		t.Errorf("a file that is not a save was touched: %q", got)
	}
	if info, err := os.Stat(filepath.Join(localDir, "b.srm")); err != nil || !info.ModTime().Equal(t0) {
		t.Errorf("b.srm arrived without its modification time: %v", err)
	}

	// Everything both devices hold is now the base, and nothing is left to do.
	local2, err := ssyncBuildManifest(context.Background(), ssyncTestRoots(localDir))
	if err != nil {
		t.Fatal(err)
	}
	remote2, err := ssyncBuildManifest(context.Background(), ssyncTestRoots(remoteDir))
	if err != nil {
		t.Fatal(err)
	}
	again := ssyncBuildPlan(local2, remote2, updates, 0, "Here", "There")
	if len(again.Items) != 0 {
		t.Fatalf("a second comparison still finds work: %+v", again.Items)
	}
	if len(again.Same) != len(want) || len(updates) != len(want) {
		t.Errorf("%d files the same and %d in the new base, want %d", len(again.Same), len(updates), len(want))
	}
	for k, sum := range again.Same {
		if updates[k] != sum {
			t.Errorf("the base for %s is %q, the file is %q", k, updates[k], sum)
		}
	}
}

func TestSsyncStopsWhenTheOtherDeviceCannotSnapshot(t *testing.T) {
	localDir, remoteDir := ssyncTestState(t)
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	t100 := t0.Add(100 * time.Second)
	ssyncTestWrite(t, localDir, "ln.srm", "local newer", t100)
	ssyncTestWrite(t, remoteDir, "ln.srm", "remote older", t0)
	ssyncTestWrite(t, remoteDir, "b.srm", "only there", t0)
	plan := ssyncTestPlan(t, localDir, remoteDir, nil)
	peer := &ssyncFakePeer{roots: map[string]string{"retroarch-saves": remoteDir}, failPrep: true}
	res, _ := ssyncExecute(context.Background(), plan, peer, ssyncTestRoots(localDir), "There", "before sync with There", nil)
	if len(plan.Items) != 2 || res.Failed != len(plan.Items) || len(res.Errors) != 1 || res.Pulled+res.Pushed != 0 {
		t.Fatalf("result %+v for %d items, want everything left alone", res, len(plan.Items))
	}
	if got := ssyncTestRead(t, remoteDir, "ln.srm"); got != "remote older" {
		t.Errorf("the other device's file was replaced without a snapshot: %q", got)
	}
	if _, err := os.Stat(filepath.Join(localDir, "b.srm")); !os.IsNotExist(err) {
		t.Errorf("b.srm was copied although the sync stopped: %v", err)
	}
}

func TestSsyncRefusesACorruptTransfer(t *testing.T) {
	localDir, remoteDir := ssyncTestState(t)
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	ssyncTestWrite(t, remoteDir, "b.srm", "only there", t0)
	plan := ssyncTestPlan(t, localDir, remoteDir, nil)
	peer := &ssyncFakePeer{roots: map[string]string{"retroarch-saves": remoteDir}, corrupt: true}
	res, updates := ssyncExecute(context.Background(), plan, peer, ssyncTestRoots(localDir), "There", "before sync with There", nil)
	if res.Failed != 1 || res.Pulled != 0 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "checksum") {
		t.Fatalf("result %+v, want one copy refused for its checksum", res)
	}
	if _, ok := updates["retroarch-saves/b.srm"]; ok {
		t.Error("a failed copy went into the sync base")
	}
	entries, err := os.ReadDir(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the refused copy left %d file(s) behind", len(entries))
	}
}

func TestSsyncWriteFileChecksSizeAndSum(t *testing.T) {
	ssyncTestState(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "x.srm")
	body := "save data"
	sum := ssyncTestSHA(body)
	size := int64(len(body))
	if err := ssyncWriteFile(dst, strings.NewReader(body[:4]), size, sum, 0); err == nil {
		t.Fatal("a short transfer was accepted")
	}
	if err := ssyncWriteFile(dst, strings.NewReader(body+"extra"), size, sum, 0); err == nil {
		t.Fatal("a long transfer was accepted")
	}
	if err := ssyncWriteFile(dst, strings.NewReader("save dat!"), size, sum, 0); !errors.Is(err, errSsyncChecksum) {
		t.Fatalf("a changed byte: %v, want a checksum error", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("a refused transfer created the file")
	}
	mtime := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := ssyncWriteFile(dst, strings.NewReader(body), size, strings.ToUpper(sum), mtime.Unix()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dst); err != nil || !info.ModTime().Equal(mtime) {
		t.Fatalf("the modification time was not kept: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("%d files in the folder, want just the save", len(entries))
	}
	// The written file's checksum is cached as written, so a copy it replaced
	// with the same size and time is never trusted for it.
	if e := ssyncHashSnapshot()[dst]; e.SHA != sum || e.Size != size {
		t.Errorf("hash cache holds %+v for the new file", e)
	}
}

func TestSsyncBaseRoundTripDropsBadEntries(t *testing.T) {
	ssyncTestState(t)
	id := strings.Repeat("ab", 32)
	good := ssyncTestSHA("x")
	if err := ssyncSaveBase(id, map[string]string{
		"retroarch-saves/Zelda.srm": strings.ToUpper(good),
		"retroarch-saves/../x.srm":  good,
		"Bad Key/Zelda.srm":         good,
		"retroarch-saves/y.srm":     "not a checksum",
	}); err != nil {
		t.Fatal(err)
	}
	got := ssyncLoadBase(id)
	if len(got) != 1 || got["retroarch-saves/Zelda.srm"] != good {
		t.Fatalf("loaded %v", got)
	}
	if ssyncLastSync(id).IsZero() {
		t.Error("the last sync time is unknown after a sync")
	}
	ssyncForgetPeer(id)
	if len(ssyncLoadBase(id)) != 0 || !ssyncLastSync(id).IsZero() {
		t.Error("forgetting the device kept its sync base")
	}
	if len(ssyncLoadBase("not-an-id")) != 0 {
		t.Error("a malformed id loaded a base")
	}
}
