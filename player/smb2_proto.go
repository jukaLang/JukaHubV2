package main

// smb2_proto.go — the SMB2/3 wire protocol under smb2.go (MS-SMB2):
// direct TCP framing (one zero byte + 24-bit length), the 64-byte header,
// message ids and credits, NEGOTIATE (2.0.2 to 3.1.1 with SHA-512 preauth
// integrity), SESSION_SETUP (smb2_ntlm.go), signing and encryption
// (smb2_sign.go), and the commands a read-only client needs: TREE_CONNECT,
// CREATE, QUERY_DIRECTORY, READ, CLOSE, plus WRITE for the srvsvc pipe
// (smb2_rpc.go). One request is in flight at a time (smbSession.mu holds
// the connection), so the message-id and credit bookkeeping needs no lock
// of its own. Every length and offset the server sends is checked before
// it is used, and no frame larger than smb2MaxFrame is read.

import (
	"context"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"time"
)

const (
	smb2HeaderLen    = 64
	smb2TransformLen = 52
	smb2MaxFrame     = 4 << 20
	smb2MaxReadChunk = 1 << 20
	smb2DirBufLen    = 64 << 10
	smb2CreditWant   = 128
	smb2MaxDirPages  = 20000
	smb2MaxDirItems  = 200000
)

const (
	smb2ConnectTimeout = 5 * time.Second
	smb2RequestTimeout = 30 * time.Second
)

// Dialects.
const (
	smb2Dialect202 = 0x0202
	smb2Dialect210 = 0x0210
	smb2Dialect300 = 0x0300
	smb2Dialect302 = 0x0302
	smb2Dialect311 = 0x0311
)

// Commands.
const (
	smb2CmdNegotiate      = 0x00
	smb2CmdSessionSetup   = 0x01
	smb2CmdLogoff         = 0x02
	smb2CmdTreeConnect    = 0x03
	smb2CmdTreeDisconnect = 0x04
	smb2CmdCreate         = 0x05
	smb2CmdClose          = 0x06
	smb2CmdRead           = 0x08
	smb2CmdWrite          = 0x09
	smb2CmdQueryDirectory = 0x0e
)

// Header flags, capabilities, security modes, session and share flags,
// ciphers, file attributes, access masks and create options.
const (
	smb2FlagResponse = 0x00000001
	smb2FlagAsync    = 0x00000002
	smb2FlagSigned   = 0x00000008

	smb2CapLargeMTU   = 0x00000004
	smb2CapEncryption = 0x00000040

	smb2SecSigningEnabled  = 0x0001
	smb2SecSigningRequired = 0x0002

	smb2SessionGuest   = 0x0001
	smb2SessionNull    = 0x0002
	smb2SessionEncrypt = 0x0004

	smb2ShareTypePipe = 0x02
	smb2ShareEncrypt  = 0x00008000

	smb2CipherAES128GCM = 0x0002

	smb2AttrHidden    = 0x00000002
	smb2AttrSystem    = 0x00000004
	smb2AttrDirectory = 0x00000010

	smb2AccessRead  = 0x00120089
	smb2AccessDir   = 0x00100081
	smb2AccessAttrs = 0x00100080
	smb2AccessPipe  = 0x0012019f

	smb2OptDirectory    = 0x00000001
	smb2OptNonDirectory = 0x00000040
)

// NTSTATUS codes the client tells apart (MS-ERREF 2.3).
const (
	smb2StatusSuccess               = 0x00000000
	smb2StatusPending               = 0x00000103
	smb2StatusBufferOverflow        = 0x80000005
	smb2StatusNoMoreFiles           = 0x80000006
	smb2StatusInvalidParameter      = 0xc000000d
	smb2StatusNoSuchFile            = 0xc000000f
	smb2StatusEndOfFile             = 0xc0000011
	smb2StatusMoreProcessing        = 0xc0000016
	smb2StatusAccessDenied          = 0xc0000022
	smb2StatusObjectNameInvalid     = 0xc0000033
	smb2StatusObjectNameNotFound    = 0xc0000034
	smb2StatusObjectPathNotFound    = 0xc000003a
	smb2StatusObjectPathSyntaxBad   = 0xc000003b
	smb2StatusSharingViolation      = 0xc0000043
	smb2StatusDeletePending         = 0xc0000056
	smb2StatusNoSuchUser            = 0xc0000064
	smb2StatusWrongPassword         = 0xc000006a
	smb2StatusLogonFailure          = 0xc000006d
	smb2StatusAccountRestriction    = 0xc000006e
	smb2StatusInvalidLogonHours     = 0xc000006f
	smb2StatusInvalidWorkstation    = 0xc0000070
	smb2StatusPasswordExpired       = 0xc0000071
	smb2StatusAccountDisabled       = 0xc0000072
	smb2StatusInsufficientResources = 0xc000009a
	smb2StatusFileIsADirectory      = 0xc00000ba
	smb2StatusNotSupported          = 0xc00000bb
	smb2StatusNetworkNameDeleted    = 0xc00000c9
	smb2StatusBadNetworkName        = 0xc00000cc
	smb2StatusRequestNotAccepted    = 0xc00000d0
	smb2StatusNotADirectory         = 0xc0000103
	smb2StatusLogonTypeNotGranted   = 0xc000015b
	smb2StatusUserSessionDeleted    = 0xc0000203
	smb2StatusPasswordMustChange    = 0xc0000224
	smb2StatusAccountLockedOut      = 0xc0000234
	smb2StatusPathNotCovered        = 0xc0000257
	smb2StatusNetworkSessionExpired = 0xc000035c
)

