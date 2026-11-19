package main

// shader_looks_download.go — Settings → Emulation → Download looks: the
// shaders behind the PT, CRT and LCD looks, and SkyWalker541's TrimUI Brick
// overlays, fetched into RetroArch's own folders.
//
// Shaders: the three presets are fetched from libretro's glsl-shaders
// repository (raw.githubusercontent.com, branch master) —
//
//	handheld/PT_SkyWalker541.glslp   SkyWalker541's pixel transparency, made
//	                                 for the GB/GBC/GBA on the Brick
//	crt/zfast-crt.glslp              the light CRT the A133 runs at full speed
//	handheld/zfast-lcd.glslp         the light LCD grid
//
// and every file a preset names is fetched after it: each shaderN pass, each
// texture, and a #reference preset (whose own files follow the same way), so
// only the three preset paths are written here. A path that fails falls back
// to RetroArch's full GLSL pack (buildbot.libretro.com shaders_glsl.zip, the
// file RetroArch's own "Update GLSL Shaders" uses), downloaded to a temporary
// file on the card and read for just the files needed. Everything lands in
// the glsl folder of RetroArch's shader directory (video_shader_dir, else the
// usual shaders folder, in its shaders_glsl), where videofilter.go's probe
// looks. A RetroArch set to a slang driver (glcore, vulkan…) gets no shaders:
// it could not load them.
//
// Overlays: SkyWalker541/TrimUI-Brick-Overlays (CC0), the repository's zip
// from codeload.github.com, again through a temporary file. Its folder tree
// is kept under RetroArch's overlay folder as SkyWalker541/…, and only .cfg,
// .png, .txt and .md files are taken. The Overlay row on the same page lists
// the .cfg files. The repository keeps its RetroArch configs in a folder of
// their own, apart from the pictures, so a .cfg whose picture is not where it
// says gets the path to the one picture of that name in the pack (the one
// beside a folder of the same name when there are several); a picture that
// cannot be told apart is left as it was, and logged. There is no automatic
// overlay: the names are the author's, and his grid overlays under a grid
// shader (PT) show the grid twice.
//
// Safety: HTTPS to five hosts only (redirects included), every size bounded
// (a shader file 1 MB, an overlay file 32 MB, the packs 256 MB and 768 MB),
// zip entries that are absolute, climb out with "..", are links or hidden are
// skipped, and every file is written through a temporary name and renamed.
// data/looks-files.json remembers the checksum of each file written: a file
// that is not on that list, or was changed since, is the user's and is never
// overwritten (it is counted and said). Running it again updates the rest.
// The work runs on its own goroutine; the row shows the progress and a toast
// says the result (offline: no internet connection).

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	looksRawBase      = "https://raw.githubusercontent.com/libretro/glsl-shaders/master/"
	looksPackURL      = "https://buildbot.libretro.com/assets/frontend/shaders_glsl.zip"
	looksOverlayDir   = "SkyWalker541"
	looksManifestName = "looks-files.json"

	looksShaderFileMax   = 1 << 20
	looksShaderTotalMax  = 16 << 20
	looksShaderFilesMax  = 64
	looksPackMax         = 256 << 20
	looksOverlayZipMax   = 768 << 20
	looksOverlayFileMax  = 32 << 20
	looksOverlayTotalMax = 1 << 30
	looksOverlayFilesMax = 4000
	looksOverlayFailMax  = 20
	looksCfgMax          = 256 << 10
	looksManifestMax     = 4 << 20
	looksTimeout         = 20 * time.Minute
)

// looksPreset is one look's preset in libretro's GLSL pack.
type looksPreset struct {
	Look  string // videofilter mode
	Short string // its name in the result toast
	Rel   string
}

var looksPresets = []looksPreset{
	{Look: "pt", Short: "PT", Rel: "handheld/PT_SkyWalker541.glslp"},
	{Look: "crt", Short: "CRT", Rel: "crt/zfast-crt.glslp"},
	{Look: "lcd", Short: "LCD", Rel: "handheld/zfast-lcd.glslp"},
}

