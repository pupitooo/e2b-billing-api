// Command number-format checks the project's decimal literal grouping rule.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type replacement struct {
	start, end int
	value      string
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("number-format", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	fix := flags.Bool("fix", false, "format decimal literals and calendar years")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	paths := flags.Args()
	if len(paths) == 0 {
		paths = []string{"."}
	}
	failed := false
	for _, path := range paths {
		err := filepath.WalkDir(path, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				base := entry.Name()
				if name != path && (strings.HasPrefix(base, ".") || base == "tools" || base == "vendor" || base == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") {
				return nil
			}
			bad, err := processFile(name, *fix, diagnostics)
			if err != nil {
				return err
			}
			failed = failed || bad
			return nil
		})
		if err != nil {
			fmt.Fprintln(diagnostics, err)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func processFile(path string, fix bool, diagnostics io.Writer) (bool, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, path, source, 0)
	if err != nil {
		return false, err
	}
	years := yearLiterals(file)
	var edits []replacement
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok {
			return true
		}
		expected := expectedLiteral(literal.Kind, literal.Value, years[literal])
		if expected != literal.Value {
			position := set.Position(literal.Pos())
			edits = append(edits, replacement{position.Offset, position.Offset + len(literal.Value), expected})
			if !fix {
				fmt.Fprintf(diagnostics, "%s: numeric literal %s must be written as %s\n", position, literal.Value, expected)
			}
		}
		return true
	})
	if len(edits) == 0 || !fix {
		return len(edits) != 0, nil
	}
	for index := len(edits) - 1; index >= 0; index-- {
		edit := edits[index]
		source = append(append(append([]byte{}, source[:edit.start]...), edit.value...), source[edit.end:]...)
	}
	formatted, err := format.Source(source)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return false, os.WriteFile(path, formatted, info.Mode())
}

// yearLiterals recognizes time.Date, year-named values, and Year comparisons.
// These calendar values use ordinary digits even when a test uses year 10000.
func yearLiterals(file *ast.File) map[*ast.BasicLit]bool {
	aliases := make(map[string]bool)
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		if path == "time" {
			name := "time"
			if spec.Name != nil {
				name = spec.Name.Name
			}
			aliases[name] = true
		}
	}
	years := make(map[*ast.BasicLit]bool)
	mark := func(expression ast.Expr) {
		ast.Inspect(expression, func(node ast.Node) bool {
			if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.INT {
				years[literal] = true
			}
			return true
		})
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			date := false
			switch function := node.Fun.(type) {
			case *ast.SelectorExpr:
				owner, ok := function.X.(*ast.Ident)
				date = ok && aliases[owner.Name] && function.Sel.Name == "Date"
			case *ast.Ident:
				date = aliases["."] && function.Name == "Date"
			}
			if date && len(node.Args) > 0 {
				mark(node.Args[0])
			}
		case *ast.ValueSpec:
			for index, name := range node.Names {
				if yearName(name.Name) && index < len(node.Values) {
					mark(node.Values[index])
				}
			}
		case *ast.AssignStmt:
			for index, left := range node.Lhs {
				if name, ok := left.(*ast.Ident); ok && yearName(name.Name) && index < len(node.Rhs) {
					mark(node.Rhs[index])
				}
			}
		case *ast.KeyValueExpr:
			if name, ok := node.Key.(*ast.Ident); ok && yearName(name.Name) {
				mark(node.Value)
			}
		case *ast.BinaryExpr:
			if node.Op == token.LSS || node.Op == token.LEQ || node.Op == token.GTR || node.Op == token.GEQ || node.Op == token.EQL || node.Op == token.NEQ {
				if yearExpression(node.X) {
					mark(node.Y)
				}
				if yearExpression(node.Y) {
					mark(node.X)
				}
			}
		}
		return true
	})
	return years
}

func yearName(name string) bool { return strings.HasSuffix(strings.ToLower(name), "year") }

func yearExpression(expression ast.Expr) bool {
	if name, ok := expression.(*ast.Ident); ok {
		return yearName(name.Name)
	}
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	function, ok := call.Fun.(*ast.SelectorExpr)
	return ok && function.Sel.Name == "Year"
}

func expectedLiteral(kind token.Token, value string, year bool) string {
	if kind != token.INT && kind != token.FLOAT && kind != token.IMAG {
		return value
	}
	if year {
		return strings.ReplaceAll(value, "_", "")
	}
	digits := strings.ReplaceAll(value, "_", "")
	lower := strings.ToLower(digits)
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0b") || strings.HasPrefix(lower, "0o") ||
		(kind == token.INT && len(digits) > 1 && digits[0] == '0') {
		return value
	}
	imaginary := ""
	if strings.HasSuffix(value, "i") {
		value, imaginary = strings.TrimSuffix(value, "i"), "i"
	}
	mantissa, exponent := value, ""
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		mantissa, exponent = value[:index], value[index:]
		prefix, magnitude := exponent[:1], exponent[1:]
		if strings.HasPrefix(magnitude, "+") || strings.HasPrefix(magnitude, "-") {
			prefix, magnitude = prefix+magnitude[:1], magnitude[1:]
		}
		if len(strings.ReplaceAll(magnitude, "_", "")) >= 5 || strings.Contains(magnitude, "_") {
			exponent = prefix + groupDigits(strings.ReplaceAll(magnitude, "_", ""), false)
		}
	}
	integer, fraction, point := strings.Cut(mantissa, ".")
	plainInteger, plainFraction := strings.ReplaceAll(integer, "_", ""), strings.ReplaceAll(fraction, "_", "")
	if len(plainInteger)+len(plainFraction) >= 5 || strings.Contains(mantissa, "_") {
		mantissa = groupDigits(plainInteger, false)
		if point {
			mantissa += "." + groupDigits(plainFraction, true)
		}
	}
	return mantissa + exponent + imaginary
}

func groupDigits(digits string, fractional bool) string {
	if len(digits) <= 3 {
		return digits
	}
	first := len(digits) % 3
	if fractional || first == 0 {
		first = 3
	}
	result := digits[:first]
	for index := first; index < len(digits); index += 3 {
		result += "_" + digits[index:min(index+3, len(digits))]
	}
	return result
}
