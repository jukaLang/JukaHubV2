# Building and packaging

JukaHub is a Go + SDL2 program, so every build needs CGO and SDL2 headers. The
Windows wrapper installs both for you; on other hosts you install them yourself.

Requirements are in [`player/go.mod`](../player/go.mod) (Go **1.26+**, toolchain
pinned to 1.27.3). Dependencies are fetched with `go mod download` on first build,
so the first build needs network access.

| Task | Command |
|------|---------|
| Windows desktop build | `cd player && ./build.bat` |
| Linux/macOS development build | `cd player && go build -o JukaHub .` |
| TrimUI SD-card package | `cd player && SYSROOT=<sdk> ./package-sdcard.sh` |
| Release artifacts (all platforms) | `cd player && ./release.sh` |

## From the repository root

[`Makefile`](../Makefile) names the commands the docs and CI use, so you do not
have to remember which of them to run on which platform. `make` on its own
lists every target.

| Target | Effect |
|--------|--------|
| `make dev` | build and launch (Windows: bootstraps the toolchain first) |
| `make build` | build without launching |
| `make test` | player tests; `make test-web` / `make e2e` for the builder |
| `make lint` | gofmt, `go vet`, builder syntax, config JSON |
| `make bench` | cold-boot benchmark against the 2 s device budget |
| `make check` | what CI checks on every push, locally (staticcheck aside) |
| `make release` | the same packaging as `player/release.sh` |

On Windows the player targets are thin wrappers around `player\build.bat`, so
the sections below still apply to what actually runs.

---

## Windows

```bash
cd player
./build.bat
```

`build.bat` is self-contained: it looks for Go 1.27+ and GCC, installs a private
copy of either into `%LOCALAPPDATA%\JukaHub\tools` when missing, downloads the
SDL2 / SDL2_image / SDL2_ttf MinGW development packages into
`player/.sdl2/x86_64-w64-mingw32/`, builds `JukaHub.exe`, copies the three SDL
DLLs next to it, and then launches the app.

| Mode | Effect |
|------|--------|
| `build.bat` | build, then launch |
| `build.bat nolaunch` | build only (what CI and you usually want) |
| `build.bat nobuild` | set up the toolchain and SDL2, then stop |
| `build.bat diag` | print the detected environment and paths |
| `build.bat clean` | remove build outputs and the local `.sdl2/` copy |

If `cmd` reports `The system cannot find the batch label specified - ensure_gcc`,
the copy of `build.bat` lost its CRLF line endings: `cmd.exe` builds its label
table from the first chunk of the file, so an LF-only script cannot reach labels
further down. The repository pins `*.bat` to CRLF in `.gitattributes`, so
restoring the file (`git checkout -- player/build.bat`) repairs a local copy.

Pinned versions live at the top of the script (`SDL2_VER`, `IMG_VER`,
`TTF_VER`, `GO_FALLBACK`, `W64DK_VER`). Set `SDL2_DIR` to use an SDL2
installation you already have instead of the managed copy.

Manual build, for a shell that already has MinGW-w64 and SDL2:

```bash
cd player
export CGO_ENABLED=1
export CGO_CFLAGS="-I$PWD/.sdl2/x86_64-w64-mingw32/include -I$PWD/.sdl2/x86_64-w64-mingw32/include/SDL2"
export CGO_LDFLAGS="-L$PWD/.sdl2/x86_64-w64-mingw32/lib"
go build -o JukaHub.exe .
```

The SDL2 DLLs must sit next to `JukaHub.exe` to run.

### The executable icon

Windows reads a program's icon from a resource compiled into the `.exe`, not
from a file next to it, so JukaHub's own icon is linked in from these files in
`player/`:

| File | Role |
|------|------|
| `JukaHub.ico` | multi-size icon: 16-32 px are the simplified mark drawn for their pixel grid, 40-256 px the full logo |
| `JukaHub.rc` | resource script that attaches the icon to the binary |
| `rsrc_windows_amd64.syso` | compiled resource, linked automatically by Go |
| `JukaPlayer.png` | the 300 px logo: the running window's icon (embedded with `go:embed`) and the Apps-menu icon of a TrimUI release folder |