// looksOverlayZips are the overlay repository's zip, by branch.
var looksOverlayZips = []string{
	"https://codeload.github.com/SkyWalker541/TrimUI-Brick-Overlays/zip/refs/heads/main",
	"https://codeload.github.com/SkyWalker541/TrimUI-Brick-Overlays/zip/refs/heads/master",
}

var looksOverlayExts = map[string]bool{".cfg": true, ".png": true, ".txt": true, ".md": true}

// looksDL is the download in progress, for the row. Any goroutine.
var looksDL struct {
	sync.Mutex
	running bool
	stage   string
	percent int // -1 when the size is not known
}

func looksSetStage(stage string, percent int) {
	looksDL.Lock()
	looksDL.stage, looksDL.percent = stage, percent
	looksDL.Unlock()
}

// looksDownloadStatus is what the row says while a download runs ("" idle).
func looksDownloadStatus() (string, int) {
	looksDL.Lock()
	defer looksDL.Unlock()
	if !looksDL.running {
		return "", 0
	}
	return looksDL.stage, looksDL.percent
}

// looksDownloadRow is the Emulation page's action.
func looksDownloadRow() setRow {
	return setRow{
		ID: "raTuneLooksGet", Label: "Download looks", Kind: setKindAction,
		Detail: func(*Config) string {
			if stage, _ := looksDownloadStatus(); stage != "" {
				return stage
			}
			return "PT, CRT and LCD shaders from libretro, and SkyWalker541's Brick overlays · again to update"
		},
		Value: func(*Config) string {
			stage, pct := looksDownloadStatus()
			switch {
			case stage == "":
				return ""
			case pct >= 0:
				return strconv.Itoa(pct) + "%"
			}
			return "…"
		},
		Open: looksDownloadStart,
	}
}

// looksDownloadStart starts the download. Main thread.
func looksDownloadStart(c *Config) {
	looksDL.Lock()
	if looksDL.running {
		looksDL.Unlock()
		setToast("Already downloading looks")
		return
	}
	looksDL.running, looksDL.stage, looksDL.percent = true, "Starting…", -1
	looksDL.Unlock()
	showToastPrimitive("Downloading looks…", ToastKindInfo)
	go func() {
		var res looksResult
		func() {
			defer func() {
				if r := recover(); r != nil {
					res.Err = fmt.Errorf("stopped: %v", r)
				}
			}()
			res = looksDownload()
		}()
		looksDL.Lock()
		looksDL.running, looksDL.stage = false, ""
		looksDL.Unlock()
		videoFilterProbesForget()
		raTuneOverlaysForget()
		lookParamsForget()
		msg, kind := res.message()
		logInfo("video", "download looks: %s", msg)
		tbxPost(func() {
			ShowToast(msg, kind)
			if c != nil && setIsHubScene(setHubSceneName(c)) {
				setHubRefresh(c)
			}
		})
	}()
}

// looksResult is what one download did.
type looksResult struct {
	Err      error    // nothing could start (no RetroArch)
	Ready    []string // looks whose files are on the card now
	Failed   []string
	ShadeErr error  // the first reason a look failed
	Slang    string // the driver, when shaders were skipped for it
	Overlays int    // overlay files written or already current
	OverErr  error
	Kept     int // files the user changed, left alone
}

// looksProblem words an error for a toast.
func looksProblem(err error) string { return uiFontProblem(err) }

func looksOffline(err error) bool {
	return err != nil && looksProblem(err) == "no internet connection"
}

