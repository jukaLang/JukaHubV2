#!/usr/bin/env python3
"""mkbootlogo.py: render the JukaHub boot logo the handheld shows at power-on.

A TrimUI shows one still picture from its boot partition while Linux starts.
When the device boots straight into JukaHub, that picture should already be
JukaHub: the logo tile over the wordmark on black, placed exactly where the
boot animation's first frame puts them, so the still logo appears to come
alive when the app takes over instead of being replaced by a different one.

JukaHub writes the picture itself (Settings -> General -> Boot logo,
player/bootlogo.go), rendered at whatever size and pixel format the firmware's
own file uses. This script renders the same composition as reference files for
everyone who flashes by hand (CrossMix's BootLogo app, NextUI's Bootlogo.pak)
and for the docs. The placement is player/bootlogo_layout.go's, constant for
constant; the logo artwork is mklogo.py's render at the exact size, and the
wordmark is the bundled Inter that the app draws it with.

Run from anywhere (Python 3.9+, numpy, Pillow):

    python3 branding/mkbootlogo.py                     # write every file
    python3 branding/mkbootlogo.py --preview out.png   # contact sheet, nothing else

Files whose bytes did not change are left alone.

It writes, under branding/bootlogo/:

  jukahub-bootlogo-1280x720.bmp/.png   TrimUI Smart Pro and Smart Pro S
  jukahub-bootlogo-1024x768.bmp/.png   TrimUI Brick and Hammer
  jukahub-boot-sheet.png               the boot logos beside a mock of the
                                       animation's later beat, for the docs
                                       (also docs/assets/, where the docs
                                       site can show it)

The BMPs are what the flashing tools want: 24-bit, uncompressed, bottom-up,
a plain 40-byte header, well under 6 MB. The PNGs are the same pixels.
"""

import argparse
import io
import math
import os
import struct
import sys

import numpy as np
from PIL import Image, ImageDraw, ImageFilter, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, HERE)
sys.dont_write_bytecode = True  # no __pycache__ left in branding/ by the import below
import mklogo  # noqa: E402  (render_logo and the palette; same folder)

FONT = os.path.join(ROOT, "player", "Inter-Regular.ttf")
OUT = "branding/bootlogo"

# ── Layout: player/bootlogo_layout.go, constant for constant ──────────────
SPRITE_FRAC = 0.25        # the artwork's square (its margin included), of the height
WORD_FRAC = 0.06          # the wordmark's font size, of the height
GAP_FRAC = 0.03           # artwork square to the wordmark's line box
LIFT_FRAC = 0.02          # the group sits this far above the true centre
TRACK_GAP_FRAC = 0.055    # wordmark line box to the progress track
TRACK_W_FRAC = 0.2        # the track's width, of the width
STATUS_GAP_FRAC = 0.018   # track to the status line under it
INTER_LINE_EM = 1.2109375  # Go's fallback line box when it has no font to ask

INK = "#EEF3FF"           # bootLogoInk: white with a breath of the logo's ice
WORDMARK = "JukaHub"

# The line box is MEASURED here: Pillow's getmetrics() is FreeType's rounded
# ascender plus descender, the same numbers SDL_ttf turns into the height of
# the surface it renders the wordmark into, and the app places that surface by
# font.Height(). (Inter at 43 px measures 53; the 1.2109375 em estimate, which
# Go only uses without a font, gives 52.)

TARGETS = (
    # file stem, width, height, devices
    ("jukahub-bootlogo-1280x720", 1280, 720, "TrimUI Smart Pro, Smart Pro S"),
    ("jukahub-bootlogo-1024x768", 1024, 768, "TrimUI Brick, Hammer"),
)

