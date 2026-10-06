package domain

import "testing"

func TestValidImageURL(t *testing.T) {
	ok := []string{"", "/static/products/a.svg", "https://cdn.example.com/a.png", "http://example.com/x.jpg?w=200"}
	bad := []string{"//evil.example/a.png", "javascript:alert(1)", "data:image/svg+xml;base64,AAAA", "ftp://x/y", "https://", "/a\\b", "relative/path.png", "file:///etc/passwd"}
	for _, s := range ok {
		if !validImageURL(s) {
			t.Errorf("%q should be accepted", s)
		}
	}
	for _, s := range bad {
		if validImageURL(s) {
			t.Errorf("%q should be rejected", s)
		}
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{"Trail Running Shoes": "trail-running-shoes", "  A--B  ": "a-b", "জুতা": "", "Café 2": "caf-2"} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
