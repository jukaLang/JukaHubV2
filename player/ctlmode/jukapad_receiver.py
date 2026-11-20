#!/usr/bin/env python3
"""JukaPad receiver: turns a JukaHub handheld into a game controller on this PC.

Run it on the PC, then open Controller Mode on the handheld and choose
"Wi-Fi to a PC". The first time, both screens show the same 6-digit code:
press Enter here and A on the handheld. After that the handheld connects by
itself whenever both are on the same network.

  Windows  Xbox 360 controller through ViGEmBus (pip install vgamepad);
           --pad ds4 for a DualShock 4 instead.
  Linux    an Xbox 360-like pad through /dev/uinput (no extra packages).
  macOS    not supported (macOS has no virtual-controller interface).

Options: --name NAME  --port 8320  --tsp HANDHELD_IP  --pad xbox|ds4
         --selftest (checks the whole protocol offline)  --vectors

Python 3.8+, standard library only (plus vgamepad on Windows). The protocol
is described in JukaHub's player/ctlmode_wifi_proto.go; this file and that
one must change together.
"""

import argparse
import hashlib
import hmac
import json
import os
import secrets
import select
import socket
import struct
import sys
import tempfile
import threading
import time
import zlib

# ── Protocol constants (keep in step with ctlmode_wifi_proto.go) ──────────

MAGIC = b"JP"
VERSION = 1
RECEIVER_PORT = 8320
HANDHELD_PORT = 8323

T_PROBE = 0x01
T_ANNOUNCE = 0x02
T_PAIR_HELLO = 0x10
T_PAIR_COMMIT = 0x11
T_PAIR_NONCE = 0x12
T_PAIR_REVEAL = 0x13
T_PAIR_CONFIRM = 0x14
T_PAIR_DONE = 0x15
T_PAIR_FAIL = 0x16
T_HELLO = 0x20
T_CHALLENGE = 0x21
T_NOT_PAIRED = 0x22
T_PROOF = 0x23
T_STATE = 0x30
T_FEEDBACK = 0x31
T_BYE_H = 0x3E
T_BYE_R = 0x3F

FAIL_REFUSED = 1
FAIL_BUSY = 2
FAIL_TAMPER = 3
FAIL_TIMEOUT = 4

STATE_LEN = 34
FEEDBACK_LEN = 24
BYE_LEN = 20
MAC_LEN = 8

PAIR_TIMEOUT = 120.0
# A bare Enter is not tied to the code the user read: a stranger who knows the
# handheld's id (it is in every probe) can replace the pairing just before the
# key press. Enter counts only once the current code has been on screen this
# long.
CONFIRM_GRACE = 1.5
SILENCE_RELEASE = 1.0
SESSION_TTL = 15.0
FEEDBACK_IDLE = 1.0
FEEDBACK_RUMBLE = 0.2
RUMBLE_MS = 300

# Button bits: positional with Xbox names (ctlBtn* in player/ctlmode.go).
B_A, B_B, B_X, B_Y = 1 << 0, 1 << 1, 1 << 2, 1 << 3
B_LB, B_RB, B_LT, B_RT = 1 << 4, 1 << 5, 1 << 6, 1 << 7
B_BACK, B_START, B_GUIDE = 1 << 8, 1 << 9, 1 << 10
B_LS, B_RS = 1 << 11, 1 << 12
B_UP, B_DOWN, B_LEFT, B_RIGHT = 1 << 13, 1 << 14, 1 << 15, 1 << 16

ZERO16 = bytes(16)

# ── X25519 (RFC 7748), pure Python ────────────────────────────────────────

_P = 2 ** 255 - 19
_A24 = 121665
X25519_BASE = (9).to_bytes(32, "little")


def x25519(scalar, u):
    """RFC 7748 X25519: scalar and u are 32 bytes; returns 32 bytes."""
    k = bytearray(scalar)
    k[0] &= 248
    k[31] &= 127
    k[31] |= 64
    k = int.from_bytes(bytes(k), "little")
    ub = bytearray(u)
    ub[31] &= 127
    x1 = int.from_bytes(bytes(ub), "little") % _P
    x2, z2, x3, z3 = 1, 0, x1, 1
    swap = 0
    for t in range(254, -1, -1):
        kt = (k >> t) & 1
        swap ^= kt
        if swap:
            x2, x3 = x3, x2
            z2, z3 = z3, z2
        swap = kt
        a = (x2 + z2) % _P
        aa = a * a % _P
        b = (x2 - z2) % _P
        bb = b * b % _P
        e = (aa - bb) % _P
        c = (x3 + z3) % _P
        d = (x3 - z3) % _P
        da = d * a % _P
        cb = c * b % _P
        x3 = (da + cb) * (da + cb) % _P
        z3 = x1 * (da - cb) * (da - cb) % _P
        x2 = aa * bb % _P
        z2 = e * (aa + _A24 * e) % _P
    if swap:
        x2, x3 = x3, x2
        z2, z3 = z3, z2
    return (x2 * pow(z2, _P - 2, _P) % _P).to_bytes(32, "little")


# ── Key schedule ──────────────────────────────────────────────────────────


def _sha256(*parts):
    h = hashlib.sha256()
    for p in parts:
        h.update(p)
    return h.digest()


def _hmac(key, *parts):
    h = hmac.new(key, digestmod=hashlib.sha256)
    for p in parts:
        h.update(p)
    return h.digest()


def hkdf_sha256(ikm, salt, info, length=32):
    prk = _hmac(salt, ikm)
    out, t, i = b"", b"", 1
    while len(out) < length:
        t = _hmac(prk, t, info, bytes([i]))
        out += t
        i += 1
    return out[:length]


def commit_hash(pkb, pka, nb):
    """The receiver's commitment, sent before it sees the handheld's nonce."""
    return _sha256(b"jukapad-commit-v1", pkb, pka, nb)


def pair_code(pka, pkb, na, nb):
    """The 6-digit code both screens show."""
    d = _sha256(b"jukapad-code-v1", pka, pkb, na, nb)
    return "%06d" % (int.from_bytes(d[:4], "big") % 1000000)


def derive_ltk(z, na, nb, hid, rid, pka, pkb):
    return hkdf_sha256(z, na + nb, b"jukapad-ltk-v1" + hid + rid + pka + pkb, 32)


def confirm_tag(ltk, who, pid):
    return _hmac(ltk, b"jukapad-confirm-" + who, pid)[:16]


def proof_tag(ltk, who, hid, rid, na, nb):
    return _hmac(ltk, b"jukapad-proof-" + who, hid, rid, na, nb)[:16]


def session_key(ltk, hid, rid, na, nb):
    return _hmac(ltk, b"jukapad-session-v1", hid, rid, na, nb)


def session_id(sk):
    sid = struct.unpack("<I", _hmac(sk, b"jukapad-sid")[:4])[0]
    return sid or 1


def fmt_code(code):
    return code[:3] + " " + code[3:] if len(code) == 6 else code


# ── Packets ───────────────────────────────────────────────────────────────


def hdr(t):
    return MAGIC + bytes([VERSION, t])


def pack_name(name):
    raw = name.encode("utf-8", "replace")[:32]
    while raw:
        try:
            raw.decode("utf-8")
            break
        except UnicodeDecodeError:
            raw = raw[:-1]
    return bytes([len(raw)]) + raw


def read_name(buf, off):
    if off >= len(buf):
        return ""
    n = buf[off]
    raw = buf[off + 1:off + 1 + n]
    s = raw.decode("utf-8", "replace")
    return "".join(ch for ch in s if ch.isprintable()).strip()[:32]


