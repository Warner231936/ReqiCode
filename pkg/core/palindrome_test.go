package core

import "testing"

func TestIsPalindrome(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"empty string", "", true},
		{"single char", "a", true},
		{"two same chars", "aa", true},
		{"two diff chars", "ab", false},
		{"palindrome", "racecar", true},
		{"palindrome with spaces", "race car", true},
		{"not palindrome", "hello", false},
		{"not palindrome 2", "hello world", false},
		{"single letter palindrome", "a", true},
		{"numbers", "12321", true},
		{"not numbers", "12345", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPalindrome(tt.input)
			if result != tt.expected {
				t.Errorf("IsPalindrome(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}