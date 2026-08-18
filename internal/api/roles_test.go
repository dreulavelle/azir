package api_test

import (
	"net/http"
	"testing"
)

// roleNamed finds one role in the list the console reads.
func roleNamed(t *testing.T, c *http.Client, url, name string) map[string]any {
	t.Helper()
	out := do(t, c, http.MethodGet, url+"/api/roles", nil, http.StatusOK)
	list, _ := out["roles"].([]any)
	for _, entry := range list {
		row, ok := entry.(map[string]any)
		if ok && row["name"] == name {
			return row
		}
	}
	return nil
}

/*
A role with its own mix of permissions is a setting, not a rebuild.

Everything in Azir checks the permission rather than the name, which was always
true and until now bought nothing, because there were three roles and no way to
make a fourth. The case it exists for is the one phone.manage was split out
for: a senior technician trusted to change a customer's phone system without
being handed the credential vault.
*/
func TestARoleCanBeMadeWithItsOwnMix(t *testing.T) {
	srv, client := server(t)

	made := do(t, client, http.MethodPost, srv.URL+"/api/roles", map[string]any{
		"name":        "Senior Tech",
		"description": "Everyday work, plus phones",
		"permissions": []string{"tool.read", "ticket.comment", "phone.manage"},
	}, http.StatusOK)

	// Typed with a capital and a space; stored as an identifier.
	if made["name"] != "senior-tech" {
		t.Errorf("stored as %q, wanted senior-tech", made["name"])
	}

	got := roleNamed(t, client, srv.URL, "senior-tech")
	if got == nil {
		t.Fatal("the new role is not in the list")
	}
	if got["builtin"] != false {
		t.Error("a role somebody made is not built in")
	}

	var held []string
	for _, p := range got["permissions"].([]any) {
		held = append(held, p.(string))
	}
	if len(held) != 3 {
		t.Fatalf("kept %v", held)
	}
	// The vault is the thing this role exists in order not to have.
	for _, p := range held {
		if p == "credential.manage" {
			t.Error("granted a permission that was never asked for")
		}
	}
}

// A permission the server has never heard of would sit in the row looking
// granted and be checked by nothing, so it is dropped rather than stored.
func TestARoleCannotHoldAPermissionThatDoesNotExist(t *testing.T) {
	srv, client := server(t)

	made := do(t, client, http.MethodPost, srv.URL+"/api/roles", map[string]any{
		"name":        "invented",
		"permissions": []string{"tool.read", "everything.always", ""},
	}, http.StatusOK)

	for _, p := range made["permissions"].([]any) {
		if p.(string) != "tool.read" {
			t.Errorf("kept %q, which nothing checks", p)
		}
	}
}

/*
The admin role is the way back in.

A console that lets somebody edit their own way out of administering it is a
console that eventually will, so admin is refused on both verbs — and refused
by the server, not merely left without a button.
*/
func TestTheAdminRoleCannotBeChangedOrRemoved(t *testing.T) {
	srv, client := server(t)

	do(t, client, http.MethodPatch, srv.URL+"/api/roles/admin",
		map[string]any{"permissions": []string{"tool.read"}}, http.StatusForbidden)
	do(t, client, http.MethodDelete, srv.URL+"/api/roles/admin", nil, http.StatusForbidden)

	admin := roleNamed(t, client, srv.URL, "admin")
	if admin == nil {
		t.Fatal("admin is gone")
	}
	if len(admin["permissions"].([]any)) < 2 {
		t.Error("admin was narrowed anyway")
	}
}

// Accounts point at a role, and so does the setting that says what single
// sign-on hands new arrivals. Taking it out from under either is how somebody
// ends up with an account that cannot be resolved at all.
func TestARoleInUseCannotBeRemoved(t *testing.T) {
	srv, client := server(t)

	do(t, client, http.MethodPost, srv.URL+"/api/roles", map[string]any{
		"name":        "temporary",
		"permissions": []string{"tool.read"},
	}, http.StatusOK)

	// Nothing uses it, so it goes.
	do(t, client, http.MethodDelete, srv.URL+"/api/roles/temporary", nil, http.StatusOK)
	if roleNamed(t, client, srv.URL, "temporary") != nil {
		t.Fatal("it is still there")
	}

	// Make it again and give it to somebody.
	do(t, client, http.MethodPost, srv.URL+"/api/roles", map[string]any{
		"name":        "temporary",
		"permissions": []string{"tool.read"},
	}, http.StatusOK)
	do(t, client, http.MethodPost, srv.URL+"/api/users", map[string]any{
		"email":    "held@azir.local",
		"role":     "temporary",
		"password": "a-long-enough-password",
	}, http.StatusCreated)

	do(t, client, http.MethodDelete, srv.URL+"/api/roles/temporary", nil, http.StatusConflict)
	if roleNamed(t, client, srv.URL, "temporary") == nil {
		t.Error("removed a role an account was still using")
	}
}

// The other two that ship are yours to shape — only admin is fixed.
func TestTheOtherBuiltInRolesCanBeChanged(t *testing.T) {
	srv, client := server(t)

	do(t, client, http.MethodPatch, srv.URL+"/api/roles/viewer", map[string]any{
		"description": "Read-only, and can see the activity log",
		"permissions": []string{"tool.read", "audit.read"},
	}, http.StatusOK)

	viewer := roleNamed(t, client, srv.URL, "viewer")
	var held []string
	for _, p := range viewer["permissions"].([]any) {
		held = append(held, p.(string))
	}
	if len(held) != 2 {
		t.Errorf("viewer holds %v", held)
	}
}

/*
role.manage is not a way to become an administrator on your own.

Hold it, add credential.manage to the role you are already in, and the vault
opens on the next request — which is what an audit of this found it doing. It
is inherent to letting anybody edit roles at all; what is not inherent is
letting somebody do it to their own without another person involved.

Somebody else with the permission can still change this role, and an
administrator always can. Only the loop back to yourself is closed.
*/
func TestARoleCannotWidenItself(t *testing.T) {
	srv, client := server(t)

	do(t, client, http.MethodPost, srv.URL+"/api/roles", map[string]any{
		"name":        "role-keeper",
		"permissions": []string{"tool.read", "role.manage"},
	}, http.StatusOK)
	do(t, client, http.MethodPost, srv.URL+"/api/users", map[string]any{
		"email":    "keeper@azir.local",
		"role":     "role-keeper",
		"password": "a-long-enough-password",
	}, http.StatusCreated)

	keeper := signInAs(t, srv.URL, "keeper@azir.local", "a-long-enough-password")

	// Their own is refused...
	do(t, keeper, http.MethodPatch, srv.URL+"/api/roles/role-keeper", map[string]any{
		"permissions": []string{"tool.read", "role.manage", "credential.manage"},
	}, http.StatusForbidden)

	// ...and refusing it is what keeps the vault shut.
	do(t, keeper, http.MethodGet, srv.URL+"/api/credentials", nil, http.StatusForbidden)

	// Another role is still theirs to change, which is the permission working.
	do(t, keeper, http.MethodPatch, srv.URL+"/api/roles/viewer", map[string]any{
		"permissions": []string{"tool.read", "audit.read"},
	}, http.StatusOK)

	// And the administrator who granted it can still change it.
	do(t, client, http.MethodPatch, srv.URL+"/api/roles/role-keeper", map[string]any{
		"permissions": []string{"tool.read"},
	}, http.StatusOK)
}
