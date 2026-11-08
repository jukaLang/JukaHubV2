package main

// ssyncbundle.go — Save sync without a network: every save in one encrypted
// .jukasaves file, carried to the other device on a card or over SFTP.
//
// Format, version 1:
//
//	"JUKASAVES1\n"              magic
//	uint32 big-endian length    of the header that follows
//	header JSON                 key function, salt, rounds, cipher, chunk size,
//	                            nonce prefix, who made it and when
//	chunks                      uint32 big-endian length + one sealed chunk
//
// The key is PBKDF2-HMAC-SHA256 of the passphrase (600,000 rounds, 16-byte
// random salt). Each chunk seals at most 64 KiB with AES-256-GCM; its nonce is
// a random 4-byte prefix and the chunk's counter, and its additional data is
// the whole header plus a "last chunk" flag. A changed header, a reordered,
// dropped, flipped or appended chunk and a file cut short all fail; a wrong
// passphrase fails on the first chunk, before anything is written.
//
// Inside is a tar stream: manifest.json (an ssyncManifest) first, then every
// save as files/<folder key>/<path>. Import reads the manifest for its
// preview, then stages the files the merge needs, checks the whole file and
// every SHA-256, and only then merges them with the same rules as a sync
// between two devices (newest wins, a true conflict keeps both copies).
//
// argon2id would be the better passphrase function, but it is not vendored
// and no new module may be added. The header names the function, so a later
// version can switch without breaking files made by this one.

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ssyncBundleExt     = ".jukasaves"
	ssyncBundleMagic   = "JUKASAVES1\n"
	ssyncBundleChunk   = 64 << 10
	ssyncBundleMinIter = 100000
	ssyncBundleMaxIter = 5000000
	ssyncBundleMaxHdr  = 4096
	ssyncMinPassphrase = 8
)

// ssyncBundleIterations is the PBKDF2 work for a new file. A test seam: the
// tests use the minimum so they stay fast.
var ssyncBundleIterations = 600000

var (
	errSsyncAuth      = errors.New("wrong passphrase, or the file was changed or damaged")
	errSsyncTruncated = errors.New("the file is cut short — copy it again")
	errSsyncNotBundle = errors.New("this is not a JukaHub saves file")
)

// ssyncBundleHeader is the readable part of a saves file. Nothing in it is
// secret; all of it is authenticated by every chunk.
type ssyncBundleHeader struct {
	V       int    `json:"v"`
	KDF     string `json:"kdf"`
	Iter    int    `json:"iter"`
	Salt    string `json:"salt"`
	Cipher  string `json:"cipher"`
	Chunk   int    `json:"chunk"`
	Nonce   string `json:"nonce"`
	Created string `json:"created"`
	Device  string `json:"device,omitempty"`
	Name    string `json:"name"`
}

// ssyncBundleHeaderBytes is the header as stored: magic, length and JSON. The
// same bytes are every chunk's additional data.
func ssyncBundleHeaderBytes(h ssyncBundleHeader) ([]byte, error) {
	js, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if len(js) > ssyncBundleMaxHdr {
		return nil, errors.New("the header is too large")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(js)))
	out := make([]byte, 0, len(ssyncBundleMagic)+4+len(js))
	out = append(out, ssyncBundleMagic...)
	out = append(out, size[:]...)
	out = append(out, js...)
	return out, nil
}

