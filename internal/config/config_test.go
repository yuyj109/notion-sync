package config

import "testing"

func TestNotionID(t *testing.T) {
	for _, tt := range []struct {
		input string
		valid bool
	}{
		{"01234567-89ab-cdef-0123-456789abcdef", true},
		{"0123456789ABCDEF0123456789ABCDEF", true},
		{"01234567-89ab-cdef-0123-456789abcdeg", false},
		{"", false},
	} {
		if got := ValidID(tt.input); got != tt.valid {
			t.Errorf("ValidID(%q) = %t", tt.input, got)
		}
	}
	if got := NormalizeID("01234567-89AB-CDEF-0123-456789ABCDEF"); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("NormalizeID = %q", got)
	}
}

func TestFormatID(t *testing.T) {
	for _, input := range []string{"0123456789ABCDEF0123456789ABCDEF", "01234567-89ab-cdef-0123-456789abcdef"} {
		if got := FormatID(input); got != "01234567-89ab-cdef-0123-456789abcdef" {
			t.Errorf("FormatID(%q) = %q", input, got)
		}
	}
	if got := FormatID("invalid"); got != "invalid" {
		t.Errorf("invalid input changed: %q", got)
	}
}
