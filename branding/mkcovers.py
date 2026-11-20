#!/usr/bin/env python3
"""mkcovers.py: draw the library covers of JukaHub's built-in games.

JukaLand, the Synth, Solitaire and the Games screen's arcade minigames (Snake,
Tetris, 2048, Pong, Minesweeper, Flappy) ship inside JukaHub, so no thumbnail
server has a box for them. Their covers are drawn here instead and embedded in
the player (player/builtin_covers/, go:embed in builtin_games.go), the same
480 x 300 landscape shape and title pill as the bundled HTML5 demos' covers.

Each cover is drawn at 4x and reduced with Lanczos, so edges are smooth
without a vector toolkit. Run from anywhere (Python 3.9+, Pillow):

    python3 branding/mkcovers.py

Files whose bytes did not change are left alone.
"""

import io
import math
import os
import random

from PIL import Image, ImageDraw, ImageFilter, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
OUT = os.path.join(ROOT, "player", "builtin_covers")
FONT = os.path.join(ROOT, "player", "Inter-SemiBold.ttf")

W, H = 480, 300
S = 4  # supersampling


def lerp(a, b, t):
    return tuple(int(round(a[i] + (b[i] - a[i]) * t)) for i in range(len(a)))


def vgradient(w, h, top, bottom):
    img = Image.new("RGB", (w, h))
    d = ImageDraw.Draw(img)
    for y in range(h):
        d.line([(0, y), (w, y)], fill=lerp(top, bottom, y / max(1, h - 1)))
    return img


def title_pill(img, text):
    """The dark rounded band with the white title, as on the HTML5 covers."""
    d = ImageDraw.Draw(img, "RGBA")
    pad = 14 * S
    top = H * S - 54 * S
    d.rounded_rectangle([pad, top, W * S - pad, H * S - 10 * S], radius=12 * S, fill=(8, 12, 22, 215),
                        outline=(255, 255, 255, 40), width=1 * S)
    font = ImageFont.truetype(FONT, 27 * S)
    tw = d.textlength(text, font=font)
    asc, desc = font.getmetrics()
    ty = top + (44 * S - (asc + desc)) // 2 - 1 * S
    d.text(((W * S - tw) / 2, ty), text, font=font, fill=(245, 248, 255, 255))


# ── JukaLand: a voxel island under a morning sky ──────────────────────────

GRASS_TOP = (104, 196, 92)
GRASS_SIDE_L = (120, 84, 52)
GRASS_SIDE_R = (96, 66, 40)
GRASS_LIP = (82, 164, 72)
STONE_TOP = (150, 156, 166)
STONE_L = (118, 122, 132)
STONE_R = (96, 100, 110)
WATER_TOP = (64, 150, 230)
LEAF_TOP = (64, 150, 70)
LEAF_L = (48, 120, 56)
LEAF_R = (38, 100, 46)
LOG_TOP = (170, 130, 84)
LOG_L = (120, 84, 50)
LOG_R = (100, 70, 42)
SAND_TOP = (232, 214, 150)
SAND_L = (200, 180, 118)
SAND_R = (176, 156, 100)


def iso(gx, gy, gz, ox, oy, t):
    """Grid (x right-down, y left-down, z up) to screen."""
    return (ox + (gx - gy) * t, oy + (gx + gy) * t / 2 - gz * t)


def cube(d, gx, gy, gz, ox, oy, t, top, left, right, lip=None):
    x, y = iso(gx, gy, gz, ox, oy, t)
    # top face: diamond
    pt = [(x, y), (x + t, y + t / 2), (x, y + t), (x - t, y + t / 2)]
    pl = [(x - t, y + t / 2), (x, y + t), (x, y + 2 * t), (x - t, y + 1.5 * t)]
    pr = [(x, y + t), (x + t, y + t / 2), (x + t, y + 1.5 * t), (x, y + 2 * t)]
    d.polygon(pl, fill=left)
    d.polygon(pr, fill=right)
    d.polygon(pt, fill=top)
    if lip:
        # a grass fringe hanging over the dirt sides
        lw = t * 0.22
        d.polygon([(x - t, y + t / 2), (x, y + t), (x, y + t + lw), (x - t, y + t / 2 + lw)], fill=lip)
        d.polygon([(x, y + t), (x + t, y + t / 2), (x + t, y + t / 2 + lw), (x, y + t + lw)], fill=lip)
    edge = tuple(max(0, c - 40) for c in top)
    d.line(pt + [pt[0]], fill=edge, width=max(1, S))


