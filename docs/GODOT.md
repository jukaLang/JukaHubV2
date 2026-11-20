# Godot games

Many indie games are made with the [Godot](https://godotengine.org) engine. A
Godot game's PC download usually holds two files: the program (`Game.exe`,
`Game.x86_64`) and the game itself, `Game.pck` — its scenes, scripts and
assets, with no engine inside. On a TrimUI handheld that `.pck` plays in an
engine built for the device.

JukaHub uses the community's
[TrimUI Smart Pro Godot port](https://github.com/ogregorio/trimui-tsp-godot-port)
for that (it grew out of the Godot launcher in dokoma's
[Kiwi add-on collection](https://github.com/dokoma/kiwi)). The port is a
launcher folder, `Emus/GODOT`, and a games folder, `Roms/GODOT`: the layout the
TrimUI firmware's own menu reads, so the games also show up there.

## Setting it up

On the handheld, open **Emulators** and find the **Godot** tile (or search
Home for "Godot"). Press **X** (or **A**): JukaHub downloads the port from
GitHub (Wi-Fi needed, once) and:

- lays its `Emus/GODOT` folder over the card's. Files of your own in that
  folder stay; a port file you had edited (a changed `launch.sh`) is replaced,
  and your copy is kept in `Emus/GODOT/.jukahub-replaced/<date>` — JukaHub
  records each port file's checksum to tell your edits from the port's own
  files;
- makes `Roms/GODOT` when the card has none, and copies in any files the port
  ships for it that the card does not have yet — never over a file of yours;
- marks the port's scripts and programs runnable.

The install card shows the download. Once the port is there the tile reads
**Godot port**, with the engine versions it carries when their file names say
so, **A** opens the Godot games, and **X** fetches the port again (an update).
JukaHub notes where the port came from in its data folder (`godot-port.json`).

When the standalone port is gone from GitHub (or its download holds no
`Emus/GODOT` launcher), JukaHub tries the Kiwi collection's own `Emus/GODOT`,
unless its launcher needs the rest of Kiwi (Kiwi's `/kiwi` folder), which
JukaHub does not install. A download that fails for another reason (Wi-Fi,
GitHub's rate limit) is reported as it is; try again later.

The port was made for the Smart Pro. The Brick, Brick Pro and Hammer have the
same chip and GPU and get the tile marked **untested on this model**; so does
the Smart Pro S, whose chip and GPU differ (A523, Mali). There is no Godot tile
on a PC.

## Adding games

Put each game's `.pck` in **`Roms/GODOT`**. The port also reads, beside a game
of the same name:

- `Game.png` — its picture, which JukaHub shows as the game's cover too;
- `Game.gptk` — its pad-to-keys map, for games that only read the keyboard;
- `Game.runtime` — which of the port's engines plays it.

Only the `.pck` files are listed in **Games**, on the **Godot** shelf. A `.pck`
dropped from the phone (companion page) goes into the card's Godot folder once
there is one, and **A** on a `.pck` in **Files** starts it the same way.

A `.pck` plays only in an engine of its own version: a pack made with Godot 3.5
needs a Godot 3.5 engine (an engine of the same major version and the same or a
newer minor version usually works), and a Godot 4 pack never plays in a Godot 3
engine. Handheld Godot ports mostly carry Godot 3 engines (FRT), so many recent
Godot 4 games will not start. A game whose PC download has no `.pck` was built
with its pack inside the program and cannot be played this way either.

## When a game does not start

Games start through the port's `launch.sh`, exactly as the firmware's menu
starts them. JukaHub reads the engine version from every `.pck`'s header and
writes it into the log at each start, with the engines the port carries. When a
game ends at once, the launch report says so if:

- the file is not a Godot pack at all (a renamed file, or a pack taken out of
  something else);
- it was made with a Godot version the port has no engine for — for example a
  Godot 4.3 game on a port of Godot 3.5 engines;
- it was made with Godot 4 and the port's engine versions are not known.

The launch script's own output is in the report as well.
