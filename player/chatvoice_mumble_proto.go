package main

// chatvoice_mumble_proto.go — the small part of the Mumble protocol that
// Voice rooms speak (chatvoice_mumble.go).
//
// A Mumble control message is a 6-byte header (big-endian type and length)
// followed by a protobuf body. Voice travels inside UDPTunnel messages on the
// same TLS connection, in Mumble's legacy voice packet format, which every
// server accepts from a client that reports version 1.4. Only the fields
// JukaHub uses are written or read; any other field is skipped by its wire
// type, so a newer server's additions are harmless. Every length is checked
// before it is used: these bytes come from a server on the Internet.

import (
	"encoding/binary"
	"errors"
	"io"
)

// Control message types, in Mumble.proto's order.
const (
	mumbleMsgVersion          = 0
	mumbleMsgUDPTunnel        = 1
	mumbleMsgAuthenticate     = 2
	mumbleMsgPing             = 3
	mumbleMsgReject           = 4
	mumbleMsgServerSync       = 5
	mumbleMsgChannelRemove    = 6
	mumbleMsgChannelState     = 7
	mumbleMsgUserRemove       = 8
	mumbleMsgUserState        = 9
	mumbleMsgPermissionDenied = 12
	mumbleMsgCodecVersion     = 21
	mumbleMsgServerConfig     = 24
)

const (
	// mumbleMaxMessage bounds one control message (a long welcome text or a
	// channel description with a picture stays far below it).
	mumbleMaxMessage = 4 << 20
	// mumbleVoiceOpus is the codec number in a voice packet's first byte.
	mumbleVoiceOpus = 4
	// An Opus frame's size field carries the length in its low 13 bits and
	// marks the last packet of a talk spurt with 0x2000.
	mumbleOpusTerminator = 0x2000
	mumbleOpusSizeMask   = 0x1fff
	// mumbleClientVersion is 1.4.0, the newest version whose voice packets
	// use the legacy format this file writes.
	mumbleClientVersion = 1<<16 | 4<<8
)

// Reject reasons (Mumble.proto Reject.RejectType) JukaHub reacts to.
const (
	mumbleRejectInvalidName = 2
	mumbleRejectWrongUserPW = 3 // the name belongs to a registered user
	mumbleRejectServerPW    = 4
	mumbleRejectNameInUse   = 5
	mumbleRejectServerFull  = 6
)

var errMumbleProto = errors.New("malformed voice server message")

// ── Protobuf, the few wire types Mumble uses ─────────────────────────────

// pbWriter appends protobuf fields.
type pbWriter struct{ b []byte }

func (w *pbWriter) key(field, wire int) {
	w.b = binary.AppendUvarint(w.b, uint64(field)<<3|uint64(wire))
}

func (w *pbWriter) varint(field int, v uint64) {
	w.key(field, 0)
	w.b = binary.AppendUvarint(w.b, v)
}

func (w *pbWriter) boolean(field int, v bool) {
	n := uint64(0)
	if v {
		n = 1
	}
	w.varint(field, n)
}

func (w *pbWriter) str(field int, v string) {
	w.key(field, 2)
	w.b = binary.AppendUvarint(w.b, uint64(len(v)))
	w.b = append(w.b, v...)
}

// pbField is one decoded field: u holds a varint or fixed value, b the bytes
// of a length-delimited one (aliasing the message buffer).
type pbField struct {
	num  int
	wire int
	u    uint64
	b    []byte
}

// pbEach calls fn for every field of a protobuf message.
func pbEach(data []byte, fn func(f pbField)) error {
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 {
			return errMumbleProto
		}
		data = data[n:]
		f := pbField{num: int(key >> 3), wire: int(key & 7)}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(data)
			if n <= 0 {
				return errMumbleProto
			}
			f.u, data = v, data[n:]
		case 1:
			if len(data) < 8 {
				return errMumbleProto
			}
			f.u, data = binary.LittleEndian.Uint64(data), data[8:]
		case 2:
			l, n := binary.Uvarint(data)
			if n <= 0 || l > uint64(len(data)-n) {
				return errMumbleProto
			}
			end := n + int(l)
			f.b, data = data[n:end], data[end:]
		case 5:
			if len(data) < 4 {
				return errMumbleProto
			}
			f.u, data = uint64(binary.LittleEndian.Uint32(data)), data[4:]
		default:
			return errMumbleProto
		}
		fn(f)
	}
	return nil
}

// ── Messages JukaHub sends ───────────────────────────────────────────────

