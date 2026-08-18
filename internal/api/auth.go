package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
)

// sessionCookie is the browser's session cookie name.
const sessionCookie = "azir_session"

// authenticate resolves the actor for a request, or reports that there is none.
func (s *Server) authenticate(r *http.Request) (identity.Actor, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return identity.Actor{}, identity.ErrUnauthenticated
	}
	return s.DB.ActorForSession(r.Context(), cookie.Value)
}

// require wraps a handler so it runs only for an actor holding a permission.
//
// Enforcement lives here rather than in each handler for the same reason the
// plugin permission guard does: a check that must be remembered is a check that
// will eventually be forgotten, and the forgetting is invisible.
func (s *Server) require(permission string, next func(http.ResponseWriter, *http.Request, identity.Actor)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, err := s.authenticate(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errBody("sign in to continue"))
			return
		}
		if err := actor.Require(permission); err != nil {
			// Refusals are audited: an attempt to reach something out of reach
			// is worth seeing, whether it is an attack or a wrong assumption
			// about who can do what.
			s.Audit.Record(r.Context(), audit.Event{
				ActorUserID: actor.Email,
				Action:      "access.denied",
				Outcome:     audit.OutcomeDenied,
				Detail:      permission,
			})
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":               "your role does not permit this",
				"required_permission": permission,
			})
			return
		}
		next(w, r, actor)
	}
}

// setup creates the first administrator.
//
// Open only while no account exists, so it cannot be used to add a second
// administrator to a running deployment.
//
// Deliberately a local account, even in a deployment that will run entirely on
// single sign-on. Someone has to be able to configure the identity provider
// before the identity provider works, and that same account is what remains
// when the provider is the thing that is broken.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	count, err := s.DB.CountUsers(r.Context())
	if err != nil {
		s.fail(w, err, "could not check setup state")
		return
	}
	if count > 0 {
		writeJSON(w, http.StatusConflict, errBody("setup has already been completed"))
		return
	}

	var body struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	user, err := s.DB.CreateUser(r.Context(), body.Email, body.DisplayName, "admin", body.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: user.Email, Action: "setup.complete", Outcome: audit.OutcomeOK,
	})
	s.issueSession(w, r, user.ID, user.Email)
}

// login exchanges credentials for a session.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	actor, err := s.DB.Authenticate(r.Context(), body.Email, body.Password)
	if err != nil {
		if errors.Is(err, identity.ErrBadCredentials) {
			s.Audit.Record(r.Context(), audit.Event{
				ActorUserID: strings.ToLower(body.Email),
				Action:      "login", Outcome: audit.OutcomeDenied,
			})
			// Deliberately identical whether the account exists or the
			// password was wrong.
			writeJSON(w, http.StatusUnauthorized, errBody("incorrect email or password"))
			return
		}
		s.fail(w, err, "could not sign in")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "login", Outcome: audit.OutcomeOK,
	})
	s.issueSession(w, r, actor.UserID, actor.Email)
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, userID uuid.UUID, email string) {
	token, expires, err := s.DB.CreateSession(r.Context(), userID, r.UserAgent(), clientIP(r))
	if err != nil {
		s.fail(w, err, "could not start a session")
		return
	}
	s.setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusOK, map[string]string{"email": email})
}

// setSessionCookie writes the session cookie. Shared with the single sign-on
// path, which answers with a redirect rather than a body but must land the
// browser in exactly the same signed-in state.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true, // unreadable from JavaScript, so an XSS cannot steal it
		SameSite: http.SameSiteLaxMode,
		// Secure is set when the request arrived over TLS. A self-hosted
		// deployment on plain HTTP inside a LAN would otherwise be unable to
		// log in at all, which is worse than the cookie lacking the flag there.
		Secure: overTLS(r),
	})
}

// logout ends the current session.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = s.DB.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		Expires: time.Unix(0, 0), HttpOnly: true, MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

// whoami returns the current actor, which the frontend uses to decide what to
// show. It is a convenience, not a control: every action is checked server-side
// regardless of what the interface offered.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticate(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errBody("not signed in"))
		return
	}
	writeJSON(w, http.StatusOK, actor)
}

// clientIP prefers the forwarded header when present, since a self-hosted
// deployment usually sits behind something.
func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, ok := strings.Cut(forwarded, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	host, _, ok := strings.Cut(r.RemoteAddr, ":")
	if !ok {
		return r.RemoteAddr
	}
	return host
}
