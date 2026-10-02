// Package modelcheck judges whether the configured model is adequate for the
// work it is being asked to do, and says so before a long run wastes hours.
//
// The failure this exists to prevent is specific and was observed: a 7B model
// produced a self-improvement test for 5 consecutive attempts, each attempt
// failing for a different type-level reason, and nothing in the system said the
// model was the problem. The operator had to infer it from the failure pattern.
//
// That inference should not be necessary. A system that measures its own
// generations already has the evidence -- rejection rate, near-miss rate, tokens
// per second, structured-output success -- and a model whose structured output
// keeps failing validation is telling you it is too small for the task. This
// package turns that into an explicit verdict.
package modelcheck

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Verdict is the assessment of one model's fitness for a task.
type Verdict struct {
	Model string `json:"model"`

	// Adequate is the headline. False means escalate before spending more compute.
	Adequate bool `json:"adequate"`
	// Confidence in the assessment itself, low when the sample is small.
	Confidence float64 `json:"confidence"`
	// Reasons are the specific observed signals, most severe first.
	Reasons []string `json:"reasons"`
	// Recommendation names what to do about it.
	Recommendation string `json:"recommendation"`
	// Evidence is the measured basis for the verdict.
	Evidence Evidence `json:"evidence"`
}

// Evidence is what the verdict was drawn from.
type Evidence struct {
	SampleSize int `json:"sample_size"`
	Successes  int `json:"successes"`
	Failures   int `json:"failures"`
	// RejectionsByKind counts validation failures by cause. A dominant single
	// cause points at a capability gap; a flat spread points at a prompt problem.
	RejectionsByKind map[string]int `json:"rejections_by_kind"`
	// MedianTokensPerSecond is the generation rate. Below roughly 15, a
	// single-file generation exceeds a ninety-second budget and iteration
	// becomes impractical regardless of quality.
	MedianTokensPerSecond float64 `json:"median_tokens_per_second"`
	// MedianSecondsPerCall is the end-to-end latency a caller waits.
	MedianSecondsPerCall float64 `json:"median_seconds_per_call"`
	// StructuredSuccessRate is the fraction of generations that passed
	// validation without needing a retry. This is the signal that separates "the
	// model is too small" from "the prompt is bad": a capable model at a fixed
	// prompt succeeds first time; an incapable one retries the same way.
	StructuredSuccessRate float64 `json:"structured_success_rate"`
}

// Task describes what the model is being asked to do.
type Task struct {
	Name string `json:"name"`
	// MinStructuredSuccess is the first-attempt pass rate below which the model is
	// considered inadequate for structured output.
	MinStructuredSuccess float64 `json:"min_structured_success"`
	// MinTokensPerSecond is the generation rate below which iteration is
	// impractical. Zero disables the check.
	MinTokensPerSecond float64 `json:"min_tokens_per_second"`
	// MaxSecondsPerCall is the latency above which a single generation is
	// impractical inside a retry loop. Zero disables the check.
	MaxSecondsPerCall float64 `json:"max_seconds_per_call"`
	// RecommendedParams is the parameter count the recommendation will name when
	// it decides the model is undersized.
	RecommendedParams string `json:"recommended_params"`
}

// DefaultTasks are the roles the system actually uses a model for.
var DefaultTasks = map[string]Task{
	"structured-code": {
		Name:                 "structured-code",
		MinStructuredSuccess: 0.50,
		MinTokensPerSecond:   15,
		MaxSecondsPerCall:    120,
		RecommendedParams:    "14B+",
	},
	"architecture-planning": {
		Name:                 "architecture-planning",
		MinStructuredSuccess: 0.40,
		MinTokensPerSecond:   10,
		MaxSecondsPerCall:    180,
		RecommendedParams:    "14B+",
	},
	// Classification and routing are near-trivial for any model above 1B; the
	// thresholds are low deliberately so a small model is not told to escalate
	// for work it can already do.
	"classification": {
		Name:                 "classification",
		MinStructuredSuccess: 0.20,
		MinTokensPerSecond:   0,
		MaxSecondsPerCall:    0,
		RecommendedParams:    "7B is sufficient",
	},
}

// Observation is one recorded generation outcome, fed in by the caller.
type Observation struct {
	// Success is true when the generation passed every validation gate without
	// needing a retry.
	Success bool
	// Duration is the end-to-end call latency.
	Duration time.Duration
	// CompletionTokens is how much the model actually produced.
	CompletionTokens int
	// RejectionKind names the validation failure, empty on success. Kinds are
	// matched case-insensitively against known signatures.
	RejectionKind string
}

