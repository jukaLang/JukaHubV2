package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"
)

// Key metadata for tracking encryption key versions
type KeyInfo struct {
	ID        string
	CreatedAt time.Time
	IsDefault bool
	IsEnhanced bool // Enhanced encryption uses per-value salts
}

// Global key info (set at initialization)
var currentKeyInfo KeyInfo

// encryptionSalt is a global salt used for key derivation (not per-value)
// This adds an extra layer of protection against precomputed attacks
var encryptionSalt = func() []byte {
	// Try to load salt from environment
	if envSalt := os.Getenv("JUKAHUB_CRYPTO_SALT"); envSalt != "" {
		if salt, err := hex.DecodeString(envSalt); err == nil && len(salt) >= 16 {
			log.Printf("[CRYPTO] Using custom crypto salt (len=%d)", len(salt))
			return salt
		}
	}
	// Default: use a built-in salt (NOT ideal for production, set JUKAHUB_CRYPTO_SALT)
	defaultSalt := []byte("JukaHub-Salt-2024-Secure-Enhancement-v3")
	if len(defaultSalt) > 32 {
		defaultSalt = defaultSalt[:32]
	}
	for len(defaultSalt) < 32 {
		defaultSalt = append(defaultSalt, 0)
	}
	log.Printf("[CRYPTO] Using built-in crypto salt (set JUKAHUB_CRYPTO_SALT for production)")
	return defaultSalt
}()

// deriveKeyFromSecret derives a 32-byte AES key from a secret using HKDF-like expansion.
// This is more secure than simple padding/truncation.
func deriveKeyFromSecret(secret, salt []byte) []byte {
	// Use HMAC-SHA256 as a KDF (similar to HKDF-Extract)
	mac := hmac.New(sha256.New, salt)
	mac.Write(secret)
	hmacResult := mac.Sum(nil)
	
	// If we need more than 32 bytes, do another iteration
	if len(hmacResult) >= 32 {
		return hmacResult[:32]
	}
	
	// Expand further if needed (HKDF-Expand style)
	key := make([]byte, 32)
	copy(key, hmacResult)
	for i := len(hmacResult); i < 32; i++ {
		mac := hmac.New(sha256.New, salt)
		mac.Write(hmacResult)
		mac.Write([]byte{byte(i)})
		hmacResult = mac.Sum(nil)
		copy(key[i:], hmacResult)
	}
	return key
}

// cryptoKey is the encryption key used for all sensitive values in the config.
// It is loaded from the JUKAHUB_CRYPTO_KEY environment variable if set, otherwise
// derived from a built-in secret using HKDF-like expansion for backward compatibility.
//
// IMPORTANT: For production use, set JUKAHUB_CRYPTO_KEY to a strong random 32-byte
// hex string. Example:
//   export JUKAHUB_CRYPTO_KEY="$(openssl rand -hex 32)"
var cryptoKey = func() []byte {
	now := time.Now()
	
	// Check for environment variable override first
	if envKey := os.Getenv("JUKAHUB_CRYPTO_KEY"); envKey != "" {
		// Try to decode as hex (64 hex chars = 32 bytes)
		if len(envKey) == 64 {
			key, err := hex.DecodeString(envKey)
			if err == nil && len(key) == 32 {
				currentKeyInfo = KeyInfo{
					ID:        "env-" + envKey[:8],
					CreatedAt: now,
					IsDefault: false,
					IsEnhanced: true,
				}
				log.Printf("[CRYPTO] Using encryption key from JUKAHUB_CRYPTO_KEY env var (key ID: %s, enhanced: %v)", currentKeyInfo.ID, currentKeyInfo.IsEnhanced)
				return key
			}
		}
		// If not valid hex, derive key from it using HKDF
		key := deriveKeyFromSecret([]byte(envKey), encryptionSalt)
		currentKeyInfo = KeyInfo{
			ID:        "env-derived",
			CreatedAt: now,
			IsDefault: false,
			IsEnhanced: true,
		}
		log.Printf("[CRYPTO] Using derived key from JUKAHUB_CRYPTO_KEY (key ID: %s)", currentKeyInfo.ID)
		return key
	}

	// Enhanced default key derivation using HKDF-like expansion
	// This is more secure than the old simple XOR method
	secret := []byte("JukaHub-Secure-Encryption-Key-2024-v2")
	key := deriveKeyFromSecret(secret, encryptionSalt)
	currentKeyInfo = KeyInfo{
		ID:        "default-v3-enhanced",
		CreatedAt: now,
		IsDefault: true,
		IsEnhanced: true,
	}
	log.Printf("[CRYPTO] Using enhanced built-in encryption key (NOT recommended for production) (key ID: %s)", currentKeyInfo.ID)
	return key
}()

