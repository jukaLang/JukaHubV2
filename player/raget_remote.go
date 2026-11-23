package main

// raget_remote.go — reading part of a zip on a web server, for raget.go.
//
// TrimUI publishes the RetroArch it builds for each handheld inside that
// model's SD base package on GitHub: a zip of the whole card, hundreds of
// megabytes, of which RetroArch's folder is one part and its binary a few tens
// of megabytes. Downloading the whole package to pick one folder out of it
// would need the card's free space twice over and minutes of Wi-Fi for files
// that are thrown away. A zip keeps its directory at the end and names the
// offset of every file in it, and GitHub's release downloads answer HTTP range
// requests, so only what is used is read:
//
//   - raGetRemote is an io.ReaderAt over the asset's URL. Reads are served
//     from raGetBlock-sized blocks fetched with "Range: bytes=a-b" and kept in
//     a small LRU (raGetBlocksKept): archive/zip's many small reads of headers
//     become one request per block, and a file read from start to end is
//     downloaded once. GitHub answers the release URL with a redirect to a
//     signed URL on its asset host, valid for minutes; that URL is reused for
//     raGetDirectTTL, then asked again (and at once when it is refused). Every
//     hop is held to the download allowlist by the client's CheckRedirect and
//     the URL itself by emulatorUpdateValidateURL. A server that ignores the
//     range (200 instead of 206), a Content-Range other than the one asked
//     for, or more bytes than the reader's budget is an error — never a silent
//     download of the whole package.
//   - raGetOpen opens one entry with its size and CRC-32 checked when the
//     reader reaches the end: Store and Deflate through archive/zip itself, and
//     the other methods 7-Zip writes into zips — BZip2 (compress/bzip2), LZMA
//     (the vendored ulikunitz/xz/lzma that sevenzip.go uses) and Zstandard (the
//     vendored klauspost/compress/zstd) — from the raw stream. A guide for the
//     Brick Pro's package says only 7-Zip unpacks it, which an LZMA or BZip2
//     zip would explain. Anything else (Deflate64, PPMd, encryption) is
//     refused with the method's number.

import (
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz/lzma"
)

// raGetBlock is one range request: big enough that a folder of small files
// costs few requests, small enough that the blocks kept stay a few megabytes
// of the handheld's memory. raGetDirBlock is one while a zip's directory is
// read: its last kilobytes and the directory itself, a few megabytes for a
// whole card. Variables, so a test can read a package in small blocks.
var (
	raGetBlock    int64 = 4 << 20
	raGetDirBlock int64 = 256 << 10
)

const (
	// raGetBlocksKept is how many blocks the LRU holds.
	raGetBlocksKept = 4
	// raGetDirectTTL is how long the signed URL a redirect led to is reused.
	raGetDirectTTL = 3 * time.Minute
	// raGetFetchTries bounds the attempts at one block.
	raGetFetchTries = 3
	// The zip methods raGetOpen reads besides Store and Deflate.
	raGetMethodBzip2 = 12
	raGetMethodLZMA  = 14
	raGetMethodZstd  = 93
	// raGetLZMADictMax bounds an LZMA entry's dictionary.
	raGetLZMADictMax = 128 << 20
	// raGetZstdMemMax bounds a Zstandard entry's window.
	raGetZstdMemMax = 256 << 20
)

// errRaGetNoRange is a server that sent the whole file for a range request.
var errRaGetNoRange = errors.New("the server does not answer partial downloads")

// raGetRemote is an io.ReaderAt over a file on a web server (see the top of
// the file). One goroutine uses it at a time; the lock only keeps its state
// whole.
type raGetRemote struct {
	ctx    context.Context
	client *http.Client
	url    string
	size   int64

	mu sync.Mutex
	// direct is the URL the last redirect led to, reused until directAt is
	// raGetDirectTTL old.
	direct   string
	directAt time.Time
	// blockSize is one request's length (raGetBlock unless setBlockSize
	// changed it: a zip's directory is read in smaller ones).
	blockSize int64
	blocks    map[int64][]byte
	lru       []int64 // block numbers, the most recently used last
	// fetched counts the bytes transferred; budget bounds them (0: no bound).
	fetched int64
	budget  int64
	// onFetch, when set, hears fetched after every block.
	onFetch func(fetched int64)
}

// newRaGetRemote is a reader of the size-byte file at raw, which may transfer
// at most budget bytes until setBudget says otherwise.
func newRaGetRemote(ctx context.Context, client *http.Client, raw string, size, budget int64) *raGetRemote {
	if client == nil {
		client = newEmulatorUpdateClient()
	}
	return &raGetRemote{ctx: ctx, client: client, url: raw, size: size, budget: budget, blockSize: raGetBlock, blocks: map[int64][]byte{}}
}

// setBlockSize changes the length of the requests from now on (the blocks
// kept are dropped: their numbering was the old size's).
func (r *raGetRemote) setBlockSize(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n == r.blockSize {
		return
	}
	r.blockSize = n
	r.blocks = map[int64][]byte{}
	r.lru = nil
}

