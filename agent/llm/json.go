package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ExtractJSON finds and decodes the first JSON value in a model's output.
//
// Local models are not reliable about emitting bare JSON: they wrap it in prose,
// wrap it in a Markdown fence, emit a preamble followed by the object, or emit
// several objects because they continued the imagined conversation. Each of those
// happens routinely with 7B-class models, and a parser that rejects them would
// make small local models unusable for structured work.
//
// The decoder therefore tries, in order: the whole string, a fenced block, the
// first balanced top-level object or array, and the last such object. The last
// case matters because a model that emits an example object followed by its real
// answer produces the answer last.
func ExtractJSON(text string, target any) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return fmt.Errorf("llm: empty response to decode as JSON")
	}

	// 1. The whole string, if it happens to be valid JSON.
	if err := json.Unmarshal([]byte(trimmed), target); err == nil {
		return nil
	}

	// 2. Fenced blocks, preferring the last one.
	for _, block := range fencedBlocks(trimmed) {
		if err := json.Unmarshal([]byte(block), target); err == nil {
			return nil
		}
	}

	// 3. Balanced top-level objects and arrays, in order and then reversed.
	candidates := balancedCandidates(trimmed)
	for _, c := range candidates {
		if err := json.Unmarshal([]byte(c), target); err == nil {
			return nil
		}
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		if err := json.Unmarshal([]byte(candidates[i]), target); err == nil {
			return nil
		}
	}

	return fmt.Errorf("llm: no valid JSON object found in %d bytes of model output; first 200 chars: %q",
		len(text), truncate(strings.TrimSpace(text), 200))
}

// ExtractObject decodes into a map, which is what a tool-call decision is.
func ExtractObject(text string) (map[string]any, error) {
	out := map[string]any{}
	if err := ExtractJSON(text, &out); err != nil {
		return nil, err
	}
	return out, nil
}

var fenceRe = regexp.MustCompile("(?s)```(?:[a-zA-Z0-9_+-]*)\r?\n(.*?)```")

func fencedBlocks(s string) []string {
	matches := fenceRe.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// balancedCandidates returns every balanced top-level {...} or [...] span, using
// a scanner that understands string literals and escapes.
//
// A naive brace counter breaks on JSON containing braces inside string values,
// which model output does constantly (file paths, code snippets, error messages).
// Tracking whether a brace is inside a string is what makes this usable.
func balancedCandidates(s string) []string {
	var out []string
	// stack holds the indices of currently-open brackets. The top entry is the
	// index of the bracket that will close the span being accumulated, which is
	// what lets a completed top-level span be sliced exactly.
	var stack []int
	inStr := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			stack = append(stack, i)
		case '}', ']':
			if len(stack) == 0 {
				continue
			}
			open := s[stack[len(stack)-1]]
			if (open == '{' && c == '}') || (open == '[' && c == ']') {
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					out = append(out, s[open:i+1])
				}
			} else {
				// Mismatched nesting: abandon rather than emit nonsense.
				stack = nil
			}
		}
	}
	return out
}