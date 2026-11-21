package main

// smbserve_conn.go — one SMB connection of the share (smbserve.go): direct
// TCP framing, the 64-byte SMB2 header, compound chains, credits, signing,
// and the commands that are not about files: NEGOTIATE (also the SMB1
// "SMB 2.???" probe), SESSION_SETUP (smbserve_ntlm.go), LOGOFF,
// TREE_CONNECT, TREE_DISCONNECT, ECHO and CANCEL. File commands are in
// smbserve_fs.go, QUERY_INFO in smbserve_info.go, the srvsvc pipe in
// smbserve_rpc.go.
//
// Every request is handled to the end before the next frame is read, so a
// connection needs no locks of its own. No frame over smbSrvMaxFrame is
// read, every length and offset a request carries is checked against the
// message (smbSrvSlice), and anything malformed ends the connection.

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

const (
	smbSrvMaxFrame    = 1 << 20
	smbSrvMaxIO       = 65536
	smbSrvMaxCredits  = 512
	smbSrvMaxChain    = 32
	smbSrvMaxSessions = 4
	smbSrvMaxTrees    = 16
	smbSrvMaxHandles  = 512 // per connection: 4 × 512 open files stay under a 4096 fd limit
	smbSrvMaxBadSigs  = 3
)

// Commands the client (smb2_proto.go) never sends.
const (
	smbSrvCmdFlush        = 0x07
	smbSrvCmdLock         = 0x0a
	smbSrvCmdIoctl        = 0x0b
	smbSrvCmdCancel       = 0x0c
	smbSrvCmdEcho         = 0x0d
	smbSrvCmdChangeNotify = 0x0f
	smbSrvCmdQueryInfo    = 0x10
	smbSrvCmdSetInfo      = 0x11
	smbSrvCmdOplockBreak  = 0x12
)

const (
	smbSrvFlagRelated = 0x00000004
	smbSrvDialectAny  = 0x02ff
)

// NTSTATUS codes the client has no name for (MS-ERREF 2.3).
const (
	smbSrvStatusUnsuccessful      = 0xc0000001
	smbSrvStatusInvalidInfoClass  = 0xc0000003
	smbSrvStatusInfoLengthMis     = 0xc0000004
	smbSrvStatusInvalidDevRequest = 0xc0000010
	smbSrvStatusBufferTooSmall    = 0xc0000023
	smbSrvStatusNameCollision     = 0xc0000035
	smbSrvStatusDiskFull          = 0xc000007f
	smbSrvStatusPipeEmpty         = 0xc00000d9
	smbSrvStatusDirNotEmpty       = 0xc0000101
	smbSrvStatusFileClosed        = 0xc0000128
	smbSrvStatusNotFound          = 0xc0000225
)

// smbSrvConn is one client connection.
type smbSrvConn struct {
	srv      *smbService
	nc       net.Conn
	br       *bufio.Reader
	root     *os.Root
	ip       string
	clientID uint64
	nbName   string
	guid     [16]byte

	negotiated bool
	dialect    uint16
	credits    int
	badSigs    int
	sessions   map[uint64]*smbSrvSession
	handles    map[uint64]*smbSrvHandle
	nextHandle uint64
}

// smbSrvSession is one sign-in on a connection.
type smbSrvSession struct {
	id       uint64
	ntlm     *smbSrvNtlm
	signer   *smb2Signer
	valid    bool
	user     string
	trees    map[uint32]*smbSrvTree
	nextTree uint32
}

// smbSrvTree is one connected share: the SD card or IPC$.
type smbSrvTree struct {
	id  uint32
	ipc bool
}

// smbSrvReq is one request of a frame (a compound chain has several).
type smbSrvReq struct {
	msg     []byte
	cmd     uint16
	flags   uint32
	mid     uint64
	pid     uint32
	charge  uint16
	credReq uint16
	treeID  uint32
	sessID  uint64
	related bool
	sess    *smbSrvSession
	tree    *smbSrvTree
}

