# RetroAchievements in games

JukaHub has two RetroAchievements parts:

- **Your RetroAchievements** (the trophy page) shows your profile from the
  website: recent unlocks and progress. It needs your username and your Web
  API key, and it never changes RetroArch.
- **Settings → RetroAchievements** sets RetroArch's own achievements, the ones
  that unlock while a game plays: sign-in, hardcore, leaderboards, the unlock sound and the rest.

Achievements in games work in RetroArch games only (the cores JukaHub starts
through RetroArch). PPSSPP has its own sign-in (see the PPSSPP card).

It is new and **not yet checked on a TrimUI**: the key names and values below
come from RetroArch's own config files and source, and the unlock line from
RetroArch logs people have shared.

## Sign in for games

RetroArch needs its own login to unlock achievements; the Web API key the
trophy page uses cannot sign it in. **Settings → RetroAchievements → Sign in
for games** asks for your username and then your password (the keyboard hides
it). JukaHub writes them once into RetroArch's `retroarch.cfg`, empties any old
`cheevos_token` and switches `cheevos_enable` on.

At the next game RetroArch logs in with the password. When that works it
forgets the password and keeps a login token (`cheevos_token`) instead, written
when it saves its settings on exit. If your RetroArch does not save its
settings on exit (`config_save_on_exit = "false"`), the password stays in its
file and it logs in with it each time, exactly as after a sign-in in
RetroArch's own menu.

JukaHub never logs, shows or keeps the password in its own files. A password
with a double quote or a line break cannot be stored in RetroArch's config
format; sign in from RetroArch's menu (Settings → Achievements) instead.

The row shows the account RetroArch signs in with, or *Not signed in*. Only
the owner can sign in: it is the account of the owner's games, and a
profile's games play with achievements off.

## The settings

Every row starts on **RetroArch's own**: JukaHub writes nothing, so your setup
stays as it is until you pick a value. A value you pick is written into
`retroarch.cfg` just before a RetroArch game starts from JukaHub and put back
when the game ends, like JukaHub's other game settings. A game started from
the firmware's own menu keeps RetroArch's values. Only the owner's games get
them: a profile's games have achievements switched off, so nobody else's
unlocks land on your account.

| Row | RetroArch key | What it does |
|-----|---------------|--------------|
| Achievements in games | `cheevos_enable` | RetroArch's achievements while you play |
| Hardcore mode | `cheevos_hardcore_mode_enable` | Unlocks count as hardcore; no state loading, rewind, cheats or slow motion |
| Leaderboards | see below | Leaderboard messages and the running score |
| Challenge indicators | `cheevos_challenge_indicators` | A small badge while a challenge can still be earned |
| Unlock sound | `cheevos_unlock_sound_enable` | A chime when an achievement unlocks |
| Screenshot on unlock | `cheevos_auto_screenshot` | RetroArch saves a picture of every unlock |
| Encore mode | `cheevos_start_active` | Achievements you already have can unlock again |
| Rich presence | `cheevos_richpresence_enable` | Your profile shows what you are doing in the game |
| Badges | `cheevos_badges_enable` | Achievement pictures in RetroArch's menu |
| Verbose messages | `cheevos_verbose_enable` | RetroArch's extra messages: sign-in, game summary |

**Leaderboards** offers On, Trackers only, Notifications only and Off.
RetroArch 1.9.1 to 1.14 read one word, `cheevos_leaderboards_enable`
(`true`, `trackers`, `notifications`, `false`); the builds since early 2023
show and hide each message with `cheevos_visibility_lboard_start`, `_submit`,
`_cancel` and `_trackers`. JukaHub writes both, so the choice holds on either.
On a newer RetroArch, Off hides the leaderboards; it still sends hardcore
scores.

Offline RetroAchievements (when it serves unlocks without Wi-Fi) turns hardcore
off for that game, whatever this page says.

## Hardcore mode and JukaHub

With hardcore on, RetroArch refuses to load a state, to rewind and to apply
cheats (it turns hardcore off when cheats load). JukaHub does not promise them:

- the in-game menu's **Load state** says *Hardcore mode is on* and does not
  open; **Rewind** and **Cheats** are not offered;
