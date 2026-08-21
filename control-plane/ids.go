package controlplane

import (
	"crypto/rand"
	"encoding/base64"
)

// newID returns a URL-safe random id for sessions.
func newID() string {
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