// smb2Tree is one connected share.
type smb2Tree struct {
	id      uint32
	pipe    bool
	encrypt bool
}

// smb2Conn is one TCP connection with one authenticated session on it.
// Only the holder of smbSession.mu touches it.
type smb2Conn struct {
	nc   net.Conn
	host string

	dialect     uint16
	multiCredit bool
	maxRead     int
	maxTrans    int
	signingReq  bool
	cipherGCM   bool
	preauth     []byte

	nextMid uint64
	credits uint32

	sessionID uint64
	guest     bool
	signer    *smb2Signer
	crypt     *smb2Crypt
	encrypt   bool
	// peerSigns is set once the server has signed a reply; after that an
	// unsigned success reply is refused (a stripped signature).
	peerSigns bool

	trees map[string]*smb2Tree
	// broken: the stream is out of step or the session is gone; the
	// connection is dropped and smbSession dials a new one.
	broken bool
	// lastReq is the last request as sent, before encryption (preauth hash).
	lastReq []byte
}

// smb2Resp is one response. Offsets inside a body count from msg[0], the
// start of the SMB2 header, as MS-SMB2 defines them.
type smb2Resp struct {
	msg     []byte
	status  uint32
	flags   uint32
	treeID  uint32
	session uint64
}

func (r *smb2Resp) body() []byte { return r.msg[smb2HeaderLen:] }

// smb2Bad is a malformed-reply error.
func smb2Bad(what string) error {
	return fmt.Errorf("%w: %s", errSMBProtocol, what)
}

// smb2IsTimeout reports a deadline that ran out (ours or the context's).
func smb2IsTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// smb2Connect dials addr, negotiates and signs in.
func smb2Connect(ctx context.Context, addr, uncHost, user, password, domain string) (*smb2Conn, error) {
	d := net.Dialer{Timeout: smb2ConnectTimeout}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("smb: connect: %w", err)
	}
	c := &smb2Conn{nc: nc, host: uncHost, credits: 1, trees: map[string]*smb2Tree{}}
	if err := c.negotiate(ctx); err != nil {
		nc.Close()
		if smb2MaybeSMB1(err) && smb2ProbeSMB1(ctx, addr) {
			return nil, fmt.Errorf("%w (%v)", errSMBv1Only, err)
		}
		return nil, err
	}
	if err := c.sessionSetup(ctx, user, password, domain); err != nil {
		nc.Close()
		return nil, err
	}
	logInfo("smb", "connected: SMB %s, signed %v, encrypted %v, guest %v",
		smb2DialectName(c.dialect), c.signer != nil, c.encrypt, c.guest)
	return c, nil
}

func smb2DialectName(d uint16) string {
	switch d {
	case smb2Dialect202:
		return "2.0.2"
	case smb2Dialect210:
		return "2.1"
	case smb2Dialect300:
		return "3.0"
	case smb2Dialect302:
		return "3.0.2"
	case smb2Dialect311:
		return "3.1.1"
	}
	return fmt.Sprintf("0x%04x", d)
}

// deadline applies ctx (or the default request timeout) to the socket and
// makes a cancelled ctx interrupt a blocked read.
func (c *smb2Conn) deadline(ctx context.Context) (stop func() bool) {
	dl, ok := ctx.Deadline()
	if !ok {
		dl = time.Now().Add(smb2RequestTimeout)
	}
	_ = c.nc.SetDeadline(dl)
	return context.AfterFunc(ctx, func() { _ = c.nc.SetDeadline(time.Unix(1, 0)) })
}

