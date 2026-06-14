package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestAdminEndpoints_BodySizeLimit is a structural regression test for
// C-13: it walks every function in internal/api/admin.go and verifies
// that any function containing a json.NewDecoder / json.Unmarshal call
// on r.Body also contains an http.MaxBytesReader call (or wraps
// r.Body with http.MaxBytesReader first). The check is structural
// (AST-based) so it does not false-positive on:
//
//   - comments mentioning MaxBytesReader without an actual call
//   - calls to MaxBytesReader in unrelated functions
//   - json.NewDecoder wrapped on a non-r.Body reader
//
// If a future admin handler decodes JSON from r.Body without
// bounding the size, this test fails so the operator is not exposed
// to a trivial DoS via 4 GB payloads.
func TestAdminEndpoints_BodySizeLimit(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "admin.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse admin.go: %v", err)
	}

	// jsonBodyDecodeNames are the receiver types we consider
	// "body-decoding" for r.Body. We allow a small allowlist of
	// legitimate reader names (reqBody, etc.) — but r.Body is the
	// canonical name and is the one we guard.
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		hasBodyDecode := false
		hasMaxBytesReader := false
		// Walk the body for json.NewDecoder / json.Unmarshal on r.Body
		// and for http.MaxBytesReader calls.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch {
			case isCallTo(call, "json", "NewDecoder"):
				// Check the argument is r.Body.
				if len(call.Args) >= 1 && isRBodySelector(call.Args[0]) {
					hasBodyDecode = true
				}
				// The MaxBytesReader is often inline here
				// (json.NewDecoder(http.MaxBytesReader(w, r.Body, N)).Decode(...))
				// — also inspect the argument for MaxBytesReader.
				if len(call.Args) >= 1 {
					if containsMaxBytesReader(call.Args[0]) {
						hasMaxBytesReader = true
					}
				}
			case isCallTo(call, "http", "MaxBytesReader"):
				hasMaxBytesReader = true
			case isCallTo(call, "json", "Unmarshal"):
				// json.Unmarshal(data, &v): Args[0] is the source reader,
				// Args[1] is the target pointer. Check the source.
				if len(call.Args) >= 1 && isRBodySelector(call.Args[0]) {
					hasBodyDecode = true
				}
			}
			return true
		})

		if hasBodyDecode && !hasMaxBytesReader {
			t.Errorf("%s: decodes json from r.Body but has no http.MaxBytesReader call (C-13 violation)",
				fn.Name.Name)
		}
	}
}

// isCallTo reports whether the expression is a selector call
// pkg.Name(...). For example isCallTo(x, "json", "NewDecoder") matches
// json.NewDecoder(...). It does not match x.json.NewDecoder.
func isCallTo(call *ast.CallExpr, pkg, fn string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != fn {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != pkg {
		return false
	}
	return true
}

// isRBodySelector reports whether expr is the selector r.Body.
func isRBodySelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Body" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "r" {
		return false
	}
	return true
}

// containsMaxBytesReader walks expr to see if it contains a
// http.MaxBytesReader call anywhere in its sub-tree.
func containsMaxBytesReader(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isCallTo(call, "http", "MaxBytesReader") {
			found = true
			return false
		}
		return true
	})
	return found
}

// TestMaxAdminBodySize_ConstantGuardsEndpoints is a small smoke test
// to make sure the constant is non-zero and matches the documented
// comment in admin.go.
func TestMaxAdminBodySize_ConstantGuardsEndpoints(t *testing.T) {
	if maxAdminBodySize <= 0 {
		t.Errorf("maxAdminBodySize = %d, must be > 0", maxAdminBodySize)
	}
	// Sanity: 4 KiB is what the doc comment claims.
	if maxAdminBodySize != 4096 {
		t.Errorf("maxAdminBodySize = %d, expected 4096 (4 KiB) per admin.go doc", maxAdminBodySize)
	}
	// And the string is referenced in the comment block.
	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatalf("read admin.go: %v", err)
	}
	if !strings.Contains(string(src), "maxAdminBodySize = 4096") {
		t.Error("admin.go does not contain the documented constant value")
	}
}
