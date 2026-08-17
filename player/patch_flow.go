package main

// patch_flow.go bridges the safe engine to the Patch scene. Every operation
// runs on a worker goroutine and reports through a bounded event channel; the
// render loop drains it once per frame and never performs network or
// filesystem work itself.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PatchEventKind classifies scene-visible operation events.
type PatchEventKind string

const (
	PatchEventState PatchEventKind = "state" // row status updates
	PatchEventToast PatchEventKind = "toast" // user-facing message (kind in Text)
	PatchEventDone  PatchEventKind = "done"  // operation finished
	PatchEventSnap  PatchEventKind = "snap"  // full snapshot refresh
	PatchEventModal PatchEventKind = "modal" // request a confirmation modal
)

// PatchEvent is one bounded event from a worker.
type PatchEvent struct {
	Kind   PatchEventKind
	Text   string // message or status
	Row    PatchComponent
	Status PatchRowStatus
	Snap   *PatchSnapshot
	Modal  *PatchModalRequest
}

// PatchModalRequest asks the scene to show a confirmation modal.
type PatchModalRequest struct {
	ID       string
	Title    string
	Body     string
	Confirm  string
	HighRisk bool
}

// patchEventCh is the single bounded event channel (capacity 64; producers
// drop instead of blocking the worker forever).
var patchEventCh = make(chan PatchEvent, 64)

// patchEventSafe is a non-blocking publish used by workers.
func patchEventSafe(ev PatchEvent) {
	select {
	case patchEventCh <- ev:
	default:
		// Channel full: drop the event. State is rebuilt on the next snapshot.
	}
}

// DrainPatchEvents applies all pending events on the SDL thread and returns
// whether any new event arrived (so the scene can refresh).
func DrainPatchEvents(apply func(PatchEvent)) bool {
	any := false
	for {
		select {
		case ev := <-patchEventCh:
			any = true
			if apply != nil {
				apply(ev)
			}
		default:
			return any
		}
	}
}

// patchBusyLabel is the current operation label shown in the scene header.
var patchBusyLabel string

func setPatchBusy(label string) {
	patchBusyLabel = label
	patchEventSafe(PatchEvent{Kind: PatchEventState, Text: label})
}

// clearPatchBusy clears the busy label.
func clearPatchBusy() {
	patchBusyLabel = ""
}

// CurrentPatchBusyLabel returns the busy label ("" when idle).
func CurrentPatchBusyLabel() string { return patchBusyLabel }

// RunPatchOp runs fn on a worker goroutine under the single-operation lock.
// The operation is fully async; completion is reported via PatchEventDone.
func RunPatchOp(label string, fn func(ctx context.Context) error) {
	if !tryBeginPatchOp() {
		patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: "Another Patch operation is already running"})
		return
	}
	setPatchBusy(label)
	go func() {
		defer endPatchOp()
		defer clearPatchBusy()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		err := fn(ctx)
		if err != nil {
			patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: label + " failed: " + err.Error()})
		} else {
			patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: label + " completed"})
		}
		patchEventSafe(PatchEvent{Kind: PatchEventDone})
		patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(appConfig)})
	}()
}

// patchRowStatus is a helper to publish a row status change.
func patchRowStatus(c PatchComponent, s PatchRowStatus) {
	patchEventSafe(PatchEvent{Kind: PatchEventState, Row: c, Status: s})
}

// --- Operations ---

// OpCheckUpdates checks the JukaHub release feed.
func OpCheckUpdates(ctx context.Context, config *Config) error {
	patchRowStatus(ComponentApp, StatusChecking)
	rel, err := FetchLatestJukaHubRelease(ctx)
	if err != nil {
		patchRowStatus(ComponentApp, StatusOffline)
		return fmt.Errorf("checking JukaHub releases: %w", err)
	}
	installed := installedPatchVersions(config)[ComponentApp]
	available := strings.TrimPrefix(rel.TagName, "v")
	if NewerVersion(installed, available) {
		patchRowStatus(ComponentApp, StatusAvailable)
	} else {
		patchRowStatus(ComponentApp, StatusCurrent)
	}
	patchStateMu.Lock()
	patchState.LastCheckAt = time.Now().UTC().Format(time.RFC3339)
	patchState.LastCheckError = ""
	patchStateMu.Unlock()
	savePatchState()
	return nil
}

