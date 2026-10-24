# Packages and the Patch package manager

**Patch** (Tools → Patch) is JukaHub's package manager: install, update, verify
and roll back files on the device without a PC, plus JukaHub's own update flow.
It is deliberately declarative — a package describes *files*, never scripts
that run behind your back.

Every install, upgrade and remove is a **journaled transaction**: files are
backed up first, the journal is written before anything is mutated, and an
interrupted operation is rolled back at the next launch.

---

## What Patch does

- **Install / Update / Reinstall / Remove** from the package list — rows show
  their state (`Installed`, `Update`) and **A** opens the row menu.
- **Refresh repository** — download and verify the signed repository index (the
  `apt-get update` equivalent; offline keeps the last verified index).
- **Dependency resolution** — `depends` are resolved into a transaction plan,
  removal checks reverse dependencies first, and the plan is ordered by risk.
  Every archive is digest-verified against the signed index before install.
- **Verify & repair** — validate config, tools and assets; clear stale
  downloads and report damage.
- **History & Rollback** — transaction log, rollback to the last committed
  backup set, and a **Restore latest backup** action. Edits you made after the
  transaction are never silently overwritten.
- **Maintenance** — repair, cache clean, diagnostics.
- **Check for updates** — compares the latest release's publication time with
  the compile stamp inside the running binary (`main.buildTime`, set by
  `build.bat` / `release.sh`). A newer release adds an **Install update**
  action here, and the home header gets a download chip next to the weather
  that installs the release in one press. Nothing is downloaded until one of
  those is pressed. The same check runs at most once a day at launch and only
  ever advertises.
- **Export diagnostics** — a redacted report.
- **JukaHub bundled assets** starter package — restores the app's own fonts,
  background and default config.

> Installed-package holds (`dbSetHeld`) exist in the package database but are
> not exposed as a menu action yet.

### Where the state lives

`PatchStateDir()` (next to the installation on the device) is created on demand
with:

```text
<state>/state.json          update-check state, versions, last errors
<state>/db/db.json          installed-package database (written atomically)
<state>/downloads/          verified package archives
<state>/staging/            extraction area
<state>/journals/           transaction journals
<state>/backups/<stamp>/    per-transaction backup sets
<state>/reports/            diagnostics exports
<state>/packages.json       the editable local package index
```

---

## The local package index

Packages can be installed from a JSON index you edit yourself (the Patch screen
has **Edit packages.json**):

```json
{
  "schema": 1,
  "packages": [
    {
      "id": "my-theme",
      "name": "My theme",
      "version": "1.0.0",
      "description": "My custom JukaHub theme",
      "root": "/mnt/SDCARD/JukaHub/packages/my-theme",
      "dest_root": "/mnt/SDCARD",
      "files": [
        { "src": "theme.json", "dest": "/JukaHub/themes/my-theme.json", "sha256": "sha256:…", "keep_user": true },
        { "src": "tool.sh", "dest": "/Tools/tool.sh", "executable": true }
      ]
    }
  ]
}
```

- **Local packages** point `root` at a directory on the device; files are copied
  from there.
- **Remote packages** add `archive_url` (HTTPS, GitHub-only) and `archive_sha`;
  the archive is downloaded, digest-checked and extracted into staging before
  install.
- Destinations default to the SD-card root (or the app directory on desktop) and
  can be overridden per file.

### Never touched

Installation refuses these destinations outright — for packages from the index
*and* for signed packages:

`Roms`, `BIOS`, `Saves`, `States`, `Screenshots`, `Collections`, `.system`,
`JukaHub/data`, `jukauser.json`

No firmware flashing, no SD-card reformatting, no ROM or save access.

---

## Package format

A package is a **zip** containing a manifest at the root and a payload
directory:

```text
<name>.zip
├── manifest.json
└── payload/…        ← files referenced by the operations' "src"
```

### `manifest.json`

```json
{
  "schema": 1, "name": "jukahub-cache-clean", "version": "1.0.0",
  "title": "JukaHub Cache Cleaner",
  "description": "Installs the cache-clean helper tool.",
  "architecture": "all", "devices": ["TG5040"], "os": ["trimui-stock"],
  "risk": "low", "depends": [], "conflicts": [], "provides": [],
  "restart": "none",
  "operations": [
    { "kind": "install", "src": "jukahub-cache-clean.sh", "dest": "/JukaHub/tools/jukahub-cache-clean.sh" },
    { "kind": "executable", "dest": "/JukaHub/tools/jukahub-cache-clean.sh" }
  ],
  "verify": ["/JukaHub/tools/jukahub-cache-clean.sh"],
  "rollback": "automatic"
}
```

| Field | Rules |
|-------|-------|
| `schema` | must be `1`; any other value is rejected |
| `name` | `a`–`z`, `0`–`9`, `-`, `_`, `.` only, 1–64 characters |
| `version` | semantic version |
| `title`, `description` | shown in the package list |
| `architecture` | `arm64` or `all` |
| `devices`, `os` | compatibility gate (e.g. `TG5040`, `trimui-stock`) |
| `risk` | `low`, `medium` or `system` |
| `depends`, `conflicts`, `provides` | dependency metadata |
| `restart` | `none`, `jukahub` or `device` |
| `operations` | at least one; see below |
| `verify` | paths to hash when verifying the installation |
| `rollback` | `automatic` |

