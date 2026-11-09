#!/usr/bin/env python3
"""mklogo.py: render every JukaHub logo asset from one construction.

The logo is the Home screen's glass design reduced to an app icon: the navy
backdrop with its two blooms (cyan upper-left, violet lower-right, the blue and
lilac of the boot screen's liquid between them), a pane of refractive glass
whose rim catches both lights, and a white J set like the Inter wordmark (flat
top, round terminal). Everything is drawn from signed-distance fields, so each
size gets exact anti-aliasing instead of a downscale of one big bitmap.

Two drawings share the construction:

  logo   the full icon: tile, glass pane, lit rim, J. Used from 40 px up.
  mark   the tile and the J only, the J larger and pixel-snapped. Used at
         32 px and below, where a pane and a rim would only be noise.

Run from anywhere (Python 3.9+, numpy, Pillow):

    python3 branding/mklogo.py                     # write every asset
    python3 branding/mklogo.py --preview out.png   # contact sheet, nothing else

Files whose bytes did not change are left alone, so a run after editing one
constant shows exactly which assets it touched. (Another numpy or Pillow build
can round a pixel differently, which is why there is no "is it stale" check.)

It writes:

  branding/jukahub-logo.svg, jukahub-mark.svg   vector exports
  branding/jukahub-logo-1024.png                the full-size render
  player/JukaPlayer.png                         window icon (go:embed) and the
                                                release folder's TrimUI icon
  player/trimui/Apps/JukaHub/icon.png           SD-card package's TrimUI icon
  player/JukaHub.ico                            Windows icon, 16-256 px
  player/rsrc_windows_amd64.syso                the .ico as a COFF resource,
                                                laid out exactly as windres does
  favicon.png, favicon.svg, apple-touch-icon.png   the web builder
  docs/assets/jukahub-logo.png, jukahub-mark.svg   README and the docs site

build.bat still recompiles the .syso with windres on every Windows build; the
writer here only means the committed object never lags the .ico.
"""

import argparse
import io
import os
import struct
import sys

import numpy as np
from PIL import Image

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# ── Palette: the glass design's own tokens ────────────────────────────────
# mockups/home-16x9-and-4x3.html, player/themes.go ("hub"), player/design.go,
# player/themegallery.go and player/bootliquid.go.
NAVY_DEEP = "#070B14"     # --navy-a; the hub preset's Background is #070B12
NAVY_LIFT = "#0B1426"     # --navy-b
CYAN = "#22D3EE"          # --cyan: the backdrop's upper-left bloom
CYAN_LIGHT = "#55D6FF"    # hub Info / BorderFocus: the focus ring and glow
VIOLET = "#7C3AED"        # --violet, themeDefaultBloom2: the lower-right bloom
VIOLET_LIGHT = "#8B7CFF"  # ColorAccentSecondary
BLUE = "#2563EB"          # the boot liquid's third blob
LILAC = "#C084FC"         # the boot liquid's fourth blob
WHITE = "#FFFFFF"
ICE = "#DDF7FF"           # the J's foot: white with a breath of the accent
SHADOW = "#03050E"        # the lifted pane's shadow
J_SHADOW = "#000A1E"      # the J's short drop shadow

# ── Construction, in fractions of the canvas ──────────────────────────────
TILE_MARGIN = 0.055
TILE_CORNER = 0.25        # of the tile's side: Home's 12px badge on 48px
PANE_INSET = 0.165
PANE_CORNER = 0.23        # of the pane's side
RIM_WIDTH = 0.014         # the lit rim; Home's 3px ring on a ~210px tile
LIGHT = (0.55, 0.83)      # the light travels from the upper left; edges facing it shine

# The blooms: colour, centre, gaussian sigma, peak alpha. Drawn back to front.
BLOOMS = (
    (BLUE, 0.80, 0.14, 0.18, 0.50),
    (LILAC, 0.10, 0.92, 0.12, 0.22),
    (CYAN, 0.12, 0.15, 0.24, 0.88),
    (VIOLET, 0.90, 0.90, 0.25, 0.92),
)

# The J: stroke width, total height, hook radius (to the stroke's centre line),
# and a nudge right so its weight (the stem) sits on the optical centre.
J_LOGO = dict(w=0.118, h=0.47, r=0.147, bias=0.010)
J_MARK = dict(w=0.160, h=0.60, r=0.178, bias=0.010)

