# Wi-Fi network picker

JukaHub can scan, join and manage Wi-Fi networks without leaving the app. The
stock firmware's Wi-Fi screen is a separate application; some images ship
without one at all, and a handheld that boots straight into JukaHub otherwise
has no way to enter a new password when it moves to another network.

## Opening it

| From | How |
|------|-----|
| Status bar | tap the signal meter at the top right (Home and every scene) |
| D-pad | **Up** into the top bar, then **Left** onto the meter (on Home: **Up** from Settings reaches the clock, **Left** walks the weather chip and the meter); **A** opens the picker |
| Quick Settings | **Start → Quick Settings**, highlight **Wi-Fi Networks**, press **A** |
| Hardware scene | **Settings → Hardware → Wi-Fi** (the button is added in memory; the config file is not changed) |
| Config trigger | a button with `"trigger": "wifi_open"` on any scene |

The meter's tap target is deliberately larger than the bars it draws (it spans
the height of the top bar and extends into the empty space to its left), because
the icon itself is smaller than a fingertip.

## What the meter means

The corner shows three states, and only one of them is a signal strength:

| Corner | State |
|--------|-------|
| Four solid bars, green to red | a measured strength: the kernel's link quality from `/proc/net/wireless`, or the supplicant's RSSI from `wpa_cli signal_poll` when that table is missing |
| Four hollow bars outlined in the accent | the interface is up and neither source reports a number; connected, strength unknown |
| Hollow bars with a slash | no wireless link at all |

The slash is deliberate. An empty four-bar meter reads as *weak signal*, which is
the opposite of the truth when the radio is not associated, so the no-link state
is drawn as a crossed-out meter instead. Its ink is the warning colour when
nothing else is keeping the device online, and a muted grey when it is (an
Ethernet link, for instance): the mark is about Wi-Fi, and the text beside it
(`! Offline`) is about the internet.

The meter never invents a level. When no quality is readable it says *unknown*
rather than filling half the bars, so a strong link and an unmeasurable one do
not look the same.

### Where the number comes from

Two sources, in this order, because stock images differ:

1. **`/proc/net/wireless`** — the kernel's link quality on a 0..70 scale. Present
   on the stock Smart Pro and Brick kernels, and the fastest read (a file, no
   process).
2. **`wpa_cli -i <iface> signal_poll`** — the supplicant's own `RSSI` in dBm,
   mapped onto the same four bars. Images built without wireless extensions have
   no `/proc/net/wireless` at all, and some drivers list an interface with
   quality `0` while associated; both cases used to leave the meter *unknown*
   forever, and now show a real level.

The poll runs on a worker, never on the render loop: a supplicant that stops
answering would otherwise freeze the UI for the command timeout. The last answer
is cached for the same four seconds as the kernel read, so the first frames after
boot read *unknown* and become bars one refresh later. A non-negative `RSSI` (the
`0` a disconnected supplicant prints) is not treated as a measurement, and a
failed poll clears the cached level rather than leaving a stale one on screen.

The picker reuses this chain rather than reading the scan table for its own
network: the **CONNECTED** row draws its bars from the same meter reading, so an
image with no `/proc/net/wireless` shows a real level there too instead of the
single bar a driver's `0` used to produce. The other rows keep the RSSI from
`wpa_cli scan_results` — they are not the link the device is on, and the meter
does not measure them.

A read-only probe is one row away: **Tools → Wi-Fi signal** prints the same
numbers and names the source it used.

The Wi-Fi radio itself is still switched on and off in Quick Settings (**Wi-Fi**
row). The picker can also turn it on: with an empty list, pressing **A** switches
the radio on and scans.

## Controls

| Input | Action |
|-------|--------|
| D-pad Up/Down, arrows | move through networks |
| **A**, Enter, tap a row | join the highlighted network |
| tap **Reconnect** (or tap the row tagged CONNECTED) | drop the association and join it again with the saved key |
| **X**, `d` | disconnect (the saved network is kept) |
| **Y**, `r` | rescan |
| **SELECT**, `f` | forget the saved network (press twice) |
| **B**, Esc, tap **Close** | close the picker |

Every one of those actions is also a pill along the bottom of the panel, so the
picker is fully usable by touch. The pills follow the state: Reconnect and
Disconnect appear only while connected, Forget only while a saved network is
selected.

Tapping the row that already shows **CONNECTED** reconnects it instead of doing
nothing. That is the useful retry when the panel says connected but a page still
will not load (a stale association, or a router that rebooted while the handheld
slept): the link is dropped and re-joined with the stored key.

Choosing a network the device has joined before reconnects it with the stored
key: no password prompt. A new secured network opens the on-screen keyboard,
where the password is masked and is not trimmed (a leading or trailing space is
part of the key). Open networks join immediately.

The password keyboard accepts a hardware keyboard too, and the panel never
blocks the rest of the app while it types: JukaHub's virtual keyboard is the top
layer, and closing it returns to the picker.

## How it works

The backend is `wpa_supplicant`, driven through `wpa_cli`:

