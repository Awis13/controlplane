package ipalloc_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The advisory lock lives inside ipalloc.Allocate, so a store that allocates
// any other way is unlocked no matter how correct its arithmetic is. That is
// exactly the bug this package was extracted to remove from the WireGuard
// store, and it is invisible to every other test here: a second hand-rolled
// allocator would compile, pass, and quietly race.
//
// This walks the two stores that allocate addresses and checks each one both
// calls Allocate and no longer carries a loop of its own. It is a structural
// check, not a behavioural one, and it is the strongest honest statement
// available without a live Postgres.
func TestStoresAllocateThroughTheLockedPath(t *testing.T) {
	stores := []struct {
		name string
		path string
	}{
		{name: "tenant", path: "../tenant/store.go"},
		{name: "wireguard", path: "../wireguard/store.go"},
	}

	for _, store := range stores {
		t.Run(store.name, func(t *testing.T) {
			src, err := parser.ParseFile(token.NewFileSet(), store.path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", store.path, err)
			}

			fn := findFunc(src, "GetNextAvailableIP")
			if fn == nil {
				t.Fatalf("%s has no GetNextAvailableIP", store.path)
			}

			var callsAllocate, hasOwnLoop bool
			ast.Inspect(fn, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.SelectorExpr:
					if ident, ok := node.X.(*ast.Ident); ok && ident.Name == "ipalloc" && node.Sel.Name == "Allocate" {
						callsAllocate = true
					}
				case *ast.ForStmt:
					hasOwnLoop = true
				case *ast.RangeStmt:
					hasOwnLoop = true
				}
				return true
			})

			if !callsAllocate {
				t.Error("does not allocate through ipalloc.Allocate, so it is not taking the advisory lock")
			}
			if hasOwnLoop {
				t.Error("still walks addresses itself; allocation belongs to ipalloc")
			}
		})
	}
}

// TestNoStoreKeepsItsOwnAdvisoryLock pins that the lock is taken in one place.
// Two lock sites are how the keys drift apart, and two different keys do not
// serialize against each other.
func TestNoStoreKeepsItsOwnAdvisoryLock(t *testing.T) {
	for _, path := range []string{"../tenant/store.go", "../wireguard/store.go"} {
		src, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(src, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if ok && strings.Contains(lit.Value, "pg_advisory") {
				t.Errorf("%s takes an advisory lock directly: %s", path, lit.Value)
			}
			return true
		})
	}
}

func findFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}