# ICO sizes: Explorer's small/medium views, the title bar and taskbar at 100 to
# 250% scaling, Alt-Tab, large icons and the 256 px jumbo tile. 40 px and up
# draw the full logo; the PNG-compressed entries start at 64 px.
ICO_SIZES = (16, 20, 24, 32, 40, 48, 64, 128, 256)
ICO_PNG_FROM = 64
MARK_UP_TO = 32
SNAP_UP_TO = 64  # sizes whose edges are snapped to whole pixels


def rgb(hexstr):
    h = hexstr.lstrip("#")
    return np.array([int(h[i:i + 2], 16) / 255.0 for i in (0, 2, 4)])


# ── Signed distance fields (negative inside) ──────────────────────────────

def sd_round_box(x, y, cx, cy, hw, hh, r):
    qx = np.abs(x - cx) - hw + r
    qy = np.abs(y - cy) - hh + r
    outside = np.hypot(np.maximum(qx, 0), np.maximum(qy, 0))
    return outside + np.minimum(np.maximum(qx, qy), 0) - r


def sd_arc(x, y, cx, cy, radius, a0, a1):
    """Distance to an arc's centre line from angle a0 to a1 (screen angles:
    0 points right, pi/2 points down), with round ends."""
    px, py = x - cx, y - cy
    t = (np.arctan2(py, px) - a0) % (2 * np.pi)
    ring = np.abs(np.hypot(px, py) - radius)
    e0 = np.hypot(px - radius * np.cos(a0), py - radius * np.sin(a0))
    e1 = np.hypot(px - radius * np.cos(a1), py - radius * np.sin(a1))
    return np.where(t <= a1 - a0, ring, np.minimum(e0, e1))


def coverage(d, px):
    """Pixel coverage of the inside of d, with px the size of one pixel."""
    return np.clip(0.5 - d / px, 0.0, 1.0)


def normals(d):
    gy, gx = np.gradient(d)
    n = np.hypot(gx, gy) + 1e-12
    return gx / n, gy / n


def smooth01(t):
    t = np.clip(t, 0.0, 1.0)
    return t * t * (3 - 2 * t)


class Canvas:
    """A premultiplied float RGBA buffer that layers are composited onto."""

    def __init__(self, size):
        self.size = size
        idx = (np.arange(size) + 0.5) / size
        self.u, self.v = np.meshgrid(idx, idx)
        self.px = 1.0 / size
        self.buf = np.zeros((size, size, 4))

    def over(self, color, alpha):
        a = np.clip(alpha, 0.0, 1.0)[..., None]
        src = np.broadcast_to(color, self.buf[..., :3].shape) * a
        self.buf[..., :3] = src + self.buf[..., :3] * (1 - a)
        self.buf[..., 3:] = a + self.buf[..., 3:] * (1 - a)

    def image(self):
        a = self.buf[..., 3:]
        color = np.where(a > 1e-6, self.buf[..., :3] / np.maximum(a, 1e-6), 0)
        out = np.concatenate([color, a], axis=-1)
        return Image.fromarray((np.clip(out, 0, 1) * 255 + 0.5).astype(np.uint8), "RGBA")


def backdrop(u, v, blur=0.0, saturate=1.0):
    """Home's backdrop: the navy diagonal and the blooms over it. blur widens
    every bloom (a gaussian blurred by a gaussian stays one) for the view
    through the glass, and saturate is that view's richer colour."""
    t = np.clip((u + v) / 2, 0, 1)[..., None]
    col = rgb(NAVY_DEEP) * (1 - t) + rgb(NAVY_LIFT) * t
    for color, cx, cy, sigma, alpha in BLOOMS:
        s2 = sigma * sigma + blur * blur
        a = min(1.0, alpha * sigma * sigma / s2 * (1.15 if blur else 1.0))
        g = (np.exp(-((u - cx) ** 2 + (v - cy) ** 2) / (2 * s2)) * a)[..., None]
        col = col * (1 - g) + rgb(color) * g
    if saturate != 1.0:
        luma = (col * np.array([0.2126, 0.7152, 0.0722])).sum(-1, keepdims=True)
        col = np.clip(luma + (col - luma) * saturate, 0, 1)
    return col


# ── The J ─────────────────────────────────────────────────────────────────

