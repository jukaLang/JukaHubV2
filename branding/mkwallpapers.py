#!/usr/bin/env python3
"""mkwallpapers.py: draw the wallpapers JukaHub ships for its themes.

Eight pictures, each made for a family of theme presets (themes.go) and
coloured from their palettes and backdrop glows, so a theme that brings its
wallpaper along still looks like itself under the glass:

    Neon Horizon     synthwave, vaporwave, cyberpunk, arcade
    Midnight Aurora  midnight, aurora, nord, dracula
    Deep Ocean       ocean, ocean glass, cobalt, blue
    Misty Forest     forest, green, gruvbox
    Ember Glow       ember, sunset, amber, ruby, red
    Paper Garden     paper, e-ink, sand, solarized light, moss
    Sakura Dusk      sakura, cute, girly, lavender, orchid
    Arctic Light     arctic, light

The player draws a wallpaper under the theme's background colour at about 60 %
(ui_glass.go drawAppBackdrop), so the dark ones are vivid and the light ones
soft: what survives that veil is what was meant to. Nothing important sits in
the middle, where the glass panels go.

They are 1280 x 720 JPEGs (the Smart Pro's screen); on a 4:3 screen the player
crops the middle instead of squeezing them (themewallpapers.go). Embedded from
player/builtin_wallpapers/ (go:embed) and copied to the data folder's
wallpapers on first use, where the wallpaper picker lists them.

Everything is drawn at 2x and reduced with Lanczos, with a little grain so the
gradients do not band. Run from anywhere (Python 3.9+, Pillow, numpy):

    python3 branding/mkwallpapers.py [--preview DIR]

Files whose bytes did not change are left alone; the output is deterministic.
"""

import argparse
import io
import math
import os
import random

import numpy as np
from PIL import Image, ImageDraw, ImageFilter

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
OUT = os.path.join(ROOT, "player", "builtin_wallpapers")

W, H = 1280, 720
S = 2  # supersampling
SW, SH = W * S, H * S


# ── helpers ───────────────────────────────────────────────────────────────

def hexc(h):
    h = h.lstrip("#")
    return np.array([int(h[i:i + 2], 16) for i in (0, 2, 4)], dtype=np.float32)


def vgrad(stops, h=SH, w=SW):
    """Vertical gradient through [(t, '#rrggbb'), ...], t in 0..1."""
    ys = np.linspace(0, 1, h, dtype=np.float32)
    out = np.zeros((h, 3), dtype=np.float32)
    for i in range(len(stops) - 1):
        t0, c0 = stops[i]
        t1, c1 = stops[i + 1]
        m = (ys >= t0) & (ys <= t1)
        k = ((ys[m] - t0) / max(1e-6, t1 - t0))[:, None]
        k = k * k * (3 - 2 * k)  # smoothstep between stops
        out[m] = hexc(c0) * (1 - k) + hexc(c1) * k
    return np.repeat(out[:, None, :], w, axis=1)


def grid():
    yy, xx = np.mgrid[0:SH, 0:SW].astype(np.float32)
    return xx / SW, yy / SH


def screen_blend(base, layer, alpha):
    """Screen-blend layer (0..255) over base with a per-pixel alpha (0..1)."""
    b = base / 255.0
    l = layer / 255.0
    s = 1 - (1 - b) * (1 - l)
    return (b * (1 - alpha) + s * alpha) * 255.0


def over(base, color, alpha):
    """Paint a flat colour over base with a per-pixel alpha (H x W)."""
    a = np.clip(alpha, 0, 1)[..., None]
    return base * (1 - a) + hexc(color) * a


def mask_from(draw_fn, blur=0):
    """A float mask (0..1) drawn with PIL at the supersampled size."""
    m = Image.new("L", (SW, SH), 0)
    draw_fn(ImageDraw.Draw(m))
    if blur:
        m = m.filter(ImageFilter.GaussianBlur(blur * S))
    return np.asarray(m, dtype=np.float32) / 255.0


def ridge_points(rng, x0, x1, y0, y1, rough, depth):
    """A natural ridge line by midpoint displacement, in supersampled px."""
    pts = [(x0, y0), (x1, y1)]
    disp = rough
    for _ in range(depth):
        new = [pts[0]]
        for (ax, ay), (bx, by) in zip(pts, pts[1:]):
            new += [((ax + bx) / 2, (ay + by) / 2 + rng.uniform(-disp, disp)), (bx, by)]
        pts = new
        disp *= 0.55
    return pts


def value_noise(rng, cells_x, cells_y, w=SW, h=SH):
    """Smooth noise: random values on a coarse grid, bicubic-upsampled."""
    small = Image.fromarray((rng.random((cells_y, cells_x)) * 255).astype(np.uint8), "L")
    big = small.resize((w, h), Image.BICUBIC)
    return np.asarray(big, dtype=np.float32) / 255.0


