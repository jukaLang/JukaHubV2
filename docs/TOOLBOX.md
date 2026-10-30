# Toolbox

Tools → **Toolbox** installs a small, curated set of popular command-line tools
on the card and runs them from the app. It exists because the useful half of
"just copy the binary on there" is deciding *which* binary matches the device,
fetching it safely and remembering what you installed.

## What is in the catalog

| Tool | Use it for | Licence |
|---|---|---|
| `rclone` | Sync saves or folders to a cloud, an SSH host or another card | MIT |
| `jq` | Filter and reshape JSON in a script or a snippet | MIT |
| `ripgrep` (`rg`) | Search a ROM folder or a config tree quickly | MIT / Unlicense |
| `fd` | Find files by name without `find` syntax | MIT / Apache-2.0 |
| `pandoc` | Convert Markdown, HTML and EPUB between formats | GPL-2.0 |
| `age` | Encrypt a save file or a note with a passphrase or a key | BSD-3-Clause |
| `naabu` | Scan a host or a LAN range for open TCP ports | MIT |
| `httpx` | Check which hosts answer HTTP, with status, title and server | MIT |
| `dnsx` | Resolve names and walk DNS records for a host | MIT |

The last three send traffic to other machines, so they are the only entries in
the catalog behind the one-time prompt described in
[Network scanning](#network-scanning).

The three helper tools JukaHub needs for media (`yt-dlp`, `ffmpeg`,
`ffprobe`) are separate: those install themselves on first need. `ffplay` is
no longer fetched — the embedded player decodes with `ffmpeg`, and BtbN's
`ffplay` needs X11, which the handheld does not have. See
[DEPENDENCIES.md](DEPENDENCIES.md).

## Installing and running

Open **Tools → Toolbox** (the Tools card on Home, or the `tools_open` trigger).

| Button | What it does |
|---|---|
| **A** | Installs the highlighted tool the first time; runs it afterwards |
| **X** | Opens the on-screen keyboard for the argument line, then runs |
| **Y** | Copies the installed path to the clipboard |
| **B** | Back, then closes |

The output opens in the same viewer the network probes use: **Up/Down** scroll,
**X** switches to a hex view, **Y** pretty-prints JSON, **B** goes back to the
toolbox. A run is bounded by the snippet timeout, so a tool waiting for stdin
cannot hang the panel.

Arguments are split without a shell: whitespace separates, `"…"` and `'…'` group
words, and a backslash escapes the next character. Nothing is expanded, so a
`;`, a `|`, a `*` or a `$(…)` reaches the tool as literal text. That is the
reason there is no "shell command" field here — Power tools → **Script snippets**
is the deliberate shell, and it asks you to save the command first.

The typed line is remembered for the session only. Close the app and each tool
goes back to its catalog example (`--version`, `version`), because an argument
line can hold a path or a passphrase and a stale one from last week is worse than
a default.

## Network scanning

`naabu`, `httpx` and `dnsx` are recon tools: they work by sending packets to
hosts other than the one in your hand. Installing them is harmless and is not
gated; **running** one asks first.

On the first run of any of the three, the row shows a toast — *"Only scan
networks you own or may test — press again to accept"* — and does not start.
Pressing the same button again within five seconds records
`networkScanAuthorized: true` in `jukauser.json` and runs the tool. From then on
every scanning tool runs immediately, and the prompt never returns. Deleting
that key from `jukauser.json` (or setting it to `false`) brings the prompt back,
which is what to do before handing the card to somebody else. The row itself
always carries the warning: `… · scans other hosts`.

Each tool's default run is `-version`, because with no arguments `httpx` and
`dnsx` read from stdin and would sit until the snippet timeout. Actual use is
typed in the **X** argument editor. Examples, all against a host you control:

```
naabu  -host 192.168.1.10 -top-ports 100
naabu  -host 192.168.1.0/24 -top-ports 20 -silent
httpx  -u 192.168.1.10 -title -status-code -server
dnsx   -d example.com -a -resp
```

The tools run with the app's own privileges. A TCP connect scan (the default)
needs no privileges; `naabu`'s SYN mode (`-s s`) needs raw sockets, which the
app does not have, so it will refuse rather than silently do something else.
`httpx` and `dnsx` are pure Go builds with no runtime library needs; `naabu`'s
SYN support may link `libpcap`, which the stock images are not guaranteed to
carry — that is the one entry whose runtime on the handheld has not been
verified, and the troubleshooting table says what a loader error means.
There is no Nmap, no OS fingerprinting and no exploit catalog here: this is
port, HTTP and DNS recon, nothing that claims to find or use a vulnerability.

What you scan is your responsibility. The acknowledgement exists so that the
feature cannot be used by accident out of the box, not because the app can tell
whether a target is yours — it cannot, and it does not try.

## Using a tool from the rest of the app

Installed tools are placed on `PATH` for **Power tools → Script snippets**, so a
tool becomes a button in two steps:

1. Toolbox → install `rclone`, **Y** copies `/…/required/rclone`.
2. Power tools → Script snippets → save `rclone copy /Roms/Backups nas:/handheld`.

Plugins run with the same environment, so a plugin script can call an installed
tool by name as well. Snippets and plugins run with the app's own privileges;
only install and run what you trust ([SECURITY.md](../SECURITY.md)).

## Where the files go

- The tools folder is `required/` next to the JukaHub binary — the same folder
  the bundled helpers use. On the TrimUI package that is
  `Apps/JukaHub/required/`.
- Downloads are staged in that folder, not in `/tmp` (a small tmpfs on the
  handheld), and the finished binary is renamed into place. An existing file is
  never overwritten: a running tool would fail with `ETXTBSY` otherwise.
- A zero-byte file (an interrupted install) is treated as missing, so pressing
  **A** again repairs it.

## What it verifies, and what it does not

| Check | Done |
|---|---|
| Only allowlisted repos are contacted | yes — `allowedToolRepos` in `tools.go`, no free-text repo or URL in the UI |
| Asset choice is platform-correct | yes — catalog patterns per `goos/goarch`, matched on stable name parts |
| Checksum and signature files are skipped | yes — a `.sha256`/`.sig` side-car is never selected as the download (`naabu-checksums.txt` in a scanner release included) |
| A scanning tool runs at most after one explicit acknowledgement | yes — `networkScanAuthorized` in `jukauser.json`, checked on every run path |
| Download size is bounded | yes — 512 MB cap, and a file below 4 KB is treated as truncated |
| Archive entries stay inside the destination | yes — the shared extraction checks |
| Upstream hash or signature | **no** — the trust boundary is GitHub TLS, the upstream project and the allowlist |

That last row is the honest gap: the Patch/package path has its own signed index,
and the toolbox does not. The reasoning, and what to do instead if that matters
for your deployment, is in
[SECURITY.md](../SECURITY.md#toolbox-and-helper-tool-downloads).

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| "Offline — connect WiFi to install" | The install is refused rather than failed; join a network and press **A** again |
| "github api rate limit exceeded" | Unauthenticated GitHub API calls are limited per address; wait for the retry window |
| "no asset in … matched …" | The upstream renamed its files. The error names the patterns; update `toolboxCatalog` and a test in `toolbox_test.go` |
| "archive did not contain <name>" | The upstream changed the binary's name inside the archive; same fix as above |
| The row still says Not installed | Check free space on the card; the install card reports the first useful error instead of a generic one |
| The tool runs but finds no files | It runs with the app's working directory; pass absolute paths |
| `naabu` exits with a loader error naming `libpcap` | The upstream arm64 build may link `libpcap` for its SYN scanner. Connect scans need no library, but if the loader refuses the binary it cannot start at all on that image; `httpx` and `dnsx` are pure Go and unaffected |

## Adding a tool

One entry in `toolboxCatalog` (`player/toolbox.go`) and one line in
`allowedToolRepos` (`player/tools.go`), plus a case in
`TestToolboxPatternsMatchRealAssetNames`. Set `NetworkScan: true` if the tool
sends traffic to other hosts — that flag is what puts a run behind the one-time
acknowledgement and adds the warning to its row. Keep it to tools whose upstream ships
official release binaries for `linux/arm64` and `windows/amd64`: anything else
turns into a per-device problem this catalog cannot solve. The security review
of a new entry is the same as for a new dependency — the repo becomes something
the app will download from and execute.
