#!/usr/bin/env python3
"""Preview of the cracks the Cinematic boot animation leaves in the ground.

player/bootcracks.go draws the crack layer on the handheld; this draws the same
layer from the same seed, step for step (the same xorshift draws in the same
order, the same raster), over the same ground, with the logo at rest, so a
change to the cracks can be judged here before it is built. Change the two
together. Needs Python 3 with NumPy and Pillow; nothing else.

    python3 -I branding/mkbootcracks.py OUTDIR [W H] [SEED] [final]

Writes OUTDIR/cracks-final.png (the scene once the cracks have run their
length), OUTDIR/cracks-layer.png (the layer alone, RGBA) and, unless "final"
is given, OUTDIR/cracks-sheet.png (the cracks 0, 25, 60, 120, 220 and 420 ms
after the impact). W H default to 1280 720 (Smart Pro); 1024 768 is the Brick.
"""
import math
import os
import sys

import numpy as np
from PIL import Image, ImageFilter

MASK = 0xFFFFFFFF
REPO = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
ACCENT = (85, 216, 255)  # glassAccent() with no theme accent
GROW_MS = 420.0  # bootCrackGrowMS
MAX_NODES = 2400  # bootCrackMaxNodes
LIT = (math.sqrt(0.5), math.sqrt(0.5))  # towards a crack's lit wall


class Rng:
    """bootStyleRand: a xorshift32 step mapped to [0, 1)."""

    def __init__(self, seed):
        self.s = (seed & MASK) or 1

    def __call__(self):
        s = self.s
        s ^= (s << 13) & MASK
        s ^= s >> 17
        s ^= (s << 5) & MASK
        self.s = s
        return s / 4294967296.0


def clamp01(v):
    return 0.0 if v < 0 else 1.0 if v > 1 else v


def smoothstep(e0, e1, x):
    if e1 == e0:
        return 0.0 if x < e0 else 1.0
    t = clamp01((x - e0) / (e1 - e0))
    return t * t * (3 - 2 * t)


def angdiff(a, b):
    """bootAngleDiff: a - b wrapped to (-pi, pi]."""
    d = math.fmod(a - b, 2 * math.pi)
    if d > math.pi:
        d -= 2 * math.pi
    elif d <= -math.pi:
        d += 2 * math.pi
    return d


def foot_sd(x, y, a, rc):
    """bootFootSD."""
    qx = abs(x) - (a - rc)
    qy = abs(y) - (a - rc)
    return math.hypot(max(qx, 0.0), max(qy, 0.0)) + min(max(qx, qy), 0.0) - rc


def foot_r(theta, a, rc):
    """bootFootR."""
    c, s = math.cos(theta), math.sin(theta)
    lo, hi = 0.0, 1.5 * a
    for _ in range(22):
        mid = (lo + hi) / 2
        if foot_sd(c * mid, s * mid, a, rc) < 0:
            lo = mid
        else:
            hi = mid
    return lo


# ── The network (bootCrackGrow) ───────────────────────────────────────────


class Plan:
    def __init__(self, w, h, cx, cy, tile, seed):
        self.w, self.h, self.cx, self.cy, self.tile = w, h, cx, cy, tile
        self.a = tile / 2
        self.rc = tile * 0.25
        self.max_len = 0.46 * max(w, h)
        self.step = self.max_len * 0.032
        self.hw0 = tile * 0.014
        self.chip_r = tile * 1.6
        self.ring_off = 0.0
        self.lines = []  # polylines of (x, y, hp, hm)
        self.nodes = 0
        self.pebbles = []  # (x, y, r)
        self.rng = Rng(seed)