def _mac(sk, body):
    return _hmac(sk, body)[:MAC_LEN]


def pack_state(sk, sid, seq, buttons, lx, ly, rx, ry, lt, rt):
    body = hdr(T_STATE) + struct.pack("<IIIhhhhBB", sid, seq, buttons, lx, ly, rx, ry, lt, rt)
    return body + _mac(sk, body)


def unpack_state(sk, pkt):
    """(sid, seq, buttons, lx, ly, rx, ry, lt, rt) or None when forged/malformed."""
    if len(pkt) != STATE_LEN or pkt[:4] != hdr(T_STATE):
        return None
    if not hmac.compare_digest(_mac(sk, pkt[:-MAC_LEN]), pkt[-MAC_LEN:]):
        return None
    return struct.unpack("<IIIhhhhBB", pkt[4:-MAC_LEN])


def pack_feedback(sk, sid, seq, large, small, ms):
    body = hdr(T_FEEDBACK) + struct.pack("<IIBBH", sid, seq, large, small, ms)
    return body + _mac(sk, body)


def unpack_feedback(sk, pkt):
    if len(pkt) != FEEDBACK_LEN or pkt[:4] != hdr(T_FEEDBACK):
        return None
    if not hmac.compare_digest(_mac(sk, pkt[:-MAC_LEN]), pkt[-MAC_LEN:]):
        return None
    return struct.unpack("<IIBBH", pkt[4:-MAC_LEN])


def pack_bye(sk, t, sid, seq):
    body = hdr(t) + struct.pack("<II", sid, seq)
    return body + _mac(sk, body)


def unpack_bye(sk, t, pkt):
    if len(pkt) != BYE_LEN or pkt[:4] != hdr(t):
        return None
    if not hmac.compare_digest(_mac(sk, pkt[:-MAC_LEN]), pkt[-MAC_LEN:]):
        return None
    return struct.unpack("<II", pkt[4:-MAC_LEN])


def pack_announce(rid, hid, paired, name):
    return hdr(T_ANNOUNCE) + rid + hid + bytes([1 if paired else 0]) + pack_name(name)


# ── DSU (cemuhook) data packet: only for the Go test's vectors ────────────


def dsu_axis(v):
    """-32768..32767 (+ = right / up) onto 0..255 with 128 at rest."""
    if v < -32767:
        v = -32767
    if v >= 0:
        return 128 + v * 127 // 32767
    return 128 - (-v) * 128 // 32767


def dsu_packet(server_id, msg_type, payload):
    body = struct.pack("<I", msg_type) + payload
    pkt = bytearray(b"DSUS" + struct.pack("<HHII", 1001, len(body), 0, server_id) + body)
    pkt[8:12] = struct.pack("<I", zlib.crc32(bytes(pkt)) & 0xFFFFFFFF)
    return bytes(pkt)


def dsu_data_packet(server_id, mac6, battery, number, st, ts_us):
    buttons, lx, ly, rx, ry, lt, rt = st
    b1 = 0
    for bit, val in ((B_LEFT, 0x80), (B_DOWN, 0x40), (B_RIGHT, 0x20), (B_UP, 0x10),
                     (B_START, 0x08), (B_RS, 0x04), (B_LS, 0x02), (B_BACK, 0x01)):
        if buttons & bit:
            b1 |= val
    b2 = 0
    # cemuhook names the face buttons the Nintendo way, by position: its Y, B,
    # A, X are left, bottom, right, top (Square, Cross, Circle, Triangle) --
    # Xbox X, A, B, Y.
    for bit, val in ((B_X, 0x80), (B_A, 0x40), (B_B, 0x20), (B_Y, 0x10),
                     (B_RB, 0x08), (B_LB, 0x04), (B_RT, 0x02), (B_LT, 0x01)):
        if buttons & bit:
            b2 |= val

    def an(bit):
        return 255 if buttons & bit else 0

    p = struct.pack("<BBBB", 0, 2, 1, 0) + mac6 + bytes([battery])
    p += bytes([1]) + struct.pack("<I", number)
    p += bytes([b1, b2, 1 if buttons & B_GUIDE else 0, 0])
    p += bytes([dsu_axis(lx), dsu_axis(-ly if ly > -32768 else 32767), dsu_axis(rx),
                dsu_axis(-ry if ry > -32768 else 32767)])
    p += bytes([an(B_LEFT), an(B_DOWN), an(B_RIGHT), an(B_UP),
                an(B_X), an(B_A), an(B_B), an(B_Y), an(B_RB), an(B_LB), rt, lt])
    p += bytes(12)
    p += struct.pack("<Q", ts_us) + bytes(24)
    return dsu_packet(server_id, 0x100002, p)


# ── Paired handhelds store ────────────────────────────────────────────────


class Store:
    """~/.jukapad/paired.json: this receiver's id and the handhelds it trusts."""

    def __init__(self, path):
        self.path = path
        self.rid = None
        self.handhelds = {}
        self._load()

    def _load(self):
        try:
            with open(self.path, "r", encoding="utf-8") as f:
                data = json.load(f)
            rid = bytes.fromhex(data.get("id", ""))
            if len(rid) == 16:
                self.rid = rid
            for hid_hex, ent in (data.get("handhelds") or {}).items():
                key = bytes.fromhex(ent.get("key", ""))
                if len(bytes.fromhex(hid_hex)) == 16 and len(key) == 32:
                    self.handhelds[hid_hex] = {"name": str(ent.get("name", ""))[:32], "key": key,
                                               "paired": str(ent.get("paired", ""))}
        except FileNotFoundError:
            pass
        except (ValueError, OSError, AttributeError, TypeError) as exc:
            print("Note: %s was unreadable (%s); starting fresh." % (self.path, exc))
        if self.rid is None:
            self.rid = secrets.token_bytes(16)
            self.save()

    def save(self):
        d = os.path.dirname(self.path)
        os.makedirs(d, mode=0o700, exist_ok=True)
        data = {"id": self.rid.hex(), "handhelds": {
            h: {"name": e["name"], "key": e["key"].hex(), "paired": e["paired"]}
            for h, e in self.handhelds.items()}}
        fd, tmp = tempfile.mkstemp(prefix=".paired-", dir=d)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                json.dump(data, f, indent=2)
            os.chmod(tmp, 0o600)
            os.replace(tmp, self.path)
        except BaseException:
            try:
                os.unlink(tmp)
            except OSError:
                pass
            raise

    def get(self, hid):
        return self.handhelds.get(hid.hex())

    def add(self, hid, name, key):
        self.handhelds[hid.hex()] = {"name": name, "key": key,
                                     "paired": time.strftime("%Y-%m-%d %H:%M:%S")}
        self.save()


def default_store_path():
    return os.path.join(os.path.expanduser("~"), ".jukapad", "paired.json")


# ── Virtual pads ──────────────────────────────────────────────────────────


class Pad:
    """A virtual controller. set_state takes SDL's convention (+Y down,
    sticks -32768..32767, triggers 0..255). rumble_cb(large, small, ms) is set
    by the receiver; ms 0 means "until changed"."""

    rumble_cb = None

    def set_state(self, buttons, lx, ly, rx, ry, lt, rt):
        raise NotImplementedError

    def release(self):
        self.set_state(0, 0, 0, 0, 0, 0, 0)

    def fileno(self):
        return None

    def poll(self):
        pass

    def close(self):
        pass


