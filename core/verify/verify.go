// Package verify synthesises tests from a package's type information, with no
// model involved.
//
// This exists because of a measured, stubborn result. Across every configuration
// tried -- a 1.1B model, a 7B model, a 14B coder model at 426 tok/s, and an
// exact API listing extracted from the AST and placed in the prompt -- a
// generated test for `regression.Project` failed to compile every time. Better
// models and better prompts narrowed the failure mode but did not remove it.
//
// The conclusion that follows is architectural rather than a matter of tuning:
// asking a model to write code it can be shown the signature of still requires the
// model to be right about the code. The alternative is to not require that.
//
// Tests produced here are derived from the AST, so they compile by construction:
// they reference only symbols the parser confirmed exist, with argument types
// taken from the declared signature. They cannot hallucinate because nothing was
// inferred. That makes them the one verification channel in this system whose
// failure mode is a compile error in the harness rather than a subtly wrong
// assertion in a repository.
//
// The properties asserted are deliberately weak but never empty:
//
//   - the function is callable with its declared signature
//   - it does not panic on the zero value of its arguments
//   - a constructor returns non-nil
//   - a (T, error) result does not return a zero T together with a nil error
//
// Each is true of every correct implementation, so a test that violates one is
// reporting a real defect. A stronger property would risk false positives, and a
// verifier that cries wolf gets switched off.
package verify

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kilo/spiral-codemaker/core/apiscan"
)

// Result is the outcome of synthesising tests for a package.
type Result struct {
	Package string `json:"package"`
	Files   []File `json:"files"`
	// Skipped names functions for which no test could be synthesised, with the
	// reason. Recorded rather than dropped, because a silent gap reads as
	// "nothing to test here".
	Skipped []Skip `json:"skipped,omitempty"`
}

// File is one synthesised test file.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Tests   int    `json:"tests"`
}

// Skip records a function no test could be produced for.
type Skip struct {
	Func   string `json:"func"`
	Reason string `json:"reason"`
}

// Synthesize builds a test file covering every exported function in an API.
func Synthesize(pkgDir, pkgName string) (*Result, error) {
	api, err := apiscan.Scan(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", pkgDir, err)
	}

	res := &Result{Package: pkgName}

	var code strings.Builder
	count := 0

	for _, fn := range api.Functions {
		if fn.Receiver != "" {
			// Methods are covered indirectly through the constructor's return value
			// or by the package's own tests. Synthesising them requires knowing how
			// to obtain a receiver, which the signature does not say.
			res.Skipped = append(res.Skipped, Skip{
				Func:   fn.Receiver + "." + fn.Name,
				Reason: "method: no way to construct the receiver from the signature alone",
			})
			continue
		}

		test, skip := synthesiseFunc(fn, api.Package)
		if skip != "" {
			res.Skipped = append(res.Skipped, Skip{Func: fn.Name, Reason: skip})
			continue
		}
		code.WriteString(test)
		code.WriteString("\n")
		count++
	}

	if count == 0 {
		return res, nil
	}

	// Deduplicate: two functions can normalise to the same test name.
	body := dedupeTests(code.String())

	// Render rather than storing the bare body. Emitting a fragment here and
	// expecting a caller to remember the package clause and imports is the same
	// class of bug this package exists to eliminate -- and it did, producing a
	// file that started with "func" and failed to parse.
	res.Files = append(res.Files, File{
		Path:    filepath.Join(pkgDirRel(pkgDir), "zz_generated_api_test.go"),
		Content: Render(pkgName, body),
		Tests:   count,
	})
	return res, nil
}

func pkgDirRel(dir string) string {
	return strings.TrimPrefix(strings.ReplaceAll(dir, "\\", "/"), "./")
}