def j_geometry(spec, size=None):
    """The J's stem centre x, top y, hook centre y, hook radius and stroke
    width, in canvas fractions. With a pixel size, the stem's edges and its
    flat top land on whole pixels, so a small icon's one vertical is sharp."""
    w, h, r, bias = spec["w"], spec["h"], spec["r"], spec["bias"]
    x0 = -2 * r - w / 2  # the terminal's outer edge, relative to the stem centre
    x1 = w / 2
    sx = 0.5 - (x0 + x1) / 2 + bias
    top = 0.5 - h / 2
    if size:
        wp = max(2, round(w * size))
        right = round((sx + w / 2) * size)
        sx = (right - wp / 2) / size
        w = wp / size
        top = round(top * size) / size
    hook_cy = top + h - r - w / 2
    return sx, top, hook_cy, r, w


def sd_j(c, geo, dy=0.0):
    sx, top, hook_cy, r, w = geo
    top += dy
    hook_cy += dy
    stem = sd_round_box(c.u, c.v, sx, (top + hook_cy) / 2, w / 2, (hook_cy - top) / 2, 0.0)
    hook = sd_arc(c.u, c.v, sx - r, hook_cy, r, 0.0, np.pi) - w / 2
    return np.minimum(stem, hook)


def draw_j(c, geo, tile_alpha, glow=0.30, shadow=0.22):
    d = sd_j(c, geo)
    outside = d > 0
    c.over(rgb(CYAN_LIGHT), np.exp(-np.maximum(d, 0) / 0.04) * glow * tile_alpha * outside)
    if shadow:
        ds = sd_j(c, geo, dy=0.010)
        c.over(rgb(J_SHADOW), coverage(ds, c.px * 4) * shadow * outside)
    t = np.clip((c.v - 0.28) / 0.45, 0, 1)[..., None]
    c.over(rgb(WHITE) * (1 - t) + rgb(ICE) * t, coverage(d, c.px))


# ── The two drawings ──────────────────────────────────────────────────────

def tile_box(size=None, bleed=False):
    """The tile's margin, side and corner radius. Up to 64 px the margin is a
    whole number of pixels, so the tile's edge is one sharp step; at 20 px and
    below the tile fills the canvas, the way small Windows icons do. A bleed
    tile always fills it: the favicon, where a browser tab or the web
    builder's app bar adds its own room."""
    m = TILE_MARGIN
    if bleed or (size and size <= 20):
        m = 0.0
    elif size and size <= MARK_UP_TO:
        m = 1.0 / size
    elif size and size <= SNAP_UP_TO:
        m = round(m * size) / size
    side = 1 - 2 * m
    return m, side, TILE_CORNER * side


def draw_tile(c, opaque=False, bleed=False):
    m, side, corner = tile_box(None if opaque else c.size, bleed)
    if opaque:
        d_tile = np.full(c.u.shape, -1.0)
        tile_alpha = np.ones(c.u.shape)
    else:
        d_tile = sd_round_box(c.u, c.v, 0.5, 0.5, side / 2, side / 2, corner)
        tile_alpha = coverage(d_tile, c.px)
    c.over(backdrop(c.u, c.v), tile_alpha)
    if not opaque:
        # The tile's own hairline, lit from the upper left like Home's tiles.
        nx, ny = normals(d_tile)
        lit = np.clip(-(nx * LIGHT[0] + ny * LIGHT[1]), 0, 1)
        hair = np.clip(1 - np.abs(d_tile + 0.8 * c.px) / (0.9 * c.px), 0, 1)
        c.over(rgb(WHITE), hair * (0.05 + 0.30 * lit))
    return tile_alpha


def rim_colour(c, inset):
    t = smooth01(((c.u - inset) + (c.v - inset)) / (2 * (1 - 2 * inset)))[..., None]
    return rgb(CYAN_LIGHT) * (1 - t) + rgb(VIOLET_LIGHT) * t