// OpVerifyAssets validates and migrates jukaconfig.json in place (backup via
// journal is implicit through AtomicWrite's .bak file).
func OpVerifyAssets(ctx context.Context, config *Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	patchRowStatus(ComponentAssets, StatusPlanning)
	msg, err := MigrateConfigFile(P().ConfigPath())
	if err != nil {
		patchRowStatus(ComponentAssets, StatusFailed)
		return err
	}
	patchRowStatus(ComponentAssets, StatusCurrent)
	// Surface the outcome to the scene.
	patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: msg})
	return nil
}

// OpVerifyTools checks bundled helper-tool presence and architecture.
func OpVerifyTools(ctx context.Context, config *Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	patchRowStatus(ComponentTools, StatusChecking)
	var missing []string
	for _, tool := range []string{"yt-dlp", "ffplay", "mpv"} {
		if runtime.GOOS == "windows" && !strings.HasSuffix(tool, ".exe") {
			tool += ".exe"
		}
		if _, err := P().LookPath(tool); err != nil {
			missing = append(missing, strings.TrimSuffix(tool, ".exe"))
		}
	}
	if len(missing) > 0 {
		patchRowStatus(ComponentTools, StatusUnavailable)
		return fmt.Errorf("missing: %s (JukaHub will fall back to system PATH)", strings.Join(missing, ", "))
	}
	patchRowStatus(ComponentTools, StatusCurrent)
	return nil
}

// OpExportDiagnostics writes the diagnostics report.
func OpExportDiagnostics(ctx context.Context, config *Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	path, err := ExportDiagnostics(config)
	if err != nil {
		return err
	}
	patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: "Diagnostics written to " + path})
	patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(config)})
	return nil
}

// OpRepair runs the local repair pass.
func OpRepair(ctx context.Context, config *Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	r := RepairJukaHub(config)
	lines := make([]string, 0, len(r.Fixed)+len(r.Warned))
	for _, f := range r.Fixed {
		lines = append(lines, "✓ "+f)
	}
	for _, w := range r.Warned {
		lines = append(lines, "⚠ "+w)
	}
	if len(lines) == 0 {
		lines = append(lines, "No issues found")
	}
	patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: "Repair finished"})
	patchEventSafe(PatchEvent{Kind: PatchEventModal, Modal: &PatchModalRequest{
		ID:      "repair-report",
		Title:   "Repair report",
		Body:    strings.Join(lines, "\n"),
		Confirm: "OK",
	}})
	return nil
}

// OpListBackups refreshes the snapshot (backups are listed there).
func OpListBackups(ctx context.Context, config *Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	snap := BuildPatchSnapshot(config)
	patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: snap})
	if len(snap.Backups) == 0 {
		patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: "No backups yet"})
	}
	return nil
}

// OpRestoreBackup restores the newest backup set.
func OpRestoreBackup(ctx context.Context, config *Config, name string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	patchRowStatus(ComponentApp, StatusRollingBack)
	if err := RestoreBackupSet(name); err != nil {
		patchRowStatus(ComponentApp, StatusFailed)
		return err
	}
	patchRowStatus(ComponentApp, StatusCurrent)
	return nil
}

// --- Package operations (apt-style) ---

// loadSignedRepoIndex loads the verified signed repository index from the
// cache. It returns an error when missing or when the signature does not
// verify, so a stale/untrusted index is never used.
func loadSignedRepoIndex() (*SignedRepoIndex, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "repository", "index.json"))
	if err != nil {
		return nil, err
	}
	var idx SignedRepoIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("repository index: %w", err)
	}
	if err := verifyRepoSignature(&idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// repoPkgsByName flattens the signed index into a name -> entry map.
func repoPkgsByName(idx *SignedRepoIndex) map[string]*RepoPkg {
	m := make(map[string]*RepoPkg, len(idx.Packages))
	for i := range idx.Packages {
		m[idx.Packages[i].Name] = &idx.Packages[i]
	}
	return m
}

// OpInstallPackage installs a package (resolving dependencies from the signed
// index when present, or the local packages.json) and records the transaction.
func OpInstallPackage(ctx context.Context, config *Config, id string) error {
	if idx, err := loadSignedRepoIndex(); err == nil {
		return opInstallFromSigned(ctx, config, idx, id)
	}
	// Fall back to the local packages.json (user-defined packages).
	p, _, err := findPackageByID(id)
	if err != nil {
		return err
	}
	files, err := PlanPackageInstall(p, "")
	if err != nil {
		return err
	}
	if p.ArchiveURL != "" {
		if err := installRemotePackage(ctx, p); err != nil {
			return err
		}
	} else {
		if err := InstallPackage(ctx, p, files); err != nil {
			return err
		}
	}
	patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(config)})
	return nil
}