func (r *smbSrvReq) body() []byte { return r.msg[smb2HeaderLen:] }

// smbSrvOut is one response before the chain is put together.
type smbSrvOut struct {
	req     *smbSrvReq
	status  uint32
	body    []byte
	treeID  uint32
	sessID  uint64
	credits uint16
	signer  *smb2Signer
}

// smbSrvChain carries what related requests take from the one before:
// session, tree and the handle the last CREATE opened (or why it failed).
type smbSrvChain struct {
	sessID    uint64
	treeID    uint32
	fileID    uint64
	haveFile  bool
	createErr uint32
}

func newSmbSrvConn(s *smbService, nc net.Conn, root *os.Root, ip string, id uint64, name string, guid [16]byte) *smbSrvConn {
	return &smbSrvConn{
		srv: s, nc: nc, br: bufio.NewReaderSize(nc, 64<<10), root: root, ip: ip, clientID: id,
		nbName: name, guid: guid, credits: 1,
		sessions: map[uint64]*smbSrvSession{}, handles: map[uint64]*smbSrvHandle{},
	}
}

// serve reads and answers frames until the client leaves or misbehaves.
func (c *smbSrvConn) serve() {
	defer c.closeAll()
	signedIn := time.Now().Add(smbSrvLoginWait)
	for {
		wait := smbSrvIdleTimeout
		if !c.anyValidSession() {
			wait = time.Until(signedIn)
			if wait <= 0 {
				return
			}
		}
		_ = c.nc.SetReadDeadline(time.Now().Add(wait))
		frame, err := smbSrvReadFrame(c.br)
		if err != nil {
			return
		}
		out, keep := c.handleFrame(frame)
		if len(out) > 0 {
			if len(out) > 0xffffff {
				return
			}
			pfx := []byte{0, byte(len(out) >> 16), byte(len(out) >> 8), byte(len(out))}
			_ = c.nc.SetWriteDeadline(time.Now().Add(smbSrvWriteWait))
			bufs := net.Buffers{pfx, out}
			if _, err := bufs.WriteTo(c.nc); err != nil {
				return
			}
		}
		if !keep {
			return
		}
	}
}

func (c *smbSrvConn) anyValidSession() bool {
	for _, s := range c.sessions {
		if s.valid {
			return true
		}
	}
	return false
}

// closeAll closes every handle the connection left open (delete-on-close
// included, as Windows does when a client vanishes).
func (c *smbSrvConn) closeAll() {
	for id := range c.handles {
		c.closeHandle(id)
	}
}

