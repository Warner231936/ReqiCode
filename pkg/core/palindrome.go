package core

import "unicode"

// IsPalindrome checks if a string reads the same forwards and backwards.
// It ignores non-alphanumeric characters.
func IsPalindrome(s string) bool {
	// Build a slice of only alphanumeric characters
	var chars []rune
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			chars = append(chars, r)
		}
	}

	// Check if the filtered string is a palindrome
	for i := 0; i < len(chars)/2; i++ {
		if chars[i] != chars[len(chars)-1-i] {
			return false
		}
	}
	return true
}