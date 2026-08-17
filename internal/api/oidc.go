package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/oidc"
	"github.com/dreulavelle/azir/internal/store"
)

// oidcProviderName is what a federated account is stamped with. One value for
// any OIDC provider: the deployment has one, and recording which vendor it was
// would only matter if it could have been more than one.
const oidcProviderName = "oidc"

// callbackPath is where the provider sends the browser back to. It must match
// the redirect URI registered with the provider exactly.
const callbackPath = "/api/auth/oidc/callback"

// authState is what the sign-in screen is allowed to know before anyone has
// signed in: whether to offer the button, and what to call it.
func (s *Server) authState(w http.ResponseWriter, r *http.Request) {
	count, err := s.DB.CountUsers(r.Context())
	if err != nil {
		s.fail(w, err, "could not check setup state")
		return
	}
	cfg, err := s.DB.AuthConfig(r.Context())
	if err != nil {
		s.fail(w, err, "could not read the sign-in settings")
		return
	}

	// Configured but not yet usable is the same as off, as far as the sign-in
	// screen is concerned: a button that cannot work is worse than no button.
	ready := cfg.Enabled && cfg.Issuer != "" && cfg.ClientID != ""

	writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup":  count == 0,
		"oidc_enabled": ready,
		"oidc_label":   ssoLabel(cfg.Issuer),
	})
}

// ssoLabel names the button after the provider it goes to.
func ssoLabel(issuer string) string {
	if strings.Contains(issuer, "login.microsoftonline.com") {
		return "Microsoft"
	}
	if host, err := url.Parse(issuer); err == nil && host.Host != "" {
		return host.Host
	}
	return "single sign-on"
}

// provider builds the configured provider, resolving the client secret from
// the vault at the moment it is needed rather than holding it in memory.
func (s *Server) provider(ctx context.Context, r *http.Request) (*oidc.Provider, error) {
	cfg, err := s.DB.AuthConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("single sign-on is not enabled")
	}

	secret, err := s.Creds.Open(ctx, nil, store.AuthPlugin, store.AuthSecretKind)
	if err != nil {
		return nil, errors.New("no client secret is stored for the identity provider")
	}

	return s.OIDC.Get(ctx, cfg, string(secret), s.redirectURL(r, cfg))
}