def jukaland():
    img = vgradient(W * S, H * S, (96, 170, 236), (196, 228, 250))
    d = ImageDraw.Draw(img, "RGBA")
    # sun with a soft halo
    halo = Image.new("RGBA", img.size, (0, 0, 0, 0))
    hd = ImageDraw.Draw(halo)
    sx, sy = 392 * S, 62 * S
    hd.ellipse([sx - 60 * S, sy - 60 * S, sx + 60 * S, sy + 60 * S], fill=(255, 244, 200, 110))
    halo = halo.filter(ImageFilter.GaussianBlur(18 * S))
    img.paste(halo, (0, 0), halo)
    d = ImageDraw.Draw(img, "RGBA")
    d.rectangle([sx - 20 * S, sy - 20 * S, sx + 20 * S, sy + 20 * S], fill=(255, 236, 150))
    # blocky clouds
    for cx, cy, n in [(70, 50, 5), (230, 34, 4), (300, 92, 3)]:
        for i in range(n):
            bx = (cx + i * 14) * S
            by = (cy - (6 if 0 < i < n - 1 else 0)) * S
            d.rectangle([bx, by, bx + 16 * S, by + 12 * S], fill=(255, 255, 255, 235))
        d.rectangle([cx * S, (cy + 6) * S, (cx + n * 14 + 2) * S, (cy + 14) * S], fill=(255, 255, 255, 235))

    t = 17 * S
    ox, oy = 214 * S, 98 * S
    rnd = random.Random(7)
    size = 7
    # height map: an island that rises toward the back
    hmap = {}
    for gx in range(size):
        for gy in range(size):
            dist = math.hypot(gx - 3, gy - 3)
            h = 2 if dist < 1.6 else (1 if dist < 3.0 else 0)
            if (gx, gy) in [(5, 0), (6, 1)]:
                h = 0
            hmap[(gx, gy)] = h
    water = {(6, 5), (6, 6), (5, 6), (4, 6), (6, 4)}
    sand = {(5, 5), (3, 6), (6, 3), (5, 4), (4, 5)}
    blocks = []
    for (gx, gy), h in hmap.items():
        if (gx, gy) in water:
            blocks.append((gx, gy, -0.25, "water"))
            continue
        for z in range(h):
            blocks.append((gx, gy, z, "stone" if z < h - 1 else "dirt"))
        blocks.append((gx, gy, h, "sand" if (gx, gy) in sand else "grass"))
    # a tree
    tx, ty = 2, 2
    th = hmap[(tx, ty)] + 1
    for z in range(th, th + 3):
        blocks.append((tx, ty, z, "log"))
    for lx in range(tx - 1, tx + 2):
        for ly in range(ty - 1, ty + 2):
            blocks.append((lx, ly, th + 3, "leaf"))
    blocks.append((tx, ty, th + 4, "leaf"))
    # a second, small tree
    blocks.append((4, 1, hmap[(4, 1)] + 1, "log"))
    blocks.append((4, 1, hmap[(4, 1)] + 2, "leaf"))
    # painter's order: back to front, bottom to top
    blocks.sort(key=lambda b: (b[0] + b[1], b[2]))
    for gx, gy, gz, kind in blocks:
        if kind == "grass":
            cube(d, gx, gy, gz, ox, oy, t, GRASS_TOP, GRASS_SIDE_L, GRASS_SIDE_R, lip=GRASS_LIP)
        elif kind == "sand":
            cube(d, gx, gy, gz, ox, oy, t, SAND_TOP, SAND_L, SAND_R)
        elif kind in ("stone", "dirt"):
            if kind == "dirt":
                cube(d, gx, gy, gz, ox, oy, t, GRASS_SIDE_L, GRASS_SIDE_L, GRASS_SIDE_R)
            else:
                cube(d, gx, gy, gz, ox, oy, t, STONE_TOP, STONE_L, STONE_R)
        elif kind == "water":
            x, y = iso(gx, gy, gz, ox, oy, t)
            d.polygon([(x, y), (x + t, y + t / 2), (x, y + t), (x - t, y + t / 2)], fill=WATER_TOP + (230,))
            d.line([(x - t * 0.4, y + t * 0.5), (x + t * 0.1, y + t * 0.5)], fill=(200, 236, 255, 200), width=2 * S)
        elif kind == "log":
            cube(d, gx, gy, gz, ox, oy, t, LOG_TOP, LOG_L, LOG_R)
        elif kind == "leaf":
            j = rnd.randint(-10, 10)
            top = tuple(max(0, min(255, c + j)) for c in LEAF_TOP)
            cube(d, gx, gy, gz, ox, oy, t, top, LEAF_L, LEAF_R)
    title_pill(img, "JUKALAND")
    return img.resize((W, H), Image.LANCZOS)


# ── Synth: a glowing 16-step grid on a dark stage ─────────────────────────

LANES = [
    ("kick", (255, 138, 76), "x...x...x...x..x"),
    ("snare", (255, 92, 150), "....x.......x..."),
    ("hat", (92, 220, 255), "x.x.x.xxx.x.x.x."),
    ("clap", (190, 120, 255), "............x..."),
    ("bass", (120, 240, 160), "x..x..x...x.x..."),
    ("lead", (255, 214, 92), "..x...x.x.....x."),
]


