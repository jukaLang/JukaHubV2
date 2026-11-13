package main

// minecraft_peer.go — Minecraft: Java Edition files from a paired PC.
//
// PortMaster's Minecraft port is the engine; the game is the player's own
// copy, the versions/, libraries/ and assets/ the official launcher (or Prism)
// downloaded into .minecraft on a PC where they own the game. Copying those by
// hand means knowing which of thousands of files one version needs. With
// JukaHub on the PC and the two paired (Nearby devices), the handheld asks:
//
//	GET /peer/v1/minecraft/versions          the versions in the PC's .minecraft
//	GET /peer/v1/minecraft/plan?version=ID   every file that version needs —
//	                                         its json and jar, the parents it
//	                                         inheritsFrom (Forge, Fabric), the
//	                                         libraries for Linux, the asset
//	                                         index and its objects — with size
//	                                         and SHA-1 where the json gives one
//	GET /peer/v1/minecraft/file?path=REL     one of those files
//
// and copies them into the port's .minecraft, checking every SHA-1 the
// launcher's own json lists. Only paired devices reach the endpoints, and only
// under versions/, libraries/ and assets/: the PC's account files
// (launcher_accounts.json and friends), saves and settings are never served.
// A copy that stops resumes: files already there at the right size are kept.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mcPeerVersionsPath = "/peer/v1/minecraft/versions"
	mcPeerPlanPath     = "/peer/v1/minecraft/plan"
	mcPeerFilePath     = "/peer/v1/minecraft/file"

	// mcPlanMaxFiles and mcPlanMaxBytes bound one version's copy (a modern
	// version with its assets is ~5,000 files and well under 1 GB).
	mcPlanMaxFiles = 40000
	mcPlanMaxBytes = int64(6) << 30
	// mcVersionJSONMax bounds a version json or an asset index.
	mcVersionJSONMax = 32 << 20
	// mcPeerVersionsMax bounds the versions one PC reports.
	mcPeerVersionsMax = 200
)

// ── Wire types ──────────────────────────────────────────────────────────

type mcPeerVersion struct {
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"`
	Inherits string `json:"inheritsFrom,omitempty"`
	Jar      bool   `json:"jar"`
}

type mcPeerVersions struct {
	Java     bool            `json:"java"`
	Versions []mcPeerVersion `json:"versions"`
}

type mcPlanFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	SHA1 string `json:"sha1,omitempty"`
}

type mcPlan struct {
	Version string       `json:"version"`
	Files   []mcPlanFile `json:"files"`
	Bytes   int64        `json:"bytes"`
	// Missing counts files the version needs that the PC does not have (a
	// version never started there); the port's launcher may fetch them.
	Missing int `json:"missing"`
}

// ── The launcher's version json ─────────────────────────────────────────

type mcSHA1Size struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
}

type mcRule struct {
	Action string `json:"action"`
	OS     *struct {
		Name string `json:"name"`
	} `json:"os"`
}

type mcLibrary struct {
	Name      string `json:"name"`
	Downloads struct {
		Artifact *mcSHA1Size `json:"artifact"`
	} `json:"downloads"`
	Rules []mcRule `json:"rules"`
	// Natives marks a library that is only native code (LWJGL's platform
	// jars before 1.13): x86 builds the handheld cannot use.
	Natives map[string]string `json:"natives"`
}

type mcVersionJSON struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	InheritsFrom string     `json:"inheritsFrom"`
	Jar          string     `json:"jar"`
	Assets       string     `json:"assets"`
	AssetIndex   mcSHA1Size `json:"assetIndex"`
	Downloads    struct {
		Client mcSHA1Size `json:"client"`
	} `json:"downloads"`
	Libraries []mcLibrary `json:"libraries"`
	Logging   struct {
		Client struct {
			File mcSHA1Size `json:"file"`
		} `json:"client"`
	} `json:"logging"`
}

