package identity_test

import (
	"os"
	"strings"
	"testing"

	"github.com/dreulavelle/azir/internal/identity"
)

// Where the console keeps the human-readable name for each permission.
const labelFile = "../../web/src/Roles.tsx"

/*
Every permission has words a person can read.

The console renders the roles grid from whatever the server sends, and falls
back to the dotted name when it has never heard of a permission. That fallback
is not a failure anyone notices: the row still appears, still has its dots in
the right columns, and reads "data manage" under a group called Other. It had
already happened twice — phone.manage and data.manage were both added here and
never given words — and both times the grid looked fine enough that nobody
looked twice.

So the two lists are compared. Adding a permission without naming it now fails
here instead of quietly shipping.
*/
func TestEveryPermissionIsNamedInTheConsole(t *testing.T) {
	source, err := os.ReadFile(labelFile)
	if err != nil {
		// A Go-only checkout has nothing to check against, which is not a
		// reason to fail somebody's build.
		t.Skipf("no console source to check against: %v", err)
	}

	for _, permission := range identity.AllPermissions {
		if !strings.Contains(string(source), `"`+permission+`"`) {
			t.Errorf("%s has no label in %s, so the roles grid shows it as %q under Other",
				permission, labelFile, strings.ReplaceAll(permission, ".", " "))
		}
	}
}
