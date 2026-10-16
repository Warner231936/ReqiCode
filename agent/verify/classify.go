package verify

import (
	"fmt"
	"regexp"
	"strings"
)

// Signatures of the distinct ways a build or test run can fail.
//
// Matching on signatures rather than on exit codes is what lets the repair loop
// decide whether a retry could possibly help. A missing dependency will not fix
// itself on an identical second attempt; a compile error is a real defect the
// agent should repair. Both exit non-zero.
var (
	goCompileErr  = regexp.MustCompile(`(?m)^.*\.go:\d+:\d+:`)
	tscErr        = regexp.MustCompile(`error TS\d+`)
	rustcErr      = regexp.MustCompile(`(?m)^error\[E\d+\]`)
	cargoFailed   = regexp.MustCompile(`could not compile|error: aborting|error: could not find`)
	dotnetErr     = regexp.MustCompile(`error [A-Z]{2,}\d+`)
	javacErr      = regexp.MustCompile(`(?m)^.*\.java:\d+: error`)
	gccErr        = regexp.MustCompile(`(?m)^(.*\.[ch]:\d+:\d+: )?error: `)
	pythonTrace   = regexp.MustCompile(`Traceback \(most recent call last\)`)
	assertFailed  = regexp.MustCompile(`(?i)\b(assertionerror|assert failed|assertion failed|assertion ` + "`" + `failed)\b`)
	panicked      = regexp.MustCompile(`(?i)panic:|fatal error:|segmentation fault|nil pointer dereference|access violation`)
	noSuchModule  = regexp.MustCompile(`(?i)(no required module provides|cannot find module|module not found|unresolved import|is not in std|cannot find package|no such file or directory: .*\.(go|py|js|ts))`)
	missingTool   = regexp.MustCompile(`(?i)(executable file not found|command not found|is not recognized as an internal|no such file or directory|not found on your system|cannot find the path)`)
	permissionFai = regexp.MustCompile(`(?i)(permission denied|access is denied|unauthorized|EACCES|operation not permitted)`)
	networkFail   = regexp.MustCompile(`(?i)(connection refused|no such host|network is unreachable|temporary failure in name resolution|dial tcp|502 bad gateway|503 service|EOF)`)
	testSummaryF  = regexp.MustCompile(`(?i)(\d+ (failed|failures?)|FAILED|--- FAIL:|^FAIL|^not ok|tests? failed)`)
	lintF         = regexp.MustCompile(`(?i)(\d+ (problems?|errors?|issues?)|[a-z_.]+:\d+:\d+:[^ ])`)
)

// ClassifyFailure assigns a kind to a failed command.
func ClassifyFailure(command, stdout, stderr string, exitCode int) Failure {
	combined := stderr + "\n" + stdout

	switch {
	case missingTool.MatchString(combined):
		return Failure{Kind: FailureMissingTool,
			Message: firstMatchLine(missingTool, combined), Retryable: false,
			Advice: "install the tool, or remove the verification command that needs it; retrying will not help"}

	case noSuchModule.MatchString(combined):
		return Failure{Kind: FailureMissingDependency,
			Message: firstMatchLine(noSuchModule, combined), Retryable: false,
			Advice: "an import or module reference is wrong; repair the code rather than retrying"}

	case networkFail.MatchString(combined):
		return Failure{Kind: FailureNetwork,
			Message: firstMatchLine(networkFail, combined), Retryable: true,
			Advice: "a dependency fetch failed; a retry may succeed once the network recovers"}

	case permissionFai.MatchString(combined):
		return Failure{Kind: FailurePermission,
			Message: firstMatchLine(permissionFai, combined), Retryable: false,
			Advice: "the process lacks permission; do not attempt to escalate privileges to work around it"}

	case panicked.MatchString(combined):
		return Failure{Kind: FailurePanic,
			Message: firstMatchLine(panicked, combined), Retryable: false,
			Advice: "the program crashed; the stack trace in the output identifies the defect"}

	case goCompileErr.MatchString(stdout) || tscErr.MatchString(combined) ||
		rustcErr.MatchString(combined) || cargoFailed.MatchString(combined) ||
		dotnetErr.MatchString(combined) || javacErr.MatchString(combined) || gccErr.MatchString(combined):
		return Failure{Kind: FailureCompile,
			Message:  firstCompileLine(combined),
			Retryable: false,
			Advice:   "the code does not compile; repair the reported errors rather than retrying the same edit"}

	case pythonTrace.MatchString(combined):
		return Failure{Kind: FailureCompile,
			Message: firstMatchLine(pythonTrace, combined), Retryable: false,
			Advice:  "the module raised an exception at import or collection time"}

	case testSummaryF.MatchString(combined) || assertFailed.MatchString(combined):
		return Failure{Kind: FailureTest,
			Message:  firstMatchLine(testSummaryF, combined),
			Retryable: false,
			Advice:   "a test failed; read the assertion and its expected value, not the summary line"}

	case lintF.MatchString(combined):
		return Failure{Kind: FailureLint,
			Message: firstMatchLine(lintF, combined), Retryable: false,
			Advice:  "lint findings must be fixed or explicitly waived"}

	case exitCode != 0:
		return Failure{Kind: FailureUnknown,
			Message:  fmt.Sprintf("exit code %d: %s", exitCode, firstLineOf(combined)),
			Retryable: true,
			Advice:   "no failure signature was recognised; read the raw output before changing anything"}

	default:
		return Failure{Kind: FailureNone, Message: "no failure detected", Retryable: false}
	}
}

func firstMatchLine(re *regexp.Regexp, s string) string {
	m := re.FindString(s)
	if m == "" {
		return ""
	}
	return firstLineOf(m)
}

// firstCompileLine finds the first compiler diagnostic.
//
// Compiler output leads with a preamble ("# package", a build banner) that names
// no defect. Taking the first line of the whole output would hand the model
// something useless, so the first line carrying a file:line reference or an
// "error" marker is preferred.
func firstCompileLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		switch {
		case goCompileErr.MatchString(t), tscErr.MatchString(t), rustcErr.MatchString(t),
			dotnetErr.MatchString(t), javacErr.MatchString(t), gccErr.MatchString(t),
			cargoFailed.MatchString(t):
			return t
		}
	}
	return firstLineOf(output)
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 240 {
		return s[:240] + "..."
	}
	return strings.TrimSpace(s)
}