def walk(p, x, y, heading, length, hw0, depth, step):
    rng = p.rng
    nodes = [(x, y, hw0, hw0)]
    travelled = 0.0
    pp, pm = hw0, hw0
    while travelled < length and p.nodes < MAX_NODES:
        l = step * (0.6 + 0.8 * rng())
        turn = (rng() - 0.5) * 0.30
        if rng() < 0.16:
            k = 0.22 + 0.30 * rng()
            if rng() < 0.5:
                k = -k
            turn += k
        out = math.atan2(y - p.cy, x - p.cx)
        heading += turn + angdiff(out, heading) * 0.10
        c, s = math.cos(heading), math.sin(heading)
        nx, ny = x + c * l, y + s * l
        j = (rng() - 0.5) * 0.28 * l
        mx, my = (x + nx) / 2 - s * j, (y + ny) / 2 + c * j
        travelled += l
        f = min(1.0, travelled / length)
        hw = hw0 * (1 - f) ** 0.75
        hp = hw * (0.7 + 0.6 * rng())
        hm = hw * (0.7 + 0.6 * rng())
        if depth == 0 and math.hypot(nx - p.cx, ny - p.cy) < p.chip_r and rng() < 0.22:
            if rng() < 0.5:
                hp *= 1.8 + 1.2 * rng()
            else:
                hm *= 1.8 + 1.2 * rng()
        nodes.append((mx, my, (pp + hp) / 2, (pm + hm) / 2))
        nodes.append((nx, ny, hp, hm))
        p.nodes += 2
        pp, pm = hp, hm
        x, y = nx, ny
        if x < -l or y < -l or x > p.w + l or y > p.h + l:
            break
        chance = 0.12 if depth == 0 else 0.07
        if depth < 2 and length - travelled > step and rng() < chance:
            fork = 0.32 + 0.42 * rng()
            if rng() < 0.5:
                fork = -fork
            left = (length - travelled) * (0.3 + 0.35 * rng())
            walk(p, x, y, heading + fork, left, max(hw * 0.75, 0.35), depth + 1, step)
    p.lines.append(nodes)
    return nodes


def ring_cross(p, nodes, off):
    for i in range(1, len(nodes)):
        x1, y1 = nodes[i][0], nodes[i][1]
        d1 = foot_sd(x1 - p.cx, y1 - p.cy, p.a, p.rc)
        if d1 < off:
            continue
        x0, y0 = nodes[i - 1][0], nodes[i - 1][1]
        d0 = foot_sd(x0 - p.cx, y0 - p.cy, p.a, p.rc)
        u = 1.0 if d1 == d0 else clamp01((off - d0) / (d1 - d0))
        return (x0 + (x1 - x0) * u, y0 + (y1 - y0) * u)
    return None


def ring_arc(p, p0, p1, off, hw, chord):
    rng = p.rng
    t0 = math.atan2(p0[1] - p.cy, p0[0] - p.cx)
    t1 = math.atan2(p1[1] - p.cy, p1[0] - p.cx)
    span = math.fmod(t1 - t0 + 4 * math.pi, 2 * math.pi)
    bulge = (rng() - 0.5) * 0.35 * off
    stop = 1.0
    if rng() < 0.3:
        stop = 0.35 + 0.4 * rng()
    if span <= 0.02 or span > math.pi:
        return
    r0 = math.hypot(p0[0] - p.cx, p0[1] - p.cy)
    r1 = math.hypot(p1[0] - p.cx, p1[1] - p.cy)
    e0 = r0 - (foot_r(t0, p.a, p.rc) + off)
    e1 = r1 - (foot_r(t1, p.a, p.rc) + off)
    mean = (r0 + r1) / 2
    m = max(2, int(math.ceil(span * mean / (p.step * 0.6))))
    nodes = []
    for i in range(m + 1):
        u = i / m
        if u > stop:
            break
        if chord:
            x = p0[0] + (p1[0] - p0[0]) * u
            y = p0[1] + (p1[1] - p0[1]) * u
            th = math.atan2(y - p.cy, x - p.cx)
            r = math.hypot(x - p.cx, y - p.cy) + bulge * math.sin(math.pi * u)
        else:
            th = t0 + span * u
            r = foot_r(th, p.a, p.rc) + off + e0 * (1 - u) + e1 * u + bulge * math.sin(math.pi * u)
        if 0 < i < m:
            r += (rng() - 0.5) * 0.22 * p.step
        k = hw * (0.7 + 0.6 * rng())
        if stop < 1:
            k *= 1 - u / stop
        nodes.append((p.cx + math.cos(th) * r, p.cy + math.sin(th) * r, k, k))
    if len(nodes) >= 2 and p.nodes + len(nodes) <= MAX_NODES:
        p.nodes += len(nodes)
        p.lines.append(nodes)


