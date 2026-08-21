package main

import (
	"strings"
	"testing"
)

// Call-signs travel in "|"-delimited packets and through the shell, so the
// separators and control characters have to come out.
func TestSanitizeCallSign(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Falcon", "Falcon"},
		{"  Falcon  ", "Falcon"},
		{"Kevin|Ops", "KevinOps"},
		{"line\nbreak", "linebreak"},
		{"carriage\rreturn", "carriagereturn"},
		{"bell\x07here", "bellhere"},
		{"", ""},
		{"   ", ""},
		{strings.Repeat("x", 64), strings.Repeat("x", 32)},
	}
	for _, c := range cases {
		if got := sanitizeCallSign(c.in); got != c.want {
			t.Errorf("sanitizeCallSign(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A sanitized call-sign must never contain a field separator, whatever went
// in — that is what stops a name forging packet framing.
func TestSanitizeCallSignNeverKeepsSeparators(t *testing.T) {
	for _, in := range []string{"a|b|c", "|||", "on\nair|x", "\x00|\x1f"} {
		got := sanitizeCallSign(in)
		if strings.ContainsAny(got, "|\r\n") {
			t.Errorf("sanitizeCallSign(%q) = %q, still carries a separator", in, got)
		}
	}
}

func TestRandomCallSignAvoidsTaken(t *testing.T) {
	taken := make(map[string]bool, len(callSignWords))
	for _, w := range callSignWords {
		taken[strings.ToLower(w)] = true
	}
	// Every word is spoken for, so it must fall back to a suffixed name
	// rather than hand out a duplicate or an empty string.
	got := randomCallSign(taken)
	if got == "" {
		t.Fatal("randomCallSign returned empty with a full net")
	}
	if !strings.Contains(got, "-") {
		t.Errorf("randomCallSign(full) = %q, want a disambiguating suffix", got)
	}

	// With one word free, it should find it rather than suffix.
	delete(taken, strings.ToLower(callSignWords[0]))
	for i := 0; i < 200; i++ {
		if randomCallSign(taken) == callSignWords[0] {
			return
		}
	}
	t.Errorf("randomCallSign never found the one free word %q", callSignWords[0])
}

func TestCallSignWordsAreClean(t *testing.T) {
	seen := make(map[string]bool, len(callSignWords))
	for _, w := range callSignWords {
		if sanitizeCallSign(w) != w {
			t.Errorf("word %q does not survive sanitizing", w)
		}
		if seen[strings.ToLower(w)] {
			t.Errorf("duplicate word %q in the list", w)
		}
		seen[strings.ToLower(w)] = true
	}
	if len(callSignWords) < 64 {
		t.Errorf("only %d words — too many collisions on a busy net", len(callSignWords))
	}
}