class FakePad(Pad):
    def __init__(self):
        self.states = []

    def set_state(self, buttons, lx, ly, rx, ry, lt, rt):
        self.states.append((buttons, lx, ly, rx, ry, lt, rt))


def _clamp16(v):
    return max(-32768, min(32767, v))


class VGamepadPad(Pad):
    """Windows: ViGEmBus through vgamepad. Xbox 360 (default) or DS4."""

    def __init__(self, kind):
        import vgamepad as vg  # noqa: imported here: only Windows has it
        self.vg = vg
        self.kind = kind
        if kind == "ds4":
            self.pad = vg.VDS4Gamepad()
            b = vg.DS4_BUTTONS
            self.map = [(B_A, b.DS4_BUTTON_CROSS), (B_B, b.DS4_BUTTON_CIRCLE),
                        (B_X, b.DS4_BUTTON_SQUARE), (B_Y, b.DS4_BUTTON_TRIANGLE),
                        (B_LB, b.DS4_BUTTON_SHOULDER_LEFT), (B_RB, b.DS4_BUTTON_SHOULDER_RIGHT),
                        (B_LT, b.DS4_BUTTON_TRIGGER_LEFT), (B_RT, b.DS4_BUTTON_TRIGGER_RIGHT),
                        (B_BACK, b.DS4_BUTTON_SHARE), (B_START, b.DS4_BUTTON_OPTIONS),
                        (B_LS, b.DS4_BUTTON_THUMB_LEFT), (B_RS, b.DS4_BUTTON_THUMB_RIGHT)]
        else:
            self.pad = vg.VX360Gamepad()
            b = vg.XUSB_BUTTON
            self.map = [(B_A, b.XUSB_GAMEPAD_A), (B_B, b.XUSB_GAMEPAD_B),
                        (B_X, b.XUSB_GAMEPAD_X), (B_Y, b.XUSB_GAMEPAD_Y),
                        (B_LB, b.XUSB_GAMEPAD_LEFT_SHOULDER), (B_RB, b.XUSB_GAMEPAD_RIGHT_SHOULDER),
                        (B_BACK, b.XUSB_GAMEPAD_BACK), (B_START, b.XUSB_GAMEPAD_START),
                        (B_GUIDE, b.XUSB_GAMEPAD_GUIDE),
                        (B_LS, b.XUSB_GAMEPAD_LEFT_THUMB), (B_RS, b.XUSB_GAMEPAD_RIGHT_THUMB),
                        (B_UP, b.XUSB_GAMEPAD_DPAD_UP), (B_DOWN, b.XUSB_GAMEPAD_DPAD_DOWN),
                        (B_LEFT, b.XUSB_GAMEPAD_DPAD_LEFT), (B_RIGHT, b.XUSB_GAMEPAD_DPAD_RIGHT)]
        self.pad.register_notification(callback_function=self._notify)
        self.pad.reset()
        self.pad.update()

    def _notify(self, client, target, large_motor, small_motor, led, user_data):
        cb = self.rumble_cb
        if cb:
            cb(int(large_motor) & 0xFF, int(small_motor) & 0xFF, 0)

    def set_state(self, buttons, lx, ly, rx, ry, lt, rt):
        p = self.pad
        for bit, vb in self.map:
            if buttons & bit:
                p.press_button(button=vb)
            else:
                p.release_button(button=vb)
        p.left_trigger(value=lt)
        p.right_trigger(value=rt)
        if self.kind == "ds4":
            vg = self.vg
            sp = vg.DS4_SPECIAL_BUTTONS.DS4_SPECIAL_BUTTON_PS
            if buttons & B_GUIDE:
                p.press_special_button(special_button=sp)
            else:
                p.release_special_button(special_button=sp)
            d = vg.DS4_DPAD_DIRECTIONS
            up, down = buttons & B_UP, buttons & B_DOWN
            left, right = buttons & B_LEFT, buttons & B_RIGHT
            if up and right:
                dp = d.DS4_BUTTON_DPAD_NORTHEAST
            elif up and left:
                dp = d.DS4_BUTTON_DPAD_NORTHWEST
            elif down and right:
                dp = d.DS4_BUTTON_DPAD_SOUTHEAST
            elif down and left:
                dp = d.DS4_BUTTON_DPAD_SOUTHWEST
            elif up:
                dp = d.DS4_BUTTON_DPAD_NORTH
            elif down:
                dp = d.DS4_BUTTON_DPAD_SOUTH
            elif left:
                dp = d.DS4_BUTTON_DPAD_WEST
            elif right:
                dp = d.DS4_BUTTON_DPAD_EAST
            else:
                dp = d.DS4_BUTTON_DPAD_NONE
            p.directional_pad(direction=dp)
            # DS4 sticks are 0..255 with 0 at the top, like SDL's +Y down.
            p.left_joystick(x_value=(lx + 32768) >> 8, y_value=(ly + 32768) >> 8)
            p.right_joystick(x_value=(rx + 32768) >> 8, y_value=(ry + 32768) >> 8)
        else:
            # XInput's +Y is up.
            p.left_joystick(x_value=lx, y_value=_clamp16(-ly))
            p.right_joystick(x_value=rx, y_value=_clamp16(-ry))
        p.update()

    def close(self):
        try:
            self.pad.unregister_notification()
        except Exception:  # noqa: best effort on exit
            pass
        try:
            self.pad.reset()
            self.pad.update()
        except Exception:  # noqa
            pass


def _ioc(direction, nr, size):
    return (direction << 30) | (size << 16) | (ord("U") << 8) | nr


