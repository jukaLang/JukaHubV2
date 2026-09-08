# JukaHub Security

This document describes the security features implemented in JukaHub, including encryption, key management, input validation, and best practices for deployment.

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

## Encryption Architecture

### Supported Encrypted Fields

The following configuration fields should be encrypted:

| Field | Description | Encrypted By Default |
|-------|-------------|---------------------|
| `discord_token` | Discord bot/user token | ✅ Yes |
| `groq_api_key` | Groq AI API key | ⚠️ Should be |
| `google_api_key` | Google YouTube API key | ⚠️ Should be |
| `openai_api_key` | OpenAI API key (future) | ⚠️ Should be |
| `anthropic_api_key` | Anthropic API key (future) | ⚠️ Should be |

### Encryption Format

Encrypted values use the format: `ENC:<keyID>/<hex-encoded ciphertext>`

Example:
```
"groq_api_key": "ENC:default-v3-enhanced/985e49a4a499a71e1683dff6e5174bdc024f5f3c267d2622c01228ae887d7f2bebcd484ff75984ed2a5f62a4caa8e2d73f0aa9470d28dffc83f1d5f4fe1fc5ce0c1401552d81c5e2aa7e9464d86e68558a97ffe0389a5958955f74ef4acd31d7a0b16300"
```

The `keyID` prefix enables future key rotation support. Legacy format `ENC:<hex>` is also supported for backward compatibility.

The enhanced format uses per-value random nonces plus an HMAC-derived key from a configured secret, so identical plaintext values produce different ciphertexts even when the same environment key is used.

### Authentication wrapper

For additional defense-in-depth, an HMAC-verified wrapper is available:

```
"ENC:default-v3-enhanced/<hex>:AUTH:<hmac hex>"
```

This lets the player detect tampering with the encrypted blob itself, not just GCM authentication.

## Encryption Key Management

### Setting a Custom Encryption Key

For production deployments, set the `JUKAHUB_CRYPTO_KEY` environment variable:

```bash
# Generate a strong random 32-byte key (64 hex characters)
export JUKAHUB_CRYPTO_KEY="$(openssl rand -hex 32)"

# Or on Windows (PowerShell)
$env:JUKAHUB_CRYPTO_KEY = [System.Convert]::ToHex([System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
```

If you pass a 64-character hex value, the player uses it directly as the AES-256 key. If you pass a shorter or non-hex value, the player derives a 32-byte key from it with an HKDF-like HMAC-SHA256 expansion and the configured salt.

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
   - The Discord token is encrypted by default
   - Verify it shows the `ENC:` prefix in config

3. **Limit bot permissions**
   - Only grant necessary permissions
   - Use the principle of least privilege

### Transport and network security

- Outbound HTTP requests use HTTPS only for configured endpoints (GitHub API, update checks, tool downloads).
- GitHub API calls set a descriptive `User-Agent`, the `Accept` header for the intended API version, and respect `429`/`Retry-After` rate limiting.
- Updates and package archives are checksum-verified against signed metadata before use (see Patch, below).

## Patch / signed repositories

The Patch tool uses an Ed25519-signed repository index. The verification public key is embedded in the player (`patch_signed.go`), and every package archive is SHA-256-verified against the signed index before install. HTTPS alone is never treated as authenticity.

The signing private key lives only in the offline repository tooling:

```PATCH_SIGN_KEY=<hex private key> go run ./tools/build-patch-repo --src ./patch-packages --out ./patch-repo
```

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