// roundTrip sends one request and returns its final response (interim
// STATUS_PENDING replies are skipped). payload is the larger of the bytes
// sent and the bytes asked back; it sets the credit charge.
func (c *smb2Conn) roundTrip(ctx context.Context, cmd uint16, tree *smb2Tree, body []byte, payload int) (*smb2Resp, error) {
	if c.broken {
		return nil, fmt.Errorf("%w: connection lost", errSMBClosed)
	}
	setup := cmd == smb2CmdNegotiate || cmd == smb2CmdSessionSetup
	encrypted := !setup && (c.encrypt || (tree != nil && tree.encrypt))
	if encrypted && c.crypt == nil {
		return nil, errSMBEncryptionRequired
	}
	charge := uint16(1)
	if c.multiCredit && payload > 65536 {
		charge = uint16((payload-1)/65536 + 1)
	}
	if uint32(charge) > c.credits {
		c.broken = true
		return nil, smb2Bad("the server granted no credits")
	}
	msg := make([]byte, smb2HeaderLen+len(body))
	copy(msg, "\xfeSMB")
	binary.LittleEndian.PutUint16(msg[4:], smb2HeaderLen)
	if c.multiCredit {
		binary.LittleEndian.PutUint16(msg[6:], charge)
	}
	binary.LittleEndian.PutUint16(msg[12:], cmd)
	binary.LittleEndian.PutUint16(msg[14:], max(smb2CreditWant, charge+8))
	mid := c.nextMid
	binary.LittleEndian.PutUint64(msg[24:], mid)
	if tree != nil {
		binary.LittleEndian.PutUint32(msg[36:], tree.id)
	}
	binary.LittleEndian.PutUint64(msg[40:], c.sessionID)
	copy(msg[smb2HeaderLen:], body)
	c.nextMid += uint64(charge)
	c.credits -= uint32(charge)
	if !encrypted && !setup && c.signer != nil {
		c.signer.sign(msg)
	}
	c.lastReq = msg

	stop := c.deadline(ctx)
	r, err := c.exchange(msg, cmd, mid, encrypted)
	if !stop() && err == nil {
		// ctx ended while the reply came in; the socket deadline may
		// already be in the past, so this connection is not reused.
		c.broken = true
	}
	if err != nil {
		c.broken = true
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w (%w)", ctx.Err(), err)
		}
		return nil, err
	}
	switch r.status {
	case smb2StatusNetworkSessionExpired, smb2StatusUserSessionDeleted, smb2StatusNetworkNameDeleted:
		c.broken = true
	}
	return r, nil
}

// exchange writes msg and reads until the reply for mid arrives.
func (c *smb2Conn) exchange(msg []byte, cmd uint16, mid uint64, encrypted bool) (*smb2Resp, error) {
	out := msg
	if encrypted {
		out = c.crypt.seal(msg, c.sessionID)
	}
	var pfx [4]byte
	binary.BigEndian.PutUint32(pfx[:], uint32(len(out)))
	bufs := net.Buffers{pfx[:], out}
	if _, err := bufs.WriteTo(c.nc); err != nil {
		return nil, err
	}
	for {
		frame, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		wasEncrypted := false
		switch {
		case len(frame) >= 4 && string(frame[:4]) == "\xfdSMB":
			if c.crypt == nil {
				return nil, smb2Bad("unexpected encrypted reply")
			}
			if frame, err = c.crypt.open(frame, c.sessionID); err != nil {
				return nil, err
			}
			wasEncrypted = true
		case len(frame) >= 4 && string(frame[:4]) == "\xffSMB":
			return nil, errSMBv1Only
		}
		if len(frame) < smb2HeaderLen || string(frame[:4]) != "\xfeSMB" ||
			binary.LittleEndian.Uint16(frame[4:]) != smb2HeaderLen {
			return nil, smb2Bad("reply header")
		}
		if next := binary.LittleEndian.Uint32(frame[20:]); next != 0 {
			if next < smb2HeaderLen || uint64(next) > uint64(len(frame)) {
				return nil, smb2Bad("compound reply")
			}
			frame = frame[:next]
		}
		c.credits = min(c.credits+uint32(binary.LittleEndian.Uint16(frame[14:])), 1<<16)
		flags := binary.LittleEndian.Uint32(frame[16:])
		rmid := binary.LittleEndian.Uint64(frame[24:])
		if rmid == math.MaxUint64 {
			continue // oplock/lease break notice; this client takes none
		}
		if flags&smb2FlagResponse == 0 || rmid != mid || binary.LittleEndian.Uint16(frame[12:]) != cmd {
			return nil, smb2Bad("reply out of order")
		}
		status := binary.LittleEndian.Uint32(frame[8:])
		if status == smb2StatusPending && flags&smb2FlagAsync != 0 {
			continue // interim reply; the final one follows
		}
		r := &smb2Resp{msg: frame, status: status, flags: flags, session: binary.LittleEndian.Uint64(frame[40:])}
		if flags&smb2FlagAsync == 0 {
			r.treeID = binary.LittleEndian.Uint32(frame[36:])
		}
		if err := c.checkReply(r, cmd, encrypted, wasEncrypted); err != nil {
			return nil, err
		}
		return r, nil
	}
}

// checkReply enforces signing/encryption on a reply. Error replies may come
// unsigned (servers do that for expired sessions); they only ever fail an
// operation.
func (c *smb2Conn) checkReply(r *smb2Resp, cmd uint16, sentEncrypted, wasEncrypted bool) error {
	if wasEncrypted {
		return nil // AES-GCM already authenticated it
	}
	isErr := r.status>>30 == 3
	if sentEncrypted && !isErr {
		return fmt.Errorf("%w: unencrypted reply in an encrypted session", errSMBSignature)
	}
	// Every reply to a request of this session carries its SessionId, and the
	// signature covers it, so a reply with another SessionId is checked like
	// any other (skipping it would let a changed SessionId strip signing).
	if c.signer == nil || cmd == smb2CmdSessionSetup {
		return nil
	}
	if r.flags&smb2FlagSigned != 0 {
		if !c.signer.verify(r.msg) {
			return fmt.Errorf("%w: bad signature", errSMBSignature)
		}
		c.peerSigns = true
		return nil
	}
	if !isErr && (c.signingReq || c.peerSigns) {
		return fmt.Errorf("%w: unsigned reply", errSMBSignature)
	}
	return nil
}

