#!/usr/bin/env python3
"""JukaHub Spatial — reference implementation (this file is the specification).

The virtual-surround DSP runs in three places: in Go for JukaHub's own media
(player/spatial_dsp.go) and in C for games (player/spatialfx/
jukahub_spatial.c, one .so that is both a RetroArch audio DSP filter and a
LADSPA plugin). Every constant, every formula and the order of every operation
below is normative; the ports are checked against the golden vectors this
script writes (player/testdata/spatial/*.txt):

    python3 jh_spatial_ref.py <output-dir>

Model (parametric — no measured HRTF data is shipped or needed):

  * Headphones. The stereo input plays through four virtual loudspeakers: a
    front pair at -/+30 degrees fed with L and R, and a rear pair at -/+110
    degrees fed with the ambience (the side signal, L-R), band-limited,
    decorrelated by allpass filters and delayed 15 ms so the front image stays
    in front (precedence effect). Each loudspeaker reaches each ear through a
    spherical-head model (Brown & Duda 1998): a one-pole/one-zero head-shadow
    filter and the head's path delay (Woodworth), plus a gentle extra low-pass
    on the far ear. A small virtual room — six early reflections per ear and a
    4-line feedback delay network — moves the sound out of the head.
  * Speakers (the handheld's own pair, ~10 cm apart). Binaural cues do not
    survive loudspeakers, so this mode only widens: the side signal above
    300 Hz is raised; bass stays centred.
  * A stateless soft limiter keeps every output sample strictly inside
    (-1, 1): above the knee it follows a tanh curve, capped at LIMIT_CEIL, the
    largest float32 below 1. (0.9 + 0.1 * tanh(t) rounds to exactly 1.0 once
    t passes about 17.7 in double and 8.7 in float — inputs of 2.67 and 1.77 —
    so without the cap the bound would not hold; the float ports and this
    reference share the same cap.) A NaN reaching the limiter comes out as 0.
  * Off, or strength 0: the input is returned untouched.
  * Robustness rules (no golden vector exercises them; S16 input can never
    trigger the first): an input sample that is not a finite number (NaN,
    +/-inf) is read as 0 before it reaches any state, and a strength that is
    not a number reads as 0, i.e. off.

Determinism rules for the ports:
  * Integer delays are floor(x + 0.5) — never banker's rounding.
  * A delay line is written first, then read: tap(0) is this frame's sample.
  * Fractional reads use linear interpolation:
      y = (1 - f) * tap(i) + f * tap(i + 1),   i = floor(d), f = d - i.
  * One-pole low-pass:  y += (1 - p) * (x - y),  p = exp(-2*pi*fc/fs).
    One-pole high-pass: x - lowpass(x) with the same p.
  * The order of operations per frame is exactly the order in the process_*
    methods; the coefficients are computed exactly as in configure().
"""

import math
import sys

# ── Constants (normative) ────────────────────────────────────────────────

HEAD_RADIUS = 0.0875        # metres
SOUND_SPEED = 343.0         # metres per second
ALPHA_MIN = 0.1             # head-shadow HF gain at THETA_MIN
THETA_MIN = 150.0           # degrees
FRONT_AZ = 30.0             # front pair at -/+ this many degrees
REAR_AZ = 110.0             # rear pair at -/+ this many degrees
REAR_DELAY_MS = 15.0
REAR_HPF_HZ = 120.0
REAR_LPF_HZ = 7000.0
CONTRA_LPF_HZ = 1500.0      # extra low-pass on every far-ear path
AP_MS_LEFT = (3.1, 7.3)     # rear-left decorrelation allpasses
AP_MS_RIGHT = (4.7, 5.9)    # rear-right decorrelation allpasses
AP_GAIN = 0.5
ER_LPF_HZ = 6000.0
FDN_DAMP_HZ = 4000.0
FDN_PREDELAY_MS = 20.0
SIDE_HPF_HZ = 300.0         # speakers mode
LIMIT_KNEE = 0.9
LIMIT_CEIL = 0.99999994     # as float32 this is 0x3F7FFFFF, the largest float below 1
NORM_LO_HZ = 100.0          # loudness normalisation band ...
NORM_HI_HZ = 8000.0
NORM_POINTS = 24            # ... sampled at this many log-spaced points

MODE_OFF, MODE_HEADPHONES, MODE_SPEAKERS = 0, 1, 2
ROOM_SMALL, ROOM_MEDIUM, ROOM_LARGE = 0, 1, 2

