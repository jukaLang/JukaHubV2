# JukaHub Security

This document describes the security features implemented in JukaHub, including encryption, key management, input validation, and best practices for deployment.

## Reporting a vulnerability

**Do not open a public issue for a security problem.** Use GitHub's private
vulnerability reporting: open
[Security → Report a vulnerability](https://github.com/jukaLang/JukaHubV2/security/advisories/new)
on this repository. Include what you did, what happened, the version
(`JukaHub -v` in the header, or the release tag), and — if possible — a minimal
config or patch that reproduces it.

You will get an acknowledgement and a triage decision (affected: yes/no, plan)
before anything is fixed publicly, and credit in the release notes unless you
prefer to stay anonymous.

## Overview

JukaHub implements several security measures to protect sensitive credentials stored in configuration files and to harden runtime behavior:

- **AES-256-GCM encryption** for API keys and tokens stored in `jukaconfig.json`
- **Per-value salts and unique ciphertexts** for the enhanced encryption format (same plaintext encrypts differently each time)
- **Configurable encryption keys and salt** via `JUKAHUB_CRYPTO_KEY` and `JUKAHUB_CRYPTO_SALT` environment variables
- **Key derivation** for custom environment-variable keys using an HKDF-like HMAC-SHA256 expansion, instead of naive padding/truncation
- **Key versioning** for future rotation support (`ENC:<keyID>/<hex ciphertext>`)
- **HMAC-authenticated encryption wrapper** available for defense-in-depth (`EncryptWithAuth` / `DecryptWithAuth`)
- **Encrypted data verification** without exposing plaintext (`VerifyEncryptedData`)
- **Automatic detection** of plaintext secrets with startup warnings (`ConfigValidator.SecurityCheck`)
- **Input validation and sanitization** for custom config strings (`ValidateCustomString` / `SanitizeCustomString`)
- **Secure memory handling** (best-effort key zeroization on exit via the `Cleanup` deferred-cleanup list)
- **Log sanitization** so URLs and query parameters containing tokens are not emitted in logs
- **A curated download allowlist** for every helper and toolbox tool, with stable-name asset matching and no shell on run (see [Toolbox and helper-tool downloads](#toolbox-and-helper-tool-downloads))
- **An in-app posture view** (Tools → Security & trust) that reports plaintext secrets, the storage key in use, package state, open LAN servers and the browser session without leaving the device

## Encryption Architecture

### Supported Encrypted Fields

The following configuration fields should be encrypted:

| Field | Description | Encrypted By Default |
|-------|-------------|---------------------|
| `discord_token` | Discord bot/user token | Yes |
| `groq_api_key` | Groq AI API key | Recommended |
| `google_api_key` | Google YouTube API key | Recommended |
| `openai_api_key` | OpenAI API key (future) | Recommended |
| `anthropic_api_key` | Anthropic API key (future) | Recommended |

### Encryption Format

Encrypted values use the format: `ENC:<keyID>/<hex-encoded ciphertext>`

Example (illustrative only — replace with your own value):
```
"groq_api_key": "ENC:default-v3-enhanced/00112233445566778899aabbccddeeff..."
```

The `keyID` prefix enables future key rotation support. Legacy format `ENC:<hex>` is also supported for backward compatibility.

The enhanced format uses per-value random nonces plus an HMAC-derived key from a configured secret, so identical plaintext values produce different ciphertexts even when the same environment key is used.

### Authentication wrapper

For additional defense-in-depth, an HMAC-verified wrapper is available:

```
"ENC:AUTH:<keyID>/<hex>:<hmac hex>"
```

This lets the player detect tampering with the encrypted blob itself, not just GCM authentication.

### Setting a secret from the command line

The player can encrypt a value with exactly the key it will use at runtime, so
the result can be prepared on a PC and copied to the handheld:

```bash
# Reads the token from stdin so it never lands in shell history.
./JukaHub -set-secret discord_token

# Verify every encrypted value in the config (never prints plaintext).
./JukaHub -check-secrets
```

`-set-secret discord_token` writes `channel_profile.token`;
`-set-secret discord_channel` writes the (unencrypted) channel id; any other
name is stored under `variables`. Use `-config <file>` to target a config other
than `jukaconfig.json` in the current directory. If `JUKAHUB_CRYPTO_KEY` or
`JUKAHUB_CRYPTO_SALT` are set, they must be set the same way when the player
runs, or the value will not decrypt.

## Encryption Key Management

### Setting a Custom Encryption Key

For production deployments, set the `JUKAHUB_CRYPTO_KEY` environment variable:

```bash
# Generate a strong random 32-byte key (64 hex characters)
export JUKAHUB_CRYPTO_KEY="$(openssl rand -hex 32)"

# Or on Windows (PowerShell)
$env:JUKAHUB_CRYPTO_KEY = [System.Convert]::ToHex([System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
```If you pass a 64-character hex value, the player uses it directly as the AES-256 key. If you pass a shorter or non-hex value, the player derives a 32-byte key from it with an HKDF-like HMAC-SHA256 expansion and the configured salt.

### Key Salt

You can also set `JUKAHUB_CRYPTO_SALT`. If unset, the player uses a built-in salt. For production, set a strong random salt the same way you set the key.

### Key ID recorded in the config

When a custom environment key is used, the config stores a key ID like `env-<prefix>` or `env-derived`. When the built-in key is used, the ID is `default-v3-enhanced`. That ID is embedded in every encrypted value so the player can route decryption to the right key family in the future.

### Key Requirements

- **Length**: Exactly 32 bytes (256 bits for AES-256)
- **Format**: Hex-encoded (64 characters) preferred for direct use; otherwise the value is treated as a passphrase and derived
- **Storage**: Store securely (environment variable, secrets manager, CI secret store, etc.)

### Default Key (Not Recommended for Production)

If `JUKAHUB_CRYPTO_KEY` is not set, JukaHub uses a built-in derived key. This is **NOT secure** for production because:

- The derivation secret is compiled into the binary
- Anyone with the binary can derive the same key and decrypt your secrets
- The key is the same across all installations

A warning is logged at startup when the default key is used, and `ConfigValidator.SecurityCheck` reports it.

## Encryption Functions

### Go API

```go
import "player" // or your package path

// Encrypt a plaintext value (random nonce per call)
encrypted, err := EncryptString("my-secret-api-key")
// Result: "ENC:default-v3-enhanced/a1b2c3d4..."

// Encrypt with audit logging (recommended for config fields)
encrypted, err := EncryptAPIToken("my-key", "groq_api_key")

// Decrypt an encrypted value
decrypted, err := DecryptString(encrypted)
// Result: "my-secret-api-key"

// Decrypt with audit logging
decrypted, err := DecryptAPIToken(encrypted, "groq_api_key")

// Authenticated encryption wrapper (defense-in-depth)
encrypted, err := EncryptWithAuth("my-secret-api-key")
// Result: "ENC:default-v3-enhanced/<hex>:AUTH:<hmac>"

// Authenticated decryption wrapper
decrypted, err := DecryptWithAuth(encrypted)

// Verify encrypted data without exposing plaintext
valid := VerifyEncryptedData(encrypted)
```

### Encryption Details

- **Algorithm**: AES-256-GCM (authenticated encryption)
- **Nonce**: Random 12 bytes per encryption (cryptographically secure RNG)
- **Key derivation**: HKDF-like HMAC-SHA256 expansion from the configured secret and salt
- **Output**: Hex-encoded nonce + ciphertext with key ID prefix

## Security Validation

JukaHub includes a `ConfigValidator.SecurityCheck()` method that scans for potential security issues:

```go
validator := NewConfigValidator()
warnings := validator.SecurityCheck(config)
for _, warning := range warnings {
    log.Printf("Security Warning: %s", warning)
}
```

### Detected Issues

The security check warns about:

1. **Plaintext secrets**: API keys stored without encryption
2. **User Discord tokens**: Using user token type instead of bot tokens
3. **Default encryption key**: Using the compiled-in default instead of a custom key

### Input validation and sanitization

`ConfigValidator` also exposes helpers for user-supplied config strings:

- `ValidateCustomString(value, fieldName)` returns warnings for suspicious patterns (injection-like sequences, path traversal, embedded secrets, control characters).
- `SanitizeCustomString(value)` returns a safer version for logging/display.

Use these before rendering user content, building shell commands, or logging config values.

## Secure Memory Handling

JukaHub implements best-effort secure memory handling:

- Encryption keys are zeroized on program exit via the `Cleanup` deferred-cleanup list
- Decrypted values are eligible for garbage collection after use
- Note: Go's garbage collector may create copies, so this is not a guarantee

For high-security scenarios, consider:

- Using a secrets manager instead of config files
- Running with minimal privileges
- Using OS-level memory locking where available

## Best Practices

### Development

1. **Never commit secrets to version control**
   - Add `jukaconfig.json` to `.gitignore` if it contains real secrets
   - Use placeholder values in committed configs

2. **Use the development key for testing**
   - The built-in default key is fine for local development
   - Don't deploy with the default key to production

3. **Rotate keys periodically**
   - Generate a new key with `openssl rand -hex 32`
   - Generate a matching salt with `openssl rand -hex 32`
   - Re-encrypt all secrets with the new key/salt
   - Update the environment variables

### Production Deployment

1. **Set a strong encryption key**
   ```bash
   export JUKAHUB_CRYPTO_KEY="your-64-char-hex-key-here"
   export JUKAHUB_CRYPTO_SALT="your-64-char-hex-salt-here"
   ```

2. **Encrypt all API keys before deploying**
   - Use the encryption functions to encrypt keys
   - Store only encrypted values in config files

3. **Restrict config file permissions**
   ```bash
   chmod 600 jukaconfig.json
   ```

4. **Use environment-specific configs**
   - Different keys (and salts) for dev/staging/production
   - Never copy production configs to development

5. **Monitor security warnings**
   - Check logs for security warnings at startup
   - Address plaintext secret warnings promptly

### Discord Integration

When configuring Discord:

1. **Use bot tokens when possible**
   - Bot tokens are scoped to the bot's permissions
   - User tokens may have broader access

2. **Store tokens encrypted**
   - If your deployment stores config values with the built-in encryption helpers, keep those values encrypted
   - Verify any stored secret you care about is not left as plaintext in config
   - Rotate any token that has ever been committed: the default key is built
     into the binary, so a ciphertext in a public repository is as good as
     plaintext. Use `-set-secret` with a fresh token and never commit the result
     if it points at your own bot.

3. **Limit bot permissions**
   - Only grant necessary permissions
   - Use the principle of least privilege

### Transport and network security

- Outbound HTTP requests use HTTPS only for configured endpoints (GitHub API, update checks, tool downloads).
- GitHub API calls set a descriptive `User-Agent`, the `Accept` header for the intended API version, and respect `429`/`Retry-After` rate limiting.
- Updates and package archives are checksum-verified against signed metadata before use (see Patch, below).

### SSH/SFTP server

The Tools panel can start an SSH server that exposes the file system over
SFTP. It is opt-in and stops when the app closes. Its security model:

- **Authentication** — one account, `juka`, with a 16-character password
  generated from `crypto/rand` on every start and shown only on the device. It
  is never published as a custom variable (which would persist it in
  `jukauser.json`) and never logged. Optional OpenSSH public keys are read from
  `ssh_authorized_keys` in the app data folder. `MaxAuthTries` is 3 and at most
  4 connections are open at once, so online guessing is not practical.
- **SFTP only** — the server accepts the `sftp` subsystem and refuses shell,
  exec, pty, environment, agent-forwarding and global requests. Port forwarding
  and tunnels are therefore unavailable, even for an authenticated client.
- **Path confinement** — client paths are POSIX paths rooted at `/`; each
  component is joined to the local root and `..` is removed before the join.
  Symlinks are resolved and must still point inside the root, so a link planted
  on the SD card cannot expose `/etc/shadow`. Writes through dangling links are
  refused, and creating hard or symbolic links over SFTP is rejected.
- **Host key** — an Ed25519 host key is generated on first start and stored as
  `ssh_host_ed25519_key` (mode `0600`) in the app data folder. The SHA256
  fingerprint is shown in the terminal (`ssh status`), so a client warning can
  be verified and a changed host key noticed.
- **Least privilege** — the server runs as the JukaHub process user, not root.
  Set `JUKAHUB_SSH_READONLY=1` (or `ssh_readonly` in the config) to refuse all
  writes, and `JUKAHUB_SSH_ROOT` to expose only one folder instead of `/`.

As with FTP, the server is meant for a trusted home network. A leaked password
is valid until the server is restarted; restart it from Tools to rotate.

## Patch / signed repositories

The Patch tool uses an Ed25519-signed repository index. The verification public key is embedded in the player (`patch_signed.go`), and every package archive is SHA-256-verified against the signed index before install. HTTPS alone is never treated as authenticity.

The signing private key lives only in the offline repository tooling:

```
PATCH_SIGN_KEY=<hex private key> go run ./tools/build-patch-repo --src ./patch-packages --out ./patch-repo
```

## Toolbox and helper-tool downloads

JukaHub fetches two kinds of files itself: the helper tools it needs to play
media (`yt-dlp`, `ffmpeg`, `ffprobe`) and the tools a user installs
from **Tools → Toolbox** (`jq`, `rclone`, `ripgrep`, `fd`, `pandoc`, `age`, and
the scanners `naabu`, `httpx`, `dnsx`).

**What bounds a download**

- **An allowlist, in code.** A URL only ever comes from the latest release of a
  repo listed in `allowedToolRepos` (`player/tools.go`). There is no free-text
  repo name and no URL field anywhere in the UI: adding a tool is a code change
  that gets reviewed like any other.
- **A catalog, for the toolbox.** `player/toolbox.go` names every installable
  tool and the exact asset patterns for each platform. An id that is not in the
  catalog cannot be installed, and a platform with no pattern is refused with a
  message instead of a download that can never work.
- **Stable-name matching.** Asset names are matched on platform and archive
  tokens, never on a version. Checksum, signature and release-note side-cars
  (`.sha256`, `.sig`, `.asc`, `.txt`, `.json`) are never picked, so a token match
  cannot hand the downloader a hash file by mistake.
- **The same streamer as everything else.** Downloads are HTTPS, capped at
  512 MB, refused below 4 KB (a truncated file), staged inside the tools folder
  (never `/tmp`, which is a small tmpfs on the handheld), and unpacked with the
  entry checks that keep an archive from writing outside its destination.
- **A direct exec, never a shell.** Running a tool builds an argument vector:
  quotes group words, and `;`, `|`, `&&`, `*` and `$()` reach the tool as
  literal text. The installed binaries live in `required/` next to the app and
  are also on `PATH` for Power tools → Script snippets.

**What is *not* verified, and why that is a conscious trade**

Helper and toolbox binaries are **not** checked against an upstream hash or
signature. The Patch / package path does verify (see below) because it ships its
own signed index; these tools publish releases with their own hashes and
signatures, and pinning those would mean maintaining a second signed index that
has to follow six upstream release cadences. The trust boundary is therefore:
GitHub TLS, the upstream project's release process, and this repo's allowlist.
If that is not enough for a given deployment, install the binaries yourself and
copy them into `required/` — JukaHub uses a present file and never overwrites it.

**Network scanners, and the one thing the app cannot check**

The three scanning tools are ordinary catalog entries with one extra rule: a run
is refused until the user has acknowledged, once, that a scan sends traffic to
hosts other than this device. The first press arms the prompt and the second
records `networkScanAuthorized: true` in `jukauser.json`; every later run of any
scanner goes straight through, and deleting the key brings the prompt back.

Whether a given target is a network the user owns or is allowed to test is not
something the app can verify, and it does not pretend to. This is a
conscience-and-ownership gate, not a technical control, and it is recorded here
as accepted risk rather than implied protection. Nothing in the catalog does OS
fingerprinting or carries an exploit: the scanners report open ports, HTTP
responses and DNS records.

**What the toolbox cannot do**

- Install anything outside the catalog, from a URL, or from a package.
- Replace a running binary: an installed tool is left alone so a launch never
  fails with `ETXTBSY`.
- Run with elevated privileges: the tool runs as the JukaHub user, with the same
  filesystem access the app already has.
- Confirm that a scanned network belongs to the user; that acknowledgement is
  theirs to make.

## Threat model

A one-page statement of what this build trusts. It is deliberately short: every
row either names the mechanism that carries the risk or says the risk is
accepted.

| Asset | Trusted? | Mechanism |
|---|---|---|
| Secrets in `jukaconfig.json` | Stored encrypted, key is not | AES-256-GCM, per-value nonces, key from `JUKAHUB_CRYPTO_KEY` or the built-in default (flagged in the posture view) |
| App and asset updates | Yes, via signature | Ed25519-signed repository index; SHA-256 per file; rollback copy kept |
| Packages | Yes, via signature | Same signed index, transactional install, digest + signature check before anything is replaced |
| Helper and toolbox tools | Yes, via allowlist + TLS | Curated repos in code, stable-name asset choice, size-capped staging, no shell on run; the network scanners additionally need a one-time acknowledgement before the first run |
| SD card contents | **No** | It is removable and editable from a PC: config, plugins, ROMs and saves are all data the device did not author |
| Plugins and snippets | **No**, by design | They are programs the user asked to run; they execute with the app's own privileges and a cleared environment |
| Browser content | **No** | Only `http`/`https` survive link handling; cookies are stored per host; a page's script never gets the secret store |
| LAN file servers (FTP/SFTP) | Trusted-network only | Plain FTP has no encryption; SFTP has a generated password and a host-key fingerprint; both are off until started and shown in the posture view |
| Emulator processes | Trusted to run | The app waits for them; it never reads their memory except through the explicit memory-scanner feature on the Linux target |

Two consequences worth stating plainly: a config or a plugin read from someone
else's card is hostile input, and a LAN server left running on an untrusted
network is a data-exfiltration path. Both are visible in **Tools → Security &
trust** so the state is never a surprise.

## Browser data on the card

The Browser keeps a session across a relaunch, which means cookies are written
to disk. What is stored, and what is not:

- `data/browser-cookies.json`, mode `0600`, one `schemaVersion` and one
  `savedAt` field. Per cookie: host, optional `Domain`, path, name, value, the
  `Secure`/`HttpOnly` flags and the expiry — no addresses, titles or visit times.
- Session cookies are saved on purpose (that is the feature); expired cookies
  are never written and are dropped on load; a page's own sign-out removes it
  from the file; the list is capped at 256 records, sorted so truncation is
  deterministic; a corrupt file costs the session and nothing else.
- **Browser → Forget** empties the jar, deletes the file and reloads the page.
  The toolbar asks twice: the first press arms it and names the count.
- The line under the page says which hosts those cookies belong to, so a saved
  session is visible without a press. It is built from the same records — hosts
  and counts only; no address, path or value ever reaches the screen.
- The page cache (bbolt) holds response bodies, not credentials; the ad-block
  exception list holds hosts only.

## In-app posture view

**Tools → Security & trust** summarises the state this document describes, so a
user does not need a PC to answer "is this device configured sensibly?":

| Row | What it reads | Warns when |
|---|---|---|
| Config secrets | `ConfigValidator.SecurityCheck` | a sensitive key is stored in plaintext |
| Encryption key | `JUKAHUB_CRYPTO_KEY` presence | the built-in default key is in use |
| Tool downloads | `allowedToolRepos` size | never |
| Packages & patches | `packages.json` count | never |
| File servers | FTP/SFTP listeners | either server is running |
| Browser session | the cookie jar file | never |
| Telemetry | the opt-in flag | never (off by default) |
| Threat model & details | this document | never |

The page is read-only: it reports, it never changes a setting. Securing the
device is still `-set-secret`, the Settings rows and the Tools rows the rest of
this document describes.

## Troubleshooting

### Decryption Failures

If decryption fails:

- Verify the encryption key hasn't changed since the value was encrypted
- Check that the encrypted value wasn't modified/truncated in the config file
- Ensure the config file wasn't corrupted or reformatted by another tool
- If you recently rotated keys, make sure old values were re-encrypted with the new key

### "Using built-in default encryption key" Warning

This warning means you're using the insecure default key. To fix:

1. Generate a new key: `openssl rand -hex 32`
2. (Optional but recommended) Generate a new salt: `openssl rand -hex 32`
3. Set `JUKAHUB_CRYPTO_KEY` (and `JUKAHUB_CRYPTO_SALT`) environment variable(s)
4. Restart the application
5. Re-encrypt all secrets with the new key

### Key Rotation

To rotate encryption keys:

1. Generate a new key and salt
2. Set `JUKAHUB_CRYPTO_KEY` and `JUKAHUB_CRYPTO_SALT` to the new values
3. Decrypt all existing values with the old key (you must still have access to the old key)
4. Re-encrypt with the new key
5. Update the config file
6. Restart the application

Note: Key rotation requires access to both the old and new keys during the transition.

## Static-analysis triage (CodeQL)

CodeQL reports on `main` flag path, command, hash and allocation sinks. Every
alert is handled in one of three ways: fixed in code, guarded and covered by a
test (the analyzer cannot see through the helper, so it re-reports the sink),
or accepted by design.

**Fixed in code**

- Paths read from a config value or a scene row are resolved against a known
  root before use: File Explorer navigation and listing, PDF opening, chat file
  attachment, archive extraction and app launch entries (`paths.go`).
- Chat attachments are only read from disk when they are an image below the
  folder the File Explorer attached from (`chatAttachmentRoot` in `chat.go`).
  A path typed into a message, restored from an edited history file or attached
  by a remote room is refused instead of read.
- Archive entries (tool downloads and File Explorer unzip) are reduced to a
  plain base name or checked to stay below the destination directory, so a
  hostile archive cannot write outside it.
- `removeTempVideo` only deletes `jukahub-video-*.mp4` files in the player's own
  temp directory; stopping a video no longer removes user media.
- Editing a secret in `jukaconfig.json` rebuilds the byte slice with `append`
  rather than a computed capacity, so the offset math cannot overflow.
- yt-dlp search commands from the scene config run without a shell, and options
  that launch other programs (`--exec`, `--downloader`, `--postprocessor-args`,
  `--update`, ...) are rejected before the process starts.
- The browser's link picker decides by *parsed* scheme: only `http` and `https`
  survive. The old blocklist of `javascript:`/`data:`/... spellings missed the
  whitespace and mixed-case forms a browser tolerates (`browser.go`).
- A config-declared canvas size is clamped before it becomes an `int32`, so a
  huge number cannot wrap negative and scale scene geometry into nothing
  (`canvasDimension` in `uiprofile.go`).
- MQTT strings are truncated to what their `uint16` length prefix can describe,
  on a rune boundary, so an oversized topic or payload is a shorter packet
  rather than a malformed one (`mqttString` in `smart_home.go`).
- The SSH password is compared through a salted HMAC-SHA256 digest, and a
  server started without a password refuses password auth instead of accepting
  an empty one (`sshPasswordDigest` in `ssh_server.go`).

**Guarded and tested (re-reported because the guard is a helper call)**

- Archive extraction: `runtimeArchivePath` and `archiveLinkTarget` contain every
  entry and symlink target below the destination; `windows_runtime_install_test.go`
  builds archives with `../` entries and escaping links and asserts the error.
- App launch: `isLaunchableAppPath` confines a row to the scanned app folders and
  the extension allowlist; `apps_test.go` covers the refusals.
- Plugin scripts: `resolveWithinRoot` confines the entry to the plugin folder.
- Lyrics and subtitle sidecars (`lyrics.go`, `videosubs.go`) and the photo
  gallery (`ambient.go`) open files next to media the user already chose. The
  path is cleaned, but it is deliberately not confined to one root: the media
  library spans the SD card.
- `make([]string, 0, len(a)+len(b)+len(c)+1)` in `streaming.go` sizes an argument
  vector from three slice lengths; integer overflow would need more live slices
  than an address space can hold, so this is a false positive.

**Accepted by design**

These sinks take input the user typed or installed on purpose. They are still
validated (path cleaning, extension allowlists, containment below the relevant
folder), but running the program is the feature:

- the built-in terminal (`cd`, `cat`, `sh -c`/`cmd /c` for typed commands);
- plugin scripts, whose entry file must resolve inside the plugin's own folder;
- the Apps launcher, which starts installed programs from the scanned app
  folders only (extension allowlist: `.exe`, `.lnk`, `.bat`, `.cmd`, `.url`,
  `.sh`).

If one of these is ever exposed to content the device owner does not control
(for example a remotely supplied config), remove that feature from the input
path instead of weakening the guard.

## Security Limitations

1. **Config file encryption is obfuscation, not strong security**
   - The encryption key (or passphrase) must be available to the application at runtime
   - Anyone with access to both the config and the key can decrypt

2. **Memory dumps may expose secrets**
   - Decrypted values exist in memory during runtime
   - Core dumps or memory analysis could expose them

3. **Go garbage collection limitations**
   - Go doesn't provide secure memory zeroization guarantees
   - Values may persist in memory after use

4. **Environment-variable leakage**
   - `JUKAHUB_CRYPTO_KEY` may be visible to other processes on the same user account via `/proc` or process listing tools, depending on OS and permissions
   - Prefer a secrets manager or restricted environment where possible

For high-security applications, consider:

- Using a dedicated secrets manager (HashiCorp Vault, AWS Secrets Manager, etc.)
- Running in a trusted execution environment (TEE)
- Implementing additional access controls and key management policies
