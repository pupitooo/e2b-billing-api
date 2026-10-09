package main

import (
	"bytes"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Five-digit decimal literals require three-digit groups, smaller values can
// remain plain, and calendar years and non-decimal forms retain their own syntax.
func TestExpectedLiteral(t *testing.T) {
	for _, fixture := range []struct {
		value, want string
		kind        token.Token
		year        bool
	}{
		{"9999", "9999", token.INT, false},
		{"10000", "10_000", token.INT, false},
		{"1000000", "1_000_000", token.INT, false},
		{"1_000", "1_000", token.INT, false},
		{"10_00_00", "100_000", token.INT, false},
		{"12345.6789", "12_345.678_9", token.FLOAT, false},
		{".00005", ".000_05", token.FLOAT, false},
		{"1e5", "1e5", token.FLOAT, false},
		{"1e10000", "1e10_000", token.FLOAT, false},
		{"100000i", "100_000i", token.IMAG, false},
		{"0x123456", "0x123456", token.INT, false},
		{"0b101010", "0b101010", token.INT, false},
		{"0600", "0600", token.INT, false},
		{`"1000000"`, `"1000000"`, token.STRING, false},
		{"2_026", "2026", token.INT, true},
		{"10_000", "10000", token.INT, true},
	} {
		t.Run(fixture.value, func(t *testing.T) {
			if got := expectedLiteral(fixture.kind, fixture.value, fixture.year); got != fixture.want {
				t.Fatalf("Literal %s: got %s, want %s", fixture.value, got, fixture.want)
			}
		})
	}
}

// Check mode reports source locations without editing. Fix mode repairs real
// literals and aliased calendar years while preserving comments, strings, and mode.
func TestRunChecksAndFixes(t *testing.T) {
	source := "package fixture\nimport clock \"time\"\n// 1000000 is prose.\nvar units = 1000000\nvar text = \"1000000\"\nconst MinUTCYear = 1_000\nvar date = clock.Date(2_026, 1, 1, 0, 0, 0, 0, clock.UTC)\n"
	path := writeFixture(t, t.TempDir(), "source.go", source)
	var diagnostics bytes.Buffer
	if code := run([]string{path}, &diagnostics); code != 1 || !strings.Contains(diagnostics.String(), "source.go:4:13:") {
		t.Fatalf("Expected positioned failure, got %d: %s", code, &diagnostics)
	}
	before, _ := os.ReadFile(path)
	if string(before) != source {
		t.Fatal("Check mode edited a source file")
	}
	if code := run([]string{"-fix", path}, &diagnostics); code != 0 {
		t.Fatalf("Fix failed with %d: %s", code, &diagnostics)
	}
	after, _ := os.ReadFile(path)
	for _, want := range []string{"units = 1_000_000", `text = "1000000"`, "// 1000000 is prose.", "MinUTCYear = 1000", "clock.Date(2026,"} {
		if !strings.Contains(string(after), want) {
			t.Errorf("Formatted source omitted %q: %s", want, after)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("File mode changed: %v", info.Mode())
	}
	if code := run([]string{path}, &diagnostics); code != 0 {
		t.Fatalf("Rechecking formatted source failed: %s", &diagnostics)
	}
}

// Year expressions, field names, and dot-imported time.Date calls use ordinary
// calendar digits; an unrelated quantity named years has the numeric grouping rule.
func TestYearContexts(t *testing.T) {
	source := "package fixture\nimport . \"time\"\nvar year = 2_026\nvar years = 10000\nvar date = Date(10_000, 1, 1, 0, 0, 0, 0, UTC)\nfunc check() { year = 2_027; _ = date.Year() < 9_999; _ = year >= 1_000; _ = struct { Year int }{Year: 2_026} }\n"
	path := writeFixture(t, t.TempDir(), "years.go", source)
	var diagnostics bytes.Buffer
	if code := run([]string{"-fix", path}, &diagnostics); code != 0 {
		t.Fatalf("Year formatting failed: %s", &diagnostics)
	}
	after, _ := os.ReadFile(path)
	for _, want := range []string{"year = 2026", "years = 10_000", "Date(10000,", "year = 2027", "Year() < 9999", "year >= 1000", "Year: 2026"} {
		if !strings.Contains(string(after), want) {
			t.Errorf("Expected year or quantity format %q in %s", want, after)
		}
	}
}

// Recursive checks include test files and paths with spaces while excluding
// private worktrees, Git metadata, and third-party dependency directories.
func TestRunDirectoryScope(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"tools", "vendor", "node_modules", ".git"} {
		writeFixture(t, filepath.Join(root, directory), "ignored.go", "package ignored\nvar units = 1000000\n")
	}
	writeFixture(t, filepath.Join(root, "path with spaces"), "quantity_test.go", "package fixture\nvar units = 1000000\n")
	var diagnostics bytes.Buffer
	if code := run([]string{root}, &diagnostics); code != 1 || strings.Contains(diagnostics.String(), "ignored.go") {
		t.Fatalf("Incorrect check scope, code %d: %s", code, &diagnostics)
	}
	if code := run([]string{"-fix", root}, &diagnostics); code != 0 {
		t.Fatalf("Recursive fix failed: %s", &diagnostics)
	}
	if code := run([]string{root}, &diagnostics); code != 0 {
		t.Fatalf("Formatted tree failed: %s", &diagnostics)
	}
}

// Invalid Go and missing paths produce actionable failures; fix mode must not
// overwrite syntactically invalid source while trying to group its digits.
func TestRunInvalidSource(t *testing.T) {
	source := "package fixture\nvar units = 100__000\n"
	path := writeFixture(t, t.TempDir(), "invalid.go", source)
	var diagnostics bytes.Buffer
	if code := run([]string{"-fix", path}, &diagnostics); code != 1 || diagnostics.Len() == 0 {
		t.Fatalf("Invalid Go was accepted: %d, %s", code, &diagnostics)
	}
	after, _ := os.ReadFile(path)
	if string(after) != source {
		t.Fatal("Invalid source was overwritten")
	}
	if code := run([]string{path + ".missing"}, &diagnostics); code != 1 {
		t.Fatalf("Missing source was accepted: %d", code)
	}
}

// writeFixture creates an isolated source file with a known mode so check and
// fix scenarios can verify diagnostics, content preservation, and traversal.
func writeFixture(t *testing.T, directory, name, source string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}