// smbSrvReadFrame reads one direct-TCP frame (NetBIOS keep-alives are
// skipped). A frame over smbSrvMaxFrame or of another NetBIOS type is an
// error.
func smbSrvReadFrame(r io.Reader) ([]byte, error) {
	var pfx [4]byte
	for {
		if _, err := io.ReadFull(r, pfx[:]); err != nil {
			return nil, err
		}
		n := int(pfx[1])<<16 | int(pfx[2])<<8 | int(pfx[3])
		if pfx[0] == 0x85 && n == 0 {
			continue
		}
		if pfx[0] != 0 || n < 4 || n > smbSrvMaxFrame {
			return nil, smb2Bad("frame length")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
}

// smbSrvSlice returns msg[off:off+n] when that lies inside the message
// after the header; offsets count from the header, as MS-SMB2 has them.
func smbSrvSlice(msg []byte, off, n uint64) ([]byte, bool) {
	if n == 0 {
		return nil, true
	}
	if off < smb2HeaderLen || off > uint64(len(msg)) || n > uint64(len(msg))-off {
		return nil, false
	}
	return msg[off : off+n], true
}

// smbSrvParseHeader reads an SMB2 request header. msg is this request only.
func smbSrvParseHeader(msg []byte) (*smbSrvReq, bool) {
	if len(msg) < smb2HeaderLen+2 || string(msg[:4]) != "\xfeSMB" ||
		binary.LittleEndian.Uint16(msg[4:]) != smb2HeaderLen {
		return nil, false
	}
	flags := binary.LittleEndian.Uint32(msg[16:])
	if flags&smb2FlagResponse != 0 {
		return nil, false
	}
	r := &smbSrvReq{
		msg:     msg,
		cmd:     binary.LittleEndian.Uint16(msg[12:]),
		flags:   flags,
		mid:     binary.LittleEndian.Uint64(msg[24:]),
		pid:     binary.LittleEndian.Uint32(msg[32:]),
		charge:  binary.LittleEndian.Uint16(msg[6:]),
		credReq: binary.LittleEndian.Uint16(msg[14:]),
		treeID:  binary.LittleEndian.Uint32(msg[36:]),
		sessID:  binary.LittleEndian.Uint64(msg[40:]),
		related: flags&smbSrvFlagRelated != 0,
	}
	if flags&smb2FlagAsync != 0 {
		r.treeID = 0 // AsyncId sits where Reserved and TreeId are
	}
	return r, true
}

// smbSrvSplitChain cuts a frame into its compound requests. Each one runs
// to its NextCommand (padding included, which its signature covers); a
// NextCommand that is not 8-aligned or leaves the frame ends the connection.
func smbSrvSplitChain(frame []byte) ([][]byte, bool) {
	var msgs [][]byte
	off := 0
	for {
		if len(frame)-off < smb2HeaderLen || len(msgs) >= smbSrvMaxChain {
			return nil, false
		}
		next := binary.LittleEndian.Uint32(frame[off+20:])
		if next == 0 {
			return append(msgs, frame[off:]), true
		}
		if next < smb2HeaderLen || next%8 != 0 || uint64(next) > uint64(len(frame)-off) {
			return nil, false
		}
		msgs = append(msgs, frame[off:off+int(next)])
		off += int(next)
	}
}

// handleFrame answers one frame; keep is false when the connection must end.
func (c *smbSrvConn) handleFrame(frame []byte) (out []byte, keep bool) {
	switch {
	case len(frame) >= 4 && string(frame[:4]) == "\xffSMB":
		return c.smb1Negotiate(frame)
	case len(frame) < 4 || string(frame[:4]) != "\xfeSMB":
		return nil, false // SMB1 commands, TRANSFORM (no encryption here)
	}
	msgs, ok := smbSrvSplitChain(frame)
	if !ok {
		return nil, false
	}
	var ch smbSrvChain
	outs := make([]smbSrvOut, 0, len(msgs))
	for _, m := range msgs {
		r, ok := smbSrvParseHeader(m)
		if !ok || r.mid == ^uint64(0) {
			return nil, false
		}
		if r.related {
			r.sessID, r.treeID = ch.sessID, ch.treeID
		}
		o, keep := c.handleOne(r, &ch)
		if !keep {
			return nil, false
		}
		ch.sessID, ch.treeID = o.sessID, o.treeID
		if r.cmd == smbSrvCmdCancel {
			continue // CANCEL has no response and takes no credit
		}
		o.credits = c.grant(r.charge, r.credReq)
		outs = append(outs, o)
	}
	return c.assemble(outs), true
}

// grant spends a request's credits and grants max(1, requested), keeping at
// most smbSrvMaxCredits outstanding.
func (c *smbSrvConn) grant(charge, want uint16) uint16 {
	c.credits -= max(int(charge), 1)
	if c.credits < 0 {
		c.credits = 0
	}
	g := min(max(int(want), 1), smbSrvMaxCredits-c.credits)
	g = max(g, 1)
	c.credits += g
	return uint16(g)
}

// assemble builds the response frame: headers, 8-byte padding between
// compound responses, NextCommand, then each response signed over its own
// bytes (padding included).
func (c *smbSrvConn) assemble(outs []smbSrvOut) []byte {
	if len(outs) == 0 {
		return nil
	}
	var frame []byte
	for i, o := range outs {
		start := len(frame)
		h := make([]byte, smb2HeaderLen)
		copy(h, "\xfeSMB")
		binary.LittleEndian.PutUint16(h[4:], smb2HeaderLen)
		binary.LittleEndian.PutUint16(h[6:], o.req.charge)
		binary.LittleEndian.PutUint32(h[8:], o.status)
		binary.LittleEndian.PutUint16(h[12:], o.req.cmd)
		binary.LittleEndian.PutUint16(h[14:], o.credits)
		binary.LittleEndian.PutUint32(h[16:], smb2FlagResponse|o.req.flags&smbSrvFlagRelated)
		binary.LittleEndian.PutUint64(h[24:], o.req.mid)
		binary.LittleEndian.PutUint32(h[32:], o.req.pid)
		binary.LittleEndian.PutUint32(h[36:], o.treeID)
		binary.LittleEndian.PutUint64(h[40:], o.sessID)
		frame = append(frame, h...)
		body := o.body
		if body == nil {
			body = smbSrvErrorBody(nil)
		}
		frame = append(frame, body...)
		if i < len(outs)-1 {
			for (len(frame)-start)%8 != 0 {
				frame = append(frame, 0)
			}
			binary.LittleEndian.PutUint32(frame[start+20:], uint32(len(frame)-start))
		}
		if o.signer != nil {
			o.signer.sign(frame[start:])
		}
	}
	return frame
}

// smbSrvErrorBody is the ERROR response body (MS-SMB2 2.2.2) with optional
// error data (the size STATUS_BUFFER_TOO_SMALL asks for).
func smbSrvErrorBody(data []byte) []byte {
	b := make([]byte, 8, 9+len(data))
	binary.LittleEndian.PutUint16(b, 9)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(data)))
	if len(data) == 0 {
		return append(b, 0)
	}
	return append(b, data...)
}

