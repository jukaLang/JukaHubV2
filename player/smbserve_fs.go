package main

// smbserve_fs.go — the file commands of the SMB share (smbserve_conn.go):
// CREATE, CLOSE, FLUSH, READ, WRITE, LOCK, QUERY_DIRECTORY, SET_INFO and
// IOCTL, all inside the SD card's os.Root.
//
//   - Paths: Windows' backslashes become slashes; "..", wildcards, control
//     characters and alternate data streams are refused ("name::$DATA" is
//     the file itself). Names are matched exactly first, then without regard
//     to case by scanning the folder (smbSrvResolve), so a case-sensitive
//     card behaves like the case-insensitive disk Windows expects.
//   - No oplocks, leases, durable handles or sharing modes: every open gets
//     OplockLevel NONE and two clients may write the same file, as with FTP.
//   - Read-only (smb_readonly) is checked at CREATE (access mask, create
//     dispositions, delete-on-close) and again on every write, so turning it
//     on also stops handles that were open for writing.
//   - Compound chains: a FileId of all ones in a related request is the
//     handle the chain's CREATE opened, or that CREATE's error.

import (
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"strings"
	"syscall"
)

// Access mask bits (MS-SMB2 2.2.13.1).
const (
	smbSrvAccessReadData   = 0x00000001
	smbSrvAccessWriteData  = 0x00000002
	smbSrvAccessAppend     = 0x00000004
	smbSrvAccessExecute    = 0x00000020
	smbSrvAccessDelete     = 0x00010000
	smbSrvAccessMaxAllowed = 0x02000000
	smbSrvGenericAll       = 0x10000000
	smbSrvGenericExecute   = 0x20000000
	smbSrvGenericWrite     = 0x40000000
	smbSrvGenericRead      = 0x80000000
	smbSrvAccessAll        = 0x001f01ff
	smbSrvAccessReadOnly   = 0x001200a9
	// smbSrvAccessWriteMask: write data, append, write EA, delete child,
	// write attributes, delete, write DAC, write owner.
	smbSrvAccessWriteMask = 0x000d0156
)

// Create dispositions and options.
const (
	smbSrvDispSupersede   = 0
	smbSrvDispOpen        = 1
	smbSrvDispCreate      = 2
	smbSrvDispOpenIf      = 3
	smbSrvDispOverwrite   = 4
	smbSrvDispOverwriteIf = 5

	smbSrvOptDeleteOnClose = 0x00001000
)

const (
	smbSrvFsctlDfsReferrals   = 0x00060194
	smbSrvFsctlDfsReferralsEx = 0x000601b0
	smbSrvFsctlPipeTransceive = 0x0011c017

	smbSrvMaxDirEntries = 100000
)

// smbSrvHandle is one open file, folder or pipe.
type smbSrvHandle struct {
	id         uint64
	sess       uint64
	tree       uint32
	rel        string
	isDir      bool
	f          *os.File
	access     uint32
	delOnClose bool
	pos        int64
	what       string
	scan       *smbSrvDirScan
	pipe       *smbSrvPipe
}

// smbSrvDirScan is a folder listing in progress.
type smbSrvDirScan struct {
	ents []smbSrvDirEnt
	pos  int
	sent bool
}

// smbSrvDirEnt is one listed name.
type smbSrvDirEnt struct {
	name string
	st   smbSrvStat
}