// mcSafeName accepts a version id or one path segment: no separators, no
// dot-dot, nothing a path could be built from, and nothing Windows treats as
// a device (CON, NUL, COM1, LPT1, CONIN$ — with or without an extension) or
// silently trims (a trailing dot or space).
func mcSafeName(s string) bool {
	if s == "" || len(s) > 128 || s == "." || s == ".." || strings.HasPrefix(s, ".") ||
		strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return false
		}
	}
	stem := strings.ToUpper(s)
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.TrimRight(stem, " ")
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "CLOCK$":
		return false
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9' {
		return false
	}
	return true
}

// mcLibAllowed applies a library's rules for Linux (the handheld's system);
// the "features" rules are launcher options and are ignored.
func mcLibAllowed(rules []mcRule) bool {
	if len(rules) == 0 {
		return true
	}
	allowed := false
	for _, r := range rules {
		if r.OS == nil || r.OS.Name == "" || r.OS.Name == "linux" {
			allowed = r.Action == "allow"
		}
	}
	return allowed
}

// mcMavenPath turns "group:artifact:version[:classifier][@ext]" into the
// path the launcher stores it under; "" for a malformed name.
func mcMavenPath(name string) string {
	ext := "jar"
	if i := strings.LastIndexByte(name, '@'); i > 0 {
		name, ext = name[:i], name[i+1:]
	}
	parts := strings.Split(name, ":")
	if len(parts) < 3 || len(parts) > 4 || !mcSafeName(ext) {
		return ""
	}
	for _, p := range parts {
		if p == "" || strings.Contains(p, "..") || strings.ContainsAny(p, `/\`) {
			return ""
		}
	}
	file := parts[1] + "-" + parts[2]
	if len(parts) == 4 {
		file += "-" + parts[3]
	}
	return strings.ReplaceAll(parts[0], ".", "/") + "/" + parts[1] + "/" + parts[2] + "/" + file + "." + ext
}

// mcPeerFileAllowed checks a relative path from the wire: clean, slash
// separated, and under the folders a version is made of.
func mcPeerFileAllowed(rel string) (string, bool) {
	if rel == "" || len(rel) > 512 || strings.ContainsAny(rel, "\\\x00:") || strings.HasPrefix(rel, "/") {
		return "", false
	}
	if path.Clean(rel) != rel {
		return "", false
	}
	for _, seg := range strings.Split(rel, "/") {
		if !mcSafeName(seg) {
			return "", false
		}
	}
	for _, prefix := range []string{"versions/", "libraries/", "assets/indexes/", "assets/objects/", "assets/log_configs/"} {
		if strings.HasPrefix(rel, prefix) {
			return rel, true
		}
	}
	return "", false
}

func mcReadJSON(p string, v interface{}) error {
	if info, err := os.Lstat(p); err != nil || !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	return json.NewDecoder(io.LimitReader(f, mcVersionJSONMax)).Decode(v)
}

// mcBuildPlan lists every file version id needs from the .minecraft at root.
func mcBuildPlan(root, id string) (mcPlan, error) {
	plan := mcPlan{Version: id}
	if !mcSafeName(id) {
		return plan, errors.New("not a version name")
	}
	seen := map[string]int{} // path → index in plan.Files, -1 when missing
	add := func(rel, sha string, size int64) {
		if rel == "" {
			return
		}
		if _, ok := mcPeerFileAllowed(rel); !ok {
			return
		}
		sha = strings.ToLower(sha)
		if len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
			sha = ""
		}
		if i, dup := seen[rel]; dup {
			// A later mention may carry the checksum the first lacked (a
			// Forge json's "jar" before its parent's downloads.client).
			if i >= 0 && plan.Files[i].SHA1 == "" && sha != "" && (size <= 0 || size == plan.Files[i].Size) {
				plan.Files[i].SHA1 = sha
			}
			return
		}
		if len(seen) >= mcPlanMaxFiles {
			return
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !info.Mode().IsRegular() {
			seen[rel] = -1
			plan.Missing++
			return
		}
		seen[rel] = len(plan.Files)
		f := mcPlanFile{Path: rel, Size: info.Size()}
		// A checksum is only worth sending for the file it describes.
		if sha != "" && (size <= 0 || size == info.Size()) {
			f.SHA1 = sha
		}
		plan.Files = append(plan.Files, f)
		plan.Bytes += info.Size()
	}

	assetIndex := mcSHA1Size{}
	logging := mcSHA1Size{}
	cur := id
	for depth := 0; cur != "" && depth < 6; depth++ {
		if !mcSafeName(cur) {
			return plan, fmt.Errorf("bad parent version %q", cur)
		}
		var v mcVersionJSON
		jsonRel := "versions/" + cur + "/" + cur + ".json"
		if err := mcReadJSON(filepath.Join(root, filepath.FromSlash(jsonRel)), &v); err != nil {
			if depth == 0 {
				return plan, fmt.Errorf("version %s: %w", cur, err)
			}
			// A parent the PC never downloaded: the port's launcher may.
			plan.Missing++
			break
		}
		add(jsonRel, "", 0)
		// A loader profile (Forge, Fabric) has no jar of its own: it runs its
		// parent's.
		if v.InheritsFrom == "" || v.Downloads.Client.SHA1 != "" || regularFile(filepath.Join(root, "versions", cur, cur+".jar")) {
			add("versions/"+cur+"/"+cur+".jar", v.Downloads.Client.SHA1, v.Downloads.Client.Size)
		}
		if v.Jar != "" && mcSafeName(v.Jar) {
			add("versions/"+v.Jar+"/"+v.Jar+".jar", "", 0)
		}
		for _, lib := range v.Libraries {
			if !mcLibAllowed(lib.Rules) || strings.Contains(lib.Name, ":natives-") ||
				lib.Natives != nil && lib.Downloads.Artifact == nil {
				continue
			}
			if a := lib.Downloads.Artifact; a != nil && a.Path != "" {
				add("libraries/"+a.Path, a.SHA1, a.Size)
				continue
			}
			if p := mcMavenPath(lib.Name); p != "" {
				add("libraries/"+p, "", 0)
			}
		}
		if assetIndex.ID == "" {
			assetIndex = v.AssetIndex
			if assetIndex.ID == "" && v.Assets != "" {
				assetIndex.ID = v.Assets
			}
		}
		if logging.ID == "" {
			logging = v.Logging.Client.File
		}
		cur = v.InheritsFrom
		if len(seen) >= mcPlanMaxFiles {
			return plan, errors.New("this version lists too many files")
		}
	}
	if logging.ID != "" && mcSafeName(logging.ID) {
		add("assets/log_configs/"+logging.ID, logging.SHA1, logging.Size)
	}
	if assetIndex.ID != "" && mcSafeName(assetIndex.ID) {
		indexRel := "assets/indexes/" + assetIndex.ID + ".json"
		add(indexRel, assetIndex.SHA1, assetIndex.Size)
		var idx struct {
			Objects map[string]struct {
				Hash string `json:"hash"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		if err := mcReadJSON(filepath.Join(root, filepath.FromSlash(indexRel)), &idx); err == nil {
			names := make([]string, 0, len(idx.Objects))
			for name := range idx.Objects {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				o := idx.Objects[name]
				h := strings.ToLower(o.Hash)
				if len(h) != 40 || strings.Trim(h, "0123456789abcdef") != "" {
					continue
				}
				add("assets/objects/"+h[:2]+"/"+h, h, o.Size)
				if len(seen) >= mcPlanMaxFiles {
					return plan, errors.New("this version lists too many files")
				}
			}
		}
	}
	if plan.Bytes > mcPlanMaxBytes {
		return plan, errors.New("this version is larger than a handheld copy allows")
	}
	return plan, nil
}

// mcListVersions lists the versions in the .minecraft at root.
func mcListVersions(root string) []mcPeerVersion {
	var out []mcPeerVersion
	for _, id := range mcLocalVersions(root) {
		if !mcSafeName(id) {
			continue
		}
		var v mcVersionJSON
		if err := mcReadJSON(filepath.Join(root, "versions", id, id+".json"), &v); err != nil {
			continue
		}
		out = append(out, mcPeerVersion{ID: id, Type: v.Type, Inherits: v.InheritsFrom,
			Jar: regularFile(filepath.Join(root, "versions", id, id+".jar"))})
		if len(out) >= mcPeerVersionsMax {
			break
		}
	}
	return out
}

// ── Server: the PC side ─────────────────────────────────────────────────

func mcHandlePeerVersions(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	out := mcPeerVersions{Versions: []mcPeerVersion{}}
	if root := mcJavaHome(); root != "" {
		out.Java = true
		out.Versions = mcListVersions(root)
	}
	peerWriteJSONResponse(w, http.StatusOK, out)
}

// mcPlanSlot lets one plan be built at a time: each reads a few version
// jsons and stats thousands of files.
var mcPlanSlot = make(chan struct{}, 1)

func mcHandlePeerPlan(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	root := mcJavaHome()
	if root == "" {
		http.Error(w, "no Minecraft: Java Edition files on this computer", http.StatusNotFound)
		return
	}
	select {
	case mcPlanSlot <- struct{}{}:
		defer func() { <-mcPlanSlot }()
	default:
		http.Error(w, "busy with another copy — try again in a moment", http.StatusConflict)
		return
	}
	plan, err := mcBuildPlan(root, r.URL.Query().Get("version"))
	if err != nil {
		// The details name local folders; they stay in this PC's log.
		logWarn("minecraft", "plan for %s: %v", setOr(call.rec.Name, call.id), err)
		http.Error(w, "that version could not be read on this computer", http.StatusNotFound)
		return
	}
	logDebug("minecraft", "plan %s for %s: %d files, %s", plan.Version, setOr(call.rec.Name, call.id), len(plan.Files), humanBytes(plan.Bytes))
	peerWriteJSONResponse(w, http.StatusOK, plan)
}

// mcServePath resolves a wire path to a file the PC may serve: inside the
// .minecraft's versions/, libraries/ or assets/ once every link is followed,
// and a regular file.
func mcServePath(root, rel string) (string, bool) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	inside := false
	for _, sub := range []string{"versions", "libraries", filepath.Join("assets", "indexes"),
		filepath.Join("assets", "objects"), filepath.Join("assets", "log_configs")} {
		if isPathWithin(full, filepath.Join(realRoot, sub)) && filepath.Clean(full) != filepath.Join(realRoot, sub) {
			inside = true
			break
		}
	}
	if !inside {
		return "", false
	}
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return full, true
}

