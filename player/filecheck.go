package main

// filecheck.go — Verify a file: SHA-256, SHA-1, MD5 and CRC32 of one file in
// a single read, and the comparison with the checksum a download page (or a
// No-Intro / Redump list, which use the other three) gives for it.
//
// The read runs on a goroutine of its own — a 4 GB disc image takes a minute
// or two on a card — so leaving the screen does not stop it; choosing another
// file or Stop does, and it waits while a game owns the screen. The screen
// (filecheck_ui.go) copies the state out under fileCheckState.mu.

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// fileSumKind is one of the four checksums.
type fileSumKind int

const (
	fileSumSHA256 fileSumKind = iota
	fileSumSHA1
	fileSumMD5
	fileSumCRC32
	fileSumCount
)

// fileSumName is a checksum's name as download pages write it.
func fileSumName(k fileSumKind) string {
	switch k {
	case fileSumSHA256:
		return "SHA-256"
	case fileSumSHA1:
		return "SHA-1"
	case fileSumMD5:
		return "MD5"
	case fileSumCRC32:
		return "CRC32"
	}
	return ""
}

// fileSumHexLen is how many hex digits a checksum has: how a pasted one is
// recognised.
func fileSumHexLen(k fileSumKind) int {
	switch k {
	case fileSumSHA256:
		return 64
	case fileSumSHA1:
		return 40
	case fileSumMD5:
		return 32
	case fileSumCRC32:
		return 8
	}
	return 0
}

// fileCheckRun is the file being (or last) read.
type fileCheckRun struct {
	gen      uint64
	path     string
	size     int64
	done     int64
	sums     [fileSumCount]string
	running  bool
	stopped  bool
	err      string
	started  time.Time
	finished time.Time
	// expect is the checksum to compare with, cleaned to lower-case hex.
	expect string
}

var fileCheckState struct {
	mu     sync.Mutex
	gen    uint64
	cancel context.CancelFunc
	run    fileCheckRun
}

// fileCheckSnapshot copies the run.
func fileCheckSnapshot() fileCheckRun {
	fileCheckState.mu.Lock()
	defer fileCheckState.mu.Unlock()
	return fileCheckState.run
}

// fileCheckStart reads path. A read already running stops; a comparison set
// for the same file is kept, so reading it again checks again.
func fileCheckStart(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("that is a folder")
	}
	ctx, cancel := context.WithCancel(context.Background())
	fileCheckState.mu.Lock()
	if fileCheckState.cancel != nil {
		fileCheckState.cancel()
	}
	fileCheckState.gen++
	gen := fileCheckState.gen
	expect := ""
	if fileCheckState.run.path == path {
		expect = fileCheckState.run.expect
	}
	fileCheckState.run = fileCheckRun{gen: gen, path: path, size: info.Size(), running: true, started: time.Now(), expect: expect}
	fileCheckState.cancel = cancel
	fileCheckState.mu.Unlock()
	go fileCheckWork(ctx, cancel, gen, path)
	return nil
}

// fileCheckStop stops the read under way.
func fileCheckStop() {
	fileCheckState.mu.Lock()
	defer fileCheckState.mu.Unlock()
	if fileCheckState.run.running && fileCheckState.cancel != nil {
		fileCheckState.cancel()
	}
}

// fileCheckWork reads the file and publishes the four sums.
func fileCheckWork(ctx context.Context, cancel context.CancelFunc, gen uint64, path string) {
	defer cancel()
	sums, err := fileCheckHash(ctx, path, func(done int64) {
		fileCheckState.mu.Lock()
		if fileCheckState.gen == gen {
			fileCheckState.run.done = done
		}
		fileCheckState.mu.Unlock()
	})
	fileCheckState.mu.Lock()
	defer fileCheckState.mu.Unlock()
	if fileCheckState.gen != gen {
		return
	}
	run := &fileCheckState.run
	run.running = false
	run.finished = time.Now()
	fileCheckState.cancel = nil
	switch {
	case ctx.Err() != nil:
		run.stopped = true
	case err != nil:
		run.err = filesFriendlyError(err)
	default:
		run.sums = sums
		run.done = run.size
	}
}

// fileCheckHash reads path once through all four hashes, reporting progress
// about seven times a second, and stops with ctx.
func fileCheckHash(ctx context.Context, path string, progress func(int64)) ([fileSumCount]string, error) {
	var out [fileSumCount]string
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	h256, h1, h5, hc := sha256.New(), sha1.New(), md5.New(), crc32.NewIEEE()
	w := io.MultiWriter(h256, h1, h5, hc)
	buf := make([]byte, 1<<20)
	var done int64
	last := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		for gameScreenSuspended() && ctx.Err() == nil {
			time.Sleep(500 * time.Millisecond)
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			done += int64(n)
			if time.Since(last) > 150*time.Millisecond {
				progress(done)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return out, rerr
		}
	}
	progress(done)
	out[fileSumSHA256] = hex.EncodeToString(h256.Sum(nil))
	out[fileSumSHA1] = hex.EncodeToString(h1.Sum(nil))
	out[fileSumMD5] = hex.EncodeToString(h5.Sum(nil))
	out[fileSumCRC32] = hex.EncodeToString(hc.Sum(nil))
	return out, nil
}

// fileCheckParseExpected finds the checksum in pasted text: the longest run of
// hex digits that is exactly a checksum's length (64, 40, 32 or 8), so a
// sha256sum line ("<hash>  name.iso"), "SHA256: <HASH>" and a bare hash all
// work, and a short hex run inside a file name does not win over the hash
// beside it. ok is false when there is none. Pure.
func fileCheckParseExpected(text string) (string, bool) {
	text = strings.ToLower(text)
	best := ""
	start := -1
	consider := func(end int) {
		if start < 0 {
			return
		}
		run := text[start:end]
		start = -1
		switch len(run) {
		case 64, 40, 32, 8:
			if len(run) > len(best) {
				best = run
			}
		}
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		hexDigit := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		switch {
		case hexDigit && start < 0:
			start = i
		case !hexDigit:
			consider(i)
		}
	}
	consider(len(text))
	return best, best != ""
}

// fileCheckCompare says which checksum expect is (by its length), whether the
// file's matches, and whether there is an answer yet. Pure.
func fileCheckCompare(sums [fileSumCount]string, expect string) (kind fileSumKind, match bool, known bool) {
	if expect == "" {
		return 0, false, false
	}
	for k := fileSumKind(0); k < fileSumCount; k++ {
		if len(expect) == fileSumHexLen(k) {
			return k, sums[k] != "" && sums[k] == expect, sums[k] != ""
		}
	}
	return 0, false, false
}

// fileCheckSetExpected stores the checksum to compare with, out of text. It
// reports false when text holds none.
func fileCheckSetExpected(text string) bool {
	sum, ok := fileCheckParseExpected(text)
	if !ok {
		return false
	}
	fileCheckState.mu.Lock()
	fileCheckState.run.expect = sum
	fileCheckState.mu.Unlock()
	return true
}

// fileCheckClearExpected forgets the comparison.
func fileCheckClearExpected() {
	fileCheckState.mu.Lock()
	fileCheckState.run.expect = ""
	fileCheckState.mu.Unlock()
}

// fileCheckReport is the four sums as text, for Copy all.
func fileCheckReport(run fileCheckRun) string {
	var b strings.Builder
	b.WriteString(run.path + "\n")
	b.WriteString(formatSize(run.size) + "\n")
	for k := fileSumKind(0); k < fileSumCount; k++ {
		b.WriteString(fileSumName(k) + ": " + run.sums[k] + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