// setBudget changes the most bytes the reader may have transferred in all.
func (r *raGetRemote) setBudget(budget int64) {
	r.mu.Lock()
	r.budget = budget
	r.mu.Unlock()
}

// transferred is the bytes fetched so far.
func (r *raGetRemote) transferred() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fetched
}

// setOnFetch sets (or clears, with nil) the progress hook.
func (r *raGetRemote) setOnFetch(fn func(fetched int64)) {
	r.mu.Lock()
	r.onFetch = fn
	r.mu.Unlock()
}

// ReadAt implements io.ReaderAt.
func (r *raGetRemote) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("raget: negative offset")
	}
	n := 0
	for n < len(p) {
		if off >= r.size {
			return n, io.EOF
		}
		blk, start, err := r.blockAt(off)
		if err != nil {
			return n, err
		}
		within := off - start
		if within < 0 || within >= int64(len(blk)) {
			return n, io.ErrUnexpectedEOF
		}
		c := copy(p[n:], blk[within:])
		n += c
		off += int64(c)
	}
	return n, nil
}

// blockAt is the block holding byte off, from the LRU or fetched, and the
// offset it starts at.
func (r *raGetRemote) blockAt(off int64) ([]byte, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	size := r.blockSize
	if size <= 0 {
		size = raGetBlock
	}
	idx := off / size
	start := idx * size
	if b, ok := r.blocks[idx]; ok {
		r.touchLocked(idx)
		return b, start, nil
	}
	end := min(start+size, r.size)
	if start >= end {
		return nil, start, io.EOF
	}
	if r.budget > 0 && r.fetched+(end-start) > r.budget {
		return nil, start, fmt.Errorf("reading %s would download more than the %s planned for it", path.Base(r.url), formatInstallBytes(r.budget))
	}
	var data []byte
	var err error
	for try := 0; try < raGetFetchTries; try++ {
		if try > 0 {
			select {
			case <-r.ctx.Done():
				return nil, start, r.ctx.Err()
			case <-time.After(time.Duration(try*try) * 2 * time.Second):
			}
		}
		data, err = r.fetchLocked(start, end-1)
		if err == nil || r.ctx.Err() != nil || errors.Is(err, errRaGetNoRange) {
			break
		}
	}
	if err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return nil, start, ctxErr
		}
		return nil, start, err
	}
	r.fetched += int64(len(data))
	if r.onFetch != nil {
		r.onFetch(r.fetched)
	}
	r.blocks[idx] = data
	r.lru = append(r.lru, idx)
	for len(r.lru) > raGetBlocksKept {
		delete(r.blocks, r.lru[0])
		r.lru = r.lru[1:]
	}
	return data, start, nil
}

// touchLocked marks block idx as the most recently used.
func (r *raGetRemote) touchLocked(idx int64) {
	for i, b := range r.lru {
		if b == idx {
			r.lru = append(append(r.lru[:i:i], r.lru[i+1:]...), idx)
			return
		}
	}
}

// fetchLocked downloads bytes first..last (inclusive): from the signed URL a
// redirect led to while it is fresh, else from the release URL itself.
func (r *raGetRemote) fetchLocked(first, last int64) ([]byte, error) {
	target, reused := r.url, false
	if r.direct != "" && time.Since(r.directAt) < raGetDirectTTL {
		target, reused = r.direct, true
	}
	data, final, status, err := r.get(target, first, last)
	if reused && err != nil && status != 0 && status != http.StatusOK {
		// The signed URL ran out (or was refused): ask the release URL again.
		r.direct = ""
		data, final, _, err = r.get(r.url, first, last)
		reused = false
	}
	if err != nil {
		return nil, err
	}
	if !reused && final != "" && final != r.url {
		r.direct, r.directAt = final, time.Now()
	}
	return data, nil
}

// get is one range request. final is the URL that answered (after
// redirects), status its HTTP status (0 when none came).
func (r *raGetRemote) get(target string, first, last int64) (data []byte, final string, status int, err error) {
	if err := emulatorUpdateValidateURL(target); err != nil {
		return nil, "", 0, err
	}
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("User-Agent", "JukaHub-EmulatorUpdates/"+versionString(nil))
	req.Header.Set("Range", "bytes="+strconv.FormatInt(first, 10)+"-"+strconv.FormatInt(last, 10))
	// Asked plainly: a compressed answer would make the range the compressed
	// body's. (Go's transport asks for none on a range request; a proxy might.)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	final = target
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	host := raGetHost(final)
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		return nil, final, resp.StatusCode, fmt.Errorf("%w (%s)", errRaGetNoRange, host)
	default:
		return nil, final, resp.StatusCode, fmt.Errorf("HTTP %d from %s", resp.StatusCode, host)
	}
	a, b, total, ok := raGetContentRange(resp.Header.Get("Content-Range"))
	if !ok || a != first || b != last || (total >= 0 && total != r.size) {
		return nil, final, resp.StatusCode, fmt.Errorf("%s answered %q for bytes %d-%d of a %d-byte file",
			host, resp.Header.Get("Content-Range"), first, last, r.size)
	}
	data = make([]byte, last-first+1)
	if _, err := io.ReadFull(resp.Body, data); err != nil {
		return nil, final, resp.StatusCode, fmt.Errorf("reading bytes %d-%d from %s: %w", first, last, host, err)
	}
	return data, final, resp.StatusCode, nil
}

