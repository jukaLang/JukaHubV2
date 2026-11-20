# TV mode

The TrimUI Brick Pro can send its picture to a TV or a monitor through its
USB-C port (DisplayPort alt mode), with a USB-C to HDMI cable or a dock. The
handheld's firmware drives that output; JukaHub does not. TV mode is what
JukaHub does when a TV is the screen: larger text, an outside controller set
up as player 1, and a toast when the TV comes and goes.

This is a first version, built without a Brick Pro on a TV. JukaHub watches
every place the kernel may report a connected display and writes what it sees
to its log, so it learns the real path from the first owner's log. Until then,
treat the automatic part as **not verified on a Brick Pro yet**.

## The setting

**Settings → Display → TV mode**:

- **Auto** (the default) turns TV mode on while a TV is connected, and off
  when it goes. It only watches on a model with a video out (the Brick Pro).
- **On** shows the TV interface on any device, TV or not. It is the way to try
  it, or to use it when Auto does not notice your TV.
- **Off** stops the watch: the TV, if any, shows the handheld's own interface.

When a TV connects, a toast says **TV connected — TV mode on**; when it goes,
**TV disconnected — TV mode off**. Two readings two seconds apart must agree
before either happens, so a loose cable does not flicker the interface.

The rows under it, wherever TV mode can turn on (On on any device, Auto on a
Brick Pro):

- **TV connection** — what the watch reads now: Connected, Not connected,
  Checking, or Unknown when the firmware has none of the status files JukaHub
  knows. The line under it names the file that said so.
- **TV text size** — 115%, 130% (the default) or 145%. Text grows by this much
  while TV mode is on. Your own **Text size** (the row above, and
  [Accessibility](ACCESSIBILITY.md)) is not changed, and the two together never
  go past Accessibility's largest size.
- **Controller is player 1** — see below. On by default.
- **Pair a controller** — opens the Bluetooth page. Its line lists the
  controllers connected now. A USB controller just plugs in (into the dock, or
  through a USB-C hub).
- **Built-in screen off on the TV** (Brick Pro only, off by default) — switches
  the handheld's backlight off while a TV is connected, to save battery. It only
  acts when a TV was actually seen, never from **On** alone. We do not know yet
  whether the firmware shows the same picture on both screens or makes the TV
  the only one: if the TV goes dark too, turn this off. When the TV is
  unplugged the screen lights again within a few seconds, in a game too.
- **Restart to fit the screen** — appears when the screen's shape no longer
  matches the one JukaHub started with (see below).

## Controllers

JukaHub's menus take the buttons of every connected controller, the
handheld's own included, so an outside pad works on Home and in Settings at
once.

For games, while TV mode is on and **Controller is player 1** is on, a game
that runs in RetroArch and is started from JukaHub gets this player order:
the outside controllers first (USB or Bluetooth, in the order they were
connected), then the handheld's own buttons. JukaHub writes RetroArch's
`input_playerN_joypad_index` keys for that one launch and puts your own values
back when the game closes, the same way as its other launch settings. A new
controller in TV mode gets a toast that says which player it is.

Two limits:

- The controller numbers are the ones JukaHub's own controller list uses,
  which is RetroArch's order on a TrimUI as far as we know. If the handheld
  stays player 1 on your device, turn the row off and tell us (the log has a
  `tvmode` line with the order that was written).
- A Local play session (**Tools → Local play**) with two or more players sets
  the order itself, and TV mode leaves it alone. Games outside RetroArch (PortMaster
  ports, PPSSPP, DraStic, NextUI's own emulators) set up their own controllers.

When a TV connects and no outside controller is there, the toast says so and
points at **Pair a controller**.

## The picture's shape

The Brick's panel is 4:3, and JukaHub draws a 4:3 layout for it. A TV is
usually 16:9. What happens depends on the firmware, which we have not seen yet:

- If it shows the panel's picture on the TV, the TV gets the 4:3 layout with
  bars at the sides, and nothing else changes.
- If the screen becomes 16:9 while JukaHub runs, the layout cannot change under
  the running window. JukaHub notices, shows **The screen is now 1920x1080 —
  restart JukaHub to fit it**, and **Restart to fit the screen** appears in
  the TV mode rows.
- When JukaHub starts with a 16:9 screen on a Brick Pro (with TV mode on Auto
  or On), or on another 4:3 model with TV mode On, it draws the wide layout
  from the start.

## For testers

The drivers page (**Settings → About → Drivers & hardware**) has a **Video
out** row that reads the status files fresh each time it opens: a display
connected (and which file said so), nothing connected on the files that exist,
or no such file at all. The JukaHub log has a `tvmode` line each time the
reading changes, and one list of the kernel's `/sys/class` folders, which is
where a cable shows up when none of the known files does.

JukaHub reads, and never writes:

- USB Type-C: a partner's DisplayPort alternate mode (SVID `ff01`) under
  `/sys/class/typec`, with its `active` or `displayport/hpd` file;
- extcon cable states (`/sys/class/extcon/*/state`, `DP=1` or `HDMI=1`);
- Android's `/sys/class/switch/hdmi/state`;
- DRM connectors (`/sys/class/drm/card*-DP-*/status`, `card*-HDMI-*/status`);
- Allwinner's display driver dump (`/sys/class/disp/disp/attr/sys`), which
  lists each screen's output, and `/sys/class/hdmi/hdmi/attr/hpd`;
- the framebuffer's size (`/sys/class/graphics/fb0/mode`, `virtual_size`): a
  framebuffer that had the panel's size and then changed counts as a TV too.