def draw_pane(c, tile_alpha):
    p = PANE_INSET
    rw = max(RIM_WIDTH, 1.25 * c.px)
    if c.size <= SNAP_UP_TO:
        p = round(p * c.size) / c.size
        rw = max(1, round(RIM_WIDTH * c.size)) / c.size
    half = 0.5 - p
    corner = PANE_CORNER * (1 - 2 * p)
    d = sd_round_box(c.u, c.v, 0.5, 0.5, half, half, corner)
    outside = d > 0
    rim = rim_colour(c, p)
    # The light the rim throws on the tile, and the shadow of the lifted pane.
    c.over(rim, np.exp(-np.maximum(d, 0) / 0.05) * 0.25 * outside * tile_alpha)
    ds = sd_round_box(c.u, c.v, 0.5, 0.5 + 0.025, half, half, corner)
    c.over(rgb(SHADOW), np.exp(-np.maximum(ds, 0) / 0.05) * 0.28 * outside * tile_alpha)
    # Through the glass: the backdrop magnified like a lens, softened, richer.
    mag = 1.22
    seen = backdrop(0.5 + (c.u - 0.5) / mag, 0.5 + (c.v - 0.5) / mag - 0.015, blur=0.07, saturate=1.35)
    frost = (0.05 + 0.08 * np.clip(1 - (c.v - p) / (1 - 2 * p), 0, 1) ** 1.5)[..., None]
    seen = seen * (1 - frost) + rgb(WHITE) * frost
    sheen = (np.clip(1 - ((c.u - p) + (c.v - p)) / 0.55, 0, 1) ** 2 * 0.07)[..., None]
    seen = seen * (1 - sheen) + rgb(WHITE) * sheen
    inside = coverage(d, c.px)
    c.over(seen, inside)
    # The edge: a soft inner bevel on the lit side, the cyan-to-violet rim, and
    # a specular hairline where the light hits.
    nx, ny = normals(d)
    lit = np.clip(-(nx * LIGHT[0] + ny * LIGHT[1]), 0, 1)
    bevel = np.clip(1 + d / 0.03, 0, 1) * (d < 0)
    c.over(rgb(WHITE), bevel ** 2.5 * lit * 0.16 * inside)
    c.over(rim, coverage(np.abs(d + rw / 2) - rw / 2, c.px) * 0.9)
    hair = np.clip(1 - np.abs(d + 0.8 * c.px) / (0.9 * c.px), 0, 1)
    c.over(rgb(WHITE), hair * 0.5 * lit ** 2)


def render_logo(size, opaque=False):
    c = Canvas(size)
    tile_alpha = draw_tile(c, opaque)
    draw_pane(c, tile_alpha)
    draw_j(c, j_geometry(J_LOGO, size if size <= SNAP_UP_TO else None), tile_alpha)
    return c.image()


def render_mark(size, opaque=False, bleed=False):
    c = Canvas(size)
    tile_alpha = draw_tile(c, opaque, bleed)
    snap = size if size <= SNAP_UP_TO else None
    draw_j(c, j_geometry(J_MARK, snap), tile_alpha, glow=0.22, shadow=0.0 if size <= 24 else 0.18)
    return c.image()


def render_icon(size):
    """The right drawing for an icon of this size."""
    return render_mark(size) if size <= MARK_UP_TO else render_logo(size)


# ── SVG exports ───────────────────────────────────────────────────────────

def _f(x, digits=2):
    """A number for SVG: fixed point, trailing zeros dropped."""
    return ("%.*f" % (digits, x)).rstrip("0").rstrip(".") or "0"


def _gauss_stops(color, alpha):
    """Seven stops tracing a gaussian out to three sigma, the gradient's radius."""
    stops = []
    for k in range(7):
        a = alpha * np.exp(-((k / 2.0) ** 2) / 2) if k < 6 else 0.0
        stops.append('<stop offset="%s" stop-color="%s" stop-opacity="%s"/>' % (_f(k / 6), color, _f(a, 3)))
    return "".join(stops)


def _j_path(geo, n):
    sx, top, cy, r, w = (g * n for g in geo)
    ro, ri, rc = r + w / 2, r - w / 2, w / 2
    tx = sx - 2 * r  # the terminal's centre
    return (
        "M%s %sH%sV%s" % (_f(sx - w / 2), _f(top), _f(sx + w / 2), _f(cy))
        + "A%s %s 0 0 1 %s %s" % (_f(ro), _f(ro), _f(tx - w / 2), _f(cy))
        + "A%s %s 0 0 1 %s %s" % (_f(rc), _f(rc), _f(tx + w / 2), _f(cy))
        + "A%s %s 0 0 0 %s %s" % (_f(ri), _f(ri), _f(sx - w / 2), _f(cy))
        + "Z"
    )


