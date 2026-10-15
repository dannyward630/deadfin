// SPDX-License-Identifier: GPL-2.0-only

// Command convention-gates provides source-oriented CI tripwires for repository
// conventions. It intentionally recognizes SQL-shaped string literals rather
// than parsing SQL, so it is not a SQL parser or validator.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// A single SQL identifier form: bare, double-quoted, bracketed, or
// backticked. Bracketed/backticked names are accepted on both sides of a
// schema-qualifying dot (the prior qualifier accepted only bare or
// double-quoted names there, so main.[items] and main.`items` slipped past).
const sqlSingleIdent = `(?:[A-Za-z_][\w$]*|"[^"]+"|` + "`[^`]+`" + `|\[[^\]]+\])`

// A SQL object name: one identifier form, optionally schema-qualified by a
// second identifier form of any supported kind.
const sqlIdent = sqlSingleIdent + `(?:\.` + sqlSingleIdent + `)?`

// A real statement can place these tokens after an object name: a paren, a
// keyword, a terminator, or end of string. Requiring this is what keeps
// ordinary prose out of the gate: an error message like
// "create view root: %v" has a colon there, so it is not a statement.
const sqlAfterIdent = `(?:\s*\(|\s*;|\s*$|\s+(?:as|on|where|values|select|default|set|rename|add|drop|alter|to|using|begin|for|after|before|instead|returning)\b)`

var sqlStatement = regexp.MustCompile(`(?is)^(?:` +
	`select\s+(?:(?:distinct|all)\s+)?\S+|` +
	`(?:insert|replace)\s+(?:or\s+\w+\s+)?into\s+` + sqlIdent + sqlAfterIdent + `|` +
	`update\s+` + sqlIdent + `\s+set\s+|` +
	`delete\s+from\s+` + sqlIdent + sqlAfterIdent + `|` +
	`create\s+(?:(?:temp|temporary)\s+)?(?:table|index|view|trigger|schema|database)\s+(?:if\s+not\s+exists\s+)?` + sqlIdent + sqlAfterIdent + `|` +
	`create\s+unique\s+index\s+(?:if\s+not\s+exists\s+)?` + sqlIdent + sqlAfterIdent + `|` +
	`alter\s+table\s+` + sqlIdent + sqlAfterIdent + `|` +
	`drop\s+(?:table|index|view|trigger|schema|database)\s+(?:if\s+exists\s+)?` + sqlIdent + `(?:\s*;|\s*$|\s+\w+\b)|` +
	`pragma\b|` +
	`with\s+(?:recursive\s+)?\w+\s+as\s*\(|` +
	`vacuum(?:\s|;|$)|` +
	`attach\s+(?:database\s+)?\S+|` +
	`detach\s+(?:database\s+)?\S+|` +
	`reindex(?:\s|;|$)|` +
	`begin(?:\s+(?:deferred|immediate|exclusive))?(?:\s+(?:transaction|work))?\s*(?:;|$)|` +
	`commit(?:\s+(?:transaction|work))?\s*(?:;|$)|` +
	`rollback(?:\s+(?:transaction|work))?(?:\s+to(?:\s+savepoint)?\s+\w+)?\s*(?:;|$)|` +
	`savepoint\s+\w+\s*(?:;|$)|` +
	`release\s+(?:savepoint\s+)?\w+\s*(?:;|$)|` +
	`analyze(?:\s+[\w.]+)?\s*(?:;|$)|` +
	`explain\s+query\s+plan\s+)`)

func main() {
	root := flag.String("root", "deadfin", "Go module root to inspect")
	file := flag.String("file", "", "Go source file relative to root to inspect")
	flag.Parse()

	violations, err := check(*root, *file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(violations) == 0 {
		return
	}
	for _, violation := range violations {
		fmt.Fprintln(os.Stderr, violation)
	}
	os.Exit(1)
}

func check(root, selectedFile string) ([]string, error) {
	if selectedFile != "" {
		path := filepath.Join(root, selectedFile)
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("selected file must be within root: %s", selectedFile)
		}
		violations, err := checkPath(root, path)
		if err != nil {
			return nil, err
		}
		spdx, err := checkSPDXPath(root, path)
		return append(violations, spdx...), err
	}

	violations, err := checkSPDXTree(root)
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			// The whole-tree walk covers the runtime module: cmd/ and
			// internal/. It skips dot-directories (.git, and .ci, which
			// holds this gate's deliberately violating fixtures), vendored
			// code, the fetched upstream tree, and tools/, whose dev
			// tooling is checked file by file with -file where a gate
			// names it. A nested Go module is its own tree and is skipped.
			name := entry.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "upstream" || name == "tools") {
				return filepath.SkipDir
			}
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}

		fileViolations, err := checkPath(root, path)
		if err != nil {
			return err
		}
		violations = append(violations, fileViolations...)
		return nil
	})
	return violations, err
}

