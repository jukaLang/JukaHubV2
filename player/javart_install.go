package main

// javart_install.go — JukaHub's own Java: Eclipse Temurin from Adoptium's
// GitHub releases, installed into <data>/java.
//
// Which build: GitHub's release list of adoptium/temurin<N>-binaries, newest
// first. Drafts, prereleases and early-access tags are skipped, and the first
// release that has both the exact archive for this computer
// (OpenJDK25U-jre_aarch64_linux_hotspot_25.0.1_8.tar.gz,
// OpenJDK25U-jdk_x64_windows_hotspot_25.0.1_8.zip, Java 8's
// OpenJDK8U-jre_aarch64_linux_hotspot_8u472b08.tar.gz) and its .sha256.txt is
// taken: Adoptium publishes platform by platform, so the newest release may
// not have an aarch64 build yet. The archive is accepted only when its SHA-256
// matches that file, and only then unpacked. Every request stays on GitHub's
// hosts over HTTPS (validatePatchURL).
//
// Unpacking copies portmaster_python.go's care: member paths are checked,
// sizes and file counts are capped, links become copies (the SD card is FAT),
// the archive's top folder (jdk-25.0.1+8-jre/) is dropped, and everything
// goes to a stage folder first. The staged java must answer `java -version`
// with the version asked for — a Java that cannot start here fails the
// install with its first line — and the marker (.jukahub-java.json) is
// written last. One JukaHub Java exists at a time: the old one is moved aside
// only after the new one probed fine, and a removal moves the folder aside
// before deleting it, so a half-deleted Java never looks ready.
//
// Temurin's Linux builds need glibc 2.17, so they run on the TrimUI's stock
// glibc 2.33. Sizes: the JRE is about 50 MB to download, the JDK about 200 MB.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	javaReleasesURL  = "https://api.github.com/repos/adoptium/temurin%d-binaries/releases?per_page=%d&page=%d"
	javaDownloadBase = "https://github.com/adoptium/temurin%d-binaries/releases/download/"
	javaMaxReleases  = int64(8 << 20)
	// javaReleasePages is how many pages of javaReleasesPerPage releases are
	// read at most: Adoptium publishes an early-access build every week or
	// two, so ten releases can be all early access between two quarterly
	// updates.
	javaReleasePages = 6
	javaMaxSumFile   = int64(4 << 10)
	javaMaxArchive   = int64(420 << 20)
	javaMaxExpanded  = int64(1 << 30)
	javaMaxFiles     = 30000
	javaMarker       = ".jukahub-java.json"
	javaCheckFile    = "java-update-check.json"
	javaKindJRE      = "jre"
	javaKindJDK      = "jdk"
	// javaDefaultMajor is the version JukaHub offers first: the current LTS.
	javaDefaultMajor = 25
	// javaInstallTimeout bounds one install (a JDK over slow Wi-Fi).
	javaInstallTimeout = 40 * time.Minute
	// javaReleasesPerPage keeps one page of the release list well under
	// javaMaxReleases: a Temurin release lists some 400 files (every
	// platform's JDK, JRE, debug and test images with their checksums and
	// signatures), about 700 KB of JSON.
	javaReleasesPerPage = 5
)

// javaMajors are the versions JukaHub installs: the LTS releases.
var javaMajors = []int{8, 11, 17, 21, 25}

// javaKindRemove marks the install slot while JukaHub's Java is deleted, and
// javaKindTidy while the leftovers of a cut-off install are.
const (
	javaKindRemove = "remove"
	javaKindTidy   = "tidy"
)

// javaSupportedMajor reports whether JukaHub installs Java n.
func javaSupportedMajor(n int) bool {
	for _, m := range javaMajors {
		if m == n {
			return true
		}
	}
	return false
}

// javaNewestMajor is the newest version JukaHub installs.
func javaNewestMajor() int { return javaMajors[len(javaMajors)-1] }

// javaAssetRe matches a Temurin archive name exactly: version, JRE or JDK,
// CPU, OS and the archive type. alpine-linux, debugimage, testimage, sbom,
// jmods, static-libs, .msi and the .sig/.json/.sha256.txt siblings do not.
// The build part must start with a digit and has no "-" or "/": a release
// build reads 25.0.1_8, 21.0.13.1_2 or 8u472b08, while early-access builds
// read ea_21-0-1-12 and nightlies 2023-11-08-15-55.
var javaAssetRe = regexp.MustCompile(`^OpenJDK(\d+)U-(jre|jdk)_(aarch64|x64)_(linux|windows)_hotspot_([0-9][0-9A-Za-z._+]*)\.(tar\.gz|zip)$`)

