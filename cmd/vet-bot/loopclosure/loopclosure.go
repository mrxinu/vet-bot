// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// A modified version of the code found in the Golang standard library which handles nested loops.
package loopclosure

import (
	"go/ast"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

const Doc = `This is an augmented version of the loopanalyzer found in the
standard library. It handles nested loops (albeit in an expensive sort of way).`

var Analyzer = &analysis.Analyzer{
	Name:     "loopclosure-augmented",
	Doc:      Doc,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (interface{}, error) {
	inspect := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	nodeFilter := []ast.Node{
		(*ast.RangeStmt)(nil),
		(*ast.ForStmt)(nil),
	}
	inspect.Preorder(nodeFilter, func(n ast.Node) {
		inspectLoopBody(n, nil, pass)
	})
	return nil, nil
}

type LoopVar struct {
	ident *ast.Ident
	body  *ast.BlockStmt
}

func inspectLoopBody(n ast.Node, outerVars []LoopVar, pass *analysis.Pass) {
	// Find the variables updated by the loop statement.
	loopVars := make([]LoopVar, len(outerVars))
	copy(loopVars, outerVars)
	addVar := func(expr ast.Expr, body *ast.BlockStmt) {
		if id, ok := expr.(*ast.Ident); ok {
			loopVars = append(loopVars, LoopVar{
				ident: id,
				body:  body,
			})
		}
	}
	forStmt := n
	var body *ast.BlockStmt
	switch n := n.(type) {
	case *ast.RangeStmt:
		body = n.Body
		addVar(n.Key, body)
		addVar(n.Value, body)
	case *ast.ForStmt:
		body = n.Body
		switch post := n.Post.(type) {
		case *ast.AssignStmt:
			// e.g. for p = head; p != nil; p = p.next
			for _, lhs := range post.Lhs {
				addVar(lhs, body)
			}
		case *ast.IncDecStmt:
			// e.g. for i := 0; i < n; i++
			addVar(post.X, body)
		}
	}
	if loopVars == nil {
		return
	}

	inspectFuncLit := func(lit *ast.FuncLit) {
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || id.Obj == nil {
				return true
			}
			if id.Obj != nil && id.Obj.Kind != ast.Var {
				// Not referring to a variable.
				return true
			}
			for _, v := range loopVars {
				if v.ident.Obj == id.Obj {
					pass.ReportRangef(forStmt, "loop variable %s captured by func literal",
						id.Name)
				}
			}
			return true
		})
	}

	if len(body.List) == 0 {
		return
	}
	for _, stmt := range body.List {
		switch s := stmt.(type) {
		case *ast.GoStmt:
			if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
				inspectFuncLit(lit)
			}
		case *ast.DeferStmt:
			if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
				inspectFuncLit(lit)
			}

		// check nested loops as well
		case *ast.RangeStmt:
			inspectLoopBody(s, loopVars, pass)
		case *ast.ForStmt:
			inspectLoopBody(s, loopVars, pass)
		}
	}
}