// smbSrvCleanPath turns a CREATE or rename name into a path inside the
// share ("." for the share itself). Status 0 means it is fine.
func smbSrvCleanPath(name string) (string, uint32) {
	p := strings.ReplaceAll(name, `\`, "/")
	if i := strings.IndexByte(p, ':'); i >= 0 {
		if !strings.EqualFold(p[i:], "::$DATA") {
			return "", smb2StatusObjectNameInvalid
		}
		p = p[:i]
	}
	p = strings.Trim(p, "/")
	if p == "" {
		return ".", 0
	}
	if len(p) > 4096 {
		return "", smb2StatusObjectNameInvalid
	}
	for _, el := range strings.Split(p, "/") {
		if !smbSrvNameOK(el) {
			return "", smb2StatusObjectNameInvalid
		}
	}
	return p, 0
}

// smbSrvNameOK reports whether el can be one path element on the share.
func smbSrvNameOK(el string) bool {
	return el != "" && el != "." && el != ".." && len(el) <= 255 && !smb2HasControl(el) &&
		!strings.ContainsAny(el, "\\/:\"*?<>|")
}

// smbSrvJoin joins a share path and one element.
func smbSrvJoin(dir, name string) string {
	if dir == "" || dir == "." {
		return name
	}
	return dir + "/" + name
}

// smbSrvFoldMatch is the entry of dir whose name equals name without regard
// to case ("" for none).
func smbSrvFoldMatch(root *os.Root, dir, name string) string {
	d, err := root.Open(dir)
	if err != nil {
		return ""
	}
	defer d.Close()
	names, _ := d.Readdirnames(-1)
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return n
		}
	}
	return ""
}

// smbSrvResolve maps rel onto the names on disk: exactly when it exists,
// else element by element without regard to case. exists says whether the
// whole path was found; parentOK whether everything but its last element
// was (so a new file can be made there under the name as given).
func smbSrvResolve(root *os.Root, rel string) (p string, exists, parentOK bool) {
	if rel == "." {
		return ".", true, true
	}
	if _, err := root.Lstat(rel); err == nil {
		return rel, true, true
	}
	parts := strings.Split(rel, "/")
	cur := "."
	for i, el := range parts {
		next := smbSrvJoin(cur, el)
		if _, err := root.Lstat(next); err != nil {
			m := smbSrvFoldMatch(root, cur, el)
			if m == "" {
				if i == len(parts)-1 {
					return next, false, true
				}
				return next, false, false
			}
			next = smbSrvJoin(cur, m)
		}
		cur = next
	}
	return cur, true, true
}

// smbSrvErrStatus maps a file system error to NTSTATUS.
func smbSrvErrStatus(err error) uint32 {
	if err == nil {
		return smb2StatusSuccess
	}
	// First: ENOTEMPTY also counts as fs.ErrExist.
	if st, ok := smbServeErrnoStatus(err); ok {
		return st
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return smb2StatusObjectNameNotFound
	case errors.Is(err, fs.ErrExist):
		return smbSrvStatusNameCollision
	case errors.Is(err, fs.ErrPermission):
		return smb2StatusAccessDenied
	case errors.Is(err, syscall.ENOSPC):
		return smbSrvStatusDiskFull
	case errors.Is(err, syscall.ENOTDIR):
		return smb2StatusObjectPathNotFound
	case errors.Is(err, syscall.ENAMETOOLONG):
		return smb2StatusObjectNameInvalid
	}
	if strings.Contains(err.Error(), "escapes") {
		return smb2StatusAccessDenied // os.Root: a link that leaves the card
	}
	return smbSrvStatusUnsuccessful
}

// smbSrvMapAccess turns generic rights into specific ones; MAXIMUM_ALLOWED
// is everything the share allows.
func smbSrvMapAccess(a uint32, ro bool) uint32 {
	if a&smbSrvGenericRead != 0 {
		a |= 0x00120089
	}
	if a&smbSrvGenericWrite != 0 {
		a |= 0x00120116
	}
	if a&smbSrvGenericExecute != 0 {
		a |= 0x001200a0
	}
	if a&smbSrvGenericAll != 0 {
		a |= smbSrvAccessAll
	}
	if a&smbSrvAccessMaxAllowed != 0 {
		if ro {
			a |= smbSrvAccessReadOnly
		} else {
			a |= smbSrvAccessAll
		}
	}
	return a & smbSrvAccessAll
}

// handleFor finds the handle a request names. A related request's all-ones
// FileId is the chain's last handle (or the error its CREATE got).
func (c *smbSrvConn) handleFor(r *smbSrvReq, ch *smbSrvChain, fid []byte) (*smbSrvHandle, uint32) {
	p := binary.LittleEndian.Uint64(fid[0:])
	v := binary.LittleEndian.Uint64(fid[8:])
	if p == math.MaxUint64 && v == math.MaxUint64 {
		switch {
		case !r.related:
			return nil, smbSrvStatusFileClosed
		case ch.createErr != 0:
			return nil, ch.createErr
		case !ch.haveFile:
			return nil, smbSrvStatusFileClosed
		}
		v = ch.fileID
	}
	h := c.handles[v]
	if h == nil || h.sess != r.sess.id || h.tree != r.tree.id {
		return nil, smbSrvStatusFileClosed
	}
	ch.fileID, ch.haveFile = h.id, true
	return h, 0
}

// addHandle registers h and makes it the chain's handle.
func (c *smbSrvConn) addHandle(h *smbSrvHandle, ch *smbSrvChain) {
	c.nextHandle++
	h.id = c.nextHandle
	c.handles[h.id] = h
	ch.fileID, ch.haveFile, ch.createErr = h.id, true, 0
}

// closeHandle closes one handle; delete-on-close removes the file now.
func (c *smbSrvConn) closeHandle(id uint64) {
	h := c.handles[id]
	if h == nil {
		return
	}
	delete(c.handles, id)
	if h.f != nil {
		_ = h.f.Close()
	}
	if h.what != "" {
		c.srv.clients.clearTransfer(c.clientID, h.what)
	}
	// Read-only switched on after the mark refuses the delete too.
	if h.delOnClose && h.pipe == nil && h.rel != "." && !c.srv.ReadOnly() {
		if err := c.root.Remove(h.rel); err != nil {
			logDebug("smb", "delete %s: %v", h.rel, err)
		}
	}
}

// closeTreeHandles closes what one session opened on one share.
func (c *smbSrvConn) closeTreeHandles(sess uint64, tree uint32) {
	for id, h := range c.handles {
		if h.sess == sess && h.tree == tree {
			c.closeHandle(id)
		}
	}
}

// ── CREATE ────────────────────────────────────────────────────────────────

func (c *smbSrvConn) create(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	st, body := c.createInner(r, ch)
	if st != smb2StatusSuccess {
		ch.createErr, ch.haveFile = st, false
	}
	return st, body
}

func (c *smbSrvConn) createInner(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	b := r.body()
	if len(b) < 56 {
		return smb2StatusInvalidParameter, nil
	}
	access := binary.LittleEndian.Uint32(b[24:])
	disp := binary.LittleEndian.Uint32(b[36:])
	opts := binary.LittleEndian.Uint32(b[40:])
	raw, ok := smbSrvSlice(r.msg, uint64(binary.LittleEndian.Uint16(b[44:])), uint64(binary.LittleEndian.Uint16(b[46:])))
	if !ok || len(raw)%2 != 0 {
		return smb2StatusInvalidParameter, nil
	}
	if len(c.handles) >= smbSrvMaxHandles {
		return smb2StatusInsufficientResources, nil
	}
	name := smb2FromUTF16(raw)
	if r.tree.ipc {
		return c.createPipe(r, ch, name, access)
	}
	if disp > smbSrvDispOverwriteIf || opts&smb2OptDirectory != 0 && opts&smb2OptNonDirectory != 0 {
		return smb2StatusInvalidParameter, nil
	}
	rel, st := smbSrvCleanPath(name)
	if st != 0 {
		return st, nil
	}
	ro := c.srv.ReadOnly()
	explicit := smbSrvMapAccess(access&^smbSrvAccessMaxAllowed, ro)
	granted := smbSrvMapAccess(access, ro)
	if ro && (explicit&smbSrvAccessWriteMask != 0 || opts&smbSrvOptDeleteOnClose != 0 ||
		disp != smbSrvDispOpen && disp != smbSrvDispOpenIf) {
		return smb2StatusAccessDenied, nil
	}
	// Delete-on-close needs DELETE access (MS-FSA 2.1.5.1), as SET_INFO's
	// disposition does.
	if opts&smbSrvOptDeleteOnClose != 0 && granted&smbSrvAccessDelete == 0 {
		return smb2StatusAccessDenied, nil
	}
	p, exists, parentOK := smbSrvResolve(c.root, rel)
	if !parentOK {
		return smb2StatusObjectPathNotFound, nil
	}
	var fi os.FileInfo
	if exists {
		var err error
		if fi, err = c.root.Stat(p); err != nil {
			return smbSrvErrStatus(err), nil
		}
	}
	switch disp {
	case smbSrvDispOpen, smbSrvDispOverwrite:
		if !exists {
			return smb2StatusObjectNameNotFound, nil
		}
	case smbSrvDispCreate:
		if exists {
			return smbSrvStatusNameCollision, nil
		}
	}
	h := &smbSrvHandle{sess: r.sess.id, tree: r.tree.id, rel: p, access: granted}
	action := uint32(1) // FILE_OPENED
	switch {
	case exists && fi.IsDir():
		if opts&smb2OptNonDirectory != 0 {
			return smb2StatusFileIsADirectory, nil
		}
		if disp != smbSrvDispOpen && disp != smbSrvDispOpenIf {
			return smb2StatusInvalidParameter, nil
		}
		h.isDir = true
	case exists:
		if opts&smb2OptDirectory != 0 {
			return smb2StatusNotADirectory, nil
		}
		flag := os.O_RDONLY
		if granted&(smbSrvAccessWriteData|smbSrvAccessAppend) != 0 {
			flag = os.O_RDWR
		}
		switch disp {
		case smbSrvDispSupersede:
			flag, action = os.O_RDWR|os.O_TRUNC, 0 // FILE_SUPERSEDED
		case smbSrvDispOverwrite, smbSrvDispOverwriteIf:
			flag, action = os.O_RDWR|os.O_TRUNC, 3 // FILE_OVERWRITTEN
		}
		f, err := c.root.OpenFile(p, flag, 0)
		if err != nil && flag == os.O_RDWR && explicit&(smbSrvAccessWriteData|smbSrvAccessAppend) == 0 {
			// Only MAXIMUM_ALLOWED asked for writing: settle for reading.
			f, err = c.root.OpenFile(p, os.O_RDONLY, 0)
			h.access &^= smbSrvAccessWriteMask
		}
		if err != nil {
			return smbSrvErrStatus(err), nil
		}
		h.f = f
	default:
		if ro || rel == "." {
			return smb2StatusAccessDenied, nil
		}
		if opts&smb2OptDirectory != 0 {
			if err := c.root.Mkdir(p, 0o755); err != nil {
				return smbSrvErrStatus(err), nil
			}
			h.isDir = true
		} else {
			f, err := c.root.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				return smbSrvErrStatus(err), nil
			}
			h.f = f
		}
		action = 2 // FILE_CREATED
	}
	h.delOnClose = opts&smbSrvOptDeleteOnClose != 0
	stat := c.statHandle(h)
	c.addHandle(h, ch)
	mxac, qfid := smbSrvCreateContexts(r.msg, binary.LittleEndian.Uint32(b[48:]), binary.LittleEndian.Uint32(b[52:]))
	return smb2StatusSuccess, smbSrvCreateBody(h.id, action, stat, mxac, qfid, granted)
}

// statHandle reads a handle's file (zero for a pipe or a vanished file).
func (c *smbSrvConn) statHandle(h *smbSrvHandle) smbSrvStat {
	if h.pipe != nil {
		return smbSrvStat{attrs: smbSrvAttrNormal}
	}
	var fi os.FileInfo
	var err error
	if h.f != nil {
		fi, err = h.f.Stat()
	} else {
		fi, err = c.root.Stat(h.rel)
	}
	if err != nil {
		return smbSrvStat{id: smbSrvFileID(h.rel), dir: h.isDir, attrs: smbSrvAttrs(path.Base(h.rel), h.isDir)}
	}
	return smbSrvStatOf(h.rel, fi)
}

// smbSrvCreateContexts reports whether the client asked for the maximal
// access (MxAc) and the on-disk id (QFid); other contexts (durable handles,
// leases, Apple's AAPL) are ignored, which MS-SMB2 allows.
func smbSrvCreateContexts(msg []byte, off, n uint32) (mxac, qfid bool) {
	buf, ok := smbSrvSlice(msg, uint64(off), uint64(n))
	if !ok {
		return false, false
	}
	for p := 0; len(buf)-p >= 16; {
		e := buf[p:]
		next := int(binary.LittleEndian.Uint32(e[0:]))
		nameOff := int(binary.LittleEndian.Uint16(e[4:]))
		if binary.LittleEndian.Uint16(e[6:]) == 4 && nameOff+4 <= len(e) {
			switch string(e[nameOff : nameOff+4]) {
			case "MxAc":
				mxac = true
			case "QFid":
				qfid = true
			}
		}
		if next <= 0 || next%8 != 0 || next > len(e) {
			break
		}
		p += next
	}
	return mxac, qfid
}

// smbSrvAppendCtx appends one create context to a reply's list.
func smbSrvAppendCtx(dst []byte, name string, data []byte, last bool) []byte {
	e := make([]byte, 24, 24+len(data)+8)
	binary.LittleEndian.PutUint16(e[4:], 16)
	binary.LittleEndian.PutUint16(e[6:], 4)
	binary.LittleEndian.PutUint16(e[10:], 24)
	binary.LittleEndian.PutUint32(e[12:], uint32(len(data)))
	copy(e[16:20], name)
	e = append(e, data...)
	if !last {
		e = smb2Pad8(e)
		binary.LittleEndian.PutUint32(e[0:], uint32(len(e)))
	}
	return append(dst, e...)
}

// smbSrvCreateBody is the CREATE reply (OplockLevel NONE).
func smbSrvCreateBody(id uint64, action uint32, st smbSrvStat, mxac, qfid bool, maxAccess uint32) []byte {
	b := make([]byte, 88, 160)
	binary.LittleEndian.PutUint16(b, 89)
	binary.LittleEndian.PutUint32(b[4:], action)
	ft := smbSrvFileTime(st.mtime)
	for i := 8; i < 40; i += 8 {
		binary.LittleEndian.PutUint64(b[i:], ft)
	}
	if !st.dir {
		binary.LittleEndian.PutUint64(b[40:], smbSrvAlloc(st.size))
		binary.LittleEndian.PutUint64(b[48:], uint64(st.size))
	}
	binary.LittleEndian.PutUint32(b[56:], st.attrs)
	binary.LittleEndian.PutUint64(b[64:], id)
	binary.LittleEndian.PutUint64(b[72:], id)
	var ctx []byte
	if mxac {
		data := make([]byte, 8)
		binary.LittleEndian.PutUint32(data[4:], maxAccess)
		ctx = smbSrvAppendCtx(ctx, "MxAc", data, !qfid)
	}
	if qfid {
		data := make([]byte, 32)
		binary.LittleEndian.PutUint64(data, st.id)
		ctx = smbSrvAppendCtx(ctx, "QFid", data, true)
	}
	if len(ctx) == 0 {
		return append(b, 0)
	}
	binary.LittleEndian.PutUint32(b[80:], smb2HeaderLen+88)
	binary.LittleEndian.PutUint32(b[84:], uint32(len(ctx)))
	return append(b, ctx...)
}

// ── CLOSE / FLUSH / LOCK ──────────────────────────────────────────────────

func (c *smbSrvConn) closeReq(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	b := r.body()
	if len(b) < 24 {
		return smb2StatusInvalidParameter, nil
	}
	h, st := c.handleFor(r, ch, b[8:24])
	if st != 0 {
		return st, nil
	}
	rel, pipe := h.rel, h.pipe != nil
	c.closeHandle(h.id)
	out := make([]byte, 60)
	binary.LittleEndian.PutUint16(out, 60)
	if binary.LittleEndian.Uint16(b[2:])&1 != 0 && !pipe {
		if fi, err := c.root.Stat(rel); err == nil {
			s := smbSrvStatOf(rel, fi)
			binary.LittleEndian.PutUint16(out[2:], 1) // SMB2_CLOSE_FLAG_POSTQUERY_ATTRIB
			ft := smbSrvFileTime(s.mtime)
			for i := 8; i < 40; i += 8 {
				binary.LittleEndian.PutUint64(out[i:], ft)
			}
			if !s.dir {
				binary.LittleEndian.PutUint64(out[40:], smbSrvAlloc(s.size))
				binary.LittleEndian.PutUint64(out[48:], uint64(s.size))
			}
			binary.LittleEndian.PutUint32(out[56:], s.attrs)
		}
	}
	return smb2StatusSuccess, out
}

func (c *smbSrvConn) flush(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	b := r.body()
	if len(b) < 24 {
		return smb2StatusInvalidParameter, nil
	}
	h, st := c.handleFor(r, ch, b[8:24])
	if st != 0 {
		return st, nil
	}
	if h.f != nil && h.access&(smbSrvAccessWriteData|smbSrvAccessAppend) != 0 {
		if err := h.f.Sync(); err != nil {
			return smbSrvErrStatus(err), nil
		}
	}
	return smb2StatusSuccess, smbSrvU16Body(4, 4)
}

// lock grants every byte-range lock: nothing else on the device takes them.
func (c *smbSrvConn) lock(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	b := r.body()
	if len(b) < 24 {
		return smb2StatusInvalidParameter, nil
	}
	if _, st := c.handleFor(r, ch, b[8:24]); st != 0 {
		return st, nil
	}
	return smb2StatusSuccess, smbSrvU16Body(4, 4)
}

// ── READ / WRITE ──────────────────────────────────────────────────────────

func (c *smbSrvConn) read(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	b := r.body()
	if len(b) < 48 {
		return smb2StatusInvalidParameter, nil
	}
	n := binary.LittleEndian.Uint32(b[4:])
	off := binary.LittleEndian.Uint64(b[8:])
	h, st := c.handleFor(r, ch, b[16:32])
	if st != 0 {
		return st, nil
	}
	if n > smbSrvMaxIO || off > math.MaxInt64 {
		return smb2StatusInvalidParameter, nil
	}
	if h.pipe != nil {
		data, more := h.pipe.take(int(n))
		if len(data) == 0 {
			return smbSrvStatusPipeEmpty, nil
		}
		status := uint32(smb2StatusSuccess)
		if more {
			status = smb2StatusBufferOverflow
		}
		return status, smbSrvReadBody(data)
	}
	if h.isDir || h.f == nil {
		return smbSrvStatusInvalidDevRequest, nil
	}
	if h.access&(smbSrvAccessReadData|smbSrvAccessExecute) == 0 {
		return smb2StatusAccessDenied, nil
	}
	// One spare byte of capacity: a zero-length READ answers buf[:17].
	buf := make([]byte, 16+int(n), 17+int(n))
	got, err := h.f.ReadAt(buf[16:], int64(off))
	if got == 0 && n > 0 {
		if err == nil || errors.Is(err, io.EOF) {
			return smb2StatusEndOfFile, nil
		}
		return smbSrvErrStatus(err), nil
	}
	h.pos = int64(off) + int64(got)
	c.track(h, "downloading ")
	binary.LittleEndian.PutUint16(buf, 17)
	buf[2] = smb2HeaderLen + 16
	binary.LittleEndian.PutUint32(buf[4:], uint32(got))
	if got == 0 {
		return smb2StatusSuccess, buf[:17]
	}
	return smb2StatusSuccess, buf[:16+got]
}

// smbSrvReadBody is a READ reply around data.
func smbSrvReadBody(data []byte) []byte {
	b := make([]byte, 16, 16+len(data))
	binary.LittleEndian.PutUint16(b, 17)
	b[2] = smb2HeaderLen + 16
	binary.LittleEndian.PutUint32(b[4:], uint32(len(data)))
	return append(b, data...)
}

// track shows what a connection moves on the Network tools sheet.
func (c *smbSrvConn) track(h *smbSrvHandle, verb string) {
	if h.what == "" {
		h.what = verb + h.rel
		c.srv.clients.setTransfer(c.clientID, h.what)
	}
}

func (c *smbSrvConn) write(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	b := r.body()
	if len(b) < 48 {
		return smb2StatusInvalidParameter, nil
	}
	n := binary.LittleEndian.Uint32(b[4:])
	off := binary.LittleEndian.Uint64(b[8:])
	h, st := c.handleFor(r, ch, b[16:32])
	if st != 0 {
		return st, nil
	}
	data, ok := smbSrvSlice(r.msg, uint64(binary.LittleEndian.Uint16(b[2:])), uint64(n))
	if !ok || n > smbSrvMaxIO {
		return smb2StatusInvalidParameter, nil
	}
	if h.pipe != nil {
		h.pipe.put(data)
		return smb2StatusSuccess, smbSrvWriteBody(len(data))
	}
	if h.isDir || h.f == nil {
		return smbSrvStatusInvalidDevRequest, nil
	}
	if c.srv.ReadOnly() || h.access&(smbSrvAccessWriteData|smbSrvAccessAppend) == 0 {
		return smb2StatusAccessDenied, nil
	}
	if off == math.MaxUint64 {
		fi, err := h.f.Stat()
		if err != nil {
			return smbSrvErrStatus(err), nil
		}
		off = uint64(fi.Size())
	}
	if off > math.MaxInt64-uint64(n) {
		return smb2StatusInvalidParameter, nil
	}
	got, err := h.f.WriteAt(data, int64(off))
	if err != nil {
		return smbSrvErrStatus(err), nil
	}
	h.pos = int64(off) + int64(got)
	c.track(h, "uploading ")
	return smb2StatusSuccess, smbSrvWriteBody(got)
}

// smbSrvWriteBody is a WRITE reply for n bytes.
func smbSrvWriteBody(n int) []byte {
	b := make([]byte, 17)
	binary.LittleEndian.PutUint16(b, 17)
	binary.LittleEndian.PutUint32(b[4:], uint32(n))
	return b
}

// ── QUERY_DIRECTORY ───────────────────────────────────────────────────────

// smbSrvListDir lists the entries of dir that match pattern ("." and ".."
// first for a wildcard, as Windows servers do). Names no Windows path could
// address are left out.
func smbSrvListDir(root *os.Root, dir, pattern string) ([]smbSrvDirEnt, error) {
	d, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	des, err := d.ReadDir(-1)
	if err != nil && len(des) == 0 {
		return nil, err
	}
	var out []smbSrvDirEnt
	if strings.ContainsAny(pattern, "*<") && smbSrvMatch(pattern, ".") {
		if fi, err := d.Stat(); err == nil {
			self := smbSrvStatOf(dir, fi)
			out = append(out, smbSrvDirEnt{name: ".", st: self}, smbSrvDirEnt{name: "..", st: self})
		}
	}
	for _, de := range des {
		name := de.Name()
		if !smbSrvNameOK(name) || !smbSrvMatch(pattern, name) {
			continue
		}
		rel := smbSrvJoin(dir, name)
		fi, err := de.Info()
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			fi, err = root.Stat(rel)
		}
		if err != nil {
			continue
		}
		out = append(out, smbSrvDirEnt{name: name, st: smbSrvStatOf(rel, fi)})
		if len(out) >= smbSrvMaxDirEntries {
			break
		}
	}
	return out, nil
}

// smbSrvMatch reports whether name matches the Windows pattern pat without
// regard to case: * and ?, and the DOS forms < > " (MS-FSA 2.1.4.4) read
// as *, ? and a dot. "*.*" is everything.
func smbSrvMatch(pat, name string) bool {
	if pat == "" || pat == "*" || pat == "<" || pat == "*.*" {
		return true
	}
	p := []rune(strings.ToUpper(pat))
	n := []rune(strings.ToUpper(name))
	pi, ni, star, mark := 0, 0, -1, 0
	for ni < len(n) {
		if pi < len(p) {
			switch p[pi] {
			case '*', '<':
				star, mark = pi, ni
				pi++
				continue
			case '?', '>':
				pi++
				ni++
				continue
			case '"':
				if n[ni] == '.' {
					pi++
					ni++
					continue
				}
			default:
				if p[pi] == n[ni] {
					pi++
					ni++
					continue
				}
			}
		}
		if star < 0 {
			return false
		}
		pi = star + 1
		mark++
		ni = mark
	}
	for pi < len(p) && strings.ContainsRune("*<>\"", p[pi]) {
		pi++
	}
	return pi == len(p)
}

func (c *smbSrvConn) queryDirectory(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	b := r.body()
	if len(b) < 32 {
		return smb2StatusInvalidParameter, nil
	}
	class, flags := b[2], b[3]
	h, st := c.handleFor(r, ch, b[8:24])
	if st != 0 {
		return st, nil
	}
	raw, ok := smbSrvSlice(r.msg, uint64(binary.LittleEndian.Uint16(b[24:])), uint64(binary.LittleEndian.Uint16(b[26:])))
	if !ok {
		return smb2StatusInvalidParameter, nil
	}
	if !h.isDir || h.pipe != nil {
		return smb2StatusInvalidParameter, nil
	}
	if smbSrvDirEntry(class, smbSrvDirEnt{name: "x"}) == nil {
		return smbSrvStatusInvalidInfoClass, nil
	}
	maxOut := int(min(binary.LittleEndian.Uint32(b[28:]), smbSrvMaxIO))
	if h.scan == nil || flags&(0x01|0x10) != 0 { // RESTART_SCANS, REOPEN
		pattern := smb2FromUTF16(raw)
		if pattern == "" {
			pattern = "*"
		}
		ents, err := smbSrvListDir(c.root, h.rel, pattern)
		if err != nil {
			return smbSrvErrStatus(err), nil
		}
		h.scan = &smbSrvDirScan{ents: ents}
	}
	sc := h.scan
	if sc.pos >= len(sc.ents) {
		if !sc.sent {
			sc.sent = true
			return smb2StatusNoSuchFile, nil
		}
		return smb2StatusNoMoreFiles, nil
	}
	out := make([]byte, 0, min(maxOut, 16<<10))
	last := -1
	for sc.pos < len(sc.ents) {
		e := smbSrvDirEntry(class, sc.ents[sc.pos])
		start := (len(out) + 7) &^ 7
		if start+len(e) > maxOut {
			break
		}
		for len(out) < start {
			out = append(out, 0)
		}
		if last >= 0 {
			binary.LittleEndian.PutUint32(out[last:], uint32(start-last))
		}
		last = start
		out = append(out, e...)
		sc.pos++
		if flags&0x02 != 0 { // RETURN_SINGLE_ENTRY
			break
		}
	}
	if last < 0 {
		return smbSrvStatusInfoLengthMis, nil
	}
	sc.sent = true
	body := make([]byte, 8, 8+len(out))
	binary.LittleEndian.PutUint16(body, 9)
	binary.LittleEndian.PutUint16(body[2:], smb2HeaderLen+8)
	binary.LittleEndian.PutUint32(body[4:], uint32(len(out)))
	return smb2StatusSuccess, append(body, out...)
}

// ── SET_INFO ──────────────────────────────────────────────────────────────

func (c *smbSrvConn) setInfo(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	b := r.body()
	if len(b) < 32 {
		return smb2StatusInvalidParameter, nil
	}
	infoType, class := b[2], b[3]
	buf, ok := smbSrvSlice(r.msg, uint64(binary.LittleEndian.Uint16(b[8:])), uint64(binary.LittleEndian.Uint32(b[4:])))
	if !ok {
		return smb2StatusInvalidParameter, nil
	}
	h, st := c.handleFor(r, ch, b[16:32])
	if st != 0 {
		return st, nil
	}
	done := smbSrvU16Body(2, 2)
	switch {
	case infoType == 3 || h.pipe != nil:
		return smb2StatusSuccess, done // security and pipe modes: accepted, nothing to keep
	case infoType != 1:
		return smb2StatusNotSupported, nil
	}
	ro := c.srv.ReadOnly()
	switch class {
	case 4: // FileBasicInformation
		if len(buf) < 36 {
			return smbSrvStatusInfoLengthMis, nil
		}
		if ro {
			return smb2StatusAccessDenied, nil
		}
		at := smb2FileTime(binary.LittleEndian.Uint64(buf[8:]))
		mt := smb2FileTime(binary.LittleEndian.Uint64(buf[16:]))
		if !at.IsZero() || !mt.IsZero() {
			if err := c.root.Chtimes(h.rel, at, mt); err != nil {
				return smbSrvErrStatus(err), nil
			}
		}
	case 10: // FileRenameInformation
		if st := c.rename(h, buf); st != 0 {
			return st, nil
		}
	case 13: // FileDispositionInformation
		if len(buf) < 1 {
			return smbSrvStatusInfoLengthMis, nil
		}
		if st := c.setDelete(h, buf[0] != 0); st != 0 {
			return st, nil
		}
	case 64: // FileDispositionInformationEx
		if len(buf) < 4 {
			return smbSrvStatusInfoLengthMis, nil
		}
		if st := c.setDelete(h, binary.LittleEndian.Uint32(buf)&1 != 0); st != 0 {
			return st, nil
		}
	case 20: // FileEndOfFileInformation
		if len(buf) < 8 {
			return smbSrvStatusInfoLengthMis, nil
		}
		size := binary.LittleEndian.Uint64(buf)
		switch {
		case h.f == nil:
			return smb2StatusInvalidParameter, nil
		case ro || h.access&(smbSrvAccessWriteData|smbSrvAccessAppend) == 0:
			return smb2StatusAccessDenied, nil
		case size > math.MaxInt64:
			return smb2StatusInvalidParameter, nil
		}
		if err := h.f.Truncate(int64(size)); err != nil {
			return smbSrvErrStatus(err), nil
		}
	case 14: // FilePositionInformation
		if len(buf) < 8 {
			return smbSrvStatusInfoLengthMis, nil
		}
		h.pos = int64(binary.LittleEndian.Uint64(buf) & math.MaxInt64)
	case 19, 16: // FileAllocationInformation, FileModeInformation: nothing to do
	default:
		return smb2StatusNotSupported, nil
	}
	return smb2StatusSuccess, done
}

// setDelete marks a handle delete-on-close (or clears it). A folder must be
// empty.
func (c *smbSrvConn) setDelete(h *smbSrvHandle, on bool) uint32 {
	if !on {
		h.delOnClose = false
		return 0
	}
	if c.srv.ReadOnly() || h.access&smbSrvAccessDelete == 0 || h.rel == "." {
		return smb2StatusAccessDenied
	}
	if h.isDir {
		d, err := c.root.Open(h.rel)
		if err != nil {
			return smbSrvErrStatus(err)
		}
		names, _ := d.Readdirnames(1)
		_ = d.Close()
		if len(names) > 0 {
			return smbSrvStatusDirNotEmpty
		}
	}
	h.delOnClose = true
	return 0
}

// rename moves a handle's file (FILE_RENAME_INFORMATION_TYPE_2: the new name
// is a path from the share root). A case-only rename of the same file is
// allowed; another existing target needs ReplaceIfExists and must not be a
// folder.
func (c *smbSrvConn) rename(h *smbSrvHandle, buf []byte) uint32 {
	if c.srv.ReadOnly() || h.access&smbSrvAccessDelete == 0 || h.rel == "." {
		return smb2StatusAccessDenied
	}
	if len(buf) < 20 {
		return smbSrvStatusInfoLengthMis
	}
	n := uint64(binary.LittleEndian.Uint32(buf[16:]))
	if n == 0 || n%2 != 0 || n > uint64(len(buf)-20) {
		return smb2StatusInvalidParameter
	}
	newRel, st := smbSrvCleanPath(smb2FromUTF16(buf[20 : 20+n]))
	if st != 0 {
		return st
	}
	if newRel == "." {
		return smb2StatusAccessDenied
	}
	target, exists, parentOK := smbSrvResolve(c.root, newRel)
	if !parentOK {
		return smb2StatusObjectPathNotFound
	}
	dst := target
	switch {
	case exists && strings.EqualFold(target, h.rel):
		dst = smbSrvJoin(path.Dir(target), path.Base(newRel))
	case exists:
		if buf[0] == 0 {
			return smbSrvStatusNameCollision
		}
		if fi, err := c.root.Stat(target); err == nil && fi.IsDir() {
			return smb2StatusAccessDenied
		}
	}
	if dst == h.rel {
		return 0
	}
	if err := c.root.Rename(h.rel, dst); err != nil {
		return smbSrvErrStatus(err)
	}
	h.rel = dst
	return 0
}

// ── IOCTL ─────────────────────────────────────────────────────────────────

// ioctl answers DFS referral requests with "not found" (no DFS here) and
// runs srvsvc calls on the IPC$ pipe; every other control is not supported.
func (c *smbSrvConn) ioctl(r *smbSrvReq, ch *smbSrvChain) (uint32, []byte) {
	b := r.body()
	if len(b) < 56 {
		return smb2StatusInvalidParameter, nil
	}
	code := binary.LittleEndian.Uint32(b[4:])
	switch code {
	case smbSrvFsctlDfsReferrals, smbSrvFsctlDfsReferralsEx:
		return smbSrvStatusNotFound, nil
	case smbSrvFsctlPipeTransceive:
	default:
		return smb2StatusNotSupported, nil
	}
	h, st := c.handleFor(r, ch, b[8:24])
	if st != 0 {
		return st, nil
	}
	if h.pipe == nil {
		return smbSrvStatusInvalidDevRequest, nil
	}
	in, ok := smbSrvSlice(r.msg, uint64(binary.LittleEndian.Uint32(b[24:])), uint64(binary.LittleEndian.Uint32(b[28:])))
	if !ok {
		return smb2StatusInvalidParameter, nil
	}
	h.pipe.put(in)
	out, more := h.pipe.take(int(min(binary.LittleEndian.Uint32(b[44:]), smbSrvMaxIO)))
	body := make([]byte, 48, 49+len(out))
	binary.LittleEndian.PutUint16(body, 49)
	binary.LittleEndian.PutUint32(body[4:], code)
	binary.LittleEndian.PutUint64(body[8:], h.id)
	binary.LittleEndian.PutUint64(body[16:], h.id)
	binary.LittleEndian.PutUint32(body[24:], smb2HeaderLen+48)
	binary.LittleEndian.PutUint32(body[32:], smb2HeaderLen+48)
	binary.LittleEndian.PutUint32(body[36:], uint32(len(out)))
	body = append(body, out...)
	if len(out) == 0 {
		body = append(body, 0)
	}
	if more {
		return smb2StatusBufferOverflow, body
	}
	return smb2StatusSuccess, body
}