func mcHandlePeerFile(w http.ResponseWriter, r *http.Request, call peerCall) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	root := mcJavaHome()
	rel, ok := mcPeerFileAllowed(r.URL.Query().Get("path"))
	if root == "" || !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	full, ok := mcServePath(root, rel)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	before, err := os.Lstat(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	// The file opened must be the one checked: a link swapped in between is
	// refused.
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > mcPlanMaxBytes {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(f, info.Size()))
}

// ── Client: the handheld side ───────────────────────────────────────────

// mcPCVersions is one paired PC's answer.
type mcPCVersions struct {
	peerID   string
	peerName string
	java     bool
	versions []mcPeerVersion
}

// mcFetchPCVersions asks the paired devices (the six seen last) for their
// Minecraft versions. Any goroutine.
func mcFetchPCVersions(ctx context.Context) []mcPCVersions {
	peers := peerPairedList()
	if len(peers) == 0 || !peerEnabledFlag.Load() {
		return nil
	}
	sort.SliceStable(peers, func(i, j int) bool { return peers[i].LastSeen.After(peers[j].LastSeen) })
	if len(peers) > 6 {
		peers = peers[:6]
	}
	found := make([]*mcPCVersions, len(peers))
	var wg sync.WaitGroup
	for i, p := range peers {
		wg.Add(1)
		go func(i int, p peerRecord) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			var ans mcPeerVersions
			if err := peerDoJSON(pctx, p.ID, http.MethodGet, mcPeerVersionsPath, nil, &ans, 1<<20); err != nil || !ans.Java {
				return
			}
			pc := &mcPCVersions{peerID: p.ID, peerName: setOr(steamCleanText(p.Name, 40), "your PC"), java: true}
			for _, v := range ans.Versions {
				if mcSafeName(v.ID) && len(pc.versions) < mcPeerVersionsMax {
					v.Type = steamCleanText(v.Type, 20)
					v.Inherits = steamCleanText(v.Inherits, 128)
					pc.versions = append(pc.versions, v)
				}
			}
			found[i] = pc
		}(i, p)
	}
	wg.Wait()
	var out []mcPCVersions
	for _, pc := range found {
		if pc != nil {
			out = append(out, *pc)
		}
	}
	return out
}

