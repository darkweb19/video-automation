package app

import (
	"encoding/hex"
	"strings"
)

// validLowerHexDigest validates a lowercase SHA-256-shaped identity. The
// digest may identify request intent or legacy media, depending on its caller.
func validLowerHexDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