* **Interface discovery.** The wireless netdev is the first entry under
  `/sys/class/net` that contains a `wireless` directory. Images whose radio has
  an unusual name can set `JUKAHUB_WIFI_IFACE` (letters, digits, `.`, `_`, `-`
  only; the override is never used as a command).
* **Scan.** `wpa_cli scan` is followed by polling `scan_results` until the list
  changes, so a cached scan is never presented as fresh. One row is shown per
  SSID: the strongest BSSID wins, and hidden networks (empty SSID) are omitted
  because two unnamed rows cannot be told apart.
* **Join.** A network entry is created, its SSID and key are written, it is
  selected, and `save_config` persists it. JukaHub then polls `status` until the
  requested SSID reports `COMPLETED`. Matching the name matters: a failed join
  leaves the previous network `COMPLETED`, which naive code reads as success.
* **Address.** The supplicant's `ip_address` is used when present. Otherwise
  JukaHub asks the firmware's DHCP client for a lease (`udhcpc` first, then
  `dhcpcd`) and re-reads the status.
* **Saved networks.** Reconnecting uses `select_network <id>` with the key the
  supplicant already holds; the password is never asked for again. An explicit
  reconnect issues `disconnect` first, because `select_network` is a no-op while
  the supplicant still believes it is associated — the exact state a reconnect
  is meant to clear.
* **Forget.** `remove_network` plus `save_config`; if the network was the
  current one, it is disconnected too, because its key is gone.
* **Failed joins** remove the half-created entry, so repeated attempts do not
  fill the supplicant's list with duplicates.

Everything that forks a process runs on a worker goroutine. The render loop
drains one result slot per frame, which is also why a slow scan or a wedged
supplicant cannot freeze the UI.

## Security notes

* The passphrase is written to `wpa_cli`'s standard input, never to its command
  line: `ps` shows every argument of every process. It is also never logged and
  never included in an error message.
* `wi-fi` passwords are masked in the keyboard field, and the buffer is cleared
  when the keyboard closes or submits.
* Network names and passwords are validated before they reach the supplicant:
  SSIDs are limited to 32 bytes with no control characters; WPA keys must be
  8–63 characters or exactly 64 hexadecimal digits.
* `wpa_cli` re-parses quoted strings, so backslashes and quotes in a network
  name or password are escaped rather than interpreted as new commands.

## Limits

* **`wpa_cli` is required.** On a firmware image without it, the picker opens
  with an explanation instead of pretending to scan. The Quick Settings Wi-Fi
  switch may still work through `nmcli`/`rfkill`, but joining networks does not.
* **No WPA-Enterprise / 802.1X.** This is a home-network picker, not a corporate
  supplicant configuration screen.
* **Hidden networks are not listed.** Joining one by typed name is not exposed
  yet; add it to `wpa_supplicant.conf` on the card if you need it.
* **No static address, DNS or proxy settings.** DHCP is used, as on the stock
  firmware.
* **SSID collapse.** On a mesh or dual-band network with one name, JukaHub keeps
  the strongest access point; it does not show one row per band.
* **Session-only saves are reported.** If the supplicant runs with
  `update_config=0`, `save_config` fails; the panel still reports the connection
  but warns that it will not survive a reboot.

## Troubleshooting

| Message | Meaning |
|---------|---------|
| `Wi-Fi control needs wpa_cli, which is not installed` | the image lacks `wpa_cli`; use the stock Wi-Fi screen or add the tool |
| `No wireless network interface on this device` | no radio is exposed to Linux, or the radio is powered off |
| `could not join <name>: the password was rejected` | the key is wrong (the supplicant reached the 4-way handshake and failed) |
| `could not join <name> (disconnected)` | out of range or the access point refused the association |
| `no IP address was assigned; check DHCP on this network` | associated, but no lease: the network's DHCP server or the firmware's client is the problem |
| `this network could not be saved for the next boot` | `save_config` failed (`update_config=0`); the connection works until reboot |

The panel's status line is the source of truth. A toast reports the result of an
action, and the list is refreshed from the supplicant afterwards, so the row's
**CONNECTED** tag always comes from `wpa_cli status`, not from what the app
expected to happen.

## Tests

`wifi_test.go` covers the parsers, the password rules, the SSID/PSK escaping, the
forget path, the panel layout at 720p and 480p, and the end-to-end UI flow from
opening the picker to a connected status. It stubs `wpa_cli` completely: no test
touches a radio, and one test asserts that the passphrase never appears in any
argument.

The status meter has its own coverage in `statusbar_status_test.go`: the
`/proc/net/wireless` parser and its bar thresholds, the `signal_poll` parser
(including `FAIL`, a missing `RSSI` and the `0` placeholder), the source chain in
`wifiMeterLevelFrom`, and the supplicant cache — an unanswered poll stays
*unknown*, a failed poll clears the previous reading, and the caller never waits
for the command.

```sh
cd player
go test -run 'TestWiFi|TestParseWPA|TestMergeWiFi|TestValidateWiFiCredentials' -count=1 .
```
