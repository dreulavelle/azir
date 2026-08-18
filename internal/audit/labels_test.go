package audit_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Where the two lists live: the actions the server records, and the words the
// console shows for them.
const (
	labelFile = "../../web/src/Audit.tsx"
	sourceDir = "../.."
)

var (
	recorded = regexp.MustCompile(`Action:\s+"([a-z][a-z.]*)"`)
	named    = regexp.MustCompile(`(?m)^\s+"?([a-z][a-z._]*)"?:\s*\{`)
)

/*
Every recorded action reads as something a person did.

The activity log is written by the parts of Azir that record things, so its raw
vocabulary is theirs — tool.invoke, credential.resolve — and the console
translates. When a translation is missing the row still appears, still has its
time and its actor, and says "Upload snapshot" or, worse, "Retention data".
Nothing looks broken; the screen just reads like a developer's log, which is
the complaint that prompted this.

Twenty-three of thirty-nine actions were in that state. Comparing the lists
costs nothing and is the only thing that keeps them together, since one is Go
and the other TypeScript and neither can see the other.
*/
func TestEveryRecordedActionHasWords(t *testing.T) {
	source, err := os.ReadFile(labelFile)
	if err != nil {
		// A Go-only checkout has nothing to check against, which is not a
		// reason to fail somebody's build.
		t.Skipf("no console source to check against: %v", err)
	}

	words := map[string]bool{}
	for _, m := range named.FindAllStringSubmatch(string(source), -1) {
		words[m[1]] = true
	}

	for action := range recordedActions(t) {
		if !words[action] {
			t.Errorf("%s is recorded but has no words in %s, so the log shows it as %q",
				action, labelFile, strings.ReplaceAll(action, ".", " "))
		}
	}
}

// recordedActions walks the Go source for every action name handed to the
// recorder.
func recordedActions(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}

	for _, dir := range []string{"internal", "cmd"} {
		root := sourceDir + "/" + dir
		err := walkGo(root, func(path string, body []byte) {
			// Test files invent actions that no deployment ever writes.
			if strings.HasSuffix(path, "_test.go") {
				return
			}
			for _, m := range recorded.FindAllStringSubmatch(string(body), -1) {
				found[m[1]] = true
			}
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	if len(found) == 0 {
		t.Fatal("found no recorded actions at all, which means this test checks nothing")
	}
	return found
}

func walkGo(root string, visit func(path string, body []byte)) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := root + "/" + e.Name()
		if e.IsDir() {
			if err := walkGo(path, visit); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		visit(path, body)
	}
	return nil
}
