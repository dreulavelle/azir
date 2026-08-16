package vault_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/dreulavelle/azir/internal/vault"
)

func key(seed byte) []byte {
	k := make([]byte, vault.KeySize)
	for i := range k {
		k[i] = seed ^ byte(i)
	}
	return k
}

func newVault(t *testing.T, keys map[int][]byte, current int) *vault.Vault {
	t.Helper()
	v, err := vault.New(keys, current)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSealOpenRoundTrip(t *testing.T) {
	v := newVault(t, map[int][]byte{1: key(0x11)}, 1)

	for _, plaintext := range []string{
		"short",
		"a-perfectly-ordinary-api-key",
		strings.Repeat("long", 5000),
		"unicode: ✓ 日本語 emoji 🔐",
	} {
		sealed, err := v.Seal([]byte(plaintext))
		if err != nil {
			t.Fatalf("seal %q: %v", plaintext[:min(len(plaintext), 20)], err)
		}
		opened, err := v.Open(sealed)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if string(opened) != plaintext {
			t.Errorf("round trip corrupted the secret")
		}
	}
}

// Two seals of the same plaintext must differ. Identical ciphertext would leak
// that two customers share a password.
func TestSealIsNotDeterministic(t *testing.T) {
	v := newVault(t, map[int][]byte{1: key(0x22)}, 1)

	a, err := v.Seal([]byte("same-secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := v.Seal([]byte("same-secret-value"))
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(a.Ciphertext, b.Ciphertext) {
		t.Error("identical plaintexts produced identical ciphertext; equal secrets are distinguishable")
	}
	if bytes.Equal(a.Nonce, b.Nonce) {
		t.Error("nonce was reused, which breaks GCM")
	}
	if bytes.Equal(a.DEKWrapped, b.DEKWrapped) {
		t.Error("the same data key was reused across secrets")
	}
}

// Plaintext must appear in none of the stored fields.
func TestSealedFieldsCarryNoPlaintext(t *testing.T) {
	v := newVault(t, map[int][]byte{1: key(0x33)}, 1)
	const secret = "AZIR-CANARY-vault-b7f3e91d-DO-NOT-EMIT"

	sealed, err := v.Seal([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}

	for name, blob := range map[string][]byte{
		"ciphertext":  sealed.Ciphertext,
		"dek_wrapped": sealed.DEKWrapped,
		"nonce":       sealed.Nonce,
		"dek_nonce":   sealed.DEKNonce,
	} {
		if bytes.Contains(blob, []byte(secret)) {
			t.Errorf("%s contains the plaintext", name)
		}
		if strings.Contains(base64.StdEncoding.EncodeToString(blob), secret) {
			t.Errorf("%s contains the plaintext when base64-encoded", name)
		}
	}
}

// GCM authenticates. Tampering must fail loudly rather than yielding plausible
// garbage that a caller might use as a credential.
func TestTamperedCiphertextIsRejected(t *testing.T) {
	v := newVault(t, map[int][]byte{1: key(0x44)}, 1)

	sealed, err := v.Seal([]byte("an-api-key-worth-protecting"))
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(s *vault.Sealed){
		"ciphertext":  func(s *vault.Sealed) { s.Ciphertext[0] ^= 0xFF },
		"nonce":       func(s *vault.Sealed) { s.Nonce[0] ^= 0xFF },
		"dek_wrapped": func(s *vault.Sealed) { s.DEKWrapped[0] ^= 0xFF },
		"dek_nonce":   func(s *vault.Sealed) { s.DEKNonce[0] ^= 0xFF },
	}

	for field, corrupt := range tests {
		t.Run(field, func(t *testing.T) {
			damaged := sealed
			damaged.Ciphertext = bytes.Clone(sealed.Ciphertext)
			damaged.Nonce = bytes.Clone(sealed.Nonce)
			damaged.DEKWrapped = bytes.Clone(sealed.DEKWrapped)
			damaged.DEKNonce = bytes.Clone(sealed.DEKNonce)
			corrupt(&damaged)

			if _, err := v.Open(damaged); err == nil {
				t.Errorf("tampering with %s was not detected", field)
			}
		})
	}
}

// A secret sealed under one key must not open under another.
func TestWrongKeyCannotOpen(t *testing.T) {
	sealer := newVault(t, map[int][]byte{1: key(0x55)}, 1)
	sealed, err := sealer.Seal([]byte("cross-key-secret"))
	if err != nil {
		t.Fatal(err)
	}

	// Same version number, different key material — the case where someone
	// restores a database against the wrong master key.
	imposter := newVault(t, map[int][]byte{1: key(0x66)}, 1)
	if _, err := imposter.Open(sealed); err == nil {
		t.Fatal("a secret opened under the wrong master key")
	}
}

// Rotation re-wraps the data key without touching ciphertext, and the secret
// stays readable.
func TestRewrapKeepsSecretReadable(t *testing.T) {
	k1, k2 := key(0x77), key(0x88)

	v1 := newVault(t, map[int][]byte{1: k1}, 1)
	sealed, err := v1.Seal([]byte("rotate-me"))
	if err != nil {
		t.Fatal(err)
	}
	originalCiphertext := bytes.Clone(sealed.Ciphertext)

	v2 := newVault(t, map[int][]byte{1: k1, 2: k2}, 2)
	rewrapped, err := v2.Rewrap(sealed)
	if err != nil {
		t.Fatal(err)
	}

	if rewrapped.KeyVersion != 2 {
		t.Errorf("want key version 2, got %d", rewrapped.KeyVersion)
	}
	if !bytes.Equal(rewrapped.Ciphertext, originalCiphertext) {
		t.Error("rewrap altered the ciphertext; the payload should never be re-encrypted")
	}
	if bytes.Equal(rewrapped.DEKWrapped, sealed.DEKWrapped) {
		t.Error("the data key was not re-wrapped")
	}

	opened, err := v2.Open(rewrapped)
	if err != nil {
		t.Fatalf("secret unreadable after rotation: %v", err)
	}
	if string(opened) != "rotate-me" {
		t.Error("rotation corrupted the secret")
	}

	// Rewrapping something already current is a no-op, not an error.
	again, err := v2.Rewrap(rewrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.DEKWrapped, rewrapped.DEKWrapped) {
		t.Error("rewrap of an already-current secret changed it")
	}
}

// Dropping an old key must make its secrets unreadable rather than silently
// opening them with a different one.
func TestUnknownKeyVersionIsRefused(t *testing.T) {
	v1 := newVault(t, map[int][]byte{1: key(0x99)}, 1)
	sealed, err := v1.Seal([]byte("sealed-under-v1"))
	if err != nil {
		t.Fatal(err)
	}

	v2 := newVault(t, map[int][]byte{2: key(0xAA)}, 2)

	_, err = v2.Open(sealed)
	if !errors.Is(err, vault.ErrUnknownVersion) {
		t.Fatalf("want ErrUnknownVersion, got %v", err)
	}
	if _, err := v2.Rewrap(sealed); !errors.Is(err, vault.ErrUnknownVersion) {
		t.Fatalf("rewrap want ErrUnknownVersion, got %v", err)
	}
}

func TestNewRejectsBadConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		keys    map[int][]byte
		current int
		want    error
	}{
		{"no keys", map[int][]byte{}, 1, vault.ErrNoKeys},
		{"short key", map[int][]byte{1: []byte("too-short")}, 1, vault.ErrBadKeyLength},
		{"current not loaded", map[int][]byte{1: key(0xBB)}, 7, vault.ErrUnknownVersion},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := vault.New(tc.keys, tc.current); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestFromEnv(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(key(0xCC))

	t.Run("single key becomes version 1", func(t *testing.T) {
		t.Setenv("AZIR_MASTER_KEY", encoded)
		v, err := vault.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if v.CurrentVersion() != 1 {
			t.Errorf("want version 1, got %d", v.CurrentVersion())
		}
	})

	t.Run("multiple keys pick the highest by default", func(t *testing.T) {
		second := base64.StdEncoding.EncodeToString(key(0xDD))
		t.Setenv("AZIR_MASTER_KEYS", "1:"+encoded+",3:"+second)
		v, err := vault.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if v.CurrentVersion() != 3 {
			t.Errorf("want version 3, got %d", v.CurrentVersion())
		}
	})

	t.Run("explicit version wins", func(t *testing.T) {
		second := base64.StdEncoding.EncodeToString(key(0xEE))
		t.Setenv("AZIR_MASTER_KEYS", "1:"+encoded+",3:"+second)
		t.Setenv("AZIR_KEY_VERSION", "1")
		v, err := vault.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if v.CurrentVersion() != 1 {
			t.Errorf("want version 1, got %d", v.CurrentVersion())
		}
	})

	t.Run("no key at all is an error, never a default", func(t *testing.T) {
		t.Setenv("AZIR_MASTER_KEY", "")
		t.Setenv("AZIR_MASTER_KEYS", "")
		if _, err := vault.FromEnv(); !errors.Is(err, vault.ErrNoKeys) {
			t.Fatalf("want ErrNoKeys, got %v", err)
		}
	})

	t.Run("malformed base64 is refused", func(t *testing.T) {
		t.Setenv("AZIR_MASTER_KEYS", "")
		t.Setenv("AZIR_MASTER_KEY", "not!valid!base64")
		if _, err := vault.FromEnv(); err == nil {
			t.Fatal("a malformed key was accepted")
		}
	})

	t.Run("a key of the wrong length is refused", func(t *testing.T) {
		t.Setenv("AZIR_MASTER_KEYS", "")
		t.Setenv("AZIR_MASTER_KEY", base64.StdEncoding.EncodeToString([]byte("only-sixteen-byt")))
		if _, err := vault.FromEnv(); !errors.Is(err, vault.ErrBadKeyLength) {
			t.Fatalf("want ErrBadKeyLength, got %v", err)
		}
	})
}

// No error from this package may quote the plaintext it failed to handle.
func TestErrorsCarryNoPlaintext(t *testing.T) {
	const secret = "AZIR-CANARY-err-b7f3e91d-DO-NOT-EMIT"

	v1 := newVault(t, map[int][]byte{1: key(0x12)}, 1)
	sealed, err := v1.Seal([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}

	v2 := newVault(t, map[int][]byte{1: key(0x34)}, 1)
	_, err = v2.Open(sealed)
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error quoted the secret: %v", err)
	}
}
