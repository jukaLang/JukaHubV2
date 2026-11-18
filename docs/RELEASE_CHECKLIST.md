# Device release checklist

Run this walk on a **stock Smart Pro** and a **Brick** before tagging a
handheld release. Budget about 20 minutes per device. Record the build commit,
firmware, SD card image, start/end time, result and log location for each run.
Repeat a failed step after collecting logs; do not count a source build or an
SSH probe as a screen, sound or button pass. Use
[HARDWARE_VALIDATION.md](HARDWARE_VALIDATION.md) for model-specific findings.

| Time | Check on each device | Pass evidence |
|---|---|---|
| 0–2 min | Cold boot into JukaHub, then open About → Drivers & hardware | Correct model and panel, usable Home, no black/flickering frame; record driver status and firmware |
| 2–4 min | Press every direction and A/B/Start/Select, adjust brightness and volume, plug in headphones | Focus and actions match labels; display and audio change cleanly |
| 4–6 min | Join Wi-Fi, fetch a small online item, sleep for one minute with the power key, wake | Clock and network return promptly; one wake event and no stuck input or sound |
| 6–11 min | Launch and leave one RetroArch game, one standalone emulator (PPSSPP or DraStic), one PortMaster port and one native app, using installed test content | Each has picture, sound and controls, returns to JukaHub, and leaves no stray helper; record unavailable families as gaps |
| 11–13 min | Play a local video for at least 30 seconds and leave the player | Smooth picture and sound, then normal UI and audio route |
| 13–16 min | Install or update a small item, verify it opens, then use the documented rollback or restore path | Update is verified before replacement; rollback restores the earlier working item |
| 16–18 min | Plug in a USB keyboard; type in search and a text field, then unplug | Text and navigation work, and the built-in pad resumes |
| 18–20 min | Quit JukaHub by Start menu and launch it again; repeat with the pad exit chord | Exactly one stock MainUI appears after each quit, and JukaHub relaunches; save `errors.txt` and the log bundle |

On Windows, connect a controller, navigate Home, open and leave a game, and
type with the keyboard. Keep this result beside the two handheld records.

Before publishing, review the two device logs for renderer, display holder,
sleep/wake, audio and update errors. Record any skipped family or failed step
in the release notes with its device and firmware; rerun the affected step
after a fix.
