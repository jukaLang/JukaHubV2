package main

// linuxpkg_index.go — where Linux packages come from and how JukaHub knows
// they are Ubuntu's.
//
// "Linux packages" is a small apt for the TrimUI handheld (Linux arm64, stock
// glibc 2.33): Ubuntu 24.04 "noble" arm64 programs from ports.ubuntu.com,
// unpacked into a folder on the card and started with Ubuntu's own C library
// from that folder (linuxpkg_install.go, linuxpkg_runtime.go). The firmware is
// never changed.
//
// The trust chain is apt's, with one key:
//
//   - dists/<suite>/Release and Release.gpg are fetched for noble,
//     noble-updates and noble-security, and the detached signature must verify
//     against the Ubuntu Archive Automatic Signing Key (2018), embedded here and
//     pinned by fingerprint. No other key is accepted.
//   - The SHA-256 and size of each component's Packages.xz (or .gz) are taken
//     from that verified Release, and the downloaded index must match them.
//   - Every .deb's SHA-256 and size come from that verified Packages index, and
//     a download that does not match is deleted before it is opened.
//
// Because of that chain, plain HTTP is acceptable: stock TrimUI firmware's CA
// bundle cannot validate ports.ubuntu.com, so HTTPS is tried first and HTTP is
// used after a transport failure — as glibc_runtime.go does for its pinned
// package — but no unverified byte is ever unpacked or trusted. A Release older
// than the one already accepted for that suite is refused, so a replayed old
// (validly signed) index cannot roll the card back to vulnerable versions.
//
// The parsed index is kept compact on the card (<data>/linux/index): one tab-
// separated line per package with only the fields the manager uses. The raw
// Packages files run to tens of megabytes and are streamed, never held in
// memory.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// linuxPkgArchiveKey is "Ubuntu Archive Automatic Signing Key (2018)
// <ftpmaster@ubuntu.com>", the key noble's Release.gpg is signed with.
//
//go:embed linuxpkg-ubuntu-archive-2018.asc
var linuxPkgArchiveKey []byte

const (
	linuxPkgHost        = "ports.ubuntu.com"
	linuxPkgArchivePath = "/ubuntu-ports/"
	linuxPkgArch        = "arm64"
	linuxPkgKeyFP       = "f6ecb3762474eda9d21b7022871920d1991bc93c"
	// The bounds on what the archive may send before it is verified.
	linuxPkgReleaseMax = int64(4 << 20)
	linuxPkgSigMax     = int64(256 << 10)
	linuxPkgLineMax    = 1 << 20
	linuxPkgDebMax     = int64(1 << 30)
	// linuxPkgXzDict bounds an index's LZMA2 dictionary (Ubuntu uses 8 MiB).
	linuxPkgXzDict = int64(64 << 20)
)

// linuxPkgSuites are read in this order; a package's newest version across
// them wins (linuxPkgOpenIndex).
var linuxPkgSuites = []string{"noble", "noble-updates", "noble-security"}

// linuxPkgComponents: main is Canonical's supported set; universe is where
// nmap, john, aircrack-ng and most tools live.
var linuxPkgComponents = []string{"main", "universe"}

// linuxPkgSupported reports whether this device runs Linux packages: an ARM64
// TrimUI. A PC has its own package manager, and Ubuntu's arm64 programs mean
// nothing on x86-64.
func linuxPkgSupported() bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64" && IsTrimuiDevice()
}

// linuxPkgLayout is where everything lives under <data>/linux.
type linuxPkgLayout struct {
	base    string // the folder itself: lock, status.json
	root    string // the unpacked packages (usr/, etc/ …)
	bin     string // one wrapper per program; first on the Terminal's PATH
	libexec string // wrappers for programs that run helpers (git-core)
	lists   string // the verified Packages files, reused while unchanged
	index   string // the compact index and its meta.json
	cache   string // downloaded .debs until they are unpacked
	info    string // one file list per installed package
}