- with Hardcore mode (or Achievements in games) set on this page, a game's
  cheats are not applied while hardcore is on (they come back when it is off).
  With both rows on *RetroArch's own*, RetroArch's own rule stands as before:
  the game's cheats load and RetroArch turns hardcore off for that game, so
  JukaHub offers Load state and Cheats again;
- the game page's **start from a save state** says hardcore is on;
- a sleeping game is never powered off by *Power off after sleeping*: its state
  could not come back at power-on, so the handheld sleeps on. A power off
  while the game runs still saves and quits it, without promising a resume.

A game with no achievements may still load states in RetroArch; JukaHub cannot
tell, so it promises less rather than more.

## When an achievement unlocks

RetroArch shows its own pop-up. JukaHub reads RetroArch's log for the game
(the one it already keeps for "why didn't my game start?"), and on each unlock
the stick lights flash, if your handheld has them. When you are back in
JukaHub a message says how many unlocked, for example *2 achievements unlocked
in Super Mario World*; it is also in the notification center.

This needs achievements on and a login in RetroArch's settings when the game
starts.

## When it does not work

- **Nothing unlocks.** Check the Sign in for games row shows your account, and
  that Achievements in games is On (or On in RetroArch's own menu). RetroArch
  needs Wi-Fi for its first login.
- **"Login failed" in RetroArch.** Sign in again from JukaHub: it empties the
  old token.
- **No message after the game.** JukaHub's log has a line `achievement <id>
  unlocked` for each one it saw; RetroArch's log for the last game is
  `gamediag/retroarch.log` in JukaHub's data folder. Search it for
  `Awarding achievement`.

## Offline achievements

Achievements keep unlocking in RetroArch games with no internet — on a plane,
on a bus — and reach retroachievements.org when you are back online.
**Settings → RetroAchievements →
Offline achievements** is On by default.

How it works. When a RetroArch game starts from JukaHub, RetroArch's
RetroAchievements requests go through a small server JukaHub runs on the
handheld itself (`127.0.0.1`, only while games play; RetroArch's
`cheevos_custom_host` is set for that one launch and put back after it, so a
game started from the firmware's menu talks to the website as before).

- **With internet**, every request goes on to retroachievements.org, and
  JukaHub keeps what a later game needs offline: your sign-in, which game a
  ROM is, its achievements and which ones you already have.
- **Without internet**, JukaHub answers those from its copy, and each unlock
  waits on the card. They are sent by themselves once JukaHub is online again
  (with how long ago they happened, where the website accepts it), and a
  message says how many went.

What you need to know:

- **A game must have been played once with internet** before it works
  offline. **Cache recently played games** (press A on the row) fetches the
  achievements of your recent cartridge games — Game Boy, GBA, NES, SNES,
  Mega Drive, N64, PC Engine and the like — so they work offline at once.
  Disc and arcade games need one online play.
- **Offline unlocks are Casual** (softcore). A game you play in hardcore goes
  straight to the website whenever there is internet; with no internet, a
  game ready offline plays in Casual for that time (a message says so), and
  you can still earn the hardcore unlocks later. Leaderboards need the
  internet.
- **Send N unlocks now** (in the same sheet) sends the waiting unlocks at
  once; **Clear the offline cache** forgets the games, never the waiting
  unlocks.
- If RetroArch already names another server (`cheevos_custom_host`, for
  example a proxy of your own), JukaHub leaves it alone.

The copy, the sign-in (the login token RetroArch uses, never your password)
and the waiting unlocks are in `ra-offline/` in JukaHub's data folder. Only
RetroArch games use it; PPSSPP's own achievements still need the internet.

## Wait for Wi-Fi

**Settings → RetroAchievements → Wait for Wi-Fi before achievement games** (On
by default): right after the handheld wakes, Wi-Fi takes a few
seconds to come back. A RetroArch game with achievements started then waits up
to 8 seconds for it — a message says so; press Play again to start at once —
so RetroArch can sign in. A game ready offline does not wait unless hardcore is
on (offline it would play in Casual), nor one started
with Wi-Fi off or a profile's game, and after a wait that found no Wi-Fi the
next starts in the following two minutes do not wait either.
