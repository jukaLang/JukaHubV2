#!/usr/bin/env python3
"""Encode a single JukaHub OS frame for plastron's Brick splash player."""

from pathlib import Path
import struct
import sys
import zlib

from PIL import Image, ImageDraw, ImageFont


HERE = Path(__file__).resolve().parent
FONT = HERE.parents[1] / "player" / "Inter-Regular.ttf"
W, H = 1024, 768


def make_frame() -> Image.Image:
    im = Image.new("RGB", (W, H), (15, 18, 30))
    draw = ImageDraw.Draw(im)
    draw.rounded_rectangle((132, 253, 892, 515), radius=32, fill=(27, 31, 49))
    draw.rounded_rectangle((132, 253, 142, 515), radius=5, fill=(107, 161, 255))
    title = ImageFont.truetype(str(FONT), 88)
    subtitle = ImageFont.truetype(str(FONT), 34)
    draw.text((181, 298), "JukaHub OS", font=title, fill=(242, 246, 255))
    draw.text((186, 427), "Starting your handheld", font=subtitle, fill=(158, 174, 202))
    return im


def encode(im: Image.Image) -> bytes:
    rgb = im.tobytes()
    stride = W * 4
    bgrx = bytearray(W * H * 4)
    for i in range(W * H):
        r, g, b = rgb[i * 3:i * 3 + 3]
        bgrx[i * 4:i * 4 + 4] = bytes((b, g, r, 255))
    filtered = bytearray(len(bgrx))
    for row_start in range(0, len(bgrx), stride):
        filtered[row_start:row_start + 4] = bgrx[row_start:row_start + 4]
        for i in range(4, stride):
            filtered[row_start + i] = (bgrx[row_start + i] - bgrx[row_start + i - 4]) & 255
    data = zlib.compress(filtered, 9)
    header = struct.pack("<4sHHHHIHHI", b"TSPZ", W, H, 1, 60, 16, 0, H, len(data))
    return header + data


def main() -> None:
    im = make_frame()
    out = HERE / "frames-brick.bin"
    out.write_bytes(encode(im))
    if "--preview" in sys.argv:
        im.save(HERE / "splash-preview.png")
    print(f"{out}: {out.stat().st_size} bytes")


if __name__ == "__main__":
    main()