func linuxPkgDirs() (linuxPkgLayout, error) {
	data, err := P().DataDir()
	if err != nil {
		return linuxPkgLayout{}, err
	}
	base := filepath.Join(data, "linux")
	if abs, err := filepath.Abs(base); err == nil {
		base = abs
	}
	return linuxPkgLayout{
		base:    base,
		root:    filepath.Join(base, "root"),
		bin:     filepath.Join(base, "bin"),
		libexec: filepath.Join(base, "libexec"),
		lists:   filepath.Join(base, "lists"),
		index:   filepath.Join(base, "index"),
		cache:   filepath.Join(base, "cache"),
		info:    filepath.Join(base, "info"),
	}, nil
}

// ── Downloads ────────────────────────────────────────────────────────────

// linuxPkgPlainHTTP is set for the rest of the session once HTTPS to the
// archive failed with a transport error (stock TrimUI cannot validate the
// certificate), so every later file does not wait for the same failure.
var linuxPkgPlainHTTP atomic.Bool

// linuxPkgTransport is one transport for every archive request, so a run of
// fifty packages reuses its connection instead of opening fifty.
var linuxPkgTransport = sync.OnceValue(ytTransport)

// linuxPkgSum is a SHA-256 and size from a verified Release or Packages file.
type linuxPkgSum struct {
	SHA256 string
	Size   int64
}

// linuxPkgHTTPError is an HTTP answer other than 200: 404 on a .deb means the
// index is older than the archive (linuxPkgApply then updates and retries).
type linuxPkgHTTPError struct {
	code int
	name string
}

func (e *linuxPkgHTTPError) Error() string {
	return fmt.Sprintf("the Ubuntu archive answered HTTP %d for %s", e.code, e.name)
}

// linuxPkgFetch downloads rel (a path under ubuntu-ports/) to dest. With want,
// the file must have exactly that size and SHA-256 or it is deleted; a dest
// that already matches is kept without a download. Without want, the file is
// only bounded by limit and must be verified by the caller (a signature).
func linuxPkgFetch(ctx context.Context, rel, dest string, limit int64, want *linuxPkgSum, progress func(done, total int64)) error {
	if strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\\?#") {
		return fmt.Errorf("refusing archive path %q", rel)
	}
	if want != nil {
		if info, err := os.Stat(dest); err == nil && info.Size() == want.Size {
			if sum, err := privateGlibcFileSHA(dest); err == nil && sum == want.SHA256 {
				return nil
			}
		}
		limit = want.Size
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if !linuxPkgPlainHTTP.Load() {
		err := linuxPkgFetchOnce(ctx, "https://"+linuxPkgHost+linuxPkgArchivePath+rel, dest, limit, want, progress)
		var netErr *url.Error
		if err == nil || ctx.Err() != nil || !errors.As(err, &netErr) {
			return err
		}
		linuxPkgPlainHTTP.Store(true)
		logWarn("linuxpkg", "HTTPS to %s failed (%v); using HTTP, every file is still checked against Ubuntu's signed index", linuxPkgHost, err)
	}
	return linuxPkgFetchOnce(ctx, "http://"+linuxPkgHost+linuxPkgArchivePath+rel, dest, limit, want, progress)
}