// REUSE-IgnoreStart

// spdxHeader is the license line that every Deadfin Go file carries before
// its package clause.
const spdxHeader = "SPDX-License-Identifier: GPL-2.0-only"

// REUSE-IgnoreEnd

// checkSPDXTree checks the license header of every Go file under root: the
// runtime module, tools/ and the nested tool modules. It skips
// dot-directories (.git, and .ci, which holds this gate's fixtures), vendored
// code, testdata, and the fetched upstream tree.
func checkSPDXTree(root string) ([]string, error) {
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "upstream") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		fileViolations, err := checkSPDXPath(root, path)
		if err != nil {
			return err
		}
		violations = append(violations, fileViolations...)
		return nil
	})
	return violations, err
}

// checkSPDXPath reports a Go file that has no spdxHeader comment before its
// package clause. A generated file ("Code generated ... DO NOT EDIT.") is
// exempt: its generator owns the content.
func checkSPDXPath(root, path string) ([]string, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	if ast.IsGenerated(file) {
		return nil, nil
	}
	for _, group := range file.Comments {
		if group.Pos() > file.Package {
			break
		}
		for _, comment := range group.List {
			if strings.TrimSpace(strings.TrimPrefix(comment.Text, "//")) == spdxHeader {
				return nil, nil
			}
		}
	}
	return []string{fmt.Sprintf("%s: missing the // %s header before the package clause", filepath.ToSlash(rel), spdxHeader)}, nil
}

func checkPath(root, path string) ([]string, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	return checkFile(fileSet, path, filepath.ToSlash(rel), file), nil
}