def _backdrop_svg(n, ids, prefix, blur=0.0):
    defs, body = [], []
    base = ids + prefix + "base"
    defs.append('<linearGradient id="%s" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="%d" y2="%d">'
                '<stop offset="0" stop-color="%s"/><stop offset="1" stop-color="%s"/></linearGradient>'
                % (base, n, n, NAVY_DEEP, NAVY_LIFT))
    body.append('<rect width="%d" height="%d" fill="url(#%s)"/>' % (n, n, base))
    for i, (color, cx, cy, sigma, alpha) in enumerate(BLOOMS):
        s2 = sigma * sigma + blur * blur
        a = min(1.0, alpha * sigma * sigma / s2 * (1.15 if blur else 1.0))
        radius = 3 * np.sqrt(s2) * n
        gid = "%s%sbloom%d" % (ids, prefix, i)
        defs.append('<radialGradient id="%s" gradientUnits="userSpaceOnUse" cx="%s" cy="%s" r="%s">%s</radialGradient>'
                    % (gid, _f(cx * n), _f(cy * n), _f(radius), _gauss_stops(color, a)))
        body.append('<rect width="%d" height="%d" fill="url(#%s)"/>' % (n, n, gid))
    return defs, body


def _j_fill(n, gid):
    return ('<linearGradient id="%s" gradientUnits="userSpaceOnUse" x1="0" y1="%s" x2="0" y2="%s">'
            '<stop offset="0" stop-color="%s"/><stop offset="1" stop-color="%s"/></linearGradient>'
            % (gid, _f(0.28 * n), _f(0.73 * n), WHITE, ICE))


