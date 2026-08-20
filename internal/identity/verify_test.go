package identity

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// A stored hash that HashPassword did not write must be rejected, not crash.
// The empty-key case panicked inside blake2b before this was checked.
func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, saltLen))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, argonKeyLen))

	for name, encoded := range map[string]string{
		"empty key":       fmt.Sprintf("argon2id$2$65536$4$%s$", salt),
		"short key":       fmt.Sprintf("argon2id$2$65536$4$%s$%s", salt, base64.RawStdEncoding.EncodeToString(make([]byte, 8))),
		"empty salt":      fmt.Sprintf("argon2id$2$65536$4$$%s", key),
		"zero time":       fmt.Sprintf("argon2id$0$65536$4$%s$%s", salt, key),
		"zero threads":    fmt.Sprintf("argon2id$2$65536$0$%s$%s", salt, key),
		"absurd memory":   fmt.Sprintf("argon2id$2$999999999$4$%s$%s", salt, key),
		"tiny memory":     fmt.Sprintf("argon2id$2$1$4$%s$%s", salt, key),
		"negative time":   fmt.Sprintf("argon2id$-1$65536$4$%s$%s", salt, key),
		"wrong algorithm": fmt.Sprintf("bcrypt$2$65536$4$%s$%s", salt, key),
		"too few fields":  "argon2id$2$65536$4$" + salt,
		"empty string":    "",
		"not base64":      "argon2id$2$65536$4$!!!!$????",
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("VerifyPassword panicked on a %s hash: %v", name, r)
				}
			}()
			if VerifyPassword(encoded, "any-password-at-all") {
				t.Fatalf("a %s hash verified an arbitrary password", name)
			}
		})
	}
}

// The bounds must not have been drawn so tightly that real hashes fail.
func TestVerifyPasswordStillAcceptsWhatHashPasswordWrites(t *testing.T) {
	encoded, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(encoded, "correct-horse-battery-staple") {
		t.Fatal("a freshly written hash did not verify its own password")
	}
	if VerifyPassword(encoded, "the-wrong-password-entirely") {
		t.Fatal("the wrong password verified")
	}
	if n := len(strings.Split(encoded, "$")); n != 6 {
		t.Fatalf("encoded form has %d fields, want 6", n)
	}
}