// ssyncReadBundleHeader reads and checks a header; raw is the bytes as stored.
func ssyncReadBundleHeader(r io.Reader) (ssyncBundleHeader, []byte, error) {
	magic := make([]byte, len(ssyncBundleMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != ssyncBundleMagic {
		return ssyncBundleHeader{}, nil, errSsyncNotBundle
	}
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return ssyncBundleHeader{}, nil, errSsyncTruncated
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > ssyncBundleMaxHdr {
		return ssyncBundleHeader{}, nil, errSsyncNotBundle
	}
	js := make([]byte, n)
	if _, err := io.ReadFull(r, js); err != nil {
		return ssyncBundleHeader{}, nil, errSsyncTruncated
	}
	var h ssyncBundleHeader
	if err := json.Unmarshal(js, &h); err != nil {
		return ssyncBundleHeader{}, nil, errSsyncNotBundle
	}
	if h.V != 1 || h.KDF != "pbkdf2-sha256" || h.Cipher != "aes-256-gcm" {
		return ssyncBundleHeader{}, nil, errors.New("this saves file needs a newer JukaHub")
	}
	if h.Iter < ssyncBundleMinIter || h.Iter > ssyncBundleMaxIter || h.Chunk < 1024 || h.Chunk > 1<<20 {
		return ssyncBundleHeader{}, nil, errSsyncNotBundle
	}
	raw := make([]byte, 0, len(magic)+4+len(js))
	raw = append(raw, magic...)
	raw = append(raw, size[:]...)
	raw = append(raw, js...)
	return h, raw, nil
}

// ssyncBundleParams decodes the salt and the nonce prefix.
func ssyncBundleParams(h ssyncBundleHeader) ([]byte, [4]byte, error) {
	var prefix [4]byte
	salt, err := base64.StdEncoding.DecodeString(h.Salt)
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return nil, prefix, errSsyncNotBundle
	}
	p, err := base64.StdEncoding.DecodeString(h.Nonce)
	if err != nil || len(p) != len(prefix) {
		return nil, prefix, errSsyncNotBundle
	}
	copy(prefix[:], p)
	return salt, prefix, nil
}

// ssyncBundleKey turns the passphrase into the file key.
func ssyncBundleKey(passphrase string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, passphrase, salt, iter, 32)
}

// ssyncBundleAEAD is AES-256-GCM over the key.
func ssyncBundleAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ssyncChunkAD is a chunk's additional data: the header and the last flag.
func ssyncChunkAD(header []byte, final bool) []byte {
	ad := make([]byte, len(header)+1)
	copy(ad, header)
	if final {
		ad[len(header)] = 1
	}
	return ad
}

// ssyncChunkNonce is the nonce prefix and the chunk counter.
func ssyncChunkNonce(prefix [4]byte, n uint64) [12]byte {
	var nonce [12]byte
	copy(nonce[:4], prefix[:])
	binary.BigEndian.PutUint64(nonce[4:], n)
	return nonce
}

// ssyncSealWriter encrypts a stream in chunks. The last chunk is sealed on
// Close, so a stream that was not closed cannot pass for a whole one.
type ssyncSealWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	prefix  [4]byte
	adMore  []byte
	adFinal []byte
	buf     []byte
	out     []byte
	n       uint64
	closed  bool
}

func newSsyncSealWriter(w io.Writer, aead cipher.AEAD, prefix [4]byte, header []byte, chunk int) *ssyncSealWriter {
	return &ssyncSealWriter{
		w:       w,
		aead:    aead,
		prefix:  prefix,
		adMore:  ssyncChunkAD(header, false),
		adFinal: ssyncChunkAD(header, true),
		buf:     make([]byte, 0, chunk),
	}
}

func (s *ssyncSealWriter) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("write after close")
	}
	written := 0
	for len(p) > 0 {
		if len(s.buf) == cap(s.buf) {
			if err := s.flush(false); err != nil {
				return written, err
			}
		}
		take := cap(s.buf) - len(s.buf)
		if take > len(p) {
			take = len(p)
		}
		s.buf = append(s.buf, p[:take]...)
		p = p[take:]
		written += take
	}
	return written, nil
}

func (s *ssyncSealWriter) flush(final bool) error {
	ad := s.adMore
	if final {
		ad = s.adFinal
	}
	nonce := ssyncChunkNonce(s.prefix, s.n)
	s.out = s.aead.Seal(s.out[:0], nonce[:], s.buf, ad)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(s.out)))
	if _, err := s.w.Write(size[:]); err != nil {
		return err
	}
	if _, err := s.w.Write(s.out); err != nil {
		return err
	}
	s.n++
	s.buf = s.buf[:0]
	return nil
}

// Close seals what is left as the last chunk (possibly empty).
func (s *ssyncSealWriter) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.flush(true)
}

// ssyncOpenReader decrypts a chunked stream. It reports io.EOF only after the
// last chunk authenticated; a stream that ends before it is errSsyncTruncated.
type ssyncOpenReader struct {
	r       io.Reader
	aead    cipher.AEAD
	prefix  [4]byte
	adMore  []byte
	adFinal []byte
	chunk   int
	n       uint64
	sealed  []byte
	plain   []byte
	pos     int
	done    bool
}