// readFrame reads one direct-TCP frame.
func (c *smb2Conn) readFrame() ([]byte, error) {
	var pfx [4]byte
	for {
		if _, err := io.ReadFull(c.nc, pfx[:]); err != nil {
			return nil, err
		}
		n := int(pfx[1])<<16 | int(pfx[2])<<8 | int(pfx[3])
		if pfx[0] == 0x85 && n == 0 {
			continue // NetBIOS keep-alive
		}
		if pfx[0] != 0 || n < 4 || n > smb2MaxFrame {
			return nil, smb2Bad("frame length")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.nc, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
}

// smb2PreauthStep is H = SHA-512(H || message) (MS-SMB2 3.2.5.2).
func smb2PreauthStep(h, msg []byte) []byte {
	s := sha512.New()
	s.Write(h)
	s.Write(msg)
	return s.Sum(nil)
}

func smb2Pad8(b []byte) []byte {
	for len(b)%8 != 0 {
		b = append(b, 0)
	}
	return b
}

func smb2AppendNegCtx(b []byte, typ uint16, data []byte) []byte {
	b = binary.LittleEndian.AppendUint16(b, typ)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(data)))
	b = append(b, 0, 0, 0, 0)
	return append(b, data...)
}

// smb2NegotiateRequest is the NEGOTIATE body: dialects 2.0.2 to 3.1.1,
// signing enabled (not required), large MTU, and for 3.1.1 the preauth
// integrity (SHA-512) and encryption (AES-128-GCM only) contexts.
func smb2NegotiateRequest() ([]byte, error) {
	dialects := []uint16{smb2Dialect202, smb2Dialect210, smb2Dialect300, smb2Dialect302, smb2Dialect311}
	body := make([]byte, 36+2*len(dialects))
	binary.LittleEndian.PutUint16(body[0:], 36)
	binary.LittleEndian.PutUint16(body[2:], uint16(len(dialects)))
	binary.LittleEndian.PutUint16(body[4:], smb2SecSigningEnabled)
	binary.LittleEndian.PutUint32(body[8:], smb2CapLargeMTU|smb2CapEncryption)
	if _, err := rand.Read(body[12:28]); err != nil {
		return nil, err
	}
	for i, d := range dialects {
		binary.LittleEndian.PutUint16(body[36+2*i:], d)
	}
	body = smb2Pad8(body)
	binary.LittleEndian.PutUint32(body[28:], uint32(smb2HeaderLen+len(body)))
	binary.LittleEndian.PutUint16(body[32:], 2)
	pre := make([]byte, 6, 38)
	binary.LittleEndian.PutUint16(pre[0:], 1)  // one hash algorithm
	binary.LittleEndian.PutUint16(pre[2:], 32) // salt length
	binary.LittleEndian.PutUint16(pre[4:], 1)  // SHA-512
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	pre = append(pre, salt...)
	body = smb2AppendNegCtx(body, 1, pre)
	body = smb2Pad8(body)
	body = smb2AppendNegCtx(body, 2, []byte{1, 0, smb2CipherAES128GCM, 0})
	return body, nil
}

// smb2NegInfo is what the client keeps from a NEGOTIATE reply.
type smb2NegInfo struct {
	secMode  uint16
	dialect  uint16
	caps     uint32
	maxTrans uint32
	maxRead  uint32
	cipher   uint16
}

// smb2ParseNegotiate parses a NEGOTIATE reply (msg = header + body).
func smb2ParseNegotiate(msg []byte) (*smb2NegInfo, error) {
	if len(msg) < smb2HeaderLen+64 {
		return nil, smb2Bad("short NEGOTIATE reply")
	}
	p := msg[smb2HeaderLen:]
	if binary.LittleEndian.Uint16(p) != 65 {
		return nil, smb2Bad("NEGOTIATE reply size")
	}
	in := &smb2NegInfo{
		secMode:  binary.LittleEndian.Uint16(p[2:]),
		dialect:  binary.LittleEndian.Uint16(p[4:]),
		caps:     binary.LittleEndian.Uint32(p[24:]),
		maxTrans: binary.LittleEndian.Uint32(p[28:]),
		maxRead:  binary.LittleEndian.Uint32(p[32:]),
	}
	switch in.dialect {
	case smb2Dialect202, smb2Dialect210, smb2Dialect300, smb2Dialect302:
		return in, nil
	case smb2Dialect311:
	default:
		return nil, fmt.Errorf("%w: SMB dialect 0x%04x", errSMBUnsupported, in.dialect)
	}
	count := int(binary.LittleEndian.Uint16(p[6:]))
	off := uint64(binary.LittleEndian.Uint32(p[60:]))
	size := uint64(len(msg))
	preauth := false
	for range count {
		if off%8 != 0 || off < smb2HeaderLen+64 || off+8 > size {
			return nil, smb2Bad("negotiate context offset")
		}
		typ := binary.LittleEndian.Uint16(msg[off:])
		n := uint64(binary.LittleEndian.Uint16(msg[off+2:]))
		if n > size-off-8 {
			return nil, smb2Bad("negotiate context length")
		}
		data := msg[off+8 : off+8+n]
		switch typ {
		case 1:
			if len(data) < 6 || binary.LittleEndian.Uint16(data) != 1 || binary.LittleEndian.Uint16(data[4:]) != 1 {
				return nil, smb2Bad("preauth integrity context")
			}
			preauth = true
		case 2:
			if len(data) < 4 {
				return nil, smb2Bad("encryption context")
			}
			if binary.LittleEndian.Uint16(data) == 1 {
				in.cipher = binary.LittleEndian.Uint16(data[2:])
			}
		}
		off = (off + 8 + n + 7) &^ 7
	}
	if !preauth {
		return nil, smb2Bad("no preauth integrity context")
	}
	return in, nil
}

