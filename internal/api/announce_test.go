package api_test

import (
	"go/ast"
	"os"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

/*
Anything that changes what a plugin holds has to say so.

A plugin keeps a resolved credential for five minutes and a customer's settings
for thirty seconds. Core announces on azir.config.changed.<plugin> and the
plugin drops both. Saving settings always did that. Four other paths did not,
and each was found the same way — by somebody removing a thing, being told it
was removed, and watching it keep working:

  - disconnecting a system from a customer
  - replacing a credential
  - deleting a credential
  - resetting the whole deployment

They share a shape rather than a cause. Every one is a handler that deletes or
replaces a row a plugin caches, answers 200, and tells nobody. Nothing about
the code makes the omission visible: the handler is correct in isolation, the
test suite passes, and the failure only appears if you go and use the thing
afterwards within the cache window.

So this is the check instead of the habit. It reads the handlers, finds the
ones that write credentials or plugin settings, and requires each to announce —
or to be named below as a deliberate exception, with the reason written down.

It is a text match over the source, not a type-checked call graph, which makes
it approximate in one direction only: it can ask for an announcement that a
handler reaches indirectly. Being told to add a line you already have is a
cheap failure. Missing one is the expensive one, and that is the direction this
errs against.
*/

// Calls that change something a plugin has cached.
var mutatingCalls = []string{
	"s.Creds.Put(",
	"s.Creds.Delete(",
	"s.DB.SetPluginConfig(",
	"s.DB.Disconnect(",
	"s.DB.ResetAll(",
	"s.DB.SetWritesEnabled(",
}

// How a handler discharges the obligation.
var announcements = []string{
	"s.announceConfigChange(",
	"s.announceEveryPlugin(",
}

/*
Handlers that change one of those rows and deliberately stay quiet.

Each needs a reason, because "it seemed fine" is how the four above happened.
*/
var silentOnPurpose = map[string]string{
	"rotateCredentials": "re-wraps data keys and never touches plaintext, so " +
		"every cached value is still correct; announcing would cost each plugin " +
		"a round of vendor re-authentication to change nothing",
	"clearWork": "removes conversations, captures and cached answers, and no " +
		"credential or setting a plugin holds",
	"putAssistantSettings": "the assistant is not a plugin and nothing " +
		"subscribes on its behalf; its key is read from the vault on each use, " +
		"so a change is already live on the next message",
	"putAuthSettings": "the identity provider is not a plugin either, and this " +
		"drops the cache that does hold it by calling s.OIDC.Forget — a " +
		"different mechanism for the same obligation",
}

func TestEveryHandlerThatChangesAPluginSaysSo(t *testing.T) {
	var checked int
	var missing []string

	eachHandler(t, func(path, name, body string) {
		if !containsAny(body, mutatingCalls) {
			return
		}
		checked++
		if _, allowed := silentOnPurpose[name]; allowed {
			return
		}
		if containsAny(body, announcements) {
			return
		}
		missing = append(missing,
			name+" ("+path+") changes a credential or a plugin setting "+
				"without announcing it, so the plugin keeps the old value")
	})

	if checked == 0 {
		t.Fatal("found no handler that writes a credential or a setting, so this test checks nothing")
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Error(m)
	}
}

// Every named exception must still exist, or the reason is guarding nothing and
// the next person inherits a list they cannot trust.
func TestSilentOnPurposeStillExists(t *testing.T) {
	found := map[string]bool{}
	eachHandler(t, func(_, name, _ string) { found[name] = true })
	for name := range silentOnPurpose {
		if !found[name] {
			t.Errorf("%s is listed as deliberately silent but no longer exists", name)
		}
	}
}

/*
eachHandler visits every function declared in this package's non-test files.

Files are parsed one at a time rather than through parser.ParseDir, which is
deprecated for not understanding build tags. Nothing here has build tags, but
reaching for the deprecated call would be the sort of thing this repository
would rather not leave lying around for the next person to copy.
*/
func eachHandler(t *testing.T, visit func(path, name, body string)) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the api package: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, e.Name(), body, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			from := fset.Position(fn.Pos()).Offset
			to := fset.Position(fn.End()).Offset
			if to > len(body) {
				to = len(body)
			}
			visit(e.Name(), fn.Name.Name, string(body[from:to]))
		}
	}
}

func containsAny(body string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(body, n) {
			return true
		}
	}
	return false
}