func (r looksResult) message() (string, ToastKind) {
	if r.Err != nil {
		return "Could not download looks: " + looksProblem(r.Err), ToastKindWarning
	}
	if len(r.Ready) == 0 && r.Overlays == 0 && (looksOffline(r.ShadeErr) || looksOffline(r.OverErr)) {
		return "No internet connection: looks were not downloaded", ToastKindWarning
	}
	var parts []string
	kind := ToastKindSuccess
	if len(r.Ready) > 0 {
		parts = append(parts, "Looks ready: "+strings.Join(r.Ready, ", "))
	}
	if len(r.Failed) > 0 {
		parts = append(parts, "not downloaded: "+strings.Join(r.Failed, ", ")+" ("+looksProblem(r.ShadeErr)+")")
		kind = ToastKindWarning
	}
	if r.Slang != "" {
		parts = append(parts, "no shaders: RetroArch uses the "+r.Slang+" driver, which needs slang shaders")
		kind = ToastKindWarning
	}
	switch {
	case r.OverErr != nil:
		parts = append(parts, "overlays failed ("+looksProblem(r.OverErr)+")")
		kind = ToastKindWarning
	case r.Overlays > 0:
		parts = append(parts, "SkyWalker541's overlays: "+pluralizeCount(r.Overlays, "file", "files"))
	}
	if r.Kept > 0 {
		// Kept counts every file JukaHub did not write itself: the user's
		// edits and RetroArch's own "Update GLSL Shaders" copies alike.
		parts = append(parts, pluralizeCount(r.Kept, "file already on the card was", "files already on the card were")+" kept")
	}
	if len(parts) == 0 {
		return "Nothing was downloaded", ToastKindWarning
	}
	msg := strings.Join(parts, " · ")
	return strings.ToUpper(msg[:1]) + msg[1:], kind
}

// looksDownload does the whole download. Its own goroutine.
func looksDownload() looksResult {
	var res looksResult
	targets := raScopeTargets(raTuneCandidates())
	if len(targets) == 0 {
		res.Err = errors.New("RetroArch was not found")
		return res
	}
	cfgPath := targets[0]
	raw, _ := os.ReadFile(cfgPath)
	cfg := string(raw)
	ctx, cancel := context.WithTimeout(context.Background(), looksTimeout)
	defer cancel()
	client := looksHTTPClient()
	man := looksManifestLoad()
	defer looksManifestSave(man)

	if driver, _ := raConfigGet(cfg, "video_driver"); videoFilterShaderExt(driver) != "glslp" {
		res.Slang = strings.TrimSpace(driver)
	} else {
		looksFetchShaders(ctx, client, man, looksShaderDir(cfg, cfgPath), &res)
	}
	n, kept, err := looksFetchOverlays(ctx, client, man, filepath.Join(looksOverlayRoot(cfg, cfgPath), looksOverlayDir))
	res.Overlays, res.Kept, res.OverErr = n, res.Kept+kept, err
	if err != nil {
		logWarn("video", "SkyWalker541's overlays: %v", err)
	}
	return res
}

// ── Where the files go ───────────────────────────────────────────────────

// looksShaderDir is the GLSL folder of RetroArch's shader directory.
func looksShaderDir(cfg, cfgPath string) string {
	root := ""
	if v, ok := raConfigGet(cfg, "video_shader_dir"); ok {
		root = videoFilterExpandDir(v, cfgPath)
	}
	if root == "" {
		install := raInstallDir(cfgPath)
		for _, d := range []string{filepath.Join(install, ".retroarch", "shaders"), filepath.Join(install, "shaders"), filepath.Join(filepath.Dir(cfgPath), "shaders")} {
			if dirExists(d) {
				root = d
				break
			}
		}
	}
	if root == "" {
		root = filepath.Join(filepath.Dir(cfgPath), "shaders")
	}
	if strings.EqualFold(filepath.Base(root), "shaders_glsl") {
		return root
	}
	return filepath.Join(root, "shaders_glsl")
}

// looksOverlayRoot is RetroArch's overlay folder (raTuneScanOverlays' roots).
func looksOverlayRoot(cfg, cfgPath string) string {
	if v, ok := raConfigGet(cfg, "overlay_directory"); ok {
		if d := videoFilterExpandDir(v, cfgPath); d != "" {
			return d
		}
	}
	install := raInstallDir(cfgPath)
	for _, d := range []string{filepath.Join(install, "overlays"), filepath.Join(install, ".retroarch", "overlays"), filepath.Join(filepath.Dir(cfgPath), "overlays")} {
		if dirExists(d) {
			return d
		}
	}
	return filepath.Join(install, "overlays")
}