// negotiate runs NEGOTIATE and records the dialect and limits.
func (c *smb2Conn) negotiate(ctx context.Context) error {
	body, err := smb2NegotiateRequest()
	if err != nil {
		return err
	}
	r, err := c.roundTrip(ctx, smb2CmdNegotiate, nil, body, 0)
	if err != nil {
		return err
	}
	if r.status != smb2StatusSuccess {
		return &smb2StatusError{Op: "negotiate", Status: r.status}
	}
	in, err := smb2ParseNegotiate(r.msg)
	if err != nil {
		return err
	}
	c.dialect = in.dialect
	c.signingReq = in.secMode&smb2SecSigningRequired != 0
	c.multiCredit = in.dialect >= smb2Dialect210 && in.caps&smb2CapLargeMTU != 0
	c.maxRead = int(min(in.maxRead, smb2MaxReadChunk))
	if !c.multiCredit {
		c.maxRead = min(c.maxRead, 65536)
	}
	c.maxTrans = int(min(in.maxTrans, smb2DirBufLen))
	if c.maxRead < 4096 || c.maxTrans < 4096 {
		return smb2Bad("server buffer sizes")
	}
	if in.dialect == smb2Dialect311 {
		c.cipherGCM = in.cipher == smb2CipherAES128GCM
		h := smb2PreauthStep(make([]byte, 64), c.lastReq)
		c.preauth = smb2PreauthStep(h, r.msg)
	}
	return nil
}

// smb2MaybeSMB1 reports a NEGOTIATE failure that looks like a server that
// hung up on an SMB2 request (what SMB1-only servers do).
func smb2MaybeSMB1(err error) bool {
	if smb2IsTimeout(err) || errors.Is(err, context.Canceled) {
		return false
	}
	var op *net.OpError
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &op)
}

// smb2ProbeSMB1 asks addr for the SMB1 "NT LM 0.12" dialect and reports
// whether it answered in SMB1.
func smb2ProbeSMB1(ctx context.Context, addr string) bool {
	d := net.Dialer{Timeout: smb2ConnectTimeout}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	defer nc.Close()
	_ = nc.SetDeadline(time.Now().Add(smb2ConnectTimeout))
	msg := make([]byte, 32, 64)
	copy(msg, "\xffSMB")
	msg[4] = 0x72 // SMB_COM_NEGOTIATE
	msg[9] = 0x18
	binary.LittleEndian.PutUint16(msg[10:], 0xc001)
	binary.LittleEndian.PutUint16(msg[26:], 0xfeff)
	dialect := "\x02NT LM 0.12\x00"
	msg = append(msg, 0) // WordCount
	msg = binary.LittleEndian.AppendUint16(msg, uint16(len(dialect)))
	msg = append(msg, dialect...)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(msg)))
	if _, err := nc.Write(append(frame, msg...)); err != nil {
		return false
	}
	var reply [9]byte
	if _, err := io.ReadFull(nc, reply[:]); err != nil {
		return false
	}
	return reply[0] == 0 && string(reply[4:8]) == "\xffSMB" && reply[8] == 0x72
}

