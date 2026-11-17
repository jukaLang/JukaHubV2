#!/usr/bin/env python3
"""mkglass_concepts.py: render the liquid glass concept images in docs/THEMES.md.

These are design concepts, not screenshots. Each pane is drawn the way the
Realistic glass style describes it: the scene behind is blurred and slightly
magnified, the last few pixels of the edge bend the scene like the rim of a
lens (with a touch of colour fringing), the top catches a sheen, a bright rim
follows the light from the top left with a softer glint on the opposite
corner, and floating panes sit on a soft shadow. Shapes are signed-distance
fields, so edges are anti-aliased exactly.

All data is made up: the clock reads 9:41, the network is "HomeNet", the
games and the art are drawn here, and the hints are controller hints.

Run from anywhere (Python 3.9+, numpy, Pillow):

    python3 branding/mkglass_concepts.py            # write docs/assets/glass-concept-*.png
    python3 branding/mkglass_concepts.py --only 2   # one image
"""

import math
import os
import sys

import numpy as np
from PIL import Image, ImageDraw, ImageFilter, ImageFont

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "docs", "assets")
FONT_R = os.path.join(ROOT, "player", "Inter-Regular.ttf")
FONT_S = os.path.join(ROOT, "player", "Inter-SemiBold.ttf")

W, H = 1280, 720
YY, XX = np.mgrid[0:H, 0:W].astype(np.float32)

ACCENT = (0.31, 0.82, 1.0)  # the info accent, cyan
VIOLET = (0.62, 0.45, 1.0)
TXT1 = (242, 245, 250)
TXT2 = (176, 186, 202)
TXT3 = (130, 140, 158)

_fonts = {}


def font(size, bold=False):
    key = (size, bold)
    if key not in _fonts:
        _fonts[key] = ImageFont.truetype(FONT_S if bold else FONT_R, size)
    return _fonts[key]


# ---------------------------------------------------------------- primitives

def sdf_rrect(X, Y, x, y, w, h, r):
    cx, cy = x + w / 2, y + h / 2
    qx = np.abs(X - cx) - (w / 2 - r)
    qy = np.abs(Y - cy) - (h / 2 - r)
    ox, oy = np.maximum(qx, 0), np.maximum(qy, 0)
    return np.sqrt(ox * ox + oy * oy) + np.minimum(np.maximum(qx, qy), 0) - r


def sample(img, sx, sy):
    h, w = img.shape[:2]
    sx = np.clip(sx, 0, w - 1.001)
    sy = np.clip(sy, 0, h - 1.001)
    x0 = sx.astype(np.int32)
    y0 = sy.astype(np.int32)
    fx = sx - x0
    fy = sy - y0
    a = img[y0, x0]
    b = img[y0, x0 + 1]
    c = img[y0 + 1, x0]
    d = img[y0 + 1, x0 + 1]
    return (a * (1 - fx) + b * fx) * (1 - fy) + (c * (1 - fx) + d * fx) * fy


def to_pil(a):
    return Image.fromarray((np.clip(a, 0, 1) * 255 + 0.5).astype(np.uint8))


def from_pil(im):
    return np.asarray(im.convert("RGB"), np.float32) / 255


def _box(a, r, axis):
    n = a.shape[axis]
    pad = [(0, 0)] * a.ndim
    pad[axis] = (r + 1, r)
    p = np.pad(a, pad, mode="edge")
    cs = np.cumsum(p, axis=axis, dtype=np.float64)
    hi = np.take(cs, np.arange(2 * r + 1, 2 * r + 1 + n), axis=axis)
    lo = np.take(cs, np.arange(0, n), axis=axis)
    return ((hi - lo) / (2 * r + 1)).astype(np.float32)


def blur(a, radius):
    """Gaussian-like blur in float: three box passes per axis (no banding)."""
    r = max(int(round(radius * 0.58)), 1)
    out = a
    for _ in range(3):
        out = _box(_box(out, r, 0), r, 1)
    return out


def blobs(base_top, base_bottom, spots):
    """Navy gradient plus soft coloured lights: spots = (x, y, sigma, rgb, k)."""
    t = (YY / H)[..., None]
    a = np.array(base_top, np.float32) * (1 - t) + np.array(base_bottom, np.float32) * t
    for (x, y, s, col, k) in spots:
        g = np.exp(-((XX - x) ** 2 + (YY - y) ** 2) / (2 * s * s))[..., None]
        a = a + g * np.array(col, np.float32) * k
    # a little grain so the blur has something to bend
    rng = np.random.default_rng(7)
    a = a + (rng.random((H, W, 1), np.float32) - 0.5) * 0.012
    return np.clip(a, 0, 1)


