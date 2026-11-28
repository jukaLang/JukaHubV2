package main

// files_extract_7z.go — Files unpacking a .7z itself when the archive uses
// coders sevenzip.go decodes (Copy, LZMA, LZMA2, with the x86 BCJ, ARM, ARM-Thumb
// and ARM64 branch filters). Anything else — BCJ2, PPMd, AES, an unknown coder
// — comes back as an error and the caller hands the archive to 7-Zip, the way
// an .xz with a filter this build does not have already falls back.
//
// The staging folder, the os.Root writes, the space budget, the CTX cancel and
// the CRC checks are the same rules the other formats follow; an entry that
// fails its CRC removes itself and stops the extraction, so a damaged archive
// never leaves half a good-looking file in the staging area.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// feExtract7z unpacks a local .7z with the in-process reader.
func feExtract7z(ctx context.Context, archive, out string, st *feExtractStats, meter *feExtractMeter) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	arc, err := sevenZipOpen(f, info.Size())
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(out)
	if err != nil {
		return err
	}
	defer root.Close()
	budget, free := feExtractBudget(out)
	if budget < 0 || budget > feExtractMaxBytes {
		budget = feExtractMaxBytes
	}
	written := make(map[string]bool, len(arc.Entries))
	buf := make([]byte, feExtractBufSize)

	// Folders and empty files first; a solid folder's files follow in the
	// order their bytes are packed, not the order the directory lists them.
	for _, e := range arc.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, reason := feExtractRel(e.Name)
		if reason != "" {
			st.Skips.add(reason)
			continue
		}
		if rel == "" {
			continue
		}
		if e.Dir {
			if err := feExtractMkdir(root, rel, st); err != nil {
				return err
			}
			continue
		}
		if e.folder >= 0 {
			continue
		}
		meter.setEntry(rel)
		n, err := feExtractWrite(ctx, root, rel, bytes.NewReader(nil), 0, feExtractMaxBytes, 0o644, meter, nil, false)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		st.Files++
		st.Bytes += n
		written[rel] = true
	}

	byFolder := make(map[int][]int)
	for i, e := range arc.Entries {
		if e.folder >= 0 {
			byFolder[e.folder] = append(byFolder[e.folder], i)
		}
	}
	folders := make([]int, 0, len(byFolder))
	for fi := range byFolder {
		folders = append(folders, fi)
	}
	sort.Ints(folders)
	for _, fi := range folders {
		list := byFolder[fi]
		sort.SliceStable(list, func(i, j int) bool {
			return arc.Entries[list[i]].offset < arc.Entries[list[j]].offset
		})
		var end uint64
		for _, idx := range list {
			e := arc.Entries[idx]
			end = max(end, e.offset+e.Size)
		}
		rd, err := arc.streams.folderReader(f, info.Size(), fi, end)
		if err != nil {
			return err
		}
		var pos uint64
		for _, idx := range list {
			if err := ctx.Err(); err != nil {
				return err
			}
			e := arc.Entries[idx]
			rel, reason := feExtractRel(e.Name)
			if reason != "" || rel == "" {
				if reason != "" {
					st.Skips.add(reason)
				}
				continue
			}
			if e.offset < pos {
				return fmt.Errorf("%w (files that overlap)", errBadToolArchive)
			}
			if err := fe7zDiscard(ctx, rd, e.offset-pos, buf); err != nil {
				return err
			}
			pos = e.offset + e.Size
			if written[rel] {
				st.Skips.Duplicate++
				continue
			}
			if parent := filepath.Dir(rel); parent != "." {
				if err := root.MkdirAll(parent, 0o755); err != nil {
					return err
				}
			}
			meter.setEntry(rel)
			left := budget - st.Bytes
			if left < 0 {
				left = 0
			}
			if left > feExtractMaxBytes {
				left = feExtractMaxBytes
			}
			h := crc32.NewIEEE()
			body := io.LimitReader(rd, int64(e.Size))
			n, err := feExtractWrite(ctx, root, rel, io.TeeReader(body, h), int64(e.Size), left, 0o644, meter, buf, false)
			if errors.Is(err, errFeEntryTooBig) {
				return feNoSpaceError{need: n + feExtractSpaceMargin, free: free, more: true}
			}
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			if e.HasCRC && h.Sum32() != e.CRC {
				_ = root.Remove(rel)
				return fmt.Errorf("%w (%s does not match its CRC-32)", errBadToolArchive, filepath.Base(rel))
			}
			st.Files++
			st.Bytes += n
			written[rel] = true
		}
	}
	return nil
}

// fe7zDiscard drops n bytes of a folder's decoded stream, stopping when ctx
// ends.
func fe7zDiscard(ctx context.Context, r io.Reader, n uint64, buf []byte) error {
	for n > 0 {
		k := min(n, uint64(len(buf)))
		got, err := io.ReadFull(&archiveReader{ctx: ctx, r: r}, buf[:k])
		n -= uint64(got)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w (a block ends early)", errSevenZipFormat)
			}
			return err
		}
	}
	return nil
}