// javaTopRe is the archive's top folder: jdk-25.0.1+8-jre, jdk8u472-b08.
var javaTopRe = regexp.MustCompile(`^jdk[0-9A-Za-z.+_-]*$`)

// javaGHRelease is the part of GitHub's release list JukaHub reads.
type javaGHRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// javaAsset is the archive chosen for this computer.
type javaAsset struct {
	Name   string
	URL    string
	SumURL string
	Tag    string
	Size   int64
	Major  int
	Kind   string
}

// javaInstalled is what an install recorded (the marker).
type javaInstalled struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Major     int    `json:"major"`
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	Installed string `json:"installed,omitempty"`
}

// javaPlatform names this computer the way Temurin's archives do. ok is false
// where Temurin has no build (32-bit ARM, macOS here).
func javaPlatform() (osName, arch, ext string, ok bool) {
	switch runtime.GOOS {
	case "linux":
		osName, ext = "linux", "tar.gz"
	case "windows":
		osName, ext = "windows", "zip"
	default:
		return "", "", "", false
	}
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", "", "", false
	}
	return osName, arch, ext, true
}

// javaPlatformLabel is this computer in words, for messages.
func javaPlatformLabel() string {
	switch {
	case IsTrimuiDevice():
		return "this handheld"
	case runtime.GOOS == "windows" && runtime.GOARCH == "arm64":
		return "Windows on ARM"
	case runtime.GOOS == "windows":
		return "Windows"
	}
	return runtime.GOOS + " " + runtime.GOARCH
}

// javaKindLabel is "JRE" or "JDK".
func javaKindLabel(kind string) string {
	if kind == javaKindJDK {
		return "JDK"
	}
	return "JRE"
}

// javaSizeText is the rough download size of a kind, for offers.
func javaSizeText(kind string) string {
	if kind == javaKindJDK {
		return "about 200 MB"
	}
	return "about 50 MB"
}

// javaPickAsset chooses the archive from GitHub's release list (newest
// first): the first release that is neither a draft nor a prerelease nor an
// early-access build and has the exact archive and its .sha256.txt. Pure.
func javaPickAsset(data []byte, major int, kind, osName, arch string) (javaAsset, error) {
	var releases []javaGHRelease
	if err := json.Unmarshal(data, &releases); err != nil {
		return javaAsset{}, fmt.Errorf("GitHub's release list could not be read: %w", err)
	}
	wantExt := "tar.gz"
	if osName == "windows" {
		wantExt = "zip"
	}
	prefix := fmt.Sprintf(javaDownloadBase, major)
	for _, rel := range releases {
		tag := strings.ToLower(rel.TagName)
		if rel.Draft || rel.Prerelease || strings.Contains(tag, "-ea") || strings.Contains(tag, "beta") {
			continue
		}
		urls := make(map[string]string, len(rel.Assets))
		for _, a := range rel.Assets {
			urls[a.Name] = a.URL
		}
		for _, a := range rel.Assets {
			m := javaAssetRe.FindStringSubmatch(a.Name)
			if m == nil || m[1] != strconv.Itoa(major) || m[2] != kind || m[3] != arch || m[4] != osName || m[6] != wantExt {
				continue
			}
			sum, ok := urls[a.Name+".sha256.txt"]
			if !ok || !strings.HasPrefix(a.URL, prefix) || !strings.HasPrefix(sum, prefix) {
				continue
			}
			if validatePatchURL(a.URL) != nil || validatePatchURL(sum) != nil {
				continue
			}
			return javaAsset{Name: a.Name, URL: a.URL, SumURL: sum, Tag: rel.TagName, Size: a.Size, Major: major, Kind: kind}, nil
		}
	}
	return javaAsset{}, fmt.Errorf("Adoptium has no Temurin %d %s for %s %s in its latest releases", major, javaKindLabel(kind), osName, arch)
}

// javaParseSum reads a .sha256.txt ("<hex>  <name>") for name. Pure.
func javaParseSum(data []byte, name string) (string, error) {
	f := strings.Fields(string(data))
	if len(f) == 0 || len(f[0]) != 64 {
		return "", errors.New("the checksum file is not a SHA-256 line")
	}
	if _, err := hex.DecodeString(f[0]); err != nil {
		return "", errors.New("the checksum file is not a SHA-256 line")
	}
	if len(f) >= 2 && path.Base(strings.TrimPrefix(f[1], "*")) != name {
		return "", errors.New("the checksum file names another archive")
	}
	return strings.ToLower(f[0]), nil
}

