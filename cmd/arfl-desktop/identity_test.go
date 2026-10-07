package main

import (
	"regexp"
	"testing"
)

func TestFingerprintIsFourGroupsAndStable(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	fp := fingerprint(key)
	if !regexp.MustCompile(`^[A-Z2-7]{4}( · [A-Z2-7]{4}){3}$`).MatchString(fp) {
		t.Fatalf("unexpected fingerprint format %q", fp)
	}
	if fingerprint(key) != fp {
		t.Fatal("fingerprint is not stable for the same key")
	}
	if fingerprint([]byte("another key entirely, 32 bytes!!")) == fp {
		t.Fatal("different keys produced the same fingerprint")
	}
}
