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
| `make check` | everything a pull request must pass locally |
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

## Linux and macOS (development)

```bash
sudo apt install libsdl2-dev libsdl2-image-dev libsdl2-ttf-dev
cd player
go build -o JukaHub .
./JukaHub
```

On macOS use Homebrew (`sdl2`, `sdl2_image`, `sdl2_ttf`) and set `CGO_CFLAGS` /
`CGO_LDFLAGS` to the `brew --prefix sdl2*/include|lib` paths, as the Windows CI
job in [`.github/workflows/docker-image.yml`](../.github/workflows/docker-image.yml)
does for MinGW.

## TrimUI Smart Pro (linux/arm64)

A plain cross-build is not enough: the handheld's stock OS ships glibc 2.23 and
SDL2 2.26.1, so the binary must be linked against the device SDK sysroot rather
than your host libraries. Use the packaging script:

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

### Remote Play helpers

PS Remote Play streams through two native binaries built from chiaki-ng's
`libchiaki`. They are separate from the Go build because they need the TG5050
SDK and a pinned upstream checkout:

```bash
cd player
SDK_ROOT=/opt/sdk-tg5050 ./native/build-helpers.sh
```

This installs `player/bin/chiaki-stream` and `player/bin/chiaki-regist`. Both
packaging scripts copy them into `Apps/JukaHub/bin/` (and
`native/libs/brick-stock/` into `Apps/JukaHub/libs/brick-stock/`) when they are
present; a build without them still runs and the panel reports the helper as
missing. See [`player/native/README.md`](../player/native/README.md) for the
session-file contract and the Brick Pro runtime note.

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
| [`.github/workflows/build-sdcard.yml`](../.github/workflows/build-sdcard.yml) | push to `main` touching `player/**`, pull requests, manual | `JukaHub-SDCARD` artifact (the SD-card package), built in the community TrimUI toolchain image |
| [`.github/workflows/docker-image.yml`](../.github/workflows/docker-image.yml) | push / pull request to `main` | `JukaHub-<timestamp>` release with `JukaHub-Trimui.zip` and `JukaHub-Windows.zip`, each carrying `release-info.json` (`tag` + RFC3339 `date`). The release is created as a draft and published only after both archives are attached, so a download link never points at an empty release. Pull-request runs stop at the draft. |
| [`.github/workflows/release.yml`](../.github/workflows/release.yml) | `v*` tag | runs `release.sh` on ubuntu-latest with `gcc-aarch64-linux-gnu` and attaches `JukaHub-trimui-smart-pro-linux-arm64.tar.gz`, `JukaHub-win-x64.zip`, `manifest.json` and `SHA256SUMS` to the GitHub release, then publishes it once the draft upload is complete |

Both the updater and the version line in the UI read `release-info.json`, so a
release built by CI advertises itself correctly without any extra steps.

## Tests

```bash
cd player
go vet ./...
go test ./...
```

Rendering changes are expected to come with pixel or geometry tests; see
[CONTRIBUTING.md](../CONTRIBUTING.md).