def grow(w, h, cx, cy, tile, seed):
    p = Plan(w, h, cx, cy, tile, seed)
    rng = p.rng
    starts = []
    for k in range(4):
        jit = (rng() - 0.5) * 0.30
        if rng() < 0.85:
            starts.append((math.pi / 4 + k * math.pi / 2 + jit, True))
    n = 7 + int(rng() * 3)
    for _ in range(40):
        if len(starts) >= n:
            break
        t = rng() * 2 * math.pi
        if all(abs(angdiff(t, m[0])) > 0.40 for m in starts):
            starts.append((t, False))
    starts.sort(key=lambda m: math.fmod(m[0] + 4 * math.pi, 2 * math.pi))
    mains = []
    for angle, corner in starts:
        r0 = foot_r(angle, p.a, p.rc) * 0.94
        u = rng()
        length = p.max_len * ((0.6 + 0.4 * u) if corner else (0.35 + 0.45 * u))
        heading = angle + (rng() - 0.5) * 0.25
        hw = p.hw0 * (0.8 + 0.4 * rng())
        mains.append(walk(p, p.cx + math.cos(angle) * r0, p.cy + math.sin(angle) * r0, heading, length, hw, 0, p.step))
    near_off = tile * (0.16 + 0.08 * rng())
    far_off = tile * (0.45 + 0.15 * rng())
    p.ring_off = far_off
    for off, chance, hw, chord in ((near_off, 0.8, p.hw0 * 0.45, False), (far_off, 0.55, p.hw0 * 0.35, True)):
        cross = [ring_cross(p, nodes, off) for nodes in mains]
        for i in range(len(mains)):
            if rng() >= chance:
                continue
            a, b = cross[i], cross[(i + 1) % len(mains)]
            if a is None or b is None or len(mains) < 3:
                continue
            ring_arc(p, a, b, off, hw, chord)
    for _ in range(16 + int(rng() * 10)):
        t = rng() * 2 * math.pi
        r0 = foot_r(t, p.a, p.rc) * 0.97
        heading = t + (rng() - 0.5) * 1.2
        length = tile * (0.04 + 0.10 * rng())
        hw = p.hw0 * (0.3 + 0.25 * rng())
        walk(p, p.cx + math.cos(t) * r0, p.cy + math.sin(t) * r0, heading, length, hw, 2, p.step * 0.4)
    for _ in range(12):
        t = rng() * 2 * math.pi
        d = rng()
        r = tile * (0.005 + 0.009 * rng())
        far = foot_r(t, p.a, p.rc) + r + 2 + tile * 1.3 * d * d
        p.pebbles.append((p.cx + math.cos(t) * far, p.cy + math.sin(t) * far, r))
    return p


# ── The raster (bootCrackPlan.raster) ─────────────────────────────────────


def q(v):
    """A level as the handheld stores it: a byte."""
    return np.round(255 * np.clip(v, 0, 1)) / 255


