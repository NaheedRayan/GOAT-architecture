package money

import "testing"

func TestParseAndFormat(t *testing.T) {
	for in, want := range map[string]int64{"12": 1200, "12.5": 1250, "12.50": 1250, ".99": 99, "0.05": 5} {
		got, err := ParseCents(in)
		if err != nil || got != want {
			t.Errorf("ParseCents(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.234", "-1", "1.x"} {
		if _, err := ParseCents(bad); err == nil {
			t.Errorf("ParseCents(%q) should fail", bad)
		}
	}
	if got := Format(1250, "USD"); got != "$12.50" {
		t.Errorf("Format = %s", got)
	}
}
