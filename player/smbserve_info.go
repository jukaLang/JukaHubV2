package main

// smbserve_info.go — what the SMB share says about files and the card
// (MS-FSCC 2.4 / 2.5): directory entries for QUERY_DIRECTORY, and the file,
// file-system and security answers of QUERY_INFO.
//
// A FAT/exFAT card keeps one time per file that Go can read portably, so
// the four NTFS times are all the modification time. File ids are a hash of
// the path folded to lower case (stable across connections). Dot-files are
// FILE_ATTRIBUTE_HIDDEN, as Samba shows them. The volume says "NTFS",
// case-preserving and case-insensitive, without named streams (so Windows
// and macOS never try to write Zone.Identifier or resource-fork streams),
// and read-only while the share is. The security descriptor grants Everyone
// what the share allows; changing it is accepted and ignored.

import (
	"encoding/binary"
	"hash/crc32"
	"hash/fnv"
	"os"
	"path"
	"strings"
	"time"
)

// smbSrvStat is what the share reports about one file or folder.
type smbSrvStat struct {
	size  int64
	mtime time.Time
	dir   bool
	id    uint64
	attrs uint32
}

const (
	smbSrvAttrArchive = 0x00000020
	smbSrvAttrNormal  = 0x00000080
)

// smbSrvStatOf turns a FileInfo of share path rel into an smbSrvStat.
func smbSrvStatOf(rel string, fi os.FileInfo) smbSrvStat {
	st := smbSrvStat{size: fi.Size(), mtime: fi.ModTime(), dir: fi.IsDir(), id: smbSrvFileID(rel)}
	if st.dir || st.size < 0 {
		st.size = 0
	}
	st.attrs = smbSrvAttrs(path.Base(rel), st.dir)
	return st
}

// smbSrvAttrs is ARCHIVE for files, DIRECTORY for folders, plus HIDDEN for
// dot-files.
func smbSrvAttrs(name string, dir bool) uint32 {
	a := uint32(smbSrvAttrArchive)
	if dir {
		a = smb2AttrDirectory
	}
	if strings.HasPrefix(name, ".") && name != "." && name != ".." {
		a |= smb2AttrHidden
	}
	return a
}

// smbSrvFileID is a stable non-zero id for a share path.
func smbSrvFileID(rel string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(rel)))
	if v := h.Sum64(); v != 0 {
		return v
	}
	return 1
}