// mcCopyBusy reports the version being copied, if any.
func mcCopyBusy() (string, bool) {
	mcTransfer.mu.Lock()
	defer mcTransfer.mu.Unlock()
	return mcTransfer.version, mcTransfer.busy && mcTransfer.copying
}

// mcStartCopy copies version from a paired PC into gameDir (the port's
// .minecraft). done runs on the UI thread with how many files the PC lacked.
func mcStartCopy(peerID, peerName, version, gameDir string, done func(missing int, err error)) error {
	if !mcSafeName(version) {
		return errors.New("not a version name")
	}
	if gameDir == "" {
		return errors.New("the Minecraft port's folder was not found")
	}
	if err := mcTransferClaim(true, 0, version); err != nil {
		return err
	}
	gen, card := installBarBeginRun("Minecraft " + version)
	ctx, cancel := mcRunContext(gen, card)
	go func() {
		defer cancel()
		missing, err := mcCopyVersion(ctx, gen, peerID, version, gameDir)
		switch {
		case err == nil:
			logDebug("minecraft", "copied %s from %s into %s (%d missing on the PC)", version, peerName, gameDir, missing)
			installBarDoneRun(gen)
		case ctx.Err() != nil:
			logDebug("minecraft", "copy %s from %s: cancelled", version, peerName)
			installBarAbortRun(gen)
			err = context.Canceled
		default:
			logWarn("minecraft", "copy %s from %s: %v", version, peerName, err)
			installBarFailRun(gen, firstLine(peerErrorText(err, peerName)))
		}
		// Released once the card is settled, so the next claim does not see
		// this run still working.
		mcTransferRelease()
		tbxPost(func() {
			if done != nil {
				done(missing, err)
			}
		})
	}()
	return nil
}