func checkFile(fileSet *token.FileSet, path, rel string, file *ast.File) []string {
	var violations []string
	isStore := rel == "internal/store" || strings.HasPrefix(rel, "internal/store/")
	isClock := rel == "internal/clock" || strings.HasPrefix(rel, "internal/clock/")
	isHandler := rel == "internal/api/handlers" || strings.HasPrefix(rel, "internal/api/handlers/")
	timeImports := importedNames(file, "time")
	// A direct first-argument string literal passed to a diagnostic method
	// on a testing receiver is a test diagnostic, not SQL. The diagnostic
	// methods are Error, Errorf, Fatal, Fatalf, Log, Logf, Skip, and Skipf.
	// Such a literal is exempt from the raw-SQL check. The exemption is
	// parser-only. The receiver is identified by its declared type in the
	// enclosing function signature, never by type information.
	//
	// Only a direct first-argument literal is exempted. Strings in
	// variables, nested expressions, or unrelated calls stay subject to the
	// gate.
	//
	// Receiver recognition is lexical. It is scoped per function
	// declaration, function literal, and block statement, not file-wide.
	// scopeStack mirrors the AST walk. Each FuncDecl, FuncLit, or BlockStmt
	// pushes a frame naming the names it declares. The matching nil return
	// pops it. scopeMarkers records one marker per entered node.
	//
	// The pop lines up with the node that pushed it. A call inside a
	// closure resolves a name against the innermost frame that declares it.
	// An outer testing receiver is recognized inside a t.Run callback. An
	// inner non-testing parameter or local variable of the same name shadows
	// it.
	//
	// A variable merely named like a testing receiver in a different
	// function is not exempted. A local non-testing variable inside a
	// block shadows an outer testing receiver of the same name.
	testingImports := importedNames(file, "testing")
	var scopeStack []map[string]bool
	var scopeMarkers []bool
	receivers := func() map[string]bool {
		visible := make(map[string]bool)
		// Walk innermost frame first. An inner declaration shadows any
		// outer one of the same name. A name is a testing receiver only if
		// the innermost declaring frame marks it so.
		for i := len(scopeStack) - 1; i >= 0; i-- {
			for name, isTesting := range scopeStack[i] {
				if _, seen := visible[name]; seen {
					continue
				}
				visible[name] = isTesting
			}
		}
		return visible
	}
	exempt := make(map[token.Pos]bool)
	if !isStore {
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err == nil && importPath == "database/sql" {
				violations = append(violations, violation(fileSet, path, spec.Pos(), "database/sql is only permitted under internal/store"))
			}
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		// The nil callback fires once per node after its subtree. Track
		// one marker per entered node so the pop lines up with the node
		// that pushed it. FuncDecl, FuncLit, and BlockStmt carry a scope
		// frame.
		var openedScope bool
		switch node := node.(type) {
		case nil:
			if len(scopeMarkers) > 0 && scopeMarkers[len(scopeMarkers)-1] {
				scopeStack = scopeStack[:len(scopeStack)-1]
			}
			if len(scopeMarkers) > 0 {
				scopeMarkers = scopeMarkers[:len(scopeMarkers)-1]
			}
			return true
		case *ast.BasicLit:
			if !isStore && node.Kind == token.STRING && !exempt[node.Pos()] {
				value, err := strconv.Unquote(node.Value)
				if err == nil && isSQLStatement(value) {
					violations = append(violations, violation(fileSet, path, node.Pos(), "raw SQL is only permitted under internal/store"))
				}
			}
		case *ast.SelectorExpr:
			if !isClock && isTimeNowSelector(node, timeImports) {
				violations = append(violations, violation(fileSet, path, node.Pos(), "time.Now is only permitted in internal/clock"))
			}
			if isHandler && isURLQuerySelector(node) {
				violations = append(violations, violation(fileSet, path, node.Pos(), "handlers must use api/bind rather than URL.Query"))
			}
		case *ast.CallExpr:
			if !isClock && callsDotImportedTimeNow(node, timeImports) {
				violations = append(violations, violation(fileSet, path, node.Pos(), "time.Now is only permitted in internal/clock"))
			}
			// A CallExpr is visited before its argument expressions. Record
			// any direct diagnostic string here for the BasicLit case to
			// skip.
			if lit := testingDiagnosticLiteral(node, receivers()); lit != nil {
				exempt[lit.Pos()] = true
			}
		case *ast.FuncDecl:
			scopeStack = append(scopeStack, paramReceiverFlags(node.Type, testingImports))
			openedScope = true
		case *ast.FuncLit:
			scopeStack = append(scopeStack, paramReceiverFlags(node.Type, testingImports))
			openedScope = true
		case *ast.BlockStmt:
			// A block opens a new lexical scope. Names declared inside it
			// shadow the same name in an enclosing scope.
			scopeStack = append(scopeStack, make(map[string]bool))
			openedScope = true
		case *ast.AssignStmt:
			// A short variable declaration introduces names in the
			// current scope. Mark each as a non-testing receiver so it
			// shadows an outer testing receiver of the same name.
			if node.Tok == token.DEFINE && len(scopeStack) > 0 {
				frame := scopeStack[len(scopeStack)-1]
				for _, expr := range node.Lhs {
					if ident, ok := expr.(*ast.Ident); ok {
						if ident.Name != "" && ident.Name != "_" {
							frame[ident.Name] = false
						}
					}
				}
			}
		case *ast.DeclStmt:
			// A var declaration introduces names in the current scope.
			// Mark each as a non-testing receiver.
			if genDecl, ok := node.Decl.(*ast.GenDecl); ok && genDecl.Tok == token.VAR && len(scopeStack) > 0 {
				frame := scopeStack[len(scopeStack)-1]
				for _, spec := range genDecl.Specs {
					if valueSpec, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range valueSpec.Names {
							if name.Name != "" && name.Name != "_" {
								frame[name.Name] = false
							}
						}
					}
				}
			}
		}
		scopeMarkers = append(scopeMarkers, openedScope)
		return true
	})
	return violations
}

