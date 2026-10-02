package modelcheck

import (
	"strings"
	"testing"
	"time"
)

func fast(success bool, kind string) Observation {
	return Observation{Success: success, Duration: 8 * time.Second, CompletionTokens: 400, RejectionKind: kind}
}

func TestAdequateModelPasses(t *testing.T) {
	var obs []Observation
	for i := 0; i < 12; i++ {
		obs = append(obs, fast(true, ""))
	}
	v := Assess("qwen2.5-14b-q4", "structured-code", obs)
	if !v.Adequate {
		t.Errorf("a model that always succeeds must be adequate: %s", strings.Join(v.Reasons, "; "))
	}
}

func TestNoObservationsIsNotInadequate(t *testing.T) {
	// Absence of evidence is not evidence of inadequacy. Reporting a model as
	// too small before it has done anything is the kind of confident wrong
	// answer this whole project is supposed to avoid.
	v := Assess("unknown", "structured-code", nil)
	if !v.Adequate {
		t.Error("a model with no observations must not be judged inadequate")
	}
	if v.Confidence != 0 {
		t.Errorf("confidence should be zero with no evidence, got %f", v.Confidence)
	}
}

func TestLowSuccessRateIsInadequate(t *testing.T) {
	var obs []Observation
	for i := 0; i < 10; i++ {
		if i < 2 {
			obs = append(obs, fast(true, ""))
		} else {
			obs = append(obs, fast(false, "build-failure"))
		}
	}
	v := Assess("small", "structured-code", obs)
	if v.Adequate {
		t.Fatal("a 20% first-attempt success rate must be inadequate")
	}
	found := false
	for _, r := range v.Reasons {
		if strings.Contains(r, "structured success") {
			found = true
		}
	}
	if !found {
		t.Errorf("reasons should name the success rate, got %v", v.Reasons)
	}
}

func TestDominantRejectionPointsAtCapabilityNotPrompt(t *testing.T) {
	// The same check failing repeatedly is the signature of a model that cannot
	// do the task, and it changes the recommendation from "fix the prompt" to
	// "escalate the model". Getting that backwards wastes the operator's time on
	// prompt tuning.
	var obs []Observation
	for i := 0; i < 8; i++ {
		obs = append(obs, fast(false, "import cycle not allowed in test"))
	}
	v := Assess("tiny", "structured-code", obs)
	if v.Adequate {
		t.Fatal("repeated identical failures must be inadequate")
	}
	if !strings.Contains(v.Recommendation, "escalate") {
		t.Errorf("a dominant failure cause must recommend escalation, got %q", v.Recommendation)
	}
}

func TestSpreadFailuresRecommendPromptReviewFirst(t *testing.T) {
	// A flat spread of different failure kinds is more consistent with a bad
	// prompt than with an undersized model, and the recommendation should say so
	// rather than blaming the hardware.
	kinds := []string{"undefined: foo", "imported and not used", "truncated response", "package clause mismatch"}
	var obs []Observation
	for i := 0; i < 8; i++ {
		obs = append(obs, fast(false, kinds[i%len(kinds)]))
	}
	v := Assess("mid", "structured-code", obs)
	if v.Adequate {
		t.Fatal("a 0% success rate is inadequate regardless of cause")
	}
	if !strings.Contains(v.Recommendation, "prompt") {
		t.Errorf("a spread of causes should recommend prompt review first, got %q", v.Recommendation)
	}
}

func TestSlowModelIsInadequate(t *testing.T) {
	var obs []Observation
	for i := 0; i < 6; i++ {
		obs = append(obs, Observation{Success: true, Duration: 200 * time.Second, CompletionTokens: 300})
	}
	v := Assess("slow", "structured-code", obs)
	if v.Adequate {
		t.Error("a model too slow to iterate must be inadequate even when it succeeds")
	}
	if v.Evidence.MedianTokensPerSecond >= 15 {
		t.Errorf("tokens/sec should be low, got %.1f", v.Evidence.MedianTokensPerSecond)
	}
}

