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

// IdentitySubjectPrefix is where core translates between Azir's customer spine
// and a plugin's own identifiers. Per-plugin, and outside azir.tool.*, for the
// same reasons as vault and config resolution.
const IdentitySubjectPrefix = "azir.identity.resolve"

// IdentitySubject returns the identity subject for a plugin.
func IdentitySubject(pluginName string) string {
	return IdentitySubjectPrefix + "." + pluginName
}

// ErrNoIdentity means this customer has no record in this plugin's system.
// That is an ordinary state — a walk-in with no PSA account is a real
// customer — so a tool should say so rather than fail.
var ErrNoIdentity = errors.New("plugin: customer has no identity in this system")

type identityRequest struct {
	// Exactly one of these is set.
	CustomerID string `json:"customer_id,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
}

type identityResponse struct {
	CustomerID  string `json:"customer_id"`
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
}

// Identity translates between Azir's customer spine and a plugin's own
// identifiers.
//
// This exists because a handler receives an opaque Azir customer id and a
// vendor API wants its own. The alternative — passing vendor identifiers
// through the model — would defeat the spine: memory would attach to a Syncro
// number, and switching PSA later would orphan it.
type Identity struct {
	nc     *nats.Conn
	plugin string
	ttl    time.Duration

	mu    sync.RWMutex
	cache map[string]identityResponse
	at    map[string]time.Time
}

func newIdentity(nc *nats.Conn, pluginName string) *Identity {
	return &Identity{
		nc:     nc,
		plugin: pluginName,
		ttl:    2 * time.Minute,
		cache:  map[string]identityResponse{},
		at:     map[string]time.Time{},
	}
}

// External returns this plugin's identifier for an Azir customer.
func (i *Identity) External(ctx context.Context, customerID string) (string, error) {
	if customerID == "" {
		return "", ErrNoIdentity
	}
	resp, err := i.resolve(ctx, "c:"+customerID, identityRequest{CustomerID: customerID})
	if err != nil {
		return "", err
	}
	return resp.ExternalID, nil
}

// Customer returns the Azir customer id for one of this plugin's identifiers,
// along with the display name. Used when ingesting: a ticket arrives carrying
// a vendor customer id and has to be attached to the spine.
func (i *Identity) Customer(ctx context.Context, externalID string) (id, displayName string, err error) {
	if externalID == "" {
		return "", "", ErrNoIdentity
	}
	resp, err := i.resolve(ctx, "e:"+externalID, identityRequest{ExternalID: externalID})
	if err != nil {
		return "", "", err
	}
	return resp.CustomerID, resp.DisplayName, nil
}

func (i *Identity) resolve(ctx context.Context, key string, req identityRequest) (identityResponse, error) {
	i.mu.RLock()
	if hit, ok := i.cache[key]; ok && time.Now().Before(i.at[key].Add(i.ttl)) {
		i.mu.RUnlock()
		return hit, nil
	}
	i.mu.RUnlock()

	payload, err := json.Marshal(req)
	if err != nil {
		return identityResponse{}, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	msg, err := i.nc.RequestWithContext(reqCtx, IdentitySubject(i.plugin), payload)
	if err != nil {
		return identityResponse{}, fmt.Errorf("plugin: identity service unreachable: %w", err)
	}
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		if code == "404" {
			return identityResponse{}, ErrNoIdentity
		}
		return identityResponse{}, fmt.Errorf("plugin: identity resolution refused (%s)", code)
	}

	var resp identityResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return identityResponse{}, errors.New("plugin: identity response could not be decoded")
	}

	i.mu.Lock()
	i.cache[key] = resp
	i.at[key] = time.Now()
	i.mu.Unlock()

	return resp, nil
}

type identityKey struct{}

// IdentityFrom returns this plugin's identity resolver. It is present in every
// handler context.
func IdentityFrom(ctx context.Context) (*Identity, bool) {
	i, ok := ctx.Value(identityKey{}).(*Identity)
	return i, ok
}

func withIdentity(ctx context.Context, i *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, i)
}