// opInstallFromSigned installs a package from the signed repository with
// dependency resolution, digests and transaction records.
func opInstallFromSigned(ctx context.Context, config *Config, idx *SignedRepoIndex, id string) error {
	pkgs := repoPkgsByName(idx)
	entry, ok := pkgs[id]
	if !ok {
		return fmt.Errorf("package %q not in signed repository", id)
	}
	if !repoPkgCompatible(entry) {
		return fmt.Errorf("package %q is not compatible with this device/OS", id)
	}
	// Resolve dependencies.
	plan, err := resolveInstall(id, pkgs, dbIsInstalled)
	if err != nil {
		return err
	}
	sortPlanByRisk(plan)
	if planHasRisk(plan, "system") && IsWindows() {
		return errors.New("system packages are disabled on this build")
	}
	// Download + verify every package in the plan (deps first).
	for _, st := range plan.Steps {
		if err := stageAndVerifyPackage(ctx, idx, st.Pkg); err != nil {
			return err
		}
	}
	// Apply with transaction record.
	applyStep := func(c context.Context, name string) error {
		return applySignedPackage(c, idx, name)
	}
	removeStep := func(c context.Context, name string) error {
		return opRemoveSigned(c, config, idx, name)
	}
	if err := runTransaction(ctx, plan, "install:"+id, applyStep, removeStep); err != nil {
		return err
	}
	recordInstallTx(plan, "committed")
	patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(config)})
	return nil
}

// stageAndVerifyPackage downloads a package archive into the cache and
// verifies its digest against the signed index. It is a no-op when the cached
// archive already matches (offline mode).
func stageAndVerifyPackage(ctx context.Context, idx *SignedRepoIndex, name string) error {
	dir, err := PatchStateDir()
	if err != nil {
		return err
	}
	entry := findRepoPkg(idx, name)
	if entry == nil {
		return fmt.Errorf("package %q missing from index", name)
	}
	cached := filepath.Join(dir, "cache", name+".zip")
	if _, err := os.Stat(cached); err == nil {
		if verifyPackageDigest(idx, name, cached) == nil {
			return nil // cached + verified
		}
	}
	// Download from the repository base URL (approved host enforced).
	base := os.Getenv("JUKAHUB_PATCH_REPO_URL")
	if base == "" {
		base = "https://github.com/jukaLang/JukaHubV2/releases/latest/download"
	}
	u := strings.TrimRight(base, "/") + "/packages/" + name + ".zip"
	client := newPatchClient()
	if err := downloadToFile(ctx, client, u, cached, entry.PackageSHA, patchMaxDownload); err != nil {
		return err
	}
	if err := verifyPackageDigest(idx, name, cached); err != nil {
		_ = os.Remove(cached)
		return err
	}
	return nil
}