class UInputPad(Pad):
    """Linux: an Xbox 360-like pad on /dev/uinput, with rumble (force feedback).

    It uses the Xbox 360 USB ids so SDL, Steam and games map it as one; the
    face buttons follow the kernel's xpad driver (X = BTN_X = BTN_NORTH's
    code, Y = BTN_Y = BTN_WEST's code), which is what those mappings expect.
    """

    EV_SYN, EV_KEY, EV_ABS, EV_FF, EV_UINPUT = 0x00, 0x01, 0x03, 0x15, 0x0101
    ABS_X, ABS_Y, ABS_Z, ABS_RX, ABS_RY, ABS_RZ = 0x00, 0x01, 0x02, 0x03, 0x04, 0x05
    ABS_HAT0X, ABS_HAT0Y = 0x10, 0x11
    FF_RUMBLE, FF_GAIN = 0x50, 0x60
    KEYS = [(B_A, 0x130), (B_B, 0x131), (B_X, 0x133), (B_Y, 0x134),
            (B_LB, 0x136), (B_RB, 0x137), (B_BACK, 0x13A), (B_START, 0x13B),
            (B_GUIDE, 0x13C), (B_LS, 0x13D), (B_RS, 0x13E)]

    def __init__(self, name="JukaPad (Xbox 360 pad)"):
        import fcntl
        self.fcntl = fcntl
        self.fd = os.open("/dev/uinput", os.O_RDWR | os.O_NONBLOCK)
        self.ev_fmt = "llHHi"
        self.ev_size = struct.calcsize(self.ev_fmt)
        ptr64 = struct.calcsize("P") == 8
        self.ff_size = 48 if ptr64 else 44
        self.ui_set_evbit = _ioc(1, 100, 4)
        ui_set_keybit = _ioc(1, 101, 4)
        ui_set_absbit = _ioc(1, 103, 4)
        ui_set_ffbit = _ioc(1, 107, 4)
        up_size = 8 + 2 * self.ff_size
        self.ui_begin_ff_upload = _ioc(3, 200, up_size)
        self.ui_end_ff_upload = _ioc(1, 201, up_size)
        self.ui_begin_ff_erase = _ioc(3, 202, 12)
        self.ui_end_ff_erase = _ioc(1, 203, 12)
        self.effects = {}
        self.last = None
        try:
            for ev in (self.EV_KEY, self.EV_ABS, self.EV_FF):
                fcntl.ioctl(self.fd, self.ui_set_evbit, ev)
            for _, code in self.KEYS:
                fcntl.ioctl(self.fd, ui_set_keybit, code)
            absmax, absmin = [0] * 64, [0] * 64
            absfuzz, absflat = [0] * 64, [0] * 64
            for a in (self.ABS_X, self.ABS_Y, self.ABS_RX, self.ABS_RY):
                fcntl.ioctl(self.fd, ui_set_absbit, a)
                absmin[a], absmax[a], absfuzz[a], absflat[a] = -32767, 32767, 16, 128
            for a in (self.ABS_Z, self.ABS_RZ):
                fcntl.ioctl(self.fd, ui_set_absbit, a)
                absmin[a], absmax[a] = 0, 255
            for a in (self.ABS_HAT0X, self.ABS_HAT0Y):
                fcntl.ioctl(self.fd, ui_set_absbit, a)
                absmin[a], absmax[a] = -1, 1
            fcntl.ioctl(self.fd, ui_set_ffbit, self.FF_RUMBLE)
            dev = struct.pack("80sHHHHI", name.encode()[:79], 0x03, 0x045E, 0x028E, 0x0110, 16)
            dev += struct.pack("64i", *absmax) + struct.pack("64i", *absmin)
            dev += struct.pack("64i", *absfuzz) + struct.pack("64i", *absflat)
            os.write(self.fd, dev)
            fcntl.ioctl(self.fd, _ioc(0, 1, 0))  # UI_DEV_CREATE
        except BaseException:
            os.close(self.fd)
            raise

    def _emit(self, events):
        buf = b"".join(struct.pack(self.ev_fmt, 0, 0, t, c, v) for t, c, v in events)
        buf += struct.pack(self.ev_fmt, 0, 0, self.EV_SYN, 0, 0)
        os.write(self.fd, buf)

    def set_state(self, buttons, lx, ly, rx, ry, lt, rt):
        state = (buttons, lx, ly, rx, ry, lt, rt)
        if state == self.last:
            return
        self.last = state
        ev = [(self.EV_KEY, code, 1 if buttons & bit else 0) for bit, code in self.KEYS]
        ev += [(self.EV_ABS, self.ABS_X, max(-32767, lx)), (self.EV_ABS, self.ABS_Y, max(-32767, ly)),
               (self.EV_ABS, self.ABS_RX, max(-32767, rx)), (self.EV_ABS, self.ABS_RY, max(-32767, ry)),
               (self.EV_ABS, self.ABS_Z, lt), (self.EV_ABS, self.ABS_RZ, rt)]
        hx = -1 if buttons & B_LEFT else (1 if buttons & B_RIGHT else 0)
        hy = -1 if buttons & B_UP else (1 if buttons & B_DOWN else 0)
        ev += [(self.EV_ABS, self.ABS_HAT0X, hx), (self.EV_ABS, self.ABS_HAT0Y, hy)]
        self._emit(ev)

    def fileno(self):
        return self.fd

    def poll(self):
        """Force feedback: uploads, erases and play/stop requests from games."""
        while True:
            try:
                raw = os.read(self.fd, self.ev_size * 16)
            except (BlockingIOError, InterruptedError):
                return
            except OSError:
                return
            if not raw:
                return
            for off in range(0, len(raw) - self.ev_size + 1, self.ev_size):
                _, _, t, code, value = struct.unpack_from(self.ev_fmt, raw, off)
                if t == self.EV_UINPUT and code == 1:
                    self._upload(value)
                elif t == self.EV_UINPUT and code == 2:
                    self._erase(value)
                elif t == self.EV_FF and code != self.FF_GAIN:
                    self._play(code, value)

    def _upload(self, request_id):
        buf = bytearray(struct.pack("Ii", request_id, 0) + bytes(2 * self.ff_size))
        self.fcntl.ioctl(self.fd, self.ui_begin_ff_upload, buf, True)
        etype, eid = struct.unpack_from("Hh", buf, 8)
        length = struct.unpack_from("H", buf, 8 + 10)[0]
        strong, weak = struct.unpack_from("HH", buf, 8 + 16)
        if etype == self.FF_RUMBLE:
            self.effects[eid] = (strong >> 8, weak >> 8, length)
        struct.pack_into("i", buf, 4, 0)
        self.fcntl.ioctl(self.fd, self.ui_end_ff_upload, buf)

    def _erase(self, request_id):
        buf = bytearray(struct.pack("IiI", request_id, 0, 0))
        self.fcntl.ioctl(self.fd, self.ui_begin_ff_erase, buf, True)
        self.effects.pop(struct.unpack_from("I", buf, 8)[0], None)
        struct.pack_into("i", buf, 4, 0)
        self.fcntl.ioctl(self.fd, self.ui_end_ff_erase, buf)

    def _play(self, eid, value):
        cb = self.rumble_cb
        if not cb:
            return
        eff = self.effects.get(eid)
        if value and eff:
            cb(eff[0], eff[1], eff[2])
        else:
            cb(0, 0, 0)

    def close(self):
        try:
            self.release()
            self.fcntl.ioctl(self.fd, _ioc(0, 2, 0))  # UI_DEV_DESTROY
        except OSError:
            pass
        os.close(self.fd)


def choose_pad_factory(kind):
    """A function making one pad per handheld, or SystemExit with advice."""
    if sys.platform == "darwin":
        raise SystemExit("macOS is not supported: it has no virtual game controller interface.\n"
                         "Use a Windows or Linux PC, or the DSU link for emulators "
                         "(Controller Mode -> DSU on the handheld).")
    if sys.platform.startswith("win"):
        try:
            import vgamepad  # noqa: F401
        except ImportError:
            raise SystemExit("vgamepad is missing. Install it with:\n"
                             "  py -m pip install --user vgamepad\n"
                             "(it also installs the ViGEmBus driver), or run jukapad-receiver.bat.")
        except Exception as exc:  # vgamepad raises when ViGEmBus is not installed
            raise SystemExit("vgamepad could not start (%s).\n"
                             "Install the ViGEmBus driver (https://github.com/nefarius/ViGEmBus/releases) "
                             "and try again." % exc)
        return lambda: VGamepadPad(kind)
    if sys.platform.startswith("linux"):
        if kind == "ds4":
            print("Note: --pad ds4 is Windows-only; using the Xbox 360-like pad.")
        try:
            UInputPad().close()
        except FileNotFoundError:
            raise SystemExit("/dev/uinput is missing. Load it with: sudo modprobe uinput")
        except PermissionError:
            raise SystemExit("/dev/uinput is not writable by this user. Either run once with sudo, or allow it:\n"
                             "  echo 'KERNEL==\"uinput\", MODE=\"0660\", GROUP=\"input\", OPTIONS+=\"static_node=uinput\"'"
                             " | sudo tee /etc/udev/rules.d/60-jukapad.rules\n"
                             "  sudo usermod -aG input $USER   (then log out and in)\n"
                             "  sudo modprobe uinput && sudo udevadm trigger")
        return UInputPad
    raise SystemExit("This system (%s) is not supported: use Windows or Linux." % sys.platform)


# ── Receiver ──────────────────────────────────────────────────────────────