class Canvas:
    def __init__(self, arr):
        self.a = arr.astype(np.float32)

    def pil(self):
        return to_pil(self.a)

    def snapshot(self, radius=0):
        return blur(self.a, radius) if radius else self.a.copy()

    def layer(self, fn):
        lay = Image.new("RGBA", (W, H), (0, 0, 0, 0))
        fn(ImageDraw.Draw(lay), lay)
        base = self.pil().convert("RGBA")
        self.a = from_pil(Image.alpha_composite(base, lay))

    def paste(self, img, x, y, r=0):
        """Paste an RGB PIL image with rounded corners."""
        w, h = img.size
        x0, y0 = max(x, 0), max(y, 0)
        x1, y1 = min(x + w, W), min(y + h, H)
        if x1 <= x0 or y1 <= y0:
            return
        X = XX[y0:y1, x0:x1]
        Y = YY[y0:y1, x0:x1]
        cov = np.clip(0.5 - sdf_rrect(X, Y, x, y, w, h, r), 0, 1)[..., None]
        src = from_pil(img)[y0 - y:y1 - y, x0 - x:x1 - x]
        self.a[y0:y1, x0:x1] = self.a[y0:y1, x0:x1] * (1 - cov) + src * cov

    def shadow(self, x, y, w, h, r, dy=10, radius=22, alpha=0.45):
        m = Image.new("L", (W, H), 0)
        ImageDraw.Draw(m).rounded_rectangle([x, y + dy, x + w, y + h + dy], r, fill=255)
        k = np.asarray(m.filter(ImageFilter.GaussianBlur(radius)), np.float32)[..., None] / 255
        self.a *= 1 - k * alpha

    def dim(self, k, col=(0, 0, 0)):
        self.a = self.a * (1 - k) + np.array(col, np.float32) * k

    def glass(self, src, x, y, w, h, r, mag=1.05, bend=9.0, edge=16.0,
              tint=(1, 1, 1), ta=0.07, dark=0.0, sheen=0.10, rim=0.85,
              hair=0.16, fringe=0.10, focus=None, light=(-0.6, -0.8), glow=0.0):
        """Composite one pane of liquid glass refracting `src`."""
        pad = 30 if focus else 4
        x0, y0 = max(int(x) - pad, 0), max(int(y) - pad, 0)
        x1, y1 = min(int(x + w) + pad + 1, W), min(int(y + h) + pad + 1, H)
        X = XX[y0:y1, x0:x1]
        Y = YY[y0:y1, x0:x1]
        d = sdf_rrect(X, Y, x, y, w, h, r)
        gy, gx = np.gradient(d)
        nl = np.sqrt(gx * gx + gy * gy) + 1e-6
        nx, ny = gx / nl, gy / nl
        cov = np.clip(0.5 - d, 0, 1)[..., None]
        cx, cy = x + w / 2, y + h / 2
        e = min(edge, max(r * 0.95, 5.0))
        t = np.clip(1 + d / e, 0, 1) ** 2  # 0 deep inside, 1 at the boundary
        bx = cx + (X - cx) / mag
        by = cy + (Y - cy) / mag
        col = np.empty(X.shape + (3,), np.float32)
        for c, f in enumerate((1 + fringe, 1.0, 1 - fringe)):
            col[..., c] = sample(src[..., c], bx + nx * bend * f * t, by + ny * bend * f * t)
        col = col * (1 - ta) + np.array(tint, np.float32) * ta
        col *= 1 - dark
        sh = sheen * np.clip(1 - (Y - y) / (h * 0.55), 0, 1) ** 2
        col += sh[..., None]
        lx, ly = light
        ll = math.hypot(lx, ly)
        ndl = (nx * lx + ny * ly) / ll
        lit = np.maximum(ndl, 0) ** 1.4
        glint = np.maximum(-ndl, 0) ** 2
        band = np.exp(-((d + 1.0) / 1.25) ** 2)
        wide = np.exp(-((d + 5.0) / 4.5) ** 2)
        col += (band * (hair + rim * lit + rim * 0.45 * glint) + wide * 0.07 * lit)[..., None]
        col += (t * 0.04 * glow)[..., None]
        col = np.clip(col, 0, 1)
        reg = self.a[y0:y1, x0:x1]
        reg[:] = reg * (1 - cov) + col * cov
        if focus is not None:
            fc = np.array(focus, np.float32)
            outside = np.maximum(d, 0)
            fade = np.clip(1 - outside / (pad - 4), 0, 1)
            halo = (np.exp(-outside / 9.0) * 0.30 * fade * (d > 0))[..., None]
            ring = np.clip(1.2 - np.abs(d - 4.5) / 1.6, 0, 1)[..., None]
            reg[:] = reg * (1 - halo) + fc * halo
            reg[:] = reg * (1 - ring) + fc * ring


# -------------------------------------------------------------------- pieces