// smbSrvU16Body is the fixed little bodies (ECHO, LOGOFF, FLUSH, LOCK,
// TREE_DISCONNECT replies are StructureSize 4; SET_INFO's is 2).
func smbSrvU16Body(size uint16, n int) []byte {
	b := make([]byte, n)
	binary.LittleEndian.PutUint16(b, size)
	return b
}

// handleOne answers one request of a chain.
func (c *smbSrvConn) handleOne(r *smbSrvReq, ch *smbSrvChain) (smbSrvOut, bool) {
	o := smbSrvOut{req: r, treeID: r.treeID, sessID: r.sessID}
	fail := func(status uint32) (smbSrvOut, bool) {
		o.status = status
		o.body = nil
		return o, true
	}
	switch r.cmd {
	case smb2CmdNegotiate:
		if c.negotiated {
			return o, false
		}
		o.status, o.body = c.negotiate(r)
		return o, true
	case smbSrvCmdCancel:
		return o, true
	}
	if !c.negotiated {
		return o, false
	}
	if r.cmd == smb2CmdSessionSetup {
		return c.sessionSetup(r)
	}
	if r.cmd == smbSrvCmdEcho && r.sessID == 0 {
		o.body = smbSrvU16Body(4, 4)
		return o, true
	}
	s := c.sessions[r.sessID]
	if s == nil || !s.valid {
		return fail(smb2StatusUserSessionDeleted)
	}
	r.sess = s
	if r.flags&smb2FlagSigned == 0 {
		return fail(smb2StatusAccessDenied)
	}
	if !s.signer.verify(r.msg) {
		c.badSigs++
		if c.badSigs >= smbSrvMaxBadSigs {
			return o, false
		}
		return fail(smb2StatusAccessDenied)
	}
	o.signer = s.signer
	if r.cmd != smbSrvCmdEcho {
		c.srv.activity.touch()
	}
	switch r.cmd {
	case smbSrvCmdEcho:
		o.body = smbSrvU16Body(4, 4)
		return o, true
	case smb2CmdLogoff:
		c.dropSession(s)
		o.body = smbSrvU16Body(4, 4)
		return o, true
	case smb2CmdTreeConnect:
		o.status, o.body, o.treeID = c.treeConnect(r)
		return o, true
	}
	t := s.trees[r.treeID]
	if t == nil {
		return fail(smb2StatusNetworkNameDeleted)
	}
	r.tree = t
	switch r.cmd {
	case smb2CmdTreeDisconnect:
		c.closeTreeHandles(s.id, t.id)
		delete(s.trees, t.id)
		o.body = smbSrvU16Body(4, 4)
	case smb2CmdCreate:
		o.status, o.body = c.create(r, ch)
	case smb2CmdClose:
		o.status, o.body = c.closeReq(r, ch)
	case smbSrvCmdFlush:
		o.status, o.body = c.flush(r, ch)
	case smb2CmdRead:
		o.status, o.body = c.read(r, ch)
	case smb2CmdWrite:
		o.status, o.body = c.write(r, ch)
	case smbSrvCmdLock:
		o.status, o.body = c.lock(r, ch)
	case smbSrvCmdIoctl:
		o.status, o.body = c.ioctl(r, ch)
	case smb2CmdQueryDirectory:
		o.status, o.body = c.queryDirectory(r, ch)
	case smbSrvCmdQueryInfo:
		o.status, o.body = c.queryInfo(r, ch)
	case smbSrvCmdSetInfo:
		o.status, o.body = c.setInfo(r, ch)
	case smbSrvCmdChangeNotify, smbSrvCmdOplockBreak:
		o.status = smb2StatusNotSupported
	default:
		o.status = smb2StatusNotSupported
	}
	if o.status != smb2StatusSuccess && o.status != smb2StatusBufferOverflow {
		if len(o.body) == 0 || o.status>>30 == 3 && o.status != smbSrvStatusBufferTooSmall {
			o.body = nil
		}
	}
	return o, true
}