# The boot backdrop's four lights at rest (player/bootliquid.go bootBlobs):
# centre x, y (of the screen), diameter (of the height), peak alpha, colour,
# lobe shape and resting angle. The first is tinted with the theme accent.
BLOBS = (
    (0.30, 0.36, 1.00, 120, mklogo.CYAN, 0, 0.0 * 30),
    (0.71, 0.64, 1.08, 118, mklogo.VIOLET, 1, 2.1 * 30),
    (0.58, 0.24, 0.74, 92, mklogo.BLUE, 1, 4.2 * 30),
    (0.24, 0.76, 0.68, 84, mklogo.LILAC, 0, 1.2 * 30),
)
BLOB_LOBES = (
    ((0.50, 0.50, 0.17, 1.00), (0.37, 0.42, 0.12, 0.72), (0.63, 0.60, 0.13, 0.62)),
    ((0.50, 0.50, 0.16, 1.00), (0.61, 0.38, 0.11, 0.66), (0.40, 0.64, 0.14, 0.56)),
)
BACKDROP_BASE = (5, 8, 13)  # the hub preset's #070B12, a shade under (28% to black)


def go_round(v):
    """bootLogoRound: half away from zero, the way the Go side rounds."""
    return -int(-v + 0.5) if v < 0 else int(v + 0.5)


def word_px(h):
    """bootLogoWordPx."""
    return max(12, int(h * WORD_FRAC + 0.5))


def place(w, h, line_h=0):
    """bootLogoPlace: the artwork square, the wordmark's line box, the track
    and the status line on a w x h screen. line_h <= 0 estimates the line box
    from Inter's metrics, exactly as the Go fallback does."""
    side = min(go_round(h * SPRITE_FRAC), w)
    px = word_px(h)
    if line_h <= 0:
        line_h = go_round(px * INTER_LINE_EM)
    gap = go_round(h * GAP_FRAC)
    group = side + gap + line_h
    top = max(0, go_round((h - group) / 2 - h * LIFT_FRAC))
    word_top = top + side + gap
    track_w = go_round(w * TRACK_W_FRAC)
    track_h = max(3, go_round(h * 0.0045))
    track = (int((w - track_w) / 2), word_top + line_h + go_round(h * TRACK_GAP_FRAC), track_w, track_h)
    return {
        "sprite": (int((w - side) / 2), top, side, side),
        "word_px": px,
        "word_top": word_top,
        "line_h": line_h,
        "track": track,
        "status_y": track[1] + track[3] + go_round(h * STATUS_GAP_FRAC),
    }


def selfcheck():
    """The numbers player/bootlogo_layout_test.go pins, so the two sides cannot
    drift apart without this script refusing to run."""
    l = place(1280, 720, 52)
    assert l["sprite"] == (550, 219, 180, 180), l
    assert (l["word_px"], l["word_top"], l["line_h"]) == (43, 421, 52), l
    assert l["track"] == (512, 513, 256, 3) and l["status_y"] == 529, l
    # The Brick's 960x720 canvas scaled to its 1024x768 panel meets the panel's
    # own placement within a pixel and a half.
    canvas, panel, k = place(960, 720), place(1024, 768), 768 / 720
    for got, want in ((panel["sprite"][0], canvas["sprite"][0] * k), (panel["sprite"][1], canvas["sprite"][1] * k),
                      (panel["sprite"][2], canvas["sprite"][2] * k), (panel["word_top"], canvas["word_top"] * k)):
        assert abs(got - want) <= 1.5, (got, want)


# ── Drawing ───────────────────────────────────────────────────────────────

def inter(px):
    return ImageFont.truetype(FONT, px)


def line_box(font):
    ascent, descent = font.getmetrics()
    return ascent + descent


def draw_wordmark(img, font, cx, top, fill=INK):
    """The wordmark centred on cx with its line box's top at top: anchor "la"
    puts the ascender line there, which is where SDL_ttf's surface begins."""
    d = ImageDraw.Draw(img)
    d.text((cx - font.getlength(WORDMARK) / 2, top), WORDMARK, font=font, fill=fill, anchor="la")


def paste_logo(img, sprite):
    x, y, side, _ = sprite
    logo = mklogo.render_logo(side)
    img.paste(logo, (x, y), logo)


def render_bootlogo(w, h):
    """The firmware's picture: black, the logo, the wordmark."""
    font = inter(word_px(h))
    l = place(w, h, line_box(font))
    img = Image.new("RGB", (w, h), (0, 0, 0))
    paste_logo(img, l["sprite"])
    draw_wordmark(img, font, w / 2, l["word_top"])
    return img