func newSsyncOpenReader(r io.Reader, aead cipher.AEAD, prefix [4]byte, header []byte, chunk int) *ssyncOpenReader {
	return &ssyncOpenReader{
		r:       r,
		aead:    aead,
		prefix:  prefix,
		adMore:  ssyncChunkAD(header, false),
		adFinal: ssyncChunkAD(header, true),
		chunk:   chunk,
	}
}

func (o *ssyncOpenReader) Read(p []byte) (int, error) {
	for o.pos >= len(o.plain) {
		if o.done {
			return 0, io.EOF
		}
		if err := o.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, o.plain[o.pos:])
	o.pos += n
	return n, nil
}

func (o *ssyncOpenReader) next() error {
	var size [4]byte
	if _, err := io.ReadFull(o.r, size[:]); err != nil {
		return errSsyncTruncated
	}
	n := int(binary.BigEndian.Uint32(size[:]))
	if n < o.aead.Overhead() || n > o.chunk+o.aead.Overhead() {
		return errSsyncAuth
	}
	if cap(o.sealed) < n {
		o.sealed = make([]byte, n)
	}
	o.sealed = o.sealed[:n]
	if _, err := io.ReadFull(o.r, o.sealed); err != nil {
		return errSsyncTruncated
	}
	nonce := ssyncChunkNonce(o.prefix, o.n)
	// A chunk opens as a middle chunk, or else as the last one; anything else
	// was changed, reordered or sealed under another key.
	plain, err := o.aead.Open(o.plain[:0], nonce[:], o.sealed, o.adMore)
	if err != nil {
		plain, err = o.aead.Open(o.plain[:0], nonce[:], o.sealed, o.adFinal)
		if err != nil {
			return errSsyncAuth
		}
		o.done = true
		// Nothing may follow the last chunk.
		var extra [1]byte
		if k, _ := io.ReadFull(o.r, extra[:]); k > 0 {
			return errSsyncAuth
		}
	}
	o.plain = plain
	o.pos = 0
	o.n++
	return nil
}

// ── Export ────────────────────────────────────────────────────────────────

// ssyncExportDirOverride is a test seam.
var ssyncExportDirOverride string

// ssyncExportDir is where exports are written and imports are looked for
// first: "JukaHub Saves" on the card (in Documents on Windows), easy to find
// with a card reader or over SFTP.
func ssyncExportDir() string {
	if ssyncExportDirOverride != "" {
		return ssyncExportDirOverride
	}
	if IsWindows() {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, "Documents", "JukaHub Saves")
		}
	}
	if root := saveManagerSDRoot(); root != "" {
		return filepath.Join(root, "JukaHub Saves")
	}
	return filepath.Join(ssyncDataDir(), "exports")
}

// ssyncBundleFileName is "<device> saves 2026-11-08 1530.jukasaves".
func ssyncBundleFileName(device string, at time.Time) string {
	return ssyncSafeDeviceName(device) + " saves " + at.Format("2006-01-02 1504") + ssyncBundleExt
}

// ssyncUniquePath adds " (2)", " (3)"… until the name is free.
func ssyncUniquePath(p string) string {
	if !fileExists(p) {
		return p
	}
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for n := 2; n < 1000; n++ {
		candidate := stem + " (" + strconv.Itoa(n) + ")" + ext
		if !fileExists(candidate) {
			return candidate
		}
	}
	return stem + " " + strconv.FormatInt(time.Now().UnixNano(), 10) + ext
}

