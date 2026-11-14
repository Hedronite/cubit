package config

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// MaxLabel is the longest `workspace` label, in bytes.
const MaxLabel = 64

// Label derives a workspace label from its relative path: lowercase, every byte
// outside [a-z0-9._-] other than the "/" between components replaced by "_",
// cut to 64 bytes. Non-ASCII bytes each become "_", so the result is ASCII and
// does not depend on locale or normalization. The result always matches the
// scan schema's workspace pattern for a rel path without "." or ".." parts.
func Label(rel string) string {
	b := make([]byte, 0, len(rel))
	for i := 0; i < len(rel); i++ {
		c := rel[i]
		switch {
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-', c == '/':
		default:
			c = '_'
		}
		b = append(b, c)
	}
	s := string(b)
	if len(s) > MaxLabel {
		s = strings.TrimRight(s[:MaxLabel], "/")
	}
	return s
}

// Suffixed makes a label unique: the label cut to leave room, then "_" and the
// first hexLen hex digits of the sha256 of the relative path, so a collision
// gets the same suffix every run.
func Suffixed(label, rel string, hexLen int) string {
	sum := sha256.Sum256([]byte(rel))
	h := hex.EncodeToString(sum[:])[:hexLen]
	keep := MaxLabel - 1 - hexLen
	if len(label) > keep {
		label = strings.TrimRight(label[:keep], "/")
	}
	return label + "_" + h
}
