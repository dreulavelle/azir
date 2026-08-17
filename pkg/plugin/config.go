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

// ConfigSubjectPrefix is where core answers plugin configuration requests. Like
// the vault subjects it sits outside azir.tool.*, so it is never discovered as
// a model-facing capability, and it is per-plugin for the same authorisation
// reason.
const ConfigSubjectPrefix = "azir.config.resolve"

// ConfigSubject returns the settings subject for a plugin.
func ConfigSubject(pluginName string) string {
	return ConfigSubjectPrefix + "." + pluginName
}

// ConfigChangedPrefix is where core announces that an administrator changed a
// plugin's settings.
//
// Without it, a plugin keeps serving from its cache for the length of the TTL
// after someone presses save — so the administrator who has just typed the
// right credential is told the plugin is not configured, and reasonably
// concludes they typed it wrong. Correct after thirty seconds is the wrong
// answer at the only moment anybody is watching.
const ConfigChangedPrefix = "azir.config.changed"

// ConfigChangedSubject returns the announcement subject for a plugin.
func ConfigChangedSubject(pluginName string) string {
	return ConfigChangedPrefix + "." + pluginName
}

// SecretMarker flags a property in a plugin's ConfigSchema as credential
// material. Marked fields are stored in the vault rather than the config
// table, are never returned by the settings API, and never appear in a
// model-facing tool schema.
//
// It is an `x-` extension so the schema stays a valid JSON Schema and the
// admin console can render it with an ordinary form generator.
const SecretMarker = "x-azir-secret"

// ErrNotConfigured means the administrator has not filled in this setting.
var ErrNotConfigured = errors.New("plugin: setting is not configured")

type configRequest struct {
	Plugin     string `json:"plugin"`
	CustomerID string `json:"customer_id,omitempty"`
}

type configResponse struct {
	Values map[string]any `json:"values"`
}

// Config reads a plugin's non-secret settings, as entered by an administrator
// in the web console. Secret settings come from [Vault] instead.
//
// Settings are resolved at runtime rather than baked into the image, so a
// plugin needs no environment variables, no config file, and no redeploy when
// an administrator changes something.
type Config struct {
	nc     *nats.Conn
	plugin string
	ttl    time.Duration

	mu    sync.RWMutex
	cache map[string]cachedConfig
}

type cachedConfig struct {
	values  map[string]any
	expires time.Time
}

func newConfig(nc *nats.Conn, pluginName string) *Config {
	return &Config{
		nc:     nc,
		plugin: pluginName,
		ttl:    30 * time.Second,
		cache:  map[string]cachedConfig{},
	}
}

// All returns every non-secret setting for a scope. Pass an empty customerID
// for the plugin's deployment-wide settings.
func (c *Config) All(ctx context.Context, customerID string) (map[string]any, error) {
	c.mu.RLock()
	if hit, ok := c.cache[customerID]; ok && time.Now().Before(hit.expires) {
		c.mu.RUnlock()
		return hit.values, nil
	}
	c.mu.RUnlock()

	payload, err := json.Marshal(configRequest{Plugin: c.plugin, CustomerID: customerID})
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	msg, err := c.nc.RequestWithContext(reqCtx, ConfigSubject(c.plugin), payload)
	if err != nil {
		return nil, fmt.Errorf("plugin: config unreachable: %w", err)
	}
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		return nil, fmt.Errorf("plugin: config refused (%s)", code)
	}

	var resp configResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, errors.New("plugin: config response could not be decoded")
	}
	if resp.Values == nil {
		resp.Values = map[string]any{}
	}

	c.mu.Lock()
	c.cache[customerID] = cachedConfig{values: resp.Values, expires: time.Now().Add(c.ttl)}
	c.mu.Unlock()

	return resp.Values, nil
}

// String reads one setting as text, falling back to the deployment-wide value
// when a customer-scoped one is absent.
func (c *Config) String(ctx context.Context, customerID, key string) (string, error) {
	if customerID != "" {
		values, err := c.All(ctx, customerID)
		if err != nil {
			return "", err
		}
		if s, ok := values[key].(string); ok && s != "" {
			return s, nil
		}
	}
	values, err := c.All(ctx, "")
	if err != nil {
		return "", err
	}
	s, ok := values[key].(string)
	if !ok || s == "" {
		return "", fmt.Errorf("%w: %s", ErrNotConfigured, key)
	}
	return s, nil
}

// Invalidate drops cached settings so the next read sees an administrator's
// change immediately.
func (c *Config) Invalidate() {
	c.mu.Lock()
	c.cache = map[string]cachedConfig{}
	c.mu.Unlock()
}

type configKey struct{}

// ConfigFrom returns this plugin's settings reader. It is present in every
// handler context.
func ConfigFrom(ctx context.Context) (*Config, bool) {
	c, ok := ctx.Value(configKey{}).(*Config)
	return c, ok
}

func withConfig(ctx context.Context, c *Config) context.Context {
	return context.WithValue(ctx, configKey{}, c)
}