def blob_texture(shape, size=128):
    """bootLiquidBlobPixels: three gaussian lobes, windowed, normalised."""
    idx = (np.arange(size) + 0.5) / size
    u, v = np.meshgrid(idx, idx)
    f = np.zeros_like(u)
    for lx, ly, s, wgt in BLOB_LOBES[shape]:
        f += wgt * np.exp(-((u - lx) ** 2 + (v - ly) ** 2) / (2 * s * s))
    a = (1 - np.exp(-1.8 * f)) * np.sqrt(np.sin(np.pi * u) * np.sin(np.pi * v))
    a = (a / a.max()) ** 1.3
    return Image.fromarray((a * 255 + 0.5).astype(np.uint8), "L")


def layer_over(base, color, alpha):
    """Composite a flat colour through an alpha mask (float array 0..1)."""
    rgb = np.array(base, dtype=np.float64)
    c = np.array(mklogo.rgb(color) * 255 if isinstance(color, str) else color, dtype=np.float64)
    a = np.clip(alpha, 0, 1)[..., None]
    return Image.fromarray((rgb * (1 - a) + c * a + 0.5).astype(np.uint8), "RGB")


def render_alive(w, h):
    """A mock of the boot animation's later beat, for the docs: the backdrop's
    lights up around the logo, the pane's rim lit, the progress line running.
    Illustrative only; the app draws the real thing (bootos.go)."""
    img = Image.new("RGB", (w, h), BACKDROP_BASE)
    for ox, oy, size, peak, color, shape, angle in BLOBS:
        d = int(size * h)
        tex = blob_texture(shape).resize((d, d), Image.BILINEAR).rotate(-angle, resample=Image.BILINEAR)
        mask = Image.new("L", (w, h), 0)
        mask.paste(tex, (int(ox * w - d / 2), int(oy * h - d / 2)))
        img = layer_over(img, color, np.asarray(mask, dtype=np.float64) / 255 * peak / 255)
    ys, xs = np.mgrid[0:h, 0:w]
    nx, ny = (xs + 0.5) / w * 2 - 1, (ys + 0.5) / h * 2 - 1
    r = np.sqrt(nx * nx + ny * ny) / math.sqrt(2)
    t = np.clip((r - 0.35) / 0.65, 0, 1)
    img = layer_over(img, (0, 0, 0), (t * t * (3 - 2 * t)) ** 1.2 * 0.92)

    font = inter(word_px(h))
    l = place(w, h, line_box(font))
    x, y, side, _ = l["sprite"]
    cx, cy = x + side / 2, y + side / 2
    # The halo the logo throws on the light behind it.
    for dx, dy, color, a in ((-0.12, -0.10, mklogo.CYAN_LIGHT, 0.30), (0.12, 0.12, mklogo.VIOLET_LIGHT, 0.26)):
        g = np.exp(-(((xs - cx - dx * side) / (0.62 * side)) ** 2 + ((ys - cy - dy * side) / (0.62 * side)) ** 2))
        img = layer_over(img, color, g * a)
    paste_logo(img, l["sprite"])
    # The rim after the light has run round it: a soft white line over the pane.
    inset = side * 0.165
    rim = Image.new("L", (w, h), 0)
    ImageDraw.Draw(rim).rounded_rectangle(
        (x + inset, y + inset, x + side - inset, y + side - inset),
        radius=0.23 * (side - 2 * inset), outline=255, width=max(2, round(side * 0.014)))
    rim = rim.filter(ImageFilter.GaussianBlur(1.6))
    img = layer_over(img, "#FFFFFF", np.asarray(rim, dtype=np.float64) / 255 * 0.55)
    draw_wordmark(img, font, w / 2, l["word_top"])

    # The progress line: the glass track, the accent fill with its glowing head,
    # and the step running now under it.
    tx, ty, tw, th = l["track"]
    d = ImageDraw.Draw(img, "RGBA")
    d.rounded_rectangle((tx, ty, tx + tw - 1, ty + th - 1), radius=th / 2, fill=(255, 255, 255, 34))
    fw = int(tw * 0.62)
    d.rounded_rectangle((tx, ty, tx + fw - 1, ty + th - 1), radius=th / 2, fill=mklogo.CYAN)
    hx, hy = tx + fw, ty + th / 2
    d.ellipse((hx - th - 4, hy - th - 4, hx + th + 4, hy + th + 4), fill=(34, 211, 238, 70))
    d.ellipse((hx - th / 2 - 1, hy - th / 2 - 1, hx + th / 2 + 1, hy + th / 2 + 1), fill=(255, 255, 255, 230))
    small = inter(max(12, go_round(h * 0.023)))
    status = "Checking the SD card"
    d.text((w / 2 - small.getlength(status) / 2, l["status_y"]), status, font=small, fill=(150, 160, 178, 255), anchor="la")
    return img


