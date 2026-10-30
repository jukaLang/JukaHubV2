# JukaHub on the TrimUI Smart Pro (stock OS)

A copy-and-go package for the **TrimUI Smart Pro (Allwinner A133P, Linux ARM64, stock OS)**.

What the package contains:

```
Apps/JukaHub/
├── config.json          ← registers the app in the stock launcher
├── icon.png             ← icon in the Apps menu
├── launch.sh            ← what the launcher runs to start JukaHub
├── JukaHub              ← ARM64 binary (built on CI)
├── jukaconfig.json      ← layout config (generated: full scene list, device defaults)
├── Inter-Regular.ttf    ← font
├── background.jpg
├── VERSION.txt
└── required/            ← helper tools, all ARM64
    ├── yt-dlp           ← fetches YouTube info/streams (no Python)
    ├── ffmpeg           ← decoder for video and audio playback
    └── ffprobe
```

You do **not** need Python, Docker, Go or any toolchain on the device. Every
SDL2 library (2.26.1 + image + ttf) is already in the stock firmware.

---

## 1. How to build the package (GitHub Actions)

An ARM64 build needs a toolchain, so it runs on GitHub Actions (using the
community TrimUI toolchain image, which already has the A133P SDK + aarch64 gcc
+ SDL2 + Go).

1. Commit and push your changes to the repository.
2. Open the **Actions** tab → workflow **Build SD card package** → **Run
   workflow**. (The workflow also runs automatically on every push to `main`.)
3. Wait about 3–6 minutes. Download the **JukaHub-SDCARD** artifact.
4. The artifact contains `JukaHub-SDCARD.zip`.

Alternative without GitHub (if you have a Linux PC, WSL or a VPS):

```bash
cd player
SYSROOT=/usr/local/aarch64-linux-gnu-7.5.0-linaro/sysroot ./package-sdcard.sh
```

---

## 2. Installing on the SD card

1. Power the device off and remove the SD card.
2. Extract `JukaHub-SDCARD.zip`. Its folders are already laid out as
   `Apps/JukaHub/...` — **copy them straight onto the card root**, overwriting
   or merging with the existing `Apps` folder. The result looks like:

   ```
   SDCARD/
   ├── Apps/
   │   └── JukaHub/       ← the new folder
   ├── RetroArch/
   ├── Roms/
   └── ... (other stock folders, do not delete them)
   ```
3. Insert the SD card and power the device on.
4. Open the **Apps** menu → **JukaHub**.

If `Apps/JukaHub` does not appear in the menu, power the device off and on again
(the stock launcher reads the `Apps` folder at boot).

### Starting automatically at boot

Open **Settings → Smart Widgets** and turn on **Start JukaHub at login**.
JukaHub then writes one hook file:

```
SDCARD/System/starts/jukahub-autostart.sh
```

`runtrimui.sh` runs the scripts in the `System/starts` folder at boot, so
JukaHub opens as soon as the device starts. That script returns immediately
(work continues in the background), waits up to 30 seconds for MainUI to appear,
then runs `launch.sh` — boot is never held up if the app fails to start. Its
output goes to `Apps/JukaHub/autostart.log`.

To turn it off: switch the same toggle off, or delete
`System/starts/jukahub-autostart.sh` by hand. No other file in that folder is
touched. If the SD card cannot be detected (for example when the binary is run
from a symlink), the toggle refuses and explains why; set `JUKAHUB_SD_ROOT` to
the card root to point it at the right location.

Note: this is the hook the community and CrossMix use, not an official trimui
interface. If your firmware uses another mechanism, run `launch.sh` from that
firmware's hook — the result is the same.

### Self-update and restart

During an update JukaHub cannot replace its own binary, so a small helper swaps
the files after the app exits. On the handheld, `launch.sh` restarts the new
build **in place**: the helper only writes `.update-swapped`, and the same
script (the same process) runs `JukaHub` again. The firmware supervisor stays
paused and MainUI is never started again in the middle of an update.

That matters because the old launcher brought MainUI back as soon as JukaHub
exited, while the helper started a second launcher. Two UIs then drew to the
same `/dev/fb0`, and the screen appeared to flicker between MainUI and JukaHub.
There are two guards now:

- `launch.sh` re-checks that MainUI really is gone; if it is still alive after
  a few `kill -9` attempts, JukaHub is **not** started and the reason is written
  to `errors.txt`.
- If the launcher on the card does not know this protocol yet (an older
  version), the helper waits for the old launcher to finish first, then starts
  the new one.

