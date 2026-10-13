package creds

import (
	"crypto/x509"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNewToken(t *testing.T) {
	a, _ := NewToken()
	b, _ := NewToken()
	if len(a) != 43 || a == b || strings.ContainsAny(a, "/+=") {
		t.Errorf("tokens %q and %q: want 43 distinct URL-safe characters", a, b)
	}
	if !TokenMatches(a, a) || TokenMatches(a, b) || TokenMatches(a[:42], a) || TokenMatches("", a) {
		t.Error("TokenMatches is wrong")
	}
}

func TestNewKey(t *testing.T) {
	format := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)
	seen := map[string]bool{}
	for range 100 {
		k, err := NewKey()
		if err != nil {
			t.Fatal(err)
		}
		if !format.MatchString(k) {
			t.Fatalf("key %q has the wrong shape", k)
		}
		if seen[k] {
			t.Fatalf("key %q repeated", k)
		}
		seen[k] = true
	}
}

func TestKeyMatchesForgivesTyping(t *testing.T) {
	const key = "0B1D-EFGH-JKMN"
	for _, typed := range []string{"0B1D-EFGH-JKMN", "0b1d efgh jkmn", "OB1DEFGHJKMN", "0BlD-EFGH-JKMN", "0BID-EFGH-JKMN"} {
		if !KeyMatches(typed, key) {
			t.Errorf("%q should match %q", typed, key)
		}
	}
	for _, typed := range []string{"", "0B1D-EFGH-JKM", "0B1D-EFGH-JKMP", "0B1D-EFGH-JKMN-0"} {
		if KeyMatches(typed, key) {
			t.Errorf("%q should not match %q", typed, key)
		}
	}
}

func TestNewCert(t *testing.T) {
	now := time.Now()
	cert, err := NewCert([]string{"127.0.0.1", "203.0.113.7", "drop.example"}, now)
	if err != nil {
		t.Fatal(err)
	}
	leaf := cert.Leaf
	for _, h := range []string{"127.0.0.1", "203.0.113.7", "drop.example"} {
		if leaf.VerifyHostname(h) != nil {
			t.Errorf("certificate does not cover %s", h)
		}
	}
	if leaf.VerifyHostname("198.51.100.1") == nil {
		t.Error("certificate covers a host it was not made for")
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Error("certificate is not limited to server auth")
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		t.Error("certificate is not valid now")
	}

	other, _ := NewCert([]string{"127.0.0.1"}, now)
	fp := Fingerprint(cert)
	switch {
	case len(strings.Split(fp, ":")) != 32:
		t.Errorf("fingerprint %q is not 32 hex pairs", fp)
	case fp != Fingerprint(cert):
		t.Error("fingerprint is not stable")
	case fp == Fingerprint(other):
		t.Error("two runs share a fingerprint")
	}
}