# room: (time scale, early-reflection gain, RT60 in seconds)
ROOMS = {
    ROOM_SMALL: (0.6, 0.8, 0.25),
    ROOM_MEDIUM: (1.0, 1.0, 0.40),
    ROOM_LARGE: (1.5, 1.0, 0.70),
}

# Early reflections per ear: (delay ms, gain, weight of L, weight of R).
ER_TAPS_LEFT = [
    (7.1, 0.35, 0.8, 0.2), (11.3, -0.28, 0.3, 0.7), (17.9, 0.22, 0.7, 0.3),
    (23.5, -0.17, 0.4, 0.6), (31.1, 0.13, 0.6, 0.4), (38.9, -0.10, 0.5, 0.5),
]
ER_TAPS_RIGHT = [
    (7.9, 0.35, 0.2, 0.8), (12.7, -0.28, 0.7, 0.3), (16.1, 0.22, 0.3, 0.7),
    (25.3, -0.17, 0.6, 0.4), (29.7, 0.13, 0.4, 0.6), (41.3, -0.10, 0.5, 0.5),
]

FDN_MS = (29.7, 37.1, 41.1, 43.7)
FDN_IN = (0.5, -0.5, 0.5, -0.5)

# Strength s in [0, 1] scales the effect (configure() / set_strength()):
#   far-ear gain (front)  0.6 * s
#   rear gain             0.4 * s
#   early reflections     0.6 * s * room ER gain
#   late reverb           0.25 * s
#   speakers-mode width   1 + 0.8 * s


def iround(x):
    return int(math.floor(x + 0.5))


def finite_or_zero(x):
    """An input sample that is not a finite number (NaN, +/-inf) reads as 0."""
    return x if math.isfinite(x) else 0.0


def clamp_strength(s):
    """Strength in [0, 1]; a strength that is not a number (NaN) is 0, i.e. off."""
    if s != s:
        return 0.0
    return min(1.0, max(0.0, s))


def head_alpha(theta_ear_deg):
    """Brown-Duda head-shadow HF gain for the angle between source and ear axis."""
    return (1.0 + ALPHA_MIN / 2.0) + (1.0 - ALPHA_MIN / 2.0) * math.cos(theta_ear_deg / THETA_MIN * math.pi)


def head_delay_s(theta_ear_deg):
    """Path delay to the ear in seconds, offset so it is never negative."""
    ac = HEAD_RADIUS / SOUND_SPEED
    if theta_ear_deg < 90.0:
        return ac * (1.0 - math.cos(theta_ear_deg * math.pi / 180.0))
    return ac * (1.0 + (theta_ear_deg - 90.0) * math.pi / 180.0)


def ear_angle(az_deg, ear_az_deg):
    """Angle between a source at az and the ear axis at ear_az, in [0, 180]."""
    d = math.fmod(abs(az_deg - ear_az_deg), 360.0)
    if d > 180.0:
        d = 360.0 - d
    return d


def shadow_coeffs(alpha, fs):
    """Bilinear one-pole/one-zero: H(s) = (2*w0 + alpha*s) / (2*w0 + s), w0 = c/a.

    y[n] = b0*x[n] + b1*x[n-1] - a1*y[n-1]
    """
    w0 = SOUND_SPEED / HEAD_RADIUS
    k = 2.0 * fs
    d0 = 2.0 * w0 + k
    b0 = (2.0 * w0 + alpha * k) / d0
    b1 = (2.0 * w0 - alpha * k) / d0
    a1 = (2.0 * w0 - k) / d0
    return b0, b1, a1


def pole(fc, fs):
    return math.exp(-2.0 * math.pi * fc / fs)


class Line:
    """Circular delay line: write, then read; tap(0) is the sample just written."""

    def __init__(self, n):
        self.buf = [0.0] * n
        self.n = n
        self.w = 0

    def write(self, x):
        self.w += 1
        if self.w == self.n:
            self.w = 0
        self.buf[self.w] = x

    def tap(self, i):
        j = self.w - i
        if j < 0:
            j += self.n
        return self.buf[j]

    def frac(self, d):
        i = int(math.floor(d))
        f = d - i
        return (1.0 - f) * self.tap(i) + f * self.tap(i + 1)