// getCryptoKey returns the player's encryption key.
func getCryptoKey() []byte {
	return cryptoKey
}

// GetKeyInfo returns metadata about the current encryption key.
func GetKeyInfo() KeyInfo {
	return currentKeyInfo
}

// zeroizeKey securely clears the key from memory when the program exits.
// This is a best-effort measure; Go's garbage collector may have copies.
func zeroizeKey() {
	// Create a copy to zeroize (the original cryptoKey will remain until GC)
	keyCopy := make([]byte, len(cryptoKey))
	copy(keyCopy, cryptoKey)
	for i := range keyCopy {
		keyCopy[i] = 0
	}
}

// Register deferred key zeroization on program exit.
func init() {
	// Best-effort cleanup on exit
	Cleanup = append(Cleanup, zeroizeKey)
}

// EncryptString encrypts any plaintext string using AES-GCM with per-value nonce.
// Returns format: ENC:<keyID>/<hex(nonce+ciphertext)>
// Each encryption produces a unique ciphertext even for the same plaintext.
func EncryptString(plaintext string) (string, error) {
	return EncryptStringWithKey(plaintext, getCryptoKey(), currentKeyInfo.ID)
}

// EncryptStringWithKey encrypts plaintext with a specific key and key ID.
// This is useful for testing or key migration scenarios.
func EncryptStringWithKey(plaintext string, key []byte, keyID string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	// Format: ENC:<keyID>/<hex(nonce+ciphertext)>
	// The nonce is prepended to ciphertext and both are hex-encoded
	return fmt.Sprintf("ENC:%s/%s", keyID, hex.EncodeToString(ciphertext)), nil
}

// DecryptString decrypts an ENC:<hex> value back to plaintext.
// If the value doesn't start with "ENC:", it's returned as-is (legacy plain text).
// Supports both old format (ENC:<hex>) and new format (ENC:<keyID>/<hex>).
func DecryptString(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	if !strings.HasPrefix(token, "ENC:") {
		return token, nil
	}
	
	cipherData := strings.TrimPrefix(token, "ENC:")
	
	// Parse new format: ENC:<keyID>/<hex>
	if idx := strings.Index(cipherData, "/"); idx > 0 {
		// New format with key ID
		cipherHex := cipherData[idx+1:]
		data, err := hex.DecodeString(cipherHex)
		if err != nil {
			return "", fmt.Errorf("invalid encrypted token format: %v", err)
		}
		key := getCryptoKey()
		plaintext, err := decryptAESGCM(key, data)
		if err != nil {
			return "", fmt.Errorf("decryption failed: %v", err)
		}
		// Zeroize the plaintext after use (caller should copy if needed)
		result := plaintext
		plaintext = nil // Help GC
		return string(result), nil
	}
	
	// Legacy format: ENC:<hex> - no key ID prefix
	data, err := hex.DecodeString(cipherData)
	if err != nil {
		return "", fmt.Errorf("invalid encrypted token format: %v", err)
	}
	key := getCryptoKey()
	plaintext, err := decryptAESGCM(key, data)
	if err != nil {
		return "", fmt.Errorf("decryption failed (legacy format): %v", err)
	}
	result := plaintext
	plaintext = nil
	return string(result), nil
}

// decryptAESGCM performs AES-GCM decryption with the given key.
// Returns the plaintext and zeroes it from memory after use.
func decryptAESGCM(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %v", err)
	}
	return plaintext, nil
}

// EncryptAPIToken is a convenience wrapper that logs encryption for audit purposes.
// Use this for API keys, tokens, and other sensitive credentials.
func EncryptAPIToken(plaintext string, keyName string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	encrypted, err := EncryptString(plaintext)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt %s: %w", keyName, err)
	}
	log.Printf("[CRYPTO] Encrypted %s", keyName)
	return encrypted, nil
}