func linuxPkgFetchOnce(ctx context.Context, source, dest string, limit int64, want *linuxPkgSum, progress func(done, total int64)) error {
	part := dest + ".part"
	defer os.Remove(part)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	wantScheme := req.URL.Scheme
	client := &http.Client{
		Timeout:   30 * time.Minute,
		Transport: linuxPkgTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 || req.URL.Scheme != wantScheme || req.URL.Hostname() != linuxPkgHost {
				return errors.New("the package download was redirected away from Ubuntu")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &linuxPkgHTTPError{code: resp.StatusCode, name: path.Base(req.URL.Path)}
	}
	if resp.ContentLength > limit {
		return fmt.Errorf("%s is larger than expected", path.Base(req.URL.Path))
	}
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	var body io.Reader = io.LimitReader(resp.Body, limit+1)
	if progress != nil {
		body = &progressReader{r: body, total: resp.ContentLength, fn: progress}
	}
	n, copyErr := io.Copy(io.MultiWriter(out, hash), body)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > limit {
		return fmt.Errorf("%s is larger than expected", path.Base(req.URL.Path))
	}
	if want != nil && (n != want.Size || hex.EncodeToString(hash.Sum(nil)) != want.SHA256) {
		return fmt.Errorf("%s did not match the SHA-256 in Ubuntu's signed index", path.Base(dest))
	}
	return os.Rename(part, dest)
}

// ── Release ──────────────────────────────────────────────────────────────

// linuxPkgRelease is what the manager reads from a suite's Release file.
type linuxPkgRelease struct {
	Suite    string
	Codename string
	Date     time.Time
	ByHash   bool
	// SHA256 maps a path under dists/<suite>/ to its sum.
	SHA256 map[string]linuxPkgSum
}

// linuxPkgIsSHA256 reports a 64-digit lower- or upper-case hex string.
func linuxPkgIsSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// linuxPkgParseRelease reads a Release file. Only the SHA256 section is used
// for files (MD5Sum and SHA1 are ignored); a malformed line in it is an error,
// not a skipped file.
func linuxPkgParseRelease(data []byte) (linuxPkgRelease, error) {
	rel := linuxPkgRelease{SHA256: map[string]linuxPkgSum{}}
	section := ""
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if section != "SHA256" {
				continue
			}
			f := strings.Fields(line)
			if len(f) != 3 {
				return rel, errors.New("the Release file has a malformed SHA256 line")
			}
			size, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil || size < 0 || !linuxPkgIsSHA256(f[0]) {
				return rel, errors.New("the Release file has a malformed SHA256 line")
			}
			rel.SHA256[f[2]] = linuxPkgSum{SHA256: strings.ToLower(f[0]), Size: size}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return rel, errors.New("the Release file has an unreadable line")
		}
		section = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch section {
		case "Suite":
			rel.Suite = value
		case "Codename":
			rel.Codename = value
		case "Acquire-By-Hash":
			rel.ByHash = strings.EqualFold(value, "yes")
		case "Date":
			for _, layout := range []string{time.RFC1123, time.RFC1123Z, "Mon, 2 Jan 2006 15:04:05 MST"} {
				if t, err := time.Parse(layout, value); err == nil {
					rel.Date = t.UTC()
					break
				}
			}
		}
	}
	if len(rel.SHA256) == 0 {
		return rel, errors.New("the Release file lists no SHA-256 sums")
	}
	return rel, nil
}

// linuxPkgVerifyRelease checks Release.gpg against the one embedded key. The
// clock the signature is checked against is never earlier than a day after the
// Release's own date, nor than the signature's own creation time: the handheld
// boots before NTP, and a clock in the past would make Ubuntu's signature look
// "created in the future" (a Release that is re-signed later, as the frozen
// noble one would be for a key change, keeps its old Date). The archive key
// has no expiry and the signatures carry none, so a later clock changes
// nothing; the order of indexes is guarded by their Dates instead.
func linuxPkgVerifyRelease(release, sig []byte, date time.Time) error {
	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(linuxPkgArchiveKey))
	if err != nil || len(keys) != 1 || hex.EncodeToString(keys[0].PrimaryKey.Fingerprint[:]) != linuxPkgKeyFP {
		return errors.New("the built-in Ubuntu archive key does not match its pinned fingerprint")
	}
	floor := date.Add(24 * time.Hour)
	if made := linuxPkgSigTime(sig); made.After(floor) {
		floor = made.Add(time.Minute)
	}
	cfg := &packet.Config{Time: func() time.Time {
		if now := time.Now(); now.After(floor) {
			return now
		}
		return floor
	}}
	if bytes.HasPrefix(bytes.TrimSpace(sig), []byte("-----BEGIN PGP SIGNATURE-----")) {
		_, err = openpgp.CheckArmoredDetachedSignature(keys, bytes.NewReader(release), bytes.NewReader(sig), cfg)
	} else {
		_, err = openpgp.CheckDetachedSignature(keys, bytes.NewReader(release), bytes.NewReader(sig), cfg)
	}
	if err != nil {
		return fmt.Errorf("Ubuntu's signature on the package index did not verify: %w", err)
	}
	return nil
}