def segment(D, L, a, b):
    H, W = D.shape
    x0, y0, p0, m0 = a
    x1, y1, p1, m1 = b
    dx, dy = x1 - x0, y1 - y0
    ln = math.hypot(dx, dy)
    if ln < 1e-3:
        return
    ux, uy = dx / ln, dy / ln
    nx, ny = -uy, ux
    lit = nx * LIT[0] + ny * LIT[1]
    pad = max(p0, m0, p1, m1) + 2.5
    bx0 = max(0, int(math.floor(min(x0, x1) - pad)))
    by0 = max(0, int(math.floor(min(y0, y1) - pad)))
    bx1 = min(W - 1, int(math.ceil(max(x0, x1) + pad)))
    by1 = min(H - 1, int(math.ceil(max(y0, y1) + pad)))
    if bx1 < bx0 or by1 < by0:
        return
    ys, xs = np.mgrid[by0:by1 + 1, bx0:bx1 + 1]
    px, py = xs + 0.5, ys + 0.5
    t = np.clip(((px - x0) * ux + (py - y0) * uy) / ln, 0, 1)
    ex, ey = px - (x0 + dx * t), py - (y0 + dy * t)
    dist = np.sqrt(ex * ex + ey * ey)
    s = ex * nx + ey * ny
    hw = np.where(s >= 0, p0 + (p1 - p0) * t, m0 + (m1 - m0) * t)
    near = dist <= hw + 2.5
    lit_side = (s * lit) > 0
    cov = np.clip(hw + 0.5 - dist, 0, 1) * np.minimum(1.0, 2 * hw)
    deep = 0.72 + 0.28 * np.clip(1 - dist / np.maximum(hw, 0.5), 0, 1)
    d = 0.9 * cov * deep
    rim = 0.28 * abs(lit) * np.clip(1 - (dist - hw) / 1.5, 0, 1) * np.minimum(1.0, hw)
    d = np.where(lit_side, d * 0.82, np.where(dist > hw, np.maximum(d, rim), d))
    l = np.where(lit_side, 0.30 * abs(lit) * np.clip(1 - np.abs(dist - (hw + 0.35)) / 0.85, 0, 1) * np.minimum(1.0, hw / 0.8), 0.0)
    d, l = np.where(near, q(d), 0), np.where(near, q(l), 0)
    sub = D[by0:by1 + 1, bx0:bx1 + 1]
    np.maximum(sub, d, out=sub)
    sub = L[by0:by1 + 1, bx0:bx1 + 1]
    np.maximum(sub, l, out=sub)


def crushed(p, D):
    H, W = D.shape
    zone = p.tile * 0.10
    r = int(math.ceil(p.a * 1.5 + zone + 2))
    for y in range(max(0, int(p.cy) - r), min(H, int(p.cy) + r + 1)):
        for x in range(max(0, int(p.cx) - r), min(W, int(p.cx) + r + 1)):
            sd = foot_sd(x + 0.5 - p.cx, y + 0.5 - p.cy, p.a, p.rc)
            if -1 < sd < zone:
                D[y, x] = max(D[y, x], q(0.34 * (1 - smoothstep(0, zone, sd))))


def pieces(p, D, L):
    H, W = D.shape
    reach = p.ring_off + p.tile * 0.12
    r = int(math.ceil(p.a * 1.5 + reach + 2))
    x0, x1 = max(0, int(p.cx) - r), min(W - 1, int(p.cx) + r)
    y0, y1 = max(0, int(p.cy) - r), min(H - 1, int(p.cy) + r)
    bw, bh = x1 - x0 + 1, y1 - y0 + 1
    if bw <= 0 or bh <= 0:
        return
    zone = np.zeros((bh, bw), np.int8)
    for y in range(bh):
        for x in range(bw):
            sd = foot_sd(x0 + x + 0.5 - p.cx, y0 + y + 0.5 - p.cy, p.a, p.rc)
            if sd <= 0:
                zone[y, x] = -1
            elif sd < reach:
                zone[y, x] = 1
    crack = D[y0:y1 + 1, x0:x1 + 1] > 89 / 255 + 1e-9
    label = np.zeros((bh, bw), bool)
    for sy in range(bh):
        for sx in range(bw):
            if zone[sy, sx] != 1 or crack[sy, sx] or label[sy, sx]:
                continue
            label[sy, sx] = True
            stack = [(sx, sy)]
            patch = []
            open_ = False
            while stack:
                x, y = stack.pop()
                patch.append((x, y))
                for ax, ay in ((x - 1, y), (x + 1, y), (x, y - 1), (x, y + 1)):
                    if ax < 0 or ay < 0 or ax >= bw or ay >= bh:
                        open_ = True
                        continue
                    if zone[ay, ax] != 1:
                        if zone[ay, ax] == 0 and not crack[ay, ax]:
                            open_ = True
                        continue
                    if label[ay, ax] or crack[ay, ax]:
                        continue
                    label[ay, ax] = True
                    stack.append((ax, ay))
            shade = 0.05 + 0.08 * p.rng()
            if p.rng() < 0.55:
                shade = -shade
            if open_ or len(patch) < 12:
                continue
            for (x, y) in patch:
                if shade < 0:
                    D[y0 + y, x0 + x] = max(D[y0 + y, x0 + x], q(-shade))
                else:
                    L[y0 + y, x0 + x] = max(L[y0 + y, x0 + x], q(shade))


