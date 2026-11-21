# Tailscale (reach the handheld from anywhere)

Tailscale puts your devices on one private network (a "tailnet") wherever they
are: at home, on mobile data, at a friend's. With it on, your phone or PC reaches
the handheld's SSH, FTP, phone companion, VNC and network drive from anywhere,
without opening ports on a router. JukaHub has it built in since 2026-11-20.

Where: **Tools → Network tools → Tailscale**, or **Settings → Network →
Tailscale**. Smart Home's Tailscale row uses the same switch.

## On the handheld

1. Open the Tailscale screen and press **A** on the first row. The first time it
   downloads Tailscale's official static build for the handheld's CPU from
   `pkgs.tailscale.com/stable` and checks it against the SHA-256 published beside
   it. A download whose hash does not match, or a package list that names an
   unexpected file, installs nothing. Only `tailscale` and `tailscaled` are kept,
   in JukaHub's tools folder (`required/` next to the app).
2. Press **Log in**. A QR code appears: scan it with your phone's camera and sign
   in with the account your other devices use (or open the address on a
   computer). The code closes by itself once the handheld is in your tailnet.
   **Log in with an auth key** takes a key from the admin console's Keys page
   instead (`tskey-auth-…`, typed on a masked keyboard and never stored).
3. The screen shows this device's tailnet addresses (100.x.y.z and an IPv6
   address) and MagicDNS name, and your other devices with Online / Offline.
   **A** on a device copies its address.

The handheld's name in the tailnet is its **Device name** (Settings → Network → Device name),
folded to lower-case letters, digits and dashes.

### What reaches it

Tailscale runs in *userspace networking* mode: no kernel module, no routes and no
DNS changes. A connection from one of your devices to the handheld's tailnet
address is handed to the same port on the handheld itself, so every JukaHub
server that is on answers over the tailnet exactly as it does on the Wi-Fi:

| Server | Turn it on in | Then from anywhere |
| --- | --- | --- |
| SSH / SFTP | Network tools | `ssh root@100.x.y.z -p <port>` |
| FTP | Network tools | an FTP client to `100.x.y.z` |
| Phone companion | Network tools | the companion page at `http://100.x.y.z:<port>` |
| VNC, SMB network drive | Network tools | the same address |

Because the connection arrives on the handheld from `127.0.0.1`, a program that
listens only on the handheld itself (loopback) answers your tailnet devices too
(Syncthing's page still asks for its key or password). Share the
handheld only with devices you trust, or limit it with your tailnet's access
rules.

The other way round does not change: games, the stores, the browser and
everything else the handheld does keep using the normal internet. That is also
why there is no exit-node picker.

### When it runs

- Only while the switch is on (off by default).
- Only with Wi-Fi: it waits for Wi-Fi, and stops five minutes after Wi-Fi is lost.
- Not on a low battery: below 15 % off the charger it stops; at 20 % or on the
  charger it starts again.
- It keeps running while a game plays: it idles until someone connects. It runs
  at a lower priority (nice 10) on two cores, and does not upload its logs to
  Tailscale (`--no-logs-no-support`).
- It stops when JukaHub exits. A daemon a crashed JukaHub left behind is ended at
  the next start.

### Updates and removal

The **Emulator updates** pass (Settings → Games & emulators → Emulator updates) also keeps Tailscale
current: Notify me tells you, Install automatically installs the new build at a
quiet moment and restarts the daemon when no game runs. On the screen, **A** on
the version row checks now, and installs when a newer build is listed.

**Log out** takes the handheld out of your tailnet until you log in again.
**Remove Tailscale** (A twice) logs out, then deletes the program and the
handheld's tailnet state (`data/tailscale`), so a later install is a new device.

### Files

| Path | What |
| --- | --- |
| `required/tailscale`, `required/tailscaled` | the programs |
| `data/tailscale/` | the daemon's state (node key), `tailscaled.log`, `jukahub-install.json` |
| `/tmp/jukahub-tailscaled.sock` | the control socket (the card cannot hold one) |

## On a PC

JukaHub installs nothing on a PC. When Tailscale for Windows is installed (or a
`tailscale` command is on PATH), the Tailscale screen and Smart Home show its
state, addresses and devices; sign in and out from its icon in the taskbar.
Without it, the screen says where to get it: tailscale.com.

## Settings

| Key | Default | Meaning |
| --- | --- | --- |
| `tailscaleEnabled` | `false` | Run JukaHub's Tailscale on the handheld (installs on first use). |
