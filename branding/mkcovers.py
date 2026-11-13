#!/usr/bin/env python3
"""mkcovers.py: draw the library covers of JukaHub's built-in games.

JukaLand and the Synth ship inside JukaHub, so no thumbnail server has a box
for them. These two covers are drawn here instead and embedded in the player
(player/builtin_covers/, go:embed in builtin_games.go), the same 480 x 300
landscape shape and title pill as the bundled HTML5 demos' covers.

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
