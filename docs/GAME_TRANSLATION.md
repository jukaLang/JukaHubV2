# Translate the game screen

A Japanese RPG, a German adventure, a menu you cannot read: JukaHub can put a
game's on-screen text into your language while you play. It is RetroArch's
**AI service** (Batocera calls it *AI game translation*), set up for
you: you ask, the screen is read and translated, and the translation shows
over the game like subtitles or is read aloud.

It is new and **not yet checked on a TrimUI**: the RetroArch settings below
come from RetroArch's documentation. If nothing shows, see
[When it does not work](#when-it-does-not-work).

## Turning it on

**Settings → Voice & AI → Translate games**. It is off until you turn it on,
because it sends a picture of the game screen to an AI service (see
[Privacy](#privacy)). While it is on, four more rows show:

| Row | What it does |
|-----|--------------|
| Translate to | The language the text is put into. *Same as JukaHub* (the default) follows Settings → Language; or pick English, Español, Português, Français, Deutsch, Italiano or Русский |
| Show the translation as | **Text on screen** (the default): subtitles over the running game. **Read aloud**: the handheld's own voice reads it, while the game runs on. **Picture over the game**: only with your own service (below) |
| Translation service | **JukaHub (your Groq key)**, the default, or **My own service** |
| Service address | Your own service's address, e.g. `http://192.168.1.20:4404` (only with *My own service*) |

It works in **RetroArch games started from JukaHub's library** (not a
standalone emulator, not a game started from the firmware's own menu).

## Translating

In the game, hold **Select + L2** for the in-game menu and choose **Translate
screen**. The menu closes, the game runs on, and a moment later the
translation appears (or is read aloud). Choose it again for the next screen
of text. A translation takes a few seconds; it needs Wi-Fi.

If you set a RetroArch hotkey for **AI translate** (custom RetroArch hotkeys),
it does the same without the menu — also on a PC, where JukaHub has no
in-game menu.

**Text on screen** needs RetroArch 1.17 or newer (the Smart Pro's own
RetroArch is newer). JukaHub turns RetroArch's notifications and graphics
widgets on for that game, whatever the in-game OSD preset says: the subtitles
and any error message are drawn by them.

**Read aloud** with JukaHub's service is English only: the handheld's voice
(Settings → Voice & AI → Voice) is an English one. Pick it when the
translation goes into English and the voice is installed; otherwise the
translation shows as text.

**Picture over the game** pauses the game under the translated picture; choose
Translate screen (or press the hotkey) again to play on.

## Which service

**JukaHub's own** is a small service inside JukaHub that runs only while the
game does. It listens only on the handheld itself (127.0.0.1), on a port and
address picked fresh for each game. RetroArch hands it the game's picture; it
asks the vision model JukaHub's AI already uses (Groq, with the key in
**Settings → General → Groq API key**) for the screen's text in your language
and hands the answer back. A small console picture (a Game Boy's 160 lines) is
enlarged first, pixel for pixel, so the model can read pixel fonts. Without a
key, offline, or when Groq is busy, RetroArch shows a one-line message saying
so.

**Your own service** is any RetroArch-compatible AI service — a
[vgtranslate](https://gitlab.com/spherebeaker/vgtranslate) or a
[retroarch-ai-translator](https://github.com/CrazyKitty357/retroarch-ai-translator)
on your PC, ZTranslate with your key. RetroArch talks to it directly; JukaHub
only writes its address, with `target_lang=<your language>` added to it unless
it already has one. Picture mode is offered here because such services draw
the translated picture themselves.

## Privacy

The picture of the game screen leaves the handheld only when you ask for a
translation, and only to the service you chose: Groq with your key, or your
own address. JukaHub keeps neither the picture nor the translation and does
not write them to its log (only how long a translation took, or why it
failed). Groq's own handling of what it is sent is in its terms.

## What JukaHub writes to RetroArch

For that one game, in `retroarch.cfg` (your own values go back when the game
ends, like JukaHub's other per-game RetroArch settings):

| Key | Value |
|-----|-------|
| `ai_service_enable` | `true` |
| `ai_service_mode` | `3` text, `1` speech, `0` picture |
| `ai_service_url` | JukaHub's service, or your address with `target_lang` |
| `ai_service_source_lang`, `ai_service_target_lang` | `0` (the language travels in the address instead) |
| `ai_service_pause` | `true` for picture mode, else `false` |
| `network_cmd_enable` | `true` (the in-game menu sends `AI_SERVICE` to RetroArch's command port) |
| `menu_enable_widgets`, `video_font_enable` | `true` (text and picture modes) |

JukaHub writes no button binding (`input_*` key).

## When it does not work

- **No Translate screen row in the in-game menu.** It shows only in a
  RetroArch game started from the library, with Translate games on. JukaHub's
  log says `translation ready for <game>` at the start; `no usable custom
  service address` means the address in Settings needs fixing.
- **Nothing appears.** RetroArch older than 1.17 has no text mode (the log
  says so at the start of the game); RetroArch built without the AI service
  ignores it. Try *Read aloud*, or your own service in picture mode.
- **"No AI key"**, **"No internet"**, **"No text to translate"**: what they
  say. The first is fixed in Settings → General → Groq API key.
- **"Set RetroArch's AI Service Output to Text Mode"**: the AI service mode
  was changed in RetroArch's own menu during the game; JukaHub's service
  answers with text or speech, not pictures.