// ssyncExport writes every save under roots into one encrypted file in dir.
// It returns the file and how many saves it holds; progress (may be nil)
// hears after each save.
func ssyncExport(ctx context.Context, roots []saveRoot, dir, passphrase string, progress func(done, total int)) (string, int, error) {
	if len([]rune(passphrase)) < ssyncMinPassphrase {
		return "", 0, fmt.Errorf("the passphrase needs at least %d characters", ssyncMinPassphrase)
	}
	if len(roots) == 0 {
		return "", 0, errors.New("no save folders found yet — start a game once")
	}
	ssyncBuildMu.Lock()
	man, err := ssyncBuildManifest(ctx, roots)
	ssyncBuildMu.Unlock()
	if err != nil {
		return "", 0, err
	}
	if len(man.Files) == 0 {
		return "", 0, errSaveManagerEmpty
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	salt := make([]byte, 16)
	var prefix [4]byte
	if _, err := rand.Read(salt); err != nil {
		return "", 0, err
	}
	if _, err := rand.Read(prefix[:]); err != nil {
		return "", 0, err
	}
	now := time.Now()
	hdr := ssyncBundleHeader{
		V:       1,
		KDF:     "pbkdf2-sha256",
		Iter:    ssyncBundleIterations,
		Salt:    base64.StdEncoding.EncodeToString(salt),
		Cipher:  "aes-256-gcm",
		Chunk:   ssyncBundleChunk,
		Nonce:   base64.StdEncoding.EncodeToString(prefix[:]),
		Created: now.UTC().Format(time.RFC3339),
		Device:  man.Device,
		Name:    man.Name,
	}
	header, err := ssyncBundleHeaderBytes(hdr)
	if err != nil {
		return "", 0, err
	}
	key, err := ssyncBundleKey(passphrase, salt, hdr.Iter)
	if err != nil {
		return "", 0, err
	}
	aead, err := ssyncBundleAEAD(key)
	if err != nil {
		return "", 0, err
	}

	tmp, err := os.CreateTemp(dir, ".jukasaves-*.tmp")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	finished := false
	defer func() {
		if !finished {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	bw := bufio.NewWriterSize(tmp, 256<<10)
	if _, err := bw.Write(header); err != nil {
		return "", 0, err
	}
	sw := newSsyncSealWriter(bw, aead, prefix, header, hdr.Chunk)
	tw := tar.NewWriter(sw)
	manJSON, err := json.Marshal(man)
	if err != nil {
		return "", 0, err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     "manifest.json",
		Mode:     0o644,
		Size:     int64(len(manJSON)),
		ModTime:  now,
		Typeflag: tar.TypeReg,
	}); err != nil {
		return "", 0, err
	}
	if _, err := tw.Write(manJSON); err != nil {
		return "", 0, err
	}
	rootPaths := ssyncRootMap(roots)
	for i, f := range man.Files {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		src, err := ssyncLocalPath(rootPaths[f.Root], f.Path)
		if err != nil {
			return "", 0, err
		}
		if err := ssyncTarFile(tw, src, f); err != nil {
			return "", 0, fmt.Errorf("%s: %w", f.Path, err)
		}
		if progress != nil {
			progress(i+1, len(man.Files))
		}
	}
	if err := tw.Close(); err != nil {
		return "", 0, err
	}
	if err := sw.Close(); err != nil {
		return "", 0, err
	}
	if err := bw.Flush(); err != nil {
		return "", 0, err
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	final := ssyncUniquePath(filepath.Join(dir, ssyncBundleFileName(man.Name, now)))
	if err := os.Rename(tmpName, final); err != nil {
		return "", 0, err
	}
	finished = true
	return final, len(man.Files), nil
}

// ssyncTarFile adds one save, checking it is still the file the manifest
// describes: a save that changed mid-export would make the file disagree with
// its own list.
func ssyncTarFile(tw *tar.Writer, src string, f ssyncFile) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if info.Size() != f.Size {
		return errSsyncChanged
	}
	hdr := &tar.Header{
		Name:     "files/" + f.Root + "/" + f.Path,
		Mode:     0o644,
		Size:     f.Size,
		ModTime:  time.Unix(f.MTime, 0),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tw, h), io.LimitReader(in, f.Size))
	if err != nil {
		return err
	}
	if n != f.Size || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), f.SHA) {
		return errSsyncChanged
	}
	return nil
}

// ── Import ────────────────────────────────────────────────────────────────

// ssyncBundleInfo is a saves file found on this device.
type ssyncBundleInfo struct {
	Path    string
	Name    string
	Created time.Time
	Size    int64
	Mod     time.Time
}

// ssyncFindBundles lists saves files where people put them: the export
// folder, the card's top folder and Downloads. Newest first, at most fifty.
func ssyncFindBundles() []ssyncBundleInfo {
	dirs := []string{ssyncExportDir()}
	if root := saveManagerSDRoot(); root != "" {
		dirs = append(dirs, root)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, "Downloads"))
	}
	seen := map[string]bool{}
	var out []ssyncBundleInfo
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ssyncBundleExt) || len(out) >= 50 {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			info, err := e.Info()
			if err != nil {
				continue
			}
			b := ssyncBundleInfo{
				Path: p,
				Name: strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())),
				Size: info.Size(),
				Mod:  info.ModTime(),
			}
			if h, err := ssyncPeekBundle(p); err == nil {
				if name := peerCleanName(h.Name); name != "" {
					b.Name = name
				}
				if t, err := time.Parse(time.RFC3339, h.Created); err == nil {
					b.Created = t
				}
			}
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
	return out
}