def icon(d, name, cx, cy, s=22, col=TXT1, w=2):
    """Simple line icons on an s x s box centred at (cx, cy)."""
    h = s / 2
    L = lambda pts: d.line(pts, fill=col, width=w, joint="curve")
    if name == "games":
        d.rounded_rectangle([cx - h, cy - h * 0.55, cx + h, cy + h * 0.6], h * 0.5, outline=col, width=w)
        L([(cx - h * 0.55, cy), (cx - h * 0.15, cy)])
        L([(cx - h * 0.35, cy - h * 0.2), (cx - h * 0.35, cy + h * 0.2)])
        d.ellipse([cx + h * 0.3 - 2, cy - h * 0.15 - 2, cx + h * 0.3 + 2, cy - h * 0.15 + 2], fill=col)
        d.ellipse([cx + h * 0.55 - 2, cy + h * 0.12 - 2, cx + h * 0.55 + 2, cy + h * 0.12 + 2], fill=col)
    elif name == "packages":
        p = [(cx, cy - h), (cx + h * 0.9, cy - h * 0.5), (cx + h * 0.9, cy + h * 0.5), (cx, cy + h),
             (cx - h * 0.9, cy + h * 0.5), (cx - h * 0.9, cy - h * 0.5), (cx, cy - h)]
        L(p)
        L([(cx - h * 0.9, cy - h * 0.5), (cx, cy), (cx + h * 0.9, cy - h * 0.5)])
        L([(cx, cy), (cx, cy + h)])
    elif name == "apps":
        for ox in (-1, 1):
            for oy in (-1, 1):
                q = h * 0.48
                d.rounded_rectangle([cx + ox * h * 0.5 - q * 0.8, cy + oy * h * 0.5 - q * 0.8,
                                     cx + ox * h * 0.5 + q * 0.8, cy + oy * h * 0.5 + q * 0.8], 3, outline=col, width=w)
    elif name == "star":
        pts = []
        for i in range(11):
            a = -math.pi / 2 + i * math.pi / 5
            rr = h if i % 2 == 0 else h * 0.45
            pts.append((cx + rr * math.cos(a), cy + rr * math.sin(a)))
        L(pts)
    elif name == "browser":
        d.ellipse([cx - h, cy - h, cx + h, cy + h], outline=col, width=w)
        d.ellipse([cx - h * 0.42, cy - h, cx + h * 0.42, cy + h], outline=col, width=w)
        L([(cx - h, cy), (cx + h, cy)])
    elif name == "chat":
        d.rounded_rectangle([cx - h, cy - h * 0.8, cx + h, cy + h * 0.5], 5, outline=col, width=w)
        L([(cx - h * 0.4, cy + h * 0.5), (cx - h * 0.6, cy + h), (cx, cy + h * 0.5)])
        for ox in (-0.45, 0, 0.45):
            d.ellipse([cx + ox * h - 1.6, cy - h * 0.15 - 1.6, cx + ox * h + 1.6, cy - h * 0.15 + 1.6], fill=col)
    elif name == "tools":
        L([(cx - h * 0.8, cy + h * 0.8), (cx + h * 0.2, cy - h * 0.2)])
        d.arc([cx - h * 0.1, cy - h, cx + h, cy + h * 0.1], 100, 350, fill=col, width=w)
    elif name == "files":
        L([(cx - h, cy - h * 0.7), (cx - h * 0.2, cy - h * 0.7), (cx, cy - h * 0.4), (cx + h, cy - h * 0.4),
           (cx + h, cy + h * 0.75), (cx - h, cy + h * 0.75), (cx - h, cy - h * 0.7)])
    elif name == "settings":
        for i in range(8):
            a = i * math.pi / 4
            L([(cx + h * 0.62 * math.cos(a), cy + h * 0.62 * math.sin(a)),
               (cx + h * math.cos(a), cy + h * math.sin(a))])
        d.ellipse([cx - h * 0.66, cy - h * 0.66, cx + h * 0.66, cy + h * 0.66], outline=col, width=w)
        d.ellipse([cx - h * 0.25, cy - h * 0.25, cx + h * 0.25, cy + h * 0.25], outline=col, width=w)
    elif name == "media":
        d.rounded_rectangle([cx - h, cy - h * 0.7, cx + h, cy + h * 0.7], 4, outline=col, width=w)
        d.polygon([(cx - h * 0.25, cy - h * 0.35), (cx + h * 0.4, cy), (cx - h * 0.25, cy + h * 0.35)], fill=col)
    elif name == "home":
        L([(cx - h, cy - h * 0.05), (cx, cy - h), (cx + h, cy - h * 0.05)])
        L([(cx - h * 0.7, cy - h * 0.3), (cx - h * 0.7, cy + h * 0.85), (cx + h * 0.7, cy + h * 0.85),
           (cx + h * 0.7, cy - h * 0.3)])
    elif name == "store":
        L([(cx - h, cy - h * 0.6), (cx - h * 0.6, cy - h * 0.6), (cx - h * 0.3, cy + h * 0.5), (cx + h * 0.8, cy + h * 0.5),
           (cx + h, cy - h * 0.3), (cx - h * 0.45, cy - h * 0.3)])
        for ox in (-0.15, 0.6):
            d.ellipse([cx + ox * h - 2.5, cy + h * 0.85 - 2.5, cx + ox * h + 2.5, cy + h * 0.85 + 2.5], fill=col)
    elif name == "search":
        d.ellipse([cx - h, cy - h, cx + h * 0.4, cy + h * 0.4], outline=col, width=w)
        L([(cx + h * 0.25, cy + h * 0.25), (cx + h, cy + h)])
    elif name == "wifi":
        for k in (1.0, 0.68, 0.36):
            d.arc([cx - h * k, cy - h * k + h * 0.5, cx + h * k, cy + h * k + h * 0.5], 225, 315, fill=col, width=w + 1)
        d.ellipse([cx - 2.5, cy + h * 0.5 - 2.5, cx + 2.5, cy + h * 0.5 + 2.5], fill=col)
    elif name == "bluetooth":
        L([(cx - h * 0.5, cy - h * 0.45), (cx + h * 0.45, cy + h * 0.4), (cx, cy + h * 0.85), (cx, cy - h * 0.85),
           (cx + h * 0.45, cy - h * 0.4), (cx - h * 0.5, cy + h * 0.45)])
    elif name == "moon":
        d.ellipse([cx - h * 0.8, cy - h * 0.8, cx + h * 0.8, cy + h * 0.8], fill=col)
        d.ellipse([cx - h * 0.35, cy - h * 1.05, cx + h * 1.15, cy + h * 0.45], fill=(0, 0, 0, 0))
    elif name == "plane":
        L([(cx - h, cy + h * 0.1), (cx + h, cy + h * 0.1)])
        L([(cx - h * 0.1, cy - h * 0.9), (cx + h * 0.35, cy + h * 0.1), (cx - h * 0.1, cy + h * 0.95)])
        L([(cx - h * 0.85, cy - h * 0.25), (cx - h * 0.85, cy + h * 0.45)])
    elif name == "cast":
        d.rounded_rectangle([cx - h, cy - h * 0.75, cx + h, cy + h * 0.6], 3, outline=col, width=w)
        d.arc([cx - h * 0.9, cy + h * 0.05, cx - h * 0.1, cy + h * 0.85], 270, 360, fill=col, width=w)
    elif name == "rotate":
        d.arc([cx - h * 0.85, cy - h * 0.85, cx + h * 0.85, cy + h * 0.85], 30, 320, fill=col, width=w)
        d.polygon([(cx + h * 0.95, cy - h * 0.75), (cx + h * 0.9, cy - h * 0.1), (cx + h * 0.35, cy - h * 0.45)], fill=col)
    elif name == "sun":
        d.ellipse([cx - h * 0.45, cy - h * 0.45, cx + h * 0.45, cy + h * 0.45], outline=col, width=w)
        for i in range(8):
            a = i * math.pi / 4
            L([(cx + h * 0.7 * math.cos(a), cy + h * 0.7 * math.sin(a)), (cx + h * math.cos(a), cy + h * math.sin(a))])
    elif name == "speaker":
        d.polygon([(cx - h, cy - h * 0.3), (cx - h * 0.45, cy - h * 0.3), (cx + h * 0.1, cy - h * 0.85),
                   (cx + h * 0.1, cy + h * 0.85), (cx - h * 0.45, cy + h * 0.3), (cx - h, cy + h * 0.3)], fill=col)
        d.arc([cx - h * 0.3, cy - h * 0.6, cx + h * 0.75, cy + h * 0.6], 300, 60, fill=col, width=w)
    elif name == "play":
        d.polygon([(cx - h * 0.45, cy - h * 0.7), (cx + h * 0.7, cy), (cx - h * 0.45, cy + h * 0.7)], fill=col)
    elif name == "pause":
        d.rectangle([cx - h * 0.55, cy - h * 0.7, cx - h * 0.15, cy + h * 0.7], fill=col)
        d.rectangle([cx + h * 0.15, cy - h * 0.7, cx + h * 0.55, cy + h * 0.7], fill=col)
    elif name == "save":
        d.rounded_rectangle([cx - h * 0.85, cy - h * 0.85, cx + h * 0.85, cy + h * 0.85], 3, outline=col, width=w)
        d.rectangle([cx - h * 0.45, cy - h * 0.85, cx + h * 0.35, cy - h * 0.3], outline=col, width=w)
    elif name == "load":
        L([(cx, cy - h * 0.9), (cx, cy + h * 0.3)])
        L([(cx - h * 0.45, cy - h * 0.15), (cx, cy + h * 0.3), (cx + h * 0.45, cy - h * 0.15)])
        L([(cx - h * 0.85, cy + h * 0.45), (cx - h * 0.85, cy + h * 0.85), (cx + h * 0.85, cy + h * 0.85),
           (cx + h * 0.85, cy + h * 0.45)])
    elif name == "spark":
        for (a, b, k) in ((0, 0, 1.0), (0.55, -0.55, 0.45)):
            px, py, rr = cx + a * h, cy + b * h, h * 0.62 * k
            d.polygon([(px, py - rr), (px + rr * 0.28, py - rr * 0.28), (px + rr, py), (px + rr * 0.28, py + rr * 0.28),
                       (px, py + rr), (px - rr * 0.28, py + rr * 0.28), (px - rr, py), (px - rr * 0.28, py - rr * 0.28)], fill=col)
    elif name == "power":
        d.arc([cx - h * 0.85, cy - h * 0.75, cx + h * 0.85, cy + h * 0.95], 300, 240, fill=col, width=w)
        L([(cx, cy - h * 0.95), (cx, cy + h * 0.1)])
    elif name == "bell":
        d.chord([cx - h * 0.75, cy - h * 0.85, cx + h * 0.75, cy + h * 0.9], 180, 360, fill=col)
        d.rectangle([cx - h * 0.75, cy, cx + h * 0.75, cy + h * 0.45], fill=col)
        d.rectangle([cx - h * 0.95, cy + h * 0.4, cx + h * 0.95, cy + h * 0.6], fill=col)
        d.ellipse([cx - h * 0.25, cy + h * 0.6, cx + h * 0.25, cy + h * 1.0], fill=col)