The markers involved (in the app folder, temporary only): `.restart-pending` is
written by JukaHub before the swap, `.update-swapped` / `.update-failed` are
written by the helper as the result, and `launch.sh` removes them all when it
starts. `launch.sh` in the release package and in `Apps/JukaHub` must be
identical — a test rejects the build if they differ.

---

## 3. If something goes wrong

| Symptom | What to do |
|---|---|
| Black screen / the app never appears | Put the SD card in a PC and send the contents of `Apps/JukaHub/errors.txt`. |
| The app appears but exits immediately | Check `errors.txt` — usually `JukaHub` is not executable yet (`chmod +x`) or a library is missing. |
| Text is missing or shows as boxes | `Inter-Regular.ttf` is missing from the app folder. |
| The screen flickers between MainUI and JukaHub after an update | The `launch.sh` on the card is older than the app. Update once more (the release archive replaces `launch.sh`), or copy `launch.sh` from the archive into `Apps/JukaHub/`, then reboot. |
| YouTube search fails | Check that `required/yt-dlp` exists; Wi-Fi is required. Also see `logs.txt` (the **Logs** scene in the Start menu). |
| Just opened the app and Wi-Fi is not connected yet | An **Offline — connect WiFi** message appears. Connect Wi-Fi; any missing tools are downloaded automatically once the connection is back. |
| Downloading `yt-dlp` / `ffmpeg` fails | The download card shows `Could not install …` with an **A: retry** button. Press **A** to retry; once it succeeds, restart JukaHub so the new tools are used. |
| Video is slow or stutters | Lower the playback resolution in Settings to `360` or `240`. |

All helper tools install into `Apps/JukaHub/required/` on the SD card (not
`/tmp`), and JukaHub does not run `--version` when installing a tool — existing
files are not overwritten and a correct download is not deleted.

All application logs live in:

* `Apps/JukaHub/errors.txt` — process + launcher output
* `Apps/JukaHub/logs.txt` — the app's internal log (the **Logs** scene in the Start menu)

Send those files along with a bug report; no SSH needed.

---

## 4. Technical notes (for deeper tuning)

* **Architecture:** TrimUI Smart Pro = Allwinner A133P, userspace **aarch64**
  (glibc 2.23), 1280×720 display, 1–2 GB RAM, quad-core Cortex-A53.
* **Libraries:** the binary links against SONAMEs that exist in the stock
  firmware (`libSDL2-2.0.so.0`, `libSDL2_image-2.0.so.0`,
  `libSDL2_ttf-2.0.so.0`), so it does not need to ship its own `.so` files.
* **No Python in the stock OS**, so `yt-dlp` is used as the official static
  binary `yt-dlp_linux_aarch64`.
* **ffmpeg** is used as the static ARM64 build from BtbN (ffmpeg and
  ffprobe). The bundle's `ffplay` is not shipped: nothing runs it, and it is
  linked against X11, which the stock OS does not have.
* **Device config:** `player/trimui/make-config.ps1` generates
  `player/trimui/jukaconfig.device.json` from the full config. It keeps **every
  scene** and only applies the handheld defaults (fullscreen, 1280×720, the
  720p playback cap, no weather, reduced motion, low power). There is no
  hand-maintained scene list any more: one went stale, named a scene that no
  longer existed (so the script threw), and omitted nine scenes the Apps
  launcher opens — and because the launcher hides a row whose scene the config
  does not ship, the handheld quietly lost JukaLand, Live TV, Podcasts, Tickers,
  Weather, Unit Converter, Canvas Sandbox, IP Stream and the Audio Player.
  Trimming is still possible with `-Drop Name1,Name2`, and the script refuses a
  drop that a kept scene still navigates to. It also fails the whole run when
  any `change_scene:` target is missing, so a dead button cannot be generated.
  `TestDeviceConfigShipsEveryBuiltinApplet` fails if the committed file ever
  stops shipping an applet's scene.
  The file is deliberately **not** named `jukaconfig.json`, because
  `player/.gitignore` excludes that file (it may contain a user's API key) —
  with the same name the device config would not be committed and the CI build
  would fail. Re-run the script whenever the full config changes:

  ```powershell
  cd player/trimui
  powershell -File .\make-config.ps1 -Src ..\jukaconfig.json -Dst .\jukaconfig.device.json
  ```
* **Code changes for the device:**
  * `videoFormatSelector()` — the playback resolution cap follows
    `playbackResolution`.
  * `SDL_AUDIODRIVER=directsound` is now Windows-only (it used to make SDL fail
    to open audio on Linux).