def synth():
    img = vgradient(W * S, H * S, (22, 16, 46), (10, 10, 24))
    glow = Image.new("RGBA", img.size, (0, 0, 0, 0))
    gd = ImageDraw.Draw(glow)
    gd.ellipse([-80 * S, -120 * S, 260 * S, 200 * S], fill=(120, 60, 220, 90))
    gd.ellipse([280 * S, 60 * S, 600 * S, 380 * S], fill=(0, 170, 220, 70))
    glow = glow.filter(ImageFilter.GaussianBlur(40 * S))
    img.paste(glow, (0, 0), glow)

    # waveform across the top
    d = ImageDraw.Draw(img, "RGBA")
    pts = []
    for i in range(0, W * S + 1, S):
        u = i / (W * S)
        y = 40 * S + math.sin(u * math.pi * 7) * 14 * S * math.sin(u * math.pi) + math.sin(u * math.pi * 23) * 3 * S
        pts.append((i, y))
    wave = Image.new("RGBA", img.size, (0, 0, 0, 0))
    wd = ImageDraw.Draw(wave)
    wd.line(pts, fill=(120, 230, 255, 255), width=6 * S)
    blur = wave.filter(ImageFilter.GaussianBlur(6 * S))
    img.paste(blur, (0, 0), blur)
    wd2 = ImageDraw.Draw(wave)
    wd2.line(pts, fill=(220, 250, 255, 255), width=2 * S)
    img.paste(wave, (0, 0), wave)

    # the step grid
    d = ImageDraw.Draw(img, "RGBA")
    gx0, gy0 = 40 * S, 74 * S
    cols, rows = 16, len(LANES)
    gap = 4 * S
    cw = (W * S - 2 * gx0 - gap * (cols - 1)) / cols
    ch = 22 * S
    playhead = 6
    pads = Image.new("RGBA", img.size, (0, 0, 0, 0))
    pd = ImageDraw.Draw(pads)
    for r, (_, col, pat) in enumerate(LANES):
        for c in range(cols):
            x = gx0 + c * (cw + gap)
            y = gy0 + r * (ch + gap)
            on = pat[c] == "x"
            if on:
                pd.rounded_rectangle([x, y, x + cw, y + ch], radius=4 * S, fill=col + (255,))
            else:
                base = (255, 255, 255, 34 if c % 4 else 52)
                pd.rounded_rectangle([x, y, x + cw, y + ch], radius=4 * S, fill=base)
    bloom = pads.filter(ImageFilter.GaussianBlur(7 * S))
    img.paste(bloom, (0, 0), bloom)
    img.paste(pads, (0, 0), pads)
    d = ImageDraw.Draw(img, "RGBA")
    # playhead column
    x = gx0 + playhead * (cw + gap)
    d.rounded_rectangle([x - 2 * S, gy0 - 5 * S, x + cw + 2 * S, gy0 + rows * (ch + gap) - gap + 5 * S],
                        radius=5 * S, outline=(255, 255, 255, 230), width=2 * S)
    title_pill(img, "SYNTH")
    return img.resize((W, H), Image.LANCZOS)


# ── Solitaire: a fan of cards on green felt ───────────────────────────────

def bezier(p0, p1, p2, p3, n=24):
    out = []
    for i in range(n + 1):
        t = i / n
        u = 1 - t
        out.append((u * u * u * p0[0] + 3 * u * u * t * p1[0] + 3 * u * t * t * p2[0] + t * t * t * p3[0],
                    u * u * u * p0[1] + 3 * u * u * t * p1[1] + 3 * u * t * t * p2[1] + t * t * t * p3[1]))
    return out


# The heart of player/solitaire_ui.go (solHeartCurve), in its 24-unit box.
HEART = [(12, 21), (7, 17), (1.5, 13.4), (1.5, 8.4), (1.5, 5.2), (4, 3), (6.9, 3), (9, 3), (10.8, 4.2),
         (12, 6.1), (13.2, 4.2), (15, 3), (17.1, 3), (20, 3), (22.5, 5.2), (22.5, 8.4), (22.5, 13.4),
         (17, 17), (12, 21)]


def suit_shape(d, suit, x, y, size, fill):
    """One suit in a size x size box at (x, y), the same shapes the game paints."""
    k = size / 24

    def heart(tf):
        pts = [tf(*p) for p in HEART]
        poly = []
        for i in range(0, len(pts) - 1, 3):
            poly += bezier(pts[i], pts[i + 1], pts[i + 2], pts[i + 3])
        d.polygon([(x + px * k, y + py * k) for px, py in poly], fill=fill)

    def stem(top):
        d.polygon([(x + 12 * k, y + top * k), (x + 16 * k, y + 22.5 * k), (x + 8 * k, y + 22.5 * k)], fill=fill)

    if suit == "H":
        heart(lambda px, py: (px, py + 0.5))
    elif suit == "D":
        d.polygon([(x + 12 * k, y + 1.5 * k), (x + 20 * k, y + 12 * k), (x + 12 * k, y + 22.5 * k),
                   (x + 4 * k, y + 12 * k)], fill=fill)
    elif suit == "S":
        heart(lambda px, py: (12 + (px - 12) * 0.92, 1.5 + (21 - py) * 0.92))
        stem(13)
    else:
        for cx, cy in ((12, 6.8), (6.6, 13.6), (17.4, 13.6)):
            r = 4.9 * k
            d.ellipse([x + cx * k - r, y + cy * k - r, x + cx * k + r, y + cy * k + r], fill=fill)
        r = 2.8 * k
        d.ellipse([x + 12 * k - r, y + 12 * k - r, x + 12 * k + r, y + 12 * k + r], fill=fill)
        stem(11)