def contact_sheet(frames):
    """The boot logos and the mock side by side at half size, captioned."""
    pad, gap, panel_h = 24, 12, 360
    label = inter(15)
    panels = []
    for img, caption in frames:
        w = round(img.width * panel_h / img.height)
        panels.append((img.resize((w, panel_h), Image.LANCZOS), caption))
    width = pad + sum(p.width + pad for p, _ in panels)
    height = pad + panel_h + gap + 20 + pad
    sheet = Image.new("RGB", (width, height), (26, 29, 36))
    d = ImageDraw.Draw(sheet)
    x = pad
    for p, caption in panels:
        sheet.paste(p, (x, pad))
        d.rectangle((x - 1, pad - 1, x + p.width, pad + panel_h), outline=(52, 58, 70))
        d.text((x, pad + panel_h + gap), caption, font=label, fill=(201, 209, 222), anchor="la")
        x += p.width + pad
    return sheet


# ── Files ─────────────────────────────────────────────────────────────────

def bmp24(img):
    """A 24-bit uncompressed bottom-up BMP with the plain 40-byte header: the
    format every boot-logo tool for these devices accepts."""
    w, h = img.size
    row = (w * 3 + 3) & ~3
    px = np.asarray(img.convert("RGB"), dtype=np.uint8)[::-1, :, ::-1]  # bottom-up, BGR
    data = bytearray()
    pad = b"\0" * (row - w * 3)
    for line in px:
        data += line.tobytes() + pad
    header = struct.pack("<2sIHHI", b"BM", 54 + len(data), 0, 0, 54)
    info = struct.pack("<IiiHHIIiiII", 40, w, h, 1, 24, 0, len(data), 2835, 2835, 0, 0)
    return header + info + bytes(data)


def png_bytes(img):
    buf = io.BytesIO()
    img.save(buf, "PNG", optimize=True)
    return buf.getvalue()


def build():
    """Every file, as {path relative to the repo: bytes}, plus the sheet."""
    files, frames = {}, []
    for stem, w, h, devices in TARGETS:
        img = render_bootlogo(w, h)
        files["%s/%s.bmp" % (OUT, stem)] = bmp24(img)
        files["%s/%s.png" % (OUT, stem)] = png_bytes(img)
        frames.append((img, "%dx%d - %s" % (w, h, devices)))
    alive = render_alive(1280, 720)
    frames.insert(1, (alive, "The logo comes alive (mock of the animation)"))
    return files, contact_sheet(frames)


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--preview", metavar="PNG", help="write only the contact sheet, to this path")
    ap.add_argument("--root", default=ROOT, help="repository root (default: this script's parent)")
    args = ap.parse_args()
    selfcheck()
    files, sheet = build()
    if args.preview:
        sheet.save(args.preview)
        print("preview: %s" % args.preview)
        return 0
    files["%s/jukahub-boot-sheet.png" % OUT] = png_bytes(sheet)
    files["docs/assets/jukahub-boot-sheet.png"] = files["%s/jukahub-boot-sheet.png" % OUT]
    changed = 0
    for rel, data in files.items():
        path = os.path.join(args.root, *rel.split("/"))
        if os.path.exists(path):
            with open(path, "rb") as fh:
                if fh.read() == data:
                    continue
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as fh:
            fh.write(data)
        changed += 1
        print("wrote %-46s %8d bytes" % (rel, len(data)))
    if not changed:
        print("all boot logo files are up to date")
    return 0


if __name__ == "__main__":
    sys.exit(main())
