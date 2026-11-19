# Linux packages (apt for the TrimUI)

Stock TrimUI firmware is a small Linux (kernel 4.9, glibc 2.33) with no
package manager, no Python and no Perl. **Linux packages** installs Ubuntu
24.04 ("noble") arm64 programs onto the SD card and runs them there, without
changing the firmware: Python 3, Perl, git, curl, htop, nano, rsync, the
OpenSSH client, Nmap, John the Ripper, Aircrack-ng, SQLite, and anything else
in Ubuntu's `main` and `universe` that works without a desktop or services.

It is available on ARM64 TrimUI handhelds only. On Windows and on a Linux PC,
`apt` says it is the handheld's package manager (a PC's own `apt` is used
instead when it has one).

Code: `player/linuxpkg_*.go` (`_index` trust chain and index, `_install`
resolver and unpacking, `_runtime` wrappers, `_cli` commands, `_ui` page,
`_version` Debian versions and dependencies).

## Where to use it

- **Tools → Linux packages** (also Home search "linux packages", "apt",
  "python"…): what is installed and how much space it takes, **Check for
  updates** / **Update all**, a search with the on-screen keyboard (**Y**), a
  short list of useful programs with one-press install, and what you
  installed, each with **Remove**. Pressing **A** on a package first works out
  what it would bring in and how big that is.