func mumbleVersionBody(osName string) []byte {
	var w pbWriter
	w.varint(1, mumbleClientVersion) // version_v1
	w.str(2, "JukaHub")              // release
	w.str(3, osName)                 // os
	w.str(4, "JukaHub")              // os_version
	return w.b
}

func mumbleAuthBody(username string) []byte {
	var w pbWriter
	w.str(1, username) // username
	w.boolean(5, true) // opus
	return w.b
}

func mumblePingBody(stamp uint64) []byte {
	var w pbWriter
	w.varint(1, stamp) // timestamp
	return w.b
}

// mumbleMoveBody asks the server to put this session in a channel.
func mumbleMoveBody(session, channel uint32) []byte {
	var w pbWriter
	w.varint(1, uint64(session)) // session
	w.varint(5, uint64(channel)) // channel_id
	return w.b
}

// mumbleSelfMuteBody tells the room this session muted (or unmuted) itself.
func mumbleSelfMuteBody(session uint32, muted bool) []byte {
	var w pbWriter
	w.varint(1, uint64(session)) // session
	w.boolean(9, muted)          // self_mute
	return w.b
}

// mumbleCreateRoomBody asks for a temporary channel: the server deletes it
// when the last person leaves, and moves its creator in.
func mumbleCreateRoomBody(parent uint32, name string) []byte {
	var w pbWriter
	w.varint(2, uint64(parent)) // parent
	w.str(3, name)              // name
	w.boolean(8, true)          // temporary
	return w.b
}

// ── Messages JukaHub reads ───────────────────────────────────────────────

type mumbleChannelMsg struct {
	id, parent         uint32
	name               string
	hasParent, hasName bool
}

func mumbleParseChannel(b []byte) (mumbleChannelMsg, error) {
	var m mumbleChannelMsg
	err := pbEach(b, func(f pbField) {
		switch {
		case f.num == 1 && f.wire == 0:
			m.id = uint32(f.u)
		case f.num == 2 && f.wire == 0:
			m.parent, m.hasParent = uint32(f.u), true
		case f.num == 3 && f.wire == 2:
			m.name, m.hasName = string(f.b), true
		}
	})
	return m, err
}

type mumbleUserMsg struct {
	session, channel     uint32
	name                 string
	hasName, hasChannel  bool
	selfMute, serverMute bool
	hasSelfMute, hasMute bool
}

func mumbleParseUser(b []byte) (mumbleUserMsg, error) {
	var m mumbleUserMsg
	err := pbEach(b, func(f pbField) {
		if f.wire != 0 && f.num != 3 {
			return
		}
		switch f.num {
		case 1:
			m.session = uint32(f.u)
		case 3:
			if f.wire == 2 {
				m.name, m.hasName = string(f.b), true
			}
		case 5:
			m.channel, m.hasChannel = uint32(f.u), true
		case 6:
			m.serverMute, m.hasMute = f.u != 0, true
		case 9:
			m.selfMute, m.hasSelfMute = f.u != 0, true
		}
	})
	return m, err
}

// mumbleParseUint reads one varint field (ServerSync.session,
// ChannelRemove.channel_id, UserRemove.session, Reject.type …).
func mumbleParseUint(b []byte, field int) (uint64, bool) {
	var v uint64
	found := false
	if pbEach(b, func(f pbField) {
		if f.num == field && f.wire == 0 {
			v, found = f.u, true
		}
	}) != nil {
		return 0, false
	}
	return v, found
}

// mumbleParseString reads one string field (Reject.reason,
// PermissionDenied.reason …).
func mumbleParseString(b []byte, field int) string {
	s := ""
	_ = pbEach(b, func(f pbField) {
		if f.num == field && f.wire == 2 {
			s = string(f.b)
		}
	})
	return s
}

// ── Mumble's own variable-length integers (voice packets) ────────────────