// applySignedPackage stages and applies one verified signed package.
func applySignedPackage(ctx context.Context, idx *SignedRepoIndex, name string) error {
	dir, err := PatchStateDir()
	if err != nil {
		return err
	}
	cached := filepath.Join(dir, "cache", name+".zip")
	if err := verifyPackageDigest(idx, name, cached); err != nil {
		return err
	}
	staging := filepath.Join(dir, "staging", "pkg-"+sanitizeFileName(name))
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	if err := extractZipSafe(ctx, cached, staging, patchMaxEntries, patchMaxExpandBytes); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	// Load + validate the manifest, then apply its operations.
	man, err := loadManifestFromStaging(staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if man.Name != name {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("package archive name mismatch: %q vs %q", man.Name, name)
	}
	if err := validateManifest(man); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	p := manifestToPatchPackage(man)
	p.Root = staging
	files, err := PlanPackageInstall(p, "")
	if err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := InstallPackage(ctx, p, files); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	_ = os.RemoveAll(staging)
	// Record installed state in the DB.
	pkg := DBInstalledPkg{
		Name: man.Name, Version: man.Version, Title: man.Title,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
		KeyID:       idx.KeyID,
		Risk:        man.Risk, Restart: man.Restart,
	}
	for _, f := range files {
		dst, derr := resolvePackageDest(p, &f)
		if derr != nil {
			continue
		}
		pkg.Files = append(pkg.Files, DBInstalledFile{Path: dst, SHA256: checksumFile(dst), Origin: "package"})
	}
	return dbRecordInstall(pkg)
}

// loadManifestFromStaging reads manifest.json from a staged package.
func loadManifestFromStaging(staging string) (*PackageManifest, error) {
	data, err := os.ReadFile(filepath.Join(staging, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("package missing manifest.json: %w", err)
	}
	var man PackageManifest
	if err := json.Unmarshal(data, &man); err != nil {
		return nil, fmt.Errorf("package manifest: %w", err)
	}
	return &man, nil
}

// manifestToPatchPackage converts a signed manifest into the installable
// PatchPackage form.
func manifestToPatchPackage(m *PackageManifest) *PatchPackage {
	files := make([]PackageFile, 0, len(m.Operations))
	for _, op := range m.Operations {
		switch op.Kind {
		case "install", "replace":
			files = append(files, PackageFile{Src: op.Src, Dest: op.Dest, Executable: op.Kind == "executable"})
		case "remove":
			files = append(files, PackageFile{Src: op.Dest, Dest: op.Dest})
		}
	}
	return &PatchPackage{
		ID: m.Name, Name: m.Title, Version: m.Version,
		Description: m.Description, Files: files,
	}
}

// recordInstallTx appends a committed transaction to the history.
func recordInstallTx(plan *TransactionPlan, result string) {
	names := make([]string, 0, len(plan.Steps))
	for _, st := range plan.Steps {
		names = append(names, st.Pkg+"@"+st.Version)
	}
	_ = dbAppendTransaction(DBTransaction{
		ID: "tx-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		At: time.Now().UTC().Format(time.RFC3339),
		Op: "install", Packages: names, Result: result,
	})
}

// OpRemovePackage removes a package's files (after explicit confirmation).
func OpRemovePackage(ctx context.Context, config *Config, id string) error {
	if idx, err := loadSignedRepoIndex(); err == nil {
		return opRemoveSigned(ctx, config, idx, id)
	}
	p, _, err := findPackageByID(id)
	if err != nil {
		return err
	}
	var rerr error
	if p.ArchiveURL != "" {
		rerr = removeRemotePackage(ctx, p)
	} else {
		rerr = RemovePackage(ctx, p, p.Files)
	}
	if rerr != nil {
		return rerr
	}
	patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(config)})
	return nil
}

// opRemoveSigned removes an installed signed package with reverse-dependency
// checks and a transaction record.
func opRemoveSigned(ctx context.Context, config *Config, idx *SignedRepoIndex, id string) error {
	pkgs := repoPkgsByName(idx)
	plan, err := planRemove(id, pkgs, dbIsInstalled)
	if err != nil {
		return err
	}
	removeStep := func(c context.Context, name string) error {
		p := dbFindInstalled(name)
		if p == nil {
			return nil
		}
		files := make([]PackageFile, 0, len(p.Files))
		for _, f := range p.Files {
			files = append(files, PackageFile{Src: f.Path, Dest: f.Path})
		}
		pp := &PatchPackage{ID: name, Name: p.Title, Version: p.Version, Files: files}
		return RemovePackage(c, pp, files)
	}
	if err := runTransaction(ctx, plan, "remove:"+id, nil, removeStep); err != nil {
		return err
	}
	for _, st := range plan.Steps {
		_ = dbRemoveInstalled(st.Pkg)
	}
	_ = dbAppendTransaction(DBTransaction{
		ID: "tx-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		At: time.Now().UTC().Format(time.RFC3339),
		Op: "remove", Packages: []string{id}, Result: "committed",
	})
	patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(config)})
	return nil
}