// ── Shaders ──────────────────────────────────────────────────────────────

// looksBudget bounds what one look may fetch.
type looksBudget struct {
	files int
	bytes int
}

// looksCheckFile refuses what is not the file it should be: an error page,
// a binary where a shader belongs, a texture that is not a picture.
func looksCheckFile(rel string, data []byte) error {
	switch strings.ToLower(path.Ext(rel)) {
	case ".png":
		if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			return fmt.Errorf("%s is not a PNG picture", rel)
		}
	case ".jpg", ".jpeg", ".bmp", ".tga":
	default:
		head := bytes.ToLower(bytes.TrimSpace(data[:min(len(data), 64)]))
		if bytes.IndexByte(data, 0) >= 0 || bytes.HasPrefix(head, []byte("<!doctype")) || bytes.HasPrefix(head, []byte("<html")) {
			return fmt.Errorf("%s is not a shader file", rel)
		}
	}
	return nil
}

// looksCollect gets a file and, for a preset, every file it names, by path in
// the pack. files collects them.
func looksCollect(rel string, get func(string) ([]byte, error), files map[string][]byte, depth int, b *looksBudget) error {
	if depth > lookRefDepth {
		return errors.New("the presets refer to each other too deeply")
	}
	if _, done := files[rel]; done {
		return nil
	}
	data, err := get(rel)
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	if err := looksCheckFile(rel, data); err != nil {
		return err
	}
	b.files++
	b.bytes += len(data)
	if b.files > looksShaderFilesMax || b.bytes > looksShaderTotalMax {
		return errors.New("the look names more files than a look should")
	}
	files[rel] = data
	if !strings.EqualFold(path.Ext(rel), ".glslp") {
		return nil
	}
	p := lookParsePreset(string(data))
	if len(p.Shaders) == 0 && p.Reference == "" {
		return fmt.Errorf("%s names no shader", rel)
	}
	deps := append(append([]string(nil), p.Shaders...), p.Textures...)
	if p.Reference != "" {
		deps = append(deps, p.Reference)
	}
	for _, d := range deps {
		dep, ok := lookRepoJoin(path.Dir(rel), d)
		if !ok {
			return fmt.Errorf("%s names a file outside the shader pack (%s)", rel, d)
		}
		if err := looksCollect(dep, get, files, depth+1, b); err != nil {
			return err
		}
	}
	return nil
}

