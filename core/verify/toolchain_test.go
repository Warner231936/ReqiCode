package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runGoTest compiles and runs the generated tests in dir.
//
// The generated code is only worth anything if it builds, so this runs the real
// toolchain rather than asserting on strings. A synthetically-generated test that
// does not compile is worse than no test: it looks like coverage and provides
// none.
func runGoTest(t *testing.T, dir string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		// cgo is required for the race detector and is not always configured in a
		// temporary directory's environment. Skip rather than report a failure
		// that is about the machine rather than the generated code.
		if _, err := exec.LookPath("go"); err != nil {
			t.Skip("go toolchain not on PATH")
		}
	}

	cmd := exec.Command("go", "test", "./...", "-count=1")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated tests did not build or pass:\n%s\n%s", out, filepath.Base(dir))
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("expected a passing run, got:\n%s", out)
	}
}