// sessionSetup signs in with NTLMv2 in SPNEGO and sets up signing and
// encryption keys for the session.
func (c *smb2Conn) sessionSetup(ctx context.Context, user, password, domain string) error {
	h := c.preauth
	step := func(token []byte) (*smb2Resp, error) {
		if len(token) > 0xffff {
			return nil, fmt.Errorf("%w: sign-in message too long", errSMBAuth)
		}
		body := make([]byte, 24, 24+len(token))
		binary.LittleEndian.PutUint16(body[0:], 25)
		body[3] = smb2SecSigningEnabled
		binary.LittleEndian.PutUint16(body[12:], smb2HeaderLen+24)
		binary.LittleEndian.PutUint16(body[14:], uint16(len(token)))
		body = append(body, token...)
		r, err := c.roundTrip(ctx, smb2CmdSessionSetup, nil, body, 0)
		if err != nil {
			return nil, err
		}
		if h != nil {
			h = smb2PreauthStep(h, c.lastReq)
			if r.status == smb2StatusMoreProcessing {
				h = smb2PreauthStep(h, r.msg)
			}
		}
		return r, nil
	}
	r, err := step(smb2SpnegoInit(smb2NtlmNegotiateMsg(smb2NtlmClientFlags)))
	if err != nil {
		return err
	}
	if r.status != smb2StatusMoreProcessing {
		if r.status == smb2StatusSuccess {
			return smb2Bad("sign-in finished without a challenge")
		}
		return &smb2StatusError{Op: "sign-in", Status: r.status}
	}
	c.sessionID = r.session
	blob, err := smb2SessionSecBuf(r)
	if err != nil {
		return err
	}
	tok, rejected, err := smb2SpnegoToken(blob)
	if err != nil {
		return err
	}
	if rejected || len(tok) == 0 {
		return fmt.Errorf("%w: the server offered no NTLM sign-in", errSMBAuth)
	}
	ch, err := smb2ParseNtlmChallenge(tok)
	if err != nil {
		return err
	}
	auth, key, err := smb2NtlmAuthenticateMsg(ch, user, password, domain, "JUKAHUB")
	if err != nil {
		return err
	}
	r, err = step(smb2SpnegoResp(auth))
	if err != nil {
		return err
	}
	if r.status != smb2StatusSuccess {
		return &smb2StatusError{Op: "sign-in", Status: r.status}
	}
	p := r.body()
	if len(p) < 8 || binary.LittleEndian.Uint16(p) != 9 {
		return smb2Bad("SESSION_SETUP reply")
	}
	flags := binary.LittleEndian.Uint16(p[2:])
	c.guest = flags&(smb2SessionGuest|smb2SessionNull) != 0 || len(key) == 0
	if !c.guest {
		key = key[:min(len(key), 16)]
		signer, err := smb2NewSigner(c.dialect, key, h)
		if err != nil {
			return err
		}
		if r.flags&smb2FlagSigned != 0 {
			if !signer.verify(r.msg) {
				return fmt.Errorf("%w: sign-in reply", errSMBSignature)
			}
			c.peerSigns = true
		} else if c.dialect == smb2Dialect311 {
			return fmt.Errorf("%w: unsigned sign-in reply", errSMBSignature)
		}
		c.signer = signer
		if c.dialect == smb2Dialect311 && c.cipherGCM {
			if c.crypt, err = smb2NewCrypt(key, h); err != nil {
				return err
			}
		}
	}
	if flags&smb2SessionEncrypt != 0 {
		c.encrypt = true
		if c.crypt == nil {
			return smb2EncryptionError(c)
		}
	}
	return nil
}

// smb2EncryptionError explains why this session cannot encrypt.
func smb2EncryptionError(c *smb2Conn) error {
	switch {
	case c.guest:
		return fmt.Errorf("%w (guest sessions cannot encrypt)", errSMBEncryptionRequired)
	case c.dialect != smb2Dialect311:
		return fmt.Errorf("%w (SMB %s needs AES-CCM)", errSMBEncryptionRequired, smb2DialectName(c.dialect))
	default:
		return fmt.Errorf("%w (no AES-128-GCM)", errSMBEncryptionRequired)
	}
}

// smb2SessionSecBuf returns the security blob of a SESSION_SETUP reply.
func smb2SessionSecBuf(r *smb2Resp) ([]byte, error) {
	p := r.body()
	if len(p) < 8 || binary.LittleEndian.Uint16(p) != 9 {
		return nil, smb2Bad("SESSION_SETUP reply")
	}
	off := uint64(binary.LittleEndian.Uint16(p[4:]))
	n := uint64(binary.LittleEndian.Uint16(p[6:]))
	if n == 0 {
		return nil, nil
	}
	if off < smb2HeaderLen+8 || off+n > uint64(len(r.msg)) {
		return nil, smb2Bad("SESSION_SETUP security buffer")
	}
	return r.msg[off : off+n], nil
}