// linuxPkgSigTime is the newest creation time among the signature packets of
// a Release.gpg (armoured or not), or zero. It only moves the verification
// clock forward; the time itself is covered by the signature it belongs to.
func linuxPkgSigTime(sig []byte) time.Time {
	var r io.Reader = bytes.NewReader(sig)
	if bytes.HasPrefix(bytes.TrimSpace(sig), []byte("-----BEGIN PGP SIGNATURE-----")) {
		block, err := armor.Decode(bytes.NewReader(sig))
		if err != nil {
			return time.Time{}
		}
		r = block.Body
	}
	var newest time.Time
	packets := packet.NewReader(r)
	for i := 0; i < 16; i++ {
		p, err := packets.Next()
		if err != nil {
			break
		}
		if s, ok := p.(*packet.Signature); ok && s.CreationTime.After(newest) {
			newest = s.CreationTime
		}
	}
	return newest
}

// ── Packages ─────────────────────────────────────────────────────────────

// linuxPkgEntry is one package as the manager keeps it.
type linuxPkgEntry struct {
	Name        string
	Version     string
	Arch        string
	Section     string
	Size        int64 // the .deb
	InstalledKB int64 // Installed-Size, in KiB
	SHA256      string
	Filename    string // pool/… under ubuntu-ports/
	Essential   bool
	Depends     string
	PreDepends  string
	Provides    string
	Desc        string // the Description's first line
}

// linuxPkgSafePoolPath accepts a Filename that stays under pool/.
func linuxPkgSafePoolPath(p string) bool {
	return strings.HasPrefix(p, "pool/") && path.Clean(p) == p && !strings.Contains(p, "..") &&
		!strings.ContainsAny(p, "\\?#\x00 \t")
}

// linuxPkgEntryUsable reports whether a stanza can be installed from here.
func linuxPkgEntryUsable(e linuxPkgEntry) bool {
	return linuxPkgValidName(e.Name) && e.Version != "" && !strings.ContainsAny(e.Version, " \t") &&
		(e.Arch == linuxPkgArch || e.Arch == "all") && linuxPkgSafePoolPath(e.Filename) &&
		linuxPkgIsSHA256(e.SHA256) && e.Size > 0 && e.Size <= linuxPkgDebMax
}

// linuxPkgReadLine reads one line of any length up to linuxPkgLineMax.
func linuxPkgReadLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadSlice('\n')
	if err == nil || !errors.Is(err, bufio.ErrBufferFull) {
		return string(line), err
	}
	buf := append([]byte(nil), line...)
	for errors.Is(err, bufio.ErrBufferFull) {
		if len(buf) > linuxPkgLineMax {
			return "", errors.New("the package index has an over-long line")
		}
		line, err = br.ReadSlice('\n')
		buf = append(buf, line...)
	}
	return string(buf), err
}