// smbSrvFileTime is t as FILETIME (0 for the zero time).
func smbSrvFileTime(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	v := t.UnixNano()/100 + smb2FileTimeEpoch
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// smbSrvAlloc is size rounded up to 4 KiB clusters.
func smbSrvAlloc(size int64) uint64 {
	if size <= 0 {
		return 0
	}
	return uint64((size + 4095) &^ 4095)
}

// smbSrvPutTimes writes the four times (creation, access, write, change).
func smbSrvPutTimes(b []byte, t time.Time) {
	ft := smbSrvFileTime(t)
	for i := 0; i < 32; i += 8 {
		binary.LittleEndian.PutUint64(b[i:], ft)
	}
}

// smbSrvDirEntry encodes one entry in a FileInformationClass QUERY_DIRECTORY
// supports (nil for another class). NextEntryOffset is left zero.
func smbSrvDirEntry(class byte, e smbSrvDirEnt) []byte {
	var fixed int
	switch class {
	case 1: // FileDirectoryInformation
		fixed = 64
	case 2: // FileFullDirectoryInformation
		fixed = 68
	case 3: // FileBothDirectoryInformation
		fixed = 94
	case 12: // FileNamesInformation
		fixed = 12
	case 37: // FileIdBothDirectoryInformation
		fixed = 104
	case 38: // FileIdFullDirectoryInformation
		fixed = 80
	default:
		return nil
	}
	nm := smb2UTF16(e.name)
	b := make([]byte, fixed+len(nm))
	copy(b[fixed:], nm)
	if class == 12 {
		binary.LittleEndian.PutUint32(b[8:], uint32(len(nm)))
		return b
	}
	smbSrvPutTimes(b[8:], e.st.mtime)
	if !e.st.dir {
		binary.LittleEndian.PutUint64(b[40:], uint64(e.st.size))
		binary.LittleEndian.PutUint64(b[48:], smbSrvAlloc(e.st.size))
	}
	binary.LittleEndian.PutUint32(b[56:], e.st.attrs)
	binary.LittleEndian.PutUint32(b[60:], uint32(len(nm)))
	switch class {
	case 37:
		binary.LittleEndian.PutUint64(b[96:], e.st.id)
	case 38:
		binary.LittleEndian.PutUint64(b[72:], e.st.id)
	}
	return b
}

// ── QUERY_INFO ────────────────────────────────────────────────────────────

func (c *smbSrvConn) queryInfo(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	b := r.body()
	if len(b) < 40 {
		return smb2StatusInvalidParameter, nil
	}
	infoType, class := b[2], b[3]
	maxOut := binary.LittleEndian.Uint32(b[4:])
	addl := binary.LittleEndian.Uint32(b[16:])
	h, st := c.handleFor(r, ch, b[24:40])
	if st != 0 {
		return st, nil
	}
	var data []byte
	fixed := true
	switch infoType {
	case 1:
		data, fixed, st = c.fileInfo(h, class)
	case 2:
		if h.pipe != nil {
			return smb2StatusNotSupported, nil
		}
		data, fixed, st = c.fsInfo(class)
	case 3:
		data = smbSrvSecurityDesc(addl, h.isDir, c.srv.ReadOnly())
		if uint32(len(data)) > maxOut {
			need := binary.LittleEndian.AppendUint32(nil, uint32(len(data)))
			return smbSrvStatusBufferTooSmall, smbSrvErrorBody(need)
		}
	default:
		return smb2StatusNotSupported, nil
	}
	if st != 0 {
		return st, nil
	}
	status := uint32(smb2StatusSuccess)
	if uint32(len(data)) > maxOut {
		if fixed {
			return smbSrvStatusInfoLengthMis, nil
		}
		data, status = data[:maxOut], smb2StatusBufferOverflow
	}
	body := make([]byte, 8, 9+len(data))
	binary.LittleEndian.PutUint16(body, 9)
	binary.LittleEndian.PutUint16(body[2:], smb2HeaderLen+8)
	binary.LittleEndian.PutUint32(body[4:], uint32(len(data)))
	body = append(body, data...)
	if len(data) == 0 {
		body = append(body, 0)
	}
	return status, body
}

// fileInfo answers the file information classes Windows, macOS and Linux
// ask for. fixed is false for the classes that may be cut short with
// STATUS_BUFFER_OVERFLOW.
func (c *smbSrvConn) fileInfo(h *smbSrvHandle, class byte) ([]byte, bool, uint32) {
	st := c.statHandle(h)
	if h.pipe != nil && class != 4 && class != 5 {
		return nil, true, smb2StatusNotSupported
	}
	basic := func() []byte {
		b := make([]byte, 40)
		smbSrvPutTimes(b, st.mtime)
		binary.LittleEndian.PutUint32(b[32:], st.attrs)
		return b
	}
	standard := func() []byte {
		b := make([]byte, 24)
		if !st.dir {
			binary.LittleEndian.PutUint64(b[0:], smbSrvAlloc(st.size))
			binary.LittleEndian.PutUint64(b[8:], uint64(st.size))
		}
		binary.LittleEndian.PutUint32(b[16:], 1)
		if h.delOnClose {
			b[20] = 1
		}
		if st.dir {
			b[21] = 1
		}
		return b
	}
	u32 := func(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
	u64 := func(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }
	switch class {
	case 4: // FileBasicInformation
		return basic(), true, 0
	case 5: // FileStandardInformation
		return standard(), true, 0
	case 6: // FileInternalInformation
		return u64(st.id), true, 0
	case 7: // FileEaInformation
		return u32(0), true, 0
	case 8: // FileAccessInformation
		return u32(h.access), true, 0
	case 14: // FilePositionInformation
		return u64(uint64(h.pos)), true, 0
	case 16, 17: // FileModeInformation, FileAlignmentInformation
		return u32(0), true, 0
	case 18: // FileAllInformation
		full := `\`
		if h.rel != "." {
			full += strings.ReplaceAll(h.rel, "/", `\`)
		}
		name := smb2UTF16(full)
		b := make([]byte, 0, 100+len(name))
		b = append(b, basic()...)
		b = append(b, standard()...)
		b = binary.LittleEndian.AppendUint64(b, st.id)
		b = binary.LittleEndian.AppendUint32(b, 0) // EA size
		b = binary.LittleEndian.AppendUint32(b, h.access)
		b = binary.LittleEndian.AppendUint64(b, uint64(h.pos))
		b = binary.LittleEndian.AppendUint32(b, 0) // mode
		b = binary.LittleEndian.AppendUint32(b, 0) // alignment
		b = binary.LittleEndian.AppendUint32(b, uint32(len(name)))
		return append(b, name...), false, 0
	case 22: // FileStreamInformation: the unnamed data stream of a file
		if st.dir {
			return nil, false, 0
		}
		nm := smb2UTF16("::$DATA")
		b := make([]byte, 24, 24+len(nm))
		binary.LittleEndian.PutUint32(b[4:], uint32(len(nm)))
		binary.LittleEndian.PutUint64(b[8:], uint64(st.size))
		binary.LittleEndian.PutUint64(b[16:], smbSrvAlloc(st.size))
		return append(b, nm...), false, 0
	case 28: // FileCompressionInformation: not compressed
		b := make([]byte, 16)
		binary.LittleEndian.PutUint64(b, uint64(st.size))
		return b, true, 0
	case 34: // FileNetworkOpenInformation
		b := make([]byte, 56)
		smbSrvPutTimes(b, st.mtime)
		if !st.dir {
			binary.LittleEndian.PutUint64(b[32:], smbSrvAlloc(st.size))
			binary.LittleEndian.PutUint64(b[40:], uint64(st.size))
		}
		binary.LittleEndian.PutUint32(b[48:], st.attrs)
		return b, true, 0
	case 35: // FileAttributeTagInformation
		b := make([]byte, 8)
		binary.LittleEndian.PutUint32(b, st.attrs)
		return b, true, 0
	}
	return nil, true, smb2StatusNotSupported
}

// fsInfo answers the file-system classes. Without statfs (it never fails on
// the handheld) the card reports 1 TiB free rather than none, so a copy is
// not refused up front; a full card still fails the write itself.
func (c *smbSrvConn) fsInfo(class byte) ([]byte, bool, uint32) {
	const unit, sector = 4096, 512
	total, free, ok := smbServeDiskSpace(c.root.Name())
	if !ok {
		total, free = 1<<40, 1<<40
	}
	totalU, freeU := total/unit, free/unit
	switch class {
	case 1: // FileFsVolumeInformation
		label := smb2UTF16(smbServeShareName)
		b := make([]byte, 18, 18+len(label))
		binary.LittleEndian.PutUint32(b[8:], crc32.ChecksumIEEE([]byte(c.nbName)))
		binary.LittleEndian.PutUint32(b[12:], uint32(len(label)))
		return append(b, label...), false, 0
	case 3: // FileFsSizeInformation
		b := make([]byte, 24)
		binary.LittleEndian.PutUint64(b[0:], totalU)
		binary.LittleEndian.PutUint64(b[8:], freeU)
		binary.LittleEndian.PutUint32(b[16:], unit/sector)
		binary.LittleEndian.PutUint32(b[20:], sector)
		return b, true, 0
	case 4: // FileFsDeviceInformation: a mounted disk
		b := make([]byte, 8)
		binary.LittleEndian.PutUint32(b[0:], 0x07)
		binary.LittleEndian.PutUint32(b[4:], 0x20)
		return b, true, 0
	case 5: // FileFsAttributeInformation
		attrs := uint32(0x00000002 | 0x00000004) // case preserved, Unicode names
		if c.srv.ReadOnly() {
			attrs |= 0x00080000 // FILE_READ_ONLY_VOLUME
		}
		name := smb2UTF16("NTFS")
		b := make([]byte, 12, 12+len(name))
		binary.LittleEndian.PutUint32(b[0:], attrs)
		binary.LittleEndian.PutUint32(b[4:], 255)
		binary.LittleEndian.PutUint32(b[8:], uint32(len(name)))
		return append(b, name...), false, 0
	case 7: // FileFsFullSizeInformation
		b := make([]byte, 32)
		binary.LittleEndian.PutUint64(b[0:], totalU)
		binary.LittleEndian.PutUint64(b[8:], freeU)
		binary.LittleEndian.PutUint64(b[16:], freeU)
		binary.LittleEndian.PutUint32(b[24:], unit/sector)
		binary.LittleEndian.PutUint32(b[28:], sector)
		return b, true, 0
	case 11: // FileFsSectorSizeInformation
		b := make([]byte, 28)
		for i := 0; i < 16; i += 4 {
			binary.LittleEndian.PutUint32(b[i:], sector)
		}
		binary.LittleEndian.PutUint32(b[16:], 0x3) // aligned device and partition
		return b, true, 0
	}
	return nil, true, smb2StatusNotSupported
}

// smbSrvSecurityDesc is a self-relative security descriptor with the parts
// addl asks for (owner 1, group 2, DACL 4): owner and group Everyone, and a
// DACL that grants Everyone what the share allows (inherited in folders).
func smbSrvSecurityDesc(addl uint32, dir, ro bool) []byte {
	everyone := []byte{1, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0} // S-1-1-0
	mask := uint32(smbSrvAccessAll)
	if ro {
		mask = smbSrvAccessReadOnly
	}
	b := make([]byte, 20, 80)
	b[0] = 1
	ctrl := uint16(0x8000) // SE_SELF_RELATIVE
	if addl&1 != 0 {
		binary.LittleEndian.PutUint32(b[4:], uint32(len(b)))
		b = append(b, everyone...)
	}
	if addl&2 != 0 {
		binary.LittleEndian.PutUint32(b[8:], uint32(len(b)))
		b = append(b, everyone...)
	}
	if addl&4 != 0 {
		ctrl |= 0x0004 // SE_DACL_PRESENT
		binary.LittleEndian.PutUint32(b[16:], uint32(len(b)))
		acl := make([]byte, 16, 28)
		acl[0] = 2                                  // ACL revision
		binary.LittleEndian.PutUint16(acl[2:], 28)  // AclSize
		binary.LittleEndian.PutUint16(acl[4:], 1)   // AceCount
		binary.LittleEndian.PutUint16(acl[10:], 20) // AceSize (ACCESS_ALLOWED_ACE at 8)
		if dir {
			acl[9] = 0x03 // object and container inherit
		}
		binary.LittleEndian.PutUint32(acl[12:], mask)
		b = append(b, append(acl, everyone...)...)
	}
	binary.LittleEndian.PutUint16(b[2:], ctrl)
	return b
}
