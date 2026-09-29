package validation

import (
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/core/state"
)

type CodeValidator struct {
	rules []ValidationRule
}

type ValidationRule struct {
	Name        string
	Description string
	Severity    string
}

func NewCodeValidator() *CodeValidator {
	v := &CodeValidator{
		rules: []ValidationRule{
			{Name: "no-unreachable", Description: "No unreachable code", Severity: "medium"},
			{Name: "no-unused-vars", Description: "No unused variables", Severity: "low"},
			{Name: "no-empty-catch", Description: "No empty error handling", Severity: "high"},
			{Name: "bounded-loops", Description: "No unbounded loops", Severity: "high"},
		},
	}
	return v
}

func (v *CodeValidator) Validate(proposal state.CodeProposal) []ValidationError {
	var errors []ValidationError
	if proposal.Operation == state.OpCreate || proposal.Operation == state.OpModify {
		if proposal.After == "" {
			errors = append(errors, ValidationError{
				Rule:     "missing-content",
				Severity: "high",
				Message:  "proposal has no content in 'after'",
			})
		}
	}
	if proposal.Confidence < 0 {
		errors = append(errors, ValidationError{
			Rule:     "invalid-confidence",
			Severity: "high",
			Message:  "confidence must be non-negative",
		})
	}
	return errors
}

type ValidationError struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func (v *CodeValidator) SecurityScan(code string) []SecurityIssue {
	var issues []SecurityIssue
	if strings.Contains(code, "eval(") && !strings.Contains(code, "// nosem") {
		issues = append(issues, SecurityIssue{
			Rule:    "eval-usage",
			Severity: "high",
			Message:  "use of eval is a security risk",
		})
	}
	if strings.Contains(code, "innerHTML") && !strings.Contains(code, "// nosem") {
		issues = append(issues, SecurityIssue{
			Rule:    "xss-risk",
			Severity: "high",
			Message:  "innerHTML assignment may cause XSS",
		})
	}
	if strings.Contains(code, "DROP TABLE") || strings.Contains(code, "DROP DATABASE") {
		issues = append(issues, SecurityIssue{
			Rule:    "sql-drop",
			Severity: "critical",
			Message:  "SQL DROP statement detected",
		})
	}
	return issues
}

type SecurityIssue struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func (v *CodeValidator) ValidateProposal(p state.CodeProposal) error {
	verrs := v.Validate(p)
	if len(verrs) > 0 {
		var msgs []string
		for _, e := range verrs {
			msgs = append(msgs, fmt.Sprintf("[%s] %s", e.Rule, e.Message))
		}
		return fmt.Errorf("validation errors: %s", strings.Join(msgs, "; "))
	}
	return nil
}
