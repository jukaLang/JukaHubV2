# Game hotkeys

RetroArch has shortcuts for saving a state, fast forward, quitting and more:
the **hotkey button** held with another button. Every firmware ships its own
set, and changing them means digging through RetroArch's menus, where the
change then holds for every game. In JukaHub they are rows: for every
console, for one console, or for one game. A game's choice wins over its
console's, and a console's over All consoles.

Nothing is written for good. Before a RetroArch game starts from the library,
JukaHub writes the hotkeys into retroarch.cfg for that launch, and puts the
firmware's back when RetroArch exits. RetroArch started from the firmware's own
menu keeps its own hotkeys. Standalone emulators and NextUI's paks are left
alone.

It is new and **not yet checked on a TrimUI**: see
[When a hotkey does not work](#when-a-hotkey-does-not-work).

## Where to change them

**Settings → Controls**, below **Game buttons**:

| Row | What it does |
|---|---|
| **Hotkeys for** | *All consoles*, or one console that then gets its own hotkeys |
| **Hotkey button** | the button held with every shortcut: Select, Menu, Start, L1, R1, L2 or R2, and L3 / R3 where something sends them (sticks that click; F1 / F2 on a Brick, named so there). The original Smart Pro has neither, so they are not offered |
| **Shortcut** | which shortcut the next row changes; **A** lists them all with their buttons |
| **Its buttons** | that shortcut's button |
| **Clear these hotkeys** | the console (or All consoles) back to the defaults; shown once something is set |

A console's rows start at **Default · …**, which follows All consoles and
shows what that is. **RetroArch's own** (the default) writes nothing, so the
firmware's hotkey stays.

For **one game**, open its options sheet (Start in the library): **Hotkey**
picks the shortcut (Left/Right), **Hotkey buttons** its button for this game
(Left/Right, A goes round). Default follows the console.

## The shortcuts

| Shortcut | RetroArch's setting |
|---|---|
| Hotkey button | `input_enable_hotkey` |
| RetroArch menu | `input_menu_toggle` |
| Quit game | `input_exit_emulator` |
| Save state / Load state | `input_save_state` / `input_load_state` |
| Previous slot / Next slot | `input_state_slot_decrease` / `input_state_slot_increase` |
| Fast forward | `input_toggle_fast_forward` (press to turn on and off) |
| Fast forward (hold) | `input_hold_fast_forward` |
| Rewind | `input_rewind` (only where RetroArch's rewind is on) |
| Screenshot | `input_screenshot` |
| Pause | `input_pause_toggle` |
| Restart game | `input_reset` |
| Translate screen | `input_ai_service` (RetroArch's AI service) |

Each is written as `input_<name>_btn`, or `input_<name>_axis` when the button
is an axis (the Smart Pro's L2 and R2), with the other one unbound.

## Buttons JukaHub keeps

With **Select** as the hotkey button, some pairs are JukaHub's own during a
game, so they cannot be a shortcut:

| Pair | JukaHub uses it for |
|---|---|
| Select + L2 | the in-game menu (while it is on) |
| Select + R2 | the game assistant (while it is on) |
| Select + R1 | the screenshot (while it is on) |
| Select + L1 | the games mouse (while Mouse in games and apps is on) |
| Select + Start | force quit when held; only **Quit game** may sit there |

Left/Right skips a button that cannot be used; the list (A) still shows it
with the reason, and picking it there is refused with a message. The same
goes for the hotkey button itself and for a button another shortcut already
has. When levels disagree (a console's Save state on X, a game's Load state on
X), the game's wins for that launch and the other is skipped.

When a shortcut takes a button one of the firmware's own hotkeys used, that
hotkey is unbound for the launch, so one press never does two things. The
same goes for the button you make the hotkey button, and when JukaHub makes
Select the hotkey button, firmware hotkeys on the pairs above are unbound too.

A RetroArch with no hotkey button at all fires every hotkey on its button
alone, so there no shortcut is written (the game's row says so) until you
choose a **Hotkey button**. And a Hotkey button is not set on a button where
the pad's autoconfig profile keeps one of the firmware's hotkeys (its Quit on
Start, say): that hotkey would then fire at every press.

## How the button codes are found

A hotkey is a number (`input_save_state_btn = "3"`), and the number for X
differs between firmwares and joypad drivers. JukaHub never guesses it: it
copies player 1's own bindings from retroarch.cfg (`input_player1_x_btn` and
the rest) and, for buttons the config leaves out, from the handheld's
autoconfig profile (a file with "trimui" in its name under RetroArch's
autoconfig folder). The Menu key has no player binding: its number is the
firmware's own hotkey or menu binding when no other button uses it.

## When a hotkey does not work

After a launch that could not apply something, the game's **Hotkey buttons**
row says what, and the log (tag `hotkeys`) says why, for example
"JukaHub cannot read B's code in RetroArch's controls". Bind the button once
in RetroArch's own Controls menu (or keep the firmware's autoconfig profile)
and start the game again.

A firmware hotkey that lives only in the autoconfig profile cannot be unbound
from retroarch.cfg; if a press does two things, give one of them another
button.

## Files

| File | What |
|---|---|
| `Apps/JukaHub/data/game-hotkeys.json` | the choices: `{"global": {"hotkey": "select", "exit": "start"}, "consoles": {"snes": {"save": "x"}}, "games": {"<rom path>": {"ffHold": "r2"}}}`; a value is a button (`a` … `r3`, `start`, `select`, `menu`, `up` … `right`) or `own` |
| `Apps/JukaHub/data/retroarch-launch-scope.json` | the values to put back after the game ([Looks](LOOKS.md) and the other launch settings use it too) |

Rename file and the Game Boy Color move carry a game's hotkeys; Reset game
and Delete game drop them.