class Allpass:
    """Schroeder allpass in direct form, D >= 1 samples:

        y[n] = -g*x[n] + x[n-D] + g*y[n-D]

    Two lines of length D+1. x is written first, so x.tap(D) is x[n-D]; y is
    written last, so before that write y.tap(D-1) is y[n-D].
    """

    def __init__(self, d):
        self.d = max(1, d)
        self.x = Line(self.d + 1)
        self.y = Line(self.d + 1)

    def process(self, v):
        self.x.write(v)
        out = -AP_GAIN * v + self.x.tap(self.d) + AP_GAIN * self.y.tap(self.d - 1)
        self.y.write(out)
        return out


class Spatial:
    def __init__(self, fs, mode=MODE_HEADPHONES, strength=0.75, room=ROOM_MEDIUM):
        self.fs = float(fs)
        self.mode = mode
        self.room = room
        self.strength = 0.0
        self.configure()
        self.set_strength(strength)

    # ── set-up ──
    def configure(self):
        fs = self.fs
        scale, er_gain, rt60 = ROOMS[self.room]
        self.er_room_gain = er_gain
        # Virtual loudspeakers in the order FL, FR, RL, RR: (azimuth, extra delay s).
        spk = ((-FRONT_AZ, 0.0), (FRONT_AZ, 0.0),
               (-REAR_AZ, REAR_DELAY_MS / 1000.0), (REAR_AZ, REAR_DELAY_MS / 1000.0))
        raw = []
        for az, extra in spk:
            for ear_az in (-90.0, 90.0):  # left ear, then right ear
                th = ear_angle(az, ear_az)
                raw.append((th, head_delay_s(th) + extra))
        tmin = min(t for _, t in raw)
        # paths[k][e] = (delay in samples, b0, b1, a1); e = 0 left ear, 1 right ear.
        self.paths = []
        for k in range(4):
            row = []
            for e in range(2):
                th, t = raw[k * 2 + e]
                b0, b1, a1 = shadow_coeffs(head_alpha(th), fs)
                row.append(((t - tmin) * fs, b0, b1, a1))
            self.paths.append(row)
        maxd = max(p[0] for row in self.paths for p in row)
        self.spk_lines = [Line(int(math.floor(maxd)) + 3) for _ in range(4)]
        self.sh_x1 = [[0.0, 0.0] for _ in range(4)]
        self.sh_y1 = [[0.0, 0.0] for _ in range(4)]
        self.p_contra = pole(CONTRA_LPF_HZ, fs)
        self.contra_lp = [[0.0, 0.0] for _ in range(4)]
        # Rear feed: band-limit, then decorrelate each side.
        self.p_rear_hp = pole(REAR_HPF_HZ, fs)
        self.p_rear_lp = pole(REAR_LPF_HZ, fs)
        self.rear_hp_state = 0.0
        self.rear_lp_state = 0.0
        self.ap_left = [Allpass(iround(ms * fs / 1000.0)) for ms in AP_MS_LEFT]
        self.ap_right = [Allpass(iround(ms * fs / 1000.0)) for ms in AP_MS_RIGHT]
        # Early reflections: integer taps scaled with the room.
        self.er_left = [(iround(ms * scale * fs / 1000.0), g, wl, wr) for ms, g, wl, wr in ER_TAPS_LEFT]
        self.er_right = [(iround(ms * scale * fs / 1000.0), g, wl, wr) for ms, g, wl, wr in ER_TAPS_RIGHT]
        self.predelay = iround(FDN_PREDELAY_MS * scale * fs / 1000.0)
        er_max = max([t[0] for t in self.er_left + self.er_right] + [self.predelay])
        self.er_lines = [Line(er_max + 2), Line(er_max + 2)]
        self.p_er = pole(ER_LPF_HZ, fs)
        self.er_lp = [0.0, 0.0]
        # Late reverb: 4 lines, Householder feedback, damped.
        self.fdn_len = [iround(ms * scale * fs / 1000.0) for ms in FDN_MS]
        self.fdn_gain = [10.0 ** (-3.0 * n / (rt60 * fs)) for n in self.fdn_len]
        self.fdn_lines = [Line(n) for n in self.fdn_len]
        self.p_damp = pole(FDN_DAMP_HZ, fs)
        self.fdn_z = [0.0] * 4
        # Speakers mode.
        self.p_side = pole(SIDE_HPF_HZ, fs)
        self.side_lp = 0.0

    def set_strength(self, s):
        s = clamp_strength(s)
        self.strength = s
        self.g_contra = 0.6 * s
        self.g_rear = 0.4 * s
        self.g_er = 0.6 * s * self.er_room_gain
        self.g_rev = 0.25 * s
        self.width = 1.0 + 0.8 * s
        self.makeup = self.loudness_makeup()

    def path_response(self, k, e, w):
        """Complex response of the direct path k -> ear e at w rad/sample (incl. far-ear low-pass)."""
        d, b0, b1, a1 = self.paths[k][e]
        z1 = complex(math.cos(w), -math.sin(w))
        h = (b0 + b1 * z1) / (1.0 + a1 * z1)
        h *= complex(math.cos(w * d), -math.sin(w * d))
        if (k % 2) != e:  # far ear: FL->R, FR->L, RL->R, RR->L
            h *= (1.0 - self.p_contra) / (1.0 - self.p_contra * z1)
        return h

    def loudness_makeup(self):
        """1 / sqrt(mean power gain) of a centred signal: direct paths + room energy."""
        acc = 0.0
        for i in range(NORM_POINTS):
            f = NORM_LO_HZ * (NORM_HI_HZ / NORM_LO_HZ) ** (i / (NORM_POINTS - 1.0))
            w = 2.0 * math.pi * f / self.fs
            # Left ear: FL near + FR far (a centred signal has no side, so no rears).
            h = self.path_response(0, 0, w) + self.g_contra * self.path_response(1, 0, w)
            acc += abs(h) ** 2
        direct = acc / NORM_POINTS
        er_energy = sum(g * g for _, g, _, _ in ER_TAPS_LEFT)
        total = direct + self.g_er * self.g_er * er_energy + self.g_rev * self.g_rev * 0.5
        return 1.0 / math.sqrt(total)

    def reset(self):
        self.configure()
        self.set_strength(self.strength)

    # ── processing ──
    def process(self, xl, xr):
        if self.mode == MODE_OFF or self.strength <= 0.0:
            return xl, xr
        xl = finite_or_zero(xl)
        xr = finite_or_zero(xr)
        if self.mode == MODE_SPEAKERS:
            return self.process_speakers(xl, xr)
        return self.process_headphones(xl, xr)

    def process_speakers(self, xl, xr):
        mid = 0.5 * (xl + xr)
        side = 0.5 * (xl - xr)
        self.side_lp += (1.0 - self.p_side) * (side - self.side_lp)
        side2 = side + (self.width - 1.0) * (side - self.side_lp)
        return limit(mid + side2), limit(mid - side2)

    def process_headphones(self, xl, xr):
        # 1. Rear feed: side signal, high-passed, low-passed, decorrelated per side.
        side = 0.5 * (xl - xr)
        self.rear_hp_state += (1.0 - self.p_rear_hp) * (side - self.rear_hp_state)
        hp = side - self.rear_hp_state
        self.rear_lp_state += (1.0 - self.p_rear_lp) * (hp - self.rear_lp_state)
        rl = self.rear_lp_state
        rr = -self.rear_lp_state
        for ap in self.ap_left:
            rl = ap.process(rl)
        for ap in self.ap_right:
            rr = ap.process(rr)
        feeds = (xl, xr, rl, rr)
        # 2. Virtual loudspeakers to the two ears.
        ear = [0.0, 0.0]
        for k in range(4):
            line = self.spk_lines[k]
            line.write(feeds[k])
            for e in range(2):
                d, b0, b1, a1 = self.paths[k][e]
                v = line.frac(d)
                y = b0 * v + b1 * self.sh_x1[k][e] - a1 * self.sh_y1[k][e]
                self.sh_x1[k][e] = v
                self.sh_y1[k][e] = y
                near = (k % 2) == e
                if not near:
                    self.contra_lp[k][e] += (1.0 - self.p_contra) * (y - self.contra_lp[k][e])
                    y = self.contra_lp[k][e]
                if k < 2:
                    g = 1.0 if near else self.g_contra
                else:
                    g = self.g_rear
                ear[e] += g * y
        # 3. Early reflections from the low-passed inputs.
        self.er_lp[0] += (1.0 - self.p_er) * (xl - self.er_lp[0])
        self.er_lp[1] += (1.0 - self.p_er) * (xr - self.er_lp[1])
        self.er_lines[0].write(self.er_lp[0])
        self.er_lines[1].write(self.er_lp[1])
        er_l = 0.0
        for d, g, wl, wr in self.er_left:
            er_l += g * (wl * self.er_lines[0].tap(d) + wr * self.er_lines[1].tap(d))
        er_r = 0.0
        for d, g, wl, wr in self.er_right:
            er_r += g * (wl * self.er_lines[0].tap(d) + wr * self.er_lines[1].tap(d))
        # 4. Late reverb: read every line's oldest sample, damp, mix, write back.
        rev_in = 0.5 * (self.er_lines[0].tap(self.predelay) + self.er_lines[1].tap(self.predelay))
        for i in range(4):
            y = self.fdn_lines[i].tap(self.fdn_len[i] - 1)
            self.fdn_z[i] += (1.0 - self.p_damp) * (y - self.fdn_z[i])
        zsum = self.fdn_z[0] + self.fdn_z[1] + self.fdn_z[2] + self.fdn_z[3]
        for i in range(4):
            fb = self.fdn_z[i] - 0.5 * zsum
            self.fdn_lines[i].write(FDN_IN[i] * rev_in + self.fdn_gain[i] * fb)
        rev_l = 0.5 * (self.fdn_z[0] + self.fdn_z[2])
        rev_r = 0.5 * (self.fdn_z[1] + self.fdn_z[3])
        # 5. Mix, normalise, limit.
        out_l = self.makeup * (ear[0] + self.g_er * er_l + self.g_rev * rev_l)
        out_r = self.makeup * (ear[1] + self.g_er * er_r + self.g_rev * rev_r)
        return limit(out_l), limit(out_r)