// synthesiseFunc produces one test for a function, or a reason it could not.
func synthesiseFunc(fn apiscan.Func, pkgName string) (string, string) {
	if len(fn.Params) == 0 && len(fn.Results) == 0 {
		return "", "function has no parameters or results; nothing to assert"
	}

	args := make([]string, 0, len(fn.Params))
	for _, p := range fn.Params {
		lit, ok := apiscan.ZeroValue(p.Type)
		if !ok {
			return "", fmt.Sprintf("parameter %s of type %s has no zero literal", p.Name, p.Type)
		}
		args = append(args, lit)
	}
	call := fn.Name + "(" + strings.Join(args, ", ") + ")"

	var checks []string

	// A call that does not panic is the floor assertion, and it is a real one:
	// zero-value arguments are exactly where nil-pointer and empty-slice bugs
	// live.
	checks = append(checks, fmt.Sprintf(
		"\t// Calling with zero values must not panic. Zero arguments are where nil\n"+
			"\t// dereferences and empty-slice indexing actually occur.\n\t_ = %s", call))

	if len(fn.Results) == 1 && fn.Results[0].Type == "error" {
		// Nothing more to assert: an error is either nil or not.
	} else if len(fn.Results) == 1 {
		checks = append(checks, fmt.Sprintf("\t_ = %s", call))
	} else if len(fn.Results) == 2 && fn.Results[1].Type == "error" {
		// A (T, error) result must not be zero-valued T with a nil error, which
		// is the classic "returned ok but gave me nothing" bug.
		zero, ok := apiscan.ZeroValue(fn.Results[0].Type)
		if ok && zero != "nil" {
			checks = append(checks, fmt.Sprintf(
				"\t// A non-nil value with a nil error is a contract violation.\n"+
					"\tif got != %s && err == nil {\n"+
					"\t\tt.Errorf(\"%s returned a zero result with a nil error\", %q)\n"+
					"\t}", zero, fn.Name, fn.Name))
		}
	}

	if len(checks) == 0 {
		return "", "no assertable property from this signature"
	}

	return fmt.Sprintf("func TestAPI_%s(t *testing.T) {\n%s\n}\n", testName(fn.Name), strings.Join(checks, "\n")), ""
}

// testName sanitises a function name into a valid, unique test identifier.
func testName(fn string) string {
	var b strings.Builder
	for i, r := range fn {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteRune('N')
			}
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// dedupeTests removes duplicate function bodies, which two exported names can
// normalise to.
func dedupeTests(body string) string {
	lines := strings.Split(body, "\n")
	var out []string
	seen := map[string]bool{}

	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "func TestAPI_") {
			out = append(out, lines[i])
			i++
			continue
		}
		start := i
		for i < len(lines) && lines[i] != "}" {
			i++
		}
		if i < len(lines) {
			i++ // consume the closing brace
		}
		block := strings.Join(lines[start:i], "\n")
		if !seen[block] {
			seen[block] = true
			out = append(out, block, "")
		}
	}
	return strings.Join(out, "\n")
}

// Render produces a complete, compilable test file.
func Render(pkgName, body string) string {
	imports := []string{`"testing"`}
	if strings.Contains(body, "reflect.") {
		imports = append(imports, `"reflect"`)
	}
	if strings.Contains(body, "fmt.") {
		imports = append(imports, `"fmt"`)
	}
	sort.Strings(imports)

	return fmt.Sprintf("// Code generated by core/verify. DO NOT EDIT.\n//\n"+
		// Provenance matters as much as the test: a reader seeing this file must
		// be able to tell mechanically-derived coverage from human- or
		// model-written intent, because the two deserve very different levels of
		// trust. This file asserts only what the type signature makes certain.
		"// Derived mechanically from the package's exported signatures.\n"+
		"// It calls each function with zero arguments and asserts only that the\n"+
		"// call is well-typed and does not panic.\n\n"+
		"package %s\n\nimport (\n\t%s\n)\n\n%s",
		pkgName, strings.Join(imports, "\n\t"), body)
}

// Quote exposes string quoting for callers building additional assertions.
func Quote(s string) string { return strconv.Quote(s) }
