package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

const keySize = 32 // AES-256

// DecodeKey parses a 32-byte AES-256 key from a hex-encoded string.
func DecodeKey(hexKey string) ([]byte, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("encryption key must be %d bytes, got %d", keySize, len(key))
	}
	return key, nil
}

// EncryptToken encrypts plaintext with AES-256-GCM using a fresh random nonce.
// Returns (ciphertext-with-tag, nonce). The nonce must be stored alongside the
// ciphertext to decrypt later.
func EncryptToken(key, plaintext []byte) (ct, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	ct = gcm.Seal(nil, nonce, plaintext, nil)
	return ct, nonce, nil
}

// DecryptToken reverses EncryptToken. Returns an error if the ciphertext or nonce is invalid.
func DecryptToken(key, ct, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ct, nil)
}
