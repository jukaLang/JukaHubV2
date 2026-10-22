# Remote Play helpers

The two binaries JukaHub's PS Remote Play client drives. They are the only
native code in the client; everything else (discovery, PS4 pairing, the panel,
the host store) is Go in the parent folder.

| File | Purpose |
|---|---|
| `chiaki-stream.c` | Reads a session file, runs chiaki-ng's libchiaki against the console, decodes H.264/HEVC with FFmpeg and draws with SDL2 |
| `chiaki-regist.c` | PS5 pairing handshake (PSN Account-ID + PIN), writes a result file |
| `CMakeLists.txt` | Adds both targets to a chiaki-ng build |
| `build-helpers.sh` | Fetches the pinned chiaki-ng commit, cross-compiles for TG5050 and installs into `../bin` |

PS4 pairing is **not** here: it is implemented in Go in
[`../psremote_ps4.go`](../psremote_ps4.go), including the registration crypto for
firmware 8, 9 and 10.

## Build

```sh
SDK_ROOT=/opt/sdk-tg5050 ./build-helpers.sh
```

Output:

```text
player/bin/chiaki-stream
player/bin/chiaki-regist
```

`release.sh` copies them into every TrimUI target as `Apps/JukaHub/bin/`. The
Brick Pro additionally needs the FFmpeg/OpenSSL runtime; drop it into
`native/libs/brick-stock/` before packaging (or ship it next to the app as
`Apps/JukaHub/libs/brick-stock/`).

## Session file (written by JukaHub, deleted by the helper)

```text
host=192.168.1.40
regist_key=0a1b2c3d
rp_key=<base64, 16 bytes>
ps5=0
target=1000
rp_version=10.0
width=1280
height=720
fps=30
bitrate=8000
volume=80
verbose=0
```

## Pairing result file (PS5)

```text
target=1000100
rp_key_type=2
regist_key=<hex>
rp_key=<hex, 16 bytes>
server_mac=<hex, 6 bytes>
```

The helper also prints `stage=...` diagnostics to stderr; JukaHub turns the last
stage into the message the panel shows.
