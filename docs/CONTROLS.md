# Controls

JukaHub is controller-first: every screen is reachable with the D-pad and the
four face buttons. Keyboard and mouse are supported on desktop builds.

The top bar always shows the system status — network, weather, clock, battery
and the notification bell on the right, the **Apps** pill and a **back** pill on
the left. The bottom of every screen is the hint bar for real controller
bindings; there is no second clock or battery indicator down there.

All bindings below are also listed in-app: press **F1** anywhere for the
shortcuts overlay, **B** or **F1** to close it.

---

## Everywhere

| Button | Action |
|--------|--------|
| D-pad / left stick | Move the selection |
| A | Open / confirm |
| B | Back (header pill, `Escape` and the scene's Back action all do the same) |
| X | Secondary action — delete, toggle, search, depending on the screen |
| Y | Tertiary action — new/create, save, submit in the chat keyboard |
| L1 / R1 | Previous / next section, page or track |
| Start | Open the start menu (pause) |
| Back | Settings / menu |

## Home

| Button | Action |
|--------|--------|
| A | Open the focused tile |
| B | Settings |
| Y | Toggle theme (cycles the presets) |
| X | Favourites |
| D-pad | Move between tiles and the footer row |

## Live TV

Playlists (`.m3u` / `.m3u8`) live in the `tv` folder next to the app, or use
**Load Playlist** to fetch the public iptv-org list (cached for six hours).
Channels are grouped by playlist category, so a long file does not have to be
scrolled channel by channel.

| Button | Action |
|--------|--------|
| Left / Right | Previous / next category (**All**, **Favorites**, then the groups) |
| Up / Down | Move through the channel list |
| A | Play the highlighted channel |
| X | Search channels (filters while you type) |
| Y | Add / remove the channel from favourites |

Favourites are stored in `data/tvfavorites.json` next to the app, so they
survive playlist reloads and updates.

## Video player

| Button | Action |
|--------|--------|
| A | Play / pause |
| B | Stop and go back |
| Left / Right | Seek |
| Up / Down | Volume |
| L1 / R1 | Playback speed ±0.25× |

Progress, resume position and subtitles are handled automatically; the control
bar hides itself while playback continues and reappears on any input.

## On-screen keyboard

Opens for search fields, the chat composer, the terminal and the unit converter.

| Button | Action |
|--------|--------|
| D-pad | Move between keys |
| A | Type the selected key |
| L1 | Shift (upper/lower case), without losing the focused key |
| Y | Submit (send in chat, search elsewhere) |
| B / Back / Start | Close |

It also closes with its own close button, by tapping outside the panel, or when
you press **Done** / **Send**.

## Music player

| Button | Action |
|--------|--------|
| Up / Down | Move through the playlist |
| A | Play the highlighted track |
| X or Y | Play / pause |
| Left / Right | Seek −10 s / +10 s |
| L1 / R1 | Previous / next track |
| L3 (left stick click) | Show / hide synced `.lrc` lyrics |
| R3 (right stick click) | Cycle the sleep timer (off, 15, 30, 60 min) |
| B | Back |

On a keyboard, **Space** pauses, **L** toggles lyrics, **[** / **]** change the
listening speed, **T** cycles the sleep timer and **-** / **+** set the volume.

A `.lrc` file next to the track (same file name) is loaded automatically; its
timed lines appear inside the artwork square and the current line is
highlighted. Long files — audiobooks and podcasts of eight minutes or more —
resume where you stopped, and the listening speed is remembered between
launches.

## Watch Party

Host on one handheld and join from the others on the same WiFi. The host runs a
small HTTP room on port 8318 (or the next free port); the invite is the address
plus a five-character code. Anyone in the room can queue a link or press
play, pause and seek for everyone, and the panel shows presence, the shared
queue and a watch-time leaderboard.

| Input | Action |
|-------|--------|
| F8, or Tools → Watch Party | Open / close the panel |
| Up / Down | Move through the rows |
| Enter / A | Host, join, play a queued item, copy the invite or leave |
| X / Delete | Remove the highlighted queue item |
| B / Esc | Close the panel (the party keeps running) |

**Join** opens the on-screen keyboard for an invite such as
`192.168.1.5:8318 ABCD2`. **Add a link** queues any `http(s)` video URL,
including YouTube links. Playback drift beyond a few seconds is corrected
automatically; smaller differences are left alone so the picture does not
stutter. Only streams are shared — local files stay on the device.

## Pomodoro

| Button | Action |
|--------|--------|
| A | Start / pause |
| X | Skip to the next phase |
| Y | Reset the session |
| B | Back |

## Alarm

| Button | Action |
|--------|--------|
| Up / Down | Move through alarms and fields |
| A | Select / save |
| X | Toggle the focused alarm |
| Y | New alarm |
| L1 | Delete the focused alarm |
| B | Back |

## Calculator

| Button | Action |
|--------|--------|
| D-pad | Move over the keypad |
| A | Press the focused key |
| L1 / R1 | Clear (**C**) / backspace (**<**) |
| B | Back |

## Terminal

The built-in shell, useful for diagnostics over the on-screen keyboard.

| Input | Action |
|-------|--------|
| Typed command | Runs it |
| Tab | Open the on-screen keyboard (needed on a controller-only device) |
| Up / Down | Command history |
| PgUp / PgDn | Scroll the output |
| Ctrl+L | Clear the output |
| Esc | Back |
| `help` | Built-in command list |

## JukaLand

| Button | Action |
|--------|--------|
| D-pad | Move |
| A | Jump |
| B | Break block |
| X | Place block |
| Y | Enter / exit the car |
| Start | Craft menu |
| Back / Select | Exit the game |

## Desktop keyboard

| Key | Action |
|-----|--------|
| Arrows | Navigate |
| Enter | Confirm |
| Esc / B | Back |
| Q / E | Previous / next scene (not in the terminal, where they are ordinary characters) |
| Ctrl+K | Jump to the YouTube search screen |
| Ctrl+Q | Quick settings |
| Ctrl+Shift+V | Clipboard manager |
| F1 | Shortcuts overlay |
| F2 | Global search |
| F8 | Watch Party panel |
| F11 / Alt+Enter | Toggle fullscreen |
| F12 | Screenshot |
| PgUp / PgDn | Page up / down |

Mouse and touch work as well: clicking activates a card or tile, and hovering
highlights the card under the pointer.

---

## Related

- [Configuration reference](CONFIGURATION.md) — how a button in the config runs a trigger
- [Building](BUILDING.md) — running a desktop build to use the keyboard bindings