def svg_logo(n=1024):
    """The full logo as SVG. Every id carries the "jhl-" prefix so the logo and
    the mark can be inlined into one page without their gradients colliding.
    It is an export: the lens, rim and glow are matched by eye with SVG
    gradients and blurs, and the PNGs remain the reference rendering."""
    k = "jhl-"
    m, side, corner = tile_box()
    p = PANE_INSET
    ps, pc = (1 - 2 * p) * n, PANE_CORNER * (1 - 2 * p) * n
    rw = RIM_WIDTH * n
    jpath = _j_path(j_geometry(J_LOGO), n)
    defs, body = _backdrop_svg(n, k, "tile-")
    seen_defs, seen_body = _backdrop_svg(n, k, "seen-", blur=0.07)
    mag = 1.22
    out = ['<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">' % (n, n, n, n),
           "<title>JukaHub</title>", "<defs>"]
    out += defs + seen_defs
    out.append('<clipPath id="%stile"><rect x="%s" y="%s" width="%s" height="%s" rx="%s"/></clipPath>'
               % (k, _f(m * n), _f(m * n), _f(side * n), _f(side * n), _f(corner * n)))
    out.append('<clipPath id="%spane"><rect x="%s" y="%s" width="%s" height="%s" rx="%s"/></clipPath>'
               % (k, _f(p * n), _f(p * n), _f(ps), _f(ps), _f(pc)))
    out.append('<linearGradient id="%srim" gradientUnits="userSpaceOnUse" x1="%s" y1="%s" x2="%s" y2="%s">'
               '<stop offset="0" stop-color="%s"/><stop offset="0.25" stop-color="%s"/>'
               '<stop offset="0.75" stop-color="%s"/><stop offset="1" stop-color="%s"/></linearGradient>'
               % (k, _f(p * n), _f(p * n), _f((1 - p) * n), _f((1 - p) * n), CYAN_LIGHT, CYAN_LIGHT, VIOLET_LIGHT, VIOLET_LIGHT))
    frost_stops = "".join('<stop offset="%s" stop-color="#fff" stop-opacity="%s"/>'
                          % (_f(t / 4), _f(0.05 + 0.08 * (1 - t / 4) ** 1.5)) for t in range(5))
    out.append('<linearGradient id="%sfrost" x1="0" y1="0" x2="0" y2="1">%s</linearGradient>' % (k, frost_stops))
    reach = 0.55 * n / 2  # the sheen fades out along the diagonal over this x/y
    sheen_stops = "".join('<stop offset="%s" stop-color="#fff" stop-opacity="%s"/>'
                          % (_f(t / 4), _f(0.07 * (1 - t / 4) ** 2)) for t in range(5))
    out.append('<linearGradient id="%ssheen" gradientUnits="userSpaceOnUse" x1="%s" y1="%s" x2="%s" y2="%s">%s</linearGradient>'
               % (k, _f(p * n), _f(p * n), _f(p * n + reach), _f(p * n + reach), sheen_stops))
    out.append('<linearGradient id="%slit" gradientUnits="userSpaceOnUse" x1="%s" y1="%s" x2="%s" y2="%s">'
               '<stop offset="0" stop-color="#fff" stop-opacity="0.55"/><stop offset="0.5" stop-color="#fff" stop-opacity="0"/></linearGradient>'
               % (k, _f(p * n), _f(p * n), _f((1 - p) * n), _f((1 - p) * n)))
    out.append('<linearGradient id="%shair" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="%d" y2="%d">'
               '<stop offset="0" stop-color="#fff" stop-opacity="0.35"/><stop offset="0.5" stop-color="#fff" stop-opacity="0.06"/>'
               '<stop offset="1" stop-color="#fff" stop-opacity="0.05"/></linearGradient>' % (k, n, n))
    out.append(_j_fill(n, k + "j"))
    out.append('<filter id="%ssoft" x="-50%%" y="-50%%" width="200%%" height="200%%"><feGaussianBlur stdDeviation="%s"/></filter>' % (k, _f(0.035 * n)))
    out.append('<filter id="%sglow" x="-50%%" y="-50%%" width="200%%" height="200%%"><feGaussianBlur stdDeviation="%s"/></filter>' % (k, _f(0.03 * n)))
    out.append('<filter id="%stight" x="-20%%" y="-20%%" width="140%%" height="140%%"><feGaussianBlur stdDeviation="%s"/></filter>' % (k, _f(0.004 * n)))
    out.append('<filter id="%srich"><feColorMatrix type="saturate" values="1.35"/></filter>' % k)
    out.append("</defs>")
    pane_rect = 'x="%s" y="%s" width="%s" height="%s" rx="%s"' % (_f(p * n), _f(p * n), _f(ps), _f(ps), _f(pc))
    out.append('<g clip-path="url(#%stile)">' % k)
    out += body
    # The light the rim throws on the tile, then the lifted pane's shadow.
    out.append('<rect %s fill="none" stroke="url(#%srim)" stroke-width="%s" opacity="0.30" filter="url(#%ssoft)"/>' % (pane_rect, k, _f(0.05 * n), k))
    out.append('<rect %s fill="%s" opacity="0.30" filter="url(#%ssoft)" transform="translate(0 %s)"/>' % (pane_rect, SHADOW, k, _f(0.025 * n)))
    # The view through the glass: magnified, softened, richer; frost and sheen.
    out.append('<g clip-path="url(#%spane)"><g filter="url(#%srich)" transform="translate(%s %s) scale(%s) translate(%s %s)">'
               % (k, k, _f(n / 2), _f(n / 2 - 0.015 * n), _f(mag), _f(-n / 2), _f(-n / 2)))
    out += seen_body
    out.append("</g>")
    out.append('<rect %s fill="url(#%sfrost)"/>' % (pane_rect, k))
    out.append('<rect %s fill="url(#%ssheen)"/>' % (pane_rect, k))
    out.append('<rect %s fill="none" stroke="url(#%slit)" stroke-width="%s" opacity="0.5" filter="url(#%stight)"/>' % (pane_rect, k, _f(0.03 * n), k))
    out.append("</g>")
    out.append('<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="none" stroke="url(#%srim)" stroke-width="%s" opacity="0.9"/>'
               % (_f(p * n + rw / 2), _f(p * n + rw / 2), _f(ps - rw), _f(ps - rw), _f(pc - rw / 2), k, _f(rw)))
    # The J: its glow, a short drop shadow, the letter.
    out.append('<path d="%s" fill="%s" opacity="0.45" filter="url(#%sglow)"/>' % (jpath, CYAN_LIGHT, k))
    out.append('<path d="%s" fill="%s" opacity="0.22" filter="url(#%stight)" transform="translate(0 %s)"/>' % (jpath, J_SHADOW, k, _f(0.010 * n)))
    out.append('<path d="%s" fill="url(#%sj)"/>' % (jpath, k))
    out.append("</g>")
    out.append('<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="none" stroke="url(#%shair)" stroke-width="1"/>'
               % (_f(m * n + 0.5), _f(m * n + 0.5), _f(side * n - 1), _f(side * n - 1), _f(corner * n - 0.5), k))
    out.append("</svg>")
    return "\n".join(out) + "\n"