def limit(x):
    """Soft limiter: identity up to the knee, then a tanh curve capped at LIMIT_CEIL."""
    if x != x:
        return 0.0
    a = abs(x)
    if a <= LIMIT_KNEE:
        return x
    y = LIMIT_KNEE + (1.0 - LIMIT_KNEE) * math.tanh((a - LIMIT_KNEE) / (1.0 - LIMIT_KNEE))
    y = min(y, LIMIT_CEIL)
    return y if x > 0 else -y


# ── golden vectors ──────────────────────────────────────────────────────

def lcg_noise(n, fs):
    """Deterministic stereo test signal: two LCG noise channels (+/-0.5) plus a centred 440 Hz tone."""
    out = []
    state = 12345
    for i in range(n):
        state = (1103515245 * state + 12345) & 0x7FFFFFFF
        a = state / 2147483647.0 - 0.5
        state = (1103515245 * state + 12345) & 0x7FFFFFFF
        b = state / 2147483647.0 - 0.5
        tone = 0.6 * math.sin(2.0 * math.pi * 440.0 * i / fs)
        out.append((a + tone, b + tone))
    return out


CASES = [
    # name, fs, mode, strength, room, input, frames
    ("hp48_impl_med", 48000, MODE_HEADPHONES, 0.75, ROOM_MEDIUM, "impL", 4800),
    ("hp48_impr_full_large", 48000, MODE_HEADPHONES, 1.0, ROOM_LARGE, "impR", 4800),
    ("hp441_impl_half_small", 44100, MODE_HEADPHONES, 0.5, ROOM_SMALL, "impL", 4410),
    ("hp22_impl_med", 22050, MODE_HEADPHONES, 0.75, ROOM_MEDIUM, "impL", 2205),
    ("hp48_noise_full", 48000, MODE_HEADPHONES, 1.0, ROOM_MEDIUM, "noise", 4800),
    ("sp48_noise", 48000, MODE_SPEAKERS, 0.75, ROOM_MEDIUM, "noise", 2400),
    ("off48_noise", 48000, MODE_OFF, 0.75, ROOM_MEDIUM, "noise", 256),
]


def make_input(kind, n, fs):
    if kind == "impL":
        return [(1.0 if i == 0 else 0.0, 0.0) for i in range(n)]
    if kind == "impR":
        return [(0.0, 1.0 if i == 0 else 0.0) for i in range(n)]
    if kind == "noise":
        return lcg_noise(n, fs)
    raise ValueError(kind)


def write_golden(outdir):
    import os
    os.makedirs(outdir, exist_ok=True)
    for name, fs, mode, s, room, kind, n in CASES:
        sp = Spatial(fs, mode, s, room)
        lines = ["# jukahub spatial golden vector v1 (player/spatialfx/jh_spatial_ref.py)",
                 "# fs=%d mode=%d strength=%.6g room=%d input=%s frames=%d" % (fs, mode, s, room, kind, n)]
        for xl, xr in make_input(kind, n, fs):
            yl, yr = sp.process(xl, xr)
            lines.append("%.7g %.7g" % (yl, yr))
        with open(os.path.join(outdir, name + ".txt"), "w", newline="\n") as fh:
            fh.write("\n".join(lines) + "\n")
        print("wrote", name, n)


if __name__ == "__main__":
    if len(sys.argv) > 1:
        write_golden(sys.argv[1])
    else:
        print(__doc__)