def fbm(rng, octaves=4, base=4):
    total, amp, norm = 0, 1.0, 0
    for o in range(octaves):
        n = base * (2 ** o)
        total = total + value_noise(rng, n * 16 // 9 + 1, n + 1) * amp
        norm += amp
        amp *= 0.5
    return total / norm


def finish(arr, seed):
    """Grain, clamp, reduce to 1280 x 720, return a PIL image."""
    rng = np.random.default_rng(seed + 991)
    arr = arr + rng.normal(0, 1.6, arr.shape).astype(np.float32)
    img = Image.fromarray(np.clip(arr, 0, 255).astype(np.uint8), "RGB")
    return img.resize((W, H), Image.LANCZOS)


def stars(img, rng, count, ymax, bright=1.0, avoid=None):
    """Small stars over the top part, a few with a soft halo."""
    layer = Image.new("L", (SW, SH), 0)
    d = ImageDraw.Draw(layer)
    for _ in range(count):
        x = rng.random() * SW
        y = (rng.random() ** 1.6) * ymax * SH
        r = (0.5 + rng.random() ** 6 * 2.2) * S
        v = int(90 + rng.random() * 165)
        d.ellipse([x - r, y - r, x + r, y + r], fill=v)
    halo = layer.filter(ImageFilter.GaussianBlur(3 * S))
    m = (np.asarray(layer, np.float32) + np.asarray(halo, np.float32) * 1.6) / 255.0 * bright
    if avoid is not None:
        m = m * (1 - np.clip(avoid, 0, 1))
    return screen_blend(img, np.full_like(img, 255), np.clip(m, 0, 1)[..., None])


# ── Neon Horizon (synthwave) ─────────────────────────────────────────────

def neon_horizon():
    rng = random.Random(11)
    nrng = np.random.default_rng(11)
    hz = 0.60  # the horizon, as a fraction of the height
    img = vgrad([(0, "#0d0418"), (0.30, "#23093a"), (0.52, "#5a1257"), (hz, "#ff5f9e"), (hz + 0.001, "#16052a"), (1, "#07020f")])
    img = stars(img, nrng, 260, hz * 0.85, 0.9)
    # The sun: a disc with a warm-to-magenta gradient, sliced by gaps that
    # widen toward the horizon.
    cx, cy, r = 0.5 * SW, 0.40 * SH, 0.22 * SH
    x, y = grid()
    xs, ys = x * SW, y * SH
    disc = ((xs - cx) ** 2 + (ys - cy) ** 2) <= r * r
    t = np.clip((ys - (cy - r)) / (2 * r), 0, 1)
    sun = hexc("#ffe07a")[None, None, :] * (1 - t[..., None]) + hexc("#ff3fb9")[None, None, :] * t[..., None]
    cut = np.zeros_like(t, dtype=bool)
    for i in range(7):
        top = cy + r * (0.05 + i * 0.13)
        cut |= (ys >= top) & (ys < top + r * (0.012 + i * 0.012))
    sun_m = (disc & ~cut).astype(np.float32)
    sun_m = np.asarray(Image.fromarray((sun_m * 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(0.8 * S)), np.float32) / 255
    img = img * (1 - sun_m[..., None]) + sun * sun_m[..., None]
    img += np.exp(-(((xs - cx) ** 2 + (ys - cy) ** 2) / (r * 1.9) ** 2))[..., None] * hexc("#ff4fd8") * 0.55
    # Mountains in front of the sun's foot.
    def ridge(d, base, amp, color, seed):
        rr = random.Random(seed)
        pts = [(0, SH)]
        xcur = 0
        while xcur <= SW:
            pts.append((xcur, base * SH - rr.random() * amp * SH))
            xcur += rr.randint(40, 110) * S
        pts += [(SW, base * SH), (SW, SH)]
        d.polygon(pts, fill=color)
    m1 = mask_from(lambda d: ridge(d, hz + 0.002, 0.11, 255, 5))
    m2 = mask_from(lambda d: ridge(d, hz + 0.002, 0.06, 255, 9))
    img = over(img, "#2a0b45", m1 * 0.95)
    img = over(img, "#14052a", m2)
    # The floor: a perspective grid in magenta and cyan, glowing.
    floor = mask_from(lambda d: d.rectangle([0, hz * SH, SW, SH], fill=255))
    lines = Image.new("L", (SW, SH), 0)
    d = ImageDraw.Draw(lines)
    vx, vy = 0.5 * SW, hz * SH
    for k in range(1, 26):
        yy = vy + (k ** 2.05) * 0.95 * S
        if yy > SH:
            break
        d.line([(0, yy), (SW, yy)], fill=255, width=max(1, int(S * (0.8 + k * 0.08))))
    for k in range(-24, 25):
        x0 = vx + k * 0.032 * SW
        x1 = vx + k * 0.28 * SW
        d.line([(x0, vy), (x1, SH)], fill=255, width=2 * S)
    lm = np.asarray(lines, np.float32) / 255 * floor
    lglow = np.asarray(Image.fromarray((lm * 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(6 * S)), np.float32) / 255
    fade = np.clip((y - hz) / (1 - hz), 0, 1) ** 0.6
    col = hexc("#ff4fd8")[None, None, :] * (1 - x[..., None]) + hexc("#43d9ff")[None, None, :] * x[..., None]
    img = img + col * (lm * (0.35 + 0.65 * fade))[..., None] * 0.9 + col * (lglow * 1.4 * fade)[..., None]
    # Haze over the horizon.
    img += np.exp(-((y - hz) / 0.035) ** 2)[..., None] * hexc("#ff7ab8") * 0.55
    return finish(img, 11)


# ── Midnight Aurora ──────────────────────────────────────────────────────

def midnight_aurora():
    nrng = np.random.default_rng(21)
    img = vgrad([(0, "#03050f"), (0.55, "#081430"), (1, "#0c1d36")])
    x, y = grid()
    aur = np.zeros((SH, SW), np.float32)
    tint_mix = np.zeros((SH, SW), np.float32)
    rays = value_noise(nrng, 220, 3) * 0.55 + value_noise(nrng, 60, 2) * 0.45
    for (base, amp, freq, phase, height, weight) in [(0.40, 0.07, 1.7, 0.4, 0.22, 1.0), (0.30, 0.05, 2.6, 2.1, 0.15, 0.7), (0.47, 0.04, 3.3, 4.0, 0.10, 0.5)]:
        edge = base + amp * np.sin(x * freq * 2 * math.pi + phase) + 0.025 * np.sin(x * 9.1 + phase * 3)
        above = np.clip((edge - y) / height, 0, None)
        curtain = np.where(y <= edge, np.exp(-above * 2.2), np.exp(-((y - edge) / 0.012) ** 2))
        aur += curtain * weight * (0.35 + 0.65 * rays)
        tint_mix += curtain * weight * np.clip(above, 0, 1)
    aur = np.clip(aur, 0, 1.4)
    green = hexc("#3dffb0")
    teal = hexc("#2bd9c1")
    violet = hexc("#8b5cf6")
    k = np.clip(tint_mix / (aur + 1e-3), 0, 1)[..., None]
    col = teal * (1 - k) + violet * k
    col = col * 0.85 + green * 0.15 * (1 - k)
    img = img + col * (aur[..., None] * 0.75)
    img = stars(img, nrng, 520, 0.75, 0.9, avoid=aur * 1.2)
    # Two ridges of mountains and a faint mist between them.
    def ridge(d, base, amp, seed, step):
        rr = random.Random(seed)
        pts = [(0, SH)]
        xx = 0
        hcur = rr.random()
        while xx <= SW + step * S:
            hcur = min(1, max(0, hcur + (rr.random() - 0.5) * 0.7))
            pts.append((xx, (base - amp * hcur) * SH))
            xx += step * S
        pts += [(SW, SH)]
        d.polygon(pts, fill=255)
    back = mask_from(lambda d: ridge(d, 0.86, 0.16, 3, 46), blur=0.6)
    front = mask_from(lambda d: ridge(d, 0.97, 0.12, 8, 34), blur=0.6)
    img = over(img, "#0d1a33", back)
    img += (np.exp(-((y - 0.86) / 0.05) ** 2) * (1 - front))[..., None] * hexc("#2a4a7a") * 0.35
    img = over(img, "#050a16", front)
    return finish(img, 21)


# ── Deep Ocean ───────────────────────────────────────────────────────────

def deep_ocean():
    nrng = np.random.default_rng(31)
    rng = random.Random(31)
    img = vgrad([(0, "#0d5f86"), (0.25, "#073a5c"), (0.65, "#03182e"), (1, "#020914")])
    x, y = grid()
    # Light shafts fanning down from the surface.
    shafts = np.zeros((SH, SW), np.float32)
    for i in range(9):
        x0 = 0.12 + i * 0.1 + rng.uniform(-0.03, 0.03)
        slope = (x0 - 0.5) * 0.35 + rng.uniform(-0.05, 0.05)
        width = rng.uniform(0.012, 0.035)
        cxl = x0 + slope * y
        shafts += np.exp(-((x - cxl) / (width * (1 + y * 1.5))) ** 2) * (1 - y) ** 1.6 * rng.uniform(0.4, 1.0)
    img += shafts[..., None] * hexc("#5fd8f0") * 0.32
    # Caustics just under the surface: thin bright threads that fade fast.
    n = np.sin(x * 38 + np.sin(y * 21 + x * 7) * 2.2) + np.sin(y * 47 - x * 16 + np.sin(x * 13) * 1.7)
    caus = np.clip(1 - np.abs(n) * 1.8, 0, 1) ** 4 * np.exp(-y / 0.09)
    img += caus[..., None] * hexc("#9ff0ff") * 0.28
    # Bubbles rising on the right.
    bub = Image.new("L", (SW, SH), 0)
    d = ImageDraw.Draw(bub)
    for _ in range(46):
        bx = rng.gauss(0.80, 0.07) * SW
        by = rng.uniform(0.15, 0.95) * SH
        r = rng.uniform(2, 9) * S * (1.2 - by / SH * 0.6)
        d.ellipse([bx - r, by - r, bx + r, by + r], outline=200, width=max(1, int(S * 1.2)))
        d.ellipse([bx - r * 0.45, by - r * 0.55, bx - r * 0.1, by - r * 0.2], fill=255)
    bm = np.asarray(bub.filter(ImageFilter.GaussianBlur(0.7 * S)), np.float32) / 255
    img = screen_blend(img, np.full_like(img, 220), bm[..., None] * 0.55)
    # A reef along the bottom: two smooth ridges of rock, the far one hazy.
    def reef(d, base, amp, seed):
        rr = random.Random(seed)
        pts = ridge_points(rr, -20 * S, SW + 20 * S, (base - amp * 0.5) * SH, (base - amp * 0.3) * SH, amp * SH, 7)
        d.polygon([(-20 * S, SH)] + pts + [(SW + 20 * S, SH)], fill=255)
    r1 = mask_from(lambda d: reef(d, 0.97, 0.13, 4), blur=3)
    r2 = mask_from(lambda d: reef(d, 1.02, 0.09, 6), blur=0.8)
    img = over(img, "#06294a", r1 * 0.85)
    img = over(img, "#010812", r2)
    return finish(img, 31)


# ── Misty Forest ─────────────────────────────────────────────────────────

def misty_forest():
    rng = random.Random(41)
    nrng = np.random.default_rng(41)
    img = vgrad([(0, "#08130e"), (0.22, "#1f3b2b"), (0.40, "#c9963f"), (0.50, "#6f8a5a"), (1, "#0a140f")])
    x, y = grid()
    img += np.exp(-(((x - 0.62) / 0.22) ** 2 + ((y - 0.40) / 0.10) ** 2))[..., None] * hexc("#ffd27a") * 0.6
    layers = [(0.50, "#4f7a5e", 0.55, 26), (0.62, "#2f5a43", 0.65, 34), (0.76, "#1a3a2a", 0.8, 44), (0.92, "#08140d", 1.0, 60)]
    fog = hexc("#9fc2a6")
    for li, (base, color, opacity, tree_h) in enumerate(layers):
        def draw_trees(d, base=base, tree_h=tree_h, li=li):
            xx = -20 * S
            hill_phase = rng.random() * 6
            while xx < SW + 20 * S:
                hill = math.sin(xx / SW * 3.1 + hill_phase) * 0.025 + math.sin(xx / SW * 8.3 + hill_phase * 2) * 0.01
                h = tree_h * S * rng.uniform(0.6, 1.25)
                w = h * rng.uniform(0.32, 0.42)
                bx = xx
                by = (base + hill) * SH
                # a pine: stacked triangles
                for tier in range(4):
                    ty = by - h * (0.18 + tier * 0.24)
                    tw = w * (1 - tier * 0.2)
                    d.polygon([(bx - tw, ty + h * 0.3), (bx + tw, ty + h * 0.3), (bx, ty - h * 0.12)], fill=255)
                d.rectangle([bx - w * 0.08, by - h * 0.2, bx + w * 0.08, by + 4 * S], fill=255)
                d.rectangle([bx - w * 1.3, by, bx + w * 1.3, SH], fill=255)
                xx += w * rng.uniform(0.9, 1.7)
        m = mask_from(draw_trees, blur=0.5 if li == 3 else 0.9)
        img = over(img, color, m * opacity)
        # mist rising from behind the next layer
        mist = np.exp(-((y - base - 0.02) / 0.05) ** 2) * (0.55 - li * 0.1)
        mist *= 0.7 + 0.3 * value_noise(nrng, 18, 4)
        img = img * (1 - mist[..., None] * 0.6) + fog * (mist[..., None] * 0.6)
    # Fireflies low in the front.
    ff = Image.new("L", (SW, SH), 0)
    d = ImageDraw.Draw(ff)
    for _ in range(34):
        fx, fy = rng.uniform(0.05, 0.95) * SW, rng.uniform(0.72, 0.97) * SH
        r = rng.uniform(1.2, 2.6) * S
        d.ellipse([fx - r, fy - r, fx + r, fy + r], fill=255)
    fm = np.asarray(ff, np.float32) / 255
    fg = np.asarray(ff.filter(ImageFilter.GaussianBlur(5 * S)), np.float32) / 255
    img += (fm * 0.9 + fg * 2.2)[..., None] * hexc("#ffd36b")
    return finish(img, 41)


# ── Ember Glow ───────────────────────────────────────────────────────────

def ember_glow():
    rng = random.Random(51)
    nrng = np.random.default_rng(51)
    img = vgrad([(0, "#0a0605"), (0.55, "#1a0b07"), (0.85, "#4a1608"), (1, "#a8380c")])
    x, y = grid()
    img += np.exp(-(((x - 0.5) / 0.6) ** 2 + ((y - 1.05) / 0.22) ** 2))[..., None] * hexc("#f18752") * 0.9
    smoke = fbm(nrng, 4, 3)
    img *= (0.8 + 0.35 * smoke)[..., None]
    sparks = Image.new("L", (SW, SH), 0)
    d = ImageDraw.Draw(sparks)
    for _ in range(260):
        sy = (1 - rng.random() ** 2.2) * SH
        sx = rng.random() * SW
        life = sy / SH
        r = (0.6 + rng.random() * 1.8) * S * (0.4 + life)
        length = rng.uniform(4, 22) * S * life
        ang = math.radians(rng.uniform(-80, -60))
        v = int(120 + 135 * life * rng.random())
        d.line([(sx, sy), (sx + math.cos(ang) * length * 0.4, sy + math.sin(ang) * length)], fill=v // 2, width=max(1, int(r)))
        d.ellipse([sx - r, sy - r, sx + r, sy + r], fill=v)
    sm = np.asarray(sparks, np.float32) / 255
    sg = np.asarray(sparks.filter(ImageFilter.GaussianBlur(4 * S)), np.float32) / 255
    hot = hexc("#ffd36b")[None, None, :] * (1 - y[..., None]) + hexc("#ff5e00")[None, None, :] * y[..., None]
    img += hot * (sm * 1.0 + sg * 1.8)[..., None]
    return finish(img, 51)


# ── Paper Garden (light) ─────────────────────────────────────────────────

def paper_garden():
    rng = random.Random(61)
    nrng = np.random.default_rng(61)
    img = vgrad([(0, "#f6f2e8"), (1, "#efe8d8")])
    fibers = fbm(nrng, 5, 6)
    img *= (0.965 + 0.05 * fibers)[..., None]
    fine = nrng.normal(0, 1, (SH, SW)).astype(np.float32)
    fine = np.asarray(Image.fromarray(np.clip(fine * 40 + 128, 0, 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(0.8 * S)), np.float32)
    img += ((fine - 128) / 128 * 3.0)[..., None]

    def wash(color, draw_fn, alpha, blur=2.5):
        m = mask_from(draw_fn, blur=blur)
        rim = m - np.asarray(Image.fromarray((m * 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(6 * S)), np.float32) / 255
        tex = 0.75 + 0.5 * value_noise(nrng, 40, 24)
        a = (m * alpha * tex + np.clip(rim, 0, 1) * alpha * 0.9)
        # multiply-style: darken the paper toward the colour
        c = hexc(color)
        return img * (1 - a[..., None]) + (img * c / 255.0) * a[..., None]

    def leaf(d, cx, cy, length, width, angle):
        pts = []
        for i in range(25):
            t = i / 24
            w = math.sin(math.pi * t) * width * (1 - 0.3 * t)
            pts.append((t * length, w))
        for i in range(24, -1, -1):
            t = i / 24
            w = math.sin(math.pi * t) * width * (1 - 0.3 * t)
            pts.append((t * length, -w * 0.85))
        ca, sa = math.cos(angle), math.sin(angle)
        d.polygon([(cx + (px * ca - py * sa) * S, cy + (px * sa + py * ca) * S) for px, py in pts], fill=255)

    stem_paths = []

    def sprig(d, ox, oy, direction, count, seed):
        """Leaves on alternate sides of a gently bending stem; the stem's
        points are kept so it can be inked afterwards."""
        rr = random.Random(seed)
        ang = direction
        x0, y0 = ox, oy
        path = [(x0, y0)]
        for i in range(count):
            step = rr.uniform(34, 52)
            x1 = x0 + math.cos(ang) * step * S
            y1 = y0 + math.sin(ang) * step * S
            side = 1 if i % 2 else -1
            # the leaf grows from the stem, pointing out and forward
            leaf(d, x1, y1, rr.uniform(70, 110) * (1 - i / count * 0.45), rr.uniform(18, 28) * (1 - i / count * 0.4),
                 ang + side * rr.uniform(0.6, 0.95))
            x0, y0 = x1, y1
            path.append((x0, y0))
            ang += rr.uniform(-0.16, 0.16)
        stem_paths.append(path)

    def berries(d, pts):
        for x, y, r in pts:
            d.ellipse([x - r, y - r, x + r, y + r], fill=255)

    img = wash("#a9bf98", lambda d: sprig(d, -10 * S, 120 * S, 0.35, 9, 1), 0.55)
    img = wash("#c9b27a", lambda d: sprig(d, 60 * S, -20 * S, 1.2, 6, 2), 0.45)
    img = wash("#d79a7c", lambda d: berries(d, [(150 * S, 300 * S, 22 * S), (205 * S, 250 * S, 16 * S), (110 * S, 230 * S, 13 * S)]), 0.5)
    img = wash("#9db58f", lambda d: sprig(d, SW + 10 * S, SH - 90 * S, math.pi + 0.3, 10, 3), 0.55)
    img = wash("#cfae6c", lambda d: sprig(d, SW - 80 * S, SH + 20 * S, -math.pi / 2 - 0.5, 6, 4), 0.45)
    img = wash("#d4896a", lambda d: berries(d, [(SW - 210 * S, SH - 330 * S, 20 * S), (SW - 160 * S, SH - 380 * S, 14 * S)]), 0.45)
    # The stems, inked along the paths the leaves grew from, thinning out.
    stems = Image.new("L", (SW, SH), 0)
    d = ImageDraw.Draw(stems)
    for path in stem_paths:
        for i, (a, b) in enumerate(zip(path, path[1:])):
            d.line([a, b], fill=255, width=max(1, int((2.6 - 1.6 * i / len(path)) * S)))
    sm = np.asarray(stems.filter(ImageFilter.GaussianBlur(0.6 * S)), np.float32) / 255
    img = over(img, "#7d8b62", sm * 0.6)
    return finish(img, 61)


# ── Sakura Dusk (light) ──────────────────────────────────────────────────

def sakura_dusk():
    rng = random.Random(71)
    img = vgrad([(0, "#fbdde6"), (0.5, "#fff0ee"), (1, "#fff8f3")])
    x, y = grid()
    img += np.exp(-(((x - 0.68) / 0.35) ** 2 + ((y - 0.58) / 0.22) ** 2))[..., None] * hexc("#ffdcca") * 0.22
    # A far mountain with soft flanks and a snow cap, fading into the haze.
    peak_x, peak_y = 0.66 * SW, 0.47 * SH

    def flank(t):
        return peak_y + (SH * 0.62) * (abs(t) ** 1.25)

    left = [(peak_x - u * 0.30 * SW, flank(u)) for u in np.linspace(0, 1, 40)]
    right = [(peak_x + u * 0.33 * SW, flank(u * 0.95)) for u in np.linspace(0, 1, 40)]
    mtn = mask_from(lambda d: d.polygon(list(reversed(left)) + right[1:] + [(SW, SH), (0.2 * SW, SH)], fill=255), blur=1.5)
    snow_rr = random.Random(5)
    cap_pts = list(reversed(left[:9])) + right[1:10]
    edge = [(px, py + snow_rr.uniform(4, 16) * S) for px, py in reversed(cap_pts)]
    cap = mask_from(lambda d: d.polygon(cap_pts + edge, fill=255), blur=1.2)
    tone = vgrad([(0, "#dcc3dc"), (0.5, "#e6cfe2"), (0.8, "#f6e6ee"), (1, "#fff3f3")])
    img = img * (1 - mtn[..., None] * 0.85) + tone * (mtn[..., None] * 0.85)
    img = over(img, "#fffafd", cap * 0.9)

    branches = Image.new("L", (SW, SH), 0)
    flowers = Image.new("L", (SW, SH), 0)
    centers = Image.new("L", (SW, SH), 0)
    db, df, dc = ImageDraw.Draw(branches), ImageDraw.Draw(flowers), ImageDraw.Draw(centers)

    def blossom(cx, cy, r):
        rot = rng.random() * math.tau
        for p in range(5):
            a = rot + p * math.tau / 5
            px, py = cx + math.cos(a) * r * 0.55, cy + math.sin(a) * r * 0.55
            df.ellipse([px - r * 0.5, py - r * 0.5, px + r * 0.5, py + r * 0.5], fill=int(rng.uniform(185, 255)))
        dc.ellipse([cx - r * 0.17, cy - r * 0.17, cx + r * 0.17, cy + r * 0.17], fill=255)

    def cluster(cx, cy, n):
        for _ in range(n):
            blossom(cx + rng.gauss(0, 13) * S, cy + rng.gauss(0, 10) * S, rng.uniform(9, 14) * S)

    def branch(x0, y0, ang, length, width, depth, droop):
        """A tapering, gently curving branch; side shoots; blossoms bunched
        along it and at its tip."""
        segs = 10
        xx, yy = x0, y0
        for i in range(segs):
            ang += rng.uniform(-0.10, 0.10) + droop
            step = length / segs
            x1, y1 = xx + math.cos(ang) * step, yy + math.sin(ang) * step
            w = max(1.5 * S, width * (1 - 0.75 * i / segs))
            db.line([(xx, yy), (x1, y1)], fill=255, width=int(w))
            db.ellipse([x1 - w / 2, y1 - w / 2, x1 + w / 2, y1 + w / 2], fill=255)
            if i > 2 and rng.random() < 0.45:
                cluster(x1, y1, rng.randint(1, 3))
            if depth > 1 and i > 1 and rng.random() < 0.32:
                side = rng.choice([-1, 1])
                branch(x1, y1, ang + side * rng.uniform(0.45, 0.85), length * rng.uniform(0.35, 0.5), w * 0.65, depth - 1, droop)
            xx, yy = x1, y1
        cluster(xx, yy, rng.randint(4, 7))

    # Along the top edge, curving up: the middle stays clear for the glass.
    branch(-30 * S, 70 * S, 0.16, 640 * S, 26 * S, 3, -0.012)
    branch(SW + 30 * S, 40 * S, math.pi - 0.22, 520 * S, 20 * S, 3, 0.014)
    bm = np.asarray(branches.filter(ImageFilter.GaussianBlur(0.6 * S)), np.float32) / 255
    fm = np.asarray(flowers.filter(ImageFilter.GaussianBlur(0.7 * S)), np.float32) / 255
    cm = np.asarray(centers.filter(ImageFilter.GaussianBlur(0.5 * S)), np.float32) / 255
    img = over(img, "#5a3646", bm * 0.9)
    petal = hexc("#f59cbd")[None, None, :] * (1 - y[..., None]) + hexc("#ffc4d6")[None, None, :] * y[..., None]
    img = img * (1 - fm[..., None] * 0.92) + petal * (fm[..., None] * 0.92)
    img = over(img, "#d24f7a", cm * 0.85)
    # Petals drifting down, more of them near the branches.
    pet = Image.new("L", (SW, SH), 0)
    for _ in range(55):
        px, py = rng.random() * SW, (rng.random() ** 1.4) * SH
        r = rng.uniform(4, 7.5) * S
        ell = Image.new("L", (int(r * 4), int(r * 4)), 0)
        ImageDraw.Draw(ell).ellipse([r * 0.8, r * 1.45, r * 3.2, r * 2.55], fill=int(rng.uniform(150, 230)))
        ell = ell.rotate(rng.uniform(0, 180), resample=Image.BICUBIC)
        pet.paste(ell, (int(px), int(py)), ell)
    pm = np.asarray(pet.filter(ImageFilter.GaussianBlur(0.6 * S)), np.float32) / 255
    img = over(img, "#f4a3bf", pm * 0.75)
    return finish(img, 71)


# ── Arctic Light (light) ─────────────────────────────────────────────────

def arctic_light():
    rng = random.Random(81)
    nrng = np.random.default_rng(81)
    img = vgrad([(0, "#c9def0"), (0.45, "#e4eff9"), (1, "#f4f8fc")])
    x, y = grid()
    img += np.exp(-(((x - 0.78) / 0.20) ** 2 + ((y - 0.16) / 0.16) ** 2))[..., None] * hexc("#ffffff") * 0.40
    # Pale bands of light in the sky.
    band = np.zeros((SH, SW), np.float32)
    for base, amp, ph in [(0.18, 0.05, 0.5), (0.26, 0.035, 2.0)]:
        edge = base + amp * np.sin(x * 2 * math.pi * 1.3 + ph)
        band += np.exp(-((y - edge) / 0.028) ** 2) * (0.6 + 0.4 * value_noise(nrng, 80, 3))
    img = img * (1 - band[..., None] * 0.22) + hexc("#a7e3ee") * (band[..., None] * 0.22)

    # Three ranges, far to near: natural ridges, each filled with a vertical
    # tint (bluer in its folds), with a crisp lit edge along the top.
    ranges = [(0.62, 0.20, 2, "#d3e2f0", "#b9cde2"), (0.76, 0.18, 5, "#e3edf6", "#c4d6e8"), (0.92, 0.13, 9, "#f3f8fc", "#d3e1ee")]
    for base, amp, seed, top, low in ranges:
        rr = random.Random(seed)
        pts = ridge_points(rr, -40 * S, SW + 40 * S, (base - amp * rr.uniform(0.3, 0.7)) * SH, (base - amp * rr.uniform(0.3, 0.7)) * SH, amp * SH * 1.4, 8)
        m = mask_from(lambda d, pts=pts: d.polygon([(-40 * S, SH)] + pts + [(SW + 40 * S, SH)], fill=255), blur=0.4)
        tint = vgrad([(0, top), (base, top), (min(1, base + 0.25), low), (1, low)])
        img = img * (1 - m[..., None]) + tint * m[..., None]
        # Shade the flanks that face away from the sun (it is up to the right):
        # where the ridge climbs to the right, the slope below it is in shadow.
        px = np.array([p[0] for p in pts], np.float32)
        py = np.array([p[1] for p in pts], np.float32)
        cols = np.arange(SW, dtype=np.float32)
        ridge_y = np.interp(cols, px, py)
        # the flanks, not every notch: the slope of a well-smoothed ridge
        k = np.exp(-np.linspace(-3, 3, 241) ** 2)
        k /= k.sum()
        pad = np.pad(ridge_y, 120, mode="edge")
        smooth = np.convolve(pad, k, mode="same")[120:-120]
        slope = np.gradient(smooth)
        shade_col = np.clip(-slope * 2.2, 0, 1)
        depth = np.exp(-np.clip(np.arange(SH, dtype=np.float32)[:, None] - ridge_y[None, :], 0, None) / (0.16 * SH))
        shade = shade_col[None, :] * depth
        # softened in both directions, so it reads as light on a slope rather
        # than as stripes down from the ridge
        shade = np.asarray(Image.fromarray(np.clip(shade * 255, 0, 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(24 * S)), np.float32) / 255
        img = over(img, low, shade * m * 0.55)
        edge_img = Image.new("L", (SW, SH), 0)
        ImageDraw.Draw(edge_img).line(pts, fill=255, width=int(2.2 * S))
        em = np.asarray(edge_img.filter(ImageFilter.GaussianBlur(0.9 * S)), np.float32) / 255
        img = over(img, "#ffffff", em * 0.75)
        # mist at each range's foot
        img = over(img, "#f6fafd", np.exp(-((y - base - 0.05) / 0.05) ** 2) * 0.35)
    flakes = Image.new("L", (SW, SH), 0)
    d = ImageDraw.Draw(flakes)
    for _ in range(90):
        fx, fy = rng.random() * SW, rng.random() * 0.7 * SH
        r = rng.uniform(1, 2.6) * S
        d.ellipse([fx - r, fy - r, fx + r, fy + r], fill=255)
    fm = np.asarray(flakes.filter(ImageFilter.GaussianBlur(0.8 * S)), np.float32) / 255
    img = over(img, "#ffffff", fm * 0.8)
    return finish(img, 81)


WALLPAPERS = [
    ("neon-horizon", neon_horizon),
    ("midnight-aurora", midnight_aurora),
    ("deep-ocean", deep_ocean),
    ("misty-forest", misty_forest),
    ("ember-glow", ember_glow),
    ("paper-garden", paper_garden),
    ("sakura-dusk", sakura_dusk),
    ("arctic-light", arctic_light),
]


def jpeg_bytes(img):
    buf = io.BytesIO()
    img.save(buf, "JPEG", quality=88, optimize=True, subsampling="4:2:0")
    return buf.getvalue()


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--preview", help="also write a contact sheet and each picture under the backdrop's veil here")
    ap.add_argument("--only", help="draw just this one (an id)")
    args = ap.parse_args()
    os.makedirs(OUT, exist_ok=True)
    made = []
    for wid, fn in WALLPAPERS:
        if args.only and wid != args.only:
            continue
        img = fn()
        data = jpeg_bytes(img)
        path = os.path.join(OUT, wid + ".jpg")
        old = open(path, "rb").read() if os.path.exists(path) else None
        if old != data:
            with open(path, "wb") as f:
                f.write(data)
        made.append((wid, img, len(data), old == data))
        print("%-16s %6d bytes%s" % (wid, len(data), " (unchanged)" if old == data else ""))
    if args.preview and made:
        os.makedirs(args.preview, exist_ok=True)
        cols = 2
        rows = (len(made) + cols - 1) // cols
        tw, th = 640, 360
        sheet = Image.new("RGB", (cols * tw, rows * th), (0, 0, 0))
        for i, (wid, img, _, _) in enumerate(made):
            sheet.paste(img.resize((tw, th), Image.LANCZOS), ((i % cols) * tw, (i // cols) * th))
        sheet.save(os.path.join(args.preview, "wallpapers-sheet.png"))


if __name__ == "__main__":
    main()