class Pairing:
    def __init__(self, pid, hid, name, addr, pka):
        self.pid, self.hid, self.name, self.addr, self.pka = pid, hid, name, addr, pka
        self.sk = secrets.token_bytes(32)
        self.pkb = x25519(self.sk, X25519_BASE)
        self.nb = secrets.token_bytes(32)
        self.cb = None
        self.na = None
        self.ltk = None
        self.code = None
        self.shown = 0.0
        self.user_ok = None
        self.h_ok = False
        self.finished = False
        self.started = time.monotonic()


class Session:
    def __init__(self, hid, name, addr, sk, sid, pad):
        self.hid, self.name, self.addr, self.sk, self.sid, self.pad = hid, name, addr, sk, sid, pad
        self.rx_seq = 0
        self.tx_seq = 0
        self.last_rx = time.monotonic()
        self.released = True
        self.motors = (0, 0)
        self.motors_until = None
        self.next_fb = 0.0


class Receiver:
    """The JukaPad receiver. confirm(pairing) is called when a code is ready;
    the answer comes back through answer(yes)."""

    def __init__(self, name, store, pad_factory, confirm, port=RECEIVER_PORT, bind="0.0.0.0",
                 tsp=None, tsp_port=HANDHELD_PORT, log=print, silence=SILENCE_RELEASE,
                 confirm_grace=CONFIRM_GRACE):
        self.name = name[:32]
        self.store = store
        self.rid = store.rid
        self.pad_factory = pad_factory
        self.confirm = confirm
        self.confirm_grace = confirm_grace
        self.tsp = (tsp, tsp_port) if tsp else None
        self.log = log
        self.silence = silence
        self.lock = threading.RLock()
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.bind((bind, port))
        self.port = self.sock.getsockname()[1]
        self.pairing = None
        self.pending = {}
        self.sessions = {}
        self.pads = {}
        self.running = True
        self.next_tsp = 0.0

    # The loop.
    def serve_forever(self):
        while self.running:
            fds = [self.sock]
            with self.lock:
                for pad in self.pads.values():
                    if pad.fileno() is not None:
                        fds.append(pad)
            try:
                ready, _, _ = select.select(fds, [], [], 0.1)
            except (OSError, ValueError):
                if not self.running:
                    break
                raise
            for r in ready:
                if r is self.sock:
                    try:
                        data, addr = self.sock.recvfrom(2048)
                    except OSError:
                        continue
                    with self.lock:
                        try:
                            self.handle(data, addr)
                        except Exception as exc:  # a malformed packet must not stop the receiver
                            self.log("Ignored a bad packet from %s: %r" % (addr[0], exc))
                else:
                    with self.lock:
                        try:
                            r.poll()
                        except OSError as exc:  # a failed rumble upload must not stop the receiver
                            self.log("Rumble request failed: %r" % (exc,))
            with self.lock:
                self.tick(time.monotonic())

    def stop(self):
        with self.lock:
            self.running = False
            for s in list(self.sessions.values()):
                s.tx_seq += 1
                self._send(pack_bye(s.sk, T_BYE_R, s.sid, s.tx_seq), s.addr)
                s.pad.release()
            self.sessions.clear()
            for pad in self.pads.values():
                pad.close()
            self.pads.clear()
        try:
            self.sock.close()
        except OSError:
            pass

    def _send(self, pkt, addr):
        try:
            self.sock.sendto(pkt, addr)
        except OSError:
            pass

    def handle(self, data, addr):
        if len(data) < 4 or data[:2] != MAGIC or data[2] != VERSION:
            return
        t, body = data[3], data[4:]
        if t == T_PROBE and len(body) >= 17:
            hid = body[:16]
            self._send(pack_announce(self.rid, hid, self.store.get(hid) is not None, self.name), addr)
        elif t == T_PAIR_HELLO and len(body) >= 57:
            self._pair_hello(body[:8], body[8:24], body[24:56], read_name(body, 56), addr)
        elif t == T_PAIR_NONCE and len(body) >= 40:
            self._pair_nonce(body[:8], body[8:40], addr)
        elif t == T_PAIR_CONFIRM and len(body) >= 24:
            self._pair_confirm(body[:8], body[8:24], addr)
        elif t == T_PAIR_FAIL and len(body) >= 9:
            p = self.pairing
            if p and p.pid == body[:8] and not p.finished:
                self.log("Pairing with %s was cancelled on the handheld." % (p.name or "the handheld"))
                self.pairing = None
        elif t == T_HELLO and len(body) >= 48:
            self._hello(body[:16], body[16:32], body[32:48], addr)
        elif t == T_PROOF and len(body) >= 48:
            self._proof(body[:16], body[16:32], body[32:48], addr)
        elif t == T_STATE:
            self._state(data, addr)
        elif t == T_BYE_H:
            self._bye(data)

    # Pairing (the handheld initiates; this side commits first).
    def _pair_hello(self, pid, hid, pka, name, addr):
        p = self.pairing
        now = time.monotonic()
        if p and p.pid == pid:
            if p.pka == pka:
                self._send(hdr(T_PAIR_COMMIT) + pid + self.rid + p.pkb + p.cb, addr)
            return
        if p and not p.finished and now - p.started < PAIR_TIMEOUT and p.hid != hid:
            self._send(hdr(T_PAIR_FAIL) + pid + bytes([FAIL_BUSY]), addr)
            return
        if p and p.code and not p.finished:
            self.log("The handheld started pairing again; the old code is void.")
        p = Pairing(pid, hid, name, addr, pka)
        p.cb = commit_hash(p.pkb, pka, p.nb)
        self.pairing = p
        self.log("Pairing request from %s (%s)." % (name or "a handheld", addr[0]))
        self._send(hdr(T_PAIR_COMMIT) + pid + self.rid + p.pkb + p.cb, addr)

    def _pair_nonce(self, pid, na, addr):
        p = self.pairing
        if not p or p.pid != pid:
            return
        if p.na is not None:
            if p.na == na:
                self._send(hdr(T_PAIR_REVEAL) + pid + p.nb, addr)
            else:
                self.log("Pairing stopped: the handheld's nonce changed (tampering?).")
                self._send(hdr(T_PAIR_FAIL) + pid + bytes([FAIL_TAMPER]), addr)
                self.pairing = None
            return
        z = x25519(p.sk, p.pka)
        if z == bytes(32):
            self._send(hdr(T_PAIR_FAIL) + pid + bytes([FAIL_TAMPER]), addr)
            self.pairing = None
            return
        p.na = na
        p.ltk = derive_ltk(z, na, p.nb, p.hid, self.rid, p.pka, p.pkb)
        p.code = pair_code(p.pka, p.pkb, na, p.nb)
        p.shown = time.monotonic()
        self._send(hdr(T_PAIR_REVEAL) + pid + p.nb, addr)
        self.confirm(p)

    def _pair_confirm(self, pid, tag, addr):
        p = self.pairing
        if not p or p.pid != pid or p.ltk is None:
            return
        if not hmac.compare_digest(confirm_tag(p.ltk, b"h", pid), tag):
            return
        p.h_ok = True
        p.addr = addr
        self._pair_maybe_finish(p)

    def _pair_maybe_finish(self, p):
        if p.h_ok and p.user_ok and not p.finished:
            p.finished = True
            self.store.add(p.hid, p.name, p.ltk)
            self.log("Paired with %s. It will connect on its own from now on." % (p.name or "the handheld"))
        if p.finished:
            self._send(hdr(T_PAIR_DONE) + p.pid + confirm_tag(p.ltk, b"r", p.pid), p.addr)

    def answer(self, yes):
        """The PC user's answer to the code on screen. False when nothing waits."""
        with self.lock:
            p = self.pairing
            if not p or p.code is None or p.user_ok is not None:
                return False
            if yes:
                if time.monotonic() - p.shown < self.confirm_grace:
                    self.log("The code just appeared: compare it with the handheld's, then press Enter again.")
                    return True
                p.user_ok = True
                self._pair_maybe_finish(p)
                if not p.finished:
                    self.log("Now choose Codes match on the handheld.")
            else:
                self._send(hdr(T_PAIR_FAIL) + p.pid + bytes([FAIL_REFUSED]), p.addr)
                self.log("Pairing refused.")
                self.pairing = None
            return True

    # Reconnect: fresh nonces both ways, mutual proof under the paired key.
    def _hello(self, hid, rid, na, addr):
        if rid != self.rid:
            return
        ent = self.store.get(hid)
        if ent is None:
            self._send(hdr(T_NOT_PAIRED) + self.rid + hid, addr)
            return
        pend = self.pending.get(hid)
        if pend is None or pend["na"] != na:
            nb = secrets.token_bytes(16)
            sk = session_key(ent["key"], hid, self.rid, na, nb)
            pend = {"na": na, "nb": nb, "sk": sk, "sid": session_id(sk), "at": time.monotonic()}
            self.pending[hid] = pend
        proof = proof_tag(ent["key"], b"r", hid, self.rid, na, pend["nb"])
        self._send(hdr(T_CHALLENGE) + self.rid + hid + na + pend["nb"] + proof, addr)

    def _proof(self, hid, nb, tag, addr):
        pend = self.pending.get(hid)
        ent = self.store.get(hid)
        if pend is None or ent is None or pend["nb"] != nb:
            return
        if not hmac.compare_digest(proof_tag(ent["key"], b"h", hid, self.rid, pend["na"], nb), tag):
            return
        self._install(hid, ent, pend, addr)

    def _install(self, hid, ent, pend, addr):
        self.pending.pop(hid, None)
        old = self.sessions.get(hid)
        if old is not None and old.sid == pend["sid"]:
            return
        pad = self.pads.get(hid)
        if pad is None:
            pad = self.pad_factory()
            self.pads[hid] = pad
        pad.rumble_cb = lambda large, small, ms, h=hid: self.rumble(h, large, small, ms)
        if old is not None:
            pad.release()
        s = Session(hid, ent["name"], addr, pend["sk"], pend["sid"], pad)
        self.sessions[hid] = s
        self.log("Connected: %s (%s)." % (s.name or "handheld", addr[0]))
        self._feedback(s, time.monotonic())

    def _find_session(self, sid):
        for s in self.sessions.values():
            if s.sid == sid:
                return s
        return None

    def _state(self, pkt, addr):
        if len(pkt) != STATE_LEN:
            return
        sid = struct.unpack_from("<I", pkt, 4)[0]
        s = self._find_session(sid)
        if s is None:
            # The PROOF may have been lost: a state under a pending key proves
            # the handheld holds it just as well.
            for hid, pend in list(self.pending.items()):
                if pend["sid"] == sid and unpack_state(pend["sk"], pkt) is not None:
                    ent = self.store.get(hid)
                    if ent is not None:
                        self._install(hid, ent, pend, addr)
                        s = self.sessions.get(hid)
                    break
            if s is None:
                return
        st = unpack_state(s.sk, pkt)
        if st is None or st[1] <= s.rx_seq:
            return
        s.rx_seq = st[1]
        s.last_rx = time.monotonic()
        s.addr = addr
        s.released = False
        s.pad.set_state(*st[2:])

    def _bye(self, pkt):
        if len(pkt) != BYE_LEN:
            return
        s = self._find_session(struct.unpack_from("<I", pkt, 4)[0])
        if s is None:
            return
        b = unpack_bye(s.sk, T_BYE_H, pkt)
        if b is None or b[1] <= s.rx_seq:
            return
        s.pad.release()
        del self.sessions[s.hid]
        self.log("%s disconnected." % (s.name or "The handheld"))

    # Rumble: from the pad (any thread).
    def rumble(self, hid, large, small, ms):
        with self.lock:
            s = self.sessions.get(hid)
            if s is None:
                return
            s.motors = (large, small)
            s.motors_until = time.monotonic() + ms / 1000.0 if ms else None
            self._feedback(s, time.monotonic())

    def _feedback(self, s, now):
        large, small = s.motors
        on = large or small
        s.tx_seq += 1
        self._send(pack_feedback(s.sk, s.sid, s.tx_seq, large, small, RUMBLE_MS if on else 0), s.addr)
        s.next_fb = now + (FEEDBACK_RUMBLE if on else FEEDBACK_IDLE)

    def tick(self, now):
        for hid, s in list(self.sessions.items()):
            if not s.released and now - s.last_rx > self.silence:
                s.pad.release()
                s.released = True
            if now - s.last_rx > SESSION_TTL:
                s.pad.release()
                del self.sessions[hid]
                self.log("Lost %s (no packets for %d s)." % (s.name or "the handheld", SESSION_TTL))
                continue
            if s.motors_until is not None and now >= s.motors_until:
                s.motors, s.motors_until = (0, 0), None
                self._feedback(s, now)
            elif now >= s.next_fb:
                self._feedback(s, now)
        for hid, pend in list(self.pending.items()):
            if now - pend["at"] > 10:
                del self.pending[hid]
        p = self.pairing
        if p and not p.finished and now - p.started > PAIR_TIMEOUT:
            self.log("Pairing timed out.")
            self._send(hdr(T_PAIR_FAIL) + p.pid + bytes([FAIL_TIMEOUT]), p.addr)
            self.pairing = None
        elif p and p.finished and now - p.started > PAIR_TIMEOUT + 30:
            self.pairing = None
        if self.tsp and now >= self.next_tsp:
            self.next_tsp = now + 2.0
            self._send(pack_announce(self.rid, ZERO16, False, self.name), self.tsp)