// javaGet fetches a small file from GitHub (the release list, a checksum).
func javaGet(ctx context.Context, raw string, max int64) ([]byte, error) {
	if err := validatePatchURL(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub-Java/1.0")
	if strings.HasPrefix(raw, "https://api.github.com/") {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	resp, err := pmPythonClient(60 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("GitHub's limit for requests without an account is reached — try again in an hour")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub answered HTTP %d for %s", resp.StatusCode, path.Base(req.URL.Path))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than expected", path.Base(req.URL.Path))
	}
	return data, nil
}

// javaDownload streams the archive to dest, accepting it only when its
// SHA-256 is want.
func javaDownload(ctx context.Context, raw, dest, want string, progress func(done, total int64)) error {
	if err := validatePatchURL(raw); err != nil {
		return err
	}
	part := dest + ".part"
	defer os.Remove(part)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub-Java/1.0")
	resp, err := pmPythonClient(30 * time.Minute).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered HTTP %d for the Java archive", resp.StatusCode)
	}
	if resp.ContentLength > javaMaxArchive {
		return errors.New("the Java archive is larger than expected")
	}
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	var body io.Reader = io.LimitReader(resp.Body, javaMaxArchive+1)
	if progress != nil {
		body = &progressReader{r: body, total: resp.ContentLength, fn: progress}
	}
	n, copyErr := io.Copy(io.MultiWriter(out, hash), body)
	syncErr := out.Sync()
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return copyErr
	case syncErr != nil:
		return syncErr
	case closeErr != nil:
		return closeErr
	case n > javaMaxArchive:
		return errors.New("the Java archive is larger than expected")
	case hex.EncodeToString(hash.Sum(nil)) != want:
		return errors.New("the Java archive did not match the SHA-256 Adoptium published for it")
	}
	return os.Rename(part, dest)
}

// javaCleanRel checks a member path below the archive's top folder.
func javaCleanRel(rel string) error {
	if strings.ContainsAny(rel, "\\\x00") || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." ||
		strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") || strings.Contains(rel, ":") {
		return fmt.Errorf("the Java archive has an unsafe path %q", rel)
	}
	return nil
}

// javaSplitMember splits an archive member into its top folder and the path
// below it, and checks both: one top folder for the whole archive (*top keeps
// the first), named like Temurin's. rel is "" for the top folder itself.
func javaSplitMember(name string, top *string) (string, error) {
	name = strings.TrimPrefix(name, "./")
	if name == "" {
		return "", nil
	}
	if strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) {
		return "", fmt.Errorf("the Java archive has an unsafe path %q", name)
	}
	t, rel, _ := strings.Cut(name, "/")
	if *top == "" {
		if !javaTopRe.MatchString(t) {
			return "", fmt.Errorf("the Java archive has an unexpected top folder %q", t)
		}
		*top = t
	} else if t != *top {
		return "", fmt.Errorf("the Java archive has more than one top folder (%q)", t)
	}
	rel = strings.TrimSuffix(rel, "/")
	if rel == "" {
		return "", nil
	}
	if err := javaCleanRel(rel); err != nil {
		return "", err
	}
	return rel, nil
}

// javaWriteMember writes one regular file below dest.
func javaWriteMember(dest, rel string, r io.Reader, size int64, executable bool) error {
	target := filepath.Join(dest, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(r, size+1))
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return copyErr
	case closeErr != nil:
		return closeErr
	case n != size:
		return fmt.Errorf("the Java archive's %s is cut short", rel)
	}
	return nil
}

