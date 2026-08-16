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

	sealed, err := v.Seal([]byte(canary))
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

	opened, err := v.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != canary {
		t.Fatalf("vault did not round-trip the secret intact")
	}
}