// linuxPkgReadPackages streams a Packages file and calls fn for every stanza
// that can be installed here. skipped counts the others (wrong architecture,
// unsafe path, malformed sums).
func linuxPkgReadPackages(r io.Reader, fn func(linuxPkgEntry) error) (skipped int, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var e linuxPkgEntry
	field := ""
	have := false
	flush := func() error {
		defer func() { e, field, have = linuxPkgEntry{}, "", false }()
		if !have {
			return nil
		}
		if !linuxPkgEntryUsable(e) {
			skipped++
			return nil
		}
		return fn(e)
	}
	for {
		line, rerr := linuxPkgReadLine(br)
		if rerr != nil && rerr != io.EOF {
			return skipped, rerr
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.TrimSpace(line) == "":
			if err := flush(); err != nil {
				return skipped, err
			}
		case line[0] == ' ' || line[0] == '\t':
			// A folded line continues the field above; only the relation
			// fields are worth joining.
			cont := strings.TrimSpace(line)
			switch field {
			case "Depends":
				e.Depends += " " + cont
			case "Pre-Depends":
				e.PreDepends += " " + cont
			case "Provides":
				e.Provides += " " + cont
			}
		default:
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				field = ""
				continue
			}
			have = true
			field = key
			value = strings.TrimSpace(value)
			switch key {
			case "Package":
				e.Name = value
			case "Version":
				e.Version = value
			case "Architecture":
				e.Arch = value
			case "Section":
				e.Section = value
			case "Filename":
				e.Filename = value
			case "SHA256":
				e.SHA256 = strings.ToLower(value)
			case "Size":
				e.Size, _ = strconv.ParseInt(value, 10, 64)
			case "Installed-Size":
				e.InstalledKB, _ = strconv.ParseInt(value, 10, 64)
			case "Depends":
				e.Depends = value
			case "Pre-Depends":
				e.PreDepends = value
			case "Provides":
				e.Provides = value
			case "Essential":
				e.Essential = strings.EqualFold(value, "yes")
			case "Description":
				e.Desc = value
			}
		}
		if rerr == io.EOF {
			return skipped, flush()
		}
	}
}

// linuxPkgTSVClean keeps a field on its line and in its column.
func linuxPkgTSVClean(s string) string {
	if strings.ContainsAny(s, "\t\r\n") {
		s = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(s)
	}
	return strings.TrimSpace(s)
}

// tsv is the entry as one index line (without the newline).
func (e linuxPkgEntry) tsv() string {
	essential := "0"
	if e.Essential {
		essential = "1"
	}
	return strings.Join([]string{
		e.Name, e.Version, e.Arch, linuxPkgTSVClean(e.Section),
		strconv.FormatInt(e.Size, 10), strconv.FormatInt(e.InstalledKB, 10),
		e.SHA256, e.Filename, essential,
		linuxPkgTSVClean(e.Depends), linuxPkgTSVClean(e.PreDepends), linuxPkgTSVClean(e.Provides),
		linuxPkgTSVClean(e.Desc),
	}, "\t")
}

// linuxPkgParseTSV reads an index line back.
func linuxPkgParseTSV(line string) (linuxPkgEntry, bool) {
	f := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
	if len(f) != 13 {
		return linuxPkgEntry{}, false
	}
	size, err1 := strconv.ParseInt(f[4], 10, 64)
	isize, err2 := strconv.ParseInt(f[5], 10, 64)
	if err1 != nil || err2 != nil {
		return linuxPkgEntry{}, false
	}
	e := linuxPkgEntry{
		Name: f[0], Version: f[1], Arch: f[2], Section: f[3], Size: size, InstalledKB: isize,
		SHA256: f[6], Filename: f[7], Essential: f[8] == "1",
		Depends: f[9], PreDepends: f[10], Provides: f[11], Desc: f[12],
	}
	return e, linuxPkgEntryUsable(e)
}

// ── Meta ─────────────────────────────────────────────────────────────────

// linuxPkgMeta is what the last update left: what each suite's verified
// Release said, and the counts the page shows without opening the index.
type linuxPkgMeta struct {
	// Updated is the wall-clock time of the last update, for display only (the
	// handheld's clock may be wrong until NTP).
	Updated    string                       `json:"updated,omitempty"`
	Suites     map[string]linuxPkgMetaSuite `json:"suites,omitempty"`
	Packages   int                          `json:"packages,omitempty"`
	Upgradable int                          `json:"upgradable"`
}