### Operations

| `kind` | Required fields | Effect |
|--------|-----------------|--------|
| `install` | `src`, `dest` | copy a payload file to an absolute destination |
| `replace` | `src`, `dest`, `baseline_sha256` | replace a file only if its current hash matches the baseline |
| `remove` | `dest` | delete a file owned by the package |
| `patch` | `key`, `value` | structured key/value change |
| `executable` | `dest` | set the executable bit |
| `chmod` | `dest`, `mode` | set a specific mode |

Anything else — including lifecycle scripts such as `postinst` — is rejected
outright. The manifest is validated strictly: unknown schema versions, unknown
operation kinds, missing fields, duplicate `(kind, dest)` pairs, destinations
inside the protected directories above, and manifests with no operations all
fail before a single file is touched.

### Building a package

```bash
mkdir -p patch-packages/my-package/payload
# write patch-packages/my-package/manifest.json + payload files
```

The shipped example is
[`player/patch-packages/jukahub-cache-clean/`](../player/patch-packages/jukahub-cache-clean).

---

## Signed repository (trust model)

Packages come from a JukaHub-maintained repository whose index is **Ed25519
signed**; the verification public key is compiled into the application. HTTPS
alone is never treated as authenticity.

`repository/index.json`:

```json
{
  "format": 1,
  "key_id": "jukahub-patch-dev-2026",
  "issued_at": "2026-01-01T00:00:00Z",
  "expires_at": "2026-04-01T00:00:00Z",
  "packages": [
    { "name": "…", "version": "1.0.0", "architecture": "arm64",
      "devices": ["TG5040"], "os": ["trimui-stock"], "risk": "low",
      "depends": [], "conflicts": [], "provides": [],
      "compressed_size": 1234, "installed_size": 4567,
      "restart": "none", "package_sha256": "…" }
  ],
  "signature": "<hex Ed25519 signature over the canonical JSON>"
}
```

Verification refuses: an unknown `format`, a `key_id` that is not the embedded
key, a signature that does not verify, an index issued in the future, and an
expired index. Each package archive is then SHA-256-verified against the signed
entry before it is unpacked, and the same device/OS/architecture gate as the
local index is applied.

The private key lives only in the offline tooling — never in this repository,
never in the app. Assemble and sign a repository with:

```bash
cd player
PATCH_SIGN_KEY=<hex private key> go run ./tools/build-patch-repo --src ./patch-packages --out ./patch-repo
```

Input layout is `<src>/<name>/manifest.json` plus `<src>/<name>/payload/…`;
the tool writes deterministic `packages/<name>.zip` archives and the signed
`repository/index.json`, and warns when a package was signed with a key other
than the embedded one.

> The key shipped in `patch_signed.go` is the development key
> (`jukahub-patch-dev-2026`). A production deployment replaces it with the
> official key and publishes the matching repository.

### Signed app releases

App updates use the same key and the same canonical-JSON scheme. A release may
publish `release.json` beside its archives:

```json
{
  "format": 1,
  "key_id": "jukahub-patch-dev-2026",
  "tag": "JukaHub-20261020154603",
  "issued_at": "2026-10-20T15:46:03Z",
  "expires_at": "2027-10-20T15:46:03Z",
  "targets": [
    { "asset": "JukaHub-Windows.zip", "sha256": "…", "size": 29289140 },
    { "asset": "JukaHub-Trimui.zip", "sha256": "…", "size": 10142417 }
  ],
  "signature": "<hex Ed25519 signature over the canonical JSON>"
}
```

The updater's order of trust is: **signature, then digest, then HTTPS**. When
`release.json` is published it must verify, must name the release tag being
installed, must list this platform's archive, and its digest must agree with the
GitHub asset digest — otherwise the install stops. A release without the
manifest is still installable (older releases and the URL fallback), but the
updater logs that it fell back to the digest, because the GitHub digest and the
archive come from the same account and a compromise there defeats the check.

Sign a release after both archives are built and before uploading them:

```bash
cd player
PATCH_SIGN_KEY=<hex private key> go run ./tools/sign-release \
  -tag JukaHub-20261020154603 \
  -asset JukaHub-Windows.zip=dist/JukaHub-Windows.zip \
  -asset JukaHub-Trimui.zip=dist/JukaHub-Trimui.zip \
  -out dist/release.json
```

Upload `release.json` as a release asset. Until the release pipeline runs that
step with the signing key in CI, releases stay unsigned and the updater keeps
using the GitHub digest (with the log warning above).

---

## Related

- [Building](BUILDING.md) — release artifacts and the compile stamp the updater compares
- [Configuration reference](CONFIGURATION.md) — `packages_refresh` and the package list element
- [Security](../player/SECURITY.md) — secrets handling