// javaExtractTarGz unpacks a Linux archive into dest without its top folder.
// Links are never created (the card is FAT): a link to a file in the archive
// becomes a copy of it, a link to a folder is left out.
func javaExtractTarGz(ctx context.Context, archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string]bool{}
	type link struct{ name, target string }
	var links []link
	top := ""
	count, entries := 0, 0
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		// Folders and links count too, so the link list stays bounded.
		if entries++; entries > javaMaxFiles*2 {
			return errors.New("the Java archive has more files than expected")
		}
		if h.Typeflag == tar.TypeXGlobalHeader || h.Typeflag == tar.TypeXHeader {
			continue
		}
		rel, err := javaSplitMember(h.Name, &top)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(rel)), 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			count++
			total += h.Size
			if count > javaMaxFiles || h.Size < 0 || total > javaMaxExpanded {
				return errors.New("the Java archive expands beyond its limit")
			}
			// The launchers and the helpers Java starts itself must run,
			// whatever the archive's modes say (a FAT card ignores them).
			executable := h.Mode&0o111 != 0 || strings.HasPrefix(rel, "bin/") ||
				rel == "lib/jspawnhelper" || rel == "lib/jexec"
			if err := javaWriteMember(dest, rel, tr, h.Size, executable); err != nil {
				return err
			}
			files[rel] = true
		case tar.TypeSymlink:
			t := h.Linkname
			if t == "" || path.IsAbs(t) || strings.ContainsAny(t, "\\\x00") {
				continue
			}
			joined := path.Join(path.Dir(rel), t)
			if joined == ".." || strings.HasPrefix(joined, "../") {
				continue
			}
			links = append(links, link{rel, joined})
		case tar.TypeLink:
			t := strings.TrimPrefix(h.Linkname, "./")
			if rest, ok := strings.CutPrefix(t, top+"/"); ok && javaCleanRel(rest) == nil {
				links = append(links, link{rel, rest})
			}
		}
	}
	// Links to links resolve over a few passes; links to folders are dropped.
	for pass := 0; pass < 4 && len(links) > 0; pass++ {
		var left []link
		for _, l := range links {
			if !files[l.target] {
				left = append(left, l)
				continue
			}
			from := filepath.Join(dest, filepath.FromSlash(l.target))
			to := filepath.Join(dest, filepath.FromSlash(l.name))
			info, err := os.Stat(from)
			if err != nil {
				return err
			}
			count++
			total += info.Size()
			if count > javaMaxFiles || total > javaMaxExpanded {
				return errors.New("the Java archive expands beyond its limit")
			}
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return err
			}
			if err := copyFileStream(from, to, info.Mode().Perm()|0o644); err != nil {
				return err
			}
			files[l.name] = true
		}
		if len(left) == len(links) {
			break
		}
		links = left
	}
	if !files["bin/java"] {
		return errors.New("the Java archive has no bin/java")
	}
	return nil
}

// javaExtractZip unpacks a Windows archive into dest without its top folder.
func javaExtractZip(ctx context.Context, archive, dest string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	if len(r.File) > javaMaxFiles*2 {
		return errors.New("the Java archive has more files than expected")
	}
	top := ""
	count := 0
	var total int64
	haveJava := false
	for _, f := range r.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := javaSplitMember(f.Name, &top)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		mode := f.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			continue
		case mode.IsDir() || strings.HasSuffix(f.Name, "/"):
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(rel)), 0o755); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			continue
		}
		count++
		size := int64(f.UncompressedSize64)
		total += size
		if count > javaMaxFiles || size < 0 || f.UncompressedSize64 > uint64(javaMaxExpanded) || total > javaMaxExpanded {
			return errors.New("the Java archive expands beyond its limit")
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		werr := javaWriteMember(dest, rel, rc, size, false)
		rc.Close()
		if werr != nil {
			return werr
		}
		if strings.EqualFold(rel, "bin/java.exe") {
			haveJava = true
		}
	}
	if !haveJava {
		return errors.New("the Java archive has no bin\\java.exe")
	}
	return nil
}

// javaManagedDir is <data>/java, JukaHub's Java ("" without a data folder).
func javaManagedDir() string {
	data, err := P().DataDir()
	if err != nil || data == "" {
		return ""
	}
	return filepath.Join(data, "java")
}

// javaManagedInfo reads the marker of JukaHub's Java.
func javaManagedInfo() (javaInstalled, bool) {
	dir := javaManagedDir()
	if dir == "" {
		return javaInstalled{}, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, javaMarker))
	if err != nil || len(raw) > 4<<10 {
		return javaInstalled{}, false
	}
	var m javaInstalled
	if json.Unmarshal(raw, &m) != nil || m.Name == "" || m.Major <= 0 {
		return javaInstalled{}, false
	}
	return m, true
}

// javaManagedRuntime is JukaHub's Java when it is complete. It starts nothing:
// the version was probed when it was installed.
func javaManagedRuntime() (javaRuntime, bool) {
	m, ok := javaManagedInfo()
	if !ok {
		return javaRuntime{}, false
	}
	java := filepath.Join(javaManagedDir(), "bin", javaExeName())
	if !regularFile(java) {
		return javaRuntime{}, false
	}
	return javaRuntimeAt(java, m.Major, m.Version, true), true
}

// javaInstallIO is how an install reports: the install card's phase and
// bytes, and plain lines for the Terminal. Any of them may be nil.
type javaInstallIO struct {
	phase    func(phase string)
	progress func(done, total int64)
	say      func(line string)
}

