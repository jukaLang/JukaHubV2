# VPN

Settings → Network → **VPN** sends the device's traffic through a
[WireGuard](https://www.wireguard.com/) tunnel: games and their netplay, the
browser, YouTube and downloads, the updater, PS Remote Play away from home —
everything, not only JukaHub's own connections. It is on while the switch is on
and a configuration is set; switch it off and traffic goes straight out over
Wi-Fi again.

Most VPN providers (Mullvad, Proton VPN, IVPN, AirVPN, Windscribe, NordVPN's
"NordLynx" export and others) hand out WireGuard configurations, and so does a
WireGuard server of your own (PiVPN, a router, `wg-quick` on a PC). JukaHub
takes the usual `.conf` file:

```ini
[Interface]
PrivateKey = <your private key>
Address = 10.64.12.3/32, fc00:bbbb:bbbb:bb01::3/128
DNS = 10.64.0.1

[Peer]
PublicKey = <the server's public key>
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0, ::/0
# PersistentKeepalive = 25
```

## Setting it up

1. Get a WireGuard configuration from your provider or server.
2. Put it on the device, one of three ways:
   - **From the card:** copy the `.conf` to the SD card — its top folder,
     `Downloads`, `VPN` or `WireGuard` — then Settings → Network → **VPN
     configuration** → **Import a .conf file** and pick it.
   - **In Files:** press **A** on the `.conf` file; JukaHub recognises a
     WireGuard configuration and asks for **A** once more to use it.
   - **From your phone:** Settings → Network → VPN configuration → **Send one
     from your phone** shows the companion QR. The companion page has a **VPN
     (WireGuard)** card: paste the text or pick the file, then **Save on
     handheld**. A provider's QR code holds the same text: scan it with the
     phone's camera app and paste what it shows. Do this on your own Wi-Fi —
     the companion page is plain HTTP on the local network.
3. Switch **VPN** on — in Settings or in Quick Settings.

The Wi-Fi meter in the top corner then wears a small lock: in the meter's
colour while the tunnel is up, in the warning colour while the VPN is on but
not connected (traffic is not going through it then).

## What goes through it

**Send through the VPN** has two choices:

| Choice | What it does |
| --- | --- |
| **Everything** (default) | All traffic, whatever the file's `AllowedIPs` say. Your Wi-Fi network itself (the router, a PlayStation at home, the phone companion, FTP) stays reachable directly. |
| **Only the configuration's networks** | Just the `AllowedIPs` the file lists — for a server that should only reach your home network, say `192.168.1.0/24`. |

With **Everything**, the configuration's **DNS** servers answer every name
lookup while the VPN is on, so lookups do not leak to the Wi-Fi's router either.
A family the tunnel has no address for — usually IPv6 — is refused while the VPN
is on: connections to it fail at once, programs use IPv4 instead, and nothing
slips past the VPN over IPv6. Broadcast and multicast stay on the Wi-Fi, so LAN
play and finding your other devices keep working.

On the handheld the tunnel needs the server's address first: when it is a name
and the network is not up yet, the VPN waits for it ("waiting for the
network"), and until it is found, traffic goes out directly as usual.

## Settings rows

| Row | Shows / does |
| --- | --- |
| **VPN** | The switch. Its second line is the state: *Connected*, *Connecting to …*, *The server is not answering*, or *Not connected:* and the reason. |
| **VPN configuration** | The configuration's name, its server and address. **A** imports one, sends you to the phone, or removes it (which also switches the VPN off). Keys a file carries that JukaHub ignores are listed there (see below). |
| **Send through the VPN** | Everything, or only the configuration's networks. |
| **VPN status** | The last handshake, the tunnel's addresses, the server, the data in and out, and whether DNS goes through the VPN. Before the first connection it shows your **public key** — the one a server of your own needs. |

The tunnel's own messages ("connected to …", "no answer from … for 90
seconds", "the network moved from wlan0 to …") go to the Logs screen and into
bug reports, which never carry the configuration or a key.

## How it works on the handheld

JukaHub carries its own WireGuard (`player/wg_*.go`); nothing has to be
installed. Switching the VPN on starts a small tunnel process — the same
JukaHub binary started as `JukaHub --vpn-daemon` — which, as root:

- creates the network interface `jukavpn0` (it loads the `tun` module, or
  makes `/dev/net/tun`, when the firmware has the driver but not the device
  file), gives it the configuration's addresses and MTU, and runs WireGuard on
  it;
- routes `0.0.0.0/1` and `128.0.0.0/1` (and `::/1`, `8000::/1`) into it. They
  are more specific than the default route, so everything takes the tunnel
  while the default route stays as the network set it, and the Wi-Fi
  network's own route is more specific still — which is why the LAN stays
  reachable. Routes are only ever added beside the network's own, never in
  place of them. A family the tunnel has no address for gets the same two
  halves as "unreachable" routes instead, and multicast (`224.0.0.0/4`) and
  the limited broadcast stay routed by the Wi-Fi;
- sends the encrypted packets out of the interface the default route uses
  (Wi-Fi) with `SO_BINDTODEVICE`, so they cannot loop into the tunnel, and
  follows the network when it changes or the Wi-Fi driver comes back;
- points `/etc/resolv.conf` at the configuration's DNS servers (as soon as the
  network has written one), keeping the original in `data/vpn-resolv.json` so
  it comes back when the VPN stops — even after a crash or a power cut
  (JukaHub puts it back as soon as it notices).

The tunnel is a process of its own so that it outlives the app's process: it
keeps running while PS Remote Play has the screen (JukaHub quits for the stream)
and across a restart after an update. It stops when the VPN is switched off,
and on its own when the JukaHub session ends — leaving JukaHub for the stock
launcher leaves no tunnel behind. If it ever dies, its interface and routes go
with it and the device simply falls back to its normal connection.

A server refuses a handshake whose timestamp is not newer than the last one it
took from your key, and a handheld without a clock battery starts each day in
the past until it reaches a time server. JukaHub's timestamps therefore never go
backwards: they stay above a mark kept in `data/vpn-clock`, moved a minute ahead
at a time while the tunnel runs and set just past the last handshake when it
stops.

**Needs:** JukaHub running as root (the stock firmware does) and a kernel with
the TUN driver. When one is missing, the VPN row says so instead of failing
quietly.

## On Windows

Windows tunnels need a driver and an administrator, so JukaHub hands the tunnel
to **WireGuard for Windows** ([wireguard.com/install](https://www.wireguard.com/install/)):
switching the VPN on installs it as WireGuard's tunnel service named *JukaHub*,
which routes the whole PC. Windows asks for permission each time the VPN is
switched on or off, or its configuration or route changes — the prompt names
JukaHub, which does the change and nothing else (it uses the `wireguard.exe` in
Program Files, and only if an administrator or Windows owns it). Nothing
changes without the prompt: turn it down and the row says what is still
running. The configuration WireGuard reads sits in `%ProgramData%\JukaHub\vpn`,
where only the system and administrators can read the key. Like any WireGuard
tunnel it comes back with Windows until it is switched off here (or in
WireGuard), and safe mode does not stop it. The status row shows whether the
service runs; WireGuard's own window shows handshakes.

What differs from the handheld: **Everything** sends both IPv4 and IPv6 into
the tunnel, so a configuration without an IPv6 address leaves IPv6 with nowhere
to go rather than around the VPN; and DNS is set on WireGuard's adapter, which
Windows prefers but does not use exclusively.

## Where things are kept

| What | Where |
| --- | --- |
| The configuration | `data/vpn.json` (handheld: `Apps/JukaHub/data`; Windows: JukaHub's application-data folder). Not in `jukauser.json`, never in backups, never shown again after import. |
| The switch and the route choice | `jukauser.json`: `vpnEnabled`, `vpnRoute` (`config` for only the configuration's networks). |
| The tunnel's state | `/tmp/jukahub-vpn/status.json` while it runs (in RAM). |
| The original DNS settings | `data/vpn-resolv.json` while the VPN runs. |

## Not supported

- **More than one `[Peer]`.** JukaHub connects to one server; keep the one you
  want in the file.
- **`PreUp`, `PostUp`, `PreDown`, `PostDown`, `Table`, `FwMark`, `SaveConfig`.**
  They are read past — scripts in a file are never run — and listed under
  VPN configuration.
- **A kill switch.** While the VPN is on but the server does not answer,
  traffic waits for the tunnel; if the tunnel process itself stops, the device
  falls back to its normal connection rather than going offline.
- **OpenVPN, IKEv2 and other protocols.** WireGuard only.

## Troubleshooting

| The row says | Look at |
| --- | --- |
| *The server is not answering* | The `Endpoint` (host and port) and that the server is up; a wrong key looks exactly like this too, because WireGuard does not answer a key it does not know. On a server of your own, check the handheld's public key (VPN status) is the one it has. |
| *Connecting · waiting for the network: cannot look up …* | Wi-Fi is not connected yet, or the server's name does not resolve. |
| *This firmware's kernel has no TUN driver* | The firmware cannot run a VPN; a custom firmware with the `tun` module can. |
| *The VPN needs JukaHub to run as root* | JukaHub was started by a user; on the handheld the stock launcher starts it as root. |
| *Needs WireGuard for Windows* | Install it, then switch the VPN on. |
| *Windows asked for permission and it was not given* | Switch the VPN off and on (or back) again and allow JukaHub in the prompt. |
| *Still set up in WireGuard* | The VPN is off here but Windows refused to remove the tunnel (or it was stopped in WireGuard): VPN configuration → **Remove the tunnel from WireGuard**, or remove it in WireGuard. |
| Pages load, names do not | The configuration's `DNS` server is not reachable through the tunnel; remove the `DNS` line to keep the network's. |