def signal(d, x, y, col=(102, 214, 140)):
    for i in range(4):
        hh = 4 + i * 3
        d.rounded_rectangle([x + i * 6, y + 14 - hh, x + i * 6 + 3, y + 14], 1, fill=col)


def battery(d, x, y, pct=0.82, col=TXT1):
    d.rounded_rectangle([x, y, x + 30, y + 15], 4, outline=col, width=2)
    d.rounded_rectangle([x + 31, y + 4, x + 34, y + 11], 1, fill=col)
    d.rounded_rectangle([x + 3, y + 3, x + 3 + 24 * pct, y + 12], 2, fill=(102, 214, 140))


def hint(d, x, y, key, label, col=TXT1, keycol=TXT2):
    """A controller hint chip: outlined key then its label. Returns the end x."""
    fk = font(15, True)
    fl = font(16)
    kw = d.textlength(key, font=fk)
    if len(key) == 1:
        d.ellipse([x, y, x + 24, y + 24], outline=keycol, width=2)
        d.text((x + 12, y + 12), key, font=fk, fill=col, anchor="mm")
        x += 24
    else:
        d.rounded_rectangle([x, y, x + kw + 16, y + 24], 12, outline=keycol, width=2)
        d.text((x + 8 + kw / 2, y + 12), key, font=fk, fill=col, anchor="mm")
        x += kw + 16
    x += 8
    d.text((x, y + 12), label, font=fl, fill=col, anchor="lm")
    return x + d.textlength(label, font=fl) + 22


def hints_right(d, right, y, items, **kw):
    """Lay hints out so the last one ends at `right`."""
    probe = Image.new("RGBA", (10, 10))
    pd = ImageDraw.Draw(probe)
    total = 0
    for k, l in items:
        total = hint(pd, total, 0, k, l) if total else hint(pd, 0, 0, k, l)
    x = right - (total - 22)
    for k, l in items:
        x = hint(d, x, y, k, l, **kw)


def status_bar(d, right=1248, y=18, col=TXT1):
    signal(d, right - 160, y + 1)
    d.text((right - 122, y + 8), "9:41", font=font(19), fill=col, anchor="lm")
    battery(d, right - 66, y + 1)


# ---------------------------------------------------------------------- art