// redirectURL is where the provider sends the browser back to.
//
// Configured explicitly when set, because a deployment behind a reverse proxy
// cannot see its own public address, and a redirect URI that does not match the
// registered one exactly is refused by the provider.
func (s *Server) redirectURL(r *http.Request, cfg store.AuthConfig) string {
	if cfg.RedirectURL != "" {
		return cfg.RedirectURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host + callbackPath
}

// oidcStart sends the browser to the identity provider.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	p, err := s.provider(r.Context(), r)
	if err != nil {
		s.signInFailed(w, r, "", "could not start sign-in", err)
		return
	}

	authURL, state, err := p.Start()
	if err != nil {
		s.signInFailed(w, r, "", "could not start sign-in", err)
		return
	}
	if err := s.DB.StartOIDCState(r.Context(), state); err != nil {
		s.signInFailed(w, r, "", "could not start sign-in", err)
		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

// oidcCallback completes a sign-in.
//
// This handler is reached by a browser being redirected, so it answers with a
// redirect rather than JSON: on success to the console, on failure to the
// sign-in screen carrying a reason someone can act on.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// The provider reports its own refusals here — consent declined, account
	// disabled upstream — and they are not this deployment's failures.
	if reason := query.Get("error"); reason != "" {
		detail := query.Get("error_description")
		if detail == "" {
			detail = reason
		}
		s.signInFailed(w, r, "", detail, nil)
		return
	}

	state, err := s.DB.ConsumeOIDCState(r.Context(), query.Get("state"))
	if err != nil {
		// Also the path a replayed callback takes: the row is gone after the
		// first use, so the second attempt is indistinguishable from a forged
		// one, and both are refused.
		s.signInFailed(w, r, "", "this sign-in link has expired or was already used", err)
		return
	}

	p, err := s.provider(r.Context(), r)
	if err != nil {
		s.signInFailed(w, r, "", "single sign-on is not available", err)
		return
	}

	who, err := p.Exchange(r.Context(), query.Get("code"), state)
	if err != nil {
		s.signInFailed(w, r, "", "the identity provider could not verify this sign-in", err)
		return
	}

	if !p.PermittedDomain(who.Email) {
		s.signInFailed(w, r, who.Email, "this email domain is not allowed to sign in", nil)
		return
	}

	cfg := p.Config()
	user, err := s.DB.LinkOrCreateFederatedUser(r.Context(), store.FederatedUser{
		Provider:    oidcProviderName,
		ExternalID:  who.Subject,
		Email:       who.Email,
		DisplayName: who.DisplayName,
	}, cfg.AutoProvision, cfg.DefaultRole)
	if err != nil {
		if errors.Is(err, store.ErrNotInvited) {
			s.signInFailed(w, r, who.Email,
				"this account has not been added to Azir; an administrator can add it", nil)
			return
		}
		s.signInFailed(w, r, who.Email, "could not complete sign-in", err)
		return
	}
	if user.Disabled {
		s.signInFailed(w, r, who.Email, "this account is disabled", nil)
		return
	}

	token, expires, err := s.DB.CreateSession(r.Context(), user.ID, r.UserAgent(), clientIP(r))
	if err != nil {
		s.signInFailed(w, r, who.Email, "could not start a session", err)
		return
	}
	s.setSessionCookie(w, r, token, expires)

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: user.Email, Action: "login.sso", Outcome: audit.OutcomeOK,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// signInFailed records the attempt and returns the browser to the sign-in
// screen with something a person can act on.
//
// The message is deliberately about what to do next rather than about what
// broke: the underlying error goes to the log and the audit trail, where an
// administrator can see it, and not into a URL.
func (s *Server) signInFailed(w http.ResponseWriter, r *http.Request, who, message string, err error) {
	if err != nil {
		s.Log.Warn("sso sign-in failed", "error", err, "email", who)
	}
	actor := who
	if actor == "" {
		actor = "unknown"
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor, Action: "login.sso",
		Outcome: audit.OutcomeDenied, Detail: message,
	})
	http.Redirect(w, r, "/?sso_error="+url.QueryEscape(message), http.StatusFound)
}

// --- administration ----------------------------------------------------------

// authSettingsView is the configuration as an administrator sees it. The client
// secret is reported as present or absent and never returned.
type authSettingsView struct {
	store.AuthConfig
	ClientSecretSet bool   `json:"client_secret_set"`
	CallbackURL     string `json:"callback_url"`
}

func (s *Server) getAuthSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.DB.AuthConfig(r.Context())
	if err != nil {
		s.fail(w, err, "could not read the sign-in settings")
		return
	}

	view := authSettingsView{AuthConfig: cfg, CallbackURL: s.redirectURL(r, cfg)}
	if refs, err := s.Creds.List(r.Context()); err == nil {
		for _, ref := range refs {
			if ref.Plugin == store.AuthPlugin && ref.Kind == store.AuthSecretKind {
				view.ClientSecretSet = true
			}
		}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) putAuthSettings(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body struct {
		store.AuthConfig
		// Empty means "leave the stored one alone", matching how every other
		// secret field in this console behaves.
		ClientSecret string `json:"client_secret"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	cfg := body.AuthConfig
	cfg.TenantID = strings.TrimSpace(cfg.TenantID)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.Issuer = strings.TrimSpace(cfg.Issuer)

	// A tenant with no issuer is the ordinary Entra case, so derive it rather
	// than asking an administrator to paste a URL they would have to look up.
	if cfg.Issuer == "" && cfg.TenantID != "" {
		cfg.Issuer = oidc.EntraIssuer(cfg.TenantID)
	}
	if cfg.DefaultRole == "" {
		cfg.DefaultRole = "viewer"
	}
	for i, d := range cfg.AllowedDomains {
		cfg.AllowedDomains[i] = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "@")
	}

	if secret := strings.TrimSpace(body.ClientSecret); secret != "" {
		if _, err := s.Creds.Put(r.Context(), nil, store.AuthPlugin, store.AuthSecretKind, []byte(secret)); err != nil {
			s.fail(w, err, "could not store the client secret")
			return
		}
	}

	// Enabling is where a mistake locks people out or lets the wrong people
	// in, so the configuration has to actually work before it can be switched
	// on: discovery must succeed and a secret must be present.
	if cfg.Enabled {
		secret, err := s.Creds.Open(r.Context(), nil, store.AuthPlugin, store.AuthSecretKind)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(
				"a client secret is required before single sign-on can be enabled"))
			return
		}
		s.OIDC.Forget()
		if _, err := oidc.Discover(r.Context(), cfg, string(secret), s.redirectURL(r, cfg)); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
			return
		}
	}

	if err := s.DB.SetAuthConfig(r.Context(), cfg, actor.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	s.OIDC.Forget()

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "auth.configure",
		Outcome: audit.OutcomeOK, Detail: enabledWord(cfg.Enabled),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": cfg.Enabled})
}

func enabledWord(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