// looksGetRaw fetches one file of libretro's GLSL pack from GitHub.
func looksGetRaw(ctx context.Context, client *http.Client, rel string) ([]byte, error) {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, looksRawBase+strings.Join(segs, "/"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub-Looks/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, looksShaderFileMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > looksShaderFileMax {
		return nil, errors.New("the file is too large for a shader")
	}
	return data, nil
}

// looksWriteShaderFiles writes one look's files into the GLSL folder, the
// presets last, so the probe never finds a preset whose passes are missing.
func looksWriteShaderFiles(man *looksManifest, glslDir string, files map[string][]byte, res *looksResult) error {
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Slice(rels, func(i, j int) bool {
		pi, pj := strings.EqualFold(path.Ext(rels[i]), ".glslp"), strings.EqualFold(path.Ext(rels[j]), ".glslp")
		if pi != pj {
			return pj
		}
		return rels[i] < rels[j]
	})
	for _, rel := range rels {
		dest := filepath.Join(glslDir, filepath.FromSlash(rel))
		if !isPathWithin(dest, glslDir) {
			return fmt.Errorf("%s is outside the shader folder", rel)
		}
		st, err := looksWriteOwned(man, dest, bytes.NewReader(files[rel]), looksShaderFileMax)
		if err != nil {
			return err
		}
		if st == looksKept {
			res.Kept++
			logInfo("video", "%s was changed on the card; it is kept", dest)
		}
	}
	return nil
}

// looksFetchShaders gets the three looks, from GitHub and then, for any that
// failed, from RetroArch's pack.
func looksFetchShaders(ctx context.Context, client *http.Client, man *looksManifest, glslDir string, res *looksResult) {
	var missing []looksPreset
	for i, p := range looksPresets {
		looksSetStage("Downloading the "+p.Short+" shader…", i*20/len(looksPresets))
		files := map[string][]byte{}
		get := func(rel string) ([]byte, error) { return looksGetRaw(ctx, client, rel) }
		err := looksCollect(p.Rel, get, files, 0, &looksBudget{})
		if err == nil {
			err = looksWriteShaderFiles(man, glslDir, files, res)
		}
		if err != nil {
			logInfo("video", "%s from GitHub: %v", p.Rel, err)
			if res.ShadeErr == nil {
				res.ShadeErr = err
			}
			missing = append(missing, p)
			continue
		}
		res.Ready = append(res.Ready, p.Short)
	}
	if len(missing) == 0 || ctx.Err() != nil || looksOffline(res.ShadeErr) {
		for _, p := range missing {
			res.Failed = append(res.Failed, p.Short)
		}
		return
	}
	still, err := looksFromPack(ctx, client, man, glslDir, missing, res)
	if err != nil {
		logWarn("video", "RetroArch's shader pack: %v", err)
		res.ShadeErr = err
	}
	for _, p := range still {
		res.Failed = append(res.Failed, p.Short)
	}
}

// looksZipIndex maps each file of a pack by its lower-case path, then, where
// that key is still free, by its path without the pack's top folder: a pack
// with no top folder keeps its real paths.
func looksZipIndex(files []*zip.File) map[string]*zip.File {
	index := map[string]*zip.File{}
	var usable []*zip.File
	for _, f := range files {
		if f.FileInfo().IsDir() || f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if unsafeArchiveName(name) {
			continue
		}
		usable = append(usable, f)
		if k := strings.ToLower(name); index[k] == nil {
			index[k] = f
		}
	}
	for _, f := range usable {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if i := strings.IndexByte(name, '/'); i > 0 && i < len(name)-1 {
			if k := strings.ToLower(name[i+1:]); index[k] == nil {
				index[k] = f
			}
		}
	}
	return index
}

// looksFromPack fetches the looks GitHub did not give from RetroArch's GLSL
// pack, and returns those still missing.
func looksFromPack(ctx context.Context, client *http.Client, man *looksManifest, glslDir string, want []looksPreset, res *looksResult) ([]looksPreset, error) {
	tmp, err := looksDownloadTemp(ctx, client, []string{looksPackURL}, glslDir, ".jukahub-pack-*.zip", looksPackMax, "Downloading RetroArch's shader pack")
	if err != nil {
		return want, err
	}
	defer os.Remove(tmp)
	zr, err := zip.OpenReader(tmp)
	if err != nil {
		return want, fmt.Errorf("the shader pack is not a zip: %w", err)
	}
	defer zr.Close()
	index := looksZipIndex(zr.File)
	get := func(rel string) ([]byte, error) {
		f, ok := index[strings.ToLower(rel)]
		if !ok {
			return nil, errors.New("not in RetroArch's pack")
		}
		if f.UncompressedSize64 > looksShaderFileMax {
			return nil, errors.New("too large for a shader")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, looksShaderFileMax+1))
		if err != nil {
			return nil, err
		}
		if len(data) > looksShaderFileMax {
			return nil, errors.New("too large for a shader")
		}
		return data, nil
	}
	var still []looksPreset
	var firstErr error
	for _, p := range want {
		files := map[string][]byte{}
		err := looksCollect(p.Rel, get, files, 0, &looksBudget{})
		if err == nil {
			err = looksWriteShaderFiles(man, glslDir, files, res)
		}
		if err != nil {
			logInfo("video", "%s from RetroArch's pack: %v", p.Rel, err)
			if firstErr == nil {
				firstErr = err
			}
			still = append(still, p)
			continue
		}
		res.Ready = append(res.Ready, p.Short)
	}
	return still, firstErr
}

