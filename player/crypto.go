package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// getCryptoKey returns the encryption key from environment or a dev default.
// In production, set JUKAHUB_CRYPTO_KEY to a 32-byte hex string.
func getCryptoKey() []byte {
	if key := os.Getenv("JUKAHUB_CRYPTO_KEY"); key != "" {
		if len(key) == 64 {
			b, _ := hex.DecodeString(key)
			return b
		}
	}
	// Dev fallback - change this or set env var in production
	devKey := "JukaHub-Discord-Token-Encryption-Key-2024"
	if len(devKey) > 32 {
		devKey = devKey[:32]
	}
	for len(devKey) < 32 {
		devKey += "0"
	}
	return []byte(devKey)
}

// EncryptToken encrypts a plaintext token using AES-GCM.
// Returns format: ENC:<base64 ciphertext>
func EncryptToken(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	key := getCryptoKey()
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
	return "ENC:" + hex.EncodeToString(ciphertext), nil
}

// DecryptToken decrypts an ENC:<base64> token back to plaintext.
// If the token doesn't start with "ENC:", it's returned as-is (legacy plain text).
func DecryptToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	if !strings.HasPrefix(token, "ENC:") {
		return token, nil
	}
	data, err := hex.DecodeString(strings.TrimPrefix(token, "ENC:"))
	if err != nil {
		return "", fmt.Errorf("invalid encrypted token format: %v", err)
	}
	key := getCryptoKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed: %v", err)
	}
	return string(plaintext), nil
}