// mcCopyOrder puts the version's own folder last, its json at the very end:
// the json is what marks a version as present (mcLocalVersions), so a copy
// that stops half way never shows as playable.
func mcCopyOrder(files []mcPlanFile, version string) []mcPlanFile {
	own := "versions/" + version + "/"
	marker := own + version + ".json"
	out := make([]mcPlanFile, 0, len(files))
	var tail []mcPlanFile
	var last *mcPlanFile
	for i := range files {
		switch {
		case files[i].Path == marker:
			last = &files[i]
		case strings.HasPrefix(files[i].Path, own):
			tail = append(tail, files[i])
		default:
			out = append(out, files[i])
		}
	}
	out = append(out, tail...)
	if last != nil {
		out = append(out, *last)
	}
	return out
}

// mcCopyVersion does the copy: the plan, a space check, then each file. It
// returns how many files the version needs that the PC does not have.
func mcCopyVersion(ctx context.Context, gen uint64, peerID, version, gameDir string) (int, error) {
	mcCardUpdate(gen, installPhaseResolving, 0, 0)
	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	var plan mcPlan
	err := peerDoJSON(pctx, peerID, http.MethodGet, mcPeerPlanPath+"?version="+url.QueryEscape(version), nil, &plan, 32<<20)
	cancel()
	if err != nil {
		return 0, err
	}
	if len(plan.Files) == 0 {
		return plan.Missing, errors.New("the PC listed no files for this version — start it once there first")
	}
	if len(plan.Files) > mcPlanMaxFiles {
		return plan.Missing, errors.New("the PC listed too many files")
	}
	var total, need int64
	for _, f := range plan.Files {
		if _, ok := mcPeerFileAllowed(f.Path); !ok || f.Size < 0 || f.Size > mcPlanMaxBytes {
			return plan.Missing, fmt.Errorf("the PC listed a file JukaHub will not write: %q", f.Path)
		}
		if f.SHA1 != "" && (len(f.SHA1) != 40 || strings.Trim(f.SHA1, "0123456789abcdef") != "") {
			return plan.Missing, fmt.Errorf("the PC listed a bad checksum for %q", f.Path)
		}
		total += f.Size
		if info, err := os.Stat(filepath.Join(gameDir, filepath.FromSlash(f.Path))); err != nil || info.Size() != f.Size {
			need += f.Size
		}
	}
	if total > mcPlanMaxBytes {
		return plan.Missing, errors.New("this version is larger than a handheld copy allows")
	}
	if err := os.MkdirAll(gameDir, 0o755); err != nil {
		return plan.Missing, fmt.Errorf("cannot create %s: %w", gameDir, err)
	}
	if free, err := freeBytes(gameDir); err == nil && free < need+64<<20 {
		return plan.Missing, fmt.Errorf("needs %s free on the card, %s free", humanBytes(need+64<<20), humanBytes(free))
	}

	var done int64
	mcCardUpdate(gen, installPhaseDownload, 0, total)
	for _, f := range mcCopyOrder(plan.Files, version) {
		if err := ctx.Err(); err != nil {
			return plan.Missing, err
		}
		dst := filepath.Join(gameDir, filepath.FromSlash(f.Path))
		if mcHaveFile(dst, f) {
			done += f.Size
			mcCardUpdate(gen, installPhaseDownload, done, total)
			continue
		}
		base := done
		progress := func(n int64) { mcCardUpdate(gen, installPhaseDownload, base+n, total) }
		if err := mcFetchFile(ctx, peerID, f, dst, progress); err != nil {
			return plan.Missing, fmt.Errorf("%s: %w", path.Base(f.Path), err)
		}
		done += f.Size
		mcCardUpdate(gen, installPhaseDownload, done, total)
	}
	return plan.Missing, nil
}