// ssyncPeekBundle reads a file's header without the passphrase.
func ssyncPeekBundle(p string) (ssyncBundleHeader, error) {
	f, err := os.Open(p)
	if err != nil {
		return ssyncBundleHeader{}, err
	}
	defer f.Close()
	h, _, err := ssyncReadBundleHeader(bufio.NewReader(f))
	return h, err
}

// ssyncOpenBundle opens a saves file with a passphrase. The reader yields the
// decrypted tar stream; the caller closes the file.
func ssyncOpenBundle(p, passphrase string) (*os.File, ssyncBundleHeader, *ssyncOpenReader, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, ssyncBundleHeader{}, nil, err
	}
	br := bufio.NewReaderSize(f, 256<<10)
	hdr, header, err := ssyncReadBundleHeader(br)
	if err != nil {
		f.Close()
		return nil, ssyncBundleHeader{}, nil, err
	}
	salt, prefix, err := ssyncBundleParams(hdr)
	if err != nil {
		f.Close()
		return nil, ssyncBundleHeader{}, nil, err
	}
	key, err := ssyncBundleKey(passphrase, salt, hdr.Iter)
	if err != nil {
		f.Close()
		return nil, ssyncBundleHeader{}, nil, err
	}
	aead, err := ssyncBundleAEAD(key)
	if err != nil {
		f.Close()
		return nil, ssyncBundleHeader{}, nil, err
	}
	return f, hdr, newSsyncOpenReader(br, aead, prefix, header, hdr.Chunk), nil
}

// ssyncBundleErr keeps the errors that say what went wrong and calls anything
// else a damaged file.
func ssyncBundleErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errSsyncAuth), errors.Is(err, errSsyncTruncated), errors.Is(err, errSsyncChecksum),
		errors.Is(err, context.Canceled):
		return err
	}
	return fmt.Errorf("the file is damaged: %w", err)
}

// ssyncReadBundleManifest reads the first entry, the manifest.
func ssyncReadBundleManifest(tr *tar.Reader) (ssyncManifest, error) {
	h, err := tr.Next()
	if err != nil {
		return ssyncManifest{}, ssyncBundleErr(err)
	}
	if h.Name != "manifest.json" || h.Size <= 0 || h.Size > ssyncManifestLimit {
		return ssyncManifest{}, errSsyncNotBundle
	}
	var man ssyncManifest
	if err := json.NewDecoder(io.LimitReader(tr, h.Size)).Decode(&man); err != nil {
		return ssyncManifest{}, ssyncBundleErr(err)
	}
	if man.V != 1 || len(man.Files) > ssyncMaxFiles {
		return ssyncManifest{}, errSsyncNotBundle
	}
	return man, nil
}

// ssyncImportPreview is a saves file read up to its manifest and compared with
// the saves on this device.
type ssyncImportPreview struct {
	Path   string
	Header ssyncBundleHeader
	From   string
	Bundle ssyncManifest
	Plan   ssyncPlan
	// Kept counts saves in the file that leave this device's copy as it is:
	// older ones, and conflicts whose copy an earlier import already kept.
	Kept int
}

