package logging_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/dreulavelle/azir/internal/logging"
	"github.com/dreulavelle/azir/internal/vault"
)

// canary is a sentinel credential. Nothing in Azir may ever emit it. The value
// is deliberately distinctive so a single grep proves the property, and the
// tests below fail loudly the moment redaction stops working.
const canary = "AZIR-CANARY-b7f3e91d4a2c8065-DO-NOT-EMIT"

func newLogger() (*logging.Handler, *slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := logging.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return h, slog.New(h), &buf
}

// The canary must not survive any route into a log line.
func TestCanaryNeverReachesLogs(t *testing.T) {
	h, log, buf := newLogger()
	h.Register(canary)

	cases := []struct {
		name string
		emit func()
	}{
		{"as a message", func() { log.Info("connecting with " + canary) }},
		{"as a sensitive-keyed attr", func() { log.Info("auth", "password", canary) }},
		{"as an innocuously-keyed attr", func() { log.Info("auth", "note", canary) }},
		{"inside an error", func() { log.Error("failed", "error", errors.New("using "+canary)) }},
		{"inside a group", func() {
			log.Info("cfg", slog.Group("creds", slog.String("api_key", canary)))
		}},
		{"in a formatted string", func() { log.Info(fmt.Sprintf("token=%s", canary)) }},
		{"via a derived logger", func() { log.With("secret", canary).Info("derived") }},
		{"via a derived logger, plain key", func() { log.With("detail", canary).Info("derived") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			tc.emit()
			if strings.Contains(buf.String(), canary) {
				t.Fatalf("canary reached the log via %s:\n%s", tc.name, buf.String())
			}
			if buf.Len() == 0 {
				t.Fatal("nothing was logged; the test proves nothing")
			}
		})
	}
}

// A canary test that cannot fail is worthless. This proves the detector works
// by checking that an unredacted logger does emit the sentinel — so a green
// suite means redaction ran, not that the check was vacuous.
func TestCanaryDetectorActuallyDetects(t *testing.T) {
	var buf bytes.Buffer
	plain := slog.New(slog.NewJSONHandler(&buf, nil))
	plain.Info("connecting with " + canary)

	if !strings.Contains(buf.String(), canary) {
		t.Fatal("an unredacted logger did not emit the canary; the test is vacuous")
	}
}

/*
Core must treat every field name the SDK treats as secret.

These were two hand-maintained lists and they drifted: the SDK grew the
telephony names — a SIP AuthID, a voicemail PIN, a licence key — when the 3CX
plugin was written, and core's never heard of them. Supervised plugins have
their stdout piped through this handler, so the gap meant a plugin's tool
output was protected while the same value in its log line was not.

They are one list now. This asserts it stays one, for a plugin that logs an
attribute rather than returning it.
*/
func TestCoreRedactsEveryKeyTheSDKConsidersSecret(t *testing.T) {
	// Named explicitly rather than ranged over an exported list: the point is
	// to fail if one of these stops being covered, and a test that iterates
	// whatever the source currently says would pass by construction.
	for _, key := range []string{
		"password", "passwd", "secret", "token", "apikey", "api_key",
		"authorization", "credential", "private_key", "session", "cookie",
		"authid", "auth_id", "vmpin", "sipid", "sip_id",
		"licensekey", "license_key", "pin",
		// Core's own, which no plugin has reason to emit.
		"dek", "master_key",
	} {
		t.Run(key, func(t *testing.T) {
			_, log, buf := newLogger()
			// Deliberately not registered as a literal: this is about the key
			// name alone, which is all core has for a value it never issued.
			log.Info("from a plugin", key, "AZIR-UNREGISTERED-VALUE-b7f3e91d")

			if strings.Contains(buf.String(), "AZIR-UNREGISTERED-VALUE") {
				t.Fatalf("a value under key %q reached the log: %s", key, buf.String())
			}
		})
	}
}

// Redaction must not be so aggressive that logs stop being useful.
func TestNonSecretsSurvive(t *testing.T) {
	h, log, buf := newLogger()
	h.Register(canary)

	log.Info("tool invoked", "plugin", "syncro", "tool", "tickets.search", "count", 12)

	out := buf.String()
	for _, want := range []string{"syncro", "tickets.search", "12"} {
		if !strings.Contains(out, want) {
			t.Errorf("redaction destroyed useful field %q: %s", want, out)
		}
	}
}

// A secret registered after loggers were derived must still be scrubbed, since
// credentials are resolved long after startup.
func TestLateRegistrationAppliesToExistingLoggers(t *testing.T) {
	h, log, buf := newLogger()
	derived := log.With("component", "plugin-3cx")

	h.Register(canary)
	derived.Info("connecting with " + canary)

	if strings.Contains(buf.String(), canary) {
		t.Fatalf("late-registered secret leaked: %s", buf.String())
	}
}

// The canary must survive a vault round trip unchanged, and must not appear in
// the sealed representation.
func TestCanaryRoundTripsThroughVaultWithoutExposure(t *testing.T) {
	key := make([]byte, vault.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	v, err := vault.New(map[int][]byte{1: key}, 1)
	if err != nil {
		t.Fatal(err)
	}

	sealed, err := v.Seal([]byte(canary), nil)
	if err != nil {
		t.Fatal(err)
	}

	for name, blob := range map[string][]byte{
		"ciphertext":  sealed.Ciphertext,
		"dek_wrapped": sealed.DEKWrapped,
		"nonce":       sealed.Nonce,
	} {
		if bytes.Contains(blob, []byte(canary)) {
			t.Errorf("canary is present in %s as plaintext", name)
		}
		if strings.Contains(base64.StdEncoding.EncodeToString(blob), canary) {
			t.Errorf("canary is present in base64-encoded %s", name)
		}
	}

	opened, err := v.Open(sealed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != canary {
		t.Fatalf("vault did not round-trip the secret intact")
	}
}