These files, and every other copy of the logo (the SD-card package's
`trimui/Apps/JukaHub/icon.png`, the web builder's favicons, the README image),
are rendered by `branding/mklogo.py` from one construction; see
[branding/README.md](../branding/README.md). To change the logo, edit that
script and run `python3 branding/mklogo.py` (Python 3 with numpy and Pillow):
it rewrites every asset that changed, the `.syso` included, so no `windres` is
needed. Everything it writes is committed, so a plain `go build` (and the
Windows CI job) gets the icon with no extra flags. `build.bat` still recompiles
the `.syso` with `windres` on every build, `./mkwinres.sh` does the same by
hand, and `build.bat diag` reports whether the compiled resource is present.
Do not regenerate the `.ico` from `JukaPlayer.png`: a downscale loses the small
sizes' hinting.

The resource is per architecture: a `windows/arm64` build needs its own
`rsrc_windows_arm64.syso` or it falls back to the generic icon.

## Linux and macOS (development)

```bash
sudo apt install libsdl2-dev libsdl2-image-dev libsdl2-ttf-dev
cd player
go build -o JukaHub .
./JukaHub
```

On macOS install the same three libraries with Homebrew
(`brew install sdl2 sdl2_image sdl2_ttf`); the SDL bindings find them through
`pkg-config`. No CI job builds for macOS: the shipped builds are TrimUI and
Windows.

## TrimUI Smart Pro (linux/arm64)

A plain cross-build is not enough: the handheld's stock OS ships an older glibc
than current distributions (2.33 since firmware 1.0.4) and SDL2 2.26.1, so the
binary must be linked against the device SDK sysroot rather than your host
libraries. Use the packaging script:

```bash
cd player
SYSROOT=/usr/local/aarch64-linux-gnu-7.5.0-linaro/sysroot ./package-sdcard.sh
```

It builds `linux/arm64` with CGO, stages the app files from
[`player/trimui/Apps/JukaHub/`](../player/trimui/Apps/JukaHub), fetches the
static ARM64 `yt-dlp` and the ffmpeg/ffprobe builds into `required/`,
strips them, and writes:

- `dist/JukaHub-SDCARD/Apps/JukaHub/` — copy-ready folder
- `dist/JukaHub-SDCARD.zip` — the same folder as an archive
- `dist/build-summary.txt` — version, linked SONAMEs, file list

BtbN's bundle also carries `ffplay`; it is left out of the package because
nothing runs it (playback decodes with `ffmpeg`) and it needs X11, which the
handheld does not have.

| Environment variable | Default | Effect |
|----------------------|---------|--------|
| `SYSROOT` | linaro toolchain path | device SDK sysroot |
| `WITH_YTDLP` | `1` | download the static ARM64 `yt-dlp` |
| `WITH_FFMPEG` | `1` | download and strip ffmpeg/ffprobe |
| `FFMPEG_BUILD` | `ffmpeg-master-latest-linuxarm64-gpl` | BtbN release asset |
| `YTDLP_URL`, `FFMPEG_URL` | upstream latest | override the download sources |

Device packaging notes, written for the end user, are in
[`player/trimui/README.md`](../player/trimui/README.md); the on-device
config is generated by `player/trimui/make-config.ps1`.

### The release binary, with Docker

The release job compiles the handheld binary with
[`.github/scripts/trimui-build.sh`](../.github/scripts/trimui-build.sh), which
runs the build inside the pinned community toolchain image (the device SDK
sysroot, the Linaro aarch64 gcc and SDL2 2.26.1) with no network, the
checkout, your Go toolchain and Go's module and build caches mounted at their
own paths. On Linux or WSL 2 with Docker and Go, the same commands build the
same binary, and later builds reuse your Go build cache:

```bash
export TOOLCHAIN_IMAGE=<the TOOLCHAIN_IMAGE value in .github/workflows/docker-image.yml>
sh .github/scripts/trimui-build.sh go JukaHub-Trimui /tmp/trimui-go-build.log
sh .github/scripts/trimui-build.sh spatial JukaHub-Trimui   # optional: lib/jukahub-spatial.so
sh .github/scripts/trimui-build.sh check JukaHub-Trimui/JukaHub
```

The output folder must be inside the checkout, the only folder the container
sees. `check` refuses a binary the handhelds could not start: one that is not
AArch64, asks for another loader than `/lib/ld-linux-aarch64.so.1`, does not
link the firmware's SDL2, or needs a glibc symbol newer than 2.33.

### Remote Play helpers

PS Remote Play streams through two native binaries built from chiaki-ng's
`libchiaki`, `chiaki-stream` and `chiaki-regist`. They are separate from the Go
build because they need chiaki-ng's sources (pinned to a commit) and an
aarch64 build host or SDK. [`player/native/build-helpers.sh`](../player/native/build-helpers.sh)
builds them either way and installs them into `player/native/bin/`; both
packaging scripts and the release workflows copy them into `Apps/JukaHub/bin/`
from there (and `native/libs/brick-stock/` into `Apps/JukaHub/libs/brick-stock/`
when it exists). A package without them still runs; the Remote Play panel then
downloads them itself from the newest release that carries
`JukaHub-RemotePlay-linux-arm64.zip`, the two helpers on their own
([REMOTE_PLAY.md → Installing the helpers](REMOTE_PLAY.md#installing-the-helpers)).

The release builds them **natively on an arm64 runner** inside an Ubuntu 20.04
root ([`.github/workflows/remote-play-helpers.yml`](../.github/workflows/remote-play-helpers.yml)),
and you can do the same on any aarch64 Linux machine with glibc 2.31 or older
(a 20.04 arm64 container works):

```bash
cd player
JUKAHUB_HELPERS_NATIVE=1 ./native/build-helpers.sh
```

That mode builds FFmpeg 6.1 (only the H.264 and HEVC decoders and swscale),
opus, json-c and libevent from pinned, checksum-verified sources into
`DEPS_PREFIX` (default `/opt/jukahub-deps`) and links them statically, together with the
distribution's static OpenSSL and chiaki-ng's bundled curl (the script points
chiaki-lib at curl's static target, `libcurl_static`, and stops if the configure
turns shared libraries back on). chiaki-ng and miniupnpc are fetched by full
commit id and checked against it. Only the C library and SDL2 stay dynamic: the handheld's firmware
SDL2 is the one that drives its display, so the helpers link against SDL2
2.26.1, the version the firmware ships, built only for its headers and soname.
The binaries are staged in the build directory and copied into
`player/native/bin/` only after the checks pass: the script refuses the result
if anything else is `NEEDED`, if a symbol
needs a glibc newer than `GLIBC_MAX` (2.31; the Smart Pro has 2.33 since
firmware 1.0.4, and a binary built against a newer glibc does not start there),
or if either binary fails to start, and it writes `player/native/bin/BUILDINFO.txt`
with the versions, glibc ceiling, linked libraries and SHA-256 of both files.
The Python tools (CMake and the grpcio-tools protoc nanopb uses) are pinned to
exact versions in
[`player/native/helper-build-requirements.txt`](../player/native/helper-build-requirements.txt)
and installed as wheels only:

```bash
python3 -m pip install --only-binary=:all: -r native/helper-build-requirements.txt
```

The older cross build against the TG5050 SDK still works and links the SDK's
FFmpeg/OpenSSL dynamically:

```bash
cd player
SDK_ROOT=/opt/sdk-tg5050 ./native/build-helpers.sh
```

| Environment variable | Default | Effect |
|----------------------|---------|--------|
| `JUKAHUB_HELPERS_NATIVE` | `0` | `1` = native aarch64 build with static dependencies |
| `DEPS_PREFIX`, `DEPS_SRC` | `/opt/jukahub-deps`, `/tmp/jukahub-deps-src` | native mode's dependency prefix and download cache |
| `GLIBC_MAX` | `2.31` | newest glibc symbol version the binaries may need |
| `SDK_ROOT` | `/opt/sdk-tg5050` | TG5050 SDK for the cross build |
| `CHIAKI_COMMIT`, `CHIAKI_SOURCE`, `BUILD_DIR` | pinned commit, `/tmp/...` | chiaki-ng checkout and build directory |
| `JOBS` | number of CPUs | parallel build jobs |
| `LOG_DIR` | `/tmp/jukahub-helper-logs` | one log per build step; the console shows one line per step, and a failed step prints the end of its log |

See [`player/native/README.md`](../player/native/README.md) for the
session-file contract and the Brick Pro runtime note.

### AndroidRT and the Box64 pack

Two more native pieces are built beside the helpers by
[`player/native/build-runtimes.sh`](../player/native/build-runtimes.sh). Each is
one zip holding one folder with `BUILDINFO.txt` (sources, compiler, glibc
ceiling, SHA-256 of every binary, what was checked) and a `SHA256SUMS` that
lists every other file; JukaHub installs such a package only when every file is
listed and matches.

| Package | Folder | What it holds | Where it goes |
|---------|--------|---------------|---------------|
| `JukaHub-AndroidRT-linux-arm64.zip` | `androidrt/` | AndroidRT, the Android-app runtime ([`player/androidrt`](../player/androidrt/README.md)): `loader`, `launch.sh`, `surface-probe`, `props.txt.example` | every TrimUI archive's `Apps/JukaHub/androidrt/`, and the release on its own |
| `JukaHub-Wine-linux-arm64.zip` | `wine-runtime/` | Box64 for the Windows-app runtime: `box64/box64`, the x86_64 libraries its install puts beside it (`box64/x64lib/`), its default settings (`box64/box64.box64rc`) and licence, and `wine.json`, the Wine build to download | the release only; JukaHub installs it when Windows apps are set up |

```bash
cd player
sh native/build-runtimes.sh androidrt    # an aarch64 host, or CROSS=aarch64-linux-gnu- (the loader is then not started)
sh native/build-runtimes.sh wine         # an aarch64 host only
sh native/build-runtimes.sh verify native/bin/JukaHub-Wine-linux-arm64.zip
```

Both land in `player/native/bin/`, where `release.sh` and `package-sdcard.sh`
pick them up. The release builds them natively in the same Ubuntu 20.04 arm64
root as the helpers and refuses a binary that is not AArch64, asks for another
loader, needs anything but the C library, or needs a glibc symbol newer than
2.31 (or a private one). The AndroidRT loader must also keep its TLS slot at
offset 0 and answer `loader --version`; Box64 must start (`box64 --version`)
and name its pinned version. Box64 is fetched by the full id of its pinned
release commit (v0.4.4) and built as a generic ARM64 build (`-DARM_DYNAREC=ON
-DCMAKE_BUILD_TYPE=RelWithDebInfo`; no BOX32, because Wine's WoW64 build runs
32-bit Windows programs under Box64 alone), with gcc 10 from Ubuntu 20.04's
universe, its DWARF stripped. `wine.json` names Kron4ek's Wine 10.0 amd64 WoW64
build with its pinned SHA-256 and the size of the verified download; Wine 10.0
is pinned on purpose, because Wine 11.x aborts on the handheld's 4.9 kernel.
Two more checks only inform the run's summary: AndroidRT's host smoke suite run
on arm64, and `box64 wine --version` with that Wine.

| Environment variable | Default | Effect |
|----------------------|---------|--------|
| `OUT_DIR` | `player/native/bin` | where the zips go |
| `WORK_DIR`, `LOG_DIR` | `/tmp/jukahub-runtime-build`, `/tmp/jukahub-runtime-logs` | build folders, and one log per step (the console shows one line per step; a failed step prints the end of its log) |
| `GLIBC_MAX` | `2.31` | newest glibc symbol version a binary may need |
| `CROSS` | unset | cross-build AndroidRT (passed on to `build-tsp.sh`) |
| `BOX64_CC` | `gcc-10` when installed, else `cc` | Box64's compiler |
| `JOBS` | number of CPUs | parallel build jobs |

## Release artifacts

```bash
cd player
./release.sh
```

`release.sh` reads `Version` from `player/jukaconfig.json`, stamps the build
time into the binary with `-ldflags "-X main.buildDate=… -X main.buildTime=…"`,
and writes to `dist/`:

- `JukaHub-win-x64.zip` — the Windows executable
- `JukaHub-<device>-linux-arm64.tar.gz` for each of `trimui-smart-pro`,
  `trimui-smart-pro-s`, `trimui-brick`, `trimui-brick-pro`, `trimui-hammer`.
  The Brick family runs the compact 4:3 UI canvas; the Smart Pro family keeps
  the 1280×720 canvas.
- `JukaHub-RemotePlay-linux-arm64.zip`, `JukaHub-AndroidRT-linux-arm64.zip`
  and `JukaHub-Wine-linux-arm64.zip` when `native/bin` holds the helpers and
  verified runtime packages (each TrimUI archive then also carries `bin/` and
  `androidrt/`)
- `manifest.json` — `schema`, `product`, `version`, `channel`, `published_at`
  and one `targets[]` entry per asset (`device`, `os`, `arch`, `asset`,
  `sha256`, `size`, `min_firmware`, `files`)
- `SHA256SUMS`

The compile stamp is what the updater compares: release tags are timestamps, so
"is there something newer" is decided by comparing the release's publication
time with the stamp inside the running binary, never by comparing version
numbers. See [Patch and package format](PACKAGES.md) for what consumes it.

## Continuous integration

| Workflow | Trigger | Output |
|----------|---------|--------|
| [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) | push to `main`, pull requests | two jobs: the web builder checks (workflow pins, syntax, jsdom tests, ESLint) and the player's `go vet`, staticcheck and `go test` |
| [`.github/workflows/weekly.yml`](../.github/workflows/weekly.yml) | Mondays, manual | the slow checks: race detector, `govulncheck`, cold-boot budget, the Chromium smoke test, the CI helper scripts', the game-audio plugin's and AndroidRT's host tests (the AndroidRT smoke suite, TEST-4), and a native Windows build with its tests |
| [`.github/workflows/build-sdcard.yml`](../.github/workflows/build-sdcard.yml) | manual only | `JukaHub-SDCARD` artifact (the SD-card package, Remote Play helpers and AndroidRT included), built in the community TrimUI toolchain image |
| [`.github/workflows/docker-image.yml`](../.github/workflows/docker-image.yml) | push / pull request to `main` | `JukaHub-<timestamp>` release with `JukaHub-Trimui.zip` (its `bin/` holds the Remote Play helpers, its `androidrt/` AndroidRT), `JukaHub-Windows.zip` and, when they were built, `JukaHub-RemotePlay-linux-arm64.zip` (the helpers alone: `bin/` plus `SHA256SUMS`, which a JukaHub without them downloads by itself), `JukaHub-AndroidRT-linux-arm64.zip` and `JukaHub-Wine-linux-arm64.zip` (the Box64 pack of the Windows-app runtime) — all three optional, so the release is published without them — the two app archives each carrying `release-info.json` (`tag` + RFC3339 `date`). The release is created as a draft and published only after both archives are attached, so a download link never points at an empty release. Pull-request runs stop at the draft. The Windows job and the TrimUI compile (`build-trimui`, the toolchain image under `docker run` through [`trimui-build.sh`](../.github/scripts/trimui-build.sh)) each start from the Go build cache an earlier push-to-main run of the same workflow left as an artifact, so a release normally compiles only the player; [`.github/scripts/go-build-cache.sh`](../.github/scripts/go-build-cache.sh) explains why it is not `actions/cache`. The TrimUI compile runs beside the Remote Play helpers, and a short packaging job waits for both. |
| [`.github/workflows/release.yml`](../.github/workflows/release.yml) | `v*` tag | runs `release.sh` (with the Remote Play helpers and the two runtime packages in `native/bin`) on ubuntu-latest with `gcc-aarch64-linux-gnu` and attaches `JukaHub-trimui-smart-pro-linux-arm64.tar.gz`, `JukaHub-win-x64.zip`, `manifest.json`, `SHA256SUMS` and (when they were built) `JukaHub-RemotePlay-linux-arm64.zip`, `JukaHub-AndroidRT-linux-arm64.zip` and `JukaHub-Wine-linux-arm64.zip` to the GitHub release, then publishes it once the draft upload is complete |
| [`.github/workflows/remote-play-helpers.yml`](../.github/workflows/remote-play-helpers.yml) | called by the three workflows above, or manual | `remote-play-helpers` artifact: `chiaki-stream`, `chiaki-regist` and `BUILDINFO.txt`, built by `build-helpers.sh` in native mode on an arm64 runner inside a debootstrapped Ubuntu 20.04 root; `androidrt-package` (`JukaHub-AndroidRT-linux-arm64.zip`) and `wine-runtime-package` (`JukaHub-Wine-linux-arm64.zip`), built by `build-runtimes.sh` in the same root, with AndroidRT's smoke suite on arm64 and Wine 10.0 under the new Box64 as advisory steps. Each part is reused from an earlier push-to-main run of the same calling workflow when nothing it is built from changed (the artifacts `remote-play-helpers-<hash>`, `androidrt-pack-<hash>` and `wine-runtime-pack-<hash>`, each hash over that part's sources, under the build cache's provenance rules). Each may fail without stopping anything, the others included: the callers run regardless and their packages then ship without that part, with a warning. |

Both the updater and the version line in the UI read `release-info.json`, so a
release built by CI advertises itself correctly without any extra steps.

## Tests

```bash
cd player
go vet ./...
go test ./...
```

Rendering, layout and feature flows get no tests (check them on screen); the
suite covers saved data, security, the updater and installers, and parsers of
outside files. See
[CONTRIBUTING.md](../CONTRIBUTING.md#tests-for-what-you-changed).
