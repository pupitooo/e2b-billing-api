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
		name  string
		value string
		want  string
		kind  token.Token
		year  bool
	}{
		{
			name:  "below five digits",
			value: "9999",
			want:  "9999",
			kind:  token.INT,
			year:  false,
		},
		{
			name:  "five-digit integer",
			value: "10000",
			want:  "10_000",
			kind:  token.INT,
			year:  false,
		},
		{
			name:  "million integer",
			value: "1000000",
			want:  "1_000_000",
			kind:  token.INT,
			year:  false,
		},
		{
			name:  "optional grouping below five digits",
			value: "1_000",
			want:  "1_000",
			kind:  token.INT,
			year:  false,
		},
		{
			name:  "incorrect integer groups",
			value: "10_00_00",
			want:  "100_000",
			kind:  token.INT,
			year:  false,
		},
		{
			name:  "integer and fractional groups",
			value: "12345.6789",
			want:  "12_345.678_9",
			kind:  token.FLOAT,
			year:  false,
		},
		{
			name:  "fraction without integer part",
			value: ".00005",
			want:  ".000_05",
			kind:  token.FLOAT,
			year:  false,
		},
		{
			name:  "small exponent",
			value: "1e5",
			want:  "1e5",
			kind:  token.FLOAT,
			year:  false,
		},
		{
			name:  "five-digit exponent",
			value: "1e10000",
			want:  "1e10_000",
			kind:  token.FLOAT,
			year:  false,
		},
		{
			name:  "imaginary literal",
			value: "100000i",
			want:  "100_000i",
			kind:  token.IMAG,
			year:  false,
		},
		{
			name:  "hexadecimal literal",
			value: "0x123456",
			want:  "0x123456",
			kind:  token.INT,
			year:  false,
		},
		{
			name:  "binary literal",
			value: "0b101010",
			want:  "0b101010",
			kind:  token.INT,
			year:  false,
		},
		{
			name:  "octal permission literal",
			value: "0600",
			want:  "0600",
			kind:  token.INT,
			year:  false,
		},
		{
			name:  "string content",
			value: `"1000000"`,
			want:  `"1000000"`,
			kind:  token.STRING,
			year:  false,
		},
		{
			name:  "grouped calendar year",
			value: "2_026",
			want:  "2026",
			kind:  token.INT,
			year:  true,
		},
		{
			name:  "calendar year above supported range",
			value: "10_000",
			want:  "10000",
			kind:  token.INT,
			year:  true,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if got := expectedLiteral(fixture.kind, fixture.value, fixture.year); got != fixture.want {
				t.Fatalf("Literal %s: got %s, want %s", fixture.value, got, fixture.want)
			}
		})
	}
}