// Assess judges a model against a task from a batch of observations.
//
// The assessment is deliberately conservative about *why*: a high failure rate
// is reported as insufficient evidence of capability, not as proof of
// inadequacy, because a bad prompt produces the same symptom. What the verdict
// does assert is that escalating is cheaper than continuing, which is true
// regardless of the cause.
func Assess(model, taskName string, obs []Observation) Verdict {
	v := Verdict{Model: model}
	task, ok := DefaultTasks[taskName]
	if !ok {
		task = DefaultTasks["structured-code"]
		task.Name = taskName
	}

	v.Evidence = summarize(obs)
	if len(obs) == 0 {
		v.Confidence = 0
		v.Adequate = true // nothing observed is not evidence of inadequacy
		v.Reasons = []string{"no observations yet"}
		v.Recommendation = "collect observations before judging"
		return v
	}

	// Confidence in the verdict scales with sample size and saturates, because
	// three failures justify a warning and three hundred justify a conclusion.
	v.Confidence = min(1.0, float64(len(obs))/12.0)

	var severe []string

	if rate := v.Evidence.StructuredSuccessRate; rate < task.MinStructuredSuccess {
		severe = append(severe, fmt.Sprintf(
			"first-attempt structured success %.0f%% is below the %.0f%% needed for %s",
			rate*100, task.MinStructuredSuccess*100, task.Name))
	}

	// A dominant single rejection cause points at a capability gap rather than a
	// prompt problem, and the recommendation differs: fix the prompt versus
	// escalate the model. Infrastructure failures are excluded, because a
	// repeated timeout says nothing about the model's reasoning.
	dominant, dominantCount := dominantKind(v.Evidence.RejectionsByKind)
	if dominant == "unavailable" {
		dominantCount = 0
	}
	if dominantCount > 0 && float64(dominantCount)/float64(len(obs)) > 0.6 {
		severe = append(severe, fmt.Sprintf(
			"%.0f%% of attempts failed the same check (%s), which indicates a capability limit rather than a prompt problem",
			100*float64(dominantCount)/float64(len(obs)), dominant))
	}

	if task.MinTokensPerSecond > 0 && v.Evidence.MedianTokensPerSecond > 0 &&
		v.Evidence.MedianTokensPerSecond < task.MinTokensPerSecond {
		severe = append(severe, fmt.Sprintf(
			"generation runs at %.1f tok/s, below the %.0f tok/s needed to iterate",
			v.Evidence.MedianTokensPerSecond, task.MinTokensPerSecond))
	}

	if task.MaxSecondsPerCall > 0 && v.Evidence.MedianSecondsPerCall > float64(task.MaxSecondsPerCall) {
		severe = append(severe, fmt.Sprintf(
			"a single generation takes %.0fs, over the %.0fs budget for a retry loop",
			v.Evidence.MedianSecondsPerCall, task.MaxSecondsPerCall))
	}

	// A timeout or an unreachable provider is an infrastructure symptom, not a
	// statement about model capability. Attributing it to the model would send the
	// operator to buy a bigger one when the actual problem is a busy server, a
	// truncated prompt, or a client timeout that is simply too short.
	if unavailable := v.Evidence.RejectionsByKind["unavailable"]; unavailable > 0 {
		severe = append(severe, fmt.Sprintf(
			"%d of %d attempts never reached a usable response (timeout or provider unavailable); "+
				"that is an infrastructure symptom rather than a model capability limit",
			unavailable, len(obs)))
	}

	v.Reasons = severe
	v.Adequate = len(severe) == 0
	if v.Adequate {
		v.Recommendation = "model is adequate for " + task.Name
		return v
	}

	// Do not recommend a model the operator is already running. "Escalate to a
	// 14B+ model" while assessing a 14B model is worse than useless: it reads as
	// a system that has not noticed what it is doing. When the recommended size is
	// already in use, the honest conclusion is that something other than model
	// size is the binding constraint.
	alreadyRecommended := sizeAtLeast(model, task.RecommendedParams)
	if dominantCount > 0 && float64(dominantCount)/float64(len(obs)) > 0.6 {
		if alreadyRecommended {
			v.Recommendation = fmt.Sprintf(
				"already at the recommended size for %s; the dominant failure is not a size problem. "+
					"Check that the failure kind is a real model limitation and not a server or timeout issue "+
					"(here: %s)", task.Name, dominant)
		} else {
			v.Recommendation = fmt.Sprintf(
				"escalate to a %s model for %s; the repeated failure is a capability limit, not a prompt problem",
				task.RecommendedParams, task.Name)
		}
	} else if alreadyRecommended {
		v.Recommendation = fmt.Sprintf(
			"already at the recommended size for %s; improve the prompt or the task framing before escalating",
			task.Name)
	} else {
		v.Recommendation = fmt.Sprintf(
			"review the prompt first, then escalate to %s if failures persist",
			task.RecommendedParams)
	}
	return v
}

// sizeAtLeast reports whether a model file appears to be at least as large as the
// recommended parameter count.
//
// Parsed from the filename because that is where model size is recorded for GGUF
// files, and a wrong answer here produces the circular recommendation above.
// Unrecognised names return false, so an unknown model is still told to
// escalate rather than being falsely reassured.
func sizeAtLeast(model, recommended string) bool {
	params := parseParamCount(recommended)
	if params == 0 {
		return false
	}
	have := parseParamCount(model)
	if have == 0 {
		return false
	}
	return have >= params
}

