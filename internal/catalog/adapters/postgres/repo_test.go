package postgres

import "testing"

func TestLikePatternEscapesWildcards(t *testing.T) {
	for in, want := range map[string]string{
		"tra":    "%tra%",
		"50%":    `%50\%%`,
		"a_b":    `%a\_b%`,
		`back\s`: `%back\\s%`,
		"":       "%%",
	} {
		if got := likePattern(in); got != want {
			t.Errorf("likePattern(%q) = %q, want %q", in, got, want)
		}
	}
}
