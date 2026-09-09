package domain

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a random 128-bit identifier in hexadecimal. Identifiers are
// opaque, so a plain random string is preferred to a dependency on a UUID
// package.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform this runs on; treating it
		// as fatal is better than silently issuing a predictable identifier.
		panic("domain: cannot read random bytes: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