// ssyncImportPreviewFile reads a saves file's manifest with the passphrase and
// plans merging it into roots. A wrong passphrase fails here, before anything
// is written.
func ssyncImportPreviewFile(ctx context.Context, p, passphrase string, roots []saveRoot) (ssyncImportPreview, error) {
	f, hdr, or, err := ssyncOpenBundle(p, passphrase)
	if err != nil {
		return ssyncImportPreview{}, err
	}
	defer f.Close()
	man, err := ssyncReadBundleManifest(tar.NewReader(or))
	if err != nil {
		return ssyncImportPreview{}, err
	}
	ssyncBuildMu.Lock()
	local, err := ssyncBuildManifest(ctx, roots)
	ssyncBuildMu.Unlock()
	if err != nil {
		return ssyncImportPreview{}, err
	}
	from := peerCleanName(man.Name)
	if from == "" {
		from = peerCleanName(hdr.Name)
	}
	if from == "" {
		from = "the other device"
	}
	base := map[string]string{}
	if peerValidID(man.Device) && !peerSameID(man.Device, local.Device) {
		base = ssyncLoadBase(man.Device)
	}
	plan := ssyncBuildPlan(local, man, base, 0, local.Name, from)
	// An import only changes this device: what a sync would send stays as is.
	// A conflict whose copy from the file is already kept here (the same file
	// imported again) is settled as well.
	kept := 0
	plan = ssyncFilterPlan(plan, func(it ssyncItem) bool {
		switch {
		case it.Action == ssyncPush:
			if it.HasRemote {
				kept++
			}
			return false
		case it.Action == ssyncConflict && it.LocalWins && ssyncHasKeptCopy(local.Files, it.Root, it.Path, it.Remote.SHA):
			kept++
			return false
		}
		return true
	})
	return ssyncImportPreview{Path: p, Header: hdr, From: from, Bundle: man, Plan: plan, Kept: kept}, nil
}

// ssyncStagedIO is an import's "other device": the saves staged from the file.
// Nothing is sent anywhere.
type ssyncStagedIO struct {
	dir string
}

func (s ssyncStagedIO) prepare(context.Context, []string) error { return nil }

func (s ssyncStagedIO) pull(_ context.Context, f ssyncFile, dst string) error {
	return ssyncCopyLocal(filepath.Join(s.dir, f.Root, filepath.FromSlash(f.Path)), dst, f)
}

func (s ssyncStagedIO) push(context.Context, ssyncFile, string) error { return nil }

// ssyncImportApply stages the saves the merge needs, checks the whole file
// (its last chunk included) and every SHA-256, and only then merges them into
// roots with a safety snapshot of whatever it overwrites.
func ssyncImportApply(ctx context.Context, prev ssyncImportPreview, passphrase string, roots []saveRoot, progress func(done, total int, current string)) (ssyncResult, error) {
	need := map[string]ssyncFile{}
	for _, it := range prev.Plan.Items {
		if it.Action == ssyncPull || it.Action == ssyncConflict {
			need[it.Remote.key()] = it.Remote
		}
	}
	if len(need) == 0 {
		return ssyncResult{Peer: prev.From, At: time.Now()}, nil
	}
	if err := os.MkdirAll(ssyncDataDir(), 0o755); err != nil {
		return ssyncResult{}, err
	}
	stage, err := os.MkdirTemp(ssyncDataDir(), "import-")
	if err != nil {
		return ssyncResult{}, err
	}
	defer os.RemoveAll(stage)

	f, _, or, err := ssyncOpenBundle(prev.Path, passphrase)
	if err != nil {
		return ssyncResult{}, err
	}
	defer f.Close()
	tr := tar.NewReader(or)
	if _, err := ssyncReadBundleManifest(tr); err != nil {
		return ssyncResult{}, err
	}
	staged := 0
	for {
		if err := ctx.Err(); err != nil {
			return ssyncResult{}, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ssyncResult{}, ssyncBundleErr(err)
		}
		key := strings.TrimPrefix(h.Name, "files/")
		want, ok := need[key]
		if !ok || key == h.Name {
			continue
		}
		i := strings.IndexByte(key, '/')
		if i <= 0 {
			continue
		}
		dst, err := ssyncLocalPath(filepath.Join(stage, key[:i]), key[i+1:])
		if err != nil {
			return ssyncResult{}, err
		}
		if err := ssyncWriteFile(dst, tr, want.Size, want.SHA, want.MTime); err != nil {
			return ssyncResult{}, ssyncBundleErr(err)
		}
		delete(need, key)
		staged++
		if progress != nil {
			progress(staged, staged+len(need), path.Base(key))
		}
	}
	// The whole file must authenticate, its last chunk included, before a
	// single save is merged.
	if _, err := io.Copy(io.Discard, or); err != nil {
		return ssyncResult{}, ssyncBundleErr(err)
	}
	if len(need) > 0 {
		return ssyncResult{}, errors.New("the file is missing saves its own list names")
	}
	res, _ := ssyncExecute(ctx, prev.Plan, ssyncStagedIO{dir: stage}, roots, prev.From, "before import from "+prev.From, progress)
	return res, nil
}
