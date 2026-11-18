# Hotspot

**Tools → Hotspot** (also **Settings → Network → Hotspot**) turns the handheld
into a Wi-Fi network of its own, the way the stock TrimUI firmware's hotspot
does: two handhelds can play netplay with no router, and a phone can join to
use the Companion. There is no internet through it — phones keep their mobile
data for that. It is for the TrimUI handhelds; on Windows the tile and the row
are hidden.

The handheld has one radio and it does one job at a time, so **the device's own
Wi-Fi connection is off while the hotspot is on**. Turning the hotspot off puts
back what was there before: the saved network is rejoined (the Wi-Fi keeper
does it, with DHCP), or the radio is switched off again if it was off.

## The screen

- **Hotspot** — the switch, with what is happening (Starting…, On at
  192.168.43.1 · 2 devices, Could not start: …).
- **Network name** and **Password** — A edits either on the keyboard; X on the
  password makes a new random one. A running hotspot restarts with the change
  (devices that had joined join again).
- **Join with a phone** — a full-screen QR code (`WIFI:T:WPA;S:…;P:…;;`) the
  phone's camera joins with, no typing.
- **Joined** — who is on it (name when the DHCP server knows it, address, MAC),
  and the **Phone companion**'s address on the hotspot with its own QR code.

The first time the screen opens it picks the name and password: the firmware's
own hotspot when its hostapd config is on the card (`/etc/hostapd.conf`,
`/etc/wifi/hostapd.conf`, …), otherwise `JukaHub-XXXX` (the radio's MAC) and a
random 8-character password. The firmware's password is only taken when it is
not one of the shared factory ones (12345678, password, one repeated character
and the like); otherwise the hotspot gets its own random one. They are kept in `jukauser.json` under
`_hotspot_ssid` and `_hotspot_pass` (underscore keys are never exported).

## How it starts

1. If the firmware ships Allwinner's own hotspot tool (`softap_up` /
   `softap_down`, part of the A133/A523 board support package the TrimUI
   firmware is built on), JukaHub runs `softap_up <name> <password> broadcast`
   and checks that hostapd runs and the radio has an address.
2. Otherwise JukaHub drives the firmware's `hostapd` itself: `udhcpc` and
   `wpa_supplicant` step off the radio, a WPA2-PSK config (2.4 GHz, channel 6)
   is written to `/tmp/jukahub-hotspot/`, hostapd starts with a pid file, the
   radio takes `192.168.43.1/24`, and a DHCP server hands out
   `192.168.43.10–60`: busybox `udhcpd`, else `dnsmasq`, else a small DHCP
   server built into JukaHub. No router or DNS option is handed out.
3. A firmware with neither tool says so ("This firmware has no hotspot
   tools"); nothing pretends to work.

While it runs, a check every 3 seconds notices hostapd stopping (Wi-Fi comes
back) and the radio being switched off (airplane mode ends the hotspot). The
Wi-Fi keeper, the wake watcher and the supplicant start stand aside. The Wi-Fi
page says the hotspot is using the radio and to turn it off in Tools → Hotspot;
nothing on that page (Disconnect, Forget, a join) tears the hotspot down behind
your back.

The name and password cannot change while the hotspot is starting or turning
off ("One moment…"). Screen Share paints over the password row and the join
code, since either lets a viewer onto the handheld's network.

**Sleep.** The hotspot goes off as the device goes to sleep, with the screen
already dark, and Wi-Fi is given back when it wakes, not before (starting
wpa_supplicant and a join would only hold the suspend up). A start still
running then turns itself off as soon as it finishes.

**Quitting.** The teardown card has its own "Turning the hotspot off" row
(about 3 s at most): hostapd and the DHCP server are stopped quickly and Wi-Fi
is handed back to the firmware the way it was before.

**After a crash.** The run directory records which method was running, so the
next start ends a hostapd it left behind (`softap_down` for the firmware's
tool), and also a DHCP server that outlived hostapd, which would otherwise hand
`192.168.43.x` addresses into whatever network the radio joins next.

Turning the hotspot off puts back what was there: the keeper is nudged to
rejoin the saved network, but a Disconnect you pressed earlier, or Wi-Fi
switched off in the firmware, still holds.

## When it does not start

The log has every step under `hotspot`, starting with the line that names the
tools found (`hotspot: tools: softap_up=… hostapd=…`), then what softap_up or
hostapd answered. Send that part of the log with a bug report.