// OpRefreshRepo reloads packages.json into the snapshot.
func OpRefreshRepo(ctx context.Context, config *Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if _, err := LoadPackageRepo(); err != nil {
		return fmt.Errorf("packages.json: %w", err)
	}
	patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(config)})
	return nil
}

// OpRollbackTx rolls back the most recent committed transaction with a backup
// set, restoring the pre-transaction file state.
func OpRollbackTx(ctx context.Context, config *Config) error {
	txs := dbTransactions()
	for _, tx := range txs {
		if tx.Result != "committed" || tx.BackupSet == "" {
			continue
		}
		if err := RestoreBackupSet(tx.BackupSet); err != nil {
			return err
		}
		// Mark the transaction rolled back and refresh.
		_ = dbAppendTransaction(DBTransaction{
			ID: "tx-" + fmt.Sprintf("%d", time.Now().UnixNano()),
			At: time.Now().UTC().Format(time.RFC3339),
			Op: "rollback", Packages: tx.Packages, Result: "rolled-back",
			RollbackOf: tx.ID,
		})
		patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(config)})
		return nil
	}
	return errors.New("no committed transaction with a backup is available to roll back")
}

// OpRefreshSignedRepo downloads, verifies and caches the signed repository
// index (apt-get update equivalent). Offline refresh keeps the cached index.
func OpRefreshSignedRepo(ctx context.Context, config *Config) error {
	dir, err := PatchStateDir()
	if err != nil {
		return err
	}
	base := strings.TrimRight(os.Getenv("JUKAHUB_PATCH_REPO_URL"), "/")
	if base == "" {
		base = "https://github.com/jukaLang/JukaHubV2/releases/latest/download"
	}
	u := base + "/repository/index.json"
	client := newPatchClient()
	dest := filepath.Join(dir, "repository", "index.json")
	if err := downloadToFile(ctx, client, u, dest, "", patchMaxMetadata); err != nil {
		// Keep the previous verified index (offline behavior) and report.
		return fmt.Errorf("repository refresh failed (using cached index): %w", err)
	}
	// Verify signature before trusting.
	if _, err := loadSignedRepoIndex(); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("repository index rejected: %w", err)
	}
	patchEventSafe(PatchEvent{Kind: PatchEventSnap, Snap: BuildPatchSnapshot(config)})
	return nil
}

// OpVerifyPackages hashes every installed managed file and reports damage.
func OpVerifyPackages(ctx context.Context, config *Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	var damaged []string
	patchDBMu.Lock()
	for _, p := range patchDB.Installed {
		for _, f := range p.Files {
			if _, err := os.Stat(f.Path); err != nil {
				damaged = append(damaged, "missing: "+f.Path)
				continue
			}
			if f.SHA256 != "" && !strings.EqualFold(checksumFile(f.Path), f.SHA256) {
				damaged = append(damaged, "modified: "+f.Path)
			}
		}
	}
	patchDBMu.Unlock()
	if len(damaged) == 0 {
		patchEventSafe(PatchEvent{Kind: PatchEventToast, Text: "All managed files verified"})
		return nil
	}
	patchEventSafe(PatchEvent{Kind: PatchEventModal, Modal: &PatchModalRequest{
		ID: "verify-report", Title: "Verification report",
		Body:    strings.Join(damaged, "\n"),
		Confirm: "OK",
	}})
	return nil
}

// findPackageByID looks up a package in the current repo.
func findPackageByID(id string) (*PatchPackage, *PackageRepo, error) {
	repo, err := LoadPackageRepo()
	if err != nil {
		return nil, nil, err
	}
	for i := range repo.Packages {
		if repo.Packages[i].ID == id {
			return &repo.Packages[i], repo, nil
		}
	}
	return nil, nil, fmt.Errorf("package %q not found in packages.json", id)
}

// humanBytes formats a byte count compactly.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// sanitizeFileName makes an asset name safe for use as a local filename.
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "..", "_")
	if name == "" || name == "." || name == ".." {
		return "download"
	}
	return name
}