// ── Overlays ─────────────────────────────────────────────────────────────

// looksHiddenPath is a pack path inside a hidden or macOS metadata folder.
func looksHiddenPath(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") || strings.EqualFold(seg, "__MACOSX") {
			return true
		}
	}
	return false
}

// looksFetchOverlays extracts SkyWalker541's overlay pack under dest and
// returns how many files are in place, and how many were the user's.
func looksFetchOverlays(ctx context.Context, client *http.Client, man *looksManifest, dest string) (int, int, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, 0, err
	}
	tmp, err := looksDownloadTemp(ctx, client, looksOverlayZips, filepath.Dir(dest), ".jukahub-overlays-*.zip", looksOverlayZipMax, "Downloading SkyWalker541's overlays")
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(tmp)
	zr, err := zip.OpenReader(tmp)
	if err != nil {
		return 0, 0, fmt.Errorf("the overlay pack is not a zip: %w", err)
	}
	defer zr.Close()
	looksSetStage("Unpacking SkyWalker541's overlays…", -1)
	var total uint64
	count, written, kept, failed := 0, 0, 0, 0
	var cfgs []string
	pngs := map[string][]string{}
	for _, f := range zr.File {
		if ctx.Err() != nil {
			return written, kept, ctx.Err()
		}
		if f.FileInfo().IsDir() || f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		name := strings.ReplaceAll(f.Name, `\`, "/")
		i := strings.IndexByte(name, '/')
		if i < 0 {
			continue // the zip's files all sit in its top folder
		}
		rel := name[i+1:]
		ext := strings.ToLower(path.Ext(rel))
		if rel == "" || unsafeArchiveName(rel) || looksHiddenPath(rel) || !looksOverlayExts[ext] {
			continue
		}
		if f.UncompressedSize64 > looksOverlayFileMax {
			logInfo("video", "overlay pack: %s is too large; skipped", rel)
			continue
		}
		count++
		total += f.UncompressedSize64
		if count > looksOverlayFilesMax || total > looksOverlayTotalMax {
			return written, kept, errors.New("the overlay pack is larger than expected")
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if !isPathWithin(target, dest) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return written, kept, err
		}
		st, err := looksWriteOwned(man, target, rc, looksOverlayFileMax)
		rc.Close()
		if err != nil {
			// One file the card refuses (a name this file system cannot
			// hold) is skipped; many mean the card itself is the problem.
			failed++
			logInfo("video", "overlay pack: %s: %v", rel, err)
			if failed > looksOverlayFailMax {
				return written, kept, fmt.Errorf("%s: %w", rel, err)
			}
			continue
		}
		if st == looksKept {
			kept++
			continue
		}
		written++
		switch ext {
		case ".cfg":
			cfgs = append(cfgs, rel)
		case ".png":
			key := strings.ToLower(path.Base(rel))
			pngs[key] = append(pngs[key], rel)
		}
	}
	// A picture the user kept is still the pack's name for it.
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if i := strings.IndexByte(name, '/'); i >= 0 && strings.EqualFold(path.Ext(name), ".png") {
			rel := name[i+1:]
			key := strings.ToLower(path.Base(rel))
			known := false
			for _, r := range pngs[key] {
				known = known || r == rel
			}
			if !known && !unsafeArchiveName(rel) && !looksHiddenPath(rel) {
				pngs[key] = append(pngs[key], rel)
			}
		}
	}
	for _, rel := range cfgs {
		looksFixOverlayCfg(man, dest, rel, pngs)
	}
	if written+kept == 0 {
		return 0, 0, errors.New("the pack had no overlays")
	}
	return written, kept, nil
}

// looksOverlayResolves reports whether a picture path in a .cfg (relative to
// the .cfg's folder cfgDir, inside root) names a file that is there.
func looksOverlayResolves(root, cfgDir, val string) bool {
	val = strings.ReplaceAll(val, `\`, "/")
	if strings.HasPrefix(val, "/") || (len(val) >= 2 && val[1] == ':') {
		return regularFile(filepath.FromSlash(val))
	}
	return regularFile(filepath.Join(root, filepath.FromSlash(cfgDir), filepath.FromSlash(val)))
}

// looksFindImage picks the pack's picture a .cfg meant: the one of that name,
// or among several the one in a folder named like the .cfg's own.
func looksFindImage(pngs map[string][]string, cfgDir, val string) (string, bool) {
	cands := pngs[strings.ToLower(path.Base(strings.ReplaceAll(val, `\`, "/")))]
	if len(cands) == 1 {
		return cands[0], true
	}
	want := strings.ToLower(path.Base(cfgDir))
	pick := ""
	for _, c := range cands {
		if strings.ToLower(path.Base(path.Dir(c))) == want {
			if pick != "" {
				return "", false
			}
			pick = c
		}
	}
	return pick, pick != ""
}

// looksFixOverlayCfg points a .cfg's pictures at where the pack put them.
func looksFixOverlayCfg(man *looksManifest, root, rel string, pngs map[string][]string) {
	target := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(target)
	if err != nil || info.Size() > looksCfgMax {
		return
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return
	}
	cfgDir := path.Dir(rel)
	lines := strings.Split(string(raw), "\n")
	changed := false
	for i, line := range lines {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		if !strings.HasSuffix(strings.ToLower(key), "_overlay") {
			continue
		}
		val := lookPresetValue(v)
		switch strings.ToLower(path.Ext(strings.ReplaceAll(val, `\`, "/"))) {
		case ".png", ".jpg", ".jpeg", ".bmp", ".tga":
		default:
			continue
		}
		if looksOverlayResolves(root, cfgDir, val) {
			continue
		}
		found, ok := looksFindImage(pngs, cfgDir, val)
		if !ok {
			logInfo("video", "overlay %s: cannot tell which picture %q is; left as it was", rel, val)
			continue
		}
		relPath, err := filepath.Rel(filepath.FromSlash(cfgDir), filepath.FromSlash(found))
		if err != nil {
			continue
		}
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
		}
		lines[i] = key + " = \"" + filepath.ToSlash(relPath) + "\"" + cr
		changed = true
	}
	if !changed {
		return
	}
	if _, err := looksWriteOwned(man, target, strings.NewReader(strings.Join(lines, "\n")), looksCfgMax); err != nil {
		logWarn("video", "overlay %s: cannot fix its picture paths: %v", rel, err)
	}
}

// ── Downloading ──────────────────────────────────────────────────────────

// looksDownloadTemp downloads the first address that answers into a
// temporary file in dir (the card, not memory), bounded by max, and returns
// its path. The caller removes it.
func looksDownloadTemp(ctx context.Context, client *http.Client, urls []string, dir, pattern string, max int64, stage string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// A download a crash or a power cut stopped left its temporary file, up to
	// the pack's size, on the card. Only one download runs at a time.
	if pre, suf, ok := strings.Cut(pattern, "*"); ok {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if n := e.Name(); !e.IsDir() && len(n) > len(pre)+len(suf) && strings.HasPrefix(n, pre) && strings.HasSuffix(n, suf) {
					_ = os.Remove(filepath.Join(dir, n))
				}
			}
		}
	}
	var lastErr error
	for _, address := range urls {
		p, err := looksDownloadOnce(ctx, client, address, dir, pattern, max, stage)
		if err == nil {
			return p, nil
		}
		logInfo("video", "%s: %v", address, err)
		lastErr = err
		if ctx.Err() != nil || looksOffline(err) {
			break
		}
	}
	return "", lastErr
}

func looksDownloadOnce(ctx context.Context, client *http.Client, address, dir, pattern string, max int64, stage string) (string, error) {
	looksSetStage(stage+"…", -1)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "JukaHub-Looks/"+versionString(nil))
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	if resp.ContentLength > max {
		return "", errors.New("the download is larger than expected")
	}
	out, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := out.Name()
	done := false
	defer func() {
		if out != nil {
			out.Close()
		}
		if !done {
			_ = os.Remove(name)
		}
	}()
	total := resp.ContentLength
	var got int64
	buf := make([]byte, 64<<10)
	lastShown := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			got += int64(n)
			if got > max {
				return "", errors.New("the download is larger than expected")
			}
			if _, werr := out.Write(buf[:n]); werr != nil {
				return "", werr
			}
			if time.Since(lastShown) >= 500*time.Millisecond {
				lastShown = time.Now()
				if total > 0 {
					looksSetStage(stage+"…", int(got*100/total))
				} else {
					looksSetStage(stage+"… "+formatSize(got), -1)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	if total > 0 && got != total {
		return "", fmt.Errorf("the download stopped at %d of %d bytes", got, total)
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		out = nil
		return "", err
	}
	out = nil
	done = true
	return name, nil
}

var looksHTTP struct {
	once   sync.Once
	client *http.Client
}

// looksAllowedHosts are the only hosts looks come from, redirects included.
var looksAllowedHosts = map[string]bool{
	"raw.githubusercontent.com":     true,
	"objects.githubusercontent.com": true,
	"codeload.github.com":           true,
	"github.com":                    true,
	"buildbot.libretro.com":         true,
}

func looksAllowedURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && looksAllowedHosts[strings.ToLower(u.Hostname())]
}

// looksHTTPClient talks HTTPS to the hosts above only, with the firmware CA
// fallback a handheld needs (bugrepRootCAs).
func looksHTTPClient() *http.Client {
	looksHTTP.once.Do(func() {
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 45 * time.Second,
			MaxIdleConns:          2,
			IdleConnTimeout:       30 * time.Second,
		}
		if pool := bugrepRootCAs(); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		looksHTTP.client = &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !looksAllowedURL(req.URL) {
				return fmt.Errorf("redirect to %s refused", req.URL.Host)
			}
			return nil
		}}
	})
	return looksHTTP.client
}

// ── Files JukaHub wrote ──────────────────────────────────────────────────

// looksManifest is the checksum of every file a download wrote, by path.
type looksManifest struct {
	Files map[string]string `json:"files"`
}

var looksManifestPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, looksManifestName)
}

func looksManifestLoad() *looksManifest {
	m := &looksManifest{}
	if p := looksManifestPath(); p != "" {
		if info, err := os.Stat(p); err == nil && info.Size() <= looksManifestMax {
			if raw, err := os.ReadFile(p); err == nil {
				_ = json.Unmarshal(raw, m)
			}
		}
	}
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	return m
}

func looksManifestSave(m *looksManifest) {
	p := looksManifestPath()
	if p == "" || m == nil {
		return
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(p, append(data, '\n'), 0o644); err != nil {
		logWarn("video", "cannot save the list of downloaded looks: %v", err)
	}
}

// What looksWriteOwned did with a file.
const (
	looksWritten = "written"
	looksSame    = "same"
	looksKept    = "kept"
)

// looksWriteOwned writes one downloaded file to dest unless the file there is
// the user's: one JukaHub did not write, or one changed since it did. The
// bytes go to a temporary name first and are renamed into place.
func looksWriteOwned(man *looksManifest, dest string, r io.Reader, limit int64) (string, error) {
	dest = filepath.Clean(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	part := dest + ".jukahub-part"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(r, limit+1))
	if err == nil && n > limit {
		err = errors.New("the file is larger than expected")
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(part)
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if info, err := os.Lstat(dest); err == nil {
		if !info.Mode().IsRegular() {
			_ = os.Remove(part)
			return looksKept, nil
		}
		cur, herr := hashFile(dest)
		if herr == nil && cur == sum {
			_ = os.Remove(part)
			man.Files[dest] = sum
			return looksSame, nil
		}
		if rec, ours := man.Files[dest]; herr != nil || !ours || rec != cur {
			_ = os.Remove(part)
			return looksKept, nil
		}
	} else if !os.IsNotExist(err) {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, dest); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	man.Files[dest] = sum
	return looksWritten, nil
}
