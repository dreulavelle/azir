package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dreulavelle/azir/internal/store"
)

// A callback that arrives twice must succeed at most once, or a leaked
// authorization code is worth as much as a session.
func TestASignInAttemptCanOnlyBeUsedOnce(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	attempt := store.OIDCState{State: "state-value", Nonce: "nonce-value", Verifier: "verifier-value"}
	if err := db.StartOIDCState(ctx, attempt); err != nil {
		t.Fatal(err)
	}

	got, err := db.ConsumeOIDCState(ctx, attempt.State)
	if err != nil {
		t.Fatalf("the attempt could not be consumed: %v", err)
	}
	if got.Nonce != attempt.Nonce || got.Verifier != attempt.Verifier {
		t.Errorf("the attempt came back changed: %+v", got)
	}

	if _, err := db.ConsumeOIDCState(ctx, attempt.State); !errors.Is(err, store.ErrNoSuchState) {
		t.Errorf("a replayed callback was accepted, or refused wrongly: %v", err)
	}
}

func TestAnUnknownSignInAttemptIsRefused(t *testing.T) {
	db := testDB(t)
	if _, err := db.ConsumeOIDCState(context.Background(), "never-started"); !errors.Is(err, store.ErrNoSuchState) {
		t.Errorf("a forged state was not refused: %v", err)
	}
}

// Authenticating against a directory is not the same as being invited into
// this deployment.
func TestAnUnknownPersonIsRefusedUnlessProvisioningIsOn(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	who := store.FederatedUser{
		Provider: "oidc", ExternalID: "object-id-1",
		Email: "stranger@cooli.ai", DisplayName: "A Stranger",
	}

	if _, err := db.LinkOrCreateFederatedUser(ctx, who, false, "viewer"); !errors.Is(err, store.ErrNotInvited) {
		t.Fatalf("an uninvited person was let in, or refused wrongly: %v", err)
	}

	created, err := db.LinkOrCreateFederatedUser(ctx, who, true, "viewer")
	if err != nil {
		t.Fatalf("provisioning was on but the account was not created: %v", err)
	}
	if created.Role != "viewer" {
		t.Errorf("new account has role %q, want the configured default", created.Role)
	}
	if created.Provider == nil || *created.Provider != "oidc" {
		t.Error("the account was not stamped with the provider it came from")
	}
}

// An account created before the provider was configured must be linked rather
// than duplicated, or the same person ends up with two rows and one of them
// has the permissions.
func TestAnExistingLocalAccountIsLinkedNotDuplicated(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	local, err := db.CreateUser(ctx, "Tech@Cooli.ai", "Tech", "technician", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}

	linked, err := db.LinkOrCreateFederatedUser(ctx, store.FederatedUser{
		Provider: "oidc", ExternalID: "object-id-2",
		// Different case, as a directory may well report it.
		Email: "tech@cooli.ai", DisplayName: "Tech Person",
	}, false, "viewer")
	if err != nil {
		t.Fatalf("an existing account could not sign in through the provider: %v", err)
	}
	if linked.ID != local.ID {
		t.Fatal("a second account was created for someone who already had one")
	}

	// The role stays where it was. The provider says who someone is; it does
	// not get an opinion about what they may do.
	if linked.Role != "technician" {
		t.Errorf("role became %q; signing in through the provider changed it", linked.Role)
	}

	users, err := db.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Errorf("there are %d accounts for one person", len(users))
	}
}

// Once linked, the stable identifier is what matches — an address can change.
func TestARenamedPersonIsStillTheSameAccount(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	first, err := db.LinkOrCreateFederatedUser(ctx, store.FederatedUser{
		Provider: "oidc", ExternalID: "object-id-3",
		Email: "before@cooli.ai", DisplayName: "Before",
	}, true, "technician")
	if err != nil {
		t.Fatal(err)
	}

	after, err := db.LinkOrCreateFederatedUser(ctx, store.FederatedUser{
		Provider: "oidc", ExternalID: "object-id-3",
		Email: "after@cooli.ai", DisplayName: "After",
	}, false, "viewer")
	if err != nil {
		t.Fatalf("a renamed person could not sign in: %v", err)
	}
	if after.ID != first.ID {
		t.Fatal("a rename produced a second account")
	}
	if after.Email != "after@cooli.ai" || after.DisplayName != "After" {
		t.Errorf("the directory's current values were not adopted: %+v", after)
	}
	if after.Role != "technician" {
		t.Errorf("role became %q; the default role was applied to an existing account", after.Role)
	}
}

func TestAuthConfigRoundTrips(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// Off, with nothing configured, is what a fresh deployment must look like.
	initial, err := db.AuthConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Enabled {
		t.Error("single sign-on is enabled on a fresh deployment")
	}
	if initial.AutoProvision {
		t.Error("accounts are auto-provisioned on a fresh deployment")
	}

	want := store.AuthConfig{
		Enabled: true, Issuer: "https://login.microsoftonline.com/tenant/v2.0",
		ClientID: "client", TenantID: "tenant",
		AllowedDomains: []string{"cooli.ai"}, AutoProvision: true,
		DefaultRole: "technician", RedirectURL: "https://azir.cooli.ai/api/auth/oidc/callback",
	}
	if err := db.SetAuthConfig(ctx, want, "admin@cooli.ai"); err != nil {
		t.Fatal(err)
	}

	got, err := db.AuthConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Issuer != want.Issuer || got.ClientID != want.ClientID ||
		got.TenantID != want.TenantID || got.DefaultRole != want.DefaultRole ||
		!got.Enabled || !got.AutoProvision || got.RedirectURL != want.RedirectURL {
		t.Errorf("configuration did not round-trip: %+v", got)
	}
	if len(got.AllowedDomains) != 1 || got.AllowedDomains[0] != "cooli.ai" {
		t.Errorf("allowed domains did not round-trip: %v", got.AllowedDomains)
	}
	if got.UpdatedBy != "admin@cooli.ai" {
		t.Errorf("updated_by is %q", got.UpdatedBy)
	}

	// A role that does not exist would leave provisioning pointing at nothing.
	bad := want
	bad.DefaultRole = "no-such-role"
	if err := db.SetAuthConfig(ctx, bad, "admin@cooli.ai"); err == nil {
		t.Error("a default role that does not exist was accepted")
	}
}
