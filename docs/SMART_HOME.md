# Smart home & self-hosting

JukaHub can drive the house from the handheld: Home Assistant entities, a
Pi-hole and router status line, and the Tailscale tunnel. Everything is a client
— the app never opens a port of its own for this.

Open it from **Start → Smart Home**. The panel refreshes when it opens; `R` (or
`X` on a controller) refreshes again, `A` toggles the focused row, `B` closes.
Nothing is contacted until the panel is opened, so a config with no smart-home
settings costs nothing at boot.

## Configuration

All keys are custom variables: put them in `jukauser.json` (Settings → anything
that saves writes there) or in `variables.custom` of `jukaconfig.json`. They are
read when the panel opens, so no restart is needed after an edit that reloads
the config.

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
not part of the shipped config — but it is plain JSON on the SD card, so treat
the card like any other device holding credentials. A Home Assistant token
created for this panel can be limited to the entities it should control.

## What each row does

**Home Assistant** — `GET /api/states` with `Authorization: Bearer <token>`; the
panel keeps the controllable domains (`light`, `switch`, `fan`,
`input_boolean`, `cover`), sorted by friendly name. Toggling posts
`{"entity_id": …}` to `/api/services/<domain>/toggle`. Sensors are deliberately
not listed: this is a control surface, not a dashboard. A rejected token says so
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
why.

## Limits worth knowing

- The panel probes run in parallel with a 6 s timeout each; a host that is off
  keeps the previous numbers and the row explains the failure.
- `tailscale status --json` is not a documented stable schema; the parser reads
  the two fields it needs and ignores the rest.
- Nothing here listens on the network, and no credentials are ever written to
  the app log.