def svg_mark(n=64):
    """The mark (tile and J) as SVG, full bleed: what the favicon, the web
    builder's app bar and the docs site use. Its ids carry the "jhm-" prefix."""
    k = "jhm-"
    m, side, corner = tile_box(bleed=True)
    defs, body = _backdrop_svg(n, k, "tile-")
    out = ['<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">' % (n, n, n, n),
           "<title>JukaHub</title>", "<defs>"]
    out += defs
    out.append('<clipPath id="%stile"><rect x="%s" y="%s" width="%s" height="%s" rx="%s"/></clipPath>'
               % (k, _f(m * n), _f(m * n), _f(side * n), _f(side * n), _f(corner * n)))
    out.append(_j_fill(n, k + "j"))
    out.append("</defs>")
    out.append('<g clip-path="url(#%stile)">' % k)
    out += body
    out.append('<path d="%s" fill="url(#%sj)"/>' % (_j_path(j_geometry(J_MARK), n), k))
    out.append("</g>")
    out.append("</svg>")
    return "\n".join(out) + "\n"


# ── Windows: the .ico and its compiled resource ──────────────────────────

def ico_bytes(images):
    """A .ico of the given RGBA images: 32-bit DIBs (with the AND mask Windows
    still reads) below ICO_PNG_FROM, PNG-compressed entries from there up."""
    blobs = []
    for im in images:
        w, h = im.size
        if w >= ICO_PNG_FROM:
            buf = io.BytesIO()
            im.save(buf, "PNG", optimize=True)
            blobs.append((w, h, buf.getvalue()))
            continue
        px = np.array(im)
        bgra = px[::-1, :, [2, 1, 0, 3]].tobytes()  # bottom-up rows, BGRA
        row = ((w + 31) // 32) * 4
        mask = bytearray()
        for y in range(h - 1, -1, -1):
            bits = bytearray(row)
            for x in range(w):
                if px[y, x, 3] == 0:
                    bits[x >> 3] |= 0x80 >> (x & 7)
            mask += bits
        header = struct.pack("<IiiHHIIiiII", 40, w, h * 2, 1, 32, 0, len(bgra) + len(mask), 0, 0, 0, 0)
        blobs.append((w, h, header + bgra + bytes(mask)))
    out = bytearray(struct.pack("<HHH", 0, 1, len(blobs)))
    offset = 6 + 16 * len(blobs)
    for w, h, data in blobs:
        out += struct.pack("<BBBBHHII", w % 256, h % 256, 0, 0, 1, 32, len(data), offset)
        offset += len(data)
    for _, _, data in blobs:
        out += data
    return bytes(out)


def ico_entries(data):
    """(width, height, colours, planes, bpp, payload) for each image of a .ico."""
    _, kind, count = struct.unpack_from("<HHH", data, 0)
    if kind != 1:
        raise ValueError("not an icon file")
    entries = []
    for i in range(count):
        w, h, colours, _, planes, bpp, size, offset = struct.unpack_from("<BBBBHHII", data, 6 + 16 * i)
        entries.append((w, h, colours, planes, bpp, data[offset:offset + size]))
    return entries


def syso_bytes(ico):
    """rsrc_windows_amd64.syso for `1 ICON "JukaHub.ico"`: the COFF object
    `windres -O coff` writes for that script, byte for byte in layout. One
    .rsrc section holds the resource tree (RT_ICON 1..n and RT_GROUP_ICON 1,
    language 1033), the data entries and the 8-byte-aligned payloads; each data
    entry's offset carries an IMAGE_REL_AMD64_ADDR32NB relocation against the
    section symbol, which is how the Go linker turns it into an RVA."""
    entries = ico_entries(ico)
    n = len(entries)
    group = struct.pack("<HHH", 0, 1, n)
    for i, (w, h, colours, planes, bpp, payload) in enumerate(entries):
        group += struct.pack("<BBBBHHIH", w, h, colours, 0, planes, bpp, len(payload), i + 1)
    leaves = [(3, i + 1, payload) for i, (*_, payload) in enumerate(entries)] + [(14, 1, group)]

    def directory(count):
        return struct.pack("<IIHHHH", 0, 0, 0, 0, 0, count)

    root_size = 16 + 8 * 2
    icon_dir_size = 16 + 8 * n
    lang_size = 16 + 8
    off_icon_dir = root_size
    off_icon_langs = off_icon_dir + icon_dir_size
    off_group_dir = off_icon_langs + lang_size * n
    off_group_lang = off_group_dir + lang_size
    off_data_entries = off_group_lang + lang_size
    off_payload = off_data_entries + 16 * len(leaves)

    tree = bytearray()
    tree += directory(2)
    tree += struct.pack("<II", 3, 0x80000000 | off_icon_dir)
    tree += struct.pack("<II", 14, 0x80000000 | off_group_dir)
    tree += directory(n)
    for i in range(n):
        tree += struct.pack("<II", i + 1, 0x80000000 | (off_icon_langs + lang_size * i))
    for i in range(n):
        tree += directory(1) + struct.pack("<II", 1033, off_data_entries + 16 * i)
    tree += directory(1) + struct.pack("<II", 1, 0x80000000 | off_group_lang)
    tree += directory(1) + struct.pack("<II", 1033, off_data_entries + 16 * n)
    assert len(tree) == off_data_entries

    payloads = bytearray()
    data_entries = bytearray()
    relocs = []
    cursor = off_payload
    for k, (_, _, blob) in enumerate(leaves):
        relocs.append(off_data_entries + 16 * k)
        data_entries += struct.pack("<IIII", cursor, len(blob), 0, 0)
        payloads += blob
        cursor += len(blob)
        if k != len(leaves) - 1:
            pad = (-cursor) % 8
            payloads += b"\0" * pad
            cursor += pad
    section = bytes(tree + data_entries + payloads)

    raw_ptr = 20 + 40
    reloc_ptr = raw_ptr + len(section)
    sym_ptr = reloc_ptr + 10 * len(relocs)
    head = struct.pack("<HHIIIHH", 0x8664, 1, 0, sym_ptr, 1, 0, 0x0004)
    sec = struct.pack("<8sIIIIIIHHI", b".rsrc", 0, 0, len(section), raw_ptr, reloc_ptr, 0, len(relocs), 0, 0x40300040)
    rel = b"".join(struct.pack("<IIH", va, 0, 3) for va in relocs)
    sym = struct.pack("<8sIhHBB", b".rsrc", 0, 1, 0, 3, 0)
    return head + sec + section + rel + sym + struct.pack("<I", 4)


# ── Assets ────────────────────────────────────────────────────────────────

def png_bytes(im):
    buf = io.BytesIO()
    im.save(buf, "PNG", optimize=True)
    return buf.getvalue()


def build_assets():
    """Every committed file, as {path relative to the repo: bytes}."""
    logo1024 = render_logo(1024)
    app300 = png_bytes(render_logo(300))
    ico = ico_bytes([render_icon(s) for s in ICO_SIZES])
    files = {
        "branding/jukahub-logo-1024.png": png_bytes(logo1024),
        "branding/jukahub-logo.svg": svg_logo().encode(),
        "branding/jukahub-mark.svg": svg_mark().encode(),
        "player/JukaPlayer.png": app300,
        "player/trimui/Apps/JukaHub/icon.png": app300,
        "player/JukaHub.ico": ico,
        "player/rsrc_windows_amd64.syso": syso_bytes(ico),
        "favicon.png": png_bytes(render_mark(48, bleed=True)),
        "favicon.svg": svg_mark().encode(),
        "apple-touch-icon.png": png_bytes(render_logo(180, opaque=True).convert("RGB")),
        "docs/assets/jukahub-logo.png": png_bytes(render_logo(256)),
        "docs/assets/jukahub-mark.svg": svg_mark().encode(),
    }
    return files


def contact_sheet(path):
    sizes = (256, 128, 64, 48, 40, 32, 24, 20, 16)
    pad = 16
    width = sum(sizes) + pad * (len(sizes) + 1)
    rows = ((13, 17, 26), (236, 239, 244), (124, 130, 140))
    sheet = Image.new("RGB", (width, (256 + 2 * pad) * len(rows)), (0, 0, 0))
    for r, bg in enumerate(rows):
        x = pad
        y0 = r * (256 + 2 * pad)
        sheet.paste(bg, (0, y0, width, y0 + 256 + 2 * pad))
        for s in sizes:
            im = render_icon(s)
            sheet.paste(im, (x, y0 + pad), im)
            x += s + pad
    sheet.save(path)


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--preview", metavar="PNG", help="write a contact sheet of every icon size and exit")
    ap.add_argument("--root", default=ROOT, help="repository root (default: this script's parent)")
    args = ap.parse_args()
    if args.preview:
        contact_sheet(args.preview)
        print("preview: %s" % args.preview)
        return 0
    changed = 0
    for rel, data in build_assets().items():
        path = os.path.join(args.root, *rel.split("/"))
        if os.path.exists(path):
            with open(path, "rb") as fh:
                if fh.read() == data:
                    continue
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as fh:
            fh.write(data)
        changed += 1
        print("wrote %-40s %7d bytes" % (rel, len(data)))
    if not changed:
        print("all logo assets are up to date")
    return 0


if __name__ == "__main__":
    sys.exit(main())