func TestFastModelPassesSpeedChecks(t *testing.T) {
	var obs []Observation
	for i := 0; i < 6; i++ {
		obs = append(obs, Observation{Success: true, Duration: 4 * time.Second, CompletionTokens: 500})
	}
	v := Assess("quick", "structured-code", obs)
	if !v.Adequate {
		t.Errorf("a fast, reliable model must be adequate: %v", v.Reasons)
	}
}

func TestClassificationTaskDoesNotEscalateSmallModels(t *testing.T) {
	// Escalation advice that fires for work a small model already does is noise,
	// and noise is how a real warning gets ignored.
	var obs []Observation
	for i := 0; i < 10; i++ {
		if i%3 == 0 {
			obs = append(obs, fast(true, ""))
		} else {
			obs = append(obs, fast(false, "other"))
		}
	}
	v := Assess("small", "classification", obs)
	if !v.Adequate {
		t.Errorf("classification should tolerate a weak model: %v", v.Reasons)
	}
}

func TestConfidenceGrowsWithSampleSize(t *testing.T) {
	few := Assess("m", "structured-code", []Observation{fast(false, "x"), fast(false, "x"), fast(false, "x")})
	many := Assess("m", "structured-code", make([]Observation, 30))
	if many.Confidence <= few.Confidence {
		t.Errorf("confidence should grow with evidence: few=%f many=%f", few.Confidence, many.Confidence)
	}
	if many.Confidence > 1 {
		t.Errorf("confidence must be bounded, got %f", many.Confidence)
	}
}

func TestNormalizeKindAggregatesPhrasings(t *testing.T) {
	cases := map[string]string{
		"import cycle not allowed in test":                 "import-cycle",
		"references state without importing it":            "missing-import",
		"core/x.go:3:2: imported and not used":             "unused-import",
		"undefined: buildState":                            "undefined-symbol",
		"unbalanced braces, response was truncated":        "truncated",
		"a response that only mentions the package clause": "package-clause",
		"context deadline exceeded":                        "unavailable",
		"":                                                 "",
	}
	for input, want := range cases {
		if got := normalizeKind(input); got != want {
			t.Errorf("normalizeKind(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRepeatedDifferentPhrasingsAggregateIntoOneKind(t *testing.T) {
	// Two different sentences describing the same mistake must count once, or
	// the dominant-cause signal is diluted by phrasing noise.
	var obs []Observation
	obs = append(obs, fast(false, "import cycle not allowed in test"))
	obs = append(obs, fast(false, "an internal test (package regression) must not import its own package"))
	obs = append(obs, fast(false, "self import is invalid"))
	v := Assess("m", "structured-code", obs)
	if v.Evidence.RejectionsByKind["import-cycle"] != 3 {
		t.Errorf("expected three phrasings to aggregate, got %v", v.Evidence.RejectionsByKind)
	}
}

func TestFormatVerdictLeadsWithConclusion(t *testing.T) {
	var obs []Observation
	for i := 0; i < 8; i++ {
		obs = append(obs, fast(false, "undefined: x"))
	}
	out := FormatVerdict(Assess("small-model", "structured-code", obs))
	if !strings.HasPrefix(out, "INADEQUATE") {
		t.Errorf("output must lead with the verdict, got:\n%s", out)
	}
	if !strings.Contains(out, "recommendation") {
		t.Error("output must state a recommendation")
	}
	if !strings.Contains(out, "small-model") {
		t.Error("output must name the model")
	}
}

func TestFormatVerdictAdequate(t *testing.T) {
	var obs []Observation
	for i := 0; i < 6; i++ {
		obs = append(obs, fast(true, ""))
	}
	out := FormatVerdict(Assess("good-model", "structured-code", obs))
	if !strings.HasPrefix(out, "ADEQUATE") {
		t.Errorf("expected ADEQUATE, got:\n%s", out)
	}
}

func TestUnknownTaskFallsBackToStructuredCode(t *testing.T) {
	var obs []Observation
	for i := 0; i < 10; i++ {
		if i < 1 {
			obs = append(obs, fast(true, ""))
		} else {
			obs = append(obs, fast(false, "other"))
		}
	}
	v := Assess("m", "a-task-nobody-defined", obs)
	if v.Adequate {
		t.Error("an unknown task should fall back to the structured-code thresholds, not to adequacy")
	}
}
