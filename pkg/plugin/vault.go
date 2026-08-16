package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// VaultSubjectPrefix is where core answers credential resolution requests. The
// plugin's own name is the final token.
//
// Per-plugin subjects rather than one shared subject so that authorisation can
// be enforced by NATS itself: a plugin granted publish rights only on
// azir.vault.resolve.syncro cannot ask for another plugin's credentials, no
// matter what its request body claims. Core cannot verify a self-declared
// name, so until per-plugin nkeys are issued this is the enforcement point
// rather than the enforcement.
//
// Deliberately NOT under [SubjectPrefix]: tools under azir.tool.* are
// discoverable and become model-facing capabilities. Credential resolution
// must never be either, and nothing here is advertised in $SRV.
const VaultSubjectPrefix = "azir.vault.resolve"

// VaultSubject returns the resolution subject for a plugin.
func VaultSubject(pluginName string) string {
	return VaultSubjectPrefix + "." + pluginName
}

// ErrNoCredential means no credential is configured for the requested scope.
var ErrNoCredential = errors.New("plugin: no credential configured for this scope")

// vaultRequest is the wire format for a resolution.
type vaultRequest struct {
	CustomerID string `json:"customer_id,omitempty"`
	Plugin     string `json:"plugin"`
	Kind       string `json:"kind"`
}

type vaultResponse struct {
	Value string `json:"value"`
}

// Vault resolves credentials for a plugin, server-side, and caches them in
// process memory.
//
// The cache is memory-only with a short TTL and is never serialised: if
// another component could fetch it, it could eventually be fetched into a
// prompt. Resolved values are registered with the redactor as they arrive, so
// a credential cannot escape through a log line or a handler's return value
// even if a plugin author is careless with it.
type Vault struct {
	nc      *nats.Conn
	plugin  string
	red     *Redactor
	onLearn func(string)
	ttl     time.Duration

	mu    sync.RWMutex
	cache map[string]cachedSecret
}

type cachedSecret struct {
	value   string
	expires time.Time
}

func newVault(nc *nats.Conn, pluginName string, red *Redactor, onLearn func(string)) *Vault {
	return &Vault{
		nc:      nc,
		plugin:  pluginName,
		red:     red,
		onLearn: onLearn,
		ttl:     5 * time.Minute,
		cache:   map[string]cachedSecret{},
	}
}

// For resolves a credential for a customer. Pass an empty customerID for a
// deployment-wide secret.
//
// The returned value must not be logged, formatted into an error, or included
// in a handler's return value. It is registered for redaction automatically,
// which is a backstop rather than a licence.
func (v *Vault) For(ctx context.Context, customerID, kind string) (string, error) {
	key := customerID + "\x00" + kind

	v.mu.RLock()
	if hit, ok := v.cache[key]; ok && time.Now().Before(hit.expires) {
		v.mu.RUnlock()
		return hit.value, nil
	}
	v.mu.RUnlock()

	payload, err := json.Marshal(vaultRequest{
		CustomerID: customerID,
		Plugin:     v.plugin,
		Kind:       kind,
	})
	if err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	msg, err := v.nc.RequestWithContext(reqCtx, VaultSubject(v.plugin), payload)
	if err != nil {
		return "", fmt.Errorf("plugin: vault unreachable: %w", err)
	}
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		if code == "404" {
			return "", ErrNoCredential
		}
		return "", fmt.Errorf("plugin: vault refused resolution (%s)", code)
	}

	var resp vaultResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return "", errors.New("plugin: vault response could not be decoded")
	}
	if resp.Value == "" {
		return "", ErrNoCredential
	}

	// Registered before returning, so the value is redactable from the first
	// moment it exists in this process.
	v.red.Learn(resp.Value)
	if v.onLearn != nil {
		v.onLearn(resp.Value)
	}

	v.mu.Lock()
	v.cache[key] = cachedSecret{value: resp.Value, expires: time.Now().Add(v.ttl)}
	v.mu.Unlock()

	return resp.Value, nil
}

// Forget drops a cached credential. Call it when a vendor rejects a
// credential, so a rotation is picked up without waiting out the TTL.
func (v *Vault) Forget(customerID, kind string) {
	v.mu.Lock()
	delete(v.cache, customerID+"\x00"+kind)
	v.mu.Unlock()
}

// vaultKey is the context key carrying the Vault into handlers.
type vaultKey struct{}

// VaultFrom returns the Vault for this plugin. It is present in every handler
// context.
func VaultFrom(ctx context.Context) (*Vault, bool) {
	v, ok := ctx.Value(vaultKey{}).(*Vault)
	return v, ok
}

func withVault(ctx context.Context, v *Vault) context.Context {
	return context.WithValue(ctx, vaultKey{}, v)
}