// parseParamCount reads a parameter count out of a model name or a
// recommendation such as "Qwen2.5-Coder-14B-Instruct-Q4_K_M.gguf" or "14B".
//
// A digit run immediately followed by B is required. Taking the text before the
// first "b" instead -- the obvious implementation -- yields "qwen2.5-coder-14"
// for that filename, which does not parse, so every real model name was
// reported as unknown and the system cheerfully recommended escalating past the
// model it was already running.
var paramPattern = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*[bB]`)

func parseParamCount(s string) float64 {
	m := paramPattern.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	return v
}

func summarize(obs []Observation) Evidence {
	e := Evidence{
		SampleSize:       len(obs),
		RejectionsByKind: map[string]int{},
	}

	var rates []float64
	var latencies []float64
	firstTry := 0

	for _, o := range obs {
		// Latency and rate are collected for *every* observation, not only
		// failures. An earlier version recorded them only for failed calls, which
		// meant a model that always succeeded but far too slowly to iterate
		// reported no rate at all and was judged ADEQUATE -- the exact case the
		// speed check exists to catch.
		if o.Duration > 0 {
			latencies = append(latencies, o.Duration.Seconds())
		}
		if o.CompletionTokens > 0 && o.Duration > 0 {
			rates = append(rates, float64(o.CompletionTokens)/o.Duration.Seconds())
		}

		if o.Success {
			e.Successes++
			firstTry++
			continue
		}
		e.Failures++
		kind := normalizeKind(o.RejectionKind)
		if kind != "" {
			e.RejectionsByKind[kind]++
		}
	}

	if len(obs) > 0 {
		e.StructuredSuccessRate = float64(firstTry) / float64(len(obs))
	}
	e.MedianTokensPerSecond = median(rates)
	e.MedianSecondsPerCall = median(latencies)
	return e
}

// normalizeKind maps a free-text rejection onto a stable kind so repeated causes
// aggregate instead of scattering across phrasings.
func normalizeKind(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	switch {
	case strings.Contains(s, "import cycle") || strings.Contains(s, "self import") ||
		strings.Contains(s, "own package") || strings.Contains(s, "must not import"):
		return "import-cycle"
	case strings.Contains(s, "without importing") || strings.Contains(s, "missing import"):
		return "missing-import"
	case strings.Contains(s, "imported and not used"):
		return "unused-import"
	case strings.Contains(s, "undefined:"):
		return "undefined-symbol"
	case strings.Contains(s, "declared and not used"):
		return "unused-variable"
	case strings.Contains(s, "truncated") || strings.Contains(s, "unbalanced"):
		return "truncated"
	case strings.Contains(s, "no package clause") || strings.Contains(s, "package clause"):
		return "package-clause"
	case strings.Contains(s, "placeholder") || strings.Contains(s, "echo"):
		return "prompt-echo"
	case strings.Contains(s, "does not reference") || strings.Contains(s, "not cover"):
		return "off-target"
	case strings.Contains(s, "not compile") || strings.Contains(s, "build") || strings.Contains(s, "syntax"):
		return "build-failure"
	case strings.Contains(s, "deadline") || strings.Contains(s, "timeout") || strings.Contains(s, "not available"):
		return "unavailable"
	case strings.Contains(s, "no usable"):
		return "no-source"
	}
	return "other"
}

func dominantKind(counts map[string]int) (string, int) {
	best, bestN := "", 0
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if counts[k] > bestN {
			best, bestN = k, counts[k]
		}
	}
	return best, bestN
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// FormatVerdict renders an assessment for a terminal, leading with the headline
// so the reader learns the conclusion before the reasoning.
func FormatVerdict(v Verdict) string {
	var b strings.Builder
	headline := "ADEQUATE"
	if !v.Adequate {
		headline = "INADEQUATE"
	}
	fmt.Fprintf(&b, "%s: %s (%d observations, confidence %.0f%%)\n",
		headline, v.Model, v.Evidence.SampleSize, v.Confidence*100)

	if v.Evidence.SampleSize > 0 {
		fmt.Fprintf(&b, "  first-attempt success: %.0f%% (%d ok, %d failed)\n",
			v.Evidence.StructuredSuccessRate*100, v.Evidence.Successes, v.Evidence.Failures)
		if v.Evidence.MedianTokensPerSecond > 0 {
			fmt.Fprintf(&b, "  generation rate:         %.1f tok/s, %.0fs per call\n",
				v.Evidence.MedianTokensPerSecond, v.Evidence.MedianSecondsPerCall)
		}
		if len(v.Evidence.RejectionsByKind) > 0 {
			kinds := make([]string, 0, len(v.Evidence.RejectionsByKind))
			for k := range v.Evidence.RejectionsByKind {
				kinds = append(kinds, k)
			}
			sort.Strings(kinds)
			parts := make([]string, 0, len(kinds))
			for _, k := range kinds {
				parts = append(parts, fmt.Sprintf("%s=%d", k, v.Evidence.RejectionsByKind[k]))
			}
			fmt.Fprintf(&b, "  failure kinds:           %s\n", strings.Join(parts, " "))
		}
	}

	for _, r := range v.Reasons {
		fmt.Fprintf(&b, "  - %s\n", r)
	}
	fmt.Fprintf(&b, "  recommendation: %s\n", v.Recommendation)
	return b.String()
}
