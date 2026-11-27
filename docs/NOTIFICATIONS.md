# Notifications and downloads

The notification centre (the bell on the status bar) holds a **live download
row** while an install is running. It shows the operation, bytes received, a
percentage and a progress bar, and updates in place — a percent tick does not
push a new row or a new toast. The row is removed when the run finishes or
fails, and is never written to `notifications.json`.

The top-centre install card and the activity-strip chip read the same run
state as that row. They are three views of one download, not three different
progress values. A narration voice install used to add a fourth view ("25%
downloaded" as a toast); that toast is gone — the quarterly sound remains.

## Resuming downloads

Downloads that use JukaHub's common helpers keep a `.part` file beside the
destination. A retry sends `Range: bytes=N-` and appends to the staged bytes.
If the server ignores the range and answers `200`, the helper starts over
instead of appending the whole body twice. A validation failure (oversized or
suspiciously small file) removes the partial; a dropped connection keeps it
for the next attempt.

The main patch/update downloader, WINE components and helper-tool downloads
all follow this rule. Media queue downloads are resumed by yt-dlp itself.

## Parallel queue and Wi-Fi

The media queue fetches up to two files at once. Each job has its own cancel
function, so pausing the queue stops all of them and removing one row stops
only that file. Subtitle steps still wait until the queue is idle and run one
at a time. The activity strip already shows one chip per active file.

Emulator and Windows-emulator update batches do **not** switch Wi-Fi on or off
per core: they only require that the device is online, and the whole batch runs
under one update operation. A Wi-Fi keeper hold now spans the batch so a
background reconnect cannot move the radio under a running transfer.