// ── NEGOTIATE ─────────────────────────────────────────────────────────────

// smbSrvNegotiateBody is the NEGOTIATE reply for dialect (0x02FF answers an
// SMB1 multi-protocol request): signing enabled and required, no large MTU,
// 64 KiB reads, writes and transactions, and SPNEGO offering NTLMSSP.
func (c *smbSrvConn) smbSrvNegotiateBody(dialect uint16) []byte {
	tok := smbSrvNegotiateToken()
	b := make([]byte, 64, 64+len(tok))
	binary.LittleEndian.PutUint16(b[0:], 65)
	binary.LittleEndian.PutUint16(b[2:], smb2SecSigningEnabled|smb2SecSigningRequired)
	binary.LittleEndian.PutUint16(b[4:], dialect)
	copy(b[8:24], c.guid[:])
	binary.LittleEndian.PutUint32(b[28:], smbSrvMaxIO)
	binary.LittleEndian.PutUint32(b[32:], smbSrvMaxIO)
	binary.LittleEndian.PutUint32(b[36:], smbSrvMaxIO)
	binary.LittleEndian.PutUint64(b[40:], smb2FileTimeNow())
	binary.LittleEndian.PutUint16(b[56:], smb2HeaderLen+64)
	binary.LittleEndian.PutUint16(b[58:], uint16(len(tok)))
	return append(b, tok...)
}

// negotiate picks 2.1, else 2.0.2, from the client's dialects.
func (c *smbSrvConn) negotiate(r *smbSrvReq) (uint32, []byte) {
	b := r.body()
	if len(b) < 36 {
		return smb2StatusInvalidParameter, nil
	}
	count := int(binary.LittleEndian.Uint16(b[2:]))
	if count == 0 || 36+2*count > len(b) {
		return smb2StatusInvalidParameter, nil
	}
	var pick uint16
	for i := range count {
		switch d := binary.LittleEndian.Uint16(b[36+2*i:]); d {
		case smb2Dialect210:
			pick = d
		case smb2Dialect202:
			if pick == 0 {
				pick = d
			}
		}
	}
	if pick == 0 {
		return smb2StatusNotSupported, nil
	}
	c.negotiated, c.dialect = true, pick
	return smb2StatusSuccess, c.smbSrvNegotiateBody(pick)
}