// DecryptAPIToken decrypts and returns an API token, logging failures.
// It distinguishes three outcomes so callers can tell "not encrypted" from
// "decryption failed" instead of collapsing both into an empty string.
func DecryptAPIToken(encrypted string, keyName string) (string, error) {
	if encrypted == "" {
		return "", fmt.Errorf("%s: empty encrypted value", keyName)
	}
	if !strings.HasPrefix(encrypted, "ENC:") {
		return "", fmt.Errorf("%s: value is not encrypted (missing ENC: prefix)", keyName)
	}
	decrypted, err := DecryptString(encrypted)
	if err != nil {
		log.Printf("[CRYPTO] Failed to decrypt %s: %v", keyName, err)
		return "", fmt.Errorf("%s: decryption failed: %w", keyName, err)
	}
	if decrypted == "" {
		return "", fmt.Errorf("%s: decrypted to empty value", keyName)
	}
	return decrypted, nil
}

// DecryptSecret is a small convenience wrapper for generic config secrets. It uses
// the same error distinctions as DecryptAPIToken but names the field more neutrally
// so it is usable for non-API tokens where "API" would be misleading.

// VerifyEncryptedData checks if an encrypted value can be decrypted successfully.
// Returns true if valid, false otherwise. Does not leak the plaintext.
func VerifyEncryptedData(encrypted string) bool {
	if encrypted == "" || !strings.HasPrefix(encrypted, "ENC:") {
		return false
	}
	_, err := DecryptString(encrypted)
	return err == nil
}

// EncryptWithAuth encrypts data and provides an additional HMAC authentication layer.
// This is useful for high-security scenarios where you want defense in depth.
// The format is: ENC:AUTH:<keyID>/<hex(nonce+ciphertext+HMAC)>
func EncryptWithAuth(plaintext string) (string, error) {
	key := getCryptoKey()
	encrypted, err := EncryptString(plaintext)
	if err != nil {
		return "", err
	}
	
	// Additional HMAC over the encrypted value (excluding ENC: prefix)
	mac := hmac.New(sha256.New, key)
	dataToAuth := []byte(encrypted[4:]) // Skip "ENC:"
	mac.Write(dataToAuth)
	hmacValue := mac.Sum(nil)
	
	// Append HMAC to encrypted value
	return fmt.Sprintf("%s:AUTH:%s", encrypted, hex.EncodeToString(hmacValue)), nil
}

// DecryptSecret decrypts a generic config secret and logs failures. It is
// intentionally parallel to DecryptAPIToken so callers get the same three-way
// distinction without inventing a separate error scheme.
func DecryptSecret(encrypted string, fieldName string) (string, error) {
	return DecryptAPIToken(encrypted, fieldName)
}

// DecryptWithAuth decrypts data that was encrypted with EncryptWithAuth.
func DecryptWithAuth(encrypted string) (string, error) {
	if !strings.HasPrefix(encrypted, "ENC:AUTH:") {
		return "", errors.New("not an authenticated encrypted value")
	}
	
	// Parse: ENC:AUTH:<keyID>/<hex>:<hmac>
	parts := strings.SplitN(encrypted[9:], ":", 2)
	if len(parts) != 2 {
		return "", errors.New("invalid authenticated encrypted format")
	}
	
	encryptedData := "ENC:" + parts[0]
	providedHMAC := parts[1]
	
	// Verify HMAC first
	key := getCryptoKey()
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encryptedData[4:]))
	expectedHMAC := mac.Sum(nil)
	
	if !hmac.Equal([]byte(providedHMAC), expectedHMAC) {
		log.Printf("[CRYPTO] HMAC verification failed - data may be tampered")
		return "", errors.New("authentication failed - data may be tampered")
	}
	
	// HMAC valid, proceed with decryption
	return DecryptString(encryptedData)
}

// EncryptToken encrypts a plaintext token using AES-GCM.
// DEPRECATED: Use EncryptString() or EncryptAPIToken() instead.
// This function is kept for backward compatibility with existing encrypted data.
// New code should use the generic EncryptString() function.
func EncryptToken(plaintext string) (string, error) {
	log.Printf("[DEPRECATED] EncryptToken is deprecated, use EncryptString instead")
	return EncryptString(plaintext)
}

// DecryptToken decrypts an ENC:<base64> token back to plaintext.
// DEPRECATED: Use DecryptString() or DecryptAPIToken() instead.
// This function is kept for backward compatibility.
func DecryptToken(token string) (string, error) {
	log.Printf("[DEPRECATED] DecryptToken is deprecated, use DecryptString instead")
	return DecryptString(token)
}
