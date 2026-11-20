# Profiles

A handheld that more than one person plays can keep **each person's own
saves, Continue list, trophies and look**. Someone who only wants to try a
game can play as a **Guest**, and nothing they do is kept.

Profiles are off until you make one. With no profile, JukaHub works exactly as
it always has: no question at start, no chip on the top bar, no new menu row.

## The owner

Everything JukaHub already keeps — your saves, your Continue list, your
trophies, your settings — is **your** profile, the owner's. Nothing is moved
or copied when profiles are added: your files stay where they always were, and
deleting every other profile leaves you exactly where you started.

## Adding someone

**Settings → General → Profiles → Add a profile.** Type a name on the
keyboard and press Done. Up to seven people can have a profile besides you.

A new profile starts with your look (theme, wallpaper, text size, language…)
and an empty library: no Continue list, no playtime, no trophies, no
favourites. From then on it is theirs to change.

In the same list, pick a person to **switch to** them, **rename** them, give
them a **colour**, see where their **saves** are, or **delete** them.

## Switching

- **Start → Device → Switch profile** (F10 on a keyboard), or tap the chip
  beside the Menu pill on the top bar. "Who is playing?" lists everyone and
  Guest; Up/Down and A (Enter) pick, B (Esc) closes.
- **Ask who is playing at start** (in Settings → Profiles) opens the same
  list every time JukaHub starts. It is off by default: JukaHub then starts as
  whoever played last.
- A switch waits until the game is closed.

The chip on the top bar shows who is playing — an initial in their colour and
their name, or **GUEST** in amber during a Guest session. It only appears once
there is a second profile.

## What belongs to each person

| Each person's own | Shared by the whole device |
|---|---|
| Continue list and game history | Wi-Fi, VPN, DNS, Bluetooth |
| RetroArch saves and save states | volume, brightness, power settings |
| playtime and JukaHub trophies | installed emulators, cores and packages |
| favourites and "recently played" marks | game options, collections, notes, alarms, chat |
| theme, wallpaper, Home layout, menu colour | the A/B button fix of the handheld |
| language, typeface, text size, all of Accessibility | RetroAchievements sign-in (the owner's) |
| interface sounds, background music | |
| the name Home greets | |
| words the keyboard learned, browser bookmarks | |

**RetroAchievements** stays the owner's account. Other profiles and Guests play
with RetroAchievements switched off for the game, so nobody else's unlocks are
credited to you.

## Saves

- **RetroArch games** (most consoles) save to the profile's own folders:
  `profiles/<id>/saves` and `profiles/<id>/states` in JukaHub's data folder.
  RetroArch's own settings are only changed for that one launch and put back
  when the game closes — games started from the firmware's own menu still use
  your folders.
- **Standalone emulators** (PPSSPP, DraStic and others) and PortMaster ports
  keep one save folder that JukaHub cannot point elsewhere, so their saves are
  **shared with the owner**. JukaHub never lets that happen silently: the
  first time each one is started in a profile it stops and says so; start the
  game again to play. The profile's page lists these emulators under
  "Saves shared with …". PortMaster ports start without that notice: their
  saves are always the owner's.
- **Save Manager**, cloud sync (Google Drive, RomM) and backups work on the
  owner's saves. Save Manager, restoring a backup and Settings → About →
  Reset ask you to switch to the owner first; a backup made while someone else
  plays is still the owner's.

## Guest

Pick **Guest** in "Who is playing?" (or Settings → Profiles → Guest → Start
a Guest session) to hand the handheld to someone for a while:

- nothing is recorded — no Continue list, no history, no playtime, no
  trophies (and no trophy celebrations), no RetroAchievements;
- RetroArch saves go to a scratch folder that is **wiped** when the session
  ends: when you switch to someone else, or at the next start;
- the look is the owner's, read-only: changes made as Guest are not kept;
- standalone emulators warn first, as above — a Guest's save there would be
  the owner's save.

## Deleting a profile

Settings → Profiles → the person → **Delete profile** asks twice. Their folder
(saves, states, history and settings) is not erased: it moves to
`profiles/.deleted/` in JukaHub's data folder, where Files can still reach it
if you change your mind. The profile that is playing cannot be deleted —
switch to someone else first.

## Where things are kept

- `profiles/profiles.json` in JukaHub's data folder — the list (names,
  colours, who played last, the start-up question). No personal data.
- `profiles/<id>/` — one person's settings and library (`profile.json`), their
  Continue list, game history, learned words, bookmarks and RetroArch saves.
- `profiles/.guest/` — a Guest session's scratch folder, removed when it ends.
- The owner's data is where it always was.