# ── Console ───────────────────────────────────────────────────────────────


def local_ipv4():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(("192.0.2.1", 9))  # no packet is sent: this only picks a route
        return s.getsockname()[0]
    except OSError:
        return "?"
    finally:
        s.close()


def console_confirm(p):
    print()
    print("=" * 60)
    print("  Pairing request from: %s" % (p.name or "a JukaHub handheld"))
    print("  Code:  %s" % fmt_code(p.code))
    print("  Does the handheld show the same code? Press Enter to pair,")
    print("  or type n and Enter to refuse.")
    print("=" * 60)
    sys.stdout.flush()


def stdin_loop(rcv):
    for line in sys.stdin:
        ans = line.strip().lower()
        if ans in ("", "y", "yes"):
            if not rcv.answer(True) and ans:
                print("(no pairing is waiting)")
        elif ans in ("n", "no"):
            rcv.answer(False)
        elif ans in ("q", "quit", "exit"):
            rcv.running = False
            return


def log_line(msg):
    print(time.strftime("%H:%M:%S ") + msg)
    sys.stdout.flush()


# ── Self-test: a fake handheld runs the whole protocol over loopback ─────


class FakeHandheld:
    def __init__(self, dest, name="Test handheld"):
        self.dest = dest
        self.name = name
        self.hid = secrets.token_bytes(16)
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.bind(("127.0.0.1", 0))
        self.sock.settimeout(0.05)
        self.ltk = None
        self.rid = None
        self.sk = None
        self.sid = None
        self.seq = 0
        self.fb_seq = 0
        self.code = None

    def send(self, pkt):
        self.sock.sendto(pkt, self.dest)

    def recv(self, want, timeout=3.0, resend=None):
        end = time.monotonic() + timeout
        nxt = 0.0
        while time.monotonic() < end:
            if resend is not None and time.monotonic() >= nxt:
                self.send(resend)
                nxt = time.monotonic() + 0.3
            try:
                data, _ = self.sock.recvfrom(2048)
            except socket.timeout:
                continue
            if len(data) >= 4 and data[:3] == MAGIC + bytes([VERSION]) and data[3] in want:
                return data
        return None

    def probe(self):
        pkt = self.recv((T_ANNOUNCE,), resend=hdr(T_PROBE) + self.hid + pack_name(self.name))
        if pkt is None:
            return None
        self.rid = pkt[4:20]
        return pkt[20:36] == self.hid, pkt[36] & 1 == 1, read_name(pkt, 37)

    def pair(self, tamper_nonce=False):
        sk = secrets.token_bytes(32)
        pka = x25519(sk, X25519_BASE)
        pid = secrets.token_bytes(8)
        c = self.recv((T_PAIR_COMMIT, T_PAIR_FAIL),
                      resend=hdr(T_PAIR_HELLO) + pid + self.hid + pka + pack_name(self.name))
        if c is None or c[3] != T_PAIR_COMMIT:
            return "no commit"
        rid, pkb, cb = c[12:28], c[28:60], c[60:92]
        na = secrets.token_bytes(32)
        r = self.recv((T_PAIR_REVEAL, T_PAIR_FAIL), resend=hdr(T_PAIR_NONCE) + pid + na)
        if r is None or r[3] != T_PAIR_REVEAL:
            return "no reveal"
        if tamper_nonce:
            f = self.recv((T_PAIR_FAIL,), resend=hdr(T_PAIR_NONCE) + pid + secrets.token_bytes(32))
            return "tamper-detected" if f is not None and f[12] == FAIL_TAMPER else "tamper-missed"
        nb = r[12:44]
        if not hmac.compare_digest(commit_hash(pkb, pka, nb), cb):
            return "bad commitment"
        z = x25519(sk, pkb)
        self.code = pair_code(pka, pkb, na, nb)
        ltk = derive_ltk(z, na, nb, self.hid, rid, pka, pkb)
        d = self.recv((T_PAIR_DONE, T_PAIR_FAIL), timeout=5.0,
                      resend=hdr(T_PAIR_CONFIRM) + pid + confirm_tag(ltk, b"h", pid))
        if d is None:
            return "no done"
        if d[3] == T_PAIR_FAIL:
            return "refused" if d[12] == FAIL_REFUSED else "failed %d" % d[12]
        if not hmac.compare_digest(d[12:28], confirm_tag(ltk, b"r", pid)):
            return "bad done tag"
        self.ltk, self.rid = ltk, rid
        return "ok"

    def connect(self, bad_proof=False):
        na = secrets.token_bytes(16)
        c = self.recv((T_CHALLENGE, T_NOT_PAIRED), resend=hdr(T_HELLO) + self.hid + self.rid + na)
        if c is None:
            return "no challenge"
        if c[3] == T_NOT_PAIRED:
            return "not paired"
        if c[4:20] != self.rid or c[20:36] != self.hid or c[36:52] != na:
            return "challenge mismatch"
        nb, proof = c[52:68], c[68:84]
        if not hmac.compare_digest(proof_tag(self.ltk, b"r", self.hid, self.rid, na, nb), proof):
            return "receiver proof wrong"
        tag = proof_tag(self.ltk, b"h", self.hid, self.rid, na, nb)
        if bad_proof:
            tag = bytes(16)
        self.sk = session_key(self.ltk, self.hid, self.rid, na, nb)
        self.sid = session_id(self.sk)
        self.seq = 0
        self.fb_seq = 0
        f = self.recv((T_FEEDBACK,), timeout=1.0, resend=hdr(T_PROOF) + self.hid + nb + tag)
        if f is None:
            return "no feedback"
        fb = unpack_feedback(self.sk, f)
        if fb is None or fb[0] != self.sid:
            return "bad feedback"
        self.fb_seq = fb[1]
        return "ok"

    def state(self, *st):
        self.seq += 1
        pkt = pack_state(self.sk, self.sid, self.seq, *st)
        self.send(pkt)
        return pkt

    def feedback(self, timeout=1.0, want=None):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            f = self.recv((T_FEEDBACK,), timeout=max(0.01, end - time.monotonic()))
            if f is None:
                return None
            fb = unpack_feedback(self.sk, f)
            if fb is None or fb[1] <= self.fb_seq:
                continue
            self.fb_seq = fb[1]
            if want is None or want(fb):
                return fb
        return None


