package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestUIStaysAClient keeps the TUI a thin client of the hub: it reads
// snapshots and sends commands through hub.Backend, and never drives the
// dispatcher, the fleet coordinator, or GitHub itself. That boundary is
// what lets the Mac and Android apps reach the same hub over the network.
//
// If this fails, add the operation to hub.Backend (and hubtest.Fake)
// instead of calling it here.
func TestUIStaysAClient(t *testing.T) {
	const mod = "github.com/jflowers/gh-hud/internal/"
	// Internal packages the UI may import. dispatch, fleet and model are
	// here for their data types and pure helpers only; forbidden below
	// is everything that does work.
	allowed := map[string]bool{
		mod + "clipboard": true, // local to the viewer's machine
		mod + "config":    true,
		mod + "dispatch":  true,
		mod + "fleet":     true,
		mod + "hub":       true,
		mod + "model":     true,
	}
	forbidden := map[string]map[string]string{
		"dispatch": {"Dispatcher": "use hub.Backend", "New": "the hub owns the dispatcher"},
		"fleet":    {"Coordinator": "use hub.Backend", "New": "the hub owns the fleet"},
		"model":    {"FetchAll": "the hub fetches; read Snapshot.Res"},
		"hub":      {"Hub": "depend on hub.Backend", "New": "main builds the hub"},
	}
	stdForbidden := map[string]string{
		"os/exec": "run commands in the hub (or clipboard, for viewer-local actions)",
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		local := map[string]string{} // import name -> last path element
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if why, bad := stdForbidden[path]; bad {
				t.Errorf("%s imports %s: %s", fset.Position(imp.Pos()), path, why)
			}
			if !strings.HasPrefix(path, mod) {
				continue
			}
			if !allowed[path] {
				t.Errorf("%s imports %s: the UI may only import %v", fset.Position(imp.Pos()), path, keys(allowed))
			}
			base := path[strings.LastIndex(path, "/")+1:]
			n := base
			if imp.Name != nil {
				n = imp.Name.Name
			}
			local[n] = base
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if why, bad := forbidden[local[pkg.Name]][sel.Sel.Name]; bad {
				t.Errorf("%s uses %s.%s: %s", fset.Position(sel.Pos()), pkg.Name, sel.Sel.Name, why)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no UI source files found")
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, strings.TrimPrefix(k, "github.com/jflowers/gh-hud/internal/"))
	}
	slices.Sort(out)
	return out
}