// paramReceiverFlags returns every named parameter of a single function
// signature. Each maps to whether its declared type is a testing receiver
// type (*testing.T, testing.T, testing.TB, or *testing.TB). It is the
// per-function scope frame pushed onto scopeStack.
//
// A name declared here shadows the same name in an enclosing frame. An
// inner non-testing parameter of the same name as an outer testing receiver
// is not treated as a receiver. Only the innermost declaring frame
// determines a name's status.
//
// Identification is purely syntactic. The type expression must be a
// selector on the "testing" package named T or TB. The import path behind
// the identifier must be "testing", not a fake package aliased as
// "testing". No type information is required.
func paramReceiverFlags(ft *ast.FuncType, testingImports map[string]bool) map[string]bool {
	names := make(map[string]bool)
	if ft == nil || ft.Params == nil {
		return names
	}
	for _, field := range ft.Params.List {
		isTesting := isTestingReceiverType(field.Type, testingImports)
		if len(field.Names) == 0 {
			// A single unnamed param carries no usable selector name.
			continue
		}
		for _, name := range field.Names {
			if name.Name != "" && name.Name != "_" {
				names[name.Name] = isTesting
			}
		}
	}
	return names
}

// isTestingReceiverType reports whether expr is *testing.T, testing.T,
// testing.TB, or *testing.TB. It unwraps at most one pointer indirection.
// It matches a selector whose package identifier is "testing" and whose
// name is "T" or "TB". The identifier must map to a real import of
// "testing", not a fake package aliased as "testing". This is the intended
// parser-only boundary.
func isTestingReceiverType(expr ast.Expr, testingImports map[string]bool) bool {
	inner := expr
	if star, ok := inner.(*ast.StarExpr); ok {
		inner = star.X
	}
	sel, ok := inner.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && testingImports[pkg.Name] && (sel.Sel.Name == "T" || sel.Sel.Name == "TB")
}

// isTestingDiagnosticMethod reports whether name is one of the
// testing.T/testing.TB methods whose first argument is a free-form
// diagnostic string rather than data. It is an immutable lookup.
func isTestingDiagnosticMethod(name string) bool {
	switch name {
	case "Error", "Errorf", "Fatal", "Fatalf",
		"Log", "Logf", "Skip", "Skipf":
		return true
	}
	return false
}

// testingDiagnosticLiteral returns the first argument of call as a string
// literal when call is <receiver>.<Method>(<string-literal>, ...) on a
// testing receiver, or nil otherwise. Only a literal that is the direct
// first argument qualifies: a string reached through a variable, a nested
// call, or a concatenation is not exempted.
func testingDiagnosticLiteral(call *ast.CallExpr, receivers map[string]bool) *ast.BasicLit {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	if !isTestingDiagnosticMethod(sel.Sel.Name) {
		return nil
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || !receivers[ident.Name] {
		return nil
	}
	if len(call.Args) == 0 {
		return nil
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil
	}
	return lit
}

func isSQLStatement(value string) bool {
	value = strings.TrimSpace(value)
	for {
		switch {
		case strings.HasPrefix(value, "--"):
			if newline := strings.IndexByte(value, '\n'); newline >= 0 {
				value = strings.TrimSpace(value[newline+1:])
				continue
			}
			return false
		case strings.HasPrefix(value, "/*"):
			end := strings.Index(value[2:], "*/")
			if end < 0 {
				return false
			}
			value = strings.TrimSpace(value[end+4:])
			continue
		}
		return sqlStatement.MatchString(value)
	}
}

func importedNames(file *ast.File, importPath string) map[string]bool {
	names := make(map[string]bool)
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != importPath {
			continue
		}
		if spec.Name == nil {
			names[filepath.Base(importPath)] = true
			continue
		}
		if spec.Name.Name != "_" {
			names[spec.Name.Name] = true
		}
	}
	return names
}

func isTimeNowSelector(selector *ast.SelectorExpr, imported map[string]bool) bool {
	if selector.Sel.Name != "Now" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && imported[pkg.Name]
}

func callsDotImportedTimeNow(call *ast.CallExpr, imported map[string]bool) bool {
	ident, ok := call.Fun.(*ast.Ident)
	return ok && ident.Name == "Now" && imported["."]
}

func isURLQuerySelector(selector *ast.SelectorExpr) bool {
	if selector.Sel.Name != "Query" {
		return false
	}
	url, ok := selector.X.(*ast.SelectorExpr)
	return ok && url.Sel.Name == "URL"
}

func violation(fileSet *token.FileSet, path string, position token.Pos, message string) string {
	location := fileSet.Position(position)
	return fmt.Sprintf("%s:%d:%d: %s", path, location.Line, location.Column, message)
}