func (o javaInstallIO) setPhase(p string) {
	if o.phase != nil {
		o.phase(p)
	}
}

func (o javaInstallIO) line(s string) {
	if o.say != nil {
		o.say(s)
	}
}

// javaInstallState is the one install that may run at a time, and who waits
// for it (javaEnsure calls that it satisfies).
var javaInstallState struct {
	mu      sync.Mutex
	busy    bool
	major   int
	kind    string
	waiters []javaInstallWaiter
}

type javaInstallWaiter struct {
	minMajor int
	needJDK  bool
	then     func(rt javaRuntime)
}

// javaInstallBusy names the install that is running, or "".
func javaInstallBusy() string {
	javaInstallState.mu.Lock()
	defer javaInstallState.mu.Unlock()
	if !javaInstallState.busy {
		return ""
	}
	switch javaInstallState.kind {
	case javaKindRemove:
		return "Removing JukaHub's Java"
	case javaKindTidy:
		return "Deleting an unfinished Java download"
	}
	return "Java " + strconv.Itoa(javaInstallState.major) + " (" + javaKindLabel(javaInstallState.kind) + ")"
}

// javaInstallBegin claims the install slot.
func javaInstallBegin(major int, kind string) bool {
	javaInstallState.mu.Lock()
	defer javaInstallState.mu.Unlock()
	if javaInstallState.busy {
		return false
	}
	javaInstallState.busy, javaInstallState.major, javaInstallState.kind = true, major, kind
	return true
}

// javaInstallEnd frees the slot and returns who waited for it.
func javaInstallEnd() []javaInstallWaiter {
	javaInstallState.mu.Lock()
	defer javaInstallState.mu.Unlock()
	w := javaInstallState.waiters
	javaInstallState.busy, javaInstallState.waiters = false, nil
	return w
}

// javaInstallJoin queues then on the running install when that brings a Java
// that satisfies the request.
func javaInstallJoin(minMajor int, needJDK bool, then func(rt javaRuntime)) bool {
	javaInstallState.mu.Lock()
	defer javaInstallState.mu.Unlock()
	if !javaInstallState.busy || javaInstallState.major < minMajor || (needJDK && javaInstallState.kind != javaKindJDK) {
		return false
	}
	javaInstallState.waiters = append(javaInstallState.waiters, javaInstallWaiter{minMajor, needJDK, then})
	return true
}

// javaLeftoverPatterns are what an install or a removal leaves in the data
// folder while it works: the download, the stage folder, the old Java.
var javaLeftoverPatterns = []string{".java-stage-*", ".java-old-*", ".java-dl-*"}

// javaCleanLeftovers removes what an install that was cut off left in data.
// The caller holds the install slot.
func javaCleanLeftovers(data string) {
	for _, pattern := range javaLeftoverPatterns {
		old, _ := filepath.Glob(filepath.Join(data, pattern))
		for _, o := range old {
			if err := os.RemoveAll(o); err != nil {
				logDebug("java", "could not remove %s: %v", o, err)
			}
		}
	}
}

// javaTidyOnce runs javaTidyLeftovers once per run (the first scan).
var javaTidyOnce sync.Once

// javaTidyLeftovers deletes what an install or a removal that was cut off
// (JukaHub closed, the battery ran out) left behind — up to a few hundred MB
// on the card — without waiting for the next install. It takes the install
// slot for that; when an install or a removal holds it, that one cleans up
// itself. Off the main thread.
func javaTidyLeftovers() {
	data, err := P().DataDir()
	if err != nil || data == "" {
		return
	}
	found := false
	for _, pattern := range javaLeftoverPatterns {
		if m, _ := filepath.Glob(filepath.Join(data, pattern)); len(m) > 0 {
			found = true
			break
		}
	}
	if !found || !javaInstallBegin(0, javaKindTidy) {
		return
	}
	defer javaInstallEnd()
	logInfo("java", "deleting what an unfinished Java install left in %s", data)
	javaCleanLeftovers(data)
}

// javaCheckSpace refuses an install the volume has no room for: the archive
// plus what it unpacks to (about twice its size again), while the old Java
// stays until the new one works. A volume that cannot say how much is free
// is not refused.
func javaCheckSpace(dir string, archive int64) error {
	if archive <= 0 {
		return nil
	}
	free, err := filesFreeSpace(dir)
	if err != nil || free <= 0 {
		return nil
	}
	need := archive*3 + 32<<20
	if free >= need {
		return nil
	}
	return fmt.Errorf("not enough free space on %s: Java needs about %s and %s is free — delete something and try again",
		javaVolumeName(), formatSize(need), formatSize(free))
}