def selftest():
    failures = []

    def check(cond, what):
        print(("  ok    " if cond else "  FAIL  ") + what)
        if not cond:
            failures.append(what)

    def wait_for(fn, timeout=2.0):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            if fn():
                return True
            time.sleep(0.02)
        return fn()

    print("JukaPad receiver self-test")
    # X25519 against RFC 7748 section 6.1.
    a = bytes.fromhex("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
    b = bytes.fromhex("5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb")
    check(x25519(a, X25519_BASE).hex() == "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a",
          "X25519 public key (RFC 7748)")
    check(x25519(a, x25519(b, X25519_BASE)).hex() ==
          "4a5d9d5ba4ce2de1728e3bf480350f25e07e21c947d19e3376f09b3c1e161742", "X25519 shared secret (RFC 7748)")
    # HKDF against RFC 5869 test case 1.
    okm = hkdf_sha256(bytes([0x0b] * 22), bytes(range(13)), bytes(range(0xf0, 0xfa)), 42)
    check(okm.hex() == "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf"
                       "34007208d5b887185865", "HKDF-SHA256 (RFC 5869 case 1)")

    tmp = tempfile.mkdtemp(prefix="jukapad-selftest-")
    store = Store(os.path.join(tmp, "paired.json"))
    pads = []
    answers = {"yes": True}
    shown = {}

    def factory():
        pads.append(FakePad())
        return pads[-1]

    rcv = None

    def confirm(p):
        shown["code"] = p.code
        threading.Timer(0.05, lambda: rcv.answer(answers["yes"])).start()

    rcv = Receiver("Selftest PC", store, factory, confirm, port=0, bind="127.0.0.1",
                   log=lambda m: None, silence=0.3, confirm_grace=0)
    th = threading.Thread(target=rcv.serve_forever, daemon=True)
    th.start()
    try:
        hh = FakeHandheld(("127.0.0.1", rcv.port))
        pr = hh.probe()
        check(pr is not None and pr[0] and not pr[1] and pr[2] == "Selftest PC", "discovery: announce, not paired")

        answers["yes"] = False
        check(hh.pair() == "refused", "pairing refused on the PC reaches the handheld")
        answers["yes"] = True
        check(hh.pair(tamper_nonce=True) == "tamper-detected", "a changed nonce stops pairing")
        res = hh.pair()
        check(res == "ok", "pairing completes (%s)" % res)
        check(shown.get("code") == hh.code and len(hh.code or "") == 6, "both sides show the same 6-digit code")
        check(store.get(hh.hid) is not None and store.get(hh.hid)["key"] == hh.ltk, "receiver stored the key")
        if os.name == "posix":
            check(os.stat(store.path).st_mode & 0o077 == 0, "paired.json is private (0600)")
        check(Store(store.path).get(hh.hid) is not None, "pairing survives a restart")
        pr = hh.probe()
        check(pr is not None and pr[1], "discovery reports paired")

        res = hh.connect(bad_proof=True)
        check(res == "no feedback", "a wrong handheld proof is refused")
        res = hh.connect()
        check(res == "ok", "reconnect without prompts (%s)" % res)
        pad = pads[-1] if pads else FakePad()

        hh.state(B_A | B_UP, 32767, -32768, -100, 100, 255, 7)
        check(wait_for(lambda: pad.states and pad.states[-1] == (B_A | B_UP, 32767, -32768, -100, 100, 255, 7)),
              "state reaches the pad")
        old = hh.state(B_B, 0, 0, 0, 0, 0, 0)
        check(wait_for(lambda: pad.states[-1][0] == B_B), "next state applied")
        hh.state(B_X, 0, 0, 0, 0, 0, 0)
        check(wait_for(lambda: pad.states[-1][0] == B_X), "third state applied")
        n = len(pad.states)
        hh.send(old)
        forged = bytearray(pack_state(hh.sk, hh.sid, hh.seq + 5, B_Y, 0, 0, 0, 0, 0, 0))
        forged[-1] ^= 1
        hh.send(bytes(forged))
        other = pack_state(secrets.token_bytes(32), hh.sid, hh.seq + 6, B_Y, 0, 0, 0, 0, 0, 0)
        hh.send(other)
        time.sleep(0.15)
        check(len(pad.states) == n and pad.states[-1][0] == B_X, "replayed and forged states are ignored")

        pad.rumble_cb(200, 90, 0)
        fb = hh.feedback(want=lambda f: f[2] == 200)
        check(fb is not None and fb[3] == 90 and fb[4] > 0, "rumble reaches the handheld")
        fb = hh.feedback(timeout=0.6, want=lambda f: f[2] == 200)
        check(fb is not None, "rumble repeats while on")
        pad.rumble_cb(0, 0, 0)
        fb = hh.feedback(want=lambda f: f[2] == 0 and f[3] == 0)
        check(fb is not None and fb[4] == 0, "rumble stop reaches the handheld")

        check(wait_for(lambda: pad.states[-1] == (0, 0, 0, 0, 0, 0, 0), 1.5),
              "silence releases every button")
        hh.state(B_START, 0, 0, 0, 0, 0, 0)
        check(wait_for(lambda: pad.states[-1][0] == B_START), "states resume after silence")
        hh.seq += 1
        hh.send(pack_bye(hh.sk, T_BYE_H, hh.sid, hh.seq))
        check(wait_for(lambda: pad.states[-1] == (0, 0, 0, 0, 0, 0, 0) and not rcv.sessions),
              "BYE releases and ends the session")
        hh.state(B_A, 0, 0, 0, 0, 0, 0)
        time.sleep(0.15)
        check(pad.states[-1][0] == 0, "states after BYE are ignored")

        stranger = FakeHandheld(("127.0.0.1", rcv.port), "Stranger")
        stranger.rid, stranger.ltk = rcv.rid, secrets.token_bytes(32)
        check(stranger.connect() == "not paired", "an unpaired handheld is told to pair")
        check(hh.connect() == "ok" and len(pads) == 1, "reconnect reuses the same virtual pad")
    finally:
        rcv.stop()
        th.join(2)
    print("self-test %s" % ("PASSED" if not failures else "FAILED (%d)" % len(failures)))
    return 0 if not failures else 1


# ── Test vectors for player/ctlmode_wifi_test.go ──────────────────────────


def vectors():
    a = bytes.fromhex("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
    b = bytes.fromhex("5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb")
    pka, pkb = x25519(a, X25519_BASE), x25519(b, X25519_BASE)
    hid, rid, pid = bytes([1]) * 16, bytes([2]) * 16, bytes([3]) * 8
    na, nb = bytes([4]) * 32, bytes([5]) * 32
    z = x25519(a, pkb)
    ltk = derive_ltk(z, na, nb, hid, rid, pka, pkb)
    na2, nb2 = bytes([6]) * 16, bytes([7]) * 16
    sk = session_key(ltk, hid, rid, na2, nb2)
    sid = session_id(sk)
    out = [
        ("commit", commit_hash(pkb, pka, nb).hex()),
        ("code", pair_code(pka, pkb, na, nb)),
        ("ltk", ltk.hex()),
        ("confirmH", confirm_tag(ltk, b"h", pid).hex()),
        ("confirmR", confirm_tag(ltk, b"r", pid).hex()),
        ("proofR", proof_tag(ltk, b"r", hid, rid, na2, nb2).hex()),
        ("proofH", proof_tag(ltk, b"h", hid, rid, na2, nb2).hex()),
        ("sessionKey", sk.hex()),
        ("sid", "%d" % sid),
        ("state", pack_state(sk, sid, 7, B_A | B_UP | B_GUIDE, 32767, -32768, -1234, 4321, 255, 3).hex()),
        ("feedback", pack_feedback(sk, sid, 9, 200, 17, 300).hex()),
        ("byeH", pack_bye(sk, T_BYE_H, sid, 8).hex()),
        ("dsu", dsu_data_packet(0x11223344, bytes([0x02, 0x4A, 0x55, 0x4B, 0x41, 0x01]), 5, 5,
                                (B_A | B_UP | B_START | B_RB | B_GUIDE | B_RT, 32767, -32768, -12345, 4000, 200, 0),
                                123456789).hex()),
    ]
    for k, v in out:
        print("%s %s" % (k, v))
    return 0


# ── Main ──────────────────────────────────────────────────────────────────


def main(argv=None):
    ap = argparse.ArgumentParser(description="JukaPad receiver: use a JukaHub handheld as a game controller.")
    ap.add_argument("--name", default=socket.gethostname()[:32], help="the name the handheld shows")
    ap.add_argument("--port", type=int, default=RECEIVER_PORT, help="UDP port (default 8320)")
    ap.add_argument("--tsp", metavar="IP", help="the handheld's IP, for networks that block broadcast")
    ap.add_argument("--pad", choices=("xbox", "ds4"), default="xbox", help="virtual pad type (ds4: Windows only)")
    ap.add_argument("--selftest", action="store_true", help="check the protocol offline and exit")
    ap.add_argument("--vectors", action="store_true", help="print test vectors and exit")
    args = ap.parse_args(argv)
    if args.vectors:
        return vectors()
    if args.selftest:
        return selftest()
    factory = choose_pad_factory(args.pad)
    store = Store(default_store_path())
    try:
        rcv = Receiver(args.name, store, factory, console_confirm, port=args.port, tsp=args.tsp, log=log_line)
    except OSError as exc:
        raise SystemExit("Cannot listen on UDP port %d (%s). Is another receiver running?" % (args.port, exc))
    print("JukaPad receiver \"%s\" is listening on %s, UDP port %d." % (rcv.name, local_ipv4(), rcv.port))
    print("On the handheld: Controller Mode -> Wi-Fi to a PC. Paired handhelds: %d." % len(store.handhelds))
    if sys.platform.startswith("win"):
        print("If Windows asks, allow Python on private networks.")
    if args.tsp:
        print("Also announcing to %s:%d (broadcast-free networks)." % (args.tsp, HANDHELD_PORT))
    print("Ctrl+C or q + Enter stops it.")
    threading.Thread(target=stdin_loop, args=(rcv,), daemon=True).start()
    try:
        rcv.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        rcv.stop()
        print("Stopped.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