def card_face(rank, suit, cw, ch):
    card = Image.new("RGBA", (cw, ch), (0, 0, 0, 0))
    d = ImageDraw.Draw(card)
    rad = cw // 12
    d.rounded_rectangle([0, 0, cw - 1, ch - 1], radius=rad, fill=(250, 250, 246, 255), outline=(0, 0, 0, 80), width=S)
    ink = (204, 38, 50, 255) if suit in "HD" else (26, 28, 34, 255)
    font = ImageFont.truetype(FONT, int(ch * 0.17))
    inset = cw // 14
    d.text((inset, inset // 2), rank, font=font, fill=ink)
    pip = int(ch * 0.17 * 0.8)
    suit_shape(d, suit, cw - inset - pip, inset, pip, ink)
    big = int(cw * 0.46)
    band = int(ch * 0.17) + inset
    suit_shape(d, suit, (cw - big) / 2, band + (ch - band - big) / 2, big, ink)
    return card


def solitaire():
    img = vgradient(W * S, H * S, (22, 104, 62), (8, 52, 32))
    glow = Image.new("RGBA", img.size, (0, 0, 0, 0))
    gd = ImageDraw.Draw(glow)
    gd.ellipse([90 * S, -60 * S, 390 * S, 220 * S], fill=(90, 200, 130, 80))
    glow = glow.filter(ImageFilter.GaussianBlur(40 * S))
    img.paste(glow, (0, 0), glow)

    cw, ch = 100 * S, 142 * S
    fan = [("10", "S"), ("J", "D"), ("Q", "C"), ("K", "H"), ("A", "S")]
    cx, pivot_y = W * S // 2, 330 * S
    for i, (rank, suit) in enumerate(fan):
        angle = (i - 2) * 11
        card = card_face(rank, suit, cw, ch)
        layer = Image.new("RGBA", img.size, (0, 0, 0, 0))
        layer.paste(card, (cx - cw // 2, 30 * S), card)
        rotated = layer.rotate(-angle, resample=Image.BICUBIC, center=(cx, pivot_y))
        shadow = Image.new("RGBA", img.size, (0, 0, 0, 0))
        shadow.paste((0, 0, 0, 110), (0, 0), rotated.split()[3])
        shadow = shadow.filter(ImageFilter.GaussianBlur(5 * S))
        img.paste(shadow, (3 * S, 5 * S), shadow)
        img.paste(rotated, (0, 0), rotated)
    title_pill(img, "SOLITAIRE")
    return img.resize((W, H), Image.LANCZOS)


# ── The arcade minigames of the Games screen (games.go) ───────────────────
#
# Each cover paints the game the way games.go draws it: the same tile, piece,
# pipe and bird colours, so the card and the game look like one thing.


def glow_layer(img, layer, radius):
    """Paste layer twice: blurred as a bloom, then sharp on top."""
    bloom = layer.filter(ImageFilter.GaussianBlur(radius))
    img.paste(bloom, (0, 0), bloom)
    img.paste(layer, (0, 0), layer)


def soft_spot(img, box, color, blur):
    spot = Image.new("RGBA", img.size, (0, 0, 0, 0))
    ImageDraw.Draw(spot).ellipse(box, fill=color)
    spot = spot.filter(ImageFilter.GaussianBlur(blur))
    img.paste(spot, (0, 0), spot)


def grid_lines(img, step, color, top=0, bottom=None):
    d = ImageDraw.Draw(img, "RGBA")
    bottom = H * S if bottom is None else bottom
    for x in range(0, W * S + 1, step):
        d.line([(x, top), (x, bottom)], fill=color, width=S)
    for y in range(top, bottom + 1, step):
        d.line([(0, y), (W * S, y)], fill=color, width=S)


# Snake: a green snake curling toward a red apple on a dark grid.

def snake():
    img = vgradient(W * S, H * S, (12, 36, 30), (6, 16, 18))
    soft_spot(img, [120 * S, -40 * S, 420 * S, 240 * S], (40, 200, 120, 70), 40 * S)
    cell = 30 * S
    grid_lines(img, cell, (255, 255, 255, 14))
    # the body, tail first, on whole cells (x, y), one rounded square a cell
    # as the game draws it
    path = [(1, 6), (2, 6), (3, 6), (4, 6), (4, 5), (4, 4), (4, 3), (5, 3), (6, 3), (7, 3), (7, 4), (7, 5),
            (8, 5), (9, 5), (10, 5), (10, 4), (10, 3), (10, 2), (11, 2), (12, 2)]
    layer = Image.new("RGBA", img.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    n = len(path)
    inset = 2 * S
    for i, (cx, cy) in enumerate(path[:-1]):
        t = i / (n - 1)
        col = lerp((36, 140, 84), (100, 228, 132), t) + (255,)
        x, y = cx * cell, cy * cell
        d.rounded_rectangle([x + inset, y + inset, x + cell - inset, y + cell - inset], radius=7 * S, fill=col)
    # the head: a little bigger, with eyes looking right
    hx, hy = path[-1]
    x, y = hx * cell, hy * cell
    d.rounded_rectangle([x, y, x + cell + 2 * S, y + cell], radius=9 * S, fill=(150, 250, 170, 255))
    for ey in (y + 6 * S, y + cell - 13 * S):
        d.ellipse([x + 15 * S, ey, x + 23 * S, ey + 7 * S], fill=(255, 255, 255, 255))
        d.ellipse([x + 18 * S, ey + 1 * S, x + 23 * S, ey + 6 * S], fill=(16, 24, 22, 255))
    # a forked tongue
    tx = x + cell + 2 * S
    ty = y + cell // 2
    d.line([(tx, ty), (tx + 10 * S, ty)], fill=(235, 70, 90, 255), width=2 * S)
    d.line([(tx + 10 * S, ty), (tx + 15 * S, ty - 4 * S)], fill=(235, 70, 90, 255), width=2 * S)
    d.line([(tx + 10 * S, ty), (tx + 15 * S, ty + 4 * S)], fill=(235, 70, 90, 255), width=2 * S)
    glow_layer(img, layer, 6 * S)
    # the food, two cells ahead, in the game's red
    ax, ay = 14 * cell + cell // 2, 2 * cell + cell // 2 + 1 * S
    apple = Image.new("RGBA", img.size, (0, 0, 0, 0))
    ad = ImageDraw.Draw(apple)
    r = 12 * S
    ad.ellipse([ax - r - 3 * S, ay - r + 1 * S, ax + 1 * S, ay + r + 1 * S], fill=(220, 50, 50, 255))
    ad.ellipse([ax - 1 * S, ay - r + 1 * S, ax + r + 3 * S, ay + r + 1 * S], fill=(220, 50, 50, 255))
    ad.ellipse([ax - 10 * S, ay - 7 * S, ax - 4 * S, ay - 1 * S], fill=(255, 170, 170, 200))
    ad.line([(ax, ay - r + 2 * S), (ax + 2 * S, ay - r - 7 * S)], fill=(110, 70, 40, 255), width=2 * S)
    ad.ellipse([ax + 2 * S, ay - r - 9 * S, ax + 13 * S, ay - r - 3 * S], fill=(90, 200, 90, 255))
    glow_layer(img, apple, 8 * S)
    title_pill(img, "SNAKE")
    return img.resize((W, H), Image.LANCZOS)


# Tetris: a stacked well with one line about to clear and a T falling in.

TET = {
    "I": (0, 240, 240), "J": (40, 90, 240), "L": (240, 160, 0), "O": (240, 220, 0),
    "S": (0, 220, 90), "T": (160, 40, 240), "Z": (240, 40, 40),
}


def block(d, x, y, c, col):
    """One bevelled block, the light edge top-left and the shadow bottom-right."""
    b = 3 * S
    light = tuple(min(255, v + 70) for v in col)
    dark = tuple(max(0, v - 70) for v in col)
    d.rectangle([x, y, x + c - 1, y + c - 1], fill=dark)
    d.polygon([(x, y), (x + c, y), (x + c - b, y + b), (x + b, y + b), (x + b, y + c - b), (x, y + c)], fill=light)
    d.rectangle([x + b, y + b, x + c - b - 1, y + c - b - 1], fill=col)


def tetris():
    img = vgradient(W * S, H * S, (18, 20, 48), (8, 8, 22))
    soft_spot(img, [140 * S, 20 * S, 340 * S, 260 * S], (120, 70, 255, 60), 50 * S)
    c = 20 * S
    cols, rows = 10, 11
    wx = (W * S - cols * c) // 2
    wy = 18 * S
    d = ImageDraw.Draw(img, "RGBA")
    # the well
    d.rounded_rectangle([wx - 6 * S, wy - 6 * S, wx + cols * c + 6 * S, wy + rows * c + 6 * S], radius=6 * S,
                        fill=(6, 8, 20, 230), outline=(140, 150, 220, 120), width=2 * S)
    for gx in range(1, cols):
        d.line([(wx + gx * c, wy), (wx + gx * c, wy + rows * c)], fill=(255, 255, 255, 12), width=S)
    # the stack, bottom row first; "." is a gap
    stack = [
        "IIIIJJJLLO",
        "ZZTTTJLLOO",
        "SZZTOO.SSO",
        "SS..OO..SS",
        "J.......LS",
    ]
    clear = 0  # row index (from the bottom) that is full
    for r, line in enumerate(stack):
        y = wy + (rows - 1 - r) * c
        for gx, ch in enumerate(line):
            if ch != ".":
                block(d, wx + gx * c, y, c, TET[ch])
    # the falling T, rotated, aimed at the gap
    for gx, gy in [(5, 3), (6, 3), (7, 3), (6, 4)]:
        block(d, wx + gx * c, wy + gy * c, c, TET["T"])
    # a ghost where it lands
    for gx, gy in [(5, rows - 5), (6, rows - 5), (7, rows - 5), (6, rows - 4)]:
        d.rectangle([wx + gx * c + 2 * S, wy + gy * c + 2 * S, wx + gx * c + c - 2 * S, wy + gy * c + c - 2 * S],
                    outline=(200, 160, 255, 140), width=2 * S)
    # the full bottom line flashes
    flash = Image.new("RGBA", img.size, (0, 0, 0, 0))
    fd = ImageDraw.Draw(flash)
    y = wy + (rows - 1 - clear) * c
    fd.rectangle([wx - 10 * S, y - 2 * S, wx + cols * c + 10 * S, y + c + 2 * S], fill=(255, 255, 255, 120))
    flash = flash.filter(ImageFilter.GaussianBlur(6 * S))
    img.paste(flash, (0, 0), flash)
    # side panels: the next piece and a few loose blocks
    d = ImageDraw.Draw(img, "RGBA")
    nx, ny = 52 * S, 40 * S
    d.rounded_rectangle([nx, ny, nx + 84 * S, ny + 70 * S], radius=8 * S, fill=(255, 255, 255, 18),
                        outline=(255, 255, 255, 50), width=S)
    sc = 16 * S
    for gx, gy in [(0, 1), (1, 1), (2, 1), (3, 1)]:
        block(d, nx + 10 * S + gx * sc, ny + 12 * S + gy * sc, sc, TET["I"])
    rx = W * S - 52 * S - 84 * S
    d.rounded_rectangle([rx, ny, rx + 84 * S, ny + 70 * S], radius=8 * S, fill=(255, 255, 255, 18),
                        outline=(255, 255, 255, 50), width=S)
    for gx, gy in [(1, 0), (2, 0), (0, 1), (1, 1)]:
        block(d, rx + 18 * S + gx * sc, ny + 19 * S + gy * sc, sc, TET["S"])
    for gx, gy in [(0, 0), (1, 0), (1, 1), (2, 1)]:
        block(d, nx + 18 * S + gx * sc, 150 * S + gy * sc, sc, TET["Z"])
    for gx, gy in [(0, 0), (0, 1), (1, 1), (2, 1)]:
        block(d, rx + 18 * S + gx * sc, 150 * S + gy * sc, sc, TET["J"])
    title_pill(img, "TETRIS")
    return img.resize((W, H), Image.LANCZOS)


# 2048: the board with its tiles in the game's colours, 2048 glowing gold.

T2048 = {
    0: (20, 26, 42), 2: (238, 228, 218), 4: (237, 224, 200), 8: (242, 177, 121), 16: (245, 149, 99),
    32: (246, 124, 95), 64: (246, 94, 59), 128: (237, 207, 114), 256: (237, 204, 97), 512: (237, 200, 80),
    1024: (237, 197, 63), 2048: (237, 194, 46),
}


def g2048():
    img = vgradient(W * S, H * S, (44, 34, 30), (20, 16, 18))
    soft_spot(img, [250 * S, -20 * S, 520 * S, 220 * S], (255, 200, 80, 70), 40 * S)
    board = [
        [2, 4, 8, 16],
        [256, 128, 64, 32],
        [512, 1024, 2048, 4],
        [0, 2, 8, 2],
    ]
    cell = 52 * S
    gap = 6 * S
    size = 4 * cell + 5 * gap
    bx = (W * S - size) // 2
    by = 12 * S
    d = ImageDraw.Draw(img, "RGBA")
    d.rounded_rectangle([bx, by, bx + size, by + size], radius=12 * S, fill=(60, 53, 46, 255))
    tiles = Image.new("RGBA", img.size, (0, 0, 0, 0))
    td = ImageDraw.Draw(tiles)
    for ry, row in enumerate(board):
        for cx, val in enumerate(row):
            x = bx + gap + cx * (cell + gap)
            y = by + gap + ry * (cell + gap)
            col = T2048[val] if val else (84, 74, 64)
            if val == 2048:
                big = (x, y)
                continue
            td.rounded_rectangle([x, y, x + cell, y + cell], radius=7 * S, fill=col + (255,))
            if not val:
                continue
            text = str(val)
            fs = {1: 28, 2: 26, 3: 22, 4: 18}[len(text)] * S
            font = ImageFont.truetype(FONT, fs)
            ink = (119, 110, 101) if val <= 4 else (249, 246, 242)
            tw = td.textlength(text, font=font)
            asc, desc = font.getmetrics()
            td.text((x + (cell - tw) / 2, y + (cell - asc - desc) / 2 - 1 * S), text, font=font, fill=ink + (255,))
    img.paste(tiles, (0, 0), tiles)
    # 2048 itself pops out of the board, as a merge does
    x, y = big
    pop = 6 * S
    shadow = Image.new("RGBA", img.size, (0, 0, 0, 0))
    ImageDraw.Draw(shadow).rounded_rectangle([x - pop, y - pop + 5 * S, x + cell + pop, y + cell + pop + 5 * S],
                                             radius=10 * S, fill=(0, 0, 0, 150))
    shadow = shadow.filter(ImageFilter.GaussianBlur(5 * S))
    img.paste(shadow, (0, 0), shadow)
    d = ImageDraw.Draw(img, "RGBA")
    d.rounded_rectangle([x - pop, y - pop, x + cell + pop, y + cell + pop], radius=9 * S, fill=T2048[2048],
                        outline=(255, 236, 160), width=2 * S)
    font = ImageFont.truetype(FONT, 20 * S)
    tw = d.textlength("2048", font=font)
    asc, desc = font.getmetrics()
    d.text((x + (cell - tw) / 2, y + (cell - asc - desc) / 2 - 1 * S), "2048", font=font, fill=(255, 255, 255))
    title_pill(img, "2048")
    return img.resize((W, H), Image.LANCZOS)


# Pong: two glowing paddles, the dashed net and a ball with its trail.

DIGITS = {
    "0": ["111", "101", "101", "101", "111"], "1": ["010", "110", "010", "010", "111"],
    "2": ["111", "001", "111", "100", "111"], "3": ["111", "001", "111", "001", "111"],
    "4": ["101", "101", "111", "001", "001"], "5": ["111", "100", "111", "001", "111"],
    "7": ["111", "001", "010", "010", "010"],
}


def pixel_digit(d, ch, x, y, px, fill):
    for r, line in enumerate(DIGITS[ch]):
        for c, bit in enumerate(line):
            if bit == "1":
                d.rectangle([x + c * px, y + r * px, x + (c + 1) * px - 1, y + (r + 1) * px - 1], fill=fill)


def pong():
    img = vgradient(W * S, H * S, (14, 10, 32), (6, 6, 14))
    soft_spot(img, [-60 * S, 20 * S, 160 * S, 240 * S], (0, 200, 255, 60), 40 * S)
    soft_spot(img, [320 * S, 20 * S, 540 * S, 240 * S], (255, 60, 170, 60), 40 * S)
    d = ImageDraw.Draw(img, "RGBA")
    # the net
    for y in range(14 * S, 240 * S, 18 * S):
        d.rectangle([W * S // 2 - 2 * S, y, W * S // 2 + 2 * S, y + 10 * S], fill=(255, 255, 255, 70))
    # the score
    px = 7 * S
    pixel_digit(d, "3", W * S // 2 - 52 * S, 22 * S, px, (255, 255, 255, 150))
    pixel_digit(d, "5", W * S // 2 + 31 * S, 22 * S, px, (255, 255, 255, 150))
    # paddles
    lay = Image.new("RGBA", img.size, (0, 0, 0, 0))
    ld = ImageDraw.Draw(lay)
    ld.rounded_rectangle([34 * S, 120 * S, 46 * S, 196 * S], radius=6 * S, fill=(80, 220, 255, 255))
    ld.rounded_rectangle([W * S - 46 * S, 52 * S, W * S - 34 * S, 128 * S], radius=6 * S, fill=(255, 90, 190, 255))
    glow_layer(img, lay, 9 * S)
    # the ball's trail: fading dots back toward the left paddle
    trail = Image.new("RGBA", img.size, (0, 0, 0, 0))
    tdraw = ImageDraw.Draw(trail)
    bx, by = 330 * S, 104 * S
    vx, vy = -1.0, 0.36
    steps = 14
    for i in range(steps, 0, -1):
        k = i / steps
        x = bx + vx * i * 13 * S
        y = by + vy * i * 13 * S
        r = (9 - 5 * k) * S
        tdraw.ellipse([x - r, y - r, x + r, y + r], fill=(255, 255, 255, int(150 * (1 - k))))
    img.paste(trail, (0, 0), trail)
    ball = Image.new("RGBA", img.size, (0, 0, 0, 0))
    r = 9 * S
    ImageDraw.Draw(ball).ellipse([bx - r, by - r, bx + r, by + r], fill=(255, 255, 255, 255))
    glow_layer(img, ball, 8 * S)
    title_pill(img, "PONG")
    return img.resize((W, H), Image.LANCZOS)


# Minesweeper: a half-cleared field with numbers, flags and one mine.

MS_NUM = {1: (90, 170, 255), 2: (90, 210, 120), 3: (240, 110, 100), 4: (190, 130, 255)}


def mine(d, cx, cy, r, body=(30, 32, 40, 255)):
    for a in range(0, 180, 45):
        dx, dy = math.cos(math.radians(a)) * r * 1.45, math.sin(math.radians(a)) * r * 1.45
        d.line([(cx - dx, cy - dy), (cx + dx, cy + dy)], fill=body, width=int(r * 0.32))
    d.ellipse([cx - r, cy - r, cx + r, cy + r], fill=body)
    hr = r * 0.32
    d.ellipse([cx - r * 0.45 - hr, cy - r * 0.45 - hr, cx - r * 0.45 + hr, cy - r * 0.45 + hr],
              fill=(255, 255, 255, 200))


def flag(d, x, y, c):
    pole = (60, 64, 76, 255)
    d.rectangle([x + c * 0.48, y + c * 0.18, x + c * 0.56, y + c * 0.78], fill=pole)
    d.rectangle([x + c * 0.30, y + c * 0.74, x + c * 0.74, y + c * 0.82], fill=pole)
    d.polygon([(x + c * 0.56, y + c * 0.18), (x + c * 0.82, y + c * 0.32), (x + c * 0.56, y + c * 0.48)],
              fill=(240, 70, 70, 255))


def minesweeper():
    img = vgradient(W * S, H * S, (26, 34, 54), (12, 16, 28))
    soft_spot(img, [60 * S, -40 * S, 420 * S, 200 * S], (90, 140, 255, 50), 40 * S)
    # "#" hidden, "." open blank, digits open, "F" flag, "*" the mine that went off
    field = [
        "##########",
        "##3F2#####",
        "#2F21F2###",
        "#2111212##",
        "11..1*2F##",
        "....1221##",
        "1.......1#",
    ]
    c = 32 * S
    cols, rows = len(field[0]), len(field)
    fx = (W * S - cols * c) // 2
    fy = 12 * S
    d = ImageDraw.Draw(img, "RGBA")
    font = ImageFont.truetype(FONT, 20 * S)
    for ry, line in enumerate(field):
        for cx, ch in enumerate(line):
            x, y = fx + cx * c, fy + ry * c
            g = 2 * S
            if ch in "#F":
                d.rounded_rectangle([x + g, y + g, x + c - g, y + c - g], radius=5 * S, fill=(56, 70, 100, 255))
                d.rounded_rectangle([x + g, y + g, x + c - g, y + g + 5 * S], radius=4 * S, fill=(84, 100, 136, 255))
                d.rounded_rectangle([x + g, y + g + 3 * S, x + c - g, y + c - g], radius=5 * S, fill=(56, 70, 100, 255))
                if ch == "F":
                    flag(d, x, y, c)
            elif ch == "*":
                d.rounded_rectangle([x + g, y + g, x + c - g, y + c - g], radius=5 * S, fill=(150, 40, 52, 255))
            else:
                d.rounded_rectangle([x + g, y + g, x + c - g, y + c - g], radius=5 * S, fill=(22, 28, 42, 255))
                if ch.isdigit():
                    text = ch
                    tw = d.textlength(text, font=font)
                    asc, desc = font.getmetrics()
                    d.text((x + (c - tw) / 2, y + (c - asc - desc) / 2), text, font=font,
                           fill=MS_NUM[int(ch)] + (255,))
    # the mine, with a burst behind it
    my = field.index(next(l for l in field if "*" in l))
    mx = field[my].index("*")
    cx, cy = fx + mx * c + c // 2, fy + my * c + c // 2
    burst = Image.new("RGBA", img.size, (0, 0, 0, 0))
    bd = ImageDraw.Draw(burst)
    bd.ellipse([cx - 34 * S, cy - 34 * S, cx + 34 * S, cy + 34 * S], fill=(255, 140, 60, 150))
    burst = burst.filter(ImageFilter.GaussianBlur(14 * S))
    img.paste(burst, (0, 0), burst)
    d = ImageDraw.Draw(img, "RGBA")
    mine(d, cx, cy, 8 * S)
    title_pill(img, "MINESWEEPER")
    return img.resize((W, H), Image.LANCZOS)


# Flappy: the yellow bird between two green pipes over a blue sky.

def pipe(d, x, top, bottom, w, cap_at_top):
    body = (70, 190, 90, 255)
    dark = (45, 145, 70, 255)
    light = (130, 225, 140, 255)
    d.rectangle([x, top, x + w, bottom], fill=body)
    d.rectangle([x + w - 9 * S, top, x + w, bottom], fill=dark)
    d.rectangle([x + 6 * S, top, x + 12 * S, bottom], fill=light)
    cap_h = 18 * S
    cy = top if cap_at_top else bottom - cap_h
    d.rounded_rectangle([x - 6 * S, cy, x + w + 6 * S, cy + cap_h], radius=3 * S, fill=body,
                        outline=dark, width=2 * S)
    d.rectangle([x, cy + 3 * S, x + 6 * S, cy + cap_h - 3 * S], fill=light)


def flappy():
    img = vgradient(W * S, H * S, (60, 150, 215), (160, 214, 240))
    d = ImageDraw.Draw(img, "RGBA")
    # soft round clouds, as the game paints them
    for x, y, r in [(60, 70, 34), (100, 84, 26), (300, 46, 30), (340, 58, 22), (430, 120, 28)]:
        d.ellipse([(x - r) * S, (y - r) * S, (x + r) * S, (y + r) * S], fill=(255, 255, 255, 90))
    # distant hills
    for x, r in [(40, 70), (170, 90), (330, 80), (470, 70)]:
        d.ellipse([(x - r) * S, (232 - r * 0.5) * S, (x + r) * S, (232 + r * 0.9) * S], fill=(120, 200, 140, 255))
    # pipes: one pair the bird is passing, one further on
    pw = 58 * S
    gap_top, gap_bot = 92 * S, 176 * S
    pipe(d, 220 * S, -10 * S, gap_top, pw, cap_at_top=False)
    pipe(d, 220 * S, gap_bot, 250 * S, pw, cap_at_top=True)
    pipe(d, 400 * S, -10 * S, 52 * S, pw, cap_at_top=False)
    pipe(d, 400 * S, 140 * S, 250 * S, pw, cap_at_top=True)
    # the ground
    d.rectangle([0, 236 * S, W * S, H * S], fill=(90, 180, 95, 255))
    d.rectangle([0, 236 * S, W * S, 242 * S], fill=(130, 215, 120, 255))
    for x in range(-10 * S, W * S, 22 * S):
        d.polygon([(x, 242 * S), (x + 11 * S, 242 * S), (x + 4 * S, 250 * S), (x - 7 * S, 250 * S)],
                  fill=(76, 160, 82, 255))
    # the bird, tilted up mid-flap, with a dotted flight path behind it
    bx, by = 152 * S, 120 * S
    for i in range(1, 6):
        px = bx - i * 22 * S
        py = by + (i * i) * 2.2 * S
        rr = 3 * S
        d.ellipse([px - rr, py - rr, px + rr, py + rr], fill=(255, 255, 255, 170 - i * 25))
    bird = Image.new("RGBA", (120 * S, 120 * S), (0, 0, 0, 0))
    b = ImageDraw.Draw(bird)
    c0 = 60 * S
    r = 22 * S
    b.ellipse([c0 - r, c0 - r, c0 + r, c0 + r], fill=(255, 215, 70, 255), outline=(200, 140, 30, 255), width=2 * S)
    b.ellipse([c0 - r + 6 * S, c0 + 2 * S, c0 + r - 8 * S, c0 + r - 2 * S], fill=(255, 238, 150, 255))
    # wing
    b.ellipse([c0 - 22 * S, c0 - 4 * S, c0 + 2 * S, c0 + 12 * S], fill=(255, 245, 200, 255),
              outline=(210, 150, 40, 255), width=2 * S)
    # eye
    b.ellipse([c0 + 4 * S, c0 - 16 * S, c0 + 18 * S, c0 - 2 * S], fill=(255, 255, 255, 255))
    b.ellipse([c0 + 11 * S, c0 - 12 * S, c0 + 17 * S, c0 - 6 * S], fill=(25, 30, 40, 255))
    # beak
    b.rounded_rectangle([c0 + 12 * S, c0 + 1 * S, c0 + 32 * S, c0 + 9 * S], radius=4 * S, fill=(250, 150, 40, 255))
    b.rounded_rectangle([c0 + 12 * S, c0 + 7 * S, c0 + 28 * S, c0 + 14 * S], radius=4 * S, fill=(235, 120, 30, 255))
    bird = bird.rotate(18, resample=Image.BICUBIC)
    shadow = Image.new("RGBA", bird.size, (0, 0, 0, 0))
    shadow.paste((0, 0, 0, 70), (0, 0), bird.split()[3])
    shadow = shadow.filter(ImageFilter.GaussianBlur(4 * S))
    img.paste(shadow, (bx - c0 + 3 * S, by - c0 + 5 * S), shadow)
    img.paste(bird, (bx - c0, by - c0), bird)
    title_pill(img, "FLAPPY")
    return img.resize((W, H), Image.LANCZOS)


def write(name, img):
    os.makedirs(OUT, exist_ok=True)
    buf = io.BytesIO()
    img.convert("RGB").save(buf, "PNG", optimize=True)
    data = buf.getvalue()
    path = os.path.join(OUT, name)
    try:
        with open(path, "rb") as f:
            if f.read() == data:
                print("unchanged", path)
                return
    except FileNotFoundError:
        pass
    with open(path, "wb") as f:
        f.write(data)
    print("wrote", path, len(data), "bytes")


if __name__ == "__main__":
    write("jukaland.png", jukaland())
    write("synth.png", synth())
    write("solitaire.png", solitaire())
    write("snake.png", snake())
    write("tetris.png", tetris())
    write("2048.png", g2048())
    write("pong.png", pong())
    write("minesweeper.png", minesweeper())
    write("flappy.png", flappy())