// smb1Negotiate answers the SMB1 NEGOTIATE older clients open with: when
// it offers "SMB 2.???" the reply is an SMB2 NEGOTIATE for dialect 0x02FF
// and the client negotiates again in SMB2; "SMB 2.002" alone settles on
// 2.0.2. SMB1 itself is never spoken.
func (c *smbSrvConn) smb1Negotiate(frame []byte) ([]byte, bool) {
	if c.negotiated || len(frame) < 35 || frame[4] != 0x72 {
		return nil, false
	}
	wc := int(frame[32])
	at := 33 + 2*wc
	if at+2 > len(frame) {
		return nil, false
	}
	n := int(binary.LittleEndian.Uint16(frame[at:]))
	if at+2+n > len(frame) {
		return nil, false
	}
	wild, v202 := false, false
	for _, d := range strings.Split(string(frame[at+2:at+2+n]), "\x00") {
		switch strings.TrimPrefix(d, "\x02") {
		case "SMB 2.???":
			wild = true
		case "SMB 2.002":
			v202 = true
		}
	}
	dialect := uint16(smbSrvDialectAny)
	switch {
	case wild:
	case v202:
		dialect = smb2Dialect202
		c.negotiated, c.dialect = true, smb2Dialect202
	default:
		return nil, false
	}
	req := &smbSrvReq{cmd: smb2CmdNegotiate}
	o := smbSrvOut{req: req, body: c.smbSrvNegotiateBody(dialect), credits: c.grant(1, 1)}
	return c.assemble([]smbSrvOut{o}), true
}

// ── SESSION_SETUP / LOGOFF ────────────────────────────────────────────────

// smbSrvSessionBody is the SESSION_SETUP reply body.
func smbSrvSessionBody(tok []byte) []byte {
	b := make([]byte, 8, 9+len(tok))
	binary.LittleEndian.PutUint16(b, 9)
	if len(tok) == 0 {
		return append(b, 0)
	}
	binary.LittleEndian.PutUint16(b[4:], smb2HeaderLen+8)
	binary.LittleEndian.PutUint16(b[6:], uint16(len(tok)))
	return append(b, tok...)
}

// sessionSetup runs NTLM: NEGOTIATE gets a CHALLENGE (status MORE_PROCESSING
// _REQUIRED), AUTHENTICATE is checked against the share's account.
func (c *smbSrvConn) sessionSetup(r *smbSrvReq) (smbSrvOut, bool) {
	o := smbSrvOut{req: r, sessID: r.sessID}
	b := r.body()
	if len(b) < 24 {
		o.status = smb2StatusInvalidParameter
		return o, true
	}
	blob, ok := smbSrvSlice(r.msg, uint64(binary.LittleEndian.Uint16(b[12:])), uint64(binary.LittleEndian.Uint16(b[14:])))
	if !ok {
		o.status = smb2StatusInvalidParameter
		return o, true
	}
	var s *smbSrvSession
	if r.sessID == 0 {
		if len(c.sessions) >= smbSrvMaxSessions {
			o.status = smb2StatusRequestNotAccepted
			return o, true
		}
		s = &smbSrvSession{id: c.srv.sessSeq.Add(1), trees: map[uint32]*smbSrvTree{}}
		c.sessions[s.id] = s
	} else {
		s = c.sessions[r.sessID]
		if s == nil {
			o.status = smb2StatusUserSessionDeleted
			return o, true
		}
		if s.valid && (r.flags&smb2FlagSigned == 0 || !s.signer.verify(r.msg)) {
			o.status = smb2StatusAccessDenied
			return o, true
		}
	}
	o.sessID = s.id
	refuse := func() (smbSrvOut, bool) {
		s.ntlm = nil
		if !s.valid {
			delete(c.sessions, s.id)
		}
		o.status = smb2StatusLogonFailure
		return o, true
	}
	tok, spnego, err := smbSrvUnwrapToken(blob)
	if err != nil {
		return refuse()
	}
	switch smbSrvNtlmType(tok) {
	case 0:
		if !spnego {
			return refuse()
		}
		// Another mechanism's token: name NTLMSSP and wait for its NEGOTIATE.
		o.status = smb2StatusMoreProcessing
		o.body = smbSrvSessionBody(smbSrvSpnegoResp(1, true, nil))
		return o, true
	case 1:
		if len(tok) < 16 {
			return refuse()
		}
		n, err := newSmbSrvNtlm(c.nbName)
		if err != nil {
			return refuse()
		}
		s.ntlm = n
		chal := n.challengeMsg(binary.LittleEndian.Uint32(tok[12:]), smb2FileTimeNow())
		if spnego {
			chal = smbSrvSpnegoResp(1, true, chal)
		}
		o.status = smb2StatusMoreProcessing
		o.body = smbSrvSessionBody(chal)
		return o, true
	case 3:
		if s.ntlm == nil || c.srv.limiter.locked(c.ip) {
			return refuse()
		}
		auth, err := s.ntlm.verify(tok, smbServeUser, c.srv.Password())
		if err != nil {
			c.srv.limiter.fail(c.ip)
			logDebug("smb", "sign-in refused from %s", c.ip)
			time.Sleep(smbSrvFailDelay)
			return refuse()
		}
		c.srv.limiter.succeed(c.ip)
		s.ntlm = nil
		if !s.valid {
			signer, err := smb2NewSigner(c.dialect, auth.sessionKey[:min(len(auth.sessionKey), 16)], nil)
			if err != nil {
				return refuse()
			}
			s.signer, s.valid, s.user = signer, true, auth.user
		}
		c.srv.clients.setUser(c.clientID, auth.user)
		c.srv.activity.touch()
		var resp []byte
		if spnego {
			resp = smbSrvSpnegoResp(0, false, nil)
		}
		o.status = smb2StatusSuccess
		o.body = smbSrvSessionBody(resp)
		o.signer = s.signer
		return o, true
	}
	return refuse()
}