// tree returns the connected share, connecting on first use.
func (c *smb2Conn) tree(ctx context.Context, share string) (*smb2Tree, error) {
	key := strings.ToLower(share)
	if t, ok := c.trees[key]; ok {
		return t, nil
	}
	path := smb2UTF16(`\\` + c.host + `\` + share)
	if len(path) > 0xffff {
		return nil, errSMBBadPath
	}
	body := make([]byte, 8, 8+len(path))
	binary.LittleEndian.PutUint16(body[0:], 9)
	binary.LittleEndian.PutUint16(body[4:], smb2HeaderLen+8)
	binary.LittleEndian.PutUint16(body[6:], uint16(len(path)))
	body = append(body, path...)
	r, err := c.roundTrip(ctx, smb2CmdTreeConnect, nil, body, 0)
	if err != nil {
		return nil, err
	}
	if r.status != smb2StatusSuccess {
		return nil, &smb2StatusError{Op: "open share", Name: share, Status: r.status}
	}
	p := r.body()
	if len(p) < 16 || binary.LittleEndian.Uint16(p) != 16 {
		return nil, smb2Bad("TREE_CONNECT reply")
	}
	t := &smb2Tree{
		id:      r.treeID,
		pipe:    p[2] == smb2ShareTypePipe,
		encrypt: binary.LittleEndian.Uint32(p[4:])&smb2ShareEncrypt != 0,
	}
	if t.encrypt && c.crypt == nil {
		c.treeDisconnect(ctx, t)
		return nil, smb2EncryptionError(c)
	}
	c.trees[key] = t
	return t, nil
}

// diskTree is tree for a file share (not a pipe or printer share).
func (c *smb2Conn) diskTree(ctx context.Context, share string) (*smb2Tree, error) {
	t, err := c.tree(ctx, share)
	if err != nil {
		return nil, err
	}
	if t.pipe {
		return nil, fmt.Errorf("%w: %q is not a file share", errSMBShareNotFound, share)
	}
	return t, nil
}

func (c *smb2Conn) treeDisconnect(ctx context.Context, t *smb2Tree) {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint16(body, 4)
	_, _ = c.roundTrip(ctx, smb2CmdTreeDisconnect, t, body, 0)
}

func (c *smb2Conn) logoff(ctx context.Context) {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint16(body, 4)
	_, _ = c.roundTrip(ctx, smb2CmdLogoff, nil, body, 0)
}

// smb2Open is the useful part of a CREATE reply.
type smb2Open struct {
	fid   [16]byte
	attrs uint32
	size  int64
	mtime time.Time
}

// create opens an existing file, directory or pipe for reading (it never
// creates or overwrites anything: CreateDisposition is FILE_OPEN).
func (c *smb2Conn) create(ctx context.Context, t *smb2Tree, name string, access, shareAccess, options uint32) (*smb2Open, error) {
	nameB := smb2UTF16(name)
	if len(nameB) > 0xfffe {
		return nil, fmt.Errorf("%w: path too long", errSMBBadPath)
	}
	body := make([]byte, 56, 56+len(nameB)+8)
	binary.LittleEndian.PutUint16(body[0:], 57)
	binary.LittleEndian.PutUint32(body[4:], 2) // impersonation level: Impersonation
	binary.LittleEndian.PutUint32(body[24:], access)
	binary.LittleEndian.PutUint32(body[32:], shareAccess)
	binary.LittleEndian.PutUint32(body[36:], 1) // FILE_OPEN
	binary.LittleEndian.PutUint32(body[40:], options)
	binary.LittleEndian.PutUint16(body[44:], smb2HeaderLen+56)
	binary.LittleEndian.PutUint16(body[46:], uint16(len(nameB)))
	body = append(body, nameB...)
	body = smb2Pad8(append(body, 0))
	r, err := c.roundTrip(ctx, smb2CmdCreate, t, body, 0)
	if err != nil {
		return nil, err
	}
	if r.status != smb2StatusSuccess {
		return nil, &smb2StatusError{Op: "open", Name: strings.ReplaceAll(name, `\`, "/"), Status: r.status}
	}
	p := r.body()
	if len(p) < 88 || binary.LittleEndian.Uint16(p) != 89 {
		return nil, smb2Bad("CREATE reply")
	}
	eof := binary.LittleEndian.Uint64(p[48:])
	if eof > math.MaxInt64/2 {
		return nil, smb2Bad("file size")
	}
	o := &smb2Open{
		attrs: binary.LittleEndian.Uint32(p[56:]),
		size:  int64(eof),
		mtime: smb2FileTime(binary.LittleEndian.Uint64(p[24:])),
	}
	copy(o.fid[:], p[64:80])
	return o, nil
}

// closeFile closes a handle; errors do not matter to a reader.
func (c *smb2Conn) closeFile(ctx context.Context, t *smb2Tree, fid [16]byte) {
	if c.broken {
		return
	}
	body := make([]byte, 24)
	binary.LittleEndian.PutUint16(body, 24)
	copy(body[8:], fid[:])
	_, _ = c.roundTrip(ctx, smb2CmdClose, t, body, 0)
}

// queryDir asks for the next page of a directory listing
// (FileDirectoryInformation, pattern "*"). done is set at the end.
func (c *smb2Conn) queryDir(ctx context.Context, t *smb2Tree, fid [16]byte, restart bool) (buf []byte, done bool, err error) {
	pattern := smb2UTF16("*")
	outLen := min(c.maxTrans, smb2DirBufLen)
	body := make([]byte, 32, 32+len(pattern))
	binary.LittleEndian.PutUint16(body[0:], 33)
	body[2] = 0x01 // FileDirectoryInformation
	if restart {
		body[3] = 0x01 // SMB2_RESTART_SCANS
	}
	copy(body[8:24], fid[:])
	binary.LittleEndian.PutUint16(body[24:], smb2HeaderLen+32)
	binary.LittleEndian.PutUint16(body[26:], uint16(len(pattern)))
	binary.LittleEndian.PutUint32(body[28:], uint32(outLen))
	body = append(body, pattern...)
	r, err := c.roundTrip(ctx, smb2CmdQueryDirectory, t, body, outLen)
	if err != nil {
		return nil, false, err
	}
	switch r.status {
	case smb2StatusSuccess:
	case smb2StatusNoMoreFiles, smb2StatusNoSuchFile:
		return nil, true, nil
	default:
		return nil, false, &smb2StatusError{Op: "list folder", Status: r.status}
	}
	p := r.body()
	if len(p) < 8 || binary.LittleEndian.Uint16(p) != 9 {
		return nil, false, smb2Bad("QUERY_DIRECTORY reply")
	}
	off := uint64(binary.LittleEndian.Uint16(p[2:]))
	n := uint64(binary.LittleEndian.Uint32(p[4:]))
	if n == 0 {
		return nil, true, nil
	}
	if off < smb2HeaderLen+8 || n > uint64(outLen) || off+n > uint64(len(r.msg)) {
		return nil, false, smb2Bad("QUERY_DIRECTORY buffer")
	}
	return r.msg[off : off+n], false, nil
}

// smb2ParseDirInfo appends the entries of one FileDirectoryInformation
// buffer to out. "." and ".." and names that could not be a single path
// element ("/", "\", NUL, ":") are left out.
func smb2ParseDirInfo(buf []byte, out []smbEntry) ([]smbEntry, error) {
	off := 0
	for {
		if len(buf)-off < 64 {
			return out, smb2Bad("directory entry")
		}
		e := buf[off:]
		next := binary.LittleEndian.Uint32(e[0:])
		nameLen := uint64(binary.LittleEndian.Uint32(e[60:]))
		if nameLen > uint64(len(e)-64) || nameLen%2 != 0 {
			return out, smb2Bad("directory entry name")
		}
		name := smb2FromUTF16(e[64 : 64+nameLen])
		if smb2ValidName(name) {
			if len(out) >= smb2MaxDirItems {
				return out, smb2Bad("too many directory entries")
			}
			attrs := binary.LittleEndian.Uint32(e[56:])
			ent := smbEntry{
				Name:    name,
				ModTime: smb2FileTime(binary.LittleEndian.Uint64(e[24:])),
				IsDir:   attrs&smb2AttrDirectory != 0,
				Hidden:  smb2HiddenEntry(name, attrs),
			}
			if size := binary.LittleEndian.Uint64(e[40:]); !ent.IsDir && size <= math.MaxInt64/2 {
				ent.Size = int64(size)
			}
			out = append(out, ent)
		}
		if next == 0 {
			return out, nil
		}
		// The next entry starts after this one's name (entries never overlap).
		if uint64(next) > uint64(len(e)) || uint64(next) < 64+nameLen {
			return out, smb2Bad("directory entry offset")
		}
		off += int(next)
	}
}

// read reads up to n bytes at off. The returned slice points into the
// reply. STATUS_END_OF_FILE is io.EOF; STATUS_BUFFER_OVERFLOW (a pipe
// message longer than n) still returns the data.
func (c *smb2Conn) read(ctx context.Context, t *smb2Tree, fid [16]byte, off int64, n int) ([]byte, error) {
	if n <= 0 {
		return nil, smb2Bad("the server granted no credits")
	}
	body := make([]byte, 49)
	binary.LittleEndian.PutUint16(body[0:], 49)
	body[2] = smb2HeaderLen + 16 // where the data should start in the reply
	binary.LittleEndian.PutUint32(body[4:], uint32(n))
	binary.LittleEndian.PutUint64(body[8:], uint64(off))
	copy(body[16:32], fid[:])
	r, err := c.roundTrip(ctx, smb2CmdRead, t, body, n)
	if err != nil {
		return nil, err
	}
	switch r.status {
	case smb2StatusSuccess, smb2StatusBufferOverflow:
	case smb2StatusEndOfFile:
		return nil, io.EOF
	default:
		return nil, &smb2StatusError{Op: "read", Status: r.status}
	}
	p := r.body()
	if len(p) < 16 || binary.LittleEndian.Uint16(p) != 17 {
		return nil, smb2Bad("READ reply")
	}
	dOff := uint64(p[2])
	dLen := uint64(binary.LittleEndian.Uint32(p[4:]))
	if dLen == 0 {
		return nil, nil
	}
	if dOff < smb2HeaderLen+16 || dLen > uint64(n) || dOff+dLen > uint64(len(r.msg)) {
		return nil, smb2Bad("READ data")
	}
	return r.msg[dOff : dOff+dLen], nil
}

// readSize is how much one READ may ask for right now.
func (c *smb2Conn) readSize() int {
	n := c.maxRead
	if c.multiCredit {
		n = min(n, int(c.credits)*65536)
	}
	return n
}

// writePipe writes data to a named pipe (the srvsvc RPC pipe on IPC$; the
// client never writes to files).
func (c *smb2Conn) writePipe(ctx context.Context, t *smb2Tree, fid [16]byte, data []byte) error {
	if !t.pipe {
		return errSMBUnsupported
	}
	body := make([]byte, 48, 48+len(data))
	binary.LittleEndian.PutUint16(body[0:], 49)
	binary.LittleEndian.PutUint16(body[2:], smb2HeaderLen+48)
	binary.LittleEndian.PutUint32(body[4:], uint32(len(data)))
	copy(body[16:32], fid[:])
	body = append(body, data...)
	r, err := c.roundTrip(ctx, smb2CmdWrite, t, body, len(data))
	if err != nil {
		return err
	}
	if r.status != smb2StatusSuccess {
		return &smb2StatusError{Op: "write pipe", Status: r.status}
	}
	return nil
}

// smb2FileTime converts FILETIME (100 ns since 1601) to time.Time; 0 and
// out-of-range values are the zero time.
func smb2FileTime(ft uint64) time.Time {
	if ft == 0 || ft > math.MaxInt64 {
		return time.Time{}
	}
	d := int64(ft) - smb2FileTimeEpoch
	return time.Unix(d/10000000, (d%10000000)*100)
}