// mcHaveFile reports whether dst already holds f: the right size and, when
// the launcher gave one, the right SHA-1 (a resumed copy re-reads what is
// there rather than trusting a size a power cut may have left).
func mcHaveFile(dst string, f mcPlanFile) bool {
	info, err := os.Lstat(dst)
	if err != nil || !info.Mode().IsRegular() || info.Size() != f.Size {
		return false
	}
	if f.SHA1 == "" {
		return true
	}
	in, err := os.Open(dst)
	if err != nil {
		return false
	}
	defer in.Close()
	h := sha1.New()
	if _, err := io.Copy(h, in); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == f.SHA1
}

// mcFetchFile downloads one file to dst through a hidden .part file, checking
// its size and (when known) SHA-1 and flushing it before it takes the real
// name.
func mcFetchFile(ctx context.Context, peerID string, f mcPlanFile, dst string, progress func(n int64)) error {
	fctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	resp, err := peerDo(fctx, peerID, http.MethodGet, mcPeerFilePath+"?path="+url.QueryEscape(f.Path), nil, -1)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peerResponseError(resp)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	part := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".part")
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha1.New()
	n, err := io.Copy(io.MultiWriter(out, h), &mcCountingReader{r: io.LimitReader(resp.Body, f.Size+1), fn: progress})
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != f.Size {
		err = fmt.Errorf("got %d bytes, expected %d", n, f.Size)
	}
	if err == nil && f.SHA1 != "" && hex.EncodeToString(h.Sum(nil)) != f.SHA1 {
		err = errors.New("checksum does not match the launcher's")
	}
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	_ = os.Remove(dst)
	if err := os.Rename(part, dst); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}

// mcCountingReader reports how much has been read.
type mcCountingReader struct {
	r  io.Reader
	n  int64
	fn func(int64)
}

func (c *mcCountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.fn != nil && n > 0 {
		c.fn(c.n)
	}
	return n, err
}
