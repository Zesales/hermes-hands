package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The size budget keeps "one job per unit" from eroding quietly: no
// function longer than maxFuncLines, no file longer than maxFileLines
// (tests, vendor/ and testdata/ excepted). A unit that outgrows it has
// taken on a second job — split it, don't raise the limit.
const (
	maxFuncLines = 80
	maxFileLines = 450
)

// overBudget is what is over the budget today, at its size today. An entry
// may only shrink: growing fails, and once it fits the budget it must leave
// this list. These are the delegation loop, the SSE stream and the brief —
// split them when they are next changed for a reason of their own.
var overBudget = map[string]int{
	"internal/loop/loop.go:Loop.Run":            139,
	"internal/api/stream.go:Client.streamRun":   144,
	"internal/knowsystem/brief.go:System.Brief": 102,
}

func TestSizeBudget(t *testing.T) {
	sizes := map[string]int{} // "file" or "file:Func" -> lines
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && (d.Name() == "vendor" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		p = filepath.ToSlash(p)
		sizes[p] = fset.Position(f.End()).Line
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				sizes[p+":"+funcName(fn)] = fset.Position(fn.End()).Line - fset.Position(fn.Pos()).Line + 1
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) < 50 {
		t.Fatalf("walked only %d files and functions — wrong directory?", len(sizes))
	}

	var problems []string
	for unit, n := range sizes {
		limit, what := maxFileLines, "file"
		if strings.Contains(unit, ":") {
			limit, what = maxFuncLines, "function"
		}
		if was, listed := overBudget[unit]; listed {
			switch {
			case n > was:
				problems = append(problems, unit+": grew past its listed size — it may only shrink; split it instead")
			case n <= limit:
				problems = append(problems, unit+": fits the budget now — drop it from overBudget")
			}
			continue
		}
		if n > limit {
			problems = append(problems, unit+": "+what+" over the budget — split it by job")
		}
	}
	for unit := range overBudget {
		if _, ok := sizes[unit]; !ok {
			problems = append(problems, unit+": listed in overBudget but gone — drop the entry")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// funcName is "Name", or "Recv.Name" for a method.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	t := fn.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if g, ok := t.(*ast.IndexExpr); ok { // a generic receiver
		t = g.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
