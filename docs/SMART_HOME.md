# Smart Home

JukaHub controls the house from the handheld: TP-Link Kasa and Tapo plugs,
power strips, switches and lights, Google's own speakers, displays and TVs,
everything Home Assistant has — and, through Home Assistant, Apple Home and
Google Home accessories. Pi-hole, the router, Tailscale and WireGuard have a
status panel of their own. Everything is a client: the app opens no port of
its own for any of it, and nothing goes through a cloud.

## Where it is

- **The Smart Home tab** of the Tabs Home (the default Home), between
  Entertainment and Favourites. Each device is a tile. A switches a light or a
  plug; A on a speaker, and X on any device, opens its sheet: on/off,
  brighter, dimmer, full brightness, night light, warm white and daylight,
  volume, mute, pause and play, stop, and Hide. A tile says the state — *On ·
  60%*, *Playing · …*, *Volume 40%* — or why the device cannot be used yet.
  **Add devices** sets everything up; **Network & self-hosting** opens the
  status panel. An existing tab order gets Smart Home at its end; L2 on Home
  moves or hides it like any tab.
- **Start → Smart Home** and search open the tab on the Tabs Home, and the
  Smart Home panel on any other Home layout. The panel lists the same TP-Link
  and Google devices (A switches them, or pauses and plays a speaker), Home
  Assistant's entities, the self-hosting rows and *Set up devices*.
- **On the phone**: the Phone Companion's *Set up Smart Home* card opens the
  `/smarthome` page, which Add devices also opens with a code.

Nothing is contacted at boot. The first time the tab or the panel is shown,
JukaHub looks round the Wi-Fi — a TP-Link broadcast and a Google Cast mDNS
browse, about three seconds — and then reads each device every few seconds
while the tab, the panel or the phone page is open. It looks round again
every quarter of an hour, and at once from **Add devices › Find devices**.
The devices found are kept, so the tab shows them by name at the next start
before they answer.

## Setting it up

| Source | What to do |
|---|---|
| **TP-Link Kasa** (older plugs, strips, switches, bulbs) | Nothing: found and switched with no account. |
| **TP-Link Tapo and newer Kasa** | Sign in with your **TP-Link ID**, the e-mail and password of the Tapo or Kasa app: Add devices › TP-Link Kasa & Tapo › *Sign in on this device* (on-screen keyboard, the password masked) or *Sign in from your phone*. JukaHub checks it with one of your devices before it keeps it. |
| **Google Home** — Nest Mini, Nest Audio, Nest Hub, Chromecast, Google TV, speaker groups | Nothing: they appear by themselves, with no Google account. |
| **Home Assistant** | Add devices › Home Assistant shows a code. On the phone page paste the address and a long-lived access token (your profile › Security › Long-lived access tokens). Its lights, switches, fans and covers become tiles. |
| **Apple Home** | Through Home Assistant: see below. |

### Why Google Home and Apple Home work this way

Google Home and Apple Home let no device outside their own apps switch the
accessories of a home: Google's Home APIs exist only inside Android and iOS
apps, and HomeKit only on Apple devices. What JukaHub reaches directly:

- Google's own devices speak Google Cast on the home network, which needs no
  account: what is playing, volume, mute, pause and play, stop.
- Other makers' lights and plugs are reached through their maker (TP-Link
  here) or through Home Assistant.
- **Matter** accessories can be shared with a second controller.
  **Apple Home:** open the accessory's settings, choose **Turn On Pairing
  Mode** and copy the code; in the Home Assistant phone app choose Settings ›
  Devices & services › Add device › Add Matter device, say it is already in
  use, and paste the code. **Google Home:** the device's Settings › Linked
  Matter apps & services gives the code. The accessory stays in Apple or
  Google Home and appears in JukaHub through Home Assistant.
- HomeKit-only accessories (no Matter logo) can join Home Assistant through its
  HomeKit Device integration, but only once they are removed from Apple Home.

### TP-Link details

- Older Kasa devices answer the classic local protocol on port 9999 with no
  account. Tapo and newer Kasa hardware speak **KLAP** over HTTP: its
  handshake proves the TP-Link ID's hash, so the password itself never leaves
  the handheld. Tapo firmware from before KLAP uses "secure passthrough", where
  the login goes to the device encrypted with a key only it can read. Nothing
  goes to TP-Link's cloud, and credentials only ever go to an address on the
  home network.
- **Needs Third-Party Compatibility:** the newest firmware speaks a protocol
  (TPAP) only TP-Link's own apps know. In the Tapo app open **Me › Tapo Lab ›
  Third-Party Compatibility** (Kasa app: **Settings › Third-Party
  Compatibility**), switch it on, wait a minute, then Find devices: on most
  firmware that brings KLAP back. TP-Link does not support local control by
  other apps officially, so a firmware update can change this.