// raGetHost is a URL's host, for messages (a signed URL's query is long and
// says nothing).
func raGetHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// raGetContentRange reads "bytes first-last/total" ("*" total: -1).
func raGetContentRange(s string) (first, last, total int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(s), "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	span, size, found := strings.Cut(strings.TrimSpace(rest), "/")
	if !found {
		return 0, 0, 0, false
	}
	a, b, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, 0, false
	}
	var err error
	if first, err = strconv.ParseInt(strings.TrimSpace(a), 10, 64); err != nil || first < 0 {
		return 0, 0, 0, false
	}
	if last, err = strconv.ParseInt(strings.TrimSpace(b), 10, 64); err != nil || last < first {
		return 0, 0, 0, false
	}
	total = -1
	if size = strings.TrimSpace(size); size != "*" {
		if total, err = strconv.ParseInt(size, 10, 64); err != nil || total <= last {
			return 0, 0, 0, false
		}
	}
	return first, last, total, true
}

// ── One entry ────────────────────────────────────────────────────────────────

// raGetOpen opens f for reading. The reader reports an error at the end when
// the bytes are not f's size or CRC-32 (archive/zip itself for Store and
// Deflate, raGetChecked for the rest).
func raGetOpen(f *zip.File) (io.ReadCloser, error) {
	switch f.Method {
	case zip.Store, zip.Deflate:
		return f.Open()
	case raGetMethodBzip2, raGetMethodLZMA, raGetMethodZstd:
	default:
		return nil, fmt.Errorf("%s is packed with compression method %d, which JukaHub cannot unpack", path.Base(f.Name), f.Method)
	}
	if f.Flags&0x1 != 0 {
		return nil, fmt.Errorf("%s is encrypted", path.Base(f.Name))
	}
	raw, err := f.OpenRaw()
	if err != nil {
		return nil, err
	}
	out := &raGetChecked{want: f.UncompressedSize64, crc: f.CRC32, h: crc32.NewIEEE(), name: path.Base(f.Name)}
	switch f.Method {
	case raGetMethodBzip2:
		out.r = bzip2.NewReader(raw)
	case raGetMethodLZMA:
		dec, err := raGetLZMA(raw, f.UncompressedSize64, f.Flags&0x2 != 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path.Base(f.Name), err)
		}
		out.r = dec
	case raGetMethodZstd:
		dec, err := zstd.NewReader(raw, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(raGetZstdMemMax))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path.Base(f.Name), err)
		}
		out.r, out.close = dec, dec.Close
	}
	return out, nil
}

// raGetLZMA reads a zip entry packed with LZMA (method 14): a four-byte header
// (the encoder's version and the size of the properties, always 5), the five
// properties bytes, then the stream. The classic .lzma header the vendored
// reader wants is made of the properties and the size: the entry's size, or
// "unknown" when the stream ends with an end marker (general purpose bit 1).
func raGetLZMA(raw io.Reader, size uint64, endMarker bool) (io.Reader, error) {
	var head [9]byte
	if _, err := io.ReadFull(raw, head[:]); err != nil {
		return nil, fmt.Errorf("short LZMA header: %w", err)
	}
	if n := binary.LittleEndian.Uint16(head[2:4]); n != 5 {
		return nil, fmt.Errorf("LZMA properties of %d bytes, not 5", n)
	}
	var classic [lzma.HeaderLen]byte
	copy(classic[:5], head[4:9])
	if endMarker {
		binary.LittleEndian.PutUint64(classic[5:], ^uint64(0))
	} else {
		binary.LittleEndian.PutUint64(classic[5:], size)
	}
	return lzma.ReaderConfig{DictCap: raGetLZMADictMax}.NewReader(io.MultiReader(bytes.NewReader(classic[:]), raw))
}

// raGetChecked passes a decoded entry through, counting and hashing it, and
// turns the end into an error when the size or CRC-32 is not the entry's.
type raGetChecked struct {
	r     io.Reader
	want  uint64
	crc   uint32
	n     uint64
	h     hash.Hash32
	name  string
	close func()
	err   error
}

func (c *raGetChecked) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.r.Read(p)
	if n > 0 {
		c.n += uint64(n)
		if c.n > c.want {
			c.err = fmt.Errorf("%s unpacks to more than the %d bytes its zip entry states", c.name, c.want)
			return 0, c.err
		}
		c.h.Write(p[:n])
	}
	switch {
	case err == io.EOF:
		switch {
		case c.n != c.want:
			c.err = fmt.Errorf("%s ended after %d of its %d bytes", c.name, c.n, c.want)
		case c.h.Sum32() != c.crc:
			c.err = fmt.Errorf("%s does not match its CRC-32", c.name)
		default:
			c.err = io.EOF
		}
		return n, c.err
	case err != nil:
		c.err = err
	}
	return n, err
}

// Close implements io.Closer.
func (c *raGetChecked) Close() error {
	if c.close != nil {
		c.close()
		c.close = nil
	}
	return nil
}