// dropSession ends a session and closes its handles.
func (c *smbSrvConn) dropSession(s *smbSrvSession) {
	for id, h := range c.handles {
		if h.sess == s.id {
			c.closeHandle(id)
		}
	}
	delete(c.sessions, s.id)
}

// ── TREE_CONNECT ──────────────────────────────────────────────────────────

// treeConnect connects \\server\JukaHub (the card) or \\server\IPC$.
func (c *smbSrvConn) treeConnect(r *smbSrvReq) (uint32, []byte, uint32) {
	b := r.body()
	if len(b) < 8 {
		return smb2StatusInvalidParameter, nil, 0
	}
	raw, ok := smbSrvSlice(r.msg, uint64(binary.LittleEndian.Uint16(b[4:])), uint64(binary.LittleEndian.Uint16(b[6:])))
	if !ok || len(raw) == 0 {
		return smb2StatusInvalidParameter, nil, 0
	}
	p := smb2FromUTF16(raw)
	share := p[strings.LastIndex(p, `\`)+1:]
	t := &smbSrvTree{}
	switch {
	case strings.EqualFold(share, smbServeShareName):
	case strings.EqualFold(share, "IPC$"):
		t.ipc = true
	default:
		return smb2StatusBadNetworkName, nil, 0
	}
	s := r.sess
	if len(s.trees) >= smbSrvMaxTrees {
		return smb2StatusInsufficientResources, nil, 0
	}
	s.nextTree++
	t.id = s.nextTree
	s.trees[t.id] = t
	out := make([]byte, 16)
	binary.LittleEndian.PutUint16(out, 16)
	out[2] = 1 // SMB2_SHARE_TYPE_DISK
	access := uint32(smbSrvAccessAll)
	if t.ipc {
		out[2] = smb2ShareTypePipe
		binary.LittleEndian.PutUint32(out[4:], 0x30) // no client-side caching
	} else if c.srv.ReadOnly() {
		access = smbSrvAccessReadOnly
	}
	binary.LittleEndian.PutUint32(out[12:], access)
	return smb2StatusSuccess, out, t.id
}