// javaVolumeName is where JukaHub's data lives, in words.
func javaVolumeName() string {
	if IsTrimuiDevice() {
		return "the SD card"
	}
	return "the disk"
}

// javaPlainIOError says a full card plainly; any other error stays as it is.
func javaPlainIOError(err error) error {
	if isDiskFullError(err) {
		return errors.New(javaVolumeName() + " is full — delete something and try again")
	}
	return err
}

// javaRename moves a folder. Windows refuses that for a moment while its
// virus scanner still reads files that were just written or run, so there it
// tries again for about three seconds before giving up.
func javaRename(from, to string) error {
	err := os.Rename(from, to)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	for wait := 100 * time.Millisecond; wait <= 1600*time.Millisecond; wait *= 2 {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(wait)
		if err = os.Rename(from, to); err == nil {
			return nil
		}
	}
	return err
}

// javaWriteSynced writes a small file and waits until it is on the card.
func javaWriteSynced(p string, data []byte) error {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	switch {
	case werr != nil:
		return werr
	case serr != nil:
		return serr
	}
	return cerr
}

// javaFindRelease looks up the archive to install for major and kind.
func javaFindRelease(ctx context.Context, major int, kind string) (javaAsset, error) {
	osName, arch, _, ok := javaPlatform()
	if !ok {
		return javaAsset{}, errors.New("Adoptium has no Temurin build for " + javaPlatformLabel())
	}
	var asset javaAsset
	var err error
	for page := 1; page <= javaReleasePages; page++ {
		list, gerr := javaGet(ctx, fmt.Sprintf(javaReleasesURL, major, javaReleasesPerPage, page), javaMaxReleases)
		if gerr != nil {
			return javaAsset{}, fmt.Errorf("reading Temurin's releases: %w", gerr)
		}
		asset, err = javaPickAsset(list, major, kind, osName, arch)
		if err == nil || javaReleaseCount(list) < javaReleasesPerPage {
			break
		}
	}
	if err != nil && runtime.GOOS == "windows" && runtime.GOARCH == "arm64" {
		return javaAsset{}, fmt.Errorf("Adoptium has no Temurin %d %s for Windows on ARM", major, javaKindLabel(kind))
	}
	return asset, err
}

// javaReleaseCount is how many releases a page of the list holds (0 when it
// cannot be read), so a short page ends the walk.
func javaReleaseCount(data []byte) int {
	var releases []json.RawMessage
	if json.Unmarshal(data, &releases) != nil {
		return 0
	}
	return len(releases)
}