// mumbleAppendVarint writes v as Mumble's PacketDataStream does. Voice
// packets only carry non-negative numbers, so the negative forms are not
// written.
func mumbleAppendVarint(b []byte, v uint64) []byte {
	switch {
	case v < 0x80:
		return append(b, byte(v))
	case v < 0x4000:
		return append(b, byte(v>>8)|0x80, byte(v))
	case v < 0x200000:
		return append(b, byte(v>>16)|0xC0, byte(v>>8), byte(v))
	case v < 0x10000000:
		return append(b, byte(v>>24)|0xE0, byte(v>>16), byte(v>>8), byte(v))
	case v < 0x100000000:
		return append(b, 0xF0, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
	b = append(b, 0xF4)
	return binary.BigEndian.AppendUint64(b, v)
}

// mumbleReadVarint decodes one number; n is 0 when b is too short or
// malformed.
func mumbleReadVarint(b []byte) (v uint64, n int) {
	return mumbleReadVarintDepth(b, 0)
}

// mumbleReadVarintDepth allows the negative prefix (0xF8) once: Mumble never
// nests it, and a hostile run of them must not recurse a million times.
func mumbleReadVarintDepth(b []byte, depth int) (v uint64, n int) {
	if len(b) == 0 {
		return 0, 0
	}
	c := b[0]
	switch {
	case c&0x80 == 0:
		return uint64(c), 1
	case c&0xC0 == 0x80:
		if len(b) < 2 {
			return 0, 0
		}
		return uint64(c&0x3F)<<8 | uint64(b[1]), 2
	case c&0xF0 == 0xF0:
		switch c & 0xFC {
		case 0xF0:
			if len(b) < 5 {
				return 0, 0
			}
			return uint64(binary.BigEndian.Uint32(b[1:])), 5
		case 0xF4:
			if len(b) < 9 {
				return 0, 0
			}
			return binary.BigEndian.Uint64(b[1:]), 9
		case 0xF8:
			if depth > 0 {
				return 0, 0
			}
			inner, m := mumbleReadVarintDepth(b[1:], depth+1)
			if m == 0 {
				return 0, 0
			}
			return ^inner, 1 + m
		}
		return ^uint64(c & 0x03), 1
	case c&0xF0 == 0xE0:
		if len(b) < 4 {
			return 0, 0
		}
		return uint64(c&0x0F)<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3]), 4
	}
	// 110xxxxx
	if len(b) < 3 {
		return 0, 0
	}
	return uint64(c&0x1F)<<16 | uint64(b[1])<<8 | uint64(b[2]), 3
}

// ── Voice packets ────────────────────────────────────────────────────────

// mumbleVoice is one Opus frame a server forwarded from someone in the room.
type mumbleVoice struct {
	session uint32
	opus    []byte // aliases the packet
	last    bool   // the end of a talk spurt
}

// mumbleParseVoice reads a server-to-client legacy voice packet: header byte
// (codec<<5 | target), sender session, sequence, Opus size and data, then
// optional positional audio that is ignored. Other codecs and pings report
// false.
func mumbleParseVoice(p []byte) (mumbleVoice, bool) {
	if len(p) < 1 || p[0]>>5 != mumbleVoiceOpus {
		return mumbleVoice{}, false
	}
	p = p[1:]
	session, n := mumbleReadVarint(p)
	if n == 0 {
		return mumbleVoice{}, false
	}
	p = p[n:]
	if _, n = mumbleReadVarint(p); n == 0 { // sequence
		return mumbleVoice{}, false
	}
	p = p[n:]
	size, n := mumbleReadVarint(p)
	if n == 0 {
		return mumbleVoice{}, false
	}
	p = p[n:]
	length := int(size & mumbleOpusSizeMask)
	if length > len(p) {
		return mumbleVoice{}, false
	}
	return mumbleVoice{session: uint32(session), opus: p[:length], last: size&mumbleOpusTerminator != 0}, true
}

// mumbleVoicePacket builds a client-to-server voice packet for normal
// talking (target 0). seq counts 10 ms frames, as Mumble's own client does.
func mumbleVoicePacket(dst []byte, seq uint64, opus []byte, last bool) []byte {
	dst = append(dst[:0], mumbleVoiceOpus<<5)
	dst = mumbleAppendVarint(dst, seq)
	size := uint64(len(opus))
	if last {
		size |= mumbleOpusTerminator
	}
	dst = mumbleAppendVarint(dst, size)
	return append(dst, opus...)
}

// ── Framing ──────────────────────────────────────────────────────────────

func mumbleFrame(kind uint16, body []byte) []byte {
	buf := make([]byte, 6+len(body))
	binary.BigEndian.PutUint16(buf, kind)
	binary.BigEndian.PutUint32(buf[2:], uint32(len(body)))
	copy(buf[6:], body)
	return buf
}

// mumbleReadMsg reads one control message into buf (grown when needed) and
// returns its type and body; the body is only valid until the next read.
func mumbleReadMsg(r io.Reader, buf *[]byte) (uint16, []byte, error) {
	var hdr [6]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	kind := binary.BigEndian.Uint16(hdr[:])
	n := binary.BigEndian.Uint32(hdr[2:])
	if n > mumbleMaxMessage {
		return 0, nil, errMumbleProto
	}
	if int(n) > cap(*buf) {
		*buf = make([]byte, n)
	}
	body := (*buf)[:n]
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return kind, body, nil
}
