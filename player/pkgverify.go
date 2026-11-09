package main

// pkgverify.go — what makes a package download trustworthy enough to unpack.
//
// The registry (packages.json in the jukaLang/Packages repository) is an
// unsigned list of apps and links. An entry may now carry two optional fields:
//
//   - "sha256": the SHA-256 of the file "download" points at, 64 hex digits (a
//     "sha256:" prefix is accepted). A download whose hash differs is refused
//     before anything is unpacked. A value that is present but is not a SHA-256
//     refuses the install outright: a malformed hash must never read as "no
//     hash".
//   - "size": when the entry has a hash and its size is a plain byte count (a
//     JSON integer or a string of digits), the download must be exactly that
//     long, and the transfer stops as soon as it is longer. A human size ("1.2
//     MB") stays what it always was: the store's display value.
//
// An entry without a hash still installs — refusing it would break every
// package until the registry publishes hashes — but the store labels it "Not
// verified" and the log says so on every install. The registry has no
// signature convention to check: patch_signed.go signs the Patch repository's
// own index, a different format, and its only embedded key is the development
// key. Signing the registry is a follow-up in MODERNIZATION.md, not something
// this file pretends to do.
//
// The download streams to a file next to its destination (the staging area
// inside the packages folder, pkgstage.go) and is hashed as it arrives: no
// part of it is held in memory, and it is bounded by the published size or by
// pkgDownloadMaxBytes.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// pkgDownloadMaxBytes bounds a download whose exact size the registry
	// does not publish. It matches pkgMaxExpandBytes: an archive larger than
	// that could not be unpacked anyway.
	pkgDownloadMaxBytes = int64(1 << 30)
	// pkgDownloadTimeout bounds one package download. The strip's X on the
	// package's chip cancels it sooner.
	pkgDownloadTimeout = 20 * time.Minute
	// pkgDownloadSpaceStep is how often a download whose size nobody states
	// checks that the card still has room.
	pkgDownloadSpaceStep = int64(32 << 20)
)

// errPkgChecksum and errPkgSize mark a download that does not match what the
// registry published, so the install can say so in plain words.
var (
	errPkgChecksum = errors.New("the download does not match the catalogue's SHA-256")
	errPkgSize     = errors.New("the download is not the size the catalogue publishes")
)

// pkgApplyIntegrity fills p's SHA256, DownloadSize and HashProblem from one
// raw registry entry.
func pkgApplyIntegrity(p *Package, item json.RawMessage) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(item, &fields) != nil {
		return
	}
	sum, size, problem := pkgParseIntegrity(fields)
	p.SHA256, p.HashProblem = sum, problem
	if sum != "" {
		p.DownloadSize = size
	}
}

// pkgParseIntegrity reads the optional "sha256" and "size" fields of one
// registry entry: sum is lowercase hex ("" when absent or empty), size the
// exact byte count when "size" is a plain integer (0 otherwise), and problem
// is non-empty when a "sha256" is present but unusable.
func pkgParseIntegrity(fields map[string]json.RawMessage) (sum string, size int64, problem string) {
	if raw, ok := fields["sha256"]; ok && strings.TrimSpace(string(raw)) != "null" {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			problem = "the catalogue's checksum is not text"
		} else {
			s = strings.ToLower(strings.TrimSpace(s))
			s = strings.TrimSpace(strings.TrimPrefix(s, "sha256:"))
			switch {
			case s == "":
				// An empty value means the same as no value.
			case len(s) != sha256.Size*2 || !isHex(s):
				problem = "the catalogue's checksum is not a SHA-256"
			default:
				sum = s
			}
		}
	}
	if raw, ok := fields["size"]; ok {
		size = pkgExactSize(raw)
	}
	return sum, size, problem
}

// pkgExactSize is a byte count given as a JSON integer or as a string of
// digits; anything else — a human size, a fraction, an exponent, a negative —
// is 0 (no exact size).
func pkgExactSize(raw json.RawMessage) int64 {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s = strings.TrimSpace(s)
	} else {
		s = strings.TrimSpace(string(raw))
	}
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// pkgDownloadResult is what a finished download proved about the file.
type pkgDownloadResult struct {
	SHA256   string
	Size     int64
	Verified bool
	// Zip reports that the file starts with a zip signature.
	Zip bool
}

// pkgHTTPClient follows redirects (a GitHub release download is one) but never
// from HTTPS down to plain HTTP, where the file could be swapped on the way.
var pkgHTTPClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("refusing a redirect from HTTPS to plain HTTP")
		}
		return nil
	},
}