def grain(D, L, x, y, r):
    H, W = D.shape
    for py in range(int(y - r - 3), int(y + r + 4)):
        for px in range(int(x - r - 3), int(x + r + 4)):
            if px < 0 or py < 0 or px >= W or py >= H:
                continue
            ds = math.hypot(px + 0.5 - (x + 0.8 * r + 0.6), py + 0.5 - (y + 0.8 * r + 0.6))
            sh = clamp01(r + 0.5 - ds) * 0.5
            cov = clamp01(r + 0.5 - math.hypot(px + 0.5 - x, py + 0.5 - y))
            D[py, px] = q(max(D[py, px] * (1 - cov), sh * (1 - cov)))
            L[py, px] = max(L[py, px], q(cov * 0.2))


def raster(p):
    """The layer, h*w RGBA (straight alpha), and the cracks' reach and start."""
    W, H = int(p.w), int(p.h)
    D = np.zeros((H, W), np.float64)
    L = np.zeros((H, W), np.float64)
    crushed(p, D)
    for nodes in p.lines:
        for i in range(1, len(nodes)):
            segment(D, L, nodes[i - 1], nodes[i])
    pieces(p, D, L)
    for (x, y, r) in p.pebbles:
        grain(D, L, x, y, r)
    A = 1 - (1 - D) * (1 - L)
    C = np.where(A > 0, 255 * L / np.maximum(A, 1e-9), 0)
    out = np.zeros((H, W, 4), np.uint8)
    out[..., 0] = out[..., 1] = out[..., 2] = np.round(C).astype(np.uint8)
    out[..., 3] = np.round(255 * A).astype(np.uint8)
    reach = 0.0
    for nodes in p.lines:
        for (x, y, hp, hm) in nodes:
            reach = max(reach, math.hypot(x - p.cx, y - p.cy) + max(hp, hm) + 2)
    return out, reach, 0.9 * foot_r(0, p.a, p.rc)


# ── The rest of the scene, for context ────────────────────────────────────


class Noise:
    """bootNoise."""

    def __init__(self, cols, aspect, rng):
        self.cols = cols
        self.rows = int(max(1, round(cols / aspect)))
        self.v = [rng() for _ in range((self.cols + 1) * (self.rows + 1))]

    def at(self, u, v):
        gx, gy = clamp01(u) * self.cols, clamp01(v) * self.rows
        x, y = min(int(gx), self.cols - 1), min(int(gy), self.rows - 1)
        tx, ty = smoothstep(0, 1, gx - x), smoothstep(0, 1, gy - y)
        i = y * (self.cols + 1) + x
        j = i + self.cols + 1
        top = self.v[i] + (self.v[i + 1] - self.v[i]) * tx
        bot = self.v[j] + (self.v[j + 1] - self.v[j]) * tx
        return top + (bot - top) * ty


def ground(w, h, cx, cy, seed):
    """bootGroundPixels, scaled up over the screen as the boot draws it."""
    pad = 0.04 * w
    gw, gh = w + 2 * pad, h + 2 * pad
    tw = 128
    th = int(max(1, round(tw * gh / gw)))
    fx, fy, aspect = (cx + pad) / gw, (cy + pad) / gh, gw / gh
    rng = Rng(seed | 1)
    coarse = Noise(9, aspect, rng)
    fine = Noise(23, aspect, rng)
    img = np.zeros((th, tw), np.float64)
    for y in range(th):
        v = (y + 0.5) / th
        for x in range(tw):
            u = (x + 0.5) / tw
            du, dv = (u - fx) * aspect / 0.55, (v - fy) / 0.55
            pool = 0.16 + 0.84 * math.exp(-(du * du + dv * dv))
            stone = 0.70 + 0.30 * (0.65 * coarse.at(u, v) + 0.35 * fine.at(u, v))
            img[y, x] = pool * stone
    im = Image.fromarray(np.uint8(np.round(img * 255)), "L").resize((int(round(gw)), int(round(gh))), Image.BILINEAR)
    return np.asarray(im, np.float64)[int(pad):int(pad) + h, int(pad):int(pad) + w] / 255