type linuxPkgMetaSuite struct {
	// Date is the verified Release's own Date (RFC 3339): never older on the
	// next update.
	Date  string            `json:"date"`
	Lists map[string]string `json:"lists,omitempty"`
}

func linuxPkgLoadMeta(lay linuxPkgLayout) linuxPkgMeta {
	var m linuxPkgMeta
	data, err := os.ReadFile(filepath.Join(lay.index, "meta.json"))
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func linuxPkgSaveMeta(lay linuxPkgLayout, m linuxPkgMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return linuxPkgWriteFile(filepath.Join(lay.index, "meta.json"), data)
}

// linuxPkgWriteFile replaces a small bookkeeping file through a temporary file
// and a rename, so a pulled card leaves the old copy or the new one.
func linuxPkgWriteFile(target string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr == nil {
		werr = serr
	}
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	return os.Rename(tmp, target)
}

// ── Update ───────────────────────────────────────────────────────────────

// linuxPkgListFile is one verified Packages file on the card.
type linuxPkgListFile struct {
	path string
	xz   bool
}

// linuxPkgUpdate is `apt update`: the three suites' signed Releases, their
// Packages files (only the ones whose SHA-256 changed are downloaded again),
// and a new compact index when anything changed.
func linuxPkgUpdate(ctx context.Context, o *linuxPkgIO) error {
	lay, err := linuxPkgPrepare()
	if err != nil {
		return err
	}
	old := linuxPkgLoadMeta(lay)
	next := linuxPkgMeta{Suites: map[string]linuxPkgMetaSuite{}}
	var lists []linuxPkgListFile
	changed := !regularFile(filepath.Join(lay.index, "packages.tsv"))
	keep := map[string]bool{}
	for n, suite := range linuxPkgSuites {
		o.line(fmt.Sprintf("Get:%d %s %s Release", n+1, linuxPkgHost, suite))
		relFile := filepath.Join(lay.lists, suite+"_Release")
		sigFile := relFile + ".gpg"
		if err := linuxPkgFetch(ctx, "dists/"+suite+"/Release", relFile, linuxPkgReleaseMax, nil, nil); err != nil {
			return fmt.Errorf("%s: %w", suite, err)
		}
		if err := linuxPkgFetch(ctx, "dists/"+suite+"/Release.gpg", sigFile, linuxPkgSigMax, nil, nil); err != nil {
			return fmt.Errorf("%s: %w", suite, err)
		}
		keep[relFile], keep[sigFile] = true, true
		data, err := os.ReadFile(relFile)
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(sigFile)
		if err != nil {
			return err
		}
		// Parsed before it is verified only for its date (the clock floor);
		// nothing else is used until the signature has checked out.
		rel, perr := linuxPkgParseRelease(data)
		if err := linuxPkgVerifyRelease(data, sig, rel.Date); err != nil {
			return fmt.Errorf("%s: %w", suite, err)
		}
		if perr != nil {
			return fmt.Errorf("%s: %w", suite, perr)
		}
		// The Suite itself, not the Codename: every pocket says "Codename:
		// noble", so the signed noble-updates Release would otherwise pass for
		// noble, and its newer Date would then refuse the real one for good.
		if rel.Suite != suite {
			return fmt.Errorf("%s: the signed Release is for %q, not %s", suite, rel.Suite, suite)
		}
		if prev, ok := old.Suites[suite]; ok && !rel.Date.IsZero() {
			if t, err := time.Parse(time.RFC3339, prev.Date); err == nil && rel.Date.Before(t) {
				return fmt.Errorf("%s: the archive sent an older signed index (%s) than the one already on this card (%s); refusing it",
					suite, rel.Date.Format("2006-01-02"), t.Format("2006-01-02"))
			}
		}
		// A Date that did not parse keeps the one already accepted, so the
		// rollback guard above is never dropped by an update.
		ms := linuxPkgMetaSuite{Lists: map[string]string{}, Date: old.Suites[suite].Date}
		if !rel.Date.IsZero() {
			ms.Date = rel.Date.Format(time.RFC3339)
		}
		for _, comp := range linuxPkgComponents {
			found := false
			for _, ext := range []string{".xz", ".gz"} {
				name := comp + "/binary-" + linuxPkgArch + "/Packages" + ext
				sum, ok := rel.SHA256[name]
				if !ok {
					continue
				}
				local := filepath.Join(lay.lists, suite+"_"+comp+"_Packages"+ext)
				remote := "dists/" + suite + "/" + name
				if rel.ByHash {
					remote = "dists/" + suite + "/" + comp + "/binary-" + linuxPkgArch + "/by-hash/SHA256/" + sum.SHA256
				}
				if old.Suites[suite].Lists[comp] != sum.SHA256 {
					changed = true
					o.line(fmt.Sprintf("Get: %s %s/binary-%s Packages [%s]", suite, comp, linuxPkgArch, formatSize(sum.Size)))
				}
				if err := linuxPkgFetch(ctx, remote, local, 0, &sum, o.progress); err != nil {
					return fmt.Errorf("%s %s: %w", suite, comp, err)
				}
				lists = append(lists, linuxPkgListFile{path: local, xz: ext == ".xz"})
				keep[local] = true
				ms.Lists[comp] = sum.SHA256
				found = true
				break
			}
			if !found {
				return fmt.Errorf("%s: the signed Release lists no %s package index for %s", suite, comp, linuxPkgArch)
			}
		}
		next.Suites[suite] = ms
	}
	// Lists from an earlier layout or a removed component.
	if entries, err := os.ReadDir(lay.lists); err == nil {
		for _, e := range entries {
			if p := filepath.Join(lay.lists, e.Name()); !keep[p] {
				_ = os.Remove(p)
			}
		}
	}
	next.Updated = time.Now().UTC().Format(time.RFC3339)
	if !changed {
		next.Packages, next.Upgradable = old.Packages, old.Upgradable
		if err := linuxPkgSaveMeta(lay, next); err != nil {
			return err
		}
		o.line("The package index is already up to date.")
		return nil
	}
	o.line("Reading package lists…")
	count, err := linuxPkgBuildIndex(ctx, lay, lists)
	if err != nil {
		return err
	}
	next.Packages = count
	if err := linuxPkgSaveMeta(lay, next); err != nil {
		return err
	}
	o.line(fmt.Sprintf("%s in the index.", pluralizeCount(count, "package", "packages")))
	logInfo("linuxpkg", "index updated: %d packages (plain HTTP: %v)", count, linuxPkgPlainHTTP.Load())
	return nil
}

// linuxPkgBuildIndex streams the verified Packages files into a new compact
// index and swaps it in.
func linuxPkgBuildIndex(ctx context.Context, lay linuxPkgLayout, lists []linuxPkgListFile) (int, error) {
	target := filepath.Join(lay.index, "packages.tsv")
	if err := os.MkdirAll(lay.index, 0o755); err != nil {
		return 0, err
	}
	tmp := target + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp)
	bw := bufio.NewWriterSize(out, 256<<10)
	count := 0
	for _, l := range lists {
		if err := ctx.Err(); err != nil {
			out.Close()
			return 0, err
		}
		f, err := os.Open(l.path)
		if err != nil {
			out.Close()
			return 0, err
		}
		var r io.Reader
		if l.xz {
			r = newFeXzReader(ctx, f, linuxPkgXzDict)
		} else {
			gz, gerr := gzip.NewReader(f)
			if gerr != nil {
				f.Close()
				out.Close()
				return 0, gerr
			}
			r = gz
		}
		_, perr := linuxPkgReadPackages(r, func(e linuxPkgEntry) error {
			count++
			_, werr := bw.WriteString(e.tsv() + "\n")
			return werr
		})
		f.Close()
		if perr != nil {
			out.Close()
			return 0, fmt.Errorf("%s: %w", filepath.Base(l.path), perr)
		}
	}
	if err := bw.Flush(); err != nil {
		out.Close()
		return 0, err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return 0, err
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, errors.New("the package index came out empty")
	}
	return count, os.Rename(tmp, target)
}

