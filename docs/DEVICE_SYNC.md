# Device Sync

Two paired JukaHubs can make more than their saves the same, over Wi-Fi, with
nothing in between. Pair them from **Tools → Sync** ([Sync](SYNC.md): open it
on both, or pair from the phone page its QR code opens). On that page **What
syncs with a paired device** has one switch per category, and **Compare** on a
paired device lists what each switched-on category would do under **Sync
now**. Saves and save states also move by themselves around games while both
are on the same Wi-Fi ([what happens by itself](SYNC.md#what-happens-by-itself));
BIOS, ROMs, collections and settings move only from Compare.

> Status: built 2026-11-20 (`player/devsync.go`, `devsync_data.go`,
> `devsync_ui.go`); not yet run between two devices.

| Category | Starts | What happens |
|---|---|---|
| Saves | on | Save sync's rules ([CLOUD_SYNC.md](CLOUD_SYNC.md#the-rules) has the cloud version): newest wins, both kept when both changed, a safety snapshot first |
| Save states | on | the same, for the state folders (a save folder whose name says "states") |
| BIOS | on | firmware files one device lacks are copied to it; a file both have is never replaced, even when it differs; nothing is deleted |
| Collections | on | merged by name both ways: every game of both, in the order of the device that changed the list last |
| Recents & play time | on | Continue lists merged by last played (games on that card only); per-game play time keeps the higher figure |
| ROMs | off | games one device lacks, console folder by console folder; the comparison shows each console's count and size, A on a console leaves it out, and Sync now asks once more with the total |

BIOS and ROMs copy only when the switch is on on **both** handhelds: a device
with the switch off answers neither lists nor copies. Ports folders (`PORTS`,
`Ports (PORTS)`, `PortMaster`) and shell scripts never travel as ROMs, because
they run as programs.

**Send my settings** (same section) sends this device's theme, colours,
wallpaper, Home layout and tabs, language, typeface, text size, interface
sounds and music, and Accessibility to one paired device, after a confirm. The
other device keeps them waiting on its Save sync page until its user chooses
**Use them**. Wi-Fi, accounts and passwords, the name Home greets, the pad
remap, power and brightness and every other device-specific setting never
travel; a wallpaper or theme file that is not on the other card is left as it
was there.

## How it travels

The same paired HTTPS connection as Save sync and handoff (TLS 1.3, both keys
pinned, port 8322): no new port opens. The device that compares decides what
moves; the other only needs Nearby devices on.

- Large copies resume: a dropped BIOS or ROM copy keeps its part in a hidden
  `.jukadevsync` folder inside the target folder and goes on from there at the
  next sync, as long as the source file is still the same (size and time); a
  file that changed meanwhile starts over. A copy only lands once complete
  (rename into place).
- A card short of space stops the copies with "card is full" (64 MB is kept
  free).
- Games travel as paths from the card root, so the same game on two cards is
  one entry in a collection or the recents.
- An older JukaHub on the other side answers only saves; the comparison says
  "has an older JukaHub" and the rest waits until it is updated.
- A USB cable cannot carry a sync: the stock TrimUI kernels have no USB network
  gadget (RNDIS/ECM).

## Where it reads and writes

| What | Folders |
|---|---|
| BIOS | the folders the BIOS doctor checks that are on the card: RetroArch's `system_directory`, `<card>/BIOS`, `<card>/RetroArch/.retroarch/system`, JukaHub's own `BIOS` |
| ROMs | `<card>/Roms` and JukaHub's own `Roms`; a console is a folder right under it; picture folders (`Imgs`, `.media`…), `map.txt`, `gamelist.xml` and caches are left out |
| Collections | `collections.json` (change times in `collections-nextui.json`) |
| Recents & play time | `game-recents.json`, the play time in `jukauser.json` |
| Switches | `devsyncCategories` in `jukauser.json` |
