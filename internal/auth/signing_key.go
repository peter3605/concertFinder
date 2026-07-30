package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// SigningKey returns the bytes to use for HMAC signatures (CSRF token,
// unsubscribe token, anything else non-encryption). Prefers an explicitly
// configured signing key; falls back to HMAC-SHA256(encryptionKey, "signing")
// so a single-key setup still gets a domain-separated derived key rather
// than reusing the encryption key raw.
func SigningKey(explicit string, encryptionKey []byte) []byte {
	if explicit != "" {
		if b, err := hex.DecodeString(explicit); err == nil {
			return b
		}
		// Fall back to raw bytes if not hex-encoded.
		return []byte(explicit)
	}
	mac := hmac.New(sha256.New, encryptionKey)
	mac.Write([]byte("concertfinder-signing-v1"))
	return mac.Sum(nil)
}
