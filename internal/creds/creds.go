// Package creds makes the per-run secrets: the token in the link, the key the
// friend types, and the TLS certificate. Nothing here is ever written to disk;
// every run starts with fresh ones.
package creds

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strings"
)

// NewToken returns 256 random bits, URL-safe so they can sit in the link's path.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// TokenMatches compares in constant time, so response timing reveals nothing
// about how much of a guess was right.
func TokenMatches(got, token string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// Crockford's base32 alphabet: no I, L, O, or U, so a key read aloud over the
// phone can't be misheard as a lookalike letter.
const keyAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewKey returns 60 random bits as XXXX-XXXX-XXXX. It is the second factor
// next to the link's token, meant to travel by a different channel.
func NewKey() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var k strings.Builder
	for i, v := range b {
		if i > 0 && i%4 == 0 {
			k.WriteByte('-')
		}
		// 256 is a multiple of 32, so masking keeps every symbol equally likely.
		k.WriteByte(keyAlphabet[v&31])
	}
	return k.String(), nil
}

// KeyMatches forgives how a person types a key (case, spaces, dashes, and the
// lookalikes the alphabet leaves out) and compares in constant time.
func KeyMatches(typed, key string) bool {
	return subtle.ConstantTimeCompare([]byte(normalizeKey(typed)), []byte(normalizeKey(key))) == 1
}

func normalizeKey(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '-', ' ':
			return -1
		case 'o', 'O':
			return '0'
		case 'i', 'I', 'l', 'L':
			return '1'
		}
		if 'a' <= r && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}, s)
}