// ── Index ────────────────────────────────────────────────────────────────

// linuxPkgIndex is the compact index opened for one operation: names and
// provides in memory, the lines themselves read from the card on demand.
type linuxPkgIndex struct {
	f        *os.File
	refs     map[string]linuxPkgIndexRef
	provides map[string][]string
}

type linuxPkgIndexRef struct {
	off     int64
	version string
}

var errLinuxPkgNoIndex = errors.New("there is no package index yet; run apt update first")

// linuxPkgOpenIndex reads the index's names, keeping the newest version of
// each package across the suites.
func linuxPkgOpenIndex(lay linuxPkgLayout) (*linuxPkgIndex, error) {
	f, err := os.Open(filepath.Join(lay.index, "packages.tsv"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errLinuxPkgNoIndex
		}
		return nil, err
	}
	x := &linuxPkgIndex{f: f, refs: make(map[string]linuxPkgIndexRef, 80000), provides: map[string][]string{}}
	br := bufio.NewReaderSize(f, 256<<10)
	var off int64
	for {
		line, err := linuxPkgReadLine(br)
		if err != nil && err != io.EOF {
			f.Close()
			return nil, err
		}
		start := off
		off += int64(len(line))
		if fields := strings.SplitN(line, "\t", 13); len(fields) == 13 {
			name, version := fields[0], fields[1]
			prev, seen := x.refs[name]
			if !seen || linuxPkgVersionCompare(version, prev.version) > 0 {
				key := strings.Clone(name)
				x.refs[key] = linuxPkgIndexRef{off: start, version: strings.Clone(version)}
				// Any version's Provides is listed; the resolver checks the
				// newest one's own field before it relies on a provider.
				for _, group := range linuxPkgParseDeps(fields[11]) {
					for _, p := range group {
						if !slices.Contains(x.provides[p.Name], key) {
							x.provides[p.Name] = append(x.provides[p.Name], key)
						}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	return x, nil
}

func (x *linuxPkgIndex) Close() {
	if x != nil && x.f != nil {
		x.f.Close()
	}
}

// Get is the newest version of name, read from the card.
func (x *linuxPkgIndex) Get(name string) (linuxPkgEntry, bool) {
	ref, ok := x.refs[name]
	if !ok {
		return linuxPkgEntry{}, false
	}
	br := bufio.NewReaderSize(io.NewSectionReader(x.f, ref.off, 1<<30), 8<<10)
	line, err := linuxPkgReadLine(br)
	if err != nil && err != io.EOF {
		return linuxPkgEntry{}, false
	}
	return linuxPkgParseTSV(line)
}

// Has reports a real package of that name.
func (x *linuxPkgIndex) Has(name string) bool {
	_, ok := x.refs[name]
	return ok
}

// Providers are the packages whose Provides names name, in index order (main
// before universe).
func (x *linuxPkgIndex) Providers(name string) []string { return x.provides[name] }

// Scan walks every newest entry in index order until fn returns false.
func (x *linuxPkgIndex) Scan(ctx context.Context, fn func(linuxPkgEntry) bool) error {
	if _, err := x.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReaderSize(x.f, 256<<10)
	var off int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := linuxPkgReadLine(br)
		if err != nil && err != io.EOF {
			return err
		}
		start := off
		off += int64(len(line))
		if line != "" {
			if e, ok := linuxPkgParseTSV(line); ok && x.refs[e.Name].off == start {
				if !fn(e) {
					return nil
				}
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}