- **Terminal**: `apt update | upgrade | install | remove | autoremove | search
  | show | list --installed | list --upgradable | clean` (see
  [Terminal](TERMINAL.md#apt-linux-packages-on-the-handheld)).
- **SSH shell on the device**: `<JukaHub>/data/linux/bin/apt …` (it runs
  `JukaHub apt …`, which needs no display), and `. <JukaHub>/data/linux/env.sh`
  puts the installed programs on that shell's `PATH`.
- **Audit toolkit**: installing sqlmap or Nikto installs Python 3 or Perl first
  when the handheld has none; Nmap, John the Ripper and Aircrack-ng install
  from here (see [Toolbox](TOOLBOX.md#on-the-handheld-trimui)).

## How downloads are checked

The chain is apt's, with exactly one trusted key — the Ubuntu Archive
Automatic Signing Key (2018), fingerprint
`F6EC B376 2474 EDA9 D21B 7022 8719 20D1 991B C93C`, built into JukaHub
(`player/linuxpkg-ubuntu-archive-2018.asc`):

1. `dists/<suite>/Release` and `Release.gpg` are fetched for `noble`,
   `noble-updates` and `noble-security` from `ports.ubuntu.com/ubuntu-ports`.
   The detached signature must verify against that key; nothing else is
   accepted.
2. Each component's `Packages.xz` (or `.gz`) must match the SHA-256 and size in
   the verified Release (fetched by hash when the archive offers it, so a
   mirror update in between cannot mismatch).
3. Every `.deb` must match the SHA-256 and size in the verified `Packages`
   index; a mismatch is deleted before it is opened.

Because of that chain, plain HTTP is acceptable, as it is for apt. HTTPS is
tried first; stock TrimUI firmware's certificate bundle cannot validate
`ports.ubuntu.com`, so after a transport failure the rest of the session uses
HTTP — but no unverified byte is ever unpacked or trusted. Two more checks:

- The device clock may be wrong until NTP. The signature's time check uses the
  later of the clock and the Release's own date plus a day (the archive key has
  no expiry, so this cannot let anything expired through).
- A Release older than the one already accepted for that suite is refused, so
  a replayed old (validly signed) index cannot roll the card back to older,
  vulnerable versions. Ubuntu sets no `Valid-Until`, so freshness beyond that
  depends on running **Check for updates**.

The newest version of each package across the three suites wins (Debian version
order: epochs, `~`, letters before symbols, numeric runs).

## Where the files live

Everything is under `<JukaHub>/data/linux/` on the card:

| Path | What |
|---|---|
| `root/` | The unpacked packages (`usr/`, `etc/` …) |
| `bin/` | One small `#!/bin/sh` wrapper per program, `.jpkg-env`, and `apt` / `jpkg` |
| `libexec/git-core/` | Wrappers for git's helper programs (`GIT_EXEC_PATH`) |
| `env.sh` | `. env.sh` in a firmware shell puts `bin/` first on `PATH` |
| `status.json` | Installed packages: version, chosen by you or pulled in, size, dependencies |
| `info/<package>.list` | Every file and link a package owns, so remove and upgrade delete exactly those |
| `index/packages.tsv`, `index/meta.json` | The compact index (one line per package) and each suite's accepted Release date |
| `lists/` | The verified `Packages` files, reused while their SHA-256 is unchanged |
| `cache/` | Downloads until they are unpacked (`apt clean` empties it) |
| `lock` | Held while something changes (the page, the Terminal, the Audit toolkit and an SSH `apt` share it) |

## How programs run

Ubuntu's programs need glibc 2.39; the kernel would load the firmware's 2.33
for them. So nothing in `root/` is started directly. Each program has a wrapper
in `bin/` that runs Ubuntu's own loader from the card:

```
ld-linux-aarch64.so.1 --inhibit-cache --library-path <root lib dirs> --argv0 "$0" <program> "$@"
```

Only that process — and what it starts through other wrappers — uses the
card's libraries. `LD_LIBRARY_PATH` is never set, so firmware tools keep the
firmware's libraries; a firmware `LD_PRELOAD` is dropped for these programs.
A script from a package (`#!/usr/bin/python3`, `#!/usr/bin/perl`) gets a
wrapper that runs it with the wrapped interpreter. `bin/.jpkg-env` sets what
relocated programs need, only for what is installed: `PYTHONHOME`, `PERL5LIB`
(Ubuntu perl's `@INC` under the root), `GCONV_PATH`, `TERMINFO_DIRS`,
`SSL_CERT_FILE` / `CURL_CA_BUNDLE` / `GIT_SSL_CAINFO`, `OPENSSL_CONF`, `TZDIR`,
`GIT_EXEC_PATH`, `GIT_TEMPLATE_DIR`, `NMAPDIR`, `MAGIC`, and `LC_ALL=C.UTF-8`
when another locale is asked for (only the C locales exist here). The loader
is started once after `libc6` is installed, as the proof that it runs on this
kernel; the result is in the log (`linuxpkg`).

The Terminal puts `bin/` first on `PATH` for its commands. Nothing else in
JukaHub or the firmware sees it.

`libc6` itself is installed from the verified index like any package (same
release line as the programs). The pinned runtime GreenOvercast uses
([Private C runtime](PRIVATE_GLIBC.md)) is separate and unchanged.

## What is different from a real Ubuntu

- **No maintainer scripts.** There is no dpkg; `postinst` and friends never
  run. The two things they would do that matter here are done in Go: the CA
  bundle (`etc/ssl/certs/ca-certificates.crt` from Mozilla's roots in
  `ca-certificates`) and links. `update-alternatives` does not exist, so names
  such as `editor` or `awk` that Ubuntu makes with it are missing; shared
  libraries that Ubuntu reaches through alternatives (BLAS, LAPACK) are on the
  loader's path directly.
- **No services or daemons.** Nothing starts at boot, no users are added, and
  systemd units and init scripts are not unpacked.
- **A small "system" set is never installed**: `dpkg`, `debconf`
  (`debconf-2.0`), `cdebconf`, `ucf`, `install-info`, `init-system-helpers`,
  `init`, `systemd`, `systemd-sysv`, `sysvinit-utils`, `lsb-base`, `adduser`,
  `passwd`, `login`, `base-files`, `base-passwd`, `usr-is-merged`, `usrmerge`,
  `libc-bin`, plus every package Ubuntu marks *Essential* (coreutils, bash,
  tar … — the firmware's busybox stands in) except `perl-base` (it holds
  `/usr/bin/perl`) and `ncurses-base` (terminal descriptions). Recommends are
  not followed; `ca-certificates` comes with OpenSSL/GnuTLS and `ncurses-base`
  with ncurses, because nothing would work over TLS or in a terminal without
  them.
- **exFAT/FAT cards.** No symbolic links and no Unix modes beyond the mount's.
  Ubuntu's merged `/usr` is applied while unpacking (`bin/`, `sbin/`, `lib/` go
  into `usr/`), so the big folder links are never needed. A link to a file is a
  real symlink where the card can hold one, otherwise a copy of its target made
  after the whole run is unpacked (and made again when the target's package is
  upgraded). A program's other names (`python3` → `python3.12`, git's builtins)
  are not copied: their wrappers start the target under that name. Links to
  folders are left out. Names FAT cannot hold (`Foo::Bar.3pm`) are skipped. The
  card ignores case, so file ownership is compared case-insensitively.
- **Less on the card.** Documentation, manual pages, info pages, translations,
  lintian overrides and bug-report helpers are not unpacked.
- **Space.** A first `apt update` downloads about 20 MB of index (kept, so later
  updates fetch only what changed) and the compact index takes about 30 MB.
  Python 3 is roughly 25 MB downloaded and 80–100 MB on the card with its
  dependencies; Perl about 30 MB on the card. Every install checks free space
  first.
- **Interrupted runs.** A package whose files were being written when a run
  stopped (X in the Terminal, leaving the Terminal, a pulled card) is marked
  half-installed and unpacked again by the next install or upgrade. Programs
  that are running keep their old files: new files are written beside them and
  renamed into place.
- **ffmpeg** is not in the page's list: Ubuntu's build pulls in a desktop's worth
  of audio, video and GPU libraries. JukaHub installs its own static ffmpeg
  where it needs one. `apt install ffmpeg` still works if you want it.

## Status

Written 2026-11-18 without a Go toolchain: statically checked, not yet built,
and unverified on a device. To confirm on a TrimUI: glibc 2.39's loader on
kernel 4.9 from the exFAT card (the GreenOvercast probe suggests it works),
Python 3.12 and Perl 5.38 starting through the wrappers, the HTTP fallback,
the zstd/xz `.deb` decoding speed on the A133, and the space a Python install
really takes.
