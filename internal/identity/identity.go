// Package identity answers who is asking and what they may do.
//
// Local accounts exist alongside the intended OIDC path rather than instead of
// it. A self-hosted product needs a way in when the identity provider is the
// thing that is broken, and an administrator who cannot log in cannot fix
// anything.
//
// Permissions are checked by name, never by role. A call site asks whether an
// actor holds "plugin.configure", not whether they are an admin, so adding a
// role later is a row in a table rather than an edit to every check.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

// Permissions. Named constants rather than loose strings so a typo is a
// compile error instead of a silently permissive check.
const (
	PermPluginConfigure  = "plugin.configure"
	PermPluginApprove    = "plugin.approve"
	PermUserManage       = "user.manage"
	PermRoleManage       = "role.manage"
	PermCustomerManage   = "customer.manage"
	PermCredentialManage = "credential.manage"
	PermAuditRead        = "audit.read"
	PermToolRead         = "tool.read"
	PermToolWrite        = "tool.write"
	PermTicketComment    = "ticket.comment"
	PermTicketStatus     = "ticket.status"
	PermTicketAssign     = "ticket.assign"

	// PermPhoneManage is changing a customer's phone system.
	//
	// Separate from PermToolWrite because they are different sizes of mistake:
	// commenting on the wrong ticket is embarrassing, disabling the wrong
	// extension takes a business's phones off the air. An MSP that trusts every
	// technician with the first does not necessarily trust every technician
	// with the second, and a role system that cannot express that forces the
	// looser answer.
	PermPhoneManage = "phone.manage"
)

// AllPermissions is every permission the application defines, for the admin UI
// to render and for validation when a custom role is created.
var AllPermissions = []string{
	PermPluginConfigure, PermPluginApprove, PermUserManage, PermRoleManage,
	PermCustomerManage, PermCredentialManage, PermAuditRead,
	PermToolRead, PermToolWrite,
	PermTicketComment, PermTicketStatus, PermTicketAssign,
	PermPhoneManage,
}

var (
	// ErrUnauthenticated means no valid session was presented.
	ErrUnauthenticated = errors.New("identity: not signed in")
	// ErrForbidden means the actor is known but lacks the permission.
	ErrForbidden = errors.New("identity: not permitted")
	// ErrBadCredentials is deliberately identical for an unknown user and a
	// wrong password, so the response cannot be used to enumerate accounts.
	ErrBadCredentials = errors.New("identity: incorrect email or password")
)

// Actor is an authenticated user and what they may do.
type Actor struct {
	UserID      uuid.UUID `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	Permissions []string  `json:"permissions"`
}

// Can reports whether this actor holds a permission.
func (a Actor) Can(permission string) bool {
	return slices.Contains(a.Permissions, permission)
}

// Require returns ErrForbidden unless the actor holds the permission.
func (a Actor) Require(permission string) error {
	if a.Can(permission) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrForbidden, permission)
}

// --- password hashing -------------------------------------------------------

// Argon2id parameters. Deliberately explicit rather than defaults, and encoded
// into the stored hash so they can be raised later without invalidating
// existing passwords.
const (
	argonTime    = 2
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// HashPassword returns an encoded argon2id hash.
func HashPassword(password string) (string, error) {
	if len(password) < 12 {
		return "", errors.New("identity: password must be at least 12 characters")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s",
		argonTime, argonMemory, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against an encoded hash in constant time.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		return false
	}
	var t, m, p int
	if _, err := fmt.Sscanf(parts[1]+" "+parts[2]+" "+parts[3], "%d %d %d", &t, &m, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// --- session tokens ---------------------------------------------------------

// SessionLifetime is how long a login lasts.
const SessionLifetime = 12 * time.Hour

// NewSessionToken returns a token and the hash to store.
//
// Only the hash is persisted, so a copy of the database does not hand someone a
// working login — the same reason a password is never stored either.
func NewSessionToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken hashes a session token for storage and lookup.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// --- context ----------------------------------------------------------------

type actorKey struct{}

// WithActor attaches an authenticated actor to a context.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// FromContext returns the authenticated actor, if any.
func FromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}