def layout(w, h):
    """bootLogoPlace's artwork square and the tile inside it."""
    sprite = round(h * 0.25)
    gap = round(h * 0.03)
    line_h = round(round(h * 0.06) * 1.2109375)
    top = round((h - (sprite + gap + line_h)) / 2 - h * 0.02)
    return sprite, sprite * (1 - 2 * 0.055), w / 2, top + sprite / 2


def scene(w, h, seed, layer, front=None, reach=0.0):
    """The ground, the crack layer (revealed to front when given) and the logo
    at rest with its shadow, on a dark theme."""
    sprite, tile, cx, cy = layout(w, h)
    out = np.zeros((h, w, 3), np.float64)
    g = ground(w, h, cx, cy, seed ^ 0x9E3779B9)
    tint = [52 + (ACCENT[0] - 52) * 0.30, 58 + (ACCENT[1] - 58) * 0.30, 76 + (ACCENT[2] - 76) * 0.30]
    for c in range(3):
        out[..., c] = g * round(tint[c]) * 235 / 255
    a = layer[..., 3].astype(np.float64) / 255
    if front is not None:
        yy, xx = np.mgrid[0:h, 0:w]
        soft = max(10.0, 0.05 * reach)
        a = a * np.clip((front - np.hypot(xx + 0.5 - cx, yy + 0.5 - cy)) / soft, 0, 1)
    for c in range(3):
        out[..., c] = out[..., c] * (1 - a) + layer[..., c] * a
    logo = Image.open(os.path.join(REPO, "player", "JukaPlayer.png")).convert("RGBA").resize((sprite, sprite), Image.LANCZOS)
    la = np.asarray(logo, np.float64)
    x0, y0 = int(round(cx - sprite / 2)), int(round(cy - sprite / 2))
    sh = np.asarray(logo.split()[3].filter(ImageFilter.GaussianBlur(sprite * 0.05)), np.float64) / 255 * 0.8
    off = int(round(0.05 * tile))
    out[y0 + off:y0 + off + sprite, x0 + off:x0 + off + sprite] *= (1 - sh)[..., None]
    a = la[..., 3:4] / 255
    out[y0:y0 + sprite, x0:x0 + sprite] = out[y0:y0 + sprite, x0:x0 + sprite] * (1 - a) + la[..., :3] * a
    return Image.fromarray(np.uint8(np.clip(np.round(out), 0, 255)), "RGB")


def main():
    args = sys.argv[1:]
    if not args:
        sys.exit(__doc__)
    outdir, args = args[0], args[1:]
    w, h = 1280, 720
    if len(args) >= 2 and args[0].isdigit() and args[1].isdigit():
        w, h, args = int(args[0]), int(args[1]), args[2:]
    seed = 0x1234567
    if args and args[0].isdigit():
        seed, args = int(args[0]), args[1:]
    os.makedirs(outdir, exist_ok=True)
    _, tile, cx, cy = layout(w, h)
    p = grow(w, h, cx, cy, tile, seed)
    layer, reach, start = raster(p)
    Image.fromarray(layer, "RGBA").save(os.path.join(outdir, "cracks-layer.png"))
    scene(w, h, seed, layer).save(os.path.join(outdir, "cracks-final.png"))
    print("cracks %d, points %d, reach %d px" % (len(p.lines), p.nodes, round(reach)))
    if "final" in args:
        return
    frames = []
    for t in (0, 25, 60, 120, 220, 420):
        u = 1 - (1 - min(1.0, t / GROW_MS)) ** 2
        front = start + (reach + max(10.0, 0.05 * reach) - start) * u
        frames.append(scene(w, h, seed, layer, front, reach).resize((w // 2, h // 2), Image.LANCZOS))
    sheet = Image.new("RGB", (w // 2 * 3, h // 2 * 2))
    for i, im in enumerate(frames):
        sheet.paste(im, ((i % 3) * (w // 2), (i // 3) * (h // 2)))
    sheet.save(os.path.join(outdir, "cracks-sheet.png"))


if __name__ == "__main__":
    main()