// run checks and fixes decimal literals and calendar years while preserving
// comments, strings, file modes, excluded directories, and invalid source.
func TestRun(t *testing.T) {
	t.Run("run checks and fixes", func(t *testing.T) {
		tt := struct {
			inputSource        string
			wantCheckExitCode  int
			wantFixExitCode    int
			wantDiagnostic     string
			wantSourceContains []string
			wantFileMode       os.FileMode
		}{
			inputSource: `package fixture
import clock "time"
// 1000000 is prose.
var units = 1000000
var text = "1000000"
const MinUTCYear = 1_000
var date = clock.Date(2_026, 1, 1, 0, 0, 0, 0, clock.UTC)
`,
			wantCheckExitCode:  1,
			wantFixExitCode:    0,
			wantDiagnostic:     "source.go:4:13:",
			wantSourceContains: []string{"units = 1_000_000", `text = "1000000"`, "// 1000000 is prose.", "MinUTCYear = 1000", "clock.Date(2026,"},
			wantFileMode:       0o640,
		}

		source := tt.inputSource
		path := writeFixture(t, t.TempDir(), "source.go", source)
		var diagnostics bytes.Buffer
		if code := run([]string{path}, &diagnostics); code != tt.wantCheckExitCode || !strings.Contains(diagnostics.String(), tt.wantDiagnostic) {
			t.Fatalf("Expected positioned failure, got %d: %s", code, &diagnostics)
		}
		before, _ := os.ReadFile(path)
		if string(before) != source {
			t.Fatal("Check mode edited a source file")
		}
		if code := run([]string{"-fix", path}, &diagnostics); code != tt.wantFixExitCode {
			t.Fatalf("Fix failed with %d: %s", code, &diagnostics)
		}
		after, _ := os.ReadFile(path)
		for _, want := range tt.wantSourceContains {
			if !strings.Contains(string(after), want) {
				t.Errorf("Formatted source omitted %q: %s", want, after)
			}
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != tt.wantFileMode {
			t.Fatalf("File mode changed: %v", info.Mode())
		}
		if code := run([]string{path}, &diagnostics); code != tt.wantFixExitCode {
			t.Fatalf("Rechecking formatted source failed: %s", &diagnostics)
		}
	})
	t.Run("year contexts", func(t *testing.T) {
		tt := struct {
			inputSource        string
			wantExitCode       int
			wantSourceContains []string
		}{
			inputSource: `package fixture
import . "time"
var year = 2_026
var years = 10000
var date = Date(10_000, 1, 1, 0, 0, 0, 0, UTC)
func check() { year = 2_027; _ = date.Year() < 9_999; _ = year >= 1_000; _ = struct { Year int }{Year: 2_026} }
`,
			wantExitCode:       0,
			wantSourceContains: []string{"year = 2026", "years = 10_000", "Date(10000,", "year = 2027", "Year() < 9999", "year >= 1000", "Year: 2026"},
		}

		source := tt.inputSource
		path := writeFixture(t, t.TempDir(), "years.go", source)
		var diagnostics bytes.Buffer
		if code := run([]string{"-fix", path}, &diagnostics); code != tt.wantExitCode {
			t.Fatalf("Year formatting failed: %s", &diagnostics)
		}
		after, _ := os.ReadFile(path)
		for _, want := range tt.wantSourceContains {
			if !strings.Contains(string(after), want) {
				t.Errorf("Expected year or quantity format %q in %s", want, after)
			}
		}
	})
	t.Run("run directory scope", func(t *testing.T) {
		tt := struct {
			excludedDirectories    []string
			inputSource            string
			wantCheckExitCode      int
			wantFixExitCode        int
			wantExcludedDiagnostic string
		}{
			excludedDirectories: []string{"tools", "vendor", "node_modules", ".git"},
			inputSource: `package fixture
var units = 1000000
`,
			wantCheckExitCode:      1,
			wantFixExitCode:        0,
			wantExcludedDiagnostic: "ignored.go",
		}

		root := t.TempDir()
		for _, directory := range tt.excludedDirectories {
			writeFixture(t, filepath.Join(root, directory), "ignored.go", "package ignored\nvar units = 1000000\n")
		}
		writeFixture(t, filepath.Join(root, "path with spaces"), "quantity_test.go", tt.inputSource)
		var diagnostics bytes.Buffer
		if code := run([]string{root}, &diagnostics); code != tt.wantCheckExitCode || strings.Contains(diagnostics.String(), tt.wantExcludedDiagnostic) {
			t.Fatalf("Incorrect check scope, code %d: %s", code, &diagnostics)
		}
		if code := run([]string{"-fix", root}, &diagnostics); code != tt.wantFixExitCode {
			t.Fatalf("Recursive fix failed: %s", &diagnostics)
		}
		if code := run([]string{root}, &diagnostics); code != tt.wantFixExitCode {
			t.Fatalf("Formatted tree failed: %s", &diagnostics)
		}
	})
	t.Run("run invalid source", func(t *testing.T) {
		tt := struct {
			inputSource    string
			wantExitCode   int
			wantDiagnostic bool
		}{
			inputSource: `package fixture
var units = 100__000
`,
			wantExitCode:   1,
			wantDiagnostic: true,
		}

		source := tt.inputSource
		path := writeFixture(t, t.TempDir(), "invalid.go", source)
		var diagnostics bytes.Buffer
		if code := run([]string{"-fix", path}, &diagnostics); code != tt.wantExitCode || (diagnostics.Len() > 0) != tt.wantDiagnostic {
			t.Fatalf("Invalid Go was accepted: %d, %s", code, &diagnostics)
		}
		after, _ := os.ReadFile(path)
		if string(after) != source {
			t.Fatal("Invalid source was overwritten")
		}
		if code := run([]string{path + ".missing"}, &diagnostics); code != tt.wantExitCode {
			t.Fatalf("Missing source was accepted: %d", code)
		}
	})
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
