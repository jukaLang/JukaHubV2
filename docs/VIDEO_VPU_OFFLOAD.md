# Video: hardware decode (Cedar / sunxi VPU) offload on the A133P

Desk investigation of the open modernization item ("ffmpeg hardware decode/VPU
offload on the A133P, cedar/sunxi path"). Compiled from public sources, not from
a device image — every claim below is tagged with how firmly the sources support
it, because the conclusion is "viable, but only through the vendor CedarX route,
and only after an on-device probe", and that hinges on facts we have not yet
confirmed on real hardware.

## What the A133P's VPU is

The A133 pairs four Cortex-A53 cores and the PowerVR GE8300 GPU (the part the
render-pipeline work in [MODERNIZATION.md](../player/MODERNIZATION.md) covers)
with an Allwinner "Cedar" video engine (the CedarX generation). Firmly sourced
spec points:

| Codec | Hardware decode? | Claims from the sources |
|---|---|---|
| H.264/AVC | Yes | Datasheet: Baseline/Main/High L5.1 up to 4K@30. TrimUI's own firmware notes list H.264 1080p@60 |
| H.265/HEVC | Yes | Datasheet: Main L5.0 up to 4K@30 (firmware notes say MP/L5.2 4K@30 — sources disagree on profile/level) |
| VP9 | Yes, weakly | Datasheet granular entry: **720p@30 only** — not a 1080p VP9 engine |
| Others (VP8, MPEG-1/2/4, VC-1/WMV9, MJPEG…) | Firmware claims yes | TinaPlayer release notes: mostly 1080p@60, VC-1 1080p@30 — a firmware/player claim, not a silicon statement |

Sources: the A133 datasheet PDF is indexed on
[linux-sunxi](https://linux-sunxi.org/images/8/8f/A133_Datasheet_V1.1.pdf) and
[Allwinner's brief](https://www.allwinnertech.com/uploads/pdf/20210803142431e6.pdf);
the codec list is from the
[TrimUI firmware release notes](https://github.com/trimui/firmware_smartpro/releases),
which state the stock media player does hardware decoding "based on TinaPlayer".

**Model caveat:** this table applies to the *A133P devices* — the original
Smart Pro (TG5040) and the Brick family. Per
[Notebookcheck](https://www.notebookcheck.net/TrimUI-Smart-Pro-S-debuts-as-new-retro-handheld-boasting-up-to-2-5x-faster-GPU-performance-than-older-model.1161227.0.html)
the Smart Pro S moved to an Allwinner A523, a different SoC generation with a
different VPU. Do not extrapolate these numbers to it; the device model
classification in [device.go](../player/device.go) is the gate.

## What this changes in the player

Today the decoder is a shell-out ([videoplayer.go](../player/videoplayer.go)):
`ffmpegDecoderArgs` runs ffmpeg with `-f rawvideo -pix_fmt yuv420p` and pipes
planar frames into Go at the decode target size; scaling happens *inside* ffmpeg
(`-vf scale=...`) when the source is larger than the target, which is already
the right shape — CPU cost is decode plus the pipe. The audio path is a second
ffmpeg pipe (`-f s16le`), paced by us. Neither process ever touches hardware
decode: rawvideo output forces full software decode upstream of the pipe.

So "offload" is not a matter of adding `-hwaccel` flags — **upstream ffmpeg has
no Cedar accel backend** (see below). It means replacing the decode step of that
pipe with something that talks to the VPU and still emits `yuv420p` bytes to the
same consumer.

## Route 1 — vendor CedarX (the viable one)

- The vendor stack lives behind the `cedar_dev` character device (`/dev/cedar_dev`)
  with ION/CMA-backed buffers. That interface is the standard Allwinner vendor
  pattern ([allwinner-zh cedar_ve.c](https://github.com/allwinner-zh/linux-3.4-sunxi/blob/master/drivers/media/cedar-ve/cedar_ve.c);
  community cedar tools explicitly document needing `/dev/cedar_dev` plus
  `libVE.so`/`libMemAdapter.so` — [carroarmato0/allwinner-cedar-tools](https://github.com/carroarmato0/allwinner-cedar-tools)).
  *Tag: ecosystem-level firm, exact TG5040 image contents unverified* — the one
  fetched TrimUI-adjacent cedar-tools repo targets an H618 board, so do not
  treat it as a manifest of this firmware.
- Userspace API shape (from the public
  [CedarX source](https://github.com/allwinner-zh/media-codec/blob/master/sunxi-cedarx/SOURCE/vdecoder/vdecoder.c)):
  `CreateVideoDecoder` → `RequestVideoStreamBuffer` / `SubmitVideoStreamData` →
  `DecodeVideoStream` → frame buffers out. It is compressed-stream-in /
  decoded-surface-out with an explicit buffer lifecycle — a real rework of the
  decoder loop, not a decoder function swap. Frame buffers are not ordinary
  linear malloc memory by default; the bridge has to copy/convert out of ION
  memory (that copy is a fraction of what full software decode cost).
- The directly relevant precedent is an A133-specific bundle of prebuilt CedarX
  libraries and demos (`tplayerdemo` etc.) from the TinaSDK
  ([Nopiskl/Allwinner_Drivers, A133 multimedia](https://github.com/Nopiskl/Allwinner_Drivers/tree/main/A133_multimedia_drivers/cedar_libs)).
  It documents `LD_LIBRARY_PATH` usage, not a stable public ABI, so treat it as
  a probe target, not a dependency to ship.
- Redistributability is the other reason for the bridge shape: the CedarX libs
  are Allwinner blobs. We should not pack them into our package; anything we
  ship must `dlopen` libraries already on the firmware image and fall back to
  the current ffmpeg pipe when they are missing (same posture the MPV-style
  played-before conclusions took elsewhere in the repo: no speculative code
  against unproven vendor libraries).

**Recommended architecture if the probe passes:** a small C helper (built for
aarch64 on the community TrimUI toolchain the release build already uses)
`dlopen`s `libcedarx`/`libve`-family libraries, decodes one input file, and
writes `yuv420p` to stdout at the same contract `ffmpegDecoderArgs` produces.
The Go side then keeps its pipe/frame-pacing/drop logic unchanged and only the
helper binary is new — and a runtime probe can even keep using the vendor's own
`tplayerdemo` first to confirm the VPU works before we build anything.

## Route 2 — mainline Cedrus (not viable on the stock image here)

Cedrus is the upstream V4L2 **stateless** decoder (`drivers/staging/media/sunxi/cedrus`),
documented by [Bootlin](https://bootlin.com/blog/allwinner-vpu-main-goals-delivery/)
for older Allwinner parts (A33/H3-class; MPEG-2, H.264, HEVC). A133 is not among
the supported platforms, and the reviewed
[mainline decoding guides](https://pine64.org/documentation/General/Mainline_Hardware_Decoding/)
note that upstream ffmpeg lacks the V4L2-Request-API patches (a fork is needed
even where the kernel does support it). So: mainline-kernel-level project, and
**the stock TrimUI vendor kernel does not provide this node just because its
version number says "5.x"**. Paths only open up with a ported kernel; separate
track entirely.

## What the on-device probe has to answer

Everything the desk pass could not verify, in probe order:

1. Does the stock image expose `/dev/cedar_dev` (and `/dev/ion`)? A visible
   node alone is not success — see the
   [CMA/ION allocation failure](https://github.com/aodzip/cedar/issues/4)
   precedent where devices had the node but no allocatable CMA memory.
2. Which libraries does the firmware ship, and what does the stock TinaPlayer
   binary link against (`ldd`): `libcedarx.so`? `libvdecoder.so`? `libVE.so`,
   `libMemAdapter.so`? This is the honest manifest the public refs could not
   give us.
3. Does the vendor `tplayerdemo` from the A133 bundle run, decode a real
   H.264/HEVC file, and keep A/V synced — i.e. is the VPU usable at all from
   our native process's environment?
4. Frame layout out of the vdecoder (stride, plane order, YUV format variants)
   — the stdin/stdout helper walks straight into this.
5. CPU headroom of the pipe stage afterwards: the win we are buying is decode
   cycles on the four A53s; measure with the existing device diagnostics
   (`LogDeviceDiagnostics`, memory monitor) rather than assuming.

## Bottom line

- Hardware decode exists on the silicon and is advertised by the firmware — the
  current ffmpeg rawvideo pipe simply bypasses all of it.
- Mainline Cedrus is off the table without a kernel port; correct to treat
  cedar/sunxi as a *vendor* stack problem.
- The realistic integration is the CedarX→stdout bridge above behind
  `dlopen`-time detection with automatic ffmpeg fallback, gated on device model
  (TG5040/Brick family, not the A523-based Smart Pro S).
- Nothing is shippable until the five probe questions are answered on a real
  TG5040/Brick SD image; at that point this document's assumptions get replaced
  by measured facts.

## Sources

- A133 datasheet / brief: linux-sunxi.org, allwinnertech.com (PDFs, indexed content)
- TrimUI firmware release notes (TinaPlayer codec list): github.com/trimui/firmware_smartpro/releases
- Smart Pro / Smart Pro S SoC reports: notebookcheck.net (two links in text)
- CedarX source & API: github.com/allwinner-zh/media-codec
- Vendor `cedar_dev` driver: github.com/allwinner-zh/linux-3.4-sunxi (cedar_ve.c)
- Community cedar tools interface: github.com/carroarmato0/allwinner-cedar-tools
- A133 CedarX bundle + demos: github.com/Nopiskl/Allwinner_Drivers (A133_multimedia_drivers)
- Cedrus mainline status & ffmpeg caveat: bootlin.com, pine64.org

*Also on record: the older H3-era "ffmpeg-cedar"/cedardevPlugin experiments
could not be verified against a primary repo during this pass; no evidence was
found that any of them target the A133/CedarX generation.*