def art_synthwave(w, h):
    """Original cover art: a neon sunset over a grid road."""
    im = Image.new("RGB", (w, h))
    a = np.zeros((h, w, 3), np.float32)
    t = np.linspace(0, 1, h)[:, None, None]
    top, mid = np.array([0.08, 0.03, 0.22]), np.array([0.85, 0.22, 0.48])
    a[:] = top * (1 - t) + mid * t
    im = to_pil(a)
    d = ImageDraw.Draw(im)
    horizon = int(h * 0.62)
    sx, sy, sr = w * 0.62, horizon - 8, h * 0.30
    for i in range(int(sr * 2)):
        yy = sy - sr + i
        if yy > horizon:
            break
        k = i / (sr * 2)
        if yy > sy - sr * 0.1 and (int(yy) // 6) % 3 == 0:
            continue
        half = math.sqrt(max(sr * sr - (yy - sy) ** 2, 0))
        col = (255, int(220 - 130 * k), int(90 + 40 * k))
        d.line([(sx - half, yy), (sx + half, yy)], fill=col)
    mtn = [(0, horizon), (w * 0.12, horizon - 52), (w * 0.22, horizon - 18), (w * 0.33, horizon - 70),
           (w * 0.45, horizon - 10), (w * 0.5, horizon)]
    d.polygon(mtn, fill=(40, 12, 70))
    d.polygon([(w * 0.72, horizon), (w * 0.85, horizon - 40), (w, horizon - 20), (w, horizon)], fill=(40, 12, 70))
    d.rectangle([0, horizon, w, h], fill=(18, 6, 40))
    vx = w * 0.62
    for i in range(-14, 15):
        d.line([(vx, horizon), (vx + i * w * 0.12, h)], fill=(255, 70, 200), width=2)
    yv = 0.0
    k = 3.0
    while horizon + yv < h:
        d.line([(0, horizon + yv), (w, horizon + yv)], fill=(255, 70, 200), width=2)
        yv += k
        k *= 1.35
    return im


def art_tile(w, h, c1, c2, shape, seed=0):
    """Original square cover art: a two-tone gradient and one bold shape."""
    t = (np.linspace(0, 1, h)[:, None] * 0.6 + np.linspace(0, 1, w)[None, :] * 0.4)[..., None]
    a = np.array(c1, np.float32) * (1 - t) + np.array(c2, np.float32) * t
    im = to_pil(a)
    d = ImageDraw.Draw(im)
    rng = np.random.default_rng(seed)
    white = (255, 255, 255)
    if shape == "rings":
        for i in range(5):
            r = 30 + i * 26
            d.ellipse([w / 2 - r, h * 0.55 - r, w / 2 + r, h * 0.55 + r], outline=white + (0,), width=6)
    elif shape == "peaks":
        d.polygon([(0, h), (w * 0.35, h * 0.35), (w * 0.6, h * 0.7), (w * 0.8, h * 0.45), (w, h * 0.8), (w, h)],
                  fill=tuple(int(v * 255 * 0.35) for v in c1))
        d.ellipse([w * 0.68, h * 0.12, w * 0.86, h * 0.30], fill=(255, 240, 200))
    elif shape == "blocks":
        for _ in range(9):
            x = rng.integers(0, w - 40)
            y = rng.integers(int(h * 0.35), h - 40)
            s = rng.integers(26, 60)
            d.rectangle([x, y, x + s, y + s], fill=tuple(int(v) for v in rng.integers(150, 255, 3)))
    elif shape == "orbit":
        d.ellipse([w * 0.3, h * 0.3, w * 0.7, h * 0.7], fill=(255, 214, 120))
        d.ellipse([w * 0.08, h * 0.42, w * 0.92, h * 0.58], outline=white, width=5)
    elif shape == "wave":
        for k in range(6):
            pts = [(x, h * (0.45 + 0.07 * k) + 18 * math.sin(x / 34 + k)) for x in range(0, w + 8, 8)]
            d.line(pts, fill=white, width=5)
    elif shape == "kart":
        d.rounded_rectangle([w * 0.2, h * 0.55, w * 0.8, h * 0.72], 18, fill=(250, 250, 250))
        for ox in (0.3, 0.7):
            d.ellipse([w * ox - 22, h * 0.66, w * ox + 22, h * 0.66 + 44], fill=(30, 30, 40))
        d.ellipse([w * 0.42, h * 0.36, w * 0.58, h * 0.52], fill=(255, 200, 80))
    return im


def art_pixel_game(w, h, px=8):
    """An original pixel-art platformer frame, drawn at 1/px scale then scaled up."""
    sw, sh = w // px, h // px
    im = Image.new("RGB", (sw, sh))
    d = ImageDraw.Draw(im)
    for y in range(sh):
        k = y / sh
        d.line([(0, y), (sw, y)], fill=(int(92 + 80 * k), int(148 + 70 * k), int(252 - 20 * k)))
    for (cx, cy) in ((22, 12), (70, 8), (118, 16), (178, 26)):
        d.rectangle([cx, cy, cx + 14, cy + 4], fill=(255, 255, 255))
        d.rectangle([cx + 3, cy - 3, cx + 10, cy], fill=(255, 255, 255))
    for (hx, hh, hw) in ((0, 22, 40), (34, 30, 46), (96, 18, 50), (130, 28, 40)):
        d.ellipse([hx, sh - 14 - hh, hx + hw, sh - 14 + hh], fill=(64, 168, 82))
    d.rectangle([0, sh - 14, sw, sh], fill=(160, 98, 44))
    d.rectangle([0, sh - 14, sw, sh - 11], fill=(92, 200, 90))
    for x in range(0, sw, 8):
        d.rectangle([x, sh - 9, x + 3, sh - 7], fill=(120, 70, 30))
    for x in range(40, 100, 6):
        d.rectangle([x, sh - 40, x + 5, sh - 35], fill=(220, 120, 40), outline=(120, 60, 20))
    d.rectangle([64, sh - 40, 69, sh - 35], fill=(250, 200, 40), outline=(150, 100, 20))
    for x in (46, 56, 76, 86):
        d.ellipse([x, sh - 50, x + 3, sh - 46], fill=(255, 220, 60))
    # hero
    hx, hy = 26, sh - 24
    d.rectangle([hx, hy, hx + 6, hy + 10], fill=(230, 60, 60))
    d.rectangle([hx + 1, hy - 4, hx + 5, hy], fill=(250, 210, 170))
    d.rectangle([hx, hy - 5, hx + 6, hy - 4], fill=(120, 40, 30))
    d.rectangle([hx + 1, hy + 6, hx + 5, hy + 10], fill=(40, 70, 180))
    # critter
    d.ellipse([120, sh - 21, 129, sh - 14], fill=(150, 80, 40))
    d.rectangle([122, sh - 19, 123, sh - 18], fill=(255, 255, 255))
    d.rectangle([126, sh - 19, 127, sh - 18], fill=(255, 255, 255))
    return im.resize((w, h), Image.NEAREST)


# ------------------------------------------------------------------- scenes

def home_backdrop():
    return blobs((0.035, 0.055, 0.12), (0.02, 0.03, 0.07), [
        (150, 120, 260, (0.10, 0.55, 0.85), 0.55),
        (1120, 640, 300, (0.50, 0.25, 0.85), 0.55),
        (760, 300, 220, (0.15, 0.30, 0.80), 0.35),
        (420, 690, 200, (0.05, 0.60, 0.65), 0.35),
        (1080, 120, 160, (0.85, 0.30, 0.65), 0.22),
    ])


def draw_home(c, footer_hints=True):
    """Concept 1: the Home dashboard on realistic liquid glass."""
    tiles = []
    cont = (16, 150, 486, 330)
    media = (514, 154, 716, 150)
    mid = [(514 + i * 246, 322, 230, 150) for i in range(3)]
    low = [(16 + i * 205.6, 490, 190, 150) for i in range(6)]
    search = (16, 104, 446, 36)
    footer = (16, 660, 1248, 48)
    src = c.snapshot(18)
    for (x, y, w, h) in [media] + mid + low:
        c.shadow(x, y, w, h, 20, dy=10, radius=18, alpha=0.40)
    c.shadow(*cont, 22, dy=12, radius=22, alpha=0.5)
    # continue card: original artwork with a gradient for text
    art = art_synthwave(cont[2], cont[3])
    ga = np.asarray(art, np.float32) / 255
    g = np.linspace(0, 1, cont[3])[:, None, None]
    ga = ga * (1 - 0.62 * np.clip((g - 0.35) / 0.65, 0, 1))
    c.paste(to_pil(ga), cont[0], cont[1], 20)
    c.glass(src, *search, 18, mag=1.03, bend=6, ta=0.06, sheen=0.08)
    c.glass(src, *media, 20, mag=1.06, bend=10, ta=0.07, tint=(0.75, 0.85, 1.0))
    for (x, y, w, h) in mid + low:
        c.glass(src, x, y, w, h, 20, mag=1.06, bend=10, ta=0.06)
    c.glass(src, *footer, 24, mag=1.02, bend=6, ta=0.05, sheen=0.06, rim=0.5)
    # glass over the art: the Resume pill and the CONTINUE chip refract the sunset
    art_src = c.snapshot(10)
    c.glass(art_src, 34, 410, 130, 44, 22, mag=1.12, bend=10, ta=0.16, tint=ACCENT, sheen=0.16, rim=1.0)
    c.glass(art_src, 34, 170, 154, 28, 14, mag=1.05, bend=5, ta=0.10, dark=0.15, sheen=0.06)
    c.glass(art_src, 428, 170, 52, 52, 26, mag=1.25, bend=12, ta=0.10, sheen=0.18, rim=1.0)
    # focus ring around the continue card
    X = XX
    Y = YY
    d = sdf_rrect(X, Y, *cont, 20)
    ring = np.clip(1.2 - np.abs(d - 5) / 1.6, 0, 1)[..., None]
    halo = (np.exp(-np.maximum(d, 0) / 10.0) * 0.28 * (d > 0))[..., None]
    fc = np.array(ACCENT, np.float32)
    c.a = c.a * (1 - halo) + fc * halo
    c.a = c.a * (1 - ring) + fc * ring
    # the rest of the scene's glass chips (icon wells)
    src2 = c.snapshot(6)
    wells = [(media[0] + 22, media[1] + 48, 54, 54)]
    wells += [(x + 18, y + 18, 46, 46) for (x, y, w, h) in mid + low]
    for (x, y, w, h) in wells:
        c.glass(src2, x, y, w, h, 14, mag=1.15, bend=6, ta=0.10, sheen=0.14, rim=0.9)
    c.glass(src2, 1204, 12, 36, 36, 18, mag=1.2, bend=6, ta=0.08, sheen=0.14)

    def text(d, lay):
        d.text((18, 8), "JukaHub", font=font(28), fill=TXT1)
        status_bar(d, right=1190)
        icon(d, "bell", 1222, 29, 18, TXT1)
        d.text((16, 50), "Good morning", font=font(38), fill=TXT1)
        icon(d, "search", 38, 122, 16, TXT1)
        d.text((60, 122), "Search apps, settings, files and the web", font=font(17), fill=TXT2, anchor="lm")
        d.ellipse([430, 111, 452, 133], outline=TXT2, width=2)
        d.text((441, 122), "Y", font=font(13, True), fill=TXT1, anchor="mm")
        d.text((111, 184), "CONTINUE PLAYING", font=font(13, True), fill=(200, 238, 255), anchor="mm")
        icon(d, "play", 455, 196, 20, TXT1)
        d.text((34, 330), "Neon Drift", font=font(28, True), fill=TXT1)
        d.text((34, 368), "Arcade racer · Level 4 · 30m ago", font=font(16), fill=(220, 226, 236))
        d.ellipse([44, 420, 68, 444], fill=(255, 255, 255, 235))
        d.text((56, 432), "A", font=font(14, True), fill=(10, 40, 60), anchor="mm")
        d.text((78, 432), "Resume", font=font(17, True), fill=TXT1, anchor="lm")
        icon(d, "media", media[0] + 49, media[1] + 75, 24, TXT1)
        d.text((media[0] + 98, media[1] + 54), "Media", font=font(24), fill=TXT1)
        d.text((media[0] + 98, media[1] + 86), "Watch and listen", font=font(17), fill=TXT2)
        for i in range(34):
            hh = 10 + 46 * abs(math.sin(i * 0.55)) * (0.4 + 0.6 * math.sin(i / 34 * math.pi))
            bx = media[0] + 470 + i * 6.5
            d.rounded_rectangle([bx, media[1] + 75 - hh / 2, bx + 3, media[1] + 75 + hh / 2], 1,
                                fill=(140, 170, 235, 110))
        names = [("games", "Games", "Play your library"), ("packages", "Packages", "Install and update"),
                 ("apps", "Apps", "Portable and desktop")]
        for (x, y, w, h), (ic, n, s) in zip(mid, names):
            icon(d, ic, x + 41, y + 41, 20, TXT1)
            d.text((x + 20, y + 92), n, font=font(21), fill=TXT1)
            d.text((x + 20, y + 120), s, font=font(14), fill=TXT2)
        names = [("star", "Favorites", "Pinned for later"), ("browser", "Browser", "The open web"),
                 ("chat", "Chat", "Messages and servers"), ("tools", "Tools", "Network and system"),
                 ("files", "Files", "Browse storage"), ("settings", "Settings", "Device and design")]
        for (x, y, w, h), (ic, n, s) in zip(low, names):
            icon(d, ic, x + 41, y + 41, 20, TXT1)
            d.text((x + 20, y + 92), n, font=font(21), fill=TXT1)
            d.text((x + 20, y + 120), s, font=font(14), fill=TXT2)
        icon(d, "power", 44, 684, 16, TXT1)
        d.text((62, 684), "Exit", font=font(17), fill=TXT1, anchor="lm")
        if footer_hints:
            hints_right(d, 1240, 672, [("D-Pad", "Navigate"), ("A", "Open"), ("Y", "Search"),
                                       ("L2/R2", "Sections"), ("Start", "Menu")])
    c.layer(text)


def concept_home():
    c = Canvas(home_backdrop())
    draw_home(c)
    return c


def concept_dock():
    """Concept 2: carousel of covers over a floating liquid dock with a lens."""
    c = Canvas(blobs((0.03, 0.05, 0.11), (0.015, 0.02, 0.06), [
        (200, 200, 300, (0.10, 0.50, 0.90), 0.45),
        (1100, 560, 320, (0.55, 0.25, 0.90), 0.50),
        (640, 720, 260, (0.10, 0.65, 0.70), 0.30),
    ]))
    covers = [
        ((0.95, 0.42, 0.25), (0.55, 0.10, 0.45), "peaks", "Summit Line"),
        ((0.15, 0.55, 0.95), (0.05, 0.15, 0.45), "wave", "Tide Runner"),
        ((0.98, 0.75, 0.20), (0.90, 0.30, 0.20), "kart", "Pocket Kart"),
        ((0.30, 0.15, 0.55), (0.05, 0.05, 0.20), "orbit", "Far Orbit"),
        ((0.20, 0.75, 0.45), (0.05, 0.30, 0.30), "blocks", "Block Garden"),
        ((0.95, 0.30, 0.55), (0.35, 0.10, 0.55), "rings", "Echo Rings"),
    ]
    focus_i = 2
    size, big, gap = 180, 260, 26
    xs = []
    x = 640 - big / 2 - (size + gap) * focus_i
    for i in range(len(covers)):
        s = big if i == focus_i else size
        xs.append((x, s))
        x += s + gap
    base = 584
    for i, (x, s) in enumerate(xs):
        c.shadow(x, base - s, s, s, 24, dy=14, radius=22, alpha=0.55)
    for i, ((c1, c2, shp, _), (x, s)) in enumerate(zip(covers, xs)):
        art = art_tile(int(s), int(s), c1, c2, shp, seed=i)
        c.paste(art, int(x), int(base - s), 24)
    fx, fs = xs[focus_i]
    d = sdf_rrect(XX, YY, fx, base - fs, fs, fs, 24)
    ring = np.clip(1.2 - np.abs(d - 6) / 1.8, 0, 1)[..., None]
    halo = (np.exp(-np.maximum(d, 0) / 14.0) * 0.35 * (d > 0))[..., None]
    fc = np.array((1.0, 1.0, 1.0), np.float32)
    c.a = c.a * (1 - halo) + np.array(ACCENT, np.float32) * halo
    c.a = c.a * (1 - ring) + fc * ring
    # the dock floats over the bottom of the covers, so they bend through it
    dock = (250, 548, 780, 92)
    src = c.snapshot(14)
    c.shadow(*dock, 46, dy=14, radius=26, alpha=0.55)
    c.glass(src, *dock, 46, mag=1.04, bend=16, edge=26, ta=0.07, sheen=0.12, rim=1.0, fringe=0.18)
    items = ["home", "games", "media", "chat", "store", "search", "settings"]
    sel = 1
    step = 780 / len(items)
    lens_cx = 250 + step * (sel + 0.5)
    src2 = c.snapshot(0)  # the lens is clear: magnify without blur

    def icons(d, lay):
        for i, ic in enumerate(items):
            icon(d, ic, 250 + step * (i + 0.5), 594, 26, TXT1 if i == sel else TXT2, 2)
    c.layer(icons)
    src2 = c.snapshot(0)
    c.glass(src2, lens_cx - 54, 556, 108, 76, 38, mag=1.35, bend=14, edge=22, ta=0.10, tint=ACCENT,
            sheen=0.16, rim=1.1, fringe=0.25)
    src3 = c.snapshot(10)
    c.glass(src3, 1168, 14, 96, 34, 17, mag=1.05, bend=5, ta=0.07, sheen=0.1)

    def text(d, lay):
        d.text((32, 28), "JukaHub", font=font(30), fill=TXT1)
        status_bar(d, right=1150)
        icon(d, "bell", 1196, 31, 16, TXT1)
        d.ellipse([1222, 20, 1250, 42], fill=(255, 92, 92))
        d.text((1236, 31), "3", font=font(13, True), fill=(255, 255, 255), anchor="mm")
        name = covers[focus_i][3]
        d.text((640, 236), name, font=font(34, True), fill=TXT1, anchor="mm")
        d.text((640, 274), "Last played yesterday  ·  2 h 14 m", font=font(17), fill=TXT2, anchor="mm")
        d.text((640, 660), "Games", font=font(15, True), fill=(200, 238, 255), anchor="mm")
        hints_right(d, 1248, 684, [("L1/R1", "Dock"), ("A", "Play"), ("X", "Options")])
    c.layer(text)
    return c


def concept_quick_settings():
    """Concept 3: Quick Settings as a liquid sheet over Home."""
    c = Canvas(home_backdrop())
    draw_home(c, footer_hints=False)
    c.dim(0.30, (0.0, 0.01, 0.04))
    sheet = (660, 64, 588, 584)
    src = c.snapshot(26)
    c.shadow(*sheet, 34, dy=18, radius=34, alpha=0.6)
    c.glass(src, *sheet, 34, mag=1.03, bend=14, edge=24, ta=0.10, dark=0.10, sheen=0.10, rim=1.0, fringe=0.15)
    # round toggles
    toggles = [("wifi", "Wi-Fi", "HomeNet", True), ("bluetooth", "Bluetooth", "2 devices", True),
               ("plane", "Airplane", "Off", False), ("moon", "Focus", "Off", False),
               ("cast", "Share screen", "Ready", False), ("rotate", "Rumble", "On", True)]
    src2 = c.snapshot(8)
    pos = []
    for i, t in enumerate(toggles):
        col, row = i % 3, i // 3
        x = sheet[0] + 32 + col * 178
        y = sheet[1] + 84 + row * 96
        pos.append((x, y))
        on = t[3]
        c.glass(src2, x, y, 58, 58, 29, mag=1.12, bend=8, ta=0.55 if on else 0.08,
                tint=ACCENT if on else (1, 1, 1), sheen=0.16, rim=1.0,
                focus=(1, 1, 1) if i == 0 else None)
    # sliders: glass capsules with a liquid fill
    sl = [("sun", "Brightness", 0.72), ("speaker", "Volume", 0.48)]
    for j, (ic, name, v) in enumerate(sl):
        x, y, w, h = sheet[0] + 32, sheet[1] + 290 + j * 70, sheet[2] - 64, 50
        c.glass(src2, x, y, w, h, 25, mag=1.04, bend=8, ta=0.08, sheen=0.12, rim=0.9)
        fw = max(h, w * v)
        c.glass(src2, x, y, fw, h, 25, mag=1.0, bend=0, ta=0.85, tint=(0.93, 0.96, 1.0), sheen=0.08,
                rim=0.6, hair=0.1)
        c.glass(c.snapshot(0), x + fw - 46, y + 3, 44, 44, 22, mag=1.3, bend=8, ta=0.04, sheen=0.18, rim=1.1)
    # now playing card, then the power row as three small glass pills
    np_box = (sheet[0] + 32, sheet[1] + 438, sheet[2] - 64, 64)
    c.glass(src2, *np_box, 20, mag=1.05, bend=8, ta=0.08, sheen=0.10, rim=0.9)
    art = art_tile(44, 44, (0.95, 0.55, 0.30), (0.55, 0.20, 0.55), "peaks", seed=3)
    c.paste(art, np_box[0] + 10, np_box[1] + 10, 10)
    pw = (np_box[2] - 24) / 3
    pills = [(np_box[0] + i * (pw + 12), sheet[1] + 518, pw, 40) for i in range(3)]
    for (x, y, w, h) in pills:
        c.glass(src2, x, y, w, h, 20, mag=1.05, bend=6, ta=0.07, sheen=0.10, rim=0.8)

    def text(d, lay):
        d.text((sheet[0] + 32, sheet[1] + 30), "Quick Settings", font=font(26, True), fill=TXT1)
        battery(d, sheet[0] + sheet[2] - 120, sheet[1] + 38)
        d.text((sheet[0] + sheet[2] - 32, sheet[1] + 46), "82%", font=font(16), fill=TXT1, anchor="rm")
        for (x, y), (ic, n, s, on) in zip(pos, toggles):
            icon(d, ic, x + 29, y + 29, 22, (8, 30, 48) if on else TXT1, 2)
            d.text((x + 70, y + 18), n, font=font(16, True), fill=TXT1, anchor="lm")
            d.text((x + 70, y + 40), s, font=font(14), fill=TXT2, anchor="lm")
        for j, (ic, name, v) in enumerate(sl):
            x, y = sheet[0] + 32, sheet[1] + 290 + j * 70
            icon(d, ic, x + 26, y + 25, 20, (20, 34, 52), 2)
            d.text((x + 50, y + 25), name, font=font(16, True), fill=(20, 34, 52), anchor="lm")
        x, y, w, h = np_box
        d.text((x + 68, y + 21), "Lo-fi Lights", font=font(17, True), fill=TXT1, anchor="lm")
        d.text((x + 68, y + 44), "Background music", font=font(14), fill=TXT2, anchor="lm")
        icon(d, "pause", x + w - 30, y + 32, 18, TXT1)
        icon(d, "play", x + w - 70, y + 32, 16, TXT2)
        for (px, py, pwid, ph), (ic, lab) in zip(pills, [("power", "Power off"), ("moon", "Rest"),
                                                         ("rotate", "Restart")]):
            tw = d.textlength(lab, font=font(15))
            sx = px + pwid / 2 - (tw + 26) / 2
            icon(d, ic, sx + 8, py + 20, 15, TXT1)
            d.text((sx + 26, py + 20), lab, font=font(15), fill=TXT1, anchor="lm")
        hints_right(d, 1240, 672, [("D-Pad", "Move"), ("A", "Toggle"), ("B", "Close")])
    c.layer(text)
    return c


def concept_ingame():
    """Concept 4: the in-game menu as clear glass over a running game."""
    game = art_pixel_game(W, H, 6)
    c = Canvas(from_pil(game))
    menu = (60, 92, 400, 556)
    src = c.snapshot(0)  # over games the glass bends but does not blur
    c.shadow(*menu, 30, dy=14, radius=28, alpha=0.45)
    c.glass(src, *menu, 30, mag=1.08, bend=18, edge=30, ta=0.20, tint=(0.05, 0.08, 0.16), dark=0.38,
            sheen=0.12, rim=1.1, fringe=0.2)
    rows = [("play", "Resume"), ("save", "Save state"), ("load", "Load state"), ("spark", "Ask Juka"),
            ("settings", "Game options"), ("power", "Quit game")]
    src2 = c.snapshot(0)
    sel = 0
    c.glass(src2, menu[0] + 18, menu[1] + 96, menu[2] - 36, 56, 28, mag=1.10, bend=8, ta=0.28,
            tint=ACCENT, sheen=0.14, rim=1.0)
    toast = (900, 30, 340, 64)
    c.shadow(*toast, 32, dy=8, radius=18, alpha=0.35)
    c.glass(src2, *toast, 32, mag=1.1, bend=12, edge=22, ta=0.2, tint=(0.05, 0.08, 0.16), dark=0.35,
            sheen=0.12, rim=1.0)

    def text(d, lay):
        d.text((menu[0] + 30, menu[1] + 26), "Pixel Meadow", font=font(26, True), fill=TXT1)
        d.text((menu[0] + 30, menu[1] + 74), "Game Boy Advance · 47 min", font=font(15), fill=TXT2, anchor="lm")
        for i, (ic, n) in enumerate(rows):
            y = menu[1] + 124 + i * 62
            colr = (8, 30, 48) if i == sel else TXT1
            icon(d, ic, menu[0] + 52, y, 20, colr if i == sel else TXT1, 2)
            d.text((menu[0] + 82, y), n, font=font(19, i == sel), fill=TXT1, anchor="lm")
        icon(d, "save", toast[0] + 34, toast[1] + 32, 22, TXT1)
        d.text((toast[0] + 62, toast[1] + 22), "Saved to slot 2", font=font(17, True), fill=TXT1, anchor="lm")
        d.text((toast[0] + 62, toast[1] + 44), "Hold Select + L2 for this menu", font=font(13), fill=TXT2, anchor="lm")
        x, y = menu[0] + 30, menu[1] + menu[3] - 36
        x = hint(d, x, y - 4, "A", "Choose")
        hint(d, x, y - 4, "B", "Back to game")
    c.layer(text)
    return c


def concept_variants():
    """Concept 5: one tile in Clear, Tinted and Reduce Transparency, dark and light."""
    dark = blobs((0.035, 0.055, 0.12), (0.02, 0.03, 0.07), [
        (180, 180, 200, (0.10, 0.55, 0.85), 0.6), (640, 260, 180, (0.85, 0.30, 0.65), 0.4),
        (1100, 180, 220, (0.50, 0.25, 0.85), 0.6)])
    light = blobs((0.93, 0.94, 0.96), (0.86, 0.88, 0.92), [
        (180, 560, 200, (-0.05, 0.10, 0.25), 0.9), (640, 470, 180, (0.20, -0.10, 0.10), 0.6),
        (1100, 560, 220, (0.10, -0.05, 0.25), 0.8)])
    t = np.clip((YY - 340) / 40, 0, 1)[..., None]
    c = Canvas(dark * (1 - t) + light * t)
    # stripes behind the panes make the bending and blur easy to see
    def stripes(d, lay):
        for i in range(3):
            cx = 60 + i * 400
            for k in range(7):
                d.rounded_rectangle([cx + 40 + k * 44, 70, cx + 60 + k * 44, 320], 10,
                                    fill=(255, 255, 255, 34))
                d.rounded_rectangle([cx + 40 + k * 44, 400, cx + 60 + k * 44, 650], 10,
                                    fill=(20, 30, 60, 30))
    c.layer(stripes)
    src = c.snapshot(16)
    modes = ["Clear", "Tinted", "Reduce Transparency"]
    boxes = []
    for row, is_light in ((0, False), (1, True)):
        for i, m in enumerate(modes):
            x, y, w, h = 80 + i * 400, 110 + row * 330, 320, 170
            boxes.append((x, y, w, h, m, is_light))
            c.shadow(x, y, w, h, 24, dy=12, radius=20, alpha=0.35 if not is_light else 0.18)
            if m == "Clear":
                c.glass(src, x, y, w, h, 24, mag=1.06, bend=12, ta=0.06 if not is_light else 0.12,
                        tint=(1, 1, 1), sheen=0.12, rim=1.0, fringe=0.15)
            elif m == "Tinted":
                tint = (0.25, 0.55, 0.95) if not is_light else (0.55, 0.78, 1.0)
                c.glass(src, x, y, w, h, 24, mag=1.06, bend=12, ta=0.32, tint=tint, sheen=0.12, rim=1.0)
            else:
                fill = (0.12, 0.15, 0.22) if not is_light else (0.97, 0.97, 0.98)
                c.glass(src, x, y, w, h, 24, mag=1.0, bend=0, ta=1.0, tint=fill, sheen=0.0, rim=0.35,
                        hair=0.22 if not is_light else 0.0, fringe=0)
                if is_light:
                    dd = sdf_rrect(XX, YY, x, y, w, h, 24)
                    line = np.clip(1 - np.abs(dd + 0.5), 0, 1)[..., None] * 0.35
                    c.a = c.a * (1 - line) + np.array((0.55, 0.58, 0.65), np.float32) * line
    src2 = c.snapshot(6)
    for (x, y, w, h, m, is_light) in boxes:
        if m == "Reduce Transparency":
            continue
        c.glass(src2, x + 22, y + 24, 54, 54, 16, mag=1.15, bend=6, ta=0.10 if not is_light else 0.18,
                tint=(1, 1, 1), sheen=0.14, rim=0.9)

    def text(d, lay):
        for (x, y, w, h, m, is_light) in boxes:
            t1 = TXT1 if not is_light else (24, 30, 44)
            t2 = TXT2 if not is_light else (78, 88, 108)
            if m == "Reduce Transparency":
                d.rounded_rectangle([x + 22, y + 24, x + 76, y + 78], 16,
                                    fill=(40, 48, 64) if not is_light else (226, 230, 238))
            icon(d, "games", x + 49, y + 51, 24, t1)
            d.text((x + 24, y + 108), "Games", font=font(24), fill=t1)
            d.text((x + 24, y + 140), "Play your library", font=font(16), fill=t2)
            d.text((x + w / 2, y - 26), m, font=font(17, True), fill=t1, anchor="mm")
        d.text((20, 26), "Dark theme", font=font(15, True), fill=TXT3)
        d.text((20, 356), "Light theme", font=font(15, True), fill=(90, 98, 116))
    c.layer(text)
    return c


CONCEPTS = [
    ("glass-concept-1-home.png", concept_home),
    ("glass-concept-2-dock.png", concept_dock),
    ("glass-concept-3-quick-settings.png", concept_quick_settings),
    ("glass-concept-4-in-game.png", concept_ingame),
    ("glass-concept-5-variants.png", concept_variants),
]


def main():
    only = None
    if "--only" in sys.argv:
        only = int(sys.argv[sys.argv.index("--only") + 1])
    os.makedirs(OUT, exist_ok=True)
    for i, (name, fn) in enumerate(CONCEPTS, 1):
        if only and i != only:
            continue
        path = os.path.join(OUT, name)
        fn().pil().save(path, optimize=True)
        print("wrote", os.path.relpath(path, ROOT))


if __name__ == "__main__":
    main()
