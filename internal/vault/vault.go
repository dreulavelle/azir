// Package vault seals and opens credential material.
//
// Envelope encryption: each secret is sealed with a freshly generated data
// key, and that data key is wrapped with a versioned master key. Rotation
// re-wraps data keys and never touches ciphertext, and a compromised data key
// exposes exactly one secret.
//
// Nothing in this package logs, formats or returns plaintext in an error.
// Callers are responsible for the same discipline: a credential that reaches a
// log line has leaked just as surely as one that reaches a prompt.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// KeySize is the required master key length: AES-256.
const KeySize = 32

var (
	// ErrNoKeys is returned when no master key is configured.
	ErrNoKeys = errors.New("vault: no master key configured")
	// ErrUnknownVersion means a stored secret references a key that is not
	// loaded. The secret is unreadable until that key is supplied — the vault
	// never silently substitutes another.
	ErrUnknownVersion = errors.New("vault: unknown key version")
	// ErrBadKeyLength is returned for a key that is not 32 bytes.
	ErrBadKeyLength = errors.New("vault: master key must be 32 bytes")
)

// Sealed is an encrypted secret as stored. It contains no plaintext and is
// safe to pass around, though not to log.
type Sealed struct {
	DEKWrapped []byte
	DEKNonce   []byte
	Ciphertext []byte
	Nonce      []byte
	KeyVersion int
}

// Vault holds master keys by version. Older versions are retained so that
// secrets sealed before a rotation remain readable until they are re-wrapped.
type Vault struct {
	keys    map[int][]byte
	current int
}

// New builds a Vault. current must be present in keys.
func New(keys map[int][]byte, current int) (*Vault, error) {
	if len(keys) == 0 {
		return nil, ErrNoKeys
	}
	for v, k := range keys {
		if len(k) != KeySize {
			return nil, fmt.Errorf("%w: version %d", ErrBadKeyLength, v)
		}
	}
	if _, ok := keys[current]; !ok {
		return nil, fmt.Errorf("%w: current version %d not loaded", ErrUnknownVersion, current)
	}
	return &Vault{keys: keys, current: current}, nil
}

// FromEnv builds a Vault from environment variables.
//
//	AZIR_MASTER_KEY    base64 32-byte key, becomes version 1
//	AZIR_MASTER_KEYS   "1:<base64>,2:<base64>" for multi-version deployments
//	AZIR_KEY_VERSION   which version to seal new secrets with (default: highest)
//
// Keys arrive by environment rather than by file so that a Docker secret or a
// platform secret store can supply them without landing on disk.
func FromEnv() (*Vault, error) {
	keys := map[int][]byte{}

	if multi := os.Getenv("AZIR_MASTER_KEYS"); multi != "" {
		for pair := range strings.SplitSeq(multi, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			version, encoded, ok := strings.Cut(pair, ":")
			if !ok {
				return nil, errors.New("vault: AZIR_MASTER_KEYS entries must be <version>:<base64>")
			}
			v, err := strconv.Atoi(strings.TrimSpace(version))
			if err != nil {
				return nil, fmt.Errorf("vault: bad key version %q", version)
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
			if err != nil {
				return nil, fmt.Errorf("vault: key version %d is not valid base64", v)
			}
			keys[v] = raw
		}
	} else if single := os.Getenv("AZIR_MASTER_KEY"); single != "" {
		raw, err := base64.StdEncoding.DecodeString(single)
		if err != nil {
			return nil, errors.New("vault: AZIR_MASTER_KEY is not valid base64")
		}
		keys[1] = raw
	}

	if len(keys) == 0 {
		return nil, ErrNoKeys
	}

	current := 0
	for v := range keys {
		if v > current {
			current = v
		}
	}
	if explicit := os.Getenv("AZIR_KEY_VERSION"); explicit != "" {
		v, err := strconv.Atoi(explicit)
		if err != nil {
			return nil, fmt.Errorf("vault: bad AZIR_KEY_VERSION %q", explicit)
		}
		current = v
	}
	return New(keys, current)
}

// CurrentVersion reports which master key seals new secrets.
func (v *Vault) CurrentVersion() int { return v.current }

// Seal encrypts plaintext under a fresh data key.
func (v *Vault) Seal(plaintext []byte) (Sealed, error) {
	dek := make([]byte, KeySize)
	if _, err := rand.Read(dek); err != nil {
		return Sealed{}, fmt.Errorf("vault: generate data key: %w", err)
	}

	ciphertext, nonce, err := sealWith(dek, plaintext)
	if err != nil {
		return Sealed{}, err
	}
	wrapped, dekNonce, err := sealWith(v.keys[v.current], dek)
	if err != nil {
		return Sealed{}, err
	}

	return Sealed{
		DEKWrapped: wrapped,
		DEKNonce:   dekNonce,
		Ciphertext: ciphertext,
		Nonce:      nonce,
		KeyVersion: v.current,
	}, nil
}

// Open decrypts a sealed secret.
func (v *Vault) Open(s Sealed) ([]byte, error) {
	master, ok := v.keys[s.KeyVersion]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownVersion, s.KeyVersion)
	}
	dek, err := openWith(master, s.DEKWrapped, s.DEKNonce)
	if err != nil {
		return nil, fmt.Errorf("vault: unwrap data key: %w", err)
	}
	plaintext, err := openWith(dek, s.Ciphertext, s.Nonce)
	if err != nil {
		return nil, fmt.Errorf("vault: open secret: %w", err)
	}
	return plaintext, nil
}

// Rewrap moves a sealed secret onto the current master key without decrypting
// its payload. This is what makes key rotation cheap.
func (v *Vault) Rewrap(s Sealed) (Sealed, error) {
	if s.KeyVersion == v.current {
		return s, nil
	}
	old, ok := v.keys[s.KeyVersion]
	if !ok {
		return Sealed{}, fmt.Errorf("%w: %d", ErrUnknownVersion, s.KeyVersion)
	}
	dek, err := openWith(old, s.DEKWrapped, s.DEKNonce)
	if err != nil {
		return Sealed{}, fmt.Errorf("vault: unwrap data key: %w", err)
	}
	wrapped, dekNonce, err := sealWith(v.keys[v.current], dek)
	if err != nil {
		return Sealed{}, err
	}
	s.DEKWrapped = wrapped
	s.DEKNonce = dekNonce
	s.KeyVersion = v.current
	return s, nil
}

func sealWith(key, plaintext []byte) (ciphertext, nonce []byte, err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("vault: generate nonce: %w", err)
	}
	return gcm.Seal(nil, nonce, plaintext, nil), nonce, nil
}

func openWith(key, ciphertext, nonce []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("vault: nonce has wrong length")
	}
	// GCM authenticates, so a tampered ciphertext fails here rather than
	// producing plausible garbage.
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrBadKeyLength
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: new cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// GenerateKey returns a base64 master key suitable for AZIR_MASTER_KEY.
func GenerateKey() (string, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k), nil
}