- A device that was never signed in to TP-Link's cloud, or was reset, takes
  TP-Link's setup accounts instead; JukaHub tries those after yours, as the
  apps do. A refused sign-in is not tried again for five minutes with the same
  ID, so a wrong password never becomes a stream of failed logins.
- Cameras, doorbells and robot vacuums (HTTPS only) are not listed.

## Configuration

The TP-Link ID, the devices found and the hidden ones live in
`smarthome.json` in the data folder (0600; the password encrypted with the
device's key, like every other stored password). **Add devices › Hidden
devices** brings a hidden device back.

The rest are custom variables in `jukauser.json` (Settings → anything that
saves writes there) or in `variables.custom` of `jukaconfig.json`. The phone
page writes the Home Assistant ones; they are read when the tab or the panel
opens, so no restart is needed after an edit that reloads the config.

| Key | Meaning |
|-----|---------|
| `ha_url` | Home Assistant base URL, e.g. `http://homeassistant.local:8123` |
| `ha_token` | long-lived access token (Profile → Security → Long-lived tokens) |
| `ha_insecure` | `true` accepts a self-signed certificate; off by default |
| `pihole_url` | Pi-hole web root, e.g. `http://pi.hole` |
| `pihole_token` | v6 password, or the v5 API token when the legacy API is used |
| `mqtt_host`, `mqtt_port`, `mqtt_user`, `mqtt_pass` | broker (port defaults to 1883) |
| `mqtt_prefix` | topic prefix for the toggle mirror, default `jukahub` |
| `router_host` | router address when the default gateway is not it |
| `tailscale_toggle` | `true` shows the Tailscale row even before the CLI is found |

Tokens and passwords live in `jukauser.json`, which is never committed and is
not part of the shipped config. The phone page stores the Home Assistant token
encrypted; one typed by hand can be stored encrypted too, using the same secret
store as the other API keys:

```bash
cd player
./JukaHub -set-secret ha_token        # value read from stdin
./JukaHub -set-secret pihole_token
./JukaHub -set-secret mqtt_pass
```

A value written that way is `ENC:…` in the file and is decrypted only when the
panel loads its settings; if the key is missing the integration reports the
credential as unset rather than sending ciphertext. A Home Assistant token
created for this panel can be limited to the entities it should control. The
phone page accepts a plain `http://` Home Assistant address only on the home
network (or Tailscale): a token must not cross the internet in the clear.

## What each panel row does

**Home Assistant** — `GET /api/states` with `Authorization: Bearer <token>`; the
panel keeps the controllable domains (`light`, `switch`, `fan`,
`input_boolean`, `cover`), sorted by friendly name. Toggling posts
`{"entity_id": …}` to `/api/services/<domain>/toggle`; a light's sheet in the
tab steps its brightness with `light.turn_on`. Sensors are deliberately not
listed: this is a control surface, not a dashboard. A rejected token says so
instead of showing an empty house, and the last good list stays visible when a
refresh fails.

**MQTT** — toggles are mirrored to `<prefix>/<entity_id>` with `ON`/`OFF` (or
`OPEN`), QoS 0, no retained message. It is publish-only on purpose: a
subscription loop, keepalives and reconnect state are a lot of machinery for a
panel that sends a handful of messages an hour. Point a Node-RED flow,
zigbee2mqtt bridge or dashboard at the topic. A broker that is down never turns
a successful light toggle into an error — the mirror is best effort and the
failure goes to the log.

**Pi-hole** — tries the v6 REST API first (`POST /api/auth` → `X-FTL-SID`
header → `GET /api/stats/summary`) and falls back to the v5
`/admin/api.php?summaryRaw&auth=…` when the instance is older. The row shows
blocking state, queries today, blocked count and percentage.

**Router** — a TCP handshake to the default gateway's web port, which answers
"is the router there and how far away": address plus round-trip in ms. No login,
no scraping. Set `router_host` when the gateway is not the router (or on a
platform whose gateway the app cannot read).

**Tailscale** — `tailscale status --json` gives `BackendState` and
`Self.TailscaleIPs`; the switch runs `tailscale up` / `tailscale down`, which
reuses the stored prefs. The CLI's own error text is shown, so "access denied"
reads as itself.

**WireGuard** — status only. `wg show interfaces` lists tunnels but bringing one
up needs root, and a switch that always fails is worse than a line that says
why. To send the device's own traffic through a WireGuard server, use
Settings → Network → VPN instead ([VPN.md](VPN.md)), which needs no `wg` tool.

## Limits worth knowing

- Each device read has a timeout of a few seconds; a device that is off keeps
  its tile, says *Not answering*, and A on it looks round the network again.
- A press shows the new state at once and sends the command in the
  background; a device that refuses puts the old state back with a toast.
- Google Cast control covers the receiver (volume, mute, stop) and whatever
  app plays media on it (pause, play); starting something to play is the
  phone's job.
- `tailscale status --json` is not a documented stable schema; the parser reads
  the two fields it needs and ignores the rest.
- Nothing here listens on the network, and no credentials are ever written to
  the app log.