// javaInstall installs Temurin major (JRE or JDK) as JukaHub's Java,
// replacing the one there after the new one started fine. Off the main
// thread; the caller holds the install slot.
func javaInstall(ctx context.Context, major int, kind string, o javaInstallIO) (javaRuntime, error) {
	if kind != javaKindJDK {
		kind = javaKindJRE
	}
	data, err := P().DataDir()
	if err != nil {
		return javaRuntime{}, err
	}
	if err := os.MkdirAll(data, 0o755); err != nil {
		return javaRuntime{}, err
	}
	javaCleanLeftovers(data)
	o.setPhase(installPhaseResolving)
	o.line(fmt.Sprintf("Looking for the newest Temurin %d %s for %s…", major, javaKindLabel(kind), javaPlatformLabel()))
	asset, err := javaFindRelease(ctx, major, kind)
	if err != nil {
		return javaRuntime{}, err
	}
	sums, err := javaGet(ctx, asset.SumURL, javaMaxSumFile)
	if err != nil {
		return javaRuntime{}, fmt.Errorf("reading the archive's checksum: %w", err)
	}
	want, err := javaParseSum(sums, asset.Name)
	if err != nil {
		return javaRuntime{}, err
	}
	if err := javaCheckSpace(data, asset.Size); err != nil {
		return javaRuntime{}, err
	}
	sizeNote := ""
	if asset.Size > 0 {
		sizeNote = " (" + formatSize(asset.Size) + ")"
	}
	o.line("Downloading " + asset.Name + sizeNote + "…")
	o.setPhase(installPhaseDownload)
	archive := filepath.Join(data, ".java-dl-"+asset.Name)
	if err := javaDownload(ctx, asset.URL, archive, want, o.progress); err != nil {
		return javaRuntime{}, javaPlainIOError(err)
	}
	defer os.Remove(archive)
	o.line("The download matches its SHA-256. Unpacking…")
	o.setPhase(installPhaseExtract)
	stage, err := os.MkdirTemp(data, ".java-stage-")
	if err != nil {
		return javaRuntime{}, javaPlainIOError(err)
	}
	defer os.RemoveAll(stage)
	if strings.HasSuffix(asset.Name, ".zip") {
		err = javaExtractZip(ctx, archive, stage)
	} else {
		err = javaExtractTarGz(ctx, archive, stage)
	}
	if err != nil {
		return javaRuntime{}, javaPlainIOError(err)
	}
	o.setPhase(installPhaseVerify)
	o.line("Checking that it starts here…")
	java := filepath.Join(stage, "bin", javaExeName())
	probeCtx, probeCancel := context.WithTimeout(ctx, javaProbeTimeout)
	got, version, err := javaProbeVersion(probeCtx, java, nil)
	probeCancel()
	if err != nil {
		return javaRuntime{}, fmt.Errorf("the downloaded Java did not start here: %w", err)
	}
	if got != major {
		return javaRuntime{}, fmt.Errorf("the archive holds Java %d, not %d", got, major)
	}
	if kind == javaKindJDK && !regularFile(filepath.Join(stage, "bin", javaToolName("javac"))) {
		return javaRuntime{}, errors.New("the JDK archive has no javac")
	}
	raw, err := json.Marshal(javaInstalled{Name: asset.Name, SHA256: want, Major: major, Version: version, Kind: kind,
		Installed: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return javaRuntime{}, err
	}
	// The files reach the card before the marker that makes them "ready",
	// and the marker before the folder is moved into place.
	if err := syncDisks(); err != nil {
		logWarn("java", "%v", err)
	}
	if err := javaWriteSynced(filepath.Join(stage, javaMarker), raw); err != nil {
		return javaRuntime{}, javaPlainIOError(err)
	}
	final := filepath.Join(data, "java")
	old := ""
	if _, err := os.Lstat(final); err == nil {
		old = filepath.Join(data, ".java-old-"+strconv.FormatInt(time.Now().UnixNano(), 36))
		if err := javaRename(final, old); err != nil {
			return javaRuntime{}, javaInUseError(err)
		}
	}
	if err := javaRename(stage, final); err != nil {
		if old != "" {
			if rerr := javaRename(old, final); rerr != nil {
				logWarn("java", "could not put the old Java back (%v); it is gone until Java is installed again", rerr)
			}
		}
		return javaRuntime{}, javaPlainIOError(err)
	}
	if err := syncDisks(); err != nil {
		logWarn("java", "%v", err)
	}
	if old != "" {
		if err := os.RemoveAll(old); err != nil {
			logWarn("java", "could not delete the old Java (%v); JukaHub deletes the rest on its next start", err)
		}
	}
	logInfo("java", "installed %s (%s)", asset.Name, version)
	o.line("Installed Java " + version + " (" + javaKindLabel(kind) + ") in " + final)
	javaRescanNow()
	rt, ok := javaManagedRuntime()
	if !ok {
		return javaRuntime{}, errors.New("the installed Java is not complete")
	}
	return rt, nil
}

// javaInUseError explains a rename that failed, most often because a Java
// program is still running from the folder (Windows locks it).
func javaInUseError(err error) error {
	if runtime.GOOS == "windows" {
		return errors.New("Java is in use — close the Java programs that are running and try again")
	}
	return err
}

// javaRemove deletes JukaHub's Java. The folder is moved aside first, so a
// removal that stops halfway never leaves a Java that looks ready.
func javaRemove() error {
	data, err := P().DataDir()
	if err != nil {
		return err
	}
	final := filepath.Join(data, "java")
	if _, err := os.Lstat(final); err != nil {
		javaDropManaged()
		return nil
	}
	old := filepath.Join(data, ".java-old-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := javaRename(final, old); err != nil {
		return javaInUseError(err)
	}
	javaDropManaged()
	// The caller holds the install slot: what a cut-off install left goes
	// too.
	javaCleanLeftovers(data)
	if _, err := os.Lstat(old); err == nil {
		logWarn("java", "could not delete all of the old Java; JukaHub deletes the rest on its next start")
	}
	logInfo("java", "removed JukaHub's Java")
	return nil
}

// javaUpdateCheck is the last update check, kept in <data>/java-update-check.json
// so the Java screen checks by itself at most once a day.
type javaUpdateCheck struct {
	Checked string `json:"checked"`
	Have    string `json:"have"`
	Latest  string `json:"latest"`
}

func javaUpdateCheckPath() string {
	data, err := P().DataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(data, javaCheckFile)
}

// javaLoadUpdateCheck reads the last check (zero when there is none).
func javaLoadUpdateCheck() javaUpdateCheck {
	var c javaUpdateCheck
	p := javaUpdateCheckPath()
	if p == "" {
		return c
	}
	raw, err := os.ReadFile(p)
	if err != nil || len(raw) > 4<<10 {
		return c
	}
	_ = json.Unmarshal(raw, &c)
	return c
}

// javaCheckForUpdate asks GitHub for the newest build of the installed
// version and kind. newer is true when it is not the one installed. Off the
// main thread.
func javaCheckForUpdate(ctx context.Context) (javaAsset, bool, error) {
	m, ok := javaManagedInfo()
	if !ok {
		return javaAsset{}, false, errors.New("JukaHub has not installed a Java")
	}
	asset, err := javaFindRelease(ctx, m.Major, m.Kind)
	if err != nil {
		return javaAsset{}, false, err
	}
	c := javaUpdateCheck{Checked: time.Now().UTC().Format(time.RFC3339), Have: m.Name, Latest: asset.Name}
	if raw, err := json.Marshal(c); err == nil {
		if p := javaUpdateCheckPath(); p != "" {
			_ = os.WriteFile(p, raw, 0o644)
		}
	}
	return asset, asset.Name != m.Name, nil
}

// javaStartInstall installs major/kind with the install card up. Main
// thread. then (may be nil) runs on the main thread after a successful
// install, as do the javaEnsure calls that waited for it.
func javaStartInstall(major int, kind string, then func(rt javaRuntime)) {
	if !javaInstallBegin(major, kind) {
		ShowToast("Busy: "+javaInstallBusy()+" — one moment", ToastKindInfo)
		return
	}
	if !IsOnline() {
		javaInstallEnd()
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return
	}
	if then != nil {
		javaInstallState.mu.Lock()
		javaInstallState.waiters = append(javaInstallState.waiters, javaInstallWaiter{minMajor: major, needJDK: kind == javaKindJDK, then: then})
		javaInstallState.mu.Unlock()
	}
	label := "Java " + strconv.Itoa(major) + " (" + javaKindLabel(kind) + ")"
	gen, barCtx := installBarBeginRun(label)
	javaScreenChanged()
	go func() {
		var rt javaRuntime
		var failure error
		defer func() {
			if r := recover(); r != nil {
				failure = fmt.Errorf("%v", r)
			}
			cancelled := failure != nil && barCtx.Err() != nil
			if failure != nil {
				logWarn("java", "install %s: %v", label, failure)
				installBarFailRun(gen, installCardDetail(firstLine(failure.Error())))
			} else {
				installBarDoneRun(gen)
			}
			javaForget()
			waiters := javaInstallEnd()
			tbxPost(func() { javaInstallFinished(label, rt, failure, cancelled, waiters) })
		}()
		ctx, cancel := context.WithTimeout(barCtx, javaInstallTimeout)
		defer cancel()
		rt, failure = javaInstall(ctx, major, kind, javaInstallIO{
			phase:    func(p string) { installBarUpdateRun(gen, p, 0, 0) },
			progress: func(done, total int64) { installBarUpdateRun(gen, installPhaseDownload, done, total) },
		})
	}()
}

// javaInstallFinished reports an install on the main thread and runs who
// waited for it.
func javaInstallFinished(label string, rt javaRuntime, failure error, cancelled bool, waiters []javaInstallWaiter) {
	javaScreenChanged()
	switch {
	case cancelled:
		ShowToast(label+" was not installed: stopped", ToastKindInfo)
		return
	case failure != nil:
		PlayErrorSound()
		ShowToast(label+": "+plainNetworkError(failure), ToastKindError)
		return
	}
	PlaySuccessSound()
	ShowToast(label+" is ready", ToastKindSuccess)
	go javaEnsureUserDir()
	for _, w := range waiters {
		if javaSatisfies(rt, w.minMajor, w.needJDK) {
			javaRunWhenReady(rt, w.then)
		}
	}
}

// javaFinishTermInstall hands the waiters of a Terminal install to the main
// thread.
func javaFinishTermInstall(rt javaRuntime, failure error) {
	waiters := javaInstallEnd()
	tbxPost(func() {
		javaScreenChanged()
		if failure != nil {
			return
		}
		go javaEnsureUserDir()
		for _, w := range waiters {
			if javaSatisfies(rt, w.minMajor, w.needJDK) {
				javaRunWhenReady(rt, w.then)
			}
		}
	})
}