// pkgDownloadTo streams rawURL into dest (created or truncated), hashing as it
// goes, and checks the result against what the registry published for p. It
// refuses an error page (a 404 used to be saved and "installed" as the
// package). A failed or refused download removes dest. progress may be nil.
func pkgDownloadTo(ctx context.Context, rawURL, dest string, p Package, progress func(done, total int64)) (pkgDownloadResult, error) {
	var res pkgDownloadResult
	if ctx == nil {
		ctx = context.Background()
	}
	if p.DownloadSize > pkgDownloadMaxBytes {
		return res, fmt.Errorf("the package is larger than %d MiB", pkgDownloadMaxBytes>>20)
	}
	limit := pkgDownloadMaxBytes
	if p.DownloadSize > 0 {
		limit = p.DownloadSize
	}
	tooBig := func() error {
		if p.DownloadSize > 0 {
			return fmt.Errorf("%w (more than %d bytes)", errPkgSize, p.DownloadSize)
		}
		return fmt.Errorf("the package is larger than %d MiB", limit>>20)
	}
	ctx, cancel := context.WithTimeout(ctx, pkgDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return res, err
	}
	req.Header.Set("User-Agent", "JukaHub/1.0")
	resp, err := pkgHTTPClient.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return res, fmt.Errorf("the server answered %s", resp.Status)
	}
	total := resp.ContentLength
	if total > limit {
		return res, tooBig()
	}
	if p.DownloadSize > 0 && total >= 0 && total != p.DownloadSize {
		return res, fmt.Errorf("%w (the server sends %d bytes, the catalogue says %d)", errPkgSize, total, p.DownloadSize)
	}
	// The card must hold the download with room to spare before a byte is
	// written: a package can be a gigabyte, and saves live on the same card.
	// A size nobody states is checked as it arrives (pkgDownloadSpaceStep).
	expect := total
	if p.DownloadSize > expect {
		expect = p.DownloadSize
	}
	if expect < 0 {
		expect = 0
	}
	if err := pkgCheckFreeSpace(filepath.Dir(dest), expect); err != nil {
		return res, err
	}
	if total < 0 && p.DownloadSize > 0 {
		// The progress bar can show a fraction when the catalogue says how big
		// the download is, even if the server does not.
		total = p.DownloadSize
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return res, err
	}
	kept := false
	defer func() {
		if !kept {
			_ = os.Remove(dest)
		}
	}()
	h := sha256.New()
	var head [4]byte
	headN := 0
	buf := make([]byte, 64<<10)
	var done int64
	var copyErr error
	// With no stated size, the card's room is checked again every
	// pkgDownloadSpaceStep bytes instead of once up front.
	spaceCheckAt := int64(-1)
	if expect == 0 {
		spaceCheckAt = pkgDownloadSpaceStep
	}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if done+int64(n) > limit {
				copyErr = tooBig()
				break
			}
			if spaceCheckAt >= 0 && done+int64(n) >= spaceCheckAt {
				if serr := pkgCheckFreeSpace(filepath.Dir(dest), 0); serr != nil {
					copyErr = serr
					break
				}
				spaceCheckAt += pkgDownloadSpaceStep
			}
			chunk := buf[:n]
			if headN < len(head) {
				headN += copy(head[headN:], chunk)
			}
			_, _ = h.Write(chunk)
			if _, werr := out.Write(chunk); werr != nil {
				copyErr = werr
				break
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			copyErr = rerr
			break
		}
	}
	syncErr := out.Sync()
	closeErr := out.Close()
	for _, e := range []error{copyErr, syncErr, closeErr} {
		if e != nil {
			return res, e
		}
	}
	res.Size = done
	res.SHA256 = hex.EncodeToString(h.Sum(nil))
	res.Zip = headN == len(head) && pkgZipSignature(head)
	if p.DownloadSize > 0 && done != p.DownloadSize {
		return res, fmt.Errorf("%w (%d bytes, the catalogue says %d)", errPkgSize, done, p.DownloadSize)
	}
	if p.SHA256 != "" {
		if res.SHA256 != p.SHA256 {
			return res, fmt.Errorf("%w (the download's is %s…)", errPkgChecksum, res.SHA256[:12])
		}
		res.Verified = true
	}
	kept = true
	return res, nil
}

// pkgZipSignature reports whether a file starts like a zip archive: a local
// file header (PK\x03\x04) or the end record of an empty one (PK\x05\x06).
// "PK" alone also starts plenty of files that are not archives.
func pkgZipSignature(head [4]byte) bool {
	if head[0] != 'P' || head[1] != 'K' {
		return false
	}
	return (head[2] == 3 && head[3] == 4) || (head[2] == 5 && head[3] == 6)
}

// pkgCheckFreeSpace refuses when the volume holding dir has less than need
// bytes free on top of pkgFreeSpaceMargin. A volume that cannot say how much
// is free is not refused.
func pkgCheckFreeSpace(dir string, need int64) error {
	free, err := filesFreeSpace(dir)
	if err != nil {
		return nil
	}
	if free >= need+pkgFreeSpaceMargin {
		return nil
	}
	if need == 0 {
		return fmt.Errorf("not enough free space: the card is nearly full (%s free)", formatInstallBytes(free))
	}
	return fmt.Errorf("not enough free space: the download needs %s and %s is free", formatInstallBytes(need+pkgFreeSpaceMargin), formatInstallBytes(free))
}
