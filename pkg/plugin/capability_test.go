package plugin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

/*
Every capability that exists is a capability that is allowed.

The names and the allowed set are two lists, and a name missing from the second
takes the whole plugin down at startup with "unknown capability". That is a
good failure — loud, immediate, impossible to miss — but it happens on a
running deployment rather than here, and finding out from a container restart
loop is a worse afternoon than finding out from a test.

This is the fifth pair of lists in this codebase that had to agree with nothing
checking. The others were permission labels, audit action names, the fields a
plugin publishes, and the sheet's own columns.
*/
func TestEveryCapabilityIsAllowed(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "capability.go", nil, 0)
	if err != nil {
		t.Fatalf("reading the capability list: %v", err)
	}

	declared := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "Cap") || i >= len(spec.Values) {
				continue
			}
			lit, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			declared[name.Name] = strings.Trim(lit.Value, `"`)
		}
		return true
	})
	if len(declared) == 0 {
		t.Fatal("found no capabilities at all, so this test checks nothing")
	}

	for name, value := range declared {
		if _, allowed := vocabulary[Capability(value)]; !allowed {
			t.Errorf("%s (%q) is declared but not in the allowed set, so any plugin providing it "+
				"refuses to start", name, value)
		}
	}
}
