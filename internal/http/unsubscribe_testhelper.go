package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// encodeTokenForTest builds an unsubscribe-format token from an already-
// assembled payload. Lives outside _test.go so the internal helper is
// accessible from the test file without exporting the underlying HMAC dance.
func encodeTokenForTest(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(mac.Sum(nil))
}
